#!/usr/bin/env python3
"""Writes testdata/vectors/pco/<case>.json: the wrapped-format bytes the pco crate (1.1.0)
writes for a chunk, with the numbers they decode to.

    uv run --with pcodec==1.1.0 --with numpy python tools/oracle/pcovectors.py

One file per case. Each holds the type, the header, the chunk metadata, the pages (their
number of values and bytes) and the values as little-endian bytes, all in hex. The chunk is
compressed by the reference through its Python binding; the mode and the delta encoding are
requested with the binding's specs, which the reference may decline, so the file also records
the mode and the delta encoding it actually wrote (read from the first bits of the metadata).
The Go test checks that every mode and every delta encoding is reached by at least one vector.

The data comes from a generator seeded per case: the output is deterministic.

There is no 0-value case: the reference refuses to compress an empty chunk. A Vortex pco
array with no valid value has no chunk at all; internal/array/pco_test.go covers it.
"""
import json
import os
import sys

import numpy as np
from pcodec import ChunkConfig, DeltaSpec, ModeSpec, PagingSpec
from pcodec.wrapped import FileCompressor

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "testdata", "vectors", "pco")

MODES = {0: "classic", 1: "int_mult", 2: "float_mult", 3: "float_quant", 4: "dict"}
DELTAS = {0: "none", 1: "consecutive", 2: "lookback", 3: "conv1"}
DTYPES = {
    "u8": np.uint8, "u16": np.uint16, "u32": np.uint32, "u64": np.uint64,
    "i8": np.int8, "i16": np.int16, "i32": np.int32, "i64": np.int64,
    "f16": np.float16, "f32": np.float32, "f64": np.float64,
}


class Bits:
    """Reads the bits of the chunk metadata, least significant first."""

    def __init__(self, b):
        self.v = int.from_bytes(b, "little")
        self.pos = 0

    def read(self, n):
        out = (self.v >> self.pos) & ((1 << n) - 1)
        self.pos += n
        return out


def mode_and_delta(meta, nbits):
    r = Bits(meta)
    mode = r.read(4)
    if mode in (1, 2):
        r.read(nbits)
    elif mode == 3:
        r.read(8)
    elif mode == 4:
        n = r.read(25)
        r.pos = (r.pos + 7) // 8 * 8 + n * nbits
    return MODES[mode], DELTAS[r.read(4)]


def rng(name):
    return np.random.default_rng(sum(name.encode()) * 7919)


def ints(name, dt, n, spread):
    g = rng(name)
    info = np.iinfo(dt)
    lo, hi = max(info.min, -spread), min(info.max, spread)
    if info.min == 0:
        lo, hi = 0, min(info.max, spread)
    return g.integers(lo, hi, n, dtype=np.int64).astype(dt)


def floats(name, dt, n):
    g = rng(name)
    return (g.normal(0, 1000, n)).astype(dt)


def case(name, typ, values, config, paging=None):
    dt = DTYPES[typ]
    arr = np.ascontiguousarray(values, dtype=dt)
    fc = FileCompressor()
    cc = fc.chunk_compressor(arr, config)
    meta = bytes(cc.write_meta())
    ns = cc.n_per_page()
    pages = [{"n": int(n), "data": bytes(cc.write_page(i)).hex()} for i, n in enumerate(ns)]
    mode, delta = mode_and_delta(meta, arr.dtype.itemsize * 8)
    doc = {
        "name": name, "type": typ, "mode": mode, "delta": delta,
        "header": bytes(fc.write_header()).hex(), "meta": meta.hex(), "pages": pages,
        "values": arr.astype(arr.dtype.newbyteorder("<")).tobytes().hex(),
    }
    with open(os.path.join(OUT, name + ".json"), "w") as f:
        json.dump(doc, f, indent=1, sort_keys=True)
        f.write("\n")
    print("%-28s %s/%s, %d pages" % (name, mode, delta, len(pages)), file=sys.stderr)


def cfg(mode=None, delta=None, paging=None, level=8):
    return ChunkConfig(
        compression_level=level,
        mode_spec=mode or ModeSpec.classic(),
        delta_spec=delta or DeltaSpec.no_op(),
        paging_spec=paging or PagingSpec.equal_pages_up_to(1 << 18),
        enable_8_bit=True,
    )


def main():
    os.makedirs(OUT, exist_ok=True)
    for f in os.listdir(OUT):
        os.remove(os.path.join(OUT, f))
    # Classic mode, every type.
    for typ in DTYPES:
        dt = DTYPES[typ]
        vals = floats("classic_" + typ, dt, 700) if typ[0] == "f" else ints("classic_" + typ, dt, 700, 1 << 40)
        case("classic_" + typ, typ, vals, cfg())
    # Constant data, one value, and a value count around the batch size.
    case("constant_u32", "u32", np.full(300, 42), cfg())
    case("constant_f64", "f64", np.full(300, 1.5), cfg())
    case("one_u64", "u64", [123456789012345], cfg())
    case("one_f32", "f32", [-0.5], cfg())
    for n in (255, 256, 257, 258, 263):
        case("sizes_%d" % n, "i32", ints("sizes_%d" % n, np.int32, n, 1 << 20), cfg())
    # Integer multiples.
    # (base, smallest and largest multiplier) per type.
    mults = {
        "u8": (16, 0, 15), "i8": (16, -7, 7), "u16": (500, 0, 120), "i16": (100, -300, 300),
        "u32": (1000, 0, 5000), "i32": (1000, -5000, 5000), "u64": (1000, 0, 5000), "i64": (1000, -5000, 5000),
    }
    for typ, (base, lo, hi) in mults.items():
        g = rng("mult_" + typ)
        k = g.integers(lo, hi, 600, dtype=np.int64)
        vals = (k * base + g.integers(0, 3, 600)).astype(DTYPES[typ])
        case("int_mult_" + typ, typ, vals, cfg(mode=ModeSpec.try_int_mult(base)))
    # Float multiples and quantization.
    for typ in ("f16", "f32", "f64"):
        dt = DTYPES[typ]
        g = rng("fmult_" + typ)
        k = g.integers(-3000, 3000, 600)
        case("float_mult_" + typ, typ, (k * 0.1).astype(dt), cfg(mode=ModeSpec.try_float_mult(0.1)))
        wide = {"f16": 3, "f32": 8, "f64": 20}[typ]
        base = floats("fquant_" + typ, np.float64, 600).astype(dt)
        utype = {"f16": np.uint16, "f32": np.uint32, "f64": np.uint64}[typ]
        bits = base.view(utype) & utype(~((1 << wide) - 1) & ((1 << base.itemsize * 8) - 1))
        case("float_quant_" + typ, typ, bits.view(dt), cfg(mode=ModeSpec.try_float_quant(wide)))
    # Dictionary mode.
    for typ in ("u16", "u32", "u64", "i32", "f32", "f64", "u8"):
        dt = DTYPES[typ]
        g = rng("dict_" + typ)
        pool = g.permutation(np.arange(40) * 1009 + 5)
        vals = pool[g.integers(0, 40, 3000)].astype(dt)
        case("dict_" + typ, typ, vals, cfg(mode=ModeSpec.try_dict()))
    # Consecutive deltas, every order.
    for order in range(1, 8):
        for typ in ("i32", "u64", "f32"):
            dt = DTYPES[typ]
            g = rng("consec_%d_%s" % (order, typ))
            x = g.integers(-3, 4, 1000).astype(np.int64)
            for _ in range(order):
                x = np.cumsum(x)
            vals = (x % (1 << 30)).astype(dt) if typ != "f32" else (x % (1 << 20)).astype(dt) / 8
            case("consecutive_%d_%s" % (order, typ), typ, vals, cfg(delta=DeltaSpec.try_consecutive(order)))
    # Consecutive deltas in a mode with a secondary latent, and around the batch size.
    g = rng("consec_secondary")
    case("consecutive_int_mult", "i64", (np.cumsum(g.integers(0, 50, 800)) * 12 + g.integers(0, 2, 800)), cfg(mode=ModeSpec.try_int_mult(12), delta=DeltaSpec.try_consecutive(2)))
    for n in (256, 257, 258, 262, 263, 520):
        g = rng("consec_edge_%d" % n)
        x = np.cumsum(np.cumsum(g.integers(-5, 6, n)))
        case("consecutive_edge_%d" % n, "i64", x, cfg(delta=DeltaSpec.try_consecutive(7)))
    # Lookback deltas.
    for typ in ("u16", "u32", "i64", "f64"):
        dt = DTYPES[typ]
        g = rng("look_" + typ)
        pattern = g.integers(0, 1 << 14, 37)
        vals = (pattern[np.arange(1500) % 37] + g.integers(0, 3, 1500)).astype(dt)
        case("lookback_" + typ, typ, vals, cfg(delta=DeltaSpec.try_lookback()))
    g = rng("look_secondary")
    pattern = g.integers(0, 1 << 14, 25) * 8
    case("lookback_int_mult", "u32", (pattern[np.arange(1200) % 25] + g.integers(0, 2, 1200)).astype(np.uint32), cfg(mode=ModeSpec.try_int_mult(8), delta=DeltaSpec.try_lookback()))
    # Convolutions.
    for typ in ("u8", "u16", "u32", "i16", "i32", "f32"):
        dt = DTYPES[typ]
        for order in (1, 2, 6, 9):
            g = rng("conv_%s_%d" % (typ, order))
            t = np.arange(1200)
            wave = 3000 * np.sin(t / 17.0) + 1200 * np.sin(t / 5.0) + g.normal(0, 3, 1200)
            if typ in ("u8",):
                vals = (100 + 0.03 * wave).astype(dt)
            elif typ.startswith("f"):
                vals = (wave / 7).astype(dt)
            else:
                vals = (20000 + wave).astype(dt)
            case("conv1_%s_%d" % (typ, order), typ, vals, cfg(delta=DeltaSpec.try_conv1(order)))
    # Pages.
    g = rng("pages")
    vals = np.cumsum(g.integers(0, 9, 1000)).astype(np.uint32)
    case("pages_exact", "u32", vals, cfg(paging=PagingSpec.exact_page_sizes([1, 255, 256, 300, 188])))
    case("pages_exact_consecutive", "u32", vals, cfg(delta=DeltaSpec.try_consecutive(2), paging=PagingSpec.exact_page_sizes([1, 255, 256, 300, 188])))
    case("pages_exact_lookback", "u32", vals % 50, cfg(delta=DeltaSpec.try_lookback(), paging=PagingSpec.exact_page_sizes([1, 255, 256, 300, 188])))
    case("pages_equal", "u32", vals, cfg(paging=PagingSpec.equal_pages_up_to(300)))
    # More than one page, past the batch size, with the default level 0 and 12.
    case("level_0", "i32", ints("level_0", np.int32, 5000, 1 << 25), cfg(level=0))
    case("level_12", "i32", ints("level_12", np.int32, 5000, 1 << 25), cfg(level=12))


main()
