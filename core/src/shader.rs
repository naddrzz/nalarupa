//! Shader fills (`{type: "shader", url, uniforms}`): the file is a WebGL fragment shader (GLSL ES
//! 1.00, or 3.00 with one `out vec4`), uniforms carry `@directives` in the `/** … */` comment right
//! above them. It is rewritten to Vulkan GLSL 450, compiled by naga, drawn on an offscreen wgpu
//! device (created on first use) and read back as an image that svg.rs paints like an image fill.
//!
//! What a shader sees:
//! - `gl_FragCoord.xy`: node-local units, origin bottom-left, pixel centres at .5;
//! - `@resolution` = node size, `@time` = 0 and `@mouse` = node centre (not animated; exports
//!   always use these values);
//! - `@sdf`: signed distance to the outline (node units, positive inside) + its gradient, clamped;
//!   `@backdrop`: transparent 1×1 when no backdrop is available;
//!   plain `sampler2D`: the image at the uniform's url, repeated; missing = transparent 1×1;
//! - the colour written is premultiplied (SkSL convention).

use resvg::tiny_skia;
use serde_json::{json, Map, Value};
use std::borrow::Cow;
use std::collections::HashMap;
use std::sync::{Arc, Mutex};

/// The filled shape, for the `@sdf` texture (node-local coordinates).
#[derive(Debug)]
pub enum Outline {
    /// Rounded rectangle, radii [tl, tr, br, bl].
    RRect([f64; 4]),
    Ellipse,
    /// SVG path data (absolute) with the node origin, and whether it fills even-odd.
    Path { d: String, x: f64, y: f64, evenodd: bool },
}

/// A cached shader image is capped at 4096 px per side, the SDF at 1024, the backdrop at 2048.
const MAX_SIDE: f64 = 4096.0;
const SDF_MAX: f64 = 1024.0;
const BACKDROP_MAX: f64 = 2048.0;

/// Paints what lies behind the node into a `w`×`h` pixmap (premultiplied, top row first).
pub type Backdrop<'a> = dyn FnMut(u32, u32) -> Option<tiny_skia::Pixmap> + 'a;
/// Rows per GPU submission: heavy shaders stay far from the driver's 2 s watchdog.
const TILE: u32 = 256;
/// Lines put in front of the user's code by `transpile`.
const HEADER_LINES: usize = 2;

#[derive(Clone, Debug, PartialEq)]
enum Kind {
    Float(f64),
    Int(f64),
    Bool(bool),
    Vec(Vec<f64>, bool),
    /// Default RGBA and whether the uniform is a vec4.
    Color([f64; 4], bool),
    Resolution(bool),
    Time,
    Mouse(bool),
    Image,
    Sdf,
    Backdrop,
    /// Matrices and other types, left at zero.
    Zero,
}

#[derive(Debug)]
struct Uniform {
    name: String,
    /// Name inside the GLSL 450 uniform block (or the texture/sampler pair's base name).
    member: String,
    ty: String,
    kind: Kind,
}

#[derive(Debug)]
struct Program {
    module: naga::Module,
    info: naga::valid::ModuleInfo,
    uniforms: Vec<Uniform>,
    /// Block member → (byte offset, integer components).
    layout: HashMap<String, (u32, bool)>,
    size: u32,
}

#[derive(Default)]
struct Notes {
    color: bool,
    resolution: bool,
    time: bool,
    mouse: bool,
    sdf: bool,
    backdrop: bool,
    range: bool,
    default: Option<Value>,
}

/// Directives of the `/** … */` comment that ends right before byte `at` (only whitespace between).
fn notes(src: &str, at: usize) -> Result<Notes, String> {
    let mut n = Notes::default();
    let Some(body) = src[..at].trim_end().strip_suffix("*/") else { return Ok(n) };
    let Some(text) = body.rfind("/*").and_then(|i| body[i + 2..].strip_prefix('*')) else { return Ok(n) };
    for line in text.lines() {
        let mut rest = line.trim_start().strip_prefix('*').unwrap_or(line.trim_start()).trim();
        while let Some(r) = rest.strip_prefix('@') {
            let end = r.find(|c: char| !(c.is_ascii_alphanumeric() || c == '_')).unwrap_or(r.len());
            let (name, r) = (&r[..end], r[end..].trim_start());
            let (args, r) = if let Some(inner) = r.strip_prefix('(') {
                let close = inner.find(')').ok_or_else(|| format!("Unterminated argument list in annotation: '{r}'"))?;
                (&inner[..close], inner[close + 1..].trim_start())
            } else {
                let next = r.find('@').unwrap_or(r.len());
                (r[..next].trim(), &r[next..])
            };
            rest = r;
            let flag = |f: &mut bool| if args.is_empty() { *f = true; Ok(()) } else { Err(format!("@{name} takes no arguments")) };
            match name {
                "color" => flag(&mut n.color)?,
                "resolution" => flag(&mut n.resolution)?,
                "time" => flag(&mut n.time)?,
                "mouse" => flag(&mut n.mouse)?,
                "sdf" => flag(&mut n.sdf)?,
                "backdrop" => flag(&mut n.backdrop)?,
                "min" | "max" | "range" => {
                    let parts: Vec<&str> = args.split(',').map(str::trim).filter(|s| !s.is_empty()).collect();
                    if parts.len() != if name == "range" { 2 } else { 1 } || parts.iter().any(|p| p.parse::<f64>().is_err()) {
                        return Err(format!("@{name} expects numeric arguments, got '{args}'"));
                    }
                    n.range = true;
                }
                "label" if args.is_empty() => return Err("@label requires a value".into()),
                "label" => {}
                "default" => n.default = Some(default_value(args)?),
                _ => return Err(format!("Unknown annotation directive: @{name}")),
            }
        }
    }
    Ok(n)
}

fn default_value(a: &str) -> Result<Value, String> {
    match a.trim() {
        "" => Err("@default requires a value".into()),
        "true" => Ok(json!(true)),
        "false" => Ok(json!(false)),
        h if h.starts_with('#') => Ok(json!(h)),
        list => {
            let nums = list.split(',').map(str::trim).filter(|s| !s.is_empty()).map(|s| s.parse::<f64>().map_err(|_| format!("@default got non-numeric value: '{s}'"))).collect::<Result<Vec<_>, _>>()?;
            Ok(if nums.len() == 1 { json!(nums[0]) } else { json!(nums) })
        }
    }
}

/// Uniform kind from its GLSL type and directives.
fn kind(name: &str, ty: &str, n: &Notes) -> Result<Kind, String> {
    let err = |m: &str| Err(format!("Uniform '{name}': {m}"));
    let int = ty.starts_with('i');
    let dim = ty.chars().last().and_then(|c| c.to_digit(10)).unwrap_or(1) as usize;
    if n.color && !matches!(ty, "vec3" | "vec4") {
        return err("@color requires vec3 or vec4");
    }
    let special = n.resolution || n.time || n.mouse || n.sdf || n.backdrop;
    if (special || n.color) && n.range {
        return err(&format!("@range/@min/@max is not valid for '{ty}'"));
    }
    if (n.time || n.mouse || n.sdf || n.backdrop) && n.default.is_some() {
        return err("this uniform does not support @default");
    }
    let nums = |d: &Value| d.as_array().map(|a| a.iter().filter_map(Value::as_f64).collect::<Vec<_>>()).unwrap_or_else(|| d.as_f64().into_iter().collect());
    Ok(if n.color {
        let alpha = ty == "vec4";
        let c = match &n.default {
            None => [1.0; 4],
            Some(Value::String(h)) => crate::svg::rgba(h).ok_or_else(|| format!("Uniform '{name}': @default invalid hex literal: '{h}'"))?,
            Some(d) => {
                let v = nums(d);
                if v.len() != if alpha { 4 } else { 3 } {
                    return err("@color @default needs one number per component");
                }
                [v[0], v[1], v[2], v.get(3).copied().unwrap_or(1.0)].map(|x| (x.clamp(0.0, 1.0) * 255.0).round() / 255.0)
            }
        };
        Kind::Color(c, alpha)
    } else if n.resolution {
        if !matches!(ty, "vec2" | "ivec2") {
            return err("@resolution requires vec2 or ivec2");
        }
        Kind::Resolution(int)
    } else if n.time {
        if ty != "float" {
            return err("@time requires float");
        }
        Kind::Time
    } else if n.mouse {
        if !matches!(ty, "vec2" | "ivec2") {
            return err("@mouse requires vec2 or ivec2");
        }
        Kind::Mouse(int)
    } else if n.sdf || n.backdrop {
        if ty != "sampler2D" {
            return err("@sdf/@backdrop requires sampler2D");
        }
        if n.sdf { Kind::Sdf } else { Kind::Backdrop }
    } else {
        match ty {
            "float" | "int" => {
                let d = match &n.default {
                    None => 0.0,
                    Some(d) => d.as_f64().ok_or_else(|| format!("Uniform '{name}': @default must be a single number"))?,
                };
                if ty == "int" { Kind::Int(d.trunc()) } else { Kind::Float(d) }
            }
            "bool" => Kind::Bool(match &n.default {
                None => false,
                Some(Value::Bool(b)) => *b,
                _ => return err("is bool, @default must be true or false"),
            }),
            "vec2" | "vec3" | "vec4" | "ivec2" | "ivec3" | "ivec4" => {
                let d = match &n.default {
                    None => vec![0.0; dim],
                    Some(d) => nums(d),
                };
                if d.len() != dim {
                    return err(&format!("@default needs {dim} numbers"));
                }
                Kind::Vec(d, int)
            }
            "sampler2D" if n.default.is_some() => return err("is sampler2D, @default is not supported"),
            "sampler2D" => Kind::Image,
            _ => Kind::Zero,
        }
    })
}

/// Same-length copy with comments blanked (newlines kept), so byte offsets and lines still match.
fn blank_comments(src: &str) -> String {
    let mut b = src.as_bytes().to_vec();
    let mut i = 0;
    while i + 1 < b.len() {
        let end = match (b[i], b[i + 1]) {
            (b'/', b'/') => b[i..].iter().position(|&c| c == b'\n').map_or(b.len(), |p| i + p),
            (b'/', b'*') => b[i + 2..].windows(2).position(|w| w == b"*/").map_or(b.len(), |p| i + p + 4),
            _ => {
                i += 1;
                continue;
            }
        };
        for c in &mut b[i..end] {
            if *c != b'\n' {
                *c = b' ';
            }
        }
        i = end;
    }
    String::from_utf8(b).unwrap_or_default()
}

fn is_ident(c: u8) -> bool {
    c.is_ascii_alphanumeric() || c == b'_'
}

/// Identifiers of `s` with their byte ranges (numbers are skipped).
fn idents(s: &str) -> Vec<(usize, usize)> {
    let b = s.as_bytes();
    let (mut out, mut i) = (Vec::new(), 0);
    while i < b.len() {
        let c = b[i];
        if c.is_ascii_digit() || (c == b'.' && b.get(i + 1).is_some_and(u8::is_ascii_digit)) {
            i += 1;
            while i < b.len() && (is_ident(b[i]) || b[i] == b'.') {
                i += 1;
            }
        } else if c.is_ascii_alphabetic() || c == b'_' {
            let start = i;
            while i < b.len() && is_ident(b[i]) {
                i += 1;
            }
            out.push((start, i));
        } else {
            i += 1;
        }
    }
    out
}

/// GLSL 450 keywords and built-ins that WebGL code may use as plain names.
const RESERVED: &[&str] = &[
    "texture", "sample", "input", "output", "filter", "common", "partition", "active", "buffer", "shared", "patch",
    "subroutine", "precise", "resource", "coherent", "volatile", "restrict", "readonly", "writeonly", "noperspective",
    "superp", "atomic_uint", "image2D", "smooth", "flat", "centroid",
];

/// Rewrite a WebGL fragment shader as Vulkan GLSL 450 and collect its uniforms.
fn transpile(src: &str) -> Result<(String, Vec<Uniform>), String> {
    let mut clean = blank_comments(src).into_bytes();
    let text = String::from_utf8_lossy(&clean).to_string();
    let es3 = text.lines().find_map(|l| l.trim().strip_prefix("#version")).and_then(|v| v.split_whitespace().next()?.parse::<u32>().ok()).is_some_and(|v| v >= 300);
    let mut uniforms = Vec::new();
    let mut out_var = None;
    let mut blank = Vec::new();
    // Top-level statements (brace depth 0) and preprocessor lines.
    let b = text.as_bytes();
    let (mut i, mut depth, mut start, mut line_start) = (0usize, 0i32, 0usize, true);
    while i < b.len() {
        let c = b[i];
        if line_start && (c == b' ' || c == b'\t' || c == b'\r') {
            i += 1;
            continue;
        }
        if line_start && c == b'#' {
            let mut end = i;
            while end < b.len() && b[end] != b'\n' {
                end += if b[end] == b'\\' { 2 } else { 1 };
            }
            let end = end.min(b.len());
            let directive = text[i..end].trim_start_matches('#').trim_start();
            if directive.starts_with("version") || directive.starts_with("extension") {
                blank.push((i, end));
            }
            if depth == 0 {
                start = end;
            }
            i = end;
            continue;
        }
        line_start = c == b'\n';
        match c {
            b'{' => depth += 1,
            b'}' => {
                depth -= 1;
                if depth == 0 {
                    start = i + 1;
                }
            }
            b';' if depth == 0 => {
                statement(src, &text, start, i, &mut uniforms, &mut out_var, &mut blank)?;
                start = i + 1;
            }
            _ => {}
        }
        i += 1;
    }
    for (a, e) in blank {
        for c in &mut clean[a..e] {
            if *c != b'\n' {
                *c = b' ';
            }
        }
    }
    let body = String::from_utf8(clean).map_err(|e| e.to_string())?;

    let mut map: HashMap<&str, String> = HashMap::new();
    for r in RESERVED {
        if !(es3 && *r == "texture") {
            map.insert(r, format!("nr_{r}"));
        }
    }
    let member = |name: &str| if RESERVED.contains(&name) { format!("nr_{name}") } else { name.to_string() };
    for u in &mut uniforms {
        u.member = member(&u.name);
    }
    if !es3 {
        map.insert("texture2D", "texture".into());
        map.insert("texture2DLod", "textureLod".into());
        map.insert("texture2DLodEXT", "textureLod".into());
        map.insert("texture2DProj", "textureProj".into());
        map.insert("gl_FragColor", "nr_FragColor".into());
    }
    if let Some(o) = &out_var {
        map.insert(o.as_str(), "nr_FragColor".into());
    }
    map.insert("gl_FragCoord", "nr_FragCoord".into());
    map.insert("main", "nr_main".into());
    for q in ["highp", "mediump", "lowp"] {
        map.insert(q, String::new());
    }
    let mut block = String::new();
    let mut samplers = String::new();
    let mut binding = 1;
    for u in &uniforms {
        let m = &u.member;
        match u.ty.as_str() {
            "sampler2D" => {
                samplers += &format!("layout(set = 0, binding = {binding}) uniform texture2D {m}_nrt; layout(set = 0, binding = {}) uniform sampler {m}_nrs; ", binding + 1);
                binding += 2;
                map.insert(&u.name, format!("sampler2D({m}_nrt, {m}_nrs)"));
            }
            "bool" => {
                block += &format!("int {m}_nrb; ");
                map.insert(&u.name, format!("bool({m}_nrb)"));
            }
            t if t.starts_with("bvec") => {
                block += &format!("i{} {m}_nrb; ", &t[1..]);
                map.insert(&u.name, format!("{t}({m}_nrb)"));
            }
            t => {
                block += &format!("{t} {m}; ");
            }
        }
    }
    let toks = idents(&body);
    let mut code = String::with_capacity(body.len() + 512);
    let (mut last, mut has_main, mut writes_color) = (0, false, out_var.is_some());
    for (a, e) in toks {
        let word = &body[a..e];
        if word == "main" && body[e..].trim_start().starts_with('(') {
            has_main = true;
        }
        writes_color |= !es3 && word == "gl_FragColor";
        // A uniform's name after `.` is a swizzle or a member (`c.b`), not the uniform.
        let after_dot = body[..a].trim_end().ends_with('.');
        if let Some(rep) = map.get(word).filter(|_| !after_dot || RESERVED.contains(&word) || !uniforms.iter().any(|u| u.name == word)) {
            code += &body[last..a];
            code += rep;
            last = e;
        }
    }
    code += &body[last..];
    if !has_main {
        return Err("main() is missing!".into());
    }
    if !writes_color {
        return Err("No color output found. Expected `out vec4 <name>` or `gl_FragColor`.".into());
    }
    let glsl = format!(
        "#version 450\nlayout(std140, set = 0, binding = 0) uniform NrUniforms {{ {block}vec4 nr_unit; }}; {samplers}layout(location = 0) out vec4 nr_FragColor; vec4 nr_FragCoord;\n{code}\nvoid main() {{ nr_FragCoord = vec4(gl_FragCoord.x * nr_unit.x, nr_unit.z - gl_FragCoord.y * nr_unit.y, 0.0, 1.0); nr_FragColor = vec4(0.0); nr_main(); }}\n"
    );
    Ok((glsl, uniforms))
}

/// One top-level statement `text[start..=end]`: uniform, precision and in/out declarations.
fn statement(src: &str, text: &str, start: usize, end: usize, uniforms: &mut Vec<Uniform>, out_var: &mut Option<String>, blank: &mut Vec<(usize, usize)>) -> Result<(), String> {
    let stmt = &text[start..end];
    let toks: Vec<&str> = idents(stmt).into_iter().map(|(a, e)| &stmt[a..e]).collect();
    let words: Vec<&str> = toks.iter().copied().filter(|t| !matches!(*t, "highp" | "mediump" | "lowp")).collect();
    let Some(&first) = words.first() else { return Ok(()) };
    let quals: Vec<&str> = if first == "layout" { words.iter().skip_while(|w| **w != "out" && **w != "in").copied().collect() } else { words.clone() };
    match quals.first().copied().unwrap_or(first) {
        "precision" => blank.push((start, end + 1)),
        "in" | "varying" | "attribute" => return Err("The shader must not use anything but gl_FragCoord and uniforms!".into()),
        "out" => {
            if out_var.is_some() || stmt.contains(',') || stmt.contains('[') || quals.get(1) != Some(&"vec4") || quals.len() != 3 {
                return Err("Must use a single `out vec4` variable!".into());
            }
            *out_var = Some(quals[2].to_string());
            blank.push((start, end + 1));
        }
        "uniform" => {
            let ty = *words.get(1).ok_or("uniform without a type")?;
            if stmt.contains('[') {
                return Err(format!("Unsupported uniform type: uniform arrays ({})", stmt.trim()));
            }
            if !matches!(ty, "float" | "int" | "bool" | "vec2" | "vec3" | "vec4" | "ivec2" | "ivec3" | "ivec4" | "bvec2" | "bvec3" | "bvec4" | "mat2" | "mat3" | "mat4" | "sampler2D") {
                return Err(format!("Unsupported uniform type: {ty}"));
            }
            let at = start + stmt.find("uniform").unwrap_or(0);
            let n = notes(src, at)?;
            let k = kind(words[2..].first().copied().unwrap_or(""), ty, &n)?;
            for name in &words[2..] {
                uniforms.push(Uniform { name: name.to_string(), member: name.to_string(), ty: ty.to_string(), kind: k.clone() });
            }
            blank.push((start, end + 1));
        }
        _ => {}
    }
    Ok(())
}

/// Parse, transpile and validate a shader source.
fn compile(src: &str) -> Result<Program, String> {
    let (glsl, mut uniforms) = transpile(src)?;
    let module = naga::front::glsl::Frontend::default()
        .parse(&naga::front::glsl::Options::from(naga::ShaderStage::Fragment), &glsl)
        .map_err(|e| {
            e.errors.iter().map(|err| {
                let line = err.meta.location(&glsl).line_number as usize;
                format!("line {}: {}", line.saturating_sub(HEADER_LINES), err.kind)
            }).collect::<Vec<_>>().join("\n")
        })?;
    let info = naga::valid::Validator::new(naga::valid::ValidationFlags::all(), naga::valid::Capabilities::empty())
        .validate(&module)
        .map_err(|e| format!("shader validation: {}", e.as_inner()))?;
    let mut layout = HashMap::new();
    let mut size = 16;
    for (_, g) in module.global_variables.iter() {
        if g.space != naga::AddressSpace::Uniform {
            continue;
        }
        if let naga::TypeInner::Struct { members, span } = &module.types[g.ty].inner {
            size = *span;
            for m in members {
                let int = match &module.types[m.ty].inner {
                    naga::TypeInner::Scalar(s) | naga::TypeInner::Vector { scalar: s, .. } => s.kind != naga::ScalarKind::Float,
                    _ => false,
                };
                layout.insert(m.name.clone().unwrap_or_default(), (m.offset, int));
            }
        }
    }
    for u in &mut uniforms {
        if layout.contains_key(&format!("{}_nrb", u.member)) {
            u.member += "_nrb";
        }
    }
    Ok(Program { module, info, uniforms, layout, size })
}

/// The stored value when it has the right shape, else the default.
fn components(k: &Kind, v: Option<&Value>, w: f64, h: f64) -> Vec<f64> {
    match k {
        Kind::Float(d) => vec![v.and_then(Value::as_f64).unwrap_or(*d)],
        Kind::Int(d) => vec![v.and_then(Value::as_f64).unwrap_or(*d).trunc()],
        Kind::Bool(d) => vec![match v {
            Some(Value::Bool(b)) => *b as u8 as f64,
            Some(Value::Number(n)) => (n.as_f64() != Some(0.0)) as u8 as f64,
            _ => *d as u8 as f64,
        }],
        Kind::Vec(d, _) => match v.and_then(Value::as_array) {
            // Components the array does not give stay 0.
            Some(a) => (0..d.len()).map(|i| a.get(i).map_or(0.0, |x| x.as_f64().unwrap_or(d[i]))).collect(),
            None => d.clone(),
        },
        Kind::Color(d, alpha) => {
            let c = v.and_then(Value::as_str).map(|s| crate::svg::rgba(s).unwrap_or([0.0; 4])).unwrap_or(*d);
            c[..if *alpha { 4 } else { 3 }].to_vec()
        }
        Kind::Resolution(int) => if *int { vec![w.trunc(), h.trunc()] } else { vec![w, h] },
        Kind::Time => vec![0.0],
        Kind::Mouse(int) => if *int { vec![(w / 2.0).trunc(), (h / 2.0).trunc()] } else { vec![w / 2.0, h / 2.0] },
        Kind::Image | Kind::Sdf | Kind::Backdrop | Kind::Zero => Vec::new(),
    }
}

/// A texture handed to the shader: RGBA8 (premultiplied, top row first) or RGBA16F.
enum Tex {
    Rgba8(u32, u32, Vec<u8>),
    Half(u32, u32, Vec<u16>),
}

fn f16(v: f32) -> u16 {
    let x = v.to_bits();
    let sign = ((x >> 16) & 0x8000) as u16;
    let exp = ((x >> 23) & 0xff) as i32 - 127 + 15;
    let man = x & 0x7f_ffff;
    if v.is_nan() {
        return sign | 0x7e00;
    }
    if exp >= 31 {
        return sign | 0x7c00;
    }
    if exp <= 0 {
        if exp < -10 {
            return sign;
        }
        let m = (man | 0x80_0000) >> (1 - exp);
        return sign | ((m + 0x1000) >> 13) as u16;
    }
    let h = ((exp as u32) << 10) | (man >> 13);
    // Round to nearest; a carry into the exponent is still the right value.
    sign | (h + ((man >> 12) & 1)) as u16
}

/// `@sdf` texture: R = signed distance to the outline in node units (positive inside),
/// G/B = its gradient per texel with y up, A = 1; rows bottom-up (GL orientation).
fn sdf(outline: &Outline, w: f64, h: f64, dpi: f64) -> Tex {
    let r = (w.max(h) * dpi).max(1e-9);
    let s = dpi * r.min(SDF_MAX) / r;
    let (sw, sh) = ((w * s).ceil().max(1.0) as usize, (h * s).ceil().max(1.0) as usize);
    let (fx, fy) = (w / sw as f64, h / sh as f64);
    let mut px = vec![[0f32; 4]; sw * sh];
    let sign = |v: f64| if v > 0.0 { 1.0 } else if v < 0.0 { -1.0 } else { 0.0 };
    match outline {
        Outline::RRect(rad) => {
            let (hw, hh) = (w / 2.0, h / 2.0);
            for row in 0..sh {
                let y = (row as f64 + 0.5) * fy - hh;
                for col in 0..sw {
                    let x = (col as f64 + 0.5) * fx - hw;
                    let r = if x < 0.0 { if y < 0.0 { rad[0] } else { rad[3] } } else if y < 0.0 { rad[1] } else { rad[2] };
                    let (qx, qy) = (x.abs() - hw + r, y.abs() - hh + r);
                    let (d, ux, uy) = if qx > 0.0 && qy > 0.0 {
                        let l = qx.hypot(qy);
                        (r - l, qx / l, qy / l)
                    } else if qx > qy {
                        (r - qx, 1.0, 0.0)
                    } else {
                        (r - qy, 0.0, 1.0)
                    };
                    px[row * sw + col] = [d as f32, (-sign(x) * ux * fx) as f32, (sign(y) * uy * fy) as f32, 1.0];
                }
            }
        }
        Outline::Ellipse => {
            let (a, b) = (w / 2.0, h / 2.0);
            for row in 0..sh {
                let y = (row as f64 + 0.5) * fy - b;
                for col in 0..sw {
                    let x = (col as f64 + 0.5) * fx - a;
                    let (d, nx, ny) = ellipse_distance(a, b, x, y);
                    // The inward normal at the closest point, y flipped to point up.
                    px[row * sw + col] = [d as f32, (nx * fx) as f32, (-ny * fy) as f32, 1.0];
                }
            }
        }
        Outline::Path { d, x, y, evenodd } => path_sdf(d, *x, *y, *evenodd, sw, sh, w, h, s, &mut px),
    }
    let mut out = Vec::with_capacity(sw * sh * 4);
    for row in (0..sh).rev() {
        for p in &px[row * sw..(row + 1) * sw] {
            out.extend(p.map(f16));
        }
    }
    Tex::Half(sw as u32, sh as u32, out)
}

/// Signed distance from (x, y) to the ellipse with semi-axes a, b (positive inside) and the unit
/// inward normal at the closest point (y down).
fn ellipse_distance(a: f64, b: f64, x: f64, y: f64) -> (f64, f64, f64) {
    let (px, py) = (x.abs(), y.abs());
    let inside = (px / a).powi(2) + (py / b).powi(2) <= 1.0;
    if (a - b).abs() < 1e-9 {
        let l = px.hypot(py);
        let (nx, ny) = if l > 0.0 { (-x / l, -y / l) } else { (0.0, 0.0) };
        return (a - l, nx, ny);
    }
    // Closest point (a cos t, b sin t): Newton on the perpendicularity condition, first quadrant.
    let mut t = (a * py).atan2(b * px).clamp(0.0, std::f64::consts::FRAC_PI_2);
    for _ in 0..8 {
        let (c, s) = (t.cos(), t.sin());
        let f = (a * a - b * b) * s * c - px * a * s + py * b * c;
        let df = (a * a - b * b) * (c * c - s * s) - px * a * c - py * b * s;
        if df.abs() < 1e-12 {
            break;
        }
        t = (t - f / df).clamp(0.0, std::f64::consts::FRAC_PI_2);
    }
    let (ex, ey) = (a * t.cos(), b * t.sin());
    let dist = (px - ex).hypot(py - ey);
    let (gx, gy) = (t.cos() / a, t.sin() / b);
    let gl = gx.hypot(gy).max(1e-12);
    let (nx, ny) = (-gx / gl * x.signum(), -gy / gl * y.signum());
    (if inside { dist } else { -dist }, if x == 0.0 { 0.0 } else { nx }, if y == 0.0 { 0.0 } else { ny })
}

/// Raster SDF for arbitrary outlines: anti-aliased coverage, sub-pixel edge seeds, jump
/// flooding, distance (sign from coverage), a σ = 3 texel blur and a Scharr gradient.
#[allow(clippy::too_many_arguments)]
fn path_sdf(d: &str, x0: f64, y0: f64, evenodd: bool, sw: usize, sh: usize, w: f64, h: f64, scale: f64, px: &mut [[f32; 4]]) {
    let mut alpha = vec![0f32; sw * sh];
    if let (Some(path), Some(mut pm)) = (svg_path(d), tiny_skia::Pixmap::new(sw as u32, sh as u32)) {
        let mut paint = tiny_skia::Paint::default();
        paint.set_color_rgba8(0, 0, 0, 255);
        paint.anti_alias = true;
        let rule = if evenodd { tiny_skia::FillRule::EvenOdd } else { tiny_skia::FillRule::Winding };
        let ts = tiny_skia::Transform::from_scale((sw as f64 / w) as f32, (sh as f64 / h) as f32).pre_translate(-x0 as f32, -y0 as f32);
        pm.fill_path(&path, &paint, rule, ts, None);
        for (a, p) in alpha.iter_mut().zip(pm.pixels()) {
            *a = p.alpha() as f32 / 255.0;
        }
    }
    let at = |c: isize, r: isize| if c >= 0 && r >= 0 && (c as usize) < sw && (r as usize) < sh { alpha[r as usize * sw + c as usize] } else { 0.0 };
    let far = 2.0 * sw.max(sh) as f32;
    let mut seed = vec![[far, far]; sw * sh];
    for r in 0..sh as isize {
        for c in 0..sw as isize {
            let a = at(c, r);
            let mut off = [0f32; 2];
            let mut count = 0.0;
            for (dc, dr) in [(1, 0), (0, 1), (-1, 0), (0, -1)] {
                let n = at(c + dc, r + dr);
                if (a - 0.5) * (n - 0.5) < 0.0 {
                    let t = (0.5 - a) / (n - a);
                    off[0] += t * dc as f32;
                    off[1] += t * dr as f32;
                    count += 1.0;
                }
            }
            if count > 0.0 {
                seed[r as usize * sw + c as usize] = [c as f32 + off[0] / count, r as f32 + off[1] / count];
            }
        }
    }
    let mut step = 1usize << (usize::BITS - 1 - sw.max(sh).leading_zeros());
    while step > 0 {
        for r in 0..sh {
            for c in 0..sw {
                let mut best = seed[r * sw + c];
                let mut bd = (c as f32 - best[0]).powi(2) + (r as f32 - best[1]).powi(2);
                for nr in [r.checked_sub(step), Some(r), Some(r + step).filter(|v| *v < sh)].into_iter().flatten() {
                    for nc in [c.checked_sub(step), Some(c), Some(c + step).filter(|v| *v < sw)].into_iter().flatten() {
                        let s = seed[nr * sw + nc];
                        let d = (c as f32 - s[0]).powi(2) + (r as f32 - s[1]).powi(2);
                        if d < bd {
                            bd = d;
                            best = s;
                        }
                    }
                }
                seed[r * sw + c] = best;
            }
        }
        step >>= 1;
    }
    let mut dist: Vec<f32> = (0..sw * sh).map(|i| {
        let (c, r) = ((i % sw) as f32, (i / sw) as f32);
        let s = seed[i];
        let v = ((c - s[0]).powi(2) + (r - s[1]).powi(2)).sqrt() / scale as f32;
        if alpha[i] >= 0.5 { v } else { -v }
    }).collect();
    blur(&mut dist, sw, sh, 3.0);
    let g = |c: usize, r: usize| dist[r * sw + c];
    for r in 0..sh {
        let (up, down) = (r.saturating_sub(1), (r + 1).min(sh - 1));
        for c in 0..sw {
            let (left, right) = (c.saturating_sub(1), (c + 1).min(sw - 1));
            let gx = 10.0 / 32.0 * (g(right, r) - g(left, r)) + 3.0 / 32.0 * (g(right, up) - g(left, up) + g(right, down) - g(left, down));
            let gy = 10.0 / 32.0 * (g(c, up) - g(c, down)) + 3.0 / 32.0 * (g(left, up) - g(left, down) + g(right, up) - g(right, down));
            px[r * sw + c] = [g(c, r), gx, gy, 1.0];
        }
    }
}

/// Separable Gaussian blur, edges clamped.
fn blur(v: &mut [f32], w: usize, h: usize, sigma: f32) {
    let radius = (sigma * 3.0).ceil() as isize;
    let mut k: Vec<f32> = (0..=radius).map(|i| (-(i * i) as f32 / (2.0 * sigma * sigma)).exp()).collect();
    let sum = k[0] + 2.0 * k[1..].iter().sum::<f32>();
    k.iter_mut().for_each(|x| *x /= sum);
    let mut tmp = vec![0f32; w.max(h)];
    for (len, stride, lines, step) in [(w, 1, h, w), (h, w, w, 1)] {
        for line in 0..lines {
            let at = |i: isize| v[line * step + (i.clamp(0, len as isize - 1) as usize) * stride];
            for (i, t) in tmp.iter_mut().enumerate().take(len) {
                let i = i as isize;
                *t = k[0] * at(i) + (1..=radius).map(|j| k[j as usize] * (at(i - j) + at(i + j))).sum::<f32>();
            }
            for (i, t) in tmp.iter().enumerate().take(len) {
                v[line * step + i * stride] = *t;
            }
        }
    }
}

fn svg_path(d: &str) -> Option<tiny_skia::Path> {
    use svgtypes::SimplePathSegment as S;
    let mut pb = tiny_skia::PathBuilder::new();
    for seg in svgtypes::SimplifyingPathParser::from(d) {
        match seg.ok()? {
            S::MoveTo { x, y } => pb.move_to(x as f32, y as f32),
            S::LineTo { x, y } => pb.line_to(x as f32, y as f32),
            S::CurveTo { x1, y1, x2, y2, x, y } => pb.cubic_to(x1 as f32, y1 as f32, x2 as f32, y2 as f32, x as f32, y as f32),
            S::Quadratic { x1, y1, x, y } => pb.quad_to(x1 as f32, y1 as f32, x as f32, y as f32),
            S::ClosePath => pb.close(),
        }
    }
    pb.finish()
}

struct Gpu {
    device: wgpu::Device,
    queue: wgpu::Queue,
    vs: wgpu::ShaderModule,
    pipes: HashMap<u64, Arc<(wgpu::RenderPipeline, wgpu::BindGroupLayout)>>,
    adapter: String,
    /// Vulkan: hand the driver our own strict SPIR-V (see `strict_spirv`).
    spirv: bool,
}

static GPU: Mutex<Option<Result<Gpu, String>>> = Mutex::new(None);
type Compiled = Arc<Result<Program, String>>;
static PROGRAMS: Mutex<Option<HashMap<u64, Compiled>>> = Mutex::new(None);

/// Full-screen triangle.
const VS: &str = "@vertex fn vs(@builtin(vertex_index) i: u32) -> @builtin(position) vec4<f32> {
    let p = vec2<f32>(f32((i << 1u) & 2u), f32(i & 2u));
    return vec4<f32>(p * 2.0 - 1.0, 0.0, 1.0);
}";

fn gpu_init() -> Result<Gpu, String> {
    let instance = wgpu::Instance::new(wgpu::InstanceDescriptor {
        backends: wgpu::Backends::from_env().unwrap_or(wgpu::Backends::PRIMARY),
        ..wgpu::InstanceDescriptor::new_without_display_handle()
    });
    let pick = |fallback: bool| pollster::block_on(instance.request_adapter(&wgpu::RequestAdapterOptions {
        power_preference: wgpu::PowerPreference::from_env().unwrap_or(wgpu::PowerPreference::LowPower),
        force_fallback_adapter: fallback,
        compatible_surface: None,
        apply_limit_buckets: false,
    }));
    // No GPU: the backends' software rasterizers (WARP on Windows) still run shaders.
    let adapter = pick(false).or_else(|_| pick(true)).map_err(|e| format!("no GPU adapter for shader fills: {e}"))?;
    let spirv = adapter.get_info().backend == wgpu::Backend::Vulkan && adapter.features().contains(wgpu::Features::PASSTHROUGH_SHADERS);
    let required_features = if spirv { wgpu::Features::PASSTHROUGH_SHADERS } else { wgpu::Features::empty() };
    let (device, queue) = pollster::block_on(adapter.request_device(&wgpu::DeviceDescriptor { label: Some("nalarupa shader fills"), required_features, ..Default::default() }))
        .map_err(|e| format!("GPU device for shader fills: {e}"))?;
    device.on_uncaptured_error(Arc::new(|e: wgpu::Error| eprintln!("shader fill GPU error: {e}")));
    let vs = device.create_shader_module(wgpu::ShaderModuleDescriptor { label: Some("nr-vs"), source: wgpu::ShaderSource::Wgsl(VS.into()) });
    let info = adapter.get_info();
    Ok(Gpu { device, queue, vs, pipes: HashMap::new(), adapter: format!("{} ({:?})", info.name, info.backend), spirv })
}

/// The shader reads `@backdrop` (so the caller has to paint what lies behind the node).
pub fn uses_backdrop(src: &str) -> bool {
    program(src).as_ref().as_ref().is_ok_and(|p| p.uniforms.iter().any(|u| u.kind == Kind::Backdrop))
}

/// Name of the adapter shader fills run on (starts the device if needed).
pub fn adapter() -> Result<String, String> {
    let mut g = GPU.lock().unwrap_or_else(|e| e.into_inner());
    g.get_or_insert_with(gpu_init).as_ref().map(|g| g.adapter.clone()).map_err(Clone::clone)
}

fn program(src: &str) -> Compiled {
    let key = crate::svg::fxhash(src);
    let mut cache = PROGRAMS.lock().unwrap_or_else(|e| e.into_inner());
    let cache = cache.get_or_insert_with(HashMap::new);
    if cache.len() > 64 {
        cache.clear();
    }
    cache.entry(key).or_insert_with(|| Arc::new(compile(src))).clone()
}

/// Pixels per unit of a shader fill's image: rendered at the canvas DPI (2 on Retina) and
/// scaled when zoomed in; exports use the same image.
pub const SCALE: f64 = 2.0;
static PNGS: Mutex<Option<HashMap<u64, String>>> = Mutex::new(None);

/// `render` at `SCALE`, as base64 PNG, cached by everything that changes the pixels.
/// `backdrop` = (a key of what lies behind the node, the function that paints it).
pub fn png(src: &str, uniforms: Option<&Map<String, Value>>, w: f64, h: f64, outline: &Outline, image: &mut dyn FnMut(&str) -> Option<tiny_skia::Pixmap>, backdrop: Option<(u64, &mut Backdrop<'_>)>) -> Result<String, String> {
    use base64::Engine as _;
    let given = uniforms.map(|u| Value::Object(u.clone()).to_string()).unwrap_or_default();
    let behind = backdrop.as_ref().map_or(0, |b| b.0);
    let key = crate::svg::fxhash(&format!("{src}\u{0}{given}\u{0}{w}x{h}\u{0}{outline:?}\u{0}{behind}"));
    if let Some(p) = PNGS.lock().unwrap_or_else(|e| e.into_inner()).as_ref().and_then(|c| c.get(&key)) {
        return Ok(p.clone());
    }
    let t = std::time::Instant::now();
    let pm = render(src, uniforms, w, h, SCALE, outline, image, backdrop.map(|b| b.1))?;
    let drawn = t.elapsed();
    let data = base64::engine::general_purpose::STANDARD.encode(encode_png(&pm)?);
    if std::env::var_os("NALARUPA_SHADER_TRACE").is_some() {
        eprintln!("shader fill {}x{}: render {drawn:?}, png {:?}", pm.width(), pm.height(), t.elapsed() - drawn);
    }
    let mut cache = PNGS.lock().unwrap_or_else(|e| e.into_inner());
    let cache = cache.get_or_insert_with(HashMap::new);
    if cache.len() > 32 {
        cache.clear();
    }
    cache.insert(key, data.clone());
    Ok(data)
}

/// PNG with fast compression: shader output is often noisy (grain), where the default level spends
/// ~0.7 s per megapixel.
fn encode_png(pm: &tiny_skia::Pixmap) -> Result<Vec<u8>, String> {
    let rgba: Vec<u8> = pm.pixels().iter().flat_map(|p| {
        let c = p.demultiply();
        [c.red(), c.green(), c.blue(), c.alpha()]
    }).collect();
    let mut out = Vec::new();
    let mut enc = png::Encoder::new(&mut out, pm.width(), pm.height());
    enc.set_color(png::ColorType::Rgba);
    enc.set_depth(png::BitDepth::Eight);
    enc.set_compression(png::Compression::Fast);
    enc.write_header().and_then(|mut w| w.write_image_data(&rgba)).map_err(|e| e.to_string())?;
    Ok(out)
}

/// Draw the shader in `src` over a `w`×`h` node at `scale` pixels per unit. `image` loads the
/// pixels behind a sampler2D uniform's url (premultiplied RGBA, top row first).
#[allow(clippy::too_many_arguments)]
pub fn render(src: &str, uniforms: Option<&Map<String, Value>>, w: f64, h: f64, scale: f64, outline: &Outline, image: &mut dyn FnMut(&str) -> Option<tiny_skia::Pixmap>, mut backdrop: Option<&mut Backdrop<'_>>) -> Result<tiny_skia::Pixmap, String> {
    if !(w > 0.0 && h > 0.0) {
        return Err("empty fill".into());
    }
    let prog = program(src);
    let prog = prog.as_ref().as_ref().map_err(Clone::clone)?;
    let (pw, ph) = ((w * scale).ceil().clamp(1.0, MAX_SIDE) as u32, (h * scale).ceil().clamp(1.0, MAX_SIDE) as u32);
    let mut ubo = vec![0u8; prog.size as usize];
    let mut put = |member: &str, vals: &[f64]| {
        if let Some(&(off, int)) = prog.layout.get(member) {
            for (i, v) in vals.iter().enumerate() {
                let at = off as usize + i * 4;
                if at + 4 <= ubo.len() {
                    let bytes = if int { (*v as i32).to_le_bytes() } else { (*v as f32).to_le_bytes() };
                    ubo[at..at + 4].copy_from_slice(&bytes);
                }
            }
        }
    };
    let mut textures = Vec::new();
    for u in &prog.uniforms {
        let given = uniforms.and_then(|m| m.get(&u.name));
        match &u.kind {
            Kind::Image => {
                let pm = given.and_then(Value::as_str).filter(|s| !s.is_empty()).and_then(&mut *image);
                textures.push((pm.map_or(Tex::Rgba8(1, 1, vec![0; 4]), |p| Tex::Rgba8(p.width(), p.height(), p.data().to_vec())), wgpu::AddressMode::Repeat));
            }
            Kind::Sdf => textures.push((sdf(outline, w, h, scale), wgpu::AddressMode::ClampToEdge)),
            Kind::Backdrop => {
                // The node's box at the render scale, at most 2048 px per side, GL rows.
                let r = (w.max(h) * scale).max(1e-9);
                let k = scale * r.min(BACKDROP_MAX) / r;
                let (bw, bh) = ((w * k).ceil().max(1.0) as u32, (h * k).ceil().max(1.0) as u32);
                let tex = backdrop.as_mut().and_then(|f| f(bw, bh)).map(|p| {
                    let row = p.width() as usize * 4;
                    Tex::Rgba8(p.width(), p.height(), p.data().chunks_exact(row).rev().flatten().copied().collect())
                });
                textures.push((tex.unwrap_or(Tex::Rgba8(1, 1, vec![0; 4])), wgpu::AddressMode::MirrorRepeat));
            }
            k => put(&u.member, &components(k, given, w, h)),
        }
    }
    put("nr_unit", &[w / pw as f64, h / ph as f64, h, 0.0]);

    let mut guard = GPU.lock().unwrap_or_else(|e| e.into_inner());
    let gpu = match guard.get_or_insert_with(gpu_init) {
        Ok(g) => g,
        Err(e) => return Err(e.clone()),
    };
    let t = std::time::Instant::now();
    let result = draw(gpu, src, prog, &ubo, &textures, pw, ph);
    if std::env::var_os("NALARUPA_SHADER_TRACE").is_some() {
        eprintln!("  gpu {:?} on {}", t.elapsed(), gpu.adapter);
    }
    if result.as_ref().is_err_and(|e| e.starts_with("GPU:")) {
        // Lost device or timeout: start over with a fresh one next time.
        *guard = None;
    }
    result
}

/// naga's SPIR-V with every float add, sub, mul, div, mod and dot marked NoContraction (no fusing,
/// no re-association). Vulkan drivers otherwise rewrite e.g. `dot(i + c, k)` per call site, and the
/// ubiquitous hash `fract(sin(dot(p, k)) * 43758.5)` then gives one lattice point a different value
/// in each neighbouring cell: blocky noise. WebGL (ANGLE on D3D) keeps such hashes consistent.
fn strict_spirv(prog: &Program) -> Result<Vec<u32>, String> {
    // OpFAdd, OpFSub, OpFMul, OpFDiv, OpFRem, OpFMod, Op{Vector,Matrix}Times*, OpDot.
    const FLOAT_OPS: [u32; 12] = [129, 131, 133, 136, 140, 141, 142, 143, 144, 145, 146, 148];
    let pipe = naga::back::spv::PipelineOptions { shader_stage: naga::ShaderStage::Fragment, entry_point: "main".into() };
    let mut words = naga::back::spv::write_vec(&prog.module, &prog.info, &naga::back::spv::Options::default(), Some(&pipe)).map_err(|e| format!("shader compile: {e}"))?;
    let (mut ids, mut types_at, mut i) = (Vec::new(), None, 5);
    while i < words.len() {
        let (op, len) = (words[i] & 0xffff, (words[i] >> 16) as usize);
        if len == 0 {
            return Err("shader compile: malformed SPIR-V".into());
        }
        if FLOAT_OPS.contains(&op) && i + 2 < words.len() {
            ids.push(words[i + 2]);
        }
        // Annotations end where the first type declaration (OpTypeVoid … OpTypeForwardPointer) starts.
        if types_at.is_none() && (19..=39).contains(&op) {
            types_at = Some(i);
        }
        i += len;
    }
    let at = types_at.ok_or("shader compile: SPIR-V without types")?;
    // OpDecorate <id> NoContraction (decoration 42).
    let deco: Vec<u32> = ids.iter().flat_map(|id| [(3 << 16) | 71, *id, 42]).collect();
    words.splice(at..at, deco);
    Ok(words)
}

fn pipeline(gpu: &mut Gpu, src: &str, prog: &Program, samplers: usize) -> Result<Arc<(wgpu::RenderPipeline, wgpu::BindGroupLayout)>, String> {
    let key = crate::svg::fxhash(src);
    if let Some(p) = gpu.pipes.get(&key) {
        return Ok(p.clone());
    }
    let d = &gpu.device;
    let scope = d.push_error_scope(wgpu::ErrorFilter::Validation);
    let fs = if gpu.spirv {
        let spirv = strict_spirv(prog)?;
        let entry = wgpu::PassthroughShaderEntryPoint { name: "main".into(), workgroup_size: (0, 0, 0) };
        // SAFETY: the words are naga's SPIR-V for a module it validated, plus NoContraction decorations.
        unsafe {
            d.create_shader_module_passthrough(wgpu::ShaderModuleDescriptorPassthrough {
                label: Some("nr-fs"),
                entry_points: Cow::Owned(vec![entry]),
                spirv: Some(Cow::Owned(spirv)),
                ..Default::default()
            })
        }
    } else {
        d.create_shader_module(wgpu::ShaderModuleDescriptor { label: Some("nr-fs"), source: wgpu::ShaderSource::Naga(Cow::Owned(prog.module.clone())) })
    };
    let mut entries = vec![wgpu::BindGroupLayoutEntry {
        binding: 0,
        visibility: wgpu::ShaderStages::FRAGMENT,
        ty: wgpu::BindingType::Buffer { ty: wgpu::BufferBindingType::Uniform, has_dynamic_offset: false, min_binding_size: None },
        count: None,
    }];
    for i in 0..samplers as u32 {
        entries.push(wgpu::BindGroupLayoutEntry {
            binding: 1 + 2 * i,
            visibility: wgpu::ShaderStages::FRAGMENT,
            ty: wgpu::BindingType::Texture { sample_type: wgpu::TextureSampleType::Float { filterable: true }, view_dimension: wgpu::TextureViewDimension::D2, multisampled: false },
            count: None,
        });
        entries.push(wgpu::BindGroupLayoutEntry { binding: 2 + 2 * i, visibility: wgpu::ShaderStages::FRAGMENT, ty: wgpu::BindingType::Sampler(wgpu::SamplerBindingType::Filtering), count: None });
    }
    let bgl = d.create_bind_group_layout(&wgpu::BindGroupLayoutDescriptor { label: None, entries: &entries });
    let layout = d.create_pipeline_layout(&wgpu::PipelineLayoutDescriptor { label: None, bind_group_layouts: &[Some(&bgl)], immediate_size: 0 });
    let pipe = d.create_render_pipeline(&wgpu::RenderPipelineDescriptor {
        label: Some("nr-shader-fill"),
        layout: Some(&layout),
        vertex: wgpu::VertexState { module: &gpu.vs, entry_point: Some("vs"), compilation_options: Default::default(), buffers: &[] },
        fragment: Some(wgpu::FragmentState {
            module: &fs,
            entry_point: Some("main"),
            compilation_options: Default::default(),
            targets: &[Some(wgpu::ColorTargetState { format: wgpu::TextureFormat::Rgba8Unorm, blend: None, write_mask: wgpu::ColorWrites::ALL })],
        }),
        primitive: wgpu::PrimitiveState::default(),
        depth_stencil: None,
        multisample: wgpu::MultisampleState::default(),
        multiview_mask: None,
        cache: None,
    });
    if let Some(e) = pollster::block_on(scope.pop()) {
        return Err(format!("shader compile: {e}"));
    }
    let p = Arc::new((pipe, bgl));
    gpu.pipes.insert(key, p.clone());
    Ok(p)
}

fn draw(gpu: &mut Gpu, src: &str, prog: &Program, ubo: &[u8], textures: &[(Tex, wgpu::AddressMode)], pw: u32, ph: u32) -> Result<tiny_skia::Pixmap, String> {
    let t = std::time::Instant::now();
    let p = pipeline(gpu, src, prog, textures.len())?;
    if std::env::var_os("NALARUPA_SHADER_TRACE").is_some() {
        eprintln!("  pipeline {:?}", t.elapsed());
    }
    let (pipe, bgl) = (&p.0, &p.1);
    let (d, q) = (&gpu.device, &gpu.queue);
    let ubuf = d.create_buffer(&wgpu::BufferDescriptor { label: None, size: ubo.len().max(16) as u64, usage: wgpu::BufferUsages::UNIFORM | wgpu::BufferUsages::COPY_DST, mapped_at_creation: false });
    q.write_buffer(&ubuf, 0, ubo);
    let mut views = Vec::new();
    for (t, mode) in textures {
        let (tw, th, format, bytes, bpp): (u32, u32, _, &[u8], u32) = match t {
            Tex::Rgba8(w, h, data) => (*w, *h, wgpu::TextureFormat::Rgba8Unorm, data, 4),
            Tex::Half(w, h, data) => (*w, *h, wgpu::TextureFormat::Rgba16Float, bytemuck_u16(data), 8),
        };
        let size = wgpu::Extent3d { width: tw, height: th, depth_or_array_layers: 1 };
        let tex = d.create_texture(&wgpu::TextureDescriptor { label: None, size, mip_level_count: 1, sample_count: 1, dimension: wgpu::TextureDimension::D2, format, usage: wgpu::TextureUsages::TEXTURE_BINDING | wgpu::TextureUsages::COPY_DST, view_formats: &[] });
        q.write_texture(
            wgpu::TexelCopyTextureInfo { texture: &tex, mip_level: 0, origin: wgpu::Origin3d::ZERO, aspect: wgpu::TextureAspect::All },
            bytes,
            wgpu::TexelCopyBufferLayout { offset: 0, bytes_per_row: Some(tw * bpp), rows_per_image: Some(th) },
            size,
        );
        let sampler = d.create_sampler(&wgpu::SamplerDescriptor {
            address_mode_u: *mode,
            address_mode_v: *mode,
            mag_filter: wgpu::FilterMode::Linear,
            min_filter: wgpu::FilterMode::Linear,
            ..Default::default()
        });
        views.push((tex.create_view(&Default::default()), sampler));
    }
    let mut entries = vec![wgpu::BindGroupEntry { binding: 0, resource: ubuf.as_entire_binding() }];
    for (i, (v, s)) in views.iter().enumerate() {
        entries.push(wgpu::BindGroupEntry { binding: 1 + 2 * i as u32, resource: wgpu::BindingResource::TextureView(v) });
        entries.push(wgpu::BindGroupEntry { binding: 2 + 2 * i as u32, resource: wgpu::BindingResource::Sampler(s) });
    }
    let group = d.create_bind_group(&wgpu::BindGroupDescriptor { label: None, layout: bgl, entries: &entries });
    let size = wgpu::Extent3d { width: pw, height: ph, depth_or_array_layers: 1 };
    let target = d.create_texture(&wgpu::TextureDescriptor { label: None, size, mip_level_count: 1, sample_count: 1, dimension: wgpu::TextureDimension::D2, format: wgpu::TextureFormat::Rgba8Unorm, usage: wgpu::TextureUsages::RENDER_ATTACHMENT | wgpu::TextureUsages::COPY_SRC, view_formats: &[] });
    let view = target.create_view(&Default::default());
    let wait = || d.poll(wgpu::PollType::Wait { submission_index: None, timeout: Some(std::time::Duration::from_secs(20)) }).map_err(|e| format!("GPU: {e}"));
    for y0 in (0..ph).step_by(TILE as usize) {
        let mut enc = d.create_command_encoder(&Default::default());
        {
            let mut pass = enc.begin_render_pass(&wgpu::RenderPassDescriptor {
                label: None,
                color_attachments: &[Some(wgpu::RenderPassColorAttachment {
                    view: &view,
                    depth_slice: None,
                    resolve_target: None,
                    ops: wgpu::Operations { load: if y0 == 0 { wgpu::LoadOp::Clear(wgpu::Color::TRANSPARENT) } else { wgpu::LoadOp::Load }, store: wgpu::StoreOp::Store },
                })],
                ..Default::default()
            });
            pass.set_pipeline(pipe);
            pass.set_bind_group(0, &group, &[]);
            pass.set_scissor_rect(0, y0, pw, TILE.min(ph - y0));
            pass.draw(0..3, 0..1);
        }
        q.submit([enc.finish()]);
        wait()?;
    }
    let row = (pw * 4).div_ceil(256) * 256;
    let buf = d.create_buffer(&wgpu::BufferDescriptor { label: None, size: (row * ph) as u64, usage: wgpu::BufferUsages::MAP_READ | wgpu::BufferUsages::COPY_DST, mapped_at_creation: false });
    let mut enc = d.create_command_encoder(&Default::default());
    enc.copy_texture_to_buffer(
        wgpu::TexelCopyTextureInfo { texture: &target, mip_level: 0, origin: wgpu::Origin3d::ZERO, aspect: wgpu::TextureAspect::All },
        wgpu::TexelCopyBufferInfo { buffer: &buf, layout: wgpu::TexelCopyBufferLayout { offset: 0, bytes_per_row: Some(row), rows_per_image: Some(ph) } },
        size,
    );
    q.submit([enc.finish()]);
    let slice = buf.slice(..);
    let (tx, rx) = std::sync::mpsc::channel();
    slice.map_async(wgpu::MapMode::Read, move |r| {
        let _ = tx.send(r);
    });
    wait()?;
    rx.recv().map_err(|e| format!("GPU: {e}"))?.map_err(|e| format!("GPU: {e}"))?;
    let mut pm = tiny_skia::Pixmap::new(pw, ph).ok_or("image too large")?;
    {
        let data = slice.get_mapped_range().map_err(|e| format!("GPU: {e}"))?;
        let out = pm.data_mut();
        for r in 0..ph as usize {
            let src = &data[r * row as usize..r * row as usize + pw as usize * 4];
            let dst = &mut out[r * pw as usize * 4..(r + 1) * pw as usize * 4];
            for (o, i) in dst.chunks_exact_mut(4).zip(src.chunks_exact(4)) {
                // SkSL output is premultiplied; keep it a valid premultiplied pixel.
                let a = i[3];
                o.copy_from_slice(&[i[0].min(a), i[1].min(a), i[2].min(a), a]);
            }
        }
    }
    buf.unmap();
    Ok(pm)
}

fn bytemuck_u16(v: &[u16]) -> &[u8] {
    // SAFETY: u16 has no padding and u8 has alignment 1; the length is the byte length.
    unsafe { std::slice::from_raw_parts(v.as_ptr().cast::<u8>(), std::mem::size_of_val(v)) }
}

#[cfg(test)]
mod tests {
    use super::*;

    const DEMO: &str = r#"
precision mediump float;
/** @resolution */
uniform vec2 u_res;
/** @time */
uniform float u_time;
/**
 * Tint used on the left half.
 * @label Tint @color
 * @default #ff0000
 */
uniform vec3 u_tint;
/** @default 0.25 @range 0, 1 */
uniform float u_mix;
/** @default true */
uniform bool u_on;
/** @sdf */
uniform sampler2D u_shape;
vec3 shade(vec3 c) { return c * 0.5; }
vec2 shade(vec2 c) { return c; }
void main() {
  vec2 uv = gl_FragCoord.xy / u_res;
  float d = texture2D(u_shape, uv).r;
  vec2 size = vec2(textureSize(u_shape, 0));
  vec3 c = uv.x < 0.5 ? u_tint : vec3(0.0, uv.y, u_mix);
  if (!u_on) { c = shade(c); }
  gl_FragColor = vec4(c, d > 0.0 && size.x > 0.0 ? 1.0 : 0.0);
}
"#;

    #[test]
    fn annotations_and_transpile() {
        let (glsl, u) = transpile(DEMO).unwrap();
        assert!(glsl.contains("sampler2D(u_shape_nrt, u_shape_nrs)") && glsl.contains("bool(u_on_nrb)") && !glsl.contains("mediump"), "{glsl}");
        let (swz, _) = transpile("uniform sampler2D b; void main() { vec3 c = vec3(1.0); gl_FragColor = vec4(c.b) + texture2D(b, vec2(0.5)); }").unwrap();
        assert!(swz.contains("vec4(c.b)") && swz.contains("texture(sampler2D(b_nrt, b_nrs)"), "swizzles keep their name: {swz}");
        assert!(compile("uniform bvec2 f; void main() { gl_FragColor = vec4(f.x ? 1.0 : 0.0); }").is_ok());
        let k: Vec<(&str, &Kind)> = u.iter().map(|u| (u.name.as_str(), &u.kind)).collect();
        assert_eq!(k[0], ("u_res", &Kind::Resolution(false)));
        assert_eq!(k[1], ("u_time", &Kind::Time));
        assert_eq!(k[2], ("u_tint", &Kind::Color([1.0, 0.0, 0.0, 1.0], false)));
        assert_eq!(k[3], ("u_mix", &Kind::Float(0.25)));
        assert_eq!(k[4], ("u_on", &Kind::Bool(true)));
        assert_eq!(k[5], ("u_shape", &Kind::Sdf));
        let p = compile(DEMO).unwrap();
        assert!(p.layout.contains_key("u_on_nrb") && p.layout.contains_key("nr_unit"));
        assert!(compile("void main() { }").unwrap_err().contains("No color output"));
        assert!(compile("/** @bogus */ uniform float x; void main() { gl_FragColor = vec4(x); }").unwrap_err().contains("@bogus"));
        assert!(compile("void main() { gl_FragColor = vec4(oops); }").unwrap_err().contains("line 1"));
    }

    #[test]
    fn sdf_of_a_circle() {
        let Tex::Half(w, h, px) = sdf(&Outline::Ellipse, 100.0, 100.0, 1.0) else { panic!() };
        assert_eq!((w, h), (100, 100));
        let back = |v: u16| {
            let e = ((v >> 10) & 0x1f) as i32;
            let m = (v & 0x3ff) as f32;
            let s = if v & 0x8000 != 0 { -1.0 } else { 1.0 };
            s * if e == 0 { m / 1024.0 * 2f32.powi(-14) } else { (1.0 + m / 1024.0) * 2f32.powi(e - 15) }
        };
        let centre = back(px[(50 * 100 + 50) * 4]);
        let corner = back(px[0]);
        assert!((centre - 49.3).abs() < 0.5 && corner < -19.0, "{centre} {corner}");
        // A path outline gives the same field, except that it is blurred (σ = 3 texels), which
        // lowers the cone's peak by about σ·√(π/2).
        let path = Outline::Path { d: "M0 50A50 50 0 1 0 100 50A50 50 0 1 0 0 50Z".into(), x: 0.0, y: 0.0, evenodd: false };
        let Tex::Half(_, _, p2) = sdf(&path, 100.0, 100.0, 1.0) else { panic!() };
        let (c2, e2) = (back(p2[(50 * 100 + 50) * 4]), back(p2[(50 * 100 + 1) * 4]));
        assert!(centre - c2 > 2.5 && centre - c2 < 4.5 && (e2 - 1.5).abs() < 1.0, "{c2} {e2}");
    }

    #[test]
    fn renders_on_the_gpu() {
        if let Err(e) = adapter() {
            eprintln!("no GPU adapter, skipped: {e}");
            return;
        }
        let src = "/** @resolution */ uniform vec2 r;\n/** @color @default #0000ff */ uniform vec3 c;\n/** @sdf */ uniform sampler2D s;\n/** @backdrop */ uniform sampler2D b;\n\
            void main() { vec2 uv = gl_FragCoord.xy / r; float inside = texture2D(s, uv).r > 0.0 ? 1.0 : 0.0; gl_FragColor = vec4(uv.y, texture2D(b, uv).g, c.b * inside, 1.0); }";
        let mut behind = |w: u32, h: u32| {
            let mut p = tiny_skia::Pixmap::new(w, h)?;
            p.fill(tiny_skia::Color::from_rgba8(0, 128, 0, 255));
            Some(p)
        };
        let pm = render(src, None, 64.0, 32.0, 2.0, &Outline::Ellipse, &mut |_| None, Some(&mut behind)).unwrap();
        assert_eq!((pm.width(), pm.height()), (128, 64));
        let px = |x: u32, y: u32| pm.pixel(x, y).unwrap();
        assert!(px(64, 0).red() > 250 && px(64, 63).red() < 5, "origin bottom-left: the top row has uv.y ≈ 1");
        assert!((px(64, 32).green() as i32 - 128).abs() <= 2, "backdrop sampled");
        assert!(px(64, 32).blue() > 250 && px(0, 0).blue() == 0, "@default colour, and the SDF is positive inside the ellipse only");
        // A hash that Vulkan fast-math would make differ between neighbouring lattice cells.
        let hash = "float h(vec3 p) { return fract(sin(dot(p, vec3(127.1, 311.7, 74.7))) * 43758.5453); }\n\
            void main() { vec3 p = vec3(gl_FragCoord.xy / 4.0, 0.3); vec3 i = floor(p); gl_FragColor = vec4(abs(h(i + vec3(1.0, 0.0, 0.0)) - h(floor(p + vec3(1.0, 0.0, 0.0)))) * 1000.0, 0.0, 0.0, 1.0); }";
        let pm = render(hash, None, 64.0, 64.0, 1.0, &Outline::RRect([0.0; 4]), &mut |_| None, None).unwrap();
        assert!(pm.pixels().iter().all(|p| p.red() == 0), "the same lattice point hashes the same from every cell");
    }

    #[test]
    fn broken_shaders_show_error_hatch() {
        let dir = std::env::temp_dir().join(format!("nalarupa-shader-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("bad.glsl"), "void main() { }").unwrap();
        let fill = |url: &str| json!({"type": "frame", "id": url.trim_start_matches("./").replace('.', "-"), "x": 0, "y": 0, "width": 50, "height": 50, "layout": "none", "fill": [{"type": "shader", "url": url}]});
        std::fs::write(dir.join("doc.pen"), json!({"version": "2.19", "children": [fill("./bad.glsl"), fill("./missing.glsl")]}).to_string()).unwrap();
        let mut e = crate::engine::Engine::new(dir.join("data"));
        e.offline();
        e.open(&dir.join("doc.pen")).unwrap();
        for id in ["bad-glsl", "missing-glsl"] {
            let (svg, _) = e.ctx().node_svg(id).unwrap();
            assert!(svg.contains("rotate(135) scale(5)"), "{svg}");
        }
    }
}
