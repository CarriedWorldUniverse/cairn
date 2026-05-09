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
// Returns the first commit-level failure with a wrapped rejection
// reason; vanilla (non-agent) commits are skipped.
func VerifyAgentCommits(
	ctx context.Context,
	commits []CommitToVerify,
	svc *cairnidentity.AgentService,
	enforce bool,
) error {
	if !enforce {
		return nil
	}
	for _, c := range commits {
		if err := verifyOne(ctx, c, svc); err != nil {
			return err
		}
	}
	return nil
}

func verifyOne(ctx context.Context, c CommitToVerify, svc *cairnidentity.AgentService) error {
	slug, domain, isAgent := cairnidentity.ParseAgentEmail(c.AuthorEmail)
	if !isAgent {
		// Non-agent commit — vanilla Forgejo handles it; skip.
		return nil
	}

	agent, err := svc.GetByEmail(ctx, slug, domain)
	if err != nil {
		if errors.Is(err, cairnidentity.ErrAgentNotFound) {
			return fmt.Errorf("%w: commit %s author %s (slug=%s domain=%s)",
				ErrOrphanAgent, c.SHA, c.AuthorEmail, slug, domain)
		}
		return err
	}

	if agent.Status != cairn.AgentStatusActive {
		return fmt.Errorf("%w: commit %s by %s (status=%s)",
			ErrAgentNotActive, c.SHA, c.AuthorEmail, agent.Status)
	}

	blocked, err := svc.IsBlocked(ctx, agent.Fingerprint)
	if err != nil {
		return fmt.Errorf("cairn hook: check blocklist for %s: %w", agent.Fingerprint, err)
	}
	if blocked {
		return fmt.Errorf("%w: commit %s by %s (fingerprint=%s)",
			ErrAgentBlocked, c.SHA, c.AuthorEmail, agent.Fingerprint)
	}

	if err := VerifyAgentSignature(c.Raw, agent.PublicKey); err != nil {
		// VerifyAgentSignature returns ErrSignatureMissing or ErrInvalidSignature.
		return fmt.Errorf("%w: commit %s", err, c.SHA)
	}

	ownerUsername, err := svc.UsernameByID(ctx, agent.UserID)
	if err != nil {
		return fmt.Errorf("cairn hook: resolve owner for %s: %w", agent.Fingerprint, err)
	}
	if err := cairnidentity.VerifyTrailers(c.Message, agent, ownerUsername); err != nil {
		return fmt.Errorf("%w: commit %s", err, c.SHA)
	}

	return nil
}
