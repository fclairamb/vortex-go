package encode

import (
	"github.com/fclairamb/vortex-go/internal/pco"
	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// pcoValuesPerPage is the numbers of a page of vortex.pco: the reference calls
// Pco::from_primitive with 8192, and its pages of a chunk are all about that size.
const pcoValuesPerPage = 8192

// pcoState is the memory of a Pco encoding.
type pcoState struct {
	vals  []uint64
	chunk []byte
	coder pco.Coder
}

// pcoType is the number type of Pco that T is stored as. 8-bit numbers have none: the crate
// codes numbers of 16, 32 and 64 bits.
func pcoType[T Number]() pco.Type {
	idx := 0
	switch unsafex.Size[T]() {
	case 4:
		idx = 1
	case 8:
		idx = 2
	}
	switch {
	case isFloat[T]():
		return [...]pco.Type{pco.F16, pco.F32, pco.F64}[idx]
	case signed[T]():
		return [...]pco.Type{pco.I16, pco.I32, pco.I64}[idx]
	}
	return [...]pco.Type{pco.U16, pco.U32, pco.U64}[idx]
}

// Pco is the Pco encoding of the numbers of v: the valid ones, in chunks of 2^18 and pages of
// about 8192, as the reference writes them. It returns nil for 8-bit numbers, which Pco does not
// code.
func Pco[T Number](e *Encoder, v []T, nulls []bool) *Node {
	if unsafex.Size[T]() == 1 {
		return nil
	}
	n, _ := pcoNode(e, v, nulls)
	return n
}

// pcoValues fills st.vals with the valid values of v. A value is read as one word of its
// width, little-endian, not a byte at a time: the width is that of T, so the words are
// aligned as v is.
func pcoValues[T Number](st *pcoState, v []T, nulls []bool) {
	src := unsafex.Bytes(v)
	st.vals = st.vals[:0]
	switch unsafex.Size[T]() {
	case 2:
		words := unsafex.Slice[uint16](src)
		for i := range v {
			if !isNull(nulls, i) {
				st.vals = append(st.vals, uint64(words[i]))
			}
		}
	case 4:
		words := unsafex.Slice[uint32](src)
		for i := range v {
			if !isNull(nulls, i) {
				st.vals = append(st.vals, uint64(words[i]))
			}
		}
	default:
		words := unsafex.Slice[uint64](src)
		for i := range v {
			if !isNull(nulls, i) {
				st.vals = append(st.vals, words[i])
			}
		}
	}
}

// pcoNode returns the node, and the bytes of its chunk metadata buffers.
func pcoNode[T Number](e *Encoder, v []T, nulls []bool) (*Node, int) {
	st := pcoStates.Get().(*pcoState)
	defer pcoStates.Put(st)
	pcoValues(st, v, nulls)
	coded := st.coder.Compress(pcoType[T](), st.vals, pcoValuesPerPage)
	node := e.node(IDPco)
	meta := protowire.AppendBytes(e.tmp[:0], 1, pco.WriteHeader(e.tmp2[:0]))
	metaBytes := 0
	for _, ch := range coded {
		st.chunk = st.chunk[:0]
		for _, n := range ch.N {
			e.tmp2 = protowire.AppendUint(e.tmp2[:0], 1, uint64(n))
			st.chunk = protowire.AppendBytes(st.chunk, 1, e.tmp2)
		}
		meta = protowire.AppendBytes(meta, 2, st.chunk)
	}
	for _, ch := range coded {
		node.add(e.keep(ch.Meta), 1)
		metaBytes += len(ch.Meta)
	}
	for _, ch := range coded {
		for _, p := range ch.Pages {
			node.add(e.keep(p), 1)
		}
	}
	e.tmp = meta
	node.Meta = e.keep(meta) // before the validity, which uses the same scratch
	node.kid(e.validity(nulls, len(v), len(v)))
	return node, metaBytes
}

// EstimatePco is the size of Pco: the sample is coded, and its pages scaled to the rows.
func EstimatePco[T Number](e *Encoder, sample []T, nulls []bool, rows int) int {
	n, metaBytes := pcoNode(e, sample, nulls)
	pages := 0
	for _, b := range n.Bufs {
		pages += len(b.Data)
	}
	pages -= metaBytes
	return metaBytes + scale(pages, len(sample), rows) + validBytes(nulls, rows)
}
