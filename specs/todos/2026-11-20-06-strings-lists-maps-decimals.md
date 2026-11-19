---
model: sonnet
effort: high
pr: batch
automerge: false
---

# Strings, binary, lists, maps and decimals are read

## Problem

After the first scan, only numbers, booleans and structs are read. The other logical types
of `docs/interface.md` have canonical arrays of their own, and decimals need the
`vortex.Decimal` value type that Florent chose on 2026-11-19 (`docs/goals.md`, "Decisions
taken").

Upstream paths are in `~/code/other/vortex/vortex-array/src/arrays/`.

## Proposal

1. **`vortex.Decimal`**, in `decimal.go`: the unscaled value on 256 bits, two's complement,
   32 bytes, no pointer. `DecimalFromInt64`, `DecimalFromBig` (error above 256 bits),
   `Int64() (int64, bool)`, `Big() *big.Int`, `Text(scale int) string` (sign, digits, a
   point at `scale`, no exponent), `Cmp`. No allocation except `Big` and `Text`.
2. **Accessors**: `Column.Bytes(i)`, `Column.Decimal(i)`, `Column.Elems()`,
   `Column.List(i) (start, end int)`. They panic on a column of another type. `Bytes`
   returns a view that is valid until the next batch and never points into the file.
3. **Decoders**, registered like the earlier ones:
   - `vortex.varbin` (`varbin/vtable/`): metadata `{offsets_ptype}`, child 0 the offsets
     (rows + 1, any integer type), child 1 the validity, buffer 0 the bytes;
   - `vortex.varbinview` (`varbinview/vtable/`, the view layout at
     `varbinview/view/mod.rs:31-88`): the data buffers come first and the views buffer
     last, 16 bytes per view. A view of 12 bytes or fewer holds its bytes inline, a longer
     one holds a prefix, a buffer index and an offset;
   - `vortex.list` (`list/vtable/`): metadata `{elements_len, offset_ptype}`, children:
     elements, offsets (rows + 1), validity;
   - `vortex.listview` (`listview/vtable/`): metadata `{elements_len, offset_ptype,
     size_ptype}`, children: elements, offsets, sizes, validity. Views may overlap and be
     out of order, so `List(i)` is `(offset, offset+size)`;
   - `vortex.fixed_size_list` (`fixed_size_list/vtable/`): child 0 the elements (rows times
     size), child 1 the validity. `List(i)` is `(i*size, (i+1)*size)`;
   - `vortex.map` (`map/vtable/`): one child, a `vortex.listview` of structs `{key,
     value}`. `Elems()` is that struct;
   - `vortex.decimal` (`decimal/vtable/`): metadata `{values_type}` with I8=0, I16, I32,
     I64, I128, I256=5 (`dtype/decimal/types.rs:28-41`). The storage width is independent
     of the precision: the fixtures `decimal_narrow_storage`, `decimal_wide_storage` and
     `decimal_gap_values` exist for that. `Values[int64]` works when the precision is 18 or
     less, whatever the storage width, and `Decimal(i)` always works;
   - `vortex.constant` gains string, binary, decimal and list scalars;
   - UUID: an extension over a fixed-size list of 16 `u8`. `Bytes(i)` gives the 16 bytes.
4. Every offset, size, view and buffer index is checked against its buffer before use.
   Offsets must not decrease. A view that points outside its buffer is `ErrCorrupt`.
5. The canonical hash of `internal/testutil` gains these types. Entries leave `pending`.
6. Update `api.txt`.

## Tests

- The harness: `varbin`, `varbinview`, `list`, `listview`, `fixed_size_list`, `map`,
  `struct_nested`, `constant`, the 7 `decimal*` fixtures that only need `vortex.decimal`,
  and `extra/uuid` pass.
- `Decimal`: round trip with `big.Int` for 0, 1, -1, the bounds of 64, 128 and 256 bits,
  one past 256 bits (error), `Text` with scale 0, 2, larger than the digit count, and a
  negative value. `Int64` reports overflow. `testing.AllocsPerRun` is 0 for the accessors.
- Hand-built arrays for the error paths: decreasing offsets, an offset past the bytes, a
  view with a bad buffer index, a list view past its elements, a decimal buffer too short,
  an unknown `values_type`, a map whose child is not a list view.
- A list of lists of strings, and a nullable struct in a nullable list: `List`, `Elems`,
  `Field` and `Null` agree with the oracle's first and last values.
- Batch sizes 1, 7 and 1024 on `varbinview` and `listview`.

## To verify

- The view layout: field order, and whether the prefix is 4 bytes.
- How a decimal narrower than its precision's canonical width is sign-extended, and what
  the null slots hold (`decimal_null_slots` has non-zero values in null slots: they must
  not reach the hash).
- Whether `keys_sorted` of the map type changes anything for a reader.
