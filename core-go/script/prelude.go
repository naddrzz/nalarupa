package script

// The JavaScript every execute sandbox starts with (script.rs PRELUDE and EXPORT_GLOBALS, byte for byte with LF
// line endings). The prelude is the agent's API: each call JSON-encodes its objects and hands strings to a native
// (natives.go). `__base` records the globals that exist before the agent's code runs, so only the agent's own carry
// over to its later calls.

// prelude defines the agent API, the rejection tracker and `__base`.
const prelude = `
var document = "document";
var __j = (v) => v === undefined ? "null" : JSON.stringify(v);
function Insert(parent, node) { return __I(String(parent), __j(node)); }
function Copy(path, parent, data) { return __C(String(path), String(parent), __j(data)); }
function Update(path, data) { __U(String(path), __j(data)); }
function Replace(path, node) { return __R(String(path), __j(node)); }
function Move(path, parent, index) { __M(String(path), parent == null ? "" : String(parent), index == null ? -1 : Number(index)); }
function Delete(path) { __D(String(path)); }
function SetVariables(v, replace) { __SV(__j(v), !!replace); }
function GetVariables() { return JSON.parse(__GV()); }
function Print(...a) { __P(a.map(x => typeof x === "string" ? x : JSON.stringify(x)).join(" ")); }
function TakeScreenshot(ids) { __T(__j(Array.isArray(ids) ? ids : [ids])); }
function Export(ids, format, out, opts) { __X(__j(Array.isArray(ids) ? ids : [ids]), String(format), String(out || ""), __j(opts || {})); }
function FindEmptySpace(o) { return JSON.parse(__F(__j(o || {}))); }
function Generate(type, a, b) { const r = __GEN(String(type), String(a == null ? "" : a), String(b == null ? "" : b)); return r || undefined; }
function Get(a, b, c) {
  let path, visit, opts = {};
  for (const x of [a, b, c]) {
    if (typeof x === "string") path = x;
    else if (typeof x === "function") visit = x;
    else if (x && typeof x === "object") opts = x;
  }
  if (!visit) {
    if (!path || path === "document") throw new Error("Reading the whole document without a visitor is not allowed; pass a node id or a visitor function");
    return JSON.parse(__G(path, __j(opts)));
  }
  const tree = JSON.parse(__GT(path || "document", __j(opts)));
  const out = [];
  const walk = (e, parentCtx, depth, index) => {
    let skip = false;
    const ctx = { node: e.n, parentCtx, depth, index, bounds: e.b, problems: e.p || undefined, skipChildren() { skip = true; } };
    const r = visit(e.n, ctx);
    if (r !== undefined) out.push(r);
    if (!skip) (e.k || []).forEach((k, i) => walk(k, ctx, depth + 1, i));
  };
  tree.forEach((t, i) => walk(t, undefined, 0, i));
  return out;
}
var __rejected = new Map();
function __track(p, reason, handled) { if (handled) __rejected.delete(p); else __rejected.set(p, reason); }
function __unhandled() { for (const r of __rejected.values()) return [r]; }
var __base = new Set(Object.getOwnPropertyNames(globalThis));
`

// exportGlobals is the JSON of every global the agent set that JSON can hold (functions dropped).
const exportGlobals = `(() => {
  const out = {};
  for (const k of Object.getOwnPropertyNames(globalThis)) {
    if (__base.has(k) || k === "__base") continue;
    const v = globalThis[k];
    if (v === undefined || typeof v === "function") continue;
    try { out[k] = JSON.parse(JSON.stringify(v)); } catch (e) {}
  }
  return JSON.stringify(out);
})()`
