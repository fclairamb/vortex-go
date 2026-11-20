"""Times the reference reader (vortex-data, through Python) on the generated file of the
benchmarks, for the ratio published in docs/benchmarks.md: open, full scan, 3 columns out of 16
and a filter that keeps 1%. Run by `make bench-rust`. The numbers are for information. Each case decodes to an Arrow table: a scan
without that step only builds the compressed arrays.

usage: bench.py [file]    (default: the newest .cache/bench/gen16-*.vortex)
"""

import glob
import os
import sys
import time

import vortex as vx
import vortex.expr as ve

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
RUNS = 5


def best(fn):
    """The fastest of RUNS runs, in seconds."""
    times = []
    for _ in range(RUNS):
        t = time.perf_counter()
        fn()
        times.append(time.perf_counter() - t)
    return min(times)


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else sorted(glob.glob(os.path.join(ROOT, ".cache/bench/gen16-*.vortex")))[-1]
    f = vx.open(path)
    rows = len(f)
    cases = {
        "open": (lambda: vx.open(path), 1),
        "scan_full": (lambda: f.scan().read_all().to_arrow_table(), rows),
        "scan_3of16": (lambda: f.scan(["id", "price", "city"]).read_all().to_arrow_table(), rows),
        "scan_filter": (lambda: f.scan(["id", "user", "price"], expr=ve.column("user") < ve.literal(vx.int_(32), 1000)).read_all().to_arrow_table(), rows),
    }
    for name, (fn, n) in cases.items():
        s = best(fn)
        print(f"{name} {s * 1e9:.0f} ns/op {n / s:.0f} rows/s")


if __name__ == "__main__":
    main()
