//! Turns the stored document into a render tree: component instances are
//! expanded, `$variables` are substituted for the active theme, disabled nodes
//! are dropped. Also hosts the on-load migration of older .pen versions.

use serde_json::{json, Map, Value};
use std::collections::HashMap;

pub struct RNode {
    /// Addressable path: `id`, or `instanceId/childId` inside component instances.
    pub id: String,
    pub ty: String,
    pub p: Map<String, Value>,
    pub kids: Vec<RNode>,
    // Filled by layout (absolute document coordinates).
    pub x: f64,
    pub y: f64,
    pub w: f64,
    pub h: f64,
    /// Local-bounds origin relative to the node's own origin; non-zero only for groups
    /// (union of their children). The transform pivot is `(x - bx, y - by)`.
    pub bx: f64,
    pub by: f64,
    pub lines: Vec<crate::text::Line>,
    pub problem: Option<&'static str>,
    /// Component id when this node is an expanded `ref` instance.
    pub inst: Option<String>,
}

impl RNode {
    pub fn s(&self, k: &str) -> Option<&str> {
        self.p.get(k).and_then(Value::as_str)
    }
    pub fn f(&self, k: &str) -> Option<f64> {
        self.p.get(k).and_then(num)
    }
    pub fn b(&self, k: &str) -> bool {
        self.p.get(k).and_then(Value::as_bool).unwrap_or(false)
    }
    pub fn name(&self) -> &str {
        self.s("name").unwrap_or(&self.ty)
    }
    pub fn visit(&self, f: &mut dyn FnMut(&RNode, usize), depth: usize) {
        f(self, depth);
        for k in &self.kids {
            k.visit(f, depth + 1);
        }
    }
}

pub fn num(v: &Value) -> Option<f64> {
    v.as_f64().or_else(|| v.as_str().and_then(|s| s.trim().parse().ok()))
}

pub struct Resolver<'a> {
    vars: &'a Map<String, Value>,
    defaults: Vec<(String, String)>,
    index: HashMap<String, &'a Value>,
}

impl<'a> Resolver<'a> {
    /// `libs` are imported documents as (alias, root); their nodes are addressable as `alias:id`.
    pub fn new(root: &'a Value, libs: &'a [(String, Value)], empty: &'a Map<String, Value>) -> Self {
        let vars = root["variables"].as_object().unwrap_or(empty);
        let defaults = root["themes"].as_object().map(|t| {
            t.iter().filter_map(|(axis, vals)| Some((axis.clone(), vals.as_array()?.first()?.as_str()?.to_string()))).collect()
        }).unwrap_or_default();
        let mut index = HashMap::new();
        crate::doc::walk(root["children"].as_array().map(Vec::as_slice).unwrap_or(&[]), &mut |n| {
            if let Some(id) = n["id"].as_str() {
                index.insert(id.to_string(), n);
            }
        });
        for (alias, lib) in libs {
            crate::doc::walk(lib["children"].as_array().map(Vec::as_slice).unwrap_or(&[]), &mut |n| {
                if let Some(id) = n["id"].as_str() {
                    index.insert(format!("{alias}:{id}"), n);
                }
            });
        }
        Resolver { vars, defaults, index }
    }

    pub fn node(&self, id: &str) -> Option<&'a Value> {
        self.index.get(id).copied()
    }

    pub fn resolve(&self, node: &Value) -> Option<RNode> {
        self.resolve_in(node, "", &self.defaults.clone(), 0)
    }

    fn resolve_in(&self, node: &Value, prefix: &str, theme: &[(String, String)], depth: usize) -> Option<RNode> {
        if depth > 64 {
            return None;
        }
        let mut theme = theme.to_vec();
        if let Some(t) = node["theme"].as_object() {
            for (axis, val) in t {
                let val = val.as_str().unwrap_or_default().to_string();
                match theme.iter_mut().find(|(a, _)| a == axis) {
                    Some(slot) => slot.1 = val,
                    None => theme.push((axis.clone(), val)),
                }
            }
        }
        let own_id = node["id"].as_str().unwrap_or_default();
        let path = if prefix.is_empty() { own_id.to_string() } else { format!("{prefix}/{own_id}") };
        // Instance roots keep their own id; their children are addressed as instanceId/childId.
        let node_was_ref = node["type"] == "ref";
        let ref_target = node["ref"].as_str().unwrap_or_default().to_string();
        let child_prefix = if node_was_ref { path.clone() } else { prefix.to_string() };

        // Component instance: clone the component, apply instance props and descendant overrides.
        let expanded;
        let node = if node["type"] == "ref" {
            let Some(merged) = self.expand(node, 0) else {
                let mut p = Map::new();
                for (k, v) in node.as_object()? {
                    p.insert(k.clone(), v.clone());
                }
                p.insert("fill".into(), json!("#FF3B3022"));
                return Some(RNode::leaf(path, "frame", p, Some("missing component")));
            };
            expanded = merged;
            &expanded
        } else {
            node
        };

        let mut p = Map::new();
        for (k, v) in node.as_object()? {
            if k != "children" {
                p.insert(k.clone(), self.subst(v, &theme, 0));
            }
        }
        if p.get("enabled") == Some(&json!(false)) {
            return None;
        }
        let ty = match p.get("type").and_then(Value::as_str).unwrap_or("frame") {
            "ref" => "frame".to_string(),
            t => t.to_string(),
        };
        let kids = node["children"].as_array().map(|ks| {
            ks.iter().filter_map(|k| self.resolve_in(k, &child_prefix, &theme, depth + 1)).collect()
        }).unwrap_or_default();
        let mut r = RNode::leaf(path, &ty, p, None);
        r.kids = kids;
        if node_was_ref {
            r.inst = Some(ref_target);
        }
        Some(r)
    }

    /// A `ref` instance as a plain node: the component cloned, then the instance's props and
    /// descendant overrides applied. A component may itself be an instance of another component
    /// (.pen allows refs of refs), so the chain is expanded from the innermost component out.
    fn expand(&self, node: &Value, depth: usize) -> Option<Value> {
        let comp = *self.index.get(node["ref"].as_str()?)?;
        let mut merged = if comp["type"] == "ref" && depth < 16 { self.expand(comp, depth + 1)? } else { comp.clone() };
        for (k, v) in node.as_object()? {
            if !matches!(k.as_str(), "id" | "type" | "ref" | "descendants" | "reusable") {
                merged[k] = v.clone();
            }
        }
        merged["reusable"] = json!(false);
        if let Some(desc) = node["descendants"].as_object() {
            for (key, over) in desc {
                apply_override(&mut merged, key, over);
            }
        }
        Some(merged)
    }

    /// Replace `$name` strings with the variable's value for the active theme.
    /// Unknown names stay literal (so "$49" prices survive).
    fn subst(&self, v: &Value, theme: &[(String, String)], depth: usize) -> Value {
        match v {
            Value::String(s) if s.starts_with('$') && depth < 16 => match self.var_value(&s[1..], theme) {
                Some(val) => self.subst(&val, theme, depth + 1),
                None => v.clone(),
            },
            Value::Array(a) => Value::Array(a.iter().map(|x| self.subst(x, theme, depth)).collect()),
            Value::Object(o) => Value::Object(o.iter().map(|(k, x)| (k.clone(), self.subst(x, theme, depth))).collect()),
            _ => v.clone(),
        }
    }

    pub fn var_value(&self, name: &str, theme: &[(String, String)]) -> Option<Value> {
        let def = self.vars.get(name)?;
        let val = &def["value"];
        let Some(list) = val.as_array() else { return Some(val.clone()) };
        // Scan from the end, first entry whose theme fully matches wins.
        list.iter().rev().find(|e| {
            e["theme"].as_object().map_or(true, |t| t.iter().all(|(a, v)| theme.iter().any(|(ta, tv)| ta == a && Some(tv.as_str()) == v.as_str())))
        }).or_else(|| list.first()).map(|e| e.get("value").cloned().unwrap_or_else(|| e.clone()))
    }
}

impl RNode {
    pub fn leaf(id: String, ty: &str, p: Map<String, Value>, problem: Option<&'static str>) -> RNode {
        RNode { id, ty: ty.to_string(), p, kids: Vec::new(), x: 0.0, y: 0.0, w: 0.0, h: 0.0, bx: 0.0, by: 0.0, lines: Vec::new(), problem, inst: None }
    }
}

/// `key` is an id path relative to the component root, e.g. `childId` or `nestedInstance/childId`.
fn apply_override(root: &mut Value, key: &str, over: &Value) {
    let (head, rest) = match key.split_once('/') {
        Some((h, r)) => (h, Some(r)),
        None => (key, None),
    };
    let Some(target) = find_desc_mut(root, head) else { return };
    match rest {
        Some(rest) if target["type"] == "ref" => {
            if !target["descendants"].is_object() {
                target["descendants"] = json!({});
            }
            let slot = target["descendants"].as_object_mut().unwrap().entry(rest.to_string()).or_insert_with(|| json!({}));
            merge_into(slot, over);
        }
        Some(rest) => apply_override(target, rest, over),
        None if over.get("type").is_some() => {
            let id = target["id"].clone();
            *target = over.clone();
            target["id"] = id;
        }
        None => merge_into(target, over),
    }
}

fn find_desc_mut<'v>(node: &'v mut Value, id: &str) -> Option<&'v mut Value> {
    let kids = node.get_mut("children")?.as_array_mut()?;
    if let Some(i) = kids.iter().position(|k| k["id"] == id) {
        return Some(&mut kids[i]);
    }
    for k in kids.iter_mut() {
        if let Some(f) = find_desc_mut(k, id) {
            return Some(f);
        }
    }
    None
}

fn merge_into(target: &mut Value, over: &Value) {
    if let (Some(t), Some(o)) = (target.as_object_mut(), over.as_object()) {
        for (k, v) in o {
            t.insert(k.clone(), v.clone());
        }
    }
}

/// Run the .pen file migrations (2.6 → 2.19) in place and stamp the document as 2.19.
/// Files without a version are treated as current; icon_font / stroke objects are upgraded regardless.
pub fn migrate(root: &mut Value) {
    // ponytail: migrations older than 2.6 (layout objects, text size, image nodes) are skipped; no kit uses them.
    let ver = root["version"].as_str().and_then(|v| {
        let (a, b) = v.split_once('.')?;
        Some((a.parse::<u32>().ok()?, b.parse::<u32>().ok()?))
    }).unwrap_or((2, 19));
    let before = |m: u32| ver < (2, m);
    fn walk(n: &mut Value, f: &dyn Fn(&mut Value)) {
        f(n);
        for key in ["descendants", "children"] {
            match n.get_mut(key) {
                Some(Value::Object(d)) => d.values_mut().for_each(|v| walk(v, f)),
                Some(Value::Array(a)) => a.iter_mut().for_each(|v| walk(v, f)),
                _ => {}
            }
        }
    }
    let fix = |n: &mut Value| {
        let Some(o) = n.as_object_mut() else { return };
        let ty = o.get("type").and_then(Value::as_str).unwrap_or("").to_string();
        if ty == "icon_font" {
            o.insert("type".into(), json!("icon"));
        }
        if let Some(v) = o.remove("iconFontName") {
            o.insert("icon".into(), v);
        }
        if let Some(v) = o.remove("iconFontFamily") {
            o.insert("library".into(), v);
        }
        // 2.11 → 2.12: stroke objects become flat props; a missing align means inner.
        let legacy = |s: &Map<String, Value>| before(12) || s.contains_key("thickness") || s.contains_key("align");
        if let Some(s) = o.get("stroke").and_then(Value::as_object).filter(|s| legacy(s)).cloned() {
            o.insert("stroke".into(), s.get("fill").cloned().unwrap_or(json!([])));
            o.insert("strokeWidth".into(), s.get("thickness").cloned().unwrap_or(json!(0)));
            o.insert("strokeLinejoin".into(), s.get("join").cloned().unwrap_or(json!("miter")));
            let cap = s.get("cap").and_then(Value::as_str).filter(|c| matches!(*c, "round" | "square")).unwrap_or("butt");
            o.insert("strokeLinecap".into(), json!(cap));
            let align = match s.get("align").and_then(Value::as_str) {
                Some("outside" | "outer") => "outer",
                Some("center") => "center",
                _ => "inner",
            };
            o.insert("strokeAlignment".into(), json!(align));
        }
        // 2.6 → 2.7: sweep angles flip direction.
        if before(7) && matches!(ty.as_str(), "ellipse" | "ref") {
            if let Some(v) = o.get("sweepAngle").and_then(Value::as_f64) {
                o.insert("sweepAngle".into(), json!(-v));
            }
        }
        // 2.14 → 2.15: uneven per-side strokes get round joins.
        if before(15) && matches!(ty.as_str(), "rectangle" | "frame") {
            if let Some(w) = o.get("strokeWidth").and_then(Value::as_object) {
                let g = |k: &str| w.get(k).and_then(num).unwrap_or(0.0);
                if g("top") != g("right") || g("top") != g("bottom") || g("top") != g("left") {
                    o.insert("strokeLinejoin".into(), json!("round"));
                }
            }
        }
        // 2.15 → 2.16: groups with a layout become non-clipping frames; others lose layout props.
        if before(16) && ty == "group" {
            if matches!(o.get("layout").and_then(Value::as_str), Some("horizontal" | "vertical")) {
                o.insert("type".into(), json!("frame"));
                o.insert("clip".into(), json!(false));
            } else {
                for k in ["layout", "gap", "padding", "justifyContent", "alignItems", "layoutIncludeStroke"] {
                    o.remove(k);
                }
            }
        }
        // 2.18 → 2.19: inner shadows become outer, spread is gone.
        if before(19) {
            let shadows: Vec<&mut Value> = match o.get_mut("effect") {
                Some(Value::Array(a)) => a.iter_mut().collect(),
                Some(e @ Value::Object(_)) => vec![e],
                _ => vec![],
            };
            for e in shadows.into_iter().filter(|e| e["type"] == "shadow") {
                if e["shadowType"] == "inner" {
                    e["shadowType"] = json!("outer");
                }
                if let Some(e) = e.as_object_mut() {
                    e.remove("spread");
                }
            }
        }
    };
    if let Some(kids) = root.get_mut("children").and_then(Value::as_array_mut) {
        for k in kids {
            walk(k, &fix);
        }
    }
    if root.is_object() {
        root["version"] = json!("2.19");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn instances_and_themes() {
        let root = json!({
            "themes": {"mode": ["light", "dark"]},
            "variables": {"fg": {"type": "color", "value": [{"value": "#111111"}, {"value": "#EEEEEE", "theme": {"mode": "dark"}}]}},
            "children": [
                {"type": "frame", "id": "btn", "reusable": true, "children": [{"type": "text", "id": "lbl", "content": "Go", "fill": "$fg"}]},
                {"type": "ref", "id": "b1", "ref": "btn", "theme": {"mode": "dark"}, "descendants": {"lbl": {"content": "Buy $49"}}}
            ]
        });
        let empty = Map::new();
        let r = Resolver::new(&root, &[], &empty);
        let inst = r.resolve(&root["children"][1]).unwrap();
        assert_eq!(inst.id, "b1");
        assert_eq!(inst.kids[0].id, "b1/lbl");
        assert_eq!(inst.kids[0].p["content"], "Buy $49");
        assert_eq!(inst.kids[0].p["fill"], "#EEEEEE");
        let comp = r.resolve(&root["children"][0]).unwrap();
        assert_eq!(comp.kids[0].p["fill"], "#111111");
    }

    /// A component that is itself an instance (ref of a ref) expands through the whole chain,
    /// keeping each level's overrides (the shadcn kit's modals are built this way).
    #[test]
    fn ref_of_ref_expands() {
        let root = json!({"children": [
            {"type": "frame", "id": "card", "reusable": true, "width": 100, "children": [{"type": "text", "id": "t", "content": "A"}]},
            {"type": "ref", "id": "v", "ref": "card", "reusable": true, "width": 80, "descendants": {"t": {"content": "B"}}},
            {"type": "ref", "id": "i", "ref": "v", "x": 5}
        ]});
        let empty = Map::new();
        let r = Resolver::new(&root, &[], &empty);
        let inst = r.resolve(&root["children"][2]).unwrap();
        assert_eq!((inst.ty.as_str(), inst.inst.as_deref()), ("frame", Some("v")));
        assert_eq!((inst.p["width"].as_f64(), inst.p["x"].as_f64()), (Some(80.0), Some(5.0)));
        assert_eq!(inst.kids.len(), 1);
        assert_eq!((inst.kids[0].id.as_str(), inst.kids[0].p["content"].as_str()), ("i/t", Some("B")));
    }

    #[test]
    fn migrates_old_nodes() {
        let mut root = json!({"version": "2.6", "children": [{"type": "icon_font", "iconFontName": "x", "iconFontFamily": "lucide",
            "stroke": {"thickness": 1, "fill": "#000"}},
            {"type": "ellipse", "sweepAngle": 90, "effect": {"type": "shadow", "shadowType": "inner", "spread": 2}},
            {"type": "group", "layout": "vertical", "children": [{"type": "group", "gap": 4}]}]});
        migrate(&mut root);
        let n = &root["children"][0];
        assert_eq!(n["type"], "icon");
        assert_eq!(n["icon"], "x");
        assert_eq!(n["strokeAlignment"], "inner");
        assert_eq!(n["stroke"], "#000");
        assert_eq!(n["strokeLinecap"], "butt");
        let e = &root["children"][1];
        assert_eq!(e["sweepAngle"], -90.0);
        assert_eq!(e["effect"], json!({"type": "shadow", "shadowType": "outer"}));
        let g = &root["children"][2];
        assert_eq!((g["type"].as_str(), g["clip"].as_bool()), (Some("frame"), Some(false)));
        assert!(g["children"][0].get("gap").is_none());
        assert_eq!(root["version"], "2.19");
        migrate(&mut root);
        assert_eq!(root["children"][1]["sweepAngle"], -90.0, "migrations run once");
    }
}
