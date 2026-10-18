// Accessibility check against the real window: `bun scripts/a11y-check.ts`
// Reads the Windows UI Automation tree (what Narrator and other screen readers see) on the home screen,
// in the editor with the agent panel open and in the Inspector, and fails on an interactive element with
// no name. Also drives the keyboard: Tab to a control, then Enter or Space must activate it.
// Windows only. Writes the full trees to screenshots/uia-*.txt.
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { launch } from '@gpuix/react/automation'

if (process.platform !== 'win32') {
  console.log('a11y-check: Windows only (UI Automation)')
  process.exit(0)
}
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))
const INTERACTIVE = new Set(['Button', 'CheckBox', 'TabItem', 'ListItem', 'TreeItem', 'ComboBox', 'MenuItem', 'Edit', 'Hyperlink', 'RadioButton', 'Slider'])
const app = await launch({ command: 'bun', args: ['app.tsx'], cwd: join(import.meta.dir, '..'), env: { ...process.env, GPUIX_BACKGROUND: '1' } })
const failures: string[] = []
const dump = (name: string, pid: number) => {
  const r = Bun.spawnSync(['powershell', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', join(import.meta.dir, 'uia-dump.ps1'), '-ProcId', String(pid)])
  const text = r.stdout.toString()
  mkdirSync(join(import.meta.dir, '..', 'screenshots'), { recursive: true })
  writeFileSync(join(import.meta.dir, '..', 'screenshots', `uia-${name}.txt`), text)
  const rows = text.split(/\r?\n/).filter(Boolean).map((l) => l.split('\t'))
  const interactive = rows.filter(([type]) => INTERACTIVE.has(type))
  const unnamed = interactive.filter(([, , label]) => !label?.trim())
  const counts: Record<string, number> = {}
  for (const [type] of interactive) counts[type] = (counts[type] ?? 0) + 1
  console.log(`${name}: ${rows.length} nodes, interactive ${JSON.stringify(counts)}, unnamed ${unnamed.length}`)
  for (const [type, focusable] of unnamed) failures.push(`${name}: unnamed ${type} (focusable=${focusable})`)
  return rows
}
try {
  const { pid } = (await app.call('initialize' as any, { protocolVersion: 1, client: 'a11y-check' } as any)) as { pid: number }
  await app.getByTestId('template-slides').waitFor({ timeoutMs: 30000 })
  await sleep(500)
  const home = dump('home', pid)
  for (const want of ['New design: A 1440 desktop frame', 'Open .nala / .pen…']) {
    if (!home.some(([, , label]) => label?.startsWith(want.split(':')[0]))) failures.push(`home: no control named like "${want}"`)
  }
  // Keyboard: two Tabs reach the "Slide deck" card; Enter opens it.
  // Keystrokes use GPUI syntax: modifiers go in the string ("shift-tab").
  const key = async (k: string) => {
    await app.call('keyDown' as any, { key: k, isHeld: false } as any)
    await app.call('keyUp' as any, { key: k } as any)
    await sleep(120)
  }
  await key('tab')
  await key('tab')
  await key('enter')
  await app.getByTestId('canvas').waitFor({ timeoutMs: 5000 }).catch(() => failures.push('keyboard: Enter on the Tab-focused "Slide deck" card did not open it'))
  if (!(await app.getByTestId('layer-sld01').count())) failures.push('keyboard: Enter opened something other than the slide deck')
  await sleep(1200)
  // Space: from the Agent tab, Shift+Tab reaches the Design tab; Space switches the panel.
  await app.getByTestId('tab-agent').click()
  await sleep(300)
  if (!(await app.getByTestId('agent-input').count())) failures.push('the Agent tab did not open the agent panel')
  await key('shift-tab')
  await key('space')
  if (await app.getByTestId('agent-input').count()) failures.push('keyboard: Space on the Tab-focused Design tab did not switch the panel')
  await app.getByTestId('tab-agent').click()
  await sleep(500)
  const editor = dump('editor-agent', pid)
  for (const want of ['Zoom in', 'Undo (Ctrl+Z)', 'Let it cook: off', 'Model: Default', 'Send', '2 agents', 'Chat history', 'Message the design agent', 'Design canvas']) {
    if (!editor.some(([, , label]) => label === want)) failures.push(`editor: no control named "${want}"`)
  }
  // Inspector: a selected frame shows its fields; every one needs a name.
  await app.getByTestId('layer-sld01').click()
  await app.getByTestId('tab-design').click()
  await sleep(500)
  const design = dump('editor-design', pid)
  for (const want of ['Layer name', 'Fill', 'Stroke color', 'Bring forward', 'Delete']) {
    if (!design.some(([, , label]) => label === want)) failures.push(`inspector: no control named "${want}"`)
  }
} finally {
  await app.close()
}
if (failures.length) {
  console.log(failures.join('\n'))
  process.exit(1)
}
console.log('a11y-check ok')
