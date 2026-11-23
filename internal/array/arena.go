package array

import (
	"math/bits"
	"slices"
	"sync"

	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// minPooled is the smallest buffer, in elements, that a pool shares between arenas: a
// smaller one costs less to allocate than to share. An arena keeps its own small buffers
// from one Release to the next, so a decode of few rows, a filter that keeps little, works
// in the same memory too.
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
	clear(p.retired)
	p.retired = p.retired[:0]
	p.mu.Unlock()
}

// Recycle releases the retired arenas: the consumer has moved on to the next batch.
func (p *Pool) Recycle() {
	p.mu.Lock()
	for _, a := range p.retired {
		a.release()
	}
	clear(p.retired)
	p.retired = p.retired[:0]
	p.mu.Unlock()
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
	pool       *Pool
	bytes      [][]byte
	ints       [][]int
	smallBytes [][]byte // the buffers under minPooled, kept for the next decode
	smallInts  [][]int
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
	if a == nil {
		return alignedBytes(n)
	}
	if n < minPooled {
		b := takeSmall(&a.smallBytes, n)
		if b == nil {
			b = alignedBytes(n)
		} else if zero {
			clear(b[:n])
		}
		a.bytes = append(a.bytes, b)
		return b[:n:n]
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
	if a == nil {
		return make([]int, n)
	}
	if n < minPooled {
		b := takeSmall(&a.smallInts, n)
		if b == nil {
			b = make([]int, n)
		} else if zero {
			clear(b[:n])
		}
		a.ints = append(a.ints, b)
		return b[:n:n]
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

// takeSmall removes a buffer of at least n elements from the small buffers of an arena, and
// returns it: nil when there is none. When none is large enough, they are all too small for
// what the arena decodes now: they are dropped, so that a decode whose rows grow does not
// keep every size it ever made.
func takeSmall[T any](free *[][]T, n int) []T {
	for i, b := range *free {
		if cap(b) >= n {
			*free = slices.Delete(*free, i, i+1)
			return b
		}
	}
	clear(*free)
	*free = (*free)[:0]
	return nil
}

// Release gives the buffers of the arena back: the arrays made in it must not be read any
// more. The arena is empty, and can be used again.
func (a *Arena) Release() {
	if a == nil || len(a.bytes)+len(a.ints) == 0 {
		return
	}
	a.pool.mu.Lock()
	a.release()
	a.pool.mu.Unlock()
}

// release gives the buffers back, the lock of the pool held: the small ones stay with the
// arena, for its next decode, the others go to the pool.
func (a *Arena) release() {
	p := a.pool
	for _, b := range a.bytes {
		if cap(b) < minPooled {
			a.smallBytes = append(a.smallBytes, b)
			continue
		}
		c, _ := class(cap(b))
		p.bytes[c] = append(p.bytes[c], b)
	}
	for _, b := range a.ints {
		if cap(b) < minPooled {
			a.smallInts = append(a.smallInts, b)
			continue
		}
		c, _ := class(cap(b))
		p.ints[c] = append(p.ints[c], b)
	}
	clear(a.bytes)
	clear(a.ints)
	a.bytes, a.ints = a.bytes[:0], a.ints[:0]
}
