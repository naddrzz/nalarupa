//! Headless Nalarupa: render/export documents, run execute scripts, serve MCP.
//!   nalarupa-cli render <doc.pen> <out-dir> [scale] [--offline]   one PNG per top-level node
//!   nalarupa-cli svg <doc.pen> <node-id> <out.svg> [--offline]
//!   nalarupa-cli html <doc.pen> <node-id> <out.html>
//!   nalarupa-cli exec <doc.pen> <script.js> [--save]
//!   nalarupa-cli mcp                                    serve MCP until Ctrl+C
//!   nalarupa-cli layout <doc.pen> [<saved.pen>] [--scripts]
//!                                                       laid-out tree of every top-level node as JSON
//!                                                       (the Go port's parity oracle); saves the opened document;
//!                                                       `--scripts` runs script nodes and dumps their status
//!   nalarupa-cli measure <cases.json> [<font-cache>]    metrics, width and wrapped lines of each {text, style, maxW?}
//!                                                       (the Go port's text oracle), offline
//!   nalarupa-cli skia-path <cases.json>                 tiny-skia path geometry of each case (build, transform,
//!                                                       dash, stroke) with f32 values as bit patterns (the Go
//!                                                       port's path oracle, see skia_path below)
//!   nalarupa-cli skia-raster <scenes.json> <out-dir>    tiny-skia pixels of each scene (pixmap and mask ops) as raw
//!                                                       bytes plus index.json (the Go port's raster oracle, see
//!                                                       skia_raster below)
//!   nalarupa-cli usvg-tree <file.svg> [--resources <dir>] [--fonts <cache-dir>]
//!                                                       the usvg tree of an SVG as JSON with f32 values as bit
//!                                                       patterns (the Go port's usvg oracle, see usvg_tree below)
//!   nalarupa-cli resvg <file.svg> <out.rgba> [--fonts <cache-dir>] [--scale <s>]
//!                                                       the SVG rendered by resvg as premultiplied RGBA bytes; prints
//!                                                       {"width","height"} or {"error"} (the Go port's render oracle,
//!                                                       see resvg_render below)
//!   nalarupa-cli image <doc.pen> <node-id> <png|jpeg|webp> <scale> <quality> <out> [--offline]
//!                                                       `node_image` of one node (the Go port's export oracle)
//!   nalarupa-cli pdf <doc.pen> <out.pdf> <node-id>... [--offline]
//!                                                       `pdf_trees` then `encode_pdf`: one page per node (the Go
//!                                                       port's PDF oracle)
//!   nalarupa-cli pdf-svg <file.svg> <out.pdf> [--fonts <cache-dir>]
//!                                                       `encode_pdf` of one SVG file's usvg tree, with the options of
//!                                                       `resvg` (the Go port's PDF text oracle)
//!   nalarupa-cli canvas <doc.pen> <scenario.json> <out-dir>
//!                                                       replays canvas steps on a resvg `Renderer` (offline, pinned
//!                                                       clock): each render's RGB bytes as <step>.rgb plus log.json
//!                                                       (the Go port's canvas oracle, see canvas below)

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
            // `--offline`: no font or asset downloads (the Go port's export parity oracle).
            if args.iter().skip(4).any(|a| a == "--offline") {
                e.offline();
            }
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
            // `--offline`: no font or asset downloads (the Go port's svg parity oracle).
            if args.iter().skip(5).any(|a| a == "--offline") {
                e.offline();
            }
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
            // `--scripts`: script nodes run (waiting for each, as an export does) and their `__status` is dumped as
            // "status" (the Go port's script-node oracle).
            let scripts = args.iter().skip(3).any(|a| a == "--scripts");
            let mut doc = nalarupa_core::doc::Doc::load(Path::new(arg(2)?))?;
            let (dir, path) = (doc.dir(), doc.path.clone());
            nalarupa_core::resolve::migrate(&mut doc.root);
            doc.ensure_ids();
            if let Some(out) = args.get(3).filter(|a| *a != "--scripts") {
                doc.save(Path::new(out))?;
            }
            let empty = serde_json::Map::new();
            let r = nalarupa_core::resolve::Resolver::new(&doc.root, &[], &empty);
            let mut fonts = nalarupa_core::text::Fonts::new(std::env::temp_dir().join("nalarupa-cli-fonts"));
            fonts.online = false;
            let trees: Vec<Value> = doc.children().iter().filter_map(|n| r.resolve(n)).map(|mut n| {
                nalarupa_core::layout::layout(&mut n, &mut fonts);
                if scripts {
                    nalarupa_core::script::expand_scripts(&mut n, &r, &mut fonts, dir.as_deref(), path.as_deref(), false);
                }
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
        "skia-path" => {
            let src = std::fs::read_to_string(arg(2)?).map_err(|e| e.to_string())?;
            let cases: Vec<Value> = serde_json::from_str(&src).map_err(|e| e.to_string())?;
            let out = cases.iter().map(skia_path::case).collect::<Result<Vec<_>, _>>()?;
            println!("{}", serde_json::to_string_pretty(&out).map_err(|e| e.to_string())?);
            Ok(())
        }
        "skia-raster" => skia_raster::run(arg(2)?, arg(3)?),
        "usvg-tree" => {
            let resources = args.iter().position(|a| a == "--resources").map(|i| arg(i + 1)).transpose()?;
            let fonts = args.iter().position(|a| a == "--fonts").map(|i| arg(i + 1)).transpose()?;
            usvg_tree::run(arg(2)?, resources, fonts)
        }
        "resvg" => {
            let fonts = args.iter().position(|a| a == "--fonts").map(|i| arg(i + 1)).transpose()?;
            let scale = args.iter().position(|a| a == "--scale").map(|i| arg(i + 1)).transpose()?;
            let scale: f32 = scale.map_or(Ok(1.0), |s| s.parse().map_err(|_| format!("bad scale {s}")))?;
            resvg_render::run(arg(2)?, arg(3)?, fonts, scale)
        }
        "image" => {
            let mut e = engine();
            if args.iter().skip(8).any(|a| a == "--offline") {
                e.offline();
            }
            e.open(Path::new(arg(2)?))?;
            let scale: f64 = arg(5)?.parse().map_err(|_| "bad scale".to_string())?;
            let quality: u8 = arg(6)?.parse().map_err(|_| "bad quality".to_string())?;
            // `node_image`, with the engine released before rasterising.
            let scale = scale.clamp(0.05, 8.0);
            let tree = e.ctx().node_tree_at(arg(3)?, scale)?;
            drop(e);
            let bytes = nalarupa_core::render::encode_tree(&tree, arg(4)?, scale, quality)?;
            std::fs::write(arg(7)?, bytes).map_err(|e| e.to_string())
        }
        "pdf" => {
            let mut e = engine();
            if args.iter().skip(4).any(|a| a == "--offline") {
                e.offline();
            }
            e.open(Path::new(arg(2)?))?;
            let ids: Vec<String> = args.iter().skip(4).filter(|a| *a != "--offline").cloned().collect();
            if ids.is_empty() {
                return Err("missing node id".into());
            }
            // `pdf_trees` under the engine, `encode_pdf` after releasing it (as html.rs does).
            let trees = e.ctx().pdf_trees(&ids)?;
            drop(e);
            let bytes = nalarupa_core::render::encode_pdf(&trees)?;
            std::fs::write(arg(3)?, bytes).map_err(|e| e.to_string())
        }
        "pdf-svg" => {
            let fonts = args.iter().position(|a| a == "--fonts").map(|i| arg(i + 1)).transpose()?;
            let tree = resvg_render::tree(arg(2)?, fonts)?;
            let bytes = nalarupa_core::render::encode_pdf(&[tree])?;
            std::fs::write(arg(3)?, bytes).map_err(|e| e.to_string())
        }
        "canvas" => canvas::run(arg(2)?, arg(3)?, arg(4)?),
        other => Err(format!("unknown command '{other}'")),
    }
}

fn dump(n: &RNode) -> Value {
    let mut v = json!({"id": n.id, "type": n.ty, "x": n.x, "y": n.y, "w": n.w, "h": n.h, "problem": n.problem,
           "children": n.kids.iter().map(dump).collect::<Vec<_>>()});
    if let Some(status) = n.p.get("__status") {
        v["status"] = status.clone();
    }
    v
}

/// The Go port's tiny-skia path oracle. Each case is an object:
///   "path": [op...]       builder ops: ["M",x,y] ["L",x,y] ["Q",x1,y1,x,y] ["C",x1,y1,x2,y2,x,y] ["Z"]
///                         ["rect",l,t,r,b] ["oval",l,t,r,b] ["circle",cx,cy,r] ["path",[op...]] (push_*)
///   "shape": [kind,...]   or a constructor instead: ["rect",l,t,r,b] ["oval",l,t,r,b] ["circle",cx,cy,r]
///   "transform": [op...]  from identity: ["row",sx,ky,kx,sy,tx,ty] ["translate"|"scale"|"skew",a,b] ["rotate",deg]
///                         ["rotateAt",deg,x,y], pre/post ops ["preScale",sx,sy] ["postTranslate",tx,ty]
///                         ["preRotate",deg] ["postRotateAt",deg,x,y] ["preConcat",6 values] ..., ["invert"]
///   "mapRect": [l,t,r,b]  a rect mapped by the transform
///   "dash": {"array": [...], "offset": n, "resScale": n}
///   "stroke": {"width": n, "miterLimit": n, "cap": "butt|round|square", "join": "miter|miter-clip|round|bevel",
///              "resScale": n}
/// Numbers are JSON numbers or strings: "nan", "inf", "-inf", or "0x..." (f32 bits). Stages run in order
/// (built, transformed, dashed, stroked), each on the previous result; a requested stage is null where Rust
/// returns None. Output f32 values are their bit patterns.
mod skia_path {
    use resvg::tiny_skia::{LineCap, LineJoin, Path, PathBuilder, PathSegment, PathStroker, Rect, Stroke, StrokeDash, Transform};
    use serde_json::{json, Map, Value};

    pub(super) fn num(v: &Value) -> Result<f32, String> {
        match v {
            Value::Number(n) => n.as_f64().map(|n| n as f32).ok_or_else(|| format!("bad number {n}")),
            Value::String(s) => match s.as_str() {
                "nan" => Ok(f32::NAN),
                "inf" => Ok(f32::INFINITY),
                "-inf" => Ok(f32::NEG_INFINITY),
                _ => s.strip_prefix("0x").and_then(|h| u32::from_str_radix(h, 16).ok()).map(f32::from_bits).ok_or_else(|| format!("bad number {s}")),
            },
            _ => Err(format!("bad number {v}")),
        }
    }

    pub(super) fn nums(v: &[Value]) -> Result<Vec<f32>, String> {
        v.iter().map(num).collect()
    }

    /// An op `[name, args...]`: the name and, when `numeric`, the args as numbers.
    fn op(v: &Value, numeric: bool) -> Result<(&str, Vec<f32>), String> {
        let a = v.as_array().filter(|a| !a.is_empty()).ok_or_else(|| format!("bad op {v}"))?;
        let name = a[0].as_str().ok_or_else(|| format!("bad op {v}"))?;
        Ok((name, if numeric { nums(&a[1..])? } else { Vec::new() }))
    }

    pub(super) fn list(v: &Value) -> Result<&Vec<Value>, String> {
        v.as_array().ok_or_else(|| format!("expected a list: {v}"))
    }

    pub(super) fn build(ops: &Value) -> Result<Option<Path>, String> {
        let mut pb = PathBuilder::new();
        for o in list(ops)? {
            if op(o, false)?.0 == "path" {
                if let Some(p) = build(o.get(1).ok_or("a path op needs ops")?)? {
                    pb.push_path(&p);
                }
                continue;
            }
            let (name, v) = op(o, true)?;
            match (name, v.as_slice()) {
                ("M", &[x, y]) => pb.move_to(x, y),
                ("L", &[x, y]) => pb.line_to(x, y),
                ("Q", &[x1, y1, x, y]) => pb.quad_to(x1, y1, x, y),
                ("C", &[x1, y1, x2, y2, x, y]) => pb.cubic_to(x1, y1, x2, y2, x, y),
                ("Z", &[]) => pb.close(),
                ("rect", &[l, t, r, b]) => {
                    if let Some(r) = Rect::from_ltrb(l, t, r, b) {
                        pb.push_rect(r)
                    }
                }
                ("oval", &[l, t, r, b]) => {
                    if let Some(r) = Rect::from_ltrb(l, t, r, b) {
                        pb.push_oval(r)
                    }
                }
                ("circle", &[x, y, r]) => pb.push_circle(x, y, r),
                _ => return Err(format!("bad path op {o}")),
            }
        }
        Ok(pb.finish())
    }

    fn shape(v: &Value) -> Result<Option<Path>, String> {
        let (name, v) = op(v, true)?;
        Ok(match (name, v.as_slice()) {
            ("rect", &[l, t, r, b]) => Rect::from_ltrb(l, t, r, b).map(PathBuilder::from_rect),
            ("oval", &[l, t, r, b]) => Rect::from_ltrb(l, t, r, b).and_then(PathBuilder::from_oval),
            ("circle", &[x, y, r]) => PathBuilder::from_circle(x, y, r),
            _ => return Err(format!("bad shape {name}")),
        })
    }

    fn transform(ops: &Value) -> Result<Option<Transform>, String> {
        let mut ts = Transform::identity();
        for o in list(ops)? {
            let (name, v) = op(o, true)?;
            ts = match (name, v.as_slice()) {
                ("row", &[a, b, c, d, e, f]) => Transform::from_row(a, b, c, d, e, f),
                ("translate", &[x, y]) => Transform::from_translate(x, y),
                ("scale", &[x, y]) => Transform::from_scale(x, y),
                ("skew", &[x, y]) => Transform::from_skew(x, y),
                ("rotate", &[a]) => Transform::from_rotate(a),
                ("rotateAt", &[a, x, y]) => Transform::from_rotate_at(a, x, y),
                ("preScale", &[x, y]) => ts.pre_scale(x, y),
                ("postScale", &[x, y]) => ts.post_scale(x, y),
                ("preTranslate", &[x, y]) => ts.pre_translate(x, y),
                ("postTranslate", &[x, y]) => ts.post_translate(x, y),
                ("preRotate", &[a]) => ts.pre_rotate(a),
                ("postRotate", &[a]) => ts.post_rotate(a),
                ("preRotateAt", &[a, x, y]) => ts.pre_rotate_at(a, x, y),
                ("postRotateAt", &[a, x, y]) => ts.post_rotate_at(a, x, y),
                ("preConcat", &[a, b, c, d, e, f]) => ts.pre_concat(Transform::from_row(a, b, c, d, e, f)),
                ("postConcat", &[a, b, c, d, e, f]) => ts.post_concat(Transform::from_row(a, b, c, d, e, f)),
                ("invert", &[]) => match ts.invert() {
                    Some(ts) => ts,
                    None => return Ok(None),
                },
                _ => return Err(format!("bad transform op {o}")),
            };
        }
        Ok(Some(ts))
    }

    fn bits(x: f32) -> Value {
        json!(x.to_bits())
    }

    fn rect(r: Rect) -> Value {
        json!([bits(r.left()), bits(r.top()), bits(r.right()), bits(r.bottom())])
    }

    fn path_json(p: &Path) -> Value {
        let segments: Vec<Value> = p.segments().map(|s| match s {
            PathSegment::MoveTo(p) => json!(["M", bits(p.x), bits(p.y)]),
            PathSegment::LineTo(p) => json!(["L", bits(p.x), bits(p.y)]),
            PathSegment::QuadTo(p1, p) => json!(["Q", bits(p1.x), bits(p1.y), bits(p.x), bits(p.y)]),
            PathSegment::CubicTo(p1, p2, p) => json!(["C", bits(p1.x), bits(p1.y), bits(p2.x), bits(p2.y), bits(p.x), bits(p.y)]),
            PathSegment::Close => json!(["Z"]),
        }).collect();
        json!({"segments": segments, "bounds": rect(p.bounds()), "tight": p.compute_tight_bounds().map(rect)})
    }

    fn stage(p: &Option<Path>) -> Value {
        p.as_ref().map_or(Value::Null, path_json)
    }

    fn opt_num(v: &Value, key: &str, default: f32) -> Result<f32, String> {
        v.get(key).map(num).transpose().map(|n| n.unwrap_or(default))
    }

    pub fn case(c: &Value) -> Result<Value, String> {
        let mut out = Map::new();
        let mut path = match (c.get("shape"), c.get("path")) {
            (Some(s), None) => shape(s)?,
            (None, Some(ops)) => build(ops)?,
            _ => return Err(format!("a case needs one of path and shape: {c}")),
        };
        out.insert("built".into(), stage(&path));
        if let Some(ops) = c.get("transform") {
            let ts = transform(ops)?;
            out.insert("transform".into(), ts.map_or(Value::Null, |t| json!([bits(t.sx), bits(t.ky), bits(t.kx), bits(t.sy), bits(t.tx), bits(t.ty)])));
            if let Some(ts) = ts {
                out.insert("resScale".into(), bits(PathStroker::compute_resolution_scale(&ts)));
                if let Some(r) = c.get("mapRect") {
                    let v = nums(list(r)?)?;
                    let &[l, t, r, b] = v.as_slice() else { return Err("mapRect needs 4 values".into()) };
                    out.insert("mappedRect".into(), Rect::from_ltrb(l, t, r, b).and_then(|r| r.transform(ts)).map_or(Value::Null, rect));
                }
            }
            path = match (path, ts) {
                (Some(p), Some(ts)) => p.transform(ts),
                _ => None,
            };
            out.insert("transformed".into(), stage(&path));
        }
        if let Some(d) = c.get("dash") {
            let dash = StrokeDash::new(nums(list(&d["array"])?)?, opt_num(d, "offset", 0.0)?);
            out.insert("dashOk".into(), json!(dash.is_some()));
            path = match (path, dash) {
                (Some(p), Some(dash)) => p.dash(&dash, opt_num(d, "resScale", 1.0)?),
                _ => None,
            };
            out.insert("dashed".into(), stage(&path));
        }
        if let Some(s) = c.get("stroke") {
            let stroke = Stroke {
                width: opt_num(s, "width", 1.0)?,
                miter_limit: opt_num(s, "miterLimit", 4.0)?,
                line_cap: match s.get("cap").and_then(Value::as_str).unwrap_or("butt") {
                    "butt" => LineCap::Butt,
                    "round" => LineCap::Round,
                    "square" => LineCap::Square,
                    other => return Err(format!("bad cap {other}")),
                },
                line_join: match s.get("join").and_then(Value::as_str).unwrap_or("miter") {
                    "miter" => LineJoin::Miter,
                    "miter-clip" => LineJoin::MiterClip,
                    "round" => LineJoin::Round,
                    "bevel" => LineJoin::Bevel,
                    other => return Err(format!("bad join {other}")),
                },
                dash: None,
            };
            let res = opt_num(s, "resScale", 1.0)?;
            path = path.and_then(|p| p.stroke(&stroke, res));
            out.insert("stroked".into(), stage(&path));
        }
        Ok(Value::Object(out))
    }
}

/// The Go port's tiny-skia raster oracle. The scenes file is a list of scenes, run in order; each is an object:
///   "name"            unique; the output file stem
///   "kind"            "pixmap" (Pixmap::new) or "mask" (Mask::new)
///   "width","height"  the size; a mask with "from": {"pixmap": name, "type": "alpha"|"luminance"} is instead
///                     Mask::from_pixmap of an earlier pixmap scene, and its size is that pixmap's
///   "ops": [op...]    run in order, each an object with "op":
///     pixmap: {"op":"fill","color":C}
///             {"op":"fill_rect","rect":[l,t,r,b],"paint":P,"transform":T,"mask":M}
///             {"op":"fill_path","path":[...],"rule":R,"paint":P,"transform":T,"mask":M}
///             {"op":"stroke_path","path":[...],"stroke":S,"paint":P,"transform":T,"mask":M}
///             {"op":"draw_pixmap","pixmap":name,"x":i,"y":i,"paint":{"opacity":n,"blend":B,"quality":Q},
///              "transform":T,"mask":M}
///             {"op":"apply_mask","mask":name}
///     mask:   {"op":"fill_path"|"intersect_path","path":[...],"rule":R,"aa":bool,"transform":T}
///             {"op":"invert"}
///   path  skia_path's builder ops: ["M",x,y] ["L",x,y] ["Q",...] ["C",...] ["Z"] ["rect",l,t,r,b] ["oval",l,t,r,b]
///         ["circle",cx,cy,r]
///   T     [sx,ky,kx,sy,tx,ty] (Transform::from_row), default identity
///   M     an earlier mask scene, default none
///   R     "winding" (default) or "evenodd"
///   S     {"width","miterLimit","cap","join"} as in skia_path, plus "dash": {"array":[...],"offset":n}; the dash
///         is StrokeDash::new's result as is, so an invalid one strokes undashed
///   P     {"shader":SH (default black), "blend":B (default "source-over"), "aa":bool (default true),
///          "hq":bool (force_hq_pipeline), "colorspace":"linear" (default)|"gamma2"|"simple-srgb"|"full-srgb"}
///   SH    {"type":"solid","color":C}
///         {"type":"linear","start":[x,y],"end":[x,y],"stops":ST,"mode":SP,"transform":T}
///         {"type":"radial","start":[x,y],"startRadius":n,"end":[x,y],"endRadius":n,"stops":ST,"mode":SP,"transform":T}
///         {"type":"sweep","center":[x,y],"startAngle":n,"endAngle":n,"stops":ST,"mode":SP,"transform":T}
///         {"type":"pattern","pixmap":name,"mode":SP,"quality":Q,"opacity":n,"transform":T}
///   C     [r,g,b,a] as u8 (Color::from_rgba8), or {"f":[r,g,b,a]} (Color::from_rgba, must be valid)
///   ST    [[pos,C]...];  SP "pad" (default)|"reflect"|"repeat";  Q "nearest" (default)|"bilinear"|"bicubic"
///   B     a BlendMode name in kebab case: "clear", "source-over", "color-dodge", "luminosity", ...
/// Numbers are as in skia_path. Other keys ("tolerance", "why": read by the Go harness) are ignored. An op is
/// skipped, as a caller would skip it, when its path, rect or shader constructor returns None (an empty path,
/// a degenerate gradient). Writes <out-dir>/<name>.rgba (pixmap.data(): premultiplied RGBA) or <name>.a8
/// (mask.data()), and <out-dir>/index.json mapping each name to {"kind","width","height"}, or to null where
/// Pixmap::new or Mask::new returns None. Scenes may only reference earlier, non-null scenes.
mod skia_raster {
    use super::skia_path::{build, list, num, nums};
    use resvg::tiny_skia::{
        BlendMode, Color, ColorSpace, FillRule, FilterQuality, GradientStop, LineCap, LineJoin, LinearGradient, Mask, MaskType, Paint,
        Pattern, Pixmap, PixmapPaint, Point, RadialGradient, Rect, Shader, SpreadMode, Stroke, StrokeDash, SweepGradient, Transform,
    };
    use serde_json::{json, Map, Value};
    use std::collections::HashMap;

    enum Out {
        Pixmap(Pixmap),
        Mask(Mask),
    }

    type Done = HashMap<String, Option<Out>>;

    const BLEND_MODES: [(&str, BlendMode); 29] = [
        ("clear", BlendMode::Clear),
        ("source", BlendMode::Source),
        ("destination", BlendMode::Destination),
        ("source-over", BlendMode::SourceOver),
        ("destination-over", BlendMode::DestinationOver),
        ("source-in", BlendMode::SourceIn),
        ("destination-in", BlendMode::DestinationIn),
        ("source-out", BlendMode::SourceOut),
        ("destination-out", BlendMode::DestinationOut),
        ("source-atop", BlendMode::SourceAtop),
        ("destination-atop", BlendMode::DestinationAtop),
        ("xor", BlendMode::Xor),
        ("plus", BlendMode::Plus),
        ("modulate", BlendMode::Modulate),
        ("screen", BlendMode::Screen),
        ("overlay", BlendMode::Overlay),
        ("darken", BlendMode::Darken),
        ("lighten", BlendMode::Lighten),
        ("color-dodge", BlendMode::ColorDodge),
        ("color-burn", BlendMode::ColorBurn),
        ("hard-light", BlendMode::HardLight),
        ("soft-light", BlendMode::SoftLight),
        ("difference", BlendMode::Difference),
        ("exclusion", BlendMode::Exclusion),
        ("multiply", BlendMode::Multiply),
        ("hue", BlendMode::Hue),
        ("saturation", BlendMode::Saturation),
        ("color", BlendMode::Color),
        ("luminosity", BlendMode::Luminosity),
    ];

    fn s<'v>(v: &'v Value, key: &str) -> Option<&'v str> {
        v.get(key).and_then(Value::as_str)
    }

    fn opt_num(v: &Value, key: &str, default: f32) -> Result<f32, String> {
        v.get(key).map(num).transpose().map(|n| n.unwrap_or(default))
    }

    fn flag(v: &Value, key: &str, default: bool) -> Result<bool, String> {
        v.get(key).map_or(Some(default), Value::as_bool).ok_or_else(|| format!("bad {key} in {v}"))
    }

    fn get<'v>(v: &'v Value, key: &str) -> Result<&'v Value, String> {
        v.get(key).ok_or_else(|| format!("missing {key} in {v}"))
    }

    fn pick<T: Copy>(v: &Value, key: &str, default: &str, names: &[(&str, T)]) -> Result<T, String> {
        let name = s(v, key).unwrap_or(default);
        names.iter().find(|n| n.0 == name).map(|n| n.1).ok_or_else(|| format!("bad {key} {name}"))
    }

    fn point(v: &Value, key: &str) -> Result<Point, String> {
        let p = nums(list(get(v, key)?)?)?;
        let &[x, y] = p.as_slice() else { return Err(format!("{key} needs 2 values")) };
        Ok(Point::from_xy(x, y))
    }

    fn transform(v: &Value) -> Result<Transform, String> {
        let Some(t) = v.get("transform") else { return Ok(Transform::identity()) };
        let t = nums(list(t)?)?;
        let &[sx, ky, kx, sy, tx, ty] = t.as_slice() else { return Err("a transform needs 6 values".into()) };
        Ok(Transform::from_row(sx, ky, kx, sy, tx, ty))
    }

    fn color(v: &Value) -> Result<Color, String> {
        if let Some(f) = v.get("f") {
            let c = nums(list(f)?)?;
            let &[r, g, b, a] = c.as_slice() else { return Err(format!("bad color {v}")) };
            return Color::from_rgba(r, g, b, a).ok_or_else(|| format!("bad color {v}"));
        }
        let c: Option<Vec<u8>> = list(v)?.iter().map(|x| x.as_u64().and_then(|x| u8::try_from(x).ok())).collect();
        let Some(&[r, g, b, a]) = c.as_deref() else { return Err(format!("bad color {v}")) };
        Ok(Color::from_rgba8(r, g, b, a))
    }

    fn spread(v: &Value) -> Result<SpreadMode, String> {
        pick(v, "mode", "pad", &[("pad", SpreadMode::Pad), ("reflect", SpreadMode::Reflect), ("repeat", SpreadMode::Repeat)])
    }

    fn quality(v: &Value) -> Result<FilterQuality, String> {
        pick(v, "quality", "nearest", &[("nearest", FilterQuality::Nearest), ("bilinear", FilterQuality::Bilinear), ("bicubic", FilterQuality::Bicubic)])
    }

    fn rule(v: &Value) -> Result<FillRule, String> {
        pick(v, "rule", "winding", &[("winding", FillRule::Winding), ("evenodd", FillRule::EvenOdd)])
    }

    fn pixmap<'a>(done: &'a Done, v: &Value) -> Result<&'a Pixmap, String> {
        let name = s(v, "pixmap").ok_or_else(|| format!("missing pixmap in {v}"))?;
        match done.get(name) {
            Some(Some(Out::Pixmap(p))) => Ok(p),
            _ => Err(format!("{name} is not an earlier, non-null pixmap scene")),
        }
    }

    fn mask<'a>(done: &'a Done, v: &Value) -> Result<Option<&'a Mask>, String> {
        let Some(name) = s(v, "mask") else { return Ok(None) };
        match done.get(name) {
            Some(Some(Out::Mask(m))) => Ok(Some(m)),
            _ => Err(format!("{name} is not an earlier, non-null mask scene")),
        }
    }

    fn stops(v: &Value) -> Result<Vec<GradientStop>, String> {
        list(get(v, "stops")?)?
            .iter()
            .map(|st| match list(st)?.as_slice() {
                [pos, c] => Ok(GradientStop::new(num(pos)?, color(c)?)),
                _ => Err(format!("a stop is [pos, color]: {st}")),
            })
            .collect()
    }

    fn shader<'a>(v: &Value, done: &'a Done) -> Result<Option<Shader<'a>>, String> {
        let ts = transform(v)?;
        Ok(match s(v, "type") {
            Some("solid") => Some(Shader::SolidColor(color(get(v, "color")?)?)),
            Some("linear") => LinearGradient::new(point(v, "start")?, point(v, "end")?, stops(v)?, spread(v)?, ts),
            Some("radial") => RadialGradient::new(
                point(v, "start")?,
                opt_num(v, "startRadius", 0.0)?,
                point(v, "end")?,
                opt_num(v, "endRadius", 0.0)?,
                stops(v)?,
                spread(v)?,
                ts,
            ),
            Some("sweep") => SweepGradient::new(point(v, "center")?, opt_num(v, "startAngle", 0.0)?, opt_num(v, "endAngle", 360.0)?, stops(v)?, spread(v)?, ts),
            Some("pattern") => Some(Pattern::new(pixmap(done, v)?.as_ref(), spread(v)?, quality(v)?, opt_num(v, "opacity", 1.0)?, ts)),
            _ => return Err(format!("bad shader {v}")),
        })
    }

    /// The op's paint, or None when its shader constructor returns None.
    fn paint<'a>(o: &Value, done: &'a Done) -> Result<Option<Paint<'a>>, String> {
        let p = o.get("paint").unwrap_or(&Value::Null);
        let shader = match p.get("shader") {
            Some(sh) => match shader(sh, done)? {
                Some(sh) => sh,
                None => return Ok(None),
            },
            None => Shader::SolidColor(Color::BLACK),
        };
        let colorspace = pick(
            p,
            "colorspace",
            "linear",
            &[("linear", ColorSpace::Linear), ("gamma2", ColorSpace::Gamma2), ("simple-srgb", ColorSpace::SimpleSRGB), ("full-srgb", ColorSpace::FullSRGBGamma)],
        )?;
        Ok(Some(Paint {
            shader,
            blend_mode: pick(p, "blend", "source-over", &BLEND_MODES)?,
            anti_alias: flag(p, "aa", true)?,
            colorspace,
            force_hq_pipeline: flag(p, "hq", false)?,
        }))
    }

    fn stroke(o: &Value) -> Result<Stroke, String> {
        let st = get(o, "stroke")?;
        let dash = match st.get("dash") {
            Some(d) => StrokeDash::new(nums(list(get(d, "array")?)?)?, opt_num(d, "offset", 0.0)?),
            None => None,
        };
        Ok(Stroke {
            width: opt_num(st, "width", 1.0)?,
            miter_limit: opt_num(st, "miterLimit", 4.0)?,
            line_cap: pick(st, "cap", "butt", &[("butt", LineCap::Butt), ("round", LineCap::Round), ("square", LineCap::Square)])?,
            line_join: pick(
                st,
                "join",
                "miter",
                &[("miter", LineJoin::Miter), ("miter-clip", LineJoin::MiterClip), ("round", LineJoin::Round), ("bevel", LineJoin::Bevel)],
            )?,
            dash,
        })
    }

    fn int(o: &Value, key: &str) -> Result<i32, String> {
        o.get(key).map_or(Some(0), |v| v.as_i64().and_then(|v| i32::try_from(v).ok())).ok_or_else(|| format!("bad {key} in {o}"))
    }

    fn pixmap_op(pm: &mut Pixmap, o: &Value, done: &Done) -> Result<(), String> {
        let ts = transform(o)?;
        let m = mask(done, o)?;
        match s(o, "op") {
            Some("fill") => pm.fill(color(get(o, "color")?)?),
            Some("fill_rect") => {
                let r = nums(list(get(o, "rect")?)?)?;
                let &[l, t, r, b] = r.as_slice() else { return Err("a rect needs 4 values".into()) };
                if let (Some(rect), Some(paint)) = (Rect::from_ltrb(l, t, r, b), paint(o, done)?) {
                    pm.fill_rect(rect, &paint, ts, m);
                }
            }
            Some("fill_path") => {
                if let (Some(path), Some(paint)) = (build(get(o, "path")?)?, paint(o, done)?) {
                    pm.fill_path(&path, &paint, rule(o)?, ts, m);
                }
            }
            Some("stroke_path") => {
                if let (Some(path), Some(paint)) = (build(get(o, "path")?)?, paint(o, done)?) {
                    pm.stroke_path(&path, &paint, &stroke(o)?, ts, m);
                }
            }
            Some("draw_pixmap") => {
                let p = o.get("paint").unwrap_or(&Value::Null);
                let paint = PixmapPaint { opacity: opt_num(p, "opacity", 1.0)?, blend_mode: pick(p, "blend", "source-over", &BLEND_MODES)?, quality: quality(p)? };
                pm.draw_pixmap(int(o, "x")?, int(o, "y")?, pixmap(done, o)?.as_ref(), &paint, ts, m);
            }
            Some("apply_mask") => pm.apply_mask(m.ok_or("apply_mask needs a mask")?),
            _ => return Err(format!("bad pixmap op {o}")),
        }
        Ok(())
    }

    fn mask_op(mk: &mut Mask, o: &Value) -> Result<(), String> {
        match s(o, "op") {
            Some("invert") => mk.invert(),
            Some(name @ ("fill_path" | "intersect_path")) => {
                if let Some(path) = build(get(o, "path")?)? {
                    let (rule, aa, ts) = (rule(o)?, flag(o, "aa", true)?, transform(o)?);
                    if name == "fill_path" {
                        mk.fill_path(&path, rule, aa, ts);
                    } else {
                        mk.intersect_path(&path, rule, aa, ts);
                    }
                }
            }
            _ => return Err(format!("bad mask op {o}")),
        }
        Ok(())
    }

    fn size(sc: &Value, key: &str) -> Result<u32, String> {
        sc.get(key).and_then(Value::as_u64).and_then(|v| u32::try_from(v).ok()).ok_or_else(|| format!("bad {key} in scene {sc}"))
    }

    pub fn run(scenes: &str, out: &str) -> Result<(), String> {
        let src = std::fs::read_to_string(scenes).map_err(|e| e.to_string())?;
        let scenes: Vec<Value> = serde_json::from_str(&src).map_err(|e| e.to_string())?;
        let out = std::path::Path::new(out);
        std::fs::create_dir_all(out).map_err(|e| e.to_string())?;
        let mut done = Done::new();
        let mut index = Map::new();
        for sc in &scenes {
            let name = s(sc, "name").ok_or_else(|| format!("a scene needs a name: {sc}"))?;
            if done.contains_key(name) {
                return Err(format!("duplicate scene {name}"));
            }
            let ops: &[Value] = match sc.get("ops") {
                Some(ops) => list(ops)?,
                None => &[],
            };
            let result = match s(sc, "kind") {
                Some("pixmap") => match Pixmap::new(size(sc, "width")?, size(sc, "height")?) {
                    Some(mut pm) => {
                        for o in ops {
                            pixmap_op(&mut pm, o, &done)?;
                        }
                        std::fs::write(out.join(format!("{name}.rgba")), pm.data()).map_err(|e| e.to_string())?;
                        Some(Out::Pixmap(pm))
                    }
                    None => None,
                },
                Some("mask") => {
                    let mk = match sc.get("from") {
                        Some(f) => {
                            let ty = pick(f, "type", "alpha", &[("alpha", MaskType::Alpha), ("luminance", MaskType::Luminance)])?;
                            Some(Mask::from_pixmap(pixmap(&done, f)?.as_ref(), ty))
                        }
                        None => Mask::new(size(sc, "width")?, size(sc, "height")?),
                    };
                    match mk {
                        Some(mut mk) => {
                            for o in ops {
                                mask_op(&mut mk, o)?;
                            }
                            std::fs::write(out.join(format!("{name}.a8")), mk.data()).map_err(|e| e.to_string())?;
                            Some(Out::Mask(mk))
                        }
                        None => None,
                    }
                }
                _ => return Err(format!("bad kind in scene {name}")),
            };
            index.insert(
                name.to_string(),
                match &result {
                    Some(Out::Pixmap(p)) => json!({"kind": "pixmap", "width": p.width(), "height": p.height()}),
                    Some(Out::Mask(m)) => json!({"kind": "mask", "width": m.width(), "height": m.height()}),
                    None => Value::Null,
                },
            );
            done.insert(name.to_string(), result);
        }
        let index = serde_json::to_string_pretty(&Value::Object(index)).map_err(|e| e.to_string())?;
        std::fs::write(out.join("index.json"), index + "\n").map_err(|e| e.to_string())
    }
}

/// The Go port's usvg oracle: the tree `resvg::usvg::Tree::from_data` builds from an SVG file, as JSON. Options
/// are `tree_options` (render.rs): svg.rs's image href resolver, `resources_dir` from `--resources` (None without
/// it), and with `--fonts <cache-dir>` the fontdb and resolver of `Fonts::new(cache-dir)` with `online = false`
/// (without it, the default empty fontdb and resolver, so text is dropped). Every f32 is its bit pattern (a u32);
/// a transform T is [sx,ky,kx,sy,tx,ty], a rect R is [left,top,right,bottom] and a size is [w,h], all as bits.
///   tree   {"size","root":G,"linearGradients","radialGradients","patterns","clipPaths","masks","filters"}
///          (the collections as id lists), or {"error": usvg::Error variant} plus "xml": roxmltree::Error variant
///          for ParsingFailed; parse errors are output, not CLI failures
///   G      {"kind":"group","id","transform","absTransform","opacity","blendMode","isolate","clipPath":C|null,
///          "mask":M|null,"filters":[FL],"bbox","absBbox","strokeBbox","absStrokeBbox","layerBbox","absLayerBbox",
///          "children":[node...]}
///   FL     {"id","rect","primitives":[{"rect","colorInterpolation":"sRGB"|"linearRGB","result","kind":K}]}
///   K      {"type", then per type: blend "in1","in2","mode" | colorMatrix "in","matrix":{"type":"matrix","values"
///          |"saturate","value"|"hueRotate","value"|"luminanceToAlpha"} | componentTransfer "in","r","g","b","a" (each
///          {"type":"identity"|"table","values"|"discrete","values"|"linear","slope","intercept"|"gamma","amplitude",
///          "exponent","offset"}) | composite "in1","in2","operator","k":[k1..k4]|null | convolveMatrix "in","targetX",
///          "targetY","columns","rows","data","divisor","bias","edgeMode","preserveAlpha" | diffuseLighting "in",
///          "surfaceScale","diffuseConstant","lightingColor","light":L | displacementMap "in1","in2","scale","xChannel",
///          "yChannel" | dropShadow "in","dx","dy","stdDevX","stdDevY","color","opacity" | flood "color","opacity" |
///          gaussianBlur "in","stdDevX","stdDevY" | image "root":G | merge "inputs" | morphology "in","operator",
///          "radiusX","radiusY" | offset "in","dx","dy" | specularLighting "in","surfaceScale","specularConstant",
///          "specularExponent","lightingColor","light":L | tile "in" | turbulence "baseFrequencyX","baseFrequencyY",
///          "numOctaves","seed","stitchTiles","kind"};  an input is "SourceGraphic", "SourceAlpha" or {"ref":name}
///   L      {"type":"distant","azimuth","elevation"} | {"type":"point","x","y","z"} | {"type":"spot","x","y","z",
///          "pointsAtX","pointsAtY","pointsAtZ","specularExponent","limitingConeAngle":bits|null}
///   path   {"kind":"path","id","visible","fill":F|null,"stroke":S|null,"paintOrder","renderingMode","absTransform",
///          "bbox","absBbox","strokeBbox","absStrokeBbox","segments":[["M",x,y] ["L",x,y] ["Q",..] ["C",..] ["Z"]]}
///   image  {"kind":"image","id","visible","size","renderingMode","absTransform","bbox","absBbox","format",
///          "dataLen" (jpeg|png|gif|webp) or "tree" (svg: a nested tree object)}
///   text   {"kind":"text","id","renderingMode","writingMode","dx","dy","rotate" (bits),"absTransform","bbox","absBbox",
///          "strokeBbox","absStrokeBbox","chunks":[chunk...],"layouted":[span...],"flattened":G}
///   chunk  {"x","y" (bits|null),"anchor","text","textFlow":null|{"id","startOffset","segments"},"spans":[tspan...]}
///   tspan  {"start","end","fill":F|null,"stroke":S|null,"paintOrder","font":{"families":[..],"style","stretch",
///          "weight","variations":[[tag,bits]..]},"fontSize","smallCaps","applyKerning","opticalSizing",
///          "decoration":{"underline"|"overline"|"lineThrough": {"fill","stroke"}|null},"dominantBaseline",
///          "alignmentBaseline","baselineShift":["baseline"|"sub"|"super"|bits..],"visible","letterSpacing",
///          "wordSpacing","textLength":bits|null,"lengthAdjust"}
///   span   {"fill","stroke","paintOrder","fontSize","variations","opticalSizing","visible","glyphs":[glyph...],
///          "underline","overline","lineThrough": path|null};  glyph {"id","text","font":{"family","file","index"},
///          "fontSize","transform","outlineTransform"} (file: the font file name, "" for font data)
///   F      {"paint":P,"opacity","rule"};  S {"paint":P,"dasharray":[..]|null,"dashoffset","miterlimit","opacity",
///          "width","linecap","linejoin"}
///   P      {"type":"color","color":[r,g,b]} | {"type":"linear","id","x1","y1","x2","y2","transform","spread","stops"}
///          | {"type":"radial","id","cx","cy","r","fx","fy","fr","transform","spread","stops"}
///          | {"type":"pattern","id","transform","rect","root":G};  stop {"offset","color","opacity"}
///   C      {"id","transform","clipPath":C|null,"root":G};  M {"id","rect","kind","mask":M|null,"root":G}
mod usvg_tree {
    use resvg::tiny_skia::{NonZeroRect, Rect, Size, Transform};
    use resvg::usvg::tiny_skia_path::PathSegment;
    use resvg::usvg::{self, filter, BaseGradient, ClipPath, Group, ImageKind, ImageRendering, Mask, Node, Paint, Tree};
    use serde_json::{json, Value};

    thread_local! {
        static FONTDB: std::cell::RefCell<Option<std::sync::Arc<usvg::fontdb::Database>>> = const { std::cell::RefCell::new(None) };
    }

    pub(super) fn run(file: &str, resources: Option<&str>, fonts: Option<&str>) -> Result<(), String> {
        let bytes = std::fs::read(file).map_err(|e| e.to_string())?;
        let mut opt = usvg::Options {
            image_href_resolver: nalarupa_core::svg::image_href_resolver(),
            resources_dir: resources.map(std::path::PathBuf::from),
            ..usvg::Options::default()
        };
        if let Some(cache) = fonts {
            let mut fonts = nalarupa_core::text::Fonts::new(std::path::PathBuf::from(cache));
            fonts.online = false;
            opt.fontdb = std::sync::Arc::clone(&fonts.db);
            opt.font_resolver = fonts.resolver();
        }
        FONTDB.with(|db| *db.borrow_mut() = Some(std::sync::Arc::clone(&opt.fontdb)));
        let out = match Tree::from_data(&bytes, &opt) {
            Ok(tree) => tree_json(&tree),
            Err(e) => error(&e),
        };
        println!("{}", serde_json::to_string_pretty(&out).map_err(|e| e.to_string())?);
        Ok(())
    }

    fn error(e: &usvg::Error) -> Value {
        match e {
            usvg::Error::NotAnUtf8Str => json!({"error": "NotAnUtf8Str"}),
            usvg::Error::MalformedGZip => json!({"error": "MalformedGZip"}),
            usvg::Error::ElementsLimitReached => json!({"error": "ElementsLimitReached"}),
            usvg::Error::InvalidSize => json!({"error": "InvalidSize"}),
            usvg::Error::ParsingFailed(x) => {
                let dbg = format!("{x:?}");
                let name = dbg.split(['(', ' ', '{']).next().unwrap_or_default();
                json!({"error": "ParsingFailed", "xml": name})
            }
        }
    }

    fn tree_json(t: &Tree) -> Value {
        json!({
            "size": size(t.size()),
            "root": group(t.root()),
            "linearGradients": t.linear_gradients().iter().map(|g| g.id()).collect::<Vec<_>>(),
            "radialGradients": t.radial_gradients().iter().map(|g| g.id()).collect::<Vec<_>>(),
            "patterns": t.patterns().iter().map(|p| p.id()).collect::<Vec<_>>(),
            "clipPaths": t.clip_paths().iter().map(|c| c.id()).collect::<Vec<_>>(),
            "masks": t.masks().iter().map(|m| m.id()).collect::<Vec<_>>(),
            "filters": t.filters().iter().map(|f| f.id()).collect::<Vec<_>>(),
        })
    }

    fn f(x: f32) -> Value {
        json!(x.to_bits())
    }

    fn ts(t: Transform) -> Value {
        json!([t.sx, t.ky, t.kx, t.sy, t.tx, t.ty].map(f32::to_bits))
    }

    fn rect(r: Rect) -> Value {
        json!([r.left(), r.top(), r.right(), r.bottom()].map(f32::to_bits))
    }

    fn nz(r: NonZeroRect) -> Value {
        json!([r.left(), r.top(), r.right(), r.bottom()].map(f32::to_bits))
    }

    fn size(s: Size) -> Value {
        json!([s.width(), s.height()].map(f32::to_bits))
    }

    fn group(g: &Group) -> Value {
        json!({
            "kind": "group",
            "id": g.id(),
            "transform": ts(g.transform()),
            "absTransform": ts(g.abs_transform()),
            "opacity": f(g.opacity().get()),
            "blendMode": g.blend_mode().to_string(),
            "isolate": g.isolate(),
            "clipPath": g.clip_path().map(clip),
            "mask": g.mask().map(mask),
            "filters": g.filters().iter().map(|x| filter(x)).collect::<Vec<_>>(),
            "bbox": rect(g.bounding_box()),
            "absBbox": rect(g.abs_bounding_box()),
            "strokeBbox": rect(g.stroke_bounding_box()),
            "absStrokeBbox": rect(g.abs_stroke_bounding_box()),
            "layerBbox": nz(g.layer_bounding_box()),
            "absLayerBbox": nz(g.abs_layer_bounding_box()),
            "children": g.children().iter().map(node).collect::<Vec<_>>(),
        })
    }

    fn node(n: &Node) -> Value {
        match n {
            Node::Group(g) => group(g),
            Node::Path(p) => path_json(p),
            Node::Image(i) => {
                let mut v = json!({
                    "kind": "image",
                    "id": i.id(),
                    "visible": i.is_visible(),
                    "size": size(i.size()),
                    "renderingMode": match i.rendering_mode() {
                        ImageRendering::OptimizeQuality => "optimize-quality",
                        ImageRendering::OptimizeSpeed => "optimize-speed",
                        ImageRendering::Smooth => "smooth",
                        ImageRendering::HighQuality => "high-quality",
                        ImageRendering::CrispEdges => "crisp-edges",
                        ImageRendering::Pixelated => "pixelated",
                    },
                    "absTransform": ts(i.abs_transform()),
                    "bbox": rect(i.bounding_box()),
                    "absBbox": rect(i.abs_bounding_box()),
                });
                let (format, data) = match i.kind() {
                    ImageKind::JPEG(d) => ("jpeg", d),
                    ImageKind::PNG(d) => ("png", d),
                    ImageKind::GIF(d) => ("gif", d),
                    ImageKind::WEBP(d) => ("webp", d),
                    ImageKind::SVG(t) => {
                        v["format"] = json!("svg");
                        v["tree"] = tree_json(t);
                        return v;
                    }
                };
                v["format"] = json!(format);
                v["dataLen"] = json!(data.len());
                v
            }
            Node::Text(t) => text_json(t),
        }
    }

    fn segments(p: &resvg::usvg::tiny_skia_path::Path) -> Vec<Value> {
        p.segments().map(|s| match s {
            PathSegment::MoveTo(a) => json!(["M", f(a.x), f(a.y)]),
            PathSegment::LineTo(a) => json!(["L", f(a.x), f(a.y)]),
            PathSegment::QuadTo(a, b) => json!(["Q", f(a.x), f(a.y), f(b.x), f(b.y)]),
            PathSegment::CubicTo(a, b, c) => json!(["C", f(a.x), f(a.y), f(b.x), f(b.y), f(c.x), f(c.y)]),
            PathSegment::Close => json!(["Z"]),
        }).collect()
    }

    fn fill_json(x: &usvg::Fill) -> Value {
        json!({
            "paint": paint(x.paint()),
            "opacity": f(x.opacity().get()),
            "rule": match x.rule() { usvg::FillRule::NonZero => "nonzero", usvg::FillRule::EvenOdd => "evenodd" },
        })
    }

    fn stroke_json(s: &usvg::Stroke) -> Value {
        json!({
            "paint": paint(s.paint()),
            "dasharray": s.dasharray().map(|d| d.iter().map(|&x| f(x)).collect::<Vec<_>>()),
            "dashoffset": f(s.dashoffset()),
            "miterlimit": f(s.miterlimit().get()),
            "opacity": f(s.opacity().get()),
            "width": f(s.width().get()),
            "linecap": match s.linecap() { usvg::LineCap::Butt => "butt", usvg::LineCap::Round => "round", usvg::LineCap::Square => "square" },
            "linejoin": match s.linejoin() {
                usvg::LineJoin::Miter => "miter",
                usvg::LineJoin::MiterClip => "miter-clip",
                usvg::LineJoin::Round => "round",
                usvg::LineJoin::Bevel => "bevel",
            },
        })
    }

    fn paint_order(o: usvg::PaintOrder) -> &'static str {
        match o { usvg::PaintOrder::FillAndStroke => "fill-and-stroke", usvg::PaintOrder::StrokeAndFill => "stroke-and-fill" }
    }

    fn path_json(p: &usvg::Path) -> Value {
        let segments = segments(p.data());
        json!({
            "kind": "path",
            "id": p.id(),
            "visible": p.is_visible(),
            "fill": p.fill().map(fill_json),
            "stroke": p.stroke().map(stroke_json),
            "paintOrder": paint_order(p.paint_order()),
            "renderingMode": match p.rendering_mode() {
                usvg::ShapeRendering::OptimizeSpeed => "optimize-speed",
                usvg::ShapeRendering::CrispEdges => "crisp-edges",
                usvg::ShapeRendering::GeometricPrecision => "geometric-precision",
            },
            "absTransform": ts(p.abs_transform()),
            "bbox": rect(p.bounding_box()),
            "absBbox": rect(p.abs_bounding_box()),
            "strokeBbox": rect(p.stroke_bounding_box()),
            "absStrokeBbox": rect(p.abs_stroke_bounding_box()),
            "segments": segments,
        })
    }

    fn face_json(id: usvg::fontdb::ID) -> Value {
        FONTDB.with(|db| {
            let db = db.borrow();
            let Some(face) = db.as_ref().and_then(|db| db.face(id)) else { return Value::Null };
            let file = match &face.source {
                usvg::fontdb::Source::File(p) => p.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default(),
                usvg::fontdb::Source::SharedFile(p, _) => p.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default(),
                usvg::fontdb::Source::Binary(_) => String::new(),
            };
            json!({"family": face.families[0].0, "file": file, "index": face.index})
        })
    }

    fn variations(v: &[usvg::FontVariation]) -> Value {
        json!(v.iter().map(|v| json!([String::from_utf8_lossy(&v.tag), f(v.value)])).collect::<Vec<_>>())
    }

    fn optical_sizing(o: usvg::FontOpticalSizing) -> &'static str {
        match o { usvg::FontOpticalSizing::Auto => "auto", usvg::FontOpticalSizing::None => "none" }
    }

    fn deco(d: Option<&usvg::TextDecorationStyle>) -> Value {
        match d {
            Some(d) => json!({"fill": d.fill().map(fill_json), "stroke": d.stroke().map(stroke_json)}),
            None => Value::Null,
        }
    }

    fn text_json(t: &usvg::Text) -> Value {
        let chunks: Vec<Value> = t.chunks().iter().map(|c| {
            let spans: Vec<Value> = c.spans().iter().map(|s| {
                let font = s.font();
                json!({
                    "start": s.start(),
                    "end": s.end(),
                    "fill": s.fill().map(fill_json),
                    "stroke": s.stroke().map(stroke_json),
                    "paintOrder": paint_order(s.paint_order()),
                    "font": {
                        "families": font.families().iter().map(|f| f.to_string()).collect::<Vec<_>>(),
                        "style": format!("{:?}", font.style()),
                        "stretch": format!("{:?}", font.stretch()),
                        "weight": font.weight(),
                        "variations": variations(font.variations()),
                    },
                    "fontSize": f(s.font_size().get()),
                    "smallCaps": s.small_caps(),
                    "applyKerning": s.apply_kerning(),
                    "opticalSizing": optical_sizing(s.font_optical_sizing()),
                    "decoration": {
                        "underline": deco(s.decoration().underline()),
                        "overline": deco(s.decoration().overline()),
                        "lineThrough": deco(s.decoration().line_through()),
                    },
                    "dominantBaseline": format!("{:?}", s.dominant_baseline()),
                    "alignmentBaseline": format!("{:?}", s.alignment_baseline()),
                    "baselineShift": s.baseline_shift().iter().map(|b| match b {
                        usvg::BaselineShift::Baseline => json!("baseline"),
                        usvg::BaselineShift::Subscript => json!("sub"),
                        usvg::BaselineShift::Superscript => json!("super"),
                        usvg::BaselineShift::Number(n) => f(*n),
                    }).collect::<Vec<_>>(),
                    "visible": s.is_visible(),
                    "letterSpacing": f(s.letter_spacing()),
                    "wordSpacing": f(s.word_spacing()),
                    "textLength": s.text_length().map(f),
                    "lengthAdjust": format!("{:?}", s.length_adjust()),
                })
            }).collect();
            json!({
                "x": c.x().map(f),
                "y": c.y().map(f),
                "anchor": format!("{:?}", c.anchor()),
                "text": c.text(),
                "textFlow": match c.text_flow() {
                    usvg::TextFlow::Linear => Value::Null,
                    usvg::TextFlow::Path(p) => json!({"id": p.id(), "startOffset": f(p.start_offset()), "segments": segments(p.path())}),
                },
                "spans": spans,
            })
        }).collect();
        let layouted: Vec<Value> = t.layouted().iter().map(|s| json!({
            "fill": s.fill.as_ref().map(fill_json),
            "stroke": s.stroke.as_ref().map(stroke_json),
            "paintOrder": paint_order(s.paint_order),
            "fontSize": f(s.font_size.get()),
            "variations": variations(&s.variations),
            "opticalSizing": optical_sizing(s.font_optical_sizing),
            "visible": s.visible,
            "glyphs": s.positioned_glyphs.iter().map(|g| json!({
                "id": g.id.0,
                "text": g.text,
                "font": face_json(g.font),
                "fontSize": f(g.font_size()),
                "transform": ts(g.transform()),
                "outlineTransform": ts(g.outline_transform()),
            })).collect::<Vec<_>>(),
            "underline": s.underline.as_ref().map(path_json),
            "overline": s.overline.as_ref().map(path_json),
            "lineThrough": s.line_through.as_ref().map(path_json),
        })).collect();
        json!({
            "kind": "text",
            "id": t.id(),
            "renderingMode": format!("{:?}", t.rendering_mode()),
            "writingMode": format!("{:?}", t.writing_mode()),
            "dx": t.dx().iter().map(|&x| f(x)).collect::<Vec<_>>(),
            "dy": t.dy().iter().map(|&x| f(x)).collect::<Vec<_>>(),
            "rotate": t.rotate().iter().map(|&x| f(x)).collect::<Vec<_>>(),
            "absTransform": ts(t.abs_transform()),
            "bbox": rect(t.bounding_box()),
            "absBbox": rect(t.abs_bounding_box()),
            "strokeBbox": rect(t.stroke_bounding_box()),
            "absStrokeBbox": rect(t.abs_stroke_bounding_box()),
            "chunks": chunks,
            "layouted": layouted,
            "flattened": group(t.flattened()),
        })
    }

    fn paint(p: &Paint) -> Value {
        match p {
            Paint::Color(c) => json!({"type": "color", "color": [c.red, c.green, c.blue]}),
            Paint::LinearGradient(g) => {
                let mut v = json!({"type": "linear", "id": g.id(), "x1": f(g.x1()), "y1": f(g.y1()), "x2": f(g.x2()), "y2": f(g.y2())});
                gradient(&mut v, g);
                v
            }
            Paint::RadialGradient(g) => {
                let mut v = json!({"type": "radial", "id": g.id(), "cx": f(g.cx()), "cy": f(g.cy()), "r": f(g.r().get()),
                                   "fx": f(g.fx()), "fy": f(g.fy()), "fr": f(g.fr().get())});
                gradient(&mut v, g);
                v
            }
            Paint::Pattern(p) => json!({"type": "pattern", "id": p.id(), "transform": ts(p.transform()), "rect": nz(p.rect()), "root": group(p.root())}),
        }
    }

    fn gradient(v: &mut Value, g: &BaseGradient) {
        v["transform"] = ts(g.transform());
        v["spread"] = json!(match g.spread_method() {
            usvg::SpreadMethod::Pad => "pad",
            usvg::SpreadMethod::Reflect => "reflect",
            usvg::SpreadMethod::Repeat => "repeat",
        });
        v["stops"] = g.stops().iter().map(|s| {
            let c = s.color();
            json!({"offset": f(s.offset().get()), "color": [c.red, c.green, c.blue], "opacity": f(s.opacity().get())})
        }).collect();
    }

    fn clip(c: &ClipPath) -> Value {
        json!({"id": c.id(), "transform": ts(c.transform()), "clipPath": c.clip_path().map(clip), "root": group(c.root())})
    }

    fn mask(m: &Mask) -> Value {
        let kind = match m.kind() { usvg::MaskType::Luminance => "luminance", usvg::MaskType::Alpha => "alpha" };
        json!({"id": m.id(), "rect": nz(m.rect()), "kind": kind, "mask": m.mask().map(mask), "root": group(m.root())})
    }

    fn filter(fl: &filter::Filter) -> Value {
        let primitives: Vec<Value> = fl.primitives().iter().map(|p| json!({
            "rect": nz(p.rect()),
            "colorInterpolation": match p.color_interpolation() {
                filter::ColorInterpolation::SRGB => "sRGB",
                filter::ColorInterpolation::LinearRGB => "linearRGB",
            },
            "result": p.result(),
            "kind": filter_kind(p.kind()),
        })).collect();
        json!({"id": fl.id(), "rect": nz(fl.rect()), "primitives": primitives})
    }

    fn input(i: &filter::Input) -> Value {
        match i {
            filter::Input::SourceGraphic => json!("SourceGraphic"),
            filter::Input::SourceAlpha => json!("SourceAlpha"),
            filter::Input::Reference(name) => json!({"ref": name}),
        }
    }

    fn floats(v: &[f32]) -> Value {
        json!(v.iter().map(|x| x.to_bits()).collect::<Vec<_>>())
    }

    fn rgb(c: usvg::Color) -> Value {
        json!([c.red, c.green, c.blue])
    }

    fn transfer(t: &filter::TransferFunction) -> Value {
        use filter::TransferFunction as T;
        match t {
            T::Identity => json!({"type": "identity"}),
            T::Table(v) => json!({"type": "table", "values": floats(v)}),
            T::Discrete(v) => json!({"type": "discrete", "values": floats(v)}),
            T::Linear { slope, intercept } => json!({"type": "linear", "slope": f(*slope), "intercept": f(*intercept)}),
            T::Gamma { amplitude, exponent, offset } => {
                json!({"type": "gamma", "amplitude": f(*amplitude), "exponent": f(*exponent), "offset": f(*offset)})
            }
        }
    }

    fn light(l: filter::LightSource) -> Value {
        match l {
            filter::LightSource::DistantLight(d) => json!({"type": "distant", "azimuth": f(d.azimuth), "elevation": f(d.elevation)}),
            filter::LightSource::PointLight(p) => json!({"type": "point", "x": f(p.x), "y": f(p.y), "z": f(p.z)}),
            filter::LightSource::SpotLight(s) => json!({
                "type": "spot", "x": f(s.x), "y": f(s.y), "z": f(s.z),
                "pointsAtX": f(s.points_at_x), "pointsAtY": f(s.points_at_y), "pointsAtZ": f(s.points_at_z),
                "specularExponent": f(s.specular_exponent.get()),
                "limitingConeAngle": s.limiting_cone_angle.map(f),
            }),
        }
    }

    fn channel(c: filter::ColorChannel) -> &'static str {
        match c { filter::ColorChannel::R => "R", filter::ColorChannel::G => "G", filter::ColorChannel::B => "B", filter::ColorChannel::A => "A" }
    }

    fn filter_kind(k: &filter::Kind) -> Value {
        use filter::Kind;
        match k {
            Kind::Blend(fe) => json!({"type": "blend", "in1": input(fe.input1()), "in2": input(fe.input2()), "mode": fe.mode().to_string()}),
            Kind::ColorMatrix(fe) => {
                let matrix = match fe.kind() {
                    filter::ColorMatrixKind::Matrix(v) => json!({"type": "matrix", "values": floats(v)}),
                    filter::ColorMatrixKind::Saturate(v) => json!({"type": "saturate", "value": f(v.get())}),
                    filter::ColorMatrixKind::HueRotate(v) => json!({"type": "hueRotate", "value": f(*v)}),
                    filter::ColorMatrixKind::LuminanceToAlpha => json!({"type": "luminanceToAlpha"}),
                };
                json!({"type": "colorMatrix", "in": input(fe.input()), "matrix": matrix})
            }
            Kind::ComponentTransfer(fe) => json!({
                "type": "componentTransfer", "in": input(fe.input()),
                "r": transfer(fe.func_r()), "g": transfer(fe.func_g()), "b": transfer(fe.func_b()), "a": transfer(fe.func_a()),
            }),
            Kind::Composite(fe) => {
                use filter::CompositeOperator as O;
                let (op, k) = match fe.operator() {
                    O::Over => ("over", Value::Null),
                    O::In => ("in", Value::Null),
                    O::Out => ("out", Value::Null),
                    O::Atop => ("atop", Value::Null),
                    O::Xor => ("xor", Value::Null),
                    O::Arithmetic { k1, k2, k3, k4 } => ("arithmetic", floats(&[k1, k2, k3, k4])),
                };
                json!({"type": "composite", "in1": input(fe.input1()), "in2": input(fe.input2()), "operator": op, "k": k})
            }
            Kind::ConvolveMatrix(fe) => {
                let m = fe.matrix();
                json!({
                    "type": "convolveMatrix", "in": input(fe.input()),
                    "targetX": m.target_x(), "targetY": m.target_y(), "columns": m.columns(), "rows": m.rows(),
                    "data": floats(m.data()), "divisor": f(fe.divisor().get()), "bias": f(fe.bias()),
                    "edgeMode": match fe.edge_mode() {
                        filter::EdgeMode::None => "none",
                        filter::EdgeMode::Duplicate => "duplicate",
                        filter::EdgeMode::Wrap => "wrap",
                    },
                    "preserveAlpha": fe.preserve_alpha(),
                })
            }
            Kind::DiffuseLighting(fe) => json!({
                "type": "diffuseLighting", "in": input(fe.input()), "surfaceScale": f(fe.surface_scale()),
                "diffuseConstant": f(fe.diffuse_constant()), "lightingColor": rgb(fe.lighting_color()),
                "light": light(fe.light_source()),
            }),
            Kind::DisplacementMap(fe) => json!({
                "type": "displacementMap", "in1": input(fe.input1()), "in2": input(fe.input2()), "scale": f(fe.scale()),
                "xChannel": channel(fe.x_channel_selector()), "yChannel": channel(fe.y_channel_selector()),
            }),
            Kind::DropShadow(fe) => json!({
                "type": "dropShadow", "in": input(fe.input()), "dx": f(fe.dx()), "dy": f(fe.dy()),
                "stdDevX": f(fe.std_dev_x().get()), "stdDevY": f(fe.std_dev_y().get()),
                "color": rgb(fe.color()), "opacity": f(fe.opacity().get()),
            }),
            Kind::Flood(fe) => json!({"type": "flood", "color": rgb(fe.color()), "opacity": f(fe.opacity().get())}),
            Kind::GaussianBlur(fe) => json!({
                "type": "gaussianBlur", "in": input(fe.input()),
                "stdDevX": f(fe.std_dev_x().get()), "stdDevY": f(fe.std_dev_y().get()),
            }),
            Kind::Image(fe) => json!({"type": "image", "root": group(fe.root())}),
            Kind::Merge(fe) => json!({"type": "merge", "inputs": fe.inputs().iter().map(input).collect::<Vec<_>>()}),
            Kind::Morphology(fe) => json!({
                "type": "morphology", "in": input(fe.input()),
                "operator": match fe.operator() { filter::MorphologyOperator::Erode => "erode", filter::MorphologyOperator::Dilate => "dilate" },
                "radiusX": f(fe.radius_x().get()), "radiusY": f(fe.radius_y().get()),
            }),
            Kind::Offset(fe) => json!({"type": "offset", "in": input(fe.input()), "dx": f(fe.dx()), "dy": f(fe.dy())}),
            Kind::SpecularLighting(fe) => json!({
                "type": "specularLighting", "in": input(fe.input()), "surfaceScale": f(fe.surface_scale()),
                "specularConstant": f(fe.specular_constant()), "specularExponent": f(fe.specular_exponent()),
                "lightingColor": rgb(fe.lighting_color()), "light": light(fe.light_source()),
            }),
            Kind::Tile(fe) => json!({"type": "tile", "in": input(fe.input())}),
            Kind::Turbulence(fe) => json!({
                "type": "turbulence", "baseFrequencyX": f(fe.base_frequency_x().get()),
                "baseFrequencyY": f(fe.base_frequency_y().get()), "numOctaves": fe.num_octaves(), "seed": fe.seed(),
                "stitchTiles": fe.stitch_tiles(),
                "kind": match fe.kind() { filter::TurbulenceKind::FractalNoise => "fractalNoise", filter::TurbulenceKind::Turbulence => "turbulence" },
            }),
        }
    }
}

/// `resvg <file.svg> <out.rgba>`: the tree is parsed with the options `usvg_tree` uses (the file's folder as
/// the resources dir), and rendered at `--scale` onto a pixmap of `size.to_int_size().scale_by(scale)` with
/// `Transform::from_scale(scale, scale)`. The premultiplied RGBA bytes go to <out.rgba>; stdout gets
/// {"width","height"}, or {"error"} when parsing fails or the pixmap cannot be allocated (no file then).
mod resvg_render {
    use resvg::tiny_skia::{Pixmap, Transform};
    use resvg::usvg::{self, Tree};
    use serde_json::json;

    /// The usvg tree of an SVG file with the oracle's options (`pdf-svg`).
    pub(super) fn tree(file: &str, fonts: Option<&str>) -> Result<Tree, String> {
        let bytes = std::fs::read(file).map_err(|e| e.to_string())?;
        Tree::from_data(&bytes, &options(file, fonts)).map_err(|e| e.to_string())
    }

    fn options(file: &str, fonts: Option<&str>) -> usvg::Options<'static> {
        let mut opt = usvg::Options {
            image_href_resolver: nalarupa_core::svg::image_href_resolver(),
            resources_dir: std::path::Path::new(file).parent().map(std::path::Path::to_path_buf),
            ..usvg::Options::default()
        };
        if let Some(cache) = fonts {
            let mut fonts = nalarupa_core::text::Fonts::new(std::path::PathBuf::from(cache));
            fonts.online = false;
            opt.fontdb = std::sync::Arc::clone(&fonts.db);
            opt.font_resolver = fonts.resolver();
        }
        opt
    }

    pub(super) fn run(file: &str, out: &str, fonts: Option<&str>, scale: f32) -> Result<(), String> {
        let bytes = std::fs::read(file).map_err(|e| e.to_string())?;
        let opt = options(file, fonts);
        let result = match Tree::from_data(&bytes, &opt) {
            Err(e) => json!({"error": e.to_string()}),
            Ok(tree) => match tree.size().to_int_size().scale_by(scale).and_then(|s| Pixmap::new(s.width(), s.height())) {
                None => json!({"error": "no pixmap"}),
                Some(mut pm) => {
                    resvg::render(&tree, Transform::from_scale(scale, scale), &mut pm.as_mut());
                    std::fs::write(out, pm.data()).map_err(|e| e.to_string())?;
                    json!({"width": pm.width(), "height": pm.height()})
                }
            },
        };
        println!("{result}");
        Ok(())
    }
}

/// The Go port's canvas oracle: a `Renderer` forced to `Raster::Resvg`, offline, with a pinned slide clock
/// and pinned live-zoom cost, so every step is deterministic. The scenario is {"steps": [...]}, each step an
/// object with "op":
///   render {w, h, cam: [x, y], zoom, bg: [r, g, b], solo, sharp, slow}   writes <step>.rgb (`slow` takes the
///                                                       scaled preview on a live zoom, else the viewport repaint)
///   update {id, props} · insert {parent, node, index} · delete {id} · move {id, parent, index}   edit the doc
///   sync                                                the engine's sync: shift_siblings, sync, settle_covered
///                                                       (syncing again when any settled), refresh_slides
///   slide {id}                                          the engine's slide_in (without the glass check)
///   tick {ms}                                           advance the clock, then expire_slides
///   hit {x, y} · empty {w, h, dir, pad, anchor} · flat · live
/// log.json holds one entry per step.
mod canvas {
    use nalarupa_core::doc::{Doc, ROOT};
    use nalarupa_core::render::{find_empty_space, hit_chain, Ctx, Raster, Renderer, Slide, MAX_SLIDES};
    use nalarupa_core::svg::Assets;
    use nalarupa_core::text::Fonts;
    use serde_json::{json, Value};
    use std::collections::HashSet;
    use std::path::Path;
    use std::time::{Duration, Instant};

    fn slides(r: &Renderer) -> Value {
        let now = r.now();
        r.slides.iter().map(|s| json!({"id": s.id, "state": s.state(now)})).collect()
    }

    fn done(res: Result<Value, String>) -> Value {
        match res {
            Ok(v) => json!({"ok": v}),
            Err(e) => json!({"error": e}),
        }
    }

    pub(super) fn run(doc: &str, scenario: &str, out: &str) -> Result<(), String> {
        let mut d = Doc::load(Path::new(doc))?;
        nalarupa_core::resolve::migrate(&mut d.root);
        d.ensure_ids();
        let data = nalarupa_core::render::data_dir();
        let mut fonts = Fonts::new(data.join("fonts"));
        fonts.online = false;
        let mut assets = Assets::new(data.join("cache"));
        assets.online = false;
        let src = std::fs::read_to_string(scenario).map_err(|e| e.to_string())?;
        let sc: Value = serde_json::from_str(&src).map_err(|e| e.to_string())?;
        let out = Path::new(out);
        std::fs::create_dir_all(out).map_err(|e| e.to_string())?;
        let mut r = Renderer::default();
        (r.raster, r.clock) = (Raster::Resvg, Some(Instant::now()));
        let mut slid: HashSet<String> = HashSet::new();
        let mut log = Vec::new();
        for (i, s) in sc["steps"].as_array().ok_or("scenario has no steps")?.iter().enumerate() {
            let op = s["op"].as_str().unwrap_or_default();
            let num = |k: &str, d: f64| s[k].as_f64().unwrap_or(d);
            let id = s["id"].as_str().unwrap_or_default();
            let index = s["index"].as_u64().map(|i| i as usize);
            let entry = match op {
                "render" => {
                    let (w, h) = (num("w", 800.0) as u32, num("h", 600.0) as u32);
                    let bg = s["bg"].as_array().map_or([255, 255, 255], |c| [0, 1, 2].map(|k| c.get(k).and_then(Value::as_u64).unwrap_or(0) as u8));
                    r.ms_per_mp = if s["slow"] == true { 1e9 } else { 0.0 };
                    let px = r.render(w, h, s["cam"][0].as_f64().unwrap_or(0.0), s["cam"][1].as_f64().unwrap_or(0.0), num("zoom", 1.0), bg, s["solo"].as_str(), s["sharp"] != false);
                    let rgb: Vec<u8> = px.chunks_exact(4).flat_map(|p| [p[0], p[1], p[2]]).collect();
                    let file = format!("{i}.rgb");
                    std::fs::write(out.join(&file), rgb).map_err(|e| e.to_string())?;
                    json!({"width": w.clamp(1, 8192), "height": h.clamp(1, 8192), "file": file, "slides": slides(&r)})
                }
                "update" => done(d.update(id, &s["props"]).map(|_| Value::Null)),
                "insert" => done(d.insert(s["parent"].as_str().unwrap_or(ROOT), s["node"].clone(), index).map(Value::from)),
                "delete" => done(d.delete(id).map(|_| Value::Null)),
                "move" => done(d.move_node(id, s["parent"].as_str(), index).map(|_| Value::Null)),
                "sync" => {
                    let (mut passes, mut settled, mut dropped) = (Vec::new(), false, false);
                    loop {
                        let mut cx = Ctx { doc: &d, libs: &[], fonts: &mut fonts, assets: &mut assets };
                        let roots = r.shift_siblings(&mut cx);
                        let rebuilt = r.sync(&mut cx);
                        passes.push(rebuilt.clone());
                        if !r.slides.is_empty() {
                            if r.settle_covered() {
                                settled = true;
                                continue;
                            }
                            dropped = r.refresh_slides(&mut cx, &rebuilt, roots);
                        }
                        break;
                    }
                    json!({"rebuilt": passes, "settled": settled, "dropped": dropped, "dirty": r.dirty(), "order": r.order, "bounds": r.bounds(), "slides": slides(&r)})
                }
                "slide" => {
                    let room = r.slides.len() < MAX_SLIDES && d.parent_of(id).is_some_and(|(p, _)| r.can_slide_into(&d, &p));
                    let pushed = slid.insert(id.to_string()) && room;
                    if pushed {
                        r.slides.push(Slide::new_at(id, r.now()));
                    }
                    json!({"pushed": pushed, "slides": slides(&r)})
                }
                "tick" => {
                    r.clock = Some(r.now() + Duration::from_millis(s["ms"].as_u64().unwrap_or(0)));
                    json!({"expired": r.expire_slides(), "slides": slides(&r)})
                }
                "hit" => json!({"chain": hit_chain(&r, num("x", 0.0), num("y", 0.0))}),
                "empty" => {
                    let anchor = s["anchor"].as_str().and_then(|a| r.find(a));
                    json!({"result": find_empty_space(&r, num("w", 0.0), num("h", 0.0), s["dir"].as_str().unwrap_or("right"), num("pad", 0.0), anchor)})
                }
                "flat" => {
                    let flat: Vec<Value> = r.flat().map(|f| json!({"id": f.id, "name": f.name, "ty": f.ty, "x": f.x, "y": f.y, "w": f.w, "h": f.h, "depth": f.depth,
                        "parent": f.parent, "problem": f.problem, "placeholder": f.placeholder, "pending": f.pending, "faded": f.faded, "m": f.m,
                        "local": f.local, "radius": f.radius, "clip": f.clip})).collect();
                    json!({"flat": flat})
                }
                "live" => {
                    let live: Vec<Value> = r.live().into_iter().map(|(f, clip, cuts)| json!({"id": f.id, "clip": clip, "cuts": cuts})).collect();
                    json!({"live": live})
                }
                other => return Err(format!("step {i}: unknown op '{other}'")),
            };
            let mut entry = entry;
            entry["op"] = json!(op);
            log.push(entry);
        }
        std::fs::write(out.join("log.json"), serde_json::to_string_pretty(&log).map_err(|e| e.to_string())?).map_err(|e| e.to_string())
    }
}
