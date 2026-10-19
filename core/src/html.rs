//! Export: PNG / JPEG / WebP / SVG files, multi-page PDF decks and HTML+CSS code generation (flexbox maps 1:1).

use crate::engine::Engine;
use crate::render::find_rnode;
use crate::resolve::{num, RNode};
use crate::svg::{color, esc, fills};
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
                    e.ctx().node_svg(id).ok_or_else(|| format!("node not found: {id}"))?.0.into_bytes()
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
        let top = crate::render::top_index(&e.doc, id).ok_or_else(|| format!("node not found: {id}"))?;
        let root = e.ctx().laid_out(top).ok_or_else(|| format!("node not found: {id}"))?;
        let n = find_rnode(&root, id).ok_or_else(|| format!("node not found: {id}"))?;
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
    let (c, a) = color(v)?;
    if a >= 0.999 {
        return Some(c);
    }
    let h = c.strip_prefix('#')?;
    let p = |i: usize| u8::from_str_radix(&h[i..i + 2], 16).unwrap_or(0);
    Some(format!("rgba({}, {}, {}, {:.3})", p(0), p(2), p(4), a))
}

fn size_css(v: Option<&Value>, prop: &str, parent_dir: Option<&str>, horiz_axis: bool, measured: f64) -> String {
    let main_axis = match parent_dir {
        Some("horizontal") => horiz_axis,
        Some("vertical") => !horiz_axis,
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
        Some(v) => num(v).map(|n| format!("{prop}:{n}px;")).unwrap_or_default(),
        None => {
            let _ = measured;
            String::new()
        }
    }
}

impl Html<'_> {
    fn node(&mut self, n: &RNode, parent: Option<&RNode>) {
        let parent_dir = parent.map(|p| if p.ty == "frame" { p.s("layout").unwrap_or("horizontal") } else { "none" });
        let absolute = parent_dir == Some("none") || n.s("layoutPosition") == Some("absolute");
        let mut css = String::new();
        if parent.is_none() {
            css += "position:relative;";
        } else if absolute {
            let p = parent.unwrap();
            let _ = write!(css, "position:absolute;left:{}px;top:{}px;", n.x - p.x, n.y - p.y);
        }
        let text_auto = n.ty == "text" && n.s("textGrowth").unwrap_or("auto") == "auto";
        if !text_auto {
            css += &size_css(n.p.get("width"), "width", parent_dir, true, n.w);
            css += &size_css(n.p.get("height"), "height", parent_dir, false, n.h);
            if matches!(n.ty.as_str(), "rectangle" | "ellipse" | "icon" | "polygon" | "line" | "path") {
                if n.p.get("width").is_none() {
                    let _ = write!(css, "width:{}px;", n.w);
                }
                if n.p.get("height").is_none() {
                    let _ = write!(css, "height:{}px;", n.h);
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
            let _ = write!(css, "transform:{};transform-origin:0 0;", tf.trim_start());
        }
        // Shapes CSS cannot draw are inlined as SVG, which carries their own paint.
        let as_svg = matches!(n.ty.as_str(), "path" | "polygon" | "line") || n.ty == "ellipse" && (n.f("innerRadius").unwrap_or(0.0) > 0.0 || n.f("sweepAngle").is_some_and(|s| s.abs() < 360.0));
        if !as_svg {
            self.paint_css(n, &mut css);
        }
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
                // The div's CSS transform already rotates/flips it.
                let mut p = n.p.clone();
                for k in ["rotation", "flipX", "flipY"] {
                    p.remove(k);
                }
                let mut shape = RNode::leaf(n.id.clone(), &n.ty, p, None);
                (shape.x, shape.y, shape.w, shape.h) = (n.x, n.y, n.w, n.h);
                let mut svg = crate::svg::Svg::new(self.fonts, self.assets, self.base.clone());
                svg.node(&shape);
                let _ = writeln!(self.out, "<div{attrs} style=\"{css}\">{}</div>", svg.finish(Some((n.x, n.y, n.w.max(1.0), n.h.max(1.0)))));
            }
            _ => {
                if n.ty == "frame" {
                    match n.s("layout").unwrap_or("horizontal") {
                        "none" => css += "position:relative;",
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
                    let _ = write!(css, "position:relative;width:{}px;height:{}px;", n.w, n.h);
                } else if n.ty == "ellipse" {
                    css += "border-radius:50%;";
                }
                let _ = writeln!(self.out, "<div{attrs} style=\"{css}\">");
                for k in &n.kids {
                    self.node(k, Some(n));
                }
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

    fn paint_css(&mut self, n: &RNode, css: &mut String) {
        let mut shadows: Vec<String> = Vec::new();
        if n.ty != "text" && n.ty != "icon" {
            let mut layers = Vec::new();
            for f in fills(n.p.get("fill")).into_iter().rev() {
                match f {
                    Value::String(s) => layers.extend(css_color(s).map(|c| format!("linear-gradient({c},{c})"))),
                    Value::Object(o) => match o.get("type").and_then(Value::as_str).unwrap_or("color") {
                        "color" => layers.extend(o.get("color").and_then(Value::as_str).and_then(css_color).map(|c| format!("linear-gradient({c},{c})"))),
                        "gradient" => {
                            let stops: Vec<String> = o.get("colors").and_then(Value::as_array).into_iter().flatten().filter_map(|c| {
                                Some(format!("{} {}%", css_color(c["color"].as_str()?)?, num(&c["position"]).unwrap_or(0.0) * 100.0))
                            }).collect();
                            let rot = o.get("rotation").and_then(num).unwrap_or(0.0);
                            if o.get("gradientType").and_then(Value::as_str) == Some("radial") {
                                layers.push(format!("radial-gradient({})", stops.join(",")));
                            } else {
                                layers.push(format!("linear-gradient({}deg,{})", -rot, stops.join(",")));
                            }
                        }
                        "image" => {
                            let url = o.get("url").and_then(Value::as_str).unwrap_or("");
                            if !url.is_empty() {
                                let size = match o.get("mode").and_then(Value::as_str) {
                                    Some("fit") => "contain",
                                    Some("fill") => "cover",
                                    _ => "100% 100%",
                                };
                                layers.push(format!("url('{}') center/{size} no-repeat", esc(url)));
                            }
                        }
                        _ => {}
                    },
                    _ => {}
                }
            }
            if !layers.is_empty() {
                layers.reverse();
                let _ = write!(css, "background:{};", layers.join(","));
            }
            let r = crate::svg::radii(n);
            if r.iter().any(|v| *v > 0.0) {
                let _ = write!(css, "border-radius:{}px {}px {}px {}px;", r[0], r[1], r[2], r[3]);
            }
            // No stroke without a width; alignment defaults to center.
            let stroke = fills(n.p.get("stroke")).first().and_then(|f| f.as_str().map(str::to_string).or_else(|| f["color"].as_str().map(str::to_string))).and_then(|c| css_color(&c));
            match (stroke, n.p.get("strokeWidth")) {
                (Some(c), Some(Value::Object(s))) => {
                    for side in ["top", "right", "bottom", "left"] {
                        if let Some(w) = s.get(side).and_then(num).filter(|w| *w > 0.0) {
                            let _ = write!(css, "border-{side}:{w}px solid {c};");
                        }
                    }
                }
                (Some(c), Some(v)) => {
                    if let Some(w) = num(v).filter(|w| *w > 0.0) {
                        match n.s("strokeAlignment") {
                            Some("inner" | "inside") => shadows.push(format!("inset 0 0 0 {w}px {c}")),
                            Some("outer" | "outside") => shadows.push(format!("0 0 0 {w}px {c}")),
                            _ => shadows.extend([format!("inset 0 0 0 {}px {c}", w / 2.0), format!("0 0 0 {}px {c}", w / 2.0)]),
                        }
                    }
                }
                _ => {}
            }
        }
        let effects = match n.p.get("effect") {
            Some(Value::Array(a)) => a.iter().collect::<Vec<_>>(),
            Some(v @ Value::Object(_)) => vec![v],
            _ => vec![],
        };
        shadows.extend(effects.into_iter().filter(|e| e["type"] == "shadow" && e["enabled"] != false).filter_map(|e| {
            // Defaults: opaque black, no blur; an invalid color is transparent.
            let c = css_color(e["color"].as_str().unwrap_or("#000000"))?;
            Some(format!("{}{}px {}px {}px {c}", if e["shadowType"] == "inner" { "inset " } else { "" }, num(&e["offset"]["x"]).unwrap_or(0.0), num(&e["offset"]["y"]).unwrap_or(0.0), num(&e["blur"]).unwrap_or(0.0)))
        }));
        if !shadows.is_empty() {
            let _ = write!(css, "box-shadow:{};", shadows.join(","));
        }
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
        if n.s("textGrowth").unwrap_or("auto") == "auto" {
            css.push_str("white-space:pre;");
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
        if let Some(c) = fills(n.p.get("fill")).first().and_then(|f| f.as_str().map(str::to_string).or_else(|| f["color"].as_str().map(str::to_string))).and_then(|c| css_color(&c)) {
            let _ = write!(css, "color:{c};");
        }
    }
}

pub fn file_name(p: &Path) -> String {
    p.file_name().map(|s| s.to_string_lossy().to_string()).unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use crate::engine::Engine;
    use serde_json::json;

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
}
