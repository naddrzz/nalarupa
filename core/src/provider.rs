//! The model a run talks to, behind one interface. The agent loop and the AI Pen build requests in Claude Messages
//! shape (system, content blocks, tools, tool results with images) and read back `{content, stop_reason, usage}` in
//! the same shape; each wire format implements `Provider` and translates as needed.

use crate::claude::Event;
use crate::engine::Engine;
use serde_json::{json, Value};

pub trait Provider: Send {
    /// Stream one request and return `{content, stop_reason, usage}`, reporting text, thinking and tool calls as they
    /// arrive. `beta` is the Claude API's `anthropic-beta` header; other formats ignore it.
    fn stream(&self, body: Value, beta: Option<&str>, cancel: &dyn Fn() -> bool, on: &mut dyn FnMut(Event)) -> Result<Value, String>;
}

impl Provider for crate::claude::Client {
    fn stream(&self, body: Value, beta: Option<&str>, cancel: &dyn Fn() -> bool, on: &mut dyn FnMut(Event)) -> Result<Value, String> {
        crate::claude::Client::stream(self, body, beta, cancel, on)
    }
}

/// The model behind the API backends: the active bring-your-own-key provider with the `provider` backend, else the
/// Claude API (at the base URL from the settings when one is set).
pub fn current(e: &Engine) -> Result<Box<dyn Provider>, String> {
    if e.backend() == "provider" {
        let list = e.settings.get("providers").cloned().unwrap_or(Value::Null);
        let p = active(&list, e.setting("agent_provider").as_deref()).ok_or("No provider is set up. Add one in Agent settings (gear icon).")?;
        return from_config(p);
    }
    let key = e.api_key().ok_or("No Anthropic API key. Add one in Settings, set ANTHROPIC_API_KEY, or switch the backend to Claude Code.")?;
    Ok(Box::new(crate::claude::Client::at(key, e.setting("anthropic_base_url").as_deref())))
}

/// The model a run uses. Claude backends: the chosen one (`agent_model`). A provider: its own choice
/// (`provider_model`, kept apart so a provider's model ids never reach a Claude backend) when the provider lists it,
/// else its first model. `None` leaves the choice to the backend (Claude Code's own default).
pub fn model(e: &Engine, backend: &str) -> Result<Option<String>, String> {
    let chosen = e.setting("agent_model");
    match backend {
        "provider" => {
            let list = e.settings.get("providers").cloned().unwrap_or(Value::Null);
            let p = active(&list, e.setting("agent_provider").as_deref()).ok_or("No provider is set up. Add one in Agent settings (gear icon).")?;
            pick_model(p, e.setting("provider_model").as_deref()).map(Some).ok_or_else(|| format!("Provider \"{}\" lists no models. Add a model id in Agent settings.", name(p)))
        }
        "claude-code" => Ok(chosen),
        _ => Ok(Some(chosen.unwrap_or_else(|| crate::agent::DEFAULT_MODEL.into()))),
    }
}

/// How a saved key reads back: never the key itself.
const MASK: &str = "••••••••";
/// The wire formats a provider can speak.
const FORMATS: [&str; 2] = ["anthropic", "chat"];

fn name(p: &Value) -> &str {
    p["name"].as_str().filter(|n| !n.is_empty()).unwrap_or("Provider")
}

/// A saved provider's client: `{format, base_url, api_key, vision}`.
pub fn from_config(p: &Value) -> Result<Box<dyn Provider>, String> {
    let base = p["base_url"].as_str().map(str::trim).filter(|b| !b.is_empty()).ok_or_else(|| format!("Provider \"{}\" needs a base URL. Set it in Agent settings.", name(p)))?;
    let key = p["api_key"].as_str().map(str::to_string).filter(|k| !k.trim().is_empty());
    match p["format"].as_str().unwrap_or("") {
        "anthropic" => Ok(Box::new(crate::claude::Client::at(key.unwrap_or_default(), Some(base)))),
        "chat" => Ok(Box::new(crate::openai_chat::Client::new(base, key, p["vision"] != false))),
        f => Err(format!("Provider \"{}\" uses the \"{f}\" format, which this version of Nalarupa cannot speak yet.", name(p))),
    }
}

/// The provider's model for `chosen`: it when listed, else the first listed model.
pub fn pick_model(p: &Value, chosen: Option<&str>) -> Option<String> {
    let models: Vec<&str> = p["models"].as_array().into_iter().flatten().filter_map(Value::as_str).collect();
    chosen.filter(|c| models.contains(c)).or_else(|| models.first().copied()).map(str::to_string)
}

/// The provider with `id` in the saved list, else the first one (a removed choice falls back).
pub fn active<'a>(list: &'a Value, id: Option<&str>) -> Option<&'a Value> {
    let all = list.as_array()?;
    id.and_then(|id| all.iter().find(|p| p["id"] == id)).or_else(|| all.first())
}

/// Add or update a provider in `list`; returns its id. A missing or masked key keeps the saved one.
pub fn save(list: &mut Value, mut p: Value) -> Result<String, String> {
    if !list.is_array() {
        *list = Value::Array(Vec::new());
    }
    let base = p["base_url"].as_str().map(str::trim).unwrap_or("");
    if base.is_empty() {
        return Err("A provider needs a base URL (its API root, e.g. https://openrouter.ai/api/v1).".into());
    }
    let format = p["format"].as_str().unwrap_or("");
    if !FORMATS.contains(&format) {
        return Err(format!("Unknown format \"{format}\": choose anthropic or chat."));
    }
    let all = list.as_array_mut().expect("an array");
    let id = p["id"].as_str().filter(|i| !i.is_empty()).map(str::to_string).unwrap_or_else(|| format!("p{:08x}", crate::doc::random_u64() & 0xffff_ffff));
    let old = all.iter().position(|q| q["id"] == id.as_str());
    let key = match p["api_key"].as_str().map(str::trim) {
        Some(k) if !k.is_empty() && k != MASK => json!(k),
        // Blank, masked or absent: keep what was saved (a local server can have no key at all).
        _ => old.map(|i| all[i]["api_key"].clone()).unwrap_or(Value::Null),
    };
    let models: Vec<String> = p["models"].as_array().into_iter().flatten().filter_map(Value::as_str).map(str::trim).filter(|m| !m.is_empty()).map(str::to_string).collect();
    p = json!({
        "id": id,
        "name": p["name"].as_str().map(str::trim).filter(|n| !n.is_empty()).unwrap_or(base),
        "format": format,
        "base_url": base,
        "api_key": key,
        "models": models,
        "vision": p["vision"] != false,
    });
    match old {
        Some(i) => all[i] = p,
        None => all.push(p),
    }
    Ok(id)
}

/// The list as the UI may read it: keys replaced by a mask.
pub fn masked(list: &Value) -> Value {
    let mut out = list.clone();
    for p in out.as_array_mut().into_iter().flatten() {
        if p["api_key"].as_str().is_some_and(|k| !k.is_empty()) {
            p["api_key"] = json!(MASK);
        }
    }
    out
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::net::TcpListener;
    use std::sync::{Arc, Mutex};
    use std::time::Duration;

    /// One request as a fake server received it.
    #[derive(Clone, Debug)]
    pub struct Received {
        pub path: String,
        pub headers: Vec<(String, String)>,
        pub body: Value,
    }

    /// A server that answers request n with `replies[n]` (an SSE body) and keeps every request it received.
    pub fn fake_server(replies: Vec<String>) -> (String, Arc<Mutex<Vec<Received>>>) {
        let l = TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", l.local_addr().unwrap());
        let seen = Arc::new(Mutex::new(Vec::new()));
        let log = seen.clone();
        std::thread::spawn(move || {
            for reply in replies {
                let Ok((mut s, _)) = l.accept() else { return };
                s.set_read_timeout(Some(Duration::from_millis(300))).unwrap();
                let mut raw = Vec::new();
                let mut buf = [0u8; 65536];
                while let Ok(n) = s.read(&mut buf) {
                    if n == 0 {
                        break;
                    }
                    raw.extend_from_slice(&buf[..n]);
                    let text = String::from_utf8_lossy(&raw);
                    if let Some((head, body)) = text.split_once("\r\n\r\n") {
                        let len = head.lines().find_map(|l| l.to_ascii_lowercase().strip_prefix("content-length:").map(|v| v.trim().parse::<usize>().unwrap_or(0))).unwrap_or(0);
                        if body.len() >= len {
                            break;
                        }
                    }
                }
                let text = String::from_utf8_lossy(&raw).to_string();
                let (head, body) = text.split_once("\r\n\r\n").unwrap_or((&text, ""));
                let mut lines = head.lines();
                let path = lines.next().unwrap_or("").split(' ').nth(1).unwrap_or("").to_string();
                let headers = lines.filter_map(|l| l.split_once(':')).map(|(k, v)| (k.trim().to_ascii_lowercase(), v.trim().to_string())).collect();
                log.lock().unwrap().push(Received { path, headers, body: serde_json::from_str(body).unwrap_or(Value::Null) });
                let _ = write!(s, "HTTP/1.1 200 OK\r\ncontent-type: text/event-stream\r\nconnection: close\r\n\r\n{reply}");
            }
        });
        (base, seen)
    }

    /// An SSE body from `data:` payloads.
    pub fn sse(events: &[Value]) -> String {
        events.iter().map(|e| format!("data: {e}\n\n")).collect()
    }

    fn header<'a>(r: &'a Received, name: &str) -> Option<&'a str> {
        r.headers.iter().find(|(k, _)| k == name).map(|(_, v)| v.as_str())
    }

    fn providers() -> Value {
        json!([
            {"id": "or", "name": "OpenRouter", "format": "chat", "base_url": "https://openrouter.ai/api/v1", "api_key": "sk-or", "models": ["qwen/qwen3-coder", "x/y"], "vision": false},
            {"id": "gw", "name": "Gateway", "format": "anthropic", "base_url": "https://gw.example.com", "api_key": "sk-gw", "models": ["claude-opus-5-5"], "vision": true},
            {"id": "rs", "name": "Responses", "format": "responses", "base_url": "https://api.openai.com/v1", "api_key": "sk", "models": ["gpt-x"], "vision": true},
        ])
    }

    #[test]
    fn the_active_provider_picks_the_client_and_the_model() {
        let ps = providers();
        assert!(from_config(&ps[0]).is_ok() && from_config(&ps[1]).is_ok());
        assert!(from_config(&json!({"format": "chat"})).err().unwrap().contains("base URL"), "a provider needs a base URL");
        assert!(from_config(&json!({"format": "gemini", "base_url": "x"})).err().unwrap().contains("gemini"));
        // The chosen model when the provider offers it, else its first model.
        assert_eq!(pick_model(&ps[0], Some("x/y")).as_deref(), Some("x/y"));
        assert_eq!(pick_model(&ps[0], Some("claude-opus-5-5")).as_deref(), Some("qwen/qwen3-coder"));
        assert_eq!(pick_model(&ps[0], None).as_deref(), Some("qwen/qwen3-coder"));
        assert_eq!(active(&ps, Some("gw")).unwrap()["name"], "Gateway");
        assert_eq!(active(&ps, Some("gone")).unwrap()["id"], "or", "a removed choice falls back to the first provider");
        assert!(active(&json!([]), None).is_none());
    }

    #[test]
    fn saving_a_provider_keeps_its_key_unless_a_new_one_is_given() {
        let mut list = providers();
        let id = save(&mut list, json!({"id": "or", "name": "OpenRouter 2", "format": "chat", "base_url": "https://openrouter.ai/api/v1", "api_key": "••••••••", "models": ["a"], "vision": true})).unwrap();
        assert_eq!(id, "or");
        assert_eq!(list[0]["api_key"], "sk-or", "a masked key keeps the saved one");
        assert_eq!(list[0]["name"], "OpenRouter 2");
        save(&mut list, json!({"id": "or", "format": "chat", "base_url": "u", "api_key": "sk-new", "models": []})).unwrap();
        assert_eq!(list[0]["api_key"], "sk-new");
        let fresh = save(&mut list, json!({"name": "Local", "format": "chat", "base_url": "http://localhost:11434/v1", "models": ["llama"]})).unwrap();
        assert!(!fresh.is_empty() && list.as_array().unwrap().iter().any(|p| p["id"] == fresh.as_str()));
        assert!(save(&mut list, json!({"name": "No URL", "format": "chat"})).unwrap_err().contains("base URL"));
        assert_eq!(masked(&list)[0]["api_key"], "••••••••");
        assert!(masked(&list).as_array().unwrap().iter().all(|p| p["api_key"] != "sk-new"));
    }

    #[test]
    fn a_base_url_routes_the_claude_api_and_keeps_its_headers() {
        let reply = sse(&[json!({"type": "message_stop"})]);
        for base in ["/gateway", "/gateway/", "/gateway/v1", "/gateway?team=a#notes"] {
            let (root, seen) = fake_server(vec![reply.clone()]);
            let client = crate::claude::Client::at("sk-test".into(), Some(&format!("{root}{base}")));
            Provider::stream(&client, json!({"model": "m", "messages": []}), Some("b1"), &|| false, &mut |_| {}).unwrap();
            let r = seen.lock().unwrap()[0].clone();
            let want = if base.contains('?') { "/gateway/v1/messages?team=a" } else { "/gateway/v1/messages" };
            assert_eq!(r.path, want, "{base}");
            assert_eq!(header(&r, "x-api-key"), Some("sk-test"));
            assert_eq!(header(&r, "anthropic-version"), Some("2023-06-01"));
            assert_eq!(header(&r, "anthropic-beta"), Some("b1"));
            assert_eq!(r.body["stream"], json!(true));
        }
    }

    #[test]
    fn a_whole_turn_passes_through_the_provider_interface() {
        let turn1 = sse(&[
            json!({"type": "message_start", "message": {"usage": {"input_tokens": 12}}}),
            json!({"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}),
            json!({"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Building the hero."}}),
            json!({"type": "content_block_stop", "index": 0}),
            json!({"type": "content_block_start", "index": 1, "content_block": {"type": "tool_use", "id": "toolu_1", "name": "execute", "input": {}}}),
            json!({"type": "content_block_delta", "index": 1, "delta": {"type": "input_json_delta", "partial_json": "{\"input\":\"Insert("}}),
            json!({"type": "content_block_delta", "index": 1, "delta": {"type": "input_json_delta", "partial_json": "document)\"}"}}),
            json!({"type": "content_block_stop", "index": 1}),
            json!({"type": "message_delta", "delta": {"stop_reason": "tool_use"}, "usage": {"output_tokens": 30}}),
            json!({"type": "message_stop"}),
        ]);
        let turn2 = sse(&[
            json!({"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": "Done."}}),
            json!({"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 2}}),
        ]);
        let (root, seen) = fake_server(vec![turn1, turn2]);
        let p: Box<dyn Provider> = Box::new(crate::claude::Client::at("k".into(), Some(&root)));
        let mut events = Vec::new();
        let mut on = |ev: Event| events.push(match ev {
            Event::Text(t) => format!("text:{t}"),
            Event::Thinking(t) => format!("thinking:{t}"),
            Event::ToolStart { name, .. } => format!("start:{name}"),
            Event::ToolInput { json, .. } => format!("input:{json}"),
        });
        let first = p.stream(json!({"model": "m", "messages": [{"role": "user", "content": "Design a page"}]}), None, &|| false, &mut on).unwrap();
        assert_eq!(first["stop_reason"], "tool_use");
        assert_eq!(first["content"][1]["input"], json!({"input": "Insert(document)"}));
        assert_eq!(first["usage"], json!({"input_tokens": 12, "output_tokens": 30}));
        assert_eq!(events, ["text:Building the hero.", "start:execute", "input:{\"input\":\"Insert(", "input:{\"input\":\"Insert(document)\"}"]);
        let screenshot = json!({"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}});
        let messages = json!([
            {"role": "user", "content": "Design a page"},
            {"role": "assistant", "content": first["content"]},
            {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "text", "text": "OK"}, screenshot], "is_error": false}]},
        ]);
        let second = p.stream(json!({"model": "m", "messages": messages}), None, &|| false, &mut |_| {}).unwrap();
        assert_eq!(second["stop_reason"], "end_turn");
        let sent = seen.lock().unwrap()[1].body.clone();
        assert_eq!(sent["messages"], messages, "the tool result and its screenshot reach the model as built");
    }
}
