use serde_json::{Value, json};
use vortex::array::arrays::{ConstantArray, RunEnd, VarBinViewArray};
use vortex::array::builders::dict::dict_encode;
use vortex::array::scalar::Scalar;
use vortex::array::{ArrayRef, IntoArray};
use vortex_fastlanes::bitpack_compress::{
    bit_width_histogram, bitpack_encode, bitpack_to_best_bit_width, find_best_bit_width,
};
use vortex_fastlanes::{FoRData, RLEData};
use vortex_sequence::sequence_encode;
use vortex_sparse::Sparse;
use vortex_zigzag::zigzag_encode;

use crate::cases::{self, Rng};
use crate::{Ctx, bitmap, hex, prim, raw};

pub type Files = Vec<(String, Value)>;

/// One vector: the input, its validity, the parameters and the node that comes out of the
/// encoder (null when the encoder declines the input).
fn vector<T: Copy>(name: &str, input: &[T], valid: &Option<Vec<bool>>, params: Value, out: Option<Value>) -> Value {
    json!({
        "name": name,
        "len": input.len(),
        "input": hex(&raw(input)),
        "valid": valid.as_ref().map(|v| bitmap(v)),
        "params": params,
        "output": out,
    })
}

fn file(encoding: &str, ptype: &str, vectors: Vec<Value>) -> (String, Value) {
    (ptype.to_string(), json!({"encoding": encoding, "ptype": ptype, "vectors": vectors}))
}

/// The nulls to try with n values: none, and a few when there is room for both.
fn validities(rng: &mut Rng, n: usize) -> Vec<(&'static str, Option<Vec<bool>>)> {
    let mut v = vec![("", None)];
    if n > 1 {
        v.push(("_nulls", Some(cases::nulls(rng, n, 5))));
    }
    v
}

const LENS: [usize; 4] = [1, 130, 1024, 1025];

macro_rules! ints {
    ($m:ident, $ctx:expr) => {
        vec![
            ("i8", $m!(i8, 8, $ctx)),
            ("i16", $m!(i16, 16, $ctx)),
            ("i32", $m!(i32, 32, $ctx)),
            ("i64", $m!(i64, 64, $ctx)),
            ("u8", $m!(u8, 8, $ctx)),
            ("u16", $m!(u16, 16, $ctx)),
            ("u32", $m!(u32, 32, $ctx)),
            ("u64", $m!(u64, 64, $ctx)),
        ]
    };
}

macro_rules! numbers {
    ($m:ident, $ctx:expr) => {{
        let mut v = ints!($m, $ctx);
        v.push(("f32", $m!(f32, 32, $ctx)));
        v.push(("f64", $m!(f64, 64, $ctx)));
        v
    }};
}

fn dump(ctx: &Ctx, a: ArrayRef) -> Value {
    ctx.dump(&a)
}

macro_rules! bitpacked {
    ($t:ty, $bits:expr, $ctx:expr) => {{
        let ctx: &Ctx = $ctx;
        let mut rng = Rng(7 + $bits);
        let mut vs = vec![];
        let max = <$t>::MAX as u64;
        let widths = [0u32, 1, $bits / 2, $bits - 1];
        for w in widths {
            for n in [1usize, 130, 1024, 1025] {
                for every in [0u64, 9] {
                    if w == 0 && every == 0 && n != 1 {
                        continue;
                    }
                    if n >= 1024 && (w != $bits / 2 || every == 0 && n == 1025) {
                        continue;
                    }
                    let data: Vec<$t> = cases::small(&mut rng, n, w, every, $bits - 1)
                        .iter()
                        .map(|x| (x & max) as $t)
                        .collect();
                    for (suffix, valid) in validities(&mut rng, n) {
                        let a = prim(&data, &valid);
                        let e = bitpack_encode(&a, w as u8, None, &mut ctx.exec()).unwrap();
                        let name = format!("w{w}_n{n}_e{every}{suffix}");
                        vs.push(vector(&name, &data, &valid, json!({"bit_width": w}), Some(dump(ctx, e.into_array()))));
                    }
                }
            }
        }
        // The best width, from the histogram, and the array it makes.
        for (i, n) in [1usize, 130, 1025].into_iter().enumerate() {
            for bits in [0u32, 3, $bits / 2, $bits - 1] {
                if n == 1025 && bits != $bits / 2 {
                    continue;
                }
                let every = [0u64, 40, 7][i];
                let data: Vec<$t> = cases::small(&mut rng, n, bits, every, $bits - 1)
                    .iter()
                    .map(|x| (x & max) as $t)
                    .collect();
                for (suffix, valid) in validities(&mut rng, n) {
                    let a = prim(&data, &valid);
                    let hist = bit_width_histogram(a.as_view(), &mut ctx.exec()).unwrap();
                    let best = find_best_bit_width(a.ptype(), &hist).unwrap();
                    let e = bitpack_to_best_bit_width(&a, &mut ctx.exec()).ok();
                    let name = format!("best_b{bits}_n{n}_e{every}{suffix}");
                    vs.push(vector(
                        &name,
                        &data,
                        &valid,
                        json!({"best": true, "bit_width": best, "histogram": hist}),
                        e.map(|e| dump(ctx, e.into_array())),
                    ));
                }
            }
        }
        vs
    }};
}

macro_rules! for_ {
    ($t:ty, $bits:expr, $ctx:expr) => {{
        let ctx: &Ctx = $ctx;
        let mut rng = Rng(11 + $bits);
        let mut vs = vec![];
        for n in [1usize, 130, 1025] {
            for (kind, bits) in [("tight", 5u32), ("wide", $bits)] {
                if n == 1025 && kind == "wide" {
                    continue;
                }
                let base = rng.next();
                let data: Vec<$t> = cases::small(&mut rng, n, bits.min($bits), 0, 8)
                    .iter()
                    .map(|x| base.wrapping_add(*x) as $t)
                    .collect();
                for (suffix, valid) in validities(&mut rng, n) {
                    let a = prim(&data, &valid);
                    let e = FoRData::encode(a, &mut ctx.exec()).unwrap();
                    vs.push(vector(&format!("{kind}_n{n}{suffix}"), &data, &valid, json!({}), Some(dump(ctx, e.into_array()))));
                }
            }
        }
        vs
    }};
}

fn signed(ctx: &Ctx) -> Vec<(&'static str, Vec<Value>)> {
    macro_rules! zz {
        ($t:ty, $bits:expr, $ctx:expr) => {{
            let ctx: &Ctx = $ctx;
            let mut rng = Rng(13 + $bits);
            let mut vs = vec![];
            for n in LENS {
                let mut data: Vec<$t> = (0..n).map(|_| (rng.next() >> (64 - 6)) as $t - 32).collect();
                if n > 4 {
                    data[1] = <$t>::MIN;
                    data[2] = <$t>::MAX;
                    data[3] = 0;
                }
                for (suffix, valid) in validities(&mut rng, n) {
                    let a = prim(&data, &valid);
                    let e = zigzag_encode(a.as_view()).unwrap();
                    vs.push(vector(&format!("n{n}{suffix}"), &data, &valid, json!({}), Some(dump(ctx, e.into_array()))));
                }
            }
            vs
        }};
    }
    vec![
        ("i8", zz!(i8, 8, ctx)),
        ("i16", zz!(i16, 16, ctx)),
        ("i32", zz!(i32, 32, ctx)),
        ("i64", zz!(i64, 64, ctx)),
    ]
}

macro_rules! rle {
    ($t:ty, $bits:expr, $ctx:expr) => {{
        let ctx: &Ctx = $ctx;
        let mut rng = Rng(17 + $bits);
        let mut vs = vec![];
        for n in [1usize, 130, 1025, 2100] {
            for (kind, max) in [("short", 3u64), ("long", 400)] {
                if n == 2100 && ($bits > 16 || max == 3) {
                    continue;
                }
                if n == 1025 && $bits > 32 && max == 3 {
                    continue;
                }
                let data: Vec<$t> = cases::runs(&mut rng, n, max, 6).iter().map(|x| *x as $t).collect();
                for (suffix, valid) in validities(&mut rng, n) {
                    let a = prim(&data, &valid);
                    let e = RLEData::encode(a.as_view(), &mut ctx.exec()).unwrap();
                    vs.push(vector(&format!("{kind}_n{n}{suffix}"), &data, &valid, json!({}), Some(dump(ctx, e.into_array()))));
                }
            }
        }
        vs
    }};
}

macro_rules! runend {
    ($t:ty, $bits:expr, $ctx:expr) => {{
        let ctx: &Ctx = $ctx;
        let mut rng = Rng(19 + $bits);
        let mut vs = vec![];
        for n in [1usize, 130, 1025, 70000] {
            for (kind, max) in [("short", 3u64), ("long", 400)] {
                if n == 70000 && (max == 3 || $bits != 8) || n == 1025 && $bits > 16 && max == 3 {
                    continue;
                }
                let data: Vec<$t> = cases::runs(&mut rng, n, max, 6).iter().map(|x| *x as $t).collect();
                for (suffix, valid) in validities(&mut rng, n) {
                    if n == 70000 && valid.is_some() {
                        continue;
                    }
                    let a = prim(&data, &valid);
                    let e = RunEnd::encode(a.into_array(), &mut ctx.exec()).unwrap();
                    vs.push(vector(&format!("{kind}_n{n}{suffix}"), &data, &valid, json!({}), Some(dump(ctx, e.into_array()))));
                }
            }
        }
        vs
    }};
}

macro_rules! dict {
    ($t:ty, $bits:expr, $ctx:expr) => {{
        let ctx: &Ctx = $ctx;
        let mut rng = Rng(23 + $bits);
        let mut vs = vec![];
        for n in [1usize, 130, 1025] {
            for (kind, bits) in [("few", 2u32), ("many", 9)] {
                if n == 1025 && ($bits > 16 && kind == "many") {
                    continue;
                }
                let data: Vec<$t> = cases::small(&mut rng, n, bits, 0, 8).iter().map(|x| *x as $t).collect();
                for (suffix, valid) in validities(&mut rng, n) {
                    let a = prim(&data, &valid);
                    let e = dict_encode(&a.into_array(), &mut ctx.exec()).unwrap();
                    vs.push(vector(&format!("{kind}_n{n}{suffix}"), &data, &valid, json!({}), Some(dump(ctx, e.into_array()))));
                }
            }
        }
        vs
    }};
}

macro_rules! sequence {
    ($t:ty, $bits:expr, $ctx:expr) => {{
        let ctx: &Ctx = $ctx;
        let mut vs = vec![];
        let max = <$t>::MAX as i128;
        let min = <$t>::MIN as i128;
        let step_max = 1i128 << ($bits / 2);
        let mut cases: Vec<(String, Vec<$t>)> = vec![];
        for (base, step) in [(0i128, 1i128), (5, 3), (min, 1), (max - 2000, 1), (max, -1), (min / 2, step_max), (0, -1), (3, 0)] {
            for n in [1usize, 2, 130, 1025] {
                let v: Option<Vec<$t>> = (0..n as i128)
                    .map(|i| {
                        let x = base + i * step;
                        (x >= min && x <= max).then_some(x as $t)
                    })
                    .collect();
                if let Some(v) = v {
                    cases.push((format!("b{base}_s{step}_n{n}"), v));
                }
            }
        }
        // one value off a sequence: at the start, in the middle, at the end
        for n in [3usize, 130] {
            for at in [0, n / 2, n - 1] {
                let mut v: Vec<$t> = (0..n).map(|i| (i as i128 * 2 + 1) as $t).collect();
                v[at] = v[at].wrapping_add(5);
                cases.push((format!("off_n{n}_at{at}"), v));
            }
        }
        for (name, data) in cases {
            let a = prim(&data, &None);
            let e = sequence_encode(a.as_view(), &mut ctx.exec()).unwrap();
            vs.push(vector(&name, &data, &None, json!({}), e.map(|e| dump(ctx, e))));
        }
        let data: Vec<$t> = vec![1, 2, 3, 4];
        let valid = Some(vec![true, false, true, true]);
        let a = prim(&data, &valid);
        let e = sequence_encode(a.as_view(), &mut ctx.exec()).unwrap();
        vs.push(vector("with_a_null", &data, &valid, json!({}), e.map(|e| dump(ctx, e))));
        vs
    }};
}

macro_rules! constant {
    ($t:ty, $bits:expr, $ctx:expr) => {{
        let ctx: &Ctx = $ctx;
        let mut vs = vec![];
        for v in [0 as $t, 1 as $t, <$t>::MAX, <$t>::MIN] {
            let a = ConstantArray::new(Scalar::from(v), 5).into_array();
            vs.push(vector(&format!("v{v}"), &[v; 5], &None, json!({}), Some(dump(ctx, a))));
        }
        vs
    }};
}

macro_rules! sparse {
    ($t:ty, $bits:expr, $ctx:expr) => {{
        let ctx: &Ctx = $ctx;
        let mut rng = Rng(29 + $bits);
        let mut vs = vec![];
        for n in [40usize, 130, 1025] {
            for (kind, every) in [("rare", 20u64), ("half", 2)] {
                if n == 1025 && ($bits > 16 && kind == "half") {
                    continue;
                }
                let data: Vec<$t> = (0..n)
                    .map(|_| if rng.next() % every == 0 { (rng.next() >> 60) as $t + 1 as $t } else { 7 as $t })
                    .collect();
                for (suffix, valid) in validities(&mut rng, n) {
                    let a = prim(&data, &valid).into_array();
                    let e = Sparse::encode(&a, None, &mut ctx.exec()).unwrap();
                    vs.push(vector(&format!("{kind}_n{n}{suffix}"), &data, &valid, json!({}), Some(dump(ctx, e))));
                }
            }
        }
        vs
    }};
}

fn strings_dict(ctx: &Ctx) -> Files {
    let mut rng = Rng(31);
    let words = ["", "a", "abcdefghijkl", "abcdefghijklm", "a longer string that is not inlined", "zz"];
    let mut files = vec![];
    for (kind, binary) in [("utf8", false), ("binary", true)] {
        let mut vs = vec![];
        for n in [1usize, 130, 1025] {
            for k in [2usize, 6] {
                let data: Vec<&str> = (0..n).map(|_| words[(rng.next() as usize) % k]).collect();
                for (suffix, valid) in validities(&mut rng, n) {
                    let a = match (&valid, binary) {
                        (None, false) => VarBinViewArray::from_iter_str(data.iter().copied()),
                        (None, true) => VarBinViewArray::from_iter_bin(data.iter().map(|s| s.as_bytes())),
                        (Some(v), false) => VarBinViewArray::from_iter_nullable_str(
                            data.iter().zip(v).map(|(s, ok)| ok.then_some(*s)),
                        ),
                        (Some(v), true) => VarBinViewArray::from_iter_nullable_bin(
                            data.iter().zip(v).map(|(s, ok)| ok.then_some(s.as_bytes())),
                        ),
                    };
                    let e = dict_encode(&a.into_array(), &mut ctx.exec()).unwrap();
                    let input: Vec<String> = data.iter().map(|s| hex(s.as_bytes())).collect();
                    vs.push(json!({
                        "name": format!("k{k}_n{n}{suffix}"),
                        "len": n,
                        "strings": input,
                        "valid": valid.as_ref().map(|v| bitmap(v)),
                        "params": {},
                        "output": dump(ctx, e.into_array()),
                    }));
                }
            }
        }
        files.push(file("dict", kind, vs));
    }
    files
}

pub fn all(ctx: &Ctx) -> Vec<(String, Files)> {
    let mut out = vec![];
    let mut add = |name: &str, per_type: Vec<(&str, Vec<Value>)>| {
        out.push((
            name.to_string(),
            per_type.into_iter().map(|(t, v)| file(name, t, v)).collect::<Files>(),
        ));
    };
    add("bitpacked", ints!(bitpacked, ctx));
    add("for", ints!(for_, ctx));
    add("zigzag", signed(ctx));
    add("rle", numbers!(rle, ctx));
    add("runend", numbers!(runend, ctx));
    add("dict", numbers!(dict, ctx));
    add("dict", vec![]);
    add("sequence", ints!(sequence, ctx));
    add("sparse", numbers!(sparse, ctx));
    add("constant", ints!(constant, ctx));
    out.push(("dict".to_string(), strings_dict(ctx)));
    out.retain(|(_, f)| !f.is_empty());
    merge(out)
}

/// merge joins the entries that have the same name.
fn merge(v: Vec<(String, Files)>) -> Vec<(String, Files)> {
    let mut out: Vec<(String, Files)> = vec![];
    for (n, f) in v {
        match out.iter_mut().find(|(m, _)| *m == n) {
            Some((_, g)) => g.extend(f),
            None => out.push((n, f)),
        }
    }
    out
}

