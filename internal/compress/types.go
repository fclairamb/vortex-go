package compress

import (
	"encoding/binary"
	"math/bits"

	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// maxInline is the longest string a view of a varbinview holds itself.
const maxInline = 12

// Strings is the compressed array of the strings v (utf8, else binary), or nil for the plain
// array.
func (c *Compressor) Strings(v [][]byte, nulls []bool, utf8 bool) *encode.Node {
	return c.strings(c.top(), v, nulls, utf8)
}

// PicksDictStrings is PicksDict for strings.
func (c *Compressor) PicksDictStrings(v [][]byte, nulls []bool, utf8 bool) bool {
	n := c.strings(c.probe(), v, nulls, utf8)
	return n != nil && n.ID == encode.IDDict
}

// headSet is a set of the heads of strings, in the memory of a Compressor.
type headSet struct {
	slots []uint64 // a head plus one, 0 for an empty slot
}

// head is what the reference counts as the distinct values of strings (StringStats): the
// length and the first 4 bytes of a string, which equal strings share. A null row counts as
// an empty string, as the zeroed view of a null does.
func head(s []byte) uint64 {
	var prefix uint32
	for i, b := range s[:min(4, len(s))] {
		prefix |= uint32(b) << (8 * i)
	}
	return uint64(uint32(len(s))) | uint64(prefix)<<32
}

// over says whether the strings v have more than limit distinct heads: the reference does not
// try a dictionary on them. It stops at the first head over the limit.
func (h *headSet) over(v [][]byte, nulls []bool, limit int) bool {
	// the heads it holds at most, at a load of a half at most: there is always an empty slot
	size := 1 << bits.Len(uint(2*min(len(v), limit+1)-1))
	if len(h.slots) < size {
		h.slots = make([]uint64, size)
	}
	slots := h.slots[:size]
	clear(slots)
	shift := 64 - bits.TrailingZeros(uint(size))
	n := 0
	for i, s := range v {
		key := uint64(1)
		if !isNull(nulls, i) {
			key += head(s)
		}
		for at := key * 0x9e3779b97f4a7c15 >> shift; ; at = (at + 1) & uint64(size-1) {
			if slots[at] == key {
				break
			}
			if slots[at] == 0 {
				slots[at] = key
				if n++; n > limit {
					return true
				}
				break
			}
		}
	}
	return false
}

// binsCost is the cost of a plain varbinview.
func binsCost(v [][]byte, nulls []bool) int {
	total := nodeOverhead + 2*bufferOverhead + 16*len(v) + validityCost(nulls, len(v))
	for i, x := range v {
		if len(x) > maxInline && !isNull(nulls, i) {
			total += len(x)
		}
	}
	return total
}

// constantBytes is the constant array of the strings v, under their validity when the compressor
// may mask.
func (c *Compressor) constantBytes(x at, v [][]byte, nulls []bool, utf8 bool) (*encode.Node, bool) {
	if c.limit.NoMasked {
		return encode.ConstantBytes(x.e, v, nulls, utf8)
	}
	return encode.ConstantBytesMasked(x.e, v, nulls, utf8)
}

func (c *Compressor) strings(x at, v [][]byte, nulls []bool, utf8 bool) *encode.Node {
	if n, ok := c.constantBytes(x, v, nulls, utf8); ok && cost(n) < binsCost(v, nulls) {
		return n
	}
	valid := 0
	for i := range v {
		if !isNull(nulls, i) {
			valid++
		}
	}
	// FSST is for strings: the reference has no scheme of it for binary
	var l cands
	l.add(sDict, x.cascades() && x.parent != sDict && valid > 0 && !c.heads.over(v, nulls, valid/2))
	l.add(sFSST, utf8 && valid > 0)
	// the second scheme of fragments of strings, kept when the sample says it is smaller
	l.add(sOnPair, utf8 && valid > 0 && !c.limit.NoOnPair)
	// the reference tries it for binary only, between the dictionary and zstd
	l.add(sVarBin, !utf8)
	l.add(sZstd, c.zstd())
	return pick(c, x, v, nulls, &l, binsOf, func(x at, s scheme, v [][]byte, nulls []bool) *encode.Node {
		return c.applyStrings(x, s, v, nulls, utf8)
	})
}

// applyStrings is the array of strings of scheme s.
func (c *Compressor) applyStrings(x at, s scheme, v [][]byte, nulls []bool, utf8 bool) *encode.Node {
	var n *encode.Node
	switch s {
	case sDict:
		n = encode.DictBytes(x.e, v, nulls)
		if beatsBare(x, n, binsCost(v, nulls)) {
			return n
		}
		if x.cascades() {
			c.kid(x.child(s, exclCodes), n, 0, uintOf(n.Kids[0].Bufs[0].Align))
			c.dictValues(x.child(s, exclSValues), n, utf8)
		}
	case sFSST:
		n = encode.FSSTBytes(x.e, v, nulls)
		if x.cascades() {
			c.kid(x.child(s, 0), n, 0, I32)
			c.kid(x.child(s, 0), n, 1, intOf(n.Kids[1].Bufs[0].Align))
		}
	case sOnPair:
		n = encode.OnPairBytes(x.e, v, nulls)
		if x.cascades() {
			kid := c.kid
			if x.trial {
				kid = c.packedKid // a trial does not try every scheme on the four children
			}
			kid(x.child(s, 0), n, 0, U32)
			kid(x.child(s, 0), n, 1, U16)
			kid(x.child(s, 0), n, 2, uintOf(n.Kids[2].Bufs[0].Align))
			kid(x.child(s, 0), n, 3, U32)
		}
	case sVarBin:
		n = encode.VarBinBytes(x.e, v, nulls)
		if x.cascades() {
			c.kid(x.child(s, 0), n, 0, uintOf(n.Kids[0].Bufs[0].Align))
		}
	default:
		n = encode.ZstdBytes(x.e, v, nulls)
	}
	return n
}

// packedKid is kid with the one scheme that always pays for integers of a known width,
// bit-packing, for the sample trial of OnPair, whose four children would cost more to try
// scheme by scheme than the whole of the trial of FSST. The chosen array is compressed with kid.
func (c *Compressor) packedKid(x at, n *encode.Node, i int, p PType) {
	var out *encode.Node
	switch raw := n.Kids[i].Bufs[0].Data; p {
	case U16:
		out, _ = encode.BitPackBest(x.e, unsafex.Slice[uint16](raw), nil)
	case U32:
		out, _ = encode.BitPackBest(x.e, unsafex.Slice[uint32](raw), nil)
	}
	// the offsets of more than 2^32 codes, in u64, stay as they are in a trial
	if out != nil {
		n.Kids[i] = out
	}
}

// dictValues compresses the values of a dictionary of strings, a varbinview, in place.
func (c *Compressor) dictValues(x at, n *encode.Node, utf8 bool) {
	vals := n.Kids[1]
	data, views := vals.Bufs[0].Data, vals.Bufs[1].Data
	rows := len(views) / 16
	strs := c.bins.get(rows)
	for i := range strs {
		view := views[16*i : 16*i+16]
		size := int(binary.LittleEndian.Uint32(view))
		if size <= maxInline {
			strs[i] = view[4 : 4+size]
		} else {
			at := int(binary.LittleEndian.Uint32(view[12:]))
			strs[i] = data[at : at+size]
		}
	}
	nulls := c.kidNulls(vals, rows)
	if out := c.strings(x, strs, nulls, utf8); out != nil {
		n.Kids[1] = out
	}
}

// Decimals is the compressed array of decimals of width bytes, or nil for the plain array:
// their byte parts, the integers compressed, when the values fit an i64.
func (c *Compressor) Decimals(raw []byte, width int, nulls []bool) *encode.Node {
	n, ok := encode.DecimalBytes(c.e, raw, width, nulls)
	if !ok {
		return nil
	}
	c.kid(c.top().child(sDecimal, 0), n, 0, intOf(n.Kids[0].Bufs[0].Align))
	if cost(n) >= nodeOverhead+bufferOverhead+len(raw)+validityCost(nulls, len(raw)/width) {
		return nil
	}
	return n
}

// Timestamps is the compressed array of timestamps in ticks of a second (one of the
// encode.Ticks constants), or nil for the plain array: their datetime parts, the parts
// compressed as integers, or the integers themselves when that is smaller.
func (c *Compressor) Timestamps(v []int64, nulls []bool, ticks int64) *encode.Node {
	parts, _ := encode.DateTimeParts(c.e, v, nulls, ticks)
	k := c.top().child(sDateTime, 0)
	c.kid(k, parts, 0, intOf(parts.Kids[0].Bufs[0].Align))
	c.kid(k, parts, 1, I32)
	c.kid(k, parts, 2, I32)
	best := numberCost(v, nulls)
	n := c.Numbers(I64, unsafex.Bytes(v), nulls)
	if n != nil {
		best = cost(n)
	}
	if cost(parts) < best {
		return parts
	}
	return n
}

// Bools is the compressed array of booleans, or nil for the plain array: a constant, or nothing.
func (c *Compressor) Bools(v, nulls []bool) *encode.Node {
	constant := encode.ConstantBoolsMasked
	if c.limit.NoMasked {
		constant = encode.ConstantBools
	}
	n, _ := constant(c.e, v, nulls)
	if n != nil && cost(n) >= nodeOverhead+bufferOverhead+(len(v)+7)/8+validityCost(nulls, len(v)) {
		return nil
	}
	return n
}
