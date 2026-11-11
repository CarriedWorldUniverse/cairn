//go:build unix

package packread

import (
	"os"
	"syscall"
)

// mapFile maps f read-only. A nil slice with a nil error means "not mapped":
// the caller reads through the file handle instead.
func mapFile(f *os.File, size int64) ([]byte, error) {
	if size <= 0 || int64(int(size)) != size {
		return nil, nil
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		// Address space too small (a large pack on a 32-bit platform), or a
		// filesystem that cannot map: not an error, just the slower path.
		return nil, nil
	}
	return data, nil
}

func unmapFile(data []byte) {
	if data != nil {
		_ = syscall.Munmap(data)
	}
}
