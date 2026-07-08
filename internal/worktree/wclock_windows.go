//go:build windows

package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Windows has no flock, so use an O_EXCL lock FILE as a cross-process mutex,
// spinning (bounded) until it can be created. There is deliberately no
// stale-steal (that would itself race two would-be stealers), so a cairn that
// crashes mid-operation leaves .cairn/wc.lock behind and it must be removed by
// hand — rare, and safer than risking two concurrent writers. Fixes issue #81
// on Windows too (POSIX uses kernel flock in wclock_unix.go).
const (
	wcLockTimeout = 60 * time.Second
	wcLockPoll    = 30 * time.Millisecond
)

func openWCLock(cairnDir string) (*os.File, error) {
	p := filepath.Join(cairnDir, "wc.lock")
	deadline := time.Now().Add(wcLockTimeout)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("working copy lock held >%s; if no cairn is running, remove %s", wcLockTimeout, p)
		}
		time.Sleep(wcLockPoll)
	}
}

func closeWCLock(f *os.File) {
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}
