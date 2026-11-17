package agent

// The mcp.rs test, then the server over HTTP: routing, the Origin guard, JSON-RPC, tool lists, calls, the
// permission tool, the port range, and `install` against a throwaway home and a stand-in `claude`.

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func TestOriginGuardMatchesExactLocalHosts(t *testing.T) {
	for _, ok := range []string{"http://localhost", "http://localhost:5173", "http://127.0.0.1:47700", "http://[::1]", "http://[::1]:3000"} {
		if !localOrigin(ok) {
			t.Error(ok)
		}
	}
	for _, bad := range []string{"null", "", "https://localhost", "http://localhost.evil.com", "http://localhostevil.com", "http://127.0.0.1.evil.com",
		"http://127.0.0.10", "http://localhost:", "http://localhost:80a", "http://localhost/x", "http://localhost@evil.com",
		"http://[::1]x", "http://[::1", "http://evil.com", "http://localhost:80:80"} {
		if localOrigin(bad) {
			t.Error(bad)
		}
	}
}

// mcpEngine is an engine with its MCP server running; the server's base URL is returned too.
func mcpEngine(t *testing.T) (*engine.Engine, string) {
	t.Helper()
	e := newEngine(t)
	port, srv, err := listenMCP(e)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	e.Locked(func() { e.MCPPort = port })
	return e, fmt.Sprintf("http://127.0.0.1:%d", port)
}

type reply struct {
	status int
	header http.Header
	body   string
}

func post(t *testing.T, url, body string, header ...string) reply {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return reply{res.StatusCode, res.Header, string(b)}
}

func TestTheMCPServerSpeaksJSONRPCOverHTTP(t *testing.T) {
	e, base := mcpEngine(t)
	mcp := base + "/mcp"
	for _, c := range []struct {
		url, body string
		header    []string
		status    int
		want      string
	}{
		{base + "/other", "{}", nil, 404, "not found"},
		{base + "/mcpx", "{}", nil, 404, "not found"},
		{mcp, "{}", []string{"Origin", "https://evil.example"}, 403, "forbidden origin"},
		{mcp, "{}", []string{"Origin", "null"}, 403, "forbidden origin"},
		{mcp, "{}", []string{"Origin", "http://localhost:5173"}, 202, ""},
		{mcp + "?x=1", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil, 202, ""},
		{mcp, "\xff", nil, 400, "bad body"},
		{mcp, "{not json", nil, 200, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error: key must be a string at line 1 column 2"}}`},
		{mcp, `{"jsonrpc":"2.0","id":9,"method":"nope"}`, nil, 200, `{"jsonrpc":"2.0","id":9,"error":{"code":-32601,"message":"method not found: nope"}}`},
		{mcp, `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/x"},5,{"jsonrpc":"2.0","id":"2","method":"ping"}]`, nil, 200,
			`[{"jsonrpc":"2.0","id":1,"result":{}},{"jsonrpc":"2.0","id":"2","result":{}}]`},
		{mcp, `[{"jsonrpc":"2.0","method":"notifications/x"}]`, nil, 202, ""},
		{mcp, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_editor_state","arguments":[]}}`, nil, 200,
			`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid params: arguments must be an object"}}`},
		{mcp, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`, nil, 200,
			`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{"listChanged":false}},"serverInfo":{"name":"nalarupa","version":"0.1.0"},"instructions":` +
				string(jsonv.Marshal(mcpInstructions)) + `}}`},
		{mcp, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"spawn_agents","arguments":{"agents":[]}}}`, nil, 200,
			`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"spawn_agents is only available to Nalarupa's built-in agents"}],"isError":true}}`},
	} {
		got := post(t, c.url, c.body, c.header...)
		if got.status != c.status || got.body != c.want {
			t.Errorf("%s %s:\n got %d %s\nwant %d %s", c.url, c.body, got.status, got.body, c.status, c.want)
		}
		if c.status == 200 && got.header.Get("Content-Type") != "application/json" {
			t.Errorf("%s: content type %q", c.body, got.header.Get("Content-Type"))
		}
	}
	res, err := http.Get(mcp)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 405 || res.Header.Get("Allow") != "POST" {
		t.Errorf("GET: %d %q", res.StatusCode, res.Header.Get("Allow"))
	}

	// Tool lists: external agents get the public tools; a built-in agent also gets the permission tool, a child
	// loses get_editor_state, and only a main agent of a parallel turn gets spawn_agents.
	names := func(path string) string {
		v := jsonv.MustParse(post(t, base+path, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`).body)
		var out []string
		for _, tool := range jsonv.Items(jsonv.Get(jsonv.Get(v, "result"), "tools")) {
			out = append(out, jsonv.StrOr(jsonv.Get(tool, "name")))
		}
		return strings.Join(out, ",")
	}
	e.Locked(func() {
		e.Agent.Profiles = append(e.Agent.Profiles, engine.Profile{ID: "main"}, engine.Profile{ID: "kid", Parent: "main"})
		e.Agent.Parallel = 3
	})
	for path, want := range map[string]string{
		"/mcp":      "get_editor_state,read_guide,get_style,execute,open_document",
		"/mcp/ag1":  "get_editor_state,read_guide,get_style,execute,open_document,approve",
		"/mcp/main": "get_editor_state,read_guide,get_style,execute,open_document,spawn_agents,approve",
		"/mcp/kid":  "read_guide,get_style,execute,open_document,approve",
	} {
		if got := names(path); got != want {
			t.Errorf("%s: %s", path, got)
		}
	}

	// A call runs the tool as its agent; an external one is logged in the chat once it is over.
	e.Locked(func() { e.TakeEvents() })
	call := func(path, name, args string) jsonv.Value {
		return jsonv.Get(jsonv.MustParse(post(t, base+path, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, args)).body), "result")
	}
	ok := call("/mcp", "execute", `{"input": "Insert(document, {type: \"rectangle\", name: \"McpRect\", width: 10, height: 10})"}`)
	if jsonv.IsTrue(jsonv.Get(ok, "isError")) || !strings.Contains(jsonv.StrOr(jsonv.Get(jsonv.Items(jsonv.Get(ok, "content"))[0], "text")), "McpRect") {
		t.Errorf("execute: %s", jsonv.Marshal(ok))
	}
	if bad := call("/mcp", "no_such_tool", "null"); !jsonv.IsTrue(jsonv.Get(bad, "isError")) {
		t.Errorf("unknown tool: %s", jsonv.Marshal(bad))
	}
	call("/mcp/agA", "execute", `{"input": "page = 1"}`)
	var logged []string
	e.Locked(func() {
		for _, ev := range e.Events {
			if jsonv.StrIs(jsonv.Get(ev, "kind"), "external") {
				logged = append(logged, fmt.Sprintf("%s:%v", jsonv.StrOr(jsonv.Get(ev, "text")), jsonv.Get(ev, "error")))
			}
		}
		if _, ok := e.Script.Globals["agA"]; !ok {
			t.Error("agA's call ran as agA")
		}
	})
	if strings.Join(logged, ",") != "execute:false,no_such_tool:true" {
		t.Errorf("logged %v", logged)
	}
}

// `approve` blocks only its own request until the user answers; Stop denies it.
func TestApproveWaitsForTheUser(t *testing.T) {
	e, base := mcpEngine(t)
	e.Locked(func() {
		e.Agent.Profiles = append(e.Agent.Profiles, engine.Profile{ID: "ag1", Name: "A", Color: "#000"})
	})
	ask := func() chan string {
		out := make(chan string, 1)
		go func() {
			res, err := http.Post(base+"/mcp/ag1", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":"Bash","input":{"command":"ls"}}}}`))
			if err != nil {
				out <- err.Error()
				return
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			out <- string(b)
		}()
		return out
	}
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
		t.Fatal("no permission request")
		return ""
	}
	answer := ask()
	id := waiting()
	// Meanwhile other requests are served.
	if got := post(t, base+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); got.status != 200 {
		t.Errorf("ping while waiting: %d", got.status)
	}
	select {
	case got := <-answer:
		t.Fatalf("answered before the user: %s", got)
	case <-time.After(100 * time.Millisecond):
	}
	if err := AnswerPermission(e, id, "allow"); err != nil {
		t.Fatal(err)
	}
	want := `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{\"behavior\":\"allow\",\"updatedInput\":{\"command\":\"ls\"}}"}],"isError":false}}`
	if got := <-answer; got != want {
		t.Errorf("allowed:\n got %s\nwant %s", got, want)
	}

	answer = ask()
	waiting()
	Stop(e)
	want = `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{\"behavior\":\"deny\",\"message\":\"The user denied permission for this tool call.\"}"}],"isError":false}}`
	if got := <-answer; got != want {
		t.Errorf("stopped:\n got %s\nwant %s", got, want)
	}
}

// The server takes the first free port of its 20; with none free it fails as Rust does. The test moves the range
// away from the app's, so it never takes the ports of a running app or of another package's tests.
func TestTheServerTakesTheFirstFreePort(t *testing.T) {
	old := firstMCPPort
	firstMCPPort = 47900
	t.Cleanup(func() { firstMCPPort = old })
	e := newEngine(t)
	var held []net.Listener
	defer func() {
		for _, l := range held {
			l.Close()
		}
	}()
	first := 0
	for p := firstMCPPort; p < firstMCPPort+20 && first == 0; p++ {
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			held, first = append(held, l), p
		}
	}
	if first == 0 {
		t.Skip("no free port in the range")
	}
	port, srv, err := listenMCP(e)
	if err != nil {
		if first == firstMCPPort+19 {
			return
		}
		t.Fatal(err)
	}
	defer srv.Close()
	if int(port) <= first || int(port) >= firstMCPPort+20 {
		t.Errorf("port %d with %d busy", port, first)
	}
	for p := firstMCPPort; p < firstMCPPort+20; p++ {
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			held = append(held, l)
		}
	}
	if _, err := StartMCP(e); err == nil || err.Error() != "no free port for the MCP server" {
		t.Errorf("all busy: %v", err)
	}
}

// `install` writes Cursor's and Codex's config files and registers the server with the Claude Code CLI, in a
// throwaway home (app/scripts/mcp-install-check.ts, which Windows skips, checks the same on the DLL).
func TestInstallWritesEachClientsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	url := MCPURL(47701)
	install := func(client string) (string, error) { return InstallMCP(client, 47701) }

	cursor := filepath.Join(home, ".cursor", "mcp.json")
	if got, err := install("cursor"); err != nil || got != "Cursor: wrote "+cursor {
		t.Errorf("cursor: %q %v", got, err)
	}
	if b, _ := os.ReadFile(cursor); string(b) != "{\n  \"mcpServers\": {\n    \"nalarupa\": {\n      \"url\": \""+url+"\"\n    }\n  }\n}" {
		t.Errorf("cursor file %q", b)
	}
	os.WriteFile(cursor, []byte(`{"theme":"dark","mcpServers":{"other":{"url":"http://example.test/mcp"},"nalarupa":{"url":"http://stale"}}}`), 0o666)
	install("cursor")
	if b, _ := os.ReadFile(cursor); string(jsonv.Marshal(jsonv.MustParse(string(b)))) != `{"theme":"dark","mcpServers":{"other":{"url":"http://example.test/mcp"},"nalarupa":{"url":"`+url+`"}}}` {
		t.Errorf("merged %s", b)
	}
	for body, want := range map[string]string{
		"[1,2]":                    cursor + " is not a JSON object; not touching it",
		"{ \"mcpServers\": {},\n}": cursor + " is not valid JSON (trailing comma at line 2 column 1); not touching it",
	} {
		os.WriteFile(cursor, []byte(body), 0o666)
		if _, err := install("cursor"); err == nil || err.Error() != want {
			t.Errorf("%q: %v", body, err)
		}
		if b, _ := os.ReadFile(cursor); string(b) != body {
			t.Errorf("the refused file changed: %q", b)
		}
	}

	codex := filepath.Join(home, ".codex", "config.toml")
	os.MkdirAll(filepath.Dir(codex), 0o777)
	os.WriteFile(codex, []byte("model = \"gpt-5\"\n"), 0o666)
	if got, err := install("codex"); err != nil || got != "Codex: wrote "+codex {
		t.Errorf("codex: %q %v", got, err)
	}
	toml := "model = \"gpt-5\"\n\n[mcp_servers.nalarupa]\nurl = \"" + url + "\"\n"
	if b, _ := os.ReadFile(codex); string(b) != toml {
		t.Errorf("codex file %q", b)
	}
	if got, _ := install("codex"); got != "Codex: already configured in "+codex {
		t.Errorf("again: %q", got)
	}

	log := filepath.Join(t.TempDir(), "runs.jsonl")
	t.Setenv("NALARUPA_FAKE_LOG", log)
	old := claudeProgram
	claudeProgram = os.Args[0]
	t.Cleanup(func() { claudeProgram = old })
	for mode, want := range map[string]string{"install": "", "install-exists": "", "install-fail": "boom: not logged in\n"} {
		t.Setenv("NALARUPA_FAKE_CLAUDE", mode)
		got, err := install("claude-code")
		if want == "" && (err != nil || got != "Claude Code: nalarupa → "+url) || want != "" && (err == nil || err.Error() != want) {
			t.Errorf("%s: %q %v", mode, got, err)
		}
	}
	if args := argsOf(runs(t, log)[0]); strings.Join(args, " ") != "mcp add --transport http --scope user nalarupa "+url {
		t.Errorf("claude args %q", args)
	}
	claudeProgram = "nalarupa-no-such-cli"
	if _, err := install("claude-code"); err == nil || !strings.HasPrefix(err.Error(), "could not run `claude` (") {
		t.Errorf("missing CLI: %v", err)
	}
	for _, c := range []string{"vscode", ""} {
		if _, err := install(c); err == nil || err.Error() != fmt.Sprintf("unknown client '%s' (claude-code, cursor, codex)", c) {
			t.Errorf("%q: %v", c, err)
		}
	}
}
