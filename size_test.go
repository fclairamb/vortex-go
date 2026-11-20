//go:build interop

package vortex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The size gate (docs/testing.md section 4): the TPC-H lineitem and orders and the ClickBench
// hits_5k data, read from the reference's own files, written by our writer in the Default and
// Compact modes. A ratio is our size over the size of the reference file of the same mode. The
// ratios are committed in testdata/size-ratios.json: the gate fails above the limit of the mode,
// and when a ratio is higher than the committed one. SIZE_RATIOS_UPDATE=1 rewrites the file with
// the measured ratios, which can only go down.

// sizeLimits is the highest ratio each mode may have: 1.10 for Default, and for Compact the
// highest ratio measured without a Pco encoder (1.157, tpch_orders), plus 2% (docs/goals.md).
var sizeLimits = map[string]float64{"regular": 1.10, "compact": 1.18}

var sizeDatasets = []string{"tpch_lineitem", "tpch_orders", "clickbench_hits_5k"}

const sizeRatiosFile = "testdata/size-ratios.json"

// fileData reads every row of a file as the Data of its columns, which must all be flat.
func fileData(t testing.TB, path string) (*Schema, []Data) {
	f, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fields := f.Schema().Fields()
	cols := make([]*body, len(fields))
	for i, fld := range fields {
		cols[i] = &body{}
		switch k := fld.Type.Kind(); {
		case k == Utf8 || k == Binary:
			cols[i].kind = dBytes
		case k == KindDecimal:
			cols[i].kind = dDec
		default:
			cols[i].kind, cols[i].pk = dPrim, storageKind(fld.Type)
		}
		if fld.Type.Nullable() {
			cols[i].nulls = []bool{}
		}
	}
	for b, err := range f.Scan(context.Background(), Query{}) {
		if err != nil {
			t.Fatal(err)
		}
		for i, d := range cols {
			c := b.Col(i)
			for r := range b.Len() {
				null := c.Null(r)
				if d.nulls != nil {
					d.nulls = append(d.nulls, null)
				}
				switch d.kind {
				case dBytes:
					d.bins = append(d.bins, bytes.Clone(c.Bytes(r)))
				case dDec:
					d.decs = append(d.decs, c.Decimal(r))
				}
			}
			if d.kind == dPrim {
				d.raw = append(d.raw, c.arr.Data[:b.Len()*numberWidth(d.pk)]...)
			}
			d.n += b.Len()
		}
	}
	data := make([]Data, len(cols))
	for i, d := range cols {
		data[i] = Data{d}
	}
	return f.Schema(), data
}

// writeDataset writes the data in the given mode and returns the file.
func writeDataset(t testing.TB, s *Schema, data []Data, c Compression) []byte {
	var buf bytes.Buffer
	w := NewWriter(&buf, s, WriteOptions{Compression: c})
	if err := w.Write(data...); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestSizeRatios is the size gate. With INTEROP_DIR, it also writes its files there, with the
// hashes the reference computed on its own files, for the oracle.
func TestSizeRatios(t *testing.T) {
	dir := filepath.Join(".cache", "fixtures", strings.TrimSpace(readFile(t, ".vortex-version")))
	committed := map[string]float64{}
	if err := json.Unmarshal([]byte(readFile(t, sizeRatiosFile)), &committed); err != nil {
		t.Fatal(err)
	}
	measured := map[string]float64{}
	for _, name := range sizeDatasets {
		s, data := fileData(t, filepath.Join(dir, name+".regular.vortex"))
		for _, mode := range []string{"regular", "compact"} {
			ref, err := os.Stat(filepath.Join(dir, name+"."+mode+".vortex"))
			if err != nil {
				t.Fatal(err)
			}
			c := map[string]Compression{"regular": Default, "compact": Compact}[mode]
			file := writeDataset(t, s, data, c)
			ours := len(file)
			key := name + "." + mode
			if out := os.Getenv("INTEROP_DIR"); out != "" {
				// the reference reader must read back the data of its own file
				expected := readFile(t, filepath.Join(dir, name+".regular.vortex.expected.json"))
				writeInteropFile(t, out, "size_"+name+"_"+mode, file, []byte(expected))
			}
			ratio := float64(int(float64(ours)/float64(ref.Size())*1000+0.999)) / 1000 // rounded up
			measured[key] = ratio
			t.Logf("%s: %d bytes, the reference %d, ratio %.3f (committed %.3f)", key, ours, ref.Size(), ratio, committed[key])
			if ratio > sizeLimits[mode] {
				t.Errorf("%s: ratio %.3f, above %.2f", key, ratio, sizeLimits[mode])
			}
			if old, ok := committed[key]; ok && ratio > old {
				t.Errorf("%s: ratio %.3f, above the committed %.3f", key, ratio, old)
			}
		}
	}
	if os.Getenv("SIZE_RATIOS_UPDATE") != "" {
		keys := slices.Sorted(func(yield func(string) bool) {
			for k := range measured {
				if !yield(k) {
					return
				}
			}
		})
		var b strings.Builder
		b.WriteString("{\n")
		for i, k := range keys {
			fmt.Fprintf(&b, "  %q: %.3f", k, measured[k])
			if i < len(keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString("}\n")
		if err := os.WriteFile(sizeRatiosFile, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t testing.TB, path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
