// Package replica wires push-triggered pack-store replication into the
// cairn server. Replication is asynchronous and off the push path: the
// post-receive hook (a short-lived separate process) only marks the pushed
// repo dirty in a spool directory; a debounced Runner inside the long-running
// server process consumes the spool on a ticker and snapshots each dirty repo
// into an encrypted pack store by shelling out to porterpack's
// `repo-snapshot` verb. porterpack is a no-op when refs haven't changed, so
// over-marking is harmless; a push landing mid-snapshot re-marks the repo and
// the Runner keeps the fresh marker for the next tick (it only clears a
// marker it saw unchanged across the whole snapshot).
package replica

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Spool is a directory of dirty-repo markers: one file per repo id, named
// after the repo id, containing the bare repo's absolute path. Mark is called
// by the post-receive subcommand; Pending/Clear are called by the Runner.
type Spool struct{ Dir string }

// Mark records repoID as dirty with the given repoPath, via an atomic
// temp-file-then-rename write. A later Mark for the same repoID overwrites
// the path.
func (s Spool) Mark(repoID, repoPath string) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("replica: spool mkdir: %w", err)
	}
	final := filepath.Join(s.Dir, repoID)
	tmp := final + ".tmp-" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, []byte(repoPath), 0o600); err != nil {
		return fmt.Errorf("replica: spool write %s: %w", repoID, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replica: spool rename %s: %w", repoID, err)
	}
	return nil
}

// Pending returns every dirty repo id currently marked, mapped to its bare
// repo path. A missing spool directory is treated as empty, not an error.
func (s Spool) Pending() (map[string]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("replica: spool readdir: %w", err)
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.Contains(e.Name(), ".tmp-") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			// A marker vanishing between ReadDir and ReadFile (e.g. concurrent
			// Clear) is not fatal — just skip it.
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("replica: spool read %s: %w", e.Name(), err)
		}
		out[e.Name()] = string(b)
	}
	return out, nil
}

// Clear removes repoID's marker. A missing marker is not an error.
func (s Spool) Clear(repoID string) error {
	err := os.Remove(filepath.Join(s.Dir, repoID))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replica: spool clear %s: %w", repoID, err)
	}
	return nil
}

// Config is the replication runner's configuration. The zero value is
// disabled (Enabled reports false).
type Config struct {
	Bin        string        // porterpack binary path
	Store      string        // localdir pack-store path
	KeyFile    string        // recipient private key file
	Recipients []string      // recipient pubkey files (>=1)
	Spool      string        // spool dir
	Interval   time.Duration // debounce tick
}

// ConfigFromEnv builds a Config from CAIRN_REPLICA_* env vars:
//
//	CAIRN_REPLICA_BIN        porterpack binary path
//	CAIRN_REPLICA_STORE      localdir pack-store path
//	CAIRN_REPLICA_KEY        recipient private key file
//	CAIRN_REPLICA_RECIPIENTS comma-separated recipient pubkey files
//	CAIRN_REPLICA_SPOOL      spool directory (must match the post-receive hook's)
//	CAIRN_REPLICA_INTERVAL   Go duration string, default 60s
//
// Replication is opt-in: BIN, STORE, KEY, RECIPIENTS and SPOOL must ALL be
// set, else ConfigFromEnv returns a disabled zero Config. If some but not all
// of those five are set, a warning names the missing ones (likely
// misconfiguration) before returning disabled.
func ConfigFromEnv() Config {
	bin := os.Getenv("CAIRN_REPLICA_BIN")
	store := os.Getenv("CAIRN_REPLICA_STORE")
	key := os.Getenv("CAIRN_REPLICA_KEY")
	recipients := os.Getenv("CAIRN_REPLICA_RECIPIENTS")
	spool := os.Getenv("CAIRN_REPLICA_SPOOL")

	set := map[string]string{
		"CAIRN_REPLICA_BIN":        bin,
		"CAIRN_REPLICA_STORE":      store,
		"CAIRN_REPLICA_KEY":        key,
		"CAIRN_REPLICA_RECIPIENTS": recipients,
		"CAIRN_REPLICA_SPOOL":      spool,
	}
	nSet := 0
	var missing []string
	for name, v := range set {
		if v != "" {
			nSet++
		} else {
			missing = append(missing, name)
		}
	}
	if nSet == 0 {
		return Config{}
	}
	if nSet < len(set) {
		sort.Strings(missing)
		log.Printf("replica: partial CAIRN_REPLICA_* config — missing %s — replication disabled", strings.Join(missing, ", "))
		return Config{}
	}

	interval := 60 * time.Second
	if v := os.Getenv("CAIRN_REPLICA_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Printf("replica: CAIRN_REPLICA_INTERVAL=%q invalid (%v) — using default %s", v, err, interval)
		} else {
			interval = d
		}
	}

	var recips []string
	for _, r := range strings.Split(recipients, ",") {
		if r = strings.TrimSpace(r); r != "" {
			recips = append(recips, r)
		}
	}

	return Config{
		Bin:        bin,
		Store:      store,
		KeyFile:    key,
		Recipients: recips,
		Spool:      spool,
		Interval:   interval,
	}
}

// Enabled reports whether c is a usable (non-zero) configuration.
func (c Config) Enabled() bool {
	return c.Bin != "" && c.Store != "" && c.KeyFile != "" && len(c.Recipients) > 0 && c.Spool != ""
}

// snapshotTimeout bounds a single porterpack repo-snapshot invocation so a
// wedged snapshot can't block the runner forever.
const snapshotTimeout = 10 * time.Minute

// Run scans the spool every c.Interval and snapshots each pending repo into
// the pack store by shelling out to porterpack. It blocks until ctx is
// canceled. A repo's marker is cleared only after its snapshot succeeds; a
// failure is logged and the marker survives for retry on the next tick.
func (c Config) Run(ctx context.Context) {
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runOnce(ctx)
		}
	}
}

func (c Config) runOnce(ctx context.Context) {
	spool := Spool{Dir: c.Spool}
	pending, err := spool.Pending()
	if err != nil {
		log.Printf("replica: spool scan: %v", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	repoIDs := make([]string, 0, len(pending))
	for id := range pending {
		repoIDs = append(repoIDs, id)
	}
	sort.Strings(repoIDs)

	for _, repoID := range repoIDs {
		repoPath := pending[repoID]
		// Record the marker's identity before snapshotting: a push landing
		// mid-snapshot re-Marks (rename = fresh mtime), and clearing that
		// fresh marker would silently skip the new push until the one after.
		// Only Clear when the marker is still the file we started from.
		markerPath := filepath.Join(c.Spool, repoID)
		before, beforeErr := os.Stat(markerPath)
		if err := c.snapshot(ctx, repoID, repoPath); err != nil {
			log.Printf("replica: snapshot %s failed: %v", repoID, err)
			continue
		}
		after, afterErr := os.Stat(markerPath)
		if beforeErr != nil || afterErr != nil ||
			!after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
			log.Printf("replica: %s re-marked during snapshot; keeping marker for next tick", repoID)
			continue
		}
		if err := spool.Clear(repoID); err != nil {
			log.Printf("replica: clear marker %s: %v", repoID, err)
		}
	}
}

func (c Config) snapshot(ctx context.Context, repoID, repoPath string) error {
	cctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	args := []string{"repo-snapshot", "-store", c.Store, "-key", c.KeyFile}
	for _, r := range c.Recipients {
		args = append(args, "-recipient", r)
	}
	args = append(args, "-name", repoID, "-repo", repoPath)

	cmd := exec.CommandContext(cctx, c.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w (stderr: %s)", c.Bin, err, strings.TrimSpace(stderr.String()))
	}
	log.Printf("replica: snapshotted %s: %s", repoID, strings.TrimSpace(stdout.String()))
	return nil
}
