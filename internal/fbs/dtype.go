package fbs

import (
	"fmt"

	"github.com/fclairamb/vortex-go/internal/flatbuf"
)

// Kind is the arm of the DType union (dtype.fbs, enum Type).
type Kind uint8

// The arms of the union. 0 is NONE.
const (
	KindNull          Kind = 1
	KindBool          Kind = 2
	KindPrimitive     Kind = 3
	KindDecimal       Kind = 4
	KindUtf8          Kind = 5
	KindBinary        Kind = 6
	KindStruct        Kind = 7
	KindList          Kind = 8
	KindExtension     Kind = 9
	KindFixedSizeList Kind = 10
	KindVariant       Kind = 11
	KindUnion         Kind = 12
	KindMap           Kind = 13
)

// PType values (dtype.fbs, enum PType).
const (
	U8 uint8 = iota
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

// nullableID is the field id of `nullable` in the table of each kind. Null has none, and an
// extension type takes the nullability of its storage.
var nullableID = map[Kind]int{
	KindBool: 0, KindPrimitive: 1, KindDecimal: 2, KindUtf8: 0, KindBinary: 0, KindStruct: 2,
	KindList: 1, KindFixedSizeList: 2, KindVariant: 0, KindUnion: 3, KindMap: 3,
}

// DType is the table that wraps the union (field 0 is the type, field 1 the value).
type DType struct{ t flatbuf.Table }

// ReadDType reads the root DType of buf. maxDepth bounds the nesting of the type.
func ReadDType(buf []byte, maxDepth int) (DType, error) {
	t, err := flatbuf.Root(buf, maxDepth)
	return DType{t}, err
}

// body returns the table of the union, which must be of kind want (0 for any).
func (d DType) body(want Kind) (Kind, flatbuf.Table, error) {
	k, t, err := d.t.Union(0)
	if err == nil && Kind(k) != want && want != 0 {
		err = fmt.Errorf("fbs: dtype is of kind %d, not %d", k, want)
	}
	return Kind(k), t, err
}

// Kind returns the arm of the union. It is an error for the union to be NONE.
func (d DType) Kind() (Kind, error) {
	k, _, err := d.body(0)
	if err == nil && k == 0 {
		err = fmt.Errorf("fbs: dtype has no type")
	}
	return k, err
}

// Nullable reports the nullability of the type.
func (d DType) Nullable() (bool, error) {
	k, t, err := d.body(0)
	if err != nil {
		return false, err
	}
	switch k {
	case KindNull:
		return false, nil
	case KindExtension:
		_, storage, _, err := d.Extension()
		if err != nil {
			return false, err
		}
		return storage.Nullable()
	}
	id, ok := nullableID[k]
	if !ok {
		return false, fmt.Errorf("fbs: unknown dtype kind %d", k)
	}
	return t.Bool(id, false)
}

// Primitive returns the PType of a primitive type.
func (d DType) Primitive() (uint8, error) {
	_, t, err := d.body(KindPrimitive)
	if err != nil {
		return 0, err
	}
	return t.Uint8(0, U8)
}

// Decimal returns the precision and scale of a decimal type.
func (d DType) Decimal() (precision uint8, scale int8, err error) {
	_, t, err := d.body(KindDecimal)
	if err != nil {
		return 0, 0, err
	}
	if precision, err = t.Uint8(0, 0); err != nil {
		return 0, 0, err
	}
	scale, err = t.Int8(1, 0)
	return precision, scale, err
}

func dtypes(t flatbuf.Table, id int) (List[DType], error) {
	return tables(t, id, func(t flatbuf.Table) DType { return DType{t} })
}

func names(t flatbuf.Table, id int) (List[string], error) {
	v, err := t.Strings(id)
	return List[string]{v.String, v.Len()}, err
}

// Struct returns the field names and types of a struct type.
func (d DType) Struct() (List[string], List[DType], error) {
	_, t, err := d.body(KindStruct)
	if err != nil {
		return List[string]{}, List[DType]{}, err
	}
	return namesAndTypes(t)
}

func namesAndTypes(t flatbuf.Table) (List[string], List[DType], error) {
	n, err := names(t, 0)
	if err != nil {
		return n, List[DType]{}, err
	}
	ds, err := dtypes(t, 1)
	return n, ds, err
}

// Union returns the names, types and type ids of a union type.
func (d DType) Union() (List[string], List[DType], []byte, error) {
	_, t, err := d.body(KindUnion)
	if err != nil {
		return List[string]{}, List[DType]{}, nil, err
	}
	n, ds, err := namesAndTypes(t)
	if err != nil {
		return n, ds, nil, err
	}
	ids, _, err := t.Bytes(2)
	return n, ds, ids, err
}

func (d DType) child(kind Kind, id int) (DType, flatbuf.Table, error) {
	_, t, err := d.body(kind)
	if err != nil {
		return DType{}, t, err
	}
	c, _, err := t.Table(id)
	return DType{c}, t, err
}

// List returns the element type of a list type.
func (d DType) List() (DType, error) {
	e, _, err := d.child(KindList, 0)
	return e, err
}

// FixedSizeList returns the element type and the size of a fixed-size list type.
func (d DType) FixedSizeList() (DType, uint32, error) {
	e, t, err := d.child(KindFixedSizeList, 0)
	if err != nil {
		return e, 0, err
	}
	size, err := t.Uint32(1, 0)
	return e, size, err
}

// Extension returns the id, the storage type and the metadata of an extension type.
func (d DType) Extension() (id string, storage DType, metadata []byte, err error) {
	storage, t, err := d.child(KindExtension, 1)
	if err != nil {
		return "", storage, nil, err
	}
	if id, _, err = t.String(0); err != nil {
		return "", storage, nil, err
	}
	metadata, _, err = t.Bytes(2)
	return id, storage, metadata, err
}

// Map returns the key and value types of a map type, and whether its keys are sorted.
func (d DType) Map() (key, value DType, keysSorted bool, err error) {
	key, t, err := d.child(KindMap, 0)
	if err != nil {
		return key, DType{}, false, err
	}
	v, _, err := t.Table(1)
	if err != nil {
		return key, DType{}, false, err
	}
	keysSorted, err = t.Bool(2, false)
	return key, DType{v}, keysSorted, err
}
