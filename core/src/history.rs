//! Undo history as patches: a step stores what the edit changed, not a copy of the document.

use crate::engine::same;
use serde_json::{Map, Value};

/// What turns one document into another. Unchanged parts are not stored.
#[derive(Debug)]
pub enum Patch {
    /// Replace the whole value.
    Set(Value),
    /// Per changed key; `None` removes it. `order` restores the key order when keys were added or removed.
    Obj { order: Option<Vec<String>>, keys: Vec<(String, Option<Patch>)> },
    /// Replace `[head, len - tail)`: the slice outright, or element by element when its length is kept.
    Arr { head: usize, tail: usize, mid: Mid },
}

#[derive(Debug)]
pub enum Mid {
    Set(Vec<Value>),
    Each(Vec<Option<Patch>>),
}

/// The patch that changes nothing.
impl Default for Patch {
    fn default() -> Patch {
        Patch::Obj { order: None, keys: Vec::new() }
    }
}

/// The patch that turns `from` into `to`; `None` when they are the same document.
pub fn diff(from: &Value, to: &Value) -> Option<Patch> {
    match (from, to) {
        (Value::Object(a), Value::Object(b)) => {
            let mut keys = Vec::new();
            let mut reshaped = false;
            for (k, w) in b {
                match a.get(k) {
                    Some(v) => keys.extend(diff(v, w).map(|p| (k.clone(), Some(p)))),
                    None => {
                        reshaped = true;
                        keys.push((k.clone(), Some(Patch::Set(w.clone()))));
                    }
                }
            }
            for k in a.keys().filter(|k| !b.contains_key(*k)) {
                reshaped = true;
                keys.push((k.clone(), None));
            }
            let order = reshaped.then(|| b.keys().cloned().collect());
            (!keys.is_empty()).then_some(Patch::Obj { order, keys })
        }
        (Value::Array(a), Value::Array(b)) => {
            let head = a.iter().zip(b).take_while(|(x, y)| same(x, y)).count();
            if head == a.len() && head == b.len() {
                return None;
            }
            let tail = a[head..].iter().rev().zip(b[head..].iter().rev()).take_while(|(x, y)| same(x, y)).count();
            let (am, bm) = (&a[head..a.len() - tail], &b[head..b.len() - tail]);
            let mid = if am.len() == bm.len() { Mid::Each(am.iter().zip(bm).map(|(x, y)| diff(x, y)).collect()) } else { Mid::Set(bm.to_vec()) };
            Some(Patch::Arr { head, tail, mid })
        }
        _ => (!same(from, to)).then(|| Patch::Set(to.clone())),
    }
}

impl Patch {
    /// Apply to `target`; returns the patch that undoes it.
    pub fn apply(self, target: &mut Value) -> Patch {
        match (self, target) {
            (Patch::Set(v), t) => Patch::Set(std::mem::replace(t, v)),
            (Patch::Obj { order, keys }, Value::Object(m)) => {
                let before = order.is_some().then(|| m.keys().cloned().collect());
                let mut inv = Vec::with_capacity(keys.len());
                for (k, p) in keys {
                    match (p, m.get_mut(&k)) {
                        (Some(p), Some(slot)) => inv.push((k, Some(p.apply(slot)))),
                        (Some(Patch::Set(v)), None) => {
                            m.insert(k.clone(), v);
                            inv.push((k, None));
                        }
                        (None, Some(_)) => {
                            let old = m.remove(&k).unwrap_or_default();
                            inv.push((k, Some(Patch::Set(old))));
                        }
                        _ => {}
                    }
                }
                if let Some(order) = order {
                    reorder(m, &order);
                }
                Patch::Obj { order: before, keys: inv }
            }
            (Patch::Arr { head, tail, mid }, Value::Array(a)) => {
                let end = a.len().saturating_sub(tail);
                if head > end {
                    return Patch::default();
                }
                let mid = match mid {
                    Mid::Set(vs) => Mid::Set(a.splice(head..end, vs).collect()),
                    Mid::Each(ps) => Mid::Each(ps.into_iter().zip(&mut a[head..end]).map(|(p, v)| p.map(|p| p.apply(v))).collect()),
                };
                Patch::Arr { head, tail, mid }
            }
            _ => Patch::default(),
        }
    }

    /// Every value the patch stores outright (what the step would write back).
    pub fn values_mut(&mut self, f: &mut impl FnMut(&mut Value)) {
        match self {
            Patch::Set(v) => f(v),
            Patch::Obj { keys, .. } => keys.iter_mut().filter_map(|(_, p)| p.as_mut()).for_each(|p| p.values_mut(f)),
            Patch::Arr { mid: Mid::Set(vs), .. } => vs.iter_mut().for_each(|v| f(v)),
            Patch::Arr { mid: Mid::Each(ps), .. } => ps.iter_mut().flatten().for_each(|p| p.values_mut(f)),
        }
    }
}

fn reorder(m: &mut Map<String, Value>, order: &[String]) {
    let mut rest = std::mem::take(m);
    for k in order {
        if let Some(v) = rest.remove(k) {
            m.insert(k.clone(), v);
        }
    }
    m.extend(rest);
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// `diff` then `apply` reaches the target, and the returned patch comes back.
    fn round_trip(a: Value, b: Value) {
        let patch = diff(&a, &b).expect("documents differ");
        let mut v = a.clone();
        let inv = patch.apply(&mut v);
        assert_eq!(v, b);
        inv.apply(&mut v);
        assert_eq!(v, a);
    }

    #[test]
    fn patches_round_trip() {
        let n = |i: i64| json!({"id": format!("n{i}"), "x": i, "fill": "#f00"});
        let doc = json!({"version": 1, "children": [n(0), n(1), n(2), n(3)]});
        let mut edited = doc.clone();
        edited["children"][1]["x"] = json!(50);
        round_trip(doc.clone(), edited);
        let mut added = doc.clone();
        added["children"].as_array_mut().unwrap().insert(0, n(9));
        round_trip(doc.clone(), added);
        let mut removed = doc.clone();
        removed["children"].as_array_mut().unwrap().remove(2);
        round_trip(doc.clone(), removed);
        let mut moved = doc.clone();
        let c = moved["children"].as_array_mut().unwrap();
        let first = c.remove(0);
        c.push(first);
        round_trip(doc.clone(), moved);
        let mut keys = doc.clone();
        keys["children"][2].as_object_mut().unwrap().remove("fill");
        keys["children"][3]["stroke"] = json!({"width": 2});
        keys["name"] = json!("x");
        round_trip(doc.clone(), keys);
        round_trip(doc, json!({"children": 5}));
    }

    #[test]
    fn restores_key_order() {
        let a = json!({"id": "a", "x": 1, "y": 2, "w": 3});
        let b = json!({"id": "a", "w": 3, "z": 9});
        let mut v = a.clone();
        let inv = diff(&a, &b).unwrap().apply(&mut v);
        assert_eq!(v.as_object().unwrap().keys().collect::<Vec<_>>(), ["id", "w", "z"]);
        inv.apply(&mut v);
        assert_eq!(v.as_object().unwrap().keys().collect::<Vec<_>>(), ["id", "x", "y", "w"]);
    }

    #[test]
    fn a_rewritten_number_is_no_change() {
        assert!(diff(&json!({"x": 10}), &json!({"x": 10.0})).is_none());
        assert!(diff(&json!({"c": [{"x": 1}]}), &json!({"c": [{"x": 1.0}]})).is_none());
        assert!(diff(&json!({"x": 10}), &json!({"x": 10.5})).is_some());
    }

    #[test]
    fn one_edit_in_a_big_document_stores_little() {
        let kids: Vec<Value> = (0..5000).map(|i| json!({"id": format!("n{i}"), "name": format!("r{i}"), "x": i, "width": 20, "fill": "#f00"})).collect();
        let a = json!({"children": [{"id": "f", "children": kids}]});
        let mut b = a.clone();
        b["children"][0]["children"][5]["x"] = json!(-1);
        let patch = diff(&b, &a).unwrap();
        assert!(format!("{patch:?}").len() < 400, "{patch:?}");
    }
}
