package array

import (
	"errors"
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
		a, err := TakeDict(c[0], c[1], U8, typ)
		if err != nil || a.Skipped != skip || a.Len != 3 {
			t.Errorf("%s: %+v, %v", name, a, err)
		}
	}
}
