//go:build interop

package vortex

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

// interopRow has a field of the common kinds, written by a RowWriter.
type interopRow struct {
	ID    int64             `vortex:"id"`
	F     float64           `vortex:"f"`
	S     string            `vortex:"s"`
	B     bool              `vortex:"b"`
	N     sql.Null[int32]   `vortex:"n"`
	Bin   []byte            `vortex:"bin"`
	L     []int32           `vortex:"l"`
	At    time.Time         `vortex:"at"`
	Price Decimal           `vortex:"price,decimal(12,2)"`
	NS    *string           `vortex:"ns"`
	UID   [16]byte          `vortex:"uid"`
	St    struct{ A int16 } `vortex:"st"`
}

// TestInteropRows writes rows with a RowWriter into the folder of the oracle, with the
// canonical hashes of the rows: the reference reader must give them back.
func TestInteropRows(t *testing.T) {
	dir := interopDir(t)
	for _, n := range []int{0, 1, 5000, 20000} {
		r := rand.New(rand.NewPCG(7, uint64(n)))
		rows := make([]interopRow, n)
		cols := map[string][]val{}
		add := func(name string, v val) { cols[name] = append(cols[name], v) }
		le := func(x uint64, w int) []byte { return binary.LittleEndian.AppendUint64(nil, x)[:w] }
		for i := range rows {
			x := &rows[i]
			x.ID, x.F, x.S, x.B = r.Int64(), r.NormFloat64(), fmt.Sprint("row", r.IntN(50)), r.IntN(2) == 0
			x.N = sql.Null[int32]{V: r.Int32(), Valid: r.IntN(3) != 0}
			x.Bin = bytes.Repeat([]byte{byte(i)}, r.IntN(6))
			x.L = make([]int32, r.IntN(4))
			x.At = time.Unix(r.Int64N(4e9), r.Int64N(1e6)*1e3).UTC()
			x.Price = DecimalFromInt64(r.Int64N(1e9) - 5e8)
			if r.IntN(2) == 0 {
				s := fmt.Sprint(i)
				x.NS = &s
			}
			for k := range x.UID {
				x.UID[k] = byte(r.Uint32())
			}
			x.St.A = int16(r.Int32())
			add("id", val{b: le(uint64(x.ID), 8)})
			add("f", val{b: le(math.Float64bits(x.F), 8)})
			add("s", val{b: []byte(x.S)})
			add("b", val{b: []byte{map[bool]byte{false: 0, true: 1}[x.B]}})
			if x.N.Valid {
				add("n", val{b: le(uint64(uint32(x.N.V)), 4)})
			} else {
				add("n", val{null: true})
			}
			add("bin", val{b: x.Bin})
			lv := val{}
			for k := range x.L {
				x.L[k] = r.Int32()
				lv.kids = append(lv.kids, val{b: le(uint64(uint32(x.L[k])), 4)})
			}
			add("l", lv)
			add("at", val{b: le(uint64(x.At.UnixMicro()), 8)})
			dec := x.Price.le()
			add("price", val{b: dec[:]})
			if x.NS != nil {
				add("ns", val{b: []byte(*x.NS)})
			} else {
				add("ns", val{null: true})
			}
			add("uid", val{b: x.UID[:]})
			add("st", val{kids: []val{{b: le(uint64(uint16(x.St.A)), 2)}}})
		}
		schema := schemaOf[interopRow](t)
		hashes := map[string]hashEntry{}
		for _, f := range schema.Fields() {
			hashColumn(hashes, f.Name, f.Type, cols[f.Name])
		}
		out, err := json.MarshalIndent(map[string]any{"rows": n, "columns": hashes}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range interopModes {
			file := writeRowsOf(t, rows, WriteOptions{Compression: m.c}, 3000, 1, 7000)
			writeInteropFile(t, dir, fmt.Sprintf("rowwriter_%d%s", n, m.suffix), file, out)
		}
	}
}
