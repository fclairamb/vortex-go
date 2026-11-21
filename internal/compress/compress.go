// Package compress is the compressor of the writer: for one chunk of one column, it tries the
// encodings of internal/encode that fit its type on a sample of the rows, keeps the one that
// makes the smallest array, and applies it to the whole chunk. The children of an encoding (the
// codes of a dictionary, the integers of ALP, the lengths of FSST) are compressed the same way,
// down to a depth of 3, as the reference does (vortex-compressor and vortex-btrblocks).
//
// A candidate is measured by encoding the sample with it, its children included, and counting
// the bytes of the tree plus an overhead per array and per buffer, so that an encoding that
// saves less than what it costs to describe is not taken. The plain array is always a
// candidate, and the result is checked against it on the whole chunk: a chunk is never made
// larger. The sample is runs of 64 consecutive rows, at least 16 of them and about 1% of the
// chunk, at offsets drawn from a fixed seed: the same rows always give the same tree.
//
// A Compressor is not safe for concurrent use: the writer has one per column. Once it has
// seen chunks of the largest size, it allocates nothing.
package compress

import (
	"math/bits"
	"math/rand/v2"

	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// Mode is the set of encodings the compressor takes.
type Mode uint8

const (
	// Default is the lightweight encodings.
	Default Mode = iota
	// Compact adds Pco for the numbers wider than 8 bits, and zstd for the numbers and the strings.
	Compact
)

// Limits lists the encodings that a compressor leaves out, because the edition a file targets
// does not have them. The zero value leaves out none.
type Limits struct {
	NoRLE      bool // fastlanes.rle
	NoMasked   bool // vortex.masked
	NoSequence bool // vortex.sequence
	NoZstd     bool // vortex.zstd
	NoOnPair   bool // vortex.onpair
	NoPco      bool // vortex.pco
}

// PType is the type of the numbers of an array.
type PType uint8

// The types of numbers.
const (
	U8 PType = iota
	U16
	U32
	U64
	I8
	I16
	I32
	I64
	F32
	F64
)

// Width is the bytes of a number of the type.
func (p PType) Width() int {
	switch p {
	case U8, I8:
		return 1
	case U16, I16:
		return 2
	case U32, I32, F32:
		return 4
	}
	return 8
}

// unsigned is the unsigned type of the width of p.
func (p PType) unsigned() PType { return uintOf(p.Width()) }

// uintOf is the unsigned type of width bytes.
func uintOf(width int) PType { return PType(bits.TrailingZeros(uint(width))) }

// intOf is the signed type of width bytes.
func intOf(width int) PType { return I8 + uintOf(width) }

// The schemes: an encoding, and what the compressor does with its children.
type scheme uint8

const (
	sPlain scheme = iota
	sConstant
	sSequence
	sBitPack
	sFoR
	sZigZag
	sRunEnd
	sRLE
	sDict
	sSparse
	sALP
	sALPRD
	sFSST
	sOnPair
	sPco
	sZstd
	sVarBin
	sDecimal
	sDateTime
)

// The constants of the reference.
const (
	sampleSize   = 64         // the rows of a run of the sample
	sampleCount  = 16         // the fewest runs
	sampleSeed   = 1234567890 // the seed of the offsets of the runs
	maxDepth     = 3          // the deepest child that is compressed
	runThreshold = 4          // the average run length under which run-end and RLE are skipped
	// The bytes an array costs besides its buffers (its node in the flatbuffer of the
	// segment), and a buffer besides its data (its descriptor and its padding).
	nodeOverhead   = 16
	bufferOverhead = 8
)

// Compressor holds the memory of the compression of the chunks of a column.
type Compressor struct {
	e     *encode.Encoder // the arrays that are kept
	trial *encode.Encoder // the arrays of the candidates, dropped after each choice
	raw   arena[byte]     // the samples of numbers
	nulls arena[bool]     // the samples of nulls, and the nulls of children
	bins  arena[[]byte]   // the samples of strings, and the values of dictionaries
	mode  Mode
	limit Limits
}

// New returns a Compressor.
func New(mode Mode) *Compressor { return NewLimited(mode, Limits{}) }

// NewLimited returns a Compressor that does without the encodings of limit.
func NewLimited(mode Mode, limit Limits) *Compressor {
	return &Compressor{e: encode.New(), trial: encode.New(), mode: mode, limit: limit}
}

// Configure makes c a Compressor of this mode and of these limits, with the memory it has: the
// arrays of its earlier calls must not be used any more.
func (c *Compressor) Configure(mode Mode, limit Limits) {
	c.Reset()
	c.mode, c.limit = mode, limit
}

// zstd says whether zstd is a candidate.
func (c *Compressor) zstd() bool { return c.mode == Compact && !c.limit.NoZstd }

// pco says whether Pco is a candidate for numbers of width bytes: never for 8-bit numbers.
func (c *Compressor) pco(width int) bool { return c.mode == Compact && !c.limit.NoPco && width > 1 }

// Reset frees the arrays of the earlier calls, which must not be used any more.
func (c *Compressor) Reset() {
	c.e.Reset()
	c.trial.Reset()
	c.raw.reset()
	c.nulls.reset()
	c.bins.reset()
}

// arena hands out slices that live until its reset. A slice that does not fit is taken from
// a larger buffer: the ones already given out stay valid.
type arena[T any] struct {
	buf  []T
	off  int
	need int // what is given out
	peak int // the most that was given out at once since the last reset
}

func (a *arena[T]) get(n int) []T {
	a.off = (a.off + 7) &^ 7 // the numbers of a sample are aligned
	a.need += (n + 7) &^ 7
	a.peak = max(a.peak, a.need)
	if a.off+n > len(a.buf) {
		a.buf = make([]T, max(n, 4<<10))
		a.off = 0
	}
	s := a.buf[a.off : a.off+n : a.off+n]
	a.off += n
	clear(s)
	return s
}

// mark is where the arena is, for restore.
func (a *arena[T]) mark() [2]int { return [2]int{a.off, a.need} }

// restore frees the slices given out since the mark.
func (a *arena[T]) restore(m [2]int) { a.off, a.need = m[0], m[1] }

// reset frees the slices, and makes the buffer hold what was asked of it, in one block.
func (a *arena[T]) reset() {
	if a.peak > len(a.buf) {
		a.buf = make([]T, a.peak)
	}
	a.off, a.need, a.peak = 0, 0, 0
}

// cost is the bytes of the tree of n in a file.
func cost(n *encode.Node) int {
	total := nodeOverhead + len(n.Meta)
	for _, b := range n.Bufs {
		total += bufferOverhead + len(b.Data)
	}
	for _, k := range n.Kids {
		total += cost(k)
	}
	return total
}

// validityCost is the cost of the validity of rows rows.
func validityCost(nulls []bool, rows int) int {
	if !hasNull(nulls) {
		return 0
	}
	return nodeOverhead + bufferOverhead + (rows+7)/8
}

func hasNull(nulls []bool) bool {
	for _, x := range nulls {
		if x {
			return true
		}
	}
	return false
}

func isNull(nulls []bool, i int) bool { return nulls != nil && nulls[i] }

// sampleRuns calls f with the start of each run of the sample of an array of n rows, and
// returns the rows of the sample: all of them when the array is small.
func sampleRuns(n int, f func(start, size int)) int {
	count := max((n/100/sampleSize+15)/16*16, sampleCount)
	total := count * sampleSize
	if total >= n {
		f(0, n)
		return n
	}
	var rng rand.PCG
	rng.Seed(sampleSeed, sampleSeed)
	// count partitions of the rows, the first n%count one longer, and as many runs of the
	// sample, the first total%count one longer: a run is drawn within its partition
	for i := range count {
		start, stop := part(n, count, i)
		lo, hi := part(total, count, i)
		size := hi - lo
		f(start+int(rng.Uint64()%uint64(stop-size-start+1)), size)
	}
	return total
}

// part is partition i of count of n rows.
func part(n, count, i int) (start, stop int) {
	long := n % count
	short := n / count
	if i < long {
		return i * (short + 1), (i + 1) * (short + 1)
	}
	start = long*(short+1) + (i-long)*short
	return start, start + short
}

// sampleNulls is the nulls of the sample, nil when there is none.
func (c *Compressor) sampleNulls(nulls []bool, rows int) []bool {
	if nulls == nil {
		return nil
	}
	out := c.nulls.get(rows)[:0]
	sampleRuns(len(nulls), func(start, size int) { out = append(out, nulls[start:start+size]...) })
	return out
}

// kidNulls is the nulls of a primitive child, from its validity.
func (c *Compressor) kidNulls(k *encode.Node, rows int) []bool {
	if len(k.Kids) == 0 {
		return nil
	}
	v := k.Kids[0]
	nulls := c.nulls.get(rows)
	for i := range nulls {
		nulls[i] = v.ID == encode.IDConstant || v.Bufs[0].Data[i>>3]&(1<<(i&7)) == 0
	}
	return nulls
}

// at is where an array is compressed: the encoder of its nodes, whether it is a trial (a
// sample, or a child of one), its depth and the scheme of its parent.
type at struct {
	e      *encode.Encoder
	depth  int
	trial  bool
	parent scheme
}

// child is where a child of an array made by scheme s is compressed.
func (x at) child(s scheme) at { return at{e: x.e, depth: x.depth + 1, trial: x.trial, parent: s} }

// cascades says whether the children of the array are compressed.
func (x at) cascades() bool { return x.depth < maxDepth }

// kid compresses child i of n, a primitive array of numbers of type p, in place.
func (c *Compressor) kid(x at, n *encode.Node, i int, p PType) {
	k := n.Kids[i]
	if k.ID != encode.IDPrimitive {
		return
	}
	raw := k.Bufs[0].Data
	rows := len(raw) / p.Width()
	if out := c.numbers(x, p, raw, c.kidNulls(k, rows)); out != nil {
		n.Kids[i] = out
	}
}

// kids compresses the children of n, made by scheme s for numbers of type p.
func (c *Compressor) kids(x at, n *encode.Node, p PType, s scheme) {
	if !x.cascades() {
		return
	}
	k := x.child(s)
	width := func(i int) int { return n.Kids[i].Bufs[0].Align }
	switch n.ID {
	case encode.IDFoR:
		c.kid(k, n, 0, p)
	case encode.IDZigZag:
		c.kid(k, n, 0, p.unsigned())
	case encode.IDRunEnd:
		c.kid(k, n, 0, uintOf(width(0)))
		c.kid(k, n, 1, p)
	case encode.IDRLE:
		c.kid(k, n, 0, p)
		c.kid(k, n, 1, U16)
		c.kid(k, n, 2, U64)
	case encode.IDDict:
		c.kid(k, n, 0, uintOf(width(0)))
		c.kid(k, n, 1, p)
	case encode.IDALP:
		c.kid(k, n, 0, intOf(p.Width()))
	case encode.IDALPRD:
		c.kid(k, n, 0, U16)
		c.kid(k, n, 1, uintOf(width(1)))
	}
}

// numbers is the compressed array of the numbers raw of type p, or nil when the plain array is
// the smallest. In a trial every candidate is built on the whole array, and the smallest kept.
// Else the candidates are measured on a sample, in the trial encoder, and the best one is
// applied to the whole array.
func (c *Compressor) numbers(x at, p PType, raw []byte, nulls []bool) *encode.Node {
	switch p {
	case U8:
		return ints(c, x, p, unsafex.Slice[uint8](raw), nulls, nil)
	case U16:
		return ints(c, x, p, unsafex.Slice[uint16](raw), nulls, nil)
	case U32:
		return ints(c, x, p, unsafex.Slice[uint32](raw), nulls, nil)
	case U64:
		return ints(c, x, p, unsafex.Slice[uint64](raw), nulls, nil)
	case I8:
		return ints(c, x, p, unsafex.Slice[int8](raw), nulls, encode.ZigZag[int8])
	case I16:
		return ints(c, x, p, unsafex.Slice[int16](raw), nulls, encode.ZigZag[int16])
	case I32:
		return ints(c, x, p, unsafex.Slice[int32](raw), nulls, encode.ZigZag[int32])
	case I64:
		return ints(c, x, p, unsafex.Slice[int64](raw), nulls, encode.ZigZag[int64])
	case F32:
		return floats(c, x, p, unsafex.Slice[float32](raw), nulls)
	}
	return floats(c, x, p, unsafex.Slice[float64](raw), nulls)
}

// top is where the array of a chunk is compressed.
func (c *Compressor) top() at { return at{e: c.e} }

// Numbers is the compressed array of the numbers raw of type p, or nil for the plain array.
func (c *Compressor) Numbers(p PType, raw []byte, nulls []bool) *encode.Node {
	return c.numbers(c.top(), p, raw, nulls)
}

// PicksDict says whether the compressor would write the numbers raw of type p as a
// dictionary: the probe of the dictionary layout of the writer.
func (c *Compressor) PicksDict(p PType, raw []byte, nulls []bool) bool {
	n := c.numbers(c.top(), p, raw, nulls)
	return n != nil && n.ID == encode.IDDict
}
