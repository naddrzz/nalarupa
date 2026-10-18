// Model picker + fast mode through the Claude Code backend: `bun scripts/model-check.ts`
// Two tiny runs (Haiku 5.5, then Opus 5.5 with fast mode); the user's settings are restored after.
import { call } from '../src/core'

call('init')
call('new', { template: 'empty' })
const before = call<Record<string, unknown>>('settings_get')
const keys = ['agent_backend', 'agent_model', 'agent_fast'] as const
const run = async (model: string, fast: boolean) => {
  call('settings_set', { key: 'agent_backend', value: 'claude-code' })
  call('settings_set', { key: 'agent_model', value: model })
  call('settings_set', { key: 'agent_fast', value: fast || null })
  call('agent_reset')
  call('agent_send', { text: 'Reply with just OK. Do not call any tools.' })
  const started = Date.now()
  const seen: string[] = []
  for (;;) {
    await new Promise((r) => setTimeout(r, 1000))
    const { state, events } = call('poll')
    for (const e of events) if (e.type === 'agent' && (e.kind === 'text' || e.kind === 'error' || e.kind === 'done')) seen.push(`[${e.kind}] ${String(e.text ?? '').slice(0, 160)}`)
    if (!state.agentRunning && Date.now() - started > 3000) break
    if (Date.now() - started > 120000) { call('agent_stop'); seen.push('[timeout]'); break }
  }
  console.log(`${model}${fast ? ' + fast' : ''}:`, seen.join(' | ') || '(no events)')
  // Leave no test chat in the user's history.
  const current = call<{ current: string | null }>('chat_list').current
  if (current) call('chat_delete', { id: current })
  return !seen.some((s) => s.startsWith('[error]') || s === '[timeout]')
}
let ok = false
try {
  ok = (await run('claude-haiku-5-5', false)) && (await run('claude-opus-5-5', true))
} finally {
  for (const k of keys) call('settings_set', { key: k, value: before[k] ?? null })
}
process.exit(ok ? 0 : 1)
