package v1

// RegisterRequestJSON is the wire format for POST /api/cairn/v1/agents.
//
// PublicKeyHex is hex-encoded so the JSON is text-only and copy-paste
// friendly. The handler decodes it before passing to the service.
type RegisterRequestJSON struct {
	ProposedOwner string `json:"proposed_owner"`
	Slug          string `json:"slug"`
	Domain        string `json:"domain"`
	PublicKeyHex  string `json:"public_key"`
}

// AgentJSON is the wire format for agent resources returned by the
// API. ActivatedAt is omitted when nil (pending agents).
type AgentJSON struct {
	Fingerprint  string `json:"fingerprint"`
	OwnerName    string `json:"owner"`
	Slug         string `json:"slug"`
	Domain       string `json:"domain"`
	PublicKeyHex string `json:"public_key"`
	Status       string `json:"status"`
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
