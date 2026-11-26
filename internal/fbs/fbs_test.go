package fbs

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

const fixtureDir = "../../testdata/fixtures/0.95.0"

// segmentOf returns the bytes of a segment of the file, which must be uncompressed.
func segmentOf(t *testing.T, file []byte, s PostscriptSegment, ok bool, err error) []byte {
	t.Helper()
	if !ok || err != nil {
		t.Fatalf("segment: %v, %v", ok, err)
	}
	off, _ := s.Offset()
	n, _ := s.Length()
	if c, _ := s.Compression(); c != 0 || s.Encrypted() {
		t.Fatalf("segment is compressed (%d) or encrypted", c)
	}
	if off+uint64(n) > uint64(len(file)) || n == 0 {
		t.Fatalf("segment %d+%d out of a file of %d", off, n, len(file))
	}
	return file[off : off+uint64(n)]
}

func TestFixtures(t *testing.T) {
	for _, name := range []string{"primitives", "struct_nested", "chunked", "varbin", "dict"} {
		t.Run(name, func(t *testing.T) {
			file, err := os.ReadFile(filepath.Join(fixtureDir, name+".vortex"))
			if err != nil {
				t.Fatal(err)
			}
			// The 8-byte trailer: version u16, postscript length u16, "VTXF".
			trailer := file[len(file)-8:]
			if string(trailer[4:]) != "VTXF" {
				t.Fatalf("magic %q", trailer[4:])
			}
			psLen := int(binary.LittleEndian.Uint16(trailer[2:]))
			ps, err := ReadPostscript(file[len(file)-8-psLen : len(file)-8])
			if err != nil {
				t.Fatal(err)
			}
			s, ok, err := ps.Footer()
			footer, err := ReadFooter(segmentOf(t, file, s, ok, err))
			if err != nil {
				t.Fatal(err)
			}
			arrays, err := footer.ArraySpecs()
			layouts, err2 := footer.LayoutSpecs()
			segs, err3 := footer.SegmentSpecs()
			if err != nil || err2 != nil || err3 != nil || arrays.Len() == 0 || layouts.Len() == 0 || segs.Len() == 0 {
				t.Fatal(arrays.Len(), layouts.Len(), segs.Len(), err, err2, err3)
			}
			for i := range arrays.Len() {
				a, err := arrays.At(i)
				if id, err2 := a.ID(); err != nil || err2 != nil || id == "" {
					t.Fatal(i, id, err, err2)
				}
			}
			for i := range layouts.Len() {
				l, err := layouts.At(i)
				if id, err2 := l.ID(); err != nil || err2 != nil || id == "" {
					t.Fatal(i, id, err, err2)
				}
			}
			for i := range segs.Len() {
				sp, err := segs.At(i)
				if err != nil || sp.Length == 0 || sp.Offset+uint64(sp.Length) > uint64(len(file)) {
					t.Fatal(i, sp, err)
				}
			}

			s, ok, err = ps.Layout()
			layout, err := ReadLayout(segmentOf(t, file, s, ok, err), 32)
			if err != nil {
				t.Fatal(err)
			}
			if rows, err := layout.RowCount(); rows == 0 || err != nil {
				t.Fatal(rows, err)
			}
			if enc, err := layout.Encoding(); int(enc) >= layouts.Len() || err != nil {
				t.Fatal(enc, err)
			}
			if _, err := layout.Metadata(); err != nil {
				t.Fatal(err)
			}
			if _, err := layout.Children(); err != nil {
				t.Fatal(err)
			}
			if _, err := layout.Segments(); err != nil {
				t.Fatal(err)
			}

			s, ok, err = ps.DType()
			dt, err := ReadDType(segmentOf(t, file, s, ok, err), 32)
			if err != nil {
				t.Fatal(err)
			}
			if k, err := dt.Kind(); k != KindStruct || err != nil {
				t.Fatal(k, err)
			}
			if _, err := ps.Metadata(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostscriptSegmentsAndMetadata(t *testing.T) {
	seg := fbtest.Tab{0: fbtest.U64(100), 1: fbtest.U32(20), 2: fbtest.U8(3),
		3: fbtest.T(fbtest.Tab{0: fbtest.U8(3)}), 4: fbtest.T(fbtest.Tab{})}
	buf := fbtest.Build(fbtest.Tab{
		0: fbtest.T(seg), 1: fbtest.T(seg), 2: fbtest.T(seg), 3: fbtest.T(seg),
		4: fbtest.Tables(fbtest.Tab{0: fbtest.Str("k"), 1: fbtest.T(seg)}),
	})
	ps, err := ReadPostscript(buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, get := range []func() (PostscriptSegment, bool, error){ps.DType, ps.Layout, ps.Statistics, ps.Footer} {
		s, ok, err := get()
		if !ok || err != nil {
			t.Fatal(ok, err)
		}
		off, _ := s.Offset()
		n, _ := s.Length()
		ae, _ := s.AlignmentExponent()
		c, _ := s.Compression()
		if off != 100 || n != 20 || ae != 3 || c != 3 || !s.Encrypted() {
			t.Fatal(off, n, ae, c, s.Encrypted())
		}
	}
	md, err := ps.Metadata()
	if err != nil || md.Len() != 1 {
		t.Fatal(md.Len(), err)
	}
	m, _ := md.At(0)
	if k, err := m.Key(); k != "k" || err != nil {
		t.Fatal(k, err)
	}
	if s, ok, err := m.Segment(); !ok || err != nil {
		t.Fatal(ok, err)
	} else if off, _ := s.Offset(); off != 100 {
		t.Fatal(off)
	}

	// A segment without compression or encryption.
	ps, _ = ReadPostscript(fbtest.Build(fbtest.Tab{0: fbtest.T(fbtest.Tab{})}))
	s, _, _ := ps.DType()
	if c, err := s.Compression(); c != 0 || err != nil || s.Encrypted() {
		t.Fatal(c, err)
	}
	if _, ok, _ := ps.Footer(); ok {
		t.Fatal("absent footer")
	}
	// A compression spec that is out of the buffer.
	b := fbtest.Build(fbtest.Tab{0: fbtest.T(fbtest.Tab{3: fbtest.T(fbtest.Tab{0: fbtest.U8(1)})})})
	ps, err = ReadPostscript(b[:len(b)-1])
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ = ps.DType()
	if _, err := s.Compression(); err == nil {
		t.Fatal("no error for a truncated compression spec")
	}
	if _, err := ReadPostscript(nil); err == nil {
		t.Fatal("no error for an empty buffer")
	}
}

func TestFooterStructs(t *testing.T) {
	seg := make([]byte, segmentSpecSize)
	binary.LittleEndian.PutUint64(seg, 1<<33)
	binary.LittleEndian.PutUint32(seg[8:], 77)
	seg[12], seg[13] = 6, 1
	binary.LittleEndian.PutUint16(seg[14:], 513)
	buf := fbtest.Build(fbtest.Tab{
		0: fbtest.Tables(fbtest.Tab{0: fbtest.Str("vortex.primitive")}),
		1: fbtest.Tables(fbtest.Tab{0: fbtest.Str("vortex.flat")}, fbtest.Tab{0: fbtest.Str("vortex.struct")}),
		2: fbtest.Structs(segmentSpecSize, seg, seg),
	})
	f, err := ReadFooter(buf)
	if err != nil {
		t.Fatal(err)
	}
	segs, err := f.SegmentSpecs()
	if err != nil || segs.Len() != 2 {
		t.Fatal(segs.Len(), err)
	}
	sp, err := segs.At(1)
	if want := (SegmentSpec{1 << 33, 77, 6, 1, 513}); sp != want || err != nil {
		t.Fatal(sp, err)
	}
	if _, err := segs.At(2); err == nil {
		t.Fatal("no error past the end")
	}
	ls, _ := f.LayoutSpecs()
	l, _ := ls.At(1)
	if id, _ := l.ID(); id != "vortex.struct" {
		t.Fatal(id)
	}
}

func TestLayoutTree(t *testing.T) {
	buf := fbtest.Build(fbtest.Tab{
		0: fbtest.U16(2), 1: fbtest.U64(1000), 2: fbtest.Bytes([]byte{1}),
		3: fbtest.Tables(fbtest.Tab{0: fbtest.U16(1), 4: fbtest.U32s(5, 6)}),
		4: fbtest.U32s(9),
	})
	l, err := ReadLayout(buf, 2)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := l.Encoding()
	rows, _ := l.RowCount()
	md, _ := l.Metadata()
	segs, _ := l.Segments()
	kids, err := l.Children()
	if enc != 2 || rows != 1000 || len(md) != 1 || len(segs) != 1 || kids.Len() != 1 || err != nil {
		t.Fatal(enc, rows, md, segs, kids.Len(), err)
	}
	kid, _ := kids.At(0)
	if s, _ := kid.Segments(); len(s) != 2 || s[1] != 6 {
		t.Fatal(s)
	}
	// One level deeper than allowed.
	l, _ = ReadLayout(buf, 1)
	kids, _ = l.Children()
	if _, err := kids.At(0); err == nil {
		t.Fatal("no depth error")
	}
}

func TestArray(t *testing.T) {
	buf0 := make([]byte, bufferSize)
	binary.LittleEndian.PutUint16(buf0, 5)
	buf0[2], buf0[3] = 4, 1
	binary.LittleEndian.PutUint32(buf0[4:], 4096)
	stats := fbtest.Tab{
		0: fbtest.Bytes([]byte{1}), 1: fbtest.U8(Exact), 2: fbtest.Bytes([]byte{2}), 3: fbtest.U8(Exact),
		4: fbtest.Bytes([]byte{3}), 5: fbtest.U8(1), 6: fbtest.U8(0), 7: fbtest.U8(1),
		8: fbtest.U64(11), 9: fbtest.U64(12), 10: fbtest.U64(13),
	}
	buf := fbtest.Build(fbtest.Tab{
		0: fbtest.T(fbtest.Tab{
			0: fbtest.U16(7), 1: fbtest.Bytes([]byte{9, 9}),
			2: fbtest.Tables(fbtest.Tab{0: fbtest.U16(8)}),
			3: fbtest.U16s(0, 3), 4: fbtest.T(stats),
		}),
		1: fbtest.Structs(bufferSize, buf0),
	})
	a, err := ReadArray(buf, 3)
	if err != nil {
		t.Fatal(err)
	}
	bufs, err := a.Buffers()
	if err != nil || bufs.Len() != 1 {
		t.Fatal(bufs.Len(), err)
	}
	if b, err := bufs.At(0); b != (Buffer{5, 4, 1, 4096}) || err != nil {
		t.Fatal(b, err)
	}
	n, ok, err := a.Root()
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	enc, _ := n.Encoding()
	md, _ := n.Metadata()
	bs, _ := n.Buffers()
	kids, _ := n.Children()
	if enc != 7 || len(md) != 2 || len(bs) != 2 || bs[1] != 3 || kids.Len() != 1 {
		t.Fatal(enc, md, bs, kids.Len())
	}
	kid, _ := kids.At(0)
	if e, _ := kid.Encoding(); e != 8 {
		t.Fatal(e)
	}
	if _, ok, _ := kid.Stats(); ok {
		t.Fatal("no stats expected")
	}
	st, ok, err := n.Stats()
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	mn, _, _ := st.Min()
	mx, _, _ := st.Max()
	sum, _, _ := st.Sum()
	mnp, _ := st.MinPrecision()
	mxp, _ := st.MaxPrecision()
	sorted, sok, _ := st.IsSorted()
	strict, kok, _ := st.IsStrictSorted()
	cons, cok, _ := st.IsConstant()
	nulls, nok, _ := st.NullCount()
	size, zok, _ := st.UncompressedSizeInBytes()
	nans, aok, _ := st.NanCount()
	if mn[0] != 1 || mx[0] != 2 || sum[0] != 3 || mnp != Exact || mxp != Exact ||
		!sorted || !sok || strict || !kok || !cons || !cok || nulls != 11 || !nok || size != 12 || !zok || nans != 13 || !aok {
		t.Fatal("stats")
	}
	// Absent optional stats and default precisions.
	n2, _ := ReadArray(fbtest.Build(fbtest.Tab{0: fbtest.T(fbtest.Tab{4: fbtest.T(fbtest.Tab{})})}), 3)
	r, _, _ := n2.Root()
	st, _, _ = r.Stats()
	_, sok, _ = st.IsSorted()
	_, nok, _ = st.NullCount()
	if p, _ := st.MinPrecision(); p != Inexact || sok || nok {
		t.Fatal("defaults")
	}
}

func TestFileStatistics(t *testing.T) {
	buf := fbtest.Build(fbtest.Tab{
		0: fbtest.Tables(fbtest.Tab{8: fbtest.U64(4)}),
		1: fbtest.Tables(fbtest.Tab{0: fbtest.Str("vortex.min"), 1: fbtest.Bytes([]byte{1})}, fbtest.Tab{0: fbtest.Str("vortex.max")}),
		2: fbtest.Tables(fbtest.Tab{0: fbtest.U16s(0, 1)}),
		3: fbtest.U16s(0, 0),
		4: fbtest.Bytes([]byte{2, 8, 0}),
	})
	s, err := ReadFileStatistics(buf)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := s.FieldStats()
	if err != nil || fs.Len() != 1 {
		t.Fatal(fs.Len(), err)
	}
	st, _ := fs.At(0)
	if n, _, _ := st.NullCount(); n != 4 {
		t.Fatal(n)
	}
	specs, err := s.AggregateSpecs()
	if err != nil || specs.Len() != 2 {
		t.Fatal(specs.Len(), err)
	}
	sp, _ := specs.At(0)
	id, _ := sp.ID()
	opts, _ := sp.Options()
	sp1, _ := specs.At(1)
	opts1, _ := sp1.Options()
	if id != "vortex.min" || len(opts) != 1 || len(opts1) != 0 {
		t.Fatal(id, opts, opts1)
	}
	sets, err := s.AggregateSets()
	if err != nil || sets.Len() != 1 {
		t.Fatal(sets.Len(), err)
	}
	set, _ := sets.At(0)
	if idx, err := set.Specs(); len(idx) != 2 || idx[1] != 1 || err != nil {
		t.Fatal(idx, err)
	}
	if e, err := s.EntrySets(); len(e) != 2 || err != nil {
		t.Fatal(e, err)
	}
	if p, err := s.Partials(); len(p) != 3 || err != nil {
		t.Fatal(p, err)
	}
}
