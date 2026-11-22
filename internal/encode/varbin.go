package encode

// validBytesOf is the total length of the valid values of v.
func validBytesOf(v [][]byte, nulls []bool) int {
	total := 0
	for i, x := range v {
		if !isNull(nulls, i) {
			total += len(x)
		}
	}
	return total
}

// VarBinBytes is the vortex.varbin array of the byte slices of v: the values end to end in
// one buffer, the rows + 1 offsets (the narrowest unsigned type that holds the last) and the
// validity. A null row has no byte.
func VarBinBytes(e *Encoder, v [][]byte, nulls []bool) *Node {
	total := validBytesOf(v, nulls)
	data := e.allocDirty(total) // the valid values, end to end
	ptype, w := narrow(uint64(total))
	offs := e.allocDirty((len(v) + 1) * w)
	putWidth(offs, w, 0)
	at := 0
	for i, x := range v {
		if !isNull(nulls, i) {
			at += copy(data[at:], x)
		}
		putWidth(offs[(i+1)*w:], w, uint64(at))
	}
	n := e.node(IDVarBin)
	e.tmp = putUint(e.tmp[:0], 1, uint64(ptype))
	n.Meta = e.keep(e.tmp)
	n.add(data, 1)
	n.kid(e.leaf(offs, w, nil, len(v)+1))
	n.kid(e.validity(nulls, len(v), len(v)))
	return n
}

// EstimateVarBinBytes is the size of VarBinBytes, its offsets not compressed.
func EstimateVarBinBytes(sample [][]byte, nulls []bool, rows int) int {
	total := scale(validBytesOf(sample, nulls), len(sample), rows)
	_, w := narrow(uint64(total))
	return total + (rows+1)*w + validBytes(nulls, rows)
}
