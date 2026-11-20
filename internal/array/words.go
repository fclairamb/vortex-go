package array

import (
	"encoding/binary"
	"fmt"
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

// mapWords reads src as words of type T, applies op to runs of at most 1024 of them, and
// writes the result to dst, which is as long as src.
func mapWords[T word](dst, src []byte, op func([]T)) {
	bits := bitsOf[T]()
	w := bits / 8
	var buf [1024]T
	for len(src) > 0 {
		n := min(1024, len(src)/w)
		loadWords(buf[:n], src, bits)
		op(buf[:n])
		storeWords(dst, buf[:n], bits)
		src, dst = src[n*w:], dst[n*w:]
	}
}

// mapInts applies op, per integer width, to the values of src, into a new slice.
func mapInts(
	src []byte, width int, op8 func([]uint8), op16 func([]uint16), op32 func([]uint32), op64 func([]uint64),
) []byte {
	dst := make([]byte, len(src))
	switch width {
	case 1:
		mapWords(dst, src, op8)
	case 2:
		mapWords(dst, src, op16)
	case 4:
		mapWords(dst, src, op32)
	default:
		mapWords(dst, src, op64)
	}
	return dst
}

// addConst is src, little-endian integers of width bytes, plus c, wrapping.
func addConst(src []byte, width int, c uint64) []byte {
	return mapInts(src, width,
		func(v []uint8) {
			for i := range v {
				v[i] += uint8(c)
			}
		},
		func(v []uint16) {
			for i := range v {
				v[i] += uint16(c)
			}
		},
		func(v []uint32) {
			for i := range v {
				v[i] += uint32(c)
			}
		},
		func(v []uint64) {
			for i := range v {
				v[i] += c
			}
		})
}

// unzigzag is src, zigzag-encoded integers of width bytes, decoded: (n >> 1) ^ -(n & 1).
func unzigzag(src []byte, width int) []byte {
	return mapInts(src, width,
		func(v []uint8) {
			for i, n := range v {
				v[i] = n>>1 ^ -(n & 1)
			}
		},
		func(v []uint16) {
			for i, n := range v {
				v[i] = n>>1 ^ -(n & 1)
			}
		},
		func(v []uint32) {
			for i, n := range v {
				v[i] = n>>1 ^ -(n & 1)
			}
		},
		func(v []uint64) {
			for i, n := range v {
				v[i] = n>>1 ^ -(n & 1)
			}
		})
}
