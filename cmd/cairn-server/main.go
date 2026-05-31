// Command cairn-server is cairn's agent-git host: a go-git-backed git server
// with two herald-authed ingresses — SSH (casket identity) and HTTP Smart-HTTP
// behind interchange-gateway. This file wires config + the repo core +
// /healthz; the ingresses are added by the SSH and HTTP tasks.
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
	"log"
	"net/http"
	"os"

	"github.com/CarriedWorldUniverse/cairn/internal/repo"
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"cairn"}`))
	})

	log.Printf("cairn listening on %s (db=%s, repos=%s)", httpAddr, dbPath, repoRoot)
	if err := http.ListenAndServe(httpAddr, mux); err != nil {
		log.Fatalf("cairn: %v", err)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
