package array

import (
	"encoding/binary"
	"fmt"
)

// decodeOnPair decodes vortex.onpair. The metadata is {1: uncompressed_lengths_ptype,
// 3: dict_size, 4: codes_len, 5: dict_offsets_ptype, 6: codes_ptype, 7: codes_offsets_ptype}.
// Buffer 0 is the bytes of the dictionary; the children are the dictionary offsets
// (dict_size + 1), the codes (codes_len), the code offsets (rows + 1), the uncompressed
// lengths and an optional validity.
func decodeOnPair(n *Node, t DType, rows int) (*Array, error) {
	if !isBytes(t) {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	m, err := readMessage(n.Metadata())
	if err != nil {
		return nil, err
	}
	ptypes, dictSize, codesLen, err := onPairMeta(&m)
	if err != nil {
		return nil, err
	}
	if n.NumBuffers() != 1 {
		return nil, fmt.Errorf("expected 1 buffer, got %d", n.NumBuffers())
	}
	if c := n.NumChildren(); c != 4 && c != 5 {
		return nil, fmt.Errorf("expected 4 or 5 children, got %d", c)
	}
	dict, err := n.Buffer(0)
	if err != nil {
		return nil, err
	}
	if dictSize > len(dict) {
		// A token is at least 1 byte; this also keeps the offsets from being sized by the file.
		return nil, fmt.Errorf("dict_size is %d, the dictionary has %d bytes", dictSize, len(dict))
	}
	dictOffs, codes, offs, ulen, err := onPairChildren(n, ptypes, dictSize, codesLen, len(dict), rows)
	if err != nil {
		return nil, err
	}
	maxTok := 0
	for i := range dictSize {
		maxTok = max(maxTok, dictOffs[i+1]-dictOffs[i])
	}
	total, err := outputSize(ulen, offs[:rows], offs[1:], maxTok)
	if err != nil {
		return nil, err
	}
	a := &Array{Type: t, Len: rows, Data: make([]byte, total), Starts: make([]int, rows), Ends: make([]int, rows)}
	if n.NumChildren() == 5 {
		if a.Validity, err = n.Validity(4, rows); err != nil {
			return nil, err
		}
	}
	d := &onPairDict{bytes: dict, offs: dictOffs}
	if err := d.decode(a.Data, a.Starts, a.Ends, codes, offs, ulen); err != nil {
		return nil, err
	}
	return a, nil
}

// onPairDict is the dictionary of an OnPair array: token i is bytes[offs[i]:offs[i+1]].
type onPairDict struct {
	bytes []byte
	offs  []int
}

// decode decodes row i, the codes[offs[i]:offs[i+1]], to ulen[i] bytes of dst, one row after
// the other, and sets the ranges of the rows. dst holds exactly the sum of the lengths.
//
// A token of up to 8 bytes is copied as 8 bytes when both the dictionary and dst have room
// for them, the rest being overwritten by what follows.
func (d *onPairDict) decode(dst []byte, outStarts, outEnds []int, codes, offs, ulen []int) error {
	ntok := len(d.offs) - 1
	o := 0
	for i := range ulen {
		rowEnd := o + ulen[i]
		outStarts[i], outEnds[i] = o, rowEnd
		for _, code := range codes[offs[i]:offs[i+1]] {
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

// onPairChildren reads the integer children, and checks their offsets.
func onPairChildren(
	n *Node, ptypes [4]PType, dictSize, codesLen, dictBytes, rows int,
) (dictOffs, codes, offs, ulen []int, err error) {
	if dictOffs, err = n.ints(0, "dictionary offsets", ptypes[1], dictSize+1); err != nil {
		return
	}
	if err = checkOffsets(dictOffs, dictBytes); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("dictionary offsets: %w", err)
	}
	if codes, err = n.ints(1, "codes", ptypes[2], codesLen); err != nil {
		return
	}
	if offs, err = n.ints(2, "codes offsets", ptypes[3], rows+1); err != nil {
		return
	}
	if err = checkOffsets(offs, codesLen); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("codes offsets: %w", err)
	}
	ulen, err = n.ints(3, "uncompressed lengths", ptypes[0], rows)
	return
}
