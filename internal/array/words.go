package array

import (
	"encoding/binary"
	"fmt"

	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// word is an unsigned integer as the integer decoders work on it.
type word interface {
	~uint8 | ~uint16 | ~uint32 | ~uint64
}

// bitsOf is the number of bits of T.
func bitsOf[T word]() int {
	n := 0
	for z := ^T(0); z != 0; z >>= 1 {
		n++
	}
	return n
}

// unsignedOf is the unsigned type of the same width as the signed integer type p.
func unsignedOf(p PType) PType { return p - I8 + U8 }

// intPrim checks that t is an integer type, and returns its width in bytes.
func intPrim(t DType) (int, error) {
	if t.Kind != Prim || t.PType.Float() {
		return 0, fmt.Errorf("cannot be a %s", t)
	}
	return t.PType.Width(), nil
}

// loadWords reads len(dst) little-endian words of bits bits from src.
func loadWords[T word](dst []T, src []byte, bits int) {
	switch bits {
	case 8:
		for i := range dst {
			dst[i] = T(src[i])
		}
	case 16:
		for i := range dst {
			dst[i] = T(binary.LittleEndian.Uint16(src[2*i:]))
		}
	case 32:
		for i := range dst {
			dst[i] = T(binary.LittleEndian.Uint32(src[4*i:]))
		}
	default:
		for i := range dst {
			dst[i] = T(binary.LittleEndian.Uint64(src[8*i:]))
		}
	}
}

// storeWords writes src as little-endian words of bits bits at the start of dst.
func storeWords[T word](dst []byte, src []T, bits int) {
	switch bits {
	case 8:
		for i, v := range src {
			dst[i] = byte(v)
		}
	case 16:
		for i, v := range src {
			binary.LittleEndian.PutUint16(dst[2*i:], uint16(v))
		}
	case 32:
		for i, v := range src {
			binary.LittleEndian.PutUint32(dst[4*i:], uint32(v))
		}
	default:
		for i, v := range src {
			binary.LittleEndian.PutUint64(dst[8*i:], uint64(v))
		}
	}
}

// words views b as words of T: the memory of b when it is aligned, a copy otherwise.
func words[T integer](b []byte) []T { return unsafex.Slice[T](b) }

// target is where a decoder writes the values it makes from the ones of a, of the same width:
// over them when nothing else reads them and they can be read as numbers, in the arena otherwise.
func (n *Node) target(a *Array) []byte {
	if a.fresh && unsafex.Aligned(a.Data, 8) {
		return a.Data
	}
	return n.ctx.opt.Arena.Bytes(len(a.Data), false)
}

// addWords is src plus c, wrapping, into dst.
func addWords[T word](dst, src []T, c T) {
	for i, v := range src {
		dst[i] = v + c
	}
}

// addConst is src, little-endian integers of width bytes, plus c, wrapping: in dst, which may
// be src.
func addConst(dst, src []byte, width int, c uint64) []byte {
	switch width {
	case 1:
		addWords(dst, src, uint8(c))
	case 2:
		addWords(words[uint16](dst), words[uint16](src), uint16(c))
	case 4:
		addWords(words[uint32](dst), words[uint32](src), uint32(c))
	default:
		addWords(words[uint64](dst), words[uint64](src), c)
	}
	return dst
}

// unzigzagWords is src, zigzag-encoded, decoded into dst: (n >> 1) ^ -(n & 1).
func unzigzagWords[T word](dst, src []T) {
	for i, n := range src {
		dst[i] = n>>1 ^ -(n & 1)
	}
}

// unzigzag is src, zigzag-encoded integers of width bytes, decoded: in dst, which may be src.
func unzigzag(dst, src []byte, width int) []byte {
	switch width {
	case 1:
		unzigzagWords(dst, src)
	case 2:
		unzigzagWords(words[uint16](dst), words[uint16](src))
	case 4:
		unzigzagWords(words[uint32](dst), words[uint32](src))
	default:
		unzigzagWords(words[uint64](dst), words[uint64](src))
	}
	return dst
}
