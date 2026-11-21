#!/usr/bin/env python3
"""The oracle: reads Vortex files with the reference reader (vortex-data) and writes the
expected values the Go tests compare against.

    uv run --with vortex-data==$(cat .vortex-version) --with pyarrow python tools/oracle/oracle.py expected FILE...
    uv run ... python tools/oracle/oracle.py check DIR

The canonical hash is defined in docs/testing.md, section 1. The output has no timestamp
and sorted keys, so running it twice gives byte-identical files.
"""
import hashlib
import json
import operator
import re
import struct
import sys

import pyarrow as pa
import vortex as vx

PTYPES = {"u8", "u16", "u32", "u64", "i8", "i16", "i32", "i64", "f16", "f32", "f64"}


# ---------------------------------------------------------------------------------------
# DType tree. The Python API hides map, extension and list element types, so the tree is
# parsed from the reference's own text form of the DType.
# ---------------------------------------------------------------------------------------


class _Parser:
    def __init__(self, text):
        self.s = text
        self.i = 0

    def peek(self):
        return self.s[self.i] if self.i < len(self.s) else ""

    def expect(self, lit):
        if not self.s.startswith(lit, self.i):
            raise ValueError("dtype %r: expected %r at %d" % (self.s, lit, self.i))
        self.i += len(lit)

    def ident(self):
        m = re.compile(r"[\w.]+").match(self.s, self.i)
        if not m:
            raise ValueError("dtype %r: identifier expected at %d" % (self.s, self.i))
        self.i = m.end()
        return m.group(0)

    def number(self):
        m = re.compile(r"-?\d+").match(self.s, self.i)
        if not m:
            raise ValueError("dtype %r: number expected at %d" % (self.s, self.i))
        self.i = m.end()
        return int(m.group(0))

    def dtype(self):
        if self.peek() == "{":
            self.i += 1
            fields = []
            while self.peek() != "}":
                end = self.s.index("=", self.i)
                name = self.s[self.i:end]
                self.i = end + 1
                fields.append({"name": name, "dtype": self.dtype()})
                if self.peek() == ",":
                    self.i += 2
            self.expect("}")
            node = {"kind": "struct", "fields": fields}
        else:
            node = self.named()
        if self.peek() == "?":
            self.i += 1
            node["nullable"] = True
        node.setdefault("nullable", node["kind"] == "null")
        return node

    def named(self):
        tok = self.ident()
        if tok in PTYPES:
            return {"kind": "primitive", "ptype": tok}
        if tok in ("null", "bool", "utf8", "binary"):
            return {"kind": tok}
        if tok == "decimal":
            self.expect("(")
            p = self.number()
            self.expect(",")
            sc = self.number()
            self.expect(")")
            return {"kind": "decimal", "precision": p, "scale": sc}
        if tok == "list":
            self.expect("(")
            el = self.dtype()
            self.expect(")")
            return {"kind": "list", "element": el}
        if tok == "fixed_size_list":
            self.expect("(")
            el = self.dtype()
            self.expect(")")
            self.expect("[")
            size = self.number()
            self.expect("]")
            return {"kind": "fixed_size_list", "element": el, "size": size}
        if tok == "map":
            self.expect("(")
            key = self.dtype()
            self.expect(", ")
            value = self.dtype()
            self.expect(", keys_sorted=")
            flag = self.ident()
            self.expect(")")
            return {"kind": "map", "key": key, "value": value, "keys_sorted": flag == "true"}
        if "." in tok:
            meta = ""
            if self.peek() == "[":
                end = self.s.index("]", self.i)
                meta = self.s[self.i + 1:end]
                self.i = end + 1
            self.expect("(")
            storage = self.dtype()
            self.expect(")")
            return {"kind": "extension", "ext_id": tok, "ext_metadata": meta, "storage": storage}
        raise ValueError("dtype %r: unknown type %r" % (self.s, tok))


def parse_dtype(text):
    p = _Parser(text)
    node = p.dtype()
    if p.i != len(text):
        raise ValueError("dtype %r: trailing text at %d" % (text, p.i))
    return node


# ---------------------------------------------------------------------------------------
# Canonical hash (docs/testing.md, section 1). row_bytes returns, for each row of the
# array, the validity byte followed by the value when valid.
# ---------------------------------------------------------------------------------------

VALID = b"\x01"
NULL = b"\x00"


def u32(n):
    return struct.pack("<I", n)


def _is_varlen(t):
    return (pa.types.is_string(t) or pa.types.is_large_string(t) or pa.types.is_binary(t)
            or pa.types.is_large_binary(t) or pa.types.is_string_view(t)
            or pa.types.is_binary_view(t))


def _fixed_width(t):
    if pa.types.is_fixed_size_binary(t):
        return t.byte_width
    return t.bit_width // 8


def row_bytes(arr):
    if isinstance(arr, pa.ChunkedArray):
        arr = arr.combine_chunks()
    t = arr.type
    n = len(arr)
    if isinstance(t, pa.BaseExtensionType):
        return row_bytes(arr.storage)
    if pa.types.is_dictionary(t):
        return row_bytes(arr.dictionary_decode())
    if pa.types.is_null(t):
        return [NULL] * n
    valid = arr.is_valid().to_pylist()
    if pa.types.is_boolean(t):
        return [VALID + (b"\x01" if v else b"\x00") if ok else NULL
                for v, ok in zip(arr.to_pylist(), valid)]
    if _is_varlen(t):
        out = []
        for v, ok in zip(arr.to_pylist(), valid):
            if not ok:
                out.append(NULL)
                continue
            b = v.encode("utf-8") if isinstance(v, str) else v
            out.append(VALID + u32(len(b)) + b)
        return out
    if (pa.types.is_integer(t) or pa.types.is_floating(t) or pa.types.is_temporal(t)
            or pa.types.is_decimal(t) or pa.types.is_fixed_size_binary(t)):
        w = _fixed_width(t)
        buf = memoryview(arr.buffers()[1]).cast("B")
        base = arr.offset * w
        return [VALID + bytes(buf[base + i * w:base + (i + 1) * w]) if valid[i] else NULL
                for i in range(n)]
    if pa.types.is_struct(t):
        kids = [row_bytes(c) for c in arr.flatten()]
        return [VALID + b"".join(k[i] for k in kids) if valid[i] else NULL for i in range(n)]
    if pa.types.is_fixed_size_list(t):
        size = t.list_size
        kids = row_bytes(arr.values.slice(arr.offset * size, n * size))
        return [VALID + b"".join(kids[i * size:(i + 1) * size]) if valid[i] else NULL
                for i in range(n)]
    if pa.types.is_map(t):
        keys, items = row_bytes(arr.keys), row_bytes(arr.items)
        entries = [VALID + k + v for k, v in zip(keys, items)]
        offs = arr.offsets.to_pylist()
        return [VALID + u32(offs[i + 1] - offs[i]) + b"".join(entries[offs[i]:offs[i + 1]])
                if valid[i] else NULL for i in range(n)]
    if pa.types.is_list(t) or pa.types.is_large_list(t):
        kids = row_bytes(arr.values)
        offs = arr.offsets.to_pylist()
        return [VALID + u32(offs[i + 1] - offs[i]) + b"".join(kids[offs[i]:offs[i + 1]])
                if valid[i] else NULL for i in range(n)]
    if pa.types.is_list_view(t) or pa.types.is_large_list_view(t):
        kids = row_bytes(arr.values)
        offs, sizes = arr.offsets.to_pylist(), arr.sizes.to_pylist()
        return [VALID + u32(sizes[i]) + b"".join(kids[offs[i]:offs[i] + sizes[i]])
                if valid[i] else NULL for i in range(n)]
    raise ValueError("canonical hash: unsupported arrow type %s" % t)


def _json_quote(s):
    out = []
    for ch in s:
        if ch in '"\\':
            out.append("\\" + ch)
        elif ord(ch) < 0x20:
            out.append("\\u%04x" % ord(ch))
        else:
            out.append(ch)
    return '"' + "".join(out) + '"'


def canonical_json(v):
    """The canonical JSON of a Variant value (docs/testing.md). Not run: the reference's
    Python package cannot read a Variant column, see extra/MISSING.md."""
    import datetime
    import decimal
    if v is None:
        return "null"
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, int):
        return str(v)
    if isinstance(v, float):
        if v != v:
            return "NaN"
        if v in (float("inf"), float("-inf")):
            return "Infinity" if v > 0 else "-Infinity"
        s = "%.17g" % v
        return s if any(c in s for c in ".e") else s + ".0"
    if isinstance(v, str):
        return _json_quote(v)
    if isinstance(v, (bytes, bytearray)):
        return '{"$binary":"%s"}' % bytes(v).hex()
    if isinstance(v, decimal.Decimal):
        return '{"$decimal":"%s"}' % format(v, "f")
    if isinstance(v, (datetime.datetime, datetime.date)):
        t = v if isinstance(v, datetime.datetime) else datetime.datetime(v.year, v.month, v.day)
        t = t.astimezone(datetime.timezone.utc) if t.tzinfo else t
        frac = ("%06d" % t.microsecond).rstrip("0")
        return '{"$time":"%s%sZ"}' % (t.strftime("%Y-%m-%dT%H:%M:%S"), "." + frac if frac else "")
    if isinstance(v, (list, tuple)):
        return "[" + ",".join(canonical_json(e) for e in v) + "]"
    if isinstance(v, dict):
        keys = sorted(v, key=lambda k: k.encode("utf-8"))
        return "{" + ",".join(_json_quote(k) + ":" + canonical_json(v[k]) for k in keys) + "}"
    raise ValueError("canonical json: unsupported value %r" % (v,))


def variant_row_bytes(arr):
    out = []
    for v, ok in zip(arr.to_pylist(), arr.is_valid().to_pylist()):
        if not ok:
            out.append(NULL)
            continue
        b = canonical_json(v).encode("utf-8")
        out.append(VALID + u32(len(b)) + b)
    return out


def canonical_sha256(arr, variant=False):
    h = hashlib.sha256()
    for b in (variant_row_bytes(arr) if variant else row_bytes(arr)):
        h.update(b)
    return h.hexdigest()


# ---------------------------------------------------------------------------------------
# Expected values
# ---------------------------------------------------------------------------------------


def show(v):
    if isinstance(v, (bytes, bytearray)):
        return "0x" + bytes(v).hex()
    return str(v)[:200]


def sample(arr):
    """The first 5 and the last 5 values as text. Temporal types show the storage integer."""
    if isinstance(arr, pa.ChunkedArray):
        arr = arr.combine_chunks()
    t = arr.type
    if isinstance(t, pa.BaseExtensionType):
        arr, t = arr.storage, t.storage_type
    if pa.types.is_temporal(t):
        arr = arr.view(pa.int32() if t.bit_width == 32 else pa.int64())
    n = len(arr)
    head = arr.slice(0, min(5, n)).to_pylist()
    tail = arr.slice(max(n - 5, 0)).to_pylist()
    return {"first": [show(v) for v in head], "last": [show(v) for v in tail]}


def column_entry(arr, dtype):
    variant = dtype["kind"] == "extension" and "variant" in dtype["ext_id"]
    digest = canonical_sha256(arr, variant)
    entry = {"nulls": arr.null_count, "sha256": digest}
    try:
        entry.update(sample(arr))
    except Exception as e:  # a type that cannot be shown as text: the hash still stands
        entry.update({"first": [], "last": [], "sample_error": str(e)})
    return entry


def collect(path, arr, dtype, out):
    out[path] = column_entry(arr, dtype)
    if dtype["kind"] == "struct":
        if isinstance(arr, pa.ChunkedArray):
            arr = arr.combine_chunks()
        for field, child in zip(dtype["fields"], arr.flatten()):
            collect(path + "." + field["name"], child, field["dtype"], out)


ENCODING_LINE = re.compile(r"^\s*[\w\[\].]+: ([\w.]+)\(.*len=\d+")


def encodings_of(array):
    ids = set()
    for line in array.display_tree().splitlines():
        m = ENCODING_LINE.match(line)
        if m:
            ids.add(m.group(1))
    return sorted(ids)


def expected(path):
    f = vx.open(path)
    array = f.scan().read_all()
    dtype = parse_dtype(str(f.dtype))
    if dtype["kind"] != "struct":
        raise ValueError("%s: the top-level dtype is not a struct" % path)
    table = array.to_arrow_table()
    columns = {}
    for field, col in zip(dtype["fields"], table.columns):
        collect(field["name"], col, field["dtype"], columns)
    return {
        "columns": columns,
        "encodings": encodings_of(array),
        "rows": table.num_rows,
        "schema": dtype,
    }


def write_expected(path):
    data = expected(path)
    with open(path + ".expected.json", "w", encoding="utf-8", newline="\n") as fh:
        json.dump(data, fh, sort_keys=True, indent=2, ensure_ascii=True)
        fh.write("\n")


def literal_of(dtype, value):
    """A literal of the dtype of a column, for a filter."""
    if dtype["kind"] == "primitive" and dtype["ptype"][0] in "iu":
        return vx.expr.literal(vx.int_(int(dtype["ptype"][1:])), value)
    if dtype["kind"] == "primitive":
        return vx.expr.literal(vx.float_(int(dtype["ptype"][1:])), value)
    if dtype["kind"] == "utf8":
        return vx.expr.literal(vx.utf8(), value)
    raise ValueError("no filter on a %s" % dtype["kind"])


def filter_expr(spec, dtype):
    """The predicate of a filter of a hashes file: the column compared with up to two bounds,
    "ge", "gt", "le", "lt" and "eq" ({"column": "k", "ge": 10, "lt": 20}). The comparisons are the
    operators of the expression, which every release has (the functions gt_eq and lt_eq are
    newer than 0.83)."""
    ve = vx.expr
    field = next(f["dtype"] for f in dtype["fields"] if f["name"] == spec["column"])
    col = ve.column(spec["column"])
    terms = [fn(col, literal_of(field, spec[op]))
             for op, fn in (("ge", operator.ge), ("gt", operator.gt), ("le", operator.le), ("lt", operator.lt), ("eq", operator.eq))
             if op in spec]
    pred = terms[0]
    for term in terms[1:]:
        pred = ve.and_(pred, term)
    return pred


def check_filters(path, want):
    """Reads the file with each filter of the hashes file pushed to the reference reader, which
    prunes the zones with the statistics of the file: a wrong bound loses rows. Returns the
    problems."""
    problems = []
    f = vx.open(path)
    dtype = parse_dtype(str(f.dtype))
    for spec in want.get("filters", []):
        label = json.dumps({k: v for k, v in spec.items() if k not in ("rows", "columns")}, sort_keys=True)
        table = f.scan(expr=filter_expr(spec, dtype)).read_all().to_arrow_table()
        if table.num_rows != spec["rows"]:
            problems.append("filter %s: %d rows, want %d" % (label, table.num_rows, spec["rows"]))
            continue
        columns = {}
        for field, col in zip(dtype["fields"], table.columns):
            collect(field["name"], col, field["dtype"], columns)
        for name, w in sorted(spec["columns"].items()):
            if columns[name]["sha256"] != w["sha256"] or columns[name]["nulls"] != w["nulls"]:
                problems.append("filter %s: column %s differs" % (label, name))
    return problems


def check(folder):
    """Opens every <name>.vortex of the folder that has a <name>.hashes.json with the
    reference reader, and compares the row count and, per column path, the null count and
    the canonical hash with the ones the writer computed from its own source data. Returns
    the number of files that differ (or cannot be read), 2 when there is no file to check."""
    import glob
    import os
    paths = sorted(glob.glob(os.path.join(folder, "*.vortex")))
    paths = [p for p in paths if os.path.exists(p[:-len(".vortex")] + ".hashes.json")]
    if not paths:
        sys.stderr.write("oracle check: no .vortex file with a .hashes.json in %s\n" % folder)
        return 2
    bad = 0
    for path in paths:
        name = os.path.basename(path)
        with open(path[:-len(".vortex")] + ".hashes.json", encoding="utf-8") as fh:
            want = json.load(fh)
        try:
            got = expected(path)
        except BaseException as e:  # the reference reader refuses the file (a Rust panic is one)
            if isinstance(e, KeyboardInterrupt):
                raise
            sys.stderr.write("FAIL %s: the reference reader failed: %s\n" % (name, str(e).replace("\n", " ")))
            bad += 1
            continue
        problems = []
        if got["rows"] != want["rows"]:
            problems.append("rows: got %d, want %d" % (got["rows"], want["rows"]))
        for col in sorted(set(got["columns"]) | set(want["columns"])):
            g, w = got["columns"].get(col), want["columns"].get(col)
            if g is None or w is None:
                problems.append("column %s: %s" % (col, "missing from the file" if g is None else "not expected"))
            elif g["nulls"] != w["nulls"] or g["sha256"] != w["sha256"]:
                problems.append("column %s: nulls %s/%s, sha256 %s/%s (got/want)" % (
                    col, g["nulls"], w["nulls"], g["sha256"][:12], w["sha256"][:12]))
        try:
            problems.extend(check_filters(path, want))
        except BaseException as e:  # the reference reader refuses the filter
            if isinstance(e, KeyboardInterrupt):
                raise
            problems.append("filters: the reference reader failed: %s" % str(e).replace("\n", " "))
        if "schema" in want and got["schema"] != want["schema"]:
            problems.append("schema: got %s, want %s" % (json.dumps(got["schema"], sort_keys=True),
                                                         json.dumps(want["schema"], sort_keys=True)))
        if problems:
            bad += 1
            sys.stderr.write("FAIL %s:\n  %s\n" % (name, "\n  ".join(problems)))
        else:
            print("ok   %s" % name)
    print("%d file(s), %d failed, vortex-data %s" % (len(paths), bad, getattr(vx, "__version__", "?")))
    return bad


def main(argv):
    if len(argv) >= 2 and argv[1] == "expected" and len(argv) > 2:
        for p in argv[2:]:
            write_expected(p)
        return 0
    if len(argv) == 3 and argv[1] == "check":
        return min(check(argv[2]), 255)
    sys.stderr.write("usage: oracle.py expected FILE... | oracle.py check DIR\n")
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
