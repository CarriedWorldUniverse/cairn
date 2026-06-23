//go:build linux

package worktree

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// reflinkOrCopy clones src to dst using the FICLONE ioctl (copy-on-write
// block sharing) when the underlying filesystem supports it, falling back
// to a plain byte copy otherwise. dst is created (or truncated) with mode
// 0o644.
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

	err = unix.IoctlFileClone(int(out.Fd()), int(in.Fd()))
	if err == nil {
		return nil
	}
	if !isCloneUnsupported(err) {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}

	// Fall back to a plain copy. The clone ioctl leaves dst unchanged on
	// failure, but reset offset/length defensively before copying.
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}
	if err := out.Truncate(0); err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}
	return nil
}

// isCloneUnsupported reports whether err indicates the FICLONE ioctl is not
// available for the given source/destination (filesystem unsupported,
// cross-device, or invalid argument), as opposed to a genuine I/O failure.
func isCloneUnsupported(err error) bool {
	return errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.EINVAL)
}

// reflinkSupported reports whether dir resides on a filesystem that supports
// reflink (FICLONE) clones. It probes by cloning a throwaway temp file to a
// sibling and reporting whether the ioctl succeeded.
func reflinkSupported(dir string) bool {
	in, err := os.CreateTemp(dir, ".reflink-probe-src-*")
	if err != nil {
		return false
	}
	defer os.Remove(in.Name())
	defer in.Close()

	if _, err := in.Write([]byte{0}); err != nil {
		return false
	}

	dst := filepath.Join(dir, filepath.Base(in.Name())+".dst")
	out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return false
	}
	defer os.Remove(dst)
	defer out.Close()

	return unix.IoctlFileClone(int(out.Fd()), int(in.Fd())) == nil
}
