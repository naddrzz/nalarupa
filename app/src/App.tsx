import { memo, useCallback, useEffect, useRef, useState } from 'react'
import { useGpuixRequired, useWindowSize, type PublicInstance } from '@gpuix/react'
import { call, tryCall, type Box, type State } from './core'
import { applyEvents, clearPresence, store } from './agents'
import { Canvas, fitCam, isMovable, type Cam, type Mods, type Tool } from './Canvas'
import { defaultSavePath } from './paths'
import { readClipboard, writeClipboard } from './clipboard'
import { LeftPanel } from './Layers'
import { Inspector } from './Inspector'
import { AgentPanel } from './Agent'
import { Home } from './Home'
import { Present } from './Present'
import { C, LEFT_W, REDUCED_MOTION, RIGHT_W, TOP_H } from './theme'
import { hasKeyboardFocus, Icon, IconButton, Pressable, Segmented, ShortcutScope, T, type Toast } from './ui'
import { centerOffset, inParent, type M } from './geometry'
import type { IconName } from './icons'

let flips = 0
let nudges = 0

const TOOLS: { tool: Tool; icon: IconName; key: string; label: string }[] = [
  { tool: 'move', icon: 'cursor', key: 'v', label: 'Move (V)' },
  { tool: 'frame', icon: 'frame', key: 'f', label: 'Frame (F / A)' },
  { tool: 'rectangle', icon: 'square', key: 'r', label: 'Rectangle (R)' },
  { tool: 'ellipse', icon: 'circle', key: 'o', label: 'Ellipse (O)' },
  { tool: 'text', icon: 'type', key: 't', label: 'Text (T)' },
  { tool: 'pen', icon: 'pen', key: 'p', label: 'Pen (P)' },
  { tool: 'hand', icon: 'hand', key: 'h', label: 'Hand (H)' },
]

// The side panels ignore the camera: with stable props, panning and zooming re-render only the canvas.
const Left = memo(LeftPanel)
const Design = memo(Inspector)
const Chat = memo(AgentPanel)

export function App() {
  const renderer = useGpuixRequired()
  const win = useWindowSize()
  const [init] = useState(() => call('init'))
  const [screen, setScreen] = useState<'home' | 'editor' | 'present'>('home')
  const [opened, setOpened] = useState(false)
  const [state, setState] = useState<State>(init.state)
  const [selection, setSelection] = useState<string[]>([])
  const [tool, setTool] = useState<Tool>('move')
  const [cam, setCam] = useState<Cam>({ x: -100, y: -100, zoom: 0.5 })
  const [rightTab, setRightTab] = useState<'design' | 'agent'>('agent')
  const [chatTick, setChatTick] = useState(0)
  const [toastMsg, setToastMsg] = useState<{ text: string; error: boolean } | null>(null)
  const [presentFrom, setPresentFrom] = useState<string | undefined>()
  const [hoverId, setHoverId] = useState<string | null>(null)
  const toastTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const canvasEl = useRef<PublicInstance>(null)

  const cw = Math.max(100, (win?.width ?? 1440) - LEFT_W - RIGHT_W)
  const ch = Math.max(100, (win?.height ?? 900) - TOP_H)

  const toast = useCallback<Toast>((m, error = false) => {
    setToastMsg({ text: m, error })
    if (toastTimer.current) clearTimeout(toastTimer.current)
    // Confirmations fade after 3.5 s; an error waits for the user to read it and dismiss it.
    toastTimer.current = error ? null : setTimeout(() => setToastMsg(null), 3500)
  }, [])
  // Agent inserts appear in place when the OS asks for reduced motion.
  useEffect(() => void tryCall('reduce_motion', { on: REDUCED_MOTION }), [])

  const select = useCallback((ids: string[]) => {
    setSelection(ids)
    tryCall('select', { ids })
    if (ids.length) setRightTab((t) => (t === 'agent' && store.items.length === 0 ? 'design' : t))
  }, [])

  const fitTo = useCallback((ids?: string[]) => {
    const b = tryCall<Box | null>('bounds', { ids: ids ?? [] })
    if (b) setCam(fitCam(b, cw, ch, 60, 1))
  }, [cw, ch])

  const openedDoc = useCallback(() => {
    clearPresence()
    setSelection([])
    setOpened(true)
    setScreen('editor')
    setTimeout(() => fitTo(), 0)
  }, [fitTo])

  // Poll the core for document changes (agent / MCP edits) and agent events.
  useEffect(() => {
    const t = setInterval(() => {
      const r = tryCall<{ state: State; events: any[] }>('poll')
      if (!r) return
      setState((s) => (s.version === r.state.version && s.agentRunning === r.state.agentRunning && s.dirty === r.state.dirty && s.title === r.state.title && s.animating === r.state.animating ? s : r.state))
      if (applyEvents(r.events, performance.now())) setChatTick((n) => n + 1)
      const toastEv = r.events.find((e) => e.type === 'toast')
      if (toastEv) toast(toastEv.text)
      if (r.events.some((e) => e.type === 'document_opened')) openedDoc()
    }, 120)
    return () => clearInterval(t)
  }, [openedDoc])

  useEffect(() => {
    renderer.setWindowTitle?.(`${state.title}${state.dirty ? ' •' : ''} — Nalarupa`)
  }, [state.title, state.dirty])

  useEffect(() => {
    // Drop selected ids that no longer exist (undo, agent deletes).
    if (!selection.length) return
    const boxes = tryCall<Box[]>('boxes', { ids: selection }) ?? []
    if (boxes.length !== selection.length) setSelection(boxes.map((b) => b.id))
  }, [state.version])

  // The control that held focus can unmount (Inspector Delete/Hide, Delete on a Layers row): then no element
  // takes keys and every shortcut is dead. Hand focus back to the canvas once the panels have re-rendered.
  useEffect(() => {
    if (screen !== 'editor') return
    // Retry for about a second: under a slow frame the control can unmount well after the state change.
    let tries = 0
    const t = setInterval(() => {
      if (renderer.getFocusedElementId?.() == null && canvasEl.current) {
        renderer.focusElement?.(canvasEl.current.id)
        clearInterval(t)
      } else if (++tries >= 10) clearInterval(t)
    }, 100)
    return () => clearInterval(t)
  }, [selection, state.version, screen, renderer])

  const saveTo = (path?: string) => {
    const r = tryCall('save', { path })
    if (r) toast(`Saved ${r.path}`)
    else toast('Unable to save the document. Check that the folder exists and is writable.', true)
    return !!r
  }
  const save = (as = false) => {
    if (as) {
      renderer.promptForPaths?.({ directories: true, prompt: 'Save here' }).then((dirs) => {
        if (dirs?.[0]) saveTo(defaultSavePath(state.title, dirs[0]))
      })
    } else saveTo(state.path ?? defaultSavePath(state.title))
  }

  const present = useCallback((from?: string) => {
    setPresentFrom(from ?? selection[0])
    setScreen('present')
  }, [selection])
  const focusLayer = useCallback((id: string) => fitTo([id]), [fitTo])
  // Keys go back to the canvas once a prompt is sent: Space-pan and shortcuts work while the agent designs.
  const sent = useCallback(() => canvasEl.current && renderer.focusElement?.(canvasEl.current.id), [renderer])
  // A clicked toolbar button keeps focus and eats Space (activate), so Space-pan would not start: a mouse pick
  // hands the keys to the canvas, a keyboard pick (Enter/Space on a Tabbed-to button) leaves focus on the button.
  const pickTool = (t: Tool) => {
    setTool(t)
    if (!hasKeyboardFocus()) sent()
  }

  const zoomTo = (to: number) => {
    const z = Math.min(64, Math.max(0.02, to))
    const cx = cam.x + cw / 2 / cam.zoom
    const cy = cam.y + ch / 2 / cam.zoom
    setCam({ zoom: z, x: cx - cw / 2 / z, y: cy - ch / 2 / z })
  }
  const zoomBy = (f: number) => zoomTo(cam.zoom * f)

  const run = <R = any,>(cmd: string, args: Record<string, unknown>, then?: (r: R) => void) => {
    const r = tryCall<R>(cmd, args)
    if (r === null) toast('That action needs a different selection. Select the layers it applies to and try again.', true)
    else then?.(r)
    return true
  }
  const nodeOf = (id: string) => tryCall('node', { id })
  const parentOf = (id: string) => (tryCall<Box[]>('boxes', { ids: [id] }) ?? [])[0]?.parent ?? 'document'
  const childIds = (parent: string) => (parent === 'document' ? tryCall<Box[]>('frames') : tryCall<Box[]>('children_boxes', { id: parent }))?.map((b) => b.id) ?? []
  const copySelection = () => {
    const r = tryCall<{ html: string; text: string; ids: string[] }>('copy', { ids: selection })
    if (!r?.ids.length) return false
    writeClipboard({ html: r.html, text: r.text })
    return true
  }
  const inside = (id: string, of: string) => {
    if (id.startsWith(`${of}/`)) return true
    for (let p = parentOf(id); p !== 'document'; p = parentOf(p)) if (p === of) return true
    return false
  }
  const ALIGN: Record<string, string> = { w: 'top', a: 'left', s: 'bottom', d: 'right', h: 'hcenter', v: 'vcenter' }

  const onShortcut = (key: string, m: Mods): boolean => {
    const sel = selection
    const has = sel.length > 0
    if (m.mod && key === 'g' && has) {
      if (m.shift) return run<{ ids: string[] }>('unwrap', { ids: sel }, (r) => select(r.ids))
      return run<{ id: string }>('wrap', { ids: sel, kind: m.alt ? 'frame' : 'group' }, (r) => select([r.id]))
    }
    if (m.mod && key === 'backspace' && has) return run<{ ids: string[] }>('unwrap', { ids: sel }, (r) => select(r.ids))
    if (key === 'a' && m.shift && !m.mod && has) {
      if (m.alt) return run('remove_layout', { ids: sel })
      return run<{ id: string }>('add_layout', { ids: sel }, (r) => select([r.id]))
    }
    if (m.alt && !m.mod && !m.shift && ALIGN[key] && has) return run('align', { ids: sel, dir: ALIGN[key] })
    if (m.shift && !m.mod && !m.alt && (key === 'h' || key === 'v') && has) {
      const k = key === 'h' ? 'flipX' : 'flipY'
      const undoKey = `flip:${++flips}`
      // A flipped layer mirrors its descendants too, so a selected descendant of a selected layer is left to it.
      for (const id of sel.filter((id) => !sel.some((o) => o !== id && inside(id, o)))) {
        const info = nodeOf(id)
        const l = info?.local
        if (!l) continue
        const slash = id.includes('/')
        // Instances and their descendants inherit the component's flip: write it explicitly, `null` would fall back to it.
        const props: Record<string, unknown> = { [k]: l[k] ? (slash || info.node?.type === 'ref' ? false : null) : true }
        if (info.box && !slash && (isMovable(info) || info.parent === 'document')) {
          // The flip mirrors about the origin: move the origin so the center stays put.
          const [rot, fx, fy] = [Number(l.rotation), !!l.flipX, !!l.flipY]
          const n = info.node
          const [ox0, oy0] = centerOffset(info.box.w, info.box.h, rot, fx, fy, l.bx, l.by)
          const [ox, oy] = centerOffset(info.box.w, info.box.h, rot, k === 'flipX' ? !fx : fx, k === 'flipY' ? !fy : fy, l.bx, l.by)
          props.x = Math.round((Number(n.x ?? 0) + ox0 - ox) * 100) / 100
          props.y = Math.round((Number(n.y ?? 0) + oy0 - oy) * 100) / 100
        }
        tryCall('update', { ids: [id], props, coalesce: undoKey })
      }
      return true
    }
    const t = TOOLS.find((x) => x.key === key) ?? (key === 'a' ? TOOLS[1] : undefined)
    if (t && !m.mod && !m.shift && !m.alt) return setTool(t.tool), true
    if ((key === 'delete' || key === 'backspace') && has) {
      return run('delete', { ids: sel }, () => select([]))
    }
    if (m.mod && key === 'z') return tryCall(m.shift ? 'redo' : 'undo'), true
    if (m.mod && key === 'y') return tryCall('redo'), true
    if (m.mod && key === 'd' && has) {
      const r = tryCall('duplicate', { ids: sel })
      if (r?.ids) select(r.ids)
      return true
    }
    if (m.mod && key === 'c' && has) return copySelection(), true
    if (m.mod && key === 'x' && has) {
      if (copySelection()) run('delete', { ids: sel }, () => select([]))
      return true
    }
    if (m.mod && key === 'v') {
      const c = readClipboard()
      const view = { x: cam.x, y: cam.y, w: cw / cam.zoom, h: ch / cam.zoom }
      return run<{ ids: string[] }>('paste', { html: c.html ?? '', text: c.text ?? '', targets: sel, view }, (r) => { if (r.ids.length) select(r.ids) })
    }
    if (m.mod && key === 'a') {
      // Nothing selected: every top-level layer; else every child of the selection's shared parent.
      const parents = [...new Set(sel.map(parentOf))]
      if (!has || parents.length === 1) select(childIds(has ? parents[0] : 'document'))
      return true
    }
    if ((key === '[' || key === ']') && has) {
      const dir = key === ']' ? (m.mod ? 'up' : 'front') : m.mod ? 'down' : 'back'
      return run('reorder', { ids: sel, dir })
    }
    if (m.mod && key === 's') return save(m.shift), true
    if (m.mod && key === 'enter') return present(), true
    if (m.mod && key === '0') return zoomTo(1), true
    if (key === 'escape') {
      if (tool !== 'move') setTool('move')
      select([])
      return true
    }
    if (key === 'enter' && m.shift) {
      const parents = [...new Set(sel.map(parentOf))].filter((p) => p !== 'document')
      if (parents.length) select(parents)
      return true
    }
    if (key === 'enter' && has) {
      const kids = sel.flatMap((id) => (tryCall<Box[]>('children_boxes', { id }) ?? []).map((b) => b.id))
      if (kids.length) select(kids)
      return true
    }
    if ((key === '1' || key === '!') && m.shift) return fitTo(), true
    if ((key === '2' || key === '@') && m.shift) return fitTo(selection), true
    if (key === '=' || key === '+') return zoomBy(1.25), true
    if (key === '-') return zoomBy(0.8), true
    const nudge: Record<string, [number, number]> = { left: [-1, 0], right: [1, 0], up: [0, -1], down: [0, 1] }
    if (nudge[key] && has) {
      // Inside auto layout arrows reorder along the layout axis (Alt: to the end); elsewhere they nudge.
      const free = new Map<string, string[]>()
      const loose: string[] = []
      for (const id of sel) {
        const info = nodeOf(id)
        const layout = info?.node?.layoutPosition !== 'absolute' ? info?.parentLayout : 'none'
        if (!layout || layout === 'none') loose.push(id)
        else if ((layout === 'vertical') === (key === 'up' || key === 'down')) {
          const later = key === 'right' || key === 'down'
          tryCall('reorder', { ids: [id], dir: m.alt ? (later ? 'front' : 'back') : later ? 'up' : 'down' })
        }
      }
      // A moved layer carries its descendants, so a loose descendant of a loose layer is left to it.
      for (const id of loose.filter((id) => !loose.some((o) => o !== id && inside(id, o)))) {
        const p = parentOf(id)
        free.set(p, [...(free.get(p) ?? []), id])
      }
      const s = m.shift ? 10 : 1
      // `move_by` works in the parent's space: turn the screen delta into it (rotated or flipped frames).
      const undoKey = `nudge:${++nudges}`
      for (const [parent, ids] of free) {
        const pm: M | null = parent === 'document' ? null : nodeOf(parent)?.box?.m ?? null
        const [dx, dy] = inParent(pm, nudge[key][0] * s, nudge[key][1] * s)
        tryCall('move_by', { ids, dx, dy, coalesce: undoKey })
      }
      return true
    }
    return false
  }
  // Buttons and the left panel's rows own focus after a click and pass on the keys they do not handle. A stable
  // wrapper keeps the memoized panels from re-rendering with every `onShortcut`.
  const shortcutRef = useRef(onShortcut)
  shortcutRef.current = onShortcut
  const penKeyRef = useRef<((key: string, m: Mods) => boolean) | null>(null)
  const panelShortcut = useCallback((key: string, m: Mods) => !!penKeyRef.current?.(key, m) || shortcutRef.current(key, m), [])

  const toastEl = toastMsg ? (
    <div
      testId="toast"
      role={toastMsg.error ? 'alert' : 'status'}
      style={{
        position: 'absolute', left: screen === 'editor' ? LEFT_W + 16 : 16, bottom: 16, maxWidth: 560, display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 8,
        paddingLeft: 12, paddingRight: toastMsg.error ? 4 : 12, paddingTop: 6, paddingBottom: 6, borderRadius: 8, backgroundColor: C.raised, borderWidth: 1,
        borderColor: toastMsg.error ? C.danger : C.border, boxShadow: { offsetX: 0, offsetY: 8, blurRadius: 24, spreadRadius: 0, color: '#00000080' },
      }}
    >
      {toastMsg.error ? <Icon name="alert" size={14} color={C.danger} /> : null}
      <T size={12} grow style={{ minWidth: 0 }}>{toastMsg.text}</T>
      {toastMsg.error ? <IconButton icon="x" size={24} title="Dismiss message" testId="toast-dismiss" onClick={() => setToastMsg(null)} /> : null}
    </div>
  ) : null

  if (screen === 'present') {
    return <Present w={win?.width ?? 1440} h={win?.height ?? 900} start={presentFrom} version={state.version} animating={!!state.animating} exit={() => setScreen('editor')} />
  }
  if (screen === 'home') {
    // One root like the editor: as two window-level siblings, a showing toast blanked Home.
    return (
      <div style={{ width: '100%', height: '100%' }}>
        {/* Read fresh: files saved or opened since startup belong in Recent too. */}
        <Home
          recent={tryCall<{ recent?: string[] }>('settings_get')?.recent ?? []}
          // The polled flag lags an edit by up to 120 ms, so ask the core when a template or file is picked.
          dirty={() => tryCall<State>('state')?.dirty ?? state.dirty}
          title={state.title}
          save={() => {
            try {
              // Fresh like `dirty`: an open or Save As moments ago is not in the polled state yet.
              const current = tryCall<State>('state') ?? state
              return saveTo(current.path ?? defaultSavePath(current.title))
            } catch {
              toast('Unable to save the document. Check that the folder exists and is writable.', true)
              return false
            }
          }}
          back={opened ? () => setScreen('editor') : undefined}
          onOpened={openedDoc}
          toast={toast}
        />
        {toastEl}
      </div>
    )
  }

  return (
    <ShortcutScope value={panelShortcut}>
    <div style={{ width: '100%', height: '100%', display: 'flex', flexDirection: 'column', backgroundColor: C.bg }}>
      <div style={{ height: TOP_H, flexShrink: 0, display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 4, paddingLeft: 8, paddingRight: 10, backgroundColor: C.panel, borderBottomWidth: 1, borderColor: C.border }}>
        <IconButton icon="home" title="Home" testId="home" onClick={() => setScreen('home')} />
        <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, width: LEFT_W - 48, paddingLeft: 4 }}>
          <T size={13} weight="600" style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{state.title}</T>
          {state.dirty ? <div testId="unsaved-dot" style={{ width: 6, height: 6, flexShrink: 0, borderRadius: 3, backgroundColor: C.accent }} /> : null}
        </div>
        {TOOLS.map((t) => (
          <IconButton key={t.tool} icon={t.icon} title={t.label} testId={`tool-${t.tool}`} active={tool === t.tool} onClick={() => pickTool(t.tool)} />
        ))}
        <div style={{ width: 1, height: 20, backgroundColor: C.border, marginLeft: 6, marginRight: 6 }} />
        <IconButton icon="undo" title="Undo (Ctrl+Z)" testId="undo" onClick={() => tryCall('undo')} />
        <IconButton icon="redo" title="Redo (Ctrl+Shift+Z)" testId="redo" onClick={() => tryCall('redo')} />
        <div style={{ flexGrow: 1 }} />
        <IconButton icon="minus" title="Zoom out" onClick={() => zoomBy(0.8)} />
        <Pressable testId="zoom-level" label={`Zoom ${Math.round(cam.zoom * 100)}%. Fit to screen`} onClick={() => fitTo()} style={{ width: 52, height: 28, borderRadius: 6, display: 'flex', alignItems: 'center', justifyContent: 'center', hover: { backgroundColor: C.raised } }}>
          <T size={12} color={C.muted}>{`${Math.round(cam.zoom * 100)}%`}</T>
        </Pressable>
        <IconButton icon="plus" title="Zoom in" onClick={() => zoomBy(1.25)} />
        <IconButton icon="maximize" title="Zoom to fit (Shift+1)" testId="fit" onClick={() => fitTo()} />
        <div style={{ width: 1, height: 20, backgroundColor: C.border, marginLeft: 6, marginRight: 6 }} />
        <IconButton icon="play" title="Present (Ctrl+Enter)" testId="present" onClick={() => present()} />
        <IconButton icon="save" title="Save (Ctrl+S)" testId="save" onClick={() => save()} />
        <IconButton icon="saveAs" title="Save as… (Ctrl+Shift+S)" onClick={() => save(true)} />
      </div>
      <div style={{ flexGrow: 1, minHeight: 0, display: 'flex', flexDirection: 'row' }}>
        <Left version={state.version} selection={selection} select={select} focus={focusLayer} present={present} toast={toast} hover={setHoverId} onShortcut={panelShortcut} />
        <Canvas
          w={cw} h={ch} cam={cam} setCam={setCam} version={state.version} selection={selection}
          select={select} tool={tool} setTool={setTool} toast={toast} onShortcut={onShortcut} penKeyRef={penKeyRef} chatOpen={rightTab === 'agent'} hoverId={hoverId}
          animating={!!state.animating} rootRef={canvasEl}
        />
        <div style={{ width: RIGHT_W, flexShrink: 0, height: '100%', display: 'flex', flexDirection: 'column', backgroundColor: C.panel, borderLeftWidth: 1, borderColor: C.border }}>
          <div style={{ padding: 10, borderBottomWidth: 1, borderColor: C.border }}>
            <Segmented value={rightTab} onChange={setRightTab} label="Right panel" options={[{ value: 'design', label: 'Design', testId: 'tab-design' }, { value: 'agent', label: state.agentRunning ? 'Agent •' : 'Agent', name: state.agentRunning ? 'Agent, running' : 'Agent', testId: 'tab-agent' }]} />
          </div>
          {rightTab === 'design' ? (
            <Design version={state.version} selection={selection} select={select} toast={toast} />
          ) : (
            <ShortcutScope value={null}>
              <Chat tick={chatTick} running={state.agentRunning} mcpUrl={init.mcpUrl} toast={toast} onSent={sent} />
            </ShortcutScope>
          )}
        </div>
      </div>
      {toastEl}
    </div>
    </ShortcutScope>
  )
}
