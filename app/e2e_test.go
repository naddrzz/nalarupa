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
			tmpBinDir, mkErr := os.MkdirTemp("", "nalarupa-e2e-bin-")
			if mkErr != nil {
				e2eBuildErr = mkErr
				return
			}
			e2eBinPath = filepath.Join(tmpBinDir, "nalarupa.exe")
			_ = copyFileIfPresent(filepath.Join(cwd, "assets", "brand", "nalarupa.ico"), filepath.Join(tmpBinDir, "nalarupa.ico"))
			cmd = exec.Command("go", "build", "-ldflags", "-H=windowsgui", "-o", e2eBinPath, ".")
			cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
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

func copyFileIfPresent(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
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

	// On Windows use loopback TCP automation to avoid AF_UNIX 108-byte sun_path limits on long test names.
	var autoSpec, dialNet, dialAddr string
	if runtime.GOOS == "windows" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("allocate loopback port: %v", err)
		}
		dialAddr = ln.Addr().String()
		_ = ln.Close()
		dialNet = "tcp"
		autoSpec = "tcp:" + dialAddr
	} else {
		sockDir, err := os.MkdirTemp("/tmp", "nala-e2e-")
		if err != nil {
			sockDir = root
		} else {
			t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
		}
		dialAddr = filepath.Join(sockDir, "auto.sock")
		dialNet = "unix"
		autoSpec = dialAddr
	}

	d := &e2eDriver{
		t:       t,
		homeDir: homeDir,
		dataDir: dataDir,
	}

	cmd := exec.Command(bin)
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"HOME="+homeDir,
		"USERPROFILE="+homeDir,
		"NALARUPA_DATA="+dataDir,
		"MYGO_AUTOMATION="+autoSpec,
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
		c, err := net.DialTimeout(dialNet, dialAddr, 300*time.Millisecond)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("timed out waiting for MYGO_AUTOMATION %s; stderr:\n%s", autoSpec, d.stderr.String())
	}
	d.conn = conn
	d.reader = bufio.NewReader(conn)

	// Wait for initial window to appear and settle (may be smaller than 1000px on compact displays, min 840px)
	d.until("main window visible", 10*time.Second, func() bool {
		ws := d.windows()
		return len(ws) == 1 && ws[0].Visible && ws[0].Kind == "content" && ws[0].ContentBounds.Width >= 800
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
	if len(res.PNG) == 0 || res.Width < 800 || res.Height < 500 {
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

func buildStandInClaudeCLI(t *testing.T, binDir string) string {
	t.Helper()
	srcDir := t.TempDir()
	mainGo := `package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"
)

func emit(v any) {
	b, _ := json.Marshal(v)
	os.Stdout.Write(append(b, '\n'))
}

func main() {
	args := os.Args[1:]
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "auth" && args[i+1] == "status" {
			emit(map[string]any{"loggedIn": true, "authMethod": "claude.ai", "email": "dev@nalarupa.test", "subscriptionType": "max"})
			return
		}
	}
	var mcpCfgPath string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--mcp-config" {
			mcpCfgPath = args[i+1]
		}
	}
	_, _ = io.ReadAll(os.Stdin)

	var mcpURL string
	if mcpCfgPath != "" {
		if raw, err := os.ReadFile(mcpCfgPath); err == nil {
			var cfg struct {
				MCPServers map[string]struct {
					URL string ` + "`json:\"url\"`" + `
				} ` + "`json:\"mcpServers\"`" + `
			}
			if json.Unmarshal(raw, &cfg) == nil {
				mcpURL = cfg.MCPServers["nalarupa"].URL
			}
		}
	}

	emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start", "message": map[string]any{"model": "claude-sonnet-5-5"}}})
	emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_1", "name": "mcp__nalarupa__execute"}}})

	statements := []string{
		"{\"input\": \"fid = Insert(document, { type: 'frame', name: 'Hero Banner', x: 0, y: 0, width: 960, height: 540, layout: 'vertical', padding: 48, gap: 16, fill: '#181825', clip: true, placeholder: true });\\n",
		"Insert(fid, { type: 'text', name: 'Eyebrow', content: 'NALARUPA LIVE', fontSize: 14, fontWeight: 'bold', fill: '#607DFF' });\\n",
		"Insert(fid, { type: 'text', name: 'Headline', content: 'Designed Live in MyGo', fontSize: 40, fontWeight: 'bold', fill: '#FF6A3D' });\\n",
		"Insert(fid, { type: 'rectangle', name: 'CTA', width: 180, height: 48, cornerRadius: 12, fill: '#2563EB' });\\n",
		"Update(fid, { placeholder: false });\"}",
	}
	for _, chunk := range statements {
		emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": chunk}}})
		time.Sleep(160 * time.Millisecond)
	}

	fullCode := "fid = Insert(document, { type: 'frame', name: 'Hero Banner', x: 0, y: 0, width: 960, height: 540, layout: 'vertical', padding: 48, gap: 16, fill: '#181825', clip: true, placeholder: true });\n" +
		"Insert(fid, { type: 'text', name: 'Eyebrow', content: 'NALARUPA LIVE', fontSize: 14, fontWeight: 'bold', fill: '#607DFF' });\n" +
		"Insert(fid, { type: 'text', name: 'Headline', content: 'Designed Live in MyGo', fontSize: 40, fontWeight: 'bold', fill: '#FF6A3D' });\n" +
		"Insert(fid, { type: 'rectangle', name: 'CTA', width: 180, height: 48, cornerRadius: 12, fill: '#2563EB' });\n" +
		"Update(fid, { placeholder: false });"

	if mcpURL != "" {
		reqBody, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params":  map[string]any{"name": "execute", "arguments": map[string]any{"input": fullCode}},
		})
		if resp, err := http.Post(mcpURL, "application/json", bytes.NewReader(reqBody)); err == nil {
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	}

	emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": "mcp__nalarupa__execute", "input": map[string]any{"input": fullCode}}}}})
	emit(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "Inserted Hero Banner"}}}})
	emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "text", "text": ""}}})
	emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "Created Hero Banner on the canvas."}}})
	emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_stop"}})
	emit(map[string]any{"type": "result", "session_id": "sess_e2e_1", "duration_ms": 650, "total_cost_usd": 0.012, "num_turns": 1, "usage": map[string]any{"input_tokens": 320, "output_tokens": 95}})
}
`
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module standinclaude\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exeName := "claude"
	if runtime.GOOS == "windows" {
		exeName = "claude.exe"
	}
	outPath := filepath.Join(binDir, exeName)
	cmd := exec.Command("go", "build", "-o", outPath, ".")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stand-in claude failed: %v\n%s", err, string(out))
	}
	return outPath
}

func TestRealE2E_LiveClaudeCodeStreamingAgent(t *testing.T) {
	binDir := t.TempDir()
	_ = buildStandInClaudeCLI(t, binDir)

	d := launchRealApp(t, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	d.pressKey("template-empty")

	// Set prompt value on the real Agent composer input and press Send!
	d.setValueByKey("agent-input", "Design a bold Hero Banner")
	d.pressKey("agent-send")

	// Observe live agent cursor badge appearing & moving across canvas while streaming!
	var cursorPositions [][2]float64
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		tr := d.tree()
		hasReply := false
		for _, n := range tr.Nodes {
			if k, ok := n.Key.(string); ok && strings.HasPrefix(k, "agent-cursor-") {
				pos := [2]float64{n.Bounds.X, n.Bounds.Y}
				if len(cursorPositions) == 0 || cursorPositions[len(cursorPositions)-1] != pos {
					cursorPositions = append(cursorPositions, pos)
				}
			}
			if strings.Contains(n.Label, "Created Hero Banner on the canvas.") {
				hasReply = true
			}
		}
		if hasReply && d.hasLabelContaining("Hero Banner") {
			break
		}
		time.Sleep(45 * time.Millisecond)
	}
	t.Logf("Observed %d distinct live agent cursor positions during streaming: %v", len(cursorPositions), cursorPositions)
	if len(cursorPositions) == 0 {
		t.Fatalf("expected live agent cursor badge during streaming")
	}

	d.until("Hero Banner in Layers & streamed reply in Agent transcript", 6*time.Second, func() bool {
		return d.hasLabelContaining("Hero Banner") && d.hasLabelContaining("Created Hero Banner on the canvas.")
	})

	d.pressKey("fit")
	shot := d.captureImage()
	assertDistinctPixels(t, shot, 8, "Live Claude Code agent canvas")
}

func TestRealE2E_Round3ExportScrollSymmetryAndZoom(t *testing.T) {
	d := launchRealApp(t)

	// 1. Start Mobile template ("iPhone 16" at 0,0 clip:true) and check symmetric viewport & full panel bounds!
	d.pressKey("template-mobile")
	d.until("Mobile editor ready", 6*time.Second, func() bool {
		return d.hasLabelContaining("Editor toolbar") && d.hasLabelContaining("iPhone 16")
	})

	winInfo := d.windows()[0]
	winH := winInfo.ContentBounds.Height

	// Verify Agent bottom composer buttons are 100% inside window ContentBounds (never clipped behind taskbar!)
	for _, key := range []string{"agent-input", "agent-model", "agent-effort", "agent-skills", "agent-send"} {
		found := false
		for _, n := range d.tree().Nodes {
			if ks, ok := n.Key.(string); ok && ks == key {
				found = true
				bottom := n.Bounds.Y + n.Bounds.Height
				if n.Bounds.Y < 0 || bottom > winH+1.0 {
					t.Fatalf("Real window control %s out of ContentBounds [0..%.1f]: Y=%.1f H=%.1f Bottom=%.1f",
						key, winH, n.Bounds.Y, n.Bounds.Height, bottom)
				}
				break
			}
		}
		if !found {
			t.Fatalf("Real window control %s missing from tree", key)
		}
	}

	// 2. Grab live MCP URL and verify FindEmptySpace symmetry across 3 mobile screens ("iPhone 16", "Menu sesi", "Review perubahan")
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
	d.pressKey("agent-settings")

	callMCP := func(js string) string {
		mcpBody, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      "execute",
				"arguments": map[string]any{"input": js},
			},
		})
		resp, err := http.Post(mcpURL, "application/json", bytes.NewReader(mcpBody))
		if err != nil {
			t.Fatalf("MCP POST failed: %v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 || bytes.Contains(raw, []byte(`"isError":true`)) {
			t.Fatalf("MCP execute error: %s", string(raw))
		}
		return string(raw)
	}

	symOut := callMCP(`
		const p2 = FindEmptySpace({width: 393, height: 852, padding: 80});
		const s2 = Insert(document, {type: "frame", name: "Menu sesi", x: p2.x, y: p2.y, width: 393, height: 852, fill: "#FFFFFF", clip: true});
		const p3 = FindEmptySpace({width: 393, height: 852, padding: 80});
		const s3 = Insert(document, {type: "frame", name: "Review perubahan", x: p3.x, y: p3.y, width: 393, height: 852, fill: "#FFFFFF", clip: true});
		Print("Y_COORDS=" + p2.y + "," + p3.y);
	`)
	if !strings.Contains(symOut, "Y_COORDS=0,0") {
		t.Fatalf("expected Menu sesi and Review perubahan aligned at y=0 with iPhone 16, got: %s", symOut)
	}

	// 3. Verify smooth zoom in & zoom out via real toolbar buttons and Ctrl+Wheel input!
	d.pressKey("zoom-in")
	d.pressKey("zoom-out")
	d.pressKey("fit")

	// 4. Build tall 1440x4154 shadowed liquid-glass page, select it, scroll Inspector to bottom, and click Export PNG & JPEG!
	callMCP(`
		Get(n => n.type === "frame" && Delete(n.id));
		` + buildTallShadowedGlassPageScript())

	d.until("Arunika — Landing Page in Layers", 6*time.Second, func() bool {
		return d.hasLabelContaining("Arunika — Landing Page")
	})

	// Select Arunika frame via Layers row click, switch to Design tab, scroll Inspector to bottom
	for _, n := range d.tree().Nodes {
		if strings.Contains(n.Label, "Arunika — Landing Page") && strings.HasPrefix(fmt.Sprint(n.Key), "layer-") {
			d.clickAt(n.Bounds.X+n.Bounds.Width/2, n.Bounds.Y+n.Bounds.Height/2)
			break
		}
	}
	d.pressKey("tab-design")

	// Scroll inspector down via real mouse wheel events so Export section scrolls cleanly into the bottom of the viewport!
	var inspBounds autoRect
	for _, n := range d.tree().Nodes {
		if ks, ok := n.Key.(string); ok && ks == "inspector-scroll" {
			inspBounds = n.Bounds
			break
		}
	}
	if err := d.rpc("input", map[string]any{
		"window": 0,
		"events": []map[string]any{
			{"type": "wheel", "x": inspBounds.X + 80, "y": inspBounds.Y + 120, "dy": 600},
			{"type": "wheel", "x": inspBounds.X + 80, "y": inspBounds.Y + 120, "dy": 600},
		},
	}, nil); err != nil {
		t.Fatalf("wheel scroll inspector failed: %v", err)
	}
	_ = d.idle(1500)

	// Verify BOTH export-png AND export-svg sit strictly inside ContentBounds.Height!
	for _, expKey := range []string{"export-png", "export-jpeg", "export-svg", "export-html"} {
		found := false
		for _, n := range d.tree().Nodes {
			if ks, ok := n.Key.(string); ok && ks == expKey {
				found = true
				bottom := n.Bounds.Y + n.Bounds.Height
				if bottom > winH+1.0 {
					t.Fatalf("Real window %s clipped past bottom %.1f: Bottom=%.1f", expKey, winH, bottom)
				}
				break
			}
		}
		if !found {
			t.Fatalf("Real window %s not found after scrolling inspector", expKey)
		}
	}

	// Click Export JPEG in the real window and immediately prove the Windows GUI loop responds in < 300ms (NEVER "Not Responding"!)
	clickStart := time.Now()
	if err := d.rpc("act", map[string]any{"window": 0, "key": "export-jpeg", "action": "press"}, nil); err != nil {
		t.Fatalf("press export-jpeg failed: %v", err)
	}
	wsDuringExport := d.windows()
	respLatency := time.Since(clickStart)
	t.Logf("Live Windows GUI response latency immediately after clicking Export JPEG on 1440x4154 page: %v", respLatency)
	if len(wsDuringExport) != 1 || respLatency > 500*time.Millisecond {
		t.Fatalf("GUI froze during Export JPEG: latency=%v", respLatency)
	}

	// Wait for background JPEG export to write file to dataDir/exports and show Exported toast!
	d.until("Exported JPEG toast & file on disk", 25*time.Second, func() bool {
		return d.hasLabelContaining("Exported ")
	})
	finalShot := d.captureImage()
	assertDistinctPixels(t, finalShot, 12, "Final exported Arunika canvas")
}
