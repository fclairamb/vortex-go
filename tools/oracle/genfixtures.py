#!/usr/bin/env python3
"""Writes, with the reference writer, the fixtures upstream does not publish, into
testdata/fixtures/<version>/extra/. Small data, fixed seed.

    uv run --with vortex-data==$(cat .vortex-version) --with pyarrow python tools/oracle/genfixtures.py

A fixture that already exists is kept (a rewrite could change its bytes). Each fixture
names the encoding it exists for: when the reference writer does not emit it, the file is
not written and MISSING.md lists it, with what was tried.
"""
import os
import random
import re
import sys
import uuid
import datetime
import decimal

import pyarrow as pa
import vortex as vx

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
ENCODING_LINE = re.compile(r"^\s*[\w\[\].]+: ([\w.]+)\(.*len=\d+")


def rng():
    return random.Random(20261120)


def onpair(r):
    n = 3000
    return pa.table({
        "nullable_text": [("user_%d@acme.com" % r.randrange(500)) if r.random() < 0.7 else None
                          for _ in range(n)],
        "text": ["item-%d-acme" % r.randrange(800) for _ in range(n)],
    })


def masked(r):
    n = 3000
    return pa.table({
        "sparse_f64": [r.random() if r.random() < 0.1 else None for _ in range(n)],
    })


def uuid_table(r):
    ids = [uuid.UUID(int=r.getrandbits(128), version=4).bytes for _ in range(20)]
    return pa.table({
        "id": pa.array(ids, pa.uuid()),
        "nullable_id": pa.array([b if i % 3 else None for i, b in enumerate(ids)], pa.uuid()),
    })


def empty(r):
    return pa.table({
        "id": pa.array([], pa.int64()),
        "name": pa.array([], pa.string()),
        "score": pa.array([], pa.float64()),
    })


def wide(r):
    n = 100
    cols = {}
    for i in range(200):
        kind = i % 4
        if kind == 0:
            cols["c%03d_i64" % i] = [r.randrange(-10**9, 10**9) for _ in range(n)]
        elif kind == 1:
            cols["c%03d_f64" % i] = [r.random() for _ in range(n)]
        elif kind == 2:
            cols["c%03d_str" % i] = ["v%d" % r.randrange(50) for _ in range(n)]
        else:
            cols["c%03d_nullable" % i] = [r.randrange(1000) if r.random() < 0.8 else None
                                          for _ in range(n)]
    return pa.table(cols)


def default_ints(r):
    n = 30000
    return pa.table({
        "sorted": list(range(1000, 1000 + n)),
        "low_card": [r.choice([3, 5, 8, 13, 21, 34, 55, 89]) * 1000 for _ in range(n)],
        "random": [r.randrange(-512, 512) for _ in range(n)],
        "nullable": [r.randrange(300) if r.random() < 0.8 else None for _ in range(n)],
        # Constant lists: 4.8 MB of values that compress to nothing. The writer chunks a
        # column by its uncompressed size, so this is what makes a vortex.chunked layout.
        "bulk": pa.array([[7] * 40 for _ in range(n)], pa.list_(pa.int32())),
    })


def default_mixed(r):
    n = 30000
    base = datetime.datetime(2026, 1, 1)
    cols = {
        "sorted": list(range(1000, 1000 + n)),
        "low_card": [r.choice([3, 5, 8, 13, 21, 34, 55, 89]) * 1000 for _ in range(n)],
        "random": [r.randrange(-8, 8) for _ in range(n)],
        "nullable": [r.randrange(8) if r.random() < 0.8 else None for _ in range(n)],
        "float": [r.randrange(16) / 4 for _ in range(n)],
        "str_low": [r.choice(["red", "green", "blue", "amber"]) for _ in range(n)],
        "str_high": [("row-%02d-acme-" % r.randrange(16)).ljust(80, "x") for _ in range(n)],
        "flag": [r.random() < 0.3 for _ in range(n)],
        "price": pa.array([decimal.Decimal(r.randrange(16)) / 100 for _ in range(n)],
                          pa.decimal128(12, 2)),
        "ts": pa.array([base + datetime.timedelta(seconds=7 * i) for i in range(n)],
                       pa.timestamp("us")),
        "rec": pa.array(
            [{"a": r.randrange(4), "b": "s%d" % r.randrange(4)} if r.random() < 0.9 else None
             for _ in range(n)],
            pa.struct([("a", pa.int32()), ("b", pa.string())])),
        "tags": pa.array([[7] * 40 for _ in range(n)],
                         pa.list_(pa.int32())),
    }
    return pa.table(cols)


def compact(path, array):
    vx.io.VortexWriteOptions.compact().write(array, path)


# name, builder, encoding it must contain (None: no requirement), writer (None: default)
FIXTURES = [
    ("onpair.vortex", onpair, "vortex.onpair"),
    ("masked.vortex", masked, "vortex.masked"),
    ("uuid.vortex", uuid_table, None),
    ("empty.vortex", empty, None),
    ("wide.vortex", wide, None),
    ("default_ints.vortex", default_ints, "vortex.dict"),
    ("default_mixed.vortex", default_mixed, "vortex.dict"),
    ("default_compact.vortex", default_mixed, None, compact),
]

# Not producible from Python. What was tried is the record the completeness spec reads.
UNREACHABLE = [
    ("variant.vortex", "vortex.variant",
     "vortex-data 0.93.0 has no variant dtype in its Python API (vx.DType has no variant "
     "class, vx.arrays no variant array), and pyarrow has no variant extension type "
     "(vx.array(pa.table(...)) only maps the Arrow types it knows). vx.io.write and "
     "VortexWriteOptions (default, compact, with_edition('core2026.08.3')) only choose "
     "encodings for the types that exist."),
    ("parquet_variant.vortex", "vortex.parquet.variant",
     "Same as variant: nothing in the Python API builds or converts to a parquet-variant "
     "array, and no data shape written with default or compact options produced it."),
]


def encodings(path):
    ids = set()
    for line in vx.open(path).scan().read_all().display_tree().splitlines():
        m = ENCODING_LINE.match(line)
        if m:
            ids.add(m.group(1))
    return ids


def main():
    with open(os.path.join(ROOT, ".vortex-version"), encoding="utf-8") as fh:
        version = fh.read().strip()
    out = os.path.join(ROOT, "testdata", "fixtures", version, "extra")
    os.makedirs(out, exist_ok=True)
    missing = []
    for name, build, want, *writer in FIXTURES:
        path = os.path.join(out, name)
        if not os.path.exists(path):
            if writer:
                writer[0](path, vx.array(build(rng())))
            else:
                vx.io.VortexWriteOptions.default().write(vx.array(build(rng())), path)
        if want and want not in encodings(path):
            os.remove(path)
            missing.append((name, want, "the default writer did not emit it for the data of "
                            "genfixtures.py (%s)" % build.__name__))
    missing += UNREACHABLE
    lines = ["# Fixtures the reference writer could not produce", "",
             "Written by `tools/oracle/genfixtures.py` for vortex-data %s. Nothing is faked: "
             "each entry below has no file." % version, ""]
    for name, ident, why in sorted(missing):
        lines += ["## %s" % name, "", "- Identifier: `%s`" % ident, "- Tried: %s" % why, ""]
    path = os.path.join(out, "MISSING.md")
    with open(path, "w", encoding="utf-8", newline="\n") as fh:
        fh.write("\n".join(lines))


if __name__ == "__main__":
    sys.exit(main())
