//! The vectors of the encoders of floats and strings: ALP (the exponents given, and the ones the
//! search picks), ALP-RD (the search, and a cut given) and FSST (the table the reference trains,
//! and what it makes of the strings with it).

use serde_json::{Value, json};
use vortex::array::arrays::VarBinViewArray;
use vortex::array::{ArrayRef, IntoArray};
use vortex_alp::{Exponents, RDEncoder, RDEncoderExt, alp_encode};
use vortex_fsst::{fsst_compress, fsst_train_compressor};

use crate::cases::Rng;
use crate::enc::{Files, file, validities, vector};
use crate::{Ctx, bitmap, hex, prim};

const LENS: [usize; 4] = [1, 130, 1025, 2100];

/// The 2100 rows (two chunks of patches) are for one kind: the files stay small.
fn wanted(kind: &str, n: usize, valid: bool) -> bool {
    n != 2100 || kind == "mixed" && !valid
}

/// The kinds of floats: prices, measurements, integers, random bits and the special values.
fn kinds() -> [&'static str; 6] {
    ["price", "noisy", "ints", "random", "special", "mixed"]
}

fn gen64(rng: &mut Rng, kind: &str, n: usize) -> Vec<f64> {
    (0..n)
        .map(|i| match kind {
            "price" => (rng.next() % 1_000_000) as f64 / 100.0,
            "noisy" => 20.0 + (rng.next() % 1000) as f64 / 1000.0 + (rng.next() % 1000) as f64 * 1e-12,
            "ints" => (rng.next() % 100_000) as f64 - 500.0,
            "random" => f64::from_bits(rng.next()),
            "special" => [1.5, f64::NAN, -0.0, f64::INFINITY, 2.25, f64::NEG_INFINITY, 0.0][i % 7],
            _ => {
                if rng.next() % 9 == 0 {
                    f64::from_bits(rng.next())
                } else {
                    (rng.next() % 100_000) as f64 / 100.0
                }
            }
        })
        .collect()
}

fn gen32(rng: &mut Rng, kind: &str, n: usize) -> Vec<f32> {
    (0..n)
        .map(|i| match kind {
            "price" => (rng.next() % 100_000) as f32 / 100.0,
            "noisy" => 20.0 + (rng.next() % 1000) as f32 / 1000.0 + (rng.next() % 1000) as f32 * 1e-7,
            "ints" => (rng.next() % 10_000) as f32 - 500.0,
            "random" => f32::from_bits(rng.next() as u32),
            "special" => [1.5, f32::NAN, -0.0, f32::INFINITY, 2.25, f32::NEG_INFINITY, 0.0][i % 7],
            _ => {
                if rng.next() % 9 == 0 {
                    f32::from_bits(rng.next() as u32)
                } else {
                    (rng.next() % 10_000) as f32 / 100.0
                }
            }
        })
        .collect()
}

macro_rules! alp {
    ($t:ty, $gen:ident, $maxe:expr, $ctx:expr) => {{
        let ctx: &Ctx = $ctx;
        let mut rng = Rng(37 + $maxe as u64);
        let mut vs = vec![];
        for kind in kinds() {
            for n in LENS {
                let data = $gen(&mut rng, kind, n);
                for (suffix, valid) in validities(&mut rng, n) {
                    if !wanted(kind, n, valid.is_some()) {
                        continue;
                    }
                    let a = prim(&data, &valid);
                    let best = alp_encode(a.as_view(), None, &mut ctx.exec()).unwrap();
                    vs.push(vector(&format!("best_{kind}_n{n}{suffix}"), &data, &valid, json!({"best": true}), Some(ctx.dump(&best.into_array()))));
                    for (e, f) in [(2u8, 0u8), ($maxe / 2, $maxe / 4)] {
                        let a = prim(&data, &valid);
                        let x = alp_encode(a.as_view(), Some(Exponents { e, f }), &mut ctx.exec()).unwrap();
                        let name = format!("e{e}_f{f}_{kind}_n{n}{suffix}");
                        vs.push(vector(&name, &data, &valid, json!({"alp_e": e, "alp_f": f}), Some(ctx.dump(&x.into_array()))));
                    }
                }
            }
        }
        vs
    }};
}

macro_rules! alprd {
    ($t:ty, $gen:ident, $bits:expr, $ctx:expr) => {{
        let ctx: &Ctx = $ctx;
        let mut rng = Rng(41 + $bits as u64);
        let mut vs = vec![];
        for kind in kinds() {
            for n in LENS {
                let data = $gen(&mut rng, kind, n);
                for (suffix, valid) in validities(&mut rng, n) {
                    if !wanted(kind, n, valid.is_some()) {
                        continue;
                    }
                    let a = prim(&data, &valid);
                    let enc = RDEncoder::new(&data);
                    let x = enc.encode(a.as_view());
                    vs.push(vector(&format!("search_{kind}_n{n}{suffix}"), &data, &valid, json!({"best": true}), Some(ctx.dump(&x.into_array()))));
                    // a cut given: the first values' left parts make the dictionary
                    let cut = $bits - 16u8;
                    let mut codes: Vec<u16> = vec![];
                    for v in data.iter().take(40) {
                        let l = (v.to_bits() >> cut) as u16;
                        if !codes.contains(&l) && codes.len() < 8 {
                            codes.push(l);
                        }
                    }
                    let enc = RDEncoder::from_parts(cut, codes.clone());
                    let a = prim(&data, &valid);
                    let x = enc.encode(a.as_view());
                    vs.push(vector(&format!("cut{cut}_{kind}_n{n}{suffix}"), &data, &valid, json!({"right_bits": cut, "dict": codes}), Some(ctx.dump(&x.into_array()))));
                }
            }
        }
        vs
    }};
}

fn urls(rng: &mut Rng, n: usize) -> Vec<Vec<u8>> {
    let hosts = ["www.acme.com", "shop.acme.org", "api.example.net", "acme.io"];
    let paths = ["index.html", "products/list", "a/b/c/d", "search?q=rust&page=", "static/img/logo.png"];
    (0..n)
        .map(|_| {
            let h = hosts[(rng.next() % 4) as usize];
            let p = paths[(rng.next() % 5) as usize];
            format!("https://{h}/{p}{}", rng.next() % 1000).into_bytes()
        })
        .collect()
}

fn words(rng: &mut Rng, n: usize) -> Vec<Vec<u8>> {
    let w = ["", "a", "the", "quick", "brown", "fox", "jumps", "over", "lazy", "dog", "encoding", "vortex"];
    (0..n)
        .map(|_| {
            let k = rng.next() % 5;
            let parts: Vec<&str> = (0..k).map(|_| w[(rng.next() % 12) as usize]).collect();
            parts.join(" ").into_bytes()
        })
        .collect()
}

fn uuids(rng: &mut Rng, n: usize) -> Vec<Vec<u8>> {
    (0..n)
        .map(|_| {
            let (a, b) = (rng.next(), rng.next());
            format!("{:08x}-{:04x}-4{:03x}-{:04x}-{:012x}", a >> 32, a >> 16 & 0xffff, a & 0xfff, b >> 48, b & 0xffff_ffff_ffff).into_bytes()
        })
        .collect()
}

/// Bytes that are not text: any byte, including 0 and 255, which the table must escape or hold.
fn binary(rng: &mut Rng, n: usize) -> Vec<Vec<u8>> {
    (0..n)
        .map(|_| {
            let len = (rng.next() % 40) as usize;
            (0..len).map(|_| if rng.next() % 4 == 0 { 0 } else { (rng.next() >> 56) as u8 }).collect()
        })
        .collect()
}

/// Random lowercase text with no repetition to speak of.
fn noise(rng: &mut Rng, n: usize) -> Vec<Vec<u8>> {
    (0..n).map(|_| (0..30).map(|_| b'a' + (rng.next() % 26) as u8).collect()).collect()
}

fn fsst_strings(ctx: &Ctx) -> Files {
    let mut rng = Rng(43);
    let mut files = vec![];
    for (kind, utf8) in [("utf8", true), ("binary", false)] {
        let mut vs = vec![];
        let sets: [(&str, fn(&mut Rng, usize) -> Vec<Vec<u8>>); 5] =
            [("urls", urls), ("words", words), ("uuids", uuids), ("noise", noise), ("binary", binary)];
        for (name, make) in sets {
            if utf8 && name == "binary" {
                continue;
            }
            // 3000 urls are above the 16 KB the training samples
            for n in [1usize, 130, 1025, 3000] {
                if n == 3000 && name != "urls" || n == 1025 && name != "urls" && name != "words" {
                    continue;
                }
                let data = make(&mut rng, n);
                for (suffix, valid) in validities(&mut rng, n) {
                    if n == 3000 && valid.is_some() {
                        continue;
                    }
                    let a: ArrayRef = match (&valid, utf8) {
                        (None, true) => VarBinViewArray::from_iter_str(data.iter().map(|s| std::str::from_utf8(s).unwrap())),
                        (None, false) => VarBinViewArray::from_iter_bin(data.iter().map(|s| s.as_slice())),
                        (Some(v), true) => VarBinViewArray::from_iter_nullable_str(
                            data.iter().zip(v).map(|(s, ok)| ok.then(|| std::str::from_utf8(s).unwrap())),
                        ),
                        (Some(v), false) => VarBinViewArray::from_iter_nullable_bin(
                            data.iter().zip(v).map(|(s, ok)| ok.then(|| s.as_slice())),
                        ),
                    }
                    .into_array();
                    let c = fsst_train_compressor(&a, &mut ctx.exec()).unwrap();
                    let x = fsst_compress(&a, &c, &mut ctx.exec()).unwrap();
                    let input: Vec<String> = data.iter().map(|s| hex(s)).collect();
                    vs.push(json!({
                        "name": format!("{name}_n{n}{suffix}"),
                        "len": n,
                        "strings": input,
                        "valid": valid.as_ref().map(|v| bitmap(v)),
                        "params": {},
                        "output": ctx.dump(&x.into_array()),
                    }));
                }
            }
        }
        files.push(file("fsst", kind, vs));
    }
    files
}

pub fn all(ctx: &Ctx) -> Vec<(String, Files)> {
    let f = |enc: &str, per: Vec<(&str, Vec<Value>)>| -> (String, Files) {
        (enc.to_string(), per.into_iter().map(|(t, v)| file(enc, t, v)).collect())
    };
    vec![
        f("alp", vec![("f32", alp!(f32, gen32, 10u8, ctx)), ("f64", alp!(f64, gen64, 18u8, ctx))]),
        f("alprd", vec![("f32", alprd!(f32, gen32, 32u8, ctx)), ("f64", alprd!(f64, gen64, 64u8, ctx))]),
        ("fsst".to_string(), fsst_strings(ctx)),
    ]
}
