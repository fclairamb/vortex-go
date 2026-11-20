package array

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/fclairamb/vortex-go/internal/fbs"
)

// maxDepth bounds the nesting of the array nodes.
const maxDepth = 128

// Options changes how an array is decoded.
type Options struct {
	// Tolerate is asked, for a struct field whose decoding failed with an unsupported
	// encoding, whether to carry on: the field then is an Array with Skipped set. The path
	// is the dotted names of the fields from the root. Tests use it, a scan never does.
	Tolerate func(path string, err error) bool
}

type context struct {
	table map[string]Decoder
	opt   Options
	specs []string
	bufs  [][]byte
}

// Node is a node of the tree of a serialized array, as a decoder sees it.
type Node struct {
	ctx      *context
	path     string
	metadata []byte
	children fbs.List[fbs.ArrayNode]
	buffers  []uint16
}

// Decoder builds the array of the given type and number of rows from a node.
type Decoder func(n *Node, t DType, rows int) (*Array, error)

// decoders is the table of the decoders, by identifier of encoding. It is built once, and
// reached through the context: a package variable would be an initialization cycle.
var decoders = sync.OnceValue(registry)

// registry builds the table of the decoders.
func registry() map[string]Decoder {
	return map[string]Decoder{
		"vortex.null":            decodeNull,
		"vortex.bool":            decodeBool,
		"vortex.primitive":       decodePrimitive,
		"vortex.struct":          decodeStruct,
		"vortex.chunked":         decodeChunked,
		"vortex.constant":        decodeConstant,
		"vortex.ext":             decodeExt,
		"vortex.masked":          decodeMasked,
		"vortex.bytebool":        decodeByteBool,
		"vortex.varbin":          decodeVarBin,
		"vortex.varbinview":      decodeVarBinView,
		"vortex.list":            decodeList,
		"vortex.listview":        decodeListView,
		"vortex.fixed_size_list": decodeFixedSizeList,
		"vortex.map":             decodeMap,
		"vortex.decimal":         decodeDecimal,
	}
}

// Decode decodes the serialized array of seg: its buffers, the flatbuffer of the tree, then
// the length of the flatbuffer. specs are the array specs of the footer, rows the number of
// rows and t the type the array must have.
func Decode(seg []byte, specs []string, t DType, rows int, opt Options) (*Array, error) {
	if len(seg) < 4 {
		return nil, fmt.Errorf("segment of %d bytes has no flatbuffer length", len(seg))
	}
	fbLen := int(binary.LittleEndian.Uint32(seg[len(seg)-4:]))
	if fbLen > len(seg)-4 {
		return nil, fmt.Errorf("flatbuffer of %d bytes in a segment of %d", fbLen, len(seg))
	}
	body := seg[:len(seg)-4-fbLen]
	msg, err := fbs.ReadArray(seg[len(body):len(seg)-4], maxDepth)
	if err != nil {
		return nil, err
	}
	bufs, err := buffers(msg, body)
	if err != nil {
		return nil, err
	}
	root, ok, err := msg.Root()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("array has no root node")
	}
	ctx := &context{opt: opt, specs: specs, bufs: bufs, table: decoders()}
	return ctx.decode(root, "", t, rows)
}

// buffers cuts the data buffers out of body, each one after its padding.
func buffers(msg fbs.Array, body []byte) ([][]byte, error) {
	list, err := msg.Buffers()
	if err != nil {
		return nil, err
	}
	out := make([][]byte, list.Len())
	pos := 0
	for i := range out {
		b, _ := list.At(i) // i < Len, and a struct vector has nothing else to check
		if b.Compression != 0 {
			return nil, fmt.Errorf("buffer %d is compressed (%d), which is not supported", i, b.Compression)
		}
		start := pos + int(b.Padding)
		end := start + int(b.Length)
		if end > len(body) {
			return nil, fmt.Errorf("buffer %d of %d bytes at %d is past the %d bytes of data", i, b.Length, start, len(body))
		}
		out[i] = body[start:end:end]
		pos = end
	}
	return out, nil
}

// spec is the identifier of the encoding of a node.
func (c *context) spec(fb fbs.ArrayNode) (string, error) {
	enc, err := fb.Encoding()
	if err != nil {
		return "", err
	}
	if int(enc) >= len(c.specs) {
		return "", fmt.Errorf("encoding %d, the footer has %d array specs", enc, len(c.specs))
	}
	return c.specs[enc], nil
}

func (c *context) decode(fb fbs.ArrayNode, path string, t DType, rows int) (*Array, error) {
	id, err := c.spec(fb)
	if err != nil {
		return nil, err
	}
	dec, ok := c.table[id]
	if !ok {
		return nil, fmt.Errorf("unsupported encoding %q: %w", id, errors.ErrUnsupported)
	}
	n := &Node{ctx: c, path: path}
	if n.metadata, err = fb.Metadata(); err != nil {
		return nil, err
	}
	if n.children, err = fb.Children(); err != nil {
		return nil, err
	}
	if n.buffers, err = fb.Buffers(); err != nil {
		return nil, err
	}
	a, err := dec(n, t, rows)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	return a, nil
}

// Metadata is the protobuf of the encoding.
func (n *Node) Metadata() []byte { return n.metadata }

// NumChildren is the number of children.
func (n *Node) NumChildren() int { return n.children.Len() }

// NumBuffers is the number of buffers.
func (n *Node) NumBuffers() int { return len(n.buffers) }

// Buffer returns buffer i of the node.
func (n *Node) Buffer(i int) ([]byte, error) {
	if i >= len(n.buffers) {
		return nil, fmt.Errorf("buffer %d is missing, the node has %d", i, len(n.buffers))
	}
	idx := int(n.buffers[i])
	if idx >= len(n.ctx.bufs) {
		return nil, fmt.Errorf("buffer %d is buffer %d of the array, which has %d", i, idx, len(n.ctx.bufs))
	}
	return n.ctx.bufs[idx], nil
}

// Child decodes child i as an array of type t with the given number of rows.
func (n *Node) Child(i int, t DType, rows int) (*Array, error) {
	return n.child(i, n.path, t, rows)
}

// ChildEncoding is the identifier of the encoding of child i. Call it after the child has been
// decoded: it returns "" for a child that cannot be read.
func (n *Node) ChildEncoding(i int) string {
	fb, _ := n.children.At(i)
	id, _ := n.ctx.spec(fb)
	return id
}

func (n *Node) child(i int, path string, t DType, rows int) (*Array, error) {
	fb, err := n.children.At(i)
	if err != nil {
		return nil, err
	}
	return n.ctx.decode(fb, path, t, rows)
}

// Field decodes child i as the struct field called name. When it fails with an unsupported
// encoding and Options.Tolerate accepts it, the result is an Array with Skipped set.
func (n *Node) Field(i int, name string, t DType, rows int) (*Array, error) {
	path := name
	if n.path != "" {
		path = n.path + "." + name
	}
	a, err := n.child(i, path, t, rows)
	if err != nil && errors.Is(err, errors.ErrUnsupported) && n.ctx.opt.Tolerate != nil && n.ctx.opt.Tolerate(path, err) {
		return &Array{Type: t, Len: rows, Skipped: err}, nil
	}
	return a, err
}

// Validity decodes child i as the validity of an array of the given number of rows.
func (n *Node) Validity(i, rows int) (*Bitmap, error) {
	a, err := n.Child(i, DType{Kind: Bool}, rows)
	if err != nil {
		return nil, fmt.Errorf("validity: %w", err)
	}
	return &a.Bits, nil
}

// noMetadata checks that an encoding without metadata has none.
func (n *Node) noMetadata() error {
	if len(n.metadata) != 0 {
		return fmt.Errorf("expects no metadata, got %d bytes", len(n.metadata))
	}
	return nil
}

// optionalValidity reads the validity child of an encoding that has at most one child.
func (n *Node) optionalValidity(rows int) (*Bitmap, error) {
	switch n.NumChildren() {
	case 0:
		return nil, nil
	case 1:
		return n.Validity(0, rows)
	}
	return nil, fmt.Errorf("expected 0 or 1 child, got %d", n.NumChildren())
}
