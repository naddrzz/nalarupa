package agent

// The ports of agent.rs's tests come first, then the run, queue, children, permission and chat scenarios of the
// Go port (the claude_code.rs tests and the Claude Code runs are in claudecode_test.go).

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/guide"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// newEngine is an offline engine on its own data folder with the agent attached. The test ends only once its
// runs have ended, so their files can go with the folder.
func newEngine(t *testing.T) *engine.Engine {
	t.Helper()
	e := engine.New(t.TempDir())
	e.Offline()
	Attach(e)
	t.Cleanup(func() {
		Stop(e)
		waitIdle(t, e)
		flushChatSaves()
	})
	return e
}

// waitIdle waits until no run is active and no continuation is pending.
func waitIdle(t *testing.T, e *engine.Engine) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		idle := false
		e.Locked(func() { idle = e.Agent.Active == 0 && !e.Agent.Running })
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the agent never went idle")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// agentEvents are the queued agent events.
func agentEvents(e *engine.Engine) []jsonv.Value {
	var out []jsonv.Value
	e.Locked(func() {
		for _, ev := range e.Events {
			if jsonv.StrIs(jsonv.Get(ev, "type"), "agent") {
				out = append(out, ev)
			}
		}
	})
	return out
}

func kinds(evs []jsonv.Value) []string {
	out := []string{}
	for _, ev := range evs {
		out = append(out, jsonv.StrOr(jsonv.Get(ev, "kind")))
	}
	return out
}

func count(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return n
}

func TestTruncateCutsAtACharBoundary(t *testing.T) {
	for _, c := range []struct {
		s    string
		n    int
		want string
	}{{"abc", 5, "abc"}, {"abcdef", 3, "abc…"}, {"日本語", 4, "日…"}, {"日本語", 9, "日本語"}} {
		if got := truncate(c.s, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

func TestToolDoneListsEveryDetectedIssueEvenPastTheOutputCut(t *testing.T) {
	var names strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&names, "\"Node %d\": \"n%05d\"\n", i, i)
	}
	text := "OK\n\n## Created nodes by name\n" + names.String() + "\n## Issues detected (fix them in the next call)\n- Text \"A\" has no fill\n- \"Card\" is outside its parent\n"
	ev := toolDoneEvent("t1", "execute", jsonv.NewObject(), text, nil, false)
	if len(jsonv.StrOr(jsonv.Get(ev, "output"))) >= len(text) {
		t.Error("the output is cut")
	}
	if got := string(jsonv.Marshal(jsonv.Get(ev, "issues"))); got != `["Text \"A\" has no fill","\"Card\" is outside its parent"]` {
		t.Error(got)
	}
	if got := string(jsonv.Marshal(jsonv.Get(toolDoneEvent("t2", "execute", jsonv.NewObject(), "OK\n", nil, false), "issues"))); got != "[]" {
		t.Error(got)
	}
}

func TestEachBackendGetsItsInstructions(t *testing.T) {
	api, cli := Instructions("api"), Instructions("claude-code")
	if !strings.HasPrefix(api, systemIntro) || !strings.HasPrefix(cli, systemIntro) {
		t.Fatal("both start with the intro")
	}
	execute, _ := guide.Read("execute")
	if !strings.Contains(api, execute) {
		t.Error("the API backend carries the execute guide")
	}
	if !strings.Contains(cli, "read_guide") || strings.Contains(cli, execute) {
		t.Error("Claude Code reads the guide through the tool")
	}
}

func TestBothBackendsAlwaysGetTheDesignCore(t *testing.T) {
	core, failed := guide.Read("designing")
	if failed {
		t.Fatal("the design core is a guide topic")
	}
	for _, backend := range []string{"api", "claude-code"} {
		if !strings.Contains(Instructions(backend), core) {
			t.Errorf("%s instructions carry the design core", backend)
		}
	}
	// Always loaded: every line costs context on every turn (spec #614: about 60 lines).
	const budget = 60
	if n := len(lines(core)); n > budget {
		t.Errorf("the design core stays within its %d-line budget (%d lines)", budget, n)
	}
}

func TestANewStyleVariationDiffersOnNamedAxes(t *testing.T) {
	core, _ := guide.Read("designing")
	for _, axis := range []string{"framing", "layout structure", "type pairing", "color logic"} {
		if !strings.Contains(core, axis) {
			t.Errorf("the design core names the %s axis once, for every prompt to point at", axis)
		}
	}
	if !strings.Contains(nextStyle, "variation axes") || strings.Contains(nextStyle, "archetype") {
		t.Error(nextStyle)
	}
	if !strings.Contains(nextLayout, "design direction") || strings.Contains(nextLayout, "style") {
		t.Error(nextLayout)
	}
}

func TestTheDesignCoreCarriesTheCritiquePass(t *testing.T) {
	core, _ := guide.Read("designing")
	if !strings.Contains(core, "Critique each finished section") || !strings.Contains(core, "screenshot") || !strings.Contains(core, "two revision") {
		t.Error(core)
	}
	for _, quality := range []string{"Hierarchy", "Alignment", "Spacing rhythm", "Contrast", "Content", "Fixation", "Distinctness"} {
		if !strings.Contains(core, "**"+quality+":**") {
			t.Errorf("the critique checklist names %s", quality)
		}
	}
}

func TestASingleDesignerGetsThePromptUnchanged(t *testing.T) {
	e := newEngine(t)
	e.Locked(func() { e.Agent.Parallel, e.Agent.Mode = 1, "side" })
	if got := wrapParallel(e, "Landing page for a bakery"); got != "Landing page for a bakery" {
		t.Error(got)
	}
}

func TestSideBySideDesignersEachGetTheirOwnDesignDirection(t *testing.T) {
	entry := func(p string) jsonv.Value {
		return engine.Object("prompt", p, "containerNodes", engine.Strings([]string{"f2"}))
	}
	tide := "Tide table · chart white, deep-water ink, buoy red · Barlow Condensed + Source Serif 4 · the tide curve behind the rows"
	seed := "Seed packet · allotment green, chalk, seed yellow · Bricolage Grotesque + Work Sans · a 'sow by' stamp per section"
	if err := sideDirections([]jsonv.Value{entry(tide), entry(seed)}); err != nil {
		t.Error(err)
	}
	if err := sideDirections([]jsonv.Value{entry(tide), entry("")}); err == nil || !strings.Contains(err.Error(), "design direction") {
		t.Error("every designer needs one", err)
	}
	if err := sideDirections([]jsonv.Value{entry(tide), entry(" " + tide + " ")}); err == nil || !strings.Contains(err.Error(), "share one design direction") {
		t.Error("no two designers share one", err)
	}
	a := childPrompt(true, 2, 3, []string{"f2"}, "{}", tide)
	b := childPrompt(true, 3, 3, []string{"f3"}, "{}", seed)
	if !strings.Contains(a, tide) || !strings.Contains(b, seed) || strings.Contains(a, seed) {
		t.Errorf("each designer's instructions carry its own direction:\n%s\n%s", a, b)
	}
	alone := childPrompt(false, 2, 2, []string{"f2"}, "{}", "Build the footer")
	if !strings.Contains(alone, "Task: Build the footer") || strings.Contains(alone, "design direction") {
		t.Errorf("a split task is unchanged:\n%s", alone)
	}
}

func TestOnlyTheLastToolCallOfAMaxTokensReplyIsCutOff(t *testing.T) {
	call := func(id string) jsonv.Value {
		return engine.Object("type", "tool_use", "id", id, "name", "execute", "input", jsonv.NewObject())
	}
	blocks := []jsonv.Value{call("toolu_a"), call("toolu_b")}
	if got := cutOffCall("max_tokens", blocks); !jsonv.StrIs(jsonv.Get(got, "id"), "toolu_b") {
		t.Error(got)
	}
	if cutOffCall("tool_use", blocks) != nil {
		t.Error("tool_use")
	}
	if cutOffCall("max_tokens", []jsonv.Value{call("toolu_a"), engine.Object("type", "text", "text", "done")}) != nil {
		t.Error("calls that finished before the text still run")
	}
	if cutOffCall("max_tokens", nil) != nil {
		t.Error("empty")
	}
}

func TestChatSessionRoundtrips(t *testing.T) {
	dir := t.TempDir()
	e := engine.New(dir)
	e.Offline()
	Attach(e)
	a := &e.Agent
	var p, helper engine.Profile
	e.Locked(func() {
		p = a.NewProfile("")
		a.Main = p.ID
		a.Session = "claude-code-session"
		a.Messages = []jsonv.Value{jsonv.MustParse(`{"role": "user", "content": [{"type": "text", "text": "Design a page"}]}`)}
		a.Chat = &engine.Chat{ID: "abc123", Title: "Design a page"}
		ev := func(kind, text string) jsonv.Value {
			return engine.Object("type", "agent", "kind", kind, "agent", p.JSON(), "text", text)
		}
		for _, c := range []struct {
			kind, text string
			save       bool
		}{{"user", "Design a page", true}, {"text_delta", "Hel", false}, {"tool_progress", "", false}, {"text_delta", "lo", false}, {"done", "", true}} {
			if got := a.Record(ev(c.kind, c.text)); got != c.save {
				t.Errorf("%s: record %v", c.kind, got)
			}
		}
		helper = a.NewProfile(p.ID)
		if !a.Record(engine.Object("type", "agent", "kind", "done", "agent", helper.JSON())) {
			t.Error("a helper's done saves")
		}
		if err := saveChat(e); err != nil {
			t.Fatal(err)
		}
	})
	flushChatSaves()

	listed := listChats(dir)
	if len(listed) != 1 || !jsonv.StrIs(jsonv.Get(listed[0], "id"), "abc123") || !jsonv.StrIs(jsonv.Get(listed[0], "title"), "Design a page") {
		t.Fatal(string(jsonv.Marshal(jsonv.NewArray(listed...))))
	}

	var b engine.AgentState
	saved, err := readChat(dir, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	b.Restore("abc123", saved)
	if b.Session != "claude-code-session" {
		t.Error(b.Session)
	}
	if !jsonv.Equal(jsonv.NewArray(b.Messages...), jsonv.NewArray(a.Messages...)) {
		t.Error("messages")
	}
	if b.Main != a.Main {
		t.Error(b.Main)
	}
	if q, ok := b.Profile(p.ID); !ok || q.Name != p.Name {
		t.Error(q)
	}
	if q, ok := b.Profile(helper.ID); !ok || q.Name != helper.Name || q.Parent != p.ID {
		t.Error("a restored chat keeps its helpers' names", q)
	}
	if got := kinds(b.Chat.Events); !slices.Equal(got, []string{"user", "text_delta", "done", "done"}) {
		t.Error(got)
	}
	if got := jsonv.Get(b.Chat.Events[1], "text"); !jsonv.StrIs(got, "Hello") {
		t.Error(got)
	}
	if _, err := readChat(dir, "../settings"); err == nil {
		t.Error("a path is no chat id")
	}
}

func TestProfileNamesAreUniqueAndANewConversationStartsEmpty(t *testing.T) {
	var a engine.AgentState
	names := map[string]bool{}
	for i := 0; i < 40; i++ {
		names[a.NewProfile("").Name] = true
	}
	if len(names) != 40 {
		t.Errorf("no two agents of one conversation share a name: %d names", len(names))
	}
	var old []string
	for _, p := range a.Profiles {
		old = append(old, p.ID)
	}
	a.Clear()
	if len(a.Profiles) != 0 {
		t.Error("a new conversation forgets the old agents")
	}
	if fresh := a.NewProfile(""); slices.Contains(old, fresh.ID) {
		t.Error("a new conversation's agent never takes an old id (its script globals are kept)")
	}
}

func TestProfileColorsAreDistinctWithinAConversation(t *testing.T) {
	for i := 0; i < 200; i++ {
		var a engine.AgentState
		colors := map[string]bool{}
		for j := 0; j < engine.AgentColors(); j++ {
			colors[a.NewProfile("").Color] = true
		}
		if len(colors) != engine.AgentColors() {
			t.Fatal("agents of one conversation never share a color")
		}
	}
}

func TestChatTitleIsOneLine(t *testing.T) {
	if got := chatTitle("  Make it dark\nand add a footer "); got != "Make it dark and add a footer" {
		t.Error(got)
	}
	if got := chatTitle("Design a pricing page\n\nPlans:\r\n- Free\t- Pro"); got != "Design a pricing page Plans: - Free - Pro" {
		t.Error(got)
	}
	if got := chatTitle(strings.Repeat("word ", 40)); utf8.RuneCountInString(got) != 100 {
		t.Error(got)
	}
}

func cause(t *testing.T, ev string) (string, bool) {
	t.Helper()
	return errorCause(jsonv.MustParse(ev))
}

func TestErrorCauseReadsResultThenErrors(t *testing.T) {
	if got, _ := cause(t, `{"result": " Credit balance is too low "}`); got != "Credit balance is too low" {
		t.Error(got)
	}
	if got, _ := cause(t, `{"result": "", "errors": ["", "Quota exceeded", " Try later "]}`); got != "Quota exceeded\nTry later" {
		t.Error(got)
	}
	if _, ok := cause(t, `{"result": "  ", "errors": []}`); ok {
		t.Error("no cause: the exit error shows stderr instead")
	}
	if _, ok := cause(t, `{}`); ok {
		t.Error("empty")
	}
}

func TestASignedOutCLIPointsAtTheTerminalAndTheSettings(t *testing.T) {
	hint, _ := cause(t, `{"result": "Not logged in · Please run /login"}`)
	if !strings.HasPrefix(hint, "Claude Code is not signed in") || !strings.Contains(hint, "Agent settings") {
		t.Error(hint)
	}
	if !signedOut(hint) || signedOut("Credit balance is too low") {
		t.Error("the error card offers Sign in for this cause only")
	}
	if got, _ := cause(t, `{"result": "Invalid API key · Please run /login"}`); got != hint {
		t.Error(got)
	}
	if got := exitError("exit status: 1", "Error: Not logged in", false); got != hint {
		t.Error(got)
	}
	if got, _ := cause(t, `{"result": "Credit balance is too low"}`); got != "Credit balance is too low" {
		t.Error(got)
	}
	// A path that merely contains /login keeps the real cause.
	if got := exitError("exit status: 1", "Cannot find module /app/login.js", false); got != "claude exited with exit status: 1: Cannot find module /app/login.js" {
		t.Error(got)
	}
	if got, _ := cause(t, `{"result": "ENOENT: /srv/login/config.json"}`); got != "ENOENT: /srv/login/config.json" {
		t.Error(got)
	}
	// The phrase in the middle of a long text, where clipping would drop it, still gives the hint.
	long := strings.Repeat("x", 5000) + "Not logged in · Please run /login" + strings.Repeat("y", 5000)
	if got, _ := errorCause(engine.Object("result", long)); got != hint {
		t.Error("long result")
	}
	if got := exitError("exit status: 1", long, false); got != hint {
		t.Error("long stderr")
	}
}

func TestLongErrorTextIsClippedToBothEnds(t *testing.T) {
	var trace strings.Builder
	for i := 1; i <= 2000; i++ {
		fmt.Fprintf(&trace, "    at frame %d (node:internal/modules/cjs/loader:1234:5)\n", i)
	}
	e := exitError("exit status: 1", trace.String(), false)
	if len(e) > errorMax {
		t.Errorf("%d bytes", len(e))
	}
	if !strings.HasPrefix(e, "claude exited with exit status: 1: at frame 1 ") || !strings.Contains(e, "\n…\n") || !strings.HasSuffix(e, "at frame 2000 (node:internal/modules/cjs/loader:1234:5)") {
		t.Error(e)
	}
	c, _ := errorCause(engine.Object("result", strings.Repeat("日本語", 2000)))
	if len(c) > errorMax || !strings.Contains(c, "…") || !utf8.ValidString(c) {
		t.Error("multi-byte text is cut on a char boundary")
	}
	ascii, _ := errorCause(engine.Object("result", strings.Repeat("x", errorMax+1)))
	if len(ascii) > errorMax {
		t.Errorf("%d bytes", len(ascii))
	}
	if got := clipEnds("short", errorMax); got != "short" {
		t.Error(got)
	}
}

func TestExitErrorHasNoDuplicateOrEmptyTail(t *testing.T) {
	for _, c := range []struct {
		stderr   string
		reported bool
		want     string
	}{
		{"", true, ""}, // the error result already showed the cause
		{"boom\n", true, ""},
		{" \n", false, "claude exited with exit status: 1"},
		{"boom\n", false, "claude exited with exit status: 1: boom"},
	} {
		if got := exitError("exit status: 1", c.stderr, c.reported); got != c.want {
			t.Errorf("%q %v: %q, want %q", c.stderr, c.reported, got, c.want)
		}
	}
}

// The per-agent MCP config is written for its run; only configs of dead sessions (a day old) are swept. That the
// run's own config goes with it is checked by the Claude Code runs (claudecode_test.go).
func TestMCPConfigIsRemovedWithItsRun(t *testing.T) {
	e := newEngine(t)
	dir := e.DataDir
	e.Locked(func() { e.MCPPort = 4321 })
	stale, fresh, other := filepath.Join(dir, "claude-code-mcp-dead.json"), filepath.Join(dir, "claude-code-mcp-live.json"), filepath.Join(dir, "settings.json")
	for _, f := range []string{stale, fresh, other} {
		if err := os.WriteFile(f, []byte("{}"), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-mcpConfigStale - time.Minute)
	for _, f := range []string{stale, other} {
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := MCPConfig(e, "a1")
	if err != nil {
		t.Fatal(err)
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if !exists(cfg) || !exists(fresh) || !exists(other) {
		t.Error("the run's config, a live session's and other files stay")
	}
	if exists(stale) {
		t.Error("a config left by a dead session is swept")
	}
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), "4321/mcp/a1") {
		t.Error(string(b))
	}
	e.Locked(func() { e.MCPPort = 0 })
	if _, err := MCPConfig(e, "a1"); err == nil || err.Error() != "the MCP server is not running" {
		t.Error(err)
	}
}

// Stop reaches an AI Pen that outlives its run: a later turn's fresh cancel flag does not orphan the pen, and the
// state reports it drawing so the composer offers Stop.
func TestStopCancelsAPenFromAnEarlierTurn(t *testing.T) {
	e := newEngine(t)
	pen := engine.NewFlag()
	e.Locked(func() {
		e.Agent.Pens = append(e.Agent.Pens, engine.Pen{Frame: "penframe", Cancel: pen})
		if !jsonv.IsTrue(jsonv.Get(e.State(), "penDrawing")) {
			t.Error("drawing")
		}
		// A later turn swaps the run's flag, as startTurn does.
		e.Agent.Cancel = engine.NewFlag()
	})
	Stop(e)
	if !pen.IsSet() {
		t.Error("Stop cancels the pen")
	}
	e.Locked(func() {
		e.Agent.Pens = nil
		e.Agent.Cancel = engine.NewFlag()
		if jsonv.IsTrue(jsonv.Get(e.State(), "penDrawing")) {
			t.Error("not drawing")
		}
	})
}

// A pen that outlives its run draws into a frame of the open document: New, Open and an MCP `open_document` wait
// for it like for a run, so it never draws into a document that replaced it.
func TestADrawingPenKeepsTheDocumentOpen(t *testing.T) {
	e := newEngine(t)
	var before jsonv.Value
	e.Locked(func() {
		e.Agent.Pens = append(e.Agent.Pens, engine.Pen{Frame: "penframe", Cancel: engine.NewFlag()})
		before = jsonv.Clone(e.Doc.Root)
	})
	// Never opened: a missing file is created by `open_document`.
	path := filepath.Join(t.TempDir(), "pen-switch.nala")
	_, newErr := e.Dispatch(engine.Object("cmd", "new", "template", "empty"))
	_, openErr := e.Dispatch(engine.Object("cmd", "open", "path", path))
	var tool engine.ToolOut
	e.Locked(func() { tool = e.Tool("open_document", engine.Object("path", path)) })
	if newErr == nil || newErr.Error() != engine.SwitchBusy {
		t.Error("New waits for the pen", newErr)
	}
	if openErr == nil || openErr.Error() != engine.SwitchBusy {
		t.Error("Open waits for the pen", openErr)
	}
	if !tool.Error || tool.Text != engine.SwitchBusy {
		t.Error("open_document waits for the pen:", tool.Text)
	}
	e.Locked(func() {
		e.Agent.Pens = nil
		if !jsonv.Equal(e.Doc.Root, before) {
			t.Error("the document stays")
		}
	})
	if _, err := os.Stat(path); err == nil {
		t.Error("nothing was created on disk")
	}
}

// A pen whose `execute` saw a Stop while its script ran starts stopped, also from an MCP client's call (no run
// going on); a pen from a call that began after the Stop draws.
func TestAStopDuringExecuteReachesItsPens(t *testing.T) {
	e := newEngine(t)
	var mark uint64
	e.Locked(func() {
		mark = e.Agent.Stops
		if StoppedSince(e, mark, "external") {
			t.Error("no Stop: the pen draws")
		}
	})
	Stop(e)
	e.Locked(func() {
		if !StoppedSince(e, mark, "external") {
			t.Error("a Stop while the script ran stops its pen, with no run going on")
		}
		after := e.Agent.Stops
		if StoppedSince(e, after, "external") {
			t.Error("a call that began after the Stop draws")
		}
		// While the stopped built-in run winds down, its own later calls start stopped; an external agent's do not.
		e.Agent.Running = true
		e.Agent.Profiles = append(e.Agent.Profiles, engine.Profile{ID: "agS", Name: "S", Color: "#000000"})
		if !StoppedSince(e, after, "agS") {
			t.Error("the stopping run's own call starts stopped")
		}
		if StoppedSince(e, after, "external") {
			t.Error("an external call during the run's wind-down draws")
		}
		e.Agent.Profiles = nil
		e.Agent.Cancel = engine.NewFlag()
		e.Agent.Running = false
	})
}

// Stop drops the queued prompts and says so, so the chat does not keep promising to send them.
func TestStopAnnouncesDroppedQueue(t *testing.T) {
	e := newEngine(t)
	saved := func(kind string) int {
		flushChatSaves()
		v, err := readChat(e.DataDir, "5709e5")
		if err != nil {
			t.Fatal(err)
		}
		return count(kinds(jsonv.Items(jsonv.Get(v, "events"))), kind)
	}
	e.Locked(func() {
		e.Agent.Running = true
		e.Agent.Chat = &engine.Chat{ID: "5709e5", Title: "stop"}
	})
	Send(e, "also add a footer", 1, "split", 1, "")
	e.Locked(func() {
		if len(e.Agent.Queue) != 1 {
			t.Error(e.Agent.Queue)
		}
	})
	Stop(e)
	e.Locked(func() {
		if len(e.Agent.Queue) != 0 {
			t.Error(e.Agent.Queue)
		}
	})
	if n := count(kinds(agentEvents(e)), "dequeued"); n != 1 {
		t.Error(n)
	}
	if n := saved("dequeued"); n != 1 {
		t.Error("the saved chat keeps the announcement for replay", n)
	}
	Stop(e)
	if n := count(kinds(agentEvents(e)), "dequeued"); n != 1 {
		t.Error("nothing queued, nothing announced", n)
	}
	// A prompt that arrives while the stopped agents are still shutting down is dropped and announced too.
	Send(e, "one more thing", 1, "split", 1, "")
	e.Locked(func() {
		dropQueue(e)
		if len(e.Agent.Queue) != 0 {
			t.Error(e.Agent.Queue)
		}
	})
	if n := saved("dequeued"); n != 2 {
		t.Error(n)
	}
	e.Locked(func() {
		e.Agent.Running = false
		e.Agent.Chat = nil
	})
}

// A prompt queued behind a running agent keeps the Agents and Let it cook choices it was sent with (the latest
// wins).
func TestQueuedPromptKeepsItsOptions(t *testing.T) {
	e := newEngine(t)
	e.Locked(func() { e.Agent.Running = true })
	Send(e, "first", 2, "side", 1, "layout")
	Send(e, "second", 3, "split", 3, "style")
	e.Locked(func() {
		text, opts, ok := takeQueue(e)
		e.Agent.Running = false
		if !ok || text != "first\n\nsecond" {
			t.Error(text)
		}
		if opts != (engine.QueueOpts{Parallel: 3, Mode: "split", Designs: 3, Iterate: "style"}) {
			t.Error(opts)
		}
		if _, _, ok := takeQueue(e); ok {
			t.Error("the queue is empty after it is taken")
		}
	})
}

func nodeID(e *engine.Engine, name string) string {
	found := ""
	doc.Walk(e.Doc.Children(), func(n jsonv.Value) {
		if jsonv.StrIs(jsonv.Get(n, "name"), name) {
			found = jsonv.StrOr(jsonv.Get(n, "id"))
		}
	})
	return found
}

// A run that ends before the call that clears `placeholder` must not leave the frames its agents flagged
// "Generating", except the one an AI Pen is still drawing into, and must not touch an external agent's frames.
func TestRunEndClearsStalePlaceholders(t *testing.T) {
	e := newEngine(t)
	e.Locked(func() {
		e.Agent.Profiles = append(e.Agent.Profiles, engine.Profile{ID: "agT", Name: "T", Color: "#000000"})
		run := func(agent, src string) {
			out := e.Tool("execute", engine.Object("input", src, "__agent", agent))
			if out.Error {
				t.Fatal(out.Text)
			}
		}
		frame := "{type:'frame', width:100, height:100, placeholder:true"
		run("agT", "a = Insert(document, "+frame+", name:'A'})\nn = Insert(a, "+frame+", name:'Nested'})\nb = Insert(document, "+frame+", name:'B'})")
		run("external", "Insert(document, "+frame+", name:'External'})")
		a, nested, drawing, external := nodeID(e, "A"), nodeID(e, "Nested"), nodeID(e, "B"), nodeID(e, "External")
		e.Agent.Pens = append(e.Agent.Pens, engine.Pen{Frame: drawing, Cancel: engine.NewFlag()})
		clearGenerating(e)
		flag := func(id string) bool { return jsonv.IsTrue(jsonv.Get(e.Doc.Find(id), "placeholder")) }
		if flag(a) || flag(nested) {
			t.Error("flags the built-in agent set are cleared, nested frames too")
		}
		if !flag(drawing) {
			t.Error("a frame an AI Pen draws into keeps its flag")
		}
		if !flag(external) {
			t.Error("a frame an external agent flagged keeps its flag")
		}
		e.Agent.Pens = nil
	})
}

// The whole run undone while it still ran: the frame its first Insert flagged is gone when the run ends, and a
// later redo must not bring it back "Generating" with no agent running.
func TestRedoAfterTheRunDoesNotRestoreGenerating(t *testing.T) {
	e := newEngine(t)
	e.Locked(func() {
		e.LoadTemplate("empty")
		e.Agent.Profiles = append(e.Agent.Profiles, engine.Profile{ID: "agR", Name: "R", Color: "#000000"})
		e.Agent.Running = true
		out := e.Tool("execute", engine.Object("input", "Insert(document, {type:'frame', width:100, height:100, placeholder:true, name:'Undone'})", "__agent", "agR"))
		if out.Error {
			t.Fatal(out.Text)
		}
		if !e.Undo() {
			t.Fatal("undo")
		}
		clearGenerating(e)
		e.Agent.Running = false
		if !e.Redo() {
			t.Fatal("redo")
		}
		id := nodeID(e, "Undone")
		if id == "" || jsonv.IsTrue(jsonv.Get(e.Doc.Find(id), "placeholder")) {
			t.Error("the redone frame is back, not Generating")
		}
	})
}

// Auto-allowed paths: the workspace for file tools, skills for Read; `..` cannot escape.
func TestPermissionPaths(t *testing.T) {
	ws, d, skills := "C:/work/site", "C:/work/site/app.pen", "C:/home/.claude/skills"
	ok := func(f, tool string) bool { return export.PathAllowed(f, tool, ws, d, skills) }
	if !ok("C:/work/site/img/hero.png", "Write") {
		t.Error("inside")
	}
	if ok("C:/work/site/../secrets.txt", "Read") {
		t.Error("..")
	}
	if ok("C:/work/site2/a.txt", "Read") {
		t.Error("a sibling folder with the same prefix")
	}
	if ok("C:/work/site/app.pen", "Write") || !ok("C:/work/site/app.pen", "Read") {
		t.Error("the open document")
	}
	if !ok("C:/home/.claude/skills/x/SKILL.md", "Read") || ok("C:/home/.claude/skills/x/SKILL.md", "Edit") {
		t.Error("skills")
	}
	if export.PathAllowed("C:/work/site/a.txt", "Write", "", "", skills) {
		t.Error("no document folder, nothing allowed")
	}
}

// A symlink inside the workspace does not make its target (outside, or the open document) count as inside.
func TestPermissionPathsFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix only, as in Rust")
	}
	base := t.TempDir()
	ws, out := filepath.Join(base, "project"), filepath.Join(base, "outside")
	for _, d := range []string{ws, out} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	d := filepath.Join(ws, "design.nala")
	link := func(target, at string) {
		if err := os.Symlink(target, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(d, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	link(out, filepath.Join(ws, "link"))
	link(d, filepath.Join(ws, "alias.nala"))
	// Joined as written: `..` stays for PathAllowed to resolve.
	ok := func(f, tool string) bool { return export.PathAllowed(ws+"/"+f, tool, ws, d, "") }
	if !ok("sub/new.txt", "Write") {
		t.Error("a plain new file in the workspace stays allowed")
	}
	if ok("link/x.txt", "Write") {
		t.Error("new file through a link to outside")
	}
	if ok("link/../escape.txt", "Write") {
		t.Error("`..` after a link leaves the target's folder")
	}
	if ok("alias.nala", "Edit") || !ok("alias.nala", "Read") {
		t.Error("link to the open document")
	}
	link(filepath.Join(out, "missing.txt"), filepath.Join(ws, "dangling.txt"))
	link(filepath.Join(out, "newdir"), filepath.Join(ws, "dangling-dir"))
	link(filepath.Join(ws, "loop"), filepath.Join(ws, "loop"))
	if ok("dangling.txt", "Write") {
		t.Error("dangling link to a file outside")
	}
	if ok("dangling-dir/x.txt", "Write") {
		t.Error("dangling link to a folder outside")
	}
	if ok("loop", "Write") {
		t.Error("link loop")
	}
}
