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
	bytes [classes][][]byte // by the class of their capacity
	ints  [classes][][]int
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

// Forget drops the retired arenas without releasing them: the scan ended, and what its last
// batch reads stays as it is.
func (p *Pool) Forget() {
	p.mu.Lock()
	p.retired = nil
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

// classes is the number of classes of the buffers of a pool, and one more.
const classes = 4*bits.UintSize + 1

// class is the smallest class of buffers that holds n elements, n being more than 8, and the
// size of the buffers of that class: from 2^(k-1) to 2^k, the sizes are 2^(k-1) plus 1 to 4
// quarters of it. A buffer is then at most a quarter larger than what it holds, so that the
// memory of a scan of a file of few rows, which is never given back, is not up to twice what
// it decodes, as with powers of two.
func class(n int) (c, size int) {
	k := bits.Len(uint(n - 1)) // 2^(k-1) < n <= 2^k
	q := (n - 1) >> (k - 3)    // 4 to 7
	return 4*(k-1) + q - 4, (q + 1) << (k - 3)
}

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
	c, size := class(n)
	p := a.pool
	p.mu.Lock()
	b := takeFree(&p.bytes, c)
	p.mu.Unlock()
	if b == nil {
		b = alignedBytes(size)
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
	c, size := class(n)
	p := a.pool
	p.mu.Lock()
	b := takeFree(&p.ints, c)
	p.mu.Unlock()
	if b == nil {
		b = make([]int, size)
	} else if zero {
		clear(b[:n])
	}
	a.ints = append(a.ints, b)
	return b[:n:n]
}

// takeFree removes a buffer of class c, or else of the class above, from the buffers of a pool,
// and returns it: nil when there is none. A buffer of the class above is at most half as large
// again as what it is taken for.
func takeFree[T any](free *[classes][][]T, c int) []T {
	for _, c := range [2]int{c, c + 1} {
		if k := len(free[c]); k > 0 {
			b := free[c][k-1]
			free[c] = free[c][:k-1]
			return b
		}
	}
	return nil
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
		c, _ := class(cap(b))
		p.bytes[c] = append(p.bytes[c], b)
	}
	for _, b := range a.ints {
		c, _ := class(cap(b))
		p.ints[c] = append(p.ints[c], b)
	}
	p.mu.Unlock()
	clear(a.bytes)
	clear(a.ints)
	a.bytes, a.ints = a.bytes[:0], a.ints[:0]
}
