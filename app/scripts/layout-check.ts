// Auto layout through the core (no window): `bun scripts/layout-check.ts`
// Rows and columns re-flow when the document is edited, not only when it is loaded: gap, padding, alignment,
// fill_container and fit_content react to update / insert / delete / reorder / set_layout, undo restores the
// arrangement, and a saved document lays out the same way after reopening.
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { call } from '../src/core'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`layout-check failed: ${msg}`)
}
// The core reads NALARUPA_DATA through libc, which `process.env` writes don't reach: rerun in a child
// with a fresh data folder so the documents saved here never touch the real ones.
if (!process.env.NALARUPA_CHECK_CHILD) {
  const data = mkdtempSync(join(tmpdir(), 'nalarupa-layout-'))
  const run = Bun.spawnSync([process.execPath, import.meta.path], { env: { ...process.env, NALARUPA_DATA: data, NALARUPA_CHECK_CHILD: '1' }, stdio: ['inherit', 'inherit', 'inherit'] })
  rmSync(data, { recursive: true, force: true })
  process.exit(run.exitCode ?? 1)
}
const data = process.env.NALARUPA_DATA!
type Box = { id: string; x: number; y: number; w: number; h: number }
const box = (id: string): Box => call<Box[]>('boxes', { ids: [id] })[0]
const spans = (ids: string[]) => ids.map((id) => box(id)).map((b) => `${b.x},${b.y} ${b.w}x${b.h}`).join(' | ')
const insert = (parent: string, node: object) => call('insert', { parent, node }).id as string
const update = (id: string, props: object) => call('update', { ids: [id], props })
const rect = (w: number | string, h: number | string) => ({ type: 'rectangle', width: w, height: h, fill: '#ff0000' })

call('init')
call('new', { template: 'empty' })

// A 400x100 row: padding [vertical, horizontal], gap, centred on both axes.
const row = insert('document', { type: 'frame', name: 'Row', x: 10, y: 20, width: 400, height: 100, layout: 'horizontal', gap: 10, padding: [5, 20], fill: '#ffffff' })
const a = insert(row, rect(50, 30))
const b = insert(row, rect(70, 60))
assert(spans([a, b]) === '30,25 50x30 | 90,25 70x60', `start-aligned row honours padding and gap: ${spans([a, b])}`)
update(row, { justifyContent: 'center', alignItems: 'center' })
assert(spans([a, b]) === '145,55 50x30 | 205,40 70x60', `centred row: ${spans([a, b])}`)
update(row, { justifyContent: 'end', alignItems: 'end' })
assert(box(b).x + box(b).w === 10 + 400 - 20 && box(b).y + box(b).h === 20 + 100 - 5, `end-aligned children touch the padding edge: ${spans([a, b])}`)
update(row, { justifyContent: 'space_between', alignItems: 'start', gap: 99 })
assert(box(a).x === 30 && box(b).x + box(b).w === 390 && box(b).x - (box(a).x + box(a).w) === 400 - 40 - 120, `space_between ignores the gap: ${spans([a, b])}`)

// Editing reflows: a changed gap and a third child move their siblings; delete closes the hole; undo brings it back.
update(row, { justifyContent: 'start', gap: 20 })
assert(box(b).x === 30 + 50 + 20, `a new gap moves the next child: ${box(b).x}`)
const c = insert(row, rect(40, 10))
assert(box(c).x === 30 + 50 + 20 + 70 + 20, `an inserted child lands after its siblings: ${box(c).x}`)
call('reorder', { ids: [c], dir: 'back' })
assert(box(c).x === 30 && box(a).x === 30 + 40 + 20, `reorder back puts the child first in the row: c ${box(c).x}, a ${box(a).x}`)
call('delete', { ids: [c] })
assert(box(a).x === 30 && box(b).x === 100, `deleting a child closes the gap: ${spans([a, b])}`)
call('undo')
assert(box(c).x === 30 && box(a).x === 90, `undo restores the deleted child and the arrangement: ${spans([c, a, b])}`)
call('undo')
call('undo')
assert(box(b).x === 30 + 50 + 20 && !call('node', { id: c }).node, `undo twice more removes the child again: ${spans([a, b])}`)

// fill_container shares the space the fixed children leave; the gap counts, padding does too.
update(a, { width: 'fill_container' })
assert(box(a).w === 400 - 40 - 70 - 20 && box(b).x + box(b).w === 390, `a fill child takes the remaining width: ${spans([a, b])}`)
const f2 = insert(row, rect('fill_container', 10))
assert(box(a).w === box(f2).w && box(a).w === (400 - 40 - 70 - 40) / 2, `two fill children split evenly: ${box(a).w} / ${box(f2).w}`)
call('delete', { ids: [f2] })

// A vertical column that hugs its content: fit_content grows with padding, gap and every child.
const col = insert('document', { type: 'frame', name: 'Col', x: 500, y: 0, layout: 'vertical', gap: 8, padding: 12, fill: '#eeeeee' })
const c1 = insert(col, rect(100, 40))
insert(col, rect(60, 20))
assert(box(col).w === 124 && box(col).h === 12 * 2 + 40 + 8 + 20, `a fit_content column hugs its children: ${box(col).w}x${box(col).h}`)
update(c1, { height: 100 })
assert(box(col).h === 12 * 2 + 100 + 8 + 20, `the column grows when a child grows: ${box(col).h}`)
update(col, { alignItems: 'center' })
assert(box(c1).x === 512 && call<Box[]>('children_boxes', { id: col })[1].x === 512 + (100 - 60) / 2, `alignItems centres the narrower child across the column: ${spans([c1])}`)
const abs = insert(col, { ...rect(5, 5), layoutPosition: 'absolute', x: 3, y: 4 })
assert(box(col).h === 12 * 2 + 100 + 8 + 20 && box(abs).x === 503 && box(abs).y === 4, `an absolute child ignores the layout: ${spans([abs])} col ${box(col).h}`)

// set_layout flips the direction; layout "none" leaves children where x/y put them.
call('set_layout', { ids: [row], layout: 'vertical' })
assert(box(a).x === 30 && box(b).y > box(a).y, `set_layout vertical stacks the row's children: ${spans([a, b])}`)
call('undo')
assert(box(b).x > box(a).x && box(a).y === box(b).y, 'undo of set_layout restores the row')
const free = insert('document', { type: 'frame', name: 'Free', x: 0, y: 300, width: 200, height: 100, layout: 'none' })
const fk = insert(free, { ...rect(20, 20), x: 33, y: 44 })
assert(box(fk).x === 33 && box(fk).y === 344, `layout none keeps x/y relative to the frame: ${spans([fk])}`)

// A fit_content frame whose children are all fill_container has nothing to size from: it keeps its fallback size.
const lone = insert('document', { type: 'frame', name: 'Lone', x: 0, y: 500, width: 'fit_content(30)', height: 'fit_content(20)' })
const lk = insert(lone, rect('fill_container', 'fill_container'))
assert(box(lone).w === 30 && box(lone).h === 20 && box(lk).w === 30, `fit_content falls back when only fill children remain: ${box(lone).w}x${box(lone).h}`)

// Errors: an unknown node and a layout command with nothing selected are errors, not silent no-ops.
let failed = ''
try {
  call('set_layout', { ids: ['no-such-node'], layout: 'vertical' })
} catch (e) {
  failed = (e as Error).message
}
assert(failed, 'set_layout on a missing node throws')

// Save → open keeps the arrangement.
const before = spans([a, b, c1, abs])
const path = join(data, 'layout.pen')
call('save', { path })
call('new', { template: 'empty' })
call('open', { path })
assert(spans([a, b, c1, abs]) === before, `the layout is identical after reopening: ${spans([a, b, c1, abs])} vs ${before}`)
console.log('layout-check ok')
