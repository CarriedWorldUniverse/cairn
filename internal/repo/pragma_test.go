package repo

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The server is the one cairn process with several genuine concurrent writers
// (gRPC API, Smart-HTTP, SSH receive-pack, the replica runner), and it opened
// SQLite with no per-connection settings at all (#174). These tests pin the
// two that matter, on EVERY pooled connection, not just the one that ran the
// schema DDL.

func openTestService(t *testing.T) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cairn.db")
	svc, err := Open(dbPath, filepath.Join(dir, "repos"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc, dbPath
}

// pinConns takes n DISTINCT connections out of the pool and keeps them all open
// so each PRAGMA/INSERT below really runs on a different connection.
func pinConns(t *testing.T, db *sql.DB, n int) []*sql.Conn {
	t.Helper()
	ctx := context.Background()
	conns := make([]*sql.Conn, 0, n)
	for i := 0; i < n; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	t.Cleanup(func() {
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return conns
}

// Every pooled connection must carry both pragmas. Before the fix only the
// connection that happened to execute schema.sql had foreign_keys on, and
// none had a busy_timeout.
func TestOpenAppliesPragmasToEveryPooledConnection(t *testing.T) {
	svc, _ := openTestService(t)
	for i, c := range pinConns(t, svc.db, 6) {
		var fk, busy int
		if err := c.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatalf("conn %d: PRAGMA foreign_keys: %v", i, err)
		}
		if err := c.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busy); err != nil {
			t.Fatalf("conn %d: PRAGMA busy_timeout: %v", i, err)
		}
		if fk != 1 {
			t.Fatalf("conn %d: foreign_keys = %d, want 1 — enforcement depends on which pooled connection serves a request", i, fk)
		}
		if busy != 5000 {
			t.Fatalf("conn %d: busy_timeout = %d, want 5000 — a contended write fails immediately instead of waiting", i, busy)
		}
	}
}

// A dangling repo_id must be rejected no matter which connection serves the
// insert. This is the user-visible half of the foreign-keys inconsistency.
func TestForeignKeysRejectDanglingRepoOnEveryConnection(t *testing.T) {
	svc, _ := openTestService(t)
	for i, c := range pinConns(t, svc.db, 6) {
		_, err := c.ExecContext(context.Background(),
			`INSERT INTO push_event(id, repo_id, ref, old_sha, new_sha, pusher_agent_id, forced, at)
			 VALUES(?, 'no-such-repo', 'refs/heads/x', '0', '1', 'agent', 0, '2026-01-01T00:00:00Z')`,
			"evt-"+string(rune('a'+i)))
		if err == nil {
			t.Fatalf("conn %d accepted a push_event with a dangling repo_id — foreign keys are not enforced on this connection", i)
		}
		if !strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY") {
			t.Fatalf("conn %d: unexpected error (want a FOREIGN KEY failure): %v", i, err)
		}
	}
}

// A writer that finds the lock held must WAIT, not fail. Holding a write
// transaction on one connection, a write on another must still be blocked when
// its own 1.5 s deadline expires — not back within milliseconds with
// "database is locked". The control below opens the same file with a plain
// DSN and shows the immediate failure the fix removes.
func TestWritersWaitForTheLockInsteadOfFailing(t *testing.T) {
	svc, dbPath := openTestService(t)
	holder, err := svc.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("BEGIN IMMEDIATE: %v", err)
	}
	defer func() { _, _ = holder.ExecContext(context.Background(), `ROLLBACK`) }()

	// Contended writer through the fixed pool: must still be waiting at 1.5 s.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = svc.db.ExecContext(ctx, `INSERT INTO repo(id, org_id, slug, storage_path, created_at, updated_at) VALUES('w','o','s','/p','t','t')`)
	waited := time.Since(start)
	if err == nil {
		t.Fatal("write succeeded while another connection held the write lock")
	}
	if waited < time.Second {
		t.Fatalf("contended write failed after %s: %v — it should have waited for the lock (busy_timeout), not failed immediately", waited, err)
	}

	// Control: the SAME file with no busy_timeout fails immediately. This is
	// what every server write did before the fix.
	plain, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("plain open: %v", err)
	}
	defer plain.Close()
	start = time.Now()
	_, err = plain.ExecContext(context.Background(), `INSERT INTO repo(id, org_id, slug, storage_path, created_at, updated_at) VALUES('w2','o','s','/p','t','t')`)
	instant := time.Since(start)
	if err == nil {
		t.Fatal("control: plain write succeeded while the lock was held")
	}
	if instant > 500*time.Millisecond {
		t.Fatalf("control: plain write took %s to fail; expected an immediate SQLITE_BUSY without busy_timeout", instant)
	}
}

// DeleteRepo cascades pull_check/pull_request/push_event by hand but never
// touched embargo_recipient. With foreign keys enforced on every connection,
// the schema's ON DELETE CASCADE now removes those rows itself — so deleting a
// repo no longer strands its recipient grants.
func TestDeleteRepoCascadesEmbargoRecipients(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := context.Background()
	r, err := svc.CreateRepo(ctx, "org", "slug")
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if err := svc.GrantEmbargoRecipient(ctx, r.ID, "agent-1", "operator"); err != nil {
		t.Fatalf("GrantEmbargoRecipient: %v", err)
	}
	if err := svc.DeleteRepo(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	var n int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embargo_recipient WHERE repo_id=?`, r.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d embargo_recipient rows survived DeleteRepo — ON DELETE CASCADE is not being enforced", n)
	}
}
