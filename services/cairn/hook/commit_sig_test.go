package hook

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// buildSignedCommit constructs a fake commit object with the given
// body, signs it, and returns the raw bytes that ExtractSignedCommitData
// should be able to parse.
func buildSignedCommit(t *testing.T, body string, signer ssh.Signer) []byte {
	t.Helper()

	headers := []string{
		"tree 1234567890abcdef1234567890abcdef12345678",
		"author nexus-plumb <nexus-plumb@darksoft.co.nz> 1700000000 +0000",
		"committer nexus-plumb <nexus-plumb@darksoft.co.nz> 1700000000 +0000",
	}
	unsigned := strings.Join(headers, "\n") + "\n\n" + body

	armored, err := SignSSHSig(signer, []byte(unsigned), "git")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	armoredLines := strings.Split(strings.TrimRight(string(armored), "\n"), "\n")
	var gpgsigLines []string
	for i, line := range armoredLines {
		if i == 0 {
			gpgsigLines = append(gpgsigLines, "gpgsig "+line)
		} else {
			gpgsigLines = append(gpgsigLines, " "+line)
		}
	}

	signedHeaders := append([]string{}, headers...)
	signedHeaders = append(signedHeaders, gpgsigLines...)
	out := strings.Join(signedHeaders, "\n") + "\n\n" + body
	return []byte(out)
}

func TestExtractSignedCommitData_HappyPath(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	commit := buildSignedCommit(t, "Test commit\n", signer)

	signed, armored, err := ExtractSignedCommitData(commit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(armored), "BEGIN SSH SIGNATURE") {
		t.Errorf("armored signature missing PEM header")
	}
	if bytes.Contains(signed, []byte("gpgsig")) {
		t.Error("signed payload still contains gpgsig header")
	}
	if !bytes.Contains(signed, []byte("Test commit")) {
		t.Error("signed payload missing commit body")
	}
}

func TestExtractSignedCommitData_NoSignature(t *testing.T) {
	commit := []byte("tree abc123\nauthor jane <jane@example.com> 1700000000 +0000\n\nUnsigned\n")
	_, _, err := ExtractSignedCommitData(commit)
	if !errors.Is(err, ErrSignatureMissing) {
		t.Errorf("err = %v, want ErrSignatureMissing", err)
	}
}

func TestExtractSignedCommitData_Malformed(t *testing.T) {
	_, _, err := ExtractSignedCommitData([]byte("no separator here"))
	if err == nil {
		t.Error("expected error for malformed commit object")
	}
}

func TestVerifyAgentSignature_HappyPath(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	commit := buildSignedCommit(t, "Test commit\n", signer)

	if err := VerifyAgentSignature(commit, pub); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestVerifyAgentSignature_TamperedBody(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	commit := buildSignedCommit(t, "Test commit\n", signer)

	tampered := bytes.Replace(commit, []byte("Test"), []byte("Evil"), 1)
	err := VerifyAgentSignature(tampered, pub)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestVerifyAgentSignature_WrongKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	commit := buildSignedCommit(t, "Test commit\n", signer)

	err := VerifyAgentSignature(commit, otherPub)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestVerifyAgentSignature_NoSignature(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	commit := []byte("tree abc\nauthor a <a@x> 1 +0000\n\nbody\n")
	err := VerifyAgentSignature(commit, pub)
	if !errors.Is(err, ErrSignatureMissing) {
		t.Errorf("err = %v, want ErrSignatureMissing", err)
	}
}
