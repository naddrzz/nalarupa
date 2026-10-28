//! The "AI Pen" (streaming SVG generation, `Generate("svg", frameId, prompt)`): Claude
//! writes an SVG and the frame fills while it streams. Every element becomes a path node as soon
//! as its tag is complete; the path still being written shows as a thin outline that grows with
//! each finished number, under a pen cursor, and gets its real paint when its tag closes.
//! The streamed prefix is closed into a well-formed document and parsed by usvg, so styles,
//! groups, transforms, `use` and gradients resolve exactly as in a full parse.

use crate::engine::engine;
use resvg::usvg::{self, tiny_skia_path as tsp};
use serde_json::{json, Map, Value};
use std::cell::Cell;
use std::fmt::Write as _;
use std::io::{BufRead, BufReader};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

const SYSTEM: &str = "You are a vector illustrator. Reply with one standalone SVG document and nothing else: no prose, no markdown fences. \
Start with <svg and give it a viewBox. Use only path, rect, circle, ellipse, line, polyline, polygon, g, and linearGradient / radialGradient inside defs; \
no text, images, filters, masks, patterns, scripts or animation. Paint back to front: large background shapes first, details last. \
Give every shape an explicit fill (and stroke where needed) as hex colors. Keep it lean: at most about 80 shapes, path data with few decimals.";

/// Id given to the element whose geometry is still streaming, to find it in the parsed tree.
const PEN_ID: &str = "nalarupa-pen";
/// Outline of the path being drawn (Nalarupa's agent presence color).
const PREVIEW: &str = "#607DFF";
/// The document is updated at most this often while the model writes.
const APPLY_EVERY: Duration = Duration::from_millis(50);

/// Draw `prompt` into `frame` (`size` = its width and height) in the background. Stops early when
/// `cancel` (the pen's entry in `agent.pens`) is set by Stop, or the frame disappears.
pub fn start(frame: String, prompt: String, size: (f64, f64), cancel: Arc<AtomicBool>) {
    std::thread::spawn(move || {
        let mut pen = Pen { frame, size, text: String::new(), ids: Vec::new(), applied: None, seen: 0, point: None };
        let ask = format!("{prompt}\n\nThe artwork fills a {:.0} × {:.0} frame: use a viewBox with that aspect ratio.", size.0, size.1);
        let res = generate(&ask, &cancel, &mut |t| pen.feed(t, false));
        if let Err(err) = res {
            engine().emit(json!({"type": "toast", "text": format!("AI Pen: {err}")}));
        }
        pen.feed("", true);
        // Its own entry: another pen drawing into the same frame keeps its own.
        let mut e = engine();
        if let Some(i) = e.agent.pens.iter().position(|(_, c)| Arc::ptr_eq(c, &cancel)) {
            e.agent.pens.remove(i);
        }
    });
}

struct Pen {
    frame: String,
    size: (f64, f64),
    text: String,
    /// Node id per drawn element, stable while the drawing grows.
    ids: Vec<String>,
    applied: Option<Instant>,
    /// Text length at the last update.
    seen: usize,
    point: Option<(f64, f64)>,
}

impl Pen {
    /// Take more model output; redraw when due. False once the frame is gone (stop generating).
    fn feed(&mut self, t: &str, done: bool) -> bool {
        self.text.push_str(t);
        if !done && (self.text.len() == self.seen || self.applied.is_some_and(|a| a.elapsed() < APPLY_EVERY)) {
            return true;
        }
        self.applied = Some(Instant::now());
        self.seen = self.text.len();
        let drawing = draw(&self.text, self.size, done);
        let mut e = engine();
        let mut props = Map::new();
        if let Some(d) = drawing {
            let mut kids = d.nodes;
            for (i, k) in kids.iter_mut().enumerate() {
                if self.ids.len() <= i {
                    self.ids.push(e.doc.gen_id());
                }
                k["id"] = json!(self.ids[i]);
            }
            props.insert("children".into(), Value::Array(kids));
            // The frame keeps its size, lays nothing out and clips the artwork.
            props.insert("layout".into(), json!("none"));
            props.insert("clip".into(), json!(true));
            if d.point.is_some() && d.point != self.point {
                self.point = d.point;
                if let (Some((px, py)), Some(f)) = (d.point, e.renderer.find(&self.frame)) {
                    let ev = json!({"type": "pen", "id": self.frame, "x": f.x + px, "y": f.y + py});
                    e.emit(ev);
                }
            }
        }
        if done {
            props.insert("placeholder".into(), json!(false));
            let ev = json!({"type": "pen_end", "id": self.frame});
            e.emit(ev);
        }
        if props.is_empty() {
            return e.doc.find(&self.frame).is_some();
        }
        e.apply_generated(&self.frame, &props, done)
    }
}

/// Stream the SVG text from Claude (same backend as the agent) into `on`.
fn generate(ask: &str, cancel: &AtomicBool, on: &mut dyn FnMut(&str) -> bool) -> Result<(), String> {
    if crate::agent::backend() == "claude-code" {
        return claude_code(ask, cancel, on);
    }
    let (key, model) = {
        let e = engine();
        let key = e.api_key().ok_or("no Anthropic API key")?;
        (key, e.setting("agent_model").unwrap_or_else(|| crate::agent::DEFAULT_MODEL.into()))
    };
    let body = json!({"model": model, "max_tokens": 16000, "system": SYSTEM, "messages": [{"role": "user", "content": ask}]});
    let gone = Cell::new(false);
    let res = crate::claude::Client::new(key).stream(body, None, &|| gone.get() || cancel.load(Ordering::SeqCst), &mut |ev| {
        if let crate::claude::Event::Text(t) = ev {
            if !on(t) {
                gone.set(true);
            }
        }
    });
    match res {
        Err(e) if e != "stopped" => Err(e),
        _ => Ok(()),
    }
}

/// The `claude` CLI without tools or session, on the composer's model (unset = the CLI's default,
/// as for the agent's own process).
fn cli(ask: &str, model: Option<&str>) -> Command {
    let mut cmd = Command::new("claude");
    cmd.arg("-p")
        .arg(ask)
        .args(["--output-format", "stream-json", "--verbose", "--include-partial-messages", "--no-session-persistence", "--strict-mcp-config", "--tools", ""])
        .arg("--system-prompt")
        .arg(SYSTEM)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    if let Some(m) = model {
        cmd.args(["--model", m]);
    }
    cmd
}

/// Stream the `claude` CLI's text deltas into `on`.
fn claude_code(ask: &str, cancel: &AtomicBool, on: &mut dyn FnMut(&str) -> bool) -> Result<(), String> {
    let model = engine().setting("agent_model");
    let mut child = cli(ask, model.as_deref())
        .spawn()
        .map_err(|e| format!("could not start the `claude` CLI ({e})"))?;
    let stdout = child.stdout.take().ok_or("no output from claude")?;
    let mut failed = None;
    // ponytail: cancellation is noticed at the next streamed line; a watcher thread could kill a silent process sooner.
    for line in BufReader::new(stdout).lines() {
        let Ok(line) = line else { break };
        if cancel.load(Ordering::SeqCst) {
            break;
        }
        let Ok(ev) = serde_json::from_str::<Value>(&line) else { continue };
        match ev["type"].as_str() {
            Some("stream_event") if ev["event"]["delta"]["type"] == "text_delta" => {
                if !on(ev["event"]["delta"]["text"].as_str().unwrap_or("")) {
                    break;
                }
            }
            Some("result") if ev["is_error"] == true => failed = Some(ev["result"].as_str().unwrap_or("Claude Code reported an error").to_string()),
            _ => {}
        }
    }
    let _ = child.kill();
    let _ = child.wait();
    failed.map_or(Ok(()), Err)
}

/// Nodes for the SVG streamed so far, fitted into a `size` frame, plus the pen position (frame
/// coordinates). None until the root `<svg>` tag is complete.
struct Drawing {
    nodes: Vec<Value>,
    point: Option<(f64, f64)>,
}

fn draw(text: &str, (fw, fh): (f64, f64), done: bool) -> Option<Drawing> {
    let src = &text[text.find("<svg")?..];
    let src = match src.find("</svg>") {
        Some(i) if done => &src[..i + 6],
        _ => src,
    };
    let doc = close(src)?;
    let mut opt = usvg::Options::default();
    // Model output never reads local or remote files.
    opt.image_href_resolver = usvg::ImageHrefResolver { resolve_data: Box::new(|_, _, _| None), resolve_string: Box::new(|_, _| None) };
    let tree = usvg::Tree::from_str(&doc, &opt).ok()?;
    let (w0, h0) = (tree.size().width() as f64, tree.size().height() as f64);
    // The drawing fits into the frame (contain, centered).
    let s = (fw / w0).min(fh / h0);
    let fit = tsp::Transform::from_row(s as f32, 0.0, 0.0, s as f32, ((fw - w0 * s) / 2.0) as f32, ((fh - h0 * s) / 2.0) as f32);
    let mut out = Drawing { nodes: Vec::new(), point: None };
    walk(tree.root(), fit, 1.0, (w0 * s / 1000.0).max(0.5), &mut out);
    Some(out)
}

fn walk(g: &usvg::Group, fit: tsp::Transform, opacity: f64, outline: f64, out: &mut Drawing) {
    for n in g.children() {
        match n {
            // ponytail: group opacity is applied per path (overlapping children blend), and clips/masks are dropped.
            usvg::Node::Group(sub) => walk(sub, fit, opacity * sub.opacity().get() as f64, outline, out),
            usvg::Node::Path(p) if p.is_visible() => {
                if let Some(node) = path_node(p, fit, opacity, outline, &mut out.point) {
                    out.nodes.push(node);
                }
            }
            _ => {}
        }
    }
}

fn r2(v: f64) -> f64 {
    (v * 100.0).round() / 100.0
}

fn path_node(p: &usvg::Path, fit: tsp::Transform, opacity: f64, outline: f64, point: &mut Option<(f64, f64)>) -> Option<Value> {
    let ts = fit.pre_concat(p.abs_transform());
    let data = p.data().clone().transform(ts)?;
    let mut d = String::new();
    for seg in data.segments() {
        let _ = match seg {
            tsp::PathSegment::MoveTo(a) => write!(d, "M{} {}", r2(a.x as f64), r2(a.y as f64)),
            tsp::PathSegment::LineTo(a) => write!(d, "L{} {}", r2(a.x as f64), r2(a.y as f64)),
            tsp::PathSegment::QuadTo(a, b) => write!(d, "Q{} {} {} {}", r2(a.x as f64), r2(a.y as f64), r2(b.x as f64), r2(b.y as f64)),
            tsp::PathSegment::CubicTo(a, b, c) => {
                write!(d, "C{} {} {} {} {} {}", r2(a.x as f64), r2(a.y as f64), r2(b.x as f64), r2(b.y as f64), r2(c.x as f64), r2(c.y as f64))
            }
            tsp::PathSegment::Close => write!(d, "Z"),
        };
    }
    if let Some(last) = data.points().last() {
        *point = Some((last.x as f64, last.y as f64));
    }
    let b = data.bounds();
    let bx = (b.x() as f64, b.y() as f64, b.width() as f64, b.height() as f64);
    let (x, y, w, h) = (r2(bx.0), r2(bx.1), r2(bx.2), r2(bx.3));
    let mut n = json!({"type": "path", "x": x, "y": y, "width": w, "height": h, "geometry": d, "viewBox": [x, y, w, h]});
    if p.id() == PEN_ID {
        n["stroke"] = json!(PREVIEW);
        n["strokeWidth"] = json!(r2(outline));
        return Some(n);
    }
    if opacity < 1.0 {
        n["opacity"] = json!(r2(opacity));
    }
    if let Some(f) = p.fill() {
        if let Some(paint) = paint(f.paint(), f.opacity().get() as f64, ts, bx) {
            n["fill"] = paint;
        }
        if f.rule() == usvg::FillRule::EvenOdd {
            n["fillRule"] = json!("evenodd");
        }
    }
    if let Some(s) = p.stroke() {
        if let Some(paint) = paint(s.paint(), s.opacity().get() as f64, ts, bx) {
            let scale = ((ts.sx * ts.sy - ts.kx * ts.ky).abs() as f64).sqrt();
            n["stroke"] = paint;
            n["strokeWidth"] = json!(r2(s.width().get() as f64 * scale));
            n["strokeLinecap"] = json!(match s.linecap() {
                usvg::LineCap::Round => "round",
                usvg::LineCap::Square => "square",
                usvg::LineCap::Butt => "butt",
            });
            n["strokeLinejoin"] = json!(match s.linejoin() {
                usvg::LineJoin::Round => "round",
                usvg::LineJoin::Bevel => "bevel",
                _ => "miter",
            });
        }
    }
    Some(n)
}

fn hex(c: usvg::Color, alpha: f64) -> String {
    let a = (alpha.clamp(0.0, 1.0) * 255.0).round() as u8;
    if a == 255 { format!("#{:02x}{:02x}{:02x}", c.red, c.green, c.blue) } else { format!("#{:02x}{:02x}{:02x}{a:02x}", c.red, c.green, c.blue) }
}

/// SVG paint → Nalarupa fill. Gradients become box-relative ones (see `svg.rs` `gradient`): the
/// SVG gradient geometry in frame space, expressed in the node box's 0..1 units.
fn paint(p: &usvg::Paint, opacity: f64, ts: tsp::Transform, (bx, by, bw, bh): (f64, f64, f64, f64)) -> Option<Value> {
    let (stops, m) = match p {
        usvg::Paint::Color(c) => return Some(json!(hex(*c, opacity))),
        usvg::Paint::LinearGradient(g) => (g.stops(), ts.pre_concat(g.transform())),
        usvg::Paint::RadialGradient(g) => (g.stops(), ts.pre_concat(g.transform())),
        usvg::Paint::Pattern(_) => return None,
    };
    let colors: Vec<Value> = stops.iter().map(|s| json!({"color": hex(s.color(), s.opacity().get() as f64 * opacity), "position": r2(s.offset().get() as f64)})).collect();
    if bw < 1e-6 || bh < 1e-6 {
        return colors.first().map(|c| c["color"].clone());
    }
    let unit = |x: f32, y: f32| {
        let mut pt = tsp::Point::from_xy(x, y);
        m.map_point(&mut pt);
        ((pt.x as f64 - bx) / bw, (pt.y as f64 - by) / bh)
    };
    let r3 = |v: f64| (v * 1000.0).round() / 1000.0;
    Some(match p {
        usvg::Paint::LinearGradient(g) => {
            let (a, z) = (unit(g.x1(), g.y1()), unit(g.x2(), g.y2()));
            let up = ((z.0 - a.0) / 2.0, (z.1 - a.1) / 2.0);
            let phi = up.0.atan2(-up.1);
            json!({"type": "gradient", "gradientType": "linear", "rotation": r2(-phi.to_degrees()),
                   "center": {"x": r3((a.0 + z.0) / 2.0), "y": r3((a.1 + z.1) / 2.0)},
                   "size": {"width": 1, "height": r3(2.0 * up.0.hypot(up.1))}, "colors": colors})
        }
        usvg::Paint::RadialGradient(g) => {
            let (c, ex, ey) = (unit(g.cx(), g.cy()), unit(g.cx() + g.r().get(), g.cy()), unit(g.cx(), g.cy() + g.r().get()));
            let (right, down) = ((ex.0 - c.0, ex.1 - c.1), (ey.0 - c.0, ey.1 - c.1));
            let phi = right.1.atan2(right.0);
            json!({"type": "gradient", "gradientType": "radial", "rotation": r2(-phi.to_degrees()), "center": {"x": r3(c.0), "y": r3(c.1)},
                   "size": {"width": r3(2.0 * right.0.hypot(right.1)), "height": r3(2.0 * down.0.hypot(down.1))}, "colors": colors})
        }
        _ => return None,
    })
}

/// Close a streamed SVG prefix into a well-formed document: complete markup only, then the path
/// or polygon whose geometry is still being written (cut after its last finished number, tagged
/// `PEN_ID`), then end tags for every element still open. None until the `<svg>` tag is complete.
fn close(src: &str) -> Option<String> {
    let mut open: Vec<&str> = Vec::new();
    let (mut cut, mut i) = (0, 0);
    let mut growing = None;
    while let Some(k) = src[i..].find('<') {
        let lt = i + k;
        let rest = &src[lt..];
        let special = [("<!--", "-->"), ("<![CDATA[", "]]>"), ("<?", "?>"), ("<!", ">")].into_iter().find(|(o, _)| rest.starts_with(o));
        let end = match special {
            Some((_, c)) => match rest.find(c) {
                Some(e) => lt + e + c.len(),
                None => break,
            },
            None => match tag_end(rest) {
                Some(e) => lt + e + 1,
                None => {
                    growing = growing_shape(rest, &open);
                    break;
                }
            },
        };
        if special.is_none() {
            let tag = &src[lt + 1..end - 1];
            if let Some(name) = tag.strip_prefix('/') {
                if let Some(at) = open.iter().rposition(|n| *n == name.trim()) {
                    open.truncate(at);
                }
            } else if !tag.ends_with('/') {
                open.push(tag_name(tag));
            }
        }
        cut = end;
        i = end;
    }
    if cut == 0 {
        return None;
    }
    let mut doc = src[..cut].to_string();
    doc.extend(growing);
    for name in open.iter().rev() {
        let _ = write!(doc, "</{name}>");
    }
    Some(doc)
}

/// Offset of the `>` ending the tag that starts `tag`, outside attribute quotes.
fn tag_end(tag: &str) -> Option<usize> {
    let mut quote = None;
    for (i, c) in tag.char_indices().skip(1) {
        match quote {
            Some(q) if c == q => quote = None,
            Some(_) => {}
            None if c == '"' || c == '\'' => quote = Some(c),
            None if c == '>' => return Some(i),
            None => {}
        }
    }
    None
}

fn tag_name(tag: &str) -> &str {
    tag.split(|c: char| c.is_whitespace() || c == '/').next().unwrap_or("")
}

/// The unfinished tag `rest` as a complete preview element when it is a path/polygon/polyline
/// whose geometry attribute is being written (and it would be drawn, i.e. not inside defs).
fn growing_shape(rest: &str, open: &[&str]) -> Option<String> {
    const UNDRAWN: [&str; 8] = ["defs", "clipPath", "mask", "symbol", "pattern", "marker", "linearGradient", "radialGradient"];
    if open.iter().any(|n| UNDRAWN.contains(n)) {
        return None;
    }
    let name = tag_name(&rest[1..]);
    let attr = match name {
        "path" => "d=",
        "polygon" | "polyline" => "points=",
        _ => return None,
    };
    let at = rest.match_indices(attr).filter(|(i, _)| rest[..*i].ends_with(|c: char| c.is_whitespace())).last()?.0 + attr.len();
    let q = rest[at..].chars().next().filter(|c| *c == '"' || *c == '\'')?;
    let value = &rest[at + 1..];
    let value = match value.find(q) {
        // Geometry complete, later attributes still coming: keep showing the outline.
        Some(end) => &value[..end],
        None => value.trim_end_matches(|c: char| c.is_ascii_digit() || matches!(c, '.' | '-' | '+' | 'e' | 'E')),
    };
    let attrs = rest[1 + name.len()..at + 1].replace(" id=", " data-id=");
    Some(format!("<{name} id=\"{PEN_ID}\"{attrs}{value}{q}/>"))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A streamed SVG becomes nodes in the frame while it grows: finished shapes get their paint,
    /// the path being written is an outline cut after its last finished number, gradients map
    /// to box-relative fills, and the final document matches what a full parse gives.
    #[test]
    fn streamed_svg_draws_as_it_grows() {
        let svg = r##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 100"><defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#ff0000"/><stop offset="1" stop-color="#0000ff"/></linearGradient></defs><rect x="0" y="0" width="200" height="100" fill="url(#g)"/><g transform="translate(10 10)"><path fill="#00ff00" stroke="#000" stroke-width="2" d="M0 0 L50 0 L50 40 Z"/></g><circle cx="150" cy="50" r="20" fill="#123456" opacity="0.5"/></svg>"##;
        let size = (400.0, 400.0);
        // Before the root tag is complete there is nothing to draw.
        assert!(draw(&svg[..20], size, false).is_none());

        // Mid-path: the rect is done, the triangle's `d` stops at "M0 0 L50 0 L5".
        let cut = svg.find("L50 40").unwrap() + 2;
        let d = draw(&format!("Sure!\n```svg\n{}", &svg[..cut]), size, false).unwrap();
        assert_eq!(d.nodes.len(), 2);
        let rect = &d.nodes[0];
        // 200×100 fitted into 400×400: scale 2, centered vertically (offset 100).
        assert_eq!((rect["x"].as_f64(), rect["y"].as_f64(), rect["width"].as_f64(), rect["height"].as_f64()), (Some(0.0), Some(100.0), Some(400.0), Some(200.0)));
        assert_eq!(rect["fill"]["gradientType"], "linear");
        assert_eq!(rect["fill"]["rotation"].as_f64().map(f64::abs), Some(180.0), "top-to-bottom gradient points down");
        let growing = &d.nodes[1];
        assert_eq!(growing["stroke"], PREVIEW);
        assert!(growing["fill"].is_null());
        assert_eq!(growing["geometry"], "M20 120L120 120", "translate(10 10) then ×2 and +100 y, cut before the unfinished number");
        assert_eq!(d.point, Some((120.0, 120.0)));

        // Finished: real paint, group transform applied, opacity carried.
        let d = draw(svg, size, true).unwrap();
        assert_eq!(d.nodes.len(), 3);
        let tri = &d.nodes[1];
        assert_eq!((tri["fill"].as_str(), tri["strokeWidth"].as_f64()), (Some("#00ff00"), Some(4.0)));
        assert_eq!(tri["geometry"], "M20 120L120 120L120 200Z");
        assert_eq!(d.nodes[2]["opacity"].as_f64(), Some(0.5));
        assert_eq!(d.nodes[2]["fill"], "#123456");
    }

    /// The Claude Code pen's command carries the picked model, like the agent's own process; with
    /// "Default" (unset) it passes no flag and the CLI's default applies.
    #[test]
    fn cli_passes_the_picked_model_only_when_set() {
        let args = |model| cli("a cat", model).get_args().map(|a| a.to_string_lossy().into_owned()).collect::<Vec<_>>();
        let picked = args(Some("claude-haiku-5-5"));
        let at = picked.iter().position(|a| a == "--model").expect("--model passed");
        assert_eq!(picked[at + 1], "claude-haiku-5-5");
        assert!(!args(None).iter().any(|a| a == "--model"));
    }

    /// Unfinished markup never breaks the parse: an open attribute elsewhere, comments, and an
    /// element id on the growing path are all handled.
    #[test]
    fn close_keeps_prefixes_well_formed() {
        let base = r#"<svg viewBox="0 0 10 10"><!-- hi --><g><path id="a" fill="red" d="M1 1 L9 1 L9 9"#;
        let doc = close(base).unwrap();
        assert!(doc.ends_with(r#"<path id="nalarupa-pen" data-id="a" fill="red" d="M1 1 L9 1 L9 "/></g></svg>"#), "{doc}");
        assert!(usvg::Tree::from_str(&doc, &usvg::Options::default()).is_ok());
        // An unfinished non-geometry attribute waits for the tag to end...
        assert_eq!(close(r#"<svg viewBox="0 0 10 10"><rect fill="re"#).unwrap(), r#"<svg viewBox="0 0 10 10"></svg>"#);
        // ...but a path whose geometry is done stays an outline while its later attributes stream.
        assert!(close(r##"<svg viewBox="0 0 10 10"><path d="M1 1 L9 9" fill="#ab"##).unwrap().contains(r#"<path id="nalarupa-pen" d="M1 1 L9 9"/>"#));
        assert_eq!(close("<sv"), None);
    }
}
