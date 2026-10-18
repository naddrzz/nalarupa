// Documents, export, variables, settings and edit commands through the core (no window): `bun scripts/doc-check.ts`
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
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
  const run = Bun.spawnSync([process.execPath, import.meta.path], { env: { ...process.env, NALARUPA_DATA: data, NALARUPA_CHECK_CHILD: '1' }, stdio: ['inherit', 'inherit', 'inherit'] })
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
const magic = (file: string, bytes: number[]) => readFileSync(file).subarray(0, bytes.length).equals(Buffer.from(bytes))

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
throws(() => call('open', { path: join(data, 'missing.pen') }), /./, 'opening a missing file')
writeFileSync(join(data, 'broken.pen'), '{not json')
throws(() => call('open', { path: join(data, 'broken.pen') }), /./, 'opening invalid JSON')
assert(call('state').path === b && ids() === 'sld01,sld02,sld03', 'a failed open keeps the current document')
throws(() => call('open'), /path required/, 'open without a path')

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
}
one('png', [0x89, 0x50, 0x4e, 0x47])
one('jpg', [0xff, 0xd8, 0xff])
one('webp', [0x52, 0x49, 0x46, 0x46])
assert(readFileSync(call('export', { ids: ['sld02'], format: 'svg', path: out })[0], 'utf8').includes('<svg'), 'svg export')
const [pdf] = call('export', { format: 'pdf', path: join(out, 'deck.pdf') })
assert(readFileSync(pdf, 'latin1').startsWith('%PDF-') && readFileSync(pdf, 'latin1').match(/\/Type ?\/Page\b(?!s)/g)?.length === 3, 'deck pdf has one page per slide')
const [html] = call('export', { ids: ['sld02'], format: 'html-css', path: join(out, 'slide.html') })
assert(/<html[\s>]/i.test(readFileSync(html, 'utf8')) && readFileSync(html, 'utf8').includes('Hello'), 'html export contains the text')
assert(Buffer.from(call('png', { id: 'sld01', scale: 0.1 }), 'base64').subarray(0, 4).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47])), 'png command returns a PNG')
throws(() => call('export', { ids: ['sld02'], format: 'bmp', path: out }), /unknown export format/, 'unknown export format')
throws(() => call('export', { ids: ['nope'], format: 'png', path: out }), /./, 'exporting a missing node')
call('new', { template: 'empty' })
throws(() => call('export', { format: 'pdf', path: join(out, 'empty.pdf') }), /nothing to export/, 'pdf of an empty document')

// Settings persist to the data folder; the API key is never read back.
call('settings_set', { key: 'anthropic_api_key', value: 'sk-test-not-real' })
call('settings_set', { key: 'agent_backend', value: 'claude-code' })
const s = call('settings_get')
assert(s.agent_backend === 'claude-code' && s.anthropic_api_key !== 'sk-test-not-real', 'settings_get masks the API key')
assert(JSON.parse(readFileSync(join(data, 'settings.json'), 'utf8')).agent_backend === 'claude-code', 'settings are saved to settings.json')
call('settings_set', { key: 'anthropic_api_key', value: null })
assert(!('anthropic_api_key' in call('settings_get')), 'a null value removes the setting')
throws(() => call('settings_set', { value: 1 }), /key required/, 'settings_set without a key')

console.log('doc-check ok')
process.exit(0)
