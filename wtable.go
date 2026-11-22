package vortex

import (
	"bytes"
	"hash/maphash"
)

// codeTable finds the code of a value of the run of a dictionary: open addressing with linear
// probing, in a power of two of slots kept at most half full. A slot holds a code plus one, 0
// when it is empty. A Go map keyed by strings hashes and compares through more steps; the
// table compares a hash first, and the values of the run where they are.
type codeTable struct {
	slots []uint32
	ids   []uint64 // by code: the bits of a number, the hash of a string; 0 for null
	seed  maphash.Seed
	shift uint8 // 64 minus the bits of the index of a slot
	strs  bool  // the values are strings: an id is their hash
}

// fib is 2^64 over the golden ratio: a number times it has its high bits spread.
const fib = 0x9E3779B97F4A7C15

// minSlots is the slots of a new table.
const minSlots = 64

func newCodeTable(strs bool) codeTable {
	t := codeTable{strs: strs, seed: maphash.MakeSeed()}
	t.size(minSlots)
	return t
}

// size makes the table n empty slots, n a power of two.
func (t *codeTable) size(n int) {
	t.slots = make([]uint32, n)
	t.shift = 64
	for ; n > 1; n >>= 1 {
		t.shift--
	}
}

// reset empties the table for a new run, and keeps its memory.
func (t *codeTable) reset() {
	clear(t.slots)
	t.ids = t.ids[:0]
}

// hash is where the probe for the value of this id starts.
func (t *codeTable) hash(id uint64) uint64 {
	if t.strs {
		return id
	}
	return id * fib
}

// number returns the code of the number of these bits, or -1 and the slot where it goes.
func (t *codeTable) number(id uint64) (code, slot int) {
	mask := len(t.slots) - 1
	for i := int((id * fib) >> t.shift); ; i = (i + 1) & mask {
		s := t.slots[i]
		if s == 0 {
			return -1, i
		}
		if t.ids[s-1] == id {
			return int(s - 1), i
		}
	}
}

// str returns the code of the string v, or -1 and the slot where it goes, and its hash.
// vals holds the strings of the run, by code.
func (t *codeTable) str(vals [][]byte, v []byte) (code, slot int, id uint64) {
	id = maphash.Bytes(t.seed, v)
	mask := len(t.slots) - 1
	for i := int(id >> t.shift); ; i = (i + 1) & mask {
		s := t.slots[i]
		if s == 0 {
			return -1, i, id
		}
		if t.ids[s-1] == id && bytes.Equal(vals[s-1], v) {
			return int(s - 1), i, id
		}
	}
}

// put gives the next code to the value of this id, which goes to the slot number or str
// returned, and grows the table when it is half full.
func (t *codeTable) put(slot int, id uint64) {
	t.slots[slot] = uint32(len(t.ids) + 1)
	t.ids = append(t.ids, id)
	if 2*len(t.ids) > len(t.slots) {
		t.grow()
	}
}

// skip gives the next code to a value that is not in the table: null.
func (t *codeTable) skip() { t.ids = append(t.ids, 0) }

// grow doubles the slots and puts the codes back in them.
func (t *codeTable) grow() {
	old := t.slots
	t.size(2 * len(old))
	mask := len(t.slots) - 1
	for _, s := range old {
		if s == 0 {
			continue
		}
		i := int(t.hash(t.ids[s-1]) >> t.shift)
		for t.slots[i] != 0 {
			i = (i + 1) & mask
		}
		t.slots[i] = s
	}
}
