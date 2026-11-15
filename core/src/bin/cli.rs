//! Headless Nalarupa: render/export documents, run execute scripts, serve MCP.
//!   nalarupa-cli render <doc.pen> <out-dir> [scale]   one PNG per top-level node
//!   nalarupa-cli svg <doc.pen> <node-id> <out.svg>
//!   nalarupa-cli html <doc.pen> <node-id> <out.html>
//!   nalarupa-cli exec <doc.pen> <script.js> [--save]
//!   nalarupa-cli mcp                                    serve MCP until Ctrl+C
//!   nalarupa-cli layout <doc.pen> [<saved.pen>]         laid-out tree of every top-level node as JSON
//!                                                       (the Go port's parity oracle); saves the opened document
//!   nalarupa-cli measure <cases.json> [<font-cache>]    metrics, width and wrapped lines of each {text, style, maxW?}
//!                                                       (the Go port's text oracle), offline

use nalarupa_core::engine::engine;
use nalarupa_core::resolve::RNode;
use serde_json::{json, Value};
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
        "layout" => {
            // Opened as `Engine::open` does (migrate, repair ids), without imports, recents or the data folder.
            let mut doc = nalarupa_core::doc::Doc::load(Path::new(arg(2)?))?;
            nalarupa_core::resolve::migrate(&mut doc.root);
            doc.ensure_ids();
            if let Some(out) = args.get(3) {
                doc.save(Path::new(out))?;
            }
            let empty = serde_json::Map::new();
            let r = nalarupa_core::resolve::Resolver::new(&doc.root, &[], &empty);
            let mut fonts = nalarupa_core::text::Fonts::new(std::env::temp_dir().join("nalarupa-cli-fonts"));
            fonts.online = false;
            let trees: Vec<Value> = doc.children().iter().filter_map(|n| r.resolve(n)).map(|mut n| {
                nalarupa_core::layout::layout(&mut n, &mut fonts);
                dump(&n)
            }).collect();
            println!("{}", serde_json::to_string_pretty(&trees).map_err(|e| e.to_string())?);
            Ok(())
        }
        "measure" => {
            // No maxW wraps at an infinite width (only `\n` breaks).
            let src = std::fs::read_to_string(arg(2)?).map_err(|e| e.to_string())?;
            let cases: Vec<Value> = serde_json::from_str(&src).map_err(|e| e.to_string())?;
            let cache = args.get(3).map(std::path::PathBuf::from).unwrap_or_else(|| std::env::temp_dir().join("nalarupa-cli-fonts"));
            let mut fonts = nalarupa_core::text::Fonts::new(cache);
            fonts.online = false;
            let out: Vec<Value> = cases.iter().map(|c| {
                let s = &c["style"];
                let st = nalarupa_core::text::TextStyle {
                    family: s["family"].as_str().unwrap_or("Inter").to_string(),
                    size: s["size"].as_f64().unwrap_or(14.0),
                    weight: s["weight"].as_u64().unwrap_or(400) as u16,
                    italic: s["italic"].as_bool().unwrap_or(false),
                    letter_spacing: s["letterSpacing"].as_f64().unwrap_or(0.0),
                    line_height: s["lineHeight"].as_f64(),
                };
                let text = c["text"].as_str().unwrap_or("");
                let m = fonts.metrics(&st);
                let lines = fonts.wrap(text, c["maxW"].as_f64().unwrap_or(f64::INFINITY), &st);
                json!({
                    "metrics": {"ascent": m.ascent, "descent": m.descent, "lineH": m.line_h, "baseline": m.baseline},
                    "width": fonts.width(text, &st),
                    "lines": lines.iter().map(|l| json!({"text": l.text, "width": l.width, "soft": l.soft})).collect::<Vec<_>>(),
                })
            }).collect();
            println!("{}", serde_json::to_string_pretty(&out).map_err(|e| e.to_string())?);
            Ok(())
        }
        other => Err(format!("unknown command '{other}'")),
    }
}

fn dump(n: &RNode) -> Value {
    json!({"id": n.id, "type": n.ty, "x": n.x, "y": n.y, "w": n.w, "h": n.h, "problem": n.problem,
           "children": n.kids.iter().map(dump).collect::<Vec<_>>()})
}
