// Documents, export, variables, settings and edit commands through the core (no window): `bun scripts/doc-check.ts`
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
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
  const run = Bun.spawnSync([process.execPath, import.meta.path], { env: { ...process.env, PATH, NALARUPA_DATA: data, NALARUPA_CHECK_CHILD: '1' }, stdio: ['inherit', 'inherit', 'inherit'] })
  rmSync(data, { recursive: true, force: true })
  process.exit(run.exitCode ?? 1)
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
assert(htmlOf({ fill: '#ff0000', effect: { type: 'blur', radius: 4 } }).includes('filter:blur(2px)'), 'html exports layer blur')
assert(/padding:12px;background:linear-gradient/.test(htmlOf({ fill: '#ffff00', stroke: grad(), strokeWidth: 12 })), 'html exports a gradient stroke')
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

// Settings persist to the data folder; the API key is never read back.
call('settings_set', { key: 'anthropic_api_key', value: 'sk-test-not-real' })
call('settings_set', { key: 'agent_backend', value: 'claude-code' })
const s = call('settings_get')
assert(s.agent_backend === 'claude-code' && s.anthropic_api_key === '••••••••', 'settings_get masks the API key')
assert(JSON.parse(readFileSync(join(data, 'settings.json'), 'utf8')).agent_backend === 'claude-code', 'settings are saved to settings.json')
call('settings_set', { key: 'anthropic_api_key', value: null })
assert(!('anthropic_api_key' in call('settings_get')), 'a null value removes the setting')
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
  } finally {
    call('agent_stop')
  }
  await until(() => !call('state').agentRunning, 'the agent stops')
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
  const brief = 'lorem ipsum '.repeat(12000)
  const bin = join(data, 'bin')
  rmSync(join(bin, 'stdin.txt'), { force: true })
  rmSync(join(bin, 'args.txt'), { force: true })
  try {
    call('agent_send', { text: `Summarize this brief: ${brief}` })
    const sent = join(bin, 'stdin.txt')
    await until(() => existsSync(sent) && readFileSync(sent, 'utf8').includes(brief), 'the whole prompt reaches the CLI on stdin')
    await until(() => existsSync(join(bin, 'args.txt')), 'the CLI started')
    assert(!readFileSync(join(bin, 'args.txt'), 'utf8').includes('lorem'), 'the prompt is not an argument')
    assert(call('state').agentRunning, 'a long prompt starts the run')
  } finally {
    call('agent_stop')
  }
  await until(() => !call('state').agentRunning, 'the long-prompt run stops')
}

console.log('doc-check ok')
process.exit(0)
