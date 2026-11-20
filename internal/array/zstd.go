package array

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/fclairamb/vortex-go/internal/protowire"
)

// maxZstdBytes bounds what the frames of one array may decompress to. The size of a frame
// is declared by the file, and a few bytes of zstd can stand for gigabytes.
const maxZstdBytes = 1 << 30

// zstdFrame is the metadata of one frame.
type zstdFrame struct {
	size    int // uncompressed size, in bytes
	nValues int
}

// zstdMeta is the metadata of vortex.zstd: {dictionary_size, frames: [{uncompressed_size,
// n_values}]}.
type zstdMeta struct {
	frames   []zstdFrame
	dictSize int
}

func parseZstdMeta(b []byte) (m zstdMeta, err error) {
	it := protowire.NewIter(b)
	for it.Next() {
		switch {
		case it.Num() == 1 && it.Type() == protowire.Varint:
			if m.dictSize, err = count("dictionary_size", it.Uint()); err != nil {
				return m, err
			}
		case it.Num() == 2 && it.Type() == protowire.Bytes:
			f, err := parseZstdFrame(it.Bytes())
			if err != nil {
				return m, err
			}
			m.frames = append(m.frames, f)
		}
	}
	return m, it.Err()
}

func parseZstdFrame(b []byte) (f zstdFrame, err error) {
	v, err := varints(b, 2)
	if err != nil {
		return f, err
	}
	if f.size, err = count("uncompressed_size", v[0]); err != nil {
		return f, err
	}
	f.nValues, err = count("n_values", v[1])
	return f, err
}

// zstdPlain holds the decoders that have no dictionary. A decoder is reused from one array
// to the next: building one is far dearer than a frame.
var zstdPlain = sync.Pool{New: func() any { return newZstdDecoder() }}

func newZstdDecoder(opts ...zstd.DOption) *zstd.Decoder {
	opts = append(opts, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxZstdBytes))
	d, _ := zstd.NewReader(nil, opts...) // fails on an invalid option only, and ours are fixed
	return d
}

// zstdMagicDict is the magic number a trained dictionary starts with.
const zstdMagicDict = 0xEC30A437

// zstdDecoder returns the decoder of the frames of an array, and what to call when it is done.
func zstdDecoder(dict []byte) (*zstd.Decoder, func(), error) {
	if len(dict) == 0 {
		d := zstdPlain.Get().(*zstd.Decoder)
		return d, func() { zstdPlain.Put(d) }, nil
	}
	var d *zstd.Decoder
	if len(dict) >= 4 && binary.LittleEndian.Uint32(dict) == zstdMagicDict {
		var err error
		d, err = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxZstdBytes),
			zstd.WithDecoderDicts(dict))
		if err != nil {
			return nil, nil, fmt.Errorf("dictionary: %w", err)
		}
	} else {
		d = newZstdDecoder(zstd.WithDecoderDictRaw(0, dict))
	}
	return d, d.Close, nil
}

// decodeZstd decodes vortex.zstd. Child 0 is the validity. The buffers are the dictionary,
// when dictionary_size is above 0, then one per frame. A frame holds the valid rows only: for
// numbers their little-endian values, for strings and binary each value as a u32 length and its
// bytes.
func decodeZstd(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Prim && !isBytes(t) {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	m, err := parseZstdMeta(n.Metadata())
	if err != nil {
		return nil, err
	}
	valid, err := n.optionalValidity(rows)
	if err != nil {
		return nil, err
	}
	nValid := rows
	if valid != nil {
		nValid = countBits(valid, rows)
	}
	dict, first, err := zstdDictionary(n, &m)
	if err != nil {
		return nil, err
	}
	total, err := m.check(t, nValid)
	if err != nil {
		return nil, err
	}
	dec, done, err := zstdDecoder(dict)
	if err != nil {
		return nil, err
	}
	defer done()
	data := make([]byte, total)
	off := 0
	for i, f := range m.frames {
		frame, err := n.Buffer(first + i)
		if err != nil {
			return nil, err
		}
		if err := decodeZstdFrame(dec, frame, data[off:off:off+f.size], i, f.size); err != nil {
			return nil, err
		}
		off += f.size
	}
	a := &Array{Type: t, Len: rows, Validity: valid}
	if t.Kind == Prim {
		a.Data = spreadValues(data, t.PType.Width(), valid, rows)
		return a, nil
	}
	return a, m.strings(a, data, valid)
}

// zstdDictionary returns the dictionary of the array, if any, and the index of the buffer of its
// first frame.
func zstdDictionary(n *Node, m *zstdMeta) (dict []byte, first int, err error) {
	if m.dictSize > 0 {
		if dict, err = n.Buffer(0); err != nil {
			return nil, 0, err
		}
		if len(dict) != m.dictSize {
			return nil, 0, fmt.Errorf("dictionary buffer of %d bytes, the metadata says %d", len(dict), m.dictSize)
		}
		first = 1
	}
	if got := n.NumBuffers() - first; got != len(m.frames) {
		return nil, 0, fmt.Errorf("%d frame buffers, the metadata lists %d frames", got, len(m.frames))
	}
	return dict, first, nil
}

// check validates the frames against the number of valid rows and returns the total size.
func (m *zstdMeta) check(t DType, nValid int) (int, error) {
	total, values := 0, 0
	for i, f := range m.frames {
		if f.size > maxZstdBytes-total {
			return 0, fmt.Errorf("frame %d: the frames decompress to more than %d bytes", i, maxZstdBytes)
		}
		if f.nValues > nValid-values {
			return 0, fmt.Errorf("the frames hold more than the %d valid values", nValid)
		}
		if t.Kind == Prim && f.size != f.nValues*t.PType.Width() {
			return 0, fmt.Errorf("frame %d: %d bytes for %d %s values", i, f.size, f.nValues, t.PType)
		}
		total += f.size
		values += f.nValues
	}
	if values != nValid {
		return 0, fmt.Errorf("the frames hold %d values, there are %d valid rows", values, nValid)
	}
	return total, nil
}

// decodeZstdFrame decompresses a frame into dst, which has room for size bytes and no more.
func decodeZstdFrame(dec *zstd.Decoder, frame, dst []byte, i, size int) error {
	var h zstd.Header
	if err := h.Decode(frame); err != nil {
		return fmt.Errorf("frame %d: %w", i, err)
	}
	if h.HasFCS && h.FrameContentSize != uint64(size) {
		return fmt.Errorf("frame %d: its header declares %d bytes, the metadata %d", i, h.FrameContentSize, size)
	}
	out, err := dec.DecodeAll(frame, dst)
	if err != nil {
		return fmt.Errorf("frame %d: %w", i, err)
	}
	if len(out) != size {
		return fmt.Errorf("frame %d decompresses to %d bytes, the metadata says %d", i, len(out), size)
	}
	return nil
}

// countBits counts the set bits of the first n bits of b.
func countBits(b *Bitmap, n int) int {
	c := 0
	for i := range n {
		if b.Get(i) {
			c++
		}
	}
	return c
}

// spreadValues puts the values of the valid rows back at their rows, the other rows being
// zero. It returns values itself when there is no validity.
func spreadValues(values []byte, width int, valid *Bitmap, rows int) []byte {
	if valid == nil {
		return values
	}
	out := make([]byte, rows*width)
	src := 0
	for i := range rows {
		if valid.Get(i) {
			copy(out[i*width:(i+1)*width], values[src:src+width])
			src += width
		}
	}
	return out
}

// strings reads the values of data, frame after frame, as u32 lengths and bytes, and sets the
// ranges of the rows of a.
func (m *zstdMeta) strings(a *Array, data []byte, valid *Bitmap) error {
	a.Data = data
	a.Starts, a.Ends = make([]int, a.Len), make([]int, a.Len)
	row, off := 0, 0
	for i, f := range m.frames {
		end := off + f.size
		for range f.nValues {
			for valid != nil && !valid.Get(row) {
				row++
			}
			if end-off < 4 {
				return fmt.Errorf("frame %d: a value has no length", i)
			}
			l := int(binary.LittleEndian.Uint32(data[off:]))
			off += 4
			if l > end-off {
				return fmt.Errorf("frame %d: a value of %d bytes runs past the end of the frame", i, l)
			}
			a.Starts[row], a.Ends[row] = off, off+l
			off += l
			row++
		}
		if off != end {
			return fmt.Errorf("frame %d: %d bytes after its %d values", i, end-off, f.nValues)
		}
	}
	return nil
}
