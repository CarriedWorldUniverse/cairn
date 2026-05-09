package cairn

import (
	"bytes"
	"crypto/sha512"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	casket "github.com/CarriedWorldUniverse/casket-go"
)

// sshSigMagic is the leading bytes of an SSHSIG-wrapped blob per
// PROTOCOL.sshsig.
const sshSigMagic = "SSHSIG"

// CommitSignHelper reads data from stdin, derives the agent's keypair
// from the owner's seed file via casket.DeriveAgentKey(seed, slug),
// produces an SSH-format signature in the given namespace (typically
// "git"), and writes the PEM-armored result to stdout.
//
// This is the function git invokes when configured with
//
//	gpg.format       = ssh
//	gpg.ssh.program  = cairn commit-sign-helper --slug <slug>
//
// The private key is never persisted to disk — it is derived on each
// signing call and discarded when the function returns.
func CommitSignHelper(instanceURL, slug, namespace string, in io.Reader, out io.Writer) error {
	if namespace == "" {
		namespace = "git"
	}

	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	seed, err := paths.ReadSeed()
	if err != nil {
		return err
	}

	priv, _, err := casket.DeriveAgentKey(seed, slug)
	if err != nil {
		return err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return err
	}

	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}

	armored, err := signSSHSig(signer, data, namespace)
	if err != nil {
		return err
	}
	_, err = out.Write(armored)
	return err
}

// inferSlugFromKeyfile takes a keyfile path like
// "/home/user/.config/cairn/host/plumb.key.pub" and returns "plumb".
// Strips ".key.pub", ".pub", or ".key" suffix from the basename. If
// none match, returns the basename unchanged.
func inferSlugFromKeyfile(path string) string {
	if path == "" {
		return ""
	}
	base := filepath.Base(path)
	for _, suffix := range []string{".key.pub", ".pub", ".key"} {
		if strings.HasSuffix(base, suffix) {
			return strings.TrimSuffix(base, suffix)
		}
	}
	return base
}

// signSSHSig produces an SSHSIG-armored signature over data per
// PROTOCOL.sshsig.
func signSSHSig(signer ssh.Signer, data []byte, namespace string) ([]byte, error) {
	hashed := sha512.Sum512(data)
	signedBlob := signedDataBlob(signer.PublicKey(), hashed[:], namespace)

	sig, err := signer.Sign(nil, signedBlob)
	if err != nil {
		return nil, err
	}

	// PROTOCOL.sshsig outer envelope.
	var buf bytes.Buffer
	buf.WriteString(sshSigMagic)
	writeUint32(&buf, 1) // version
	writeString(&buf, signer.PublicKey().Marshal())
	writeString(&buf, []byte(namespace))
	writeString(&buf, nil) // reserved
	writeString(&buf, []byte("sha512"))
	writeString(&buf, ssh.Marshal(sig))

	return pem.EncodeToMemory(&pem.Block{
		Type:  "SSH SIGNATURE",
		Bytes: buf.Bytes(),
	}), nil
}

// signedDataBlob is the data the signer actually signs. Per
// PROTOCOL.sshsig: MAGIC + namespace + reserved + hash_algo + hashed_data.
func signedDataBlob(_ ssh.PublicKey, hashed []byte, namespace string) []byte {
	var buf bytes.Buffer
	buf.WriteString(sshSigMagic)
	writeString(&buf, []byte(namespace))
	writeString(&buf, nil)
	writeString(&buf, []byte("sha512"))
	writeString(&buf, hashed)
	return buf.Bytes()
}

// writeString writes a length-prefixed string (SSH wire format).
func writeString(buf *bytes.Buffer, s []byte) {
	var lenBytes [4]byte
	binary.BigEndian.PutUint32(lenBytes[:], uint32(len(s)))
	buf.Write(lenBytes[:])
	buf.Write(s)
}

func writeUint32(buf *bytes.Buffer, n uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], n)
	buf.Write(b[:])
}

// parseSSHSignatureBlob is used by tests to round-trip-verify our
// own output. Returns the inner ssh.Signature.
func parseSSHSignatureBlob(armored []byte) (*ssh.Signature, error) {
	block, _ := pem.Decode(armored)
	if block == nil || block.Type != "SSH SIGNATURE" {
		return nil, errors.New("not an SSH SIGNATURE PEM block")
	}
	r := bytes.NewReader(block.Bytes)

	magic := make([]byte, len(sshSigMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != sshSigMagic {
		return nil, fmt.Errorf("missing SSHSIG magic")
	}
	var version uint32
	if err := binary.Read(r, binary.BigEndian, &version); err != nil {
		return nil, err
	}
	// Skip publickey, namespace, reserved, hash_algorithm strings.
	for i := 0; i < 4; i++ {
		if _, err := readString(r); err != nil {
			return nil, err
		}
	}
	sigBytes, err := readString(r)
	if err != nil {
		return nil, err
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(sigBytes, &sig); err != nil {
		return nil, err
	}
	return &sig, nil
}

// verifySSHSignedData reconstructs the SSHSIG signed-data envelope and
// verifies the signature against the public key.
func verifySSHSignedData(pub ssh.PublicKey, data []byte, sig *ssh.Signature, namespace string) error {
	hashed := sha512.Sum512(data)
	signedBlob := signedDataBlob(pub, hashed[:], namespace)
	return pub.Verify(signedBlob, sig)
}

func readString(r *bytes.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}
