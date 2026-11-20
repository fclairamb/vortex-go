package encode

import (
	"github.com/fclairamb/vortex-go/internal/pco"
	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// pcoValuesPerPage is the numbers of a page of vortex.pco: the reference calls
// Pco::from_primitive with 8192, and its pages of a chunk are all about that size.
const pcoValuesPerPage = 8192

// pcoState is the memory of the Pco encoder of an Encoder.
type pcoState struct {
	coder pco.Coder
	vals  []uint64
	chunk []byte
}

func (e *Encoder) pcoState() *pcoState {
	if e.pc == nil {
		e.pc = new(pcoState)
	}
	return e.pc
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

// pcoNode returns the node, and the bytes of its chunk metadata buffers.
func pcoNode[T Number](e *Encoder, v []T, nulls []bool) (*Node, int) {
	st := e.pcoState()
	w := unsafex.Size[T]()
	src := unsafex.Bytes(v)
	st.vals = st.vals[:0]
	for i := range v {
		if isNull(nulls, i) {
			continue
		}
		var b uint64
		for k := 0; k < w; k++ {
			b |= uint64(src[i*w+k]) << (8 * k)
		}
		st.vals = append(st.vals, b)
	}
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
