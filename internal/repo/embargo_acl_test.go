package repo

import (
	"context"
	"testing"
)

// TestEmbargoRecipientACL exercises the recipient ACL CRUD: grant (idempotent),
// membership check, list, and revoke (idempotent, future-only).
func TestEmbargoRecipientACL(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatal(err)
	}

	// Nobody is a recipient initially.
	if ok, err := svc.IsEmbargoRecipient(ctx, r.ID, "agent-7"); err != nil || ok {
		t.Fatalf("IsEmbargoRecipient before grant = (%v,%v), want (false,nil)", ok, err)
	}

	// Grant is idempotent (a re-grant must not error or duplicate).
	if err := svc.GrantEmbargoRecipient(ctx, r.ID, "agent-7", "ops"); err != nil {
		t.Fatalf("GrantEmbargoRecipient: %v", err)
	}
	if err := svc.GrantEmbargoRecipient(ctx, r.ID, "agent-7", "ops"); err != nil {
		t.Fatalf("GrantEmbargoRecipient (re-grant): %v", err)
	}
	if ok, err := svc.IsEmbargoRecipient(ctx, r.ID, "agent-7"); err != nil || !ok {
		t.Fatalf("IsEmbargoRecipient after grant = (%v,%v), want (true,nil)", ok, err)
	}

	// A second grant + list returns both, sorted.
	if err := svc.GrantEmbargoRecipient(ctx, r.ID, "agent-3", "ops"); err != nil {
		t.Fatalf("GrantEmbargoRecipient agent-3: %v", err)
	}
	got, err := svc.ListEmbargoRecipients(ctx, r.ID)
	if err != nil {
		t.Fatalf("ListEmbargoRecipients: %v", err)
	}
	if len(got) != 2 || got[0] != "agent-3" || got[1] != "agent-7" {
		t.Fatalf("ListEmbargoRecipients = %v, want [agent-3 agent-7]", got)
	}

	// Revoke is idempotent and only affects the named agent.
	if err := svc.RevokeEmbargoRecipient(ctx, r.ID, "agent-7"); err != nil {
		t.Fatalf("RevokeEmbargoRecipient: %v", err)
	}
	if err := svc.RevokeEmbargoRecipient(ctx, r.ID, "agent-7"); err != nil {
		t.Fatalf("RevokeEmbargoRecipient (re-revoke): %v", err)
	}
	if ok, _ := svc.IsEmbargoRecipient(ctx, r.ID, "agent-7"); ok {
		t.Error("agent-7 still a recipient after revoke")
	}
	if ok, _ := svc.IsEmbargoRecipient(ctx, r.ID, "agent-3"); !ok {
		t.Error("agent-3 wrongly revoked")
	}
}

// TestBareForServe proves the gate's bare selection: only an authorized recipient
// fetching (git-upload-pack) over a repo that actually has an embargo bare is
// pointed at the embargo bare; every other combination falls back to the public
// bare (the frozen projection).
func TestBareForServe(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatal(err)
	}
	pub := svc.storagePath(r.ID)
	emb := svc.EmbargoStoragePath(r.ID)

	// A recipient grant alone (no embargo bare yet) → still public: nothing gated.
	if err := svc.GrantEmbargoRecipient(ctx, r.ID, "agent-7", "ops"); err != nil {
		t.Fatal(err)
	}
	if got := svc.BareForServe(ctx, r.ID, "agent-7", "git-upload-pack"); got != pub {
		t.Fatalf("no embargo bare: BareForServe = %s, want public %s", got, pub)
	}

	// Create the embargo bare. Now the recipient's fetch resolves to it.
	if err := svc.ensureEmbargoBare(r.ID); err != nil {
		t.Fatal(err)
	}
	if got := svc.BareForServe(ctx, r.ID, "agent-7", "git-upload-pack"); got != emb {
		t.Fatalf("recipient fetch: BareForServe = %s, want embargo %s", got, emb)
	}

	// A non-recipient fetch stays on the public (frozen) bare.
	if got := svc.BareForServe(ctx, r.ID, "stranger", "git-upload-pack"); got != pub {
		t.Fatalf("non-recipient fetch: BareForServe = %s, want public %s", got, pub)
	}

	// A push (git-receive-pack) ALWAYS targets the public bare, even for a
	// recipient — the client pushes embargo refs there and post-receive relocates.
	if got := svc.BareForServe(ctx, r.ID, "agent-7", "git-receive-pack"); got != pub {
		t.Fatalf("recipient push: BareForServe = %s, want public %s", got, pub)
	}
}
