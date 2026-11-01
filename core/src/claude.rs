//! Streaming Claude Messages API client (raw HTTP + SSE; Rust has no official SDK).
//! Rebuilds the assistant content blocks exactly as sent (thinking signatures included)
//! so they can be appended to the history unchanged, and reports partial tool input
//! while the model is still writing it.

use serde_json::{json, Value};
use std::time::Duration;

const URL: &str = "https://api.anthropic.com/v1/messages";
/// A stream (or the wait for its headers) silent this long fails as stalled; the API pings every few seconds.
const STALL: Duration = Duration::from_secs(120);

pub enum Event<'a> {
    Text(&'a str),
    Thinking(&'a str),
    /// A tool call started streaming.
    ToolStart { id: &'a str, name: &'a str },
    /// Accumulated (partial, possibly invalid) JSON of a tool call's input so far.
    ToolInput { id: &'a str, name: &'a str, json: &'a str },
}

pub struct Client {
    http: ureq::Agent,
    key: String,
    url: String,
    stall: Duration,
}

impl Client {
    pub fn new(key: String) -> Client {
        let http = ureq::Agent::config_builder()
            .timeout_global(Some(Duration::from_secs(900)))
            .http_status_as_error(false)
            .build()
            .into();
        Client { http, key, url: URL.into(), stall: STALL }
    }

    /// A client for the Claude API at `base` (a gateway's root, with or without `/v1`, keeping any query), else the
    /// Anthropic API.
    pub fn at(key: String, base: Option<&str>) -> Client {
        let Some(b) = base.map(str::trim).filter(|b| !b.is_empty()) else { return Client::new(key) };
        let b = b.split('#').next().unwrap_or(b);
        let (root, query) = b.split_once('?').map_or((b, None), |(r, q)| (r, Some(q)));
        let mut url = format!("{}/v1/messages", root.trim_end_matches('/').trim_end_matches("/v1"));
        if let Some(q) = query.filter(|q| !q.is_empty()) {
            url = format!("{url}?{q}");
        }
        Client { url, ..Client::new(key) }
    }

    /// POST /v1/messages with `stream: true`; returns `{content, stop_reason, usage}`.
    pub fn stream(&self, mut body: Value, beta: Option<&str>, cancel: &dyn Fn() -> bool, on: &mut dyn FnMut(Event)) -> Result<Value, String> {
        body["stream"] = json!(true);
        // Reasoning items from an OpenAI Responses provider earlier in the chat mean nothing here.
        for m in body["messages"].as_array_mut().into_iter().flatten() {
            if let Some(blocks) = m["content"].as_array_mut() {
                blocks.retain(|b| b["type"] != "reasoning_item");
            }
        }
        let mut req = self.http.post(&self.url).header("anthropic-version", "2023-06-01").header("content-type", "application/json");
        // A keyless provider (a local Anthropic-compatible server) sends no key header at all.
        if !self.key.trim().is_empty() {
            req = req.header("x-api-key", &self.key);
        }
        if let Some(b) = beta {
            req = req.header("anthropic-beta", b);
        }
        let mut blocks: Vec<Value> = Vec::new();
        let mut partial: Vec<String> = Vec::new();
        let mut stop_reason = Value::Null;
        let mut usage = json!({});
        crate::sse::post(req, body, self.stall, "the Claude API", cancel, api_error, &mut |data| {
            let ev: Value = serde_json::from_str(data).map_err(|e| format!("bad stream event: {e}"))?;
            let idx = ev["index"].as_u64().unwrap_or(0) as usize;
            match ev["type"].as_str().unwrap_or("") {
                "message_start" => usage = ev["message"]["usage"].clone(),
                "content_block_start" => {
                    while blocks.len() <= idx {
                        blocks.push(Value::Null);
                        partial.push(String::new());
                    }
                    let block = ev["content_block"].clone();
                    if block["type"] == "tool_use" {
                        on(Event::ToolStart { id: block["id"].as_str().unwrap_or(""), name: block["name"].as_str().unwrap_or("") });
                    }
                    blocks[idx] = block;
                }
                "content_block_delta" => {
                    let Some(block) = blocks.get_mut(idx) else { return Ok(true) };
                    let d = &ev["delta"];
                    match d["type"].as_str().unwrap_or("") {
                        "text_delta" => {
                            let t = d["text"].as_str().unwrap_or("");
                            let cur = block["text"].as_str().unwrap_or("").to_string() + t;
                            block["text"] = json!(cur);
                            on(Event::Text(t));
                        }
                        "thinking_delta" => {
                            let t = d["thinking"].as_str().unwrap_or("");
                            let cur = block["thinking"].as_str().unwrap_or("").to_string() + t;
                            block["thinking"] = json!(cur);
                            on(Event::Thinking(t));
                        }
                        "signature_delta" => block["signature"] = d["signature"].clone(),
                        "input_json_delta" => {
                            partial[idx].push_str(d["partial_json"].as_str().unwrap_or(""));
                            on(Event::ToolInput { id: block["id"].as_str().unwrap_or(""), name: block["name"].as_str().unwrap_or(""), json: &partial[idx] });
                        }
                        _ => {}
                    }
                }
                "content_block_stop" => {
                    if let Some(block) = blocks.get_mut(idx) {
                        if block["type"] == "tool_use" {
                            let raw = partial.get(idx).cloned().unwrap_or_default();
                            // Eager input streaming skips server validation: parse strictly here.
                            block["input"] = tool_input(&raw);
                        }
                    }
                }
                "message_delta" => {
                    stop_reason = ev["delta"]["stop_reason"].clone();
                    if let Some(out) = ev["usage"]["output_tokens"].as_u64() {
                        usage["output_tokens"] = json!(out);
                    }
                }
                "error" => return Err(format!("Claude API stream error: {}", ev["error"]["message"].as_str().unwrap_or("unknown"))),
                _ => {}
            }
            Ok(true)
        })?;
        blocks.retain(|b| !b.is_null());
        Ok(json!({"content": blocks, "stop_reason": stop_reason, "usage": usage}))
    }
}

/// A tool call's streamed input: `{}` when empty, else its JSON, or `{"__invalid_json": raw}` for the agent to repair.
pub fn tool_input(raw: &str) -> Value {
    if raw.trim().is_empty() {
        json!({})
    } else {
        serde_json::from_str(raw).unwrap_or_else(|_| json!({"__invalid_json": raw}))
    }
}

/// The failure for an HTTP error status. A rejected key can only be fixed in the app's settings, so say so.
fn api_error(status: u16, msg: &str) -> String {
    if status == 401 {
        "The Claude API rejected this key. Replace it in Agent settings (gear icon) or switch the backend to Claude Code.".into()
    } else {
        format!("Claude API error {status}: {msg}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Instant;
    use std::io::{Read, Write};
    use std::net::TcpListener;
    use std::sync::atomic::{AtomicBool, Ordering};

    /// A server that sends one text event (or nothing) and then goes silent without closing.
    fn silent_server(first_event: bool) -> String {
        let l = TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("http://{}/v1/messages", l.local_addr().unwrap());
        std::thread::spawn(move || {
            let (mut s, _) = l.accept().unwrap();
            let mut buf = [0u8; 8192];
            let _ = s.read(&mut buf);
            if first_event {
                let ev = r#"{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}"#;
                let _ = write!(s, "HTTP/1.1 200 OK\r\ncontent-type: text/event-stream\r\n\r\ndata: {ev}\n\n");
            }
            std::thread::sleep(Duration::from_secs(30));
        });
        url
    }

    fn client(url: String, stall: Duration) -> Client {
        Client { url, stall, ..Client::new("k".into()) }
    }

    #[test]
    fn stop_interrupts_a_silent_stream_and_a_silent_header_wait() {
        for first_event in [true, false] {
            let c = client(silent_server(first_event), STALL);
            let stop = AtomicBool::new(false);
            let t = Instant::now();
            let r = std::thread::scope(|sc| {
                sc.spawn(|| {
                    std::thread::sleep(Duration::from_millis(500));
                    stop.store(true, Ordering::SeqCst);
                });
                c.stream(json!({}), None, &|| stop.load(Ordering::SeqCst), &mut |_| {})
            });
            assert_eq!(r.unwrap_err(), "stopped");
            assert!(t.elapsed() < Duration::from_secs(3), "stop took {:?}", t.elapsed());
        }
    }

    #[test]
    fn a_rejected_key_points_at_the_settings() {
        let e = api_error(401, "invalid x-api-key");
        assert!(e.contains("Agent settings") && !e.contains("401"), "{e}");
        assert_eq!(api_error(529, "Overloaded"), "Claude API error 529: Overloaded");
    }

    #[test]
    fn a_silent_stream_fails_as_stalled() {
        let c = client(silent_server(true), Duration::from_millis(600));
        let r = c.stream(json!({}), None, &|| false, &mut |_| {});
        assert!(r.unwrap_err().starts_with("connection stalled"));
    }

    #[test]
    fn a_cleanly_closed_stream_returns_its_content() {
        let l = TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("http://{}/v1/messages", l.local_addr().unwrap());
        std::thread::spawn(move || {
            let (mut s, _) = l.accept().unwrap();
            // Drain the whole request first: closing with unread data would reset the connection.
            let mut buf = [0u8; 8192];
            s.set_read_timeout(Some(Duration::from_millis(200))).unwrap();
            while s.read(&mut buf).map_or(false, |n| n > 0) {}
            let ev = r#"{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hi"}}"#;
            let _ = write!(s, "HTTP/1.1 200 OK\r\ncontent-type: text/event-stream\r\nconnection: close\r\n\r\ndata: {ev}\n\n");
        });
        let r = client(url, STALL).stream(json!({}), None, &|| false, &mut |_| {}).unwrap();
        assert_eq!(r["content"][0]["text"], "hi", "{r}");
    }
}
