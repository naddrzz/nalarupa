// End-to-end check against the real GPU window: `bun scripts/e2e.ts`
// Screenshots land in app/screenshots/. The window opens in the background.
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { join } from 'node:path'
import { launch } from '@gpuix/react/automation'

const out = join(import.meta.dir, '..', 'screenshots')
mkdirSync(out, { recursive: true })
// GPUIX can't capture on Linux; there the window runs under a headless Wayland compositor, so grab it with grim.
const shot = (name: string) => process.platform === 'linux' ? Bun.$`grim ${join(out, `${name}.png`)}`.quiet() : app.screenshot({ path: join(out, `${name}.png`) })
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

// The core takes the next free port from 47700 when another Nalarupa already holds it; find this window's.
const alive = (p: number) => fetch(`http://127.0.0.1:${p}/mcp`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/list' }) }).then(() => true, () => false)
const ports = async () => (await Promise.all(Array.from({ length: 16 }, (_, i) => 47700 + i).map(async (p) => ((await alive(p)) ? p : 0)))).filter(Boolean)
let port = 0

async function mcp(name: string, args: Record<string, unknown>) {
  const res = await fetch(`http://127.0.0.1:${port}/mcp`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ jsonrpc: '2.0', id: Date.now(), method: 'tools/call', params: { name, arguments: args } }),
  }).then((r) => r.json())
  const text = res.result?.content?.[0]?.text ?? JSON.stringify(res)
  if (res.result?.isError) throw new Error(`${name} failed: ${text}`)
  return text as string
}

// Own data folder, so another running Nalarupa can't touch this run's exports and chats; reuse the
// font/icon caches (same lookup as core's render::data_dir) so nothing is downloaded again.
const data = mkdtempSync(join(tmpdir(), 'nalarupa-e2e-'))
const own = join(process.env.LOCALAPPDATA ?? process.env.XDG_DATA_HOME ?? join(homedir(), '.local', 'share'), 'Nalarupa')
for (const dir of ['cache', 'fonts']) if (existsSync(join(own, dir))) cpSync(join(own, dir), join(data, dir), { recursive: true })

const before = new Set(await ports())
const app = await launch({ command: 'bun', args: ['app.tsx'], cwd: join(import.meta.dir, '..'), env: { ...process.env, GPUIX_BACKGROUND: '1', NALARUPA_DATA: data } })
try {
  await app.getByTestId('template-slides').waitFor({ timeoutMs: 30000 })
  port = (await ports()).find((p) => !before.has(p)) ?? 0
  if (!port) throw new Error('no MCP port for the test window')
  await shot('01-home')

  await app.getByTestId('template-slides').click()
  await app.getByTestId('canvas').waitFor()
  await sleep(1500)
  await shot('02-editor-slides')

  await app.getByText('Slides').click()
  await sleep(800)
  await shot('03-slides-panel')
  await app.getByText('Layers').click()

  // Layer chevrons: the whole 24 px target expands the row without selecting it, and a second quick
  // click collapses it instead of opening rename (the press used to land on the row).
  const toggle = app.getByTestId('layer-toggle-sld01')
  const rows = async () => (await app.getByTestId('layers').getByType('div').all()).filter((n) => /^layer-(?!toggle|eye)/.test(n.testId ?? '')).length
  const rows0 = await rows()
  const tb = await toggle.bounds()
  await app.mouse.click({ x: tb.x + 2, y: tb.y + 2 })
  await sleep(250)
  if ((await rows()) <= rows0 || (await app.getByTestId('selection-box').count())) throw new Error('the layer chevron did not expand (or selected the layer)')
  await toggle.click()
  await sleep(250)
  if ((await rows()) !== rows0 || (await app.getByTestId('layer-rename').count())) throw new Error('a quick second chevron click did not collapse (or opened rename)')

  // Layers as a keyboard tree (rows topmost first: sld03, sld02, sld01). Keys without an element id go
  // to whatever has focus, so each step also proves the previous one moved focus.
  const key = async (k: string) => {
    await app.call('keyDown', { key: k })
    await sleep(200)
  }
  await app.getByTestId('layer-sld03').press('down')
  await sleep(200)
  await key('down')
  await key('right')
  if ((await rows()) !== rows0 + 3 || (await app.getByTestId('selection-box').count())) throw new Error('Down, Down, Right did not expand sld01 (or selected it)')
  await key('right')
  await key('enter')
  if (!(await app.getByTestId('inspector-font-size').count())) throw new Error('Right, Enter did not select the first child (the Kicker text)')
  await key('left')
  await key('left')
  if ((await rows()) !== rows0) throw new Error('Left, Left did not step to sld01 and collapse it around the selection')
  await key('home')
  await key('ctrl-enter')
  if (!(await app.getByText('2 layers selected').count())) throw new Error('Home, Ctrl+Enter did not add sld03 to the selection')
  await key('end')
  await key('enter')
  if ((await app.getByText('2 layers selected').count()) || (await app.getByTestId('inspector-font-size').count())) throw new Error('End, Enter did not select sld01 alone')
  await shot('03b-layers-keyboard')
  await app.getByTestId('canvas').press('escape')
  await sleep(200)

  // Draw a rectangle on the canvas with the rectangle tool.
  await app.getByTestId('tool-rectangle').click()
  const canvas = await app.getByTestId('canvas').bounds()
  await app.mouse.drag({ x: canvas.x + 120, y: canvas.y + 420 }, { x: canvas.x + 260, y: canvas.y + 500 }, { steps: 6 })
  await sleep(500)
  await shot('04-drawn-rectangle')

  // An external agent edits the open document over MCP (what Claude Code/Cursor would do).
  console.log((await mcp('get_editor_state', {})).split('\n').slice(0, 6).join('\n'))
  const result = await mcp('execute', {
    input: `
pos = FindEmptySpace({width: 1920, height: 1080, padding: 100, direction: "right"})
s = Insert(document, {type:"frame", name:"04 Metrics", x: pos.x, y: 0, width:1920, height:1080, fill:"$slide-bg", clip:true, layout:"vertical", padding:[140,160], gap:64})
Insert(s, {type:"text", name:"Title", content:"Growth, at a glance", fontFamily:"$font-display", fontSize:96, fontWeight:"700", fill:"$slide-fg"})
row = Insert(s, {type:"frame", name:"Stats", width:"fill_container", gap:40})
for (const [v, l] of [["3.2×","faster design reviews"],["48%","fewer handoff bugs"],["12 min","from prompt to deck"]]) {
  c = Insert(row, {type:"frame", name:"Stat " + l, width:"fill_container", layout:"vertical", gap:12, padding:40, cornerRadius:24, fill:"#1A1D24"})
  Insert(c, {type:"text", name:"Value", content:v, fontFamily:"$font-display", fontSize:120, fontWeight:"700", fill:"$slide-accent"})
  Insert(c, {type:"text", name:"Label", content:l, fontFamily:"$font-display", fontSize:32, fill:"$slide-muted"})
}
TakeScreenshot([s])`,
  })
  console.log(result.split('\n').slice(0, 4).join('\n'))
  // Inserted children slide in (50 units low, fading in) for ~0.6 s.
  await sleep(150)
  await shot('05a-slide-in')
  await sleep(1200)
  await app.getByTestId('fit').click()
  await sleep(1200)
  await shot('05-after-mcp-agent')

  // Slides tab → Export deck to PDF: one page per top-level frame (3 template slides + the MCP one).
  await app.getByText('Slides').click()
  await sleep(800)
  await app.getByTestId('export-deck-pdf').click()
  await sleep(800)
  const pdfPath = (await app.getByTestId('toast').textContent()).replace(/^Exported /, '')
  const pdf = readFileSync(pdfPath, 'latin1')
  const pages = pdf.match(/\/Type ?\/Page\b(?!s)/g)?.length ?? 0
  if (!pdf.startsWith('%PDF-') || pages !== 4) throw new Error(`deck PDF ${pdfPath} has ${pages} pages, expected 4`)
  console.log('deck PDF:', pdfPath, `${pages} pages`)

  // Saved chats: a session file on disk is listed in the history dropdown, reopens, and can be removed.
  const sessions = join(data, 'sessions')
  const chatId = `e2e${Date.now().toString(36)}`
  const chatFile = join(sessions, `${chatId}.json`)
  const agent = { id: 'ag0e2e', name: 'Arunika', color: '#7C9CFF', parent: null }
  mkdirSync(sessions, { recursive: true })
  writeFileSync(chatFile, JSON.stringify({
    version: 1, id: chatId, title: 'E2E saved chat', updatedAt: Date.now(), agent, messages: [], session: null,
    events: [
      { type: 'agent', kind: 'user', agent, text: 'E2E saved chat', at: Date.now() - 3000 },
      { type: 'agent', kind: 'text_delta', agent, text: 'Restored from disk.', at: Date.now() - 2000 },
      { type: 'agent', kind: 'done', agent, durationMs: 1000, usage: {}, stopped: false, at: Date.now() - 1000 },
    ],
  }))
  await app.getByTestId('tab-agent').click()
  await app.getByTestId('agent-history').click()
  await app.getByTestId(`chat-${chatId}`).waitFor()
  await shot('07-chat-history')
  await app.getByTestId(`chat-${chatId}`).click()
  await sleep(500)
  // The prompt bubble and the agent's reply (a markdown block) are back.
  const reply = (await app.getByType('markdown').all()).filter((n) => (n.bounds?.height ?? 0) > 0)
  if (!(await app.getByText('E2E saved chat').count()) || reply.length !== 1) throw new Error('the saved chat did not reopen')
  await shot('08-restored-chat')

  // Per-tool permission: Claude Code asks through the MCP permission tool (as the restored chat's
  // agent); the card in the chat answers it.
  const asked = fetch(`http://127.0.0.1:${port}/mcp/${agent.id}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ jsonrpc: '2.0', id: 7, method: 'tools/call', params: { name: 'approve', arguments: { tool_name: 'Bash', input: { command: 'echo hi', description: 'Print a greeting' }, tool_use_id: 't1' } } }),
  }).then((r) => r.json())
  await app.getByTestId('permission-allow').waitFor()
  await shot('09-permission-request')
  await app.getByTestId('permission-allow').click()
  const verdict = JSON.parse((await asked).result.content[0].text)
  if (verdict.behavior !== 'allow' || verdict.updatedInput?.command !== 'echo hi') throw new Error(`permission answer: ${JSON.stringify(verdict)}`)
  await sleep(400)
  if (await app.getByTestId('permission-allow').count()) throw new Error('the answered permission card stayed')

  // The parallel-agents mode control sits on its own row, so it stays inside the composer card
  // (it used to share the "Agents" row and stick out past the card's right edge).
  await app.getByText('2×').click()
  await app.getByText('Side by side').waitFor()
  const field = (await app.getByTestId('agent-input').bounds())!
  const mode = (await app.getByText('Side by side').bounds())!
  if (mode.x + mode.width > field.x + field.width) throw new Error(`the "Side by side" label ends at ${mode.x + mode.width}, past the composer field's right edge ${field.x + field.width}`)
  await app.getByText('1×').click()
  await sleep(200)

  // Let it cook: pick 3 style variations, then back to "Don't iterate" (the choice persists).
  await app.getByTestId('agent-cook').click()
  await app.getByTestId('cook-menu').waitFor()
  await app.getByTestId('cook-count-3').click()
  await app.getByTestId('cook-mode-style').click()
  await sleep(200)
  await shot('10-let-it-cook')
  if (!(await app.getByText('3x').count())) throw new Error('Let it cook did not take 3 variants')
  await app.getByTestId('cook-mode-layout').click()
  await app.getByTestId('cook-count-1').click()
  await app.getByTestId('cook-menu').press('escape')
  await sleep(200)
  if (!(await app.getByText('1x').count()) || (await app.getByTestId('cook-menu').count())) throw new Error('Let it cook did not reset and close')
  // A dropdown closed by its own trigger reopens on the very next click (that click used to be swallowed).
  for (const [trigger, menu] of [['agent-cook', 'cook-menu'], ['agent-history', 'chat-history']]) {
    await app.getByTestId(trigger).click()
    await app.getByTestId(menu).waitFor()
    await app.getByTestId(trigger).click()
    await sleep(150)
    if (await app.getByTestId(menu).count()) throw new Error(`${trigger} did not close its dropdown`)
    await app.getByTestId(trigger).click()
    await sleep(150)
    if (!(await app.getByTestId(menu).count())) throw new Error(`${trigger} swallowed a quick reopen`)
    await app.getByTestId(menu).press('escape')
    await sleep(150)
  }
  await app.getByTestId('agent-history').click()
  await app.getByTestId(`chat-${chatId}`).hover()
  // Removal cannot be undone, so the first press only arms it.
  await app.getByTestId(`chat-remove-${chatId}`).click()
  await sleep(200)
  if (!existsSync(chatFile)) throw new Error('one press on remove deleted the chat without asking')
  await app.getByTestId(`chat-remove-confirm-${chatId}`).click()
  await sleep(300)
  if (existsSync(chatFile)) throw new Error(`removing the chat left ${chatFile}`)
  if (await app.getByType('markdown').count()) throw new Error('removing the open chat did not clear it')
  await app.getByTestId('agent-history').click()
  await sleep(300)
  if (await app.getByTestId('chat-history').count()) throw new Error('the history button did not close the dropdown')

  // An error says how to recover and stays until dismissed: a listed chat whose file vanished.
  const lostId = `e2elost${Date.now().toString(36)}`
  const lostFile = join(sessions, `${lostId}.json`)
  writeFileSync(lostFile, JSON.stringify({ version: 1, id: lostId, title: 'E2E lost chat', updatedAt: Date.now(), agent, messages: [], session: null, events: [] }))
  await app.getByTestId('agent-history').click()
  await app.getByTestId(`chat-${lostId}`).waitFor()
  rmSync(lostFile)
  await app.getByTestId(`chat-${lostId}`).click()
  await sleep(4200) // longer than a confirmation toast lives
  const errText = await app.getByTestId('toast').textContent().catch(() => '')
  if (!/Unable to open this chat.*Pick another chat/.test(errText)) throw new Error(`the error toast did not stay or name a way out: ${JSON.stringify(errText)}`)
  if (await app.getByTestId(`chat-${lostId}`).count()) throw new Error('the vanished chat stayed in the history list')
  await shot('11-error-toast')
  await app.getByTestId('toast-dismiss').click()
  await sleep(200)
  if (await app.getByTestId('toast').count()) throw new Error('dismissing the error toast left it on screen')
  if (await app.getByTestId('chat-history').count()) await app.getByTestId('chat-history').press('escape')

  await app.getByTestId('present').click()
  await sleep(1500)
  await shot('06-present')
  await app.getByTestId('presentation').press('escape')
  await sleep(500)
  console.log('e2e ok — screenshots in', out)
} finally {
  await app.close()
  rmSync(data, { recursive: true, force: true })
}
