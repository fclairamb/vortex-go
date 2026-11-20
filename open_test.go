package vortex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
	"github.com/fclairamb/vortex-go/internal/testutil"
)

// ----- the fixtures -----

var ptypes = map[string]Kind{
	"i8": Int8, "i16": Int16, "i32": Int32, "i64": Int64, "u8": Uint8, "u16": Uint16,
	"u32": Uint32, "u64": Uint64, "f16": Float16, "f32": Float32, "f64": Float64,
}

var simple = map[string]Kind{
	"null": Null, "bool": Bool, "utf8": Utf8, "binary": Binary, "variant": Variant,
}

var units = map[string]Unit{"ns": Nanosecond, "µs": Microsecond, "ms": Millisecond, "s": Second, "days": Day}

// typeFromExpected converts the oracle's DType tree into a Type.
func typeFromExpected(t *testing.T, d *testutil.DType) Type {
	t.Helper()
	out := Type{nullable: d.Nullable}
	conv := func(c *testutil.DType) *Type { v := typeFromExpected(t, c); return &v }
	switch d.Kind {
	case "primitive":
		out.kind = ptypes[d.Ptype]
	case "decimal":
		out.kind, out.precision, out.scale = Decimal, uint8(d.Precision), int8(d.Scale)
	case "list":
		out.kind, out.elem = List, conv(d.Element)
	case "fixed_size_list":
		out.kind, out.elem, out.size = FixedSizeList, conv(d.Element), uint32(d.Size)
	case "map":
		out.kind, out.elem, out.value = Map, conv(d.Key), conv(d.Value)
	case "struct":
		out.kind = Struct
		for _, f := range d.Fields {
			out.fields = append(out.fields, Field{f.Name, typeFromExpected(t, f.DType)})
		}
	case "extension":
		out.nullable = d.Storage.Nullable
		meta, zone, _ := strings.Cut(d.ExtMetadata, ", tz=")
		out.unit, out.zone = units[meta], zone
		switch d.ExtID {
		case "vortex.date":
			out.kind = Date
		case "vortex.time":
			out.kind = Time
		case "vortex.timestamp":
			out.kind = Timestamp
		case "vortex.uuid":
			out.kind = UUID
		default:
			t.Fatalf("extension %q has no type", d.ExtID)
		}
	default:
		k, ok := simple[d.Kind]
		if !ok {
			t.Fatalf("kind %q has no type", d.Kind)
		}
		out.kind = k
	}
	return out
}

type countingReader struct {
	r     io.ReaderAt
	reads atomic.Int32
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	c.reads.Add(1)
	return c.r.ReadAt(p, off)
}

func fixtures(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("testdata/fixtures/*/*.vortex")
	if err != nil || len(paths) == 0 {
		t.Fatal("no fixtures", err)
	}
	return paths
}

func TestOpenFixtures(t *testing.T) {
	for _, path := range fixtures(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			exp, err := testutil.LoadExpected(path + ".expected.json")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cr := &countingReader{r: bytes.NewReader(raw)}
			f, err := Open(cr, int64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			if n := cr.reads.Load(); n > 2 {
				t.Errorf("%d reads, want at most 2", n)
			}
			if f.NumRows() != exp.Rows {
				t.Errorf("NumRows = %d, want %d", f.NumRows(), exp.Rows)
			}
			if want := typeFromExpected(t, &exp.Schema); !f.Schema().root.Equal(want) {
				t.Errorf("schema = %s\nwant     %s", f.Schema(), want)
			}
			if len(f.arrayEncodings) == 0 || len(f.layoutEncodings) == 0 || len(f.segments) == 0 {
				t.Error("the footer is empty")
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenFile(t *testing.T) {
	path := fixtures(t)[0]
	f, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Schema().Fields()) == 0 {
		t.Error("no fields")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenFile(filepath.Join(t.TempDir(), "missing.vortex")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.vortex")
	if err := os.WriteFile(bad, []byte("not vortex at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFile(bad); !errors.Is(err, ErrNotVortex) || !strings.Contains(err.Error(), "bad.vortex") {
		t.Fatalf("not a vortex file: %v", err)
	}
	if _, err := OpenFile(t.TempDir()); err == nil {
		t.Fatal("a directory must not open")
	}
}

type failingCloser struct{}

func (failingCloser) Close() error { return errors.New("boom") }

func TestCloseError(t *testing.T) {
	f := &File{closer: failingCloser{}}
	if err := f.Close(); err == nil || err.Error() != "boom" {
		t.Fatalf("Close = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if err := (&File{}).Close(); err != nil {
		t.Fatal(err)
	}
}

// ----- synthetic files -----

func prim(p uint8, nullable bool) fbtest.Tab {
	n := uint8(0)
	if nullable {
		n = 1
	}
	return fbtest.Tab{0: fbtest.U8(3), 1: fbtest.T(fbtest.Tab{0: fbtest.U8(p), 1: fbtest.U8(n)})}
}

// dtypeTab is a DType table of the union arm kind (the fbs.Kind values).
func dtypeTab(kind uint8, body fbtest.Tab) fbtest.Tab {
	return fbtest.Tab{0: fbtest.U8(kind), 1: fbtest.T(body)}
}

func structTab(names []string, types ...fbtest.Tab) fbtest.Tab {
	return dtypeTab(7, fbtest.Tab{0: fbtest.Strs(names...), 1: fbtest.Tables(types...)})
}

func extTab(id string, meta []byte, storage fbtest.Tab) fbtest.Tab {
	body := fbtest.Tab{0: fbtest.Str(id), 1: fbtest.T(storage)}
	if meta != nil {
		body[2] = fbtest.Bytes(meta)
	}
	return dtypeTab(9, body)
}

func segSpec(off uint64, length uint32) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint64(b, off)
	binary.LittleEndian.PutUint32(b[8:], length)
	return b
}

func layoutTab(enc uint16, rows uint64, segs []uint32, kids ...fbtest.Tab) fbtest.Tab {
	t := fbtest.Tab{0: fbtest.U16(enc), 1: fbtest.U64(rows), 4: fbtest.U32s(segs...)}
	if kids != nil {
		t[3] = fbtest.Tables(kids...)
	}
	return t
}

type parts struct {
	dtype   []byte // nil: no dtype segment
	layout  []byte
	footer  []byte
	rewrite func(ps map[string]fbtest.Tab) // changes the segments of the postscript
	version uint16
	psLen   int // 0: the real length
	head    string
	tail    string
	postBuf func([]byte) []byte
}

func goodDType() fbtest.Tab {
	return structTab([]string{"a", "b"}, prim(7, false), prim(4, true))
}

func goodFooter(segs ...[]byte) fbtest.Tab {
	return fbtest.Tab{
		0: fbtest.Tables(fbtest.Tab{0: fbtest.Str("vortex.primitive")}),
		1: fbtest.Tables(fbtest.Tab{0: fbtest.Str("vortex.flat")}, fbtest.Tab{0: fbtest.Str("vortex.struct")}),
		2: fbtest.Structs(16, segs...),
	}
}

func defaults() parts {
	return parts{
		dtype:   fbtest.Build(goodDType()),
		layout:  fbtest.Build(layoutTab(1, 42, nil, layoutTab(0, 42, []uint32{0}))),
		footer:  fbtest.Build(goodFooter(segSpec(4, 8))),
		version: 1, head: magic, tail: magic,
	}
}

func segTab(off uint64, length uint32) fbtest.Tab {
	return fbtest.Tab{0: fbtest.U64(off), 1: fbtest.U32(length), 2: fbtest.U8(3)}
}

// build lays out head magic, dtype, layout, footer, postscript and trailer.
func (p parts) build() []byte {
	var out bytes.Buffer
	out.WriteString(p.head)
	ps := map[string]fbtest.Tab{}
	add := func(name string, b []byte) {
		ps[name] = segTab(uint64(out.Len()), uint32(len(b)))
		out.Write(b)
	}
	if p.dtype != nil {
		add("dtype", p.dtype)
	}
	add("layout", p.layout)
	add("footer", p.footer)
	if p.rewrite != nil {
		p.rewrite(ps)
	}
	root := fbtest.Tab{}
	for id, name := range []string{"dtype", "layout", "stats", "footer"} {
		if s, ok := ps[name]; ok {
			root[id] = fbtest.T(s)
		}
	}
	psBuf := fbtest.Build(root)
	if p.postBuf != nil {
		psBuf = p.postBuf(psBuf)
	}
	out.Write(psBuf)
	n := p.psLen
	if n == 0 {
		n = len(psBuf)
	}
	trailer := binary.LittleEndian.AppendUint16(nil, p.version)
	trailer = binary.LittleEndian.AppendUint16(trailer, uint16(n))
	out.Write(trailer)
	out.WriteString(p.tail)
	return out.Bytes()
}

func open(b []byte) (*File, error) { return Open(bytes.NewReader(b), int64(len(b))) }

func TestOpenSynthetic(t *testing.T) {
	f, err := open(defaults().build())
	if err != nil {
		t.Fatal(err)
	}
	if f.NumRows() != 42 {
		t.Fatal(f.NumRows())
	}
	if got := f.Schema().String(); got != "struct{a: i64, b: i8?}" {
		t.Fatal(got)
	}
	if len(f.layout.children) != 1 || f.layout.children[0].segments[0] != 0 {
		t.Fatalf("layout = %+v", f.layout)
	}
	// A statistics segment is accepted and ignored.
	p := defaults()
	p.rewrite = func(ps map[string]fbtest.Tab) { ps["stats"] = ps["layout"] }
	if _, err := open(p.build()); err != nil {
		t.Fatal(err)
	}
}

func TestOpenDamage(t *testing.T) {
	huge := func(ps map[string]fbtest.Tab) { ps["footer"] = segTab(1<<40, 10) }
	tests := []struct {
		name string
		raw  func() []byte
		want error
		text string
	}{
		{"empty", func() []byte { return nil }, ErrNotVortex, "too short"},
		{"11 bytes", func() []byte { return make([]byte, 11) }, ErrNotVortex, "too short"},
		{"first magic", func() []byte { p := defaults(); p.head = "VTXG"; return p.build() }, ErrNotVortex, "offset 0"},
		{"last magic", func() []byte { p := defaults(); p.tail = "VTXG"; return p.build() }, ErrNotVortex, "offset"},
		{"version 2", func() []byte { p := defaults(); p.version = 2; return p.build() }, ErrUnsupportedVersion, "version 2"},
		{"postscript larger than the file", func() []byte { p := defaults(); p.psLen = 9000; return p.build() }, ErrCorrupt, "9000"},
		{"empty postscript", func() []byte { p := defaults(); p.postBuf = func([]byte) []byte { return nil }; return p.build() }, ErrCorrupt, "postscript"},
		{"segment past the end", func() []byte { p := defaults(); p.rewrite = huge; return p.build() }, ErrCorrupt, "outside the file body"},
		{"segment length past the end", func() []byte {
			p := defaults()
			p.rewrite = func(ps map[string]fbtest.Tab) { ps["layout"] = segTab(4, 1<<30) }
			return p.build()
		}, ErrCorrupt, "outside the file body"},
		{"segment in the magic", func() []byte {
			p := defaults()
			p.rewrite = func(ps map[string]fbtest.Tab) { ps["dtype"] = segTab(0, 3) }
			return p.build()
		}, ErrCorrupt, "outside"},
		{"no layout segment", func() []byte {
			p := defaults()
			p.rewrite = func(ps map[string]fbtest.Tab) { delete(ps, "layout") }
			return p.build()
		}, ErrCorrupt, "no layout segment"},
		{"no footer segment", func() []byte {
			p := defaults()
			p.rewrite = func(ps map[string]fbtest.Tab) { delete(ps, "footer") }
			return p.build()
		}, ErrCorrupt, "no footer segment"},
		{"segment that is not a table", func() []byte {
			p := defaults()
			p.postBuf = func([]byte) []byte { return fbtest.Build(fbtest.Tab{0: fbtest.U32(1 << 20)}) }
			return p.build()
		}, ErrCorrupt, "dtype segment"},
		{"unsorted segments", func() []byte {
			p := defaults()
			p.footer = fbtest.Build(goodFooter(segSpec(8, 2), segSpec(4, 2)))
			return p.build()
		}, ErrCorrupt, "sorted"},
		{"footer segment past the end", func() []byte {
			p := defaults()
			p.footer = fbtest.Build(goodFooter(segSpec(1<<20, 2)))
			return p.build()
		}, ErrCorrupt, "past the end"},
		{"footer segment length past the end", func() []byte {
			p := defaults()
			p.footer = fbtest.Build(goodFooter(segSpec(4, 1<<30)))
			return p.build()
		}, ErrCorrupt, "past the end"},
		{"root dtype is not a struct", func() []byte {
			p := defaults()
			p.dtype = fbtest.Build(prim(4, false))
			return p.build()
		}, ErrCorrupt, "want a struct"},
		{"unknown extension", func() []byte {
			p := defaults()
			p.dtype = fbtest.Build(structTab([]string{"x"}, extTab("acme.thing", nil, prim(4, false))))
			return p.build()
		}, ErrCorrupt, `"acme.thing"`},
		{"union", func() []byte {
			p := defaults()
			p.dtype = fbtest.Build(structTab([]string{"x"}, dtypeTab(12, fbtest.Tab{})))
			return p.build()
		}, ErrCorrupt, "union"},
		{"layout encoding out of range", func() []byte {
			p := defaults()
			p.layout = fbtest.Build(layoutTab(7, 1, nil))
			return p.build()
		}, ErrCorrupt, "layout encoding 7"},
		{"layout segment out of range", func() []byte {
			p := defaults()
			p.layout = fbtest.Build(layoutTab(0, 1, []uint32{3}))
			return p.build()
		}, ErrCorrupt, "segment 3"},
		{"child layout out of range", func() []byte {
			p := defaults()
			p.layout = fbtest.Build(layoutTab(0, 1, nil, layoutTab(9, 1, nil)))
			return p.build()
		}, ErrCorrupt, "layout encoding 9"},
		{"too many rows", func() []byte {
			p := defaults()
			p.layout = fbtest.Build(layoutTab(0, 1<<63, nil))
			return p.build()
		}, ErrCorrupt, "too large"},
		{"garbage footer", func() []byte {
			p := defaults()
			p.footer = []byte{1, 2, 3}
			return p.build()
		}, ErrCorrupt, "footer"},
		{"garbage layout", func() []byte {
			p := defaults()
			p.layout = []byte{1, 2, 3}
			return p.build()
		}, ErrCorrupt, "layout"},
		{"garbage dtype", func() []byte {
			p := defaults()
			p.dtype = []byte{1, 2, 3}
			return p.build()
		}, ErrCorrupt, "dtype"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := open(tt.raw())
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.text) {
				t.Fatalf("err = %q, want %q in it", err, tt.text)
			}
		})
	}
}

func TestOpenNoDType(t *testing.T) {
	p := defaults()
	p.dtype = nil
	if _, err := open(p.build()); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestOpenBadFooterLists(t *testing.T) {
	// Each footer field points to garbage: a vector offset that leaves the buffer.
	for id := range 3 {
		p := defaults()
		p.footer = fbtest.Build(fbtest.Tab{id: fbtest.U32(1 << 20)})
		if _, err := open(p.build()); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("footer field %d: %v", id, err)
		}
	}
	// An element that is not a table, and a spec without id.
	for _, footer := range []fbtest.Tab{
		{0: fbtest.Tables(fbtest.Tab{})},
		{1: fbtest.Tables(fbtest.Tab{})},
		{2: fbtest.Structs(4, []byte{0, 0, 0, 0})},
	} {
		p := defaults()
		p.footer = fbtest.Build(footer)
		if _, err := open(p.build()); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("footer %v: %v", footer, err)
		}
	}
}

// TestOpenDamagedBuffers cuts each flatbuffer short, and flips each of its bytes: Open
// must fail with a known error or succeed, never panic.
func TestOpenDamagedBuffers(t *testing.T) {
	known := func(err error) bool {
		return err == nil || errors.Is(err, ErrCorrupt) || errors.Is(err, errors.ErrUnsupported)
	}
	base := defaults()
	base.layout = fbtest.Build(layoutTab(1, 42, []uint32{0}, layoutTab(0, 42, []uint32{0})))
	base.dtype = fbtest.Build(structTab([]string{"a", "d", "e", "f"}, prim(7, false),
		extTab("vortex.timestamp", []byte{2, 3, 0, 'U', 'T', 'C'}, prim(7, false)),
		dtypeTab(8, fbtest.Tab{0: fbtest.T(prim(1, false))}),
		dtypeTab(10, fbtest.Tab{0: fbtest.T(prim(1, false)), 1: fbtest.U32(4)})))
	buffers := map[string]func(p *parts) *[]byte{
		"dtype":  func(p *parts) *[]byte { return &p.dtype },
		"layout": func(p *parts) *[]byte { return &p.layout },
		"footer": func(p *parts) *[]byte { return &p.footer },
	}
	for name, get := range buffers {
		orig := *get(&base)
		for cut := range len(orig) {
			p := base
			*get(&p) = orig[:cut]
			if _, err := open(p.build()); !known(err) {
				t.Fatalf("%s cut at %d: %v", name, cut, err)
			}
		}
		for i := range orig {
			for _, v := range []byte{0, 0xff, 0x80} {
				p := base
				mut := bytes.Clone(orig)
				mut[i] = v
				*get(&p) = mut
				if _, err := open(p.build()); !known(err) {
					t.Fatalf("%s byte %d = %d: %v", name, i, v, err)
				}
			}
		}
	}
	// The postscript, by its length and its bytes.
	good := base.build()
	psLen := int(binary.LittleEndian.Uint16(good[len(good)-6:]))
	ps := good[len(good)-8-psLen : len(good)-8]
	for cut := range psLen {
		p := base
		p.postBuf = func(b []byte) []byte { return b[:cut] }
		if _, err := open(p.build()); !known(err) {
			t.Fatalf("postscript cut at %d: %v", cut, err)
		}
	}
	for i := range ps {
		for _, v := range []byte{0, 0xff, 0x80} {
			p := base
			p.postBuf = func(b []byte) []byte { b = bytes.Clone(b); b[i] = v; return b }
			if _, err := open(p.build()); !known(err) {
				t.Fatalf("postscript byte %d = %d: %v", i, v, err)
			}
		}
	}
}

// failingReader fails every read from failFrom on, once n reads were served.
type failingReader struct {
	r    io.ReaderAt
	ok   int
	err  error
	read int
}

func (f *failingReader) ReadAt(p []byte, off int64) (int, error) {
	f.read++
	if f.read > f.ok {
		return 0, f.err
	}
	return f.r.ReadAt(p, off)
}

func TestOpenReadErrors(t *testing.T) {
	boom := errors.New("boom")
	raw := bytes.Repeat([]byte{0}, 100000)
	for ok := range 2 {
		_, err := Open(&failingReader{r: bytes.NewReader(raw), ok: ok, err: boom}, int64(len(raw)))
		if !errors.Is(err, boom) {
			t.Fatalf("ok=%d: %v", ok, err)
		}
	}
	// A reader that is shorter than the size it claims.
	if _, err := Open(bytes.NewReader(raw[:50]), 60); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	if _, err := Open(&shortReader{raw}, 100); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}

type shortReader struct{ b []byte }

func (s *shortReader) ReadAt(p []byte, _ int64) (int, error) { return len(p) - 1, nil }

// TestOpenFarFooter puts the segments before the last 64 KiB: a second read is needed.
func TestOpenFarFooter(t *testing.T) {
	p := defaults()
	p.footer = fbtest.Build(goodFooter(segSpec(4, 8)))
	pad := bytes.Repeat([]byte{0}, 70000)
	// The dtype comes first, then padding, then the layout and the footer.
	var out bytes.Buffer
	out.WriteString(magic)
	segs := map[string]fbtest.Tab{}
	put := func(name string, b []byte) {
		segs[name] = segTab(uint64(out.Len()), uint32(len(b)))
		out.Write(b)
	}
	put("dtype", p.dtype)
	out.Write(pad)
	put("layout", p.layout)
	put("footer", p.footer)
	psBuf := fbtest.Build(fbtest.Tab{0: fbtest.T(segs["dtype"]), 1: fbtest.T(segs["layout"]), 3: fbtest.T(segs["footer"])})
	out.Write(psBuf)
	out.Write(binary.LittleEndian.AppendUint16(nil, 1))
	out.Write(binary.LittleEndian.AppendUint16(nil, uint16(len(psBuf))))
	out.WriteString(magic)
	cr := &countingReader{r: bytes.NewReader(out.Bytes())}
	f, err := Open(cr, int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if f.NumRows() != 42 || cr.reads.Load() != 3 {
		t.Fatal(f.NumRows(), cr.reads.Load())
	}
	// The same, with the far read failing.
	fr := &failingReader{r: bytes.NewReader(out.Bytes()), ok: 2, err: io.ErrClosedPipe}
	if _, err := Open(fr, int64(out.Len())); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}
