//! Built-in design agents:
//! - every agent has a profile (name + color) that the canvas cursor and chat use;
//! - `api` backend: streamed Claude Messages API with a manual tool loop;
//! - `claude-code` backend: the user's `claude` CLI with partial-message streaming,
//!   wired to our MCP server through a per-agent URL (`/mcp/<agent-id>`);
//! - `execute` input is fed to the engine while it streams, so designs appear
//!   statement by statement before the call completes;
//! - `spawn_agents` starts parallel child agents that each own container frames.
//! The UI polls the events emitted here.

use crate::engine::{engine, tool_specs, Engine};
use serde_json::{json, Value};
use std::collections::HashMap;
use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Condvar, Mutex, OnceLock};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

pub const DEFAULT_MODEL: &str = "claude-opus-5-5";
/// Models with a fast output mode (Claude Code `fastMode`, API `speed: "fast"`).
pub const FAST_MODELS: [&str; 3] = ["claude-opus-5-5", "claude-opus-5", "claude-opus-4-8"];

/// Fast mode only where the model has it; `model` is a full id (or None for the CLI default).
fn fast_for(model: Option<&str>, fast: bool) -> bool {
    fast && model.is_some_and(|m| FAST_MODELS.contains(&m))
}
const MAX_TURNS: usize = 80;
const MAX_CHILDREN: usize = 5;
/// Saved chats untouched for this many days are deleted when the history is listed.
const CHAT_KEEP_DAYS: u64 = 20;
/// Re-run the streamed snippet at most this often while the model is writing it.
const STREAM_THROTTLE: Duration = Duration::from_millis(120);

const NAMES: &[&str] = &["Arunika", "Bayu", "Candra", "Dewi", "Gita", "Langit", "Mega", "Rama", "Sekar", "Tirta", "Wulan", "Surya"];
const COLORS: &[&str] = &["#7C9CFF", "#4FD1C5", "#FFB347", "#B794F4", "#FF7AB6", "#63B3ED", "#F6AD55", "#68D391", "#FC8181", "#F6E05E", "#9F7AEA", "#38B2AC"];

#[derive(Clone)]
pub struct Profile {
    pub id: String,
    pub name: String,
    pub color: String,
    pub parent: Option<String>,
}

impl Profile {
    pub fn json(&self) -> Value {
        json!({"id": self.id, "name": self.name, "color": self.color, "parent": self.parent})
    }
}

#[derive(Default)]
pub struct AgentState {
    /// Claude API history of the main conversation.
    pub messages: Vec<Value>,
    /// Claude Code session of the main conversation.
    pub session: Option<String>,
    /// True while any agent (main or child) is working.
    pub running: bool,
    active: usize,
    pub cancel: Arc<AtomicBool>,
    pub profiles: Vec<Profile>,
    pub main: Option<String>,
    /// Prompts sent while agents were busy; flushed as one message afterwards.
    pub queue: Vec<String>,
    /// Parallel agent setting of the current turn: (count, mode "split" | "side").
    pub parallel: (usize, String),
    /// The conversation being saved to `<data_dir>/sessions/<id>.json`; None until the first prompt.
    pub chat: Option<Chat>,
    /// Permission requests waiting for the user's answer (request id → asking agent, the waiting MCP call).
    pub permissions: HashMap<String, (String, std::sync::mpsc::Sender<bool>)>,
    /// "Let it cook": designs left in this run (counting the current one) and "layout" | "style".
    pub cook: (usize, String),
    /// Frames an AI Pen is still drawing into, one entry per running pen: its `placeholder` flag is the pen's to clear.
    pub pens: Vec<String>,
}

pub struct Chat {
    pub id: String,
    /// The first prompt, cut to 100 characters.
    pub title: String,
    /// Agent events as the UI folds them (deltas merged, progress dropped); the UI replays them on load.
    pub events: Vec<Value>,
}

impl AgentState {
    pub fn profile(&self, id: &str) -> Option<&Profile> {
        self.profiles.iter().find(|p| p.id == id)
    }

    /// Keep an agent event of the current chat. True when it ends a message or a turn: time to save.
    pub fn record(&mut self, ev: &Value) -> bool {
        let Some(chat) = &mut self.chat else { return false };
        let kind = ev["kind"].as_str().unwrap_or("");
        match kind {
            // Permission cards only matter while they wait.
            "tool_progress" | "all_done" | "permission" | "permission_done" => return false,
            "text_delta" | "thinking_delta" => {
                let last = chat.events.iter_mut().rev().find(|e| e["agent"]["id"] == ev["agent"]["id"]);
                if let Some(last) = last.filter(|e| e["kind"] == kind) {
                    last["text"] = json!(format!("{}{}", last["text"].as_str().unwrap_or(""), ev["text"].as_str().unwrap_or("")));
                    return false;
                }
            }
            _ => {}
        }
        let mut ev = ev.clone();
        ev["at"] = json!(now_ms());
        chat.events.push(ev);
        matches!(kind, "user" | "message_end" | "done" | "dequeued")
    }

    /// Start over with no conversation (the old one stays saved).
    fn clear(&mut self) {
        self.messages.clear();
        self.session = None;
        self.main = None;
        self.profiles.clear();
        self.queue.clear();
        self.chat = None;
        self.cook.0 = 1;
    }

    /// Continue a saved chat: backend resume state, main agent identity and transcript.
    fn restore(&mut self, id: &str, saved: &Value) {
        self.clear();
        self.messages = saved["messages"].as_array().cloned().unwrap_or_default();
        self.session = saved["session"].as_str().map(str::to_string);
        self.main = self.add_saved(&saved["agent"]);
        let events = saved["events"].as_array().cloned().unwrap_or_default();
        // Every agent of the chat keeps its name, so agents spawned from here on get other names.
        for ev in &events {
            self.add_saved(&ev["agent"]);
        }
        self.chat = Some(Chat { id: id.into(), title: saved["title"].as_str().unwrap_or_default().into(), events });
    }

    /// A saved agent identity (`Profile::json`), added once per id; returns its id.
    fn add_saved(&mut self, a: &Value) -> Option<String> {
        let (Some(pid), Some(name), Some(color)) = (a["id"].as_str(), a["name"].as_str(), a["color"].as_str()) else { return None };
        if self.profile(pid).is_none() {
            self.profiles.push(Profile { id: pid.into(), name: name.into(), color: color.into(), parent: a["parent"].as_str().map(str::to_string) });
        }
        Some(pid.into())
    }

    fn new_profile(&mut self, parent: Option<String>) -> Profile {
        // Ids count up for the whole process, not per conversation: the script globals of an
        // earlier conversation's main agent stay keyed by its id. The fixed-width suffix keeps two counts from spelling one id.
        static NEXT_ID: AtomicUsize = AtomicUsize::new(0);
        let n = self.profiles.len();
        let used: Vec<&str> = self.profiles.iter().map(|p| p.name.as_str()).collect();
        let pick = (crate::doc::random_u64() as usize) % NAMES.len();
        // Names stay unique within a conversation: once all are taken, count up ("Surya 2").
        let name = (0..NAMES.len()).map(|i| NAMES[(pick + i) % NAMES.len()].to_string()).find(|n| !used.contains(&n.as_str())).unwrap_or_else(|| {
            (2..).map(|k| format!("{} {k}", NAMES[pick])).find(|n| !used.contains(&n.as_str())).unwrap_or_default()
        });
        let p = Profile { id: format!("ag{}{:03x}", NEXT_ID.fetch_add(1, Ordering::Relaxed), crate::doc::random_u64() & 0xfff), name, color: COLORS[(pick + n * 5) % COLORS.len()].to_string(), parent };
        self.profiles.push(p.clone());
        p
    }
}

/// Emit an agent event (chat + canvas). Carries the agent identity so the UI can color it.
fn emit(p: &Profile, kind: &str, extra: Value) {
    let mut ev = json!({"type": "agent", "kind": kind, "agent": p.json()});
    if let (Some(o), Some(x)) = (ev.as_object_mut(), extra.as_object()) {
        for (k, v) in x {
            o.insert(k.clone(), v.clone());
        }
    }
    engine().emit(ev);
}

pub(crate) fn backend() -> String {
    engine().backend()
}

const SYSTEM_INTRO: &str = "You are a design agent inside Nalarupa, an infinite-canvas design editor. You design directly on the user's canvas \
(web pages, app screens, dashboards, slide decks, social graphics) by calling the `execute` tool; every statement you write appears on the canvas while you write it. \
Keep chat replies short: one sentence on what you are about to build, build it in visible steps (one execute per section), then summarize in one or two sentences. \
The guide below is the source of truth for the document format and the execute API.\n\n";

/// "Let it cook" (iteration mode): with more than one design the agent keeps going after a
/// design is done, one variation per finished turn.
pub const MAX_DESIGNS: usize = 6;
const FIRST_DESIGN: &str = "This is the first of several design variations. Unless the user asked for a flow or several screens, design one focused screen; \
for a broad product idea that is the main screen people land on first, the one that sets the visual and conceptual tone. Keep the scope tight: \
later iterations explore other directions for exactly this scope.";
const NEXT_LAYOUT: &str = "Now design another variation of this page. Work out, as a strong product designer would, what could inspire it and give it more depth; \
draw on what you already know instead of browsing the web, and let those insights shape the result. Make it a clearly different layout, placed next to \
the earlier designs. Important: keep the visual style of the first design (fonts, palette, radius, elevation); do not pick a new style.";
const NEXT_STYLE: &str = "Now design another variation of this page. Work out, as a strong product designer would, what could inspire it and give it more depth; \
draw on what you already know instead of browsing the web, and let those insights shape the result. Placed next to the earlier designs. Important: give it a new \
visual direction: call get_style and choose a style archetype that none of the earlier variations used; do not reuse the previous style.";
const ITERATING: &str = "Iterating more on the design…";

/// Start a turn. While agents are busy the prompt is queued and sent when they finish.
/// `designs` > 1 turns on "Let it cook" with `iterate` = "layout" or "style".
pub fn send(prompt: String, parallel: usize, mode: &str, designs: usize, iterate: &str) -> Result<(), String> {
    {
        let mut e = engine();
        if e.agent.running {
            e.agent.queue.push(prompt.clone());
            e.emit(json!({"type": "agent", "kind": "queued", "text": prompt}));
            return Ok(());
        }
    }
    begin(prompt, parallel, mode, designs, iterate, false);
    Ok(())
}

/// Start a turn without the busy check: also how a finished run hands over to its continuation (`resume`).
fn begin(prompt: String, parallel: usize, mode: &str, designs: usize, iterate: &str, resume: bool) {
    {
        let mut e = engine();
        e.agent.parallel = (parallel.clamp(1, MAX_CHILDREN + 1), if mode == "side" { "side".into() } else { "split".into() });
        e.agent.cook = (designs.clamp(1, MAX_DESIGNS), if iterate == "style" { "style".into() } else { "layout".into() });
    }
    let mut text = wrap_parallel(&prompt);
    if designs > 1 {
        text = format!("{text}\n\n{FIRST_DESIGN}");
    }
    start_turn(&prompt, text, resume);
}

/// One-line chat title: the first prompt with whitespace runs (line breaks too) collapsed, capped at 100 chars.
fn chat_title(shown: &str) -> String {
    shown.split_whitespace().collect::<Vec<_>>().join(" ").chars().take(100).collect()
}

/// Send `prompt` to the main agent (same conversation), showing `shown` as the user's message.
/// A `resume` turn continues a finished run: it is dropped when Stop came in since that run ended.
fn start_turn(shown: &str, prompt: String, resume: bool) {
    // Held until the run is marked running: a clear that sweeps the chat files must not interleave with a new chat's first save.
    let ops = chat_ops();
    let main = {
        let mut e = engine();
        if resume && e.agent.cancel.load(Ordering::SeqCst) {
            // Prompts sent after Stop queued behind this continuation: run them now as a fresh turn,
            // keeping the agent busy so nothing else slips in, instead of leaving them stranded.
            let queued = std::mem::take(&mut e.agent.queue);
            if queued.is_empty() {
                e.agent.running = false;
                return;
            }
            let (n, mode) = e.agent.parallel.clone();
            drop(e);
            drop(ops);
            begin(queued.join("\n\n"), n, &mode, 1, "", false);
            return;
        }
        if e.agent.chat.is_none() {
            let id = format!("{:x}{:04x}", now_ms(), crate::doc::random_u64() & 0xffff);
            e.agent.chat = Some(Chat { id, title: chat_title(shown), events: Vec::new() });
        }
        let main = match e.agent.main.clone().and_then(|id| e.agent.profile(&id).cloned()) {
            Some(p) => p,
            None => {
                let p = e.agent.new_profile(None);
                e.agent.main = Some(p.id.clone());
                p
            }
        };
        e.agent.cancel = Arc::new(AtomicBool::new(false));
        main
    };
    emit(&main, "user", json!({"text": shown}));
    start_thread(main, prompt, None);
}

/// Parallel-agents wrappers ("split work" and "side by side" modes).
fn wrap_parallel(prompt: &str) -> String {
    let (n, mode) = engine().agent.parallel.clone();
    if n <= 1 {
        return prompt.to_string();
    }
    if mode == "side" {
        return format!(
            "{prompt}\n\nWork as the coordinator of {n} designers who each design their own take on this task side by side.\n\
             1. Pick a sensible screen size for the task.\n\
             2. Create {n} empty top-level frames in one row, named \"1. <short title>\" … \"{n}. <short title>\" (not placeholders), using FindEmptySpace.\n\
             3. Call spawn_agents with {} entries: entry k gets containerNodes [frame k+1] and an empty prompt.\n\
             4. Then design your own take inside frame 1. Do not touch the other frames.",
            n - 1
        );
    }
    format!(
        "{prompt}\n\nSplit this work across at most {} extra designers with the `spawn_agents` tool. First create the container frames each part needs \
         (top-level frames placed with FindEmptySpace), give each designer one or more containers and a self-contained task, keep the last part for yourself, \
         and continue with your own part right after spawning.",
        n - 1
    )
}

fn start_thread(p: Profile, prompt: String, child: Option<String>) {
    {
        let mut e = engine();
        e.agent.active += 1;
        e.agent.running = true;
    }
    std::thread::spawn(move || {
        let started = Instant::now();
        emit(&p, "start", json!({"child": child.is_some()}));
        let result = if backend() == "claude-code" { run_claude_code(&p, &prompt, child.is_some()) } else { run_api(&p, &prompt, child.is_some()) };
        let stopped = engine().agent.cancel.load(Ordering::SeqCst);
        let usage = match result {
            Ok(u) => u,
            Err(err) => {
                if !stopped {
                    if !err.is_empty() {
                        emit(&p, "error", json!({"text": err}));
                    }
                    // A failed main turn ends the iterations.
                    if child.is_none() {
                        engine().agent.cook.0 = 1;
                    }
                }
                json!({})
            }
        };
        {
            let mut e = engine();
            e.streams_of_agent_end(&p.id);
            // A child runs once under a fresh id: its globals can never be read again.
            if child.is_some() {
                e.script.globals.remove(&p.id);
            }
            // A request its (now gone) process still waits on is denied; its card goes away.
            e.agent.permissions.retain(|_, (agent, waiting)| *agent != p.id || waiting.send(false).is_err());
        }
        emit(&p, "done", json!({"durationMs": started.elapsed().as_millis() as u64, "usage": usage, "stopped": stopped}));
        enum Next {
            Iterate(&'static str),
            Queued(String),
        }
        let next = {
            let mut e = engine();
            e.agent.active -= 1;
            if e.agent.active > 0 {
                None
            } else {
                clear_generating(&mut e);
                e.emit(json!({"type": "agent", "kind": "all_done"}));
                // The next variation starts before the queue: queued prompts wait for the last variation.
                let next = if e.agent.cook.0 > 1 && !stopped {
                    e.agent.cook.0 -= 1;
                    Some(Next::Iterate(if e.agent.cook.1 == "style" { NEXT_STYLE } else { NEXT_LAYOUT }))
                } else if stopped {
                    // Prompts queued while the stopped agents were shutting down are dropped too.
                    drop_queue(&mut e);
                    None
                } else {
                    (!e.agent.queue.is_empty()).then(|| Next::Queued(std::mem::take(&mut e.agent.queue).join("\n\n")))
                };
                // A continuation keeps the agent busy until its thread starts, so a reset cannot slip in between.
                e.agent.running = next.is_some();
                next
            }
        };
        match next {
            Some(Next::Iterate(prompt)) => start_turn(ITERATING, wrap_parallel(prompt), true),
            Some(Next::Queued(q)) => {
                let (n, mode) = engine().agent.parallel.clone();
                begin(q, n, &mode, 1, "", true);
            }
            None => {}
        }
    });
}

/// The run is over: frames still flagged `placeholder` (Stop, an error or a dead process came before the
/// call that clears it) stop showing as "Generating". Silent and out of the undo history, like the AI Pen's own finish.
fn clear_generating(e: &mut Engine) {
    let mut stale = Vec::new();
    crate::doc::walk(e.doc.children(), &mut |n| {
        if n["placeholder"] == true && n["id"].as_str().is_some_and(|id| !e.agent.pens.iter().any(|p| p == id)) {
            stale.push(n["id"].as_str().unwrap_or_default().to_string());
        }
    });
    let off = json!({"placeholder": false}).as_object().cloned().unwrap_or_default();
    for id in stale {
        e.apply_generated(&id, &off, true);
    }
}

/// Drop the queued prompts and tell the UI, so their bubbles stop promising to send them.
fn drop_queue(e: &mut Engine) {
    if !std::mem::take(&mut e.agent.queue).is_empty() {
        e.emit(json!({"type": "agent", "kind": "dequeued"}));
    }
}

pub fn stop() {
    let mut e = engine();
    e.agent.cancel.store(true, Ordering::SeqCst);
    drop_queue(&mut e);
    e.agent.cook.0 = 1;
    for (_, (_, waiting)) in e.agent.permissions.drain() {
        let _ = waiting.send(false);
    }
    let ids: Vec<String> = e.agent.profiles.iter().map(|p| p.id.clone()).collect();
    for id in ids {
        e.streams_of_agent_end(&id);
    }
}

/// `agent_reset`: start a new conversation. The running thread keeps writing into whatever chat and
/// session are current, so it is refused until the agents have stopped.
pub fn reset() -> Result<(), String> {
    let mut e = engine();
    if e.agent.running {
        return Err("Stop the agent before starting a new conversation.".into());
    }
    e.agent.clear();
    Ok(())
}

/// Name of our MCP tool that Claude Code asks before running a tool it may not use on its own
/// (`--permission-prompt-tool`).
pub const PERMISSION_TOOL: &str = "approve";

/// Claude Code wants a tool that is not pre-allowed. File tools on paths inside
/// the open document's folder (and reading installed skills) are allowed outright; anything else
/// shows a permission card and waits for the user. Answers Claude Code's permission JSON.
pub fn ask_permission(agent: &str, args: &Value) -> crate::engine::ToolOut {
    let tool = args["tool_name"].as_str().unwrap_or("").to_string();
    let input = args["input"].clone();
    let allow = crate::engine::ToolOut::ok(json!({"behavior": "allow", "updatedInput": input}).to_string());
    let deny = |why: &str| crate::engine::ToolOut::ok(json!({"behavior": "deny", "message": why}).to_string());
    let (asked, p, id) = {
        let mut e = engine();
        let skills = dirs::home_dir().map(|h| h.join(".claude").join("skills"));
        if input["file_path"].as_str().is_some_and(|f| path_allowed(Path::new(f), &tool, e.doc.dir().as_deref(), e.doc.path.as_deref(), skills.as_deref())) {
            return allow;
        }
        let Some(p) = e.agent.profile(agent).cloned() else { return deny("Unknown agent.") };
        if e.agent.cancel.load(Ordering::SeqCst) {
            return deny("The agent was stopped.");
        }
        let id = format!("pm{:x}", crate::doc::random_u64() & 0xffff_ffff);
        let (tx, rx) = std::sync::mpsc::channel();
        e.agent.permissions.insert(id.clone(), (p.id.clone(), tx));
        (rx, p, id)
    };
    emit(&p, "permission", json!({"id": id, "tool": tool, "input": input}));
    let allowed = asked.recv().unwrap_or(false);
    emit(&p, "permission_done", json!({"id": id, "allowed": allowed}));
    if allowed { allow } else { deny("The user denied permission for this tool call.") }
}

/// `agent_permission`: the user's answer to a permission card: "allow", "deny", or "auto"
/// (allow, and from the next turn on let Claude Code's auto mode decide; kept as a setting).
pub fn answer_permission(id: &str, answer: &str) -> Result<(), String> {
    let mut e = engine();
    if answer == "auto" {
        e.settings.insert("agent_auto_permissions".into(), json!(true));
        let _ = e.save_settings(); // still auto for this run when it did not save; never block the answer
    }
    let (_, waiting) = e.agent.permissions.remove(id).ok_or("This permission request is no longer waiting.")?;
    let _ = waiting.send(answer != "deny");
    Ok(())
}

/// Allowed paths: inside the workspace (here: the document's folder) for any file tool,
/// inside the skills folder for Read. The open document itself is never written behind the app's back.
fn path_allowed(file: &Path, tool: &str, workspace: Option<&Path>, document: Option<&Path>, skills: Option<&Path>) -> bool {
    let file = normalize(file);
    if document.is_some_and(|d| normalize(d) == file) && tool != "Read" {
        return false;
    }
    let inside = |root: Option<&Path>| root.is_some_and(|r| file.starts_with(normalize(r)));
    inside(workspace) || (tool == "Read" && inside(skills))
}

/// Absolute path with `.` and `..` resolved lexically, so `dir/../../x` cannot pass as inside `dir`.
fn normalize(p: &Path) -> PathBuf {
    let mut out = PathBuf::new();
    for c in std::path::absolute(p).unwrap_or_else(|_| p.to_path_buf()).components() {
        match c {
            std::path::Component::ParentDir => {
                out.pop();
            }
            std::path::Component::CurDir => {}
            c => out.push(c),
        }
    }
    out
}

fn now_ms() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map_or(0, |d| d.as_millis() as u64)
}

/// Chat ids come back from the UI: only plain alphanumerics may name a file.
fn chat_path(data_dir: &Path, id: &str) -> Result<PathBuf, String> {
    if id.is_empty() || !id.bytes().all(|b| b.is_ascii_alphanumeric()) {
        return Err(format!("invalid chat id: {id}"));
    }
    Ok(data_dir.join("sessions").join(format!("{id}.json")))
}

/// A chat snapshot on its way to disk: cloned under the engine lock, serialized and written outside it.
struct ChatSave {
    data_dir: PathBuf,
    path: PathBuf,
    doc: Value,
}

impl ChatSave {
    /// Write atomically: a temp file, then a rename over the old one.
    fn write(&self) -> Result<(), String> {
        let tmp = self.path.with_extension("json.tmp");
        let write = || -> std::io::Result<()> {
            std::fs::create_dir_all(self.data_dir.join("sessions"))?;
            let mut f = std::fs::File::create(&tmp)?;
            f.write_all(self.doc.to_string().as_bytes())?;
            f.sync_all()?;
            std::fs::rename(&tmp, &self.path)
        };
        write().map_err(|e| e.to_string())
    }
}

fn snapshot_chat(data_dir: &Path, a: &AgentState) -> Result<Option<ChatSave>, String> {
    let Some(chat) = &a.chat else { return Ok(None) };
    let path = chat_path(data_dir, &chat.id)?;
    let main = a.main.as_deref().and_then(|id| a.profile(id)).map(Profile::json);
    // ponytail: rewrites the whole file at every message end; switch to an append-only log if long chats get slow.
    let doc = json!({"version": 1, "id": chat.id, "title": chat.title, "updatedAt": now_ms(), "agent": main,
                     "events": chat.events, "messages": a.messages, "session": a.session});
    Ok(Some(ChatSave { data_dir: data_dir.to_path_buf(), path, doc }))
}

/// The chat writer: the newest snapshot waits in `pending` (older ones are superseded), `busy` while one is written;
/// `failed` holds the error of the last write, cleared by the next one that succeeds.
#[derive(Default)]
struct ChatWriter {
    state: Mutex<(Option<ChatSave>, bool, Option<String>)>,
    changed: Condvar,
}

fn chat_writer() -> &'static ChatWriter {
    static WRITER: OnceLock<&'static ChatWriter> = OnceLock::new();
    WRITER.get_or_init(|| {
        let w: &'static ChatWriter = Box::leak(Box::default());
        std::thread::spawn(move || loop {
            let job = {
                let mut s = w.state.lock().unwrap_or_else(|e| e.into_inner());
                while s.0.is_none() {
                    s = w.changed.wait(s).unwrap_or_else(|e| e.into_inner());
                }
                s.1 = true;
                s.0.take()
            };
            let result = job.map_or(Ok(()), |j| j.write());
            {
                let mut s = w.state.lock().unwrap_or_else(|e| e.into_inner());
                s.1 = false;
                s.2 = result.clone().err();
            }
            w.changed.notify_all();
            if let Err(err) = result {
                engine().emit(json!({"type": "toast", "text": format!("Could not save the chat: {err}")}));
            }
        });
        w
    })
}

/// Save the current chat in the background. Only the clone happens here, under the engine lock.
pub fn save_chat(data_dir: &Path, a: &AgentState) -> Result<(), String> {
    let Some(job) = snapshot_chat(data_dir, a)? else { return Ok(()) };
    let w = chat_writer();
    w.state.lock().unwrap_or_else(|e| e.into_inner()).0 = Some(job);
    w.changed.notify_all();
    Ok(())
}

/// Wait until every queued chat save is on disk, and return the error of the last write if it failed.
/// Safe with the engine locked: the writer is idle again before it toasts a failure.
fn flush_chat_saves() -> Option<String> {
    let w = chat_writer();
    let mut s = w.state.lock().unwrap_or_else(|e| e.into_inner());
    while s.0.is_some() || s.1 {
        s = w.changed.wait(s).unwrap_or_else(|e| e.into_inner());
    }
    s.2.clone()
}

/// `chat_flush`: called as the app exits, since the writer is a detached thread the process does not wait for.
pub fn shutdown() -> Result<(), String> {
    flush_chat_saves().map_or(Ok(()), Err)
}

fn read_chat(data_dir: &Path, id: &str) -> Result<Value, String> {
    // Shown as-is in the app: say what failed and what to do next.
    let text = std::fs::read_to_string(chat_path(data_dir, id)?)
        .map_err(|_| "Unable to open this chat: its file was moved or deleted. Pick another chat or start a new one.".to_string())?;
    serde_json::from_str(&text).map_err(|_| "Unable to open this chat: its file is damaged. Pick another chat or start a new one.".to_string())
}

/// Saved chats, newest first. Deletes files untouched for `CHAT_KEEP_DAYS`.
fn list_chats(data_dir: &Path) -> Vec<Value> {
    let cutoff = SystemTime::now() - Duration::from_secs(CHAT_KEEP_DAYS * 86_400);
    let mut chats = Vec::new();
    for entry in std::fs::read_dir(data_dir.join("sessions")).into_iter().flatten().flatten() {
        let path = entry.path();
        if entry.metadata().and_then(|m| m.modified()).is_ok_and(|t| t < cutoff) {
            let _ = std::fs::remove_file(&path);
            continue;
        }
        let Some(id) = path.file_stem().and_then(|s| s.to_str()).filter(|_| path.extension().is_some_and(|x| x == "json")) else { continue };
        // ponytail: parses every file for its title; keep an index file if the history gets large.
        let Some(v) = std::fs::read_to_string(&path).ok().and_then(|t| serde_json::from_str::<Value>(&t).ok()) else { continue };
        chats.push(json!({"id": id, "title": v["title"], "updatedAt": v["updatedAt"]}));
    }
    chats.sort_by_key(|c| std::cmp::Reverse(c["updatedAt"].as_u64().unwrap_or(0)));
    chats
}

/// `chat_list`: the saved chats plus the id of the current one.
pub fn chats() -> Value {
    flush_chat_saves();
    let (dir, current) = {
        let e = engine();
        (e.data_dir.clone(), e.agent.chat.as_ref().map(|c| c.id.clone()))
    };
    json!({"current": current, "chats": list_chats(&dir)})
}

/// Serializes load, delete and clear: each touches the chat files and the current chat in separate steps, so one must not interleave with another.
/// Always taken before the engine lock.
fn chat_ops() -> std::sync::MutexGuard<'static, ()> {
    static OPS: Mutex<()> = Mutex::new(());
    OPS.lock().unwrap_or_else(|e| e.into_inner())
}

/// `chat_load`: make a saved chat the current conversation; returns its events for the UI to replay.
pub fn load_chat(id: &str) -> Result<Value, String> {
    let _ops = chat_ops();
    flush_chat_saves();
    let mut e = engine();
    if e.agent.running {
        return Err("Stop the agent before opening another chat.".into());
    }
    let saved = read_chat(&e.data_dir, id)?;
    e.agent.restore(id, &saved);
    Ok(json!({"id": id, "title": saved["title"], "events": saved["events"]}))
}

/// `chat_delete`: remove a saved chat. Removing the current one starts a new conversation.
pub fn delete_chat(id: &str) -> Result<(), String> {
    let _ops = chat_ops();
    let path = {
        let mut e = engine();
        let path = chat_path(&e.data_dir, id)?;
        if e.agent.chat.as_ref().is_some_and(|c| c.id == id) {
            if e.agent.running {
                return Err("Stop the agent before removing the running chat.".into());
            }
            e.agent.clear();
        }
        path
    };
    // Flushed after the lock drops: the chat is no longer current, so no later snapshot can bring the file back.
    flush_chat_saves();
    match std::fs::remove_file(path) {
        Err(err) if err.kind() != std::io::ErrorKind::NotFound => Err(err.to_string()),
        _ => Ok(()),
    }
}

/// `chat_clear`: delete every saved chat but a running one. True when the current chat went too.
pub fn clear_chats() -> Result<bool, String> {
    let _ops = chat_ops();
    let (dir, running, keep) = {
        let mut e = engine();
        let running = e.agent.running;
        // Both the file and the temp file its writer renames over it: removing the temp file fails the next save.
        let keep = running.then(|| e.agent.chat.as_ref().map(|c| [format!("{}.json", c.id), format!("{}.json.tmp", c.id)])).flatten();
        if !running {
            e.agent.clear();
        }
        (e.data_dir.clone(), running, keep)
    };
    // Flushed after the lock drops: only a running chat can still queue snapshots, and its file is kept.
    flush_chat_saves();
    for entry in std::fs::read_dir(dir.join("sessions")).into_iter().flatten().flatten() {
        if !keep.as_ref().is_some_and(|k| k.iter().any(|f| entry.file_name().to_str() == Some(f.as_str()))) {
            std::fs::remove_file(entry.path()).map_err(|err| err.to_string())?;
        }
    }
    Ok(!running)
}

/// `spawn_agents`: start child agents that each design inside their container nodes.
/// Returns immediately; children report progress through their own events.
pub fn spawn_children(parent: &str, config: &Value) -> crate::engine::ToolOut {
    let Some(entries) = config["config"].as_array().filter(|a| !a.is_empty()) else {
        return crate::engine::ToolOut::err("spawn_agents needs `config`: [{prompt, containerNodes: [ids]}]");
    };
    if entries.len() > MAX_CHILDREN {
        return crate::engine::ToolOut::err(format!("at most {MAX_CHILDREN} agents can be spawned at once"));
    }
    let mut started = Vec::new();
    for entry in entries {
        let ids: Vec<String> = entry["containerNodes"].as_array().into_iter().flatten().filter_map(|v| v.as_str().map(str::to_string)).collect();
        if ids.is_empty() {
            return crate::engine::ToolOut::err("each spawn_agents entry needs at least one container node id");
        }
        let (child, context, total) = {
            let mut e = engine();
            if let Some(missing) = ids.iter().find(|id| e.doc.find(id).is_none()) {
                return crate::engine::ToolOut::err(format!("container node not found: {missing}"));
            }
            let child = e.agent.new_profile(Some(parent.to_string()));
            let nodes: Vec<Value> = ids.iter().filter_map(|id| e.doc.find(id).cloned()).collect();
            let context = truncate(&serde_json::to_string(&nodes).unwrap_or_default(), 12000);
            let vars = truncate(&e.doc.variables().to_string(), 4000);
            (child, format!("{context}\n\nDocument variables: {vars}"), e.agent.parallel.0)
        };
        let task = entry["prompt"].as_str().unwrap_or("").trim().to_string();
        let prompt = if engine().agent.parallel.1 == "side" || task.is_empty() {
            let k = started.len() + 2;
            format!(
                "You are designer {k} of {total} working side by side on the same brief; each designer has their own frame.\n\
                 Design your own take inside the frame `{}` only. Prefix the frame's name with \"{k}.\" if it is not already, \
                 never edit nodes outside it, and do not look at the other frames.\n\nThe frame (already read for you, no need to Get it again):\n{context}",
                ids[0]
            )
        } else {
            format!(
                "Design the following task inside the node(s) {}. Only edit inside them.\nTask: {task}\n\n\
                 The containers as they are now (already read for you, no need to Get them again):\n{context}",
                ids.iter().map(|i| format!("`{i}`")).collect::<Vec<_>>().join(", ")
            )
        };
        started.push(format!("{} → {}", child.name, ids.join(", ")));
        start_thread(child, prompt, Some(parent.to_string()));
    }
    crate::engine::ToolOut::ok(format!(
        "Started {} designers: {}. They are working in their containers now. Continue with your own part; do not verify or edit their work.",
        started.len(),
        started.join("; ")
    ))
}

fn api_tools(child: bool) -> Value {
    let parallel = engine().agent.parallel.0 > 1;
    let mut tools: Vec<Value> = tool_specs()
        .as_array()
        .unwrap()
        .iter()
        .filter(|t| !child || t["name"] != "get_editor_state")
        .map(|t| {
            let mut tool = json!({"name": t["name"], "description": t["description"], "input_schema": t["inputSchema"]});
            if t["name"] == "execute" {
                // Stream the snippet as it is written so the canvas updates live.
                tool["eager_input_streaming"] = json!(true);
            }
            tool
        })
        .collect();
    if parallel && !child {
        tools.push(spawn_spec());
    }
    Value::Array(tools)
}

pub fn spawn_spec() -> Value {
    json!({"name": "spawn_agents", "description": "Start parallel designer agents. Each entry gets a self-contained task and the ids of existing container frames it may edit. Create the containers first. Returns immediately.",
           "input_schema": {"type": "object", "properties": {"config": {"type": "array", "items": {"type": "object", "properties": {
               "prompt": {"type": "string"}, "containerNodes": {"type": "array", "items": {"type": "string"}}}, "required": ["prompt", "containerNodes"]}}},
           "required": ["config"]}})
}

fn run_api(p: &Profile, prompt: &str, child: bool) -> Result<Value, String> {
    let (key, model, effort, cancel, state, fast) = {
        let mut e = engine();
        let key = e.api_key()
            .ok_or("No Anthropic API key. Add one in Settings, set ANTHROPIC_API_KEY, or switch the backend to Claude Code.")?;
        let model = e.setting("agent_model").unwrap_or_else(|| DEFAULT_MODEL.into());
        let effort = e.setting("agent_effort").unwrap_or_else(|| "high".into());
        let fast = fast_for(Some(&model), e.settings.get("agent_fast") == Some(&json!(true)));
        (key, model, effort, e.agent.cancel.clone(), e.editor_state(), fast)
    };
    let mut messages = if child { Vec::new() } else { engine().agent.messages.clone() };
    let user = if child { prompt.to_string() } else { format!("{prompt}\n\nThe result of `get_editor_state` (fresh; call it again only if the document changed):\n{state}") };
    messages.push(json!({"role": "user", "content": [{"type": "text", "text": user}]}));

    let modern = ["claude-opus-5-5", "claude-fable-5", "claude-sonnet-5-5"].iter().any(|m| model.starts_with(m));
    let client = crate::claude::Client::new(key);
    let system = json!([{"type": "text", "text": format!("{SYSTEM_INTRO}{}", crate::guide::system_docs()), "cache_control": {"type": "ephemeral"}}]);
    let tools = api_tools(child);
    let mut usage = json!({"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "turns": 0});

    for _ in 0..MAX_TURNS {
        if cancel.load(Ordering::SeqCst) {
            break;
        }
        let mut body = json!({"model": model, "max_tokens": 32000, "system": system, "tools": tools, "messages": messages, "output_config": {"effort": effort}});
        let mut betas: Vec<&str> = Vec::new();
        if modern {
            body["fallbacks"] = json!("default");
            body["thinking"] = json!({"type": "adaptive", "display": "updates"});
            betas.extend(["server-side-fallback-2026-07-01", "thinking-display-updates-2026-08-18"]);
        }
        if fast {
            body["speed"] = json!("fast");
            // Opus 5.5 has fast mode generally; Opus 5 and 4.8 still need the research-preview header.
            if model != "claude-opus-5-5" {
                betas.push("fast-mode-2026-02-01");
            }
        }
        let beta_header = betas.join(",");
        let beta = (!betas.is_empty()).then_some(beta_header.as_str());
        let mut last_preview: HashMap<String, Instant> = HashMap::new();
        let mut last_progress = Instant::now();
        let stop_flag = cancel.clone();
        let resp = client.stream(body, beta, &|| stop_flag.load(Ordering::SeqCst), &mut |ev| match ev {
            crate::claude::Event::Text(t) => emit(p, "text_delta", json!({"text": t})),
            crate::claude::Event::Thinking(t) => emit(p, "thinking_delta", json!({"text": t})),
            crate::claude::Event::ToolStart { id, name } => emit(p, "tool_start", json!({"id": id, "name": name})),
            crate::claude::Event::ToolInput { id, name, json } => {
                if name != "execute" {
                    return;
                }
                let due = last_preview.get(id).is_none_or(|t| t.elapsed() >= STREAM_THROTTLE);
                if due {
                    if let Some(src) = crate::stream::partial_string(json, "input") {
                        engine().stream_update(id, &p.id, &src);
                        if last_progress.elapsed() >= Duration::from_millis(300) {
                            emit(p, "tool_progress", json!({"id": id, "lines": src.lines().count()}));
                            last_progress = Instant::now();
                        }
                    }
                    last_preview.insert(id.to_string(), Instant::now());
                }
            }
        });
        let resp = match resp {
            Ok(r) => r,
            Err(e) if e == "stopped" => break,
            Err(e) => return Err(e),
        };
        for (k, src) in [("input", "input_tokens"), ("output", "output_tokens"), ("cacheRead", "cache_read_input_tokens"), ("cacheWrite", "cache_creation_input_tokens")] {
            usage[k] = json!(usage[k].as_u64().unwrap_or(0) + resp["usage"][src].as_u64().unwrap_or(0));
        }
        usage["turns"] = json!(usage["turns"].as_u64().unwrap_or(0) + 1);
        let stop = resp["stop_reason"].as_str().unwrap_or("").to_string();
        let content = resp["content"].clone();
        let blocks = content.as_array().cloned().unwrap_or_default();
        emit(p, "message_end", json!({}));
        if stop == "refusal" {
            emit(p, "error", json!({"text": "Claude declined this request."}));
            break;
        }
        // Append unchanged so thinking blocks stay valid for the next request.
        messages.push(json!({"role": "assistant", "content": content}));
        let calls: Vec<&Value> = blocks.iter().filter(|b| b["type"] == "tool_use").collect();
        if stop == "max_tokens" && !calls.is_empty() {
            // A truncated tool input must not run.
            for b in &calls {
                engine().stream_end(b["id"].as_str().unwrap_or(""));
            }
            emit(p, "note", json!({"text": "The reply hit the output limit mid-tool-call; ask the agent to continue."}));
            break;
        }
        if stop != "tool_use" {
            break;
        }
        let mut results = Vec::new();
        for b in calls {
            let (id, name) = (b["id"].as_str().unwrap_or(""), b["name"].as_str().unwrap_or(""));
            let out = if let Some(raw) = b["input"]["__invalid_json"].as_str() {
                engine().stream_end(id);
                crate::engine::ToolOut::err(json!({"INVALID_JSON": raw}).to_string())
            } else if name == "spawn_agents" {
                spawn_children(&p.id, &b["input"])
            } else {
                let mut args = b["input"].clone();
                args["__stream"] = json!(id);
                args["__agent"] = json!(p.id);
                crate::engine::call_tool(name, &args)
            };
            // Outside the engine lock (the guard above is a temporary): screenshots rasterize here.
            let out = out.finish();
            emit(p, "tool_done", json!({"id": id, "name": name, "input": b["input"], "output": truncate(&out.text, 6000), "images": out.images.iter().take(3).collect::<Vec<_>>(), "error": out.error}));
            let mut parts = vec![json!({"type": "text", "text": out.text})];
            for img in &out.images {
                parts.push(json!({"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": img}}));
            }
            results.push(json!({"type": "tool_result", "tool_use_id": id, "content": parts, "is_error": out.error}));
        }
        messages.push(json!({"role": "user", "content": results}));
        if !child {
            engine().agent.messages = messages.clone();
        }
    }
    if !child {
        engine().agent.messages = messages;
    }
    Ok(usage)
}

fn truncate(s: &str, n: usize) -> String {
    if s.len() <= n {
        return s.to_string();
    }
    let mut cut = n;
    while !s.is_char_boundary(cut) {
        cut -= 1;
    }
    format!("{}…", &s[..cut])
}

/// MCP client config for one agent's Claude Code process (per-agent URL = attribution).
fn claude_mcp_config(agent: &str) -> Result<std::path::PathBuf, String> {
    let (port, dir) = {
        let e = engine();
        (e.mcp_port, e.data_dir.clone())
    };
    if port == 0 {
        return Err("the MCP server is not running".into());
    }
    let path = dir.join(format!("claude-code-mcp-{agent}.json"));
    let cfg = json!({"mcpServers": {"nalarupa": {"type": "http", "url": format!("http://127.0.0.1:{port}/mcp/{agent}")}}});
    std::fs::write(&path, cfg.to_string()).map_err(|e| e.to_string())?;
    Ok(path)
}

fn run_claude_code(p: &Profile, prompt: &str, child: bool) -> Result<Value, String> {
    let cfg = claude_mcp_config(&p.id)?;
    let (session, cancel, state) = {
        let mut e = engine();
        let state = if child { String::new() } else { e.editor_state() };
        (if child { None } else { e.agent.session.clone() }, e.agent.cancel.clone(), state)
    };
    let full_prompt = if child { prompt.to_string() } else { format!("{prompt}\n\nThe result of `get_editor_state` (fresh; call it again only if the document changed):\n{state}") };
    let (auto, model, fast) = {
        let e = engine();
        let model = e.setting("agent_model");
        let fast = fast_for(model.as_deref(), e.settings.get("agent_fast") == Some(&json!(true)));
        (e.settings.get("agent_auto_permissions") == Some(&json!(true)), model, fast)
    };
    let mut cmd = Command::new("claude");
    // The prompt goes in on stdin: as an argument a long message or selection overflows the OS limit.
    cmd.arg("-p")
        .args(["--output-format", "stream-json", "--verbose", "--include-partial-messages", "--strict-mcp-config"])
        .arg("--mcp-config").arg(&cfg)
        // Pre-allowed set: the canvas tools plus web search/fetch. Every other tool asks the
        // user through our MCP permission tool (a permission card).
        .args(["--allowedTools", "mcp__nalarupa,WebSearch,WebFetch"])
        .args(["--disallowedTools", "Task,Agent,AskUserQuestion"])
        .args(["--permission-prompt-tool", &format!("mcp__nalarupa__{PERMISSION_TOOL}")])
        .arg("--append-system-prompt")
        .arg(format!("{SYSTEM_INTRO}Your canvas tools are the `mcp__nalarupa__*` MCP tools. Read the guide with read_guide before your first execute."))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    if let Some(s) = &session {
        cmd.args(["--resume", s]);
    }
    if auto {
        cmd.args(["--permission-mode", "auto"]);
    }
    // Unset = the CLI's own default model. Fast mode is per session (Claude Code v2.1.205+).
    if let Some(m) = &model {
        cmd.args(["--model", m]);
    }
    if fast {
        cmd.args(["--settings", r#"{"fastMode":true}"#]);
    }
    let mut child_proc = cmd.spawn().map_err(|e| {
        if e.kind() == std::io::ErrorKind::NotFound {
            format!("could not start the `claude` CLI ({e}). Install Claude Code or switch the backend to the Claude API.")
        } else {
            format!("could not start the `claude` CLI ({e})")
        }
    })?;
    // A thread writes it so a prompt larger than the pipe buffer cannot block before output is read; dropping the handle sends EOF.
    let stdin_writer = child_proc.stdin.take().map(|mut stdin| {
        std::thread::spawn(move || std::io::Write::write_all(&mut stdin, full_prompt.as_bytes()))
    });
    let stdout = child_proc.stdout.take().unwrap();
    let mut stderr = child_proc.stderr.take().unwrap();
    let proc = Arc::new(std::sync::Mutex::new(child_proc));
    let finished = Arc::new(AtomicBool::new(false));
    let watcher = {
        let (proc, cancel, finished) = (proc.clone(), cancel.clone(), finished.clone());
        std::thread::spawn(move || {
            while !finished.load(Ordering::SeqCst) {
                if cancel.load(Ordering::SeqCst) {
                    let _ = proc.lock().unwrap_or_else(|e| e.into_inner()).kill();
                    break;
                }
                std::thread::sleep(Duration::from_millis(150));
            }
        })
    };
    let err_reader = std::thread::spawn(move || {
        let mut err = String::new();
        let _ = std::io::Read::read_to_string(&mut stderr, &mut err);
        err
    });

    // Per streamed message: content index → (tool id, tool name, partial input JSON).
    let mut tools: HashMap<u64, (String, String, String)> = HashMap::new();
    let mut inputs: HashMap<String, (String, Value)> = HashMap::new();
    let mut last_preview: HashMap<String, Instant> = HashMap::new();
    let mut usage = json!({});
    let mut reported = false;
    let mut textless_error = false;
    for line in BufReader::new(stdout).lines() {
        let Ok(line) = line else { break };
        let Ok(ev) = serde_json::from_str::<Value>(&line) else { continue };
        if !child {
            if let Some(id) = ev["session_id"].as_str() {
                engine().agent.session = Some(id.to_string());
            }
        }
        match ev["type"].as_str() {
            Some("stream_event") => {
                let se = &ev["event"];
                let idx = se["index"].as_u64().unwrap_or(0);
                match se["type"].as_str() {
                    Some("message_start") => tools.clear(),
                    Some("content_block_start") if se["content_block"]["type"] == "tool_use" => {
                        let id = se["content_block"]["id"].as_str().unwrap_or("").to_string();
                        let name = short_tool(se["content_block"]["name"].as_str().unwrap_or(""));
                        emit(p, "tool_start", json!({"id": id, "name": name}));
                        tools.insert(idx, (id, name, String::new()));
                    }
                    Some("content_block_delta") => {
                        let d = &se["delta"];
                        match d["type"].as_str() {
                            Some("text_delta") => emit(p, "text_delta", json!({"text": d["text"]})),
                            Some("thinking_delta") => emit(p, "thinking_delta", json!({"text": d["thinking"]})),
                            Some("input_json_delta") => {
                                if let Some((id, name, buf)) = tools.get_mut(&idx) {
                                    buf.push_str(d["partial_json"].as_str().unwrap_or(""));
                                    let due = last_preview.get(id.as_str()).is_none_or(|t| t.elapsed() >= STREAM_THROTTLE);
                                    if name == "execute" && due {
                                        if let Some(src) = crate::stream::partial_string(buf, "input") {
                                            engine().stream_update(id, &p.id, &src);
                                            emit(p, "tool_progress", json!({"id": id, "lines": src.lines().count()}));
                                        }
                                        last_preview.insert(id.clone(), Instant::now());
                                    }
                                }
                            }
                            _ => {}
                        }
                    }
                    Some("message_stop") => emit(p, "message_end", json!({})),
                    _ => {}
                }
            }
            Some("assistant") => {
                for b in ev["message"]["content"].as_array().into_iter().flatten() {
                    if b["type"] == "tool_use" {
                        let id = b["id"].as_str().unwrap_or("").to_string();
                        let name = short_tool(b["name"].as_str().unwrap_or(""));
                        if name == "TodoWrite" {
                            emit(p, "todos", json!({"todos": b["input"]["todos"]}));
                        }
                        inputs.insert(id, (name, b["input"].clone()));
                    }
                }
            }
            Some("user") => {
                for b in ev["message"]["content"].as_array().into_iter().flatten().filter(|b| b["type"] == "tool_result") {
                    let id = b["tool_use_id"].as_str().unwrap_or("").to_string();
                    let (name, input) = inputs.remove(&id).unwrap_or_default();
                    engine().stream_end(&id);
                    let (text, images) = tool_result_parts(&b["content"]);
                    emit(p, "tool_done", json!({"id": id, "name": name, "input": input, "output": truncate(&text, 6000), "images": images, "error": b["is_error"] == true}));
                }
            }
            Some("result") => {
                let u = &ev["usage"];
                usage = json!({"input": u["input_tokens"], "output": u["output_tokens"], "cacheRead": u["cache_read_input_tokens"],
                               "cacheWrite": u["cache_creation_input_tokens"], "turns": ev["num_turns"], "costUsd": ev["total_cost_usd"]});
                if ev["is_error"] == true {
                    // Without text the card would hide the real cause; leave that to the exit error (stderr).
                    match error_cause(&ev) {
                        Some(text) => {
                            emit(p, "error", json!({"text": text}));
                            reported = true;
                        }
                        None => textless_error = true,
                    }
                }
            }
            _ => {}
        }
    }
    finished.store(true, Ordering::SeqCst);
    let _ = watcher.join();
    let status = proc.lock().unwrap_or_else(|e| e.into_inner()).wait().map_err(|e| e.to_string())?;
    let err = err_reader.join().unwrap_or_default();
    let sent = stdin_writer.map_or(Ok(()), |w| w.join().unwrap_or_else(|_| Err(std::io::Error::other("writer thread panicked"))));
    if !cancel.load(Ordering::SeqCst) && !status.success() {
        return Err(exit_error(&status.to_string(), &err, reported));
    }
    // A clean exit after a cut-off prompt would otherwise pass as a finished run.
    if let (Err(e), false) = (sent, cancel.load(Ordering::SeqCst)) {
        return Err(format!("could not send the prompt to the `claude` CLI ({e})"));
    }
    if textless_error && !cancel.load(Ordering::SeqCst) {
        emit(p, "error", json!({"text": "Claude Code reported an error"}));
    }
    Ok(usage)
}

/// The cause an error `result` event carries: its `result` text, else its `errors` list; None when it has neither.
fn error_cause(ev: &Value) -> Option<String> {
    let result = ev["result"].as_str().map(str::trim).filter(|t| !t.is_empty());
    let errors: Vec<&str> = ev["errors"].as_array().into_iter().flatten().filter_map(Value::as_str).map(str::trim).filter(|t| !t.is_empty()).collect();
    result.map(str::to_string).or_else(|| (!errors.is_empty()).then(|| errors.join("\n")))
}

/// The failure for a non-zero exit. Empty when an error result already showed the cause, so
/// `start_thread` ends the run without a second card; never ends in a bare colon.
fn exit_error(status: &str, stderr: &str, reported: bool) -> String {
    let stderr = stderr.trim();
    if reported {
        String::new()
    } else if stderr.is_empty() {
        format!("claude exited with {status}")
    } else {
        format!("claude exited with {status}: {stderr}")
    }
}

fn short_tool(name: &str) -> String {
    name.trim_start_matches("mcp__nalarupa__").to_string()
}

fn tool_result_parts(content: &Value) -> (String, Vec<String>) {
    match content {
        Value::String(s) => (s.clone(), Vec::new()),
        Value::Array(items) => {
            let text = items.iter().filter_map(|i| i["text"].as_str()).collect::<Vec<_>>().join("\n");
            let images = items.iter().filter_map(|i| i["source"]["data"].as_str().or(i["data"].as_str()).map(str::to_string)).take(3).collect();
            (text, images)
        }
        _ => (String::new(), Vec::new()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn truncate_cuts_at_a_char_boundary() {
        assert_eq!(truncate("abc", 5), "abc");
        assert_eq!(truncate("abcdef", 3), "abc…");
        // Byte 4 is inside the second 3-byte char; a plain `String::truncate(4)` would panic.
        assert_eq!(truncate("日本語", 4), "日…");
        assert_eq!(truncate("日本語", 9), "日本語");
    }

    #[test]
    fn chat_session_roundtrips() {
        let dir = std::env::temp_dir().join(format!("nalarupa-test-chats-{:x}", crate::doc::random_u64()));
        let mut a = AgentState::default();
        let p = a.new_profile(None);
        a.main = Some(p.id.clone());
        a.session = Some("claude-code-session".into());
        a.messages = vec![json!({"role": "user", "content": [{"type": "text", "text": "Design a page"}]})];
        a.chat = Some(Chat { id: "abc123".into(), title: "Design a page".into(), events: Vec::new() });
        let ev = |kind: &str, text: &str| json!({"type": "agent", "kind": kind, "agent": p.json(), "text": text});
        assert!(a.record(&ev("user", "Design a page")));
        assert!(!a.record(&ev("text_delta", "Hel")));
        assert!(!a.record(&ev("tool_progress", "")));
        assert!(!a.record(&ev("text_delta", "lo")));
        assert!(a.record(&ev("done", "")));
        let helper = a.new_profile(Some(p.id.clone()));
        assert!(a.record(&json!({"type": "agent", "kind": "done", "agent": helper.json()})));
        save_chat(&dir, &a).unwrap();
        flush_chat_saves();

        let listed = list_chats(&dir);
        assert_eq!(listed.len(), 1);
        assert_eq!((listed[0]["id"].as_str(), listed[0]["title"].as_str()), (Some("abc123"), Some("Design a page")));

        let mut b = AgentState::default();
        b.restore("abc123", &read_chat(&dir, "abc123").unwrap());
        assert_eq!(b.session.as_deref(), Some("claude-code-session"));
        assert_eq!(b.messages, a.messages);
        assert_eq!(b.main, a.main);
        assert_eq!(b.profile(&p.id).map(|q| q.name.as_str()), Some(p.name.as_str()));
        assert_eq!(b.profile(&helper.id).map(|q| (q.name.as_str(), q.parent.as_deref())), Some((helper.name.as_str(), Some(p.id.as_str()))), "a restored chat keeps its helpers' names");
        let events = &b.chat.as_ref().unwrap().events;
        let kinds: Vec<&str> = events.iter().filter_map(|e| e["kind"].as_str()).collect();
        assert_eq!(kinds, ["user", "text_delta", "done", "done"]);
        assert_eq!(events[1]["text"], "Hello");
        assert!(read_chat(&dir, "../settings").is_err());
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn profile_names_are_unique_and_a_new_conversation_starts_empty() {
        let mut a = AgentState::default();
        let mut names: Vec<String> = (0..40).map(|_| a.new_profile(None).name).collect();
        names.sort();
        names.dedup();
        assert_eq!(names.len(), 40, "no two agents of one conversation share a name");
        let old: Vec<String> = a.profiles.iter().map(|p| p.id.clone()).collect();
        a.clear();
        assert!(a.profiles.is_empty(), "a new conversation forgets the old agents");
        let fresh = a.new_profile(None);
        assert!(!old.contains(&fresh.id), "a new conversation's agent never takes an old id (its script globals are kept)");
    }

    #[test]
    fn chat_title_is_one_line() {
        assert_eq!(chat_title("  Make it dark\nand add a footer "), "Make it dark and add a footer");
        assert_eq!(chat_title("Design a pricing page\n\nPlans:\r\n- Free\t- Pro"), "Design a pricing page Plans: - Free - Pro");
        assert_eq!(chat_title(&"word ".repeat(40)).chars().count(), 100);
    }

    #[test]
    fn error_cause_reads_result_then_errors() {
        assert_eq!(error_cause(&json!({"result": " Credit balance is too low "})).as_deref(), Some("Credit balance is too low"));
        assert_eq!(error_cause(&json!({"result": "", "errors": ["", "Not logged in", " Run /login "]})).as_deref(), Some("Not logged in\nRun /login"));
        assert_eq!(error_cause(&json!({"result": "  ", "errors": []})), None, "no cause: the exit error shows stderr instead");
        assert_eq!(error_cause(&json!({})), None);
    }

    #[test]
    fn exit_error_has_no_duplicate_or_empty_tail() {
        assert_eq!(exit_error("exit status: 1", "", true), "", "the error result already showed the cause");
        assert_eq!(exit_error("exit status: 1", "boom\n", true), "");
        assert_eq!(exit_error("exit status: 1", " \n", false), "claude exited with exit status: 1");
        assert_eq!(exit_error("exit status: 1", "boom\n", false), "claude exited with exit status: 1: boom");
    }

    /// Stop drops the queued prompts and says so, so the chat does not keep promising to send them.
    #[test]
    fn stop_announces_dropped_queue() {
        let kinds = || engine().events.iter().filter(|e| e["type"] == "agent").filter_map(|e| e["kind"].as_str().map(str::to_string)).collect::<Vec<_>>();
        let dir = std::env::temp_dir().join(format!("nalarupa-test-stop-{:x}", crate::doc::random_u64()));
        let saved = |kind: &str| {
            flush_chat_saves();
            read_chat(&dir, "5709e5").unwrap()["events"].as_array().unwrap().iter().filter(|e| e["kind"] == kind).count()
        };
        let data_dir = {
            let mut e = engine();
            let old = std::mem::replace(&mut e.data_dir, dir.clone());
            e.agent.running = true;
            e.agent.chat = Some(Chat { id: "5709e5".into(), title: "stop".into(), events: Vec::new() });
            old
        };
        send("also add a footer".into(), 1, "split", 1, "").unwrap();
        assert_eq!(engine().agent.queue.len(), 1);
        stop();
        assert!(engine().agent.queue.is_empty());
        assert_eq!(kinds().iter().filter(|k| *k == "dequeued").count(), 1);
        assert_eq!(saved("dequeued"), 1, "the saved chat keeps the announcement for replay");
        stop();
        assert_eq!(kinds().iter().filter(|k| *k == "dequeued").count(), 1, "nothing queued, nothing announced");
        // A prompt that arrives while the stopped agents are still shutting down is dropped and announced too.
        send("one more thing".into(), 1, "split", 1, "").unwrap();
        drop_queue(&mut engine());
        assert!(engine().agent.queue.is_empty());
        assert_eq!(saved("dequeued"), 2);
        let mut e = engine();
        e.agent.running = false;
        e.agent.chat = None;
        e.data_dir = data_dir;
        let _ = std::fs::remove_dir_all(&dir);
    }

    /// A run that ends before the call that clears `placeholder` must not leave frames "Generating", except
    /// the one an AI Pen is still drawing into.
    #[test]
    fn run_end_clears_stale_placeholders() {
        let mut e = engine();
        let old = e.doc.root.clone();
        let frame = |name: &str| json!({"type": "frame", "name": name, "x": 0, "y": 0, "width": 100, "height": 100, "placeholder": true});
        let (a, drawing) = (e.doc.insert("document", frame("A"), None).unwrap(), e.doc.insert("document", frame("B"), None).unwrap());
        let nested = e.doc.insert(&a, frame("Nested"), None).unwrap();
        e.agent.pens.push(drawing.clone());
        clear_generating(&mut e);
        let flag = |e: &Engine, id: &str| e.doc.find(id).unwrap()["placeholder"] == true;
        assert!(!flag(&e, &a) && !flag(&e, &nested), "stale flags are cleared, nested frames too");
        assert!(flag(&e, &drawing), "a frame an AI Pen draws into keeps its flag");
        e.agent.pens.clear();
        e.doc.root = old;
    }

    /// Auto-allowed paths: the workspace for file tools, skills for Read; `..` cannot escape.
    #[test]
    fn permission_paths() {
        let (ws, doc, skills) = (Path::new("C:/work/site"), Path::new("C:/work/site/app.pen"), Path::new("C:/home/.claude/skills"));
        let ok = |f: &str, tool: &str| path_allowed(Path::new(f), tool, Some(ws), Some(doc), Some(skills));
        assert!(ok("C:/work/site/img/hero.png", "Write"));
        assert!(!ok("C:/work/site/../secrets.txt", "Read"));
        assert!(!ok("C:/work/site2/a.txt", "Read"));
        assert!(!ok("C:/work/site/app.pen", "Write") && ok("C:/work/site/app.pen", "Read"));
        assert!(ok("C:/home/.claude/skills/x/SKILL.md", "Read") && !ok("C:/home/.claude/skills/x/SKILL.md", "Edit"));
        assert!(!path_allowed(Path::new("C:/work/site/a.txt"), "Write", None, None, Some(skills)), "no document folder, nothing allowed");
    }
}
