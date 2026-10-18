// Launch the compiled binary and screenshot it: `bun scripts/exe-check.ts`
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { launch } from '@gpuix/react/automation'

const exe = join(import.meta.dir, '..', 'dist', process.platform === 'win32' ? 'nalarupa.exe' : 'nalarupa')
const app = await launch({ command: exe, env: { ...process.env, GPUIX_BACKGROUND: '1' } })
try {
  await app.getByTestId('template-blank').waitFor({ timeoutMs: 30000 })
  await app.getByTestId('template-blank').click()
  await app.getByTestId('canvas').waitFor()
  await new Promise((r) => setTimeout(r, 1200))
  mkdirSync(join(import.meta.dir, '..', 'screenshots'), { recursive: true })
  await app.screenshot({ path: join(import.meta.dir, '..', 'screenshots', '07-compiled-binary.png') })
  console.log('compiled binary ok')
} finally {
  await app.close()
}
