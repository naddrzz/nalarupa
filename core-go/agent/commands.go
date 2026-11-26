package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/model"
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
	engine.HandleUnlocked("models_fetch", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		target := strings.TrimSpace(orDefault(req, "target", "provider"))
		provID := strings.TrimSpace(jsonv.StrOr(jsonv.Get(req, "id")))
		format := strings.TrimSpace(jsonv.StrOr(jsonv.Get(req, "format")))
		baseURL := strings.TrimSpace(jsonv.StrOr(jsonv.Get(req, "base_url")))
		apiKey := strings.TrimSpace(jsonv.StrOr(jsonv.Get(req, "api_key")))
		reqID := strings.TrimSpace(jsonv.StrOr(jsonv.Get(req, "req_id")))
		persist := jsonv.IsTrue(jsonv.Get(req, "persist"))

		normEndpoint := func(u, def string) string {
			u = strings.TrimRight(strings.TrimSpace(u), "/")
			if u == "" {
				return strings.TrimRight(def, "/")
			}
			return u
		}

		var precheckErr error
		var savedBase string
		e.Locked(func() {
			if target == "api" {
				if format == "" {
					format = "anthropic"
				}
				savedBase, _ = e.Setting("anthropic_base_url")
				if baseURL == "" {
					baseURL = savedBase
				}
				if apiKey == "" || apiKey == engine.Mask {
					if normEndpoint(baseURL, "https://api.anthropic.com") != normEndpoint(savedBase, "https://api.anthropic.com") {
						if _, hasSaved := e.APIKey(); hasSaved {
							precheckErr = errors.New("Enter the API key for the edited Base URL before fetching models.")
							return
						}
					} else {
						apiKey, _ = e.APIKey()
					}
				}
				return
			}
			for _, p := range jsonv.Items(jsonv.Get(e.Settings, "providers")) {
				if provID != "" && jsonv.StrIs(jsonv.Get(p, "id"), provID) {
					if format == "" {
						format = jsonv.StrOr(jsonv.Get(p, "format"))
					}
					savedBase = strings.TrimSpace(jsonv.StrOr(jsonv.Get(p, "base_url")))
					if baseURL == "" {
						baseURL = savedBase
					}
					if apiKey == "" || apiKey == engine.Mask {
						savedKey := strings.TrimSpace(jsonv.StrOr(jsonv.Get(p, "api_key")))
						if normEndpoint(baseURL, "") != normEndpoint(savedBase, "") {
							if savedKey != "" {
								precheckErr = errors.New("Base URL changed; enter the API key for this endpoint before fetching models.")
								return
							}
						} else {
							apiKey = savedKey
						}
					}
					break
				}
			}
		})
		if precheckErr != nil {
			return nil, precheckErr
		}
		if target != "api" && baseURL == "" {
			return nil, errors.New("A base URL is required to fetch models.")
		}
		if apiKey == engine.Mask {
			apiKey = ""
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					e.Locked(func() {
						e.Emit(engine.Object("type", "models_fetched", "target", target, "id", provID, "req_id", reqID, "ok", false,
							"models", engine.Strings(nil), "error", fmt.Sprintf("internal error (panic): %v", r)))
					})
				}
			}()
			models, err := model.FetchModels(format, baseURL, apiKey)
			e.Locked(func() {
				if err != nil {
					e.Emit(engine.Object("type", "models_fetched", "target", target, "id", provID, "req_id", reqID, "ok", false,
						"models", engine.Strings(nil), "error", err.Error()))
					return
				}
				var saveErr error
				if target == "api" && len(models) > 0 {
					saveErr = e.SetSetting("anthropic_models", engine.Strings(models))
				} else if target == "provider" && persist && provID != "" && len(models) > 0 {
					list := jsonv.Arr(jsonv.Clone(jsonv.Get(e.Settings, "providers")))
					if list != nil {
						for i, p := range list.Items {
							if jsonv.StrIs(jsonv.Get(p, "id"), provID) &&
								normEndpoint(jsonv.StrOr(jsonv.Get(p, "base_url")), "") == normEndpoint(baseURL, "") {
								seen := map[string]bool{}
								var merged []string
								for _, em := range jsonv.Items(jsonv.Get(p, "models")) {
									if s, ok := jsonv.Str(em); ok && strings.TrimSpace(s) != "" && !seen[strings.TrimSpace(s)] {
										s = strings.TrimSpace(s)
										seen[s] = true
										merged = append(merged, s)
									}
								}
								for _, nm := range models {
									nm = strings.TrimSpace(nm)
									if nm != "" && !seen[nm] {
										seen[nm] = true
										merged = append(merged, nm)
									}
								}
								pObj := jsonv.Obj(jsonv.Clone(p))
								if pObj != nil {
									pObj.Set("models", engine.Strings(merged))
									list.Items[i] = pObj
									saveErr = e.SetSetting("providers", list)
								}
								break
							}
						}
					}
				}
				if saveErr != nil {
					e.Emit(engine.Object("type", "models_fetched", "target", target, "id", provID, "req_id", reqID, "ok", false,
						"models", engine.Strings(nil), "error", saveErr.Error()))
					return
				}
				e.Emit(engine.Object("type", "models_fetched", "target", target, "id", provID, "req_id", reqID, "ok", true,
					"models", engine.Strings(models), "error", ""))
			})
		}()
		return true, nil
	})
	engine.HandleUnlocked("skills_list", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		var docPath string
		e.Locked(func() {
			if e.Doc != nil {
				docPath = e.Doc.Path
			}
		})
		force := jsonv.IsTrue(jsonv.Get(req, "refresh"))
		skills := DiscoverSkillsRefresh(docPath, force)
		out := jsonv.NewArray()
		out.Items = make([]jsonv.Value, 0, len(skills))
		for _, s := range skills {
			out.Items = append(out.Items, engine.Object(
				"name", s.Name,
				"description", s.Description,
				"path", s.Path,
				"dir", s.Dir,
				"source", s.Source,
			))
		}
		return out, nil
	})
}
