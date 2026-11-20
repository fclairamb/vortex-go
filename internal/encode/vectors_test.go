package encode

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// The vectors of testdata/vectors/<encoding>/<type>.json: what the encoders of the reference
// make of an input, written by tools/vectors. Ours must make the same bytes, and our decoder
// must give the input back.

type vecFile struct {
	Encoding string   `json:"encoding"`
	PType    string   `json:"ptype"`
	Vectors  []vector `json:"vectors"`
}

type vector struct {
	Output  *vnode   `json:"output"`
	Params  vparams  `json:"params"`
	Valid   *string  `json:"valid"`
	Name    string   `json:"name"`
	Input   string   `json:"input"`
	Strings []string `json:"strings"`
	Len     int      `json:"len"`
}

type vparams struct {
	AlpE      *int     `json:"alp_e"`
	AlpF      *int     `json:"alp_f"`
	Dict      []uint16 `json:"dict"`
	BitWidth  int      `json:"bit_width"`
	RightBits int      `json:"right_bits"`
	Best      bool     `json:"best"`
}

type vnode struct {
	ID       string   `json:"id"`
	Metadata string   `json:"metadata"`
	Buffers  []string `json:"buffers"`
	Children []*vnode `json:"children"`
}

// run makes the node of a vector of raw numbers. ok is false when the encoder declines.
type run func(e *Encoder, raw []byte, nulls []bool, p vparams) (n *Node, ok bool)

func intRuns[T Int]() map[string]run {
	return map[string]run{
		"bitpacked": func(e *Encoder, raw []byte, nulls []bool, p vparams) (*Node, bool) {
			var n *Node
			var err error
			if p.Best {
				n, err = BitPackBest(e, unsafex.Slice[T](raw), nulls)
			} else {
				n, err = BitPack(e, unsafex.Slice[T](raw), nulls, p.BitWidth)
			}
			return n, err == nil
		},
		"for": func(e *Encoder, raw []byte, nulls []bool, _ vparams) (*Node, bool) {
			return FoR(e, unsafex.Slice[T](raw), nulls), true
		},
		"sequence": func(e *Encoder, raw []byte, nulls []bool, _ vparams) (*Node, bool) {
			return Sequence(e, unsafex.Slice[T](raw), nulls)
		},
	}
}

func numberRuns[T Number]() map[string]run {
	m := map[string]run{
		"rle": func(e *Encoder, raw []byte, nulls []bool, _ vparams) (*Node, bool) {
			return RLE(e, unsafex.Slice[T](raw), nulls), true
		},
		"runend": func(e *Encoder, raw []byte, nulls []bool, _ vparams) (*Node, bool) {
			return RunEnd(e, unsafex.Slice[T](raw), nulls), true
		},
		"dict": func(e *Encoder, raw []byte, nulls []bool, _ vparams) (*Node, bool) {
			return Dict(e, unsafex.Slice[T](raw), nulls), true
		},
		"sparse": func(e *Encoder, raw []byte, nulls []bool, _ vparams) (*Node, bool) {
			return Sparse(e, unsafex.Slice[T](raw), nulls)
		},
		"constant": func(e *Encoder, raw []byte, nulls []bool, _ vparams) (*Node, bool) {
			return Constant(e, unsafex.Slice[T](raw), nulls)
		},
		"zstd": func(e *Encoder, raw []byte, nulls []bool, _ vparams) (*Node, bool) {
			return Zstd(e, unsafex.Slice[T](raw), nulls), true
		},
	}
	// Pco does not code 8-bit numbers: the encoder declines them
	m["pco"] = func(e *Encoder, raw []byte, nulls []bool, _ vparams) (*Node, bool) {
		n := Pco(e, unsafex.Slice[T](raw), nulls)
		return n, n != nil
	}
	return m
}

func signedRuns[T Signed]() map[string]run {
	return map[string]run{
		"zigzag": func(e *Encoder, raw []byte, nulls []bool, _ vparams) (*Node, bool) {
			return ZigZag(e, unsafex.Slice[T](raw), nulls), true
		},
	}
}

func floatRuns[T Float]() map[string]run {
	return map[string]run{
		"alp": func(e *Encoder, raw []byte, nulls []bool, p vparams) (*Node, bool) {
			v := unsafex.Slice[T](raw)
			if p.AlpE == nil {
				return ALP(e, v, nulls), true
			}
			n := ALPWith(e, v, nulls, Exponents{uint8(*p.AlpE), uint8(*p.AlpF)})
			return n, n != nil
		},
		"alprd": func(e *Encoder, raw []byte, nulls []bool, p vparams) (*Node, bool) {
			v := unsafex.Slice[T](raw)
			if p.Dict == nil {
				return ALPRD(e, v, nulls)
			}
			return ALPRDWith(e, v, nulls, p.RightBits, p.Dict)
		},
	}
}

func merge(ms ...map[string]run) map[string]run {
	out := map[string]run{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// runs is, per type, the encoders that take it.
var runs = map[string]map[string]run{
	"i8":  merge(intRuns[int8](), numberRuns[int8](), signedRuns[int8]()),
	"i16": merge(intRuns[int16](), numberRuns[int16](), signedRuns[int16]()),
	"i32": merge(intRuns[int32](), numberRuns[int32](), signedRuns[int32]()),
	"i64": merge(intRuns[int64](), numberRuns[int64](), signedRuns[int64]()),
	"u8":  merge(intRuns[uint8](), numberRuns[uint8]()),
	"u16": merge(intRuns[uint16](), numberRuns[uint16]()),
	"u32": merge(intRuns[uint32](), numberRuns[uint32]()),
	"u64": merge(intRuns[uint64](), numberRuns[uint64]()),
	"f32": merge(numberRuns[float32](), floatRuns[float32]()),
	"f64": merge(numberRuns[float64](), floatRuns[float64]()),
}

var ptypes = map[string]array.PType{
	"i8": array.I8, "i16": array.I16, "i32": array.I32, "i64": array.I64,
	"u8": array.U8, "u16": array.U16, "u32": array.U32, "u64": array.U64,
	"f32": array.F32, "f64": array.F64,
}

func unhex(tb testing.TB, s string) []byte {
	tb.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// nullsOf is the null rows of a vector: its bitmap says which rows are valid.
func nullsOf(tb testing.TB, v vector) []bool {
	if v.Valid == nil {
		return nil
	}
	bits := unhex(tb, *v.Valid)
	nulls := make([]bool, v.Len)
	for i := range nulls {
		nulls[i] = bits[i/8]>>(i%8)&1 == 0
	}
	return nulls
}

// diff names the first difference between the node we made and the one of the reference.
func diff(path string, got *Node, want *vnode) string {
	if got.ID != want.ID {
		return fmt.Sprintf("%s: id %s, want %s", path, got.ID, want.ID)
	}
	path += "/" + got.ID
	if hex.EncodeToString(got.Meta) != want.Metadata {
		return fmt.Sprintf("%s: metadata %x, want %s", path, got.Meta, want.Metadata)
	}
	if len(got.Bufs) != len(want.Buffers) || len(got.Kids) != len(want.Children) {
		return fmt.Sprintf("%s: %d buffers and %d children, want %d and %d", path,
			len(got.Bufs), len(got.Kids), len(want.Buffers), len(want.Children))
	}
	for i, b := range got.Bufs {
		if w, _ := hex.DecodeString(want.Buffers[i]); !bytes.Equal(b.Data, w) {
			return fmt.Sprintf("%s: buffer %d is %d bytes, want %d bytes, different", path, i, len(b.Data), len(w))
		}
	}
	for i, k := range got.Kids {
		if d := diff(fmt.Sprintf("%s[%d]", path, i), k, want.Children[i]); d != "" {
			return d
		}
	}
	return ""
}

func forEachVector(t *testing.T, f func(t *testing.T, e *Encoder, file vecFile, v vector)) int {
	t.Helper()
	paths, err := filepath.Glob("../../testdata/vectors/*/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no vector files: %v", err)
	}
	e := New()
	count := 0
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var file vecFile
		if json.Unmarshal(raw, &file) != nil || file.Encoding == "" {
			continue // the ALP and Pco vectors have their own tests
		}
		for _, v := range file.Vectors {
			count++
			t.Run(file.Encoding+"/"+file.PType+"/"+v.Name, func(t *testing.T) {
				e.Reset()
				f(t, e, file, v)
			})
		}
	}
	return count
}

// TestVectorsBytes checks every encoder against what the reference makes of the same input.
func TestVectorsBytes(t *testing.T) {
	n := forEachVector(t, func(t *testing.T, e *Encoder, file vecFile, v vector) {
		if file.Encoding == "dict" && v.Strings != nil {
			vectorStrings(t, e, v)
			return
		}
		if file.Encoding == "fsst" {
			vectorFSST(t, e, v)
			return
		}
		if file.Encoding == "onpair" {
			vectorOnPair(t, e, v)
			return
		}
		got, ok := runs[file.PType][file.Encoding](e, unhex(t, v.Input), nullsOf(t, v), v.Params)
		if v.Output == nil {
			if ok {
				t.Fatalf("encoded, the reference declines")
			}
			return
		}
		if !ok {
			t.Fatalf("declined, the reference encodes")
		}
		if d := diff("", got, v.Output); d != "" {
			t.Fatal(d)
		}
	})
	if n < 1000 {
		t.Fatalf("only %d vectors", n)
	}
}

func vectorStrings(t *testing.T, e *Encoder, v vector) {
	vals := make([][]byte, len(v.Strings))
	strs := make([]string, len(v.Strings))
	for i, s := range v.Strings {
		vals[i] = unhex(t, s)
		strs[i] = string(vals[i])
	}
	got := DictBytes(e, vals, nullsOf(t, v))
	if d := diff("", got, v.Output); d != "" {
		t.Fatal("bytes: " + d)
	}
	e.Reset()
	got = DictStrings(e, strs, nullsOf(t, v))
	if d := diff("", got, v.Output); d != "" {
		t.Fatal("strings: " + d)
	}
}

// TestVectorsDecode gives the input of every vector back through our decoder, from the node
// of the reference as well as from ours.
func TestVectorsDecode(t *testing.T) {
	forEachVector(t, func(t *testing.T, e *Encoder, file vecFile, v vector) {
		if v.Output == nil || v.Strings != nil {
			return
		}
		raw, nulls := unhex(t, v.Input), nullsOf(t, v)
		got, _ := runs[file.PType][file.Encoding](e, raw, nulls, v.Params)
		checkDecode(t, got, ptypes[file.PType], raw, nulls)
	})
}

// checkDecode decodes the tree as an array of numbers of type p and compares the valid rows
// with raw.
func checkDecode(t *testing.T, n *Node, p array.PType, raw []byte, nulls []bool) {
	t.Helper()
	w := p.Width()
	rows := len(raw) / w
	a := decode(t, n, array.DType{Kind: array.Prim, PType: p, Nullable: hasNull(nulls)}, rows)
	for i := range rows {
		if isNull(nulls, i) {
			if !a.Null(i) {
				t.Fatalf("row %d is valid, want null", i)
			}
			continue
		}
		if a.Null(i) || !bytes.Equal(a.Data[i*w:(i+1)*w], raw[i*w:(i+1)*w]) {
			t.Fatalf("row %d = %x, want %x", i, a.Data[i*w:(i+1)*w], raw[i*w:(i+1)*w])
		}
	}
}
