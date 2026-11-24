package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMCPServerOriginGuardBatchAndTools(t *testing.T) {
	b := NewBridge(t.TempDir())
	defer b.Shutdown()

	var initRes InitResult
	if err := b.Call("init", nil, &initRes); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	if initRes.MCPPort <= 0 || !strings.HasPrefix(initRes.MCPURL, "http://127.0.0.1:") {
		t.Fatalf("unexpected MCP init result: %+v", initRes)
	}
	if err := b.Call("new", map[string]any{"template": "slides"}, nil); err != nil {
		t.Fatalf("new slides failed: %v", err)
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", initRes.MCPPort)
	post := func(path, body string, headers map[string]string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp, raw
	}

	// 1. Routing & HTTP method checks
	if r, _ := post("/other", "{}", nil); r.StatusCode != 404 {
		t.Fatalf("expected 404 on /other, got %d", r.StatusCode)
	}
	if r, _ := post("/mcpx", "{}", nil); r.StatusCode != 404 {
		t.Fatalf("expected 404 on /mcpx, got %d", r.StatusCode)
	}
	getResp, err := http.Get(base + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != 405 || getResp.Header.Get("Allow") != "POST" {
		t.Fatalf("expected GET /mcp -> 405 Allow: POST, got %d (%q)", getResp.StatusCode, getResp.Header.Get("Allow"))
	}

	// 2. DNS-rebinding Origin guard
	for _, evil := range []string{
		"https://evil.example",
		"http://localhost.evil.com",
		"http://localhostevil.com",
		"http://127.0.0.1.evil.com",
		"http://127.0.0.10",
		"null",
	} {
		if r, _ := post("/mcp", "{}", map[string]string{"Origin": evil}); r.StatusCode != 403 {
			t.Fatalf("expected 403 for Origin %q, got %d", evil, r.StatusCode)
		}
	}
	for _, good := range []string{"http://127.0.0.1:5173", "http://localhost:5173"} {
		if r, _ := post("/mcp", "{}", map[string]string{"Origin": good}); r.StatusCode != 202 {
			t.Fatalf("expected 202 for local Origin %q, got %d", good, r.StatusCode)
		}
	}

	// 3. JSON-RPC notifications, parse errors, unknown method, and batches
	if r, _ := post("/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil); r.StatusCode != 202 {
		t.Fatalf("notification expected 202, got %d", r.StatusCode)
	}
	_, badRaw := post("/mcp", "{not json", nil)
	var badMap map[string]any
	_ = json.Unmarshal(badRaw, &badMap)
	if errObj, _ := badMap["error"].(map[string]any); asFloat(errObj["code"]) != -32700 {
		t.Fatalf("expected -32700 parse error, got %s", string(badRaw))
	}

	_, batchRaw := post("/mcp", `[
		{"jsonrpc":"2.0","id":1,"method":"ping"},
		{"jsonrpc":"2.0","method":"notifications/x"},
		{"jsonrpc":"2.0","id":2,"method":"ping"}
	]`, nil)
	var batchArr []map[string]any
	if err := json.Unmarshal(batchRaw, &batchArr); err != nil || len(batchArr) != 2 || asFloat(batchArr[0]["id"]) != 1 || asFloat(batchArr[1]["id"]) != 2 {
		t.Fatalf("unexpected batch response: %s", string(batchRaw))
	}

	// 4. initialize + tools/list (external vs /mcp/<agent-id>)
	_, listExtRaw := post("/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	if !bytes.Contains(listExtRaw, []byte(`"execute"`)) || bytes.Contains(listExtRaw, []byte(`"approve"`)) {
		t.Fatalf("external tools/list unexpected: %s", string(listExtRaw))
	}
	_, listIntRaw := post("/mcp/ag1", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	if !bytes.Contains(listIntRaw, []byte(`"approve"`)) {
		t.Fatalf("internal /mcp/ag1 tools/list missing approve: %s", string(listIntRaw))
	}

	// 5. execute tool + atomic rollback on thrown error
	callTool := func(name string, args map[string]any) map[string]any {
		payload, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      name,
				"arguments": args,
			},
		})
		_, raw := post("/mcp", string(payload), nil)
		var resp struct {
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("unmarshal tools/call %s: %v (%s)", name, err, string(raw))
		}
		return resp.Result
	}

	stRes := callTool("get_editor_state", map[string]any{})
	if isErr, _ := stRes["isError"].(bool); isErr {
		t.Fatalf("get_editor_state failed: %+v", stRes)
	}

	// Execute script that throws mid-mutation: document must roll back cleanly!
	var beforeLayers []LayerRow
	_ = b.Call("layers", nil, &beforeLayers)
	failExec := callTool("execute", map[string]any{
		"input": `Insert("document", { id: "rollback_test", type: "rectangle", x: 10, y: 10, width: 50, height: 50 }); throw new Error("abort!");`,
	})
	if isErr, _ := failExec["isError"].(bool); !isErr {
		t.Fatalf("expected throwing execute to report isError=true: %+v", failExec)
	}
	var afterLayers []LayerRow
	_ = b.Call("layers", nil, &afterLayers)
	if len(afterLayers) != len(beforeLayers) {
		t.Fatalf("atomic rollback failed: layer count changed %d -> %d", len(beforeLayers), len(afterLayers))
	}
}

func TestMCPInstallClientsInTempHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell stand-in claude test runs on Unix")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$@\" > \"$HOME/claude-args\"\ncase \"$(cat \"$HOME/claude-mode\" 2>/dev/null)\" in\n  exists) echo \"MCP server nalarupa already exists in user config\" >&2; exit 1 ;;\n  fail) echo \"boom: not logged in\" >&2; exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	b := NewBridge(filepath.Join(root, "data"))
	defer b.Shutdown()
	var initRes InitResult
	if err := b.Call("init", nil, &initRes); err != nil {
		t.Fatal(err)
	}

	// Cursor install & merge preservation
	var out string
	if err := b.Call("mcp_install", map[string]any{"client": "cursor"}, &out); err != nil {
		t.Fatalf("mcp_install cursor failed: %v", err)
	}
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	if !strings.Contains(out, cursorPath) {
		t.Fatalf("expected output to mention %s, got %q", cursorPath, out)
	}
	// Refuses non-object JSON without clobbering
	_ = os.WriteFile(cursorPath, []byte("[1,2]"), 0o644)
	if err := b.Call("mcp_install", map[string]any{"client": "cursor"}, &out); err == nil {
		t.Fatal("expected mcp_install cursor to refuse JSON array config")
	}

	// Codex TOML install
	if err := b.Call("mcp_install", map[string]any{"client": "codex"}, &out); err != nil {
		t.Fatalf("mcp_install codex failed: %v", err)
	}
	codexBytes, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil || !strings.Contains(string(codexBytes), initRes.MCPURL) {
		t.Fatalf("codex config.toml missing MCP URL: %v (%s)", err, string(codexBytes))
	}

	// Claude Code CLI registration
	if err := b.Call("mcp_install", map[string]any{"client": "claude-code"}, &out); err != nil {
		t.Fatalf("mcp_install claude-code failed: %v", err)
	}
	argsBytes, _ := os.ReadFile(filepath.Join(home, "claude-args"))
	if !strings.Contains(string(argsBytes), "mcp") {
		t.Fatalf("expected claude CLI to receive mcp args, got %q", string(argsBytes))
	}
}

func TestCoreDocumentLayoutPaintShaderScriptComponentAndLockFreedom(t *testing.T) {
	b := NewBridge(t.TempDir())
	defer b.Shutdown()

	var initRes InitResult
	if err := b.Call("init", nil, &initRes); err != nil {
		t.Fatal(err)
	}
	if err := b.Call("new", map[string]any{"template": "empty"}, nil); err != nil {
		t.Fatal(err)
	}

	docDir := t.TempDir()
	docPath := filepath.Join(docDir, "scene.nala")
	if err := b.Call("save", map[string]any{"path": docPath}, nil); err != nil {
		t.Fatal(err)
	}
	glslSrc := "precision mediump float;\n/** @resolution */\nuniform vec2 res;\n/** @color @default #3b82f6 */\nuniform vec3 tint;\nvoid main() { vec2 p = gl_FragCoord.xy / res; gl_FragColor = vec4(mix(tint, vec3(1.0, 0.4, 0.2), p.x), 1.0); }\n"
	if err := os.WriteFile(filepath.Join(docDir, "card.glsl"), []byte(glslSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Auto-layout, text shaping, gradient/blend paint, shader paint, script node, and component + instance
	code := `
const frameId = Insert("document", {
  type: "frame",
  name: "Main Frame",
  x: 0, y: 0, width: 640, height: 480,
  layout: "vertical",
  padding: 24,
  gap: 16,
  fill: {
    type: "gradient",
    gradientType: "linear",
    colors: [{ color: "#1A1A2E", position: 0 }, { color: "#16213E", position: 1 }]
  }
});

Insert(frameId, {
  type: "text",
  content: "Nalarupa Native Go + MyGo",
  fontSize: 28,
  fontWeight: "bold",
  fill: "#FFFFFF"
});

Insert(frameId, {
  type: "rectangle",
  width: 240,
  height: 120,
  cornerRadius: 12,
  fill: {
    type: "shader",
    url: "card.glsl"
  }
});

Insert(frameId, {
  type: "script",
  width: 200,
  height: 60,
  code: "for (let i = 0; i < 4; i++) { Ellipse(i * 40 + 10, 10, 24, 24, { fill: '#FF6A3D' }); }"
});

const compId = Insert("document", {
  type: "frame",
  name: "Primary Button",
  x: 700, y: 0, width: 160, height: 44,
  reusable: true,
  layout: "horizontal",
  padding: [10, 18],
  fill: "#FF6A3D",
  cornerRadius: 8,
  children: [
    { type: "text", name: "Label", content: "Launch", fontSize: 14, fill: "#16110F" }
  ]
});

const instId = Copy(compId, frameId, { name: "CTA Instance" });
savedIds = { frameId, compId, instId };
`
	if _, err := b.ExecuteJS(code); err != nil {
		t.Fatalf("execute rich scene failed: %v", err)
	}

	var allLayers []LayerRow
	_ = b.Call("layers", nil, &allLayers)
	var mainFrameID, compID, instID string
	for _, l := range allLayers {
		switch l.Name {
		case "Main Frame":
			mainFrameID = l.ID
		case "Primary Button":
			compID = l.ID
		case "CTA Instance":
			instID = l.ID
		}
	}
	if mainFrameID == "" || compID == "" || instID == "" {
		t.Fatalf("missing inserted IDs: frame=%q comp=%q inst=%q", mainFrameID, compID, instID)
	}

	// 2. Verify viewport tags & post-execute diagnostics
	if err := b.Call("viewport", map[string]any{"x": -50, "y": -50, "w": 900, "h": 600}, nil); err != nil {
		t.Fatalf("viewport call failed: %v", err)
	}

	// 3. Render scene via RenderInto and assert non-background pixels rendered (shader + gradient + text + script)
	var rgba *image.RGBA
	img := b.RenderInto(&rgba, 640, 480, 0, 0, 1.0, CanvasBG, "", true)
	nonBg := 0
	for i := 0; i < len(img.Pix); i += 4 {
		r, g, bl := img.Pix[i], img.Pix[i+1], img.Pix[i+2]
		if r != CanvasBG[0] || g != CanvasBG[1] || bl != CanvasBG[2] {
			nonBg++
		}
	}
	if nonBg < 50000 {
		t.Fatalf("expected rich rendered scene to paint >50k pixels, got %d", nonBg)
	}

	// 4. Component instance child lookup & override
	var compKids []Box
	_ = b.Call("children_boxes", map[string]any{"id": compID}, &compKids)
	if len(compKids) == 0 {
		t.Fatal("expected Primary Button component to have label child")
	}
	instChildID := instID + "/" + compKids[0].ID
	var instChild NodeDetail
	if err := b.Call("node", map[string]any{"id": instChildID}, &instChild); err != nil || !instChild.Exists {
		t.Fatalf("expected instance child %s to exist: %v", instChildID, err)
	}
	if err := b.Call("update", map[string]any{
		"ids":   []string{instChildID},
		"props": map[string]any{"content": "Updated CTA"},
	}, nil); err != nil {
		t.Fatalf("instance override failed: %v", err)
	}

	// 5. Drag coalescing (drag_begin -> multiple moves -> drag_end -> single Undo restores original!)
	var beforeBox []Box
	_ = b.Call("boxes", map[string]any{"ids": []string{compID}}, &beforeBox)
	origX := beforeBox[0].X
	_, _ = b.CallRaw("drag_begin", nil)
	for step := 1; step <= 5; step++ {
		_ = b.Call("update", map[string]any{
			"ids":   []string{compID},
			"props": map[string]any{"x": origX + float64(step*10)},
		}, nil)
	}
	_, _ = b.CallRaw("drag_end", nil)
	_ = b.Call("undo", nil, nil)
	var afterUndo []Box
	_ = b.Call("boxes", map[string]any{"ids": []string{compID}}, &afterUndo)
	if afterUndo[0].X != origX {
		t.Fatalf("drag_begin/drag_end should coalesce into 1 undo step: got X=%v, want %v", afterUndo[0].X, origX)
	}

	// 6. Lock-freedom check: concurrent poll/state calls complete swiftly while PNG/SVG/HTML/PDF export runs
	var wg sync.WaitGroup
	wg.Add(1)
	var maxPollLatency time.Duration
	doneExport := make(chan struct{})
	go func() {
		defer wg.Done()
		for {
			select {
			case <-doneExport:
				return
			default:
				t0 := time.Now()
				var st CoreState
				_ = b.Call("state", nil, &st)
				if d := time.Since(t0); d > maxPollLatency {
					maxPollLatency = d
				}
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()
	for _, fmtName := range []string{"png", "svg", "html-css", "pdf"} {
		var files []string
		if err := b.Call("export", map[string]any{
			"ids":    []string{mainFrameID},
			"format": fmtName,
			"path":   "",
			"opts":   map[string]any{"scale": 2},
		}, &files); err != nil {
			close(doneExport)
			wg.Wait()
			t.Fatalf("export %s failed: %v", fmtName, err)
		}
		if len(files) == 0 {
			close(doneExport)
			wg.Wait()
			t.Fatalf("export %s returned zero files", fmtName)
		}
		if fi, err := os.Stat(files[0]); err != nil || fi.Size() == 0 {
			close(doneExport)
			wg.Wait()
			t.Fatalf("export %s produced empty file %s: %v", fmtName, files[0], err)
		}
	}
	close(doneExport)
	wg.Wait()
	if maxPollLatency > 500*time.Millisecond {
		t.Fatalf("state poll blocked too long during export: %v", maxPollLatency)
	}
}
