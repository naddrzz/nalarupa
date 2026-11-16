package agent

// Runs over a fake Claude API (no network): the event sequence and request bodies of a turn with an `execute`
// call, queueing, Stop, Let it cook, a panicking stream, children, permissions and chats.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// request is one request the fake API received.
type request struct {
	header http.Header
	body   jsonv.Value
}

// fakeAPI is a Claude Messages API: reply answers the n-th request (from 0) with an SSE body, or writes the
// response itself and returns "".
type fakeAPI struct {
	mu   sync.Mutex
	reqs []request
	// got gets a value per request received.
	got chan struct{}
}

func newFakeAPI(t *testing.T, reply func(n int, w http.ResponseWriter, r *http.Request) string) (*fakeAPI, string) {
	t.Helper()
	f := &fakeAPI{got: make(chan struct{}, 100)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body, _ := jsonv.Parse(raw)
		f.mu.Lock()
		n := len(f.reqs)
		f.reqs = append(f.reqs, request{r.Header.Clone(), body})
		f.mu.Unlock()
		f.got <- struct{}{}
		if s := reply(n, w, r); s != "" {
			w.Header().Set("content-type", "text/event-stream")
			io.WriteString(w, s)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeAPI) requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *fakeAPI) wait(t *testing.T) {
	t.Helper()
	select {
	case <-f.got:
	case <-time.After(20 * time.Second):
		t.Fatal("no request came")
	}
}

// sse is an SSE body of `data:` payloads.
func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		fmt.Fprintf(&b, "data: %s\n\n", jsonv.Marshal(jsonv.MustParse(e)))
	}
	return b.String()
}

// textReply is a reply of one text block ending with stop.
func textReply(text, stop string) string {
	return sse(`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, text),
		`{"type":"content_block_stop","index":0}`,
		fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":5}}`, stop),
		`{"type":"message_stop"}`)
}

// toolReply is a text block, then an `execute` call of src streamed in two pieces.
func toolReply(text, id, src string) string {
	input := string(jsonv.Marshal(engine.Object("input", src)))
	half := len(input) / 2
	return sse(`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, text),
		`{"type":"content_block_stop","index":0}`,
		fmt.Sprintf(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":%q,"name":"execute","input":{}}}`, id),
		fmt.Sprintf(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":%q}}`, input[:half]),
		fmt.Sprintf(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":%q}}`, input[half:]),
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`)
}

// apiEngine is an engine whose agent runs on the fake API at url.
func apiEngine(t *testing.T, url string) *engine.Engine {
	t.Helper()
	e := newEngine(t)
	e.Locked(func() {
		e.Settings.Set("agent_backend", "api")
		e.Settings.Set("anthropic_api_key", "k")
		e.Settings.Set("anthropic_base_url", url)
	})
	return e
}

func withoutProgress(ks []string) []string {
	return slices.DeleteFunc(ks, func(k string) bool { return k == "tool_progress" })
}

func lastOf(evs []jsonv.Value, kind string) jsonv.Value {
	for i := len(evs) - 1; i >= 0; i-- {
		if jsonv.StrIs(jsonv.Get(evs[i], "kind"), kind) {
			return evs[i]
		}
	}
	return nil
}

func TestAnAPIRunStreamsRunsItsToolAndSavesTheChat(t *testing.T) {
	api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		if n == 0 {
			return toolReply("On it.", "toolu_1", "Insert(document, {type:'frame', width:10, height:10, name:'F'})")
		}
		return textReply("Done.", "end_turn")
	})
	e := apiEngine(t, url)
	if err := Send(e, "Make a frame", 1, "split", 1, "layout"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, e)
	evs := agentEvents(e)
	want := []string{"user", "start", "text_delta", "tool_start", "message_end", "tool_done", "text_delta", "message_end", "done", "all_done"}
	if got := withoutProgress(kinds(evs)); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	start := lastOf(evs, "start")
	if got := string(jsonv.Marshal(start)); !strings.Contains(got, `"child":false,"backend":"api","model":"claude-opus-5-5"`) {
		t.Error(got)
	}
	done := lastOf(evs, "tool_done")
	if jsonv.IsTrue(jsonv.Get(done, "error")) || !strings.HasPrefix(jsonv.StrOr(jsonv.Get(done, "output")), "OK") {
		t.Error(string(jsonv.Marshal(done)))
	}
	if got := string(jsonv.Marshal(jsonv.Get(lastOf(evs, "done"), "usage"))); got != `{"input":20,"output":10,"cacheRead":0,"cacheWrite":0,"turns":2}` {
		t.Error(got)
	}
	e.Locked(func() {
		if nodeID(e, "F") == "" {
			t.Error("the execute call drew the frame")
		}
		if len(e.Agent.Messages) != 4 {
			t.Error("history", len(e.Agent.Messages))
		}
	})
	reqs := api.requests()
	if len(reqs) != 2 {
		t.Fatal(len(reqs))
	}
	first := jsonv.Obj(reqs[0].body)
	if got := strings.Join(first.Keys(), ","); got != "model,max_tokens,system,tools,messages,output_config,fallbacks,thinking,stream" {
		t.Error(got)
	}
	if got := reqs[0].header.Get("anthropic-beta"); got != "server-side-fallback-2026-07-01,thinking-display-updates-2026-08-18" {
		t.Error(got)
	}
	if got := jsonv.StrOr(jsonv.Get(jsonv.Items(jsonv.Get(first, "system"))[0], "text")); got != Instructions("api") {
		t.Error("the system prompt is the API instructions")
	}
	var execute jsonv.Value
	for _, tool := range jsonv.Items(jsonv.Get(first, "tools")) {
		if jsonv.StrIs(jsonv.Get(tool, "name"), "execute") {
			execute = tool
		}
		if jsonv.StrIs(jsonv.Get(tool, "name"), "spawn_agents") {
			t.Error("a single designer gets no spawn_agents")
		}
	}
	if !jsonv.IsTrue(jsonv.Get(execute, "eager_input_streaming")) {
		t.Error("execute streams")
	}
	user := jsonv.StrOr(jsonv.Get(jsonv.Items(jsonv.Get(jsonv.Items(jsonv.Get(first, "messages"))[0], "content"))[0], "text"))
	if !strings.HasPrefix(user, "Make a frame"+editorStateNote) {
		t.Error(user)
	}
	msgs := jsonv.Items(jsonv.Get(reqs[1].body, "messages"))
	result := jsonv.Items(jsonv.Get(msgs[len(msgs)-1], "content"))[0]
	if !jsonv.StrIs(jsonv.Get(result, "type"), "tool_result") || !jsonv.StrIs(jsonv.Get(result, "tool_use_id"), "toolu_1") {
		t.Error(string(jsonv.Marshal(result)))
	}
	chats := Chats(e)
	if list := jsonv.Items(jsonv.Get(chats, "chats")); len(list) != 1 || !jsonv.Equal(jsonv.Get(list[0], "id"), jsonv.Get(chats, "current")) ||
		!jsonv.StrIs(jsonv.Get(list[0], "title"), "Make a frame") {
		t.Error(string(jsonv.Marshal(chats)))
	}
}

func TestAPromptSentWhileBusyRunsAfterTheTurn(t *testing.T) {
	release := make(chan struct{})
	api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		if n == 0 {
			<-release
		}
		return textReply("ok", "end_turn")
	})
	e := apiEngine(t, url)
	Send(e, "first", 1, "split", 1, "layout")
	api.wait(t)
	Send(e, "second", 1, "split", 1, "layout")
	if got := lastOf(agentEvents(e), "queued"); !jsonv.StrIs(jsonv.Get(got, "text"), "second") {
		t.Error("queued", got)
	}
	close(release)
	api.wait(t)
	waitIdle(t, e)
	reqs := api.requests()
	if len(reqs) != 2 {
		t.Fatal(len(reqs))
	}
	msgs := jsonv.Items(jsonv.Get(reqs[1].body, "messages"))
	if len(msgs) != 3 || !strings.HasPrefix(jsonv.StrOr(jsonv.Get(jsonv.Items(jsonv.Get(msgs[2], "content"))[0], "text")), "second") {
		t.Error("the queued prompt continues the conversation", string(jsonv.Marshal(jsonv.NewArray(msgs...))))
	}
	if n := count(kinds(agentEvents(e)), "done"); n != 2 {
		t.Error(n)
	}
}

func TestStopEndsAStreamingRun(t *testing.T) {
	api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		w.Header().Set("content-type", "text/event-stream")
		io.WriteString(w, sse(`{"type":"message_start","message":{"usage":{}}}`))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
		}
		return ""
	})
	e := apiEngine(t, url)
	Send(e, "go", 1, "split", 3, "style")
	api.wait(t)
	began := time.Now()
	Stop(e)
	waitIdle(t, e)
	if time.Since(began) > 5*time.Second {
		t.Error("Stop took", time.Since(began))
	}
	evs := agentEvents(e)
	if !jsonv.IsTrue(jsonv.Get(lastOf(evs, "done"), "stopped")) {
		t.Error("done says stopped")
	}
	if lastOf(evs, "error") != nil || count(kinds(evs), "user") != 1 {
		t.Error("a stopped run shows no error and no iteration", kinds(evs))
	}
}

func TestLetItCookRunsTheNextVariation(t *testing.T) {
	api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string { return textReply("ok", "end_turn") })
	e := apiEngine(t, url)
	Send(e, "a landing page", 1, "split", 2, "style")
	waitIdle(t, e)
	reqs := api.requests()
	if len(reqs) != 2 {
		t.Fatal(len(reqs))
	}
	text := func(r request) string {
		msgs := jsonv.Items(jsonv.Get(r.body, "messages"))
		return jsonv.StrOr(jsonv.Get(jsonv.Items(jsonv.Get(msgs[len(msgs)-1], "content"))[0], "text"))
	}
	if !strings.Contains(text(reqs[0]), firstDesign) || !strings.HasPrefix(text(reqs[1]), nextStyle) {
		t.Error(text(reqs[0]), "\n", text(reqs[1]))
	}
	var shown []string
	for _, ev := range agentEvents(e) {
		if jsonv.StrIs(jsonv.Get(ev, "kind"), "user") {
			shown = append(shown, jsonv.StrOr(jsonv.Get(ev, "text")))
		}
	}
	if !slices.Equal(shown, []string{"a landing page", iterating}) {
		t.Error(shown)
	}
}

func TestAPanickingStreamFailsOnlyItsRun(t *testing.T) {
	api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		if n == 0 {
			// A block that is not an object, then a delta into it: the client panics, as Rust's IndexMut does.
			return sse(`{"type":"message_start","message":{"usage":{}}}`,
				`{"type":"content_block_start","index":0,"content_block":"x"}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`)
		}
		return textReply("fine", "end_turn")
	})
	e := apiEngine(t, url)
	Send(e, "one", 1, "split", 1, "layout")
	waitIdle(t, e)
	evs := agentEvents(e)
	if got := jsonv.StrOr(jsonv.Get(lastOf(evs, "error"), "text")); !strings.HasPrefix(got, "internal error (panic)") {
		t.Error(got)
	}
	if lastOf(evs, "done") == nil {
		t.Error("the run still ends")
	}
	Send(e, "two", 1, "split", 1, "layout")
	waitIdle(t, e)
	if len(api.requests()) != 2 || count(kinds(agentEvents(e)), "done") != 2 {
		t.Error("the next run works")
	}
}

func TestAModelErrorEndsTheTurnWithItsText(t *testing.T) {
	_, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
		return ""
	})
	e := apiEngine(t, url)
	Send(e, "one", 1, "split", 3, "layout")
	waitIdle(t, e)
	evs := agentEvents(e)
	got := lastOf(evs, "error")
	if !strings.HasPrefix(jsonv.StrOr(jsonv.Get(got, "text")), "The Claude API rejected this key.") || !jsonv.IsFalse(jsonv.Get(got, "signIn")) {
		t.Error(string(jsonv.Marshal(got)))
	}
	if count(kinds(evs), "user") != 1 {
		t.Error("a failed main turn ends the iterations")
	}
}

func TestANoKeySetupFailsTheTurn(t *testing.T) {
	e := newEngine(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	e.Locked(func() { e.Settings.Set("agent_backend", "api") })
	Send(e, "one", 1, "split", 1, "layout")
	waitIdle(t, e)
	evs := agentEvents(e)
	if got := jsonv.StrOr(jsonv.Get(lastOf(evs, "error"), "text")); !strings.HasPrefix(got, "No Anthropic API key.") {
		t.Error(got)
	}
	// The model and the client come from one snapshot: no client, no model either (Rust's `and_then`).
	if start := lastOf(evs, "start"); jsonv.Get(start, "model") != nil {
		t.Error(string(jsonv.Marshal(start)))
	}
}

func TestChildrenRunInParallelWithinTheTurnsLimit(t *testing.T) {
	api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string { return textReply("done", "end_turn") })
	e := apiEngine(t, url)
	var main engine.Profile
	var f1, f2 string
	e.Locked(func() {
		main = e.Agent.NewProfile("")
		e.Agent.Main = main.ID
		e.Agent.Parallel, e.Agent.Mode = 3, "split"
		f1, _ = e.InsertRoot(engine.Object("type", "frame", "name", "One", "width", jsonv.Int(10), "height", jsonv.Int(10)))
		f2, _ = e.InsertRoot(engine.Object("type", "frame", "name", "Two", "width", jsonv.Int(10), "height", jsonv.Int(10)))
	})
	entry := func(prompt string, ids ...string) jsonv.Value {
		return engine.Object("prompt", prompt, "containerNodes", engine.Strings(ids))
	}
	cfg := func(entries ...jsonv.Value) jsonv.Value { return engine.Object("config", jsonv.NewArray(entries...)) }
	for _, c := range []struct {
		parent string
		config jsonv.Value
		want   string
	}{
		{main.ID, engine.Object("config", jsonv.NewArray()), "spawn_agents needs `config`"},
		{main.ID, cfg(entry("a", f1), entry("a", f1), entry("a", f1), entry("a", f1), entry("a", f1), entry("a", f1)), "at most 5 agents can be spawned at once"},
		{"agNobody", cfg(entry("a", f1)), "only the main agent can spawn agents"},
		{main.ID, cfg(entry("a", f1), entry("b", f2), entry("c", f2)), "2 more designers can be started this turn, but 3 were requested"},
		{main.ID, cfg(entry("a")), "each spawn_agents entry needs at least one container node id"},
		{main.ID, cfg(entry("a", "nope")), "container node not found: nope"},
	} {
		if out := SpawnChildren(e, c.parent, c.config); !out.Error || !strings.HasPrefix(out.Text, c.want) {
			t.Errorf("%s: %s", c.want, out.Text)
		}
	}
	out := SpawnChildren(e, main.ID, cfg(entry("Build the header", f1), entry("Build the footer", f2)))
	if out.Error || !strings.HasPrefix(out.Text, "Started 2 designers: ") {
		t.Fatal(out.Text)
	}
	waitIdle(t, e)
	if out := SpawnChildren(e, main.ID, cfg(entry("more", f1))); !strings.HasPrefix(out.Text, "0 more designers can be started this turn, but 1 were requested") {
		t.Error(out.Text)
	}
	starts := 0
	for _, ev := range agentEvents(e) {
		if jsonv.StrIs(jsonv.Get(ev, "kind"), "start") && jsonv.IsTrue(jsonv.Get(ev, "child")) {
			starts++
			if !jsonv.StrIs(jsonv.Get(jsonv.Get(ev, "agent"), "parent"), main.ID) {
				t.Error("a child names its parent")
			}
		}
	}
	if starts != 2 {
		t.Error(starts)
	}
	reqs := api.requests()
	if len(reqs) != 2 {
		t.Fatal(len(reqs))
	}
	for _, r := range reqs {
		for _, tool := range jsonv.Items(jsonv.Get(r.body, "tools")) {
			if name := jsonv.StrOr(jsonv.Get(tool, "name")); name == "get_editor_state" || name == "spawn_agents" {
				t.Error("a child gets no", name)
			}
		}
		user := jsonv.StrOr(jsonv.Get(jsonv.Items(jsonv.Get(jsonv.Items(jsonv.Get(r.body, "messages"))[0], "content"))[0], "text"))
		if !strings.HasPrefix(user, "Design the following task inside the node(s) `") || !strings.Contains(user, "Document variables: ") {
			t.Error(user)
		}
	}
}

func TestPermissionCardsWaitForTheUser(t *testing.T) {
	e := newEngine(t)
	dir := t.TempDir()
	var p engine.Profile
	e.Locked(func() {
		p = e.Agent.NewProfile("")
		e.Doc.Path = filepath.Join(dir, "site.pen")
	})
	args := engine.Object("tool_name", "Bash", "input", engine.Object("command", "ls"))
	ask := func() chan engine.ToolOut {
		out := make(chan engine.ToolOut, 1)
		go func() { out <- AskPermission(e, p.ID, args) }()
		return out
	}
	// waiting is the id of the card once it shows.
	waiting := func() string {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var id string
			e.Locked(func() {
				for k := range e.Agent.Permissions {
					id = k
				}
			})
			if id != "" {
				return id
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("no permission card")
		return ""
	}
	for _, c := range []struct{ answer, want string }{
		{"allow", `{"behavior":"allow","updatedInput":{"command":"ls"}}`},
		{"deny", `{"behavior":"deny","message":"The user denied permission for this tool call."}`},
		{"auto", `{"behavior":"allow","updatedInput":{"command":"ls"}}`},
	} {
		out := ask()
		id := waiting()
		if err := AnswerPermission(e, id, c.answer); err != nil {
			t.Fatal(err)
		}
		if got := (<-out).Text; got != c.want {
			t.Errorf("%s: %s", c.answer, got)
		}
		if err := AnswerPermission(e, id, "allow"); err == nil || err.Error() != "This permission request is no longer waiting." {
			t.Error(err)
		}
	}
	if err := AnswerPermission(e, "pm1", "maybe"); err == nil || err.Error() != `Unknown permission answer "maybe"; use allow, deny or auto.` {
		t.Error(err)
	}
	e.Locked(func() {
		if !jsonv.IsTrue(jsonv.Get(e.Settings, "agent_auto_permissions")) {
			t.Error("auto is kept as a setting")
		}
	})
	if b, _ := os.ReadFile(filepath.Join(e.DataDir, "settings.json")); !strings.Contains(string(b), `"agent_auto_permissions": true`) {
		t.Error("and saved")
	}
	evs := agentEvents(e)
	if n := count(kinds(evs), "permission"); n != 3 || count(kinds(evs), "permission_done") != 3 {
		t.Error(kinds(evs))
	}
	if got := string(jsonv.Marshal(lastOf(evs, "permission"))); !strings.Contains(got, `"tool":"Bash","input":{"command":"ls"}`) {
		t.Error(got)
	}
	// Stop denies a waiting card, and a stopped run asks no more.
	out := ask()
	waiting()
	Stop(e)
	if got := (<-out).Text; !strings.Contains(got, `"behavior":"deny"`) {
		t.Error(got)
	}
	if got := AskPermission(e, p.ID, args).Text; got != `{"behavior":"deny","message":"The agent was stopped."}` {
		t.Error(got)
	}
	// A file inside the document's folder needs no card; an unknown agent is denied.
	inside := engine.Object("tool_name", "Write", "input", engine.Object("file_path", filepath.Join(dir, "img", "a.png")))
	if got := AskPermission(e, p.ID, inside).Text; !strings.HasPrefix(got, `{"behavior":"allow"`) {
		t.Error(got)
	}
	if got := AskPermission(e, "agNobody", args).Text; got != `{"behavior":"deny","message":"Unknown agent."}` {
		t.Error(got)
	}
}

func TestSavedChatsListLoadDeleteAndClear(t *testing.T) {
	e := newEngine(t)
	dir := filepath.Join(e.DataDir, "sessions")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o666); err != nil {
			t.Fatal(err)
		}
		return p
	}
	agent := `{"id":"ag1abc","name":"Bayu","color":"#4FD1C5","parent":null}`
	write("a1.json", `{"version":1,"id":"a1","title":"A","updatedAt":1,"agent":`+agent+`,"events":[{"kind":"user","agent":`+agent+`,"text":"hi"}],"messages":[],"session":"s1"}`)
	write("b2.json", `{"version":1,"id":"b2","title":"B","updatedAt":2,"agent":null,"events":[],"messages":[],"session":null}`)
	write("c3.json", `{"title":`)
	write("bad-id.json", `{"title":"never listed"}`)
	old := write("d4.json", `{"title":"old","updatedAt":3}`)
	past := time.Now().Add(-chatKeep - time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	if got := string(jsonv.Marshal(Chats(e))); got != `{"current":null,"chats":[{"id":"b2","title":"B","updatedAt":2},{"id":"a1","title":"A","updatedAt":1}]}` {
		t.Error(got)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("a chat untouched for 20 days is swept")
	}
	for _, c := range []struct{ id, want string }{
		{"../settings", "invalid chat id: ../settings"},
		{"c3", "Unable to open this chat: its file is damaged. Pick another chat or start a new one."},
		{"zz9", "Unable to open this chat: its file was moved or deleted. Pick another chat or start a new one."},
	} {
		if _, err := LoadChat(e, c.id); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v", c.id, err)
		}
	}
	if err := DeleteChat(e, "a.b"); err == nil || err.Error() != "invalid chat id: a.b" {
		t.Error(err)
	}

	loaded, err := LoadChat(e, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(jsonv.Marshal(loaded)); got != `{"id":"a1","title":"A","events":[{"kind":"user","agent":`+agent+`,"text":"hi"}]}` {
		t.Error(got)
	}
	e.Locked(func() {
		if e.Agent.Session != "s1" || e.Agent.Main != "ag1abc" || e.Agent.Chat.ID != "a1" {
			t.Error(e.Agent.Session, e.Agent.Main)
		}
		e.Agent.Running = true
	})
	if _, err := LoadChat(e, "b2"); err == nil || err.Error() != "Stop the agent before opening another chat." {
		t.Error(err)
	}
	if err := DeleteChat(e, "a1"); err == nil || err.Error() != "Stop the agent before removing the running chat." {
		t.Error(err)
	}
	if err := Reset(e); err == nil || err.Error() != "Stop the agent before starting a new conversation." {
		t.Error(err)
	}
	// A running chat survives a clear.
	if cleared, err := ClearChats(e); err != nil || cleared {
		t.Error(cleared, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a1.json")); err != nil {
		t.Error("the running chat's file stays")
	}
	if _, err := os.Stat(filepath.Join(dir, "b2.json")); err == nil {
		t.Error("the others go")
	}
	e.Locked(func() { e.Agent.Running = false })
	if err := DeleteChat(e, "a1"); err != nil {
		t.Fatal(err)
	}
	e.Locked(func() {
		if e.Agent.Chat != nil || e.Agent.Session != "" {
			t.Error("deleting the current chat starts a new conversation")
		}
	})
	if err := DeleteChat(e, "a1"); err != nil {
		t.Error("a chat already gone deletes fine:", err)
	}

	// The saved bytes keep Rust's key order.
	e.Locked(func() {
		e.Agent.Chat = &engine.Chat{ID: "e5", Title: "T"}
		if err := saveChat(e); err != nil {
			t.Fatal(err)
		}
	})
	if err := flushChatSaves(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "e5.json"))
	if s := string(b); !strings.HasPrefix(s, `{"version":1,"id":"e5","title":"T","updatedAt":`) || !strings.HasSuffix(s, `,"agent":null,"events":[],"messages":[],"session":null}`) {
		t.Error(s)
	}
	if cleared, err := ClearChats(e); err != nil || !cleared {
		t.Error(cleared, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Error("everything goes", entries)
	}
	e.Locked(func() {
		if e.Agent.Chat != nil {
			t.Error("the current chat went too")
		}
	})
}

// A permission id drawn twice replaces the older request, which is denied instead of waiting forever.
func TestARepeatedPermissionIDDeniesTheOlderRequest(t *testing.T) {
	e := newEngine(t)
	var p engine.Profile
	e.Locked(func() { p = e.Agent.NewProfile("") })
	args := engine.Object("tool_name", "Bash", "input", engine.Object("command", "ls"))
	pending := func() int {
		n := 0
		e.Locked(func() { n = len(e.Agent.Permissions) })
		return n
	}
	first := make(chan engine.ToolOut, 1)
	doc.Reseed(42)
	go func() { first <- AskPermission(e, p.ID, args) }()
	for deadline := time.Now().Add(10 * time.Second); pending() == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no permission card")
		}
	}
	second := make(chan engine.ToolOut, 1)
	doc.Reseed(42)
	go func() { second <- AskPermission(e, p.ID, args) }()
	select {
	case out := <-first:
		if !strings.Contains(out.Text, `"behavior":"deny"`) {
			t.Error(out.Text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the replaced request still waits")
	}
	Stop(e)
	<-second
}
