// Built-in agent check through the Claude Code backend: `bun scripts/agent-check.ts`
import { call } from '../src/core'

call('init')
call('new', { template: 'empty' })
call('settings_set', { key: 'agent_backend', value: 'claude-code' })
call('agent_send', { text: 'Create one 600x400 frame named "Hello Card" with a vertical layout, padding 40, a white fill, and a 48px bold text "Hello from Nalarupa" in #111111 inside it. Keep it minimal; one execute call is enough.' })
const started = Date.now()
for (;;) {
  await new Promise((r) => setTimeout(r, 1000))
  const { state, events } = call('poll')
  for (const e of events) if (e.type === 'agent') console.log(`[${e.kind}] ${String(e.text).slice(0, 160)}`)
  if (!state.agentRunning && Date.now() - started > 3000) break
  if (Date.now() - started > 240000) { call('agent_stop'); break }
}
const frames = call('frames')
console.log('frames:', JSON.stringify(frames.map((f: any) => [f.name, Math.round(f.w), Math.round(f.h)])))
process.exit(frames.some((f: any) => f.name === 'Hello Card') ? 0 : 1)
