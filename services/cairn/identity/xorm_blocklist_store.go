package identity

import (
	"context"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"xorm.io/xorm"
)

type xormBlocklistStore struct {
	engine *xorm.Engine
}

// NewXormBlocklistStore returns an AgentBlocklistStore backed by xorm.
func NewXormBlocklistStore(engine *xorm.Engine) AgentBlocklistStore {
	return &xormBlocklistStore{engine: engine}
}

func (s *xormBlocklistStore) Block(ctx context.Context, agentID int64, reason string) error {
	sess := s.engine.NewSession()
	defer sess.Close()
	row := &cairn.AgentBlocklist{
		AgentID:   agentID,
		BlockedAt: time.Now(),
		Reason:    reason,
	}
	_, err := sess.Context(ctx).Insert(row)
	return err
}

func (s *xormBlocklistStore) IsBlocked(ctx context.Context, agentID int64) (bool, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	count, err := sess.Context(ctx).
		Where("agent_id = ?", agentID).
		Count(&cairn.AgentBlocklist{})
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *xormBlocklistStore) List(ctx context.Context) ([]*cairn.AgentBlocklist, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var out []*cairn.AgentBlocklist
	if err := sess.Context(ctx).Find(&out); err != nil {
		return nil, err
	}
	return out, nil
}
