// Save and reopen through the real window: `bun scripts/file-e2e.ts`
// Draw → Save (toolbar) → Home lists it under Recent → reopen shows the drawing; a second Save keeps the
// same file; a recent file that vanished from disk is no longer offered. No OS file dialogs involved.
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
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

  // A recent file deleted from disk is not offered any more.
  rmSync(saved)
  await app.getByTestId('home').click()
  await app.getByTestId('template-blank').waitFor()
  assert(!(await recent.count()), 'a vanished recent file is hidden')
  console.log('file-e2e ok')
} finally {
  await app?.close()
  if (saved) rmSync(saved, { force: true })
  rmSync(data, { recursive: true, force: true })
}
