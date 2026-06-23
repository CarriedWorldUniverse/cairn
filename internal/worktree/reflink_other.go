//go:build !linux

package worktree

import (
	"fmt"
	"io"
	"os"
)

// reflinkOrCopy copies src to dst with a plain byte copy. Reflink (FICLONE)
// cloning is Linux-only, so non-linux platforms always copy. dst is created
// (or truncated) with mode 0o644.
func reflinkOrCopy(src, dst string) error {
	in, err := os.OpenFile(src, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}
	return nil
}

// reflinkSupported always reports false on non-linux platforms, which have no
// FICLONE ioctl.
func reflinkSupported(string) bool { return false }
