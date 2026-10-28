// Components and instances through the core (no window): `bun scripts/component-check.ts`
// An instance (`type: "ref"`) follows its component: root overrides, `descendants` overrides (content, fill,
// enabled:false, a replacing `type`), edits to the component, nested instances, instance children that cannot be
// deleted, and a deleted component. Layout comes from boxes, colours from PNG exports.
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { call } from '../src/core'
import { decodePng, type Px } from './png'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`component-check failed: ${msg}`)
}
// The core reads NALARUPA_DATA through libc, which `process.env` writes don't reach: rerun in a child
// with a fresh data folder so the documents saved here never touch the real ones.
if (!process.env.NALARUPA_CHECK_CHILD) {
  const data = mkdtempSync(join(tmpdir(), 'nalarupa-component-'))
  let code = 1
  try {
    code = Bun.spawnSync([process.execPath, import.meta.path], { env: { ...process.env, NALARUPA_DATA: data, NALARUPA_CHECK_CHILD: '1' }, stdio: ['inherit', 'inherit', 'inherit'] }).exitCode ?? 1
  } finally {
    rmSync(data, { recursive: true, force: true })
  }
  process.exit(code)
}
const data = process.env.NALARUPA_DATA!
const dir = mkdtempSync(join(data, 'doc-'))

type Box = { id: string; name: string; type: string; x: number; y: number; w: number; h: number }
const box = (id: string) => call<Box[]>('boxes', { ids: [id] })[0]
const kids = (id: string) => call<Box[]>('children_boxes', { id })
const kid = (id: string, name: string) => kids(id).find((k) => k.name === name)
const insert = (parent: string, node: object) => call('insert', { parent, node }).id as string
const update = (id: string, props: object) => call('update', { ids: [id], props })
const corner = (id: string): Px => decodePng(call('export', { ids: [id], format: 'png', path: dir, opts: { scale: 1 } })[0]).at(1, 1)
const colour = (id: string, want: Px, msg: string) => {
  const got = corner(id)
  assert(got.every((v, i) => Math.abs(v - want[i]) <= 3), `${msg}: got ${got}, want ${want}`)
}
const BLUE = [0, 0, 255, 255]
const RED = [255, 0, 0, 255]
const CYAN = [0, 255, 255, 255]
const GREEN = [0, 255, 0, 255]

call('init')
call('new', { template: 'empty' })

// A button component: padding, a label and a dot in a row. It lives above the page, the instances in a column.
const button = insert('document', { type: 'frame', name: 'Button', reusable: true, x: 0, y: -200, padding: [10, 16], gap: 8, fill: '#0000ff', alignItems: 'center' })
const label = insert(button, { type: 'text', name: 'Label', content: 'Button', fontFamily: 'Inter', fontSize: 14, fill: '#ffffff' })
const dot = insert(button, { type: 'rectangle', name: 'Dot', width: 10, height: 10, fill: '#ffff00' })
const page = insert('document', { type: 'frame', name: 'Page', x: 0, y: 0, width: 400, height: 400, layout: 'vertical', gap: 10, padding: 10, fill: '#ffffff' })
const plain = insert(page, { type: 'ref', ref: button, name: 'Plain' })
assert(call('layers').find((l: { id: string }) => l.id === button)?.reusable === true, 'the component is reusable')
assert(box(plain).w === box(button).w && box(plain).h === box(button).h, `an instance is as big as its component: ${box(plain).w}x${box(plain).h} vs ${box(button).w}x${box(button).h}`)
assert(kids(plain).map((k) => k.name).join() === 'Label,Dot', 'an instance has the component\'s children')
colour(plain, BLUE, 'an instance paints the component\'s fill')

// Root overrides and descendants overrides: another label, a recoloured instance, a hidden child, a replaced child.
const save = insert(page, { type: 'ref', ref: button, name: 'Save', descendants: { [label]: { content: 'Save' } } })
const red = insert(page, { type: 'ref', ref: button, name: 'Red', fill: '#ff0000', descendants: { [dot]: { enabled: false } } })
const round = insert(page, { type: 'ref', ref: button, name: 'Round', descendants: { [dot]: { type: 'ellipse', name: 'Round dot', width: 20, height: 20, fill: '#00ff00' } } })
assert(box(save).w < box(plain).w, `a shorter label makes a narrower instance: ${box(save).w} < ${box(plain).w}`)
assert(box(save).w - 2 * 16 - 8 - 10 === kid(save, 'Label')!.w, 'the instance hugs its (overridden) label: padding, gap and dot around it')
colour(red, RED, 'a root fill override repaints the instance')
colour(plain, BLUE, 'and leaves the other instances alone')
colour(button, BLUE, 'and the component')
assert(!kid(red, 'Dot') && kid(red, 'Label'), `enabled:false hides the dot: ${kids(red).map((k) => k.name)}`)
assert(box(red).w === box(plain).w - 8 - 10, `the hidden dot and its gap leave the layout: ${box(red).w}`)
const replaced = kid(round, 'Round dot')
assert(replaced && replaced.type === 'ellipse' && replaced.w === 20 && !kid(round, 'Dot'), `an override with a type replaces the child: ${kids(round).map((k) => `${k.name}:${k.type}`)}`)
assert(call('node', { id: save }).node.descendants[label].content === 'Save', 'the override is stored on the instance, keyed by the child id')

// Edits to the component reach every instance, except where an instance overrides the property.
update(button, { fill: '#00ffff', padding: [20, 16] })
colour(plain, CYAN, 'a component fill change repaints its instances')
colour(save, CYAN, 'including those that override only a child')
colour(red, RED, 'but not the one that overrides the fill itself')
const roomy = box(plain).h
assert(roomy === box(button).h, `a padding change re-flows the instance like the component: ${roomy}`)
update(button, { padding: [10, 16] })
assert(box(plain).h === roomy - 20 && box(plain).h === box(button).h, `padding back to 10 shrinks the instance by 20: ${box(plain).h} vs ${roomy}`)
update(label, { content: 'A much longer label' })
assert(box(plain).w > box(red).w && box(save).w < box(plain).w, `a component text edit widens its instances, not the one with its own label: ${box(plain).w} vs ${box(save).w}`)

// Instance children are addressed as "instance/child": editable, but not deletable.
const wide = box(save).w
call('update', { ids: [`${save}/${label}`], props: { content: 'Save changes now' } })
assert(box(save).w > wide && call('node', { id: save }).node.descendants[label].content === 'Save changes now', 'updating "instance/child" stores an override and re-flows the instance')
assert(box(plain).w === box(button).w, 'the component and the other instance are untouched')
let blocked = ''
try {
  call('delete', { ids: [`${save}/${dot}`] })
} catch (e) {
  blocked = (e as Error).message
}
assert(/cannot delete descendants of component instances/.test(blocked), `deleting an instance child is refused: ${blocked}`)
assert(kid(save, 'Dot'), 'the dot is still there')
call('undo')
assert(box(save).w === wide, `undo drops the child override: ${box(save).w} vs ${wide}`)

// Nested instances: a card holds an instance of a chip; the outer instance reaches into it with "inner/child" paths.
const chip = insert('document', { type: 'frame', name: 'Chip', reusable: true, x: 0, y: -400, width: 60, height: 30, layout: 'none', fill: '#ff0000' })
const tag = insert(chip, { type: 'rectangle', name: 'Tag', x: 5, y: 5, width: 20, height: 20, fill: '#ffffff' })
const card = insert('document', { type: 'frame', name: 'Card', reusable: true, x: 0, y: -100, layout: 'horizontal', gap: 6, padding: 6, fill: '#222222' })
const inner = insert(card, { type: 'ref', ref: chip, name: 'Inner' })
const cards = insert('document', { type: 'frame', name: 'Cards', x: 500, y: 0, width: 300, height: 300, layout: 'vertical', gap: 6, fill: '#ffffff' })
const deep = insert(cards, { type: 'ref', ref: card, name: 'Deep', descendants: { [`${inner}/${tag}`]: { fill: '#00ff00' } } })
const tinted = insert(cards, { type: 'ref', ref: card, name: 'Tinted', descendants: { [inner]: { fill: '#0000ff' } } })
const pristine = insert(cards, { type: 'ref', ref: card, name: 'Pristine' })
assert(box(deep).w === 60 + 12 && box(deep).h === 30 + 12, `an instance of a card holding a chip is 72x42: ${box(deep).w}x${box(deep).h}`)
const innerOf = (id: string) => kids(id)[0]
const tagOf = (id: string) => kids(innerOf(id).id)[0]
assert(innerOf(deep).type === 'frame' && tagOf(deep).w === 20, `the chip shows inside the card instance: ${innerOf(deep).type} ${tagOf(deep).w}`)
colour(innerOf(deep).id, RED, 'a nested instance paints the chip fill')
colour(innerOf(tinted).id, BLUE, 'an override on the nested instance repaints the chip')
colour(innerOf(pristine).id, RED, 'another card instance still has the original chip')
colour(tagOf(deep).id, GREEN, 'a "inner/child" override repaints the chip\'s own child')
colour(tagOf(pristine).id, [255, 255, 255, 255], 'and only for that card instance')

// Save → open keeps the overrides; a deleted component leaves its instances empty, and undo brings it back.
const before = [save, red, round, deep].map((id) => `${box(id).w}x${box(id).h}`).join()
const path = join(dir, 'components.pen')
call('save', { path })
call('new', { template: 'empty' })
call('open', { path })
assert([save, red, round, deep].map((id) => `${box(id).w}x${box(id).h}`).join() === before, 'instances lay out the same after reopening')
assert(!kid(red, 'Dot') && kid(round, 'Round dot')?.type === 'ellipse', 'the hidden and replaced children are kept after reopening')
colour(red, RED, 'and the root fill override')
call('delete', { ids: [button] })
assert(box(save).w === 0 && box(save).h === 0, `an instance of a deleted component collapses: ${box(save).w}x${box(save).h}`)
assert(call('node', { id: save }).node.ref === button, 'but it keeps pointing at the component id')
call('undo')
assert(`${box(save).w}x${box(save).h}` === before.split(',')[0], 'undo restores the component and the instance')
console.log('component-check ok')
