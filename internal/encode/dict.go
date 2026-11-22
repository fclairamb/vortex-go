package encode

import (
	"bytes"
	"encoding/binary"
	"hash/maphash"
	"math/bits"

	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// maxInline is the longest string a view of a varbinview holds itself.
const maxInline = 12

// keys is the values to put in a dictionary: numbers of w bytes in raw, or byte slices, or
// strings.
type keys struct {
	raw  []byte
	bins [][]byte
	strs []string
	w    int
	kind int
}

const (
	keyNumbers = iota
	keyBytes
	keyStrings
)

func (k *keys) hash(seed maphash.Seed, i int) uint64 {
	switch k.kind {
	case keyBytes:
		return maphash.Bytes(seed, k.bins[i])
	case keyStrings:
		return maphash.String(seed, k.strs[i])
	}
	return maphash.Bytes(seed, k.raw[i*k.w:(i+1)*k.w])
}

func (k *keys) eq(i, j int) bool {
	switch k.kind {
	case keyBytes:
		return bytes.Equal(k.bins[i], k.bins[j])
	case keyStrings:
		return k.strs[i] == k.strs[j]
	}
	return bytes.Equal(k.raw[i*k.w:(i+1)*k.w], k.raw[j*k.w:(j+1)*k.w])
}

// size is the bytes of value i of a set of strings or byte slices.
func (k *keys) size(i int) int {
	if k.kind == keyBytes {
		return len(k.bins[i])
	}
	return len(k.strs[i])
}

// copyTo copies value i to dst, which has room for it.
func (k *keys) copyTo(dst []byte, i int) {
	switch k.kind {
	case keyBytes:
		copy(dst, k.bins[i])
	case keyStrings:
		copy(dst, k.strs[i])
	default:
		copy(dst, k.raw[i*k.w:(i+1)*k.w])
	}
}

// dictTables is the memory of classify.
type dictTables struct {
	codes []uint32
	first []int32
	count []int32
	tab   []int32
}

// classify gives each row the code of its value, in the order of their first appearance, a
// null being a value of its own. The codes are in e.dt.codes, the row of the first appearance
// of each code in e.dt.first, and how many rows use each in e.dt.count, until e.done. It
// returns the number of codes and the code of null, -1 when no row is null.
func (e *Encoder) classify(k *keys, rows int, nulls []bool) (distinct, nullCode int) {
	if e.dt == nil {
		e.dt = dictStates.Get().(*dictTables)
	}
	e.dt.codes = grow(e.dt.codes, rows)
	e.dt.first = grow(e.dt.first, rows+1)
	e.dt.count = grow(e.dt.count, rows+1)
	tabSize := 1 << bits.Len(uint(2*rows+1))
	e.dt.tab = grow(e.dt.tab, tabSize)
	clear(e.dt.tab)
	mask := uint64(tabSize - 1)
	nullCode = -1
	for i := range rows {
		var code int
		if isNull(nulls, i) {
			if nullCode < 0 {
				nullCode = distinct
				e.dt.first[distinct], e.dt.count[distinct] = int32(i), 0
				distinct++
			}
			code = nullCode
		} else {
			s := k.hash(e.seed, i) & mask
			for e.dt.tab[s] != 0 && !k.eq(i, int(e.dt.first[e.dt.tab[s]-1])) {
				s = (s + 1) & mask
			}
			if e.dt.tab[s] == 0 {
				e.dt.first[distinct], e.dt.count[distinct] = int32(i), 0
				distinct++
				e.dt.tab[s] = int32(distinct)
			}
			code = int(e.dt.tab[s]) - 1
		}
		e.dt.codes[i] = uint32(code)
		e.dt.count[code]++
	}
	return distinct, nullCode
}

// grow returns s with n elements, kept when it is large enough.
func grow[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, max(2*cap(s), n))[:n]
	}
	return s[:n]
}

// Dict is the dictionary encoding of the numbers of v: the codes, in the smallest unsigned
// type that holds them, and the distinct values in the order of their first appearance. A null
// has a value of its own, null in the validity of the values, so that no code is null.
// Numbers are compared by their bits.
func Dict[T Number](e *Encoder, v []T, nulls []bool) *Node {
	k := keys{raw: unsafex.Bytes(v), w: unsafex.Size[T](), kind: keyNumbers}
	distinct, nullCode := e.classify(&k, len(v), nulls)
	defer e.done()
	vals := e.alloc(distinct * k.w)
	for c := range distinct {
		if c != nullCode {
			k.copyTo(vals[c*k.w:], int(e.dt.first[c]))
		}
	}
	return e.dictNode(len(v), distinct, e.leaf(vals, k.w, e.nullAt(distinct, nullCode), distinct))
}

// DictBytes is Dict for byte slices: the values are a varbinview.
func DictBytes(e *Encoder, v [][]byte, nulls []bool) *Node {
	return e.dictVarBin(&keys{bins: v, kind: keyBytes}, len(v), nulls)
}

// DictStrings is Dict for strings: the values are a varbinview.
func DictStrings(e *Encoder, v []string, nulls []bool) *Node {
	return e.dictVarBin(&keys{strs: v, kind: keyStrings}, len(v), nulls)
}

// nullAt is the nulls of the distinct values: only the one of code nullCode, if any.
func (e *Encoder) nullAt(distinct, nullCode int) []bool {
	if nullCode < 0 {
		return nil
	}
	f := e.flags(distinct)
	f[nullCode] = true
	return f
}

func (e *Encoder) dictVarBin(k *keys, rows int, nulls []bool) *Node {
	distinct, nullCode := e.classify(k, rows, nulls)
	defer e.done()
	long := 0
	for c := range distinct {
		if c != nullCode && k.size(int(e.dt.first[c])) > maxInline {
			long += k.size(int(e.dt.first[c]))
		}
	}
	data, views := e.allocDirty(long), e.alloc(16*distinct) // a short string leaves zeroes in its view
	at := 0
	for c := range distinct {
		if c == nullCode {
			continue
		}
		i := int(e.dt.first[c])
		view, size := views[16*c:16*c+16], k.size(i)
		binary.LittleEndian.PutUint32(view, uint32(size))
		if size <= maxInline {
			k.copyTo(view[4:], i)
			continue
		}
		k.copyTo(data[at:], i)
		copy(view[4:8], data[at:at+4])
		binary.LittleEndian.PutUint32(view[12:], uint32(at))
		at += size
	}
	vals := e.node(IDVarBinView)
	vals.add(data, 1) // an empty buffer when no string is long
	vals.add(views, 16)
	vals.kid(e.validity(e.nullAt(distinct, nullCode), distinct, distinct))
	return e.dictNode(rows, distinct, vals)
}

// dictNode is the node of the dictionary of the codes in e.dt.codes and the values node.
func (e *Encoder) dictNode(rows, distinct int, vals *Node) *Node {
	ptype, w := narrow(uint64(max(distinct-1, 0)))
	codes := e.allocDirty(rows * w)
	for i := range rows {
		putWidth(codes[i*w:], w, uint64(e.dt.codes[i]))
	}
	n := e.node(IDDict)
	e.tmp = putUint(e.tmp[:0], 1, uint64(distinct))
	e.tmp = putUint(e.tmp, 2, uint64(ptype))
	e.tmp = protowire.AppendUint(e.tmp, 3, 0)
	e.tmp = protowire.AppendUint(e.tmp, 4, 1)
	n.Meta = e.keep(e.tmp)
	n.kid(e.leaf(codes, w, nil, rows))
	n.kid(vals)
	return n
}
