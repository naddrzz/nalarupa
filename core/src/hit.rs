//! Hit surface of a painted shape: a click lands on what the shape paints, not on its rectangular box.

use crate::resolve::{num, RNode};
use crate::svg::{color, fills, geometry};
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

/// How an open subpath's ends paint (SVG's default is butt).
#[derive(Clone, Copy, PartialEq)]
enum Cap {
    Butt,
    Round,
    Square,
}

struct Poly {
    pts: Vec<(f64, f64)>,
    closed: bool,
}

struct Stroke {
    /// Per side [top, right, bottom, left] for a rectangle; other shapes paint the top width all round.
    widths: [f64; 4],
    align: Align,
    cap: Cap,
}

/// Outline of a node in its box coordinates: the filled area and/or the stroke band that paints.
pub struct Shape {
    polys: Vec<Poly>,
    evenodd: bool,
    filled: bool,
    stroke: Option<Stroke>,
    /// The box of a rectangle whose sides have different stroke widths: its band is tested side by side.
    sides: Option<(f64, f64, f64, f64)>,
}

/// Whether a paint list paints anything: an enabled paint that is not fully transparent.
fn paints(v: Option<&Value>) -> bool {
    let opaque = |c: Option<&str>| c.and_then(color).is_some_and(|c| c.1 > 0.0);
    fills(v).iter().any(|f| match f {
        Value::String(c) => opaque(Some(c)),
        Value::Object(o) => {
            o.get("opacity").and_then(num).unwrap_or(1.0) > 0.0
                && (o.get("type").and_then(Value::as_str).unwrap_or("color") != "color" || opaque(o.get("color").and_then(Value::as_str)))
        }
        _ => false,
    })
}

impl Shape {
    /// `None` when the box is the surface: a rectangle with a fill, or a shape that paints nothing
    /// (it stays selectable by its box).
    pub fn of(n: &RNode) -> Option<Self> {
        if !matches!(n.ty.as_str(), "rectangle" | "ellipse" | "polygon" | "path" | "line") {
            return None;
        }
        let rect = n.ty == "rectangle";
        let filled = n.ty != "line" && paints(n.p.get("fill"));
        // Like the renderer: a rectangle strokes each side at its own width, other shapes use the top width.
        let widths = match n.p.get("strokeWidth") {
            Some(Value::Object(s)) => {
                let g = |k: &str| s.get(k).and_then(num).unwrap_or(0.0).max(0.0);
                [g("top"), g("right"), g("bottom"), g("left")]
            }
            Some(v) => [num(v).unwrap_or(0.0).max(0.0); 4],
            None => [0.0; 4],
        };
        let widths = if rect { widths } else { [widths[0]; 4] };
        let stroked = widths.iter().any(|w| *w > 0.0) && paints(n.p.get("stroke"));
        if !stroked && (!filled || rect) || filled && rect {
            return None;
        }
        let geo = geometry(n);
        let polys = flatten(&geo.d);
        if polys.is_empty() {
            return None;
        }
        let align = match n.s("strokeAlignment") {
            _ if !geo.closed => Align::Center,
            Some("inner" | "inside") => Align::Inner,
            Some("outer" | "outside") => Align::Outer,
            _ => Align::Center,
        };
        let cap = match n.s("strokeLinecap") {
            Some("round") => Cap::Round,
            Some("square") => Cap::Square,
            _ => Cap::Butt,
        };
        let sides = (rect && widths.iter().any(|w| *w != widths[0])).then_some((n.x, n.y, n.w, n.h));
        Some(Shape { polys, evenodd: geo.evenodd, filled, stroke: stroked.then_some(Stroke { widths, align, cap }), sides })
    }

    pub fn hits(&self, x: f64, y: f64) -> bool {
        let inside = self.contains(x, y);
        if self.filled && inside {
            return true;
        }
        let Some(s) = &self.stroke else { return false };
        if let Some(b) = self.sides {
            return s.on_sides(b, x, y);
        }
        let width = s.widths[0];
        let reach = s.reach(width);
        // Past an open end a butt cap paints nothing (only the pick slack), a square one half the width.
        let overhang = match s.cap {
            Cap::Round => None,
            Cap::Butt => Some(reach - width / 2.0),
            Cap::Square => Some(reach),
        };
        self.near(x, y, reach, overhang) && match s.align {
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

    /// Whether the point is within `reach` of a stroked segment. Joins are round; past an open subpath's end
    /// the point may lie `overhang` beyond it (flat cap), or `reach` from the end point when `overhang` is None.
    fn near(&self, x: f64, y: f64, reach: f64, overhang: Option<f64>) -> bool {
        for p in &self.polys {
            let segments = if p.closed { p.pts.len() } else { p.pts.len() - 1 };
            for i in 0..segments {
                let (a, b) = (p.pts[i], p.pts[(i + 1) % p.pts.len()]);
                let (dx, dy) = (b.0 - a.0, b.1 - a.1);
                let len = dx * dx + dy * dy;
                let t = if len > 0.0 { ((x - a.0) * dx + (y - a.1) * dy) / len } else { 0.0 };
                let at = |t: f64| (x - (a.0 + t * dx)).hypot(y - (a.1 + t * dy));
                let past_end = !p.closed && (i == 0 && t < 0.0 || i + 1 == segments && t > 1.0);
                let hit = match overhang {
                    _ if (0.0..=1.0).contains(&t) => at(t) <= reach,
                    Some(over) if past_end => at(t) <= reach && (if t < 0.0 { -t } else { t - 1.0 }) * len.sqrt() <= over,
                    _ => at(t.clamp(0.0, 1.0)) <= reach,
                };
                if hit {
                    return true;
                }
            }
        }
        false
    }
}

impl Stroke {
    /// How far from the outline a stroke of `width` is hit: the painted band, at least the pick slack.
    fn reach(&self, width: f64) -> f64 {
        if self.align == Align::Center { width / 2.0 } else { width }.max(MIN_REACH)
    }

    /// A rectangle's band when its sides differ: each side reaches in and/or out by its own width, and a
    /// side of width 0 paints nothing.
    fn on_sides(&self, (bx, by, bw, bh): (f64, f64, f64, f64), x: f64, y: f64) -> bool {
        let [t, r, b, l] = self.widths.map(|w| {
            let reach = if w > 0.0 { self.reach(w) } else { 0.0 };
            match self.align {
                Align::Inner => (reach, 0.0),
                Align::Center => (reach, reach),
                Align::Outer => (0.0, reach),
            }
        });
        let outside = x < bx - l.1 || x > bx + bw + r.1 || y < by - t.1 || y > by + bh + b.1;
        let hole = x > bx + l.0 && x < bx + bw - r.0 && y > by + t.0 && y < by + bh - b.0;
        !outside && !hole
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
    fn open_ends_follow_the_cap() {
        let line = |cap: &str| shape("path", json!({"geometry": "M0 0 L100 0", "stroke": "#000000", "strokeWidth": 10, "strokeLinecap": cap}), (0.0, 0.0, 100.0, 0.0)).unwrap();
        // 4 past the end, 3 off the axis: inside a round cap (radius 5), outside a butt one, inside a square one.
        assert!(!line("butt").hits(104.0, 3.0) && line("round").hits(104.0, 3.0) && line("square").hits(104.0, 3.0));
        assert!(!line("round").hits(104.0, 4.0) && line("square").hits(104.0, 4.0), "a square cap has corners");
        assert!(line("butt").hits(100.0, 4.0) && !line("butt").hits(101.0, 0.0), "a butt end is flat");
    }

    #[test]
    fn rectangle_sides_keep_their_own_widths() {
        let p = json!({"stroke": "#000000", "strokeWidth": {"top": 6}, "strokeAlignment": "inner"});
        let s = shape("rectangle", p, (0.0, 0.0, 100.0, 100.0)).unwrap();
        assert!(s.hits(50.0, 4.0), "the painted top");
        assert!(!s.hits(1.0, 50.0) && !s.hits(50.0, 99.0) && !s.hits(50.0, 50.0), "the unpainted sides and the inside");
    }

    #[test]
    fn boxes_stay_the_surface_when_the_box_is_painted_or_nothing_is() {
        assert!(shape("rectangle", json!({"fill": "#ff0000", "stroke": "#000000", "strokeWidth": 2}), (0.0, 0.0, 10.0, 10.0)).is_none());
        assert!(shape("rectangle", json!({}), (0.0, 0.0, 10.0, 10.0)).is_none());
        assert!(shape("ellipse", json!({}), (0.0, 0.0, 10.0, 10.0)).is_none());
        assert!(shape("ellipse", json!({"fill": "#00000000"}), (0.0, 0.0, 10.0, 10.0)).is_none(), "a transparent fill paints nothing");
        assert!(shape("ellipse", json!({"fill": {"type": "color", "color": "#ff0000", "opacity": 0}}), (0.0, 0.0, 10.0, 10.0)).is_none());
        assert!(shape("path", json!({"geometry": "", "fill": "#ff0000"}), (0.0, 0.0, 10.0, 10.0)).is_none(), "an empty path paints nothing");
        let frame = shape("rectangle", json!({"fill": "#ffffff00", "stroke": "#000000", "strokeWidth": 2}), (0.0, 0.0, 100.0, 100.0)).unwrap();
        assert!(!frame.hits(50.0, 50.0), "a rectangle with a transparent fill is hit on its stroke only");
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
