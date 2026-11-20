package vortex

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
	"github.com/fclairamb/vortex-go/internal/testutil"
)

// vnode is an array node with metadata.
func vnode(enc int, meta []byte, bufs []uint16, kids ...fbtest.Tab) fbtest.Tab {
	t := aNode(enc, bufs, kids...)
	if meta != nil {
		t[1] = fbtest.Bytes(meta)
	}
	return t
}

// inline is the view of a value of up to 12 bytes.
func inline(v []byte) []byte {
	return append(binary.LittleEndian.AppendUint32(nil, uint32(len(v))), append(append([]byte(nil), v...), make([]byte, 12-len(v))...)...)
}

func views(vals ...[]byte) abuf {
	var b []byte
	for _, v := range vals {
		b = append(b, inline(v)...)
	}
	return abuf{data: b}
}

// variantFile is a file of one column, v: variant?, of 5 rows of an unshredded parquet variant.
func variantFile() []byte {
	dtype := structType(false, []string{"v"}, dtypeTab(11, fbtest.Tab{0: fbtest.U8(1)}))
	meta := []byte{1, 1, 0, 1, 'k'}
	micros := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC).UnixMicro()
	values := [][]byte{
		{2, 1, 0, 0, 2, 3 << 2, 5},
		append([]byte{8 << 2, 2}, binary.LittleEndian.AppendUint32(nil, 12345)...),
		binary.LittleEndian.AppendUint64([]byte{12 << 2}, uint64(micros)),
		{},
		{5 << 2, 1},
	}
	arr := aSeg(
		aNode(aStruct, nil,
			vnode(aParquetVariant, []byte{0x08, 0x01}, nil,
				aNode(aBool, []uint16{0}),
				aNode(aVarBinView, []uint16{1}),
				aNode(aVarBinView, []uint16{2}))),
		abuf{data: bits(true, true, true, false, true)}, views(meta, meta, meta, meta, meta), views(values...))
	return buildFile(dtype, layoutTab(lFlat, 5, []uint32{0}), arr)
}

func TestVariantColumn(t *testing.T) {
	f, err := open(variantFile())
	if err != nil {
		t.Fatal(err)
	}
	batches, err := scanAll(t, f, maxBatchRows)
	if err != nil || len(batches) != 1 {
		t.Fatalf("%d batches, %v", len(batches), err)
	}
	c := batches[0].Col(0)
	if c.Type().Kind() != Variant || c.Type().String() != "variant?" {
		t.Fatalf("type = %s", c.Type())
	}
	micros := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	want := []any{
		map[string]any{"k": int64(5)},
		VariantDecimal{Value: DecimalFromInt64(12345), Scale: 2},
		micros,
		nil,
	}
	for i, w := range want {
		got, err := c.Variant(i)
		if err != nil || !reflect.DeepEqual(got, w) {
			t.Errorf("row %d = %#v, %v, want %#v", i, got, err, w)
		}
	}
	if !c.Null(3) {
		t.Fatal("row 3 must be null")
	}
	if s := want[1].(VariantDecimal).String(); s != "123.45" {
		t.Fatalf("String = %q", s)
	}
	if _, err := c.Variant(4); err == nil || !strings.Contains(err.Error(), "cut short") {
		t.Fatalf("damaged row: %v", err)
	}
	mustPanic(t, "Variant out of range", func() { _, _ = c.Variant(5) })
	mustPanic(t, "Variant on a bool", func() { _, _ = (&Column{typ: Type{kind: Bool}}).Variant(0) })
}

// TestVariantColumnHash checks the hash of the harness: the canonical JSON of the row, hashed
// like a string.
func TestVariantColumnHash(t *testing.T) {
	f, err := open(variantFile())
	if err != nil {
		t.Fatal(err)
	}
	batches, _ := scanAll(t, f, maxBatchRows)
	c := batches[0].Col(0)
	hash := func(i int) string {
		h := testutil.NewHasher()
		hashRow(h, c, i)
		return h.Sum()
	}
	sum := func(text string) string {
		b := append([]byte{1}, binary.LittleEndian.AppendUint32(nil, uint32(len(text)))...)
		s := sha256.Sum256(append(b, text...))
		return hex.EncodeToString(s[:])
	}
	if got, want := hash(0), sum(`{"k":5}`); got != want {
		t.Fatalf("row 0 = %s, want %s", got, want)
	}
	if got, want := hash(1), sum(`{"$decimal":"123.45"}`); got != want {
		t.Fatalf("row 1 = %s, want %s", got, want)
	}
	if got, want := hash(2), sum(`{"$time":"2026-11-20T01:02:03Z"}`); got != want {
		t.Fatalf("row 2 = %s, want %s", got, want)
	}
	if got, want := hash(3), "6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d"; got != want { // sha256 of the byte 0
		t.Fatalf("null row = %s, want %s", got, want)
	}
	if got, want := hash(4), sum("error: variant value is cut short"); got != want {
		t.Fatalf("a damaged row hashes its error: %s, want %s", got, want)
	}
}
