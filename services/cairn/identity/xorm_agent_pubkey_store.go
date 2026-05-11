// Cairn-specific code; AGPLv3. See LICENSING.md.

package identity

import (
	"context"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"xorm.io/xorm"
)

type xormAgentPubkeyStore struct {
	engine *xorm.Engine
}

// NewXormAgentPubkeyStore returns an AgentPubkeyStore backed by xorm.
func NewXormAgentPubkeyStore(engine *xorm.Engine) AgentPubkeyStore {
	return &xormAgentPubkeyStore{engine: engine}
}

func (s *xormAgentPubkeyStore) Insert(ctx context.Context, row *cairn.AgentPubkey) error {
	sess := s.engine.NewSession()
	defer sess.Close()
	if _, err := sess.Context(ctx).Insert(row); err != nil {
		if isUniqueViolation(err) {
			return ErrPubkeyAlreadyClaimed
		}
		return err
	}
	return nil
}

func (s *xormAgentPubkeyStore) GetByFingerprint(ctx context.Context, fingerprint string) (*cairn.AgentPubkey, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var row cairn.AgentPubkey
	has, err := sess.Context(ctx).Where("fingerprint = ?", fingerprint).Get(&row)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, ErrAgentNotFound
	}
	return &row, nil
}

func (s *xormAgentPubkeyStore) ListByAgent(ctx context.Context, agentID int64) ([]*cairn.AgentPubkey, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var out []*cairn.AgentPubkey
	if err := sess.Context(ctx).Where("agent_id = ?", agentID).Find(&out); err != nil {
		return nil, err
	}
	return out, nil
}
