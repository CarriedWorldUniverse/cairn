//go:build !unix

package packread

import "os"

// mapFile never maps on platforms without mmap(2); callers fall back to
// reading through the file handle.
func mapFile(f *os.File, size int64) ([]byte, error) { return nil, nil }

func unmapFile(data []byte) {}
