//go:build !unix

package vortex

import "os"

// platformMap is nil: the platform has no call to map a file that the standard library
// offers, and OpenMapped is OpenFile.
var platformMap func(f *os.File, size int64) ([]byte, func([]byte) error, error)
