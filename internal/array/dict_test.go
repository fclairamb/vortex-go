package array

import (
	"errors"
	"strings"
	"testing"
)

func TestTakeDictSkipped(t *testing.T) {
	typ := DType{Kind: Prim, PType: I64}
	skip := errors.New("skipped")
	ok := &Array{Type: typ, Len: 2, Data: make([]byte, 16)}
	codes := &Array{Type: DType{Kind: Prim, PType: U8}, Len: 3, Data: []byte{0, 1, 0}}
	for name, c := range map[string][2]*Array{
		"values": {{Type: typ, Len: 2, Skipped: skip}, codes},
		"codes":  {ok, {Type: codes.Type, Len: 3, Skipped: skip}},
	} {
		a, err := TakeDict(nil, c[0], c[1], U8, typ)
		if err != nil || a.Skipped != skip || a.Len != 3 {
			t.Errorf("%s: %+v, %v", name, a, err)
		}
	}
}

// intArr is an array of integers of type p: the low bytes of vals.
func intArr(p PType, vals ...int64) *Array {
	return &Array{Type: DType{Kind: Prim, PType: p}, Len: len(vals), Data: ints(p.Width(), vals...)}
}

// bitmapOf is a bitmap of the bits.
func bitmapOf(bits ...bool) *Bitmap {
	b := &Bitmap{Bytes: make([]byte, (len(bits)+7)/8)}
	for i, v := range bits {
		if v {
			b.Bytes[i>>3] |= 1 << (i & 7)
		}
	}
	return b
}

// sameRows fails when two arrays of numbers or of strings do not hold the same rows.
func sameRows(t *testing.T, what string, got, want *Array) {
	t.Helper()
	if got.Len != want.Len || got.Type.Kind != want.Type.Kind || got.Type.Nullable != want.Type.Nullable {
		t.Fatalf("%s: %d rows of %s, want %d of %s", what, got.Len, got.Type, want.Len, want.Type)
	}
	w := got.Type.PType.Width()
	for i := range got.Len {
		if got.Null(i) != want.Null(i) {
			t.Fatalf("%s: row %d: null %v, want %v", what, i, got.Null(i), want.Null(i))
		}
		if got.Type.Kind == Prim {
			if g, e := got.Data[i*w:(i+1)*w], want.Data[i*w:(i+1)*w]; string(g) != string(e) {
				t.Fatalf("%s: row %d: %x, want %x", what, i, g, e)
			}
		} else if str(got, i) != str(want, i) {
			t.Fatalf("%s: row %d: %q, want %q", what, i, str(got, i), str(want, i))
		}
	}
}

// TestTakeDictFast checks the rows made from the codes where they are against the ones the
// general path makes: every width of code and of value, strings, null codes and null values.
func TestTakeDictFast(t *testing.T) {
	codeVals := []int64{2, 0, 1, 2, 2, 0, 1, 0, 2, 1}
	nulls := bitmapOf(true, false, true, true, false, true, true, true, false, true)
	values := map[string]*Array{"strings": utf8Arr("a", "", "ccc")}
	for _, p := range []PType{U8, I16, U32, I64} {
		values[p.String()] = intArr(p, 7, -3, 1<<40+5)
	}
	for name, vals := range values {
		for _, valueNulls := range []*Bitmap{nil, bitmapOf(true, false, true)} {
			vals.Validity = valueNulls
			for _, cp := range []PType{U8, I8, U16, I16, U32, I32, U64, I64} {
				for _, codeNulls := range []*Bitmap{nil, nulls} {
					codes := intArr(cp, codeVals...)
					codes.Validity = codeNulls
					typ := vals.Type
					typ.Nullable = true
					idx, err := toInts(nil, codes, "codes", cp, codeNulls)
					if err != nil {
						t.Fatal(err)
					}
					for i := range idx {
						if codes.Null(i) {
							idx[i] = -1
						}
					}
					want := withType(take(nil, vals, idx), typ)
					var slot Array
					got, err := TakeDictInto(&slot, new(Pool).NewArena(), vals, codes, cp, typ)
					if err != nil || got != &slot {
						t.Fatalf("%s, codes %s: %v, in the array given: %v", name, cp, err, got == &slot)
					}
					sameRows(t, name+", codes "+cp.String(), got, want)
				}
			}
		}
	}
}

// TestTakeDictFastWrongCode checks that a code past the values, or negative, is an error, and
// that the code of a null row is not read.
func TestTakeDictFastWrongCode(t *testing.T) {
	vals := utf8Arr("a", "b")
	for name, c := range map[string]struct {
		codes *Array
		want  string
	}{
		"past":     {intArr(U8, 0, 2, 1), "code 2 at row 1 is past the 2 values"},
		"far":      {intArr(U64, 0, -1), "codes: value"},
		"negative": {intArr(I16, 0, -1), "negative value -1 at row 1"},
	} {
		if _, err := TakeDict(nil, vals, c.codes, c.codes.Type.PType, utf8t); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	codes := intArr(U8, 0, 200, 1)
	codes.Validity = bitmapOf(true, false, true)
	a, err := TakeDict(nil, vals, codes, U8, utf8n)
	if err != nil || !a.Null(1) || str(a, 0) != "a" || str(a, 2) != "b" || str(a, 1) != "" {
		t.Fatalf("%+v, %v", a, err)
	}
	// More values than a code can name: every code is one of them.
	many := intArr(I32, make([]int64, 300)...)
	if a, err := TakeDict(nil, many, intArr(U8, 255, 0), U8, many.Type); err != nil || a.Len != 2 {
		t.Fatalf("%+v, %v", a, err)
	}
	// No value at all: only null rows.
	none := &Array{Type: utf8t}
	if _, err := TakeDict(nil, none, intArr(U8, 0), U8, utf8t); err == nil {
		t.Fatal("a code of a dictionary without value is accepted")
	}
	codes = intArr(U8, 0, 0)
	codes.Validity = bitmapOf(false, false)
	if a, err := TakeDict(nil, none, codes, U8, utf8n); err != nil || !a.Null(0) || !a.Null(1) {
		t.Fatalf("%+v, %v", a, err)
	}
}

// TestToIntsNullGarbage checks that the value of a null row is not read: it may be anything.
func TestToIntsNullGarbage(t *testing.T) {
	a := intArr(I8, -1, 5)
	got, err := toInts(nil, a, "codes", I8, bitmapOf(false, true))
	if err != nil || got[0] != 0 || got[1] != 5 {
		t.Fatalf("%v, %v", got, err)
	}
}
