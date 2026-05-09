package identity

import (
	"context"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"xorm.io/xorm"
)

// xormAgentStore is the xorm-backed AgentStore. Each method opens a
// short-lived session, executes, and releases — no long-lived sessions.
// See spec §4.1 for the connection discipline rationale.
type xormAgentStore struct {
	engine *xorm.Engine
}

// NewXormAgentStore returns an AgentStore backed by the given xorm engine.
func NewXormAgentStore(engine *xorm.Engine) AgentStore {
	return &xormAgentStore{engine: engine}
}

func (s *xormAgentStore) Register(ctx context.Context, a *cairn.Agent) error {
	sess := s.engine.NewSession()
	defer sess.Close()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	_, err := sess.Context(ctx).Insert(a)
	return err
}

func (s *xormAgentStore) GetByFingerprint(ctx context.Context, fingerprint string) (*cairn.Agent, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var a cairn.Agent
	has, err := sess.Context(ctx).Where("fingerprint = ?", fingerprint).Get(&a)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, ErrAgentNotFound
	}
	return &a, nil
}

func (s *xormAgentStore) GetByEmail(ctx context.Context, slug, domain string) (*cairn.Agent, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var a cairn.Agent
	has, err := sess.Context(ctx).
		Where("slug = ? AND domain = ?", slug, domain).
		Get(&a)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, ErrAgentNotFound
	}
	return &a, nil
}

func (s *xormAgentStore) ListByUser(ctx context.Context, userID int64, status cairn.AgentStatus) ([]*cairn.Agent, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var out []*cairn.Agent
	q := sess.Context(ctx).Where("user_id = ?", userID)
	if status != "" {
		q = q.And("status = ?", string(status))
	}
	if err := q.Find(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *xormAgentStore) Approve(ctx context.Context, fingerprint string) error {
	sess := s.engine.NewSession()
	defer sess.Close()
	now := time.Now()
	count, err := sess.Context(ctx).
		Where("fingerprint = ?", fingerprint).
		Cols("status", "activated_at").
		Update(&cairn.Agent{
			Status:      cairn.AgentStatusActive,
			ActivatedAt: &now,
		})
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrAgentNotFound
	}
	return nil
}
