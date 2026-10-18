//! Rasterizer: one cached resvg tree per top-level node, rebuilt only when that
//! node (or shared components/variables) changes. The canvas keeps one raster of
//! the viewport plus a margin: panning inside it is a copy, zooming
//! scales it until the UI asks for a sharp frame, and edits repaint only their
//! dirty rectangle. Painting culls per node and splits into bands across cores; each band is
//! rasterized by vello_cpu (`raster.rs`), or by resvg alone with `NALARUPA_RASTER=resvg`.

use crate::doc::Doc;
use crate::raster;
use crate::resolve::{RNode, Resolver};
use crate::svg::{fxhash, Assets, Svg};
use crate::text::Fonts;
use resvg::{tiny_skia, usvg};
use serde_json::{json, Map, Value};
use std::collections::{HashMap, HashSet};
use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, Instant};

/// Insert animation: a node the agent inserts
/// starts 50 units low and transparent; the offset decays at 20/s and the opacity at 5/s.
/// Layout siblings the insert moved glide from where they were with the same offset decay.
const SLIDE_OFFSET: f64 = 50.0;
const SLIDE_DECAY: f64 = 20.0;
const FADE_DECAY: f64 = 5.0;
/// Done at offset < 0.5 and opacity > 0.95: ln(20)/5 ≈ 0.6 s (the offset settles at 0.23 s).
const SLIDE_TIME: Duration = Duration::from_millis(600);
/// A slide no frame has shown yet (canvas not on screen) is dropped after this.
const SLIDE_WAIT: Duration = Duration::from_secs(2);

/// A node sliding into place. It is left out of the cached view and painted from its own
/// snapshot, offset and faded, over every frame until it settles (cheaper than re-laying out
/// and re-rasterizing its layer each frame; looks the same).
pub struct Slide {
    pub id: String,
    added: Instant,
    /// The first frame that showed it: the animation clock starts there, not at the insert.
    start: Option<Instant>,
    /// Offset at the start (doc units); it decays to zero.
    from: (f64, f64),
    /// Fades in (an inserted node) rather than staying opaque (a sibling the insert moved).
    fade: bool,
    /// Inserted since the last sync: its layout siblings are still to be checked for moves.
    fresh: bool,
    /// The node alone in absolute doc coordinates, without descendants that slide themselves.
    tree: Option<usvg::Tree>,
    /// Sliding ancestors: their offset and fade carry this node along.
    ancestors: Vec<String>,
    /// Raster of `tree` at (zoom, device rect).
    cache: Option<(f64, (i32, i32, u32, u32), tiny_skia::Pixmap)>,
}

impl Slide {
    pub fn new(id: &str) -> Slide {
        Slide { id: id.to_string(), added: Instant::now(), start: None, from: (0.0, SLIDE_OFFSET), fade: true, fresh: true, tree: None, ancestors: Vec::new(), cache: None }
    }

    /// (offset x, offset y, opacity) now.
    fn state(&self, now: Instant) -> (f64, f64, f64) {
        let t = self.start.map_or(0.0, |s| now.duration_since(s).as_secs_f64());
        let k = (-SLIDE_DECAY * t).exp();
        (self.from.0 * k, self.from.1 * k, if self.fade { 1.0 - (-FADE_DECAY * t).exp() } else { 1.0 })
    }
}

/// Flattened, laid-out node used for hit-testing, overlays and agent queries.
#[derive(Clone)]
pub struct Flat {
    pub id: String,
    pub name: String,
    pub ty: String,
    pub x: f64,
    pub y: f64,
    pub w: f64,
    pub h: f64,
    pub depth: usize,
    pub parent: Option<String>,
    pub problem: Option<&'static str>,
    /// Frame marked `placeholder: true` (agent still working on it).
    pub placeholder: bool,
    /// Has an image fill that is still being generated.
    pub pending: bool,
    /// Painted transform `[a, b, c, d, e, f]` (x' = a·x + c·y + e, y' = b·x + d·y + f) from this box's
    /// coordinates to the canvas: own and ancestors' rotations/flips. `None` = identity.
    pub m: Option<[f64; 6]>,
    /// Own props, box, parent and sibling index: a change means this node must repaint.
    sig: u64,
    /// How far shadows, blurs and strokes paint outside the box; infinite when the node or an
    /// ancestor is rotated or flipped (it paints outside its layout box).
    reach: f64,
}

pub struct Layer {
    hash: u64,
    tree: Option<usvg::Tree>,
    scene: Option<raster::Scene>,
    pub bbox: (f64, f64, f64, f64),
    /// Everything this layer paints, including shadows and blur outside a clipping frame.
    paint: Rect,
    pub flat: Vec<Flat>,
}

type Rect = (f64, f64, f64, f64);

/// Device pixels kept around the viewport on each side.
const MARGIN: i32 = 512;
/// A scaled preview is only used up to this zoom ratio; beyond it the frame is repainted.
const MAX_PREVIEW_SCALE: f64 = 3.0;

/// Live zoom frames are painted sharp (viewport only) while that is estimated to fit this budget.
const LIVE_BUDGET_MS: f64 = 30.0;

/// Which rasterizer paints the canvas.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Raster {
    Vello,
    /// resvg/tiny-skia only (`NALARUPA_RASTER=resvg`): for A/B tests and as the reference.
    Resvg,
}

impl Default for Raster {
    fn default() -> Self {
        if std::env::var("NALARUPA_RASTER").is_ok_and(|v| v == "resvg") { Raster::Resvg } else { Raster::Vello }
    }
}

/// The cached raster: `pix` covers world-device pixels starting at (`x0`, `y0`) = doc x zoom.
struct View {
    pix: tiny_skia::Pixmap,
    x0: i32,
    y0: i32,
    zoom: f64,
    bg: [u8; 3],
    solo: Option<String>,
}

#[derive(Default)]
pub struct Renderer {
    pub layers: HashMap<String, Layer>,
    pub order: Vec<String>,
    pub pixels: Vec<u8>,
    view: Option<View>,
    /// Doc-space areas changed since the cached view was painted.
    dirty: Vec<Rect>,
    /// Stacking order changed: the cached view is unusable.
    restack: bool,
    /// Variables/themes/components hash of the last sync; a change can restyle anything.
    shared: u64,
    pub raster: Raster,
    /// One reusable vello context per band.
    bands: Vec<raster::Band>,
    /// Cost of the last full paint per megapixel (0 = not measured yet).
    ms_per_mp: f64,
    /// Agent-inserted nodes sliding into place.
    pub slides: Vec<Slide>,
}

/// Rasterize a node tree from `Ctx::node_tree_at(.., scale)`: needs no engine state, so callers
/// can do it after releasing the engine lock (agent screenshots).
pub fn encode_tree(tree: &usvg::Tree, format: &str, scale: f64, quality: u8) -> Result<Vec<u8>, String> {
    let size = tree.size();
    // The view is already whole pixels at `scale` (see `node_svg_at`); round away f32 noise.
    let (pw, ph) = (((size.width() as f64 * scale).round() as u32).clamp(1, 8192), ((size.height() as f64 * scale).round() as u32).clamp(1, 8192));
    let mut pm = tiny_skia::Pixmap::new(pw, ph).ok_or("image too large")?;
    if format == "jpeg" {
        pm.fill(tiny_skia::Color::WHITE);
    }
    let sx = pw as f32 / size.width();
    let sy = ph as f32 / size.height();
    resvg::render(tree, tiny_skia::Transform::from_scale(sx, sy), &mut pm.as_mut());
    let mut out = Vec::new();
    match format {
        // Opaque after the white fill, so the premultiplied pixels are plain RGBA.
        "jpeg" => jpeg_encoder::Encoder::new(&mut out, quality.clamp(1, 100))
            .encode(pm.data(), pw as u16, ph as u16, jpeg_encoder::ColorType::Rgba)
            .map_err(|e| e.to_string())?,
        "webp" => {
            let rgba: Vec<u8> = pm.pixels().iter().flat_map(|p| {
                let c = p.demultiply();
                [c.red(), c.green(), c.blue(), c.alpha()]
            }).collect();
            image_webp::WebPEncoder::new(&mut out).encode(&rgba, pw, ph, image_webp::ColorType::Rgba8).map_err(|e| e.to_string())?
        }
        _ => out = pm.encode_png().map_err(|e| e.to_string())?,
    }
    Ok(out)
}

pub struct Ctx<'a> {
    pub doc: &'a Doc,
    pub libs: &'a [(String, Value)],
    pub fonts: &'a mut Fonts,
    pub assets: &'a mut Assets,
}

impl Ctx<'_> {
    /// Resolve + lay out the top-level node at `index`.
    pub fn laid_out(&mut self, index: usize) -> Option<RNode> {
        let empty = Map::new();
        let r = Resolver::new(&self.doc.root, self.libs, &empty);
        let mut n = r.resolve(self.doc.children().get(index)?)?;
        crate::layout::layout(&mut n, self.fonts);
        // Script nodes need their laid-out size before they can run.
        crate::script::expand_scripts(&mut n, &r, self.fonts, self.doc.dir().as_deref());
        Some(n)
    }

    pub fn tree_options(&self) -> usvg::Options<'static> {
        let mut opt = usvg::Options { fontdb: Arc::clone(&self.fonts.db), font_resolver: self.fonts.resolver(), ..usvg::Options::default() };
        opt.resources_dir = self.doc.dir();
        opt
    }

    /// SVG for a single (possibly nested) node, cropped to its box.
    pub fn node_svg(&mut self, id: &str) -> Option<(String, (f64, f64, f64, f64))> {
        self.node_svg_at(id, 1.0)
    }

    /// Raster export: the node's visual bounds (shadows, blur and outer stroke
    /// included) from their unrounded corner, `ceil((max − min) · scale)` pixels in each axis. The
    /// float arithmetic matches JS, so e.g. y = 3266.9999999999995 + 933 exports 934 rows.
    fn node_svg_at(&mut self, id: &str, scale: f64) -> Option<(String, (f64, f64, f64, f64))> {
        let top = top_index(self.doc, id)?;
        let root = self.laid_out(top)?;
        let node = find_rnode(&root, id)?;
        let (x, y, w, h) = crate::svg::visual_bounds(node);
        let pw = (((x + w) - x) * scale).ceil().max(1.0);
        let ph = (((y + h) - y) * scale).ceil().max(1.0);
        let view = (x, y, pw / scale, ph / scale);
        let mut svg = Svg::new(self.fonts, self.assets, self.doc.dir());
        svg.node(node);
        Some((svg.finish(Some(view)), view))
    }

    fn node_tree(&mut self, id: &str) -> Result<usvg::Tree, String> {
        self.node_tree_at(id, 1.0)
    }

    pub fn node_tree_at(&mut self, id: &str, scale: f64) -> Result<usvg::Tree, String> {
        let (svg, _) = self.node_svg_at(id, scale).ok_or_else(|| format!("node not found: {id}"))?;
        usvg::Tree::from_str(&svg, &self.tree_options()).map_err(|e| e.to_string())
    }

    pub fn node_png(&mut self, id: &str, scale: f64) -> Result<Vec<u8>, String> {
        self.node_image(id, "png", scale, 0)
    }

    /// Rasterize a node as "png", "jpeg" (flattened on white, `quality` 1–100) or "webp" (lossless).
    pub fn node_image(&mut self, id: &str, format: &str, scale: f64, quality: u8) -> Result<Vec<u8>, String> {
        let scale = scale.clamp(0.05, 8.0);
        encode_tree(&self.node_tree_at(id, scale)?, format, scale, quality)
    }

    /// One PDF page per node (1 px = 1 pt), in the given order; text stays selectable.
    pub fn nodes_pdf(&mut self, ids: &[String]) -> Result<Vec<u8>, String> {
        use krilla_svg::SurfaceExt;
        let mut pdf = krilla::Document::new();
        for id in ids {
            let tree = self.node_tree(id)?;
            let size = tree.size();
            let settings = krilla::page::PageSettings::from_wh(size.width(), size.height()).ok_or("empty page")?;
            let mut page = pdf.start_page_with(settings);
            let mut surface = page.surface();
            let pt = krilla::geom::Size::from_wh(size.width(), size.height()).ok_or("empty page")?;
            surface.draw_svg(&tree, pt, krilla_svg::SvgSettings::default());
            surface.finish();
            page.finish();
        }
        pdf.finish().map_err(|e| format!("PDF export failed: {e:?}"))
    }
}

pub fn top_index(doc: &Doc, id: &str) -> Option<usize> {
    let head = id.split('/').next()?;
    doc.children().iter().position(|n| n["id"] == head || crate::doc::find_in(std::slice::from_ref(n), head).is_some())
}

/// Push `n` and the descendants of `n` on the way to node `id` (not `id` itself); false if absent.
fn ancestors_of<'r>(n: &'r RNode, id: &str, chain: &mut Vec<&'r RNode>) -> bool {
    chain.push(n);
    if n.kids.iter().any(|k| k.id == id || ancestors_of(k, id, chain)) {
        return true;
    }
    chain.pop();
    false
}

pub fn find_rnode<'r>(n: &'r RNode, id: &str) -> Option<&'r RNode> {
    if n.id == id {
        return Some(n);
    }
    n.kids.iter().find_map(|k| find_rnode(k, id))
}

fn shared_hash(doc: &Doc, libs: &[(String, Value)]) -> u64 {
    let mut s = String::new();
    s.push_str(&doc.root.get("variables").map(Value::to_string).unwrap_or_default());
    s.push_str(&doc.root.get("themes").map(Value::to_string).unwrap_or_default());
    crate::doc::walk(doc.children(), &mut |n| {
        if n["reusable"] == true {
            s.push_str(&n.to_string());
        }
    });
    for (a, l) in libs {
        s.push_str(a);
        s.push_str(&l["children"].as_array().map(|c| c.len()).unwrap_or(0).to_string());
    }
    s.push_str(&doc.dir().map(|d| d.to_string_lossy().to_string()).unwrap_or_default());
    fxhash(&s)
}

impl Renderer {
    /// Rebuild layers whose content changed since the last sync; returns their ids.
    pub fn sync(&mut self, cx: &mut Ctx) -> Vec<String> {
        let shared = shared_hash(cx.doc, cx.libs);
        let reshared = std::mem::replace(&mut self.shared, shared) != shared;
        let hidden: HashSet<String> = self.slides.iter().map(|s| s.id.clone()).collect();
        let mut order = Vec::new();
        let mut keep = HashMap::new();
        let mut built = Vec::new();
        for (i, node) in cx.doc.children().iter().enumerate() {
            let id = node["id"].as_str().unwrap_or_default().to_string();
            let mut hash = fxhash(&node.to_string()) ^ shared.rotate_left(17);
            // A node starting or ending its slide rebuilds its layer.
            for h in hidden.iter().filter(|h| crate::doc::find_in(std::slice::from_ref(node), h).is_some()) {
                hash ^= fxhash(h).rotate_left(29);
            }
            match self.layers.remove(&id) {
                Some(l) if l.hash == hash => {
                    keep.insert(id.clone(), l);
                }
                old => built.push((id.clone(), prepare_layer(cx, i, hash, &hidden), old)),
            }
            order.push(id);
        }
        let rebuilt: Vec<String> = built.iter().map(|(id, _, _)| id.clone()).collect();
        // Parsing (text shaping included) dominates; do it on all cores.
        let opts = cx.tree_options();
        let parse = &|svg: &String| {
            let tree = usvg::Tree::from_str(svg, &opts).ok()?;
            let scene = raster::Scene::new(&tree);
            Some((tree, scene))
        };
        let parsed: Vec<Option<(usvg::Tree, raster::Scene)>> = std::thread::scope(|s| {
            let jobs: Vec<_> = built.iter().map(|(_, (_, svg), _)| s.spawn(move || svg.as_ref().and_then(parse))).collect();
            jobs.into_iter().map(|j| j.join().ok().flatten()).collect()
        });
        for ((id, (mut layer, _), old), parsed) in built.into_iter().zip(parsed) {
            if let Some((t, scene)) = parsed {
                let b = t.root().abs_layer_bounding_box();
                layer.paint = union(layer.paint, (b.x() as f64, b.y() as f64, b.width() as f64, b.height() as f64));
                layer.tree = Some(t);
                layer.scene = Some(scene);
            }
            match old {
                Some(old) if !reshared => changed_nodes(&old, &layer, &mut self.dirty),
                Some(old) => self.dirty.extend([old.paint, layer.paint]),
                None => self.dirty.push(layer.paint),
            }
            keep.insert(id, layer);
        }
        for gone in self.layers.values() {
            self.dirty.push(gone.paint);
        }
        // Same layers in a different stacking order: every overlap may change.
        let (old, new): (HashSet<&String>, HashSet<&String>) = (self.order.iter().collect(), order.iter().collect());
        if !self.order.iter().filter(|id| new.contains(id)).eq(order.iter().filter(|id| old.contains(id))) {
            self.restack = true;
        }
        self.layers = keep;
        self.order = order;
        rebuilt
    }

    /// Layout change animation on insert: children of the new node's ancestors that the
    /// insert moved (auto-layout reflow) glide from where they were. Call before `sync` (the
    /// layers still hold the old positions); returns the layouts it made, for `refresh_slides`.
    pub fn shift_siblings(&mut self, cx: &mut Ctx) -> HashMap<usize, Option<RNode>> {
        let mut roots: HashMap<usize, Option<RNode>> = HashMap::new();
        let fresh: Vec<String> = self.slides.iter_mut().filter_map(|s| std::mem::take(&mut s.fresh).then(|| s.id.clone())).collect();
        let now = Instant::now();
        for id in &fresh {
            let Some(top) = top_index(cx.doc, id) else { continue };
            // A brand-new top-level node has no old positions: nothing moved.
            let Some(layer) = cx.doc.children()[top]["id"].as_str().and_then(|t| self.layers.get(t)) else { continue };
            let old: HashMap<&str, (f64, f64)> = layer.flat.iter().map(|f| (f.id.as_str(), (f.x, f.y))).collect();
            let Some(root) = roots.entry(top).or_insert_with(|| cx.laid_out(top)).as_ref() else { continue };
            let mut chain = Vec::new();
            if !ancestors_of(root, id, &mut chain) {
                continue;
            }
            for k in chain.iter().flat_map(|a| a.kids.iter()) {
                let Some(&(ox, oy)) = old.get(k.id.as_str()) else { continue };
                let (dx, dy) = (ox - k.x, oy - k.y);
                if dx.abs() < 0.5 && dy.abs() < 0.5 {
                    continue;
                }
                match self.slides.iter_mut().find(|s| s.id == k.id) {
                    // Already gliding: continue from where it shows now.
                    Some(s) if !s.fade => {
                        let (cx0, cy0, _) = s.state(now);
                        (s.from, s.start, s.cache) = ((cx0 + dx, cy0 + dy), None, None);
                    }
                    Some(_) => {}
                    None => self.slides.push(Slide { from: (dx, dy), fade: false, fresh: false, ..Slide::new(&k.id) }),
                }
            }
        }
        roots
    }

    /// Snapshot sliding nodes that are new or whose layer was just rebuilt (so a node edited
    /// mid-slide shows its edit). Each snapshot leaves out descendants that slide themselves.
    pub fn refresh_slides(&mut self, cx: &mut Ctx, rebuilt: &[String], mut roots: HashMap<usize, Option<RNode>>) {
        let ids: Vec<String> = self.slides.iter().map(|s| s.id.clone()).collect();
        for s in &mut self.slides {
            let Some(top) = top_index(cx.doc, &s.id) else {
                s.tree = None;
                continue;
            };
            let top_id = cx.doc.children()[top]["id"].as_str().unwrap_or_default();
            if s.tree.is_some() && !rebuilt.iter().any(|r| r == top_id) {
                continue;
            }
            let root = roots.entry(top).or_insert_with(|| cx.laid_out(top));
            let Some((root, node)) = root.as_ref().and_then(|r| Some((r, find_rnode(r, &s.id)?))) else {
                s.tree = None;
                continue;
            };
            let mut svg = Svg::new(cx.fonts, cx.assets, cx.doc.dir());
            svg.skip = ids.iter().filter(|i| **i != s.id).cloned().collect();
            svg.node(node);
            let text = svg.finish(None);
            s.tree = usvg::Tree::from_str(&text, &cx.tree_options()).ok();
            s.ancestors = ids.iter().filter(|a| **a != s.id && find_rnode(root, a).is_some_and(|an| find_rnode(an, &s.id).is_some())).cloned().collect();
            s.cache = None;
        }
    }

    /// Drop slides that have settled (or were never shown); true if any ended.
    pub fn expire_slides(&mut self) -> bool {
        let now = Instant::now();
        let before = self.slides.len();
        self.slides.retain(|s| match s.start {
            Some(t) => now.duration_since(t) < SLIDE_TIME,
            None => now.duration_since(s.added) < SLIDE_WAIT,
        });
        self.slides.len() != before
    }

    /// Paint the sliding nodes over the w×h frame in `self.pixels` (device origin = camera·zoom).
    fn composite_slides(&mut self, w: u32, h: u32, cam_x: f64, cam_y: f64, zoom: f64) {
        let now = Instant::now();
        for s in &mut self.slides {
            s.start.get_or_insert(now);
        }
        let states: HashMap<String, (f64, f64, f64)> = self.slides.iter().map(|s| (s.id.clone(), s.state(now))).collect();
        let (vx, vy) = ((cam_x * zoom).round() as i32, (cam_y * zoom).round() as i32);
        let Some(mut out) = tiny_skia::PixmapMut::from_bytes(&mut self.pixels, w, h) else { return };
        for s in &mut self.slides {
            let Some(tree) = &s.tree else { continue };
            let (mut dx, mut dy, mut alpha) = states[&s.id];
            for a in &s.ancestors {
                if let Some((adx, ady, aa)) = states.get(a) {
                    dx += adx;
                    dy += ady;
                    alpha *= aa;
                }
            }
            // Slides paint away from where they lie: content just outside the viewport can show
            // (the margin covers the start offset plus a sliding parent's).
            let m = s.from.0.abs().max(s.from.1.abs()) + 2.0 * SLIDE_OFFSET;
            let view = (cam_x - m, cam_y - m, w as f64 / zoom + 2.0 * m, h as f64 / zoom + 2.0 * m);
            let bb = tree.root().abs_layer_bounding_box();
            let b = (bb.x() as f64, bb.y() as f64, bb.width() as f64, bb.height() as f64);
            if !overlaps(b, view) {
                continue;
            }
            let (x0, y0) = ((b.0.max(view.0) * zoom).floor() as i32, (b.1.max(view.1) * zoom).floor() as i32);
            let x1 = ((b.0 + b.2).min(view.0 + view.2) * zoom).ceil() as i32;
            let y1 = ((b.1 + b.3).min(view.1 + view.3) * zoom).ceil() as i32;
            let key = (zoom, (x0, y0, (x1 - x0).max(1) as u32, (y1 - y0).max(1) as u32));
            if s.cache.as_ref().is_none_or(|c| c.0 != key.0 || c.1 != key.1) {
                let Some(mut pm) = tiny_skia::Pixmap::new(key.1 .2, key.1 .3) else { continue };
                resvg::render(tree, tiny_skia::Transform::from_row(zoom as f32, 0.0, 0.0, zoom as f32, -x0 as f32, -y0 as f32), &mut pm.as_mut());
                s.cache = Some((key.0, key.1, pm));
            }
            let Some((_, _, pm)) = &s.cache else { continue };
            let paint = tiny_skia::PixmapPaint { opacity: alpha.clamp(0.0, 1.0) as f32, ..Default::default() };
            out.draw_pixmap(x0 - vx + (dx * zoom).round() as i32, y0 - vy + (dy * zoom).round() as i32, pm.as_ref(), &paint, tiny_skia::Transform::identity(), None);
        }
    }

    pub fn flat(&self) -> impl Iterator<Item = &Flat> {
        self.order.iter().filter_map(|id| self.layers.get(id)).flat_map(|l| l.flat.iter())
    }

    pub fn find(&self, id: &str) -> Option<&Flat> {
        self.flat().find(|f| f.id == id)
    }

    /// Union of all top-level bounding boxes.
    pub fn bounds(&self) -> Option<(f64, f64, f64, f64)> {
        let boxes: Vec<_> = self.order.iter().filter_map(|id| self.layers.get(id)).map(|l| l.bbox).collect();
        let x0 = boxes.iter().map(|b| b.0).reduce(f64::min)?;
        let y0 = boxes.iter().map(|b| b.1).reduce(f64::min)?;
        let x1 = boxes.iter().map(|b| b.0 + b.2).reduce(f64::max)?;
        let y1 = boxes.iter().map(|b| b.1 + b.3).reduce(f64::max)?;
        Some((x0, y0, x1 - x0, y1 - y0))
    }

    /// Draw the visible part of the document into `self.pixels` (RGBA, opaque).
    /// Without `sharp`, a zoom change is served by a viewport-only paint when that is cheap
    /// enough, else by scaling the cached view; the UI asks for a sharp frame once the camera settles.
    #[allow(clippy::too_many_arguments)]
    pub fn render(&mut self, w: u32, h: u32, cam_x: f64, cam_y: f64, zoom: f64, bg: [u8; 3], solo: Option<&str>, sharp: bool) -> &[u8] {
        let (w, h) = (w.clamp(1, 8192), h.clamp(1, 8192));
        let zoom = zoom.clamp(0.01, 256.0);
        self.render_view(w, h, cam_x, cam_y, zoom, bg, solo, sharp);
        if !self.slides.is_empty() && solo.is_none() && self.pixels.len() == (w * h * 4) as usize {
            self.composite_slides(w, h, cam_x, cam_y, zoom);
        }
        &self.pixels
    }

    #[allow(clippy::too_many_arguments)]
    fn render_view(&mut self, w: u32, h: u32, cam_x: f64, cam_y: f64, zoom: f64, bg: [u8; 3], solo: Option<&str>, sharp: bool) -> &[u8] {
        let (vx, vy) = ((cam_x * zoom).round() as i32, (cam_y * zoom).round() as i32);
        let reusable = |v: &View| v.bg == bg && v.solo.as_deref() == solo;
        if std::mem::take(&mut self.restack) || self.view.as_ref().is_some_and(|v| !reusable(v)) {
            self.view = None;
        }
        let dirty = std::mem::take(&mut self.dirty);
        let mut p = Painter { layers: &self.layers, order: &self.order, raster: self.raster, bands: &mut self.bands };
        if let Some(v) = self.view.as_mut() {
            for d in under_backdrops(merge(dirty), &self.layers) {
                repaint(&mut p, v, d);
            }
        }
        if let Some(v) = &self.view {
            let (ox, oy) = (vx - v.x0, vy - v.y0);
            let exact = (v.zoom - zoom).abs() < 1e-9;
            if exact && ox >= 0 && oy >= 0 && ox as u32 + w <= v.pix.width() && oy as u32 + h <= v.pix.height() {
                crop_into(&mut self.pixels, &v.pix, ox as u32, oy as u32, w, h);
                return &self.pixels;
            }
            let live = !sharp && !exact;
            // The cached view stays at its zoom for the sharp frame that follows.
            if live && self.ms_per_mp * (w * h) as f64 / 1e6 < LIVE_BUDGET_MS {
                self.pixels.resize((w * h * 4) as usize, 0);
                if p.paint(solo, zoom, bg, vx, vy, w, h, &mut self.pixels) {
                    return &self.pixels;
                }
            }
            let s = zoom / v.zoom;
            let covered = {
                let (cx0, cy0) = (v.x0 as f64 / v.zoom, v.y0 as f64 / v.zoom);
                let (cx1, cy1) = (cx0 + v.pix.width() as f64 / v.zoom, cy0 + v.pix.height() as f64 / v.zoom);
                cam_x >= cx0 && cam_y >= cy0 && cam_x + w as f64 / zoom <= cx1 && cam_y + h as f64 / zoom <= cy1
            };
            if live && covered && (1.0 / MAX_PREVIEW_SCALE..=MAX_PREVIEW_SCALE).contains(&s) {
                self.pixels.resize((w * h * 4) as usize, 0);
                if let Some(mut pm) = tiny_skia::PixmapMut::from_bytes(&mut self.pixels, w, h) {
                    pm.fill(tiny_skia::Color::from_rgba8(bg[0], bg[1], bg[2], 255));
                    let tx = (v.x0 as f64 / v.zoom - cam_x) * zoom;
                    let ty = (v.y0 as f64 / v.zoom - cam_y) * zoom;
                    let paint = tiny_skia::PixmapPaint { quality: tiny_skia::FilterQuality::Bilinear, ..Default::default() };
                    pm.draw_pixmap(0, 0, v.pix.as_ref(), &paint, tiny_skia::Transform::from_row(s as f32, 0.0, 0.0, s as f32, tx as f32, ty as f32), None);
                    return &self.pixels;
                }
            }
        }
        let (x0, y0) = (vx - MARGIN, vy - MARGIN);
        let (cw, ch) = (w + 2 * MARGIN as u32, h + 2 * MARGIN as u32);
        let reuse = self.view.take().map(|v| v.pix).filter(|p| p.width() == cw && p.height() == ch);
        let t = std::time::Instant::now();
        let painted = reuse.or_else(|| tiny_skia::Pixmap::new(cw, ch)).and_then(|mut pix| p.paint(solo, zoom, bg, x0, y0, cw, ch, pix.data_mut()).then_some(pix));
        let Some(pix) = painted else {
            // Callers read w×h×4 bytes whatever happens.
            self.pixels.resize((w * h * 4) as usize, 0);
            return &self.pixels;
        };
        self.ms_per_mp = t.elapsed().as_secs_f64() * 1e9 / (cw * ch) as f64;
        crop_into(&mut self.pixels, &pix, MARGIN as u32, MARGIN as u32, w, h);
        self.view = Some(View { pix, x0, y0, zoom, bg, solo: solo.map(str::to_string) });
        &self.pixels
    }
}

/// What painting needs from the renderer, borrowed apart from the cached view.
struct Painter<'a> {
    layers: &'a HashMap<String, Layer>,
    order: &'a [String],
    raster: Raster,
    bands: &'a mut Vec<raster::Band>,
}

impl Painter<'_> {
    /// Paint world-device pixels [x0, x0+w) × [y0, y0+h) at `zoom` into `out` (w×h RGBA, opaque),
    /// in horizontal bands across cores. False if a band panicked.
    #[allow(clippy::too_many_arguments)]
    fn paint(&mut self, solo: Option<&str>, zoom: f64, bg: [u8; 3], x0: i32, y0: i32, w: u32, h: u32, out: &mut [u8]) -> bool {
        if out.len() != (w * h * 4) as usize {
            return false;
        }
        let threads = std::thread::available_parallelism().map_or(4, |n| n.get()).clamp(1, 16) as u32;
        let band = h.div_ceil(threads).max(128);
        let n = h.div_ceil(band) as usize;
        if self.bands.len() < n {
            self.bands.resize_with(n, Default::default);
        }
        let (layers, order, raster) = (self.layers, self.order, self.raster);
        std::thread::scope(|s| {
            let jobs: Vec<_> = out
                .chunks_mut((w * band * 4) as usize)
                .zip(self.bands.iter_mut())
                .enumerate()
                .map(|(i, (chunk, b))| {
                    let by = y0 + (i as u32 * band) as i32;
                    let bh = (chunk.len() / (w as usize * 4)) as u32;
                    s.spawn(move || paint_band(layers, order, solo, zoom, bg, x0, by, w, bh, raster, b, chunk))
                })
                .collect();
            // Join every band (not `all`): an unjoined panicked thread would panic the scope.
            jobs.into_iter().fold(true, |ok, j| j.join().is_ok() && ok)
        })
    }
}

fn union(a: Rect, b: Rect) -> Rect {
    let (x0, y0) = (a.0.min(b.0), a.1.min(b.1));
    (x0, y0, (a.0 + a.2).max(b.0 + b.2) - x0, (a.1 + a.3).max(b.1 + b.3) - y0)
}

/// Coalesce overlapping dirty rects; many scattered ones become their union.
fn merge(mut rects: Vec<Rect>) -> Vec<Rect> {
    let mut out: Vec<Rect> = Vec::new();
    while let Some(mut r) = rects.pop() {
        while let Some(i) = out.iter().position(|o| overlaps(*o, r)) {
            r = union(r, out.swap_remove(i));
        }
        out.push(r);
    }
    if out.len() > 8 {
        out = out.into_iter().reduce(union).into_iter().collect();
    }
    out
}

/// A background blur shows what lies beneath it: a change within its blur reach repaints all of
/// it (and, in turn, any background blur over that).
fn under_backdrops(mut rects: Vec<Rect>, layers: &HashMap<String, Layer>) -> Vec<Rect> {
    let backdrops: Vec<(Rect, f64)> = layers.values().filter_map(|l| l.scene.as_ref()).flat_map(|s| s.backdrops().iter().copied()).collect();
    loop {
        let mut grown = false;
        for r in rects.iter_mut() {
            for &(b, reach) in &backdrops {
                let near = overlaps((r.0 - reach, r.1 - reach, r.2 + 2.0 * reach, r.3 + 2.0 * reach), b);
                let inside = r.0 <= b.0 && r.1 <= b.1 && r.0 + r.2 >= b.0 + b.2 && r.1 + r.3 >= b.1 + b.3;
                if near && !inside {
                    *r = union(*r, b);
                    grown = true;
                }
            }
        }
        if !grown {
            return rects;
        }
        rects = merge(rects);
    }
}

/// Doc rects that differ between two builds of one layer: nodes added, removed, moved or restyled.
fn changed_nodes(old: &Layer, new: &Layer, out: &mut Vec<Rect>) {
    // The root carries the frame's own fill, clip and effects: any change there repaints it whole.
    if old.flat.first().map(|f| f.sig) != new.flat.first().map(|f| f.sig) {
        out.extend([old.paint, new.paint]);
        return;
    }
    let before: HashMap<&str, &Flat> = old.flat.iter().map(|f| (f.id.as_str(), f)).collect();
    let mut seen = HashSet::new();
    let mut changed = Vec::new();
    for f in &new.flat {
        seen.insert(f.id.as_str());
        match before.get(f.id.as_str()) {
            Some(o) if o.sig == f.sig => {}
            Some(o) => changed.extend([*o, f]),
            None => changed.push(f),
        }
    }
    changed.extend(old.flat.iter().filter(|o| !seen.contains(o.id.as_str())));
    // A rotated/flipped node paints outside its layout box: repaint the whole layer.
    if changed.iter().any(|f| f.reach.is_infinite()) {
        out.extend([old.paint, new.paint]);
        return;
    }
    out.extend(changed.into_iter().map(|f| (f.x - f.reach, f.y - f.reach, f.w + 2.0 * f.reach, f.h + 2.0 * f.reach)));
}

/// Generous paint reach outside a node's box from its effects and stroke.
fn reach(p: &Map<String, Value>) -> f64 {
    let num = |v: &Value| v.as_f64().unwrap_or(0.0).abs();
    let effects = match p.get("effect") {
        Some(Value::Array(a)) => a.iter().collect(),
        Some(v @ Value::Object(_)) => vec![v],
        _ => Vec::new(),
    };
    let fx = effects.iter().map(|e| num(&e["offset"]["x"]).max(num(&e["offset"]["y"])) + 3.0 * num(&e["blur"]).max(num(&e["radius"])) + num(&e["spread"])).fold(0.0, f64::max);
    let stroke = match p.get("strokeWidth") {
        Some(Value::Object(m)) => m.values().map(num).fold(0.0, f64::max),
        Some(v) => num(v),
        None => 0.0,
    };
    4.0 + fx + stroke
}

/// Copy a w×h window of `pix` into `out`, reusing its allocation.
fn crop_into(out: &mut Vec<u8>, pix: &tiny_skia::Pixmap, x: u32, y: u32, w: u32, h: u32) {
    let stride = pix.width() as usize * 4;
    out.clear();
    for row in y..y + h {
        let at = row as usize * stride + x as usize * 4;
        out.extend_from_slice(&pix.data()[at..at + w as usize * 4]);
    }
}

/// Repaint the doc-space rectangle `d` inside the cached view.
fn repaint(p: &mut Painter, v: &mut View, d: Rect) {
    let z = v.zoom;
    let x0 = ((d.0 * z).floor() as i32 - 2).max(v.x0);
    let y0 = ((d.1 * z).floor() as i32 - 2).max(v.y0);
    let x1 = (((d.0 + d.2) * z).ceil() as i32 + 2).min(v.x0 + v.pix.width() as i32);
    let y1 = (((d.1 + d.3) * z).ceil() as i32 + 2).min(v.y0 + v.pix.height() as i32);
    if x1 <= x0 || y1 <= y0 {
        return;
    }
    let (w, h) = ((x1 - x0) as u32, (y1 - y0) as u32);
    let mut part = vec![0; (w * h * 4) as usize];
    if !p.paint(v.solo.as_deref(), z, v.bg, x0, y0, w, h, &mut part) {
        return;
    }
    let (stride, pw) = (v.pix.width() as usize * 4, w as usize * 4);
    let (ox, oy) = ((x0 - v.x0) as usize, (y0 - v.y0) as usize);
    let data = v.pix.data_mut();
    for (row, src) in part.chunks_exact(pw).enumerate() {
        let at = (oy + row) * stride + ox * 4;
        data[at..at + pw].copy_from_slice(src);
    }
}

#[allow(clippy::too_many_arguments)]
fn paint_band(layers: &HashMap<String, Layer>, order: &[String], solo: Option<&str>, zoom: f64, bg: [u8; 3], x0: i32, y0: i32, w: u32, h: u32, raster: Raster, band: &mut raster::Band, out: &mut [u8]) {
    let view = (x0 as f64 / zoom, y0 as f64 / zoom, w as f64 / zoom, h as f64 / zoom);
    let visible = order.iter().filter(|id| solo.is_none_or(|s| s == id.as_str())).filter_map(|id| layers.get(id)).filter(|l| overlaps(l.paint, view));
    match raster {
        Raster::Vello => {
            let scenes: Vec<_> = visible.filter_map(|l| Some((l.scene.as_ref()?, l.tree.as_ref()?))).collect();
            raster::paint(band, &scenes, zoom, bg, x0, y0, w, h, out);
        }
        Raster::Resvg => {
            let Some(mut pm) = tiny_skia::PixmapMut::from_bytes(out, w, h) else { return };
            pm.fill(tiny_skia::Color::from_rgba8(bg[0], bg[1], bg[2], 255));
            let tf = tiny_skia::Transform::from_row(zoom as f32, 0.0, 0.0, zoom as f32, -x0 as f32, -y0 as f32);
            for tree in visible.filter_map(|l| l.tree.as_ref()) {
                if has_hairline(tree.root()) {
                    resvg::render(tree, tf, &mut pm);
                } else {
                    draw_culled(tree.root(), tf, view, &mut pm);
                }
            }
        }
    }
}

fn overlaps(a: Rect, b: Rect) -> bool {
    a.0 < b.0 + b.2 && b.0 < a.0 + a.2 && a.1 < b.1 + b.3 && b.1 < a.1 + a.3
}

/// Draw only the nodes that reach `view` (doc space). resvg gives every isolated group
/// (opacity, clip, filter) its own layer of up to 5x the target, even off-screen, so
/// plain groups are descended into and invisible subtrees are skipped entirely.
fn draw_culled(g: &usvg::Group, tf: tiny_skia::Transform, view: Rect, pm: &mut tiny_skia::PixmapMut) {
    let parent = tf.pre_concat(g.abs_transform());
    for node in g.children() {
        let Some(bb) = node.abs_layer_bounding_box() else { continue };
        let r = (bb.x() as f64, bb.y() as f64, bb.width() as f64, bb.height() as f64);
        if !overlaps(r, view) {
            continue;
        }
        if let usvg::Node::Group(sub) = node {
            let inside = r.0 >= view.0 && r.1 >= view.1 && r.0 + r.2 <= view.0 + view.2 && r.1 + r.3 <= view.1 + view.3;
            if !sub.should_isolate() && !inside && !has_hairline(sub) {
                draw_culled(sub, tf, view, pm);
                continue;
            }
        }
        // render_node shifts by the node's layer box (it targets a node-sized pixmap); undo that.
        resvg::render_node(node, parent.pre_translate(bb.x(), bb.y()), pm);
    }
}

/// A stroked horizontal/vertical path has no layer box of its own, so `render_node` cannot draw
/// it alone; its group is then drawn whole (the group's box includes the stroke).
fn has_hairline(g: &usvg::Group) -> bool {
    g.children().iter().any(|n| matches!(n, usvg::Node::Path(p) if p.stroke().is_some() && n.abs_layer_bounding_box().is_none()))
}

/// Lay out a top-level node and emit its SVG; the tree is parsed later (in parallel).
fn prepare_layer(cx: &mut Ctx, index: usize, hash: u64, hidden: &HashSet<String>) -> (Layer, Option<String>) {
    let Some(root) = cx.laid_out(index) else {
        return (Layer { hash, tree: None, scene: None, bbox: (0.0, 0.0, 0.0, 0.0), paint: (0.0, 0.0, 0.0, 0.0), flat: Vec::new() }, None);
    };
    let mut flat = Vec::new();
    let mut stack: Vec<String> = Vec::new();
    let mut index: Vec<usize> = Vec::new();
    // Per depth: this node or an ancestor is rotated or flipped.
    let mut turned: Vec<bool> = Vec::new();
    let mut mats: Vec<Option<[f64; 6]>> = Vec::new();
    root.visit(&mut |n, depth| {
        stack.truncate(depth);
        index.truncate(depth + 1);
        turned.truncate(depth);
        mats.truncate(depth);
        let rotated = n.f("rotation").is_some_and(|r| r.abs() > 1e-6) || n.b("flipX") || n.b("flipY");
        turned.push(rotated || turned.last() == Some(&true));
        let parent_m = mats.last().copied().flatten();
        mats.push(if rotated { Some(compose(parent_m, own_matrix(n))) } else { parent_m });
        if index.len() <= depth {
            index.push(0);
        } else {
            index[depth] += 1;
        }
        let mut sig = format!("{}|{}|{}|{}|{}|{:?}|{}", n.ty, n.x, n.y, n.w, n.h, stack.last(), index[depth]);
        for (k, v) in n.p.iter().filter(|(k, _)| *k != "children") {
            sig.push_str(k);
            sig.push_str(&v.to_string());
        }
        if hidden.contains(&n.id) {
            sig.push_str("|sliding");
        }
        flat.push(Flat {
            id: n.id.clone(),
            name: n.name().to_string(),
            ty: n.ty.clone(),
            x: n.x,
            y: n.y,
            w: n.w,
            h: n.h,
            depth,
            parent: stack.last().cloned(),
            problem: n.problem,
            placeholder: n.ty == "frame" && n.b("placeholder"),
            pending: crate::svg::fills(n.p.get("fill")).iter().any(|f| f["url"].as_str().is_some_and(is_pending_url)),
            m: mats[depth],
            sig: fxhash(&sig),
            reach: if turned[depth] { f64::INFINITY } else { reach(&n.p) },
        });
        stack.push(n.id.clone());
    }, 0);
    let pad = 64.0;
    let x0 = flat.iter().map(|f| f.x).fold(f64::MAX, f64::min) - pad;
    let y0 = flat.iter().map(|f| f.y).fold(f64::MAX, f64::min) - pad;
    let x1 = flat.iter().map(|f| f.x + f.w).fold(f64::MIN, f64::max) + pad;
    let y1 = flat.iter().map(|f| f.y + f.h).fold(f64::MIN, f64::max) + pad;
    let padded = (x0, y0, x1 - x0, y1 - y0);
    let bbox = if root.b("clip") && root.ty == "frame" { (root.x, root.y, root.w, root.h) } else { padded };
    let mut svg = Svg::new(cx.fonts, cx.assets, cx.doc.dir());
    svg.skip = hidden.clone();
    svg.node(&root);
    (Layer { hash, tree: None, scene: None, bbox, paint: padded, flat }, Some(svg.finish(None)))
}

/// A node's own rotation (CCW) and flips about its origin, as in `svg::transform`.
fn own_matrix(n: &RNode) -> [f64; 6] {
    let t = -n.f("rotation").unwrap_or(0.0).to_radians();
    let (sx, sy) = (if n.b("flipX") { -1.0 } else { 1.0 }, if n.b("flipY") { -1.0 } else { 1.0 });
    let (a, b, c, d) = (t.cos() * sx, t.sin() * sx, -t.sin() * sy, t.cos() * sy);
    let (px, py) = (n.x - n.bx, n.y - n.by);
    [a, b, c, d, px - a * px - c * py, py - b * px - d * py]
}

/// `p ∘ m` (m applies first).
fn compose(p: Option<[f64; 6]>, m: [f64; 6]) -> [f64; 6] {
    let Some(p) = p else { return m };
    [
        p[0] * m[0] + p[2] * m[1], p[1] * m[0] + p[3] * m[1],
        p[0] * m[2] + p[2] * m[3], p[1] * m[2] + p[3] * m[3],
        p[0] * m[4] + p[2] * m[5] + p[4], p[1] * m[4] + p[3] * m[5] + p[5],
    ]
}

/// Canvas point → box coordinates (inverse of `m`).
pub fn unapply(m: [f64; 6], x: f64, y: f64) -> (f64, f64) {
    let det = m[0] * m[3] - m[1] * m[2];
    let (x, y) = (x - m[4], y - m[5]);
    ((m[3] * x - m[2] * y) / det, (m[0] * y - m[1] * x) / det)
}

/// Topmost-first chain of nodes under a document point: [top-level, child, ..., deepest].
pub fn hit_chain(r: &Renderer, x: f64, y: f64) -> Vec<String> {
    for id in r.order.iter().rev() {
        let Some(layer) = r.layers.get(id) else { continue };
        let inside = |f: &Flat| {
            let (x, y) = f.m.map_or((x, y), |m| unapply(m, x, y));
            x >= f.x && y >= f.y && x <= f.x + f.w && y <= f.y + f.h
        };
        let Some(root) = layer.flat.first().filter(|f| inside(f)) else { continue };
        let mut chain = vec![root.id.clone()];
        // Deepest, last-painted descendant along each level.
        loop {
            let cur = chain.last().unwrap().clone();
            // A script node's generated children select the script node.
            if layer.flat.iter().any(|f| f.id == cur && f.ty == "script") {
                break;
            }
            let next = layer.flat.iter().rev().find(|f| f.parent.as_deref() == Some(&cur) && inside(f) && f.w > 0.0 && f.h > 0.0);
            match next {
                Some(f) => chain.push(f.id.clone()),
                None => break,
            }
        }
        return chain;
    }
    Vec::new()
}

/// Find a free spot of `w`×`h` next to an anchor node (or the whole document).
pub fn find_empty_space(r: &Renderer, w: f64, h: f64, dir: &str, pad: f64, anchor: Option<&Flat>) -> Value {
    let boxes: Vec<(f64, f64, f64, f64)> = r.order.iter().filter_map(|id| r.layers.get(id)).filter_map(|l| l.flat.first()).map(|f| (f.x, f.y, f.w, f.h)).collect();
    let overlaps = |x: f64, y: f64| boxes.iter().any(|b| x < b.0 + b.2 + pad && x + w + pad > b.0 && y < b.1 + b.3 + pad && y + h + pad > b.1);
    let (ax, ay, aw, ah) = anchor.map(|f| (f.x, f.y, f.w, f.h)).or_else(|| r.bounds().map(|b| (b.0 + 64.0, b.1 + 64.0, b.2 - 128.0, b.3 - 128.0))).unwrap_or((0.0, 0.0, 0.0, 0.0));
    let (mut x, mut y) = match dir {
        "left" => (ax - pad - w, ay),
        "top" => (ax, ay - pad - h),
        "bottom" => (ax, ay + ah + pad),
        _ => (ax + aw + pad, ay),
    };
    if boxes.is_empty() {
        return json!({"x": 0, "y": 0});
    }
    for _ in 0..500 {
        if !overlaps(x, y) {
            break;
        }
        match dir {
            "left" => x -= 40.0,
            "top" => y -= 40.0,
            "bottom" => y += 40.0,
            _ => x += 40.0,
        }
    }
    json!({"x": x.round(), "y": y.round()})
}

pub fn is_pending_url(u: &str) -> bool {
    u.starts_with("nalarupa:pending") || u.starts_with("pencil:pending")
}

/// Settings, chats, caches and exports. `NALARUPA_DATA` points a run elsewhere (screenshots, tests).
pub fn data_dir() -> PathBuf {
    if let Some(dir) = std::env::var_os("NALARUPA_DATA").filter(|d| !d.is_empty()) {
        return PathBuf::from(dir);
    }
    dirs::data_local_dir().unwrap_or_else(std::env::temp_dir).join("Nalarupa")
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    /// System fonts and assets without downloads.
    pub fn offline() -> (Fonts, Assets) {
        let dir = std::env::temp_dir().join("nalarupa-render-test");
        let mut fonts = Fonts::new(dir.join("fonts"));
        fonts.online = false;
        let mut assets = Assets::new(dir.join("cache"));
        assets.online = false;
        (fonts, assets)
    }

    pub fn sync(r: &mut Renderer, doc: &Doc) {
        let (mut fonts, mut assets) = offline();
        r.sync(&mut Ctx { doc, libs: &[], fonts: &mut fonts, assets: &mut assets });
    }

    /// Rotating a node repaints everything it painted before and after (no trails): the
    /// incremental frame equals a cold render.
    #[test]
    fn rotation_leaves_no_trails() {
        let json = r##"{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":2000,"height":2000,"fill":"#ffffff",
            "children":[{"type":"rectangle","id":"r","x":900,"y":900,"width":300,"height":60,"fill":"#ff0000"}]}]}"##;
        let mut doc = Doc::from_json(json).unwrap();
        let frame = |r: &mut Renderer| r.render(600, 400, 700.0, 700.0, 1.0, [240, 240, 240], None, true).to_vec();
        let mut live = Renderer::default();
        sync(&mut live, &doc);
        frame(&mut live);
        for deg in [30, 75, 0] {
            doc.root["children"][0]["children"][0]["rotation"] = serde_json::json!(deg);
            sync(&mut live, &doc);
            let incremental = frame(&mut live);
            let mut cold = Renderer::default();
            sync(&mut cold, &doc);
            assert!(incremental == frame(&mut cold), "stale pixels after rotating to {deg} degrees");
        }
    }

    /// Viewport culling, the cached view (512 px margin), resizes and dirty repaints never lose or
    /// misplace content: after a seeded walk of pans, resizes, zooms and edits (each preceded by a
    /// live frame, as the UI draws), every sharp frame matches a cold render of the same camera.
    #[test]
    fn cached_view_matches_cold_render() {
        let json = r##"{"version":"2.19","children":[
            {"type":"frame","id":"a","x":-1500,"y":-900,"width":900,"height":700,"fill":"#ffffff","layout":"none","clip":true,"children":[
                {"type":"rectangle","id":"a1","x":700,"y":500,"width":400,"height":400,"fill":"#ff0000"},
                {"type":"rectangle","id":"a2","x":50,"y":50,"width":200,"height":120,"fill":"#00aa00","cornerRadius":24,"strokeWidth":6,"stroke":"#000000"}]},
            {"type":"frame","id":"b","x":200,"y":-300,"width":600,"height":1600,"fill":"#eeeeff","layout":"none","children":[
                {"type":"rectangle","id":"b1","x":450,"y":1400,"width":400,"height":400,"fill":"#0000ff"},
                {"type":"ellipse","id":"b2","x":100,"y":200,"width":300,"height":300,"fill":"#ffaa00","rotation":30,
                 "effect":{"type":"shadow","blur":30,"offset":{"x":0,"y":12},"color":"#00000088"}}]},
            {"type":"frame","id":"c","x":3000,"y":2500,"width":400,"height":300,"fill":"#222222"}]}"##;
        let mut doc = Doc::from_json(json).unwrap();
        let mut seed = 0x2545F4914F6CDD1Du64;
        let mut rnd = |n: u64| {
            seed ^= seed << 13;
            seed ^= seed >> 7;
            seed ^= seed << 17;
            seed % n
        };
        let mut live = Renderer::default();
        sync(&mut live, &doc);
        let (mut w, mut h, mut cx, mut cy, mut zoom) = (800u32, 600u32, -1600.0f64, -1000.0f64, 0.5f64);
        for step in 0..150 {
            match rnd(6) {
                0 => (cx, cy) = (cx + (rnd(101) as f64 - 50.0) / zoom, cy + (rnd(101) as f64 - 50.0) / zoom),
                1 => (cx, cy) = (cx + (rnd(4001) as f64 - 2000.0) / zoom, cy + (rnd(4001) as f64 - 2000.0) / zoom),
                2 => (w, h) = (300 + rnd(900) as u32, 200 + rnd(800) as u32),
                3 => zoom = [0.13 * 1.25, 0.25, 0.5, 1.25, 2.5, 0.7][rnd(6) as usize],
                4 => {
                    let (x, y) = (rnd(800) as f64, rnd(1500) as f64);
                    doc.root["children"][1]["children"][0]["x"] = json!(x);
                    doc.root["children"][1]["children"][0]["y"] = json!(y);
                    sync(&mut live, &doc);
                }
                _ => (cx, cy) = (-1600.0 + rnd(5000) as f64, -1000.0 + rnd(4000) as f64),
            }
            live.render(w, h, cx, cy, zoom, [28, 28, 31], None, false);
            let got = live.render(w, h, cx, cy, zoom, [28, 28, 31], None, true).to_vec();
            // Oracle: a cold render of a much larger view on the same pixel grid, cropped. Culling a
            // node that paints into the small view would drop it here but not there.
            const PAD: u32 = 1500;
            let mut cold = Renderer::default();
            sync(&mut cold, &doc);
            let (bw, bh) = (w + 2 * PAD, h + 2 * PAD);
            // Same device grid: the view origin is round(cam·zoom), so start the big view PAD px before it.
            let (vx, vy) = ((cx * zoom).round() - PAD as f64, (cy * zoom).round() - PAD as f64);
            let big = cold.render(bw, bh, vx / zoom, vy / zoom, zoom, [28, 28, 31], None, true);
            let row = |y: u32| &big[(((y + PAD) * bw + PAD) * 4) as usize..(((y + PAD) * bw + PAD + w) * 4) as usize];
            let want: Vec<u8> = (0..h).flat_map(|y| row(y).iter().copied()).collect();
            let bad = got.chunks(4).zip(want.chunks(4)).filter(|(g, c)| (0..3).any(|i| g[i].abs_diff(c[i]) > 16)).count();
            assert!(bad == 0, "step {step}: {bad} pixels differ from a cold render ({w}x{h} cam {cx:.0},{cy:.0} zoom {zoom})");
        }
    }

    /// Boxes carry their painted transform, and hits land where rotated/flipped nodes are painted.
    #[test]
    fn rotated_boxes_hit_where_painted() {
        let json = r##"{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","rotation":90,
            "children":[{"type":"rectangle","id":"r","x":10,"y":20,"width":50,"height":30},
                        {"type":"rectangle","id":"k","x":300,"y":300,"width":40,"height":40,"flipX":true,"rotation":-90}]}]}"##;
        let doc = Doc::from_json(json).unwrap();
        let mut r = Renderer::default();
        sync(&mut r, &doc);
        // 90° CCW about the frame origin: (x, y) → (y, -x).
        let m = r.find("r").unwrap().m.unwrap();
        let (x, y) = (m[0] * 60.0 + m[2] * 50.0 + m[4], m[1] * 60.0 + m[3] * 50.0 + m[5]);
        assert!((x - 50.0).abs() < 1e-9 && (y + 60.0).abs() < 1e-9, "corner maps to ({x}, {y})");
        assert_eq!(hit_chain(&r, 35.0, -35.0), ["f", "r"]);
        assert!(hit_chain(&r, 35.0, 35.0).is_empty(), "the unrotated box is not hit");
        // Child turned back by -90° and mirrored about its origin (300, 300): local (u, v) → (300 - v, 300 - u) in frame
        // space, i.e. it paints at frame x 260..300, y 260..300 → canvas (y, -x).
        assert_eq!(hit_chain(&r, 280.0, -280.0), ["f", "k"]);
    }

    /// Horizontal/vertical stroked lines have no layer box; both rasterizers must still draw them
    /// in every band they cross.
    #[test]
    fn hairlines_survive_banding() {
        let json = r##"{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":800,"height":800,"layout":"none","fill":"#ffffff","children":[
            {"type":"path","id":"h","x":0,"y":400,"width":800,"height":0,"geometry":"M0 0 L800 0","stroke":"#000000","strokeWidth":2},
            {"type":"path","id":"v","x":400,"y":0,"width":0,"height":800,"geometry":"M0 0 L0 800","stroke":"#000000","strokeWidth":2}]}]}"##;
        let doc = Doc::from_json(json).unwrap();
        let mut missing = Vec::new();
        for raster in [Raster::Resvg, Raster::Vello] {
            let mut r = Renderer { raster, ..Renderer::default() };
            sync(&mut r, &doc);
            let px = r.render(800, 800, 0.0, 0.0, 1.0, [255, 255, 255], None, true).to_vec();
            let dark = |x: usize, y: usize| px[(y * 800 + x) * 4] < 128 || px[(y * 800 + x - 1) * 4] < 128;
            missing.extend((25..800).step_by(50).filter(|&y| !dark(400, y)).map(|y| format!("{raster:?} vertical y={y}")));
            missing.extend((25..800).step_by(50).filter(|&x| !dark(x, 400)).map(|x| format!("{raster:?} horizontal x={x}")));
        }
        assert!(missing.is_empty(), "hairline pixels missing: {missing:?}");
    }

    /// An agent-inserted node is left out of the cached view, painted 50 units low and
    /// transparent at its first frame, part way there 100 ms later, and back in the
    /// cached view (at its real place) once it settles.
    #[test]
    fn inserted_node_slides_in() {
        let json = r##"{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","fill":"#ffffff",
            "children":[{"type":"rectangle","id":"r","x":100,"y":100,"width":100,"height":50,"fill":"#ff0000"}]}]}"##;
        let doc = Doc::from_json(json).unwrap();
        let (mut fonts, mut assets) = offline();
        let mut r = Renderer::default();
        r.slides.push(Slide::new("r"));
        let mut sync = |r: &mut Renderer| {
            let mut cx = Ctx { doc: &doc, libs: &[], fonts: &mut fonts, assets: &mut assets };
            let roots = r.shift_siblings(&mut cx);
            let rebuilt = r.sync(&mut cx);
            r.refresh_slides(&mut cx, &rebuilt, roots);
        };
        sync(&mut r);
        let red = |px: &[u8], x: usize, y: usize| px[(y * 400 + x) * 4] > 200 && px[(y * 400 + x) * 4 + 1] < 200;
        let frame = |r: &mut Renderer| r.render(400, 400, 0.0, 0.0, 1.0, [30, 30, 30], None, true).to_vec();
        let px = frame(&mut r);
        assert!(!red(&px, 150, 125), "first frame: fully transparent");

        // 100 ms in: offset 50·e^-2 ≈ 6.8, opacity 1 - e^-0.5 ≈ 0.39.
        r.slides[0].start = Some(Instant::now() - Duration::from_millis(100));
        let px = frame(&mut r);
        let at = |x: usize, y: usize| px[(y * 400 + x) * 4 + 1];
        assert!(at(150, 103) > 245, "the top rows have moved down");
        assert!((120..200).contains(&at(150, 153)), "drawn lower and part transparent, got green {}", at(150, 153));

        r.slides[0].start = Some(Instant::now() - SLIDE_TIME);
        assert!(r.expire_slides() && r.slides.is_empty());
        sync(&mut r);
        let px = frame(&mut r);
        assert!(red(&px, 150, 101) && !red(&px, 150, 152), "settled in the cached view at its own place");
    }

    /// Inserting into a centered column moves the existing child up (auto-layout reflow); it
    /// glides there from where it was instead of jumping.
    #[test]
    fn reflowed_siblings_glide() {
        let col = |kids: &str| format!(r##"{{"version":"2.19","children":[{{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"vertical",
            "justifyContent":"center","fill":"#ffffff","children":[{kids}]}}]}}"##);
        let a = r##"{"type":"rectangle","id":"a","width":100,"height":100,"fill":"#ff0000"}"##;
        let before = Doc::from_json(&col(a)).unwrap();
        let after = Doc::from_json(&col(&format!(r##"{a},{{"type":"rectangle","id":"b","width":100,"height":100,"fill":"#0000ff"}}"##))).unwrap();
        let (mut fonts, mut assets) = offline();
        let mut r = Renderer::default();
        let mut sync = |r: &mut Renderer, doc: &Doc| {
            let mut cx = Ctx { doc, libs: &[], fonts: &mut fonts, assets: &mut assets };
            let roots = r.shift_siblings(&mut cx);
            let rebuilt = r.sync(&mut cx);
            r.refresh_slides(&mut cx, &rebuilt, roots);
        };
        sync(&mut r, &before);
        assert_eq!(r.find("a").map(|f| f.y), Some(150.0));
        r.slides.push(Slide::new("b"));
        sync(&mut r, &after);
        assert_eq!(r.find("a").map(|f| f.y), Some(100.0));
        let a = r.slides.iter().find(|s| s.id == "a").expect("the moved sibling glides");
        assert_eq!((a.from, a.fade), ((0.0, 50.0), false));
        // First frame: `a` still shows where it was (150..250), not at its new place (100..200).
        let px = r.render(400, 400, 0.0, 0.0, 1.0, [30, 30, 30], None, true).to_vec();
        let red = |x: usize, y: usize| px[(y * 400 + x) * 4] > 200 && px[(y * 400 + x) * 4 + 2] < 100;
        assert!(red(50, 240) && !red(50, 120), "the sibling starts at its old position");
    }
}
