package vortex

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/fclairamb/vortex-go/internal/compress"
	"github.com/fclairamb/vortex-go/internal/edition"
	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/variant"
)

// validType reports a type that cannot be written. path is the dotted name of its field.
func validType(t Type, path string) error {
	var msg string
	switch t.kind {
	case KindDecimal:
		if t.precision < 1 || t.precision > 76 || int(t.scale) > int(t.precision) {
			msg = fmt.Sprintf("decimal of precision %d and scale %d, want a precision of 1 to 76 and a scale up to it",
				t.precision, t.scale)
		}
	case Date, Time, Timestamp:
		msg = validTemporal(t)
	case List, FixedSizeList:
		return validType(*t.elem, path+"[]")
	case Map:
		if err := validType(*t.elem, path+".key"); err != nil {
			return err
		}
		return validType(*t.value, path+".value")
	case Struct:
		for _, f := range t.fields {
			if err := validType(f.Type, joinPath(path, f.Name)); err != nil {
				return err
			}
		}
	default:
		if t.kind > Variant {
			msg = fmt.Sprintf("unknown kind %d", uint8(t.kind))
		}
	}
	if msg != "" {
		return fmt.Errorf("vortex: NewWriter: field %q: %s", path, msg)
	}
	return nil
}

// validTemporal says what is wrong with the unit or the zone of a date, a time or a timestamp.
func validTemporal(t Type) string {
	switch {
	case t.kind == Date && t.unit != Millisecond && t.unit != Day:
		return fmt.Sprintf("a date in %s, want ms or days", t.unit)
	case t.kind == Time && t.unit > Second:
		return fmt.Sprintf("a time in %s, want ns, us, ms or s", t.unit)
	case t.kind == Timestamp && (t.unit > Second || len(t.zone) > 1<<16-1):
		return fmt.Sprintf("a timestamp in %s with a zone of %d bytes, want ns, us, ms or s and a zone of up to 65535 bytes",
			t.unit, len(t.zone))
	}
	return ""
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// goType is the name of the Go type that Of takes for the numbers of kind k: i32 is int32,
// u8 is uint8, f64 is float64.
func goType(k Kind) string {
	name := k.String()
	switch name[0] {
	case 'i':
		return "int" + name[1:]
	case 'u':
		return "uint" + name[1:]
	default:
		return "float" + name[1:]
	}
}

func wcheckf(path, format string, args ...any) error {
	return fmt.Errorf("vortex: Write: field %q: %s", path, fmt.Sprintf(format, args...))
}

// checkData reports a Data that does not fit its type: the rows, the shape, the nulls, and
// what the encoding relies on (UTF-8, the offsets of a list, the width of a decimal).
func checkData(path string, t Type, d Data, n int) error {
	if d.body == nil {
		return wcheckf(path, "no values: the Data is the zero value")
	}
	if d.n != n {
		return wcheckf(path, "%d rows, want %d", d.n, n)
	}
	if err := checkNulls(path, t, d, n); err != nil {
		return err
	}
	switch t.kind {
	case Null:
		return want(path, t, d, d.kind == dNull)
	case Bool:
		return want(path, t, d, d.kind == dBool)
	case Utf8, Binary, UUID:
		return checkBytes(path, t, d)
	case KindDecimal:
		return checkDecimals(path, t, d)
	case Variant:
		return checkVariants(path, t, d)
	case Struct:
		return checkStruct(path, t, d)
	case List, FixedSizeList, Map:
		return checkList(path, t, d)
	default:
		return want(path, t, d, d.kind == dPrim && d.pk == storageKind(t))
	}
}

// want is the error of a Data of another shape than the type needs, when ok is false.
func want(path string, t Type, d Data, ok bool) error {
	if ok {
		return nil
	}
	return wcheckf(path, "the type %s cannot take %s%s", t, d.describe(), hint(t))
}

// hint says which constructor the type calls for.
func hint(t Type) string {
	switch t.kind {
	case Null:
		return " (OfNulls)"
	case Bool:
		return " (OfBools)"
	case Utf8, Binary:
		return " (OfStrings or OfBytes)"
	case UUID:
		return " (OfBytes)"
	case KindDecimal:
		return " (OfDecimals)"
	case Variant:
		return " (OfVariants)"
	case Struct:
		return " (OfStruct)"
	case List, FixedSizeList, Map:
		return " (OfList)"
	default:
		return " (Of[" + goType(storageKind(t)) + "])"
	}
}

func checkNulls(path string, t Type, d Data, n int) error {
	if d.nulls == nil || t.kind == Null {
		return nil
	}
	if len(d.nulls) != n {
		return wcheckf(path, "Nulls has %d values for %d rows", len(d.nulls), n)
	}
	if t.nullable {
		return nil
	}
	for i, null := range d.nulls {
		if null {
			return wcheckf(path, "row %d is null and the field is not nullable", i)
		}
	}
	return nil
}

func isNull(nulls []bool, i int) bool { return nulls != nil && nulls[i] }

// checkBytes checks the strings of a utf8 column and the 16 bytes of a uuid.
func checkBytes(path string, t Type, d Data) error {
	if err := want(path, t, d, d.kind == dBytes || d.kind == dStr && t.kind != UUID); err != nil {
		return err
	}
	for i := range d.n {
		if isNull(d.nulls, i) {
			continue
		}
		var size int
		var valid bool
		if d.kind == dStr {
			size, valid = len(d.strs[i]), t.kind != Utf8 || utf8.ValidString(d.strs[i])
		} else {
			size, valid = len(d.bins[i]), t.kind != Utf8 || utf8.Valid(d.bins[i])
		}
		if t.kind == UUID && size != 16 {
			return wcheckf(path, "row %d is %d bytes, a uuid is 16", i, size)
		}
		if !valid {
			return wcheckf(path, "row %d is not valid UTF-8", i)
		}
	}
	return nil
}

// decimalWidth is the bytes of a decimal of this precision, the narrowest that holds it.
func decimalWidth(precision uint8) int {
	switch {
	case precision <= 2:
		return 1
	case precision <= 4:
		return 2
	case precision <= 9:
		return 4
	case precision <= 18:
		return 8
	case precision <= 38:
		return 16
	default:
		return 32
	}
}

// fits reports whether the value is held by its first width bytes, two's complement.
func (d Decimal) fits(width int) bool {
	le := d.le()
	sign := byte(0)
	if d.negative() {
		sign = 0xff
	}
	for _, b := range le[width:] {
		if b != sign {
			return false
		}
	}
	return le[width-1]&0x80 == sign&0x80
}

func checkDecimals(path string, t Type, d Data) error {
	if err := want(path, t, d, d.kind == dDec); err != nil {
		return err
	}
	w := decimalWidth(t.precision)
	for i, v := range d.decs {
		if !isNull(d.nulls, i) && !v.fits(w) {
			return wcheckf(path, "row %d does not fit the %d bytes of a decimal of precision %d", i, w, t.precision)
		}
	}
	return nil
}

// variantChecker checks the values of the variant columns. It builds nothing, so it is shared.
var variantChecker = variant.Encoder{Decimal: variantDecimalOf}

// checkVariants checks that every value of a variant column is one the encoding holds.
func checkVariants(path string, t Type, d Data) error {
	if err := want(path, t, d, d.kind == dVars); err != nil {
		return err
	}
	for i, v := range d.vars {
		if err := variantChecker.Check(v); err != nil {
			return wcheckf(path, "row %d: %v", i, err)
		}
	}
	return nil
}

func checkStruct(path string, t Type, d Data) error {
	if err := want(path, t, d, d.kind == dStruct); err != nil {
		return err
	}
	if len(d.kids) != len(t.fields) {
		return wcheckf(path, "OfStruct has %d fields, the type has %d", len(d.kids), len(t.fields))
	}
	for i, f := range t.fields {
		if err := checkData(joinPath(path, f.Name), f.Type, d.kids[i], d.n); err != nil {
			return err
		}
	}
	return nil
}

func checkList(path string, t Type, d Data) error {
	if err := want(path, t, d, d.kind == dList); err != nil {
		return err
	}
	elems := d.kids[0]
	if elems.body == nil {
		return wcheckf(path, "OfList has no elements: the Data is the zero value")
	}
	if len(d.offs) != d.n+1 {
		return wcheckf(path, "OfList has %d offsets, %d rows need %d", len(d.offs), d.n, d.n+1)
	}
	if elems.n > maxListElems {
		return wcheckf(path, "%d elements, at most %d", elems.n, maxListElems)
	}
	prev := 0
	for i, o := range d.offs {
		if o < prev || o > elems.n {
			return wcheckf(path, "offset %d is %d: offsets must not decrease and stay within the %d elements",
				i, o, elems.n)
		}
		prev = o
	}
	switch t.kind {
	case FixedSizeList:
		if elems.n != d.n*t.Size() {
			return wcheckf(path, "%d elements for %d rows of a fixed-size list of %d", elems.n, d.n, t.Size())
		}
		for i, o := range d.offs {
			if o != i*t.Size() {
				return wcheckf(path, "offset %d is %d, a fixed-size list of %d has regular offsets (%d)",
					i, o, t.Size(), i*t.Size())
			}
		}
		return checkData(path+"[]", *t.elem, elems, elems.n)
	case Map:
		return checkEntries(path, t, elems)
	default:
		return checkData(path+"[]", *t.elem, elems, elems.n)
	}
}

// checkEntries checks the keys and the values of a map: elems is OfStruct(keys, values).
func checkEntries(path string, t Type, elems Data) error {
	if elems.kind != dStruct || len(elems.kids) != 2 || elems.nulls != nil {
		return wcheckf(path, "the elements of a map are OfStruct(keys, values), got %s", elems.describe())
	}
	if err := checkData(path+".key", *t.elem, elems.kids[0], elems.n); err != nil {
		return err
	}
	return checkData(path+".value", *t.value, elems.kids[1], elems.n)
}

// setEdition sets what the writer leaves out for the edition of the file, "" being the newest.
func (w *Writer) setEdition(name string) error {
	if name == "" {
		name = edition.ID
	}
	if !edition.Known(name) {
		return fmt.Errorf("vortex: NewWriter: unknown edition %q, want one of %s", name, strings.Join(edition.Names(), ", "))
	}
	w.opt.Edition = name
	has := func(kind, id string) bool { return edition.Allows(name, kind, id) }
	w.legacyZones = !has(edition.KindLayout, "vortex.zoned")
	w.legacyFile = !has(edition.KindAggregate, aggMax)
	w.plainLists = !has(edition.KindArray, "vortex.listview")
	w.limit = compress.Limits{
		NoRLE:      !has(edition.KindArray, encode.IDRLE),
		NoMasked:   !has(edition.KindArray, encode.IDMasked),
		NoSequence: !has(edition.KindArray, encode.IDSequence),
		NoZstd:     !has(edition.KindArray, encode.IDZstd),
		NoOnPair:   !has(edition.KindArray, encode.IDOnPair),
	}
	return nil
}

// validEdition reports a type that the edition of the file cannot hold. path is the dotted
// name of its field.
func validEdition(t Type, path, name string) error {
	var kind, id string
	switch t.kind {
	case FixedSizeList:
		kind, id = edition.KindArray, idFixedSizeList
	case Map:
		kind, id = edition.KindArray, idMap
	case UUID:
		kind, id = edition.KindDType, "vortex.uuid"
	case Variant:
		kind, id = edition.KindArray, idVariant
	}
	if id != "" && !edition.Allows(name, kind, id) {
		return fmt.Errorf("vortex: NewWriter: field %q: a %s is not in the edition %q (%s %s is newer)",
			path, t.kind, name, kind, id)
	}
	switch t.kind {
	case List, FixedSizeList:
		return validEdition(*t.elem, path+"[]", name)
	case Map:
		if err := validEdition(*t.elem, path+".key", name); err != nil {
			return err
		}
		return validEdition(*t.value, path+".value", name)
	case Struct:
		for _, f := range t.fields {
			if err := validEdition(f.Type, joinPath(path, f.Name), name); err != nil {
				return err
			}
		}
	}
	return nil
}

// variantDecimalOf is how the variant encoder knows a VariantDecimal: its unscaled value as
// little-endian bytes, and its scale.
func variantDecimalOf(v any) ([]byte, int, bool) {
	d, ok := v.(VariantDecimal)
	if !ok {
		return nil, 0, false
	}
	le := d.Value.le()
	return le[:], d.Scale, true
}
