// The `execute` scripting API through the core (no window): `bun scripts/script-check.ts`
// What agents write: Copy / Update / Replace / Move / Delete, variables, reads, FindEmptySpace, globals,
// and the transaction rules (one undo step per call, rollback and editId repair on error).
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { call } from '../src/core'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`script-check failed: ${msg}`)
}
// The core reads NALARUPA_DATA through libc, which `process.env` writes don't reach: rerun in a child
// with a fresh data folder so nothing here touches the real settings or chats.
if (!process.env.NALARUPA_CHECK_CHILD) {
  const data = mkdtempSync(join(tmpdir(), 'nalarupa-script-'))
  const run = Bun.spawnSync([process.execPath, import.meta.path], { env: { ...process.env, NALARUPA_DATA: data, NALARUPA_CHECK_CHILD: '1' }, stdio: ['inherit', 'inherit', 'inherit'] })
  rmSync(data, { recursive: true, force: true })
  process.exit(run.exitCode ?? 1)
}
const ex = (input: string, more: Record<string, unknown> = {}) => call('tool', { name: 'execute', args: { input, ...more } }) as { text: string; error: boolean }
const run = (input: string) => {
  const out = ex(input)
  assert(!out.error, `execute failed: ${out.text}`)
  return out.text
}
const node = (id: string) => call('node', { id })
const layers = () => JSON.stringify(call('layers'))
const printed = (text: string) => text.split('## Print output\n')[1]?.trim()

call('init')
call('new', { template: 'empty' })

// One call builds a frame; the report names every created node and the globals it left behind.
const built = run('f = Insert(document, {type:"frame", name:"F", x:0, y:0, width:400, height:300, layout:"none", fill:"#ffffff"}); a = Insert(f, {type:"rectangle", name:"A", x:10, y:10, width:50, height:50, fill:"#ff0000"})')
assert(/"F": "\w+" \(= f\)/.test(built) && /"A": "\w+" \(= a\)/.test(built), `report names created nodes: ${built}`)
assert(/carry over to later calls: f, a/.test(built), 'report lists the carried globals')
const F = built.match(/"F": "(\w+)"/)![1]
const A = built.match(/"A": "(\w+)"/)![1]
assert(node(A).parent === F, 'A is inside F')

// Globals carry over to the next call; Copy with overrides, Update and Get read back.
const copied = run('b = Copy(a, f, {name:"A2", x:100}); Update(a, {fill:"#00ff00"}); Print(Get(a).fill, Get(b).x, Get(b).name)')
assert(printed(copied) === '#00ff00 100 A2', `copy/update/get: ${printed(copied)}`)
const B = copied.match(/"A2": "(\w+)"/)![1]
assert(node(B).parent === F && node(A).node.fill === '#00ff00' && node(B).node.fill === '#ff0000', 'the copy keeps the source fill, the update only touches A')

// Visitor reads walk the tree in document order.
assert(printed(run('Print(Get(f, (n) => n.name))')) === '["F","A","A2"]', 'Get with a visitor lists names in tree order')

// Replace swaps a node for a new one at the same place; Move reorders within the parent; Delete removes.
const replaced = run('c = Replace(b, {type:"ellipse", name:"E", x:100, y:10, width:20, height:20})')
const E = replaced.match(/"E": "(\w+)"/)![1]
assert(node(E).node.type === 'ellipse' && node(E).parent === F && !node(B).node, 'Replace swaps the node')
run('Move(c, f, 0)')
const order = (call('layers') as { id: string; parent: string; index: number }[]).filter((l) => l.parent === F).sort((x, y) => x.index - y.index).map((l) => l.id)
assert(order.join() === [E, A].join(), `Move to index 0: ${order}`)
run('Delete(c)')
assert(!node(E).node && node(A).parent === F, 'Delete removes only that node')

// Variables.
run('SetVariables({brand:{type:"color", value:"#123456"}})')
assert(JSON.parse(printed(run('Print(GetVariables())'))).variables.brand.value === '#123456', 'SetVariables / GetVariables round trip')
run('Update(a, {fill:"$brand"})')
assert(node(A).node.fill === '$brand', 'a node can reference a variable')

// FindEmptySpace: to the right of the frame, and below it.
const right = JSON.parse(printed(run('Print(FindEmptySpace({width:100, height:100, direction:"right"}))')))
assert(right.x >= 400 && typeof right.y === 'number', `empty space right of F: ${JSON.stringify(right)}`)
const below = JSON.parse(printed(run('Print(FindEmptySpace({width:100, height:100, direction:"bottom"}))')))
assert(below.y >= 300, `empty space below F: ${JSON.stringify(below)}`)

// Undo: one call is one undo step, however many operations it made.
const before = layers()
run('Insert(f, {type:"rectangle", name:"U1", width:5, height:5}); Insert(f, {type:"rectangle", name:"U2", width:5, height:5})')
assert(layers().includes('U1') && layers().includes('U2'), 'both inserts landed')
call('undo')
assert(layers() === before, 'one undo reverts the whole call')

// A failing call rolls back what it did and hands out an editId; patching by editId runs the fixed snippet.
const bad = ex('Insert(f, {type:"rectangle", name:"Half"}); Delete("no-such-node")')
assert(bad.error && /rolled back/.test(bad.text) && /node not found: no-such-node/.test(bad.text), `failed call reports and rolls back: ${bad.text}`)
assert(layers() === before && !layers().includes('Half'), 'nothing of the failed call is left')
const editId = bad.text.match(/editId: "(\w+)"/)![1]
const fixed = ex('', { editId, edits: [{ find: 'Delete("no-such-node")', replace: 'Print("fixed")' }] })
assert(!fixed.error && layers().includes('Half') && printed(fixed.text) === 'fixed', `editId repair: ${fixed.text}`)
assert(ex('', { editId, edits: [{ find: 'x', replace: 'y' }] }).error, 'a repaired editId is used up')
assert(/unknown editId/.test(ex('', { editId: 'nope', edits: [] }).text), 'unknown editId is an error')
assert(/not both/.test(ex('Print(1)', { editId, edits: [{ find: 'a', replace: 'b' }] }).text), 'input and edits together are refused')
assert(/required/.test(ex('   ').text) && ex('   ').error, 'an empty input is an error')

// Reading the whole document without a visitor is refused; a missing global is an error, not undefined.
assert(/not allowed/.test(ex('Print(Get("document"))').text), 'Get("document") without a visitor is refused')
assert(/page is not defined/.test(ex('Print(page)').text), 'unknown globals are a ReferenceError')

console.log('script-check ok')
process.exit(0)
