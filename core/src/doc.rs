//! .pen-compatible document model. Nodes stay `serde_json::Value` so unknown
//! properties round-trip losslessly between .pen files and Nalarupa.

use serde_json::{json, Map, Value};
use std::cell::Cell;
use std::path::{Path, PathBuf};

pub const FORMAT_VERSION: &str = "2.19";
pub const ROOT: &str = "document";

const NODE_TYPES: &[&str] = &[
    "frame", "group", "rectangle", "ellipse", "polygon", "line", "path", "text", "note", "prompt",
    "context", "icon", "script", "browser", "ref",
];

#[derive(Clone)]
pub struct Doc {
    pub root: Value,
    pub path: Option<PathBuf>,
}

impl Default for Doc {
    fn default() -> Self {
        Doc::new()
    }
}

impl Doc {
    pub fn new() -> Self {
        Doc { root: json!({"version": FORMAT_VERSION, "children": []}), path: None }
    }

    pub fn from_json(text: &str) -> Result<Self, String> {
        let root: Value = serde_json::from_str(text.trim_start_matches('\u{feff}'))
            .map_err(|e| format!("invalid document JSON: {e}"))?;
        if !root.is_object() {
            return Err("document must be a JSON object".into());
        }
        let mut d = Doc { root, path: None };
        if !d.root["children"].is_array() {
            d.root["children"] = json!([]);
        }
        Ok(d)
    }

    pub fn load(path: &Path) -> Result<Self, String> {
        let text = std::fs::read_to_string(path).map_err(|e| format!("cannot read {}: {e}", path.display()))?;
        let mut d = Doc::from_json(&text)?;
        d.path = Some(path.to_path_buf());
        Ok(d)
    }

    /// Atomic save: write a temp file next to the target, then rename over it.
    pub fn save(&self, path: &Path) -> Result<(), String> {
        let text = serde_json::to_string_pretty(&self.root).map_err(|e| e.to_string())?;
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| e.to_string())?;
        }
        let tmp = path.with_extension("tmp-nalarupa");
        std::fs::write(&tmp, text).map_err(|e| format!("cannot write {}: {e}", tmp.display()))?;
        std::fs::rename(&tmp, path).map_err(|e| format!("cannot save {}: {e}", path.display()))
    }

    pub fn dir(&self) -> Option<PathBuf> {
        self.path.as_ref().and_then(|p| p.parent().map(Path::to_path_buf))
    }

    pub fn children(&self) -> &[Value] {
        self.root["children"].as_array().map(Vec::as_slice).unwrap_or(&[])
    }

    // ── lookup ──────────────────────────────────────────────────────────────

    pub fn find(&self, id: &str) -> Option<&Value> {
        find_in(self.children(), id)
    }

    fn index_path(&self, id: &str) -> Option<Vec<usize>> {
        let mut acc = Vec::new();
        index_path_in(self.children(), id, &mut acc).then_some(acc)
    }

    fn node_at_mut(&mut self, ip: &[usize]) -> &mut Value {
        let mut cur = &mut self.root["children"][ip[0]];
        for &i in &ip[1..] {
            cur = &mut cur["children"][i];
        }
        cur
    }

    fn children_at_mut(&mut self, parent_ip: &[usize]) -> &mut Vec<Value> {
        let parent = if parent_ip.is_empty() { &mut self.root } else { self.node_at_mut(parent_ip) };
        if !parent["children"].is_array() {
            parent["children"] = json!([]);
        }
        parent["children"].as_array_mut().unwrap()
    }

    /// Returns (parent id or "document", index among siblings).
    pub fn parent_of(&self, id: &str) -> Option<(String, usize)> {
        let ip = self.index_path(id)?;
        let parent = if ip.len() == 1 {
            ROOT.to_string()
        } else {
            let mut cur = &self.root["children"][ip[0]];
            for &i in &ip[1..ip.len() - 1] {
                cur = &cur["children"][i];
            }
            cur["id"].as_str().unwrap_or_default().to_string()
        };
        Some((parent, *ip.last().unwrap()))
    }

    pub fn all_ids(&self) -> Vec<String> {
        let mut out = Vec::new();
        walk(self.children(), &mut |n| {
            if let Some(id) = n["id"].as_str() {
                out.push(id.to_string())
            }
        });
        out
    }

    pub fn gen_id(&self) -> String {
        loop {
            let id = random_id();
            if self.find(&id).is_none() {
                return id;
            }
        }
    }

    // ── mutations ───────────────────────────────────────────────────────────

    pub fn insert(&mut self, parent: &str, mut node: Value, index: Option<usize>) -> Result<String, String> {
        validate_node(&node)?;
        self.assign_ids(&mut node, &mut Vec::new());
        let id = node["id"].as_str().unwrap().to_string();
        let parent_ip = self.container_path(parent)?;
        let kids = self.children_at_mut(&parent_ip);
        let at = index.unwrap_or(kids.len()).min(kids.len());
        kids.insert(at, node);
        Ok(id)
    }

    /// Merge properties into a node; `null` removes a property. Paths with `/`
    /// address descendants of component instances and become overrides.
    pub fn update(&mut self, path: &str, data: &Value) -> Result<(), String> {
        let data = data.as_object().ok_or("update data must be an object")?;
        if let Some((inst, rest)) = path.split_once('/') {
            let node = self.find_mut(inst).ok_or_else(|| format!("node not found: {inst}"))?;
            if node["type"] != "ref" {
                return Err(format!("'{inst}' is not a component instance; '/' paths only address instance descendants"));
            }
            let desc = ensure_obj(&mut node["descendants"]);
            let entry = desc.entry(rest.to_string()).or_insert_with(|| json!({}));
            merge_props(entry, data, true);
            return Ok(());
        }
        let node = self.find_mut(path).ok_or_else(|| format!("node not found: {path}"))?;
        merge_props(node, data, false);
        Ok(())
    }

    pub fn delete(&mut self, path: &str) -> Result<(), String> {
        if path.contains('/') {
            return Err("cannot delete descendants of component instances; override them with {enabled:false}".into());
        }
        let ip = self.index_path(path).ok_or_else(|| format!("node not found: {path}"))?;
        let (last, parent_ip) = ip.split_last().unwrap();
        self.children_at_mut(parent_ip).remove(*last);
        Ok(())
    }

    pub fn move_node(&mut self, path: &str, parent: Option<&str>, index: Option<usize>) -> Result<(), String> {
        if path.contains('/') {
            return Err("cannot move descendants of component instances".into());
        }
        let ip = self.index_path(path).ok_or_else(|| format!("node not found: {path}"))?;
        let target = match parent {
            Some(p) => p.to_string(),
            None => self.parent_of(path).unwrap().0,
        };
        if target != ROOT {
            let node = self.find(path).unwrap();
            if target == path || find_in(node["children"].as_array().map(Vec::as_slice).unwrap_or(&[]), &target).is_some() {
                return Err("cannot move a node into itself".into());
            }
        }
        let (last, parent_ip) = ip.split_last().unwrap();
        let node = self.children_at_mut(parent_ip).remove(*last);
        let tip = self.container_path(&target)?;
        let kids = self.children_at_mut(&tip);
        let at = index.unwrap_or(kids.len()).min(kids.len());
        kids.insert(at, node);
        Ok(())
    }

    /// Copy `source` (already resolved when it addresses an instance descendant)
    /// under `parent`. Reusable sources become connected `ref` instances.
    pub fn copy(&mut self, source: &Value, parent: &str, data: Option<&Value>) -> Result<String, String> {
        let mut node = if source["reusable"] == true {
            json!({"type": "ref", "ref": source["id"]})
        } else {
            source.clone()
        };
        let mut remap = Vec::new();
        self.assign_ids(&mut node, &mut remap);
        if let Some(obj) = node.as_object_mut() {
            obj.remove("reusable");
            if source["reusable"] != true {
                obj.remove("x");
                obj.remove("y");
            }
        }
        if let Some(d) = data.and_then(Value::as_object) {
            let mut top = d.clone();
            let desc = top.remove("descendants");
            merge_props(&mut node, &top, false);
            if let Some(Value::Object(desc)) = desc {
                if node["type"] == "ref" {
                    node["descendants"] = Value::Object(desc);
                } else {
                    for (key, over) in desc {
                        let target = remap.iter().find(|(old, _)| *old == key).map(|(_, new)| new.clone())
                            .or_else(|| find_by_name(&node, &key))
                            .ok_or_else(|| format!("descendant '{key}' not found in copied node"))?;
                        let slot = find_mut_in_node(&mut node, &target).unwrap();
                        if over.get("type").is_some() {
                            let mut repl = over.clone();
                            self.assign_ids(&mut repl, &mut Vec::new());
                            *slot = repl;
                        } else {
                            merge_props(slot, over.as_object().ok_or("descendant override must be an object")?, false);
                        }
                    }
                }
            }
        }
        self.insert_prepared(parent, node)
    }

    pub fn replace(&mut self, path: &str, mut node: Value) -> Result<String, String> {
        validate_node(&node)?;
        if let Some((inst, rest)) = path.split_once('/') {
            self.assign_ids(&mut node, &mut Vec::new());
            node["id"] = json!(rest.rsplit('/').next().unwrap());
            let inst_node = self.find_mut(inst).ok_or_else(|| format!("node not found: {inst}"))?;
            if inst_node["type"] != "ref" {
                return Err(format!("'{inst}' is not a component instance"));
            }
            ensure_obj(&mut inst_node["descendants"]).insert(rest.to_string(), node);
            return Ok(path.to_string());
        }
        let ip = self.index_path(path).ok_or_else(|| format!("node not found: {path}"))?;
        self.assign_ids(&mut node, &mut Vec::new());
        let id = node["id"].as_str().unwrap().to_string();
        *self.node_at_mut(&ip) = node;
        Ok(id)
    }

    pub fn set_variables(&mut self, vars: &Value, replace: bool) -> Result<(), String> {
        let vars = vars.as_object().ok_or("variables must be an object")?;
        for (name, def) in vars {
            if name.starts_with('$') {
                return Err(format!("variable name '{name}' must not start with '$'"));
            }
            let ty = def["type"].as_str().ok_or_else(|| format!("variable '{name}' needs {{type, value}}"))?;
            if !["color", "number", "string", "boolean"].contains(&ty) || def.get("value").is_none() {
                return Err(format!("variable '{name}': type must be color|number|string|boolean and value is required"));
            }
        }
        let root = self.root.as_object_mut().unwrap();
        if replace || !root.get("variables").is_some_and(Value::is_object) {
            root.insert("variables".into(), json!({}));
        }
        let mut axes: Vec<(String, String)> = Vec::new();
        let target = root["variables"].as_object_mut().unwrap();
        for (name, def) in vars {
            if let Some(list) = def["value"].as_array() {
                for entry in list {
                    if let Some(theme) = entry["theme"].as_object() {
                        for (axis, val) in theme {
                            axes.push((axis.clone(), val.as_str().unwrap_or_default().to_string()));
                        }
                    }
                }
            }
            target.insert(name.clone(), def.clone());
        }
        if !axes.is_empty() {
            let themes = ensure_obj(root.entry("themes").or_insert_with(|| json!({})));
            for (axis, val) in axes {
                let list = themes.entry(axis).or_insert_with(|| json!([]));
                let arr = list.as_array_mut().unwrap();
                if !arr.iter().any(|v| v == &json!(val)) {
                    arr.push(json!(val));
                }
            }
        }
        Ok(())
    }

    pub fn variables(&self) -> Value {
        json!({"variables": self.root.get("variables").cloned().unwrap_or(json!({})),
               "themes": self.root.get("themes").cloned().unwrap_or(json!({}))})
    }

    // ── helpers ─────────────────────────────────────────────────────────────

    fn find_mut(&mut self, id: &str) -> Option<&mut Value> {
        let ip = self.index_path(id)?;
        Some(self.node_at_mut(&ip))
    }

    fn container_path(&self, parent: &str) -> Result<Vec<usize>, String> {
        if parent == ROOT || parent.is_empty() {
            return Ok(Vec::new());
        }
        if parent.contains('/') {
            return Err("cannot insert into a component instance; use Replace or descendants overrides".into());
        }
        let node = self.find(parent).ok_or_else(|| format!("parent not found: {parent}"))?;
        match node["type"].as_str() {
            Some("frame") | Some("group") => Ok(self.index_path(parent).unwrap()),
            other => Err(format!("only frame and group nodes can have children (parent '{parent}' is {})", other.unwrap_or("?"))),
        }
    }

    fn insert_prepared(&mut self, parent: &str, node: Value) -> Result<String, String> {
        let id = node["id"].as_str().unwrap_or_default().to_string();
        let ip = self.container_path(parent)?;
        self.children_at_mut(&ip).push(node);
        Ok(id)
    }

    /// Give a subtree fresh unique ids (input ids are never trusted), recording old→new.
    fn assign_ids(&self, node: &mut Value, remap: &mut Vec<(String, String)>) {
        let fresh = loop {
            let id = random_id();
            if self.find(&id).is_none() && !remap.iter().any(|(_, n)| *n == id) {
                break id;
            }
        };
        if let Some(old) = node["id"].as_str() {
            remap.push((old.to_string(), fresh.clone()));
        } else {
            remap.push((String::new(), fresh.clone()));
        }
        node["id"] = json!(fresh);
        if let Some(kids) = node.get_mut("children").and_then(Value::as_array_mut) {
            for k in kids {
                self.assign_ids(k, remap);
            }
        }
    }
}

pub fn find_in<'a>(nodes: &'a [Value], id: &str) -> Option<&'a Value> {
    for n in nodes {
        if n["id"] == id {
            return Some(n);
        }
        if let Some(kids) = n["children"].as_array() {
            if let Some(f) = find_in(kids, id) {
                return Some(f);
            }
        }
    }
    None
}

fn index_path_in(nodes: &[Value], id: &str, acc: &mut Vec<usize>) -> bool {
    for (i, n) in nodes.iter().enumerate() {
        acc.push(i);
        if n["id"] == id {
            return true;
        }
        if let Some(kids) = n["children"].as_array() {
            if index_path_in(kids, id, acc) {
                return true;
            }
        }
        acc.pop();
    }
    false
}

fn find_mut_in_node<'a>(node: &'a mut Value, id: &str) -> Option<&'a mut Value> {
    if node["id"] == id {
        return Some(node);
    }
    node.get_mut("children")?.as_array_mut()?.iter_mut().find_map(|k| find_mut_in_node(k, id))
}

fn find_by_name(node: &Value, name: &str) -> Option<String> {
    let mut hits = Vec::new();
    walk(std::slice::from_ref(node), &mut |n| {
        if n["name"] == name {
            hits.push(n["id"].as_str().unwrap_or_default().to_string());
        }
    });
    (hits.len() == 1).then(|| hits.remove(0))
}

pub fn walk<'a>(nodes: &'a [Value], f: &mut dyn FnMut(&'a Value)) {
    for n in nodes {
        f(n);
        if let Some(kids) = n["children"].as_array() {
            walk(kids, f);
        }
    }
}

fn validate_node(node: &Value) -> Result<(), String> {
    let ty = node["type"].as_str().ok_or("node needs a string `type`")?;
    if !NODE_TYPES.contains(&ty) {
        return Err(format!("unknown node type '{ty}' (valid: {})", NODE_TYPES.join(", ")));
    }
    if ty == "ref" && !node["ref"].is_string() {
        return Err("ref nodes need a `ref` id".into());
    }
    if node.get("children").is_some_and(|c| !c.is_null()) && !matches!(ty, "frame" | "group") {
        return Err(format!("only frame and group nodes can have children ({ty} cannot)"));
    }
    Ok(())
}

fn ensure_obj(v: &mut Value) -> &mut Map<String, Value> {
    if !v.is_object() {
        *v = json!({});
    }
    v.as_object_mut().unwrap()
}

/// `keep_type` allows `type` inside instance overrides (replacement nodes).
fn merge_props(target: &mut Value, data: &Map<String, Value>, keep_type: bool) {
    let obj = ensure_obj(target);
    for (k, v) in data {
        if matches!(k.as_str(), "id" | "ref" | "children") || (k == "type" && !keep_type) {
            continue;
        }
        if v.is_null() {
            obj.remove(k);
        } else {
            obj.insert(k.clone(), v.clone());
        }
    }
}

thread_local! {
    static SEED: Cell<u64> = Cell::new(
        std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_nanos() as u64).unwrap_or(7) | 1,
    );
}

/// Make the next generated ids deterministic. Streaming previews and the final
/// `execute` of the same call use the same seed, so nodes keep their ids.
pub fn reseed(seed: u64) {
    SEED.with(|s| s.set(seed | 1));
}

pub fn random_u64() -> u64 {
    SEED.with(|s| {
        let mut x = s.get();
        x ^= x << 13;
        x ^= x >> 7;
        x ^= x << 17;
        s.set(x);
        x
    })
}

fn random_id() -> String {
    const ABC: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
    let mut r = random_u64();
    (0..5).map(|_| {
        let c = ABC[(r % ABC.len() as u64) as usize] as char;
        r /= ABC.len() as u64;
        c
    }).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ops_roundtrip() {
        let mut d = Doc::new();
        let f = d.insert(ROOT, json!({"type":"frame","id":"ignored","name":"F","layout":"vertical"}), None).unwrap();
        assert_ne!(f, "ignored");
        let t = d.insert(&f, json!({"type":"text","content":"hi"}), None).unwrap();
        // Leaves get no `children: null` (re-inserting a copy must validate).
        assert!(d.find(&t).unwrap().get("children").is_none());
        d.update(&t, &json!({"fontSize": 20, "content": null})).unwrap();
        assert_eq!(d.find(&t).unwrap()["fontSize"], 20);
        assert!(d.find(&t).unwrap().get("content").is_none());
        assert!(d.insert(&t, json!({"type":"text"}), None).is_err());
        let g = d.insert(ROOT, json!({"type":"group"}), None).unwrap();
        d.move_node(&t, Some(&g), None).unwrap();
        assert_eq!(d.parent_of(&t).unwrap().0, g);
        assert!(d.move_node(&g, Some(&g), None).is_err());
        let src = d.find(&f).unwrap().clone();
        let c = d.copy(&src, ROOT, Some(&json!({"name":"Copy"}))).unwrap();
        assert_eq!(d.find(&c).unwrap()["name"], "Copy");
        d.delete(&c).unwrap();
        assert!(d.find(&c).is_none());
        d.set_variables(&json!({"bg":{"type":"color","value":[{"value":"#fff","theme":{"mode":"light"}},{"value":"#000","theme":{"mode":"dark"}}]}}), false).unwrap();
        assert_eq!(d.root["themes"]["mode"], json!(["light","dark"]));
    }
}
