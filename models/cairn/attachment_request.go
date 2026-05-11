//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

// AttachmentRequestStatus is the lifecycle state of an attachment request.
type AttachmentRequestStatus string

const (
	// AttachmentRequestPending — submitted, awaiting owner decision.
	AttachmentRequestPending AttachmentRequestStatus = "pending"
	// AttachmentRequestApproved — owner approved; agent + pubkey active.
	AttachmentRequestApproved AttachmentRequestStatus = "approved"
	// AttachmentRequestRejected — owner rejected; audit retained.
	AttachmentRequestRejected AttachmentRequestStatus = "rejected"
)

// AttachmentRequest is a pending (or historical) ask from an agent to
// attach to a human owner's cluster. Anonymous submission is allowed;
// the owner approves via API or UI before the agent + pubkey become
// active.
//
// See docs/cairn/specs/2026-05-11-cairn-instance-rooted-identity.md.
type AttachmentRequest struct {
	ID              int64                   `xorm:"pk autoincr"`
	OwnerUsername   string                  `xorm:"VARCHAR(255) NOT NULL INDEX"`
	Slug            string                  `xorm:"VARCHAR(64) NOT NULL"`
	Domain          string                  `xorm:"VARCHAR(255) NOT NULL"`
	PubkeyContent   string                  `xorm:"TEXT NOT NULL"`
	Fingerprint     string                  `xorm:"VARCHAR(255) NOT NULL INDEX"`
	Status          AttachmentRequestStatus `xorm:"VARCHAR(16) NOT NULL DEFAULT 'pending' INDEX"`
	RequestedUnix   int64                   `xorm:"created"`
	DecidedUnix     int64                   `xorm:"NOT NULL DEFAULT 0"`
	DecidedByUserID int64                   `xorm:"NOT NULL DEFAULT 0"`
}

// TableName returns the SQL table name.
func (AttachmentRequest) TableName() string { return "cairn_attachment_request" }
