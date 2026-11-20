package testutil

import (
	"context"
	"io"
	"sync/atomic"
)

// FailAt is an io.ReaderAt whose call N (from 1) fails with Err. The other calls go to R. It
// is safe for concurrent use.
type FailAt struct {
	R     io.ReaderAt
	Err   error
	N     int64
	calls atomic.Int64
}

// ReadAt reads from R, except on call N.
func (f *FailAt) ReadAt(p []byte, off int64) (int, error) {
	if f.calls.Add(1) == f.N {
		return 0, f.Err
	}
	return f.R.ReadAt(p, off)
}

// Calls is the number of calls so far.
func (f *FailAt) Calls() int64 { return f.calls.Load() }

// Short is an io.ReaderAt that returns at most Max bytes per call, and no error: a reader
// that has to be called again for the rest.
type Short struct {
	R   io.ReaderAt
	Max int
}

// ReadAt reads at most Max bytes from R.
func (s Short) ReadAt(p []byte, off int64) (int, error) {
	n, err := s.R.ReadAt(p[:min(len(p), s.Max)], off)
	if n > 0 {
		return n, nil
	}
	return n, err
}

// EOFAtEnd is an io.ReaderAt over the Size bytes of R that returns io.EOF with the last bytes,
// as io.ReaderAt allows.
type EOFAtEnd struct {
	R    io.ReaderAt
	Size int64
}

// ReadAt reads from R, with io.EOF when the read reaches the end.
func (e EOFAtEnd) ReadAt(p []byte, off int64) (int, error) {
	n, err := e.R.ReadAt(p, off)
	if err == nil && off+int64(n) == e.Size {
		err = io.EOF
	}
	return n, err
}

// Blocking is an io.ReaderAt whose calls block until Ctx is done, then return its error. Its
// Started channel, when set, gets a value at each call that finds room in it.
type Blocking struct {
	Ctx     context.Context
	Started chan<- struct{}
}

// ReadAt waits for the context.
func (b Blocking) ReadAt([]byte, int64) (int, error) {
	select {
	case b.Started <- struct{}{}:
	default:
	}
	<-b.Ctx.Done()
	return 0, b.Ctx.Err()
}
