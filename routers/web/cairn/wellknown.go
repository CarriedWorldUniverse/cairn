// Package cairn — Cairn web UI augmentations.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"text/template"
)

//go:embed templates/wellknown/*
var wellKnownAssets embed.FS

// Manifest is the wire shape of /.well-known/cairn.json.
//
// IMPORTANT: this struct must NEVER include the instance HMAC key
// or any other server secret. The fingerprint algorithm is named
// (transparency); the key remains secret.
type Manifest struct {
	CairnVersion         string         `json:"cairn_version"`
	ForgejoVersion       string         `json:"forgejo_version"`
	InstanceName         string         `json:"instance_name"`
	FingerprintAlgo      string         `json:"fingerprint_algo"`
	SigningAlgo          string         `json:"signing_algo"`
	DerivationAlgo       string         `json:"derivation_algo"`
	DerivationInfoPrefix string         `json:"derivation_info_prefix"`
	EmailConvention      string         `json:"email_convention"`
	Trailers             []string       `json:"trailers"`
	Endpoints            map[string]string `json:"endpoints"`
	Features             map[string]any `json:"features"`
}

// LLMsTxtData is the template input for the llms.txt template.
type LLMsTxtData struct {
	InstanceName string
	CairnVersion string
}

// BuildManifest assembles the manifest struct from runtime info.
// Caller-injectable values (instanceName, cairnVersion, forgejoVersion)
// keep the function pure-ish for testing.
func BuildManifest(instanceName, cairnVersion, forgejoVersion string, features map[string]any) Manifest {
	return Manifest{
		CairnVersion:         cairnVersion,
		ForgejoVersion:       forgejoVersion,
		InstanceName:         instanceName,
		FingerprintAlgo:      "HMAC-SHA256",
		SigningAlgo:          "Ed25519",
		DerivationAlgo:       "HKDF-SHA256",
		DerivationInfoPrefix: "cairn-agent-v1:",
		EmailConvention:      "nexus-{slug}@{domain}",
		Trailers:             []string{"Agent-Id", "Agent-Owner", "Agent-Domain"},
		Endpoints: map[string]string{
			"agents":         "/api/cairn/v1/agents",
			"agent_identity": "/api/cairn/v1/agents/{fingerprint}/identity",
			"attach":         "/api/cairn/v1/agents/attachment-requests",
			"manifest":       "/.well-known/cairn.json",
			"llms_txt":       "/.well-known/llms.txt",
		},
		Features: features,
	}
}

// CairnManifestHandler returns an HTTP handler that serves the manifest.
// Constructor lets the route registration inject the runtime values.
func CairnManifestHandler(instanceName, cairnVersion, forgejoVersion string, features map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := BuildManifest(instanceName, cairnVersion, forgejoVersion, features)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(m)
	}
}

// LLMsTxtHandler returns an HTTP handler for /.well-known/llms.txt.
// Hand-curated markdown with two template-substituted fields.
func LLMsTxtHandler(instanceName, cairnVersion string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmpl, err := template.ParseFS(wellKnownAssets, "templates/wellknown/llms.txt.tmpl")
		if err != nil {
			http.Error(w, fmt.Sprintf("cairn: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_ = tmpl.Execute(w, LLMsTxtData{
			InstanceName: instanceName,
			CairnVersion: cairnVersion,
		})
	}
}

// SecurityTxtHandler returns an HTTP handler for /.well-known/security.txt
// per RFC 9116. Static content embedded in the binary; operator can
// override post-deploy by mounting a different file at the same path.
func SecurityTxtHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := wellKnownAssets.ReadFile("templates/wellknown/security.txt")
		if err != nil {
			http.Error(w, fmt.Sprintf("cairn: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(body)
	}
}
