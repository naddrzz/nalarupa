//! .pen-compatible document model. Nodes stay `serde_json::Value` so unknown
//! properties round-trip losslessly between .pen files and Nalarupa.

use serde_json::{json, Map, Value};
use std::cell::Cell;
use std::collections::{HashMap, HashSet};
use std::path::{Component, Path, PathBuf};

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

    /// Atomic save: write a temp file next to the target, then rename over it. A symlink is followed
    /// (the rename would replace the link, leaving the real file stale); a failed save leaves no temp file.
    pub fn save(&self, path: &Path) -> Result<(), String> {
        let text = serde_json::to_string_pretty(&self.root).map_err(|e| e.to_string())?;
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| e.to_string())?;
        }
        let target = match std::fs::canonicalize(path) {
            Ok(target) => target,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound && !path.is_symlink() => path.to_path_buf(),
            Err(e) => return Err(format!("cannot resolve {}: {e}", path.display())),
        };
        let mut tmp_name = target.file_name().unwrap_or_default().to_os_string();
        tmp_name.push(".tmp-nalarupa");
        let tmp = target.with_file_name(tmp_name);
        std::fs::write(&tmp, text).map_err(|e| {
            let _ = std::fs::remove_file(&tmp);
            format!("cannot write {}: {e}", tmp.display())
        })?;
        std::fs::rename(&tmp, &target).map_err(|e| {
            let _ = std::fs::remove_file(&tmp);
            format!("cannot save {}: {e}", path.display())
        })
    }

    pub fn dir(&self) -> Option<PathBuf> {
        self.path.as_ref().and_then(|p| p.parent().map(Path::to_path_buf))
    }

    /// Save as: the relative references of `root` (image and shader `url`, `scriptUri`, `imports`)
    /// resolve against the folder of the document, so a document moving from `from` to `to`
    /// gets them rewritten to reach the same files. Absolute paths, urls and `pencil:` ids stay.
    pub fn relocate(root: &mut Value, from: &Path, to: &Path) {
        let (from, to) = (lexical(from), lexical(to));
        if let Some(imports) = root.get_mut("imports").and_then(Value::as_object_mut) {
            for v in imports.values_mut() {
                if let Some(n) = v.as_str().and_then(|s| relocated(&s.replace("%20", " "), &from, &to)) {
                    *v = json!(n.replace(' ', "%20"));
                }
            }
        }
        if let Some(children) = root.get_mut("children") {
            relocate_in(children, &from, &to);
        }
    }

    /// Whether two folders are the same one (by their paths, without touching the disk).
    pub fn same_dir(a: &Path, b: &Path) -> bool {
        lexical(a) == lexical(b)
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

    fn node_at(&self, ip: &[usize]) -> &Value {
        ip[1..].iter().fold(&self.root["children"][ip[0]], |cur, &i| &cur["children"][i])
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

    /// The ids in `ids` that sit inside none of the other listed layers, in their given order.
    /// One walk of the document, whatever the number of ids.
    pub fn outermost(&self, ids: &[String]) -> Vec<String> {
        let listed: std::collections::HashSet<&str> = ids.iter().map(String::as_str).collect();
        let mut nested = std::collections::HashSet::new();
        fn visit<'a>(nodes: &'a [Value], under: bool, listed: &std::collections::HashSet<&str>, nested: &mut std::collections::HashSet<&'a str>) {
            for n in nodes {
                let id = n["id"].as_str().unwrap_or_default();
                if under {
                    nested.insert(id);
                }
                if let Some(kids) = n["children"].as_array() {
                    visit(kids, under || listed.contains(id), listed, nested);
                }
            }
        }
        visit(self.children(), false, &listed, &mut nested);
        let mut seen = std::collections::HashSet::new();
        ids.iter()
            .filter(|id| {
                // A component instance's rendered descendants ("inst/child") are not in the tree: judge them by the instance.
                let base = id.split('/').next().unwrap_or_default();
                !nested.contains(base) && !id.match_indices('/').any(|(i, _)| listed.contains(&id[..i])) && seen.insert(id.as_str())
            })
            .cloned()
            .collect()
    }

    /// Index paths of the first node with each of `ids`, found in one walk of the document.
    fn index_paths<'a>(&self, ids: impl Iterator<Item = &'a str>) -> HashMap<String, Vec<usize>> {
        let mut want: HashSet<&str> = ids.collect();
        let mut found = HashMap::new();
        fn visit(nodes: &[Value], acc: &mut Vec<usize>, want: &mut HashSet<&str>, found: &mut HashMap<String, Vec<usize>>) {
            for (i, n) in nodes.iter().enumerate() {
                if want.is_empty() {
                    return;
                }
                acc.push(i);
                if let Some(id) = n["id"].as_str() {
                    if want.remove(id) {
                        found.insert(id.to_string(), acc.clone());
                    }
                }
                if let Some(kids) = n["children"].as_array() {
                    visit(kids, acc, want, found);
                }
                acc.pop();
            }
        }
        visit(self.children(), &mut Vec::new(), &mut want, &mut found);
        found
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
        check_depth(parent_ip.len(), &node)?;
        let kids = self.children_at_mut(&parent_ip);
        let at = index.unwrap_or(kids.len()).min(kids.len());
        kids.insert(at, node);
        Ok(id)
    }

    /// Merge properties into a node; `null` removes a property. Paths with `/`
    /// address descendants of component instances and become overrides.
    pub fn update(&mut self, path: &str, data: &Value) -> Result<(), String> {
        let id = path.split('/').next().unwrap_or_default();
        let ip = self.index_path(id).ok_or_else(|| format!("node not found: {id}"))?;
        self.update_at(ip, path, data)
    }

    /// `update` once the index path of `path`'s node (or of its instance, for a `/` path) is known.
    fn update_at(&mut self, ip: Vec<usize>, path: &str, data: &Value) -> Result<(), String> {
        self.check_update(&ip, path, data)?;
        let props = data.as_object().unwrap();
        if let Some((_, rest)) = path.split_once('/') {
            let desc = ensure_obj(&mut self.node_at_mut(&ip)["descendants"]);
            let entry = desc.entry(rest.to_string()).or_insert_with(|| json!({}));
            merge_props(entry, props, true);
            return Ok(());
        }
        merge_props(self.node_at_mut(&ip), props, false);
        Ok(())
    }

    /// Everything that can make `update_at` fail, checked before it changes anything.
    fn check_update(&self, ip: &[usize], path: &str, data: &Value) -> Result<(), String> {
        data.as_object().ok_or("update data must be an object")?;
        if let Some((inst, _)) = path.split_once('/') {
            if self.node_at(ip)["type"] != "ref" {
                return Err(format!("'{inst}' is not a component instance; '/' paths only address instance descendants"));
            }
            return check_levels(override_levels(ip.len()), data);
        }
        check_depth(ip.len() - 1, data)
    }

    /// `update` for many nodes: finds them all (and the instances of `/` paths) in one walk instead of one walk each.
    /// All or nothing: every item is checked before the first one is applied.
    pub fn update_all(&mut self, items: &[(&str, &Value)]) -> Result<(), String> {
        let paths = self.index_paths(items.iter().map(|(id, _)| id.split('/').next().unwrap_or_default()));
        let mut found = Vec::with_capacity(items.len());
        for (id, data) in items {
            let base = id.split('/').next().unwrap_or_default();
            let ip = paths.get(base).ok_or_else(|| format!("node not found: {base}"))?;
            self.check_update(ip, id, data)?;
            found.push(ip.clone());
        }
        for (ip, (id, data)) in found.into_iter().zip(items) {
            self.update_at(ip, id, data)?;
        }
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

    /// `delete` for many nodes: finds them all in one walk instead of one walk each.
    pub fn delete_all(&mut self, ids: &[String]) -> Result<(), String> {
        if ids.iter().any(|id| id.contains('/')) {
            return Err("cannot delete descendants of component instances; override them with {enabled:false}".into());
        }
        let found = self.index_paths(ids.iter().map(String::as_str));
        let mut paths = ids.iter().map(|id| found.get(id).ok_or_else(|| format!("node not found: {id}"))).collect::<Result<Vec<_>, _>>()?;
        // Last first, so a removal never moves a node that is still to go.
        paths.sort_unstable_by(|a, b| b.cmp(a));
        paths.dedup();
        for ip in paths {
            let (last, parent_ip) = ip.split_last().unwrap();
            self.children_at_mut(parent_ip).remove(*last);
        }
        Ok(())
    }

    /// `move_node` to the end of `parent` for each of `ids` in turn, finding them all in one walk.
    pub fn move_all(&mut self, ids: &[String], parent: &str) -> Result<(), String> {
        if ids.iter().any(|id| id.contains('/')) {
            return Err("cannot move descendants of component instances".into());
        }
        let tip = self.container_path(parent)?;
        let found = self.index_paths(ids.iter().map(String::as_str));
        let mut seen = HashSet::new();
        let mut paths = Vec::new();
        for id in ids.iter().filter(|id| seen.insert(id.as_str())) {
            let ip = found.get(id).ok_or_else(|| format!("node not found: {id}"))?;
            if tip.starts_with(ip) {
                return Err("cannot move a node into itself".into());
            }
            check_depth(tip.len(), self.node_at(ip))?;
            paths.push(ip);
        }
        // Take them out last first (see `delete_all`), then put them back in the order given.
        let mut order: Vec<usize> = (0..paths.len()).collect();
        order.sort_unstable_by(|&a, &b| paths[b].cmp(paths[a]));
        let mut taken: Vec<Option<Value>> = paths.iter().map(|_| None).collect();
        for k in order {
            let (last, parent_ip) = paths[k].split_last().unwrap();
            taken[k] = Some(self.children_at_mut(parent_ip).remove(*last));
        }
        let tip = self.container_path(parent)?;
        self.children_at_mut(&tip).extend(taken.into_iter().flatten());
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
        let tip = self.container_path(&target)?;
        check_depth(tip.len(), self.find(path).unwrap())?;
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
            validate_node(source).map_err(|e| format!("source: {e}"))?;
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
                for (key, over) in &desc {
                    if over.get("type").is_some() {
                        validate_node(over).map_err(|e| format!("descendant '{key}': {e}"))?;
                    }
                }
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
            let inst_ip = self.index_path(inst).ok_or_else(|| format!("node not found: {inst}"))?;
            if self.node_at_mut(&inst_ip)["type"] != "ref" {
                return Err(format!("'{inst}' is not a component instance"));
            }
            check_levels(override_levels(inst_ip.len()), &node)?;
            self.assign_ids(&mut node, &mut Vec::new());
            node["id"] = json!(rest.rsplit('/').next().unwrap());
            let inst_node = self.node_at_mut(&inst_ip);
            ensure_obj(&mut inst_node["descendants"]).insert(rest.to_string(), node);
            return Ok(path.to_string());
        }
        let ip = self.index_path(path).ok_or_else(|| format!("node not found: {path}"))?;
        check_depth(ip.len() - 1, &node)?;
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
        // Axes where some variable has an un-themed entry: that entry is the default, so the axis needs a base theme.
        let mut based: Vec<String> = Vec::new();
        let target = root["variables"].as_object_mut().unwrap();
        for (name, def) in vars {
            if let Some(list) = def["value"].as_array() {
                let mut seen: Vec<&String> = Vec::new();
                for entry in list {
                    if let Some(theme) = entry["theme"].as_object() {
                        for (axis, val) in theme {
                            axes.push((axis.clone(), val.as_str().unwrap_or_default().to_string()));
                            seen.push(axis);
                        }
                    }
                }
                for axis in seen {
                    if list.iter().any(|e| e["theme"].get(axis).is_none()) && !based.contains(axis) {
                        based.push(axis.clone());
                    }
                }
            }
            target.insert(name.clone(), def.clone());
        }
        if !axes.is_empty() {
            let themes = ensure_obj(root.entry("themes").or_insert_with(|| json!({})));
            for axis in based {
                let list = themes.entry(axis).or_insert_with(|| json!([]));
                if list.as_array().is_some_and(Vec::is_empty) {
                    list.as_array_mut().unwrap().push(json!("default"));
                }
            }
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
        check_depth(ip.len(), &node)?;
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

/// Deepest JSON nesting we accept in a document. serde_json refuses documents more
/// than 128 levels deep, so anything deeper would save but never reopen. A node
/// costs two levels (its object and the `children` array it sits in); 122 fits 60
/// nested frames and leaves a few levels of headroom.
const MAX_LEVELS: usize = 122;

/// Levels of JSON nesting in `v` (a scalar has none).
fn json_depth(v: &Value) -> usize {
    match v {
        Value::Object(m) => 1 + m.values().map(json_depth).max().unwrap_or(0),
        Value::Array(a) => 1 + a.iter().map(json_depth).max().unwrap_or(0),
        _ => 0,
    }
}

/// Refuse a value that would sit `above` levels down and push the document past `MAX_LEVELS`.
fn check_levels(above: usize, v: &Value) -> Result<(), String> {
    if above + json_depth(v) > MAX_LEVELS {
        return Err("nesting too deep: reduce how deeply frames, groups and property values are nested".into());
    }
    Ok(())
}

/// Refuse a node (with everything in it) that would sit under `parent_depth` ancestor nodes.
fn check_depth(parent_depth: usize, node: &Value) -> Result<(), String> {
    check_levels(2 + 2 * parent_depth, node)
}

/// Levels above an override of an instance that is `inst_depth` nodes deep: the
/// instance's object and its `descendants` object.
fn override_levels(inst_depth: usize) -> usize {
    2 * inst_depth + 2
}

/// Check a node and, recursively, every child: each must be an object with a valid `type`.
fn validate_node(node: &Value) -> Result<(), String> {
    if !node.is_object() {
        return Err(format!("node must be an object, got {}", node.to_string().chars().take(40).collect::<String>()));
    }
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
    match node.get("children") {
        None | Some(Value::Null) => {}
        Some(Value::Array(kids)) => {
            for (i, k) in kids.iter().enumerate() {
                validate_node(k).map_err(|e| format!("children[{i}] of '{ty}': {e}"))?;
            }
        }
        Some(_) => return Err(format!("`children` of '{ty}' must be an array")),
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

/// `p` made absolute with `.` and `..` folded away, without touching the disk (the folder may not exist yet).
fn lexical(p: &Path) -> PathBuf {
    let p = if p.as_os_str().is_empty() { Path::new(".") } else { p };
    let mut out = PathBuf::new();
    for c in std::path::absolute(p).unwrap_or_else(|_| p.to_path_buf()).components() {
        match c {
            Component::CurDir => {}
            Component::ParentDir => {
                out.pop();
            }
            c => out.push(c),
        }
    }
    out
}

/// The relative reference `url` (resolved against `from`) as written for a document in `to`; None when it is
/// not a relative path (empty, absolute, a `scheme:` url or a `pencil:` id). Another drive gets an absolute path.
fn relocated(url: &str, from: &Path, to: &Path) -> Option<String> {
    let (prefix, url) = url.strip_prefix("file://").map_or(("", url), |rest| ("file://", rest)); // loaders read `file://x` as relative too
    let scheme = url.split_once(':').is_some_and(|(s, _)| !s.is_empty() && s.chars().all(|c| c.is_ascii_alphanumeric() || "+-.".contains(c)));
    if url.is_empty() || scheme || url.starts_with(['/', '\\']) || Path::new(url).is_absolute() {
        return None;
    }
    let full = lexical(&from.join(url));
    let target: Vec<_> = full.components().collect();
    let base: Vec<_> = to.components().collect();
    let common = target.iter().zip(&base).take_while(|(a, b)| a == b).count();
    let names = |cs: &[Component]| cs.iter().map(|c| c.as_os_str().to_string_lossy().to_string()).collect::<Vec<_>>();
    if common == 0 {
        return Some(format!("{prefix}{}", full.to_string_lossy().replace('\\', "/")));
    }
    let rel = vec!["..".to_string(); base.len() - common].into_iter().chain(names(&target[common..])).collect::<Vec<_>>().join("/");
    Some(if rel.starts_with("..") { format!("{prefix}{rel}") } else { format!("{prefix}./{rel}") })
}

fn relocate_in(v: &mut Value, from: &Path, to: &Path) {
    match v {
        Value::Array(a) => a.iter_mut().for_each(|x| relocate_in(x, from, to)),
        Value::Object(o) => {
            let fill = matches!(o.get("type").and_then(Value::as_str), Some("image" | "shader"));
            for (k, x) in o.iter_mut() {
                match x {
                    Value::String(s) if (k == "url" && fill) || k == "scriptUri" => {
                        if let Some(n) = relocated(s, from, to) {
                            *s = n;
                        }
                    }
                    _ => relocate_in(x, from, to),
                }
            }
        }
        _ => {}
    }
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

    fn nested_doc() -> Doc {
        let leaf = |id: &str| json!({"type":"rectangle","id":id});
        Doc::from_json(&json!({"children":[
            {"type":"frame","id":"f","children":[leaf("a"), {"type":"group","id":"g","children":[leaf("b"), leaf("c"), leaf("d")]}, leaf("e")]},
            {"type":"frame","id":"h","children":[leaf("i"), leaf("j")]},
            {"type":"group","id":"k","children":[]}
        ]}).to_string()).unwrap()
    }

    /// The bulk operations are `update` / `delete` / `move_node` over many ids with one walk of the tree.
    #[test]
    fn bulk_operations_match_one_node_at_a_time() {
        let ids = |l: &[&str]| l.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        let (mut one, mut all) = (nested_doc(), nested_doc());
        let props = json!({"opacity": 0.5, "name": null});
        let list = ids(&["j", "b", "f", "e", "nope"]);
        for id in &list[..4] {
            one.update(id, &props).unwrap();
        }
        all.update_all(&list[..4].iter().map(|id| (id.as_str(), &props)).collect::<Vec<_>>()).unwrap();
        assert_eq!(one.root, all.root);
        let kept = all.root.clone();
        assert_eq!(all.update_all(&[("a", &props), ("nope", &props)]).unwrap_err(), "node not found: nope");
        assert!(all.update_all(&[("a", &props), ("e", &json!(5))]).is_err());
        assert!(all.update_all(&[("a", &props), ("e/x", &props)]).is_err());
        assert_eq!(all.root, kept, "a refused update_all changes nothing, not even the items before the bad one");
        // Instance descendants ("inst/child") become overrides, like `update`; a non-instance is refused.
        let with_ref = json!({"children":[{"type":"ref","id":"r","ref":"c"},{"type":"rectangle","id":"a"}]}).to_string();
        let (mut one_i, mut all_i) = (Doc::from_json(&with_ref).unwrap(), Doc::from_json(&with_ref).unwrap());
        let over = json!({"opacity": 0.5});
        one_i.update("r/x", &over).unwrap();
        one_i.update("a", &over).unwrap();
        all_i.update_all(&[("r/x", &over), ("a", &over)]).unwrap();
        assert_eq!(one_i.root, all_i.root);
        assert!(all_i.update_all(&[("a/x", &over)]).is_err());

        for id in ["i", "c", "g", "a"] {
            one.delete(id).unwrap();
        }
        all.delete_all(&ids(&["i", "c", "g", "a"])).unwrap();
        assert_eq!(one.root, all.root);
        assert_eq!(all.delete_all(&ids(&["e", "nope"])).unwrap_err(), "node not found: nope");
        assert!(all.delete_all(&ids(&["f/x"])).is_err());

        let (mut one, mut all) = (nested_doc(), nested_doc());
        for id in ["j", "e", "b", "i"] {
            one.move_node(id, Some("k"), None).unwrap();
        }
        all.move_all(&ids(&["j", "e", "b", "i", "j"]), "k").unwrap();
        assert_eq!(one.root, all.root);
        let kept = all.root.clone();
        assert!(all.move_all(&ids(&["a", "f"]), "g").is_err(), "a node cannot move into its own descendant");
        assert!(all.move_all(&ids(&["a", "nope"]), "k").is_err());
        assert!(all.move_all(&ids(&["a"]), "a").is_err());
        assert_eq!(all.root, kept, "a refused move changes nothing");
    }

    #[test]
    fn outermost_drops_nested_and_repeated_ids() {
        let d = nested_doc();
        let ids = |l: &[&str]| l.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        assert_eq!(d.outermost(&ids(&["c", "g", "i", "c", "i", "f", "b"])), ids(&["i", "f"]));
        assert_eq!(d.outermost(&ids(&["c", "i", "c", "d"])), ids(&["c", "i", "d"]));
        // An instance's rendered descendant goes with the instance or its ancestors, and stays when only unrelated layers are listed.
        assert_eq!(d.outermost(&ids(&["k/x", "f"])), ids(&["k/x", "f"]));
        assert_eq!(d.outermost(&ids(&["g/x", "f"])), ids(&["f"]));
        assert_eq!(d.outermost(&ids(&["g/x", "g"])), ids(&["g"]));
    }

    #[test]
    fn children_are_validated_like_the_top_level_node() {
        let mut d = Doc::new();
        let f = d.insert(ROOT, json!({"type":"frame"}), None).unwrap();
        for bad in [json!("x"), json!(5), json!(null), json!({"type":"nope"}), json!({"name":"x"}), json!({"type":"ref"}), json!({"type":"text","children":[]})] {
            let node = json!({"type":"frame","children":[bad.clone()]});
            let err = d.insert(ROOT, node.clone(), None).unwrap_err();
            assert!(err.contains("children[0]"), "{bad}: {err}");
            assert!(d.replace(&f, node).is_err(), "{bad}");
        }
        let deep = json!({"type":"frame","children":[{"type":"group","children":[{"type":"text"}, "x"]}]});
        assert!(d.insert(ROOT, deep, None).unwrap_err().contains("children[1]"));
        assert!(d.insert(ROOT, json!({"type":"frame","children":"x"}), None).is_err());
        assert_eq!(d.children().len(), 1, "nothing was stored");
        d.insert(ROOT, json!({"type":"frame","children":[{"type":"group","children":[{"type":"text"}]}]}), None).unwrap();
    }

    #[test]
    fn base_value_with_one_override_keeps_a_default_theme() {
        let mut d = Doc::new();
        d.set_variables(&json!({"bg":{"type":"color","value":[{"value":"#fff"},{"value":"#111","theme":{"mode":"dark"}}]}}), false).unwrap();
        assert_eq!(d.root["themes"]["mode"], json!(["default","dark"]));
    }

    /// Everything the doc accepts must reopen: nesting is capped below serde_json's recursion limit.
    #[test]
    fn nesting_is_capped_to_what_reopens() {
        let frame = || json!({"type":"frame","children":[]});
        let mut d = Doc::new();
        let mut parent = ROOT.to_string();
        let mut ids = Vec::new();
        for _ in 0..60 {
            parent = d.insert(&parent, frame(), None).unwrap();
            ids.push(parent.clone());
        }
        let err = d.insert(&parent, frame(), None).unwrap_err();
        assert!(err.contains("nesting too deep"), "{err}");
        assert!(Doc::from_json(&serde_json::to_string_pretty(&d.root).unwrap()).is_ok());
        // Paste/copy, replace and move are held to the same cap.
        assert!(d.copy(&frame(), &parent, None).is_err());
        assert!(d.replace(&parent, json!({"type":"frame","children":[frame()]})).is_err());
        let top = d.insert(ROOT, frame(), None).unwrap();
        assert!(d.move_node(&top, Some(&parent), None).is_err());
        assert!(d.move_node(&ids[0], Some(&top), None).is_err());
        assert!(d.find(&top).is_some() && d.find(&parent).is_some());
        // A whole subtree is measured, not just its root.
        let mut deep = frame();
        for _ in 0..60 {
            deep = json!({"type":"frame","children":[deep]});
        }
        assert!(Doc::new().insert(ROOT, deep, None).is_err());
        // Property values and instance overrides count too, however they get in.
        let mut nest = json!(1);
        for _ in 0..MAX_LEVELS {
            nest = json!([nest]);
        }
        assert!(d.update(&parent, &json!({"effects": nest.clone()})).is_err());
        assert!(d.update(&parent, &json!({"effects": [[1]]})).is_err());
        let mut e = Doc::new();
        let comp = e.insert(ROOT, json!({"type":"frame","reusable":true,"children":[]}), None).unwrap();
        let inst = e.insert(ROOT, json!({"type":"ref","ref":comp}), None).unwrap();
        assert!(e.update(&format!("{inst}/x"), &json!({"effects": nest.clone()})).is_err());
        assert!(e.replace(&format!("{inst}/x"), json!({"type":"frame","children":[],"effects":nest})).is_err());
        let mut over = frame();
        for _ in 0..MAX_LEVELS / 2 {
            over = json!({"type":"frame","children":[over]});
        }
        assert!(e.copy(&json!({"type":"ref","ref":comp}), ROOT, Some(&json!({"descendants":{"x":over}}))).is_err());
        assert!(e.update(&format!("{inst}/x"), &json!({"opacity": 0.5})).is_ok());
    }

    /// A copy's typed descendant overrides are checked like any node, whether it copies a ref or a frame.
    #[test]
    fn copy_validates_descendant_replacements() {
        let mut e = Doc::new();
        let comp = e.insert(ROOT, json!({"type":"frame","reusable":true,"children":[{"type":"text","name":"t"}]}), None).unwrap();
        let bad = json!({"descendants":{"t":{"type":"frame","children":["str"]}}});
        let err = e.copy(&json!({"type":"ref","ref":comp}), ROOT, Some(&bad)).unwrap_err();
        assert!(err.contains("descendant 't'") && err.contains("children[0]"), "{err}");
        let err = e.copy(&json!({"type":"frame","children":[{"type":"text","name":"t"}]}), ROOT, Some(&bad)).unwrap_err();
        assert!(err.contains("descendant 't'"), "{err}");
        assert_eq!(e.children().len(), 1);
    }

    /// A loaded frame can hold a non-object child; copying it errors instead of panicking in `assign_ids`.
    #[test]
    fn copy_validates_source() {
        let mut e = Doc::new();
        let err = e.copy(&json!({"type":"frame","children":["str"]}), ROOT, None).unwrap_err();
        assert!(err.contains("source") && err.contains("children[0]"), "{err}");
        assert_eq!(e.children().len(), 0);
    }

    #[test]
    fn relocate_rewrites_relative_references() {
        let (from, to) = (Path::new("/p/a"), Path::new("/p/b/c"));
        let mut root = json!({"imports": {"lib": "my%20lib.pen", "abs": "/x/l.pen"}, "children": [{"id": "f", "type": "frame", "children": [
            {"fill": {"type": "image", "url": "img/r.png"}},
            {"fill": [{"type": "shader", "url": "./s.glsl"}, {"type": "image", "url": "../up/u.png"}]},
            {"type": "script", "scriptUri": "dots.js"},
            {"type": "browser", "url": "example.com"},
            {"fill": {"type": "image", "url": "https://h/i.png"}},
            {"fill": {"type": "image", "url": "data:image/png;base64,AA=="}},
            {"fill": {"type": "image", "url": "nalarupa:pending-x"}},
            {"fill": {"type": "image", "url": "/abs/i.png"}},
            {"fill": {"type": "image", "url": ""}},
            {"fill": {"type": "image", "url": "file://img/f.png"}},
            {"type": "script", "scriptUri": "file:///abs/s.js"}]}]});
        Doc::relocate(&mut root, from, to);
        assert_eq!(root["imports"], json!({"lib": "../../a/my%20lib.pen", "abs": "/x/l.pen"}));
        let k = &root["children"][0]["children"];
        assert_eq!(k[0]["fill"]["url"], "../../a/img/r.png");
        assert_eq!(k[1]["fill"][0]["url"], "../../a/s.glsl");
        assert_eq!(k[1]["fill"][1]["url"], "../../up/u.png");
        assert_eq!(k[2]["scriptUri"], "../../a/dots.js");
        assert_eq!(k[3]["url"], "example.com");
        for (i, url) in [(4, "https://h/i.png"), (5, "data:image/png;base64,AA=="), (6, "nalarupa:pending-x"), (7, "/abs/i.png"), (8, "")] {
            assert_eq!(k[i]["fill"]["url"], url);
        }
        assert_eq!(k[9]["fill"]["url"], "file://../../a/img/f.png");
        assert_eq!(k[10]["scriptUri"], "file:///abs/s.js");
        // Into a folder below the old one the path points down.
        let mut root = json!({"children": [{"fill": {"type": "image", "url": "./images/x.jpg"}}]});
        Doc::relocate(&mut root, Path::new("/p"), Path::new("/p/sub"));
        assert_eq!(root["children"][0]["fill"]["url"], "../images/x.jpg");
        Doc::relocate(&mut root, Path::new("/p/sub"), Path::new("/p"));
        assert_eq!(root["children"][0]["fill"]["url"], "./images/x.jpg");
        assert!(Doc::same_dir(Path::new("/p/a/../a/"), Path::new("/p/a")));
    }
}
