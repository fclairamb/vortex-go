# Fixtures the reference writer could not produce

Written by `tools/oracle/genfixtures.py` for vortex-data 0.94.0. Nothing is faked: each entry below has no file.

## parquet_variant.vortex

- Identifier: `vortex.parquet.variant`
- Tried: Same as variant: nothing in the Python API builds or converts to a parquet-variant array, and no data shape written with default or compact options produced it.

## variant.vortex

- Identifier: `vortex.variant`
- Tried: vortex-data 0.93.0 has no variant dtype in its Python API (vx.DType has no variant class, vx.arrays no variant array), and pyarrow has no variant extension type (vx.array(pa.table(...)) only maps the Arrow types it knows). vx.io.write and VortexWriteOptions (default, compact, with_edition('core2026.08.3')) only choose encodings for the types that exist.
