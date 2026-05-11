package v1

// AgentJSON is the wire format for agent resources returned by the
// API. ActivatedAt is omitted when nil (pending agents).
type AgentJSON struct {
	Fingerprint  string `json:"fingerprint"`
	OwnerName    string `json:"owner"`
	Slug         string `json:"slug"`
	Domain       string `json:"domain"`
	PublicKeyHex string `json:"public_key"`
	Status       string `json:"status"`
	Blocked      bool   `json:"blocked"`
	CreatedAt    string `json:"created_at"`
	ActivatedAt  string `json:"activated_at,omitempty"`
}

// ErrorJSON is the wire format for error responses.
type ErrorJSON struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// BlockRequestJSON is the wire format for POST /agents/{fp}/block.
type BlockRequestJSON struct {
	Reason string `json:"reason"`
}

// AttachmentRequestCreateJSON is the wire format for
// POST /api/cairn/v1/agents/attachment-requests. PubkeyContent is the
// full OpenSSH-format authorized_keys line, e.g.
// "ssh-ed25519 AAAA... comment".
type AttachmentRequestCreateJSON struct {
	OwnerUsername string `json:"owner_username"`
	Slug          string `json:"slug"`
	Domain        string `json:"domain"`
	PubkeyContent string `json:"pubkey_content"`
}

// AttachmentRequestJSON is the wire format for attachment-request
// resources. The pubkey content is intentionally NOT echoed back: it's
// stored server-side and the listing endpoint doesn't need to round-trip
// the raw key.
type AttachmentRequestJSON struct {
	ID            int64  `json:"id"`
	OwnerUsername string `json:"owner"`
	Slug          string `json:"slug"`
	Domain        string `json:"domain"`
	Fingerprint   string `json:"fingerprint"`
	Status        string `json:"status"`
	RequestedAt   string `json:"requested_at"`
	DecidedAt     string `json:"decided_at,omitempty"`
}
