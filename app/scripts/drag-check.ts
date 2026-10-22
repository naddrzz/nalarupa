// Canvas drag gestures through the core (no window): `bun scripts/drag-check.ts`
// The canvas wraps every move, resize, rotate and pen drag in drag_begin / drag_end: the live edits in
// between must land as one undo step, and a drag that changes nothing must leave no step behind.
import { call } from '../src/core'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`drag-check failed: ${msg}`)
}
const node = (id: string) => call('node', { id }).node
const dirty = () => call('state').dirty as boolean

call('init')
call('new', { template: 'empty' })
const F = call('insert', { parent: 'document', node: { type: 'frame', name: 'F', x: 0, y: 0, width: 400, height: 300, layout: 'none', fill: '#fff' } }).id
const A = call('insert', { parent: F, node: { type: 'rectangle', name: 'A', x: 10, y: 10, width: 50, height: 50 } }).id
const B = call('insert', { parent: F, node: { type: 'rectangle', name: 'B', x: 100, y: 20, width: 50, height: 30 } }).id

// A move drag: many live updates, one undo step that returns to the pre-drag position.
call('drag_begin')
for (let i = 0; i < 5; i++) call('move_by', { ids: [A, B], dx: 4, dy: 2 })
assert(node(A).x === 30 && node(A).y === 20 && node(B).x === 120, 'live edits apply while the drag runs')
call('drag_end')
call('undo')
assert(node(A).x === 10 && node(A).y === 10 && node(B).x === 100 && node(B).y === 20, 'one undo reverts the whole move drag')
call('redo')
assert(node(A).x === 30 && node(B).x === 120, 'redo reapplies the whole drag at once')
call('undo')

// A selection holding a frame and its own child moves the child once, with its parent.
call('move_by', { ids: [F, A], dx: 10, dy: 0 })
assert(node(F).x === 10 && node(A).x === 10, 'a selected child is not moved again with its selected parent')
call('move_by', { ids: [A, F], dx: -10, dy: 0 })
assert(node(F).x === 0 && node(A).x === 10, 'the order of the ids does not matter')

// Resize and rotate drags go through update (the nodes keep their ids): several updates, one step.
call('drag_begin')
for (const w of [60, 70, 80]) call('update', { ids: [A], props: { width: w } })
call('update', { ids: [A], props: { rotation: 15 } })
call('drag_end')
assert(node(A).width === 80 && node(A).rotation === 15, 'resize and rotate updates stick')
call('undo')
assert(node(A).width === 50 && !node(A).rotation, 'one undo reverts the resize and rotate drag')

// A pen drag creates a path (path_write) and keeps editing it inside the same gesture.
call('drag_begin')
const P = call('path_write', { parent: F, contours: [{ closed: false, anchors: [{ x: 0, y: 0 }, { x: 50, y: 0 }] }] }).id
call('path_write', { id: P, contours: [{ closed: false, anchors: [{ x: 0, y: 0 }, { x: 50, y: 0 }, { x: 50, y: 40 }] }] })
call('drag_end')
assert(node(P).type === 'path', 'the pen drag made a path')
call('undo')
assert(!node(P), 'one undo removes a path drawn in one drag')
assert(node(A).x === 10, 'and only the path: the earlier edits are untouched')

// A click that moves nothing leaves no undo step: the next undo reverts the previous real edit.
call('update', { ids: [B], props: { x: 200 } })
call('drag_begin')
call('drag_end')
call('undo')
assert(node(B).x === 100, 'an unchanged drag adds no undo step')

// A drag that moves something and is then dragged back to the start is not an edit either.
call('update', { ids: [B], props: { x: 200 } })
const before = JSON.stringify(call('layers'))
call('drag_begin')
call('move_by', { ids: [A], dx: 30, dy: 0 })
call('move_by', { ids: [A], dx: -30, dy: 0 })
call('drag_end')
assert(JSON.stringify(call('layers')) === before && node(A).x === 10, 'dragging back to the start changes nothing')
call('undo')
assert(node(B).x === 100, 'and leaves no undo step: the next undo reverts the edit before it')

// drag_end with no drag running is harmless; the end of a drag consumes it, so a second one adds nothing.
call('update', { ids: [B], props: { x: 150 } })
call('drag_begin')
call('move_by', { ids: [A], dx: 7, dy: 0 })
call('drag_end')
call('drag_end')
call('undo')
assert(node(A).x === 10 && node(B).x === 150, 'a stray drag_end adds no step')
call('undo')
assert(node(B).x === 100, 'the step before the drag is still there')

// A drag makes a clean document dirty (a fresh template starts clean).
call('new', { template: 'slides' })
const S = call('frames')[0].id
const sx = node(S).x
assert(!dirty(), 'a new document starts clean')
call('drag_begin')
call('move_by', { ids: [S], dx: 5, dy: 5 })
call('drag_end')
assert(dirty() && node(S).x === sx + 5, 'a drag marks a clean document dirty')

// A new drag after undo clears the redo stack.
call('new', { template: 'empty' })
const G = call('insert', { parent: 'document', node: { type: 'frame', name: 'G', x: 0, y: 0, width: 100, height: 100, layout: 'none' } }).id
call('undo')
call('redo')
call('drag_begin')
call('move_by', { ids: [G], dx: 5, dy: 5 })
call('drag_end')
assert(node(G).x === 5, 'the drag moved the layer')
call('undo')
call('drag_begin')
call('move_by', { ids: [G], dx: 9, dy: 0 })
call('drag_end')
call('redo')
assert(node(G).x === 9, 'a drag after an undo drops the redo step')

// Deleting during a drag (a dragged layer dropped outside) is still one step.
call('drag_begin')
call('delete', { ids: [G] })
call('drag_end')
assert(!node(G), 'the layer is gone after the drag')
call('undo')
assert(node(G).x === 9, 'one undo brings the deleted layer back where it was before the drag')

// Undo and redo during a drag are ignored: the drag still lands as one step on top of the history.
call('new', { template: 'empty' })
const H = call('insert', { parent: 'document', node: { type: 'frame', name: 'H', x: 0, y: 0, width: 100, height: 100, layout: 'none' } }).id
call('update', { ids: [H], props: { x: 10 } })
call('drag_begin')
call('move_by', { ids: [H], dx: 5, dy: 0 })
call('undo')
call('redo')
assert(node(H).x === 15, 'undo and redo do nothing while a drag runs')
call('drag_end')
call('undo')
assert(node(H).x === 10, 'the drag is one undo step')
call('undo')
assert(node(H).x === 0, 'the edit before the drag is still its own step')
call('undo')
assert(!node(H), 'and the insert before that')

console.log('drag-check ok')
process.exit(0)
