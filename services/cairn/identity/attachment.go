// Cairn-specific code; AGPLv3. See LICENSING.md.

package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// FingerprintFromContent computes the Cairn fingerprint of an
// OpenSSH-format public key text using the instance HMAC key. Used by
// the attachment-request flow where the agent submits its key as text
// (rather than raw ed25519 bytes).
//
// The fingerprint is HMAC-SHA256 of the raw SSH wire-format key bytes
// (ssh.PublicKey.Marshal()) under the instance HMAC key. For ed25519
// agents this matches Fingerprint(hmacKey, ed25519PublicKey) because
// the SSH wire format of an ed25519 key embeds the same 32-byte
// public component plus a fixed prefix.
//
// Note: Fingerprint(hmacKey, ed25519Pub) hashes the bare 32 ed25519
// bytes; FingerprintFromContent hashes the full SSH wire format. They
// are intentionally different inputs producing different outputs — the
// attachment-request flow consistently uses the wire-format flavour.
func FingerprintFromContent(instanceHMACKey []byte, pubkeyContent string) (string, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubkeyContent))
	if err != nil {
		return "", fmt.Errorf("%w: parse pubkey: %v", ErrInvalidInput, err)
	}
	mac := hmac.New(sha256.New, instanceHMACKey)
	mac.Write(pub.Marshal())
	sum := mac.Sum(nil)
	return fingerprintPrefix + base64.RawURLEncoding.EncodeToString(sum), nil
}

// CreateAttachmentRequest records a new pending attachment-request from
// an agent. Anonymous-callable at the service layer — HTTP handlers
// decide whether auth is required (Task 4).
//
// Validates slug/domain shape, parses the pubkey, computes the Cairn
// fingerprint, INSERTs a row with Status=pending, and returns it.
func (s *AgentService) CreateAttachmentRequest(ctx context.Context, ownerUsername, slug, domain, pubkeyContent string) (*cairn.AttachmentRequest, error) {
	if err := validateSlugDomain(slug, domain); err != nil {
		return nil, err
	}
	if strings.TrimSpace(pubkeyContent) == "" {
		return nil, fmt.Errorf("%w: pubkey content empty", ErrInvalidInput)
	}
	// Confirm the owner exists. Anonymous callers are allowed to submit
	// the request but the owner must resolve.
	if _, err := s.users.UserIDByUsername(ctx, ownerUsername); err != nil {
		return nil, err
	}
	fp, err := FingerprintFromContent(s.hmacKey, pubkeyContent)
	if err != nil {
		return nil, err
	}
	req := &cairn.AttachmentRequest{
		OwnerUsername: ownerUsername,
		Slug:          slug,
		Domain:        domain,
		PubkeyContent: pubkeyContent,
		Fingerprint:   fp,
		Status:        cairn.AttachmentRequestPending,
	}
	if err := s.requests.Insert(ctx, req); err != nil {
		return nil, err
	}
	return req, nil
}

// ListPendingForOwner returns the pending attachment requests for the
// named owner. Convenience for the user-settings UI.
func (s *AgentService) ListPendingForOwner(ctx context.Context, ownerUsername string) ([]*cairn.AttachmentRequest, error) {
	return s.requests.ListPendingByOwner(ctx, ownerUsername)
}

// ApproveAttachmentRequest is the atomic approval flow. It:
//
//  1. Loads the request; ensures status=pending (else ErrAlreadyDecided).
//  2. Provisions (or finds) the agent's Forgejo user.
//  3. Inserts the pubkey into Forgejo's public_key table.
//  4. Finds-or-creates the cairn_agent row.
//  5. Inserts a cairn_agent_pubkey binding.
//  6. Sets the agent to Active.
//  7. Marks the request approved with DecidedUnix + DecidedByUserID.
//
// Returns the (possibly newly created) Agent. Returns
// ErrPubkeyAlreadyClaimed if the fingerprint is already bound to a
// different agent.
//
// Steps 2-6 reuse the same registerCore helper invoked by Register;
// it is not currently wrapped in a single SQL transaction because the
// Forgejo-side writes (CreateUser, AddPublicKey) and the Cairn-side
// writes share no engine. registerCore is idempotent for steps 2-4
// (find-or-create) and step 5 fails fast on the unique constraint, so
// partial failures leave a recoverable state. A future revision can
// wrap the Cairn-side steps in an xorm transaction once we have a
// transactional registrar pattern.
func (s *AgentService) ApproveAttachmentRequest(ctx context.Context, requestID, decidedByUserID int64) (*cairn.Agent, error) {
	req, err := s.requests.GetByID(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if req.Status != cairn.AttachmentRequestPending {
		return nil, ErrAlreadyDecided
	}
	ownerID, err := s.users.UserIDByUsername(ctx, req.OwnerUsername)
	if err != nil {
		return nil, err
	}

	agent, err := s.registerCore(ctx, ownerID, req.Slug, req.Domain, req.PubkeyContent, req.Fingerprint, true /*autoApprove*/)
	if err != nil {
		return nil, err
	}
	if err := s.requests.UpdateDecision(ctx, requestID, cairn.AttachmentRequestApproved, decidedByUserID); err != nil {
		return nil, err
	}
	return agent, nil
}

// RejectAttachmentRequest marks a pending request rejected. Returns
// ErrAlreadyDecided if the request has already been approved or
// rejected.
func (s *AgentService) RejectAttachmentRequest(ctx context.Context, requestID, decidedByUserID int64) error {
	req, err := s.requests.GetByID(ctx, requestID)
	if err != nil {
		return err
	}
	if req.Status != cairn.AttachmentRequestPending {
		return ErrAlreadyDecided
	}
	return s.requests.UpdateDecision(ctx, requestID, cairn.AttachmentRequestRejected, decidedByUserID)
}
