package array

import (
	"encoding/binary"
	"fmt"

	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// onPairMaxToken is the longest token of a dictionary the writers make: the codes of such a
// dictionary are decoded 16 bytes at a time (bulk).
const onPairMaxToken = 16

// decodeOnPair decodes vortex.onpair. The metadata is {1: uncompressed_lengths_ptype,
// 3: dict_size, 4: codes_len, 5: dict_offsets_ptype, 6: codes_ptype, 7: codes_offsets_ptype}.
// Buffer 0 is the bytes of the dictionary; the children are the dictionary offsets
// (dict_size + 1), the codes (codes_len), the code offsets (rows + 1), the uncompressed
// lengths and an optional validity.
//
// The codes of the rows follow each other: they are decoded in one pass, as the reference
// reader does, and the rows are then the declared lengths, whose sum is checked. When the
// codes do not decode to it, the rows are decoded one by one, to say which one is wrong.
func decodeOnPair(n *Node, t DType, rows int) (*Array, error) {
	d, ptypes, codesLen, err := n.onPairHead(t)
	if err != nil {
		return nil, err
	}
	codes, err := n.denseChild(1, "codes", ptypes[2], codesLen)
	if err != nil {
		return nil, err
	}
	offs, err := n.denseChild(2, "codes offsets", ptypes[3], rows+1)
	if err != nil {
		return nil, err
	}
	lens, err := n.denseChild(3, "uncompressed lengths", ptypes[0], rows)
	if err != nil {
		return nil, err
	}
	ar := n.ctx.opt.Arena
	a := &Array{Type: t, Len: rows}
	if n.NumChildren() == 5 {
		if a.Validity, err = n.Validity(4, rows); err != nil {
			return nil, err
		}
	}
	a.Starts, a.Ends = ar.Ints(rows, false), ar.Ints(rows, false)
	if d.fast && fillInts(a.Ends, lens.Data, ptypes[0]) {
		total, first, last, err := rowsOfCodes(a.Starts, a.Ends, offs.Data, ptypes[3], codesLen, onPairMaxToken)
		if err == nil {
			if a.Data = d.bulkOf(ar, codes.Data, ptypes[2], first, last, total); a.Data != nil {
				return a, nil
			}
		}
	}
	return a, n.onPairSlow(d, a, codes, offs, lens, ptypes)
}

// onPairSlow decodes the rows of a one by one, with every check: the codes of a dictionary
// that has a token of more than 16 bytes, and the ones bulk refused.
func (n *Node) onPairSlow(d *onPairDict, a *Array, codesA, offsA, lensA *Array, ptypes [4]PType) error {
	ar := n.ctx.opt.Arena
	codes, err := toInts(ar, codesA, "codes", ptypes[2], nil)
	if err != nil {
		return err
	}
	offs, err := toInts(ar, offsA, "codes offsets", ptypes[3], nil)
	if err != nil {
		return err
	}
	if err = checkOffsets(offs, len(codes)); err != nil {
		return fmt.Errorf("codes offsets: %w", err)
	}
	ulen, err := toInts(ar, lensA, "uncompressed lengths", ptypes[0], nil)
	if err != nil {
		return err
	}
	total, err := outputSize(ulen, offs[:a.Len], offs[1:], d.maxTok)
	if err != nil {
		return err
	}
	a.Data = ar.Bytes(total, false)
	return d.decode(a.Data, a.Starts, a.Ends, codes, offs[:a.Len], offs[1:], ulen)
}

// onPairHead reads what a vortex.onpair node of an array of type t says before its rows: the
// dictionary, the types of its integer children (see onPairMeta) and the number of codes.
func (n *Node) onPairHead(t DType) (d *onPairDict, ptypes [4]PType, codesLen int, err error) {
	if !isBytes(t) {
		return nil, ptypes, 0, fmt.Errorf("cannot be a %s", t)
	}
	m, err := readMessage(n.Metadata())
	if err != nil {
		return nil, ptypes, 0, err
	}
	ptypes, dictSize, codesLen, err := onPairMeta(&m)
	if err != nil {
		return nil, ptypes, 0, err
	}
	if n.NumBuffers() != 1 {
		return nil, ptypes, 0, fmt.Errorf("expected 1 buffer, got %d", n.NumBuffers())
	}
	if c := n.NumChildren(); c != 4 && c != 5 {
		return nil, ptypes, 0, fmt.Errorf("expected 4 or 5 children, got %d", c)
	}
	dict, err := n.Buffer(0)
	if err != nil {
		return nil, ptypes, 0, err
	}
	if dictSize > len(dict) {
		// A token is at least 1 byte; this also keeps the offsets from being sized by the file.
		return nil, ptypes, 0, fmt.Errorf("dict_size is %d, the dictionary has %d bytes", dictSize, len(dict))
	}
	dictOffs, err := n.ints(0, "dictionary offsets", ptypes[1], dictSize+1)
	if err != nil {
		return nil, ptypes, 0, err
	}
	if err = checkOffsets(dictOffs, len(dict)); err != nil {
		return nil, ptypes, 0, fmt.Errorf("dictionary offsets: %w", err)
	}
	d = &onPairDict{bytes: dict, offs: dictOffs}
	for i := range dictSize {
		d.maxTok = max(d.maxTok, dictOffs[i+1]-dictOffs[i])
	}
	// The reference pads its dictionary with the bytes that the last token is read with. A
	// dictionary without them is read 16 bytes at a time all the same, from a copy that has them.
	if d.fast = d.maxTok <= onPairMaxToken; d.fast && dictOffs[dictSize]+onPairMaxToken > len(dict) {
		d.bytes = n.ctx.opt.Arena.Bytes(len(dict)+onPairMaxToken, false)
		clear(d.bytes[copy(d.bytes, dict):])
	}
	return d, ptypes, codesLen, nil
}

// onPairDict is the dictionary of an OnPair array: token i is bytes[offs[i]:offs[i+1]].
type onPairDict struct {
	bytes  []byte
	offs   []int
	maxTok int // the bytes of the longest token
	// fast says that no token is longer than 16 bytes, and that 16 bytes can be read at the
	// start of every token: bulk decodes the codes.
	fast bool
}

// bulkOf decodes the codes first to last of data, integers of type p, to total bytes of the
// arena, in one pass. It returns nil when it cannot: the codes are not 8 or 16 bits wide, one
// of them is not a token, or they do not decode to total bytes.
func (d *onPairDict) bulkOf(ar *Arena, data []byte, p PType, first, last, total int) []byte {
	// 16 bytes are written for a token: the last one may write up to 15 bytes past the rows.
	dst := ar.Bytes(total+onPairMaxToken, false)
	got := -1
	switch p {
	case U8:
		got = onPairBulk(d, dst, data[first:last])
	case U16:
		got = onPairBulk(d, dst, words[uint16](data)[first:last])
	}
	if got != total {
		return nil
	}
	return dst[:total:total]
}

// onPairBulk decodes codes to dst and returns the number of bytes it wrote, or -1 when a
// code is not a token or dst is too short: dst is 16 bytes longer than what the codes are
// declared to make.
//
// A token is written as 16 bytes, whatever its length: the bytes past it are overwritten by
// what follows, and the dictionary has 16 bytes to read at the start of every token. As many
// tokens as dst has room for at 16 bytes each are written with no test of dst in between.
func onPairBulk[C uint8 | uint16](d *onPairDict, dst []byte, codes []C) int {
	offs, src := d.offs, d.bytes
	ntok := len(offs) - 1
	o := 0
	for len(codes) > 0 {
		batch := min((len(dst)-o)/onPairMaxToken, len(codes))
		if batch == 0 {
			return -1
		}
		for _, code := range codes[:batch] {
			c := int(code)
			if c >= ntok {
				return -1
			}
			p := offs[c]
			unsafex.Store64(dst, o, binary.LittleEndian.Uint64(src[p:]))
			unsafex.Store64(dst, o+8, binary.LittleEndian.Uint64(src[p+8:]))
			o += offs[c+1] - p
		}
		codes = codes[batch:]
	}
	return o
}

// decode decodes row i, the codes[starts[i]:ends[i]], to ulen[i] bytes of dst, one row after
// the other, with every check, and sets the ranges of the rows. dst holds exactly the sum of
// the lengths.
//
// A token of up to 8 bytes is copied as 8 bytes when both the dictionary and dst have room
// for them, the rest being overwritten by what follows.
func (d *onPairDict) decode(dst []byte, outStarts, outEnds []int, codes, starts, ends, ulen []int) error {
	ntok := len(d.offs) - 1
	o := 0
	for i := range ulen {
		rowEnd := o + ulen[i]
		outStarts[i], outEnds[i] = o, rowEnd
		for _, code := range codes[starts[i]:ends[i]] {
			if code >= ntok {
				return fmt.Errorf("row %d has code %d, there are %d tokens", i, code, ntok)
			}
			p, l := d.offs[code], d.offs[code+1]-d.offs[code]
			if o+l > rowEnd {
				return fmt.Errorf("row %d decodes to more than its %d bytes", i, ulen[i])
			}
			if l <= 8 && p+8 <= len(d.bytes) && o+8 <= len(dst) {
				binary.LittleEndian.PutUint64(dst[o:], binary.LittleEndian.Uint64(d.bytes[p:]))
			} else {
				copy(dst[o:], d.bytes[p:p+l])
			}
			o += l
		}
		if o != rowEnd {
			return fmt.Errorf("row %d decodes to %d bytes, declared %d", i, o-(rowEnd-ulen[i]), ulen[i])
		}
	}
	return nil
}

// onPairMeta reads the metadata: the ptypes of the lengths, the dictionary offsets, the codes
// and the code offsets, then the sizes of the dictionary and of the codes.
func onPairMeta(m *message) (ptypes [4]PType, dictSize, codesLen int, err error) {
	for i, f := range [...]struct {
		what string
		num  int
	}{{"uncompressed_lengths_ptype", 1}, {"dict_offsets_ptype", 5}, {"codes_ptype", 6}, {"codes_offsets_ptype", 7}} {
		if ptypes[i], err = intType(f.what, m.varint[f.num]); err != nil {
			return ptypes, 0, 0, err
		}
	}
	if dictSize, err = count("dict_size", m.varint[3]); err != nil {
		return ptypes, 0, 0, err
	}
	codesLen, err = count("codes_len", m.varint[4])
	return ptypes, dictSize, codesLen, err
}
