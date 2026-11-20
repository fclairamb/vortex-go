package unsafex

import (
	"bytes"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSliceAligned(t *testing.T) {
	b := unsafeBytes([]uint64{1, 2})
	got := Slice[uint64](b)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("got %v", got)
	}
	got[0] = 7 // aligned: shares the memory
	if b[0] != 7 {
		t.Fatal("an aligned slice must share the memory")
	}
}

func TestSliceUnaligned(t *testing.T) {
	// A 64-bit aligned base, then a view one byte in: misaligned for every width above 1.
	base := make([]uint64, 3)
	raw := unsafeBytes(base)
	view := raw[1:17]
	for i := range view {
		view[i] = byte(i + 1)
	}
	got := Slice[uint16](view)
	if len(got) != 8 || got[0] != 0x0201 || got[7] != 0x100f {
		t.Fatalf("uint16: %x", got)
	}
	got[0] = 0 // a copy: the source is untouched
	if view[0] != 1 {
		t.Fatal("a misaligned slice must be a copy")
	}
	if g := Slice[float64](view[:8]); len(g) != 1 {
		t.Fatalf("float64: %v", g)
	}
}

func TestSliceEmpty(t *testing.T) {
	if Slice[int32](nil) != nil || Slice[int32]([]byte{1, 2, 3}) != nil {
		t.Fatal("fewer bytes than one value must give nil")
	}
	if got := Slice[uint8]([]byte{4, 5}); len(got) != 2 || got[1] != 5 {
		t.Fatalf("uint8: %v", got)
	}
}

// TestOnlyUnsafexImportsUnsafe fails when any other package of the module imports unsafe.
func TestOnlyUnsafexImportsUnsafe(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n != "." && n != ".." && (strings.HasPrefix(n, ".") || n == "testdata" || n == "unsafex") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"unsafe"` {
				t.Errorf("%s imports unsafe: only internal/unsafex may", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWalkSeesTheModule(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "..", "go.mod")); err != nil {
		t.Fatal(err)
	}
}

type putRow struct {
	N     int64
	S     string
	B     []byte
	Valid bool
	V     string
}

func offsetOf(name string) uintptr {
	f, _ := reflect.TypeFor[putRow]().FieldByName(name)
	return f.Offset
}

// TestPutKeepsPointersAlive writes strings and slices through Put and runs the collector in
// between: the values must survive, as the destination is typed memory the collector scans.
func TestPutKeepsPointersAlive(t *testing.T) {
	rows := make([]putRow, 50)
	rv := reflect.ValueOf(rows)
	Put(rv, offsetOf("N"), []int64{1, 2, 3})
	PutFunc(rv, offsetOf("S"), func(i int) string { return strings.Repeat("s", i+1) })
	PutFunc(rv, offsetOf("B"), func(i int) []byte { return bytes.Repeat([]byte{byte(i)}, i+1) })
	null := func(i int) bool { return i%2 == 1 }
	PutNull(rv, offsetOf("V"), offsetOf("Valid"), []string{"a", "b", "c"}, null)
	for range 3 {
		runtime.GC()
		_ = make([]byte, 1<<20)
	}
	for i, r := range rows {
		if r.S != strings.Repeat("s", i+1) || !bytes.Equal(r.B, bytes.Repeat([]byte{byte(i)}, i+1)) {
			t.Fatalf("row %d: %+v", i, r)
		}
		if i < 3 && r.N != int64(i+1) || i >= 3 && r.N != 0 {
			t.Fatalf("row %d: N = %d, only 3 values were put", i, r.N)
		}
		if want := i < 3 && i%2 == 0; r.Valid != want || (want && r.V != string(rune('a'+i))) || (!want && r.V != "") {
			t.Fatalf("row %d: %+v", i, r)
		}
	}
	// A null row gets the zero value, also over what a previous batch left.
	rows[1].V, rows[1].Valid = "old", true
	PutNullFunc(rv, offsetOf("V"), offsetOf("Valid"), func(i int) string { return "new" }, null)
	if rows[0].V != "new" || !rows[0].Valid || rows[1].V != "" || rows[1].Valid {
		t.Fatalf("%+v %+v", rows[0], rows[1])
	}
	Put(reflect.ValueOf([]putRow(nil)), 0, []int64{1})
}

func TestBytes(t *testing.T) {
	v := []uint32{0x04030201, 0x08070605}
	b := Bytes(v)
	if !bytes.Equal(b, []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatal(b)
	}
	b[0] = 9 // shares the memory
	if v[0] != 0x04030209 {
		t.Fatal(v)
	}
	if Bytes([]int8{}) != nil || Bytes[float64](nil) != nil {
		t.Fatal("an empty slice has no bytes")
	}
	if got := Bytes([]float64{1}); len(got) != 8 {
		t.Fatal(len(got))
	}
}

func TestStringBytes(t *testing.T) {
	if got := StringBytes("abc"); string(got) != "abc" {
		t.Fatalf("got %q", got)
	}
	if got := StringBytes(""); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}
