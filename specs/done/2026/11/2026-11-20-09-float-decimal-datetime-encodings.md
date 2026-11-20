---
model: sonnet
effort: high
pr: batch
automerge: false
---

# The float, decimal and timestamp encodings are decoded: ALP, ALP-RD, decimal byte parts, datetime parts

## Problem

Floats, decimals and timestamps written by the reference writer go through four encodings
of the edition that are not decoded yet: `vortex.alp`, `vortex.alprd`,
`vortex.decimal_byte_parts` and `vortex.datetimeparts`. Each one turns its values into
integer children that the previous spec already decodes.

Upstream paths are in `~/code/other/vortex/encodings/`.

## Proposal

1. `vortex.alp` (`alp/src/alp/`, decode in `alp/decompress.rs`): metadata `{exp_e, exp_f,
   patches?}` (`alp/array/mod.rs:174-182`). Child 0 is the encoded integers (`i32` for
   `f32`, `i64` for `f64`), then the patch children (indices, values, optional chunk
   offsets). Value = `encoded * 10^exp_f * 10^-exp_e`, computed exactly as upstream does
   it (two table lookups and two multiplications, in that order: the result must be
   bit-identical). Patches hold the floats that did not round-trip, NaN and infinities
   included.
2. `vortex.alprd` (`alp/src/alp_rd/`, `alp_rd_decode` at `alp_rd/mod.rs:155`): metadata
   `{right_bit_width, dict_len, dict: [uint32], left_parts_ptype, patches}`
   (`alp_rd/array/mod.rs:60-72`). Children: left parts (dictionary codes), right parts
   (`u32` or `u64`), then the patches of the left parts. Value bits = `dict[left] <<
   right_bit_width | right`. For `f32` and `f64`.
3. `vortex.decimal_byte_parts` (`decimal-byte-parts/src/decimal_byte_parts/plugin/v1.rs`):
   metadata `{zeroth_child_ptype, lower_part_count}`. Child 0 is the most significant
   part, a signed integer array. With no lower part, the decimal is that integer
   sign-extended to the decimal's width. The fixture `decimal_byte_parts_narrow_msp` has a
   most significant part narrower or wider than the canonical width.
4. `vortex.datetimeparts` (`datetime-parts/src/`, decode in `canonical.rs`): metadata
   `{days_ptype, seconds_ptype, subseconds_ptype}`, children days (it carries the nulls),
   seconds and subseconds. The timestamp in its unit = `days * 86400 * unit + seconds *
   unit + subseconds`, with the unit of the column's type.
5. NaN payloads and `-0` survive: the canonical hash takes the raw bits
   (`docs/testing.md` section 1).
6. Entries leave `pending`: the float and decimal columns of `default_mixed` too.

## Tests

- The harness: `alp`, `alprd`, `decimal_byte_parts`, `decimal_byte_parts_narrow_msp` and
  `datetimeparts` pass.
- ALP, bit-exact, without a file: for every exponent pair that upstream can pick, decode
  a few integers and compare with constants produced by the reference (a short Python
  script in `tools/oracle`, its output committed in `testdata/vectors/alp.json`).
- ALP patches: NaN with a payload, `+Inf`, `-Inf`, `-0`, a subnormal.
- ALP-RD: seeded doubles split by a naive encoder in the test, decoded, compared bit for
  bit. With patches on the left parts.
- Datetime parts: each unit (s, ms, us, ns), a negative timestamp (before 1970), a null.
- Error paths with hand-built arrays: exponent out of the table, `right_bit_width` above
  the type width, a left code past the dictionary, `lower_part_count` above zero with no
  such child, a parts type that is not an integer.

## To verify

- The exact float arithmetic of the ALP decode in the `alp` crate (0.0.6, `Cargo.toml:103`
  upstream) and in `encodings/alp/src/alp/mod.rs`: which powers-of-ten tables, and whether
  the product is `(encoded as f64) * F10[exp_f] * IF10[exp_e]`. A different order gives a
  different last bit.
- Whether `vortex.decimal_byte_parts` with `lower_part_count` above zero can be written by
  0.93.0 under its v1 identifier. If no fixture has it, return an "unsupported" error for
  it, tested with a hand-built array.
- How datetime parts handles a timestamp type with a time zone: the zone is in the type,
  not in the parts.
