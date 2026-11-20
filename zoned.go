package vortex

import (
	"encoding/binary"
	"fmt"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/protowire"
)

// zonedMetadataVersion is the first byte of the metadata of a vortex.zoned layout.
const zonedMetadataVersion = 1

// aggregateSpec is a statistic of a zone map: the identifier of the aggregate, and its
// options, serialized.
type aggregateSpec struct {
	id      string
	options []byte
}

// zoneMap is what a zoned layout says about its zones. The zones table itself (one row per
// zone, one field per statistic) is the layout node zones, not read here.
type zoneMap struct {
	zones      *layoutNode
	aggregates []aggregateSpec
	stats      []byte
	zoneLen    uint32
	legacy     bool
}

// parseZoned reads the metadata of a vortex.zoned layout: a version byte, then a protobuf
// {zone_len: uint32, aggregate_specs: [{id: string, options: bytes}]}.
func parseZoned(meta []byte) (zoneMap, error) {
	var z zoneMap
	if len(meta) == 0 {
		return z, badLayout("zoned metadata is empty")
	}
	if meta[0] != zonedMetadataVersion {
		return z, badLayout("zoned metadata version %d, want %d", meta[0], zonedMetadataVersion)
	}
	if len(meta) == 1 {
		return z, badLayout("zoned metadata has no protobuf")
	}
	it := protowire.NewIter(meta[1:])
	for it.Next() {
		switch {
		case it.Num() == 1 && it.Type() == protowire.Varint:
			if it.Uint() > 1<<32-1 {
				return z, badLayout("zone length %d overflows 32 bits", it.Uint())
			}
			z.zoneLen = uint32(it.Uint())
		case it.Num() == 2 && it.Type() == protowire.Bytes:
			spec, err := parseAggregateSpec(it.Bytes())
			if err != nil {
				return z, err
			}
			z.aggregates = append(z.aggregates, spec)
		}
	}
	if err := it.Err(); err != nil {
		return z, badLayout("zoned metadata: %v", err)
	}
	return z, nil
}

func parseAggregateSpec(b []byte) (aggregateSpec, error) {
	var s aggregateSpec
	it := protowire.NewIter(b)
	for it.Next() {
		if it.Type() != protowire.Bytes {
			continue
		}
		switch it.Num() {
		case 1:
			s.id = string(it.Bytes())
		case 2:
			s.options = it.Bytes()
		}
	}
	if err := it.Err(); err != nil {
		return s, badLayout("aggregate spec: %v", err)
	}
	return s, nil
}

// parseStats reads the metadata of a vortex.stats layout: the zone length as a u32
// little-endian, then the bitset of the statistics present.
func parseStats(meta []byte) (zoneMap, error) {
	if len(meta) < 4 {
		return zoneMap{}, badLayout("stats metadata is %d bytes, want 4 at least", len(meta))
	}
	return zoneMap{zoneLen: binary.LittleEndian.Uint32(meta), stats: meta[4:], legacy: true}, nil
}

// zoned is vortex.zoned and its older form vortex.stats: the data, and the statistics of
// every zone of it. Reading the rows is reading the data.
type zoned struct {
	reader
	zones zoneMap
}

func (f *File) newZoned(
	l *layoutNode, legacy bool, t array.DType, opt array.Options, path string, lo, hi int,
) (reader, error) {
	var z zoneMap
	var err error
	if legacy {
		z, err = parseStats(l.metadata)
	} else {
		z, err = parseZoned(l.metadata)
	}
	if err != nil {
		return nil, err
	}
	if len(l.children) != 2 {
		return nil, badLayout("a zoned layout has %d children, want 2 (data, zones)", len(l.children))
	}
	if l.children[0].rowCount != l.rowCount {
		return nil, badLayout("zoned data has %d rows, its parent %d", l.children[0].rowCount, l.rowCount)
	}
	z.zones = l.children[1]
	data, err := f.newReader(l.children[0], t, opt, path, lo, hi)
	if err != nil {
		return nil, err
	}
	return &zoned{reader: data, zones: z}, nil
}

// dict is vortex.dict: the values, then the codes. The values are read once, when the first
// codes are, and kept.
type dict struct {
	codes  reader
	f      *File
	values *layoutNode
	vals   *array.Array
	opt    array.Options
	path   string
	t      array.DType
	ptype  array.PType
}

func (f *File) newDict(l *layoutNode, t array.DType, opt array.Options, path string, lo, hi int) (reader, error) {
	if len(l.children) != 2 {
		return nil, badLayout("a dict layout has %d children, want 2 (values, codes)", len(l.children))
	}
	ptype, nullable, err := parseDict(l.metadata, t.Nullable)
	if err != nil {
		return nil, err
	}
	if l.children[1].rowCount != l.rowCount {
		return nil, badLayout("dict codes have %d rows, their parent %d", l.children[1].rowCount, l.rowCount)
	}
	ct := array.DType{Kind: array.Prim, PType: ptype, Nullable: nullable}
	codes, err := f.newReader(l.children[1], ct, opt, path, lo, hi)
	if err != nil {
		return nil, err
	}
	return &dict{f: f, values: l.children[0], codes: codes, t: t, opt: opt, ptype: ptype, path: path}, nil
}

// parseDict reads the metadata of a vortex.dict layout, {codes_ptype, is_nullable_codes?,
// all_values_referenced?}, and returns the type of the codes and whether they are nullable.
func parseDict(meta []byte, parentNullable bool) (array.PType, bool, error) {
	var ptype uint64
	nullable := parentNullable
	it := protowire.NewIter(meta)
	for it.Next() {
		if it.Type() != protowire.Varint {
			continue
		}
		switch it.Num() {
		case 1:
			ptype = it.Uint()
		case 2:
			nullable = it.Uint() != 0
		}
	}
	if err := it.Err(); err != nil {
		return 0, false, badLayout("dict metadata: %v", err)
	}
	if ptype > uint64(array.I64) {
		return 0, false, badLayout("dict codes type %d is not an integer type", ptype)
	}
	return array.PType(ptype), nullable, nil
}

func (d *dict) avail() int { return d.codes.avail() }

func (d *dict) take(n int) (*array.Array, error) {
	if d.vals == nil {
		r, err := d.f.newReader(d.values, d.t, d.opt, d.path, 0, int(d.values.rowCount))
		if err != nil {
			return nil, err
		}
		if d.vals, err = readAll(r, d.t); err != nil {
			return nil, err
		}
	}
	codes, err := d.codes.take(n)
	if err != nil {
		return nil, err
	}
	a, err := array.TakeDict(d.vals, codes, d.ptype, d.t)
	if err != nil {
		return nil, fmt.Errorf("%w: layout: dict: %v", ErrCorrupt, err)
	}
	return a, nil
}

// readAll reads every row of r.
func readAll(r reader, t array.DType) (*array.Array, error) {
	var parts []*array.Array
	for n := r.avail(); n > 0; n = r.avail() {
		a, err := r.take(n)
		if err != nil {
			return nil, err
		}
		parts = append(parts, a)
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return array.Concat(t, parts), nil
}
