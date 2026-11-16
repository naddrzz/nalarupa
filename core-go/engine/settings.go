package engine

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// Mask stands for a stored API key wherever the UI reads settings back.
const Mask = "••••••••"

// SetSetting sets one setting (nil removes it) and saves. If the save fails the setting keeps its old value,
// so an error means nothing changed, in memory or on disk.
func (e *Engine) SetSetting(key string, value jsonv.Value) error {
	old, had := e.Settings.Get(key)
	if value == nil {
		e.Settings.Remove(key)
	} else {
		e.Settings.Set(key, value)
	}
	err := e.SaveSettings()
	if err != nil {
		if had {
			e.Settings.Set(key, old)
		} else {
			e.Settings.Remove(key)
		}
	}
	return err
}

// SaveSettings writes settings.json atomically (a temp file renamed over it): a crash or a reader mid-save never
// sees a truncated file, which would drop every setting, the API key included, at the next start.
func (e *Engine) SaveSettings() error {
	text := jsonv.MarshalPretty(e.Settings)
	path := doc.JoinPath(e.DataDir, "settings.json")
	tmp := path + ".tmp"
	err := os.WriteFile(tmp, text, 0o666)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		doc.RemoveFile(tmp) // a failed save leaves no copy of the new settings (an API key) behind
		return fmt.Errorf("cannot save %s: %s", path, doc.IOError(err))
	}
	return nil
}

// Setting is a non-empty string setting.
func (e *Engine) Setting(key string) (string, bool) {
	s, ok := jsonv.Str(jsonv.Get(e.Settings, key))
	return s, ok && s != ""
}

// nonblank is a key as typed or set: surrounding whitespace is dropped and a blank key is none.
func nonblank(k string, ok bool) (string, bool) {
	k = strings.TrimSpace(k)
	return k, ok && k != ""
}

// APIKey is the Anthropic API key: the setting, else ANTHROPIC_API_KEY. A blank one of either is no key
// (`ANTHROPIC_API_KEY=` is common in CI and devcontainers).
func (e *Engine) APIKey() (string, bool) {
	if k, ok := nonblank(e.Setting("anthropic_api_key")); ok {
		return k, true
	}
	return nonblank(os.LookupEnv("ANTHROPIC_API_KEY"))
}

// Backend is the agent backend: the setting, else the API when a key is available, else the Claude Code CLI.
func (e *Engine) Backend() string {
	if b, ok := e.Setting("agent_backend"); ok {
		return b
	}
	if _, ok := e.APIKey(); ok {
		return "api"
	}
	return "claude-code"
}

// MaskedProviders is the providers list as the UI may read it: keys replaced by Mask.
func MaskedProviders(list jsonv.Value) jsonv.Value {
	out := jsonv.Clone(list)
	for _, p := range jsonv.Items(out) {
		if k, ok := jsonv.Str(jsonv.Get(p, "api_key")); ok && k != "" {
			jsonv.Obj(p).Set("api_key", Mask)
		}
	}
	return out
}

// SettingsView is the settings as the UI reads them (`settings_get`): keys masked, plus the backend the agent
// will use, so the UI never re-derives it from settings alone.
func (e *Engine) SettingsView() *jsonv.Object {
	s := jsonv.Clone(e.Settings).(*jsonv.Object)
	if s.Has("anthropic_api_key") {
		s.Set("anthropic_api_key", Mask)
	}
	if list, ok := s.Get("providers"); ok {
		s.Set("providers", MaskedProviders(list))
	}
	s.Set("backend", e.Backend())
	return s
}

// providerFormats are the API formats a provider can speak (provider.rs `FORMATS`).
var providerFormats = []string{"anthropic", "chat", "responses"}

// SaveProvider adds or replaces provider p in the `providers` setting and saves it (lib.rs `provider_save`,
// provider.rs `save`): it returns the provider's id, new ones drawn as `p` and 8 hex digits. A blank, masked or
// absent key keeps the saved one, so the UI can save back what it read.
func (e *Engine) SaveProvider(p jsonv.Value) (string, error) {
	list := jsonv.Arr(jsonv.Clone(jsonv.Get(e.Settings, "providers")))
	if list == nil {
		list = jsonv.NewArray()
	}
	base, _ := jsonv.Str(jsonv.Get(p, "base_url"))
	base = strings.TrimSpace(base)
	if base == "" {
		return "", errors.New("A provider needs a base URL (its API root, e.g. https://openrouter.ai/api/v1).")
	}
	format, _ := jsonv.Str(jsonv.Get(p, "format"))
	if !slices.Contains(providerFormats, format) {
		return "", fmt.Errorf("Unknown format \"%s\": choose anthropic, chat or responses.", format)
	}
	id, ok := jsonv.Str(jsonv.Get(p, "id"))
	if !ok || id == "" {
		id = fmt.Sprintf("p%08x", doc.RandomU64()&0xffff_ffff)
	}
	old := slices.IndexFunc(list.Items, func(q jsonv.Value) bool { return jsonv.StrIs(jsonv.Get(q, "id"), id) })
	var key jsonv.Value
	if k, ok := jsonv.Str(jsonv.Get(p, "api_key")); ok && strings.TrimSpace(k) != "" && strings.TrimSpace(k) != Mask {
		key = strings.TrimSpace(k)
	} else if old >= 0 {
		// Blank, masked or absent: keep what was saved (a local server can have no key at all).
		key = jsonv.Clone(jsonv.Get(list.Items[old], "api_key"))
	}
	models := []string{}
	for _, m := range jsonv.Items(jsonv.Get(p, "models")) {
		if s, ok := jsonv.Str(m); ok && strings.TrimSpace(s) != "" {
			models = append(models, strings.TrimSpace(s))
		}
	}
	name, _ := jsonv.Str(jsonv.Get(p, "name"))
	if name = strings.TrimSpace(name); name == "" {
		name = base
	}
	saved := Object("id", id, "name", name, "format", format, "base_url", base, "api_key", key, "models", Strings(models),
		"vision", !jsonv.IsFalse(jsonv.Get(p, "vision")))
	if old >= 0 {
		list.Items[old] = saved
	} else {
		list.Items = append(list.Items, saved)
	}
	return id, e.SetSetting("providers", list)
}

// RemoveProvider drops the provider with this id from the `providers` setting and saves it.
func (e *Engine) RemoveProvider(id string) error {
	// A missing list is an empty one; anything else saved (null included) goes back as it was, and a null is
	// removed by SetSetting, as Rust's `set_setting` does.
	list, had := e.Settings.Get("providers")
	if !had {
		list = jsonv.NewArray()
	}
	list = jsonv.Clone(list)
	if a := jsonv.Arr(list); a != nil {
		a.Items = slices.DeleteFunc(a.Items, func(q jsonv.Value) bool { return jsonv.StrIs(jsonv.Get(q, "id"), id) })
	}
	return e.SetSetting("providers", list)
}
