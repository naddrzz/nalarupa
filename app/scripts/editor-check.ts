// Core checks for the canvas editing commands (no window): `bun scripts/editor-check.ts`
import { call } from '../src/core'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`editor-check failed: ${msg}`)
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

console.log('editor-check ok')
process.exit(0)
