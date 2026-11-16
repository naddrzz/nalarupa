package agent

// The person's Claude Code CLI as the agent's account (claude_code.rs): whether it is signed in, and its own
// sign-in and sign-out. Nalarupa only starts the CLI's flows (`claude auth login` opens the browser); it never
// reads or stores the credentials, which stay with Claude Code.

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/model"
)

// signIn is a running `claude auth login`: its generation and process, and done, closed when it exits.
type signIn struct {
	gen  uint64
	cmd  *exec.Cmd
	done chan struct{}
}

func (s *signIn) running() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// login is the running sign-in, so a second click does not start another, Cancel can stop it, and only the
// watcher of the current one reports.
var login struct {
	sync.Mutex
	cur *signIn
}

var generation atomic.Uint64

// loginPoll is how often a sign-in's watcher checks whether it ended.
const loginPoll = 250 * time.Millisecond

// ClaudeStatus is `claude_auth_status`: `{installed, signedIn, method, account, plan}` from `claude auth status`
// (no model call).
func ClaudeStatus() jsonv.Value { return statusOf(claudeProgram) }

func statusOf(program string) jsonv.Value {
	out, err := command(program, "auth", "status").Output()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return engine.Object("installed", false, "signedIn", false, "method", nil, "account", nil, "plan", nil)
	}
	return parseStatus(model.Lossy(out), err == nil)
}

// parseStatus reads the CLI's JSON (`loggedIn`, `authMethod`, `email`, `subscriptionType`); it exits 1 when
// signed out.
func parseStatus(stdout string, success bool) jsonv.Value {
	v, err := jsonv.Parse([]byte(strings.TrimSpace(stdout)))
	if err != nil {
		v = nil
	}
	method, ok := jsonv.Str(jsonv.Get(v, "authMethod"))
	ok = ok && method != "none"
	signedIn := success && !jsonv.IsFalse(jsonv.Get(v, "loggedIn")) && ok
	var m, account, plan jsonv.Value
	if signedIn {
		m, account, plan = method, jsonv.Clone(jsonv.Get(v, "email")), jsonv.Clone(jsonv.Get(v, "subscriptionType"))
	}
	return engine.Object("installed", true, "signedIn", signedIn, "method", m, "account", account, "plan", plan)
}

// ClaudeLogin is `claude_auth_login`: start the CLI's own sign-in in the background (it opens the browser). When
// it exits, e gets a `claude_auth` event with the new status. console signs in to an Anthropic Console account
// (API billing).
func ClaudeLogin(e *engine.Engine, console bool) error {
	return loginWith(claudeProgram, console, func(state jsonv.Value) {
		e.Locked(func() { e.Emit(engine.Object("type", "claude_auth", "state", state)) })
	})
}

// loginWith is ClaudeLogin with the program and the report injected (tests use a stand-in CLI).
func loginWith(program string, console bool, report func(jsonv.Value)) error {
	login.Lock()
	defer login.Unlock()
	if login.cur != nil && login.cur.running() {
		return nil
	}
	args := []string{"auth", "login"}
	if console {
		args = append(args, "--console")
	}
	cmd := command(program, args...)
	if err := cmd.Start(); err != nil {
		text, missing := startError(err)
		if missing {
			return errors.New("Claude Code is not installed: install it (code.claude.com), then sign in here.")
		}
		return fmt.Errorf("could not start `claude auth login`: %s", text)
	}
	s := &signIn{gen: generation.Add(1), cmd: cmd, done: make(chan struct{})}
	go func() {
		cmd.Wait()
		close(s.done)
	}()
	login.cur = s
	go func() {
		defer func() { recover() }()
		for {
			time.Sleep(loginPoll)
			login.Lock()
			if login.cur != s {
				// Cancelled or replaced by a newer sign-in: that one reports.
				login.Unlock()
				return
			}
			if !s.running() {
				login.cur = nil
				login.Unlock()
				break
			}
			login.Unlock()
		}
		// Read outside every lock: a slow CLI must not hold anything else up.
		report(statusOf(program))
	}()
	return nil
}

// CancelLogin is `claude_auth_cancel`: stop a sign-in that is still waiting for the browser (also on app
// shutdown).
func CancelLogin() {
	login.Lock()
	defer login.Unlock()
	s := login.cur
	login.cur = nil
	if s != nil {
		s.cmd.Process.Kill()
		<-s.done
	}
}

// ClaudeLogout is `claude_auth_logout`: `claude auth logout`, then the new status.
func ClaudeLogout() (jsonv.Value, error) {
	_, err := command(claudeProgram, "auth", "logout").Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return nil, fmt.Errorf("`claude auth logout` failed: %s", strings.TrimSpace(model.Lossy(exit.Stderr)))
	}
	if err != nil {
		text, _ := startError(err)
		return nil, fmt.Errorf("could not run `claude auth logout`: %s", text)
	}
	return ClaudeStatus(), nil
}
