package pco

import "encoding/binary"

// join turns the latents of a page into numbers, written to dst.
func (c *Chunk) join(pv [3]*pageVar, n int, dst []byte) error {
	var lookbacks []uint64
	if pv[varDelta] != nil {
		lookbacks = pv[varDelta].body
	}
	prim := c.undelta(varPrimary, pv[varPrimary], n, lookbacks)
	var sec []uint64
	if pv[varSecondary] != nil {
		sec = c.undelta(varSecondary, pv[varSecondary], n, lookbacks)
	}
	t := c.typ
	out := make([]uint64, n)
	switch c.mode {
	case modeClassic:
		for i, l := range prim {
			out[i] = t.fromOrdered(l)
		}
	case modeDict:
		for i, l := range prim {
			if l >= uint64(len(c.dict)) {
				return corrupt("dictionary index %d, the dictionary has %d values", l, len(c.dict))
			}
			out[i] = t.fromOrdered(c.dict[l])
		}
	case modeIntMult:
		for i, l := range prim {
			out[i] = t.fromOrdered((l*c.base + sec[i]) & t.mask())
		}
	case modeFloatMult:
		base := t.fromOrdered(c.base)
		for i, l := range prim {
			adj := t.floatToOrdered(floatMul(t, intFloatFromLatent(t, l), base)) + sec[i] + t.mid()
			out[i] = t.fromOrdered(adj & t.mask())
		}
	case modeFloatQuant:
		cutoff := t.mid() >> c.k
		high := ^(uint64(1)<<c.k - 1) & t.mask()
		for i, y := range prim {
			flip := t.mid()
			if y < cutoff {
				flip = high
			}
			out[i] = ((y << c.k) ^ sec[i] ^ flip) & t.mask()
		}
	}
	write(dst, out, t.Bits()/8)
	return nil
}

// write stores the low width bytes of each value, little-endian.
func write(dst []byte, vals []uint64, width int) {
	for i, v := range vals {
		switch width {
		case 1:
			dst[i] = byte(v)
		case 2:
			binary.LittleEndian.PutUint16(dst[2*i:], uint16(v))
		case 4:
			binary.LittleEndian.PutUint32(dst[4*i:], uint32(v))
		default:
			binary.LittleEndian.PutUint64(dst[8*i:], v)
		}
	}
}
