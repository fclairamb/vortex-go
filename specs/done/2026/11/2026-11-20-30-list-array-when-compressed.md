---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Write `vortex.list` for compressed lists: offsets only, no sizes

## Problem

`vortex.list` is read and never written. The writer always makes a `vortex.listview`:
the elements, a `u32` offset and a `u32` size per row (`wencode.go:291-313`).

The reference compressor turns a list view into a `vortex.list` when the views are in
order and touch each other, or when there is no element
(`~/code/other/vortex/vortex-compressor/src/compressor/cascade.rs:177-183`). It then
narrows the offsets and compresses them (`structural.rs:128-160`). A `vortex.list` has
rows + 1 offsets and no sizes child.

Our `OfList` offsets always qualify: one more value than there are rows, never
decreasing (`api.txt:186-190`).

## Proposal

1. `wencode.go`, `encodeList`: when the chunk is compressed (`w.comp != nil`) and the
   type is a `List`, write `vortex.list`: metadata `{elements_len, offset_ptype}`,
   children the elements, the offsets (rows + 1, rebased so that the first is 0, in the
   narrowest unsigned type that holds the last) and the validity. The shape
   `internal/array/list.go:16-17` decodes.
2. The offsets go through the compressor like the offsets of a list view today
   (`encodeU32s`, `wcompress.go:122-141`), in their narrow type.
3. With `Compression: None`, nothing changes: a `vortex.listview`.
4. A map keeps its `vortex.listview` of entries, unless the reference does otherwise (see
   To verify).
5. Register `vortex.list` in `writtenArrays` (`wstats.go:456`). `go generate`.

## Tests

- Round trip of `list<i32>`, `list<utf8>`, `list<list<i64>>`, nullable lists, empty
  lists, a chunk with no element at all, in `Default` and `Compact`: the file has
  `vortex.list` and no `vortex.listview`.
- Negative: `Compression: None` still writes `vortex.listview`.
- A second chunk of the same column has offsets that start at 0.
- Offsets width: 200 elements give `u8` offsets, 70,000 give `u32`.
- `make interop`: the list types of `allTypes()` in `Default` mode, read by the reference.
- `TestWrittenMatchesFiles` (spec 28) meets `vortex.list`.
- The file of a `list<i32>` column of 100,000 rows is not larger than before.

## To verify

- What `compress_map_array` does with the entries of a map
  (`~/code/other/vortex/vortex-compressor/src/compressor/structural.rs:200-230`): if it
  ends as a `vortex.list` too, do the same.
- That `vortex.fixed_size_list` is not concerned (`cascade.rs:186-225`).
