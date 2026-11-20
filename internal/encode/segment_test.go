package encode

import (
	"encoding/binary"
	"slices"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// segmentOf serializes the tree as an array segment: the buffers in the order of a walk that
// visits a node before its children, then the flatbuffer and its length. It returns the
// segment and the encodings it uses.
func segmentOf(root *Node) ([]byte, []string) {
	var specs []string
	var body []byte
	var descs [][]byte
	var build func(n *Node) fbtest.Tab
	build = func(n *Node) fbtest.Tab {
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

// decode reads the tree back as an array of type t.
func decode(tb testing.TB, root *Node, t array.DType, rows int) *array.Array {
	tb.Helper()
	seg, specs := segmentOf(root)
	a, err := array.Decode(seg, specs, t, rows, array.Options{})
	if err != nil {
		tb.Fatalf("decode %s: %v", root.ID, err)
	}
	return a
}
