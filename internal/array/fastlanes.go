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
func unpackBlock[T word](out *[blockSize]T, packed []T, w int) {
	if w == 0 {
		*out = [blockSize]T{}
		return
	}
	bits := bitsOf[T]()
	lanes := blockSize / bits
	mask := T(1)<<w - 1 // w == bits shifts everything out: the mask is all ones
	for row := range bits {
		word, shift := row*w/bits, row*w%bits
		dst := out[flOrder[row/8]*16+row%8*128:][:lanes]
		lo := packed[word*lanes:][:lanes]
		if shift+w <= bits {
			for l := range dst {
				dst[l] = lo[l] >> shift & mask
			}
			continue
		}
		hi := packed[(word+1)*lanes:][:lanes]
		for l := range dst {
			dst[l] = (lo[l]>>shift | hi[l]<<(bits-shift)) & mask
		}
	}
}

// unpack decodes len(out) values of w bits from the blocks of packed, starting at value offset
// of the first block, into out. A whole block is unpacked where it goes.
func unpack[T word](out, packed []T, w, offset int) {
	var block [blockSize]T
	blockWords := blockSize / bitsOf[T]() * w
	for b := 0; len(out) > 0; b++ {
		in := packed[b*blockWords:][:blockWords]
		if offset == 0 && len(out) >= blockSize {
			unpackBlock((*[blockSize]T)(out), in, w)
			out = out[blockSize:]
			continue
		}
		unpackBlock(&block, in, w)
		n := copy(out, block[offset:])
		out = out[n:]
		offset = 0
	}
}

// unpackWidth is unpack for the integer type of the given width in bytes.
func unpackWidth(width int, out, packed []byte, w, offset, rows int) {
	out = out[:rows*width]
	switch width {
	case 1:
		unpack(out, packed, w, offset)
	case 2:
		unpack(words[uint16](out), words[uint16](packed), w, offset)
	case 4:
		unpack(words[uint32](out), words[uint32](packed), w, offset)
	default:
		unpack(words[uint64](out), words[uint64](packed), w, offset)
	}
}
