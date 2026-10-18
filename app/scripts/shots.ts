// README screenshots into docs/screenshots: `bun scripts/shots.ts`
// Runs the app on a fresh data folder (no recent files or saved chats) and a live 2× agent run through
// the configured backend (Claude Code by default), so it takes a few minutes. Mid-run frames go to
// docs/screenshots/live/ to pick the agent-cursor shot from.
import { cpSync, existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { launch } from '@gpuix/react/automation'

const out = join(import.meta.dir, '..', '..', 'docs', 'screenshots')
const live = join(out, 'live')
mkdirSync(live, { recursive: true })
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

// Fresh data folder; reuse the font/icon caches so nothing is downloaded again.
const data = mkdtempSync(join(tmpdir(), 'nalarupa-shots-'))
const own = join(process.env.LOCALAPPDATA ?? '', 'Nalarupa')
for (const dir of ['cache', 'fonts']) if (existsSync(join(own, dir))) cpSync(join(own, dir), join(data, dir), { recursive: true })

const app = await launch({ command: 'bun', args: ['app.tsx'], cwd: join(import.meta.dir, '..'), env: { ...process.env, GPUIX_BACKGROUND: '1', NALARUPA_DATA: data } })
const shot = (name: string) => app.screenshot({ path: join(out, `${name}.png`) })
try {
  await app.getByTestId('template-slides').waitFor({ timeoutMs: 30000 })
  await sleep(800)
  await shot('home')

  // Slides template: the deck, the Slides tab, Present mode.
  await app.getByTestId('template-slides').click()
  await app.getByTestId('canvas').waitFor()
  await app.getByTestId('tab-slides').click()
  await app.getByTestId('fit').click()
  await sleep(1500)
  await shot('slides')
  await app.getByTestId('present').click()
  await app.getByTestId('presentation').waitFor()
  await sleep(1200)
  await shot('present')
  await app.call('keyDown', { key: 'escape' })
  await sleep(500)

  // Two agents side by side on an empty canvas.
  await app.getByTestId('home').click()
  await app.getByTestId('template-empty').click()
  await app.getByTestId('agent-input').waitFor()
  await app.getByText('2×').click()
  await app.getByText('Side by side').click()
  await app.getByTestId('agent-input').fill('Design two landing page hero sections (1200x720 each) for Kopi Nusantara, an Indonesian specialty coffee brand: one bold and dark, one light and warm with subtle batik-inspired accents. Each has a navigation bar, a headline, a short paragraph, two buttons and a product image. Keep each designer to two or three execute calls.')
  await app.getByTestId('agent-input').press('enter')
  const started = Date.now()
  for (let i = 1; Date.now() - started < 420_000; i++) {
    await sleep(4000)
    // Keep the growing frames in view (an empty canvas starts far zoomed out).
    if (i % 3 === 0) {
      await app.getByTestId('fit').click()
      await sleep(600)
    }
    await app.screenshot({ path: join(live, `agent-${String(i).padStart(2, '0')}.png`) })
    if ((await app.getByTestId('agent-send').count()) && !(await app.getByTestId('agent-stop').count()) && Date.now() - started > 15_000) break
  }
  await sleep(2500)
  await app.getByTestId('fit').click()
  await sleep(1500)
  await shot('agent')

  // The Design tab on something the agents made.
  const c = await app.getByTestId('canvas').bounds()
  await app.mouse.click({ x: c.x + c.width * 0.3, y: c.y + c.height * 0.45 })
  await app.getByTestId('tab-design').click()
  await sleep(1200)
  await shot('design')
  console.log(`shots ok in ${out} (agent run ${Math.round((Date.now() - started) / 1000)} s)`)
} finally {
  await app.close()
  rmSync(data, { recursive: true, force: true })
}
