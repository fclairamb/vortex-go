//! Prints each row of the Variant column "v" of a Vortex file as a line of JSON: `null` for a
//! null row.
//!
//!     cargo run --release -- file.vortex

use std::env;

use vortex::arrow::ArrowSessionExt;
use futures::StreamExt;
use parquet_variant_compute::VariantArray;
use parquet_variant_json::VariantToJson;
use vortex::VortexSessionDefault;
use vortex::array::VortexSessionExecute;
use vortex::array::arrays::struct_::StructArrayExt;
use vortex::error::VortexResult;
use vortex::file::OpenOptionsSessionExt;
use vortex::session::VortexSession;

async fn run(path: &str) -> VortexResult<()> {
    let session = VortexSession::default();
    vortex_parquet_variant::initialize(&session);
    let file = session.open_options().open_path(path).await?;
    let mut stream = file.scan()?.into_array_stream()?;
    while let Some(chunk) = stream.next().await {
        let chunk = chunk?;
        let mut ctx = session.create_execution_ctx();
        let column = chunk
            .as_opt::<vortex::array::arrays::Struct>()
            .expect("a struct")
            .unmasked_field_by_name("v")?
            .clone();
        let arrow = session.arrow().execute_arrow(column, None, &mut ctx)?;
        let variants = VariantArray::try_new(arrow.as_ref()).expect("a variant array");
        for i in 0..variants.len() {
            if variants.is_null(i) {
                println!("null");
            } else {
                println!("{}", variants.value(i).to_json_value().expect("json"));
            }
        }
    }
    Ok(())
}

#[tokio::main]
async fn main() {
    let path = env::args().nth(1).expect("usage: variantjson file.vortex");
    run(&path).await.expect("read");
}
