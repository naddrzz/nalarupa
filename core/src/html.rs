//! Export: PNG / JPEG / WebP / SVG files, multi-page PDF decks and HTML+CSS code generation (flexbox maps 1:1).

use crate::engine::Engine;
use crate::render::find_rnode;
use crate::resolve::{num, RNode};
use crate::svg::{blend_mode, color, esc, fills};
use serde_json::Value;
use std::fmt::Write;
use std::path::{Path, PathBuf};

fn out_path(e: &Engine, out: &str, default_name: &str) -> PathBuf {
    let base = e.doc.dir().unwrap_or_else(|| e.data_dir.join("exports"));
    let p = if out.is_empty() { base.join(default_name) } else { PathBuf::from(out) };
    if p.is_absolute() { p } else { base.join(p) }
}

/// Export nodes; returns the written file paths.
pub fn export(e: &mut Engine, ids: &[String], format: &str, out: &str, opts: &Value) -> Result<Vec<String>, String> {
    let mut written = Vec::new();
    let format = if format == "jpg" { "jpeg" } else { format };
    match format {
        "png" | "jpeg" | "webp" | "svg" => {
            let dir = out_path(e, out, "exports");
            std::fs::create_dir_all(&dir).map_err(|err| err.to_string())?;
            let scale = opts["scale"].as_f64().unwrap_or(2.0);
            let quality = opts["quality"].as_f64().unwrap_or(90.0).clamp(1.0, 100.0) as u8;
            for id in ids {
                let file = dir.join(format!("{}.{format}", id.replace('/', "_")));
                let bytes = if format == "svg" {
                    let mut cx = e.ctx();
                    cx.node_svg(id).ok_or_else(|| cx.missing(id))?.0.into_bytes()
                } else {
                    e.ctx().node_image(id, format, scale, quality)?
                };
                std::fs::write(&file, bytes).map_err(|err| err.to_string())?;
                written.push(file.display().to_string());
            }
        }
        "pdf" => {
            // No ids = the whole deck: every visible top-level frame, in document order (hidden slides are skipped, as in Present).
            let ids: Vec<String> = if ids.is_empty() {
                e.doc.children().iter().filter(|n| n["type"] == "frame" && n["enabled"] != false).filter_map(|n| n["id"].as_str().map(str::to_string)).collect()
            } else {
                ids.to_vec()
            };
            if ids.is_empty() {
                return Err("nothing to export: the document has no visible top-level frames".into());
            }
            let file = out_path(e, out, &format!("{}.pdf", e.title()));
            if let Some(dir) = file.parent() {
                std::fs::create_dir_all(dir).map_err(|err| err.to_string())?;
            }
            let pdf = e.ctx().nodes_pdf(&ids)?;
            std::fs::write(&file, pdf).map_err(|err| err.to_string())?;
            written.push(file.display().to_string());
        }
        "html-css" | "html-tailwind" | "html" => {
            let file = out_path(e, out, "export.html");
            if let Some(dir) = file.parent() {
                std::fs::create_dir_all(dir).map_err(|err| err.to_string())?;
            }
            let html = html_document(e, ids, opts)?;
            std::fs::write(&file, html).map_err(|err| err.to_string())?;
            written.push(file.display().to_string());
        }
        other => return Err(format!("unknown export format '{other}' (use png, jpeg, webp, svg, pdf or html-css)")),
    }
    Ok(written)
}

pub fn html_document(e: &mut Engine, ids: &[String], opts: &Value) -> Result<String, String> {
    let names = opts["includeLayerNames"] != false;
    let ids_attr = opts["includeLayerIds"] == true;
    let base = e.doc.dir();
    let mut body = String::new();
    let mut fonts = Vec::new();
    for id in ids {
        let mut cx = e.ctx();
        let top = crate::render::top_index(cx.doc, id).ok_or_else(|| cx.missing(id))?;
        let root = cx.laid_out(top).ok_or_else(|| cx.missing(id))?;
        let n = find_rnode(&root, id).ok_or_else(|| cx.missing(id))?;
        let mut h = Html { out: &mut body, names, ids: ids_attr, base: base.clone(), families: &mut fonts, fonts: &mut e.fonts, assets: &mut e.assets };
        h.node(n, None);
    }
    if opts["includeHtmlScaffold"] == false {
        return Ok(body);
    }
    // One stylesheet per family with only the weights in use: Google rejects a request
    // naming a weight the family does not have.
    fonts.sort();
    fonts.dedup();
    let mut link = String::new();
    let mut i = 0;
    while i < fonts.len() {
        let fam = fonts[i].0.clone();
        let weights: Vec<String> = fonts.iter().filter(|f| f.0 == fam).map(|f| f.1.to_string()).collect();
        i += weights.len();
        link += &format!("<link rel=\"stylesheet\" href=\"https://fonts.googleapis.com/css2?family={}:wght@{}&display=swap\">
", fam.replace(' ', "+"), weights.join(";"));
    }
    Ok(format!(
        "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>{}</title>\n{link}\n<style>*{{box-sizing:border-box;margin:0;padding:0}}body{{display:flex;flex-direction:column;align-items:center;gap:40px;padding:40px 0;background:#f4f4f5}}</style>\n</head>\n<body>\n{body}</body>\n</html>\n",
        esc(&e.title())
    ))
}

struct Html<'a> {
    out: &'a mut String,
    names: bool,
    ids: bool,
    base: Option<PathBuf>,
    families: &'a mut Vec<(String, u16)>,
    fonts: &'a mut crate::text::Fonts,
    assets: &'a mut crate::svg::Assets,
}

fn css_color(v: &str) -> Option<String> {
    css_color_alpha(v, 1.0)
}

/// `css_color` with the alpha scaled by `k` (a paint's `opacity`).
fn css_color_alpha(v: &str, k: f64) -> Option<String> {
    let (c, a) = color(v)?;
    let a = a * k;
    if a >= 0.999 {
        return Some(c);
    }
    let h = c.strip_prefix('#')?;
    let p = |i: usize| u8::from_str_radix(&h[i..i + 2], 16).unwrap_or(0);
    Some(format!("rgba({}, {}, {}, {:.3})", p(0), p(2), p(4), a))
}

/// CSS gradient of a gradient paint. Backgrounds have no per-layer opacity, so `opacity` goes into the stop alphas.
fn gradient_css(o: &serde_json::Map<String, Value>) -> Option<String> {
    let opacity = o.get("opacity").and_then(num).unwrap_or(1.0).clamp(0.0, 1.0);
    // Like the canvas: positions clamped to 0..1 and sorted.
    // An unparseable colour is transparent black, not a dropped stop.
    let mut stops: Vec<(f64, String)> = o.get("colors").and_then(Value::as_array).into_iter().flatten().map(|c| {
        let position = num(&c["position"]).unwrap_or(0.0).clamp(0.0, 1.0);
        let col = c["color"].as_str().and_then(|s| css_color_alpha(s, opacity)).unwrap_or_else(|| "rgba(0, 0, 0, 0)".into());
        (position, col)
    }).collect();
    if stops.is_empty() {
        return None;
    }
    stops.sort_by(|a, b| a.0.total_cmp(&b.0));
    let rot = o.get("rotation").and_then(num).unwrap_or(0.0);
    let pt = |k: &str, a: &str, d: f64| o.get(k).and_then(|v| v.get(a)).and_then(num).unwrap_or(d);
    let at = |k: &str| pt("center", k, 0.5) * 100.0;
    let kind = o.get("gradientType").and_then(Value::as_str);
    // CSS has no shifted or resized linear gradient, nor a rotated ellipse (a multiple of 90deg or a circle needs none).
    let quarter = (rot / 90.0 - (rot / 90.0).round()).abs() < 1e-9;
    let (rw, rh) = (pt("size", "width", 1.0), pt("size", "height", 1.0));
    let beyond_css = match kind {
        Some("radial") => !quarter && rw != rh,
        Some("angular") => false,
        _ => at("x") != 50.0 || at("y") != 50.0 || rh != 1.0,
    };
    if beyond_css {
        return Some(svg_gradient(kind == Some("radial"), (at("x") / 100.0, at("y") / 100.0), (rw, rh), rot, &stops));
    }
    let stops: Vec<String> = stops.into_iter().map(|(p, c)| format!("{c} {}%", p * 100.0)).collect();
    Some(match kind {
        // The canvas ellipse spans `size` of the box; CSS radii are box percentages.
        Some("radial") => {
            let (rx, ry) = (rw / 2.0, rh / 2.0);
            let (sn, cs) = (-rot).to_radians().sin_cos();
            let r = |a: f64, b: f64| (a.hypot(b) * 100.0).max(1e-4);
            format!("radial-gradient({}% {}% at {}% {}%,{})", r(rx * cs, ry * sn), r(rx * sn, ry * cs), at("x"), at("y"), stops.join(","))
        }
        // Angle 0 points up and runs clockwise, like the canvas; rotation is CCW.
        Some("angular") => format!("conic-gradient(from {}deg at {}% {}%,{})", -rot, at("x"), at("y"), stops.join(",")),
        _ => format!("linear-gradient({}deg,{})", -rot, stops.join(",")),
    })
}

/// A gradient as an SVG background over the unit box, stretched to the element, with the geometry of `svg.rs`:
/// the frame (rotated CCW, then scaled with the box) is `size` wide and tall around `center`.
fn svg_gradient(radial: bool, (cx, cy): (f64, f64), (w, h): (f64, f64), rot: f64, stops: &[(f64, String)]) -> String {
    let half = |v: f64| if v == 0.0 { 1e-6 } else { v / 2.0 };
    let (rx, ry) = (half(w), half(h));
    let (sn, cs) = (-rot).to_radians().sin_cos();
    let (up, right) = ((ry * sn, -ry * cs), (rx * cs, rx * sn));
    let stops: String = stops.iter().map(|(p, c)| format!("<stop offset=\"{}\" stop-color=\"{c}\"/>", px(*p))).collect();
    let g = if radial {
        format!(
            "<radialGradient id=\"g\" cx=\"0\" cy=\"0\" r=\"1\" gradientTransform=\"matrix({} {} {} {} {} {})\">{stops}</radialGradient>",
            px(right.0), px(right.1), px(-up.0), px(-up.1), px(cx), px(cy)
        )
    } else {
        format!(
            "<linearGradient id=\"g\" x1=\"{}\" y1=\"{}\" x2=\"{}\" y2=\"{}\">{stops}</linearGradient>",
            px(cx - up.0), px(cy - up.1), px(cx + up.0), px(cy + up.1)
        )
    };
    let svg = format!("<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 1 1\" preserveAspectRatio=\"none\"><defs>{g}</defs><rect width=\"1\" height=\"1\" fill=\"url(#g)\"/></svg>");
    let uri = svg.replace('%', "%25").replace('"', "%22").replace('<', "%3C").replace('>', "%3E").replace('#', "%23");
    format!("url('data:image/svg+xml,{uri}') 0 0/100% 100% no-repeat")
}

/// A gradient stroke: the gradient fills a box moved out by `reach` of each side width, masked down to its per-side ring.
fn stroke_overlay(grad: &str, w: &Value, alignment: Option<&str>, blend: Option<&str>) -> Option<String> {
    let sides = match w {
        Value::Object(s) => ["top", "right", "bottom", "left"].map(|k| s.get(k).and_then(num).unwrap_or(0.0).max(0.0)),
        v => [num(v).filter(|w| *w > 0.0)?; 4],
    };
    // The box reaches out by the whole width (outer), half of it (centered) or not at all (inner), per side.
    let reach = match alignment {
        Some("inner" | "inside") => 0.0,
        Some("outer" | "outside") => 1.0,
        _ => 0.5,
    };
    let px = |f: f64| if sides.iter().all(|w| *w == sides[0]) { format!("{}px", sides[0] * f + 0.0) } else { sides.map(|w| format!("{}px", w * f + 0.0)).join(" ") };
    let (inset, pad) = (px(-reach), px(1.0));
    let blend = blend.map(|b| format!("mix-blend-mode:{b};")).unwrap_or_default();
    Some(format!(
        "<div style=\"{}inset:{inset};padding:{pad};background:{grad};{blend}-webkit-mask:linear-gradient(#000 0 0) content-box,linear-gradient(#000 0 0);-webkit-mask-composite:xor;mask:linear-gradient(#000 0 0) content-box,linear-gradient(#000 0 0);mask-composite:exclude;\"></div>\n",
        OVERLAY
    ))
}

/// Overlay boxes sit above the node's own background and below its children.
const OVERLAY: &str = "position:absolute;z-index:-1;border-radius:inherit;pointer-events:none;";

/// The fills bottom to top as CSS layers, with the paint's opacity where CSS cannot fold it into the layer, and its blend mode.
fn fill_layers(n: &RNode) -> Vec<(String, f64, Option<&'static str>)> {
    let mut layers: Vec<(String, f64, Option<&'static str>)> = Vec::new();
    for f in fills(n.p.get("fill")) {
        let first = layers.len();
        match f {
            Value::String(s) => layers.extend(css_color(s).map(|c| (format!("linear-gradient({c},{c})"), 1.0, None))),
            Value::Object(o) => match o.get("type").and_then(Value::as_str).unwrap_or("color") {
                "color" => layers.extend(o.get("color").and_then(Value::as_str).and_then(css_color).map(|c| (format!("linear-gradient({c},{c})"), 1.0, None))),
                "gradient" => layers.extend(gradient_css(o).map(|g| (g, 1.0, None))),
                "image" => {
                    let url = o.get("url").and_then(Value::as_str).unwrap_or("");
                    if !url.is_empty() {
                        let size = match o.get("mode").and_then(Value::as_str) {
                            Some("fit") => "contain",
                            Some("fill") => "cover",
                            _ => "100% 100%",
                        };
                        let opacity = o.get("opacity").and_then(num).unwrap_or(1.0).clamp(0.0, 1.0);
                        layers.push((format!("url('{}') center/{size} no-repeat", esc(url)), opacity, None));
                    }
                }
                _ => {}
            },
            _ => {}
        }
        // The paint just pushed (if any) blends with what is under it.
        if let Some(l) = layers.get_mut(first) {
            l.2 = blend_mode(f["blendMode"].as_str());
        }
    }
    layers
}

/// A bottom fill or a stroke with a blend mode blends with what lies beneath the element, not just its own layers.
fn blends_backdrop(n: &RNode) -> bool {
    n.ty != "text" && n.ty != "icon" && (fill_layers(n).first().is_some_and(|l| l.2.is_some()) || fills(n.p.get("stroke")).iter().any(|s| blend_mode(s["blendMode"].as_str()).is_some()))
}

fn size_css(v: Option<&Value>, prop: &str, parent_dir: Option<&str>, horiz_axis: bool, measured: f64) -> String {
    let main_axis = match parent_dir {
        Some("horizontal") => horiz_axis,
        Some("vertical") => !horiz_axis,
        // Like the layout engine, any other layout value on a frame (e.g. an imported one) is a row.
        Some(dir) if dir != "none" => horiz_axis,
        _ => false,
    };
    match v {
        Some(Value::String(s)) if s.starts_with("fill_container") => {
            if parent_dir.is_some() && parent_dir != Some("none") {
                if main_axis { "flex:1 1 0;min-width:0;min-height:0;".into() } else { "align-self:stretch;".into() }
            } else {
                format!("{prop}:100%;")
            }
        }
        Some(Value::String(s)) if s.starts_with("fit_content") => format!("{prop}:fit-content;"),
        // Fixed main-axis sizes do not shrink (the canvas lets them overflow); CSS flex items would.
        Some(v) => num(v).map(|n| format!("{prop}:{n}px;{}", if main_axis { "flex-shrink:0;" } else { "" })).unwrap_or_default(),
        None => {
            let _ = measured;
            String::new()
        }
    }
}

/// Rounded so trig noise (6e-15 at 90deg) stays out of the CSS; `+ 0.0` turns -0 into 0.
fn px(v: f64) -> f64 {
    (v * 1e6).round() / 1e6 + 0.0
}

impl Html<'_> {
    fn node(&mut self, n: &RNode, parent: Option<&RNode>) {
        let in_flow = parent.is_some_and(|p| p.ty == "frame" && p.s("layout") != Some("none")) && n.s("layoutPosition") != Some("absolute");
        let tilted = n.f("rotation").is_some_and(|r| r != 0.0) || n.b("flipX") || n.b("flipY");
        if !(in_flow && tilted) {
            return self.element(n, parent, None);
        }
        // The canvas places a rotated or flipped child by its bounding box, so it takes that much room in the
        // flow: the box is a wrapper of that size and the element sits in it with its origin where the layout put it.
        let l = crate::layout::local_aabb(n);
        let _ = writeln!(self.out, "<div style=\"position:relative;flex-shrink:0;width:{}px;height:{}px;\">", px(l[2] - l[0]), px(l[3] - l[1]));
        self.element(n, parent, Some((n.bx - l[0], n.by - l[1])));
        let _ = writeln!(self.out, "</div>");
    }

    /// `slot` = where the node's unrotated box sits in its wrapper (see `node`).
    fn element(&mut self, n: &RNode, parent: Option<&RNode>, slot: Option<(f64, f64)>) {
        // A wrapped node is sized in its wrapper, not as a flex item.
        let parent_dir = parent.filter(|_| slot.is_none()).map(|p| if p.ty == "frame" { p.s("layout").unwrap_or("horizontal") } else { "none" });
        let absolute = slot.is_some() || parent_dir == Some("none") || n.s("layoutPosition") == Some("absolute");
        let mut css = String::new();
        if parent.is_none() {
            css += "position:relative;";
        } else if let Some((x, y)) = slot {
            let _ = write!(css, "position:absolute;left:{}px;top:{}px;", px(x), px(y));
        } else if absolute {
            let p = parent.unwrap();
            let _ = write!(css, "position:absolute;left:{}px;top:{}px;", n.x - p.x, n.y - p.y);
        } else if parent.is_some_and(blends_backdrop) {
            // The parent's blending overlays are not behind its content: a child must be positioned to paint above them.
            css += "position:relative;";
        }
        let text_auto = n.ty == "text" && n.s("textGrowth").unwrap_or("auto") == "auto";
        if !text_auto {
            // In a wrapper the laid-out size is final: fill and fit have no flow left to resolve against.
            let fixed = slot.map(|_| (Value::from(px(n.w)), Value::from(px(n.h))));
            let (wv, hv) = (fixed.as_ref().map(|f| &f.0).or(n.p.get("width")), fixed.as_ref().map(|f| &f.1).or(n.p.get("height")));
            // fit_content(N) on a node with nothing to hug is N (the laid-out size); the CSS keyword would collapse it to 0.
            let (wl, hl) = (Value::from(px(n.w)), Value::from(px(n.h)));
            let is_fit = |v: Option<&Value>| v.and_then(Value::as_str).is_some_and(|s| s.starts_with("fit_content"));
            // A frame without a size is fit_content too; when it hugs a fill child CSS would size it from that child's content.
            let wv = if is_fit(wv) && !crate::layout::hugs(n, 0) || crate::layout::hugs_fill(n, 0) { Some(&wl) } else { wv };
            let hv = if is_fit(hv) && !crate::layout::hugs(n, 1) || crate::layout::hugs_fill(n, 1) { Some(&hl) } else { hv };
            css += &size_css(wv, "width", parent_dir, true, n.w);
            css += &size_css(hv, "height", parent_dir, false, n.h);
            // Fixed-width text without a width wraps at width 0 on the canvas: one word per line.
            if n.ty == "text" && wv.is_none() {
                css += &size_css(Some(&Value::from(px(n.w))), "width", parent_dir, true, n.w);
            }
            if matches!(n.ty.as_str(), "rectangle" | "ellipse" | "icon" | "polygon" | "line" | "path") {
                if wv.is_none() {
                    css += &size_css(Some(&Value::from(n.w)), "width", parent_dir, true, n.w);
                }
                if hv.is_none() {
                    css += &size_css(Some(&Value::from(n.h)), "height", parent_dir, false, n.h);
                }
            }
        }
        if let Some(o) = n.f("opacity") {
            let _ = write!(css, "opacity:{o};");
        }
        // Rotation is CCW and flips are about the top-left corner.
        let mut tf = String::new();
        if let Some(r) = n.f("rotation").filter(|r| *r != 0.0) {
            let _ = write!(tf, "rotate({}deg)", -r);
        }
        if n.b("flipX") || n.b("flipY") {
            let _ = write!(tf, " scale({},{})", if n.b("flipX") { -1 } else { 1 }, if n.b("flipY") { -1 } else { 1 });
        }
        if !tf.is_empty() {
            // A group's box starts at (bx, by) from the origin it turns about.
            let _ = write!(css, "transform:{};transform-origin:{}px {}px;", tf.trim_start(), px(-n.bx), px(-n.by));
        }
        // Shapes CSS cannot draw are inlined as SVG, which carries their own paint.
        let as_svg = matches!(n.ty.as_str(), "path" | "polygon" | "line") || n.ty == "ellipse" && (n.f("innerRadius").unwrap_or(0.0) > 0.0 || n.f("sweepAngle").is_some_and(|s| s.abs() < 360.0));
        let (under, over) = if as_svg { Default::default() } else { self.paint_css(n, &mut css) };
        let attrs = self.attrs(n);
        match n.ty.as_str() {
            "text" => {
                self.text_css(n, &mut css);
                let content = n.s("content").unwrap_or("");
                let _ = writeln!(self.out, "<p{attrs} style=\"{css}\">{}</p>", esc(content).replace('\n', "<br>"));
            }
            "icon" => {
                // No fill = invisible icon.
                let fill = fills(n.p.get("fill")).first().and_then(|f| f.as_str().or_else(|| f["color"].as_str())).and_then(css_color);
                let svg = match fill {
                    Some(fill) => {
                        let svg = self.assets.icon_weight(n.s("library").unwrap_or("lucide"), n.s("icon").unwrap_or(""), n.f("weight").unwrap_or(200.0)).unwrap_or_default();
                        svg.find("<svg").map(|i| svg[i..].replacen("<svg", &format!("<svg style=\"width:100%;height:100%;color:{fill}\""), 1)).unwrap_or_default()
                    }
                    None => String::new(),
                };
                let _ = writeln!(self.out, "<span{attrs} style=\"display:inline-flex;{css}\">{svg}</span>");
            }
            _ if as_svg => {
                // The div's CSS already rotates, flips and fades it.
                let mut p = n.p.clone();
                for k in ["rotation", "flipX", "flipY", "opacity"] {
                    p.remove(k);
                }
                let mut shape = RNode::leaf(n.id.clone(), &n.ty, p, None);
                (shape.x, shape.y, shape.w, shape.h) = (n.x, n.y, n.w, n.h);
                let mut svg = crate::svg::Svg::new(self.fonts, self.assets, self.base.clone());
                svg.node(&shape);
                // An inline svg sits on the text baseline (display:block keeps a height-0 line in place) and browsers clip it to its viewport; caps, outer strokes and shadows paint past the box.
                let svg = svg.finish(Some((n.x, n.y, n.w.max(1.0), n.h.max(1.0)))).replacen("<svg ", "<svg style=\"display:block;overflow:visible\" ", 1);
                let _ = writeln!(self.out, "<div{attrs} style=\"{css}\">{svg}</div>");
            }
            _ => {
                if n.ty == "frame" {
                    match n.s("layout").unwrap_or("horizontal") {
                        // An absolute frame already contains its children; a later position:relative would override it.
                        "none" => {
                            if !absolute && !css.contains("position:") {
                                css += "position:relative;";
                            }
                        }
                        dir => {
                            let _ = write!(css, "display:flex;flex-direction:{};", if dir == "vertical" { "column" } else { "row" });
                            // space_between/space_around ignore the gap.
                            if let Some(g) = n.f("gap").filter(|_| !matches!(n.s("justifyContent"), Some("space_between" | "space_around"))) {
                                let _ = write!(css, "gap:{g}px;");
                            }
                            let j = match n.s("justifyContent") {
                                Some("center") => "center",
                                Some("end") => "flex-end",
                                Some("space_between") => "space-between",
                                Some("space_around") => "space-around",
                                _ => "flex-start",
                            };
                            let a = match n.s("alignItems") {
                                Some("center") => "center",
                                Some("end") => "flex-end",
                                _ => "flex-start",
                            };
                            let _ = write!(css, "justify-content:{j};align-items:{a};");
                            if parent.is_some() && !absolute {
                                css += "flex-shrink:0;";
                            }
                        }
                    }
                    let p = crate::layout::padding(n);
                    if p.iter().any(|v| *v != 0.0) {
                        let _ = write!(css, "padding:{}px {}px {}px {}px;", p[0], p[1], p[2], p[3]);
                    }
                    if n.b("clip") {
                        css += "overflow:hidden;";
                    }
                } else if n.ty == "group" {
                    if !absolute && !css.contains("position:") {
                        css += "position:relative;";
                    }
                    css += &size_css(Some(&Value::from(n.w)), "width", parent_dir, true, n.w);
                    css += &size_css(Some(&Value::from(n.h)), "height", parent_dir, false, n.h);
                } else if n.ty == "ellipse" {
                    css += "border-radius:50%;";
                }
                let _ = writeln!(self.out, "<div{attrs} style=\"{css}\">");
                self.out.push_str(&under);
                for k in &n.kids {
                    self.node(k, Some(n));
                }
                self.out.push_str(&over);
                let _ = writeln!(self.out, "</div>");
            }
        }
    }

    fn attrs(&self, n: &RNode) -> String {
        let mut a = String::new();
        if self.names {
            let _ = write!(a, " data-name=\"{}\"", esc(n.name()));
        }
        if self.ids {
            let _ = write!(a, " data-id=\"{}\"", esc(&n.id));
        }
        a
    }

    /// Background, radius, stroke, shadows and blur. Returns the overlay boxes (translucent image fills, gradient
    /// strokes) that go first inside the element, and the one that goes after its children (the inner stroke of a
    /// clipping frame, which is drawn above them).
    fn paint_css(&mut self, n: &RNode, css: &mut String) -> (String, String) {
        let mut shadows: Vec<String> = Vec::new();
        let mut above: Vec<String> = Vec::new();
        let mut under = String::new();
        let mut over = String::new();
        if n.ty != "text" && n.ty != "icon" {
            let layers = fill_layers(n);
            // CSS lists the topmost background first. A translucent image cannot be a background layer: it and
            // every fill above it become overlays, and so do all fills when the bottom one blends with the backdrop
            // (a background only blends with its own layers).
            let split = if layers.first().is_some_and(|l| l.2.is_some()) { 0 } else { layers.iter().position(|l| l.1 < 1.0).unwrap_or(layers.len()) };
            if split > 0 {
                let _ = write!(css, "background:{};", layers[..split].iter().rev().map(|l| l.0.as_str()).collect::<Vec<_>>().join(","));
                if layers[..split].iter().any(|l| l.2.is_some()) {
                    let _ = write!(css, "background-blend-mode:{};", layers[..split].iter().rev().map(|l| l.2.unwrap_or("normal")).collect::<Vec<_>>().join(","));
                }
            }
            for (l, a, b) in &layers[split..] {
                let blend = b.map(|b| format!("mix-blend-mode:{b};")).unwrap_or_default();
                let _ = writeln!(under, "<div style=\"{OVERLAY}inset:0;background:{l};opacity:{a};{blend}\"></div>");
            }
            let r = crate::svg::radii(n);
            if r.iter().any(|v| *v > 0.0) {
                let _ = write!(css, "border-radius:{}px {}px {}px {}px;", r[0], r[1], r[2], r[3]);
            }
            // No stroke without a width; alignment defaults to center. Every stroke is painted, bottom to top.
            let strokes = fills(n.p.get("stroke"));
            let blends = strokes.iter().any(|s| blend_mode(s["blendMode"].as_str()).is_some());
            for paint in &strokes {
                let stroke = paint.as_str().map(str::to_string).or_else(|| paint["color"].as_str().map(str::to_string)).and_then(|c| css_color(&c));
                let gradient = paint.as_object().filter(|o| o.get("type").and_then(Value::as_str) == Some("gradient")).and_then(gradient_css);
                // A blending stroke is a masked ring too: only an overlay can blend with the layers beneath. Once one
                // is, every stroke is, so the stack keeps its order (a border or box-shadow would paint below it).
                if let (true, Some(w)) = (blends, n.p.get("strokeWidth")) {
                    if let Some(l) = gradient.clone().or_else(|| stroke.clone().map(|c| format!("linear-gradient({c},{c})"))) {
                        under += &stroke_overlay(&l, w, n.s("strokeAlignment"), blend_mode(paint["blendMode"].as_str())).unwrap_or_default();
                        continue;
                    }
                }
                match (stroke, gradient, n.p.get("strokeWidth")) {
                    // A border has one colour and lies inside the box: only a lone inner stroke can use it, the rest become masked rings.
                    (Some(c), _, Some(Value::Object(s))) if strokes.len() == 1 && matches!(n.s("strokeAlignment"), Some("inner" | "inside")) => {
                        for side in ["top", "right", "bottom", "left"] {
                            if let Some(w) = s.get(side).and_then(num).filter(|w| *w > 0.0) {
                                let _ = write!(css, "border-{side}:{w}px solid {c};");
                            }
                        }
                    }
                    (Some(c), _, Some(w @ Value::Object(s))) => {
                        let (grad, align) = (format!("linear-gradient({c},{c})"), n.s("strokeAlignment"));
                        if n.ty == "frame" && n.b("clip") && !matches!(align, Some("outer" | "outside")) {
                            // The inner part of the ring is drawn after the children, which would cover it below them; a centered stroke
                            // keeps its outer half where it was (overflow clips it).
                            let half = if matches!(align, Some("inner" | "inside")) { w.clone() } else { Value::Object(s.iter().map(|(k, v)| (k.clone(), Value::from(num(v).unwrap_or(0.0) / 2.0))).collect()) };
                            over += &stroke_overlay(&grad, &half, Some("inner"), None).unwrap_or_default().replace("z-index:-1;", "");
                            if !matches!(align, Some("inner" | "inside")) {
                                under += &stroke_overlay(&grad, &half, Some("outer"), None).unwrap_or_default();
                            }
                        } else {
                            under += &stroke_overlay(&grad, w, align, None).unwrap_or_default();
                        }
                    }
                    (Some(c), _, Some(v)) => {
                        if let Some(w) = num(v).filter(|w| *w > 0.0) {
                            // The first box-shadow is the topmost, so each stroke goes in front of the ones below it.
                            let own = match n.s("strokeAlignment") {
                                Some("inner" | "inside") => vec![format!("inset 0 0 0 {w}px {c}")],
                                Some("outer" | "outside") => vec![format!("0 0 0 {w}px {c}")],
                                _ => vec![format!("inset 0 0 0 {}px {c}", w / 2.0), format!("0 0 0 {}px {c}", w / 2.0)],
                            };
                            // An inset shadow paints below the children: a clipping frame draws it in an overlay after them.
                            let (inside, rest): (Vec<_>, Vec<_>) = own.into_iter().partition(|s| n.ty == "frame" && n.b("clip") && s.starts_with("inset "));
                            above.splice(0..0, inside);
                            shadows.splice(0..0, rest);
                        }
                    }
                    (None, Some(g), Some(w)) => under += &stroke_overlay(&g, w, n.s("strokeAlignment"), None).unwrap_or_default(),
                    _ => {}
                }
            }
        }
        let effects = match n.p.get("effect") {
            Some(Value::Array(a)) => a.iter().collect::<Vec<_>>(),
            Some(v @ Value::Object(_)) => vec![v],
            _ => vec![],
        };
        // (inner, offset x, offset y, blur, colour). Defaults: opaque black, no blur; an invalid color is transparent.
        let list: Vec<(bool, f64, f64, f64, String)> = effects.into_iter().filter(|e| e["type"] == "shadow" && e["enabled"] != false).filter_map(|e| {
            let c = css_color(e["color"].as_str().unwrap_or("#000000"))?;
            Some((e["shadowType"] == "inner", num(&e["offset"]["x"]).unwrap_or(0.0), num(&e["offset"]["y"]).unwrap_or(0.0), num(&e["blur"]).unwrap_or(0.0), c))
        }).collect();
        // The box has no shadow of its own to follow: text and groups cast the shadow of their glyphs and children,
        // and CSS cannot draw an inner shadow there.
        let mut filters: Vec<String> = Vec::new();
        let gradient_text = n.ty == "text" && fills(n.p.get("fill")).first().is_some_and(|f| f["type"] == "gradient");
        if matches!(n.ty.as_str(), "text" | "group" | "icon") {
            let outer = list.iter().filter(|s| !s.0);
            if n.ty == "text" && !gradient_text {
                let t: Vec<String> = outer.map(|(_, x, y, b, c)| format!("{x}px {y}px {b}px {c}")).collect();
                if !t.is_empty() {
                    let _ = write!(css, "text-shadow:{};", t.join(","));
                }
            } else {
                // A text-shadow would show through gradient text, whose colour is transparent. `drop-shadow()` takes
                // the standard deviation, half the blur radius.
                let outer: Vec<_> = outer.collect();
                if let [(_, x, y, b, c)] = outer[..] {
                    filters.push(format!("drop-shadow({x}px {y}px {}px {c})", b / 2.0));
                } else if !outer.is_empty() {
                    // Chained `drop-shadow()`s would cast shadows of the earlier shadows: every one needs the original alpha.
                    filters.push(shadow_filter(&outer));
                }
            }
        } else {
            shadows.extend(list.iter().map(|(inner, x, y, b, c)| format!("{}{x}px {y}px {b}px {c}", if *inner { "inset " } else { "" })));
        }
        if !above.is_empty() {
            let _ = writeln!(over, "<div style=\"position:absolute;inset:0;border-radius:inherit;pointer-events:none;box-shadow:{};\"></div>", above.join(","));
        }
        // The overlays after the children stack against this box.
        if !over.is_empty() && !css.contains("position:") {
            css.push_str("position:relative;");
        }
        // Overlays would cover the element's own inset shadows (inner strokes and shadows): paint those above them.
        if !under.is_empty() {
            let inner: Vec<String> = shadows.iter().filter(|s| s.starts_with("inset ")).cloned().collect();
            shadows.retain(|s| !s.starts_with("inset "));
            if !inner.is_empty() {
                let _ = writeln!(under, "<div style=\"{OVERLAY}inset:0;box-shadow:{};\"></div>", inner.join(","));
            }
            // Overlays stack against this box, inside it.
            if !css.contains("position:") {
                css.push_str("position:relative;");
            }
            // Overlays that blend with the backdrop go in the page's stacking order, not in a box of their own.
            if blends_backdrop(n) {
                under = under.replace("z-index:-1;", "");
            } else {
                css.push_str("isolation:isolate;");
            }
        }
        if !shadows.is_empty() {
            let _ = write!(css, "box-shadow:{};", shadows.join(","));
        }
        // CSS `blur()` takes the standard deviation, which is half the radius.
        let fx = crate::svg::Effects::of(n);
        if fx.blur > 0.0 {
            filters.push(format!("blur({}px)", (fx.blur * 100.0).round() / 100.0));
        }
        if !filters.is_empty() {
            let _ = write!(css, "filter:{};", filters.join(" "));
        }
        // Like the canvas, a node below full opacity shows no background blur.
        if fx.backdrop > 0.0 && n.f("opacity").unwrap_or(1.0) >= 1.0 {
            let b = (fx.backdrop * 100.0).round() / 100.0;
            let _ = write!(css, "backdrop-filter:blur({b}px);-webkit-backdrop-filter:blur({b}px);");
        }
        (under, over)
    }

    fn text_css(&mut self, n: &RNode, css: &mut String) {
        let st = crate::text::TextStyle::of(n);
        self.families.push((st.family.clone(), st.weight));
        let _ = write!(css, "font-family:'{}',sans-serif;font-size:{}px;font-weight:{};", esc(&st.family), st.size, st.weight);
        if st.italic {
            css.push_str("font-style:italic;");
        }
        // Line heights round to whole pixels.
        if let Some(lh) = st.line_height.map(|lh| (lh * st.size).round()).filter(|lh| *lh > 0.0) {
            let _ = write!(css, "line-height:{lh}px;");
        }
        if st.letter_spacing != 0.0 {
            let _ = write!(css, "letter-spacing:{}px;", st.letter_spacing);
        }
        if let Some(a) = n.s("textAlign") {
            let _ = write!(css, "text-align:{a};");
        }
        // Like the canvas, fixed-width text keeps its newlines and runs of spaces and breaks a word wider than the box,
        // except in a box of width 0, where a word is never split.
        if n.s("textGrowth").unwrap_or("auto") == "auto" {
            css.push_str("white-space:pre;");
        } else if n.w > 0.0 {
            css.push_str("white-space:pre-wrap;overflow-wrap:anywhere;");
        } else {
            css.push_str("white-space:pre-wrap;");
        }
        let mut deco = Vec::new();
        if n.b("underline") {
            deco.push("underline");
        }
        if n.b("strikethrough") {
            deco.push("line-through");
        }
        if !deco.is_empty() {
            let _ = write!(css, "text-decoration:{};", deco.join(" "));
        }
        let gradient = fills(n.p.get("fill")).first().and_then(|f| f.as_object().filter(|o| o.get("type").and_then(Value::as_str) == Some("gradient"))).and_then(gradient_css);
        if let Some(g) = gradient {
            let _ = write!(css, "background:{g};-webkit-background-clip:text;background-clip:text;color:transparent;");
            // Decorations default to the transparent colour; an opaque one lets the clipped gradient show through them.
            if !deco.is_empty() {
                css.push_str("text-decoration-color:#000000;");
            }
        } else if let Some(c) = fills(n.p.get("fill")).first().and_then(|f| f.as_str().map(str::to_string).or_else(|| f["color"].as_str().map(str::to_string))).and_then(|c| css_color(&c)) {
            let _ = write!(css, "color:{c};");
        }
        // Like shapes, text takes the top width; CSS has no inner stroke, so it paints centered.
        let width = match n.p.get("strokeWidth") {
            Some(Value::Object(s)) => s.get("top").and_then(num),
            v => v.and_then(num),
        };
        let stroke = fills(n.p.get("stroke")).first().and_then(|f| f.as_str().map(str::to_string).or_else(|| f["color"].as_str().map(str::to_string))).and_then(|c| css_color(&c));
        if let (Some(c), Some(w)) = (stroke, width.filter(|w| *w > 0.0)) {
            // Stroke-only text is an outline: the inherited color must not fill the glyphs.
            if fills(n.p.get("fill")).is_empty() {
                css.push_str("color:transparent;");
            }
            if matches!(n.s("strokeAlignment"), Some("outer" | "outside")) {
                let _ = write!(css, "-webkit-text-stroke:{}px {c};paint-order:stroke fill;", w * 2.0);
            } else {
                let _ = write!(css, "-webkit-text-stroke:{w}px {c};");
            }
        }
    }
}

/// One `filter` casting every outer shadow from the element's own alpha (unlike chained `drop-shadow()`s, which
/// shadow each other), as an inline SVG filter. Its blur is the standard deviation, half the radius.
fn shadow_filter(shadows: &[&(bool, f64, f64, f64, String)]) -> String {
    let mut f = String::new();
    for (i, (_, x, y, b, c)) in shadows.iter().enumerate() {
        let _ = write!(f, "<feGaussianBlur in=\"SourceAlpha\" stdDeviation=\"{}\"/><feOffset dx=\"{x}\" dy=\"{y}\" result=\"o{i}\"/><feFlood flood-color=\"{c}\"/><feComposite in2=\"o{i}\" operator=\"in\" result=\"s{i}\"/>", b / 2.0);
    }
    let merge: String = (0..shadows.len()).rev().map(|i| format!("<feMergeNode in=\"s{i}\"/>")).collect();
    // The region must hold every offset, blurred shadow: a percentage of the element would clip big ones. The element's
    // size is not known here, so it starts a margin before the origin and runs far past any element.
    let m = shadows.iter().map(|(_, x, y, b, _)| x.abs().max(y.abs()) + b * 1.5).fold(0.0, f64::max).ceil() + 1.0;
    let svg = format!("<svg xmlns=\"http://www.w3.org/2000/svg\"><filter id=\"s\" filterUnits=\"userSpaceOnUse\" x=\"-{m}\" y=\"-{m}\" width=\"100000\" height=\"100000\" color-interpolation-filters=\"sRGB\">{f}<feMerge>{merge}<feMergeNode in=\"SourceGraphic\"/></feMerge></filter></svg>");
    format!("url('data:image/svg+xml,{}#s')", svg.replace('%', "%25").replace('<', "%3C").replace('>', "%3E").replace('#', "%23").replace('"', "%22"))
}

pub fn file_name(p: &Path) -> String {
    p.file_name().map(|s| s.to_string_lossy().to_string()).unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use crate::engine::Engine;
    use serde_json::json;

    /// The `style` of a rectangle exported as an HTML fragment, plus the whole fragment.
    fn rect_html(extra: serde_json::Value) -> (String, String) {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let mut node = json!({"type": "rectangle", "x": 0, "y": 0, "width": 160, "height": 100});
        node.as_object_mut().unwrap().extend(extra.as_object().unwrap().clone());
        let id = e.insert_root(node).unwrap();
        let html = super::html_document(&mut e, &[id], &json!({"includeHtmlScaffold": false})).unwrap();
        let style = html.split("style=\"").nth(1).unwrap().split('"').next().unwrap().to_string();
        (style, html)
    }

    #[test]
    fn html_paints_match_the_canvas() {
        let grad = |extra: serde_json::Value| {
            let mut g = json!({"type": "gradient", "rotation": 90, "colors": [{"color": "#ff0000", "position": 0}, {"color": "#0000ff", "position": 1}]});
            g.as_object_mut().unwrap().extend(extra.as_object().unwrap().clone());
            g
        };
        // The first fill is painted first, so it is the last CSS layer.
        let (s, _) = rect_html(json!({"fill": ["#ff0000", "#0000ff80"]}));
        assert!(s.contains("background:linear-gradient(rgba(0, 0, 255, 0.502),rgba(0, 0, 255, 0.502)),linear-gradient(#ff0000,#ff0000);"), "{s}");
        // Gradient opacity is folded into the stops.
        let (s, _) = rect_html(json!({"fill": grad(json!({"opacity": 0.4}))}));
        assert!(s.contains("linear-gradient(-90deg,rgba(255, 0, 0, 0.400) 0%,rgba(0, 0, 255, 0.400) 100%)"), "{s}");
        // A translucent image and the fills above it are overlays; the fills below stay backgrounds.
        let img = json!({"type": "image", "url": "a.png", "mode": "stretch", "opacity": 0.5});
        let (s, h) = rect_html(json!({"fill": ["#ffff00", img.clone(), "#00ff0080"]}));
        assert!(s.contains("background:linear-gradient(#ffff00,#ffff00);") && s.contains("isolation:isolate"), "{s}");
        let (i, g) = (h.find("url('a.png')").unwrap(), h.find("rgba(0, 255, 0, 0.502)").unwrap());
        assert!(h.contains("opacity:0.5;") && i < g, "{h}");
        // Inner strokes are not hidden by the overlay.
        let (_, h) = rect_html(json!({"fill": img, "stroke": "#000000", "strokeWidth": 4, "strokeAlignment": "inner"}));
        assert!(h.contains("box-shadow:inset 0 0 0 4px #000000;") && h.find("url('a.png')") < h.rfind("box-shadow:inset"), "{h}");
        // A gradient stroke is a masked ring.
        let (s, h) = rect_html(json!({"fill": "#ffff00", "stroke": grad(json!({})), "strokeWidth": 12}));
        assert!(s.contains("isolation:isolate") && !s.contains("border"), "{s}");
        assert!(h.contains("padding:12px;background:linear-gradient(-90deg,#ff0000 0%,#0000ff 100%);") && h.contains("mask-composite:exclude"), "{h}");
        // A fill blend mode blends with the fills under it; a translucent image's overlay uses mix-blend-mode.
        let (s, _) = rect_html(json!({"fill": ["#ffee00", {"type": "color", "color": "#00ffff", "blendMode": "multiply"}]}));
        assert!(s.contains("background:linear-gradient(#00ffff,#00ffff),linear-gradient(#ffee00,#ffee00);background-blend-mode:multiply,normal;"), "{s}");
        let (_, h) = rect_html(json!({"fill": ["#ffee00", img.clone(), {"type": "color", "color": "#00ffff", "blendMode": "screen"}]}));
        assert!(h.contains("mix-blend-mode:screen;"), "{h}");
        // A bottom fill that blends is an overlay in the page's stacking order (no background, no isolation, no negative z-index),
        // so it blends with the layers beneath the element; fills above it follow as overlays.
        let (s, h) = rect_html(json!({"fill": [{"type": "color", "color": "#00ffff", "blendMode": "multiply"}, "#ff000080"]}));
        assert!(!s.contains("background") && !s.contains("isolation") && !h.contains("z-index"), "{h}");
        assert!(h.contains("background:linear-gradient(#00ffff,#00ffff);opacity:1;mix-blend-mode:multiply;") && h.find("#00ffff") < h.find("rgba(255, 0, 0, 0.502)"), "{h}");
        // A blending stroke is a masked ring with the blend mode.
        let (s, h) = rect_html(json!({"fill": "#ffffff", "stroke": {"type": "color", "color": "#00ffff", "blendMode": "screen"}, "strokeWidth": 6}));
        assert!(s.contains("background:linear-gradient(#ffffff,#ffffff);") && !s.contains("border") && !s.contains("isolation"), "{s}");
        assert!(h.contains("padding:6px;background:linear-gradient(#00ffff,#00ffff);mix-blend-mode:screen;") && !h.contains("z-index"), "{h}");
        // Background blur is a backdrop filter with the standard deviation, like layer blur.
        let (s, _) = rect_html(json!({"fill": "#ffffff40", "effect": {"type": "background_blur", "radius": 16}}));
        assert!(s.contains("backdrop-filter:blur(8px);-webkit-backdrop-filter:blur(8px);"), "{s}");
        let (s, _) = rect_html(json!({"fill": "#ffffff40", "opacity": 0.5, "effect": {"type": "background_blur", "radius": 16}}));
        assert!(!s.contains("backdrop-filter"), "{s}");
        // A blending stroke makes the whole stack rings, so a plain stroke above it stays above it.
        let (s, h) = rect_html(json!({"fill": "#ffffff", "stroke": [{"type": "color", "color": "#00ffff", "blendMode": "screen"}, "#ff0000"], "strokeWidth": 6}));
        assert!(!s.contains("box-shadow") && h.matches("mask-composite:exclude").count() == 2 && h.find("mix-blend-mode:screen") < h.find("#ff0000"), "{h}");
        // Every stroke is painted, the topmost first in the box-shadow list.
        let (s, _) = rect_html(json!({"fill": "#ffffff", "stroke": ["#ff0000", "#0000ff80"], "strokeWidth": 10, "strokeAlignment": "inner"}));
        assert!(s.contains("box-shadow:inset 0 0 0 10px rgba(0, 0, 255, 0.502),inset 0 0 0 10px #ff0000;"), "{s}");
        // Per-side widths of a stroke stack are masked rings, one per stroke.
        let (s, h) = rect_html(json!({"fill": "#ffffff", "stroke": ["#ff0000", "#0000ff80"], "strokeWidth": {"top": 4, "left": 2}}));
        assert!(!s.contains("border") && h.matches("mask-composite:exclude").count() == 2 && h.find("#ff0000") < h.find("rgba(0, 0, 255"), "{h}");
        // Per-side rings follow the stroke alignment: inner stays inside, outer reaches out by each side's width, centered by half.
        for (align, inset) in [("inner", "0px 0px 0px 0px"), ("outer", "-4px 0px 0px -2px"), ("center", "-2px 0px 0px -1px")] {
            let (_, h) = rect_html(json!({"fill": "#ffffff", "stroke": ["#ff0000", "#0000ff80"], "strokeWidth": {"top": 4, "left": 2}, "strokeAlignment": align}));
            assert!(h.contains(&format!("inset:{inset};padding:4px 0px 0px 2px;")), "{align}: {h}");
        }
        // A lone per-side stroke is a CSS border only when inner; centered (the default) and outer rings reach out of the box.
        let w = json!({"top": 4, "left": 2});
        let (s, _) = rect_html(json!({"fill": "#ffffff", "stroke": "#000000", "strokeWidth": w.clone(), "strokeAlignment": "inner"}));
        assert!(s.contains("border-top:4px solid #000000;") && s.contains("border-left:2px solid #000000;"), "{s}");
        for (align, inset) in [(None, "-2px 0px 0px -1px"), (Some("outer"), "-4px 0px 0px -2px")] {
            let (s, h) = rect_html(json!({"fill": "#ffffff", "stroke": "#000000", "strokeWidth": w.clone(), "strokeAlignment": align}));
            assert!(!s.contains("border-") && h.contains(&format!("inset:{inset};padding:4px 0px 0px 2px;")), "{align:?}: {h}");
        }
        // Layer blur: CSS takes the standard deviation, half the radius.
        let (s, _) = rect_html(json!({"fill": "#ff0000", "effect": {"type": "blur", "radius": 4}}));
        assert!(s.contains("filter:blur(2px);"), "{s}");
        // Angular gradients are conic, rotated like the canvas.
        let (s, _) = rect_html(json!({"fill": grad(json!({"gradientType": "angular"}))}));
        assert!(s.contains("conic-gradient(from -90deg at 50% 50%,#ff0000 0%,#0000ff 100%)"), "{s}");
        // Radial gradients carry center and size; stops are sorted like the canvas.
        let (s, _) = rect_html(json!({"fill": grad(json!({"gradientType": "radial", "rotation": 0, "center": {"x": 0.3, "y": 0.4}, "size": {"width": 0.5, "height": 1.0}, "colors": [{"color": "#0000ff", "position": 1.5}, {"color": "#ff0000", "position": 0}]}))}));
        assert!(s.contains("radial-gradient(25% 50% at 30% 40%,#ff0000 0%,#0000ff 100%)"), "{s}");
        // A shortened or shifted linear gradient and a rotated ellipse are SVG backgrounds with the canvas geometry.
        let (s, _) = rect_html(json!({"fill": grad(json!({"size": {"height": 0.5}}))}));
        assert!(s.contains("data:image/svg+xml,%3Csvg") && s.contains("x1=%220.75%22 y1=%220.5%22 x2=%220.25%22 y2=%220.5%22"), "{s}");
        let (s, _) = rect_html(json!({"fill": grad(json!({"center": {"x": 0.25, "y": 0.5}}))}));
        assert!(s.contains("x1=%220.75%22 y1=%220.5%22 x2=%22-0.25%22 y2=%220.5%22"), "{s}");
        let (s, _) = rect_html(json!({"fill": grad(json!({"gradientType": "radial", "rotation": 45, "size": {"width": 0.3, "height": 0.8}}))}));
        assert!(s.contains("radialGradient") && s.contains("matrix(0.106066 -0.106066 0.282843 0.282843 0.5 0.5)"), "{s}");
        // An unparseable stop colour stays as transparent black, like the canvas.
        let (s, _) = rect_html(json!({"fill": grad(json!({"colors": [{"color": "#ff0000", "position": 0}, {"color": "transparent", "position": 1}]}))}));
        assert!(s.contains("linear-gradient(-90deg,#ff0000 0%,rgba(0, 0, 0, 0) 100%)"), "{s}");
    }

    /// The inner stroke of a clipping frame is drawn above its children, so it is an overlay after them, not a box-shadow under them.
    #[test]
    fn clipping_frame_strokes_above_children() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let kid = json!({"type": "rectangle", "name": "kid", "x": 0, "y": 0, "width": 100, "height": 100, "fill": "#ff0000"});
        let mut html = |clip: bool, align: &str| {
            let frame = json!({"type": "frame", "layout": "none", "clip": clip, "width": 100, "height": 100, "fill": "#eeeeee", "stroke": "#0000ff", "strokeWidth": 10, "strokeAlignment": align, "children": [kid.clone()]});
            let id = e.insert_root(frame).unwrap();
            super::html_document(&mut e, &[id], &json!({"includeHtmlScaffold": false})).unwrap()
        };
        let h = html(true, "inner");
        let overlay = "<div style=\"position:absolute;inset:0;border-radius:inherit;pointer-events:none;box-shadow:inset 0 0 0 10px #0000ff;\"></div>";
        assert!(h.find("#ff0000") < h.find(overlay) && h.matches("box-shadow").count() == 1, "{h}");
        // The outer half of a centered stroke stays on the frame, which overflow does not clip.
        let h = html(true, "center");
        assert!(h.contains("box-shadow:0 0 0 5px #0000ff;") && h.find("#ff0000") < h.find("box-shadow:inset 0 0 0 5px #0000ff;"), "{h}");
        // Without clip the children sit inside the stroke as before.
        let h = html(false, "inner");
        assert!(h.contains("box-shadow:inset 0 0 0 10px #0000ff;") && !h.contains("pointer-events"), "{h}");
        // A per-side stroke that cannot be a border is a masked ring: its inner part goes after the children too (a lone one is a border, which no child covers).
        let mut ring = |align: &str| {
            let frame = json!({"type": "frame", "layout": "none", "clip": true, "width": 100, "height": 100, "stroke": ["#0000ff", "#00ff00"], "strokeWidth": {"top": 10, "right": 10, "bottom": 10, "left": 10}, "strokeAlignment": align, "children": [kid.clone()]});
            let id = e.insert_root(frame).unwrap();
            super::html_document(&mut e, &[id], &json!({"includeHtmlScaffold": false})).unwrap()
        };
        let h = ring("inner");
        assert!(h.find("#ff0000") < h.find("padding:10px;") && !h.contains("z-index:-1"), "{h}");
        let h = ring("center");
        assert!(h.find("#ff0000") < h.find("inset:0px;padding:5px;") && h.contains("inset:-5px;padding:5px;") && h.matches("z-index:-1").count() == 2, "{h}");
    }

    /// A frame whose fill blends with the backdrop paints its overlay in tree order: flow children must be positioned to stay above it.
    #[test]
    fn children_stay_above_a_blending_fill() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let kid = json!({"type": "rectangle", "name": "kid", "width": 20, "height": 20, "fill": "#ff0000"});
        let frame = |fill: serde_json::Value| json!({"type": "frame", "layout": "horizontal", "width": 100, "height": 60, "fill": fill, "children": [kid.clone()]});
        let mut html = |fill: serde_json::Value| {
            let id = e.insert_root(frame(fill)).unwrap();
            super::html_document(&mut e, &[id], &json!({"includeHtmlScaffold": false})).unwrap()
        };
        let h = html(json!({"type": "color", "color": "#00ffff", "blendMode": "multiply"}));
        let line = h.lines().find(|l| l.contains("data-name=\"kid\"")).unwrap();
        assert!(line.contains("position:relative;") && h.contains("mix-blend-mode:multiply;"), "{h}");
        let h = html(json!("#00ffff"));
        assert!(!h.lines().find(|l| l.contains("data-name=\"kid\"")).unwrap().contains("position:relative;"), "{h}");
    }

    /// Text and groups cast the shadow of their glyphs and children; gradient text keeps its fill.
    #[test]
    fn text_and_group_shadows_follow_their_shapes() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let sh = json!({"type": "shadow", "offset": {"x": 6, "y": 6}, "blur": 4, "color": "#ff0000"});
        let grad = json!({"type": "gradient", "gradientType": "linear", "colors": [{"color": "#ff0000", "position": 0}, {"color": "#0000ff", "position": 1}]});
        let id = e.insert_root(json!({"type": "frame", "layout": "none", "width": 300, "height": 200, "children": [
            {"type": "text", "name": "t", "content": "Hi", "fontSize": 40, "fill": "#0000ff", "effect": sh},
            {"type": "text", "name": "g", "content": "Grad", "fontSize": 40, "fill": grad, "effect": sh},
            {"type": "text", "name": "g2", "content": "Grad", "fontSize": 40, "fill": grad, "underline": true, "effect": [sh, sh]},
            {"type": "group", "name": "grp", "effect": [sh, {"type": "blur", "radius": 4}], "children": [{"type": "ellipse", "width": 60, "height": 60, "fill": "#00aa00"}]}
        ]})).unwrap();
        let h = super::html_document(&mut e, &[id], &json!({"includeHtmlScaffold": false})).unwrap();
        let style = |name: &str| h.split(&format!("data-name=\"{name}\" style=\"")).nth(1).unwrap().split('"').next().unwrap().to_string();
        let (t, g, g2, grp) = (style("t"), style("g"), style("g2"), style("grp"));
        assert!(t.contains("text-shadow:6px 6px 4px #ff0000;") && t.contains("color:#0000ff;") && !t.contains("box-shadow"), "{t}");
        // Gradient text is clipped to its glyphs, so its shadow is a filter that follows them.
        assert!(g.contains("background:linear-gradient(-0deg,#ff0000 0%,#0000ff 100%);-webkit-background-clip:text;background-clip:text;color:transparent;"), "{g}");
        assert!(g.contains("filter:drop-shadow(6px 6px 2px #ff0000);") && !g.contains("text-shadow") && !g.contains("box-shadow"), "{g}");
        // Several shadows all start from the element's alpha, so they are one SVG filter, not a chain of `drop-shadow()`s.
        assert!(g2.contains("filter:url('data:image/svg+xml,%3Csvg") && g2.contains("%3CfeMergeNode in=%22SourceGraphic%22/%3E") && !g2.contains("drop-shadow"), "{g2}");
        // The filter region grows with the offset and blur (6 + 1.5 * 4, rounded up, plus one) instead of a share of the element.
        assert!(g2.contains("filterUnits=%22userSpaceOnUse%22 x=%22-13%22 y=%22-13%22"), "{g2}");
        // The decoration needs a colour of its own: the text colour is transparent.
        assert!(g2.contains("text-decoration:underline;") && g2.contains("text-decoration-color:#000000;") && !g.contains("text-decoration"), "{g2}");
        assert!(grp.contains("filter:drop-shadow(6px 6px 2px #ff0000) blur(2px);") && !grp.contains("box-shadow"), "{grp}");
    }

    /// Fixed-size children keep their main-axis size in an overflowing auto layout, as on the canvas.
    #[test]
    fn fixed_size_children_do_not_shrink() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let kid = |ty: &str| json!({"type": ty, "width": 60, "height": 20, "fill": "#ff0000"});
        let row = |layout: &str| {
            json!({"type": "frame", "width": 100, "height": 60, "layout": layout, "children": [kid("rectangle"), kid("ellipse"), {"type": "rectangle", "width": "fill_container", "height": 20, "fill": "#0000ff"}]})
        };
        let html = |e: &mut Engine, layout: &str| {
            let id = e.insert_root(row(layout)).unwrap();
            super::html_document(e, &[id], &json!({"includeHtmlScaffold": false})).unwrap()
        };
        let h = html(&mut e, "horizontal");
        assert_eq!(h.matches("width:60px;flex-shrink:0;").count(), 2, "{h}");
        assert!(h.contains("flex:1 1 0;") && !h.contains("height:20px;flex-shrink:0;"), "{h}");
        let v = html(&mut e, "vertical");
        assert_eq!(v.matches("height:20px;flex-shrink:0;").count(), 3, "{v}");
        assert!(!v.contains("width:60px;flex-shrink:0;"), "{v}");
        let u = html(&mut e, "diagonal");
        assert_eq!(u.matches("width:60px;flex-shrink:0;").count(), 2, "{u}");
        // Groups take their size from their children and must not shrink either.
        let id = e.insert_root(json!({"type": "frame", "width": 100, "height": 60, "layout": "horizontal", "children": [{"type": "group", "children": [{"type": "rectangle", "width": 80, "height": 20, "fill": "#ff0000"}]}]})).unwrap();
        let g = super::html_document(&mut e, &[id], &json!({"includeHtmlScaffold": false})).unwrap();
        assert!(g.contains("width:80px;flex-shrink:0;"), "{g}");
    }

    /// `fit_content(N)` on a node with nothing to hug is N, like the canvas; only a layout frame with children hugs.
    #[test]
    fn fit_content_fallback_is_exported_as_pixels() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let id = e.insert_root(json!({"type": "frame", "name": "root", "layout": "none", "width": 300, "height": 200, "children": [
            {"type": "frame", "name": "row", "layout": "horizontal", "width": "fit_content", "height": "fit_content", "gap": 10, "padding": 10, "x": 10, "y": 10, "children": [
                {"type": "frame", "name": "A", "layout": "none", "width": "fit_content(40)", "height": "fit_content(40)", "fill": "#00aa00"},
                {"type": "rectangle", "name": "B", "width": "fit_content(60)", "height": "fit_content(30)", "fill": "#ff0000"}]},
            {"type": "frame", "name": "E", "layout": "vertical", "x": 0, "y": 100, "width": "fit_content(25)", "height": "fit_content", "children": [
                {"type": "rectangle", "width": "fill_container", "height": 10}]}]})).unwrap();
        let h = super::html_document(&mut e, &[id], &json!({"includeHtmlScaffold": false})).unwrap();
        let line = |name: &str| h.lines().find(|l| l.contains(&format!("data-name=\"{name}\""))).unwrap().to_string();
        let (a, b, row, empty) = (line("A"), line("B"), line("row"), line("E"));
        assert!(a.contains("width:40px;") && a.contains("height:40px;") && !a.contains("fit-content"), "{a}");
        assert!(b.contains("width:60px;") && b.contains("height:30px;") && !b.contains("fit-content"), "{b}");
        // The row hugs its children.
        assert!(row.contains("width:fit-content;") && row.contains("height:fit-content;"), "{row}");
        // Its only child fills the width, so nothing hugs there and the fallback is the size; the height still hugs.
        assert!(empty.contains("width:25px;") && empty.contains("height:fit-content;"), "{empty}");
    }

    /// A fit_content parent with a fill child gets the laid-out size: CSS would size it from the fill child's content.
    #[test]
    fn hugging_parent_of_a_fill_child_is_exported_as_pixels() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let id = e.insert_root(json!({"type": "frame", "name": "Card", "layout": "vertical", "padding": 16, "gap": 8, "children": [
            {"type": "rectangle", "name": "fill", "width": "fill_container", "height": 20, "fill": "#00ff00"},
            {"type": "rectangle", "name": "btn", "width": 120, "height": 40, "fill": "#0000ff"}]})).unwrap();
        let h = super::html_document(&mut e, &[id], &json!({"includeHtmlScaffold": false})).unwrap();
        let card = h.lines().find(|l| l.contains("data-name=\"Card\"")).unwrap();
        // The width has a fill child (152 = 120 + 2 * 16); the height has none and stays the browser's to hug.
        assert!(card.contains("width:152px;") && !card.contains("fit-content") && !card.contains("height:"), "{card}");
    }

    /// A rotated or flipped child of an auto layout takes its bounding box in the flow, like the canvas.
    #[test]
    fn tilted_children_reserve_their_bounding_box() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let rect = |extra: serde_json::Value| {
            let mut r = json!({"type": "rectangle", "width": 60, "height": 60, "fill": "#ff0000"});
            r.as_object_mut().unwrap().extend(extra.as_object().unwrap().clone());
            r
        };
        let id = e.insert_root(json!({"type": "frame", "layout": "horizontal", "width": 300, "height": 120, "padding": 10, "gap": 10, "children": [rect(json!({"rotation": 90})), rect(json!({"flipX": true})), rect(json!({}))]})).unwrap();
        let h = super::html_document(&mut e, &[id], &json!({"includeHtmlScaffold": false})).unwrap();
        // 90deg CCW about the origin: the box spans (0..60, -60..0), so it sits 60 down in its wrapper.
        assert!(h.contains("<div style=\"position:relative;flex-shrink:0;width:60px;height:60px;\">\n<div data-name=\"rectangle\" style=\"position:absolute;left:0px;top:60px;width:60px;height:60px;"), "{h}");
        assert!(h.contains("transform:rotate(-90deg);transform-origin:0px 0px;"), "{h}");
        // flipX about the origin spans (-60..0): the origin is the wrapper's right edge.
        assert!(h.contains("left:60px;top:0px;width:60px;height:60px;") && h.contains("transform:scale(-1,1);"), "{h}");
        assert_eq!(h.matches("position:relative;flex-shrink:0;").count(), 2, "only the tilted children are wrapped: {h}");
        // 45deg: the wrapper is the 84.85 wide bounding box.
        let id = e.insert_root(json!({"type": "frame", "layout": "horizontal", "width": 300, "height": 120, "children": [rect(json!({"rotation": 45}))]})).unwrap();
        let h = super::html_document(&mut e, &[id], &json!({"includeHtmlScaffold": false})).unwrap();
        assert!(h.contains("width:84.852814px;height:84.852814px;"), "{h}");
    }

    #[test]
    fn html_opacity_is_applied_once() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        for node in [
            json!({"type": "polygon", "polygonCount": 5, "fill": "#ff0000"}),
            json!({"type": "path", "geometry": "M0 0 L10 0 L10 10 Z", "fill": "#ff0000"}),
            json!({"type": "line", "stroke": "#ff0000", "strokeWidth": 3}),
            json!({"type": "ellipse", "innerRadius": 0.5, "fill": "#ff0000"}),
        ] {
            let ty = node["type"].as_str().unwrap().to_string();
            let mut node = node;
            node.as_object_mut().unwrap().extend(json!({"x": 0, "y": 0, "width": 50, "height": 50, "opacity": 0.5}).as_object().unwrap().clone());
            let id = e.insert_root(node).unwrap();
            let html = super::html_document(&mut e, &[id], &json!({"includeHtmlScaffold": false})).unwrap();
            // `fill-opacity` is paint alpha; only the node's own opacity must not repeat.
            assert_eq!(html.matches("opacity:0.5;").count() + html.matches(" opacity=").count(), 1, "{ty}: {html}");
            assert!(html.contains("opacity:0.5;"), "{ty}: {html}");
        }
    }

    #[test]
    fn exports_slides_deck_and_rasters() {
        let out = std::env::temp_dir().join("nalarupa-test-export");
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("slides");

        let files = super::export(&mut e, &[], "pdf", out.join("deck.pdf").to_str().unwrap(), &json!({})).unwrap();
        let pdf = std::fs::read(&files[0]).unwrap();
        assert!(pdf.starts_with(b"%PDF-"));
        let text = String::from_utf8_lossy(&pdf).replace("/Type /", "/Type/");
        let pages = text.matches("/Type/Page").count() - text.matches("/Type/Pages").count();
        assert_eq!(pages, 3, "one page per top-level frame");

        e.edit(|d| d.update("sld02", &json!({"enabled": false}))).unwrap();
        let files = super::export(&mut e, &[], "pdf", out.join("partial.pdf").to_str().unwrap(), &json!({})).unwrap();
        let text = String::from_utf8_lossy(&std::fs::read(&files[0]).unwrap()).replace("/Type /", "/Type/");
        assert_eq!(text.matches("/Type/Page").count() - text.matches("/Type/Pages").count(), 2, "a hidden slide is skipped");
        e.edit(|d| d.update("sld02", &json!({"enabled": null}))).unwrap();

        let ids = ["sld01".to_string()];
        let jpg = super::export(&mut e, &ids, "jpg", out.to_str().unwrap(), &json!({"scale": 0.25})).unwrap();
        assert!(std::fs::read(&jpg[0]).unwrap().starts_with(&[0xFF, 0xD8, 0xFF]));
        let webp = super::export(&mut e, &ids, "webp", out.to_str().unwrap(), &json!({"scale": 0.25})).unwrap();
        let webp = std::fs::read(&webp[0]).unwrap();
        assert!(webp.starts_with(b"RIFF") && &webp[8..12] == b"WEBP");
        assert!(super::export(&mut e, &ids, "tiff", "", &json!({})).is_err());
    }

    /// A hidden layer (or one inside a hidden parent) says so instead of "node not found".
    #[test]
    fn hidden_layer_export_says_hidden() {
        let out = std::env::temp_dir().join("nalarupa-test-export-hidden");
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("slides");
        e.edit(|d| d.update("sld02", &json!({"enabled": false}))).unwrap();
        let child = e.doc.find("sld02").unwrap()["children"][0]["id"].as_str().unwrap().to_string();
        for id in ["sld02".to_string(), child] {
            for format in ["png", "svg", "pdf", "html-css"] {
                let err = super::export(&mut e, &[id.clone()], format, out.to_str().unwrap(), &json!({})).unwrap_err();
                assert!(err.contains("hidden") && !err.contains("node not found"), "{format} {id}: {err}");
            }
            assert!(e.ctx().node_png(&id, 1.0).unwrap_err().contains("hidden"));
        }
        let err = super::export(&mut e, &["nope".to_string()], "png", out.to_str().unwrap(), &json!({})).unwrap_err();
        assert!(err.contains("node not found: nope"), "{err}");
    }

    /// A component child hidden through an instance override is addressed as `instanceId/childId`.
    #[test]
    fn hidden_instance_child_export_says_hidden() {
        let out = std::env::temp_dir().join("nalarupa-test-export-hidden-inst");
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        let comp = json!({"type":"frame","reusable":true,"width":100,"height":50,"children":[{"id":"kid","type":"rectangle","width":10,"height":10}]});
        let comp = e.edit(|d| d.insert("", comp, None)).unwrap();
        let kid = e.doc.find(&comp).unwrap()["children"][0]["id"].as_str().unwrap().to_string();
        let inst = e.edit(|d| d.insert("", json!({"type":"ref","ref":comp,"descendants":{kid.clone():{"enabled":false}}}), None)).unwrap();
        let id = format!("{inst}/{kid}");
        let err = super::export(&mut e, &[id.clone()], "png", out.to_str().unwrap(), &json!({})).unwrap_err();
        assert!(err.contains("hidden") && !err.contains("node not found"), "{err}");
        let err = super::export(&mut e, &[format!("{inst}/nope")], "png", out.to_str().unwrap(), &json!({})).unwrap_err();
        assert!(err.contains("node not found") && !err.contains("hidden"), "{err}");
    }

    /// A text stroke reaches the SVG and HTML exports (it was dropped before).
    #[test]
    fn text_stroke_is_exported() {
        let out = std::env::temp_dir().join("nalarupa-test-text-stroke");
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let text = |align: &str| json!({"type": "text", "content": "Out", "fontSize": 50, "fill": "#ffff00", "stroke": "#000000", "strokeWidth": 4, "strokeAlignment": align});
        let (center, outer) = (e.insert_root(text("center")).unwrap(), e.insert_root(text("outer")).unwrap());

        let svg = |e: &mut Engine, id: &String| std::fs::read_to_string(&super::export(e, &[id.clone()], "svg", out.to_str().unwrap(), &json!({})).unwrap()[0]).unwrap();
        let svg_center = svg(&mut e, &center);
        assert!(svg_center.contains(r##"stroke="#000000""##) && svg_center.contains(r#"stroke-width="4""#), "{svg_center}");
        assert!(svg(&mut e, &outer).contains(r#"stroke-width="8""#), "an outer stroke is drawn double width and clipped");

        let html = |e: &mut Engine, id: &String| std::fs::read_to_string(&super::export(e, &[id.clone()], "html", out.join("export.html").to_str().unwrap(), &json!({})).unwrap()[0]).unwrap();
        let html_center = html(&mut e, &center);
        assert!(html_center.contains("-webkit-text-stroke:4px #000000;") && !html_center.contains("color:transparent"), "{html_center}");
        assert!(html(&mut e, &outer).contains("-webkit-text-stroke:8px #000000;paint-order:stroke fill;"));
        // Stroke-only text does not take the inherited color as its fill.
        let hollow = e.insert_root(json!({"type": "text", "content": "Out", "fontSize": 50, "stroke": "#000000", "strokeWidth": 4})).unwrap();
        assert!(html(&mut e, &hollow).contains("color:transparent;-webkit-text-stroke:4px #000000;"));
    }

    /// Fixed-width text keeps its newlines and spaces and breaks long words like the canvas; auto-growth text stays on `pre`.
    #[test]
    fn fixed_width_text_wraps_like_the_canvas() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let style_of = |e: &mut Engine, growth: &str| {
            let id = e.insert_root(json!({"type": "text", "content": "a\n\nb   c", "fontSize": 16, "textGrowth": growth, "width": 100})).unwrap();
            let html = super::html_document(e, &[id], &json!({"includeHtmlScaffold": false})).unwrap();
            html.split("style=\"").nth(1).unwrap().split('"').next().unwrap().to_string()
        };
        for growth in ["fixed-width", "fixed-width-height"] {
            let style = style_of(&mut e, growth);
            assert!(style.contains("white-space:pre-wrap;overflow-wrap:anywhere;"), "{growth}: {style}");
        }
        // Without a width the box is 0 wide: one word per line, no split inside a word.
        let id = e.insert_root(json!({"type": "text", "content": "one two", "fontSize": 16, "textGrowth": "fixed-width"})).unwrap();
        let html = super::html_document(&mut e, &[id], &json!({"includeHtmlScaffold": false})).unwrap();
        assert!(html.contains("width:0px;") && html.contains("white-space:pre-wrap;") && !html.contains("overflow-wrap"), "{html}");
        let auto = style_of(&mut e, "auto");
        assert!(auto.contains("white-space:pre;") && !auto.contains("overflow-wrap"), "{auto}");
    }
}
