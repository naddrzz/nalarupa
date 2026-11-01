//! The OpenAI Responses wire format (`POST <base>/responses`), needed for OpenAI models that only call tools through
//! it. Requests come in Claude Messages shape and are translated; the stream is translated back into the same events
//! and content blocks the Claude client returns. The full history is sent every turn (`store: false`); reasoning
//! items ride along in the history as `{"type": "reasoning_item", "item": …}` blocks and are sent back unchanged.

use crate::claude::Event;
use serde_json::{json, Map, Value};
use std::time::Duration;

/// A silent stream fails after this long.
const STALL: Duration = Duration::from_secs(120);

pub struct Client {
    http: ureq::Agent,
    url: String,
    key: Option<String>,
    /// Whether the model can see images: screenshots are sent, or replaced by a note.
    vision: bool,
    /// OpenAI's own API, which takes the effort as `reasoning.effort`.
    openai: bool,
    stall: Duration,
}

impl Client {
    /// `base` is the API root (e.g. `https://api.openai.com/v1`); a blank key sends no Authorization header.
    pub fn new(base: &str, key: Option<String>, vision: bool) -> Client {
        let http = ureq::Agent::config_builder()
            .timeout_global(Some(Duration::from_secs(900)))
            .http_status_as_error(false)
            .build()
            .into();
        Client { http, url: crate::provider::endpoint(base, "/responses"), key: key.filter(|k| !k.trim().is_empty()), vision, openai: base.contains("://api.openai.com"), stall: STALL }
    }

    pub fn stream(&self, body: Value, _beta: Option<&str>, cancel: &dyn Fn() -> bool, on: &mut dyn FnMut(Event)) -> Result<Value, String> {
        let mut req = self.http.post(&self.url).header("content-type", "application/json");
        if let Some(k) = &self.key {
            req = req.header("authorization", &format!("Bearer {k}"));
        }
        let mut body = translate(&body, self.vision, self.openai);
        body["stream"] = json!(true);
        let mut thinking = String::new();
        let mut text = String::new();
        // Text by output index, so text written after a call stays after it when the turn is rebuilt.
        let mut texts: Vec<(usize, String)> = Vec::new();
        let mut refusal = String::new();
        let mut reasoning: Vec<Value> = Vec::new();
        // Function calls by output index: (call_id, name, arguments so far).
        let mut calls: Vec<(usize, String, String, String)> = Vec::new();
        let mut stop = "end_turn";
        let mut usage = json!({});
        // Finished only by response.completed or .incomplete; a socket that just closes means the reply was cut.
        let mut ended = false;
        crate::sse::post(req, body, self.stall, "the provider", cancel, crate::openai_chat::api_error, &mut |data| {
            let ev: Value = serde_json::from_str(data).map_err(|e| format!("bad stream event: {e}"))?;
            let idx = ev["output_index"].as_u64().unwrap_or(0) as usize;
            match ev["type"].as_str().unwrap_or("") {
                "response.reasoning_summary_text.delta" | "response.reasoning_text.delta" => {
                    let t = ev["delta"].as_str().unwrap_or("");
                    thinking.push_str(t);
                    on(Event::Thinking(t));
                }
                "response.refusal.delta" => refusal.push_str(ev["delta"].as_str().unwrap_or("")),
                "response.output_text.delta" => {
                    let t = ev["delta"].as_str().unwrap_or("");
                    text.push_str(t);
                    match texts.last_mut() {
                        Some((i, seg)) if *i == idx => seg.push_str(t),
                        _ if !t.is_empty() => texts.push((idx, t.to_string())),
                        _ => {}
                    }
                    on(Event::Text(t));
                }
                "response.output_item.added" if ev["item"]["type"] == "function_call" => {
                    let item = &ev["item"];
                    // Results must reference `call_id`; the item's own `id` (`fc_…`) is a different thing.
                    let id = item["call_id"].as_str().or(item["id"].as_str()).unwrap_or("").to_string();
                    let name = item["name"].as_str().unwrap_or("").to_string();
                    on(Event::ToolStart { id: &id, name: &name });
                    calls.push((idx, id, name, item["arguments"].as_str().unwrap_or("").to_string()));
                }
                "response.function_call_arguments.delta" => {
                    if let Some(c) = calls.iter_mut().find(|c| c.0 == idx) {
                        c.3.push_str(ev["delta"].as_str().unwrap_or(""));
                        on(Event::ToolInput { id: &c.1, name: &c.2, json: &c.3 });
                    }
                }
                "response.output_item.done" => {
                    let item = &ev["item"];
                    match item["type"].as_str().unwrap_or("") {
                        "function_call" => {
                            if let Some(c) = calls.iter_mut().find(|c| c.0 == idx) {
                                if let Some(a) = item["arguments"].as_str() {
                                    c.3 = a.to_string();
                                }
                            }
                        }
                        "reasoning" => reasoning.push(item.clone()),
                        _ => {}
                    }
                }
                "response.completed" => {
                    usage = usage_of(&ev["response"]);
                    ended = true;
                    return Ok(false);
                }
                "response.incomplete" => {
                    usage = usage_of(&ev["response"]);
                    let reason = ev["response"]["incomplete_details"]["reason"].as_str().unwrap_or("unknown");
                    if reason != "max_output_tokens" {
                        return Err(format!("The provider stopped the reply early ({reason})."));
                    }
                    stop = "max_tokens";
                    ended = true;
                    return Ok(false);
                }
                "response.failed" => {
                    let e = &ev["response"]["error"];
                    return Err(format!("Provider error: {}", e["message"].as_str().unwrap_or("the response failed")));
                }
                "error" => return Err(format!("Provider stream error: {}", ev["message"].as_str().or(ev["error"]["message"].as_str()).unwrap_or("unknown"))),
                _ => {}
            }
            Ok(true)
        })?;
        if !ended {
            return Err("The provider's stream ended before the reply finished. Try again.".into());
        }
        // A refusal is not an empty success: stop_reason `refusal`, as the Claude API reports it, so the agent
        // keeps the prompt in its history and shows the model's reason.
        if text.is_empty() && calls.is_empty() && !refusal.is_empty() {
            return Ok(json!({"content": [], "stop_reason": "refusal", "refusal": refusal.trim(), "usage": usage}));
        }
        let mut content = Vec::new();
        if !thinking.is_empty() {
            content.push(json!({"type": "thinking", "thinking": thinking}));
        }
        content.extend(reasoning.into_iter().map(|item| json!({"type": "reasoning_item", "item": item})));
        // A reply cut off by the token limit stays max_tokens even with calls in it: the last one may be truncated.
        if !calls.is_empty() && stop == "end_turn" {
            stop = "tool_use";
        }
        // Text segments and calls in the order the model wrote them (their output index).
        let mut parts: Vec<(usize, Value)> = texts.into_iter().map(|(i, t)| (i, json!({"type": "text", "text": t}))).collect();
        parts.extend(calls.into_iter().map(|(i, id, name, args)| (i, json!({"type": "tool_use", "id": id, "name": name, "input": crate::claude::tool_input(&args)}))));
        parts.sort_by_key(|(i, _)| *i);
        content.extend(parts.into_iter().map(|(_, b)| b));
        Ok(json!({"content": content, "stop_reason": stop, "usage": usage}))
    }
}

fn usage_of(r: &Value) -> Value {
    json!({"input_tokens": r["usage"]["input_tokens"], "output_tokens": r["usage"]["output_tokens"]})
}

fn data_url(b: &Value) -> String {
    let src = &b["source"];
    format!("data:{};base64,{}", src["media_type"].as_str().unwrap_or("image/png"), src["data"].as_str().unwrap_or(""))
}

/// A Claude Messages request as a Responses request: system → `instructions`, the history → input items (reasoning
/// items sent back unchanged, tool calls as `function_call`, tool results as `function_call_output` with their
/// screenshots as `input_image`, or a note when the model cannot see images). Claude-only fields are dropped.
pub fn translate(body: &Value, vision: bool, openai: bool) -> Value {
    let mut input = Vec::new();
    let mut reasons = false;
    for m in body["messages"].as_array().into_iter().flatten() {
        let blocks = match &m["content"] {
            Value::String(s) => vec![json!({"type": "text", "text": s})],
            Value::Array(b) => b.clone(),
            _ => Vec::new(),
        };
        if m["role"] == "assistant" {
            for b in &blocks {
                match b["type"].as_str().unwrap_or("") {
                    "reasoning_item" => {
                        input.push(b["item"].clone());
                        reasons = true;
                    }
                    "text" => input.push(json!({"role": "assistant", "content": [{"type": "output_text", "text": b["text"]}]})),
                    "tool_use" => input.push(json!({"type": "function_call", "call_id": b["id"], "name": b["name"], "arguments": b["input"].to_string()})),
                    _ => {}
                }
            }
            continue;
        }
        let mut parts = Vec::new();
        for b in &blocks {
            match b["type"].as_str().unwrap_or("") {
                "tool_result" => {
                    let inner = match &b["content"] {
                        Value::String(s) => vec![json!({"type": "text", "text": s})],
                        Value::Array(a) => a.clone(),
                        _ => Vec::new(),
                    };
                    let has_image = inner.iter().any(|p| p["type"] == "image");
                    let output = if vision {
                        json!(inner
                            .iter()
                            .filter_map(|p| match p["type"].as_str() {
                                Some("text") => Some(json!({"type": "input_text", "text": p["text"]})),
                                Some("image") => Some(json!({"type": "input_image", "image_url": data_url(p)})),
                                _ => None,
                            })
                            .collect::<Vec<_>>())
                    } else {
                        let mut text: String = inner.iter().filter(|p| p["type"] == "text").filter_map(|p| p["text"].as_str()).collect::<Vec<_>>().join("\n");
                        if has_image {
                            text.push_str("\n[A screenshot was taken, but this model cannot see images: the screenshot is not available.]");
                        }
                        json!(text)
                    };
                    input.push(json!({"type": "function_call_output", "call_id": b["tool_use_id"], "output": output}));
                }
                "text" => parts.push(json!({"type": "input_text", "text": b["text"]})),
                "image" if vision => parts.push(json!({"type": "input_image", "image_url": data_url(b)})),
                _ => {}
            }
        }
        if !parts.is_empty() {
            input.push(json!({"role": "user", "content": parts}));
        }
    }
    let system: String = match &body["system"] {
        Value::String(s) => s.clone(),
        Value::Array(parts) => parts.iter().filter_map(|p| p["text"].as_str()).collect::<Vec<_>>().join("\n\n"),
        _ => String::new(),
    };
    let mut out = Map::new();
    out.insert("model".into(), body["model"].clone());
    if !system.is_empty() {
        out.insert("instructions".into(), json!(system));
    }
    out.insert("input".into(), json!(input));
    // No server-side state: the full history goes every turn, so local servers that store nothing work too.
    out.insert("store".into(), json!(false));
    if let Some(tools) = body["tools"].as_array().filter(|t| !t.is_empty()) {
        let tools: Vec<Value> = tools.iter().map(|t| json!({"type": "function", "name": t["name"], "description": t["description"], "parameters": t["input_schema"]})).collect();
        out.insert("tools".into(), json!(tools));
    }
    if let Some(n) = body["max_tokens"].as_u64() {
        out.insert("max_output_tokens".into(), json!(n));
    }
    if let (true, Some(effort)) = (openai && crate::openai_chat::reasoning_model(body["model"].as_str().unwrap_or("")), body["output_config"]["effort"].as_str()) {
        out.insert("reasoning".into(), json!({"effort": effort}));
        reasons = true;
    }
    if reasons {
        // Reasoning items come back encrypted (store: false), to be sent on the next turn.
        out.insert("include".into(), json!(["reasoning.encrypted_content"]));
    }
    Value::Object(out)
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

    fn events(evs: &[Value]) -> String {
        evs.iter().map(|e| format!("event: {}\ndata: {e}\n\n", e["type"].as_str().unwrap_or(""))).collect()
    }

    #[test]
    fn a_reply_with_reasoning_text_and_a_function_call_streams_back() {
        let reasoning = json!({"type": "reasoning", "id": "rs_1", "encrypted_content": "gAAA", "summary": [{"type": "summary_text", "text": "Plan."}]});
        let reply = events(&[
            json!({"type": "response.created", "response": {"id": "resp_1"}}),
            json!({"type": "response.output_item.added", "output_index": 0, "item": {"type": "reasoning", "id": "rs_1"}}),
            json!({"type": "response.reasoning_summary_text.delta", "output_index": 0, "delta": "Plan."}),
            json!({"type": "response.output_item.done", "output_index": 0, "item": reasoning}),
            json!({"type": "response.output_item.added", "output_index": 1, "item": {"type": "message", "id": "msg_1", "role": "assistant", "content": []}}),
            json!({"type": "response.output_text.delta", "output_index": 1, "delta": "Building "}),
            json!({"type": "response.output_text.delta", "output_index": 1, "delta": "the hero."}),
            json!({"type": "response.output_item.added", "output_index": 2, "item": {"type": "function_call", "id": "fc_9", "call_id": "call_9", "name": "execute", "arguments": ""}}),
            json!({"type": "response.function_call_arguments.delta", "output_index": 2, "delta": "{\"input\":"}),
            json!({"type": "response.function_call_arguments.delta", "output_index": 2, "delta": "\"Insert(document)\"}"}),
            json!({"type": "response.output_item.done", "output_index": 2, "item": {"type": "function_call", "id": "fc_9", "call_id": "call_9", "name": "execute", "arguments": "{\"input\":\"Insert(document)\"}"}}),
            json!({"type": "response.completed", "response": {"status": "completed", "usage": {"input_tokens": 50, "output_tokens": 20}}}),
        ]);
        let (base, seen) = fake_server(vec![reply]);
        let c = Client::new(&format!("{base}/v1"), Some("sk-x".into()), true);
        let mut got = Vec::new();
        let out = c.stream(json!({"model": "gpt-x", "max_tokens": 1000, "messages": [{"role": "user", "content": "Go"}]}), None, &|| false, &mut |ev| got.push(match ev {
            Event::Text(t) => format!("text:{t}"),
            Event::Thinking(t) => format!("thinking:{t}"),
            Event::ToolStart { id, name } => format!("start:{id}:{name}"),
            Event::ToolInput { id, json, .. } => format!("input:{id}:{json}"),
        })).unwrap();
        assert_eq!(got[0], "thinking:Plan.");
        assert!(got.contains(&"start:call_9:execute".to_string()), "results reference the call_id, not the item id: {got:?}");
        assert_eq!(out["stop_reason"], "tool_use");
        assert_eq!(out["usage"], json!({"input_tokens": 50, "output_tokens": 20}));
        assert_eq!(out["content"], json!([
            {"type": "thinking", "thinking": "Plan."},
            {"type": "reasoning_item", "item": reasoning},
            {"type": "text", "text": "Building the hero."},
            {"type": "tool_use", "id": "call_9", "name": "execute", "input": {"input": "Insert(document)"}},
        ]));
        let r = seen.lock().unwrap()[0].clone();
        assert_eq!(r.path, "/v1/responses");
        assert_eq!(r.body["stream"], true);
        assert_eq!(r.body["store"], false);
    }

    #[test]
    fn the_history_becomes_input_items_with_images_in_the_tool_output() {
        let reasoning = json!({"type": "reasoning", "id": "rs_1", "encrypted_content": "gAAA", "summary": []});
        let png = json!({"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBOR"}});
        let body = json!({
            "model": "gpt-5.1",
            "max_tokens": 32000,
            "system": [{"type": "text", "text": "You are a design agent.", "cache_control": {"type": "ephemeral"}}],
            "tools": [{"name": "execute", "description": "Run", "input_schema": {"type": "object"}, "eager_input_streaming": true}],
            "output_config": {"effort": "medium"},
            "messages": [
                {"role": "user", "content": [{"type": "text", "text": "Design a page"}]},
                {"role": "assistant", "content": [
                    {"type": "thinking", "thinking": "Plan."},
                    {"type": "reasoning_item", "item": reasoning},
                    {"type": "text", "text": "Building."},
                    {"type": "tool_use", "id": "call_9", "name": "execute", "input": {"input": "TakeScreenshot()"}},
                ]},
                {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_9", "content": [{"type": "text", "text": "OK"}, png], "is_error": false}]},
            ],
        });
        let req = translate(&body, true, true);
        assert_eq!(req["instructions"], "You are a design agent.");
        assert_eq!(req["max_output_tokens"], 32000);
        assert_eq!(req["reasoning"], json!({"effort": "medium"}));
        assert_eq!(req["include"], json!(["reasoning.encrypted_content"]));
        assert_eq!(req["tools"], json!([{"type": "function", "name": "execute", "description": "Run", "parameters": {"type": "object"}}]));
        assert_eq!(req["input"], json!([
            {"role": "user", "content": [{"type": "input_text", "text": "Design a page"}]},
            reasoning,
            {"role": "assistant", "content": [{"type": "output_text", "text": "Building."}]},
            {"type": "function_call", "call_id": "call_9", "name": "execute", "arguments": "{\"input\":\"TakeScreenshot()\"}"},
            {"type": "function_call_output", "call_id": "call_9", "output": [
                {"type": "input_text", "text": "OK"},
                {"type": "input_image", "image_url": "data:image/png;base64,iVBOR"},
            ]},
        ]));
        let blind = translate(&body, false, false);
        assert!(blind["input"][4]["output"].as_str().unwrap().contains("screenshot is not available"), "{}", blind["input"][4]);
        assert!(blind.get("reasoning").is_none(), "effort goes only to OpenAI's own API");
    }

    #[test]
    fn an_incomplete_or_failed_response_reads_clearly() {
        let cut = events(&[json!({"type": "response.incomplete", "response": {"status": "incomplete", "incomplete_details": {"reason": "max_output_tokens"}, "usage": {"input_tokens": 1, "output_tokens": 2}}})]);
        let failed = events(&[json!({"type": "response.failed", "response": {"status": "failed", "error": {"code": "server_error", "message": "The server had an error"}}})]);
        let (base, _) = fake_server(vec![cut, failed]);
        let c = Client::new(&base, None, true);
        let out = c.stream(json!({"model": "m", "messages": []}), None, &|| false, &mut |_| {}).unwrap();
        assert_eq!(out["stop_reason"], "max_tokens");
        let e = c.stream(json!({"model": "m", "messages": []}), None, &|| false, &mut |_| {}).unwrap_err();
        assert!(e.contains("The server had an error"), "{e}");
        // Text after a call stays after it, so the replayed history keeps the model's order.
        let ordered = events(&[
            json!({"type": "response.output_text.delta", "output_index": 0, "delta": "First."}),
            json!({"type": "response.output_item.added", "output_index": 1, "item": {"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "execute", "arguments": "{}"}}),
            json!({"type": "response.output_text.delta", "output_index": 2, "delta": "Then."}),
            json!({"type": "response.completed", "response": {"status": "completed"}}),
        ]);
        let (base, _) = fake_server(vec![ordered]);
        let out = Client::new(&base, None, true).stream(json!({"model": "m", "messages": []}), None, &|| false, &mut |_| {}).unwrap();
        let kinds: Vec<&str> = out["content"].as_array().unwrap().iter().map(|b| b["text"].as_str().or(b["name"].as_str()).unwrap_or("")).collect();
        assert_eq!(kinds, ["First.", "execute", "Then."]);
        // Cut off by the token limit with a call in it: max_tokens, so the agent does not run a truncated call.
        let cut_call = events(&[
            json!({"type": "response.output_item.added", "output_index": 0, "item": {"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "execute", "arguments": ""}}),
            json!({"type": "response.function_call_arguments.delta", "output_index": 0, "delta": "{\"input\": \"Ins"}),
            json!({"type": "response.incomplete", "response": {"status": "incomplete", "incomplete_details": {"reason": "max_output_tokens"}}}),
        ]);
        let refused = events(&[
            json!({"type": "response.refusal.delta", "output_index": 0, "delta": "I can't help with that."}),
            json!({"type": "response.completed", "response": {"status": "completed"}}),
        ]);
        let (base, _) = fake_server(vec![cut_call, refused]);
        let c = Client::new(&base, None, true);
        assert_eq!(c.stream(json!({"model": "m", "messages": []}), None, &|| false, &mut |_| {}).unwrap()["stop_reason"], "max_tokens");
        let out = c.stream(json!({"model": "m", "messages": []}), None, &|| false, &mut |_| {}).unwrap();
        assert_eq!((out["stop_reason"].as_str(), out["refusal"].as_str()), (Some("refusal"), Some("I can't help with that.")));
        // An empty text delta opens no text block.
        let (base, _) = fake_server(vec![events(&[
            json!({"type": "response.output_text.delta", "output_index": 0, "delta": ""}),
            json!({"type": "response.completed", "response": {"status": "completed"}}),
        ])]);
        let out = Client::new(&base, None, true).stream(json!({"model": "m", "messages": []}), None, &|| false, &mut |_| {}).unwrap();
        assert_eq!(out["content"], json!([]));
        // Text, then the connection closes without response.completed: a cut-off reply, not a finished one.
        let (base, _) = fake_server(vec![events(&[json!({"type": "response.output_text.delta", "output_index": 0, "delta": "Half"})])]);
        let e = Client::new(&base, None, true).stream(json!({"model": "m", "messages": []}), None, &|| false, &mut |_| {}).unwrap_err();
        assert!(e.contains("ended before"), "{e}");
    }
}
