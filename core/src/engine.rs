//! Editor state shared by the UI (FFI), the MCP server and the built-in agent.

use crate::doc::{Doc, ROOT};
use crate::render::{Ctx, Renderer};
use crate::svg::Assets;
use crate::text::Fonts;
use serde_json::{json, Map, Number, Value};
use std::path::{Path, PathBuf};
use std::sync::{Mutex, MutexGuard, OnceLock};

pub struct ToolOut {
    pub text: String,
    /// Base64 PNGs; screenshots land here only after `finish`.
    pub images: Vec<String>,
    pub error: bool,
    /// Screenshots (node id, tree, scale) still to rasterize: `finish` does it once the caller has
    /// released the engine lock, so the canvas keeps drawing while the agent looks at its work.
    pub shots: Vec<(String, resvg::usvg::Tree, f64)>,
}

impl ToolOut {
    pub fn ok(text: impl Into<String>) -> ToolOut {
        ToolOut { text: text.into(), images: Vec::new(), error: false, shots: Vec::new() }
    }
    pub fn err(text: impl Into<String>) -> ToolOut {
        ToolOut { error: true, ..ToolOut::ok(text) }
    }
    /// Rasterize pending screenshots. Call without holding `engine()`.
    pub fn finish(mut self) -> ToolOut {
        for (id, tree, scale) in std::mem::take(&mut self.shots) {
            match crate::render::encode_tree(&tree, "png", scale, 0) {
                Ok(png) => self.images.push(base64::Engine::encode(&base64::engine::general_purpose::STANDARD, png)),
                Err(err) => self.text += &format!("Screenshot of {id} failed: {err}\n"),
            }
        }
        self
    }
}

pub struct Engine {
    pub doc: Doc,
    pub libs: Vec<(String, Value)>,
    pub fonts: Fonts,
    pub assets: Assets,
    pub renderer: Renderer,
    pub version: u64,
    synced: u64,
    pub saved: u64,
    undo: Vec<Value>,
    redo: Vec<Value>,
    pub selection: Vec<String>,
    pub events: Vec<Value>,
    pub script: crate::script::ScriptState,
    pub data_dir: PathBuf,
    pub settings: Map<String, Value>,
    pub mcp_port: u16,
    pub agent: crate::agent::AgentState,
    /// Document snapshot taken when a drag starts; committed as one undo step on release.
    pub drag_before: Option<Value>,
    /// Whether the document was unchanged since its last save when the drag started.
    pub drag_clean: bool,
    /// `execute` calls the model is still writing (key = tool call id).
    pub streams: Vec<Stream>,
    /// Document as displayed while streams are active: real doc + every stream's complete statements.
    pub preview: Option<Doc>,
    /// True while JavaScript runs; layout queries then read the document being edited.
    pub executing: bool,
    /// Visible canvas area in document units (x, y, w, h), reported by the UI.
    pub viewport: Option<(f64, f64, f64, f64)>,
    /// The OS asks for reduced motion: agent inserts appear in place instead of sliding in.
    pub reduce_motion: bool,
    /// Key of the last undo step when it may absorb the next edit with the same key
    /// (one inspector field focus or scrub gesture = one undo step).
    coalesce: Option<String>,
    /// Nodes that already slid in: a streamed insert committed again, undo/redo or a preview
    /// rebuild never replays the animation.
    slid: std::collections::HashSet<String>,
    /// Pending image urls whose download thread is still running; opening a file keeps them.
    pub inflight: std::collections::HashSet<String>,
}

pub struct Stream {
    pub key: String,
    pub agent: String,
    pub source: String,
    pub seed: u64,
    pub touched: usize,
}

static ENGINE: OnceLock<Mutex<Engine>> = OnceLock::new();

/// The process-wide engine. Poisoned locks are recovered: state stays usable after a panic.
pub fn engine() -> MutexGuard<'static, Engine> {
    ENGINE.get_or_init(|| Mutex::new(Engine::new(crate::render::data_dir()))).lock().unwrap_or_else(|e| e.into_inner())
}

/// One `execute` call between `begin_execute` and `end_execute`.
struct ExecuteCall {
    args: Value,
    seen: usize,
    agent: String,
}

/// Two `execute` calls must not interleave: each edits the document in place and rolls back by
/// restoring a snapshot.
static EXECUTE_GATE: Mutex<()> = Mutex::new(());

/// A user edit waits here while a script runs: the script rolls back by restoring its snapshot,
/// which would also throw away an edit made in between.
pub fn edit_gate() -> MutexGuard<'static, ()> {
    EXECUTE_GATE.lock().unwrap_or_else(|e| e.into_inner())
}

/// Dispatch one agent tool call without holding the engine across it. `execute` runs its JavaScript
/// with the engine free (each host function locks it for its own work), so polls, renders and
/// Stop keep answering while a long script runs; the other tools run under the lock.
pub fn call_tool(name: &str, args: &Value) -> ToolOut {
    if name != "execute" {
        return engine().tool(name, args);
    }
    let _gate = EXECUTE_GATE.lock().unwrap_or_else(|e| e.into_inner());
    let mut e = engine();
    let call = e.begin_execute(args);
    // Stop reaches a running built-in agent only; anyone else would see a stale flag.
    let cancel = (e.agent.running && e.agent.profile(&call.agent).is_some()).then(|| e.agent.cancel.clone());
    let (out, mut e) = crate::script::execute_shared(e, &call.args, cancel);
    e.end_execute(&call);
    out
}

/// A key as typed or set: surrounding whitespace is dropped and a blank key is none.
fn nonblank(v: Option<String>) -> Option<String> {
    v.map(|k| k.trim().to_string()).filter(|k| !k.is_empty())
}

/// A file saved while a background image was loading keeps its pending url, and no thread will ever
/// resolve it: count it as a failed load (empty url, like `resolve_pending`) so the node settles.
/// Urls in `inflight` belong to a download of this process, which still resolves them.
fn drop_pending(v: &mut Value, inflight: &std::collections::HashSet<String>) {
    match v {
        Value::Array(a) => a.iter_mut().for_each(|x| drop_pending(x, inflight)),
        Value::Object(o) => {
            for (k, x) in o.iter_mut() {
                match x {
                    Value::String(s) if k == "url" && crate::render::is_pending_url(s) && !inflight.contains(s.as_str()) => s.clear(),
                    _ => drop_pending(x, inflight),
                }
            }
        }
        _ => {}
    }
}

/// `ours` with the changes that turned `base` into `theirs` applied; where both changed the same
/// value, `theirs` wins. Lists of nodes (`id`) merge per node, the rest as a whole.
fn merge3(base: &Value, theirs: &Value, ours: &Value) -> Value {
    if theirs == base {
        return ours.clone();
    }
    if ours == base {
        return theirs.clone();
    }
    match (base, theirs, ours) {
        (Value::Object(b), Value::Object(t), Value::Object(o)) => {
            let mut out = Map::new();
            for (k, ov) in o {
                match (b.get(k), t.get(k)) {
                    (Some(bv), Some(tv)) => {
                        out.insert(k.clone(), merge3(bv, tv, ov));
                    }
                    (Some(bv), None) if bv == ov => {}
                    (None, Some(tv)) => {
                        out.insert(k.clone(), merge3(&Value::Null, tv, ov));
                    }
                    (None, None) => {
                        out.insert(k.clone(), ov.clone());
                    }
                    (Some(_), None) => {}
                }
            }
            for (k, tv) in t {
                if !o.contains_key(k) && !b.contains_key(k) {
                    out.insert(k.clone(), tv.clone());
                }
            }
            Value::Object(out)
        }
        (Value::Array(b), Value::Array(t), Value::Array(o)) if [b, t, o].iter().all(|a| a.iter().all(|n| n["id"].is_string())) => {
            let find = |a: &'_ [Value], id: &Value| a.iter().find(|n| n["id"] == *id).cloned();
            let has = |a: &[Value], id: &Value| a.iter().any(|n| n["id"] == *id);
            let mut out: Vec<Value> = Vec::new();
            for n in o {
                match (find(b, &n["id"]), find(t, &n["id"])) {
                    (Some(bn), Some(tn)) => out.push(merge3(&bn, &tn, n)),
                    (None, _) => out.push(n.clone()),
                    (Some(_), None) => {}
                }
            }
            for (i, n) in t.iter().enumerate() {
                if !has(b, &n["id"]) && !has(o, &n["id"]) {
                    let at = t[..i].iter().rev().find_map(|p| out.iter().position(|x| x["id"] == p["id"]).map(|j| j + 1)).unwrap_or(0);
                    out.insert(at, n.clone());
                }
            }
            // A reorder by `theirs` re-sequences the shared nodes in the slots they already fill.
            let shared = |a: &[Value], other: &[Value]| a.iter().filter(|n| has(other, &n["id"])).map(|n| n["id"].clone()).collect::<Vec<_>>();
            if shared(b, t) != shared(t, b) {
                let slots: Vec<usize> = (0..out.len()).filter(|&i| has(t, &out[i]["id"])).collect();
                let mut moved: Vec<Value> = slots.iter().map(|&i| out[i].clone()).collect();
                moved.sort_by_key(|n| t.iter().position(|x| x["id"] == n["id"]));
                for (&i, n) in slots.iter().zip(moved) {
                    out[i] = n;
                }
            }
            Value::Array(out)
        }
        _ => theirs.clone(),
    }
}

const UNDO_LIMIT: usize = 100;
/// The agent edits whatever document is open, so a document switch waits for it to stop.
pub const SWITCH_BUSY: &str = "Stop the agent before opening another document.";
/// An external agent has no Save / Don't save prompt, so it must not replace unsaved work.
pub const SWITCH_DIRTY: &str = "The open document has unsaved changes; ask the user to save it first.";

impl Engine {
    pub fn new(data_dir: PathBuf) -> Engine {
        let _ = std::fs::create_dir_all(&data_dir);
        let settings = std::fs::read_to_string(data_dir.join("settings.json"))
            .ok()
            .and_then(|t| serde_json::from_str::<Value>(&t).ok())
            .and_then(|v| v.as_object().cloned())
            .unwrap_or_default();
        let mut e = Engine {
            doc: Doc::new(),
            libs: Vec::new(),
            fonts: Fonts::new(data_dir.join("fonts")),
            assets: Assets::new(data_dir.join("cache")),
            renderer: Renderer::default(),
            version: 1,
            synced: 0,
            saved: 1,
            undo: Vec::new(),
            redo: Vec::new(),
            selection: Vec::new(),
            events: Vec::new(),
            script: Default::default(),
            data_dir,
            settings,
            mcp_port: 0,
            agent: Default::default(),
            drag_before: None,
            drag_clean: false,
            streams: Vec::new(),
            preview: None,
            executing: false,
            viewport: None,
            reduce_motion: false,
            coalesce: None,
            slid: Default::default(),
            inflight: Default::default(),
        };
        e.load_template("blank");
        e.saved = e.version;
        e
    }

    pub fn offline(&mut self) {
        self.fonts.online = false;
        self.assets.online = false;
    }

    /// Bring the cached render layers up to date with the document.
    pub fn sync(&mut self) {
        if self.synced == self.version {
            return;
        }
        let doc = match &self.preview {
            Some(p) if !self.executing => p,
            _ => &self.doc,
        };
        let mut cx = Ctx { doc, libs: &self.libs, fonts: &mut self.fonts, assets: &mut self.assets };
        let roots = self.renderer.shift_siblings(&mut cx);
        let rebuilt = self.renderer.sync(&mut cx);
        if !self.renderer.slides.is_empty() {
            self.renderer.refresh_slides(&mut cx, &rebuilt, roots);
        }
        self.synced = self.version;
    }

    /// An agent `Insert` below the top level slides into place.
    pub fn slide_in(&mut self, id: &str) {
        // Sibling glides start from these slides too, so this one check stills both.
        if self.reduce_motion {
            return;
        }
        // Past the cap nothing more slides, so skip the parent lookup (a linear search per insert).
        if self.renderer.slides.len() >= crate::render::MAX_SLIDES {
            self.slid.insert(id.to_string());
            return;
        }
        let room = self.doc.parent_of(id).is_some_and(|(p, _)| self.renderer.can_slide_into(&self.doc, &p));
        if self.slid.insert(id.to_string()) && room {
            self.renderer.slides.push(crate::render::Slide::new(id));
        }
    }

    /// Settled slides go back into the cached view at the next sync (the document is unchanged).
    pub fn expire_slides(&mut self) {
        if self.renderer.expire_slides() {
            self.synced = 0;
        }
    }

    /// The model streamed more of an `execute` call: run its newly completed statements, once,
    /// in the stream's live JS context against the preview document, so the design appears
    /// while the agent is still writing. Anything that is not a plain append rebuilds the preview.
    pub fn stream_update(&mut self, key: &str, agent: &str, source: &str) {
        let prefix = &source[..crate::stream::complete_prefix(source)];
        let i = match self.streams.iter().position(|s| s.key == key) {
            Some(i) if self.streams[i].source == prefix => return,
            Some(i) => {
                self.streams[i].source = prefix.to_string();
                i
            }
            None => {
                self.streams.push(Stream {
                    key: key.to_string(),
                    agent: agent.to_string(),
                    source: prefix.to_string(),
                    seed: crate::svg::fxhash(key),
                    touched: 0,
                });
                self.streams.len() - 1
            }
        };
        let clean = !self.dirty();
        if let Some(mut preview) = self.preview.take() {
            let seed = self.streams[i].seed;
            std::mem::swap(&mut self.doc, &mut preview);
            let touched = crate::script::stream_step(self, key, agent, prefix, seed);
            std::mem::swap(&mut self.doc, &mut preview);
            self.preview = Some(preview);
            if let Some(touched) = touched {
                self.redraw(clean);
                let s = &mut self.streams[i];
                let seen = std::mem::replace(&mut s.touched, touched.len());
                if touched.len() > seen {
                    let ev = json!({"type": "agent_activity", "agent": s.agent, "op": "write", "ids": touched[seen..], "streaming": true});
                    self.emit(ev);
                }
                return;
            }
        }
        self.rebuild_preview();
    }

    pub fn stream_end(&mut self, key: &str) {
        let before = self.streams.len();
        self.streams.retain(|s| s.key != key);
        if self.streams.len() != before {
            self.rebuild_preview();
        }
    }

    pub fn streams_of_agent_end(&mut self, agent: &str) {
        let keys: Vec<String> = self.streams.iter().filter(|s| s.agent == agent).map(|s| s.key.clone()).collect();
        for k in keys {
            self.stream_end(&k);
        }
    }

    /// The final `execute` for a streamed call arrived: retire its preview and reuse its id seed.
    fn take_stream(&mut self, key: Option<&str>, source: &str) -> Option<Stream> {
        let src = source.trim();
        let i = self.streams.iter().position(|s| key == Some(s.key.as_str()) || (!s.source.trim().is_empty() && src.starts_with(s.source.trim())))?;
        let s = self.streams.remove(i);
        self.rebuild_preview();
        Some(s)
    }

    /// Preview from scratch: real document + every stream's complete statements, each stream
    /// in a fresh JS context.
    fn rebuild_preview(&mut self) {
        crate::script::reset_streams(self);
        let clean = !self.dirty();
        if self.streams.is_empty() {
            if self.preview.take().is_some() {
                self.redraw(clean);
            }
            return;
        }
        let mut scratch = self.doc.clone();
        std::mem::swap(&mut self.doc, &mut scratch);
        let jobs: Vec<(String, String, String, u64, usize)> = self.streams.iter().map(|s| (s.key.clone(), s.agent.clone(), s.source.clone(), s.seed, s.touched)).collect();
        let mut activity = Vec::new();
        for (i, (key, agent, source, seed, seen)) in jobs.into_iter().enumerate() {
            let touched = crate::script::stream_step(self, &key, &agent, &source, seed).unwrap_or_default();
            if touched.len() > seen {
                activity.push(json!({"type": "agent_activity", "agent": agent, "op": "write", "ids": touched[seen..].to_vec(), "streaming": true}));
            }
            self.streams[i].touched = touched.len();
        }
        std::mem::swap(&mut self.doc, &mut scratch);
        self.preview = Some(scratch);
        self.redraw(clean);
        for a in activity {
            self.emit(a);
        }
    }

    pub fn ctx(&mut self) -> Ctx<'_> {
        Ctx { doc: &self.doc, libs: &self.libs, fonts: &mut self.fonts, assets: &mut self.assets }
    }

    /// Record `before` as an undo step and mark the document changed. A commit that lands in the
    /// middle of a drag (an agent's `execute`) is its own step taken from the pre-drag document, and
    /// the drag's snapshot takes the change along, so the drag undoes without it.
    pub fn commit(&mut self, before: Value) {
        if same(&before, &self.doc.root) {
            return;
        }
        let before = match self.drag_before.take() {
            Some(start) => {
                self.drag_before = Some(merge3(&before, &self.doc.root, &start));
                start
            }
            None => before,
        };
        self.coalesce = None;
        self.undo.push(before);
        if self.undo.len() > UNDO_LIMIT {
            self.undo.remove(0);
        }
        self.redo.clear();
        self.touch();
    }

    /// Commit a drag (or batch) that started from `before`. If it ended where it started, no undo
    /// step is added and a document that was clean when it began stays clean.
    pub fn end_drag(&mut self, before: Value, was_clean: bool) {
        if same(&before, &self.doc.root) {
            self.keep_clean(was_clean);
        } else {
            self.commit(before);
        }
    }

    /// The preview changed, not the document: render again, and a document that was `clean` before
    /// the preview ran (its host functions bump the version too) stays clean.
    fn redraw(&mut self, clean: bool) {
        self.version += 1;
        self.keep_clean(clean);
    }

    pub fn touch(&mut self) {
        self.version += 1;
        self.selection.retain(|id| id.contains('/') || self.doc.find(id).is_some());
        // Keep in-flight agent previews layered on top of the latest document.
        if !self.streams.is_empty() && !self.executing {
            self.rebuild_preview();
        }
    }

    /// Undo and redo are ignored during a drag: `drag_end` would otherwise push the pre-drag snapshot
    /// as a new step over the swapped document.
    pub fn undo(&mut self) -> bool {
        if self.drag_before.is_some() {
            return false;
        }
        let Some(prev) = self.undo.pop() else { return false };
        self.coalesce = None;
        self.redo.push(std::mem::replace(&mut self.doc.root, prev));
        self.touch();
        true
    }

    pub fn redo(&mut self) -> bool {
        if self.drag_before.is_some() {
            return false;
        }
        let Some(next) = self.redo.pop() else { return false };
        self.coalesce = None;
        self.undo.push(std::mem::replace(&mut self.doc.root, next));
        self.touch();
        true
    }

    /// During a drag, mutations apply live and are committed once by `drag_end`.
    pub fn edit_or_drag<R>(&mut self, f: impl FnOnce(&mut Doc) -> Result<R, String>) -> Result<R, String> {
        if self.drag_before.is_none() {
            return self.edit(f);
        }
        let r = f(&mut self.doc)?;
        self.touch();
        Ok(r)
    }

    /// Like `edit`, but consecutive edits with the same `key` share one undo step.
    pub fn edit_coalesced<R>(&mut self, key: &str, f: impl FnOnce(&mut Doc) -> Result<R, String>) -> Result<R, String> {
        if self.drag_before.is_some() {
            return self.edit_or_drag(f);
        }
        let merge = self.coalesce.as_deref() == Some(key) && !self.undo.is_empty();
        let before = self.doc.root.clone();
        let r = f(&mut self.doc).inspect_err(|_| self.doc.root = before.clone())?;
        if !same(&before, &self.doc.root) {
            if merge {
                self.redo.clear();
                self.touch();
            } else {
                self.commit(before);
            }
            self.coalesce = Some(key.to_string());
        }
        Ok(r)
    }

    /// Run several edits as one undo step (they must use `edit_or_drag`); all or nothing.
    pub fn batch<R>(&mut self, f: impl FnOnce(&mut Engine) -> Result<R, String>) -> Result<R, String> {
        if self.drag_before.is_some() {
            return f(self);
        }
        let before = self.doc.root.clone();
        let was_clean = !self.dirty();
        self.drag_before = Some(before.clone());
        let r = f(self);
        self.drag_before = None;
        match r {
            Ok(_) => self.end_drag(before, was_clean),
            Err(_) => {
                self.doc.root = before;
                self.touch();
                self.keep_clean(was_clean);
            }
        }
        r
    }

    /// After a rolled-back edit the document is as it was: if it was clean before the edit, it stays
    /// clean (a document switch is refused while it is dirty), however far the version moved.
    pub fn keep_clean(&mut self, was_clean: bool) {
        if was_clean {
            self.saved = self.version;
        }
    }

    /// Apply a document mutation as one undoable step.
    pub fn edit<R>(&mut self, f: impl FnOnce(&mut Doc) -> Result<R, String>) -> Result<R, String> {
        let before = self.doc.root.clone();
        match f(&mut self.doc) {
            Ok(r) => {
                self.commit(before);
                Ok(r)
            }
            Err(e) => {
                self.doc.root = before;
                Err(e)
            }
        }
    }

    pub fn dirty(&self) -> bool {
        self.version != self.saved
    }

    pub fn title(&self) -> String {
        self.doc.path.as_ref().and_then(|p| p.file_stem()).map(|s| s.to_string_lossy().to_string()).unwrap_or_else(|| "Untitled".into())
    }

    pub fn emit(&mut self, ev: Value) {
        // Agent events also build the saved chat; it is written when a message or turn ends.
        if ev["type"] == "agent" && self.agent.record(&ev) {
            if let Err(err) = crate::agent::save_chat(&self.data_dir, &self.agent) {
                self.events.push(json!({"type": "toast", "text": format!("Could not save the chat: {err}")}));
            }
        }
        self.events.push(ev);
        if self.events.len() > 2000 {
            self.events.drain(..1000);
        }
    }

    fn reset_history(&mut self) {
        self.undo.clear();
        self.redo.clear();
        self.selection.clear();
        self.renderer.slides.clear();
        self.slid.clear();
        self.script = Default::default();
        self.touch();
        self.saved = self.version;
    }

    pub fn open(&mut self, path: &Path) -> Result<(), String> {
        let mut doc = Doc::load(path)?;
        crate::resolve::migrate(&mut doc.root);
        drop_pending(&mut doc.root, &self.inflight);
        self.doc = doc;
        self.load_libs();
        self.reset_history();
        self.remember_recent(path);
        Ok(())
    }

    pub fn load_template(&mut self, name: &str) {
        let text = match name {
            "slides" => include_str!("../templates/slides.json"),
            "mobile" => include_str!("../templates/mobile.json"),
            "empty" => r#"{"version":"2.19","children":[]}"#,
            _ => include_str!("../templates/blank.json"),
        };
        self.doc = Doc::from_json(text).unwrap_or_default();
        self.libs.clear();
        self.reset_history();
    }

    /// Load `imports` (alias → relative .pen path) so `alias:id` refs resolve. One level deep.
    fn load_libs(&mut self) {
        self.libs.clear();
        let Some(imports) = self.doc.root["imports"].as_object().cloned() else { return };
        let base = self.doc.dir().unwrap_or_default();
        for (alias, uri) in imports {
            let Some(rel) = uri.as_str() else { continue };
            let rel = rel.replace("%20", " ");
            if let Ok(mut lib) = Doc::load(&base.join(&rel)) {
                crate::resolve::migrate(&mut lib.root);
                drop_pending(&mut lib.root, &self.inflight);
                self.libs.push((alias, lib.root));
            }
        }
    }

    pub fn save(&mut self, path: Option<&str>) -> Result<String, String> {
        let target = match path.filter(|p| !p.is_empty()) {
            Some(p) => PathBuf::from(p),
            None => self.doc.path.clone().ok_or("document has no path yet; pass a path to save")?,
        };
        self.doc.root["version"] = json!(crate::doc::FORMAT_VERSION);
        self.doc.save(&target)?;
        self.doc.path = Some(target.clone());
        self.saved = self.version;
        self.remember_recent(&target);
        Ok(target.to_string_lossy().to_string())
    }

    fn remember_recent(&mut self, path: &Path) {
        let p = path.to_string_lossy().to_string();
        let mut recent: Vec<Value> = self.settings.get("recent").and_then(Value::as_array).cloned().unwrap_or_default();
        recent.retain(|r| r.as_str() != Some(&p));
        recent.insert(0, json!(p));
        recent.truncate(20);
        self.settings.insert("recent".into(), Value::Array(recent));
        let _ = self.save_settings(); // the document is saved; a recents list that did not save must not fail it
    }

    /// Set one setting (`null` removes it) and save. If the save fails the setting keeps its old value,
    /// so an error means nothing changed, in memory or on disk.
    pub fn set_setting(&mut self, key: &str, value: Value) -> Result<(), String> {
        let old = if value.is_null() { self.settings.remove(key) } else { self.settings.insert(key.to_string(), value) };
        let saved = self.save_settings();
        if saved.is_err() {
            match old {
                Some(v) => self.settings.insert(key.to_string(), v),
                None => self.settings.remove(key),
            };
        }
        saved
    }

    /// Atomic like `Doc::save`: a crash or a reader mid-save never sees a truncated file (which would drop
    /// every setting, the API key included, at the next start).
    pub fn save_settings(&self) -> Result<(), String> {
        let text = serde_json::to_string_pretty(&self.settings).map_err(|e| e.to_string())?;
        let path = self.data_dir.join("settings.json");
        let tmp = path.with_extension("json.tmp");
        let saved = std::fs::write(&tmp, text).and_then(|_| std::fs::rename(&tmp, &path));
        if saved.is_err() {
            let _ = std::fs::remove_file(&tmp); // a failed save leaves no copy of the new settings (an API key) behind
        }
        saved.map_err(|e| format!("cannot save {}: {e}", path.display()))
    }

    pub fn setting(&self, key: &str) -> Option<String> {
        self.settings.get(key).and_then(Value::as_str).filter(|s| !s.is_empty()).map(str::to_string)
    }

    /// The Anthropic API key: the setting, else `ANTHROPIC_API_KEY`. A blank one of either is no key
    /// (`ANTHROPIC_API_KEY=` is common in CI and devcontainers).
    pub fn api_key(&self) -> Option<String> {
        nonblank(self.setting("anthropic_api_key")).or_else(|| nonblank(std::env::var("ANTHROPIC_API_KEY").ok()))
    }

    /// The agent backend: the setting, else the API when a key is available, else the Claude Code CLI.
    pub fn backend(&self) -> String {
        self.setting("agent_backend").unwrap_or_else(|| if self.api_key().is_some() { "api".into() } else { "claude-code".into() })
    }

    /// Markdown summary the agent reads before editing.
    pub fn editor_state(&mut self) -> String {
        self.sync();
        let mut s = String::from("# Nalarupa editor state\n\n");
        let path = self.doc.path.as_ref().map(|p| p.display().to_string()).unwrap_or_else(|| "Untitled (not saved yet)".into());
        s += &format!("- Document: {path}{}\n", if self.dirty() { " (unsaved changes)" } else { "" });
        let mut sel: Vec<String> = self.selection.iter().take(40).map(|id| match self.renderer.find(id) {
            Some(f) => format!("`{id}` \"{}\" {} {}x{}", f.name, f.ty, f.w.round(), f.h.round()),
            None => format!("`{id}`"),
        }).collect();
        if self.selection.len() > 40 {
            sel.push(format!("... +{} others", self.selection.len() - 40));
        }
        s += &format!("- Selection: {}\n", if sel.is_empty() { "nothing".into() } else { sel.join(", ") });
        let vp = self.viewport;
        let mut tops: Vec<(bool, String)> = self.doc.children().iter().filter_map(|n| {
            let id = n["id"].as_str()?;
            let f = self.renderer.find(id)?;
            let visible = vp.is_none_or(|(x, y, w, h)| f.x < x + w && f.y < y + h && f.x + f.w > x && f.y + f.h > y);
            Some((visible, format!("  - `{id}` \"{}\" {} at ({}, {}) size {}x{}{}{}", f.name, f.ty, f.x.round(), f.y.round(), f.w.round(), f.h.round(),
                if n["reusable"] == true { " [component]" } else { "" },
                if vp.is_some() { if visible { " [user visible]" } else { " [outside viewport]" } } else { "" })))
        }).collect();
        tops.sort_by_key(|(visible, _)| !visible);
        let more = tops.len().saturating_sub(40);
        let mut tops: Vec<String> = tops.into_iter().take(40).map(|(_, l)| l).collect();
        if more > 0 {
            tops.push(format!("  - ... +{more} others"));
        }
        s += &format!("- Top-level nodes ({}):\n{}\n", self.doc.children().len(), tops.join("\n"));
        let mut comps = Vec::new();
        crate::doc::walk(self.doc.children(), &mut |n| {
            if n["reusable"] == true && comps.len() < 80 {
                comps.push(format!("`{}` \"{}\"", n["id"].as_str().unwrap_or(""), n["name"].as_str().unwrap_or("")));
            }
        });
        if !comps.is_empty() {
            s += &format!("- Components: {}\n", comps.join(", "));
        }
        let vars = self.doc.root["variables"].as_object().map(|v| v.len()).unwrap_or(0);
        let themes = self.doc.root["themes"].as_object().map(|t| t.keys().cloned().collect::<Vec<_>>().join(", ")).unwrap_or_default();
        s += &format!("- Variables: {vars}{}\n", if themes.is_empty() { String::new() } else { format!(" (theme axes: {themes})") });
        if let Some(b) = self.renderer.bounds() {
            s += &format!("- Canvas content bounds: x {} y {} w {} h {}\n", b.0.round(), b.1.round(), b.2.round(), b.3.round());
        }
        s += "\nCall `read_guide` (no arguments) before your first `execute`; it explains the .pen schema and the execute API.\n";
        s
    }

    /// Retire the stream an `execute` call belongs to; the call runs with its id seed.
    fn begin_execute(&mut self, args: &Value) -> ExecuteCall {
        let stream = self.take_stream(args["__stream"].as_str(), args["input"].as_str().unwrap_or(""));
        let mut args = args.clone();
        if let Some(s) = &stream {
            args["__seed"] = json!(s.seed);
        }
        let seen = stream.as_ref().map_or(0, |s| s.touched);
        let agent = stream.map(|s| s.agent).or_else(|| args["__agent"].as_str().map(str::to_string)).unwrap_or_else(|| "agent".into());
        self.script.edit = None;
        ExecuteCall { args, seen, agent }
    }

    /// Report what a finished `execute` call wrote and read.
    fn end_execute(&mut self, call: &ExecuteCall) {
        let ExecuteCall { seen, agent, .. } = call;
        // Not found in `events`: a poll may have drained them while the script ran.
        if let Some(ev) = self.script.edit.take() {
            let writes: Vec<Value> = ev["ids"].as_array().map(|a| a.iter().skip(*seen).cloned().collect()).unwrap_or_default();
            if !writes.is_empty() {
                self.emit(json!({"type": "agent_activity", "agent": agent, "op": "write", "ids": writes, "streaming": false}));
            }
            if ev["read"].as_array().is_some_and(|r| !r.is_empty()) {
                self.emit(json!({"type": "agent_activity", "agent": agent, "op": "read", "ids": ev["read"], "streaming": false}));
            }
        }
    }

    /// Dispatch one agent tool call (shared by MCP and the built-in agent).
    pub fn tool(&mut self, name: &str, args: &Value) -> ToolOut {
        match name {
            "get_editor_state" => ToolOut::ok(self.editor_state()),
            "read_guide" => crate::guide::read(args["topic"].as_str().unwrap_or("")),
            "get_style" => crate::guide::style(args["name"].as_str().unwrap_or("")),
            "execute" if self.executing => ToolOut::err("Another execute call is still running."),
            "execute" => {
                let call = self.begin_execute(args);
                let out = crate::script::execute(self, &call.args);
                self.end_execute(&call);
                out
            }
            "open_document" => {
                if self.agent.running || self.executing {
                    return ToolOut::err(SWITCH_BUSY);
                }
                if self.dirty() {
                    return ToolOut::err(SWITCH_DIRTY);
                }
                let Some(path) = args["path"].as_str() else { return ToolOut::err("path is required") };
                let p = Path::new(path);
                // A missing file is created on disk first, so a failed create leaves the open document alone.
                let created = if p.exists() {
                    Ok(())
                } else {
                    Doc::new().save(p)
                };
                let res = created.and_then(|_| self.open(p));
                match res {
                    Ok(()) => {
                        self.emit(json!({"type": "document_opened"}));
                        ToolOut::ok(format!("Opened {path}"))
                    }
                    Err(e) => ToolOut::err(e),
                }
            }
            _ => ToolOut::err(format!("unknown tool: {name}")),
        }
    }

    /// A background image finished: swap the pending url for the real one everywhere
    /// (no undo step). Empty url = failed.
    pub fn resolve_pending(&mut self, pending: &str, url: &str) {
        self.inflight.remove(pending);
        fn walk(v: &mut Value, from: &str, to: &str) -> bool {
            match v {
                Value::String(s) if s == from => {
                    *s = to.to_string();
                    true
                }
                Value::Array(a) => a.iter_mut().fold(false, |acc, x| walk(x, from, to) || acc),
                Value::Object(o) => o.values_mut().fold(false, |acc, x| walk(x, from, to) || acc),
                _ => false,
            }
        }
        let mut hit = walk(&mut self.doc.root, pending, url);
        for v in self.undo.iter_mut().chain(self.redo.iter_mut()).chain(self.libs.iter_mut().map(|(_, v)| v)) {
            hit |= walk(v, pending, url);
        }
        if hit {
            self.touch();
            if url.is_empty() {
                self.emit(json!({"type": "toast", "text": "Failed to load a generated image"}));
            }
        }
    }

    /// A background font download finished: text that used a fallback face lays out again.
    /// The document is unchanged, so a clean one stays clean.
    pub fn font_ready(&mut self, family: &str, weight: u16, italic: bool) {
        if self.fonts.downloaded(family, weight, italic) {
            let clean = !self.dirty();
            self.redraw(clean);
        }
    }

    /// A background icon or image download finished (or failed): layers that drew without it draw again.
    /// The document is unchanged, so a clean one stays clean.
    pub fn asset_ready(&mut self, key: &str, ok: bool) {
        self.assets.downloaded(key, ok);
        let clean = !self.dirty();
        self.redraw(clean);
    }

    /// A background generator (the AI Pen) drew into node `id`: set `props` on it without an undo
    /// step, in the document and the live preview. When `done`,
    /// every undo/redo snapshot holding the node gets them too, so history never brings back a
    /// half-drawn result. False when the node is gone.
    pub fn apply_generated(&mut self, id: &str, props: &Map<String, Value>, done: bool) -> bool {
        fn node<'a>(v: &'a mut Value, id: &str) -> Option<&'a mut Value> {
            if v["id"] == id {
                return Some(v);
            }
            v.get_mut("children")?.as_array_mut()?.iter_mut().find_map(|k| node(k, id))
        }
        let set = |v: &mut Value| {
            node(v, id).map(|n| {
                for (k, x) in props {
                    n[k] = x.clone();
                }
            }).is_some()
        };
        let hit = set(&mut self.doc.root);
        if let Some(p) = &mut self.preview {
            set(&mut p.root);
        }
        if done {
            for v in self.undo.iter_mut().chain(self.redo.iter_mut()) {
                set(v);
            }
        }
        if hit {
            self.version += 1;
        }
        hit
    }

    pub fn select_top_level(&self) -> Vec<String> {
        self.doc.children().iter().filter_map(|n| n["id"].as_str().map(str::to_string)).collect()
    }

    pub fn insert_root(&mut self, node: Value) -> Result<String, String> {
        self.edit(|d| d.insert(ROOT, node, None))
    }
}

/// Tool schemas advertised over MCP and to the built-in agent.
pub fn tool_specs() -> Value {
    json!([
        {"name": "get_editor_state", "description": "Summary of the open Nalarupa document: path, selection, top-level frames, components, variables. Call this first.",
         "inputSchema": {"type": "object", "properties": {}}},
        {"name": "read_guide", "description": "Read Nalarupa's design guide. No topic returns the overview and the list of topics (schema, execute, components, slides, design, code).",
         "inputSchema": {"type": "object", "properties": {"topic": {"type": "string", "description": "Guide topic, e.g. 'schema', 'execute', 'slides'"}}}},
        {"name": "get_style", "description": "Ready-made visual style archetypes (fonts, palette, radius, elevation). No name lists them.",
         "inputSchema": {"type": "object", "properties": {"name": {"type": "string"}}}},
        {"name": "execute", "description": "Run a JavaScript snippet against the canvas (Insert/Update/Copy/Replace/Move/Delete/Get/SetVariables/TakeScreenshot/Export...). All operations in one call are a single undoable transaction; on error everything is rolled back. Read the 'execute' guide first. To fix a failed call, send `editId` with `edits` instead of resending the snippet.",
         "inputSchema": {"type": "object", "properties": {
            "input": {"type": "string", "description": "JavaScript source"},
            "editId": {"type": "string", "description": "editId from a failed call, used with edits"},
            "edits": {"type": "array", "items": {"type": "object", "properties": {"find": {"type": "string"}, "replace": {"type": "string"}, "all": {"type": "boolean"}}, "required": ["find", "replace"]}}
         }}},
        {"name": "open_document", "description": "Open a .pen/.nala file in the editor, or create it when it does not exist.",
         "inputSchema": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}}
    ])
}

/// Numbers equal by value: integers exactly (not through `f64`, which merges values above 2^53),
/// an integer and a float only when the float holds that very integer.
fn same_number(x: &Number, y: &Number) -> bool {
    let int = |n: &Number| n.as_i64().map(i128::from).or_else(|| n.as_u64().map(i128::from));
    match (int(x), int(y)) {
        (Some(i), Some(j)) => i == j,
        (Some(i), None) | (None, Some(i)) => {
            let f = if x.is_f64() { x.as_f64() } else { y.as_f64() }.unwrap_or(f64::NAN);
            f.fract() == 0.0 && f as i128 == i
        }
        (None, None) => x.as_f64() == y.as_f64(),
    }
}

/// Equal as documents: a whole number written back as a float (`10` vs `10.0`) is not a change.
fn same(a: &Value, b: &Value) -> bool {
    match (a, b) {
        (Value::Number(x), Value::Number(y)) => same_number(x, y),
        (Value::Array(x), Value::Array(y)) => x.len() == y.len() && x.iter().zip(y).all(|(x, y)| same(x, y)),
        (Value::Object(x), Value::Object(y)) => x.len() == y.len() && x.iter().all(|(k, v)| y.get(k).is_some_and(|w| same(v, w))),
        _ => a == b,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn drag_that_ends_where_it_started_leaves_no_undo_step() {
        let mut e = Engine::new(std::env::temp_dir().join(format!("nalarupa-test-drag-noop-{}", std::process::id())));
        e.doc.root = json!({"children": [{"id": "a", "type": "rectangle", "x": 10, "y": 10}]});
        e.drag_before = Some(e.doc.root.clone());
        e.doc.root["children"][0]["x"] = json!(40.0);
        e.doc.root["children"][0]["x"] = json!(10.0);
        let before = e.drag_before.take().unwrap();
        e.commit(before);
        assert!(e.undo.is_empty(), "10 written back as 10.0 is not a change");
        e.doc.root["children"][0]["x"] = json!(10.5);
        e.commit(json!({"children": [{"id": "a", "type": "rectangle", "x": 10, "y": 10}]}));
        assert_eq!(e.undo.len(), 1);
    }

    #[test]
    fn unchanged_drag_keeps_a_clean_document_clean() {
        let mut e = Engine::new(std::env::temp_dir().join(format!("nalarupa-test-drag-clean-{}", std::process::id())));
        e.doc.root = json!({"children": [{"id": "a", "type": "rectangle", "x": 10}]});
        e.saved = e.version;
        let before = e.doc.root.clone();
        e.doc.root["children"][0]["x"] = json!(10.0);
        e.touch();
        assert!(e.dirty());
        e.end_drag(before, true);
        assert!(!e.dirty() && e.undo.is_empty());
    }

    #[test]
    fn same_number_compares_integers_exactly() {
        assert!(same(&json!(10), &json!(10.0)));
        assert!(!same(&json!(10), &json!(10.5)));
        assert!(!same(&json!(9007199254740992_i64), &json!(9007199254740993_i64)));
        assert!(!same(&json!(9007199254740993_i64), &json!(9007199254740992.0)));
        assert!(same(&json!(9007199254740992_i64), &json!(9007199254740992.0)));
        assert!(same(&json!(u64::MAX), &json!(u64::MAX)));
        assert!(!same(&json!(u64::MAX), &json!(-1)));
    }

    #[test]
    fn rewritten_number_does_not_start_a_coalescing_group() {
        let mut e = Engine::new(std::env::temp_dir().join(format!("nalarupa-test-coalesce-noop-{}", std::process::id())));
        e.doc.root = json!({"children": [{"id": "a", "x": 10, "y": 0}]});
        e.edit(|d| { d.root["children"][0]["y"] = json!(1); Ok(()) }).unwrap();
        e.edit_coalesced("x", |d| { d.root["children"][0]["x"] = json!(10.0); Ok(()) }).unwrap();
        assert!(e.coalesce.is_none());
        e.edit_coalesced("x", |d| { d.root["children"][0]["x"] = json!(20); Ok(()) }).unwrap();
        assert_eq!(e.undo.len(), 2, "the real edit is its own undo step");
    }

    #[test]
    fn blank_key_is_none() {
        assert_eq!(nonblank(None), None);
        assert_eq!(nonblank(Some(String::new())), None);
        assert_eq!(nonblank(Some("  \t".into())), None);
        assert_eq!(nonblank(Some(" sk-ant-x\n".into())), Some("sk-ant-x".into()));
    }

    #[test]
    fn merge3_keeps_a_reorder_by_theirs() {
        let n = |id: &str, x: i64| json!({"id": id, "x": x});
        let base = json!({"children": [n("a", 0), n("b", 0), n("c", 0)]});
        let theirs = json!({"children": [n("c", 0), n("a", 0), n("b", 0)]});
        let ours = json!({"children": [n("a", 5), n("b", 0), n("c", 0), n("d", 0)]});
        let m = merge3(&base, &theirs, &ours);
        let ids: Vec<_> = m["children"].as_array().unwrap().iter().map(|n| n["id"].as_str().unwrap()).collect();
        assert_eq!(ids, ["c", "a", "b", "d"]);
        assert_eq!(m["children"][1]["x"], 5);
    }

    #[test]
    fn open_keeps_pending_urls_of_running_downloads() {
        let mut v = json!({"children": [
            {"fill": {"type": "image", "url": "nalarupa:pending-image-1-a"}},
            {"fill": {"type": "image", "url": "nalarupa:pending-image-2-b"}},
            {"fill": {"type": "image", "url": "./images/x.jpg"}}]});
        drop_pending(&mut v, &["nalarupa:pending-image-1-a".to_string()].into());
        assert_eq!(v["children"][0]["fill"]["url"], "nalarupa:pending-image-1-a");
        assert_eq!(v["children"][1]["fill"]["url"], "");
        assert_eq!(v["children"][2]["fill"]["url"], "./images/x.jpg");
    }

    #[test]
    fn resolve_pending_reaches_libraries() {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test-resolve-libs"));
        e.libs.push(("lib".into(), json!({"children": [{"fill": {"type": "image", "url": "nalarupa:pending-image-1-a"}}]})));
        e.inflight.insert("nalarupa:pending-image-1-a".into());
        e.resolve_pending("nalarupa:pending-image-1-a", "./images/x.jpg");
        assert_eq!(e.libs[0].1["children"][0]["fill"]["url"], "./images/x.jpg");
        assert!(e.inflight.is_empty());
    }

    #[test]
    fn failed_settings_save_keeps_old_values() {
        let dir = std::env::temp_dir().join("nalarupa-test-settings-save");
        let _ = std::fs::remove_dir_all(&dir);
        let mut e = Engine::new(dir.clone());
        e.set_setting("k", json!("old")).unwrap();
        let blocker = dir.join("settings.json.tmp");
        std::fs::create_dir_all(&blocker).unwrap(); // a folder where the temp file goes: every save fails
        assert!(e.set_setting("k", json!("new")).is_err());
        assert!(e.set_setting("k", Value::Null).is_err());
        assert!(e.set_setting("other", json!("x")).is_err());
        assert_eq!(e.setting("k").as_deref(), Some("old"));
        assert_eq!(e.setting("other"), None);
        std::fs::remove_dir(&blocker).unwrap();
        e.set_setting("k", Value::Null).unwrap();
        let saved: Value = serde_json::from_str(&std::fs::read_to_string(dir.join("settings.json")).unwrap()).unwrap();
        assert!(saved.get("k").is_none() && !blocker.exists());
        std::fs::remove_file(dir.join("settings.json")).unwrap();
        std::fs::create_dir(dir.join("settings.json")).unwrap(); // the temp file writes, the rename fails
        assert!(e.set_setting("k", json!("new")).is_err());
        assert!(!blocker.exists(), "a failed rename must not leave the new settings in the temp file");
        assert_eq!(e.setting("k"), None);
        let _ = std::fs::remove_dir_all(&dir);
    }

    fn xs(e: &Engine) -> Vec<(String, i64)> {
        e.doc.root["children"].as_array().unwrap().iter().map(|n| (n["name"].as_str().unwrap().to_string(), n["x"].as_i64().unwrap())).collect()
    }

    /// A commit that lands during a drag is its own undo step; the drag undoes without it.
    #[test]
    fn commit_during_drag_keeps_both_steps() {
        let mut e = Engine::new(std::env::temp_dir().join(format!("nalarupa-test-drag-commit-{}", std::process::id())));
        e.load_template("empty");
        e.doc.root["children"] = json!([{"id": "u", "type": "frame", "name": "U", "x": 0}]);
        e.drag_before = Some(e.doc.root.clone());
        e.doc.root["children"][0]["x"] = json!(50);
        let before = e.doc.root.clone(); // the script's snapshot, taken mid-drag
        e.doc.root["children"].as_array_mut().unwrap().push(json!({"id": "a", "type": "frame", "name": "AgentF", "x": 300}));
        e.commit(before);
        e.doc.root["children"][0]["x"] = json!(100);
        let start = e.drag_before.take().unwrap();
        e.commit(start);
        assert_eq!(xs(&e), [("U".into(), 100), ("AgentF".into(), 300)]);
        assert!(e.undo());
        assert_eq!(xs(&e), [("U".into(), 0), ("AgentF".into(), 300)]);
        assert!(e.undo());
        assert_eq!(xs(&e), [("U".into(), 0)]);
        assert!(!e.undo());
    }

    /// Undo and redo during a drag do nothing, so the drag commits as one step on top of the history.
    #[test]
    fn undo_and_redo_are_ignored_during_a_drag() {
        let mut e = Engine::new(std::env::temp_dir().join(format!("nalarupa-test-drag-undo-{}", std::process::id())));
        e.load_template("empty");
        e.doc.root["children"] = json!([{"id": "u", "type": "frame", "name": "U", "x": 0}]);
        e.edit(|d| { d.root["children"][0]["x"] = json!(10); Ok(()) }).unwrap();
        e.edit(|d| { d.root["children"][0]["x"] = json!(20); Ok(()) }).unwrap();
        assert!(e.undo());
        e.drag_before = Some(e.doc.root.clone());
        e.doc.root["children"][0]["x"] = json!(50);
        assert!(!e.undo() && !e.redo());
        assert_eq!((e.undo.len(), e.redo.len()), (1, 1));
        let before = e.drag_before.take().unwrap();
        e.end_drag(before, false);
        assert_eq!(xs(&e), [("U".into(), 50)]);
        assert!(e.undo());
        assert_eq!(xs(&e), [("U".into(), 10)]);
        assert!(e.undo());
        assert_eq!(xs(&e), [("U".into(), 0)]);
        assert!(!e.undo());
    }

    /// A font landing in the background redraws the canvas but is not an edit.
    #[test]
    fn downloaded_font_redraws_without_dirtying() {
        let dir = std::env::temp_dir().join(format!("nalarupa-test-font-ready-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let mut e = Engine::new(dir.clone());
        e.offline();
        let face = e.fonts.db.faces().find(|f| f.weight.0 == 400 && f.style == resvg::usvg::fontdb::Style::Normal).expect("a system font");
        let (family, id) = (face.families[0].0.clone(), face.id);
        let (resvg::usvg::fontdb::Source::File(p) | resvg::usvg::fontdb::Source::SharedFile(p, _)) = &face.source else { panic!("font from a file") };
        let bytes = std::fs::read(p).unwrap();
        std::sync::Arc::make_mut(&mut e.fonts.db).remove_face(id);
        let _ = std::fs::create_dir_all(dir.join("fonts"));
        std::fs::write(dir.join("fonts").join(format!("{}-400.ttf", family.replace(' ', "_"))), bytes).unwrap();
        let before = e.version;
        assert!(!e.dirty());
        e.font_ready(&family, 400, false);
        assert!(e.version > before && !e.dirty());
        e.touch();
        let v = e.version;
        e.font_ready(&family, 400, false);
        assert_eq!(e.version, v, "nothing new loaded: no redraw");
        assert!(e.dirty());
        let _ = std::fs::remove_dir_all(&dir);
    }
}
