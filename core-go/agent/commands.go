package agent

import (
	"errors"
	"fmt"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// The agent's UI commands (lib.rs `dispatch`). They lock the engine themselves, so they join the dispatcher
// unlocked.
func init() {
	str := func(req jsonv.Value, k string) (string, bool) { return jsonv.Str(jsonv.Get(req, k)) }
	// required is a string argument, or the error Rust gives when it is missing.
	required := func(req jsonv.Value, k, missing string) (string, error) {
		s, ok := str(req, k)
		if !ok {
			return "", errors.New(missing)
		}
		return s, nil
	}
	done := func(err error) (jsonv.Value, error) {
		if err != nil {
			return nil, err
		}
		return true, nil
	}
	// u64Or is serde's `as_u64().unwrap_or(d)`.
	u64Or := func(req jsonv.Value, k string, d uint64) uint64 {
		if n, ok := jsonv.Get(req, k).(jsonv.Number); ok {
			if u, ok := n.Uint64(); ok {
				return u
			}
		}
		return d
	}
	orDefault := func(req jsonv.Value, k, d string) string {
		if s, ok := str(req, k); ok {
			return s
		}
		return d
	}
	engine.HandleUnlocked("agent_send", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		return done(Send(e, jsonv.StrOr(jsonv.Get(req, "text")), u64Or(req, "parallel", 1), orDefault(req, "mode", "split"),
			u64Or(req, "designs", 1), orDefault(req, "iterate", "layout")))
	})
	engine.HandleUnlocked("agent_stop", func(e *engine.Engine, _ jsonv.Value) (jsonv.Value, error) {
		Stop(e)
		return true, nil
	})
	engine.HandleUnlocked("agent_reset", func(e *engine.Engine, _ jsonv.Value) (jsonv.Value, error) { return done(Reset(e)) })
	engine.HandleUnlocked("agent_instructions", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		b, ok := str(req, "backend")
		switch {
		case !ok:
			return Instructions(Backend(e)), nil
		case b == "api" || b == "claude-code":
			return Instructions(b), nil
		case b == "provider":
			// A provider gets the API backend's instructions.
			return Instructions("api"), nil
		}
		return nil, fmt.Errorf("unknown backend '%s': api, claude-code or provider", b)
	})
	// Claude Code's own account: its state, and its own browser sign-in and sign-out.
	engine.HandleUnlocked("claude_auth_status", func(*engine.Engine, jsonv.Value) (jsonv.Value, error) { return ClaudeStatus(), nil })
	engine.HandleUnlocked("claude_auth_login", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		return done(ClaudeLogin(e, jsonv.IsTrue(jsonv.Get(req, "console"))))
	})
	engine.HandleUnlocked("claude_auth_cancel", func(*engine.Engine, jsonv.Value) (jsonv.Value, error) {
		CancelLogin()
		return true, nil
	})
	engine.HandleUnlocked("claude_auth_logout", func(*engine.Engine, jsonv.Value) (jsonv.Value, error) { return ClaudeLogout() })
	engine.HandleUnlocked("agent_permission", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		id, err := required(req, "id", "id required")
		if err != nil {
			return nil, err
		}
		answer, err := required(req, "answer", "answer required: allow, deny or auto")
		if err != nil {
			return nil, err
		}
		return done(AnswerPermission(e, id, answer))
	})
	engine.HandleUnlocked("chat_list", func(e *engine.Engine, _ jsonv.Value) (jsonv.Value, error) { return Chats(e), nil })
	engine.HandleUnlocked("chat_load", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		id, err := required(req, "id", "id required")
		if err != nil {
			return nil, err
		}
		return LoadChat(e, id)
	})
	engine.HandleUnlocked("chat_delete", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		id, err := required(req, "id", "id required")
		if err != nil {
			return nil, err
		}
		return done(DeleteChat(e, id))
	})
	engine.HandleUnlocked("chat_flush", func(e *engine.Engine, _ jsonv.Value) (jsonv.Value, error) { return done(Shutdown(e)) })
	engine.HandleUnlocked("chat_clear", func(e *engine.Engine, _ jsonv.Value) (jsonv.Value, error) {
		cleared, err := ClearChats(e)
		if err != nil {
			return nil, err
		}
		return cleared, nil
	})
	engine.HandleUnlocked("mcp_install", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		var port uint16
		e.Locked(func() { port = e.MCPPort })
		text, err := InstallMCP(jsonv.StrOr(jsonv.Get(req, "client")), port)
		if err != nil {
			return nil, err
		}
		return text, nil
	})
}
