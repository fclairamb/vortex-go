package array

import (
	"fmt"

	"github.com/fclairamb/vortex-go/internal/pco"
	"github.com/fclairamb/vortex-go/internal/protowire"
)

// maxPcoChunk is the most values a chunk may hold: the page size of the writer.
const maxPcoChunk = 1 << 18

// pcoTypes maps the primitive types to the number types of Pco.
var pcoTypes = map[PType]pco.Type{
	U8: pco.U8, U16: pco.U16, U32: pco.U32, U64: pco.U64,
	I8: pco.I8, I16: pco.I16, I32: pco.I32, I64: pco.I64,
	F16: pco.F16, F32: pco.F32, F64: pco.F64,
}

// pcoMeta is the metadata of vortex.pco: {header, chunks: [{pages: [{n_values}]}]}.
type pcoMeta struct {
	header []byte
	chunks [][]int // per chunk, the values of its pages
}

func parsePcoMeta(b []byte) (m pcoMeta, err error) {
	it := protowire.NewIter(b)
	for it.Next() {
		switch {
		case it.Num() == 1 && it.Type() == protowire.Bytes:
			m.header = it.Bytes()
		case it.Num() == 2 && it.Type() == protowire.Bytes:
			pages, err := parsePcoChunk(it.Bytes())
			if err != nil {
				return m, err
			}
			m.chunks = append(m.chunks, pages)
		}
	}
	return m, it.Err()
}

func parsePcoChunk(b []byte) (pages []int, err error) {
	it := protowire.NewIter(b)
	for it.Next() {
		if it.Num() == 1 && it.Type() == protowire.Bytes {
			v, err := varints(it.Bytes(), 1)
			if err != nil {
				return nil, err
			}
			n, err := count("n_values", v[0])
			if err != nil {
				return nil, err
			}
			pages = append(pages, n)
		}
	}
	return pages, it.Err()
}

// decodePco decodes vortex.pco. Child 0 is the validity. The buffers are the metadata of each
// chunk, then the pages of every chunk. Only the valid values are stored.
func decodePco(n *Node, t DType, rows int) (*Array, error) {
	pt, ok := pcoTypes[t.PType]
	if t.Kind != Prim || !ok {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	m, err := parsePcoMeta(n.Metadata())
	if err != nil {
		return nil, err
	}
	valid, err := n.optionalValidity(rows)
	if err != nil {
		return nil, err
	}
	nValid := rows
	if valid != nil {
		nValid = countBits(valid, rows)
	}
	pages, err := m.check(nValid)
	if err != nil {
		return nil, err
	}
	if want := len(m.chunks) + pages; n.NumBuffers() != want {
		return nil, fmt.Errorf("%d buffers, the metadata lists %d chunks and %d pages", n.NumBuffers(), len(m.chunks), pages)
	}
	hdr, _, err := pco.ReadHeader(m.header)
	if err != nil {
		return nil, err
	}
	w := t.PType.Width()
	values := make([]byte, nValid*w)
	off, page := 0, len(m.chunks)
	for i, chunkPages := range m.chunks {
		meta, err := n.Buffer(i)
		if err != nil {
			return nil, err
		}
		chunk, err := hdr.Chunk(pt, meta)
		if err != nil {
			return nil, fmt.Errorf("chunk %d: %w", i, err)
		}
		for _, nv := range chunkPages {
			buf, err := n.Buffer(page)
			if err != nil {
				return nil, err
			}
			if err := chunk.Page(buf, nv, values[off:off+nv*w]); err != nil {
				return nil, fmt.Errorf("chunk %d: %w", i, err)
			}
			off += nv * w
			page++
		}
	}
	return &Array{Type: t, Len: rows, Validity: valid, Data: spreadValues(values, w, valid, rows)}, nil
}

// check validates the page counts against the number of valid rows and returns the number of
// pages.
func (m *pcoMeta) check(nValid int) (int, error) {
	pages, total := 0, 0
	for i, chunk := range m.chunks {
		inChunk := 0
		for _, nv := range chunk {
			if nv == 0 {
				return 0, fmt.Errorf("chunk %d has an empty page", i)
			}
			if nv > maxPcoChunk-inChunk {
				return 0, fmt.Errorf("chunk %d has more than %d values", i, maxPcoChunk)
			}
			inChunk += nv
		}
		if inChunk > nValid-total {
			return 0, fmt.Errorf("the chunks hold more than the %d valid values", nValid)
		}
		total += inChunk
		pages += len(chunk)
	}
	if total != nValid {
		return 0, fmt.Errorf("the chunks hold %d values, there are %d valid rows", total, nValid)
	}
	return pages, nil
}
