package compress

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// segmentOf serializes the tree as an array segment, as internal/encode's tests do: the
// buffers in the order of a walk that visits a node before its children, then the flatbuffer
// and its length. It returns the segment and the encodings it uses.
func segmentOf(root *encode.Node) ([]byte, []string) {
	var specs []string
	var body []byte
	var descs [][]byte
	var build func(n *encode.Node) fbtest.Tab
	build = func(n *encode.Node) fbtest.Tab {
		idx := slices.Index(specs, n.ID)
		if idx < 0 {
			idx = len(specs)
			specs = append(specs, n.ID)
		}
		t := fbtest.Tab{0: fbtest.U16(uint16(idx))}
		if len(n.Meta) > 0 {
			t[1] = fbtest.Bytes(n.Meta)
		}
		var bufs []uint16
		for _, b := range n.Bufs {
			bufs = append(bufs, uint16(len(descs)))
			d := binary.LittleEndian.AppendUint16(nil, 0)
			d = append(d, 0, 0)
			descs = append(descs, binary.LittleEndian.AppendUint32(d, uint32(len(b.Data))))
			body = append(body, b.Data...)
		}
		var kids []fbtest.Tab
		for _, k := range n.Kids {
			kids = append(kids, build(k))
		}
		if kids != nil {
			t[2] = fbtest.Tables(kids...)
		}
		if bufs != nil {
			t[3] = fbtest.U16s(bufs...)
		}
		return t
	}
	rootTab := build(root)
	msg := fbtest.Tab{0: fbtest.T(rootTab)}
	if descs != nil {
		msg[1] = fbtest.Structs(8, descs...)
	}
	fb := fbtest.Build(msg)
	out := append(body, fb...)
	return binary.LittleEndian.AppendUint32(out, uint32(len(fb))), specs
}

// checkNumbers decodes the tree as numbers of type p and compares every valid row with raw.
func checkNumbers(t testing.TB, n *encode.Node, p PType, raw []byte, nulls []bool) {
	t.Helper()
	if n == nil {
		return
	}
	rows := len(raw) / p.Width()
	seg, specs := segmentOf(n)
	ap := array.PType(p)
	if p >= F32 {
		ap++ // the reader has a f16
	}
	a, err := array.Decode(seg, specs, array.DType{Kind: array.Prim, PType: ap, Nullable: hasNull(nulls)}, rows, array.Options{})
	if err != nil {
		t.Fatalf("%s: %v", tree(n), err)
	}
	w := p.Width()
	for i := range rows {
		if isNull(nulls, i) != a.Null(i) || !isNull(nulls, i) && !bytes.Equal(a.Data[i*w:(i+1)*w], raw[i*w:(i+1)*w]) {
			t.Fatalf("%s: row %d differs", tree(n), i)
		}
	}
}

// checkBins decodes the tree as strings and compares every valid row with v.
func checkBins(t testing.TB, n *encode.Node, v [][]byte, nulls []bool, utf8 bool) {
	t.Helper()
	if n == nil {
		return
	}
	seg, specs := segmentOf(n)
	kind := array.Binary
	if utf8 {
		kind = array.Utf8
	}
	a, err := array.Decode(seg, specs, array.DType{Kind: kind, Nullable: hasNull(nulls)}, len(v), array.Options{})
	if err != nil {
		t.Fatalf("%s: %v", tree(n), err)
	}
	for i := range v {
		if isNull(nulls, i) != a.Null(i) || !isNull(nulls, i) && !bytes.Equal(a.Data[a.Starts[i]:a.Ends[i]], v[i]) {
			t.Fatalf("%s: row %d differs", tree(n), i)
		}
	}
}
