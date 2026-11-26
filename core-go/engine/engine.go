// Package engine is the editor state shared by the UI, the MCP server and the built-in agent (engine.rs): the
// open document with its libraries, fonts, assets and canvas renderer, the undo history, the selection, the
// event queue, the settings and the data folder.
//
// Concurrency mirrors Rust's process-wide `engine()` mutex: an Engine is guarded by its own lock (Lock and
// Unlock), and every method expects the caller to hold it. Font and asset downloads run on their own goroutines
// and take the lock to deliver their result (FontReady, AssetReady); every goroutine the engine starts recovers
// a panic, as Rust's thread boundaries do. A user edit also takes the edit gate (EditGate), which an `execute`
// call holds while its script runs.
//
// Floating-point products get explicit conversions so they are never fused into a multiply-add.
package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/history"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/render"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

const (
	// undoLimit is how many undo steps are kept.
	undoLimit = 100
	// eventLimit: past it the oldest eventDrop events are dropped (no UI is polling).
	eventLimit = 2000
	eventDrop  = 1000
)

// SwitchBusy refuses a document switch while the agent or an AI Pen still edits the open document. Home shows
// any refusal starting "Stop the agent" as is.
const SwitchBusy = "Stop the agent or the AI Pen before opening another document."

// SwitchDirty: an external agent has no Save / Don't save prompt, so it must not replace unsaved work.
const SwitchDirty = "The open document has unsaved changes; ask the user to save it first."

// ScriptState is what `execute` keeps between calls (script.rs `ScriptState`): each agent's globals and the
// snippets of failed calls by editId. Opening or creating a document forgets it.
type ScriptState struct {
	Globals map[string]*jsonv.Object
	Failed  map[string]string
	// edit is the `agent_edit` event the last finished call emitted, for endExecute.
	edit *jsonv.Object
	// live holds the JS state of the `execute` calls still streaming, by stream key.
	live map[string]*script.Live
}

// ToolOut is a tool call's answer: its text, base64 PNG screenshots and whether it failed. An `execute` answer
// also holds the screenshots, icons and exports still to finish: call Finish without holding the engine lock.
type ToolOut struct {
	Text   string
	Images []string
	Error  bool
	// Audit holds a lightweight snapshot of the committed document and screen layout boxes captured under EditGate,
	// allowing post-execute design auditing to run completely off-lock without racing concurrent MCP edits.
	Audit *AuditSnapshot
	// pending is what Finish still has to do.
	pending pendingOut
}

func ok(text string) ToolOut  { return ToolOut{Text: text} }
func bad(text string) ToolOut { return ToolOut{Text: text, Error: true} }

// Engine is the editor state. Lock it around every method call.
type Engine struct {
	mu sync.Mutex
	// gate is the edit gate (EditGate).
	gate sync.Mutex

	Doc      *doc.Doc
	Libs     []resolve.Lib
	Fonts    *text.Fonts
	Assets   *svg.Assets
	Renderer render.Renderer
	// ids is where the open document's new ids come from (every document the engine holds draws from it).
	ids *doc.IDSource
	// Version goes up on every change the canvas must show; Saved is the version last saved.
	Version, Saved uint64
	synced         uint64
	// scriptsSeen is script.Finished() at the last ExpireSlides.
	scriptsSeen uint64
	// savedRoot is the document as last saved, created or opened: undoing or redoing back to it is clean again.
	savedRoot *jsonv.Object
	// Each step turns the current document into the one before (undo) or after (redo) it.
	undo, redo []history.Patch
	Selection  []string
	Events     []jsonv.Value
	Script     ScriptState
	DataDir    string
	Settings   *jsonv.Object
	MCPPort    uint16
	Agent      AgentState
	// DragBefore is the document snapshot taken when a drag starts (nil: no drag); committed as one undo
	// step on release. DragClean is whether the document was clean then.
	DragBefore *jsonv.Object
	DragClean  bool
	// Streams are the `execute` calls the model is still writing.
	Streams []*Stream
	// Preview is the document as displayed while streams are active: the document plus every stream's complete
	// statements; nil shows Doc.
	Preview *doc.Doc
	// Executing is true while JavaScript runs; layout queries then read the document being edited.
	Executing bool
	// Viewport is the visible canvas area in document units (x, y, w, h) the UI reported; nil when none.
	Viewport *[4]float64
	// ReduceMotion: the OS asks for reduced motion, so agent inserts appear in place.
	ReduceMotion bool
	// coalesce is the key of the last undo step while it may absorb the next edit with the same key.
	coalesce    string
	hasCoalesce bool
	// slid holds the nodes that already slid in: a streamed insert committed again, undo/redo or a preview
	// rebuild never replays the animation.
	slid map[string]bool
	// Inflight holds the pending image urls whose download is still running; opening a file keeps them.
	Inflight map[string]bool
	// Hooks are the built-in agent's parts of an `execute` call (phase 9); the zero value is inert.
	Hooks AgentHooks
	// ImageBase is where `Generate("ai")` and `Generate("stock")` download their placeholder photos from (tests
	// point it at a local server); the client is Assets.Client.
	ImageBase string
	// ClipSession marks the node clipboard this engine writes (editor.rs `session`, process-wide in Rust): drawn
	// from the id generator on first use, "" until then.
	ClipSession      string
	hasSlidesAtomic  atomic.Bool
	versionAtomic    atomic.Uint64
	slideReleaseSeq  atomic.Uint64
	// ProgressiveSlides enables staggered element-by-element slide choreography for burst and nested inserts in the desktop UI.
	ProgressiveSlides bool
	staggerLast       time.Time
	staggerSeq        int

	pendingModifyID    string
	pendingModifyTree  *usvg.Tree
	pendingModifyClips []*usvg.Tree
	pendingModifyHash  uint64
	pendingModifyOrg   [2]float64
	pendingModifyHasO  bool
	commitSkipEdits   map[string]bool
	activeStream      *Stream
	execAddedPreTop   map[string]bool

	cursorMu         sync.Mutex
	pendingReleaseID []string
	pendingHoldID    []string
	currentExecAgent string
}

// HasSlidesFast reports the last synced/updated slide state atomically without acquiring the engine lock.
func (e *Engine) HasSlidesFast() bool { return e.hasSlidesAtomic.Load() }

// VersionFast reports the latest committed engine document/preview version atomically without acquiring the engine lock.
func (e *Engine) VersionFast() uint64 { return e.versionAtomic.Load() }

// SlideReleaseSeqFast reports the monotonic counter of cursor-triggered slide/edit releases without acquiring the engine lock.
func (e *Engine) SlideReleaseSeqFast() uint64 { return e.slideReleaseSeq.Load() }

// Lock takes the engine lock.
func (e *Engine) Lock() { e.mu.Lock() }

// TryLock attempts to take the engine lock without blocking.
func (e *Engine) TryLock() bool { return e.mu.TryLock() }

// Unlock releases the engine lock and publishes the latest Version atomically.
func (e *Engine) Unlock() {
	e.versionAtomic.Store(e.Version)
	e.mu.Unlock()
}

// EditGate takes the edit gate: a user edit waits here while a script runs (the script rolls back by restoring
// its snapshot, which would also throw away an edit made in between). Call the returned function to release it.
// Take it before the engine lock.
func (e *Engine) EditGate() func() {
	e.gate.Lock()
	return e.gate.Unlock
}

// Locked runs f under the engine lock, releasing it even when f panics.
func (e *Engine) Locked(f func()) {
	e.mu.Lock()
	defer e.Unlock()
	f()
}

// guard runs f on a goroutine the engine started, under the lock; a panic is recovered (state stays usable).
func (e *Engine) guard(f func()) {
	defer func() { recover() }()
	e.Locked(f)
}

// DataDir is the data folder: NALARUPA_DATA, else Nalarupa in the platform's local data folder (Rust's
// `dirs::data_local_dir`), else in the temp folder.
func DataDir() string {
	if d := os.Getenv("NALARUPA_DATA"); d != "" {
		return d
	}
	base := ""
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
	case "darwin":
		if home != "" {
			base = filepath.Join(home, "Library", "Application Support")
		}
	default:
		if x := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(x) {
			base = x
		} else if home != "" {
			base = filepath.Join(home, ".local", "share")
		}
	}
	if base == "" {
		base = os.TempDir()
	}
	return doc.JoinPath(base, "Nalarupa")
}

var (
	global     *Engine
	globalOnce sync.Once
)

// Global is the process-wide engine, created on first use in DataDir.
func Global() *Engine {
	globalOnce.Do(func() { global = New(DataDir()) })
	return global
}

// New is an engine on the blank template with the settings in dataDir (created if missing). Font and asset
// downloads deliver their results under the engine lock.
func New(dataDir string) *Engine {
	os.MkdirAll(dataDir, 0o777)
	settings := jsonv.NewObject()
	if b, err := os.ReadFile(doc.JoinPath(dataDir, "settings.json")); err == nil {
		if v, err := jsonv.Parse(b); err == nil {
			if o := jsonv.Obj(v); o != nil {
				settings = o
			}
		}
	}
	e := &Engine{
		Fonts:     text.New(doc.JoinPath(dataDir, "fonts")),
		Assets:    svg.NewAssets(doc.JoinPath(dataDir, "cache")),
		ids:       doc.NewIDSource(uint64(time.Now().UnixNano())),
		Version:   1,
		Saved:     1,
		DataDir:   dataDir,
		Settings:  settings,
		slid:      map[string]bool{},
		Inflight:  map[string]bool{},
		ImageBase: "https://picsum.photos",
		Agent:     AgentState{Cancel: NewFlag()},
	}
	e.Fonts.OnReady = func(family string, weight uint16, italic bool) {
		e.guard(func() { e.FontReady(family, weight, italic) })
	}
	e.Assets.OnReady = func(key string, ok bool) {
		e.guard(func() { e.AssetReady(key, ok) })
	}
	e.LoadTemplate("blank")
	e.Saved = e.Version
	e.versionAtomic.Store(e.Version)
	return e
}

// Offline stops font and asset downloads.
func (e *Engine) Offline() {
	e.Fonts.Online = false
	e.Assets.Online = false
}

// IDs is the generator the open document's new ids are drawn from (Rust's thread-local `random_u64`).
func (e *Engine) IDs() *doc.IDSource { return e.ids }

// Seed pins the ids new nodes take from now on (the same seed gives the same ids as Rust's `doc::reseed`).
func (e *Engine) Seed(seed uint64) { e.ids.Reseed(seed) }

// setDoc makes d the open document, drawing its new ids from the engine's generator.
func (e *Engine) setDoc(d *doc.Doc) {
	d.IDs = e.ids
	e.Doc = d
}

// Ctx is the canvas context of the open document.
func (e *Engine) Ctx() *render.Ctx {
	return &render.Ctx{Doc: e.Doc, Libs: e.Libs, Fonts: e.Fonts, Assets: e.Assets}
}

// Dirty reports unsaved changes.
func (e *Engine) Dirty() bool { return e.Version != e.Saved }

// Title is the document's file name without extension, or "Untitled".
func (e *Engine) Title() string { return e.Doc.Title() }

// SwitchBusyNow reports that an agent run, an `execute` or an AI Pen that outlives its run still edits the
// open document, so it must not be replaced (see SwitchBusy).
func (e *Engine) SwitchBusyNow() bool {
	return e.Agent.Running || e.Executing || len(e.Agent.Pens) > 0
}

// Emit queues an event for the UI's next poll; past eventLimit the oldest are dropped.
func (e *Engine) Emit(ev jsonv.Value) {
	// Agent events also build the saved chat; it is written when a message or turn ends.
	if jsonv.StrIs(jsonv.Get(ev, "type"), "agent") && e.Agent.Record(ev) && e.Hooks.SaveChat != nil {
		if err := e.Hooks.SaveChat(); err != nil {
			e.Events = append(e.Events, Object("type", "toast", "text", "Could not save the chat: "+err.Error()))
		}
	}
	e.Events = append(e.Events, ev)
	if len(e.Events) > eventLimit {
		e.Events = append([]jsonv.Value(nil), e.Events[eventDrop:]...)
	}
}

// TakeEvents returns the queued events and empties the queue.
func (e *Engine) TakeEvents() []jsonv.Value {
	out := e.Events
	e.Events = nil
	return out
}

// SelectTopLevel is the ids of the top-level nodes.
func (e *Engine) SelectTopLevel() []string {
	var out []string
	for _, n := range e.Doc.Children() {
		if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok {
			out = append(out, id)
		}
	}
	return out
}

// InsertRoot inserts node at the top level as one undo step.
func (e *Engine) InsertRoot(node jsonv.Value) (string, error) {
	var id string
	err := e.Edit(func(d *doc.Doc) (err error) {
		id, err = d.Insert(doc.Root, node, -1)
		return err
	})
	return id, err
}
