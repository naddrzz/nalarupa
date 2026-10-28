//! Rasterizer: one cached resvg tree per top-level node, rebuilt only when that
//! node (or shared components/variables) changes. The canvas keeps one raster of
//! the viewport plus a margin: panning inside it is a copy, zooming
//! scales it until the UI asks for a sharp frame, and edits repaint only their
//! dirty rectangle. Painting culls per node and splits into bands across cores; each band is
//! rasterized by vello_cpu (`raster.rs`), or by resvg alone with `NALARUPA_RASTER=resvg`.

use crate::doc::Doc;
use crate::raster;
use crate::resolve::{RNode, Resolver};
use crate::svg::{clip_svg, fxhash, is_turned, own_matrix, Assets, Svg};
use crate::text::{Fonts, TextStyle};
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
/// Every slide is snapshotted on each sync (and checked against all the others), so a bulk
/// insert animates only its first few nodes; the rest appear in place.
pub const MAX_SLIDES: usize = 32;

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
    /// The node alone in absolute doc coordinates (under its ancestors' rotation, flips and
    /// opacity), without descendants that slide themselves.
    tree: Option<usvg::Tree>,
    /// Linear part of the ancestors' painted matrix: carries the offset into the parent's frame.
    lin: [f64; 4],
    /// Sliding ancestors: their offset and fade carry this node along.
    ancestors: Vec<String>,
    /// Clip shapes of the clipping ancestors, each with the ids of the nodes whose slide moves it
    /// (the ancestor and those above it). The snapshot sits at the settled spot and is moved, so
    /// the clips are applied when it is painted, not baked into it.
    clips: Vec<(usvg::Tree, Vec<String>)>,
    /// Raster of `tree` at (zoom, device rect).
    cache: Option<(f64, (i32, i32, u32, u32), tiny_skia::Pixmap)>,
}

impl Slide {
    pub fn new(id: &str) -> Slide {
        Slide { id: id.to_string(), added: Instant::now(), start: None, from: (0.0, SLIDE_OFFSET), fade: true, fresh: true, tree: None, lin: [1.0, 0.0, 0.0, 1.0], ancestors: Vec::new(), clips: Vec::new(), cache: None }
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
    /// Local bounds' offset from the origin (non-zero for groups) and the node's own effective rotation and flips.
    pub local: ([f64; 2], f64, bool, bool),
    /// Clips its children to its box (a `clip` frame), so they cannot be hit outside it.
    clip: bool,
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
        let mut opt = usvg::Options { fontdb: Arc::clone(&self.fonts.db), font_resolver: self.fonts.resolver(), image_href_resolver: crate::svg::image_href_resolver(), ..usvg::Options::default() };
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

    /// Why `id` cannot be exported: resolving drops hidden nodes, so a node that exists in the
    /// document but is absent from the laid-out tree is hidden itself or inside a hidden parent.
    /// An instance child `instanceId/childId` lives in the component, so each segment is looked up
    /// on its own.
    pub fn missing(&self, id: &str) -> String {
        if id.split('/').all(|seg| self.doc.find(seg).is_some()) {
            format!("{id} is hidden (or inside a hidden layer); show it before exporting")
        } else {
            format!("node not found: {id}")
        }
    }

    pub fn node_tree_at(&mut self, id: &str, scale: f64) -> Result<usvg::Tree, String> {
        let (svg, _) = self.node_svg_at(id, scale).ok_or_else(|| self.missing(id))?;
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

/// Whether `id` or one of its ancestors has an opacity below 1, with variables resolved for the
/// theme active there; one that does not resolve to a number counts as dimmed.
fn dimmed(doc: &Doc, id: &str) -> bool {
    fn path<'v>(nodes: &'v [Value], id: &str, chain: &mut Vec<&'v Value>) -> bool {
        nodes.iter().any(|n| {
            chain.push(n);
            let found = n["id"] == id || n["children"].as_array().is_some_and(|k| path(k, id, chain));
            if !found {
                chain.pop();
            }
            found
        })
    }
    let mut chain = Vec::new();
    path(doc.children(), id, &mut chain);
    crate::resolve::along(&doc.root, &chain, "opacity").iter().any(|o| !o.is_null() && crate::resolve::num(o).is_none_or(|o| o < 1.0))
}

pub fn find_rnode<'r>(n: &'r RNode, id: &str) -> Option<&'r RNode> {
    if n.id == id {
        return Some(n);
    }
    n.kids.iter().find_map(|k| find_rnode(k, id))
}

fn shared_hash(doc: &Doc, libs: &[(String, Value)], fonts: u64) -> u64 {
    // A downloaded font changes how text lays out.
    let mut s = fonts.to_string();
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
        // Instances resolve a library's variables in its own scope.
        s.push_str(&l.get("variables").map(Value::to_string).unwrap_or_default());
        s.push_str(&l.get("themes").map(Value::to_string).unwrap_or_default());
    }
    s.push_str(&doc.dir().map(|d| d.to_string_lossy().to_string()).unwrap_or_default());
    fxhash(&s)
}

impl Renderer {
    /// Rebuild layers whose content changed since the last sync; returns their ids.
    pub fn sync(&mut self, cx: &mut Ctx) -> Vec<String> {
        let shared = shared_hash(cx.doc, cx.libs, cx.fonts.generation);
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
                let room = self.slides.len() < MAX_SLIDES;
                match self.slides.iter_mut().find(|s| s.id == k.id) {
                    // Already gliding: continue from where it shows now.
                    Some(s) if !s.fade => {
                        let (cx0, cy0, _) = s.state(now);
                        (s.from, s.start, s.cache) = ((cx0 + dx, cy0 + dy), None, None);
                    }
                    Some(_) => {}
                    None if room => self.slides.push(Slide { from: (dx, dy), fade: false, fresh: false, ..Slide::new(&k.id) }),
                    None => {}
                }
            }
        }
        roots
    }

    /// Whether an insert into `parent` may start its own slide: there is room, `parent` and its
    /// ancestors are opaque (the settled layer composites a semi-transparent one with its children
    /// as a single group, which a snapshot painted over the parent's own paint cannot match, so
    /// those inserts appear in place), and no inserted node already sliding holds `parent` (its
    /// snapshot carries the new node along).
    pub fn can_slide_into(&self, doc: &Doc, parent: &str) -> bool {
        self.slides.len() < MAX_SLIDES
            && !dimmed(doc, parent)
            && !self.slides.iter().any(|s| s.fade && (s.id == parent || doc.find(&s.id).and_then(|n| n["children"].as_array()).is_some_and(|k| crate::doc::find_in(k, parent).is_some())))
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
            // The settled layer paints the node inside its ancestors' groups: carry their
            // rotation, flips and opacity (as `prepare_layer` and `Svg::node` do).
            let mut chain = Vec::new();
            ancestors_of(root, &s.id, &mut chain);
            let (m, alpha) = chain.iter().fold((None, 1.0), |(m, a), n| (if is_turned(n) { Some(compose(m, own_matrix(n))) } else { m }, a * n.f("opacity").unwrap_or(1.0).min(1.0)));
            s.lin = m.map_or([1.0, 0.0, 0.0, 1.0], |m: [f64; 6]| [m[0], m[1], m[2], m[3]]);
            // Each clipping ancestor cuts the node where it cuts it in the layer: its shape under
            // the matrix of the ancestors down to it.
            s.clips.clear();
            let mut turned = None;
            for (i, a) in chain.iter().enumerate() {
                if is_turned(a) {
                    turned = Some(compose(turned, own_matrix(a)));
                }
                if let Some(tree) = clip_svg(a, turned).and_then(|t| usvg::Tree::from_str(&t, &cx.tree_options()).ok()) {
                    s.clips.push((tree, chain[..=i].iter().map(|n| n.id.clone()).collect()));
                }
            }
            // An invisible ancestor hides the node, as it does in the layer.
            if alpha <= 0.0 {
                s.tree = None;
                continue;
            }
            let mut svg = Svg::new(cx.fonts, cx.assets, cx.doc.dir());
            svg.skip = ids.iter().filter(|i| **i != s.id).cloned().collect();
            svg.node(node);
            svg.wrap(m, alpha);
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
    /// With `solo` only the nodes inside that top-level frame paint; the cached view leaves
    /// every sliding node out, so skipping them would blank the presented slide.
    fn composite_slides(&mut self, w: u32, h: u32, cam_x: f64, cam_y: f64, zoom: f64, solo: Option<&str>) {
        let now = Instant::now();
        let shown: Option<HashSet<&str>> = solo.map(|id| self.layers.get(id).into_iter().flat_map(|l| l.flat.iter().map(|f| f.id.as_str())).collect());
        // A slide in another frame keeps waiting: its clock starts when it is first on screen.
        for s in &mut self.slides {
            if shown.as_ref().is_none_or(|ids| ids.contains(s.id.as_str())) {
                s.start.get_or_insert(now);
            }
        }
        // Offsets are in the parent's frame: turn them with its rotation and flips.
        let states: HashMap<String, (f64, f64, f64)> = self
            .slides
            .iter()
            .map(|s| {
                let (dx, dy, alpha) = s.state(now);
                (s.id.clone(), (s.lin[0] * dx + s.lin[2] * dy, s.lin[1] * dx + s.lin[3] * dy, alpha))
            })
            .collect();
        let (vx, vy) = ((cam_x * zoom).round() as i32, (cam_y * zoom).round() as i32);
        let Some(mut out) = tiny_skia::PixmapMut::from_bytes(&mut self.pixels, w, h) else { return };
        // Bottom frame first: a slide's cover pass repaints the frames above it, which hold
        // nothing of the slides in them, so a higher frame's slide must paint after it.
        let rank = |s: &Slide| self.order.iter().position(|id| self.layers.get(id).is_some_and(|l| l.flat.iter().any(|f| f.id == s.id)));
        self.slides.sort_by_key(rank);
        for s in &mut self.slides {
            let Some(tree) = &s.tree else { continue };
            if shown.as_ref().is_some_and(|ids| !ids.contains(s.id.as_str())) {
                continue;
            }
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
            let (rx, ry) = ((dx * zoom).round() as i32, (dy * zoom).round() as i32);
            let (px, py) = (x0 - vx + rx, y0 - vy + ry);
            // The clipping ancestors cut the moving node where they stand (sliding ones move theirs).
            let mut cut = (!s.clips.is_empty()).then(|| pm.clone());
            for (tree, owners) in &s.clips {
                let Some(cut) = cut.as_mut() else { break };
                let (cdx, cdy) = owners.iter().filter_map(|o| states.get(o)).fold((0.0, 0.0), |(x, y), st| (x + st.0, y + st.1));
                let Some(mut shape) = tiny_skia::Pixmap::new(cut.width(), cut.height()) else { continue };
                let tf = tiny_skia::Transform::from_row(zoom as f32, 0.0, 0.0, zoom as f32, (-x0 - rx) as f32 + (cdx * zoom).round() as f32, (-y0 - ry) as f32 + (cdy * zoom).round() as f32);
                resvg::render(tree, tf, &mut shape.as_mut());
                cut.apply_mask(&tiny_skia::Mask::from_pixmap(shape.as_ref(), tiny_skia::MaskType::Alpha));
            }
            let pm = cut.as_ref().unwrap_or(pm);
            out.draw_pixmap(px, py, pm.as_ref(), &paint, tiny_skia::Transform::identity(), None);
            // Top-level frames stacked above the slide's own cover it, as they do once it settles.
            let own = self.order.iter().position(|id| self.layers.get(id).is_some_and(|l| l.flat.iter().any(|f| f.id == s.id)));
            let above = own.map_or(&[][..], |i| &self.order[i + 1..]);
            let (cw, ch) = (pm.width() as i32, pm.height() as i32);
            let (cx0, cy0, cx1, cy1) = (px.max(0), py.max(0), (px + cw).min(w as i32), (py + ch).min(h as i32));
            if cx1 <= cx0 || cy1 <= cy0 {
                continue;
            }
            let cover = ((cx0 + vx) as f64 / zoom, (cy0 + vy) as f64 / zoom, (cx1 - cx0) as f64 / zoom, (cy1 - cy0) as f64 / zoom);
            let trees: Vec<&usvg::Tree> = above.iter().filter(|id| solo.is_none_or(|s| s == id.as_str())).filter_map(|id| self.layers.get(id)).filter(|l| overlaps(l.paint, cover)).filter_map(|l| l.tree.as_ref()).collect();
            if trees.is_empty() {
                continue;
            }
            let Some(mut over) = tiny_skia::Pixmap::new((cx1 - cx0) as u32, (cy1 - cy0) as u32) else { continue };
            let tf = tiny_skia::Transform::from_row(zoom as f32, 0.0, 0.0, zoom as f32, -(cx0 + vx) as f32, -(cy0 + vy) as f32);
            for tree in trees {
                if has_hairline(tree.root()) {
                    resvg::render(tree, tf, &mut over.as_mut());
                } else {
                    draw_culled(tree.root(), tf, cover, &mut over.as_mut());
                }
            }
            out.draw_pixmap(cx0, cy0, over.as_ref(), &tiny_skia::PixmapPaint::default(), tiny_skia::Transform::identity(), None);
        }
    }

    pub fn flat(&self) -> impl Iterator<Item = &Flat> {
        self.order.iter().filter_map(|id| self.layers.get(id)).flat_map(|l| l.flat.iter())
    }

    /// Nodes that need animated overlays (placeholder frames, pending images), each with the canvas box its
    /// clipping ancestors leave visible (`None` when nothing clips it). A node clipped away entirely is left out.
    pub fn live(&self) -> Vec<(&Flat, Option<Rect>)> {
        let canvas = |f: &Flat| {
            let m = f.m.unwrap_or([1.0, 0.0, 0.0, 1.0, 0.0, 0.0]);
            let pts = [(f.x, f.y), (f.x + f.w, f.y), (f.x, f.y + f.h), (f.x + f.w, f.y + f.h)].map(|(x, y)| (m[0] * x + m[2] * y + m[4], m[1] * x + m[3] * y + m[5]));
            let lo = |g: fn(&(f64, f64)) -> f64| pts.iter().map(g).fold(f64::MAX, f64::min);
            let hi = |g: fn(&(f64, f64)) -> f64| pts.iter().map(g).fold(f64::MIN, f64::max);
            (lo(|p| p.0), lo(|p| p.1), hi(|p| p.0) - lo(|p| p.0), hi(|p| p.1) - lo(|p| p.1))
        };
        let mut out = Vec::new();
        for layer in self.order.iter().filter_map(|id| self.layers.get(id)) {
            let mut path: Vec<usize> = Vec::new();
            for (i, f) in layer.flat.iter().enumerate() {
                path.truncate(f.depth);
                if f.placeholder || f.pending {
                    // A rotated clipping frame is cut by its canvas bounding box (its corners may still show).
                    let clip = path.iter().map(|&p| &layer.flat[p]).filter(|p| p.clip).map(canvas).reduce(|a, b| {
                        let (x0, y0, x1, y1) = (a.0.max(b.0), a.1.max(b.1), (a.0 + a.2).min(b.0 + b.2), (a.1 + a.3).min(b.1 + b.3));
                        (x0, y0, (x1 - x0).max(0.0), (y1 - y0).max(0.0))
                    });
                    let own = canvas(f);
                    let seen = clip.is_none_or(|c| c.2 > 0.0 && c.3 > 0.0 && c.0 < own.0 + own.2 && c.0 + c.2 > own.0 && c.1 < own.1 + own.3 && c.1 + c.3 > own.1);
                    if seen {
                        out.push((f, clip));
                    }
                }
                path.push(i);
            }
        }
        out
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
        if !self.slides.is_empty() && self.pixels.len() == (w * h * 4) as usize {
            self.composite_slides(w, h, cam_x, cam_y, zoom, solo);
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
    // A clip frame that changed or went away cuts (or stops cutting) its descendants, and a node
    // moved under another parent changes which clips cut its own: their signatures stay the same,
    // so repaint wherever they reach.
    let mut roots: HashSet<&str> = changed.iter().filter(|f| f.clip).map(|f| f.id.as_str()).collect();
    roots.extend(new.flat.iter().filter(|f| before.get(f.id.as_str()).is_some_and(|o| o.parent != f.parent)).map(|f| f.id.as_str()));
    for layer in [old, new] {
        for (i, f) in layer.flat.iter().enumerate() {
            if roots.contains(f.id.as_str()) {
                changed.extend(layer.flat[i + 1..].iter().take_while(|d| d.depth > f.depth));
            }
        }
    }
    // A rotated/flipped node paints outside its layout box: repaint the whole layer.
    if changed.iter().any(|f| f.reach.is_infinite()) {
        out.extend([old.paint, new.paint]);
        return;
    }
    out.extend(changed.into_iter().map(|f| (f.x - f.reach, f.y - f.reach, f.w + 2.0 * f.reach, f.h + 2.0 * f.reach)));
}

/// Generous paint reach outside a node's box from its effects, stroke and text overflow.
fn reach(n: &RNode) -> f64 {
    let p = &n.p;
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
    // Glyphs spill out of a line box shorter than the font's ascent plus descent (tight line heights,
    // overhangs): one em each way. A fixed-size box also lets its lines run past it: lines below the
    // box and words wider than it.
    let glyphs = if n.ty == "text" {
        let st = TextStyle::of(n);
        let lines = n.lines.len() as f64 * st.size * st.line_height.unwrap_or(0.0).max(2.0);
        let wide = n.lines.iter().map(|l| l.width).fold(0.0, f64::max);
        st.size + (lines - n.h).max(wide - n.w).max(0.0)
    } else {
        0.0
    };
    4.0 + fx + stroke + glyphs
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
    // Per depth: this node or an ancestor is hidden while it slides.
    let mut sliding: Vec<bool> = Vec::new();
    root.visit(&mut |n, depth| {
        stack.truncate(depth);
        index.truncate(depth + 1);
        turned.truncate(depth);
        mats.truncate(depth);
        sliding.truncate(depth);
        let rotated = is_turned(n);
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
        // The whole subtree: when the slide ends, descendants that overflow the node repaint too.
        sliding.push(hidden.contains(&n.id) || sliding.last() == Some(&true));
        if sliding[depth] {
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
            local: ([n.bx, n.by], n.f("rotation").unwrap_or(0.0), n.b("flipX"), n.b("flipY")),
            clip: n.ty == "frame" && n.b("clip"),
            sig: fxhash(&sig),
            reach: if turned[depth] { f64::INFINITY } else { reach(n) },
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
/// The deepest node is the last-painted one whose box holds the point; a child can be hit outside
/// its parent's box unless an ancestor clips it. A group's union box is not a hit surface, so only its
/// children can be hit: a point inside its box but on none of them falls through to the layers below.
pub fn hit_chain(r: &Renderer, x: f64, y: f64) -> Vec<String> {
    for id in r.order.iter().rev() {
        let Some(layer) = r.layers.get(id) else { continue };
        let inside = |f: &Flat| {
            let (x, y) = f.m.map_or((x, y), |m| unapply(m, x, y));
            x >= f.x && y >= f.y && x <= f.x + f.w && y <= f.y + f.h
        };
        // Per node, in paint order: is it reachable (no clipping ancestor misses the point), and is it
        // under a script node (whose generated children select the script node).
        let mut path: Vec<(usize, bool, bool)> = Vec::new(); // (index, reachable, below a script)
        let mut hit = None;
        for (i, f) in layer.flat.iter().enumerate() {
            path.truncate(f.depth);
            let (open, scripted) = path.last().map_or((true, false), |&(p, open, scripted)| {
                let p = &layer.flat[p];
                (open && (!p.clip || inside(p)), scripted || p.ty == "script")
            });
            path.push((i, open, scripted));
            if open && !scripted && f.ty != "group" && (f.depth == 0 || f.w > 0.0 && f.h > 0.0) && inside(f) {
                hit = Some(i);
            }
        }
        let Some(mut i) = hit else { continue };
        let mut chain = vec![layer.flat[i].id.clone()];
        while let Some(p) = layer.flat[i].parent.as_deref() {
            i = layer.flat[..i].iter().rposition(|f| f.id == p).unwrap();
            chain.push(p.to_string());
        }
        chain.reverse();
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

    /// Glyphs of a tight-line-height text stick out of its layout box: editing or deleting it
    /// repaints them too, so the incremental frame equals a cold render (no descender tails left).
    #[test]
    fn text_overflow_leaves_no_ghosts() {
        let json = |lh: f64, box_: &str, size: &str| {
            format!(
                r##"{{"version":"2.19","children":[{{"type":"frame","id":"f","x":0,"y":0,"width":1920,"height":1080,"layout":"none","fill":"#0f1115","children":[
                {{"type":"text","id":"t","x":160,"y":587,"width":1600,{box_},"content":"Say one thing,\nbeautifully.","fontSize":{size},"fontWeight":"700","lineHeight":{lh},"fill":"#f4f1ea"}}]}}]}}"##
            )
        };
        let frame = |r: &mut Renderer| r.render(640, 400, 160.0, 700.0, 1.0, [28, 28, 31], None, true).to_vec();
        let edits: [(&str, fn(&mut Value)); 4] = [
            ("delete", |t| *t = Value::Null),
            ("recolor", |t| t["fill"] = json!("#ff0000")),
            ("move", |t| t["y"] = json!(287)),
            ("hide", |t| t["enabled"] = json!(false)),
        ];
        // Tight line heights, a string font size, and a fixed-height box its lines run out of.
        let cases = [(1.05, r#""textGrowth":"fixed-width""#, "120"), (0.8, r#""textGrowth":"fixed-width""#, "120"), (1.05, r#""textGrowth":"fixed-width""#, r#""120""#), (1.05, r#""textGrowth":"fixed-width-height","height":40"#, "120")];
        for (lh, box_, size) in cases {
            for (name, edit) in edits {
                let mut doc = Doc::from_json(&json(lh, box_, size)).unwrap();
                let mut live = Renderer::default();
                sync(&mut live, &doc);
                frame(&mut live);
                edit(&mut doc.root["children"][0]["children"][0]);
                if doc.root["children"][0]["children"][0].is_null() {
                    doc.root["children"][0]["children"] = json!([]);
                }
                sync(&mut live, &doc);
                let incremental = frame(&mut live);
                let mut cold = Renderer::default();
                sync(&mut cold, &doc);
                // Banded rasterising may round a channel by one; a ghost is far off.
                let stale = incremental.iter().zip(frame(&mut cold)).filter(|(a, b)| a.abs_diff(*b) > 10).count();
                assert!(stale == 0, "{stale} stale glyph channels after {name} at lineHeight {lh}, {box_}, fontSize {size}");
            }
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

    /// A child painted outside its unclipped parent's box is hit there; a clipping ancestor still hides it.
    #[test]
    fn overflow_hits_unless_clipped() {
        let json = r##"{"version":"2.19","children":[
            {"type":"frame","id":"f","x":100,"y":100,"width":100,"height":100,"layout":"none","children":[
                {"type":"rectangle","id":"o","x":80,"y":80,"width":100,"height":100}]},
            {"type":"frame","id":"t","x":400,"y":0,"width":200,"height":200,"layout":"none","children":[
                {"type":"frame","id":"i","x":0,"y":0,"width":50,"height":50,"layout":"none","children":[
                    {"type":"rectangle","id":"n","x":40,"y":40,"width":100,"height":100}]}]},
            {"type":"frame","id":"c","x":800,"y":0,"width":100,"height":100,"layout":"none","clip":true,"children":[
                {"type":"rectangle","id":"h","x":80,"y":80,"width":100,"height":100}]}]}"##;
        let doc = Doc::from_json(json).unwrap();
        let mut r = Renderer::default();
        sync(&mut r, &doc);
        assert_eq!(hit_chain(&r, 190.0, 190.0), ["f", "o"]);
        assert_eq!(hit_chain(&r, 250.0, 250.0), ["f", "o"], "outside the frame's box");
        assert!(hit_chain(&r, 285.0, 285.0).is_empty());
        assert_eq!(hit_chain(&r, 500.0, 100.0), ["t", "i", "n"], "outside the inner frame, inside the outer one");
        assert_eq!(hit_chain(&r, 860.0, 50.0), ["c"]);
        assert_eq!(hit_chain(&r, 880.0, 80.0), ["c", "h"]);
        assert!(hit_chain(&r, 950.0, 150.0).is_empty(), "clipped overflow is invisible");
    }

    /// A pending image or placeholder inside a clipping frame is cut to the frame's box; one clipped away is not live.
    #[test]
    fn live_overlays_follow_clipping_ancestors() {
        let pending = r##"{"type":"image","url":"nalarupa:pending-x"}"##;
        let json = format!(r##"{{"version":"2.19","children":[
            {{"type":"frame","id":"c","x":100,"y":100,"width":300,"height":200,"layout":"none","clip":true,"children":[
                {{"type":"rectangle","id":"img","x":150,"y":50,"width":400,"height":300,"fill":{pending}}},
                {{"type":"rectangle","id":"gone","x":500,"y":10,"width":50,"height":50,"fill":{pending}}},
                {{"type":"frame","id":"far","x":350,"y":0,"width":100,"height":100,"layout":"none","clip":true,"children":[
                    {{"type":"rectangle","id":"apart","x":-50,"y":10,"width":100,"height":50,"fill":{pending}}}]}}]}},
            {{"type":"frame","id":"open","x":600,"y":0,"width":100,"height":100,"layout":"none","children":[
                {{"type":"rectangle","id":"free","x":80,"y":80,"width":100,"height":100,"fill":{pending}}}]}}]}}"##);
        let doc = Doc::from_json(&json).unwrap();
        let mut r = Renderer::default();
        sync(&mut r, &doc);
        let live: Vec<_> = r.live().into_iter().map(|(f, c)| (f.id.as_str(), c)).collect();
        // `img` sits at doc (250, 150) and sticks out of the frame (x 100..400, y 100..300).
        assert_eq!(live, [("img", Some((100.0, 100.0, 300.0, 200.0))), ("free", None)]);
    }

    /// A group paints nothing of its own: a point in its box but on none of its children hits the layer below.
    #[test]
    fn group_box_does_not_swallow_hits() {
        let json = r##"{"version":"2.19","children":[
            {"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","children":[
                {"type":"rectangle","id":"u","x":100,"y":100,"width":150,"height":150},
                {"type":"group","id":"g","children":[
                    {"type":"rectangle","id":"a","x":20,"y":20,"width":40,"height":40},
                    {"type":"rectangle","id":"b","x":300,"y":300,"width":40,"height":40}]}]},
            {"type":"group","id":"t","children":[
                {"type":"rectangle","id":"c","x":500,"y":0,"width":20,"height":20},
                {"type":"rectangle","id":"d","x":700,"y":200,"width":20,"height":20}]}]}"##;
        let doc = Doc::from_json(json).unwrap();
        let mut r = Renderer::default();
        sync(&mut r, &doc);
        assert_eq!(hit_chain(&r, 40.0, 40.0), ["f", "g", "a"]);
        assert_eq!(hit_chain(&r, 150.0, 150.0), ["f", "u"], "inside the group's box, on none of its children");
        assert_eq!(hit_chain(&r, 320.0, 320.0), ["f", "g", "b"]);
        assert_eq!(hit_chain(&r, 510.0, 10.0), ["t", "c"]);
        assert!(hit_chain(&r, 600.0, 100.0).is_empty(), "inside a top-level group's box only");
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

    /// Text takes a stroke like a shape: stroke-only text is visible, and a stroke paints around filled glyphs.
    #[test]
    fn text_stroke_is_painted() {
        let text = |x: u32, extra: &str| format!(r##"{{"type":"text","x":{x},"y":10,"content":"Out","fontSize":50,"fontWeight":"700"{extra}}}"##);
        let json = format!(
            r##"{{"version":"2.19","children":[{{"type":"frame","id":"f","x":0,"y":0,"width":580,"height":80,"layout":"none","fill":"#ffffff","children":[{},{},{},{},{}]}}]}}"##,
            text(10, r##","fill":"#ffff00""##),
            text(150, r##","fill":"#ffff00","stroke":"#000000","strokeWidth":4"##),
            text(290, r##","stroke":"#000000","strokeWidth":4"##),
            text(430, r##","fill":"#ffff00","stroke":"#000000","strokeWidth":4,"strokeAlignment":"outer""##),
            text(570, r##","fill":"#ffff00","stroke":"#000000","strokeWidth":4,"strokeAlignment":"inner""##),
        );
        let doc = Doc::from_json(&json).unwrap();
        for raster in [Raster::Resvg, Raster::Vello] {
            let mut r = Renderer { raster, ..Renderer::default() };
            sync(&mut r, &doc);
            let px = r.render(720, 80, 0.0, 0.0, 1.0, [255, 255, 255], None, true).to_vec();
            let dark = |x0: usize, x1: usize| (0..80).flat_map(|y| (x0..x1).map(move |x| (x, y))).filter(|&(x, y)| px[(y * 720 + x) * 4] < 80).count();
            let counts = [dark(0, 140), dark(140, 280), dark(280, 420), dark(420, 560), dark(560, 720)];
            assert_eq!(counts[0], 0, "{raster:?}: fill-only text has no dark pixels");
            assert!(counts[1] > 100 && counts[2] > 100 && counts[3] > 100 && counts[4] > 100, "{raster:?}: stroke pixels {counts:?}");
            // An outer stroke grows past the glyphs, an inner one stays inside them.
            assert!(counts[3] > counts[1] && counts[4] < counts[1], "{raster:?}: outer > center > inner expected, got {counts:?}");
        }
    }

    /// A text shadow follows what paints: stroke-only text casts its stroke, not solid glyphs.
    #[test]
    fn stroke_only_text_casts_a_hollow_shadow() {
        let text = |y: u32, extra: &str| {
            format!(r##"{{"type":"text","x":10,"y":{y},"content":"Out","fontSize":50,"fontWeight":"700","stroke":"#000000","strokeWidth":2,"effect":{{"type":"shadow","offset":{{"x":150,"y":0}},"color":"#ff0000"}}{extra}}}"##)
        };
        let json = format!(
            r##"{{"version":"2.19","children":[{{"type":"frame","id":"f","x":0,"y":0,"width":300,"height":180,"layout":"none","fill":"#ffffff","children":[{},{}]}}]}}"##,
            text(10, r##","fill":"#ffff00""##),
            text(100, ""),
        );
        let doc = Doc::from_json(&json).unwrap();
        for raster in [Raster::Resvg, Raster::Vello] {
            let mut r = Renderer { raster, ..Renderer::default() };
            sync(&mut r, &doc);
            let px = r.render(300, 180, 0.0, 0.0, 1.0, [255, 255, 255], None, true).to_vec();
            let red = |y0: usize, y1: usize| (y0..y1).flat_map(|y| (150..300).map(move |x| (y * 300 + x) * 4)).filter(|&o| px[o] > 200 && px[o + 1] < 80).count();
            let (solid, hollow) = (red(0, 95), red(95, 180));
            assert!(hollow > 100 && hollow * 3 < solid * 2, "{raster:?}: shadow pixels filled {solid}, stroke-only {hollow}");
        }
    }

    /// A shadow offset lives in the node's own axes: it turns with a rotation and mirrors with a flip,
    /// in the resvg raster too (it used to stay on canvas axes and vanished at 90°).
    #[test]
    fn shadow_offset_follows_rotation_and_flips() {
        let shadow = r##""effect":{"type":"shadow","offset":{"x":30,"y":0},"blur":0,"color":"#ff0000"}"##;
        let json = format!(
            r##"{{"version":"2.19","children":[{{"type":"frame","id":"f","x":0,"y":0,"width":200,"height":200,"layout":"none","fill":"#ffffff","clip":true,"children":[
                {{"type":"rectangle","x":80,"y":120,"width":40,"height":20,"rotation":90,"fill":"#0000ff",{shadow}}}]}},
              {{"type":"frame","id":"g","x":0,"y":300,"width":200,"height":200,"layout":"none","fill":"#ffffff","clip":true,"children":[
                {{"type":"rectangle","x":100,"y":50,"width":40,"height":40,"flipX":true,"fill":"#0000ff",{shadow}}}]}}]}}"##
        );
        let doc = Doc::from_json(&json).unwrap();
        for raster in [Raster::Resvg, Raster::Vello] {
            let mut r = Renderer { raster, ..Renderer::default() };
            sync(&mut r, &doc);
            let px = r.render(200, 500, 0.0, 0.0, 1.0, [255, 255, 255], None, true).to_vec();
            let red = |x0: usize, x1: usize, y0: usize, y1: usize| {
                (y0..y1).flat_map(|y| (x0..x1).map(move |x| (y * 200 + x) * 4)).filter(|&o| px[o] > 200 && px[o + 1] < 60 && px[o + 2] < 60).count()
            };
            // Turned 90° the rectangle covers x 80..100, y 80..120 and the shadow lands 30 above it.
            assert_eq!(red(80, 100, 50, 80), 600, "{raster:?}: rotated shadow");
            assert_eq!(red(0, 200, 0, 200), 600, "{raster:?}: rotated shadow stays in one place");
            // Mirrored it covers x 60..100 (flipping runs about its right edge), so the shadow shows at x 30..60.
            assert_eq!(red(30, 60, 350, 390), 1200, "{raster:?}: flipped shadow lands left of the rectangle");
            assert_eq!(red(0, 200, 300, 500), 1200, "{raster:?}: flipped shadow stays in one place");
        }
    }

    /// A group's shadow follows what its children paint, strokes included: the same silhouette as
    /// the shadow on the stroked rectangle itself, in both rasters.
    #[test]
    fn group_shadow_includes_children_strokes() {
        let rect = r##""type":"rectangle","x":40,"y":40,"width":70,"height":70,"fill":"#e03030","stroke":"#102030","strokeWidth":10"##;
        let shadow = r##""effect":{"type":"shadow","offset":{"x":12,"y":12},"blur":0,"color":"#206020"}"##;
        let frame = |y: u32, node: String| {
            format!(r##"{{"type":"frame","id":"f{y}","x":0,"y":{y},"width":200,"height":200,"layout":"none","fill":"#ffffff","children":[{node}]}}"##)
        };
        let json = format!(
            r##"{{"version":"2.19","children":[{},{}]}}"##,
            frame(0, format!(r##"{{"type":"group",{shadow},"children":[{{{rect}}}]}}"##)),
            frame(300, format!(r##"{{{rect},{shadow}}}"##)),
        );
        let doc = Doc::from_json(&json).unwrap();
        for raster in [Raster::Resvg, Raster::Vello] {
            let mut r = Renderer { raster, ..Renderer::default() };
            sync(&mut r, &doc);
            let px = r.render(200, 500, 0.0, 0.0, 1.0, [255, 255, 255], None, true).to_vec();
            // Shadow-coloured pixels on a row below the stroke's outer edge (115), inside the shadow (47..127).
            let row = |y: usize| (0..200).filter(|&x| px[(y * 200 + x) * 4..][..3] == [0x20, 0x60, 0x20]).map(|x| x as i32).collect::<Vec<_>>();
            let (group, own) = (row(118), row(418));
            assert_eq!((group.first(), group.last()), (Some(&47), Some(&126)), "{raster:?}: group shadow spans the stroke on every side");
            assert_eq!(group, own, "{raster:?}: group shadow matches the stroked rectangle's own");
        }
    }

    /// An inner shadow is measured from the fill edge whatever the stroke alignment: the band stays
    /// on the fill (x 50..61) and nothing is painted outside the shape, in both rasters.
    #[test]
    fn inner_shadow_ignores_stroke_alignment() {
        for align in ["inner", "center", "outer"] {
            let json = format!(
                r##"{{"version":"2.19","children":[{{"type":"frame","id":"f","x":0,"y":0,"width":200,"height":200,"layout":"none","fill":"#ffffff","children":[{{"type":"rectangle","id":"r","x":50,"y":40,"width":100,"height":100,"fill":"#0000ff","stroke":"#00000000","strokeWidth":8,"strokeAlignment":"{align}","effect":{{"type":"shadow","shadowType":"inner","offset":{{"x":12,"y":0}},"blur":0,"color":"#ff0000"}}}}]}}]}}"##
            );
            let doc = Doc::from_json(&json).unwrap();
            for raster in [Raster::Resvg, Raster::Vello] {
                let mut r = Renderer { raster, ..Renderer::default() };
                sync(&mut r, &doc);
                let px = r.render(200, 200, 0.0, 0.0, 1.0, [255, 255, 255], None, true).to_vec();
                let red: Vec<usize> = (0..200).filter(|&x| { let p = &px[(90 * 200 + x) * 4..]; p[0] > 200 && p[1] < 60 && p[2] < 60 }).collect();
                assert_eq!((red.first(), red.last()), (Some(&50), Some(&61)), "{align} {raster:?}: inner shadow band");
            }
        }
    }

    /// Text paints like a shape: fill, then inner shadows, then the stroke, so the stroke covers an inner shadow.
    #[test]
    fn text_stroke_covers_inner_shadow() {
        let text = |x: u32, extra: &str| {
            format!(r##"{{"type":"text","x":{x},"y":10,"content":"Out","fontSize":50,"fontWeight":"700","fill":"#ffff00","stroke":"#000000","strokeWidth":4{extra}}}"##)
        };
        let json = format!(
            r##"{{"version":"2.19","children":[{{"type":"frame","id":"f","x":0,"y":0,"width":300,"height":80,"layout":"none","fill":"#ffffff","children":[{},{}]}}]}}"##,
            text(10, ""),
            text(150, r##","effect":{"type":"shadow","shadowType":"inner","offset":{"x":8,"y":8},"color":"#ff0000"}"##),
        );
        let doc = Doc::from_json(&json).unwrap();
        for raster in [Raster::Resvg, Raster::Vello] {
            let mut r = Renderer { raster, ..Renderer::default() };
            sync(&mut r, &doc);
            let px = r.render(300, 80, 0.0, 0.0, 1.0, [255, 255, 255], None, true).to_vec();
            let count = |x0: usize, keep: &dyn Fn(&[u8]) -> bool| (0..80).flat_map(|y| (x0..x0 + 140).map(move |x| (y * 300 + x) * 4)).filter(|&o| keep(&px[o..o + 3])).count();
            // White, yellow and red all have full red, so only the stroke darkens the red channel.
            let (plain, shadowed) = (count(0, &|p| p[0] < 80), count(140, &|p| p[0] < 80));
            let red = count(140, &|p| p[0] > 200 && p[1] < 80);
            assert!(red > 50, "{raster:?}: the inner shadow shows inside the fill, got {red} red pixels");
            assert!(plain > 100 && shadowed + plain / 50 >= plain, "{raster:?}: stroke pixels without shadow {plain}, with inner shadow {shadowed}");
        }
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

    /// A settled slide repaints the node's descendants, also where they overflow its box.
    #[test]
    fn settled_slide_repaints_overflowing_children() {
        let before = Doc::from_json(r##"{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":500,"height":400,"layout":"none","fill":"#ffffff"}]}"##).unwrap();
        let after = Doc::from_json(
            r##"{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":500,"height":400,"layout":"none","fill":"#ffffff","children":[
            {"type":"frame","id":"c","x":100,"y":100,"width":100,"height":100,"layout":"none","fill":"#334455","children":[
                {"type":"rectangle","id":"b","x":80,"y":20,"width":100,"height":50,"fill":"#22ff55"}]}]}]}"##,
        )
        .unwrap();
        let (mut fonts, mut assets) = offline();
        let mut r = Renderer::default();
        let mut sync = |r: &mut Renderer, doc: &Doc| {
            let mut cx = Ctx { doc, libs: &[], fonts: &mut fonts, assets: &mut assets };
            let roots = r.shift_siblings(&mut cx);
            let rebuilt = r.sync(&mut cx);
            r.refresh_slides(&mut cx, &rebuilt, roots);
        };
        let frame = |r: &mut Renderer| r.render(500, 400, 0.0, 0.0, 1.0, [30, 30, 30], None, true).to_vec();
        sync(&mut r, &before);
        frame(&mut r);
        r.slides.push(Slide::new("c"));
        sync(&mut r, &after);
        frame(&mut r);
        r.slides[0].start = Some(Instant::now() - SLIDE_TIME);
        assert!(r.expire_slides());
        sync(&mut r, &after);
        let px = frame(&mut r);
        // The badge spans x 180..280, y 120..170: the part past the card's right edge (200) is the overflow.
        let i = (150 * 500 + 250) * 4;
        assert_eq!(px[i..i + 3], [0x22, 0xff, 0x55], "the overflowing part of the badge is back");
    }

    /// A sliding node stays in its frame's place in the stacking order: a top-level frame above
    /// it covers it mid-slide as it does once settled, and one below it does not.
    #[test]
    fn slide_stays_under_frames_above_its_own() {
        let json = r##"{"version":"2.19","children":[
            {"type":"frame","id":"p","x":100,"y":100,"width":300,"height":200,"layout":"none","fill":"#2255ff","children":[
                {"type":"rectangle","id":"k","x":20,"y":20,"width":100,"height":100,"fill":"#ff0000"}]},
            {"type":"frame","id":"q","x":150,"y":100,"width":300,"height":300,"layout":"none","fill":"#ffff00"}]}"##;
        let doc = Doc::from_json(json).unwrap();
        let (mut fonts, mut assets) = offline();
        let mut r = Renderer::default();
        r.slides.push(Slide::new("k"));
        let mut cx = Ctx { doc: &doc, libs: &[], fonts: &mut fonts, assets: &mut assets };
        let roots = r.shift_siblings(&mut cx);
        let rebuilt = r.sync(&mut cx);
        r.refresh_slides(&mut cx, &rebuilt, roots);
        let at = |px: &[u8], x: usize, y: usize| px[(y * 500 + x) * 4..(y * 500 + x) * 4 + 3].to_vec();
        r.render(500, 500, 0.0, 0.0, 1.0, [30, 30, 30], None, true);
        r.slides[0].start = Some(Instant::now() - Duration::from_millis(100));
        let px = r.render(500, 500, 0.0, 0.0, 1.0, [30, 30, 30], None, true).to_vec();
        // k sits at 120..220 x 120..220 (6.8 low mid-slide); q covers x ≥ 150.
        assert_eq!(at(&px, 180, 150), [255, 255, 0], "the frame above covers the sliding node");
        assert!(at(&px, 130, 150)[0] > 100, "the part outside that frame still shows the sliding node");
    }

    /// Two slides in different frames: the lower one's cover pass must not erase the higher
    /// one, whichever was added first.
    #[test]
    fn slide_in_higher_frame_survives_lower_covers() {
        let json = r##"{"version":"2.19","children":[
            {"type":"frame","id":"p","x":0,"y":0,"width":300,"height":300,"layout":"none","fill":"#2255ff","children":[
                {"type":"rectangle","id":"a","x":10,"y":10,"width":100,"height":240,"fill":"#ff0000"}]},
            {"type":"frame","id":"q","x":0,"y":0,"width":300,"height":300,"layout":"none","fill":"#ffff00","children":[
                {"type":"rectangle","id":"b","x":10,"y":150,"width":100,"height":100,"fill":"#00ff00"}]}]}"##;
        let doc = Doc::from_json(json).unwrap();
        let (mut fonts, mut assets) = offline();
        let mut r = Renderer::default();
        r.slides.push(Slide::new("b"));
        r.slides.push(Slide::new("a"));
        let mut cx = Ctx { doc: &doc, libs: &[], fonts: &mut fonts, assets: &mut assets };
        let roots = r.shift_siblings(&mut cx);
        let rebuilt = r.sync(&mut cx);
        r.refresh_slides(&mut cx, &rebuilt, roots);
        r.render(300, 300, 0.0, 0.0, 1.0, [30, 30, 30], None, true);
        for s in &mut r.slides {
            s.start = Some(Instant::now() - Duration::from_millis(100));
        }
        let px = r.render(300, 300, 0.0, 0.0, 1.0, [30, 30, 30], None, true).to_vec();
        // b (y 150..250, 6.8 low) is mid-slide over the yellow frame q, which a's cover (y 10..250) repaints.
        let g = px[(200 * 300 + 50) * 4 + 1];
        let rb = px[(200 * 300 + 50) * 4];
        assert!(g > 250 && rb < 230, "b still shows over q, got r {rb} g {g}");
    }

    /// A node sliding into a rotated or semi-transparent frame paints under that frame's rotation and
    /// opacity, so the end of the slide does not jump to the settled view.
    #[test]
    fn slide_follows_parent_rotation_and_opacity() {
        for extra in [r#""rotation":20"#, r#""opacity":0.5"#] {
            let json = format!(
                r##"{{"version":"2.19","children":[{{"type":"frame","id":"p","x":100,"y":100,"width":300,"height":200,"layout":"none","fill":"#2255ff",{extra},
                "children":[{{"type":"rectangle","id":"k","x":120,"y":120,"width":100,"height":100,"fill":"#ff0000"}}]}}]}}"##
            );
            let doc = Doc::from_json(&json).unwrap();
            let (mut fonts, mut assets) = offline();
            let mut r = Renderer::default();
            r.slides.push(Slide::new("k"));
            let mut sync = |r: &mut Renderer| {
                let mut cx = Ctx { doc: &doc, libs: &[], fonts: &mut fonts, assets: &mut assets };
                let roots = r.shift_siblings(&mut cx);
                let rebuilt = r.sync(&mut cx);
                r.refresh_slides(&mut cx, &rebuilt, roots);
            };
            sync(&mut r);
            let frame = |r: &mut Renderer| r.render(500, 400, 0.0, 0.0, 1.0, [30, 30, 30], None, true).to_vec();
            frame(&mut r);
            // Near the end: the offset is gone and the opacity is 0.95.
            r.slides[0].start = Some(Instant::now() - Duration::from_millis(590));
            let late = frame(&mut r);
            r.slides[0].start = Some(Instant::now() - SLIDE_TIME);
            assert!(r.expire_slides());
            sync(&mut r);
            let done = frame(&mut r);
            let jumped = late.chunks(4).zip(done.chunks(4)).filter(|(a, b)| a[0].abs_diff(b[0]) as u32 + a[1].abs_diff(b[1]) as u32 > 40).count();
            assert!(jumped < 100, "{extra}: {jumped} pixels change when the slide ends");
        }
    }

    /// A node sliding into a clipping frame is cut at the frame's edge while it moves (its snapshot
    /// is painted offset, so the clip cannot sit in the snapshot), and still shows inside.
    #[test]
    fn slide_is_clipped_by_its_clipping_parent() {
        let json = r##"{"version":"2.19","children":[
            {"type":"frame","id":"p","x":100,"y":100,"width":300,"height":200,"layout":"none","clip":true,"fill":"#eeeeaa","children":[
                {"type":"rectangle","id":"k","x":20,"y":150,"width":260,"height":50,"fill":"#00ff00"}]}]}"##;
        let doc = Doc::from_json(json).unwrap();
        let (mut fonts, mut assets) = offline();
        let mut r = Renderer::default();
        r.slides.push(Slide::new("k"));
        let mut cx = Ctx { doc: &doc, libs: &[], fonts: &mut fonts, assets: &mut assets };
        let roots = r.shift_siblings(&mut cx);
        let rebuilt = r.sync(&mut cx);
        r.refresh_slides(&mut cx, &rebuilt, roots);
        r.render(500, 400, 0.0, 0.0, 1.0, [30, 30, 30], None, true);
        // Mid-slide: about 15 units low, the frame's bottom edge is at y 300.
        r.slides[0].start = Some(Instant::now() - Duration::from_millis(100));
        let px = r.render(500, 400, 0.0, 0.0, 1.0, [30, 30, 30], None, true).to_vec();
        let green = |x: usize, y: usize| px[(y * 500 + x) * 4 + 1] > px[(y * 500 + x) * 4] + 12;
        let outside = (302..400).flat_map(|y| (0..500).map(move |x| (x, y))).filter(|&(x, y)| green(x, y)).count();
        assert_eq!(outside, 0, "the sliding node shows below the clipping frame");
        assert!(green(200, 290), "the part inside the frame still shows the sliding node");
    }

    /// Present mode renders one frame alone; a node sliding into it must still paint there (the
    /// cached layer leaves it out), and one sliding in another frame must not.
    #[test]
    fn solo_frame_paints_its_sliding_nodes() {
        let json = r##"{"version":"2.19","children":[
            {"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","fill":"#ffffff","children":[
                {"type":"rectangle","id":"r","x":100,"y":100,"width":100,"height":50,"fill":"#ff0000"}]},
            {"type":"frame","id":"g","x":0,"y":500,"width":400,"height":400,"layout":"none","fill":"#ffffff","children":[
                {"type":"rectangle","id":"s","x":100,"y":100,"width":100,"height":50,"fill":"#ff0000"}]}]}"##;
        let doc = Doc::from_json(json).unwrap();
        let (mut fonts, mut assets) = offline();
        let mut r = Renderer::default();
        r.slides.extend([Slide::new("r"), Slide::new("s")]);
        let mut cx = Ctx { doc: &doc, libs: &[], fonts: &mut fonts, assets: &mut assets };
        let roots = r.shift_siblings(&mut cx);
        let rebuilt = r.sync(&mut cx);
        r.refresh_slides(&mut cx, &rebuilt, roots);
        let green = |px: &[u8], x: usize, y: usize| px[(y * 400 + x) * 4 + 1];
        // Paint the cached view first: the slide must be read within ~40 ms of its start below
        // (offset 50·e^-20t > 3), and a cold first paint on a loaded machine can take longer.
        r.render(400, 400, 0.0, 0.0, 1.0, [30, 30, 30], Some("f"), true);
        // 100 ms in the node is part way there; only the presented frame's node paints.
        for s in &mut r.slides {
            s.start = Some(Instant::now() - Duration::from_millis(100));
        }
        let px = r.render(400, 400, 0.0, 0.0, 1.0, [30, 30, 30], Some("f"), true).to_vec();
        assert!(green(&px, 150, 153) < 200, "the sliding node paints in the solo frame");
        let px = r.render(400, 400, 0.0, 500.0, 1.0, [30, 30, 30], Some("f"), true).to_vec();
        assert!(green(&px, 150, 153) == 30, "a node sliding in another frame stays out");
        r.slides.iter_mut().find(|s| s.id == "s").unwrap().start = None;
        r.render(400, 400, 0.0, 0.0, 1.0, [30, 30, 30], Some("f"), true);
        assert!(r.slides.iter().find(|s| s.id == "s").unwrap().start.is_none(), "its clock does not start while another frame is presented");
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

    /// A sliding node with a drop or inner shadow is drawn by resvg; its filter layers stay
    /// within the frame (plus a margin), however big the node is (it once allocated the node's area).
    #[test]
    fn sliding_huge_shadowed_node_renders_quickly() {
        for shadow in ["outer", "inner"] {
            let json = format!(
                r##"{{"version":"2.19","children":[{{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","children":[
                {{"type":"frame","id":"n","x":0,"y":0,"width":100000,"height":100000,"fill":"#ff0000","effect":{{"type":"shadow","shadowType":"{shadow}","blur":10}}}}]}}]}}"##
            );
            let doc = Doc::from_json(&json).unwrap();
            let (mut fonts, mut assets) = offline();
            let mut r = Renderer::default();
            r.slides.push(Slide::new("n"));
            let mut cx = Ctx { doc: &doc, libs: &[], fonts: &mut fonts, assets: &mut assets };
            let roots = r.shift_siblings(&mut cx);
            let rebuilt = r.sync(&mut cx);
            r.refresh_slides(&mut cx, &rebuilt, roots);
            r.slides[0].start = Some(Instant::now() - Duration::from_millis(300));
            let t = Instant::now();
            let px = r.render(400, 400, 0.0, 0.0, 1.0, [30, 30, 30], None, true).to_vec();
            assert!(t.elapsed() < Duration::from_secs(5), "{shadow} shadow: took {:?}", t.elapsed());
            let at = (200 * 400 + 200) * 4;
            assert!(px[at] > 100 && px[at + 1] < 100, "{shadow} shadow: the node still paints, got {:?}", &px[at..at + 3]);
        }
    }
}
