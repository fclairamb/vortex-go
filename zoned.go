package vortex

import (
	"encoding/binary"
	"fmt"
	"maps"
	"slices"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/edition"
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

// statistics are the aggregates a zone map can hold, by identifier. They are recognized in
// the metadata of a zoned layout; a statistic is a hint, so one that is not in this table
// is kept in aggregateSpec and otherwise ignored.
var statistics = map[string]string{
	"vortex.min":         "smallest value of the zone",
	"vortex.max":         "largest value of the zone",
	"vortex.bounded_min": "lower bound of the zone, possibly truncated",
	"vortex.bounded_max": "upper bound of the zone, possibly truncated",
	"vortex.null_count":  "number of nulls of the zone",
	"vortex.nan_count":   "number of NaNs of the zone",
}

// statisticIDs lists the statistics that are recognized.
func statisticIDs() []string { return slices.Sorted(maps.Keys(statistics)) }

var _ = edition.Register(edition.KindAggregate, statisticIDs)

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
			edition.Met(edition.KindAggregate, spec.id)
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

// parseZoneLayout reads the metadata of a zoned layout and checks its children. The zones
// child of the zone map is the second one.
func parseZoneLayout(l *layoutNode, legacy bool) (zoneMap, error) {
	var z zoneMap
	var err error
	if legacy {
		z, err = parseStats(l.metadata)
	} else {
		z, err = parseZoned(l.metadata)
	}
	if err != nil {
		return z, err
	}
	if len(l.children) != 2 {
		return z, badLayout("a zoned layout has %d children, want 2 (data, zones)", len(l.children))
	}
	if l.children[0].rowCount != l.rowCount {
		return z, badLayout("zoned data has %d rows, its parent %d", l.children[0].rowCount, l.rowCount)
	}
	z.zones = l.children[1]
	return z, nil
}

func (f *File) newZoned(
	l *layoutNode, legacy bool, t array.DType, opt array.Options, path string, lo, hi int,
) (reader, error) {
	z, err := parseZoneLayout(l, legacy)
	if err != nil {
		return nil, err
	}
	data, err := f.newReader(l.children[0], t, opt, path, lo, hi)
	if err != nil {
		return nil, err
	}
	return &zoned{reader: data, zones: z}, nil
}

// dict is vortex.dict: the values, then the codes. The values are read once, when the first
// codes are, and kept. The rows are made a batch at a time, from the codes of the batch.
type dict struct {
	codes  reader
	f      *File
	values *layoutNode
	vals   *array.Array
	path   string
	t      array.DType
	opt    array.Options
	arena  *array.Arena // the buffers of the rows of the last batch
	varena *array.Arena // the buffers of vals
	slot   array.Array  // the rows take returns
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
	return &dict{f: f, values: l.children[0], codes: codes, t: t, opt: opt, ptype: ptype, path: path,
		arena: opt.Pool.NewArena(), varena: opt.Pool.NewArena()}, nil
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

func (d *dict) skip(n int) {
	d.codes.skip(n)
	d.finish()
}

// finish gives the memory of the reader back once its last row is taken or skipped: the batch
// that holds it is the last one to read it.
func (d *dict) finish() {
	if d.opt.Pool != nil && d.codes.avail() == 0 {
		d.opt.Pool.Retire(d.arena)
		d.opt.Pool.Retire(d.varena)
	}
}

func (d *dict) units(base int, out *[]*unit) int { return d.codes.units(base, out) }

func (d *dict) take(n int) (*array.Array, error) {
	if err := d.load(); err != nil {
		return nil, err
	}
	codes, err := d.codes.take(n)
	if err != nil {
		return nil, err
	}
	return d.rowsOf(codes)
}

// takeSel makes the rows kept from their codes, when the codes can be decoded for them only.
func (d *dict) takeSel(n int, sel []int) (*array.Array, bool, error) {
	if err := d.load(); err != nil {
		return nil, false, err
	}
	codes, ok, err := takeSel(d.codes, n, sel)
	if err != nil || !ok {
		return nil, false, err
	}
	a, err := d.rowsOf(codes)
	return a, err == nil, err
}

// load reads the values, once.
func (d *dict) load() error {
	if d.vals == nil {
		opt := d.opt
		opt.Pool, opt.Arena = nil, d.varena // the values live as long as the reader
		r, err := d.f.newReader(d.values, d.t, opt, d.path, 0, int(d.values.rowCount))
		if err != nil {
			return err
		}
		if d.vals, err = readAll(r, d.t); err != nil {
			return err
		}
	}
	return nil
}

// rowsOf makes the rows of the codes of a batch.
func (d *dict) rowsOf(codes *array.Array) (*array.Array, error) {
	// The rows are made in the memory of the ones of the batch before, which stays in the
	// cache of the processor.
	d.arena.Release()
	a, err := array.TakeDictInto(&d.slot, d.arena, d.vals, codes, d.ptype, d.t)
	if err != nil {
		return nil, fmt.Errorf("%w: layout: dict: %v", ErrCorrupt, err)
	}
	d.finish()
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
		parts = append(parts, a.SliceInto(new(array.Array), 0, n)) // take reuses its array
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return array.Concat(t, parts), nil
}

func (z *zoned) takeSel(n int, sel []int) (*array.Array, bool, error) {
	return takeSel(z.reader, n, sel)
}
