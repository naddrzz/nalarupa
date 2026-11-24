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
	"slices"
	"time"
)

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

	// Build fresh binary
	buildCmd := exec.Command("bash", "build.sh")
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
	if _, err := os.Stat(binPath + ".exe"); err == nil {
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

	fmt.Println("shots: captured static screenshots; pass live Claude Code session for agent screenshots")
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
	_, err := c.call("idle", map[string]any{"timeout": ms})
	return err
}

func (c *autoRPC) pressKey(key string) error {
	_, err := c.call("act", map[string]any{"key": key, "action": "press"})
	return err
}

func (c *autoRPC) capturePNG(dest string) error {
	res, err := c.call("capture", map[string]any{})
	if err != nil {
		return err
	}
	b64, _ := res["png"].(string)
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, raw, 0o644)
}
