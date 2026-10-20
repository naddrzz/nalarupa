// MCP HTTP contract (no window): `bun scripts/mcp-check.ts`
// What external agents (Claude Code / Cursor / Codex) rely on: routing, status codes, the DNS-rebinding
// guard, JSON-RPC errors and batches, per-agent tool lists, and each tool's happy path and error.
import { existsSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { call } from '../src/core'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`mcp-check failed: ${msg}`)
}
// The core reads NALARUPA_DATA through libc, which `process.env` writes don't reach: rerun in a child
// with a fresh data folder so settings and documents written here never touch the real ones.
if (!process.env.NALARUPA_CHECK_CHILD) {
  const data = mkdtempSync(join(tmpdir(), 'nalarupa-mcp-'))
  // PATH is the empty data folder: the Claude Code backend the spawn_agents check selects cannot find `claude`, so no agent reaches a model.
  const run = Bun.spawnSync([process.execPath, import.meta.path], { env: { ...process.env, NALARUPA_DATA: data, NALARUPA_CHECK_CHILD: '1', PATH: data }, stdio: ['inherit', 'inherit', 'inherit'] })
  rmSync(data, { recursive: true, force: true })
  process.exit(run.exitCode ?? 1)
}
const data = process.env.NALARUPA_DATA
const { mcpPort, mcpUrl, dataDir } = call('init')
assert(dataDir === data, `core uses this run's NALARUPA_DATA (got ${dataDir})`)
call('new', { template: 'slides' })

const post = (body: string, path = '/mcp', headers: Record<string, string> = {}) =>
  fetch(`http://127.0.0.1:${mcpPort}${path}`, { method: 'POST', headers: { 'content-type': 'application/json', ...headers }, body })
const rpc = async (msg: object, path?: string) => (await post(JSON.stringify({ jsonrpc: '2.0', ...msg }), path)).json()
const tool = async (name: string, args: object = {}) => (await rpc({ id: name, method: 'tools/call', params: { name, arguments: args } })).result
const text = (r: any) => r.content.map((c: any) => c.text ?? '').join('')

{
  // Routing and HTTP status codes.
  assert((await post('{}', '/other')).status === 404, 'unknown path is 404')
  assert((await post('{}', '/mcpx')).status === 404, '/mcpx is not /mcp')
  const get = await fetch(`http://127.0.0.1:${mcpPort}/mcp`)
  assert(get.status === 405 && get.headers.get('allow') === 'POST', `GET is 405 with Allow: POST (got ${get.status})`)
  // DNS-rebinding guard: a page on another origin must not drive the editor; local pages may.
  assert((await post('{}', '/mcp', { origin: 'https://evil.example' })).status === 403, 'foreign Origin is 403')
  for (const origin of ['http://localhost.evil.com', 'http://localhostevil.com', 'http://127.0.0.1.evil.com', 'http://127.0.0.10', 'null'])
    assert((await post('{}', '/mcp', { origin })).status === 403, `lookalike Origin ${origin} is 403`)
  assert((await post('{}', '/mcp', { origin: 'http://127.0.0.1:5173' })).status === 202, '127.0.0.1 Origin is allowed')
  assert((await post('{}', '/mcp', { origin: 'http://localhost:5173' })).status === 202, 'localhost Origin is allowed (an empty request is acknowledged with 202)')
  // JSON-RPC: notifications get 202, bad JSON and unknown methods get JSON-RPC errors (HTTP 200).
  assert((await post(JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' }))).status === 202, 'notification is 202')
  const bad = await (await post('{not json')).json()
  assert(bad.error?.code === -32700 && bad.id === null, `parse error is -32700: ${JSON.stringify(bad)}`)
  const unknown = await rpc({ id: 9, method: 'nope' })
  assert(unknown.id === 9 && unknown.error?.code === -32601, `unknown method is -32601: ${JSON.stringify(unknown)}`)
  const batch = await (await post(JSON.stringify([{ jsonrpc: '2.0', id: 1, method: 'ping' }, { jsonrpc: '2.0', method: 'notifications/x' }, { jsonrpc: '2.0', id: 2, method: 'ping' }]))).json()
  assert(Array.isArray(batch) && batch.map((m: any) => m.id).join() === '1,2', `batch answers requests only, in order: ${JSON.stringify(batch)}`)

  // A non-object `arguments` is an invalid-params error for that call only (it used to panic the handler: HTTP 500, batch lost).
  const tc = (id: number, args: unknown) => ({ jsonrpc: '2.0', id, method: 'tools/call', params: { name: 'get_editor_state', arguments: args } })
  for (const args of [[], 'str', 5, true]) {
    const r = await post(JSON.stringify(tc(1, args)))
    const j = await r.json()
    assert(r.status === 200 && j.error?.code === -32602, `arguments ${JSON.stringify(args)} is -32602 with HTTP 200 (got ${r.status} ${JSON.stringify(j)})`)
  }
  const mixedRes = await post(JSON.stringify([tc(1, {}), tc(2, 'x'), tc(3, null), tc(4, undefined)]))
  const mixed = await mixedRes.json()
  assert(mixedRes.status === 200 && mixed.length === 4 && !mixed[0].error && mixed[1].error?.code === -32602 && !mixed[2].error && !mixed[3].error, `a bad call does not take its batch down: ${JSON.stringify(mixed).slice(0, 300)}`)

  // initialize echoes the client's protocol version and names the server.
  const init = await rpc({ id: 1, method: 'initialize', params: { protocolVersion: '2025-03-26' } })
  assert(init.result.protocolVersion === '2025-03-26' && init.result.serverInfo.name === 'nalarupa' && init.result.instructions, `initialize: ${JSON.stringify(init).slice(0, 200)}`)

  // Tool lists: external agents get the five public tools; built-in agents also get the permission tool.
  const names = (r: any) => r.result.tools.map((t: any) => t.name).sort().join()
  assert(names(await rpc({ id: 1, method: 'tools/list' })) === 'execute,get_editor_state,get_style,open_document,read_guide', 'external tool list')
  assert(names(await rpc({ id: 1, method: 'tools/list' }, '/mcp/ag1')).includes('approve'), 'a /mcp/<agent-id> path serves the tool list with the permission tool')

  // Each tool: happy path and a failure that comes back as isError (not an HTTP or JSON-RPC error).
  const state = await tool('get_editor_state')
  assert(!state.isError && text(state).includes('sld01'), 'get_editor_state lists the slides')
  const guide = await tool('read_guide')
  assert(!guide.isError && /schema/.test(text(guide)), 'read_guide without a topic lists topics')
  assert(!(await tool('read_guide', { topic: 'execute' })).isError, 'read_guide execute')
  assert((await tool('read_guide', { topic: 'no-such-topic' })).isError, 'unknown guide topic is an error')
  assert(!(await tool('get_style')).isError, 'get_style without a name lists styles')
  assert((await tool('get_style', { name: 'no-such-style' })).isError, 'unknown style is an error')
  assert(!call<any>('state').dirty, 'a fresh document is clean')
  assert((await tool('execute', { input: 'Insert("missing-parent", {type:"rectangle"})' })).isError && !call<any>('state').dirty, 'a rolled-back execute leaves a clean document clean')
  const ok = await tool('execute', { input: 'Insert("sld01", {type:"rectangle", name:"McpRect", width:10, height:10})' })
  assert(!ok.isError && text(ok).includes('McpRect'), `execute inserts: ${text(ok)}`)
  // The first insert succeeds before the second throws: the whole call must roll back. The layer list
  // names every node (get_editor_state does not), so it shows a leftover rectangle.
  const before = JSON.stringify(call('layers'))
  assert(before.includes('McpRect'), 'the layer list names inserted nodes')
  const broken = await tool('execute', { input: 'Insert("sld01", {type:"rectangle", name:"RolledBack"}); Insert("missing-parent", {type:"rectangle"})' })
  assert(broken.isError, 'execute against a missing parent is an error')
  assert(JSON.stringify(call('layers')) === before, 'a failed execute rolls back the edits it made')
  const badKid = await tool('execute', { input: 'Insert("sld01", {type:"frame", width:10, height:10, children:["str"]})' })
  assert(badKid.isError && text(badKid).includes('children[0]') && text(badKid).includes('editId'), `a non-object child is a repairable error: ${text(badKid)}`)
  assert(JSON.stringify(call('layers')) === before, 'a bad child rolls back')
  assert((await tool('no_such_tool')).isError, 'unknown tool is an error')
  assert((await tool('open_document')).isError, 'open_document needs a path')
  // The McpRect edit left the document dirty: a document switch must refuse, not drop it.
  const doc = join(data, 'created.pen')
  const unsaved = await tool('open_document', { path: doc })
  assert(unsaved.isError && /unsaved changes/.test(text(unsaved)) && !existsSync(doc), `open_document refuses a dirty document: ${text(unsaved)}`)
  assert(call<any>('state').dirty && JSON.stringify(call('layers')) === before, 'a refused open_document keeps the unsaved edits')
  call('save', { path: join(data, 'saved.pen') })
  assert(!(await tool('open_document', { path: doc })).isError && existsSync(doc), 'open_document creates a missing file')
  assert(!(await tool('open_document', { path: doc })).isError, 'open_document reopens it')
  assert((await tool('spawn_agents', { agents: [] })).isError, 'external agents cannot spawn agents')

  // Each /mcp/<agent-id> keeps its own execute globals: B reusing `page` must not redirect A.
  const as = async (agent: string, input: string) => {
    const r = (await rpc({ id: agent, method: 'tools/call', params: { name: 'execute', arguments: { input } } }, `/mcp/${agent}`)).result
    assert(!r.isError, `${agent} execute failed: ${text(r)}`)
    return text(r)
  }
  await as('agA', 'page = Insert(document, {type:"frame", name:"A page", width:400, height:300})')
  await as('agB', 'page = Insert(document, {type:"frame", name:"B page", x:500, width:400, height:300})')
  await as('agA', 'Insert(page, {type:"text", name:"A text", content:"from A", fill:"#000"})')
  const layers = (call('layers') as any[]).map((x) => '  '.repeat(x.depth) + x.name).join('\n')
  assert(/A page\n {2}A text/.test(layers) && !/B page\n {2}A text/.test(layers), `A's text lands in A's frame:\n${layers}`)
  assert((await as('agC', 'Print(typeof page)')).includes('undefined'), 'another agent does not inherit A/B globals')

  // spawn_agents cuts the container JSON at 12000 bytes: that must not panic inside a multi-byte character
  // (a panic drops the connection, so the call would get no JSON-RPC reply at all).
  call('settings_set', { key: 'agent_backend', value: 'claude-code' })
  const jp = '日本語のテキスト'.repeat(10)
  await as('agD', `box = Insert(document, {type:"frame", name:"Box", x:900, width:600, height:400, layout:"vertical"}); for (let i = 0; i < 61; i++) Insert(box, {type:"text", content:"${jp}", fontSize:12, fill:"#111"}); Print(box.id)`)
  const box = (call('layers') as any[]).find((x) => x.name === 'Box').id
  const spawned = await rpc({ id: 'sp', method: 'tools/call', params: { name: 'spawn_agents', arguments: { config: [{ prompt: 'x', containerNodes: [box] }] } } }, '/mcp/agD')
  assert(!spawned.result?.isError && text(spawned.result).includes('Started 1 designers'), `spawn_agents survives a container cut inside a multi-byte char: ${JSON.stringify(spawned).slice(0, 200)}`)

  // A script that runs for seconds must not freeze the window: `call` is synchronous on this one thread,
  // so a core call that waits for the script (it ran under the engine lock) blocks for its whole run.
  const slow = tool('execute', { input: 'const until = Date.now() + 2000; while (Date.now() < until) {} Print("done")' })
  await new Promise((r) => setTimeout(r, 400))
  const asked = performance.now()
  call('state')
  const waited = performance.now() - asked
  assert(waited < 1000, `state() answers while a script runs (waited ${waited.toFixed(0)} ms)`)
  assert(text(await slow).includes('done'), 'the long script still finishes and prints')

  console.log('mcp-check ok — MCP at', mcpUrl)
}
process.exit(0)
