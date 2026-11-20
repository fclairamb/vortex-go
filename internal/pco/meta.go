package pco

import (
	"math"
	"math/bits"
)

// Type is a number type.
type Type uint8

// The number types Pco codes.
const (
	U8 Type = iota
	U16
	U32
	U64
	I8
	I16
	I32
	I64
	F16
	F32
	F64
)

var typeNames = [...]string{"u8", "u16", "u32", "u64", "i8", "i16", "i32", "i64", "f16", "f32", "f64"}

func (t Type) String() string { return typeNames[t] }

// Bits is the width of a number of the type.
func (t Type) Bits() int {
	switch t {
	case U8, I8:
		return 8
	case U16, I16, F16:
		return 16
	case U32, I32, F32:
		return 32
	}
	return 64
}

// Float reports whether t is a floating point type.
func (t Type) Float() bool { return t >= F16 }

// Signed reports whether t is a signed integer type.
func (t Type) Signed() bool { return t >= I8 && t <= I64 }

// maxN is the most numbers a chunk may hold.
const maxN = 1 << 24

const (
	maxANSBits       = 14
	maxLookbackLog   = 24
	maxConvOrder     = 32
	interleaving     = 4
	batchN           = 256
	maxConvQuantBits = 31
)

// The modes.
const (
	modeClassic = iota
	modeIntMult
	modeFloatMult
	modeFloatQuant
	modeDict
)

// The delta encodings.
const (
	deltaNone = iota
	deltaConsecutive
	deltaLookback
	deltaConv1
)

// Header is the header of a file: the version of the format.
type Header struct{ Major, Minor int }

// ReadHeader reads the header from b: one byte, then a second one from major version 4. It
// returns the number of bytes it took. Versions before 3 are refused: they code the delta
// encoding differently, and no Vortex writer has produced them.
func ReadHeader(b []byte) (Header, int, error) {
	if len(b) == 0 {
		return Header{}, 0, corrupt("empty header")
	}
	h := Header{Major: int(b[0])}
	n := 1
	if h.Major >= 4 {
		if len(b) < 2 {
			return h, 0, corrupt("header of 1 byte, version 4 has 2")
		}
		h.Minor, n = int(b[1]), 2
	}
	switch {
	case h.Major > 4:
		return h, 0, corrupt("format version %d.%d is newer than the 4.1 this reader knows", h.Major, h.Minor)
	case h.Major < 3:
		return h, 0, corrupt("format version %d is older than 3, which is not supported", h.Major)
	}
	return h, n, nil
}

// latentVar is what a chunk says about one latent variable: its tANS table and its bins.
type latentVar struct {
	nodes  []ansNode
	lowers []uint64 // per tANS state, the lower bound of its bin
	bins   []uint64 // per bin, its lower bound
	mask   uint64
	bits   int // width of its latents
	ansLog int
	nBins  int
}

// ansNode is a state of the tANS decoder.
type ansNode struct {
	next    uint32 // first next state
	offBits uint8  // the offset bits of the bin of the state
	toRead  uint8  // the bits to read to choose the next state
}

// delta is the delta encoding of a chunk.
type delta struct {
	weights   []int64
	bias      int64
	kind      int
	order     int // consecutive
	windowLog int // lookback
	stateLog  int
	quant     int  // conv1
	secondary bool // the secondary latent is delta encoded too
}

// Chunk is the metadata of a chunk, ready to decode its pages.
type Chunk struct {
	// vars is the delta, primary and secondary latent variables, nil when absent.
	vars  [3]*latentVar
	dict  []uint64
	delta delta
	base  uint64 // IntMult and FloatMult
	mode  int
	k     int // FloatQuant
	typ   Type
}

const (
	varDelta = iota
	varPrimary
	varSecondary
)

func bitLen(v int) int { return bits.Len(uint(v)) }

// Chunk reads the metadata of a chunk of numbers of type t.
func (h Header) Chunk(t Type, meta []byte) (*Chunk, error) {
	r := newBitReader(meta)
	c := &Chunk{typ: t}
	nb := t.Bits()
	if err := c.readMode(r, nb); err != nil {
		return nil, err
	}
	if err := c.readDelta(r); err != nil {
		return nil, err
	}
	var err error
	if c.delta.kind == deltaLookback {
		if c.vars[varDelta], err = readLatentVar(r, 32); err != nil {
			return nil, err
		}
	}
	pb := nb
	if c.mode == modeDict {
		pb = 32
	}
	if c.vars[varPrimary], err = readLatentVar(r, pb); err != nil {
		return nil, err
	}
	if c.mode != modeClassic && c.mode != modeDict {
		if c.vars[varSecondary], err = readLatentVar(r, nb); err != nil {
			return nil, err
		}
	}
	r.align("chunk metadata")
	if r.err != nil {
		return nil, r.err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Chunk) readMode(r *bitReader, nb int) error {
	c.mode = int(r.read(4))
	switch c.mode {
	case modeClassic:
	case modeIntMult, modeFloatMult:
		c.base = r.read(nb)
	case modeFloatQuant:
		c.k = int(r.read(8))
	case modeDict:
		n := int(r.read(25))
		r.align("dictionary length")
		if r.err != nil {
			return r.err
		}
		if n*nb > r.left() {
			return corrupt("a dictionary of %d values, the input has %d bits left", n, r.left())
		}
		c.dict = make([]uint64, n)
		for i := range c.dict {
			c.dict[i] = r.read(nb)
		}
	default:
		return corrupt("unknown mode %d", c.mode)
	}
	return r.err
}

func (c *Chunk) readDelta(r *bitReader) error {
	d := &c.delta
	d.kind = int(r.read(4))
	switch d.kind {
	case deltaNone:
	case deltaConsecutive:
		d.order = int(r.read(3))
		d.secondary = r.read(1) == 1
		if d.order == 0 && r.err == nil {
			return corrupt("consecutive delta encoding of order 0")
		}
	case deltaLookback:
		d.windowLog = 1 + int(r.read(5))
		d.stateLog = int(r.read(4))
		d.secondary = r.read(1) == 1
		if d.windowLog > maxLookbackLog {
			return corrupt("lookback window of 2^%d, at most 2^%d", d.windowLog, maxLookbackLog)
		}
		if d.stateLog > d.windowLog {
			return corrupt("lookback state of 2^%d, larger than its window of 2^%d", d.stateLog, d.windowLog)
		}
	case deltaConv1:
		d.quant = int(r.read(5))
		d.bias = int64(r.read(64) ^ 1<<63)
		order := 1 + int(r.read(5))
		d.weights = make([]int64, order)
		for i := range d.weights {
			d.weights[i] = int64(int32(uint32(r.read(32)) ^ 1<<31))
		}
	default:
		return corrupt("unknown delta encoding %d", d.kind)
	}
	return r.err
}

// readLatentVar reads the tANS table and the bins of a latent variable of the given width.
func readLatentVar(r *bitReader, nb int) (*latentVar, error) {
	v := &latentVar{bits: nb, mask: 1<<nb - 1}
	if nb == 64 {
		v.mask = math.MaxUint64
	}
	v.ansLog = int(r.read(4))
	v.nBins = int(r.read(15))
	if r.err != nil {
		return nil, r.err
	}
	switch {
	case 1<<v.ansLog < v.nBins:
		return nil, corrupt("tANS size 2^%d is too small for %d bins", v.ansLog, v.nBins)
	case v.nBins == 1 && v.ansLog > 0:
		return nil, corrupt("1 bin but a tANS size of 2^%d", v.ansLog)
	case v.ansLog > maxANSBits:
		return nil, corrupt("tANS size 2^%d, at most 2^%d", v.ansLog, maxANSBits)
	}
	weights := make([]uint32, v.nBins)
	lowers := make([]uint64, v.nBins)
	offBits := make([]uint8, v.nBins)
	offWidth := bitLen(nb)
	for i := range weights {
		weights[i] = uint32(r.read(v.ansLog)) + 1
		lowers[i] = r.read(nb)
		ob := int(r.read(offWidth))
		if ob > nb {
			return nil, corrupt("%d offset bits in a %d bit type", ob, nb)
		}
		offBits[i] = uint8(ob)
		if r.err != nil {
			return nil, r.err
		}
	}
	v.bins = lowers
	return v, v.buildTable(weights, lowers, offBits)
}

// buildTable spreads the symbols over the tANS states (the spread_state_symbols algorithm of
// the reference) and derives the decoder from them.
func (v *latentVar) buildTable(weights []uint32, lowers []uint64, offBits []uint8) error {
	if len(weights) == 0 {
		weights = []uint32{1}
	}
	size := uint32(1) << v.ansLog
	var sum uint32
	for _, w := range weights {
		sum += w
	}
	if sum != size {
		return corrupt("the bin weights add up to %d, the tANS size is %d", sum, size)
	}
	stride := 3 * size / 5
	if stride%2 == 0 {
		stride++
	}
	symbols := make([]uint32, size)
	step := uint32(0)
	for sym, w := range weights {
		for range w {
			symbols[(stride*step)&(size-1)] = uint32(sym)
			step++
		}
	}
	next := slicesClone(weights) // x_s of the tANS paper
	v.nodes = make([]ansNode, size)
	v.lowers = make([]uint64, size)
	for state, sym := range symbols {
		base := next[sym]
		toRead := bits.LeadingZeros32(base) - bits.LeadingZeros32(size)
		var ob uint8
		if int(sym) < len(offBits) {
			ob, v.lowers[state] = offBits[sym], lowers[sym]
		}
		v.nodes[state] = ansNode{next: base<<toRead - size, offBits: ob, toRead: uint8(toRead)}
		next[sym]++
	}
	return nil
}

func slicesClone(s []uint32) []uint32 { return append([]uint32(nil), s...) }

// validate checks the chunk against its number type and the rules the reference applies after
// reading the metadata.
func (c *Chunk) validate() error {
	if err := c.validateMode(); err != nil {
		return err
	}
	d := &c.delta
	switch d.kind {
	case deltaLookback:
		window := uint64(1) << d.windowLog
		for _, l := range c.vars[varDelta].bins {
			if l < 1 || l > window {
				return corrupt("lookback bin with a lower bound of %d outside [1, %d]", l, window)
			}
		}
	case deltaConv1:
		return c.validateConv()
	}
	return nil
}

// validateMode checks the mode against the number type.
func (c *Chunk) validateMode() error {
	t := c.typ
	switch c.mode {
	case modeIntMult:
		if t.Float() || c.base == 0 {
			return corrupt("invalid int mult mode for %s", t)
		}
	case modeFloatMult:
		if !t.Float() || !validFloatBase(t, c.base) {
			return corrupt("invalid float mult mode for %s", t)
		}
	case modeFloatQuant:
		if !t.Float() || c.k < 1 || c.k > mantissaBits(t) {
			return corrupt("invalid float quant mode for %s", t)
		}
	}
	return nil
}

// validateConv checks the Conv1 delta encoding against its latent type.
func (c *Chunk) validateConv() error {
	d := &c.delta
	pb := c.vars[varPrimary].bits
	if pb == 64 {
		return corrupt("conv1 delta encoding on 64 bit latents")
	}
	if d.quant > min(maxConvQuantBits, 2*pb-1) {
		return corrupt("conv1 quantization of %d", d.quant)
	}
	var sum float64
	for _, w := range d.weights {
		sum += math.Abs(float64(w))
	}
	if risk := math.Abs(float64(d.bias)) + math.Pow(2, float64(pb))*sum; risk >= math.Pow(2, float64(2*pb-1)) {
		return corrupt("conv1 weights and bias risk overflowing, as high as %g", risk)
	}
	return nil
}
