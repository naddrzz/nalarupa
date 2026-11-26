package engine

import (
	"errors"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/model"
)

// The provider resolution that reads the settings (provider.rs `current`, `model`, and the locked part of lib.rs
// `provider_test`); the clients and the rest are in package model.

// CurrentProvider is the model behind the API backends (provider.rs `current`): the active bring-your-own-key
// provider with the `provider` backend, else the Claude API (at the base URL from the settings when one is set).
func (e *Engine) CurrentProvider() (model.Provider, error) {
	if e.Backend() == "provider" {
		p := model.Active(jsonv.Get(e.Settings, "providers"), e.settingOr("agent_provider"))
		if p == nil {
			return nil, errors.New(model.NoProvider)
		}
		return model.FromConfig(p)
	}
	key, ok := e.APIKey()
	if !ok {
		return nil, errors.New("No Anthropic API key. Add one in Settings, set ANTHROPIC_API_KEY, or switch the backend to Claude Code.")
	}
	return model.ClaudeAt(key, e.settingOr("anthropic_base_url")), nil
}

// RunModel is the model a run on backend uses (provider.rs `model`). Claude backends: the chosen one
// (`agent_model`). A provider: its own choice (`provider_model`, kept apart so a provider's model ids never reach a
// Claude backend) when the provider lists it, else its first model. ok false leaves the choice to the backend
// (Claude Code's own default).
func (e *Engine) RunModel(backend string) (name string, ok bool, err error) {
	chosen, has := e.Setting("agent_model")
	switch backend {
	case "provider":
		m, err := model.ProviderModel(jsonv.Get(e.Settings, "providers"), e.settingOr("agent_provider"), e.settingOr("provider_model"))
		return m, err == nil, err
	case "claude-code":
		return chosen, has, nil
	}
	if !has {
		chosen = model.DefaultModel
	}
	return chosen, true, nil
}

// ProviderTest is the part of lib.rs `provider_test` that runs under the engine lock: the client and model of the
// saved provider id. The caller then runs model.Test off the lock and emits its event.
func (e *Engine) ProviderTest(id string) (model.Provider, string, error) {
	var p jsonv.Value
	for _, q := range jsonv.Items(jsonv.Get(e.Settings, "providers")) {
		if jsonv.StrIs(jsonv.Get(q, "id"), id) {
			p = q
			break
		}
	}
	if p == nil {
		return nil, "", errors.New("unknown provider")
	}
	client, err := model.FromConfig(p)
	if err != nil {
		return nil, "", err
	}
	m, ok := model.PickModel(p, "")
	if !ok {
		return nil, "", errors.New("This provider lists no models: add a model id first.")
	}
	return client, m, nil
}

// settingOr is a non-empty string setting, or "".
func (e *Engine) settingOr(key string) string {
	s, _ := e.Setting(key)
	return s
}

// DefaultModelContextWindow returns a sensible default context window (in tokens) for modelID when not explicitly configured.
func DefaultModelContextWindow(backend, modelID string) int {
	m := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case strings.Contains(m, "1m") || strings.Contains(m, "gemini-"):
		return 1_000_000
	case strings.Contains(m, "gpt-5") || strings.Contains(m, "gpt-6"):
		return 400_000
	case strings.Contains(m, "qwen3") || strings.Contains(m, "qwen4") || strings.Contains(m, "kimi") || strings.Contains(m, "codestral"):
		return 262_144
	case strings.Contains(m, "claude") || strings.Contains(m, "fable") || strings.Contains(m, "opus") ||
		strings.Contains(m, "sonnet") || strings.Contains(m, "haiku") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4"):
		return 200_000
	case backend == "api" || backend == "claude-code":
		return 200_000
	default:
		return 128_000
	}
}

// ContextWindowLimit resolves the active context window limit (in tokens) for backend and modelID.
// Custom providers use their configured `context_window` when set (capped by `agent_context_window` if also set);
// otherwise `agent_context_window` or DefaultModelContextWindow applies.
func (e *Engine) ContextWindowLimit(backend, modelID string) int {
	globalCap := ParseContextWindow(jsonv.Get(e.Settings, "agent_context_window"))
	if backend == "provider" {
		p := model.Active(jsonv.Get(e.Settings, "providers"), e.settingOr("agent_provider"))
		if provCap := ParseContextWindow(jsonv.Get(p, "context_window")); provCap > 0 {
			if globalCap > 0 && globalCap < provCap {
				return globalCap
			}
			return provCap
		}
	}
	if globalCap > 0 {
		return globalCap
	}
	return DefaultModelContextWindow(backend, modelID)
}

// EffectiveContextWindow resolves the active context window limit for the currently selected backend and model.
func (e *Engine) EffectiveContextWindow() int {
	b := e.Backend()
	m, _, _ := e.RunModel(b)
	return e.ContextWindowLimit(b, m)
}
