package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

func TestMapRemoteErrAuth(t *testing.T) {
	got := mapRemoteErr(transport.ErrAuthenticationRequired)
	if got == nil || !strings.Contains(got.Error(), "CAIRN_TOKEN") {
		t.Fatalf("auth err not humanized: %v", got)
	}
}

func TestMapRemoteErrAuthWrapped(t *testing.T) {
	wrapped := fmt.Errorf("push: %w", transport.ErrAuthenticationRequired)
	got := mapRemoteErr(wrapped)
	if got == nil || !strings.Contains(got.Error(), "CAIRN_TOKEN") {
		t.Fatalf("wrapped auth err not humanized: %v", got)
	}
}

func TestMapRemoteErrAuthorizationFailed(t *testing.T) {
	got := mapRemoteErr(transport.ErrAuthorizationFailed)
	if got == nil || !strings.Contains(got.Error(), "CAIRN_TOKEN") {
		t.Fatalf("authorization failed err not humanized: %v", got)
	}
}

func TestMapRemoteErrNotFound(t *testing.T) {
	got := mapRemoteErr(transport.ErrRepositoryNotFound)
	if got == nil || !strings.Contains(got.Error(), "repository not found") {
		t.Fatalf("not-found not humanized: %v", got)
	}
}

func TestMapRemoteErrNotFoundWrapped(t *testing.T) {
	wrapped := fmt.Errorf("clone: %w", transport.ErrRepositoryNotFound)
	got := mapRemoteErr(wrapped)
	if got == nil || !strings.Contains(got.Error(), "repository not found") {
		t.Fatalf("wrapped not-found err not humanized: %v", got)
	}
}

func TestMapRemoteErrNetwork(t *testing.T) {
	got := mapRemoteErr(errors.New("dial tcp 1.2.3.4:443: connect: connection refused"))
	if got == nil || !strings.Contains(got.Error(), "could not reach") {
		t.Fatalf("network err not humanized: %v", got)
	}
}

func TestMapRemoteErrNetworkNoSuchHost(t *testing.T) {
	got := mapRemoteErr(errors.New("no such host: git.example.com"))
	if got == nil || !strings.Contains(got.Error(), "could not reach") {
		t.Fatalf("no-such-host err not humanized: %v", got)
	}
}

func TestMapRemoteErrFallThrough(t *testing.T) {
	// an unrecognized error passes through mapErr unchanged-ish (non-nil, original-ish)
	orig := errors.New("some other failure")
	got := mapRemoteErr(orig)
	if got == nil {
		t.Fatal("nil")
	}
}

func TestMapRemoteErrNil(t *testing.T) {
	if mapRemoteErr(nil) != nil {
		t.Fatal("nil should map to nil")
	}
}

// TestMapRemoteErrProtectedTeachesUndo: a protected-branch / hook-decline push
// rejection is humanized into guidance that teaches `cairn undo` and the
// push-line-then-PR recovery (the case you hit after folding into an upstream
// branch locally).
func TestMapRemoteErrProtectedTeachesUndo(t *testing.T) {
	for _, raw := range []string{
		"command error on refs/heads/main: GH006: Protected branch update failed",
		"pre-receive hook declined",
		"remote: error: protected branch hook declined",
	} {
		got := mapRemoteErr(errors.New(raw))
		if got == nil {
			t.Fatalf("nil for %q", raw)
		}
		if !strings.Contains(got.Error(), "undo") || !strings.Contains(strings.ToLower(got.Error()), "pull request") {
			t.Fatalf("rejection not humanized to teach undo+PR for %q: %v", raw, got)
		}
	}
}
