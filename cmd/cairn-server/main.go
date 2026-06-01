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
//	LEDGER_GRPC_ADDR  ledger gRPC address for PR-as-issue (default ledger.cwb.svc:8081)
//	CAIRN_TLS_CERT    path to cairn's client TLS certificate (PEM)
//	CAIRN_TLS_KEY     path to cairn's client TLS private key (PEM)
//	CAIRN_TLS_CA      path to the cwb-ca certificate (PEM) for ledger mTLS
//	CAIRN_PUBLIC_BASE optional public base URL for PR/ExternalRef links ("" omits)
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
	"path/filepath"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/herald"
	"github.com/CarriedWorldUniverse/cairn/internal/httpd"
	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
	"github.com/CarriedWorldUniverse/cairn/internal/protect"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
	"github.com/CarriedWorldUniverse/cairn/internal/sshd"
	gossh "golang.org/x/crypto/ssh"
)

func main() {
	// Hidden subcommand invoked by the per-repo pre-receive hook.
	if len(os.Args) >= 3 && os.Args[1] == "pre-receive" {
		os.Exit(runPreReceive(os.Args[2]))
	}

	httpAddr := env("CAIRN_HTTP_ADDR", ":8100")
	dbPath := env("CAIRN_DB", "/var/lib/nexus/cairn.db")
	repoRoot := env("CAIRN_REPO_ROOT", "/var/lib/nexus/repos")

	core, err := repo.Open(dbPath, repoRoot)
	if err != nil {
		log.Fatalf("cairn: open core: %v", err)
	}
	defer core.Close()

	// Install the per-repo pre-receive protection hook at repo creation. The
	// hook shells back into this same binary's pre-receive subcommand.
	selfPath, err := os.Executable()
	if err != nil {
		log.Fatalf("cairn: resolve own path: %v", err)
	}
	core.SetHookInstaller(func(repoID, hooksDir string) error {
		if err := os.MkdirAll(hooksDir, 0o755); err != nil {
			return err
		}
		hook := filepath.Join(hooksDir, "pre-receive")
		return os.WriteFile(hook, []byte(protect.HookScript(selfPath, repoID)), 0o755)
	})

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

	// ledger client for PR-as-issue: cairn opens a tracking issue on PR open,
	// forwarding the opener's identity as cwb-* gRPC metadata over mTLS.
	ledgerAddr := env("LEDGER_GRPC_ADDR", "ledger.cwb.svc:8081")
	ledgerCert := env("CAIRN_TLS_CERT", "/etc/cairn/tls/tls.crt")
	ledgerKey := env("CAIRN_TLS_KEY", "/etc/cairn/tls/tls.key")
	ledgerCA := env("CAIRN_TLS_CA", "/etc/cairn/tls/ca.crt")
	ledgerCli, err := ledgerclient.NewClient(ledgerAddr, ledgerCert, ledgerKey, ledgerCA)
	if err != nil {
		log.Fatalf("cairn: ledger client: %v", err)
	}
	defer ledgerCli.Close()
	publicBase := env("CAIRN_PUBLIC_BASE", "") // optional; "" omits ExternalRef.url

	httpSrv := httpd.New(httpd.Config{Core: core, Ledger: ledgerCli, PublicBase: publicBase})
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
