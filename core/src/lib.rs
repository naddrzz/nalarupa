//! Nalarupa core. The GPUIX (Bun) front-end loads this as a C-ABI library:
//! `nr_call(json) -> json` for commands and `nr_render(...) -> *RGBA` for the canvas.

pub mod agent;
pub mod claude;
pub mod doc;
pub mod editor;
pub mod engine;
pub mod figma;
pub mod guide;
pub mod hit;
pub mod history;
pub mod html;
pub mod layout;
pub mod mcp;
pub mod net;
pub mod pen;
pub mod raster;
pub mod render;
pub mod resolve;
pub mod script;
pub mod shader;
pub mod stream;
pub mod svg;
pub mod text;

use doc::ROOT;
use engine::{engine, Engine};
use serde_json::{json, Value};
use std::ffi::{c_char, CString};

fn ids(v: &Value) -> Vec<String> {
    match v {
        Value::Array(a) => a.iter().filter_map(|x| x.as_str().map(str::to_string)).collect(),
        Value::String(s) => vec![s.clone()],
        _ => Vec::new(),
    }
}

/// Layer rows: auto-layout children in layout order, everything else
/// topmost first. `index` is the position among the siblings in the document.
fn layers(e: &Engine) -> Value {
    fn walk(nodes: &[Value], depth: usize, parent: &str, flow: bool, out: &mut Vec<Value>) {
        let mut order: Vec<(usize, &Value)> = nodes.iter().enumerate().collect();
        if !flow {
            order.reverse();
        }
        for (index, n) in order {
            let id = n["id"].as_str().unwrap_or_default();
            let layout = n["type"] == "frame" && n["layout"] != "none";
            let content = n["content"].as_str().filter(|_| n["type"] == "text").map(|c| c.lines().next().unwrap_or(c));
            out.push(json!({
                "id": id, "name": n["name"].as_str().filter(|s| !s.trim().is_empty()).or(content).unwrap_or(n["type"].as_str().unwrap_or("node")),
                "type": n["type"], "depth": depth, "parent": parent, "index": index, "layout": layout,
                "reusable": n["reusable"] == true, "hidden": n["enabled"] == false,
                "children": n["children"].as_array().map(|c| c.len()).unwrap_or(0),
            }));
            if let Some(kids) = n["children"].as_array() {
                walk(kids, depth + 1, id, layout, out);
            }
        }
    }
    let mut out = Vec::new();
    walk(e.doc.children(), 0, ROOT, false, &mut out);
    Value::Array(out)
}

fn rect_arg(v: &Value) -> Option<editor::Rect> {
    let g = |k: &str| v[k].as_f64();
    Some((g("x")?, g("y")?, g("w")?, g("h")?))
}

fn rect(f: &render::Flat) -> Value {
    json!({"id": f.id, "name": f.name, "type": f.ty, "x": f.x, "y": f.y, "w": f.w, "h": f.h, "parent": f.parent, "depth": f.depth,
           "placeholder": f.placeholder, "pending": f.pending, "m": f.m})
}

fn state(e: &Engine) -> Value {
    json!({"version": e.version, "title": e.title(), "path": e.doc.path, "dirty": e.dirty(), "selection": e.selection,
           "agentRunning": e.agent.running, "executing": e.executing, "penDrawing": !e.agent.pens.is_empty(), "mcpPort": e.mcp_port, "animating": !e.renderer.slides.is_empty() || script::busy()})
}

/// Execute one UI command.
pub fn dispatch(req: &Value) -> Result<Value, String> {
    let cmd = req["cmd"].as_str().unwrap_or("");
    let _gate = matches!(
        cmd,
        "update" | "move_by" | "insert" | "delete" | "duplicate" | "reorder" | "reparent" | "wrap" | "unwrap" | "add_layout" | "set_layout" | "remove_layout" | "align" | "paste" | "path_write"
            | "drag_begin" | "drag_end" | "undo" | "redo" | "set_variable" | "save"
    )
    .then(engine::edit_gate);
    match cmd {
        "init" => {
            let need_mcp = engine().mcp_port == 0;
            if need_mcp {
                if let Ok(port) = mcp::start() {
                    engine().mcp_port = port;
                }
            }
            let e = engine();
            Ok(json!({"mcpPort": e.mcp_port, "mcpUrl": mcp::url(e.mcp_port), "dataDir": e.data_dir, "state": state(&e),
                      "recent": e.settings.get("recent").cloned().unwrap_or(json!([])),
                      "hasApiKey": e.api_key().is_some()}))
        }
        "poll" => {
            let mut e = engine();
            e.expire_slides();
            let events = std::mem::take(&mut e.events);
            Ok(json!({"state": state(&e), "events": events}))
        }
        "state" => Ok(state(&engine())),
        "new" => {
            let mut e = engine();
            if e.agent.running || e.executing {
                return Err(engine::SWITCH_BUSY.into());
            }
            e.load_template(req["template"].as_str().unwrap_or("blank"));
            Ok(state(&e))
        }
        "open" => {
            let mut e = engine();
            if e.agent.running || e.executing {
                return Err(engine::SWITCH_BUSY.into());
            }
            e.open(std::path::Path::new(req["path"].as_str().ok_or("path required")?))?;
            Ok(state(&e))
        }
        "save" => {
            let mut e = engine();
            let path = e.save(req["path"].as_str())?;
            Ok(json!({"path": path, "state": state(&e)}))
        }
        "layers" => Ok(layers(&engine())),
        "frames" => {
            let mut e = engine();
            e.sync();
            let out: Vec<Value> = e.renderer.order.iter().filter_map(|id| e.renderer.find(id)).map(rect).collect();
            Ok(Value::Array(out))
        }
        "bounds" => {
            let mut e = engine();
            e.sync();
            let sel = ids(&req["ids"]);
            // No ids: every top-level layer, as painted.
            let b = if sel.is_empty() { editor::fit_bounds(&e) } else { editor::painted_bounds(&e, &sel) };
            Ok(b.map_or(Value::Null, |(x, y, w, h)| json!({"x": x, "y": y, "w": w, "h": h})))
        }
        "hit" => {
            let mut e = engine();
            e.sync();
            let chain = render::hit_chain(&e.renderer, req["x"].as_f64().unwrap_or(0.0), req["y"].as_f64().unwrap_or(0.0));
            Ok(json!(chain))
        }
        "boxes" => {
            let mut e = engine();
            e.sync();
            let out: Vec<Value> = ids(&req["ids"]).iter().filter_map(|id| e.renderer.find(id)).map(rect).collect();
            Ok(Value::Array(out))
        }
        "children_boxes" => {
            let mut e = engine();
            e.sync();
            let parent = req["id"].as_str().unwrap_or_default().to_string();
            let out: Vec<Value> = e.renderer.flat().filter(|f| f.parent.as_deref() == Some(&parent)).map(rect).collect();
            Ok(Value::Array(out))
        }
        "node" => {
            let mut e = engine();
            let id = req["id"].as_str().ok_or("id required")?;
            let raw = e.doc.find(id).cloned();
            e.sync();
            let parent = e.doc.parent_of(id).map(|p| p.0);
            let parent_layout = parent.as_deref().and_then(|p| e.doc.find(p)).map(|p| {
                if p["type"] == "frame" { p["layout"].as_str().unwrap_or("horizontal").to_string() } else { "none".into() }
            }).unwrap_or_else(|| "none".into());
            let local = e.renderer.find(id).map(|f| {
                let ([bx, by], rotation, flip_x, flip_y) = f.local;
                json!({"bx": bx, "by": by, "rotation": rotation, "flipX": flip_x, "flipY": flip_y})
            });
            let flat = e.renderer.find(id).map(rect);
            // A component instance holds only its overrides: what it inherits, for the Inspector to show where it has none.
            let empty = serde_json::Map::new();
            let resolver = resolve::Resolver::new(&e.doc.root, &e.libs, &empty);
            let inherited = raw.as_ref().filter(|n| n["type"] == "ref").and_then(|n| resolver.inherited(n));
            let exists = raw.is_some() || resolver.has_path(id);
            Ok(json!({"node": raw, "exists": exists, "inherited": inherited, "box": flat, "local": local, "parent": parent, "parentLayout": parent_layout,
                      "variables": e.doc.root.get("variables").cloned().unwrap_or(json!({}))}))
        }
        "select" => {
            let mut e = engine();
            e.selection = ids(&req["ids"]);
            Ok(state(&e))
        }
        "update" => {
            let mut e = engine();
            let targets = ids(&req["ids"]);
            let props = req["props"].clone();
            let apply = |d: &mut doc::Doc| d.update_all(&targets.iter().map(|id| (id.as_str(), &props)).collect::<Vec<_>>());
            match req["coalesce"].as_str() {
                Some(key) => e.edit_coalesced(key, apply)?,
                None => e.edit_or_drag(apply)?,
            }
            Ok(state(&e))
        }
        "move_by" => {
            let mut e = engine();
            let (dx, dy) = (req["dx"].as_f64().unwrap_or(0.0), req["dy"].as_f64().unwrap_or(0.0));
            let targets = ids(&req["ids"]);
            e.sync();
            // A moved layer carries its descendants: one that is also listed would move twice.
            let pos: Vec<(String, f64, f64)> = e.doc.outermost(&targets).into_iter().filter_map(|id| {
                let n = e.doc.find(&id)?;
                let x = n["x"].as_f64().unwrap_or(0.0);
                let y = n["y"].as_f64().unwrap_or(0.0);
                Some((id, x, y))
            }).collect();
            let apply = |d: &mut doc::Doc| pos.iter().try_for_each(|(id, x, y)| d.update(id, &json!({"x": editor::num(x + dx), "y": editor::num(y + dy)})));
            match req["coalesce"].as_str() {
                Some(key) => e.edit_coalesced(key, apply)?,
                None => e.edit_or_drag(apply)?,
            }
            Ok(state(&e))
        }
        "insert" => {
            let mut e = engine();
            let parent = req["parent"].as_str().unwrap_or(ROOT).to_string();
            let node = req["node"].clone();
            let id = e.edit(|d| d.insert(&parent, node, None))?;
            e.selection = vec![id.clone()];
            Ok(json!({"id": id, "state": state(&e)}))
        }
        "delete" => {
            let mut e = engine();
            // A selected descendant of a selected layer goes with it; deleting it separately would fail.
            let targets = e.doc.outermost(&ids(&req["ids"]));
            e.edit_or_drag(|d| d.delete_all(&targets))?;
            Ok(state(&e))
        }
        "duplicate" => {
            let mut e = engine();
            let new_ids = editor::duplicate(&mut e, &ids(&req["ids"]), req["inPlace"] == true)?;
            e.selection = new_ids.clone();
            Ok(json!({"ids": new_ids, "state": state(&e)}))
        }
        // Stacking order (layout order inside auto layout): up/down by one, front/back.
        "reorder" => {
            let mut e = engine();
            let targets = if req["ids"].is_array() { ids(&req["ids"]) } else { ids(&req["id"]) };
            let dir = req["dir"].as_str().unwrap_or("up").to_string();
            let mut items: Vec<(String, String, usize)> = targets.iter().filter_map(|id| e.doc.parent_of(id).map(|(p, i)| (id.clone(), p, i))).collect();
            items.sort_by_key(|x| x.2);
            if dir == "up" || dir == "front" {
                items.reverse();
            }
            e.edit_or_drag(|d| {
                // Final slot of the previous layer per parent: a one-step move must not pass it (the block stays in order at either end).
                let mut last: std::collections::HashMap<&str, usize> = std::collections::HashMap::new();
                // Position within the block per parent: front/back slots count from each parent's own end.
                let mut rank: std::collections::HashMap<&str, usize> = std::collections::HashMap::new();
                for (id, parent, _) in items.iter() {
                    let k = *rank.entry(parent.as_str()).and_modify(|n| *n += 1).or_insert(0);
                    let (_, idx) = d.parent_of(id).ok_or("node not found")?;
                    let count = if parent == ROOT { d.children().len() } else { d.find(parent).and_then(|p| p["children"].as_array()).map_or(1, |c| c.len()) };
                    let to = match dir.as_str() {
                        "front" => count.saturating_sub(1 + k),
                        "back" => k,
                        "down" => idx.saturating_sub(1),
                        _ => (idx + 1).min(count - 1),
                    };
                    let to = if last.get(parent.as_str()) == Some(&to) && (dir == "up" || dir == "down") { idx } else { to };
                    last.insert(parent, to);
                    d.move_node(id, Some(parent), Some(to))?;
                }
                Ok(())
            })?;
            Ok(state(&e))
        }
        // Move under another parent keeping the canvas position; `index` among its other children.
        "reparent" => {
            let mut e = engine();
            let targets = if req["ids"].is_array() { ids(&req["ids"]) } else { ids(&req["id"]) };
            editor::reparent(&mut e, &targets, req["parent"].as_str().unwrap_or(ROOT), req["index"].as_u64().map(|i| i as usize))?;
            Ok(state(&e))
        }
        "snap_targets" => {
            let mut e = engine();
            e.sync();
            Ok(json!(editor::snap_targets(&e, &ids(&req["ids"]), rect_arg(&req["view"]))))
        }
        "nodes_in_rect" => {
            let mut e = engine();
            e.sync();
            let r = rect_arg(req).ok_or("x, y, w, h required")?;
            Ok(json!(editor::nodes_in_rect(&e, r, req["deep"] == true)))
        }
        "drop_target" => {
            let mut e = engine();
            e.sync();
            let g = |k: &str| req[k].as_f64().unwrap_or(0.0);
            Ok(editor::drop_target(&e, g("x"), g("y"), g("w"), g("h"), &ids(&req["ignore"]), req["any"] == true, req["parent"].as_str()))
        }
        "wrap" => {
            let mut e = engine();
            let id = editor::wrap(&mut e, &ids(&req["ids"]), req["kind"].as_str().unwrap_or("group"))?;
            Ok(json!({"id": id, "state": state(&e)}))
        }
        "unwrap" => {
            let mut e = engine();
            let out = editor::unwrap(&mut e, &ids(&req["ids"]))?;
            Ok(json!({"ids": out, "state": state(&e)}))
        }
        "add_layout" => {
            let mut e = engine();
            let id = editor::add_layout(&mut e, &ids(&req["ids"]))?;
            Ok(json!({"id": id, "state": state(&e)}))
        }
        "set_layout" => {
            let mut e = engine();
            editor::set_layout(&mut e, &ids(&req["ids"]), req["layout"].as_str().unwrap_or("horizontal"))?;
            Ok(state(&e))
        }
        "remove_layout" => {
            let mut e = engine();
            editor::remove_layout(&mut e, &ids(&req["ids"]))?;
            Ok(state(&e))
        }
        "align" => {
            let mut e = engine();
            editor::align(&mut e, &ids(&req["ids"]), req["dir"].as_str().unwrap_or(""))?;
            Ok(state(&e))
        }
        "copy" => Ok(editor::copy(&mut engine(), &ids(&req["ids"]))),
        "paste" => {
            let mut e = engine();
            let out = editor::paste(&mut e, req["html"].as_str().unwrap_or(""), req["text"].as_str().unwrap_or(""), &ids(&req["targets"]), rect_arg(&req["view"]))?;
            Ok(json!({"ids": out, "state": state(&e)}))
        }
        // Vector editing: a path's anchors in canvas coordinates, and writing them back (or a new path).
        "path_edit" => editor::path_edit(&mut engine(), req["id"].as_str().ok_or("id required")?),
        "path_write" => {
            let mut e = engine();
            let id = editor::path_write(&mut e, req["id"].as_str(), req["parent"].as_str().unwrap_or(ROOT), &req["contours"])?;
            Ok(json!({"id": id, "state": state(&e)}))
        }
        "drag_begin" => {
            let mut e = engine();
            e.drag_before = Some(e.doc.root.clone());
            e.drag_clean = !e.dirty();
            Ok(json!(true))
        }
        "drag_end" => {
            let mut e = engine();
            if let Some(before) = e.drag_before.take() {
                let was_clean = e.drag_clean;
                e.end_drag(before, was_clean);
            }
            Ok(state(&e))
        }
        "undo" => {
            let mut e = engine();
            e.undo();
            Ok(state(&e))
        }
        "redo" => {
            let mut e = engine();
            e.redo();
            Ok(state(&e))
        }
        "tool" => {
            let out = engine::call_tool(req["name"].as_str().unwrap_or(""), &req["args"]);
            let out = out.finish();
            Ok(json!({"text": out.text, "error": out.error, "images": out.images.len()}))
        }
        "export" => {
            let job = {
                let mut e = engine();
                let targets = ids(&req["ids"]);
                html::plan(&mut e, &targets, req["format"].as_str().unwrap_or("png"), req["path"].as_str().unwrap_or(""), &req["opts"])?
            };
            // The guard is gone: rasterizing and writing must not block the canvas.
            Ok(json!(job.run()?))
        }
        "variables" => Ok(engine().doc.variables()),
        "set_variable" => {
            let mut e = engine();
            let vars = json!({req["name"].as_str().ok_or("name required")?: req["def"].clone()});
            e.edit(|d| d.set_variables(&vars, false))?;
            Ok(state(&e))
        }
        "settings_get" => {
            let e = engine();
            let mut s = e.settings.clone();
            if s.contains_key("anthropic_api_key") {
                s.insert("anthropic_api_key".into(), json!("••••••••"));
            }
            // The backend the agent will use, so the UI never re-derives it from settings alone.
            s.insert("backend".into(), json!(e.backend()));
            Ok(Value::Object(s))
        }
        "settings_set" => {
            let key = req["key"].as_str().ok_or("key required")?;
            engine().set_setting(key, req["value"].clone())?;
            Ok(json!(true))
        }
        "agent_send" => agent::send(
            req["text"].as_str().unwrap_or("").to_string(),
            req["parallel"].as_u64().unwrap_or(1) as usize,
            req["mode"].as_str().unwrap_or("split"),
            req["designs"].as_u64().unwrap_or(1) as usize,
            req["iterate"].as_str().unwrap_or("layout"),
        )
        .map(|_| json!(true)),
        // Nodes that need animated overlays: placeholder frames (generating glow) and pending images (shimmer).
        "live" => {
            let mut e = engine();
            e.sync();
            // `clip` is the canvas box its clipping ancestors leave visible; the overlay must not paint outside it. `cuts` are
            // those ancestors that round their corners (box `x y w h` and radii `r`), each of which cuts it too. `radius` is the
            // node's own [tl, tr, br, bl] (an ellipse is round by its type).
            let out: Vec<Value> = e.renderer.live().into_iter().map(|(f, clip, cuts)| {
                let mut v = rect(f);
                v["radius"] = json!(f.radius);
                v["clip"] = clip.map_or(Value::Null, |(x, y, w, h)| json!({"x": x, "y": y, "w": w, "h": h}));
                v["cuts"] = Value::Array(cuts.into_iter().map(|((x, y, w, h), r)| json!({"x": x, "y": y, "w": w, "h": h, "r": r})).collect());
                v
            }).collect();
            Ok(Value::Array(out))
        }
        "reduce_motion" => {
            engine().reduce_motion = req["on"].as_bool().unwrap_or(false);
            Ok(json!(true))
        }
        "viewport" => {
            let mut e = engine();
            let g = |k: &str| req[k].as_f64().unwrap_or(0.0);
            e.viewport = Some((g("x"), g("y"), g("w"), g("h")));
            Ok(json!(true))
        }
        "agent_stop" => {
            agent::stop();
            Ok(json!(true))
        }
        "agent_reset" => agent::reset().map(|_| json!(true)),
        "agent_permission" => agent::answer_permission(req["id"].as_str().ok_or("id required")?, req["answer"].as_str().ok_or("answer required: allow, deny or auto")?).map(|_| json!(true)),
        "chat_list" => Ok(agent::chats()),
        "chat_load" => agent::load_chat(req["id"].as_str().ok_or("id required")?),
        "chat_delete" => agent::delete_chat(req["id"].as_str().ok_or("id required")?).map(|_| json!(true)),
        "chat_flush" => agent::shutdown().map(|_| json!(true)),
        "chat_clear" => agent::clear_chats().map(|cleared| json!(cleared)),
        "mcp_install" => {
            let port = engine().mcp_port;
            mcp::install(req["client"].as_str().unwrap_or(""), port).map(Value::String)
        }
        "png" => {
            let id = req["id"].as_str().ok_or("id required")?;
            let scale = req["scale"].as_f64().unwrap_or(1.0).clamp(0.05, 8.0);
            let tree = engine().ctx().node_tree_at(id, scale)?;
            let png = render::encode_tree(&tree, "png", scale, 0)?;
            Ok(json!(base64::Engine::encode(&base64::engine::general_purpose::STANDARD, png)))
        }
        _ => Err(format!("unknown command '{cmd}'")),
    }
}

fn to_c(v: Value) -> *mut c_char {
    CString::new(v.to_string().replace('\0', "")).unwrap_or_default().into_raw()
}

/// # Safety
/// `ptr` must point to `len` bytes of UTF-8 JSON. Free the result with `nr_free`.
#[no_mangle]
pub unsafe extern "C" fn nr_call(ptr: *const u8, len: u32) -> *mut c_char {
    let bytes = if ptr.is_null() { &[][..] } else { std::slice::from_raw_parts(ptr, len as usize) };
    let result = std::panic::catch_unwind(|| {
        let req: Value = serde_json::from_slice(bytes).map_err(|e| format!("bad request: {e}"))?;
        dispatch(&req)
    });
    to_c(match result {
        Ok(Ok(v)) => json!({"ok": true, "data": v}),
        Ok(Err(e)) => json!({"ok": false, "error": e}),
        Err(_) => json!({"ok": false, "error": "internal error (panic)"}),
    })
}

/// # Safety
/// `p` must come from `nr_call`.
#[no_mangle]
pub unsafe extern "C" fn nr_free(p: *mut c_char) {
    if !p.is_null() {
        drop(CString::from_raw(p));
    }
}

/// Render the viewport (camera in document units) to RGBA. `bg` is 0xRRGGBB.
/// When `solo` is non-empty only that top-level node is drawn (presentation mode).
/// The returned buffer (w*h*4 bytes) stays valid until the next call.
/// `flags` bit 0 = sharp: never answer a zoom change with the scaled cached view.
///
/// # Safety
/// `solo_ptr` must point to `solo_len` bytes of UTF-8 (or be null).
#[no_mangle]
#[allow(clippy::too_many_arguments)]
pub unsafe extern "C" fn nr_render(w: u32, h: u32, cam_x: f64, cam_y: f64, zoom: f64, bg: u32, solo_ptr: *const u8, solo_len: u32, flags: u32) -> *const u8 {
    let solo = if solo_ptr.is_null() || solo_len == 0 { None } else { std::str::from_utf8(std::slice::from_raw_parts(solo_ptr, solo_len as usize)).ok().map(str::to_string) };
    let res = std::panic::catch_unwind(|| {
        let mut e = engine();
        e.expire_slides();
        e.sync();
        let rgb = [(bg >> 16) as u8, (bg >> 8) as u8, bg as u8];
        e.renderer.render(w, h, cam_x, cam_y, zoom, rgb, solo.as_deref(), flags & 1 != 0).as_ptr()
    });
    res.unwrap_or(std::ptr::null())
}
