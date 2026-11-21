package array

import (
	"math/bits"
	"sync"

	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// minPooled is the smallest buffer, in elements, that a pool keeps: a smaller one costs less
// to allocate than to share.
const minPooled = 512

// Pool keeps the buffers of the arrays a scan is done with, for the arrays it decodes next: a
// scan then works in the same memory from one batch to the next. It is safe for concurrent use.
type Pool struct {
	bytes [bits.UintSize][][]byte // by the power of two of their capacity
	ints  [bits.UintSize][][]int
	// retired is the arenas the scan is done with once its current batch is.
	retired []*Arena
	mu      sync.Mutex
}

// Retire records that the arrays of the arena are read by the current batch at most: Recycle
// releases it.
func (p *Pool) Retire(a *Arena) {
	if a == nil {
		return
	}
	p.mu.Lock()
	p.retired = append(p.retired, a)
	p.mu.Unlock()
}

// Recycle releases the retired arenas: the consumer has moved on to the next batch.
func (p *Pool) Recycle() {
	p.mu.Lock()
	retired := p.retired
	p.retired = nil
	p.mu.Unlock()
	for _, a := range retired {
		a.Release()
	}
}

// NewArena returns an arena of the pool. A nil pool has the nil arena.
func (p *Pool) NewArena() *Arena {
	if p == nil {
		return nil
	}
	return &Arena{pool: p}
}

// Arena is the buffers of the arrays of one decode, taken from a pool and given back together
// by Release. The nil arena allocates, and gives nothing back. An arena is used by one
// goroutine at a time.
type Arena struct {
	pool  *Pool
	bytes [][]byte
	ints  [][]int
}

// class is the power of two that holds n elements, n being at least 1.
func class(n int) int { return bits.Len(uint(n - 1)) }

// alignedBytes allocates n zero bytes whose first one is aligned for any number.
func alignedBytes(n int) []byte {
	if n == 0 {
		return []byte{}
	}
	return unsafex.Bytes(make([]uint64, (n+7)/8))[:n:n]
}

// Bytes returns n bytes, aligned for any number: unsafex.Slice shares them. They are zero
// when zero is set, and anything otherwise: the caller then writes every one of them.
func (a *Arena) Bytes(n int, zero bool) []byte {
	if a == nil || n < minPooled {
		return alignedBytes(n)
	}
	c := class(n)
	p := a.pool
	p.mu.Lock()
	var b []byte
	if k := len(p.bytes[c]); k > 0 {
		b, p.bytes[c] = p.bytes[c][k-1], p.bytes[c][:k-1]
	}
	p.mu.Unlock()
	if b == nil {
		b = alignedBytes(1 << c)
	} else if zero {
		clear(b[:n])
	}
	a.bytes = append(a.bytes, b)
	return b[:n:n]
}

// Ints is Bytes for ints.
func (a *Arena) Ints(n int, zero bool) []int {
	if a == nil || n < minPooled {
		return make([]int, n)
	}
	c := class(n)
	p := a.pool
	p.mu.Lock()
	var b []int
	if k := len(p.ints[c]); k > 0 {
		b, p.ints[c] = p.ints[c][k-1], p.ints[c][:k-1]
	}
	p.mu.Unlock()
	if b == nil {
		b = make([]int, 1<<c)
	} else if zero {
		clear(b[:n])
	}
	a.ints = append(a.ints, b)
	return b[:n:n]
}

// Release gives the buffers of the arena back to its pool: the arrays made in it must not be
// read any more. The arena is empty, and can be used again.
func (a *Arena) Release() {
	if a == nil || len(a.bytes)+len(a.ints) == 0 {
		return
	}
	p := a.pool
	p.mu.Lock()
	for _, b := range a.bytes {
		c := class(cap(b))
		p.bytes[c] = append(p.bytes[c], b)
	}
	for _, b := range a.ints {
		c := class(cap(b))
		p.ints[c] = append(p.ints[c], b)
	}
	p.mu.Unlock()
	clear(a.bytes)
	clear(a.ints)
	a.bytes, a.ints = a.bytes[:0], a.ints[:0]
}
