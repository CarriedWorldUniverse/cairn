package cairn

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	casket "github.com/CarriedWorldUniverse/casket-go"
)

// AgentInit derives the agent keypair from the owner's seed file via
// HKDF + Ed25519, writes the OpenSSH-format public key to
// <hostdir>/<slug>.key.pub, and prints metadata to out.
//
// Private key is NOT persisted — commit-sign-helper re-derives it on
// each signing invocation. The pubkey file exists so git's
// gpg.format=ssh signing flow has something to point at via
// user.signingkey.
func AgentInit(instanceURL, slug, domain string, out io.Writer) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	if err := paths.EnsureHostDir(); err != nil {
		return err
	}

	seed, err := paths.ReadSeed()
	if err != nil {
		return err
	}

	_, pub, err := casket.DeriveAgentKey(seed, slug)
	if err != nil {
		return err
	}

	pubFile := paths.KeyFile(slug) + ".pub"
	pubLine := openSSHPublicKey(pub, slug+"@"+domain)
	if err := os.WriteFile(pubFile, []byte(pubLine), 0644); err != nil {
		return fmt.Errorf("cairn agent init: write pubkey: %w", err)
	}

	email := "nexus-" + slug + "@" + domain
	fmt.Fprintf(out, "slug: %s\n", slug)
	fmt.Fprintf(out, "domain: %s\n", domain)
	fmt.Fprintf(out, "email: %s\n", email)
	fmt.Fprintf(out, "public_key_file: %s\n", pubFile)
	fmt.Fprintf(out, "next: cairn agent submit --instance %s\n", instanceURL)

	return nil
}

// openSSHPublicKey serialises an Ed25519 public key in the OpenSSH
// authorized_keys format: "ssh-ed25519 <base64-blob> <comment>\n".
//
// The blob is the SSH wire format: length-prefixed "ssh-ed25519" string
// followed by the length-prefixed 32-byte raw key.
func openSSHPublicKey(pub ed25519.PublicKey, comment string) string {
	keytype := []byte("ssh-ed25519")

	buf := make([]byte, 0, 4+len(keytype)+4+len(pub))
	buf = appendString(buf, keytype)
	buf = appendString(buf, pub)

	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(buf) + " " + comment + "\n"
}

func appendString(buf, s []byte) []byte {
	var lenBytes [4]byte
	binary.BigEndian.PutUint32(lenBytes[:], uint32(len(s)))
	buf = append(buf, lenBytes[:]...)
	buf = append(buf, s...)
	return buf
}
