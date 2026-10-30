// Documents, export, variables, settings and edit commands through the core (no window): `bun scripts/doc-check.ts`
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, join } from 'node:path'
import { call } from '../src/core'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`doc-check failed: ${msg}`)
}
const throws = (fn: () => unknown, re: RegExp, msg: string) => {
  try {
    fn()
  } catch (e) {
    return assert(re.test((e as Error).message), `${msg}: wrong error "${(e as Error).message}"`)
  }
  assert(false, `${msg}: did not throw`)
}
// The core reads NALARUPA_DATA through libc, which `process.env` writes don't reach: rerun in a child
// with a fresh data folder so settings and documents written here never touch the real ones.
if (!process.env.NALARUPA_CHECK_CHILD) {
  const data = mkdtempSync(join(tmpdir(), 'nalarupa-doc-'))
  // A stand-in `claude` that records its stdin and arguments, then runs long and prints nothing: a running agent without a login.
  const bin = join(data, 'bin')
  mkdirSync(bin)
  writeFileSync(join(bin, 'claude'), '#!/bin/sh\ncat > "$(dirname "$0")/stdin.txt"\nprintf \'%s\\n\' "$@" > "$(dirname "$0")/args.txt"\nexec sleep 60\n', { mode: 0o755 })
  const PATH = `${bin}${delimiter}${process.env.PATH ?? ''}`
  // A blank ANTHROPIC_API_KEY (common in CI) must count as no key; a real one selects the API backend.
  const spawn = (child: string, key: string) => Bun.spawnSync([process.execPath, import.meta.path], { env: { ...process.env, PATH, NALARUPA_DATA: data, NALARUPA_CHECK_CHILD: child, ANTHROPIC_API_KEY: key }, stdio: ['inherit', 'inherit', 'inherit'] })
  const run = spawn('1', ' ')
  const keyed = spawn('keyed', 'sk-ant-env')
  rmSync(data, { recursive: true, force: true })
  // A child killed by a signal has a null exitCode: count it as a failure, never as a pass.
  process.exit((run.exitCode ?? 1) || (keyed.exitCode ?? 1))
}
if (process.env.NALARUPA_CHECK_CHILD === 'keyed') {
  const s = call<Record<string, unknown>>('settings_get')
  assert(call<{ hasApiKey: boolean }>('init').hasApiKey && s.backend === 'api', 'an ANTHROPIC_API_KEY in the environment selects the API backend')
  call('settings_set', { key: 'agent_backend', value: 'claude-code' })
  assert(call<Record<string, unknown>>('settings_get').backend === 'claude-code', 'the agent_backend setting overrides the environment key')
  console.log('doc-check ok (env key)')
  process.exit(0)
}
const data = process.env.NALARUPA_DATA
assert(call('init').dataDir === data, "core uses this run's NALARUPA_DATA")
const exec = (input: string) => {
  const out = call('tool', { name: 'execute', args: { input } })
  assert(!out.error, `execute failed: ${out.text}`)
  return out.text as string
}
const node = (id: string) => call('node', { id })
const ids = () => call('frames').map((f: any) => f.id).join()
const magic = (file: string, bytes: number[], at = 0) => readFileSync(file).subarray(at, at + bytes.length).equals(Buffer.from(bytes))

// Save → open round trip is lossless, and the dirty flag follows edits and saves.
call('new', { template: 'slides' })
throws(() => call('save'), /no path/, 'saving an untitled document without a path')
exec('Insert("sld02", {type:"text", name:"Round trip ✓ ünïcode 日本語", content:"Hello", fontSize:40})')
assert(call('state').dirty, 'an edit marks the document dirty')
const a = join(data, 'a.pen')
assert(call('save', { path: a }).path === a && !call('state').dirty, 'save writes the path and clears dirty')
call('new', { template: 'empty' })
assert(call('frames').length === 0, 'empty template has no frames')
call('open', { path: a })
assert(call('state').path === a && !call('state').dirty && ids() === 'sld01,sld02,sld03', 'open restores the document')
assert(JSON.stringify(call('layers')).includes('Round trip ✓ ünïcode 日本語'), 'unicode layer name survives')
const b = join(data, 'b.pen')
call('save', { path: b })
assert(readFileSync(a, 'utf8') === readFileSync(b, 'utf8'), 'save → open → save is byte-identical')

// Undo or redo back to the saved (or just created) document leaves it clean again.
call('new', { template: 'blank' })
const undoFrame = call('layers')[0].id
const undoRect = call('insert', { parent: undoFrame, node: { type: 'rectangle', x: 10, y: 10, width: 50, height: 50 } }).id
assert(call('state').dirty, 'drawing a rectangle marks a new document dirty')
call('undo')
assert(!call('state').dirty, 'undo back to the new document is clean')
call('redo')
assert(call('state').dirty, 'redo away from the new document is dirty')
call('save', { path: join(data, 'undo.pen') })
call('update', { ids: [undoRect], props: { x: 20 } })
call('undo')
assert(!call('state').dirty, 'undo back to the saved document is clean')
call('undo')
assert(call('state').dirty, 'undo past the saved document is dirty')
call('redo')
assert(!call('state').dirty, 'redo forward to the saved document is clean')

// Saving through a symlink updates the real file and keeps the link; a failed save leaves no temp file behind.
const folder = join(data, 'folder')
mkdirSync(folder)
throws(() => call('save', { path: folder }), /cannot save/, 'saving over a folder')
assert(!existsSync(`${folder}.tmp-nalarupa`), 'a failed save removes its temp file')
const real = join(data, 'real.pen')
const link = join(data, 'link.pen')
writeFileSync(real, '{"version":"2.19","children":[]}')
let linked = true
try {
  symlinkSync(real, link)
} catch (e) {
  const code = (e as NodeJS.ErrnoException).code
  if (process.platform !== 'win32' || (code !== 'EPERM' && code !== 'EACCES')) throw e
  linked = false // creating a symlink needs a privilege on Windows
}
if (linked) {
  call('open', { path: link })
  exec('Insert("document", {type:"rectangle", name:"via-link", width:5, height:5, fill:"#ff0000"})')
  assert(call('save').path === link, 'save keeps the document path')
  assert(lstatSync(link).isSymbolicLink(), 'saving through a symlink keeps the link')
  assert(readFileSync(real, 'utf8').includes('via-link'), 'saving through a symlink updates the real file')
  assert(!existsSync(join(data, 'link.pen.tmp-nalarupa')) && !existsSync(join(data, 'real.pen.tmp-nalarupa')), 'a save leaves no temp file')
  const dangling = join(data, 'dangling.pen')
  symlinkSync(join(data, 'missing.pen'), dangling)
  throws(() => call('save', { path: dangling }), /cannot resolve/, 'saving through a dangling symlink')
  assert(lstatSync(dangling).isSymbolicLink(), 'a dangling symlink is not replaced by a save')
}

// A file saved while a generated image was still loading keeps its pending url; reopening it counts that as a failed load.
for (const url of ['nalarupa:pending-image-1-ab', 'pencil:pending-1']) {
  const pend = join(data, 'pending.pen')
  const fill = JSON.stringify({ type: 'image', url, mode: 'fill' })
  writeFileSync(pend, `{"version":"2.19","children":[{"type":"frame","id":"pf","width":400,"height":300,"children":[{"type":"rectangle","id":"pr","name":"Photo","x":10,"y":10,"width":100,"height":80,"fill":${fill}}]}]}`)
  call('open', { path: pend })
  assert(call('live').length === 0, `a leftover ${url} does not shimmer forever after open`)
  assert(node('pr').node.fill.url === '', `open drops the leftover ${url}`)
}

// A hand-written file may omit node ids or repeat them: open makes them unique so every frame renders and the tree is right.
{
  const rect = (name: string, id?: string) => ({ type: 'rectangle', ...(id === undefined ? {} : { id }), name, x: 20, y: 20, width: 60, height: 40, fill: '#ff0000' })
  const frame = (name: string, x: number, kids: unknown[], id?: string) => ({ type: 'frame', ...(id === undefined ? {} : { id }), name, x, y: 0, width: 200, height: 150, fill: '#ffffff', children: kids })
  const fixtures: Record<string, unknown[]> = {
    noid: [frame('NoIdA', 0, [rect('NoIdRect')]), frame('NoIdB', 300, [rect('NoIdRect2')])],
    dupid: [frame('One', 0, [rect('R1', 'r')], 'f'), frame('Two', 300, [rect('R2', 'r')], 'f')],
  }
  for (const [name, children] of Object.entries(fixtures)) {
    const p = join(data, `${name}.nala`)
    writeFileSync(p, JSON.stringify({ version: '2.19', children }))
    call('new', { template: 'empty' })
    call('open', { path: p })
    const frames = call<any[]>('frames')
    assert(frames.length === 2 && new Set(frames.map((f) => f.x)).size === 2, `${name}: both top-level frames are listed (${frames.map((f) => `${f.name}@${f.x}`)})`)
    const layers = call<any[]>('layers')
    assert(layers.every((l) => l.id) && new Set(layers.map((l) => l.id)).size === layers.length, `${name}: every layer has its own id`)
    assert(layers.map((l) => `${l.depth}`).join('') === '0101', `${name}: children sit under their frame (${layers.map((l) => `${l.name}:${l.depth}`)})`)
    assert(call<string[]>('export', { ids: frames.map((f) => f.id), format: 'svg', path: join(data, `exp-${name}`) }).length === 2, `${name}: every top-level frame exports`)
    call('save')
    const saved = JSON.parse(readFileSync(p, 'utf8'))
    assert(saved.children.every((f: any) => f.id && f.children[0].id), `${name}: save writes the ids back`)
  }
  const dup = JSON.parse(readFileSync(join(data, 'dupid.nala'), 'utf8')).children
  assert(dup[0].id === 'f' && dup[0].children[0].id === 'r', 'the first node keeps a repeated id')
}

// A pending image inside a clipping frame reports the box the frame leaves visible, so its shimmer is cut there.
exec('p = Insert(document, {type:"frame", name:"Clip", x:100, y:100, width:300, height:200, layout:"none", clip:true}); Insert(p, {type:"rectangle", name:"Img", x:150, y:50, width:400, height:300, fill:{type:"image", url:"nalarupa:pending-qa"}}); Insert(document, {type:"rectangle", name:"Free", x:600, y:0, width:50, height:50, fill:{type:"image", url:"nalarupa:pending-qa"}})')
{
  const lv = call('live')
  const img = lv.find((n: any) => n.name === 'Img')
  assert(JSON.stringify(img?.clip) === '{"x":100,"y":100,"w":300,"h":200}', `a pending image in a clip frame carries the frame's box (${JSON.stringify(img?.clip)})`)
  assert(lv.find((n: any) => n.name === 'Free')?.clip === null, 'an unclipped pending image has no clip box')
}

// A round pending image reports its corner radii and its clipping frame's, so the shimmer is cut to that shape.
exec('Insert(document, {type:"rectangle", name:"Pill", x:0, y:600, width:100, height:100, cornerRadius:[50,50,10,0], fill:{type:"image", url:"nalarupa:pending-qa"}}); p = Insert(document, {type:"frame", name:"Rounded", x:300, y:600, width:100, height:100, layout:"none", clip:true, cornerRadius:30}); Insert(p, {type:"rectangle", name:"InRounded", x:0, y:0, width:100, height:100, fill:{type:"image", url:"nalarupa:pending-qa"}})')
{
  const lv = call('live')
  const pill = lv.find((n: any) => n.name === 'Pill')
  assert(JSON.stringify(pill?.radius) === '[50,50,10,0]', `a pending image reports its corner radii (${JSON.stringify(pill?.radius)})`)
  const inner = lv.find((n: any) => n.name === 'InRounded')
  assert(JSON.stringify(inner?.cuts) === '[{"x":300,"y":600,"w":100,"h":100,"r":[30,30,30,30]}]', `a pending image in a rounded clip frame carries the frame's radii (${JSON.stringify(inner?.cuts)})`)
  assert(lv.find((n: any) => n.name === 'Img')?.cuts.length === 0, 'a square clip frame has no radii')
}
// A square clip frame inside a rounded one leaves the outer frame's corners in force.
exec('p = Insert(document, {type:"frame", name:"Outer", x:600, y:600, width:100, height:100, layout:"none", clip:true, cornerRadius:30}); p = Insert(p, {type:"frame", name:"Inner", x:0, y:0, width:60, height:60, layout:"none", clip:true}); Insert(p, {type:"rectangle", name:"Deep", x:0, y:0, width:60, height:60, fill:{type:"image", url:"nalarupa:pending-qa"}})')
{
  const deep = call('live').find((n: any) => n.name === 'Deep')
  assert(JSON.stringify(deep?.cuts?.map((c: any) => c.r)) === '[[30,30,30,30]]', `overlapping clip frames keep the rounded one (${JSON.stringify(deep?.cuts)})`)
}
call('open', { path: b })

// Open failures leave the open document alone.
throws(() => call('open', { path: join(data, 'missing.pen') }), /cannot read .*missing\.pen/, 'opening a missing file')
writeFileSync(join(data, 'broken.pen'), '{not json')
throws(() => call('open', { path: join(data, 'broken.pen') }), /invalid document JSON/, 'opening invalid JSON')
assert(call('state').path === b && ids() === 'sld01,sld02,sld03', 'a failed open keeps the current document')
throws(() => call('open'), /path required/, 'open without a path')

// An imported library resolves its own variables and its internal component refs.
{
  const dir = mkdtempSync(join(data, 'imp-'))
  mkdirSync(join(dir, 'lib'))
  writeFileSync(join(dir, 'lib', 'kit.pen'), JSON.stringify({ version: '2.19', variables: { brand: { type: 'color', value: '#ff0000' } }, children: [
    { id: 'btn', type: 'frame', name: 'KitBtn', reusable: true, x: 0, y: 0, padding: 10, fill: '$brand', children: [{ id: 'lbl', type: 'text', content: 'Kit', fontSize: 14, fontFamily: 'Inter', fill: '#ffffff' }] },
    { id: 'card', type: 'frame', name: 'KitCard', reusable: true, x: 0, y: 100, layout: 'vertical', children: [{ id: 'inner', type: 'ref', ref: 'btn' }] },
  ] }))
  writeFileSync(join(dir, 'main.pen'), JSON.stringify({ version: '2.19', imports: { kit: 'lib/kit.pen' }, children: [
    { id: 'page', type: 'frame', name: 'Page', x: 0, y: 0, width: 300, height: 200, layout: 'vertical', fill: '#ffffff', children: [
      { id: 'b', type: 'ref', ref: 'kit:btn' },
      { id: 'c', type: 'ref', ref: 'kit:card' },
    ] }] }))
  call('open', { path: join(dir, 'main.pen') })
  const [, cb] = call('boxes', { ids: ['b', 'c'] })
  assert(cb.w > 0 && cb.h > 0, `a library component's internal ref resolves (card box ${cb.w}x${cb.h})`)
  const fill = JSON.parse(exec('Print(JSON.stringify(Get("b",{resolveInstances:true,resolveVariables:true})))').split('Print output\n')[1]).fill
  assert(fill === '#ff0000', `a library variable resolves for its own nodes (fill ${fill})`)
  call('open', { path: b })
}

// Save as into another folder keeps relative images and imports pointing at the same files, in the open
// document, in the saved copy when reopened, and in undo history.
{
  const dir = mkdtempSync(join(data, 'saveas-'))
  mkdirSync(join(dir, 'a', 'img'), { recursive: true })
  mkdirSync(join(dir, 'b'))
  writeFileSync(join(dir, 'a', 'img', 'red.svg'), '<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><rect width="8" height="8" fill="#ff0000"/></svg>')
  writeFileSync(join(dir, 'a', 'lib.pen'), JSON.stringify({ version: '2.19', children: [{ id: 'comp', type: 'frame', reusable: true, width: 40, height: 40, fill: '#00ff00' }] }))
  writeFileSync(join(dir, 'a', 'doc.pen'), JSON.stringify({ version: '2.19', imports: { lib: 'lib.pen' }, children: [{ id: 'f', type: 'frame', width: 100, height: 100, fill: '#ffffff', layout: 'none', children: [
    { id: 'r', type: 'rectangle', x: 0, y: 0, width: 50, height: 50, fill: { type: 'image', url: 'img/red.svg', mode: 'fill' } },
    { id: 'i', type: 'ref', ref: 'lib:comp', x: 50, y: 50 }] }] }))
  const shot = () => call('png', { id: 'f' }) as string
  call('open', { path: join(dir, 'a', 'doc.pen') })
  const before = shot()
  call('update', { ids: ['i'], props: { x: 55 } })
  const edited = shot()
  call('save', { path: join(dir, 'b', 'doc.pen') })
  assert(shot() === edited, 'the open document still shows its image and component after Save as')
  const saved = JSON.parse(readFileSync(join(dir, 'b', 'doc.pen'), 'utf8'))
  assert(saved.children[0].children[0].fill.url === '../a/img/red.svg' && saved.imports.lib === '../a/lib.pen', `Save as rewrites relative references (${JSON.stringify([saved.children[0].children[0].fill.url, saved.imports])})`)
  assert(edited !== before, 'the edit before Save as moved the component')
  call('undo')
  assert(call('state').path === join(dir, 'b', 'doc.pen') && shot() === before, 'an undo after Save as brings back the earlier state with its image and component')
  call('redo')
  call('save', { path: join(dir, 'b', 'doc.pen') })
  call('open', { path: join(dir, 'b', 'doc.pen') })
  assert(shot() === edited, 'the saved copy reopens with its image and imported component')
  // Saving again in place leaves the paths alone.
  call('save')
  assert(JSON.parse(readFileSync(join(dir, 'b', 'doc.pen'), 'utf8')).imports.lib === '../a/lib.pen', 'a plain save keeps the paths')
  call('open', { path: b })
}

// Edit commands with undo/redo.
const x0 = node('sld02').node.x
call('select', { ids: ['sld02'] })
assert(call('state').selection.join() === 'sld02', 'select sets the selection')
call('move_by', { ids: ['sld02'], dx: 10, dy: 0 })
assert(node('sld02').node.x === x0 + 10, 'move_by moves the node')
call('undo')
assert(node('sld02').node.x === x0, 'undo restores the position')
call('redo')
assert(node('sld02').node.x === x0 + 10, 'redo reapplies the move')
call('undo')
call('reorder', { ids: ['sld01'], dir: 'front' })
assert(ids().endsWith('sld01'), `front moves sld01 last in order (got ${ids()})`)
call('reorder', { ids: ['sld01'], dir: 'back' })
assert(ids().startsWith('sld01'), `back moves sld01 first (got ${ids()})`)
call('delete', { ids: ['sld03'] })
assert(ids() === 'sld01,sld02', 'delete removes the slide')
call('undo')
assert(ids() === 'sld01,sld02,sld03', 'undo brings it back')
// A layer selected together with its own descendant (either order) is deleted whole, in one undo step.
const nest = call('insert', { parent: 'document', node: { type: 'frame', name: 'Nest', x: 0, y: 0, width: 400, height: 300, layout: 'none' } }).id
const nestKid = call('insert', { parent: nest, node: { type: 'rectangle', name: 'Kid', x: 10, y: 10, width: 50, height: 50 } }).id
for (const order of [[nest, nestKid], [nestKid, nest]]) {
  call('delete', { ids: order })
  assert(!call('boxes', { ids: [nest, nestKid] }).length, 'delete removes a layer selected with its descendant')
  call('undo')
  assert(call('boxes', { ids: [nest, nestKid] }).length === 2, 'one undo restores the layer and its child')
}
throws(() => call('delete', { ids: [nest, 'nope01'] }), /node not found: nope01/, 'delete still rejects an unknown id')
assert(call('boxes', { ids: [nest] }).length === 1, 'a rejected delete leaves the layers in place')
call('delete', { ids: [nest] })
// An instance selected with one of its rendered descendants ("inst/child") goes whole too.
const comp = call('insert', { parent: 'document', node: { type: 'frame', name: 'Comp', reusable: true, x: 0, y: 400, width: 100, height: 100, layout: 'none' } }).id
const compKid = call('insert', { parent: comp, node: { type: 'rectangle', name: 'CompKid', x: 0, y: 0, width: 20, height: 20 } }).id
const inst = call('insert', { parent: 'document', node: { type: 'ref', ref: comp, x: 200, y: 400 } }).id
call('delete', { ids: [inst, `${inst}/${compKid}`] })
assert(!call('boxes', { ids: [inst] }).length, 'delete removes an instance selected with its descendant')
call('undo')
// A lone instance child cannot be deleted or duplicated; the Inspector's Hide is an `enabled:false` override on its path.
const kidPath = `${inst}/${compKid}`
throws(() => call('delete', { ids: [kidPath] }), /cannot delete descendants/, 'delete refuses an instance child')
throws(() => call('duplicate', { ids: [kidPath] }), /node not found/, 'duplicate refuses an instance child')
call('update', { ids: [kidPath], props: { enabled: false } })
assert(!call('boxes', { ids: [kidPath] }).length, 'enabled:false hides the instance child')
call('undo')
assert(call('boxes', { ids: [kidPath] }).length === 1, 'one undo shows the instance child again')
// A mixed selection (an instance child plus an ordinary layer): the Inspector's Hide hides both in one step.
const plain = call('insert', { parent: 'document', node: { type: 'rectangle', name: 'Plain', x: 600, y: 400, width: 20, height: 20, fill: '#00FF00' } }).id
throws(() => call('delete', { ids: [plain, kidPath] }), /cannot delete descendants/, 'delete refuses a selection that includes an instance child')
call('update', { ids: [plain, kidPath], props: { enabled: false } })
assert(!call('boxes', { ids: [plain] }).length && !call('boxes', { ids: [kidPath] }).length, 'enabled:false hides an instance child and an ordinary layer together')
call('undo')
assert(call('boxes', { ids: [plain] }).length === 1 && call('boxes', { ids: [kidPath] }).length === 1, 'one undo shows both again')
call('delete', { ids: [plain] })
call('delete', { ids: [inst] })
call('delete', { ids: [comp] })
throws(() => call('png'), /id required/, 'png without an id')
throws(() => call('no_such_command'), /unknown command/, 'unknown command')

// Variables.
call('set_variable', { name: 'brand', def: { type: 'color', value: '#ff3366' } })
assert(call('variables').variables.brand?.value === '#ff3366', `set_variable stores it: ${JSON.stringify(call('variables'))}`)
call('undo')
assert(!call('variables').variables.brand, 'set_variable is undoable')
throws(() => call('set_variable', { def: { type: 'color', value: '#000' } }), /name required/, 'set_variable without a name')

// Exports: every format writes a valid file; bad input is an error, not a silent no-op.
const out = join(data, 'out')
const one = (format: string, magicBytes: number[]) => {
  const [file] = call('export', { ids: ['sld02'], format, path: out, opts: { scale: 0.25 } })
  assert(existsSync(file) && magic(file, magicBytes), `${format} export is a valid file (${file})`)
  return file
}
one('png', [0x89, 0x50, 0x4e, 0x47])
one('jpg', [0xff, 0xd8, 0xff])
assert(magic(one('webp', [0x52, 0x49, 0x46, 0x46]), [0x57, 0x45, 0x42, 0x50], 8), 'webp export is a RIFF WEBP file')
assert(readFileSync(call('export', { ids: ['sld02'], format: 'svg', path: out })[0], 'utf8').includes('<svg'), 'svg export')
const [pdf] = call('export', { format: 'pdf', path: join(out, 'deck.pdf') })
assert(readFileSync(pdf, 'latin1').startsWith('%PDF-') && readFileSync(pdf, 'latin1').match(/\/Type ?\/Page\b(?!s)/g)?.length === 3, 'deck pdf has one page per slide')
call('update', { ids: ['sld02'], props: { enabled: false } })
const [partial] = call('export', { format: 'pdf', path: join(out, 'partial.pdf') })
assert(readFileSync(partial, 'latin1').match(/\/Type ?\/Page\b(?!s)/g)?.length === 2, 'deck pdf skips a hidden slide')
call('update', { ids: ['sld02'], props: { enabled: null } })
const [html] = call('export', { ids: ['sld02'], format: 'html-css', path: join(out, 'slide.html') })
assert(/<html[\s>]/i.test(readFileSync(html, 'utf8')) && readFileSync(html, 'utf8').includes('Hello'), 'html export contains the text')
// HTML paints follow the canvas: bottom fill last in CSS, blur as sigma, angular as conic, gradient stroke as a ring.
const htmlOf = (node: any) => {
  const id = call('insert', { parent: 'document', node: { type: 'rectangle', x: 0, y: 0, width: 160, height: 100, ...node } }).id
  return readFileSync(call('export', { ids: [id], format: 'html-css', path: join(out, `${id}.html`) })[0], 'utf8')
}
const grad = (extra: any = {}) => ({ type: 'gradient', rotation: 90, colors: [{ color: '#ff0000', position: 0 }, { color: '#0000ff', position: 1 }], ...extra })
assert(htmlOf({ fill: ['#ff0000', '#0000ff80'] }).includes('background:linear-gradient(rgba(0, 0, 255, 0.502),rgba(0, 0, 255, 0.502)),linear-gradient(#ff0000,#ff0000)'), 'html lists the topmost fill first')
assert(htmlOf({ fill: grad({ opacity: 0.4 }) }).includes('rgba(255, 0, 0, 0.400) 0%'), 'html applies gradient fill opacity')
assert(htmlOf({ fill: grad({ gradientType: 'angular' }) }).includes('conic-gradient(from -90deg at 50% 50%'), 'html exports an angular gradient as conic')
assert(htmlOf({ fill: grad({ size: { height: 0.5 } }) }).includes('x1=%220.75%22 y1=%220.5%22 x2=%220.25%22'), 'html exports a shortened linear gradient as an svg background')
assert(htmlOf({ fill: grad({ rotation: 60 }) }).includes('data:image/svg+xml,%3Csvg') && !htmlOf({ fill: grad({ rotation: 60 }) }).includes('linear-gradient(-60deg'), 'html exports a slanted linear gradient as an svg background')
assert(htmlOf({ fill: grad({ gradientType: 'radial', rotation: 45, size: { width: 0.3, height: 0.8 } }) }).includes('matrix(0.106066 -0.106066 0.282843 0.282843 0.5 0.5)'), 'html exports a rotated radial gradient as an svg background with the canvas ellipse')
assert(htmlOf({ fill: '#ff0000', effect: { type: 'blur', radius: 4 } }).includes('filter:blur(2px)'), 'html exports layer blur')
assert(/padding:12px;background:linear-gradient/.test(htmlOf({ fill: '#ffff00', stroke: grad(), strokeWidth: 12 })), 'html exports a gradient stroke')
assert(/inset:-3px;border-radius:50%;box-shadow:inset 0 0 0 6px #000000;/.test(htmlOf({ type: 'ellipse', width: 120, height: 80, fill: '#f0c000', stroke: '#000000', strokeWidth: 6 })), 'html draws a centered stroke as one ring, not two abutting shadows with a hairline between them')
// A bottom fill or stroke with a blend mode blends with the layers beneath the element: an overlay with mix-blend-mode
// outside any isolated box, not a background (background-blend-mode only blends the element's own layers).
const blendHtml = htmlOf({ fill: [{ type: 'color', color: '#00ffff', blendMode: 'multiply' }] })
assert(blendHtml.includes('mix-blend-mode:multiply;') && !blendHtml.includes('background-blend-mode') && !blendHtml.includes('isolation') && !blendHtml.includes('z-index'), `html blends a lone fill with the backdrop: ${blendHtml}`)
assert(/padding:6px;background:linear-gradient\(#00ffff,#00ffff\);mix-blend-mode:screen;/.test(htmlOf({ fill: '#ffffff', stroke: { type: 'color', color: '#00ffff', blendMode: 'screen' }, strokeWidth: 6 })), 'html blends a stroke with the backdrop')
assert(htmlOf({ fill: '#ffffff40', effect: { type: 'background_blur', radius: 16 } }).includes('backdrop-filter:blur(8px)'), 'html exports background blur as a backdrop filter')
// A no-layout frame or group inside a no-layout frame stays absolute (a later position:relative would shift it).
const nested = (type: string, name: string, y: number) => ({ type, name, ...(type === 'frame' ? { layout: 'none' } : {}), x: 20, y, width: 100, height: 50, children: [{ type: 'rectangle', x: 5, y: 5, width: 20, height: 20, fill: '#ff0000' }] })
const nestRoot = call('insert', { parent: 'document', node: { type: 'frame', layout: 'none', x: 0, y: 0, width: 300, height: 300, children: [nested('frame', 'NA', 20), nested('frame', 'NB', 150), nested('group', 'NG', 250)] } }).id
const nestHtml = readFileSync(call('export', { ids: [nestRoot], format: 'html-css', path: join(out, 'nested.html') })[0], 'utf8')
for (const name of ['NA', 'NB', 'NG']) {
  const line = nestHtml.split('\n').find((l) => l.includes(`data-name="${name}"`)) ?? ''
  assert(line.includes('position:absolute;') && !line.includes('position:relative'), `html keeps nested ${name} absolute: ${line}`)
}
// Shapes inlined as SVG paint outside their box (caps, outer strokes, shadows): the inline svg must not clip them.
for (const [type, extra] of [['line', { height: 0, strokeLinecap: 'round' }], ['polygon', { height: 40, polygonCount: 5, strokeAlignment: 'outer' }], ['path', { height: 40, geometry: 'M0 0 L100 0' }], ['ellipse', { height: 40, innerRadius: 0.5, strokeAlignment: 'outer' }]] as const) {
  const id = call('insert', { parent: 'document', node: { type, x: 20, y: 40, width: 100, stroke: '#cc0000', strokeWidth: 20, effect: { type: 'shadow', shadowType: 'outer', offset: { x: 10, y: 10 }, blur: 4, color: '#00000080' }, ...extra } }).id
  const out2 = readFileSync(call('export', { ids: [id], format: 'html-css', path: join(out, `${type}-overflow.html`) })[0], 'utf8')
  assert(/<svg [^>]*style="display:block;overflow:visible"/.test(out2), `html ${type} svg is a block and does not clip its stroke and shadow: ${out2.slice(0, 400)}`)
}
assert(Buffer.from(call('png', { id: 'sld01', scale: 0.1 }), 'base64').subarray(0, 4).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47])), 'png command returns a PNG')
throws(() => call('export', { ids: ['sld02'], format: 'bmp', path: out }), /unknown export format/, 'unknown export format')
throws(() => call('export', { ids: ['nope'], format: 'png', path: out }), /node not found: nope/, 'exporting a missing node')
call('new', { template: 'empty' })
throws(() => call('export', { format: 'pdf', path: join(out, 'empty.pdf') }), /nothing to export/, 'pdf of an empty document')
// A plain base value plus one themed override keeps the base as the default theme (the override is not the default).
const themed = exec('SetVariables({bg: {type: "color", value: [{value: "#ffffff"}, {value: "#111111", theme: {mode: "dark"}}]}}); f = Insert(document, {type: "frame", name: "Page", width: 100, height: 100, fill: "$bg"}); g = Insert(document, {type: "frame", name: "Night", width: 100, height: 100, fill: "$bg", theme: {mode: "dark"}}); Print(JSON.stringify(GetVariables().themes)); Print(Get(f, {resolveVariables: true}).fill); Print(Get(g, {resolveVariables: true}).fill)')
const themedOut = themed.split('Print output\n')[1]?.trim().split('\n')
assert(themedOut?.join('|') === '{"mode":["default","dark"]}|#ffffff|#111111', `base value stays the default theme: ${themed}`)

// A rotated or flipped node exports the box it paints, not its unrotated box.
const rect = (extra: object) => ({ type: 'rectangle', width: 100, height: 60, fill: '#ff0000', x: 200, y: 200, ...extra })
const png = (id: string) => {
  const buf = readFileSync(call('export', { ids: [id], format: 'png', path: out, opts: { scale: 1 } })[0])
  return { w: buf.readUInt32BE(16), h: buf.readUInt32BE(20), buf }
}
for (const [name, extra, w, h, origin] of [['rot90', { rotation: 90 }, 60, 100, '200 100'], ['flipx', { flipX: true }, 100, 60, '100 200'], ['flipy', { flipY: true }, 100, 60, '200 140'], ['rot30', { rotation: 30 }, 117, 102, '200 150']] as const) {
  const id = call('insert', { parent: 'document', node: rect({ name, ...extra }) }).id
  const p = png(id)
  assert(p.w === w && p.h === h, `${name} exports ${w}x${h} (got ${p.w}x${p.h})`)
  const svg = readFileSync(call('export', { ids: [id], format: 'svg', path: out })[0], 'utf8')
  assert(svg.includes(`width="${w}" height="${h}" viewBox="${origin}`), `${name} svg is ${w}x${h} at ${origin}: ${svg.slice(0, 160)}`)
  assert(readFileSync(call('export', { ids: [id], format: 'pdf', path: join(out, `${name}.pdf`) })[0], 'latin1').includes(`/MediaBox[0 0 ${w} ${h}]`), `${name} pdf page is ${w}x${h}`)
}
const frame = call('insert', { parent: 'document', node: { type: 'frame', width: 300, height: 300, x: 500, y: 500 } }).id
const kid = call('insert', { parent: frame, node: rect({ name: 'kid', flipX: true, x: 150, y: 20 }) }).id
const kidSvg = readFileSync(call('export', { ids: [kid], format: 'svg', path: out })[0], 'utf8')
assert(kidSvg.includes('width="100" height="60" viewBox="500 500 100 60"'), `a flipped child of a frame exports its painted box: ${kidSvg.slice(0, 160)}`)
// Text lines past a fixed-width-height box are painted, so the export covers them (not just the layer box).
{
  const over = (name: string, extra: object = {}) => call('insert', { parent: 'document', node: { type: 'text', name, content: 'a\nb\nc\nd\ne', textGrowth: 'fixed-width-height', width: 120, height: 40, fontSize: 24, fill: '#000000', x: 900, y: 900, ...extra } }).id
  const top = over('overflow')
  const p = png(top)
  assert(p.w === 120 && p.h > 100, `an overflowing text exports its lines, not the 120x40 box (got ${p.w}x${p.h})`)
  const svg = readFileSync(call('export', { ids: [top], format: 'svg', path: out })[0], 'utf8')
  assert(svg.includes(`height="${p.h}" viewBox="900 900 120 ${p.h}`), `svg of an overflowing text is as tall as the png: ${svg.slice(0, 160)}`)
  assert(readFileSync(call('export', { ids: [top], format: 'pdf', path: join(out, 'overflow.pdf') })[0], 'latin1').includes(`/MediaBox[0 0 120 ${p.h}]`), 'pdf page of an overflowing text covers its lines')
  const lowId = over('overflow-bottom', { textAlignVertical: 'bottom', y: 1200 })
  const low = png(lowId)
  assert(low.w === 120 && low.h === p.h, `a bottom-aligned overflowing text exports the same height (got ${low.w}x${low.h})`)
  const lowSvg = readFileSync(call('export', { ids: [lowId], format: 'svg', path: out })[0], 'utf8')
  assert(lowSvg.includes(`viewBox="900 ${1240 - low.h} 120 ${low.h}"`), `a bottom-aligned overflow reaches above its box: ${lowSvg.slice(0, 160)}`)
}

// HTML export reserves the rotated bounding box of a child in an auto layout, like the canvas does.
{
  const row = call('insert', { parent: 'document', node: { type: 'frame', name: 'row', layout: 'horizontal', x: 10, y: 10, width: 300, height: 120, padding: 10, gap: 10, fill: '#eeeeee' } }).id
  const sq = (name: string, extra: object = {}) => call('insert', { parent: row, node: { type: 'rectangle', name, width: 60, height: 60, fill: '#ff0000', ...extra } }).id
  const [rot, next] = [sq('rot', { rotation: 45 }), sq('next')]
  const [r, n] = call('boxes', { ids: [rot, next] })
  const bbox = n.x - r.x - 10 // the canvas leaves the rotated bounding box (60 * sqrt 2) between them
  const html = readFileSync(call('export', { ids: [row], format: 'html-css', path: join(out, 'tilted.html') })[0], 'utf8')
  const wrapper = html.match(/<div style="position:relative;flex-shrink:0;width:([\d.]+)px;height:([\d.]+)px;">\n<div data-name="rot"/)
  assert(Math.abs(bbox - 60 * Math.SQRT2) < 0.01, `the canvas places a 45deg square by its bounding box: ${bbox}`)
  assert(wrapper && Math.abs(Number(wrapper[1]) - bbox) < 0.01 && Math.abs(Number(wrapper[2]) - bbox) < 0.01, `html wraps the rotated child in its ${bbox} bounding box: ${html}`)
  assert(/data-name="next" style="width:60px;/.test(html), `the unrotated sibling stays a plain flex item: ${html}`)
}

// A gradient stroke on a horizontal or vertical line exports a usable gradient: a box-sized gradientTransform
// of a flat box is singular and browsers paint nothing.
for (const [name, gradientType, box] of [['hline', 'linear', { x: 20, y: 50, width: 160, height: 0 }], ['vline', 'linear', { x: 100, y: 20, width: 0, height: 160 }], ['hradial', 'radial', { x: 20, y: 50, width: 160, height: 0 }]] as const) {
  const id = call('insert', { parent: 'document', node: { type: 'line', name, ...box, strokeWidth: 20, stroke: { type: 'gradient', gradientType, rotation: 90, colors: [{ color: '#ff0000', position: 0 }, { color: '#0000ff', position: 1 }] } } }).id
  const svg = readFileSync(call('export', { ids: [id], format: 'svg', path: out })[0], 'utf8')
  assert(/<(linear|radial)Gradient [^>]*gradientUnits="userSpaceOnUse"[^>]*>/.test(svg) && /stroke="url\(#[^)]+\)"/.test(svg) && !svg.includes('gradientTransform') && !/\de-\d/.test(svg), `${name} gradient stroke has no singular matrix: ${svg.slice(0, 300)}`)
}
{
  // A rotated radial gradient on a flat box keeps the full half-length as its radius.
  const id = call('insert', { parent: 'document', node: { type: 'line', name: 'hradial45', x: 20, y: 50, width: 160, height: 0, strokeWidth: 20, stroke: { type: 'gradient', gradientType: 'radial', rotation: 45, colors: [{ color: '#ff0000', position: 0 }, { color: '#0000ff', position: 1 }] } } }).id
  const svg = readFileSync(call('export', { ids: [id], format: 'svg', path: out })[0], 'utf8')
  assert(/<radialGradient [^>]* r="80"/.test(svg), `a rotated flat radial gradient keeps its radius: ${svg.slice(0, 300)}`)
}

// Settings persist to the data folder; the API key is never read back.
call('settings_set', { key: 'anthropic_api_key', value: 'sk-test-not-real' })
call('settings_set', { key: 'agent_backend', value: 'claude-code' })
const s = call('settings_get')
assert(s.agent_backend === 'claude-code' && s.anthropic_api_key === '••••••••', 'settings_get masks the API key')
assert(JSON.parse(readFileSync(join(data, 'settings.json'), 'utf8')).agent_backend === 'claude-code', 'settings are saved to settings.json')
call('settings_set', { key: 'anthropic_api_key', value: null })
assert(!('anthropic_api_key' in call('settings_get')), 'a null value removes the setting')
// A blank ANTHROPIC_API_KEY is no key: the core and the settings panel both fall back to Claude Code.
call('settings_set', { key: 'agent_backend', value: null })
assert(call<{ hasApiKey: boolean }>('init').hasApiKey === false && call<Record<string, unknown>>('settings_get').backend === 'claude-code', 'a blank ANTHROPIC_API_KEY does not select the API backend')
call('settings_set', { key: 'anthropic_api_key', value: ' \t' })
assert(call<{ hasApiKey: boolean }>('init').hasApiKey === false && call<Record<string, unknown>>('settings_get').backend === 'claude-code', 'a blank saved API key is no key either')
call('settings_set', { key: 'anthropic_api_key', value: null })
throws(() => call('settings_set', { value: 1 }), /key required/, 'settings_set without a key')

// While an agent runs, a new conversation and a document switch are both refused: the run keeps its chat and
// would keep editing the new document. Stop then allows both. (The stand-in `claude` is a shell script: skipped
// on Windows.)
if (process.platform !== 'win32') {
  const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))
  const until = async (fn: () => boolean, msg: string) => {
    for (let i = 0; i < 100 && !fn(); i++) await sleep(100)
    assert(fn(), msg)
  }
  call('new', { template: 'empty' })
  try {
    call('agent_send', { text: 'Design a landing page' })
    await until(() => call('state').agentRunning, 'the agent starts')
    const chat = call('chat_list').current
    assert(chat, 'the running agent has a chat')
    throws(() => call('agent_reset'), /Stop the agent/, 'new conversation while an agent runs')
    assert(call('chat_list').current === chat && call('state').agentRunning, 'the refused reset keeps the running chat')
    throws(() => call('new', { template: 'slides' }), /Stop the agent/, 'new while the agent runs')
    throws(() => call('open', { path: a }), /Stop the agent/, 'open while the agent runs')
    const doc = call('tool', { name: 'open_document', args: { path: a } })
    assert(doc.error && /Stop the agent/.test(doc.text), 'open_document while the agent runs')
    assert(call('frames').length === 0 && call('state').path === null, 'a refused switch keeps the document')
    // The built-in agent flags one frame, an external agent (MCP) another: Stop clears only the former.
    // `agentRunning` is set before the run's thread emits `start`, so wait for the event.
    let agent: string | undefined
    await until(() => {
      agent ??= call('poll').events.find((ev: any) => ev.kind === 'start')?.agent.id
      return Boolean(agent)
    }, 'the running agent announces itself')
    const frame = (name: string, x: number) => `Insert(document, {type:"frame", name:"${name}", x:${x}, y:0, width:400, height:200, placeholder:true})`
    assert(!call('tool', { name: 'execute', args: { input: frame('Pricing', 0), __agent: agent } }).error, 'the agent flags a frame')
    assert(!call('tool', { name: 'execute', args: { input: frame('External job', 500), __agent: 'external' } }).error, 'an external agent flags a frame')
    assert(call('frames').every((f: any) => f.placeholder === true), 'both frames are generating')
  } finally {
    call('agent_stop')
  }
  await until(() => !call('state').agentRunning, 'the agent stops')
  const flag = (name: string) => call('frames').find((f: any) => f.name === name)?.placeholder
  assert(flag('Pricing') === false, 'Stop ends the "Generating" state of the frames the agent left')
  assert(flag('External job') === true && call('live').length === 1, 'Stop leaves a frame an external agent is still building generating')
  assert(call('agent_reset') === true && call('chat_list').current === null, 'new conversation once the agent has stopped')
  call('new', { template: 'slides' })
  assert(ids() === 'sld01,sld02,sld03', 'switching works again once the agent stopped')

  // A long message and a big selection reach the CLI on stdin, not as arguments (the OS caps the argument list), and
  // the editor state lists at most 40 selected nodes.
  call('new', { template: 'empty' })
  const made = exec('f = Insert(document, {type:"frame", name:"Big", width:800, height:800}); for (let i = 0; i < 2000; i++) Insert(f, {type:"rectangle", name:"Product card background rectangle number " + i, width:10, height:10})')
  const rects = [...made.matchAll(/^"Product card[^"]*": "([^"]+)"/gm)].map((m) => m[1])
  assert(rects.length === 2000, `built 2000 rectangles (${rects.length})`)
  call('select', { ids: rects })
  const sel = call('tool', { name: 'get_editor_state', args: {} }).text as string
  assert(sel.length < 20000 && /\.\.\. \+1960 others/.test(sel), `the selection list is capped (${sel.length} bytes)`)
  // A marker no instruction text contains: the system prompt itself is an argument.
  const brief = 'quokka orbit '.repeat(12000)
  const bin = join(data, 'bin')
  rmSync(join(bin, 'stdin.txt'), { force: true })
  rmSync(join(bin, 'args.txt'), { force: true })
  try {
    call('agent_send', { text: `Summarize this brief: ${brief}` })
    const sent = join(bin, 'stdin.txt')
    await until(() => existsSync(sent) && readFileSync(sent, 'utf8').includes(brief), 'the whole prompt reaches the CLI on stdin')
    await until(() => existsSync(join(bin, 'args.txt')), 'the CLI started')
    assert(!readFileSync(join(bin, 'args.txt'), 'utf8').includes('quokka'), 'the prompt is not an argument')
    assert(call('state').agentRunning, 'a long prompt starts the run')
  } finally {
    call('agent_stop')
  }
  await until(() => !call('state').agentRunning, 'the long-prompt run stops')
}

console.log('doc-check ok')
process.exit(0)
