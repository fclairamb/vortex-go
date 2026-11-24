//! The user metadata of a Vortex file, with the reference at the pinned release (python
//! vortex-data has no metadata in its API).
//!
//!     cargo run --release -q --bin filemeta -- read file.vortex
//!     cargo run --release -q --bin filemeta -- write file.vortex
//!
//! `read` opens the file with the metadata (`include_metadata`) and prints one line per entry,
//! sorted by key: the key, a tab, the value in hex. `write` writes a file of 1,000 rows with the
//! entries of `entries()`, then prints what `read` prints of it, so the expected values come
//! from the reference reader.

use std::env;

use vortex::VortexSessionDefault;
use vortex::array::IntoArray;
use vortex::array::arrays::StructArray;
use vortex::buffer::{Buffer, ByteBuffer, ByteBufferMut};
use vortex::error::VortexResult;
use vortex::file::{OpenOptionsSessionExt, WriteOptionsSessionExt};
use vortex::session::VortexSession;

/// The entries of the file of `write`: the limits (16 entries, a key of 64 bytes, a multibyte
/// key), an empty value, and a value of 80 KiB, which the tail a reader reads first does not hold.
fn entries() -> Vec<(String, Vec<u8>)> {
    let mut out = vec![
        ("dbbat.query_offsets".to_string(), br#"{"q1":0,"q2":512}"#.to_vec()),
        ("schema.version".to_string(), b"3".to_vec()),
        ("empty".to_string(), Vec::new()),
        ("k".repeat(64), b"longest key".to_vec()),
        ("é".repeat(32), "clé".as_bytes().to_vec()),
        ("big".to_string(), (0..80 * 1024).map(|i: u32| (i.wrapping_mul(2654435761) >> 24) as u8).collect()),
    ];
    for i in out.len()..16 {
        out.push((format!("pad.{i:02}"), vec![i as u8; i]));
    }
    out
}

async fn read(session: &VortexSession, path: &str) -> VortexResult<()> {
    let file = session.open_options().include_metadata().open_path(path).await?;
    let mut lines: Vec<(String, String)> = file
        .metadata_segments()
        .map(|(k, v)| (k.to_string(), v.as_slice().iter().map(|b| format!("{b:02x}")).collect()))
        .collect();
    lines.sort();
    for (k, v) in lines {
        println!("{k}\t{v}");
    }
    Ok(())
}

async fn write(session: &VortexSession, path: &str) -> VortexResult<()> {
    let ids = Buffer::from((0..1000i64).collect::<Vec<_>>()).into_array();
    let rows = StructArray::from_fields(&[("id", ids)])?.into_array();
    let mut out = ByteBufferMut::empty();
    session
        .write_options()
        .with_metadata_segments(entries().into_iter().map(|(k, v)| (k, ByteBuffer::copy_from(v))))
        .write(&mut out, rows.to_array_stream())
        .await?;
    std::fs::write(path, out.as_slice())?;
    Ok(())
}

#[tokio::main]
async fn main() {
    let args: Vec<String> = env::args().collect();
    let (mode, path) = match args.as_slice() {
        [_, mode, path] => (mode.as_str(), path.as_str()),
        _ => panic!("usage: filemeta read|write file.vortex"),
    };
    let session = VortexSession::default();
    match mode {
        "read" => read(&session, path).await.expect("read"),
        "write" => {
            write(&session, path).await.expect("write");
            read(&session, path).await.expect("read");
        }
        _ => panic!("usage: filemeta read|write file.vortex"),
    }
}
