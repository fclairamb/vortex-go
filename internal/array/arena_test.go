package array

import (
	"testing"

	"github.com/fclairamb/vortex-go/internal/unsafex"
)

func TestArenaNil(t *testing.T) {
	var p *Pool
	a := p.NewArena()
	if a != nil {
		t.Fatal("a nil pool has an arena")
	}
	if b := a.Bytes(2000, false); len(b) != 2000 || !unsafex.Aligned(b, 8) {
		t.Fatalf("%d bytes", len(b))
	}
	if b := a.Bytes(0, false); len(b) != 0 {
		t.Fatalf("%d bytes", len(b))
	}
	if v := a.Ints(2000, false); len(v) != 2000 {
		t.Fatalf("%d ints", len(v))
	}
	a.Release()
	new(Pool).Retire(a) // nothing to give back
}

func TestArenaReuse(t *testing.T) {
	p := new(Pool)
	a := p.NewArena()
	small := a.Bytes(8, false) // under the smallest pooled size: not kept
	b, v := a.Bytes(3000, false), a.Ints(3000, false)
	if len(small) != 8 || len(b) != 3000 || cap(b) != 3000 || len(v) != 3000 || !unsafex.Aligned(b, 8) {
		t.Fatalf("%d, %d, %d", len(small), len(b), len(v))
	}
	for i := range b {
		b[i], v[i] = 0xff, -1
	}
	a.Release()
	a.Release() // empty: nothing happens
	// The same memory comes back, as it was left, or zeroed when asked.
	if got := a.Bytes(2500, false); &got[0] != &b[0] || got[0] != 0xff {
		t.Fatal("the bytes are not reused")
	}
	if got := a.Ints(2500, true); &got[0] != &v[0] || got[0] != 0 || got[2499] != 0 {
		t.Fatal("the ints are not reused, or not zeroed")
	}
	a.Release()
	if got := a.Bytes(2500, true); &got[0] != &b[0] || got[0] != 0 || got[2499] != 0 {
		t.Fatal("the bytes are not reused, or not zeroed")
	}
	if got := a.Ints(2500, false); &got[0] != &v[0] {
		t.Fatal("the ints are not reused")
	}
	// A retired arena is released by Recycle, not before.
	p.Retire(a)
	other := p.NewArena()
	if got := other.Bytes(2500, false); &got[0] == &b[0] {
		t.Fatal("a retired arena was released")
	}
	p.Recycle()
	if got := other.Bytes(2500, false); &got[0] != &b[0] {
		t.Fatal("Recycle did not release the arena")
	}
}
