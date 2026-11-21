//go:build unix

package vortex

import (
	"errors"
	"os"
	"syscall"
)

// platformMap maps a file with mmap.
func platformMap(f *os.File, size int64) ([]byte, func([]byte) error, error) {
	if size <= 0 || int64(int(size)) != size {
		return nil, nil, errors.New("vortex: a file of this size is not mapped")
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	return data, syscall.Munmap, err
}
