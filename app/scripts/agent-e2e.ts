// Live agent run against the real window (uses the configured backend; Claude Code by default):
//   bun scripts/agent-e2e.ts [parallel] [mode]
// Captures a screenshot every few seconds into app/screenshots/agent-*.png while the agent designs.
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { launch } from '@gpuix/react/automation'

const parallel = process.argv[2] ?? '1'
const mode = process.argv[3] ?? 'split'
const out = join(import.meta.dir, '..', 'screenshots')
mkdirSync(out, { recursive: true })
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

const app = await launch({ command: 'bun', args: ['app.tsx'], cwd: join(import.meta.dir, '..'), env: { ...process.env, GPUIX_BACKGROUND: '1' } })
try {
  await app.getByTestId('template-empty').waitFor({ timeoutMs: 30000 })
  await app.getByTestId('template-empty').click()
  await app.getByTestId('agent-input').waitFor()
  if (parallel !== '1') {
    await app.getByText(`${parallel}×`).click()
    if (mode === 'side') await app.getByText('Side by side').click()
  }
  const prompt = parallel === '1'
    ? 'Design a pricing section for a note-taking app: one 1200x760 frame with a heading and three plan cards (Free, Pro, Team). Mark the frame placeholder:true while you work and clear it at the end. Keep it to two or three execute calls.'
    : 'Design two hero banners (1200x600 each) for a coffee brand: one bold and dark, one light and airy. Keep each designer to two execute calls.'
  await app.getByTestId('agent-input').fill(prompt)
  await app.getByTestId('agent-input').press('enter')
  const started = Date.now()
  let i = 0
  while (Date.now() - started < 240_000) {
    await sleep(5000)
    await app.screenshot({ path: join(out, `agent-${parallel}x-${String(++i).padStart(2, '0')}.png`) })
    const status = await app.getByTestId('agent-send').count()
    const stop = await app.getByTestId('agent-stop').count()
    if (status && !stop && Date.now() - started > 15_000) break
  }
  await sleep(2500)
  await app.getByTestId('fit').click()
  await sleep(1500)
  await app.screenshot({ path: join(out, `agent-${parallel}x-final.png`) })
  console.log(`agent run finished after ${Math.round((Date.now() - started) / 1000)}s; ${i} frames captured`)
} finally {
  await app.close()
}
