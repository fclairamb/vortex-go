package vortex

import (
	"context"
	"fmt"
	"iter"

	"github.com/fclairamb/vortex-go/internal/array"
)

// maxBatchRows is the most rows of a batch.
const maxBatchRows = 8192

// Query selects the rows of a scan. It has no field yet: every row is read.
type Query struct{}

// scanOptions are the knobs of a scan that tests change.
type scanOptions struct {
	array     array.Options
	batchRows int
}

// Scan reads the columns of the file, batch by batch, in order, on the calling goroutine.
// A batch has at most 8,192 rows. Every column is read: columns must be empty for now.
func (f *File) Scan(ctx context.Context, q Query, columns ...string) iter.Seq2[*Batch, error] {
	if len(columns) > 0 {
		return func(yield func(*Batch, error) bool) {
			yield(nil, fmt.Errorf("vortex: Scan reads every column for now, got %d column names", len(columns)))
		}
	}
	return f.scan(ctx, scanOptions{batchRows: maxBatchRows})
}

func (f *File) scan(ctx context.Context, o scanOptions) iter.Seq2[*Batch, error] {
	return func(yield func(*Batch, error) bool) {
		rd, err := f.newReader(f.layout, arrayType(f.schema.root), o.array)
		if err != nil {
			yield(nil, err)
			return
		}
		for {
			if err := ctx.Err(); err != nil {
				yield(nil, err)
				return
			}
			n := min(o.batchRows, rd.avail())
			if n == 0 {
				return
			}
			arr, err := rd.take(n)
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(f.batch(arr), nil) {
				return
			}
		}
	}
}

// batch makes the batch of a struct array.
func (f *File) batch(arr *array.Array) *Batch {
	b := &Batch{n: arr.Len, cols: make([]*Column, len(arr.Fields))}
	for i, fld := range f.schema.root.fields {
		b.cols[i] = &Column{arr: arr.Fields[i], typ: fld.Type}
	}
	return b
}

var primOfKind = map[Kind]array.PType{
	Int8: array.I8, Int16: array.I16, Int32: array.I32, Int64: array.I64,
	Uint8: array.U8, Uint16: array.U16, Uint32: array.U32, Uint64: array.U64,
	Float16: array.F16, Float32: array.F32, Float64: array.F64,
}

// arrayType is the type the decoders produce for t.
func arrayType(t Type) array.DType {
	d := array.DType{Nullable: t.nullable}
	switch t.kind {
	case Null:
		d.Kind = array.Null
	case Bool:
		d.Kind = array.Bool
	case Struct:
		d.Kind = array.Struct
		for _, f := range t.fields {
			d.Names = append(d.Names, f.Name)
			d.Fields = append(d.Fields, arrayType(f.Type))
		}
	case Date, Time, Timestamp:
		d.Kind = array.Ext
		d.Storage = &array.DType{Kind: array.Prim, PType: storage(t), Nullable: t.nullable}
	default:
		if p, ok := primOfKind[t.kind]; ok {
			d.Kind, d.PType = array.Prim, p
		} else {
			d.Kind, d.Label = array.Other, t.kind.String()
		}
	}
	return d
}

// storage is the primitive type that holds a date, a time or a timestamp.
func storage(t Type) array.PType {
	switch {
	case t.kind == Date && t.unit == Day, t.kind == Time && t.unit >= Millisecond:
		return array.I32
	}
	return array.I64
}
