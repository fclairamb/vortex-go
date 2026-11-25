//! Runs the cases of bench/cases.json with the Rust reference and prints one JSON line per
//! case and thread count: {"case", "threads", "ns_per_op", "rows_per_s", "mb_per_s"}.
//!
//!     cargo run --release -- --cases ../../bench/cases.json --root ../.. --runs 5 --threads 1,8
//!
//! Every case is run `runs` times after one warm-up run (the page cache is warm, like the Go
//! benchmarks); the lines of a case are all printed, tools benchjson keeps the median.

use std::env;
use std::fs;
use std::path::{Path, PathBuf};
use std::time::Instant;

use futures::StreamExt;
use serde_json::Value;
use vortex::VortexSessionDefault;
use vortex::expr::{col, lit, lt, root, select};
use vortex::array::stream::ArrayStreamAdapter;
use vortex::array::{ArrayRef, IntoArray, RecursiveCanonical, VortexSessionExecute};
use vortex::buffer::ByteBufferMut;
use vortex::compressor::BtrBlocksCompressorBuilder;
use vortex::error::VortexResult;
use vortex::file::{OpenOptionsSessionExt, WriteOptionsSessionExt, WriteStrategyBuilder};
use vortex::session::VortexSession;

/// The rows a write case writes: the first rows of the data file, like the Go benchmarks.
const WRITE_ROWS: u64 = 1 << 20;

struct Args {
    cases: PathBuf,
    root: PathBuf,
    runs: usize,
    threads: Vec<usize>,
}

fn parse_args() -> Args {
    let mut a = Args {
        cases: PathBuf::from("bench/cases.json"),
        root: PathBuf::from("."),
        runs: 5,
        threads: vec![1],
    };
    let mut it = env::args().skip(1);
    while let Some(k) = it.next() {
        let v = it.next().unwrap_or_else(|| panic!("{k} needs a value"));
        match k.as_str() {
            "--cases" => a.cases = PathBuf::from(v),
            "--root" => a.root = PathBuf::from(v),
            "--runs" => a.runs = v.parse().expect("--runs"),
            "--threads" => a.threads = v.split(',').map(|t| t.parse().expect("--threads")).collect(),
            _ => panic!("unknown flag {k}"),
        }
    }
    a
}

/// The data file of a data entry: {version} is .vortex-version, {rows} the row count of the
/// generated file (VORTEX_BENCH_ROWS, 10 million by default).
fn data_path(root: &Path, file: &str) -> PathBuf {
    let version = fs::read_to_string(root.join(".vortex-version")).expect(".vortex-version");
    let rows = env::var("VORTEX_BENCH_ROWS")
        .ok()
        .and_then(|v| v.parse::<u64>().ok())
        .filter(|v| *v > 0)
        .unwrap_or(10_000_000);
    root.join(
        file.replace("{version}", version.trim())
            .replace("{rows}", &rows.to_string()),
    )
}

fn literal(kind: &str, v: i64) -> vortex::expr::Expression {
    match kind {
        "i32" => lit(v as i32),
        "i64" => lit(v),
        _ => panic!("literal type {kind}"),
    }
}

/// What one run of a case did: the rows it read or wrote, and the decoded bytes.
struct Done {
    rows: u64,
    bytes: u64,
}

async fn canonical_bytes(array: ArrayRef, session: &VortexSession) -> VortexResult<u64> {
    let mut ctx = session.create_execution_ctx();
    let c = array.execute::<RecursiveCanonical>(&mut ctx)?;
    Ok(c.0.into_array().nbytes())
}

async fn scan(
    session: &VortexSession,
    path: &Path,
    threads: usize,
    projection: Option<Vec<String>>,
    filter: Option<(&str, &str, i64)>,
    range: Option<(f64, f64)>,
) -> VortexResult<Done> {
    let file = session.open_options().open_path(path).await?;
    let mut b = file.scan()?.with_concurrency(threads);
    if let Some(cols) = projection {
        let names: Vec<&str> = cols.iter().map(String::as_str).collect();
        let e = select(names, root()).bind(file.dtype())?.optimize_recursive()?;
        b = b.with_projection(e);
    }
    if let Some((column, kind, v)) = filter {
        let e = lt(col(column), literal(kind, v))
            .bind(file.dtype())?
            .optimize_recursive()?;
        b = b.with_filter(e);
    }
    if let Some((from, to)) = range {
        let n = file.row_count() as f64;
        b = b.with_row_range((n * from) as u64..(n * to) as u64);
    }
    let mut stream = b.into_array_stream()?;
    let mut done = Done { rows: 0, bytes: 0 };
    while let Some(chunk) = stream.next().await {
        let chunk = chunk?;
        done.rows += chunk.len() as u64;
        done.bytes += canonical_bytes(chunk, session).await?;
    }
    Ok(done)
}

/// The chunks a write case writes: the first rows of the data file, decoded.
async fn write_input(session: &VortexSession, path: &Path) -> VortexResult<(Vec<ArrayRef>, StructArrayDType)> {
    let file = session.open_options().open_path(path).await?;
    let dtype = file.dtype().clone();
    let n = file.row_count().min(WRITE_ROWS);
    let mut stream = file.scan()?.with_row_range(0..n).into_array_stream()?;
    let mut chunks = Vec::new();
    while let Some(chunk) = stream.next().await {
        let mut ctx = session.create_execution_ctx();
        chunks.push(chunk?.execute::<RecursiveCanonical>(&mut ctx)?.0.into_array());
    }
    Ok((chunks, dtype))
}

type StructArrayDType = vortex::dtype::DType;

async fn write(
    session: &VortexSession,
    chunks: &[ArrayRef],
    dtype: &StructArrayDType,
    mode: &str,
) -> VortexResult<Done> {
    let strategy = match mode {
        "default" => WriteStrategyBuilder::from_session(session).build(),
        "compact" => WriteStrategyBuilder::from_session(session)
            .with_btrblocks_builder(BtrBlocksCompressorBuilder::from_session(session).with_compact())
            .build(),
        // No compression: the compressor has no scheme, so every column is written as it is.
        "none" => WriteStrategyBuilder::from_session(session)
            .with_btrblocks_builder(BtrBlocksCompressorBuilder::empty())
            .build(),
        _ => panic!("compression {mode}"),
    };
    let mut out = ByteBufferMut::empty();
    let stream = ArrayStreamAdapter::new(
        dtype.clone(),
        futures::stream::iter(chunks.to_vec().into_iter().map(Ok)),
    );
    session
        .write_options()
        .with_strategy(strategy)
        .write(&mut out, stream)
        .await?;
    let rows = chunks.iter().map(|c| c.len() as u64).sum();
    Ok(Done { rows, bytes: out.len() as u64 })
}

fn str_list(v: &Value) -> Vec<String> {
    v.as_array()
        .expect("an array")
        .iter()
        .map(|s| s.as_str().expect("a string").to_string())
        .collect()
}

fn main() {
    let args = parse_args();
    let spec: Value = serde_json::from_slice(&fs::read(&args.cases).expect("cases file")).expect("cases JSON");
    for &threads in &args.threads {
        let rt = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(threads)
            .enable_all()
            .build()
            .expect("runtime");
        rt.block_on(run(&args, &spec, threads));
    }
}

async fn run(args: &Args, spec: &Value, threads: usize) {
    let session = VortexSession::default();
    for case in spec["cases"].as_array().expect("cases") {
        let name = case["name"].as_str().expect("name");
        let data = &spec["data"][case["data"].as_str().expect("data")];
        let path = data_path(&args.root, data["file"].as_str().expect("file"));
        if !path.exists() {
            eprintln!("skip {name}: {} does not exist", path.display());
            continue;
        }
        let op = case["op"].as_str().expect("op");
        let project = || Some(str_list(&data["project"]));
        let filter = || {
            let f = &data["filter"];
            (f["column"].as_str().unwrap(), f["type"].as_str().unwrap(), f["lt"].as_i64().unwrap())
        };
        let range = || (data["range"][0].as_f64().unwrap(), data["range"][1].as_f64().unwrap());

        let input = if op.starts_with("write_") {
            Some(write_input(&session, &path).await.expect("write input"))
        } else {
            None
        };
        let mut times = Vec::new();
        let mut last = Done { rows: 0, bytes: 0 };
        for run in 0..=args.runs {
            let t = Instant::now();
            let done = match op {
                "open" => session
                    .open_options()
                    .open_path(&path)
                    .await
                    .map(|_| Done { rows: 1, bytes: 0 }),
                "scan_full" => scan(&session, &path, threads, None, None, None).await,
                "scan_3col" => scan(&session, &path, threads, project(), None, None).await,
                "scan_filter" => scan(&session, &path, threads, project(), Some(filter()), None).await,
                "scan_range" => scan(&session, &path, threads, None, None, Some(range())).await,
                _ if op.starts_with("write_") => {
                    let (chunks, dtype) = input.as_ref().unwrap();
                    write(&session, chunks, dtype, &op["write_".len()..]).await
                }
                _ => panic!("operation {op}"),
            }
            .unwrap_or_else(|e| panic!("{name}: {e}"));
            let ns = t.elapsed().as_nanos() as f64;
            if run == 0 {
                continue; // the warm-up
            }
            times.push(ns);
            last = done;
        }
        for ns in times {
            let s = ns / 1e9;
            println!(
                "{}",
                serde_json::json!({
                    "case": name,
                    "threads": threads,
                    "ns_per_op": ns,
                    "rows_per_s": last.rows as f64 / s,
                    "mb_per_s": last.bytes as f64 / s / 1e6,
                })
            );
        }
    }
}
