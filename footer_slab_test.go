package vortex

import (
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// TestOpenAllocatesByBlocks checks that the nodes of the layout tree, their children and
// their segments come from blocks: a file of 2 columns of 300 chunks opens in a number of
// allocations far below its number of nodes, and its tree is the one of the file.
func TestOpenAllocatesByBlocks(t *testing.T) {
	const chunks = 300
	column := func() fbtest.Tab {
		flats := make([]fbtest.Tab, chunks)
		for i := range flats {
			flats[i] = layoutTab(lFlat, 1, []uint32{uint32(i % 2)})
		}
		return layoutTab(lChunked, chunks, nil, flats...)
	}
	dtype := structType(false, []string{"a", "b"}, prim(7, false), prim(7, false))
	raw := buildFile(dtype, layoutTab(lStruct, chunks, nil, column(), column()), primSeg(8, 1), primSeg(8, 2))
	f, err := open(raw)
	if err != nil {
		t.Fatal(err)
	}
	nodes := 0
	var count func(n *layoutNode)
	count = func(n *layoutNode) {
		nodes++
		for i, c := range n.children {
			if len(c.children) == 0 && (len(c.segments) != 1 || c.segments[0] != uint32(i%2) || c.rowCount != 1) {
				t.Fatalf("chunk %d has segments %v and %d rows", i, c.segments, c.rowCount)
			}
			count(c)
		}
	}
	count(f.layout)
	if nodes != 3+2*chunks {
		t.Fatalf("%d nodes", nodes)
	}
	allocs := testing.AllocsPerRun(10, func() {
		if _, err := open(raw); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > float64(nodes)/8 {
		t.Errorf("%v allocations for %d nodes", allocs, nodes)
	}
}
