//! Flex layout, run as five passes: fit (H), fill (H), fit (V), fill (V), position.
//! Single axis, no wrapping. Fill children never count toward a fit_content size, fit_content keeps its
//! fallback when nothing can size it, rotated/flipped children are placed by their bounding box and
//! groups are the union of their children. Writes absolute boxes into the RNode tree.

use crate::resolve::{num, RNode};
use crate::text::{Fonts, TextStyle};
use serde_json::Value;

#[derive(Clone, Copy, Debug, PartialEq)]
enum Sizing {
    Fixed,
    Fit,
    Fill,
}

/// Sizing mode and initial size: a number, or `fit_content(N)` / `fill_container(N)` with fallback N (default 0).
fn parse_spec(v: Option<&Value>) -> Option<(Sizing, f64)> {
    let v = v?;
    if let Some(n) = num(v) {
        return Some((Sizing::Fixed, n.max(0.0)));
    }
    let s = v.as_str()?;
    let fb = s.find('(').and_then(|i| s[i + 1..].trim_end_matches(')').trim().parse::<f64>().ok()).unwrap_or(0.0).max(0.0);
    if s.starts_with("fit_content") {
        Some((Sizing::Fit, fb))
    } else if s.starts_with("fill_container") {
        Some((Sizing::Fill, fb))
    } else {
        None
    }
}

fn spec(n: &RNode, ax: usize) -> (Sizing, f64) {
    parse_spec(n.p.get(if ax == 0 { "width" } else { "height" })).unwrap_or(match n.ty.as_str() {
        "frame" => (Sizing::Fit, 0.0),
        "note" | "prompt" | "context" => (Sizing::Fixed, if ax == 0 { 240.0 } else { 140.0 }),
        "browser" => (Sizing::Fixed, if ax == 0 { 1280.0 } else { 800.0 }),
        _ => (Sizing::Fixed, 0.0),
    })
}

fn sizing(n: &RNode, ax: usize) -> Sizing {
    spec(n, ax).0
}

/// Main axis (0 = horizontal, 1 = vertical). Only frames with a layout have one; groups never do.
fn dir(n: &RNode) -> Option<usize> {
    if n.ty != "frame" {
        return None;
    }
    match n.s("layout") {
        Some("none") => None,
        Some("vertical") => Some(1),
        _ => Some(0),
    }
}

/// [top, right, bottom, left]; a number, [vertical, horizontal] or four values (anything else is 0).
pub fn padding(n: &RNode) -> [f64; 4] {
    match n.p.get("padding") {
        Some(Value::Array(a)) if a.len() == 2 => {
            let (v, h) = (a.first().and_then(num).unwrap_or(0.0), a.get(1).and_then(num).unwrap_or(0.0));
            [v, h, v, h]
        }
        Some(Value::Array(a)) if a.len() == 4 => {
            let g = |i: usize| a.get(i).and_then(num).unwrap_or(0.0);
            [g(0), g(1), g(2), g(3)]
        }
        Some(Value::Array(_)) => [0.0; 4],
        Some(v) => [num(v).unwrap_or(0.0); 4],
        None => [0.0; 4],
    }
}

fn pad(n: &RNode, ax: usize) -> f64 {
    let p = padding(n);
    if ax == 0 { p[1] + p[3] } else { p[0] + p[2] }
}

fn gap(n: &RNode) -> f64 {
    n.f("gap").unwrap_or(0.0)
}

fn in_flow(n: &RNode) -> bool {
    n.s("layoutPosition") != Some("absolute")
}

fn size(n: &RNode, ax: usize) -> f64 {
    if ax == 0 { n.w } else { n.h }
}

/// Bounding box [x0, y0, x1, y1] of the node's local bounds under its rotation and flips,
/// relative to its origin (PIXI matrix: flip about the origin, then rotate CCW).
pub fn local_aabb(n: &RNode) -> [f64; 4] {
    let r = -n.f("rotation").unwrap_or(0.0).to_radians();
    let sx = if n.b("flipX") { -1.0 } else { 1.0 };
    let sy = if n.b("flipY") { -1.0 } else { 1.0 };
    let (a, b, c, d) = (r.cos() * sx, r.sin() * sx, -r.sin() * sy, r.cos() * sy);
    let mut o = [f64::MAX, f64::MAX, f64::MIN, f64::MIN];
    for (px, py) in [(n.bx, n.by), (n.bx + n.w, n.by), (n.bx, n.by + n.h), (n.bx + n.w, n.by + n.h)] {
        let (x, y) = (a * px + c * py, b * px + d * py);
        o = [o[0].min(x), o[1].min(y), o[2].max(x), o[3].max(y)];
    }
    o
}

fn outer(n: &RNode, ax: usize) -> f64 {
    let b = local_aabb(n);
    b[ax + 2] - b[ax]
}

/// Lay out one top-level node (and its subtree) at its own x/y.
pub fn layout(root: &mut RNode, fonts: &mut Fonts) {
    init(root, fonts);
    for ax in [0, 1] {
        fit(root, ax, fonts);
        fill(root, ax, fonts);
    }
    position(root);
    place(root, 0.0, 0.0, fonts);
}

/// Initial sizes (fixed values and fallbacks), origins from x/y, text and group bounds.
fn init(n: &mut RNode, fonts: &mut Fonts) {
    n.x = n.f("x").unwrap_or(0.0);
    n.y = n.f("y").unwrap_or(0.0);
    n.w = spec(n, 0).1;
    n.h = spec(n, 1).1;
    for k in &mut n.kids {
        init(k, fonts);
    }
    match n.ty.as_str() {
        "text" => text_bounds(n, fonts),
        "group" => group_bounds(n),
        _ => {}
    }
}

/// Post-order: a fit_content node with a layout hugs its in-flow, non-fill children.
fn fit(n: &mut RNode, ax: usize, fonts: &mut Fonts) {
    for k in &mut n.kids {
        fit(k, ax, fonts);
    }
    if let Some(d) = dir(n).filter(|_| sizing(n, ax) == Sizing::Fit) {
        let sized: Vec<f64> = n.kids.iter().filter(|k| in_flow(k) && sizing(k, ax) != Sizing::Fill).map(|k| outer(k, ax)).collect();
        if !sized.is_empty() {
            let content = if d == ax { sized.iter().sum::<f64>() + gap(n) * (sized.len() - 1) as f64 } else { sized.iter().copied().fold(0.0, f64::max) };
            let v = content + pad(n, ax);
            commit(n, ax, v, fonts);
        }
    }
    if n.ty == "group" {
        group_bounds(n);
    }
}

/// Pre-order: fill children share what the others leave (main axis) or take the inner size (cross), min 1.
fn fill(n: &mut RNode, ax: usize, fonts: &mut Fonts) {
    if let Some(d) = dir(n) {
        let mut share = size(n, ax) - pad(n, ax);
        if d == ax {
            let (mut all, mut fills) = (0usize, 0usize);
            for k in n.kids.iter().filter(|k| in_flow(k)) {
                all += 1;
                if sizing(k, ax) == Sizing::Fill {
                    fills += 1;
                } else {
                    share -= outer(k, ax);
                }
            }
            share -= gap(n) * all.saturating_sub(1) as f64;
            share = if fills == 0 { 0.0 } else { share / fills as f64 };
        }
        let share = share.max(1.0);
        for k in n.kids.iter_mut().filter(|k| in_flow(k) && sizing(k, ax) == Sizing::Fill) {
            commit(k, ax, share, fonts);
        }
    }
    for k in &mut n.kids {
        fill(k, ax, fonts);
    }
    if n.ty == "group" {
        group_bounds(n);
    }
}

/// Place in-flow children: justify along the main axis (space_* ignore gap and may go negative),
/// align on the cross axis; each child's rotated/flipped bounding box lands on its slot.
fn position(n: &mut RNode) {
    if let Some(e) = dir(n) {
        let c = 1 - e;
        let p = padding(n);
        let inner = [n.w - p[1] - p[3], n.h - p[0] - p[2]];
        let mut cur = [p[3], p[0]];
        let flow: Vec<[f64; 4]> = n.kids.iter().filter(|k| in_flow(k)).map(local_aabb).collect();
        let used: f64 = flow.iter().map(|b| b[e + 2] - b[e]).sum();
        let free = inner[e] - used;
        let o = flow.len();
        let mut step = gap(n);
        if o > 0 {
            let gaps = step * (o - 1) as f64;
            match n.s("justifyContent") {
                Some("end") => cur[e] += free - gaps,
                Some("center") => cur[e] += (free - gaps) / 2.0,
                // One child divides by zero; the step is never used then.
                Some("space_between") => step = free / (o - 1) as f64,
                Some("space_around") => {
                    step = free / o as f64;
                    cur[e] += step / 2.0;
                }
                _ => {}
            }
        }
        let align = n.s("alignItems").map(str::to_string);
        for (k, b) in n.kids.iter_mut().filter(|k| in_flow(k)).zip(flow) {
            let s = [b[2] - b[0], b[3] - b[1]];
            let mut q = cur;
            match align.as_deref() {
                Some("end") => q[c] += inner[c] - s[c],
                Some("center") => q[c] += (inner[c] - s[c]) / 2.0,
                _ => {}
            }
            k.x = q[0] - b[0];
            k.y = q[1] - b[1];
            cur[e] += s[e] + step;
        }
    }
    for k in &mut n.kids {
        position(k);
    }
    if n.ty == "group" {
        group_bounds(n);
    }
}

/// Size assigned by a fit or fill pass.
fn commit(n: &mut RNode, ax: usize, v: f64, fonts: &mut Fonts) {
    match n.ty.as_str() {
        // ponytail: groups keep their union here; scaling a filled group's children to the new size would fix it.
        "group" => {}
        "text" => match (n.s("textGrowth"), ax) {
            (Some("fixed-width" | "fixed-width-height"), 0) if n.w != v => {
                n.w = v;
                text_bounds(n, fonts);
            }
            (Some("fixed-width-height"), _) => n.h = v,
            // auto text keeps its intrinsic box and never wraps; fixed-width height follows the lines.
            _ => {}
        },
        _ if ax == 0 => n.w = v,
        _ => n.h = v,
    }
}

/// Text box: auto = intrinsic (ceil), fixed-width = width × ceil(lines), fixed-width-height = as set.
/// Lines wrap at ceil(width); a fixed width of 0 breaks at every opportunity.
fn text_bounds(n: &mut RNode, fonts: &mut Fonts) {
    let st = TextStyle::of(n);
    let line_h = fonts.metrics(&st).line_h;
    let content = match n.p.get("content") {
        Some(Value::String(s)) => s.clone(),
        Some(Value::Number(v)) => v.to_string(),
        _ => String::new(),
    };
    match n.s("textGrowth").map(str::to_string).as_deref() {
        Some(g @ ("fixed-width" | "fixed-width-height")) => {
            n.lines = fonts.wrap(&content, n.w.ceil(), &st);
            if g == "fixed-width" {
                n.h = (n.lines.len() as f64 * line_h).ceil();
            }
        }
        _ => {
            n.lines = fonts.wrap(&content, f64::INFINITY, &st);
            n.w = content.split('\n').map(|l| fonts.width(l.strip_suffix('\r').unwrap_or(l), &st)).fold(0.0, f64::max).ceil();
            n.h = (n.lines.len() as f64 * line_h).ceil();
        }
    }
}

/// Union of the children's transformed boxes; may start at a non-zero (even negative) origin.
fn group_bounds(n: &mut RNode) {
    let mut b = [f64::MAX, f64::MAX, f64::MIN, f64::MIN];
    for k in &n.kids {
        let l = local_aabb(k);
        b = [b[0].min(k.x + l[0]), b[1].min(k.y + l[1]), b[2].max(k.x + l[2]), b[3].max(k.y + l[3])];
    }
    if n.kids.is_empty() {
        b = [0.0; 4];
    }
    (n.bx, n.by, n.w, n.h) = (b[0], b[1], b[2] - b[0], b[3] - b[1]);
}

pub fn note_style(n: &RNode) -> TextStyle {
    let mut st = TextStyle::of(n);
    if n.p.get("fontSize").is_none() {
        st.size = 14.0;
    }
    st
}

/// Origins (relative to the parent's origin) → absolute boxes; flags children sticking out of frames.
fn place(n: &mut RNode, ox: f64, oy: f64, fonts: &mut Fonts) {
    let (ax, ay) = (ox + n.x, oy + n.y);
    n.x = ax + n.bx;
    n.y = ay + n.by;
    if matches!(n.ty.as_str(), "note" | "prompt" | "context") {
        let st = note_style(n);
        let body = n.s("content").unwrap_or("").to_string();
        n.lines = fonts.wrap(&body, (n.w - 24.0).max(10.0), &st);
    }
    let (fx, fy, fw, fh, is_frame) = (n.x, n.y, n.w, n.h, n.ty == "frame");
    for k in &mut n.kids {
        place(k, ax, ay, fonts);
        if is_frame && k.w > 0.0 && k.h > 0.0 {
            // Visual box (rotation/flip included) relative to the frame.
            let l = local_aabb(k);
            let (kx, ky, kw, kh) = (k.x - k.bx + l[0] - fx, k.y - k.by + l[1] - fy, l[2] - l[0], l[3] - l[1]);
            k.problem = if kx >= fw || ky >= fh || kx + kw <= 0.0 || ky + kh <= 0.0 {
                Some("fully clipped")
            } else if kx < -0.5 || ky < -0.5 || kx + kw > fw + 0.5 || ky + kh > fh + 0.5 {
                Some("partially clipped")
            } else {
                k.problem
            };
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::resolve::Resolver;
    use serde_json::{json, Map};

    fn lay(node: Value) -> RNode {
        let root = json!({"children": [node]});
        let empty = Map::new();
        let r = Resolver::new(&root, &[], &empty);
        let mut n = r.resolve(&root["children"][0]).unwrap();
        let mut fonts = Fonts::new(std::env::temp_dir().join("nalarupa-test-fonts"));
        fonts.online = false;
        layout(&mut n, &mut fonts);
        n
    }

    fn rect(w: Value, h: Value) -> Value {
        json!({"type": "rectangle", "width": w, "height": h})
    }

    #[test]
    fn flex_fill_and_justify() {
        let n = lay(json!({"type":"frame","id":"f","x":10,"y":20,"width":300,"height":100,"padding":10,"gap":10,
            "justifyContent":"space_between","alignItems":"center","children":[
                {"type":"rectangle","id":"a","width":50,"height":20},
                {"type":"rectangle","id":"b","width":"fill_container","height":"fill_container"},
                {"type":"rectangle","id":"c","width":50,"height":40}]}));
        assert_eq!((n.w, n.h), (300.0, 100.0));
        let b = &n.kids[1];
        assert_eq!(b.w, 300.0 - 20.0 - 100.0 - 20.0);
        assert_eq!(b.h, 80.0);
        assert_eq!(n.kids[0].x, 20.0);
        assert_eq!(n.kids[0].y, 20.0 + 10.0 + 30.0);
        assert_eq!(n.kids[2].x + 50.0, 300.0);
    }

    #[test]
    fn fit_content_vertical_and_absolute() {
        let n = lay(json!({"type":"frame","id":"f","layout":"vertical","padding":[8,12],"gap":4,"children":[
            {"type":"rectangle","id":"a","width":100,"height":10},
            {"type":"rectangle","id":"b","width":"fill_container","height":10},
            {"type":"rectangle","id":"c","width":5,"height":5,"layoutPosition":"absolute","x":-10,"y":0}]}));
        assert_eq!((n.w, n.h), (124.0, 16.0 + 24.0));
        assert_eq!(n.kids[1].w, 100.0);
        assert_eq!(n.kids[2].x, -10.0);
        assert_eq!(n.kids[2].problem, Some("fully clipped"));
    }

    #[test]
    fn group_is_union_of_children() {
        let n = lay(json!({"type":"group","id":"g","x":5,"children":[{"type":"ellipse","id":"e","x":10,"y":10,"width":20,"height":30}]}));
        assert_eq!((n.x, n.y, n.w, n.h), (15.0, 10.0, 20.0, 30.0));
        assert_eq!((n.bx, n.by), (10.0, 10.0));
        assert_eq!(n.kids[0].x, 15.0);
        // In a row, the group's box (not its origin) lands on the slot.
        let row = lay(json!({"type":"frame","id":"r","padding":4,"children":[
            {"type":"group","id":"g","children":[{"type":"rectangle","id":"a","x":-7,"y":3,"width":10,"height":10}]}]}));
        assert_eq!((row.w, row.h), (18.0, 18.0));
        assert_eq!((row.kids[0].x, row.kids[0].kids[0].x), (4.0, 4.0));
    }

    #[test]
    fn fill_children_do_not_size_fit_parents() {
        // [A, Fill] in a fit row: width = A + padding, the fill child gets max(1, -gap) = 1.
        let n = lay(json!({"type":"frame","id":"f","gap":8,"padding":2,"children":[rect(json!(40), json!(10)), rect(json!("fill_container"), json!("fill_container(99)"))]}));
        assert_eq!((n.w, n.h), (44.0, 14.0));
        assert_eq!((n.kids[1].w, n.kids[1].h), (1.0, 10.0));
        // Only fill children: the fit parent keeps its fallback.
        let n = lay(json!({"type":"frame","id":"f","width":"fit_content(30)","children":[rect(json!("fill_container"), json!(5))]}));
        assert_eq!((n.w, n.kids[0].w), (30.0, 30.0));
        // layout:none + fit_content keeps the fallback instead of hugging the children.
        let n = lay(json!({"type":"frame","id":"f","layout":"none","children":[rect(json!(40), json!(10))]}));
        assert_eq!((n.w, n.h), (0.0, 0.0));
        // Three-value padding is invalid → 0.
        let n = lay(json!({"type":"frame","id":"f","padding":[1,2,3],"children":[rect(json!(40), json!(10))]}));
        assert_eq!((n.w, n.h), (40.0, 10.0));
    }

    #[test]
    fn space_distribution_ignores_gap() {
        let row = |j: &str, w: f64| lay(json!({"type":"frame","id":"f","width":w,"gap":50,"justifyContent":j,"children":[rect(json!(20), json!(5)), rect(json!(20), json!(5)), rect(json!(20), json!(5))]}));
        let n = row("space_between", 100.0);
        assert_eq!(n.kids.iter().map(|k| k.x).collect::<Vec<_>>(), [0.0, 40.0, 80.0]);
        let n = row("space_around", 90.0);
        assert_eq!(n.kids.iter().map(|k| k.x).collect::<Vec<_>>(), [5.0, 35.0, 65.0]);
        // Overflow makes the spacing negative instead of clamping.
        let n = row("space_between", 40.0);
        assert_eq!(n.kids.iter().map(|k| k.x).collect::<Vec<_>>(), [0.0, 10.0, 20.0]);
    }

    #[test]
    fn rotated_and_flipped_children_use_their_bounding_box() {
        let n = lay(json!({"type":"frame","id":"f","children":[
            {"type":"rectangle","id":"a","width":20,"height":10,"rotation":90},
            {"type":"rectangle","id":"b","width":20,"height":10,"flipX":true}]}));
        assert!((n.w - 30.0).abs() < 1e-9 && (n.h - 20.0).abs() < 1e-9);
        // 90° CCW about the origin: box spans (0..10, -20..0) locally, so the origin sits at y = 20.
        let a = &n.kids[0];
        assert!(a.x.abs() < 1e-9 && (a.y - 20.0).abs() < 1e-9);
        // flipX mirrors about the origin: the origin is the right edge of the slot.
        assert!((n.kids[1].x - 30.0).abs() < 1e-9);
    }

    #[test]
    fn text_in_layout() {
        let n = lay(json!({"type":"frame","id":"f","width":60,"layout":"vertical","children":[
            {"type":"text","id":"t","content":"one two three four","textGrowth":"fixed-width","width":"fill_container","lineHeight":1.5},
            {"type":"text","id":"u","content":"auto text never wraps","width":"fill_container"}]}));
        let (t, u) = (&n.kids[0], &n.kids[1]);
        assert_eq!(t.w, 60.0);
        assert!(t.lines.len() > 1 && t.lines.iter().all(|l| l.width <= 60.01));
        assert_eq!(t.h, t.lines.len() as f64 * 21.0);
        assert_eq!(u.lines.len(), 1);
        assert!(u.w > 60.0 && u.w.fract() == 0.0, "auto text is intrinsic and ceiled");
        assert_eq!(n.h, t.h + u.h);
        // fixed-width without a width wraps at 0: one word per line.
        let z = lay(json!({"type":"text","id":"z","content":"a b c","textGrowth":"fixed-width"}));
        assert_eq!(z.lines.len(), 3);
    }
}
