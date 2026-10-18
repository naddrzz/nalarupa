// FFI smoke check: `bun scripts/smoke.ts` — exercises the core without opening a window.
import { call, renderPixels } from '../src/core'
import { applyEvents, imageSrc, needsFrame, resetChat, store } from '../src/agents'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`smoke failed: ${msg}`)
}

const init = call('init')
assert(init.mcpPort > 0, 'MCP server started')
call('new', { template: 'slides' })
const frames = call('frames')
assert(frames.length === 3, `slides template has 3 frames (got ${frames.length})`)
const px = renderPixels(320, 180, 0, 0, 1920 / 320 / 6, 0x1c1c1f)
assert(px && px.length === 320 * 180 * 4, 'render returns RGBA')
const hit = call('hit', { x: 200, y: 900 })
assert(hit[0] === 'sld01', `hit-test finds the cover slide (got ${hit})`)
const out = call('tool', { name: 'execute', args: { input: 'x = Insert(document, {type:"frame", name:"Smoke", width:100, height:100, fill:"#ff0000", x:0, y:2000})' } })
assert(!out.error && out.text.includes('"Smoke"'), `execute works: ${out.text}`)
call('undo')
assert(call('frames').length === 3, 'undo removes it')
// An agent Insert below the top level slides in: painted per frame, then settles back into the cached view.
call('tool', { name: 'execute', args: { input: 'Insert("sld01", {type:"rectangle", name:"Slide", width:100, height:100, fill:"#00ff00"})' } })
assert(call('state').animating, 'inserted child is animating')
renderPixels(320, 180, 0, 0, 1920 / 320 / 6, 0x1c1c1f)
await Bun.sleep(700)
renderPixels(320, 180, 0, 0, 1920 / 320 / 6, 0x1c1c1f)
assert(!call('state').animating, 'slide settled after ~0.6 s')
call('undo')
// Reduced motion (an OS preference the app passes in): agent inserts appear in place.
call('reduce_motion', { on: true })
call('tool', { name: 'execute', args: { input: 'Insert("sld01", {type:"rectangle", name:"Still", width:100, height:100, fill:"#0000ff"})' } })
assert(!call('state').animating, 'no slide-in under reduced motion')
call('undo')
call('reduce_motion', { on: false })
// Stop drops queued prompts: the chat must not keep a bubble promising to send them.
resetChat()
applyEvents([{ type: 'agent', kind: 'queued', text: 'also add a footer' }], 0)
assert(store.items.some((i) => i.kind === 'queued'), 'queued prompt shows a bubble')
applyEvents([{ type: 'agent', kind: 'dequeued' }], 0)
assert(!store.items.some((i) => i.kind === 'queued'), 'dequeued drops the queued bubble')
assert(store.items.some((i) => i.kind === 'note' && /discarded/.test(i.text)), 'dequeued says the prompt was discarded')
resetChat()
// Reduced motion: a running agent repaints the canvas layer only for fades, not every frame of the run.
{
  const cursor = { profile: { id: 'a', name: 'A', color: '#f00' }, x: 0, y: 0, tx: 0, ty: 0, appearAt: 0, lastActivity: 0, wobble: 0, phase: 0 }
  store.running.add('a')
  store.cursors.set('a', cursor)
  assert(needsFrame(100, true, 0), 'reduced motion: a cursor fading in repaints')
  assert(!needsFrame(1000, true, 0) && needsFrame(1000, false, 0), 'reduced motion: a settled running cursor is idle, normal motion animates it')
  assert(!needsFrame(1000, true, 3), 'reduced motion: generating placeholders are static')
  store.pens.set('p', { x: 0, y: 0, tx: 0, ty: 0, appearAt: 0 })
  assert(!needsFrame(1000, true, 0), 'reduced motion: a settled AI Pen is idle')
  store.pens.clear()
  store.running.clear()
  cursor.lastActivity = 0
  assert(!needsFrame(3000, true, 0) && needsFrame(6000, true, 0) && !needsFrame(7000, true, 0), 'reduced motion: a finished cursor repaints only while it fades out')
  store.pills.set('a', { text: 'hi', at: 5000 })
  assert(needsFrame(6000, true, 0) && !needsFrame(8500, true, 0), 'reduced motion: a pill repaints until it expires')
  store.pills.clear()
  store.cursors.clear()
}
const mcp = await fetch(`http://127.0.0.1:${init.mcpPort}/mcp`, {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/list' }),
}).then((r) => r.json())
assert(mcp.result.tools.some((t: { name: string }) => t.name === 'execute'), 'MCP lists execute')
assert(!mcp.result.tools.some((t: { name: string }) => t.name === 'approve'), 'external agents get no permission tool')
// Screenshots rasterize after the engine lock is released (ToolOut::finish) and still reach the agent.
const shot = await fetch(`http://127.0.0.1:${init.mcpPort}/mcp`, {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'execute', arguments: { input: 'TakeScreenshot("sld01")' } } }),
}).then((r) => r.json())
const png = shot.result.content.find((c: { type: string }) => c.type === 'image')
assert(png && Buffer.from(png.data, 'base64').subarray(0, 4).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47])), `MCP execute returns its screenshot as PNG: ${JSON.stringify(shot).slice(0, 300)}`)
// Claude Code re-encodes big tool images as JPEG: chat <img>s must say so or GPUIX refuses them.
assert(imageSrc('/9j/4AAQ').startsWith('data:image/jpeg;') && imageSrc('iVBORw0KGgo').startsWith('data:image/png;'), 'tool image MIME is sniffed')
console.log('smoke ok — MCP at', init.mcpUrl)
process.exit(0)
