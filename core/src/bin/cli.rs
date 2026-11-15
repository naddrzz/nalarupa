//! Headless Nalarupa: render/export documents, run execute scripts, serve MCP.
//!   nalarupa-cli render <doc.pen> <out-dir> [scale]   one PNG per top-level node
//!   nalarupa-cli svg <doc.pen> <node-id> <out.svg> [--offline]
//!   nalarupa-cli html <doc.pen> <node-id> <out.html>
//!   nalarupa-cli exec <doc.pen> <script.js> [--save]
//!   nalarupa-cli mcp                                    serve MCP until Ctrl+C
//!   nalarupa-cli layout <doc.pen> [<saved.pen>]         laid-out tree of every top-level node as JSON
//!                                                       (the Go port's parity oracle); saves the opened document
//!   nalarupa-cli measure <cases.json> [<font-cache>]    metrics, width and wrapped lines of each {text, style, maxW?}
//!                                                       (the Go port's text oracle), offline
//!   nalarupa-cli skia-path <cases.json>                 tiny-skia path geometry of each case (build, transform,
//!                                                       dash, stroke) with f32 values as bit patterns (the Go
//!                                                       port's path oracle, see skia_path below)
//!   nalarupa-cli skia-raster <scenes.json> <out-dir>    tiny-skia pixels of each scene (pixmap and mask ops) as raw
//!                                                       bytes plus index.json (the Go port's raster oracle, see
//!                                                       skia_raster below)

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
        "skia-path" => {
            let src = std::fs::read_to_string(arg(2)?).map_err(|e| e.to_string())?;
            let cases: Vec<Value> = serde_json::from_str(&src).map_err(|e| e.to_string())?;
            let out = cases.iter().map(skia_path::case).collect::<Result<Vec<_>, _>>()?;
            println!("{}", serde_json::to_string_pretty(&out).map_err(|e| e.to_string())?);
            Ok(())
        }
        "skia-raster" => skia_raster::run(arg(2)?, arg(3)?),
        other => Err(format!("unknown command '{other}'")),
    }
}

fn dump(n: &RNode) -> Value {
    json!({"id": n.id, "type": n.ty, "x": n.x, "y": n.y, "w": n.w, "h": n.h, "problem": n.problem,
           "children": n.kids.iter().map(dump).collect::<Vec<_>>()})
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
