// Save and reopen through the real window: `bun scripts/file-e2e.ts`
// Draw → Save (toolbar) → Home lists it under Recent → reopen shows the drawing; a second Save keeps the
// same file; Home asks before it replaces unsaved edits (Cancel / Escape / Back, Save, a failed Save, Don't save); a recent file that vanished from disk is no longer offered; a very long recent name is cut inside its row; Home and the agent's empty state scroll in a minimum-height window (Linux, where the window can be resized). No OS file dialogs involved.
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, writeFileSync, renameSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, join } from 'node:path'
import { launch } from '@gpuix/react/automation'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`file-e2e failed: ${msg}`)
}
// Poll a condition (GPUIX waitFor needs exactly one match; files land asynchronously).
const until = async (ok: () => unknown, msg: string, ms = 5000) => {
  for (const t = Date.now(); !(await ok()); await Bun.sleep(100)) assert(Date.now() - t < ms, msg)
}
// Fresh data folder: Recent starts empty and lists only what this run saves.
const data = mkdtempSync(join(tmpdir(), 'nalarupa-file-'))
let saved = ''
let app: Awaited<ReturnType<typeof launch>> | undefined
try {
  // The home folder too (HOME, USERPROFILE on Windows): untitled documents save under ~/Documents/Nalarupa,
  // which must stay out of the real home.
  app = await launch({ command: 'bun', args: ['app.tsx'], cwd: join(import.meta.dir, '..'), env: { ...process.env, GPUIX_BACKGROUND: '1', NALARUPA_DATA: data, HOME: data, USERPROFILE: data } })
  await app.getByTestId('template-blank').waitFor({ timeoutMs: 30000 })
  assert(await app.getByText('No recent files yet.').count(), 'a fresh data folder has no recent files')
  await app.getByTestId('template-blank').click()
  await app.getByTestId('canvas').waitFor()

  // Draw a rectangle inside the Desktop frame.
  const c = await app.getByTestId('canvas').bounds()
  await app.getByTestId('tool-rectangle').click()
  await app.mouse.drag({ x: c.x + c.width / 2 - 60, y: c.y + c.height / 2 - 40 }, { x: c.x + c.width / 2 + 60, y: c.y + c.height / 2 + 40 }, { steps: 5 })
  await app.getByTestId('selection-box').waitFor()
  await app.getByTestId('layers').getByText('Rectangle').waitFor()

  // Save from the toolbar: an untitled document goes to Documents/Nalarupa and the toast names the file.
  await app.getByTestId('save').click()
  await app.getByText('Saved ').waitFor()
  saved = (await app.getByText('Saved ').textContent()).replace(/^.*Saved /, '').trim()
  assert(saved.endsWith('.nala') && existsSync(saved), `save wrote the file (${saved})`)
  const doc = JSON.parse(readFileSync(saved, 'utf8'))
  assert(JSON.stringify(doc).includes('"rectangle"'), 'the saved file contains the rectangle')

  // A second Save, after another edit, writes the same file, not a numbered copy.
  await app.getByTestId('canvas').press('ctrl-c')
  await app.getByTestId('canvas').press('ctrl-v')
  await app.getByTestId('save').click()
  await until(() => (readFileSync(saved, 'utf8').match(/"rectangle"/g) ?? []).length === 2, 'the second save updated the same file with the pasted rectangle')
  assert(!existsSync(saved.replace(/\.nala$/, '-1.nala')), 'no numbered copy')

  // Home lists it under Recent; opening it brings the drawing back.
  await app.getByTestId('home').click()
  const recent = app.getByTestId(`recent-${basename(saved)}`)
  await recent.waitFor()
  await recent.click()
  await app.getByTestId('canvas').waitFor()
  await app.getByTestId('layer-toggle-frm01').click() // a reopened document starts with the Desktop frame collapsed
  await until(async () => (await app.getByTestId('layers').getByText('Rectangle').count()) === 2, 'reopening shows both saved rectangles')

  // An unsaved edit is never replaced silently: Home asks first, and Cancel keeps the document reachable.
  const rects = async () => app!.getByTestId('layers').getByText('Rectangle').count()
  const rectsAre = (n: number, msg: string) => until(async () => (await rects()) === n, msg)
  const toggleFrame = () => app!.getByTestId('layer-toggle-frm01').click()
  const prompt = app.getByTestId('unsaved-prompt')
  await app.getByTestId('canvas').press('ctrl-v')
  await rectsAre(3, 'pasting adds a third rectangle') // the layer list and the dirty flag update in the same poll
  await app.getByTestId('home').click()
  await app.getByTestId('template-empty').click()
  await prompt.waitFor()
  await app.getByTestId('unsaved-cancel').click()
  await until(async () => !(await prompt.count()), 'Cancel closes the prompt')
  await app.getByTestId('template-empty').click()
  await prompt.waitFor()
  await prompt.press('escape')
  await until(async () => !(await prompt.count()), 'Escape closes the prompt')
  await app.getByTestId('home-back').click()
  await app.getByTestId('canvas').waitFor()
  await rectsAre(3, 'Cancel and Back keep the unsaved rectangle')

  // A failed Save (a folder stands where the file should be) shows an error and keeps the prompt, and the document, in place.
  const aside = `${saved}.bak`
  renameSync(saved, aside)
  mkdirSync(saved)
  await app.getByTestId('home').click()
  await app.getByTestId('template-empty').click()
  await prompt.waitFor()
  await app.getByTestId('unsaved-save').click()
  await app.getByText('Unable to save the document').waitFor()
  assert(await prompt.count(), 'a failed Save keeps the prompt open')
  rmSync(saved, { recursive: true })
  renameSync(aside, saved)

  // Save writes the open file first, then the new document replaces it.
  await app.getByTestId('unsaved-save').click()
  await app.getByTestId('canvas').waitFor()
  await until(() => (readFileSync(saved, 'utf8').match(/"rectangle"/g) ?? []).length === 3, 'Save in the prompt wrote the third rectangle')
  await rectsAre(0, 'the empty canvas replaced the document')

  // A saved document opens without a prompt; Don't save drops the edit and leaves the file as it was.
  await app.getByTestId('home').click()
  await recent.click()
  await app.getByTestId('canvas').waitFor()
  assert(!(await prompt.count()), 'a clean document is replaced without a prompt')
  await toggleFrame()
  await rectsAre(3, 'the saved file reopens with three rectangles')
  await app.getByTestId('canvas').press('ctrl-v')
  await rectsAre(4, 'pasting adds a fourth rectangle')
  await app.getByTestId('home').click()
  await recent.click()
  await prompt.waitFor()
  await app.getByTestId('unsaved-discard').click()
  await app.getByTestId('canvas').waitFor()
  await toggleFrame()
  await rectsAre(3, "Don't save reopened the file without the fourth rectangle")
  assert((readFileSync(saved, 'utf8').match(/"rectangle"/g) ?? []).length === 3, "Don't save left the file untouched")

  // A recent file deleted from disk is not offered any more.
  rmSync(saved)
  await app.getByTestId('home').click()
  await app.getByTestId('template-blank').waitFor()
  assert(!(await recent.count()), 'a vanished recent file is hidden')

  // A very long recent name is cut inside its row, and at the minimum window height (1100×680, sway only) Home and the agent's empty state scroll, so nothing stays out of reach.
  const dir = process.env.XDG_RUNTIME_DIR
  const sock = process.platform === 'linux' && dir ? readdirSync(dir).find((f) => f.startsWith('sway-ipc')) : undefined
  {
    await app.close()
    const docs = mkdtempSync(join(tmpdir(), 'nalarupa-recent-'))
    const long = `${'Quarterly planning deck for the entire organisation including every single department and subsidiary '.repeat(2).trim()}.nala`
    const paths = Array.from({ length: 8 }, (_, i) => join(docs, i ? `project-${i}.nala` : long))
    paths.forEach((p) => writeFileSync(p, '{}'))
    writeFileSync(join(data, 'settings.json'), JSON.stringify({ recent: paths }))
    try {
      app = await launch({ command: 'bun', args: ['app.tsx'], cwd: join(import.meta.dir, '..'), env: { ...process.env, GPUIX_BACKGROUND: '1', NALARUPA_DATA: data, HOME: data, USERPROFILE: data } })
      await app.getByTestId('template-blank').waitFor({ timeoutMs: 30000 })
      if (sock) {
        await Bun.$`swaymsg -s ${join(dir!, sock)} [app_id=".*"] resize set 1100 680`.quiet()
        await Bun.sleep(1500)
      }
      const row = await app.getByTestId(`recent-${long}`).bounds()
      const [name, where] = (await app.getByTestId(`recent-${long}`).getByType('text').all()).map((n) => n.bounds!)
      assert(name.width <= 440, `a long recent name is capped at 440 px (${name.width} > 440)`)
      assert(name.x + name.width <= row.x + row.width, `a long recent name stays inside its row (${name.x + name.width} > ${row.x + row.width})`)
      assert(where.width > 40, `the path keeps room beside a long name (${where.width} px)`)
      if (sock) {
        const home = app.getByTestId('home-scroll')
        const last = app.getByTestId('recent-project-7.nala')
        // Bounds follow the scroll offset, so compare with fixed limits: the window's bottom edge, the composer's top edge.
        const above = async (el: typeof last, limit: number) => { const b = await el.bounds(); return b.y >= 0 && b.y + b.height <= limit }
        assert(!(await above(last, 680)), 'the eighth recent file starts below the 680 px window (the check needs a window this short)')
        await until(async () => { await home.wheel(0, -400); return above(last, 680) }, 'scrolling Home reaches the eighth recent file')
        await Bun.sleep(300)
        await app.getByTestId('template-blank').click()
        await app.getByTestId('canvas').waitFor()
        await until(async () => (await app!.getByTestId('agent-empty').count()) > 0, 'the agent shows its empty state')
        const empty = app.getByTestId('agent-empty')
        const composer = (await app.getByTestId('agent-input').bounds()).y - 18 // the composer's card starts about 18 px above its input
        const hint = app.getByText('Each agent gets a cursor')
        await until(async () => { await empty.wheel(0, -400); return above(hint, composer) }, "scrolling the agent's empty state reaches its hint")
      } else console.log('file-e2e: skipped the minimum-window scroll check (needs sway)')
    } finally {
      rmSync(docs, { recursive: true, force: true })
    }
  }
  console.log('file-e2e ok')
} finally {
  await app?.close()
  if (saved) rmSync(saved, { recursive: true, force: true })
  rmSync(data, { recursive: true, force: true })
}
