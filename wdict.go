package vortex

import (
	"encoding/binary"

	"github.com/fclairamb/vortex-go/internal/flatbuf"
)

// The dictionary layout (vortex-layout/src/layouts/dict/writer.rs): a column of numbers,
// strings or binary whose first block has few distinct values is written as runs. A run is
// a set of values, and the codes of the rows that use it; a new run starts when the set is
// full.

// The limits of a dictionary: variables so that the tests can lower them.
var (
	maxDictLen   = 1<<16 - 1 // the values
	maxDictBytes = 1 << 20   // the bytes of the values, a view of 16 bytes each for strings
)

// dictEligible says whether a column of type t can be written as a dictionary.
func dictEligible(t Type) bool {
	switch t.kind {
	case Utf8, Binary, Float16, Float32, Float64:
		return true
	}
	return t.kind >= Int8 && t.kind <= Uint64
}

// codeWidth is the bytes of a code: a column of bytes that cannot be null has at most 256
// values and u8 codes, the others u16 codes.
func (c *column) codeWidth() int {
	if c.t.kind == Utf8 || c.t.kind == Binary || c.t.nullable || numberWidth(storageKind(c.t)) > 1 {
		return 2
	}
	return 1
}

// decide chooses between plain chunks and a dictionary, from the rows lo to hi of the
// buffer, the first of the column: a dictionary is used when the compressor would pick one for
// them, as the reference does. When the chunks are not compressed (and for a Float16), it is
// used when fewer than half of them are distinct, a null counting as a value.
func (c *column) decide(w *Writer, lo, hi int) {
	c.mode = modePlain
	if !c.eligible || hi == lo {
		return
	}
	if w.compressing() && c.t.kind != Float16 {
		if w.probeDict(c, lo, hi) {
			c.startDict()
		}
		return
	}
	seen := map[string]struct{}{}
	nulls := false
	for i := lo; i < hi; i++ {
		if c.t.nullable && c.buf.nulls[i] {
			nulls = true
		} else {
			if key := dictKey(c, i); !has(seen, key) {
				seen[string(key)] = struct{}{}
			}
		}
		if 2*(len(seen)+boolInt(nulls)) >= hi-lo {
			return
		}
	}
	c.startDict()
}

// startDict makes the column a dictionary.
func (c *column) startDict() {
	c.mode = modeDict
	c.dict = &dictEnc{idx: map[string]int{}, nullCode: -1, width: c.codeWidth()}
}

// dictKey is the bytes that make the value of row i.
func dictKey(c *column, i int) []byte {
	if c.buf.kind == dBytes {
		return c.buf.bins[i]
	}
	w := numberWidth(c.buf.pk)
	return c.buf.raw[i*w : (i+1)*w]
}

// dictEnc is the run being built.
type dictEnc struct {
	idx      map[string]int
	raw      []byte   // the values of a column of numbers
	vals     [][]byte // the values of a column of strings
	arena    []byte
	nulls    []bool // the null values, nil until there is one
	codes    []byte
	chunks   []chunk // the chunks of codes written
	nullCode int     // the code of null, -1 when there is none
	n        int     // the values
	bytes    int     // the size of the values
	rows     int     // the rows of the run
	width    int     // the bytes of a code
}

// code returns the code of row i of the buffer, and false when the run is full: the value
// is not in it and there is no room. The first value of a run is always taken.
func (d *dictEnc) code(c *column, i int) (int, bool) {
	if c.t.nullable && c.buf.nulls[i] {
		if d.nullCode >= 0 {
			return d.nullCode, true
		}
		return d.add(c, nil, true)
	}
	key := dictKey(c, i)
	if code, ok := d.idx[string(key)]; ok {
		return code, true
	}
	return d.add(c, key, false)
}

// add adds a value, when the run has room for it.
func (d *dictEnc) add(c *column, key []byte, null bool) (int, bool) {
	cost := 16
	if c.buf.kind == dPrim {
		cost = numberWidth(c.buf.pk)
	} else if len(key) > maxInline {
		cost += len(key)
	}
	if d.n > 0 && (d.n >= maxDictLen || d.bytes+cost > maxDictBytes) {
		return 0, false
	}
	code := d.n
	if null {
		d.nullCode = code
		d.nulls = append(d.nulls, make([]bool, code-len(d.nulls))...)
		d.nulls = append(d.nulls, true)
	} else {
		d.idx[string(key)] = code
		if d.nulls != nil {
			d.nulls = append(d.nulls, false)
		}
	}
	if c.buf.kind == dPrim {
		d.raw = append(d.raw, make([]byte, numberWidth(c.buf.pk))...)
		copy(d.raw[len(d.raw)-len(key):], key)
	} else {
		d.vals = append(d.vals, copyBytes(&d.arena, key))
	}
	d.n++
	d.bytes += cost
	return code, true
}

// codeRows codes the rows lo to hi of the buffer, up to the first one that needs a write: it
// returns the row it stopped at, hi when there is none. full says that it stopped after a row
// whose code fills a chunk of codes; otherwise the row it stopped at does not fit the run. It
// only touches the column.
func (d *dictEnc) codeRows(c *column, lo, hi int) (next int, full bool) {
	for i := lo; i < hi; i++ {
		code, ok := d.code(c, i)
		if !ok {
			return i, false
		}
		if d.width == 1 {
			d.codes = append(d.codes, byte(code))
		} else {
			d.codes = binary.LittleEndian.AppendUint16(d.codes, uint16(code))
		}
		d.rows++
		if len(d.codes) >= chunkBytes {
			return i + 1, true
		}
	}
	return hi, false
}

// encodeDict ends the coding of the rows of the buffer up to hi, which prepare began: it writes
// the chunk of codes that is full, or ends the run that has no room for a row, and codes on.
func (w *Writer) encodeDict(c *column, hi int) {
	for i, full := c.coded, c.codesFull; w.err == nil; i, full = c.dict.codeRows(c, i, hi) {
		switch {
		case full:
			w.flushCodes(c)
		case i < hi:
			w.endRun(c) // the first value of a run is always taken
		default:
			return
		}
	}
}

// flushCodes writes the codes gathered as a flat chunk.
func (w *Writer) flushCodes(c *column) {
	d := c.dict
	n := len(d.codes) / d.width
	if n == 0 {
		return
	}
	t, k := Uint16Type(), Uint16
	if d.width == 1 {
		t, k = Uint8Type(), Uint8
	}
	w.start()
	w.beginFlat()
	var root flatbuf.Off
	w.withCompressor(c, func() { root = w.encode(t, Data{&body{kind: dPrim, pk: k, n: n, raw: d.codes}}, n) })
	if seg, ok := w.endFlat(root); ok {
		d.chunks = append(d.chunks, chunk{rows: uint64(n), seg: seg})
	}
	d.codes = d.codes[:0]
}

// endRun writes the codes and the values of the run, and starts a new one.
func (w *Writer) endRun(c *column) {
	d := c.dict
	w.flushCodes(c)
	vals := Data{&body{kind: dPrim, pk: c.buf.pk, n: d.n, raw: d.raw, nulls: d.nulls}}
	if c.buf.kind == dBytes {
		vals = Data{&body{kind: dBytes, n: d.n, bins: d.vals, nulls: d.nulls}}
	}
	w.beginFlat()
	var root flatbuf.Off
	w.withCompressor(c, func() { root = w.encode(c.t, vals, d.n) })
	if seg, ok := w.endFlat(root); ok {
		c.pieces = append(c.pieces, piece{rows: uint64(d.rows), seg: seg, nvals: d.n, codes: d.chunks})
	}
	clear(d.idx)
	clear(d.vals)
	d.raw, d.vals, d.arena, d.nulls, d.chunks = d.raw[:0], d.vals[:0], d.arena[:0], nil, nil
	d.nullCode, d.n, d.bytes, d.rows = -1, 0, 0, 0
}

// has says whether the map has the key, without allocating a string for the lookup.
func has(m map[string]struct{}, key []byte) bool {
	_, ok := m[string(key)]
	return ok
}
