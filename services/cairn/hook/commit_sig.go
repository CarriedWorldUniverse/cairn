package hook

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// ErrSignatureMissing is returned when the commit object has no gpgsig header.
var ErrSignatureMissing = errors.New("cairn hook: commit has no signature")

// ErrInvalidSignature is returned when the signature does not verify
// against the public key.
var ErrInvalidSignature = errors.New("cairn hook: invalid commit signature")

// ExtractSignedCommitData parses a raw git commit object and returns
// the data-as-signed (commit object minus the gpgsig header lines)
// and the PEM-armored signature block from the gpgsig header.
//
// Returns ErrSignatureMissing if the commit has no gpgsig.
//
// Git commit objects look like:
//
//	tree <sha>
//	parent <sha>
//	author <name> <email> <time> <tz>
//	committer <name> <email> <time> <tz>
//	gpgsig -----BEGIN SSH SIGNATURE-----
//	 <signature lines, indented one space>
//	 -----END SSH SIGNATURE-----
//
//	<commit message>
//
// Continuation lines for the gpgsig header value start with one space
// (RFC 822-style folded header).
func ExtractSignedCommitData(commitRaw []byte) (signedPayload, signatureBlock []byte, err error) {
	headerEnd := bytes.Index(commitRaw, []byte("\n\n"))
	if headerEnd < 0 {
		return nil, nil, fmt.Errorf("cairn hook: malformed commit object (no header/body separator)")
	}
	headerPart := commitRaw[:headerEnd]
	bodyPart := commitRaw[headerEnd:] // includes leading "\n\n"

	var (
		filteredHeaders bytes.Buffer
		sigBuf          bytes.Buffer
		inGPGSig        bool
		foundGPGSig     bool
	)
	for _, line := range bytes.Split(headerPart, []byte("\n")) {
		if inGPGSig {
			if len(line) > 0 && line[0] == ' ' {
				sigBuf.Write(line[1:])
				sigBuf.WriteByte('\n')
				continue
			}
			inGPGSig = false
		}

		if bytes.HasPrefix(line, []byte("gpgsig ")) {
			foundGPGSig = true
			inGPGSig = true
			sigBuf.Write(line[len("gpgsig "):])
			sigBuf.WriteByte('\n')
			continue
		}

		filteredHeaders.Write(line)
		filteredHeaders.WriteByte('\n')
	}

	if !foundGPGSig {
		return nil, nil, ErrSignatureMissing
	}

	headers := bytes.TrimRight(filteredHeaders.Bytes(), "\n")
	signed := append(headers, bodyPart...)

	return signed, sigBuf.Bytes(), nil
}

// VerifyAgentSignature is a top-level helper: extracts the signature
// from the raw commit, parses it, and verifies against the agent's
// public key.
//
// Returns ErrSignatureMissing if the commit has no gpgsig.
// Returns ErrInvalidSignature if verification fails.
// Returns other errors for malformed inputs.
func VerifyAgentSignature(commitRaw []byte, agentPubKey ed25519.PublicKey) error {
	sshPub, err := ssh.NewPublicKey(agentPubKey)
	if err != nil {
		return err
	}
	return verifyAgainstSSHKey(commitRaw, sshPub)
}

// VerifyAgentSignatureSSH is the post-V503 entry point: verifies a
// commit signature against an OpenSSH-format public-key text (e.g.
// "ssh-ed25519 AAAA...") loaded from Forgejo's public_key table.
//
// Returns ErrSignatureMissing if the commit has no gpgsig.
// Returns ErrInvalidSignature if verification fails.
// Returns other errors for malformed inputs.
func VerifyAgentSignatureSSH(commitRaw []byte, pubkeyContent string) error {
	sshPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubkeyContent))
	if err != nil {
		return fmt.Errorf("cairn hook: parse pubkey: %w", err)
	}
	return verifyAgainstSSHKey(commitRaw, sshPub)
}

func verifyAgainstSSHKey(commitRaw []byte, sshPub ssh.PublicKey) error {
	signed, armored, err := ExtractSignedCommitData(commitRaw)
	if err != nil {
		return err
	}
	sig, err := ParseSSHSignature(armored)
	if err != nil {
		return fmt.Errorf("cairn hook: parse signature: %w", err)
	}
	if err := VerifySSHSignedData(sshPub, signed, sig, "git"); err != nil {
		return ErrInvalidSignature
	}
	return nil
}
