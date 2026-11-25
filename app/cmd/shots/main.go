// Regenerates README screenshots in ../docs/screenshots/ using an isolated temporary NALARUPA_DATA
// directory so personal recent files or saved chats never leak into committed screenshots.
//
// Usage:
//   CGO_ENABLED=1 go run ./cmd/shots [--static-only]
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

const liveAgentShotPrompt = "Landing page for Halden, a specialty coffee roastery in Oslo: origin notes, roast schedule, and a seasonal tasting box."

func main() {
	if err := runShots(slices.Contains(os.Args[1:], "--static-only")); err != nil {
		fmt.Fprintf(os.Stderr, "shots: %v\n", err)
		os.Exit(1)
	}
}

func runShots(staticOnly bool) error {
	appDir, err := os.Getwd()
	if err != nil {
		return err
	}
	shotsDir := filepath.Join(appDir, "..", "docs", "screenshots")
	if err := os.MkdirAll(shotsDir, 0o755); err != nil {
		return err
	}

	// Build fresh binary using platform script
	var buildCmd *exec.Cmd
	if runtime.GOOS == "windows" {
		buildCmd = exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "build.ps1")
	} else {
		buildCmd = exec.Command("bash", "build.sh")
	}
	buildCmd.Dir = appDir
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	// Fresh isolated NALARUPA_DATA (never copying settings.json or sessions/)
	tmpData, err := os.MkdirTemp("", "nalarupa-shots-data-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpData)

	tmpSockDir, err := os.MkdirTemp("", "nalarupa-shots-sock-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpSockDir)
	sockPath := filepath.Join(tmpSockDir, "auto.sock")

	binPath := filepath.Join(appDir, "dist", "nalarupa")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	} else if _, err := os.Stat(binPath + ".exe"); err == nil {
		binPath += ".exe"
	}

	proc := exec.Command(binPath)
	proc.Env = append(os.Environ(),
		"NALARUPA_DATA="+tmpData,
		"MYGO_AUTOMATION="+sockPath,
	)
	if err := proc.Start(); err != nil {
		return err
	}
	defer func() {
		_ = proc.Process.Kill()
		_ = proc.Wait()
	}()

	conn, err := dialSocket(sockPath, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	client := &autoRPC{conn: conn, r: bufio.NewReader(conn)}

	_ = client.idle(3000)
	if err := client.capturePNG(filepath.Join(shotsDir, "home.png")); err != nil {
		return err
	}

	if err := client.pressKey("template-slides"); err != nil {
		return err
	}
	_ = client.idle(3000)
	if err := client.pressKey("tab-slides"); err != nil {
		return err
	}
	_ = client.idle(2000)
	if err := client.pressKey("fit"); err != nil {
		return err
	}
	_ = client.idle(2000)
	if err := client.capturePNG(filepath.Join(shotsDir, "slides.png")); err != nil {
		return err
	}

	if err := client.pressKey("present"); err != nil {
		return err
	}
	_ = client.idle(2000)
	if err := client.capturePNG(filepath.Join(shotsDir, "present.png")); err != nil {
		return err
	}

	if staticOnly {
		fmt.Println("shots: captured home.png, slides.png, present.png (--static-only)")
		return nil
	}

	// Live agent screenshot flow: exit Present -> Home -> empty canvas -> 2x Side by side -> prompt -> agent.png & design.png
	if err := client.sendKey("Escape"); err != nil {
		return err
	}
	_ = client.idle(1500)
	if err := client.pressKey("home"); err != nil {
		return err
	}
	_ = client.idle(1500)
	if err := client.pressKey("template-empty"); err != nil {
		return err
	}
	_ = client.idle(2000)
	if err := client.pressKey("parallel-2"); err != nil {
		return err
	}
	_ = client.idle(1000)
	if err := client.pressKey("mode-side"); err != nil {
		return err
	}
	_ = client.idle(1000)
	if err := client.setValue("agent-input", liveAgentShotPrompt); err != nil {
		return err
	}
	if err := client.pressKey("agent-send"); err != nil {
		return err
	}

	liveDir := filepath.Join(shotsDir, "live")
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		return err
	}

	// Wait for agent run to start or fail fast (e.g. not signed in)
	started := false
	startDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(startDeadline) {
		keys, err := client.treeKeys()
		if err != nil {
			return err
		}
		if keys["error-sign-in"] {
			return fmt.Errorf("Claude Code is not signed in; sign in first or pass --static-only")
		}
		if keys["agent-stop"] {
			started = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if started {
		runDeadline := time.Now().Add(15 * time.Minute)
		frameIdx := 0
		finished := false
		for time.Now().Before(runDeadline) {
			keys, err := client.treeKeys()
			if err != nil {
				return err
			}
			if keys["error-sign-in"] {
				return fmt.Errorf("Claude Code sign-in required during live screenshot run")
			}
			if !keys["agent-stop"] {
				finished = true
				break
			}
			frameIdx++
			_ = client.capturePNG(filepath.Join(liveDir, fmt.Sprintf("agent-%02d.png", frameIdx)))
			time.Sleep(1500 * time.Millisecond)
		}
		if !finished {
			return fmt.Errorf("timed out waiting for live agent screenshot run to finish")
		}
	}

	_ = client.idle(2000)
	if err := client.pressKey("fit"); err != nil {
		return err
	}
	_ = client.idle(2000)
	if err := client.capturePNG(filepath.Join(shotsDir, "agent.png")); err != nil {
		return err
	}

	// Select first top-level layer and switch to Design inspector tab for design.png
	if firstLayer, err := client.firstLayerKey(); err == nil && firstLayer != "" {
		_ = client.pressKey(firstLayer)
		_ = client.idle(1500)
	}
	if err := client.pressKey("tab-design"); err != nil {
		return err
	}
	_ = client.idle(2000)
	if err := client.capturePNG(filepath.Join(shotsDir, "design.png")); err != nil {
		return err
	}

	fmt.Println("shots: captured home.png, slides.png, present.png, agent.png, design.png")
	return nil
}

func dialSocket(path string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err == nil {
			return conn, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("timed out waiting for MYGO_AUTOMATION socket %s", path)
}

type autoRPC struct {
	conn net.Conn
	r    *bufio.Reader
	seq  int
}

func (c *autoRPC) call(method string, params map[string]any) (map[string]any, error) {
	c.seq++
	req := map[string]any{
		"id":     c.seq,
		"method": method,
		"params": params,
	}
	b, _ := json.Marshal(req)
	b = append(b, '\n')
	_ = c.conn.SetDeadline(time.Now().Add(60 * time.Second))
	if _, err := c.conn.Write(b); err != nil {
		return nil, err
	}
	line, err := c.r.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	var resp struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("%s: %s", method, resp.Error.Message)
	}
	return resp.Result, nil
}

func (c *autoRPC) idle(ms int) error {
	_, err := c.call("idle", map[string]any{"window": 0, "timeout": ms})
	return err
}

func (c *autoRPC) pressKey(key string) error {
	_, err := c.call("act", map[string]any{"window": 0, "key": key, "action": "press"})
	return err
}

func (c *autoRPC) setValue(key, text string) error {
	if _, err := c.call("act", map[string]any{"window": 0, "key": key, "action": "focus"}); err != nil {
		return err
	}
	_, err := c.call("act", map[string]any{"window": 0, "key": key, "action": "setValue", "text": text})
	return err
}

func (c *autoRPC) sendKey(accel string) error {
	_, err := c.call("input", map[string]any{
		"window": 0,
		"events": []map[string]any{
			{"type": "key", "key": accel},
		},
	})
	return err
}

func (c *autoRPC) treeKeys() (map[string]bool, error) {
	res, err := c.call("tree", map[string]any{"window": 0})
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	nodes, _ := res["nodes"].([]any)
	for _, raw := range nodes {
		if m, ok := raw.(map[string]any); ok {
			if k, ok := m["key"].(string); ok && k != "" {
				keys[k] = true
			}
		}
	}
	return keys, nil
}

func (c *autoRPC) firstLayerKey() (string, error) {
	res, err := c.call("tree", map[string]any{"window": 0})
	if err != nil {
		return "", err
	}
	nodes, _ := res["nodes"].([]any)
	for _, raw := range nodes {
		if m, ok := raw.(map[string]any); ok {
			if k, ok := m["key"].(string); ok && strings.HasPrefix(k, "layer-") && !strings.HasPrefix(k, "layer-toggle-") && !strings.HasPrefix(k, "layer-eye-") && k != "layer-rename" {
				return k, nil
			}
		}
	}
	return "", nil
}

func (c *autoRPC) capturePNG(dest string) error {
	res, err := c.call("capture", map[string]any{"window": 0})
	if err != nil {
		return err
	}
	b64, ok := res["png"].(string)
	if !ok || b64 == "" {
		return fmt.Errorf("capture returned missing or empty png field")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return fmt.Errorf("capture decoded 0-byte screenshot")
	}
	return os.WriteFile(dest, raw, 0o644)
}
