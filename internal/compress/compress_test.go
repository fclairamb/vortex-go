package compress

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// tree is the encodings of a node and of its children, as "id(kid kid)".
func tree(n *encode.Node) string {
	if n == nil {
		return "plain"
	}
	if len(n.Kids) == 0 {
		return n.ID
	}
	kids := make([]string, len(n.Kids))
	for i, k := range n.Kids {
		kids[i] = tree(k)
	}
	return n.ID + "(" + strings.Join(kids, " ") + ")"
}

// dump is every byte of a tree, to compare two of them.
func dump(n *encode.Node) []byte {
	if n == nil {
		return nil
	}
	out := append([]byte(n.ID), n.Meta...)
	for _, b := range n.Bufs {
		out = append(out, byte(b.Align))
		out = append(out, b.Data...)
	}
	for _, k := range n.Kids {
		out = append(out, dump(k)...)
	}
	return out
}

func ints64(n int, f func(i int) int64) []byte {
	v := make([]int64, n)
	for i := range v {
		v[i] = f(i)
	}
	return unsafex.Bytes(v)
}

func TestChoice(t *testing.T) {
	c := New(Default)
	r := rand.New(rand.NewPCG(1, 2))
	prices := make([]float64, 5000)
	for i := range prices {
		prices[i] = math.Round(r.Float64()*100_000) / 100
	}
	few := make([][]byte, 5000)
	for i := range few {
		few[i] = fmt.Appendf(nil, "group-%d", i%10)
	}
	tests := []struct {
		name string
		node func() *encode.Node
		want []string // the trees it may be
	}{
		{"sorted integers", func() *encode.Node {
			return c.Numbers(I64, ints64(5000, func(i int) int64 { return 1_000_000 + 3*int64(i) + int64(i%2) }), nil)
		}, []string{"fastlanes.for(fastlanes.bitpacked)"}},
		{"few strings", func() *encode.Node { return c.Strings(few, nil, true) }, []string{"vortex.dict"}},
		{"prices", func() *encode.Node { return c.Numbers(F64, unsafex.Bytes(prices), nil) }, []string{"vortex.alp"}},
		{"constant", func() *encode.Node { return c.Numbers(I64, ints64(5000, func(int) int64 { return 7 }), nil) },
			[]string{"vortex.constant"}},
		{"runs", func() *encode.Node {
			return c.Numbers(I64, ints64(5000, func(i int) int64 { return int64(i / 100 * 7919) }), nil)
		},
			[]string{"vortex.runend", "fastlanes.rle"}},
		{"small negative integers", func() *encode.Node {
			return c.Numbers(I32, unsafex.Bytes(randomInts[int32](r, 5000, -50, 50)), nil)
		}, []string{"vortex.zigzag", "fastlanes.for"}},
	}
	for _, tc := range tests {
		c.Reset()
		got := tree(tc.node())
		if !slices.ContainsFunc(tc.want, func(w string) bool { return strings.HasPrefix(got, w) }) {
			t.Errorf("%s: %s, want %v", tc.name, got, tc.want)
		}
	}
}

func randomInts[T encode.Int](r *rand.Rand, n int, lo, hi int64) []T {
	v := make([]T, n)
	for i := range v {
		v[i] = T(lo + r.Int64N(hi-lo+1))
	}
	return v
}

// columns are arrays of every type of numbers, shaped in the ways the schemes take: a scheme
// that is not picked for one of them is for another.
func columns(r *rand.Rand, p PType, n int) map[string][]byte {
	w := p.Width()
	bitsOf := func(f func(i int) uint64) []byte {
		out := make([]byte, n*w)
		for i := range n {
			x := f(i)
			for k := range w {
				out[i*w+k] = byte(x >> (8 * k))
			}
		}
		return out
	}
	floatsOf := func(f func(i int) float64) []byte {
		if p == F32 {
			return bitsOf(func(i int) uint64 { return uint64(math.Float32bits(float32(f(i)))) })
		}
		return bitsOf(func(i int) uint64 { return math.Float64bits(f(i)) })
	}
	if p == F32 || p == F64 {
		return map[string][]byte{
			"random":   bitsOf(func(int) uint64 { return r.Uint64() & 0x3fefffffffffffff }),
			"decimals": floatsOf(func(int) float64 { return math.Round(r.Float64()*1e4) / 100 }),
			"runs":     floatsOf(func(i int) float64 { return float64(i/50) * 1.5 }),
			"few":      floatsOf(func(i int) float64 { return []float64{0.1, 2.75, math.Pi}[i*7%3] }),
			"smooth":   floatsOf(func(i int) float64 { return math.Sin(float64(i)/100) * 1e6 }),
		}
	}
	return map[string][]byte{
		"random": bitsOf(func(int) uint64 { return r.Uint64() }),
		"small":  bitsOf(func(int) uint64 { return uint64(r.IntN(100)) }),
		"offset": bitsOf(func(int) uint64 { return 100 + uint64(r.IntN(20)) }),
		"signed": bitsOf(func(int) uint64 { return uint64(r.IntN(41) - 20) }),
		"runs":   bitsOf(func(i int) uint64 { return uint64(i / 30 % 7) }),
		"blocks": bitsOf(func(i int) uint64 { return uint64(i/5%3) * 77 }),
		"few":    bitsOf(func(i int) uint64 { return []uint64{3, 250, 17, 99}[i*13%4] << (8*w - 8) }),
		"sparse": bitsOf(func(i int) uint64 { return uint64(boolInt(i%97 == 0)) * (r.Uint64() | 1) }),
		"seq":    bitsOf(func(i int) uint64 { return uint64(3*i + 1) }),
	}
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullsOf(r *rand.Rand, n int, kind int) []bool {
	switch kind {
	case 0:
		return nil
	case 1:
		out := make([]bool, n)
		for i := range out {
			out[i] = r.IntN(5) == 0
		}
		return out
	case 2:
		out := make([]bool, n)
		for i := range out {
			out[i] = i%20 != 0 // mostly null
		}
		return out
	}
	out := make([]bool, n)
	for i := range out {
		out[i] = true
	}
	return out
}

// TestNeverLargerTyped checks that decimals, timestamps and booleans are never larger than plain.
func TestNeverLargerTyped(t *testing.T) {
	for _, mode := range []Mode{Default, Compact} {
		c := New(mode)
		for _, n := range []int{1, 7, 300, 5000} {
			r := rand.New(rand.NewPCG(7, uint64(n)))
			for kind := range 4 {
				nulls := nullsOf(r, n, kind)
				// decimals of 4 bytes spanning the i32 range, and of 16 bytes holding small values
				raw := make([]byte, 4*n)
				for i := range n {
					binary.LittleEndian.PutUint32(raw[4*i:], r.Uint32())
				}
				raw[0], raw[1], raw[2], raw[3] = 0, 0, 0, 0x80
				if n > 1 {
					raw[4], raw[5], raw[6], raw[7] = 0xff, 0xff, 0xff, 0x7f
				}
				c.Reset()
				if got := c.Decimals(raw, 4, nulls); got != nil &&
					cost(got) >= nodeOverhead+bufferOverhead+len(raw)+validityCost(nulls, n) {
					t.Errorf("decimals %d %d: %d bytes", n, kind, cost(got))
				}
				raw16 := make([]byte, 16*n)
				for i := range n {
					raw16[16*i] = byte(r.IntN(100))
				}
				c.Reset()
				if got := c.Decimals(raw16, 16, nulls); got != nil &&
					cost(got) >= nodeOverhead+bufferOverhead+len(raw16)+validityCost(nulls, n) {
					t.Errorf("decimals16 %d %d: %d bytes", n, kind, cost(got))
				}
				for _, ticks := range []int64{encode.TicksSecond, encode.TicksMilli, encode.TicksNano} {
					v := make([]int64, n)
					for i := range v {
						v[i] = int64(r.Uint64() >> 1)
						if i%3 == 0 {
							v[i] = int64(i) * 86_400 * ticks
						}
					}
					c.Reset()
					if got := c.Timestamps(v, nulls, ticks); got != nil && cost(got) >= numberCost(v, nulls) {
						t.Errorf("timestamps %d %d %d: %d bytes", n, kind, ticks, cost(got))
					}
				}
				bs := make([]bool, n)
				for i := range bs {
					bs[i] = r.IntN(2) == 0
				}
				c.Reset()
				if got := c.Bools(bs, nulls); got != nil &&
					cost(got) >= nodeOverhead+bufferOverhead+(n+7)/8+validityCost(nulls, n) {
					t.Errorf("bools %d %d: %d bytes", n, kind, cost(got))
				}
			}
		}
	}
}

// TestNeverLarger compresses columns of every type, with and without nulls, in both modes:
// the result is never larger than the plain array, and the same input gives the same bytes.
func TestNeverLarger(t *testing.T) {
	seen := map[string]bool{}
	for _, mode := range []Mode{Default, Compact} {
		c := New(mode)
		for p := U8; p <= F64; p++ {
			for _, n := range []int{1, 7, 300, 5000, 70_000} {
				r := rand.New(rand.NewPCG(uint64(p), uint64(n)))
				cols := columns(r, p, n)
				for _, name := range slices.Sorted(func(yield func(string) bool) {
					for k := range cols {
						if !yield(k) {
							return
						}
					}
				}) {
					for kind := range 4 {
						raw, nulls := cols[name], nullsOf(r, n, kind)
						c.Reset()
						got := c.Numbers(p, raw, nulls)
						first := dump(got)
						plain := nodeOverhead + bufferOverhead + len(raw) + validityCost(nulls, n)
						if got != nil && cost(got) >= plain {
							t.Errorf("%d %d %s %d: %d bytes, plain %d", p, n, name, kind, cost(got), plain)
						}
						checkNumbers(t, got, p, raw, nulls)
						c.Reset()
						if again := dump(c.Numbers(p, raw, nulls)); !bytes.Equal(first, again) {
							t.Errorf("%d %d %s %d: two compressions differ", p, n, name, kind)
						}
						seen[tree(got)] = true
					}
				}
			}
		}
	}
	for _, id := range []string{encode.IDBitPacked, encode.IDFoR, encode.IDZigZag, encode.IDRunEnd, encode.IDRLE,
		encode.IDDict, encode.IDSparse, encode.IDALP, encode.IDALPRD, encode.IDConstant, encode.IDSequence, encode.IDZstd} {
		if !slices.ContainsFunc(slices.Collect(func(yield func(string) bool) {
			for k := range seen {
				if !yield(k) {
					return
				}
			}
		}), func(s string) bool { return strings.Contains(s, id) }) {
			t.Errorf("no column picks %s", id)
		}
	}
}

func TestStrings(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	words := []string{"alpha", "beta", "gamma", "a-much-longer-value-than-twelve-bytes", ""}
	cols := map[string]func(i int) []byte{
		"few": func(i int) []byte { return []byte(words[i%len(words)]) },
		"text": func(i int) []byte {
			return fmt.Appendf(nil, "the order %d of the customer %d is late", i, r.IntN(1000))
		},
		"random": func(int) []byte {
			b := make([]byte, r.IntN(40))
			for k := range b {
				b[k] = byte(r.Uint32())
			}
			return b
		},
		"constant": func(int) []byte { return []byte("same") },
	}
	for _, mode := range []Mode{Default, Compact} {
		c := New(mode)
		for name, f := range cols {
			for _, n := range []int{1, 50, 3000, 20_000} {
				for kind := range 4 {
					v := make([][]byte, n)
					for i := range v {
						v[i] = f(i)
					}
					nulls := nullsOf(r, n, kind)
					c.Reset()
					got := c.Strings(v, nulls, name != "random")
					if got != nil && cost(got) >= binsCost(v, nulls) {
						t.Errorf("%s %d %d: %d bytes, plain %d", name, n, kind, cost(got), binsCost(v, nulls))
					}
					checkBins(t, got, v, nulls, name != "random")
				}
			}
		}
	}
	c := New(Default)
	if !c.PicksDictStrings(slices.Repeat([][]byte{[]byte("x-long-value-of-a-dictionary"), []byte("y")}, 2000), nil, true) {
		t.Error("two values are not a dictionary")
	}
	c.Reset()
	if c.PicksDict(I64, ints64(3000, func(i int) int64 { return int64(i) }), nil) {
		t.Error("a sequence is a dictionary")
	}
	if !c.PicksDict(I64, ints64(3000, func(i int) int64 { return []int64{-1 << 60, 5, 1 << 50}[i%3] }), nil) {
		t.Error("three values are not a dictionary")
	}
}

func TestDecimalsTimestampsBools(t *testing.T) {
	c := New(Default)
	r := rand.New(rand.NewPCG(5, 6))
	// 16-byte decimals that fit an i64, and one that does not
	raw := make([]byte, 16*1000)
	for i := range 1000 {
		v := r.Int64N(1_000_000) - 500_000
		for k := range 16 {
			raw[16*i+k] = byte(v >> min(8*k, 63))
		}
	}
	if got := tree(c.Decimals(raw, 16, nil)); !strings.HasPrefix(got, encode.IDDecimalBytes+"(") {
		t.Errorf("decimals: %s", got)
	}
	raw[16*3+15] = 0x40
	if got := c.Decimals(raw, 16, nil); got != nil {
		t.Errorf("a wide decimal: %s", tree(got))
	}
	// timestamps of one day in microseconds are parts; random ones are integers
	day := make([]int64, 4000)
	random := make([]int64, 4000)
	for i := range day {
		day[i] = 1_700_000_000_000_000 + int64(i)*1_000_000 + r.Int64N(1000)
		random[i] = r.Int64()
	}
	for _, tc := range []struct {
		v     []int64
		ticks int64
		want  string
	}{{day, encode.TicksMicro, encode.IDDateTimeParts}, {day, encode.TicksSecond, encode.IDFoR}, {random, encode.TicksNano, "plain"}} {
		c.Reset()
		if got := tree(c.Timestamps(tc.v, nil, tc.ticks)); !strings.HasPrefix(got, tc.want) {
			t.Errorf("timestamps of %d ticks: %s, want %s", tc.ticks, got, tc.want)
		}
	}
	if got := tree(c.Bools(slices.Repeat([]bool{true}, 100), nil)); got != encode.IDConstant {
		t.Errorf("bools: %s", got)
	}
	if got := c.Bools([]bool{true, false}, nil); got != nil {
		t.Errorf("bools: %s", tree(got))
	}
}

func TestTypes(t *testing.T) {
	for p, want := range map[PType][2]int{U8: {1, 0}, U16: {2, 1}, U32: {4, 2}, U64: {8, 3}, I8: {1, 0},
		I16: {2, 1}, I32: {4, 2}, I64: {8, 3}, F32: {4, 2}, F64: {8, 3}} {
		if p.Width() != want[0] || p.unsigned() != PType(want[1]) {
			t.Errorf("%d: width %d, unsigned %d", p, p.Width(), p.unsigned())
		}
	}
	if intOf(4) != I32 || uintOf(2) != U16 {
		t.Error(intOf(4), uintOf(2))
	}
}

func TestSample(t *testing.T) {
	for _, n := range []int{1, 1024, 1025, 99_999, 1_000_000} {
		prev, total := -1, 0
		rows := sampleRuns(n, func(start, size int) {
			if start <= prev || start+size > n {
				t.Errorf("%d rows: a run at %d of %d after %d", n, start, size, prev)
			}
			prev, total = start+size-1, total+size
		})
		if rows != total || n > 1024 && (rows < 1024 || rows%sampleSize != 0) || n <= 1024 && rows != n {
			t.Errorf("%d rows: a sample of %d, runs of %d", n, rows, total)
		}
	}
	// a million rows: 157 runs of 1%, 160 with the rounding
	if got := sampleRuns(1_000_000, func(int, int) {}); got != 160*sampleSize {
		t.Errorf("a sample of %d", got)
	}
}

// TestNoAllocation compresses chunks once the compressor has seen them: it allocates nothing.
func TestNoAllocation(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	nums := columns(r, I32, 20_000)
	floats := columns(r, F64, 20_000)
	strs := make([][]byte, 20_000)
	for i := range strs {
		strs[i] = fmt.Appendf(nil, "value %d", r.IntN(500))
	}
	nulls := nullsOf(r, 20_000, 1)
	day := make([]int64, 20_000)
	for i := range day {
		day[i] = int64(i) * 1_234_567
	}
	dec := make([]byte, 8*20_000)
	c := New(Compact)
	numNames, floatNames := sortedKeys(nums), sortedKeys(floats)
	run := func() {
		for _, name := range numNames {
			c.Reset()
			c.Numbers(I32, nums[name], nulls)
		}
		for _, name := range floatNames {
			c.Reset()
			c.Numbers(F64, floats[name], nil)
		}
		c.Reset()
		c.Strings(strs, nulls, true)
		c.Reset()
		c.Timestamps(day, nil, encode.TicksMilli)
		c.Reset()
		c.Decimals(dec, 8, nulls)
	}
	run()
	run()
	if raceEnabled { // its sync.Pool drops what it is given at random, and the encoders use pools
		return
	}
	if n := testing.AllocsPerRun(5, run); n != 0 {
		t.Errorf("%v allocations", n)
	}
}

// TestMaskedConstant: a chunk of one value among nulls is a constant under a validity, smaller
// than the plain array, and the cases around it are not masked.
func TestMaskedConstant(t *testing.T) {
	const rows = 8192
	nulls := make([]bool, rows)
	for i := range nulls {
		nulls[i] = i%10 < 3
	}
	c := New(Default)
	want := "vortex.masked(vortex.constant vortex.bool)"
	n := c.Numbers(I64, ints64(rows, func(int) int64 { return 7 }), nulls)
	if got := tree(n); got != want {
		t.Fatalf("numbers: %s", got)
	}
	if plain := numberCost(make([]int64, rows), nulls); cost(n) >= plain/10 {
		t.Fatalf("masked costs %d, plain %d", cost(n), plain)
	}
	bins := make([][]byte, rows)
	for i := range bins {
		bins[i] = []byte("a string that is longer than twelve bytes")
	}
	if got := tree(c.Strings(bins, nulls, true)); got != want {
		t.Fatalf("strings: %s", got)
	}
	bools := make([]bool, rows)
	for i := range bools {
		bools[i] = true
	}
	if got := tree(c.Bools(bools, nulls)); got != want {
		t.Fatalf("bools: %s", got)
	}
	// no null: the constant alone; all null: the null constant; two values: not masked
	if got := tree(c.Numbers(I64, ints64(rows, func(int) int64 { return 7 }), nil)); got != "vortex.constant" {
		t.Fatalf("no null: %s", got)
	}
	all := slices.Repeat([]bool{true}, rows)
	if got := tree(c.Numbers(I64, ints64(rows, func(int) int64 { return 7 }), all)); got != "vortex.constant" {
		t.Fatalf("all null: %s", got)
	}
	two := ints64(rows, func(i int) int64 { return int64(i % 2) })
	if got := tree(c.Numbers(I64, two, nulls)); strings.HasPrefix(got, "vortex.masked") {
		t.Fatalf("two values: %s", got)
	}
}

// TestVarBinForBinary: binary values of one width are a varbin whose offsets are a sequence,
// and cost under 17 bytes a value (32 as views). A utf8 column never is a varbin, and a few
// distinct binary values stay a dictionary.
func TestVarBinForBinary(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	c := New(Default)
	v := make([][]byte, 10_000)
	for i := range v {
		v[i] = make([]byte, 16)
		for k := range v[i] {
			v[i][k] = byte(r.Uint32())
		}
	}
	got := c.Strings(v, nil, false)
	if got == nil || got.ID != encode.IDVarBin || got.Kids[0].ID != encode.IDSequence {
		t.Fatalf("got %s, want a varbin of a sequence", tree(got))
	}
	if per := float64(cost(got)) / float64(len(v)); per >= 17 {
		t.Errorf("%.2f bytes a value", per)
	}
	checkBins(t, got, v, nil, false)
	c.Reset()
	if got := c.Strings(v, nil, true); got != nil && got.ID == encode.IDVarBin {
		t.Error("a utf8 column is a varbin")
	}
	c.Reset()
	few := make([][]byte, 10_000)
	for i := range few {
		few[i] = v[i%3]
	}
	if got := c.Strings(few, nil, false); got == nil || got.ID != encode.IDDict {
		t.Errorf("3 distinct values are %s", tree(got))
	}
}

// ids is the identifiers of a tree.
func ids(n *encode.Node) []string {
	if n == nil {
		return nil
	}
	out := []string{n.ID}
	for _, k := range n.Kids {
		out = append(out, ids(k)...)
	}
	return out
}

// TestLimits: the encodings of a limit are left out, and each is met with no limit, so that the
// case does hold what it leaves out.
func TestLimits(t *testing.T) {
	const rows = 8192
	nulls := make([]bool, rows)
	for i := range nulls {
		nulls[i] = i%10 < 3
	}
	r := rand.New(rand.NewPCG(3, 4))
	floats := make([]float64, rows)
	for i := range floats {
		floats[i] = float64(i / 100) // runs of equal values
	}
	long := make([][]byte, rows)
	for i := range long {
		long[i] = []byte(fmt.Sprintf("a string that is longer than twelve bytes %d", r.IntN(1<<30)))
	}
	same := make([][]byte, rows)
	for i := range same {
		same[i] = []byte("a string that is longer than twelve bytes")
	}
	bools := slices.Repeat([]bool{true}, rows)
	cases := []struct {
		name string
		id   string
		mode Mode
		off  Limits
		run  func(c *Compressor) *encode.Node
	}{
		{"sequence", encode.IDSequence, Default, Limits{NoSequence: true}, func(c *Compressor) *encode.Node {
			return c.Numbers(I64, ints64(rows, func(i int) int64 { return int64(3 * i) }), nil)
		}},
		{"masked ints", encode.IDMasked, Default, Limits{NoMasked: true}, func(c *Compressor) *encode.Node {
			return c.Numbers(I64, ints64(rows, func(int) int64 { return 7 }), nulls)
		}},
		{"masked floats", encode.IDMasked, Default, Limits{NoMasked: true}, func(c *Compressor) *encode.Node {
			return c.Numbers(F64, unsafex.Bytes(slices.Repeat([]float64{1.5}, rows)), nulls)
		}},
		{"masked strings", encode.IDMasked, Default, Limits{NoMasked: true}, func(c *Compressor) *encode.Node {
			return c.Strings(same, nulls, true)
		}},
		{"masked bools", encode.IDMasked, Default, Limits{NoMasked: true}, func(c *Compressor) *encode.Node {
			return c.Bools(bools, nulls)
		}},
		{"rle floats", encode.IDRLE, Default, Limits{NoRLE: true}, func(c *Compressor) *encode.Node {
			v := make([]float64, rows)
			for i := 0; i < rows; {
				x := float64(r.Int64N(5000)*100003) * 1.37
				for n := 1 + r.IntN(8); n > 0 && i < rows; n-- {
					v[i] = x
					i++
				}
			}
			return c.Numbers(F64, unsafex.Bytes(v), nil)
		}},
		{"zstd floats", encode.IDZstd, Compact, Limits{NoZstd: true}, func(c *Compressor) *encode.Node {
			v := make([]float64, rows)
			for i := range v {
				v[i] = float64(r.IntN(50)) / 3
			}
			return c.Numbers(F64, unsafex.Bytes(v), nil)
		}},
		{"zstd strings", encode.IDZstd, Compact, Limits{NoZstd: true}, func(c *Compressor) *encode.Node {
			return c.Strings(long, nil, true)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(tc.run(New(tc.mode))); !slices.Contains(got, tc.id) {
				t.Fatalf("with no limit: %v, want %s in it", got, tc.id)
			}
			if got := ids(tc.run(NewLimited(tc.mode, tc.off))); slices.Contains(got, tc.id) {
				t.Fatalf("with the limit: %v", got)
			}
		})
	}
}

// TestIntLimits: run-end wins the runs before RLE, so the candidates of integers are the proof.
func TestIntLimits(t *testing.T) {
	st := intStats{runs: 10, valid: 8192, packW: 20, forW: 20}
	has := func(c *Compressor, s scheme) bool {
		l := intCands(c, st, c.top(), I64, 8192, true)
		return slices.Contains(l.s[:l.n], s)
	}
	if c := New(Compact); !has(c, sRLE) || !has(c, sZstd) {
		t.Fatal("no limit: RLE and zstd are candidates")
	}
	if c := NewLimited(Compact, Limits{NoRLE: true, NoZstd: true}); has(c, sRLE) || has(c, sZstd) {
		t.Fatal("limited: RLE and zstd are left out")
	}
}
