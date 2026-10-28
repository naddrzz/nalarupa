// Core checks for the canvas editing commands (no window): `bun scripts/editor-check.ts`
import { call, renderPixels } from '../src/core'
import { inParent } from '../src/geometry'
import { ICONS, nodeIcon } from '../src/icons'
import { defaultSavePath } from '../src/paths'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { effectOf } from '../src/Inspector'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`editor-check failed: ${msg}`)
}
// The core reads NALARUPA_DATA through libc, which `process.env` writes don't reach: rerun in a child
// with a fresh data folder so the saves below don't land in the real Recent list.
if (!process.env.NALARUPA_CHECK_CHILD) {
  const data = mkdtempSync(join(tmpdir(), 'nalarupa-editor-'))
  let exitCode = 1
  try {
    const run = Bun.spawnSync([process.execPath, import.meta.path], { env: { ...process.env, NALARUPA_DATA: data, NALARUPA_CHECK_CHILD: '1' }, stdio: ['inherit', 'inherit', 'inherit'] })
    exitCode = run.exitCode ?? 1
  } finally {
    rmSync(data, { recursive: true, force: true })
  }
  process.exit(exitCode)
}
type B = { id: string; x: number; y: number; w: number; h: number; parent?: string | null }
const box = (id: string): B => call<B[]>('boxes', { ids: [id] })[0]
const same = (a: B, b: B) => Math.abs(a.x - b.x) < 0.01 && Math.abs(a.y - b.y) < 0.01 && Math.abs(a.w - b.w) < 0.01 && Math.abs(a.h - b.h) < 0.01
const node = (id: string) => call('node', { id })

call('init')
call('new', { template: 'empty' })
const F = call('insert', { parent: 'document', node: { type: 'frame', name: 'F', x: 0, y: 0, width: 400, height: 300, layout: 'none', fill: '#fff' } }).id
const A = call('insert', { parent: F, node: { type: 'rectangle', name: 'A', x: 10, y: 10, width: 50, height: 50 } }).id
const Bid = call('insert', { parent: F, node: { type: 'rectangle', name: 'B', x: 100, y: 20, width: 50, height: 30 } }).id
const [a0, b0] = [box(A), box(Bid)]

// Marquee: inside an artboard it picks children; a rectangle around the whole frame picks the frame.
assert(call<string[]>('nodes_in_rect', { x: 0, y: 0, w: 70, h: 70 }).join() === A, 'marquee touches A only')
assert(call<string[]>('nodes_in_rect', { x: 5, y: 5, w: 100, h: 20 }).sort().join() === [A, Bid].sort().join(), 'touching counts')
assert(call<string[]>('nodes_in_rect', { x: -10, y: -10, w: 500, h: 400 }).join() === F, 'a contained frame is picked whole')

// Snap targets: parent frame + siblings.
const targets = call<B[]>('snap_targets', { ids: [A] }).map((t) => t.id).sort()
assert(targets.join() === [F, Bid].sort().join(), `snap targets ${targets}`)

// Layers list freeform children topmost first.
const rows = call<{ id: string; index: number }[]>('layers')
assert(rows[1].id === Bid && rows[2].id === A && rows[1].index === 1, 'layers are topmost first')

// Group / ungroup keep positions, one undo step each.
const G = call('wrap', { ids: [A, Bid], kind: 'group' }).id
assert(node(A).parent === G && same(box(A), a0) && same(box(Bid), b0), 'group keeps positions')
const un = call('unwrap', { ids: [G] }).ids
assert(un.length === 2 && node(A).parent === F && same(box(A), a0) && same(box(Bid), b0), 'ungroup restores')
call('undo')
assert(node(A).parent === G, 'undo ungroup')
call('undo')
assert(node(A).parent === F && !node(G).node, 'undo group')

// Frame selection.
const FR = call('wrap', { ids: [A, Bid], kind: 'frame' }).id
const fr = box(FR)
assert(fr.x === 10 && fr.y === 10 && fr.w === 140 && fr.h === 50 && same(box(A), a0), 'frame selection wraps the union')
call('undo')

// Shift+A: wrap in an auto-layout row; gap and alignment inferred, positions kept.
const L = call('add_layout', { ids: [A, Bid] }).id
const ln = node(L).node
assert(ln.layout === 'horizontal' && ln.gap === 40, `inferred row with gap 40, got ${ln.layout} ${ln.gap}`)
assert(same(box(A), a0) && box(Bid).x === b0.x, 'auto layout keeps the arrangement')
// Drop target inside the row: between A and B.
const dt = call('drop_target', { x: 80, y: 30, w: 10, h: 10, ignore: [] })
assert(dt.id === L && dt.index === 1 && dt.line[0] === 80, `drop index/line ${JSON.stringify(dt)}`)
// Reorder inside the layout, then take B out keeping its canvas position.
call('reparent', { ids: [Bid], parent: L, index: 0 })
assert(box(Bid).x === 10, 'reordered first')
const bIn = box(Bid)
call('reparent', { ids: [Bid], parent: 'document' })
assert(node(Bid).parent === 'document' && same(box(Bid), bIn), 'reparent keeps the canvas position')
call('undo')
call('undo')
// Remove the layout: children stay where the layout put them.
const before = [box(A), box(Bid)]
call('remove_layout', { ids: [L] })
assert(node(L).node.layout === 'none' && same(box(A), before[0]) && same(box(Bid), before[1]), 'remove layout keeps positions')
call('undo')
call('undo')
assert(node(A).parent === F, 'undo add_layout in one step')

// Row -> Column: the cross-axis fill (height) becomes the main axis and must not collapse the children.
const M = call('insert', { parent: 'document', node: { type: 'frame', name: 'M', x: 0, y: 700, width: 400, height: 300, layout: 'none' } }).id
const [M1, M2] = [10, 100].map((x) => call('insert', { parent: M, node: { type: 'rectangle', x, y: 10, width: 50, height: 50 } }).id)
const ML = call('add_layout', { ids: [M1, M2] }).id
assert(node(M1).node.height === 'fill_container', `row children fill the cross axis, got ${node(M1).node.height}`)
call('set_layout', { ids: [ML], layout: 'vertical' })
assert(node(ML).node.layout === 'vertical', 'switched to column')
assert([M1, M2].every((k) => box(k).h >= 49.99 && node(k).node.height === 50), `column keeps child heights, got ${box(M1).h} ${box(M2).h}`)
call('undo')
assert(node(ML).node.layout === 'horizontal' && node(M1).node.height === 'fill_container', 'undo column in one step')
const rejects = (args: Record<string, unknown>) => {
  try {
    call('set_layout', args)
  } catch {
    return true
  }
  return false
}
assert(rejects({ ids: [ML], layout: 'diagonal' }) && node(ML).node.layout === 'horizontal', 'set_layout rejects unknown directions')
assert(rejects({ ids: [M1], layout: 'vertical' }) && node(M1).node.layout === undefined, 'set_layout rejects non-layout nodes')
call('undo')
call('undo')

// Align left: B moves to A's left edge.
call('align', { ids: [A, Bid], dir: 'left' })
assert(box(Bid).x === a0.x && box(A).x === a0.x, 'align left')
call('undo')

// Align works on painted bounds: a flipped and a rotated layer line up with a plain one.
const T = call('insert', { parent: 'document', node: { type: 'frame', name: 'T', x: 600, y: 0, width: 400, height: 400, layout: 'none' } }).id
const turned = [{ x: 100, y: 50 }, { x: 200, y: 150, flipX: true }, { x: 300, y: 250, rotation: 90 }].map(
  (p) => call('insert', { parent: T, node: { type: 'rectangle', width: 40, height: 40, ...p } }).id,
)
const painted = () => call<(B & { m?: number[] })[]>('boxes', { ids: turned }).map((b) => {
  const m = b.m ?? [1, 0, 0, 1, 0, 0]
  const pts = [[0, 0], [b.w, 0], [0, b.h], [b.w, b.h]].map(([x, y]) => [m[0] * (b.x + x) + m[2] * (b.y + y) + m[4], m[1] * (b.x + x) + m[3] * (b.y + y) + m[5]])
  return { l: Math.min(...pts.map((p) => p[0])), t: Math.min(...pts.map((p) => p[1])) }
})
call('align', { ids: turned, dir: 'left' })
const ls = painted()
assert(ls.every((p) => Math.abs(p.l - ls[0].l) < 0.01), `align left of flipped/rotated ${JSON.stringify(ls)}`)
call('align', { ids: turned, dir: 'top' })
const ts = painted()
assert(ts.every((p) => Math.abs(p.t - ts[0].t) < 0.01), `align top of flipped/rotated ${JSON.stringify(ts)}`)

// An arrow-key nudge goes right on screen inside a rotated or flipped frame: the delta is turned into the parent's space for move_by.
for (const [what, turn] of [['rotated', { rotation: 90 }], ['flipped', { flipX: true }]] as const) {
  const NF = call('insert', { parent: 'document', node: { type: 'frame', name: `Nudge ${what}`, x: 1200, y: 600, width: 300, height: 200, layout: 'none', ...turn } }).id
  const NK = call('insert', { parent: NF, node: { type: 'rectangle', x: 20, y: 20, width: 60, height: 40 } }).id
  const n0 = call('bounds', { ids: [NK] })
  const [ndx, ndy] = inParent(node(NF).box.m, 10, 0)
  call('move_by', { ids: [NK], dx: ndx, dy: ndy })
  const n1 = call('bounds', { ids: [NK] })
  assert(Math.abs(n1.x - n0.x - 10) < 0.01 && Math.abs(n1.y - n0.y) < 0.01, `a nudge to the right in a ${what} frame moves the child right (${n0.x},${n0.y} → ${n1.x},${n1.y})`)
}

// One arrow press over layers in different parents is one undo step: the per-parent move_by calls share a coalesce key.
{
  const [P, Q] = [0, 1].map((i) => call('insert', { parent: 'document', node: { type: 'frame', name: `Nudge pair ${i}`, x: 1600 + i * 400, y: 600, width: 300, height: 200, layout: 'none' } }).id)
  const [PK, QK] = [P, Q].map((f) => call('insert', { parent: f, node: { type: 'rectangle', x: 20, y: 20, width: 60, height: 40 } }).id)
  const [p0, q0] = [call('bounds', { ids: [PK] }), call('bounds', { ids: [QK] })]
  call('move_by', { ids: [PK], dx: 10, dy: 0, coalesce: 'nudge:test' })
  call('move_by', { ids: [QK], dx: 10, dy: 0, coalesce: 'nudge:test' })
  call('undo')
  const [p1, q1] = [call('bounds', { ids: [PK] }), call('bounds', { ids: [QK] })]
  assert(p1.x === p0.x && q1.x === q0.x, `one undo reverts a nudge across parents (${p0.x},${q0.x} → ${p1.x},${q1.x})`)
}

// Duplicate: same position, right above the source.
const d = call('duplicate', { ids: [A] }).ids[0]
assert(same(box(d), a0) && node(d).parent === F, 'duplicate in place')
const kids = call<B[]>('children_boxes', { id: F }).map((k) => k.id)
assert(kids.indexOf(d) === kids.indexOf(A) + 1, 'duplicate sits above its source')
call('undo')

// Reorder several at once.
call('reorder', { ids: [A], dir: 'front' })
assert(call<B[]>('children_boxes', { id: F }).at(-1)!.id === A, 'bring to front')
call('undo')
{
  // One step up/down on a block that touches the end of the stack keeps the selected layers' relative order.
  const RF = call('insert', { node: { type: 'frame', name: 'RF', x: 2000, y: 0, width: 500, height: 300, layout: 'none' } }).id
  const r: Record<string, string> = {}
  for (const n of ['A', 'B', 'C', 'D']) r[n] = call('insert', { parent: RF, node: { type: 'rectangle', name: n, x: 0, y: 0, width: 10, height: 10 } }).id
  const order = () => call<B[]>('children_boxes', { id: RF }).map((k) => Object.keys(r).find((n) => r[n] === k.id)).join('')   // bottom → top
  assert(order() === 'ABCD', `reorder fixture (${order()})`)
  call('reorder', { ids: [r.C, r.D], dir: 'up' })
  assert(order() === 'ABCD', `bring forward at the front keeps the order (${order()})`)
  call('reorder', { ids: [r.A, r.B], dir: 'down' })
  assert(order() === 'ABCD', `send backward at the back keeps the order (${order()})`)
  call('reorder', { ids: [r.B, r.C], dir: 'up' })
  assert(order() === 'ADBC', `bring forward moves a free block one step (${order()})`)
  // Layers from two parents in one call: front/back count from each parent's own end.
  const RG = call('insert', { node: { type: 'frame', name: 'RG', x: 2600, y: 0, width: 500, height: 300, layout: 'none' } }).id
  const g: Record<string, string> = {}
  for (const n of ['E', 'F', 'G']) g[n] = call('insert', { parent: RG, node: { type: 'rectangle', name: n, x: 0, y: 0, width: 10, height: 10 } }).id
  const orderG = () => call<B[]>('children_boxes', { id: RG }).map((k) => Object.keys(g).find((n) => g[n] === k.id)).join('')
  call('reorder', { ids: [r.A, g.E], dir: 'front' })
  assert(order() === 'DBCA' && orderG() === 'FGE', `bring to front across parents (${order()}, ${orderG()})`)
  call('reorder', { ids: [r.A, g.E], dir: 'back' })
  assert(order() === 'ADBC' && orderG() === 'EFG', `send to back across parents (${order()}, ${orderG()})`)
}

// Clipboard: copy → paste lands in place next to the visible source; text and SVG paste.
const clip = call('copy', { ids: [A] })
assert(clip.html.includes('data-nala-node-clipboard'), 'copy writes the html marker')
const view = { x: -100, y: -100, w: 800, h: 600 }
const p = call('paste', { html: clip.html, text: clip.text, targets: [], view }).ids
assert(p.length === 1 && node(p[0]).parent === F && same(box(p[0]), a0), 'paste in place')
const into = call('paste', { html: clip.html, text: clip.text, targets: [Bid], view }).ids
assert(node(into[0]).parent === F, 'paste with a leaf selected goes into its container')
const t = call('paste', { html: '', text: 'hello', targets: [], view }).ids
assert(node(t[0]).node.content === 'hello' && node(t[0]).parent === 'document', 'plain text pastes as text')
const s = call('paste', { html: '', text: '<svg xmlns="http://www.w3.org/2000/svg" width="20" height="10"></svg>', targets: [], view }).ids
assert(box(s[0]).w === 20 && box(s[0]).h === 10, 'svg pastes at its size')
// A Figma copy (fig-kiwi archive) arrives as layers at its Figma position, its instance expanded.
const fig = await Bun.file(new URL('../../core/tests/figma-clipboard.html', import.meta.url)).text()
const card = call('paste', { html: fig, text: '', targets: [], view }).ids
const figKids = node(card[0]).node.children
assert(card.length === 1 && box(card[0]).x === 40 && figKids.map((k: any) => k.name).join() === 'Title,Body,Row', 'Figma paste')
assert(figKids[2].children[1].children[0].content === 'Buy now', 'the instance keeps its text override')

// A line (documented in the agent guide) can be inserted, created by execute and copied → pasted.
const LN = call('insert', { parent: F, node: { type: 'line', name: 'Ln', x: 20, y: 200, width: 100, height: 60, stroke: '#000000', strokeWidth: 6 } }).id
assert(node(LN).node.type === 'line', 'insert accepts a line')
const lineOut = call('tool', { name: 'execute', args: { input: 'Insert(document, {type:"line", name:"x", width:100, height:50, stroke:"#000", strokeWidth:4})' } })
assert(!lineOut.error, `execute Insert accepts a line: ${lineOut.text}`)
const lineClip = call('copy', { ids: [LN] })
const lp = call('paste', { html: lineClip.html, text: lineClip.text, targets: [], view }).ids
assert(lp.length === 1 && node(lp[0]).node.type === 'line' && same(box(lp[0]), box(LN)), 'a copied line pastes in place')

// Pen: points drawn on the canvas become a path in F; reading them back gives the same canvas points.
const drawn = [{ closed: false, anchors: [{ x: 10, y: 120 }, { x: 60, y: 120, out: [80, 140] }, { x: 60, y: 170 }] }]
const P = call('path_write', { parent: F, contours: drawn }).id
assert(node(P).parent === F && node(P).node.geometry.startsWith('M0 0 L50 0 C'), 'pen path')
const r4 = (v: unknown) => JSON.stringify(v, (_, n) => (typeof n === 'number' ? Math.round(n * 1e4) / 1e4 : n))
const back = call('path_edit', { id: P })
assert(r4(back) === r4(drawn), `path points round trip: ${JSON.stringify(back)}`)
back[0].anchors[2].x = 90
call('path_write', { id: P, contours: back })
assert(Math.abs(box(P).w - 80) < 0.01 && r4(call('path_edit', { id: P })[0].anchors[0]) === r4(drawn[0].anchors[0]), 'moving a point grows the box, the rest stays')

// Inspector gestures: same coalesce key = one undo step.
call('update', { ids: [A], props: { x: 11 }, coalesce: 'k1' })
call('update', { ids: [A], props: { x: 12 }, coalesce: 'k1' })
call('update', { ids: [A], props: { x: 13 }, coalesce: 'k1' })
call('undo')
assert(node(A).node.x === 10, 'coalesced edits undo together')

// A turned layer is selected and framed where it paints (rotation is about its own x/y): this 300×40 bar paints at x 100..140, y 0..300.
const RF = call('insert', { parent: 'document', node: { type: 'frame', name: 'RF', x: 1000, y: 0, width: 800, height: 800, layout: 'none' } }).id
const bar = call('insert', { parent: RF, node: { type: 'rectangle', name: 'bar', x: 100, y: 300, width: 300, height: 40, rotation: 90 } }).id
const marquee = (x: number, y: number, w: number, h: number) => call<string[]>('nodes_in_rect', { x, y, w, h, deep: false }).includes(bar)
assert(marquee(1105, 120, 30, 40), 'marquee over the painted bar selects it')
assert(!marquee(1300, 305, 80, 30), 'marquee over the unrotated box selects nothing')
const pb = call<B>('bounds', { ids: [bar] })
assert(same({ ...pb, id: bar }, { id: bar, x: 1100, y: 0, w: 40, h: 300 }), `bounds of a turned layer ${JSON.stringify(pb)}`)
// At 45° the strip runs from (1100, 300) up to the right: its bounds' empty corner is not a hit.
call('update', { ids: [bar], props: { rotation: 45 } })
assert(marquee(1200, 190, 20, 20), 'marquee on the 45° bar selects it')
assert(!marquee(1110, 90, 30, 30), "marquee in the empty corner of a 45° bar's bounds misses it")
// A zero-height layer paints a line: the SAT needs its normal too, or the empty corner of its bounds is a hit.
call('update', { ids: [bar], props: { height: 0 } })
assert(marquee(1200, 190, 20, 20), 'marquee on a zero-height 45° line selects it')
assert(!marquee(1110, 90, 30, 30), "marquee in the empty corner of a zero-height 45° line's bounds misses it")

// A drag that ends where it started adds no undo step, even though move_by writes 10 back as 10.0.
const D1 = call('insert', { parent: 'document', node: { type: 'rectangle', name: 'D1', x: 10, y: 10, width: 50, height: 50 } }).id
const D2 = call('insert', { parent: 'document', node: { type: 'rectangle', name: 'D2', x: 100, y: 10, width: 50, height: 50 } }).id
call('update', { ids: [D2], props: { x: 200 } })
call('drag_begin')
call('move_by', { ids: [D1], dx: 30, dy: 0 })
assert(node(D1).node.x === 40, 'move_by moves D1 during the drag')
call('move_by', { ids: [D1], dx: -30, dy: 0 })
call('drag_end')
assert(node(D1).node.x === 10, 'the drag back ends at the start')
call('undo')
assert(node(D2).node.x === 100, 'a drag back to the start leaves no undo step')
call('update', { ids: [D2], props: { x: 200 } })
call('drag_begin')
call('move_by', { ids: [D1], dx: 30, dy: 0 })
call('drag_end')
call('undo')
assert(node(D1).node.x === 10 && node(D2).node.x === 200, 'a drag that ends away from the start is one undo step')
call('undo')
call('update', { ids: [D2], props: { x: 200 } })
call('drag_begin')
call('move_by', { ids: [D1], dx: 0, dy: 0 })
call('drag_end')
call('undo')
assert(node(D2).node.x === 100, 'a zero-distance drag leaves no undo step')

// Drop targets follow the frame's painted box: a rotated row is entered where it is painted (its
// insertion line runs along the turned axis), the empty spot of the unrotated box is the document.
const R = call('insert', { parent: 'document', node: { type: 'frame', name: 'R', x: 2000, y: 2000, width: 300, height: 100, fill: '#fff', layout: 'horizontal', gap: 10, padding: 10, rotation: 90 } }).id
const rk = [0, 1, 2].map(() => call('insert', { parent: R, node: { type: 'rectangle', width: 50, height: 50 } }).id)
const centre = (id: string) => {
  const b = call('bounds', { ids: [id] })
  return [b.x + b.w / 2, b.y + b.h / 2]
}
const [c0, c1] = [centre(rk[0]), centre(rk[1])]
const [gx, gy] = [(c0[0] + c1[0]) / 2, (c0[1] + c1[1]) / 2]
const rd = call('drop_target', { x: gx, y: gy, w: 10, h: 10, ignore: [] })
assert(rd.id === R && rd.layout === 'horizontal' && rd.index === 1, `drop over a rotated row ${JSON.stringify(rd)}`)
const [lx1, ly1, lx2, ly2] = rd.line
const [dx, dy] = [lx2 - lx1, ly2 - ly1]
const along = (gx - lx1) * dx + (gy - ly1) * dy
assert(Math.abs(dx * (gy - ly1) - dy * (gx - lx1)) < 0.01 && along >= -0.01 && along <= dx * dx + dy * dy + 0.01 && Math.abs(Math.hypot(dx, dy) - 100) < 0.01, `the line crosses the turned row at the gap ${JSON.stringify(rd.line)}`)
const [e0, e1] = [centre(rk[0]), centre(rk[2])]
assert(call('drop_target', { x: e0[0] + (e0[0] - e1[0]) / 4, y: e0[1] + (e0[1] - e1[1]) / 4, w: 10, h: 10, ignore: [] }).index === 0, 'before the first child of a rotated row')
assert(call('drop_target', { x: 2250, y: 2050, w: 10, h: 10, ignore: [] }).id === 'document', 'the empty spot of the unrotated row is the document')
const free = call('insert', { parent: 'document', node: { type: 'frame', name: 'Free', x: 3000, y: 2000, width: 300, height: 200, fill: '#fff', layout: 'none', rotation: 30 } }).id
assert(call('drop_target', { x: 3150, y: 2100, w: 10, h: 10, ignore: [] }).id === free, 'drop into a rotated free frame at its centre')
assert(call('drop_target', { x: 3001, y: 2199, w: 10, h: 10, ignore: [] }).id === 'document', 'the unrotated corner of a rotated free frame is the document')

// drop_target runs on every mouse move of a drag: it must stay linear in the layer count (it used to look up every layer's type in the tree first, ~80 ms at 1,500 layers).
const BF = call('insert', { parent: 'document', node: { type: 'frame', name: 'BF', x: 5000, y: 0, width: 4000, height: 4000, layout: 'none', fill: '#fff' } }).id
for (let i = 0; i < 1500; i++) call('insert', { parent: BF, node: { type: 'rectangle', x: 5000 + (i % 50) * 70, y: Math.floor(i / 50) * 70, width: 60, height: 60, fill: '#f00' } })
call('drop_target', { x: 5500, y: 500, w: 10, h: 10, ignore: [] }) // warm-up: the first call syncs the pending document
const t0 = performance.now()
for (let i = 0; i < 5; i++) call('drop_target', { x: 5500, y: 500, w: 10, h: 10, ignore: [] })
const per = (performance.now() - t0) / 5
assert(call<{ id: string }>('drop_target', { x: 5500, y: 500, w: 10, h: 10, ignore: [] }).id === BF, 'drop_target finds the big frame among 1,500 layers')
assert(per < 25, `drop_target with 1,500 layers took ${per.toFixed(1)} ms per call`)

// The Inspector Effect field only takes a shadow color the core paints: #RGB, #RRGGBB, #RRGGBBAA (not #RGBA, which is drawn as nothing).
const shadow = (color: string) => ({ type: 'shadow', blur: 0, offset: { x: 60, y: 60 }, color })
const SH = call('insert', { parent: 'document', node: { type: 'rectangle', x: 100, y: 100, width: 100, height: 100, fill: '#ffffff' } }).id
for (const color of ['#000', '#00000077', '#ff000055']) {
  assert(effectOf(JSON.stringify(shadow(color)), {}), `the Effect field accepts the shadow color ${color}`)
  call('update', { ids: [SH], props: { effect: shadow(color) } })
  const p = renderPixels(300, 300, 0, 0, 1, 0xffffff)!
  const o = (230 * 300 + 230) * 4
  assert(p[o] + p[o + 1] + p[o + 2] < 765, `the core paints a ${color} shadow`)
}
assert(effectOf(JSON.stringify(shadow('#0007')), {}) === undefined, 'the Effect field rejects #RGBA, which the core does not paint')

// Every layer type that has its own look gets its own Layers/Inspector icon (only rectangle uses the square).
const icons = Object.fromEntries(['frame', 'group', 'rectangle', 'ellipse', 'text', 'icon', 'line', 'note', 'prompt', 'context', 'script', 'browser'].map((t) => [t, nodeIcon(t)]))
assert(new Set(Object.values(icons)).size === Object.keys(icons).length, `layer types share an icon ${JSON.stringify(icons)}`)
assert(nodeIcon('path') === nodeIcon('polygon') && nodeIcon('frame', true) === 'component', 'path/polygon and reusable icons')

// A blank or whitespace-only name is labelled like a missing one (the type, or a text layer's content), in Layers and for the frame label / Slides caption.
{
  call('new', { template: 'empty' })
  const fr = call('insert', { node: { type: 'frame', name: '   ', x: 0, y: 0, width: 200, height: 100 } }).id
  const ids = ['rectangle', 'ellipse'].map((type, i) => call('insert', { parent: fr, node: { type, name: i ? '' : '   ', x: 10, y: 10, width: 20, height: 20 } }).id)
  const tx = call('insert', { parent: fr, node: { type: 'text', name: ' ', content: 'blank name', x: 10, y: 50, fontSize: 14 } }).id
  const names = Object.fromEntries(call<{ id: string; name: string }[]>('layers').map((l) => [l.id, l.name]))
  assert(names[fr] === 'frame' && names[ids[0]] === 'rectangle' && names[ids[1]] === 'ellipse' && names[tx] === 'blank name', `blank names fall back in Layers (${JSON.stringify(names)})`)
  assert(call<{ id: string; name: string }[]>('frames').find((f) => f.id === fr)?.name === 'frame', 'a blank frame name falls back to its type for the canvas label')
}

// Save as… into a folder that already holds <title>.nala picks a free name instead of replacing it.
{
  const dir = mkdtempSync(join(tmpdir(), 'nala-saveas-'))
  try {
    const first = defaultSavePath('Untitled', dir)
    assert(first === join(dir, 'Untitled.nala'), `an empty folder gets the plain name (${first})`)
    call('new', { template: 'empty' })
    call('insert', { node: { type: 'frame', name: 'FIRST DOC' } })
    call('save', { path: first })
    const second = defaultSavePath('Untitled', dir)
    assert(second === join(dir, 'Untitled-1.nala'), `an existing document is not reused (${second})`)
    call('new', { template: 'empty' })
    call('insert', { node: { type: 'frame', name: 'SECOND DOC' } })
    call('save', { path: second })
    assert(readFileSync(first, 'utf8').includes('FIRST DOC'), 'the first document is untouched')
    assert(defaultSavePath('a/b:c', dir) === join(dir, 'abc.nala'), 'path characters are dropped from the title')
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}
// Editing many layers at once is linear in their number: select-all + group, set one property and delete
// each cost about 4x the time for 4x the layers (grouping was ~16x, 4000 layers took 4.9 s).
{
  const bulk = (n: number) => {
    call('new', { template: 'empty' })
    const kids = Array.from({ length: n }, (_, i) => ({ type: 'rectangle', name: 'r' + i, x: (i % 100) * 5, y: Math.floor(i / 100) * 5, width: 4, height: 4, fill: '#f00' }))
    const fr = call('insert', { node: { type: 'frame', name: 'bulk', layout: 'none', width: 5000, height: 5000, children: kids } }).id
    call('frames')
    const ids = call<{ id: string }[]>('layers').filter((l) => l.id !== fr).map((l) => l.id)
    const time = (fn: () => void) => {
      const t0 = performance.now()
      fn()
      return performance.now() - t0
    }
    const group = time(() => call('wrap', { ids, kind: 'group' }))
    const g = node(call<{ id: string; type: string }[]>('layers').find((l) => l.type === 'group')!.id).node
    assert(g.children.length === n && g.children[0].name === 'r0' && g.children[n - 1].name === 'r' + (n - 1), `grouping ${n} layers keeps all of them, in order`)
    call('undo')
    const update = time(() => call('update', { ids, props: { opacity: 0.5 } }))
    assert(node(ids[n - 1]).node.opacity === 0.5, `updating ${n} layers reaches the last one`)
    call('undo')
    const del = time(() => call('delete', { ids }))
    assert(call<{ id: string }[]>('layers').length === 1, `deleting ${n} layers leaves only the frame`)
    return [group, update, del]
  }
  bulk(500) // warm up
  const [short, long] = [bulk(1000), bulk(4000)]
  for (const [i, what] of ['group', 'update', 'delete'].entries())
    assert(long[i] < Math.max(short[i], 20) * 9 && long[i] < 3000, `${what} of selected layers is linear (1000 layers ${short[i].toFixed(0)} ms, 4000 ${long[i].toFixed(0)} ms)`)
}
// Fit all frames the union of what the top-level layers paint: a flipped frame (x -600..0) or a
// turned one (y -300..300) is inside it, and there is no margin on the unflipped side.
call('new', { template: 'empty' })
call('insert', { parent: 'document', node: { type: 'frame', name: 'A', x: 0, y: 0, width: 400, height: 300, fill: '#eee' } })
const FB = call('insert', { parent: 'document', node: { type: 'frame', name: 'B', x: 0, y: 500, width: 600, height: 300, fill: '#fcc', flipX: true } }).id
const fa = call<B>('bounds', {})
assert(same({ ...fa, id: '' }, { id: '', x: -600, y: 0, w: 1000, h: 800 }), `fit-all bounds with a flipped frame ${JSON.stringify(fa)}`)
call('update', { ids: [FB], props: { flipX: false, x: 0, y: 300, rotation: 90 } })
const ft = call<B>('bounds', {})
assert(same({ ...ft, id: '' }, { id: '', x: 0, y: -300, w: 400, h: 600 }), `fit-all bounds with a turned frame ${JSON.stringify(ft)}`)

// A child overflowing an unclipped frame is part of Fit all; a clipping frame cuts it off.
call('new', { template: 'empty' })
const FO = call('insert', { parent: 'document', node: { type: 'frame', name: 'O', x: 0, y: 0, width: 400, height: 300, layout: 'none', fill: '#eee' } }).id
call('insert', { parent: FO, node: { type: 'rectangle', x: 300, y: 200, width: 500, height: 400, fill: '#f00' } })
const fo = call<B>('bounds', {})
assert(same({ ...fo, id: '' }, { id: '', x: 0, y: 0, w: 800, h: 600 }), `fit-all bounds with an overflowing child ${JSON.stringify(fo)}`)
call('update', { ids: [FO], props: { clip: true } })
const fc = call<B>('bounds', {})
assert(same({ ...fc, id: '' }, { id: '', x: 0, y: 0, w: 400, h: 300 }), `fit-all bounds with a clipping frame ${JSON.stringify(fc)}`)

assert(ICONS.saveAs !== ICONS.open, 'Save as… has its own icon, not the Open folder')

console.log('editor-check ok')
process.exit(0)
