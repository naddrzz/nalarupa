//! The OpenAI Chat Completions wire format (`POST <base>/chat/completions`), which most "OpenAI-compatible" servers
//! speak (OpenRouter, Groq, vLLM, LM Studio, Ollama). Requests come in Claude Messages shape and are translated;
//! the stream is translated back into the same events and content blocks the Claude client returns.

use crate::claude::Event;
use serde_json::{json, Map, Value};
use std::time::Duration;

/// A silent stream fails after this long; servers send keep-alive comments well within it.
const STALL: Duration = Duration::from_secs(120);

pub struct Client {
    http: ureq::Agent,
    url: String,
    key: Option<String>,
    /// Whether the model can see images: screenshots are sent, or replaced by a note.
    vision: bool,
    /// OpenAI's own API, which renames a few fields for its reasoning models.
    openai: bool,
    stall: Duration,
}

impl Client {
    /// `base` is the server's API root, as the provider documents it (e.g. `https://api.openai.com/v1`,
    /// `http://localhost:11434/v1`); a blank key sends no Authorization header (local servers).
    pub fn new(base: &str, key: Option<String>, vision: bool) -> Client {
        let http = ureq::Agent::config_builder()
            .timeout_global(Some(Duration::from_secs(900)))
            .http_status_as_error(false)
            .build()
            .into();
        let base = base.trim().trim_end_matches('/');
        Client {
            http,
            url: format!("{base}/chat/completions"),
            key: key.filter(|k| !k.trim().is_empty()),
            vision,
            openai: base.contains("://api.openai.com"),
            stall: STALL,
        }
    }

    pub fn stream(&self, body: Value, _beta: Option<&str>, cancel: &dyn Fn() -> bool, on: &mut dyn FnMut(Event)) -> Result<Value, String> {
        let mut req = self.http.post(&self.url).header("content-type", "application/json");
        if let Some(k) = &self.key {
            req = req.header("authorization", &format!("Bearer {k}"));
        }
        let mut body = translate(&body, self.vision, self.openai);
        body["stream"] = json!(true);
        body["stream_options"] = json!({"include_usage": true});
        let mut text = String::new();
        let mut thinking = String::new();
        // Tool calls by their stream index: (id, name, arguments so far).
        let mut calls: Vec<(String, String, String)> = Vec::new();
        let mut finish = String::new();
        let mut usage = json!({});
        crate::sse::post(req, body, self.stall, "the provider", cancel, api_error, &mut |data| {
            if data.trim() == "[DONE]" {
                return Ok(false);
            }
            let ev: Value = serde_json::from_str(data).map_err(|e| format!("bad stream event: {e}"))?;
            if let Some(msg) = ev["error"]["message"].as_str() {
                return Err(format!("Provider stream error: {msg}"));
            }
            if let Some(u) = ev.get("usage").filter(|u| u.is_object()) {
                usage = json!({"input_tokens": u["prompt_tokens"], "output_tokens": u["completion_tokens"]});
            }
            let Some(choice) = ev["choices"].get(0) else { return Ok(true) };
            if let Some(f) = choice["finish_reason"].as_str() {
                finish = f.to_string();
            }
            let d = &choice["delta"];
            // Compatible servers stream reasoning under several names; it is shown, never sent back.
            for k in ["reasoning_content", "reasoning"] {
                if let Some(t) = d[k].as_str().filter(|t| !t.is_empty()) {
                    thinking.push_str(t);
                    on(Event::Thinking(t));
                }
            }
            if let Some(t) = d["content"].as_str().filter(|t| !t.is_empty()) {
                text.push_str(t);
                on(Event::Text(t));
            }
            for tc in d["tool_calls"].as_array().into_iter().flatten() {
                let i = tc["index"].as_u64().unwrap_or(calls.len() as u64) as usize;
                while calls.len() <= i {
                    calls.push(Default::default());
                }
                let call = &mut calls[i];
                let fresh = call.1.is_empty();
                // A call keeps the first id it was shown under: one arriving late must not rename it mid-stream.
                if let Some(id) = tc["id"].as_str().filter(|id| !id.is_empty() && call.0.is_empty()) {
                    call.0 = id.to_string();
                }
                if let Some(name) = tc["function"]["name"].as_str().filter(|n| !n.is_empty()) {
                    call.1 = name.to_string();
                }
                if call.0.is_empty() {
                    call.0 = format!("call_{}_{i}", crate::doc::random_u64() & 0xffff_ffff);
                }
                if fresh && !call.1.is_empty() {
                    on(Event::ToolStart { id: &call.0, name: &call.1 });
                }
                if let Some(a) = tc["function"]["arguments"].as_str().filter(|a| !a.is_empty()) {
                    call.2.push_str(a);
                    on(Event::ToolInput { id: &call.0, name: &call.1, json: &call.2 });
                }
            }
            Ok(true)
        })?;
        let mut content = Vec::new();
        if !thinking.is_empty() {
            content.push(json!({"type": "thinking", "thinking": thinking}));
        }
        if !text.is_empty() {
            content.push(json!({"type": "text", "text": text}));
        }
        let calls: Vec<_> = calls.into_iter().filter(|c| !c.1.is_empty()).collect();
        for (id, name, args) in &calls {
            content.push(json!({"type": "tool_use", "id": id, "name": name, "input": crate::claude::tool_input(args)}));
        }
        // Whether tools run is decided by the calls collected, not by the finish reason (servers differ).
        // A reply cut off by the token limit stays max_tokens even with calls in it: the last one may be truncated.
        let stop = if finish == "length" { "max_tokens" } else if !calls.is_empty() { "tool_use" } else { "end_turn" };
        Ok(json!({"content": content, "stop_reason": stop, "usage": usage}))
    }
}

/// A Claude Messages request as a Chat Completions request. Claude-only fields (prompt caching, thinking, betas)
/// are dropped; tool results become `tool` messages, and their screenshots one user message after all of a turn's
/// tool replies (tool messages are text-only), or a note when the model cannot see images.
pub fn translate(body: &Value, vision: bool, openai: bool) -> Value {
    let mut messages = Vec::new();
    let system: String = match &body["system"] {
        Value::String(s) => s.clone(),
        Value::Array(parts) => parts.iter().filter_map(|p| p["text"].as_str()).collect::<Vec<_>>().join("\n\n"),
        _ => String::new(),
    };
    if !system.is_empty() {
        messages.push(json!({"role": "system", "content": system}));
    }
    for m in body["messages"].as_array().into_iter().flatten() {
        let blocks = match &m["content"] {
            Value::String(s) => vec![json!({"type": "text", "text": s})],
            Value::Array(b) => b.clone(),
            _ => Vec::new(),
        };
        if m["role"] == "assistant" {
            let text: String = blocks.iter().filter(|b| b["type"] == "text").filter_map(|b| b["text"].as_str()).collect();
            let calls: Vec<Value> = blocks
                .iter()
                .filter(|b| b["type"] == "tool_use")
                .map(|b| json!({"id": b["id"], "type": "function", "function": {"name": b["name"], "arguments": b["input"].to_string()}}))
                .collect();
            let mut msg = json!({"role": "assistant", "content": if text.is_empty() { Value::Null } else { json!(text) }});
            if !calls.is_empty() {
                msg["tool_calls"] = json!(calls);
            }
            messages.push(msg);
            continue;
        }
        let mut parts = Vec::new();
        let mut images = Vec::new();
        for b in &blocks {
            match b["type"].as_str().unwrap_or("") {
                "tool_result" => {
                    let inner = match &b["content"] {
                        Value::String(s) => vec![json!({"type": "text", "text": s})],
                        Value::Array(a) => a.clone(),
                        _ => Vec::new(),
                    };
                    let mut text: String = inner.iter().filter(|p| p["type"] == "text").filter_map(|p| p["text"].as_str()).collect::<Vec<_>>().join("\n");
                    let shots: Vec<Value> = inner.iter().filter(|p| p["type"] == "image").map(image_part).collect();
                    if !shots.is_empty() {
                        text.push_str(if vision {
                            "\n[The screenshot of this call follows in the next message.]"
                        } else {
                            "\n[A screenshot was taken, but this model cannot see images: the screenshot is not available.]"
                        });
                        if vision {
                            images.extend(shots);
                        }
                    }
                    messages.push(json!({"role": "tool", "tool_call_id": b["tool_use_id"], "content": text}));
                }
                "text" => parts.push(json!({"type": "text", "text": b["text"]})),
                "image" if vision => parts.push(image_part(b)),
                _ => {}
            }
        }
        // Nothing may sit between an assistant's tool calls and their replies: images come after all of them.
        parts.extend(images);
        if !parts.is_empty() {
            messages.push(json!({"role": "user", "content": parts}));
        }
    }
    let mut out = Map::new();
    out.insert("model".into(), body["model"].clone());
    out.insert("messages".into(), json!(messages));
    if let Some(tools) = body["tools"].as_array().filter(|t| !t.is_empty()) {
        let tools: Vec<Value> = tools
            .iter()
            .map(|t| json!({"type": "function", "function": {"name": t["name"], "description": t["description"], "parameters": t["input_schema"]}}))
            .collect();
        out.insert("tools".into(), json!(tools));
    }
    if let Some(n) = body["max_tokens"].as_u64() {
        out.insert(if openai { "max_completion_tokens" } else { "max_tokens" }.into(), json!(n));
    }
    if let (true, Some(effort)) = (openai && reasoning_model(body["model"].as_str().unwrap_or("")), body["output_config"]["effort"].as_str()) {
        out.insert("reasoning_effort".into(), json!(effort));
    }
    Value::Object(out)
}

/// OpenAI's reasoning models (o-series, gpt-5 and later), the only ones that take a reasoning effort.
pub fn reasoning_model(model: &str) -> bool {
    let m = model.rsplit('/').next().unwrap_or(model);
    (m.starts_with('o') && m[1..].starts_with(|c: char| c.is_ascii_digit())) || ["gpt-5", "gpt-6", "gpt-7"].iter().any(|p| m.starts_with(p))
}

fn image_part(b: &Value) -> Value {
    let src = &b["source"];
    json!({"type": "image_url", "image_url": {"url": format!("data:{};base64,{}", src["media_type"].as_str().unwrap_or("image/png"), src["data"].as_str().unwrap_or(""))}})
}

/// The failure for an HTTP error status. A rejected key can only be fixed in the provider's settings.
pub fn api_error(status: u16, msg: &str) -> String {
    if status == 401 || status == 403 {
        format!("The provider rejected this key ({msg}). Check it in Agent settings (gear icon).")
    } else {
        format!("Provider error {status}: {msg}")
    }
}

impl crate::provider::Provider for Client {
    fn stream(&self, body: Value, beta: Option<&str>, cancel: &dyn Fn() -> bool, on: &mut dyn FnMut(Event)) -> Result<Value, String> {
        Client::stream(self, body, beta, cancel, on)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::provider::tests::fake_server;
    use serde_json::json;

    fn chunks(events: &[Value]) -> String {
        events.iter().map(|e| format!("data: {e}\n\n")).collect::<String>() + "data: [DONE]\n\n"
    }

    fn delta(d: Value) -> Value {
        json!({"choices": [{"index": 0, "delta": d, "finish_reason": null}]})
    }

    fn record(events: &mut Vec<String>) -> impl FnMut(Event) + '_ {
        move |ev| events.push(match ev {
            Event::Text(t) => format!("text:{t}"),
            Event::Thinking(t) => format!("thinking:{t}"),
            Event::ToolStart { id, name } => format!("start:{id}:{name}"),
            Event::ToolInput { id, json, .. } => format!("input:{id}:{json}"),
        })
    }

    #[test]
    fn a_text_reply_streams_with_its_usage_and_reasoning() {
        let reply = chunks(&[
            delta(json!({"role": "assistant", "reasoning_content": "Plan first."})),
            delta(json!({"content": "Hello "})),
            delta(json!({"content": "there"})),
            json!({"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}),
            json!({"choices": [], "usage": {"prompt_tokens": 21, "completion_tokens": 4}}),
        ]);
        let (base, seen) = fake_server(vec![reply]);
        let c = Client::new(&format!("{base}/v1"), Some("sk-or".into()), true);
        let mut events = Vec::new();
        let out = c.stream(json!({"model": "m", "max_tokens": 100, "messages": [{"role": "user", "content": "Hi"}]}), None, &|| false, &mut record(&mut events)).unwrap();
        assert_eq!(events, ["thinking:Plan first.", "text:Hello ", "text:there"]);
        assert_eq!(out["content"], json!([{"type": "thinking", "thinking": "Plan first."}, {"type": "text", "text": "Hello there"}]));
        assert_eq!(out["stop_reason"], "end_turn");
        assert_eq!(out["usage"], json!({"input_tokens": 21, "output_tokens": 4}));
        let r = seen.lock().unwrap()[0].clone();
        assert_eq!(r.path, "/v1/chat/completions");
        assert!(r.headers.iter().any(|(k, v)| k == "authorization" && v == "Bearer sk-or"));
        assert_eq!(r.body["stream"], true);
        assert_eq!(r.body["stream_options"], json!({"include_usage": true}));
    }

    #[test]
    fn tool_calls_arrive_in_fragments_whole_or_in_parallel() {
        let reply = chunks(&[
            delta(json!({"tool_calls": [{"index": 0, "id": "call_a", "type": "function", "function": {"name": "execute", "arguments": ""}}]})),
            delta(json!({"tool_calls": [{"index": 0, "function": {"arguments": "{\"input\":"}}]})),
            delta(json!({"tool_calls": [{"index": 1, "id": "call_b", "type": "function", "function": {"name": "get_editor_state", "arguments": "{}"}}]})),
            delta(json!({"tool_calls": [{"index": 0, "function": {"arguments": "\"Insert(document)\"}"}}]})),
            // OpenRouter-style: the usage chunk repeats the finish reason; servers may omit ids.
            json!({"choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls"}], "usage": {"prompt_tokens": 9, "completion_tokens": 7}}),
        ]);
        let (base, _) = fake_server(vec![reply]);
        let c = Client::new(&base, None, true);
        let mut events = Vec::new();
        let out = c.stream(json!({"model": "m", "messages": [{"role": "user", "content": "Go"}]}), None, &|| false, &mut record(&mut events)).unwrap();
        assert_eq!(out["stop_reason"], "tool_use");
        assert_eq!(out["content"], json!([
            {"type": "tool_use", "id": "call_a", "name": "execute", "input": {"input": "Insert(document)"}},
            {"type": "tool_use", "id": "call_b", "name": "get_editor_state", "input": {}},
        ]));
        assert_eq!(events[0], "start:call_a:execute");
        assert!(events.contains(&"input:call_a:{\"input\":\"Insert(document)\"}".to_string()), "{events:?}");
        assert!(events.contains(&"start:call_b:get_editor_state".to_string()));
        // A call whose arguments never parse is handed back for repair, like the Claude client does.
        let bad = chunks(&[delta(json!({"tool_calls": [{"index": 0, "function": {"name": "execute", "arguments": "{\"input\": \"x"}}]})), json!({"choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls"}]})]);
        let (base, _) = fake_server(vec![bad]);
        let out = Client::new(&base, None, true).stream(json!({"model": "m", "messages": []}), None, &|| false, &mut |_| {}).unwrap();
        assert_eq!(out["content"][0]["input"], json!({"__invalid_json": "{\"input\": \"x"}));
        assert_eq!(out["stop_reason"], "tool_use");
        // Cut off by the token limit mid-call: the agent must see max_tokens, so it does not run a truncated call.
        let cut = chunks(&[delta(json!({"tool_calls": [{"index": 0, "id": "call_c", "function": {"name": "execute", "arguments": "{\"input\": \"Ins"}}]})), json!({"choices": [{"index": 0, "delta": {}, "finish_reason": "length"}]})]);
        let (base, _) = fake_server(vec![cut]);
        let out = Client::new(&base, None, true).stream(json!({"model": "m", "messages": []}), None, &|| false, &mut |_| {}).unwrap();
        assert_eq!(out["stop_reason"], "max_tokens");
        // An id that only arrives after the call started does not change the call's identity mid-stream.
        let late = chunks(&[
            delta(json!({"tool_calls": [{"index": 0, "function": {"name": "execute", "arguments": "{}"}}]})),
            delta(json!({"tool_calls": [{"index": 0, "id": "call_late", "function": {"arguments": ""}}]})),
        ]);
        let (base, _) = fake_server(vec![late]);
        let mut ids = Vec::new();
        let out = Client::new(&base, None, true).stream(json!({"model": "m", "messages": []}), None, &|| false, &mut |ev| {
            if let Event::ToolStart { id, .. } | Event::ToolInput { id, .. } = ev {
                ids.push(id.to_string());
            }
        }).unwrap();
        assert!(ids.iter().all(|i| i == &ids[0]), "{ids:?}");
        assert_eq!(out["content"][0]["id"], json!(ids[0]));
        assert!(out["content"][0]["id"].as_str().is_some_and(|id| !id.is_empty()), "a missing id is made up");
    }

    /// The agent loop's history after one tool turn: text, a call, its result with a screenshot.
    fn history() -> Value {
        let png = json!({"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBOR"}});
        json!({
            "model": "gpt-x",
            "max_tokens": 32000,
            "system": [{"type": "text", "text": "You are a design agent.", "cache_control": {"type": "ephemeral"}}],
            "tools": [{"name": "execute", "description": "Run a snippet", "input_schema": {"type": "object", "properties": {"input": {"type": "string"}}}, "eager_input_streaming": true}],
            "output_config": {"effort": "high"},
            "thinking": {"type": "adaptive"},
            "messages": [
                {"role": "user", "content": [{"type": "text", "text": "Design a page"}]},
                {"role": "assistant", "content": [
                    {"type": "thinking", "thinking": "hm", "signature": "sig"},
                    {"type": "text", "text": "Building."},
                    {"type": "tool_use", "id": "call_a", "name": "execute", "input": {"input": "Insert(document)"}},
                    {"type": "tool_use", "id": "call_b", "name": "execute", "input": {"input": "TakeScreenshot()"}},
                ]},
                {"role": "user", "content": [
                    {"type": "tool_result", "tool_use_id": "call_a", "content": [{"type": "text", "text": "OK"}], "is_error": false},
                    {"type": "tool_result", "tool_use_id": "call_b", "content": [{"type": "text", "text": "OK"}, png], "is_error": false},
                ]},
            ],
        })
    }

    #[test]
    fn requests_become_chat_messages_with_screenshots_after_the_tool_replies() {
        let req = translate(&history(), true, false);
        assert_eq!(req["model"], "gpt-x");
        assert_eq!(req["max_tokens"], 32000);
        assert!(req.get("thinking").is_none() && req.get("output_config").is_none(), "Claude-only fields are dropped: {req}");
        assert_eq!(req["tools"], json!([{"type": "function", "function": {"name": "execute", "description": "Run a snippet", "parameters": {"type": "object", "properties": {"input": {"type": "string"}}}}}]));
        assert_eq!(req["messages"], json!([
            {"role": "system", "content": "You are a design agent."},
            {"role": "user", "content": [{"type": "text", "text": "Design a page"}]},
            {"role": "assistant", "content": "Building.", "tool_calls": [
                {"id": "call_a", "type": "function", "function": {"name": "execute", "arguments": "{\"input\":\"Insert(document)\"}"}},
                {"id": "call_b", "type": "function", "function": {"name": "execute", "arguments": "{\"input\":\"TakeScreenshot()\"}"}},
            ]},
            {"role": "tool", "tool_call_id": "call_a", "content": "OK"},
            {"role": "tool", "tool_call_id": "call_b", "content": "OK\n[The screenshot of this call follows in the next message.]"},
            {"role": "user", "content": [{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBOR"}}]},
        ]));
        // A model that cannot see images gets a note instead, and no image message.
        let blind = translate(&history(), false, false);
        let msgs = blind["messages"].as_array().unwrap();
        assert_eq!(msgs.len(), 5);
        assert!(msgs[4]["content"].as_str().unwrap().contains("screenshot is not available"), "{}", msgs[4]);
        // OpenAI itself wants max_completion_tokens; only its reasoning models take the effort.
        let openai = translate(&history(), true, true);
        assert_eq!(openai["max_completion_tokens"], 32000);
        assert!(openai.get("max_tokens").is_none());
        assert!(openai.get("reasoning_effort").is_none(), "gpt-x is not a reasoning model");
        for model in ["o3", "o4-mini", "gpt-5.1", "gpt-6-astra"] {
            let mut h = history();
            h["model"] = json!(model);
            assert_eq!(translate(&h, true, true)["reasoning_effort"], "high", "{model}");
        }
    }

    #[test]
    fn errors_read_like_the_providers_own() {
        assert!(api_error(401, "Incorrect API key").contains("rejected this key"));
        assert_eq!(api_error(404, "The model `x` does not exist"), "Provider error 404: The model `x` does not exist");
    }
}
