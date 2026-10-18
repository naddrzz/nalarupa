//! Streaming Claude Messages API client (raw HTTP + SSE; Rust has no official SDK).
//! Rebuilds the assistant content blocks exactly as sent (thinking signatures included)
//! so they can be appended to the history unchanged, and reports partial tool input
//! while the model is still writing it.

use serde_json::{json, Value};
use std::io::{BufRead, BufReader};
use std::time::Duration;

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
}

impl Client {
    pub fn new(key: String) -> Client {
        let http = ureq::Agent::config_builder()
            .timeout_global(Some(Duration::from_secs(900)))
            .http_status_as_error(false)
            .build()
            .into();
        Client { http, key }
    }

    /// POST /v1/messages with `stream: true`; returns `{content, stop_reason, usage}`.
    pub fn stream(&self, mut body: Value, beta: Option<&str>, cancel: &dyn Fn() -> bool, on: &mut dyn FnMut(Event)) -> Result<Value, String> {
        body["stream"] = json!(true);
        let mut req = self.http.post("https://api.anthropic.com/v1/messages")
            .header("x-api-key", &self.key)
            .header("anthropic-version", "2023-06-01")
            .header("content-type", "application/json");
        if let Some(b) = beta {
            req = req.header("anthropic-beta", b);
        }
        let res = req.send_json(&body).map_err(|e| format!("network error: {e}"))?;
        let status = res.status().as_u16();
        let (_, body) = res.into_parts();
        if status >= 400 {
            let text = body.into_with_config().limit(1 << 20).read_to_string().unwrap_or_default();
            let msg = serde_json::from_str::<Value>(&text).ok().and_then(|v| v["error"]["message"].as_str().map(str::to_string)).unwrap_or(text);
            return Err(format!("Claude API error {status}: {msg}"));
        }

        let mut blocks: Vec<Value> = Vec::new();
        let mut partial: Vec<String> = Vec::new();
        let mut stop_reason = Value::Null;
        let mut usage = json!({});
        let mut data = String::new();
        for line in BufReader::new(body.into_reader()).lines() {
            if cancel() {
                return Err("stopped".into());
            }
            let line = line.map_err(|e| format!("stream interrupted: {e}"))?;
            if let Some(d) = line.strip_prefix("data:") {
                data.push_str(d.trim_start());
                continue;
            }
            if !line.is_empty() || data.is_empty() {
                continue;
            }
            let ev: Value = serde_json::from_str(&std::mem::take(&mut data)).map_err(|e| format!("bad stream event: {e}"))?;
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
                    let Some(block) = blocks.get_mut(idx) else { continue };
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
                            block["input"] = if raw.trim().is_empty() {
                                json!({})
                            } else {
                                serde_json::from_str(&raw).unwrap_or_else(|_| json!({"__invalid_json": raw}))
                            };
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
        }
        blocks.retain(|b| !b.is_null());
        Ok(json!({"content": blocks, "stop_reason": stop_reason, "usage": usage}))
    }
}
