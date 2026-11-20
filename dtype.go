package vortex

import (
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/fclairamb/vortex-go/internal/edition"
	"github.com/fclairamb/vortex-go/internal/fbs"
)

// maxDTypeDepth bounds the flatbuffer tables of a dtype: two per level of nesting.
const maxDTypeDepth = 128

var primitiveKinds = map[uint8]Kind{
	fbs.U8: Uint8, fbs.U16: Uint16, fbs.U32: Uint32, fbs.U64: Uint64,
	fbs.I8: Int8, fbs.I16: Int16, fbs.I32: Int32, fbs.I64: Int64,
	fbs.F16: Float16, fbs.F32: Float32, fbs.F64: Float64,
}

// simpleKinds are the dtypes without a payload besides their nullability.
var simpleKinds = map[fbs.Kind]Kind{
	fbs.KindNull: Null, fbs.KindBool: Bool, fbs.KindUtf8: Utf8, fbs.KindBinary: Binary,
	fbs.KindVariant: Variant,
}

// typeOf converts a dtype flatbuffer into a Type.
func typeOf(d fbs.DType) (Type, error) {
	k, err := d.Kind()
	if err != nil {
		return Type{}, err
	}
	if k == fbs.KindExtension {
		return extensionType(d)
	}
	t := Type{}
	if t.nullable, err = d.Nullable(); err != nil {
		return Type{}, err
	}
	if kind, ok := simpleKinds[k]; ok {
		t.kind = kind
		t.nullable = t.nullable || kind == Null // the null type holds only nulls
		return t, nil
	}
	switch k {
	case fbs.KindPrimitive:
		var p uint8
		if p, err = d.Primitive(); err != nil {
			return Type{}, err
		}
		kind, ok := primitiveKinds[p]
		if !ok {
			return Type{}, fmt.Errorf("unknown primitive type %d", p)
		}
		t.kind = kind
	case fbs.KindDecimal:
		t.kind = KindDecimal
		t.precision, t.scale, err = d.Decimal()
	case fbs.KindList, fbs.KindFixedSizeList, fbs.KindMap, fbs.KindStruct:
		err = nested(d, k, &t)
	default:
		err = errors.New("union types are not supported")
	}
	return t, err
}

// nested fills the types made of other types.
func nested(d fbs.DType, k fbs.Kind, t *Type) (err error) {
	switch k {
	case fbs.KindList:
		t.kind = List
		t.elem, err = child(d.List())
	case fbs.KindFixedSizeList:
		t.kind = FixedSizeList
		var e fbs.DType
		if e, t.size, err = d.FixedSizeList(); err == nil {
			t.elem, err = child(e, nil)
		}
	case fbs.KindMap:
		t.kind = Map
		var key, value fbs.DType
		if key, value, _, err = d.Map(); err == nil {
			if t.elem, err = child(key, nil); err == nil {
				t.value, err = child(value, nil)
			}
		}
	default:
		t.kind = Struct
		t.fields, err = structFields(d)
	}
	return err
}

func child(d fbs.DType, err error) (*Type, error) {
	if err != nil {
		return nil, err
	}
	t, err := typeOf(d)
	return &t, err
}

func structFields(d fbs.DType) ([]Field, error) {
	names, types, err := d.Struct()
	if err != nil {
		return nil, err
	}
	if names.Len() != types.Len() {
		return nil, fmt.Errorf("struct with %d names and %d types", names.Len(), types.Len())
	}
	fields := make([]Field, names.Len())
	for i := range fields {
		name, err := names.At(i)
		if err != nil {
			return nil, err
		}
		ft, err := types.At(i)
		if err != nil {
			return nil, err
		}
		if fields[i].Type, err = typeOf(ft); err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		fields[i].Name = name
	}
	return fields, nil
}

// extensionType reads the extension types the format defines. The storage is only read for
// its nullability, which the extension takes.
func extensionType(d fbs.DType) (Type, error) {
	id, storage, meta, err := d.Extension()
	if err != nil {
		return Type{}, err
	}
	t := Type{}
	if t.nullable, err = storage.Nullable(); err != nil {
		return Type{}, err
	}
	edition.Met(edition.KindDType, id)
	parse, ok := extensionTypes[id]
	if !ok {
		return Type{}, fmt.Errorf("unsupported extension type %q: %w", id, errors.ErrUnsupported)
	}
	err = parse(&t, meta)
	return t, err
}

// extensionTypes are the extension types of the format, by identifier: each sets the kind
// and the parameters of the type from the metadata.
var extensionTypes = map[string]func(t *Type, meta []byte) error{
	"vortex.date": func(t *Type, meta []byte) (err error) {
		t.kind = Date
		t.unit, err = unitOf(meta, Millisecond, Day)
		return err
	},
	"vortex.time": func(t *Type, meta []byte) (err error) {
		t.kind = Time
		t.unit, err = unitOf(meta, Nanosecond, Second)
		return err
	},
	"vortex.timestamp": func(t *Type, meta []byte) (err error) {
		t.kind = Timestamp
		t.unit, t.zone, err = timestampMeta(meta)
		return err
	},
	"vortex.uuid": func(t *Type, meta []byte) error {
		t.kind = UUID
		if len(meta) > 1 {
			return fmt.Errorf("uuid metadata of %d bytes", len(meta))
		}
		return nil
	},
}

// extensionIDs lists the extension types that are read.
func extensionIDs() []string { return slices.Sorted(maps.Keys(extensionTypes)) }

var _ = edition.Register(edition.KindDType, extensionIDs)

func unitOf(meta []byte, lo, hi Unit) (Unit, error) {
	if len(meta) != 1 {
		return 0, fmt.Errorf("unit metadata of %d bytes, want 1", len(meta))
	}
	u := Unit(meta[0])
	if u < lo || u > hi {
		return 0, fmt.Errorf("unit %d is not allowed here", meta[0])
	}
	return u, nil
}

// timestampMeta reads [unit u8][zone length u16 LE][zone].
func timestampMeta(meta []byte) (Unit, string, error) {
	if len(meta) < 3 {
		return 0, "", fmt.Errorf("timestamp metadata of %d bytes", len(meta))
	}
	u, err := unitOf(meta[:1], Nanosecond, Second)
	if err != nil {
		return 0, "", err
	}
	n := int(binary.LittleEndian.Uint16(meta[1:]))
	if len(meta) != 3+n {
		return 0, "", fmt.Errorf("timestamp zone of %d bytes in metadata of %d", n, len(meta))
	}
	return u, string(meta[3:]), nil
}
