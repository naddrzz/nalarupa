// The feedback `execute` gives an agent after a call, through the core (no window): `bun scripts/issues-check.ts`
// "Issues detected" (layout and content warnings for the nodes the call touched), Generate errors,
// and the Export / TakeScreenshot lines of the report.
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { call } from '../src/core'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`issues-check failed: ${msg}`)
}
// The core reads NALARUPA_DATA through libc, which `process.env` writes don't reach: rerun in a child
// with a fresh data folder so nothing here touches the real settings or chats.
if (!process.env.NALARUPA_CHECK_CHILD) {
  const data = mkdtempSync(join(tmpdir(), 'nalarupa-issues-'))
  const run = Bun.spawnSync([process.execPath, import.meta.path], { env: { ...process.env, NALARUPA_DATA: data, NALARUPA_CHECK_CHILD: '1' }, stdio: ['inherit', 'inherit', 'inherit'] })
  rmSync(data, { recursive: true, force: true })
  process.exit(run.exitCode ?? 1)
}
const ex = (input: string) => call('tool', { name: 'execute', args: { input } }) as { text: string; error: boolean; images: number }
const run = (input: string) => {
  const out = ex(input)
  assert(!out.error, `execute failed: ${out.text}`)
  return out.text
}
/** The bullet list under "Issues detected", empty when the call reported none. */
const issues = (text: string) => (text.split('## Issues detected (fix them in the next call)\n')[1] ?? '').split('\n').filter((l) => l.startsWith('- '))
const has = (text: string, re: RegExp) => issues(text).some((l) => re.test(l))

call('init')
call('new', { template: 'empty' })

// A clean build reports no issues section at all.
const clean = run('f = Insert(document, {type:"frame", name:"Clean", x:0, y:0, width:300, height:200, layout:"none", fill:"#ffffff"}); Insert(f, {type:"text", name:"T", x:10, y:10, content:"Hi", fontFamily:"Inter", fontSize:16, fill:"#111111"})')
assert(!clean.includes('Issues detected'), `a clean call has no issues section:\n${clean}`)

// Text without a fill is invisible.
let out = run('Insert(f, {type:"text", name:"Ghost", x:10, y:50, content:"boo", fontFamily:"Inter", fontSize:16})')
assert(has(out, /text "Ghost" \(\w+\) has no fill and is invisible/), `invisible text: ${out}`)

// Reading text below 12px is reported; a short label may be smaller.
out = run('Insert(f, {type:"text", name:"Fine print", x:10, y:130, content:"Orders close Thursday at six; collect on Saturday.", fontFamily:"Inter", fontSize:10, fill:"#111111"})')
assert(has(out, /text "Fine print" \(\w+\): body text at 10px is below 12px/), `small body text: ${out}`)
out = run('Insert(f, {type:"text", name:"Tag", x:200, y:130, content:"NEW", fontFamily:"Inter", fontSize:10, fill:"#111111"})')
assert(!has(out, /below 12px/), `a short label may be small: ${out}`)

// Outside auto layout, sibling edges 1–2px apart are reported (here against the column of text at x 10), once per axis;
// edges 8px apart are a choice, and auto layout places its own children.
out = run('Insert(f, {type:"rectangle", name:"Nudged", x:11.5, y:170, width:40, height:10, fill:"#cccccc"})')
assert(issues(out).filter((l) => /"Nudged" \(\w+\) nearly align: their left edges are 1\.5px apart/.test(l)).length === 1, `near-miss alignment: ${out}`)
out = run('Insert(f, {type:"rectangle", name:"Offset", x:18, y:186, width:40, height:10, fill:"#cccccc"})')
assert(!has(out, /"Offset"/), `8px apart is not a near miss: ${out}`)

// Text below WCAG AA contrast against what it sits on is reported; large text needs 3:1, body text 4.5:1.
out = run('Insert(f, {type:"text", name:"Faint", x:200, y:60, content:"Open daily", fontFamily:"Inter", fontSize:16, fill:"#aaaaaa"})')
assert(has(out, /text "Faint" \(\w+\): contrast 2\.32:1 against its background #ffffff is below WCAG AA \(4\.5:1\)/), `low contrast: ${out}`)
out = run('Insert(f, {type:"text", name:"Big grey", x:200, y:150, content:"Hours", fontFamily:"Inter", fontSize:28, fill:"#949494"})')
assert(!has(out, /contrast/), `large text at 3:1 passes: ${out}`)
out = run('night = Insert(document, {type:"frame", name:"Night", x:0, y:600, width:300, height:100, layout:"vertical", fill:"#111111"}); wrap = Insert(night, {type:"frame", name:"Wrap", layout:"vertical"}); Insert(wrap, {type:"text", name:"Soot", content:"Hard to see", fontFamily:"Inter", fontSize:16, fill:"#333333"})')
assert(has(out, /text "Soot" \(\w+\): contrast 1\.\d\d:1 against its background #111111/), `the background is found through a transparent frame: ${out}`)

// A layer wider than its fixed parent sticks out.
out = run('Insert(f, {type:"rectangle", name:"Wide", x:0, y:100, width:500, height:20, fill:"#ff0000"})')
assert(has(out, /"Wide" \(\w+\) sticks out of its parent/), `sticks out: ${out}`)

// x/y inside an auto-layout parent are ignored; absolute positioning opts out of the warning.
out = run('col = Insert(document, {type:"frame", name:"Col", x:400, y:0, width:200, height:200, layout:"vertical", fill:"#ffffff"}); Insert(col, {type:"rectangle", name:"Flow", x:5, y:5, width:50, height:50, fill:"#00ff00"})')
assert(has(out, /"Flow" \(\w+\): x\/y are ignored inside a flex layout parent/), `x/y in flex: ${out}`)
out = run('Insert(col, {type:"rectangle", name:"Pinned", x:5, y:5, width:20, height:20, layoutPosition:"absolute", fill:"#0000ff"})')
assert(!has(out, /Pinned/), `an absolute child in a flex parent is fine: ${out}`)
out = run('Insert(col, {type:"rectangle", name:"Stacked", width:51, height:20, fill:"#00ff00"})')
assert(!has(out, /nearly align/), `auto layout children are never near misses: ${out}`)

// An instance's x/y come from its component, not the snippet: no warning unless the instance sets its own.
out = run(`comp = Insert(document, {type:"frame", name:"Btn", reusable:true, x:300, y:400, padding:8, fill:"#0000ff", width:100, height:40})
row = Insert(document, {type:"frame", name:"Row", x:900, y:0, layout:"horizontal", gap:8, padding:8, fill:"#eeeeee"})
Insert(row, {type:"ref", ref:comp, name:"inst1"}); Copy(comp, row)`)
assert(!out.includes('Issues detected'), `instances of a placed component are not flagged for the component's x/y: ${out}`)
out = run('Insert(row, {type:"ref", ref:comp, name:"inst2", x:5, y:5})')
assert(has(out, /"inst2" \(\w+\): x\/y are ignored inside a flex layout parent/), `an instance's own x/y in flex: ${out}`)

// A nested instance reports its own x/y override (set through the outer instance's descendants), not the component's.
out = run(`inner = Insert(document, {type:"frame", name:"Inner", reusable:true, x:50, y:60, width:30, height:30, fill:"#ff0000"})
outer = Insert(document, {type:"frame", name:"Outer", reusable:true, x:700, y:700, layout:"horizontal", gap:4, padding:4, width:80, height:50, fill:"#ffffff"})
ni = Insert(outer, {type:"ref", ref:inner})
host = Insert(document, {type:"frame", name:"Host", x:1200, y:0, layout:"horizontal", width:200, height:100, fill:"#dddddd"})
oi = Insert(host, {type:"ref", ref:outer, name:"oi"})`)
assert(!out.includes('x/y are ignored'), `a nested instance of a placed component is not flagged: ${out}`)
out = run('Update(oi + "/" + ni, {x:5, y:5})')
assert(has(out, /x\/y are ignored inside a flex layout parent/), `a nested instance's own x/y in flex: ${out}`)

// fill_container needs a flex parent.
out = run('Insert(f, {type:"rectangle", name:"Fill", x:0, y:130, width:"fill_container", height:10, fill:"#999999"})')
assert(has(out, /"Fill" \(\w+\) uses fill_container but its parent has no flex layout/), `fill_container in a freeform frame: ${out}`)

// A fit_content parent whose only children fill it collapses to nothing.
out = run('Insert(document, {type:"frame", name:"Squash", x:700, y:0, width:"fit_content", height:"fit_content", layout:"vertical", children:[{type:"rectangle", name:"Kid", width:"fill_container", height:"fill_container", fill:"#cccccc"}]})')
assert(has(out, /"Squash" \(\w+\) collapsed to 0×0/), `collapsed frame: ${out}`)

// An icon that is not in its library.
out = run('Insert(f, {type:"icon", name:"Nope", x:200, y:10, width:24, height:24, library:"lucide", icon:"definitely-not-an-icon", fill:"#111111"})')
assert(has(out, /icon "Nope" \(\w+\): 'definitely-not-an-icon' not found in library 'lucide'/), `missing icon: ${out}`)
out = run('Insert(f, {type:"icon", name:"Real", x:240, y:10, width:24, height:24, library:"lucide", icon:"star", fill:"#111111"})')
assert(!has(out, /Real/), `a real icon is not flagged: ${out}`)

// Only what the call touched (and the children of a touched parent) is checked.
run('other = Insert(document, {type:"frame", name:"Other", x:0, y:900, width:100, height:100, layout:"none", fill:"#ffffff"})')
out = run('Update(other, {name:"Other 2"})')
assert(!out.includes('Issues detected'), `problems elsewhere are not reported by an unrelated edit:\n${out}`)
out = run('Update(f, {name:"Clean"})')
assert(has(out, /"Wide" \(\w+\) sticks out/), `touching a parent re-checks its children: ${out}`)

// Get hands the same problem to a visitor as `c.problems`.
const seen = run('Get(f, (n, c) => c.problems && Print(n.name, c.problems))')
assert(/## Print output\nWide /.test(seen), `a visitor sees c.problems for the node that sticks out: ${seen}`)

// Generate: unknown types and svg on a non-frame are errors that roll the whole call back.
const before = JSON.stringify(call('layers'))
let bad = ex('Insert(f, {type:"rectangle", name:"Half", width:5, height:5}); Generate("video", f, "a cat")')
assert(bad.error && /Generate type 'video' is not available/.test(bad.text) && /rolled back/.test(bad.text), `unknown Generate type: ${bad.text}`)
bad = ex('r = Insert(f, {type:"rectangle", name:"Half", width:5, height:5}); Generate("svg", r, "a cat")')
assert(bad.error && /Generate\("svg"\) draws into a frame/.test(bad.text), `svg into a rectangle: ${bad.text}`)
assert(JSON.stringify(call('layers')) === before, 'a failed Generate leaves nothing behind')

// Export: written files are listed; a bad format or node is reported in the text, not thrown.
// A relative path lands in the export folder (the data folder's `exports` while the document is unsaved).
const dir = 'shots'
const fid = call('frames').find((fr: { name: string }) => fr.name === 'Clean').id
out = run(`Export(["${fid}"], "png", ${JSON.stringify(dir)}, {scale: 0.5})`)
const file = out.match(/Exported: (.+)/)?.[1]
assert(file && existsSync(file) && readFileSync(file).subarray(1, 4).toString() === 'PNG', `Export writes a PNG: ${out}`)
assert(/Export failed: unknown export format 'bmp'/.test(run(`Export(["${fid}"], "bmp", ${JSON.stringify(dir)})`)), 'an unknown export format is reported')
assert(/Export failed: node not found: nope/.test(run(`Export(["nope"], "png", ${JSON.stringify(dir)})`)), 'a missing node is reported')

// Export never writes over the open document or any other file, and stays in the document's folder.
const work = mkdtempSync(join(tmpdir(), 'nalarupa-issues-out-'))
const doc = join(work, 'design.nala')
const victim = join(work, 'victim.txt')
call('save', { path: doc })
writeFileSync(victim, 'precious')
// A file named like an export is still the user's own (#596).
const kept = [join(work, 'report.pdf'), join(work, 'page.html')]
for (const f of kept) writeFileSync(f, 'precious')
const saved = readFileSync(doc, 'utf8')
const outside = mkdtempSync(join(tmpdir(), 'nalarupa-issues-outside-'))
const escape = join(outside, 'escape.pdf')
const elsewhere = join(outside, 'elsewhere')
for (const [format, path] of [['pdf', doc], ['html-css', doc], ['pdf', victim], ['html-css', victim], ['pdf', kept[0]], ['html-css', kept[1]], ['pdf', join(work, '.hidden.pdf')], ['pdf', escape], ['png', elsewhere]]) {
  out = run(`Export([], ${JSON.stringify(format)}, ${JSON.stringify(path)})`)
  assert(/Export failed: .*ask the user|Export failed: .*file name/.test(out) && !out.includes('Exported:'), `Export ${format} to ${path} is refused: ${out}`)
}
assert(readFileSync(doc, 'utf8') === saved && [victim, ...kept].every(f => readFileSync(f, 'utf8') === 'precious'), 'a refused Export leaves the files as they were')
assert(!existsSync(escape) && !existsSync(elsewhere) && !existsSync(join(work, '.hidden.pdf')), 'a refused Export creates nothing')
// The open document itself, even when its name passes the format check.
for (const [format, name] of [['pdf', 'self.pdf'], ['html-css', 'self.html']]) {
  const self = join(work, name)
  call('save', { path: self })
  const kept = readFileSync(self, 'utf8')
  out = run(`Export([], ${JSON.stringify(format)}, ${JSON.stringify(self)})`)
  assert(/Export failed: .*never over the open document/.test(out) && readFileSync(self, 'utf8') === kept, `Export ${format} over the open document is refused: ${out}`)
}
call('save', { path: doc })
out = run('Export([], "pdf", "deck.pdf")')
assert(readFileSync(join(work, 'deck.pdf')).subarray(0, 5).toString() === '%PDF-', `a PDF named like one inside the document's folder is written: ${out}`)
rmSync(work, { recursive: true, force: true })
rmSync(outside, { recursive: true, force: true })

// TakeScreenshot attaches an image; a missing node is a line in the report.
assert(ex(`TakeScreenshot(["${fid}"])`).images === 1, 'TakeScreenshot attaches one image')
assert(/Screenshot of nope failed: node not found: nope/.test(run('TakeScreenshot(["nope"])')), 'a screenshot of a missing node is reported')

console.log('issues-check ok')
process.exit(0)
