package vortex

import (
	"reflect"
	"time"

	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// guard is f when the layout checks out, and nothing otherwise.
func guard(ok bool, f fastFn) fastFn {
	if !ok {
		return nil
	}
	return f
}

// fastLeaf returns how to fill a field of type gt, at off in T, from a column of type vt
// without reflection, or nil when the field is not one of the fixed-width kinds, a string or
// a []byte, plain or in a sql.Null. The Go type has been checked against the column.
func fastLeaf(row, gt reflect.Type, vt Type, off uintptr) fastFn {
	var valid uintptr
	null := isSQLNull(gt)
	if null {
		v, ok := gt.Field(0), gt.Field(1)
		gt, valid, off = v.Type, off+ok.Offset, off+v.Offset
	}
	l := slot{row: row, gt: gt, off: off, valid: valid, null: null}
	switch gt {
	case timeType:
		unit, loc := vt.unit, time.UTC
		if vt.zone != "" {
			loc, _ = time.LoadLocation(vt.zone) // checked by the decoder
		}
		return getLeaf(l, func(c *Column) func(int) time.Time {
			return func(i int) time.Time { return timeOf(intAt(c, i), unit, loc) }
		})
	case durationType:
		unit := vt.unit
		return getLeaf(l, func(c *Column) func(int) time.Duration {
			return func(i int) time.Duration { return durationOf(intAt(c, i), unit) }
		})
	case decimalType:
		return getLeaf(l, func(c *Column) func(int) Decimal { return c.Decimal })
	}
	switch gt.Kind() {
	case reflect.Bool:
		return getLeaf(l, func(c *Column) func(int) bool { return c.Bool })
	case reflect.String:
		return getLeaf(l, func(c *Column) func(int) string {
			return func(i int) string { return string(c.Bytes(i)) }
		})
	case reflect.Slice:
		if isBytes(gt) && vt.kind != List {
			return getLeaf(l, func(c *Column) func(int) []byte { return c.Bytes })
		}
	case reflect.Array:
		if vt.kind == UUID {
			return getLeaf(l, func(c *Column) func(int) [uuidSize]byte {
				return func(i int) [uuidSize]byte { return [uuidSize]byte(c.Bytes(i)) }
			})
		}
	}
	return numberLeaf(l)
}

// slot is the place of a field to fill, and what it is.
type slot struct {
	row, gt    reflect.Type
	off, valid uintptr // of the value, and of the bool of a sql.Null
	null       bool
}

// numberLeaf is fastLeaf for the numbers.
func numberLeaf(l slot) fastFn {
	switch l.gt.Kind() {
	case reflect.Int8:
		return numLeaf[int8](l)
	case reflect.Int16:
		return numLeaf[int16](l)
	case reflect.Int32:
		return numLeaf[int32](l)
	case reflect.Int64, reflect.Int:
		return numLeaf[int64](l)
	case reflect.Uint8:
		return numLeaf[uint8](l)
	case reflect.Uint16:
		return numLeaf[uint16](l)
	case reflect.Uint32:
		return numLeaf[uint32](l)
	case reflect.Uint64, reflect.Uint:
		return numLeaf[uint64](l)
	case reflect.Float32:
		return numLeaf[float32](l)
	case reflect.Float64:
		return numLeaf[float64](l)
	}
	return nil
}

func numLeaf[V unsafex.Number](l slot) fastFn {
	return guard(checkLayout(l.row, l.gt, l.off, reflect.TypeFor[V]().Size()), func(c *Column, rows reflect.Value) error {
		vals, err := Values[V](c)
		if err == nil && l.null {
			unsafex.PutNull(rows, l.off, l.valid, vals, c.Null)
		} else if err == nil {
			unsafex.Put(rows, l.off, vals)
		}
		return err
	})
}

func getLeaf[V any](l slot, get func(c *Column) func(int) V) fastFn {
	return guard(checkLayout(l.row, l.gt, l.off, reflect.TypeFor[V]().Size()), func(c *Column, rows reflect.Value) error {
		if l.null {
			unsafex.PutNullFunc(rows, l.off, l.valid, get(c), c.Null)
		} else {
			unsafex.PutFunc(rows, l.off, get(c))
		}
		return nil
	})
}
