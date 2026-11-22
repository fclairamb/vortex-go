package encode

// The blocks of the arena are full of a pattern in the tests of the package, and marked as
// written: alloc has to clear them, and a caller of allocDirty that reads a byte it did not
// write makes other bytes than the reference, which the vectors catch.
func init() {
	newBlock = func(n int) block {
		b := block{buf: make([]byte, n), dirty: n}
		for i := range b.buf {
			b.buf[i] = 0xa5
		}
		return b
	}
}
