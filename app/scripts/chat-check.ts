// Saved chat history through the core (no window): `bun scripts/chat-check.ts`
// The chat list, loading, deleting and clearing the Agent panel's history menu relies on.
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { randomBytes } from 'node:crypto'
import { delimiter, join } from 'node:path'
import { call } from '../src/core'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`chat-check failed: ${msg}`)
}
const throws = (fn: () => unknown, re: RegExp, msg: string) => {
  try {
    fn()
  } catch (e) {
    return assert(re.test((e as Error).message), `${msg}: wrong error "${(e as Error).message}"`)
  }
  assert(false, `${msg}: did not throw`)
}
// The core reads NALARUPA_DATA through libc, which `process.env` writes don't reach: rerun in a child
// with a fresh data folder so the chats written here never touch the real history.
const windows = process.platform === 'win32'
if (!process.argv.includes('--child')) {
  const data = mkdtempSync(join(tmpdir(), 'nalarupa-chat-'))
  // A stand-in `claude` that runs long and prints nothing: a running agent without a login (a shell script, so not on Windows).
  // When `replay.txt` exists next to it, it prints those lines first (`sleep <s>` pauses) and exits.
  const bin = join(data, 'bin')
  mkdirSync(bin)
  const stub = '#!/bin/bash\nf="$(dirname "$0")/replay.txt"\n' +
    'if [ -f "$f" ]; then while IFS= read -r l; do case "$l" in sleep*) sleep "${l#sleep }";; *) printf \'%s\\n\' "$l";; esac; done < "$f"; exit 0; fi\nexec sleep 60\n'
  if (!windows) writeFileSync(join(bin, 'claude'), stub, { mode: 0o755 })
  const PATH = `${bin}${delimiter}${process.env.PATH ?? ''}`
  const run = Bun.spawnSync([process.execPath, import.meta.path, '--child'], { env: { ...process.env, PATH, NALARUPA_DATA: data }, stdio: ['inherit', 'inherit', 'inherit'] })
  rmSync(data, { recursive: true, force: true })
  process.exit(run.exitCode ?? 1)
}
const data = process.env.NALARUPA_DATA!
const sessions = join(data, 'sessions')
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))
const until = async (fn: () => boolean, msg: string) => {
  for (let i = 0; i < 100 && !fn(); i++) await sleep(100)
  assert(fn(), msg)
}
type List = { current: string | null; chats: { id: string; title: string; updatedAt: number }[] }
const list = () => call<List>('chat_list')
const ids = () => list().chats.map((c) => c.id).sort().join()
const save = (id: string, title: string, updatedAt: number, events: object[] = []) => {
  mkdirSync(sessions, { recursive: true })
  const agent = { id: 'agfix', name: 'Fixture', color: '#ff3366' }
  writeFileSync(join(sessions, `${id}.json`), JSON.stringify({ version: 1, id, title, updatedAt, agent, events, messages: [], session: null }))
}

assert(call('init').dataDir === data, "core uses this run's NALARUPA_DATA")
call('new', { template: 'empty' })
assert(list().current === null && list().chats.length === 0, 'a fresh data folder has no chats')

// Saved chats list newest first, with their titles.
save('aaa111', 'Old pricing page', 1000)
save('bbb222', 'Newer hero section', 3000, [{ type: 'agent', kind: 'text', text: 'Built the hero.' }])
save('ccc333', 'Middle', 2000)
assert(list().chats.map((c) => c.id).join() === 'bbb222,ccc333,aaa111', `newest first: ${ids()}`)
assert(list().chats[0].title === 'Newer hero section', 'titles are listed')

// Files that are not chats are left out; a file untouched for more than 20 days is dropped from the history.
writeFileSync(join(sessions, 'notes.txt'), 'not a chat')
writeFileSync(join(sessions, 'damaged1.json'), '{not json')
assert(ids() === 'aaa111,bbb222,ccc333', 'non-chat and damaged files are not listed')
const stale = join(sessions, 'aaa111.json')
const old = new Date(Date.now() - 30 * 86_400_000)
utimesSync(stale, old, old)
assert(ids() === 'bbb222,ccc333' && !existsSync(stale), 'chats older than 20 days are deleted when listed')

// Loading returns the saved events and makes the chat current.
const loaded = call('chat_load', { id: 'bbb222' })
assert(loaded.id === 'bbb222' && loaded.title === 'Newer hero section' && loaded.events[0].text === 'Built the hero.', `load returns the transcript: ${JSON.stringify(loaded)}`)
assert(list().current === 'bbb222', 'the loaded chat is current')

// Load errors say what to do next; ids that are not plain alphanumerics never reach the file system.
throws(() => call('chat_load', { id: 'zzz999' }), /moved or deleted/, 'loading a missing chat')
throws(() => call('chat_load', { id: 'damaged1' }), /file is damaged/, 'loading a damaged chat')
throws(() => call('chat_load', { id: '../settings' }), /invalid chat id/, 'a path in the id')
throws(() => call('chat_load', { id: '' }), /invalid chat id|id required/, 'an empty id')
throws(() => call('chat_load'), /id required/, 'load without an id')
throws(() => call('chat_delete', { id: '..\\settings' }), /invalid chat id/, 'delete with a path in the id')
assert(list().current === 'bbb222', 'failed loads keep the current chat')

// Deleting another chat leaves the current one; deleting the current one starts a new conversation.
call('chat_delete', { id: 'ccc333' })
assert(ids() === 'bbb222' && list().current === 'bbb222', 'deleting another chat keeps the current one')
call('chat_delete', { id: 'ccc333' })
call('chat_delete', { id: 'bbb222' })
assert(ids() === '' && list().current === null, 'deleting the current chat clears it, and deleting twice is fine')

// A new conversation is a chat as soon as the first prompt is sent; while the agent runs the chat is protected.
let run = ''
if (!windows) {
  call('agent_send', { text: 'Design a landing page' })
  try {
    await until(() => call('state').agentRunning, 'the agent starts')
    run = list().current!
    assert(run && list().chats[0].id === run && list().chats[0].title === 'Design a landing page', 'the first prompt creates the chat, titled by it')
    save('ddd444', 'Another', 5000)
    throws(() => call('chat_load', { id: 'ddd444' }), /Stop the agent/, 'load while the agent runs')
    throws(() => call('chat_delete', { id: run }), /Stop the agent/, 'deleting the running chat')
    call('chat_delete', { id: 'ddd444' })
    assert(ids() === run, 'another chat can be deleted while the agent runs')
    save('eee555', 'Spare', 6000)
    assert(call('chat_clear') === false && call('chat_list').current === run && ids() === run, 'clear keeps the running chat and reports it')
  } finally {
    call('agent_stop')
  }
  await until(() => !call('state').agentRunning, 'the agent stops')
}

// Saving a long chat full of screenshots must not stall the engine: a worker times a cheap core call (it needs the
// same lock as render and poll) while a replayed agent run saves the chat after every tool call.
if (!windows) {
  call('chat_clear')
  const shot = randomBytes(520_000).toString('base64')
  const lines: string[] = []
  const calls = 96
  for (let i = 0; i < calls; i++) {
    const line = (o: object) => lines.push(JSON.stringify(o))
    line({ type: 'stream_event', session_id: 's', event: { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: `step ${i}` } } })
    line({ type: 'stream_event', event: { type: 'message_stop' } })
    line({ type: 'assistant', message: { content: [{ type: 'tool_use', id: `t${i}`, name: 'mcp__nalarupa__execute', input: { input: 'return 1' } }] } })
    line({ type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: `t${i}`, content: [{ type: 'text', text: 'ok' }, { type: 'image', source: { type: 'base64', media_type: 'image/png', data: shot } }] }] } })
    lines.push('sleep 0.1')
  }
  writeFileSync(join(data, 'bin', 'replay.txt'), lines.join('\n') + '\n')
  const worker = join(data, 'stall.ts')
  writeFileSync(worker, `import { call } from ${JSON.stringify(join(import.meta.dir, '../src/core'))}
let stop = false
self.onmessage = async (e) => {
  if (e.data === 'stop') return void (stop = true)
  let max = 0
  while (!stop) { const t = performance.now(); call('frames'); max = Math.max(max, performance.now() - t); await Bun.sleep(3) }
  postMessage(max)
}`)
  const w = new Worker(worker)
  const stall = new Promise<number>((r) => (w.onmessage = (e) => r(e.data)))
  w.postMessage('go')
  await sleep(300)
  call('agent_send', { text: 'Take many screenshots' })
  await until(() => call('state').agentRunning, 'the replayed agent starts')
  for (let i = 0; i < 600 && call('poll').state.agentRunning; i++) await sleep(100)
  w.postMessage('stop')
  const max = await stall
  w.terminate()
  assert(!call('state').agentRunning, 'the replayed agent finishes')
  // Before the writer moved off the engine lock the stall grew with the chat (130+ ms at this size); the clone alone stays far below.
  assert(max < 60, `saving a ${calls * 0.7} MB chat stalled the engine for ${max.toFixed(0)} ms`)
  const id = list().current!
  const text = readFileSync(join(sessions, `${id}.json`), 'utf8')
  assert(text.includes(`step ${calls - 1}`) && text.length > calls * 600_000, 'the last save holds the whole chat')
  call('chat_load', { id })
  assert(call('chat_clear') === true, 'the chat is cleared again')
}

// Once stopped, clear removes everything, including the current chat.
save('fff666', 'Last', 7000)
assert(call('chat_clear') === true && ids() === '' && list().current === null, 'clear removes every chat and the current one')
assert(!existsSync(join(sessions, `${run}.json`)) && !existsSync(join(sessions, 'fff666.json')), 'the chat files are gone')

console.log('chat-check ok')
process.exit(0)
