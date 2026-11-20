package testutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestFaultReaders(t *testing.T) {
	data := []byte("0123456789")
	boom := errors.New("boom")
	f := &FailAt{R: bytes.NewReader(data), N: 2, Err: boom}
	p := make([]byte, 4)
	if _, err := f.ReadAt(p, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadAt(p, 0); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := f.ReadAt(p, 0); err != nil || f.Calls() != 3 {
		t.Fatal(err, f.Calls())
	}
	s := Short{R: bytes.NewReader(data), Max: 3}
	if n, err := s.ReadAt(p, 0); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := s.ReadAt(p, 9); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := s.ReadAt(p, 10); n != 0 || err != io.EOF {
		t.Fatal(n, err)
	}
	e := EOFAtEnd{R: bytes.NewReader(data), Size: 10}
	if n, err := e.ReadAt(p, 6); n != 4 || err != io.EOF {
		t.Fatal(n, err)
	}
	if n, err := e.ReadAt(p, 0); n != 4 || err != nil {
		t.Fatal(n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	done := make(chan error)
	b := Blocking{Ctx: ctx, Started: started}
	go func() { _, err := b.ReadAt(p, 0); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	started <- struct{}{}
	if _, err := b.ReadAt(p, 0); !errors.Is(err, context.Canceled) { // Started is full
		t.Fatal(err)
	}
}
