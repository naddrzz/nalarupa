// Agent chat panel: agent headers, typewriter text,
// collapsible thinking, tool cards ("Designing…" → "Designed"), plan panel,
// usage rows, queued prompts, parallel agents.
import { useEffect, useMemo, useRef, useState } from 'react'
import { call, tryCall } from './core'
import { barAgents, imageSrc, profileOf, resetChat, restoreChat, store, type ChatItem } from './agents'
import { useGpuixRequired, type PublicInstance } from '@gpuix/react'
import * as SelectPrimitive from '@gpuix/react/select'
import { C, FONT, INDICATOR_TICK_MS, MONO, REDUCED_MOTION, ellipsisAt, pulseAt } from './theme'
import { Button, Field, FocusRing, Icon, IconButton, Pressable, Row, Segmented, setKeyboardFocus, T, Toggle, useKeyboardFocusId, useRing, useTrigger, type Toast } from './ui'

// Suggestion set; the empty chat shows 6 random picks of these 12.
const SUGGESTIONS = [
  'a landing page for a specialty coffee roastery in Bandung',
  'a mobile app that tracks batik workshops and orders',
  'a 6-slide pitch deck for a plant-care startup',
  'an analytics dashboard for a solar farm',
  'a portfolio site for an architecture studio',
  'a mobile onboarding flow for a language-learning app',
  'a pricing page for a developer tool',
  'a recipe app with a cozy, editorial look',
  'a ticketing site for an indie film festival',
  'a fintech card dashboard in a dark theme',
  'a travel itinerary app for island hopping',
  'a newsletter signup page for a design magazine',
]

const LABELS: Record<string, [string, string]> = {
  execute: ['Designing', 'Designed'],
  get_editor_state: ['Getting editor state', 'Got editor state'],
  get_style: ['Checking styles', 'Checked styles'],
  read_guide: ['Reading guide', 'Read guide'],
  spawn_agents: ['Starting agents', 'Started agents'],
  open_document: ['Opening document', 'Opened document'],
  ToolSearch: ['Setting up tools', 'Set up tools'],
  TodoWrite: ['Updating plan', 'Updated plan'],
}

function toolLabel(name: string, running: boolean) {
  const l = LABELS[name]
  if (l) return running ? l[0] : l[1]
  const short = name.split('__').pop() ?? name
  return short.charAt(0).toUpperCase() + short.slice(1)
}

function duration(ms: number) {
  const s = Math.max(1, Math.round(ms / 1000))
  return s >= 60 ? `${Math.floor(s / 60)}m ${s % 60}s` : `${s}s`
}

/** Re-render on an interval while `active` (animated ellipsis, typewriter). */
function useTick(ms: number, active: boolean) {
  const [, set] = useState(0)
  useEffect(() => {
    if (!active) return
    const t = setInterval(() => set((n) => (n + 1) % 1_000_000), ms)
    return () => clearInterval(t)
  }, [ms, active])
}

// Under reduced motion the ellipsis stays still and replies appear whole (no typewriter).
const dots = () => (REDUCED_MOTION ? '…' : ellipsisAt(performance.now()))

// Typewriter: ~5 words per 16 ms frame, instant once a later message exists.
const revealed = new Map<number, number>()
function visibleText(it: Extract<ChatItem, { kind: 'assistant' }>, isLast: boolean) {
  const words = it.text.split(/(\s+)/)
  const shown = REDUCED_MOTION ? words.length : Math.min(words.length, (revealed.get(it.id) ?? 0) + 10)
  revealed.set(it.id, isLast ? shown : words.length)
  return { text: words.slice(0, isLast ? shown : words.length).join(''), done: !isLast || shown >= words.length }
}

export function AgentPanel({ tick, running, mcpUrl, toast, onSent }: { tick: number; running: boolean; mcpUrl: string; toast: Toast; onSent?: () => void }) {
  const [draft, setDraft] = useState('')
  const [settings, setSettings] = useState(false)
  const [parallel, setParallel] = useState('1')
  const [mode, setMode] = useState<'split' | 'side'>('split')
  const [open, setOpen] = useState<Set<number>>(new Set())
  const [planOpen, setPlanOpen] = useState(true)
  const [, setChatVersion] = useState(0)
  const picks = useRef(SUGGESTIONS.slice().sort(() => Math.random() - 0.5).slice(0, 6))
  // "Let it cook" settings (design count, iterate mode) persist in the app settings.
  const saved = useMemo(() => tryCall<Record<string, unknown>>('settings_get') ?? {}, [])
  const [designs, setDesigns] = useState(Math.min(MAX_DESIGNS, Math.max(1, Number(saved.agent_designs) || 1)))
  const [cookMode, setCookMode] = useState<CookMode>(saved.agent_iterate === 'style' ? 'style' : 'layout')
  const setCook = (n: number, m: CookMode) => {
    setDesigns(n)
    setCookMode(m)
    tryCall('settings_set', { key: 'agent_designs', value: n })
    tryCall('settings_set', { key: 'agent_iterate', value: m })
  }
  const items = store.items
  const lastId = items[items.length - 1]?.id
  const typing = items.some((i) => i.kind === 'assistant' && i.id === lastId && (revealed.get(i.id) ?? 0) < i.text.split(/(\s+)/).length)
  useTick(16, typing)
  useTick(REDUCED_MOTION ? 400 : INDICATOR_TICK_MS, running)

  const send = (text: string) => {
    const t = text.trim()
    if (!t) return
    if (tryCall('agent_send', { text: t, parallel: Number(parallel), mode, designs, iterate: cookMode }) !== null) {
      setDraft('')
      onSent?.()
    } else toast('Unable to start the agent. Check the backend in Agent settings and try again.', true)
  }
  // The core refuses while an agent runs ("Stop the agent before …"): keep the chat and say why.
  const newChat = () => {
    try {
      call('agent_reset')
    } catch (e) {
      return toast((e as Error).message, true)
    }
    resetChat()
    setChatVersion((n) => n + 1)
    toast('New conversation')
  }
  const toggle = (id: number) => setOpen((s) => {
    const n = new Set(s)
    if (n.has(id)) n.delete(id)
    else n.add(id)
    return n
  })

  const { all: agents, running: runningAgents, shown: shownAgents, more: moreAgents } = barAgents()
  const planning = running && (() => {
    const last = items[items.length - 1]
    return !!last && ((last.kind === 'tool' && !last.running) || (last.kind === 'assistant' && !last.streaming) || last.kind === 'user')
  })()
  const todos = store.todos
  const doneTodos = todos.filter((t) => t.status === 'completed').length

  return (
    <div style={{ display: 'flex', flexDirection: 'column', flexGrow: 1, minHeight: 0 }}>
      <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 4, paddingLeft: 14, paddingRight: 8, height: 40, borderBottomWidth: 1, borderColor: C.border }}>
        <Icon name="sparkles" size={14} color={C.accent} />
        <T weight="600" grow>Design agent</T>
        <ChatHistory running={running} toast={toast} changed={() => setChatVersion((n) => n + 1)} />
        <IconButton icon="plus" title="New agent" testId="agent-new" onClick={newChat} />
        <IconButton icon="settings" title="Agent settings" testId="agent-settings" active={settings} onClick={() => setSettings((s) => !s)} />
      </div>
      {settings ? <AgentSettings mcpUrl={mcpUrl} toast={toast} /> : null}
      {agents.length > 1 && (running || agents.some((a) => a.parent)) ? (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4, paddingLeft: 14, paddingRight: 8, paddingTop: 5, paddingBottom: 6, borderBottomWidth: 1, borderColor: C.border, backgroundColor: C.panel2 }}>
          <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 8, height: 24 }}>
            <T size={11} color={C.muted} grow>{running ? `${runningAgents.length}/${agents.length} agents running` : 'Finished'}</T>
            {running ? <IconButton icon="stop" title="Stop all agents" color={C.danger} onClick={() => tryCall('agent_stop')} size={24} /> : null}
          </div>
          <div testId="agent-bar" style={{ display: 'flex', flexDirection: 'row', flexWrap: 'wrap', gap: 4 }}>
            {shownAgents.map((a) => (
              <div key={a.id} style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 4, paddingLeft: 6, paddingRight: 6, height: 20, flexShrink: 0, borderRadius: 10, backgroundColor: C.raised, opacity: store.running.has(a.id) ? 1 : 0.6 }}>
                <div style={{ width: 6, height: 6, borderRadius: 3, backgroundColor: a.color }} />
                <T size={10} color={C.text}>{a.name}</T>
              </div>
            ))}
            {moreAgents ? <T size={10} color={C.muted}>{`+${moreAgents}`}</T> : null}
          </div>
        </div>
      ) : null}

      {items.length === 0 ? (
        <div style={{ flexGrow: 1, minHeight: 0, display: 'flex', flexDirection: 'column', gap: 8, padding: 14 }}>
          <T size={15} weight="600" color={C.text}>Ask me to design anything</T>
          {picks.current.map((s) => (
            <Pressable key={s} testId={`suggestion-${s.slice(0, 12)}`} label={`Design ${s}`} onClick={() => send(`Design ${s}`)} style={{ padding: 10, borderRadius: 8, borderWidth: 1, borderColor: C.border, hover: { backgroundColor: C.panel2 } }}>
              <T size={12} color={C.muted}>{s}</T>
            </Pressable>
          ))}
          <T size={11} color={C.ghost}>Each agent gets a cursor on the canvas; what it writes appears while it writes it. Set Agents above 1× to split the work across several agents.</T>
        </div>
      ) : (
        <virtual-list estimatedItemHeight={44} followTail alignment="bottom" style={{ flexGrow: 1, minHeight: 0, paddingTop: 8, paddingBottom: 8 }}>
          {items.map((m) => (
            <div key={m.id} style={{ paddingLeft: 12, paddingRight: 12 }}>
              <Item m={m} isLast={m.id === lastId} expanded={open.has(m.id)} toggle={() => toggle(m.id)} tick={tick} />
            </div>
          ))}
          {planning ? (
            <div key="planning" style={{ paddingTop: 6, paddingBottom: 6, paddingLeft: 12, paddingRight: 12 }}>
              <T size={12} color={C.ghost}>{`Planning next moves${dots()}`}</T>
            </div>
          ) : null}
        </virtual-list>
      )}

      {todos.length ? (
        <div style={{ borderTopWidth: 1, borderColor: C.border, paddingLeft: 12, paddingRight: 12, paddingTop: 8, paddingBottom: 8, display: 'flex', flexDirection: 'column', gap: 4 }}>
          <Pressable label={`Plan, ${doneTodos} of ${todos.length} done`} expanded={planOpen} onClick={() => setPlanOpen((o) => !o)} style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, height: 24, borderRadius: 4 }}>
            <Icon name={planOpen ? 'chevronDown' : 'chevronRight'} size={12} color={C.ghost} />
            <T size={11} weight="600" color={C.muted} grow>Plan</T>
            <T size={11} color={C.ghost}>{`${doneTodos}/${todos.length}`}</T>
          </Pressable>
          {planOpen ? (
            <div testId="plan-todos" style={{ display: 'flex', flexDirection: 'column', gap: 4, flexShrink: 1, minHeight: 0, maxHeight: 240, overflowY: 'scroll' }}>
              {todos.map((t, i) => (
                <div key={i} style={{ display: 'flex', flexDirection: 'row', gap: 6, alignItems: 'flex-start' }}>
                  <T size={11} color={t.status === 'completed' ? C.ok : t.status === 'in_progress' ? C.accent : C.ghost} mono>{t.status === 'completed' ? '✓' : t.status === 'in_progress' ? '◐' : t.status === 'cancelled' ? '✕' : '○'}</T>
                  <T size={11} color={t.status === 'completed' ? C.ghost : C.text} grow style={{ minWidth: 0 }}>{t.status === 'in_progress' && t.activeForm ? t.activeForm : t.content}</T>
                </div>
              ))}
            </div>
          ) : null}
        </div>
      ) : null}

      <div style={{ padding: 10, borderTopWidth: 1, borderColor: C.border, display: 'flex', flexDirection: 'column', gap: 8 }}>
        <div style={{ borderRadius: 10, borderWidth: 1, borderColor: C.border, backgroundColor: C.panel2, padding: 8, display: 'flex', flexDirection: 'column', gap: 6 }}>
          <textarea
            testId="agent-input"
            aria-label="Message the design agent"
            value={draft}
            placeholder={running ? 'Queue a follow-up…' : 'Design anything…'}
            minRows={2}
            maxRows={8}
            onChange={(e) => setDraft(e.value ?? '')}
            onSubmit={(e) => send(e.value ?? draft)}
            theme={{ caret: C.accent }}
            style={{ fontSize: 13, color: C.text, fontFamily: FONT, width: '100%' }}
          />
          <Row gap={6}>
            <T size={11} color={C.muted}>Agents</T>
            <Segmented label="Agents working in parallel" value={parallel} onChange={setParallel} options={['1', '2', '3', '4'].map((v) => ({ value: v, label: `${v}×`, name: v === '1' ? '1 agent' : `${v} agents` }))} />
          </Row>
          {parallel !== '1' ? <Segmented label="How agents share the work" value={mode} onChange={setMode} options={[{ value: 'split', label: 'Split' }, { value: 'side', label: 'Side by side' }]} /> : null}
          <Row gap={6}>
            <ModelPicker settingsOpen={settings} />
            <div style={{ flexGrow: 1 }} />
            <CookMenu designs={designs} mode={cookMode} set={setCook} />
            {running ? (
              <IconButton icon="stop" title="Stop the agent" testId="agent-stop" color={C.danger} onClick={() => tryCall('agent_stop')} />
            ) : null}
            <IconButton icon="send" title="Send" testId="agent-send" color={C.accent} onClick={() => send(draft)} />
          </Row>
        </div>
      </div>
    </div>
  )
}

const MAX_DESIGNS = 6
type CookMode = 'layout' | 'style'

/** One option button of the "Let it cook" popover. */
function Choice({ label, active, dim, onClick, testId, width }: { label: string; active: boolean; dim?: boolean; onClick: () => void; testId: string; width: number }) {
  return (
    <Pressable
      testId={testId}
      label={label}
      selected={active}
      onClick={onClick}
      style={{
        // 0.8 keeps the label at 4.5:1; these stay clickable (they turn cooking on), so they must stay readable.
        width, height: 28, display: 'flex', alignItems: 'center', justifyContent: 'center', borderRadius: 6, opacity: dim ? 0.8 : 1,
        borderWidth: 1, borderColor: active ? C.raised : C.border, backgroundColor: active ? C.raised : undefined, hover: { backgroundColor: C.raised },
      }}
    >
      <T size={12} color={active ? C.text : C.muted} weight={active ? '600' : undefined}>{label}</T>
    </Pressable>
  )
}

/** "Let it cook": after a design is done the agent keeps designing variations on its own. */
function CookMenu({ designs, mode, set }: { designs: number; mode: CookMode; set: (designs: number, mode: CookMode) => void }) {
  const [open, setOpen] = useState(false)
  const trigger = useTrigger()
  const on = designs > 1
  const col = 116
  const counts = Array.from({ length: MAX_DESIGNS }, (_, i) => i + 1)
  return (
    <div style={{ position: 'relative', display: 'flex' }}>
      <Pressable
        ref={trigger.ref}
        testId="agent-cook"
        label={on ? `Let it cook: ${designs} variants` : 'Let it cook: off'}
        expanded={open}
        onClick={() => setOpen(!open)}
        style={{
          display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 3, height: 28, paddingLeft: 8, paddingRight: 8, borderRadius: 6,
          backgroundColor: on ? C.accentSoft : undefined, hover: { backgroundColor: C.raised },
        }}
      >
        <Icon name="sparkles" size={12} color={on ? C.accent : C.ghost} />
        <T size={11} color={on ? C.accent : C.ghost}>{`${designs}x`}</T>
      </Pressable>
      {open ? (
        <anchored side="top" align="end" gap={4} fit="snap" snapMargin={8} deferred priority={1} occlude>
          <div
            testId="cook-menu"
            role="dialog"
            aria-label="Let it cook"
            tabIndex={0}
            autoFocus
            onMouseDownOutside={(e) => { if (!trigger.hit(e)) setOpen(false) }}
            onKeyDown={(e) => { if (e.key === 'escape') setOpen(false) }}
            style={{
              width: 260, padding: 12, display: 'flex', flexDirection: 'column', gap: 12, borderRadius: 12, borderWidth: 1, borderColor: C.border, backgroundColor: C.panel2,
              boxShadow: { offsetX: 0, offsetY: 8, blurRadius: 24, spreadRadius: 0, color: '#00000080' },
            }}
          >
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              <T size={12} weight="600">Let it cook</T>
              <T size={12} color={C.muted}>Iterate on designs automatically: when a design is done the agent keeps going and designs new variations on its own.</T>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              <T size={11} weight="600" color={C.ghost}>ITERATION MODE</T>
              <Row gap={4}>
                {(['layout', 'style'] as const).map((m) => (
                  <Choice key={m} testId={`cook-mode-${m}`} width={col} label={m === 'style' ? 'Style' : 'Layout'} active={on && mode === m} dim={!on} onClick={() => set(on ? designs : 2, m)} />
                ))}
              </Row>
              <T size={11} color={C.ghost}>{mode === 'style' ? 'Each variation takes a new visual direction.' : 'Each variation explores a new layout and keeps the visual style.'}</T>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              <T size={11} weight="600" color={C.ghost}>VARIANTS</T>
              {[0, 2, 4].map((i) => (
                <Row key={i} gap={4}>
                  {counts.slice(i, i + 2).map((n) => (
                    <Choice key={n} testId={`cook-count-${n}`} width={col} label={n === 1 ? "Don't iterate" : String(n)} active={designs === n} onClick={() => set(n, mode)} />
                  ))}
                </Row>
              ))}
            </div>
          </div>
        </anchored>
      ) : null}
    </div>
  )
}

type SavedChat = { id: string; title: string; updatedAt: number }
type ChatList = { current: string | null; chats: SavedChat[] }

const GROUPS = ['Today', 'Yesterday', 'Previous 7 days', 'Older']
function groupOf(t: number) {
  const now = new Date()
  const day = (n: number) => new Date(now.getFullYear(), now.getMonth(), now.getDate() - n).getTime()
  return t >= day(0) ? GROUPS[0] : t >= day(1) ? GROUPS[1] : t >= day(7) ? GROUPS[2] : GROUPS[3]
}

/** Saved chats dropdown: grouped by day, running dot, remove on hover, clear with confirm. */
function ChatHistory({ running, toast, changed }: { running: boolean; toast: Toast; changed: () => void }) {
  const [list, setList] = useState<ChatList | null>(null)
  const [hover, setHover] = useState<string | null>(null)
  const [confirm, setConfirm] = useState(false)
  // Removing a saved chat cannot be undone: the first press arms it, the second removes.
  const [armed, setArmed] = useState<string | null>(null)
  const trigger = useTrigger()
  // Keyboard path to remove one chat: the "x" also shows while its row (or the "x") has keyboard focus,
  // and arming it from the keyboard moves focus to the "Remove" confirm that replaces it.
  const renderer = useGpuixRequired()
  const kb = useKeyboardFocusId()
  const box = useRef<PublicInstance>(null)
  const els = useRef(new Map<string, PublicInstance>())
  const keep = (key: string) => (el: PublicInstance | null) => void (el ? els.current.set(key, el) : els.current.delete(key))
  const kbChat = kb === null ? null : [...els.current].find(([, el]) => el.id === kb)?.[0].replace(/^\w+:/, '') ?? null
  const focusKeys = (el?: PublicInstance | null) => {
    if (!el) return
    renderer.focusElement?.(el.id)
    setKeyboardFocus(el.id)
  }
  useEffect(() => {
    if (armed && kbChat === armed) focusKeys(els.current.get(`confirm:${armed}`))
  }, [armed])
  // Titles saved from a multi-line prompt hold line breaks; a row is one line, so collapse them.
  const refresh = () => {
    const l = tryCall<ChatList>('chat_list') ?? { current: null, chats: [] }
    setList({ ...l, chats: l.chats.map((c) => ({ ...c, title: (c.title ?? '').split(/\s+/).filter(Boolean).join(' ') })) })
  }
  const close = () => {
    setList(null)
    setConfirm(false)
  }
  // Core refusals ("Stop the agent before …") are shown as toasts.
  const act = <R,>(cmd: string, args: Record<string, unknown> = {}): R | null => {
    try {
      return call<R>(cmd, args)
    } catch (e) {
      toast((e as Error).message, true)
      return null
    }
  }
  const open = (id: string) => {
    const r = act<{ events: any[] }>('chat_load', { id })
    // A chat whose file vanished stays listed until the list is read again: drop it now.
    if (!r) return refresh()
    restoreChat(r.events ?? [])
    close()
    changed()
  }
  const remove = (id: string) => {
    if (act('chat_delete', { id }) === null) return
    if (id === list?.current) {
      resetChat()
      changed()
    }
    refresh()
  }
  const clear = () => {
    if (act<boolean>('chat_clear')) {
      resetChat()
      changed()
    }
    setConfirm(false)
    refresh()
  }

  const chats = list?.chats ?? []
  return (
    <div ref={trigger.ref} style={{ position: 'relative', display: 'flex' }}>
      <IconButton icon="history" title="Chat history" testId="agent-history" active={!!list} onClick={() => (list ? close() : refresh())} />
      {list ? (
        <anchored side="bottom" align="end" gap={4} fit="snap" snapMargin={8} deferred priority={1} occlude>
          <div
            ref={box}
            testId="chat-history"
            role="dialog"
            aria-label="Chat history"
            tabIndex={0}
            autoFocus
            onMouseDownOutside={(e) => { if (!trigger.hit(e)) close() }}
            onKeyDown={(e) => { if (e.key === 'escape') close() }}
            style={{
              width: 280, maxHeight: 440, display: 'flex', flexDirection: 'column', paddingTop: 6, paddingBottom: 6, borderRadius: 10,
              borderWidth: 1, borderColor: C.border, backgroundColor: C.panel2,
              boxShadow: { offsetX: 0, offsetY: 8, blurRadius: 24, spreadRadius: 0, color: '#00000080' },
            }}
          >
            {chats.length === 0 ? (
              <div style={{ padding: 10 }}><T size={12} color={C.ghost}>No saved chats yet</T></div>
            ) : (
              <div style={{ display: 'flex', flexDirection: 'column', flexShrink: 1, minHeight: 0, overflowY: 'scroll' }}>
                {GROUPS.map((g) => {
                  const rows = chats.filter((c) => groupOf(c.updatedAt) === g)
                  if (!rows.length) return null
                  return (
                    <div key={g} style={{ display: 'flex', flexDirection: 'column' }}>
                      <div style={{ paddingLeft: 12, paddingTop: 6, paddingBottom: 4 }}><T size={11} weight="600" color={C.ghost}>{g}</T></div>
                      {rows.map((c) => {
                        const live = running && c.id === list.current
                        return (
                          <div
                            key={c.id}
                            onMouseEnter={() => setHover(c.id)}
                            onMouseLeave={() => setHover((h) => (h === c.id ? null : h))}
                            style={{
                              display: 'flex', flexDirection: 'row', alignItems: 'center', height: 30, marginLeft: 4, marginRight: 4, paddingRight: 2, borderRadius: 6,
                              backgroundColor: c.id === list.current ? C.raised : undefined, hover: { backgroundColor: C.raised },
                            }}
                          >
                            <Pressable
                              ref={keep(`row:${c.id}`)}
                              testId={`chat-${c.id}`}
                              label={`Open chat: ${c.title || 'Untitled chat'}`}
                              onClick={() => open(c.id)}
                              style={{ flexGrow: 1, minWidth: 0, height: 30, display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, paddingLeft: 8, borderRadius: 6 }}
                            >
                              {live ? <div style={{ width: 6, height: 6, borderRadius: 3, flexShrink: 0, backgroundColor: C.accent }} /> : null}
                              <T size={12} color={c.id === list.current ? C.text : '#C9C9D0'} grow style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{c.title || 'Untitled chat'}</T>
                            </Pressable>
                            {live ? null : armed === c.id ? (
                              <Pressable ref={keep(`confirm:${c.id}`)} testId={`chat-remove-confirm-${c.id}`} label={`Remove chat: ${c.title || 'Untitled chat'}`} onClick={() => {
                                const keys = kbChat === c.id
                                setArmed(null)
                                remove(c.id)
                                // The row is gone: keep keyboard focus inside the popover.
                                if (keys) focusKeys(box.current)
                              }} style={{ height: 24, paddingLeft: 8, paddingRight: 8, borderRadius: 5, display: 'flex', alignItems: 'center', backgroundColor: '#F8717122', hover: { backgroundColor: '#F8717133' } }}>
                                <T size={11} color={C.danger} weight="600">Remove</T>
                              </Pressable>
                            ) : hover === c.id || kbChat === c.id ? (
                              <IconButton ref={keep(`x:${c.id}`)} icon="x" size={24} title={`Remove chat: ${c.title || 'Untitled chat'}`} testId={`chat-remove-${c.id}`} onClick={() => setArmed(c.id)} />
                            ) : null}
                          </div>
                        )
                      })}
                    </div>
                  )
                })}
              </div>
            )}
            {chats.length ? (
              <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, marginTop: 6, paddingTop: 6, paddingLeft: 8, paddingRight: 8, borderTopWidth: 1, borderColor: C.border }}>
                {confirm ? (
                  <>
                    <T size={12} grow>Clear all chats?</T>
                    <Button onClick={() => setConfirm(false)}>Cancel</Button>
                    <Button danger testId="chat-clear-confirm" onClick={clear}>Clear</Button>
                  </>
                ) : (
                  <Pressable testId="chat-clear" label="Clear history" onClick={() => setConfirm(true)} style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, height: 28, paddingLeft: 4, paddingRight: 8, borderRadius: 6, hover: { backgroundColor: C.raised } }}>
                    <Icon name="trash" size={13} />
                    <T size={12} color={C.muted}>Clear history</T>
                  </Pressable>
                )}
              </div>
            ) : null}
          </div>
        </anchored>
      ) : null}
    </div>
  )
}

function AgentHeader({ agentId }: { agentId: string }) {
  const p = profileOf(agentId)
  const model = tryModel()
  return (
    <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, paddingTop: 8 }}>
      <div style={{ width: 8, height: 8, borderRadius: 4, backgroundColor: p.color }} />
      <T size={12} weight="600" color={C.text}>{p.name}</T>
      {p.parent ? <T size={11} color={C.ghost}>{`· helping ${profileOf(p.parent).name}`}</T> : <T size={11} color={C.ghost}>{model}</T>}
    </div>
  )
}

let cachedModel: string | null = null
function tryModel() {
  if (cachedModel === null) {
    const s = tryCall<Record<string, string>>('settings_get') ?? {}
    const cli = s.backend === 'claude-code'
    const model = MODELS.find((m) => m.value === s.agent_model)?.label
    cachedModel = cli ? (model ? `Claude Code · ${model}` : 'Claude Code') : model ?? 'Opus 5.5'
  }
  return cachedModel
}

const MODELS = [
  { value: 'claude-opus-5-5', label: 'Opus 5.5' },
  { value: 'claude-sonnet-5-5', label: 'Sonnet 5.5' },
  { value: 'claude-haiku-5-5', label: 'Haiku 5.5' },
  { value: 'claude-fable-5-1', label: 'Fable 5.1' },
]
/** Models with a fast output mode (Claude Code `fastMode`, API `speed: "fast"`); mirrors core `FAST_MODELS`. */
const FAST_MODELS = ['claude-opus-5-5', 'claude-opus-5', 'claude-opus-4-8']

/** Model picker plus the Fast toggle, which shows only where the model has a fast mode. Claude Code
 *  also offers "Default", which leaves the CLI on its own configured model. */
function ModelPicker({ settingsOpen }: { settingsOpen: boolean }) {
  // Re-read when the settings panel closes: the backend may have changed.
  const s = useMemo(() => tryCall<Record<string, unknown>>('settings_get') ?? {}, [settingsOpen])
  const cli = s.backend === 'claude-code'
  const items = cli ? [{ value: 'default', label: 'Default' }, ...MODELS] : MODELS
  const stored = typeof s.agent_model === 'string' ? s.agent_model : null
  const current = stored ?? (cli ? 'default' : 'claude-opus-5-5')
  const [model, setModel] = useState(current)
  const [fast, setFast] = useState(s.agent_fast === true)
  useEffect(() => setModel(current), [current])
  const { ref: triggerRef, on: focused, clear } = useRing()
  // Arrow keys move the menu's highlight, which is only a 1.1:1 background shift: ring it while the
  // keyboard drives the menu, not while the mouse does.
  const [keys, setKeys] = useState(false)
  const pick = (v: string) => {
    setModel(v)
    cachedModel = null
    tryCall('settings_set', { key: 'agent_model', value: v === 'default' ? null : v })
  }
  const label = items.find((m) => m.value === model)?.label ?? model
  return (
    <>
      <SelectPrimitive.Root items={items} value={model} onValueChange={pick}>
        <SelectPrimitive.Trigger
          testId="agent-model"
          role="button"
          aria-label={`Model: ${label}`}
          ref={triggerRef}
          onMouseDown={() => { clear(); setKeys(false) }}
          onKeyDown={() => setKeys(true)}
          style={(st) => ({
            display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 4, height: 28, paddingLeft: 8, paddingRight: 6, borderRadius: 6, cursor: 'pointer',
            backgroundColor: st.open ? C.raised : undefined, hover: { backgroundColor: C.raised }, ...(focused ? { position: 'relative' } : {}),
          })}
        >
          <SelectPrimitive.Value style={{ fontSize: 12, color: C.muted, fontFamily: FONT }} />
          <Icon name="chevronDown" size={12} color={C.ghost} />
          {focused ? <FocusRing /> : null}
        </SelectPrimitive.Trigger>
        <SelectPrimitive.Content
          side="top"
          sideOffset={4}
          onKeyDown={() => setKeys(true)}
          style={{
            width: 200, padding: 4, display: 'flex', flexDirection: 'column', borderRadius: 10, backgroundColor: C.panel2, borderWidth: 1, borderColor: C.border,
            boxShadow: { offsetX: 0, offsetY: 8, blurRadius: 24, spreadRadius: 0, color: '#00000080' },
          }}
        >
          {items.map((m) => (
            <SelectPrimitive.Item
              key={m.value}
              value={m.value}
              testId={`agent-model-${m.value}`}
              role="option"
              aria-label={m.label}
              aria-selected={m.value === model}
              // GPUI does not bubble mouse events: the pointer taking over shows on the items, not the menu.
              onMouseEnter={() => { if (keys) setKeys(false) }}
              style={(st) => ({
                position: 'relative', display: 'flex', flexDirection: 'row', alignItems: 'center', height: 30, paddingLeft: 8, paddingRight: 8, borderRadius: 6, cursor: 'pointer',
                backgroundColor: st.highlighted ? C.raised : C.panel2,
              })}
            >
              {(st) => (
                <>
                  <T size={12} color={st.selected ? C.text : C.muted} weight={st.selected ? '600' : undefined} grow>{m.label}</T>
                  {st.selected ? <T size={12} color={C.text}>✓</T> : null}
                  {keys && st.highlighted ? <FocusRing inset /> : null}
                </>
              )}
            </SelectPrimitive.Item>
          ))}
        </SelectPrimitive.Content>
      </SelectPrimitive.Root>
      {FAST_MODELS.includes(model) ? (
        <Toggle label="Fast" on={fast} testId="agent-fast" onChange={(on) => { setFast(on); tryCall('settings_set', { key: 'agent_fast', value: on || null }) }} />
      ) : null}
    </>
  )
}

function Item({ m, isLast, expanded, toggle }: { m: ChatItem; isLast: boolean; expanded: boolean; toggle: () => void; tick: number }) {
  switch (m.kind) {
    case 'user':
    case 'queued':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'end', paddingTop: 8, paddingBottom: 4 }}>
          {/* A queued bubble is faded (0.6 keeps its text at 4.5:1); its caption is not, it is already ghost text. */}
          <div style={{ maxWidth: 236, paddingLeft: 12, paddingRight: 12, paddingTop: 8, paddingBottom: 8, borderRadius: 18, backgroundColor: C.raised, opacity: m.kind === 'queued' ? 0.6 : 1 }}>
            <text style={{ fontSize: 12, color: C.text, fontFamily: FONT }}>{m.text}</text>
          </div>
          {m.kind === 'queued' ? <T size={10} color={C.ghost}>Queued — sent when the agents finish</T> : null}
        </div>
      )
    case 'assistant': {
      const { text } = visibleText(m, isLast)
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4, paddingBottom: 4 }}>
          {m.header ? <AgentHeader agentId={m.agent.id} /> : null}
          <markdown source={text} style={{ fontSize: 13, color: C.text, fontFamily: FONT }} />
        </div>
      )
    }
    case 'thinking': {
      const live = !m.endedAt
      const label = live ? `Thinking${dots()}` : `Thought for ${duration((m.endedAt ?? 0) - m.startedAt)}`
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4, paddingTop: 4, paddingBottom: 4 }}>
          <Pressable label={label} expanded={expanded} onClick={toggle} style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, minHeight: 24, borderRadius: 4 }}>
            <Icon name={expanded ? 'chevronDown' : 'chevronRight'} size={11} color={C.ghost} />
            <T size={12} color={C.ghost}>{label}</T>
          </Pressable>
          {expanded && m.text.trim() ? <markdown source={m.text} style={{ fontSize: 12, color: C.muted, fontFamily: FONT }} /> : null}
        </div>
      )
    }
    case 'tool': {
      if (m.name === 'TodoWrite') return <div style={{ height: 0 }} />
      const label = toolLabel(m.name, m.running)
      const src = m.name === 'execute' && typeof m.input?.input === 'string' ? (m.input.input as string) : null
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, paddingTop: 3, paddingBottom: 3 }}>
          <Pressable label={m.running ? `${label}, running` : `${label}, ${m.error ? 'failed' : 'done'}`} expanded={expanded} onClick={toggle} style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, minHeight: 24, borderRadius: 4 }}>
            <div style={{ width: 8, height: 8, borderRadius: 4, backgroundColor: m.agent.color, opacity: m.running && !REDUCED_MOTION ? pulseAt(performance.now()) : 1 }} />
            <T size={12} color={m.running ? C.text : C.muted}>{m.running ? `${label}${dots()}` : label}</T>
            {m.running && m.lines ? <T size={11} color={C.ghost}>{`${m.lines} lines`}</T> : null}
            {!m.running ? <T size={12} color={m.error ? C.ghost : C.ok}>{m.error ? '✕' : '✓'}</T> : null}
            <div style={{ flexGrow: 1 }} />
            <Icon name={expanded ? 'chevronDown' : 'chevronRight'} size={11} color={C.ghost} />
          </Pressable>
          {(m.images ?? []).map((img, i) => (
            <img key={i} src={imageSrc(img)} objectFit="contain" style={{ width: 252, height: 158, borderRadius: 6 }} />
          ))}
          {expanded ? (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6, padding: 8, borderRadius: 8, backgroundColor: C.panel2, borderWidth: 1, borderColor: C.border }}>
              {m.output ? (
                <>
                  <T size={11} color={C.ghost} weight="600">OUTPUT</T>
                  {m.output.split('\n').slice(0, 40).map((line, i) => (
                    <text key={i} style={{ fontSize: 11, color: m.error ? C.danger : C.muted, fontFamily: MONO, whiteSpace: 'nowrap' }}>{line || ' '}</text>
                  ))}
                </>
              ) : null}
              <T size={11} color={C.ghost} weight="600">{src !== null ? 'SCRIPT' : 'INPUT'}</T>
              {src !== null ? (
                <code code={src} language="javascript" style={{ fontSize: 11 }} />
              ) : (
                <code code={JSON.stringify(m.input ?? {}, null, 2)} language="json" style={{ fontSize: 11 }} />
              )}
            </div>
          ) : null}
        </div>
      )
    }
    case 'usage': {
      const u = m.usage ?? {}
      const rows: [string, unknown][] = [
        ['Input tokens', u.input], ['Output tokens', u.output], ['Cache reads', u.cacheRead], ['Cache writes', u.cacheWrite], ['Model turns', u.turns],
        ...(typeof u.costUsd === 'number' && u.costUsd > 0 ? [['Cost', `$${u.costUsd.toFixed(3)}`] as [string, unknown]] : []),
      ]
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4, paddingTop: 6, paddingBottom: 8 }}>
          {m.stopped ? <T size={11} color={C.ghost} style={{ fontStyle: 'italic' }}>Agent stopped by user.</T> : null}
          <Pressable label={`${m.agent.name} designed for ${duration(m.durationMs)}. Show usage`} expanded={expanded} onClick={toggle} style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, minHeight: 24, borderRadius: 4 }}>
            <div style={{ width: 6, height: 6, borderRadius: 3, backgroundColor: m.agent.color }} />
            <T size={11} color={C.ghost}>{`${m.agent.name} designed for ${duration(m.durationMs)}`}</T>
          </Pressable>
          {expanded ? rows.filter(([, v]) => v !== undefined && v !== null).map(([k, v]) => (
            <Row key={k}>
              <T size={11} color={C.ghost} grow>{k}</T>
              <T size={11} color={C.muted} mono>{String(v)}</T>
            </Row>
          )) : null}
        </div>
      )
    }
    case 'note':
      return <div style={{ paddingTop: 4, paddingBottom: 4 }}><T size={11} color={C.ghost} style={{ fontStyle: 'italic' }}>{m.text}</T></div>
    case 'error':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginTop: 6, marginBottom: 6, padding: 10, borderRadius: 8, borderWidth: 1, borderColor: '#F8717155', backgroundColor: '#F871711A' }}>
          <T size={12} weight="600" color={C.danger}>{`Something went wrong${m.agent ? ` with ${m.agent.name}` : ''}`}</T>
          <T size={11} color={C.muted}>{m.text}</T>
        </div>
      )
    case 'external':
      return (
        <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, paddingTop: 3, paddingBottom: 3 }}>
          <Icon name="plug" size={11} color={C.ghost} />
          <T size={11} color={C.ghost}>{`External agent · ${toolLabel(m.text, false)}`}</T>
        </div>
      )
    case 'permission':
      return <PermissionCard m={m} />
  }
}

const WARN = '#F59E0B'

/** Permission card: what the agent wants to run, then Switch to Auto / Allow / Deny. */
function PermissionCard({ m }: { m: Extract<ChatItem, { kind: 'permission' }> }) {
  const [answered, setAnswered] = useState<string | null>(null)
  const auto = useMemo(() => (tryCall<Record<string, unknown>>('settings_get') ?? {}).agent_auto_permissions === true, [])
  const target = m.input.file_path ?? m.input.command ?? m.input.url ?? m.input.path ?? m.input.pattern
  const desc = typeof m.input.description === 'string' ? m.input.description : null
  const answer = (a: 'auto' | 'allow' | 'deny') => {
    if (tryCall('agent_permission', { id: m.reqId, answer: a }) !== null) setAnswered(a)
  }
  return (
    <div testId={`permission-${m.reqId}`} style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 6, marginBottom: 6, padding: 10, borderRadius: 8, borderWidth: 1, borderColor: `${WARN}66`, backgroundColor: `${WARN}14` }}>
      <Row gap={6}>
        <Icon name="settings" size={13} color={WARN} />
        <T size={12} weight="600" color={WARN}>Permission request</T>
      </Row>
      <T size={12} color={C.text}>{desc ?? `${m.agent.name} wants to use ${m.tool}${target ? ' on' : ''}`}</T>
      {target ? <text style={{ fontSize: 11, color: C.muted, fontFamily: MONO }}>{String(target).slice(0, 400)}</text> : null}
      {answered ? (
        <T size={11} color={C.ghost}>{answered === 'deny' ? 'Denied' : answered === 'auto' ? 'Allowed · auto mode from the next message' : 'Allowed'}</T>
      ) : (
        <Row gap={6}>
          {auto ? null : <Button testId="permission-auto" onClick={() => answer('auto')}>Switch to Auto</Button>}
          <Button primary testId="permission-allow" onClick={() => answer('allow')}>Allow</Button>
          <Button testId="permission-deny" onClick={() => answer('deny')}>Deny</Button>
        </Row>
      )}
    </div>
  )
}

function AgentSettings({ mcpUrl, toast }: { mcpUrl: string; toast: Toast }) {
  const s = useMemo(() => tryCall<Record<string, string>>('settings_get') ?? {}, [])
  const [backend, setBackend] = useState(s.backend ?? 'claude-code')
  const [hasKey, setHasKey] = useState(!!s.anthropic_api_key)
  const [keyBox, setKeyBox] = useState(0)
  const [perms, setPerms] = useState<'ask' | 'auto'>((s as Record<string, unknown>).agent_auto_permissions === true ? 'auto' : 'ask')
  const set = (key: string, value: unknown) => {
    cachedModel = null
    return tryCall('settings_set', { key, value }) !== null // false: not saved, the setting kept its old value
  }
  const keyNotSaved = () => toast('Unable to save the API key setting. Check that the app data folder is writable, then try again.', true)
  const install = (client: string) => {
    const r = tryCall<string>('mcp_install', { client })
    if (r) toast(r)
    else toast(`Unable to configure ${client}. Check that it is installed and on your PATH, then try again.`, true)
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, padding: 12, borderBottomWidth: 1, borderColor: C.border, backgroundColor: C.panel2 }}>
      <T size={11} color={C.muted} weight="600">Backend</T>
      <Segmented
        label="Agent backend"
        value={backend}
        onChange={(v) => { setBackend(v); set('agent_backend', v) }}
        options={[{ value: 'api', label: 'Claude API' }, { value: 'claude-code', label: 'Claude Code CLI' }]}
      />
      {backend === 'api' ? (
        <>
          {/* settings_get only returns a mask for a saved key: never edit it, an empty box means keep the key. */}
          <Row gap={6}>
            <Field key={keyBox} name="Anthropic API key" value="" placeholder={hasKey ? 'Key saved. Paste a new key to replace it' : 'sk-ant-…'} onCommit={(v) => {
              const key = v.trim() // a blank key is no key (Engine::api_key), so it must not replace a saved one
              if (!key) return false // Field reverts the box to empty, so the saved-key placeholder shows again
              if (!set('anthropic_api_key', key)) {
                keyNotSaved()
                return false
              }
              setHasKey(true)
              setKeyBox((n) => n + 1) // remount: the new key must not stay readable in the box
              toast('API key saved locally')
            }} testId="api-key" />
            {hasKey ? <Button testId="api-key-remove" onClick={() => { if (!set('anthropic_api_key', null)) return keyNotSaved(); setHasKey(false); toast('API key removed') }}>Remove</Button> : null}
          </Row>
          <T size={11} color={C.ghost}>Or set ANTHROPIC_API_KEY. Pick the model below the chat box.</T>
          <Row>
            <T size={11} color={C.muted}>Effort</T>
            <Field name="Effort" value={s.agent_effort ?? 'high'} width={90} placeholder="high" onCommit={(v) => set('agent_effort', v || null)} />
          </Row>
        </>
      ) : (
        <>
          <T size={11} color={C.ghost}>Uses your installed `claude` CLI and its login. Canvas tools are passed in as the nalarupa MCP server.</T>
          <T size={11} color={C.muted} weight="600">Other tools (shell, files outside the document folder)</T>
          <Segmented
            label="Permission for other tools"
            value={perms}
            onChange={(v) => { setPerms(v); set('agent_auto_permissions', v === 'auto' ? true : null) }}
            options={[{ value: 'ask', label: 'Ask me' }, { value: 'auto', label: 'Auto mode' }]}
          />
        </>
      )}
      <T size={11} color={C.muted} weight="600">Connect external agents (MCP)</T>
      <T size={11} color={C.ghost} mono>{mcpUrl}</T>
      <Row gap={6}>
        <Button onClick={() => install('claude-code')}>Claude Code</Button>
        <Button onClick={() => install('cursor')}>Cursor</Button>
        <Button onClick={() => install('codex')}>Codex</Button>
      </Row>
    </div>
  )
}
