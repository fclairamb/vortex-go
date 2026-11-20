package compress

import (
	"encoding/binary"

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
	n := c.strings(c.top(), v, nulls, utf8)
	return n != nil && n.ID == encode.IDDict
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
	l.add(sDict, x.cascades() && x.parent != sDict && valid > 0)
	l.add(sFSST, utf8 && valid > 0)
	// the second scheme of fragments of strings, kept when the sample says it is smaller
	l.add(sOnPair, utf8 && valid > 0 && !c.limit.NoOnPair)
	// the reference tries it for binary only, between the dictionary and zstd
	l.add(sVarBin, !utf8)
	l.add(sZstd, c.zstd())
	return pick(c, x, v, nulls, &l, binsCost, sampleBins, func(x at, s scheme, v [][]byte, nulls []bool) *encode.Node {
		return c.applyStrings(x, s, v, nulls, utf8)
	})
}

// applyStrings is the array of strings of scheme s.
func (c *Compressor) applyStrings(x at, s scheme, v [][]byte, nulls []bool, utf8 bool) *encode.Node {
	var n *encode.Node
	switch s {
	case sDict:
		n = encode.DictBytes(x.e, v, nulls)
		if x.cascades() {
			c.kid(x.child(s), n, 0, uintOf(n.Kids[0].Bufs[0].Align))
			c.dictValues(x.child(s), n, utf8)
		}
	case sFSST:
		n = encode.FSSTBytes(x.e, v, nulls)
		if x.cascades() {
			c.kid(x.child(s), n, 0, I32)
			c.kid(x.child(s), n, 1, intOf(n.Kids[1].Bufs[0].Align))
		}
	case sOnPair:
		n = encode.OnPairBytes(x.e, v, nulls)
		if x.cascades() {
			c.kid(x.child(s), n, 0, U32)
			c.kid(x.child(s), n, 1, U16)
			c.kid(x.child(s), n, 2, uintOf(n.Kids[2].Bufs[0].Align))
			c.kid(x.child(s), n, 3, U32)
		}
	case sVarBin:
		n = encode.VarBinBytes(x.e, v, nulls)
		if x.cascades() {
			c.kid(x.child(s), n, 0, uintOf(n.Kids[0].Bufs[0].Align))
		}
	default:
		n = encode.ZstdBytes(x.e, v, nulls)
	}
	return n
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
	c.kid(c.top().child(sDecimal), n, 0, intOf(n.Kids[0].Bufs[0].Align))
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
	k := c.top().child(sDateTime)
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
