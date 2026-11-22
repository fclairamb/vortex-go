package array

import (
	"encoding/binary"
	"fmt"

	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// fsstEscape is the code that says the next byte is a literal.
const fsstEscape = 255

// fsstMaxSymbol is the longest symbol.
const fsstMaxSymbol = 8

// decodeFSST decodes vortex.fsst: metadata {uncompressed_lengths_ptype, codes_offsets_ptype}.
//
// The current form has 3 buffers (the symbols, as little-endian u64, their lengths, the code
// bytes) and the children uncompressed lengths, code offsets (rows + 1) and an optional
// validity. The older form has 2 buffers (the symbols and their lengths) and the children
// codes, a varbin, then uncompressed lengths: its validity is the one of the codes.
func decodeFSST(n *Node, t DType, rows int) (*Array, error) {
	m, lp, tbl, err := n.fsstHead(t)
	if err != nil {
		return nil, err
	}
	if n.NumBuffers() == 3 {
		return n.fsstCurrent(t, &m, lp, tbl, rows)
	}
	var p fsstParts
	if err := p.legacy(n, t, lp, rows); err != nil {
		return nil, err
	}
	a := &Array{Type: t, Len: rows, Validity: p.valid}
	codes, starts, ends, ulen := p.codes, p.starts, p.ends, p.ulen
	total, err := outputSize(ulen, starts, ends, fsstMaxSymbol)
	if err != nil {
		return nil, err
	}
	ar := n.ctx.opt.Arena
	a.Data = ar.Bytes(total, false)
	a.Starts, a.Ends = ar.Ints(rows, false), ar.Ints(rows, false)
	if err := tbl.decode(a.Data, a.Starts, a.Ends, codes, starts, ends, ulen); err != nil {
		return nil, err
	}
	return a, nil
}

// fsstHead reads what a vortex.fsst node of an array of type t says before its rows: its
// metadata, the type of the lengths of the rows, and the symbols.
func (n *Node) fsstHead(t DType) (m message, lp PType, tbl *symbolTable, err error) {
	if !isBytes(t) {
		return m, 0, nil, fmt.Errorf("cannot be a %s", t)
	}
	if m, err = readMessage(n.Metadata()); err != nil {
		return m, 0, nil, err
	}
	if lp, err = intType("uncompressed_lengths_ptype", m.varint[1]); err != nil {
		return m, 0, nil, err
	}
	if nb := n.NumBuffers(); nb != 2 && nb != 3 {
		return m, 0, nil, fmt.Errorf("expected 2 or 3 buffers, got %d", nb)
	}
	syms, err := n.Buffer(0)
	if err != nil {
		return m, 0, nil, err
	}
	lens, err := n.Buffer(1)
	if err != nil {
		return m, 0, nil, err
	}
	tbl, err = newSymbolTable(syms, lens)
	return m, lp, tbl, err
}

// fsstParts is what the children of an FSST array of the older form say about its rows.
type fsstParts struct {
	valid        *Bitmap
	starts, ends []int // per row, the range of codes
	ulen         []int // per row, the declared length
	codes        []byte
}

// fsstCurrent decodes the form with 3 buffers. The lengths of the rows are read into the ends
// of the rows, which one pass over the offsets of the codes turns into the ranges of the rows,
// checking both: the lengths and the offsets are read where their child decoded them.
func (n *Node) fsstCurrent(t DType, m *message, lp PType, tbl *symbolTable, rows int) (*Array, error) {
	cp, err := intType("codes_offsets_ptype", m.varint[2])
	if err != nil {
		return nil, err
	}
	if c := n.NumChildren(); c != 2 && c != 3 {
		return nil, fmt.Errorf("expected 2 or 3 children, got %d", c)
	}
	codes, err := n.Buffer(2)
	if err != nil {
		return nil, err
	}
	lens, err := n.denseChild(0, "uncompressed lengths", lp, rows)
	if err != nil {
		return nil, err
	}
	offs, err := n.denseChild(1, "codes offsets", cp, rows+1)
	if err != nil {
		return nil, err
	}
	ar := n.ctx.opt.Arena
	a := &Array{Type: t, Len: rows}
	a.Starts, a.Ends = ar.Ints(rows, false), ar.Ints(rows, false)
	if !fillInts(a.Ends, lens.Data, lp) {
		_, err := toInts(nil, lens, "uncompressed lengths", lp, nil)
		return nil, err
	}
	total, first, last, err := fsstRowsOf(a.Starts, a.Ends, offs.Data, cp, len(codes))
	if err != nil {
		return nil, err
	}
	if n.NumChildren() == 3 {
		if a.Validity, err = n.Validity(2, rows); err != nil {
			return nil, err
		}
	}
	a.Data = ar.Bytes(total, false)
	// The codes of the rows follow each other: they are decoded in one pass, and the rows
	// are the declared lengths (see decode). When they do not decode to the sum of the
	// lengths, the rows are decoded one by one, to say which one is wrong.
	if tbl.bulk(a.Data, codes[first:last]) != total {
		offsets, _ := toInts(nil, offs, "codes offsets", cp, nil) // fsstRows checked them
		ulen := make([]int, rows)
		for i := range ulen {
			ulen[i] = a.Ends[i] - a.Starts[i]
		}
		if err := tbl.decode(a.Data, a.Starts, a.Ends, codes, offsets[:rows], offsets[1:], ulen); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// fsstRowsOf is fsstRows for offsets of type p, in data.
func fsstRowsOf(starts, ends []int, data []byte, p PType, n int) (total, first, last int, err error) {
	switch p {
	case U8:
		return fsstRows(starts, ends, data, n)
	case I8:
		return fsstRows(starts, ends, words[int8](data), n)
	case U16:
		return fsstRows(starts, ends, words[uint16](data), n)
	case I16:
		return fsstRows(starts, ends, words[int16](data), n)
	case U32:
		return fsstRows(starts, ends, words[uint32](data), n)
	case I32:
		return fsstRows(starts, ends, words[int32](data), n)
	case U64:
		return fsstRows(starts, ends, words[uint64](data), n)
	}
	return fsstRows(starts, ends, words[int64](data), n)
}

// fsstRows turns the lengths of the rows, in ends, into the ranges of the rows in the decoded
// bytes, and checks the offsets of the codes of the rows, offs, against them: the offsets do
// not decrease, stay within the n codes, and a row is not longer than its codes can make. It
// returns the sum of the lengths, and the range of the codes of all the rows.
//
// The checks of a row are folded into the sign of bad, with no branch: the codes of the row,
// what is left of the n codes after them, and what its codes can make minus its length are
// not negative. A row past the n codes may make a wrong product, but its other term is
// negative. When one of them is, fsstRowError says which row is wrong.
func fsstRows[O integer](starts, ends []int, offs []O, n int) (total, first, last int, err error) {
	offs, starts = offs[:len(ends)+1], starts[:len(ends)]
	prev := int(offs[0])
	first, bad := prev, prev|(n-prev)
	for i, l := range ends {
		next := int(offs[i+1])
		d := next - prev
		bad |= d | (n - next) | (d*fsstMaxSymbol - l)
		starts[i], total = total, total+l
		ends[i], prev = total, next
	}
	if bad < 0 {
		for i := range ends { // the lengths back
			ends[i] -= starts[i]
		}
		return 0, 0, 0, fsstRowError(ends, offs, n)
	}
	return total, first, prev, nil
}

// fsstRowError is the error of the first wrong row of fsstRows, ends holding the lengths.
func fsstRowError[O integer](ends []int, offs []O, n int) error {
	prev := int(offs[0])
	if prev < 0 {
		return fmt.Errorf("offset 0 is %d, after 0", prev)
	}
	for i, l := range ends {
		next := int(offs[i+1])
		if next < prev {
			return fmt.Errorf("offset %d is %d, after %d", i+1, next, prev)
		}
		// The codes past the n there are are checked at the end: they make no bytes here.
		if limit := (min(next, n) - min(prev, n)) * fsstMaxSymbol; l > limit {
			return fmt.Errorf("row %d is declared %d bytes, its codes hold at most %d", i, l, limit)
		}
		prev = next
	}
	return fmt.Errorf("offset %d is past the %d elements", prev, n)
}

// denseChild decodes child i of the node as rows integers of type p with no null.
func (n *Node) denseChild(i int, what string, p PType, rows int) (*Array, error) {
	a, err := n.Child(i, DType{Kind: Prim, PType: p}, rows)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if a.Validity != nil {
		return nil, fmt.Errorf("%s: has nulls", what)
	}
	return a, nil
}

// legacy reads the form with 2 buffers.
func (p *fsstParts) legacy(n *Node, t DType, lp PType, rows int) error {
	if n.NumChildren() != 2 {
		return fmt.Errorf("expected 2 children, got %d", n.NumChildren())
	}
	c, err := n.Child(0, DType{Kind: Binary, Nullable: t.Nullable}, rows)
	if err != nil {
		return fmt.Errorf("codes: %w", err)
	}
	p.codes, p.starts, p.ends, p.valid = c.Data, c.Starts, c.Ends, c.Validity
	p.ulen, err = n.ints(1, "uncompressed lengths", lp, rows)
	return err
}

// outputSize checks the declared lengths against the code ranges and returns their sum. A
// code emits at most perCode bytes, so a row longer than that is hostile: the output is never
// sized from a length the file cannot back.
func outputSize(ulen, starts, ends []int, perCode int) (int, error) {
	total := 0
	for i, l := range ulen {
		if limit := (ends[i] - starts[i]) * perCode; l > limit {
			return 0, fmt.Errorf("row %d is declared %d bytes, its codes hold at most %d", i, l, limit)
		}
		total += l // at most 8 times the codes of a row: the sum cannot overflow
	}
	return total, nil
}

// symbolTable is the symbols of an FSST array.
type symbolTable struct {
	sym [256]uint64 // little-endian: the first byte of the symbol is the low one
	len [256]uint8  // 0 for the escape, and for a code that has no symbol
	n   int
	// past is 256-n in every byte: added to a code, it carries out of the byte when the code
	// has no symbol (bulk). 0 when there is no symbol.
	past uint64
}

// newSymbolTable reads the symbols (8 bytes each) and their lengths (one byte each).
func newSymbolTable(syms, lens []byte) (*symbolTable, error) {
	if len(syms)%8 != 0 {
		return nil, fmt.Errorf("symbols buffer of %d bytes is not a multiple of 8", len(syms))
	}
	n := len(syms) / 8
	if n > fsstEscape {
		return nil, fmt.Errorf("%d symbols, at most %d", n, fsstEscape)
	}
	if len(lens) != n {
		return nil, fmt.Errorf("%d symbols and %d symbol lengths", n, len(lens))
	}
	tbl := &symbolTable{n: n}
	if n > 0 {
		tbl.past = uint64(256-n) * fsstLow
	}
	for i := range n {
		l := lens[i]
		if l < 1 || l > fsstMaxSymbol {
			return nil, fmt.Errorf("symbol %d is %d bytes long, want 1 to %d", i, l, fsstMaxSymbol)
		}
		tbl.sym[i], tbl.len[i] = binary.LittleEndian.Uint64(syms[8*i:]), l
	}
	return tbl, nil
}

// decode decodes row i, the codes[starts[i]:ends[i]], to ulen[i] bytes of dst, one row after
// the other, and sets the ranges of the rows. dst holds exactly the sum of the lengths.
//
// When the codes of the rows follow each other, which is how they are written, they are
// decoded in one pass by bulk, as the reference reader does: the rows are then the declared
// lengths, and only their sum is checked. When the sum is not what the codes decode to, or
// bulk refuses a code, the rows are decoded one by one, to say which one is wrong.
func (tbl *symbolTable) decode(dst []byte, outStarts, outEnds []int, codes []byte, starts, ends, ulen []int) error {
	o, next, chained := 0, 0, len(ulen) > 0
	for i, l := range ulen {
		outStarts[i], outEnds[i] = o, o+l
		o += l
		chained = chained && (i == 0 || starts[i] == next)
		next = ends[i]
	}
	if chained && tbl.bulk(dst, codes[starts[0]:next]) == len(dst) {
		return nil
	}
	o = 0
	for i, l := range ulen {
		if err := tbl.row(dst, o, o+l, codes[starts[i]:ends[i]], i); err != nil {
			return err
		}
		o += l
	}
	return nil
}

// fsstLow and fsstHigh are the low bit and the high bit of each byte of a word.
const (
	fsstLow  = 0x0101010101010101
	fsstHigh = 0x8080808080808080
)

// bulk decodes codes to dst and returns the number of bytes it wrote, or -1 when it cannot
// decode them: a code has no symbol, the codes end with an escape, or dst is too short.
//
// The codes are read 8 at a time. A word of 8 codes with no escape, when dst has 64 bytes
// left, is 8 stores of 8 bytes with no test in between: a symbol is at most 8 bytes, the
// bytes past it are overwritten by what follows. A code with no symbol is one of n or more:
// added to 256-n, it carries out of its byte, which a few operations on the word find for its
// 8 codes at once, and bad keeps for the end. The other codes, the ones around an escape and
// the last ones, are decoded one by one, with every test.
func (tbl *symbolTable) bulk(dst, codes []byte) int {
	var bad uint64
	past, pastLow := tbl.past, tbl.past&^fsstHigh
	o := 0
	for c := 0; c < len(codes); {
		if c+8 <= len(codes) && o+8*fsstMaxSymbol <= len(dst) && past != 0 {
			w := binary.LittleEndian.Uint64(codes[c:])
			if x := ^w; (x-fsstLow)&^x&fsstHigh == 0 { // no byte of w is 255
				// The carry out of bit 7 of each byte of w+past, from the bits 7 of w and past,
				// and the carry into it, which is bit 7 of the sum of their low 7 bits.
				bad |= w&past | (w|past)&((w&^fsstHigh)+pastLow)
				unsafex.Store64(dst, o, tbl.sym[byte(w)])
				o += int(tbl.len[byte(w)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>8)])
				o += int(tbl.len[byte(w>>8)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>16)])
				o += int(tbl.len[byte(w>>16)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>24)])
				o += int(tbl.len[byte(w>>24)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>32)])
				o += int(tbl.len[byte(w>>32)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>40)])
				o += int(tbl.len[byte(w>>40)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>48)])
				o += int(tbl.len[byte(w>>48)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>56)])
				o, c = o+int(tbl.len[byte(w>>56)]), c+8
				continue
			}
		}
		if o, c = tbl.one(dst, codes, o, c); o < 0 {
			return -1
		}
	}
	if bad&fsstHigh != 0 {
		return -1
	}
	return o
}

// one decodes the code at codes[c] to dst[o:], with every test, and returns where the next
// byte goes and the next code. It returns -1 for a code that bulk cannot decode.
func (tbl *symbolTable) one(dst, codes []byte, o, c int) (int, int) {
	code := codes[c]
	if code == fsstEscape {
		if c+1 == len(codes) || o == len(dst) {
			return -1, c
		}
		dst[o] = codes[c+1]
		return o + 1, c + 2
	}
	l := int(tbl.len[code])
	if l == 0 || o+l > len(dst) {
		return -1, c
	}
	s := tbl.sym[code]
	if o+8 <= len(dst) {
		binary.LittleEndian.PutUint64(dst[o:], s)
	} else {
		for k := range l {
			dst[o+k] = byte(s >> (8 * k))
		}
	}
	return o + l, c + 1
}

// row decodes the codes of row i to dst[o:rowEnd], with every check.
//
// A symbol is written as 8 bytes when dst has room for them: the bytes past the symbol are
// then overwritten by what follows. Near the end of dst, it is written byte by byte. A row
// that decodes to more than its bytes is seen at its end, or at the end of dst.
func (tbl *symbolTable) row(dst []byte, o, rowEnd int, row []byte, i int) error {
	start := o
	for c := 0; c < len(row); c++ {
		code := row[c]
		l := int(tbl.len[code])
		if l != 0 && o+8 <= len(dst) {
			binary.LittleEndian.PutUint64(dst[o:], tbl.sym[code])
			o += l
			continue
		}
		switch {
		case code == fsstEscape:
			if c++; c == len(row) {
				return fmt.Errorf("row %d ends with an escape", i)
			}
			if o >= rowEnd {
				return fmt.Errorf("row %d decodes to more than its %d bytes", i, rowEnd-start)
			}
			dst[o] = row[c]
			o++
		case l == 0:
			return fmt.Errorf("row %d has code %d, there are %d symbols", i, code, tbl.n)
		default: // the last bytes of dst
			if o+l > rowEnd {
				return fmt.Errorf("row %d decodes to more than its %d bytes", i, rowEnd-start)
			}
			s := tbl.sym[code]
			for k := range l {
				dst[o+k] = byte(s >> (8 * k))
			}
			o += l
		}
	}
	if o > rowEnd {
		return fmt.Errorf("row %d decodes to more than its %d bytes", i, rowEnd-start)
	}
	if o != rowEnd {
		return fmt.Errorf("row %d decodes to %d bytes, declared %d", i, o-start, rowEnd-start)
	}
	return nil
}
