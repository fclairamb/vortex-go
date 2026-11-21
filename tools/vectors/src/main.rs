//! Writes the byte-exact vectors of the encoders: for each deterministic encoder of the
//! reference, an input, the parameters, and the node it serializes to (id, metadata, buffers
//! and children, as hex). See README.md.
//!
//!     cargo run --release -- ../../testdata/vectors

use std::fs;
use std::path::PathBuf;

use serde_json::{Value, json};
use vortex::VortexSessionDefault;
use vortex::array::arrays::PrimitiveArray;
use vortex::array::session::ArraySessionExt;
use vortex::array::validity::Validity;
use vortex::array::{ArrayRef, ExecutionCtx, VortexSessionExecute};
use vortex::session::VortexSession;
use vortex_buffer::{BitBufferMut, Buffer};

mod cases;
mod enc;
mod floats;

pub struct Ctx {
    pub session: VortexSession,
}

impl Ctx {
    pub fn exec(&self) -> ExecutionCtx {
        self.session.create_execution_ctx()
    }

    /// dump is the serialization of an array: its id, metadata, buffers and children.
    pub fn dump(&self, a: &ArrayRef) -> Value {
        let s = self
            .session
            .array_serialize(a)
            .expect("serialize")
            .expect("a serialization");
        json!({
            "id": s.serialized_id.to_string(),
            "metadata": hex(&s.metadata),
            "buffers": s.buffers.iter().map(|b| hex(b.as_slice())).collect::<Vec<_>>(),
            "children": s.children.iter().map(|c| self.dump(c)).collect::<Vec<_>>(),
        })
    }
}

pub fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

/// A bitmap, least significant bit first.
pub fn bitmap(valid: &[bool]) -> String {
    let mut b = vec![0u8; valid.len().div_ceil(8)];
    for (i, v) in valid.iter().enumerate() {
        if *v {
            b[i / 8] |= 1 << (i % 8);
        }
    }
    hex(&b)
}

pub fn validity(valid: &Option<Vec<bool>>) -> Validity {
    match valid {
        None => Validity::NonNullable,
        Some(v) => {
            let mut bits = BitBufferMut::with_capacity(v.len());
            for x in v {
                bits.append(*x);
            }
            Validity::from(bits.freeze())
        }
    }
}

pub fn prim<T: vortex::array::dtype::NativePType>(v: &[T], valid: &Option<Vec<bool>>) -> PrimitiveArray {
    PrimitiveArray::new(Buffer::copy_from(v), validity(valid))
}

pub fn raw<T: Copy>(v: &[T]) -> Vec<u8> {
    let n = std::mem::size_of_val(v);
    unsafe { std::slice::from_raw_parts(v.as_ptr() as *const u8, n).to_vec() }
}

fn main() {
    let dir: PathBuf = std::env::args().nth(1).expect("the output folder").into();
    let session = VortexSession::default();
    vortex_fastlanes::initialize(&session);
    vortex_zigzag::initialize(&session);
    vortex_sequence::initialize(&session);
    vortex_sparse::initialize(&session);
    vortex_alp::initialize(&session);
    vortex_fsst::initialize(&session);
    vortex_onpair::initialize(&session);
    let ctx = Ctx { session };
    for (name, vectors) in enc::all(&ctx) {
        let d = dir.join(&name);
        fs::create_dir_all(&d).unwrap();
        for (file, v) in vectors {
            let s = if v.get("vectors").is_some() { render(&v) } else { serde_json::to_string(&v).unwrap() + "\n" };
            fs::write(d.join(format!("{file}.json")), s).unwrap();
        }
    }
}

/// One vector per line: small files, and a diff that names the vector that changed.
fn render(v: &Value) -> String {
    let mut s = String::new();
    let head = json!({"encoding": v["encoding"], "ptype": v["ptype"]});
    let head = serde_json::to_string(&head).unwrap();
    s.push_str(&head[..head.len() - 1]);
    s.push_str(",\"vectors\":[\n");
    let vs = v["vectors"].as_array().unwrap();
    for (i, x) in vs.iter().enumerate() {
        s.push_str(&serde_json::to_string(x).unwrap());
        s.push_str(if i + 1 < vs.len() { ",\n" } else { "\n" });
    }
    s.push_str("]}\n");
    s
}
