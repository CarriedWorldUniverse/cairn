// Command cairn-server is cairn's agent-git host: a go-git-backed git server
// with two herald-authed ingresses — SSH (casket identity) and HTTP Smart-HTTP
// behind interchange-gateway. This file wires config + the repo core +
// both ingresses + /healthz.
//
// Config (env):
//
//	CAIRN_HTTP_ADDR   HTTP listen address (default :8100)
//	CAIRN_SSH_ADDR    SSH listen address  (default :2222)
//	CAIRN_DB          sqlite catalogue path (default /var/lib/nexus/cairn.db)
//	CAIRN_REPO_ROOT   bare-repo storage dir (default /var/lib/nexus/repos)
//	CAIRN_HOST_KEY    base64(std) Ed25519 private host key for SSH (required for SSH)
//	HERALD_BASE_URL   herald base URL for the by-fingerprint lookup (NEX-412)
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/herald"
	"github.com/CarriedWorldUniverse/cairn/internal/httpd"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
	"github.com/CarriedWorldUniverse/cairn/internal/sshd"
	gossh "golang.org/x/crypto/ssh"
)

func main() {
	httpAddr := env("CAIRN_HTTP_ADDR", ":8100")
	dbPath := env("CAIRN_DB", "/var/lib/nexus/cairn.db")
	repoRoot := env("CAIRN_REPO_ROOT", "/var/lib/nexus/repos")

	core, err := repo.Open(dbPath, repoRoot)
	if err != nil {
		log.Fatalf("cairn: open core: %v", err)
	}
	defer core.Close()

	// herald identity for the SSH path: real NEX-412 client behind a short-TTL
	// cache. Until NEX-412 is deployed this resolves nothing (404 -> auth fail);
	// point HERALD_BASE_URL at herald once NEX-412 ships.
	heraldBase := env("HERALD_BASE_URL", "http://herald.cwb.svc:8099")
	agents := herald.NewCachedAgents(herald.NewHeraldClient(heraldBase, nil), 30*time.Second)

	// SSH ingress (parallel, not gateway-fronted).
	sshAddr := env("CAIRN_SSH_ADDR", ":2222")
	hostSigner, err := loadHostKey()
	if err != nil {
		log.Fatalf("cairn: ssh host key: %v", err)
	}
	sshSrv := sshd.New(sshd.Config{Core: core, Agents: agents, HostSigner: hostSigner})
	sshLn, err := net.Listen("tcp", sshAddr)
	if err != nil {
		log.Fatalf("cairn: ssh listen %s: %v", sshAddr, err)
	}
	go func() {
		log.Printf("cairn ssh listening on %s", sshAddr)
		if err := sshSrv.Serve(sshLn); err != nil {
			log.Fatalf("cairn: ssh: %v", err)
		}
	}()

	httpSrv := httpd.New(httpd.Config{Core: core})
	log.Printf("cairn http listening on %s (db=%s, repos=%s)", httpAddr, dbPath, repoRoot)
	if err := http.ListenAndServe(httpAddr, httpSrv.Handler()); err != nil {
		log.Fatalf("cairn: %v", err)
	}
}

// loadHostKey loads cairn's Ed25519 SSH host key from CAIRN_HOST_KEY
// (base64-std of the 64-byte private key). Generated + stored in a k3s Secret
// by deploy; required so the host identity is stable across restarts.
func loadHostKey() (gossh.Signer, error) {
	b64 := os.Getenv("CAIRN_HOST_KEY")
	if b64 == "" {
		return nil, errors.New("CAIRN_HOST_KEY required (base64 ed25519 private key)")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode host key: %w", err)
	}
	return gossh.NewSignerFromKey(ed25519.PrivateKey(raw))
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
