//! Laid-out render tree → SVG. Used for rasterizing (via resvg) and for SVG export.
//! Paint order: drop shadows (cast by the node's own outline, knocked out under it)
//! → fills → inner shadows → stroke → children; a clipping frame draws its stroke above its children.
//! A layer blur wraps everything the node draws, shadows included.

use crate::resolve::{num, RNode};
use crate::text::{Fonts, Line, TextStyle};
use base64::Engine as _;
use resvg::tiny_skia;
use serde_json::{Map, Value};
use std::collections::HashMap;
use std::fmt::Write;
use std::path::{Path, PathBuf};

type Rect = (f64, f64, f64, f64);

/// Downloads that run at the same time; the rest of a document's missing assets wait their turn.
const MAX_DOWNLOADS: usize = 8;

type Download = Box<dyn FnOnce() -> bool + Send>;

/// Icon SVGs and remote images, fetched once and cached on disk.
pub struct Assets {
    dir: PathBuf,
    icons: HashMap<String, Option<String>>,
    /// Icon keys and image URLs whose download failed this session (not retried on every layer rebuild).
    failed: std::collections::HashSet<String>,
    /// Downloads in flight (icon key or image url), run on background threads so they never hold the engine.
    loading: std::collections::HashSet<String>,
    /// Downloads waiting for a free thread (at most `MAX_DOWNLOADS` run at once), and how many run now.
    queue: std::collections::VecDeque<(String, Download)>,
    running: usize,
    /// Bumped when a download ends; the renderer rebuilds every layer when it changes.
    pub generation: u64,
    /// Whether a `data:` image is within the size limit, by url hash (its header is decoded once, not per use).
    data_ok: HashMap<u64, bool>,
    /// Too-deep verdicts of SVG images, keyed by `deep_key` and valid while the stamp matches.
    deep: HashMap<String, (u64, bool)>,
    /// PDF export: refuse a raster image whose data does not fully decode (see `decodes`), since
    /// the PDF library decodes late and one damaged image would fail the whole document.
    verify: bool,
    /// Full-decode verdicts by href hash, kept for one verifying export.
    decoded: HashMap<u64, bool>,
    pub online: bool,
}

impl Assets {
    pub fn new(dir: PathBuf) -> Assets {
        let _ = std::fs::create_dir_all(dir.join("icons"));
        let _ = std::fs::create_dir_all(dir.join("images"));
        Assets { dir, icons: HashMap::new(), failed: Default::default(), loading: Default::default(), queue: Default::default(), running: 0, generation: 0, data_ok: HashMap::new(), deep: HashMap::new(), verify: false, decoded: HashMap::new(), online: true }
    }

    /// Turn the full-decode check of `image` on or off; each switch drops the cached verdicts, as files may change between exports.
    pub fn verify(&mut self, on: bool) {
        self.verify = on;
        self.decoded.clear();
    }

    /// Icon at the default weight (200; only Material Symbols has weights).
    pub fn icon(&mut self, library: &str, name: &str) -> Option<String> {
        self.icon_weight(library, name, 200.0)
    }

    /// The icon's SVG, or None while it downloads in the background (the renderer draws again when it lands).
    pub fn icon_weight(&mut self, library: &str, name: &str, weight: f64) -> Option<String> {
        let (url, tag) = icon_url(library, name, weight);
        let key = format!("{tag}/{name}");
        if let Some(v) = self.icons.get(&key) {
            return v.clone();
        }
        let file = self.dir.join("icons").join(format!("{tag}-{}.svg", name.replace(['/', '\\', '.'], "_")));
        let svg = std::fs::read_to_string(&file).ok();
        if svg.is_none() && self.online && !name.is_empty() && !self.failed.contains(&key) {
            self.spawn(key, move || crate::net::get_text(&url).filter(|t| t.contains("<svg")).is_some_and(|t| store(&file, t.as_bytes())));
            return None;
        }
        self.icons.insert(key, svg.clone());
        svg
    }

    /// This icon is still downloading, so None from `icon_weight` does not mean it does not exist.
    pub fn icon_loading(&self, library: &str, name: &str, weight: f64) -> bool {
        self.loading(&format!("{}/{name}", icon_url(library, name, weight).1))
    }

    /// A download of this icon key or image url is still running.
    pub fn loading(&self, key: &str) -> bool {
        self.loading.contains(key)
    }

    /// The icon keys and image urls downloading now (running or queued).
    pub fn in_flight(&self) -> Vec<String> {
        self.loading.iter().cloned().collect()
    }

    /// Run `job` (a download that stores its result on disk, true on success) on a background thread
    /// unless this key is already loading or failed, then tell the engine, which draws again.
    /// Past `MAX_DOWNLOADS` running, it waits in the queue until one ends.
    fn spawn(&mut self, key: String, job: impl FnOnce() -> bool + Send + 'static) {
        if self.failed.contains(&key) || !self.loading.insert(key.clone()) {
            return;
        }
        self.queue.push_back((key, Box::new(job)));
        self.start_queued();
    }

    fn start_queued(&mut self) {
        while self.running < MAX_DOWNLOADS {
            let Some((key, job)) = self.queue.pop_front() else { return };
            self.running += 1;
            std::thread::spawn(move || {
                let ok = job();
                crate::engine::engine().asset_ready(&key, ok);
            });
        }
    }

    /// A download ended: a failed one is not retried this session. Always bumps `generation`
    /// (a failed image draws as unavailable instead of blank).
    pub fn downloaded(&mut self, key: &str, ok: bool) {
        self.loading.remove(key);
        self.running = self.running.saturating_sub(1);
        if !ok {
            self.failed.insert(key.to_string());
        }
        self.generation += 1;
        self.start_queued();
    }

    /// Resolve an image fill url to a local file path usvg can load. A raster image over
    /// `MAX_IMAGE_PIXELS` is refused like a missing one: decoding it would freeze the render and
    /// need gigabytes (a few KB of PNG can declare 30000 x 30000). An SVG nested deeper than
    /// `MAX_SVG_DEPTH` counts as unavailable too: usvg recurses per level and overflows the stack.
    pub fn image(&mut self, url: &str, base: Option<&Path>) -> Option<String> {
        let href = self.resolve_image(url, base)?;
        if !self.within_pixel_limit(&href, url) {
            return None;
        }
        if self.verify
            && !*self.decoded.entry(fxhash(&href)).or_insert_with(|| {
                let ok = decodes(&href);
                if !ok {
                    eprintln!("image {}: damaged, left out of the export", if href.starts_with("data:") { "<data URL>" } else { url });
                }
                ok
            })
        {
            return None;
        }
        let too_deep = match deep_key(&href) {
            None => false,
            Some((key, stamp)) => match self.deep.get(&key) {
                Some(&(s, v)) if s == stamp => v,
                _ => {
                    let v = svg_too_deep(&href);
                    self.deep.insert(key, (stamp, v));
                    v
                }
            },
        };
        (!too_deep).then_some(href)
    }

    /// Whether a raster image's declared size is within `MAX_IMAGE_PIXELS` (the verdict of a `data:` image is cached).
    fn within_pixel_limit(&mut self, href: &str, url: &str) -> bool {
        let is_data = href.starts_with("data:");
        let key = fxhash(href);
        if is_data {
            if let Some(&ok) = self.data_ok.get(&key) {
                return ok;
            }
        }
        let size = href_size(href);
        let ok = size.is_none_or(|(w, h)| w as u64 * h as u64 <= MAX_IMAGE_PIXELS);
        if let (false, Some((w, h))) = (ok, size) {
            let shown = if is_data { "<data URL>" } else { url };
            eprintln!("image {shown}: {w}x{h} px is over the {} megapixel limit", MAX_IMAGE_PIXELS / 1_000_000);
        }
        if is_data {
            self.data_ok.insert(key, ok);
        }
        ok
    }

    fn resolve_image(&mut self, url: &str, base: Option<&Path>) -> Option<String> {
        if url.is_empty() || url.starts_with("pencil:") {
            return None;
        }
        if url.starts_with("data:") {
            return Some(url.to_string());
        }
        if url.starts_with("http://") || url.starts_with("https://") {
            // resvg picks the decoder from the file extension, so cached files need one
            // (extension-less files from older versions are renamed on first use).
            let stem = self.dir.join("images").join(format!("{:016x}", fxhash(url)));
            let cached = ["png", "jpg", "gif", "webp", "svg"].iter().map(|e| stem.with_extension(e)).find(|f| f.exists());
            let file = match cached {
                Some(f) => f,
                None => {
                    // Not on disk yet: download in the background (the fill draws nothing until it lands).
                    let Ok(bytes) = std::fs::read(&stem) else {
                        if self.online {
                            let (u, stem) = (url.to_string(), stem.clone());
                            self.spawn(url.to_string(), move || crate::net::get_bytes(&u).is_some_and(|b| store(&stem.with_extension(image_ext(&b)), &b)));
                        }
                        return None;
                    };
                    let file = stem.with_extension(image_ext(&bytes));
                    std::fs::write(&file, &bytes).ok()?;
                    let _ = std::fs::remove_file(&stem);
                    file
                }
            };
            return Some(file.to_string_lossy().replace('\\', "/"));
        }
        let rel = url.strip_prefix("file://").unwrap_or(url);
        let p = Path::new(rel);
        let full = if p.is_absolute() { p.to_path_buf() } else { base?.join(rel) };
        full.exists().then(|| full.to_string_lossy().replace('\\', "/"))
    }
}

/// Download url and cache tag of an icon.
fn icon_url(library: &str, name: &str, weight: f64) -> (String, String) {
    let lib = library.to_ascii_lowercase();
    match lib.as_str() {
        "feather" => (format!("https://unpkg.com/feather-icons@4.29.2/dist/icons/{name}.svg"), "feather".to_string()),
        "phosphor" => {
            // Phosphor names carry the style: "house-fill", "gear-thin".
            let style = ["thin", "light", "bold", "fill", "duotone"].into_iter().find(|s| name.ends_with(&format!("-{s}"))).unwrap_or("regular");
            (format!("https://unpkg.com/@phosphor-icons/core@2.1.1/assets/{style}/{name}.svg"), "phosphor".to_string())
        }
        l if l.starts_with("material") => {
            let variant = if l.contains("rounded") { "rounded" } else if l.contains("sharp") { "sharp" } else { "outlined" };
            let w = ((weight / 100.0).round() * 100.0).clamp(100.0, 700.0) as u16;
            (format!("https://unpkg.com/@material-symbols/svg-{w}@0.27.0/{variant}/{name}.svg"), format!("material_{variant}_{w}"))
        }
        _ => (format!("https://unpkg.com/lucide-static@0.563.0/icons/{name}.svg"), "lucide_0.563".to_string()),
    }
}

/// Write a downloaded file under a temporary name first, so a render never reads it half written.
fn store(file: &Path, bytes: &[u8]) -> bool {
    let tmp = file.with_extension("part");
    std::fs::write(&tmp, bytes).is_ok() && std::fs::rename(&tmp, file).is_ok()
}

/// Largest raster image decoded, in pixels (RGBA: 400 MB).
const MAX_IMAGE_PIXELS: u64 = 100_000_000;

/// Whether a PNG, GIF or WebP image decodes completely (a truncated or CRC-damaged one passes the
/// header check but fails here); other formats, which the PDF library embeds as they are, count as fine.
fn decodes(href: &str) -> bool {
    let bytes = if href.starts_with("data:") {
        match data_url::DataUrl::process(href).ok().and_then(|u| u.decode_to_vec().ok()) {
            Some((b, _)) => b,
            None => return true,
        }
    } else {
        match std::fs::read(href) {
            Ok(b) => b,
            Err(_) => return true,
        }
    };
    match image_ext(&bytes) {
        "png" => tiny_skia::Pixmap::decode_png(&bytes).is_ok(),
        "gif" => {
            let mut opts = gif::DecodeOptions::new();
            opts.set_color_output(gif::ColorOutput::RGBA);
            matches!(opts.read_info(&bytes[..]).and_then(|mut d| d.read_next_frame().map(|f| f.is_some())), Ok(true))
        }
        "webp" => image_webp::WebPDecoder::new(std::io::Cursor::new(&bytes)).and_then(|mut d| {
            let mut buf = vec![0; d.output_buffer_size().ok_or(image_webp::DecodingError::ImageTooLarge)?];
            d.read_image(&mut buf)
        }).is_ok(),
        _ => true,
    }
}

/// Declared pixel size of a raster image given as `Assets::image` returns it (a file path or a
/// `data:` url), read from its header without decoding. None for SVG or anything unrecognised.
fn href_size(href: &str) -> Option<(u32, u32)> {
    if href.starts_with("data:") {
        let (bytes, _) = data_url::DataUrl::process(href).ok()?.decode_to_vec().ok()?;
        return image_size(&mut std::io::Cursor::new(bytes));
    }
    image_size(&mut std::io::BufReader::new(std::fs::File::open(href).ok()?))
}

/// Width and height from a PNG, GIF, JPEG or WebP header.
fn image_size<R: std::io::BufRead + std::io::Seek>(r: &mut R) -> Option<(u32, u32)> {
    use std::io::{Read, SeekFrom};
    let mut head = [0u8; 24];
    let n = r.read(&mut head).ok()?;
    let be16 = |b: &[u8]| u16::from_be_bytes([b[0], b[1]]) as u32;
    match head {
        [0x89, b'P', b'N', b'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 13, b'I', b'H', b'D', b'R', ..] if n >= 24 => Some((u32::from_be_bytes(head[16..20].try_into().ok()?), u32::from_be_bytes(head[20..24].try_into().ok()?))),
        [b'G', b'I', b'F', b'8', ..] if n >= 10 => Some((u16::from_le_bytes([head[6], head[7]]) as u32, u16::from_le_bytes([head[8], head[9]]) as u32)),
        [b'R', b'I', b'F', b'F', _, _, _, _, b'W', b'E', b'B', b'P', ..] => {
            r.seek(SeekFrom::Start(0)).ok()?;
            image_webp::WebPDecoder::new(r).ok().map(|d| d.dimensions())
        }
        [0xFF, 0xD8, ..] => {
            r.seek(SeekFrom::Start(2)).ok()?;
            // Walk the marker segments to the first start-of-frame (C0-CF but not DHT, JPG, DAC).
            loop {
                let mut m = [0u8; 2];
                r.read_exact(&mut m).ok()?;
                match m {
                    [0xFF, 0xFF] => {
                        r.seek(SeekFrom::Current(-1)).ok()?;
                    }
                    [0xFF, 0x01 | 0xD0..=0xD7] => {}
                    [0xFF, 0xD9 | 0xDA] | [_, 0x00] => return None,
                    [0xFF, t] => {
                        let mut seg = [0u8; 7];
                        r.read_exact(&mut seg[..2]).ok()?;
                        let len = be16(&seg) as i64;
                        if matches!(t, 0xC0..=0xCF) && !matches!(t, 0xC4 | 0xC8 | 0xCC) {
                            r.read_exact(&mut seg[2..7]).ok()?;
                            return Some((be16(&seg[5..]), be16(&seg[3..])));
                        }
                        r.seek(SeekFrom::Current(len - 2)).ok()?;
                    }
                    _ => return None,
                }
            }
        }
        _ => None,
    }
}

/// Deepest element nesting an SVG image may have; usvg's own parse overflows a 2 MiB stack at ~3500.
const MAX_SVG_DEPTH: usize = 256;

thread_local! {
    /// Element depth of the SVG images being parsed right now, outermost first (see `image_href_resolver`).
    static NESTED_DEPTH: std::cell::Cell<usize> = const { std::cell::Cell::new(0) };
}

/// usvg's data-url image resolver, refusing SVGs whose nesting, summed over images embedded in
/// images, exceeds `MAX_SVG_DEPTH`. `Assets::image` vets the top-level image; this covers a shallow
/// SVG that carries a deep one as a `data:` url, which usvg would parse recursively.
pub fn image_href_resolver() -> resvg::usvg::ImageHrefResolver<'static> {
    let inner = resvg::usvg::ImageHrefResolver::default_data_resolver();
    resvg::usvg::ImageHrefResolver {
        resolve_data: Box::new(move |mime, data, opts| {
            let used = NESTED_DEPTH.get();
            let depth = if is_svg_payload(mime, &data) { max_depth(&data, MAX_SVG_DEPTH - used) } else { 0 };
            if used + depth > MAX_SVG_DEPTH {
                return None;
            }
            NESTED_DEPTH.set(used + depth);
            let kind = inner(mime, data, opts);
            NESTED_DEPTH.set(used);
            kind
        }),
        resolve_string: resvg::usvg::ImageHrefResolver::default_string_resolver(),
    }
}

/// Whether usvg parses a data url of this MIME type and content as an SVG (it sniffs `text/plain`).
fn is_svg_payload(mime: &str, bytes: &[u8]) -> bool {
    matches!(mime, "image/svg+xml" | "text/plain") && image_ext(bytes) == "svg"
}

/// Cache key and change stamp of an image `svg_too_deep` would scan: a `data:` url (by hash) or an
/// `.svg` file (by path, stamped with its size and modification time).
fn deep_key(href: &str) -> Option<(String, u64)> {
    if href.starts_with("data:") {
        return Some((format!("{:016x}", fxhash(href)), 0));
    }
    if !href.to_ascii_lowercase().ends_with(".svg") {
        return None;
    }
    let meta = std::fs::metadata(href).ok()?;
    let nanos = meta.modified().ok().and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok()).map_or(0, |d| d.as_nanos() as u64);
    Some((href.to_string(), meta.len().wrapping_add(nanos)))
}

/// Whether the image behind a `data:` url or a file path is an SVG nested deeper than `MAX_SVG_DEPTH`.
fn svg_too_deep(href: &str) -> bool {
    let bytes = if let Some(rest) = href.strip_prefix("data:") {
        let Some((head, payload)) = rest.split_once(',') else { return false };
        // Only what usvg parses as markup: other MIME types are skipped, or ignored by usvg.
        if !matches!(head.split(';').next().unwrap_or(""), "image/svg+xml" | "text/plain" | "") {
            return false;
        }
        let bytes = if head.ends_with(";base64") {
            match base64::engine::general_purpose::STANDARD.decode(payload.trim()) {
                Ok(b) => b,
                Err(_) => return false,
            }
        } else {
            percent_decode(payload)
        };
        if image_ext(&bytes) != "svg" {
            return false;
        }
        bytes
    } else if href.to_ascii_lowercase().ends_with(".svg") {
        match std::fs::read(href) {
            Ok(b) => b,
            Err(_) => return false,
        }
    } else {
        return false;
    };
    max_depth(&bytes, MAX_SVG_DEPTH) > MAX_SVG_DEPTH
}

fn percent_decode(s: &str) -> Vec<u8> {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        let hex = |c: u8| (c as char).to_digit(16);
        match (b[i], b.get(i + 1).and_then(|&c| hex(c)), b.get(i + 2).and_then(|&c| hex(c))) {
            (b'%', Some(h), Some(l)) => {
                out.push((h * 16 + l) as u8);
                i += 3;
            }
            (c, ..) => {
                out.push(c);
                i += 1;
            }
        }
    }
    out
}

/// How deeply markup nests elements (comments, CDATA, PIs and the doctype skipped); stops at
/// `limit + 1` once it is exceeded.
fn max_depth(b: &[u8], limit: usize) -> usize {
    let find = |from: usize, pat: &[u8]| b[from..].windows(pat.len()).position(|w| w == pat).map_or(b.len(), |p| from + p + pat.len());
    let (mut i, mut depth, mut peak) = (0, 0usize, 0usize);
    while i < b.len() {
        if b[i] != b'<' {
            i += 1;
        } else if b[i..].starts_with(b"<!--") {
            i = find(i + 4, b"-->");
        } else if b[i..].starts_with(b"<![CDATA[") {
            i = find(i + 9, b"]]>");
        } else if b[i..].starts_with(b"<?") {
            i = find(i + 2, b"?>");
        } else if b[i..].starts_with(b"<!") {
            // A doctype may hold an internal subset in brackets.
            let (mut j, mut sq) = (i + 2, 0usize);
            while j < b.len() && (b[j] != b'>' || sq > 0) {
                match b[j] {
                    b'[' => sq += 1,
                    b']' => sq = sq.saturating_sub(1),
                    _ => {}
                }
                j += 1;
            }
            i = j + 1;
        } else {
            let close = b.get(i + 1) == Some(&b'/');
            let (mut j, mut quote) = (i + 1, 0u8);
            while j < b.len() && (b[j] != b'>' || quote != 0) {
                match (quote, b[j]) {
                    (0, q @ (b'"' | b'\'')) => quote = q,
                    (q, c) if q == c => quote = 0,
                    _ => {}
                }
                j += 1;
            }
            if close {
                depth = depth.saturating_sub(1);
            } else if b[j.min(b.len()) - 1] != b'/' {
                depth += 1;
                peak = peak.max(depth);
                if depth > limit {
                    return depth;
                }
            }
            i = j + 1;
        }
    }
    peak
}

/// File extension for downloaded image bytes (resvg decodes by extension).
fn image_ext(b: &[u8]) -> &'static str {
    match b {
        [0x89, b'P', b'N', b'G', ..] => "png",
        [0xFF, 0xD8, ..] => "jpg",
        [b'G', b'I', b'F', b'8', ..] => "gif",
        [b'R', b'I', b'F', b'F', _, _, _, _, b'W', b'E', b'B', b'P', ..] => "webp",
        _ => "svg",
    }
}

pub fn fxhash(s: &str) -> u64 {
    s.bytes().fold(0xcbf29ce484222325u64, |h, b| (h ^ b as u64).wrapping_mul(0x100000001b3))
}

pub fn esc(s: &str) -> String {
    let mut o = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '&' => o.push_str("&amp;"),
            '<' => o.push_str("&lt;"),
            '>' => o.push_str("&gt;"),
            '"' => o.push_str("&quot;"),
            '\'' => o.push_str("&apos;"),
            c if (c as u32) < 0x20 && c != '\t' => {}
            c => o.push(c),
        }
    }
    o
}

pub(crate) fn rgba8(s: &str) -> Option<[u8; 4]> {
    let h = s.trim().strip_prefix('#')?;
    if !h.bytes().all(|b| b.is_ascii_hexdigit()) {
        return None;
    }
    let c = h.as_bytes();
    let hx = |a: u8, b: u8| u8::from_str_radix(std::str::from_utf8(&[a, b]).ok()?, 16).ok();
    Some(match c.len() {
        3 => [hx(c[0], c[0])?, hx(c[1], c[1])?, hx(c[2], c[2])?, 255],
        6 | 8 => [hx(c[0], c[1])?, hx(c[2], c[3])?, hx(c[4], c[5])?, if c.len() == 8 { hx(c[6], c[7])? } else { 255 }],
        _ => return None,
    })
}

/// Parse `#RGB`, `#RRGGBB` or `#RRGGBBAA` into (`#rrggbb`, alpha). Anything else
/// (`#RGBA`, CSS names, unresolved `$variables`) is transparent, i.e. `None`.
pub fn color(s: &str) -> Option<(String, f64)> {
    let [r, g, b, a] = rgba8(s)?;
    Some((format!("#{r:02x}{g:02x}{b:02x}"), a as f64 / 255.0))
}

pub(crate) fn rgba(s: &str) -> Option<[f64; 4]> {
    rgba8(s).map(|c| c.map(|v| v as f64 / 255.0))
}

fn hex(c: [f64; 4]) -> String {
    let b = |v: f64| (v.clamp(0.0, 1.0) * 255.0).round() as u8;
    format!("#{:02x}{:02x}{:02x}", b(c[0]), b(c[1]), b(c[2]))
}

/// Tight bounds (x, y, w, h) of an SVG path: curve extrema, not control points.
pub fn path_bbox(d: &str) -> Option<Rect> {
    let mut b = [f64::MAX, f64::MAX, f64::MIN, f64::MIN];
    let mut add = |x: f64, y: f64| b = [b[0].min(x), b[1].min(y), b[2].max(x), b[3].max(y)];
    let (mut cur, mut start) = ((0.0, 0.0), (0.0, 0.0));
    // Parameters in (0,1) where a cubic/quadratic's derivative vanishes on one axis.
    let cubic_t = |p0: f64, p1: f64, p2: f64, p3: f64| -> Vec<f64> {
        let (a, b, c) = (-p0 + 3.0 * p1 - 3.0 * p2 + p3, 2.0 * (p0 - 2.0 * p1 + p2), p1 - p0);
        let roots = if a.abs() < 1e-12 {
            if b.abs() < 1e-12 { vec![] } else { vec![-c / b] }
        } else {
            let disc = b * b - 4.0 * a * c;
            if disc < 0.0 { vec![] } else { vec![(-b + disc.sqrt()) / (2.0 * a), (-b - disc.sqrt()) / (2.0 * a)] }
        };
        roots.into_iter().filter(|t| *t > 0.0 && *t < 1.0).collect()
    };
    for seg in svgtypes::SimplifyingPathParser::from(d) {
        match seg.ok()? {
            svgtypes::SimplePathSegment::MoveTo { x, y } => {
                add(x, y);
                (cur, start) = ((x, y), (x, y));
            }
            svgtypes::SimplePathSegment::LineTo { x, y } => {
                add(x, y);
                cur = (x, y);
            }
            svgtypes::SimplePathSegment::CurveTo { x1, y1, x2, y2, x, y } => {
                let at = |t: f64, p0: f64, p1: f64, p2: f64, p3: f64| {
                    let s = 1.0 - t;
                    s * s * s * p0 + 3.0 * s * s * t * p1 + 3.0 * s * t * t * p2 + t * t * t * p3
                };
                let mut ts = cubic_t(cur.0, x1, x2, x);
                ts.extend(cubic_t(cur.1, y1, y2, y));
                for t in ts {
                    add(at(t, cur.0, x1, x2, x), at(t, cur.1, y1, y2, y));
                }
                add(x, y);
                cur = (x, y);
            }
            svgtypes::SimplePathSegment::Quadratic { x1, y1, x, y } => {
                let at = |t: f64, p0: f64, p1: f64, p2: f64| (1.0 - t) * (1.0 - t) * p0 + 2.0 * (1.0 - t) * t * p1 + t * t * p2;
                for (p0, p1, p2) in [(cur.0, x1, x), (cur.1, y1, y)] {
                    let den = p0 - 2.0 * p1 + p2;
                    let t = if den.abs() > 1e-12 { (p0 - p1) / den } else { -1.0 };
                    if t > 0.0 && t < 1.0 {
                        add(at(t, cur.0, x1, x), at(t, cur.1, y1, y));
                    }
                }
                add(x, y);
                cur = (x, y);
            }
            svgtypes::SimplePathSegment::ClosePath => cur = start,
        }
    }
    (b[2] >= b[0]).then(|| (b[0], b[1], b[2] - b[0], b[3] - b[1]))
}

/// One fill or stroke paint: an SVG paint server value with its alpha and blend mode.
struct Paint {
    value: String,
    alpha: f64,
    blend: Option<&'static str>,
}

impl Paint {
    fn attrs(&self, prop: &str) -> String {
        let style = self.blend.map(|m| format!(r#" style="mix-blend-mode:{m}""#)).unwrap_or_default();
        format!(r#" {prop}="{}" {prop}-opacity="{}"{style}"#, self.value, self.alpha)
    }
}

struct Shadow {
    dx: f64,
    dy: f64,
    sigma: f64,
    color: String,
    alpha: f64,
    blend: Option<&'static str>,
    inner: bool,
}

#[derive(Default)]
pub(crate) struct Effects {
    shadows: Vec<Shadow>,
    /// Layer blur sigma (radius / 2; several blurs compound).
    pub(crate) blur: f64,
    /// Background blur sigma (the largest of several).
    pub(crate) backdrop: f64,
}

impl Effects {
    pub(crate) fn of(n: &RNode) -> Effects {
        let mut fx = Effects::default();
        let list: Vec<&Value> = match n.p.get("effect") {
            Some(Value::Array(a)) => a.iter().collect(),
            Some(v @ Value::Object(_)) => vec![v],
            _ => vec![],
        };
        for e in list.into_iter().filter(|e| e["enabled"] != false) {
            let sigma = |k: &str| num(&e[k]).unwrap_or(0.0).max(0.0) / 2.0;
            match e["type"].as_str() {
                Some("shadow") => {
                    let (color, alpha) = match e.get("color") {
                        None => ("#000000".to_string(), 1.0),
                        Some(c) => match c.as_str().and_then(color) {
                            Some(c) => c,
                            None => continue,
                        },
                    };
                    let (dx, dy) = (num(&e["offset"]["x"]).unwrap_or(0.0), num(&e["offset"]["y"]).unwrap_or(0.0));
                    let blend = blend_mode(e["blendMode"].as_str());
                    fx.shadows.push(Shadow { dx, dy, sigma: sigma("blur"), color, alpha, blend, inner: e["shadowType"] == "inner" });
                }
                Some("blur") => fx.blur = fx.blur.hypot(sigma("radius")),
                Some("background_blur") => fx.backdrop = fx.backdrop.max(sigma("radius")),
                _ => {}
            }
        }
        fx
    }

    /// How far drop shadows reach past the source outline.
    fn reach(&self) -> f64 {
        self.shadows.iter().map(|s| s.dx.abs().max(s.dy.abs()) + 3.0 * s.sigma).fold(0.0, f64::max)
    }
}

/// Fill outline of a shape node in absolute coordinates.
struct Geo {
    d: String,
    evenodd: bool,
    closed: bool,
}

impl Geo {
    fn rule(&self, attr: &str) -> String {
        if self.evenodd { format!(r#" {attr}="evenodd""#) } else { String::new() }
    }
}

struct Stroke {
    paints: Vec<Paint>,
    /// top, right, bottom, left
    widths: [f64; 4],
    /// "inner" | "center" | "outer"
    align: &'static str,
}

impl Stroke {
    fn k(&self) -> f64 {
        if self.align == "center" { 1.0 } else { 2.0 }
    }
    /// How far the stroke reaches outside the fill outline.
    fn reach(&self) -> f64 {
        let w = self.widths.iter().copied().fold(0.0, f64::max);
        match self.align {
            "inner" => 0.0,
            "outer" => w,
            _ => w / 2.0,
        }
    }
}

pub struct Svg<'a> {
    pub fonts: &'a mut Fonts,
    pub assets: &'a mut Assets,
    pub base: Option<PathBuf>,
    defs: String,
    out: String,
    uid: usize,
    /// Per open container: (container is rotated/flipped, ids it painted so far); the backdrop for background blur.
    painted: Vec<(bool, Vec<String>)>,
    /// Give nodes ids, only needed when the tree has a background blur.
    tag: bool,
    /// Nodes (with their subtrees) left out: they are sliding in and painted on their own.
    pub skip: std::collections::HashSet<String>,
    /// Outline of the shape whose fills are being emitted, for shader fills' `@sdf`.
    outline: Option<crate::shader::Outline>,
    /// How far the paints being built reach outside the node box (a stroke's outer half): image patterns grow by it so they do not tile there.
    pad: f64,
}

impl<'a> Svg<'a> {
    pub fn new(fonts: &'a mut Fonts, assets: &'a mut Assets, base: Option<PathBuf>) -> Self {
        Svg { fonts, assets, base, defs: String::new(), out: String::new(), uid: 0, painted: Vec::new(), tag: false, skip: Default::default(), outline: None, pad: 0.0 }
    }

    /// Number ids from `uid` on, so several inline SVGs in one page do not share ids; read back with `uid`.
    pub fn start_ids(mut self, uid: usize) -> Self {
        self.uid = uid;
        self
    }

    pub fn uid(&self) -> usize {
        self.uid
    }

    /// Wrap emitted nodes into a document. With `view` the output is cropped to that box (exports);
    /// without, coordinates stay absolute (renderer cache).
    pub fn finish(self, view: Option<Rect>) -> String {
        let head = match view {
            Some((x, y, w, h)) => format!(r#"width="{}" height="{}" viewBox="{x} {y} {w} {h}""#, w.max(1.0), h.max(1.0)),
            None => r#"width="1" height="1""#.to_string(),
        };
        format!(r#"<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" {head}><defs>{}</defs>{}</svg>"#, self.defs, self.out)
    }

    /// Wrap what was emitted so far in an ancestor's painted matrix and opacity.
    pub fn wrap(&mut self, m: Option<[f64; 6]>, opacity: f64) {
        let mut g = String::new();
        if opacity < 1.0 {
            let _ = write!(g, r#" opacity="{opacity}""#);
        }
        if let Some([a, b, c, d, e, f]) = m {
            let _ = write!(g, r#" transform="matrix({a} {b} {c} {d} {e} {f})""#);
        }
        if !g.is_empty() {
            self.out = format!("<g{g}>{}</g>", self.out);
        }
    }

    fn id(&mut self, prefix: &str) -> String {
        self.uid += 1;
        format!("{prefix}{}", self.uid)
    }

    pub fn node(&mut self, n: &RNode) {
        if self.painted.is_empty() {
            self.tag = has_backdrop_blur(n);
        }
        let opacity = n.f("opacity").unwrap_or(1.0);
        if opacity <= 0.0 || self.skip.contains(&n.id) {
            return;
        }
        let tf = transform(n);
        let fx = Effects::of(n);
        // The backdrop is blurred inside the node's opacity layer, which starts out empty: below
        // full opacity nothing shows.
        let backdrop = fx.backdrop > 0.0 && opacity >= 1.0;
        // Id of the node's black outline (its mask path): what casts its shadows and shapes its
        // background blur. Defined by `shape`/`group`/`glyphs`, absent when the node has none.
        let outline = (!fx.shadows.is_empty() || backdrop).then(|| self.id("m"));
        if let Some(m) = outline.as_deref().filter(|_| backdrop) {
            self.backdrop_blur(n, &tf, fx.backdrop, m);
        }
        let gid = self.id("n");
        let mut g = String::new();
        if self.tag {
            let _ = write!(g, r#" id="{gid}""#);
        }
        if opacity < 1.0 {
            let _ = write!(g, r#" opacity="{opacity}""#);
        }
        if !tf.is_empty() {
            let _ = write!(g, r#" transform="{tf}""#);
        }
        let _ = write!(self.out, "<g{g}>");
        let blur = (fx.blur > 0.0).then(|| {
            let id = self.id("f");
            let (x, y, w, h) = inflate(extent(n), 3.0 * fx.blur + fx.reach() + max_stroke(n) + 2.0);
            let _ = write!(self.defs, r#"<filter id="{id}" filterUnits="userSpaceOnUse" x="{x}" y="{y}" width="{w}" height="{h}" color-interpolation-filters="sRGB"><feGaussianBlur stdDeviation="{}"/></filter>"#, fx.blur);
            let _ = write!(self.out, r#"<g filter="url(#{id})">"#);
        });
        match n.ty.as_str() {
            "frame" | "rectangle" | "ellipse" | "polygon" | "path" | "line" => self.shape(n, &fx, outline.as_deref()),
            "group" => self.group(n, &fx, outline.as_deref()),
            "text" | "icon" => self.glyphs(n, &fx, outline.as_deref()),
            "note" | "prompt" | "context" => self.note(n),
            "script" if n.p.contains_key("__status") => self.script_status(n),
            // Generated children in the node's box, clipped when `clip` is set.
            "script" => {
                let clip = n.b("clip").then(|| geometry(n));
                self.children(n, None, clip.as_ref());
            }
            "browser" => self.placeholder(n),
            _ => {}
        }
        if blur.is_some() {
            self.out.push_str("</g>");
        }
        self.out.push_str("</g>");
        if self.tag {
            if let Some(level) = self.painted.last_mut() {
                level.1.push(gid);
            }
        }
    }

    fn shape(&mut self, n: &RNode, fx: &Effects, outline: Option<&str>) {
        let geo = geometry(n);
        let b = (n.x, n.y, n.w, n.h);
        let full_ellipse = n.f("innerRadius").unwrap_or(0.0) <= 0.0 && n.f("sweepAngle").is_none_or(|s| s.abs() >= 360.0);
        self.outline = Some(match n.ty.as_str() {
            "frame" | "rectangle" => crate::shader::Outline::RRect(radii(n)),
            "ellipse" if full_ellipse => crate::shader::Outline::Ellipse,
            _ => crate::shader::Outline::Path { d: geo.d.clone(), x: n.x, y: n.y, evenodd: geo.evenodd },
        });
        let fills = if n.ty == "line" { Vec::new() } else { self.paints(n.p.get("fill"), b) };
        self.outline = None;
        let stroke = self.stroke_of(n, &geo);
        // Shapes without a visible fill have no outline (no mask for them); frames always do.
        let visible = fills.iter().any(|p| p.alpha > 0.0);
        let mask = outline.filter(|_| visible || n.ty == "frame").map(|m| {
            let mut body = format!(r##"<path d="{}"{} fill="#000"/>"##, geo.d, geo.rule("fill-rule"));
            if let Some(s) = stroke.as_ref().filter(|s| s.align != "inner") {
                body += &self.stroke_body(n, &geo, s, &[Paint { value: "#000".into(), alpha: 1.0, blend: None }], "");
            }
            let _ = write!(self.defs, r#"<g id="{m}">{body}</g>"#);
            m
        });
        let mb = inflate(b, stroke.as_ref().map_or(0.0, Stroke::reach) + 1.0);
        let outline = round_outline(n, stroke.as_ref());
        if let Some(m) = &mask {
            self.drop_shadows(fx, m, mb, outline);
        }
        // Inner shadows are measured from the fill edge whatever the stroke: an outer/center stroke
        // grows only the drop shadow's silhouette, so it gets a fill-only mask of its own.
        let grown = stroke.as_ref().is_some_and(|s| s.align != "inner");
        let inner_mask = mask.as_ref().map(|m| {
            if !grown || !fx.shadows.iter().any(|s| s.inner) {
                return m.to_string();
            }
            let id = format!("{m}i");
            let _ = write!(self.defs, r##"<path id="{id}" d="{}"{} fill="#000"/>"##, geo.d, geo.rule("fill-rule"));
            id
        });
        let inner_outline = if grown { round_outline(n, None) } else { outline };
        // A clip group is isolated, so a blended child would blend against transparent instead of the
        // frame's fill: such a frame paints its fill and inner shadows inside the clip group too.
        let clip = n.ty == "frame" && n.b("clip");
        let inside = clip
            && n.kids.iter().any(blends)
            && fills.iter().all(|p| p.blend.is_none())
            && fx.shadows.iter().all(|s| !s.inner || s.blend.is_none());
        if inside {
            self.open_clip(&geo);
        }
        let fills_id = (self.tag && !fills.is_empty()).then(|| self.id("f"));
        if let Some(id) = &fills_id {
            let _ = write!(self.out, r#"<g id="{id}">"#);
        }
        for p in &fills {
            let _ = write!(self.out, r#"<path d="{}"{}{}/>"#, geo.d, geo.rule("fill-rule"), p.attrs("fill"));
        }
        if fills_id.is_some() {
            self.out.push_str("</g>");
        }
        let base = self.base.clone();
        let unavailable = self::fills(n.p.get("fill")).iter().any(|f| {
            let u = f["url"].as_str().unwrap_or("");
            f["type"] == "image" && !crate::render::is_pending_url(u) && self.assets.image(u, base.as_deref()).is_none() && !self.assets.loading(u)
        });
        if unavailable {
            // An unavailable image is outlined with a 2 px grey stroke clipped to the shape.
            let c = self.id("c");
            let _ = write!(self.defs, r#"<clipPath id="{c}"><path d="{}"{}/></clipPath>"#, geo.d, geo.rule("clip-rule"));
            let _ = write!(self.out, r##"<path d="{}" fill="none" stroke="#808080" stroke-width="2" clip-path="url(#{c})"/>"##, geo.d);
        }
        if let Some(m) = &inner_mask {
            self.inner_shadows(fx, m, mb, inner_outline);
        }
        if !clip {
            self.stroke(n, &geo, stroke.as_ref());
        }
        if n.ty == "frame" {
            self.children(n, fills_id, (clip && !inside).then_some(&geo));
        }
        if inside {
            self.out.push_str("</g>");
        }
        if clip {
            self.stroke(n, &geo, stroke.as_ref());
        }
        if n.ty == "frame" && n.kids.is_empty() && n.p.get("slot").is_some_and(|s| !s.is_null()) {
            self.slot(n);
        }
    }

    /// Empty-slot marker: the frame inset 10 px (radii − 10), hatched with
    /// 1 px stripes every 5 px at 135° plus a 1 px outline; magenta in a component, purple in an
    /// instance. Below zoom 0.2 the thin stripes average to a flat 20 % tint.
    fn slot(&mut self, n: &RNode) {
        let (w, h) = (n.w - 20.0, n.h - 20.0);
        if w <= 0.0 || h <= 0.0 {
            return;
        }
        let color = if n.id.contains('/') || n.inst.is_some() { "#9580FF" } else { "#D480FF" };
        let mut inner = RNode::leaf(n.id.clone(), "rectangle", Map::new(), None);
        (inner.x, inner.y, inner.w, inner.h) = (n.x + 10.0, n.y + 10.0, w, h);
        inner.p.insert("cornerRadius".into(), radii(n).map(|r| (r - 10.0).max(0.0)).into_iter().collect());
        let d = rrect_d(inner.x, inner.y, w, h, radii(&inner).map(|r| (r, r)));
        // Stripe centres lie where the frame-local point projected on (−1, 1)/√2 is 5·(k + ½).
        let s = std::f64::consts::FRAC_1_SQRT_2;
        let proj = |x: f64, y: f64| (y - x) * s;
        let (lo, hi) = [(10.0, 10.0), (n.w - 10.0, 10.0), (10.0, n.h - 10.0), (n.w - 10.0, n.h - 10.0)]
            .iter()
            .fold((f64::MAX, f64::MIN), |(lo, hi), &(x, y)| (lo.min(proj(x, y)), hi.max(proj(x, y))));
        let reach = n.w + n.h;
        let mut stripes = String::new();
        for k in (lo / 5.0 - 0.5).floor() as i64..=(hi / 5.0).ceil() as i64 {
            let c = 5.0 * (k as f64 + 0.5);
            let (cx, cy) = (n.x - c * s, n.y + c * s);
            let _ = write!(stripes, "M{:.2} {:.2}L{:.2} {:.2}", cx - reach * s, cy - reach * s, cx + reach * s, cy + reach * s);
        }
        let clip = self.id("c");
        let _ = write!(self.defs, r#"<clipPath id="{clip}"><path d="{d}"/></clipPath>"#);
        let _ = write!(self.out, r#"<path d="{stripes}" fill="none" stroke="{color}" stroke-width="1" clip-path="url(#{clip})"/><path d="{d}" fill="none" stroke="{color}" stroke-width="1"/>"#);
    }

    fn group(&mut self, n: &RNode, fx: &Effects, outline: Option<&str>) {
        let kids = outline.map(|m| format!("{m}k"));
        // The children's strokes, shadows and blur paint outside their boxes and shape the group's outline.
        let b = n.kids.iter().map(visual_bounds).fold(extent(n), union);
        let mask = outline.zip(kids.as_ref()).map(|(m, k)| {
            // The children's outline in solid black (the union of their masks).
            let f = self.id("f");
            let (x, y, w, h) = inflate(b, 2.0);
            let _ = write!(
                self.defs,
                r##"<filter id="{f}" filterUnits="userSpaceOnUse" x="{x}" y="{y}" width="{w}" height="{h}"><feFlood flood-color="#000"/><feComposite in2="SourceAlpha" operator="in"/></filter><g id="{m}" filter="url(#{f})"><use xlink:href="#{k}"/></g>"##
            );
            m
        });
        if let Some(m) = &mask {
            self.drop_shadows(fx, m, b, None);
        }
        if let Some(k) = &kids {
            let _ = write!(self.out, r#"<g id="{k}">"#);
        }
        self.children(n, None, None);
        if kids.is_some() {
            self.out.push_str("</g>");
        }
        if let Some(m) = &mask {
            self.inner_shadows(fx, m, b, None);
        }
    }

    /// Text and icons: every fill paints the glyphs; shadows use the glyph outline.
    fn glyphs(&mut self, n: &RNode, fx: &Effects, outline: Option<&str>) {
        let text = n.ty == "text";
        // Text takes a stroke like any shape; icons draw their own strokes.
        let stroke = if text { self.stroke_of(n, &Geo { d: String::new(), evenodd: false, closed: true }) } else { None };
        let reach = stroke.as_ref().map_or(0.0, Stroke::reach);
        let b = if text { inflate(own_box(n), TextStyle::of(n).size / 2.0 + reach) } else { (n.x, n.y, n.w, n.h) };
        let fills = if text { self.paints(n.p.get("fill"), (n.x, n.y, n.w, n.h)) } else { Vec::new() };
        let visible = fills.iter().any(|p| p.alpha > 0.0);
        let mask = outline.map(|m| {
            let black = [Paint { value: "#000".into(), alpha: 1.0, blend: None }];
            // Like shapes, glyphs without a visible fill are no outline: stroke-only text casts its stroke.
            let mut body = if !text {
                self.icon_markup(n, Some(&black))
            } else if visible {
                self.text_markup(n, &black)
            } else {
                String::new()
            };
            match &stroke {
                Some(s) if !visible => body += &self.text_stroke(n, s, &black),
                // Over the fill an unclipped stroke adds the same outline; an inner one adds nothing.
                Some(s) if s.align != "inner" => body += &self.text_stroke_markup(n, s, &black, ""),
                _ => {}
            }
            let _ = write!(self.defs, r#"<g id="{m}">{body}</g>"#);
            m
        });
        if let Some(m) = &mask {
            self.drop_shadows(fx, m, b, None);
        }
        let body = if text { self.text_markup(n, &fills) } else { self.icon_markup(n, None) };
        self.out.push_str(&body);
        if let Some(m) = &mask {
            self.inner_shadows(fx, m, b, None);
        }
        if let Some(s) = &stroke {
            let body = self.text_stroke(n, s, &s.paints);
            self.out.push_str(&body);
        }
    }

    /// Opens a group clipped to `g`; the caller closes it.
    fn open_clip(&mut self, g: &Geo) {
        let id = self.id("c");
        let _ = write!(self.defs, r#"<clipPath id="{id}"><path d="{}"{}/></clipPath>"#, g.d, g.rule("clip-rule"));
        let _ = write!(self.out, r#"<g clip-path="url(#{id})">"#);
    }

    fn children(&mut self, n: &RNode, fills_id: Option<String>, clip: Option<&Geo>) {
        if n.kids.is_empty() {
            return;
        }
        self.painted.push((!transform(n).is_empty(), fills_id.into_iter().collect()));
        if let Some(g) = clip {
            self.open_clip(g);
        }
        for k in &n.kids {
            self.node(k);
        }
        if clip.is_some() {
            self.out.push_str("</g>");
        }
        self.painted.pop();
    }

    /// Drop shadows of the black outline `m`, knocked out where the outline itself is.
    /// With a rounded-rect `outline` (box, radius) the group is tagged for analytic shadows (raster.rs).
    fn drop_shadows(&mut self, fx: &Effects, m: &str, b: Rect, outline: Option<(Rect, f64)>) {
        let drops: Vec<&Shadow> = fx.shadows.iter().filter(|s| !s.inner).collect();
        if drops.is_empty() {
            return;
        }
        let region = drops.iter().fold(b, |r, s| union(r, inflate((b.0 + s.dx, b.1 + s.dy, b.2, b.3), 3.0 * s.sigma + 1.0)));
        let (x, y, w, h) = region;
        let ko = self.id("k");
        let _ = write!(self.defs, r##"<mask id="{ko}" maskUnits="userSpaceOnUse" x="{x}" y="{y}" width="{w}" height="{h}"><rect x="{x}" y="{y}" width="{w}" height="{h}" fill="#fff"/><use xlink:href="#{m}"/></mask>"##);
        let tag = outline.filter(|_| drops.iter().all(|s| s.blend.is_none())).map(|o| shadow_tag('d', &ko, o, &drops));
        let _ = write!(self.out, r#"<g{} mask="url(#{ko})">"#, tag.map(|t| format!(r#" id="{t}""#)).unwrap_or_default());
        for s in drops {
            let f = self.id("s");
            let src = if s.sigma > 0.0 { format!(r#"<feGaussianBlur in="SourceAlpha" stdDeviation="{}"/><feOffset"#, s.sigma) } else { r#"<feOffset in="SourceAlpha""#.to_string() };
            let _ = write!(
                self.defs,
                r#"<filter id="{f}" filterUnits="userSpaceOnUse" x="{x}" y="{y}" width="{w}" height="{h}" color-interpolation-filters="sRGB">{src} dx="{}" dy="{}" result="o"/><feFlood flood-color="{}" flood-opacity="{}"/><feComposite in2="o" operator="in"/></filter>"#,
                s.dx, s.dy, s.color, s.alpha
            );
            let style = s.blend.map(|m| format!(r#" style="mix-blend-mode:{m}""#)).unwrap_or_default();
            let _ = write!(self.out, r##"<use xlink:href="#{m}" filter="url(#{f})"{style}/>"##);
        }
        self.out.push_str("</g>");
    }

    /// Inner shadows: the offset, blurred inverse of the outline, kept inside the outline.
    fn inner_shadows(&mut self, fx: &Effects, m: &str, b: Rect, outline: Option<(Rect, f64)>) {
        for s in fx.shadows.iter().filter(|s| s.inner) {
            let (x, y, w, h) = inflate(b, s.dx.abs() + s.dy.abs() + 3.0 * s.sigma + 2.0);
            let f = self.id("s");
            let blur = if s.sigma > 0.0 { format!(r#"<feGaussianBlur stdDeviation="{}"/>"#, s.sigma) } else { String::new() };
            let _ = write!(
                self.defs,
                r#"<filter id="{f}" filterUnits="userSpaceOnUse" x="{x}" y="{y}" width="{w}" height="{h}" color-interpolation-filters="sRGB"><feFlood flood-color="{}" flood-opacity="{}"/><feComposite in2="SourceAlpha" operator="out"/><feOffset dx="{}" dy="{}"/>{blur}<feComposite in2="SourceAlpha" operator="in"/></filter>"#,
                s.color, s.alpha, s.dx, s.dy
            );
            let style = s.blend.map(|m| format!(r#" style="mix-blend-mode:{m}""#)).unwrap_or_default();
            let tag = outline.filter(|_| s.blend.is_none()).map(|o| format!(r#" id="{}""#, shadow_tag('i', &f, o, &[s])));
            let _ = write!(self.out, r##"<use{} xlink:href="#{m}" filter="url(#{f})"{style}/>"##, tag.unwrap_or_default());
        }
    }

    /// Background blur through the node's outline `m`. The canvas renderer (raster.rs) draws it from
    /// the `nrbb:{uid}:{sigma}` tag: whatever is painted beneath, blurred. resvg cannot
    /// read the canvas, so the group's content approximates the backdrop with what this top-level
    /// node already painted (the parent's fill and earlier siblings, climbing through ancestors that
    /// are not rotated/flipped); other top-level nodes behind it are missing there (exports).
    fn backdrop_blur(&mut self, n: &RNode, tf: &str, sigma: f64, m: &str) {
        let mut items: Vec<String> = Vec::new();
        for (transformed, ids) in self.painted.iter().rev() {
            items.splice(0..0, ids.iter().cloned());
            if *transformed {
                break;
            }
        }
        let (b, f) = (self.id("b"), self.id("f"));
        let l = crate::layout::local_aabb(n);
        let (px, py) = (n.x - n.bx, n.y - n.by);
        let aabb = (px + l[0], py + l[1], l[2] - l[0], l[3] - l[1]);
        let area = match n.ty.as_str() {
            "group" => union(aabb, extent(n)),
            "text" => union(aabb, turn(n, own_box(n))),
            _ => aabb,
        };
        // The outline's reach past the box: outer strokes, glyph overhang.
        let reach = max_stroke(n) + if n.ty == "text" { TextStyle::of(n).size } else { 0.0 } + 1.0;
        let (mx, my, mw, mh) = inflate(area, reach);
        let (x, y, w, h) = inflate((mx, my, mw, mh), 3.0 * sigma);
        let t = if tf.is_empty() { String::new() } else { format!(r#" transform="{tf}""#) };
        let _ = write!(
            self.defs,
            r##"<mask id="{b}" mask-type="alpha" maskUnits="userSpaceOnUse" x="{mx}" y="{my}" width="{mw}" height="{mh}"><use xlink:href="#{m}"{t}/></mask><filter id="{f}" filterUnits="userSpaceOnUse" x="{x}" y="{y}" width="{w}" height="{h}" color-interpolation-filters="sRGB"><feGaussianBlur stdDeviation="{sigma}"/></filter>"##
        );
        let uses: String = items.iter().map(|i| format!(r##"<use xlink:href="#{i}"/>"##)).collect();
        let _ = write!(self.out, r#"<g id="nrbb:{b}:{sigma}" mask="url(#{b})"><g filter="url(#{f})">{uses}</g></g>"#);
    }

    fn stroke_of(&mut self, n: &RNode, geo: &Geo) -> Option<Stroke> {
        let widths = match n.p.get("strokeWidth")? {
            Value::Object(s) => {
                let g = |k: &str| s.get(k).and_then(num).unwrap_or(0.0).max(0.0);
                [g("top"), g("right"), g("bottom"), g("left")]
            }
            v => [num(v).unwrap_or(0.0).max(0.0); 4],
        };
        let rect = matches!(n.ty.as_str(), "frame" | "rectangle");
        // Non-rect shapes only use the top width.
        let widths = if rect { widths } else { [widths[0]; 4] };
        if widths.iter().all(|w| *w <= 0.0) {
            return None;
        }
        let align = match n.s("strokeAlignment") {
            _ if !geo.closed => "center",
            Some("inner" | "inside") => "inner",
            Some("outer" | "outside") => "outer",
            _ => "center",
        };
        let mut s = Stroke { paints: Vec::new(), widths, align };
        // Inner strokes stay inside the box; a miter tip past the pad repeats the pattern edge (accepted).
        self.pad = if s.align == "inner" { 0.0 } else { s.reach() + 2.0 };
        s.paints = self.paints(n.p.get("stroke"), (n.x, n.y, n.w, n.h));
        self.pad = 0.0;
        (!s.paints.is_empty()).then_some(s)
    }

    /// Stroke outline painted with `paints`, unclipped. Rects/frames build a per-side band
    /// (filled); other shapes use an SVG stroke of k·width. `extra` is added to every path.
    fn stroke_body(&mut self, n: &RNode, geo: &Geo, s: &Stroke, paints: &[Paint], extra: &str) -> String {
        let k = s.k();
        let mut out = String::new();
        if matches!(n.ty.as_str(), "frame" | "rectangle") {
            let d = rect_stroke_d(n, s.widths.map(|w| w * k), s.align != "outer", n.s("strokeLinejoin") == Some("round"));
            for p in paints {
                let _ = write!(out, r#"<path d="{d}" fill-rule="evenodd"{}{extra}/>"#, p.attrs("fill"));
            }
        } else {
            let mut attrs = format!(r#" fill="none" stroke-width="{}""#, s.widths[0] * k);
            for (prop, attr) in [("strokeLinecap", "stroke-linecap"), ("strokeLinejoin", "stroke-linejoin")] {
                if let Some(v) = n.s(prop) {
                    let _ = write!(attrs, r#" {attr}="{}""#, esc(v));
                }
            }
            for p in paints {
                let _ = write!(out, r#"<path d="{}"{attrs}{}{extra}/>"#, geo.d, p.attrs("stroke"));
            }
        }
        out
    }

    /// Inner/outer strokes are double-width strokes clipped to inside/outside the fill.
    fn stroke(&mut self, n: &RNode, geo: &Geo, s: Option<&Stroke>) {
        let Some(s) = s else { return };
        let blended = s.paints.iter().any(|p| p.blend.is_some());
        let clip = match s.align {
            "inner" => {
                let id = self.id("c");
                let _ = write!(self.defs, r#"<clipPath id="{id}"><path d="{}"{}/></clipPath>"#, geo.d, geo.rule("clip-rule"));
                format!(r#" clip-path="url(#{id})""#)
            }
            _ => String::new(),
        };
        // A clip group is isolated, so a blended stroke would blend against transparent: clip each path instead.
        let body = self.stroke_body(n, geo, s, &s.paints, if blended { &clip } else { "" });
        match s.align {
            "inner" if blended => self.out.push_str(&body),
            "inner" => {
                let _ = write!(self.out, r#"<g{clip}>{body}</g>"#);
            }
            "outer" => {
                let id = self.id("k");
                let (x, y, w, h) = inflate((n.x, n.y, n.w, n.h), s.reach() + 2.0);
                let _ = write!(
                    self.defs,
                    r##"<mask id="{id}" maskUnits="userSpaceOnUse" x="{x}" y="{y}" width="{w}" height="{h}"><rect x="{x}" y="{y}" width="{w}" height="{h}" fill="#fff"/><path d="{}"{} fill="#000"/></mask>"##,
                    geo.d,
                    geo.rule("fill-rule")
                );
                let _ = write!(self.out, r#"<g mask="url(#{id})">{body}</g>"#);
            }
            _ => self.out.push_str(&body),
        }
    }

    /// Glyph outlines stroked with `paints`, unclipped: width k·w, so inner/outer strokes
    /// are double width and `text_stroke` clips them. `more` is added to every `<text>`.
    fn text_stroke_markup(&mut self, n: &RNode, s: &Stroke, paints: &[Paint], more: &str) -> String {
        let mut extra = format!(r#" fill="none" stroke-width="{}"{more}"#, s.widths[0] * s.k());
        for (prop, attr) in [("strokeLinecap", "stroke-linecap"), ("strokeLinejoin", "stroke-linejoin")] {
            if let Some(v) = n.s(prop) {
                let _ = write!(extra, r#" {attr}="{}""#, esc(v));
            }
        }
        let st = TextStyle::of(n);
        self.lines_with(n, &st, &n.lines, (n.x, n.y, n.w, n.h), paints, "stroke", &extra)
    }

    /// Text stroke painted with `paints`; inner/outer keep the half of the double-width stroke inside/outside the glyphs.
    fn text_stroke(&mut self, n: &RNode, s: &Stroke, paints: &[Paint]) -> String {
        if s.align == "center" {
            return self.text_stroke_markup(n, s, paints, "");
        }
        let id = self.id("k");
        let (x, y, w, h) = inflate(own_box(n), TextStyle::of(n).size / 2.0 + s.reach() + 2.0);
        let inner = s.align == "inner";
        let glyphs = self.text_markup(n, &[Paint { value: if inner { "#fff" } else { "#000" }.into(), alpha: 1.0, blend: None }]);
        let rect = if inner { String::new() } else { format!(r##"<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="#fff"/>"##) };
        let _ = write!(self.defs, r#"<mask id="{id}" maskUnits="userSpaceOnUse" x="{x}" y="{y}" width="{w}" height="{h}">{rect}{glyphs}</mask>"#);
        // A mask group is isolated, so a blended stroke would blend against transparent: mask each text instead.
        if paints.iter().any(|p| p.blend.is_some()) {
            return self.text_stroke_markup(n, s, paints, &format!(r#" mask="url(#{id})""#));
        }
        let body = self.text_stroke_markup(n, s, paints, "");
        format!(r#"<g mask="url(#{id})">{body}</g>"#)
    }

    /// Paints of a `fill`/`stroke` value. Gradients and images span the node box `b`
    /// (not the painted outline).
    fn paints(&mut self, v: Option<&Value>, b: Rect) -> Vec<Paint> {
        let mut out = Vec::new();
        for f in fills(v) {
            let blend = blend_mode(f["blendMode"].as_str());
            let (value, alpha) = match f {
                Value::String(s) => match color(s) {
                    Some(c) => c,
                    None => continue,
                },
                Value::Object(o) => {
                    let opacity = o.get("opacity").and_then(num).unwrap_or(1.0);
                    match o.get("type").and_then(Value::as_str).unwrap_or("color") {
                        "color" => match o.get("color").and_then(Value::as_str).and_then(color) {
                            Some(c) => c,
                            None => continue,
                        },
                        "gradient" => match self.gradient(o, b) {
                            Some(id) => (format!("url(#{id})"), opacity),
                            None => continue,
                        },
                        "image" => {
                            let url = o.get("url").and_then(Value::as_str).unwrap_or("");
                            match self.assets.image(url, self.base.as_deref()) {
                                Some(href) => {
                                    let aspect = match o.get("mode").and_then(Value::as_str) {
                                        Some("fill") => "xMidYMid slice",
                                        Some("fit") => "xMidYMid meet",
                                        _ => "none",
                                    };
                                    let id = self.id("p");
                                    let (x, y, w, h) = b;
                                    let _ = write!(
                                        self.defs,
                                        r#"<pattern id="{id}" patternUnits="userSpaceOnUse" x="{x}" y="{y}" width="{w}" height="{h}"><image xlink:href="{}" width="{w}" height="{h}" preserveAspectRatio="{aspect}"/></pattern>"#,
                                        esc(&href)
                                    );
                                    (format!("url(#{id})"), opacity)
                                }
                                // Still generating or downloading: nothing is drawn (a generated image shows a shimmer).
                                None if crate::render::is_pending_url(url) || self.assets.loading(url) => continue,
                                // Unavailable image: an 8 px white/black checkerboard at
                                // 10 % from the node's corner (`shape` adds the grey inner outline).
                                None => {
                                    let id = self.id("p");
                                    let _ = write!(
                                        self.defs,
                                        r##"<pattern id="{id}" patternUnits="userSpaceOnUse" x="{}" y="{}" width="16" height="16"><rect width="16" height="16" fill="#fff"/><rect x="8" width="8" height="8" fill="#000"/><rect y="8" width="8" height="8" fill="#000"/></pattern>"##,
                                        b.0, b.1
                                    );
                                    (format!("url(#{id})"), 0.1)
                                }
                            }
                        }
                        "mesh_gradient" => match self.raster_pattern(b, |w, h| crate::raster::mesh_gradient(o, w, h)) {
                            Some(id) => (format!("url(#{id})"), opacity),
                            None => continue,
                        },
                        "shader" => (format!("url(#{})", self.shader(o, b)), opacity),
                        _ => continue,
                    }
                }
                _ => continue,
            };
            out.push(Paint { value, alpha, blend });
        }
        out
    }

    /// Gradient geometry in bbox units: rotation (CCW) happens before the non-uniform W×H scale.
    fn gradient(&mut self, o: &Map<String, Value>, b: Rect) -> Option<String> {
        let mut stops: Vec<(f64, [f64; 4])> = o.get("colors")?.as_array()?.iter().map(|c| {
            (num(&c["position"]).unwrap_or(0.0).clamp(0.0, 1.0), c["color"].as_str().and_then(rgba).unwrap_or([0.0; 4]))
        }).collect();
        if stops.is_empty() {
            return None;
        }
        stops.sort_by(|a, b| a.0.total_cmp(&b.0));
        let pt = |k: &str, a: &str, d: f64| o.get(k).and_then(|v| v.get(a)).and_then(num).unwrap_or(d);
        let (cx, cy) = (pt("center", "x", 0.5), pt("center", "y", 0.5));
        let half = |v: f64| if v == 0.0 { 1e-6 } else { v / 2.0 };
        let (rx, ry) = (half(pt("size", "width", 1.0)), half(pt("size", "height", 1.0)));
        let phi = -o.get("rotation").and_then(num).unwrap_or(0.0).to_radians();
        let (cs, sn) = (phi.cos(), phi.sin());
        let up = (ry * sn, -ry * cs);
        let right = (rx * cs, rx * sn);
        let (bx, by, bw, bh) = b;
        let id = self.id("g");
        // A line's box has no height (or width): a box-sized gradientTransform is then singular and
        // browsers paint nothing, so such a box gets its end points in user space instead.
        let flat = bw.abs() < 1e-6 || bh.abs() < 1e-6;
        // Rounds away float leftovers (cos 90° = 6e-17) and negative zeros.
        let n = |v: f64| (v * 1e9).round() / 1e9 + 0.0;
        match o.get("gradientType").and_then(Value::as_str) {
            Some("angular") => return self.conic(&stops, (cx, cy), up, right, b),
            Some("radial") => {
                let m = [bw * right.0, bh * right.1, -bw * up.0, -bh * up.1, bx + bw * cx, by + bh * cy];
                let _ = if flat {
                    // The ellipse collapses to a segment; a circle over its half-length stands in. Both axes
                    // lie along the line, so their surviving components combine.
                    let r = if bw.abs() >= bh.abs() { m[0].hypot(m[2]) } else { m[1].hypot(m[3]) };
                    write!(
                        self.defs,
                        r#"<radialGradient id="{id}" cx="{}" cy="{}" r="{}" gradientUnits="userSpaceOnUse">{}</radialGradient>"#,
                        n(m[4]), n(m[5]), n(r), stop_tags(&stops)
                    )
                } else {
                    write!(
                        self.defs,
                        r#"<radialGradient id="{id}" cx="0" cy="0" r="1" gradientUnits="userSpaceOnUse" gradientTransform="matrix({} {} {} {} {} {})">{}</radialGradient>"#,
                        n(m[0]), n(m[1]), n(m[2]), n(m[3]), n(m[4]), n(m[5]), stop_tags(&stops)
                    )
                };
            }
            _ => {
                let _ = if flat {
                    write!(
                        self.defs,
                        r#"<linearGradient id="{id}" x1="{}" y1="{}" x2="{}" y2="{}" gradientUnits="userSpaceOnUse">{}</linearGradient>"#,
                        n(bx + bw * (cx - up.0)), n(by + bh * (cy - up.1)), n(bx + bw * (cx + up.0)), n(by + bh * (cy + up.1)),
                        stop_tags(&stops)
                    )
                } else {
                    write!(
                        self.defs,
                        r#"<linearGradient id="{id}" x1="{}" y1="{}" x2="{}" y2="{}" gradientUnits="userSpaceOnUse" gradientTransform="matrix({} 0 0 {} {} {})">{}</linearGradient>"#,
                        n(cx - up.0), n(cy - up.1), n(cx + up.0), n(cy + up.1), n(bw), n(bh), n(bx), n(by),
                        stop_tags(&stops)
                    )
                };
            }
        }
        Some(id)
    }

    /// Angular gradient: SVG has no conic gradient, so it is rasterized into an image pattern over the
    /// node box grown by `pad` (angle 0 points "up" through the gradient frame, clockwise on screen).
    fn conic(&mut self, stops: &[(f64, [f64; 4])], c: (f64, f64), up: (f64, f64), right: (f64, f64), b: Rect) -> Option<String> {
        let det = up.0 * right.1 - up.1 * right.0;
        if det.abs() < 1e-12 || b.2 <= 0.0 || b.3 <= 0.0 {
            return None;
        }
        let g = inflate(b, self.pad);
        self.raster_pattern(g, |pw, ph| {
            let mut pm = tiny_skia::Pixmap::new(pw, ph)?;
            for (i, px) in pm.pixels_mut().iter_mut().enumerate() {
                let (x, y) = (g.0 + ((i as u32 % pw) as f64 + 0.5) / pw as f64 * g.2, g.1 + ((i as u32 / pw) as f64 + 0.5) / ph as f64 * g.3);
                let (dx, dy) = ((x - b.0) / b.2 - c.0, (y - b.1) / b.3 - c.1);
                let (u, v) = ((dx * right.1 - dy * right.0) / det, (up.0 * dy - up.1 * dx) / det);
                let t = v.atan2(u).rem_euclid(std::f64::consts::TAU) / std::f64::consts::TAU;
                let k = sample(stops, t).map(|x| (x.clamp(0.0, 1.0) * 255.0).round() as u8);
                *px = tiny_skia::ColorU8::from_rgba(k[0], k[1], k[2], k[3]).premultiply();
            }
            Some(pm)
        })
    }

    /// An image pattern over the node box `b` of the pixels `draw(width, height)` makes.
    fn raster_pattern(&mut self, b: Rect, draw: impl FnOnce(u32, u32) -> Option<tiny_skia::Pixmap>) -> Option<String> {
        let (bx, by, bw, bh) = b;
        if bw <= 0.0 || bh <= 0.0 {
            return None;
        }
        // ponytail: fixed raster (≤512 px, 2× small boxes); smooth enough except a conic seam or a mesh's hard outline at extreme zoom.
        let scale = (512.0 / bw.max(bh)).min(2.0);
        let pm = draw((bw * scale).ceil().max(1.0) as u32, (bh * scale).ceil().max(1.0) as u32)?;
        let png = pm.encode_png().ok()?;
        let id = self.id("p");
        let _ = write!(
            self.defs,
            r#"<pattern id="{id}" patternUnits="userSpaceOnUse" x="{bx}" y="{by}" width="{bw}" height="{bh}"><image width="{bw}" height="{bh}" preserveAspectRatio="none" xlink:href="data:image/png;base64,{}"/></pattern>"#,
            base64::engine::general_purpose::STANDARD.encode(png)
        );
        Some(id)
    }

    /// Shader fill: the GLSL file run over the node box (shader.rs) as an image pattern. When the file
    /// is missing or does not compile, an error hatch: 1-unit red stripes every 5 units at 135°.
    fn shader(&mut self, o: &Map<String, Value>, b: Rect) -> String {
        let (x, y, w, h) = b;
        let url = o.get("url").and_then(Value::as_str).unwrap_or("");
        let base = self.base.clone();
        let assets = &mut *self.assets;
        let src = assets.image(url, base.as_deref()).ok_or_else(|| format!("Shader file not found: \"{url}\"")).and_then(|p| std::fs::read_to_string(p).map_err(|e| e.to_string()));
        let outline = self.outline.take().unwrap_or(crate::shader::Outline::RRect([0.0; 4]));
        // `@backdrop`: what this top-level frame painted so far, over the light canvas colour.
        // ponytail: other top-level frames beneath are not included (raster.rs's background blur does that).
        let behind = src.as_ref().ok().filter(|s| crate::shader::uses_backdrop(s)).map(|_| {
            let open = self.out.matches("<g ").count() + self.out.matches("<g>").count();
            let close = "</g>".repeat(open.saturating_sub(self.out.matches("</g>").count()));
            format!(r##"<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="{w}" height="{h}" viewBox="{x} {y} {w} {h}"><rect x="{x}" y="{y}" width="{w}" height="{h}" fill="#f6f6f6"/><defs>{}</defs>{}{close}</svg>"##, self.defs, self.out)
        });
        let opts = resvg::usvg::Options { fontdb: std::sync::Arc::clone(&self.fonts.db), font_resolver: self.fonts.resolver(), resources_dir: base.clone(), image_href_resolver: image_href_resolver(), ..Default::default() };
        let png = src.and_then(|src| {
            let mut load = |u: &str| assets.image(u, base.as_deref()).and_then(|p| decode_image(&p));
            let mut paint = |pw: u32, ph: u32| {
                let tree = resvg::usvg::Tree::from_str(behind.as_deref()?, &opts).ok()?;
                let mut pm = tiny_skia::Pixmap::new(pw, ph)?;
                resvg::render(&tree, tiny_skia::Transform::from_scale(pw as f32 / w as f32, ph as f32 / h as f32), &mut pm.as_mut());
                Some(pm)
            };
            let key = behind.as_deref().map(fxhash);
            crate::shader::png(&src, o.get("uniforms").and_then(Value::as_object), w, h, &outline, &mut load, key.map(|k| (k, &mut paint as &mut crate::shader::Backdrop<'_>)))
        });
        self.outline = Some(outline);
        let id = self.id("p");
        match png {
            Ok(data) => {
                let _ = write!(
                    self.defs,
                    r#"<pattern id="{id}" patternUnits="userSpaceOnUse" x="{x}" y="{y}" width="{w}" height="{h}"><image width="{w}" height="{h}" preserveAspectRatio="none" xlink:href="data:image/png;base64,{data}"/></pattern>"#
                );
            }
            Err(e) => {
                eprintln!("shader fill {url}: {e}");
                let _ = write!(
                    self.defs,
                    r##"<pattern id="{id}" patternUnits="userSpaceOnUse" width="1" height="1" patternTransform="translate({x} {y}) rotate(135) scale(5)"><rect x="0.4" width="0.2" height="1" fill="#ff0000"/></pattern>"##
                );
            }
        }
        id
    }

    fn text_markup(&mut self, n: &RNode, paints: &[Paint]) -> String {
        let st = TextStyle::of(n);
        self.lines_markup(n, &st, &n.lines, (n.x, n.y, n.w, n.h), paints)
    }

    /// One `<text>` per paint; lines are positioned from our own measurement (text-anchor start),
    /// so alignment, justify and wrapping match the layout exactly.
    fn lines_markup(&mut self, n: &RNode, st: &TextStyle, lines: &[Line], b: Rect, paints: &[Paint]) -> String {
        self.lines_with(n, st, lines, b, paints, "fill", "")
    }

    /// `lines_markup` painting each `<text>` with `prop` (`fill` or `stroke`) plus the raw attributes `extra`.
    fn lines_with(&mut self, n: &RNode, st: &TextStyle, lines: &[Line], b: Rect, paints: &[Paint], prop: &str, extra: &str) -> String {
        if paints.is_empty() {
            return String::new();
        }
        let m = self.fonts.metrics(st);
        let (x, y, w, h) = b;
        let fixed = matches!(n.s("textGrowth"), Some("fixed-width" | "fixed-width-height"));
        let w = if fixed { w.ceil() } else { w };
        let total = lines.len() as f64 * m.line_h;
        let off = valign_off(n, h, total);
        let align = n.s("textAlign");
        let mut spans = String::new();
        for (i, l) in lines.iter().enumerate() {
            let free = w - l.width;
            // Centred/right lines sit as if they carried one more letter
            // spacing (SkParagraph's alignment), while left-aligned lines start at the box edge.
            let (dx, ws) = match align {
                Some("center") => ((free - st.letter_spacing) / 2.0, 0.0),
                Some("right") => (free - st.letter_spacing, 0.0),
                Some("justify") if l.soft => {
                    let gaps = l.text.chars().filter(|c| matches!(c, ' ' | '\u{a0}')).count();
                    (0.0, if gaps > 0 { free / gaps as f64 } else { 0.0 })
                }
                _ => (0.0, 0.0),
            };
            let ws = if ws != 0.0 { format!(r#" word-spacing="{ws}""#) } else { String::new() };
            // Baselines land on whole pixels. ponytail: snapped in document px, i.e. exact at
            // zoom/scale 1; other zooms can miss the device-px grid by < 0.5 device px.
            let base = (y + off + i as f64 * m.line_h + m.baseline).round();
            let _ = write!(spans, r#"<tspan x="{}" y="{base}"{ws}>{}</tspan>"#, x + dx, esc(&l.text));
        }
        // Underline/strikethrough are a Nalarupa extension.
        let deco: Vec<&str> = [("underline", "underline"), ("strikethrough", "line-through")].into_iter().filter(|(k, _)| n.b(k)).map(|(_, v)| v).collect();
        let deco = if deco.is_empty() { String::new() } else { format!(r#" text-decoration="{}""#, deco.join(" ")) };
        let ls = if st.letter_spacing != 0.0 { format!(r#" letter-spacing="{}""#, st.letter_spacing) } else { String::new() };
        let italic = if st.italic { r#" font-style="italic""# } else { "" };
        let mut out = String::new();
        for p in paints {
            let _ = write!(
                out,
                // `wght` = weight on every variable font (usvg skips 400, which leaves
                // fonts whose default master is lighter, e.g. Manrope, too thin) and nothing else:
                // optical size stays at the font's default.
                r#"<text font-family="'{}', 'Noto Sans', sans-serif" font-size="{}" font-weight="{}" font-variation-settings="'wght' {}" font-optical-sizing="none"{italic}{ls}{deco} xml:space="preserve"{extra}{}>{spans}</text>"#,
                esc(&st.family), st.size, st.weight, st.weight, p.attrs(prop)
            );
        }
        out
    }

    /// Icon drawn into the centered min(w, h) square, once per fill (no fill = invisible).
    fn icon_markup(&mut self, n: &RNode, paints: Option<&[Paint]>) -> String {
        let lib = n.s("library").unwrap_or("lucide").to_string();
        let name = n.s("icon").unwrap_or("").to_string();
        let weight = n.f("weight").unwrap_or(200.0);
        let src = self.assets.icon_weight(&lib, &name, weight);
        let parsed = src.as_deref().and_then(|src| {
            let start = src.find("<svg")?;
            let tag_end = start + src[start..].find('>')?;
            let inner_end = src.rfind("</svg>").unwrap_or(src.len());
            let tag = &src[start..tag_end];
            let attr = |name: &str| -> Option<String> {
                let k = format!(" {name}=\"");
                let i = tag.find(&k)? + k.len();
                Some(tag[i..i + tag[i..].find('"')?].to_string())
            };
            let vb: Vec<f64> = attr("viewBox").unwrap_or_else(|| "0 0 24 24".into()).split([' ', ',']).filter_map(|v| v.parse().ok()).collect();
            let vb = if vb.len() == 4 && vb[2] > 0.0 && vb[3] > 0.0 { [vb[0], vb[1], vb[2], vb[3]] } else { [0.0, 0.0, 24.0, 24.0] };
            let mut attrs = String::new();
            for k in ["fill", "stroke", "stroke-width", "stroke-linecap", "stroke-linejoin"] {
                if let Some(v) = attr(k) {
                    let _ = write!(attrs, r#" {k}="{}""#, esc(&v));
                }
            }
            if attr("fill").is_none() {
                attrs.push_str(r#" fill="currentColor""#);
            }
            Some((src[tag_end + 1..inner_end].to_string(), attrs, vb))
        });
        let Some((inner, attrs, [vx, vy, vw, vh])) = parsed else {
            // Still downloading: draw nothing, like a remote image that has not arrived yet.
            if src.is_none() && self.assets.icon_loading(&lib, &name, weight) {
                return String::new();
            }
            // Missing icon: dashed box in the first solid fill color.
            let c = self.paints(n.p.get("fill"), (n.x, n.y, n.w, n.h)).into_iter().find(|p| p.value.starts_with('#'));
            return c.map(|c| format!(r#"<rect x="{}" y="{}" width="{}" height="{}" rx="3" fill="none" stroke="{}" stroke-opacity="0.4" stroke-dasharray="2 2"/>"#, n.x, n.y, n.w, n.h, c.value)).unwrap_or_default();
        };
        let s = (n.w / vw).min(n.h / vh);
        if s <= 0.0 {
            return String::new();
        }
        let (tx, ty) = (n.x + (n.w - vw * s) / 2.0 - vx * s, n.y + (n.h - vh * s) / 2.0 - vy * s);
        // Paint servers are in the icon's own units, so map the node box into them.
        let own;
        let paints = match paints {
            Some(p) => p,
            None => {
                own = self.paints(n.p.get("fill"), ((n.x - tx) / s, (n.y - ty) / s, n.w / s, n.h / s));
                &own
            }
        };
        let mut out = String::new();
        for p in paints {
            let style = p.blend.map(|m| format!(r#" style="mix-blend-mode:{m}""#)).unwrap_or_default();
            let _ = write!(
                out,
                r#"<g transform="matrix({s} 0 0 {s} {tx} {ty})" opacity="{}"{style}{}>{}</g>"#,
                p.alpha,
                attrs.replace("currentColor", &p.value),
                inner.replace("currentColor", &p.value)
            );
        }
        out
    }

    fn note(&mut self, n: &RNode) {
        let (bg, fg, label) = match n.ty.as_str() {
            "prompt" => ("#e8eeff", "#1f2a5c", "Prompt"),
            "context" => ("#efeaff", "#33245c", "Context"),
            _ => ("#fff1a8", "#3d3418", "Note"),
        };
        let _ = write!(self.out, r#"<rect x="{}" y="{}" width="{}" height="{}" rx="8" fill="{bg}"/>"#, n.x, n.y, n.w, n.h);
        let st = crate::layout::note_style(n);
        let lines = if n.lines.is_empty() { vec![Line { text: label.into(), width: 0.0, soft: false }] } else { n.lines.clone() };
        let body = self.lines_markup(n, &st, &lines, (n.x + 12.0, n.y + 12.0, (n.w - 24.0).max(0.0), (n.h - 24.0).max(0.0)), &[Paint { value: fg.into(), alpha: 1.0, blend: None }]);
        self.out.push_str(&body);
    }

    fn placeholder(&mut self, n: &RNode) {
        let label = if n.ty == "browser" { format!("Browser · {}", n.s("url").unwrap_or("")) } else { format!("Script · {}", n.s("scriptUri").unwrap_or("")) };
        let _ = write!(self.out, r##"<rect x="{}" y="{}" width="{}" height="{}" rx="6" fill="#f4f4f5" stroke="#a1a1aa" stroke-dasharray="6 4"/>"##, n.x, n.y, n.w, n.h);
        let st = TextStyle { family: "Inter".into(), size: 13.0, weight: 500, italic: false, letter_spacing: 0.0, line_height: None };
        let lines = [Line { text: label, width: 0.0, soft: false }];
        let body = self.lines_markup(n, &st, &lines, (n.x + 12.0, n.y + 10.0, n.w, 20.0), &[Paint { value: "#52525b".into(), alpha: 1.0, blend: None }]);
        self.out.push_str(&body);
    }

    /// Script status view: a tinted box with a 1 px border and, centred
    /// and scaled down to fit inside 8 px margins, an icon, the title, the description and the code.
    fn script_status(&mut self, n: &RNode) {
        let s = &n.p["__status"];
        let error = s["kind"] == "error";
        let (bg, border, fg) = if error { ("#ff6666", "#ff6666", "#e52e2e") } else { ("#36b25f", "#36b25f", "#1f9947") };
        let _ = write!(self.out, r#"<rect x="{}" y="{}" width="{}" height="{}" fill="{bg}" fill-opacity="0.102"/>"#, n.x, n.y, n.w, n.h);
        let _ = write!(self.out, r#"<rect x="{}" y="{}" width="{}" height="{}" fill="none" stroke="{border}"/>"#, n.x + 0.5, n.y + 0.5, (n.w - 1.0).max(0.0), (n.h - 1.0).max(0.0));
        let style = |family: &str, size: f64, lh: f64| TextStyle { family: family.into(), size, weight: 500, italic: false, letter_spacing: 0.0, line_height: Some(lh / size) };
        let paint = |c: &str| [Paint { value: c.into(), alpha: 1.0, blend: None }];
        let mut p = Map::new();
        p.insert("textGrowth".into(), Value::from("fixed-width"));
        let left = RNode::leaf(String::new(), "text", p.clone(), None);
        p.insert("textAlign".into(), Value::from("center"));
        let centred = RNode::leaf(String::new(), "text", p, None);
        let icon = |d: &str, x: f64, y: f64| format!(r#"<g transform="translate({x} {y}) scale(0.6667)"><path d="{d}" fill="none" stroke="{fg}" stroke-width="2.25" stroke-linecap="round" stroke-linejoin="round"/></g>"#);
        let width = if error { 240.0 } else { 200.0 };
        let mut body = String::new();
        let mut y;
        let title = s["title"].as_str().unwrap_or("").to_string();
        if error {
            body += &icon("M2.586 16.726A2 2 0 0 1 2 15.312V8.688a2 2 0 0 1 .586-1.414l4.688-4.688A2 2 0 0 1 8.688 2h6.624a2 2 0 0 1 1.414.586l4.688 4.688A2 2 0 0 1 22 8.688v6.624a2 2 0 0 1-.586 1.414l-4.688 4.688a2 2 0 0 1-1.414.586H8.688a2 2 0 0 1-1.414-.586zM15 9l-6 6M9 9l6 6", width / 2.0 - 8.0, 0.0);
            y = 20.0;
            let st = style("Inter", 12.0, 16.0);
            let lines = self.fonts.wrap(&title, width, &st);
            body += &self.lines_markup(&centred, &st, &lines, (0.0, y, width, 0.0), &paint(fg));
            y += lines.len() as f64 * 16.0;
        } else {
            let st = style("Inter", 10.0, 10.0);
            let lines = self.fonts.wrap(&title, f64::INFINITY, &st);
            let tw = lines.iter().map(|l| l.width).fold(16.0, f64::max);
            body += &icon("m16 18 6-6-6-6M8 6l-6 6 6 6", width / 2.0 - 8.0, 4.0);
            body += &self.lines_markup(&centred, &st, &lines, (width / 2.0 - tw / 2.0, 24.0, tw, 0.0), &paint(fg));
            y = 38.0;
        }
        if let Some(d) = s["description"].as_str().filter(|d| !d.is_empty()) {
            let st = style("Inter", 10.0, 14.0);
            let lines = self.fonts.wrap(d, width, &st);
            body += &self.lines_markup(&centred, &st, &lines, (0.0, y + 4.0, width, 0.0), &paint(fg));
            y += 4.0 + lines.len() as f64 * 14.0;
        }
        if let Some(c) = s["code"].as_str().filter(|c| error && !c.is_empty()) {
            let st = style("JetBrains Mono", 10.0, 12.0);
            let lines = self.fonts.wrap(c, 224.0, &st);
            let h = 16.0 + lines.len() as f64 * 12.0;
            let _ = write!(body, r##"<rect x="0" y="{}" width="{width}" height="{h}" rx="2" fill="#141414"/>"##, y + 4.0);
            body += &self.lines_markup(&left, &st, &lines, (8.0, y + 12.0, 224.0, 0.0), &paint("#adadb2"));
            y += 4.0 + h;
        }
        let k = ((n.w - 16.0).max(0.0) / width).min((n.h - 16.0).max(0.0) / y.max(1.0)).min(1.0);
        if k > 0.0 {
            let _ = write!(self.out, r#"<g transform="translate({} {}) scale({k})">{body}</g>"#, n.x + (n.w - width * k) / 2.0, n.y + (n.h - y * k) / 2.0);
        }
    }
}

/// Pixels of an image file (premultiplied RGBA, top row first), decoded by resvg's own decoders.
/// The bytes go in as data (`text/plain` makes usvg sniff the format): usvg will not open a file
/// without an extension, which is how downloaded images are cached.
pub(crate) fn decode_image(path: &str) -> Option<tiny_skia::Pixmap> {
    let data = base64::engine::general_purpose::STANDARD.encode(std::fs::read(path).ok()?);
    let svg = format!(r#"<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="1" height="1"><image xlink:href="data:text/plain;base64,{data}"/></svg>"#);
    let opts = resvg::usvg::Options { image_href_resolver: image_href_resolver(), ..Default::default() };
    let tree = resvg::usvg::Tree::from_str(&svg, &opts).ok()?;
    let b = tree.root().abs_bounding_box();
    let mut pm = tiny_skia::Pixmap::new(b.width().round() as u32, b.height().round() as u32)?;
    resvg::render(&tree, tiny_skia::Transform::identity(), &mut pm.as_mut());
    Some(pm)
}

/// Enabled fills from a `fill`/`stroke` value (single fill or array).
pub fn fills(v: Option<&Value>) -> Vec<&Value> {
    match v {
        Some(Value::Array(a)) => a.iter().filter(|f| f["enabled"] != false).collect(),
        Some(f @ Value::String(_)) => vec![f],
        Some(f @ Value::Object(_)) if f["enabled"] != false => vec![f],
        _ => Vec::new(),
    }
}

/// Whether the node or a descendant paints with a blend mode (a fill, stroke or shadow).
pub fn blends(n: &RNode) -> bool {
    let paint = |k: &str| fills(n.p.get(k)).iter().any(|f| blend_mode(f["blendMode"].as_str()).is_some());
    paint("fill") || paint("stroke") || Effects::of(n).shadows.iter().any(|s| s.blend.is_some()) || n.kids.iter().any(blends)
}

/// Blend modes as CSS `mix-blend-mode`; linearBurn is multiply and
/// linearDodge (Skia plus) is approximated by screen, since resvg has no additive mode.
pub fn blend_mode(mode: Option<&str>) -> Option<&'static str> {
    Some(match mode? {
        "darken" => "darken",
        "multiply" | "linearBurn" => "multiply",
        "colorBurn" => "color-burn",
        "light" => "lighten",
        "screen" | "linearDodge" => "screen",
        "colorDodge" => "color-dodge",
        "overlay" => "overlay",
        "softLight" => "soft-light",
        "hardLight" => "hard-light",
        "difference" => "difference",
        "exclusion" => "exclusion",
        "hue" => "hue",
        "saturation" => "saturation",
        "color" => "color",
        "luminosity" => "luminosity",
        _ => return None,
    })
}

/// Gradient stops; where alpha changes, extra stops emulate Skia's premultiplied interpolation.
fn stop_tags(stops: &[(f64, [f64; 4])]) -> String {
    let mut out = String::new();
    let mut push = |p: f64, c: [f64; 4]| {
        let _ = write!(out, r#"<stop offset="{p}" stop-color="{}" stop-opacity="{}"/>"#, hex(c), c[3]);
    };
    for (i, &(p, c)) in stops.iter().enumerate() {
        if let Some(&(pp, pc)) = i.checked_sub(1).map(|j| &stops[j]) {
            if (pc[3] - c[3]).abs() > 1e-3 && p > pp {
                for k in 1..8 {
                    let t = k as f64 / 8.0;
                    push(pp + (p - pp) * t, mix(pc, c, t));
                }
            }
        }
        push(p, c);
    }
    out
}

/// Premultiplied interpolation, returned unpremultiplied.
fn mix(a: [f64; 4], b: [f64; 4], t: f64) -> [f64; 4] {
    let al = a[3] + (b[3] - a[3]) * t;
    if al <= 0.0 {
        return [0.0; 4];
    }
    let ch = |i: usize| (a[i] * a[3] * (1.0 - t) + b[i] * b[3] * t) / al;
    [ch(0), ch(1), ch(2), al]
}

/// Color at `t` with clamped ends.
fn sample(stops: &[(f64, [f64; 4])], t: f64) -> [f64; 4] {
    let i = stops.partition_point(|s| s.0 <= t);
    match (i.checked_sub(1).map(|j| stops[j]), stops.get(i)) {
        (Some(a), Some(b)) if b.0 > a.0 => mix(a.1, b.1, (t - a.0) / (b.0 - a.0)),
        (Some(a), _) => a.1,
        (None, Some(b)) => b.1,
        (None, None) => [0.0; 4],
    }
}

fn has_backdrop_blur(n: &RNode) -> bool {
    let own = match n.p.get("effect") {
        Some(Value::Array(a)) => a.iter().any(|e| e["type"] == "background_blur" && e["enabled"] != false),
        Some(e) => e["type"] == "background_blur" && e["enabled"] != false,
        None => false,
    };
    own || n.kids.iter().any(has_backdrop_blur)
}

/// The clip shape of a node that clips its children, as a one-shape SVG (opaque white) under the
/// matrix `m`: the mask of a sliding descendant, which the layer's own clip group cannot give
/// (the slide's snapshot moves, the clip does not).
pub fn clip_svg(n: &RNode, m: Option<[f64; 6]>) -> Option<String> {
    if !n.b("clip") || !(n.ty == "frame" || n.ty == "script" && !n.p.contains_key("__status")) {
        return None;
    }
    let g = geometry(n);
    let tf = m.map_or(String::new(), |[a, b, c, d, e, f]| format!(r#" transform="matrix({a} {b} {c} {d} {e} {f})""#));
    Some(format!(r##"<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"><path d="{}"{} fill="#fff"{tf}/></svg>"##, g.d, g.rule("fill-rule")))
}

/// The node has its own rotation or flip (`transform` is not empty).
pub fn is_turned(n: &RNode) -> bool {
    n.f("rotation").is_some_and(|r| r.abs() > 1e-6) || n.b("flipX") || n.b("flipY")
}

/// A node's own rotation (CCW) and flips about its origin, the matrix form of `transform`.
pub fn own_matrix(n: &RNode) -> [f64; 6] {
    let t = -n.f("rotation").unwrap_or(0.0).to_radians();
    let (sx, sy) = (if n.b("flipX") { -1.0 } else { 1.0 }, if n.b("flipY") { -1.0 } else { 1.0 });
    let (a, b, c, d) = (t.cos() * sx, t.sin() * sx, -t.sin() * sy, t.cos() * sy);
    let (px, py) = (n.x - n.bx, n.y - n.by);
    [a, b, c, d, px - a * px - c * py, py - b * px - d * py]
}

/// Rotation (CCW) and flips about the node's origin, as an SVG transform list (flip applies first).
fn transform(n: &RNode) -> String {
    let (px, py) = (n.x - n.bx, n.y - n.by);
    let mut tf = String::new();
    if let Some(r) = n.f("rotation").filter(|r| r.abs() > 1e-6) {
        let _ = write!(tf, "rotate({} {px} {py})", -r);
    }
    let (sx, sy) = (if n.b("flipX") { -1.0 } else { 1.0 }, if n.b("flipY") { -1.0 } else { 1.0 });
    if sx < 0.0 || sy < 0.0 {
        let _ = write!(tf, " matrix({sx} 0 0 {sy} {} {})", px * (1.0 - sx), py * (1.0 - sy));
    }
    tf.trim_start().to_string()
}

/// Shadow source of a frame/rectangle as (box, radius) when it is one uniformly rounded rect: the
/// fill outline grown by an outer/center stroke the way `rect_stroke_d` grows it.
fn round_outline(n: &RNode, stroke: Option<&Stroke>) -> Option<(Rect, f64)> {
    let r = radii(n);
    if !matches!(n.ty.as_str(), "frame" | "rectangle") || r.iter().any(|v| (v - r[0]).abs() > 1e-9) {
        return None;
    }
    let Some(s) = stroke.filter(|s| s.align != "inner") else { return Some(((n.x, n.y, n.w, n.h), r[0])) };
    if s.widths.iter().any(|w| (w - s.widths[0]).abs() > 1e-9) {
        return None;
    }
    let e = s.reach();
    let r = if s.align != "outer" && r[0] < e { 0.0 } else { r[0] };
    let outer = if r > 0.0 { r + e } else if n.s("strokeLinejoin") == Some("round") { e } else { 0.0 };
    Some((inflate((n.x, n.y, n.w, n.h), e), outer))
}

/// Group id that lets raster.rs draw these shadows analytically:
/// `nrsh:{d|i}:{uid}:{x},{y},{w},{h},{radius}:{dx},{dy},{sigma},{color},{alpha};…`.
fn shadow_tag(kind: char, uid: &str, ((x, y, w, h), r): (Rect, f64), list: &[&Shadow]) -> String {
    let list: Vec<String> = list.iter().map(|s| format!("{},{},{},{},{}", s.dx, s.dy, s.sigma, s.color, s.alpha)).collect();
    format!("nrsh:{kind}:{uid}:{x},{y},{w},{h},{r}:{}", list.join(";"))
}

fn inflate(r: Rect, m: f64) -> Rect {
    (r.0 - m, r.1 - m, r.2 + 2.0 * m, r.3 + 2.0 * m)
}

fn union(a: Rect, b: Rect) -> Rect {
    let (x0, y0) = (a.0.min(b.0), a.1.min(b.1));
    (x0, y0, (a.0 + a.2).max(b.0 + b.2) - x0, (a.1 + a.3).max(b.1 + b.3) - y0)
}

/// Offset of a fixed-size text's lines from the top of its box (negative when they overflow it).
fn valign_off(n: &RNode, h: f64, total: f64) -> f64 {
    let fixed = matches!(n.s("textGrowth"), Some("fixed-width" | "fixed-width-height"));
    match n.s("textAlignVertical") {
        Some("middle") if fixed => (h - total) / 2.0,
        Some("bottom") if fixed => h - total,
        _ => 0.0,
    }
}

/// Box of a node; a text also covers the lines that overflow its fixed box.
fn own_box(n: &RNode) -> Rect {
    let own = (n.x, n.y, n.w, n.h);
    let total = n.lines.len() as f64 * n.line_h;
    if n.ty != "text" || total <= n.h {
        return own;
    }
    union(own, (n.x, n.y + valign_off(n, n.h, total), n.w, total))
}

/// Box of a node and everything it draws outside it (unclipped descendants, under their own
/// rotation and flips), in the node's own space.
fn extent(n: &RNode) -> Rect {
    let own = own_box(n);
    if n.ty == "frame" && n.b("clip") {
        return own;
    }
    n.kids.iter().map(|k| turn(k, extent(k))).fold(own, union)
}

/// Axis-aligned box of `r` (in the node's own space) after the node's rotation and flips, so it
/// matches what `transform` paints. Rounded to 1e-6 to drop trig noise (cos 90° ≠ 0), which would
/// add a pixel row on export.
fn turn(n: &RNode, r: Rect) -> Rect {
    if !is_turned(n) {
        return r;
    }
    let m = own_matrix(n);
    let (x1, y1) = (r.0 + r.2, r.1 + r.3);
    let mut o = [f64::MAX, f64::MAX, f64::MIN, f64::MIN];
    for (x, y) in [(r.0, r.1), (x1, r.1), (r.0, y1), (x1, y1)] {
        let (x, y) = (m[0] * x + m[2] * y + m[4], m[1] * x + m[3] * y + m[5]);
        o = [o[0].min(x), o[1].min(y), o[2].max(x), o[3].max(y)];
    }
    let [x0, y0, x1, y1] = o.map(|v| (v * 1e6).round() / 1e6);
    (x0, y0, x1 - x0, y1 - y0)
}

/// Visual bounds, the area an export covers: the box,
/// the stroke's outer part, the children of groups and unclipped frames, drop shadows
/// (offset ± 3σ, no spread) and layer blur (± 3σ), all under the node's own rotation and flips.
pub fn visual_bounds(n: &RNode) -> Rect {
    let w = if n.p.get("stroke").is_some() { max_stroke(n) } else { 0.0 };
    let out = match n.s("strokeAlignment") {
        Some("inner") => 0.0,
        Some("outer") => w,
        _ => w / 2.0,
    };
    let mut b = if n.ty == "group" {
        n.kids.iter().map(visual_bounds).reduce(union).unwrap_or((n.x, n.y, 0.0, 0.0))
    } else {
        inflate(own_box(n), out)
    };
    if n.ty == "frame" && !n.b("clip") {
        b = n.kids.iter().map(visual_bounds).fold(b, union);
    }
    let fx = Effects::of(n);
    let base = b;
    for s in fx.shadows.iter().filter(|s| !s.inner) {
        b = union(b, inflate((base.0 + s.dx, base.1 + s.dy, base.2, base.3), 3.0 * s.sigma));
    }
    turn(n, inflate(b, 3.0 * fx.blur))
}

fn max_stroke(n: &RNode) -> f64 {
    match n.p.get("strokeWidth") {
        Some(Value::Object(s)) => s.values().filter_map(num).fold(0.0, f64::max),
        Some(v) => num(v).unwrap_or(0.0),
        None => 0.0,
    }
}

/// Corner radii [tl, tr, br, bl], scaled down together when adjacent radii overflow a side (CSS rule).
pub fn radii(n: &RNode) -> [f64; 4] {
    let r = match n.p.get("cornerRadius") {
        Some(Value::Array(a)) if a.len() == 4 => {
            let g = |i: usize| num(&a[i]).unwrap_or(0.0);
            [g(0), g(1), g(2), g(3)]
        }
        Some(v) => [num(v).unwrap_or(0.0); 4],
        None => [0.0; 4],
    }
    .map(|v| v.max(0.0));
    let f = [n.w / (r[0] + r[1]), n.h / (r[1] + r[2]), n.w / (r[2] + r[3]), n.h / (r[3] + r[0])].into_iter().filter(|f| f.is_finite()).fold(1.0, f64::min);
    r.map(|v| v * f)
}

/// Rounded rect with per-corner elliptical radii [tl, tr, br, bl], clockwise.
fn rrect_d(x: f64, y: f64, w: f64, h: f64, r: [(f64, f64); 4]) -> String {
    let [tl, tr, br, bl] = r;
    if r.iter().all(|c| c.0 <= 0.0 || c.1 <= 0.0) {
        return format!("M{x} {y}H{}V{}H{x}Z", x + w, y + h);
    }
    format!(
        "M{} {y}H{}A{} {} 0 0 1 {} {}V{}A{} {} 0 0 1 {} {}H{}A{} {} 0 0 1 {x} {}V{}A{} {} 0 0 1 {} {y}Z",
        x + tl.0, x + w - tr.0, tr.0, tr.1, x + w, y + tr.1, y + h - br.1, br.0, br.1, x + w - br.0, y + h,
        x + bl.0, bl.0, bl.1, y + h - bl.1, y + tl.1, tl.0, tl.1, x + tl.0
    )
}

/// Rect stroke (per-side widths [t, r, b, l], already ×k): the band between the box grown and
/// shrunk by half widths. Inner corners are elliptical arcs ending where the original radius ends;
/// for inner/center strokes a radius smaller than half the adjacent widths becomes square.
fn rect_stroke_d(n: &RNode, w: [f64; 4], trim: bool, round_join: bool) -> String {
    let [t, r, b, l] = w.map(|v| v / 2.0);
    let adj = [(l, t), (r, t), (r, b), (l, b)];
    let rad = radii(n);
    let rad: [f64; 4] = std::array::from_fn(|i| if trim && rad[i] < adj[i].0.min(adj[i].1) { 0.0 } else { rad[i] });
    let outer = std::array::from_fn(|i| {
        let (h, v) = adj[i];
        if rad[i] > 0.0 {
            let o = rad[i] + h.min(v);
            (o, o)
        } else if round_join {
            (h, v)
        } else {
            (0.0, 0.0)
        }
    });
    let inner = std::array::from_fn(|i| if rad[i] > 0.0 { ((rad[i] - adj[i].0).max(0.0), (rad[i] - adj[i].1).max(0.0)) } else { (0.0, 0.0) });
    let mut d = rrect_d(n.x - l, n.y - t, n.w + l + r, n.h + t + b, outer);
    let (iw, ih) = (n.w - l - r, n.h - t - b);
    if iw > 0.0 && ih > 0.0 {
        d += &rrect_d(n.x + l, n.y + t, iw, ih, inner);
    }
    d
}

fn geometry(n: &RNode) -> Geo {
    let evenodd = n.s("fillRule") == Some("evenodd");
    match n.ty.as_str() {
        // Holes and arcs are drawn as nested contours, so the ellipse always needs evenodd.
        "ellipse" => Geo { d: ellipse_d(n), evenodd: true, closed: true },
        "polygon" => Geo { d: polygon_d(n), evenodd, closed: true },
        "path" => {
            let (d, closed) = path_geometry(n);
            Geo { d, evenodd, closed }
        }
        "line" => Geo { d: format!("M{} {}L{} {}", n.x, n.y, n.x + n.w, n.y + n.h), evenodd: false, closed: false },
        _ => Geo { d: rrect_d(n.x, n.y, n.w, n.h, radii(n).map(|r| (r, r))), evenodd: false, closed: true },
    }
}

fn ellipse_d(n: &RNode) -> String {
    let (cx, cy, rx, ry) = (n.x + n.w / 2.0, n.y + n.h / 2.0, n.w / 2.0, n.h / 2.0);
    let inner = n.f("innerRadius").unwrap_or(0.0).clamp(0.0, 1.0);
    let start = n.f("startAngle").unwrap_or(0.0);
    let sweep = n.f("sweepAngle").unwrap_or(360.0).clamp(-360.0, 360.0);
    let pt = |a: f64, k: f64| {
        let r = a.to_radians();
        (cx + rx * k * r.cos(), cy - ry * k * r.sin())
    };
    let full = |k: f64| {
        format!("M{} {cy}A{} {} 0 1 0 {} {cy}A{} {} 0 1 0 {} {cy}Z", cx + rx * k, rx * k, ry * k, cx - rx * k, rx * k, ry * k, cx + rx * k)
    };
    if sweep.abs() >= 359.9999 {
        return if inner > 0.0 { full(1.0) + &full(inner) } else { full(1.0) };
    }
    let end = start + sweep;
    let large = if sweep.abs() > 180.0 { 1 } else { 0 };
    let (sf_out, sf_in) = if sweep > 0.0 { (0, 1) } else { (1, 0) };
    let (p0, p1) = (pt(start, 1.0), pt(end, 1.0));
    let mut d = format!("M{} {}A{rx} {ry} 0 {large} {sf_out} {} {}", p0.0, p0.1, p1.0, p1.1);
    if inner > 0.0 {
        let (q1, q0) = (pt(end, inner), pt(start, inner));
        let _ = write!(d, "L{} {}A{} {} 0 {large} {sf_in} {} {}Z", q1.0, q1.1, rx * inner, ry * inner, q0.0, q0.1);
    } else {
        let _ = write!(d, "L{cx} {cy}Z");
    }
    d
}

/// Regular polygon on the inscribed ellipse (first vertex top-center, clockwise); `cornerRadius`
/// rounds every corner with a tangent arc. No `polygonCount` (or < 3) draws nothing; more than
/// `MAX_POLYGON_COUNT` is capped, since the value comes from the document.
fn polygon_d(n: &RNode) -> String {
    const MAX_POLYGON_COUNT: f64 = 1000.0;
    let Some(count) = n.f("polygonCount").filter(|c| *c >= 3.0).map(|c| c.min(MAX_POLYGON_COUNT) as usize) else { return String::new() };
    let (cx, cy, rx, ry) = (n.x + n.w / 2.0, n.y + n.h / 2.0, n.w / 2.0, n.h / 2.0);
    let pts: Vec<(f64, f64)> = (0..count).map(|i| {
        let a = (-90.0 + 360.0 * i as f64 / count as f64).to_radians();
        (cx + rx * a.cos(), cy + ry * a.sin())
    }).collect();
    let r = match n.p.get("cornerRadius") {
        Some(Value::Array(a)) => a.first().and_then(num),
        Some(v) => num(v),
        None => None,
    }.unwrap_or(0.0).max(0.0);
    let dist = |a: (f64, f64), b: (f64, f64)| (a.0 - b.0).hypot(a.1 - b.1);
    let min_edge = (0..count).map(|i| dist(pts[i], pts[(i + 1) % count])).fold(f64::MAX, f64::min);
    let f = r.min(min_edge / 2.0);
    let mut d = String::new();
    for i in 0..count {
        let (p0, p1, p2) = (pts[(i + count - 1) % count], pts[i], pts[(i + 1) % count]);
        let cmd = if i == 0 { "M" } else { "L" };
        let (l1, l2) = (dist(p0, p1), dist(p2, p1));
        if f <= 0.0 || l1 <= 0.0 || l2 <= 0.0 {
            let _ = write!(d, "{cmd}{} {}", p1.0, p1.1);
            continue;
        }
        let (v1, v2) = (((p0.0 - p1.0) / l1, (p0.1 - p1.1) / l1), ((p2.0 - p1.0) / l2, (p2.1 - p1.1) / l2));
        let theta = (v1.0 * v2.0 + v1.1 * v2.1).clamp(-1.0, 1.0).acos();
        let t = f / (theta / 2.0).tan();
        let (a, b) = ((p1.0 + v1.0 * t, p1.1 + v1.1 * t), (p1.0 + v2.0 * t, p1.1 + v2.1 * t));
        let _ = write!(d, "{cmd}{} {}A{f} {f} 0 0 1 {} {}", a.0, a.1, b.0, b.1);
    }
    d + "Z"
}

/// Path geometry mapped from its viewBox (default: the tight curve bounds) onto the node box.
/// Coordinates are transformed numerically so stroke widths are not scaled with the shape.
/// Also reports whether every subpath is closed (open paths always stroke centered).
fn path_geometry(n: &RNode) -> (String, bool) {
    let d = n.s("geometry").unwrap_or("").to_string();
    let vb = n.p.get("viewBox").and_then(Value::as_array).filter(|a| a.len() == 4).map(|a| [num(&a[0]).unwrap_or(0.0), num(&a[1]).unwrap_or(0.0), num(&a[2]).unwrap_or(1.0), num(&a[3]).unwrap_or(1.0)]);
    let vb = vb.or_else(|| path_bbox(&d).map(|b| [b.0, b.1, b.2, b.3])).unwrap_or([0.0, 0.0, 0.0, 0.0]);
    let [vx, vy, vw, vh] = vb;
    let sx = if vw > 0.0 { n.w / vw } else { 1.0 };
    let sy = if vh > 0.0 { n.h / vh } else { 1.0 };
    let (px, py) = (|x: f64| n.x + (x - vx) * sx, |y: f64| n.y + (y - vy) * sy);
    let mut out = String::with_capacity(d.len() * 2);
    let (mut closed, mut open) = (true, false);
    for seg in svgtypes::SimplifyingPathParser::from(d.as_str()) {
        let Ok(seg) = seg else { break };
        let _ = match seg {
            svgtypes::SimplePathSegment::MoveTo { x, y } => {
                closed &= !open;
                open = false;
                write!(out, "M{} {}", px(x), py(y))
            }
            svgtypes::SimplePathSegment::LineTo { x, y } => {
                open = true;
                write!(out, "L{} {}", px(x), py(y))
            }
            svgtypes::SimplePathSegment::CurveTo { x1, y1, x2, y2, x, y } => {
                open = true;
                write!(out, "C{} {} {} {} {} {}", px(x1), py(y1), px(x2), py(y2), px(x), py(y))
            }
            svgtypes::SimplePathSegment::Quadratic { x1, y1, x, y } => {
                open = true;
                write!(out, "Q{} {} {} {}", px(x1), py(y1), px(x), py(y))
            }
            svgtypes::SimplePathSegment::ClosePath => {
                open = false;
                write!(out, "Z")
            }
        };
    }
    (out, closed && !open)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use std::sync::Arc;

    fn node(ty: &str, p: Value, (x, y, w, h): Rect) -> RNode {
        let mut n = RNode::leaf("n".into(), ty, p.as_object().cloned().unwrap_or_default(), None);
        (n.x, n.y, n.w, n.h) = (x, y, w, h);
        n
    }

    /// An icon still downloading draws nothing, like a remote image that has not arrived; only an icon
    /// that does not exist gets the dashed "missing" box.
    #[test]
    fn a_loading_icon_draws_no_missing_box() {
        let dir = std::env::temp_dir().join("nalarupa-test-icon-loading");
        let _ = std::fs::remove_dir_all(&dir);
        let mut e = crate::engine::Engine::new(dir.clone());
        e.assets.online = false;
        let id = e.doc.insert(crate::doc::ROOT, json!({"type": "icon", "icon": "zap", "library": "lucide", "x": 0, "y": 0, "width": 24, "height": 24, "fill": "#cc0000"}), None).unwrap();
        let key = format!("{}/zap", icon_url("lucide", "zap", 200.0).1);
        e.assets.loading.insert(key.clone());
        let (svg, _) = e.ctx().node_svg(&id).unwrap();
        assert!(!svg.contains("stroke-dasharray"), "a loading icon draws no missing box: {svg}");
        e.assets.loading.remove(&key);
        let (svg, _) = e.ctx().node_svg(&id).unwrap();
        assert!(svg.contains("stroke-dasharray"), "a missing icon keeps its dashed box: {svg}");
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// Web images are cached with an extension sniffed from their bytes (resvg decodes by
    /// extension; extension-less files rendered blank). Old extension-less files are migrated.
    #[test]
    fn cached_web_images_get_an_extension() {
        let dir = std::env::temp_dir().join(format!("nalarupa-img-{:x}", crate::doc::random_u64()));
        let mut assets = Assets::new(dir.clone());
        assets.online = false;
        let url = "https://example.com/photo";
        let legacy = dir.join("images").join(format!("{:016x}", fxhash(url)));
        std::fs::write(&legacy, b"\x89PNG\r\n\x1a\n rest").unwrap();
        let path = assets.image(url, None).unwrap();
        assert!(path.ends_with(".png"), "{path}");
        assert!(!legacy.exists() && Path::new(&path).exists());
        assert_eq!(assets.image(url, None).unwrap(), path);
        assert_eq!(image_ext(&[0xFF, 0xD8, 0xFF]), "jpg");
        let _ = std::fs::remove_dir_all(dir);
    }

    /// A web image that is not cached downloads on a background thread: `image` answers at once (nothing
    /// drawn, not "unavailable") instead of waiting for the server, and the file is used once it lands.
    #[test]
    fn web_images_download_in_the_background() {
        use std::io::{Read, Write};
        let dir = std::env::temp_dir().join(format!("nalarupa-bg-{:x}", crate::doc::random_u64()));
        let mut assets = Assets::new(dir.clone());
        let server = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("http://{}/a.png", server.local_addr().unwrap());
        let (release, wait) = std::sync::mpsc::channel::<()>();
        std::thread::spawn(move || {
            let (mut s, _) = server.accept().unwrap();
            let _ = s.read(&mut [0; 1024]);
            let _ = wait.recv();
            let body = b"\x89PNG\r\n\x1a\n\0\0\0\rIHDR\0\0\0\x01\0\0\0\x01";
            let _ = s.write_all(format!("HTTP/1.1 200 OK\r\ncontent-length: {}\r\nconnection: close\r\n\r\n", body.len()).as_bytes());
            let _ = s.write_all(body);
        });
        let started = std::time::Instant::now();
        assert!(assets.image(&url, None).is_none() && assets.loading(&url));
        assert!(started.elapsed() < std::time::Duration::from_secs(1), "the call does not wait for the download");
        assert!(assets.image(&url, None).is_none() && assets.loading(&url), "asking again does not start another download");
        release.send(()).unwrap();
        let file = dir.join("images").join(format!("{:016x}.png", fxhash(&url)));
        let until = std::time::Instant::now() + std::time::Duration::from_secs(5);
        while !file.exists() && std::time::Instant::now() < until {
            std::thread::sleep(std::time::Duration::from_millis(20));
        }
        assets.downloaded(&url, true);
        assert!(!assets.loading(&url) && assets.generation == 1);
        assert!(assets.image(&url, None).is_some());
        // A failed download is not retried this session.
        let missing = "http://127.0.0.1:1/b.png";
        assets.downloaded(missing, false);
        assert!(assets.image(missing, None).is_none() && !assets.loading(missing));
        let _ = std::fs::remove_dir_all(dir);
    }

    /// A document with many missing assets does not start a thread for each: past MAX_DOWNLOADS the rest
    /// wait in the queue and start as running ones end.
    #[test]
    fn downloads_run_at_most_max_at_once() {
        let dir = std::env::temp_dir().join(format!("nalarupa-queue-{:x}", crate::doc::random_u64()));
        let mut assets = Assets::new(dir.clone());
        // Jobs block until the sender is dropped at the end, then count themselves as ended.
        let (hold, parked) = std::sync::mpsc::channel::<()>();
        let parked = std::sync::Arc::new(std::sync::Mutex::new(parked));
        let ended = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
        for i in 0..MAX_DOWNLOADS + 3 {
            let (parked, ended) = (parked.clone(), ended.clone());
            assets.spawn(format!("k{i}"), move || {
                let ok = parked.lock().unwrap().recv().is_ok();
                ended.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
                ok
            });
        }
        assert_eq!((assets.running, assets.queue.len()), (MAX_DOWNLOADS, 3));
        assert!((0..MAX_DOWNLOADS + 3).all(|i| assets.loading(&format!("k{i}"))), "queued keys count as loading");
        assets.downloaded("k0", true);
        assert_eq!((assets.running, assets.queue.len()), (MAX_DOWNLOADS, 2), "an ended download frees a slot for the next");
        // Release the parked jobs (MAX_DOWNLOADS + 1 started) and wait for them so no thread outlives the test.
        drop(hold);
        let until = std::time::Instant::now() + std::time::Duration::from_secs(5);
        while ended.load(std::sync::atomic::Ordering::SeqCst) < MAX_DOWNLOADS + 1 && std::time::Instant::now() < until {
            std::thread::sleep(std::time::Duration::from_millis(20));
        }
        assert_eq!(ended.load(std::sync::atomic::Ordering::SeqCst), MAX_DOWNLOADS + 1);
        let _ = std::fs::remove_dir_all(dir);
    }

    /// An image declaring more than MAX_IMAGE_PIXELS is refused from its header alone (a 875 KB
    /// PNG declaring 30000 x 30000 took 19 s and 7 GB to decode), like a missing file.
    #[test]
    fn oversized_images_are_refused() {
        let dir = std::env::temp_dir().join(format!("nalarupa-big-{:x}", crate::doc::random_u64()));
        let mut assets = Assets::new(dir.clone());
        let png = |w: u32, h: u32| [&b"\x89PNG\r\n\x1a\n\0\0\0\rIHDR"[..], &w.to_be_bytes(), &h.to_be_bytes()].concat();
        let gif = |w: u16, h: u16| [&b"GIF89a"[..], &w.to_le_bytes(), &h.to_le_bytes()].concat();
        // SOI, an APP0 segment, then SOF0 (precision 8, height, width).
        let jpg = |w: u16, h: u16| [&[0xFF, 0xD8, 0xFF, 0xE0, 0, 4, 0, 0, 0xFF, 0xC0, 0, 11, 8][..], &h.to_be_bytes(), &w.to_be_bytes(), &[1, 1, 0x11, 0]].concat();
        let cases = [("a.png", png(30000, 30000), false), ("b.png", png(8000, 8000), true), ("c.gif", gif(60000, 60000), false), ("d.jpg", jpg(20000, 20000), false), ("e.jpg", jpg(640, 480), true)];
        assert_eq!(image_size(&mut std::io::Cursor::new(jpg(640, 480))), Some((640, 480)));
        for (name, bytes, ok) in cases {
            std::fs::write(dir.join(name), &bytes).unwrap();
            assert_eq!(assets.image(name, Some(&dir)).is_some(), ok, "{name}");
            let data = format!("data:image/png;base64,{}", base64::engine::general_purpose::STANDARD.encode(&bytes));
            assert_eq!(assets.image(&data, None).is_some(), ok, "{name} as data url");
            let pct: String = bytes.iter().map(|b| format!("%{b:02X}")).collect();
            assert_eq!(assets.image(&format!("data:image/png,{pct}"), None).is_some(), ok, "{name} as percent-encoded data url");
        }
        // Bytes that only start like a PNG are not read as one.
        let mut bad = png(30000, 30000);
        bad[5] = b'X';
        assert_eq!(image_size(&mut std::io::Cursor::new(bad)), None);
        let _ = std::fs::remove_dir_all(dir);
    }

    /// An SVG image nested thousands of levels deep overflows usvg's recursion (a segfault on the
    /// next render): it is unavailable instead, whether it comes as base64, percent-encoded or a file.
    #[test]
    fn deeply_nested_svg_images_are_unavailable() {
        let deep = |n: usize| format!(r#"<?xml version="1.0"?><!-- <g> --><svg xmlns="http://www.w3.org/2000/svg" width="8" height="8">{}<rect width="5" height="5"/><path d="M0 0"/>{}</svg>"#, "<g id=\"a>\">".repeat(n), "</g>".repeat(n));
        let mut assets = Assets::new(std::env::temp_dir().join("nalarupa-test-assets"));
        let b64 = |s: &str| format!("data:image/svg+xml;base64,{}", base64::engine::general_purpose::STANDARD.encode(s));
        assert!(assets.image(&b64(&deep(100)), None).is_some());
        assert!(assets.image(&b64(&deep(6000)), None).is_none());
        let pct = |s: &str| format!("data:image/svg+xml,{}", s.replace('%', "%25").replace('<', "%3C").replace('>', "%3E"));
        assert!(assets.image(&pct(&deep(100)), None).is_some());
        assert!(assets.image(&pct(&deep(6000)), None).is_none());
        let file = std::env::temp_dir().join(format!("nalarupa-deep-{:x}.svg", crate::doc::random_u64()));
        std::fs::write(&file, deep(6000)).unwrap();
        assert!(assets.image(&file.to_string_lossy(), None).is_none());
        std::fs::write(&file, deep(100)).unwrap();
        assert!(assets.image(&file.to_string_lossy(), None).is_some());
        let _ = std::fs::remove_file(file);
        // Binary payloads are not scanned as markup, whatever their MIME type.
        let png = format!("data:application/octet-stream;base64,{}", base64::engine::general_purpose::STANDARD.encode(b"\x89PNG\r\n\x1a\n<g><g>"));
        assert!(assets.image(&png, None).is_some());
    }

    /// A shallow SVG carrying a deep one as a data url (nested again and again) parses without
    /// overflowing the stack: the inner image is dropped.
    #[test]
    fn nested_deep_svg_data_urls_are_dropped() {
        let wrap = |inner: &str| format!(r#"<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="8" height="8"><image xlink:href="data:image/svg+xml;base64,{}" width="8" height="8"/></svg>"#, base64::engine::general_purpose::STANDARD.encode(inner));
        let deep = format!(r#"<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8">{}<rect width="5" height="5"/>{}</svg>"#, "<g>".repeat(6000), "</g>".repeat(6000));
        let opts = resvg::usvg::Options { image_href_resolver: image_href_resolver(), ..Default::default() };
        fn count(g: &resvg::usvg::Group) -> usize {
            g.children().iter().map(|n| match n {
                resvg::usvg::Node::Image(i) => 1 + if let resvg::usvg::ImageKind::SVG(t) = i.kind() { count(t.root()) } else { 0 },
                resvg::usvg::Node::Group(g) => count(g),
                _ => 0,
            }).sum()
        }
        let images = |svg: &str| count(resvg::usvg::Tree::from_str(svg, &opts).unwrap().root());
        assert_eq!(images(&wrap(&deep)), 0);
        // Wrappers of 200 levels each add up: one nested inside another is fine, two are not.
        let mid = |inner: &str| format!(r#"<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="8" height="8">{}<image xlink:href="data:image/svg+xml;base64,{}" width="8" height="8"/>{}</svg>"#, "<g>".repeat(200), base64::engine::general_purpose::STANDARD.encode(inner), "</g>".repeat(200));
        let leaf = r#"<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><rect width="5" height="5"/></svg>"#;
        assert_eq!(images(&wrap(leaf)), 1);
        assert_eq!(images(&wrap(&mid(leaf))), 2);
        assert_eq!(images(&wrap(&mid(&mid(leaf)))), 1, "the inner wrapper would make 400 levels");
    }

    /// The non-separable blend modes rasterise to the W3C result in PNG export: a channel the
    /// luminosity step pushes below zero is clipped back toward the luminosity, not just cut at 0.
    #[test]
    fn png_non_separable_blends_match_w3c() {
        for (mode, want) in [("hue", [0x00, 0x5e, 0x37]), ("color", [0x00, 0x5e, 0x37]), ("saturation", [0xb1, 0x00, 0x4e]), ("luminosity", [0xff, 0x93, 0xc2])] {
            let svg = format!(r##"<svg xmlns="http://www.w3.org/2000/svg" width="4" height="4"><rect width="4" height="4" fill="#b1004e"/><rect width="4" height="4" fill="#33ffaa" style="mix-blend-mode:{mode}"/></svg>"##);
            let tree = resvg::usvg::Tree::from_str(&svg, &resvg::usvg::Options::default()).unwrap();
            let mut pm = tiny_skia::Pixmap::new(4, 4).unwrap();
            resvg::render(&tree, tiny_skia::Transform::identity(), &mut pm.as_mut());
            let p = pm.pixel(2, 2).unwrap();
            for (got, want) in [p.red(), p.green(), p.blue()].into_iter().zip(want) {
                assert!(got.abs_diff(want) <= 1, "{mode}: {:?} want {want:#x?}", [p.red(), p.green(), p.blue()]);
            }
        }
    }

    /// Export area: box + outer stroke + drop shadow (offset ± 3σ, σ = blur/2), not inner shadows.
    #[test]
    fn export_visual_bounds() {
        let n = node("frame", json!({"stroke": "#000", "strokeWidth": 2, "strokeAlignment": "outer",
            "effect": [{"type": "shadow", "offset": {"x": 0, "y": 4}, "blur": 8}, {"type": "shadow", "shadowType": "inner", "blur": 40}]}), (0.0, 0.0, 100.0, 50.0));
        assert_eq!(visual_bounds(&n), (-14.0, -10.0, 128.0, 78.0));
        assert_eq!(visual_bounds(&node("rectangle", json!({}), (5.0, 6.0, 7.0, 8.0))), (5.0, 6.0, 7.0, 8.0));
    }

    /// Lines past the box of a fixed-size text are painted, so they are exported (and reach above it when bottom-aligned).
    #[test]
    fn export_visual_bounds_include_overflowing_text() {
        let line = |t: &str| crate::text::Line { text: t.into(), width: 50.0, soft: true };
        let text = |p: Value| {
            let mut n = node("text", p, (100.0, 100.0, 120.0, 40.0));
            n.lines = vec![line("a"), line("b"), line("c"), line("d")];
            n.line_h = 29.0;
            n
        };
        let grow = json!({"textGrowth": "fixed-width-height"});
        assert_eq!(visual_bounds(&text(grow.clone())), (100.0, 100.0, 120.0, 116.0));
        assert_eq!(extent(&text(grow)), (100.0, 100.0, 120.0, 116.0));
        assert_eq!(visual_bounds(&text(json!({"textGrowth": "fixed-width-height", "textAlignVertical": "bottom"}))), (100.0, 24.0, 120.0, 116.0));
        assert_eq!(visual_bounds(&text(json!({"textGrowth": "fixed-width-height", "textAlignVertical": "middle"}))), (100.0, 62.0, 120.0, 116.0));
    }

    /// Rotated and flipped nodes export the box they paint, not their unrotated box.
    #[test]
    fn export_visual_bounds_follow_rotation_and_flips() {
        let rot = |p: Value| visual_bounds(&node("rectangle", p, (340.0, 40.0, 100.0, 60.0)));
        assert_eq!(rot(json!({"rotation": 90})), (340.0, -60.0, 60.0, 100.0));
        assert_eq!(rot(json!({"flipX": true})), (240.0, 40.0, 100.0, 60.0));
        assert_eq!(rot(json!({"flipY": true})), (340.0, -20.0, 100.0, 60.0));
        let (x, y, w, h) = rot(json!({"rotation": 30}));
        let (c, s) = (30f64.to_radians().cos(), 30f64.to_radians().sin());
        assert!((w - (100.0 * c + 60.0 * s)).abs() < 1e-5 && (h - (100.0 * s + 60.0 * c)).abs() < 1e-5, "{w}x{h}");
        assert!((x - 340.0).abs() < 1e-5 && (y - (40.0 - 100.0 * s)).abs() < 1e-5, "{x},{y}");
    }

    /// A blurred frame's filter region covers its children where they paint: a flipped or rotated
    /// child reaches left of / above its own box.
    #[test]
    fn extent_follows_children_rotation_and_flips() {
        let mut f = node("frame", json!({}), (100.0, 100.0, 100.0, 100.0));
        f.kids.push(node("rectangle", json!({"flipX": true}), (139.0, 138.0, 112.0, 130.0)));
        // flipX mirrors about the child's left edge: it paints 27..139 instead of 139..251.
        assert_eq!(extent(&f), (27.0, 100.0, 173.0, 168.0));
        f.kids[0] = node("rectangle", json!({"rotation": 180}), (139.0, 138.0, 112.0, 130.0));
        assert_eq!(extent(&f), (27.0, 8.0, 173.0, 192.0));
    }

    #[test]
    fn colors_and_bbox() {
        assert_eq!(color("#fff"), Some(("#ffffff".into(), 1.0)));
        assert_eq!(color("#00000080").unwrap().0, "#000000");
        assert!((color("#00000080").unwrap().1 - 0.502).abs() < 0.01);
        assert_eq!(color("$missing"), None);
        assert_eq!(color("red"), None, "CSS names are transparent");
        assert_eq!(color("#fff8"), None, "#RGBA is not a valid color");
        assert_eq!(path_bbox("M10 20 L30 60 Z"), Some((10.0, 20.0, 20.0, 40.0)));
        // Tight bounds: the control points (y = 100) are not reached by the curve (max y = 75).
        let (_, y, _, h) = path_bbox("M0 0 C0 100 100 100 100 0").unwrap();
        assert!(y.abs() < 1e-9 && (h - 75.0).abs() < 1e-9);
    }

    #[test]
    fn css_radius_scaling() {
        let n = node("rectangle", json!({"cornerRadius": [30, 30, 0, 0]}), (0.0, 0.0, 100.0, 40.0));
        assert_eq!(radii(&n), [30.0, 30.0, 0.0, 0.0]);
        let n = node("rectangle", json!({"cornerRadius": 50}), (0.0, 0.0, 100.0, 40.0));
        assert_eq!(radii(&n), [20.0; 4]);
    }

    #[test]
    fn shapes() {
        assert_eq!(polygon_d(&node("polygon", json!({}), (0.0, 0.0, 10.0, 10.0))), "");
        let tri = polygon_d(&node("polygon", json!({"polygonCount": 3, "cornerRadius": 2}), (0.0, 0.0, 10.0, 10.0)));
        assert_eq!(tri.matches('A').count(), 3);
        // A huge count from the document is capped instead of allocating or stalling.
        for c in [1e4, 4294967296.0, 1e300] {
            let big = polygon_d(&node("polygon", json!({"polygonCount": c}), (0.0, 0.0, 10.0, 10.0)));
            assert_eq!(big.matches('L').count(), 999, "{c}");
        }
        let line = geometry(&node("line", json!({}), (5.0, 5.0, 20.0, 0.0)));
        assert_eq!((line.d.as_str(), line.closed), ("M5 5L25 5", false));
        let (_, closed) = path_geometry(&node("path", json!({"geometry": "M0 0L10 0L10 10Z"}), (0.0, 0.0, 10.0, 10.0)));
        let (_, open) = path_geometry(&node("path", json!({"geometry": "M0 0L10 10"}), (0.0, 0.0, 10.0, 10.0)));
        assert!(closed && !open);
        // Flips mirror about the origin (top-left), after which rotation applies.
        let f = node("rectangle", json!({"flipX": true, "rotation": 90}), (10.0, 0.0, 100.0, 50.0));
        assert_eq!(transform(&f), "rotate(-90 10 0) matrix(-1 0 0 1 20 0)");
    }

    /// Our shaping must agree with usvg's: each wrapped line, laid out by usvg with text-anchor="end",
    /// starts exactly one measured width (minus usvg's untrailed letter spacing) left of the anchor.
    #[test]
    fn measured_lines_match_usvg_layout() {
        use resvg::usvg;
        let mut fonts = Fonts::new(std::env::temp_dir().join("nalarupa-test-fonts"));
        fonts.online = false;
        let st = TextStyle { family: "Arial".into(), size: 19.0, weight: 400, italic: false, letter_spacing: 0.7, line_height: None };
        let lines = fonts.wrap("AVATAR Wave Toyota, LY Ta Yo kerning pairs wrap across several lines
VAT office", 230.0, &st);
        assert!(lines.len() >= 4 && lines.iter().all(|l| l.width <= 230.01));
        let body: String = lines.iter().enumerate().map(|(i, l)| {
            format!(r#"<text x="0" y="{}" text-anchor="end" font-family="'Arial', 'Noto Sans', sans-serif" font-size="19" letter-spacing="0.7">{}</text>"#, 50 * (i + 1), esc(&l.text))
        }).collect();
        let doc = format!(r#"<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10">{body}</svg>"#);
        let opt = usvg::Options { fontdb: Arc::clone(&fonts.db), font_resolver: fonts.resolver(), ..usvg::Options::default() };
        let tree = usvg::Tree::from_str(&doc, &opt).unwrap();
        let mut starts: Vec<f32> = Vec::new();
        for c in tree.root().children() {
            if let usvg::Node::Text(t) = c {
                starts.push(t.layouted().iter().flat_map(|s| s.positioned_glyphs.iter().map(|g| g.transform().tx)).fold(f32::MAX, f32::min));
            }
        }
        assert_eq!(starts.len(), lines.len());
        for (l, got) in lines.iter().zip(starts) {
            let want = -(l.width - st.letter_spacing);
            assert!((got as f64 - want).abs() < 0.02, "{:?}: usvg starts at {got}, measured {want}", l.text);
        }
    }

    /// A line's box is flat: a box-sized gradientTransform would be singular (browsers paint
    /// nothing), so its gradient is written in user space, without float leftovers.
    #[test]
    fn gradients_on_a_flat_box_stay_invertible() {
        let mut fonts = Fonts::new(std::env::temp_dir().join("nalarupa-test-fonts"));
        fonts.online = false;
        let mut assets = Assets::new(std::env::temp_dir().join("nalarupa-test-assets"));
        assets.online = false;
        let mut svg = Svg::new(&mut fonts, &mut assets, None);
        let grad = |ty: &str, rotation: f64| json!({"gradientType": ty, "rotation": rotation, "colors": [{"color": "#f00", "position": 0}, {"color": "#00f", "position": 1}]});
        for (ty, rotation, b) in [("linear", 90.0, (20.0, 50.0, 160.0, 0.0)), ("linear", 0.0, (100.0, 20.0, 0.0, 160.0)), ("radial", 90.0, (20.0, 50.0, 160.0, 0.0))] {
            svg.gradient(grad(ty, rotation).as_object().unwrap(), b).unwrap();
        }
        assert!(!svg.defs.contains("gradientTransform"), "{}", svg.defs);
        assert!(svg.defs.contains(r#"x1="180" y1="50" x2="20" y2="50""#), "{}", svg.defs);
        assert!(svg.defs.contains(r#"cx="100" cy="50" r="80""#), "{}", svg.defs);
        svg.defs.clear();
        svg.gradient(grad("radial", 90.0).as_object().unwrap(), (20.0, 20.0, 160.0, 80.0)).unwrap();
        assert!(svg.defs.contains(r#"gradientTransform="matrix(0 -40 80 0 100 60)""#), "{}", svg.defs);
        assert!(!svg.defs.contains("e-"), "{}", svg.defs);
    }

    #[test]
    fn premultiplied_stops() {
        let s = stop_tags(&[(0.0, [1.0, 0.0, 0.0, 1.0]), (1.0, [0.0, 0.0, 1.0, 0.0])]);
        assert_eq!(s.matches("<stop").count(), 9);
        assert!(!s.contains("#7f") && s.matches("#ff0000").count() == 8, "no grey fringe toward transparent: {s}");
        assert_eq!(sample(&[(0.2, [1.0; 4]), (0.8, [0.0, 0.0, 0.0, 1.0])], 0.0), [1.0; 4]);
    }
}
