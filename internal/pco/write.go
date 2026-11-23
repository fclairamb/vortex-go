package pco

import (
	"encoding/binary"
	"math"
	"math/bits"
	"slices"
)

// The write side of the package: a Coder turns the numbers of a column into the chunks of the
// wrapped format that the reader (Header.Chunk, Chunk.Page) takes back.
//
// The stream is valid Pco, which any reader of the format decodes to the same numbers. It is not
// the stream of the pco crate (Apache-2.0, https://github.com/pcodec/pcodec), whose search is
// larger: this encoder keeps from it the classic and float-multiplier modes, the consecutive
// deltas of order 1 and 2, the bins found by sorting the latents and cutting them where the
// total of the entropy of the bins and of their offset bits is the smallest, and the tANS
// tables of the format.

// ChunkN is the most numbers a chunk holds, as in the crate (DEFAULT_MAX_PAGE_N).
const ChunkN = 1 << 18

const (
	level       = 8          // the default compression level of the crate
	maxBins     = 1 << level // the bins the histogram starts from
	sampleRuns  = 4          // the runs of the sample a mode is chosen on
	sampleRunN  = 1024       // the numbers of a run
	maxOrder    = 2          // the highest consecutive delta order tried
	floatTol    = 16         // the units in the last place that a multiple of the base may be off
	headerMajor = 4          // the format version written
	headerMinor = 1
	maxANSEst   = level + 2 // the tANS size the bins are optimized for
)

// WriteHeader appends the header of a stream, written once before the chunks.
func WriteHeader(dst []byte) []byte { return append(dst, headerMajor, headerMinor) }

// Coded is a chunk: its metadata, its pages, and the numbers of each page.
type Coded struct {
	Meta  []byte
	Pages [][]byte
	N     []int
}

// grow returns s with n elements, kept when it is large enough. What it holds is not cleared.
func grow[E any](s []E, n int) []E {
	if cap(s) < n {
		return make([]E, n, n+n/2)
	}
	return s[:n]
}

// bitWriter writes a little-endian bit stream.
type bitWriter struct {
	buf []byte
	acc uint64
	n   int // bits in acc
}

func (w *bitWriter) write(v uint64, n int) {
	if n == 0 {
		return
	}
	if n < 64 {
		v &= 1<<n - 1
	}
	w.acc |= v << uint(w.n)
	w.n += n
	if w.n >= 64 {
		w.buf = binary.LittleEndian.AppendUint64(w.buf, w.acc)
		w.n -= 64
		w.acc = 0
		if w.n > 0 {
			w.acc = v >> uint(n-w.n)
		}
	}
}

// align pads to the next byte with zeros.
func (w *bitWriter) align() {
	for i := 0; i < w.n; i += 8 {
		w.buf = append(w.buf, byte(w.acc>>uint(i)))
	}
	w.acc, w.n = 0, 0
}

// plan is how a chunk is coded.
type plan struct {
	base  uint64 // the base of the float multiplier mode, as a latent
	bits  uint64 // and as the bits of a float
	order int    // the consecutive delta order, 0 for none
	mult  bool
}

// table is a latent variable of a chunk: its bins, and the tANS table they give.
type table struct {
	lowers  []uint64
	offBits []uint8
	weights []uint32
	counts  []int
	fw      []float64
	cnt     []uint32
	sym     []uint32 // per state
	start   []int32  // per symbol, where its states start in byBase
	byBase  []uint32 // the states of each symbol, in the order of the table
	// index is, for each of the binBuckets parts of the range of the lowers, the bin of its
	// first value: the bin of a latent is looked for among the bins of its part.
	index      [binBuckets + 1]int32
	indexShift uint
	ansLog     int
}

// varEnc is a table and the symbols of the latents of a page.
type varEnc struct {
	ansV []uint32
	offV []uint64
	ansN []uint8
	offN []uint8
	table
	init [interleaving]uint32
}

type seg struct{ start, n int }

// Coder codes numbers. The memory it uses is kept from one call to the next.
type Coder struct {
	w       bitWriter
	out     []byte
	marks   []int
	coded   []Coded
	pages   [][]byte
	pageN   []int
	prim    []uint64
	sec     []uint64
	state   []uint64
	sorted  []uint64
	sortTmp []uint64
	segs    []seg
	sample  []seg
	// the training of the bins
	upos []int
	loV  []uint64  // loV[j] is the latent at cut j: the low value of the bin that starts there
	logs []float64 // logs[k] is the base 2 logarithm of k, for the counts of a bin
	dp   []float64
	from []int32
	cuts []int32
	vars [2]varEnc
	t    Type
}

// Compress codes the numbers vals, the bits of numbers of type t in their low bits, in chunks
// of at most ChunkN numbers and pages of about pageN. What it returns is valid until the next
// call.
func (c *Coder) Compress(t Type, vals []uint64, pageN int) []Coded {
	c.t = t
	c.out = c.out[:0]
	c.marks, c.pageN = c.marks[:0], c.pageN[:0]
	for start := 0; start < len(vals); start += ChunkN {
		c.chunk(vals[start:min(start+ChunkN, len(vals))], pageN)
	}
	c.coded, c.pages = c.coded[:0], c.pages[:0]
	pos, m, p := 0, 0, 0
	for m < len(c.marks) {
		k := c.marks[m] // the pages of the chunk
		meta := c.out[pos:c.marks[m+1]]
		pos = c.marks[m+1]
		first := len(c.pages)
		for i := range k {
			c.pages = append(c.pages, c.out[pos:c.marks[m+2+i]:c.marks[m+2+i]])
			pos = c.marks[m+2+i]
		}
		c.coded = append(c.coded, Coded{Meta: meta[:len(meta):len(meta)], Pages: c.pages[first:], N: c.pageN[p : p+k]})
		m += 2 + k
		p += k
	}
	return c.coded
}

// toLatent is the order-preserving latent of the bits of a number.
func (t Type) toLatent(b uint64) uint64 {
	switch {
	case t.Float():
		return t.floatToOrdered(b)
	case t.Signed():
		return b ^ t.mid()
	}
	return b
}

func (c *Coder) chunk(vals []uint64, pageN int) {
	t, n := c.t, len(vals)
	pages := (n + pageN - 1) / pageN
	c.segs = c.segs[:0]
	for i := range pages {
		size := n / pages
		if i < n%pages {
			size++
		}
		start := 0
		if i > 0 {
			last := c.segs[i-1]
			start = last.start + last.n
		}
		c.segs = append(c.segs, seg{start, size})
	}
	pl := c.choose(vals, n/pages)
	c.split(pl, vals, c.segs)
	nb := t.Bits()
	c.sorted = append(c.sorted[:0], c.prim...)
	c.sorted, c.sortTmp = sortLatents(c.sorted, c.sortTmp)
	c.train(&c.vars[0].table, c.sorted, nb)
	c.vars[0].indexBins()
	if pl.mult {
		c.sorted = append(c.sorted[:0], c.sec...)
		c.sorted, c.sortTmp = sortLatents(c.sorted, c.sortTmp)
		c.train(&c.vars[1].table, c.sorted, nb)
		c.vars[1].indexBins()
	}
	c.w = bitWriter{buf: c.out}
	c.meta(pl)
	c.w.align()
	c.out = c.w.buf
	c.marks = append(c.marks, pages, len(c.out))
	pOff, sOff := 0, 0
	for i, s := range c.segs {
		cnt := s.n - pl.order
		c.page(pl, i, s.n, c.prim[pOff:pOff+cnt], c.sec[min(sOff, len(c.sec)):min(sOff+s.n, len(c.sec))])
		c.marks = append(c.marks, len(c.out))
		c.pageN = append(c.pageN, s.n)
		pOff += cnt
		sOff += s.n
	}
}

// choose is the plan of a chunk, the one that codes a sample of it in the fewest bits.
func (c *Coder) choose(vals []uint64, pageMin int) plan {
	t, n := c.t, len(vals)
	segs := c.sample[:0]
	if n > sampleRuns*sampleRunN {
		for i := range sampleRuns {
			segs = append(segs, seg{i * (n - sampleRunN) / (sampleRuns - 1), sampleRunN})
		}
	} else {
		segs = append(segs, seg{0, n})
	}
	hi := min(maxOrder, pageMin-1)
	best, bestCost := plan{}, math.Inf(1)
	try := func(p plan) {
		// a plan has to be 1% smaller to win: the estimate is a sample, and a tie is the simpler plan
		if cost := c.cost(p, vals, segs); cost < bestCost*0.99 {
			best, bestCost = p, cost
		}
	}
	for o := 0; o <= hi; o++ {
		try(plan{order: o})
	}
	if t.Float() {
		if bb, ok := c.findBase(vals, segs); ok {
			for o := 0; o <= hi; o++ {
				try(plan{order: o, mult: true, bits: bb, base: t.floatToOrdered(bb)})
			}
		}
	}
	c.sample = segs
	return best
}

// cost is the bits that the sample segs takes under the plan p.
func (c *Coder) cost(p plan, vals []uint64, segs []seg) float64 {
	nb := c.t.Bits()
	c.split(p, vals, segs)
	c.sorted = append(c.sorted[:0], c.prim...)
	c.sorted, c.sortTmp = sortLatents(c.sorted, c.sortTmp)
	total, _ := c.estimate(c.sorted, nb)
	if p.mult {
		c.sorted = append(c.sorted[:0], c.sec...)
		c.sorted, c.sortTmp = sortLatents(c.sorted, c.sortTmp)
		second, _ := c.estimate(c.sorted, nb)
		total += second
	}
	return total
}

// split fills prim and sec with the latents of the segments, and state with their delta states.
func (c *Coder) split(p plan, vals []uint64, segs []seg) {
	t := c.t
	mask, mid := t.mask(), t.mid()
	c.prim, c.sec, c.state = c.prim[:0], c.sec[:0], c.state[:0]
	for _, s := range segs {
		base := len(c.prim)
		for _, x := range vals[s.start : s.start+s.n] {
			if p.mult {
				l, adj := multLatents(t, p.bits, x)
				c.prim = append(c.prim, l)
				c.sec = append(c.sec, adj)
			} else {
				c.prim = append(c.prim, t.toLatent(x))
			}
		}
		a := c.prim[base:]
		for range p.order {
			c.state = append(c.state, a[0])
			for i := 0; i+1 < len(a); i++ {
				a[i] = (a[i+1] - a[i]) & mask
			}
			a = a[:len(a)-1]
		}
		if p.order > 0 {
			for i := range a {
				a[i] = (a[i] + mid) & mask
			}
		}
		c.prim = c.prim[:base+len(a)]
	}
}

// toFloat64 is the float of the bits of a number of type t.
func toFloat64(t Type, b uint64) float64 {
	switch t {
	case F16:
		return float64(f16ToF32(uint16(b)))
	case F32:
		return float64(math.Float32frombits(uint32(b)))
	}
	return math.Float64frombits(b)
}

// fromFloat64 is the bits of the float of type t nearest to f.
func fromFloat64(t Type, f float64) uint64 {
	switch t {
	case F16:
		return uint64(f32ToF16(float32(f)))
	case F32:
		return uint64(math.Float32bits(float32(f)))
	}
	return math.Float64bits(f)
}

// multLatents splits the float x (as bits) over the base bb: the latent of the integer k that
// is nearest to x/base, and the adjustment that takes k*base to x.
func multLatents(t Type, bb, x uint64) (l, adj uint64) {
	mid, mant := t.mid(), mantissaBits(t)
	k := 0.0
	if q := toFloat64(t, x) / toFloat64(t, bb); !math.IsNaN(q) && !math.IsInf(q, 0) {
		k = math.Round(q)
	}
	abs := fromFloat64(t, math.Abs(k))
	if exp := (uint64(1)<<(t.Bits()-1-mant) - 1) << mant; abs&exp == exp { // too large for the type
		k, abs = 0, 0
	}
	a := uint64(math.Abs(k))
	if gpi := uint64(1) << (mant + 1); abs >= numericalBits(t, gpi) {
		a = abs - numericalBits(t, gpi) + gpi
	}
	l = mid | a
	if k < 0 {
		l = mid - 1 - a
	}
	approx := floatMul(t, intFloatFromLatent(t, l), bb)
	return l, (t.floatToOrdered(x) - t.floatToOrdered(approx) - mid) & t.mask()
}

// findBase looks for a decimal base, 1, 0.1, 0.01..., that the numbers of the sample are all
// multiples of, give or take a few units in the last place: the first one that is.
func (c *Coder) findBase(vals []uint64, segs []seg) (uint64, bool) {
	t := c.t
	mask, mid, sh := t.mask(), t.mid(), uint(64-t.Bits())
	for d := 0; d <= 3*mantissaBits(t)/10; d++ {
		bb := fromFloat64(t, math.Pow10(-d))
		ok := true
		for _, s := range segs {
			for _, x := range vals[s.start : s.start+s.n] {
				if f := toFloat64(t, x); math.IsNaN(f) || math.IsInf(f, 0) {
					continue
				}
				_, adj := multLatents(t, bb, x)
				if abs64(int64(((adj-mid)&mask)<<sh)>>sh) > floatTol {
					ok = false
					break
				}
			}
			if !ok {
				break
			}
		}
		if ok {
			return bb, true
		}
	}
	return 0, false
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// lower is the first index of s whose value is at least x, upper the first one above x.
func lower(s []uint64, x uint64) int {
	lo, hi := 0, len(s)
	for lo < hi {
		if mid := (lo + hi) / 2; s[mid] < x {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func upper(s []uint64, x uint64) int {
	lo, hi := 0, len(s)
	for lo < hi {
		if mid := (lo + hi) / 2; s[mid] <= x {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// unoptimized cuts the sorted latents s into bins of about the same number of latents, kept
// whole around the values that repeat: their bounds are in upos.
func (c *Coder) unoptimized(s []uint64) {
	m := len(s)
	c.upos = append(c.upos[:0], 0)
	last := 0
	for k := 1; k < maxBins; k++ {
		p := k * m / maxBins
		if p <= last || p >= m {
			continue
		}
		if s[p-1] == s[p] {
			a, b := lower(s, s[p]), upper(s, s[p])
			if a > last && p-a <= b-p {
				p = a
			} else {
				p = b
			}
			if p <= last || p >= m {
				continue
			}
		}
		c.upos = append(c.upos, p)
		last = p
	}
	c.upos = append(c.upos, m)
}

// estimate is the fewest bits the sorted latents s are estimated to take, and the tANS size
// the bins are optimized for: the partition of the cuts of unoptimized that the dynamic
// program finds. It builds no table, which a plan is chosen without; train makes the table
// of the partition it leaves in upos and from.
func (c *Coder) estimate(s []uint64, nb int) (float64, int) {
	m := len(s)
	nLog := 0
	if m > 1 {
		nLog = bits.Len(uint(m - 1))
	}
	est := min(maxANSEst, nLog)
	meta := float64(est + nb + bits.Len(uint(nb)))
	c.unoptimized(s)
	// the cut of those that takes the fewest bits
	nu := len(c.upos) - 1
	c.dp, c.from = grow(c.dp, nu+1), grow(c.from, nu+1)
	c.dp[0] = 0
	lm := math.Log2(float64(m))
	for k := len(c.logs); k <= m; k++ { // a count is at most m: its logarithm is computed once
		c.logs = append(c.logs, math.Log2(float64(k)))
	}
	logs := c.logs
	upos := c.upos
	dp := c.dp
	// the low value of each cut is read once, not looked up again for every pair of cuts
	c.loV = grow(c.loV, nu)
	loV := c.loV
	for j := 0; j < nu; j++ {
		loV[j] = s[upos[j]]
	}
	for i := 1; i <= nu; i++ {
		ui := upos[i]
		hiV := s[ui-1]
		best, from := math.Inf(1), 0
		for j := 0; j < i; j++ {
			n := ui - upos[j]
			cost := dp[j] + float64(n)*(lm-logs[n]+float64(bits.Len64(hiV-loV[j]))) + meta
			if cost < best {
				best, from = cost, j
			}
		}
		dp[i], c.from[i] = best, int32(from)
	}
	return dp[nu], est
}

// train makes the bins and the tANS table of the sorted latents s, and returns the bits they
// are estimated to take.
func (c *Coder) train(tb *table, s []uint64, nb int) float64 {
	nBits, est := c.estimate(s, nb)
	m := len(s)
	nu := len(c.upos) - 1
	c.cuts = c.cuts[:0]
	for i := nu; i > 0; i = int(c.from[i]) {
		c.cuts = append(c.cuts, int32(i))
	}
	slices.Reverse(c.cuts)
	nBins := len(c.cuts)
	tb.lowers, tb.offBits = grow(tb.lowers, nBins), grow(tb.offBits, nBins)
	tb.counts = grow(tb.counts, nBins)
	prev := 0
	for b, cut := range c.cuts {
		lo, hi := c.upos[prev], c.upos[cut]
		tb.lowers[b] = s[lo]
		tb.offBits[b] = uint8(bits.Len64(s[hi-1] - s[lo]))
		tb.counts[b] = hi - lo
		prev = int(cut)
	}
	tb.quantize(m, est)
	tb.build()
	return nBits
}

// quantize turns the counts of the bins into the weights of a tANS table of 2^est states, more
// when there are more bins, fewer when every weight is a multiple of a power of two.
func (tb *table) quantize(total, est int) {
	nBins := len(tb.counts)
	tb.weights = grow(tb.weights, nBins)
	if nBins == 1 {
		tb.weights[0], tb.ansLog = 1, 0
		return
	}
	lg := max(bits.Len(uint(nBins-1)), est)
	size := 1 << lg
	mult := float64(size) / float64(total)
	tb.fw = grow(tb.fw, nBins)
	desired := 0.0
	for i, n := range tb.counts {
		tb.fw[i] = max(float64(n)*mult-1, 0)
		desired += tb.fw[i]
	}
	surplus := 0.0
	if desired > 0 {
		surplus = float64(size-nBins) / desired
	}
	sum := 0
	for i := range tb.fw {
		tb.fw[i] = 1 + tb.fw[i]*surplus
		tb.weights[i] = uint32(math.Round(tb.fw[i]))
		sum += int(tb.weights[i])
	}
	w := tb.weights
	for i := 0; sum > size; i++ {
		if k := i % nBins; w[k] > 1 && (i >= 2*nBins || float64(w[k]) > tb.fw[k]) {
			w[k]--
			sum--
		}
	}
	for i := 0; sum < size; i++ {
		if k := i % nBins; i >= 2*nBins || float64(w[k]) < tb.fw[k] {
			w[k]++
			sum++
		}
	}
	tz := 32
	for _, x := range w {
		tz = min(tz, bits.TrailingZeros32(x))
	}
	for i := range w {
		w[i] >>= tz
	}
	tb.ansLog = lg - tz
}

// build derives the states of the tANS table from the weights: for each symbol, its states
// in the order of the table, which the encoder walks backwards.
func (tb *table) build() {
	size := 1 << tb.ansLog
	nBins := len(tb.weights)
	tb.sym = grow(tb.sym, size)
	spreadSymbols(tb.sym, tb.weights)
	tb.start = grow(tb.start, nBins+1)
	tb.start[0] = 0
	for sym, w := range tb.weights {
		tb.start[sym+1] = tb.start[sym] + int32(w)
	}
	// cnt is where the next state of each symbol goes
	tb.cnt = grow(tb.cnt, nBins)
	for sym := range tb.cnt {
		tb.cnt[sym] = uint32(tb.start[sym])
	}
	tb.byBase = grow(tb.byBase, size)
	for state, sym := range tb.sym {
		tb.byBase[tb.cnt[sym]] = uint32(state)
		tb.cnt[sym]++
	}
}

// binBuckets is the number of parts the range of the lowers of a table is cut in.
const binBuckets = 256

// indexBins fills the index of the bins: the bin of the first value of each part of the range
// of the lowers, and one more that is the last bin.
func (tb *table) indexBins() {
	lo, last := tb.lowers[0], len(tb.lowers)-1
	tb.indexShift = uint(max(bits.Len64(tb.lowers[last]-lo)-bits.Len(binBuckets-1), 0))
	bin := 0
	for b := range binBuckets {
		// counted from the first lower: the end of the range may be past the largest number
		first := uint64(b) << tb.indexShift
		for bin < last && tb.lowers[bin+1]-lo <= first {
			bin++
		}
		tb.index[b] = int32(bin)
	}
	tb.index[binBuckets] = int32(last)
}

// bin is the index of the bin of x, a latent the bins were made from: the last bin whose
// lower is at most x, looked for among the bins of the part of the range x is in.
func (tb *table) bin(x uint64) int {
	b := min((x-tb.lowers[0])>>tb.indexShift, binBuckets-1)
	lo, hi := int(tb.index[b]), int(tb.index[b+1])
	for lo < hi {
		if mid := (lo + hi + 1) / 2; tb.lowers[mid] <= x {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// radixMin is the number of latents from which they are sorted a byte at a time.
const radixMin = 1 << 10

// sortLatents sorts s and returns it, with tmp, a buffer of the same size that it used: the
// two may have been swapped. Many latents are sorted a byte at a time, from the low one, and
// only on the bytes that are not the same in all of them: the latents of a chunk are close.
func sortLatents(s, tmp []uint64) (sorted, buf []uint64) {
	if len(s) < radixMin {
		slices.Sort(s)
		return s, tmp
	}
	tmp = grow(tmp, len(s))
	var diff uint64
	for _, x := range s {
		diff |= x ^ s[0]
	}
	var count [256]int
	for shift := uint(0); shift < 64; shift += 8 {
		if diff>>shift&0xff == 0 {
			continue
		}
		count = [256]int{}
		for _, x := range s {
			count[x>>shift&0xff]++
		}
		pos := 0
		for i := range count {
			count[i], pos = pos, pos+count[i]
		}
		for _, x := range s {
			d := x >> shift & 0xff
			tmp[count[d]] = x
			count[d]++
		}
		s, tmp = tmp, s
	}
	return s, tmp
}

// meta writes the metadata of a chunk.
func (c *Coder) meta(p plan) {
	w := &c.w
	nb := c.t.Bits()
	if p.mult {
		w.write(modeFloatMult, 4)
		w.write(p.base, nb)
	} else {
		w.write(modeClassic, 4)
	}
	if p.order > 0 {
		w.write(deltaConsecutive, 4)
		w.write(uint64(p.order), 3)
		w.write(0, 1)
	} else {
		w.write(deltaNone, 4)
	}
	c.writeVar(&c.vars[0].table)
	if p.mult {
		c.writeVar(&c.vars[1].table)
	}
}

func (c *Coder) writeVar(tb *table) {
	w, nb := &c.w, c.t.Bits()
	w.write(uint64(tb.ansLog), 4)
	w.write(uint64(len(tb.weights)), 15)
	for i, wt := range tb.weights {
		w.write(uint64(wt-1), tb.ansLog)
		w.write(tb.lowers[i], nb)
		w.write(uint64(tb.offBits[i]), bits.Len(uint(nb)))
	}
}

// symbols finds the symbols of the latents lat of a page, and the states of the tANS coder: it
// goes through the latents of each of its lanes backwards, from any state, choosing for each
// the state of its bin that leads to the next one. What the decoder reads is then the bits that
// say where in the range of the state it goes to.
func (v *varEnc) symbols(lat []uint64) {
	n := len(lat)
	v.ansV, v.ansN = grow(v.ansV, n), grow(v.ansN, n)
	v.offV, v.offN = grow(v.offV, n), grow(v.offN, n)
	size := uint32(1) << v.ansLog
	for lane := range interleaving {
		to := uint32(0)
		if lane < n {
			for i := lane + (n-1-lane)/interleaving*interleaving; i >= 0; i -= interleaving {
				b := v.bin(lat[i])
				// The decoder goes to the state to from the state of the bin whose range
				// holds it: the one whose base is the high bits of to, as many as make a
				// number from the weight of the bin to twice it. The low bits are what
				// the decoder reads.
				w, x := v.weights[b], to+size
				k := bits.Len32(x) - bits.Len32(w)
				if x>>k < w {
					k--
				}
				st := v.byBase[v.start[b]+int32(x>>k-w)]
				v.ansV[i], v.ansN[i] = x&(1<<k-1), uint8(k)
				v.offV[i], v.offN[i] = lat[i]-v.lowers[b], v.offBits[b]
				to = st
			}
		}
		v.init[lane] = to
	}
}

// page writes the page i, of n numbers: the bodies of its latent variables.
func (c *Coder) page(p plan, i, n int, prim, sec []uint64) {
	nv := 1
	body := [2][]uint64{prim, sec}
	if p.mult {
		nv = 2
	}
	for k := range nv {
		c.vars[k].symbols(body[k])
	}
	w := &c.w
	w.buf = c.out
	nb := c.t.Bits()
	for _, s := range c.state[i*p.order : (i+1)*p.order] {
		w.write(s, nb)
	}
	for k := range nv {
		for _, s := range c.vars[k].init {
			w.write(uint64(s), c.vars[k].ansLog)
		}
	}
	w.align()
	for start := 0; start < n; start += batchN {
		for k := range nv {
			v := &c.vars[k]
			end := min(start+batchN, len(body[k]))
			for j := start; j < end; j++ {
				w.write(uint64(v.ansV[j]), int(v.ansN[j]))
			}
			for j := start; j < end; j++ {
				w.write(v.offV[j], int(v.offN[j]))
			}
		}
	}
	w.align()
	c.out = w.buf
}
