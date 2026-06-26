// Command cairn-server is cairn's agent-git host: a go-git-backed git server
// with two herald-authed ingresses — SSH (casket identity) and HTTP Smart-HTTP
// behind interchange-gateway. This file wires config + the repo core +
// both ingresses + /healthz.
//
// Config (env):
//
//	CAIRN_HTTP_ADDR   HTTP listen address for Smart-HTTP git (default :8100)
//	CAIRN_GRPC_ADDR   gRPC listen address for the JSON API behind interchange (default :8102)
//	CAIRN_SSH_ADDR    SSH listen address  (default :2222)
//	CAIRN_DB          sqlite catalogue path (default /var/lib/nexus/cairn.db)
//	CAIRN_REPO_ROOT   bare-repo storage dir (default /var/lib/nexus/repos)
//	CAIRN_HOST_KEY    base64(std) Ed25519 private host key for SSH (required for SSH)
//	HERALD_GRPC_ADDR  herald gRPC address for the by-fingerprint lookup over mTLS
//	                  (default herald.cwb.svc:8098; uses the CAIRN_TLS_* cwb-ca pair)
//	LEDGER_GRPC_ADDR  ledger gRPC address for PR-as-issue (default ledger.cwb.svc:8081)
//	CAIRN_TLS_CERT    cairn's TLS certificate (PEM) — BOTH the gRPC API server
//	                  cert (presented to interchange) AND the client cert dialing
//	                  ledger; the cert needs server+client auth usages
//	CAIRN_TLS_KEY     cairn's TLS private key (PEM)
//	CAIRN_TLS_CA      cwb-ca certificate (PEM) — verifies ledger's server cert
//	                  (client side) AND interchange's client cert (server side)
//	CAIRN_DEV_INSECURE "1" to run the gRPC API WITHOUT mTLS (local dev only;
//	                  fatal if certs unset and this is not set)
//	CAIRN_PUBLIC_BASE optional public base URL for PR/ExternalRef links ("" omits)
//
// cairn is intentionally dual-transport: the Smart-HTTP git server (:8100)
// stays plain HTTP behind interchange's reverse-proxy (header-trust) and SSH is
// unchanged, while the JSON API moved to gRPC (:8102) over mTLS. git cannot be
// gRPC, so its transports are untouched.
package main

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/grpcapi"
	"github.com/CarriedWorldUniverse/cairn/internal/herald"
	"github.com/CarriedWorldUniverse/cairn/internal/httpd"
	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
	"github.com/CarriedWorldUniverse/cairn/internal/protect"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
	"github.com/CarriedWorldUniverse/cairn/internal/sshd"
	gossh "golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	// Hidden subcommands invoked by the per-repo git hooks.
	if len(os.Args) >= 3 && os.Args[1] == "pre-receive" {
		os.Exit(runPreReceive(os.Args[2]))
	}
	if len(os.Args) >= 3 && os.Args[1] == "post-receive" {
		os.Exit(runPostReceive(os.Args[2]))
	}
	// Operator subcommands: the embargo recipient ACL (gated serve).
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "embargo-grant":
			os.Exit(runEmbargoGrant(os.Args[2:]))
		case "embargo-revoke":
			os.Exit(runEmbargoRevoke(os.Args[2:]))
		case "embargo-recipients":
			os.Exit(runEmbargoRecipients(os.Args[2:]))
		case "gc":
			os.Exit(runGC(os.Args[2:]))
		}
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
		// pre-receive: branch protection (rejects bad pushes).
		if err := os.WriteFile(filepath.Join(hooksDir, "pre-receive"),
			[]byte(protect.HookScript(selfPath, repoID)), 0o755); err != nil {
			return err
		}
		// post-receive: segregate any pushed refs/cairn/embargo/* into the private
		// store (runs after the refs land, so objects are out of quarantine).
		post := "#!/bin/sh\nexec " + selfPath + " post-receive " + repoID + "\n"
		return os.WriteFile(filepath.Join(hooksDir, "post-receive"), []byte(post), 0o755)
	})

	// TLS material (cwb-ca mTLS): one cert serves every role — the client cert
	// dialing herald (by-fingerprint) + ledger (PR-as-issue), and the server
	// cert for cairn's own gRPC API.
	tlsCert := env("CAIRN_TLS_CERT", "/etc/cairn/tls/tls.crt")
	tlsKey := env("CAIRN_TLS_KEY", "/etc/cairn/tls/tls.key")
	tlsCA := env("CAIRN_TLS_CA", "/etc/cairn/tls/ca.crt")

	// herald identity for the SSH path: resolve a casket fingerprint -> agent via
	// herald's gRPC AgentService over mTLS, dialed DIRECTLY in-cluster (the SSH
	// flow has a pubkey, not a token, so it's an mTLS service call — NOT the JWT
	// gateway edge). Wrapped in a short-TTL cache.
	heraldGRPC := env("HERALD_GRPC_ADDR", "herald.cwb.svc:8098")
	heraldCli, err := herald.NewGRPCClient(heraldGRPC, tlsCert, tlsKey, tlsCA)
	if err != nil {
		log.Fatalf("cairn: herald client: %v", err)
	}
	defer heraldCli.Close()
	agents := herald.NewCachedAgents(heraldCli, 30*time.Second)

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
	ledgerCli, err := ledgerclient.NewClient(ledgerAddr, tlsCert, tlsKey, tlsCA)
	if err != nil {
		log.Fatalf("cairn: ledger client: %v", err)
	}
	defer ledgerCli.Close()
	publicBase := env("CAIRN_PUBLIC_BASE", "") // optional; "" omits ExternalRef.url

	// gRPC JSON API (repos/pulls/org) behind interchange over mTLS — the
	// Phase 3 successor to the HTTP API handlers.
	grpcAddr := env("CAIRN_GRPC_ADDR", ":8102")
	grpcSrv := grpc.NewServer(grpcServerOptions()...)
	grpcapi.New(core, ledgerCli, publicBase).Register(grpcSrv)
	healthSrv := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcSrv, healthSrv)
	for _, svc := range []string{"cwb.cairn.v1.RepoService", "cwb.cairn.v1.PullService", "cwb.cairn.v1.OrgService"} {
		healthSrv.SetServingStatus(svc, grpc_health_v1.HealthCheckResponse_SERVING)
	}
	grpcLn, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("cairn: grpc listen %s: %v", grpcAddr, err)
	}
	go func() {
		log.Printf("cairn grpc API listening on %s", grpcAddr)
		if err := grpcSrv.Serve(grpcLn); err != nil {
			log.Fatalf("cairn: grpc: %v", err)
		}
	}()

	// Smart-HTTP git ingress — git-only now; the JSON API moved to gRPC above.
	httpSrv := httpd.New(httpd.Config{Core: core})
	log.Printf("cairn http (git) listening on %s (db=%s, repos=%s)", httpAddr, dbPath, repoRoot)
	if err := http.ListenAndServe(httpAddr, httpSrv.Handler()); err != nil {
		log.Fatalf("cairn: %v", err)
	}
}

// grpcServerOptions builds the gRPC server options for cairn's API. With the
// CAIRN_TLS_* env set the server enforces mTLS (RequireAndVerifyClientCert
// against the cwb-ca). Insecure mode requires an explicit CAIRN_DEV_INSECURE=1
// opt-in; missing certs without it are fatal — mirrors ledger/commonplace.
func grpcServerOptions() []grpc.ServerOption {
	certFile := os.Getenv("CAIRN_TLS_CERT")
	keyFile := os.Getenv("CAIRN_TLS_KEY")
	caFile := os.Getenv("CAIRN_TLS_CA")
	if certFile == "" || keyFile == "" || caFile == "" {
		if os.Getenv("CAIRN_DEV_INSECURE") == "1" {
			log.Printf("cairn: CAIRN_DEV_INSECURE=1 — gRPC API WITHOUT mTLS (dev only)")
			return nil
		}
		log.Fatalf("cairn: gRPC mTLS required — set CAIRN_TLS_CERT/_KEY/_CA (or CAIRN_DEV_INSECURE=1 for local dev)")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.Fatalf("cairn: tls: load cert/key: %v", err)
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		log.Fatalf("cairn: tls: read CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		log.Fatalf("cairn: tls: no certs parsed from CA file %s", caFile)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}
	return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(tlsCfg))}
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
