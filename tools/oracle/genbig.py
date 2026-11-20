"""Writes .cache/big.vortex with the reference writer: 2 million rows, for the test of the
peak memory of a scan (TestScanPeakHeap), which is skipped when the file is absent. The file
is not committed: `make bigfile` writes it again.
"""

import os
import random
import sys

import pyarrow as pa
import vortex as vx

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
ROWS = 2_000_000


def main():
    r = random.Random(20261120)
    table = pa.table({
        "id": pa.array(range(ROWS), pa.int64()),
        "score": pa.array([r.random() for _ in range(ROWS)], pa.float64()),
        "small": pa.array([r.randrange(100) if r.random() < 0.9 else None for _ in range(ROWS)], pa.int32()),
        "city": pa.array([r.choice(["paris", "lyon", "nantes", "lille"]) for _ in range(ROWS)]),
        "note": pa.array(["row %d of %d" % (i, r.randrange(1 << 20)) for i in range(ROWS)]),
    })
    out = os.path.join(ROOT, ".cache")
    os.makedirs(out, exist_ok=True)
    vx.io.VortexWriteOptions.default().write(vx.array(table), os.path.join(out, "big.vortex"))


if __name__ == "__main__":
    sys.exit(main())
