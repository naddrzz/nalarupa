//! Font database, text shaping/measurement and line wrapping with SkParagraph's rules.
//! Text is shaped with rustybuzz exactly like usvg does, so measured widths match what resvg draws.
//! Missing families are fetched once from Google Fonts (TTF) by a background thread and cached on disk
//! (text uses a fallback face until the download lands); characters a family lacks fall back to Noto fonts first.

use crate::resolve::RNode;
use resvg::usvg::fontdb;
use std::collections::{HashMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, OnceLock};

/// Fallback face per (char, fonts already tried); usvg otherwise rescans every system font per glyph.
type FallbackCache = Arc<Mutex<HashMap<(char, Vec<fontdb::ID>), Option<fontdb::ID>>>>;

/// Width slack when fitting text into a line (float noise between equal measurements).
const FIT_EPS: f64 = 0.01;

#[derive(Clone)]
pub struct TextStyle {
    pub family: String,
    pub size: f64,
    pub weight: u16,
    pub italic: bool,
    pub letter_spacing: f64,
    pub line_height: Option<f64>,
}

impl TextStyle {
    pub fn of(n: &RNode) -> TextStyle {
        TextStyle {
            family: n.s("fontFamily").unwrap_or("Inter").to_string(),
            size: n.f("fontSize").unwrap_or(14.0).max(1.0),
            weight: weight_num(n.p.get("fontWeight")),
            italic: n.s("fontStyle").is_some_and(|s| s.eq_ignore_ascii_case("italic")),
            letter_spacing: n.f("letterSpacing").unwrap_or(0.0),
            line_height: n.f("lineHeight").filter(|v| *v > 0.0),
        }
    }
}

/// "normal", "bold" or a number; any other name ("semibold", "medium") is 400.
pub fn weight_num(v: Option<&serde_json::Value>) -> u16 {
    let w = match v {
        Some(serde_json::Value::Number(n)) => n.as_f64(),
        Some(serde_json::Value::String(s)) => match s.trim() {
            "normal" => Some(400.0),
            "bold" => Some(700.0),
            s => s.parse().ok(),
        },
        _ => None,
    };
    w.filter(|w| (1.0..=1000.0).contains(w)).map_or(400, |w| w.round() as u16)
}

#[derive(Clone, Copy)]
pub struct Metrics {
    pub ascent: f64,
    pub descent: f64,
    /// Whole pixels, like SkParagraph.
    pub line_h: f64,
    /// Baseline offset from the top of a line.
    pub baseline: f64,
}

/// One laid-out line. `width` is its advance without trailing spaces (they hang);
/// `soft` = ended by wrapping rather than `\n` (only those lines are justified).
#[derive(Clone, Debug, PartialEq)]
pub struct Line {
    pub text: String,
    pub width: f64,
    pub soft: bool,
}

pub struct Fonts {
    pub db: Arc<fontdb::Database>,
    cache: PathBuf,
    tried: HashSet<String>,
    /// Memos, cleared whenever a font is added: primary face per (family, weight, italic),
    /// vertical metrics per face (em), shaped clusters per (face, text) as (byte offset, advance in em).
    faces: HashMap<(String, u16, bool), Option<fontdb::ID>>,
    vmetrics: HashMap<(fontdb::ID, u16), (f64, f64, f64)>,
    shaped: HashMap<(fontdb::ID, u16, String), Arc<Vec<(usize, f64)>>>,
    fallback: FallbackCache,
    /// Downloads in flight as (lowercase family, weight, italic).
    loading: HashSet<(String, u16, bool)>,
    /// Bumped when a download adds a font; the renderer rebuilds every layer when it changes.
    pub generation: u64,
    pub online: bool,
}

impl Fonts {
    pub fn new(cache: PathBuf) -> Fonts {
        let mut db = fontdb::Database::new();
        db.load_system_fonts();
        let _ = std::fs::create_dir_all(&cache);
        db.load_fonts_dir(&cache);
        let sans = ["Segoe UI", "Helvetica", "Arial", "DejaVu Sans"]
            .into_iter()
            .find(|f| db.faces().any(|x| x.families.iter().any(|(n, _)| n == f)))
            .unwrap_or("Arial");
        db.set_sans_serif_family(sans);
        prefer_variable(&mut db, &cache);
        let ids: Vec<fontdb::ID> = db.faces().map(|f| f.id).collect();
        for id in ids {
            // SAFETY: maps each font file once instead of re-opening it on every glyph lookup;
            // fontdb already memory-maps these files per access, so the risk (file changed underneath) is unchanged.
            unsafe { db.make_shared_face_data(id) };
        }
        Fonts {
            db: Arc::new(db),
            cache,
            tried: HashSet::new(),
            faces: HashMap::new(),
            vmetrics: HashMap::new(),
            shaped: HashMap::new(),
            fallback: FallbackCache::default(),
            loading: HashSet::new(),
            generation: 0,
            online: true,
        }
    }

    /// usvg font resolver with the same memoized, Noto-first fallback search used for measuring.
    pub fn resolver(&self) -> resvg::usvg::FontResolver<'static> {
        let cache = Arc::clone(&self.fallback);
        resvg::usvg::FontResolver {
            select_font: resvg::usvg::FontResolver::default_font_selector(),
            select_fallback: Box::new(move |c, tried, db| cached_fallback(&cache, c, tried, db)),
        }
    }

    pub fn has_family(&self, family: &str) -> bool {
        self.db.faces().any(|f| f.families.iter().any(|(n, _)| n.eq_ignore_ascii_case(family)))
    }

    /// Make sure `family` (at this weight/style) is available: loaded from the cache now, or downloaded
    /// in the background while text falls back to Noto Sans. An unavailable family falls back to Noto Sans.
    pub fn ensure(&mut self, st: &TextStyle) {
        if self.fetch(&st.family, st.weight, st.italic) && !st.family.eq_ignore_ascii_case("Noto Sans") && !self.has_family(&st.family) && !self.loading(&st.family) {
            self.fetch("Noto Sans", st.weight, st.italic);
        }
    }

    /// A download of this family is still running.
    pub fn loading(&self, family: &str) -> bool {
        let family = family.to_lowercase();
        self.loading.iter().any(|(f, _, _)| *f == family)
    }

    /// Returns false when this (family, weight, style) was already handled.
    fn fetch(&mut self, family: &str, weight: u16, italic: bool) -> bool {
        if !self.tried.insert(format!("{family}|{weight}|{italic}")) {
            return false;
        }
        if !self.load_cached(family, weight, italic) && self.online && self.loading.insert((family.to_lowercase(), weight, italic)) {
            let (cache, family) = (self.cache.clone(), family.to_string());
            std::thread::spawn(move || {
                download(&cache, &family, weight, italic);
                crate::engine::engine().font_ready(&family, weight, italic);
            });
        }
        true
    }

    /// A download finished (or failed): load what it cached. True when a font was added.
    pub fn downloaded(&mut self, family: &str, weight: u16, italic: bool) -> bool {
        self.loading.remove(&(family.to_lowercase(), weight, italic));
        let before = self.db.len();
        self.load_cached(family, weight, italic);
        if !self.has_family(family) && !family.eq_ignore_ascii_case("Noto Sans") {
            self.fetch("Noto Sans", weight, italic);
        }
        let added = self.db.len() != before;
        self.generation += added as u64;
        added
    }

    /// Make the face available from what is already on disk; true when it is.
    fn load_cached(&mut self, family: &str, weight: u16, italic: bool) -> bool {
        if self.variable_loaded(family, italic) {
            return true;
        }
        // Before the exact-face check: a cached static instance must not shadow the variable font.
        if let Ok(b) = std::fs::read(var_file(&self.cache, family, italic)) {
            let db = Arc::make_mut(&mut self.db);
            db.load_font_data(b);
            prefer_variable(db, &self.cache);
            self.fonts_changed();
            return true;
        }
        if self.exact_face(family, weight, italic) {
            return true;
        }
        let Ok(b) = std::fs::read(static_file(&self.cache, family, weight, italic)) else { return false };
        Arc::make_mut(&mut self.db).load_font_data(b);
        self.fonts_changed();
        true
    }

    fn fonts_changed(&mut self) {
        self.faces.clear();
        self.vmetrics.clear();
        self.shaped.clear();
        if let Ok(mut m) = self.fallback.lock() {
            m.clear();
        }
    }

    /// A variable face of this family (one face serves every weight via `wght`).
    fn variable_loaded(&self, family: &str, italic: bool) -> bool {
        self.db.faces().any(|f| {
            f.families.iter().any(|(n, _)| n.eq_ignore_ascii_case(family))
                && (f.style != fontdb::Style::Normal) == italic
                && self.db.with_face_data(f.id, |d, i| ttf_parser::Face::parse(d, i).is_ok_and(|p| p.is_variable())).unwrap_or(false)
        })
    }

    fn exact_face(&self, family: &str, weight: u16, italic: bool) -> bool {
        self.db.faces().any(|f| {
            f.families.iter().any(|(n, _)| n.eq_ignore_ascii_case(family))
                && (f.weight.0 as i32 - weight as i32).abs() <= 50
                && (f.style != fontdb::Style::Normal) == italic
        })
    }

    /// Same query usvg runs for `font-family="'<family>', 'Noto Sans', sans-serif"` (see svg.rs),
    /// after making sure the family is loaded. Memoized.
    fn face(&mut self, st: &TextStyle) -> Option<fontdb::ID> {
        let key = (st.family.clone(), st.weight, st.italic);
        if let Some(hit) = self.faces.get(&key) {
            return *hit;
        }
        self.ensure(st);
        let families = [fontdb::Family::Name(&st.family), fontdb::Family::Name("Noto Sans"), fontdb::Family::SansSerif, fontdb::Family::Serif];
        let id = self.db.query(&fontdb::Query {
            families: &families,
            weight: fontdb::Weight(st.weight),
            stretch: fontdb::Stretch::Normal,
            style: if st.italic { fontdb::Style::Italic } else { fontdb::Style::Normal },
        });
        self.faces.insert(key, id);
        id
    }

    pub fn metrics(&mut self, st: &TextStyle) -> Metrics {
        let m = self.face(st).and_then(|id| {
            if let Some(m) = self.vmetrics.get(&(id, st.weight)) {
                return Some(*m);
            }
            let m = self.db.with_face_data(id, |data, idx| {
                let mut f = ttf_parser::Face::parse(data, idx).ok()?;
                // Variable fonts move ascender/descender with weight (MVAR), as Skia applies.
                let _ = f.set_variation(ttf_parser::Tag::from_bytes(b"wght"), st.weight as f32);
                let upem = f.units_per_em() as f64;
                Some((f.ascender() as f64 / upem, f.descender() as f64 / upem, f.line_gap() as f64 / upem))
            })??;
            self.vmetrics.insert((id, st.weight), m);
            Some(m)
        });
        let (asc, desc, gap) = m.unwrap_or((0.9, -0.22, 0.0));
        let (ascent, descent, gap) = (asc * st.size, -desc * st.size, gap * st.size);
        // SkParagraph: a set line height is round(lh·size) with half leading; natural lines round their height.
        match st.line_height.map(|lh| (lh * st.size).round()).filter(|h| *h > 0.0) {
            Some(h) => Metrics { ascent, descent, line_h: h, baseline: (h - ascent - descent) / 2.0 + ascent },
            None => Metrics { ascent, descent, line_h: (ascent + descent + gap).round().max(1.0), baseline: gap / 2.0 + ascent },
        }
    }

    /// Advance of `text` (trailing spaces included) plus letter spacing after every cluster.
    pub fn width(&mut self, text: &str, st: &TextStyle) -> f64 {
        let cl = self.clusters(text, st);
        cl.iter().map(|c| c.1).sum::<f64>() * st.size + st.letter_spacing * cl.len() as f64
    }

    /// Greedy UAX #14 line breaking (CJK breaks between ideographs); `\n` always breaks,
    /// trailing spaces hang, words wider than `max_w` break between clusters. No hyphenation.
    pub fn wrap(&mut self, text: &str, max_w: f64, st: &TextStyle) -> Vec<Line> {
        let mut out = Vec::new();
        for para in text.split('\n') {
            let para = para.strip_suffix('\r').unwrap_or(para);
            let cl = self.clusters(para, st);
            // prefix[i] = advance of clusters before cl[i]
            let mut prefix = vec![0.0];
            for c in cl.iter() {
                prefix.push(prefix.last().unwrap() + c.1 * st.size + st.letter_spacing);
            }
            let at = |b: usize| prefix[cl.partition_point(|c| c.0 < b)];
            let w = |a: usize, b: usize| at(b) - at(a);
            let trimmed = |a: usize, b: usize| a + para[a..b].trim_end().len();
            let mut lines = Vec::new();
            let (mut start, mut end, mut line_w) = (0, 0, 0.0);
            for (brk, opp) in unicode_linebreak::linebreaks(para) {
                let te = trimmed(end, brk);
                if end > start && line_w + w(end, te) > max_w + FIT_EPS {
                    lines.push((start, end, true));
                    (start, line_w) = (end, 0.0);
                }
                if end == start && w(end, te) > max_w + FIT_EPS {
                    for i in cl.partition_point(|c| c.0 < end)..cl.partition_point(|c| c.0 < te) {
                        let (b, a) = (cl[i].0, cl[i].1 * st.size + st.letter_spacing);
                        if b > start && line_w + a > max_w + FIT_EPS {
                            lines.push((start, b, true));
                            (start, line_w) = (b, 0.0);
                        }
                        line_w += a;
                    }
                    line_w += w(te, brk);
                } else {
                    line_w += w(end, brk);
                }
                end = brk;
                if opp == unicode_linebreak::BreakOpportunity::Mandatory && brk < para.len() {
                    lines.push((start, end, false));
                    (start, line_w) = (end, 0.0);
                }
            }
            lines.push((start, para.len(), false));
            out.extend(lines.into_iter().map(|(a, b, soft)| {
                let t = para[a..b].trim_end();
                Line { text: t.to_string(), width: w(a, a + t.len()), soft }
            }));
        }
        out
    }

    /// Clusters of `text` as (byte offset, advance in em), shaped like usvg: rustybuzz with the
    /// primary face, then missing glyphs replaced from fallback faces.
    fn clusters(&mut self, text: &str, st: &TextStyle) -> Arc<Vec<(usize, f64)>> {
        let Some(id) = self.face(st) else {
            return Arc::new(text.char_indices().map(|(i, c)| (i, if c.is_ascii() { 0.55 } else { 1.0 })).collect());
        };
        if let Some(hit) = self.shaped.get(&(id, st.weight, text.to_string())) {
            return Arc::clone(hit);
        }
        let mut glyphs = shape(&self.db, id, text, st.weight);
        let mut used = vec![id];
        while let Some(c) = glyphs.iter().find(|g| g.1 == 0).and_then(|g| text[g.0..].chars().next()) {
            if !c.is_ascii() {
                self.fetch(noto_family(c), st.weight, false);
            }
            let Some(fb) = cached_fallback(&self.fallback, c, &used, &mut self.db) else { break };
            let alt = shape(&self.db, fb, text, st.weight);
            if alt.iter().all(|g| g.1 != 0) {
                glyphs = alt;
                break;
            }
            if alt.len() != glyphs.len() {
                break;
            }
            for (g, a) in glyphs.iter_mut().zip(alt) {
                if g.1 == 0 && a.1 != 0 {
                    *g = a;
                }
            }
            used.push(fb);
        }
        let mut out: Vec<(usize, f64)> = Vec::new();
        for (byte, _, adv) in glyphs {
            match out.last_mut() {
                Some(last) if last.0 == byte => last.1 += adv,
                _ => out.push((byte, adv)),
            }
        }
        out.sort_by_key(|c| c.0);
        let out = Arc::new(out);
        self.shaped.insert((id, st.weight, text.to_string()), Arc::clone(&out));
        out
    }
}

/// Drop other faces (cached statics, installed copies) of families that have a downloaded variable
/// font, so the variable face always wins the family query over installed fonts.
fn prefer_variable(db: &mut fontdb::Database, cache: &std::path::Path) {
    let var_dir = cache.join("var");
    let path = |f: &fontdb::FaceInfo| match &f.source {
        fontdb::Source::File(p) | fontdb::Source::SharedFile(p, _) => Some(p.clone()),
        fontdb::Source::Binary(_) => None,
    };
    // Faces loaded from bytes at runtime are variable downloads too (their file is in var/).
    let variable: HashSet<String> = db
        .faces()
        .filter(|f| path(f).map_or(true, |p| p.starts_with(&var_dir)))
        .filter(|f| db.with_face_data(f.id, |d, i| ttf_parser::Face::parse(d, i).is_ok_and(|p| p.is_variable())).unwrap_or(false))
        .flat_map(|f| f.families.iter().map(|(n, _)| n.to_lowercase()))
        .collect();
    let stale: Vec<fontdb::ID> = db
        .faces()
        .filter(|f| path(f).is_some_and(|p| !p.starts_with(&var_dir)))
        .filter(|f| f.families.iter().any(|(n, _)| variable.contains(&n.to_lowercase())))
        .map(|f| f.id)
        .collect();
    for id in stale {
        db.remove_face(id);
    }
}

/// Glyphs of `text` in one face at `weight` (only `wght` is set on variable fonts):
/// (cluster byte offset, glyph id, advance in em).
fn shape(db: &fontdb::Database, id: fontdb::ID, text: &str, weight: u16) -> Vec<(usize, u32, f64)> {
    db.with_face_data(id, |data, idx| {
        let mut face = rustybuzz::Face::from_slice(data, idx)?;
        face.set_variations(&[rustybuzz::Variation { tag: rustybuzz::ttf_parser::Tag::from_bytes(b"wght"), value: weight as f32 }]);
        let upem = face.units_per_em() as f64;
        let mut buf = rustybuzz::UnicodeBuffer::new();
        buf.push_str(text);
        let out = rustybuzz::shape(&face, &[], buf);
        Some(out.glyph_infos().iter().zip(out.glyph_positions()).map(|(i, p)| (i.cluster as usize, i.glyph_id, p.x_advance as f64 / upem)).collect())
    })
    .flatten()
    .unwrap_or_default()
}

fn cached_fallback(cache: &FallbackCache, c: char, used: &[fontdb::ID], db: &mut Arc<fontdb::Database>) -> Option<fontdb::ID> {
    let key = (c, used.to_vec());
    if let Some(hit) = cache.lock().ok().and_then(|m| m.get(&key).copied()) {
        return hit;
    }
    let found = pick_fallback(c, used, db);
    if let Ok(mut m) = cache.lock() {
        m.insert(key, found);
    }
    found
}

/// Noto faces first (nearest weight, same style preferred), then usvg's scan of every font.
fn pick_fallback(c: char, used: &[fontdb::ID], db: &mut Arc<fontdb::Database>) -> Option<fontdb::ID> {
    let shared: &fontdb::Database = db;
    let base = shared.face(*used.first()?)?;
    let (style, weight) = (base.style, base.weight.0 as i32);
    let noto = shared
        .faces()
        .filter(|f| !used.contains(&f.id) && f.families.iter().any(|(n, _)| n.starts_with("Noto")))
        .filter(|f| has_char(shared, f.id, c))
        .min_by_key(|f| (f.style != style, (f.weight.0 as i32 - weight).abs()))
        .map(|f| f.id);
    noto.or_else(|| (resvg::usvg::FontResolver::default_fallback_selector())(c, used, db))
}

fn has_char(db: &fontdb::Database, id: fontdb::ID, c: char) -> bool {
    db.with_face_data(id, |data, idx| ttf_parser::Face::parse(data, idx).ok()?.glyph_index(c)).flatten().is_some()
}

/// Fallback library, picked by script. Emoji come from monochrome Noto Emoji.
fn noto_family(c: char) -> &'static str {
    match c as u32 {
        0x0590..=0x05FF | 0xFB1D..=0xFB4F => "Noto Sans Hebrew",
        0x0600..=0x06FF | 0x0750..=0x077F | 0x08A0..=0x08FF | 0xFB50..=0xFDFF | 0xFE70..=0xFEFF => "Noto Sans Arabic",
        0x0E00..=0x0E7F => "Noto Sans Thai",
        0x10A0..=0x10FF | 0x2D00..=0x2D2F => "Noto Sans Georgian",
        0x1800..=0x18AF => "Noto Sans Mongolian",
        0xA980..=0xA9DF => "Noto Sans Javanese",
        0x1100..=0x11FF | 0x3130..=0x318F | 0xAC00..=0xD7AF => "Noto Sans KR",
        0x2E80..=0x2FFF | 0x3000..=0x30FF | 0x31F0..=0x31FF | 0x3400..=0x4DBF | 0x4E00..=0x9FFF | 0xF900..=0xFAFF | 0xFF00..=0xFFEF => "Noto Sans JP",
        0x2190..=0x21FF | 0x2300..=0x23FF | 0x25A0..=0x25FF | 0x2B00..=0x2BFF => "Noto Sans Symbols 2",
        0x2600..=0x27BF | 0xFE0F | 0x1F000..=0x1FAFF => "Noto Emoji",
        _ => "Noto Sans",
    }
}

/// The family's variable font in the cache (full axes, rendered at the font's default
/// location except `wght`). Static instances from the CSS API are pinned to opsz 14 and measure
/// differently for families whose default optical size is not 14 (DM Sans is ~0.6 % wider).
fn var_file(cache: &Path, family: &str, italic: bool) -> PathBuf {
    let compact: String = family.chars().filter(|c| !c.is_whitespace()).collect();
    cache.join("var").join(format!("{compact}{}.ttf", if italic { "-Italic" } else { "" }))
}

fn static_file(cache: &Path, family: &str, weight: u16, italic: bool) -> PathBuf {
    cache.join(format!("{}-{}{}.ttf", family.replace(' ', "_"), weight, if italic { "i" } else { "" }))
}

/// Axis tags of a Google Fonts family in file-name order (ASCII: uppercase first), if variable.
/// The metadata is read (or downloaded) once per process.
fn family_axes(cache: &Path, family: &str) -> Option<Vec<String>> {
    static AXES: OnceLock<HashMap<String, Vec<String>>> = OnceLock::new();
    let all = AXES.get_or_init(|| {
        let file = cache.join("google-fonts-metadata.json");
        let text = std::fs::read_to_string(&file).ok().or_else(|| {
            let t = crate::net::get_text("https://fonts.google.com/metadata/fonts")?;
            let _ = std::fs::write(&file, &t);
            Some(t)
        });
        // The endpoint prefixes its JSON with an anti-XSSI guard.
        let meta: serde_json::Value = text.and_then(|t| serde_json::from_str(&t[t.find('{')?..]).ok()).unwrap_or_default();
        let map = meta["familyMetadataList"].as_array().into_iter().flatten().filter_map(|f| {
            let mut tags: Vec<String> = f["axes"].as_array()?.iter().filter_map(|a| a["tag"].as_str().map(str::to_string)).collect();
            tags.sort();
            Some((f["family"].as_str()?.to_lowercase(), tags))
        });
        map.filter(|(_, t)| !t.is_empty()).collect()
    });
    all.get(&family.to_lowercase()).cloned()
}

/// One download at a time: they share the metadata file and a slow link.
static DOWNLOADS: Mutex<()> = Mutex::new(());

/// Fetch a family from Google Fonts into `cache` (the variable font if it has one, else the static
/// instance). Blocks on the network: run it off the engine lock.
fn download(cache: &Path, family: &str, weight: u16, italic: bool) {
    let _turn = DOWNLOADS.lock().unwrap_or_else(|e| e.into_inner());
    let var = var_file(cache, family, italic);
    if var.exists() {
        return;
    }
    if let Some(axes) = family_axes(cache, family) {
        let compact: String = family.chars().filter(|c| !c.is_whitespace()).collect();
        let name = format!("{compact}{}[{}].ttf", if italic { "-Italic" } else { "" }, axes.join(","));
        let dir = compact.to_lowercase();
        let bytes = ["ofl", "apache", "ufl"]
            .iter()
            .find_map(|lic| crate::net::get_bytes(&format!("https://raw.githubusercontent.com/google/fonts/main/{lic}/{dir}/{}", name.replace('[', "%5B").replace(']', "%5D"))));
        if let Some(bytes) = bytes {
            if std::fs::create_dir_all(cache.join("var")).is_ok() {
                let _ = std::fs::write(&var, &bytes);
            }
            return;
        }
    }
    let file = static_file(cache, family, weight, italic);
    if file.exists() {
        return;
    }
    let fam = family.replace(' ', "+");
    let spec = if italic { format!("{fam}:ital,wght@1,{weight}") } else { format!("{fam}:wght@{weight}") };
    let data = (|| {
        let css = crate::net::get_text(&format!("https://fonts.googleapis.com/css2?family={spec}"))
            .or_else(|| crate::net::get_text(&format!("https://fonts.googleapis.com/css2?family={fam}")))?;
        let start = css.find("url(")? + 4;
        crate::net::get_bytes(&css[start..start + css[start..].find(')')?])
    })();
    if let Some(data) = data {
        let _ = std::fs::write(&file, &data);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn fonts() -> Fonts {
        let mut f = Fonts::new(std::env::temp_dir().join("nalarupa-test-fonts"));
        f.online = false;
        f
    }

    fn style(size: f64) -> TextStyle {
        TextStyle { family: "Arial".into(), size, weight: 400, italic: false, letter_spacing: 0.0, line_height: None }
    }

    /// A font the download thread cached is loaded when it reports back, never while text is laid out.
    #[test]
    fn downloaded_font_loads_from_cache() {
        let cache = std::env::temp_dir().join(format!("nalarupa-test-dl-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&cache);
        let mut f = Fonts::new(cache.clone());
        f.online = false;
        let (family, weight, bytes) = {
            let face = f.db.faces().find(|x| x.weight.0 == 400 && x.style == fontdb::Style::Normal && matches!(x.source, fontdb::Source::File(_) | fontdb::Source::SharedFile(..))).expect("a system font");
            let (fontdb::Source::File(p) | fontdb::Source::SharedFile(p, _)) = &face.source else { unreachable!() };
            (face.families[0].0.clone(), face.weight.0, std::fs::read(p).unwrap())
        };
        let ids: Vec<_> = f.db.faces().filter(|x| x.families.iter().any(|(n, _)| *n == family)).map(|x| x.id).collect();
        for id in ids {
            Arc::make_mut(&mut f.db).remove_face(id);
        }
        assert!(!f.has_family(&family));
        let st = TextStyle { family: family.clone(), size: 16.0, weight, italic: false, letter_spacing: 0.0, line_height: None };
        f.ensure(&st);
        assert!(!f.has_family(&family) && f.generation == 0, "nothing on disk: text falls back, no block");
        f.loading.insert((family.to_lowercase(), weight, false));
        assert!(f.loading(&family));
        std::fs::write(static_file(&cache, &family, weight, false), bytes).unwrap();
        assert!(f.downloaded(&family, weight, false));
        assert!(f.has_family(&family) && !f.loading(&family) && f.generation == 1);
        assert!(!f.downloaded(&family, weight, false), "a second report adds nothing");
        assert_eq!(f.generation, 1);
        let _ = std::fs::remove_dir_all(&cache);
    }

    #[test]
    fn font_weights() {
        assert_eq!(weight_num(Some(&json!("bold"))), 700);
        assert_eq!(weight_num(Some(&json!("600"))), 600);
        assert_eq!(weight_num(Some(&json!(500))), 500);
        assert_eq!(weight_num(Some(&json!("semibold"))), 400);
        assert_eq!(weight_num(None), 400);
    }

    #[test]
    fn line_heights_round() {
        let mut f = fonts();
        let mut st = style(15.0);
        st.line_height = Some(1.5);
        let m = f.metrics(&st);
        assert_eq!(m.line_h, 23.0); // round(22.5)
        st.line_height = None;
        assert_eq!(f.metrics(&st).line_h.fract(), 0.0);
    }

    #[test]
    fn letter_spacing_and_kerning() {
        let mut f = fonts();
        let mut st = style(20.0);
        let plain = f.width("AVA", &st);
        st.letter_spacing = 2.0;
        assert!((f.width("AVA", &st) - plain - 6.0).abs() < 1e-9, "trailing spacing counts");
        st.letter_spacing = 0.0;
        let kerned = plain;
        let separate = f.width("A", &st) * 2.0 + f.width("V", &st);
        if f.face(&st).is_some() {
            assert!(kerned < separate - 0.1, "AV is kerned ({kerned} vs {separate})");
        }
    }

    #[test]
    fn wraps_at_uax14_opportunities() {
        let mut f = fonts();
        let st = style(16.0);
        let w = f.width("hello world", &st);
        let lines = f.wrap("hello world again", w + 1.0, &st);
        assert_eq!(lines.iter().map(|l| l.text.as_str()).collect::<Vec<_>>(), ["hello world", "again"]);
        assert!(lines[0].soft && !lines[1].soft);
        assert!((lines[0].width - w).abs() < 1e-6, "trailing space hangs");
        // CJK breaks between ideographs; zero width puts one cluster per line.
        let cjk = f.wrap("漢字かな", 0.0, &st);
        assert_eq!(cjk.len(), 4);
        let hard = f.wrap("a\nb", 1000.0, &st);
        assert_eq!(hard.iter().map(|l| (l.text.as_str(), l.soft)).collect::<Vec<_>>(), [("a", false), ("b", false)]);
        // Overlong words break between clusters, never producing empty lines.
        let abc = f.width("abc", &st);
        let long = f.wrap("abcdefgh", abc + 0.5, &st);
        assert_eq!(long.iter().map(|l| l.text.as_str()).collect::<Vec<_>>(), ["abc", "def", "gh"]);
    }
}
