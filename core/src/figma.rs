//! Figma clipboard → .pen nodes. Figma's `text/html` clipboard carries a "fig-kiwi" archive, base64
//! between `<!--(figma)` and `(/figma)-->`: a kiwi schema and a kiwi message, each raw-deflate or
//! zstd compressed. Instances are expanded with their overrides and styles, variables are
//! resolved to the values of their modes, and the result is plain frames, groups, text, shapes and
//! paths (components do not stay linked).

use base64::Engine as _;
use serde_json::{json, Map, Value};
use std::cell::Cell;
use std::collections::HashMap;
use std::io::Read;

/// Decompressed size, nesting and output are capped: the archive comes from the clipboard.
const MAX_BYTES: usize = 256 << 20;
const MAX_DEPTH: usize = 64;
const MAX_NODES: usize = 100_000;
const TOO_BIG: &str = "bad Figma clipboard data: decompressed size exceeds 256 MB";

/// The nodes of a Figma copy in paint order, positioned in Figma page coordinates, each with its
/// Figma box (x, y, w, h); `None` when `html` is not Figma data.
pub fn decode(html: &str) -> Result<Option<Vec<(Value, (f64, f64, f64, f64))>>, String> {
    let Some(b64) = buffer(html) else { return Ok(None) };
    let bytes = base64::engine::general_purpose::STANDARD.decode(b64).map_err(|e| format!("bad Figma clipboard data: {e}"))?;
    let msg = message(&bytes)?;
    Ok(Some(Doc::new(&msg).page()))
}

/// Base64 archive in Figma's `data-buffer` (the `data-metadata` element is also required).
fn buffer(html: &str) -> Option<String> {
    if !html.contains("data-metadata") {
        return None;
    }
    let i = html.find("(figma)")? + "(figma)".len();
    let j = i + html[i..].find("(/figma)")?;
    Some(html[i..j].chars().filter(|c| !c.is_ascii_whitespace()).collect())
}

/// Archive: "fig-kiwi" | "fig-jam.", u32 version, then (u32 length, bytes) chunks: schema, message.
fn message(b: &[u8]) -> Result<Value, String> {
    if b.len() < 12 || (&b[..8] != b"fig-kiwi" && &b[..8] != b"fig-jam.") {
        return Err("not a Figma archive".into());
    }
    let mut chunks = Vec::new();
    let mut i = 12;
    while i + 4 < b.len() {
        let n = u32::from_le_bytes([b[i], b[i + 1], b[i + 2], b[i + 3]]) as usize;
        let end = (i + 4).checked_add(n).filter(|e| *e <= b.len()).ok_or("truncated Figma archive")?;
        chunks.push(&b[i + 4..end]);
        i = end;
    }
    let [schema, data, ..] = chunks[..] else { return Err("truncated Figma archive".into()) };
    let defs = Schema::read(&inflate(schema)?)?;
    let root = defs.defs.iter().position(|d| d.name == "Message").ok_or("Figma schema has no Message")?;
    defs.decode(&mut Bb { d: &inflate(data)?, i: 0 }, root, 0)
}

/// Raw deflate, or zstd (newer Figma builds) by its magic number.
fn inflate(c: &[u8]) -> Result<Vec<u8>, String> {
    if c.starts_with(&[0x28, 0xb5, 0x2f, 0xfd]) {
        let mut out = Vec::new();
        zstd::stream::read::Decoder::new(c).and_then(|d| d.take(MAX_BYTES as u64 + 1).read_to_end(&mut out)).map_err(|e| format!("bad Figma clipboard data: {e}"))?;
        return if out.len() > MAX_BYTES { Err(TOO_BIG.into()) } else { Ok(out) };
    }
    // Not `{e:?}`: DecompressError's Debug dumps the whole partial output buffer.
    miniz_oxide::inflate::decompress_to_vec_with_limit(c, MAX_BYTES).map_err(|e| match e.status {
        miniz_oxide::inflate::TINFLStatus::HasMoreOutput => TOO_BIG.into(),
        _ => format!("bad Figma clipboard data: {e}"),
    })
}

// ---- kiwi (github.com/evanw/kiwi) binary schema and message decoding ----

struct Bb<'a> {
    d: &'a [u8],
    i: usize,
}

const EOF: &str = "truncated Figma clipboard data";

impl Bb<'_> {
    fn byte(&mut self) -> Result<u8, String> {
        let b = *self.d.get(self.i).ok_or(EOF)?;
        self.i += 1;
        Ok(b)
    }
    fn uint(&mut self) -> Result<u32, String> {
        let (mut v, mut s) = (0u32, 0u32);
        loop {
            let b = self.byte()?;
            v |= ((b & 127) as u32) << s;
            s += 7;
            if b & 128 == 0 || s >= 35 {
                return Ok(v);
            }
        }
    }
    fn int(&mut self) -> Result<i32, String> {
        let v = self.uint()?;
        Ok(if v & 1 == 1 { !((v >> 1) as i32) } else { (v >> 1) as i32 })
    }
    fn uint64(&mut self) -> Result<u64, String> {
        let (mut v, mut s) = (0u64, 0u32);
        loop {
            let b = self.byte()?;
            if b & 128 == 0 || s >= 56 {
                return Ok(v | (b as u64) << s);
            }
            v |= ((b & 127) as u64) << s;
            s += 7;
        }
    }
    /// One zero byte for 0, else 4 bytes with the exponent rotated to the front.
    fn float(&mut self) -> Result<f32, String> {
        if *self.d.get(self.i).ok_or(EOF)? == 0 {
            self.i += 1;
            return Ok(0.0);
        }
        let b = self.d.get(self.i..self.i + 4).ok_or(EOF)?;
        self.i += 4;
        Ok(f32::from_bits(u32::from_le_bytes([b[0], b[1], b[2], b[3]]).rotate_left(23)))
    }
    fn string(&mut self) -> Result<String, String> {
        let rest = self.d.get(self.i..).ok_or(EOF)?;
        let n = rest.iter().position(|b| *b == 0).ok_or(EOF)?;
        self.i += n + 1;
        Ok(String::from_utf8_lossy(&rest[..n]).into_owned())
    }
    fn left(&self) -> usize {
        self.d.len().saturating_sub(self.i)
    }
}

enum Ty {
    Bool,
    Byte,
    Int,
    Uint,
    Float,
    Str,
    Int64,
    Uint64,
    Def(usize),
}

struct Field {
    name: String,
    ty: Option<Ty>,
    array: bool,
    id: u32,
}

/// kind: 0 enum, 1 struct, 2 message.
struct Def {
    name: String,
    kind: u8,
    fields: Vec<Field>,
}

struct Schema {
    defs: Vec<Def>,
}

impl Schema {
    fn read(b: &[u8]) -> Result<Schema, String> {
        let mut bb = Bb { d: b, i: 0 };
        let mut raw = Vec::new();
        for _ in 0..bb.uint()? {
            let (name, kind) = (bb.string()?, bb.byte()?);
            let mut fields = Vec::new();
            for _ in 0..bb.uint()? {
                fields.push((bb.string()?, bb.int()?, bb.byte()? & 1 == 1, bb.uint()?));
            }
            raw.push((name, kind, fields));
        }
        let n = raw.len();
        let defs = raw.into_iter().map(|(name, kind, fields)| {
            if kind > 2 {
                return Err(format!("bad Figma schema kind {kind}"));
            }
            let fields = fields.into_iter().map(|(name, t, array, id)| {
                let ty = if kind == 0 {
                    None
                } else if t < 0 {
                    Some(match !t {
                        0 => Ty::Bool,
                        1 => Ty::Byte,
                        2 => Ty::Int,
                        3 => Ty::Uint,
                        4 => Ty::Float,
                        5 => Ty::Str,
                        6 => Ty::Int64,
                        7 => Ty::Uint64,
                        _ => return Err(format!("bad Figma schema type {t}")),
                    })
                } else if (t as usize) < n {
                    Some(Ty::Def(t as usize))
                } else {
                    return Err(format!("bad Figma schema type {t}"));
                };
                Ok(Field { name, ty, array, id })
            }).collect::<Result<_, String>>()?;
            Ok(Def { name, kind, fields })
        }).collect::<Result<_, String>>()?;
        Ok(Schema { defs })
    }

    /// Byte arrays become lowercase hex strings.
    fn decode(&self, bb: &mut Bb, def: usize, depth: usize) -> Result<Value, String> {
        if depth > MAX_DEPTH {
            return Err("Figma data nested too deep".into());
        }
        let d = &self.defs[def];
        let mut out = Map::new();
        if d.kind == 2 {
            loop {
                let id = bb.uint()?;
                if id == 0 {
                    return Ok(Value::Object(out));
                }
                let f = d.fields.iter().find(|f| f.id == id).ok_or("bad Figma message")?;
                out.insert(f.name.clone(), self.field(bb, f, depth)?);
            }
        }
        for f in &d.fields {
            out.insert(f.name.clone(), self.field(bb, f, depth)?);
        }
        Ok(Value::Object(out))
    }

    fn field(&self, bb: &mut Bb, f: &Field, depth: usize) -> Result<Value, String> {
        let ty = f.ty.as_ref().ok_or("bad Figma schema")?;
        if !f.array {
            return self.value(bb, ty, depth);
        }
        let n = bb.uint()? as usize;
        if n > bb.left() {
            return Err(EOF.into());
        }
        if let Ty::Byte = ty {
            let s = &bb.d[bb.i..bb.i + n];
            bb.i += n;
            return Ok(Value::String(s.iter().map(|b| format!("{b:02x}")).collect()));
        }
        (0..n).map(|_| self.value(bb, ty, depth)).collect::<Result<Vec<_>, _>>().map(Value::Array)
    }

    fn value(&self, bb: &mut Bb, ty: &Ty, depth: usize) -> Result<Value, String> {
        Ok(match ty {
            Ty::Bool => json!(bb.byte()? != 0),
            Ty::Byte => json!(bb.byte()?),
            Ty::Int => json!(bb.int()?),
            Ty::Uint => json!(bb.uint()?),
            Ty::Float => json!(bb.float()? as f64),
            Ty::Str => json!(bb.string()?),
            Ty::Int64 => {
                let v = bb.uint64()?;
                json!(if v & 1 == 1 { !((v >> 1) as i64) } else { (v >> 1) as i64 })
            }
            Ty::Uint64 => json!(bb.uint64()?),
            Ty::Def(i) if self.defs[*i].kind == 0 => {
                let v = bb.uint()?;
                self.defs[*i].fields.iter().find(|f| f.id == v).map_or(Value::Null, |f| json!(f.name))
            }
            Ty::Def(i) => self.decode(bb, *i, depth + 1)?,
        })
    }
}

// ---- Figma node tree → .pen ----

fn key(g: &Value) -> Option<String> {
    Some(format!("{}:{}", g["sessionID"].as_u64()?, g["localID"].as_u64()?))
}

/// A guid that is set (Figma marks "none" with 0xFFFFFFFF).
fn real(g: &Value) -> bool {
    g.is_object() && g["sessionID"] != json!(u32::MAX) && g["localID"] != json!(u32::MAX)
}

fn same(a: &Value, b: &Value) -> bool {
    a.is_object() && b.is_object() && a["sessionID"] == b["sessionID"] && a["localID"] == b["localID"]
}

/// Library key, else local guid, of a style/variable/set reference.
fn ref_key(t: &Value) -> Option<String> {
    match t["assetRef"]["key"].as_str() {
        Some(k) if !k.is_empty() => Some(k.to_string()),
        _ if real(&t["guid"]) => key(&t["guid"]),
        _ => None,
    }
}

fn truthy(v: &Value) -> bool {
    !matches!(v, Value::Null | Value::Bool(false)) && v.as_f64() != Some(0.0) && v.as_str() != Some("")
}

fn f(v: &Value) -> Option<f64> {
    v.as_f64().filter(|x| x.is_finite())
}

fn arr(v: &Value) -> Vec<Value> {
    v.as_array().cloned().unwrap_or_default()
}

/// What variable resolution reads from a node: its modes and component property assignments.
fn scope(p: &Value) -> Value {
    json!({"variableModeBySetMap": p["variableModeBySetMap"], "componentPropAssignments": p["componentPropAssignments"]})
}

#[derive(Clone)]
struct N {
    p: Value,
    kids: Vec<N>,
}

struct Doc {
    props: HashMap<String, Value>,
    /// Child guids, bottom (lowest position) first.
    kids: HashMap<String, Vec<String>>,
    blobs: Vec<Vec<u8>>,
    /// Variable key → (set key, per-mode entries).
    vars: HashMap<String, (Option<String>, Vec<Value>)>,
    modes: HashMap<String, String>,
    styles: HashMap<String, Value>,
    /// Nodes left to convert: nested instances could otherwise multiply without bound.
    budget: Cell<usize>,
}

/// Figma's sizing → .pen sizing.
#[derive(Clone, Copy, PartialEq)]
enum Size {
    Fixed,
    Fit,
    Fill,
}

impl Doc {
    fn new(msg: &Value) -> Doc {
        let changes: Vec<Value> = arr(&msg["nodeChanges"]).into_iter().filter(Value::is_object).collect();
        let mut props = HashMap::new();
        for c in &changes {
            if let Some(k) = key(&c["guid"]) {
                props.insert(k, c.clone());
            }
        }
        let mut kids: HashMap<String, Vec<(String, String)>> = HashMap::new();
        for c in &changes {
            let (Some(k), Some(parent)) = (key(&c["guid"]), key(&c["parentIndex"]["guid"])) else { continue };
            if props.contains_key(&parent) && parent != k {
                kids.entry(parent).or_default().push((c["parentIndex"]["position"].as_str().unwrap_or("").to_string(), k));
            }
        }
        let kids = kids.into_iter().map(|(p, mut v)| {
            v.sort_by(|a, b| a.0.cmp(&b.0));
            v.dedup_by(|a, b| a.1 == b.1);
            (p, v.into_iter().map(|x| x.1).collect())
        }).collect();
        let hex = |s: &str| (0..s.len() / 2).filter_map(|i| u8::from_str_radix(s.get(i * 2..i * 2 + 2)?, 16).ok()).collect();
        let blobs = arr(&msg["blobs"]).iter().map(|b| b["bytes"].as_str().map(hex).unwrap_or_default()).collect();
        let mut doc = Doc { props, kids, blobs, vars: HashMap::new(), modes: HashMap::new(), styles: HashMap::new(), budget: Cell::new(MAX_NODES) };
        for c in &changes {
            if c["type"] == "VARIABLE_SET" {
                let Some(mode) = key(&c["variableSetModes"][0]["id"]) else { continue };
                for k in [ref_key(&c["variableSetID"]).or_else(|| key(&c["guid"])), key(&c["guid"]), c["variableSetID"]["assetRef"]["key"].as_str().map(str::to_string), c["key"].as_str().map(str::to_string)].into_iter().flatten() {
                    doc.modes.insert(k, mode.clone());
                }
            }
        }
        for c in &changes {
            if c["type"] == "VARIABLE" && c["variableDataValues"]["entries"].is_array() {
                let k = c["key"].as_str().filter(|k| !k.is_empty()).map(str::to_string).or_else(|| key(&c["guid"]));
                if let Some(k) = k {
                    doc.vars.entry(k).or_insert((ref_key(&c["variableSetID"]), arr(&c["variableDataValues"]["entries"])));
                }
            }
        }
        for p in doc.props.values() {
            if let Some(k) = p["key"].as_str() {
                doc.styles.insert(k.to_string(), p.clone());
            }
            if let Some(k) = key(&p["guid"]) {
                doc.styles.insert(k, p.clone());
            }
        }
        doc
    }

    fn subtree(&self, k: &str, depth: usize) -> Option<N> {
        if depth > MAX_DEPTH {
            return None;
        }
        let kids = self.kids.get(k).map(|ks| ks.iter().filter_map(|c| self.subtree(c, depth + 1)).collect()).unwrap_or_default();
        Some(N { p: self.props.get(k)?.clone(), kids })
    }

    /// The first visible page's layers (a copy has one).
    fn page(&self) -> Vec<(Value, (f64, f64, f64, f64))> {
        let Some(root) = self.props.get("0:0") else { return Vec::new() };
        let Some(page) = self.kids.get("0:0").into_iter().flatten().find(|k| self.props[k.as_str()]["visible"] == true) else { return Vec::new() };
        let up = [root, &self.props[page.as_str()]];
        self.kids.get(page).into_iter().flatten().filter_map(|k| {
            let p = &self.props[k.as_str()];
            let g = |v: &Value| f(v).unwrap_or(0.0);
            let b = (g(&p["transform"]["m02"]), g(&p["transform"]["m12"]), g(&p["size"]["x"]), g(&p["size"]["y"]));
            Some((self.convert(self.subtree(k, 1)?, &up, false, 1)?, b))
        }).collect()
    }

    // -- styles, variables, overrides (Qon.applyStyles / resolve* / applyOverrides) --

    fn style(&self, id: &Value) -> Option<&Value> {
        self.styles.get(&ref_key(id)?)
    }

    fn apply_styles(&self, p: &mut Value, chain: &[&Value]) {
        if let Some(s) = self.style(&p["styleIdForFill"]).filter(|s| !s["fillPaints"].is_null()) {
            p["fillPaints"] = self.paints(&s["fillPaints"], chain);
        }
        if let Some(s) = self.style(&p["styleIdForStrokeFill"]).filter(|s| !s["fillPaints"].is_null()) {
            p["strokePaints"] = self.paints(&s["fillPaints"], chain);
        }
        if let Some(s) = self.style(&p["styleIdForEffect"]).filter(|s| !s["effects"].is_null()) {
            p["effects"] = s["effects"].clone();
        }
        if let Some(s) = self.style(&p["styleIdForText"]).cloned() {
            for k in ["fontName", "textCase", "fontSize", "textDecoration", "letterSpacing", "lineHeight", "paragraphSpacing"] {
                if !s[k].is_null() {
                    p[k] = s[k].clone();
                }
            }
        }
    }

    /// Paint colors bound to variables, resolved for the node at the end of `chain`.
    fn paints(&self, paints: &Value, chain: &[&Value]) -> Value {
        Value::Array(arr(paints).into_iter().map(|mut r| {
            if !r.is_object() {
                return r;
            }
            if r["stops"].is_array() {
                let vars = arr(&r["stopsVar"]);
                for (i, s) in r["stops"].as_array_mut().into_iter().flatten().enumerate().filter(|(_, s)| s.is_object()) {
                    let v = if s["colorVar"].is_null() { vars.get(i).map_or(Value::Null, |v| v["colorVar"].clone()) } else { s["colorVar"].clone() };
                    if let Some(c) = self.color_var(&v, chain) {
                        s["color"] = c;
                    }
                }
            } else if let Some(c) = self.color_var(&r["colorVar"], chain) {
                r["color"] = c;
                r["opacity"] = json!(1);
            }
            r
        }).collect())
    }

    fn color_var(&self, v: &Value, chain: &[&Value]) -> Option<Value> {
        (v["resolvedDataType"] == "COLOR").then(|| self.var(v, chain, 0)).flatten().map(|x| x["colorValue"].clone()).filter(Value::is_object)
    }

    /// A variable's value for the node at the end of `chain` (its mode, else the set's first).
    fn var(&self, n: &Value, chain: &[&Value], depth: usize) -> Option<Value> {
        if !n["value"].is_object() || depth > MAX_DEPTH {
            return None;
        }
        if n["dataType"] != "ALIAS" {
            return Some(n["value"].clone());
        }
        let (set, entries) = self.vars.get(&ref_key(&n["value"]["alias"])?)?;
        if entries.len() > 1 {
            if let Some(mode) = set.as_ref().and_then(|s| self.mode(s, chain)) {
                if let Some(e) = entries.iter().find(|e| key(&e["modeID"]).as_deref() == Some(mode.as_str())) {
                    return self.var(&e["variableData"], chain, depth + 1);
                }
            }
        }
        self.var(&entries.first()?["variableData"], chain, depth + 1)
    }

    fn mode(&self, set: &str, chain: &[&Value]) -> Option<String> {
        for p in chain.iter().rev() {
            for s in arr(&p["variableModeBySetMap"]["entries"]) {
                if ref_key(&s["variableSetID"]).as_deref() == Some(set) && !s["variableModeID"].is_null() {
                    return key(&s["variableModeID"]);
                }
            }
        }
        self.modes.get(set).cloned()
    }

    /// Follows component-property references up the tree to the assigned value.
    fn prop_ref(&self, chain: &[&Value], n: &Value, depth: usize) -> Value {
        let id = &n["value"]["propRefValue"]["defId"];
        if id.is_null() || depth > MAX_DEPTH {
            return n.clone();
        }
        for (i, p) in chain.iter().enumerate().rev() {
            if let Some(s) = p["componentPropAssignments"].as_array().and_then(|a| a.iter().find(|s| same(id, &s["defID"]))) {
                return self.prop_ref(&chain[..=i], &s["varValue"], depth + 1);
            }
        }
        n.clone()
    }

    /// Node properties bound to variables / component properties (`resolveNodeVariables`).
    fn bind(&self, p: &mut Value, up: &[&Value], entries: &Value) {
        let Some(entries) = entries.as_array() else { return };
        for r in entries {
            let Some(field) = r["variableField"].as_str() else { continue };
            let own = scope(p);
            let chain: Vec<&Value> = up.iter().copied().chain([&own]).collect();
            let Some(v) = self.var(&self.prop_ref(&chain, &r["variableData"], 0), &chain, 0) else { continue };
            let num = f(&v["floatValue"]).map(|x| json!(x));
            let set = |p: &mut Value, keys: &[&str]| {
                if let Some(x) = &num {
                    for k in keys {
                        p[*k] = x.clone();
                    }
                }
            };
            match field {
                "TEXT_DATA" if v["textDataValue"].is_object() => p["textData"] = v["textDataValue"].clone(),
                "TEXT_DATA" if v["textValue"].is_string() => p["textData"] = json!({"characters": v["textValue"]}),
                "VISIBLE" if v["boolValue"].is_boolean() => p["visible"] = v["boolValue"].clone(),
                "OVERRIDDEN_SYMBOL_ID" if real(&v["symbolIdValue"]["guid"]) => p["overriddenSymbolID"] = v["symbolIdValue"]["guid"].clone(),
                "OPACITY" => set(p, &["opacity"]),
                "CORNER_RADIUS" => set(p, &["cornerRadius", "rectangleTopLeftCornerRadius", "rectangleTopRightCornerRadius", "rectangleBottomLeftCornerRadius", "rectangleBottomRightCornerRadius"]),
                "RECTANGLE_TOP_LEFT_CORNER_RADIUS" => set(p, &["rectangleTopLeftCornerRadius"]),
                "RECTANGLE_TOP_RIGHT_CORNER_RADIUS" => set(p, &["rectangleTopRightCornerRadius"]),
                "RECTANGLE_BOTTOM_LEFT_CORNER_RADIUS" => set(p, &["rectangleBottomLeftCornerRadius"]),
                "RECTANGLE_BOTTOM_RIGHT_CORNER_RADIUS" => set(p, &["rectangleBottomRightCornerRadius"]),
                "STROKE_WEIGHT" => set(p, &["strokeWeight"]),
                "STACK_SPACING" => set(p, &["stackSpacing"]),
                "STACK_PADDING_LEFT" => set(p, &["stackHorizontalPadding"]),
                "STACK_PADDING_TOP" => set(p, &["stackVerticalPadding"]),
                "STACK_PADDING_RIGHT" => set(p, &["stackPaddingRight"]),
                "STACK_PADDING_BOTTOM" => set(p, &["stackPaddingBottom"]),
                "BORDER_TOP_WEIGHT" => set(p, &["borderTopWeight"]),
                "BORDER_BOTTOM_WEIGHT" => set(p, &["borderBottomWeight"]),
                "BORDER_LEFT_WEIGHT" => set(p, &["borderLeftWeight"]),
                "BORDER_RIGHT_WEIGHT" => set(p, &["borderRightWeight"]),
                "STACK_COUNTER_SPACING" => set(p, &["stackCounterSpacing"]),
                "FONT_SIZE" => set(p, &["fontSize"]),
                "WIDTH" if num.is_some() => p["size"] = json!({"x": num, "y": f(&p["size"]["y"]).unwrap_or(0.0)}),
                "HEIGHT" if num.is_some() => p["size"] = json!({"x": f(&p["size"]["x"]).unwrap_or(0.0), "y": num}),
                _ => {}
            }
        }
    }

    /// An instance swapped to another component keeps its own placement and assignments.
    fn swap(&self, p: &mut Value, symbol: &Value) {
        let Some(src) = key(symbol).and_then(|k| self.props.get(&k)) else { return };
        let mut n = src.clone();
        n["type"] = json!("INSTANCE");
        for k in ["guid", "parentIndex", "overrideKey"] {
            n[k] = p[k].clone();
        }
        for k in ["size", "transform", "visible", "parameterConsumptionMap", "componentPropAssignments", "symbolData", "derivedSymbolData", "variableModeBySetMap"] {
            if !p[k].is_null() {
                n[k] = p[k].clone();
            }
        }
        *p = n;
    }

    /// Instance overrides addressed by guid path (`applyOverrides`): this node's own ones apply
    /// here, deeper ones travel down (or into a nested instance's own override list).
    fn overrides(&self, e: &mut N, up: &[&Value], ovr: &[Value], derived: &[Value], depth: usize) {
        if depth > MAX_DEPTH {
            return;
        }
        let id = if e.p["overrideKey"].is_object() { e.p["overrideKey"].clone() } else { e.p["guid"].clone() };
        let split = |list: &[Value]| {
            let (mut here, mut deeper) = (Vec::new(), Vec::new());
            for o in list.iter().filter(|o| o.is_object()) {
                let g = arr(&o["guidPath"]["guids"]);
                if g.first().is_some_and(|g0| same(g0, &id)) {
                    if g.len() > 1 {
                        let mut o = o.clone();
                        o["guidPath"] = json!({"guids": g[1..]});
                        deeper.push(o);
                    } else {
                        here.push(o.clone());
                    }
                }
            }
            (here, deeper)
        };
        let (here, deeper) = split(ovr);
        let (dhere, ddeeper) = split(derived);
        let mut name = None;
        if e.p["type"] == "INSTANCE" {
            if let Some(d) = here.iter().find(|d| real(&d["overriddenSymbolID"])) {
                name = Some(e.p["name"].clone());
                self.swap(&mut e.p, &d["overriddenSymbolID"]);
            }
        }
        for d in here {
            let own = scope(&e.p);
            let chain: Vec<&Value> = up.iter().copied().chain([&own]).collect();
            let mut d = d;
            self.apply_styles(&mut d, &chain);
            let prev = e.p.clone();
            if let (Some(p), Some(o)) = (e.p.as_object_mut(), d.as_object()) {
                for (k, v) in o {
                    p.insert(k.clone(), v.clone());
                }
                p.remove("guidPath");
            }
            if !prev["variableModeBySetMap"].is_null() && e.p["variableModeBySetMap"].is_null() {
                e.p["variableModeBySetMap"] = prev["variableModeBySetMap"].clone();
            }
            if let (Some(new), Some(old)) = (d["componentPropAssignments"].as_array(), prev["componentPropAssignments"].as_array()) {
                let kept: Vec<Value> = old.iter().filter(|o| !new.iter().any(|n| same(&n["defID"], &o["defID"]))).cloned().collect();
                if let Some(a) = e.p["componentPropAssignments"].as_array_mut() {
                    a.extend(kept);
                }
            }
            for (k, was) in [("fillPaints", &prev["fillPaints"]), ("strokePaints", &prev["strokePaints"])] {
                if let Some(now) = e.p[k].as_array_mut() {
                    for (i, p) in now.iter_mut().enumerate().filter(|(_, p)| p.is_object()) {
                        if p["colorVar"].is_null() && !was[i]["colorVar"].is_null() {
                            p["colorVar"] = was[i]["colorVar"].clone();
                        }
                    }
                }
            }
            let own = scope(&e.p);
            let chain: Vec<&Value> = up.iter().copied().chain([&own]).collect();
            for (k, style) in [("fillPaints", &prev["styleIdForFill"]), ("strokePaints", &prev["styleIdForStrokeFill"])] {
                if !d[k].is_null() {
                    if let Some(s) = self.style(style).filter(|s| !s["fillPaints"].is_null()) {
                        e.p[k] = self.paints(&s["fillPaints"], &chain);
                    }
                }
            }
            for k in ["fillPaints", "strokePaints"] {
                if !e.p[k].is_null() {
                    e.p[k] = self.paints(&e.p[k], &chain);
                }
            }
            if real(&d["overriddenSymbolID"]) {
                if !e.p["symbolData"].is_object() {
                    e.p["symbolData"] = json!({});
                }
                e.p["symbolData"]["symbolID"] = d["overriddenSymbolID"].clone();
            }
            let entries = if d["variableConsumptionMap"]["entries"].is_array() { &d["variableConsumptionMap"]["entries"] } else { &d["parameterConsumptionMap"]["entries"] };
            self.bind(&mut e.p, up, entries);
        }
        if let Some(n) = name {
            e.p["name"] = n;
        }
        for d in dhere {
            for (k, v) in d.as_object().into_iter().flatten() {
                if k != "guidPath" && !v.is_null() {
                    e.p[k.as_str()] = v.clone();
                }
            }
            if let Some(p) = e.p.as_object_mut() {
                for k in ["styleIdForFill", "styleIdForStrokeFill", "styleIdForEffect", "styleIdForText"] {
                    p.remove(k);
                }
            }
        }
        if e.p["type"] == "INSTANCE" {
            let list = |p: &mut Value, k: &str, extra: Vec<Value>| {
                if !p[k].is_array() {
                    p[k] = json!([]);
                }
                p[k].as_array_mut().unwrap().extend(extra);
            };
            if !e.p["symbolData"].is_object() {
                e.p["symbolData"] = json!({});
            }
            list(&mut e.p["symbolData"], "symbolOverrides", deeper);
            list(&mut e.p, "derivedSymbolData", ddeeper);
        } else {
            let ovr: Vec<Value> = ovr.iter().cloned().chain(deeper).collect();
            let derived: Vec<Value> = derived.iter().cloned().chain(ddeeper).collect();
            let N { p, kids } = e;
            let chain: Vec<&Value> = up.iter().copied().chain([&*p]).collect();
            for k in kids {
                self.overrides(k, &chain, &ovr, &derived, depth + 1);
            }
        }
    }

    // -- conversion (Qon.convertNode) --

    /// One node and its children as .pen; `flow` = placed by its parent's auto layout.
    fn convert(&self, mut e: N, up: &[&Value], parent_flow: bool, depth: usize) -> Option<Value> {
        if depth > MAX_DEPTH || self.budget.get() == 0 {
            return None;
        }
        self.budget.set(self.budget.get() - 1);
        if e.p["type"] == "INSTANCE" {
            let swapped = real(&e.p["overriddenSymbolID"]);
            let entries = e.p["parameterConsumptionMap"]["entries"].clone();
            self.bind(&mut e.p, up, &entries);
            for a in arr(&e.p["componentPropRefs"]) {
                if truthy(&a["defID"]) && truthy(&a["componentPropNodeField"]) {
                    let entry = json!([{"variableField": a["componentPropNodeField"], "variableData": {"value": {"propRefValue": {"defId": a["defID"]}}}}]);
                    self.bind(&mut e.p, up, &entry);
                }
            }
            let symbol = if real(&e.p["overriddenSymbolID"]) { e.p["overriddenSymbolID"].clone() } else { e.p["symbolData"]["symbolID"].clone() };
            if let Some(mut l) = key(&symbol).and_then(|k| self.subtree(&k, depth + 1)) {
                let ovr = arr(&e.p["symbolData"]["symbolOverrides"]);
                let derived = arr(&e.p["derivedSymbolData"]);
                let chain: Vec<&Value> = up.iter().copied().chain([&e.p]).collect();
                for k in &mut l.kids {
                    self.overrides(k, &chain, &ovr, &derived, depth + 1);
                }
                e.kids = l.kids;
                if !swapped && real(&e.p["overriddenSymbolID"]) {
                    let keep = ["name", "fillPaints", "strokePaints"].map(|k| (k, e.p[k].clone()));
                    let to = e.p["overriddenSymbolID"].clone();
                    self.swap(&mut e.p, &to);
                    for (k, v) in keep {
                        e.p[k] = v;
                    }
                }
                e.p["type"] = json!("FRAME");
            }
        }
        let entries = e.p["parameterConsumptionMap"]["entries"].clone();
        self.bind(&mut e.p, up, &entries);
        let own = scope(&e.p);
        let chain: Vec<&Value> = up.iter().copied().chain([&own]).collect();
        self.apply_styles(&mut e.p, &chain);
        let p = &e.p;
        let ty = match p["type"].as_str().unwrap_or("") {
            "SYMBOL" | "SECTION" | "FRAME" if truthy(&p["resizeToFit"]) => "group",
            "SYMBOL" | "SECTION" | "FRAME" => "frame",
            "BOOLEAN_OPERATION" => "group",
            "VECTOR" => "path",
            "TEXT" => "text",
            "LINE" => "line",
            "ELLIPSE" => "ellipse",
            "REGULAR_POLYGON" => "polygon",
            "RECTANGLE" | "ROUNDED_RECTANGLE" => "rectangle",
            _ => return None,
        };
        let layout = match p["stackMode"].as_str() {
            Some("HORIZONTAL") => Some("horizontal"),
            Some("VERTICAL") => Some("vertical"),
            _ => None,
        };
        let absolute = p["stackPositioning"] == "ABSOLUTE";
        let flow = parent_flow && !absolute;
        let parent_dir = up.last().and_then(|q| q["stackMode"].as_str());
        let (hs, vs) = (sizing(p, parent_dir, true), sizing(p, parent_dir, false));
        let (w, h) = (f(&p["size"]["x"]).unwrap_or(0.0), f(&p["size"]["y"]).unwrap_or(0.0));

        let mut o = Map::new();
        o.insert("type".into(), json!(ty));
        if let Some(n) = p["name"].as_str().filter(|n| !n.is_empty()) {
            o.insert("name".into(), json!(n));
        }
        if absolute {
            o.insert("layoutPosition".into(), json!("absolute"));
        }
        if p["visible"] == false {
            o.insert("enabled".into(), json!(false));
        }
        if let Some(op) = f(&p["opacity"]).filter(|v| *v != 1.0) {
            o.insert("opacity".into(), json!(op));
        }
        let t = &p["transform"];
        if t.is_object() {
            let m = |k: &str| f(&t[k]).unwrap_or(0.0);
            let (m00, m01, m02, m10, m11, m12) = (m("m00"), m("m01"), m("m02"), m("m10"), m("m11"), m("m12"));
            // Rotation from the first column, flips where the matrix disagrees.
            let mirrored = m00 * m11 - m10 * m01 < 0.0;
            let (sx, sy) = ((m00 * m00 + m10 * m10).sqrt(), (m01 * m01 + m11 * m11).sqrt());
            let rot = if mirrored { (-m10).atan2(-m00) } else { m10.atan2(m00) };
            let (c, s) = (rot.cos(), rot.sin());
            if !flow {
                o.insert("x".into(), json!(m02));
                o.insert("y".into(), json!(m12));
            }
            if rot != 0.0 {
                o.insert("rotation".into(), json!(-rot.to_degrees()));
            }
            if (m00 - sx * c).abs() > 1e-6 || (m10 - sx * s).abs() > 1e-6 {
                o.insert("flipX".into(), json!(true));
            }
            if (m01 + sy * s).abs() > 1e-6 || (m11 - sy * c).abs() > 1e-6 {
                o.insert("flipY".into(), json!(true));
            }
        }

        // Children first: a fit_content frame needs to know whether it hugs anything.
        let kids: Vec<Value> = {
            let chain: Vec<&Value> = up.iter().copied().chain([p]).collect();
            let child_flow = ty == "frame" && layout.is_some();
            e.kids.iter().filter_map(|k| self.convert(k.clone(), &chain, child_flow, depth + 1)).collect()
        };
        let hugs = |axis: &str| layout.is_some() && kids.iter().any(|k| k["layoutPosition"] != "absolute" && !k[axis].as_str().is_some_and(|s| s.starts_with("fill_container")));
        let dim = |s: Size, v: f64, axis: &str, default_fit: bool| -> Option<Value> {
            match s {
                Size::Fixed => Some(json!(v)),
                Size::Fit if hugs(axis) => (!default_fit).then(|| json!("fit_content")),
                Size::Fit => Some(json!(format!("fit_content({v})"))),
                Size::Fill if flow => Some(json!("fill_container")),
                Size::Fill => Some(json!(format!("fill_container({v})"))),
            }
        };
        let mask = truthy(&p["mask"]);
        let fills = if mask { Vec::new() } else { paints(&p["fillPaints"]) };
        // Text keeps no stroke (only frames, vectors and shapes are stroked).
        let strokes = if mask || ty == "text" { Vec::new() } else { paints(&p["strokePaints"]) };
        let one = |v: Vec<Value>| if v.len() == 1 { v.into_iter().next().unwrap() } else { Value::Array(v) };

        match ty {
            "frame" => {
                if p["frameMaskDisabled"] != true {
                    o.insert("clip".into(), json!(true));
                }
                for (axis, s, v) in [("width", hs, w), ("height", vs, h)] {
                    if let Some(d) = dim(s, v, axis, true) {
                        o.insert(axis.into(), d);
                    }
                }
            }
            "group" => {
                for (axis, s) in [("width", hs), ("height", vs)] {
                    if s == Size::Fill && flow {
                        o.insert(axis.into(), json!("fill_container"));
                    }
                }
            }
            "text" => {}
            _ => {
                for (axis, s, v) in [("width", hs, w), ("height", vs, h)] {
                    if let Some(d) = dim(s, v, axis, false) {
                        o.insert(axis.into(), d);
                    }
                }
            }
        }
        match ty {
            "rectangle" | "frame" => {
                if let Some(r) = radius(p) {
                    o.insert("cornerRadius".into(), r);
                }
            }
            "ellipse" => {
                let a = &p["arcData"];
                if let Some(r) = f(&a["innerRadius"]).filter(|r| *r != 0.0) {
                    o.insert("innerRadius".into(), json!(r));
                }
                let start = f(&a["startingAngle"]).unwrap_or(0.0).to_degrees();
                if start != 0.0 {
                    o.insert("startAngle".into(), json!(-start));
                }
                if let Some(end) = f(&a["endingAngle"]) {
                    let sweep = start - end.to_degrees();
                    if sweep.abs() != 360.0 {
                        o.insert("sweepAngle".into(), json!(sweep));
                    }
                }
            }
            "polygon" => {
                if let Some(c) = f(&p["count"]) {
                    o.insert("polygonCount".into(), json!(c));
                }
                if let Some(r) = f(&p["cornerRadius"]).filter(|r| *r != 0.0) {
                    o.insert("cornerRadius".into(), json!(r));
                }
            }
            "path" => {
                if let Some((d, rule)) = self.vector(p) {
                    o.insert("geometry".into(), json!(d));
                    if rule == "evenodd" {
                        o.insert("fillRule".into(), json!(rule));
                    }
                }
            }
            "text" => text(p, &mut o, hs, vs, w, h, flow),
            _ => {}
        }
        if ty != "group" && !fills.is_empty() {
            o.insert("fill".into(), one(fills));
        }
        if ty != "group" && !strokes.is_empty() {
            o.insert("stroke".into(), one(strokes));
            let wt = |k: &str| f(&p[k]).unwrap_or(0.0);
            let widths = if truthy(&p["borderStrokeWeightsIndependent"]) {
                Some([wt("borderTopWeight"), wt("borderRightWeight"), wt("borderBottomWeight"), wt("borderLeftWeight")])
            } else {
                f(&p["strokeWeight"]).filter(|v| *v != 0.0).map(|v| [v; 4])
            };
            if let Some(ws) = widths {
                let v = if ws.iter().all(|x| *x == ws[0]) {
                    json!(ws[0])
                } else {
                    let mut m = Map::new();
                    for (k, x) in ["top", "right", "bottom", "left"].iter().zip(ws) {
                        if x != 0.0 {
                            m.insert(k.to_string(), json!(x));
                        }
                    }
                    Value::Object(m)
                };
                o.insert("strokeWidth".into(), v);
            }
            match p["strokeAlign"].as_str() {
                Some("INSIDE") => _ = o.insert("strokeAlignment".into(), json!("inner")),
                Some("OUTSIDE") => _ = o.insert("strokeAlignment".into(), json!("outer")),
                _ => {}
            }
            match p["strokeJoin"].as_str() {
                Some("BEVEL") => _ = o.insert("strokeLinejoin".into(), json!("bevel")),
                Some("ROUND") => _ = o.insert("strokeLinejoin".into(), json!("round")),
                _ => {}
            }
            match p["strokeCap"].as_str() {
                Some("ROUND") => _ = o.insert("strokeLinecap".into(), json!("round")),
                Some("SQUARE") => _ = o.insert("strokeLinecap".into(), json!("square")),
                _ => {}
            }
        }
        let fx = effects(&p["effects"]);
        if !fx.is_empty() {
            o.insert("effect".into(), one(fx));
        }
        if ty == "frame" {
            match layout {
                None => _ = o.insert("layout".into(), json!("none")),
                Some(l) => {
                    if l == "vertical" {
                        o.insert("layout".into(), json!(l));
                    }
                    if let Some(g) = f(&p["stackSpacing"]).filter(|g| *g != 0.0) {
                        o.insert("gap".into(), json!(g));
                    }
                    let pad = ["stackVerticalPadding", "stackPaddingRight", "stackPaddingBottom", "stackHorizontalPadding"].map(|k| f(&p[k]).unwrap_or(0.0));
                    if pad.iter().any(|v| *v != 0.0) {
                        o.insert("padding".into(), if pad.iter().all(|v| *v == pad[0]) {
                            json!(pad[0])
                        } else if pad[0] == pad[2] && pad[1] == pad[3] {
                            json!([pad[0], pad[1]])
                        } else {
                            json!(pad)
                        });
                    }
                    let justify = match p["stackPrimaryAlignItems"].as_str() {
                        Some("CENTER") => Some("center"),
                        Some("MAX") => Some("end"),
                        Some("SPACE_BETWEEN" | "SPACE_EVENLY") => Some("space_between"),
                        _ => None,
                    };
                    if let Some(j) = justify {
                        o.insert("justifyContent".into(), json!(j));
                    }
                    let align = match p["stackCounterAlignItems"].as_str() {
                        Some("CENTER") => Some("center"),
                        Some("MAX") => Some("end"),
                        _ => None,
                    };
                    if let Some(a) = align {
                        o.insert("alignItems".into(), json!(a));
                    }
                    if truthy(&p["bordersTakeSpace"]) {
                        o.insert("layoutIncludeStroke".into(), json!(true));
                    }
                }
            }
        }
        if matches!(ty, "frame" | "group") && !kids.is_empty() {
            o.insert("children".into(), Value::Array(kids));
        }
        Some(Value::Object(o))
    }

    /// Vector network blob → SVG path data and fill rule (`parseVectorNetworkBlob` + `dSe`/`bSe`).
    fn vector(&self, p: &Value) -> Option<(String, &'static str)> {
        let b = self.blobs.get(p["vectorData"]["vectorNetworkBlob"].as_u64()? as usize)?;
        let u32_at = |i: usize| b.get(i..i + 4).map(|s| u32::from_le_bytes([s[0], s[1], s[2], s[3]]));
        let f32_at = |i: usize| u32_at(i).map(|v| f32::from_bits(v) as f64);
        let (nv, ns, nr) = (u32_at(0)? as usize, u32_at(4)? as usize, u32_at(8)? as usize);
        let mut i = 12;
        let mut verts = Vec::new();
        for _ in 0..nv {
            verts.push((f32_at(i + 4)?, f32_at(i + 8)?));
            i += 12;
        }
        let mut segs = Vec::new();
        for _ in 0..ns {
            let (a, z) = (u32_at(i + 4)? as usize, u32_at(i + 16)? as usize);
            if a >= nv || z >= nv {
                return None;
            }
            segs.push(Seg { a, ad: (f32_at(i + 8)?, f32_at(i + 12)?), z, zd: (f32_at(i + 20)?, f32_at(i + 24)?) });
            i += 28;
        }
        let mut regions = Vec::new();
        for _ in 0..nr {
            let rule = if u32_at(i)? & 1 == 1 { "nonzero" } else { "evenodd" };
            let loops = u32_at(i + 4)? as usize;
            i += 8;
            let mut ls = Vec::new();
            for _ in 0..loops {
                let n = u32_at(i)? as usize;
                i += 4;
                let mut l = Vec::new();
                for _ in 0..n {
                    let s = u32_at(i)? as usize;
                    if s >= ns {
                        return None;
                    }
                    l.push(segs[s]);
                    i += 4;
                }
                ls.push(l);
            }
            regions.push((rule, ls));
        }
        let mut d = Vec::new();
        let mut rule = "nonzero";
        if regions.is_empty() {
            trace(&mut d, &verts, chain(segs));
        }
        for (r, loops) in regions {
            for l in loops.into_iter().filter(|l| !l.is_empty()) {
                trace(&mut d, &verts, chain(l));
            }
            rule = r;
        }
        Some((d.join(" "), rule))
    }
}

#[derive(Clone, Copy)]
struct Seg {
    a: usize,
    ad: (f64, f64),
    z: usize,
    zd: (f64, f64),
}

/// Orients segments head to tail (`bSe`).
fn chain(mut s: Vec<Seg>) -> Vec<Seg> {
    let flip = |s: &mut Seg| *s = Seg { a: s.z, ad: s.zd, z: s.a, zd: s.ad };
    if s.len() >= 2 && s[0].z != s[1].a && s[0].z != s[1].z {
        flip(&mut s[0]);
    }
    for i in 1..s.len() {
        if s[i - 1].z != s[i].a {
            flip(&mut s[i]);
        }
    }
    s
}

/// Segments to path commands, closing when a run returns to its start (`dSe`).
fn trace(d: &mut Vec<String>, v: &[(f64, f64)], segs: Vec<Seg>) {
    let r = |x: f64| (x * 1e5).round() / 1e5;
    let (mut start, mut last) = (None, None);
    for s in segs {
        let (a, z) = (v[s.a], v[s.z]);
        if last != Some(s.a) {
            d.push(format!("M{} {}", r(a.0), r(a.1)));
            start = Some(s.a);
        }
        if s.ad == (0.0, 0.0) && s.zd == (0.0, 0.0) {
            d.push(format!("L{} {}", r(z.0), r(z.1)));
        } else {
            d.push(format!("C{} {} {} {} {} {}", r(a.0 + s.ad.0), r(a.1 + s.ad.1), r(z.0 + s.zd.0), r(z.1 + s.zd.1), r(z.0), r(z.1)));
        }
        last = Some(s.z);
        if start == Some(s.z) {
            d.push("Z".into());
            (start, last) = (None, None);
        }
    }
}

/// Fill when stretched/grown in the parent's auto layout, hug when the node's own
/// auto layout sizes that axis, else fixed. `parent` = the parent's stack direction.
fn sizing(p: &Value, parent: Option<&str>, horizontal: bool) -> Size {
    let (along, across) = if horizontal { ("HORIZONTAL", "VERTICAL") } else { ("VERTICAL", "HORIZONTAL") };
    let own = p["stackMode"].as_str();
    let grow = truthy(&p["stackChildPrimaryGrow"]);
    if (parent == Some(across) && p["stackChildAlignSelf"] == "STRETCH") || (parent == Some(along) && grow) {
        Size::Fill
    } else if own == Some(across) && p["stackCounterSizing"] == "RESIZE_TO_FIT_WITH_IMPLICIT_SIZE" {
        Size::Fit
    } else if own == Some(along) && p["stackPrimarySizing"] == "FIXED" {
        Size::Fixed
    } else if own == Some(along) {
        Size::Fit
    } else {
        Size::Fixed
    }
}

/// [tl, tr, br, bl]: per-corner radii when any is set, else the uniform one; one number when equal.
fn radius(p: &Value) -> Option<Value> {
    let c = ["rectangleTopLeftCornerRadius", "rectangleTopRightCornerRadius", "rectangleBottomRightCornerRadius", "rectangleBottomLeftCornerRadius"].map(|k| f(&p[k]).unwrap_or(0.0));
    let r = if c.iter().any(|v| *v != 0.0) { c } else { [f(&p["cornerRadius"]).unwrap_or(0.0); 4] };
    if r.iter().all(|v| *v == 0.0) {
        None
    } else if r.iter().all(|v| *v == r[0]) {
        Some(json!(r[0]))
    } else {
        Some(json!(r))
    }
}

fn text(p: &Value, o: &mut Map<String, Value>, hs: Size, vs: Size, w: f64, h: f64, flow: bool) {
    let size = |s: Size, v: f64| match s {
        Size::Fill if flow => json!("fill_container"),
        Size::Fill => json!(format!("fill_container({v})")),
        Size::Fit => json!(format!("fit_content({v})")),
        Size::Fixed => json!(v),
    };
    match p["textAutoResize"].as_str().unwrap_or("NONE") {
        "WIDTH_AND_HEIGHT" => {}
        "HEIGHT" => {
            o.insert("textGrowth".into(), json!("fixed-width"));
            o.insert("width".into(), size(hs, w));
        }
        _ => {
            o.insert("textGrowth".into(), json!("fixed-width-height"));
            o.insert("width".into(), size(hs, w));
            o.insert("height".into(), size(vs, h));
        }
    }
    if let Some(c) = p["textData"]["characters"].as_str().filter(|c| !c.is_empty()) {
        o.insert("content".into(), json!(c));
    }
    let fs = f(&p["fontSize"]);
    let lh = &p["lineHeight"];
    let line = match (lh["units"].as_str(), f(&lh["value"])) {
        (Some("RAW"), Some(v)) => v,
        (Some("PIXELS"), Some(v)) => v / fs.unwrap_or(1.0),
        _ => 0.0,
    };
    if line != 0.0 {
        o.insert("lineHeight".into(), json!(line));
    }
    match p["textAlignHorizontal"].as_str() {
        Some("CENTER") => _ = o.insert("textAlign".into(), json!("center")),
        Some("RIGHT") => _ = o.insert("textAlign".into(), json!("right")),
        Some("JUSTIFIED") => _ = o.insert("textAlign".into(), json!("justify")),
        _ => {}
    }
    match p["textAlignVertical"].as_str() {
        Some("CENTER") => _ = o.insert("textAlignVertical".into(), json!("middle")),
        Some("BOTTOM") => _ = o.insert("textAlignVertical".into(), json!("bottom")),
        _ => {}
    }
    o.insert("fontFamily".into(), json!(p["fontName"]["family"].as_str().unwrap_or("Inter")));
    o.insert("fontSize".into(), json!(fs.unwrap_or(14.0)));
    // Style name → weight, first match wins (so "SemiBold" is not "Bold").
    let style = p["fontName"]["style"].as_str().unwrap_or("");
    let weights = [("Thin", "100"), ("ExtraLight", "200"), ("Extra Light", "200"), ("Light", "300"), ("Regular", "normal"), ("SemiBold", "600"), ("Semi Bold", "600"),
                   ("ExtraBold", "800"), ("Extra Bold", "800"), ("ExtraBlack", "950"), ("Extra Black", "950"), ("Black", "900"), ("Bold", "700"), ("Medium", "500")];
    let weight = if p["fontName"]["family"].is_string() { weights.iter().find(|(k, _)| style.contains(k)).map_or("normal", |w| w.1) } else { "normal" };
    o.insert("fontWeight".into(), json!(weight));
    let ls = &p["letterSpacing"];
    let spacing = match (ls["units"].as_str(), f(&ls["value"])) {
        (Some("RAW" | "PIXELS"), Some(v)) => v,
        (Some("PERCENT"), Some(v)) => v / 100.0 * fs.unwrap_or(1.0),
        _ => 0.0,
    };
    if spacing != 0.0 {
        o.insert("letterSpacing".into(), json!(spacing));
    }
    if style.contains("Italic") {
        o.insert("fontStyle".into(), json!("italic"));
    }
}

/// "#rrggbbaa" from Figma's 0..1 floats, alpha times `opacity` (`pSe`).
fn hex(c: &Value, opacity: f64) -> String {
    let b = |k: &str, m: f64| ((f(&c[k]).unwrap_or(0.0) * m * 255.0).round().clamp(0.0, 255.0)) as u8;
    format!("#{:02x}{:02x}{:02x}{:02x}", b("r", 1.0), b("g", 1.0), b("b", 1.0), b("a", opacity))
}

/// Shadow colors drop an opaque alpha (`Qs`).
fn hex_short(c: &Value) -> String {
    let h = hex(c, 1.0);
    if h.ends_with("ff") { h[..7].to_string() } else { h }
}

fn blend(m: &Value) -> Option<&'static str> {
    Some(match m.as_str()? {
        "DARKEN" => "darken",
        "MULTIPLY" => "multiply",
        "LINEAR_BURN" => "linearBurn",
        "COLOR_BURN" => "colorBurn",
        "LIGHTEN" => "light",
        "SCREEN" => "screen",
        "LINEAR_DODGE" => "linearDodge",
        "COLOR_DODGE" => "colorDodge",
        "OVERLAY" => "overlay",
        "SOFT_LIGHT" => "softLight",
        "HARD_LIGHT" => "hardLight",
        "DIFFERENCE" => "difference",
        "EXCLUSION" => "exclusion",
        "HUE" => "hue",
        "SATURATION" => "saturation",
        "COLOR" => "color",
        "LUMINOSITY" => "luminosity",
        _ => return None,
    })
}

/// Figma paints → .pen fills. Image bytes are not on the
/// clipboard, so image fills keep their mode but have no url (a placeholder).
fn paints(v: &Value) -> Vec<Value> {
    let mut out = Vec::new();
    for r in arr(v) {
        let enabled = r["visible"] == true;
        let opacity = f(&r["opacity"]).unwrap_or(1.0);
        let bm = blend(&r["blendMode"]);
        let mut o = Map::new();
        match r["type"].as_str() {
            Some("SOLID") if r["color"].is_object() => {
                let c = hex(&r["color"], opacity);
                if enabled && bm.is_none() {
                    out.push(json!(c));
                    continue;
                }
                o.insert("type".into(), json!("color"));
                o.insert("color".into(), json!(c));
            }
            Some("IMAGE") => {
                let mode = match r["imageScaleMode"].as_str() {
                    Some("FIT") => "fit",
                    Some("STRETCH") => "stretch",
                    _ => "fill",
                };
                o.insert("type".into(), json!("image"));
                o.insert("mode".into(), json!(mode));
                if opacity != 1.0 {
                    o.insert("opacity".into(), json!(opacity));
                }
            }
            Some(kind @ ("GRADIENT_LINEAR" | "GRADIENT_RADIAL" | "GRADIENT_ANGULAR")) => {
                let Some(g) = gradient(kind, &r) else { continue };
                o = g;
                if opacity != 1.0 {
                    o.insert("opacity".into(), json!(opacity));
                }
            }
            _ => continue,
        }
        if !enabled {
            o.insert("enabled".into(), json!(false));
        }
        if let Some(b) = bm {
            o.insert("blendMode".into(), json!(b));
        }
        out.push(Value::Object(o));
    }
    out
}

/// Figma's gradient transform maps the node's unit box to gradient space; its inverse gives the
/// handles, from which the .pen center, rotation and size are derived.
fn gradient(kind: &str, r: &Value) -> Option<Map<String, Value>> {
    let t = &r["transform"];
    let m = |k: &str| f(&t[k]);
    let (a, b, c, d, e, g) = (m("m00")?, m("m10")?, m("m01")?, m("m11")?, m("m02")?, m("m12")?);
    let det = a * d - b * c;
    if det == 0.0 {
        return None;
    }
    let inv = |x: f64, y: f64| ((d * (x - e) - c * (y - g)) / det, (a * (y - g) - b * (x - e)) / det);
    let mut stops: Vec<(f64, String)> = arr(&r["stops"]).iter().map(|s| (f(&s["position"]).unwrap_or(0.0), hex(&s["color"], 1.0))).collect();
    if stops.is_empty() {
        return None;
    }
    let dist = |p: (f64, f64), q: (f64, f64)| ((q.0 - p.0).powi(2) + (q.1 - p.1).powi(2)).sqrt();
    let (ty, center, rot, size): (&str, (f64, f64), f64, (Option<f64>, f64)) = match kind {
        "GRADIENT_LINEAR" => {
            let (h, fp, gp) = (inv(0.0, 0.5), inv(1.0, 0.5), inv(0.0, 1.0));
            let (mut nx, mut ny) = (gp.0 - h.0, gp.1 - h.1);
            let len = (nx * nx + ny * ny).sqrt();
            if len != 0.0 {
                (nx, ny) = (nx / len, ny / len);
            }
            let (px, py) = (-ny, nx);
            let mut l = px * (fp.0 - h.0) + py * (fp.1 - h.1);
            let mut ang = py.atan2(px) + std::f64::consts::FRAC_PI_2;
            if l < 0.0 {
                l = -l;
                ang += std::f64::consts::PI;
            }
            ("linear", ((h.0 + fp.0) / 2.0, (h.1 + fp.1) / 2.0), ang, (None, l))
        }
        "GRADIENT_RADIAL" => {
            let (h, fp, gp) = (inv(0.5, 0.5), inv(1.0, 0.5), inv(0.5, 1.0));
            let v = [fp.0 - h.0, gp.0 - h.0, fp.1 - h.1, gp.1 - h.1];
            let (y, x, z) = (v[0] * v[0] + v[1] * v[1], v[0] * v[2] + v[1] * v[3], v[2] * v[2] + v[3] * v[3]);
            let (sum, det) = (y + z, y * z - x * x);
            let disc = (sum * sum - 4.0 * det).max(0.0).sqrt();
            let (big, small) = ((sum + disc) / 2.0, (sum - disc) / 2.0);
            ("radial", h, (big - y).atan2(x) + std::f64::consts::FRAC_PI_2, (Some(small.sqrt() * 2.0), big.sqrt() * 2.0))
        }
        _ => {
            let (h, fp, gp) = (inv(0.5, 0.5), inv(1.0, 0.5), inv(0.5, 1.0));
            let ang = (fp.1 - h.1).atan2(fp.0 - h.0) + std::f64::consts::FRAC_PI_2;
            let cross = (gp.0 - h.0) * (fp.1 - h.1) - (gp.1 - h.1) * (fp.0 - h.0);
            let sign = if cross > 0.0 { -1.0 } else { 1.0 };
            // A conic gradient needs stops at 0 and 1: both get the color where the seam is crossed.
            if stops.len() >= 2 {
                stops.sort_by(|a, b| a.0.total_cmp(&b.0));
                let (first, last) = (stops[0].clone(), stops[stops.len() - 1].clone());
                if first.0 != 0.0 || last.0 != 1.0 {
                    let span = first.0 + (1.0 - last.0);
                    let t = if span == 0.0 { 0.0 } else { first.0 / span };
                    let seam = mix(&first.1, &last.1, t);
                    if first.0 != 0.0 {
                        stops.insert(0, (0.0, seam.clone()));
                    }
                    if last.0 != 1.0 {
                        stops.push((1.0, seam));
                    }
                }
            }
            ("angular", h, ang, (Some(sign * dist(h, gp) * 2.0), dist(h, fp) * 2.0))
        }
    };
    let mut o = Map::new();
    o.insert("type".into(), json!("gradient"));
    o.insert("gradientType".into(), json!(ty));
    o.insert("rotation".into(), json!(-rot.to_degrees()));
    let mut s = Map::new();
    if let Some(w) = size.0 {
        s.insert("width".into(), json!(w));
    }
    s.insert("height".into(), json!(size.1));
    o.insert("size".into(), Value::Object(s));
    o.insert("colors".into(), Value::Array(stops.into_iter().map(|(p, c)| json!({"color": c, "position": p})).collect()));
    if center != (0.5, 0.5) {
        o.insert("center".into(), json!({"x": center.0, "y": center.1}));
    }
    Some(o)
}

/// Linear mix of two "#rrggbbaa" colors (the seam color of an angular gradient), from `last` to `first`.
fn mix(first: &str, last: &str, t: f64) -> String {
    let ch = |s: &str, i: usize| u8::from_str_radix(&s[1 + i * 2..3 + i * 2], 16).unwrap_or(0) as f64;
    let c: Vec<String> = (0..4).map(|i| format!("{:02x}", (ch(first, i) + t * (ch(last, i) - ch(first, i))).round() as u8)).collect();
    format!("#{}", c.concat())
}

/// Figma effects → .pen effects; blur radii scale by 0.875.
fn effects(v: &Value) -> Vec<Value> {
    let mut out = Vec::new();
    for r in arr(v) {
        let radius = f(&r["radius"]).unwrap_or(0.0) * 0.875;
        let mut o = Map::new();
        match r["type"].as_str() {
            Some("FOREGROUND_BLUR") => {
                o.insert("type".into(), json!("blur"));
                o.insert("radius".into(), json!(radius));
            }
            Some("BACKGROUND_BLUR") => {
                o.insert("type".into(), json!("background_blur"));
                o.insert("radius".into(), json!(radius));
            }
            Some(kind @ ("DROP_SHADOW" | "INNER_SHADOW")) => {
                o.insert("type".into(), json!("shadow"));
                o.insert("shadowType".into(), json!(if kind == "DROP_SHADOW" { "outer" } else { "inner" }));
                o.insert("color".into(), json!(hex_short(&r["color"])));
                let (x, y) = (f(&r["offset"]["x"]).unwrap_or(0.0), f(&r["offset"]["y"]).unwrap_or(0.0));
                if x != 0.0 || y != 0.0 {
                    o.insert("offset".into(), json!({"x": x, "y": y}));
                }
                if radius != 0.0 {
                    o.insert("blur".into(), json!(radius));
                }
                if let Some(b) = blend(&r["blendMode"]) {
                    o.insert("blendMode".into(), json!(b));
                }
            }
            _ => continue,
        }
        if r["visible"] != true {
            o.insert("enabled".into(), json!(false));
        }
        out.push(Value::Object(o));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn kiwi_primitives() {
        // 300 as varuint, -3 as varint, 1.5 as varfloat (exponent rotated), "hi".
        let one_half = 1.5f32.to_bits().rotate_right(23).to_le_bytes();
        let mut d = vec![0xac, 0x02, 0x05];
        d.extend(one_half);
        d.extend(b"hi\0");
        let mut bb = Bb { d: &d, i: 0 };
        assert_eq!((bb.uint().unwrap(), bb.int().unwrap(), bb.float().unwrap(), bb.string().unwrap()), (300, -3, 1.5, "hi".to_string()));
        assert!(bb.byte().is_err());
    }

    #[test]
    fn figma_vector_network() {
        // A check mark: 3 vertices, 2 straight segments, no regions.
        let mut b = Vec::new();
        for v in [3u32, 2, 0] {
            b.extend(v.to_le_bytes());
        }
        for (x, y) in [(0.0f32, 5.0f32), (4.0, 9.0), (12.0, 0.0)] {
            b.extend(0u32.to_le_bytes());
            b.extend(x.to_le_bytes());
            b.extend(y.to_le_bytes());
        }
        for (a, z) in [(1u32, 0u32), (1, 2)] {
            b.extend(0u32.to_le_bytes());
            b.extend(a.to_le_bytes());
            b.extend([0u8; 8]);
            b.extend(z.to_le_bytes());
            b.extend([0u8; 8]);
        }
        let doc = Doc { props: HashMap::new(), kids: HashMap::new(), blobs: vec![b], vars: HashMap::new(), modes: HashMap::new(), styles: HashMap::new(), budget: Cell::new(1) };
        // The first segment runs backwards and is flipped so the polyline is continuous.
        assert_eq!(doc.vector(&json!({"vectorData": {"vectorNetworkBlob": 0}})), Some(("M0 5 L4 9 L12 0".to_string(), "nonzero")));
    }

    #[test]
    fn figma_clipboard_card() {
        // Made by app/scripts/figma-fixture.ts: deflate schema, zstd message.
        let html = include_str!("../tests/figma-clipboard.html");
        assert!(decode("<span>plain</span>").unwrap().is_none());
        let nodes = decode(html).unwrap().unwrap();
        assert_eq!(nodes.len(), 1, "one top-level layer, the internal canvas and variables stay out");
        let (card, b) = &nodes[0];
        assert_eq!(*b, (40.0, 60.0, 320.0, 230.0));
        let pick = |n: &Value, keys: &[&str]| -> Value { keys.iter().map(|k| (k.to_string(), n[*k].clone())).collect::<Map<_, _>>().into() };
        assert_eq!(pick(card, &["type", "name", "x", "y", "width", "height", "layout", "gap", "padding", "cornerRadius", "clip", "fill", "stroke", "strokeWidth", "strokeAlignment"]),
            json!({"type": "frame", "name": "Card", "x": 40.0, "y": 60.0, "width": 320.0, "height": null, "layout": "vertical", "gap": 12.0, "padding": 24.0,
                   "cornerRadius": 16.0, "clip": true, "fill": "#ffffffff", "stroke": "#e5e7ebff", "strokeWidth": 1.0, "strokeAlignment": "inner"}));
        assert_eq!(card["effect"], json!({"type": "shadow", "shadowType": "outer", "color": "#0000001a", "offset": {"x": 0.0, "y": 4.0}, "blur": 14.0}));
        let kids = card["children"].as_array().unwrap();
        let names: Vec<&str> = kids.iter().map(|k| k["name"].as_str().unwrap()).collect();
        assert_eq!(names, ["Title", "Body", "Row"]);
        assert_eq!(pick(&kids[0], &["content", "fontWeight", "fontSize", "textGrowth", "x"]), json!({"content": "Hello from Figma", "fontWeight": "700", "fontSize": 20.0, "textGrowth": null, "x": null}));
        assert_eq!(pick(&kids[1], &["textGrowth", "width", "lineHeight", "fontWeight"]), json!({"textGrowth": "fixed-width", "width": "fill_container", "lineHeight": 1.5, "fontWeight": "normal"}));
        let row = &kids[2];
        assert_eq!(pick(row, &["width", "height", "layout", "gap", "alignItems", "clip"]), json!({"width": "fill_container", "height": null, "layout": null, "gap": 12.0, "alignItems": "center", "clip": null}));
        let items = row["children"].as_array().unwrap();
        assert_eq!(items.iter().map(|k| k["type"].as_str().unwrap()).collect::<Vec<_>>(), ["rectangle", "frame", "path", "ellipse"]);
        assert_eq!(items[0]["fill"]["gradientType"], "linear");
        assert_eq!(items[0]["fill"]["colors"], json!([{"color": "#f97316ff", "position": 0.0}, {"color": "#ec4899ff", "position": 1.0}]));
        // The instance is expanded: its component's label with the text override, colored by the
        // variable's Dark mode that the card selects.
        let button = &items[1];
        assert_eq!(pick(button, &["name", "padding", "justifyContent", "alignItems", "cornerRadius", "fill"]),
            json!({"name": "Button", "padding": [10.0, 16.0], "justifyContent": "center", "alignItems": "center", "cornerRadius": 8.0, "fill": "#2563ebff"}));
        let label = &button["children"][0];
        assert_eq!(pick(label, &["name", "content", "fill", "fontWeight"]), json!({"name": "Label", "content": "Buy now", "fill": "#0f172aff", "fontWeight": "600"}));
        assert_eq!(pick(&items[2], &["geometry", "stroke", "strokeWidth", "strokeLinecap", "strokeLinejoin", "width", "height"]),
            json!({"geometry": "M0 6 L5 11 L16 0", "stroke": "#16a34aff", "strokeWidth": 2.0, "strokeLinecap": "round", "strokeLinejoin": "round", "width": 16.0, "height": 11.0}));
        assert_eq!(pick(&items[3], &["type", "fill", "width"]), json!({"type": "ellipse", "fill": "#22c55eff", "width": 12.0}));
    }

    #[test]
    fn inflate_errors_are_short() {
        // Over the cap: the message must not carry the (huge) partial output.
        let big = miniz_oxide::deflate::compress_to_vec(&vec![0u8; MAX_BYTES + 1], 1);
        assert_eq!(inflate(&big).unwrap_err(), TOO_BIG);
        let zbig = zstd::encode_all(std::io::repeat(0).take(MAX_BYTES as u64 + 1), 1).unwrap();
        assert_eq!(inflate(&zbig).unwrap_err(), TOO_BIG);
        // Corrupt and truncated streams get a plain description, not a byte dump.
        let err = inflate(&[0xff; 6]).unwrap_err();
        assert!(err.len() < 100 && !err.contains("output"), "{err}");
        let err = inflate(&miniz_oxide::deflate::compress_to_vec(&[7u8; 1000], 6)[..5]).unwrap_err();
        assert!(err.len() < 100 && !err.contains("output"), "{err}");
        assert_eq!(inflate(&miniz_oxide::deflate::compress_to_vec(b"ok", 6)).unwrap(), b"ok");
    }

    #[test]
    fn figma_sizing_and_transform() {
        let p = json!({"stackMode": "VERTICAL", "stackPrimarySizing": "RESIZE_TO_FIT", "stackChildAlignSelf": "STRETCH"});
        // Hugs its own vertical stack; stretched across its parent's vertical stack.
        assert!(sizing(&p, Some("VERTICAL"), false) == Size::Fit && sizing(&p, Some("VERTICAL"), true) == Size::Fill);
        assert!(sizing(&json!({}), None, true) == Size::Fixed);
        assert_eq!(radius(&json!({"cornerRadius": 8})), Some(json!(8.0)));
        assert_eq!(radius(&json!({"rectangleTopLeftCornerRadius": 4, "rectangleBottomRightCornerRadius": 2})), Some(json!([4.0, 0.0, 2.0, 0.0])));
        assert_eq!(hex(&json!({"r": 1, "g": 0.5, "b": 0, "a": 1}), 0.5), "#ff800080");
    }
}
