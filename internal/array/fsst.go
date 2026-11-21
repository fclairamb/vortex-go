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
	var p fsstParts
	if n.NumBuffers() == 3 {
		err = p.current(n, &m, lp, rows)
	} else {
		err = p.legacy(n, t, lp, rows)
	}
	if err != nil {
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

// fsstParts is what the children of an FSST array say about its rows.
type fsstParts struct {
	valid        *Bitmap
	starts, ends []int // per row, the range of codes
	ulen         []int // per row, the declared length
	codes        []byte
}

// current reads the form with 3 buffers.
func (p *fsstParts) current(n *Node, m *message, lp PType, rows int) error {
	cp, err := intType("codes_offsets_ptype", m.varint[2])
	if err != nil {
		return err
	}
	if c := n.NumChildren(); c != 2 && c != 3 {
		return fmt.Errorf("expected 2 or 3 children, got %d", c)
	}
	if p.codes, err = n.Buffer(2); err != nil {
		return err
	}
	if p.ulen, err = n.ints(0, "uncompressed lengths", lp, rows); err != nil {
		return err
	}
	offs, err := n.ints(1, "codes offsets", cp, rows+1)
	if err != nil {
		return err
	}
	if err := checkOffsets(offs, len(p.codes)); err != nil {
		return err
	}
	p.starts, p.ends = offs[:rows], offs[1:]
	if n.NumChildren() == 3 {
		p.valid, err = n.Validity(2, rows)
	}
	return err
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
// bytes past it are overwritten by what follows. A code with no symbol has a length of 0: its
// length minus one has every high bit set, which bad keeps for the end. The other codes, the
// ones around an escape and the last ones, are decoded one by one, with every test.
func (tbl *symbolTable) bulk(dst, codes []byte) int {
	var bad uint
	o := 0
	for c := 0; c < len(codes); {
		if c+8 <= len(codes) && o+8*fsstMaxSymbol <= len(dst) {
			w := binary.LittleEndian.Uint64(codes[c:])
			if x := ^w; (x-fsstLow)&^x&fsstHigh == 0 { // no byte of w is 255
				l := uint(tbl.len[byte(w)])
				unsafex.Store64(dst, o, tbl.sym[byte(w)])
				o, bad = o+int(l), bad|(l-1)
				l = uint(tbl.len[byte(w>>8)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>8)])
				o, bad = o+int(l), bad|(l-1)
				l = uint(tbl.len[byte(w>>16)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>16)])
				o, bad = o+int(l), bad|(l-1)
				l = uint(tbl.len[byte(w>>24)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>24)])
				o, bad = o+int(l), bad|(l-1)
				l = uint(tbl.len[byte(w>>32)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>32)])
				o, bad = o+int(l), bad|(l-1)
				l = uint(tbl.len[byte(w>>40)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>40)])
				o, bad = o+int(l), bad|(l-1)
				l = uint(tbl.len[byte(w>>48)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>48)])
				o, bad = o+int(l), bad|(l-1)
				l = uint(tbl.len[byte(w>>56)])
				unsafex.Store64(dst, o, tbl.sym[byte(w>>56)])
				o, bad, c = o+int(l), bad|(l-1), c+8
				continue
			}
		}
		if o, c = tbl.one(dst, codes, o, c); o < 0 {
			return -1
		}
	}
	if bad >= fsstMaxSymbol {
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
