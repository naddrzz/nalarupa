//! Direct-manipulation editing behind the canvas UI: snap targets,
//! marquee hits, drop targets, reparenting that keeps the on-screen position, wrap/unwrap,
//! flex-layout inference (Shift+A), align, duplicate and the node clipboard.

use crate::doc::{random_u64, walk, Doc, ROOT};
use crate::engine::Engine;
use crate::render::{unapply, Flat};
use base64::Engine as _;
use serde_json::{json, Map, Value};
use std::collections::{HashMap, HashSet};
use std::sync::OnceLock;

/// (x, y, w, h) in document (world) units.
pub type Rect = (f64, f64, f64, f64);

/// Integral values stay integers in the saved document; others keep two decimals.
fn num(v: f64) -> Value {
    let r = (v * 100.0).round() / 100.0;
    if r.fract() == 0.0 && r.abs() < 1e15 { json!(r as i64) } else { json!(r) }
}

fn overlaps(a: Rect, b: Rect) -> bool {
    a.0 <= b.0 + b.2 && b.0 <= a.0 + a.2 && a.1 <= b.1 + b.3 && b.1 <= a.1 + a.3
}

fn contains(outer: Rect, inner: Rect) -> bool {
    inner.0 >= outer.0 && inner.1 >= outer.1 && inner.0 + inner.2 <= outer.0 + outer.2 && inner.1 + inner.3 <= outer.1 + outer.3
}

fn union(boxes: impl Iterator<Item = Rect>) -> Option<Rect> {
    boxes.reduce(|a, b| {
        let (x0, y0) = (a.0.min(b.0), a.1.min(b.1));
        (x0, y0, (a.0 + a.2).max(b.0 + b.2) - x0, (a.1 + a.3).max(b.1 + b.3) - y0)
    })
}

fn kids<'a>(doc: &'a Doc, id: &str) -> &'a [Value] {
    if id == ROOT {
        return doc.children();
    }
    doc.find(id).and_then(|n| n["children"].as_array()).map(Vec::as_slice).unwrap_or(&[])
}

/// Drop ids whose ancestor is also listed (moving a parent moves its subtree).
fn top_only(doc: &Doc, ids: &[String]) -> Vec<String> {
    let set: HashSet<&str> = ids.iter().map(String::as_str).collect();
    let mut out: Vec<String> = Vec::new();
    for id in ids {
        let mut p = doc.parent_of(id).map(|p| p.0);
        let mut nested = false;
        while let Some(pid) = p.filter(|p| p != ROOT) {
            if set.contains(pid.as_str()) {
                nested = true;
                break;
            }
            p = doc.parent_of(&pid).map(|p| p.0);
        }
        if !nested && !out.contains(id) {
            out.push(id.clone());
        }
    }
    out
}

/// Read-only view of the document and its laid-out boxes (the engine must be synced).
struct View<'a> {
    doc: &'a Doc,
    flats: Vec<&'a Flat>,
    by_id: HashMap<&'a str, (usize, &'a Flat)>,
}

impl<'a> View<'a> {
    fn new(e: &'a Engine) -> View<'a> {
        let flats: Vec<&Flat> = e.renderer.flat().collect();
        let by_id = flats.iter().enumerate().map(|(i, &f)| (f.id.as_str(), (i, f))).collect();
        View { doc: &e.doc, flats, by_id }
    }

    fn flat(&self, id: &str) -> Option<&'a Flat> {
        self.by_id.get(id).map(|x| x.1)
    }

    fn rect(&self, id: &str) -> Option<Rect> {
        self.flat(id).map(|f| (f.x, f.y, f.w, f.h))
    }

    /// Bounds of what the node paints (its box through its rotation/flip matrix), on the canvas
    /// or, with `frame` (a container's matrix), in that container's own coordinates.
    fn painted(&self, id: &str, frame: Option<[f64; 6]>) -> Option<Rect> {
        let pts = self.corners(id, frame)?;
        union(pts.into_iter().map(|p| (p.0, p.1, 0.0, 0.0)))
    }

    /// Corners of the painted box (top-left, top-right, bottom-left, bottom-right), see `painted`.
    fn corners(&self, id: &str, frame: Option<[f64; 6]>) -> Option<[(f64, f64); 4]> {
        let f = self.flat(id)?;
        Some([(0.0, 0.0), (f.w, 0.0), (0.0, f.h), (f.w, f.h)].map(|(dx, dy)| {
            let (x, y) = (f.x + dx, f.y + dy);
            let (x, y) = f.m.map_or((x, y), |m| (m[0] * x + m[2] * y + m[4], m[1] * x + m[3] * y + m[5]));
            frame.map_or((x, y), |m| unapply(m, x, y))
        }))
    }

    /// Whether the painted box touches `r` (separating-axis test, so a turned box does not
    /// count through the corners of its bounds).
    fn touches(&self, id: &str, r: Rect) -> bool {
        let Some(c) = self.corners(id, None) else { return false };
        let rc = [(r.0, r.1), (r.0 + r.2, r.1), (r.0, r.1 + r.3), (r.0 + r.2, r.1 + r.3)];
        let b = union(c.into_iter().map(|p| (p.0, p.1, 0.0, 0.0))).unwrap();
        if !overlaps(b, r) {
            return false;
        }
        [(c[1].0 - c[0].0, c[1].1 - c[0].1), (c[2].0 - c[0].0, c[2].1 - c[0].1)].into_iter().flat_map(|a| [a, (-a.1, a.0)]).filter(|a| a.0 != 0.0 || a.1 != 0.0).all(|a| {
            let span = |pts: &[(f64, f64); 4]| pts.iter().map(|p| p.0 * a.0 + p.1 * a.1).fold((f64::MAX, f64::MIN), |s, d| (s.0.min(d), s.1.max(d)));
            let (q, w) = (span(&c), span(&rc));
            // Tolerance for rounding in the rotation matrix (an exact touch counts).
            q.0 <= w.1 + 1e-9 && w.0 <= q.1 + 1e-9
        })
    }

    fn ty(&self, id: &str) -> &'a str {
        self.doc.find(id).and_then(|n| n["type"].as_str()).unwrap_or("")
    }

    /// Main axis of a frame with auto layout (0 = horizontal, 1 = vertical), like layout.rs.
    fn layout(&self, id: &str) -> Option<usize> {
        let n = self.doc.find(id)?;
        if n["type"] != "frame" {
            return None;
        }
        match n["layout"].as_str() {
            Some("none") => None,
            Some("vertical") => Some(1),
            _ => Some(0),
        }
    }

    /// Placed by its parent's auto layout (x/y are ignored).
    fn in_flow(&self, id: &str) -> bool {
        let absolute = self.doc.find(id).is_some_and(|n| n["layoutPosition"] == "absolute");
        !absolute && self.doc.parent_of(id).is_some_and(|(p, _)| self.layout(&p).is_some())
    }

    fn accepts_children(&self, id: &str) -> bool {
        id == ROOT || (!id.contains('/') && matches!(self.ty(id), "frame" | "group"))
    }

    /// World position of the node's own origin (what its children's x/y are relative to).
    /// Groups are the union of their (turned) children, so their origin sits before the box, where layout put it.
    fn origin(&self, id: &str) -> Option<(f64, f64)> {
        if id == ROOT {
            return Some((0.0, 0.0));
        }
        self.doc.find(id)?;
        let f = self.flat(id)?;
        Some((f.x - f.local.0[0], f.y - f.local.0[1]))
    }

    fn paint_order(&self, ids: &[String]) -> Vec<String> {
        let mut v = ids.to_vec();
        v.sort_by_key(|id| self.by_id.get(id.as_str()).map_or(usize::MAX, |x| x.0));
        v
    }

    fn union(&self, ids: &[String]) -> Option<Rect> {
        union(ids.iter().filter_map(|id| self.rect(id)))
    }

    /// `fill_container` collapses outside auto layout: freeze it at the current size.
    fn freeze_fill(&self, id: &str) -> Map<String, Value> {
        let mut m = Map::new();
        if let (Some(n), Some(f)) = (self.doc.find(id), self.flat(id)) {
            for (k, v) in [("width", f.w), ("height", f.h)] {
                if n[k].as_str().is_some_and(|s| s.starts_with("fill_container")) {
                    m.insert(k.into(), num(v));
                }
            }
        }
        m
    }
}

/// Snap candidates for dragging `ids`: the parent frame and the siblings (groups recursed),
/// limited to `view` when given.
pub fn snap_targets(e: &Engine, ids: &[String], view: Option<Rect>) -> Vec<Value> {
    let v = View::new(e);
    let Some((parent, _)) = ids.first().and_then(|id| e.doc.parent_of(id)) else { return Vec::new() };
    let skip: HashSet<&str> = ids.iter().map(String::as_str).collect();
    let mut out: Vec<&Flat> = Vec::new();
    if v.ty(&parent) == "frame" {
        out.extend(v.flat(&parent));
    }
    fn siblings<'a>(v: &View<'a>, nodes: &[Value], skip: &HashSet<&str>, out: &mut Vec<&'a Flat>) {
        for n in nodes {
            let id = n["id"].as_str().unwrap_or_default();
            if skip.contains(id) {
                continue;
            }
            if n["type"] == "group" {
                siblings(v, n["children"].as_array().map(Vec::as_slice).unwrap_or(&[]), skip, out);
            } else {
                out.extend(v.flat(id));
            }
        }
    }
    siblings(&v, kids(&e.doc, &parent), &skip, &mut out);
    out.into_iter()
        .filter(|f| view.is_none_or(|r| overlaps((f.x, f.y, f.w, f.h), r)))
        .map(|f| json!({"id": f.id, "x": f.x, "y": f.y, "w": f.w, "h": f.h, "m": f.m}))
        .collect()
}

/// Union of what `ids` paint on the canvas (their boxes through rotation/flip).
pub fn painted_bounds(e: &Engine, ids: &[String]) -> Option<Rect> {
    let v = View::new(e);
    union(ids.iter().filter_map(|id| v.painted(id, None)))
}

/// Marquee hits (touching counts). Containers fully inside the rectangle are picked whole;
/// top-level frames are entered (artboards); `deep` (Ctrl) picks leaves everywhere.
pub fn nodes_in_rect(e: &Engine, r: Rect, deep: bool) -> Vec<String> {
    fn rec(v: &View, nodes: &[Value], top: bool, r: Rect, deep: bool, out: &mut Vec<String>) {
        for n in nodes {
            let Some(id) = n["id"].as_str() else { continue };
            let Some(b) = v.painted(id, None) else { continue };
            if !v.touches(id, r) {
                continue;
            }
            let ty = n["type"].as_str().unwrap_or("");
            let container = matches!(ty, "frame" | "group");
            let children = n["children"].as_array().map(Vec::as_slice).unwrap_or(&[]);
            if container && contains(r, b) {
                out.push(id.to_string());
            } else if container && (deep || (top && ty == "frame" && !children.is_empty())) {
                rec(v, children, false, r, deep, out);
            } else {
                out.push(id.to_string());
            }
        }
    }
    let v = View::new(e);
    let mut out = Vec::new();
    rec(&v, e.doc.children(), true, r, deep, &mut out);
    out
}

/// Where a drag of a `w`×`h` selection would land with the pointer at (x, y): the topmost,
/// deepest frame under the pointer (not one being dragged) that is big enough for it (`any`
/// skips the size rule), else the document. Auto-layout targets also get the insertion
/// `index` (children minus the dragged ones) and the indicator `line` [x1, y1, x2, y2].
#[allow(clippy::too_many_arguments)]
pub fn drop_target(e: &Engine, x: f64, y: f64, w: f64, h: f64, ignore: &[String], any: bool, parent: Option<&str>) -> Value {
    let v = View::new(e);
    let mut ign: HashSet<&str> = HashSet::new();
    for id in ignore {
        if let Some(n) = e.doc.find(id) {
            walk(std::slice::from_ref(n), &mut |k| {
                ign.insert(k["id"].as_str().unwrap_or_default());
            });
        }
    }
    let frame_ok = |f: &Flat| !f.id.contains('/') && !ign.contains(f.id.as_str()) && v.ty(&f.id) == "frame";
    let inside = |f: &Flat| x >= f.x && y >= f.y && x <= f.x + f.w && y <= f.y + f.h;
    let target = match parent {
        Some(p) => v.flat(p),
        None => {
            // Pre-order is paint order: the last frame under the pointer is the topmost, deepest one.
            let mut best = v.flats.iter().copied().rev().find(|f| frame_ok(f) && inside(f));
            while let Some(f) = best.filter(|f| !any && f.parent.is_some() && (f.w < w || f.h < h)) {
                let mut up = f.parent.as_deref().and_then(|p| v.flat(p));
                while let Some(p) = up.filter(|p| !frame_ok(p)) {
                    up = p.parent.as_deref().and_then(|q| v.flat(q));
                }
                best = up;
            }
            best
        }
    };
    let Some(t) = target else { return json!({"id": ROOT, "layout": "none"}) };
    let dir = v.layout(&t.id);
    let mut out = json!({"id": t.id, "x": t.x, "y": t.y, "w": t.w, "h": t.h,
                         "layout": match dir { Some(0) => "horizontal", Some(_) => "vertical", None => "none" }});
    if let Some(a) = dir {
        let lo = |f: &Flat| if a == 0 { f.x } else { f.y };
        let hi = |f: &Flat| if a == 0 { f.x + f.w } else { f.y + f.h };
        let p = if a == 0 { x } else { y };
        let siblings: Vec<&Value> = kids(&e.doc, &t.id).iter().filter(|k| !ign.contains(k["id"].as_str().unwrap_or_default())).collect();
        let (mut index, mut prev, mut next) = (siblings.len(), None, None);
        for (i, k) in siblings.iter().enumerate() {
            let Some(f) = v.flat(k["id"].as_str().unwrap_or_default()).filter(|_| k["layoutPosition"] != "absolute") else { continue };
            if (lo(f) + hi(f)) / 2.0 > p {
                (index, next) = (i, Some(f));
                break;
            }
            prev = Some(f);
        }
        let pos = match (prev, next) {
            (None, None) => (lo(t) + hi(t)) / 2.0,
            (None, Some(n)) => (lo(t) + lo(n)) / 2.0,
            (Some(p), None) => (hi(p) + hi(t)) / 2.0,
            (Some(p), Some(n)) => (hi(p) + lo(n)) / 2.0,
        };
        out["index"] = json!(index);
        out["line"] = json!(if a == 0 { [pos, t.y, pos, t.y + t.h] } else { [t.x, pos, t.x + t.w, pos] });
    }
    out
}

/// Move `ids` under `parent` (at `index` among its other children, else on top) keeping
/// their position on the canvas. Same parent without an index is a no-op.
pub fn reparent(e: &mut Engine, ids: &[String], parent: &str, index: Option<usize>) -> Result<(), String> {
    e.sync();
    let plan: Vec<(String, Map<String, Value>)> = {
        let v = View::new(e);
        if !v.accepts_children(parent) {
            return Err(format!("'{parent}' cannot contain other layers"));
        }
        let po = v.origin(parent).unwrap_or((0.0, 0.0));
        let flow = v.layout(parent).is_some();
        v.paint_order(&top_only(&e.doc, ids))
            .into_iter()
            .filter(|id| !id.contains('/') && id != parent && e.doc.find(id).is_some())
            .filter(|id| index.is_some() || e.doc.parent_of(id).is_none_or(|p| p.0 != parent))
            .map(|id| {
                let absolute = e.doc.find(&id).is_some_and(|n| n["layoutPosition"] == "absolute");
                let mut props = if flow { Map::new() } else { v.freeze_fill(&id) };
                if let (Some(o), false) = (v.origin(&id), flow && !absolute) {
                    props.insert("x".into(), num(o.0 - po.0));
                    props.insert("y".into(), num(o.1 - po.1));
                }
                (id, props)
            })
            .collect()
    };
    let parent = parent.to_string();
    e.edit_or_drag(|d| {
        for (id, _) in &plan {
            d.move_node(id, Some(&parent), None)?;
        }
        if let Some(i) = index {
            for (k, (id, _)) in plan.iter().enumerate() {
                d.move_node(id, Some(&parent), Some(i + k))?;
            }
        }
        plan.iter().try_for_each(|(id, props)| d.update(id, &Value::Object(props.clone())))
    })
}

/// Wrap the selection in a new group or (unfilled, freeform) frame placed above the topmost
/// selected layer, keeping everything where it is (Ctrl+G / Ctrl+Alt+G).
pub fn wrap(e: &mut Engine, ids: &[String], kind: &str) -> Result<String, String> {
    e.sync();
    let (parent, at, node, moves) = {
        let v = View::new(e);
        let ids: Vec<String> = v.paint_order(&top_only(&e.doc, ids)).into_iter().filter(|id| !id.contains('/') && v.rect(id).is_some()).collect();
        let last = ids.last().ok_or("select layers to wrap")?;
        let (parent, at) = e.doc.parent_of(last).ok_or("node not found")?;
        let u = v.union(&ids).ok_or("nothing visible to wrap")?;
        let po = v.origin(&parent).unwrap_or((0.0, 0.0));
        let mut node = if kind == "frame" {
            json!({"type": "frame", "name": "Frame", "width": num(u.2), "height": num(u.3), "layout": "none"})
        } else {
            json!({"type": "group", "name": "Group"})
        };
        node["x"] = num(u.0 - po.0);
        node["y"] = num(u.1 - po.1);
        let moves: Vec<(String, Map<String, Value>)> = ids.iter().map(|id| {
            let mut props = v.freeze_fill(id);
            let o = v.origin(id).unwrap_or((u.0, u.1));
            props.insert("x".into(), num(o.0 - u.0));
            props.insert("y".into(), num(o.1 - u.1));
            (id.clone(), props)
        }).collect();
        (parent, at, node, moves)
    };
    e.edit_or_drag(|d| {
        let id = d.insert(&parent, node, Some(at + 1))?;
        for (k, props) in &moves {
            d.move_node(k, Some(&id), None)?;
            d.update(k, &Value::Object(props.clone()))?;
        }
        Ok(id)
    })
}

/// Replace groups/frames by their children, in place (Ctrl+Shift+G). Returns the new selection.
pub fn unwrap(e: &mut Engine, ids: &[String]) -> Result<Vec<String>, String> {
    e.sync();
    type Plan = Vec<(String, Vec<(String, Map<String, Value>)>)>;
    let (plan, rest): (Plan, Vec<String>) = {
        let v = View::new(e);
        let mut rest = Vec::new();
        let plan = top_only(&e.doc, ids).into_iter().filter_map(|id| {
            let container = !id.contains('/') && matches!(v.ty(&id), "frame" | "group");
            let Some((parent, _)) = e.doc.parent_of(&id).filter(|_| container) else {
                rest.push(id);
                return None;
            };
            let (po, co) = (v.origin(&parent).unwrap_or((0.0, 0.0)), v.origin(&id).unwrap_or((0.0, 0.0)));
            let flow = v.layout(&parent).is_some();
            let moves = kids(&e.doc, &id).iter().filter_map(|k| {
                let kid = k["id"].as_str()?.to_string();
                let mut props = if flow { Map::new() } else { v.freeze_fill(&kid) };
                let o = v.origin(&kid).unwrap_or((co.0 + k["x"].as_f64().unwrap_or(0.0), co.1 + k["y"].as_f64().unwrap_or(0.0)));
                props.insert("x".into(), num(o.0 - po.0));
                props.insert("y".into(), num(o.1 - po.1));
                Some((kid, props))
            }).collect();
            Some((id, moves))
        }).collect();
        (plan, rest)
    };
    if plan.is_empty() {
        return Err("select a group or frame to ungroup".into());
    }
    e.edit_or_drag(|d| {
        let mut out = rest.clone();
        for (cid, moves) in &plan {
            let (parent, at) = d.parent_of(cid).ok_or("node not found")?;
            for (k, (kid, props)) in moves.iter().enumerate() {
                d.move_node(kid, Some(&parent), Some(at + 1 + k))?;
                d.update(kid, &Value::Object(props.clone()))?;
                out.push(kid.clone());
            }
            d.delete(cid)?;
        }
        Ok(out)
    })
}

/// Shift+A guess for a freeform frame's children.
#[derive(Debug, PartialEq)]
pub struct Flex {
    /// 0 = horizontal, 1 = vertical.
    pub dir: usize,
    pub gap: f64,
    pub align: &'static str,
    pub justify: Option<&'static str>,
    /// [top, right, bottom, left]
    pub padding: [f64; 4],
    /// Child indices in layout order.
    pub order: Vec<usize>,
    /// Children that fill the cross axis (then the frame keeps a fixed cross size).
    pub fill: Vec<usize>,
}

/// Infer direction, gap, alignment, padding and cross-axis fills from the children's boxes.
/// `frame` is None when a new frame is wrapped around them.
pub fn infer_flex(frame: Option<Rect>, kids: &[(Rect, bool)]) -> Flex {
    let mut f = Flex { dir: 1, gap: 10.0, align: "start", justify: None, padding: [10.0; 4], order: (0..kids.len()).collect(), fill: Vec::new() };
    let Some(r) = union(kids.iter().map(|k| k.0)) else { return f };
    let edges = |fr: Rect| [r.1 - fr.1, fr.0 + fr.2 - r.0 - r.2, fr.1 + fr.3 - r.1 - r.3, r.0 - fr.0].map(|p| p.max(0.0).round());
    if kids.len() == 1 && kids[0].1 {
        (f.align, f.justify) = ("center", Some("center"));
        if let Some(p) = frame.map(edges) {
            let (v, h) = (p[0].min(p[2]), p[1].min(p[3]));
            f.padding = [v, h, v, h];
        }
        return f;
    }
    let (sum_w, sum_h): (f64, f64) = (kids.iter().map(|k| k.0 .2).sum(), kids.iter().map(|k| k.0 .3).sum());
    let (over_x, over_y) = (sum_w > r.2 + 0.5, sum_h > r.3 + 0.5);
    let range = |c: &dyn Fn(Rect) -> f64| {
        let vals: Vec<f64> = kids.iter().map(|k| c(k.0)).collect();
        vals.iter().copied().fold(f64::MIN, f64::max) - vals.iter().copied().fold(f64::MAX, f64::min)
    };
    f.dir = if !over_x && over_y {
        0
    } else if !over_y && over_x {
        1
    } else if range(&|b| b.0 + b.2 / 2.0) > range(&|b| b.1 + b.3 / 2.0) {
        0
    } else {
        1
    };
    let (e, c) = (f.dir, 1 - f.dir);
    let lo = |b: Rect, a: usize| if a == 0 { b.0 } else { b.1 };
    let size = |b: Rect, a: usize| if a == 0 { b.2 } else { b.3 };
    f.order.sort_by(|&i, &j| lo(kids[i].0, e).total_cmp(&lo(kids[j].0, e)));
    let mut gaps: Vec<f64> = f.order.windows(2).map(|w| lo(kids[w[1]].0, e) - lo(kids[w[0]].0, e) - size(kids[w[0]].0, e)).filter(|g| *g > 0.0).collect();
    gaps.sort_by(f64::total_cmp);
    if !gaps.is_empty() {
        f.gap = gaps[gaps.len() / 2].round();
    }
    let dev = |pick: &dyn Fn(Rect) -> f64| kids.iter().map(|k| (pick(k.0) - pick(r)).abs()).sum::<f64>();
    let devs = [dev(&|b| lo(b, c)), dev(&|b| lo(b, c) + size(b, c) / 2.0), dev(&|b| lo(b, c) + size(b, c))];
    let best = (0..3).fold(0, |m, i| if devs[i] < devs[m] { i } else { m });
    f.align = ["start", "center", "end"][best];
    let full: Vec<usize> = (0..kids.len()).filter(|&i| (size(kids[i].0, c) - size(r, c)).abs() <= 0.5).collect();
    if full.len() >= 2 {
        f.fill = full;
    }
    f.padding = match frame {
        Some(fr) => edges(fr),
        None if kids.len() == 1 => [10.0; 4],
        None => [0.0; 4],
    };
    f
}

fn padding_value(p: [f64; 4]) -> Value {
    if p.iter().all(|v| *v == p[0]) {
        num(p[0])
    } else if p[0] == p[2] && p[1] == p[3] {
        json!([num(p[0]), num(p[1])])
    } else {
        json!(p.map(num))
    }
}

/// Shift+A: give a freeform frame auto layout in place, or wrap the selection in a new
/// auto-layout frame. Returns the frame id.
pub fn add_layout(e: &mut Engine, ids: &[String]) -> Result<String, String> {
    let in_place = ids.len() == 1 && e.doc.find(&ids[0]).is_some_and(|n| n["type"] == "frame" && n["layout"] == "none");
    e.batch(|e| {
        let frame = if in_place { ids[0].clone() } else { wrap(e, ids, "frame")? };
        e.sync();
        let (frame_props, kid_props, order) = {
            let v = View::new(e);
            let fr = v.rect(&frame).ok_or("frame not found")?;
            let shown: Vec<(String, Rect, bool)> = kids(&e.doc, &frame).iter().filter_map(|k| {
                let id = k["id"].as_str()?;
                Some((id.to_string(), v.rect(id)?, k["type"] == "text"))
            }).collect();
            let boxes: Vec<(Rect, bool)> = shown.iter().map(|s| (s.1, s.2)).collect();
            let f = infer_flex(in_place.then_some(fr), &boxes);
            let c = if f.dir == 0 { "height" } else { "width" };
            let mut fp = json!({"layout": if f.dir == 0 { "horizontal" } else { "vertical" }, "gap": num(f.gap),
                                "padding": padding_value(f.padding), "width": "fit_content", "height": "fit_content"});
            if f.align != "start" {
                fp["alignItems"] = json!(f.align);
            }
            if let Some(j) = f.justify {
                fp["justifyContent"] = json!(j);
            }
            if !f.fill.is_empty() {
                fp[c] = num(if c == "width" { fr.2 } else { fr.3 });
            }
            if !in_place && boxes.len() == 1 {
                let o = v.doc.find(&frame).map(|n| (n["x"].as_f64().unwrap_or(0.0), n["y"].as_f64().unwrap_or(0.0))).unwrap_or_default();
                (fp["x"], fp["y"]) = (num(o.0 - f.padding[3]), num(o.1 - f.padding[0]));
            }
            let kp: Vec<(String, Value)> = f.fill.iter().map(|&i| {
                let (id, b, text) = &shown[i];
                let mut p = json!({});
                p[c] = json!("fill_container");
                if *text {
                    p["textGrowth"] = json!(if c == "width" { "fixed-width" } else { "fixed-width-height" });
                    if c == "height" {
                        p["width"] = num(b.2);
                    }
                }
                (id.clone(), p)
            }).collect();
            let order: Vec<String> = f.order.iter().map(|&i| shown[i].0.clone()).collect();
            (fp, kp, order)
        };
        e.edit_or_drag(|d| {
            d.update(&frame, &frame_props)?;
            for (k, id) in order.iter().enumerate() {
                d.move_node(id, Some(&frame), Some(k))?;
            }
            kid_props.iter().try_for_each(|(id, p)| d.update(id, p))?;
            Ok(frame.clone())
        })
    })
}

/// Shift+Alt+A: back to freeform, children staying where the layout put them.
pub fn remove_layout(e: &mut Engine, ids: &[String]) -> Result<(), String> {
    e.sync();
    let plan: Vec<(String, Map<String, Value>)> = {
        let v = View::new(e);
        let mut plan = Vec::new();
        for id in ids.iter().filter(|id| v.layout(id).is_some()) {
            let (Some(fo), Some(fr)) = (v.origin(id), v.rect(id)) else { continue };
            let mut fp = Map::new();
            fp.insert("layout".into(), json!("none"));
            fp.insert("width".into(), num(fr.2));
            fp.insert("height".into(), num(fr.3));
            plan.push((id.clone(), fp));
            for k in kids(&e.doc, id) {
                let kid = k["id"].as_str().unwrap_or_default();
                let mut p = v.freeze_fill(kid);
                if let Some(o) = v.origin(kid).filter(|_| k["layoutPosition"] != "absolute") {
                    p.insert("x".into(), num(o.0 - fo.0));
                    p.insert("y".into(), num(o.1 - fo.1));
                }
                plan.push((kid.to_string(), p));
            }
        }
        plan
    };
    e.edit_or_drag(|d| plan.iter().try_for_each(|(id, p)| d.update(id, &Value::Object(p.clone()))))
}

/// Align (left, hcenter, right, top, vcenter, bottom). One layer aligns to its parent (a
/// top-level frame aligns its children inside it); several align to their union. Layers
/// placed by auto layout are skipped. Edges and centers are those of the painted (rotated,
/// flipped) bounds.
pub fn align(e: &mut Engine, ids: &[String], dir: &str) -> Result<(), String> {
    e.sync();
    let moves: Vec<(String, f64, f64)> = {
        let v = View::new(e);
        let free: Vec<String> = top_only(&e.doc, ids).into_iter().filter(|id| !id.contains('/') && !v.in_flow(id) && v.rect(id).is_some()).collect();
        // `frame`: the container whose own coordinates the alignment happens in (none = canvas).
        let (targets, to, frame): (Vec<String>, Option<Rect>, Option<[f64; 6]>) = match free.as_slice() {
            [one] => match e.doc.parent_of(one) {
                Some((p, _)) if p != ROOT => (free.clone(), v.rect(&p), v.flat(&p).and_then(|f| f.m)),
                _ => (kids(&e.doc, one).iter().filter_map(|k| k["id"].as_str()).filter(|k| !v.in_flow(k) && v.rect(k).is_some()).map(str::to_string).collect(), v.rect(one), v.flat(one).and_then(|f| f.m)),
            },
            _ => (free.clone(), union(free.iter().filter_map(|id| v.painted(id, None))), None),
        };
        let Some(to) = to else { return Ok(()) };
        targets.into_iter().filter_map(|id| {
            let b = v.painted(&id, frame)?;
            let (dx, dy) = match dir {
                "left" => (to.0 - b.0, 0.0),
                "hcenter" => (to.0 + to.2 / 2.0 - b.0 - b.2 / 2.0, 0.0),
                "right" => (to.0 + to.2 - b.0 - b.2, 0.0),
                "top" => (0.0, to.1 - b.1),
                "vcenter" => (0.0, to.1 + to.3 / 2.0 - b.1 - b.3 / 2.0),
                "bottom" => (0.0, to.1 + to.3 - b.1 - b.3),
                _ => return None,
            };
            // A canvas delta becomes a step in the node's parent space (x/y are relative to it).
            let (dx, dy) = match v.flat(&id).and_then(|f| f.parent.as_deref()).and_then(|p| v.flat(p)).and_then(|p| p.m).filter(|_| frame.is_none()) {
                Some(m) => {
                    let o = unapply(m, 0.0, 0.0);
                    let d = unapply(m, dx, dy);
                    (d.0 - o.0, d.1 - o.1)
                }
                None => (dx, dy),
            };
            (dx.abs() > 1e-9 || dy.abs() > 1e-9).then_some((id, dx, dy))
        }).collect()
    };
    e.edit_or_drag(|d| moves.iter().try_for_each(|(id, dx, dy)| {
        let n = d.find(id).ok_or("node not found")?;
        let (x, y) = (n["x"].as_f64().unwrap_or(0.0), n["y"].as_f64().unwrap_or(0.0));
        d.update(id, &json!({"x": num(x + dx), "y": num(y + dy)}))
    }))
}

/// Ctrl+D: copies right above their sources at the same position. A single top-level frame
/// goes into free space on its right instead, unless `in_place` (Alt-drag duplicate).
pub fn duplicate(e: &mut Engine, ids: &[String], in_place: bool) -> Result<Vec<String>, String> {
    e.sync();
    let ids = View::new(e).paint_order(&top_only(&e.doc, ids));
    let right = match ids.as_slice() {
        [one] if !in_place && e.doc.parent_of(one).is_some_and(|p| p.0 == ROOT) && e.doc.find(one).is_some_and(|n| n["type"] == "frame") => {
            e.renderer.find(one).map(|f| crate::render::find_empty_space(&e.renderer, f.w, f.h, "right", 40.0, Some(f)))
        }
        _ => None,
    };
    e.edit_or_drag(|d| {
        ids.iter().map(|id| {
            let src = d.find(id).ok_or_else(|| format!("node not found: {id}"))?.clone();
            let (parent, idx) = d.parent_of(id).ok_or_else(|| format!("node not found: {id}"))?;
            let mut data = json!({"reusable": null});
            if let Some(p) = &right {
                (data["x"], data["y"]) = (p["x"].clone(), p["y"].clone());
            } else if src["x"].is_number() || parent == ROOT {
                (data["x"], data["y"]) = (json!(src["x"].as_f64().unwrap_or(0.0)), json!(src["y"].as_f64().unwrap_or(0.0)));
            }
            let mut plain = src;
            plain["reusable"] = json!(false);
            let new = d.copy(&plain, &parent, Some(&data))?;
            d.move_node(&new, Some(&parent), Some(idx + 1))?;
            Ok(new)
        }).collect()
    })
}

fn session() -> &'static str {
    static S: OnceLock<String> = OnceLock::new();
    S.get_or_init(|| format!("{:016x}", random_u64()))
}

const MARK: &str = "data-nala-node-clipboard";
const PEN_MARK: &str = "data-pen-node-clipboard";

/// Ctrl+C: `text/html` with the nodes as base64 JSON in a span (.pen clipboard scheme, own marker;
/// positions are world coordinates) and a `text/plain` label.
pub fn copy(e: &mut Engine, ids: &[String]) -> Value {
    e.sync();
    let v = View::new(e);
    let ids: Vec<String> = v.paint_order(&top_only(&e.doc, ids)).into_iter().filter(|id| !id.contains('/') && e.doc.find(id).is_some()).collect();
    let (mut nodes, mut boxes) = (Vec::new(), Vec::new());
    for id in &ids {
        let mut n = e.doc.find(id).cloned().unwrap_or_default();
        if let Some(o) = v.origin(id) {
            (n["x"], n["y"]) = (num(o.0), num(o.1));
        }
        nodes.push(n);
        boxes.push(v.rect(id).map_or(Value::Null, |r| json!([r.0, r.1, r.2, r.3])));
    }
    let payload = json!({"source": session(), "localData": ids, "worldPositions": true, "boxes": boxes, "remoteData": {"nodes": nodes}});
    let b64 = base64::engine::general_purpose::STANDARD.encode(payload.to_string());
    let label = if ids.len() == 1 { format!("Node ID: {}", ids[0]) } else { format!("Node IDs: {}", ids.join(", ")) };
    json!({"html": format!("<meta charset=\"utf-8\"><span {MARK}=\"{b64}\">{label}</span>"), "text": label, "ids": ids})
}

struct Clip {
    nodes: Vec<Value>,
    /// World origin and box per node.
    origins: Vec<(f64, f64)>,
    boxes: Vec<Rect>,
    /// Source ids when copied in this session (enables paste in place).
    sources: Vec<String>,
    /// Plain text / SVG: always lands centered (no meaningful position).
    center: bool,
    text: bool,
}

fn svg_size(svg: &str) -> (f64, f64) {
    let tag = &svg[svg.find("<svg").unwrap_or(0)..];
    let tag = &tag[..tag.find('>').unwrap_or(tag.len())];
    let attr = |name: &str| {
        let i = tag.find(&format!(" {name}=\""))? + name.len() + 3;
        Some(tag[i..].split('"').next()?.to_string())
    };
    let n = |s: Option<String>| s.and_then(|s| s.trim_end_matches("px").trim().parse::<f64>().ok()).filter(|v| *v > 0.0);
    let vb: Vec<f64> = attr("viewBox").map(|s| s.split([' ', ',']).filter_map(|p| p.parse().ok()).collect()).unwrap_or_default();
    let (vw, vh) = if vb.len() == 4 { (Some(vb[2]), Some(vb[3])) } else { (None, None) };
    (n(attr("width")).or(vw).unwrap_or(100.0), n(attr("height")).or(vh).unwrap_or(100.0))
}

fn decode(html: &str, text: &str) -> Result<Clip, String> {
    for mark in [MARK, PEN_MARK] {
        let Some(i) = html.find(&format!("{mark}=\"")) else { continue };
        let rest = &html[i + mark.len() + 2..];
        let b64 = &rest[..rest.find('"').ok_or("bad clipboard data")?];
        let bytes = base64::engine::general_purpose::STANDARD.decode(b64.trim()).map_err(|e| format!("bad clipboard data: {e}"))?;
        let p: Value = serde_json::from_slice(&bytes).map_err(|e| format!("bad clipboard data: {e}"))?;
        let nodes: Vec<Value> = p["remoteData"]["nodes"].as_array().cloned().unwrap_or_default().into_iter().filter(|n| n.is_object()).collect();
        let num = |v: &Value| v.as_f64().unwrap_or(0.0);
        let origins: Vec<(f64, f64)> = nodes.iter().map(|n| (num(&n["x"]), num(&n["y"]))).collect();
        let boxes = nodes.iter().enumerate().map(|(i, n)| match p["boxes"][i].as_array() {
            Some(b) if b.len() == 4 => (num(&b[0]), num(&b[1]), num(&b[2]), num(&b[3])),
            _ => (origins[i].0, origins[i].1, num(&n["width"]), num(&n["height"])),
        }).collect();
        let same = p["source"] == session();
        let sources = if same { p["localData"].as_array().map(|a| a.iter().filter_map(|s| s.as_str().map(str::to_string)).collect()).unwrap_or_default() } else { Vec::new() };
        return Ok(Clip { nodes, origins, boxes, sources, center: false, text: false });
    }
    if let Some(figma) = crate::figma::decode(html)? {
        let (nodes, boxes): (Vec<Value>, Vec<Rect>) = figma.into_iter().unzip();
        let origins = boxes.iter().map(|b| (b.0, b.1)).collect();
        return Ok(Clip { nodes, origins, boxes, sources: Vec::new(), center: false, text: false });
    }
    let t = text.trim();
    let svg = t.starts_with("<svg") || (t.starts_with("<?xml") && t.contains("<svg"));
    let (node, w, h) = if svg {
        let (w, h) = svg_size(t);
        let url = format!("data:image/svg+xml;base64,{}", base64::engine::general_purpose::STANDARD.encode(t));
        (json!({"type": "rectangle", "name": "SVG", "width": num(w), "height": num(h), "fill": {"type": "image", "url": url, "mode": "fit"}}), w, h)
    } else if !t.is_empty() {
        let lines = text.lines().count().max(1) as f64;
        let w = text.lines().map(|l| l.chars().count()).max().unwrap_or(0) as f64 * 7.0;
        (json!({"type": "text", "name": "Text", "content": text, "fontFamily": "Inter", "fontSize": 12, "fill": "#000000"}), w, lines * 15.0)
    } else {
        return Ok(Clip { nodes: Vec::new(), origins: Vec::new(), boxes: Vec::new(), sources: Vec::new(), center: true, text: false });
    };
    Ok(Clip { nodes: vec![node], origins: vec![(0.0, 0.0)], boxes: vec![(0.0, 0.0, w, h)], sources: Vec::new(), center: true, text: !svg })
}

/// Ctrl+V. Nodes go into each selected container (centered, or in place when copied from
/// it), else next to their visible sources, else onto the canvas where they were (centered
/// in the viewport when that is off-screen). Plain text becomes a text layer, SVG an image.
pub fn paste(e: &mut Engine, html: &str, text: &str, targets: &[String], view: Option<Rect>) -> Result<Vec<String>, String> {
    let clip = decode(html, text)?;
    if clip.nodes.is_empty() {
        return Ok(Vec::new());
    }
    e.sync();
    let plan: Vec<(String, Vec<Value>)> = {
        let v = View::new(e);
        let u = union(clip.boxes.iter().copied()).unwrap_or_default();
        let center_on = |r: Rect| ((r.0 + r.2 / 2.0 - u.0 - u.2 / 2.0).round(), (r.1 + r.3 / 2.0 - u.1 - u.3 / 2.0).round());
        let mut parents: Vec<String> = Vec::new();
        for id in targets.iter().filter(|t| !clip.sources.contains(t)) {
            let mut cur = Some(id.clone());
            while let Some(c) = cur.filter(|c| c != ROOT) {
                if v.accepts_children(&c) {
                    if !parents.contains(&c) {
                        parents.push(c);
                    }
                    break;
                }
                cur = e.doc.parent_of(&c).map(|p| p.0);
            }
        }
        let src_parent = clip.sources.iter().find_map(|s| e.doc.parent_of(s)).map(|p| p.0);
        let visible = |r: Rect| view.is_none_or(|vr| overlaps(r, vr));
        let src_visible = clip.sources.iter().any(|s| v.rect(s).is_some_and(visible));
        let places: Vec<(String, (f64, f64))> = if !parents.is_empty() {
            parents.iter().map(|p| {
                let shift = if !clip.center && src_parent.as_deref() == Some(p.as_str()) { (0.0, 0.0) } else { v.rect(p).map(center_on).unwrap_or_default() };
                (p.clone(), shift)
            }).collect()
        } else if let (Some(p), true) = (src_parent.filter(|_| src_visible), !clip.center) {
            vec![(p, (0.0, 0.0))]
        } else if !clip.center && visible(u) {
            vec![(ROOT.to_string(), (0.0, 0.0))]
        } else {
            vec![(ROOT.to_string(), view.map(center_on).unwrap_or_default())]
        };
        places.into_iter().map(|(p, (sx, sy))| {
            let po = v.origin(&p).unwrap_or((0.0, 0.0));
            let flow = v.layout(&p).is_some();
            let nodes = clip.nodes.iter().zip(&clip.origins).map(|(n, o)| {
                let mut n = n.clone();
                if let Some(obj) = n.as_object_mut() {
                    obj.remove("reusable");
                }
                if !flow || n["layoutPosition"] == "absolute" {
                    (n["x"], n["y"]) = (num(o.0 + sx - po.0), num(o.1 + sy - po.1));
                }
                if clip.text && p == ROOT {
                    n["fill"] = json!("#FFFFFF");
                }
                n
            }).collect();
            (p, nodes)
        }).collect()
    };
    e.edit_or_drag(|d| {
        let mut out = Vec::new();
        for (p, nodes) in plan {
            for n in nodes {
                out.push(d.insert(&p, n, None)?);
            }
        }
        Ok(out)
    })
}

// ---- Pen tool / vector editing ----
// The canvas edits anchors in canvas coordinates; these map them to and from a path's geometry.

/// Affine `[a, b, c, d, e, f]` (x' = a·x + c·y + e, y' = b·x + d·y + f).
type Affine = [f64; 6];

fn compose(m: Affine, t: Affine) -> Affine {
    [m[0] * t[0] + m[2] * t[1], m[1] * t[0] + m[3] * t[1], m[0] * t[2] + m[2] * t[3], m[1] * t[2] + m[3] * t[3], m[0] * t[4] + m[2] * t[5] + m[4], m[1] * t[4] + m[3] * t[5] + m[5]]
}

fn invert(m: Affine) -> Option<Affine> {
    let det = m[0] * m[3] - m[1] * m[2];
    (det.abs() > 1e-12).then(|| {
        let (a, b, c, d) = (m[3] / det, -m[1] / det, -m[2] / det, m[0] / det);
        [a, b, c, d, -(a * m[4] + c * m[5]), -(b * m[4] + d * m[5])]
    })
}

fn map(m: &Affine, p: (f64, f64)) -> (f64, f64) {
    (m[0] * p.0 + m[2] * p.1 + m[4], m[1] * p.0 + m[3] * p.1 + m[5])
}

/// One anchor; handles are absolute points.
#[derive(Clone, Copy, Debug, PartialEq)]
struct Anchor {
    p: (f64, f64),
    i: Option<(f64, f64)>,
    o: Option<(f64, f64)>,
}

#[derive(Clone, Debug, PartialEq)]
struct Contour {
    anchors: Vec<Anchor>,
    closed: bool,
}

impl Anchor {
    fn at(p: (f64, f64)) -> Anchor {
        Anchor { p, i: None, o: None }
    }
    fn mapped(&self, m: &Affine) -> Anchor {
        Anchor { p: map(m, self.p), i: self.i.map(|h| map(m, h)), o: self.o.map(|h| map(m, h)) }
    }
}

/// Path data → contours: quadratics become cubics, a handle equal to its anchor
/// is no handle, and a closing point on the start merges into it.
fn contours_of(d: &str) -> Vec<Contour> {
    let far = |a: (f64, f64), b: (f64, f64)| (a.0 - b.0).abs() > 1e-6 || (a.1 - b.1).abs() > 1e-6;
    let mut out: Vec<Contour> = Vec::new();
    let (mut cur, mut start, mut open) = ((0.0, 0.0), (0.0, 0.0), false);
    let curve = |out: &mut Vec<Contour>, open: &mut bool, c1: (f64, f64), c2: (f64, f64), to: (f64, f64), from: (f64, f64), start: (f64, f64)| {
        if !*open {
            out.push(Contour { anchors: vec![Anchor::at(start)], closed: false });
            *open = true;
        }
        let anchors = &mut out.last_mut().unwrap().anchors;
        let last = anchors.last_mut().unwrap();
        if far(c1, from) {
            last.o = Some(c1);
        }
        anchors.push(Anchor { p: to, i: far(c2, to).then_some(c2), o: None });
    };
    for seg in svgtypes::SimplifyingPathParser::from(d) {
        let Ok(seg) = seg else { break };
        match seg {
            svgtypes::SimplePathSegment::MoveTo { x, y } => {
                out.push(Contour { anchors: vec![Anchor::at((x, y))], closed: false });
                (cur, start, open) = ((x, y), (x, y), true);
            }
            svgtypes::SimplePathSegment::LineTo { x, y } => {
                curve(&mut out, &mut open, cur, (x, y), (x, y), cur, start);
                cur = (x, y);
            }
            svgtypes::SimplePathSegment::CurveTo { x1, y1, x2, y2, x, y } => {
                curve(&mut out, &mut open, (x1, y1), (x2, y2), (x, y), cur, start);
                cur = (x, y);
            }
            svgtypes::SimplePathSegment::Quadratic { x1, y1, x, y } => {
                let k = 2.0 / 3.0;
                let c1 = (cur.0 + k * (x1 - cur.0), cur.1 + k * (y1 - cur.1));
                let c2 = (x + k * (x1 - x), y + k * (y1 - y));
                curve(&mut out, &mut open, c1, c2, (x, y), cur, start);
                cur = (x, y);
            }
            svgtypes::SimplePathSegment::ClosePath => {
                if let Some(c) = out.last_mut().filter(|_| open) {
                    if c.anchors.len() > 1 && !far(c.anchors[0].p, c.anchors[c.anchors.len() - 1].p) {
                        let last = c.anchors.pop().unwrap();
                        c.anchors[0].i = last.i;
                    }
                    c.closed = true;
                }
                (open, cur) = (false, start);
            }
        }
    }
    out
}

/// Enough to draw: two anchors, or one closed onto itself with a handle.
fn drawable(c: &Contour) -> bool {
    c.anchors.len() >= 2 || (c.closed && c.anchors.len() == 1 && (c.anchors[0].o.is_some() || c.anchors[0].i.is_some()))
}

/// Contours → path data: straight when both facing handles are absent.
fn path_d(contours: &[Contour]) -> String {
    let n = |v: f64| {
        let r = (v * 1000.0).round() / 1000.0;
        if r == 0.0 { "0".to_string() } else { r.to_string() }
    };
    let seg = |out: &mut Vec<String>, a: &Anchor, b: &Anchor| {
        if a.o.is_some() || b.i.is_some() {
            let (c1, c2) = (a.o.unwrap_or(a.p), b.i.unwrap_or(b.p));
            out.push(format!("C{} {} {} {} {} {}", n(c1.0), n(c1.1), n(c2.0), n(c2.1), n(b.p.0), n(b.p.1)));
        } else {
            out.push(format!("L{} {}", n(b.p.0), n(b.p.1)));
        }
    };
    let mut out = Vec::new();
    for c in contours {
        let Some(first) = c.anchors.first() else { continue };
        out.push(format!("M{} {}", n(first.p.0), n(first.p.1)));
        for w in c.anchors.windows(2) {
            seg(&mut out, &w[0], &w[1]);
        }
        if c.closed && drawable(c) {
            seg(&mut out, c.anchors.last().unwrap(), first);
            out.push("Z".into());
        }
    }
    out.join(" ")
}

/// Geometry → canvas: the viewBox (default: tight bounds) stretched onto the box, then the
/// painted rotation/flips (as svg.rs draws it).
fn geometry_to_canvas(n: &Value, f: &Flat) -> Affine {
    let vb = n["viewBox"].as_array().filter(|a| a.len() == 4).map(|a| (a[0].as_f64().unwrap_or(0.0), a[1].as_f64().unwrap_or(0.0), a[2].as_f64().unwrap_or(1.0), a[3].as_f64().unwrap_or(1.0)));
    let (vx, vy, vw, vh) = vb.or_else(|| crate::svg::path_bbox(n["geometry"].as_str().unwrap_or(""))).unwrap_or_default();
    let sx = if vw > 0.0 { f.w / vw } else { 1.0 };
    let sy = if vh > 0.0 { f.h / vh } else { 1.0 };
    compose(f.m.unwrap_or([1.0, 0.0, 0.0, 1.0, 0.0, 0.0]), [sx, 0.0, 0.0, sy, f.x - vx * sx, f.y - vy * sy])
}

fn contours_json(cs: &[Contour]) -> Value {
    let pt = |p: Option<(f64, f64)>| p.map_or(Value::Null, |p| json!([p.0, p.1]));
    json!(cs.iter().map(|c| json!({"closed": c.closed, "anchors": c.anchors.iter().map(|a| {
        let mut o = json!({"x": a.p.0, "y": a.p.1});
        if a.i.is_some() { o["in"] = pt(a.i) }
        if a.o.is_some() { o["out"] = pt(a.o) }
        o
    }).collect::<Vec<_>>()})).collect::<Vec<_>>())
}

fn contours_from_json(v: &Value) -> Result<Vec<Contour>, String> {
    let pt = |v: &Value| -> Option<(f64, f64)> { Some((v[0].as_f64().filter(|x| x.is_finite())?, v[1].as_f64().filter(|x| x.is_finite())?)) };
    v.as_array().ok_or("contours required")?.iter().map(|c| {
        let anchors = c["anchors"].as_array().ok_or("anchors required")?.iter().map(|a| {
            let p = (a["x"].as_f64().filter(|x| x.is_finite()).ok_or("bad anchor")?, a["y"].as_f64().filter(|x| x.is_finite()).ok_or("bad anchor")?);
            Ok(Anchor { p, i: pt(&a["in"]), o: pt(&a["out"]) })
        }).collect::<Result<Vec<_>, String>>()?;
        Ok(Contour { anchors, closed: c["closed"] == true })
    }).collect()
}

/// A path's contours in canvas coordinates, for the vector editor.
pub fn path_edit(e: &mut Engine, id: &str) -> Result<Value, String> {
    e.sync();
    let n = e.doc.find(id).ok_or_else(|| format!("node not found: {id}"))?;
    if n["type"] != "path" || id.contains('/') {
        return Err("only paths can be edited point by point".into());
    }
    let f = e.renderer.find(id).ok_or_else(|| format!("node not found: {id}"))?;
    let m = geometry_to_canvas(n, f);
    let cs: Vec<Contour> = contours_of(n["geometry"].as_str().unwrap_or("")).into_iter()
        .map(|c| Contour { anchors: c.anchors.iter().map(|a| a.mapped(&m)).collect(), closed: c.closed }).collect();
    Ok(contours_json(&cs))
}

/// Writes contours given in canvas coordinates. Into path `id`: the
/// geometry keeps its coordinate space and the box follows the tight bounds (unless the path has
/// an explicit viewBox), so nothing moves on the canvas. Without `id`: a new path (black 1 px
/// stroke) under `parent`, outside its auto layout. Returns the path's id.
pub fn path_write(e: &mut Engine, id: Option<&str>, parent: &str, contours: &Value) -> Result<String, String> {
    let cs: Vec<Contour> = contours_from_json(contours)?.into_iter().filter(drawable).collect();
    if cs.is_empty() {
        return Err("a path needs two points".into());
    }
    e.sync();
    // Box numbers keep 6 decimals: coarser rounding would rescale the geometry.
    let fine = |v: f64| json!((v * 1e6).round() / 1e6);
    let to = |m: &Affine| cs.iter().map(|c| Contour { anchors: c.anchors.iter().map(|a| a.mapped(m)).collect(), closed: c.closed }).collect::<Vec<_>>();
    let Some(id) = id else {
        let v = View::new(e);
        let parent = if parent != ROOT && v.accepts_children(parent) && v.ty(parent) == "frame" { parent } else { ROOT };
        // Canvas → the parent's own coordinates.
        let pm = match v.flat(parent) {
            Some(f) => invert(compose(f.m.unwrap_or([1.0, 0.0, 0.0, 1.0, 0.0, 0.0]), [1.0, 0.0, 0.0, 1.0, f.x, f.y])).ok_or("parent is not invertible")?,
            None => [1.0, 0.0, 0.0, 1.0, 0.0, 0.0],
        };
        let local = to(&pm);
        let (bx, by, bw, bh) = crate::svg::path_bbox(&path_d(&local)).ok_or("empty path")?;
        let geometry = path_d(&to(&compose([1.0, 0.0, 0.0, 1.0, -bx, -by], pm)));
        let mut node = json!({"type": "path", "name": "Path", "x": fine(bx), "y": fine(by), "width": fine(bw), "height": fine(bh),
                              "geometry": geometry, "stroke": "#000000", "strokeWidth": 1});
        if v.layout(parent).is_some() {
            node["layoutPosition"] = json!("absolute");
        }
        let parent = parent.to_string();
        return e.edit_or_drag(|d| d.insert(&parent, node, None));
    };
    let n = e.doc.find(id).ok_or_else(|| format!("node not found: {id}"))?;
    if n["type"] != "path" {
        return Err("only paths can be edited point by point".into());
    }
    let f = e.renderer.find(id).ok_or_else(|| format!("node not found: {id}"))?;
    let m = geometry_to_canvas(n, f);
    let geometry = path_d(&to(&invert(m).ok_or("the path is collapsed")?));
    let mut props = json!({"geometry": geometry});
    if !n["viewBox"].is_array() {
        // The box follows the geometry's new tight bounds at the current scale.
        let old = crate::svg::path_bbox(n["geometry"].as_str().unwrap_or("")).unwrap_or_default();
        let new = crate::svg::path_bbox(&geometry).ok_or("empty path")?;
        let sx = if old.2 > 0.0 { f.w / old.2 } else { 1.0 };
        let sy = if old.3 > 0.0 { f.h / old.3 } else { 1.0 };
        let (ox, oy) = ((new.0 - old.0) * sx, (new.1 - old.1) * sy);
        if ox.abs() > 1e-6 || oy.abs() > 1e-6 {
            // The origin moves along the node's own rotation/flips.
            let t = -n["rotation"].as_f64().unwrap_or(0.0).to_radians();
            let (fx, fy) = (if n["flipX"] == true { -1.0 } else { 1.0 }, if n["flipY"] == true { -1.0 } else { 1.0 });
            let (dx, dy) = (t.cos() * fx * ox - t.sin() * fy * oy, t.sin() * fx * ox + t.cos() * fy * oy);
            props["x"] = fine(n["x"].as_f64().unwrap_or(0.0) + dx);
            props["y"] = fine(n["y"].as_f64().unwrap_or(0.0) + dy);
        }
        props["width"] = fine(new.2 * sx);
        props["height"] = fine(new.3 * sy);
    }
    let id = id.to_string();
    e.edit_or_drag(|d| d.update(&id, &props))?;
    Ok(id)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn path_contours_roundtrip() {
        // A corner, a smooth point and a closing segment; the duplicate closing point merges.
        let cs = contours_of("M0 0 L10 0 C15 0 20 5 20 10 Q20 20 10 20 L0 0 Z m30 0 h5");
        assert_eq!(cs.len(), 2);
        assert!(cs[0].closed && cs[0].anchors.len() == 4 && !cs[1].closed);
        assert_eq!((cs[0].anchors[1].o, cs[0].anchors[2].i), (Some((15.0, 0.0)), Some((20.0, 5.0))));
        assert_eq!(cs[1].anchors.iter().map(|a| a.p).collect::<Vec<_>>(), [(30.0, 0.0), (35.0, 0.0)]);
        let d = path_d(&cs);
        assert_eq!(d, "M0 0 L10 0 C15 0 20 5 20 10 C20 16.667 16.667 20 10 20 L0 0 Z M30 0 L35 0");
        assert_eq!(path_d(&contours_of(&d)), d, "reading and writing again is lossless");
    }

    #[test]
    fn align_uses_the_painted_bounds() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let frame = e.edit(|d| d.insert(ROOT, json!({"type": "frame", "x": 0, "y": 0, "width": 400, "height": 400, "layout": "none"}), None)).unwrap();
        let mut ids = Vec::new();
        for n in [json!({"x": 100, "y": 50}), json!({"x": 200, "y": 150, "flipX": true}), json!({"x": 300, "y": 250, "rotation": 90})] {
            let mut n = n;
            (n["type"], n["width"], n["height"]) = (json!("rectangle"), json!(40), json!(40));
            ids.push(e.edit(|d| d.insert(&frame, n.clone(), None)).unwrap());
        }
        let edges = |e: &mut Engine, ids: &[String]| {
            e.sync();
            let v = View::new(e);
            ids.iter().map(|id| v.painted(id, None).unwrap()).collect::<Vec<Rect>>()
        };
        align(&mut e, &ids, "left").unwrap();
        assert!(edges(&mut e, &ids).iter().all(|r| (r.0 - 100.0).abs() < 1e-6), "left edges");
        align(&mut e, &ids, "top").unwrap();
        assert!(edges(&mut e, &ids).iter().all(|r| (r.1 - 50.0).abs() < 1e-6), "top edges");
        align(&mut e, &ids, "right").unwrap();
        assert!(edges(&mut e, &ids).iter().all(|r| (r.0 + r.2 - 140.0).abs() < 1e-6), "right edges");
        align(&mut e, &ids, "vcenter").unwrap();
        let cy: Vec<f64> = edges(&mut e, &ids).iter().map(|r| r.1 + r.3 / 2.0).collect();
        assert!(cy.iter().all(|c| (c - cy[0]).abs() < 1e-6), "{cy:?}");
        // One rotated layer aligns to its parent's box.
        align(&mut e, &ids[2..], "left").unwrap();
        assert!((edges(&mut e, &ids[2..])[0].0).abs() < 1e-6, "left of the parent");
    }

    #[test]
    fn align_inside_a_rotated_parent() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let frame = e.edit(|d| d.insert(ROOT, json!({"type": "frame", "x": 100, "y": 100, "width": 400, "height": 300, "rotation": 30, "layout": "none"}), None)).unwrap();
        let ids: Vec<String> = [json!({"x": 40, "y": 30, "width": 60, "height": 20}), json!({"x": 200, "y": 150, "width": 30, "height": 50, "rotation": 15})]
            .into_iter()
            .map(|mut n| {
                n["type"] = json!("rectangle");
                e.edit(|d| d.insert(&frame, n.clone(), None)).unwrap()
            })
            .collect();
        let painted = |e: &mut Engine| {
            e.sync();
            let v = View::new(e);
            ids.iter().map(|id| v.painted(id, None).unwrap()).collect::<Vec<Rect>>()
        };
        let before = painted(&mut e);
        align(&mut e, &ids, "left").unwrap();
        let after = painted(&mut e);
        let left = before.iter().map(|r| r.0).fold(f64::MAX, f64::min);
        // Positions are stored to 0.01, so a parent-space step lands within that of the canvas edge.
        assert!(after.iter().all(|r| (r.0 - left).abs() < 0.01), "canvas-space left edges: {after:?}");
        assert!(before.iter().any(|r| (r.0 - left).abs() > 1e-6), "the layers started unaligned");
    }

    #[test]
    fn reparent_keeps_a_group_with_a_turned_child_in_place() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let frame = e.edit(|d| d.insert(ROOT, json!({"type": "frame", "x": 300, "y": 300, "width": 400, "height": 400, "layout": "none"}), None)).unwrap();
        // Turned 180°, the second child paints up-left of its origin (0..40, 0..20): the group's box starts there,
        // not at the lowest child x/y (40, 20).
        let group = e.edit(|d| d.insert(ROOT, json!({"type": "group", "x": 100, "y": 100, "children": [
            {"type": "rectangle", "x": 100, "y": 100, "width": 40, "height": 20},
            {"type": "rectangle", "x": 40, "y": 20, "width": 40, "height": 20, "rotation": 180}]}), None)).unwrap();
        let painted = |e: &mut Engine| {
            e.sync();
            View::new(e).painted(&group, None).unwrap()
        };
        let before = painted(&mut e);
        reparent(&mut e, std::slice::from_ref(&group), &frame, None).unwrap();
        let after = painted(&mut e);
        assert!((after.0 - before.0).abs() < 1e-6 && (after.1 - before.1).abs() < 1e-6, "{before:?} → {after:?}");
    }

    #[test]
    fn path_write_keeps_the_canvas_position() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        let frame = e.edit(|d| d.insert(ROOT, json!({"type": "frame", "x": 100, "y": 50, "width": 400, "height": 300, "layout": "none"}), None)).unwrap();
        // Drawn on the canvas: a triangle with one curved side, inside the frame.
        let drawn = json!([{"closed": true, "anchors": [{"x": 150, "y": 100}, {"x": 250, "y": 100, "out": [270, 140]}, {"x": 200, "y": 180, "in": [230, 190]}]}]);
        let id = path_write(&mut e, None, &frame, &drawn).unwrap();
        let n = e.doc.find(&id).unwrap().clone();
        assert_eq!((n["x"].as_f64(), n["y"].as_f64(), n["stroke"].as_str()), (Some(50.0), Some(50.0), Some("#000000")), "stored relative to the frame");
        let back = path_edit(&mut e, &id).unwrap();
        let close = |a: &Value, b: &Value| (a.as_f64().unwrap() - b.as_f64().unwrap()).abs() < 1e-6;
        assert!(close(&back[0]["anchors"][2]["in"][0], &json!(230)) && close(&back[0]["anchors"][1]["y"], &json!(100)), "{back}");
        // Dragging the first anchor left and up grows the box that way; the other anchors stay.
        let mut moved = back.clone();
        moved[0]["anchors"][0] = json!({"x": 120, "y": 70});
        path_write(&mut e, Some(&id), ROOT, &moved).unwrap();
        let n = e.doc.find(&id).unwrap();
        assert!(close(&n["x"], &json!(20)) && close(&n["y"], &json!(20)), "{n}");
        let again = path_edit(&mut e, &id).unwrap();
        assert!(close(&again[0]["anchors"][0]["x"], &json!(120)) && close(&again[0]["anchors"][2]["in"][0], &json!(230)), "{again}");
        // Rotated paths are edited where they are painted.
        e.edit(|d| d.update(&id, &json!({"rotation": 90}))).unwrap();
        let turned = path_edit(&mut e, &id).unwrap();
        path_write(&mut e, Some(&id), ROOT, &turned).unwrap();
        assert_eq!(path_edit(&mut e, &id).unwrap().to_string(), turned.to_string());
        assert!(path_write(&mut e, Some(&id), ROOT, &json!([{"closed": false, "anchors": [{"x": 0, "y": 0}]}])).is_err(), "one point is no path");
    }

    #[test]
    fn flex_inference() {
        // Three boxes in a row, 20 apart, bottom-aligned, inside a frame with 16/24 margins.
        let kids = [((40.0, 30.0, 50.0, 20.0), false), ((110.0, 10.0, 50.0, 40.0), false), ((180.0, 20.0, 50.0, 30.0), false)];
        let f = infer_flex(Some((16.0, -6.0, 238.0, 72.0)), &kids);
        assert_eq!((f.dir, f.gap, f.align), (0, 20.0, "end"));
        assert_eq!(f.padding, [16.0, 24.0, 16.0, 24.0]);
        assert_eq!(f.order, vec![0, 1, 2]);
        // A column given out of order; two full-width children fill the cross axis.
        let col = [((0.0, 50.0, 100.0, 20.0), true), ((0.0, 0.0, 100.0, 30.0), false), ((20.0, 90.0, 60.0, 10.0), false)];
        let f = infer_flex(None, &col);
        assert_eq!((f.dir, f.gap, f.align, f.padding), (1, 20.0, "center", [0.0; 4]));
        assert_eq!((f.order, f.fill), (vec![1, 0, 2], vec![0, 1]));
        // A lone text is centered with the smaller margins as padding.
        let f = infer_flex(Some((0.0, 0.0, 100.0, 40.0)), &[((30.0, 12.0, 40.0, 16.0), true)]);
        assert_eq!((f.align, f.justify, f.padding), ("center", Some("center"), [12.0, 30.0, 12.0, 30.0]));
        assert_eq!(infer_flex(None, &[]).padding, [10.0; 4]);
    }

    #[test]
    fn clipboard_decoding() {
        let svg = r#"<svg xmlns="http://www.w3.org/2000/svg" width="24px" viewBox="0 0 24 12"><path d="M0 0"/></svg>"#;
        assert_eq!(svg_size(svg), (24.0, 12.0));
        let c = decode("", svg).unwrap();
        assert_eq!((c.nodes[0]["type"].as_str(), c.center), (Some("rectangle"), true));
        let c = decode("", "hello\nworld").unwrap();
        assert_eq!(c.nodes[0]["content"], "hello\nworld");
        // The .pen clipboard marker and payload shape are accepted.
        let payload = json!({"source": "other", "localData": ["a"], "remoteData": {"nodes": [{"type": "rectangle", "id": "a", "x": 5, "y": 6, "width": 10, "height": 20}]}});
        let html = format!("<span {PEN_MARK}=\"{}\">Node ID: a</span>", base64::engine::general_purpose::STANDARD.encode(payload.to_string()));
        let c = decode(&html, "Node ID: a").unwrap();
        assert_eq!((c.boxes[0], c.sources.len(), c.center), ((5.0, 6.0, 10.0, 20.0), 0, false));
    }
}
