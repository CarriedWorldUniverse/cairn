package hook

import (
	"context"
	"errors"
	"fmt"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// New error sentinels for push-time rejection reasons.
var (
	ErrOrphanAgent    = errors.New("cairn hook: agent not registered")
	ErrAgentNotActive = errors.New("cairn hook: agent not active")
	ErrAgentBlocked   = errors.New("cairn hook: agent blocked")
)

// CommitToVerify carries the data needed to verify one commit. The
// caller (the Forgejo hook) is responsible for git-walking between
// old/new ref tips and producing this slice.
type CommitToVerify struct {
	SHA         string
	AuthorEmail string
	Message     string
	Raw         []byte // full commit object bytes including gpgsig header
}

// VerifyAgentCommits walks the new commits in a push and rejects any
// that look like agent commits (author matching nexus-{slug}@{domain})
// but fail signature, ownership, status, blocklist, or trailer checks.
//
// If enforce is false, this is a no-op (returns nil). Used during
// migration window when the [cairn] enforce_signatures flag is off.
//
// rejectOrphanAgents controls how unregistered agent-format authors
// are handled: true (default) rejects with ErrOrphanAgent; false lets
// them pass (treated as if non-agent for verification purposes), per
// the [cairn] reject_orphan_agents setting.
//
// Returns the first commit-level failure with a wrapped rejection
// reason; vanilla (non-agent) commits are skipped. The loop honours
// ctx cancellation between commits so a slow push of many commits
// doesn't outrun the hook timeout.
func VerifyAgentCommits(
	ctx context.Context,
	commits []CommitToVerify,
	svc *cairnidentity.AgentService,
	enforce bool,
	rejectOrphanAgents bool,
) error {
	if !enforce {
		return nil
	}
	for _, c := range commits {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := verifyOne(ctx, c, svc, rejectOrphanAgents); err != nil {
			return err
		}
	}
	return nil
}

func verifyOne(ctx context.Context, c CommitToVerify, svc *cairnidentity.AgentService, rejectOrphanAgents bool) error {
	slug, domain, isAgent := cairnidentity.ParseAgentEmail(c.AuthorEmail)
	if !isAgent {
		// Non-agent commit — vanilla Forgejo handles it; skip.
		return nil
	}

	agent, err := svc.GetByEmail(ctx, slug, domain)
	if err != nil {
		if errors.Is(err, cairnidentity.ErrAgentNotFound) {
			if !rejectOrphanAgents {
				// Setting allows orphans; treat as non-agent and skip.
				return nil
			}
			return fmt.Errorf("%w: commit %s author %s (slug=%s domain=%s)",
				ErrOrphanAgent, c.SHA, c.AuthorEmail, slug, domain)
		}
		return err
	}

	if agent.Status != cairn.AgentStatusActive {
		return fmt.Errorf("%w: commit %s by %s (status=%s)",
			ErrAgentNotActive, c.SHA, c.AuthorEmail, agent.Status)
	}

	blocked, err := svc.IsAgentBlocked(ctx, agent.ID)
	if err != nil {
		return fmt.Errorf("cairn hook: check blocklist for agent %d: %w", agent.ID, err)
	}
	if blocked {
		return fmt.Errorf("%w: commit %s by %s (agent_id=%d)",
			ErrAgentBlocked, c.SHA, c.AuthorEmail, agent.ID)
	}

	// Load all of the agent's bound pubkeys (one per registered host)
	// from Forgejo's public_key table via the cairn_agent_pubkey FK.
	// A commit signed on any host the agent has registered must verify.
	pubContents, err := svc.PubkeyContentsForAgent(ctx, agent.ID)
	if err != nil {
		if errors.Is(err, cairnidentity.ErrAgentNotFound) {
			return fmt.Errorf("%w: commit %s by %s (agent_id=%d, no_pubkeys)",
				ErrOrphanAgent, c.SHA, c.AuthorEmail, agent.ID)
		}
		return fmt.Errorf("cairn hook: load pubkeys for agent %d: %w", agent.ID, err)
	}
	if len(pubContents) == 0 {
		// Defensive: GetByEmail succeeded, so the agent row exists, but
		// no pubkeys are bound. Same semantic failure as orphan agent —
		// no usable identity material — so map to ErrOrphanAgent.
		return fmt.Errorf("%w: commit %s by %s (agent_id=%d, no_pubkeys)",
			ErrOrphanAgent, c.SHA, c.AuthorEmail, agent.ID)
	}
	var sigErr error
	verified := false
	for _, pubContent := range pubContents {
		err := VerifyAgentSignatureSSH(c.Raw, pubContent)
		if err == nil {
			verified = true
			break
		}
		// ErrSignatureMissing is a property of the commit (no gpgsig
		// header), not of any one key — trying additional keys can't
		// change the outcome. Short-circuit.
		if errors.Is(err, ErrSignatureMissing) {
			sigErr = err
			break
		}
		// Track the most recent ErrInvalidSignature; if all keys fail
		// with it, that's the error we return.
		sigErr = err
	}
	if !verified {
		// VerifyAgentSignatureSSH returns ErrSignatureMissing or ErrInvalidSignature.
		if errors.Is(sigErr, ErrInvalidSignature) && len(pubContents) > 1 {
			return fmt.Errorf("%w: commit %s (tried %d keys)", sigErr, c.SHA, len(pubContents))
		}
		return fmt.Errorf("%w: commit %s", sigErr, c.SHA)
	}

	ownerUsername, err := svc.UsernameByID(ctx, agent.UserID)
	if err != nil {
		return fmt.Errorf("cairn hook: resolve owner for agent %d: %w", agent.ID, err)
	}
	if err := cairnidentity.VerifyTrailers(c.Message, agent, ownerUsername); err != nil {
		return fmt.Errorf("%w: commit %s", err, c.SHA)
	}

	return nil
}
