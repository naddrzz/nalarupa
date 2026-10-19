//! Canvas rasterizer on vello_cpu. A layer's usvg tree is converted once (at sync) into a flat
//! display list of kurbo paths, paints, layers and decoded images; each frame encodes only the
//! items that reach the band being painted. The effects svg.rs emits are drawn by vello too: drop
//! and inner shadows and layer blurs as filter layers (recognized from their filter primitives),
//! rect shadows tagged `nrsh:` analytically, and background blurs tagged `nrbb:` from a blurred
//! render of everything beneath them (the backdrop). What vello cannot reproduce exactly
//! (other filters, masks other than knockouts, SVG images, compound clips, patterns other than
//! images) is drawn by resvg for that subtree into a pixmap covering its device box and composited
//! as an image at an integer offset.

use resvg::{tiny_skia, usvg};
use std::collections::HashMap;
use std::ops::Range;
use std::sync::Arc;
use vello_cpu::color::{AlphaColor, Srgb};
use vello_cpu::filter_effects::{EdgeMode, Filter, FilterPrimitive};
use vello_cpu::kurbo::{Affine, BezPath, Cap, Join, Point, Rect as KRect, RoundedRect, Shape, Stroke, Vec2};
use vello_cpu::peniko::{BlendMode, ColorStop, Compose, ColorStops, Extend, Fill, Gradient, GradientKind, ImageQuality, ImageSampler, InterpolationAlphaSpace, LinearGradientPosition, Mix, RadialGradientPosition};
use vello_cpu::{Image, ImageSource, Level, PaintType, PixelMetadata, Pixmap, PixmapMut, RasterizerSettings, RenderContext, RenderSettings, Resources, TargetInit};

/// Doc-space (x, y, w, h).
type Rect = (f64, f64, f64, f64);

/// One layer's display list.
#[derive(Default)]
pub struct Scene {
    items: Vec<Item>,
    paths: Vec<BezPath>,
    /// Doc-space box of each background blur and how far around it content feeds its blur.
    backdrops: Vec<(Rect, f64)>,
}

struct Item {
    /// Doc-space area the item can paint, for culling.
    bbox: Rect,
    /// Local → doc transform.
    ts: Affine,
    op: Op,
}

enum Op {
    Fill { path: u32, paint: Paint, rule: Fill },
    Stroke { path: u32, paint: Paint, stroke: Box<Stroke> },
    /// Image pixels drawn over (0, 0, width, height) in local space.
    Image { pix: Arc<Pixmap>, quality: ImageQuality },
    /// Isolated group (clip, blend, opacity, filter) up to the `Pop` at `end`.
    Push { clip: Option<(u32, Fill)>, blend: BlendMode, opacity: f32, filter: Option<Box<Effect>>, end: usize },
    Pop,
    /// Subtree drawn by resvg: child indices from the root (text descends into its flattened
    /// group), under the parent's absolute transform.
    Fallback { at: Box<[u32]>, parent: tiny_skia::Transform, blend: BlendMode },
    Shadow(Box<Shadow>),
    /// Background blur: what lies beneath (the scenes below and this one's items before `from`, or
    /// the enclosing opacity/blur layer's content), blurred by `sigma` (doc units) over the item's box.
    Backdrop { sigma: f64, from: usize },
}

#[derive(Clone)]
struct Paint {
    kind: PaintType,
    ts: Affine,
}

/// A filter layer.
struct Effect {
    prim: FilterPrimitive,
    /// Doc-space distance the filter moves content: items that far outside the band still feed it.
    grow: f64,
    /// A layer blur: a saveLayer, which background blurs inside read from.
    layer: bool,
}

/// Rounded-rect drop or inner shadows (see `svg::Svg::drop_shadows`), in local space.
struct Shadow {
    inner: bool,
    rect: KRect,
    radius: f64,
    /// Knockout region of drop shadows: they paint here, outside the outline.
    region: KRect,
    /// (offset, sigma, color)
    list: Vec<(Vec2, f64, AlphaColor<Srgb>)>,
}

impl Scene {
    pub fn new(tree: &usvg::Tree) -> Scene {
        let mut b = Build { s: Scene::default(), images: HashMap::new(), at: Vec::new(), inside: None, failed: false, alpha: false };
        b.children(tree.root(), tree.root().abs_transform());
        b.s
    }

    /// Doc-space boxes of the background blurs and how far around each its input reaches.
    pub fn backdrops(&self) -> &[((f64, f64, f64, f64), f64)] {
        &self.backdrops
    }
}

/// What a draw pass reads besides its own scene.
struct Env<'a> {
    scenes: &'a [(&'a Scene, &'a usvg::Tree)],
    bg: [u8; 3],
    /// Contexts for backdrop renders, one per nesting depth.
    aux: &'a mut Vec<Option<Aux>>,
    depth: usize,
}

struct Aux {
    ctx: RenderContext,
    res: Resources,
}

impl Aux {
    fn new() -> Aux {
        Aux { ctx: RenderContext::new_with(1, 1, settings()), res: Resources::new() }
    }
}

/// Encode items `range` of scene `si`. `base` is the opacity/blur layer (a `Push` index in this scene)
/// the range starts inside, if any; layers still open at the end of the range are closed.
fn draw(ctx: &mut RenderContext, env: &mut Env, f: &Frame, si: usize, range: Range<usize>, base: Option<usize>) {
    let (scene, tree) = env.scenes[si];
    let mut view = f.view;
    // Open pushes (index, view outside them).
    let mut open: Vec<(usize, Rect)> = Vec::new();
    let mut i = range.start;
    while i < range.end {
        let it = &scene.items[i];
        i += 1;
        if !matches!(it.op, Op::Pop) && !overlaps(it.bbox, view) {
            if let Op::Push { end, .. } = it.op {
                i = end + 1;
            }
            continue;
        }
        let t = f.vt * it.ts;
        match &it.op {
            Op::Fill { path, paint, rule } => {
                ctx.set_transform(t);
                set_paint(ctx, paint);
                ctx.set_fill_rule(*rule);
                ctx.fill_path(&scene.paths[*path as usize]);
            }
            Op::Stroke { path, paint, stroke } => {
                ctx.set_transform(t);
                set_paint(ctx, paint);
                ctx.set_stroke((**stroke).clone());
                ctx.stroke_path(&scene.paths[*path as usize]);
            }
            Op::Image { pix, quality } => {
                ctx.set_transform(t);
                draw_image(ctx, pix.clone(), *quality);
            }
            Op::Push { clip, blend, opacity, filter, .. } => {
                ctx.set_transform(t);
                if let Some((_, rule)) = clip {
                    ctx.set_fill_rule(*rule);
                }
                ctx.push_layer(clip.map(|(p, _)| &scene.paths[p as usize]), Some(*blend), Some(*opacity), None, filter.as_ref().map(|e| e.at(t)));
                open.push((i - 1, view));
                if let Some(e) = filter {
                    view = inflate(view, e.grow);
                }
            }
            Op::Pop => {
                if let Some((_, v)) = open.pop() {
                    view = v;
                    ctx.pop_layer();
                }
            }
            Op::Fallback { at, parent, blend } => fallback(ctx, tree, at, *parent, *blend, f, view),
            Op::Shadow(s) => shadow(ctx, s, t),
            Op::Backdrop { sigma, from } => {
                let layer_push = |k: usize| matches!(&scene.items[k].op, Op::Push { opacity, filter, .. } if *opacity < 1.0 || filter.as_ref().is_some_and(|e| e.layer));
                let layer = open.iter().rev().map(|o| o.0).filter(|&k| k < *from).find(|&k| layer_push(k)).or(base);
                backdrop(ctx, env, f, si, *from, layer, it.bbox, *sigma, view);
            }
        }
    }
    for _ in open {
        ctx.pop_layer();
    }
}

/// Draw the blurred backdrop over the device pixels of `bbox` in `view`: render what lies beneath
/// around them on a second context inside a blur filter layer, as a saveLayer with a
/// backdrop filter reads its current layer (the canvas, or the innermost opacity/blur layer).
#[allow(clippy::too_many_arguments)]
fn backdrop(ctx: &mut RenderContext, env: &mut Env, f: &Frame, si: usize, from: usize, layer: Option<usize>, bbox: Rect, sigma: f64, view: Rect) {
    let z = f.zoom;
    let (ax, ay) = (bbox.0.max(view.0), bbox.1.max(view.1));
    let (bx, by) = ((bbox.0 + bbox.2).min(view.0 + view.2), (bbox.1 + bbox.3).min(view.1 + view.3));
    if bx <= ax || by <= ay {
        return;
    }
    let sd = sigma * z;
    let pad = (3.0 * sd).ceil() as i64 + 2;
    let x0 = (ax * z).floor() as i64 - pad;
    let y0 = (ay * z).floor() as i64 - pad;
    let (x1, y1) = ((bx * z).ceil() as i64 + pad, (by * z).ceil() as i64 + pad);
    let (Ok(w), Ok(h)) = (u16::try_from(x1 - x0), u16::try_from(y1 - y0)) else { return };
    let sub = Frame {
        vt: Affine::new([z, 0.0, 0.0, z, -x0 as f64, -y0 as f64]),
        view: (x0 as f64 / z, y0 as f64 / z, w as f64 / z, h as f64 / z),
        zoom: z,
        x0: x0 as i32,
        y0: y0 as i32,
    };
    let depth = env.depth;
    if env.aux.len() <= depth {
        env.aux.resize_with(depth + 1, || None);
    }
    let mut aux = env.aux[depth].take().unwrap_or_else(Aux::new);
    aux.ctx.reset_and_resize(w, h);
    // The blur is in device pixels: nothing may scale it.
    aux.ctx.set_transform(Affine::IDENTITY);
    aux.ctx.push_filter_layer(Filter::from_primitive(FilterPrimitive::GaussianBlur { std_deviation: sd as f32, edge_mode: EdgeMode::Duplicate }));
    let mut sub_env = Env { scenes: env.scenes, bg: env.bg, aux: &mut *env.aux, depth: depth + 1 };
    match layer {
        Some(p) => draw(&mut aux.ctx, &mut sub_env, &sub, si, p + 1..from, Some(p)),
        None => {
            aux.ctx.set_paint(AlphaColor::<Srgb>::from_rgba8(env.bg[0], env.bg[1], env.bg[2], 255));
            aux.ctx.fill_rect(&KRect::new(0.0, 0.0, w as f64, h as f64));
            for k in 0..si {
                draw(&mut aux.ctx, &mut sub_env, &sub, k, 0..env.scenes[k].0.items.len(), None);
            }
            draw(&mut aux.ctx, &mut sub_env, &sub, si, 0..from, None);
        }
    }
    aux.ctx.pop_layer();
    aux.ctx.flush();
    let mut pix = Pixmap::new(w, h);
    let settings = RasterizerSettings { target_init: TargetInit::Clear(AlphaColor::TRANSPARENT), ..Default::default() };
    aux.ctx.render_with(&mut pix, &mut aux.res, settings);
    env.aux[depth] = Some(aux);
    ctx.set_transform(Affine::translate(((x0 - f.x0 as i64) as f64, (y0 - f.y0 as i64) as f64)));
    draw_image(ctx, Arc::new(pix), ImageQuality::Low);
}

struct Build {
    s: Scene,
    /// Decoded images by data pointer: a pattern used by several shapes decodes once.
    images: HashMap<usize, Arc<Pixmap>>,
    /// Child indices from the root down to the node being converted.
    at: Vec<u32>,
    /// Inside a pattern or mask: items take its owner's box, and nothing can fall back.
    inside: Option<Rect>,
    failed: bool,
    /// Only the alpha of what is built matters (shadow sources, knockouts, blur masks).
    alpha: bool,
}

/// The svg.rs filters vello draws (see `effect_of`).
enum Fx {
    /// Gaussian blur of the source graphic: a layer blur.
    Blur(f64),
    /// The source alpha blurred, offset and flooded with one color: one drop shadow.
    Drop(Vec2, f64, AlphaColor<Srgb>),
    /// The offset, blurred inverse of the source alpha within `region`, kept inside the source.
    Inner(Vec2, f64, AlphaColor<Srgb>, KRect),
    /// The source alpha flooded black (group outlines): only met where alpha alone matters.
    Black,
}

impl Build {
    fn children(&mut self, g: &usvg::Group, abs: tiny_skia::Transform) {
        for (i, n) in g.children().iter().enumerate() {
            self.at.push(i as u32);
            self.node(n, abs);
            self.at.pop();
        }
    }

    fn node(&mut self, n: &usvg::Node, abs: tiny_skia::Transform) {
        match n {
            usvg::Node::Group(sub) => self.group(n, sub, abs),
            usvg::Node::Text(t) => self.group(n, t.flattened(), abs),
            usvg::Node::Path(p) => self.path(n, p, abs),
            usvg::Node::Image(img) => self.image(n, img, abs),
        }
    }

    /// Mask content (not addressable by `at`, so nothing in it can fall back), alpha only.
    fn mask_shapes(&mut self, nodes: &[usvg::Node], abs: tiny_skia::Transform, bbox: Rect) {
        let saved = (self.inside.replace(bbox), std::mem::replace(&mut self.alpha, true));
        for n in nodes {
            self.node(n, abs);
        }
        (self.inside, self.alpha) = saved;
    }

    fn push(&mut self, bbox: Rect, ts: Affine, op: Op) {
        self.s.items.push(Item { bbox: self.inside.unwrap_or(bbox), ts, op });
    }

    /// Open a layer; `close` it with the returned index.
    fn open(&mut self, bbox: Rect, ts: Affine, clip: Option<(u32, Fill)>, blend: BlendMode, opacity: f32, filter: Option<Effect>) -> usize {
        self.push(bbox, ts, Op::Push { clip, blend, opacity, filter: filter.map(Box::new), end: 0 });
        self.s.items.len() - 1
    }

    fn close(&mut self, start: usize, bbox: Rect) {
        let end = self.s.items.len();
        self.push(bbox, Affine::IDENTITY, Op::Pop);
        if let Op::Push { end: e, .. } = &mut self.s.items[start].op {
            *e = end;
        }
    }

    fn fallback(&mut self, n: &usvg::Node, parent: tiny_skia::Transform, blend: BlendMode) {
        if self.inside.is_some() {
            self.failed = true;
            return;
        }
        if let Some(b) = painted_box(n) {
            let op = Op::Fallback { at: self.at.clone().into(), parent, blend };
            self.push(rect(b.to_rect()), Affine::IDENTITY, op);
        }
    }

    fn group(&mut self, n: &usvg::Node, g: &usvg::Group, parent: tiny_skia::Transform) {
        let abs = parent.pre_concat(g.transform());
        if !g.should_isolate() {
            return self.children(g, abs);
        }
        let bbox = rect(g.abs_layer_bounding_box().to_rect());
        let blend = blend(g.blend_mode());
        if let Some(s) = shadow_of(g, abs) {
            return self.push(bbox, aff(abs), Op::Shadow(Box::new(s)));
        }
        if let Some(sigma) = g.id().strip_prefix("nrbb:").and_then(|s| s.rsplit(':').next()?.parse().ok()) {
            return self.backdrop(n, g, parent, abs, sigma);
        }
        let fx = match g.filters() {
            [] => None,
            [f] => match effect_of(f) {
                Some(Fx::Black) if self.alpha => None,
                Some(Fx::Black) | None => return self.fallback(n, parent, blend),
                fx => fx,
            },
            _ => return self.fallback(n, parent, blend),
        };
        let knock = g.mask().map(knockout);
        if knock.as_ref().is_some_and(|k| k.is_none() || g.clip_path().is_some()) {
            return self.fallback(n, parent, blend);
        }
        let (clip, ts) = match (g.clip_path(), &knock) {
            (Some(c), _) => match self.clip(c, abs) {
                Some((path, rule, ts)) => (Some((path, rule)), ts),
                None => return self.fallback(n, parent, blend),
            },
            // A knockout clips to its mask region.
            (None, Some(Some((r, _)))) => {
                let path = self.add(KRect::new(r.left() as f64, r.top() as f64, r.right() as f64, r.bottom() as f64).to_path(0.1));
                (Some((path, Fill::NonZero)), abs)
            }
            _ => (None, abs),
        };
        let (items, paths, failed) = (self.s.items.len(), self.s.paths.len(), std::mem::take(&mut self.failed));
        let opacity = g.opacity().get();
        // A filter alone needs no layer around its filter layer.
        let plain = clip.is_none() && blend == BlendMode::default() && opacity == 1.0 && knock.is_none();
        let start = (fx.is_none() || !plain).then(|| self.open(bbox, aff(ts), clip, blend, opacity, None));
        let s = abs_scale(abs);
        match fx {
            None | Some(Fx::Black) => self.children(g, abs),
            Some(Fx::Blur(sigma)) => {
                let prim = FilterPrimitive::GaussianBlur { std_deviation: sigma as f32, edge_mode: EdgeMode::None };
                let e = self.open(bbox, aff(abs), None, BlendMode::default(), 1.0, Some(Effect { prim, grow: 3.0 * sigma * s, layer: true }));
                self.children(g, abs);
                self.close(e, bbox);
            }
            Some(Fx::Drop(d, sigma, color)) => {
                let e = self.open(bbox, aff(abs), None, BlendMode::default(), 1.0, Some(drop_effect(d, sigma, color, s)));
                let alpha = std::mem::replace(&mut self.alpha, true);
                self.children(g, abs);
                self.alpha = alpha;
                self.close(e, bbox);
            }
            Some(Fx::Inner(d, sigma, color, region)) => self.inner(g, abs, bbox, drop_effect(d, sigma, color, s), region),
        }
        if let Some(Some((_, shapes))) = knock {
            // Erase where the black shapes cover.
            let k = self.open(bbox, Affine::IDENTITY, None, BlendMode::new(Mix::Normal, Compose::DestOut), 1.0, None);
            self.mask_shapes(shapes, abs, bbox);
            self.close(k, bbox);
        }
        if let Some(start) = start {
            self.close(start, bbox);
        }
        if std::mem::replace(&mut self.failed, failed) {
            // Mask content needs resvg: draw the whole group with it.
            self.s.items.truncate(items);
            self.s.paths.truncate(paths);
            self.fallback(n, parent, blend);
        }
    }

    /// svg.rs inner shadow: the region minus the source, shadowed (offset, blurred, colored),
    /// then kept where the source is.
    fn inner(&mut self, g: &usvg::Group, abs: tiny_skia::Transform, bbox: Rect, effect: Effect, region: KRect) {
        let ts = aff(abs);
        let path = self.add(region.to_path(0.1));
        // The clip bounds the destructive DestIn below.
        let c = self.open(bbox, ts, Some((path, Fill::NonZero)), BlendMode::default(), 1.0, None);
        let e = self.open(bbox, ts, None, BlendMode::default(), 1.0, Some(effect));
        self.push(bbox, ts, Op::Fill { path, paint: Paint { kind: AlphaColor::<Srgb>::BLACK.into(), ts: Affine::IDENTITY }, rule: Fill::NonZero });
        let alpha = std::mem::replace(&mut self.alpha, true);
        for mode in [Compose::DestOut, Compose::DestIn] {
            let k = self.open(bbox, Affine::IDENTITY, None, BlendMode::new(Mix::Normal, mode), 1.0, None);
            self.children(g, abs);
            self.close(k, bbox);
            if mode == Compose::DestOut {
                self.close(e, bbox);
            }
        }
        self.alpha = alpha;
        self.close(c, bbox);
    }

    /// svg.rs background blur (`<g id="nrbb:{uid}:{sigma}" mask=…>` around resvg's approximation): the
    /// blurred backdrop replaces what is under the alpha mask (drawn with Src through the
    /// node's mask path): erase under the mask, then add the backdrop masked.
    fn backdrop(&mut self, n: &usvg::Node, g: &usvg::Group, parent: tiny_skia::Transform, abs: tiny_skia::Transform, sigma: f64) {
        if self.alpha {
            // Adds nothing to an outline.
            return;
        }
        let Some(m) = g.mask().filter(|m| m.kind() == usvg::MaskType::Alpha && m.mask().is_none() && m.root().transform().is_identity()) else {
            return self.fallback(n, parent, BlendMode::default());
        };
        let Some(b) = m.rect().transform(abs).filter(|_| !m.root().children().is_empty()) else { return };
        let bbox = rect(b.to_rect());
        let (items, paths, failed) = (self.s.items.len(), self.s.paths.len(), std::mem::take(&mut self.failed));
        let from = items;
        let k = self.open(bbox, Affine::IDENTITY, None, BlendMode::new(Mix::Normal, Compose::DestOut), 1.0, None);
        self.mask_shapes(m.root().children(), abs, bbox);
        self.close(k, bbox);
        let path = self.add(KRect::new(bbox.0, bbox.1, bbox.0 + bbox.2, bbox.1 + bbox.3).to_path(0.1));
        // The clip bounds the destructive DestIn below.
        let c = self.open(bbox, Affine::IDENTITY, Some((path, Fill::NonZero)), BlendMode::new(Mix::Normal, Compose::Plus), 1.0, None);
        let sigma = sigma * abs_scale(abs);
        self.push(bbox, Affine::IDENTITY, Op::Backdrop { sigma, from });
        let i = self.open(bbox, Affine::IDENTITY, None, BlendMode::new(Mix::Normal, Compose::DestIn), 1.0, None);
        self.mask_shapes(m.root().children(), abs, bbox);
        self.close(i, bbox);
        self.close(c, bbox);
        if std::mem::replace(&mut self.failed, failed) {
            self.s.items.truncate(items);
            self.s.paths.truncate(paths);
            return self.fallback(n, parent, BlendMode::default());
        }
        self.s.backdrops.push((bbox, 3.0 * sigma));
    }

    /// A clip path that is one path (through plain transformed groups) becomes a clip layer.
    fn clip(&mut self, c: &usvg::ClipPath, abs: tiny_skia::Transform) -> Option<(u32, Fill, tiny_skia::Transform)> {
        if c.clip_path().is_some() {
            return None;
        }
        let mut ts = abs.pre_concat(c.transform());
        let mut g = c.root();
        loop {
            match g.children() {
                [usvg::Node::Path(p)] => {
                    // resvg skips invisible, unfilled and zero-area clip paths: they clip everything.
                    let b = p.data().bounds();
                    let fill = p.fill().filter(|_| p.is_visible() && b.width() > 0.0 && b.height() > 0.0);
                    let path = self.add(match fill {
                        Some(_) => bez(p.data()),
                        None => BezPath::new(),
                    });
                    let rule = fill.map_or(Fill::NonZero, |f| fill_rule(f.rule()));
                    return Some((path, rule, ts));
                }
                [usvg::Node::Group(sub)] if sub.clip_path().is_none() => {
                    ts = ts.pre_concat(sub.transform());
                    g = sub;
                }
                _ => return None,
            }
        }
    }

    fn add(&mut self, p: BezPath) -> u32 {
        self.s.paths.push(p);
        (self.s.paths.len() - 1) as u32
    }

    fn path(&mut self, n: &usvg::Node, p: &usvg::Path, abs: tiny_skia::Transform) {
        if !p.is_visible() {
            return;
        }
        let pattern_stroke = matches!(p.stroke().map(usvg::Stroke::paint), Some(usvg::Paint::Pattern(_)));
        if !p.rendering_mode().use_shape_antialiasing() || pattern_stroke {
            return self.fallback(n, abs, BlendMode::default());
        }
        let bbox = rect(p.stroke_bounding_box().transform(abs).unwrap_or(p.abs_stroke_bounding_box()));
        let (items, paths) = (self.s.items.len(), self.s.paths.len());
        let path = self.add(bez(p.data()));
        let ts = aff(abs);
        let fill = |b: &mut Build| {
            let Some(f) = p.fill() else { return true };
            let pb = p.data().bounds();
            if pb.width() == 0.0 || pb.height() == 0.0 {
                return true;
            }
            let rule = fill_rule(f.rule());
            match f.paint() {
                usvg::Paint::Pattern(pat) => b.pattern(pat, path, rule, f.opacity().get(), bbox, abs, pb),
                paint => {
                    b.push(bbox, ts, Op::Fill { path, paint: convert_paint(paint, f.opacity()), rule });
                    true
                }
            }
        };
        let stroke = |b: &mut Build| {
            if let Some(s) = p.stroke() {
                b.push(bbox, ts, Op::Stroke { path, paint: convert_paint(s.paint(), s.opacity()), stroke: Box::new(stroke_of(s)) });
            }
        };
        let ok = if p.paint_order() == usvg::PaintOrder::FillAndStroke {
            let ok = fill(self);
            stroke(self);
            ok
        } else {
            stroke(self);
            fill(self)
        };
        if !ok {
            self.s.items.truncate(items);
            self.s.paths.truncate(paths);
            self.fallback(n, abs, BlendMode::default());
        }
    }

    /// A pattern whose tile contains the whole path is its content clipped to the path.
    /// Returns false when that does not hold or the content needs resvg.
    #[allow(clippy::too_many_arguments)]
    fn pattern(&mut self, pat: &usvg::Pattern, path: u32, rule: Fill, opacity: f32, bbox: Rect, abs: tiny_skia::Transform, pb: tiny_skia::Rect) -> bool {
        let r = pat.rect();
        let Some(inv) = pat.transform().invert() else { return false };
        let Some(pb) = pb.transform(inv) else { return false };
        let eps = 0.01;
        if pb.left() < r.left() - eps || pb.top() < r.top() - eps || pb.right() > r.right() + eps || pb.bottom() > r.bottom() + eps {
            return false;
        }
        let saved = (self.inside.replace(bbox), std::mem::take(&mut self.failed));
        let start = self.open(bbox, aff(abs), Some((path, rule)), BlendMode::default(), opacity, None);
        self.children(pat.root(), abs.pre_concat(pat.transform()).pre_translate(r.x(), r.y()));
        self.close(start, bbox);
        let ok = !self.failed;
        (self.inside, self.failed) = saved;
        ok
    }

    fn image(&mut self, n: &usvg::Node, img: &usvg::Image, abs: tiny_skia::Transform) {
        if !img.is_visible() {
            return;
        }
        let key = match img.kind() {
            usvg::ImageKind::JPEG(d) | usvg::ImageKind::PNG(d) | usvg::ImageKind::GIF(d) | usvg::ImageKind::WEBP(d) => Arc::as_ptr(d) as usize,
            usvg::ImageKind::SVG(_) => return self.fallback(n, abs, BlendMode::default()),
        };
        let pix = match self.images.get(&key) {
            Some(p) => Some(p.clone()),
            None => decode(n, img).map(Arc::new).inspect(|p| {
                self.images.insert(key, p.clone());
            }),
        };
        let Some(pix) = pix else { return self.fallback(n, abs, BlendMode::default()) };
        let quality = match img.rendering_mode() {
            usvg::ImageRendering::OptimizeSpeed | usvg::ImageRendering::CrispEdges | usvg::ImageRendering::Pixelated => ImageQuality::Low,
            usvg::ImageRendering::Smooth => ImageQuality::Medium,
            _ => ImageQuality::High,
        };
        self.push(rect(img.bounding_box().transform(abs).unwrap_or(img.abs_bounding_box())), aff(abs), Op::Image { pix, quality });
    }
}

fn drop_effect(d: Vec2, sigma: f64, color: AlphaColor<Srgb>, scale: f64) -> Effect {
    let prim = FilterPrimitive::DropShadowOnly { dx: d.x as f32, dy: d.y as f32, std_deviation: sigma as f32, color, edge_mode: EdgeMode::None };
    Effect { prim, grow: (d.hypot() + 3.0 * sigma) * scale, layer: false }
}

impl Effect {
    /// The filter under the device transform `t`. Shadow offsets truncate to whole device pixels
    /// like resvg's feOffset (vello rounds them), so the canvas matches exports.
    fn at(&self, t: Affine) -> Filter {
        let prim = match self.prim {
            FilterPrimitive::DropShadowOnly { dx, dy, std_deviation, color, edge_mode } => {
                let [a, b, c, d, _, _] = t.as_coeffs();
                let linear = Affine::new([a, b, c, d, 0.0, 0.0]);
                let dev = linear * Point::new(dx as f64, dy as f64);
                let p = linear.inverse() * Point::new(dev.x.trunc(), dev.y.trunc());
                FilterPrimitive::DropShadowOnly { dx: p.x as f32, dy: p.y as f32, std_deviation, color, edge_mode }
            }
            ref p => p.clone(),
        };
        Filter::from_primitive(prim)
    }
}

/// Uniform scale of a transform (vello scales filter parameters by it).
fn abs_scale(t: tiny_skia::Transform) -> f64 {
    ((t.sx * t.sy - t.kx * t.ky).abs() as f64).sqrt()
}

/// The filters svg.rs writes, by their primitive chains (each primitive reading the previous one):
/// blur (layer blur), blur? → offset → flood → in (drop shadow), flood → out → offset → blur? → in
/// (inner shadow) and flood black → in (group outline). Blurs must be uniform and sRGB.
fn effect_of(f: &usvg::filter::Filter) -> Option<Fx> {
    use usvg::filter::{ColorInterpolation, CompositeOperator as Op, Input, Kind};
    let p = f.primitives();
    let prev = |i: &Input, k: usize| matches!(i, Input::Reference(r) if k > 0 && *r == p[k - 1].result());
    let srgb = p.iter().all(|q| q.color_interpolation() == ColorInterpolation::SRGB);
    let sigma = |b: &usvg::filter::GaussianBlur| (b.std_dev_x() == b.std_dev_y()).then_some(b.std_dev_x().get() as f64);
    let flood = |fl: &usvg::filter::Flood| {
        let c = fl.color();
        AlphaColor::<Srgb>::new([c.red as f32 / 255.0, c.green as f32 / 255.0, c.blue as f32 / 255.0, fl.opacity().get()])
    };
    let kinds: Vec<&Kind> = p.iter().map(|q| q.kind()).collect();
    match kinds.as_slice() {
        [Kind::GaussianBlur(b)] if srgb && *b.input() == Input::SourceGraphic => Some(Fx::Blur(sigma(b)?)),
        [Kind::GaussianBlur(b), Kind::Offset(o), Kind::Flood(fl), Kind::Composite(c)]
            if srgb && *b.input() == Input::SourceAlpha && prev(o.input(), 1) && c.operator() == Op::In && prev(c.input1(), 3) && *c.input2() == Input::Reference(p[1].result().to_string()) =>
        {
            Some(Fx::Drop(Vec2::new(o.dx() as f64, o.dy() as f64), sigma(b)?, flood(fl)))
        }
        [Kind::Offset(o), Kind::Flood(fl), Kind::Composite(c)]
            if srgb && *o.input() == Input::SourceAlpha && c.operator() == Op::In && prev(c.input1(), 2) && *c.input2() == Input::Reference(p[0].result().to_string()) =>
        {
            Some(Fx::Drop(Vec2::new(o.dx() as f64, o.dy() as f64), 0.0, flood(fl)))
        }
        [Kind::Flood(fl), Kind::Composite(out), Kind::Offset(o), rest @ ..] if srgb && out.operator() == Op::Out && prev(out.input1(), 1) && *out.input2() == Input::SourceAlpha && prev(o.input(), 2) => {
            let (sigma, last) = match rest {
                [Kind::GaussianBlur(b), Kind::Composite(c)] if prev(b.input(), 3) => (sigma(b)?, c),
                [Kind::Composite(c)] => (0.0, c),
                _ => return None,
            };
            let ok = last.operator() == Op::In && prev(last.input1(), p.len() - 1) && *last.input2() == Input::SourceAlpha;
            let r = f.rect();
            let region = KRect::new(r.left() as f64, r.top() as f64, r.right() as f64, r.bottom() as f64);
            ok.then(|| Fx::Inner(Vec2::new(o.dx() as f64, o.dy() as f64), sigma, flood(fl), region))
        }
        [Kind::Flood(fl), Kind::Composite(c)] => {
            let black = fl.color() == usvg::Color::black() && fl.opacity().get() == 1.0;
            (black && c.operator() == Op::In && prev(c.input1(), 1) && *c.input2() == Input::SourceAlpha).then_some(Fx::Black)
        }
        _ => None,
    }
}

/// svg.rs knockout masks (outer strokes, shadows): a luminance mask that is an opaque white rect
/// over its region under opaque black shapes (paths, text, outlines flooded black), i.e.
/// "everything but the shapes". Returns the mask rect and the shapes.
fn knockout(m: &usvg::Mask) -> Option<(tiny_skia::Rect, &[usvg::Node])> {
    let (bg, shapes) = m.root().children().split_first()?;
    let r = m.rect().to_rect();
    let usvg::Node::Path(bg) = bg else { return None };
    let b = bg.data().bounds();
    let covers = b.left() <= r.left() + 0.01 && b.top() <= r.top() + 0.01 && b.right() >= r.right() - 0.01 && b.bottom() >= r.bottom() - 0.01;
    let solid = |p: &usvg::Paint, o: usvg::Opacity, c: usvg::Color| matches!(p, usvg::Paint::Color(x) if *x == c) && o.get() == 1.0;
    let white = bg.is_visible() && bg.stroke().is_none() && bg.fill().is_some_and(|f| solid(f.paint(), f.opacity(), usvg::Color::white()));
    fn black(n: &usvg::Node) -> bool {
        let solid = |p: &usvg::Paint, o: usvg::Opacity| matches!(p, usvg::Paint::Color(x) if *x == usvg::Color::black()) && o.get() == 1.0;
        let plain = |g: &usvg::Group| g.opacity().get() == 1.0 && g.clip_path().is_none() && g.mask().is_none() && g.blend_mode() == usvg::BlendMode::Normal;
        match n {
            usvg::Node::Path(p) => p.rendering_mode().use_shape_antialiasing() && p.fill().is_none_or(|f| solid(f.paint(), f.opacity())) && p.stroke().is_none_or(|s| solid(s.paint(), s.opacity())),
            usvg::Node::Group(g) => match g.filters() {
                [] => !g.should_isolate() && g.children().iter().all(black),
                [f] => plain(g) && matches!(effect_of(f), Some(Fx::Black)),
                _ => false,
            },
            usvg::Node::Text(t) => t.flattened().children().iter().all(black),
            usvg::Node::Image(_) => false,
        }
    }
    let plain = m.kind() == usvg::MaskType::Luminance && m.mask().is_none() && m.root().transform().is_identity();
    (plain && covers && white && shapes.iter().all(black)).then_some((r, shapes))
}

/// Decoded, premultiplied pixels of a raster image: resvg draws it at a translate-only
/// transform, which tiny-skia samples nearest-neighbour, i.e. copies.
fn decode(n: &usvg::Node, img: &usvg::Image) -> Option<Pixmap> {
    let (w, h) = (img.size().width().round() as u32, img.size().height().round() as u32);
    if w > u16::MAX as u32 || h > u16::MAX as u32 {
        return None;
    }
    let mut pm = tiny_skia::Pixmap::new(w, h)?;
    let b = n.abs_layer_bounding_box()?;
    resvg::render_node(n, tiny_skia::Transform::from_translate(b.x(), b.y()), &mut pm.as_mut())?;
    Some(Pixmap::from_parts(pm.take(), w as u16, h as u16, PixelMetadata::default()))
}

/// Shadow group emitted by svg.rs: `nrsh:{d|i}:{uid}:{x},{y},{w},{h},{r}:{dx},{dy},{sigma},{#rrggbb},{alpha};…`,
/// drawn analytically only under a uniformly scaled transform rotated by less than ~0.5° (resvg
/// offsets filters along device axes, analytic shadows along local ones).
fn shadow_of(g: &usvg::Group, abs: tiny_skia::Transform) -> Option<Shadow> {
    let mut it = g.id().strip_prefix("nrsh:")?.split(':');
    let inner = it.next()? == "i";
    let _uid = it.next()?;
    let geo: Vec<f64> = it.next()?.split(',').map(|v| v.parse().ok()).collect::<Option<_>>()?;
    let &[x, y, w, h, radius] = geo.as_slice() else { return None };
    let list = it.next()?.split(';').map(|s| {
        let p: Vec<&str> = s.split(',').collect();
        let &[dx, dy, sigma, c, a] = p.as_slice() else { return None };
        let c = u32::from_str_radix(c.strip_prefix('#')?, 16).ok()?;
        let a = (a.parse::<f64>().ok()?.clamp(0.0, 1.0) * 255.0).round() as u8;
        Some((Vec2::new(dx.parse().ok()?, dy.parse().ok()?), sigma.parse::<f64>().ok()?.max(0.0), AlphaColor::from_rgba8((c >> 16) as u8, (c >> 8) as u8, c as u8, a)))
    }).collect::<Option<Vec<_>>>()?;
    let aligned = abs.kx.abs() < 0.01 && abs.ky.abs() < 0.01 && abs.sx > 0.0 && (abs.sx - abs.sy).abs() < 1e-4;
    let plain = g.opacity().get() == 1.0 && g.blend_mode() == usvg::BlendMode::Normal && g.clip_path().is_none();
    let region = match (inner, g.mask(), g.filters().is_empty()) {
        (false, Some(m), true) if m.mask().is_none() => m.rect().to_rect(),
        (true, None, false) => tiny_skia::Rect::from_xywh(x as f32, y as f32, w as f32, h as f32)?,
        _ => return None,
    };
    // An empty outline casts nothing.
    let list = if w > 0.0 && h > 0.0 { list } else { Vec::new() };
    (aligned && plain).then(|| Shadow {
        inner,
        rect: KRect::new(x, y, x + w, y + h),
        radius,
        region: KRect::new(region.left() as f64, region.top() as f64, region.right() as f64, region.bottom() as f64),
        list,
    })
}

/// vello's blurred rect evaluates erf(d / std_dev), i.e. a Gaussian of σ = std_dev / √2.
fn vello_std_dev(sigma: f64) -> f64 {
    sigma * std::f64::consts::SQRT_2
}

fn shadow(ctx: &mut RenderContext, s: &Shadow, t: Affine) {
    if s.list.is_empty() {
        return;
    }
    ctx.set_transform(t);
    // A gradient fill earlier leaves its paint transform set; it would skew the blurred rect.
    ctx.reset_paint_transform();
    let outline = RoundedRect::from_rect(s.rect, s.radius).to_path(0.01);
    if s.inner {
        for &(off, sigma, color) in &s.list {
            ctx.set_fill_rule(Fill::NonZero);
            ctx.push_clip_layer(&outline);
            ctx.set_paint(color);
            let moved = s.rect + off;
            // Paint outside the shifted outline: blurred inverse, solid beyond the blur's reach.
            let reach = if sigma > 0.0 {
                let sd = vello_std_dev(sigma);
                ctx.fill_blurred_rounded_rect(&moved, s.radius as f32, sd as f32, true);
                // vello paints the blur out to 2.5 std_dev.
                moved.inflate(2.5 * sd, 2.5 * sd).to_path(0.01)
            } else {
                RoundedRect::from_rect(moved, s.radius).to_path(0.01)
            };
            let mut rest = s.rect.to_path(0.01);
            rest.extend(reach);
            ctx.set_fill_rule(Fill::EvenOdd);
            ctx.fill_path(&rest);
            ctx.pop_layer();
        }
    } else {
        let mut ko = s.region.to_path(0.01);
        ko.extend(outline);
        ctx.set_fill_rule(Fill::EvenOdd);
        ctx.push_clip_layer(&ko);
        ctx.set_fill_rule(Fill::NonZero);
        for &(off, sigma, color) in &s.list {
            ctx.set_paint(color);
            if sigma > 0.0 {
                ctx.fill_blurred_rounded_rect(&(s.rect + off), s.radius as f32, vello_std_dev(sigma) as f32, false);
            } else {
                ctx.fill_path(&RoundedRect::from_rect(s.rect + off, s.radius).to_path(0.01));
            }
        }
        ctx.pop_layer();
    }
}

/// The area being painted: world-device pixels from (x0, y0) at `zoom`, `view` in doc space.
struct Frame {
    vt: Affine,
    view: Rect,
    zoom: f64,
    x0: i32,
    y0: i32,
}

/// Everything `n` paints: usvg's layer box of a path leaves out its stroke, which a resvg
/// fallback (a pattern stroke, e.g. an angular gradient) would then lose outside the fill box.
fn painted_box(n: &usvg::Node) -> Option<usvg::NonZeroRect> {
    match n {
        usvg::Node::Path(p) => p.abs_stroke_bounding_box().to_non_zero_rect(),
        _ => n.abs_layer_bounding_box(),
    }
}

/// Rasterize a subtree with resvg over its device box ∩ `view` (the band, grown inside filter
/// layers) and draw it at that integer offset.
fn fallback(ctx: &mut RenderContext, tree: &usvg::Tree, at: &[u32], parent: tiny_skia::Transform, blend: BlendMode, f: &Frame, view: Rect) {
    let Some(node) = node_at(tree.root(), at) else { return };
    // resvg's render_node origin is the layer box (none for a horizontal or vertical line, which
    // it draws unshifted); the pixmap covers everything painted.
    let origin = node.abs_layer_bounding_box().map_or((0.0, 0.0), |o| (o.x(), o.y()));
    let Some(b) = painted_box(node) else { return };
    let z = f.zoom;
    let x0 = (b.left() as f64).max(view.0) * z;
    let y0 = (b.top() as f64).max(view.1) * z;
    let x1 = (b.right() as f64).min(view.0 + view.2) * z;
    let y1 = (b.bottom() as f64).min(view.1 + view.3) * z;
    let (x0, y0) = (x0.floor() as i64 - f.x0 as i64, y0.floor() as i64 - f.y0 as i64);
    let (x1, y1) = (x1.ceil() as i64 - f.x0 as i64, y1.ceil() as i64 - f.y0 as i64);
    if x1 <= x0 || y1 <= y0 {
        return;
    }
    let (w, h) = ((x1 - x0) as u32, (y1 - y0) as u32);
    if w > u16::MAX as u32 || h > u16::MAX as u32 {
        return;
    }
    let Some(mut pm) = tiny_skia::Pixmap::new(w, h) else { return };
    let tf = tiny_skia::Transform::from_row(z as f32, 0.0, 0.0, z as f32, -(f.x0 as i64 + x0) as f32, -(f.y0 as i64 + y0) as f32).pre_concat(parent);
    resvg::render_node(node, tf.pre_translate(origin.0, origin.1), &mut pm.as_mut());
    let pix = Pixmap::from_parts(pm.take(), w as u16, h as u16, PixelMetadata::default());
    ctx.set_transform(Affine::translate((x0 as f64, y0 as f64)));
    ctx.set_blend_mode(blend);
    draw_image(ctx, Arc::new(pix), ImageQuality::Low);
    ctx.set_blend_mode(BlendMode::default());
}

fn draw_image(ctx: &mut RenderContext, pix: Arc<Pixmap>, quality: ImageQuality) {
    let r = KRect::new(0.0, 0.0, pix.width() as f64, pix.height() as f64);
    ctx.reset_paint_transform();
    ctx.set_paint(Image { image: ImageSource::Pixmap(pix), sampler: ImageSampler::new().with_quality(quality) });
    ctx.fill_rect(&r);
}

fn node_at<'t>(root: &'t usvg::Group, at: &[u32]) -> Option<&'t usvg::Node> {
    let (first, rest) = at.split_first()?;
    let mut n = root.children().get(*first as usize)?;
    for &i in rest {
        let g = match n {
            usvg::Node::Group(g) => g.as_ref(),
            usvg::Node::Text(t) => t.flattened(),
            _ => return None,
        };
        n = g.children().get(i as usize)?;
    }
    Some(n)
}

/// Single-threaded vello contexts reused across frames by one band thread (filter layers need
/// single-threaded rendering): the band's own and those of nested backdrop renders.
pub struct Band {
    ctx: RenderContext,
    res: Resources,
    aux: Vec<Option<Aux>>,
}

impl Default for Band {
    fn default() -> Self {
        Band { ctx: RenderContext::new_with(1, 1, settings()), res: Resources::new(), aux: Vec::new() }
    }
}

fn settings() -> RenderSettings {
    RenderSettings { level: Level::try_detect().unwrap_or(Level::baseline()), num_threads: 0 }
}

/// Paint `scenes` (bottom first) over `bg` into `out`, premultiplied RGBA of w×h world-device
/// pixels starting at (x0, y0) at `zoom`.
#[allow(clippy::too_many_arguments)]
pub fn paint(band: &mut Band, scenes: &[(&Scene, &usvg::Tree)], zoom: f64, bg: [u8; 3], x0: i32, y0: i32, w: u32, h: u32, out: &mut [u8]) {
    let (Ok(w16), Ok(h16)) = (u16::try_from(w), u16::try_from(h)) else { return };
    let Some(target) = PixmapMut::new(w16, h16, out) else { return };
    band.ctx.reset_and_resize(w16, h16);
    let f = Frame {
        vt: Affine::new([zoom, 0.0, 0.0, zoom, -x0 as f64, -y0 as f64]),
        view: (x0 as f64 / zoom, y0 as f64 / zoom, w as f64 / zoom, h as f64 / zoom),
        zoom,
        x0,
        y0,
    };
    let mut env = Env { scenes, bg, aux: &mut band.aux, depth: 0 };
    for (si, (scene, _)) in scenes.iter().enumerate() {
        draw(&mut band.ctx, &mut env, &f, si, 0..scene.items.len(), None);
    }
    band.ctx.flush();
    let settings = RasterizerSettings { target_init: TargetInit::Clear(AlphaColor::from_rgba8(bg[0], bg[1], bg[2], 255)), ..Default::default() };
    band.ctx.render_with(target, &mut band.res, settings);
}

fn inflate(r: Rect, m: f64) -> Rect {
    (r.0 - m, r.1 - m, r.2 + 2.0 * m, r.3 + 2.0 * m)
}

fn overlaps(a: Rect, b: Rect) -> bool {
    a.0 < b.0 + b.2 && b.0 < a.0 + a.2 && a.1 < b.1 + b.3 && b.1 < a.1 + a.3
}

fn rect(r: tiny_skia::Rect) -> Rect {
    (r.x() as f64, r.y() as f64, r.width() as f64, r.height() as f64)
}

fn aff(t: tiny_skia::Transform) -> Affine {
    Affine::new([t.sx, t.ky, t.kx, t.sy, t.tx, t.ty].map(f64::from))
}

fn bez(p: &tiny_skia::Path) -> BezPath {
    use tiny_skia::PathSegment as S;
    let pt = |p: tiny_skia::Point| Point::new(p.x as f64, p.y as f64);
    let mut out = BezPath::new();
    for s in p.segments() {
        match s {
            S::MoveTo(a) => out.move_to(pt(a)),
            S::LineTo(a) => out.line_to(pt(a)),
            S::QuadTo(a, b) => out.quad_to(pt(a), pt(b)),
            S::CubicTo(a, b, c) => out.curve_to(pt(a), pt(b), pt(c)),
            S::Close => out.close_path(),
        }
    }
    out
}

fn fill_rule(r: usvg::FillRule) -> Fill {
    match r {
        usvg::FillRule::NonZero => Fill::NonZero,
        usvg::FillRule::EvenOdd => Fill::EvenOdd,
    }
}

fn set_paint(ctx: &mut RenderContext, p: &Paint) {
    ctx.set_paint(p.kind.clone());
    ctx.set_paint_transform(p.ts);
}

fn color(c: usvg::Color, a: usvg::Opacity) -> AlphaColor<Srgb> {
    AlphaColor::from_rgba8(c.red, c.green, c.blue, a.to_u8())
}

/// Solid colors and gradients as resvg/tiny-skia build them (stop alpha × paint opacity,
/// unpremultiplied interpolation, one stop or a zero-length axis is a solid color).
fn convert_paint(p: &usvg::Paint, opacity: usvg::Opacity) -> Paint {
    let solid = |c: AlphaColor<Srgb>| Paint { kind: c.into(), ts: Affine::IDENTITY };
    let (base, kind): (&usvg::BaseGradient, GradientKind) = match p {
        usvg::Paint::Color(c) => return solid(color(*c, opacity)),
        usvg::Paint::LinearGradient(g) => {
            if (g.x2() - g.x1()).hypot(g.y2() - g.y1()) < 1.0 / (1 << 15) as f32 {
                return solid(g.stops().last().map_or(AlphaColor::TRANSPARENT, |s| color(s.color(), s.opacity() * opacity)));
            }
            let pos = LinearGradientPosition { start: Point::new(g.x1() as f64, g.y1() as f64), end: Point::new(g.x2() as f64, g.y2() as f64) };
            let base: &usvg::BaseGradient = g;
            (base, pos.into())
        }
        usvg::Paint::RadialGradient(g) => {
            let pos = RadialGradientPosition {
                start_center: Point::new(g.fx() as f64, g.fy() as f64),
                start_radius: g.fr().get(),
                end_center: Point::new(g.cx() as f64, g.cy() as f64),
                end_radius: g.r().get(),
            };
            let base: &usvg::BaseGradient = g;
            (base, pos.into())
        }
        usvg::Paint::Pattern(_) => return solid(AlphaColor::TRANSPARENT),
    };
    let stops: Vec<ColorStop> = base.stops().iter().map(|s| ColorStop::from((s.offset().get(), color(s.color(), s.opacity() * opacity)))).collect();
    if stops.len() < 2 {
        return solid(base.stops().first().map_or(AlphaColor::TRANSPARENT, |s| color(s.color(), s.opacity() * opacity)));
    }
    let extend = match base.spread_method() {
        usvg::SpreadMethod::Pad => Extend::Pad,
        usvg::SpreadMethod::Reflect => Extend::Reflect,
        usvg::SpreadMethod::Repeat => Extend::Repeat,
    };
    let g = Gradient { kind, extend, interpolation_alpha_space: InterpolationAlphaSpace::Unpremultiplied, stops: ColorStops::from(stops.as_slice()), ..Default::default() };
    Paint { kind: g.into(), ts: aff(base.transform()) }
}

fn stroke_of(s: &usvg::Stroke) -> Stroke {
    let cap = match s.linecap() {
        usvg::LineCap::Butt => Cap::Butt,
        usvg::LineCap::Round => Cap::Round,
        usvg::LineCap::Square => Cap::Square,
    };
    let join = match s.linejoin() {
        usvg::LineJoin::Bevel => Join::Bevel,
        usvg::LineJoin::Round => Join::Round,
        usvg::LineJoin::Miter | usvg::LineJoin::MiterClip => Join::Miter,
    };
    let k = Stroke::new(s.width().get() as f64).with_caps(cap).with_join(join).with_miter_limit(s.miterlimit().get() as f64);
    match s.dasharray() {
        Some(d) => k.with_dashes(s.dashoffset() as f64, d.iter().map(|v| *v as f64)),
        None => k,
    }
}

fn blend(m: usvg::BlendMode) -> BlendMode {
    use usvg::BlendMode as B;
    BlendMode::from(match m {
        B::Normal => Mix::Normal,
        B::Multiply => Mix::Multiply,
        B::Screen => Mix::Screen,
        B::Overlay => Mix::Overlay,
        B::Darken => Mix::Darken,
        B::Lighten => Mix::Lighten,
        B::ColorDodge => Mix::ColorDodge,
        B::ColorBurn => Mix::ColorBurn,
        B::HardLight => Mix::HardLight,
        B::SoftLight => Mix::SoftLight,
        B::Difference => Mix::Difference,
        B::Exclusion => Mix::Exclusion,
        B::Hue => Mix::Hue,
        B::Saturation => Mix::Saturation,
        B::Color => Mix::Color,
        B::Luminosity => Mix::Luminosity,
    })
}

/// Mesh gradient fill `o` over a `w`×`h` pixmap of the node box, or None when the fill is
/// invalid. `columns`×`rows` points (row-major, one color each) are `[x, y]` or `{position,
/// leftHandle, rightHandle, topHandle, bottomHandle}` in 0..1 of the box, handles relative to the
/// position. Each cell is a bicubic patch tessellated 32×32 with smoothstep vertex
/// colors (truncated to integers), drawn as Gouraud triangles without antialiasing, src-over in
/// row-major order; what no patch covers stays transparent.
pub fn mesh_gradient(o: &serde_json::Map<String, serde_json::Value>, w: u32, h: u32) -> Option<tiny_skia::Pixmap> {
    use crate::resolve::num;
    const T: usize = 32;
    let dim = |k: &str| o.get(k).and_then(num).filter(|n| *n >= 1.0 && n.fract() == 0.0).map(|n| n as usize);
    let (cols, rows) = (dim("columns")?, dim("rows")?);
    let (points, colors) = (o.get("points")?.as_array()?, o.get("colors")?.as_array()?);
    if cols.checked_mul(rows) != Some(points.len()) || colors.len() != points.len() {
        return None;
    }
    let colors: Vec<[f64; 4]> = colors.iter().map(|c| c.as_str().and_then(crate::svg::rgba8).unwrap_or([0; 4]).map(f64::from)).collect();
    // [position, left, right, top, bottom]; by default on the grid with handles a quarter cell long.
    let (cw, ch) = (1.0 / (cols - 1).max(1) as f64, 1.0 / (rows - 1).max(1) as f64);
    let xy = |v: &serde_json::Value| Some([num(&v[0])?, num(&v[1])?]);
    let pts: Vec<[[f64; 2]; 5]> = points
        .iter()
        .enumerate()
        .map(|(i, p)| {
            let at = |k: &str, d: [f64; 2]| p.get(k).and_then(xy).unwrap_or(d);
            let home = [(i % cols) as f64 * cw, (i / cols) as f64 * ch];
            [xy(p.get("position").unwrap_or(p)).unwrap_or(home), at("leftHandle", [-cw / 4.0, 0.0]), at("rightHandle", [cw / 4.0, 0.0]), at("topHandle", [0.0, -ch / 4.0]), at("bottomHandle", [0.0, ch / 4.0])]
        })
        .collect();
    let bern = |t: f64| [(1.0 - t).powi(3), 3.0 * t * (1.0 - t).powi(2), 3.0 * t * t * (1.0 - t), t.powi(3)];
    let smooth = |t: f64| t * t * (3.0 - 2.0 * t);
    let add = |a: [f64; 2], b: [f64; 2]| [a[0] + b[0], a[1] + b[1]];
    // 1/256 px fixed point, so triangles sharing an edge agree exactly on which pixels it owns.
    let fixed = |v: f64, n: u32| ((v * n as f64).clamp(-1e6, 1e6) * 256.0).round() as i64;
    let mut pm = tiny_skia::Pixmap::new(w, h)?;
    if cols < 2 || rows < 2 {
        return Some(pm);
    }
    // Patch borders share vertices: one ((cols−1)·T+1) × ((rows−1)·T+1) grid, generated and drawn
    // a row at a time.
    let gw = (cols - 1) * T + 1;
    let mut prev: Vec<([i64; 2], [f64; 4])> = Vec::new();
    for y in 0..rows - 1 {
        let patches: Vec<([usize; 4], [[[f64; 2]; 4]; 4])> = (0..cols - 1)
            .map(|x| {
                // Corners top-left, top-right, bottom-left, bottom-right.
                let k = [y * cols + x, y * cols + x + 1, (y + 1) * cols + x, (y + 1) * cols + x + 1];
                // 4×4 control net (rows along v): each corner, its handles into the patch along u
                // and along v, and their sum.
                let mut g = [[[0.0; 2]; 4]; 4];
                for (&at, (r, c, hu, hv)) in k.iter().zip([(0, 0, 2, 4), (0, 3, 1, 4), (3, 0, 2, 3), (3, 3, 1, 3)]) {
                    let p = &pts[at];
                    let (r1, c1) = (if r == 0 { 1 } else { 2 }, if c == 0 { 1 } else { 2 });
                    (g[r][c], g[r][c1], g[r1][c], g[r1][c1]) = (p[0], add(p[0], p[hu]), add(p[0], p[hv]), add(add(p[0], p[hu]), p[hv]));
                }
                (k, g)
            })
            .collect();
        // Below the first patch row, row 0 is the row above's last.
        for j in usize::from(y > 0)..=T {
            let v = j as f64 / T as f64;
            let (bv, sv) = (bern(v), smooth(v));
            let row: Vec<([i64; 2], [f64; 4])> = (0..gw)
                .map(|gx| {
                    let x = (gx / T).min(cols - 2);
                    let u = (gx - x * T) as f64 / T as f64;
                    let ((k, g), bu, su) = (&patches[x], bern(u), smooth(u));
                    let mut pt = [0.0; 2];
                    for (r, line) in g.iter().enumerate() {
                        for (c, q) in line.iter().enumerate() {
                            pt = add(pt, q.map(|z| z * bv[r] * bu[c]));
                        }
                    }
                    let wt = [(1.0 - sv) * (1.0 - su), (1.0 - sv) * su, sv * (1.0 - su), sv * su];
                    let color = std::array::from_fn(|n| (0..4).map(|q| colors[k[q]][n] * wt[q]).sum::<f64>().trunc());
                    ([fixed(pt[0], w), fixed(pt[1], h)], color)
                })
                .collect();
            for i in 0..prev.len().saturating_sub(1) {
                triangle(&mut pm, [prev[i], row[i], prev[i + 1]]);
                triangle(&mut pm, [row[i], prev[i + 1], row[i + 1]]);
            }
            prev = row;
        }
    }
    Some(pm)
}

/// One Gouraud triangle (1/256 px fixed-point corners, straight 0–255 colors) without
/// antialiasing: each pixel centre inside gets the barycentric mix, premultiplied, src-over.
fn triangle(pm: &mut tiny_skia::Pixmap, mut v: [([i64; 2], [f64; 4]); 3]) {
    let cross = |a: [i64; 2], b: [i64; 2], p: [i64; 2]| (b[0] - a[0]) * (p[1] - a[1]) - (b[1] - a[1]) * (p[0] - a[0]);
    let mut area = cross(v[0].0, v[1].0, v[2].0);
    if area < 0 {
        v.swap(1, 2);
        area = -area;
    }
    if area == 0 {
        return;
    }
    let (w, h) = (pm.width() as i64, pm.height() as i64);
    let span = |k: usize, n: i64| {
        let (lo, hi) = (v.iter().map(|q| q.0[k]).min().unwrap_or(0), v.iter().map(|q| q.0[k]).max().unwrap_or(0));
        (lo + 127).div_euclid(256).max(0)..=(hi - 128).div_euclid(256).min(n - 1)
    };
    // Centres exactly on an edge belong to the triangle it runs down (or left) in: of the two
    // sharing it, which traverse it in opposite directions, exactly one.
    let owns = |a: [i64; 2], b: [i64; 2]| b[1] > a[1] || (b[1] == a[1] && b[0] < a[0]);
    let edges = [(1, 2), (2, 0), (0, 1)];
    let px = pm.pixels_mut();
    for y in span(1, h) {
        for x in span(0, w) {
            let p = [x * 256 + 128, y * 256 + 128];
            let e = edges.map(|(a, b)| cross(v[a].0, v[b].0, p));
            if !e.iter().zip(edges).all(|(&e, (a, b))| e > 0 || (e == 0 && owns(v[a].0, v[b].0))) {
                continue;
            }
            let c: [u8; 4] = std::array::from_fn(|n| ((0..3).map(|i| e[i] as f64 * v[i].1[n]).sum::<f64>() / area as f64).round().clamp(0.0, 255.0) as u8);
            let s = tiny_skia::ColorU8::from_rgba(c[0], c[1], c[2], c[3]).premultiply();
            let d = &mut px[(y * w + x) as usize];
            let ia = 255 - s.alpha() as u32;
            let over = |s: u8, d: u8| s + ((d as u32 * ia + 127) / 255) as u8;
            *d = tiny_skia::PremultipliedColorU8::from_rgba(over(s.red(), d.red()), over(s.green(), d.green()), over(s.blue(), d.blue()), over(s.alpha(), d.alpha())).unwrap_or(s);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::doc::Doc;
    use crate::render::tests::{offline, sync};
    use crate::render::{Ctx, Raster, Renderer};
    use base64::Engine as _;

    const BG: [u8; 3] = [250, 250, 250];

    /// Frame of `doc` rendered by vello and by resvg (the reference).
    fn both(doc: &Doc, w: u32, h: u32, cam: (f64, f64), zoom: f64) -> [Vec<u8>; 2] {
        [Raster::Vello, Raster::Resvg].map(|raster| {
            let mut r = Renderer::default();
            r.raster = raster;
            sync(&mut r, doc);
            r.render(w, h, cam.0, cam.1, zoom, BG, None, true).to_vec()
        })
    }

    /// Mean |difference| per RGB channel (0–255) inside the device rect (x, y, w, h).
    fn mean_diff(a: &[u8], b: &[u8], stride: u32, (x, y, w, h): (u32, u32, u32, u32)) -> f64 {
        let mut sum = 0u64;
        for row in y..y + h {
            for col in x..x + w {
                let i = (row * stride + col) as usize * 4;
                sum += (0..3).map(|k| a[i + k].abs_diff(b[i + k]) as u64).sum::<u64>();
            }
        }
        sum as f64 / (w * h * 3) as f64
    }

    #[test]
    fn templates_match_resvg() {
        let templates = [("blank", include_str!("../templates/blank.json")), ("mobile", include_str!("../templates/mobile.json")), ("slides", include_str!("../templates/slides.json"))];
        for (name, json) in templates {
            let doc = Doc::from_json(json).unwrap();
            let mut r = Renderer::default();
            sync(&mut r, &doc);
            let (bx, by, bw, bh) = r.bounds().unwrap();
            let (w, h) = (1200, 800);
            for zoom in [(w as f64 / bw).min(h as f64 / bh), 1.0] {
                let [v, s] = both(&doc, w, h, (bx, by), zoom);
                let d = mean_diff(&v, &s, w, (0, 0, w, h));
                assert!(d < 0.5, "{name} at {zoom:.2}: mean diff {d:.3}");
            }
        }
    }

    fn png_data_url() -> String {
        let mut pm = tiny_skia::Pixmap::new(48, 32).unwrap();
        for (i, px) in pm.pixels_mut().iter_mut().enumerate() {
            let (x, y) = ((i % 48) as u8, (i / 48) as u8);
            let a = if (x / 8 + y / 8) % 2 == 0 { 255 } else { 160 };
            *px = tiny_skia::ColorU8::from_rgba(x * 5, y * 7, 200 - x * 3, a).premultiply();
        }
        format!("data:image/png;base64,{}", base64::engine::general_purpose::STANDARD.encode(pm.encode_png().unwrap()))
    }

    /// Shadowed nodes and the device box (zoom 1) where each shadow paints.
    const SHADOWS: [(&str, (u32, u32, u32, u32)); 5] = [
        ("sh1", (23, 591, 254, 184)),
        ("sh2", (283, 583, 254, 200)),
        ("sh3", (580, 620, 180, 110)),
        ("sh4", (799, 595, 262, 192)),
        ("sh5", (1050, 600, 180, 180)),
    ];

    fn scene() -> Doc {
        let img = png_data_url();
        let shadow = |x: i32, y: i32, blur: i32, c: &str, inner: bool| {
            format!(r#"{{"type":"shadow","shadowType":"{}","offset":{{"x":{x},"y":{y}}},"blur":{blur},"color":"{c}"}}"#, if inner { "inner" } else { "outer" })
        };
        let json = format!(
            r##"{{"version":"2.19","children":[{{"type":"frame","id":"scene","x":0,"y":0,"width":1300,"height":900,"fill":"#ffffff","layout":"none","children":[
            {{"type":"rectangle","id":"lin","x":40,"y":40,"width":160,"height":100,"fill":{{"type":"gradient","gradientType":"linear","rotation":45,"colors":[{{"color":"#ff3366","position":0}},{{"color":"#3366ff80","position":1}}]}}}},
            {{"type":"rectangle","id":"rad","x":240,"y":40,"width":160,"height":100,"cornerRadius":20,"fill":{{"type":"gradient","gradientType":"radial","center":{{"x":0.3,"y":0.4}},"colors":[{{"color":"#ffdd00","position":0}},{{"color":"#00aa5500","position":1}}]}}}},
            {{"type":"ellipse","id":"ang","x":440,"y":40,"width":100,"height":100,"fill":{{"type":"gradient","gradientType":"angular","colors":[{{"color":"#ff0000","position":0}},{{"color":"#0000ff","position":1}}]}}}},
            {{"type":"rectangle","id":"img1","x":580,"y":40,"width":160,"height":100,"cornerRadius":12,"fill":{{"type":"image","url":"{img}","mode":"fill"}}}},
            {{"type":"rectangle","id":"img2","x":780,"y":40,"width":160,"height":100,"fill":{{"type":"image","url":"{img}","mode":"fit"}}}},
            {{"type":"rectangle","id":"img3","x":980,"y":40,"width":160,"height":100,"opacity":0.7,"fill":{{"type":"image","url":"{img}","mode":"stretch"}}}},
            {{"type":"frame","id":"clip","x":40,"y":200,"width":200,"height":140,"clip":true,"cornerRadius":24,"fill":"#e8ecff","layout":"none","children":[
              {{"type":"ellipse","id":"clip1","x":-40,"y":-40,"width":160,"height":160,"fill":"#ff8800"}},
              {{"type":"text","id":"clip2","x":20,"y":90,"content":"Clipped text","fontFamily":"Arial","fontSize":22,"fill":"#202040"}}]}},
            {{"type":"frame","id":"op","x":280,"y":200,"width":200,"height":140,"opacity":0.5,"fill":"#00aa44","layout":"none","children":[
              {{"type":"rectangle","id":"op1","x":40,"y":30,"width":120,"height":80,"fill":"#ffffff"}}]}},
            {{"type":"rectangle","id":"st1","x":520,"y":200,"width":120,"height":100,"cornerRadius":16,"stroke":"#333333","strokeWidth":8,"strokeAlignment":"inner"}},
            {{"type":"rectangle","id":"st2","x":680,"y":200,"width":120,"height":100,"cornerRadius":16,"stroke":"#cc3333","strokeWidth":8,"strokeAlignment":"center"}},
            {{"type":"rectangle","id":"st3","x":840,"y":200,"width":120,"height":100,"cornerRadius":16,"stroke":{{"type":"gradient","gradientType":"linear","colors":[{{"color":"#3333cc","position":0}},{{"color":"#33cc33","position":1}}]}},"strokeWidth":8,"strokeAlignment":"outer"}},
            {{"type":"path","id":"path","x":1000,"y":200,"width":150,"height":120,"geometry":"M0 120 L40 10 L80 90 L150 0","stroke":"#663399","strokeWidth":6,"strokeLinecap":"round","strokeLinejoin":"round"}},
            {{"type":"text","id":"txt","x":40,"y":400,"content":"The quick brown fox - Ag 123","fontFamily":"Arial","fontSize":28,"fill":"#222222"}},
            {{"type":"text","id":"txt2","x":40,"y":450,"width":420,"textGrowth":"fixed-width","content":"Small wrapped body text at thirteen pixels, set in Segoe UI, wrapping over lines.","fontFamily":"Segoe UI","fontSize":13,"fill":"#555555"}},
            {{"type":"script","id":"dash","x":520,"y":400,"width":240,"height":80}},
            {{"type":"rectangle","id":"bl1","x":800,"y":400,"width":200,"height":100,"fill":"#ff00ff"}},
            {{"type":"rectangle","id":"bl2","x":880,"y":430,"width":200,"height":100,"fill":{{"type":"color","color":"#00ffff","blendMode":"multiply"}}}},
            {{"type":"rectangle","id":"blur","x":1100,"y":400,"width":120,"height":100,"fill":"#2266ff","effect":{{"type":"blur","radius":8}}}},
            {{"type":"rectangle","id":"sh1","x":60,"y":620,"width":180,"height":110,"cornerRadius":12,"fill":"#ffffff","effect":[{sh1}]}},
            {{"type":"rectangle","id":"sh2","x":320,"y":620,"width":180,"height":110,"fill":"#ffffff","effect":[{sh2a},{sh2b}]}},
            {{"type":"rectangle","id":"sh3","x":580,"y":620,"width":180,"height":110,"cornerRadius":20,"fill":"#f4f4f4","effect":[{sh3}]}},
            {{"type":"frame","id":"sh4","x":840,"y":620,"width":180,"height":110,"cornerRadius":16,"fill":"#ffffff","stroke":"#dddddd","strokeWidth":4,"strokeAlignment":"outer","layout":"none","effect":[{sh4}]}},
            {{"type":"ellipse","id":"sh5","x":1080,"y":620,"width":120,"height":120,"fill":"#ffffff","effect":[{sh5}]}}]}}]}}"##,
            sh1 = shadow(0, 8, 24, "#00000040", false),
            sh2a = shadow(0, 20, 20, "#0000001a", false),
            sh2b = shadow(-6, 4, 6, "#ff000030", false),
            sh3 = shadow(4, 6, 12, "#00000060", true),
            sh4 = shadow(6, 10, 16, "#20408050", false),
            sh5 = shadow(0, 8, 16, "#00000040", false),
        );
        Doc::from_json(&json).unwrap()
    }

    /// The synthetic scene matches resvg on average, and shadows drawn analytically stay within
    /// 1/255 (spec: 2/255) of resvg's filters over the area they paint, at 100% and 200%.
    #[test]
    fn scene_matches_resvg() {
        let doc = scene();
        let (w, h) = (1300, 900);
        let [v, s] = both(&doc, w, h, (0.0, 0.0), 1.0);
        let all = mean_diff(&v, &s, w, (0, 0, w, h));
        assert!(all < 0.5, "whole scene: mean diff {all:.3}");
        for (id, area) in SHADOWS {
            let d = mean_diff(&v, &s, w, area);
            assert!(d <= 1.0, "{id}: mean diff {d:.3} in its shadow area");
        }
        let (zw, zh) = (2400, 440);
        let [zv, zs] = both(&doc, zw, zh, (20.0, 580.0), 2.0);
        for (id, (x, y, aw, ah)) in SHADOWS {
            let (x, y) = ((x - 20) * 2, (y - 580) * 2);
            let d = mean_diff(&zv, &zs, zw, (x, y, (aw * 2).min(zw - x), (ah * 2).min(zh - y)));
            assert!(d <= 1.0, "{id} at 200%: mean diff {d:.3} in its shadow area");
        }
        if let Ok(dir) = std::env::var("NALARUPA_RASTER_PNG") {
            for (name, px, pw, ph) in [("vello", &v, w, h), ("resvg", &s, w, h), ("vello-200", &zv, zw, zh), ("resvg-200", &zs, zw, zh)] {
                let pm = tiny_skia::Pixmap::from_vec(px.to_vec(), tiny_skia::IntSize::from_wh(pw, ph).unwrap()).unwrap();
                pm.save_png(format!("{dir}/scene-{name}.png")).unwrap();
            }
        }
    }

    /// A gradient fill or stroke must not leak its paint transform into later shadows.
    #[test]
    fn shadows_after_gradients_match_resvg() {
        let lin = r##"{"type":"gradient","gradientType":"linear","rotation":0,"colors":[{"color":"#000000","position":0},{"color":"#ffffff","position":1}]}"##;
        let outer = r##"{"type":"shadow","shadowType":"outer","offset":{"x":0,"y":8},"blur":4,"color":"#ff0000"}"##;
        let inner = r##"{"type":"shadow","shadowType":"inner","offset":{"x":6,"y":4},"blur":4,"color":"#00000080"}"##;
        let json = format!(
            r##"{{"version":"2.19","children":[
            {{"type":"rectangle","id":"a","x":10,"y":20,"width":60,"height":40,"fill":{lin}}},
            {{"type":"rectangle","id":"s","x":10,"y":20,"width":60,"height":40,"stroke":{lin},"strokeWidth":2,"strokeAlignment":"inner"}},
            {{"type":"rectangle","id":"b","x":100,"y":20,"width":60,"height":40,"fill":"#00aa00","effect":{outer}}},
            {{"type":"rectangle","id":"c","x":20,"y":100,"width":60,"height":60,"fill":{lin},"effect":{inner}}}]}}"##
        );
        let doc = Doc::from_json(&json).unwrap();
        let (w, h) = (200, 180);
        let [v, s] = both(&doc, w, h, (0.0, 0.0), 1.0);
        for (id, area) in [("drop shadow", (100, 20, 60, 60)), ("inner shadow", (20, 100, 60, 60))] {
            let d = mean_diff(&v, &s, w, area);
            assert!(d <= 1.0, "{id} after a gradient: mean diff {d:.3}");
        }
    }

    /// The display list `doc`'s first top-level node converts to.
    fn convert(doc: &Doc) -> Scene {
        let (mut fonts, mut assets) = offline();
        let mut cx = Ctx { doc, libs: &[], fonts: &mut fonts, assets: &mut assets };
        let root = cx.laid_out(0).unwrap();
        let opts = cx.tree_options();
        let mut svg = crate::svg::Svg::new(cx.fonts, cx.assets, None);
        svg.node(&root);
        let tree = usvg::Tree::from_str(&svg.finish(None), &opts).unwrap();
        Scene::new(&tree)
    }

    fn count(scene: &Scene, f: fn(&Op) -> bool) -> usize {
        scene.items.iter().filter(|i| f(&i.op)).count()
    }

    /// Tagged rect shadows become analytic items; the ellipse's shadow and the layer blur are
    /// filter layers; images and the angular gradient (an image pattern) go through vello.
    #[test]
    fn scene_conversion() {
        let scene = convert(&scene());
        assert_eq!(count(&scene, |o| matches!(o, Op::Shadow(s) if !s.inner)), 3, "drop shadows of sh1, sh2, sh4");
        assert_eq!(count(&scene, |o| matches!(o, Op::Shadow(s) if s.inner)), 1, "inner shadow of sh3");
        assert_eq!(count(&scene, |o| matches!(o, Op::Image { .. })), 4, "three image fills and the angular gradient");
        assert_eq!(count(&scene, |o| matches!(o, Op::Push { filter: Some(_), .. })), 2, "the ellipse's shadow and the layer blur");
        assert_eq!(count(&scene, |o| matches!(o, Op::Fallback { .. })), 0);
    }

    /// Effects the analytic path does not cover, and the device box (zoom 1) each paints in.
    const EFFECTS: [(&str, (u32, u32, u32, u32)); 10] = [
        ("ellipse drop", (10, 10, 200, 170)),
        ("text drop", (230, 40, 240, 80)),
        ("polygon drop+inner", (455, 10, 175, 175)),
        ("path drop+inner", (665, 15, 190, 160)),
        ("group drop", (880, 15, 180, 150)),
        ("ellipse inner", (40, 300, 140, 110)),
        ("multiply drop", (180, 250, 260, 220)),
        ("uneven radii drop", (430, 260, 240, 200)),
        ("blurred frame", (660, 245, 240, 210)),
        ("layer blur", (925, 275, 190, 150)),
    ];

    fn effects() -> Doc {
        let shadow = |x: i32, y: i32, blur: i32, c: &str, inner: bool| {
            format!(r#"{{"type":"shadow","shadowType":"{}","offset":{{"x":{x},"y":{y}}},"blur":{blur},"color":"{c}"}}"#, if inner { "inner" } else { "outer" })
        };
        let json = format!(
            r##"{{"version":"2.19","children":[{{"type":"frame","id":"fx","x":0,"y":0,"width":1200,"height":560,"fill":"#ffffff","layout":"none","children":[
            {{"type":"ellipse","id":"e1","x":40,"y":40,"width":140,"height":100,"fill":"#ffffff","effect":[{e1}]}},
            {{"type":"text","id":"e2","x":240,"y":60,"content":"Shadow Text","fontFamily":"Arial","fontSize":32,"fill":"#3355ff","effect":[{e2}]}},
            {{"type":"polygon","id":"e3","x":480,"y":30,"width":120,"height":120,"polygonCount":5,"fill":"#ffcc00","effect":[{e3a},{e3b}]}},
            {{"type":"path","id":"e4","x":680,"y":30,"width":150,"height":120,"geometry":"M0 120 L40 10 L80 90 L150 0 L150 120 Z","fill":"#8844cc","effect":[{e4a},{e4b}]}},
            {{"type":"group","id":"e5","x":900,"y":30,"effect":[{e5}],"children":[
              {{"type":"rectangle","id":"e5a","x":0,"y":0,"width":70,"height":70,"fill":"#3399ff"}},
              {{"type":"ellipse","id":"e5b","x":60,"y":30,"width":80,"height":80,"fill":"#ff9933"}}]}},
            {{"type":"ellipse","id":"e6","x":40,"y":300,"width":140,"height":110,"fill":"#dddddd","effect":[{e6}]}},
            {{"type":"rectangle","id":"e7","x":240,"y":300,"width":140,"height":100,"cornerRadius":16,"fill":"#ffffff","effect":[{{"type":"shadow","offset":{{"x":0,"y":10}},"blur":20,"color":"#ff0066a0","blendMode":"multiply"}}]}},
            {{"type":"rectangle","id":"e8","x":480,"y":300,"width":140,"height":100,"cornerRadius":[24,4,24,4],"fill":"#ffffff","effect":[{e8}]}},
            {{"type":"frame","id":"e9","x":700,"y":280,"width":160,"height":130,"cornerRadius":10,"fill":"#eeeeff","layout":"none","effect":[{{"type":"blur","radius":4}},{e9}],"children":[
              {{"type":"ellipse","id":"e9a","x":10,"y":10,"width":60,"height":60,"fill":"#ff3366"}},
              {{"type":"text","id":"e9b","x":20,"y":80,"content":"Blurred","fontFamily":"Arial","fontSize":22,"fill":"#222222"}}]}},
            {{"type":"rectangle","id":"e10","x":950,"y":300,"width":140,"height":100,"cornerRadius":8,"fill":"#22aa66","effect":{{"type":"blur","radius":10}}}}]}}]}}"##,
            e1 = shadow(0, 8, 16, "#00000040", false),
            e2 = shadow(2, 4, 6, "#00000066", false),
            e3a = shadow(4, 6, 10, "#00000050", false),
            e3b = shadow(3, 3, 6, "#ff000080", true),
            e4a = shadow(5, 5, 8, "#00000060", false),
            e4b = shadow(-3, -3, 4, "#ffffff90", true),
            e5 = shadow(0, 6, 10, "#00000060", false),
            e6 = shadow(6, 8, 12, "#00000080", true),
            e8 = shadow(0, 8, 16, "#00000040", false),
            e9 = shadow(0, 4, 8, "#00000040", false),
        );
        Doc::from_json(&json).unwrap()
    }

    /// Shadows of any outline, inner shadows, blended shadows and layer blurs are vello filter
    /// layers (nothing falls back to resvg), within 2/255 of resvg in each effect's area at 50%,
    /// 100% and 200%.
    #[test]
    fn effects_match_resvg() {
        let doc = effects();
        let scene = convert(&doc);
        assert_eq!(count(&scene, |o| matches!(o, Op::Fallback { .. })), 0, "no resvg fallback");
        let mut report = Vec::new();
        for zoom in [0.5, 1.0, 2.0] {
            let (w, h) = ((1200.0 * zoom) as u32, (560.0 * zoom) as u32);
            let [v, s] = both(&doc, w, h, (0.0, 0.0), zoom);
            for (name, (x, y, aw, ah)) in EFFECTS {
                let z = |v: u32| (v as f64 * zoom) as u32;
                let d = mean_diff(&v, &s, w, (z(x), z(y), z(aw).min(w - z(x)), z(ah).min(h - z(y))));
                report.push((format!("{name} at {zoom}"), d));
            }
            if let Ok(dir) = std::env::var("NALARUPA_RASTER_PNG") {
                for (name, px) in [("vello", &v), ("resvg", &s)] {
                    let pm = tiny_skia::Pixmap::from_vec(px.to_vec(), tiny_skia::IntSize::from_wh(w, h).unwrap()).unwrap();
                    pm.save_png(format!("{dir}/effects-{name}-{zoom}.png")).unwrap();
                }
            }
        }
        println!("{report:.3?}");
        let bad: Vec<_> = report.iter().filter(|r| r.1 > 2.0).collect();
        assert!(bad.is_empty(), "mean diff over 2/255 in effect areas: {bad:.3?}\nall: {report:.3?}");
    }

    /// Effects cut by band edges at odd offsets (filter layers and backdrops then blur differently
    /// sized pixmaps) match one band painting everything: no seams.
    #[test]
    fn effects_have_no_band_seams() {
        let json = format!(
            r#"{{"version":"2.19","children":[{},{}]}}"#,
            effects().root["children"][0],
            glass(true, 1.0, true).root["children"][1].to_string().replace(r#""x":60,"y":60"#, r#""x":920,"y":250"#)
        );
        let doc = Doc::from_json(&json).unwrap();
        let (mut fonts, mut assets) = offline();
        let mut cx = Ctx { doc: &doc, libs: &[], fonts: &mut fonts, assets: &mut assets };
        let opts = cx.tree_options();
        let trees: Vec<usvg::Tree> = (0..2)
            .map(|i| {
                let root = cx.laid_out(i).unwrap();
                let mut svg = crate::svg::Svg::new(cx.fonts, cx.assets, None);
                svg.node(&root);
                usvg::Tree::from_str(&svg.finish(None), &opts).unwrap()
            })
            .collect();
        let scenes: Vec<Scene> = trees.iter().map(Scene::new).collect();
        let list: Vec<(&Scene, &usvg::Tree)> = scenes.iter().zip(&trees).collect();
        let (w, h, zoom) = (1300u32, 600u32, 1.1);
        let mut one = vec![0u8; (w * h * 4) as usize];
        paint(&mut Band::default(), &list, zoom, BG, -13, -7, w, h, &mut one);
        let mut banded = vec![0u8; (w * h * 4) as usize];
        for (i, rows) in banded.chunks_mut((w * 37 * 4) as usize).enumerate() {
            paint(&mut Band::default(), &list, zoom, BG, -13, -7 + i as i32 * 37, w, (rows.len() / (w as usize * 4)) as u32, rows);
        }
        let worst = one.iter().zip(&banded).map(|(a, b)| a.abs_diff(*b)).max().unwrap();
        let d = mean_diff(&one, &banded, w, (0, 0, w, h));
        println!("band seams: mean diff {d:.4}, worst channel diff {worst}");
        assert!(d < 0.05 && worst <= 6, "band seams: mean diff {d:.3}, worst channel diff {worst}");
    }

    /// Stripes under a translucent glass panel with a background blur (σ 8): inside the stripes'
    /// frame, or as its own top-level node (`over`). `opacity` applies to the glass.
    fn glass(over: bool, opacity: f64, blur: bool) -> Doc {
        let stripes: Vec<String> = (0..8)
            .map(|j| format!(r##"{{"type":"rectangle","id":"s{j}","x":{},"y":0,"width":25,"height":300,"fill":"{}"}}"##, j * 50, ["#ff3366", "#33cc99", "#3366ff", "#ffcc00"][j % 4]))
            .collect();
        let effect = if blur { r#","effect":{"type":"background_blur","radius":16}"# } else { "" };
        let glass = format!(r##"{{"type":"frame","id":"glass","x":60,"y":60,"width":280,"height":180,"cornerRadius":20,"fill":"#ffffff40","opacity":{opacity},"layout":"none"{effect}}}"##);
        let (inner, top) = if over { (String::new(), format!(",{glass}")) } else { (format!(",{glass}"), String::new()) };
        let json = format!(r##"{{"version":"2.19","children":[{{"type":"frame","id":"bg","x":0,"y":0,"width":400,"height":300,"fill":"#ffffff","layout":"none","children":[{}{inner}]}}{top}]}}"##, stripes.join(","));
        Doc::from_json(&json).unwrap()
    }

    fn renderer(raster: Raster) -> Renderer {
        let mut r = Renderer::default();
        r.raster = raster;
        r
    }

    fn vello(doc: &Doc, w: u32, h: u32) -> Vec<u8> {
        let mut r = renderer(Raster::Vello);
        sync(&mut r, doc);
        r.render(w, h, 0.0, 0.0, 1.0, BG, None, true).to_vec()
    }

    /// The glass (60, 60, 280, 180) without its rounded corners.
    const GLASS: (u32, u32, u32, u32) = (66, 66, 268, 168);

    /// An angular-gradient stroke is a pattern stroke drawn through the resvg fallback, which must
    /// cover the whole stroke band: the outer half lies outside the node's fill box.
    #[test]
    fn angular_stroke_is_not_clipped_to_the_fill_box() {
        for kind in ["ellipse", "rectangle"] {
            let json = format!(r##"{{"version":"2.19","children":[
                {{"type":"frame","id":"f","x":0,"y":0,"width":140,"height":140,"fill":"#ffffff","layout":"none","children":[
                  {{"type":"{kind}","id":"n","x":20,"y":20,"width":100,"height":100,"stroke":{{"type":"gradient","gradientType":"angular","colors":[{{"color":"#00ff00","position":0}},{{"color":"#00ff00","position":1}}]}},"strokeWidth":12,"strokeAlignment":"outer"}}]}}]}}"##);
            let px = vello(&Doc::from_json(&json).unwrap(), 140, 140);
            let i = (70 * 140 + 16) * 4; // 4 px outside the left edge, mid height
            assert_eq!(px[i..i + 3], [0, 255, 0], "{kind}: the outer stroke is missing");
        }
    }

    /// A horizontal or vertical path has no layer box, so resvg cannot draw it alone: its pattern
    /// stroke (an angular gradient over the node box) must still show.
    #[test]
    fn angular_stroke_of_an_axis_aligned_path_shows() {
        let json = r##"{"version":"2.19","children":[
            {"type":"frame","id":"f","x":0,"y":0,"width":140,"height":140,"fill":"#ffffff","layout":"none","children":[
              {"type":"path","id":"n","x":20,"y":60,"width":100,"height":20,"geometry":"M0 10L100 10","stroke":{"type":"gradient","gradientType":"angular","colors":[{"color":"#00ff00","position":0},{"color":"#00ff00","position":1}]},"strokeWidth":12}]}]}"##;
        let px = vello(&Doc::from_json(json).unwrap(), 140, 140);
        let i = (60 * 140 + 70) * 4;
        assert_eq!(px[i..i + 3], [0, 255, 0], "the path's stroke is missing");
    }

    /// A background blur blurs whatever is painted beneath it, also other top-level frames
    /// (resvg's approximation only sees its own frame, which here holds everything beneath).
    #[test]
    fn background_blur_reads_the_canvas() {
        let [same, approx] = both(&glass(false, 1.0, true), 400, 300, (0.0, 0.0), 1.0);
        let over = vello(&glass(true, 1.0, true), 400, 300);
        let sharp = vello(&glass(true, 1.0, false), 400, 300);
        let d = mean_diff(&over, &same, 400, GLASS);
        assert!(d < 0.25, "glass over another frame differs from glass inside it: {d:.3}");
        let d = mean_diff(&same, &approx, 400, GLASS);
        assert!(d <= 2.0, "glass differs from resvg's same-frame backdrop: {d:.3}");
        let d = mean_diff(&over, &sharp, 400, GLASS);
        assert!(d > 10.0, "the stripes under the glass are not blurred: {d:.3}");
    }

    /// The backdrop is drawn inside the node's opacity layer, which starts out empty: a
    /// translucent node shows no background blur. Inside a translucent parent, the blur reads only
    /// that parent's layer, so content beneath the parent stays sharp.
    #[test]
    fn background_blur_reads_its_layer() {
        for raster in [Raster::Vello, Raster::Resvg] {
            let render = |doc: &Doc| {
                let mut r = renderer(raster);
                sync(&mut r, doc);
                r.render(400, 300, 0.0, 0.0, 1.0, BG, None, true).to_vec()
            };
            let d = mean_diff(&render(&glass(true, 0.6, true)), &render(&glass(true, 0.6, false)), 400, GLASS);
            assert!(d == 0.0, "{raster:?}: translucent glass blurred its backdrop ({d:.3})");
        }
        // Vertical stripes under a translucent layer holding a horizontal band and the glass.
        let json = r##"{"version":"2.19","children":[
            {"type":"frame","id":"under","x":0,"y":0,"width":400,"height":300,"fill":"#ffffff","layout":"none","children":[
              {"type":"rectangle","id":"u1","x":75,"y":0,"width":50,"height":300,"fill":"#00aa00"},
              {"type":"rectangle","id":"u2","x":225,"y":0,"width":50,"height":300,"fill":"#00aa00"}]},
            {"type":"frame","id":"layer","x":0,"y":0,"width":400,"height":300,"opacity":0.9,"layout":"none","children":[
              {"type":"rectangle","id":"band","x":0,"y":100,"width":400,"height":40,"fill":"#0000ff"},
              {"type":"frame","id":"glass","x":60,"y":60,"width":280,"height":180,"layout":"none","effect":{"type":"background_blur","radius":16}}]}]}"##;
        let px = vello(&Doc::from_json(json).unwrap(), 400, 300);
        let at = |x: usize, y: usize| px[(y * 400 + x) * 4..(y * 400 + x) * 4 + 3].iter().map(|&c| c as i32).collect::<Vec<_>>();
        let step = |a: Vec<i32>, b: Vec<i32>| a.iter().zip(&b).map(|(p, q)| (p - q).abs()).max().unwrap();
        assert!(step(at(74, 210), at(75, 210)) > 100, "the stripe beneath the layer was blurred");
        assert!(step(at(200, 99), at(200, 100)) < 40, "the band in the layer was not blurred");
    }

    /// Moving content under a background blur in another top-level frame repaints the whole blur,
    /// in bands of any height.
    #[test]
    fn background_blur_repaints_with_what_is_beneath() {
        let mut doc = glass(true, 1.0, true);
        let frame = |r: &mut Renderer| r.render(400, 300, 0.0, 0.0, 1.0, BG, None, true).to_vec();
        let mut live = renderer(Raster::Vello);
        sync(&mut live, &doc);
        frame(&mut live);
        doc.root["children"][0]["children"][3]["x"] = serde_json::json!(160);
        sync(&mut live, &doc);
        let incremental = frame(&mut live);
        let mut cold = renderer(Raster::Vello);
        sync(&mut cold, &doc);
        let d = mean_diff(&incremental, &frame(&mut cold), 400, (0, 0, 400, 300));
        assert!(d < 0.05, "stale backdrop after moving a stripe beneath it: {d:.3}");
    }

    fn straight(pm: &tiny_skia::Pixmap, x: u32, y: u32) -> [u8; 4] {
        let c = pm.pixel(x, y).unwrap().demultiply();
        [c.red(), c.green(), c.blue(), c.alpha()]
    }

    /// Corners take their point's color and the middle of a default 2×2 mesh the even mix;
    /// moving a 3×3 mesh's centre point moves its color with it.
    #[test]
    fn mesh_gradient_colors() {
        let mesh = |v: serde_json::Value| mesh_gradient(v.as_object().unwrap(), 64, 64).unwrap();
        let near = |a: [u8; 4], b: [u8; 4], tol: u8| a.iter().zip(b).all(|(p, q)| p.abs_diff(q) <= tol);
        let pm = mesh(serde_json::json!({"type": "mesh_gradient", "columns": 2, "rows": 2, "colors": ["#ff0000", "#00ff00", "#0000ff", "#ffff00"], "points": [[0, 0], [1, 0], [0, 1], [1, 1]]}));
        for ((x, y), c) in [(0, 0), (63, 0), (0, 63), (63, 63)].into_iter().zip([[255, 0, 0, 255], [0, 255, 0, 255], [0, 0, 255, 255], [255, 255, 0, 255]]) {
            assert!(near(straight(&pm, x, y), c, 1), "corner ({x}, {y}): {:?}", straight(&pm, x, y));
        }
        let mid: [u8; 4] = std::array::from_fn(|n| ([(31, 31), (32, 31), (31, 32), (32, 32)].iter().map(|&(x, y)| straight(&pm, x, y)[n] as u32).sum::<u32>() / 4) as u8);
        assert!(near(mid, [127, 127, 63, 255], 2), "middle: {mid:?}");
        let points = |centre: [f64; 2]| serde_json::json!([[0, 0], [0.5, 0], [1, 0], [0, 0.5], centre, [1, 0.5], [0, 1], [0.5, 1], [1, 1]]);
        let three = |centre| mesh(serde_json::json!({"columns": 3, "rows": 3, "colors": ["#00f", "#00f", "#00f", "#00f", "#f00", "#00f", "#00f", "#00f", "#00f"], "points": points(centre)}));
        let (home, moved) = (three([0.5, 0.5]), three([0.25, 0.25]));
        assert!(straight(&home, 32, 32)[0] > 240 && straight(&home, 16, 16)[0] < 128, "{:?} {:?}", straight(&home, 32, 32), straight(&home, 16, 16));
        assert!(straight(&moved, 16, 16)[0] > 240 && straight(&moved, 32, 32)[0] < 220, "{:?} {:?}", straight(&moved, 16, 16), straight(&moved, 32, 32));
        assert!(home.pixels().iter().chain(moved.pixels()).all(|p| p.alpha() == 255), "the default mesh covers its box");
        assert!(mesh_gradient(serde_json::json!({"columns": 3, "rows": 3, "colors": ["#fff"], "points": points([0.5, 0.5])}).as_object().unwrap(), 8, 8).is_none());
    }

    /// A rounded rectangle with a 3×3 mesh (moved centre, curved handles).
    fn mesh_doc() -> Doc {
        let json = r##"{"version":"2.19","children":[{"type":"frame","id":"bg","x":0,"y":0,"width":480,"height":360,"fill":"#ffffff","layout":"none","children":[
            {"type":"rectangle","id":"mesh","x":40,"y":40,"width":400,"height":280,"cornerRadius":32,"fill":{"type":"mesh_gradient","columns":3,"rows":3,
              "colors":["#ff3366","#ffcc00","#33cc99","#6633ff","#ffffff","#00aaff","#ff6600","#cc33ff","#003366"],
              "points":[[0,0],{"position":[0.5,0],"leftHandle":[-0.15,0.12],"rightHandle":[0.15,-0.12]},[1,0],
                [0,0.5],{"position":[0.65,0.35],"leftHandle":[-0.25,0.1],"rightHandle":[0.2,-0.05],"topHandle":[0.1,-0.15],"bottomHandle":[-0.15,0.25]},[1,0.5],
                [0,1],{"position":[0.5,1],"leftHandle":[-0.2,-0.1],"rightHandle":[0.1,0.1]},[1,1]]}}]}]}"##;
        Doc::from_json(json).unwrap()
    }

    /// The mesh is an image pattern vello draws itself, and it matches resvg and is not flat.
    #[test]
    fn mesh_gradient_matches_resvg() {
        let doc = mesh_doc();
        let scene = convert(&doc);
        assert_eq!(count(&scene, |o| matches!(o, Op::Image { .. })), 1, "the mesh fill");
        assert_eq!(count(&scene, |o| matches!(o, Op::Fallback { .. })), 0);
        let (w, h) = (480, 360);
        let [v, s] = both(&doc, w, h, (0.0, 0.0), 1.0);
        let d = mean_diff(&v, &s, w, (40, 40, 400, 280));
        assert!(d < 1.0, "mesh: mean diff {d:.3}");
        let colors: std::collections::HashSet<&[u8]> = (40..320).flat_map(|y| (40..440).map(move |x| (y * w + x) as usize * 4)).map(|i| &v[i..i + 3]).collect();
        println!("mesh: mean diff {d:.3}, {} colors", colors.len());
        assert!(colors.len() > 2000, "only {} colors", colors.len());
        if let Ok(dir) = std::env::var("NALARUPA_RASTER_PNG") {
            for (name, px) in [("vello", &v), ("resvg", &s)] {
                let pm = tiny_skia::Pixmap::from_vec(px.to_vec(), tiny_skia::IntSize::from_wh(w, h).unwrap()).unwrap();
                pm.save_png(format!("{dir}/mesh-{name}.png")).unwrap();
            }
        }
    }

    /// Rotated text is culled by where it lands, not by usvg's flattened-path boxes (those ignore the node's transform).
    #[test]
    fn rotated_text_is_not_cut() {
        let json = r##"{"version":"2.19","children":[{"type":"text","id":"t","x":20,"y":150,"content":"Rotated text sample","fontSize":24,"fontFamily":"Inter","fill":"#ff5500","rotation":30}]}"##;
        let doc = Doc::from_json(json).unwrap();
        let (w, h) = (400u32, 260u32);
        let reach = |px: &[u8]| {
            let orange = |x: u32, y: u32| {
                let i = (y * w + x) as usize * 4;
                px[i] > 200 && px[i + 1] < 160 && px[i + 2] < 100
            };
            (0..h).flat_map(|y| (0..w).map(move |x| (x, y))).filter(|&(x, y)| orange(x, y)).fold((h, 0), |(t, r), (x, y)| (t.min(y), r.max(x)))
        };
        let [v, s] = both(&doc, w, h, (0.0, 0.0), 1.0);
        let (vello, resvg) = (reach(&v), reach(&s));
        assert!(resvg.1 > 200, "the reference draws the whole text: {resvg:?}");
        assert!(vello.0.abs_diff(resvg.0) <= 2 && vello.1.abs_diff(resvg.1) <= 2, "vello {vello:?} vs resvg {resvg:?}");
    }

    /// A blended child of a clipping frame blends against the frame's fill, as without `clip`
    /// (nested one level down too, and with the frame stroke still above the child).
    #[test]
    fn blend_inside_clip_frame() {
        let frame = |x: u32, clip: bool, child: &str| {
            format!(r##"{{"type":"frame","id":"f{x}","x":{x},"y":0,"width":100,"height":60,"layout":"none","fill":"#ffff00","clip":{clip},"stroke":"#ff0000","strokeWidth":4,"strokeAlignment":"inner","children":[{child}]}}"##)
        };
        let rect = r##"{"type":"rectangle","x":0,"y":10,"width":60,"height":40,"fill":{"type":"color","color":"#00ffff","blendMode":"multiply"}}"##;
        let nested = format!(r##"{{"type":"group","x":0,"y":0,"children":[{rect}]}}"##);
        let json = format!(
            r#"{{"version":"2.19","children":[{},{},{}]}}"#,
            frame(0, false, rect),
            frame(200, true, rect),
            frame(400, true, &nested)
        );
        let doc = Doc::from_json(&json).unwrap();
        let (w, h) = (500u32, 60u32);
        for (raster, px) in [Raster::Vello, Raster::Resvg].into_iter().zip(both(&doc, w, h, (0.0, 0.0), 1.0)) {
            let at = |x: u32, y: u32| {
                let i = (y * w + x) as usize * 4;
                [px[i], px[i + 1], px[i + 2]]
            };
            for x in [50, 250, 450] {
                let c = at(x, 30);
                assert!(c[0] < 8 && c[1] > 247 && c[2] < 8, "{raster:?} multiply at x={x}: {c:?}");
            }
            assert!(at(202, 30)[0] > 247 && at(202, 30)[1] < 8, "{raster:?} the clipped frame's stroke stays above");
        }
    }

    /// Colour emoji are images inside the flattened text; they are culled by where they land, at any depth or offset.
    #[test]
    fn emoji_far_from_the_origin_are_not_culled() {
        let json = r##"{"version":"2.19","children":[{"type":"text","id":"t","x":1020,"y":1500,"content":"😀😀😀","fontSize":40,"fontFamily":"Inter","fill":"#000000"}]}"##;
        let doc = Doc::from_json(json).unwrap();
        let (w, h) = (300u32, 100u32);
        let ink = |px: &[u8]| px.chunks(4).filter(|p| p[..3].iter().zip(BG).any(|(a, b)| a.abs_diff(b) > 40)).count();
        let [v, s] = both(&doc, w, h, (1000.0, 1480.0), 1.0);
        let (vello, resvg) = (ink(&v), ink(&s));
        assert!(count(&convert(&doc), |o| matches!(o, Op::Image { .. })) > 0, "the emoji are colour images, not monochrome glyphs");
        assert!(resvg > 500, "the reference draws the emoji: {resvg}");
        assert!(vello * 10 >= resvg * 9, "vello {vello} vs resvg {resvg}");
    }
}
