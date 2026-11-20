package pco

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// vector is a file of testdata/vectors/pco, written by tools/oracle/pcovectors.py.
type vector struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Mode   string `json:"mode"`
	Delta  string `json:"delta"`
	Header string `json:"header"`
	Meta   string `json:"meta"`
	Pages  []struct {
		N    int    `json:"n"`
		Data string `json:"data"`
	} `json:"pages"`
	Values string `json:"values"`
}

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func typeOf(t testing.TB, name string) Type {
	t.Helper()
	for i, n := range typeNames {
		if n == name {
			return Type(i)
		}
	}
	t.Fatalf("unknown type %q", name)
	return 0
}

func loadVectors(t testing.TB) []vector {
	t.Helper()
	files, err := filepath.Glob("../../testdata/vectors/pco/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no vectors: %v", err)
	}
	var out []vector
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var v vector
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out = append(out, v)
	}
	return out
}

// decodeVector decodes every page of v, given its metadata and pages.
func decodeVector(t testing.TB, v vector, meta []byte, pages [][]byte) ([]byte, error) {
	t.Helper()
	typ := typeOf(t, v.Type)
	h, _, err := ReadHeader(unhex(t, v.Header))
	if err != nil {
		t.Fatal(err)
	}
	c, err := h.Chunk(typ, meta)
	if err != nil {
		return nil, err
	}
	var out []byte
	for i, p := range v.Pages {
		dst := make([]byte, p.N*typ.Bits()/8)
		if err := c.Page(pages[i], p.N, dst); err != nil {
			return nil, err
		}
		out = append(out, dst...)
	}
	return out, nil
}

func (v vector) pages(t testing.TB) [][]byte {
	var out [][]byte
	for _, p := range v.Pages {
		out = append(out, unhex(t, p.Data))
	}
	return out
}

func TestVectors(t *testing.T) {
	modes, deltas := map[string]bool{}, map[string]bool{}
	types := map[string]bool{}
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			got, err := decodeVector(t, v, unhex(t, v.Meta), v.pages(t))
			if err != nil {
				t.Fatal(err)
			}
			if want := unhex(t, v.Values); !bytes.Equal(got, want) {
				t.Fatalf("decoded %d bytes, differing from the %d the reference holds", len(got), len(want))
			}
			// The metadata says what the oracle recorded.
			h, _, _ := ReadHeader(unhex(t, v.Header))
			c, _ := h.Chunk(typeOf(t, v.Type), unhex(t, v.Meta))
			if modeNames[c.mode] != v.Mode || deltaNames[c.delta.kind] != v.Delta {
				t.Fatalf("chunk is %s/%s, the oracle says %s/%s", modeNames[c.mode], deltaNames[c.delta.kind], v.Mode, v.Delta)
			}
		})
		modes[v.Mode], deltas[v.Delta], types[v.Type] = true, true, true
	}
	for _, m := range modeNames {
		if !modes[m] {
			t.Errorf("no vector reaches the %s mode", m)
		}
	}
	for _, d := range deltaNames {
		if !deltas[d] {
			t.Errorf("no vector reaches the %s delta encoding", d)
		}
	}
	for _, n := range typeNames {
		if !types[n] {
			t.Errorf("no vector of type %s", n)
		}
	}
}

var (
	modeNames  = [...]string{"classic", "int_mult", "float_mult", "float_quant", "dict"}
	deltaNames = [...]string{"none", "consecutive", "lookback", "conv1"}
)

// offsets returns up to 20 distinct offsets in [0, n), the first and the last included.
func offsets(n int) []int {
	if n == 0 {
		return nil
	}
	var out []int
	seen := map[int]bool{}
	for i := range 20 {
		o := i * n / 20
		if i == 19 {
			o = n - 1
		}
		if !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out
}

// Every vector, cut short at 20 offsets, in its metadata or in any of its pages, is an error:
// never a panic and never a silent success.
func TestVectorsTruncated(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			meta, pages := unhex(t, v.Meta), v.pages(t)
			for _, o := range offsets(len(meta)) {
				if _, err := decodeVector(t, v, meta[:o], pages); !errors.Is(err, ErrCorrupt) {
					t.Fatalf("metadata cut at %d of %d: err = %v", o, len(meta), err)
				}
			}
			for i := range pages {
				for _, o := range offsets(len(pages[i])) {
					cut := append([][]byte(nil), pages...)
					cut[i] = pages[i][:o]
					if _, err := decodeVector(t, v, meta, cut); !errors.Is(err, ErrCorrupt) {
						t.Fatalf("page %d cut at %d of %d: err = %v", i, o, len(pages[i]), err)
					}
				}
			}
		})
	}
}
