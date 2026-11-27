package agent

// The `claude-code` backend (agent.rs `run_claude_code`): the user's `claude` CLI in print mode with
// partial-message streaming, wired to the MCP server through a per-run config file.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/model"
	"github.com/naddrzz/nalarupa/core-go/script"
)

// claudeProgram is the Claude Code CLI (tests point it at a stand-in).
var claudeProgram = "claude"

// mcpConfigStale: a config older than this was left by a session that died before its run ended.
const mcpConfigStale = 24 * time.Hour

// killPoll is how often the watcher checks whether a run was stopped.
const killPoll = 150 * time.Millisecond

// noSession is what the CLI reports for a `--resume` id it has no session for.
const noSession = "No conversation found with session ID"

// SignedOut is the error a signed-out CLI gets: its card then offers Sign in.
const SignedOut = "Claude Code is not signed in. Sign in from Agent settings (gear icon), or switch the backend to the Claude API there."

// errorMax is the longest error text kept in the chat card and the saved chat (tool output is capped at 6000).
const errorMax = 2000

// SweepMCPConfigs deletes the per-run MCP configs of dead sessions in dir; younger ones may belong to another
// running instance.
func SweepMCPConfigs(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "claude-code-mcp-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > mcpConfigStale {
			doc.RemoveFile(doc.JoinPath(dir, name))
		}
	}
}

// nextRun numbers the MCP config files of this process.
var nextRun atomic.Uint64

// MCPConfig writes the MCP client config for one run of agent's Claude Code process (the per-agent URL is the
// attribution) and returns its path. The caller deletes it when the run ends, however it ends: the CLI reads it
// only at start, and its port belongs to this process alone.
func MCPConfig(e *engine.Engine, agent string) (string, error) {
	var port uint16
	var dir string
	e.Locked(func() { port, dir = e.MCPPort, e.DataDir })
	if port == 0 {
		return "", errors.New("the MCP server is not running")
	}
	SweepMCPConfigs(dir)
	// One file per run: the agent id is reused, so a concurrent run of it must not share (and delete) the file.
	path := doc.JoinPath(dir, fmt.Sprintf("claude-code-mcp-%s-%d-%d.json", agent, os.Getpid(), nextRun.Add(1)-1))
	cfg := engine.Object("mcpServers", engine.Object("nalarupa", engine.Object("type", "http", "url", fmt.Sprintf("http://127.0.0.1:%d/mcp/%s", port, agent))))
	if err := os.WriteFile(path, jsonv.Marshal(cfg), 0o666); err != nil {
		return "", errors.New(doc.IOError(err))
	}
	return path, nil
}

// startError is the failure to start program, worded as Rust's io::Error.
func startError(err error) (string, bool) {
	if errors.Is(err, exec.ErrNotFound) {
		if runtime.GOOS == "windows" {
			return "program not found", true
		}
		return doc.IOError(syscall.ENOENT), true
	}
	return doc.IOError(err), errors.Is(err, os.ErrNotExist)
}

// exitStatus is Rust's `ExitStatus` display.
func exitStatus(ps *os.ProcessState) string {
	ws, _ := ps.Sys().(syscall.WaitStatus)
	if runtime.GOOS == "windows" {
		code := uint32(ps.ExitCode())
		if code&0x8000_0000 != 0 {
			return fmt.Sprintf("exit code: %#x", code)
		}
		return fmt.Sprintf("exit code: %d", code)
	}
	if ws.Signaled() {
		s := fmt.Sprintf("signal: %d", int(ws.Signal()))
		if name, ok := signalNames[int(ws.Signal())]; ok {
			s += " (" + name + ")"
		}
		if ws.CoreDump() {
			s += " (core dumped)"
		}
		return s
	}
	return fmt.Sprintf("exit status: %d", ps.ExitCode())
}

// signalNames are the Linux names of the signals that end a process most often.
var signalNames = map[int]string{1: "SIGHUP", 2: "SIGINT", 3: "SIGQUIT", 4: "SIGILL", 6: "SIGABRT", 7: "SIGBUS", 8: "SIGFPE", 9: "SIGKILL", 11: "SIGSEGV", 13: "SIGPIPE", 15: "SIGTERM"}

// ccTool is a tool call of the message being streamed: its id, name and partial input JSON.
type ccTool struct{ id, name, buf string }

// ccInput is a finished tool call waiting for its result.
type ccInput struct {
	name  string
	input jsonv.Value
}

func runClaudeCode(e *engine.Engine, p engine.Profile, prompt string, child bool, name string) (*jsonv.Object, error) {
	return runClaudeCodeStep(e, p, prompt, child, name, "", false)
}

func mergeClaudeUsage(base, extra *jsonv.Object) *jsonv.Object {
	if base == nil || base.Len() == 0 {
		return extra
	}
	if extra == nil || extra.Len() == 0 {
		return base
	}
	out := jsonv.Obj(jsonv.Clone(base))
	for _, k := range []string{"input", "output", "cacheRead", "cacheWrite", "turns"} {
		a, okA := jsonv.F64(jsonv.Get(out, k))
		b, okB := jsonv.F64(jsonv.Get(extra, k))
		if okA || okB {
			out.Set(k, jsonv.Int(int64(a+b)))
		}
	}
	if cA, okA := jsonv.F64(jsonv.Get(out, "costUsd")); okA {
		if cB, okB := jsonv.F64(jsonv.Get(extra, "costUsd")); okB {
			out.Set("costUsd", jsonv.Number(fmt.Sprintf("%.6f", cA+cB)))
		}
	} else if v, ok := extra.Get("costUsd"); ok && v != nil {
		out.Set("costUsd", jsonv.Clone(v))
	}
	return out
}

func runClaudeCodeStep(e *engine.Engine, p engine.Profile, prompt string, child bool, name, overrideSession string, isReviseContinuation bool) (*jsonv.Object, error) {
	cfg, err := MCPConfig(e, p.ID)
	if err != nil {
		return nil, err
	}
	defer doc.RemoveFile(cfg)
	var session, state, effort string
	var cancel *engine.Flag
	var auto, fast bool
	var skillDirs []string
	e.Locked(func() {
		if !child {
			session = e.Agent.Session
			if !isReviseContinuation {
				state = e.EditorState()
			}
		}
		if overrideSession != "" {
			session = overrideSession
		}
		skillDirs = append([]string(nil), e.Agent.ActiveSkillDirs...)
		cancel = e.Agent.Cancel
		fast = fastFor(name, jsonv.IsTrue(jsonv.Get(e.Settings, "agent_fast")))
		auto = jsonv.IsTrue(jsonv.Get(e.Settings, "agent_auto_permissions"))
		if eff, ok := jsonv.Str(jsonv.Get(e.Settings, "agent_effort")); ok {
			effort = strings.ToLower(strings.TrimSpace(eff))
		}
	})
	full := prompt
	if !child && !isReviseContinuation {
		full = prompt + editorStateNote + state
	}
	// The prompt goes in on stdin: as an argument a long message or selection overflows the OS limit.
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--strict-mcp-config",
		"--mcp-config", cfg,
		// Pre-allowed set: the canvas tools, web search/fetch, and Claude Skills. Every other tool asks the user
		// through the MCP permission tool (a permission card).
		"--allowedTools", "mcp__nalarupa,WebSearch,WebFetch,Skill",
		"--disallowedTools", "Task,Agent,AskUserQuestion",
		"--permission-prompt-tool", "mcp__nalarupa__" + PermissionTool,
		"--append-system-prompt", Instructions("claude-code")}
	if session != "" {
		args = append(args, "--resume", session)
	}
	if auto {
		args = append(args, "--permission-mode", "auto")
	}
	// Unset is the CLI's own default model. Fast mode is per session (Claude Code v2.1.205+).
	if name != "" {
		args = append(args, "--model", name)
	}
	if fast {
		args = append(args, "--settings", `{"fastMode":true}`)
	}
	switch effort {
	case "low", "medium", "high", "xhigh", "max":
		args = append(args, "--effort", effort)
	}
	for _, dir := range skillDirs {
		if dir != "" {
			args = append(args, "--add-dir", dir)
		}
	}
	cmd := command(claudeProgram, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("could not start the `claude` CLI (%s)", doc.IOError(err))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("could not start the `claude` CLI (%s)", doc.IOError(err))
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("could not start the `claude` CLI (%s)", doc.IOError(err))
	}
	if err := cmd.Start(); err != nil {
		text, missing := startError(err)
		if missing {
			return nil, fmt.Errorf("could not start the `claude` CLI (%s). Install Claude Code or switch the backend to the Claude API.", text)
		}
		return nil, fmt.Errorf("could not start the `claude` CLI (%s)", text)
	}
	// A goroutine writes it, so a prompt larger than the pipe buffer cannot block before output is read; closing
	// the pipe sends EOF.
	sent := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				sent <- fmt.Errorf("writer panicked: %v", r)
			}
		}()
		_, err := io.WriteString(stdin, full)
		if cerr := stdin.Close(); err == nil {
			err = cerr
		}
		sent <- err
	}()
	var finished atomic.Bool
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		defer func() { recover() }()
		for !finished.Load() {
			if cancel.IsSet() {
				cmd.Process.Kill()
				return
			}
			time.Sleep(killPoll)
		}
	}()
	errText := make(chan string, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errText <- ""
			}
		}()
		// Bytes, not text: a localized shell message in the OEM code page is not UTF-8 and would be dropped whole.
		b, _ := io.ReadAll(stderr)
		errText <- model.Lossy(b)
	}()

	tools := map[uint64]*ccTool{}
	inputs := map[string]ccInput{}
	lastPreview := map[string]time.Time{}
	usage := jsonv.NewObject()
	var turnCtx claudeTurnCtx
	reported, textless, stale := false, false, false
	// Split on bytes: a line that is not UTF-8 must not end the reply and leave the CLI on a closed pipe.
	r := bufio.NewReader(stdout)
	for {
		line, rerr := r.ReadBytes('\n')
		if len(line) > 0 || rerr == nil {
			line = []byte(strings.TrimSuffix(string(line), "\n"))
			if ev, err := jsonv.Parse([]byte(model.Lossy(line))); err == nil {
				usage, reported, textless, stale = claudeEvent(e, p, child, session, ev, tools, inputs, lastPreview, usage, &turnCtx, reported, textless, stale)
			}
		}
		if rerr != nil {
			break
		}
	}
	finished.Store(true)
	watcher.Wait()
	stderrText := <-errText
	waitErr := cmd.Wait()
	measuredUsage := func() *jsonv.Object {
		if usage != nil && usage.Len() > 0 {
			return usage
		}
		return nil
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return measuredUsage(), errors.New(doc.IOError(waitErr))
	}
	// The CLI no longer knows the saved session (another working folder, a cleaned `~/.claude`): the id would fail
	// every later message too, so forget it and run this prompt again as a fresh session.
	if session != "" && (stale || strings.Contains(stderrText, noSession)) {
		if !child {
			e.Locked(func() { e.Agent.Session = "" })
		}
		if !cancel.IsSet() {
			emit(e, p, "note", engine.Object("text", "Claude Code could no longer find this chat's earlier session, so the earlier context is gone. Continuing in a new session."))
			return runClaudeCodeStep(e, p, prompt, child, name, "", isReviseContinuation)
		}
	}
	sendErr := <-sent
	if !cancel.IsSet() && !cmd.ProcessState.Success() {
		return measuredUsage(), errors.New(exitError(exitStatus(cmd.ProcessState), stderrText, reported))
	}
	// A clean exit after a cut-off prompt would otherwise pass as a finished run.
	if sendErr != nil && !cancel.IsSet() {
		return measuredUsage(), fmt.Errorf("could not send the prompt to the `claude` CLI (%s)", doc.IOError(sendErr))
	}
	if textless && !cancel.IsSet() {
		emit(e, p, "error", engine.Object("text", "Claude Code reported an error"))
		return usage, nil
	}
	if !cancel.IsSet() {
		if revPrompt := RunTurnEndTypeSafeGate(e, p); revPrompt != "" {
			nextUsage, revErr := runClaudeCodeStep(e, p, revPrompt, child, name, turnCtx.sessionID, true)
			return mergeClaudeUsage(usage, nextUsage), revErr
		}
	}
	return usage, nil
}

type claudeTurnCtx struct {
	sessionID  string
	lastInput  int
	lastOutput int
}

// claudeEvent handles one line of the CLI's stream-json output.
func claudeEvent(e *engine.Engine, p engine.Profile, child bool, session string, ev jsonv.Value, tools map[uint64]*ccTool, inputs map[string]ccInput,
	lastPreview map[string]time.Time, usage *jsonv.Object, turnCtx *claudeTurnCtx, reported, textless, stale bool) (*jsonv.Object, bool, bool, bool) {
	if id, ok := jsonv.Str(jsonv.Get(ev, "session_id")); ok && id != "" {
		if turnCtx != nil {
			turnCtx.sessionID = id
		}
		if !child {
			e.Locked(func() { e.Agent.Session = id })
		}
	}
	switch jsonv.StrOr(jsonv.Get(ev, "type")) {
	case "stream_event":
		se := jsonv.Get(ev, "event")
		idx := asU64(jsonv.Get(se, "index"))
		switch jsonv.StrOr(jsonv.Get(se, "type")) {
		case "message_start":
			clear(tools)
			if !child {
				mu := jsonv.Get(jsonv.Get(se, "message"), "usage")
				inTok := int(asU64(jsonv.Get(mu, "input_tokens")) + asU64(jsonv.Get(mu, "cache_read_input_tokens")) + asU64(jsonv.Get(mu, "cache_creation_input_tokens")))
				outTok := int(asU64(jsonv.Get(mu, "output_tokens")))
				if inTok > 0 {
					if turnCtx != nil {
						turnCtx.lastInput = inTok
						turnCtx.lastOutput = outTok
					}
					var lim int
					e.Locked(func() { lim = e.EffectiveContextWindow() })
					emitContextUsage(e, p, false, inTok+outTok, lim)
				}
			}
		case "message_delta":
			if !child && turnCtx != nil && turnCtx.lastInput > 0 {
				du := jsonv.Get(se, "usage")
				if outTok := int(asU64(jsonv.Get(du, "output_tokens"))); outTok > 0 {
					turnCtx.lastOutput = outTok
					var lim int
					e.Locked(func() { lim = e.EffectiveContextWindow() })
					emitContextUsage(e, p, false, turnCtx.lastInput+turnCtx.lastOutput, lim)
				}
			}
		case "content_block_start":
			block := jsonv.Get(se, "content_block")
			if jsonv.StrIs(jsonv.Get(block, "type"), "tool_use") {
				id := jsonv.StrOr(jsonv.Get(block, "id"))
				name := shortTool(jsonv.StrOr(jsonv.Get(block, "name")))
				emit(e, p, "tool_start", engine.Object("id", id, "name", name))
				tools[idx] = &ccTool{id: id, name: name}
			}
		case "content_block_delta":
			d := jsonv.Get(se, "delta")
			switch jsonv.StrOr(jsonv.Get(d, "type")) {
			case "text_delta":
				emit(e, p, "text_delta", engine.Object("text", jsonv.Clone(jsonv.Get(d, "text"))))
			case "thinking_delta":
				emit(e, p, "thinking_delta", engine.Object("text", jsonv.Clone(jsonv.Get(d, "thinking"))))
			case "input_json_delta":
				t := tools[idx]
				if t == nil {
					break
				}
				t.buf += jsonv.StrOr(jsonv.Get(d, "partial_json"))
				last, seen := lastPreview[t.id]
				if t.name == "execute" && (!seen || time.Since(last) >= streamThrottle) {
					if src, ok := script.PartialString(t.buf, "input"); ok {
						e.Locked(func() { e.StreamUpdate(t.id, p.ID, src) })
						emit(e, p, "tool_progress", engine.Object("id", t.id, "lines", jsonv.Int(int64(len(lines(src))))))
					}
					lastPreview[t.id] = time.Now()
				}
			}
		case "message_stop":
			emit(e, p, "message_end", engine.Object("t", u64(engine.NowMs())))
		}
	case "assistant":
		for _, b := range jsonv.Items(jsonv.Get(jsonv.Get(ev, "message"), "content")) {
			if !jsonv.StrIs(jsonv.Get(b, "type"), "tool_use") {
				continue
			}
			id := jsonv.StrOr(jsonv.Get(b, "id"))
			name := shortTool(jsonv.StrOr(jsonv.Get(b, "name")))
			if name == "TodoWrite" {
				emit(e, p, "todos", engine.Object("todos", jsonv.Clone(jsonv.Get(jsonv.Get(b, "input"), "todos"))))
			}
			inputs[id] = ccInput{name, jsonv.Clone(jsonv.Get(b, "input"))}
		}
	case "user":
		for _, b := range jsonv.Items(jsonv.Get(jsonv.Get(ev, "message"), "content")) {
			if !jsonv.StrIs(jsonv.Get(b, "type"), "tool_result") {
				continue
			}
			id := jsonv.StrOr(jsonv.Get(b, "tool_use_id"))
			in := inputs[id]
			delete(inputs, id)
			e.Locked(func() { e.StreamEnd(id) })
			text, images := toolResultParts(jsonv.Get(b, "content"))
			emit(e, p, "tool_done", toolDoneEvent(id, in.name, in.input, text, images, jsonv.IsTrue(jsonv.Get(b, "is_error"))))
		}
	case "result":
		u := jsonv.Get(ev, "usage")
		c := func(v jsonv.Value, k string) jsonv.Value { return jsonv.Clone(jsonv.Get(v, k)) }
		usage = engine.Object("input", c(u, "input_tokens"), "output", c(u, "output_tokens"), "cacheRead", c(u, "cache_read_input_tokens"),
			"cacheWrite", c(u, "cache_creation_input_tokens"), "turns", c(ev, "num_turns"), "costUsd", c(ev, "total_cost_usd"))
		if !child {
			ctxUsed := 0
			if turnCtx != nil && turnCtx.lastInput > 0 {
				outTok := turnCtx.lastOutput
				if outTok == 0 && asU64(jsonv.Get(ev, "num_turns")) <= 1 {
					outTok = int(asU64(jsonv.Get(u, "output_tokens")))
				}
				ctxUsed = turnCtx.lastInput + outTok
			} else if asU64(jsonv.Get(ev, "num_turns")) <= 1 {
				ctxUsed = int(asU64(jsonv.Get(u, "input_tokens")) + asU64(jsonv.Get(u, "cache_read_input_tokens")) + asU64(jsonv.Get(u, "cache_creation_input_tokens")) + asU64(jsonv.Get(u, "output_tokens")))
			}
			if ctxUsed > 0 {
				var lim int
				e.Locked(func() { lim = e.EffectiveContextWindow() })
				emitContextUsage(e, p, false, ctxUsed, lim)
			}
		}
		if jsonv.IsTrue(jsonv.Get(ev, "is_error")) {
			// Without text the card would hide the real cause; leave that to the exit error (stderr).
			text, ok := errorCause(ev)
			switch {
			case ok && session != "" && strings.Contains(text, noSession):
				stale = true
			case ok:
				emit(e, p, "error", engine.Object("text", text, "signIn", signedOut(text)))
				reported = true
			default:
				textless = true
			}
		}
	}
	return usage, reported, textless, stale
}

// errorCause is the cause an error `result` event carries: its `result` text, else its `errors` list; false
// when it has neither.
func errorCause(ev jsonv.Value) (string, bool) {
	text := ""
	if r, ok := jsonv.Str(jsonv.Get(ev, "result")); ok {
		text = strings.TrimSpace(r)
	}
	if text == "" {
		var errs []string
		for _, v := range jsonv.Items(jsonv.Get(ev, "errors")) {
			if s, ok := jsonv.Str(v); ok && strings.TrimSpace(s) != "" {
				errs = append(errs, strings.TrimSpace(s))
			}
		}
		text = strings.Join(errs, "\n")
	}
	if text == "" {
		return "", false
	}
	if hint, ok := signInHint(text); ok {
		return hint, true
	}
	return clipEnds(text, errorMax), true
}

// signInHint: the CLI tells a signed-out user to run `/login`, a command of its own terminal UI; point at what
// works here. Checked on the whole text, before clipping can drop the phrase; a bare path like `/login.js` is no
// match.
func signInHint(text string) (string, bool) {
	t := strings.ToLower(text)
	if strings.Contains(t, "run /login") || strings.Contains(t, "not logged in") {
		return SignedOut, true
	}
	return "", false
}

// signedOut is whether a run's error is the signed-out CLI: its card then offers Sign in.
func signedOut(err string) bool { return err == SignedOut }

// exitError is the failure for a non-zero exit. Empty when an error result already showed the cause, so the run
// ends without a second card; it never ends in a bare colon.
func exitError(status, stderr string, reported bool) string {
	stderr = strings.TrimSpace(stderr)
	prefix := "claude exited with " + status
	switch {
	case reported:
		return ""
	case stderr == "":
		return prefix
	}
	if hint, ok := signInHint(stderr); ok {
		return hint
	}
	return prefix + ": " + clipEnds(stderr, max(errorMax-(len(prefix)+2), 0))
}

func shortTool(name string) string {
	for strings.HasPrefix(name, "mcp__nalarupa__") {
		name = strings.TrimPrefix(name, "mcp__nalarupa__")
	}
	return name
}

// toolResultParts is a tool result's text and (at most 3) images.
func toolResultParts(content jsonv.Value) (string, []string) {
	switch c := content.(type) {
	case string:
		return c, nil
	case *jsonv.Array:
		var text, images []string
		for _, it := range c.Items {
			if s, ok := jsonv.Str(jsonv.Get(it, "text")); ok {
				text = append(text, s)
			}
		}
		for _, it := range c.Items {
			s, ok := jsonv.Str(jsonv.Get(jsonv.Get(it, "source"), "data"))
			if !ok {
				s, ok = jsonv.Str(jsonv.Get(it, "data"))
			}
			if ok && len(images) < 3 {
				images = append(images, s)
			}
		}
		return strings.Join(text, "\n"), images
	}
	return "", nil
}
