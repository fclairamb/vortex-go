//go:build interop

package vortex

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestInteropMetadata writes a file with user metadata, for the newest edition and for
// core 2025.05.0, with the hashes of its rows and the entries it holds (metadata.metadata: a
// key, a tab, the value in hex, sorted): `make interop` has tools/variantjson's filemeta, the
// reference reader, print the entries of the newest file and compares, and the oracle reads the
// rows of both, which the readers before Vortex 0.87, which have no metadata, do too.
func TestInteropMetadata(t *testing.T) {
	root := interopDir(t)
	want := map[string][]byte{
		"dbbat.query_offsets":   []byte(`{"q1":0,"q2":512}`),
		"empty":                 {},
		strings.Repeat("k", 64): []byte("longest key"),
		strings.Repeat("é", 32): []byte("clé"),
		"big":                   bigValue(),
		"schema.version":        []byte("3"),
	}
	for i := len(want); i < MaxMetadataEntries; i++ {
		want[fmt.Sprintf("pad.%02d", i)] = bytes.Repeat([]byte{byte(i)}, i)
	}
	var lines bytes.Buffer
	for _, k := range slices.Sorted(maps.Keys(want)) {
		fmt.Fprintf(&lines, "%s\t%s\n", k, hex.EncodeToString(want[k]))
	}
	g := &gen{r: rand.New(rand.NewPCG(5, 6))}
	d, vals := g.make(Int64Type().AsNullable(), 20000)
	hashes := map[string]hashEntry{}
	hashColumn(hashes, "id", Int64Type().AsNullable(), vals)
	out, err := json.MarshalIndent(map[string]any{"rows": len(vals), "columns": hashes}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, ed := range append([]string{""}, interopEditions...) {
		dir := root
		if ed != "" {
			dir = filepath.Join(root, strings.NewReplacer(" ", "_", ".", "_").Replace(ed))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		// Half of the entries through the options, the others once the rows are written.
		opt, set := map[string][]byte{}, map[string][]byte{}
		for i, k := range slices.Sorted(maps.Keys(want)) {
			if i%2 == 0 {
				opt[k] = want[k]
			} else {
				set[k] = want[k]
			}
		}
		var buf bytes.Buffer
		w := NewWriter(&buf, NewSchema(Field{"id", Int64Type().AsNullable()}), WriteOptions{Edition: ed, Metadata: opt})
		if err := w.Write(d); err != nil {
			t.Fatal(err)
		}
		for k, v := range set {
			if err := w.SetMetadata(k, v); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		writeInteropFile(t, dir, "metadata", buf.Bytes(), out)
		if err := os.WriteFile(filepath.Join(dir, "metadata.metadata"), lines.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		checkFileMetadata(t, openBytes(t, buf.Bytes()), want)
	}
}
