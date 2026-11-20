package unsafex

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
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
