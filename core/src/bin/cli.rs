//! Headless Nalarupa: render/export documents, run execute scripts, serve MCP.
//!   nalarupa-cli render <doc.pen> <out-dir> [scale]   one PNG per top-level node
//!   nalarupa-cli svg <doc.pen> <node-id> <out.svg>
//!   nalarupa-cli html <doc.pen> <node-id> <out.html>
//!   nalarupa-cli exec <doc.pen> <script.js> [--save]
//!   nalarupa-cli mcp                                    serve MCP until Ctrl+C

use nalarupa_core::engine::engine;
use serde_json::json;
use std::path::Path;

fn main() {
    let args: Vec<String> = std::env::args().collect();
    if let Err(e) = run(&args) {
        eprintln!("error: {e}");
        std::process::exit(1);
    }
}

fn run(args: &[String]) -> Result<(), String> {
    let arg = |i: usize| args.get(i).map(String::as_str).ok_or_else(|| "missing argument (see --help in src/bin/cli.rs)".to_string());
    match arg(1)? {
        "render" => {
            let mut e = engine();
            e.open(Path::new(arg(2)?))?;
            let out = Path::new(arg(3)?);
            std::fs::create_dir_all(out).map_err(|e| e.to_string())?;
            let scale: f64 = args.get(4).and_then(|s| s.parse().ok()).unwrap_or(1.0);
            let ids = e.select_top_level();
            for id in ids {
                let t = std::time::Instant::now();
                let png = e.ctx().node_png(&id, scale)?;
                std::fs::write(out.join(format!("{id}.png")), png).map_err(|e| e.to_string())?;
                println!("{id}.png  {:?}", t.elapsed());
            }
            Ok(())
        }
        "svg" => {
            let mut e = engine();
            e.open(Path::new(arg(2)?))?;
            let (svg, embedded) = e.ctx().export_svg(arg(3)?).ok_or("node not found")?;
            drop(e);
            std::fs::write(arg(4)?, nalarupa_core::svg::inline_embedded(&svg, &embedded)).map_err(|e| e.to_string())
        }
        "html" => {
            let mut e = engine();
            e.open(Path::new(arg(2)?))?;
            let html = nalarupa_core::html::html_document(&mut e, &[arg(3)?.to_string()], &json!({}))?;
            std::fs::write(arg(4)?, html).map_err(|e| e.to_string())
        }
        "exec" => {
            let mut e = engine();
            let doc = Path::new(arg(2)?);
            if doc.exists() {
                e.open(doc)?;
            } else {
                e.load_template("empty");
            }
            let src = std::fs::read_to_string(arg(3)?).map_err(|e| e.to_string())?;
            let out = e.tool("execute", &json!({"input": src}));
            drop(e);
            let out = out.finish();
            let mut e = engine();
            println!("{}", out.text);
            for (i, img) in out.images.iter().enumerate() {
                let bytes = base64::Engine::decode(&base64::engine::general_purpose::STANDARD, img).map_err(|e| e.to_string())?;
                let p = format!("shot-{i}.png");
                std::fs::write(&p, bytes).map_err(|e| e.to_string())?;
                println!("screenshot -> {p}");
            }
            if args.iter().any(|a| a == "--save") && !out.error {
                println!("saved {}", e.save(Some(arg(2)?))?);
            }
            if out.error { Err("execute failed".into()) } else { Ok(()) }
        }
        "mcp" => {
            let port = nalarupa_core::mcp::start()?;
            engine().mcp_port = port;
            println!("Nalarupa MCP at {}", nalarupa_core::mcp::url(port));
            loop {
                std::thread::park();
            }
        }
        other => Err(format!("unknown command '{other}'")),
    }
}
