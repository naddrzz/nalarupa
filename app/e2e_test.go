package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	e2eBuildOnce sync.Once
	e2eBinPath   string
	e2eBuildErr  error
)

func requireRealWindowEnv(t *testing.T) string {
	t.Helper()
	hasDisplay := os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != "" || os.Getenv("NALARUPA_E2E") == "1"
	if runtime.GOOS == "linux" && os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") == "" {
		t.Skip("skipping real windowed E2E test (no WAYLAND_DISPLAY/DISPLAY; run via: bash scripts/wl-run.sh go test -v -run TestRealE2E .)")
	}
	if !hasDisplay {
		t.Skip("skipping real windowed E2E test (set NALARUPA_E2E=1 to run)")
	}

	e2eBuildOnce.Do(func() {
		cwd, err := os.Getwd()
		if err != nil {
			e2eBuildErr = err
			return
		}
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "build.ps1")
			e2eBinPath = filepath.Join(cwd, "dist", "nalarupa.exe")
		} else {
			cmd = exec.Command("bash", "build.sh")
			e2eBinPath = filepath.Join(cwd, "dist", "nalarupa")
		}
		cmd.Dir = cwd
		out, err := cmd.CombinedOutput()
		if err != nil {
			e2eBuildErr = fmt.Errorf("build failed: %w\n%s", err, string(out))
		}
	})
	if e2eBuildErr != nil {
		t.Fatalf("failed building standalone nalarupa binary: %v", e2eBuildErr)
	}
	return e2eBinPath
}

type autoRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type autoWin struct {
	ID            int      `json:"id"`
	Title         string   `json:"title"`
	Kind          string   `json:"kind"`
	Bounds        autoRect `json:"bounds"`
	ContentBounds autoRect `json:"contentBounds"`
	Focused       bool     `json:"focused"`
	Visible       bool     `json:"visible"`
}

type autoNode struct {
	ID      string   `json:"id"`
	Role    string   `json:"role"`
	Key     any      `json:"key"`
	Label   string   `json:"label"`
	Value   string   `json:"value"`
	Bounds  autoRect `json:"bounds"`
	States  []string `json:"states"`
	Actions []string `json:"actions"`
}

type autoTree struct {
	Window int        `json:"window"`
	Nodes  []autoNode `json:"nodes"`
}

type e2eDriver struct {
	t        *testing.T
	proc     *exec.Cmd
	conn     net.Conn
	reader   *bufio.Reader
	nextID   int
	homeDir  string
	dataDir  string
	stderr   bytes.Buffer
}

func launchRealApp(t *testing.T, extraEnv ...string) *e2eDriver {
	t.Helper()
	bin := requireRealWindowEnv(t)

	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	dataDir := filepath.Join(root, "data")
	_ = os.MkdirAll(homeDir, 0o755)
	_ = os.MkdirAll(dataDir, 0o755)

	// Keep socket path short for Unix domain socket sun_path limits
	sockDir, err := os.MkdirTemp("/tmp", "nala-e2e-")
	if err != nil {
		sockDir = root
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	}
	sockPath := filepath.Join(sockDir, "auto.sock")

	d := &e2eDriver{
		t:       t,
		homeDir: homeDir,
		dataDir: dataDir,
	}

	cmd := exec.Command(bin)
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"HOME="+homeDir,
		"NALARUPA_DATA="+dataDir,
		"MYGO_AUTOMATION="+sockPath,
	)
	env = append(env, extraEnv...)
	cmd.Env = env
	cmd.Stderr = &d.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed launching %s: %v", bin, err)
	}
	d.proc = cmd

	t.Cleanup(func() {
		if d.conn != nil {
			_ = d.conn.Close()
		}
		if d.proc != nil && d.proc.Process != nil {
			_ = d.proc.Process.Kill()
			_ = d.proc.Wait()
		}
	})

	deadline := time.Now().Add(15 * time.Second)
	var conn net.Conn
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("unix", sockPath, 300*time.Millisecond)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("timed out waiting for MYGO_AUTOMATION socket %s; stderr:\n%s", sockPath, d.stderr.String())
	}
	d.conn = conn
	d.reader = bufio.NewReader(conn)

	// Wait for initial window to appear and settle
	d.until("main window visible", 10*time.Second, func() bool {
		ws := d.windows()
		return len(ws) == 1 && ws[0].Visible && ws[0].Kind == "content" && ws[0].ContentBounds.Width >= 1000
	})
	_ = d.idle(3000)
	return d
}

func (d *e2eDriver) rpc(method string, params any, out any) error {
	d.nextID++
	req := map[string]any{
		"id":     d.nextID,
		"method": method,
	}
	if params != nil {
		req["params"] = params
	}
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := d.conn.Write(line); err != nil {
		return fmt.Errorf("write %s: %w (stderr: %s)", method, err, d.stderr.String())
	}
	respLine, err := d.reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read %s: %w (stderr: %s)", method, err, d.stderr.String())
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("%s (%s): %s", method, resp.Error.Code, resp.Error.Message)
	}
	if out != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

func (d *e2eDriver) windows() []autoWin {
	var res struct {
		Windows []autoWin `json:"windows"`
	}
	if err := d.rpc("windows", nil, &res); err != nil {
		d.t.Fatalf("windows RPC failed: %v", err)
	}
	return res.Windows
}

func (d *e2eDriver) tree() autoTree {
	var tr autoTree
	if err := d.rpc("tree", map[string]any{"window": 0}, &tr); err != nil {
		d.t.Fatalf("tree RPC failed: %v", err)
	}
	return tr
}

func (d *e2eDriver) idle(timeoutMS int) error {
	return d.rpc("idle", map[string]any{"window": 0, "timeout": timeoutMS}, nil)
}

func (d *e2eDriver) pressKey(key string) {
	d.t.Helper()
	d.until("element key="+key, 6*time.Second, func() bool {
		for _, n := range d.tree().Nodes {
			if s, ok := n.Key.(string); ok && s == key {
				return true
			}
		}
		return false
	})
	if err := d.rpc("act", map[string]any{"window": 0, "key": key, "action": "press"}, nil); err != nil {
		d.t.Fatalf("act press key=%q failed: %v", key, err)
	}
	_ = d.idle(2000)
}

func (d *e2eDriver) setValueByKey(key, val string) {
	d.t.Helper()
	if err := d.rpc("act", map[string]any{"window": 0, "key": key, "action": "focus"}, nil); err != nil {
		d.t.Fatalf("act focus key=%q failed: %v", key, err)
	}
	if err := d.rpc("act", map[string]any{"window": 0, "key": key, "action": "setValue", "text": val}, nil); err != nil {
		d.t.Fatalf("act setValue key=%q failed: %v", key, err)
	}
	_ = d.idle(1500)
}

func (d *e2eDriver) clickAt(x, y float64) {
	d.t.Helper()
	err := d.rpc("input", map[string]any{
		"window": 0,
		"events": []map[string]any{
			{"type": "click", "x": x, "y": y},
		},
	}, nil)
	if err != nil {
		d.t.Fatalf("clickAt(%v,%v) failed: %v", x, y, err)
	}
	_ = d.idle(1500)
}

func (d *e2eDriver) drag(x0, y0, x1, y1 float64, mods ...string) {
	d.t.Helper()
	mx, my := (x0+x1)/2, (y0+y1)/2
	ev := func(typ string, x, y float64) map[string]any {
		m := map[string]any{"type": typ, "x": x, "y": y}
		if len(mods) > 0 {
			m["mods"] = mods
		}
		return m
	}
	err := d.rpc("input", map[string]any{
		"window": 0,
		"events": []map[string]any{
			ev("pointerMove", x0, y0),
			ev("pointerDown", x0, y0),
			ev("pointerMove", mx, my),
			ev("pointerMove", x1, y1),
			ev("pointerUp", x1, y1),
		},
	}, nil)
	if err != nil {
		d.t.Fatalf("drag failed: %v", err)
	}
	_ = d.idle(2000)
}

func (d *e2eDriver) sendKey(accel string) {
	d.t.Helper()
	err := d.rpc("input", map[string]any{
		"window": 0,
		"events": []map[string]any{
			{"type": "key", "key": accel},
		},
	}, nil)
	if err != nil {
		d.t.Fatalf("sendKey(%q) failed: %v", accel, err)
	}
	_ = d.idle(1500)
}

func (d *e2eDriver) captureImage() image.Image {
	d.t.Helper()
	var res struct {
		PNG    []byte `json:"png"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	if err := d.rpc("capture", map[string]any{"window": 0}, &res); err != nil {
		d.t.Fatalf("capture RPC failed: %v", err)
	}
	if len(res.PNG) == 0 || res.Width < 1000 || res.Height < 600 {
		d.t.Fatalf("unexpected capture dimensions %dx%d (%d bytes)", res.Width, res.Height, len(res.PNG))
	}
	img, err := png.Decode(bytes.NewReader(res.PNG))
	if err != nil {
		d.t.Fatalf("decode captured PNG failed: %v", err)
	}
	return img
}

func (d *e2eDriver) until(desc string, timeout time.Duration, cond func() bool) {
	d.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(60 * time.Millisecond)
	}
	d.t.Fatalf("timed out after %v waiting for %s", timeout, desc)
}

func (d *e2eDriver) hasLabelContaining(sub string) bool {
	for _, n := range d.tree().Nodes {
		if strings.Contains(n.Label, sub) || strings.Contains(n.Value, sub) {
			return true
		}
	}
	return false
}

func assertDistinctPixels(t *testing.T, img image.Image, minDistinct int, label string) {
	t.Helper()
	b := img.Bounds()
	seen := map[[3]uint8]bool{}
	stepX := max(1, b.Dx()/80)
	stepY := max(1, b.Dy()/80)
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bl, _ := img.At(x, y).RGBA()
			seen[[3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8)}] = true
		}
	}
	if len(seen) < minDistinct {
		t.Fatalf("%s screenshot looks blank/flat: only %d distinct sampled colors", label, len(seen))
	}
}

func TestRealE2E_FullSlideWorkflowAndLiveMCP(t *testing.T) {
	d := launchRealApp(t)

	// 1. Verify Home window title, template cards, and real Home PNG capture
	ws := d.windows()
	if !strings.Contains(ws[0].Title, "Nalarupa") {
		t.Fatalf("expected Nalarupa in window title, got %q", ws[0].Title)
	}
	homeShot := d.captureImage()
	assertDistinctPixels(t, homeShot, 6, "Home screen")

	// 2. Start Slide Deck template
	d.pressKey("template-slides")
	d.until("Editor toolbar & Layers", 6*time.Second, func() bool {
		return d.hasLabelContaining("Editor toolbar") && d.hasLabelContaining("Layers")
	})

	// 3. Open Agent settings in the real window and read the live HTTP MCP URL from the accessibility tree!
	d.pressKey("agent-settings")
	var mcpURL string
	d.until("MCP URL in Agent settings", 5*time.Second, func() bool {
		for _, n := range d.tree().Nodes {
			if strings.HasPrefix(n.Label, "http://127.0.0.1:") && strings.HasSuffix(n.Label, "/mcp") {
				mcpURL = n.Label
				return true
			}
		}
		return false
	})
	d.pressKey("agent-settings") // close settings

	// 4. Drive the live windowed app from an external MCP HTTP client!
	mcpBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "execute",
			"arguments": map[string]any{
				"input": `Insert("document", { type: "frame", name: "Live MCP Slide", x: 0, y: 1200, width: 1920, height: 1080, fill: "#FF6A3D" });`,
			},
		},
	})
	resp, err := http.Post(mcpURL, "application/json", bytes.NewReader(mcpBody))
	if err != nil {
		t.Fatalf("POST to live window MCP %s failed: %v", mcpURL, err)
	}
	rawResp, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || bytes.Contains(rawResp, []byte(`"isError":true`)) {
		t.Fatalf("live MCP execute failed: status=%d body=%s", resp.StatusCode, string(rawResp))
	}

	// 5. Verify the 80ms background poller automatically updates Layers AND window dirty title ("•")!
	d.until("Live MCP Slide in Layers & dirty dot in OS window title", 6*time.Second, func() bool {
		ws := d.windows()
		return len(ws) == 1 && strings.Contains(ws[0].Title, "•") && d.hasLabelContaining("Live MCP Slide")
	})

	// 6. Switch to Slides tab, capture Editor PNG, then enter Presentation mode & verify HUD + Esc!
	d.pressKey("tab-slides")
	_ = d.idle(2000)
	slidesShot := d.captureImage()
	assertDistinctPixels(t, slidesShot, 10, "Editor Slides tab")

	d.pressKey("present")
	d.until("Presentation HUD", 5*time.Second, func() bool {
		return d.hasLabelContaining("Esc to exit")
	})
	presentShot := d.captureImage()
	assertDistinctPixels(t, presentShot, 5, "Presentation mode")

	d.sendKey("Right")
	d.sendKey("Escape")
	d.until("returned to Editor from Present", 5*time.Second, func() bool {
		return d.hasLabelContaining("Editor toolbar")
	})
}

func TestRealE2E_CanvasToolsDrawMoveUndoPenAndInspector(t *testing.T) {
	d := launchRealApp(t)

	d.pressKey("template-blank")
	d.until("Editor loaded", 6*time.Second, func() bool {
		return d.hasLabelContaining("Editor toolbar")
	})

	// 1. Draw a Rectangle on the real GTK/Wayland canvas
	d.pressKey("tool-rectangle")
	cx, cy := float64(LeftW+220), float64(TopH+200)
	d.drag(cx, cy, cx+160, cy+110)

	// Drawing selects the new rectangle -> Design Inspector opens automatically!
	d.until("Design Inspector sections visible", 5*time.Second, func() bool {
		return d.hasLabelContaining("Position") && d.hasLabelContaining("Stroke & corners") && d.hasLabelContaining("Export")
	})

	// 2. Nudge, Duplicate, and Undo via real window keyboard shortcuts
	d.clickAt(cx+80, cy+55)
	d.sendKey("Shift+Right")
	d.sendKey("Ctrl+D")
	d.sendKey("Ctrl+Z")

	// 3. Draw a closed 3-anchor vector path with the Pen tool on the real canvas
	d.pressKey("tool-pen")
	px, py := float64(LeftW+460), float64(TopH+320)
	d.clickAt(px, py)
	d.clickAt(px+100, py)
	d.clickAt(px+50, py+85)
	d.clickAt(px, py) // close path!
	d.sendKey("Escape")
	d.sendKey("Escape")

	editorShot := d.captureImage()
	assertDistinctPixels(t, editorShot, 8, "Editor with drawn Rectangle and Pen path")
}

func TestRealE2E_FileSaveReopenRecentAndUnsavedGuard(t *testing.T) {
	d := launchRealApp(t)

	d.pressKey("template-mobile")
	d.until("Mobile editor open", 6*time.Second, func() bool {
		return d.hasLabelContaining("Editor toolbar")
	})

	// Draw an ellipse so document is dirty
	d.pressKey("tool-ellipse")
	d.drag(float64(LeftW+200), float64(TopH+180), float64(LeftW+300), float64(TopH+280))
	d.until("dirty indicator in title", 5*time.Second, func() bool {
		return strings.Contains(d.windows()[0].Title, "•")
	})

	// Click Save in toolbar -> writes to $HOME/Documents/Nalarupa/*.nala and clears "•"
	d.pressKey("save")
	d.until("clean window title after save", 5*time.Second, func() bool {
		return !strings.Contains(d.windows()[0].Title, "•")
	})
	docsDir := filepath.Join(d.homeDir, "Documents", "Nalarupa")
	entries, err := os.ReadDir(docsDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected saved .nala file in %s, got entries=%v err=%v", docsDir, entries, err)
	}
	savedBase := entries[0].Name()

	// Make another edit so document is dirty, return Home, test Unsaved Changes Cancel & Don't save
	d.pressKey("tool-rectangle")
	d.drag(float64(LeftW+220), float64(TopH+320), float64(LeftW+310), float64(TopH+390))
	d.pressKey("home")
	d.pressKey("template-empty")
	d.until("Unsaved changes dialog", 5*time.Second, func() bool {
		return d.hasLabelContaining("Without saving they are lost.")
	})
	d.pressKey("unsaved-cancel")
	d.pressKey("template-empty")
	d.pressKey("unsaved-discard")

	// Return Home and reopen the saved .nala file from Recent files!
	d.pressKey("home")
	d.until("saved file in Recent files", 5*time.Second, func() bool {
		return d.hasLabelContaining(savedBase)
	})
	d.pressKey("recent-" + filepath.Join(docsDir, savedBase))
	d.until("reopened saved file in Editor", 5*time.Second, func() bool {
		return d.hasLabelContaining("Editor toolbar") && strings.Contains(d.windows()[0].Title, strings.TrimSuffix(savedBase, ".nala"))
	})
}

func TestRealE2E_LiveClaudeCodeStreamingAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in python3 claude script runs on Unix")
	}
	binDir := t.TempDir()
	claudeScript := `#!/usr/bin/env python3
import json, sys, time, urllib.request

args = sys.argv[1:]
if "auth" in args and "status" in args:
    print(json.dumps({"loggedIn": True, "authMethod": "claude.ai", "email": "dev@nalarupa.test", "subscriptionType": "max"}))
    sys.exit(0)

mcp_cfg_path = ""
for i, a in enumerate(args):
    if a == "--mcp-config" and i + 1 < len(args):
        mcp_cfg_path = args[i + 1]

_ = sys.stdin.read()

mcp_url = ""
if mcp_cfg_path:
    with open(mcp_cfg_path, "r", encoding="utf-8") as f:
        cfg = json.load(f)
        mcp_url = cfg["mcpServers"]["nalarupa"]["url"]

def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

emit({"type": "stream_event", "event": {"type": "message_start", "message": {"model": "claude-sonnet-4-6"}}})
emit({"type": "stream_event", "event": {"type": "content_block_start", "index": 0, "content_block": {"type": "tool_use", "id": "toolu_1", "name": "mcp__nalarupa__execute"}}})

if mcp_url:
    code = 'const fid = Insert("document", { type: "frame", name: "Hero Banner", x: 0, y: 0, width: 960, height: 540, layout: "vertical", padding: 48, fill: "#181825", placeholder: true }); Insert(fid, { type: "text", content: "Designed Live in MyGo", fontSize: 40, fontWeight: "bold", fill: "#FF6A3D" }); Update(fid, { placeholder: false });'
    req_body = json.dumps({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {"name": "execute", "arguments": {"input": code}}
    }).encode("utf-8")
    req = urllib.request.Request(mcp_url, data=req_body, headers={"Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(req, timeout=10) as resp:
        resp.read()

emit({"type": "assistant", "message": {"content": [{"type": "tool_use", "id": "toolu_1", "name": "mcp__nalarupa__execute", "input": {"input": "Insert Hero Banner"}}]}})
emit({"type": "user", "message": {"content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": "Inserted Hero Banner"}]}})
emit({"type": "stream_event", "event": {"type": "content_block_start", "index": 1, "content_block": {"type": "text", "text": ""}}})
emit({"type": "stream_event", "event": {"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "Created Hero Banner on the canvas."}}})
emit({"type": "stream_event", "event": {"type": "message_stop"}})
emit({"type": "result", "session_id": "sess_e2e_1", "duration_ms": 650, "total_cost_usd": 0.012, "num_turns": 1, "usage": {"input_tokens": 320, "output_tokens": 95}})
`
	claudePath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudePath, []byte(claudeScript), 0o755); err != nil {
		t.Fatal(err)
	}

	d := launchRealApp(t, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	d.pressKey("template-empty")

	// Set prompt value on the real Agent composer input and press Send!
	d.setValueByKey("agent-input", "Design a bold Hero Banner")
	d.pressKey("agent-send")

	// Wait for the live stand-in Claude Code agent to insert "Hero Banner" on the canvas AND stream its reply!
	d.until("Hero Banner in Layers & streamed reply in Agent transcript", 10*time.Second, func() bool {
		return d.hasLabelContaining("Hero Banner") && d.hasLabelContaining("Created Hero Banner on the canvas.")
	})

	d.pressKey("fit")
	shot := d.captureImage()
	assertDistinctPixels(t, shot, 8, "Live Claude Code agent canvas")
}
