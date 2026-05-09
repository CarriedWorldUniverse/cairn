// Package hook contains shared logic for the cairn pre-receive hook
// and the cairn CLI signer. The PROTOCOL.sshsig wire-format helpers
// here are the single source of truth for both signing (cmd/cairn) and
// verifying (server-side hook) commit signatures.
package hook

import (
	"bytes"
	"crypto/sha512"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
)

// sshSigMagic is the leading bytes of an SSHSIG-wrapped blob per
// PROTOCOL.sshsig.
const sshSigMagic = "SSHSIG"

// SignSSHSig produces an SSHSIG-armored signature over data per
// PROTOCOL.sshsig.
func SignSSHSig(signer ssh.Signer, data []byte, namespace string) ([]byte, error) {
	hashed := sha512.Sum512(data)
	signedBlob := signedDataBlob(hashed[:], namespace)

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

// ParseSSHSignature decodes a PEM-armored SSHSIG blob and returns the
// inner ssh.Signature for use with VerifySSHSignedData.
func ParseSSHSignature(armored []byte) (*ssh.Signature, error) {
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

// VerifySSHSignedData reconstructs the SSHSIG signed-data envelope and
// verifies the signature against the public key.
func VerifySSHSignedData(pub ssh.PublicKey, data []byte, sig *ssh.Signature, namespace string) error {
	hashed := sha512.Sum512(data)
	signedBlob := signedDataBlob(hashed[:], namespace)
	return pub.Verify(signedBlob, sig)
}

// signedDataBlob is the data the signer actually signs. Per
// PROTOCOL.sshsig: MAGIC + namespace + reserved + hash_algo + hashed_data.
func signedDataBlob(hashed []byte, namespace string) []byte {
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
