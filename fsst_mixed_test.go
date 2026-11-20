package vortex

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/fclairamb/vortex-go/internal/encode"
)

// The string columns of the committed default_mixed file, which the compression ratio of our FSST and OnPair
// is checked on: tools/vectors makes what the reference's FSST makes of the same strings
// (testdata/vectors/fsst/default_mixed.json), and our output may be at most 1.15 times it. The
// strings reach tools/vectors in a file that this test writes when MIXED_STRINGS_OUT is set.

const mixedFile = "testdata/fixtures/0.93.0/extra/default_mixed.vortex"

type mixedColumn struct {
	vals  [][]byte
	nulls []bool
}

// mixedStrings reads the three string columns of default_mixed.
func mixedStrings(t *testing.T) map[string]*mixedColumn {
	f, err := OpenFile(mixedFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	idx := map[string]int{}
	for i, fl := range f.Schema().Fields() {
		idx[fl.Name] = i
	}
	cols := map[string]*mixedColumn{"str_low": {}, "str_high": {}, "rec.b": {}}
	add := func(name string, c *Column, rows int) {
		for i := range rows {
			col := cols[name]
			col.nulls = append(col.nulls, c.Null(i))
			col.vals = append(col.vals, append([]byte(nil), c.Bytes(i)...))
		}
	}
	for b, err := range f.Scan(context.Background(), Query{}) {
		if err != nil {
			t.Fatal(err)
		}
		add("str_low", b.Col(idx["str_low"]), b.Len())
		add("str_high", b.Col(idx["str_high"]), b.Len())
		rec := b.Col(idx["rec"])
		for i := range b.Len() {
			col := cols["rec.b"]
			if rec.Null(i) || rec.Field(1).Null(i) {
				col.nulls, col.vals = append(col.nulls, true), append(col.vals, nil)
				continue
			}
			col.nulls, col.vals = append(col.nulls, false), append(col.vals, append([]byte(nil), rec.Field(1).Bytes(i)...))
		}
	}
	return cols
}

func TestFSSTCompressionRatio(t *testing.T) {
	mixedRatio(t, "fsst", encode.FSSTBytes)
}

// TestOnPairCompressionRatio is the same for OnPair, against vortex-onpair's output.
func TestOnPairCompressionRatio(t *testing.T) {
	mixedRatio(t, "onpair", encode.OnPairBytes)
}

func mixedRatio(t *testing.T, enc string, run func(*encode.Encoder, [][]byte, []bool) *encode.Node) {
	cols := mixedStrings(t)
	if out := os.Getenv("MIXED_STRINGS_OUT"); out != "" {
		dump := map[string][]string{}
		valid := map[string][]bool{}
		for name, c := range cols {
			for i, v := range c.vals {
				dump[name] = append(dump[name], hex.EncodeToString(v))
				valid[name] = append(valid[name], !c.nulls[i])
			}
		}
		b, _ := json.Marshal(map[string]any{"strings": dump, "valid": valid})
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile("testdata/vectors/" + enc + "/default_mixed.json")
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Columns map[string]struct {
			Rows      int `json:"rows"`
			Bytes     int `json:"bytes"`
			Reference int `json:"reference"`
		} `json:"columns"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	e := encode.New()
	for name, c := range cols {
		want, ok := ref.Columns[name]
		total := 0
		for i, v := range c.vals {
			if !c.nulls[i] {
				total += len(v)
			}
		}
		if !ok || want.Rows != len(c.vals) || want.Bytes != total {
			t.Fatalf("%s: the reference made %+v of other strings (%d rows, %d bytes)", name, want, len(c.vals), total)
		}
		e.Reset()
		got := run(e, c.vals, c.nulls).Size()
		t.Logf("%s: %d bytes, the reference's %d, for %d of strings", name, got, want.Reference, total)
		// the floor can only rise: 1.15 times the reference's
		if 100*got > 115*want.Reference {
			t.Errorf("%s: %d bytes, the reference makes %d", name, got, want.Reference)
		}
	}
}
