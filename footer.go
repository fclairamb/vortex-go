package vortex

import (
	"fmt"

	"github.com/fclairamb/vortex-go/internal/fbs"
)

// layoutNode is a node of the layout tree. It is not interpreted yet.
type layoutNode struct {
	metadata []byte
	children []*layoutNode
	segments []uint32 // indices into File.segments
	rowCount uint64
	encoding uint16 // index into File.layoutEncodings
}

// collect decodes every element of a list.
func collect[T any](l fbs.List[T], err error) ([]T, error) {
	if err != nil {
		return nil, err
	}
	out := make([]T, l.Len())
	for i := range out {
		if out[i], err = l.At(i); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ids reads the identifiers of a list of specs.
func ids[T any](l fbs.List[T], err error, id func(T) (string, error)) ([]string, error) {
	specs, err := collect(l, err)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(specs))
	for i, s := range specs {
		if out[i], err = id(s); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readFooter keeps the dictionaries of the footer. The compression and encryption fields
// of the segments are reserved: upstream ignores them, so do we.
func (f *File) readFooter(foot fbs.Footer, off uint64) error {
	arrays, err := foot.ArraySpecs()
	if f.arrayEncodings, err = ids(arrays, err, fbs.ArraySpec.ID); err != nil {
		return wrapCorrupt("footer array specs", off, err)
	}
	layouts, err := foot.LayoutSpecs()
	if f.layoutEncodings, err = ids(layouts, err, fbs.LayoutSpec.ID); err != nil {
		return wrapCorrupt("footer layout specs", off, err)
	}
	if f.segments, err = collect(foot.SegmentSpecs()); err != nil {
		return wrapCorrupt("footer segment specs", off, err)
	}
	var prev uint64
	for i, s := range f.segments {
		if s.Offset < prev {
			return corrupt(int64(min(s.Offset, 1<<62)), "segment %d starts before segment %d, they must be sorted", i, i-1)
		}
		if s.Offset > uint64(f.size) || uint64(s.Length) > uint64(f.size)-s.Offset {
			return corrupt(int64(min(s.Offset, 1<<62)), "segment %d of %d bytes is past the end of the file (%d bytes)",
				i, s.Length, f.size)
		}
		prev = s.Offset
	}
	return nil
}

// layoutSlab is the size of the blocks the nodes of a layout tree are cut from: a file of
// many chunks has thousands of nodes, and one allocation each for the node, its children and
// its segments was most of the time of Open.
const layoutSlab = 64

// layoutBuilder holds what is left of the blocks of a tree being read.
type layoutBuilder struct {
	nodes []layoutNode
	kids  []*layoutNode
	segs  []uint32
}

// carve takes n elements off the block free, or off a new block when it has fewer. A
// quarter of a block or more is allocated on its own instead, so that what is left of the
// block is not lost. The slice has no spare capacity: an append to it cannot reach the next.
func carve[T any](free *[]T, n int) []T {
	if n > len(*free) {
		if n >= layoutSlab/4 {
			return make([]T, n)
		}
		*free = make([]T, layoutSlab)
	}
	out := (*free)[:n:n]
	*free = (*free)[n:]
	return out
}

// readLayout parses the layout tree, and checks its indices against the footer.
func (f *File) readLayout(buf []byte, off uint64) (*layoutNode, error) {
	l, err := fbs.ReadLayout(buf, maxLayoutDepth)
	if err != nil {
		return nil, wrapCorrupt("layout", off, err)
	}
	var b layoutBuilder
	n := &carve(&b.nodes, 1)[0]
	if err := f.layoutNode(&b, n, l); err != nil {
		return nil, wrapCorrupt("layout", off, err)
	}
	return n, nil
}

// layoutNode reads the node l, and the nodes below it, to n.
func (f *File) layoutNode(b *layoutBuilder, n *layoutNode, l fbs.Layout) error {
	var err error
	if n.encoding, err = l.Encoding(); err != nil {
		return err
	}
	if int(n.encoding) >= len(f.layoutEncodings) {
		return fmt.Errorf("layout encoding %d, the footer has %d", n.encoding, len(f.layoutEncodings))
	}
	if n.rowCount, err = l.RowCount(); err != nil {
		return err
	}
	if n.metadata, err = l.Metadata(); err != nil {
		return err
	}
	segs, err := l.Segments()
	if err != nil {
		return err
	}
	n.segments = carve(&b.segs, segs.Len())
	segs.Read(n.segments)
	for _, s := range n.segments {
		if int(s) >= len(f.segments) {
			return fmt.Errorf("segment %d, the footer has %d", s, len(f.segments))
		}
	}
	kids, err := l.Children()
	if err != nil {
		return err
	}
	if kids.Len() == 0 {
		return nil
	}
	nodes := carve(&b.nodes, kids.Len())
	n.children = carve(&b.kids, kids.Len())
	for i := range nodes {
		k, err := kids.At(i)
		if err != nil {
			return err
		}
		if err := f.layoutNode(b, &nodes[i], k); err != nil {
			return err
		}
		n.children[i] = &nodes[i]
	}
	return nil
}
