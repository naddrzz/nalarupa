// What the agent is told about the canvas the user is looking at, through the core (no window): `bun scripts/viewport-check.ts`
// The canvas reports its camera with `viewport`; get_editor_state then marks each top-level node as
// visible or outside it and lists the visible ones first, so a long document still starts with what is on screen.
import { call } from '../src/core'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`viewport-check failed: ${msg}`)
}
const state = () => {
  const out = call('tool', { name: 'get_editor_state', args: {} })
  assert(!out.error, `get_editor_state failed: ${out.text}`)
  return out.text as string
}
const lines = (text: string) => text.split('\n').filter((l) => /^ {2}- `/.test(l))
const mark = (text: string, name: string) => lines(text).find((l) => l.includes(`"${name}"`))!
const order = (text: string) => lines(text).map((l) => l.match(/"([^"]+)"/)![1])

call('init')
call('new', { template: 'empty' })
const at = (name: string, x: number, y: number) =>
  call('insert', { parent: 'document', node: { type: 'frame', name, x, y, width: 200, height: 100, layout: 'none', fill: '#ffffff' } }).id
const far = at('Far', 5000, 5000)
const left = at('Left', 0, 0)
const right = at('Right', 400, 0)
at('Edge', 590, 0)
at('Gap', 800, 0)
at('Below', 100, 500)

// Before the canvas reports a camera nothing is marked and the order is the document's.
let text = state()
assert(!/\[user visible\]|\[outside viewport\]/.test(text), `no viewport, no markers:\n${text}`)
assert(order(text).join() === 'Far,Left,Right,Edge,Gap,Below', `document order without a viewport: ${order(text)}`)

// A camera over x 0..600, y 0..400: Left, Right and the overlapping Edge are visible, in document order, first.
// Below overlaps it in x but sits under y 400, so it is outside.
call('viewport', { x: 0, y: 0, w: 600, h: 400 })
text = state()
for (const name of ['Left', 'Right', 'Edge']) assert(mark(text, name).endsWith('[user visible]'), `${name} is visible: ${mark(text, name)}`)
for (const name of ['Far', 'Gap', 'Below']) assert(mark(text, name).endsWith('[outside viewport]'), `${name} is outside: ${mark(text, name)}`)
assert(order(text).join() === 'Left,Right,Edge,Far,Gap,Below', `visible nodes first: ${order(text)}`)
assert(mark(text, 'Left').includes(`\`${left}\``) && mark(text, 'Far').includes(`\`${far}\``), 'rows still carry the node ids')

// Touching only at an edge is not visible: with the camera over x 200..800, Left ends at 200 and Gap starts at 800.
call('viewport', { x: 200, y: 0, w: 600, h: 400 })
text = state()
assert(mark(text, 'Gap').endsWith('[outside viewport]') && mark(text, 'Left').endsWith('[outside viewport]'), 'a node touching the camera edge is outside')
assert(mark(text, 'Below').endsWith('[outside viewport]'), 'a node under the camera is outside even where it overlaps in x')
assert(mark(text, 'Edge').endsWith('[user visible]') && mark(text, 'Right').endsWith('[user visible]'), 'nodes overlapping the camera are visible')

// Panning to a far corner flips the order.
call('viewport', { x: 4900, y: 4900, w: 600, h: 400 })
text = state()
assert(order(text)[0] === 'Far' && mark(text, 'Far').endsWith('[user visible]'), `the far frame leads after panning: ${order(text)}`)
assert(order(text).slice(1).join() === 'Left,Right,Edge,Gap,Below', `the rest keep document order: ${order(text)}`)
assert(!mark(text, 'Right').includes('[user visible]'), 'Right is no longer visible')

// The summary also names the selection and counts every top-level node.
call('select', { ids: [right] })
assert(state().includes(`- Selection: \`${right}\` "Right" frame 200x100`), `selection summary: ${state()}`)
call('select', { ids: [] })
assert(state().includes('- Selection: nothing'), 'an empty selection says nothing')
assert(state().includes('- Top-level nodes (6):'), 'the count covers every top-level node')

console.log('viewport-check ok')
process.exit(0)
