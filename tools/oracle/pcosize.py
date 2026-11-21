#!/usr/bin/env python3
"""Writes testdata/pco-size.json: the bytes the pco crate (1.1.0) takes for the sorted key
column that internal/compress/pco_test.go compresses, with the settings of Vortex (level 8,
pages of 8192 values, mode and delta chosen by the crate).

    uv run --with pcodec==1.1.0 --with numpy python tools/oracle/pcosize.py

The data is the one of pcoKeys in the Go test: key(i) = 1000000 + 3*i + i*i % 3. The file also
holds our size ("ours"), which the Go test keeps at most 1.2 times the reference's and cannot
let grow: it is rewritten by PCO_SIZE_UPDATE=1 go test ./internal/compress.
"""
import json
import os

import numpy as np
from pcodec import ChunkConfig, PagingSpec
from pcodec.wrapped import FileCompressor

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "testdata", "pco-size.json")
ROWS = 100_000


def size(arr):
    cfg = ChunkConfig(compression_level=8, paging_spec=PagingSpec.equal_pages_up_to(8192))
    fc = FileCompressor()
    cc = fc.chunk_compressor(arr, cfg)
    return len(bytes(cc.write_meta())) + sum(len(bytes(cc.write_page(i))) for i in range(len(cc.n_per_page())))


def main():
    i = np.arange(ROWS, dtype=np.int64)
    keys = 1_000_000 + 3 * i + i * i % 3
    old = {}
    if os.path.exists(OUT):
        with open(OUT) as f:
            old = json.load(f)
    old.update({"rows": ROWS, "reference": size(keys)})
    with open(OUT, "w") as f:
        json.dump(old, f, indent=1, sort_keys=True)
        f.write("\n")
    print(old)


main()
