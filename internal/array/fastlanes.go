package array

import "encoding/binary"

// blockSize is the number of values of a FastLanes block.
const blockSize = 1024

// flOrder is the order of the groups of 8 rows of a block.
var flOrder = [8]int{0, 4, 2, 6, 1, 5, 3, 7}

// rowBytes is the size of a row of a FastLanes block, in bytes: its lanes, each a word.
const rowBytes = 128

// unpackBlock unpacks the 1024 values of w bits of a FastLanes block into out, as
// little-endian words of the given number of bits. packed is the 128*w bytes of the block.
//
// The block is laid out in lanes: a lane is one word wide, there are 1024/bits of them, and
// row r of lane l holds the value at position flOrder[r/8]*16 + (r%8)*128 + l. A lane packs
// its rows one after the other, from the low bit of its first word, a value that does not
// fit the end of a word going on in the same lane of the next word.
//
// A row is 128 bytes whatever the word, and it is unpacked 8 bytes at a time, which is 8, 4,
// 2 or 1 lanes: the same shift and the same mask go for every lane of the 8 bytes. What a
// shift brings in from the lane next to it is what the mask takes off.
func unpackBlock(out, packed []byte, w, bits int) {
	out = out[:blockSize*bits/8]
	if w == 0 {
		clear(out)
		return
	}
	lanes := ^uint64(0) / (1<<(bits-1)<<1 - 1) // the low bit of each lane
	for row := range bits {
		word, shift := row*w/bits, row*w%bits
		dst := out[(flOrder[row/8]*16+row%8*128)*bits/8:][:rowBytes]
		lo := packed[word*rowBytes:][:rowBytes]
		if shift+w <= bits {
			mask := lanes * (1<<(w-1)<<1 - 1)
			for j := 0; j < rowBytes; j += 8 {
				binary.LittleEndian.PutUint64(dst[j:], binary.LittleEndian.Uint64(lo[j:])>>(shift&63)&mask)
			}
			continue
		}
		// The value starts in this word, bits-shift bits of it, and ends in the next one.
		hi := packed[(word+1)*rowBytes:][:rowBytes]
		low, loMask, hiMask := bits-shift, lanes*(1<<(bits-shift)-1), lanes*(1<<(shift+w-bits)-1)
		for j := 0; j < rowBytes; j += 8 {
			l, h := binary.LittleEndian.Uint64(lo[j:]), binary.LittleEndian.Uint64(hi[j:])
			binary.LittleEndian.PutUint64(dst[j:], l>>(shift&63)&loMask|h&hiMask<<(low&63))
		}
	}
}

// unpack decodes the values of w bits of the blocks of packed, starting at value offset of
// the first block, into out, which they fill. A whole block is unpacked where it goes.
func unpack(out, packed []byte, w, offset, bits int) {
	var block [blockSize * 8]byte
	size, blockBytes := bits/8, rowBytes*w
	for b := 0; len(out) > 0; b++ {
		in := packed[b*blockBytes:][:blockBytes]
		if offset == 0 && len(out) >= blockSize*size {
			unpackBlock(out, in, w, bits)
			out = out[blockSize*size:]
			continue
		}
		unpackBlock(block[:], in, w, bits)
		n := copy(out, block[offset*size:blockSize*size])
		out = out[n:]
		offset = 0
	}
}

// unpackWidth is unpack for the integers of the given width in bytes.
func unpackWidth(width int, out, packed []byte, w, offset, rows int) {
	unpack(out[:rows*width], packed, w, offset, 8*width)
}
