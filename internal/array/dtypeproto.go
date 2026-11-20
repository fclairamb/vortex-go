package array

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/fclairamb/vortex-go/internal/protowire"
)

// The arms of the oneof of the protobuf DType (vortex-array/proto/dtype.proto).
const (
	pbNull = 1 + iota
	pbBool
	pbPrimitive
	pbDecimal
	pbUtf8
	pbBinary
	pbStruct
	pbList
	pbExtension
	pbFixedSizeList
	pbVariant
	pbUnion
	pbMap
)

// The extension types of the format, by identifier.
const (
	extDate      = "vortex.date"
	extTime      = "vortex.time"
	extTimestamp = "vortex.timestamp"
	extUUID      = "vortex.uuid"
)

// ticksOfUnit is the ticks of a second of each time unit of the metadata of a time or a
// timestamp: nanoseconds, microseconds, milliseconds, seconds.
var ticksOfUnit = [...]int32{1e9, 1e6, 1e3, 1}

// ParseDType decodes a DType in protobuf, as an array stores it in its metadata. A date, a
// time and a timestamp are an Ext of their storage, with the identifier in Label.
func ParseDType(b []byte) (DType, error) { return parseDType(b, 0) }

func parseDType(b []byte, depth int) (DType, error) {
	if depth > maxDepth {
		return DType{}, fmt.Errorf("dtype nested deeper than %d levels", maxDepth)
	}
	it := protowire.NewIter(b)
	var t DType
	found := false
	for it.Next() {
		arm := it.Num()
		if arm < pbNull || arm > pbMap {
			continue
		}
		if it.Type() != protowire.Bytes {
			return DType{}, fmt.Errorf("dtype field %d has wire type %d", arm, it.Type())
		}
		var err error
		if t, err = parseArm(arm, it.Bytes(), depth); err != nil {
			return DType{}, err
		}
		found = true
	}
	if err := it.Err(); err != nil {
		return DType{}, err
	}
	if !found {
		return DType{}, errors.New("dtype has no type")
	}
	return t, nil
}

// fields is the content of the message of an arm: the scalar fields by number, the nested
// DTypes and the repeated fields in order.
type pbFields struct {
	varints map[int]uint64
	blobs   map[int][]byte
	repeats map[int][][]byte
}

func readPB(b []byte) (pbFields, error) {
	f := pbFields{varints: map[int]uint64{}, blobs: map[int][]byte{}, repeats: map[int][][]byte{}}
	it := protowire.NewIter(b)
	for it.Next() {
		switch it.Type() {
		case protowire.Varint:
			f.varints[it.Num()] = it.Uint()
		case protowire.Bytes:
			f.blobs[it.Num()] = it.Bytes()
			f.repeats[it.Num()] = append(f.repeats[it.Num()], it.Bytes())
		}
	}
	return f, it.Err()
}

func (f pbFields) nullable(num int) bool { return f.varints[num] != 0 }

// child parses the DType in field num.
func (f pbFields) child(num int, depth int) (DType, error) {
	b, ok := f.blobs[num]
	if !ok {
		return DType{}, fmt.Errorf("dtype field %d is missing", num)
	}
	return parseDType(b, depth+1)
}

func parseArm(arm int, b []byte, depth int) (DType, error) {
	f, err := readPB(b)
	if err != nil {
		return DType{}, err
	}
	switch arm {
	case pbNull:
		return DType{Kind: Null, Nullable: true}, nil
	case pbBool:
		return DType{Kind: Bool, Nullable: f.nullable(1)}, nil
	case pbPrimitive:
		if f.varints[1] > uint64(F64) {
			return DType{}, fmt.Errorf("primitive type %d is unknown", f.varints[1])
		}
		return DType{Kind: Prim, PType: PType(f.varints[1]), Nullable: f.nullable(2)}, nil
	case pbDecimal:
		return DType{Kind: Decimal, Scale: int8(int32(f.varints[2])), Nullable: f.nullable(3)}, nil
	case pbUtf8:
		return DType{Kind: Utf8, Nullable: f.nullable(1)}, nil
	case pbBinary:
		return DType{Kind: Binary, Nullable: f.nullable(1)}, nil
	case pbVariant:
		return DType{Kind: Variant, Nullable: f.nullable(1)}, nil
	case pbStruct:
		return parseStruct(f, depth)
	case pbExtension:
		return parseExtension(f, depth)
	case pbUnion:
		return DType{}, errors.New("union types are not supported")
	}
	return parseWrapped(arm, f, depth)
}

// parseWrapped reads the arms that hold other types: list, fixed-size list and map.
func parseWrapped(arm int, f pbFields, depth int) (DType, error) {
	elem, err := f.child(1, depth)
	if err != nil {
		return DType{}, err
	}
	switch arm {
	case pbList:
		return DType{Kind: List, Elem: &elem, Nullable: f.nullable(2)}, nil
	case pbFixedSizeList:
		return DType{Kind: FixedSizeList, Elem: &elem, Size: int(uint32(f.varints[2])), Nullable: f.nullable(3)}, nil
	}
	value, err := f.child(2, depth)
	if err != nil {
		return DType{}, err
	}
	entry := &DType{Kind: Struct, Fields: []Field{{Name: "key", Type: elem}, {Name: "value", Type: value}}}
	return DType{Kind: Map, Elem: entry, Nullable: f.nullable(4)}, nil
}

func parseStruct(f pbFields, depth int) (DType, error) {
	names, types := f.repeats[1], f.repeats[2]
	if len(names) != len(types) {
		return DType{}, fmt.Errorf("struct with %d names and %d types", len(names), len(types))
	}
	t := DType{Kind: Struct, Nullable: f.nullable(3), Fields: make([]Field, len(names))}
	for i := range names {
		if !utf8.Valid(names[i]) {
			return DType{}, errors.New("struct field name is not valid UTF-8")
		}
		ft, err := parseDType(types[i], depth+1)
		if err != nil {
			return DType{}, fmt.Errorf("field %q: %w", names[i], err)
		}
		t.Fields[i] = Field{Name: string(names[i]), Type: ft}
	}
	return t, nil
}

// parseExtension reads an extension type. Its nullability is its storage's. An unknown
// extension is its storage, with Label naming it.
func parseExtension(f pbFields, depth int) (DType, error) {
	storage, err := f.child(2, depth)
	if err != nil {
		return DType{}, err
	}
	t := DType{Kind: Ext, Storage: &storage, Nullable: storage.Nullable, Label: string(f.blobs[1])}
	meta := f.blobs[3]
	switch t.Label {
	case extTime, extTimestamp:
		if len(meta) == 0 || int(meta[0]) >= len(ticksOfUnit) {
			return DType{}, fmt.Errorf("%s metadata %v has no valid unit", t.Label, meta)
		}
		t.Ticks = ticksOfUnit[meta[0]]
	}
	return t, nil
}
