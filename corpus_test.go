package vortex

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/testutil"
)

// pending lists what the corpus harness cannot check yet, each with the identifier it waits
// for. A key is a fixture ("alp"), or a fixture and a column ("struct_nested/inner.b"); the
// value must appear in the error. A fixture with an entry fails as a whole when the scan
// itself fails with that identifier. An entry that no error matches makes the test fail: the
// list can only shrink.
var pending = map[string]string{
	"alp":                           "vortex.alp",
	"alprd":                         "vortex.alprd",
	"bitpacked":                     "fastlanes.bitpacked",
	"constant":                      "vortex.constant",
	"datetimeparts":                 "vortex.datetimeparts",
	"decimal":                       "vortex.decimal",
	"decimal_byte_parts":            "vortex.decimal_byte_parts",
	"decimal_byte_parts_narrow_msp": "vortex.decimal_byte_parts",
	"decimal_canonical_widths":      "vortex.decimal",
	"decimal_gap_values":            "vortex.decimal",
	"decimal_high_scale":            "vortex.decimal",
	"decimal_narrow_storage":        "vortex.decimal",
	"decimal_null_slots":            "vortex.decimal",
	"decimal_wide_storage":          "vortex.decimal",
	"dict":                          "vortex.dict",
	"fixed_size_list":               "vortex.fixed_size_list",
	"for":                           "fastlanes.for",
	"fsst":                          "vortex.fsst",
	"list":                          "vortex.list",
	"listview":                      "vortex.listview",
	"map":                           "vortex.map",
	"pco":                           "vortex.pco",
	"rle":                           "fastlanes.rle",
	"runend":                        "vortex.runend",
	"sequence":                      "vortex.sequence",
	"sparse":                        "vortex.sparse",
	"struct_nested/inner.b":         "vortex.varbin",
	"varbin":                        "vortex.varbin",
	"varbinview":                    "vortex.varbinview",
	"zigzag":                        "vortex.zigzag",
	"zstd":                          "vortex.zstd",
	"masked":                        "vortex.zoned",
	"onpair":                        "vortex.zoned",
	"uuid":                          "vortex.zoned",
	"wide":                          "vortex.zoned",
}

// fixtureName is the key of a fixture in pending.
func fixtureName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".vortex")
}

// acc accumulates the canonical hash and the null count of one column path.
type acc struct {
	h       *testutil.Hasher
	nulls   int64
	skipped bool // a field of it is pending: its hash cannot be checked
}

// hashRow adds row i of c to h. It returns false when c holds a pending field.
func hashRow(h *testutil.Hasher, c *Column, i int) bool {
	if c.arr.Skipped != nil {
		return false
	}
	if c.Null(i) {
		h.Null()
		return true
	}
	h.Valid()
	switch c.arr.Type.Kind {
	case array.Bool:
		h.Bool(c.Bool(i))
	case array.Prim:
		w := c.arr.Type.PType.Width()
		h.Raw(c.arr.Data[i*w : (i+1)*w])
	case array.Struct:
		for k := range c.arr.Fields {
			if !hashRow(h, c.Field(k), i) {
				return false
			}
		}
	}
	return true
}

// addPath adds the n rows of a column to its path and, for a struct, to its fields' paths,
// where a row is null when a parent is.
func addPath(accs map[string]*acc, path string, c *Column, n int, parentNull func(int) bool) {
	a := accs[path]
	if c.arr.Skipped != nil {
		a.skipped = true
		return
	}
	for i := range n {
		switch {
		case parentNull(i):
			a.h.Null()
			a.nulls++
		case !hashRow(a.h, c, i):
			a.skipped = true
		case c.Null(i):
			a.nulls++
		}
	}
	if c.arr.Type.Kind != array.Struct {
		return
	}
	for k, f := range c.typ.fields {
		child := path + "." + f.Name
		addPath(accs, child, c.Field(k), n, func(i int) bool { return parentNull(i) || c.Null(i) })
	}
	for k := range c.typ.fields {
		if accs[path+"."+c.typ.fields[k].Name].skipped {
			a.skipped = true
		}
	}
}

// checkFixture scans the fixture and compares it with the oracle, with batches of batchRows.
// It records in hit the pending entries it used.
func checkFixture(path string, batchRows int, hit map[string]bool) error {
	exp, err := testutil.LoadExpected(path + ".expected.json")
	if err != nil {
		return err
	}
	f, err := OpenFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	name := fixtureName(path)
	opt := scanOptions{batchRows: batchRows, array: array.Options{
		Tolerate: func(col string, err error) bool {
			for _, key := range []string{name, name + "/" + col} {
				if id, ok := pending[key]; ok && strings.Contains(err.Error(), id) {
					hit[key] = true
					return true
				}
			}
			return false
		},
	}}
	accs := map[string]*acc{}
	for _, p := range exp.Schema.Paths("") {
		accs[p] = &acc{h: testutil.NewHasher()}
	}
	var rows int64
	for b, err := range f.scan(context.Background(), opt) {
		if err != nil {
			if id, ok := pending[name]; ok && strings.Contains(err.Error(), id) {
				hit[name] = true
				return nil
			}
			return err
		}
		rows += int64(b.Len())
		if b.Len() > batchRows {
			return fmt.Errorf("a batch of %d rows, at most %d", b.Len(), batchRows)
		}
		for k, fld := range f.Schema().Fields() {
			addPath(accs, fld.Name, b.Col(k), b.Len(), func(int) bool { return false })
		}
	}
	return compare(exp, accs, rows)
}

func compare(exp *testutil.Expected, accs map[string]*acc, rows int64) error {
	if rows != exp.Rows {
		return fmt.Errorf("%d rows, want %d", rows, exp.Rows)
	}
	for _, p := range exp.Schema.Paths("") {
		a, want := accs[p], exp.Columns[p]
		if a.skipped {
			continue
		}
		if a.nulls != want.Nulls {
			return fmt.Errorf("column %s: %d nulls, want %d", p, a.nulls, want.Nulls)
		}
		if got := a.h.Sum(); want.SHA256 == nil || got != *want.SHA256 {
			return fmt.Errorf("column %s: hash %s, want %v", p, got, want.SHA256)
		}
	}
	return nil
}

func corpus(t *testing.T) []string {
	t.Helper()
	paths := append(fixtures(t), mustGlob(t, "testdata/fixtures/*/extra/*.vortex")...)
	slices.Sort(paths)
	return paths
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestCorpus(t *testing.T) {
	for _, batchRows := range []int{maxBatchRows, 1, 7, 1024, 100000} {
		t.Run(fmt.Sprint(batchRows), func(t *testing.T) {
			hit := map[string]bool{}
			for _, path := range corpus(t) {
				if err := checkFixture(path, batchRows, hit); err != nil {
					t.Errorf("%s: %v", fixtureName(path), err)
				}
			}
			for key, id := range pending {
				if !hit[key] {
					t.Errorf("pending entry %q (%s) passes, remove it", key, id)
				}
			}
		})
	}
}

func TestPendingNamesExist(t *testing.T) {
	names := map[string]bool{}
	for _, path := range corpus(t) {
		names[fixtureName(path)] = true
	}
	for key := range pending {
		if name, _, _ := strings.Cut(key, "/"); !names[name] {
			t.Errorf("pending entry %q: no such fixture", key)
		}
	}
}

func TestHarnessCatchesWrongValues(t *testing.T) {
	exp, err := testutil.LoadExpected("testdata/fixtures/0.93.0/primitives.vortex.expected.json")
	if err != nil {
		t.Fatal(err)
	}
	accs := map[string]*acc{}
	for _, p := range exp.Schema.Paths("") {
		accs[p] = &acc{h: testutil.NewHasher()}
	}
	for _, a := range accs {
		a.nulls = 99
	}
	if err := compare(exp, accs, exp.Rows); err == nil || !strings.Contains(err.Error(), "nulls") {
		t.Fatalf("a null count off must be caught: %v", err)
	}
	for p, a := range accs {
		a.nulls = exp.Columns[p].Nulls
	}
	if err := compare(exp, accs, exp.Rows); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("a hash off must be caught: %v", err)
	}
	if err := compare(exp, accs, 1); err == nil || !strings.Contains(err.Error(), "rows") {
		t.Fatalf("a row count off must be caught: %v", err)
	}
}
