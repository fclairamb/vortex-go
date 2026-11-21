package vortex

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/fclairamb/vortex-go/internal/array"
)

// The gaps under which two reads are merged into one, and the largest merged read. A local
// reader pays little per call, so only close ranges are merged; any other one pays a round
// trip per call, so reading the bytes between two ranges is cheaper.
const (
	localGap     = 4 << 10
	localMaxRead = 1 << 20
	remoteGap    = 1 << 20
	remoteMax    = 16 << 20
)

// mergeLimits are the gap and the largest merged read for r.
func mergeLimits(r io.ReaderAt) (gap, maxRead int64) {
	switch r.(type) {
	case *os.File, *bytes.Reader, *strings.Reader:
		return localGap, localMaxRead
	}
	return remoteGap, remoteMax
}

// unit is a segment of a scan: a flat that the loader reads and decodes, on the goroutine
// of the scan or ahead of it.
type unit struct {
	fl    *flat
	ld    *loader
	ready chan struct{} // closed once arr and err are set
	arr   *array.Array
	err   error
	buf   *ioBuf       // the buffer of the read, released when the unit is dropped
	arena *array.Arena // the buffers of arr, released with buf
	open  *array.Rows  // the segment, opened, whose rows the scan decodes
	seg   []byte       // the segment, in buf, of a unit that was read and not decoded
	mu    sync.Mutex
	start int // the row of the file the flat starts at
	idx   int // the place of the unit in the order of the scan
	state unitState
	drop  bool // the scan no longer needs the unit
	// late is set for a column that a filter does not read: when the filter keeps few rows,
	// the unit is read ahead and not decoded, and the scan decodes the rows kept.
	late bool
	// part is set when the scan reads a part of the rows of the segment only: the unit is read
	// ahead and not decoded, and the scan decodes the rows it reads.
	part bool
}

type unitState uint8

const (
	unitIdle unitState = iota // nobody reads it yet
	unitBusy                  // being read
	unitDone                  // arr and err are set
)

// claim reports whether the caller is the one to read the unit.
func (u *unit) claim() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.state != unitIdle || u.drop {
		return false
	}
	u.state = unitBusy
	return true
}

// finish stores the result of a read, the buffer b holding the segment.
func (u *unit) finish(b *ioBuf, seg []byte, ar *array.Arena, a *array.Array, err error) {
	u.mu.Lock()
	u.arr, u.seg, u.err, u.state = a, seg, err, unitDone
	drop := u.drop
	if !drop {
		u.buf, u.arena = b, ar
	}
	u.mu.Unlock()
	if drop {
		b.release()
		ar.Release()
	}
	close(u.ready)
}

// dropped reports whether the scan no longer needs the unit.
func (u *unit) dropped() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.drop
}

// release tells that the scan is done with the unit: its buffer goes back to the pool, now
// when it is read, or when its read ends.
func (u *unit) release() {
	u.mu.Lock()
	u.drop = true
	b, ar := u.buf, u.arena
	u.arr, u.seg, u.buf, u.arena = nil, nil, nil, nil
	u.mu.Unlock()
	if b != nil {
		b.release()
		ar.Release()
	}
}

// ioBuf is the buffer of a read, shared by the units it holds.
type ioBuf struct {
	pool *bufPool
	b    []byte
	refs atomic.Int32
}

func (b *ioBuf) release() {
	if b.refs.Add(-1) == 0 && b.pool != nil { // a view of a mapped file goes nowhere
		b.pool.put(b)
	}
}

// bufPool is the buffers of a scan. A buffer goes back to it when every unit it holds is done
// and the consumer has moved to the next batch.
type bufPool struct {
	free []*ioBuf
	mu   sync.Mutex
}

// get returns a buffer of n bytes, shared by refs units.
func (p *bufPool) get(n int, refs int32) *ioBuf {
	p.mu.Lock()
	var b *ioBuf
	if i := slices.IndexFunc(p.free, func(b *ioBuf) bool { return cap(b.b) >= n }); i >= 0 {
		b = p.free[i]
		p.free = slices.Delete(p.free, i, i+1)
	}
	p.mu.Unlock()
	if b == nil {
		b = &ioBuf{pool: p, b: make([]byte, n)}
	}
	b.b = b.b[:n]
	b.refs.Store(refs)
	return b
}

func (p *bufPool) put(b *ioBuf) {
	p.mu.Lock()
	p.free = append(p.free, b)
	p.mu.Unlock()
}

// loader reads and decodes the units of a scan in parallel, a bounded number of units ahead
// of the consumer. The consumer asks for the units one at a time with get; a unit nobody has
// started yet is read by the consumer itself, so the order the consumer takes them in never
// blocks it.
type loader struct {
	ctx      context.Context
	f        *File
	sem      chan struct{} // a token per running read
	progress chan struct{} // the consumer moved forward
	stop     chan struct{} // closed when the scan ends
	arrays   *array.Pool   // the buffers of the decoded arrays
	units    []*unit       // in the order of the scan
	retired  []*unit       // the units done with in the current batch
	pool     bufPool
	wg       sync.WaitGroup
	front    atomic.Int64 // the units the consumer has reached
	sparse   atomic.Bool  // the filter keeps few rows: the late units are not decoded ahead
	ahead    int          // the units read before the consumer reaches them
	gap      int64
	maxRead  int64
}

// newLoader plans the units of a scan: the flats of its readers, in the order of their first
// row, without the ones whose rows the filter rules out. It starts the reads ahead.
func newLoader(ctx context.Context, f *File, p *scanPlan, workers int) *loader {
	l := &loader{ctx: ctx, f: f, gap: f.gap, maxRead: f.maxRead, arrays: p.pool,
		sem: make(chan struct{}, workers), progress: make(chan struct{}, 1), stop: make(chan struct{})}
	// Until a batch says otherwise, a filter is taken to keep few rows: the units of the
	// columns it does not read are not decoded before the first batch asks for their rows.
	l.sparse.Store(p.wantFilter)
	var all []*unit
	p.src.units(int(p.lo), &all)
	for i, u := range all {
		u.idx = i // the order of creation, which is the order of the fields in a row
	}
	slices.SortStableFunc(all, func(a, b *unit) int { return a.start - b.start })
	width := 0
	for _, u := range all {
		if p.wantFilter && !p.filter.canMatch(int64(u.start), int64(u.start+u.fl.end-u.fl.pos)) {
			u.fl.u = nil
			continue
		}
		if u.start == all[0].start {
			width++
		}
		u.ld, u.idx, u.ready = l, len(l.units), make(chan struct{})
		l.units = append(l.units, u)
	}
	l.ahead = 2 * max(width, workers)
	if len(l.units) > 0 {
		l.wg.Add(1)
		go l.prefetch()
	}
	return l
}

// canMatch reports whether a row from lo to hi (excluded) may meet the filter.
func (n *node) canMatch(lo, hi int64) bool {
	for pos := lo; pos < hi; {
		b, limit := n.bounds(pos)
		if b.canTrue {
			return true
		}
		pos = limit
	}
	return false
}

// close stops the reads ahead and waits for them.
func (l *loader) close() {
	close(l.stop)
	l.wg.Wait()
}

// reach records that the consumer got to unit i.
func (l *loader) reach(i int) {
	for {
		cur := l.front.Load()
		if int64(i) < cur {
			return
		}
		if l.front.CompareAndSwap(cur, int64(i)+1) {
			break
		}
	}
	select {
	case l.progress <- struct{}{}:
	default:
	}
}

// get returns the array of unit u, read and decoded.
func (l *loader) get(u *unit) (*array.Array, error) {
	if err := l.wait(u); err != nil {
		return nil, err
	}
	if u.arr == nil && u.err == nil { // read ahead and not decoded: the scan wants every row
		ar := l.arrays.NewArena()
		a, err := u.fl.decode(u.seg, ar)
		u.mu.Lock()
		u.arr, u.err, u.arena = a, err, ar
		u.mu.Unlock()
	}
	return u.arr, u.err
}

// segment returns the bytes of the segment of unit u when it was read and not decoded, and
// nil when it is decoded.
func (l *loader) segment(u *unit) ([]byte, error) {
	if err := l.wait(u); err != nil || u.arr != nil {
		return nil, err
	}
	return u.seg, u.err
}

// wait reads unit u when nobody has, and waits for its read to end.
func (l *loader) wait(u *unit) error {
	l.reach(u.idx)
	if u.claim() {
		l.read(l.merge([]*unit{u})[0])
	}
	select {
	case <-u.ready:
		return nil
	case <-l.ctx.Done():
		return l.ctx.Err()
	}
}

// retire records that the scan is done with unit u once the current batch is: its buffer is
// reused only when the consumer has moved on.
func (l *loader) retire(u *unit) {
	l.reach(u.idx)
	l.retired = append(l.retired, u)
}

// recycle releases the units retired by the previous batch, and the rows their flats
// returned last.
func (l *loader) recycle() {
	for _, u := range l.retired {
		u.release()
		u.fl.slot = array.Array{}
	}
	l.retired = l.retired[:0]
	l.arrays.Recycle()
}

// prefetch claims the units ahead of the consumer, merges their reads and runs them.
func (l *loader) prefetch() {
	defer l.wg.Done()
	for next := 0; next < len(l.units); {
		limit := min(int(l.front.Load())+l.ahead, len(l.units))
		if next >= limit {
			select {
			case <-l.progress:
				continue
			case <-l.stop:
				return
			}
		}
		var group []*unit
		for _, u := range l.units[next:limit] {
			if u.claim() {
				group = append(group, u)
			}
		}
		next = limit
		for _, e := range l.merge(group) {
			select {
			case l.sem <- struct{}{}:
			case <-l.stop:
				return
			}
			l.wg.Add(1)
			go func() {
				defer l.wg.Done()
				l.read(e)
				<-l.sem
			}()
		}
	}
}

// lazy reports whether the segment seg of the unit is left as it is, for the scan to decode
// the rows it reads: the scan reads some of its rows only, and its encodings decode some rows
// only. A segment that cannot be opened is left to the scan too, which reports it.
func (u *unit) lazy(l *loader, seg []byte) bool {
	if !u.part && (!u.late || !l.sparse.Load()) {
		return false
	}
	open, err := array.OpenRows(seg, u.fl.f.arrayEncodings, u.fl.opt)
	if err == nil && !open.Can() {
		return false
	}
	u.open = open // read by the scan once the unit is ready
	return true
}

// extent is a read that covers the segments of some units.
type extent struct {
	units    []*unit
	off, end int64
}

// merge groups the segments of units into reads: by offset, merged when the gap between two
// is small and the read stays under the largest one.
func (l *loader) merge(units []*unit) []extent {
	slices.SortFunc(units, func(a, b *unit) int { return int(a.off() - b.off()) })
	var out []extent
	for _, u := range units {
		off, end := u.off(), u.off()+int64(u.fl.segLen())
		if n := len(out); n > 0 && off-out[n-1].end <= l.gap && max(end, out[n-1].end)-out[n-1].off <= l.maxRead {
			out[n-1].units = append(out[n-1].units, u)
			out[n-1].end = max(end, out[n-1].end)
			continue
		}
		out = append(out, extent{units: []*unit{u}, off: off, end: end})
	}
	return out
}

func (u *unit) off() int64 { return int64(u.fl.f.segments[u.fl.seg].Offset) }

// read runs one extent: one read, then the decoding of each of its units.
func (l *loader) read(e extent) {
	// The bytes sit in memory as they are aligned in the file: a buffer of numbers is then
	// read where it is, not copied.
	pad := e.off & 7
	var b *ioBuf
	var err error
	if m := l.f.mapped; m != nil {
		// A mapped file is read where it is: its bytes are aligned as they are in the file.
		b = &ioBuf{b: m.view(e.off-pad, e.end)}
		b.refs.Store(int32(len(e.units)))
	} else {
		b = l.pool.get(int(pad+e.end-e.off), int32(len(e.units)))
		err = readFull(l.f.r, b.b[pad:], e.off)
	}
	for _, u := range e.units {
		var a *array.Array
		var ar *array.Arena
		var seg []byte
		var uerr error
		switch {
		case err != nil:
			uerr = u.fl.ioErr(err)
		case !u.dropped():
			start := pad + u.off() - e.off
			seg = b.b[start : start+int64(u.fl.segLen())]
			if !u.lazy(l, seg) {
				ar = u.fl.opt.Pool.NewArena()
				a, uerr = u.fl.decode(seg, ar)
			}
		}
		u.finish(b, seg, ar, a, uerr)
	}
}

// workers is the number of reads a scan runs at once.
func workers() int { return runtime.GOMAXPROCS(0) }
