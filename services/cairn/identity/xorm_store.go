package identity

import (
	"context"
	"strings"
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

func (s *xormAgentStore) FindOrCreateByUserSlug(ctx context.Context, userID int64, slug, domain string) (*cairn.Agent, bool, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var a cairn.Agent
	has, err := sess.Context(ctx).
		Where("user_id = ? AND slug = ?", userID, slug).
		Get(&a)
	if err != nil {
		return nil, false, err
	}
	if has {
		return &a, false, nil
	}
	a = cairn.Agent{
		UserID:    userID,
		Slug:      slug,
		Domain:    domain,
		Status:    cairn.AgentStatusPending,
		CreatedAt: time.Now(),
	}
	if _, err := sess.Context(ctx).Insert(&a); err != nil {
		if isUniqueViolation(err) {
			return nil, false, ErrAgentExists
		}
		return nil, false, err
	}
	return &a, true, nil
}

func (s *xormAgentStore) GetByID(ctx context.Context, id int64) (*cairn.Agent, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var a cairn.Agent
	has, err := sess.Context(ctx).ID(id).Get(&a)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, ErrAgentNotFound
	}
	return &a, nil
}

// isUniqueViolation reports whether err is a database-driver unique-
// constraint error. Recognises SQLite (modernc/mattn) and Postgres
// shapes; returns false for unknown drivers (caller will see the raw
// error).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return true
	case strings.Contains(msg, "constraint failed: UNIQUE"):
		return true
	case strings.Contains(msg, "duplicate key value"):
		return true
	}
	return false
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

func (s *xormAgentStore) SetStatus(ctx context.Context, agentID int64, status cairn.AgentStatus) error {
	sess := s.engine.NewSession()
	defer sess.Close()
	upd := &cairn.Agent{Status: status}
	cols := []string{"status"}
	if status == cairn.AgentStatusActive {
		now := time.Now()
		upd.ActivatedAt = &now
		cols = append(cols, "activated_at")
	}
	count, err := sess.Context(ctx).ID(agentID).Cols(cols...).Update(upd)
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrAgentNotFound
	}
	return nil
}
