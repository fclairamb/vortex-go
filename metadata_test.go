package vortex

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/fbs"
	"github.com/fclairamb/vortex-go/internal/flatbuf"
	"github.com/fclairamb/vortex-go/internal/testutil"
)

// rustMetadata is the file the reference writer made with user metadata (`make metadata`), and
// what the reference reader reads back from it.
const rustMetadata = "testdata/metadata/rust.vortex"

// loadMetadata reads the lines of tools/variantjson's filemeta: a key, a tab, the value in hex.
func loadMetadata(t *testing.T, path string) map[string][]byte {
	t.Helper()
	raw, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	out := map[string][]byte{}
	sc := bufio.NewScanner(raw)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "\t")
		b, err := hex.DecodeString(v)
		if !ok || err != nil {
			t.Fatalf("%s: line %q", path, sc.Text())
		}
		out[k] = b
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// checkFileMetadata compares the metadata of f with want.
func checkFileMetadata(t *testing.T, f *File, want map[string][]byte) {
	t.Helper()
	keys := slices.Sorted(maps.Keys(want))
	if got := f.MetadataKeys(); !slices.Equal(got, keys) {
		t.Fatalf("keys %q, want %q", got, keys)
	}
	for _, k := range keys {
		v, ok, err := f.Metadata(k)
		if err != nil || !ok || !bytes.Equal(v, want[k]) {
			t.Fatalf("%q: %d bytes, %v, %v; want %d bytes", k, len(v), ok, err, len(want[k]))
		}
		if v == nil {
			t.Fatalf("%q: a nil value", k)
		}
	}
	if v, ok, err := f.Metadata("absent"); v != nil || ok || err != nil {
		t.Fatal(v, ok, err)
	}
}

// TestMetadataReference reads the metadata of the file of the reference writer, through each
// way to open a file: its 16 entries, a key of 64 bytes, an empty value, and a value of 80 KiB
// that the end read at Open does not hold.
func TestMetadataReference(t *testing.T) {
	want := loadMetadata(t, rustMetadata+".metadata")
	if len(want) != MaxMetadataEntries {
		t.Fatalf("%d entries", len(want))
	}
	raw, err := os.ReadFile(rustMetadata)
	if err != nil {
		t.Fatal(err)
	}
	for name, open := range map[string]func() (*File, error){
		"Open":       func() (*File, error) { return Open(bytes.NewReader(raw), int64(len(raw))) },
		"OpenFile":   func() (*File, error) { return OpenFile(rustMetadata) },
		"OpenMapped": func() (*File, error) { return OpenMapped(rustMetadata) },
	} {
		t.Run(name, func(t *testing.T) {
			f, err := open()
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if f.NumRows() != 1000 {
				t.Fatal(f.NumRows())
			}
			checkFileMetadata(t, f, want)
		})
	}
}

// metadataFile writes a file of a few rows with the metadata of o and of set.
func metadataFile(t testing.TB, o WriteOptions, set map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, NewSchema(Field{"id", Int64Type()}), o)
	if err := w.Write(Of([]int64{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		if err := w.SetMetadata(k, set[k]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// bigValue is a value that the end of the file read at Open does not hold.
func bigValue() []byte {
	b := make([]byte, maxTail+100)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func TestMetadataRoundTrip(t *testing.T) {
	opt := map[string][]byte{"schema.version": []byte("2"), "replaced": []byte("old"), "empty": nil}
	set := map[string][]byte{"replaced": []byte("new"), "dbbat.query_offsets": []byte(`{"a":1}`), "big": bigValue()}
	raw := metadataFile(t, WriteOptions{Metadata: opt}, set)
	opt["schema.version"][0] = 'x' // the writer has a copy
	want := map[string][]byte{
		"schema.version": []byte("2"), "replaced": []byte("new"), "empty": {},
		"dbbat.query_offsets": []byte(`{"a":1}`), "big": bigValue(),
	}
	f := openBytes(t, raw)
	checkFileMetadata(t, f, want)
	v, _, _ := f.Metadata("replaced")
	v[0] = 'X' // the caller has a copy
	checkFileMetadata(t, f, want)

	// The values are stored in the order of their keys, before the dtype, and they do not
	// change what the file holds.
	if got := readColumn(t, f, "id"); fmt.Sprint(got) != "[1 2 3]" {
		t.Fatal(got)
	}
	if i := bytes.Index(raw, []byte(`{"a":1}`)); i < 0 || i > bytes.Index(raw, []byte("new")) {
		t.Fatal("not in the order of the keys")
	}

	// The same through a file and a mapped file, and after Close.
	path := filepath.Join(t.TempDir(), "m.vortex")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, open := range []func(string) (*File, error){OpenFile, OpenMapped} {
		f, err := open(path)
		if err != nil {
			t.Fatal(err)
		}
		checkFileMetadata(t, f, want)
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := f.Metadata("big"); !ok || !errors.Is(err, os.ErrClosed) {
			t.Fatal(ok, err)
		}
		if v, ok, err := f.Metadata("schema.version"); !ok || err != nil || string(v) != "2" {
			t.Fatal(v, ok, err)
		}
	}
}

// readColumn reads the int64 column of a file.
func readColumn(t *testing.T, f *File, name string) []int64 {
	t.Helper()
	var out []int64
	for b, err := range f.Scan(t.Context(), Query{}, name) {
		if err != nil {
			t.Fatal(err)
		}
		v, err := Values[int64](b.Col(0))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, v...)
	}
	return out
}

// TestMetadataReads checks that a value outside the end of the file costs one read of its own
// bytes, and nothing when it is inside.
func TestMetadataReads(t *testing.T) {
	raw := metadataFile(t, WriteOptions{}, map[string][]byte{"big": bigValue(), "small": []byte("s")})
	fail := &testutil.FailAt{R: bytes.NewReader(raw), Err: errors.New("boom")}
	f, err := Open(fail, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	n := fail.Calls()
	if v, _, err := f.Metadata("small"); err != nil || string(v) != "s" || fail.Calls() != n {
		t.Fatal(v, err, fail.Calls()-n)
	}
	fail.N = n + 1
	if _, ok, err := f.Metadata("big"); !ok || err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), `"big"`) {
		t.Fatal(ok, err)
	}
	if v, _, err := f.Metadata("big"); err != nil || !bytes.Equal(v, bigValue()) || fail.Calls() != n+2 {
		t.Fatal(err, fail.Calls()-n)
	}
	if f.MetadataKeys() == nil {
		t.Fatal("nil keys")
	}
	if keys := openBytes(t, metadataFile(t, WriteOptions{}, nil)).MetadataKeys(); len(keys) != 0 {
		t.Fatal(keys)
	}
}

// TestMetadataLimits checks the rules of the reference on what the writer takes: an error of
// SetMetadata, or of the first Write or Close for WriteOptions.Metadata.
func TestMetadataLimits(t *testing.T) {
	sixteen := map[string][]byte{}
	for i := range MaxMetadataEntries {
		sixteen[fmt.Sprintf("%0*d", MaxMetadataKeyBytes, i)] = []byte{byte(i)}
	}
	f := openBytes(t, metadataFile(t, WriteOptions{Metadata: sixteen}, nil))
	if len(f.MetadataKeys()) != MaxMetadataEntries {
		t.Fatal(f.MetadataKeys())
	}
	seventeen := maps.Clone(sixteen)
	seventeen["another"] = nil
	schema := NewSchema(Field{"id", Int64Type()})
	for name, c := range map[string]struct {
		m    map[string][]byte
		want string
	}{
		"count": {seventeen, "17 metadata entries, at most 16"},
		"empty": {map[string][]byte{"": nil}, "a metadata key is empty"},
		"utf8":  {map[string][]byte{"\xff": nil}, "not valid UTF-8"},
		"long":  {map[string][]byte{strings.Repeat("é", 33): nil}, "is 66 bytes, at most 64"},
	} {
		t.Run(name, func(t *testing.T) {
			w := NewWriter(&bytes.Buffer{}, schema, WriteOptions{Metadata: c.m})
			if err := w.Write(Of([]int64{1})); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatal(err)
			}
			if err := w.Close(); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatal(err)
			}
			w = NewWriter(&bytes.Buffer{}, schema, WriteOptions{})
			if len(c.m) > 1 {
				return
			}
			for k, v := range c.m {
				if err := w.SetMetadata(k, v); err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal("SetMetadata closed the writer:", err)
			}
		})
	}

	w := NewWriter(&bytes.Buffer{}, schema, WriteOptions{Metadata: sixteen})
	if err := w.SetMetadata("another", nil); err == nil || !strings.Contains(err.Error(), "entry 17, at most 16") {
		t.Fatal(err)
	}
	for k := range sixteen {
		if err := w.SetMetadata(k, []byte("replaced")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.SetMetadata("k", nil); !errors.Is(err, ErrWriterClosed) {
		t.Fatal(err)
	}

	lowered(t, &maxSegment, 3)
	if err := NewWriter(&bytes.Buffer{}, schema, WriteOptions{}).SetMetadata("k", []byte("four")); err == nil ||
		!strings.Contains(err.Error(), `the metadata value of "k" is 4 bytes, at most 3`) {
		t.Fatal(err)
	}
}

func TestRowWriterMetadata(t *testing.T) {
	type row struct {
		ID int64 `vortex:"id"`
	}
	var buf bytes.Buffer
	w := NewRowWriter[row](&buf, WriteOptions{Metadata: map[string][]byte{"a": []byte("1")}})
	if _, err := w.Write([]row{{1}}); err != nil {
		t.Fatal(err)
	}
	if err := w.SetMetadata("b", []byte("2")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	checkFileMetadata(t, openBytes(t, buf.Bytes()), map[string][]byte{"a": []byte("1"), "b": []byte("2")})

	bad := NewRowWriter[int](&buf, WriteOptions{})
	if err := bad.SetMetadata("a", nil); err == nil || !strings.Contains(err.Error(), "not a struct") {
		t.Fatal(err)
	}
}

// withPostscript rebuilds the postscript of a file with the metadata that md builds.
func withPostscript(t *testing.T, raw []byte, md func(b *flatbuf.Builder) flatbuf.Off) []byte {
	t.Helper()
	psLen := int(binary.LittleEndian.Uint16(raw[len(raw)-6:]))
	psStart := len(raw) - trailerSize - psLen
	ps, err := fbs.ReadPostscript(raw[psStart : len(raw)-trailerSize])
	if err != nil {
		t.Fatal(err)
	}
	b := flatbuf.NewBuilder(256)
	m := md(b)
	var segs [4]flatbuf.Off
	for i, get := range []func() (fbs.PostscriptSegment, bool, error){ps.DType, ps.Layout, ps.Statistics, ps.Footer} {
		s, _, _ := get()
		off, _ := s.Offset()
		n, _ := s.Length()
		segs[i] = metaSegment(b, off, n, 3)
	}
	b.StartTable(5)
	for i, s := range segs {
		b.Ref(i, s)
	}
	b.Ref(4, m)
	out := append(slices.Clone(raw[:psStart]), b.Finish(b.EndTable())...)
	var trailer [8]byte
	binary.LittleEndian.PutUint16(trailer[0:], formatVersion)
	binary.LittleEndian.PutUint16(trailer[2:], uint16(len(out)-psStart))
	copy(trailer[4:], magic)
	return append(out, trailer[:]...)
}

func metaSegment(b *flatbuf.Builder, off uint64, n uint32, exp uint8) flatbuf.Off {
	b.StartTable(3)
	b.Uint64(0, off, 0)
	b.Uint32(1, n, 0)
	b.Uint8(2, exp, 0)
	return b.EndTable()
}

// metaEntries builds a vector of entries, each at offset 8 with 4 bytes, aligned to 8.
func metaEntries(keys ...string) func(b *flatbuf.Builder) flatbuf.Off {
	return func(b *flatbuf.Builder) flatbuf.Off {
		offs := make([]flatbuf.Off, len(keys))
		for i, k := range keys {
			key := b.String(k)
			s := metaSegment(b, 8, 4, 3)
			b.StartTable(2)
			b.Ref(0, key)
			b.Ref(1, s)
			offs[i] = b.EndTable()
		}
		return b.Offsets(offs)
	}
}

// oneEntry builds a vector of one entry, with the fields that set makes.
func oneEntry(set func(b *flatbuf.Builder) func()) func(b *flatbuf.Builder) flatbuf.Off {
	return func(b *flatbuf.Builder) flatbuf.Off {
		fields := set(b)
		b.StartTable(2)
		fields()
		return b.Offsets([]flatbuf.Off{b.EndTable()})
	}
}

// entryAt is an entry "k" whose value is n bytes at off, aligned to 2^exp.
func entryAt(off uint64, n uint32, exp uint8) func(b *flatbuf.Builder) flatbuf.Off {
	return oneEntry(func(b *flatbuf.Builder) func() {
		key := b.String("k")
		s := metaSegment(b, off, n, exp)
		return func() { b.Ref(0, key); b.Ref(1, s) }
	})
}

// TestMetadataCorrupt checks the rules of the reference reader on the postscript: Open refuses
// a file that breaks them.
func TestMetadataCorrupt(t *testing.T) {
	raw := metadataFile(t, WriteOptions{}, nil)
	keys := make([]string, MaxMetadataEntries+1)
	for i := range keys {
		keys[i] = fmt.Sprint(i)
	}
	for name, c := range map[string]struct {
		md   func(b *flatbuf.Builder) flatbuf.Off
		want string
	}{
		"count":     {metaEntries(keys...), "17 metadata entries, at most 16"},
		"duplicate": {metaEntries("a", "b", "a"), `the metadata key "a" is there twice`},
		"empty":     {metaEntries(""), "a metadata key is empty"},
		"utf8":      {metaEntries("\xc3"), "not valid UTF-8"},
		"long":      {metaEntries(strings.Repeat("k", 65)), "is 65 bytes, at most 64"},
		"no key": {oneEntry(func(b *flatbuf.Builder) func() {
			s := metaSegment(b, 8, 4, 3)
			return func() { b.Ref(1, s) }
		}), "a metadata key is empty"},
		"no segment": {oneEntry(func(b *flatbuf.Builder) func() {
			key := b.String("k")
			return func() { b.Ref(0, key) }
		}), `postscript metadata "k"`},
		"bad vector":  {func(b *flatbuf.Builder) flatbuf.Off { return b.String("\xff\xff\xff\x7f") }, "postscript metadata"},
		"past end":    {entryAt(8, 1<<31, 0), "past the end of the file"},
		"offset past": {entryAt(1<<40, 0, 0), "past the end of the file"},
		"align":       {entryAt(0, 4, 17), "aligned to 2^17, at most 2^16"},
		"misaligned":  {entryAt(9, 4, 1), "is not aligned to 2"},
	} {
		t.Run(name, func(t *testing.T) {
			bad := withPostscript(t, raw, c.md)
			_, err := Open(bytes.NewReader(bad), int64(len(bad)))
			if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), c.want) {
				t.Fatal(err)
			}
		})
	}
	// What the reference accepts: a value aligned to 64 KiB (at 0), and an empty one.
	for _, md := range []func(b *flatbuf.Builder) flatbuf.Off{entryAt(0, 4, 16), entryAt(8, 0, 0)} {
		ok := withPostscript(t, raw, md)
		f, err := Open(bytes.NewReader(ok), int64(len(ok)))
		if err != nil {
			t.Fatal(err)
		}
		if _, has, err := f.Metadata("k"); !has || err != nil {
			t.Fatal(has, err)
		}
	}
}
