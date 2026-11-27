package agent

// The claude_code.rs tests (with Rust's stand-in CLI, a .cmd file on Windows), then Claude Code runs against a
// stand-in `claude` that is this test binary itself: with NALARUPA_FAKE_CLAUDE set it logs its arguments and stdin
// and prints stream-json (a batch file cannot take the multi-line system prompt as an argument).

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("NALARUPA_FAKE_CLAUDE"); mode != "" {
		os.Exit(fakeClaude(mode))
	}
	os.Exit(m.Run())
}

// fakeClaude is the stand-in CLI: it appends {args, stdin} to NALARUPA_FAKE_LOG and answers as mode says.
func fakeClaude(mode string) int {
	stdin, _ := io.ReadAll(os.Stdin)
	args := os.Args[1:]
	if f, err := os.OpenFile(os.Getenv("NALARUPA_FAKE_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666); err == nil {
		f.Write(append(jsonv.Marshal(engine.Object("args", engine.Strings(args), "stdin", string(stdin))), '\n'))
		f.Close()
	}
	out := func(lines ...string) {
		for _, l := range lines {
			fmt.Println(l)
		}
	}
	resumed := slices.Contains(args, "--resume")
	switch {
	case mode == "stale" && resumed:
		out(`{"type":"result","is_error":true,"result":"No conversation found with session ID: old"}`)
		return 1
	case mode == "signedout":
		out(`{"type":"result","is_error":true,"result":"Not logged in · Please run /login"}`)
		fmt.Fprintln(os.Stderr, "Error: Not logged in")
		return 1
	case mode == "stderr":
		fmt.Fprintln(os.Stderr, "boom")
		return 3
	case mode == "pen":
		// The AI Pen's CLI draws the model it got.
		m := ""
		if i := slices.Index(args, "--model"); i >= 0 && i+1 < len(args) {
			m = args[i+1]
		}
		out(string(jsonv.Marshal(engine.Object("type", "stream_event", "event", engine.Object("delta", engine.Object("type", "text_delta", "text", "model="+m))))))
		return 0
	case mode == "install":
		return 0
	case mode == "install-exists":
		fmt.Fprintln(os.Stderr, "MCP server nalarupa already exists in user config")
		return 1
	case mode == "install-fail":
		fmt.Fprintln(os.Stderr, "boom: not logged in")
		return 1
	case mode == "hang":
		out(`{"type":"system","subtype":"init","session_id":"s-hang"}`)
		time.Sleep(time.Minute)
		return 0
	case mode == "ts-mcp":
		if i := slices.Index(args, "--mcp-config"); i >= 0 && i+1 < len(args) {
			if cfgRaw, err := os.ReadFile(args[i+1]); err == nil {
				if cfgVal, err := jsonv.Parse(cfgRaw); err == nil {
					mcpURL := jsonv.StrOr(jsonv.Get(jsonv.Get(jsonv.Get(cfgVal, "mcpServers"), "nalarupa"), "url"))
					js := `s = Insert(document, {type:"frame", name:"Analytics Screen", x:0, y:0, width:1280, height:800, fill:"#FAFAFA", layout:"vertical", padding:32}); Insert(s, {type:"text", name:"Heading", content:"Analytics", fontSize:42, fill:"#111111"});`
					if resumed {
						js = `Update(s, {padding:64, gap:24}); Insert(s, {type:"rectangle", name:"PrimaryCard", width:400, height:120, fill:"#0F766E"});`
					}
					rpcReq := engine.Object(
						"jsonrpc", "2.0",
						"id", jsonv.Int(1),
						"method", "tools/call",
						"params", engine.Object("name", "execute", "arguments", engine.Object("input", js)),
					)
					mcpClient := &http.Client{Timeout: 5 * time.Second}
					resp, err := mcpClient.Post(mcpURL, "application/json", bytes.NewReader(jsonv.Marshal(rpcReq)))
					if err == nil {
						_, _ = io.ReadAll(resp.Body)
						resp.Body.Close()
					}
				}
			}
		}
		out(`{"type":"system","subtype":"init","session_id":"s-ts"}`,
			`{"type":"stream_event","event":{"type":"message_start"}}`,
			`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done."}}}`,
			`{"type":"stream_event","event":{"type":"message_stop"}}`,
			`{"type":"result","is_error":false,"num_turns":1,"total_cost_usd":0.10,"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`)
		return 0
	}
	out(`{"type":"system","subtype":"init","session_id":"s-new"}`,
		`{"type":"stream_event","event":{"type":"message_start"}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"mcp__nalarupa__execute"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"input\": \"x = 1\\n"}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"TodoWrite","input":{"todos":[{"content":"a"}]}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"ok"}]}]}}`)
	os.Stdout.Write([]byte("\xff not json\n"))
	out(`{"type":"result","is_error":false,"num_turns":2,"total_cost_usd":0.5,"usage":{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`)
	return 0
}

// ccEngine is an engine whose agent runs on the stand-in CLI in mode; the log of its runs is returned too.
func ccEngine(t *testing.T, mode string) (*engine.Engine, string) {
	t.Helper()
	e := newEngine(t)
	log := filepath.Join(t.TempDir(), "runs.jsonl")
	t.Setenv("NALARUPA_FAKE_CLAUDE", mode)
	t.Setenv("NALARUPA_FAKE_LOG", log)
	old := claudeProgram
	claudeProgram = os.Args[0]
	t.Cleanup(func() { claudeProgram = old })
	e.Locked(func() {
		e.MCPPort = 9
		e.Settings.Set("agent_backend", "claude-code")
	})
	return e, log
}

// runs are the stand-in's logged invocations.
func runs(t *testing.T, log string) []jsonv.Value {
	t.Helper()
	b, _ := os.ReadFile(log)
	var out []jsonv.Value
	for _, l := range lines(string(b)) {
		out = append(out, jsonv.MustParse(l))
	}
	return out
}

func argsOf(run jsonv.Value) []string {
	var out []string
	for _, a := range jsonv.Items(jsonv.Get(run, "args")) {
		out = append(out, jsonv.StrOr(a))
	}
	return out
}

func TestAClaudeCodeRunStreamsItsEventsAndResumesItsSession(t *testing.T) {
	e, log := ccEngine(t, "ok")
	e.Locked(func() {
		e.Settings.Set("agent_model", "claude-opus-5")
		e.Settings.Set("agent_fast", true)
		e.Settings.Set("agent_auto_permissions", true)
	})
	Send(e, "Hello", 1, "split", 1, "layout")
	waitIdle(t, e)
	evs := agentEvents(e)
	want := []string{"user", "start", "text_delta", "tool_start", "message_end", "todos", "tool_done", "done", "all_done"}
	if got := withoutProgress(kinds(evs)); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if got := string(jsonv.Marshal(lastOf(evs, "start"))); !strings.Contains(got, `"child":false,"backend":"claude-code","model":"claude-opus-5"`) {
		t.Error(got)
	}
	if got := string(jsonv.Marshal(jsonv.Get(lastOf(evs, "done"), "usage"))); got != `{"input":3,"output":4,"cacheRead":0,"cacheWrite":0,"turns":2,"costUsd":0.5}` {
		t.Error(got)
	}
	if got := string(jsonv.Marshal(lastOf(evs, "tool_done"))); !strings.Contains(got, `"id":"t2","name":"TodoWrite","input":{"todos":[{"content":"a"}]},"output":"ok"`) {
		t.Error(got)
	}
	if got := lastOf(evs, "tool_start"); !jsonv.StrIs(jsonv.Get(got, "name"), "execute") {
		t.Error("MCP tools are shown by their short name", got)
	}
	e.Locked(func() {
		if e.Agent.Session != "s-new" {
			t.Error(e.Agent.Session)
		}
	})
	r := runs(t, log)
	if len(r) != 1 {
		t.Fatal(len(r))
	}
	args := argsOf(r[0])
	cfg := ""
	if i := slices.Index(args, "--mcp-config"); i >= 0 && i+1 < len(args) {
		cfg = args[i+1]
	}
	want = []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--strict-mcp-config", "--mcp-config", cfg,
		"--allowedTools", "mcp__nalarupa,WebSearch,WebFetch,Skill", "--disallowedTools", "Task,Agent,AskUserQuestion",
		"--permission-prompt-tool", "mcp__nalarupa__approve", "--append-system-prompt", Instructions("claude-code"),
		"--permission-mode", "auto", "--model", "claude-opus-5", "--settings", `{"fastMode":true}`}
	if !slices.Equal(args, want) {
		t.Errorf("argv\n%q\nwant\n%q", args, want)
	}
	if !strings.HasPrefix(filepath.Base(cfg), "claude-code-mcp-ag") {
		t.Error(cfg)
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Error("the run's MCP config goes with it")
	}
	if stdin := jsonv.StrOr(jsonv.Get(r[0], "stdin")); !strings.HasPrefix(stdin, "Hello"+editorStateNote) {
		t.Error(stdin)
	}

	Send(e, "Again", 1, "split", 1, "layout")
	waitIdle(t, e)
	r = runs(t, log)
	if args := argsOf(r[len(r)-1]); !slices.Contains(args, "--resume") || args[slices.Index(args, "--resume")+1] != "s-new" {
		t.Error("the next prompt resumes the session", args)
	}
}

func TestAMissingSessionIsRetriedOnceAsANewOne(t *testing.T) {
	e, log := ccEngine(t, "stale")
	e.Locked(func() { e.Agent.Session = "old" })
	Send(e, "Hello", 1, "split", 1, "layout")
	waitIdle(t, e)
	r := runs(t, log)
	if len(r) != 2 || !slices.Contains(argsOf(r[0]), "--resume") || slices.Contains(argsOf(r[1]), "--resume") {
		t.Fatal(r)
	}
	evs := agentEvents(e)
	if lastOf(evs, "note") == nil || lastOf(evs, "error") != nil {
		t.Error(kinds(evs))
	}
	e.Locked(func() {
		if e.Agent.Session != "s-new" {
			t.Error(e.Agent.Session)
		}
	})
}

func TestASignedOutCLIShowsOneSignInCard(t *testing.T) {
	e, _ := ccEngine(t, "signedout")
	Send(e, "Hello", 1, "split", 1, "layout")
	waitIdle(t, e)
	evs := agentEvents(e)
	if n := count(kinds(evs), "error"); n != 1 {
		t.Fatal("one card", kinds(evs))
	}
	if got := lastOf(evs, "error"); !jsonv.StrIs(jsonv.Get(got, "text"), SignedOut) || !jsonv.IsTrue(jsonv.Get(got, "signIn")) {
		t.Error(string(jsonv.Marshal(got)))
	}
}

func TestAFailedCLIShowsItsExitAndStderr(t *testing.T) {
	e, _ := ccEngine(t, "stderr")
	Send(e, "Hello", 1, "split", 1, "layout")
	waitIdle(t, e)
	want := "claude exited with exit status: 3: boom"
	if runtime.GOOS == "windows" {
		want = "claude exited with exit code: 3: boom"
	}
	if got := jsonv.StrOr(jsonv.Get(lastOf(agentEvents(e), "error"), "text")); got != want {
		t.Error(got)
	}
}

func TestStopKillsTheCLI(t *testing.T) {
	e, _ := ccEngine(t, "hang")
	Send(e, "Hello", 1, "split", 1, "layout")
	deadline := time.Now().Add(20 * time.Second)
	for {
		var s string
		e.Locked(func() { s = e.Agent.Session })
		if s == "s-hang" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the CLI never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	began := time.Now()
	Stop(e)
	waitIdle(t, e)
	if time.Since(began) > 5*time.Second {
		t.Error("the kill took", time.Since(began))
	}
	evs := agentEvents(e)
	if !jsonv.IsTrue(jsonv.Get(lastOf(evs, "done"), "stopped")) || lastOf(evs, "error") != nil {
		t.Error(kinds(evs))
	}
}

func TestAMissingCLIOrMCPServerFailsTheTurn(t *testing.T) {
	e, _ := ccEngine(t, "ok")
	claudeProgram = "nalarupa-no-such-cli"
	Send(e, "Hello", 1, "split", 1, "layout")
	waitIdle(t, e)
	notFound := "program not found"
	if runtime.GOOS != "windows" {
		notFound = "No such file or directory (os error 2)"
	}
	if got := jsonv.StrOr(jsonv.Get(lastOf(agentEvents(e), "error"), "text")); got != "could not start the `claude` CLI ("+notFound+"). Install Claude Code or switch the backend to the Claude API." {
		t.Error(got)
	}
	if entries, _ := filepath.Glob(filepath.Join(e.DataDir, "claude-code-mcp-*")); len(entries) != 0 {
		t.Error("a failed start leaves no config", entries)
	}
	e.Locked(func() { e.MCPPort = 0 })
	Send(e, "Hello", 1, "split", 1, "layout")
	waitIdle(t, e)
	if got := jsonv.StrOr(jsonv.Get(lastOf(agentEvents(e), "error"), "text")); got != "the MCP server is not running" {
		t.Error(got)
	}
}

func TestTheCLIStatusSaysHowClaudeCodeIsSignedIn(t *testing.T) {
	same := func(got jsonv.Value, want string) {
		t.Helper()
		if !jsonv.Equal(got, jsonv.MustParse(want)) {
			t.Errorf("%s, want %s", jsonv.Marshal(got), want)
		}
	}
	same(parseStatus(`{"loggedIn": true, "authMethod": "claude.ai", "email": "a@b.co", "subscriptionType": "max"}`, true),
		`{"installed": true, "signedIn": true, "method": "claude.ai", "account": "a@b.co", "plan": "max"}`)
	if got := jsonv.Get(parseStatus(`{"loggedIn": true, "authMethod": "api_key"}`, true), "method"); !jsonv.StrIs(got, "api_key") {
		t.Error(got)
	}
	same(parseStatus(`{"loggedIn": false, "authMethod": "none"}`, false), `{"installed": true, "signedIn": false, "method": null, "account": null, "plan": null}`)
	if jsonv.IsTrue(jsonv.Get(parseStatus("Not logged in", false), "signedIn")) {
		t.Error("a CLI that prints text is read by its exit code")
	}
	if !jsonv.IsFalse(jsonv.Get(statusOf("nalarupa-no-such-cli"), "installed")) {
		t.Error("not installed")
	}
}

// standIn is a stand-in CLI whose `auth status` reads a flag file, whose `auth login` sets it after a moment and
// records its arguments, and whose `auth logout` clears it. A shell script, or a .cmd file on Windows.
func standIn(t *testing.T, dir string) string {
	t.Helper()
	flag, args := filepath.Join(dir, "signed-in"), filepath.Join(dir, "login-args")
	var path, body string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, "claude.cmd")
		body = fmt.Sprintf("@echo off\r\nif \"%%2\"==\"status\" (if exist \"%[1]s\" (echo {\"loggedIn\": true, \"authMethod\": \"claude.ai\"}) else (echo {\"loggedIn\": false}& exit /b 1))\r\nif \"%%2\"==\"login\" (echo %%* > \"%[2]s\"& ping -n 2 127.0.0.1 > nul & type nul > \"%[1]s\")\r\n", flag, args)
	} else {
		path = filepath.Join(dir, "claude")
		body = fmt.Sprintf("#!/bin/sh\ncase \"$2\" in\n  status) [ -f '%[1]s' ] || { echo '{\"loggedIn\": false}'; exit 1; }; echo '{\"loggedIn\": true, \"authMethod\": \"claude.ai\"}' ;;\n  login) echo \"$@\" > '%[2]s'; sleep 1; touch '%[1]s' ;;\nesac\n", flag, args)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestASignInRunsTheCLIFlowReportsOnceAndCanBeCancelled(t *testing.T) {
	dir := t.TempDir()
	cli := standIn(t, dir)
	if jsonv.IsTrue(jsonv.Get(statusOf(cli), "signedIn")) {
		t.Fatal("signed out at first")
	}
	// A cancelled sign-in reports nothing; the next one (Console) completes and reports exactly once.
	reports := make(chan jsonv.Value, 4)
	if err := loginWith(cli, false, func(s jsonv.Value) { reports <- s }); err != nil {
		t.Fatal(err)
	}
	CancelLogin()
	if err := loginWith(cli, true, func(s jsonv.Value) { reports <- s }); err != nil {
		t.Fatal(err)
	}
	select {
	case state := <-reports:
		if !jsonv.IsTrue(jsonv.Get(state, "signedIn")) {
			t.Error(string(jsonv.Marshal(state)))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the sign-in reports when the CLI flow ends")
	}
	select {
	case state := <-reports:
		t.Error("the cancelled sign-in does not report", string(jsonv.Marshal(state)))
	case <-time.After(800 * time.Millisecond):
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "login-args")); !strings.Contains(string(b), "--console") {
		t.Error("Console sign-in passes --console:", string(b))
	}
}

// On Windows `claude` is found as `claude.exe` only, as Rust's `Command` finds it: a `claude.cmd` shim on PATH is
// not run, and an explicit path is used as is.
func TestTheCLIIsFoundAsAnExeOnly(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows lookup")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude.cmd"), []byte("@echo {\"loggedIn\": true, \"authMethod\": \"claude.ai\"}\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if cmd := command("claude", "auth", "status"); cmd.Err == nil {
		t.Error("a .cmd shim is not the CLI:", cmd.Path)
	}
	if !jsonv.IsFalse(jsonv.Get(statusOf("claude"), "installed")) {
		t.Error("with only a shim, Claude Code is not installed")
	}
	exe := filepath.Join(dir, "claude.exe")
	src, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, src, 0o755); err != nil {
		t.Fatal(err)
	}
	if cmd := command("claude"); cmd.Err != nil || !strings.EqualFold(cmd.Path, exe) {
		t.Error(cmd.Path, cmd.Err)
	}
	if cmd := command(filepath.Join(dir, "claude.cmd")); cmd.Err != nil {
		t.Error("an explicit path is used as is:", cmd.Err)
	}
}

func TestAClaudeCodeRunPassesConfiguredEffort(t *testing.T) {
	e, log := ccEngine(t, "ok")
	e.Locked(func() {
		e.Settings.Set("agent_effort", "xhigh")
	})
	Send(e, "Hello", 1, "split", 1, "layout")
	waitIdle(t, e)
	r := runs(t, log)
	if len(r) != 1 {
		t.Fatal(len(r))
	}
	args := argsOf(r[0])
	idx := slices.Index(args, "--effort")
	if idx < 0 || idx+1 >= len(args) || args[idx+1] != "xhigh" {
		t.Fatalf("expected --effort xhigh in argv, got %q", args)
	}
}

func TestAClaudeCodeRunActivatesMultipleSkillsAndAddsDirs(t *testing.T) {
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	dirA := writeSkillFixture(t, skillsRoot, "alpha-skill", "---\nname: alpha-skill\ndescription: First skill\n---\nAlpha rule.")
	dirB := writeSkillFixture(t, skillsRoot, "beta-skill", "---\nname: beta-skill\ndescription: Second skill\n---\nBeta rule.")
	t.Setenv("NALARUPA_SKILLS_ONLY", "1")
	t.Setenv("NALARUPA_SKILLS_DIR", skillsRoot)

	e, log := ccEngine(t, "ok")
	Send(e, "/alpha-skill /beta-skill Build clean dashboard", 1, "split", 1, "layout")
	waitIdle(t, e)
	r := runs(t, log)
	if len(r) != 1 {
		t.Fatal(len(r))
	}
	args := argsOf(r[0])
	if i := slices.Index(args, "--allowedTools"); i < 0 || !strings.Contains(args[i+1], "Skill") {
		t.Fatalf("expected Skill in --allowedTools, got %q", args)
	}
	var addedDirs []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--add-dir" {
			addedDirs = append(addedDirs, args[i+1])
		}
	}
	if !slices.Contains(addedDirs, filepath.Clean(dirA)) || !slices.Contains(addedDirs, filepath.Clean(dirB)) {
		t.Fatalf("expected --add-dir for %q and %q, got %q", dirA, dirB, addedDirs)
	}
	stdin := jsonv.StrOr(jsonv.Get(r[0], "stdin"))
	if !strings.Contains(stdin, "Alpha rule.") || !strings.Contains(stdin, "Beta rule.") {
		t.Fatalf("expected stdin to expand both skills, got:\n%s", stdin)
	}
}
