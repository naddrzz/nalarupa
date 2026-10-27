// FFI smoke check: `bun scripts/smoke.ts` — exercises the core without opening a window.
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { call, renderPixels } from '../src/core'
import { applyEvents, barAgents, changing, clearPresence, cursorFaded, cursorOpacity, FADE_IN, imageSrc, lastContentId, needsFrame, poke, resetChat, restoreChat, runVerb, spinning, store, takeActivity } from '../src/agents'
import { own, painted, pointIn, shimmerHalves } from '../src/geometry'
import { FRAME_LABEL_TEXT, INDICATOR_TICK_MS, ellipsisAt, ellipsisPadAt, pulseAt } from '../src/theme'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`smoke failed: ${msg}`)
}

// The pending-image shimmer's two halves sit on whole pixels and share one edge, whatever the band's position.
for (const [pos, band] of [[10.4, 100.3], [-33.5, 77], [0.5, 1], [120.49, 55.51]]) {
  const [l, r] = shimmerHalves(pos, band)
  assert([...l, ...r].every(Number.isInteger) && l[0] + l[1] === r[0], `shimmer halves meet on a whole pixel (pos ${pos}, band ${band}: ${l} | ${r})`)
}

// The agent cursor's target lies on the node as painted: a 900x30 bar turned 90° is a thin vertical bar, and
// a point picked in its unturned box would land far to the side. Flipped and turned-and-flipped nodes too.
for (const [deg, fx, fy] of [[90, false, false], [0, true, false], [0, false, true], [30, true, false], [-120, true, true]] as const) {
  const bar = { x: 300, y: 420, w: 900, h: 30, m: own(deg, fx, fy) }
  const p = painted(bar)
  for (const [u, v] of [[0, 0], [1, 1], [0.5, 0.5], [0.2, 0.9], [0.9, 0.1]]) {
    const [x, y] = pointIn(bar, u, v)
    assert(x >= p.x - 1e-6 && x <= p.x + p.w + 1e-6 && y >= p.y - 1e-6 && y <= p.y + p.h + 1e-6, `cursor target (${u}, ${v}) is on the painted bar (${deg}°, flipX ${fx}, flipY ${fy}): ${x},${y} vs ${JSON.stringify(p)}`)
  }
}
  assert(pointIn({ x: 5, y: 7, w: 10, h: 20 }, 0.5, 0.5).join() === '10,17', 'an unturned node keeps its box centre')

const init = call('init')
assert(init.mcpPort > 0, 'MCP server started')
call('new', { template: 'slides' })
const frames = call('frames')
assert(frames.length === 3, `slides template has 3 frames (got ${frames.length})`)
const px = renderPixels(320, 180, 0, 0, 1920 / 320 / 6, 0x1c1c1f)
assert(px && px.length === 320 * 180 * 4, 'render returns RGBA')
const hit = call('hit', { x: 200, y: 900 })
assert(hit[0] === 'sld01', `hit-test finds the cover slide (got ${hit})`)
// A child painted outside its unclipped parent's box is hit there.
call('tool', { name: 'execute', args: { input: 'f = Insert(document, {type:"frame", name:"Host", layout:"none", clip:false, x:5000, y:5000, width:100, height:100}); Insert(f, {type:"rectangle", name:"Overflow", x:80, y:80, width:100, height:100, fill:"#ff0000"})' } })
renderPixels(320, 180, 5000, 5000, 1, 0x1c1c1f) // the first frame starts the inserted child's slide-in clock
await Bun.sleep(700) // the inserted child slides in first
renderPixels(320, 180, 5000, 5000, 1, 0x1c1c1f)
const overflow = call('hit', { x: 5150, y: 5150 }).map((i: string) => call('layers').find((l: any) => l.id === i)?.name)
assert(overflow.join() === 'Host,Overflow', `hit finds a child outside its unclipped parent (got ${overflow})`)
call('undo')
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
// The poll can end the slide between two canvas redraws: the last slide frame is not the settled view,
// so the canvas must repaint when `animating` turns false (Canvas.tsx, the `[props.animating]` effect).
call('tool', { name: 'execute', args: { input: 'Insert("sld01", {type:"rectangle", name:"Slide", width:100, height:100, fill:"#00ff00"})' } })
// What the canvas does: redraw every ~16 ms while animating. The clock starts at the first frame, so render it
// before taking t0: a slow first render (it rebuilds the layer) must not shift the poll before the slide ends.
let lastSlide = new Uint8Array(renderPixels(320, 180, 0, 0, 1 / 6, 0x1c1c1f)!)
for (const t0 = performance.now(); performance.now() - t0 < 560; await Bun.sleep(16)) lastSlide = new Uint8Array(renderPixels(320, 180, 0, 0, 1 / 6, 0x1c1c1f)!)
await Bun.sleep(70) // the next redraw is late, the poll arrives first
assert(!call('poll').state.animating, 'poll ends the slide')
const settled = new Uint8Array(renderPixels(320, 180, 0, 0, 1 / 6, 0x1c1c1f)!)
assert(Buffer.compare(lastSlide, settled) !== 0, 'the last slide frame differs from the settled view')
call('undo')
// Present renders one frame alone (solo): the sliding node and its reflowed siblings must still show, not blank the slide.
const lit = () => {
  const p = renderPixels(480, 270, 0, 0, 0.25, 0x000000, 'sld01', true)!
  let n = 0
  for (let i = 0; i < p.length; i += 4) if (p[i] > 120) n++
  return n
}
const greens = () => {
  const p = renderPixels(480, 270, 0, 0, 0.25, 0x000000, 'sld01', true)!
  let n = 0
  for (let i = 0; i < p.length; i += 4) if (p[i + 1] > 180 && p[i] < 100 && p[i + 2] < 130) n++
  return n
}
const before = lit()
const greenBefore = greens()
assert(before > 0, 'presented slide shows its content')
call('tool', { name: 'execute', args: { input: 'Insert("sld01", {type:"rectangle", name:"BigGreen", x:100, y:100, width:900, height:500, fill:"#22ff55"})' } })
assert(call('state').animating, 'insert into a presented slide is animating')
assert(lit() > 0, 'presented slide keeps its content while the insert glides in')
await Bun.sleep(700)
renderPixels(480, 270, 0, 0, 0.25, 0x000000, 'sld01', true)
assert(!call('state').animating, 'presented slide settled after ~0.6 s')
assert(lit() >= before, 'presented slide keeps its existing content')
assert(greens() > greenBefore, 'presented slide shows its inserted content')
call('undo')
// Reduced motion (an OS preference the app passes in): agent inserts appear in place.
call('reduce_motion', { on: true })
call('tool', { name: 'execute', args: { input: 'Insert("sld01", {type:"rectangle", name:"Still", width:100, height:100, fill:"#0000ff"})' } })
assert(!call('state').animating, 'no slide-in under reduced motion')
call('undo')
call('reduce_motion', { on: false })
// Constants check only (style is unreadable under automation): the frame label style Canvas.tsx applies ellipsizes instead of slicing.
assert(FRAME_LABEL_TEXT.textOverflow === 'ellipsis' && FRAME_LABEL_TEXT.overflow === 'hidden' && FRAME_LABEL_TEXT.whiteSpace === 'nowrap', 'canvas frame labels must ellipsize')

// Chat indicators sampled on the panel's tick (even with a late first tick): ellipsis steps stay near 400 ms, the pulse never jumps.
{
  const steps: number[] = []
  let last = ellipsisAt(0), since = 0, maxJump = 0
  for (let t = 7; t < 8000; t += INDICATOR_TICK_MS) {
    assert(ellipsisAt(t).length + ellipsisPadAt(t).length === 3, `ellipsis plus pad is not three dots at ${t}`)
    maxJump = Math.max(maxJump, Math.abs(pulseAt(t) - pulseAt(t - INDICATOR_TICK_MS)))
    if (ellipsisAt(t) !== last) {
      if (since) steps.push(t - since)
      since = t
      last = ellipsisAt(t)
    }
  }
  assert(steps.length > 10 && steps.every((s) => Math.abs(s - 400) <= INDICATOR_TICK_MS), `ellipsis steps ${steps}`)
  assert(maxJump < 0.3, `pulse jumps ${maxJump.toFixed(2)} between samples`)
}
// Stop drops queued prompts: the chat must not keep a bubble promising to send them.
resetChat()
applyEvents([{ type: 'agent', kind: 'queued', text: 'also add a footer' }], 0)
assert(store.items.some((i) => i.kind === 'queued'), 'queued prompt shows a bubble')
applyEvents([{ type: 'agent', kind: 'dequeued' }], 0)
assert(!store.items.some((i) => i.kind === 'queued'), 'dequeued drops the queued bubble')
assert(store.items.some((i) => i.kind === 'note' && /discarded/.test(i.text)), 'dequeued says the prompt was discarded')
// A queued bubble stays through the cook's "Iterating…" message, even when its text is a substring of it, and goes when the queue runs.
resetChat()
applyEvents([{ type: 'agent', kind: 'queued', text: 'more' }, { type: 'agent', kind: 'user', text: 'Iterating more on the design…' }], 0)
assert(store.items.some((i) => i.kind === 'queued'), 'an unrelated user message keeps the queued bubble')
applyEvents([{ type: 'agent', kind: 'queued', text: 'design' }, { type: 'agent', kind: 'user', text: 'more' }], 0)
assert(store.items.filter((i) => i.kind === 'queued').length === 2, 'a user message equal to one of several queued texts keeps the queue')
applyEvents([{ type: 'agent', kind: 'user', text: 'more\n\ndesign' }], 0)
assert(!store.items.some((i) => i.kind === 'queued'), 'the merged queue as a user message drops the bubbles')
// The typewriter keeps typing the final reply when `done` adds a usage row below it (and a note is no later message either).
resetChat()
applyEvents([{ type: 'agent', kind: 'text_delta', text: 'All done.', agent: { id: 'a0', name: 'Agent 0', color: '#ffffff' } }], 0)
const reply = store.items.find((i) => i.kind === 'assistant')!
applyEvents([{ type: 'agent', kind: 'done', agent: { id: 'a0', name: 'Agent 0', color: '#ffffff' } }], 0)
applyEvents([{ type: 'agent', kind: 'note', text: 'A note.' }], 0)
assert(store.items.some((i) => i.kind === 'usage') && store.items.some((i) => i.kind === 'note'), 'done adds a usage row, a note adds a note row')
assert(lastContentId(store.items) === reply.id, 'a usage row or a note after the reply does not end its typewriter')
applyEvents([{ type: 'agent', kind: 'message_end', agent: { id: 'a0', name: 'Agent 0', color: '#ffffff' } }, { type: 'agent', kind: 'text_delta', text: 'Next.', agent: { id: 'a0', name: 'Agent 0', color: '#ffffff' } }], 0)
assert(lastContentId(store.items) !== reply.id, 'a later reply does')
// Chat rows: only a running tool, live thinking or streaming reply is animated or changes in place; the rest of a long chat must not re-render on a tick.
resetChat()
{
  const ag = { id: 'a0', name: 'Agent 0', color: '#ffffff' }
  for (let i = 0; i < 20; i++) applyEvents([{ type: 'agent', kind: 'tool_start', id: `t${i}`, name: 'execute', agent: ag }, { type: 'agent', kind: 'tool_done', id: `t${i}`, name: 'execute', agent: ag }], 0)
  applyEvents([{ type: 'agent', kind: 'tool_start', id: 'run', name: 'execute', agent: ag }, { type: 'agent', kind: 'thinking_delta', text: 'hm', agent: { id: 'a1', name: 'Agent 1', color: '#ffffff' } }, { type: 'agent', kind: 'text_delta', text: 'Hi', agent: { id: 'a2', name: 'Agent 2', color: '#ffffff' } }], 0)
  const live = store.items.filter(changing)
  assert(live.length === 3 && live.map((i) => i.kind).join() === 'tool,thinking,assistant', `live chat rows ${live.map((i) => i.kind)}`)
  assert(store.items.filter(spinning).map((i) => i.kind).join() === 'tool,thinking', 'the ellipsis animates on the running tool and the live thinking only')
  applyEvents([{ type: 'agent', kind: 'done', agent: ag }, { type: 'agent', kind: 'done', agent: { id: 'a1', name: 'Agent 1', color: '#ffffff' } }, { type: 'agent', kind: 'done', agent: { id: 'a2', name: 'Agent 2', color: '#ffffff' } }], 0)
  assert(!store.items.some(changing), 'a finished chat has no live row')
}
// Each reply opens with the agent header on its first item, whether that is text, thinking or a tool call; later items of the reply have none.
{
  const agent = { id: 'ag1', name: 'Surya', color: '#38B2AC' }
  const ev = (kind: string, o: object = {}) => ({ type: 'agent', kind, agent, ...o })
  const end = ev('done', { durationMs: 1, usage: {}, stopped: false })
  resetChat()
  applyEvents([ev('user', { text: 'one' }), ev('start'), ev('text_delta', { text: 'Text first' }), ev('message_end'), end], 0)
  applyEvents([ev('user', { text: 'two' }), ev('start'), ev('thinking_delta', { text: 'hmm' }), ev('message_end'), ev('text_delta', { text: 'Thinking first' }), ev('message_end'), end], 0)
  applyEvents([ev('user', { text: 'three' }), ev('start'), ev('tool_start', { id: 't1', name: 'execute' }), ev('tool_done', { id: 't1', name: 'execute', input: {}, output: 'ok' }), ev('text_delta', { text: 'Tool first' }), ev('message_end'), end], 0)
  applyEvents([ev('start'), ev('text_delta', { text: 'No prompt' }), ev('message_end'), end], 0)
  applyEvents([ev('user', { text: 'four' }), ev('start'), ev('text_delta', { text: 'Cut off' }), ev('message_end'), ev('note', { text: 'The reply hit the output limit' }), ev('text_delta', { text: 'Continued' }), ev('message_end'), end], 0)
  const marks = store.items.flatMap((i) => (i.kind === 'assistant' || i.kind === 'thinking' || i.kind === 'tool' ? [`${i.kind}${i.header ? '+' : ''}`] : []))
  assert(marks.join() === 'assistant+,thinking+,assistant,tool+,assistant,assistant+,assistant+,assistant', `reply headers ${marks}`)
}
// A stopped or failed run settles its plan: unfinished steps become cancelled, none stays in progress.
{
  const agent = { id: 'ag1', name: 'Surya', color: '#38B2AC' }
  const ev = (kind: string, o: object = {}) => ({ type: 'agent', kind, agent, ...o })
  const plan = ev('todos', { todos: [{ content: 'Build hero', status: 'completed' }, { content: 'Build pricing', status: 'in_progress', activeForm: 'Building pricing' }, { content: 'Build footer', status: 'pending' }] })
  const settled = (end: object[]) => {
    resetChat()
    applyEvents([ev('user', { text: 'go' }), ev('start'), plan, ...end], 0)
    return store.todos.map((t) => t.status).join(',')
  }
  const end = (stopped: boolean) => ev('done', { durationMs: 1, usage: {}, stopped })
  assert(settled([end(true)]) === 'completed,cancelled,cancelled', 'a stopped run cancels its unfinished steps')
  assert(settled([ev('error', { text: 'Claude API error 529: Overloaded' }), end(false)]) === 'completed,cancelled,cancelled', 'a failed run cancels its unfinished steps')
  assert(settled([end(false)]) === 'completed,in_progress,pending', 'a run that ended cleanly leaves its plan alone')
  // The closing row words how the run ended: only a clean run "designed" something.
  const verb = (end: object[]) => {
    resetChat()
    applyEvents([ev('user', { text: 'go' }), ev('start'), ...end], 0)
    const u = store.items.find((i) => i.kind === 'usage')
    return u?.kind === 'usage' ? runVerb(u) : ''
  }
  assert(verb([end(false)]) === 'designed for', 'a clean run closes with "designed for"')
  assert(verb([end(true)]) === 'stopped after', 'a stopped run closes with "stopped after"')
  assert(verb([ev('error', { text: 'could not start the claude CLI' }), end(false)]) === 'failed after', 'a failed run closes with "failed after"')
  assert(verb([ev('error', { text: 'boom' }), end(true)]) === 'stopped after', 'a stopped run reads as stopped even after an error')
  // An earlier failed run does not cancel the plan of the next run.
  settled([ev('error', { text: 'boom' }), end(false)])
  applyEvents([ev('start'), plan, end(false)], 0)
  assert(store.todos.map((t) => t.status).join(',') === 'completed,in_progress,pending', 'an earlier failure does not cancel a later run')
  // A failing helper agent does not cancel the plan another agent is still working through.
  resetChat()
  const helper = { id: 'ag2', name: 'Chandra', color: '#ED8936' }
  applyEvents([ev('user', { text: 'go' }), ev('start'), plan, { type: 'agent', kind: 'start', agent: helper }, { type: 'agent', kind: 'error', text: 'boom', agent: helper }, { type: 'agent', kind: 'done', durationMs: 1, usage: {}, agent: helper }], 0)
  assert(store.todos.map((t) => t.status).join(',') === 'completed,in_progress,pending', "a helper's failure leaves the plan owner's steps alone")
}
// A chat saved mid-run (crash, kill) reopens with its unfinished plan steps cancelled, not live; a finished run's plan is left as it ended.
{
  const agent = { id: 'ag1', name: 'Surya', color: '#38B2AC' }
  const ev = (kind: string, o: object = {}) => ({ type: 'agent', kind, agent, ...o })
  const todos = [{ content: 'Build hero', status: 'in_progress', activeForm: 'Building hero' }, { content: 'Build footer', status: 'pending' }]
  const saved = [ev('user', { text: 'Design a page' }), ev('start'), ev('todos', { todos: [{ content: 'Plan', status: 'completed' }, ...todos] }), ev('message_end')]
  restoreChat(saved)
  assert(store.todos.map((t) => t.status).join(',') === 'completed,cancelled,cancelled', 'a plan whose run never finished reopens cancelled')
  restoreChat([...saved, ev('done', { durationMs: 1, usage: {} })])
  assert(store.todos.map((t) => t.status).join(',') === 'completed,in_progress,pending', 'a plan whose run finished reopens as it ended')
  restoreChat([...saved, ev('done', { stopped: true, durationMs: 1, usage: {} })])
  assert(store.todos.map((t) => t.status).join(',') === 'completed,cancelled,cancelled', 'a stopped run reopens with a cancelled plan')
  resetChat()
}
// A long chat keeps every item, the first prompt included, live and when reopened from history.
{
  const agent = { id: 'ag1', name: 'Surya', color: '#38B2AC' }
  const ev = (kind: string, o: object = {}) => ({ type: 'agent', kind, agent, ...o })
  const events: any[] = [ev('user', { text: 'Design me a pricing page' }), ev('start')]
  for (let i = 0; i < 500; i++) events.push(ev('text_delta', { text: `Step ${i}` }), ev('message_end'), ev('tool_start', { id: `t${i}`, name: 'execute' }), ev('tool_done', { id: `t${i}`, name: 'execute', input: {}, output: 'ok' }))
  events.push(ev('done', { durationMs: 1, usage: {}, stopped: false }))
  resetChat()
  for (const e of events) applyEvents([e], 0)
  assert(store.items.length === 1002 && store.items[0].kind === 'user', `a long chat is not cut live (${store.items.length} items)`)
  restoreChat(events)
  assert(store.items.length === 1002 && store.items[0].kind === 'user', `a long chat is not cut when reopened (${store.items.length} items)`)
  resetChat()
}
// Agent bar: running agents come first and survive the cap; a new conversation forgets every agent.
resetChat()
for (let i = 0; i < 10; i++) applyEvents([{ type: 'agent', kind: i < 7 ? 'done' : 'start', agent: { id: `a${i}`, name: `Agent ${i}`, color: '#ffffff' } }], 0)
const bar = barAgents(8)
assert(bar.all.length === 10 && bar.running.length === 3, 'bar counts every agent of the conversation')
assert(bar.shown.length === 8 && bar.more === 2, 'bar caps the pills and counts the rest')
assert(bar.shown.slice(0, 3).every((a) => store.running.has(a.id)), 'running agents are listed first')
resetChat()
assert(store.profiles.size === 0 && store.running.size === 0 && barAgents().shown.length === 0, 'a new conversation clears the agent bar')
assert(store.cursors.size === 0, "a new conversation drops the old agents' canvas cursors")
resetChat()
// A new or opened document drops the old one's canvas presence: cursors, flashes and pending flash requests.
{
  applyEvents([{ type: 'agent_activity', agent: 'external', ids: ['n1'], op: 'write' }, { type: 'pen', id: 'p1', x: 5, y: 5 }], 0)
  store.cursors.set('external', { profile: { id: 'external', name: 'External agent', color: '#607dff' }, x: 0, y: 0, tx: 0, ty: 0, appearAt: 0, lastActivity: 0, wobble: 0, phase: 0 })
  store.flashes.push({ nodeId: 'n1', start: 0, color: '#607dff', longHold: false })
  clearPresence()
  assert(store.cursors.size === 0 && store.flashes.length === 0 && store.activity.length === 0 && store.pens.size === 0, "a new or opened document drops the old document's cursors, pens and flashes")
  assert(!needsFrame(100, false, 0), 'no repaint is due once the presence is cleared')
}
// Activity queued while the canvas layer is not mounted (Present) is stale once the editor is back: no cursor or flash for it.
{
  applyEvents([{ type: 'agent_activity', agent: 'external', ids: ['n1'], op: 'write' }], 1000)
  applyEvents([{ type: 'agent_activity', agent: 'external', ids: ['n2'], op: 'write' }], 60000)
  assert(store.activity.length === 1 && store.activity[0].ids[0] === 'n2', 'an unread flash request is pruned once it is stale')
  assert(takeActivity(60100).length === 1 && store.activity.length === 0, 'a fresh flash request is taken and the queue emptied')
  applyEvents([{ type: 'agent_activity', agent: 'external', ids: ['n3'], op: 'write' }], 70000)
  assert(takeActivity(90000).length === 0 && store.activity.length === 0, 'a flash request that waited too long is dropped when drained')
}
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
  assert(needsFrame(8001, true, 0, 7990) && !needsFrame(8001, true, 0, 8001), 'reduced motion: a pill still on screen at its expiry gets one more repaint')
  store.pills.clear()
  store.cursors.clear()
}
// A running agent's cursor never fades off the canvas, so a long pause between edits must not respawn it.
{
  const profile = { id: 'a', name: 'A', color: '#f00' }
  const cursor = { profile, x: 0, y: 0, tx: 0, ty: 0, appearAt: 0, lastActivity: 0, wobble: 0, phase: 0 }
  store.cursors.set('a', cursor)
  store.running.add('a')
  assert(!cursorFaded('a', cursor, 9000), 'a running agent keeps its cursor after a long pause')
  poke(profile, 9000)
  assert(cursor.appearAt === 0 && cursor.lastActivity === 9000, 'poking a running agent after a long pause does not restart the fade-in')
  store.running.clear()
  assert(cursorFaded('a', cursor, 20000) && !cursorFaded('a', cursor, 10000), 'an idle agent fades off after GONE')
  poke(profile, 20000)
  assert(cursor.appearAt === 20000, 'poking a faded cursor fades it in again')
  // A new run on a cursor that faded while idle fades it in again rather than reviving it at its old spot.
  cursor.appearAt = 0
  cursor.lastActivity = 0
  applyEvents([{ type: 'agent', agent: profile, kind: 'start' }], 20000)
  assert(store.running.has('a') && cursor.appearAt === 20000, 'starting an agent with a faded cursor restarts the fade-in')
  store.running.clear()
  store.cursors.clear()
}
// A cursor that wakes while fading out continues from the opacity it reached: no one-frame jump back to full.
{
  const profile = { id: 'a', name: 'A', color: '#f00' }
  const cursor = { profile, x: 0, y: 0, tx: 0, ty: 0, appearAt: 0, lastActivity: 0, wobble: 0, phase: 0 }
  store.cursors.set('a', cursor)
  const before = cursorOpacity(cursor, 6400, false)
  assert(before > 0 && before < 0.1, 'the cursor is nearly faded out before it wakes')
  poke(profile, 6400)
  const after = cursorOpacity(cursor, 6400, true)
  assert(Math.abs(after - before) < 1e-6, 'waking a fading cursor keeps its opacity for that frame')
  assert(cursorOpacity(cursor, 6500, true) > after && cursorOpacity(cursor, 6400 + FADE_IN, true) === 1, 'a woken cursor fades back in to full within the fade-in time')
  store.cursors.clear()
}
// A reply keeps the model that wrote it: a later turn on another model does not re-label it, and a chat
// saved before replies recorded their model shows none.
{
  const profile = { id: 'm', name: 'M', color: '#0f0' }
  const turn = (model: string | null, text: string, extra: object = { backend: 'claude-code', model }) => {
    applyEvents([{ type: 'agent', agent: profile, kind: 'start', ...extra }, { type: 'agent', agent: profile, kind: 'text_delta', text }, { type: 'agent', agent: profile, kind: 'done' }], 0)
  }
  resetChat()
  turn('claude-opus-5-5', 'one')
  turn('claude-sonnet-5-5', 'two')
  turn(null, 'three')
  turn(null, 'four', {})
  const replies = store.items.filter((i) => i.kind === 'assistant')
  assert(replies[3]?.kind === 'assistant' && replies[3].by === undefined, 'a chat saved before replies recorded their model has no author')
  assert(JSON.stringify(replies.map((i) => i.kind === 'assistant' && i.by)) === JSON.stringify([
    { backend: 'claude-code', model: 'claude-opus-5-5' }, { backend: 'claude-code', model: 'claude-sonnet-5-5' }, { backend: 'claude-code', model: null }, undefined,
  ]), `each reply records its own model: ${JSON.stringify(replies)}`)
  resetChat()
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
// Nesting stops where a saved document still reopens (serde_json's recursion limit is 128 JSON levels).
call('new', { template: 'empty' })
let nest = 'document'
let levels = 0
for (; levels < 100; levels++) {
  try {
    nest = call('insert', { parent: nest, node: { type: 'frame', width: 100, height: 100, layout: 'none', children: [] } }).id
  } catch (e) {
    assert(/nesting too deep/.test((e as Error).message), `deep insert refused with a clear error: ${(e as Error).message}`)
    break
  }
}
assert(levels >= 50 && levels < 100, `nesting is capped (got ${levels} levels)`)
const deepDir = mkdtempSync(join(tmpdir(), 'nalarupa-deep-'))
try {
  call('save', { path: join(deepDir, 'deep.pen') })
  call('open', { path: join(deepDir, 'deep.pen') })
} finally {
  rmSync(deepDir, { recursive: true, force: true })
}
// Switching a clipping frame's clip off, or unwrapping it, repaints what its descendants overflow: the
// cached canvas must match a fresh render (the descendants' own signatures do not change).
const ins = (parent: string, node: any) => call('insert', { parent, node }).id as string
const redAt = (x: number, y: number) => {
  const p = renderPixels(600, 300, 0, 0, 1, 0xffffffff)!
  const o = (y * 600 + x) * 4
  return p[o] === 255 && p[o + 1] === 0 && p[o + 2] === 0
}
let rootId = ''
let midId = ''
const clipped = () => {
  call('new', { template: 'empty' })
  const root = ins('document', { type: 'frame', name: 'root', x: 0, y: 0, width: 600, height: 300, layout: 'none', fill: '#ffffff' })
  const clip = ins(root, { type: 'frame', name: 'P', layout: 'none', clip: true, x: 50, y: 50, width: 100, height: 100, fill: '#eeeeee' })
  const mid = ins(clip, { type: 'frame', name: 'C', layout: 'none', x: 10, y: 10, width: 120, height: 80, fill: '#dddddd' })
  rootId = root
  midId = mid
  ins(mid, { type: 'rectangle', name: 'R', x: 0, y: 0, width: 300, height: 50, fill: '#ff0000' })
  assert(!redAt(300, 70), 'a clipping frame cuts its overflowing descendant')
  return clip
}
call('update', { ids: [clipped()], props: { clip: false } })
assert(redAt(300, 70), 'turning clip off repaints the overflow')
call('unwrap', { ids: [clipped()] })
assert(redAt(300, 70), 'unwrapping a clipping frame repaints the overflow')
// Turning clip on, or moving a container out of a clipping frame, repaints too.
const unclipped = () => {
  const clip = clipped()
  call('update', { ids: [clip], props: { clip: false } })
  assert(redAt(300, 70), 'an unclipped frame shows its overflowing descendant')
  return clip
}
call('update', { ids: [unclipped()], props: { clip: true } })
assert(!redAt(300, 70), 'turning clip on repaints the cut overflow')
clipped()
call('reparent', { ids: [midId], parent: rootId })
assert(redAt(300, 70), 'moving a container out of a clipping frame repaints its overflow')
// An image fill over the pixel budget is refused from its header, not decoded: a 12000 x 12000 grey PNG
// (about 140 KB) took 3 s and 1.2 GB to render, a 30000 x 30000 one 19 s and 7 GB.
{
  const W = 12000
  const be = (n: number) => Buffer.from([n >>> 24, (n >>> 16) & 255, (n >>> 8) & 255, n & 255])
  const chunk = (type: string, data: Uint8Array) => {
    const body = Buffer.concat([Buffer.from(type), data])
    return Buffer.concat([be(data.length), body, be(Bun.hash.crc32(body))])
  }
  const bombDir = mkdtempSync(join(tmpdir(), 'nalarupa-bomb-'))
  try {
    const ihdr = Buffer.concat([be(W), be(W), Buffer.from([8, 0, 0, 0, 0])])
    const idat = Bun.deflateSync(Buffer.alloc(W * (W + 1)))
    const png = Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', ihdr), chunk('IDAT', idat), chunk('IEND', Buffer.alloc(0))])
    await Bun.write(join(bombDir, 'bomb.png'), png)
    call('new', { template: 'empty' })
    call('insert', { parent: 'document', node: { type: 'rectangle', name: 'bomb', x: 0, y: 0, width: 200, height: 200, fill: { type: 'image', url: join(bombDir, 'bomb.png'), mode: 'fill' } } })
    const t0 = performance.now()
    renderPixels(320, 180, 0, 0, 1, 0xffffff)
    const ms = performance.now() - t0
    assert(ms < 1500, `an oversized image fill is refused without decoding (${ms.toFixed(0)} ms)`)
  } finally {
    rmSync(bombDir, { recursive: true, force: true })
  }
}
// A shadow that moves up (a rotated, flipped node's does) must not fade along the edges the repaint is cut
// into bands at: after an edit the canvas equals a full render (solo) of the same document.
{
  call('new', { template: 'empty' })
  const root = ins('document', { type: 'frame', name: 'root', x: 0, y: 0, width: 400, height: 400, layout: 'none', fill: '#eeeeee' })
  const e = ins(root, { type: 'ellipse', name: 'E', x: 100, y: 200, width: 120, height: 80, fill: '#ff00ff80', flipY: true, rotation: 45, effect: { type: 'shadow', offset: { x: 10, y: 10 }, blur: 4, color: '#00000080' } })
  const frame = (solo?: string) => new Uint8Array(renderPixels(400, 400, 0, 0, 1, 0xffffff, solo)!) // copy: the core reuses its buffer
  frame()
  call('move_by', { ids: [e], dx: 1, dy: 0 })
  call('move_by', { ids: [e], dx: -1, dy: 0 })
  const live = frame()
  const full = frame(root)
  let worst = 0
  for (let i = 0; i < live.length; i++) worst = Math.max(worst, Math.abs(live[i] - full[i]))
  assert(worst <= 1, `an edited canvas matches a full render around a rotated, flipped shadow (largest difference ${worst})`)
}
// One very long line (pasted minified JSON, a base64 blob) lays out in linear time: quadruple the text,
// about quadruple the time (it was ~14x, 160k characters took 9 s).
const layoutMs = (chars: number) => {
  call('new', { template: 'empty' })
  call('paste', { html: '', text: 'lorem ipsum '.repeat(chars / 12), targets: [] })
  const t0 = performance.now()
  call('frames')
  return performance.now() - t0
}
layoutMs(10000) // warm the font and shaping caches
const [short, long] = [layoutMs(40000), layoutMs(160000)]
assert(long < short * 9 && long < 4000, `a long single line lays out in linear time (40k chars ${short.toFixed(0)} ms, 160k ${long.toFixed(0)} ms)`)
console.log('smoke ok — MCP at', init.mcpUrl)
process.exit(0)
