package vortex

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
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

// damageShardEnv names, in a process TestSystematicDamage starts, the share of the fixtures
// it checks: "k/n" for the fixtures whose index is k modulo n.
const damageShardEnv = "VORTEX_DAMAGE_SHARD"

// Every fixture, truncated or with a byte of its footer, postscript or trailer flipped, opens
// and scans to an error or to data, without a panic, within the deadline and under the
// allocation limit. Go counts allocations only for the whole process, so each copy is
// measured alone in its process: the test runs itself in one process per core, each checking
// its share of the fixtures one copy after the other.
func TestSystematicDamage(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("about 90,000 scans")
	}
	shard, shards := 0, 1
	if env := os.Getenv(damageShardEnv); env != "" {
		if _, err := fmt.Sscanf(env, "%d/%d", &shard, &shards); err != nil {
			t.Fatal(err)
		}
	} else if n := runtime.GOMAXPROCS(0); n > 1 {
		runDamageShards(t, n)
		return
	}
	for i, path := range corpus(t) {
		if i%shards != shard {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(fixtureName(path), func(t *testing.T) {
			for i, b := range damage(t, raw) {
				if err := openScan(b, Query{}, true); err != nil {
					t.Fatalf("damage %d: %v", i, err)
				}
			}
		})
	}
}

// runDamageShards runs TestSystematicDamage in n processes, each on its share of the fixtures.
func runDamageShards(t *testing.T, n int) {
	for k := range n {
		t.Run(fmt.Sprintf("shard-%d", k), func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(os.Args[0], "-test.run=^TestSystematicDamage$", "-test.count=1", "-test.timeout=30m")
			cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d/%d", damageShardEnv, k, n))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}

// FuzzOpenScan opens and scans arbitrary bytes: no panic, done within the deadline, under the
// allocation limit. It is seeded with every fixture. `make fuzz` runs it.
func FuzzOpenScan(f *testing.F) {
	for _, path := range corpus(f) {
		raw, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if err := openScan(raw, Query{}, true); err != nil {
			t.Fatal(err)
		}
	})
}
