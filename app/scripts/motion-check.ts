// Reduced motion against the real window: `bun scripts/motion-check.ts`
// An external agent edits two far-apart spots through MCP. Normally its cursor glides to the second
// spot; with reduced motion (NALARUPA_REDUCED_MOTION=1 stands in for the OS setting) it is there at once.
import { join } from 'node:path'
import { launch } from '@gpuix/react/automation'

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))
const rpc = (port: number, body: object) =>
  fetch(`http://127.0.0.1:${port}/mcp`, { method: 'POST', headers: { 'content-type': 'application/json', accept: 'application/json, text/event-stream' }, body: JSON.stringify({ jsonrpc: '2.0', id: 1, ...body }) })
const alive = (p: number) => rpc(p, { method: 'tools/list' }).then(() => true, () => false)
const ports = async () => (await Promise.all(Array.from({ length: 16 }, (_, i) => 47700 + i).map(async (p) => ((await alive(p)) ? p : 0)))).filter(Boolean)

async function glide(reduced: boolean) {
  const before = new Set(await ports())
  const env = { ...process.env, GPUIX_BACKGROUND: '1', NALARUPA_REDUCED_MOTION: reduced ? '1' : '0' }
  const app = await launch({ command: 'bun', args: ['app.tsx'], cwd: join(import.meta.dir, '..'), env })
  try {
    await app.getByTestId('template-blank').waitFor({ timeoutMs: 30000 })
    await app.getByTestId('template-blank').click()
    await app.getByTestId('canvas').waitFor()
    await sleep(1500)
    const port = (await ports()).find((p) => !before.has(p))
    if (!port) throw new Error('no MCP port for the test window')
    const exec = (input: string) => rpc(port, { method: 'tools/call', params: { name: 'execute', arguments: { input } } }).then((r) => r.text())
    const frame = `Get(n => n.type === "frame" ? n.id : undefined)[0]`
    await exec(`Insert(${frame}, {type:"rectangle", name:"A", x:40, y:40, width:60, height:60, fill:"#22AA55"})`)
    await sleep(1500)
    const cursor = app.getByTestId('agent-cursor-external')
    const spotA = await cursor.bounds()
    const dist = (a: typeof spotA, b: typeof spotA) => Math.hypot(a.x - b.x, a.y - b.y)
    await exec(`Insert(${frame}, {type:"rectangle", name:"B", x:1300, y:780, width:60, height:60, fill:"#22AA55"})`)
    // The window sees the edit on its next 120 ms poll, so wait for the cursor to leave spot A (by more than
    // its 4 px idle wobble) before the first reading; a fixed delay can read it at A and take the jump for a glide.
    let early = spotA
    for (const end = Date.now() + 5000; dist(early, spotA) < 20; early = await cursor.bounds()) {
      if (Date.now() > end) throw new Error('motion-check: the cursor never left the first spot')
      await sleep(10)
    }
    await sleep(1500)
    return dist(await cursor.bounds(), early)
  } finally {
    await app.close()
  }
}

const normal = await glide(false)
await sleep(1500)
const reduced = await glide(true)
console.log(`cursor travel in the 1.5 s after it leaves the first spot: normal ${normal.toFixed(0)} px, reduced motion ${reduced.toFixed(0)} px`)
if (normal < 20) throw new Error('motion-check: the cursor did not glide with motion on (check the setup)')
if (reduced > 2) throw new Error('motion-check: the cursor still glides under reduced motion')
console.log('motion-check ok')
