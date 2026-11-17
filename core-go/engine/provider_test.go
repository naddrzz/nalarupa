package engine

import (
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/model"
)

func testProviders() jsonv.Value {
	return jsonv.MustParse(`[
		{"id": "or", "name": "OpenRouter", "format": "chat", "base_url": "https://openrouter.ai/api/v1", "api_key": "sk-or", "models": ["qwen/qwen3-coder", "x/y"], "vision": false},
		{"id": "gw", "name": "Gateway", "format": "anthropic", "base_url": "https://gw.example.com", "api_key": "sk-gw", "models": ["claude-opus-5-5"], "vision": true},
		{"id": "rs", "name": "Responses", "format": "responses", "base_url": "https://api.openai.com/v1", "api_key": "sk", "models": ["gpt-x"], "vision": true}
	]`)
}

// provider.rs saving_a_provider_keeps_its_key_unless_a_new_one_is_given.
func TestSavingAProviderKeepsItsKeyUnlessANewOneIsGiven(t *testing.T) {
	e := newEngine(t)
	e.Settings.Set("providers", testProviders())
	list := func() []jsonv.Value { return jsonv.Items(jsonv.Get(e.Settings, "providers")) }
	id, err := e.SaveProvider(jsonv.MustParse(`{"id": "or", "name": "OpenRouter 2", "format": "chat", "base_url": "https://openrouter.ai/api/v1", "api_key": "••••••••", "models": ["a"], "vision": true}`))
	if err != nil || id != "or" {
		t.Fatal(id, err)
	}
	if k := jsonv.Get(list()[0], "api_key"); !jsonv.StrIs(k, "sk-or") {
		t.Fatal("a masked key keeps the saved one", k)
	}
	if n := jsonv.Get(list()[0], "name"); !jsonv.StrIs(n, "OpenRouter 2") {
		t.Fatal(n)
	}
	if _, err := e.SaveProvider(jsonv.MustParse(`{"id": "or", "format": "chat", "base_url": "u", "api_key": "sk-new", "models": []}`)); err != nil {
		t.Fatal(err)
	}
	if k := jsonv.Get(list()[0], "api_key"); !jsonv.StrIs(k, "sk-new") {
		t.Fatal(k)
	}
	fresh, err := e.SaveProvider(jsonv.MustParse(`{"name": "Local", "format": "chat", "base_url": "http://localhost:11434/v1", "models": ["llama"]}`))
	if err != nil || fresh == "" || !hasProvider(list(), fresh) {
		t.Fatal(fresh, err)
	}
	if _, err := e.SaveProvider(jsonv.MustParse(`{"name": "No URL", "format": "chat"}`)); err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Fatal(err)
	}
	masked := jsonv.Items(MaskedProviders(jsonv.Get(e.Settings, "providers")))
	if k := jsonv.Get(masked[0], "api_key"); !jsonv.StrIs(k, Mask) {
		t.Fatal(k)
	}
	for _, p := range masked {
		if jsonv.StrIs(jsonv.Get(p, "api_key"), "sk-new") {
			t.Fatal("a key reads back masked")
		}
	}
}

func hasProvider(list []jsonv.Value, id string) bool {
	for _, p := range list {
		if jsonv.StrIs(jsonv.Get(p, "id"), id) {
			return true
		}
	}
	return false
}

// The settings pick the client and the model as provider.rs `current` and `model` do.
func TestTheSettingsPickTheProviderAndTheModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	e := newEngine(t)
	if _, err := e.CurrentProvider(); err == nil || !strings.HasPrefix(err.Error(), "No Anthropic API key.") {
		t.Fatal(err)
	}
	if m, ok, err := e.RunModel("claude-code"); ok || err != nil || m != "" {
		t.Fatal("Claude Code keeps its own default", m, ok, err)
	}
	if m, ok, _ := e.RunModel("api"); !ok || m != model.DefaultModel {
		t.Fatal(m)
	}
	e.Settings.Set("anthropic_api_key", "sk-a")
	e.Settings.Set("agent_model", "claude-x")
	if p, err := e.CurrentProvider(); err != nil {
		t.Fatal(err)
	} else if _, ok := p.(*model.Claude); !ok {
		t.Fatalf("%T", p)
	}
	if m, _, _ := e.RunModel("api"); m != "claude-x" {
		t.Fatal(m)
	}
	e.Settings.Set("agent_backend", "provider")
	if _, err := e.CurrentProvider(); err == nil || err.Error() != model.NoProvider {
		t.Fatal(err)
	}
	if _, _, err := e.RunModel("provider"); err == nil || err.Error() != model.NoProvider {
		t.Fatal(err)
	}
	e.Settings.Set("providers", testProviders())
	e.Settings.Set("agent_provider", "rs")
	if p, err := e.CurrentProvider(); err != nil {
		t.Fatal(err)
	} else if _, ok := p.(*model.Responses); !ok {
		t.Fatalf("%T", p)
	}
	// A provider's model never comes from agent_model.
	if m, ok, err := e.RunModel("provider"); !ok || err != nil || m != "gpt-x" {
		t.Fatal(m, err)
	}
	e.Settings.Set("agent_provider", "or")
	e.Settings.Set("provider_model", "x/y")
	if m, _, _ := e.RunModel("provider"); m != "x/y" {
		t.Fatal(m)
	}
	jsonv.Obj(jsonv.Items(jsonv.Get(e.Settings, "providers"))[0]).Set("models", jsonv.NewArray())
	if _, _, err := e.RunModel("provider"); err == nil || err.Error() != `Provider "OpenRouter" lists no models. Add a model id in Agent settings.` {
		t.Fatal(err)
	}
}

// The locked part of lib.rs `provider_test`.
func TestProviderTestFindsTheClientAndItsFirstModel(t *testing.T) {
	e := newEngine(t)
	if _, _, err := e.ProviderTest("or"); err == nil || err.Error() != "unknown provider" {
		t.Fatal(err)
	}
	e.Settings.Set("providers", testProviders())
	p, m, err := e.ProviderTest("or")
	if err != nil || m != "qwen/qwen3-coder" {
		t.Fatal(m, err)
	}
	if _, ok := p.(*model.Chat); !ok {
		t.Fatalf("%T", p)
	}
	jsonv.Obj(jsonv.Items(jsonv.Get(e.Settings, "providers"))[1]).Set("models", jsonv.NewArray())
	if _, _, err := e.ProviderTest("gw"); err == nil || err.Error() != "This provider lists no models: add a model id first." {
		t.Fatal(err)
	}
}
