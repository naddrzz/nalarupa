//! MCP server over Streamable HTTP (JSON responses, no SSE) on 127.0.0.1.
//! External agents (Claude Code, Cursor, Codex…) call the same tools as the
//! built-in agent. An HTTP endpoint served by the app itself needs no extra binary.

use crate::engine::{engine, tool_specs};
use serde_json::{json, Value};
use std::io::Read;
use std::path::PathBuf;

pub const DEFAULT_PORT: u16 = 47700;

const INSTRUCTIONS: &str = "Nalarupa is a desktop design editor (infinite canvas, .pen documents). \
Call get_editor_state first, then read_guide before the first execute. Edit the canvas only through execute; \
never edit .pen files on disk while the app has them open.";

/// Start the server on the first free port from DEFAULT_PORT; returns the port.
pub fn start() -> Result<u16, String> {
    for port in DEFAULT_PORT..DEFAULT_PORT + 20 {
        let Ok(server) = tiny_http::Server::http(("127.0.0.1", port)) else { continue };
        std::thread::spawn(move || {
            for req in server.incoming_requests() {
                std::thread::spawn(move || handle(req));
            }
        });
        return Ok(port);
    }
    Err("no free port for the MCP server".into())
}

pub fn url(port: u16) -> String {
    format!("http://127.0.0.1:{port}/mcp")
}

fn header(name: &str, value: &str) -> tiny_http::Header {
    tiny_http::Header::from_bytes(name.as_bytes(), value.as_bytes()).unwrap()
}

/// True for `http://<localhost|127.0.0.1|[::1]>[:port]` exactly; `null`, other schemes and lookalike hosts are not local.
fn local_origin(origin: &str) -> bool {
    let Some(authority) = origin.strip_prefix("http://") else { return false };
    let (host, port) = match authority.strip_prefix('[') {
        Some(_) => authority.split_at(authority.find(']').map_or(authority.len(), |i| i + 1)),
        None => authority.split_at(authority.find(':').unwrap_or(authority.len())),
    };
    let port_ok = port.is_empty() || port.strip_prefix(':').is_some_and(|p| !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()));
    port_ok && matches!(host, "localhost" | "127.0.0.1" | "[::1]")
}

#[cfg(test)]
mod tests {
    use super::local_origin;

    #[test]
    fn origin_guard_matches_exact_local_hosts() {
        for ok in ["http://localhost", "http://localhost:5173", "http://127.0.0.1:47700", "http://[::1]", "http://[::1]:3000"] {
            assert!(local_origin(ok), "{ok}");
        }
        for bad in [
            "null", "", "https://localhost", "http://localhost.evil.com", "http://localhostevil.com", "http://127.0.0.1.evil.com",
            "http://127.0.0.10", "http://localhost:", "http://localhost:80a", "http://localhost/x", "http://localhost@evil.com",
            "http://[::1]x", "http://[::1", "http://evil.com", "http://localhost:80:80",
        ] {
            assert!(!local_origin(bad), "{bad}");
        }
    }
}

fn handle(mut req: tiny_http::Request) {
    // DNS-rebinding guard: browsers send Origin; only local pages may talk to us.
    let origin_ok = req.headers().iter().find(|h| h.field.equiv("Origin")).map_or(true, |h| local_origin(h.value.as_str()));
    if !origin_ok {
        let _ = req.respond(tiny_http::Response::from_string("forbidden origin").with_status_code(403));
        return;
    }
    // `/mcp` for external agents, `/mcp/<agent-id>` for Nalarupa's own agents (attribution).
    let path = req.url().split('?').next().unwrap_or("").to_string();
    let agent = match path.strip_prefix("/mcp") {
        Some("") | Some("/") => None,
        Some(rest) if rest.starts_with('/') && rest.len() > 1 => Some(rest[1..].to_string()),
        _ => {
            let _ = req.respond(tiny_http::Response::from_string("not found").with_status_code(404));
            return;
        }
    };
    if *req.method() != tiny_http::Method::Post {
        let _ = req.respond(tiny_http::Response::from_string("").with_status_code(405).with_header(header("Allow", "POST")));
        return;
    }
    let mut body = String::new();
    if req.as_reader().take(64 * 1024 * 1024).read_to_string(&mut body).is_err() {
        let _ = req.respond(tiny_http::Response::from_string("bad body").with_status_code(400));
        return;
    }
    let reply = match serde_json::from_str::<Value>(&body) {
        Ok(Value::Array(batch)) => {
            let out: Vec<Value> = batch.iter().filter_map(|m| rpc(m, agent.as_deref())).collect();
            (!out.is_empty()).then(|| Value::Array(out))
        }
        Ok(msg) => rpc(&msg, agent.as_deref()),
        Err(e) => Some(json!({"jsonrpc": "2.0", "id": null, "error": {"code": -32700, "message": format!("parse error: {e}")}})),
    };
    let res = match reply {
        Some(v) => tiny_http::Response::from_string(v.to_string()).with_header(header("Content-Type", "application/json")),
        None => tiny_http::Response::from_string("").with_status_code(202),
    };
    let _ = req.respond(res);
}

/// Tools visible to a caller: children lose get_editor_state; only a main agent with
/// parallel agents enabled gets spawn_agents.
fn tools_for(agent: Option<&str>) -> Value {
    let (is_child, can_spawn) = {
        let e = engine();
        let p = agent.and_then(|a| e.agent.profile(a));
        (p.is_some_and(|p| p.parent.is_some()), p.is_some_and(|p| p.parent.is_none()) && e.agent.parallel.0 > 1)
    };
    let mut tools: Vec<Value> = tool_specs().as_array().unwrap().iter().filter(|t| !is_child || t["name"] != "get_editor_state").cloned().collect();
    if can_spawn {
        let s = crate::agent::spawn_spec();
        tools.push(json!({"name": s["name"], "description": s["description"], "inputSchema": s["input_schema"]}));
    }
    if agent.is_some() {
        tools.push(json!({"name": crate::agent::PERMISSION_TOOL,
            "description": "Internal: Nalarupa answers Claude Code permission prompts with this tool. Never call it yourself.",
            "inputSchema": {"type": "object", "properties": {"tool_name": {"type": "string"}, "input": {"type": "object"}, "tool_use_id": {"type": "string"}}}}));
    }
    Value::Array(tools)
}

fn rpc(msg: &Value, agent: Option<&str>) -> Option<Value> {
    let id = msg.get("id").cloned()?;
    let method = msg["method"].as_str().unwrap_or("");
    let result = match method {
        "initialize" => Ok(json!({
            "protocolVersion": msg["params"]["protocolVersion"].as_str().unwrap_or("2025-06-18"),
            "capabilities": {"tools": {"listChanged": false}},
            "serverInfo": {"name": "nalarupa", "version": env!("CARGO_PKG_VERSION")},
            "instructions": INSTRUCTIONS,
        })),
        "ping" => Ok(json!({})),
        "tools/list" => Ok(json!({"tools": tools_for(agent)})),
        "tools/call" => {
            let name = msg["params"]["name"].as_str().unwrap_or("");
            let mut args = msg["params"].get("arguments").cloned().unwrap_or(json!({}));
            let out = if name == "spawn_agents" {
                match agent {
                    Some(a) => crate::agent::spawn_children(a, &args),
                    None => crate::engine::ToolOut::err("spawn_agents is only available to Nalarupa's built-in agents"),
                }
            } else if let (crate::agent::PERMISSION_TOOL, Some(a)) = (name, agent) {
                // Blocks this request's thread (not the engine) until the user answers.
                crate::agent::ask_permission(a, &args)
            } else {
                match agent {
                    Some(a) => args["__agent"] = json!(a),
                    // External agents (Claude Code / Cursor / Codex configured by the user).
                    None => {
                        args["__agent"] = json!("external");
                        engine().emit(json!({"type": "agent", "kind": "external", "text": name}));
                    }
                }
                crate::engine::call_tool(name, &args)
            };
            // The engine guard is gone here: rasterize screenshots without blocking the canvas.
            let out = out.finish();
            let mut content = vec![json!({"type": "text", "text": out.text})];
            content.extend(out.images.into_iter().map(|d| json!({"type": "image", "data": d, "mimeType": "image/png"})));
            Ok(json!({"content": content, "isError": out.error}))
        }
        _ => Err((-32601, format!("method not found: {method}"))),
    };
    Some(match result {
        Ok(r) => json!({"jsonrpc": "2.0", "id": id, "result": r}),
        Err((code, message)) => json!({"jsonrpc": "2.0", "id": id, "error": {"code": code, "message": message}}),
    })
}

/// Register Nalarupa's MCP endpoint with an external agent. Only runs on an explicit user action.
pub fn install(client: &str, port: u16) -> Result<String, String> {
    let url = url(port);
    let home = dirs::home_dir().ok_or("no home directory")?;
    match client {
        "claude-code" => {
            let out = std::process::Command::new("claude")
                .args(["mcp", "add", "--transport", "http", "--scope", "user", "nalarupa", &url])
                .output()
                .map_err(|e| format!("could not run `claude` ({e})"))?;
            let text = String::from_utf8_lossy(&out.stdout).to_string() + &String::from_utf8_lossy(&out.stderr);
            if out.status.success() || text.contains("already exists") {
                Ok(format!("Claude Code: nalarupa → {url}"))
            } else {
                Err(text)
            }
        }
        "cursor" => merge_json(home.join(".cursor").join("mcp.json"), "mcpServers", json!({"url": url})).map(|p| format!("Cursor: wrote {p}")),
        "codex" => {
            let path = home.join(".codex").join("config.toml");
            let mut text = std::fs::read_to_string(&path).unwrap_or_default();
            if text.contains("[mcp_servers.nalarupa]") {
                return Ok(format!("Codex: already configured in {}", path.display()));
            }
            text += &format!("\n[mcp_servers.nalarupa]\nurl = \"{url}\"\n");
            std::fs::create_dir_all(path.parent().unwrap()).map_err(|e| e.to_string())?;
            std::fs::write(&path, text).map_err(|e| e.to_string())?;
            Ok(format!("Codex: wrote {}", path.display()))
        }
        other => Err(format!("unknown client '{other}' (claude-code, cursor, codex)")),
    }
}

fn merge_json(path: PathBuf, key: &str, entry: Value) -> Result<String, String> {
    // A missing file starts empty; one that cannot be read or parsed (a trailing comma, comments) is left alone, not replaced.
    let mut root: Value = match std::fs::read_to_string(&path) {
        Ok(t) => serde_json::from_str(&t).map_err(|e| format!("{} is not valid JSON ({e}); not touching it", path.display()))?,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => json!({}),
        Err(e) => return Err(format!("could not read {}: {e}", path.display())),
    };
    if !root.is_object() {
        return Err(format!("{} is not a JSON object; not touching it", path.display()));
    }
    if !root[key].is_object() {
        root[key] = json!({});
    }
    root[key]["nalarupa"] = entry;
    std::fs::create_dir_all(path.parent().unwrap()).map_err(|e| e.to_string())?;
    std::fs::write(&path, serde_json::to_string_pretty(&root).unwrap()).map_err(|e| e.to_string())?;
    Ok(path.display().to_string())
}
