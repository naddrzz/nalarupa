//! Hit surface of a painted shape: a click lands on what the shape paints, not on its rectangular box.

use crate::resolve::{num, RNode};
use crate::svg::{fills, geometry};
use serde_json::Value;

/// Pick slack around a thin stroke, in document units.
const MIN_REACH: f64 = 2.0;
/// Segments a curve is flattened into.
const CURVE_STEPS: usize = 16;

#[derive(Clone, Copy, PartialEq)]
enum Align {
    Inner,
    Center,
    Outer,
}

struct Poly {
    pts: Vec<(f64, f64)>,
    closed: bool,
}

/// Outline of a node in its box coordinates: the filled area and/or the stroke band that paints.
pub struct Shape {
    polys: Vec<Poly>,
    evenodd: bool,
    filled: bool,
    stroke: Option<(f64, Align)>,
}

impl Shape {
    /// `None` when the box is the surface: a rectangle with a fill, or a shape that paints nothing
    /// (it stays selectable by its box).
    pub fn of(n: &RNode) -> Option<Self> {
        if !matches!(n.ty.as_str(), "rectangle" | "ellipse" | "polygon" | "path" | "line") {
            return None;
        }
        let filled = n.ty != "line" && !fills(n.p.get("fill")).is_empty();
        let width = match n.p.get("strokeWidth") {
            Some(Value::Object(s)) => s.values().filter_map(num).fold(0.0, f64::max),
            Some(v) => num(v).unwrap_or(0.0),
            None => 0.0,
        };
        let stroked = width > 0.0 && !fills(n.p.get("stroke")).is_empty();
        if !stroked && (!filled || n.ty == "rectangle") || filled && n.ty == "rectangle" {
            return None;
        }
        let geo = geometry(n);
        let align = match n.s("strokeAlignment") {
            _ if !geo.closed => Align::Center,
            Some("inner" | "inside") => Align::Inner,
            Some("outer" | "outside") => Align::Outer,
            _ => Align::Center,
        };
        Some(Shape { polys: flatten(&geo.d), evenodd: geo.evenodd, filled, stroke: stroked.then_some((width, align)) })
    }

    pub fn hits(&self, x: f64, y: f64) -> bool {
        let inside = self.contains(x, y);
        if self.filled && inside {
            return true;
        }
        let Some((width, align)) = self.stroke else { return false };
        let reach = if align == Align::Center { width / 2.0 } else { width }.max(MIN_REACH);
        self.distance(x, y) <= reach && match align {
            Align::Inner => inside,
            Align::Center => true,
            Align::Outer => !inside,
        }
    }

    /// Whether the fill rule paints the point (every subpath is closed implicitly).
    fn contains(&self, x: f64, y: f64) -> bool {
        let (mut winding, mut crossings) = (0, 0);
        for p in &self.polys {
            for (i, &a) in p.pts.iter().enumerate() {
                let b = p.pts[(i + 1) % p.pts.len()];
                if (a.1 <= y) != (b.1 <= y) && a.0 + (y - a.1) / (b.1 - a.1) * (b.0 - a.0) > x {
                    crossings += 1;
                    winding += if b.1 > a.1 { 1 } else { -1 };
                }
            }
        }
        if self.evenodd { crossings % 2 == 1 } else { winding != 0 }
    }

    /// Distance to the nearest stroked segment.
    fn distance(&self, x: f64, y: f64) -> f64 {
        let mut best = f64::MAX;
        for p in &self.polys {
            let segments = if p.closed { p.pts.len() } else { p.pts.len().saturating_sub(1) };
            for i in 0..segments {
                let (a, b) = (p.pts[i], p.pts[(i + 1) % p.pts.len()]);
                let (dx, dy) = (b.0 - a.0, b.1 - a.1);
                let len = dx * dx + dy * dy;
                let t = if len > 0.0 { (((x - a.0) * dx + (y - a.1) * dy) / len).clamp(0.0, 1.0) } else { 0.0 };
                best = best.min((x - (a.0 + t * dx)).hypot(y - (a.1 + t * dy)));
            }
        }
        best
    }
}

/// Polylines of an SVG path (arcs arrive as curves from the simplifying parser).
fn flatten(d: &str) -> Vec<Poly> {
    let mut polys: Vec<Poly> = Vec::new();
    let (mut cur, mut start) = ((0.0, 0.0), (0.0, 0.0));
    // A segment after a close continues from the subpath's start.
    fn push(polys: &mut Vec<Poly>, start: (f64, f64), p: (f64, f64)) {
        match polys.last_mut().filter(|l| !l.closed) {
            Some(l) => l.pts.push(p),
            None => polys.push(Poly { pts: vec![start, p], closed: false }),
        }
    }
    for seg in svgtypes::SimplifyingPathParser::from(d) {
        let Ok(seg) = seg else { break };
        match seg {
            svgtypes::SimplePathSegment::MoveTo { x, y } => {
                polys.push(Poly { pts: vec![(x, y)], closed: false });
                (cur, start) = ((x, y), (x, y));
            }
            svgtypes::SimplePathSegment::LineTo { x, y } => {
                push(&mut polys, start, (x, y));
                cur = (x, y);
            }
            svgtypes::SimplePathSegment::CurveTo { x1, y1, x2, y2, x, y } => {
                for i in 1..=CURVE_STEPS {
                    let (t, s) = (i as f64 / CURVE_STEPS as f64, 1.0 - i as f64 / CURVE_STEPS as f64);
                    let at = |p0: f64, p1: f64, p2: f64, p3: f64| s * s * s * p0 + 3.0 * s * s * t * p1 + 3.0 * s * t * t * p2 + t * t * t * p3;
                    push(&mut polys, start, (at(cur.0, x1, x2, x), at(cur.1, y1, y2, y)));
                }
                cur = (x, y);
            }
            svgtypes::SimplePathSegment::Quadratic { x1, y1, x, y } => {
                for i in 1..=CURVE_STEPS {
                    let (t, s) = (i as f64 / CURVE_STEPS as f64, 1.0 - i as f64 / CURVE_STEPS as f64);
                    let at = |p0: f64, p1: f64, p2: f64| s * s * p0 + 2.0 * s * t * p1 + t * t * p2;
                    push(&mut polys, start, (at(cur.0, x1, x), at(cur.1, y1, y)));
                }
                cur = (x, y);
            }
            svgtypes::SimplePathSegment::ClosePath => {
                if let Some(l) = polys.last_mut() {
                    l.closed = true;
                }
                cur = start;
            }
        }
    }
    polys.retain(|p| p.pts.len() > 1);
    polys
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn shape(ty: &str, p: Value, (x, y, w, h): (f64, f64, f64, f64)) -> Option<Shape> {
        let mut n = RNode::leaf("n".into(), ty, p.as_object().cloned().unwrap_or_default(), None);
        (n.x, n.y, n.w, n.h) = (x, y, w, h);
        Shape::of(&n)
    }

    #[test]
    fn ellipse_hits_its_area_not_its_box() {
        let s = shape("ellipse", json!({"fill": "#ff0000"}), (100.0, 100.0, 100.0, 100.0)).unwrap();
        assert!(s.hits(150.0, 150.0) && s.hits(105.0, 150.0));
        assert!(!s.hits(103.0, 103.0) && !s.hits(197.0, 197.0), "box corners are empty");
        let ring = shape("ellipse", json!({"fill": "#ff0000", "innerRadius": 0.5}), (100.0, 100.0, 100.0, 100.0)).unwrap();
        assert!(ring.hits(110.0, 150.0) && !ring.hits(150.0, 150.0), "the hole is not painted");
    }

    #[test]
    fn open_stroke_hits_within_its_width() {
        let line = json!({"geometry": "M0 0 L150 150", "stroke": "#000000", "strokeWidth": 10});
        let s = shape("path", line, (220.0, 40.0, 150.0, 150.0)).unwrap();
        assert!(s.hits(300.0, 120.0) && s.hits(302.0, 118.0) && s.hits(300.0, 125.0));
        assert!(!s.hits(360.0, 50.0) && !s.hits(300.0, 140.0), "far from the line");
        let line = shape("line", json!({"stroke": "#000000", "strokeWidth": 1}), (0.0, 0.0, 100.0, 100.0)).unwrap();
        assert!(line.hits(50.0, 51.5) && !line.hits(50.0, 56.0), "thin lines get a little slack");
    }

    #[test]
    fn stroke_only_rectangle_is_a_frame() {
        let p = json!({"stroke": "#000000", "strokeWidth": 4});
        let s = shape("rectangle", p, (0.0, 0.0, 100.0, 100.0)).unwrap();
        assert!(s.hits(1.0, 50.0) && s.hits(50.0, 99.0) && !s.hits(50.0, 50.0), "only the band is painted");
        let inner = json!({"stroke": "#000000", "strokeWidth": 4, "strokeAlignment": "inner"});
        let s = shape("rectangle", inner, (0.0, 0.0, 100.0, 100.0)).unwrap();
        assert!(s.hits(3.0, 50.0) && !s.hits(-3.0, 50.0));
    }

    #[test]
    fn boxes_stay_the_surface_when_the_box_is_painted_or_nothing_is() {
        assert!(shape("rectangle", json!({"fill": "#ff0000", "stroke": "#000000", "strokeWidth": 2}), (0.0, 0.0, 10.0, 10.0)).is_none());
        assert!(shape("rectangle", json!({}), (0.0, 0.0, 10.0, 10.0)).is_none());
        assert!(shape("ellipse", json!({}), (0.0, 0.0, 10.0, 10.0)).is_none());
        assert!(shape("frame", json!({"stroke": "#000000", "strokeWidth": 2}), (0.0, 0.0, 10.0, 10.0)).is_none());
    }

    #[test]
    fn filled_path_follows_its_rule() {
        let d = "M0 0H100V100H0ZM25 25H75V75H25Z";
        let s = shape("path", json!({"geometry": d, "fill": "#ff0000", "fillRule": "evenodd"}), (0.0, 0.0, 100.0, 100.0)).unwrap();
        assert!(s.hits(10.0, 10.0) && !s.hits(50.0, 50.0));
        let s = shape("path", json!({"geometry": d, "fill": "#ff0000"}), (0.0, 0.0, 100.0, 100.0)).unwrap();
        assert!(s.hits(50.0, 50.0), "nonzero fills the same-wound inner square");
    }
}
