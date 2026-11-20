//go:build interop

package vortex

import (
	"bytes"
	"encoding/json"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// interopDir is where the files go: INTEROP_DIR, a temporary folder when it is not set.
func interopDir(t *testing.T) string {
	if dir := os.Getenv("INTEROP_DIR"); dir != "" {
		return dir
	}
	return t.TempDir()
}

// TestInterop writes files and the canonical hashes of their source data into INTEROP_DIR:
// `make interop` then has the reference reader open them, with tools/oracle/oracle.py check.
// One file per type, in one chunk and in four, then every type side by side.
func TestInterop(t *testing.T) {
	dir := interopDir(t)
	types := allTypes()
	var fields []Field
	for _, name := range slices.Sorted(maps.Keys(types)) {
		fields = append(fields, Field{Name: name, Type: types[name]})
		file := "type_" + strings.NewReplacer("?", "_null").Replace(name)
		writeInterop(t, dir, file, fields[len(fields)-1:], []int{200})
		writeInterop(t, dir, file+"_chunks", fields[len(fields)-1:], []int{1, 130, 0, 17})
	}
	writeInterop(t, dir, "wide", fields, []int{500, 1, 77})
	writeInterop(t, dir, "wide_one_row", fields, []int{1})
	// The reference's Python package cannot make an empty arrow array of a list of structs with
	// an extension type in it, so the files of no row leave that one out.
	empty := slices.DeleteFunc(slices.Clone(fields), func(f Field) bool { return f.Name == "list_struct" })
	writeInterop(t, dir, "wide_empty", empty, nil)
	writeInterop(t, dir, "wide_empty_write", empty, []int{0})
}

// writeInterop writes a file with one Write per size, and its hashes.
func writeInterop(t *testing.T, dir, file string, fields []Field, sizes []int) {
	t.Helper()
	g := &gen{r: rand.New(rand.NewPCG(11, uint64(len(file))))}
	var buf bytes.Buffer
	w := NewWriter(&buf, NewSchema(fields...), WriteOptions{})
	all := make([][]val, len(fields))
	total := 0
	for _, n := range sizes {
		cols := make([]Data, len(fields))
		for i, f := range fields {
			var vals []val
			cols[i], vals = g.make(f.Type, n)
			all[i] = append(all[i], vals...)
		}
		if err := w.Write(cols...); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		total += n
	}
	if err := w.Close(); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	hashes := map[string]hashEntry{}
	for i, f := range fields {
		hashColumn(hashes, f.Name, f.Type, all[i])
	}
	out, err := json.MarshalIndent(map[string]any{"rows": total, "columns": hashes}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file+".vortex"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file+".hashes.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
}
