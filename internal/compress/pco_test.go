package compress

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// pcoKeys are sorted keys: the column tools/oracle/pcosize.py has the reference code with Pco.
func pcoKeys(rows int) []int64 {
	v := make([]int64, rows)
	for i := range v {
		v[i] = 1_000_000 + 3*int64(i) + int64(i)*int64(i)%3
	}
	return v
}

func pcoPrices(rows int) []float64 {
	v := make([]float64, rows)
	for i := range v {
		v[i] = float64(10000+i*37%90000) / 100
	}
	return v
}

// TestPcoPicked: in Compact, Pco takes the keys and the prices that FoR, ALP and zstd cost
// more on; in Default it is never a candidate.
func TestPcoPicked(t *testing.T) {
	keys := unsafex.Bytes(pcoKeys(20000))
	narrow := make([]uint16, 20000)
	wide := make([]int32, 20000)
	for i := range narrow {
		narrow[i] = uint16(i*3 + i*i%3)
		wide[i] = int32(i*3 + i*i%3)
	}
	nulls := make([]bool, 20000)
	for i := range nulls {
		nulls[i] = i%7 == 0
	}
	for name, run := range map[string]func(c *Compressor) *encode.Node{
		"i64":        func(c *Compressor) *encode.Node { return c.Numbers(I64, keys, nil) },
		"i64 nulls":  func(c *Compressor) *encode.Node { return c.Numbers(I64, keys, nulls) },
		"i32":        func(c *Compressor) *encode.Node { return c.Numbers(I32, unsafex.Bytes(wide), nil) },
		"u16":        func(c *Compressor) *encode.Node { return c.Numbers(U16, unsafex.Bytes(narrow), nil) },
		"f64 prices": func(c *Compressor) *encode.Node { return c.Numbers(F64, unsafex.Bytes(pcoPrices(20000)), nil) },
	} {
		t.Run(name, func(t *testing.T) {
			if got := ids(run(New(Compact))); !slices.Contains(got, encode.IDPco) {
				t.Fatalf("Compact: %v, want Pco", got)
			}
			if got := ids(run(New(Default))); slices.Contains(got, encode.IDPco) {
				t.Fatalf("Default: %v, never Pco", got)
			}
			if got := ids(run(NewLimited(Compact, Limits{NoPco: true}))); slices.Contains(got, encode.IDPco) {
				t.Fatalf("the edition has no Pco: %v", got)
			}
		})
	}
}

// TestPcoNever8Bit: i8 and u8 are never Pco.
func TestPcoNever8Bit(t *testing.T) {
	small := make([]byte, 20000)
	for i := range small {
		small[i] = byte(i / 70)
	}
	for _, p := range []PType{U8, I8} {
		if got := ids(New(Compact).Numbers(p, small, nil)); slices.Contains(got, encode.IDPco) {
			t.Fatalf("%d: %v", p, got)
		}
	}
	if New(Compact).pco(1) || !New(Compact).pco(2) {
		t.Fatal("Pco is a candidate for 8-bit numbers")
	}
}

const pcoSizeFile = "../../testdata/pco-size.json"

// TestPcoSizeAgainstTheReference: a sorted i64 key column of 100,000 rows, in Compact, takes at
// most 1.2 times the bytes of the reference's Pco array for the same data, which
// tools/oracle/pcosize.py measured (testdata/pco-size.json). Our size is committed and can only
// go down; PCO_SIZE_UPDATE=1 rewrites it.
func TestPcoSizeAgainstTheReference(t *testing.T) {
	raw, err := os.ReadFile(pcoSizeFile)
	if err != nil {
		t.Fatal(err)
	}
	var sizes map[string]int
	if err := json.Unmarshal(raw, &sizes); err != nil {
		t.Fatal(err)
	}
	if sizes["rows"] != 100000 {
		t.Fatalf("the file is for %d rows", sizes["rows"])
	}
	n := New(Compact).Numbers(I64, unsafex.Bytes(pcoKeys(sizes["rows"])), nil)
	if n.ID != encode.IDPco {
		t.Fatalf("the column is %v, want Pco", ids(n))
	}
	ours := n.Size()
	t.Logf("ours %d bytes, the reference %d, committed %d", ours, sizes["reference"], sizes["ours"])
	if 10*ours > 12*sizes["reference"] {
		t.Errorf("%d bytes, more than 1.2 times the reference's %d", ours, sizes["reference"])
	}
	if sizes["ours"] != 0 && ours > sizes["ours"] {
		t.Errorf("%d bytes, above the committed %d", ours, sizes["ours"])
	}
	if os.Getenv("PCO_SIZE_UPDATE") != "" && ours != sizes["ours"] {
		sizes["ours"] = ours
		out, _ := json.MarshalIndent(sizes, "", " ")
		if err := os.WriteFile(pcoSizeFile, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
