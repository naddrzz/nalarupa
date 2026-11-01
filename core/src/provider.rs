//! The model a run talks to, behind one interface. The agent loop and the AI Pen build requests in Claude Messages
//! shape (system, content blocks, tools, tool results with images) and read back `{content, stop_reason, usage}` in
//! the same shape; each wire format implements `Provider` and translates as needed.

use crate::claude::Event;
use crate::engine::Engine;
use serde_json::Value;

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

/// The model behind the API backend: the Claude API, at the base URL from the settings when one is set.
pub fn current(e: &Engine) -> Result<Box<dyn Provider>, String> {
    let key = e.api_key().ok_or("No Anthropic API key. Add one in Settings, set ANTHROPIC_API_KEY, or switch the backend to Claude Code.")?;
    Ok(Box::new(crate::claude::Client::at(key, e.setting("anthropic_base_url").as_deref())))
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use serde_json::json;
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

    #[test]
    fn a_base_url_routes_the_claude_api_and_keeps_its_headers() {
        let reply = sse(&[json!({"type": "message_stop"})]);
        for base in ["/gateway", "/gateway/", "/gateway/v1"] {
            let (root, seen) = fake_server(vec![reply.clone()]);
            let client = crate::claude::Client::at("sk-test".into(), Some(&format!("{root}{base}")));
            Provider::stream(&client, json!({"model": "m", "messages": []}), Some("b1"), &|| false, &mut |_| {}).unwrap();
            let r = seen.lock().unwrap()[0].clone();
            assert_eq!(r.path, "/gateway/v1/messages", "{base}");
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
