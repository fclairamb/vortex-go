package array

import (
	"math"
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
	small := a.Bytes(8, false) // under the smallest shared size: kept by the arena itself
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

// TestArenaSmall checks that an arena keeps its small buffers from one Release to the next:
// the same memory comes back, zeroed when asked, and a size that grows drops the ones too
// small for it.
func TestArenaSmall(t *testing.T) {
	p := new(Pool)
	a := p.NewArena()
	b, v := a.Bytes(80, false), a.Ints(80, false)
	for i := range b {
		b[i], v[i] = 0xff, -1
	}
	a.Release()
	if got := a.Bytes(60, false); &got[0] != &b[0] || got[0] != 0xff {
		t.Fatal("the small bytes are not reused")
	}
	if got := a.Ints(60, true); &got[0] != &v[0] || got[0] != 0 || got[59] != 0 {
		t.Fatal("the small ints are not reused, or not zeroed")
	}
	a.Release()
	if got := a.Bytes(60, true); &got[0] != &b[0] || got[0] != 0 || got[59] != 0 {
		t.Fatal("the small bytes are not reused, or not zeroed")
	}
	if got := a.Ints(60, false); &got[0] != &v[0] {
		t.Fatal("the small ints are not reused")
	}
	a.Release()
	// Nothing is large enough for 100: the buffers of 80 are dropped, not kept for ever.
	if got := a.Bytes(100, false); &got[0] == &b[0] {
		t.Fatal("a buffer too small was reused")
	}
	if got := a.Ints(100, false); &got[0] == &v[0] {
		t.Fatal("a buffer too small was reused")
	}
	a.Release()
	if len(a.smallBytes) != 1 || len(a.smallInts) != 1 {
		t.Fatalf("%d small bytes and %d small ints kept, want 1 and 1", len(a.smallBytes), len(a.smallInts))
	}
	// A retired arena keeps its small buffers too: Recycle releases it the same way.
	p.Retire(a)
	p.Recycle()
	if got := a.Bytes(100, false); len(got) != 100 {
		t.Fatalf("%d bytes after Recycle", len(got))
	}
}

// TestArenaClass checks that a class holds what it is asked for, at most a quarter more, that
// the class of its size is itself, and that the classes grow with the size.
func TestArenaClass(t *testing.T) {
	prev := -1
	for n := 9; n < 1<<20; n += 1 + n/1000 {
		c, size := class(n)
		if size < n || 4*size > 5*n+4 {
			t.Fatalf("%d elements: class of %d", n, size)
		}
		if again, s := class(size); again != c || s != size {
			t.Fatalf("%d elements: class %d of %d, then %d of %d", n, c, size, again, s)
		}
		if c < prev || c >= classes-1 {
			t.Fatalf("%d elements: class %d after %d", n, c, prev)
		}
		prev = c
	}
	if c, _ := class(math.MaxInt); c+1 >= classes {
		t.Fatalf("the largest class is %d, the pool has %d", c, classes)
	}
}

// TestTakeFixed takes values of every width, a word of the machine or not, with a null row.
func TestTakeFixed(t *testing.T) {
	for _, w := range []int{1, 2, 4, 8, 16} {
		data := make([]byte, 3*w)
		for i := range data {
			data[i] = byte(i + 1)
		}
		got := takeFixed(nil, data, w, []int{2, -1, 0})
		want := append(append(append([]byte(nil), data[2*w:]...), make([]byte, w)...), data[:w]...)
		if string(got) != string(want) {
			t.Fatalf("%d bytes: %v, want %v", w, got, want)
		}
	}
}
