#!/usr/bin/env python3
"""Writes testdata/fixtures/compat/0.60.0/stats.vortex: a file with the vortex.stats layout,
which the writer no longer emits (it writes vortex.zoned). No release of the compat bucket
(v0.64.0 and later) has one, so the file comes from vortex-data 0.60.0, then gets its
expected values from the pinned reader:

    uv run --python 3.12 --with vortex-data==0.60.0 --with pyarrow --with typing_extensions \
        --with 'substrait<0.24' python tools/oracle/genstats.py
    uv run --with vortex-data==$(cat .vortex-version) --with pyarrow python \
        tools/oracle/oracle.py expected testdata/fixtures/compat/0.60.0/stats.vortex
"""
import pyarrow as pa
import vortex as vx

t = pa.table(
    {
        "a": pa.array([i % 50 for i in range(2000)], pa.int64()),
        "s": pa.array([str(i % 7) for i in range(2000)]),
    }
)
vx.io.write(vx.array(t), "testdata/fixtures/compat/0.60.0/stats.vortex")
