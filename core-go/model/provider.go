package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// DefaultModel is the Claude model a run uses when none is chosen (agent.rs `DEFAULT_MODEL`).
const DefaultModel = "claude-opus-5-5"

// Provider is the model a run talks to (provider.rs `Provider`). The agent loop and the AI Pen build requests in
// Claude Messages shape (system, content blocks, tools, tool results with images) and read back
// `{content, stop_reason, usage}` in the same shape; each wire format implements Provider and translates as
// needed.
type Provider interface {
	// Stream sends one request and returns `{content, stop_reason, usage}`, reporting text, thinking and tool
	// calls to on as they arrive. beta is the Claude API's `anthropic-beta` header ("" for none); other formats
	// ignore it. cancel is polled at least every 200 ms; once it reports true, Stream returns "stopped".
	Stream(body jsonv.Value, beta string, cancel func() bool, on func(Event)) (jsonv.Value, error)
}

// Endpoint is `<base><path>`, keeping a query the base carries (`…/v1?tenant=a` → `…/v1/chat/completions?tenant=a`)
// and dropping a fragment.
func Endpoint(base, path string) string {
	b, _, _ := strings.Cut(strings.TrimSpace(base), "#")
	root, q, hasQ := strings.Cut(b, "?")
	if !hasQ {
		return strings.TrimRight(b, "/") + path
	}
	if q != "" {
		return strings.TrimRight(root, "/") + path + "?" + q
	}
	return strings.TrimRight(root, "/") + path
}

// providerName is a provider's name for messages.
func providerName(p jsonv.Value) string {
	if n := jsonv.StrOr(jsonv.Get(p, "name")); n != "" {
		return n
	}
	return "Provider"
}

// FromConfig is a saved provider's client: `{format, base_url, api_key, vision}`.
func FromConfig(p jsonv.Value) (Provider, error) {
	base := strings.TrimSpace(jsonv.StrOr(jsonv.Get(p, "base_url")))
	if base == "" {
		return nil, fmt.Errorf("Provider \"%s\" needs a base URL. Set it in Agent settings.", providerName(p))
	}
	key := jsonv.StrOr(jsonv.Get(p, "api_key"))
	if strings.TrimSpace(key) == "" {
		key = ""
	}
	vision := !jsonv.IsFalse(jsonv.Get(p, "vision"))
	switch f := jsonv.StrOr(jsonv.Get(p, "format")); f {
	case "anthropic":
		return ClaudeAt(key, base), nil
	case "chat":
		return NewChat(base, key, vision), nil
	case "responses":
		return NewResponses(base, key, vision), nil
	default:
		return nil, fmt.Errorf("Provider \"%s\" uses the \"%s\" format, which this version of Nalarupa cannot speak yet.", providerName(p), f)
	}
}

// PickModel is the provider's model for chosen ("" for none): chosen when listed, else the first listed model.
func PickModel(p jsonv.Value, chosen string) (string, bool) {
	var models []string
	for _, m := range jsonv.Items(jsonv.Get(p, "models")) {
		if s, ok := jsonv.Str(m); ok {
			models = append(models, s)
		}
	}
	if chosen != "" && slices.Contains(models, chosen) {
		return chosen, true
	}
	if len(models) > 0 {
		return models[0], true
	}
	return "", false
}

// Active is the provider with id ("" for none) in the saved list, else the first one (a removed choice falls
// back); nil when the list is empty or not a list.
func Active(list jsonv.Value, id string) jsonv.Value {
	all := jsonv.Arr(list)
	if all == nil {
		return nil
	}
	if id != "" {
		if i := slices.IndexFunc(all.Items, func(p jsonv.Value) bool { return jsonv.StrIs(jsonv.Get(p, "id"), id) }); i >= 0 {
			return all.Items[i]
		}
	}
	if len(all.Items) > 0 {
		return all.Items[0]
	}
	return nil
}

// ProviderModel is the model of the active provider for chosen (provider.rs `model` with the `provider` backend):
// errors name what to fix in Agent settings.
func ProviderModel(list jsonv.Value, id, chosen string) (string, error) {
	p := Active(list, id)
	if p == nil {
		return "", errors.New(NoProvider)
	}
	m, ok := PickModel(p, chosen)
	if !ok {
		return "", fmt.Errorf("Provider \"%s\" lists no models. Add a model id in Agent settings.", providerName(p))
	}
	return m, nil
}

// NoProvider is the failure with the `provider` backend and no provider saved.
const NoProvider = "No provider is set up. Add one in Agent settings (gear icon)."

// Test sends client one tiny request for model and returns the `provider_test` event for provider id (the
// background part of lib.rs `provider_test`): {type, id, ok, model, reply} or {type, id, ok: false, model, error}.
func Test(client Provider, id, model string) jsonv.Value {
	body := obj("model", model, "max_tokens", jsonv.Number("16"),
		"messages", arr([]jsonv.Value{obj("role", "user", "content", "Reply with the single word OK.")}))
	out, err := client.Stream(body, "", func() bool { return false }, func(Event) {})
	if err != nil {
		return obj("type", "provider_test", "id", id, "ok", false, "model", model, "error", err.Error())
	}
	var text strings.Builder
	for _, b := range jsonv.Items(jsonv.Get(out, "content")) {
		if t, ok := jsonv.Str(jsonv.Get(b, "text")); ok {
			text.WriteString(t)
		}
	}
	reply := strings.TrimSpace(text.String())
	if reply == "" {
		return obj("type", "provider_test", "id", id, "ok", false, "model", model, "error", "The provider answered with no text. Check the model id.")
	}
	return obj("type", "provider_test", "id", id, "ok", true, "model", model, "reply", reply)
}
