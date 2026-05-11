// Cairn-specific code; AGPLv3. See LICENSING.md.

package identity

import (
	"context"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"xorm.io/xorm"
)

type xormAttachmentRequestStore struct {
	engine *xorm.Engine
}

// NewXormAttachmentRequestStore returns an AttachmentRequestStore
// backed by xorm.
func NewXormAttachmentRequestStore(engine *xorm.Engine) AttachmentRequestStore {
	return &xormAttachmentRequestStore{engine: engine}
}

func (s *xormAttachmentRequestStore) Insert(ctx context.Context, req *cairn.AttachmentRequest) error {
	sess := s.engine.NewSession()
	defer sess.Close()
	if _, err := sess.Context(ctx).Insert(req); err != nil {
		return err
	}
	return nil
}

func (s *xormAttachmentRequestStore) GetByID(ctx context.Context, id int64) (*cairn.AttachmentRequest, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var req cairn.AttachmentRequest
	has, err := sess.Context(ctx).ID(id).Get(&req)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, ErrAttachmentRequestNotFound
	}
	return &req, nil
}

func (s *xormAttachmentRequestStore) ListPendingByOwner(ctx context.Context, ownerUsername string) ([]*cairn.AttachmentRequest, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var out []*cairn.AttachmentRequest
	if err := sess.Context(ctx).
		Where("owner_username = ? AND status = ?", ownerUsername, string(cairn.AttachmentRequestPending)).
		Find(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *xormAttachmentRequestStore) UpdateDecision(ctx context.Context, id int64, status cairn.AttachmentRequestStatus, decidedByUserID int64) error {
	sess := s.engine.NewSession()
	defer sess.Close()
	count, err := sess.Context(ctx).
		ID(id).
		Cols("status", "decided_unix", "decided_by_user_id").
		Update(&cairn.AttachmentRequest{
			Status:          status,
			DecidedUnix:     time.Now().Unix(),
			DecidedByUserID: decidedByUserID,
		})
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrAttachmentRequestNotFound
	}
	return nil
}
