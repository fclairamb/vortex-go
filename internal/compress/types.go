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
	return c.strings(c.e, false, v, nulls, utf8, 0, sPlain)
}

// PicksDictStrings is PicksDict for strings.
func (c *Compressor) PicksDictStrings(v [][]byte, nulls []bool, utf8 bool) bool {
	n := c.strings(c.e, false, v, nulls, utf8, 0, sPlain)
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

func (c *Compressor) strings(e *encode.Encoder, trial bool, v [][]byte, nulls []bool, utf8 bool, depth int, parent scheme) *encode.Node {
	if n, ok := encode.ConstantBytes(e, v, nulls, utf8); ok && cost(n) < binsCost(v, nulls) {
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
	l.add(sDict, depth < maxDepth && parent != sDict && valid > 0)
	l.add(sFSST, utf8 && valid > 0)
	l.add(sZstd, c.mode == Compact)
	return pick(c, e, trial, v, nulls, &l, binsCost, sampleBins, func(e *encode.Encoder, trial bool, s scheme, v [][]byte, nulls []bool) *encode.Node {
		var n *encode.Node
		switch s {
		case sDict:
			n = encode.DictBytes(e, v, nulls)
			if depth < maxDepth {
				c.kid(e, trial, n, 0, uintOf(n.Kids[0].Bufs[0].Align), depth+1, s)
				c.dictValues(e, trial, n, utf8, depth+1)
			}
		case sFSST:
			n = encode.FSSTBytes(e, v, nulls)
			if depth < maxDepth {
				c.kid(e, trial, n, 0, I32, depth+1, s)
				c.kid(e, trial, n, 1, intOf(n.Kids[1].Bufs[0].Align), depth+1, s)
			}
		default:
			n = encode.ZstdBytes(e, v, nulls)
		}
		return n
	})
}

// dictValues compresses the values of a dictionary of strings, a varbinview, in place.
func (c *Compressor) dictValues(e *encode.Encoder, trial bool, n *encode.Node, utf8 bool, depth int) {
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
	if out := c.strings(e, trial, strs, nulls, utf8, depth, sDict); out != nil {
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
	c.kid(c.e, false, n, 0, intOf(n.Kids[0].Bufs[0].Align), 1, sDecimal)
	return n
}

// Timestamps is the compressed array of timestamps in ticks of a second (one of the
// encode.Ticks constants), or nil for the plain array: their datetime parts, the parts
// compressed as integers, or the integers themselves when that is smaller.
func (c *Compressor) Timestamps(v []int64, nulls []bool, ticks int64) *encode.Node {
	parts, _ := encode.DateTimeParts(c.e, v, nulls, ticks)
	c.kid(c.e, false, parts, 0, intOf(parts.Kids[0].Bufs[0].Align), 1, sDateTime)
	c.kid(c.e, false, parts, 1, I32, 1, sDateTime)
	c.kid(c.e, false, parts, 2, I32, 1, sDateTime)
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
	n, _ := encode.ConstantBools(c.e, v, nulls)
	return n
}
