package vortex

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"sync/atomic"

	"github.com/fclairamb/vortex-go/internal/fbs"
)

// The errors Open returns, to test with errors.Is. Their messages carry the byte offset.
var (
	// ErrNotVortex means the file does not start and end with the Vortex magic, or is too short.
	ErrNotVortex = errors.New("vortex: not a Vortex file")
	// ErrUnsupportedVersion means the file has a format version this package does not read.
	ErrUnsupportedVersion = errors.New("vortex: unsupported format version")
	// ErrCorrupt means a part of the file is out of bounds or malformed.
	ErrCorrupt = errors.New("vortex: corrupt file")
)

const (
	magic          = "VTXF"
	trailerSize    = 8
	minFileSize    = len(magic) + trailerSize
	formatVersion  = 1
	maxTail        = 1<<16 - 1
	maxLayoutDepth = 64
)

// File is an open Vortex file. It is safe for concurrent use.
type File struct {
	r      io.ReaderAt
	closer io.Closer
	mapped *mapped // the memory of a file that OpenMapped opened, nil otherwise
	schema *Schema
	layout *layoutNode
	// The dictionaries of the footer, which the layout and the arrays index.
	arrayEncodings  []string
	layoutEncodings []string
	readers         map[string]layoutMaker // the layout readers, by identifier
	segments        []fbs.SegmentSpec
	size            int64
	numRows         int64
	gap, maxRead    int64 // how reads are merged, see mergeLimits
	closeOnce       sync.Once
	closed          atomic.Bool // set by Close, which the scans check at each batch
}

// corrupt wraps ErrCorrupt with where and what.
func corrupt(off int64, format string, args ...any) error {
	return fmt.Errorf("%w: at offset %d: %s", ErrCorrupt, off, fmt.Sprintf(format, args...))
}

// wrapCorrupt wraps a decoding error of the part of the file that starts at off.
func wrapCorrupt(what string, off uint64, err error) error {
	return fmt.Errorf("%w: %s at offset %d: %w", ErrCorrupt, what, off, err)
}

// Open reads the trailer, the footer and the schema of the Vortex file in r, which holds
// size bytes. It reads the last 64 KiB in one call, and one more range when the footer
// reaches further back. A file larger than 64 KiB costs one more read, for its first bytes.
// The caller keeps r open for as long as the File is used.
func Open(r io.ReaderAt, size int64) (*File, error) {
	if size < int64(minFileSize) {
		return nil, fmt.Errorf("%w: %d bytes is too short, at offset 0", ErrNotVortex, size)
	}
	tailLen := min(size, maxTail)
	tail := make([]byte, tailLen)
	if err := readFull(r, tail, size-tailLen); err != nil {
		return nil, err
	}
	head := tail
	if tailLen < size {
		head = make([]byte, len(magic))
		if err := readFull(r, head, 0); err != nil {
			return nil, err
		}
	}
	if string(head[:len(magic)]) != magic {
		return nil, fmt.Errorf("%w: bad magic at offset 0", ErrNotVortex)
	}
	f, err := openTail(r, size, tail)
	if err != nil {
		return nil, err
	}
	f.r = r
	f.gap, f.maxRead = mergeLimits(r)
	return f, nil
}

// readFull reads len(b) bytes at off. A reader that returns fewer bytes without an error is
// called again for the rest; the end of the file before the last byte is corruption.
func readFull(r io.ReaderAt, b []byte, off int64) error {
	for done := 0; done < len(b); {
		n, err := r.ReadAt(b[done:], off+int64(done))
		done += n
		switch {
		case done == len(b):
		case err == nil && n > 0:
		case err == nil:
			return fmt.Errorf("vortex: reading at offset %d: %w", off+int64(done), io.ErrNoProgress)
		case errors.Is(err, io.EOF):
			return corrupt(off+int64(done), "file ends before byte %d", off+int64(len(b)))
		default:
			return fmt.Errorf("vortex: reading at offset %d: %w", off+int64(done), err)
		}
	}
	return nil
}

func openTail(r io.ReaderAt, size int64, tail []byte) (*File, error) {
	tailStart := size - int64(len(tail))
	trailer := tail[len(tail)-trailerSize:]
	if string(trailer[4:]) != magic {
		return nil, fmt.Errorf("%w: bad magic at offset %d", ErrNotVortex, size-int64(len(magic)))
	}
	if v := int(trailer[0]) | int(trailer[1])<<8; v != formatVersion {
		return nil, fmt.Errorf("%w: version %d at offset %d", ErrUnsupportedVersion, v, size-trailerSize)
	}
	psLen := int(trailer[2]) | int(trailer[3])<<8
	if psLen > len(tail)-trailerSize {
		return nil, corrupt(size-trailerSize+2, "postscript of %d bytes in a file of %d", psLen, size)
	}
	psEnd := len(tail) - trailerSize
	psStart := tailStart + int64(psEnd-psLen)
	ps, err := fbs.ReadPostscript(tail[psEnd-psLen : psEnd])
	if err != nil {
		return nil, wrapCorrupt("postscript", uint64(psStart), err)
	}
	segs, err := postscriptSegments(ps, uint64(psStart))
	if err != nil {
		return nil, err
	}
	b, err := newBlob(r, tail, tailStart, segs.lowest(), size)
	if err != nil {
		return nil, err
	}
	return load(b, segs, size)
}

// segment is a located flatbuffer of the file.
type segment struct {
	offset  uint64
	length  uint32
	present bool
}

type segments struct{ dtype, layout, footer segment }

// lowest is the smallest offset of the segments, there is always at least one.
func (s segments) lowest() int64 {
	low := uint64(math.MaxInt64)
	for _, g := range []segment{s.dtype, s.layout, s.footer} {
		if g.present {
			low = min(low, g.offset)
		}
	}
	return int64(low)
}

// postscriptSegments reads the segments of the postscript, and checks they lie in the body
// of the file: after the first magic, before the postscript at psStart.
func postscriptSegments(ps fbs.Postscript, psStart uint64) (segments, error) {
	get := func(name string, fn func() (fbs.PostscriptSegment, bool, error), required bool) (segment, error) {
		s, ok, err := fn()
		if err != nil {
			return segment{}, wrapCorrupt("postscript "+name+" segment", psStart, err)
		}
		if !ok {
			if required {
				return segment{}, corrupt(int64(psStart), "the postscript has no %s segment", name)
			}
			return segment{}, nil
		}
		off, err := s.Offset()
		if err != nil {
			return segment{}, wrapCorrupt("postscript "+name+" segment", psStart, err)
		}
		n, err := s.Length()
		if err != nil {
			return segment{}, wrapCorrupt("postscript "+name+" segment", psStart, err)
		}
		if off < uint64(len(magic)) || off > psStart || uint64(n) > psStart-off {
			return segment{}, corrupt(int64(min(off, math.MaxInt64)),
				"%s segment of %d bytes is outside the file body, which ends at %d", name, n, psStart)
		}
		return segment{offset: off, length: n, present: true}, nil
	}
	var out segments
	var err error
	if out.dtype, err = get("dtype", ps.DType, false); err != nil {
		return out, err
	}
	if out.layout, err = get("layout", ps.Layout, true); err != nil {
		return out, err
	}
	out.footer, err = get("footer", ps.Footer, true)
	return out, err
}

// blob has the bytes of the file from lo to its end.
type blob struct {
	buf []byte
	lo  int64
}

// newBlob has the bytes from low to size, with at most one more read below the tail.
func newBlob(r io.ReaderAt, tail []byte, tailStart, low, size int64) (blob, error) {
	if low >= tailStart {
		return blob{tail, tailStart}, nil
	}
	buf := make([]byte, size-low)
	if err := readFull(r, buf[:tailStart-low], low); err != nil {
		return blob{}, err
	}
	copy(buf[tailStart-low:], tail)
	return blob{buf, low}, nil
}

func (b blob) at(s segment) []byte {
	start := int64(s.offset) - b.lo
	return b.buf[start : start+int64(s.length)]
}

func load(b blob, segs segments, size int64) (*File, error) {
	f := &File{size: size, readers: layoutReaders()}
	foot, err := fbs.ReadFooter(b.at(segs.footer))
	if err != nil {
		return nil, wrapCorrupt("footer", segs.footer.offset, err)
	}
	if err := f.readFooter(foot, segs.footer.offset); err != nil {
		return nil, err
	}
	if f.layout, err = f.readLayout(b.at(segs.layout), segs.layout.offset); err != nil {
		return nil, err
	}
	if f.layout.rowCount > math.MaxInt64 {
		return nil, corrupt(int64(segs.layout.offset), "row count %d is too large", f.layout.rowCount)
	}
	f.numRows = int64(f.layout.rowCount)
	if !segs.dtype.present {
		return nil, fmt.Errorf("vortex: the file has no dtype segment: %w", errors.ErrUnsupported)
	}
	f.schema, err = readSchema(b.at(segs.dtype), segs.dtype.offset)
	return f, err
}

func readSchema(buf []byte, off uint64) (*Schema, error) {
	d, err := fbs.ReadDType(buf, maxDTypeDepth)
	if err != nil {
		return nil, wrapCorrupt("dtype", off, err)
	}
	t, err := typeOf(d)
	if err != nil {
		return nil, wrapCorrupt("dtype", off, err)
	}
	if t.kind != Struct {
		return nil, corrupt(int64(off), "the root type is %s, want a struct", t.kind)
	}
	return &Schema{t}, nil
}

// statFile is a seam for the test of the error path.
var statFile = (*os.File).Stat

// OpenFile opens the Vortex file at path. Close it when done.
func OpenFile(path string) (*File, error) {
	osf, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := statFile(osf)
	if err != nil {
		_ = osf.Close()
		return nil, err
	}
	f, err := Open(osf, st.Size())
	if err != nil {
		_ = osf.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	f.closer = osf
	return f, nil
}

// Schema returns the schema of the file.
func (f *File) Schema() *Schema { return f.schema }

// NumRows returns the number of rows of the file.
func (f *File) NumRows() int64 { return f.numRows }

// Close closes the file that OpenFile opened: a scan still running returns an error that
// wraps os.ErrClosed. It does nothing for a File made by Open. A second call returns nil.
func (f *File) Close() error {
	var err error
	f.closeOnce.Do(func() {
		if f.closer != nil {
			f.closed.Store(true)
			err = f.closer.Close()
		}
	})
	return err
}
