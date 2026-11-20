package array

import (
	"math"
	"testing"
)

func TestConstantNumbers(t *testing.T) {
	cases := []struct {
		name string
		msg  []byte
		dt   DType
		want []byte
	}{
		{"i8 negative", sInt(-3), DType{Kind: Prim, PType: I8}, []byte{0xfd, 0xfd}},
		{"i32", sInt(-2), i32, le32(-2, -2)},
		{"i64 from a uint", sUint(5), DType{Kind: Prim, PType: I64}, le64(5, 5)},
		{"u8", sUint(255), DType{Kind: Prim, PType: U8}, []byte{255, 255}},
		{"u16 from an int", sInt(300), DType{Kind: Prim, PType: U16}, []byte{44, 1, 44, 1}},
		{"u64 max", sUint(math.MaxUint64), u64, le64(math.MaxUint64, math.MaxUint64)},
		{"f16", sF16(0x3c00), DType{Kind: Prim, PType: F16}, []byte{0, 0x3c, 0, 0x3c}},
		{"f32", sF32(1.5), DType{Kind: Prim, PType: F32}, le32(0x3fc00000, 0x3fc00000)},
		{"f64", sF64(2), DType{Kind: Prim, PType: F64}, le64(0x4000000000000000, 0x4000000000000000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := mustDecode(t, constSeg(c.msg), c.dt, 2)
			if string(a.Data) != string(c.want) || a.Validity != nil {
				t.Fatalf("data = %v, want %v", a.Data, c.want)
			}
		})
	}
}

func TestConstantBoolAndNull(t *testing.T) {
	if a := mustDecode(t, constSeg(sBool(true)), bl, 10); !a.Bits.Get(9) || a.Bits.Get(0) != true {
		t.Fatalf("true: %+v", a)
	}
	if a := mustDecode(t, constSeg(sBool(false)), bl, 10); a.Bits.Get(3) {
		t.Fatalf("false: %+v", a)
	}
	if a := mustDecode(t, constSeg(sNull()), nul, 4); a.Len != 4 || !a.Null(3) {
		t.Fatalf("null: %+v", a)
	}
	a := mustDecode(t, constSeg(sNull()), DType{Kind: Bool, Nullable: true}, 4)
	if !a.Null(0) || !a.Null(3) {
		t.Fatalf("null bool: %+v", a)
	}
	a = mustDecode(t, constSeg(sNull()), i32n, 3)
	if !a.Null(2) || len(a.Data) != 12 {
		t.Fatalf("null i32: %+v", a)
	}
	// An extension type holds its storage.
	ext := DType{Kind: Ext, Storage: &i32n}
	if a = mustDecode(t, constSeg(sInt(7)), ext, 2); i32s(t, a)[1] != 7 {
		t.Fatalf("ext: %+v", a)
	}
}

func TestConstantErrors(t *testing.T) {
	i8 := DType{Kind: Prim, PType: I8}
	u8 := DType{Kind: Prim, PType: U8}
	runErrCases(t, []errCase{
		{"no buffer", segment(node(eConst, nil, nil)), i32, 1, "expected 1 buffer"},
		{"missing buffer", segment(node(eConst, nil, []uint16{4})), i32, 1, "which has 0"},
		{"bad scalar", constSeg([]byte{0x18}), i32, 1, "truncated"},
		{"no kind", constSeg(nil), i32, 1, "no value kind"},
		{"i8 too big", constSeg(sInt(128)), i8, 1, "does not fit in i8"},
		{"i8 too small", constSeg(sInt(-129)), i8, 1, "does not fit in i8"},
		{"i64 from a huge uint", constSeg(sUint(math.MaxUint64)), DType{Kind: Prim, PType: I64}, 1, "does not fit in i64"},
		{"u8 too big", constSeg(sUint(256)), u8, 1, "does not fit in u8"},
		{"u8 negative", constSeg(sInt(-1)), u8, 1, "does not fit in u8"},
		{"int for a float", constSeg(sInt(1)), DType{Kind: Prim, PType: F32}, 1, "cannot be a f32"},
		{"f64 for an f32", constSeg(sF64(1)), DType{Kind: Prim, PType: F32}, 1, "cannot be a f32"},
		{"bool for an int", constSeg(sBool(true)), i32, 1, "cannot be a i32"},
		{"int for a bool", constSeg(sInt(1)), bl, 1, "cannot be a bool"},
		{"null for a non-nullable", constSeg(sNull()), i32, 1, "cannot be a i32"},
		{"null for a struct", constSeg(sNull()), structOf(true), 1, "unsupported operation"},
		{"value for a string", constSeg(sInt(1)), DType{Kind: Other, Label: "utf8"}, 1, "constant of type utf8"},
	})
}
