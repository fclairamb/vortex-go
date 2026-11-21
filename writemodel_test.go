package vortex

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
	"strings"

	"github.com/fclairamb/vortex-go/internal/testutil"
)

// val is one row of a generated column, in the form the canonical hash and the read-back
// compare: b is the value bytes (a number as is, a bool as one byte, a string as its bytes,
// a decimal as its 32 bytes), kids the fields of a struct and the elements of a list, a
// fixed-size list and a map (whose elements are structs of key and value).
type val struct {
	b    []byte
	kids []val
	null bool
}

func eqVal(a, b val) bool {
	if a.null != b.null {
		return false
	}
	if a.null {
		return true
	}
	if !bytes.Equal(a.b, b.b) || len(a.kids) != len(b.kids) {
		return false
	}
	for i := range a.kids {
		if !eqVal(a.kids[i], b.kids[i]) {
			return false
		}
	}
	return true
}

// gen makes n rows of type t: the Data to write, and the rows it stands for. Nullable types
// get about a quarter of null rows, all of them when allNull is set.
type gen struct {
	r       *rand.Rand
	allNull bool
	bigStr  bool // an occasional string of a megabyte
	inDay   bool // times within their day (the reference reader rejects the others in statistics)
}

func (g *gen) nulls(t Type, n int) []bool {
	if !t.nullable || t.kind == Null {
		return nil
	}
	out := make([]bool, n)
	for i := range out {
		out[i] = g.allNull || g.r.IntN(4) == 0
	}
	return out
}

var edgeInts = []uint64{0, 1, 0x7f, 0x80, 0xff, 0x7fff, 0x8000, 0xffff, 0x7fffffff, 0x80000000, 0xffffffff,
	0x7fffffffffffffff, 0x8000000000000000, 0xffffffffffffffff}

func (g *gen) bits() uint64 {
	if g.r.IntN(3) == 0 {
		return edgeInts[g.r.IntN(len(edgeInts))]
	}
	return g.r.Uint64()
}

func genNumbers[T Number](g *gen, n int, conv func(uint64) T) ([]T, [][]byte) {
	vals := make([]T, n)
	raws := make([][]byte, n)
	for i := range vals {
		vals[i] = conv(g.bits())
		raws[i] = rawOf(vals[i])
	}
	return vals, raws
}

func rawOf[T Number](v T) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, v)
	return b.Bytes()
}

func (g *gen) float64(b uint64) float64 {
	switch g.r.IntN(8) {
	case 0:
		return math.NaN()
	case 1:
		return math.Copysign(0, -1)
	case 2:
		return math.Inf(1 - 2*g.r.IntN(2))
	case 3:
		return float64(int64(b))
	}
	return math.Float64frombits(b)
}

// numbers makes the Data of a column of the numbers of kind k.
func (g *gen) numbers(k Kind, n int) (Data, [][]byte) {
	switch k {
	case Int8:
		v, r := genNumbers(g, n, func(b uint64) int8 { return int8(b) })
		return Of(v), r
	case Int16:
		v, r := genNumbers(g, n, func(b uint64) int16 { return int16(b) })
		return Of(v), r
	case Int32:
		v, r := genNumbers(g, n, func(b uint64) int32 { return int32(b) })
		return Of(v), r
	case Int64:
		v, r := genNumbers(g, n, func(b uint64) int64 { return int64(b) })
		return Of(v), r
	case Uint8:
		v, r := genNumbers(g, n, func(b uint64) uint8 { return uint8(b) })
		return Of(v), r
	case Uint16:
		v, r := genNumbers(g, n, func(b uint64) uint16 { return uint16(b) })
		return Of(v), r
	case Uint32:
		v, r := genNumbers(g, n, func(b uint64) uint32 { return uint32(b) })
		return Of(v), r
	case Uint64:
		v, r := genNumbers(g, n, func(b uint64) uint64 { return b })
		return Of(v), r
	case Float32:
		v, r := genNumbers(g, n, func(b uint64) float32 { return float32(g.float64(b)) })
		return Of(v), r
	default:
		v, r := genNumbers(g, n, func(b uint64) float64 { return g.float64(b) })
		return Of(v), r
	}
}

func (g *gen) str(binary bool) []byte {
	n := g.r.IntN(40)
	switch {
	case g.r.IntN(8) == 0:
		n = 0
	case g.bigStr && g.r.IntN(50) == 0:
		n = 1 << 20
	}
	out := make([]byte, 0, n)
	for len(out) < n {
		if binary {
			out = append(out, byte(g.r.IntN(256)))
		} else {
			out = append(out, string(rune(0x20+g.r.IntN(0x2000)))...)
		}
	}
	if !binary && len(out) > n && n > 0 {
		out = []byte(strings.ToValidUTF8(string(out), "?"))
	}
	return out
}

func (g *gen) decimal(t Type) (Decimal, []byte) {
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(t.precision)), nil)
	raw := make([]byte, 40)
	for i := range raw {
		raw[i] = byte(g.r.IntN(256))
	}
	v := new(big.Int).Mod(new(big.Int).SetBytes(raw), limit)
	if g.r.IntN(2) == 0 {
		v.Neg(v)
	}
	if g.r.IntN(6) == 0 {
		v.Sub(limit, big.NewInt(1))
	}
	d, err := DecimalFromBig(v)
	if err != nil {
		panic(err)
	}
	le := d.le()
	return d, le[:]
}

// make returns the Data and the rows of n rows of type t.
func (g *gen) make(t Type, n int) (Data, []val) {
	nulls := g.nulls(t, n)
	d, vals := g.values(t, n)
	for i := range vals {
		if nulls != nil && nulls[i] {
			vals[i] = val{null: true}
		}
	}
	if t.kind == Null {
		return d, vals
	}
	if nulls != nil {
		d = d.Nulls(nulls)
	}
	return d, vals
}

func (g *gen) values(t Type, n int) (Data, []val) {
	vals := make([]val, n)
	switch t.kind {
	case Null:
		for i := range vals {
			vals[i].null = true
		}
		return OfNulls(n), vals
	case Bool:
		v := make([]bool, n)
		for i := range v {
			v[i] = g.r.IntN(2) == 0
			vals[i].b = []byte{0}
			if v[i] {
				vals[i].b[0] = 1
			}
		}
		return OfBools(v), vals
	case Utf8, Binary:
		strs, bins := make([]string, n), make([][]byte, n)
		for i := range vals {
			vals[i].b = g.str(t.kind == Binary)
			strs[i], bins[i] = string(vals[i].b), vals[i].b
		}
		if g.r.IntN(2) == 0 {
			return OfStrings(strs), vals
		}
		return OfBytes(bins), vals
	case UUID:
		bins := make([][]byte, n)
		for i := range vals {
			bins[i] = make([]byte, 16)
			for j := range bins[i] {
				bins[i][j] = byte(g.r.IntN(256))
			}
			vals[i].b = bins[i]
		}
		return OfBytes(bins), vals
	case KindDecimal:
		decs := make([]Decimal, n)
		for i := range vals {
			decs[i], vals[i].b = g.decimal(t)
		}
		return OfDecimals(decs), vals
	case Struct:
		kids := make([]Data, len(t.fields))
		kv := make([][]val, len(t.fields))
		for i, f := range t.fields {
			kids[i], kv[i] = g.make(f.Type, n)
		}
		for i := range vals {
			for j := range kids {
				vals[i].kids = append(vals[i].kids, kv[j][i])
			}
		}
		return OfStruct(kids...), vals
	case List, FixedSizeList, Map:
		return g.lists(t, n)
	case Time:
		if g.inDay {
			return g.timesInDay(t, n)
		}
		fallthrough
	case Date, Timestamp:
		if g.inDay && t.kind != Time {
			return g.nearEpoch(t, n)
		}
		fallthrough
	default:
		d, raws := g.numbers(storageKind(t), n)
		for i := range vals {
			vals[i].b = raws[i]
		}
		return d, vals
	}
}

func (g *gen) lists(t Type, n int) (Data, []val) {
	offs := make([]int, n+1)
	for i := range n {
		size := g.r.IntN(5)
		if t.kind == FixedSizeList {
			size = t.Size()
		}
		offs[i+1] = offs[i] + size
	}
	total := offs[n]
	var elems Data
	var ev []val
	if t.kind == Map {
		k, kv := g.make(*t.elem, total)
		v, vv := g.make(*t.value, total)
		elems = OfStruct(k, v)
		for i := range total {
			ev = append(ev, val{kids: []val{kv[i], vv[i]}})
		}
	} else {
		elems, ev = g.make(*t.elem, total)
	}
	vals := make([]val, n)
	for i := range vals {
		vals[i].kids = ev[offs[i]:offs[i+1]]
	}
	return OfList(offs, elems), vals
}

// readRow reads row i of a column into a val.
func readRow(c *Column, t Type, i int) val {
	if c.Null(i) {
		return val{null: true}
	}
	switch t.kind {
	case Bool:
		if c.Bool(i) {
			return val{b: []byte{1}}
		}
		return val{b: []byte{0}}
	case Utf8, Binary, UUID:
		return val{b: bytes.Clone(c.Bytes(i))}
	case KindDecimal:
		le := c.Decimal(i).le()
		return val{b: le[:]}
	case Struct:
		v := val{}
		for k, f := range t.fields {
			v.kids = append(v.kids, readRow(c.Field(k), f.Type, i))
		}
		return v
	case List, FixedSizeList, Map:
		start, end := c.List(i)
		v := val{}
		et := entryType(t)
		for j := start; j < end; j++ {
			v.kids = append(v.kids, readRow(c.Elems(), et, j))
		}
		return v
	default:
		w := c.arr.Type.PType.Width()
		return val{b: bytes.Clone(c.arr.Data[i*w : (i+1)*w])}
	}
}

// entryType is the type of the elements of a list, a fixed-size list or a map.
func entryType(t Type) Type {
	if t.kind == Map {
		return StructOf(Field{"key", *t.elem}, Field{"value", *t.value})
	}
	return *t.elem
}

// readBack reads the first column of a file back into rows.
func readBack(f *File, t Type) ([]val, error) {
	var out []val
	for b, err := range f.Scan(context.Background(), Query{}) {
		if err != nil {
			return nil, err
		}
		for i := range b.Len() {
			out = append(out, readRow(b.Col(0), t, i))
		}
	}
	return out, nil
}

// hashVal adds the row to the canonical hash of docs/testing.md.
func hashVal(h *testutil.Hasher, t Type, v val) {
	if v.null {
		h.Null()
		return
	}
	h.Valid()
	switch t.kind {
	case Utf8, Binary:
		h.Len(len(v.b))
		h.Raw(v.b)
	case KindDecimal:
		h.Raw(v.b[:decimalHashBytes(t.Precision())])
	case Struct:
		for i, f := range t.fields {
			hashVal(h, f.Type, v.kids[i])
		}
	case List, FixedSizeList, Map:
		if t.kind != FixedSizeList {
			h.Len(len(v.kids))
		}
		for _, k := range v.kids {
			hashVal(h, entryType(t), k)
		}
	default:
		h.Raw(v.b)
	}
}

// hashes is the expected value of the oracle for the column and, for a struct, its fields.
type hashEntry struct {
	Nulls  int    `json:"nulls"`
	SHA256 string `json:"sha256"`
}

func hashColumn(out map[string]hashEntry, path string, t Type, vals []val) {
	h := testutil.NewHasher()
	nulls := 0
	for _, v := range vals {
		hashVal(h, t, v)
		if v.null {
			nulls++
		}
	}
	out[path] = hashEntry{Nulls: nulls, SHA256: h.Sum()}
	if t.kind != Struct {
		return
	}
	for i, f := range t.fields {
		kids := make([]val, len(vals))
		for r, v := range vals {
			kids[r] = val{null: true}
			if !v.null {
				kids[r] = v.kids[i]
			}
		}
		hashColumn(out, path+"."+f.Name, f.Type, kids)
	}
}

func describeVal(v val) string { return fmt.Sprintf("%+v", v) }

// timesInDay makes n times of the type, all within [0, 1 day).
// nearEpoch is dates and timestamps within a century of the epoch: the readers before
// core 2026.08.0 fail on the statistics of the others.
func (g *gen) nearEpoch(t Type, n int) (Data, []val) {
	limit := int64(36500) // days
	if t.kind == Timestamp || t.unit != Day {
		limit *= 86400 * map[Unit]int64{Second: 1, Millisecond: 1e3, Microsecond: 1e6, Nanosecond: 1e9}[t.unit]
	}
	vals := make([]val, n)
	if storageKind(t) == Int32 {
		v := make([]int32, n)
		for i := range v {
			v[i] = int32(g.r.Int64N(2*limit) - limit)
			vals[i].b = binary.LittleEndian.AppendUint32(nil, uint32(v[i]))
		}
		return Of(v), vals
	}
	v := make([]int64, n)
	for i := range v {
		v[i] = g.r.Int64N(2*limit) - limit
		vals[i].b = binary.LittleEndian.AppendUint64(nil, uint64(v[i]))
	}
	return Of(v), vals
}

func (g *gen) timesInDay(t Type, n int) (Data, []val) {
	day := int64(86400) * map[Unit]int64{Second: 1, Millisecond: 1e3, Microsecond: 1e6, Nanosecond: 1e9}[t.unit]
	vals := make([]val, n)
	if storageKind(t) == Int32 {
		v := make([]int32, n)
		for i := range v {
			v[i] = int32(g.r.Int64N(day))
			vals[i].b = binary.LittleEndian.AppendUint32(nil, uint32(v[i]))
		}
		return Of(v), vals
	}
	v := make([]int64, n)
	for i := range v {
		v[i] = g.r.Int64N(day)
		vals[i].b = binary.LittleEndian.AppendUint64(nil, uint64(v[i]))
	}
	return Of(v), vals
}
