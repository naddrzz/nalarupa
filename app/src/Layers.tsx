import { useEffect, useMemo, useRef, useState } from 'react'
import { useGpuixRequired, type PublicInstance } from '@gpuix/react'
import { call, tryCall, type Box, type Layer } from './core'
import type { Mods } from './Canvas'
import { nodeIcon } from './icons'
import { C, LEFT_W } from './theme'
import { FocusRing, forwardKey, Icon, IconButton, Pressable, Segmented, setKeyboardFocus, ShortcutScope, T, useKeyboardFocusId, type Toast } from './ui'

export function LeftPanel(props: {
  version: number; selection: string[]; select: (ids: string[]) => void; focus: (id: string) => void; present: (id?: string) => void
  toast: Toast; hover?: (id: string | null) => void; onShortcut: (key: string, mods: Mods) => boolean
}) {
  const [tab, setTab] = useState<'layers' | 'slides'>('layers')
  return (
    <div style={{ width: LEFT_W, flexShrink: 0, height: '100%', display: 'flex', flexDirection: 'column', backgroundColor: C.panel, borderRightWidth: 1, borderColor: C.border }}>
      <div style={{ padding: 10, borderBottomWidth: 1, borderColor: C.border }}>
        <Segmented label="Left panel" value={tab} onChange={setTab} options={[{ value: 'layers', label: 'Layers', testId: 'tab-layers' }, { value: 'slides', label: 'Slides', testId: 'tab-slides' }]} />
      </div>
      {tab === 'layers' ? <Layers {...props} /> : <Slides {...props} />}
    </div>
  )
}

type Row = Layer & { index: number; layout: boolean }
type DropAt = { id: string; pos: 'before' | 'after' | 'inside' }
const ROW_H = 28
// A row's indent is a margin, so a deep row narrows instead of running off the panel. The first 6 levels step
// 14 px, deeper ones 3 px, which keeps the name readable at 17+ levels (imported designs nest that deep).
const indent = (depth: number) => Math.min(depth, 6) * 14 + Math.max(depth - 6, 0) * 3
// A slide row is the 14 px number, an 8 px gap and the framed thumbnail: 14 + 8 + (196 + 4) = 222 px,
// inside the 223 px the panel leaves after its border and the rows' 12 px padding.
const THUMB_W = 196

function Layers({ version, selection, select, focus, hover, onShortcut }: { version: number; selection: string[]; select: (ids: string[]) => void; focus: (id: string) => void; hover?: (id: string | null) => void; onShortcut: (key: string, mods: Mods) => boolean }) {
  const layers = useMemo(() => tryCall<Row[]>('layers') ?? [], [version])
  const [open, setOpen] = useState<Set<string>>(new Set())
  const [hoverRow, setHoverRow] = useState<string | null>(null)
  const [renaming, setRenaming] = useState<{ id: string; draft: string } | null>(null)
  // A keyboard tree (WAI-ARIA treeview): one row is the Tab stop, arrows move between visible rows,
  // Enter or Space selects. The row with keyboard focus shows a ring.
  const focusId = useKeyboardFocusId()
  const [cursor, setCursor] = useState<string | null>(null)
  const [dropAt, setDropAt] = useState<DropAt | null>(null)
  // `top` = window y of the first row's top; GPUIX keeps sending moves to the pressed row, so
  // the row under the pointer comes from its y (rows are ROW_H tall).
  const pressed = useRef<{ id: string; ids: string[]; narrow: boolean; x: number; y: number; y0: number; top: number } | null>(null)
  // Refs mirror the drag state: several mouseUp handlers may run for one release.
  const dragRef = useRef<string[] | null>(null)
  const rowEls = useRef(new Map<string, PublicInstance>())
  const renderer = useGpuixRequired()
  const dropRef = useRef<DropAt | null>(null)
  const hoverRef = useRef<string | null>(null)
  const eyeAt = useRef(0)
  // Double-click from two presses on the same row (simulated input carries no click count).
  const lastDown = useRef({ id: '', at: 0 })
  // Selected nodes reveal their ancestors, which stay expanded after the selection moves on.
  // Applied while rendering, so a reveal never paints collapsed first, and only into `open`, so Left
  // or the chevron can still collapse an ancestor of the selection. Only newly selected nodes reveal,
  // unless the document changed (a new node can be selected before its layer exists).
  const [revealed, setRevealed] = useState<{ selection: string[]; layers: Row[] } | null>(null)
  if (revealed?.selection !== selection || revealed.layers !== layers) {
    setRevealed({ selection, layers })
    const byId = new Map(layers.map((l) => [l.id, l]))
    const add: string[] = []
    for (const s of revealed?.layers === layers ? selection.filter((s) => !revealed.selection.includes(s)) : selection) {
      for (let p = byId.get(s)?.parent; p && p !== 'document'; p = byId.get(p)?.parent) if (!open.has(p)) add.push(p)
    }
    if (add.length) setOpen(new Set([...open, ...add]))
  }
  // A row is visible when every ancestor is expanded.
  const visible = useMemo(() => {
    const byId = new Map(layers.map((l) => [l.id, l]))
    return layers.filter((l) => {
      for (let p = l.parent; p && p !== 'document'; p = byId.get(p)?.parent ?? 'document') if (!open.has(p)) return false
      return true
    }).map((l) => ({ ...l, expanded: open.has(l.id) }))
  }, [layers, open])
  // The Tab stop: the last row focused or clicked while it is visible, else the first selected row.
  const tabStop = (visible.find((v) => v.id === cursor) ?? visible.find((v) => selection.includes(v.id)) ?? visible[0])?.id
  // A selection made elsewhere (canvas click, marquee, undo) scrolls the list to its first row, without
  // moving keyboard focus. The list is virtual, so a row far off screen has no element: scroll by index,
  // and again until the row is mounted, as the list may not have taken the new rows yet. Waits for the
  // row to exist (a new node can be selected before its layer does).
  const listEl = useRef<PublicInstance | null>(null)
  const scrolledFor = useRef<string[] | null>(null)
  const scrollTimer = useRef<ReturnType<typeof setInterval>>(undefined)
  useEffect(() => () => clearInterval(scrollTimer.current), [])
  useEffect(() => {
    if (scrolledFor.current === selection) return
    const at = visible.findIndex((v) => selection.includes(v.id))
    clearInterval(scrollTimer.current)
    if (at < 0) return
    scrolledFor.current = selection
    // The list's anchor is [first row, px scrolled into it, viewport px]; rows are ROW_H tall.
    const shown = () => {
      const t = listEl.current && renderer.getListScrollTop?.(listEl.current.id)
      return !!t && at >= t[0] + (t[1] > 0 ? 1 : 0) && at < t[0] + Math.floor((t[2] - 12) / ROW_H)
    }
    const go = () => {
      const done = shown()
      if (!done && listEl.current) renderer.scrollToItem?.(listEl.current.id, at)
      return done
    }
    if (go()) return
    let tries = 0
    scrollTimer.current = setInterval(() => { if (go() || ++tries >= 20) clearInterval(scrollTimer.current) }, 100)
  }, [selection, visible])
  const setExpanded = (id: string, on: boolean) => setOpen((s) => {
    const n = new Set(s)
    if (on) n.add(id)
    else n.delete(id)
    return n
  })
  const focusRow = (id: string | undefined, ring = true) => {
    const el = id ? rowEls.current.get(id) : undefined
    if (!id || !el) return
    setCursor(id)
    renderer.focusElement?.(el.id)
    el.scrollIntoView?.()
    // GPUIX fires no onFocus for programmatic focus, so the ring is moved here.
    if (ring) setKeyboardFocus(el.id)
  }
  // Click or Enter/Space: Ctrl toggles the row, Shift adds the range from the first selected row.
  const pick = (id: string, row: number, m?: { ctrl?: boolean; cmd?: boolean; shift?: boolean }) => {
    if (m?.ctrl || m?.cmd) return selection.includes(id) ? selection.filter((s) => s !== id) : [...selection, id]
    if (!m?.shift) return [id]
    const ids = visible.map((v) => v.id)
    const first = ids.findIndex((v) => selection.includes(v))
    const [a, b] = first < 0 ? [row, row] : [Math.min(first, row), Math.max(first, row)]
    return [...new Set([...selection, ...ids.slice(a, b + 1)])]
  }

  const setDrop = (at: DropAt | null) => {
    dropRef.current = at
    setDropAt(at)
  }
  const endDrag = () => {
    pressed.current = null
    dragRef.current = null
    setDrop(null)
  }
  // The release may reach the pressed row or the list; either finishes once.
  const finish = () => {
    const p = pressed.current
    if (!p) return
    if (dragRef.current && dropRef.current) drop(dragRef.current, dropRef.current)
    else if (p.narrow && !dragRef.current) select([p.id])
    endDrag()
  }
  const setHover = (id: string | null) => {
    hoverRef.current = id
    setHoverRow(id)
    hover?.(id)
  }

  // Rows list auto-layout children in layout order and everything else topmost first,
  // so "above" a row means a higher index only in freeform parents.
  const drop = (ids: string[], at: DropAt) => {
    const target = layers.find((l) => l.id === at.id)
    if (!target || ids.includes(target.id)) return
    if (at.pos === 'inside') return tryCall('reparent', { ids, parent: target.id, ...(target.layout ? { index: 0 } : {}) })
    const flow = layers.find((l) => l.id === target.parent)?.layout ?? false
    let index = flow === (at.pos === 'before') ? target.index : target.index + 1
    index -= layers.filter((l) => l.parent === target.parent && ids.includes(l.id) && l.index < index).length
    tryCall('reparent', { ids, parent: target.parent, index })
  }

  // The rename as of the last event, ahead of renders: Enter and the blur that follows it may both run
  // before a re-render, and must commit once, with the typed value.
  const live = useRef<{ id: string; draft: string } | null>(null)
  const rename = (r: { id: string; draft: string } | null) => {
    live.current = r
    setRenaming(r)
  }
  // Enter passes the field's own value: the change that typed it may not have reached `live` yet.
  const commitRename = (value?: string) => {
    const r = live.current
    if (!r) return
    const name = (value ?? r.draft).trim()
    const row = layers.find((l) => l.id === r.id)
    if (name && row && name !== row.name) tryCall('update', { ids: [r.id], props: { name } })
    rename(null)
  }

  if (!layers.length) {
    return <div style={{ padding: 16 }}><T color={C.ghost}>Empty canvas. Draw a frame (F) or ask the agent.</T></div>
  }
  return (
    <div
      testId="layers"
      style={{ flexGrow: 1, minHeight: 0, display: 'flex', flexDirection: 'column', userSelect: 'none' }}
      onMouseUp={finish}
      onMouseLeave={() => setHover(null)}
    >
      <virtual-list ref={listEl} estimatedItemHeight={28} style={{ flexGrow: 1, minHeight: 0, paddingTop: 6, paddingBottom: 6 }}>
        {visible.map((l, row) => {
          const active = selection.includes(l.id)
          const mark = dropAt?.id === l.id ? dropAt.pos : null
          return (
            <div key={l.id}>
              <div
                ref={(el: PublicInstance | null) => { if (el) rowEls.current.set(l.id, el); else rowEls.current.delete(l.id) }}
                testId={`layer-${l.id}`}
                role="treeitem"
                aria-label={l.name}
                aria-selected={active}
                aria-level={l.depth + 1}
                aria-expanded={l.children ? l.expanded : undefined}
                tabIndex={l.id === tabStop ? 0 : -1}
                onKeyDown={(e) => {
                  // The rename field's keys bubble here too.
                  if (renaming) return
                  const k = (e.key ?? '').toLowerCase()
                  // Ctrl/Cmd+Enter is an editor shortcut (Present); Ctrl+Space still toggles the row.
                  if (k === 'space' || (k === 'enter' && !(e.modifiers?.ctrl || e.modifiers?.cmd))) {
                    // The chevron handles its own Enter/Space: select only when the row itself has focus.
                    if (!e.isHeld && renderer.getFocusedElementId?.() === rowEls.current.get(l.id)?.id) select(pick(l.id, row, e.modifiers))
                  } else if (k === 'up' || k === 'down') focusRow(visible[row + (k === 'up' ? -1 : 1)]?.id)
                  else if (k === 'home' || k === 'end') focusRow(visible[k === 'home' ? 0 : visible.length - 1]?.id)
                  // Right opens a closed row, then steps to its first child; Left closes, then steps to the parent.
                  else if (k === 'right' && l.children) {
                    if (l.expanded) focusRow(visible[row + 1]?.id)
                    else setExpanded(l.id, true)
                  } else if (k === 'left') {
                    if (l.children && l.expanded) setExpanded(l.id, false)
                    else focusRow(l.parent)
                  } else if (k !== 'right') forwardKey(onShortcut, e)
                }}
                onMouseDown={(e) => {
                  setKeyboardFocus(null)
                  if (e.button !== 0) return
                  if (performance.now() - eyeAt.current < 100) return
                  setCursor(l.id)
                  const now = performance.now()
                  const double = (e.clickCount ?? 1) >= 2 || (lastDown.current.id === l.id && now - lastDown.current.at < 450)
                  lastDown.current = { id: l.id, at: double ? 0 : now }
                  if (double) return rename({ id: l.id, draft: l.name })
                  const m = e.modifiers
                  // A plain press on a selected row keeps the selection for a drag; the release narrows it.
                  const next = active && !(m?.ctrl || m?.cmd || m?.shift) ? selection : pick(l.id, row, m)
                  if (next !== selection) select(next)
                  const el = rowEls.current.get(l.id)
                  const b = el ? renderer.getElementBounds?.(el.id) : null
                  const y = e.y ?? 0
                  const top = (b ? b.y : y - ROW_H / 2) - row * ROW_H
                  pressed.current = { id: l.id, ids: next.includes(l.id) ? next : [l.id], narrow: next === selection && selection.length > 1, x: e.x ?? 0, y, y0: y, top }
                }}
                onMouseMove={(e) => {
                  const p = pressed.current
                  if (!p) return
                  // No button at a new spot: released elsewhere (same spot = GPUI hover refresh).
                  if (e.pressedButton === undefined) return void ((Math.abs((e.x ?? 0) - p.x) > 0.5 || Math.abs((e.y ?? 0) - p.y) > 0.5) && endDrag())
                  ;[p.x, p.y] = [e.x ?? 0, e.y ?? 0]
                  if (!dragRef.current) {
                    if (Math.abs(p.y - p.y0) < 4 || p.ids.some((id) => id.includes('/'))) return
                    dragRef.current = p.ids
                  }
                  const i = Math.floor((p.y - p.top) / ROW_H)
                  const t = visible[i]
                  if (!t) return setDrop(null)
                  const off = p.y - p.top - i * ROW_H
                  const into = (t.type === 'frame' || t.type === 'group') && !t.id.includes('/')
                  setDrop({ id: t.id, pos: into ? (off < 7 ? 'before' : off > 21 ? 'after' : 'inside') : off < ROW_H / 2 ? 'before' : 'after' })
                }}
                onMouseUp={finish}
                onMouseEnter={() => setHover(l.id)}
                onMouseLeave={() => { if (hoverRef.current === l.id) setHover(null) }}
                onAuxClick={() => focus(l.id)}
                style={{
                  position: 'relative', display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, height: 28, marginLeft: 6 + indent(l.depth), marginRight: 6,
                  paddingLeft: 6, paddingRight: 4, borderRadius: 6, cursor: 'pointer',
                  // Hidden layers keep their name at 4.5:1 on the panel and its hover fill: 0.6 for the grey
                  // name, 0.85 for the violet component name (0.6 measured 3.17:1), and none on a selected
                  // component row (0.85 over the selection fill measures 4.13:1).
                  opacity: l.hidden ? (l.reusable || l.type === 'ref' ? (active ? 1 : 0.85) : 0.6) : 1,
                  backgroundColor: mark === 'inside' ? '#3B82F655' : active ? '#3B82F633' : undefined,
                  hover: { backgroundColor: active ? '#3B82F633' : C.panel2 },
                }}
              >
                {l.children ? (
                  // The row forwards the keys that bubble up from the chevron: no second forward from the button.
                  <ShortcutScope value={null}>
                  <Pressable
                    onClick={() => setExpanded(l.id, !l.expanded)}
                    // Out of the Tab order: Left and Right on the row expand and collapse.
                    tabIndex={-1}
                    // A full-height 24 px target over the 14 px slot (negative margins keep the icon where
                    // it was). GPUI does not bubble clicks: only a div that paints a fill gets a hitbox that
                    // stops the press before the row (select, double-click rename). A fully transparent
                    // fill counts as none, hence alpha 1/255. Not `pointerEvents: 'auto'`, which would also
                    // stop the list's wheel over the chevron.
                    testId={`layer-toggle-${l.id}`}
                    label={`${l.expanded ? 'Collapse' : 'Expand'} ${l.name}`}
                    expanded={l.expanded}
                    style={{
                      width: 24, height: ROW_H, marginLeft: -5, marginRight: -5, flexShrink: 0, borderRadius: 5, display: 'flex', alignItems: 'center', justifyContent: 'center',
                      backgroundColor: '#00000001', hover: { backgroundColor: C.raised },
                    }}
                  >
                    <Icon name={l.expanded ? 'chevronDown' : 'chevronRight'} size={12} color={C.ghost} />
                  </Pressable>
                  </ShortcutScope>
                ) : <div style={{ width: 14, flexShrink: 0 }} />}
                <Icon name={nodeIcon(l.type, l.reusable)} size={13} color={l.reusable || l.type === 'ref' ? C.component : C.ghost} />
                {renaming?.id === l.id ? (
                  <div style={{ flexGrow: 1, minWidth: 0, height: 22, borderRadius: 4, borderWidth: 1, borderColor: C.select, backgroundColor: C.panel2, display: 'flex', alignItems: 'center', paddingLeft: 4 }}>
                    <input
                      testId="layer-rename"
                      aria-label="Layer name"
                      autoFocus
                      value={renaming.draft}
                      onChange={(e) => rename(live.current && { ...live.current, draft: e.value ?? '' })}
                      onSubmit={(e) => { commitRename(e.value); focusRow(l.id, false) }}
                      onBlur={() => commitRename()}
                      onKeyDown={(e) => { if ((e.key ?? '').toLowerCase() === 'escape') { rename(null); focusRow(l.id, false) } }}
                      style={{ flexGrow: 1, minWidth: 0, fontSize: 12, color: C.text }}
                    />
                  </div>
                ) : (
                  <T size={12} color={l.reusable || l.type === 'ref' ? C.component : active ? C.text : '#C9C9D0'} grow style={{ minWidth: 0, whiteSpace: 'nowrap', textOverflow: 'ellipsis', overflow: 'hidden' }}>
                    {l.name}
                  </T>
                )}
                {hoverRow === l.id || l.hidden ? (
                  <div
                    testId={`layer-eye-${l.id}`}
                    role="button"
                    aria-label={`${l.hidden ? 'Show' : 'Hide'} ${l.name}`}
                    onMouseDown={() => { eyeAt.current = performance.now() }}
                    onClick={() => tryCall('update', { ids: [l.id], props: { enabled: l.hidden ? null : false } })}
                    // The row keeps a press for its drag, which loses the click of an unpainted child; the
                    // painted fill gets its own hitbox. It also takes the hover from the row, so the eye
                    // holds the row's hover itself to stay mounted until the click.
                    onMouseEnter={() => setHover(l.id)}
                    onMouseLeave={() => { if (hoverRef.current === l.id) setHover(null) }}
                    style={{ width: 22, height: 22, borderRadius: 5, display: 'flex', alignItems: 'center', justifyContent: 'center', backgroundColor: '#00000001', hover: { backgroundColor: C.raised } }}
                  >
                    <Icon name={l.hidden ? 'hidden' : 'eye'} size={13} color={C.muted} />
                  </div>
                ) : null}
                {focusId !== null && rowEls.current.get(l.id)?.id === focusId ? <FocusRing /> : null}
                {mark === 'before' || mark === 'after' ? (
                  <div style={{ position: 'absolute', left: 6, right: 0, top: mark === 'before' ? -1 : 27, height: 2, backgroundColor: C.select }} />
                ) : null}
              </div>
            </div>
          )
        })}
      </virtual-list>
    </div>
  )
}

function Slides({ version, selection, select, focus, present, toast }: { version: number; selection: string[]; select: (ids: string[]) => void; focus: (id: string) => void; present: (id?: string) => void; toast: Toast }) {
  const frames = useMemo(() => (tryCall<Box[]>('frames') ?? []).filter((f) => f.type === 'frame'), [version])
  // Thumbnails are rendered by the core (PNG) at a small scale, a few per tick so a long deck neither
  // freezes the UI nor stops at a cap; a row shows a placeholder until its thumbnail is ready.
  const [thumbs, setThumbs] = useState<Map<string, string>>(new Map())
  useEffect(() => {
    let stop = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const next = new Map<string, string>()
    const live = new Set(frames.map((f) => f.id))
    const step = (from: number) => {
      if (stop) return
      for (const f of frames.slice(from, from + 8)) {
        const scale = Math.min(1, THUMB_W / Math.max(f.w, 1))
        const b64 = tryCall<string>('png', { id: f.id, scale })
        if (b64) next.set(f.id, `data:image/png;base64,${b64}`)
      }
      // Rows keep their old thumbnail until the new one replaces it, so an edit does not flash them blank;
      // thumbnails of deleted slides are dropped so create/delete cycles do not grow the map.
      setThumbs((old) => new Map([...old].filter(([id]) => live.has(id)).concat([...next])))
      if (from + 8 < frames.length) timer = setTimeout(() => step(from + 8), 0)
    }
    step(0)
    return () => { stop = true; clearTimeout(timer) }
  }, [frames])
  // Empty ids = every top-level frame, in document order; saved next to the document as <title>.pdf.
  const exportPdf = () => {
    try {
      toast(`Exported ${call<string[]>('export', { ids: [], format: 'pdf', path: '' })[0]}`)
    } catch (e) {
      const why = (e as Error).message
      console.error('[core] export:', why)
      // An empty (or all-hidden) deck is not a folder problem: name the cause instead of the generic hint.
      if (why.startsWith('nothing to export')) toast('No visible slides to export. Show a slide or add a frame first.', true)
      else toast('Unable to export. Check that the export folder is writable and try again.', true)
    }
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', flexGrow: 1, minHeight: 0 }}>
      <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', paddingLeft: 14, paddingRight: 8, height: 36 }}>
        <T size={11} color={C.muted} grow>{`${frames.length} ${frames.length === 1 ? 'slide' : 'slides'} · top-level frames`}</T>
        <IconButton icon="download" title="Export deck to PDF" testId="export-deck-pdf" onClick={exportPdf} />
        <IconButton icon="play" title="Present" testId="present-from-slides" onClick={() => present(selection[0])} />
      </div>
      <virtual-list estimatedItemHeight={150} style={{ flexGrow: 1, minHeight: 0, paddingBottom: 12 }}>
        {frames.map((f, i) => {
          const th = Math.max(20, Math.round((f.h / Math.max(f.w, 1)) * THUMB_W))
          const active = selection.includes(f.id)
          // The list ignores horizontal padding, so each row carries its own 12 px.
          return (
            <div key={f.id} style={{ paddingLeft: 12, paddingRight: 12 }}>
              <Pressable
                testId={`slide-${i + 1}`}
                label={`Slide ${i + 1}: ${f.name}`}
                selected={active}
                onClick={() => { select([f.id]); focus(f.id) }}
                style={{ display: 'flex', flexDirection: 'row', gap: 8, paddingTop: 6, paddingBottom: 6, borderRadius: 6 }}
              >
                <T size={11} color={C.ghost} style={{ width: 14, flexShrink: 0 }}>{String(i + 1)}</T>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 4, flexGrow: 1, minWidth: 0 }}>
                  <div style={{ borderRadius: 6, borderWidth: 2, borderColor: active ? C.select : C.border, overflow: 'hidden', width: THUMB_W + 4, height: th + 4 }}>
                    {thumbs.has(f.id) ? (
                      <img testId={`slide-thumb-${i + 1}`} src={thumbs.get(f.id)} objectFit="contain" style={{ width: THUMB_W, height: th, borderRadius: 4 }} />
                    ) : (
                      <div testId={`slide-thumb-pending-${i + 1}`} style={{ width: THUMB_W, height: th, borderRadius: 4, backgroundColor: C.raised }} />
                    )}
                  </div>
                  <T size={11} color={active ? C.text : C.muted} style={{ minWidth: 0, whiteSpace: 'nowrap', textOverflow: 'ellipsis', overflow: 'hidden' }}>{f.name}</T>
                </div>
              </Pressable>
            </div>
          )
        })}
      </virtual-list>
    </div>
  )
}
