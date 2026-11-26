// Package agent is the built-in design agent (agent.rs and claude_code.rs):
//   - every agent has a profile (name and color) that the canvas cursor and the chat use;
//   - the `api` and `provider` backends stream the model through package model, with a manual tool loop;
//   - the `claude-code` backend runs the user's `claude` CLI with partial-message streaming, wired to the MCP
//     server through a per-agent URL (`/mcp/<agent-id>`);
//   - `execute` input is fed to the engine while it streams, so designs appear statement by statement before the
//     call completes;
//   - `spawn_agents` starts parallel child agents that each own container frames.
//
// The UI polls the events emitted here. Every function takes the engine it works on and locks it itself: call
// none of them with the engine lock held, except the hooks Attach installs. A run is a goroutine (Rust: a thread)
// that recovers a panic as a failed run.
package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/guide"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/model"
)

// DefaultModel is the model of the Claude API backend when none is chosen.
const DefaultModel = model.DefaultModel

// FastModels are the models with a fast output mode (Claude Code `fastMode`, API `speed: "fast"`).
var FastModels = []string{"claude-fable-5-2", "claude-opus-5-5", "claude-opus-5", "claude-opus-4-8", "fable", "opus"}

// fastFor: fast mode only where the model has it; m is a full id ("" for the CLI default).
func fastFor(m string, fast bool) bool {
	if !fast || m == "" {
		return false
	}
	for _, f := range FastModels {
		if f == m {
			return true
		}
	}
	return false
}

const (
	maxTurns    = 80
	maxChildren = 5
	// streamThrottle: the streamed snippet is re-run at most this often while the model is writing it.
	streamThrottle = 90 * time.Millisecond
	// progressThrottle: `tool_progress` of an API run at most this often.
	progressThrottle = 300 * time.Millisecond
)

// Attach installs the agent's engine hooks: Stop reaching a running built-in agent's `execute`, the frames its
// calls flag `placeholder`, the AI Pens they start, saving the chat when an agent event ends a message or a turn,
// and the MCP server `init` starts.
func Attach(e *engine.Engine) {
	e.Locked(func() {
		e.Hooks.StartPen = func(agent string, stops uint64, frame, prompt string, w, h float64) {
			// Its own flag, set by Stop even after a later turn replaced the run's. It starts set when a Stop came
			// while this script ran or its run is being stopped; an earlier Stop's flag does not stop a later
			// call's pen.
			cancel := engine.NewFlag()
			if StoppedSince(e, stops, agent) {
				cancel.Set()
			}
			e.Agent.Pens = append(e.Agent.Pens, engine.Pen{Frame: frame, Cancel: cancel})
			startPen(e, frame, prompt, w, h, cancel)
		}
		e.Hooks.StartMCP = func() (uint16, error) { return StartMCP(e) }
		e.Hooks.Stop = func(agent string) <-chan struct{} {
			// Stop reaches a running built-in agent only; anyone else would see a stale flag.
			if _, ok := e.Agent.Profile(agent); ok && e.Agent.Running {
				return e.Agent.Cancel.Done()
			}
			return nil
		}
		e.Hooks.NoteFlagged = func(agent string, before *jsonv.Object) { NoteFlagged(e, agent, before) }
		e.Hooks.SaveChat = func() error { return saveChat(e) }
	})
}

// emit sends an agent event (chat and canvas) carrying the agent's identity, so the UI can color it.
func emit(e *engine.Engine, p engine.Profile, kind string, extra *jsonv.Object) {
	ev := engine.Object("type", "agent", "kind", kind, "agent", p.JSON())
	if extra != nil {
		extra.Each(func(k string, v jsonv.Value) { ev.Set(k, v) })
	}
	e.Locked(func() { e.Emit(ev) })
}

// Backend is the agent backend the next run uses.
func Backend(e *engine.Engine) string {
	var b string
	e.Locked(func() { b = e.Backend() })
	return b
}

const systemIntro = "You are a design agent inside Nalarupa, an infinite-canvas design editor. You design directly on the user's canvas " +
	"(web pages, app screens, dashboards, slide decks, social graphics) by calling the `execute` tool; every statement you write appears on the canvas while you write it. " +
	"Keep chat replies short: for a new design, your first chat message, before any tool call, is the Framing / Direction / Why message (see Designing; a main agent's prompt holds the editor state with an existing style's variables, fonts and components, so it posts with no look first; a spawned designer reads the style it extends, then posts); for any other request, one sentence on what you will do. " +
	"When the work is done, summarize it in one or two sentences. " +
	"The guide below is the source of truth for the document format and the execute API.\n\n"

// Instructions are the system instructions a main agent gets on backend ("api" or "claude-code").
func Instructions(backend string) string {
	if backend == "claude-code" {
		return systemIntro + "Your canvas tools are the `mcp__nalarupa__*` MCP tools. Read the guide with read_guide before your first execute. Once the Framing / Direction / Why message is posted, track steps 5 onward in your todo list.\n\n" + guide.Designing
	}
	return systemIntro + guide.SystemDocs()
}

// MaxDesigns is the most designs "Let it cook" (iteration mode) makes: with more than one, the agent keeps going
// after a design is done, one variation per finished turn.
const MaxDesigns = 6

const (
	firstDesign = "This is the first of several design variations. Unless the user asked for a flow or several screens, design one focused screen; " +
		"for a broad product idea that is the main screen people land on first, the one that sets the visual and conceptual tone. Keep the scope tight: " +
		"later iterations explore other directions for exactly this scope."
	nextLayout = "Now design another variation of this page. Work out, as a strong product designer would, what could inspire it and give it more depth; " +
		"draw on what you already know instead of browsing the web, and let those insights shape the result. Make it a clearly different layout, placed next to " +
		"the earlier designs. Important: keep the first design's design direction (palette, type, spacing, signature move) and change only the layout."
	nextStyle = "Now design another variation of this page. Work out, as a strong product designer would, what could inspire it and give it more depth; " +
		"draw on what you already know instead of browsing the web, and let those insights shape the result. Placed next to the earlier designs. Important: give it a new " +
		"design direction, apart from every earlier variation on at least two of the variation axes (Designing); a recolor is not a variation."
	iterating = "Iterating more on the design…"
)

// Send is `agent_send`: start a turn. While agents are busy the prompt is queued and sent when they finish.
// designs > 1 turns on "Let it cook" with iterate "layout" or "style".
func Send(e *engine.Engine, prompt string, parallel uint64, mode string, designs uint64, iterate string) error {
	queued := false
	e.Locked(func() {
		if e.Agent.Running {
			e.Agent.Queue = append(e.Agent.Queue, prompt)
			// Several queued prompts run as one turn: the latest one's options win.
			e.Agent.QueueOpts = engine.QueueOpts{Parallel: parallel, Mode: mode, Designs: designs, Iterate: iterate}
			e.Emit(engine.Object("type", "agent", "kind", "queued", "text", prompt))
			queued = true
			return
		}
		// Reserve the turn under the same lock before releasing it for skill discovery so concurrent Sends queue cleanly.
		e.Agent.Running = true
		e.Agent.Cancel = engine.NewFlag()
	})
	if !queued {
		begin(e, prompt, engine.QueueOpts{Parallel: parallel, Mode: mode, Designs: designs, Iterate: iterate}, true)
	}
	return nil
}

func clampU(v, lo, hi uint64) int { return int(max(lo, min(v, hi))) }

// begin starts a user turn (either immediately from Send with Running reserved, or dequeued after a finished run).
// Slash skills are always expanded for user prompts, including queued ones.
func begin(e *engine.Engine, prompt string, o engine.QueueOpts, checkCancel bool) {
	var docPath string
	e.Locked(func() {
		if e.Doc != nil {
			docPath = e.Doc.Path
		}
		e.Agent.Parallel = clampU(o.Parallel, 1, maxChildren+1)
		e.Agent.Mode = "split"
		if o.Mode == "side" {
			e.Agent.Mode = "side"
		}
		e.Agent.Cook = clampU(o.Designs, 1, MaxDesigns)
		e.Agent.Iterate = "layout"
		if o.Iterate == "style" {
			e.Agent.Iterate = "style"
		}
		e.Agent.ActiveSkillDirs = nil
	})
	expanded := prompt
	if strings.Contains(prompt, "/") {
		catalog := DiscoverSkills(docPath)
		if matched, clean := ExtractSlashSkills(prompt, catalog); len(matched) > 0 {
			dirs := SkillDirs(matched)
			e.Locked(func() { e.Agent.ActiveSkillDirs = dirs })
			expanded = ExpandPromptWithSkills(clean, matched)
		}
	}
	text := wrapParallel(e, expanded)
	if o.Designs > 1 {
		text += "\n\n" + firstDesign
	}
	startTurn(e, prompt, text, checkCancel, false)
}

// chatTitle is the first prompt on one line: whitespace runs (line breaks too) collapsed, capped at 100 chars.
func chatTitle(shown string) string {
	r := []rune(strings.Join(strings.Fields(shown), " "))
	if len(r) > 100 {
		r = r[:100]
	}
	return string(r)
}

// startTurn sends prompt to the main agent (same conversation), showing shown as the user's message. When
// checkCancel is true, the turn is dropped if Stop ran since the reservation or previous run ended; isIteration
// marks automatic "Let it cook" variation turns (distinct from queued user edit prompts).
func startTurn(e *engine.Engine, shown, prompt string, checkCancel, isIteration bool) {
	// Held until the run is marked running: a clear that sweeps the chat files must not interleave with a new
	// chat's first save.
	chatOps.Lock()
	var main engine.Profile
	var queued string
	var opts engine.QueueOpts
	stopped, again := false, false
	e.Locked(func() {
		if checkCancel && e.Agent.Cancel != nil && e.Agent.Cancel.IsSet() {
			// Prompts sent after Stop queued behind this continuation: run them now as a fresh turn, keeping the
			// agent busy so nothing else slips in, instead of leaving them stranded.
			stopped = true
			var ok bool
			queued, opts, ok = takeQueue(e)
			if !ok {
				e.Agent.Running = false
				e.Agent.EndTurnIsolation()
				return
			}
			e.Agent.Cancel = engine.NewFlag()
			again = true
			return
		}
		if e.Agent.Chat == nil {
			id := fmt.Sprintf("%x%04x", engine.NowMs(), doc.RandomU64()&0xffff)
			e.Agent.Chat = &engine.Chat{ID: id, Title: chatTitle(shown)}
		}
		p, ok := e.Agent.Profile(e.Agent.Main)
		if e.Agent.Main == "" || !ok {
			p = e.Agent.NewProfile("")
			e.Agent.Main = p.ID
		}
		main = p
		e.Agent.IsVariationTurn = isIteration || e.Agent.Mode == "side"
		e.BeginTurnSnapshot()
		e.Agent.Cancel = engine.NewFlag()
		e.Agent.Spawned = 0
		if e.Agent.Parallel > 1 {
			team := e.Agent.AllocTeam(main.ID, e.Agent.Parallel-1)
			members := make([]jsonv.Value, 0, len(team)+1)
			members = append(members, main.JSON())
			for _, tm := range team {
				members = append(members, tm.JSON())
			}
			e.Emit(engine.Object("type", "agent_team", "members", jsonv.NewArray(members...)))
		} else {
			e.Agent.AllocTeam(main.ID, 0)
		}
	})
	if stopped {
		chatOps.Unlock()
		if again {
			begin(e, queued, opts, true)
		}
		return
	}
	defer chatOps.Unlock()
	emit(e, main, "user", engine.Object("text", shown))
	startRun(e, main, prompt, "")
}

// wrapParallel adds the parallel agents' instructions ("split work" and "side by side" modes).
func wrapParallel(e *engine.Engine, prompt string) string {
	var n int
	var mode string
	e.Locked(func() { n, mode = e.Agent.Parallel, e.Agent.Mode })
	if n <= 1 {
		return prompt
	}
	if mode == "side" {
		return fmt.Sprintf("%s\n\nWork as the coordinator of %d designers who each design their own take on this task side by side.\n"+
			"1. Decide %d design directions for this brief, one per designer, each apart from the others on at least two variation axes "+
			"(Designing). Post them first: Framing / Direction / Why for yours, one Direction line for each other designer.\n"+
			"2. Pick a sensible screen size for the task. Never edit or overwrite earlier finished screens already on the canvas unless the user asked to edit them.\n"+
			"3. Create %d empty top-level frames in aligned rows, named \"1. <short title>\" … \"%d. <short title>\" (not placeholders), using FindEmptySpace({width: w, height: h, direction: \"right\", padding: 160}).\n"+
			"4. Immediately call spawn_agents with %d entries: entry k gets containerNodes [frame k+1] and, as its prompt, designer k+1's design direction "+
			"(concept · palette · type · signature move).\n"+
			"5. Then design your own direction inside frame 1. Do not touch the other frames.",
			prompt, n, n, n, n, n-1)
	}
	return fmt.Sprintf("%s\n\nSplit this work across %d extra designers (total %d parallel designers) using the `spawn_agents` tool. "+
		"Never modify or recolor earlier finished screens already on the canvas unless the user explicitly asked to edit them; if introducing a new palette alongside existing screens, prefix your new variable names so existing screens stay untouched. "+
		"First lock the shared system in one compact setup `execute` call: define semantic `SetVariables`, "+
		"place any shared `reusable: true` components above the screens with FindEmptySpace({width: w, height: h, direction: \"top\", padding: 80}), and place the empty top-level screen frames in aligned rows with FindEmptySpace({width: w, height: h, direction: \"right\", padding: 160}). "+
		"Immediately call `spawn_agents` right after that setup call, giving each designer its container frame id(s), `$variable` names, reusable component ids, typography scale, and a self-contained task; keep one screen for yourself and build it right after spawning.", prompt, n-1, n)
}

// startRun starts p's run on a goroutine; parent is "" for the main agent. False when a child was not started
// because Stop already ran: checked under the lock that registers it, so a Stop cannot land between the check
// and the child counting as active. Its profile is dropped again.
func startRun(e *engine.Engine, p engine.Profile, prompt, parent string) bool {
	child := parent != ""
	started := false
	e.Locked(func() {
		if child && e.Agent.Cancel.IsSet() {
			e.Agent.Profiles = deleteProfile(e.Agent.Profiles, p.ID)
			return
		}
		e.Agent.Active++
		e.Agent.Running = true
		started = true
	})
	if started {
		go run(e, p, prompt, child)
	}
	return started
}

func deleteProfile(ps []engine.Profile, id string) []engine.Profile {
	out := ps[:0:0]
	for _, q := range ps {
		if q.ID != id {
			out = append(out, q)
		}
	}
	return out
}

// next is what a finished run hands over to.
type next struct {
	prompt string // a "Let it cook" iteration
	queued string
	opts   engine.QueueOpts
	// iterate: start the iteration; else the queued prompts.
	iterate bool
}

// run is one agent's run (agent.rs `start_thread`'s thread).
func run(e *engine.Engine, p engine.Profile, prompt string, child bool) {
	// The run itself is guarded below; this keeps the process alive should the bookkeeping around it panic.
	defer func() { recover() }()
	started := time.Now()
	// The chat keeps which model wrote each reply: the settings may change before it is read again. The client
	// comes from the same snapshot as the model: a provider switched meanwhile cannot get the other provider's
	// model id.
	var backend, name string
	var hasModel bool
	var client model.Provider
	var contextLimit int
	var setup error
	e.Locked(func() {
		backend = e.Backend()
		name, hasModel, setup = e.RunModel(backend)
		effModel := name
		if !hasModel || effModel == "" {
			effModel = DefaultModel
		}
		contextLimit = e.ContextWindowLimit(backend, effModel)
		if setup == nil && backend != "claude-code" {
			client, setup = e.CurrentProvider()
		}
	})
	var shown jsonv.Value
	if setup == nil && hasModel {
		shown = name
	}
	emit(e, p, "start", engine.Object("child", child, "backend", backend, "model", shown))
	if !hasModel {
		name = ""
	}
	usage, err := guarded(func() (*jsonv.Object, error) {
		switch {
		case setup != nil:
			// A provider with no model listed (or no key) fails the turn with what to fix.
			return nil, setup
		case client != nil:
			return runAPI(e, p, prompt, child, name, client, contextLimit)
		}
		return runClaudeCode(e, p, prompt, child, name)
	})
	var stopped bool
	e.Locked(func() { stopped = e.Agent.Cancel.IsSet() })
	if err != nil {
		if !stopped {
			if text := err.Error(); text != "" {
				emit(e, p, "error", engine.Object("text", text, "signIn", signedOut(text)))
			}
			// A failed main turn ends the iterations.
			if !child {
				e.Locked(func() { e.Agent.Cook = 1 })
			}
		}
		if usage == nil {
			usage = jsonv.NewObject()
		}
	}
	e.Locked(func() {
		e.StreamsOfAgentEnd(p.ID)
		// A child runs once under a fresh id: its globals can never be read again.
		if child {
			delete(e.Script.Globals, p.ID)
		}
		// A request its (now gone) process still waits on is denied; its card goes away.
		for id, w := range e.Agent.Permissions {
			if w.Agent == p.ID {
				w.Answer <- false
				delete(e.Agent.Permissions, id)
			}
		}
	})
	emit(e, p, "done", engine.Object("durationMs", jsonv.Int(time.Since(started).Milliseconds()), "usage", usage, "stopped", stopped))
	var then *next
	e.Locked(func() {
		e.Agent.Active--
		if e.Agent.Active > 0 {
			return
		}
		clearGenerating(e)
		e.Emit(engine.Object("type", "agent", "kind", "all_done"))
		// The next variation starts before the queue: queued prompts wait for the last variation.
		switch {
		case e.Agent.Cook > 1 && !stopped:
			e.Agent.Cook--
			then = &next{iterate: true, prompt: nextLayout}
			if e.Agent.Iterate == "style" {
				then.prompt = nextStyle
			}
		case stopped:
			// Prompts queued while the stopped agents were shutting down are dropped too.
			dropQueue(e)
		default:
			if q, o, ok := takeQueue(e); ok {
				then = &next{queued: q, opts: o}
			}
		}
		// A continuation keeps the agent busy until its run starts, so a reset cannot slip in between.
		e.Agent.Running = then != nil
		if then == nil {
			e.Agent.EndTurnIsolation()
		}
	})
	switch {
	case then == nil:
	case then.iterate:
		startTurn(e, iterating, wrapParallel(e, then.prompt), true, true)
	default:
		begin(e, then.queued, then.opts, true)
	}
}

// guarded runs f, turning a panic into a failed run: the run then ends as after an error (Rust's run thread
// would die with the agent left running).
func guarded(f func() (*jsonv.Object, error)) (u *jsonv.Object, err error) {
	defer func() {
		if r := recover(); r != nil {
			u, err = nil, fmt.Errorf("internal error (panic): %v", r)
		}
	}()
	return f()
}

// NoteFlagged: an `execute` call of agent came out of before; remember the frames it newly flagged
// `placeholder` when agent is a built-in one (see clearGenerating). Call it under the engine lock.
func NoteFlagged(e *engine.Engine, agent string, before *jsonv.Object) {
	if _, ok := e.Agent.Profile(agent); !ok {
		return
	}
	old := map[string]bool{}
	doc.Walk(jsonv.Items(jsonv.Get(before, "children")), func(n jsonv.Value) {
		if jsonv.IsTrue(jsonv.Get(n, "placeholder")) {
			old[jsonv.StrOr(jsonv.Get(n, "id"))] = true
		}
	})
	doc.Walk(e.Doc.Children(), func(n jsonv.Value) {
		if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok && jsonv.IsTrue(jsonv.Get(n, "placeholder")) && !old[id] {
			e.Agent.Flagged = append(e.Agent.Flagged, id)
		}
	})
}

// clearGenerating: the run is over, so frames its agents flagged `placeholder` that are still flagged (Stop, an
// error or a dead process came before the call that clears it) stop showing as "Generating"; an external agent's
// frames keep their flag. Silent and out of the undo history, like the AI Pen's own finish. A frame undone during
// the run is cleared in history only, so a redo does not bring it back flagged. Under the engine lock.
func clearGenerating(e *engine.Engine) {
	flagged := e.Agent.Flagged
	e.Agent.Flagged = nil
	off := engine.Object("placeholder", false)
	pens := e.Agent.PenFrames()
	for _, id := range flagged {
		n := e.Doc.Find(id)
		drawn := false
		for _, f := range pens {
			drawn = drawn || f == id
		}
		if (n == nil || jsonv.IsTrue(jsonv.Get(n, "placeholder"))) && !drawn {
			e.ApplyGenerated(id, off, true)
		}
	}
}

// takeQueue is the queued prompts as one message, with the options they were sent with. Under the engine lock.
func takeQueue(e *engine.Engine) (string, engine.QueueOpts, bool) {
	queued := e.Agent.Queue
	e.Agent.Queue = nil
	if len(queued) == 0 {
		return "", engine.QueueOpts{}, false
	}
	o := e.Agent.QueueOpts
	e.Agent.QueueOpts = engine.QueueOpts{}
	return strings.Join(queued, "\n\n"), o, true
}

// dropQueue drops the queued prompts and tells the UI, so their bubbles stop promising to send them. Under the
// engine lock.
func dropQueue(e *engine.Engine) {
	dropped := len(e.Agent.Queue) > 0
	e.Agent.Queue = nil
	if dropped {
		e.Emit(engine.Object("type", "agent", "kind", "dequeued"))
	}
}

// StoppedSince is whether a pen asked for by agent's `execute` that began at mark (Agent.Stops then) starts
// stopped: a Stop came while its script ran (the engine is not held meanwhile, and an MCP client's call has no
// run whose flag Stop sets), or it is a built-in agent's call and their run is being stopped. An external agent's
// later call is not that run's. Under the engine lock.
func StoppedSince(e *engine.Engine, mark uint64, agent string) bool {
	_, builtin := e.Agent.Profile(agent)
	return e.Agent.Stops != mark || (builtin && e.Agent.Running && e.Agent.Cancel.IsSet())
}

// Stop is `agent_stop`: every run, pen, queued prompt and waiting permission request is stopped.
func Stop(e *engine.Engine) {
	e.Locked(func() {
		e.Agent.Stops++
		e.Agent.Cancel.Set()
		for _, pen := range e.Agent.Pens {
			pen.Cancel.Set()
		}
		dropQueue(e)
		e.Agent.Cook = 1
		for id, w := range e.Agent.Permissions {
			w.Answer <- false
			delete(e.Agent.Permissions, id)
		}
		for _, p := range e.Agent.Profiles {
			e.StreamsOfAgentEnd(p.ID)
		}
	})
}

// Reset is `agent_reset`: start a new conversation. A running agent keeps writing into whatever chat and
// session are current, so it is refused until the agents have stopped.
func Reset(e *engine.Engine) error {
	var err error
	e.Locked(func() {
		if e.Agent.Running {
			err = fmt.Errorf("Stop the agent before starting a new conversation.")
			return
		}
		e.Agent.Clear()
	})
	return err
}
