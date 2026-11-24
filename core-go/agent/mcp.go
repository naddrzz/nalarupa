package agent

// The MCP server over Streamable HTTP (mcp.rs; JSON responses, no SSE) on 127.0.0.1. External agents (Claude
// Code, Cursor, Codex…) call the same tools as the built-in agent. An HTTP endpoint served by the app itself needs
// no extra binary.

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/model"
)

// DefaultMCPPort is the first port the server tries; it takes the first free one of the 20 from here.
const DefaultMCPPort = 47700

// version is the core's version (Rust's CARGO_PKG_VERSION): build.sh sets it from app/mygo.json, which
// packaging/set-version.sh stamps for a release.
var version = "0.1.0"

const mcpInstructions = "Nalarupa is a desktop design editor (infinite canvas, .pen documents). " +
	"Call get_editor_state first, then read_guide before the first execute. Edit the canvas only through execute; " +
	"never edit .pen files on disk while the app has them open."

// firstMCPPort is DefaultMCPPort; tests move it to another range, so they never take the app's ports.
var firstMCPPort = DefaultMCPPort

// StartMCP starts the server for e on the first free port from DefaultMCPPort and returns the port. Each request
// is served on its own goroutine, so a waiting permission request blocks only itself.
func StartMCP(e *engine.Engine) (uint16, error) {
	port, _, err := listenMCP(e)
	return port, err
}

// listenMCP is StartMCP, also returning the server (tests close it).
func listenMCP(e *engine.Engine) (uint16, *http.Server, error) {
	for port := firstMCPPort; port < firstMCPPort+20; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveMCP(e, w, r) }),
			ErrorLog: log.New(io.Discard, "", 0)}
		go srv.Serve(ln)
		return uint16(port), srv, nil
	}
	return 0, nil, errors.New("no free port for the MCP server")
}

// MCPURL is the server's URL for external agents.
func MCPURL(port uint16) string { return fmt.Sprintf("http://127.0.0.1:%d/mcp", port) }

// localOrigin is true for `http://<localhost|127.0.0.1|[::1]>[:port]` exactly; `null`, other schemes and lookalike
// hosts are not local.
func localOrigin(origin string) bool {
	authority, ok := strings.CutPrefix(origin, "http://")
	if !ok {
		return false
	}
	split := len(authority)
	if strings.HasPrefix(authority, "[") {
		if i := strings.IndexByte(authority, ']'); i >= 0 {
			split = i + 1
		}
	} else if i := strings.IndexByte(authority, ':'); i >= 0 {
		split = i
	}
	host, port := authority[:split], authority[split:]
	portOK := port == ""
	if p, ok := strings.CutPrefix(port, ":"); ok && p != "" {
		portOK = strings.Trim(p, "0123456789") == ""
	}
	return portOK && (host == "localhost" || host == "127.0.0.1" || host == "[::1]")
}

// plain answers a status with a text body, as tiny_http's `Response::from_string` does.
func plain(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.WriteHeader(status)
	io.WriteString(w, text)
}

func serveMCP(e *engine.Engine, w http.ResponseWriter, r *http.Request) {
	defer func() {
		// Rust's request thread dies with the connection: drop it without a reply.
		if recover() != nil {
			panic(http.ErrAbortHandler)
		}
	}()
	// DNS-rebinding guard: browsers send Origin; only local pages may talk to us.
	if o := r.Header["Origin"]; len(o) > 0 && !localOrigin(o[0]) {
		plain(w, http.StatusForbidden, "forbidden origin")
		return
	}
	// `/mcp` for external agents, `/mcp/<agent-id>` for Nalarupa's own agents (attribution).
	path, _, _ := strings.Cut(r.RequestURI, "?")
	rest, ok := strings.CutPrefix(path, "/mcp")
	var agent string
	switch {
	case ok && (rest == "" || rest == "/"):
	case ok && len(rest) > 1 && rest[0] == '/':
		agent = rest[1:]
	default:
		plain(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		plain(w, http.StatusMethodNotAllowed, "")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024))
	if err != nil || !utf8.Valid(body) {
		plain(w, http.StatusBadRequest, "bad body")
		return
	}
	var reply jsonv.Value
	msg, err := jsonv.Parse(body)
	switch {
	case err != nil:
		reply = engine.Object("jsonrpc", "2.0", "id", nil, "error", engine.Object("code", jsonv.Int(-32700), "message", "parse error: "+err.Error()))
	case jsonv.Arr(msg) != nil:
		out := jsonv.NewArray()
		for _, m := range jsonv.Arr(msg).Items {
			if v := rpc(e, m, agent); v != nil {
				out.Items = append(out.Items, v)
			}
		}
		if len(out.Items) > 0 {
			reply = out
		}
	default:
		if v := rpc(e, msg, agent); v != nil {
			reply = v
		}
	}
	if reply == nil {
		plain(w, http.StatusAccepted, "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonv.Marshal(reply))
}

// toolsFor are the tools visible to a caller: children lose get_editor_state; only a main agent with parallel
// agents enabled gets spawn_agents.
func toolsFor(e *engine.Engine, agent string) jsonv.Value {
	var child, spawn bool
	e.Locked(func() {
		if p, ok := e.Agent.Profile(agent); ok && agent != "" {
			child = p.Parent != ""
			spawn = p.Parent == "" && e.Agent.Parallel > 1
		}
	})
	tools := jsonv.NewArray()
	for _, t := range jsonv.Items(engine.ToolSpecs()) {
		if !child || !jsonv.StrIs(jsonv.Get(t, "name"), "get_editor_state") {
			tools.Items = append(tools.Items, t)
		}
	}
	if spawn {
		s := SpawnSpec()
		tools.Items = append(tools.Items, engine.Object("name", jsonv.Get(s, "name"), "description", jsonv.Get(s, "description"), "inputSchema", jsonv.Get(s, "input_schema")))
	}
	if agent != "" {
		tools.Items = append(tools.Items, engine.Object("name", PermissionTool,
			"description", "Internal: Nalarupa answers Claude Code permission prompts with this tool. Never call it yourself.",
			"inputSchema", jsonv.MustParse(`{"type": "object", "properties": {"tool_name": {"type": "string"}, "input": {"type": "object"}, "tool_use_id": {"type": "string"}}}`)))
	}
	return tools
}

// rpc answers one JSON-RPC message from agent ("" for an external one); nil for a notification.
func rpc(e *engine.Engine, msg jsonv.Value, agent string) jsonv.Value {
	o := jsonv.Obj(msg)
	if o == nil {
		return nil
	}
	id, ok := o.Get("id")
	if !ok {
		return nil
	}
	method := jsonv.StrOr(jsonv.Get(msg, "method"))
	params := jsonv.Get(msg, "params")
	var result jsonv.Value
	code, message := 0, ""
	switch method {
	case "initialize":
		protocol, ok := jsonv.Str(jsonv.Get(params, "protocolVersion"))
		if !ok {
			protocol = "2025-06-18"
		}
		result = engine.Object("protocolVersion", protocol, "capabilities", engine.Object("tools", engine.Object("listChanged", false)),
			"serverInfo", engine.Object("name", "nalarupa", "version", version), "instructions", mcpInstructions)
	case "ping":
		result = jsonv.NewObject()
	case "tools/list":
		result = engine.Object("tools", toolsFor(e, agent))
	case "tools/call":
		var args jsonv.Value
		has := false
		if p := jsonv.Obj(params); p != nil {
			args, has = p.Get("arguments")
		}
		if has && args != nil && jsonv.Obj(args) == nil {
			code, message = -32602, "invalid params: arguments must be an object"
			break
		}
		result = callTool(e, jsonv.StrOr(jsonv.Get(params, "name")), jsonv.Clone(args), has, agent)
	default:
		code, message = -32601, "method not found: "+method
	}
	if code != 0 {
		return engine.Object("jsonrpc", "2.0", "id", id, "error", engine.Object("code", jsonv.Int(int64(code)), "message", message))
	}
	return engine.Object("jsonrpc", "2.0", "id", id, "result", result)
}

// callTool is a `tools/call`: args is an object or null (has false when the call gave none).
func callTool(e *engine.Engine, name string, args jsonv.Value, has bool, agent string) jsonv.Value {
	if !has {
		args = jsonv.NewObject()
	}
	var out engine.ToolOut
	switch {
	case name == "spawn_agents" && agent != "":
		out = SpawnChildren(e, agent, args)
	case name == "spawn_agents":
		out = engine.ToolOut{Text: "spawn_agents is only available to Nalarupa's built-in agents", Error: true}
	case name == PermissionTool && agent != "":
		// Blocks this request's goroutine (not the engine) until the user answers.
		out = AskPermission(e, agent, args)
	default:
		o := jsonv.Obj(args)
		if o == nil {
			o = jsonv.NewObject()
		}
		if agent != "" {
			o.Set("__agent", agent)
		} else {
			// External agents (Claude Code / Cursor / Codex configured by the user).
			o.Set("__agent", "external")
		}
		out = e.CallTool(name, o)
	}
	// The engine lock is not held here: rasterize screenshots without blocking the canvas.
	out = out.Finish(e)
	// Logged once the call is over, so the chat row says whether the edit landed.
	if agent == "" && name != "spawn_agents" {
		e.Locked(func() { e.Emit(engine.Object("type", "agent", "kind", "external", "text", name, "error", out.Error)) })
	}
	content := jsonv.NewArray(engine.Object("type", "text", "text", out.Text))
	for _, d := range out.Images {
		content.Items = append(content.Items, engine.Object("type", "image", "data", d, "mimeType", "image/png"))
	}
	return engine.Object("content", content, "isError", out.Error)
}

// InstallMCP registers Nalarupa's MCP endpoint with an external agent (mcp.rs `install`). Only runs on an
// explicit user action.
func InstallMCP(client string, port uint16) (string, error) {
	url := MCPURL(port)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("no home directory")
	}
	switch client {
	case "claude-code":
		cmd := command(claudeProgram, "mcp", "add", "--transport", "http", "--scope", "user", "nalarupa", url)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			text, _ := startError(err)
			return "", fmt.Errorf("could not run `claude` (%s)", text)
		}
		text := model.Lossy([]byte(stdout.String())) + model.Lossy([]byte(stderr.String()))
		if err == nil || strings.Contains(text, "already exists") {
			return "Claude Code: nalarupa → " + url, nil
		}
		return "", errors.New(text)
	case "cursor":
		p, err := mergeJSON(doc.JoinPath(doc.JoinPath(home, ".cursor"), "mcp.json"), "mcpServers", engine.Object("url", url))
		if err != nil {
			return "", err
		}
		return "Cursor: wrote " + p, nil
	case "codex":
		dir := doc.JoinPath(home, ".codex")
		path := doc.JoinPath(dir, "config.toml")
		b, _ := os.ReadFile(path)
		text := string(b)
		if !utf8.Valid(b) {
			text = ""
		}
		if strings.Contains(text, "[mcp_servers.nalarupa]") {
			return "Codex: already configured in " + path, nil
		}
		text += fmt.Sprintf("\n[mcp_servers.nalarupa]\nurl = \"%s\"\n", url)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return "", errors.New(doc.IOError(err))
		}
		if err := os.WriteFile(path, []byte(text), 0o666); err != nil {
			return "", errors.New(doc.IOError(err))
		}
		return "Codex: wrote " + path, nil
	}
	return "", fmt.Errorf("unknown client '%s' (claude-code, cursor, codex)", client)
}

// mergeJSON sets root[key]["nalarupa"] = entry in the JSON file at path. A missing file starts empty; one that
// cannot be read or parsed (a trailing comma, comments) is left alone, not replaced.
func mergeJSON(path, key string, entry jsonv.Value) (string, error) {
	root := jsonv.Value(jsonv.NewObject())
	b, err := os.ReadFile(path)
	switch {
	case err == nil && !utf8.Valid(b):
		return "", fmt.Errorf("could not read %s: stream did not contain valid UTF-8", path)
	case err == nil:
		if root, err = jsonv.Parse(b); err != nil {
			return "", fmt.Errorf("%s is not valid JSON (%v); not touching it", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("could not read %s: %s", path, doc.IOError(err))
	}
	o := jsonv.Obj(root)
	if o == nil {
		return "", fmt.Errorf("%s is not a JSON object; not touching it", path)
	}
	servers := jsonv.Obj(jsonv.Get(o, key))
	if servers == nil {
		servers = jsonv.NewObject()
		o.Set(key, servers)
	}
	servers.Set("nalarupa", entry)
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return "", errors.New(doc.IOError(err))
	}
	if err := os.WriteFile(path, jsonv.MarshalPretty(o), 0o666); err != nil {
		return "", errors.New(doc.IOError(err))
	}
	return path, nil
}
