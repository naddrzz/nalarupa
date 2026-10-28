import { createContext, forwardRef, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode, type Ref } from 'react'
import type { EventPayload } from '@gpuix/native'
import { useGpuixRequired, type ImgInstance, type PublicInstance } from '@gpuix/react'
import { ICONS, type IconName } from './icons'
import { C, DPR, FONT, MONO } from './theme'
import type { Rect } from './geometry'
import type { Mods } from './Canvas'

/** Screen-space SVG markup inside `box` (rotated outlines and the vector-edit overlay): GPUI cannot rotate divs, so it paints as an <img>. */
export function SvgOverlay({ body, box, testId }: { body: string; box: Rect; testId?: string }) {
  const img = useRef<ImgInstance>(null)
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${Math.round(box.w * DPR)}" height="${Math.round(box.h * DPR)}" viewBox="${box.x} ${box.y} ${box.w} ${box.h}">${body}</svg>`
  useLayoutEffect(() => img.current?.setImage(Buffer.from(svg)), [svg])
  return <img ref={img} testId={testId} objectFit="fill" style={{ position: 'absolute', left: box.x, top: box.y, width: box.w, height: box.h, pointerEvents: 'none' }} />
}

/** Show a message. Errors stay until dismissed and must say how to recover. */
export type Toast = (message: string, error?: boolean) => void

// Focus rings follow the keyboard (`:focus-visible`). GPUIX sends no `onFocus` when Tab moves focus
// (`focusNext`), so the window's Tab handler reads the focused host id once GPUI has applied it and
// stores it here; a mouse press clears it, so clicking never leaves a ring behind.
let keyboardFocusId: number | null = null
const focusListeners = new Set<() => void>()
export function setKeyboardFocus(id: number | null) {
  if (id === keyboardFocusId) return
  keyboardFocusId = id
  for (const f of focusListeners) f()
}
const subscribeFocus = (f: () => void) => {
  focusListeners.add(f)
  return () => void focusListeners.delete(f)
}
/** Host id that has keyboard focus, or null. */
export const useKeyboardFocusId = () => useSyncExternalStore(subscribeFocus, () => keyboardFocusId)
/** True while an element holds the keyboard focus id (Tab, or a text field press); a Pressable press clears it. */
export const hasKeyboardFocus = () => keyboardFocusId !== null

/** Keyboard focus ring: a 2 px outline 3 px outside the element, drawn over it. Render it as the last
 *  child of a positioned element. (A spread box-shadow does not work: GPUI paints shadows as a filled
 *  box, which showed as a solid block behind transparent buttons.) `inset` draws it on the element's own
 *  edge, for elements that clip their overflow or sit flush against their neighbours. */
export function FocusRing({ radius = 6, inset, testId }: { radius?: number; inset?: boolean; testId?: string }) {
  const o = inset ? 0 : -3
  return <div testId={testId} style={{ position: 'absolute', left: o, top: o, right: o, bottom: o, borderRadius: radius - o, borderWidth: 2, borderColor: C.focus, pointerEvents: 'none' }} />
}

type Handler = (e: EventPayload) => void
type Shortcut = (key: string, mods: Mods) => boolean

/** A focused element owns the keyboard (keys go only to it): the keys it does not use are the editor's shortcuts
 *  (Delete, Ctrl+Z, Escape). Pass this on from `onKeyDown`. */
export const forwardKey = (onShortcut: Shortcut, e: EventPayload) => {
  const m = e.modifiers
  onShortcut((e.key ?? '').toLowerCase(), { mod: !!(m?.ctrl || m?.cmd), shift: !!m?.shift, alt: !!m?.alt })
}
/** The editor's shortcuts for the buttons beneath it: a button keeps keyboard focus after a click. */
export const ShortcutScope = createContext<Shortcut | null>(null)
/** Hands keyboard focus back to the canvas. A text input keeps focus after Enter commits it and drops the editor's
 *  shortcuts (Ctrl+Z never reaches `onKeyDown`: the input binds it), so `Field` and `NumField` call this on Enter. */
export const FocusBack = createContext<(() => void) | null>(null)
type PressableProps = {
  onClick?: () => void; testId?: string; role?: string; label?: string; selected?: boolean; expanded?: boolean
  /** -1 leaves the Tab order (a parent owns the keyboard path, e.g. a tree row's chevron). */
  tabIndex?: number
  style?: Record<string, unknown>; focusTestId?: string; children?: ReactNode | ((focused: boolean) => ReactNode)
  // Passed in by `asChild` wrappers (Tooltip.Trigger) and composed with our own focus handling.
  onMouseEnter?: Handler; onMouseLeave?: Handler; onMouseDown?: Handler; onFocus?: Handler; onBlur?: Handler; onKeyDown?: Handler
}

/**
 * A clickable div with a keyboard path (GPUIX has no `<button>`): a Tab stop, Enter/Space activate,
 * a role and name for screen readers, and a focus ring while focused from the keyboard.
 */
export const Pressable = forwardRef<PublicInstance, PressableProps>(function Pressable(
  { onClick, testId, role = 'button', label, selected, expanded, tabIndex = 0, style, focusTestId, children, onMouseEnter, onMouseLeave, onMouseDown, onFocus, onBlur, onKeyDown },
  ref,
) {
  const { ref: own, on, clear } = useRing(ref)
  const shortcut = useContext(ShortcutScope)
  const radius = typeof style?.borderRadius === 'number' ? style.borderRadius : 6
  return (
    <div
      ref={own}
      testId={testId}
      role={role}
      aria-label={label}
      aria-selected={selected}
      aria-expanded={expanded}
      tabIndex={tabIndex}
      onClick={onClick}
      onMouseEnter={onMouseEnter}
      onMouseLeave={onMouseLeave}
      onMouseDown={(e) => { clear(); onMouseDown?.(e) }}
      onFocus={onFocus}
      onBlur={onBlur}
      onKeyDown={(e) => {
        onKeyDown?.(e)
        const k = (e.key ?? '').toLowerCase()
        // Ctrl/Cmd+Enter is an editor shortcut (Present), not a click.
        const combo = k === 'enter' && !!(e.modifiers?.ctrl || e.modifiers?.cmd)
        if ((k === 'enter' || k === 'space') && !combo) {
          if (!e.isHeld) onClick?.()
        } else if (shortcut) forwardKey(shortcut, e)
      }}
      style={{ cursor: 'pointer', ...style, ...(on ? { position: 'relative' } : {}) }}
    >
      {typeof children === 'function' ? children(on) : children}
      {on ? <FocusRing radius={radius} testId={focusTestId} /> : null}
    </div>
  )
})

/** Focus ring for any focusable element: spread `focusProps` on it and `ring` into its style. */
/** Keyboard focus for a focusable host element: attach `ref` (it also fills `forwarded`), render a
 *  `FocusRing` while `on`, and call `clear` on mouse down. */
export function useRing<T extends PublicInstance>(forwarded?: Ref<T>) {
  const el = useRef<T | null>(null)
  const id = useKeyboardFocusId()
  const ref = useCallback((v: T | null) => {
    el.current = v
    if (typeof forwarded === 'function') forwarded(v)
    else if (forwarded) (forwarded as { current: T | null }).current = v
  }, [forwarded])
  const on = id !== null && el.current?.id === id
  return { ref, el, on, clear: () => setKeyboardFocus(null) }
}

/** Scroll the container that gets `ref` (an `overflowY: 'scroll'` panel) to show the control Tab just focused, ring
 *  included. GPUI does not scroll on focus, and `scrollIntoView` only brings the container's direct child into view. */
export function useScrollToFocus() {
  const renderer = useGpuixRequired()
  const ref = useRef<PublicInstance>(null)
  const focus = useKeyboardFocusId()
  useEffect(() => {
    const [box, at, off] = ref.current && focus !== null
      ? [renderer.getElementBounds?.(ref.current.id), renderer.getElementBounds?.(focus), renderer.getScrollOffset?.(ref.current.id)]
      : []
    // Focus in another panel is none of this container's business.
    if (!box || !at || !off || at.x < box.x || at.x + at.width > box.x + box.width) return
    const ring = 3
    const dy = at.y - ring < box.y ? box.y - (at.y - ring) : Math.min(0, box.y + box.height - (at.y + at.height + ring))
    if (dy) renderer.scrollTo?.(ref.current!.id, off[0], Math.min(0, off[1] + dy))
  }, [focus])
  return ref
}

// GPUIX tracks a text field's focus handle on two nested divs, so GPUI's tab-stop list holds the field
// twice and `focusPrevious` from it lands on the field again: Shift+Tab never leaves. Each field keeps a
// `back` stop (a focusable div that is no tab stop) right before it; focusing that and walking back from
// it reaches the stop before the field.
const backStops = new Map<number, number>()
/** Host id of the `back` stop that sits before the text field `fieldId`, if it is one. */
export const backStopOf = (fieldId: number | null | undefined) => (fieldId == null ? undefined : backStops.get(fieldId))
const noKey = () => {}

/** Focus state of a text field: `ring` is true while it has focus, from the mouse or Tab. GPUIX sends no
 *  focus event for Tab, nor any for a click while its window is inactive (the background e2e window), so
 *  a press on `container` (the field's box; a press on the field bubbles to it) asks the renderer what
 *  has focus, as Tab does in `app.tsx`. Spread `ref`, `onFocus` and `onBlur` on the field, `onMouseDown`
 *  on its container, and draw a `FocusRing` while `ring`. Render `back` right before the field. */
export function useFieldRing(onFocus?: Handler, onBlur?: Handler) {
  const renderer = useGpuixRequired()
  const [focus, setFocus] = useState(false)
  const el = useRef<PublicInstance | null>(null)
  const { ref: own, on } = useRing()
  const ref = useCallback((v: PublicInstance | null) => { el.current = v; own(v) }, [own])
  const stop = useRef<PublicInstance | null>(null)
  useLayoutEffect(() => {
    const field = el.current?.id
    if (field === undefined || stop.current === null) return
    backStops.set(field, stop.current.id)
    return () => void backStops.delete(field)
  })
  // Leave the id alone when it already names another element: Tab sets it before the blur arrives.
  const release = () => { setFocus(false); if (el.current?.id === keyboardFocusId) setKeyboardFocus(null) }
  return {
    ref,
    back: <div ref={stop} onKeyDown={noKey} style={{ position: 'absolute', width: 0, height: 0 }} />,
    ring: focus || on,
    onFocus: (e: EventPayload) => { setFocus(true); onFocus?.(e) },
    onBlur: (e: EventPayload) => { release(); onBlur?.(e) },
    /** Drop the ring after focus was moved away from code (`focusElement`): the field gets no blur event then. */
    release,
    onMouseDown: () => void setTimeout(() => setKeyboardFocus(renderer.getFocusedElementId?.() ?? null), 30),
  }
}

export function Icon({ name, size = 15, color = C.muted }: { name: IconName; size?: number; color?: string }) {
  return <svg source={ICONS[name]} style={{ width: size, height: size, flexShrink: 0, color }} />
}

export function T({ children, size = 12, color = C.text, weight, mono, grow, style }: {
  children: ReactNode; size?: number; color?: string; weight?: string; mono?: boolean; grow?: boolean; style?: Record<string, unknown>
}) {
  return (
    <text style={{ fontSize: size, color, fontFamily: mono ? MONO : FONT, fontWeight: weight, flexGrow: grow ? 1 : undefined, ...style }}>
      {children}
    </text>
  )
}

export const IconButton = forwardRef<PublicInstance, {
  icon: IconName; onClick?: () => void; active?: boolean; title?: string; testId?: string; focusTestId?: string; color?: string; size?: number
}>(function IconButton({ icon, onClick, active, title, testId, focusTestId, color, size = 28 }, ref) {
  return (
    <Pressable
      ref={ref}
      testId={testId}
      focusTestId={focusTestId}
      label={title}
      selected={active}
      onClick={onClick}
      style={{
        width: size, height: size, borderRadius: 6, display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0,
        backgroundColor: active ? C.accentSoft : undefined,
        hover: { backgroundColor: active ? C.accentSoft : C.raised },
        active: { backgroundColor: C.border },
      }}
    >
      <Icon name={icon} color={color ?? (active ? C.accent : C.muted)} />
    </Pressable>
  )
})

export function Button({ children, onClick, primary, icon, testId, focusTestId, grow, danger }: {
  children: ReactNode; onClick?: () => void; primary?: boolean; icon?: IconName; testId?: string; focusTestId?: string; grow?: boolean; danger?: boolean
}) {
  const fg = primary ? '#16110F' : danger ? C.danger : C.text
  return (
    <Pressable
      testId={testId}
      focusTestId={focusTestId}
      onClick={onClick}
      style={{
        display: 'flex', flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: 6,
        height: 30, paddingLeft: 12, paddingRight: 12, borderRadius: 7, flexGrow: grow ? 1 : undefined,
        backgroundColor: primary ? C.accent : C.raised,
        borderWidth: primary ? 0 : 1, borderColor: C.border,
        hover: { backgroundColor: primary ? '#FF8059' : '#303036' },
        active: { backgroundColor: primary ? '#E85A2F' : C.border },
      }}
    >
      {icon ? <Icon name={icon} size={14} color={fg} /> : null}
      <T color={fg} weight={primary ? '600' : undefined}>{children}</T>
    </Pressable>
  )
}

/**
 * `<input>` that shows the start of a long value while unfocused. GPUIX leaves a value set from outside
 * scrolled to its end (caret there) and has no API to scroll it back, so the input's own text is made
 * transparent until it has focus and a clipped `<text>` paints the value beneath it. `useFieldRing`
 * tells focus from the mouse and from Tab, and the box draws a `FocusRing` while it has either.
 */
function TextInput({ value, mono, onFocus, onBlur, ...rest }: {
  value: string; mono?: boolean; testId?: string; placeholder?: string; onFocus?: Handler; onBlur?: Handler
  'aria-label'?: string; onChange?: Handler; onSubmit?: Handler; onKeyDown?: Handler; theme?: Record<string, unknown>
}) {
  const field = useFieldRing(onFocus, onBlur)
  const shown = !field.ring && value !== ''
  const fontFamily = mono ? MONO : FONT
  return (
    <div onMouseDown={field.onMouseDown} style={{ position: 'relative', flexGrow: 1, minWidth: 0, height: 26, borderRadius: 6, backgroundColor: C.panel2, borderWidth: 1, borderColor: C.field, display: 'flex', alignItems: 'center', paddingLeft: 7, paddingRight: 4 }}>
      <div style={{ flexGrow: 1, minWidth: 0, alignSelf: 'stretch', position: 'relative', display: 'flex', alignItems: 'center' }}>
        {shown ? (
          <div style={{ position: 'absolute', left: 0, top: 0, right: 0, bottom: 0, display: 'flex', alignItems: 'center', overflow: 'hidden', pointerEvents: 'none' }}>
            <T mono={mono} color={C.text} grow style={{ whiteSpace: 'nowrap', textOverflow: 'ellipsis', overflow: 'hidden' }}>{value}</T>
          </div>
        ) : null}
        {field.back}
        <input
          {...rest}
          ref={field.ref}
          value={value}
          onFocus={field.onFocus}
          onBlur={field.onBlur}
          style={{ flexGrow: 1, minWidth: 0, fontSize: 12, color: shown ? '#00000000' : C.text, fontFamily }}
        />
      </div>
      {field.ring ? <FocusRing /> : null}
    </div>
  )
}

/** Text field that commits on Enter or blur (not on every keystroke). `onCommit` returns false to reject: the field reverts. */
export function Field({ label, value, onCommit, width, mono, testId, placeholder, name }: {
  label?: string; value: string; onCommit: (v: string) => void | boolean; width?: number; mono?: boolean; testId?: string; placeholder?: string
  /** Accessible name when the visible label is the section title (a placeholder is not a label). */
  name?: string
}) {
  const back = useContext(FocusBack)
  const [draft, setDraft] = useState(value)
  // Typed and committed values as of the last event: Enter and the blur after it may both run before a
  // re-render, and the blur must neither send a stale draft nor repeat the commit.
  const typed = useRef(value)
  const sent = useRef(value)
  useEffect(() => {
    setDraft(value)
    typed.current = sent.current = value
  }, [value])
  /** False when `onCommit` rejected the value (the field reverts and keeps focus, so it can be fixed). */
  const commit = (v = typed.current) => {
    typed.current = v
    if (v === sent.current) return true
    sent.current = v
    if (onCommit(v) === false) {
      typed.current = sent.current = value
      setDraft(value)
      return false
    }
    return true
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, width, flexGrow: width ? undefined : 1, minWidth: 0 }}>
      {label ? <T size={11} color={C.ghost} style={{ width: 14 }}>{label}</T> : null}
      <TextInput
        testId={testId}
        aria-label={name ?? label}
        value={draft}
        mono={mono}
        placeholder={placeholder}
        onChange={(e) => setDraft((typed.current = e.value ?? ''))}
        onSubmit={(e) => { if (commit(e.value ?? typed.current)) back?.() }}
        onBlur={() => commit()}
        theme={{ caret: C.accent }}
      />
    </div>
  )
}

// Label scrubbing: GPUIX keeps sending moves to the pressed element, so the label sees the whole drag.
let scrub: { x: number; at: number; last: number; apply: (steps: number) => void } | null = null
let gesture = 0
function scrubMove(e: EventPayload) {
  if (!scrub) return
  // No button at a new spot: released elsewhere. Same spot: GPUI's hover refresh, ignore.
  if (e.pressedButton === undefined) return void (Math.abs((e.x ?? 0) - scrub.at) > 0.5 && (scrub = null))
  scrub.at = e.x ?? 0
  const dx = (e.x ?? 0) - scrub.x
  // 2 px per step; Shift moves in tens (10 steps per 20 px).
  const steps = e.modifiers?.shift ? Math.trunc(dx / 20) * 10 : Math.trunc(dx / 2)
  if (steps !== scrub.last) {
    scrub.last = steps
    scrub.apply(steps)
  }
}

/**
 * Number field: drag the label to scrub, Up/Down ±step (Shift ×10), Enter/blur
 * commits. `onCommit` gets a coalescing key: one focus session or one scrub = one undo step.
 */
export function NumField({ label, name = label, value, mixed, onCommit, onStep, step = 1, min, max, testId, width }: {
  label: string; value: number; mixed?: boolean; onCommit: (v: number, coalesce: string) => void
  /** Up/Down and scrub on a mixed field send the total change since the gesture began, so each value can move from its own start (a typed number still goes through `onCommit`). */
  onStep?: (delta: number, coalesce: string) => void; step?: number; min?: number; max?: number; testId?: string; width?: number
  /** Accessible name when the visible label is a letter or symbol ("W", "↻"). */
  name?: string
}) {
  const back = useContext(FocusBack)
  const fmt = (v: number) => String(Number(v.toFixed(2)))
  const shown = mixed ? '' : fmt(value)
  const [draft, setDraft] = useState(shown)
  // Latest committed value: key repeats can arrive before React re-renders.
  const latest = useRef(shown)
  latest.current = draft
  useEffect(() => setDraft(shown), [shown])
  const session = useRef<string | null>(null)
  const stepped = useRef(0)
  const key = () => (session.current ??= `field:${testId ?? label}:${++gesture}`)
  const commit = (v: number, k = key()) => {
    if (!Number.isFinite(v)) return setDraft(shown)
    // The draft shows what is stored: a value the caller would clamp never changes `shown`, so nothing would resync it.
    v = Math.min(max ?? Infinity, Math.max(min ?? -Infinity, v))
    latest.current = fmt(v)
    setDraft(latest.current)
    onCommit(v, k)
  }
  /** False when the draft is not a number: the field reverts and keeps focus. */
  const typed = () => {
    if (draft === shown) return true
    // An empty field is not 0: Number('') would commit it as 0.
    const t = draft.trim()
    const v = t === '' ? NaN : Number(t)
    commit(v)
    return Number.isFinite(v)
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 6, width, flexGrow: width ? undefined : 1, minWidth: 0 }}>
      <div
        testId={testId ? `${testId}-scrub` : undefined}
        role="slider"
        aria-label={`${name}: drag to change`}
        onMouseDown={(e) => {
          const base = mixed ? 0 : Number(latest.current) || 0
          const k = `scrub:${testId ?? label}:${++gesture}`
          stepped.current = 0
          scrub = { x: e.x ?? 0, at: e.x ?? 0, last: 0, apply: (steps) => mixed && onStep ? onStep(Math.round(steps * step * 100) / 100, k) : commit(Math.round((base + steps * step) * 100) / 100, k) }
        }}
        onMouseMove={scrubMove}
        onMouseUp={() => { scrub = null }}
        style={{ width: 14, height: 26, display: 'flex', alignItems: 'center', cursor: 'ew-resize', userSelect: 'none' }}
      >
        <T size={11} color={C.ghost}>{label}</T>
      </div>
      <TextInput
        testId={testId}
        aria-label={name}
        value={draft}
        placeholder={mixed ? 'Mixed' : undefined}
        onChange={(e) => setDraft(e.value ?? '')}
        onSubmit={() => { if (typed()) back?.() }}
        onBlur={() => {
          typed()
          session.current = null
          stepped.current = 0
        }}
        onKeyDown={(e) => {
          const k = (e.key ?? '').toLowerCase()
          if (k !== 'up' && k !== 'down') return
          const delta = (k === 'up' ? 1 : -1) * step * (e.modifiers?.shift ? 10 : 1)
          if (mixed && onStep) {
            stepped.current = Math.round((stepped.current + delta) * 100) / 100
            return onStep(stepped.current, key())
          }
          const base = Number(latest.current) || 0
          commit(Math.round((base + delta) * 100) / 100)
        }}
        theme={{ caret: C.accent }}
      />
    </div>
  )
}

/**
 * A dropdown's trigger. A press outside the open dropdown that lands on the trigger is left to the
 * trigger's own click (which closes it): closing in `onMouseDownOutside` too made that click reopen
 * it, and the old workaround (ignore clicks for 500 ms) swallowed a quick reopen. `restore` hands keyboard
 * focus back to the trigger when the dropdown closes from the keyboard (its panel unmounts with the focus).
 */
export function useTrigger() {
  const renderer = useGpuixRequired()
  const ref = useRef<PublicInstance>(null)
  const hit = (e: EventPayload) => {
    const b = ref.current && renderer.getElementBounds?.(ref.current.id)
    const [x, y] = [e.x ?? -1, e.y ?? -1]
    return !!b && x >= b.x && x <= b.x + b.width && y >= b.y && y <= b.y + b.height
  }
  const restore = () => {
    if (!ref.current) return
    renderer.focusElement?.(ref.current.id)
    setKeyboardFocus(ref.current.id)
  }
  return { ref, hit, restore }
}

/** Small toggle chip (checkbox semantics). */
export function Toggle({ label, on, onChange, testId }: { label: string; on: boolean; onChange: (on: boolean) => void; testId?: string }) {
  return (
    <Pressable
      testId={testId}
      role="checkbox"
      label={label}
      selected={on}
      onClick={() => onChange(!on)}
      style={{
        height: 24, paddingLeft: 8, paddingRight: 8, borderRadius: 5, display: 'flex', alignItems: 'center',
        borderWidth: 1, borderColor: on ? C.select : C.border, backgroundColor: on ? '#3B82F622' : C.panel2,
        hover: { backgroundColor: on ? '#3B82F633' : C.raised },
      }}
    >
      <T size={11} color={on ? C.text : C.muted}>{label}</T>
    </Pressable>
  )
}

export function Section({ title, children, right }: { title: string; children: ReactNode; right?: ReactNode }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, paddingTop: 12, paddingBottom: 12, paddingLeft: 14, paddingRight: 14, borderBottomWidth: 1, borderColor: C.border }}>
      <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center' }}>
        <T size={11} color={C.muted} weight="600" grow>{title}</T>
        {right}
      </div>
      {children}
    </div>
  )
}

export function Row({ children, gap = 8 }: { children: ReactNode; gap?: number }) {
  return <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap }}>{children}</div>
}

/** Small segmented control (one choice of several). `label` names the group for screen readers;
 *  an option's `name` replaces its visible label when that label is a terse symbol like "2×". */
export function Segmented<V extends string>({ value, options, onChange, label }: { value: V; options: { value: V; label: string; name?: string; testId?: string }[]; onChange: (v: V) => void; label?: string }) {
  return (
    <div role="tablist" aria-label={label} style={{ display: 'flex', flexDirection: 'row', gap: 2, padding: 2, borderRadius: 7, backgroundColor: C.panel2, borderWidth: 1, borderColor: C.border }}>
      {options.map((o) => (
        <Pressable
          key={o.value}
          testId={o.testId}
          focusTestId={o.testId ? `${o.testId}-focus` : undefined}
          role="tab"
          label={o.name ?? o.label}
          selected={o.value === value}
          onClick={() => onChange(o.value)}
          style={{
            flexGrow: 1, flexBasis: 0, height: 24, borderRadius: 5, display: 'flex', alignItems: 'center', justifyContent: 'center',
            paddingLeft: 6, paddingRight: 6,
            backgroundColor: o.value === value ? C.raised : undefined,
            hover: { backgroundColor: C.raised },
          }}
        >
          <T size={11} color={o.value === value ? C.text : C.muted}>{o.label}</T>
        </Pressable>
      ))}
    </div>
  )
}

export function Swatch({ color, testId }: { color: string; testId?: string }) {
  return <div testId={testId} style={{ width: 18, height: 18, borderRadius: 4, backgroundColor: color, borderWidth: 1, borderColor: '#FFFFFF22', flexShrink: 0 }} />
}
