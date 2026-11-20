package array

import (
	"strconv"
	"strings"
)

// Kind is the shape of a DType, as far as the decoders care.
type Kind uint8

// The shapes the decoders know. Other is every logical type no decoder handles yet.
const (
	Null Kind = iota
	Bool
	Prim
	Struct
	Ext
	Utf8
	Binary
	Decimal
	List
	FixedSizeList
	Map
	Other
)

// PType is a primitive number type.
type PType uint8

// The primitive types.
const (
	U8 PType = iota
	U16
	U32
	U64
	I8
	I16
	I32
	I64
	F16
	F32
	F64
)

var ptypeNames = [...]string{"u8", "u16", "u32", "u64", "i8", "i16", "i32", "i64", "f16", "f32", "f64"}

// String returns "u8", "i64", "f16"...
func (p PType) String() string { return ptypeNames[p] }

// Width is the size of a value in bytes.
func (p PType) Width() int {
	switch p {
	case U8, I8:
		return 1
	case U16, I16, F16:
		return 2
	case U32, I32, F32:
		return 4
	default:
		return 8
	}
}

// Signed reports whether p is a signed integer.
func (p PType) Signed() bool { return p >= I8 && p <= I64 }

// Float reports whether p is a floating-point type.
func (p PType) Float() bool { return p >= F16 }

// Field is a named field of a struct type.
type Field struct {
	Name string
	Type DType
}

// DType is the type an array must decode to. It is not stored in the array: the parent
// gives it.
type DType struct {
	Storage *DType // Ext
	// Elem is the element type of a List or a FixedSizeList, and the entry type (a struct of
	// key and value) of a Map.
	Elem   *DType
	Label  string  // Other: the name of the logical type, for messages
	Fields []Field // Struct
	Size   int     // FixedSizeList: the number of elements of every value
	Kind   Kind
	PType  PType
	// Nullable is whether the array may hold nulls.
	Nullable bool
}

// String describes the type in a message.
func (t DType) String() string {
	var s string
	switch t.Kind {
	case Null:
		s = "null"
	case Bool:
		s = "bool"
	case Prim:
		s = t.PType.String()
	case Struct:
		parts := make([]string, len(t.Fields))
		for i, f := range t.Fields {
			parts[i] = f.Name + ": " + f.Type.String()
		}
		s = "struct{" + strings.Join(parts, ", ") + "}"
	case Ext:
		s = "ext(" + t.Storage.String() + ")"
	case Utf8:
		s = "utf8"
	case Binary:
		s = "binary"
	case Decimal:
		s = "decimal"
	case List:
		s = "list<" + t.Elem.String() + ">"
	case FixedSizeList:
		s = "fixed_size_list<" + t.Elem.String() + ", " + strconv.Itoa(t.Size) + ">"
	case Map:
		s = "map<" + t.Elem.String() + ">"
	default:
		s = t.Label
	}
	if t.Nullable && t.Kind != Null {
		s += "?"
	}
	return s
}

// NonNullable returns t, but not nullable.
func (t DType) NonNullable() DType {
	t.Nullable = false
	return t
}
