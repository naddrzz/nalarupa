package engine

import (
	"errors"

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
