package vortex

import (
	"slices"
	"strconv"
	"strings"
)

// Kind is the logical type of a column.
type Kind uint8

// The logical types of a Vortex file. Union exists upstream but is in no edition.
const (
	Null Kind = iota
	Bool
	Int8
	Int16
	Int32
	Int64
	Uint8
	Uint16
	Uint32
	Uint64
	Float16
	Float32
	Float64
	Decimal
	Utf8
	Binary
	List
	FixedSizeList
	Map
	Struct
	Date
	Time
	Timestamp
	UUID
	Variant
)

var kindNames = [...]string{
	Null: "null", Bool: "bool", Int8: "i8", Int16: "i16", Int32: "i32", Int64: "i64",
	Uint8: "u8", Uint16: "u16", Uint32: "u32", Uint64: "u64", Float16: "f16", Float32: "f32",
	Float64: "f64", Decimal: "decimal", Utf8: "utf8", Binary: "binary", List: "list",
	FixedSizeList: "fixed_size_list", Map: "map", Struct: "struct", Date: "date", Time: "time",
	Timestamp: "timestamp", UUID: "uuid", Variant: "variant",
}

// String returns the name of the kind, like "i32" or "fixed_size_list".
func (k Kind) String() string {
	if int(k) >= len(kindNames) {
		return "kind(" + strconv.Itoa(int(k)) + ")"
	}
	return kindNames[k]
}

// Unit is the time unit of a date, a time or a timestamp.
type Unit uint8

// The time units, with the values of the file format.
const (
	Nanosecond Unit = iota
	Microsecond
	Millisecond
	Second
	Day
)

var unitNames = [...]string{"ns", "us", "ms", "s", "days"}

// String returns "ns", "us", "ms", "s" or "days".
func (u Unit) String() string {
	if int(u) >= len(unitNames) {
		return "unit(" + strconv.Itoa(int(u)) + ")"
	}
	return unitNames[u]
}

// Field is a named field of a struct.
type Field struct {
	Name string
	Type Type
}

// Type is a logical type. It is a value: compare two of them with Equal, not with ==.
// An accessor that does not apply to the kind of the type returns the zero value.
type Type struct {
	elem      *Type // list, fixed-size list; the key of a map
	value     *Type // the value of a map
	zone      string
	fields    []Field
	size      uint32
	precision uint8
	scale     int8
	kind      Kind
	unit      Unit
	nullable  bool
}

// Kind returns the logical type.
func (t Type) Kind() Kind { return t.kind }

// Nullable reports whether the values may be null. It is always true for Null.
func (t Type) Nullable() bool { return t.nullable }

// Precision is the number of digits of a decimal.
func (t Type) Precision() int { return int(t.precision) }

// Scale is the number of digits of a decimal after the point. It can be negative.
func (t Type) Scale() int { return int(t.scale) }

// Elem is the element type of a list or a fixed-size list.
func (t Type) Elem() Type {
	if t.kind != List && t.kind != FixedSizeList || t.elem == nil {
		return Type{}
	}
	return *t.elem
}

// Size is the number of elements of every value of a fixed-size list.
func (t Type) Size() int { return int(t.size) }

// Key is the key type of a map.
func (t Type) Key() Type {
	if t.kind != Map || t.elem == nil {
		return Type{}
	}
	return *t.elem
}

// Value is the value type of a map.
func (t Type) Value() Type {
	if t.kind != Map || t.value == nil {
		return Type{}
	}
	return *t.value
}

// Fields returns a copy of the fields of a struct.
func (t Type) Fields() []Field { return slices.Clone(t.fields) }

// Unit is the time unit of a date, a time or a timestamp.
func (t Type) Unit() Unit { return t.unit }

// Zone is the time zone of a timestamp, empty when it has none.
func (t Type) Zone() string { return t.zone }

// Equal reports whether the two types are the same, nullability included.
func (t Type) Equal(o Type) bool {
	if t.kind != o.kind || t.nullable != o.nullable || t.precision != o.precision ||
		t.scale != o.scale || t.size != o.size || t.unit != o.unit || t.zone != o.zone ||
		!ptrEqual(t.elem, o.elem) || !ptrEqual(t.value, o.value) {
		return false
	}
	return slices.EqualFunc(t.fields, o.fields, func(a, b Field) bool {
		return a.Name == b.Name && a.Type.Equal(b.Type)
	})
}

func ptrEqual(a, b *Type) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// String returns the type in a short form: "i32", "utf8?" (nullable), "decimal(10,2)",
// "list<i32>", "map<utf8, i64>", "timestamp[ms, tz=UTC]", "struct{a: i32, b: utf8?}".
func (t Type) String() string {
	var b strings.Builder
	t.write(&b)
	return b.String()
}

func (t Type) write(b *strings.Builder) {
	b.WriteString(t.kind.String())
	switch t.kind {
	case Decimal:
		b.WriteString("(" + strconv.Itoa(int(t.precision)) + "," + strconv.Itoa(int(t.scale)) + ")")
	case List:
		b.WriteString("<" + t.elem.String() + ">")
	case FixedSizeList:
		b.WriteString("<" + t.elem.String() + ">[" + strconv.Itoa(int(t.size)) + "]")
	case Map:
		b.WriteString("<" + t.elem.String() + ", " + t.value.String() + ">")
	case Struct:
		b.WriteString("{")
		for i, f := range t.fields {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(f.Name + ": ")
			f.Type.write(b)
		}
		b.WriteString("}")
	case Date, Time:
		b.WriteString("[" + t.unit.String() + "]")
	case Timestamp:
		b.WriteString("[" + t.unit.String())
		if t.zone != "" {
			b.WriteString(", tz=" + t.zone)
		}
		b.WriteString("]")
	default:
	}
	if t.nullable && t.kind != Null {
		b.WriteString("?")
	}
}

// Schema is the schema of a file: the fields of its root struct.
type Schema struct{ root Type }

// Fields returns a copy of the fields, in file order.
func (s *Schema) Fields() []Field { return s.root.Fields() }

// Field returns the first field with this name.
func (s *Schema) Field(name string) (Field, bool) {
	for _, f := range s.root.fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// String returns the root struct, like Type.String.
func (s *Schema) String() string { return s.root.String() }
