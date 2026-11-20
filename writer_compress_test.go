package vortex

import (
	"bytes"
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/encode"
)

// flatTypes are the types of columns the compressor takes, nullable or not.
func flatTypes() []Type {
	var out []Type
	for _, t := range []Type{Int8Type(), Int16Type(), Int32Type(), Int64Type(), Uint8Type(), Uint16Type(),
		Uint32Type(), Uint64Type(), Float16Type(), Float32Type(), Float64Type(), BoolType(), Utf8Type(),
		BinaryType(), DecimalOf(9, 2), DecimalOf(38, 4), DateType(Day), TimeType(Microsecond),
		TimestampType(Millisecond, "UTC"), TimestampType(Second, ""), UUIDType()} {
		out = append(out, t, t.AsNullable())
	}
	return out
}

// shapes are the ways the rows of a column go: each one makes the compressor pick another
// encoding.
var shapes = []string{"random", "constant", "sorted", "runs", "few", "small", "sparse"}

// shaped is n rows of type t in a shape: the value of row i, as an integer.
func shaped(r *rand.Rand, shape string, i int) int64 {
	switch shape {
	case "constant":
		return 42
	case "sorted":
		return 1_000_000 + 3*int64(i) + int64(i%2)
	case "runs":
		return int64(i / 300)
	case "few":
		return []int64{5, -7, 1 << 20, 99}[i*7%4]
	case "small":
		return int64(r.IntN(30))
	case "sparse":
		if i%50 == 0 {
			return r.Int64()
		}
		return 0
	}
	return r.Int64()
}

// shapedData is n rows of type t in a shape.
func shapedData(t Type, shape string, n int, seed uint64) Data {
	r := rand.New(rand.NewPCG(seed, uint64(len(shape))))
	var nulls []bool
	if t.nullable {
		nulls = make([]bool, n)
		for i := range nulls {
			nulls[i] = r.IntN(6) == 0
		}
	}
	at := func(i int) int64 { return shaped(r, shape, i) }
	var d Data
	switch t.kind {
	case Bool:
		v := make([]bool, n)
		for i := range v {
			v[i] = at(i)%2 == 0
		}
		d = OfBools(v)
	case Utf8, Binary:
		v := make([]string, n)
		for i := range v {
			v[i] = fmt.Sprintf("value-%d", at(i))
		}
		d = OfStrings(v)
	case UUID:
		v := make([][]byte, n)
		for i := range v {
			v[i] = bytes.Repeat([]byte{byte(at(i))}, 16)
		}
		d = OfBytes(v)
	case KindDecimal:
		v := make([]Decimal, n)
		for i := range v {
			v[i] = DecimalFromInt64(at(i) % 1_000_000_000)
		}
		d = OfDecimals(v)
	default:
		k := storageKind(t)
		raw := make([]byte, n*numberWidth(k))
		for i := range n {
			x := uint64(at(i))
			switch k {
			case Float32:
				x = uint64(math.Float32bits(float32(at(i)) / 4))
			case Float64:
				x = math.Float64bits(float64(at(i)) / 100)
			case Uint16:
				if t.kind == Float16 {
					x &= 0x3bff // no NaN: its bits are not kept
				}
			}
			if t.kind == Time {
				x = uint64(at(i)) % 86_400_000_000 // within the day
				if int64(x) < 0 {
					x = -x
				}
			}
			for b := range numberWidth(k) {
				raw[i*numberWidth(k)+b] = byte(x >> (8 * b))
			}
		}
		d = Data{&body{kind: dPrim, pk: k, n: n, raw: raw}}
	}
	if nulls != nil {
		d = d.Nulls(nulls)
	}
	return d
}

// writeWith writes the columns in the given level and in writes of the given sizes.
func writeWith(t testing.TB, c Compression, s *Schema, cols []Data, sizes ...int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, s, WriteOptions{Compression: c})
	lo := 0
	for _, n := range sizes {
		part := cols // one write of every row: a list, which sliceOf does not cut, can be written
		if len(sizes) > 1 {
			part = make([]Data, len(cols))
			for i, f := range s.Fields() {
				part[i] = sliceOf(cols[i], f.Type, lo, lo+n)
			}
		}
		if err := w.Write(part...); err != nil {
			t.Fatal(err)
		}
		lo += n
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func openBytes(t testing.TB, b []byte) *File {
	t.Helper()
	f, err := Open(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestCompressionLevels writes columns of every flat type and shape in the three levels:
// each reads back as the plain one, and only None has no encoding of the compressor.
func TestCompressionLevels(t *testing.T) {
	lowered(t, &chunkBytes, 20_000) // several chunks
	encodings := map[Compression]map[string]bool{Default: {}, Compact: {}, None: {}}
	for _, ty := range flatTypes() {
		for k, shape := range shapes {
			t.Run(fmt.Sprintf("%s/%s", ty, shape), func(t *testing.T) {
				d := shapedData(ty, shape, 5000, uint64(k))
				s := NewSchema(Field{"c", ty})
				plain, err := readBack(openBytes(t, writeWith(t, None, s, []Data{d}, 5000)), ty)
				if err != nil {
					t.Fatal(err)
				}
				for _, c := range []Compression{Default, Compact, None} {
					f := openBytes(t, writeWith(t, c, s, []Data{d}, 1, 2999, 2000))
					got, err := readBack(f, ty)
					if err != nil || len(got) != len(plain) {
						t.Fatalf("level %d: %d rows, %v", c, len(got), err)
					}
					for i := range got {
						if !eqVal(got[i], plain[i]) {
							t.Fatalf("level %d: row %d is %s, want %s", c, i, describeVal(got[i]), describeVal(plain[i]))
						}
					}
					for _, id := range f.arrayEncodings {
						encodings[c][id] = true
					}
				}
			})
		}
	}
	for _, id := range []string{encode.IDBitPacked, encode.IDFoR, encode.IDDict, encode.IDRunEnd, encode.IDALP,
		encode.IDFSST, encode.IDConstant, encode.IDDecimalBytes, encode.IDDateTimeParts} {
		if !encodings[Default][id] {
			t.Errorf("Default never wrote %s: %v", id, encodings[Default])
		}
	}
	if !encodings[Compact][encode.IDZstd] || encodings[Default][encode.IDZstd] {
		t.Errorf("zstd: in Compact %v, in Default %v", encodings[Compact][encode.IDZstd], encodings[Default][encode.IDZstd])
	}
	for id := range encodings[None] {
		if id != "vortex.primitive" && id != "vortex.bool" && id != "vortex.varbinview" && id != "vortex.decimal" &&
			id != "vortex.ext" && id != "vortex.struct" && id != "vortex.fixed_size_list" && id != "vortex.dict" {
			t.Errorf("None wrote %s", id)
		}
	}
}

// wideColumns is 8 columns of several types, in every shape, or in the one given.
func wideColumns(n int, shape string) (*Schema, []Data) {
	types := []Type{Int64Type(), Utf8Type().AsNullable(), Float64Type(), DecimalOf(12, 2), Int32Type().AsNullable(),
		TimestampType(Microsecond, ""), Uint16Type(), BinaryType()}
	var fields []Field
	var cols []Data
	for i, ty := range types {
		fields = append(fields, Field{Name: fmt.Sprintf("c%d", i), Type: ty})
		sh := shape
		if sh == "" {
			sh = shapes[i%len(shapes)]
		}
		cols = append(cols, shapedData(ty, sh, n, uint64(i)))
	}
	return NewSchema(fields...), cols
}

// TestCompressionIsDeterministic writes the same columns twice, with one thread and with
// eight: the files are the same bytes, whatever the order the columns are compressed in.
func TestCompressionIsDeterministic(t *testing.T) {
	lowered(t, &chunkBytes, 30_000)
	s, cols := wideColumns(20_000, "")
	var files [][]byte
	for _, procs := range []int{1, 8, 1, 8} {
		old := runtime.GOMAXPROCS(procs)
		files = append(files, writeWith(t, Default, s, cols, 7000, 1, 12_999))
		runtime.GOMAXPROCS(old)
	}
	for i, f := range files[1:] {
		if !bytes.Equal(f, files[0]) {
			t.Fatalf("file %d differs from the first", i+1)
		}
	}
	got, err := readBack(openBytes(t, files[0]), s.Fields()[0].Type)
	if err != nil || len(got) != 20_000 {
		t.Fatal(len(got), err)
	}
}

// TestParallelCompression compresses 8 columns in parallel, a hundred times, with chunks that
// fill in every write: the race detector watches the goroutines, the files must not change.
func TestParallelCompression(t *testing.T) {
	lowered(t, &chunkBytes, 4000)
	lowered(t, &rowBlock, 256)
	old := runtime.GOMAXPROCS(8)
	defer runtime.GOMAXPROCS(old)
	s, cols := wideColumns(3000, "")
	first := writeWith(t, Default, s, cols, 1000, 1000, 1000)
	runs := 100
	if testing.Short() && !raceEnabled {
		runs = 20
	}
	for range runs {
		if got := writeWith(t, Default, s, cols, 1000, 1000, 1000); !bytes.Equal(got, first) {
			t.Fatal("a file differs")
		}
	}
}

// TestNoAllocationPerCompressedWrite writes a chunk per Write: once the writer has seen them,
// the compressor allocates nothing: the writer allocates what it does without it (the
// statistics of a chunk).
func TestNoAllocationPerCompressedWrite(t *testing.T) {
	lowered(t, &rowBlock, 1024)
	lowered(t, &chunkBytes, 1)
	old := runtime.GOMAXPROCS(1) // a goroutine costs an allocation
	defer runtime.GOMAXPROCS(old)
	for _, wide := range []bool{false, true} {
		s, cols := wideColumns(2048, "sorted") // no dictionary layout, whatever the level
		if !wide {
			s, cols = NewSchema(s.Fields()[1]), cols[1:2]
		}
		fields := s.Fields()
		var parts [2][]Data
		for k := range parts {
			for i, f := range fields {
				parts[k] = append(parts[k], sliceOf(cols[i], f.Type, 1024*k, 1024*(k+1)))
			}
		}
		allocs := map[Compression]float64{}
		for _, c := range []Compression{None, Compact, Default} {
			w := NewWriter(discard{}, s, WriteOptions{Compression: c})
			write := func() {
				for _, p := range parts {
					if err := w.Write(p...); err != nil {
						t.Fatal(err)
					}
				}
			}
			for range 3 {
				write()
			}
			allocs[c] = testing.AllocsPerRun(20, write)
		}
		if allocs[Compact] > allocs[None] || allocs[Default] > allocs[None] {
			t.Errorf("%d columns: %v allocations per Write", len(cols), allocs)
		}
	}
}

// discard is an io.Writer that keeps nothing.
type discard struct{}

func (discard) Write(b []byte) (int, error) { return len(b), nil }

// TestDictionaryProbe: the dictionary layout is taken when the compressor would pick a
// dictionary for the first block, not for a column it compresses better another way.
func TestDictionaryProbe(t *testing.T) {
	long := []string{"a value longer than a view can hold, the first", "the second of the few values of the column"}
	few := make([]string, 3000)
	for i := range few {
		few[i] = long[i*7%3%2]
	}
	sorted := make([]int32, 3000)
	for i := range sorted {
		sorted[i] = int32(i / 2) // a value for two rows: under the 50% rule, a dictionary
	}
	f, _ := writeFile(t, NewSchema(Field{"few", Utf8Type()}, Field{"sorted", Int32Type()}), []Data{OfStrings(few), Of(sorted)})
	if got := layoutTree(f, f.layout.children[0]); !strings.Contains(got, "dict(3000)") {
		t.Errorf("few strings: %s", got)
	}
	if got := layoutTree(f, f.layout.children[1]); strings.Contains(got, "dict") {
		t.Errorf("sorted integers: %s", got)
	}
	if !slices.Contains(f.arrayEncodings, encode.IDBitPacked) {
		t.Errorf("the codes are not bit-packed: %v", f.arrayEncodings)
	}
}

// TestNestedChildrenCompressed: the elements, the offsets and the sizes of a list are
// compressed as the reference does, in a file of one column and in one of two (whose chunks
// wait to be compressed together).
func TestNestedChildrenCompressed(t *testing.T) {
	offsets := make([]int, 1001)
	elems := make([]int32, 3000)
	for i := range offsets {
		offsets[i] = 3 * i
	}
	for i := range elems {
		elems[i] = int32(i % 5)
	}
	list := OfList(offsets, Of(elems))
	ty := ListOf(Int32Type())
	for _, s := range []*Schema{NewSchema(Field{"l", ty}), NewSchema(Field{"l", ty}, Field{"n", Int8Type()})} {
		cols := []Data{list, Of(make([]int8, 1000))}[:len(s.Fields())]
		f := openBytes(t, writeWith(t, Default, s, cols, 1000))
		for _, id := range []string{encode.IDBitPacked, encode.IDSequence, encode.IDConstant} {
			if !slices.Contains(f.arrayEncodings, id) {
				t.Errorf("%d columns: no %s in %v", len(cols), id, f.arrayEncodings)
			}
		}
		got, err := readBack(f, ty)
		if err != nil || len(got) != 1000 || len(got[999].kids) != 3 {
			t.Fatalf("%d rows, %v", len(got), err)
		}
		if f := openBytes(t, writeWith(t, None, s, cols, 1000)); slices.Contains(f.arrayEncodings, encode.IDBitPacked) {
			t.Errorf("None: %v", f.arrayEncodings)
		}
	}
}
