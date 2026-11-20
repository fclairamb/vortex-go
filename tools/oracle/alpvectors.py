#!/usr/bin/env python3
"""Writes testdata/vectors/alp.json: the float a classic ALP decode gives for a few integers,
for every pair of exponents (e, f) the encoder of the alp crate (0.0.6) can pick.

    python3 tools/oracle/alpvectors.py > testdata/vectors/alp.json

The decode of the crate is `from_int(encoded) * F10[f] * IF10[e]`, left to right, each product
rounded to the float type. The tables are the literals of the crate (10^i and 10^-i rounded to
the nearest float); IEEE arithmetic is what Python floats and a binary32 round trip give, so the
bits do not depend on this script's platform. The output is deterministic.
"""
import json
import struct
import sys

F64 = {"max": 18, "ints": [0, 1, -1, 7, 12345, -987654321, 2**31 - 1, -(2**31), 2**53 + 1, 2**62, -(2**63)]}
F32 = {"max": 10, "ints": [0, 1, -1, 7, 12345, -987654321, 2**31 - 1, -(2**31)]}


def f32(x):
    """x rounded to the nearest binary32 (ties to even), as a Python float."""
    return struct.unpack("<f", struct.pack("<f", x))[0]


def bits64(x):
    return "%016x" % struct.unpack("<Q", struct.pack("<d", x))[0]


def bits32(x):
    return "%08x" % struct.unpack("<I", struct.pack("<f", x))[0]


def vectors(spec, rnd, bits):
    f10 = [rnd(float("1e%d" % i)) for i in range(spec["max"] + 1)]
    if10 = [rnd(float("1e-%d" % i)) for i in range(spec["max"] + 1)]
    out = []
    for e in range(spec["max"] + 1):
        for f in range(e + 1):
            res = []
            for i in spec["ints"]:
                res.append(bits(rnd(rnd(rnd(float(i)) * f10[f]) * if10[e])))
            out.append({"e": e, "f": f, "in": spec["ints"], "out": res})
    return out


doc = {
    "f64": vectors(F64, lambda x: x, bits64),
    "f32": vectors(F32, f32, bits32),
}
json.dump(doc, sys.stdout, sort_keys=True, indent=1)
print()
