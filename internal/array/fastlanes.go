package array

// blockSize is the number of values of a FastLanes block.
const blockSize = 1024

// flOrder is the order of the groups of 8 rows of a block.
var flOrder = [8]int{0, 4, 2, 6, 1, 5, 3, 7}

// unpackBlock unpacks the 1024 values of w bits of a FastLanes block into out. packed is the
// 128*w bytes of the block, little-endian words of T.
//
// The block is laid out in lanes: a lane is one word wide, there are 1024/bits of them, and
// row r of lane l holds the value at position flOrder[r/8]*16 + (r%8)*128 + l. A lane packs
// its rows one after the other, from the low bit of its first word, a value that does not
// fit the end of a word going on in the same lane of the next word.
func unpackBlock[T word](out *[blockSize]T, packed []byte, w int) {
	if w == 0 {
		*out = [blockSize]T{}
		return
	}
	bits := bitsOf[T]()
	lanes := blockSize / bits
	var words [blockSize]T
	loadWords(words[:lanes*w], packed, bits)
	mask := T(1)<<w - 1 // w == bits shifts everything out: the mask is all ones
	for row := range bits {
		word, shift := row*w/bits, row*w%bits
		dst := out[flOrder[row/8]*16+row%8*128:][:lanes]
		lo := words[word*lanes:][:lanes]
		if shift+w <= bits {
			for l := range dst {
				dst[l] = lo[l] >> shift & mask
			}
			continue
		}
		hi := words[(word+1)*lanes:][:lanes]
		for l := range dst {
			dst[l] = (lo[l]>>shift | hi[l]<<(bits-shift)) & mask
		}
	}
}

// unpack decodes rows values of w bits from the blocks of packed, starting at value offset of
// the first block, into out, little-endian words of T.
func unpack[T word](out, packed []byte, w, offset, rows int) {
	bits := bitsOf[T]()
	var block [blockSize]T
	blockBytes := 128 * w
	for b := 0; rows > 0; b++ {
		unpackBlock(&block, packed[b*blockBytes:][:blockBytes], w)
		n := min(blockSize-offset, rows)
		storeWords(out, block[offset:offset+n], bits)
		out = out[n*bits/8:]
		rows -= n
		offset = 0
	}
}

// unpackWidth is unpack for the integer type of the given width in bytes.
func unpackWidth(width int, out, packed []byte, w, offset, rows int) {
	switch width {
	case 1:
		unpack[uint8](out, packed, w, offset, rows)
	case 2:
		unpack[uint16](out, packed, w, offset, rows)
	case 4:
		unpack[uint32](out, packed, w, offset, rows)
	default:
		unpack[uint64](out, packed, w, offset, rows)
	}
}
