package vortex

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// mapped is a file mapped in memory: the scans read its segments where they are, without a
// copy. The memory is given back when the file is closed and no scan reads it any more.
type mapped struct {
	unmap func([]byte) error
	data  []byte
	mu    sync.Mutex
	scans int  // the scans that read the memory
	done  bool // Close was called: no scan starts, and the memory goes with the last one
}

// ReadAt copies the bytes at off: what is read once, the footer and the tables, is read like
// the bytes of any reader.
func (m *mapped) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// view is the bytes from lo to hi, where they are.
func (m *mapped) view(lo, hi int64) []byte { return m.data[lo:hi:hi] }

// begin records a scan that reads the memory. It is false when the file is closed.
func (m *mapped) begin() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return false
	}
	m.scans++
	return true
}

// end records that a scan is over, and gives the memory back when it was the last one of a
// closed file.
func (m *mapped) end() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scans--; m.done && m.scans == 0 {
		_ = m.release() // nobody is left to tell
	}
}

// Close gives the memory back, now or when the last scan that reads it ends. File.Close calls
// it once.
func (m *mapped) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done = true; m.scans == 0 {
		return m.release()
	}
	return nil
}

func (m *mapped) release() error {
	data := m.data
	m.data = nil
	return m.unmap(data)
}

// mapFile maps the size bytes of a file in memory, for reading. A variable for the tests, and
// nil where the platform has no such call: see mapped_unix.go.
var mapFile func(f *os.File, size int64) (data []byte, unmap func([]byte) error, err error) = platformMap

// OpenMapped opens the Vortex file at path like OpenFile does, and maps it in memory: a scan
// reads the segments where they are, without a copy, so a scan that reads a part of the file
// (some columns, a range of rows, the rows a filter keeps) only touches that part. Close it
// when done.
//
// It is for a local file that nothing truncates while it is read. The operating system
// reports a mapped byte that it cannot read, a truncated file or a failing disk, by stopping
// the process, where OpenFile returns an error. The columns of a batch are views of the file:
// they are not read after Close, which gives the memory back once the scans still running
// have ended, each with an error that wraps os.ErrClosed.
//
// Where the platform cannot map a file, or refuses to map this one, OpenMapped is OpenFile.
func OpenMapped(path string) (*File, error) {
	if mapFile == nil {
		return OpenFile(path)
	}
	osf, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer osf.Close() // the mapping outlives the descriptor
	st, err := statFile(osf)
	if err != nil {
		return nil, err
	}
	data, unmap, err := mapFile(osf, st.Size())
	if err != nil {
		return OpenFile(path)
	}
	m := &mapped{data: data, unmap: unmap}
	f, err := Open(m, st.Size())
	if err != nil {
		_ = m.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	f.closer, f.mapped = m, m
	return f, nil
}
