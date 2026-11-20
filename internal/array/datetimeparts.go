package array

import (
	"encoding/binary"
	"fmt"
	"math"
)

// secondsPerDay is the seconds of a day, which timestamps count without leap seconds.
const secondsPerDay = 86400

// decodeDateTimeParts decodes vortex.datetimeparts: metadata {days_ptype, seconds_ptype,
// subseconds_ptype}, children the days (which carry the nulls), the seconds and the
// subseconds. The storage of a timestamp in its unit is days * 86400 * unit + seconds * unit +
// subseconds, where unit is the number of ticks of a second, wrapping like the reference. The
// time zone is in the type, not in the parts.
func decodeDateTimeParts(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind == Ext {
		nullable := t.Nullable
		t = *t.Storage
		t.Nullable = t.Nullable || nullable
	}
	if t.Kind != Prim || t.PType != I64 || t.Ticks == 0 {
		return nil, fmt.Errorf("cannot be a %s: it is not a timestamp", t)
	}
	meta, err := varints(n.Metadata(), 3)
	if err != nil {
		return nil, err
	}
	if n.NumChildren() != 3 {
		return nil, fmt.Errorf("expected 3 children, got %d", n.NumChildren())
	}
	var parts [3][]int64
	var validity *Bitmap
	for i, what := range [...]string{"days", "seconds", "subseconds"} {
		p, err := intType(what+"_ptype", meta[i])
		if err != nil {
			return nil, err
		}
		// The days carry the nullability of the type, the others never are null.
		a, err := n.primChild(i, p, DType{Nullable: i == 0 && t.Nullable}, rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		if parts[i], err = signedValues(a, p, i > 0, what); err != nil {
			return nil, err
		}
		if i == 0 {
			validity = a.Validity
		}
	}
	ticks := int64(t.Ticks)
	out := &Array{Type: t, Len: rows, Validity: validity, Data: make([]byte, 8*rows)}
	for i := range rows {
		v := parts[0][i]*secondsPerDay*ticks + parts[1][i]*ticks + parts[2][i]
		binary.LittleEndian.PutUint64(out.Data[8*i:], uint64(v))
	}
	return out, nil
}

// signedValues reads the integers of a as int64, the type p telling how to extend them. The
// reference casts seconds and subseconds to i32 first, which fails on a value that does not fit:
// i32 asks for that check.
func signedValues(a *Array, p PType, i32 bool, what string) ([]int64, error) {
	w := p.Width()
	out := make([]int64, a.Len)
	for i := range out {
		var u uint64
		switch w {
		case 1:
			u = uint64(a.Data[i])
		case 2:
			u = uint64(binary.LittleEndian.Uint16(a.Data[2*i:]))
		case 4:
			u = uint64(binary.LittleEndian.Uint32(a.Data[4*i:]))
		default:
			u = binary.LittleEndian.Uint64(a.Data[8*i:])
		}
		if p.Signed() {
			u = uint64(int64(u<<(64-8*w)) >> (64 - 8*w))
		}
		out[i] = int64(u)
		if i32 && (out[i] < math.MinInt32 || out[i] > math.MaxInt32) {
			return nil, fmt.Errorf("%s %d at row %d does not fit in an i32", what, out[i], i)
		}
	}
	return out, nil
}
