import { useEffect, useLayoutEffect, useMemo, useRef, useState, type MutableRefObject, type Ref } from 'react'
import type { EventModifiers, EventPayload } from '@gpuix/native'
import type { CursorValue } from '@gpuix/native/host'
import { useGpuixRequired, type ImgInstance, type PublicInstance } from '@gpuix/react'
import { call, renderPixels, tryCall, type Box } from './core'
import { CANVAS_BG, C, DPR, FONT, FRAME_LABEL_TEXT, LEFT_W, TOP_H } from './theme'

import { FocusRing, setKeyboardFocus, SvgOverlay, T, useRing, type Toast } from './ui'
import { AgentLayer } from './AgentLayer'
import {
  anchorPoints, apply, corners, dragPoints, lin, measure, own, painted, resizeBox, resizeCursor, snap, turnAroundCenter, turned, unapply, unionRect,
  type Guide, type M, type Measure, type Pt, type Rect,
} from './geometry'
import {
  ANCHOR, DRAG as PEN_DRAG, HIT as PEN_HIT, POINT_SNAP, clone, constrain, deletePoints, drawable, endOf, endpoint, hitAnchor, hitHandle as hitPenHandle, hitSegment, mirror,
  mirrored, other, overlay, pointAt, pt as ptOf, round, roundRay, shownHandles, snapPoints, snapRay, split, toggleSmooth,
  type Anchor, type Contour, type Seg, type Side,
} from './pen'

// The canvas background behind the bitmap, as CSS (shown until the first frame is drawn).
const CANVAS_BG_CSS = `#${CANVAS_BG.toString(16).padStart(6, '0')}`

export type Tool = 'move' | 'frame' | 'rectangle' | 'ellipse' | 'text' | 'hand' | 'pen'
export type Cam = { x: number; y: number; zoom: number }
export type Mods = { mod: boolean; shift: boolean; alt: boolean }

const HANDLES = ['nw', 'n', 'ne', 'e', 'se', 's', 'sw', 'w'] as const
type Handle = (typeof HANDLES)[number]
/** Per corner tl, tr, br, bl (cornerRadius order): the direction toward the box's center. */
const INWARD: Pt[] = [[1, 1], [-1, 1], [-1, -1], [1, -1]]

// Overlay constants (screen px): snap threshold and color, drag threshold, handle hit areas.
const SNAP = '#F24518'
/** Measure-label fill: white 11 px text needs 4.5:1 (on SNAP it is 3.7:1). */
const SNAP_LABEL = '#D9380F'
const SNAP_PX = 5
const DRAG_PX = 4
const HIT = 7.5
const ROTATE_HIT = 17.5
const X_MARK = '<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8" viewBox="0 0 8 8"><path d="M2 2L6 6M6 2L2 6" fill="none" stroke="#000" stroke-width="1.2" stroke-linecap="round"/></svg>'

/** Result of the core's `drop_target`: where a drag would land (auto-layout targets carry index + line). */
type Drop = { id: string; layout: string; x?: number; y?: number; w?: number; h?: number; m?: M | null; index?: number; line?: [number, number, number, number] }
type Item = { id: string; box: Box; node: any; movable: boolean }
/** A rotated outline or handle in screen px (drawn through `Quads`). */
type Quad = { pts: Pt[]; stroke: string; width: number; fill?: string }

type Drag =
  | { mode: 'pan'; sx: number; sy: number; cam: Cam }
  | {
      mode: 'move'; sx: number; sy: number; dx0: number; dy0: number; ids: string[]; narrow?: string; toggle?: string; moved: boolean
      start: Rect; sentX: number; sentY: number; anchors: Pt[]; parent: string; pm: M | null; groupOf?: string; from?: string; mixed?: boolean; flow: boolean
      container: Drop | null; index?: number; deferred?: Drop; lock?: 0 | 1; dup?: { originals: string[]; offAt?: number }
    }
  // `box` and the start point `f0` are in the selection frame's coordinates; `m` maps them to the canvas.
  | { mode: 'resize'; handle: Handle; f0: Pt; box: Rect; m: M | null; items: Item[]; anchors: Pt[] }
  | { mode: 'rotate'; cx: number; cy: number; a0: number; rot0: number; flipX: boolean; flipY: boolean; id: string; box: Box; x0: number; y0: number; movable: boolean }
  | { mode: 'radius'; id: string; corner: number; f0: Pt; m: M | null; r0: number[] }
  | { mode: 'create'; tool: Tool; dx0: number; dy0: number; dx1: number; dy1: number }
  | { mode: 'marquee'; sx: number; sy: number; dx0: number; dy0: number; base: string[]; deep: boolean; shift: boolean; moved: boolean }
  | { mode: 'pen'; sx: number; sy: number; moved: boolean; target: PenDrag; click?: () => void }

/**
 * Vector editing state of one path: its contours in canvas coordinates, the selected anchors,
 * the sub-tool (pen adds points, move edits them) and the open contour the pen extends. `id` is null until
 * a new path has two points.
 */
type PenEdit = {
  id: string | null; parent: string; cs: Contour[]; sel: Set<Anchor>; sub: 'pen' | 'move'
  appending?: { c: Contour; end: Side }; action?: PenAction; version?: number
}
/** What a press would do. */
type PenAction =
  | { kind: 'start'; at: Pt; anchor?: Anchor; mid?: Pt }
  | { kind: 'place'; at: Pt; target?: Anchor }
  | { kind: 'close'; anchor: Anchor; at: Pt }
  | { kind: 'blocked' }
  | { kind: 'resume'; anchor: Anchor; c: Contour; end: Side }
  | { kind: 'handle'; anchor: Anchor; side: Side }
  | { kind: 'points'; anchor?: Anchor; seg?: Seg }
  | { kind: 'insert'; seg: Seg; t: number; at: Pt }
  | { kind: 'box' }
type PenDrag =
  | { kind: 'handle'; anchor: Anchor; side: Side; mirror: boolean; connect?: boolean }
  | { kind: 'points'; start: Pt; from: Map<Anchor, Anchor> }
  | { kind: 'box'; from: Pt; base: Set<Anchor> }
  | { kind: 'none' }
const PEN_BLUE = '#3D99FF'

export function fitCam(b: { x: number; y: number; w: number; h: number }, vw: number, vh: number, pad = 80, max = 1): Cam {
  const zoom = Math.min((vw - pad * 2) / Math.max(b.w, 1), (vh - pad * 2) / Math.max(b.h, 1), max)
  const z = Math.max(0.02, zoom)
  return { zoom: z, x: b.x - (vw / z - b.w) / 2, y: b.y - (vh / z - b.h) / 2 }
}

function handlePoints(b: { x: number; y: number; w: number; h: number }): Record<Handle, [number, number]> {
  const { x, y, w, h } = b
  return { nw: [x, y], n: [x + w / 2, y], ne: [x + w, y], e: [x + w, y + h / 2], se: [x + w, y + h], s: [x + w / 2, y + h], sw: [x, y + h], w: [x, y + h / 2] }
}

export const isMovable = (n: any) => n && (n.parentLayout === 'none' || n.node?.layoutPosition === 'absolute')
const inFlow = (n: any) => !!n && n.parentLayout !== 'none' && n.node?.layoutPosition !== 'absolute'
/** cornerRadius as [tl, tr, br, bl]; variable references count as 0. */
function radiiOf(n: any): number[] {
  const r = n?.cornerRadius
  return (Array.isArray(r) && r.length === 4 ? r : [r, r, r, r]).map((v: unknown) => Math.max(0, Number(v) || 0))
}

/** Choose what a click selects from the hit chain (top-level → deepest), Figma style. */
function pickTarget(chain: string[], selection: string[], boxes: Map<string, Box>, deep: boolean): string | undefined {
  if (!chain.length) return undefined
  if (deep) return chain[chain.length - 1]
  // A selected top-level frame stays an artboard: clicks inside it reach its children.
  const selected = chain.findIndex((id) => selection.includes(id))
  if (selected > 0 || (selected === 0 && chain.length === 1)) return chain[selected]
  for (const s of selection) {
    const parent = boxes.get(s)?.parent
    const i = parent ? chain.indexOf(parent) : -1
    if (i >= 0 && chain[i + 1]) return chain[i + 1]
  }
  return chain.length > 1 ? chain[1] : chain[0]
}

/** Axis-aligned screen line. */
function Line({ x1, y1, x2, y2, color, width = 1 }: { x1: number; y1: number; x2: number; y2: number; color: string; width?: number }) {
  const v = Math.abs(x1 - x2) < 0.5
  return (
    <div style={{
      position: 'absolute', pointerEvents: 'none', backgroundColor: color,
      left: v ? x1 - width / 2 : Math.min(x1, x2), top: v ? Math.min(y1, y2) : y1 - width / 2,
      width: v ? width : Math.abs(x2 - x1), height: v ? Math.abs(y2 - y1) : width,
    }} />
  )
}

/** Axis-aligned dashed screen line (GPUI has no dashed borders). */
function Dashed({ x1, y1, x2, y2, color, width = 1, dash }: { x1: number; y1: number; x2: number; y2: number; color: string; width?: number; dash: number }) {
  const v = Math.abs(x1 - x2) < 0.5
  const [a, b] = v ? [Math.min(y1, y2), Math.max(y1, y2)] : [Math.min(x1, x2), Math.max(x1, x2)]
  const segs: number[] = []
  for (let p = a; p < b && segs.length < 400; p += dash * 2) segs.push(p)
  return <>{segs.map((p) => v
    ? <Line key={p} x1={x1} y1={p} x2={x1} y2={Math.min(b, p + dash)} color={color} width={width} />
    : <Line key={p} x1={p} y1={y1} x2={Math.min(b, p + dash)} y2={y1} color={color} width={width} />)}</>
}

function Outline({ r, color, width = 1, testId }: { r: { x: number; y: number; w: number; h: number }; color: string; width?: number; testId?: string }) {
  return <div testId={testId} style={{ position: 'absolute', left: r.x - width, top: r.y - width, width: r.w + width * 2, height: r.h + width * 2, borderWidth: width, borderColor: color, pointerEvents: 'none' }} />
}

/** Rotated outlines and handles: GPUI cannot rotate divs, so they become one full-colour SVG on an <img>. */
function Quads({ quads, testId }: { quads: Quad[]; testId?: string }) {
  const img = useRef<ImgInstance>(null)
  const pts = quads.flatMap((q) => q.pts)
  const x = Math.floor(Math.min(...pts.map((p) => p[0]))) - 2
  const y = Math.floor(Math.min(...pts.map((p) => p[1]))) - 2
  const w = Math.ceil(Math.max(...pts.map((p) => p[0]))) + 2 - x
  const h = Math.ceil(Math.max(...pts.map((p) => p[1]))) + 2 - y
  const r = (v: number) => Math.round(v * 100) / 100
  const shapes = quads.map((q) => `<polygon points="${q.pts.map(([px, py]) => `${r(px - x)},${r(py - y)}`).join(' ')}" fill="${q.fill ?? 'none'}" stroke="${q.stroke}" stroke-width="${q.width}"/>`).join('')
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${Math.round(w * DPR)}" height="${Math.round(h * DPR)}" viewBox="0 0 ${w} ${h}">${shapes}</svg>`
  useLayoutEffect(() => img.current?.setImage(Buffer.from(svg)), [svg])
  return <img ref={img} testId={testId} objectFit="fill" style={{ position: 'absolute', left: x, top: y, width: w, height: h, pointerEvents: 'none' }} />
}

export function Canvas(props: {
  w: number; h: number; cam: Cam; setCam: (c: Cam) => void; version: number; selection: string[]
  select: (ids: string[]) => void; tool: Tool; setTool: (t: Tool) => void; toast: Toast
  onShortcut: (key: string, mods: Mods) => boolean
  /** Set to the pen-key handler (false = not editing points or not handled) so keys from other panels reach it. */
  penKeyRef?: MutableRefObject<((key: string, mods: Mods) => boolean) | null>
  hoverId?: string | null
  chatOpen?: boolean
  /** Agent-inserted nodes are sliding in: the core paints them per frame. */
  animating?: boolean
  rootRef?: Ref<PublicInstance>
}) {
  const { w, h, cam, setCam, version, selection, select, tool, setTool, toast } = props
  // Keyboard focus ring (Tab onto the canvas); a press clears it in onMouseDown.
  const ring = useRing(props.rootRef)
  const renderer = useGpuixRequired()
  // An in-place editor unmounts while it holds focus: Enter or Escape hands the keys back to the canvas.
  const refocus = () => void (ring.el.current && renderer.focusElement?.(ring.el.current.id))
  const textEl = useRef<PublicInstance>(null)

  // Tell the core what the user sees, so the agent's editor state marks visible frames.
  useEffect(() => {
    const t = setTimeout(() => tryCall('viewport', { x: cam.x, y: cam.y, w: w / cam.zoom, h: h / cam.zoom }), 250)
    return () => clearTimeout(t)
  }, [cam, w, h])
  const img = useRef<ImgInstance>(null)
  const drag = useRef<Drag | null>(null)
  const space = useRef(false)
  // Cursor feedback: an open hand while panning is armed (Space / Hand tool), a closed one while dragging.
  const [spaceDown, setSpaceDown] = useState(false)
  const [panning, setPanning] = useState(false)
  const lastPt = useRef<[number, number]>([0, 0])
  const stray = useRef(0)
  const live = useRef({ cam, w, h, version })
  live.current = { cam, w, h, version }
  const liveSel = useRef(selection)
  liveSel.current = selection
  const [ghost, setGhost] = useState<{ x: number; y: number; w: number; h: number } | null>(null)
  const [hover, setHover] = useState<Box | null>(null)
  const [editing, setEditing] = useState<{ id: string; box: Box; value: string; size: number } | null>(null)
  const editLive = useRef(editing)
  editLive.current = editing
  // The editor opens with its text selected, so typing replaces it. GPUIX has no select-on-focus, so once the
  // field holds focus the select-all chord goes through its input pipeline (sent while the canvas still held
  // focus it would select every layer). Polls until focus lands, however late; closing the editor stops it.
  const editId = editing?.id
  useEffect(() => {
    if (!editId) return
    const timer = setInterval(() => {
      const el = textEl.current
      if (el && renderer.getFocusedElementId?.() === el.id) {
        clearInterval(timer)
        ;(renderer as unknown as { simulateKeystrokes?(keys: string): void }).simulateKeystrokes?.(process.platform === 'darwin' ? 'cmd-a' : 'ctrl-a')
      }
    }, 25)
    return () => clearInterval(timer)
  }, [editId, renderer])
  const [renaming, setRenaming] = useState<{ id: string; draft: string } | null>(null)
  const renameLive = useRef(renaming)
  renameLive.current = renaming
  const lastLabel = useRef<{ id?: string; at: number }>({ at: 0 })
  const [tick, setTick] = useState(0)
  const [guides, setGuides] = useState<Guide[]>([])
  const [marquee, setMarquee] = useState<Rect | null>(null)
  const [drop, setDrop] = useState<Drop | null>(null)
  const [float, setFloat] = useState<Rect | null>(null)
  const [measured, setMeasured] = useState<{ s: Rect; t: Box; lines: Measure[] } | null>(null)
  const [cursor, setCursor] = useState<CursorValue>('default')
  // Vector editing (pen tool, point editing) mutates `pen` in place; `penRedraw` repaints the overlay.
  const pen = useRef<PenEdit | null>(null)
  const [, setPenTick] = useState(0)
  const penRedraw = () => setPenTick((t) => t + 1)
  const [penDot, setPenDot] = useState<Pt | null>(null)
  const lastPress = useRef<{ key?: unknown; at: number }>({ at: 0 })

  // Coalesced rasterization: at most one render in flight, always with the latest camera.
  // While the camera moves the core may scale its cached view; 200 ms after it settles
  // a sharp frame replaces it.
  const pending = useRef<ReturnType<typeof setTimeout> | null>(null)
  const settle = useRef<ReturnType<typeof setTimeout> | null>(null)
  // What the bitmap shows (null until the first frame): a pan paints right away, so the commit's
  // scheduled frame can skip it, but a fresh mount has nothing to skip.
  const drawn = useRef<typeof live.current | null>(null)
  // The bitmap stays 0×0 until it holds a frame: GPUI can present a new <img> (a fresh mount, as
  // when leaving Present) before any commit-time draw, and an empty one shows its placeholder.
  const [hasFrame, setHasFrame] = useState(false)
  const framed = useRef(false)
  const draw = (sharp: boolean) => {
    drawn.current = live.current
    const { cam: c, w: vw, h: vh } = live.current
    if (vw < 2 || vh < 2) return
    const pw = Math.round(vw * DPR)
    const ph = Math.round(vh * DPR)
    const px = renderPixels(pw, ph, c.x, c.y, c.zoom * DPR, CANVAS_BG, undefined, sharp)
    if (px && img.current) {
      img.current.setImagePixels(pw, ph, px)
      if (!framed.current) {
        framed.current = true
        setHasFrame(true)
      }
    }
  }
  /** Camera moves from pans and the wheel: paint the bitmap now, in the same task as the overlays'
   *  re-render, instead of after the commit and a timer (each pan step used to wait ~10–20 ms). */
  const moveCam = (next: Cam) => {
    live.current = { ...live.current, cam: next }
    draw(false)
    setCam(next)
  }
  const schedule = () => {
    // The first frame draws in the commit itself: GPUI may present the new <img> before a timer runs.
    if (!drawn.current) draw(false)
    if (!pending.current) {
      pending.current = setTimeout(() => {
        pending.current = null
        const [a, b] = [drawn.current, live.current]
        if (!a || a.cam !== b.cam || a.w !== b.w || a.h !== b.h || a.version !== b.version) draw(false)
      }, 0)
    }
    if (settle.current) clearTimeout(settle.current)
    settle.current = setTimeout(() => { settle.current = null; draw(true) }, 200)
  }
  useLayoutEffect(schedule, [w, h, cam, version])
  const animated = useRef(false)
  useEffect(() => {
    if (!props.animating) {
      // The poll can end the slide between two redraws: paint the settled view once more, or the
      // bitmap keeps the last slide frame (neither the camera nor `version` changed to repaint it).
      // Only when the slide ends, not on mount or unmount (that frame would be thrown away).
      if (animated.current) draw(false)
      animated.current = false
      return
    }
    animated.current = true
    const t = setInterval(() => draw(false), 16)
    return () => clearInterval(t)
  }, [props.animating])
  useEffect(() => () => {
    if (pending.current) clearTimeout(pending.current)
    if (settle.current) clearTimeout(settle.current)
  }, [])

  const frames = useMemo(() => tryCall<Box[]>('frames') ?? [], [version])
  const selBoxes = useMemo(() => (selection.length ? tryCall<Box[]>('boxes', { ids: selection }) ?? [] : []), [selection, version, tick])
  const boxMap = useMemo(() => new Map(selBoxes.map((b) => [b.id, b])), [selBoxes])
  const selUnion = useMemo(() => unionRect(selBoxes.map(painted)), [selBoxes])
  // Selection frame: one node in its own (rotated/flipped) box, several in their painted bounds.
  const frame = useMemo<{ box: Rect; m: M | null } | null>(
    () => (selBoxes.length === 1 ? { box: selBoxes[0], m: selBoxes[0].m ?? null } : selUnion && { box: selUnion, m: null }),
    [selBoxes, selUnion],
  )
  const selNode = useMemo(() => (selection.length === 1 ? tryCall('node', { id: selection[0] }) : null), [selection, version, tick])
  const hoverBox = useMemo(() => (props.hoverId ? (tryCall<Box[]>('boxes', { ids: [props.hoverId] }) ?? [])[0] ?? null : null), [props.hoverId, version, tick])

  const toScreen = (x: number, y: number) => [(x - cam.x) * cam.zoom, (y - cam.y) * cam.zoom] as const
  const local = (e: EventPayload) => [(e.x ?? 0) - LEFT_W, (e.y ?? 0) - TOP_H] as const
  const toDoc = (sx: number, sy: number) => [cam.x + sx / cam.zoom, cam.y + sy / cam.zoom] as const
  const screenRect = (r: Rect) => {
    const [x, y] = toScreen(r.x, r.y)
    return { x, y, w: r.w * cam.zoom, h: r.h * cam.zoom }
  }
  const view = (): Rect => ({ x: cam.x, y: cam.y, w: w / cam.zoom, h: h / cam.zoom })
  const refresh = () => {
    schedule()
    setTick((t) => t + 1)
  }
  const anchorsFor = (ids: string[]) => (tryCall<Box[]>('snap_targets', { ids, view: view() }) ?? []).map(painted).flatMap(anchorPoints)
  const handlesOn = tool === 'move' && !editing && !pen.current && !!frame && (frame.box.w * cam.zoom >= 10 || frame.box.h * cam.zoom >= 10)
  /** Box point (painted through `m`) → screen. */
  const screenPt = (x: number, y: number, m?: M | null): Pt => {
    const [a, b] = toScreen(...apply(m, x, y))
    return [a, b]
  }
  const frameScreen = (x: number, y: number) => screenPt(x, y, frame?.m)
  /** Screen point → selection-frame coordinates scaled to screen px, relative to the frame's top-left (`m` is rigid). */
  const inFramePx = (sx: number, sy: number): Pt => {
    const [fx, fy] = unapply(frame?.m, ...toDoc(sx, sy))
    return [(fx - frame!.box.x) * cam.zoom, (fy - frame!.box.y) * cam.zoom]
  }

  /** Corner-radius handles: rectangles/frames over 100 px on screen, inset along the diagonals by the radius. */
  function radiusHandles(): { i: number; x: number; y: number }[] {
    const t = selNode?.node?.type
    if (!handlesOn || !frame || selection.length !== 1 || selection[0].includes('/') || (t !== 'rectangle' && t !== 'frame')) return []
    const z = cam.zoom
    const [sw, sh] = [frame.box.w * z, frame.box.h * z]
    const half = Math.min(sw, sh) / 2
    if (sw <= 100 || sh <= 100 || half <= 12) return []
    const r = radiiOf(selNode.node)
    return corners(frame.box).map(([cx, cy], i) => {
      // 12 px in at radius 0, up to the middle at the largest radius that fits.
      const j = (12 + (Math.min(r[i] * z, half) / half) * (half - 12)) / z
      const [x, y] = frameScreen(cx + INWARD[i][0] * j, cy + INWARD[i][1] * j)
      return { i, x, y }
    })
  }

  /** Top-level frames with children are artboards: their empty body is canvas, not a target. */
  const isArtboard = (chain: string[], target: string) => {
    if (chain.length !== 1 || chain[0] !== target) return false
    const n = tryCall('node', { id: target })?.node
    return n?.type === 'frame' && (n.children?.length ?? 0) > 0 && n.layout === 'none'
  }

  function startEdit(id: string) {
    const info = tryCall('node', { id })
    if (!info?.node || info.node.type !== 'text' || !info.box) return
    setEditing({ id, box: info.box, value: String(info.node.content ?? ''), size: Number(info.node.fontSize ?? 14) })
  }

  function commitEdit(value: string) {
    const e = editLive.current
    if (!e) return
    editLive.current = null
    if (value !== '' && value !== undefined) tryCall('update', { ids: [e.id], props: { content: value } })
    setEditing(null)
  }

  /** Screen box of the in-place title field for a top-level frame. */
  function renameBox(id: string): Rect | null {
    const f = frames.find((x) => x.id === id)
    if (!f) return null
    const { x, y, w } = screenRect(painted(f))
    return { x: x - 4, y: y - 21, w: Math.max(140, Math.min(w, 240)), h: 20 }
  }

  /** Enter, blur or a press elsewhere commits once (one undo step); Escape clears `renameLive` first. */
  function commitRename(value?: string) {
    const r = renameLive.current
    if (!r) return
    renameLive.current = null
    const name = (value ?? r.draft).trim()
    const f = frames.find((x) => x.id === r.id)
    if (name && f && name !== f.name) tryCall('update', { ids: [r.id], props: { name } })
    setRenaming(null)
  }

  /** Resize handle (15 px squares/strips) or rotation zone (35 px around corners, outside) under a screen point. */
  function hitHandle(sx: number, sy: number): { handle: Handle; rotate: boolean } | null {
    if (!handlesOn || !frame) return null
    // Tested in the frame's own (rotated) axes.
    const [px, py] = inFramePx(sx, sy)
    const r = { x: 0, y: 0, w: frame.box.w * cam.zoom, h: frame.box.h * cam.zoom }
    const inside = px >= 0 && px <= r.w && py >= 0 && py <= r.h
    // Tiny selections: the body moves instead of resizing.
    if ((r.w < 25 || r.h < 25) && inside) return null
    const pts = handlePoints(r)
    for (const hnd of ['nw', 'ne', 'se', 'sw'] as const) {
      if (Math.abs(px - pts[hnd][0]) <= HIT && Math.abs(py - pts[hnd][1]) <= HIT) return { handle: hnd, rotate: false }
    }
    const inX = px > 0 && px < r.w
    const inY = py > 0 && py < r.h
    if (inX && Math.abs(py) <= HIT) return { handle: 'n', rotate: false }
    if (inX && Math.abs(py - r.h) <= HIT) return { handle: 's', rotate: false }
    if (inY && Math.abs(px) <= HIT) return { handle: 'w', rotate: false }
    if (inY && Math.abs(px - r.w) <= HIT) return { handle: 'e', rotate: false }
    if (selection.length === 1 && !inside && !selection[0].includes('/')) {
      for (const hnd of ['nw', 'ne', 'se', 'sw'] as const) {
        if (Math.abs(px - pts[hnd][0]) <= ROTATE_HIT && Math.abs(py - pts[hnd][1]) <= ROTATE_HIT) return { handle: hnd, rotate: true }
      }
    }
    return null
  }

  /** Corner-radius handle (11 px circle) under a screen point: its corner index. */
  const hitRadius = (sx: number, sy: number) => radiusHandles().find((p) => Math.abs(sx - p.x) <= 5.5 && Math.abs(sy - p.y) <= 5.5)?.i

  /** Top-level frame whose title label is under a screen point (labels select their frame). */
  function frameLabelAt(sx: number, sy: number): string | undefined {
    return visibleFrames.find((f) => {
      const { x, y, w } = screenRect(painted(f))
      return sx >= x && sx <= x + Math.min(Math.max(40, w), f.name.length * 7 + 8) && sy >= y - 18 && sy <= y - 2
    })?.id
  }

  function startResize(handle: Handle, dx: number, dy: number) {
    if (!frame) return
    const items: Item[] = []
    // Groups have no size of their own: their children are resized instead.
    const add = (id: string) => {
      const info = tryCall('node', { id })
      if (!info?.box) return
      if (info.node?.type === 'group') return (tryCall<Box[]>('children_boxes', { id }) ?? []).forEach((k) => add(k.id))
      items.push({ id, box: info.box, node: info, movable: !id.includes('/') && (isMovable(info) || info.parent === 'document') })
    }
    selection.forEach(add)
    if (!items.length) return
    call('drag_begin')
    // Layers placed by auto layout never snap; neither do rotated frames (snapping is axis-aligned).
    const anchors = items.some((it) => inFlow(it.node)) || turned(frame.m) ? [] : anchorsFor(selection)
    drag.current = { mode: 'resize', handle, f0: unapply(frame.m, dx, dy), box: frame.box, m: frame.m, items, anchors }
  }

  function startRotate(dx: number, dy: number) {
    const id = selection[0]
    const info = tryCall('node', { id })
    if (!info?.box || !info.node || info.node.type === 'group') return
    const b: Box = info.box
    const [cx, cy] = apply(b.m, b.x + b.w / 2, b.y + b.h / 2)
    call('drag_begin')
    drag.current = {
      mode: 'rotate', cx, cy, a0: (Math.atan2(dy - cy, dx - cx) * 180) / Math.PI, rot0: Number(info.node.rotation ?? 0),
      flipX: !!info.node.flipX, flipY: !!info.node.flipY, id, box: b,
      x0: Number(info.node.x ?? 0), y0: Number(info.node.y ?? 0), movable: isMovable(info) || info.parent === 'document',
    }
  }

  function startRadius(corner: number, dx: number, dy: number) {
    if (!frame) return
    call('drag_begin')
    drag.current = { mode: 'radius', id: selection[0], corner, f0: unapply(frame.m, dx, dy), m: frame.m, r0: radiiOf(selNode?.node) }
  }

  /** A parent's painted transform: its children's x / y (and so `move_by`) live in its space. */
  const parentM = (id: string): M | null => (id === 'document' ? null : tryCall('node', { id })?.box?.m ?? null)

  /** `move_by` for a canvas-space delta, turned into the parent's space (rotated or flipped frames). */
  function moveBy(ids: string[], d: Extract<Drag, { mode: 'move' }>, dx: number, dy: number) {
    const [px, py] = unapply(d.pm, dx, dy, true)
    tryCall('move_by', { ids, dx: px, dy: py })
  }

  /** Is `id` a frame or layer somewhere below `ancestor`? */
  function below(id: string, ancestor: string) {
    for (let p = tryCall('node', { id })?.parent; p && p !== 'document'; p = tryCall('node', { id: p })?.parent) if (p === ancestor) return true
    return false
  }

  /** Move the dragged nodes under `parent` (keeping their canvas position) and re-sync the drag offsets. `from` is where to put them back when the pointer leaves a frame nested in the one the drag started in. */
  function toParent(d: Extract<Drag, { mode: 'move' }>, parent: string, from?: string) {
    tryCall('reparent', { ids: d.ids, parent })
    d.parent = parent
    d.from = from
    d.pm = parentM(parent)
    d.flow = false
    d.groupOf = undefined
    const b = unionRect((tryCall<Box[]>('boxes', { ids: d.ids }) ?? []).map(painted))
    if (b) [d.sentX, d.sentY] = [b.x - d.start.x, b.y - d.start.y]
    d.anchors = anchorsFor(d.ids)
  }

  /** Alt while dragging: drag copies and put the originals back; releasing Alt (700 ms grace) undoes it. */
  function setDup(d: Extract<Drag, { mode: 'move' }>, alt: boolean) {
    if (alt && d.dup) d.dup.offAt = undefined
    else if (alt) {
      const r = tryCall<{ ids: string[] }>('duplicate', { ids: d.ids, inPlace: true })
      if (!r?.ids?.length) return
      if (d.sentX || d.sentY) moveBy(d.ids, d, -d.sentX, -d.sentY)
      d.dup = { originals: d.ids }
      d.ids = r.ids
      d.anchors = anchorsFor(d.ids)
      select(r.ids)
    } else if (d.dup) {
      const now = performance.now()
      d.dup.offAt ??= now
      if (now - d.dup.offAt < 700) return
      tryCall('delete', { ids: d.ids })
      if (d.sentX || d.sentY) moveBy(d.dup.originals, d, d.sentX, d.sentY)
      d.ids = d.dup.originals
      d.dup = undefined
      d.anchors = anchorsFor(d.ids)
      select(d.ids)
    }
  }

  function beginMove(d: Extract<Drag, { mode: 'move' }>, alt: boolean) {
    d.moved = true
    d.start = unionRect((tryCall<Box[]>('boxes', { ids: d.ids }) ?? []).map(painted)) ?? { x: d.dx0, y: d.dy0, w: 0, h: 0 }
    const infos = d.ids.map((id) => tryCall('node', { id }))
    d.parent = infos[0]?.parent ?? 'document'
    // Layers from several parents cannot all go back to one, so they never enter a nested frame.
    d.mixed = infos.some((i) => i.parent !== d.parent)
    d.pm = parentM(d.parent)
    d.flow = infos.every((i) => inFlow(i) && i.parent === d.parent)
    const p = d.parent !== 'document' ? tryCall('node', { id: d.parent }) : null
    d.groupOf = p?.node?.type === 'group' ? p.parent : undefined
    // The frame the drag starts in: leaving it reparents, staying in an auto layout reorders.
    d.container = tryCall<Drop>('drop_target', { x: d.dx0, y: d.dy0, ignore: d.ids, any: true })
    d.anchors = anchorsFor(d.ids)
    call('drag_begin')
    if (alt) setDup(d, true)
  }

  function moveTo(d: Extract<Drag, { mode: 'move' }>, dx: number, dy: number, m?: EventModifiers) {
    const ctrl = !!m?.ctrl
    // Shift keeps the move on one axis (the dominant one at its first motion; releasing Shift frees it).
    if (!m?.shift) d.lock = undefined
    else {
      d.lock ??= Math.abs(dx - d.dx0) >= Math.abs(dy - d.dy0) ? 1 : 0
      if (d.lock === 1) dy = d.dy0
      else dx = d.dx0
    }
    setDup(d, !!m?.alt)
    const tx = Math.round(dx - d.dx0)
    const ty = Math.round(dy - d.dy0)
    const c = d.container
    const [lx, ly] = unapply(c?.m, dx, dy)
    const inC = !!c && c.id !== 'document' && lx >= c.x! && ly >= c.y! && lx <= c.x! + c.w! && ly <= c.y! + c.h!
    d.deferred = undefined
    if (c && inC && c.layout !== 'none' && d.flow) {
      // Live reorder by child midpoints; the node itself sits in its slot, a ghost follows the pointer.
      const t = tryCall<Drop>('drop_target', { x: dx, y: dy, ignore: d.ids, parent: c.id })
      if (t?.index !== undefined && t.index !== d.index) {
        d.index = t.index
        tryCall('reparent', { ids: d.ids, parent: c.id, index: t.index })
      }
      setFloat({ ...d.start, x: d.start.x + tx, y: d.start.y + ty })
      setGuides([])
      setDrop(null)
      return refresh()
    }
    let snapOn = !ctrl
    const t = tryCall<Drop>('drop_target', { x: dx, y: dy, w: d.start.w, h: d.start.h, ignore: d.ids, any: ctrl })
    if (t && t.layout !== 'none' && (!inC || t.id !== c!.id)) {
      // Over an auto layout: float (layout children leave their slot) and drop at the indicator on release.
      d.deferred = t
      if (d.flow || (!inC && d.parent !== 'document')) toParent(d, 'document')
      snapOn = false
    } else if (t && t.id !== d.parent && t.id !== d.groupOf && (!inC || (!d.mixed && below(t.id, c!.id)))) toParent(d, t.id, inC ? d.from ?? d.parent : undefined)
    else if (inC && d.from && !(t && below(t.id, c!.id))) toParent(d, d.from)
    setDrop(d.deferred ?? null)
    setFloat(null)
    let [ox, oy] = [0, 0]
    let found: Guide[] = []
    if (snapOn && !d.flow) {
      const r = snap(dragPoints({ ...d.start, x: d.start.x + tx, y: d.start.y + ty }), d.anchors, SNAP_PX / cam.zoom, [d.lock !== 0, d.lock !== 1])
      ;[ox, oy] = r.offset
      found = r.guides
    }
    setGuides(found)
    const [fx, fy] = [tx + ox, ty + oy]
    if (fx !== d.sentX || fy !== d.sentY) {
      moveBy(d.ids, d, fx - d.sentX, fy - d.sentY)
      ;[d.sentX, d.sentY] = [fx, fy]
    }
    refresh()
  }

  function resizeTo(d: Extract<Drag, { mode: 'resize' }>, dx: number, dy: number, mods?: EventModifiers) {
    const { box, handle } = d
    // The pointer's motion in the frame's own axes.
    const [fx, fy] = unapply(d.m, dx, dy)
    let ddx = Math.round(fx - d.f0[0])
    let ddy = Math.round(fy - d.f0[1])
    let found: Guide[] = []
    if (!mods?.ctrl && !mods?.alt && d.anchors.length) {
      // The dragged handle snaps; edge handles only along their own axis.
      const hp = handlePoints(box)[handle]
      const r = snap([apply(d.m, hp[0] + ddx, hp[1] + ddy)], d.anchors, SNAP_PX / cam.zoom, [/[ew]/.test(handle), /[ns]/.test(handle)])
      const [ox, oy] = unapply(d.m, r.offset[0], r.offset[1], true)
      ddx += ox
      ddy += oy
      found = r.guides
    }
    setGuides(found)
    // A negative size (dragged past the opposite edge) mirrors the items.
    const nb = resizeBox(box, handle, ddx, ddy, !!mods?.shift, !!mods?.alt)
    const [kx, ky] = [nb.w / Math.max(box.w, 1e-9), nb.h / Math.max(box.h, 1e-9)]
    for (const it of d.items) {
      const b = it.box
      const n = it.node?.node
      const props: Record<string, unknown> = {}
      // Only the axes that change are written: Hug/Fill turn Fixed on that axis alone.
      if (/[ew]/.test(handle) || nb.w !== box.w) {
        props.width = Math.max(1, Math.round(Math.abs(b.w * kx)))
        props.flipX = kx < 0 !== !!n?.flipX || null
      }
      if (/[ns]/.test(handle) || nb.h !== box.h) {
        props.height = Math.max(1, Math.round(Math.abs(b.h * ky)))
        props.flipY = ky < 0 !== !!n?.flipY || null
      }
      if (n?.type === 'text') {
        const growth = n.textGrowth ?? 'auto'
        if ('height' in props) props.textGrowth = 'fixed-width-height'
        else if (growth === 'auto') props.textGrowth = 'fixed-width'
        if (props.textGrowth === 'fixed-width-height' && !('width' in props)) props.width = Math.round(b.w)
      }
      if (it.movable) {
        // The item's origin follows the frame's scale (a flip keeps it on the same side of its box);
        // the canvas shift is turned into the item's parent space.
        const [ox, oy] = unapply(d.m, ...apply(b.m, b.x, b.y))
        const shift = lin(d.m, nb.x + (ox - box.x) * kx - ox, nb.y + (oy - box.y) * ky - oy)
        const [px, py] = lin(own(Number(n?.rotation ?? 0), !!n?.flipX, !!n?.flipY), ...unapply(b.m, shift[0], shift[1], true))
        const round = (v: number) => (turned(b.m) ? Math.round(v * 100) / 100 : Math.round(v))
        props.x = round((n?.x ?? 0) + px)
        props.y = round((n?.y ?? 0) + py)
      }
      tryCall('update', { ids: [it.id], props })
    }
    refresh()
  }

  function rotateTo(d: Extract<Drag, { mode: 'rotate' }>, dx: number, dy: number, mods?: EventModifiers) {
    const a = (Math.atan2(dy - d.cy, dx - d.cx) * 180) / Math.PI
    let r = d.rot0 - (a - d.a0)
    if (mods?.shift) r = Math.round(r / 15) * 15
    r = Math.round(((((r + 180) % 360) + 360) % 360 - 180) * 100) / 100
    const props: Record<string, unknown> = { rotation: r }
    if (d.movable) {
      // Turn around the center: move the origin so the center stays put.
      ;[props.x, props.y] = turnAroundCenter(d.x0, d.y0, d.box.w, d.box.h, d.rot0, r, d.flipX, d.flipY)
    }
    tryCall('update', { ids: [d.id], props })
    refresh()
  }

  function radiusTo(d: Extract<Drag, { mode: 'radius' }>, dx: number, dy: number, mods?: EventModifiers) {
    const [fx, fy] = unapply(d.m, dx, dy)
    const [ix, iy] = INWARD[d.corner]
    // Toward the center grows the radius by the mean of both axes' motion.
    const v = Math.round(Math.max(0, d.r0[d.corner] + ((fx - d.f0[0]) * ix + (fy - d.f0[1]) * iy) / 2))
    // Equal corners move together, others one at a time; Alt swaps the two.
    const all = d.r0.every((r) => r === d.r0[0]) !== !!mods?.alt
    tryCall('update', { ids: [d.id], props: { cornerRadius: all ? v || null : d.r0.map((r, i) => (i === d.corner ? v : r)) } })
    refresh()
  }

  // ---- Vector editing (path-editing state with its pen and point tools) ----

  /** Two presses on the same thing: the click count, or within 450 ms (synthetic clicks carry no count). */
  function doublePress(e: EventPayload, key: unknown) {
    const now = performance.now()
    const dbl = (e.clickCount ?? 1) >= 2 || (key !== undefined && lastPress.current.key === key && now - lastPress.current.at < 450)
    lastPress.current = { key, at: dbl ? 0 : now }
    return dbl
  }

  function penEnter(id: string, sub: 'pen' | 'move') {
    const cs = tryCall<Contour[]>('path_edit', { id })
    if (!cs) return
    pen.current = { id, parent: 'document', cs, sel: new Set(), sub, version: tryCall<{ version: number }>('state')?.version }
    select([id])
    setTool(sub)
    setHover(null)
    penRedraw()
  }

  /** The pen's open contour ends; contours under two points go. */
  function penStop(P: PenEdit) {
    P.appending = undefined
    P.cs = P.cs.filter(drawable)
  }

  function penExit(keepTool = false) {
    const P = pen.current
    if (!P) return
    penStop(P)
    pen.current = null
    setGuides([])
    setMarquee(null)
    if (!keepTool) setTool('move')
    if (!P.id) select([])
    penRedraw()
  }

  /** Writes the contours into the path, creating it at the second point (each gesture is one undo step). */
  function penWrite() {
    const P = pen.current!
    if (!P.cs.some(drawable)) return
    const r = tryCall<{ id: string; state: { version: number } }>('path_write', { id: P.id, parent: P.parent, contours: P.cs })
    if (!r) return
    P.version = r.state.version
    if (!P.id) {
      P.id = r.id
      select([r.id])
    }
    refresh()
  }

  /** Where the pen puts a point: Shift = 45° steps from `from`, snapped along that ray. */
  function penPlace(p: Pt, on: boolean, from?: Pt): Pt {
    const cs = pen.current?.cs ?? []
    if (from) {
      const c = constrain(from, p)
      const anchors = cs.flatMap((k) => k.anchors.map(ptOf))
      const r = on ? snapRay(from, c, anchors, POINT_SNAP / cam.zoom) : undefined
      setGuides(r ? snap([r], anchors, 1e-6).guides : [])
      return r ?? roundRay(from, c)
    }
    if (!on) {
      setGuides([])
      return [round(p[0]), round(p[1])]
    }
    const r = snapPoints([p], cs, new Set(), cam.zoom, true)
    setGuides(r.guides)
    return [p[0] + r.offset[0], p[1] + r.offset[1]]
  }

  /** What a press at `p` does with the pen or point tool. */
  function penResolve(p: Pt, m?: EventModifiers): PenAction {
    const P = pen.current!
    const z = cam.zoom
    const shown = shownHandles(P.cs, P.sel)
    if (P.sub === 'move') {
      setGuides([])
      const h = hitPenHandle(shown, p, z)
      if (h) return { kind: 'handle', ...h }
      const a = hitAnchor(P.cs, p, z)
      if (a) return { kind: 'points', anchor: a }
      const s = hitSegment(P.cs, [p], z)
      if (s && !s.from.out && !s.to.in) {
        // A straight segment's midpoint dot inserts a point.
        const mid = pointAt(s.from, s.to, 0.5)
        if (Math.hypot(mid[0] - p[0], mid[1] - p[1]) <= POINT_SNAP / z) return { kind: 'insert', seg: s, t: 0.5, at: mid }
      }
      return s ? { kind: 'points', seg: s } : { kind: 'box' }
    }
    const ap = P.appending
    if (ap) {
      const h = hitPenHandle(shown, p, z)
      if (h) return { kind: 'handle', ...h }
      const ext = endOf(ap.c, ap.end)
      const at = penPlace(p, !m?.ctrl, m?.shift ? ptOf(ext) : undefined)
      const a = hitAnchor(P.cs, at, z)
      if (a === ext) return { kind: 'blocked' }
      // ponytail: only closes the own contour; joining onto another open contour's end is missing, add if needed.
      if (a && drawable(ap.c) && a === endOf(ap.c, other(ap.end))) return { kind: 'close', anchor: a, at }
      return { kind: 'place', at, target: a }
    }
    const a = hitAnchor(P.cs, p, z)
    if (a) {
      if (m?.alt) return { kind: 'points', anchor: a }
      const ep = endpoint(P.cs, a)
      return ep ? { kind: 'resume', anchor: a, c: ep.c, end: ep.end } : { kind: 'start', at: ptOf(a), anchor: a }
    }
    const h = hitPenHandle(shown, p, z)
    if (h) return { kind: 'handle', ...h }
    const s = hitSegment(P.cs, [p], z)
    if (s) return m?.alt ? { kind: 'insert', seg: s, t: s.t, at: s.at } : { kind: 'start', at: s.at }
    return { kind: 'start', at: penPlace(p, !m?.ctrl) }
  }

  function penDown(sx: number, sy: number, p: Pt, e: EventPayload) {
    const m = e.modifiers
    if (!pen.current) {
      // Start a new path in the frame under the press, from a point on the 0.5 px grid.
      const chain = (tryCall<string[]>('hit', { x: p[0], y: p[1] }) ?? []).filter((id) => !id.includes('/'))
      const parent = [...(tryCall<Box[]>('boxes', { ids: chain }) ?? [])].reverse().find((b) => b.type === 'frame')?.id ?? 'document'
      pen.current = { id: null, parent, cs: [], sel: new Set(), sub: 'pen' }
      if (selection.length) select([])
    }
    const P = pen.current
    const act: PenAction = P.cs.length ? penResolve(p, m) : { kind: 'start', at: [round(p[0]), round(p[1])] }
    const dbl = doublePress(e, act.kind === 'points' ? act.anchor ?? act.seg?.from : act.kind === 'box' ? 'box' : undefined)
    const grab = (anchor: Anchor, side: Side, mirror: boolean): PenDrag => ({ kind: 'handle', anchor, side, mirror })
    const moving = (start: Pt): PenDrag => ({ kind: 'points', start, from: new Map([...P.sel].map((a) => [a, clone(a)])) })
    let target: PenDrag = { kind: 'none' }
    let click: (() => void) | undefined
    if (act.kind === 'box' && dbl) return penExit()
    call('drag_begin')
    switch (act.kind) {
      case 'start': {
        const a: Anchor = { x: act.at[0], y: act.at[1] }
        const c: Contour = { anchors: [a], closed: false }
        P.cs.push(c)
        P.appending = { c, end: 'out' }
        P.sel = new Set([a])
        target = grab(a, 'out', true)
        break
      }
      case 'place': {
        const ap = P.appending!
        const a: Anchor = { x: act.at[0], y: act.at[1] }
        if (ap.end === 'out') ap.c.anchors.push(a)
        else ap.c.anchors.unshift(a)
        P.sel = new Set([a])
        penWrite()
        target = grab(a, ap.end, true)
        break
      }
      case 'close': {
        const ap = P.appending!
        const side = other(ap.end)
        ap.c.closed = true
        act.anchor[side] = undefined
        P.appending = undefined
        P.sel = new Set([act.anchor])
        penWrite()
        target = { kind: 'handle', anchor: act.anchor, side, mirror: false, connect: true }
        break
      }
      case 'resume':
        act.anchor[act.end] = undefined
        P.sel = new Set([act.anchor])
        P.appending = { c: act.c, end: act.end }
        penWrite()
        target = grab(act.anchor, act.end, false)
        break
      case 'handle':
        target = grab(act.anchor, act.side, mirrored(act.anchor))
        break
      case 'points': {
        // A press selects (Shift adds); inside the selection the change waits for the click.
        const hit = act.anchor ? [act.anchor] : [act.seg!.from, act.seg!.to]
        const only = () => { P.sel = new Set(hit) }
        if (!hit.every((a) => P.sel.has(a))) {
          if (m?.shift) hit.forEach((a) => P.sel.add(a))
          else only()
        } else click = m?.shift ? () => hit.forEach((a) => P.sel.delete(a)) : only
        if (dbl && act.anchor) {
          // Double-click: smooth ↔ corner.
          const a = act.anchor
          only()
          click = () => {
            const c = P.cs.find((k) => k.anchors.includes(a))
            if (c) toggleSmooth(c, a)
            penWrite()
          }
        }
        target = moving(act.anchor ? ptOf(act.anchor) : p)
        break
      }
      case 'insert': {
        const a = split(act.seg, act.t)
        P.sel = new Set([a])
        penWrite()
        target = moving(ptOf(a))
        break
      }
      case 'box': {
        const base = m?.shift ? new Set(P.sel) : new Set<Anchor>()
        if (!m?.shift) P.sel = new Set()
        target = { kind: 'box', from: p, base }
        break
      }
      case 'blocked':
        break
    }
    drag.current = { mode: 'pen', sx, sy, moved: false, target, click }
    P.action = undefined
    penRedraw()
  }

  function penDragTo(d: Extract<Drag, { mode: 'pen' }>, sx: number, sy: number, p: Pt, m?: EventModifiers) {
    const P = pen.current
    if (!P) return
    if (!d.moved) {
      if (Math.hypot(sx - d.sx, sy - d.sy) < PEN_DRAG) return
      d.moved = true
    }
    const on = !m?.ctrl
    const t = d.target
    if (t.kind === 'handle') {
      // Handle drag: Shift = 45° steps; else snapped like a point (onto the own anchor too).
      const a = t.anchor
      let q: Pt
      if (m?.shift) {
        q = roundRay(ptOf(a), constrain(ptOf(a), p))
        setGuides([])
      } else {
        const r = snapPoints([p], P.cs, new Set(), cam.zoom, on, new Set([a]))
        q = [p[0] + r.offset[0], p[1] + r.offset[1]]
        setGuides(r.guides)
      }
      if (t.connect) a[t.side] = mirror(a, q)
      else {
        // Ctrl/Cmd pulls both handles, Alt breaks them apart.
        if (m?.ctrl || m?.cmd) t.mirror = true
        else if (m?.alt) t.mirror = false
        a[t.side] = q
        if (t.mirror) a[other(t.side)] = mirror(a, q)
      }
    } else if (t.kind === 'points') {
      // Shift locks to the dominant axis (no snapping on the locked one).
      const [rx, ry] = [p[0] - t.start[0], p[1] - t.start[1]]
      const lock = m?.shift ? (Math.abs(rx) >= Math.abs(ry) ? 1 : 0) : -1
      let [dx, dy] = [lock === 0 ? 0 : rx, lock === 1 ? 0 : ry]
      const r = snapPoints([...t.from.values()].map((o) => [o.x + dx, o.y + dy] as Pt), P.cs, new Set(t.from.keys()), cam.zoom, on)
      dx += lock === 0 ? 0 : r.offset[0]
      dy += lock === 1 ? 0 : r.offset[1]
      setGuides(r.guides)
      for (const [a, o] of t.from) {
        Object.assign(a, { x: o.x + dx, y: o.y + dy, in: o.in && [o.in[0] + dx, o.in[1] + dy], out: o.out && [o.out[0] + dx, o.out[1] + dy] })
      }
    } else if (t.kind === 'box') {
      const r = { x: Math.min(t.from[0], p[0]), y: Math.min(t.from[1], p[1]), w: Math.abs(p[0] - t.from[0]), h: Math.abs(p[1] - t.from[1]) }
      P.sel = new Set(t.base)
      for (const c of P.cs) for (const a of c.anchors) {
        if (a.x < r.x || a.y < r.y || a.x > r.x + r.w || a.y > r.y + r.h) continue
        if (t.base.has(a)) P.sel.delete(a)
        else P.sel.add(a)
      }
      setMarquee(r)
      return penRedraw()
    } else return
    penWrite()
    penRedraw()
  }

  function penUp(d: Extract<Drag, { mode: 'pen' }>) {
    const P = pen.current
    if (P && !d.moved) d.click?.()
    else if (P && d.target.kind === 'handle') {
      // A handle dropped on its anchor goes; a closing handle only on its own side.
      const { anchor: a, side, connect } = d.target
      let changed = false
      for (const s of ['in', 'out'] as const) {
        const h = a[s]
        if (h && (!connect || s === side) && Math.hypot(h[0] - a.x, h[1] - a.y) <= ANCHOR / 2 / cam.zoom) {
          a[s] = undefined
          changed = true
        }
      }
      if (changed) penWrite()
    } else if (P && d.target.kind === 'points' && d.target.from.size === 1) {
      // An end point dropped on the other end of its open contour closes it.
      const a = [...d.target.from.keys()][0]
      const ep = endpoint(P.cs, a)
      const f = ep && endOf(ep.c, other(ep.end))
      if (ep && f && ep.c.anchors.length >= 3 && Math.hypot(f.x - a.x, f.y - a.y) <= PEN_HIT / cam.zoom) {
        f[other(ep.end)] = a[other(ep.end)]
        if (ep.end === 'in') ep.c.anchors.shift()
        else ep.c.anchors.pop()
        ep.c.closed = true
        P.sel = new Set([f])
        penWrite()
      }
    }
    setMarquee(null)
    setGuides([])
    const st = tryCall<{ version: number }>('drag_end')
    if (P && st) P.version = st.version
    refresh()
  }

  /** Delete removes the selected points; a path left with nothing to draw goes. */
  function penDelete() {
    const P = pen.current!
    const r = deletePoints(P.cs, P.sel)
    P.cs = r.contours
    P.sel = new Set(r.next)
    P.appending = undefined
    if (P.cs.some(drawable)) penWrite()
    else {
      if (P.id) tryCall('delete', { ids: [P.id] })
      pen.current = null
      select([])
      setTool('move')
    }
    penRedraw()
  }

  /** Keys while editing points; false = not handled here. */
  function penKey(key: string, m: Mods): boolean {
    const P = pen.current!
    if (drag.current) return !(m.mod && (key === 'z' || key === 'y'))
    if (key === 'escape') {
      // Ends the open contour, then clears the point selection, then the pen becomes the point tool, then editing ends.
      if (P.appending) {
        penStop(P)
        if (!P.cs.length) return penExit(), true
      } else if (P.sel.size) P.sel = new Set()
      else if (P.sub === 'pen') setTool('move')
      else return penExit(), true
      return penRedraw(), true
    }
    if (key === 'enter') return penExit(), true
    if ((key === 'delete' || key === 'backspace') && !m.mod) return P.sel.size ? penDelete() : undefined, true
    if (!m.mod && !m.alt && (key === 'v' || key === 'p')) return setTool(key === 'p' ? 'pen' : 'move'), true
    if (m.mod && key === 'a') {
      P.sel = new Set(P.cs.flatMap((c) => c.anchors))
      return penRedraw(), true
    }
    const nudge: Record<string, Pt> = { left: [-1, 0], right: [1, 0], up: [0, -1], down: [0, 1] }
    if (nudge[key]) {
      const k = m.shift ? 10 : 1
      for (const a of P.sel) {
        const [dx, dy] = [nudge[key][0] * k, nudge[key][1] * k]
        Object.assign(a, { x: a.x + dx, y: a.y + dy, in: a.in && [a.in[0] + dx, a.in[1] + dy], out: a.out && [a.out[0] + dx, a.out[1] + dy] })
      }
      if (P.sel.size) penWrite()
      return penRedraw(), true
    }
    return false
  }

  // The pen and move tools are the editor's sub-tools; other tools end editing.
  // Choosing the pen with one path selected edits that path.
  useEffect(() => {
    const P = pen.current
    if (P && (tool === 'pen' || tool === 'move')) {
      P.sub = tool
      if (tool === 'move') {
        penStop(P)
        if (!P.cs.length) return penExit(true)
      }
      penRedraw()
    } else if (P) penExit(true)
    else if (tool === 'pen' && selection.length === 1 && tryCall('node', { id: selection[0] })?.node?.type === 'path') penEnter(selection[0], 'pen')
    if (tool !== 'pen') setPenDot(null)
  }, [tool])
  // Selecting something else ends editing; undo (or any other change) reloads the path's points.
  useEffect(() => {
    const P = pen.current
    if (P?.id && (selection.length !== 1 || selection[0] !== P.id)) penExit()
  }, [selection])
  useEffect(() => {
    const P = pen.current
    if (!P?.id || drag.current || version <= (P.version ?? 0)) return
    const cs = tryCall<Contour[]>('path_edit', { id: P.id })
    if (!cs) return penExit()
    const i = P.appending ? P.cs.indexOf(P.appending.c) : -1
    const end = P.appending?.end ?? 'out'
    P.cs = cs
    P.sel = new Set()
    P.version = version
    P.appending = i >= 0 && cs[i] && !cs[i].closed ? { c: cs[i], end } : undefined
    penRedraw()
  }, [version])

  function onMouseDown(e: EventPayload) {
    setKeyboardFocus(null)
    const [sx, sy] = local(e)
    const [dx, dy] = toDoc(sx, sy)
    lastPt.current = [sx, sy]
    stray.current = 0
    if (editing) commitEdit(editing.value)
    if (renaming) {
      // Presses inside the title field stay there (caret placement); elsewhere they finish the rename.
      const r = renameBox(renaming.id)
      if (r && sx >= r.x && sx <= r.x + r.w && sy >= r.y && sy <= r.y + r.h) return
      commitRename()
    }
    if (e.button === 1 || tool === 'hand' || space.current) {
      drag.current = { mode: 'pan', sx, sy, cam }
      setPanning(true)
      return
    }
    if (e.button !== 0) return
    if (pen.current || tool === 'pen') return penDown(sx, sy, [dx, dy], e)
    if (tool !== 'move') {
      drag.current = { mode: 'create', tool, dx0: dx, dy0: dy, dx1: dx, dy1: dy }
      return
    }
    const mods = e.modifiers
    const shift = !!mods?.shift
    const hnd = hitHandle(sx, sy)
    if (hnd) return hnd.rotate ? startRotate(dx, dy) : startResize(hnd.handle, dx, dy)
    const corner = hitRadius(sx, sy)
    if (corner !== undefined) return startRadius(corner, dx, dy)
    const marqueeFrom = (deep: boolean) => {
      if (!shift) select([])
      drag.current = { mode: 'marquee', sx, sy, dx0: dx, dy0: dy, base: shift ? selection : [], deep, shift, moved: false }
    }
    // Ctrl/Cmd always starts a deep marquee; a plain Ctrl+click selects the deepest layer on release.
    if (mods?.ctrl || mods?.cmd) return marqueeFrom(true)
    const label = frameLabelAt(sx, sy)
    // Double-clicking a frame's title renames it in place (timed too: synthetic clicks carry no click count).
    const now = performance.now()
    const double = !!label && ((e.clickCount ?? 1) >= 2 || (lastLabel.current.id === label && now - lastLabel.current.at < 450))
    lastLabel.current = { id: label, at: double ? 0 : now }
    if (double) return setRenaming({ id: label!, draft: frames.find((f) => f.id === label)?.name ?? '' })
    const chain = label ? [label] : tryCall<string[]>('hit', { x: dx, y: dy }) ?? []
    const target = label ?? pickTarget(chain, selection, boxMap, false)
    // Inside the selection's frame the selection drags as is; a click without drag narrows to the target.
    const [fx, fy] = frame ? unapply(frame.m, dx, dy) : [0, 0]
    const inside = !shift && !label && !!frame && fx >= frame.box.x && fy >= frame.box.y && fx <= frame.box.x + frame.box.w && fy <= frame.box.y + frame.box.h
    if ((!target || (!label && isArtboard(chain, target))) && !inside) return marqueeFrom(false)
    // Double-clicking a path edits its points.
    if (target && tryCall('node', { id: target })?.node?.type === 'path' && doublePress(e, target)) return penEnter(target, 'move')
    if (target && (e.clickCount ?? 1) >= 2) {
      const i = chain.indexOf(target)
      const deeper = chain[i + 1]
      const info = tryCall('node', { id: target })
      if (info?.node?.type === 'text') return startEdit(target)
      if (deeper) select([deeper])
      return
    }
    let ids = selection
    // Shift adds an unselected layer on press; a selected one stays so it can drag, and leaves on a release without a drag.
    const toggle = shift && target && selection.includes(target) ? target : undefined
    if (!inside && target && !toggle) {
      ids = shift ? [...selection, target] : [target]
      select(ids)
    }
    const narrow = !target || isArtboard(chain, target) ? '' : target
    drag.current = {
      mode: 'move', sx, sy, dx0: dx, dy0: dy, ids: ids.filter((id) => !id.includes('/')), narrow: inside ? narrow : undefined, toggle, moved: false,
      start: { x: dx, y: dy, w: 0, h: 0 }, sentX: 0, sentY: 0, anchors: [], parent: 'document', pm: null, flow: false, container: null,
    }
  }

  function onMouseMove(e: EventPayload) {
    const [sx, sy] = local(e)
    const [dx, dy] = toDoc(sx, sy)
    const d = drag.current
    // Button-less moves at new spots mean the release happened outside the canvas: finish the
    // gesture so its undo step is not left open. Same-spot ones are GPUI's hover refreshes;
    // one stray event is tolerated.
    if (d && e.pressedButton === undefined) {
      if (Math.abs(sx - lastPt.current[0]) > 0.5 || Math.abs(sy - lastPt.current[1]) > 0.5) {
        lastPt.current = [sx, sy]
        if (++stray.current >= 2) onMouseUp()
      }
      return
    }
    stray.current = 0
    lastPt.current = [sx, sy]
    if (!d) {
      if (pen.current) {
        pen.current.action = penResolve([dx, dy], e.modifiers)
        return penRedraw()
      }
      if (tool === 'pen') return setPenDot([round(dx), round(dy)])
      if (tool === 'move' && !editing) {
        const chain = tryCall<string[]>('hit', { x: dx, y: dy }) ?? []
        const target = pickTarget(chain, selection, boxMap, !!(e.modifiers?.ctrl || e.modifiers?.cmd))
        const b = target && !selection.includes(target) && !isArtboard(chain, target) ? (tryCall<Box[]>('boxes', { ids: [target] }) ?? [])[0] : null
        setHover(b ?? null)
        // Alt: distances from the selection to the hovered layer (or the selection's parent).
        const t = e.modifiers?.alt && selUnion ? b ?? (selBoxes[0]?.parent ? (tryCall<Box[]>('boxes', { ids: [selBoxes[0].parent] }) ?? [])[0] : null) : null
        setMeasured(t && selUnion ? { s: selUnion, t, lines: measure(selUnion, painted(t)) } : null)
        const hh = hitHandle(sx, sy)
        const next: CursorValue = hh ? (hh.rotate ? 'grab' : resizeCursor(hh.handle, frame?.m)) : 'default'
        if (next !== cursor) setCursor(next)
      }
      return
    }
    if (d.mode === 'pan') {
      moveCam({ ...d.cam, x: d.cam.x - (sx - d.sx) / d.cam.zoom, y: d.cam.y - (sy - d.sy) / d.cam.zoom })
    } else if (d.mode === 'move') {
      if (!d.moved) {
        if (Math.hypot(sx - d.sx, sy - d.sy) < DRAG_PX || !d.ids.length) return
        setHover(null)
        setMeasured(null)
        beginMove(d, !!e.modifiers?.alt)
      }
      moveTo(d, dx, dy, e.modifiers)
    } else if (d.mode === 'resize') {
      resizeTo(d, dx, dy, e.modifiers)
    } else if (d.mode === 'rotate') {
      rotateTo(d, dx, dy, e.modifiers)
    } else if (d.mode === 'radius') {
      radiusTo(d, dx, dy, e.modifiers)
    } else if (d.mode === 'pen') {
      penDragTo(d, sx, sy, [dx, dy], e.modifiers)
    } else if (d.mode === 'marquee') {
      if (!d.moved && Math.hypot(sx - d.sx, sy - d.sy) < DRAG_PX) return
      d.moved = true
      const r = { x: Math.min(d.dx0, dx), y: Math.min(d.dy0, dy), w: Math.abs(dx - d.dx0), h: Math.abs(dy - d.dy0) }
      const hits = tryCall<string[]>('nodes_in_rect', { ...r, deep: d.deep }) ?? []
      const next = d.shift ? [...d.base.filter((id) => !hits.includes(id)), ...hits.filter((id) => !d.base.includes(id))] : hits
      if (next.join('\n') !== liveSel.current.join('\n')) select(next)
      setMarquee(r)
    } else if (d.mode === 'create') {
      d.dx1 = dx
      d.dy1 = dy
      const [ax, ay] = toScreen(Math.min(d.dx0, dx), Math.min(d.dy0, dy))
      setGhost({ x: ax, y: ay, w: Math.abs(dx - d.dx0) * cam.zoom, h: Math.abs(dy - d.dy0) * cam.zoom })
    }
  }

  function onMouseUp() {
    const d = drag.current
    drag.current = null
    setPanning(false)
    if (!d) return
    if (d.mode === 'pen') return penUp(d)
    if (d.mode === 'marquee') {
      setMarquee(null)
      if (!d.moved && d.deep) {
        const chain = tryCall<string[]>('hit', { x: d.dx0, y: d.dy0 }) ?? []
        const t = chain[chain.length - 1]
        if (t) select(d.shift ? (d.base.includes(t) ? d.base.filter((s) => s !== t) : [...d.base, t]) : [t])
      }
      return
    }
    if (d.mode === 'move') {
      if (!d.moved) {
        if (d.toggle) return select(liveSel.current.filter((s) => s !== d.toggle))
        const next = d.narrow ? [d.narrow] : []
        if (d.narrow !== undefined && liveSel.current.join() !== next.join()) select(next)
        return
      }
      if (d.deferred?.index !== undefined) tryCall('reparent', { ids: d.ids, parent: d.deferred.id, index: d.deferred.index })
    }
    if (d.mode === 'move' || d.mode === 'resize' || d.mode === 'rotate' || d.mode === 'radius') {
      call('drag_end')
      setGuides([])
      setDrop(null)
      setFloat(null)
      refresh()
      return
    }
    if (d.mode !== 'create') return
    setGhost(null)
    // The tool the draw started with: a tool key pressed mid-drag already switched `tool`.
    const tool = d.tool
    const x0 = Math.round(Math.min(d.dx0, d.dx1))
    const y0 = Math.round(Math.min(d.dy0, d.dy1))
    let bw = Math.round(Math.abs(d.dx1 - d.dx0))
    let bh = Math.round(Math.abs(d.dy1 - d.dy0))
    const clicked = bw < 3 && bh < 3
    if (clicked) {
      bw = tool === 'frame' ? 400 : 100
      bh = tool === 'frame' ? 300 : 100
    }
    // Parent: deepest plain frame under the press point.
    const chain = (tryCall<string[]>('hit', { x: d.dx0, y: d.dy0 }) ?? []).filter((id) => !id.includes('/'))
    const chainBoxes = tryCall<Box[]>('boxes', { ids: chain }) ?? []
    const parentBox = [...chainBoxes].reverse().find((b) => b.type === 'frame')
    const parent = parentBox?.id ?? 'document'
    // Keep drawn layers where they were drawn, even inside auto-layout frames.
    const parentLayout = parentBox ? tryCall('node', { id: parentBox.id })?.node?.layout ?? 'horizontal' : 'none'
    // A rotated or flipped parent paints through its matrix: map the drag's center into the parent's own space and
    // centre the new box there (an upright parent keeps the plain top-left offset). Text has no drawn height, so a
    // dragged text box is centred along its top edge and clicked text keeps its top-left corner.
    const [sw, sh] = tool === 'text' ? (clicked ? [0, 0] : [bw, 0]) : [bw, bh]
    const [cx, cy] = unapply(parentBox?.m, x0 + sw / 2, y0 + sh / 2)
    const at = (v: number) => Math.round(v * 100) / 100
    const rel = { x: at(cx - sw / 2 - (parentBox?.x ?? 0)), y: at(cy - sh / 2 - (parentBox?.y ?? 0)), ...(parentLayout !== 'none' ? { layoutPosition: 'absolute' } : {}) }
    const base = { ...rel, width: bw, height: bh }
    const node =
      tool === 'frame' ? { type: 'frame', name: 'Frame', ...base, fill: '#FFFFFF', layout: 'none', clip: parent === 'document' }
      : tool === 'rectangle' ? { type: 'rectangle', name: 'Rectangle', ...base, fill: '#D9D9D9' }
      : tool === 'ellipse' ? { type: 'ellipse', name: 'Ellipse', ...base, fill: '#D9D9D9' }
      : { type: 'text', name: 'Text', ...rel, content: 'Text', fontFamily: 'Inter', fontSize: 24, fill: '#111111',
          ...(clicked ? {} : { textGrowth: 'fixed-width', width: bw }) }
    const res = tryCall('insert', { parent, node })
    setTool('move')
    if (res?.id) {
      select([res.id])
      if (tool === 'text') setTimeout(() => startEdit(res.id), 30)
    }
  }

  function onScroll(e: EventPayload) {
    const [sx, sy] = local(e)
    // GPUIX reports pixels for mouse wheels too (one notch = 3 lines = 60 px, with `precise: false`);
    // scaling those as lines threw the view thousands of px per notch and the canvas looked empty.
    const ddx = e.deltaX ?? 0
    const ddy = e.deltaY ?? 0
    if (e.modifiers?.ctrl || e.modifiers?.cmd) {
      const factor = Math.exp(ddy * 0.0025)
      const zoom = Math.min(64, Math.max(0.02, cam.zoom * factor))
      const [px, py] = toDoc(sx, sy)
      moveCam({ zoom, x: px - sx / zoom, y: py - sy / zoom })
    } else if (e.modifiers?.shift) {
      moveCam({ ...cam, x: cam.x - ddy / cam.zoom })
    } else {
      moveCam({ ...cam, x: cam.x - ddx / cam.zoom, y: cam.y - ddy / cam.zoom })
    }
  }

  if (props.penKeyRef) props.penKeyRef.current = (key, m) => !!pen.current && penKey(key, m)

  function onKeyDown(e: EventPayload) {
    const key = (e.key ?? '').toLowerCase()
    if (key === 'space') {
      if (!space.current) setSpaceDown(true)
      space.current = true
      return
    }
    if (editing || renaming) return
    const m = e.modifiers
    const mods = { mod: !!(m?.ctrl || m?.cmd), shift: !!m?.shift, alt: !!m?.alt }
    if (pen.current && penKey(key, mods)) return
    // Escape mid-draw cancels it: the release then creates nothing.
    if (key === 'escape' && drag.current?.mode === 'create') {
      drag.current = null
      setGhost(null)
    }
    // Enter on a single text layer edits it, on a path its points (other Enter handling lives in onShortcut).
    const one = key === 'enter' && !mods.mod && !mods.shift && selection.length === 1 ? tryCall('node', { id: selection[0] })?.node?.type : undefined
    if (one === 'text') return startEdit(selection[0])
    if (one === 'path') return penEnter(selection[0], 'move')
    props.onShortcut(key, mods)
  }

  const visibleFrames = frames.filter((f) => !(f as Box & { placeholder?: boolean }).placeholder).filter((f) => {
    const r = screenRect(painted(f))
    return r.x < w && r.y - 21 < h && r.x + r.w > 0 && r.y + r.h > -20
  })
  const pt = (x: number, y: number) => toScreen(x, y)
  // Rotated outlines and handles collect here during render and paint as one image (`Quads`).
  const quads: Quad[] = []
  /** Outline a box where it is painted: a div when axis-aligned, a polygon when rotated. */
  const outline = (b: Box, color: string, width = 1, testId?: string) => {
    if (!turned(b.m)) return <Outline key={b.id} r={screenRect(painted(b))} color={color} width={width} testId={testId} />
    const e = width / 2 / cam.zoom
    quads.push({ pts: corners({ x: b.x - e, y: b.y - e, w: b.w + 2 * e, h: b.h + 2 * e }).map(([x, y]) => screenPt(x, y, b.m)), stroke: color, width })
    return null
  }
  const activeCorner = drag.current?.mode === 'radius' ? drag.current.corner : -1
  const radii = radiiOf(selNode?.node)

  return (
    <div
      testId="canvas"
      role="group"
      aria-label="Design canvas"
      ref={ring.ref}
      tabIndex={0}
      autoFocus
      onMouseDown={onMouseDown}
      onMouseMove={onMouseMove}
      onMouseUp={onMouseUp}
      onScroll={onScroll}
      onKeyDown={onKeyDown}
      onKeyUp={(e) => {
        const key = (e.key ?? '').toLowerCase()
        if (key === 'space') {
          space.current = false
          setSpaceDown(false)
        }
        if (key === 'alt') setMeasured(null)
      }}
      style={{
        position: 'relative', width: w, height: h, overflow: 'hidden', flexShrink: 0, userSelect: 'none', background: CANVAS_BG_CSS,
        cursor: panning ? 'grabbing' : tool === 'hand' || spaceDown ? 'grab' : tool === 'move' ? cursor : 'crosshair',
      }}
    >
      <img ref={img} objectFit="fill" style={{ position: 'absolute', left: 0, top: 0, width: hasFrame ? w : 0, height: hasFrame ? h : 0 }} />
      {visibleFrames.map((f) => {
        const { x, y, w: fw } = screenRect(painted(f))
        const active = selection.includes(f.id)
        const r = renaming?.id === f.id ? renameBox(f.id) : null
        if (renaming && r) {
          return (
            <div key={f.id} style={{ position: 'absolute', left: r.x, top: r.y, width: r.w, height: r.h, borderRadius: 4, borderWidth: 1, borderColor: C.select, backgroundColor: C.panel2, display: 'flex', alignItems: 'center', paddingLeft: 3 }}>
              <input
                testId="frame-rename"
                aria-label="Frame name"
                autoFocus
                value={renaming.draft}
                onChange={(ev) => setRenaming((s) => (s ? { ...s, draft: ev.value ?? '' } : s))}
                onSubmit={(ev) => { commitRename(ev.value); refocus() }}
                onBlur={() => commitRename()}
                onKeyDown={(ev) => {
                  if ((ev.key ?? '').toLowerCase() !== 'escape') return
                  renameLive.current = null
                  setRenaming(null)
                  refocus()
                }}
                style={{ flexGrow: 1, minWidth: 0, fontSize: 11, color: C.text, fontFamily: FONT }}
              />
            </div>
          )
        }
        return (
          <div key={f.id} testId="frame-label" style={{ position: 'absolute', left: x, top: y - 18, height: 16, display: 'flex', maxWidth: Math.max(40, fw), pointerEvents: 'none' }}>
            <T size={11} color={active ? C.select : C.muted} style={FRAME_LABEL_TEXT}>{f.name}</T>
          </div>
        )
      })}
      {hover && !drag.current ? outline(hover, C.select) : null}
      {hoverBox ? outline(hoverBox, C.select, 2, 'layer-hover') : null}
      {pen.current ? null : selBoxes.map((b) => outline(b, C.select, 1.5, 'selection-box'))}
      {selUnion && selBoxes.length > 1 ? <Outline r={screenRect(selUnion)} color={C.select} /> : null}
      {handlesOn && frame && !float ? HANDLES.map((hnd) => {
        const [hx, hy] = handlePoints(frame.box)[hnd]
        if (turned(frame.m)) {
          const s = 3.5 / cam.zoom
          quads.push({ pts: corners({ x: hx - s, y: hy - s, w: 2 * s, h: 2 * s }).map(([x, y]) => frameScreen(x, y)), stroke: C.select, width: 1, fill: '#FFFFFF' })
          return null
        }
        const [x, y] = frameScreen(hx, hy)
        return <div key={hnd} style={{ position: 'absolute', left: x - 4, top: y - 4, width: 8, height: 8, backgroundColor: '#FFFFFF', borderWidth: 1, borderColor: C.select, pointerEvents: 'none' }} />
      }) : null}
      {!float ? radiusHandles().map(({ i, x, y }) => (
        <div key={`radius-${i}`} testId="radius-handle" style={{ position: 'absolute', left: x - 5.5, top: y - 5.5, width: 11, height: 11, borderRadius: 5.5, borderWidth: 1, borderColor: C.select, backgroundColor: i === activeCorner ? C.select : '#FFFFFF', display: 'flex', alignItems: 'center', justifyContent: 'center', pointerEvents: 'none' }}>
          {/* Corners that differ show a dot (they move one at a time). */}
          {radii.every((v) => v === radii[0]) ? null : <div style={{ width: 3, height: 3, borderRadius: 1.5, backgroundColor: i === activeCorner ? '#FFFFFF' : C.select }} />}
        </div>
      )) : null}
      {frame && !editing && (frame.box.w * cam.zoom > 10 || frame.box.h * cam.zoom > 10) ? (() => {
        // Centered 6 px under the frame's lowest painted point.
        const pts = corners(frame.box).map(([x, y]) => frameScreen(x, y))
        const cx = pts.reduce((s, p) => s + p[0], 0) / 4
        const bottom = Math.max(...pts.map((p) => p[1]))
        const label = `${Math.round(frame.box.w * 100) / 100} × ${Math.round(frame.box.h * 100) / 100}`
        return (
          <div testId="size-label" style={{ position: 'absolute', left: cx - (label.length * 6 + 10) / 2, top: bottom + 6, paddingLeft: 5, paddingRight: 5, height: 18, borderRadius: 4, backgroundColor: C.selectSolid, display: 'flex', alignItems: 'center', pointerEvents: 'none' }}>
            <T size={10} color="#FFFFFF" style={{ whiteSpace: 'nowrap' }}>{label}</T>
          </div>
        )
      })() : null}
      {float ? <Outline r={screenRect(float)} color={C.select} testId="drag-ghost" /> : null}
      {drop?.line && drop.x !== undefined ? (() => {
        const [x1, y1] = pt(drop.line[0], drop.line[1])
        const [x2, y2] = pt(drop.line[2], drop.line[3])
        return (
          <>
            {outline({ id: drop.id, name: '', type: 'frame', x: drop.x!, y: drop.y!, w: drop.w!, h: drop.h!, m: drop.m }, C.select, 3, 'drop-target')}
            <Dashed x1={x1} y1={y1} x2={x2} y2={y2} color={C.select} width={2} dash={8} />
          </>
        )
      })() : null}
      {quads.length ? <Quads quads={quads} testId="rotated-overlay" /> : null}
      {guides.map((g, i) => {
        const [a1, b1] = g.axis === 0 ? pt(g.pos, g.from) : pt(g.from, g.pos)
        const [a2, b2] = g.axis === 0 ? pt(g.pos, g.to) : pt(g.to, g.pos)
        return (
          <div key={i} testId="snap-guide" style={{ position: 'absolute', left: 0, top: 0, width: w, height: h, pointerEvents: 'none' }}>
            <Line x1={a1} y1={b1} x2={a2} y2={b2} color={SNAP} />
            {g.marks.map((m) => {
              const [mx, my] = g.axis === 0 ? pt(g.pos, m) : pt(m, g.pos)
              return <svg key={m} source={X_MARK} style={{ position: 'absolute', left: mx - 4, top: my - 4, width: 8, height: 8, color: SNAP }} />
            })}
          </div>
        )
      })}
      {measured ? (
        <>
          <Outline r={screenRect(measured.s)} color={SNAP} />
          {outline(measured.t, SNAP, 1, 'measure-target')}
          {measured.lines.map((m, i) => {
            const [x1, y1] = pt(m.x1, m.y1)
            const [x2, y2] = pt(m.x2, m.y2)
            const ext = m.ext ? [...pt(m.ext[0], m.ext[1]), ...pt(m.ext[2], m.ext[3])] : null
            const label = String(Math.round(m.len * 100) / 100)
            const horizontal = Math.abs(y1 - y2) < 0.5
            const lw = label.length * 6.5 + 8
            return (
              <div key={i} testId="measure-line" style={{ position: 'absolute', left: 0, top: 0, width: w, height: h, pointerEvents: 'none' }}>
                <Line x1={x1} y1={y1} x2={x2} y2={y2} color={SNAP} />
                {ext ? <Dashed x1={ext[0]} y1={ext[1]} x2={ext[2]} y2={ext[3]} color={SNAP} dash={3} /> : null}
                <div style={{
                  position: 'absolute', height: 14, paddingLeft: 4, paddingRight: 4, borderRadius: 3, backgroundColor: SNAP_LABEL, display: 'flex', alignItems: 'center',
                  left: horizontal ? (x1 + x2) / 2 - lw / 2 : x1 + 4, top: horizontal ? y1 + 4 : (y1 + y2) / 2 - 7,
                }}>
                  <T size={11} weight="500" color="#FFFFFF" style={{ whiteSpace: 'nowrap' }}>{label}</T>
                </div>
              </div>
            )
          })}
        </>
      ) : null}
      {marquee ? (() => {
        const r = screenRect(marquee)
        return <div testId="marquee" style={{ position: 'absolute', left: r.x, top: r.y, width: r.w, height: r.h, borderWidth: 1, borderColor: C.select, backgroundColor: '#3B82F61A', pointerEvents: 'none' }} />
      })() : null}
      {pen.current ? (() => {
        // Vector editing: outline, handles and the pen's preview in one SVG, anchors as divs.
        const P = pen.current
        const act = drag.current?.mode === 'pen' ? undefined : P.action
        const ap = P.appending
        const ext = ap && endOf(ap.c, ap.end)
        const scr = (q: Pt): Pt => [(q[0] - cam.x) * cam.zoom, (q[1] - cam.y) * cam.zoom]
        const shown = shownHandles(P.cs, P.sel)
        const dots = act?.kind === 'place' && !act.target ? [act.at] : (act?.kind === 'start' && !act.anchor) || act?.kind === 'insert' ? [act.at] : []
        const preview = ext && (act?.kind === 'place' || act?.kind === 'close') ? { from: ext, handle: ext[ap!.end], to: act.at } : undefined
        const body = overlay({
          cs: P.cs, sel: P.sel, shown, toScreen: scr, dots, preview,
          hoverSeg: act?.kind === 'points' || act?.kind === 'insert' ? act.seg : undefined,
          hoverHandle: act?.kind === 'handle' ? act : undefined,
          activeHandle: drag.current?.mode === 'pen' && drag.current.target.kind === 'handle' ? drag.current.target : undefined,
        })
        // Only the area the overlay covers is rasterized.
        const pts = [...P.cs.flatMap((c) => c.anchors.flatMap((a) => [ptOf(a), a.in, a.out])), preview?.to, ...dots].filter((q): q is Pt => !!q).map(scr)
        const x0 = Math.max(0, Math.floor(Math.min(...pts.map((q) => q[0]))) - 8), y0 = Math.max(0, Math.floor(Math.min(...pts.map((q) => q[1]))) - 8)
        const box = { x: x0, y: y0, w: Math.min(w, Math.ceil(Math.max(...pts.map((q) => q[0]))) + 8) - x0, h: Math.min(h, Math.ceil(Math.max(...pts.map((q) => q[1]))) + 8) - y0 }
        const hot = !ap && (act?.kind === 'points' || act?.kind === 'resume' || act?.kind === 'start') ? act.anchor : undefined
        return (
          <>
            {box.w > 0 && box.h > 0 ? <SvgOverlay body={body} box={box} testId="pen-overlay" /> : null}
            {P.cs.flatMap((c) => c.anchors).map((a, i) => {
              const [x, y] = scr(ptOf(a))
              if (x < -8 || y < -8 || x > w + 8 || y > h + 8) return null
              const on = P.sel.has(a)
              const s = a === hot ? ANCHOR + 3 : ANCHOR
              return <div key={i} testId={on ? 'pen-anchor-selected' : 'pen-anchor'} style={{ position: 'absolute', left: x - s / 2, top: y - s / 2, width: s, height: s, borderRadius: s / 2, borderWidth: 1, borderColor: on ? '#FFFFFF' : PEN_BLUE, backgroundColor: on ? PEN_BLUE : '#FFFFFF', pointerEvents: 'none' }} />
            })}
          </>
        )
      })() : tool === 'pen' && penDot ? (() => {
        const [x, y] = toScreen(penDot[0], penDot[1])
        return <div testId="pen-dot" style={{ position: 'absolute', left: x - 4, top: y - 4, width: 8, height: 8, borderRadius: 4, borderWidth: 1, borderColor: PEN_BLUE, backgroundColor: '#FFFFFF', pointerEvents: 'none' }} />
      })() : null}
      <AgentLayer cam={cam} w={w} h={h} version={version} chatOpen={!!props.chatOpen} />
      {ghost ? <div testId="draw-ghost" style={{ position: 'absolute', left: ghost.x, top: ghost.y, width: ghost.w, height: ghost.h, borderWidth: 1, borderColor: C.select, backgroundColor: '#3B82F622', pointerEvents: 'none' }} /> : null}
      {editing ? (() => {
        // The field stays upright but is centered on where the (rotated/flipped) text is painted.
        const p = painted(editing.box)
        const [x, y] = toScreen(p.x + p.w / 2 - editing.box.w / 2, p.y + p.h / 2 - editing.box.h / 2)
        return (
          <div style={{ position: 'absolute', left: x - 2, top: y - 2, width: Math.max(80, editing.box.w * cam.zoom + 40), borderWidth: 1.5, borderColor: C.select, backgroundColor: '#FFFFFFEE', borderRadius: 3, padding: 2 }}>
            <textarea
              ref={textEl}
              testId="text-editor"
              aria-label="Text content"
              autoFocus
              value={editing.value}
              minRows={1}
              maxRows={12}
              onChange={(ev) => setEditing((s) => (s ? { ...s, value: ev.value ?? '' } : s))}
              onSubmit={(ev) => { commitEdit(ev.value ?? editing.value); refocus() }}
              onBlur={() => commitEdit(editing.value)}
              onKeyDown={(ev) => { if ((ev.key ?? '').toLowerCase() === 'escape') { commitEdit(editing.value); refocus() } }}
              style={{ fontSize: Math.max(10, Math.min(72, editing.size * cam.zoom)), color: '#111111', fontFamily: FONT, width: '100%' }}
            />
          </div>
        )
      })() : null}
      {ring.on ? <FocusRing radius={0} inset /> : null}
    </div>
  )
}
