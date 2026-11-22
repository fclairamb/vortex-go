//go:build interop

package vortex_test

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	vortex "github.com/fclairamb/vortex-go"
	"github.com/fclairamb/vortex-go/internal/testutil"
)

// TestInteropDBBat writes the archive of dbbat (uuids, a JSON text), the three sets of
// dbbat_test.go, in each compression level into INTEROP_DIR: `make interop` has the reference
// reader open them (uuid bytes of a Compact file, a Compact column of one zone with no zone
// table) and check the values against the hashes written here.
func TestInteropDBBat(t *testing.T) {
	dir := os.Getenv("INTEROP_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	modes := []struct {
		suffix string
		c      vortex.Compression
	}{{"", vortex.Default}, {"_compact", vortex.Compact}, {"_none", vortex.None}}
	for _, c := range dbbatCases {
		rows := dbbatRows(c.n, c.stmts, c.wide)
		hashes := map[string]map[string]any{}
		hash := func(name string, each func(h *testutil.Hasher, r dbbatRow)) {
			h := testutil.NewHasher()
			for _, r := range rows {
				h.Valid()
				each(h, r)
			}
			hashes[name] = map[string]any{"nulls": 0, "sha256": h.Sum()}
		}
		for name, get := range map[string]func(r dbbatRow) [16]byte{
			"connection_uid": func(r dbbatRow) [16]byte { return r.ConnectionUID },
			"query_uid":      func(r dbbatRow) [16]byte { return r.QueryUID },
			"row_uid":        func(r dbbatRow) [16]byte { return r.RowUID },
		} {
			hash(name, func(h *testutil.Hasher, r dbbatRow) { u := get(r); h.Raw(u[:]) })
		}
		hash("row_number", func(h *testutil.Hasher, r dbbatRow) {
			h.Raw(binary.LittleEndian.AppendUint32(nil, uint32(r.RowNumber)))
		})
		hash("row_data", func(h *testutil.Hasher, r dbbatRow) { h.Len(len(r.RowData)); h.Raw([]byte(r.RowData)) })
		hash("row_size_bytes", func(h *testutil.Hasher, r dbbatRow) {
			h.Raw(binary.LittleEndian.AppendUint64(nil, uint64(r.RowSizeBytes)))
		})
		out, err := json.MarshalIndent(map[string]any{"rows": len(rows), "columns": hashes}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range modes {
			file := "dbbat_" + c.name + m.suffix
			if err := os.WriteFile(filepath.Join(dir, file+".vortex"), writeDBBat(t, rows, m.c, vortex.WriteOptions{}), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, file+".hashes.json"), out, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
