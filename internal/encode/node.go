package encode

import (
	"hash/maphash"
	"math"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/fclairamb/vortex-go/internal/edition"
	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// The identifiers of the arrays the encoders make.
const (
	IDPrimitive  = "vortex.primitive"
	IDBool       = "vortex.bool"
	IDBitPacked  = "fastlanes.bitpacked"
	IDFoR        = "fastlanes.for"
	IDRLE        = "fastlanes.rle"
	IDZigZag     = "vortex.zigzag"
	IDRunEnd     = "vortex.runend"
	IDDict       = "vortex.dict"
	IDSequence   = "vortex.sequence"
	IDConstant   = "vortex.constant"
	IDMasked     = "vortex.masked"
	IDSparse     = "vortex.sparse"
	IDVarBin     = "vortex.varbin"
	IDVarBinView = "vortex.varbinview"
	IDByteBool   = "vortex.bytebool"
	IDChunked    = "vortex.chunked"

	IDALP           = "vortex.alp"
	IDALPRD         = "vortex.alprd"
	IDFSST          = "vortex.fsst"
	IDOnPair        = "vortex.onpair"
	IDDecimalBytes  = "vortex.decimal_byte_parts"
	IDDateTimeParts = "vortex.datetimeparts"
	IDZstd          = "vortex.zstd"
	IDPco           = "vortex.pco"
)

// The numbers of the PType enum of the metadata messages.
const (
	pU8  = 0
	pU16 = 1
	pU32 = 2
	pU64 = 3
	pI8  = 4
	pI16 = 5
	pI32 = 6
	pI64 = 7
)

// The fields of the ScalarValue message.
const (
	scalarNull = 1
	scalarBool = 2
	scalarInt  = 3
	scalarUint = 4
	scalarF32  = 5
	scalarF64  = 6
)

// Int is the integer types the encoders take.
type Int interface {
	~int8 | ~int16 | ~int32 | ~int64 | ~uint8 | ~uint16 | ~uint32 | ~uint64
}

// Number is the numeric types the encoders take: integers and floats.
type Number = unsafex.Number

// Buf is a buffer of an array and the alignment it asks for.
type Buf struct {
	Data  []byte
	Align int
}

// Node is an encoded array.
type Node struct {
	kids  [5]*Node
	ID    string
	Meta  []byte
	Bufs  []Buf
	Kids  []*Node
	grown []Buf
	bufs  [3]Buf
}

func (n *Node) add(data []byte, align int) {
	n.Bufs = append(n.Bufs, Buf{data, align})
	if cap(n.Bufs) > cap(n.grown) && cap(n.Bufs) > len(n.bufs) {
		n.grown = n.Bufs
	}
}

// kid adds a child. A nil child (an absent validity) is not added.
func (n *Node) kid(k *Node) {
	if k != nil {
		n.Kids = append(n.Kids, k)
	}
}

// Size is the bytes of the buffers of the tree.
func (n *Node) Size() int {
	s := 0
	for _, b := range n.Bufs {
		s += len(b.Data)
	}
	for _, k := range n.Kids {
		s += k.Size()
	}
	return s
}

// Encoder holds the memory of the nodes that the encoders make, and what they need to work.
// The zero value is not usable: call New.
type Encoder struct {
	fs      *fsstState
	op      *onPairState
	zenc    *zstd.Encoder
	blocks  []block // the arena: the buffers of the nodes, kept from one Reset to the next
	pool    []*Node
	tmp     []byte
	tmp2    []byte
	dt      *dictTables
	flagBuf []bool
	u16s    []uint16
	groups  []rdGroup
	seed    maphash.Seed
	cur     int // the block in use
	off     int // the bytes of the block in use that are handed out
	used    int
	hist    [65]int
	measure bool // the arrays are only measured, see Measure
}

// block is a buffer of the arena. Past dirty, its bytes are still the zeroes make gave them.
type block struct {
	buf   []byte
	dirty int
}

// newBlock returns a block of n zeroes. The tests of the package fill it instead, so that a
// caller of allocDirty that reads a byte it did not write makes other bytes than the reference.
var newBlock = func(n int) block { return block{buf: make([]byte, n)} }

// minBlock is the size of the first block of an Encoder. Most encoders of a writer hold a
// few tens of kilobytes.
const minBlock = 16 << 10

// New returns an Encoder.
func New() *Encoder { return &Encoder{seed: maphash.MakeSeed()} }

// Measure makes the arrays of e ones that are only measured, never read: bit-packing leaves
// the bytes of its values and of its patches unwritten, in buffers of the right sizes. The
// compressor measures its candidates on a sample that way.
func (e *Encoder) Measure() { e.measure = true }

// Reset frees the nodes and buffers of the earlier calls, which must not be used any more.
// The blocks of the arena are kept: the same calls fit them next time, with no new memory.
func (e *Encoder) Reset() {
	e.cur, e.off, e.used = 0, 0, 0
	e.done()
}

// The memory of a training of FSST or of OnPair, of a Pco coder and of the tables of a
// dictionary is large and only used during one call: it is taken from a pool for the call and
// given back at its end, so that the encoders of all the columns of a writer share as many as
// there are calls at once.
var (
	dictStates   = sync.Pool{New: func() any { return new(dictTables) }}
	fsstStates   = sync.Pool{New: func() any { return &fsstState{c2: make([]int32, 512*512)} }}
	onPairStates = sync.Pool{New: func() any { return new(onPairState) }}
	pcoStates    = sync.Pool{New: func() any { return new(pcoState) }}
)

// done gives back the memory of the tables and of the training of the call that ends.
func (e *Encoder) done() {
	if e.dt != nil {
		dictStates.Put(e.dt)
		e.dt = nil
	}
	if e.fs != nil {
		fsstStates.Put(e.fs)
		e.fs = nil
	}
	if e.op != nil {
		onPairStates.Put(e.op)
		e.op = nil
	}
}

// alloc returns n zeroed bytes, aligned on 8 bytes, that live until the next Reset.
func (e *Encoder) alloc(n int) []byte {
	b, dirty := e.take(n)
	clear(b[:dirty])
	return b
}

// allocDirty is alloc without the zeroes: the bytes are what an earlier use left. It is for
// the caller that writes every byte it reads, which saves a pass over a large buffer.
func (e *Encoder) allocDirty(n int) []byte {
	b, _ := e.take(n)
	return b
}

// take returns n bytes of the arena, aligned on 8 bytes, and how many of the first ones an
// earlier use wrote: the others are zero. A request that does not fit the rest of the block
// in use goes to the next block that holds it, a new one when none does, twice as large as
// the last: the slices already given out stay valid.
func (e *Encoder) take(n int) ([]byte, int) {
	e.off = (e.off + 7) &^ 7
	for e.cur < len(e.blocks) && e.off+n > len(e.blocks[e.cur].buf) {
		e.cur, e.off = e.cur+1, 0
	}
	if e.cur == len(e.blocks) {
		size := minBlock
		if len(e.blocks) > 0 {
			size = 2 * len(e.blocks[len(e.blocks)-1].buf)
		}
		e.blocks = append(e.blocks, newBlock(max(n, size)))
	}
	b := &e.blocks[e.cur]
	s := b.buf[e.off : e.off+n : e.off+n]
	dirty := min(max(b.dirty-e.off, 0), n)
	e.off += n
	b.dirty = max(b.dirty, e.off)
	return s, dirty
}

// node returns an empty node.
func (e *Encoder) node(id string) *Node {
	if e.used == len(e.pool) {
		e.pool = append(e.pool, new(Node))
	}
	n := e.pool[e.used]
	e.used++
	n.ID, n.Meta = id, nil
	// a node that grew its buffers (the frames of zstd) keeps them for the next use
	n.Bufs, n.Kids = n.bufs[:0], n.kids[:0]
	if cap(n.Bufs) < cap(n.grown) {
		n.Bufs = n.grown[:0]
	}
	return n
}

// keep copies b, which is scratch, into memory the node can keep.
func (e *Encoder) keep(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return append(e.allocDirty(len(b))[:0], b...)
}

// leaf is a primitive array of the raw bytes, with the validity of nulls when a row is null.
func (e *Encoder) leaf(raw []byte, width int, nulls []bool, rows int) *Node {
	n := e.node(IDPrimitive)
	n.add(raw, width)
	n.kid(e.validity(nulls, rows, rows))
	return n
}

// hasNull says whether a row is null.
func hasNull(nulls []bool) bool {
	for _, x := range nulls {
		if x {
			return true
		}
	}
	return false
}

// allNull says whether there is a row and all of them are null.
func allNull(nulls []bool) bool {
	for _, x := range nulls {
		if !x {
			return false
		}
	}
	return len(nulls) > 0
}

// validity is the bool array of the validity of the rows, padded with invalid rows up to
// padded, or nil when no row is null. When all rows are null it is a constant false, as the
// reference writes an array that has no valid value.
func (e *Encoder) validity(nulls []bool, rows, padded int) *Node {
	if !hasNull(nulls) {
		return nil
	}
	if allNull(nulls) {
		n := e.node(IDConstant)
		e.tmp = protowire.AppendUint(e.tmp[:0], scalarBool, 0)
		n.add(e.keep(e.tmp), 1)
		return n
	}
	return e.validityBits(nulls, rows, padded)
}

// validityBits is the bool array of the validity of the rows, padded with invalid rows up to
// padded, even when all the rows are null.
func (e *Encoder) validityBits(nulls []bool, rows, padded int) *Node {
	bits := e.alloc((padded + 7) / 8)
	for i := range rows {
		if !nulls[i] {
			bits[i>>3] |= 1 << (i & 7)
		}
	}
	n := e.node(IDBool)
	n.add(bits, 1)
	return n
}

// isNull says whether row i is null.
func isNull(nulls []bool, i int) bool { return nulls != nil && nulls[i] }

// putUint appends field num, left out when v is zero like protobuf does.
func putUint(b []byte, num int, v uint64) []byte {
	if v == 0 {
		return b
	}
	return protowire.AppendUint(b, num, v)
}

// signed says whether the integer type T is signed.
func signed[T Number]() bool {
	var z T
	z--
	return z < 0
}

// isFloat says whether T is a float type.
func isFloat[T Number]() bool {
	var h T = 1
	h /= 2
	return h != 0
}

// appendScalar appends the ScalarValue message of v.
func appendScalar[T Number](b []byte, v T) []byte {
	switch {
	case isFloat[T]() && unsafex.Size[T]() == 4:
		return protowire.AppendFixed32(b, scalarF32, math.Float32bits(float32(v)))
	case isFloat[T]():
		return protowire.AppendFixed64(b, scalarF64, math.Float64bits(float64(v)))
	case signed[T]():
		return protowire.AppendSint(b, scalarInt, int64(v))
	}
	return protowire.AppendUint(b, scalarUint, uint64(v))
}

// widthMask is the bits of a value of type T, in a uint64.
func widthMask[T Number]() uint64 { return ^uint64(0) >> (64 - 8*unsafex.Size[T]()) }

// ubits is the bits of v as an unsigned integer of the width of T.
func ubits[T Int](v T) uint64 { return uint64(v) & widthMask[T]() }

// putWidth writes v in the first w bytes of b, little-endian.
func putWidth(b []byte, w int, v uint64) {
	for i := range w {
		b[i] = byte(v >> (8 * i))
	}
}

// getWidth reads w bytes of b, little-endian.
func getWidth(b []byte, w int) uint64 {
	var v uint64
	for i := range w {
		v |= uint64(b[i]) << (8 * i)
	}
	return v
}

// narrow is the PType number and the width of the smallest unsigned integer that holds limit.
func narrow(limit uint64) (ptype, width int) {
	ptype, width = pU8, 1
	for limit>>(8*width) != 0 {
		ptype, width = ptype+1, 2*width
	}
	return ptype, width
}

// flags returns n false flags, scratch of the Encoder.
func (e *Encoder) flags(n int) []bool {
	if cap(e.flagBuf) < n {
		e.flagBuf = make([]bool, max(2*cap(e.flagBuf), n))
	}
	f := e.flagBuf[:n]
	clear(f)
	return f
}

// leafPadded is leaf with a validity padded to padded rows.
func (e *Encoder) leafPadded(raw []byte, width int, nulls []bool, rows, padded int) *Node {
	n := e.node(IDPrimitive)
	n.add(raw, width)
	n.kid(e.validity(nulls, rows, padded))
	return n
}

// Encodings lists the identifiers of the arrays the encoders make.
func Encodings() []string {
	return []string{
		IDPrimitive, IDBool, IDBitPacked, IDFoR, IDRLE, IDZigZag, IDRunEnd, IDDict, IDSequence,
		IDConstant, IDMasked, IDSparse, IDVarBin, IDVarBinView, IDALP, IDALPRD, IDFSST, IDOnPair, IDDecimalBytes,
		IDDateTimeParts, IDZstd, IDPco, IDByteBool, IDChunked,
	}
}

var _ = edition.RegisterWritten(edition.KindArray, Encodings)
