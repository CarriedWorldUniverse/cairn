package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func TestFingerprint_Format(t *testing.T) {
	hmacKey := []byte("test-instance-hmac-key-32-bytes!!")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	fp := Fingerprint(hmacKey, pub)

	if !strings.HasPrefix(fp, "cairn:") {
		t.Errorf("fingerprint missing cairn: prefix: %q", fp)
	}
	// HMAC-SHA256 is 32 bytes; base64 with padding is 44 chars,
	// without padding 43. Total with prefix: 49 or 50 chars.
	if l := len(fp); l < 49 || l > 51 {
		t.Errorf("fingerprint length unexpected: %d (%q)", l, fp)
	}
}

func TestFingerprint_Deterministic(t *testing.T) {
	hmacKey := []byte("test-instance-hmac-key-32-bytes!!")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	a := Fingerprint(hmacKey, pub)
	b := Fingerprint(hmacKey, pub)

	if a != b {
		t.Errorf("non-deterministic: %q != %q", a, b)
	}
}

func TestFingerprint_DifferentKeysProduceDifferentFingerprints(t *testing.T) {
	hmacKey := []byte("test-instance-hmac-key-32-bytes!!")
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)

	fp1 := Fingerprint(hmacKey, pub1)
	fp2 := Fingerprint(hmacKey, pub2)

	if fp1 == fp2 {
		t.Error("different pubkeys produced identical fingerprint")
	}
}

func TestFingerprint_DifferentHMACKeysProduceDifferentFingerprints(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	keyA := []byte("instance-A-hmac-key-bytes-32-len!")
	keyB := []byte("instance-B-hmac-key-bytes-32-len!")

	fpA := Fingerprint(keyA, pub)
	fpB := Fingerprint(keyB, pub)

	if fpA == fpB {
		t.Error("same pubkey on different instances produced same fingerprint")
	}
}

func TestParseAgentEmail_Valid(t *testing.T) {
	cases := []struct {
		email      string
		wantSlug   string
		wantDomain string
	}{
		{"nexus-plumb@darksoft.co.nz", "plumb", "darksoft.co.nz"},
		{"nexus-anvil@example.com", "anvil", "example.com"},
		{"nexus-x@y.z", "x", "y.z"},
	}
	for _, tc := range cases {
		t.Run(tc.email, func(t *testing.T) {
			slug, domain, ok := ParseAgentEmail(tc.email)
			if !ok {
				t.Fatalf("ok=false for %q", tc.email)
			}
			if slug != tc.wantSlug {
				t.Errorf("slug = %q, want %q", slug, tc.wantSlug)
			}
			if domain != tc.wantDomain {
				t.Errorf("domain = %q, want %q", domain, tc.wantDomain)
			}
		})
	}
}

func TestParseAgentEmail_Invalid(t *testing.T) {
	cases := []string{
		"nexus@darksoft.co.nz",         // no nexus- prefix
		"nexus-@darksoft.co.nz",          // empty slug
		"nexus-plumb",                    // no @
		"nexus-plumb@",                   // empty domain
		"",                               // empty
		"NEXUS-PLUMB@darksoft.co.nz",     // case-sensitive — uppercase rejected
		"nexus-PLUMB@darksoft.co.nz",     // mixed case slug rejected
		"nexus-pl umb@darksoft.co.nz",    // space in slug
	}
	for _, e := range cases {
		t.Run(e, func(t *testing.T) {
			slug, domain, ok := ParseAgentEmail(e)
			if ok {
				t.Errorf("ok=true for invalid %q (slug=%q domain=%q)", e, slug, domain)
			}
		})
	}
}
