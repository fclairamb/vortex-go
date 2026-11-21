package vortex

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeSelFile writes the file of the scans that keep few rows to a temporary directory.
func writeSelFile(t *testing.T, c Compression) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sel.vortex")
	if err := os.WriteFile(path, selFile(t, c), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestOpenMapped checks that a mapped file gives the rows of the same file read through a
// reader: every row, some columns, a range, and a filter that keeps few rows, with one thread
// and with several, in the 3 compression modes.
func TestOpenMapped(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	cols := []string{"f", "a", "p", "city", "note", "n", "s"}
	for _, comp := range []Compression{Default, None, Compact} {
		path := writeSelFile(t, comp)
		plain, err := OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m, err := OpenMapped(path)
		if err != nil {
			t.Fatal(err)
		}
		if m.mapped == nil || m.NumRows() != plain.NumRows() {
			t.Fatalf("mapped: %v, %d rows", m.mapped != nil, m.NumRows())
		}
		for name, q := range map[string]struct {
			q    Query
			cols []string
		}{
			"all":    {Query{}, cols},
			"some":   {Query{}, []string{"p", "city"}},
			"range":  {Query{Rows: Range{Start: 25_000, End: 29_000}}, cols},
			"filter": {Query{Filter: Col("f").Lt(int32(10))}, cols[1:]},
		} {
			want, err := digestOf(plain, q.q, nil, q.cols)
			if err != nil {
				t.Fatal(err)
			}
			for _, procs := range []int{1, 4} {
				runtime.GOMAXPROCS(procs)
				if got, err := digestOf(m, q.q, nil, q.cols); err != nil || got != want {
					t.Errorf("compression %d, %s, %d threads: other rows (%v)", comp, name, procs, err)
				}
			}
		}
		if err := errors.Join(m.Close(), m.Close(), plain.Close()); err != nil {
			t.Fatal(err)
		}
		if _, err := digestOf(m, Query{}, nil, cols); !errors.Is(err, os.ErrClosed) {
			t.Errorf("a scan of a closed mapped file: %v", err)
		}
	}
}

// TestOpenMappedCloseWhileScanning closes a mapped file in the middle of a scan: the scan
// ends with os.ErrClosed, and the memory is only given back once it has.
func TestOpenMappedCloseWhileScanning(t *testing.T) {
	m, err := OpenMapped(writeSelFile(t, None))
	if err != nil {
		t.Fatal(err)
	}
	batches := 0
	var last error
	for b, err := range m.scan(context.Background(), &scanOptions{batchRows: 1000}) {
		if last = err; err != nil {
			break
		}
		if batches++; batches == 3 {
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			if m.mapped.data == nil {
				t.Fatal("the memory was given back while a scan reads it")
			}
			v, _ := Values[int32](b.Col(0)) // the batch is still a view of the file
			_ = v[b.Len()-1]
		}
	}
	if !errors.Is(last, os.ErrClosed) || batches != 3 {
		t.Fatalf("%d batches, then %v", batches, last)
	}
	if m.mapped.data != nil {
		t.Fatal("the memory was not given back with the last scan")
	}
}

// TestOpenMappedFallsBack checks the files that are not mapped: where the platform has no call
// for it, when the call fails, and the errors of OpenFile for the files that cannot be opened.
func TestOpenMappedFallsBack(t *testing.T) {
	path := writeSelFile(t, None)
	for name, mapper := range map[string]func(*os.File, int64) ([]byte, func([]byte) error, error){
		"no call": nil,
		"refused": func(*os.File, int64) ([]byte, func([]byte) error, error) { return nil, nil, errors.New("no") },
	} {
		lowered(t, &mapFile, mapper)
		f, err := OpenMapped(path)
		if err != nil || f.mapped != nil || f.NumRows() != 60_000 {
			t.Fatalf("%s: %v", name, err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	lowered(t, &mapFile, platformMap)
	if _, err := OpenMapped(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing file: %v", err)
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenMapped(empty); !errors.Is(err, ErrNotVortex) { // not mapped: OpenFile says what it is
		t.Errorf("an empty file: %v", err)
	}
	junk := filepath.Join(t.TempDir(), "junk")
	if err := os.WriteFile(junk, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenMapped(junk); !errors.Is(err, ErrNotVortex) {
		t.Errorf("a file that is not a Vortex file: %v", err)
	}
	lowered(t, &statFile, func(*os.File) (os.FileInfo, error) { return nil, errors.New("no stat") })
	if _, err := OpenMapped(path); err == nil {
		t.Error("a file that cannot be measured is opened")
	}
}

// TestMappedReadAt checks the reads of a mapped file through its reader.
func TestMappedReadAt(t *testing.T) {
	m := &mapped{data: []byte("abcdef"), unmap: func([]byte) error { return nil }}
	p := make([]byte, 4)
	if n, err := m.ReadAt(p, 1); n != 4 || err != nil || string(p) != "bcde" {
		t.Fatalf("%d, %v, %q", n, err, p)
	}
	if n, err := m.ReadAt(p, 4); n != 2 || err != io.EOF {
		t.Fatalf("%d, %v", n, err)
	}
	for _, off := range []int64{-1, 6, 99} {
		if n, err := m.ReadAt(p, off); n != 0 || err != io.EOF {
			t.Fatalf("offset %d: %d, %v", off, n, err)
		}
	}
}
