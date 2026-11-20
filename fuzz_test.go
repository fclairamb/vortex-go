package vortex

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/fclairamb/vortex-go/internal/fbs"
)

// scanDeadline is the time an open and a scan of a damaged file may take.
const scanDeadline = 20 * time.Second

// allocLimit is the most an open and a full scan of a file of size bytes and rows rows may
// allocate: a multiple of the file, and of its rows for what decoding expands.
func allocLimit(size, rows int64) uint64 {
	return 64<<20 + 256*uint64(size) + 1024*uint64(min(rows, int64(maxFlatRows)))
}

// openScan opens raw and scans all of it, within the deadline and, when measure is set, the
// allocation limit, which needs nothing else to run meanwhile. It returns an error when one
// of the properties fails; the errors that the damage gives are expected.
func openScan(raw []byte, q Query, measure bool) error {
	done := make(chan error, 1)
	var before, after runtime.MemStats
	var rows int64
	if measure {
		runtime.ReadMemStats(&before)
	}
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("panic: %v", p)
			}
		}()
		f, err := Open(bytes.NewReader(raw), int64(len(raw)))
		if err == nil {
			_, hi, _ := clamp(q.Rows, f.NumRows())
			rows = hi
			for _, err := range f.Scan(context.Background(), q) {
				if err != nil {
					break
				}
			}
		}
		done <- nil
	}()
	timer := time.NewTimer(scanDeadline)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil || !measure {
			return err
		}
	case <-timer.C:
		return fmt.Errorf("open and scan take more than %v", scanDeadline)
	}
	runtime.ReadMemStats(&after)
	if got, limit := after.TotalAlloc-before.TotalAlloc, allocLimit(int64(len(raw)), rows); got > limit {
		return fmt.Errorf("%d bytes allocated, at most %d", got, limit)
	}
	return nil
}

// damage is the damaged copies of a file: truncated at 200 offsets spread over it, and with
// each byte of its footer, postscript and trailer flipped.
func damage(t *testing.T, raw []byte) [][]byte {
	var out [][]byte
	n := len(raw)
	for i := range 200 {
		out = append(out, raw[:i*n/200])
	}
	psLen := int(binary.LittleEndian.Uint16(raw[n-6:]))
	psStart := n - trailerSize - psLen
	ps, err := fbs.ReadPostscript(raw[psStart : n-trailerSize])
	if err != nil {
		t.Fatal(err)
	}
	segs, err := postscriptSegments(ps, uint64(psStart))
	if err != nil {
		t.Fatal(err)
	}
	flip := func(from, to int) {
		for i := from; i < to; i++ {
			b := bytes.Clone(raw)
			b[i] ^= 0xff
			out = append(out, b)
		}
	}
	flip(int(segs.footer.offset), int(segs.footer.offset)+int(segs.footer.length))
	flip(psStart, n)
	return out
}

// Every fixture, truncated or with a byte of its footer, postscript or trailer flipped, opens
// and scans to an error or to data, without a panic, within the deadline. The fixtures run in
// parallel; every 16th damaged copy is then run alone, under the allocation limit.
func TestSystematicDamage(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("about 90,000 scans")
	}
	var measured [][]byte
	for _, path := range corpus(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		cases := damage(t, raw)
		for i := 0; i < len(cases); i += 16 {
			measured = append(measured, cases[i])
		}
		t.Run(fixtureName(path), func(t *testing.T) {
			t.Parallel()
			for i, b := range cases {
				if err := openScan(b, Query{}, false); err != nil {
					t.Fatalf("damage %d: %v", i, err)
				}
			}
		})
	}
	t.Cleanup(func() {
		for i, b := range measured {
			if err := openScan(b, Query{}, true); err != nil {
				t.Errorf("measured damage %d: %v", i, err)
			}
		}
	})
}

// fuzzRows is the most rows the fuzzer scans, and the most rows of a flat it accepts.
const fuzzRows = 1 << 16

// fuzzBytes is the largest input of the fuzzer: mutating and minimizing larger ones is slow.
const fuzzBytes = 32 << 10

// FuzzOpenScan opens and scans arbitrary bytes: no panic, done within the deadline, under the
// allocation limit. `make fuzz` runs it.
func FuzzOpenScan(f *testing.F) {
	for _, path := range corpus(f) {
		raw, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		if len(raw) <= fuzzBytes {
			f.Add(raw)
		}
	}
	// A small file may hold any number of rows (a constant does), which then take their time
	// to scan. The fuzzer scans the first rows, of flats that are not too large.
	defer func(n uint64) { maxFlatRows = n }(maxFlatRows)
	maxFlatRows = fuzzRows
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > fuzzBytes {
			t.Skip()
		}
		if err := openScan(raw, Query{Rows: Range{End: fuzzRows}}, true); err != nil {
			t.Fatal(err)
		}
	})
}
