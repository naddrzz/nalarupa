// Script nodes through the core (no window): `bun scripts/scriptnode-check.ts`
// A `script` node runs the .js file named by `scriptUri` and shows what it returns as generated children: they
// follow the node's inputs and size, are never saved, and a script that cannot run draws its error in the node
// instead of breaking the document. (`script-check` is about the agents' `execute` API, not these nodes.)
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { call } from '../src/core'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`scriptnode-check failed: ${msg}`)
}
// The core reads NALARUPA_DATA through libc, which `process.env` writes don't reach: rerun in a child
// with a fresh data folder so the documents saved here never touch the real ones.
if (!process.env.NALARUPA_CHECK_CHILD) {
  const data = mkdtempSync(join(tmpdir(), 'nalarupa-scriptnode-'))
  const run = Bun.spawnSync([process.execPath, import.meta.path], { env: { ...process.env, NALARUPA_DATA: data, NALARUPA_CHECK_CHILD: '1' }, stdio: ['inherit', 'inherit', 'inherit'] })
  rmSync(data, { recursive: true, force: true })
  process.exit(run.exitCode ?? 1)
}
const dir = process.env.NALARUPA_DATA!
const file = (name: string, src: string) => writeFileSync(join(dir, name), src)
type Box = { id: string; x: number; y: number; w: number; h: number }
const kids = (id: string) => call<Box[]>('children_boxes', { id })
const shows = (id: string) => kids(id).length
const update = (id: string, props: object) => call('update', { ids: [id], props })
// Scripts run in the background (the engine lock is never held while one runs): wait until they are all done and
// the layers are rebuilt, as the canvas does (it renders, so the core syncs, and polls while `animating`).
const frame = () => {
  call('poll')
  call('children_boxes', { id: 'page' })
}
const settle = async () => {
  const until = Date.now() + 10_000
  frame()
  while (call<{ animating?: boolean }>('state').animating) {
    assert(Date.now() < until, 'scripts finish')
    await Bun.sleep(20)
    frame()
  }
  frame()
}

// Bars whose height is the node's height times Math.random() (seeded the same on every run); `count` is clamped to 1..5.
file('bars.js', '/** @schema 2.19\n * @input count: number(min=1, max=5) = 3\n * @input mode: enum("a", "b") = "a" */\nconst out = [];\nfor (let i = 0; i < nalarupa.input.count; i++) out.push({type: "rectangle", x: i * 10, y: 0, width: 8, height: nalarupa.height * Math.random(), fill: "#ff0000"});\nreturn out;')
file('throws.js', '/** @schema 2.19 */\nconst a = 1;\nnull.boom;\nreturn [];')
file('none.js', '/** @schema 2.19 */\nconst a = 1;')
file('noschema.js', 'return [];')
file('many.js', '/** @schema 2.19 */\nreturn Array.from({length: 1001}, () => ({type: "rectangle", width: 1, height: 1}));')
const script = (id: string, x: number, scriptUri: string, extra: object = {}) => ({ type: 'script', id, x, y: 0, width: 100, height: 50, scriptUri, ...extra })
const doc = {
  version: '2.19',
  children: [{ type: 'frame', id: 'page', layout: 'none', x: 0, y: 0, width: 1400, height: 200, fill: '#ffffff', children: [
    script('def', 0, './bars.js'),
    script('big', 150, './bars.js', { inputs: { count: 9 } }),
    script('twin', 300, './bars.js'),
    script('missing', 450, './nope.js'),
    script('throws', 600, './throws.js'),
    script('none', 750, './none.js'),
    script('noschema', 900, './noschema.js'),
    script('many', 1050, './many.js'),
    script('blank', 1200, ''),
    script('badenum', 1300, './bars.js', { inputs: { mode: 'z' } }),
  ] }],
}
writeFileSync(join(dir, 'doc.pen'), JSON.stringify(doc))
call('init')
call('open', { path: join(dir, 'doc.pen') })
await settle()

// Output becomes children placed in the node's local space; the declared default applies without inputs.
const def = kids('def')
assert(def.length === 3 && def.map((b) => b.x).join() === '0,10,20' && def.every((b) => b.w === 8 && b.id.startsWith('def/')), `default input: ${JSON.stringify(def)}`)
assert(def.every((b) => b.h > 0 && b.h < 50), 'heights are random fractions of the node height')
assert(call<Box[]>('boxes', { ids: ['def'] })[0].w === 100, 'the script node keeps its own box')

// A number input is clamped to its range; Math.random is seeded the same on every run, so two nodes agree exactly.
assert(shows('big') === 5, `count 9 is clamped to the maximum 5: ${shows('big')}`)
assert(kids('twin').map((b) => b.h).join() === def.map((b) => b.h).join(), 'two nodes with one file and the same inputs draw the same bars')

// The node is hit as one: a click on a generated bar selects the script node, not the bar.
const chain = call<string[]>('hit', { x: 4, y: 5 })
assert(chain.at(-1) === 'def', `hit on a generated bar ends at the script node: ${chain}`)

// Changing the inputs or the size re-runs the script; undo goes back.
update('def', { inputs: { count: 2 } })
await settle()
assert(shows('def') === 2, `changed inputs re-run the script: ${shows('def')}`)
update('def', { height: 100 })
await settle()
assert(Math.abs(kids('def')[0].h - def[0].h * 2) < 1e-9, `a taller node scales the output: ${kids('def')[0].h} vs ${def[0].h}`)
call('undo')
call('undo')
await settle()
assert(shows('def') === 3 && Math.abs(kids('def')[0].h - def[0].h) < 1e-9, 'undo returns to the first output')

// A script that cannot run shows its error inside the node and generates nothing; the rest of the document is intact.
const out = call<string[]>('export', { ids: ['page'], format: 'svg', path: dir })[0]
const svg = readFileSync(out, 'utf8')
const cases: [string, string][] = [
  ['missing', 'Script not found'],
  ['throws', 'null.boom'],
  ['none', 'Script must return an array of nodes'],
  ['noschema', 'Missing @schema version tag'],
  ['many', 'Too many nodes'],
  ['blank', 'No script file selected'],
  ['badenum', 'Value &apos;z&apos; is not allowed for input &apos;mode&apos;'],
]
for (const [id, text] of cases) {
  assert(shows(id) === 0, `${id} generates no children`)
  assert(svg.includes(text), `${id} draws "${text}" in the export`)
}
assert(shows('def') === 3, 'a failing neighbour does not affect a working script')

// Generated children are never saved: the file keeps the script nodes without children, and reopening regenerates them.
const saved = join(dir, 'saved.pen')
call('save', { path: saved })
const page = JSON.parse(readFileSync(saved, 'utf8')).children[0]
const def2 = page.children.find((n: { id: string }) => n.id === 'def')
assert(def2.type === 'script' && !def2.children && !readFileSync(saved, 'utf8').includes('def/0'), `the saved script node has no generated children: ${JSON.stringify(def2)}`)
call('new', { template: 'empty' })
call('open', { path: saved })
await settle()
assert(shows('def') === 3 && shows('big') === 5, 'reopening regenerates the output')

// A slow script never holds the engine: polling, rendering and box queries stay fast while it runs, the node shows
// its previous output while a newer size is computed, and the newest size wins.
file('slow.js', '/** @schema 2.19 */\nconst s = Date.now(); while (Date.now() - s < 500) {}\nreturn [{type: "rectangle", x: 0, y: 0, width: nalarupa.width, height: nalarupa.height, fill: "#ff0000"}]')
const timed = (f: () => unknown) => {
  const t = Date.now()
  f()
  return Date.now() - t
}
const slow = call<{ id: string }>('insert', { parent: 'page', node: script('slow', 0, './slow.js', { y: 100 }) }).id
assert(timed(() => call('children_boxes', { id: slow })) < 300, 'a new slow script does not hold up the box query')
assert(shows(slow) === 0 && call('state').animating, 'no output yet, and the canvas is told to keep redrawing')
assert(timed(() => call('poll')) < 300 && timed(() => call('state')) < 300, 'poll stays responsive while the script runs')
await settle()
assert(shows(slow) === 1 && kids(slow)[0].w === 100, 'the finished run is shown')
for (const w of [101, 102, 103]) {
  const ms = timed(() => {
    update(slow, { width: w })
    kids(slow)
  })
  assert(ms < 300, `resizing to ${w} does not wait for the script (${ms} ms)`)
}
assert(kids(slow)[0]?.w === 100, 'the previous output stays until the new size is computed')
await settle()
assert(kids(slow)[0].w === 103, `the newest size is shown: ${kids(slow)[0].w}`)
console.log('scriptnode-check ok')
