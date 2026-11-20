package encode

import (
	"math/bits"
	"slices"

	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// The constants of the ALP-RD search, from the alp crate.
const (
	rdCutLimit  = 16   // the most bits cut off the front of a float
	rdMaxDict   = 8    // the most entries of the dictionary of left parts
	rdMaxSample = 4096 // the sample of the search, in rows
	rdBlock     = 64   // the runs the sample is made of
)

// rdGroup is a left part and the number of rows that have it.
type rdGroup struct {
	pattern uint16
	count   uint32
}

// rdDict is the cut of a float and the dictionary of the left parts the search picks.
type rdDict struct {
	patterns  [rdMaxDict]uint16
	len       int
	rightBits int
	leftBits  int
	encodable int
}

// rdBitWidth is the bits to write value, at least one.
func rdBitWidth(v uint64) int {
	if v == 0 {
		return 1
	}
	return bits.Len64(v)
}

// findRDDict searches the cut and the dictionary that make the smallest estimated size, from a
// sample of v: for each of the 16 cuts, the 8 most frequent left parts are the dictionary and
// the rows that miss it are exceptions. Of equal sizes the shallowest cut wins.
func findRDDict[F Float](e *Encoder, v []F) rdDict {
	w := unsafex.Size[F]()
	plan := newSamplePlan(len(v), rdMaxSample, rdBlock)
	e.u16s = grow(e.u16s, plan.count)
	pat := e.u16s[:plan.count]
	for k := range pat {
		pat[k] = uint16(floatBits(v[plan.at(k)]) >> (8*w - rdCutLimit))
	}
	slices.Sort(pat)
	groups := e.groups[:0]
	for _, p := range pat {
		if n := len(groups); n > 0 && groups[n-1].pattern == p {
			groups[n-1].count++
		} else {
			groups = append(groups, rdGroup{p, 1})
		}
	}
	best, bestSize := rdDict{}, 0.0
	for cut := rdCutLimit; cut >= 1; cut-- {
		d := selectRDDict(8*w-cut, groups)
		size := float64(d.rightBits) + float64(d.leftBits) + float64((plan.count-d.encodable)*32)/float64(plan.count)
		if cut == rdCutLimit || size <= bestSize {
			best, bestSize = d, size
		}
		groups = mergeSiblings(groups)
	}
	e.groups = groups
	return best
}

// mergeSiblings rewrites the groups, ascending by pattern, as groups of patterns one bit
// shorter: the pairs that share the shorter pattern are added.
func mergeSiblings(groups []rdGroup) []rdGroup {
	out := 0
	for read := 0; read < len(groups); {
		g := groups[read]
		g.pattern >>= 1
		read++
		if read < len(groups) && groups[read].pattern>>1 == g.pattern {
			g.count += groups[read].count
			read++
		}
		groups[out] = g
		out++
	}
	return groups[:out]
}

// selectRDDict picks the most frequent patterns of the groups as the dictionary: a pattern that
// ties with one held goes after it.
func selectRDDict(rightBits int, groups []rdGroup) rdDict {
	var counts [rdMaxDict]uint32
	d := rdDict{rightBits: rightBits}
	for _, g := range groups {
		at := d.len
		for i := range d.len {
			if g.count > counts[i] {
				at = i
				break
			}
		}
		if at == d.len && d.len == rdMaxDict {
			continue
		}
		d.len = min(d.len+1, rdMaxDict)
		copy(d.patterns[at+1:d.len], d.patterns[at:d.len-1])
		copy(counts[at+1:d.len], counts[at:d.len-1])
		d.patterns[at], counts[at] = g.pattern, g.count
	}
	d.leftBits = rdBitWidth(uint64(max(d.len-1, 0)))
	for _, c := range counts[:d.len] {
		d.encodable += int(c)
	}
	return d
}

// ALPRD is the ALP-RD encoding of the floats of v, for the ones that have no short decimal
// form: the cut of each float into its left part, a code of a dictionary of at most 8 entries
// (bit-packed in 1 to 3 bits), and its right part (bit-packed); a left part that is not in
// the dictionary has the code 0 and a patch with its bits. It returns false for no row.
func ALPRD[F Float](e *Encoder, v []F, nulls []bool) (*Node, bool) {
	if len(v) == 0 {
		return nil, false
	}
	return alpRDNode(e, v, nulls, findRDDict(e, v)), true
}

// ALPRDWith is ALPRD with the cut and the dictionary given: the right part is rightBits wide,
// at most the bits of F less 1. It returns false for more than 8 entries, or no row.
func ALPRDWith[F Float](e *Encoder, v []F, nulls []bool, rightBits int, dict []uint16) (*Node, bool) {
	if len(v) == 0 || len(dict) == 0 || len(dict) > rdMaxDict || rightBits < 1 || rightBits >= 8*unsafex.Size[F]() {
		return nil, false
	}
	d := rdDict{rightBits: rightBits, len: len(dict), leftBits: rdBitWidth(uint64(len(dict) - 1))}
	copy(d.patterns[:], dict)
	return alpRDNode(e, v, nulls, d), true
}

func alpRDNode[F Float](e *Encoder, v []F, nulls []bool, d rdDict) *Node {
	if unsafex.Size[F]() == 4 {
		return alpRDParts[F, uint32](e, v, nulls, d)
	}
	return alpRDParts[F, uint64](e, v, nulls, d)
}

// rdMiss marks the row of a left part that is not in the dictionary.
const rdMiss = 0xffff

func alpRDParts[F Float, U uint32 | uint64](e *Encoder, v []F, nulls []bool, d rdDict) *Node {
	n, w := len(v), unsafex.Size[F]()
	left := unsafex.Slice[uint16](e.alloc(2 * n))
	right := unsafex.Slice[U](e.alloc(w * n))
	mask := uint64(1)<<d.rightBits - 1
	misses := 0
	for i, f := range v {
		b := floatBits(f)
		right[i] = U(b & mask)
		left[i] = rdMiss
		pattern := uint16(b >> d.rightBits)
		for c, p := range d.patterns[:d.len] {
			if p == pattern {
				left[i] = uint16(c)
				break
			}
		}
		if left[i] == rdMiss {
			misses++
		}
	}
	var pos []uint64
	var vals []uint16
	if misses > 0 {
		pos, vals = unsafex.Slice[uint64](e.alloc(8*misses)), unsafex.Slice[uint16](e.alloc(2*misses))
		k := 0
		for i, f := range v {
			if left[i] == rdMiss {
				pos[k], vals[k] = uint64(i), uint16(floatBits(f)>>d.rightBits)
				left[i] = 0
				k++
			}
		}
	}
	// the widths are below the bits of the types, and the values fit: BitPack cannot fail
	packedLeft, _ := BitPack(e, left, nulls, d.leftBits)
	packedRight, _ := BitPack(e, right, nil, d.rightBits)
	node := e.node(IDALPRD)
	node.kid(packedLeft)
	node.kid(packedRight)
	if misses > 0 {
		packedPos, _ := BitPack(e, pos, nil, rdBitWidth(pos[misses-1]))
		node.kid(packedPos)
		node.kid(e.leaf(unsafex.Bytes(vals), 2, nil, misses))
	}
	node.Meta = e.keep(e.rdMeta(d, misses))
	return node
}

// EstimateALPRD is the size of ALPRD.
func EstimateALPRD[F Float](e *Encoder, sample []F, nulls []bool, rows int) int {
	if len(sample) == 0 {
		return 0
	}
	d := findRDDict(e, sample)
	misses, last := 0, 0
	for i, f := range sample {
		pattern := uint16(floatBits(f) >> d.rightBits)
		if !slices.Contains(d.patterns[:d.len], pattern) {
			misses++
			last = i
		}
	}
	blocks := (rows + blockSize - 1) / blockSize
	size := blocks*128*(d.leftBits+d.rightBits) + validBytes(nulls, rows)
	if misses = scale(misses, len(sample), rows); misses > 0 {
		last = scale(last+1, len(sample), rows) - 1
		size += (misses+blockSize-1)/blockSize*128*rdBitWidth(uint64(last)) + 2*misses
	}
	return size
}

// rdMeta is the metadata of an ALP-RD array: the cut, the dictionary, the type of the left
// parts and the patches of the ones that are not in it.
func (e *Encoder) rdMeta(d rdDict, misses int) []byte {
	e.tmp2 = e.tmp2[:0]
	for _, p := range d.patterns[:d.len] {
		e.tmp2 = protowire.AppendVarint(e.tmp2, uint64(p))
	}
	m := protowire.AppendUint(e.tmp[:0], 1, uint64(d.rightBits))
	m = protowire.AppendUint(m, 2, uint64(d.len))
	m = protowire.AppendBytes(m, 3, e.tmp2)
	m = protowire.AppendUint(m, 4, pU16)
	if misses > 0 {
		e.tmp2 = protowire.AppendUint(protowire.AppendUint(e.tmp2[:0], 1, uint64(misses)), 3, pU64)
		m = protowire.AppendBytes(m, 5, e.tmp2)
	}
	e.tmp = m
	return m
}
