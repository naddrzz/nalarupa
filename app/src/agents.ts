// Agent store: turns core events into chat items and
// canvas activity (cursors + flashes). Mutable module state; components re-read it
// when App bumps its tick.

export type Profile = { id: string; name: string; color: string; parent?: string | null }

/** What wrote a reply: the backend and model (null = the CLI's own default) at the time the turn started. */
export type Author = { backend: string; model: string | null }

export type ChatItem =
  | { kind: 'user'; id: number; text: string }
  | { kind: 'queued'; id: number; text: string }
  | { kind: 'assistant'; id: number; agent: Profile; text: string; streaming: boolean; header: boolean; by?: Author }
  | { kind: 'thinking'; id: number; agent: Profile; text: string; startedAt: number; endedAt?: number }
  | {
      kind: 'tool'; id: number; toolId: string; agent: Profile; name: string; input?: any; output?: string
      images?: string[]; error?: boolean; lines?: number; running: boolean
    }
  | { kind: 'note'; id: number; text: string }
  | { kind: 'error'; id: number; agent?: Profile; text: string }
  | { kind: 'usage'; id: number; agent: Profile; durationMs: number; usage: any; stopped: boolean }
  | { kind: 'external'; id: number; text: string; error?: boolean }
  /** The agent waits for the user to allow a tool (permission card). */
  | { kind: 'permission'; id: number; reqId: string; agent: Profile; tool: string; input: any }

export type Todo = { content: string; status: 'pending' | 'in_progress' | 'completed' | string; activeForm?: string }

/** One canvas flash: ring + scan band on a node the agent touched. */
export type Flash = { nodeId: string; start: number; color: string; longHold: boolean }

/** The AI Pen drawing a generated SVG into a frame: follows the newest point (document units). */
export type PenState = { x: number; y: number; tx: number; ty: number; appearAt: number }

/** One agent cursor, positions in document units. */
export type CursorState = {
  profile: Profile
  x: number; y: number; tx: number; ty: number
  appearAt: number; lastActivity: number; wobble: number; phase: number
}

let nextId = 1
export const store = {
  items: [] as ChatItem[],
  profiles: new Map<string, Profile>(),
  running: new Set<string>(),
  /** Backend and model of each agent's current turn, from its `start` event. */
  authors: new Map<string, Author>(),
  todos: [] as Todo[],
  /** The agent that last wrote `todos`; only its run ending settles the plan. */
  todosOwner: '',
  cursors: new Map<string, CursorState>(),
  /** AI Pen cursors by the frame they draw into. */
  pens: new Map<string, PenState>(),
  flashes: [] as Flash[],
  /** Latest assistant text per agent for the canvas pills: text + time it was said. */
  pills: new Map<string, { text: string; at: number }>(),
  /** Pending flash requests whose node boxes the canvas still has to resolve. */
  activity: [] as { agent: string; ids: string[]; op: string }[],
  /** Camera center in doc units, so a fresh cursor can spawn near what the user sees. */
  viewCenter: { x: 0, y: 0 },
}

/**
 * `data:` URL for a base64 tool-result image. Our screenshots are PNG, but Claude Code re-encodes
 * large ones as JPEG, and GPUIX refuses to decode data under the wrong MIME ("img: load failed").
 */
export function imageSrc(b64: string): string {
  const mime = b64.startsWith('/9j/') ? 'image/jpeg' : b64.startsWith('UklGR') ? 'image/webp' : b64.startsWith('R0lGOD') ? 'image/gif' : 'image/png'
  return `data:${mime};base64,${b64}`
}

// Canvas presence timings, ms.
export const FADE_IN = 200
export const FADE_DELAY = 5000
export const FADE_DUR = 1500
export const GONE = FADE_DELAY + FADE_DUR
export const PILL_MS = 3000

/**
 * Whether the agent layer must repaint now. Normally any agent presence animates every frame. Under
 * reduced motion only fades and the pill expiry change on their own; everything else moves on events,
 * which already re-render the layer. `painted` is when the layer last rendered: a pill that was still
 * showing then needs one more render at or after its expiry, however late the next tick lands.
 */
export function needsFrame(now: number, reduced: boolean, liveCount: number, painted = now): boolean {
  const pills = [...store.pills.values()].some((p) => Math.min(now, painted) - p.at < PILL_MS)
  if (store.activity.length > 0 || store.flashes.length > 0 || pills) return true
  if (!reduced) {
    return liveCount > 0 || store.pens.size > 0 || [...store.cursors.values()].some((c) => store.running.has(c.profile.id) || now - c.lastActivity < GONE)
  }
  return [...store.pens.values()].some((p) => now - p.appearAt < FADE_IN)
    || [...store.cursors.values()].some((c) => {
      const idle = now - c.lastActivity
      return now - c.appearAt < FADE_IN || (!store.running.has(c.profile.id) && idle > FADE_DELAY && idle < GONE)
    })
}

export const FALLBACK: Profile = { id: 'agent', name: 'Agent', color: '#808080' }
export const PRESENCE = '#607dff'

export function profileOf(id: string): Profile {
  return store.profiles.get(id) ?? (id === 'external' ? { id, name: 'External agent', color: PRESENCE } : FALLBACK)
}

function lastOf(agent: string, kinds: ChatItem['kind'][]): ChatItem | undefined {
  for (let i = store.items.length - 1; i >= 0; i--) {
    const it = store.items[i]
    if ('agent' in it && it.agent?.id === agent && kinds.includes(it.kind)) return it
    if ('agent' in it && it.agent?.id === agent) return undefined
  }
  return undefined
}

function endStreaming(agent: string, now: number) {
  for (const it of store.items) {
    if (it.kind === 'assistant' && it.agent.id === agent) it.streaming = false
    if (it.kind === 'thinking' && it.agent.id === agent && it.endedAt === undefined) it.endedAt = now
  }
}

/** Whether the agent's cursor has faded off the canvas; a running agent's cursor never does. */
export function cursorFaded(agent: string, c: CursorState | undefined, now: number): boolean {
  return !c || (!store.running.has(agent) && now - c.lastActivity > GONE)
}

/** Wake (or spawn) the agent's cursor without moving it. */
export function poke(p: Profile, now: number) {
  const c = store.cursors.get(p.id)
  if (!c) {
    const x = store.viewCenter.x + (Math.random() - 0.5) * 1000
    const y = store.viewCenter.y + (Math.random() - 0.5) * 1000
    store.cursors.set(p.id, { profile: p, x, y, tx: x, ty: y, appearAt: now, lastActivity: now, wobble: 0, phase: Math.random() * Math.PI * 2 })
    return
  }
  if (cursorFaded(p.id, c, now)) c.appearAt = now
  c.lastActivity = now
}

/** Apply core events; returns true when anything visible changed. */
export function applyEvents(events: any[], now: number): boolean {
  let changed = false
  for (const ev of events) {
    if (ev.type === 'agent_activity') {
      store.activity.push({ agent: ev.agent, ids: ev.ids ?? [], op: ev.op })
      changed = true
      continue
    }
    if (ev.type === 'pen') {
      const pen = store.pens.get(ev.id)
      if (pen) Object.assign(pen, { tx: ev.x, ty: ev.y })
      else store.pens.set(ev.id, { x: ev.x, y: ev.y, tx: ev.x, ty: ev.y, appearAt: now })
      changed = true
      continue
    }
    if (ev.type === 'pen_end') {
      store.pens.delete(ev.id)
      changed = true
      continue
    }
    if (ev.type !== 'agent') continue
    changed = true
    const p: Profile | undefined = ev.agent ? { ...ev.agent } : undefined
    if (p) store.profiles.set(p.id, p)
    const aid = p?.id ?? 'agent'
    switch (ev.kind) {
      case 'user':
        store.items.push({ kind: 'user', id: nextId++, text: ev.text })
        // The core runs the whole queue as one turn, its texts joined by a blank line; any other user message (the cook's "Iterating…") leaves it queued.
        if (ev.text === store.items.flatMap((i) => (i.kind === 'queued' ? [i.text] : [])).join('\n\n')) store.items = store.items.filter((i) => i.kind !== 'queued')
        break
      case 'queued':
        store.items.push({ kind: 'queued', id: nextId++, text: ev.text })
        break
      case 'dequeued':
        store.items = store.items.filter((i) => i.kind !== 'queued')
        store.items.push({ kind: 'note', id: nextId++, text: 'Queued message discarded.' })
        break
      case 'start':
        if (p) poke(p, now) // before running.add: a cursor that faded while idle fades in again
        store.running.add(aid)
        if (typeof ev.backend === 'string') store.authors.set(aid, { backend: ev.backend, model: ev.model ?? null })
        else store.authors.delete(aid) // a chat saved before replies recorded their model
        break
      case 'text_delta': {
        const last = lastOf(aid, ['assistant', 'thinking', 'tool'])
        if (last?.kind === 'assistant' && last.streaming) last.text += ev.text
        else if (String(ev.text ?? '').trim()) {
          endStreaming(aid, now)
          const prev = store.items[store.items.length - 1]
          const header = !prev || prev.kind === 'user' || !('agent' in prev) || prev.agent?.id !== aid
          store.items.push({ kind: 'assistant', id: nextId++, agent: p ?? FALLBACK, text: ev.text, streaming: true, header, by: store.authors.get(aid) })
        }
        const cur = lastOf(aid, ['assistant'])
        if (cur?.kind === 'assistant') store.pills.set(aid, { text: cur.text, at: now })
        break
      }
      case 'thinking_delta': {
        const last = lastOf(aid, ['thinking', 'assistant', 'tool'])
        if (last?.kind === 'thinking' && last.endedAt === undefined) last.text += ev.text
        else {
          endStreaming(aid, now)
          store.items.push({ kind: 'thinking', id: nextId++, agent: p ?? FALLBACK, text: ev.text, startedAt: now })
          if (p) poke(p, now)
        }
        break
      }
      case 'tool_start':
        endStreaming(aid, now)
        if (!store.items.some((i) => i.kind === 'tool' && i.toolId === ev.id)) {
          store.items.push({ kind: 'tool', id: nextId++, toolId: ev.id, agent: p ?? FALLBACK, name: ev.name, running: true })
        }
        if (p) poke(p, now)
        break
      case 'tool_progress': {
        const t = store.items.find((i) => i.kind === 'tool' && i.toolId === ev.id)
        if (t?.kind === 'tool') t.lines = ev.lines
        break
      }
      case 'tool_done': {
        let t = store.items.find((i) => i.kind === 'tool' && i.toolId === ev.id)
        if (!t) {
          t = { kind: 'tool', id: nextId++, toolId: ev.id, agent: p ?? FALLBACK, name: ev.name, running: false }
          store.items.push(t)
        }
        if (t.kind === 'tool') Object.assign(t, { input: ev.input, output: ev.output, images: ev.images, error: ev.error, running: false, name: ev.name || t.name })
        break
      }
      case 'message_end':
        endStreaming(aid, now)
        break
      case 'todos':
        store.todos = Array.isArray(ev.todos) ? ev.todos : []
        store.todosOwner = aid
        break
      case 'note':
        store.items.push({ kind: 'note', id: nextId++, text: ev.text })
        break
      case 'error':
        store.items.push({ kind: 'error', id: nextId++, agent: p, text: ev.text })
        break
      case 'external':
        store.items.push({ kind: 'external', id: nextId++, text: ev.text, error: ev.error === true })
        break
      case 'permission':
        store.items.push({ kind: 'permission', id: nextId++, reqId: ev.id, agent: p ?? FALLBACK, tool: ev.tool, input: ev.input ?? {} })
        if (p) poke(p, now)
        break
      case 'permission_done':
        store.items = store.items.filter((i) => i.kind !== 'permission' || i.reqId !== ev.id)
        break
      case 'done': {
        endStreaming(aid, now)
        store.running.delete(aid)
        for (const it of store.items) if (it.kind === 'tool' && it.agent.id === aid && it.running) Object.assign(it, { running: false, error: true, output: 'Tool call cancelled' })
        // A failed run ends the agent as a Stop does: its unfinished plan must not keep reading as live.
        if (store.todosOwner === aid && (ev.stopped || failedRun(aid))) store.todos = store.todos.map((t) => (t.status === 'completed' ? t : { ...t, status: 'cancelled' }))
        store.items.push({ kind: 'usage', id: nextId++, agent: p ?? FALLBACK, durationMs: ev.durationMs ?? 0, usage: ev.usage ?? {}, stopped: !!ev.stopped })
        const c = store.cursors.get(aid)
        if (c) c.lastActivity = now
        store.pills.delete(aid)
        break
      }
    }
  }
  return changed
}

/** Whether the agent's current run (since its last usage row) reported an error. */
function failedRun(aid: string): boolean {
  for (let i = store.items.length - 1; i >= 0; i--) {
    const it = store.items[i]
    if (it.kind === 'usage' && it.agent.id === aid) return false
    if (it.kind === 'error' && (it.agent?.id ?? 'agent') === aid) return true
  }
  return false
}

export function resetChat() {
  store.items = []
  store.todos = []
  store.todosOwner = ''
  store.pills.clear()
  store.profiles.clear()
  store.running.clear()
  store.authors.clear()
  store.cursors.clear()
}

/** Agent presence on the canvas is in document units: a new or opened document starts without the old one's cursors and flashes. */
export function clearPresence() {
  store.cursors.clear()
  store.flashes = []
  store.activity = []
}

/** The newest item that is real chat content: a usage row or a note is not a "later message" for the typewriter. */
export function lastContentId(items: ChatItem[]): number | undefined {
  for (let i = items.length - 1; i >= 0; i--) if (items[i].kind !== 'usage' && items[i].kind !== 'note') return items[i].id
}

/** A card with an animated ellipsis (running tool, live thinking). */
export function spinning(it: ChatItem): boolean {
  return (it.kind === 'tool' && it.running) || (it.kind === 'thinking' && it.endedAt === undefined)
}

/** An item that still changes in place while the agent works, so its row must re-render on a store change. */
export function changing(it: ChatItem): boolean {
  return spinning(it) || (it.kind === 'assistant' && it.streaming)
}

/** Agent bar pills: the running agents first, then the finished ones, at most `max`; `more` counts the rest. */
export function barAgents(max = 8) {
  const all = [...store.profiles.values()]
  const running = all.filter((a) => store.running.has(a.id))
  const shown = [...running, ...all.filter((a) => !store.running.has(a.id))]
  return { all, running, shown: shown.slice(0, max), more: Math.max(0, shown.length - max) }
}

/** Rebuild a saved chat (core `chat_load`): replay its events at their recorded times. */
export function restoreChat(events: any[]) {
  resetChat()
  for (const ev of events) applyEvents([ev], ev.at ?? 0)
  // A chat saved mid-run has nothing streaming, running or queued any more.
  store.items = store.items.filter((i) => i.kind !== 'queued')
  for (const it of store.items) {
    if (it.kind === 'assistant') it.streaming = false
    if (it.kind === 'thinking') it.endedAt ??= it.startedAt
    if (it.kind === 'tool' && it.running) Object.assign(it, { running: false, error: true, output: 'Tool call cancelled' })
  }
  store.running.clear()
  store.cursors.clear()
  store.pills.clear()
}
