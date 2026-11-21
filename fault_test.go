package vortex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fclairamb/vortex-go/internal/testutil"
)

// digest scans f and returns a hash of its batches: their sizes and their values.
func digest(f *File, o *scanOptions) (string, error) {
	h := testutil.NewHasher()
	for b, err := range f.scan(context.Background(), o) {
		if err != nil {
			return h.Sum(), err
		}
		h.Len(b.Len())
		for k := range b.cols {
			for i := range b.Len() {
				hashRow(h, b.Col(k), i)
			}
		}
	}
	return h.Sum(), nil
}

// extraPath is the path of an extra fixture.
func extraPath(t *testing.T, name string) string {
	t.Helper()
	return mustGlob(t, filepath.Join("testdata", "fixtures", "*", "extra", name))[0]
}

// reopen opens the bytes of a file through r, made from the bytes.
func reopen(t *testing.T, raw []byte, r func(io.ReaderAt, int64) io.ReaderAt) *File {
	t.Helper()
	f, err := Open(r(bytes.NewReader(raw), int64(len(raw))), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func plain(r io.ReaderAt, _ int64) io.ReaderAt { return r }

// waitGoroutines waits until there are at most n goroutines, and fails after 5 seconds.
func waitGoroutines(t *testing.T, n int) {
	t.Helper()
	for start := time.Now(); runtime.NumGoroutine() > n; {
		if time.Since(start) > 5*time.Second {
			t.Fatalf("%d goroutines, %d before the scan", runtime.NumGoroutine(), n)
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
}

// A reader that fails at call N, for every N up to the reads of a scan: the scan yields one
// error, which wraps the cause, and stops.
func TestScanIOFaults(t *testing.T) {
	boom := errors.New("boom")
	for _, path := range corpus(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f := reopen(t, raw, plain)
		want, err := digest(f, &scanOptions{batchRows: 1000})
		if err != nil {
			continue // a fixture of an encoding still pending
		}
		count := &testutil.FailAt{R: f.r}
		f.r = count
		if _, err := digest(f, &scanOptions{batchRows: 1000}); err != nil {
			t.Fatal(err)
		}
		for n := int64(1); n <= count.Calls(); n++ {
			fail := &testutil.FailAt{R: count.R, N: n, Err: boom}
			f.r = fail
			errs := 0
			for _, err := range f.scan(context.Background(), &scanOptions{batchRows: 1000}) {
				if err != nil {
					errs++
					if !errors.Is(err, boom) || !strings.Contains(err.Error(), "offset") {
						t.Fatalf("%s, call %d: %v", fixtureName(path), n, err)
					}
				}
			}
			if fail.Calls() >= n && errs != 1 {
				t.Fatalf("%s, call %d of %d: %d errors", fixtureName(path), n, fail.Calls(), errs)
			}
		}
		// Open fails the same way, at each of its reads.
		for n := int64(1); ; n++ {
			fail := &testutil.FailAt{R: bytes.NewReader(raw), N: n, Err: boom}
			_, err := Open(fail, int64(len(raw)))
			if fail.Calls() < n {
				break
			}
			if !errors.Is(err, boom) {
				t.Fatalf("%s, open call %d: %v", fixtureName(path), n, err)
			}
		}
		// Short reads, and io.EOF with the last bytes, give what full reads give.
		for _, wrap := range []func(io.ReaderAt, int64) io.ReaderAt{
			func(r io.ReaderAt, _ int64) io.ReaderAt { return testutil.Short{R: r, Max: 777} },
			func(r io.ReaderAt, n int64) io.ReaderAt { return testutil.EOFAtEnd{R: r, Size: n} },
		} {
			if got, err := digest(reopen(t, raw, wrap), &scanOptions{batchRows: 1000}); err != nil || got != want {
				t.Errorf("%s: %v, another digest", fixtureName(path), err)
			}
		}
	}
}

// A cancelled context stops the scan with context.Canceled, even when a read never returns,
// and leaves no goroutine.
func TestScanCancelledWhileReading(t *testing.T) {
	before := runtime.NumGoroutine()
	f := reopen(t, extraFixture(t, "default_ints.vortex"), plain)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	f.r = testutil.Blocking{Ctx: ctx, Started: started}
	go func() {
		<-started
		cancel()
	}()
	errs := 0
	for _, err := range f.Scan(ctx, Query{}) {
		errs++
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if errs != 1 {
		t.Fatalf("%d errors", errs)
	}
	waitGoroutines(t, before)
}

// The consumer waits for a unit the loader reads, and the context is cancelled meanwhile.
func TestScanCancelledWhileWaiting(t *testing.T) {
	f := reopen(t, extraFixture(t, "default_ints.vortex"), plain)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := f.plan(&scanOptions{batchRows: maxBatchRows})
	if err != nil {
		t.Fatal(err)
	}
	reads, release := context.WithCancel(context.Background()) // the reads wait for the end of the test
	f.r = testutil.Blocking{Ctx: reads}
	ld := newLoader(ctx, f, p, 1)
	u := ld.units[0]
	for !func() bool { u.mu.Lock(); defer u.mu.Unlock(); return u.state == unitBusy }() {
		runtime.Gosched() // the loader claims it
	}
	cancel()
	if _, err := ld.get(u); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release()
	ld.close()
}

// Leaving the loop, by break or by a panic, stops every goroutine of the scan.
func TestScanBreakLeavesNoGoroutine(t *testing.T) {
	f := reopen(t, extraFixture(t, "default_ints.vortex"), plain)
	before := runtime.NumGoroutine()
	for _, err := range f.scan(context.Background(), &scanOptions{batchRows: 100}) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	waitGoroutines(t, before)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("no panic")
			}
		}()
		for range f.scan(context.Background(), &scanOptions{batchRows: 100}) {
			panic("in the body")
		}
	}()
	waitGoroutines(t, before)
}

// queries are scans of default_ints that differ by their columns, rows and filter.
func queries() []scanOptions {
	var out []scanOptions
	cols := [][]string{nil, {"sorted"}, {"low_card", "random"}, {"nullable", "bulk"}}
	filters := []Expr{{}, Col("random").Lt(0), Col("low_card").Eq(3000), Col("nullable").IsNull()}
	for i := range 16 {
		out = append(out, scanOptions{batchRows: 500 + 100*i, columns: cols[i%4],
			q: Query{Filter: filters[i/4], Rows: Range{Start: int64(i * 700), End: int64(30000 - i*300)}}})
	}
	return out
}

// Scans at once on one File give what they give one at a time.
func TestConcurrentScans(t *testing.T) {
	f := reopen(t, extraFixture(t, "default_ints.vortex"), plain)
	qs := queries()
	want := make([]string, len(qs))
	for i, q := range qs {
		var err error
		if want[i], err = digest(f, &q); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	got := make([]string, len(qs))
	errs := make([]error, len(qs))
	for i, q := range qs {
		wg.Go(func() { got[i], errs[i] = digest(f, &q) })
	}
	wg.Wait()
	for i := range qs {
		if errs[i] != nil || got[i] != want[i] {
			t.Errorf("query %d: %v, another result", i, errs[i])
		}
	}
}

// Closing the file during a scan gives an error, never a crash.
func TestCloseDuringScan(t *testing.T) {
	f, err := OpenFile(extraPath(t, "default_ints.vortex"))
	if err != nil {
		t.Fatal(err)
	}
	batches, errs := 0, 0
	for _, err := range f.scan(context.Background(), &scanOptions{batchRows: 1000}) {
		if err != nil {
			errs++
			if !errors.Is(err, os.ErrClosed) {
				t.Fatal(err)
			}
			continue
		}
		if batches++; batches == 1 {
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if batches != 1 || errs != 1 {
		t.Fatalf("%d batches, %d errors", batches, errs)
	}
}

// closingReader is a reader whose reads block until it is closed, then fail with
// os.ErrClosed, as those of an *os.File closed under them.
type closingReader struct {
	testutil.Blocking
	cancel context.CancelFunc
}

func (c closingReader) ReadAt(p []byte, off int64) (int, error) {
	_, _ = c.Blocking.ReadAt(p, off)
	return 0, os.ErrClosed
}

func (c closingReader) Close() error {
	c.cancel()
	return nil
}

// Closing the file while reads are in flight stops the scan with os.ErrClosed, and leaves no
// goroutine.
func TestCloseDuringReads(t *testing.T) {
	before := runtime.NumGoroutine()
	f := reopen(t, extraFixture(t, "default_ints.vortex"), plain)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 1)
	r := closingReader{Blocking: testutil.Blocking{Ctx: ctx, Started: started}, cancel: cancel}
	f.r, f.closer = r, r
	go func() {
		<-started
		_ = f.Close()
	}()
	batches, errs := 0, 0
	for _, err := range f.Scan(context.Background(), Query{}) {
		if err == nil {
			batches++
			continue
		}
		errs++
		if !errors.Is(err, os.ErrClosed) {
			t.Fatal(err)
		}
	}
	if batches != 0 || errs != 1 {
		t.Fatalf("%d batches, %d errors", batches, errs)
	}
	waitGoroutines(t, before)
}

// The batches come in the same order with the same content, whatever the parallelism.
func TestScanOrderWhateverTheCores(t *testing.T) {
	f := reopen(t, extraFixture(t, "default_ints.vortex"), plain)
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	var want []string
	for _, procs := range []int{1, 2, 8} {
		runtime.GOMAXPROCS(procs)
		var got []string
		for _, q := range queries() {
			d, err := digest(f, &q)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, d)
		}
		if want == nil {
			want = got
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("GOMAXPROCS %d: other batches", procs)
		}
	}
}

// A full scan merges the reads of neighbouring segments.
func TestScanMergesReads(t *testing.T) {
	f := reopen(t, extraFixture(t, "default_ints.vortex"), plain)
	log := &logReader{r: f.r}
	f.r = log
	if _, err := digest(f, &scanOptions{batchRows: maxBatchRows}); err != nil {
		t.Fatal(err)
	}
	if segs := log.segments(f); len(log.offs) >= segs {
		t.Errorf("%d reads for %d segments", len(log.offs), segs)
	}
	// A reader of another kind merges over larger gaps.
	if gap, _ := mergeLimits(log); gap != remoteGap {
		t.Errorf("gap %d", gap)
	}
}

// peakHeap scans f and returns the most heap in use at a batch, above what was in use before.
func peakHeap(t *testing.T, f *File) uint64 {
	t.Helper()
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	base, peak := m.HeapAlloc, uint64(0)
	for _, err := range f.Scan(context.Background(), Query{}) {
		if err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&m)
		peak = max(peak, m.HeapAlloc-min(base, m.HeapAlloc))
	}
	return peak
}

// maxScanHeap bounds the heap of a scan of the files below: a few chunks of every column.
const maxScanHeap = 96 << 20

// The memory of a scan does not depend on the size of the file.
func TestScanPeakHeap(t *testing.T) {
	for _, path := range []string{extraPath(t, "default_ints.vortex"), ".cache/big.vortex"} {
		if _, err := os.Stat(path); err != nil {
			t.Logf("%s: absent, `make bigfile` writes it", path)
			continue
		}
		f, err := OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if peak := peakHeap(t, f); peak > maxScanHeap {
			t.Errorf("%s: %d MiB of heap, at most %d", path, peak>>20, maxScanHeap>>20)
		} else {
			t.Logf("%s: %d rows, %d MiB of heap at most", path, f.NumRows(), peak>>20)
		}
		_ = f.Close()
	}
}

// The loader reads ahead a bounded number of units, then waits for the consumer; the end of
// the scan stops it there.
func TestLoaderWaitsForTheConsumer(t *testing.T) {
	f, err := open(sortedFile(100, 100, false))
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.plan(&scanOptions{batchRows: maxBatchRows})
	if err != nil {
		t.Fatal(err)
	}
	ld := newLoader(context.Background(), f, p, 1)
	if ld.ahead >= len(ld.units) {
		t.Fatalf("%d units ahead of %d", ld.ahead, len(ld.units))
	}
	for _, u := range ld.units[:ld.ahead] {
		<-u.ready
	}
	if u := ld.units[ld.ahead]; !u.claim() {
		t.Fatal("a unit past the read-ahead is read")
	}
	ld.close()
}

// A flat of more rows than maxFlatRows is corruption.
func TestFlatRowsLimit(t *testing.T) {
	f := reopen(t, extraFixture(t, "default_ints.vortex"), plain)
	defer func(n uint64) { maxFlatRows = n }(maxFlatRows)
	maxFlatRows = 100
	for _, err := range f.Scan(context.Background(), Query{}) {
		if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "at most 100") {
			t.Fatalf("err = %v", err)
		}
	}
}

// The error of a scan names the column, the encoding and the byte offset.
func TestScanErrorNamesColumnEncodingOffset(t *testing.T) {
	raw := extraFixture(t, "default_ints.vortex")
	f := reopen(t, raw, plain)
	defer func(n int) { nodeRowsBase = n }(nodeRowsBase)
	nodeRowsBase, f.size, f.numRows = 1000, 0, 0
	for _, err := range f.Scan(context.Background(), Query{}, "bulk") {
		for _, want := range []string{`column "bulk"`, "vortex.", "at offset "} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want %q in it", err, want)
			}
		}
	}
	boom := errors.New("boom")
	f = reopen(t, raw, plain)
	f.r = &testutil.FailAt{R: f.r, N: 1, Err: boom}
	for _, err := range f.Scan(context.Background(), Query{}, "sorted") {
		if !errors.Is(err, boom) || err.Error() != `vortex: reading at offset 8: boom, column "sorted", layout vortex.flat` {
			t.Fatalf("err = %v", err)
		}
	}
}
