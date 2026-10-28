//! `execute`: agent-written JavaScript run in an embedded QuickJS (rquickjs) sandbox against
//! the document. One call = one transaction: any error rolls everything back, success is one
//! undo step, with editId repair, persistent globals, a name→id report and layout warnings.
//! Every call gets a fresh runtime with a wall-clock deadline, a heap cap and a stack cap. A call
//! the model is still streaming keeps one live context, so each completed statement runs once
//! against the preview document.

use crate::engine::{Engine, ToolOut};
use crate::render::find_rnode;
use crate::resolve::RNode;
use rquickjs::context::EvalOptions;
use rquickjs::function::Rest;
use rquickjs::{Coerced, Context, Ctx, Exception, FromJs, Function, Runtime};
use serde_json::{json, Map, Value};
use std::cell::RefCell;
use std::collections::{HashMap, HashSet};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, MutexGuard};
use std::time::{Duration, Instant};

/// Wall-clock budget of one `execute` call.
pub const EXECUTE_TIMEOUT: Duration = Duration::from_secs(30);
/// Budget of one streaming preview step. It runs while the editor waits, so it is short; the
/// final `execute` still gets the full budget.
pub const PREVIEW_TIMEOUT: Duration = Duration::from_secs(2);
/// JS heap cap per call. Agent snippets
/// hold node JSON and `Get` results (a 10k-node document is ~25 MB once parsed), so 64 MB fits
/// large reads while a runaway allocation stays small next to the app.
pub const MEMORY_LIMIT: usize = 64 << 20;
/// Native stack QuickJS may use. Half of the 1 MB Windows main-thread stack,
/// leaving room for the layout code natives run on the same stack.
const STACK_LIMIT: usize = 512 << 10;
/// A job queue is checked against the heap cap every this many jobs; a heap within `OOM_SLACK` of
/// the cap counts as out of memory. Jobs allocate a few hundred bytes each, so the slack outruns
/// the growth between checks.
const HEAP_CHECK_JOBS: u32 = 1024;
const OOM_SLACK: usize = 4 << 20;

#[derive(Default)]
pub struct ScriptState {
    /// Globals earlier calls kept, per agent: agents reuse names like `page`, so one shared map
    /// let the last writer win for everybody.
    pub globals: HashMap<String, Map<String, Value>>,
    pub failed: HashMap<String, String>,
    /// JS state of `execute` calls still streaming, by stream key.
    live: HashMap<String, Live>,
    /// The `agent_edit` event the last finished call emitted, for `end_execute`.
    pub edit: Option<Value>,
}

/// A streamed call whose context has run `ran` against the preview document.
struct Live {
    js: Context,
    ran: String,
    /// Ids drawn so far: the next step resumes the seeded id sequence right there.
    draws: u64,
    touched: Vec<String>,
    /// The last step threw: the next update re-runs the stream from scratch.
    broken: bool,
    /// The last step hit the time or memory limit: the stream stays out of the preview.
    stalled: bool,
}

struct Exec {
    /// The engine the caller holds for the whole script; None when each host function locks it
    /// for its own work (`execute_shared`).
    engine: Option<*mut Engine>,
    budget: Duration,
    deadline: Instant,
    /// Stop: raised by the agent's Stop button while the script runs.
    cancel: Option<Arc<AtomicBool>>,
    /// The script was stopped by the time or memory limit.
    limit: bool,
    prints: Vec<String>,
    names: Vec<(String, String)>,
    shots: Vec<String>,
    exports: Vec<(Vec<String>, String, String, Value)>,
    notes: Vec<String>,
    touched: Vec<String>,
    /// Nodes the script read (Get / TakeScreenshot); the canvas flashes them longer.
    read: Vec<String>,
    /// Streaming preview: no network side effects (Generate is skipped).
    preview: bool,
    /// `Generate("svg")` calls: (frame, prompt), drawn once the call commits.
    pens: Vec<(String, String)>,
}

thread_local! {
    static EXEC: RefCell<Option<Exec>> = const { RefCell::new(None) };
}

const PRELUDE: &str = r#"
var document = "document";
var __j = (v) => v === undefined ? "null" : JSON.stringify(v);
function Insert(parent, node) { return __I(String(parent), __j(node)); }
function Copy(path, parent, data) { return __C(String(path), String(parent), __j(data)); }
function Update(path, data) { __U(String(path), __j(data)); }
function Replace(path, node) { return __R(String(path), __j(node)); }
function Move(path, parent, index) { __M(String(path), parent == null ? "" : String(parent), index == null ? -1 : Number(index)); }
function Delete(path) { __D(String(path)); }
function SetVariables(v, replace) { __SV(__j(v), !!replace); }
function GetVariables() { return JSON.parse(__GV()); }
function Print(...a) { __P(a.map(x => typeof x === "string" ? x : JSON.stringify(x)).join(" ")); }
function TakeScreenshot(ids) { __T(__j(Array.isArray(ids) ? ids : [ids])); }
function Export(ids, format, out, opts) { __X(__j(Array.isArray(ids) ? ids : [ids]), String(format), String(out || ""), __j(opts || {})); }
function FindEmptySpace(o) { return JSON.parse(__F(__j(o || {}))); }
function Generate(type, a, b) { const r = __GEN(String(type), String(a == null ? "" : a), String(b == null ? "" : b)); return r || undefined; }
function Get(a, b, c) {
  let path, visit, opts = {};
  for (const x of [a, b, c]) {
    if (typeof x === "string") path = x;
    else if (typeof x === "function") visit = x;
    else if (x && typeof x === "object") opts = x;
  }
  if (!visit) {
    if (!path || path === "document") throw new Error("Reading the whole document without a visitor is not allowed; pass a node id or a visitor function");
    return JSON.parse(__G(path, __j(opts)));
  }
  const tree = JSON.parse(__GT(path || "document", __j(opts)));
  const out = [];
  const walk = (e, parentCtx, depth, index) => {
    let skip = false;
    const ctx = { node: e.n, parentCtx, depth, index, bounds: e.b, problems: e.p || undefined, skipChildren() { skip = true; } };
    const r = visit(e.n, ctx);
    if (r !== undefined) out.push(r);
    if (!skip) (e.k || []).forEach((k, i) => walk(k, ctx, depth + 1, i));
  };
  tree.forEach((t, i) => walk(t, undefined, 0, i));
  return out;
}
var __rejected = new Map();
function __track(p, reason, handled) { if (handled) __rejected.delete(p); else __rejected.set(p, reason); }
function __unhandled() { for (const r of __rejected.values()) return [r]; }
var __base = new Set(Object.getOwnPropertyNames(globalThis));
"#;

const EXPORT_GLOBALS: &str = r#"(() => {
  const out = {};
  for (const k of Object.getOwnPropertyNames(globalThis)) {
    if (__base.has(k) || k === "__base") continue;
    const v = globalThis[k];
    if (v === undefined || typeof v === "function") continue;
    try { out[k] = JSON.parse(JSON.stringify(v)); } catch (e) {}
  }
  return JSON.stringify(out);
})()"#;

/// Host functions take their arguments as strings (the prelude JSON-encodes objects).
type Native = fn(&[String]) -> Result<String, String>;

const NATIVES: [(&str, Native); 15] = [
    ("__I", n_insert), ("__C", n_copy), ("__U", n_update), ("__R", n_replace), ("__M", n_move),
    ("__D", n_delete), ("__SV", n_setvars), ("__GV", n_getvars), ("__P", n_print), ("__T", n_shot),
    ("__X", n_export), ("__F", n_find_space), ("__GEN", n_generate), ("__G", n_get), ("__GT", n_get_tree),
];

fn arg(a: &[String], i: usize) -> &str {
    a.get(i).map_or("", String::as_str)
}

fn json_arg(a: &[String], i: usize) -> Result<Value, String> {
    let t = arg(a, i);
    if t.is_empty() {
        return Ok(Value::Null);
    }
    serde_json::from_str(t).map_err(|e| format!("invalid argument: {e}"))
}

fn with<R>(f: impl FnOnce(&mut Engine, &mut Exec) -> Result<R, String>) -> Result<R, String> {
    EXEC.with(|cell| {
        let mut guard = cell.borrow_mut();
        let ex = guard.as_mut().ok_or("no active execute call")?;
        let Some(engine) = ex.engine else {
            return f(&mut crate::engine::engine(), ex);
        };
        // SAFETY: the caller of `bind` holds the only &mut Engine for the duration of the script
        // and does not touch it until evaluation returns; natives never re-enter JS.
        f(unsafe { &mut *engine }, ex)
    })
}

fn record(ex: &mut Exec, node: &Value, id: &str) {
    if let Some(name) = node["name"].as_str() {
        ex.names.push((name.to_string(), id.to_string()));
    }
    ex.touched.push(id.to_string());
}

fn n_insert(a: &[String]) -> Result<String, String> {
    let (parent, node) = (arg(a, 0), json_arg(a, 1)?);
    with(|e, ex| {
        let id = e.doc.insert(parent, node.clone(), None)?;
        record(ex, &node, &id);
        if parent != crate::doc::ROOT {
            e.slide_in(&id);
        }
        e.version += 1;
        Ok(id)
    })
}

fn n_copy(a: &[String]) -> Result<String, String> {
    let (path, parent, data) = (arg(a, 0), arg(a, 1), json_arg(a, 2)?);
    with(|e, ex| {
        let source = match e.doc.find(path) {
            Some(n) if !path.contains('/') => n.clone(),
            _ => node_json(e, path, &GetOpts { resolve_vars: false, resolve_inst: true, geometry: true, depth: usize::MAX })?,
        };
        let id = e.doc.copy(&source, parent, (!data.is_null()).then_some(&data))?;
        let named = if data["name"].is_string() { data.clone() } else { source };
        record(ex, &named, &id);
        e.version += 1;
        Ok(id)
    })
}

fn n_update(a: &[String]) -> Result<String, String> {
    let (path, data) = (arg(a, 0), json_arg(a, 1)?);
    with(|e, ex| {
        e.doc.update(path, &data)?;
        ex.touched.push(path.to_string());
        e.version += 1;
        Ok(String::new())
    })
}

fn n_replace(a: &[String]) -> Result<String, String> {
    let (path, node) = (arg(a, 0), json_arg(a, 1)?);
    with(|e, ex| {
        let id = e.doc.replace(path, node.clone())?;
        record(ex, &node, &id);
        e.version += 1;
        Ok(id)
    })
}

fn n_move(a: &[String]) -> Result<String, String> {
    let (path, parent) = (arg(a, 0), arg(a, 1));
    let index: f64 = arg(a, 2).parse().unwrap_or(-1.0);
    with(|e, ex| {
        let idx = (index >= 0.0).then_some(index as usize);
        e.doc.move_node(path, (!parent.is_empty()).then_some(parent), idx)?;
        ex.touched.push(path.to_string());
        e.version += 1;
        Ok(String::new())
    })
}

fn n_delete(a: &[String]) -> Result<String, String> {
    with(|e, _| {
        e.doc.delete(arg(a, 0))?;
        e.version += 1;
        Ok(String::new())
    })
}

fn n_setvars(a: &[String]) -> Result<String, String> {
    let vars = json_arg(a, 0)?;
    with(|e, _| {
        e.doc.set_variables(&vars, arg(a, 1) == "true")?;
        e.version += 1;
        Ok(String::new())
    })
}

fn n_getvars(_: &[String]) -> Result<String, String> {
    with(|e, _| Ok(e.doc.variables().to_string()))
}

fn n_print(a: &[String]) -> Result<String, String> {
    with(|_, ex| {
        ex.prints.push(arg(a, 0).to_string());
        Ok(String::new())
    })
}

fn n_shot(a: &[String]) -> Result<String, String> {
    let ids = json_arg(a, 0)?;
    with(|_, ex| {
        let list: Vec<String> = ids.as_array().into_iter().flatten().filter_map(|v| v.as_str().map(str::to_string)).collect();
        ex.read.extend(list.iter().filter(|i| *i != "document").cloned());
        ex.shots.extend(list);
        Ok(String::new())
    })
}

fn n_export(a: &[String]) -> Result<String, String> {
    let (ids, format, out, opts) = (json_arg(a, 0)?, arg(a, 1), arg(a, 2), json_arg(a, 3)?);
    with(|_, ex| {
        let ids = ids.as_array().into_iter().flatten().filter_map(|v| v.as_str().map(str::to_string)).collect();
        ex.exports.push((ids, format.to_string(), out.to_string(), opts));
        Ok(String::new())
    })
}

fn n_find_space(a: &[String]) -> Result<String, String> {
    let o = json_arg(a, 0)?;
    with(|e, _| {
        e.sync();
        let anchor = o["nodeId"].as_str().and_then(|id| e.renderer.find(id).cloned());
        let r = crate::render::find_empty_space(
            &e.renderer,
            o["width"].as_f64().unwrap_or(400.0),
            o["height"].as_f64().unwrap_or(300.0),
            o["direction"].as_str().unwrap_or("right"),
            o["padding"].as_f64().unwrap_or(0.0),
            anchor.as_ref(),
        );
        Ok(r.to_string())
    })
}

fn n_generate(a: &[String]) -> Result<String, String> {
    let (ty, target, prompt) = (arg(a, 0), arg(a, 1), arg(a, 2));
    with(|e, ex| match ty {
        _ if ex.preview => Ok(String::new()),
        "ai" | "stock" => {
            // The fill points at a pending url right away (the canvas shows a
            // shimmer skeleton) and the real image replaces it when the download finishes.
            // ponytail: no image-generation backend; a seeded photo from picsum.photos stands in.
            let seed = format!("{:x}", crate::svg::fxhash(prompt));
            let pending = format!("nalarupa:pending-image-{seed}-{:x}", crate::doc::random_u64());
            e.doc.update(target, &json!({"fill": {"type": "image", "url": pending, "mode": "fill"}}))?;
            e.version += 1;
            e.inflight.insert(pending.clone());
            let dir = e.doc.dir().map(|d| d.join("images")).unwrap_or_else(|| e.data_dir.join("images"));
            let relative = e.doc.dir().is_some();
            std::thread::spawn(move || {
                let file = dir.join(format!("{seed}.jpg"));
                let ok = file.exists() || (std::fs::create_dir_all(&dir).is_ok()
                    && crate::net::get_bytes(&format!("https://picsum.photos/seed/{seed}/1600/1000")).is_some_and(|b| std::fs::write(&file, b).is_ok()));
                let url = match (ok, relative) {
                    (false, _) => String::new(),
                    (true, true) => format!("./images/{seed}.jpg"),
                    (true, false) => file.to_string_lossy().replace('\\', "/"),
                };
                crate::engine::engine().resolve_pending(&pending, &url);
            });
            ex.notes.push(format!("Generate(\"{ty}\") on {target}: the image is loading in the background and appears when ready (placeholder photo source, no image-generation backend)."));
            Ok(String::new())
        }
        "svg" => {
            // SVG artwork is drawn into a frame (the AI Pen), streamed after the call commits.
            if e.doc.find(target).is_none_or(|n| n["type"] != "frame") {
                return Err(format!("Generate(\"svg\") draws into a frame: Insert a frame with placeholder: true first and pass its id (got '{target}')"));
            }
            e.doc.update(target, &json!({"placeholder": true}))?;
            e.version += 1;
            ex.pens.push((target.to_string(), prompt.to_string()));
            ex.notes.push(format!("Generate(\"svg\") on {target}: the AI Pen draws the artwork into the frame in the background; its `placeholder` flag clears when it is done (usually within a few minutes). Keep designing; do not add children to that frame or generate it again."));
            Ok(String::new())
        }
        _ => Err(format!("Generate type '{ty}' is not available in Nalarupa; use fills, icons and paths instead")),
    })
}

pub struct GetOpts {
    pub resolve_vars: bool,
    pub resolve_inst: bool,
    pub geometry: bool,
    pub depth: usize,
}

impl GetOpts {
    fn from(v: &Value) -> GetOpts {
        GetOpts {
            resolve_vars: v["resolveVariables"] == true,
            resolve_inst: v["resolveInstances"] == true,
            geometry: v["includePathGeometry"] == true,
            depth: v["depth"].as_u64().map(|d| d as usize).unwrap_or(usize::MAX),
        }
    }
}

/// Serialize a laid-out node back into schema data (round-trips into Insert/Update).
fn to_json(e: &Engine, r: &RNode, o: &GetOpts, depth: usize, children: bool) -> Value {
    let raw = (!r.id.contains('/') && !o.resolve_vars).then(|| e.doc.find(&r.id)).flatten();
    let mut v = match raw {
        Some(raw) => {
            let mut c = raw.clone();
            if let Some(obj) = c.as_object_mut() {
                obj.remove("children");
            }
            c
        }
        None => {
            let mut m = Map::new();
            m.insert("id".into(), json!(r.id));
            m.insert("type".into(), json!(r.ty));
            for (k, val) in &r.p {
                // `__status` is a script node's render-only state.
                if k != "id" && k != "type" && !k.starts_with("__") {
                    m.insert(k.clone(), val.clone());
                }
            }
            if let Some(c) = &r.inst {
                m.insert("ref".into(), json!(c));
            }
            Value::Object(m)
        }
    };
    if !o.geometry && v.get("geometry").is_some() {
        v["geometry"] = json!("...");
    }
    let is_instance = r.inst.is_some();
    // A script node's generated children are not part of the document.
    if children && !r.kids.is_empty() && (!is_instance || o.resolve_inst) && r.ty != "script" {
        v["children"] = if depth >= o.depth {
            json!("...")
        } else {
            Value::Array(r.kids.iter().map(|k| to_json(e, k, o, depth + 1, true)).collect())
        };
    }
    v
}

fn node_json(e: &mut Engine, path: &str, o: &GetOpts) -> Result<Value, String> {
    if !o.resolve_vars && !o.resolve_inst && !path.contains('/') {
        let raw = e.doc.find(path).ok_or_else(|| format!("node not found: {path}"))?.clone();
        return Ok(trim_depth(raw, o, 0));
    }
    let top = crate::render::top_index(&e.doc, path).ok_or_else(|| format!("node not found: {path}"))?;
    let root = e.ctx().laid_out(top).ok_or_else(|| format!("node not found: {path}"))?;
    let r = find_rnode(&root, path).ok_or_else(|| format!("node not found: {path}"))?;
    Ok(to_json(e, r, o, 0, true))
}

fn trim_depth(mut v: Value, o: &GetOpts, depth: usize) -> Value {
    if !o.geometry && v.get("geometry").is_some() {
        v["geometry"] = json!("...");
    }
    if let Some(kids) = v.get_mut("children") {
        if depth >= o.depth {
            *kids = json!("...");
        } else if let Some(arr) = kids.as_array_mut() {
            for k in arr.iter_mut() {
                *k = trim_depth(std::mem::take(k), o, depth + 1);
            }
        }
    }
    v
}

fn n_get(a: &[String]) -> Result<String, String> {
    let (path, opts) = (arg(a, 0), json_arg(a, 1)?);
    with(|e, ex| {
        ex.read.push(path.to_string());
        node_json(e, path, &GetOpts::from(&opts)).map(|v| v.to_string())
    })
}

fn n_get_tree(a: &[String]) -> Result<String, String> {
    let (path, opts) = (arg(a, 0), json_arg(a, 1)?);
    with(|e, _| {
        let o = GetOpts::from(&opts);
        fn entry(e: &Engine, r: &RNode, px: f64, py: f64, o: &GetOpts) -> Value {
            let kids: Vec<Value> = if (r.inst.is_some() && !o.resolve_inst) || r.ty == "script" { Vec::new() } else { r.kids.iter().map(|k| entry(e, k, r.x, r.y, o)).collect() };
            json!({"n": to_json(e, r, o, 0, false), "b": {"x": r.x - px, "y": r.y - py, "width": r.w, "height": r.h}, "p": r.problem, "k": kids})
        }
        let mut out = Vec::new();
        if path == "document" || path.is_empty() {
            for i in 0..e.doc.children().len() {
                if let Some(root) = e.ctx().laid_out(i) {
                    out.push(entry(e, &root, 0.0, 0.0, &o));
                }
            }
        } else {
            let top = crate::render::top_index(&e.doc, path).ok_or_else(|| format!("node not found: {path}"))?;
            let root = e.ctx().laid_out(top).ok_or_else(|| format!("node not found: {path}"))?;
            let (px, py) = parent_origin(&root, path).unwrap_or((0.0, 0.0));
            let r = find_rnode(&root, path).ok_or_else(|| format!("node not found: {path}"))?;
            out.push(entry(e, r, px, py, &o));
        }
        Ok(Value::Array(out).to_string())
    })
}

fn parent_origin(n: &RNode, id: &str) -> Option<(f64, f64)> {
    if n.kids.iter().any(|k| k.id == id) {
        return Some((n.x, n.y));
    }
    n.kids.iter().find_map(|k| parent_origin(k, id))
}

fn apply_edits(source: &str, edits: &[Value]) -> Result<String, String> {
    let mut src = source.to_string();
    for (i, e) in edits.iter().enumerate() {
        let find = e["find"].as_str().ok_or_else(|| format!("edit {i}: find is required"))?;
        let replace = e["replace"].as_str().ok_or_else(|| format!("edit {i}: replace is required"))?;
        let all = e["all"] == true;
        let variants = [find.to_string(), find.replace('\n', "\\n"), find.replace("\\n", "\n")];
        let Some(f) = variants.iter().find(|v| !v.is_empty() && src.contains(v.as_str())) else {
            return Err(format!("edit {i}: `find` text not found in the stored snippet"));
        };
        if !all && src.matches(f.as_str()).count() > 1 {
            return Err(format!("edit {i}: `find` matches more than once; make it unique or set all:true"));
        }
        src = if all { src.replace(f.as_str(), replace) } else { src.replacen(f.as_str(), replace, 1) };
    }
    Ok(src)
}

fn over_time() -> bool {
    EXEC.with(|c| c.try_borrow().is_ok_and(|ex| ex.as_ref().is_some_and(|ex| Instant::now() >= ex.deadline || ex.stopped())))
}

/// A fresh sandbox: limits, host functions, prelude, then the globals earlier calls kept.
fn sandbox(globals: &Map<String, Value>) -> Result<Context, String> {
    let rt = Runtime::new().map_err(|e| e.to_string())?;
    rt.set_memory_limit(MEMORY_LIMIT);
    rt.set_max_stack_size(STACK_LIMIT);
    rt.set_interrupt_handler(Some(Box::new(over_time)));
    rt.set_host_promise_rejection_tracker(Some(Box::new(|ctx, promise, reason, handled| {
        if let Ok(track) = ctx.globals().get::<_, Function>("__track") {
            let _ = track.call::<_, ()>((promise, reason, handled));
        }
    })));
    let js = Context::full(&rt).map_err(|e| e.to_string())?;
    js.with(|ctx| {
        install(&ctx).map_err(|e| format!("host functions: {e}"))?;
        eval::<()>(&ctx, PRELUDE, None).map_err(|e| format!("prelude: {e}"))?;
        let restore: String = globals.iter().map(|(k, v)| format!("globalThis[{}] = {};\n", json!(k), v)).collect();
        eval::<()>(&ctx, &restore, None).map_err(|e| format!("restoring globals: {e}"))
    })?;
    Ok(js)
}

fn install<'js>(ctx: &Ctx<'js>) -> rquickjs::Result<()> {
    for (name, f) in NATIVES {
        let func = Function::new(ctx.clone(), move |ctx: Ctx<'js>, args: Rest<Coerced<String>>| -> rquickjs::Result<String> {
            let args: Vec<String> = args.0.into_iter().map(|a| a.0).collect();
            f(&args).map_err(|msg| Exception::throw_message(&ctx, &msg))
        })?;
        ctx.globals().set(name, func)?;
    }
    #[cfg(test)]
    ctx.globals().set("__PANIC", Function::new(ctx.clone(), || -> rquickjs::Result<()> { panic!("host function panic") })?)?;
    Ok(())
}

/// Evaluate sloppy-mode global code (agents assign globals without a keyword). Agent code is
/// named `input` so errors can point at its line.
fn eval<'js, V: FromJs<'js>>(ctx: &Ctx<'js>, src: &str, file: Option<&str>) -> Result<V, String> {
    let mut opts = EvalOptions::default();
    opts.strict = false;
    opts.filename = file.map(str::to_string);
    ctx.eval_with_options(src, opts).map_err(|err| describe(ctx, err))
}

/// A promise left rejected fails the call like a throw does.
fn unhandled_rejection(ctx: &Ctx<'_>) -> Result<(), String> {
    // Boxed in an array so a rejection with an `undefined` reason still counts as found.
    let reason: Option<rquickjs::Array> = ctx.globals().get::<_, Function>("__unhandled").and_then(|f| f.call(())).map_err(|e| describe(ctx, e))?;
    match reason {
        Some(r) => Err(describe(ctx, ctx.throw(r.get(0).map_err(|e| describe(ctx, e))?))),
        None => Ok(()),
    }
}

/// Finish a snippet: run the jobs it queued (code after an `await`, `.then` callbacks) within the
/// same deadline, then fail the call on an unhandled rejection.
///
/// Running out of memory inside a job can drop the job without a usable error (the queue just
/// ends and the call looks finished), so the heap is checked every `HEAP_CHECK_JOBS` jobs and once
/// more when the queue drains. The jobs run outside `with` because the heap walk takes the
/// runtime lock `with` holds.
fn settle(js: &Context) -> Result<(), String> {
    let rt = js.runtime();
    let near_cap = || rt.memory_usage().malloc_size >= (MEMORY_LIMIT - OOM_SLACK) as i64;
    let mut jobs = 0u32;
    loop {
        match rt.execute_pending_job() {
            Ok(true) => {}
            Ok(false) => break,
            Err(failed) => return Err(failed.0.with(|ctx| describe(&ctx, rquickjs::Error::Exception))),
        }
        jobs += 1;
        if over_time() {
            return Err(js.with(|ctx| describe(&ctx, rquickjs::Error::Exception)));
        }
        if jobs % HEAP_CHECK_JOBS == 0 && near_cap() {
            return Err(oom_stop());
        }
    }
    if near_cap() {
        return Err(oom_stop());
    }
    js.with(|ctx| unhandled_rejection(&ctx))
}

fn oom_stop() -> String {
    limit_stop(format!("InternalError: out of memory: the snippet used more than {} MB", MEMORY_LIMIT >> 20))
}

/// Time or memory limit: flag it so a streamed call stops re-running.
fn limit_stop(msg: String) -> String {
    EXEC.with(|c| {
        if let Some(ex) = c.borrow_mut().as_mut() {
            ex.limit = true;
        }
    });
    msg
}

fn describe(ctx: &Ctx<'_>, err: rquickjs::Error) -> String {
    if !err.is_exception() {
        return err.to_string();
    }
    let thrown = ctx.catch();
    if EXEC.with(|c| c.borrow().as_ref().is_some_and(Exec::stopped)) {
        return limit_stop("the agent was stopped, so the snippet was interrupted".into());
    }
    if let Some(budget) = EXEC.with(|c| c.borrow().as_ref().filter(|ex| Instant::now() >= ex.deadline).map(|ex| ex.budget)) {
        return limit_stop(format!("the snippet ran past the {:.1} s time limit and was stopped (endless loop?)", budget.as_secs_f64()));
    }
    let Some(ex) = thrown.as_exception() else {
        return format!("Uncaught {}", Coerced::<String>::from_js(ctx, thrown).map(|s| s.0).unwrap_or_default());
    };
    let name: String = ex.get("name").unwrap_or_else(|_| "Error".into());
    let message = ex.message().unwrap_or_default();
    let line = ex.stack().and_then(|s| s.split("input:").nth(1).map(|l| l.chars().take_while(char::is_ascii_digit).collect::<String>()));
    let text = format!("{name}: {message}{}", line.filter(|l| !l.is_empty()).map(|l| format!(" (line {l})")).unwrap_or_default());
    if message == "out of memory" {
        return limit_stop(format!("{text}: the snippet used more than {} MB", MEMORY_LIMIT >> 20));
    }
    text
}

impl Exec {
    fn new(engine: Option<*mut Engine>, preview: bool, budget: Duration, cancel: Option<Arc<AtomicBool>>) -> Exec {
        Exec {
            engine,
            budget,
            deadline: Instant::now() + budget,
            cancel,
            limit: false,
            prints: vec![],
            names: vec![],
            shots: vec![],
            exports: vec![],
            notes: vec![],
            touched: vec![],
            read: vec![],
            preview,
            pens: vec![],
        }
    }

    fn stopped(&self) -> bool {
        self.cancel.as_ref().is_some_and(|c| c.load(Ordering::SeqCst))
    }
}

/// Run `f` (which evaluates JavaScript) with `e` bound to the host functions.
fn bind<R>(e: &mut Engine, preview: bool, budget: Duration, f: impl FnOnce() -> R) -> (R, Exec) {
    EXEC.with(|c| *c.borrow_mut() = Some(Exec::new(Some(e as *mut Engine), preview, budget, None)));
    // A preview step can run while another thread's `execute` has the flag up.
    let was = std::mem::replace(&mut e.executing, true);
    let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(f));
    e.executing = was;
    let ex = EXEC.with(|c| c.borrow_mut().take()).expect("execute state");
    match r {
        Ok(r) => (r, ex),
        // The state is restored; the caller decides what a panic means.
        Err(p) => std::panic::resume_unwind(p),
    }
}

/// A panic in a host function becomes an ordinary error, so the call rolls back, `executing`
/// clears and the agent gets an `editId` to repair.
fn run(source: &str, globals: &Map<String, Value>) -> Result<Map<String, Value>, String> {
    std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| run_unguarded(source, globals)))
        .unwrap_or_else(|_| Err("internal error (panic) while running the script".into()))
}

fn run_unguarded(source: &str, globals: &Map<String, Value>) -> Result<Map<String, Value>, String> {
    let js = sandbox(globals)?;
    js.with(|ctx| eval::<()>(&ctx, source, Some("input")))?;
    settle(&js)?;
    js.with(|ctx| {
        let out: String = eval(&ctx, EXPORT_GLOBALS, None)?;
        Ok(serde_json::from_str::<Value>(&out).ok().and_then(|v| v.as_object().cloned()).unwrap_or_default())
    })
}

/// Restore the seeded id sequence after `draws` ids. Ids come from a thread-local generator that
/// other code (and other threads) move on between streaming steps.
fn resume_ids(seed: u64, draws: u64) {
    crate::doc::reseed(seed);
    for _ in 0..draws {
        crate::doc::random_u64();
    }
}

/// How many ids the seeded sequence has produced, given it stood at `from` before the step:
/// draw a probe and find it further down the sequence. None if it is out of reach.
fn ids_drawn(seed: u64, from: u64) -> Option<u64> {
    const REACH: u64 = 1 << 20;
    let probe = crate::doc::random_u64();
    resume_ids(seed, from);
    (from..from + REACH).find(|_| crate::doc::random_u64() == probe)
}

/// Streaming preview: run the part of stream `key`'s `source` it has not run yet, in the stream's
/// live context, against `e.doc` (the caller swaps in the preview document). Nothing is committed
/// or rolled back: what succeeded stays visible while the agent is still writing.
/// Returns every id the stream has touched, or None when it cannot continue (its prefix changed
/// in a non-append way, or a statement threw): the caller then rebuilds the preview, which
/// re-runs the stream from scratch in one piece (so later function declarations hoist).
pub fn stream_step(e: &mut Engine, key: &str, agent: &str, source: &str, seed: u64) -> Option<Vec<String>> {
    let mut live = match e.script.live.remove(key) {
        Some(l) if l.stalled => {
            let touched = l.touched.clone();
            e.script.live.insert(key.to_string(), l);
            return Some(touched);
        }
        Some(l) if l.broken || !source.starts_with(&l.ran) => return None,
        Some(l) => l,
        None => Live { js: sandbox(e.script.globals.get(agent).unwrap_or(&Map::new())).ok()?, ran: String::new(), draws: 0, touched: Vec::new(), broken: false, stalled: false },
    };
    let chunk = &source[live.ran.len()..];
    if !chunk.trim().is_empty() {
        resume_ids(seed, live.draws);
        // A panic in a host function breaks this stream (the caller rebuilds the preview) instead of
        // unwinding past the caller's document swap; what it wrote before panicking still counts.
        let (r, ex) = bind(e, true, PREVIEW_TIMEOUT, || {
            std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| live.js.with(|ctx| eval::<()>(&ctx, chunk, Some("input"))).and_then(|_| settle(&live.js))))
                .unwrap_or_else(|_| Err("internal error (panic) while running the script".into()))
        });
        match ids_drawn(seed, live.draws) {
            Some(n) => live.draws = n,
            None => live.broken = true,
        }
        live.touched.extend(ex.touched);
        live.stalled = ex.limit;
        live.broken |= r.is_err();
        live.ran = source.to_string();
    }
    let touched = live.touched.clone();
    e.script.live.insert(key.to_string(), live);
    Some(touched)
}

/// The preview is being rebuilt from the real document: streams re-run from scratch, except
/// stalled ones, which stay out of it. Contexts of ended streams are dropped.
pub fn reset_streams(e: &mut Engine) {
    let keys: HashSet<&str> = e.streams.iter().map(|s| s.key.as_str()).collect();
    e.script.live.retain(|k, l| l.stalled && keys.contains(k.as_str()));
}

pub fn execute(e: &mut Engine, args: &Value) -> ToolOut {
    execute_within(e, args, EXECUTE_TIMEOUT)
}

/// What an `execute` call needs from the engine before its JavaScript runs.
struct Prepared {
    source: String,
    edit_id: Option<String>,
    /// The document before the call: rolled back to on failure, the undo step on success.
    before: Value,
    agent: String,
    globals: Map<String, Value>,
    was_clean: bool,
    /// `agent.stops` when the call began: a Stop while the script runs still reaches its pens.
    stops: u64,
}

fn prepare(e: &mut Engine, args: &Value) -> Result<Prepared, ToolOut> {
    let edits = args["edits"].as_array();
    let input = args["input"].as_str().unwrap_or("");
    let (source, edit_id) = match edits {
        Some(edits) => {
            if !input.is_empty() {
                return Err(ToolOut::err("pass either `input` or `edits` (with `editId`), not both"));
            }
            let Some(id) = args["editId"].as_str() else { return Err(ToolOut::err("`edits` requires the `editId` from the failed call")) };
            let Some(stored) = e.script.failed.get(id) else { return Err(ToolOut::err(format!("unknown editId '{id}'"))) };
            match apply_edits(stored, edits) {
                Ok(src) => (src, Some(id.to_string())),
                Err(err) => return Err(ToolOut::err(err)),
            }
        }
        None if input.trim().is_empty() => return Err(ToolOut::err("`input` (JavaScript) is required")),
        None => (input.to_string(), None),
    };

    let before = e.doc.root.clone();
    let was_clean = !e.dirty();
    let agent = args["__agent"].as_str().unwrap_or("agent").to_string();
    let globals = e.script.globals.get(&agent).cloned().unwrap_or_default();
    if let Some(seed) = args["__seed"].as_u64() {
        crate::doc::reseed(seed);
    }
    Ok(Prepared { source, edit_id, before, agent, globals, was_clean, stops: e.agent.stops })
}

fn execute_within(e: &mut Engine, args: &Value, budget: Duration) -> ToolOut {
    let prep = match prepare(e, args) {
        Ok(p) => p,
        Err(out) => return out,
    };
    let (result, ex) = bind(e, false, budget, || run(&prep.source, &prep.globals));
    finish(e, prep, result, ex)
}

/// `execute` without holding the engine across the JavaScript: each host function locks it for its
/// own work, so polls, renders and Stop keep answering while a long script runs. The call stops
/// (and rolls back) when `cancel` rises. The caller holds `EXECUTE_GATE`, so no other
/// script edits the document meanwhile.
pub fn execute_shared(mut e: MutexGuard<'static, Engine>, args: &Value, cancel: Option<Arc<AtomicBool>>) -> (ToolOut, MutexGuard<'static, Engine>) {
    let prep = match prepare(&mut e, args) {
        Ok(p) => p,
        Err(out) => return (out, e),
    };
    EXEC.with(|c| *c.borrow_mut() = Some(Exec::new(None, false, EXECUTE_TIMEOUT, cancel)));
    e.executing = true;
    drop(e);
    let result = run(&prep.source, &prep.globals);
    let mut e = crate::engine::engine();
    e.executing = false;
    let ex = EXEC.with(|c| c.borrow_mut().take()).expect("execute state");
    let out = finish(&mut e, prep, result, ex);
    (out, e)
}

fn finish(e: &mut Engine, prep: Prepared, result: Result<Map<String, Value>, String>, ex: Exec) -> ToolOut {
    let Prepared { source, edit_id, before, agent, was_clean, stops, .. } = prep;

    let new_globals = match result {
        Ok(g) => g,
        Err(err) => {
            e.doc.root = before;
            e.forget_gone_slides();
            e.touch();
            e.keep_clean(was_clean);
            let id = edit_id.unwrap_or_else(|| format!("e{:x}", crate::doc::random_u64() & 0xffffff));
            e.script.failed.insert(id.clone(), source);
            return ToolOut::err(format!(
                "Error: {err}\nAll operations in this call have been rolled back.\nIMPORTANT: fix this by calling execute again with `editId: \"{id}\"` and an `edits` list of {{find, replace}} patches — do not resend the whole snippet."
            ));
        }
    };
    if let Some(id) = &edit_id {
        e.script.failed.remove(id);
    }
    crate::agent::note_flagged(e, &agent, &before);
    e.script.globals.entry(agent.clone()).or_default().extend(new_globals.clone());
    e.commit(before);
    let edit = json!({"type": "agent_edit", "ids": ex.touched, "read": ex.read});
    e.script.edit = Some(edit.clone());
    e.emit(edit);
    if !ex.pens.is_empty() {
        e.sync();
        for (frame, prompt) in &ex.pens {
            let size = e.renderer.find(frame).map_or((400.0, 400.0), |f| (f.w.max(1.0), f.h.max(1.0)));
            // Its own flag, set by Stop even after a later turn replaced the run's. It starts set when a Stop came while
            // this script ran or its run is being stopped; an earlier Stop's flag does not stop a later call's pen.
            let cancel = Arc::new(AtomicBool::new(crate::agent::stopped_since(e, stops, &agent)));
            e.agent.pens.push((frame.clone(), cancel.clone()));
            crate::pen::start(frame.clone(), prompt.clone(), size, cancel);
        }
    }

    let mut text = String::from("OK\n");
    for n in &ex.notes {
        text += &format!("Note: {n}\n");
    }
    if !ex.names.is_empty() {
        text += "\n## Created nodes by name\n";
        for (name, id) in &ex.names {
            let var = new_globals.iter().find(|(_, v)| v.as_str() == Some(id)).map(|(k, _)| format!(" (= {k})")).unwrap_or_default();
            text += &format!("\"{name}\": \"{id}\"{var}\n");
        }
    }
    if !new_globals.is_empty() {
        text += &format!("\nGlobal variables carry over to later calls: {}\n", new_globals.keys().cloned().collect::<Vec<_>>().join(", "));
    }
    if !ex.prints.is_empty() {
        text += "\n## Print output\n";
        text += &ex.prints.join("\n");
        text.push('\n');
    }
    let (warnings, icons) = validate(e, &ex.touched);
    if !warnings.is_empty() {
        text += "\n## Issues detected (fix them in the next call)\n";
        for w in warnings {
            text += &format!("- {w}\n");
        }
    }
    // Only the trees are built under the engine lock; `ToolOut::finish` rasterizes screenshots and writes exports.
    let (mut shots, mut downloads, mut runs) = (Vec::new(), Vec::new(), Runs::default());
    for id in &ex.shots {
        match shot_tree(e, id) {
            Ok((tree, scale, waits, needs)) => {
                shots.push((id.clone(), tree, scale));
                downloads.extend(waits);
                runs.extend(needs);
            }
            Err(err) => text += &format!("Screenshot of {id} failed: {err}\n"),
        }
    }
    let mut exports = Vec::new();
    for (ids, format, out, opts) in ex.exports {
        // Checked before the job is planned, so nothing is written for a refused path.
        let job = crate::html::check_script_export(e, &format, &out).and_then(|()| crate::html::plan(e, &ids, &format, &out, &opts));
        match job.and_then(|mut job| crate::html::claim_script_files(&mut job).map(|()| job)) {
            Ok(job) => exports.push(job),
            Err(err) => text += &format!("\nExport failed: {err}\n"),
        }
    }
    // The shots are redrawn and the exports planned again in `ToolOut::finish` once the icons and images they show
    // have downloaded and their script nodes have run, from this document and these libraries (the live ones may change meanwhile).
    let redraw = !shots.is_empty() && (!icons.is_empty() || !downloads.is_empty() || !runs.is_empty());
    let call_doc = (redraw || exports.iter().any(|job| job.stale())).then(|| (e.doc.clone(), e.libs.clone()));
    ToolOut { text, images: Vec::new(), error: false, shots, icons, downloads, runs, call_doc, exports }
}

/// The render tree and scale of a screenshot target (`document` is the first top-level node), the
/// icons and images it shows that are still downloading and the script runs it drew as running.
pub fn shot_tree(e: &mut Engine, id: &str) -> Result<(resvg::usvg::Tree, f64, Vec<String>, Runs), String> {
    let target = shot_target(&e.doc, id);
    e.sync();
    let scale = e.renderer.find(&target).map(|f| (1400.0 / f.w.max(f.h).max(1.0)).min(2.0)).unwrap_or(1.0).clamp(0.05, 8.0);
    e.assets.record();
    record_runs();
    let tree = e.ctx().node_tree_at(&target, scale);
    let runs = recorded_runs();
    let waits = e.assets.recorded();
    tree.map(|tree| (tree, scale, waits, runs))
}

/// The node a screenshot of `id` draws: `document` is the first top-level node.
pub fn shot_target(doc: &crate::doc::Doc, id: &str) -> String {
    if id == "document" { doc.children().first().and_then(|n| n["id"].as_str()).unwrap_or("").to_string() } else { id.to_string() }
}

/// Layout and content checks on nodes touched by this call, plus the icons still downloading (their
/// "not found" warning, library, name, weight): whether they exist is only known once `ToolOut::finish` waits.
fn validate(e: &mut Engine, touched: &[String]) -> (Vec<String>, Vec<(String, String, String, u32)>) {
    let touched: HashSet<&str> = touched.iter().map(String::as_str).collect();
    if touched.is_empty() {
        return (Vec::new(), Vec::new());
    }
    let mut tops: Vec<usize> = touched.iter().filter_map(|id| crate::render::top_index(&e.doc, id)).collect();
    tops.sort_unstable();
    tops.dedup();
    // An instance's resolved props carry its component's x/y; only its own count: set on the
    // instance, or by a `descendants` override for a nested one (id `outer/inner`).
    let mut own_xy: HashSet<String> = HashSet::new();
    crate::doc::walk(e.doc.children(), &mut |n| {
        let (Some(id), true) = (n["id"].as_str(), n["type"] == "ref") else { return };
        let sets_xy = |v: &Value| v.get("x").is_some() || v.get("y").is_some();
        if sets_xy(n) {
            own_xy.insert(id.to_string());
        }
        for (path, over) in n["descendants"].as_object().into_iter().flatten() {
            if sets_xy(over) {
                own_xy.insert(format!("{id}/{path}"));
            }
        }
    });
    let mut out = Vec::new();
    let mut icons = Vec::new();
    for t in tops {
        let Some(root) = e.ctx().laid_out(t) else { continue };
        let mut stack = vec![(&root, None::<&RNode>)];
        while let Some((n, parent)) = stack.pop() {
            for k in &n.kids {
                stack.push((k, Some(n)));
            }
            if !touched.contains(n.id.as_str()) && !parent.is_some_and(|p| touched.contains(p.id.as_str())) {
                continue;
            }
            let label = format!("\"{}\" ({})", n.name(), n.id);
            if n.ty == "text" && crate::svg::fills(n.p.get("fill")).is_empty() {
                out.push(format!("text {label} has no fill and is invisible"));
            }
            if let Some(p) = n.problem {
                out.push(format!("{label} sticks out of its parent ({p}); resize the parent or use fill_container/fit_content"));
            }
            let parent_layout = parent.map(|p| p.ty == "frame" && p.s("layout") != Some("none")).unwrap_or(false);
            let absolute = n.s("layoutPosition") == Some("absolute");
            let has_xy = if n.inst.is_some() { own_xy.contains(&n.id) } else { n.p.contains_key("x") || n.p.contains_key("y") };
            if parent_layout && !absolute && has_xy && touched.contains(n.id.as_str()) {
                out.push(format!("{label}: x/y are ignored inside a flex layout parent"));
            }
            let fills_parent = ["width", "height"].iter().any(|k| n.s(k).is_some_and(|v| v.starts_with("fill_container")));
            if fills_parent && (!parent_layout || absolute) && !n.s("width").unwrap_or("").contains('(') && !n.s("height").unwrap_or("").contains('(') {
                out.push(format!("{label} uses fill_container but its parent has no flex layout"));
            }
            if n.ty == "frame" && n.w < 0.5 && n.h < 0.5 && !n.kids.is_empty() {
                out.push(format!("{label} collapsed to 0×0 (fit_content parent with only fill_container children?)"));
            }
            if n.ty == "icon" {
                let (library, name) = (n.s("library").unwrap_or("lucide"), n.s("icon").unwrap_or(""));
                let weight = n.f("weight").unwrap_or(200.0);
                if e.assets.icon_weight(library, name, weight).is_none() {
                    let warning = format!("icon {label}: '{name}' not found in library '{library}'");
                    if e.assets.icon_loading(library, name, weight) {
                        icons.push((warning, library.to_string(), name.to_string(), weight.max(0.0) as u32));
                    } else {
                        out.push(warning);
                    }
                }
            }
            if n.ty == "text" {
                let st = crate::text::TextStyle::of(n);
                e.fonts.ensure(&st);
                if !e.fonts.has_family(&st.family) && !e.fonts.loading(&st.family) {
                    out.push(format!("text {label}: font family '{}' is not available", st.family));
                }
            }
        }
    }
    out.sort();
    out.dedup();
    icons.sort();
    icons.dedup();
    (out, icons)
}

// ---------------------------------------------------------------------------------------------
// Script nodes (`type: "script"`): a `.js` file (`scriptUri`, relative to the
// document) whose body returns an array of nodes; they become the node's children, placed in its
// local space (no layout) and never saved. `/** @schema X */` is required (output is migrated
// from that version) and `/** @input name: type(args) = default */` declares `nalarupa.input.*`.
// Each run gets a fresh 16 MB / 512 KB-stack QuickJS runtime, 2 s, `Math.random` = mulberry32
// seeded 0, and at most 1000 output nodes. Runs are cached by (source, inputs, size), so a script
// re-runs exactly when its file, its inputs or its size changed.

/// Script node limits.
pub const SCRIPT_MEMORY: usize = 16 << 20;
const SCRIPT_STACK: usize = 512 << 10;
pub const SCRIPT_TIMEOUT: Duration = Duration::from_secs(2);
pub const SCRIPT_NODES: usize = 1000;

/// What a script node shows instead of its output.
#[derive(Clone, Debug, PartialEq)]
pub struct Status {
    /// "error", "empty" or "running".
    pub kind: &'static str,
    pub title: String,
    pub description: Option<String>,
    pub code: Option<String>,
}

impl Status {
    fn error(title: impl Into<String>, description: Option<String>) -> Status {
        Status { kind: "error", title: title.into(), description, code: None }
    }
}

#[derive(Clone, Debug, PartialEq)]
enum InputKind {
    Number(Option<f64>, Option<f64>),
    Text,
    Boolean,
    Color,
    Ref,
    Enum(Vec<String>),
}

#[derive(Clone, Debug)]
struct Input {
    name: String,
    kind: InputKind,
    default: Value,
}

#[derive(Clone, Debug, PartialEq)]
enum Tok {
    Ident(String),
    Num(String),
    Str(String),
    Hash(String),
    Punct(char),
    Bool(bool),
    End,
}

impl Tok {
    fn show(&self) -> String {
        match self {
            Tok::Ident(s) | Tok::Num(s) | Tok::Str(s) | Tok::Hash(s) if !s.is_empty() => format!("'{s}'"),
            Tok::Ident(_) => "identifier".into(),
            Tok::Num(_) => "number".into(),
            Tok::Str(_) => "string".into(),
            Tok::Hash(_) => "hash literal".into(),
            Tok::Punct(c) => format!("'{c}'"),
            Tok::Bool(b) => format!("'{b}'"),
            Tok::End => "end of input".into(),
        }
    }
    fn literal(&self) -> bool {
        !matches!(self, Tok::Punct(_) | Tok::End)
    }
}

/// Tokens of one `@input` directive.
fn lex(src: &str) -> Result<Vec<Tok>, String> {
    let c: Vec<char> = src.chars().collect();
    let (mut i, mut out) = (0, Vec::new());
    let word = |ch: char| ch.is_ascii_alphanumeric() || ch == '_';
    while i < c.len() {
        let ch = c[i];
        let start = i;
        if ch.is_whitespace() {
            i += 1;
            continue;
        }
        i += 1;
        out.push(match ch {
            '(' | ')' | ',' | ':' | '=' => Tok::Punct(ch),
            '"' => {
                let mut s = String::new();
                while i < c.len() && c[i] != '"' {
                    if c[i] == '\\' && i + 1 < c.len() {
                        i += 1;
                    }
                    s.push(c[i]);
                    i += 1;
                }
                if i >= c.len() {
                    return Err("Unterminated string".into());
                }
                i += 1;
                Tok::Str(s)
            }
            '#' => {
                while i < c.len() && c[i].is_ascii_hexdigit() {
                    i += 1;
                }
                if i == start + 1 {
                    return Err("Expected hex digits after '#'".into());
                }
                Tok::Hash(c[start..i].iter().collect())
            }
            '-' | '.' | '0'..='9' => {
                while i < c.len() && c[i].is_ascii_digit() {
                    i += 1;
                }
                if i < c.len() && c[i] == '.' && ch != '.' {
                    i += 1;
                    while i < c.len() && c[i].is_ascii_digit() {
                        i += 1;
                    }
                }
                let s: String = c[start..i].iter().collect();
                if s == "-" || s == "." {
                    return Err(format!("Invalid number '{s}'"));
                }
                Tok::Num(s)
            }
            _ if ch.is_ascii_alphabetic() || ch == '_' => {
                while i < c.len() && word(c[i]) {
                    i += 1;
                }
                let s: String = c[start..i].iter().collect();
                match s.as_str() {
                    "true" => Tok::Bool(true),
                    "false" => Tok::Bool(false),
                    _ => Tok::Ident(s),
                }
            }
            _ => return Err(format!("Unexpected character '{ch}'")),
        });
    }
    out.push(Tok::End);
    Ok(out)
}

/// `name: type(args) = default`, with validation messages.
fn parse_input(src: &str) -> Result<Input, String> {
    let toks = lex(src)?;
    let mut it = toks.into_iter().peekable();
    let Some(Tok::Ident(name)) = it.next() else { return Err("@input must start with a name".into()) };
    if it.next() != Some(Tok::Punct(':')) {
        return Err(format!("@input '{name}': expected ':'"));
    }
    let Some(Tok::Ident(ty)) = it.next() else { return Err(format!("@input '{name}' is missing a type")) };
    let (mut positional, mut named) = (Vec::new(), Vec::<(String, Tok)>::new());
    if it.peek() == Some(&Tok::Punct('(')) {
        it.next();
        if it.peek() == Some(&Tok::Punct(')')) {
            it.next();
        } else {
            loop {
                let t = it.next().unwrap_or(Tok::End);
                if let (Tok::Ident(k), Some(Tok::Punct('='))) = (&t, it.peek()) {
                    it.next();
                    let v = it.next().unwrap_or(Tok::End);
                    if !v.literal() {
                        return Err(format!("@input '{name}' arg '{k}' must be a literal, got {}", v.show()));
                    }
                    named.push((k.clone(), v));
                } else if t.literal() {
                    positional.push(t);
                } else {
                    return Err(format!("@input '{name}' arg must be a literal, got {}", t.show()));
                }
                if it.peek() == Some(&Tok::Punct(',')) {
                    it.next();
                    continue;
                }
                break;
            }
            let t = it.next().unwrap_or(Tok::End);
            if t != Tok::Punct(')') {
                return Err(format!("@input '{name}' arg list: expected ')', got {}", t.show()));
            }
        }
    }
    let default = if it.peek() == Some(&Tok::Punct('=')) {
        it.next();
        it.next()
    } else {
        None
    };
    if let Some(t) = it.next().filter(|t| *t != Tok::End) {
        return Err(format!("@input '{name}' has unexpected trailing token {}", t.show()));
    }
    let no_args = || if positional.is_empty() && named.is_empty() { Ok(()) } else { Err(format!("@input '{name}' of type '{ty}' does not accept arguments")) };
    let bad_default = |what: &str, t: &Tok| Err(format!("@input '{name}' {what}, got {}", t.show()));
    let (kind, default) = match ty.as_str() {
        "number" => {
            if !positional.is_empty() {
                return Err(format!("@input number '{name}' does not accept positional arguments"));
            }
            let (mut min, mut max) = (None, None);
            for (k, v) in &named {
                let Tok::Num(n) = v else { return Err(format!("@input number '{name}' arg '{k}' must be a number, got {}", v.show())) };
                match k.as_str() {
                    "min" => min = n.parse().ok(),
                    "max" => max = n.parse().ok(),
                    _ => return Err(format!("@input number '{name}' has unknown arg '{k}'. Valid args: min, max")),
                }
            }
            let d = match &default {
                None => 0.0,
                Some(Tok::Num(n)) => n.parse().unwrap_or(0.0),
                Some(t) => return bad_default("default must be a number", t),
            };
            (InputKind::Number(min, max), json!(d))
        }
        "string" => {
            no_args()?;
            match &default {
                None => (InputKind::Text, json!("")),
                Some(Tok::Str(s)) => (InputKind::Text, json!(s)),
                Some(t) => return bad_default("string default must be a quoted string", t),
            }
        }
        "boolean" => {
            no_args()?;
            match &default {
                None => (InputKind::Boolean, json!(false)),
                Some(Tok::Bool(b)) => (InputKind::Boolean, json!(b)),
                Some(t) => return bad_default("default must be 'true' or 'false'", t),
            }
        }
        "color" => {
            no_args()?;
            match &default {
                None => (InputKind::Color, json!("#000000")),
                Some(Tok::Hash(s) | Tok::Str(s)) => (InputKind::Color, json!(s)),
                Some(t) => return bad_default("color default must be a hex literal like #RRGGBB", t),
            }
        }
        "ref" => {
            no_args()?;
            match &default {
                None => (InputKind::Ref, json!("")),
                Some(Tok::Str(s) | Tok::Ident(s)) => (InputKind::Ref, json!(s)),
                Some(t) => return bad_default("ref default must be an identifier or quoted string", t),
            }
        }
        "enum" => {
            if !named.is_empty() {
                return Err(format!("@input enum '{name}' does not accept named arguments"));
            }
            if positional.is_empty() {
                return Err(format!("@input enum '{name}' must specify at least one option"));
            }
            let options = positional.iter().map(|t| match t {
                Tok::Str(s) => Ok(s.clone()),
                t => Err(format!("@input enum '{name}' option must be a quoted string, got {}", t.show())),
            }).collect::<Result<Vec<_>, _>>()?;
            let d = match &default {
                None => options[0].clone(),
                Some(Tok::Str(s)) if options.contains(s) => s.clone(),
                Some(Tok::Str(s)) => return Err(format!("@input enum '{name}' default '{s}' is not one of the options")),
                Some(t) => return bad_default("enum default must be a quoted string", t),
            };
            (InputKind::Enum(options), json!(d))
        }
        _ => return Err(format!("Invalid @input type '{ty}'. Must be one of: number, string, boolean, color, ref, enum")),
    };
    Ok(Input { name, kind, default })
}

/// `@schema` and `@input` directives from every `/** … */` block of a script.
fn script_header(src: &str) -> Result<(Option<String>, Vec<Input>), String> {
    let (mut schema, mut inputs) = (None, Vec::<Input>::new());
    let mut rest = src;
    while let Some(open) = rest.find("/**") {
        let body = &rest[open + 3..];
        let Some(close) = body.find("*/") else { break };
        for line in body[..close].lines() {
            let l = line.trim_start();
            let l = l.strip_prefix('*').map_or(l, |r| r.strip_prefix(' ').unwrap_or(r)).trim();
            let Some(d) = l.strip_prefix('@') else { continue };
            let end = d.find(|c: char| !(c.is_ascii_alphanumeric() || c == '_')).unwrap_or(d.len());
            if end == 0 {
                continue;
            }
            match &d[..end] {
                "schema" => schema = Some(d[end..].trim().to_string()),
                "input" => {
                    let i = parse_input(d[end..].trim())?;
                    inputs.retain(|x| x.name != i.name);
                    inputs.push(i);
                }
                _ => {}
            }
        }
        rest = &body[close + 2..];
    }
    Ok((schema, inputs))
}

/// `nalarupa.input`: declared inputs only, stored values coerced (numbers clamped).
fn script_inputs(decl: &[Input], given: Option<&Map<String, Value>>) -> Result<Map<String, Value>, Status> {
    let mut out = Map::new();
    for i in decl {
        let v = given.and_then(|g| g.get(&i.name)).filter(|v| !v.is_null()).unwrap_or(&i.default).clone();
        let v = match &i.kind {
            InputKind::Number(min, max) => {
                let mut x = v.as_f64().or_else(|| v.as_str().and_then(|s| s.trim().parse().ok())).unwrap_or(f64::NAN);
                if x.is_nan() {
                    x = min.unwrap_or(0.0);
                }
                json!(x.max(min.unwrap_or(f64::NEG_INFINITY)).min(max.unwrap_or(f64::INFINITY)))
            }
            InputKind::Enum(options) if !v.as_str().is_some_and(|s| options.iter().any(|o| o == s)) => {
                let shown = v.as_str().map_or_else(|| v.to_string(), str::to_string);
                return Err(Status::error("Invalid input value", Some(format!("Value '{shown}' is not allowed for input '{}'. Must be one of: {}", i.name, options.join(", ")))));
            }
            _ => v,
        };
        out.insert(i.name.clone(), v);
    }
    Ok(out)
}

/// `Math.random` = mulberry32, reset to seed 0 before every run.
const SCRIPT_PRELUDE: &str = "(function () { let s = 0; globalThis.__seed = function (v) { s = v | 0; }; Math.random = function () { s = (s + 0x6D2B79F5) | 0; let t = Math.imul(s ^ (s >>> 15), 1 | s); t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t; return ((t ^ (t >>> 14)) >>> 0) / 4294967296; }; })()";

/// Error text: "Name: message" plus the stack, and the source lines around the failing one.
fn script_error(ctx: &Ctx<'_>, err: rquickjs::Error, src: &str) -> (String, Option<String>) {
    if !err.is_exception() {
        return (err.to_string(), None);
    }
    let thrown = ctx.catch();
    let Some(ex) = thrown.as_exception() else {
        return (Coerced::<String>::from_js(ctx, thrown).map(|s| s.0).unwrap_or_default(), None);
    };
    let name: String = ex.get("name").unwrap_or_else(|_| "Error".into());
    let mut text = format!("{name}: {}", ex.message().unwrap_or_default());
    let stack = ex.stack().unwrap_or_default();
    if !stack.trim().is_empty() {
        text = format!("{text}\n{}", stack.trim_end());
    }
    let line = stack.split(':').skip(1).find_map(|s| s.parse::<usize>().ok());
    let code = line.map(|a| {
        let lines: Vec<&str> = src.lines().collect();
        (a.saturating_sub(3)..(a + 2).min(lines.len())).map(|d| format!("{} {}: {}", if d + 1 == a { ">" } else { " " }, d + 1, lines[d])).collect::<Vec<_>>().join("\n")
    });
    (text, code)
}

/// Compile and call the script once; returns its JSON result.
fn run_script(src: &str, file: &str, input: &str) -> Result<String, Status> {
    let rt = Runtime::new().map_err(|e| Status::error("An internal error occurred", Some(e.to_string())))?;
    rt.set_memory_limit(SCRIPT_MEMORY);
    rt.set_max_stack_size(SCRIPT_STACK);
    // Armed just before the call: compiling is not timed. Set outside `with`, which
    // holds the runtime lock.
    let deadline = std::sync::Arc::new(std::sync::Mutex::new(None::<Instant>));
    let armed = deadline.clone();
    rt.set_interrupt_handler(Some(Box::new(move || armed.lock().is_ok_and(|d| d.is_some_and(|t| Instant::now() > t)))));
    let js = Context::full(&rt).map_err(|e| Status::error("An internal error occurred", Some(e.to_string())))?;
    // The wrapper keeps the script on line 1 onwards, so error lines are the file's lines.
    let wrapper = format!("(function (__input) {{ __seed(0); const __ctx = JSON.parse(__input); const result = ((nalarupa, pencil) => {{{src}\n}})(__ctx, __ctx); if (result === undefined) {{ throw new Error(\"Script must return an array of nodes. Add a 'return [...]' statement at the end of your script.\"); }} return JSON.stringify(result); }})");
    js.with(|ctx| {
        ctx.eval::<(), _>(SCRIPT_PRELUDE).map_err(|e| Status::error("An internal error occurred", Some(e.to_string())))?;
        let mut opts = EvalOptions::default();
        opts.strict = false;
        opts.filename = Some(file.to_string());
        let f: Function = ctx.eval_with_options(wrapper.as_str(), opts).map_err(|e| {
            let (d, code) = script_error(&ctx, e, src);
            Status { kind: "error", title: "Script compile error".into(), description: Some(d), code }
        })?;
        if let Ok(mut d) = deadline.lock() {
            *d = Some(Instant::now() + SCRIPT_TIMEOUT);
        }
        f.call::<_, String>((input,)).map_err(|e| {
            let (d, code) = script_error(&ctx, e, src);
            Status { kind: "error", title: "Script runtime error".into(), description: Some(d), code }
        })
    })
}

/// Script output: an array, migrated from `schema`, ≤ 1000 nodes, no components,
/// no connections, ids dropped.
fn script_output(json_text: &str, schema: &str) -> Result<Vec<Value>, Status> {
    let Ok(Value::Array(nodes)) = serde_json::from_str::<Value>(json_text) else {
        return Err(Status::error("Invalid script output", Some("Script must return an array of nodes.".into())));
    };
    let mut nodes = nodes;
    if schema != crate::doc::FORMAT_VERSION {
        let mut root = json!({"version": schema, "children": nodes});
        crate::resolve::migrate(&mut root);
        nodes = root["children"].as_array().cloned().unwrap_or_default();
    }
    fn walk(n: &mut Value, count: &mut usize) -> Result<(), Status> {
        *count += 1;
        if *count > SCRIPT_NODES {
            return Err(Status::error("Too many nodes", Some(format!("Script output cannot exceed {SCRIPT_NODES} nodes."))));
        }
        if !n.is_object() {
            return Err(Status::error("Error inserting script children", Some(format!("Expected a node object, got {n}"))));
        }
        if n["reusable"] == true {
            return Err(Status::error("Invalid script output", Some("Reusable nodes are not supported in script outputs.".into())));
        }
        if n["type"] == "connection" {
            return Err(Status::error("Invalid script output", Some("Connection nodes are not supported in script outputs.".into())));
        }
        if let Some(o) = n.as_object_mut() {
            o.remove("id");
        }
        if let Some(kids) = n.get_mut("children").and_then(Value::as_array_mut) {
            for k in kids {
                walk(k, count)?;
            }
        }
        Ok(())
    }
    let mut count = 0;
    for n in &mut nodes {
        walk(n, &mut count)?;
    }
    Ok(nodes)
}

type ScriptRun = Result<Vec<Value>, Status>;

/// One script run waiting for, or handed to, a worker.
struct Job {
    key: u64,
    src: String,
    file: String,
    input: String,
    schema: String,
}

/// A script node's worker: runs one job at a time, so a newer size or input replaces the one
/// still waiting instead of queueing behind it.
#[derive(Default)]
struct Slot {
    running: bool,
    current: Option<u64>,
    next: Option<Job>,
    /// Last finished output: shown while a newer run is pending.
    last: Option<ScriptRun>,
    /// Tick of the last request, so eviction drops the longest-unused slots first.
    used: u64,
}

#[derive(Default)]
struct Scripts {
    cache: HashMap<u64, ScriptRun>,
    slots: HashMap<String, Slot>,
    /// Worker threads alive; waiting slots queue beyond `WORKERS`.
    active: usize,
    tick: u64,
}

/// Most script runs going at once: a document with many script nodes queues the rest.
const WORKERS: usize = 8;
const SLOTS: usize = 1024;

static SCRIPTS: std::sync::Mutex<Option<Scripts>> = std::sync::Mutex::new(None);
/// Counts finished background runs, so the renderer knows when to rebuild a waiting layer.
static FINISHED: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

fn scripts() -> std::sync::MutexGuard<'static, Option<Scripts>> {
    SCRIPTS.lock().unwrap_or_else(|e| e.into_inner())
}

fn run_job(j: &Job) -> ScriptRun {
    run_script(&j.src, &j.file, &j.input).and_then(|out| script_output(&out, &j.schema))
}

fn remember(key: u64, r: ScriptRun) {
    let mut guard = scripts();
    let g = guard.get_or_insert_with(Scripts::default);
    if g.cache.len() > 256 {
        g.cache.clear();
    }
    g.cache.insert(key, r);
}

thread_local! {
    /// While an export or screenshot is planned (`record_runs`): the script runs it needed that were not cached.
    static ASKED: RefCell<Option<Vec<Job>>> = const { RefCell::new(None) };
}

/// Script runs an export or screenshot needed while it was planned under the engine lock: it drew them as
/// running, so `run` them once the lock is released and plan again.
#[derive(Default)]
pub struct Runs(Vec<Job>);

impl Runs {
    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    pub fn extend(&mut self, more: Runs) {
        for job in more.0 {
            if !self.0.iter().any(|j| j.key == job.key) {
                self.0.push(job);
            }
        }
    }

    /// Run each (at most `WORKERS` at once) unless its output is cached meanwhile, and cache the outputs.
    /// Call without holding `engine()`.
    pub fn run(&self) {
        for jobs in self.0.chunks(WORKERS) {
            std::thread::scope(|scope| {
                for job in jobs {
                    if scripts().as_ref().is_some_and(|s| s.cache.contains_key(&job.key)) {
                        continue;
                    }
                    scope.spawn(|| {
                        let r = std::panic::catch_unwind(|| run_job(job)).unwrap_or_else(|_| Err(Status::error("An internal error occurred", None)));
                        remember(job.key, r);
                    });
                }
            });
        }
    }
}

/// From now on, a script node laid out on this thread without a slot (an export or screenshot, under the
/// engine lock) does not run: it draws as running and is noted for `recorded_runs`.
pub fn record_runs() {
    ASKED.set(Some(Vec::new()));
}

/// Stop noting; the runs noted since `record_runs`.
pub fn recorded_runs() -> Runs {
    Runs(ASKED.take().unwrap_or_default())
}

/// `draw` (which takes the engine lock itself) without running a script node under the lock: the runs it
/// needs are run after it returns, and it draws again.
pub fn off_lock<T>(mut draw: impl FnMut() -> T) -> T {
    record_runs();
    let out = draw();
    let runs = recorded_runs();
    if runs.is_empty() {
        return out;
    }
    runs.run();
    record_runs();
    let out = draw();
    recorded_runs();
    out
}

/// Background runs finished so far.
pub fn finished() -> u64 {
    FINISHED.load(std::sync::atomic::Ordering::Acquire)
}

/// Whether a background run is still going or waiting.
pub fn busy() -> bool {
    scripts().as_ref().is_some_and(|s| s.slots.values().any(|l| l.running || l.next.is_some()))
}

/// A worker thread: runs its slot's jobs, then takes over any slot still waiting for a worker.
fn work(mut slot: String) {
    loop {
        let job = {
            let mut g = scripts();
            let Some(sc) = g.as_mut() else { return };
            let job = sc.slots.get_mut(&slot).and_then(|s| {
                let job = s.next.take()?;
                s.current = Some(job.key);
                Some(job)
            });
            match job {
                Some(job) => job,
                None => {
                    if let Some(s) = sc.slots.get_mut(&slot) {
                        s.running = false;
                        s.current = None;
                    }
                    let Some(waiting) = sc.slots.iter().find(|(_, s)| !s.running && s.next.is_some()).map(|(k, _)| k.clone()) else {
                        sc.active -= 1;
                        return;
                    };
                    if let Some(s) = sc.slots.get_mut(&waiting) {
                        s.running = true;
                    }
                    slot = waiting;
                    continue;
                }
            }
        };
        let cached = scripts().as_ref().and_then(|s| s.cache.get(&job.key).cloned());
        let r = cached.unwrap_or_else(|| std::panic::catch_unwind(|| run_job(&job)).unwrap_or_else(|_| Err(Status::error("An internal error occurred", None))));
        let mut g = scripts();
        let g = g.get_or_insert_with(Scripts::default);
        if g.cache.len() > 256 {
            g.cache.clear();
        }
        g.cache.insert(job.key, r.clone());
        if let Some(s) = g.slots.get_mut(&slot) {
            s.last = Some(r);
        }
        FINISHED.fetch_add(1, std::sync::atomic::Ordering::Release);
    }
}

/// Output of a script node: read its file, check the header, coerce inputs and run (cached).
/// With a `slot` (the node's identity) a cache miss runs on a worker thread and returns the previous
/// output, or a "running" status, at once: the caller holds the engine lock and must not wait for
/// the script. Without one the call waits for the run.
pub fn script_node(uri: &str, dir: Option<&std::path::Path>, inputs: Option<&Map<String, Value>>, w: f64, h: f64, slot: Option<&str>) -> ScriptRun {
    if uri.is_empty() {
        return Err(Status { kind: "empty", title: "No script file selected".into(), description: None, code: None });
    }
    let rel = uri.strip_prefix("file://").unwrap_or(uri);
    let p = std::path::Path::new(rel);
    let path = if p.is_absolute() { Some(p.to_path_buf()) } else { dir.map(|d| d.join(rel)) };
    let Some(path) = path else {
        return Err(Status::error(format!("Cannot resolve script path: {uri}"), None));
    };
    let Ok(src) = std::fs::read_to_string(&path) else {
        return Err(Status::error("Script not found", Some(format!("We could not find \u{201c}{rel}\u{201d} at this path. Check for typos or missing folders."))));
    };
    let (schema, decl) = script_header(&src).map_err(|e| Status::error(e, None))?;
    let Some(schema) = schema else {
        return Err(Status::error("Missing @schema version tag", Some(format!("Add /** @schema {} */ to the top of the script.", crate::doc::FORMAT_VERSION))));
    };
    let input = json!({"width": w, "height": h, "input": script_inputs(&decl, inputs)?}).to_string();
    let key = crate::svg::fxhash(&format!("{src}\u{0}{input}\u{0}{rel}"));
    let file = path.file_name().map_or(rel.to_string(), |f| f.to_string_lossy().to_string());
    let job = Job { key, src, file, input, schema };
    let mut guard = scripts();
    let g = guard.get_or_insert_with(Scripts::default);
    if let Some(r) = g.cache.get(&key) {
        return r.clone();
    }
    let Some(slot) = slot else {
        drop(guard);
        // An export or screenshot planned under the engine lock: note the run for after the lock instead.
        let Some(job) = ASKED.with_borrow_mut(|asked| match asked {
            Some(asked) => {
                if !asked.iter().any(|j| j.key == key) {
                    asked.push(job);
                }
                None
            }
            None => Some(job),
        }) else {
            return Err(Status { kind: "running", title: "Running script".into(), description: None, code: None });
        };
        // Not under the lock of a render: wait for the run, with the cache unlocked meanwhile.
        let r = run_job(&job);
        remember(key, r.clone());
        return r;
    };
    g.tick += 1;
    if g.slots.len() >= SLOTS {
        // Drop the longest-unused idle slots (their node is gone or off screen); busy ones stay.
        let mut idle: Vec<(u64, String)> = g.slots.iter().filter(|(_, s)| !s.running && s.next.is_none()).map(|(k, s)| (s.used, k.clone())).collect();
        idle.sort_unstable();
        for (_, k) in idle.into_iter().take(SLOTS / 4) {
            g.slots.remove(&k);
        }
    }
    let (tick, free) = (g.tick, g.active < WORKERS);
    let s = g.slots.entry(slot.to_string()).or_default();
    s.used = tick;
    if s.current == Some(key) {
        // The size or input went back to what is running: drop the stale one waiting behind it.
        s.next = None;
    } else if s.next.as_ref().is_none_or(|j| j.key != key) {
        s.next = Some(job);
    }
    let start = !s.running && s.next.is_some() && free;
    if start {
        s.running = true;
    }
    let last = s.last.clone();
    if start {
        g.active += 1;
        let slot = slot.to_string();
        std::thread::spawn(move || work(slot));
    }
    last.unwrap_or_else(|| Err(Status { kind: "running", title: "Running script".into(), description: None, code: None }))
}

/// Replace every laid-out script node's children with its output (or mark its status), then lay
/// the output out in the node's box. `doc` = the document's file. `live` = the caller holds the engine lock: runs happen in the background. Ids are `scriptId/k`, so they never collide with saved nodes.
pub fn expand_scripts(n: &mut RNode, r: &crate::resolve::Resolver, fonts: &mut crate::text::Fonts, dir: Option<&std::path::Path>, doc: Option<&std::path::Path>, live: bool) {
    for k in &mut n.kids {
        expand_scripts(k, r, fonts, dir, doc, live);
    }
    if n.ty != "script" {
        return;
    }
    let inputs = n.p.get("inputs").and_then(Value::as_object);
    let uri = n.s("scriptUri").unwrap_or("");
    // A slot belongs to one node of one document, so another document's output never shows here.
    let slot = live.then(|| format!("{}\u{0}{}\u{0}{uri}", doc.map_or(String::new(), |d| d.to_string_lossy().into_owned()), n.id));
    let run = script_node(uri, dir, inputs, n.w, n.h, slot.as_deref()).and_then(|mut out| {
        let mut k = 0;
        fn ids(v: &mut Value, sid: &str, k: &mut usize, r: &crate::resolve::Resolver) -> Result<(), Status> {
            v["id"] = json!(format!("{sid}/{k}"));
            *k += 1;
            if v["type"] == "ref" {
                let target = v["ref"].as_str().unwrap_or("");
                if r.node(target).is_none() {
                    return Err(Status::error("Component not found", Some(format!("Component '{target}' could not be resolved."))));
                }
            }
            for c in v.get_mut("children").and_then(Value::as_array_mut).into_iter().flatten() {
                ids(c, sid, k, r)?;
            }
            Ok(())
        }
        for v in &mut out {
            ids(v, &n.id, &mut k, r)?;
        }
        Ok(out)
    });
    n.kids.clear();
    match run {
        Ok(out) => {
            let host = json!({"type": "frame", "id": n.id, "layout": "none", "x": n.x, "y": n.y, "width": n.w, "height": n.h, "children": out});
            if let Some(mut root) = r.resolve(&host) {
                crate::layout::layout(&mut root, fonts);
                n.kids = root.kids;
            }
        }
        Err(s) => {
            n.p.insert("__status".into(), json!({"kind": s.kind, "title": s.title, "description": s.description, "code": s.code}));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Tests that run `execute` or `ToolOut::finish` against the process-wide engine hold the crate-wide lock,
    /// so one's script (`executing`) never makes another's call fail with "Another execute call is still running".
    fn global_execute() -> std::sync::MutexGuard<'static, ()> {
        crate::engine::engine_test_lock()
    }

    fn engine() -> Engine {
        let mut e = Engine::new(std::env::temp_dir().join("nalarupa-test"));
        e.offline();
        e.load_template("empty");
        e
    }

    #[test]
    fn bad_children_fail_the_call_with_a_repairable_error() {
        let mut e = engine();
        for kids in [r#"["str"]"#, "[5]", "[null]", r#"[{type:"nope",name:"bad"}]"#] {
            let out = execute(&mut e, &json!({"input": format!(r#"Insert(document, {{type:"frame", width:10, height:10, children:{kids}}})"#)}));
            assert!(out.error && out.text.contains("children[0]") && out.text.contains("editId"), "{kids}: {}", out.text);
            assert!(e.doc.children().is_empty(), "{kids}: rolled back");
            assert!(!e.executing);
        }
    }

    #[test]
    fn execute_transaction_and_repair() {
        let mut e = engine();
        let ok = execute(&mut e, &json!({"input": r##"
            card = Insert(document, {type:"frame", name:"Card", layout:"vertical", padding:16, gap:8, x:0, y:0});
            for (const t of ["A","B"]) Insert(card, {type:"text", name:"T"+t, content:t, fill:"#111111", fontSize:14});
            Print(Get(card, {depth:1}).children.length, Get(n => n.type === "text" ? n.name : undefined));
        "##}));
        assert!(!ok.error, "{}", ok.text);
        assert!(ok.text.contains("\"Card\""), "{}", ok.text);
        assert!(ok.text.contains("2 [\"TA\",\"TB\"]"), "{}", ok.text);
        assert_eq!(e.doc.children().len(), 1);
        assert!(e.script.globals["agent"].contains_key("card"));

        let bad = execute(&mut e, &json!({"input": "Insert(document,{type:'frame',name:'X'}); Update('nope', {x:1});"}));
        assert!(bad.error);
        assert_eq!(e.doc.children().len(), 1, "rolled back");
        e.saved = e.version;
        let bad_again = execute(&mut e, &json!({"input": "Insert(document,{type:'frame',name:'Y'}); Update('nope', {x:1});"}));
        assert!(bad_again.error, "{}", bad_again.text);
        assert!(!e.dirty(), "a rolled-back call leaves a clean document clean");
        let edit_id = bad.text.split("editId: \"").nth(1).unwrap().split('"').next().unwrap().to_string();
        let fixed = execute(&mut e, &json!({"editId": edit_id, "edits": [{"find": "Update('nope', {x:1});", "replace": "Update(card, {x:5});"}]}));
        assert!(!fixed.error, "{}", fixed.text);
        assert_eq!(e.doc.children().len(), 2);
        assert!(e.undo());
        assert_eq!(e.doc.children().len(), 1);
    }

    /// Every inserted node used to slide on its own, and each sync snapshotted all of them
    /// against all the others: cubic in the node count. Few slides start, and none inside another.
    #[test]
    fn bulk_inserts_slide_only_a_few_top_nodes() {
        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            for (let i = 0; i < 300; i++) Insert(root, {type:"rectangle", name:"r"+i, x:i*20, y:0, width:10, height:10});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(crate::render::MAX_SLIDES, 32);
        assert_eq!(e.renderer.slides.len(), 32);
        e.sync();
        assert_eq!(e.renderer.slides.len(), 32);

        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            card = Insert(root, {type:"frame", name:"Card", width:100, height:100, layout:"none"});
            for (let i = 0; i < 5; i++) Insert(card, {type:"rectangle", name:"r"+i, width:10, height:10});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 1, "the card carries its children along");
    }

    /// `Copy` and inserts past the slide cap appear in place; a sliding node under one of them
    /// would hide it until the slide ends, so that node appears in place too.
    #[test]
    fn slide_does_not_cover_a_later_node_that_appears_in_place() {
        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:640, height:400, layout:"none"});
            src = Insert(root, {type:"rectangle", name:"Src", x:1100, y:700, width:100, height:100, fill:"#ffffff"});
        "##}));
        assert!(!out.error, "{}", out.text);
        e.sync();
        e.renderer.slides.clear();
        let out = execute(&mut e, &json!({"input": r##"
            root = Get(n => n.name === "Root" ? n.id : undefined)[0];
            Insert(root, {type:"rectangle", name:"Panel", x:100, y:100, width:300, height:200, fill:"#ff0000"});
            Insert(root, {type:"rectangle", name:"Apart", x:500, y:20, width:40, height:40, fill:"#ff0000"});
            Copy(Get(n => n.name === "Src" ? n.id : undefined)[0], root, {x:150, y:150});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 2);
        e.sync();
        let left: Vec<String> = e.renderer.slides.iter().map(|s| e.doc.find(&s.id).unwrap()["name"].as_str().unwrap().to_string()).collect();
        assert_eq!(left, ["Apart"], "the panel settles under the copy; the node it does not touch still slides");

        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:640, height:400, layout:"none"});
            Insert(root, {type:"rectangle", name:"Panel", x:100, y:100, width:300, height:200, fill:"#ff0000"});
            for (let i = 0; i < 31; i++) Insert(root, {type:"rectangle", name:"f"+i, x:i*20, y:0, width:10, height:10});
            Insert(root, {type:"rectangle", name:"Square", x:150, y:150, width:100, height:100, fill:"#ffffff"});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 32);
        e.sync();
        assert_eq!(e.renderer.slides.len(), 31, "the panel settles under the square past the cap");
    }

    /// A rolled-back script's slides had no node to paint and no clock: `animating` stayed true
    /// until `SLIDE_WAIT`. Slides of nodes that survive (an earlier call's) are kept.
    #[test]
    fn a_rolled_back_script_leaves_no_slides_behind() {
        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            kept = Insert(root, {type:"rectangle", name:"Kept", width:10, height:10});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 1);
        let out = execute(&mut e, &json!({"input": "Insert(root, {type:'rectangle', name:'R', width:10, height:10})\nthrow new Error('boom')"}));
        assert!(out.error && out.text.contains("rolled back"), "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 1, "only the surviving node still slides");
        assert!(e.renderer.slides.iter().all(|s| e.doc.find(&s.id).is_some()));
        let out = execute(&mut e, &json!({"input": "Insert(root, {type:'rectangle', name:'R', width:10, height:10})"}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 2, "the retry slides its own node");
    }

    /// A slide is snapshotted alone, so a glass node would show the backdrop sharp until it settled:
    /// it appears in place, and so does a sliding card that gets a glass child.
    #[test]
    fn glass_nodes_do_not_slide() {
        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            Insert(root, {type:"rectangle", name:"Glass", width:100, height:100, effect:{type:"background_blur", radius:20}});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert!(e.renderer.slides.is_empty());

        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            card = Insert(root, {type:"frame", name:"Card", width:100, height:100, layout:"none"});
            Insert(card, {type:"rectangle", name:"Plain", width:10, height:10});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 1, "a plain card slides");
        let out = execute(&mut e, &json!({"input": r##"
            card = Get(n => n.name === "Card" ? n.id : undefined)[0];
            Insert(card, {type:"rectangle", name:"Glass", width:50, height:50, effect:[{type:"background_blur", radius:20}]});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert!(e.renderer.slides.is_empty(), "the card holds a glass node now");
    }

    /// Glass inside a component instance, glass added by an update, and glass inserted at the slide
    /// cap all leave no slide behind either.
    #[test]
    fn glass_found_through_instances_updates_and_the_cap() {
        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            comp = Insert(document, {type:"frame", name:"Comp", reusable:true, x:900, width:50, height:50, layout:"none", children:[{type:"rectangle", name:"Pane", width:50, height:50, effect:{type:"background_blur", radius:20}}]});
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            Insert(root, {type:"ref", ref:comp, width:50, height:50});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert!(e.renderer.slides.is_empty(), "an instance of a glass component appears in place");

        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            card = Insert(root, {type:"frame", name:"Card", width:100, height:100, layout:"none"});
            r = Insert(card, {type:"rectangle", name:"Plain", width:10, height:10});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 1);
        let out = execute(&mut e, &json!({"input": r##"
            Update(Get(n => n.name === "Plain" ? n.id : undefined)[0], {effect:{type:"background_blur", radius:20}});
        "##}));
        assert!(!out.error, "{}", out.text);
        e.sync();
        assert!(e.renderer.slides.is_empty(), "the sliding card holds a glass node after the update");

        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            card = Insert(root, {type:"frame", name:"Card", width:100, height:100, layout:"none"});
            for (let i = 0; i < 40; i++) Insert(root, {type:"rectangle", name:"r"+i, x:i, y:200, width:10, height:10});
            Insert(card, {type:"rectangle", name:"Glass", width:50, height:50, effect:{type:"background_blur", radius:20}});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert!(e.renderer.slides.iter().all(|s| e.doc.find(&s.id).is_some_and(|n| n["name"] != "Card")), "the card does not slide once it holds glass, even at the cap");
    }

    /// Copy, Move, Replace and an edit of a component definition also stop a card that holds glass afterwards.
    #[test]
    fn glass_added_by_copy_move_replace_or_a_component_edit_unslides() {
        let setup = r##"
            glass = Insert(document, {type:"rectangle", name:"Glass", x:900, width:50, height:50, effect:{type:"background_blur", radius:20}});
            comp = Insert(document, {type:"frame", name:"Comp", reusable:true, x:700, width:50, height:50, layout:"none", children:[{type:"rectangle", name:"Pane", width:50, height:50}]});
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            card = Insert(root, {type:"frame", name:"Card", width:100, height:100, layout:"none"});
            plain = Insert(card, {type:"rectangle", name:"Plain", width:10, height:10});
        "##;
        for edit in [
            r#"Copy(glass, card);"#,
            r#"Move(glass, card);"#,
            r#"Replace(plain, {type:"rectangle", name:"G2", width:10, height:10, effect:{type:"background_blur", radius:20}});"#,
        ] {
            let mut e = engine();
            let out = execute(&mut e, &json!({"input": setup}));
            assert!(!out.error, "{}", out.text);
            assert_eq!(e.renderer.slides.len(), 1, "{edit}: the card slides first");
            let out = execute(&mut e, &json!({"input": format!("glass = Get(n => n.name === 'Glass' ? n.id : undefined)[0]; card = Get(n => n.name === 'Card' ? n.id : undefined)[0]; plain = Get(n => n.name === 'Plain' ? n.id : undefined)[0]; {edit}")}));
            assert!(!out.error, "{edit}: {}", out.text);
            e.sync();
            assert!(e.renderer.slides.is_empty(), "{edit}: the card holds glass now");
        }

        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            comp = Insert(document, {type:"frame", name:"Comp", reusable:true, x:700, width:50, height:50, layout:"none", children:[{type:"rectangle", name:"Pane", width:50, height:50}]});
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            Insert(root, {type:"ref", ref:comp, width:50, height:50});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 1, "the instance slides first");
        let out = execute(&mut e, &json!({"input": r##"
            Update(Get(n => n.name === "Pane" ? n.id : undefined)[0], {effect:{type:"background_blur", radius:20}});
        "##}));
        assert!(!out.error, "{}", out.text);
        e.sync();
        assert!(e.renderer.slides.is_empty(), "the instance's component gained glass");
    }

    /// An edit that does not come from a script (the inspector) stops a sliding card that gains glass too.
    #[test]
    fn glass_added_outside_a_script_unslides() {
        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            card = Insert(root, {type:"frame", name:"Card", width:100, height:100, layout:"none"});
            Insert(card, {type:"rectangle", name:"Plain", width:10, height:10});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 1);
        let id = e.doc.root["children"][0]["children"][0]["children"][0]["id"].as_str().unwrap().to_string();
        e.edit(|d| d.update(&id, &json!({"effect": {"type": "background_blur", "radius": 20}}))).unwrap();
        e.sync();
        assert!(e.renderer.slides.is_empty(), "the card holds glass after an inspector edit");
    }

    /// A streamed step that gives a sliding card glass stops the slide: the card lives in the preview
    /// document, which is the one `sync` renders.
    #[test]
    fn glass_added_by_a_streamed_update_unslides() {
        let mut e = engine();
        let head = "root = Insert(document, {type:\"frame\", name:\"Root\", width:300, height:300, layout:\"none\"})
card = Insert(root, {type:\"frame\", name:\"Card\", width:100, height:100, layout:\"none\"})
";
        let full = format!("{head}Update(card, {{effect:{{type:\"background_blur\", radius:20}}}})\nx = 1\n");
        e.stream_update("toolu_1", "ag0", &format!("{head}Update(card"));
        e.sync();
        assert!(e.doc.children().is_empty(), "the card exists only in the preview");
        assert_eq!(e.renderer.slides.len(), 1, "a plain streamed card slides");
        e.stream_update("toolu_1", "ag0", &full);
        e.sync();
        assert!(e.renderer.slides.is_empty(), "the streamed update gave the card glass");
    }

    /// A slide is an isolated snapshot, so a blended paint would show its plain colour until the
    /// slide ends: a node with a blend mode (itself or in its subtree) appears in place instead.
    #[test]
    fn blended_inserts_do_not_slide() {
        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            Insert(root, {type:"rectangle", name:"plain", width:10, height:10, fill:"#ff8800"});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 1, "a plain insert slides");

        for (name, props) in [
            ("fill", r##"fill:{type:"color", color:"#ff8800", blendMode:"multiply"}"##),
            ("fill list", r##"fill:["#ffffff", {type:"color", color:"#ff8800", blendMode:"overlay"}]"##),
            ("stroke", r##"fill:"#ffffff", stroke:{type:"color", color:"#ff8800", blendMode:"screen"}, strokeWidth:4"##),
            ("shadow", r##"fill:"#ffffff", effect:[{type:"shadow", blur:8, color:"#ff0066a0", blendMode:"multiply"}]"##),
        ] {
            let mut e = engine();
            let src = format!(r##"root = Insert(document, {{type:"frame", name:"Root", width:300, height:300, layout:"none"}}); Insert(root, {{type:"rectangle", name:"b", width:10, height:10, {props}}});"##);
            let out = execute(&mut e, &json!({ "input": src }));
            assert!(!out.error, "{name}: {}", out.text);
            e.sync();
            assert!(e.renderer.slides.is_empty(), "{name}: a blended insert appears in place");
        }

        // A hidden blended child paints nothing, so it does not stop the slide.
        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            Insert(root, {type:"frame", name:"Card", width:100, height:100, layout:"none", children:[
                {type:"rectangle", width:10, height:10, enabled:false, fill:{type:"color", color:"#ff8800", blendMode:"multiply"}}]});
        "##}));
        assert!(!out.error, "{}", out.text);
        e.sync();
        assert_eq!(e.renderer.slides.len(), 1, "a disabled blended child is not painted");

        // A blend mode that arrives through a component instance is found in the resolved tree.
        let mut e = engine();
        let comp = e.doc.insert("document", json!({"type":"frame", "x":500, "y":0, "width":10, "height":10, "reusable":true, "fill":{"type":"color", "color":"#ff8800", "blendMode":"multiply"}}), None).unwrap();
        let out = execute(&mut e, &json!({"input": format!(r##"
            root = Insert(document, {{type:"frame", name:"Root", width:300, height:300, layout:"none"}});
            Insert(root, {{type:"ref", ref:"{comp}"}});
        "##)}));
        assert!(!out.error, "{}", out.text);
        e.sync();
        assert!(e.renderer.slides.is_empty(), "a blended instance appears in place");

        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            card = Insert(root, {type:"frame", name:"Card", width:100, height:100, layout:"none", children:[
                {type:"rectangle", width:10, height:10, fill:{type:"color", color:"#ff8800", blendMode:"multiply"}}]});
        "##}));
        assert!(!out.error, "{}", out.text);
        e.sync();
        assert!(e.renderer.slides.is_empty(), "a blended descendant keeps the card in place");
    }

    #[test]
    fn a_running_script_leaves_the_engine_free_and_stops_with_the_agent() {
        let _serial = global_execute();
        // The script ran under the engine lock: the window froze and Stop was dead until it ended.
        let stop = Arc::new(AtomicBool::new(false));
        let flag = stop.clone();
        let run = std::thread::spawn(move || execute_shared(crate::engine::engine(), &json!({"input": "Insert(document, {type:'frame', name:'Half'}); for (;;) {}"}), Some(flag)).0);
        let started = Instant::now();
        while !crate::engine::engine().executing {
            assert!(started.elapsed() < Duration::from_secs(5), "the script never started");
            std::thread::sleep(Duration::from_millis(10));
        }
        std::thread::sleep(Duration::from_millis(200));
        let asked = Instant::now();
        drop(crate::engine::engine());
        assert!(asked.elapsed() < Duration::from_secs(1), "the engine stayed locked while the script ran");
        assert!(!run.is_finished(), "the script is still running");
        let blocked = crate::engine::engine().tool("open_document", &json!({"path": "never-opened.pen"}));
        assert!(blocked.error && blocked.text == crate::engine::SWITCH_BUSY, "a document switch waits for the running script: {}", blocked.text);
        assert!(crate::engine::engine().tool("execute", &json!({"input": "Print(1)"})).error, "a direct execute does not interleave with the running one");
        assert_eq!(crate::dispatch(&json!({"cmd": "state"})).unwrap()["executing"], true, "the UI state reports the running script, so Home skips its save prompt");
        stop.store(true, Ordering::SeqCst);
        let out = run.join().unwrap();
        assert!(out.error && out.text.contains("stopped"), "{}", out.text);
        assert!(out.text.contains("rolled back"), "{}", out.text);
        let e = crate::engine::engine();
        assert!(!e.executing);
        assert!(e.doc.children().iter().all(|n| n["name"] != "Half"), "the stopped script's edits are rolled back");
    }

    #[test]
    fn screenshots_rasterize_after_the_tool_call() {
        let _serial = global_execute();
        // Rasterizing inside `tool` held the engine lock for up to ~0.5 s and froze the canvas.
        let mut e = engine();
        let out = e.tool("execute", &json!({"input": "c = Insert(document, {type:\"frame\", width:300, height:200, fill:\"#ff0000\"})\nTakeScreenshot(c)"}));
        assert!(!out.error, "{}", out.text);
        assert!(out.images.is_empty() && out.shots.len() == 1, "only the tree is built during the call");
        let out = out.finish();
        assert_eq!(out.images.len(), 1, "{}", out.text);
        let png = base64::Engine::decode(&base64::engine::general_purpose::STANDARD, &out.images[0]).unwrap();
        assert!(png.starts_with(b"\x89PNG"));
    }

    /// The url of a red PNG on a local server that answers each request after 300 ms.
    pub(crate) fn slow_red_png(name: &str) -> String {
        use std::io::{Read, Write};
        let server = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("http://{}/{name}.png", server.local_addr().unwrap());
        std::thread::spawn(move || {
            for mut s in server.incoming().flatten() {
                let mut req = [0; 1024];
                let _ = s.read(&mut req);
                std::thread::sleep(Duration::from_millis(300));
                let red = base64::Engine::decode(&base64::engine::general_purpose::STANDARD, "iVBORw0KGgoAAAANSUhEUgAAAAUAAAAFCAYAAACNbyblAAAAHElEQVQI12P4//8/w38GIAXDIBKE0DHxgljNBAAO9TXL0Y4OHwAAAABJRU5ErkJggg==").unwrap();
                let _ = s.write_all(format!("HTTP/1.1 200 OK\r\ncontent-length: {}\r\nconnection: close\r\n\r\n", red.len()).as_bytes());
                let _ = s.write_all(&red);
            }
        });
        url
    }

    /// Whether the middle pixel of a PNG is red.
    pub(crate) fn red_middle(png: &[u8]) -> bool {
        let px = resvg::tiny_skia::Pixmap::decode_png(png).unwrap();
        let c = px.pixel(px.width() / 2, px.height() / 2).unwrap();
        c.red() > 200 && c.green() < 80
    }

    /// A screenshot of an image still downloading waits for it (without the engine lock) and shows it.
    #[test]
    fn screenshots_wait_for_images_still_downloading() {
        let _serial = global_execute();
        let url = slow_red_png("shot");
        let silent = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let other = format!("http://{}/elsewhere.png", silent.local_addr().unwrap());
        let input = format!("c = Insert(document, {{type:\"frame\", width:100, height:100, layout:\"none\", fill:{{type:\"image\", url:\"{url}\", mode:\"fill\"}}}})\nTakeScreenshot(c)");
        // The global engine: `finish` waits on it, and the download reports to it.
        let out = {
            let mut e = crate::engine::engine();
            let was = std::mem::replace(&mut e.assets.online, true);
            // A download the screenshot does not show (this server never answers) is not waited for.
            assert!(e.assets.image(&other, None).is_none() && e.assets.loading(&other));
            let out = e.tool("execute", &json!({ "input": input }));
            e.assets.online = was;
            out
        };
        assert!(!out.error, "{}", out.text);
        assert!(out.downloads.contains(&url) && out.call_doc.is_some(), "the shot is redrawn once the image is in");
        assert!(!out.downloads.contains(&other), "only what the screenshot shows is waited for: {:?}", out.downloads);
        let out = out.finish();
        assert!(!crate::engine::engine().assets.loading(&url));
        let png = base64::Engine::decode(&base64::engine::general_purpose::STANDARD, &out.images[0]).unwrap();
        assert!(red_middle(&png), "the image is in the screenshot");
    }

    /// An export of an image still downloading waits for it (without the engine lock) and includes it.
    #[test]
    fn script_exports_wait_for_images_still_downloading() {
        let _serial = global_execute();
        let url = slow_red_png("export");
        let mut e = crate::engine::engine();
        let dir = e.data_dir.join("exports").join("script-export-waits");
        let _ = std::fs::remove_dir_all(&dir);
        let src = format!("c = Insert(document, {{type:\"frame\", width:100, height:100, layout:\"none\", fill:{{type:\"image\", url:\"{url}\", mode:\"fill\"}}}})\nExport(c, \"png\", {:?}, {{scale: 1}})", dir.to_str().unwrap());
        let was = std::mem::replace(&mut e.assets.online, true);
        let out = e.tool("execute", &json!({"input": src}));
        e.assets.online = was;
        drop(e);
        assert!(!out.error, "{}", out.text);
        let out = out.finish();
        assert!(out.text.contains("Exported:"), "{}", out.text);
        let file = std::fs::read_dir(&dir).unwrap().next().unwrap().unwrap().path();
        assert!(red_middle(&std::fs::read(file).unwrap()), "the image is in the export");
        // Planned again once the image is in, it is still a script's export: a file that appeared meanwhile is kept.
        let late = dir.join("late.pdf");
        let src = format!("Export(Insert(document, {{type:\"frame\", width:100, height:100, fill:{{type:\"image\", url:\"{}\", mode:\"fill\"}}}}), \"pdf\", {:?})", slow_red_png("late"), late.to_str().unwrap());
        let mut e = crate::engine::engine();
        let was = std::mem::replace(&mut e.assets.online, true);
        let out = e.tool("execute", &json!({"input": src}));
        e.assets.online = was;
        drop(e);
        assert!(!out.error && !out.exports[0].waits.is_empty(), "{}", out.text);
        std::fs::write(&late, "the user's own file").unwrap();
        let out = out.finish();
        assert!(out.text.contains("already exists") && !out.text.contains("Exported:"), "{}", out.text);
        assert_eq!(std::fs::read_to_string(&late).unwrap(), "the user's own file");
    }

    /// The UI's export of an image still downloading waits for it (without the engine lock) and includes it.
    #[test]
    fn ui_exports_wait_for_images_still_downloading() {
        let _serial = global_execute();
        let url = slow_red_png("ui-export");
        let (id, dir) = {
            let mut e = crate::engine::engine();
            let dir = e.data_dir.join("exports").join("ui-export-waits");
            let _ = std::fs::remove_dir_all(&dir);
            let src = format!("c = Insert(document, {{type:\"frame\", width:100, height:100, layout:\"none\", fill:{{type:\"image\", url:\"{url}\", mode:\"fill\"}}}})");
            let out = e.tool("execute", &json!({"input": src}));
            assert!(!out.error, "{}", out.text);
            (e.doc.children().last().unwrap()["id"].as_str().unwrap().to_string(), dir)
        };
        let was = std::mem::replace(&mut crate::engine::engine().assets.online, true);
        let export = std::thread::spawn(move || crate::html::export(&[id], "png", dir.to_str().unwrap(), &json!({"scale": 1})));
        // Switching documents while the export waits does not change what it exports.
        let until = Instant::now() + Duration::from_secs(5);
        while !crate::engine::engine().assets.loading(&url) && !export.is_finished() && Instant::now() < until {
            std::thread::sleep(Duration::from_millis(5));
        }
        let before = std::mem::take(&mut crate::engine::engine().doc);
        let joined = export.join();
        let mut e = crate::engine::engine();
        e.doc = before;
        e.assets.online = was;
        drop(e);
        let files = joined.unwrap().unwrap();
        assert!(red_middle(&std::fs::read(&files[0]).unwrap()), "the image is in the export");
    }

    #[test]
    fn script_exports_write_after_the_tool_call() {
        let _serial = global_execute();
        // Rasterizing and encoding inside `tool` held the engine lock for the whole export.
        // A script may export only inside the document's folder (here the data folder `exports`).
        let mut e = engine();
        let dir = e.data_dir.join("exports").join("script-export-after-call");
        let _ = std::fs::remove_dir_all(&dir);
        let src = format!("c = Insert(document, {{type:\"frame\", width:300, height:200, fill:\"#ff0000\"}})\nExport(c, \"png\", {:?}, {{scale: 1}})", dir.to_str().unwrap());
        let out = e.tool("execute", &json!({"input": src}));
        assert!(!out.error, "{}", out.text);
        assert!(out.exports.len() == 1 && !dir.exists(), "only the tree is built during the call");
        let out = out.finish();
        assert!(out.text.contains("Exported:"), "{}", out.text);
        let file = std::fs::read_dir(&dir).unwrap().next().unwrap().unwrap().path();
        assert!(std::fs::read(file).unwrap().starts_with(b"\x89PNG"));
    }

    #[test]
    fn script_export_never_replaces_a_file_it_did_not_write() {
        let _serial = global_execute();
        // `Export([], "pdf", "report.pdf")` silently replaced the user's own report.pdf in the document's folder.
        let dir = std::env::temp_dir().join(format!("nalarupa-test-export-keep-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(dir.join("shots")).unwrap();
        let mut e = engine();
        e.doc.path = Some(dir.join("design.nala"));
        let out = e.tool("execute", &json!({"input": "Insert(document, {type:\"frame\", width:100, height:80, fill:\"#ff0000\"})"}));
        assert!(!out.error, "{}", out.text);
        let id = e.doc.children()[0]["id"].as_str().unwrap().to_string();
        let users = [("report.pdf", "pdf", "report.pdf"), ("page.html", "html-css", "page.html"), (&*format!("shots/{id}.png"), "png", "shots")];
        for (file, format, out) in users {
            std::fs::write(dir.join(file), "the user's own file").unwrap();
            let src = format!("Export([{id:?}], {format:?}, {out:?})");
            let res = e.tool("execute", &json!({"input": src})).finish();
            assert!(res.text.contains("Export failed") && res.text.contains("already exists") && !res.text.contains("Exported:"), "{file}: {}", res.text);
            assert_eq!(std::fs::read_to_string(dir.join(file)).unwrap(), "the user's own file", "{file} is kept");
        }
        // A file that appears after the call (the export is written in `finish`) is not replaced either.
        let out = e.tool("execute", &json!({"input": format!("Export([{id:?}], \"pdf\", \"late.pdf\")")}));
        std::fs::write(dir.join("late.pdf"), "the user's own file").unwrap();
        let res = out.finish();
        assert!(res.text.contains("already exists") && !res.text.contains("Exported:"), "{}", res.text);
        assert_eq!(std::fs::read_to_string(dir.join("late.pdf")).unwrap(), "the user's own file");
        // Nor is a dangling link followed to create its target.
        #[cfg(unix)]
        {
            std::os::unix::fs::symlink(dir.join("target.pdf"), dir.join("link.pdf")).unwrap();
            let res = e.tool("execute", &json!({"input": format!("Export([{id:?}], \"pdf\", \"link.pdf\")")})).finish();
            assert!(res.text.contains("already exists") && !dir.join("target.pdf").exists(), "{}", res.text);
        }
        // A file the script exported itself is written again (the agent fixes a slide and re-exports).
        for _ in 0..2 {
            let res = e.tool("execute", &json!({"input": format!("Export([{id:?}], \"pdf\", \"deck.pdf\")")})).finish();
            assert!(res.text.contains("Exported:"), "{}", res.text);
        }
        assert!(std::fs::read(dir.join("deck.pdf")).unwrap().starts_with(b"%PDF-"));
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn streaming_preview_then_commit() {
        let mut e = engine();
        let full = "card = Insert(document, {type:\"frame\", name:\"Card\", width:200, height:100, fill:\"#ffffff\"})
Insert(card, {type:\"text\", name:\"Title\", content:\"Hi\", fill:\"#111111\"})
";
        let cut = full.find("Insert(card").unwrap() + 10;
        e.stream_update("toolu_1", "ag0", &full[..cut]);
        assert!(e.doc.children().is_empty(), "real document untouched while streaming");
        let preview = e.preview.as_ref().expect("preview exists");
        assert_eq!(preview.children().len(), 1, "first complete statement is visible");
        let preview_id = preview.children()[0]["id"].as_str().unwrap().to_string();
        assert!(e.events.iter().any(|ev| ev["type"] == "agent_activity" && ev["streaming"] == true));
        let out = e.tool("execute", &json!({"input": full, "__stream": "toolu_1", "__agent": "ag0"}));
        assert!(!out.error, "{}", out.text);
        assert!(e.preview.is_none() && e.streams.is_empty(), "stream retired");
        assert_eq!(e.doc.children()[0]["id"], preview_id, "final run keeps the previewed ids");
        assert_eq!(e.doc.children()[0]["children"].as_array().unwrap().len(), 1);
        assert!(e.undo());
        assert!(e.doc.children().is_empty(), "one undo step for the whole call");
    }

    #[test]
    fn keyless_execute_only_takes_its_own_agents_stream() {
        let mut e = engine();
        let common = "pos = {x: 0, y: 0}\n";
        let frame = |name: &str| format!("{common}Insert(document, {{type:\"frame\", name:\"{name}\", width:10, height:10}})");
        // Agent B streams a script that starts with the same statement as agent A's final call.
        e.stream_update("toolu_b", "agB", &format!("{common}Insert("));
        e.events.clear();
        // Claude Code's MCP `execute` carries no `__stream`, only the caller's `__agent`.
        let out = e.tool("execute", &json!({"input": frame("A frame"), "__agent": "agA"}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.streams.len(), 1, "agent B's stream is untouched");
        let writes: Vec<&Value> = e.events.iter().filter(|ev| ev["type"] == "agent_activity" && ev["op"] == "write" && ev["streaming"] == false).collect();
        assert!(!writes.is_empty() && writes.iter().all(|ev| ev["agent"] == "agA"), "{writes:?}");
        // The owner's own keyless call still retires its stream.
        let out = e.tool("execute", &json!({"input": frame("B frame"), "__agent": "agB"}));
        assert!(!out.error, "{}", out.text);
        assert!(e.streams.is_empty() && e.preview.is_none());
    }

    #[test]
    fn a_keyed_execute_takes_its_own_stream_before_an_earlier_prefix_match() {
        let mut e = engine();
        let common = "pos = {x: 0, y: 0}\n";
        e.stream_update("toolu_1", "agA", &format!("{common}Insert("));
        e.stream_update("toolu_2", "agA", &format!("{common}Insert("));
        let out = e.tool("execute", &json!({"input": format!("{common}Insert(document, {{type:\"frame\", name:\"Two\", width:10, height:10}})"), "__stream": "toolu_2", "__agent": "agA"}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.streams.iter().map(|s| s.key.as_str()).collect::<Vec<_>>(), ["toolu_1"], "the keyed stream is retired, the earlier one stays");
        e.stream_end("toolu_1");
    }

    #[test]
    fn rolled_back_agent_edits_leave_a_clean_document_clean() {
        let mut e = engine();
        assert!(!e.dirty());
        let out = e.tool("execute", &json!({"input": "Insert(document, {type:\"frame\", name:\"A\", width:10, height:10})\nUpdate(\"no-such-node\", {x:1})"}));
        assert!(out.error && out.text.contains("rolled back"), "{}", out.text);
        assert!(e.doc.children().is_empty() && !e.dirty(), "a failed execute is not an edit");
        // Stop in the middle of a streamed call: the preview goes away, the document never changed.
        let src = "Insert(document, {type:\"frame\", name:\"B\", width:10, height:10})\nInsert(";
        e.stream_update("toolu_stop", "ag0", src);
        assert_eq!(e.preview.as_ref().expect("preview exists").children().len(), 1);
        assert!(!e.dirty(), "a streaming preview is not an edit");
        e.stream_end("toolu_stop");
        assert!(e.preview.is_none() && e.doc.children().is_empty() && !e.dirty(), "a stopped stream is not an edit");
        // A document that really has unsaved changes stays dirty through the same paths.
        e.tool("execute", &json!({"input": "Insert(document, {type:\"frame\", name:\"C\", width:10, height:10})"}));
        assert!(e.dirty());
        e.stream_update("toolu_stop", "ag0", src);
        e.stream_end("toolu_stop");
        assert!(e.dirty());
    }

    #[test]
    fn globals_are_per_agent() {
        let mut e = engine();
        let run = |e: &mut Engine, agent: &str, input: &str| {
            let out = e.tool("execute", &json!({"input": input, "__agent": agent}));
            assert!(!out.error, "{}", out.text);
            out.text
        };
        run(&mut e, "agA", "page = Insert(document, {type:\"frame\", name:\"A page\", width:400, height:300})");
        let b = run(&mut e, "agB", "page = Insert(document, {type:\"frame\", name:\"B page\", x:500, width:400, height:300})");
        assert!(b.contains("carry over to later calls: page"), "{b}");
        run(&mut e, "agA", "Insert(page, {type:\"text\", name:\"A text\", content:\"from A\", fill:\"#000\"})");
        let frames = e.doc.children();
        let of = |name: &str| frames.iter().find(|n| n["name"] == name).unwrap()["children"].as_array().map_or(0, |c| c.len());
        assert_eq!((of("A page"), of("B page")), (1, 0), "A's text lands in A's frame");
        let child = e.tool("execute", &json!({"input": "Print(typeof page)", "__agent": "agC"}));
        assert!(child.text.contains("undefined"), "another agent starts without A's globals: {}", child.text);
    }

    #[test]
    fn warnings() {
        let mut e = engine();
        let r = execute(&mut e, &json!({"input": "f = Insert(document, {type:'frame', layout:'vertical', width:100, height:50}); Insert(f, {type:'text', name:'Ghost', content:'hi', x:5});"}));
        assert!(r.text.contains("has no fill"), "{}", r.text);
        assert!(r.text.contains("x/y are ignored"), "{}", r.text);
    }

    #[test]
    fn deadline_stops_endless_loop() {
        let mut e = engine();
        let t = Instant::now();
        // The interrupt is uncatchable, so a try/catch around the loop cannot swallow it.
        let src = "Insert(document, {type:'frame', name:'X'})\nwhile (true) { try { while (true) {} } catch (err) {} }";
        let out = execute_within(&mut e, &json!({"input": src}), Duration::from_millis(300));
        assert!(out.error && out.text.contains("time limit") && out.text.contains("editId"), "{}", out.text);
        assert!(t.elapsed() < Duration::from_secs(5), "{:?}", t.elapsed());
        assert!(e.doc.children().is_empty(), "rolled back");
        let next = execute(&mut e, &json!({"input": "Insert(document, {type:'frame', name:'Y'})"}));
        assert!(!next.error, "{}", next.text);
    }

    #[test]
    fn promise_jobs_run_and_rejections_fail_the_call() {
        let mut e = engine();
        let ok = execute(&mut e, &json!({"input": "(async () => { Insert(document, {type:'frame', name:'Before'}); await null; Insert(document, {type:'frame', name:'After'}) })();\nPromise.resolve().then(() => Insert(document, {type:'frame', name:'Then'}))"}));
        assert!(!ok.error, "{}", ok.text);
        let names: Vec<_> = e.doc.children().iter().map(|n| n["name"].as_str().unwrap_or("").to_string()).collect();
        assert_eq!(names, ["Before", "After", "Then"]);
        // A rejection (a throw after an await, or a bare rejected promise) rolls the call back.
        for src in ["(async () => { Insert(document, {type:'frame', name:'Late'}); await null; throw new Error('late failure') })()", "Insert(document, {type:'frame', name:'Late'}); Promise.reject(new Error('late failure'))"] {
            let r = execute(&mut e, &json!({"input": src}));
            assert!(r.error && r.text.contains("late failure") && r.text.contains("editId"), "{}", r.text);
            assert_eq!(e.doc.children().len(), 3, "rolled back");
        }
        // A rejection whose reason is undefined fails too.
        for src in ["Promise.reject()", "(async () => { await null; throw undefined })()"] {
            let r = execute(&mut e, &json!({"input": src}));
            assert!(r.error && r.text.contains("Uncaught"), "{}", r.text);
        }
        // A rejection that is handled later is not a failure; a job loop stops at the deadline.
        let handled = execute(&mut e, &json!({"input": "const p = Promise.reject(new Error('x')); p.catch(() => Print('caught'))"}));
        assert!(!handled.error && handled.text.contains("caught"), "{}", handled.text);
        let spin = execute_within(&mut e, &json!({"input": "const f = () => { Promise.resolve().then(f) }; f()"}), Duration::from_millis(300));
        assert!(spin.error && spin.text.contains("time limit"), "{}", spin.text);
        // Jobs that grow the heap fail the call at the memory cap, whatever the deadline.
        let grow = execute_within(&mut e, &json!({"input": "Insert(document, {type:'frame', name:'Late'}); const f = () => Promise.resolve().then(f); f()"}), Duration::from_secs(30));
        assert!(grow.error && grow.text.contains("out of memory") && grow.text.contains("editId"), "{}", grow.text);
        assert_eq!(e.doc.children().len(), 3, "rolled back");
    }

    #[test]
    fn memory_and_stack_limits() {
        let mut e = engine();
        let oom = execute(&mut e, &json!({"input": "Insert(document, {type:'frame'})\nconst a = []\nfor (;;) a.push(new Array(100000).fill(1.5))"}));
        assert!(oom.error && oom.text.contains("out of memory") && oom.text.contains("editId"), "{}", oom.text);
        assert!(e.doc.children().is_empty(), "rolled back");
        let deep = execute(&mut e, &json!({"input": "function f(n) { return f(n + 1) + 1 }\nf(0)"}));
        assert!(deep.error && deep.text.contains("call stack"), "{}", deep.text);
        // A streamed call that hits a limit leaves the preview instead of re-running on every update.
        e.stream_update("k", "ag0", "big = []\nfor (;;) big.push(new Array(100000).fill(1.5))\nx = 1");
        assert!(e.script.live["k"].stalled);
        assert!(!execute(&mut e, &json!({"input": "Insert(document, {type:'frame'})"})).error);
    }

    #[test]
    fn incremental_streaming_matches_cold_run() {
        let src = r##"stamp = Math.random()
const card = (p, t) => Insert(p, {type: "frame", name: t, width: 120, height: 40, fill: "#ffffff"})
page = Insert(document, {type: "frame", name: "Page", layout: "vertical", gap: 8, width: 400})
for (let i = 0; i < 5; i++) card(page, "Card " + i)
let first = Get(page, {depth: 1}).children[0].id
Update(first, {fill: "#ff0000"})
copied = Copy(first, page, {name: "Copied"})
Move(copied, page, 0)
SetVariables({accent: {type: "color", value: "#3366ff"}})
Delete(Get(page, {depth: 1}).children[2].id)
Insert(page, {type: "text", name: "Done", content: "done", fill: "#111111"})
"##;
        assert!(src.is_ascii());
        let seed = crate::svg::fxhash("toolu_s");
        let mut cold = engine();
        let out = execute(&mut cold, &json!({"input": src, "__seed": seed}));
        assert!(!out.error, "{}", out.text);

        let mut e = engine();
        let stamp = |e: &Engine| e.script.live["toolu_s"].js.with(|ctx| eval::<f64>(&ctx, "stamp", None).unwrap());
        let mut first_stamp = None;
        for i in (5..src.len()).step_by(5).chain([src.len()]) {
            e.stream_update("toolu_s", "ag0", &src[..i]);
            if first_stamp.is_none() && e.script.live.get("toolu_s").is_some_and(|l| l.ran.contains("stamp")) {
                first_stamp = Some(stamp(&e));
            }
        }
        // The last statement is held until the next token shows it ended.
        e.stream_update("toolu_s", "ag0", &format!("{src}Print(1)"));
        assert!(!e.script.live["toolu_s"].broken);
        assert_eq!(Some(stamp(&e)), first_stamp, "statements already run are not run again");
        assert_eq!(e.preview.as_ref().unwrap().root, cold.doc.root, "incremental preview equals a cold run");

        let out = e.tool("execute", &json!({"input": src, "__stream": "toolu_s", "__agent": "ag0"}));
        assert!(!out.error, "{}", out.text);
        assert!(e.preview.is_none() && e.script.live.is_empty(), "stream retired");
        assert_eq!(e.doc.root, cold.doc.root, "final run keeps the previewed ids");
        assert!(e.undo());
        assert!(e.doc.children().is_empty(), "one undo step");
    }

    #[test]
    fn streaming_statement_error_midway() {
        let mut e = engine();
        let src = "a = Insert(document, {type:'frame', name:'A', width:10, height:10})
mk('B')
function mk(n) { return Insert(document, {type:'frame', name:n, width:10, height:10}) }
Update('nope', {x: 1})
Insert(document, {type:'frame', name:'C', width:10, height:10})
";
        let upto = |s: &str| &src[..src.find(s).unwrap() + s.len()];
        let kids = |e: &Engine| e.preview.as_ref().unwrap().children().iter().map(|n| n["name"].as_str().unwrap().to_string()).collect::<Vec<_>>();
        // `mk` is not declared yet when its call runs on its own: the statement throws.
        e.stream_update("k", "ag0", upto("mk('B')\nfunction mk"));
        assert!(e.script.live["k"].broken);
        assert_eq!(kids(&e), ["A"]);
        // Once the declaration arrives, the stream re-runs from scratch in one piece and it hoists.
        e.stream_update("k", "ag0", upto("}\nUpdate("));
        assert!(!e.script.live["k"].broken);
        assert_eq!(kids(&e), ["A", "B"]);
        // A real error stops the preview there; later statements do not run.
        e.stream_update("k", "ag0", &format!("{src}x = 1"));
        assert!(e.script.live["k"].broken);
        assert_eq!(kids(&e), ["A", "B"]);

        let out = e.tool("execute", &json!({"input": src, "__stream": "k", "__agent": "ag0"}));
        assert!(out.error && out.text.contains("node not found: nope (line 4)") && out.text.contains("editId"), "{}", out.text);
        assert!(e.doc.children().is_empty() && e.preview.is_none() && e.streams.is_empty());
    }

    #[test]
    fn panic_in_streamed_host_call_breaks_the_stream_and_recovers() {
        let mut e = engine();
        let a = "Insert(document, {type:'frame', name:'A', width:10, height:10})\n";
        // A statement counts as complete once the next one has started.
        e.stream_update("k", "ag0", &format!("{a}next = 1"));
        assert_eq!(e.script.live["k"].touched.len(), 1);
        // The write before the panic stays in the preview and in the stream's touched ids.
        e.stream_update("k", "ag0", &format!("{a}Insert(document, {{type:'frame', name:'B', width:10, height:10}}); __PANIC()\nnext = 2"));
        assert!(e.script.live["k"].broken && e.script.live["k"].touched.len() == 2);
        assert!(!e.executing && EXEC.with(|c| c.borrow().is_none()));
        assert!(e.preview.is_some() && e.streams[0].touched == 2);
        // The next execute still commits.
        let out = e.tool("execute", &json!({"input": a, "__stream": "k", "__agent": "ag0"}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.doc.children().len(), 1);
        assert!(e.preview.is_none() && e.streams.is_empty() && !e.executing);
    }

    fn script_dir(name: &str, files: &[(&str, &str)]) -> std::path::PathBuf {
        let dir = std::env::temp_dir().join(format!("nalarupa-script-{name}-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        for (f, body) in files {
            std::fs::write(dir.join(f), body).unwrap();
        }
        dir
    }

    #[test]
    fn script_header_and_inputs() {
        let (schema, decl) = script_header("/**\n * @schema 2.10\n * @input n: number(min=1, max=5) = 3\n * @input mode: enum(\"a\", \"b\") = \"b\"\n * @input on: boolean = true\n * @input c: color = #ff0000\n * @input label: string = \"Hi \\\"x\\\"\"\n * @input target: ref\n */").unwrap();
        assert_eq!(schema.as_deref(), Some("2.10"));
        assert_eq!(decl.iter().map(|i| i.name.as_str()).collect::<Vec<_>>(), ["n", "mode", "on", "c", "label", "target"]);
        assert_eq!(decl[4].default, json!("Hi \"x\""));
        let given = json!({"n": 9, "mode": "a", "extra": 1}).as_object().cloned().unwrap();
        let v = script_inputs(&decl, Some(&given)).unwrap();
        assert_eq!(Value::Object(v), json!({"n": 5.0, "mode": "a", "on": true, "c": "#ff0000", "label": "Hi \"x\"", "target": ""}));
        let bad = json!({"mode": "z"}).as_object().cloned().unwrap();
        assert_eq!(script_inputs(&decl, Some(&bad)).unwrap_err().title, "Invalid input value");
        for (src, msg) in [
            ("@input x: vec3", "Invalid @input type 'vec3'"),
            ("@input x: number(step=2)", "unknown arg 'step'"),
            ("@input x: enum(\"a\") = \"q\"", "is not one of the options"),
            ("@input x: string = 4", "must be a quoted string"),
        ] {
            let err = script_header(&format!("/** {src} */")).unwrap_err();
            assert!(err.contains(msg), "{src}: {err}");
        }
    }

    #[test]
    fn script_node_runs() {
        let dir = script_dir("run", &[
            ("bars.js", "/** @schema 2.10\n * @input count: number = 3 */\nconst out = [];\nfor (let i = 0; i < nalarupa.input.count; i++) out.push({type: 'rectangle', id: 'kept?', x: i * 10, y: 0, width: 8, height: nalarupa.height * Math.random(), fill: '#ff0000', stroke: {align: 'outside', thickness: 2, fill: '#000000'}});\nreturn out;"),
            ("none.js", "/** @schema 2.19 */\nconst a = 1;"),
            ("throws.js", "/** @schema 2.19 */\nconst a = 1;\nnull.boom;\nreturn [];"),
            ("loop.js", "/** @schema 2.19 */\nwhile (true) {}"),
            ("many.js", "/** @schema 2.19 */\nreturn Array.from({length: 1001}, () => ({type: 'rectangle', width: 1, height: 1}));"),
            ("noschema.js", "return [];"),
        ]);
        let inputs = json!({"count": 2}).as_object().cloned().unwrap();
        let out = script_node("./bars.js", Some(&dir), Some(&inputs), 100.0, 50.0, None).unwrap();
        assert_eq!(out.len(), 2);
        assert!(out[0].get("id").is_none(), "ids are dropped");
        assert_eq!((out[0]["strokeWidth"].clone(), out[0]["strokeAlignment"].clone()), (json!(2), json!("outer")), "2.10 output is migrated");
        // Math.random is mulberry32 seeded 0 on every run.
        let mulberry = |s: &mut u32| {
            *s = s.wrapping_add(0x6D2B79F5);
            let mut t = (*s ^ (*s >> 15)).wrapping_mul(1 | *s);
            t = (t.wrapping_add((t ^ (t >> 7)).wrapping_mul(61 | t))) ^ t;
            ((t ^ (t >> 14)) as f64) / 4294967296.0
        };
        let mut seed = 0u32;
        assert!((out[0]["height"].as_f64().unwrap() - 50.0 * mulberry(&mut seed)).abs() < 1e-9);
        assert!((out[1]["height"].as_f64().unwrap() - 50.0 * mulberry(&mut seed)).abs() < 1e-9);
        assert_eq!(script_node("./bars.js", Some(&dir), Some(&inputs), 100.0, 50.0, None).unwrap(), out, "deterministic");

        let err = |f: &str| script_node(f, Some(&dir), None, 10.0, 10.0, None).unwrap_err();
        assert_eq!(err("").kind, "empty");
        assert!(err("./none.js").description.unwrap().contains("Script must return an array of nodes"));
        let t = err("./throws.js");
        assert_eq!(t.title, "Script runtime error");
        assert!(t.code.as_deref().is_some_and(|c| c.contains("> 3: null.boom;")), "{t:?}");
        let started = Instant::now();
        assert_eq!(err("./loop.js").title, "Script runtime error");
        assert!(started.elapsed() < Duration::from_secs(4), "2 s budget");
        assert_eq!(err("./many.js").title, "Too many nodes");
        assert_eq!(err("./noschema.js").title, "Missing @schema version tag");
        assert_eq!(err("./missing.js").title, "Script not found");
    }

    #[test]
    fn script_node_renders_and_hits_as_one() {
        let dir = script_dir("render", &[("dots.js", "/** @schema 2.19\n * @input color: color = #00ff00 */\nreturn [{type: 'ellipse', name: 'dot', x: nalarupa.width - 20, y: 0, width: 20, height: 20, fill: pencil.input.color}, {type: 'ref', ref: 'comp', x: 0, y: 30}];")]);
        let doc = json!({"version": "2.19", "children": [
            {"type": "frame", "id": "comp", "x": 0, "y": 500, "width": 10, "height": 10, "reusable": true, "fill": "#0000ff"},
            {"type": "frame", "id": "page", "x": 0, "y": 0, "width": 300, "height": 200, "layout": "vertical", "children": [
                {"type": "script", "id": "s", "width": "fill_container", "height": 100, "clip": true, "scriptUri": "./dots.js", "inputs": {"color": "#ff0000"}},
                {"type": "script", "id": "bad", "width": 120, "height": 80, "scriptUri": "./nope.js"}
            ]}
        ]});
        std::fs::write(dir.join("doc.pen"), doc.to_string()).unwrap();
        let mut e = engine();
        e.open(&dir.join("doc.pen")).unwrap();
        let (svg, _) = e.ctx().node_svg("page").unwrap();
        // The script ran with its laid-out width (fill_container = 300) and the input value.
        assert!(svg.contains("#ff0000") && svg.contains("clip-path"), "{svg}");
        assert!(svg.contains("Script not found") || svg.contains("fill-opacity=\"0.102\""), "error status is drawn");
        e.sync();
        let dot = e.renderer.find("s/0").expect("generated child is laid out");
        assert_eq!((dot.x, dot.w), (280.0, 20.0));
        assert!(e.renderer.find("s/1").is_some_and(|f| f.y == 30.0), "ref output resolves the component");
        assert_eq!(crate::render::hit_chain(&e.renderer, 290.0, 10.0).last().map(String::as_str), Some("s"));
        assert!(e.doc.find("s").unwrap().get("children").is_none(), "output is never saved");
    }

    #[test]
    fn live_script_runs_off_the_lock() {
        // `busy()` counts every slot: a screenshot test's sync on the global engine queues runs too.
        let _serial = global_execute();
        let dir = script_dir("live", &[("slow.js", "/** @schema 2.19 */\nconst s = Date.now(); while (Date.now() - s < 400) {}\nreturn [{type: 'rectangle', width: nalarupa.width, height: 10}];")]);
        let doc = json!({"version": "2.19", "children": [
            {"type": "frame", "id": "page", "x": 0, "y": 0, "width": 300, "height": 200, "layout": "none", "children": [
                {"type": "script", "id": "s", "x": 0, "y": 0, "width": 100, "height": 100, "scriptUri": "./slow.js"}]}
        ]});
        std::fs::write(dir.join("doc.pen"), doc.to_string()).unwrap();
        let mut e = engine();
        e.open(&dir.join("doc.pen")).unwrap();
        let started = Instant::now();
        e.sync();
        assert!(started.elapsed() < Duration::from_millis(300), "sync does not wait for the script: {:?}", started.elapsed());
        assert!(e.renderer.find("s/0").is_none(), "no output yet");
        assert!(busy(), "the run is in the background");
        // A resize meanwhile only queues the newest size and is as quick.
        let started = Instant::now();
        for w in 101..106 {
            e.edit(|d| d.update("s", &json!({"width": w}))).unwrap();
            e.sync();
        }
        assert!(started.elapsed() < Duration::from_millis(300), "resizing does not wait either: {:?}", started.elapsed());
        let until = Instant::now() + Duration::from_secs(10);
        while e.renderer.find("s/0").is_none_or(|f| f.w != 105.0) {
            assert!(Instant::now() < until, "the runs finish and the layer is rebuilt");
            std::thread::sleep(Duration::from_millis(20));
            e.expire_slides();
            e.sync();
        }
        assert!(!busy());
        // Back to the size that is running drops the stale one waiting behind it.
        let call = |w: f64| script_node("./slow.js", Some(&dir), None, w, 10.0, Some("back-slot"));
        let wait = |until: &dyn Fn() -> bool| {
            let limit = Instant::now() + Duration::from_secs(10);
            while !until() {
                assert!(Instant::now() < limit);
                std::thread::sleep(Duration::from_millis(20));
            }
        };
        assert!(call(101.0).is_err());
        wait(&|| scripts().as_ref().unwrap().slots["back-slot"].next.is_none()); // the worker has taken it
        call(102.0);
        call(101.0);
        assert!(scripts().as_ref().unwrap().slots["back-slot"].next.is_none(), "the stale size no longer waits");
        wait(&|| !busy());
        assert_eq!(call(101.0).unwrap()[0]["width"], 101.0);
    }

    /// Puts the global engine's document back when dropped: a script may export only into the open document's folder.
    struct RestoreDoc(Option<crate::doc::Doc>);

    impl Drop for RestoreDoc {
        fn drop(&mut self) {
            let mut e = crate::engine::engine();
            e.doc = self.0.take().unwrap();
            e.touch();
        }
    }

    /// A script node in a 100 px page, opened in the global engine until the guard drops.
    fn open_slow_script(name: &str, src: &str) -> (std::path::PathBuf, RestoreDoc) {
        let dir = script_dir(name, &[("slow.js", src)]);
        let doc = json!({"version": "2.19", "children": [
            {"type": "frame", "id": "page", "x": 0, "y": 0, "width": 100, "height": 100, "layout": "none", "fill": "#ffffff", "children": [
                {"type": "script", "id": "s", "x": 0, "y": 0, "width": 100, "height": 100, "scriptUri": "./slow.js"}]}
        ]});
        std::fs::write(dir.join("doc.pen"), doc.to_string()).unwrap();
        let mut e = crate::engine::engine();
        let before = e.doc.clone();
        e.open(&dir.join("doc.pen")).unwrap();
        (dir, RestoreDoc(Some(before)))
    }

    /// Exporting a script node never runs its JavaScript under the engine lock, and the file still shows its output.
    #[test]
    fn ui_exports_run_script_nodes_off_the_lock() {
        let _serial = global_execute();
        let (dir, _restore) = open_slow_script("ui-export-lock", "/** @schema 2.19 */\nconst s = Date.now(); while (Date.now() - s < 1000) {}\nreturn [{type: 'rectangle', width: nalarupa.width, height: nalarupa.height, fill: '#ff0000'}];");
        let (ids, opts) = (vec!["page".to_string()], json!({"scale": 1}));
        let started = Instant::now();
        let planned = crate::html::plan(&mut crate::engine::engine(), &ids, "png", dir.join("planned").to_str().unwrap(), &opts);
        assert!(planned.is_ok() && started.elapsed() < Duration::from_millis(500), "planning under the lock waited for the script: {:?}", started.elapsed());
        let files = crate::html::export(&ids, "png", dir.join("ui").to_str().unwrap(), &opts).unwrap();
        assert!(red_middle(&std::fs::read(&files[0]).unwrap()), "the script's output is in the export");
    }

    /// The agent's screenshots and exports of a script node run it after the tool call, off the engine lock.
    #[test]
    fn agent_shots_and_exports_run_script_nodes_off_the_lock() {
        let _serial = global_execute();
        let (dir, _restore) = open_slow_script("agent-export-lock", "/** @schema 2.19 */\nconst s = Date.now(); while (Date.now() - s < 1000) {}\nreturn [{type: 'rectangle', width: nalarupa.width, height: nalarupa.height, fill: '#ff0101'}];");
        let src = format!("TakeScreenshot(\"page\")\nExport(\"page\", \"png\", {:?}, {{scale: 1}})", dir.join("agent").to_str().unwrap());
        let started = Instant::now();
        let out = crate::engine::engine().tool("execute", &json!({"input": src}));
        assert!(!out.error, "{}", out.text);
        assert!(started.elapsed() < Duration::from_millis(500), "the tool call waited for the script under the lock: {:?}", started.elapsed());
        let out = out.finish();
        assert!(out.text.contains("Exported:"), "{}", out.text);
        let shot = base64::Engine::decode(&base64::engine::general_purpose::STANDARD, &out.images[0]).unwrap();
        assert!(red_middle(&shot), "the script's output is in the screenshot");
        let file = std::fs::read_dir(dir.join("agent")).unwrap().next().unwrap().unwrap().path();
        assert!(red_middle(&std::fs::read(file).unwrap()), "the script's output is in the export");
    }

    /// The settled layer composites a semi-transparent parent with its children as one group, which a
    /// slide painted over the parent's own paint cannot match (47 levels off, then a pop): an insert
    /// under a node with opacity below 1, at any depth, appears in place instead.
    #[test]
    fn insert_under_a_semi_transparent_ancestor_appears_in_place() {
        let mut e = engine();
        let out = execute(&mut e, &json!({"input": r##"
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            Insert(root, {type:"frame", name:"Glass", width:200, height:200, layout:"none", opacity:0.5});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 1, "the glass frame itself slides into its opaque parent");
        e.renderer.slides.clear();
        let out = execute(&mut e, &json!({"input": r##"
            Insert(Get(n => n.name === "Glass" ? n.id : undefined)[0], {type:"rectangle", name:"a", width:10, height:10});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert!(e.renderer.slides.is_empty(), "a child of a semi-transparent frame does not slide");
        let out = execute(&mut e, &json!({"input": r##"
            inner = Insert(Get(n => n.name === "Glass" ? n.id : undefined)[0], {type:"frame", name:"Inner", width:100, height:100, layout:"none"});
            Insert(inner, {type:"rectangle", name:"b", width:10, height:10});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert!(e.renderer.slides.is_empty(), "nor does a deeper descendant");
        let out = execute(&mut e, &json!({"input": r##"
            Insert(Get(n => n.name === "Root" ? n.id : undefined)[0], {type:"rectangle", name:"c", width:10, height:10});
        "##}));
        assert!(!out.error, "{}", out.text);
        assert_eq!(e.renderer.slides.len(), 1, "an insert under an opaque frame still slides");
        // Opacity given as a variable counts as its value for the theme active there.
        e.doc.set_variables(&json!({"dim": {"type": "number", "value": [{"value": 1}, {"value": 0.5, "theme": {"mode": "dark"}}]}}), false).unwrap();
        for (theme, slides) in [("light", 1), ("dark", 0)] {
            let out = execute(&mut e, &json!({"input": format!(r##"
                Insert(Get(n => n.name === "Root" ? n.id : undefined)[0], {{type:"frame", name:"{theme}", width:50, height:50, layout:"none", opacity:"$dim", theme:{{mode:"{theme}"}}}});
            "##)}));
            assert!(!out.error, "{}", out.text);
            e.renderer.slides.clear();
            let out = execute(&mut e, &json!({"input": format!(r##"
                Insert(Get(n => n.name === "{theme}" ? n.id : undefined)[0], {{type:"rectangle", width:10, height:10}});
            "##)}));
            assert!(!out.error, "{}", out.text);
            assert_eq!(e.renderer.slides.len(), slides, "{theme}: opacity from a variable");
        }
    }
}
