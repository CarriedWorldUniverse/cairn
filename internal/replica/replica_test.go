package replica

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSpoolRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := Spool{Dir: filepath.Join(dir, "spool")} // exercise the MkdirAll path

	if err := s.Mark("repo-a", "/repos/a.git"); err != nil {
		t.Fatalf("mark repo-a: %v", err)
	}
	if err := s.Mark("repo-b", "/repos/b.git"); err != nil {
		t.Fatalf("mark repo-b: %v", err)
	}

	pending, err := s.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	want := map[string]string{"repo-a": "/repos/a.git", "repo-b": "/repos/b.git"}
	if len(pending) != len(want) {
		t.Fatalf("pending = %v, want %v", pending, want)
	}
	for id, path := range want {
		if pending[id] != path {
			t.Errorf("pending[%s] = %q, want %q", id, pending[id], path)
		}
	}

	// Mark overwrite: same repoID, new path wins.
	if err := s.Mark("repo-a", "/repos/a-moved.git"); err != nil {
		t.Fatalf("re-mark repo-a: %v", err)
	}
	pending, err = s.Pending()
	if err != nil {
		t.Fatalf("pending after re-mark: %v", err)
	}
	if pending["repo-a"] != "/repos/a-moved.git" {
		t.Errorf("pending[repo-a] = %q, want overwritten path", pending["repo-a"])
	}

	if err := s.Clear("repo-a"); err != nil {
		t.Fatalf("clear repo-a: %v", err)
	}
	pending, err = s.Pending()
	if err != nil {
		t.Fatalf("pending after clear: %v", err)
	}
	if len(pending) != 1 || pending["repo-b"] != "/repos/b.git" {
		t.Errorf("pending after clear = %v, want only repo-b", pending)
	}

	// Clear of a missing marker is not an error.
	if err := s.Clear("does-not-exist"); err != nil {
		t.Errorf("clear missing marker: %v, want nil", err)
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Run("all set", func(t *testing.T) {
		t.Setenv("CAIRN_REPLICA_BIN", "/usr/local/bin/porterpack")
		t.Setenv("CAIRN_REPLICA_STORE", "/mnt/packstore")
		t.Setenv("CAIRN_REPLICA_KEY", "/etc/cairn/replica.key")
		t.Setenv("CAIRN_REPLICA_RECIPIENTS", "/etc/cairn/a.pub, /etc/cairn/b.pub")
		t.Setenv("CAIRN_REPLICA_SPOOL", "/var/lib/cairn/replica-spool")
		t.Setenv("CAIRN_REPLICA_INTERVAL", "5s")

		c := ConfigFromEnv()
		if !c.Enabled() {
			t.Fatal("Enabled() = false, want true")
		}
		if c.Bin != "/usr/local/bin/porterpack" {
			t.Errorf("Bin = %q", c.Bin)
		}
		if c.Store != "/mnt/packstore" {
			t.Errorf("Store = %q", c.Store)
		}
		if c.KeyFile != "/etc/cairn/replica.key" {
			t.Errorf("KeyFile = %q", c.KeyFile)
		}
		wantRecips := []string{"/etc/cairn/a.pub", "/etc/cairn/b.pub"}
		if len(c.Recipients) != len(wantRecips) {
			t.Fatalf("Recipients = %v", c.Recipients)
		}
		for i, r := range wantRecips {
			if c.Recipients[i] != r {
				t.Errorf("Recipients[%d] = %q, want %q", i, c.Recipients[i], r)
			}
		}
		if c.Spool != "/var/lib/cairn/replica-spool" {
			t.Errorf("Spool = %q", c.Spool)
		}
		if c.Interval != 5*time.Second {
			t.Errorf("Interval = %v, want 5s", c.Interval)
		}
	})

	t.Run("none set", func(t *testing.T) {
		for _, k := range []string{
			"CAIRN_REPLICA_BIN", "CAIRN_REPLICA_STORE", "CAIRN_REPLICA_KEY",
			"CAIRN_REPLICA_RECIPIENTS", "CAIRN_REPLICA_SPOOL", "CAIRN_REPLICA_INTERVAL",
		} {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
		c := ConfigFromEnv()
		if c.Enabled() {
			t.Errorf("Enabled() = true, want false for zero Config: %+v", c)
		}
	})

	t.Run("partial set", func(t *testing.T) {
		t.Setenv("CAIRN_REPLICA_BIN", "/usr/local/bin/porterpack")
		t.Setenv("CAIRN_REPLICA_STORE", "/mnt/packstore")
		for _, k := range []string{"CAIRN_REPLICA_KEY", "CAIRN_REPLICA_RECIPIENTS", "CAIRN_REPLICA_SPOOL"} {
			os.Unsetenv(k)
		}
		c := ConfigFromEnv()
		if c.Enabled() {
			t.Errorf("Enabled() = true, want false for partial config: %+v", c)
		}
	})
}

// writeFakePorterpack writes a shell script that appends its invocation
// arguments (one line, space-joined) to logFile and exits with the given
// status. The status can be changed later by rewriting the same path.
func writeFakePorterpack(t *testing.T, path, logFile string, exitCode int) {
	t.Helper()
	script := "#!/bin/sh\necho \"$@\" >> " + logFile + "\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake porterpack: %v", err)
	}
}

// waitForLog polls logFile until it contains at least wantLines lines, or
// fails the test at the deadline.
func waitForLog(t *testing.T, logFile string, wantLines int, deadline time.Duration) string {
	t.Helper()
	end := time.Now().Add(deadline)
	var last string
	for time.Now().Before(end) {
		b, err := os.ReadFile(logFile)
		if err == nil {
			last = string(b)
			lines := 0
			for _, c := range last {
				if c == '\n' {
					lines++
				}
			}
			if lines >= wantLines {
				return last
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d line(s) in %s; last content: %q", wantLines, logFile, last)
	return ""
}

func TestRunnerEndToEnd(t *testing.T) {
	dir := t.TempDir()
	spoolDir := filepath.Join(dir, "spool")
	logFile := filepath.Join(dir, "invocations.log")
	fakeBin := filepath.Join(dir, "porterpack")
	writeFakePorterpack(t, fakeBin, logFile, 0)

	spool := Spool{Dir: spoolDir}
	repoPath := filepath.Join(dir, "repos", "widgets.git")
	if err := spool.Mark("widgets", repoPath); err != nil {
		t.Fatalf("mark: %v", err)
	}

	cfg := Config{
		Bin:        fakeBin,
		Store:      filepath.Join(dir, "store"),
		KeyFile:    filepath.Join(dir, "key.priv"),
		Recipients: []string{filepath.Join(dir, "r1.pub"), filepath.Join(dir, "r2.pub")},
		Spool:      spoolDir,
		Interval:   20 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		cfg.Run(ctx)
		close(done)
	}()

	content := waitForLog(t, logFile, 1, 5*time.Second)
	cancel()
	<-done

	wantArgs := "repo-snapshot -store " + cfg.Store + " -key " + cfg.KeyFile +
		" -recipient " + cfg.Recipients[0] + " -recipient " + cfg.Recipients[1] +
		" -name widgets -repo " + repoPath
	if got := trimOneLine(content); got != wantArgs {
		t.Errorf("porterpack invoked with %q, want %q", got, wantArgs)
	}

	pending, err := spool.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending after success = %v, want empty (marker cleared)", pending)
	}
}

func trimOneLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

func TestRunnerFailureRetry(t *testing.T) {
	dir := t.TempDir()
	spoolDir := filepath.Join(dir, "spool")
	logFile := filepath.Join(dir, "invocations.log")
	fakeBin := filepath.Join(dir, "porterpack")
	writeFakePorterpack(t, fakeBin, logFile, 1) // fails first

	spool := Spool{Dir: spoolDir}
	repoPath := filepath.Join(dir, "repos", "gadgets.git")
	if err := spool.Mark("gadgets", repoPath); err != nil {
		t.Fatalf("mark: %v", err)
	}

	cfg := Config{
		Bin:        fakeBin,
		Store:      filepath.Join(dir, "store"),
		KeyFile:    filepath.Join(dir, "key.priv"),
		Recipients: []string{filepath.Join(dir, "r1.pub")},
		Spool:      spoolDir,
		Interval:   20 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run one tick manually via runOnce to control timing deterministically.
	cfg.runOnce(ctx)
	pending, err := spool.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if _, ok := pending["gadgets"]; !ok {
		t.Fatalf("marker cleared after failed snapshot, want it to survive: %v", pending)
	}

	// Flip the fake to succeed and rerun a tick.
	writeFakePorterpack(t, fakeBin, logFile, 0)
	cfg.runOnce(ctx)
	pending, err = spool.Pending()
	if err != nil {
		t.Fatalf("pending after success: %v", err)
	}
	if _, ok := pending["gadgets"]; ok {
		t.Errorf("marker survives after successful snapshot, want cleared: %v", pending)
	}
	if _, err := os.Stat(logFile); errors.Is(err, os.ErrNotExist) {
		t.Fatalf("log file missing after retry")
	}
}

// TestRunnerKeepsRemarkedMarker pins the mid-snapshot re-mark race: when a
// push lands while a snapshot is running (the fake porterpack re-Marks the
// repo itself), the runner must NOT clear the fresh marker — the re-marked
// push replicates on the next tick, not the next push.
func TestRunnerKeepsRemarkedMarker(t *testing.T) {
	dir := t.TempDir()
	spoolDir := filepath.Join(dir, "spool")
	logFile := filepath.Join(dir, "invocations.log")
	fakeBin := filepath.Join(dir, "porterpack")

	spool := Spool{Dir: spoolDir}
	repoPath := filepath.Join(dir, "repos", "widgets.git")
	if err := spool.Mark("widgets", repoPath); err != nil {
		t.Fatalf("mark: %v", err)
	}

	// Fake porterpack that simulates a concurrent push: it rewrites the
	// marker (rename, like Spool.Mark) before exiting successfully.
	marker := filepath.Join(spoolDir, "widgets")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + logFile + "\n" +
		"echo " + repoPath + " > " + marker + ".tmp-remark\n" +
		"mv " + marker + ".tmp-remark " + marker + "\n" +
		"exit 0\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake porterpack: %v", err)
	}

	cfg := Config{
		Bin:        fakeBin,
		Store:      filepath.Join(dir, "store"),
		KeyFile:    filepath.Join(dir, "key.priv"),
		Recipients: []string{filepath.Join(dir, "r1.pub")},
		Spool:      spoolDir,
		Interval:   20 * time.Millisecond,
	}
	cfg.runOnce(context.Background())

	pending, err := spool.Pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if _, ok := pending["widgets"]; !ok {
		t.Fatalf("re-marked marker was cleared; the mid-snapshot push would be lost until the next push")
	}
}
