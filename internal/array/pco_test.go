package array

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// pcoVector is a file of testdata/vectors/pco, which tools/oracle/pcovectors.py writes: a
// chunk, as the reference compresses it, and the values it holds.
type pcoVector struct {
	Name, Type, Header, Meta, Values string
	Pages                            []struct {
		N    int
		Data string
	}
}

func (v pcoVector) dtype() DType {
	for i, n := range ptypeNames {
		if n == v.Type {
			return DType{Kind: Prim, PType: PType(i)}
		}
	}
	panic("type " + v.Type)
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func loadPco(t *testing.T) map[string]pcoVector {
	t.Helper()
	files, err := filepath.Glob("../../testdata/vectors/pco/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no vectors: %v", err)
	}
	out := map[string]pcoVector{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var v pcoVector
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		out[v.Name] = v
	}
	return out
}

// pcoMetadata is the metadata of vortex.pco: the header, then per chunk the values of its pages.
func pcoMetadata(header []byte, chunks ...[]int) []byte {
	b := append([]byte{0x0a, byte(len(header))}, header...)
	for _, pages := range chunks {
		var c []byte
		for _, n := range pages {
			p := binary.AppendUvarint([]byte{0x08}, uint64(n))
			c = append(append(c, 0x0a, byte(len(p))), p...)
		}
		b = append(append(b, 0x12, byte(len(c))), c...)
	}
	return b
}

// pcoSeg is a vortex.pco array over the buffers, with a validity child when given.
func pcoSeg(meta []byte, validity []byte, bufs ...[]byte) []byte {
	idx := make([]uint16, len(bufs))
	var all []buf
	for i, b := range bufs {
		idx[i] = uint16(i)
		all = append(all, buf{data: b})
	}
	if validity == nil {
		return segment(node(ePco, meta, idx), all...)
	}
	all = append(all, buf{data: validity})
	return segment(node(ePco, meta, idx, bnode(uint16(len(bufs)))), all...)
}

// buffers are the chunk metadata of the vector, then its pages.
func (v pcoVector) buffers(t *testing.T) (pages []int, bufs [][]byte) {
	bufs = [][]byte{unhex(t, v.Meta)}
	for _, p := range v.Pages {
		pages = append(pages, p.N)
		bufs = append(bufs, unhex(t, p.Data))
	}
	return pages, bufs
}

func TestPcoVectors(t *testing.T) {
	for name, v := range loadPco(t) {
		t.Run(name, func(t *testing.T) {
			pages, bufs := v.buffers(t)
			want := unhex(t, v.Values)
			rows := len(want) / v.dtype().PType.Width()
			seg := pcoSeg(pcoMetadata(unhex(t, v.Header), pages), nil, bufs...)
			a := mustDecode(t, seg, v.dtype(), rows)
			if string(a.Data) != string(want) || a.Validity != nil {
				t.Fatalf("decoded %d bytes that differ from the %d expected", len(a.Data), len(want))
			}
		})
	}
}

func TestPcoNullsAndChunks(t *testing.T) {
	vecs := loadPco(t)
	a, b := vecs["classic_u32"], vecs["pages_exact"]
	pagesA, bufsA := a.buffers(t)
	pagesB, bufsB := b.buffers(t)
	valuesA, valuesB := unhex(t, a.Values), unhex(t, b.Values)
	nA, nB := len(valuesA)/4, len(valuesB)/4
	// Two chunks in a row: the metadata of both, then the pages of both.
	bufs := append([][]byte{bufsA[0], bufsB[0]}, append(bufsA[1:], bufsB[1:]...)...)
	meta := pcoMetadata(unhex(t, a.Header), pagesA, pagesB)
	rows := nA + nB
	got := mustDecode(t, pcoSeg(meta, nil, bufs...), u32t, rows)
	if string(got.Data) != string(valuesA)+string(valuesB) {
		t.Fatal("two chunks decode wrong")
	}
	// The same, with a null between each value and at the end.
	nulls := make([]byte, (2*rows+8)/8)
	for i := range rows {
		nulls[(2*i)>>3] |= 1 << (2 * i & 7)
	}
	got = mustDecode(t, pcoSeg(meta, nulls, bufs...), DType{Kind: Prim, PType: U32, Nullable: true}, 2*rows)
	all := string(valuesA) + string(valuesB)
	for i := range rows {
		if got.Null(2*i) || !got.Null(2*i+1) || string(got.Data[8*i:8*i+4]) != all[4*i:4*i+4] || binary.LittleEndian.Uint32(got.Data[8*i+4:]) != 0 {
			t.Fatalf("row %d: %v %v %v", i, got.Null(2*i), got.Null(2*i+1), got.Data[8*i:8*i+8])
		}
	}
	// An array of nulls has no chunk.
	got = mustDecode(t, pcoSeg(pcoMetadata(unhex(t, a.Header)), []byte{0}), DType{Kind: Prim, PType: U32, Nullable: true}, 3)
	if len(got.Data) != 12 || !got.Null(0) || !got.Null(2) {
		t.Fatalf("all null: %v", got.Data)
	}
}

var u32t = DType{Kind: Prim, PType: U32}

func TestPcoErrors(t *testing.T) {
	v := loadPco(t)["classic_u32"]
	pages, bufs := v.buffers(t)
	hdr := unhex(t, v.Header)
	rows := pages[0]
	good := pcoMetadata(hdr, pages)
	page := binary.AppendUvarint([]byte{0x08}, 1<<63)
	chunk := append([]byte{0x0a, byte(len(page))}, page...)
	hugePage := append([]byte{0x12, byte(len(chunk))}, chunk...)
	truncated := append([][]byte{bufs[0]}, bufs[1][:len(bufs[1])/2])
	for _, c := range []struct {
		name string
		seg  []byte
		dt   DType
		rows int
		want string
	}{
		{"a bool", pcoSeg(good, nil, bufs...), bl, rows, "cannot be a"},
		{"a string", pcoSeg(good, nil, bufs...), DType{Kind: Utf8}, rows, "cannot be a"},
		{"a type with no Pco", pcoSeg(good, nil, bufs...), DType{Kind: Prim, PType: PType(42)}, rows, "cannot be a"},
		{"metadata", pcoSeg([]byte{0x0a}, nil, bufs...), u32t, rows, "protowire"},
		{"chunk metadata", pcoSeg([]byte{0x12, 0x02, 0x0a, 0x80}, nil, bufs...), u32t, rows, "protowire"},
		{"page metadata", pcoSeg([]byte{0x12, 0x04, 0x0a, 0x02, 0x08, 0x80}, nil, bufs...), u32t, rows, "protowire"},
		{"huge page", pcoSeg(hugePage, nil, bufs...), u32t, rows, "too large"},
		{"two children", segment(node(ePco, good, []uint16{0, 1}, bnode(2), bnode(2)), buf{data: bufs[0]}, buf{data: bufs[1]}, buf{data: []byte{7}}), u32t, rows, "expected 0 or 1 child"},
		{"empty page", pcoSeg(pcoMetadata(hdr, []int{0}), nil, bufs...), u32t, 0, "empty page"},
		{"chunk too large", pcoSeg(pcoMetadata(hdr, []int{maxPcoChunk, 1}), nil, bufs...), u32t, maxPcoChunk + 1, "more than 262144 values"},
		{"more values than valid rows", pcoSeg(good, nil, bufs...), u32t, rows - 1, "more than the"},
		{"fewer values than valid rows", pcoSeg(good, nil, bufs...), u32t, rows + 1, "the chunks hold"},
		{"buffers", pcoSeg(good, nil, bufs[:1]...), u32t, rows, "1 buffers, the metadata lists 1 chunks and 1 pages"},
		{"header", pcoSeg(pcoMetadata([]byte{9, 0}, pages), nil, bufs...), u32t, rows, "newer"},
		{"chunk", pcoSeg(good, nil, []byte{0xff, 0xff}, bufs[1]), u32t, rows, "chunk 0:"},
		{"page", pcoSeg(good, nil, truncated...), u32t, rows, "chunk 0:"},
	} {
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.seg, c.dt, c.rows, c.want) })
	}
}

func TestPcoDamage(t *testing.T) {
	v := loadPco(t)["conv1_u16_2"]
	pages, bufs := v.buffers(t)
	seg := pcoSeg(pcoMetadata(unhex(t, v.Header), pages), nil, bufs...)
	for pos := range seg {
		for _, x := range []byte{0x00, 0x01, 0x7f, 0x80, 0xff} {
			damaged := append([]byte(nil), seg...)
			damaged[pos] = x
			_, _ = Decode(damaged, specs, DType{Kind: Prim, PType: U16}, pages[0], Options{})
		}
	}
}
