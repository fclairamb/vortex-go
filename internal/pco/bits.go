package pco

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrCorrupt means the input is not valid Pco: a count or a bit length is out of range, or the
// input is too short.
var ErrCorrupt = errors.New("pco: corrupt data")

func corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, args...))
}

// bitReader reads a little-endian bit stream. A read past the end sets an error and returns 0,
// so that a loop needs to look at err once per batch rather than once per read.
type bitReader struct {
	buf []byte
	pos int // in bits
	err error
}

func newBitReader(b []byte) *bitReader { return &bitReader{buf: b} }

// left is the number of bits not read yet.
func (r *bitReader) left() int { return len(r.buf)*8 - r.pos }

// read reads n bits, 0 <= n <= 64.
func (r *bitReader) read(n int) uint64 {
	if n == 0 {
		return 0
	}
	if n > r.left() {
		if r.err == nil {
			r.err = corrupt("the input ends %d bits short", n-r.left())
		}
		r.pos = len(r.buf) * 8
		return 0
	}
	i, shift := r.pos>>3, uint(r.pos&7)
	var v uint64
	if i+8 <= len(r.buf) {
		v = binary.LittleEndian.Uint64(r.buf[i:])
	} else {
		for k := 0; i+k < len(r.buf); k++ {
			v |= uint64(r.buf[i+k]) << (8 * k)
		}
	}
	v >>= shift
	if got := 64 - shift; uint(n) > got {
		v |= uint64(r.buf[i+8]) << got
	}
	r.pos += n
	if n < 64 {
		v &= 1<<n - 1
	}
	return v
}

// align skips to the next byte boundary. The bits it skips must be zero.
func (r *bitReader) align(what string) {
	if pad := (8 - r.pos&7) & 7; pad > 0 {
		if r.read(pad) != 0 && r.err == nil {
			r.err = corrupt("non-zero bits at the end of the %s", what)
		}
	}
}
