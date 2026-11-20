//go:build interop

package vortex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestInteropVariants writes Variant files, in the three compression modes, and the JSON of
// each row (variants.<mode>.jsonl): `make interop` has tools/variantjson, the reference reader,
// print the rows of each file and compares. The values are the ones JSON holds, floats being
// fractional so that both sides print them alike.
func TestInteropVariants(t *testing.T) {
	dir := interopDir(t)
	const n = 20000
	vals := make([]any, n)
	for i := range vals {
		switch i % 8 {
		case 0:
			vals[i] = nil
		case 1:
			vals[i] = i%2 == 0
		case 2:
			vals[i] = int64(i*7919 - 70000)
		case 3:
			vals[i] = float64(i) + 0.5
		case 4:
			vals[i] = fmt.Sprintf("value %d", i%97)
		case 5:
			vals[i] = []any{int64(i), "x", nil, []any{}, map[string]any{}}
		case 6:
			vals[i] = map[string]any{"id": int64(i), "tags": []any{"a", "b"}, "o": map[string]any{"k": 1.5, "z": nil}}
		default:
			vals[i] = map[string]any{"id": int64(i), "name": fmt.Sprintf("n%d", i%13)}
		}
	}
	var lines bytes.Buffer
	for _, v := range vals {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(append(b, '\n'))
	}
	for mode, c := range map[string]Compression{"default": Default, "compact": Compact, "none": None} {
		file := writeVariants(t, c, VariantType().AsNullable(), vals, 7000, 1, 8191, 4808)
		if err := os.WriteFile(filepath.Join(dir, "variants_"+mode+".vortex"), file, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "variants_"+mode+".jsonl"), lines.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
