// Pure canvas geometry (snapping and Alt-measurement), in document units.

export type Pt = [number, number]
export type Rect = { x: number; y: number; w: number; h: number }
/** A snap guide on `axis` (0 = vertical line at x = pos, 1 = horizontal at y = pos) from..to, with X marks. */
export type Guide = { axis: 0 | 1; pos: number; from: number; to: number; marks: number[] }
/** A measurement line (x1,y1)→(x2,y2) labelled `len`, with an optional dashed extension. */
export type Measure = { x1: number; y1: number; x2: number; y2: number; len: number; ext?: [number, number, number, number] }

/** The core's painted transform `[a, b, c, d, e, f]`: x' = a·x + c·y + e, y' = b·x + d·y + f. Null = identity. */
export type M = [number, number, number, number, number, number]

export const apply = (m: M | null | undefined, x: number, y: number): Pt => (m ? [m[0] * x + m[2] * y + m[4], m[1] * x + m[3] * y + m[5]] : [x, y])
/** Inverse of `apply`; `lin` leaves the translation out (for deltas). */
export const unapply = (m: M | null | undefined, x: number, y: number, lin = false): Pt => {
  if (!m) return [x, y]
  const det = m[0] * m[3] - m[1] * m[2]
  const [u, v] = lin ? [x, y] : [x - m[4], y - m[5]]
  return [(m[3] * u - m[2] * v) / det, (m[0] * v - m[1] * u) / det]
}
export const lin = (m: M | null | undefined, x: number, y: number): Pt => apply(m && [m[0], m[1], m[2], m[3], 0, 0], x, y)
/** Rotated (flips alone keep a box axis-aligned). */
export const turned = (m: M | null | undefined) => !!m && (Math.abs(m[1]) > 1e-9 || Math.abs(m[2]) > 1e-9)
/** A node's own rotation (CCW degrees) and flips about its origin, as in the core. */
export const own = (deg: number, flipX: boolean, flipY: boolean): M => {
  const t = (-deg * Math.PI) / 180
  const [sx, sy] = [flipX ? -1 : 1, flipY ? -1 : 1]
  return [Math.cos(t) * sx, Math.sin(t) * sx, -Math.sin(t) * sy, Math.cos(t) * sy, 0, 0]
}
/** Offset from a node's origin to its center in its parent's space (rotation and flips about the origin). */
export const centerOffset = (w: number, h: number, deg: number, flipX: boolean, flipY: boolean): Pt => lin(own(deg, flipX, flipY), w / 2, h / 2)
/** The origin that keeps a box's center put when its rotation goes from `from` to `to` degrees. */
export const turnAroundCenter = (x: number, y: number, w: number, h: number, from: number, to: number, flipX: boolean, flipY: boolean): Pt => {
  const [ox0, oy0] = centerOffset(w, h, from, flipX, flipY)
  const [ox, oy] = centerOffset(w, h, to, flipX, flipY)
  return [Math.round((x + ox0 - ox) * 100) / 100, Math.round((y + oy0 - oy) * 100) / 100]
}
/** Corners tl, tr, br, bl of `r` under `m`. */
export const corners = (r: Rect, m?: M | null): Pt[] =>
  ([[r.x, r.y], [r.x + r.w, r.y], [r.x + r.w, r.y + r.h], [r.x, r.y + r.h]] as Pt[]).map(([x, y]) => apply(m, x, y))
export const bounds = (pts: Pt[]): Rect => {
  const xs = pts.map((p) => p[0]), ys = pts.map((p) => p[1])
  const [x, y] = [Math.min(...xs), Math.min(...ys)]
  return { x, y, w: Math.max(...xs) - x, h: Math.max(...ys) - y }
}
/** Where a box is painted (its rotated/flipped corners' bounds). */
export const painted = (b: Rect & { m?: M | null }): Rect => (b.m ? bounds(corners(b, b.m)) : b)

export const unionRect = (rs: Rect[]): Rect | null => {
  if (!rs.length) return null
  const x = Math.min(...rs.map((r) => r.x)), y = Math.min(...rs.map((r) => r.y))
  return { x, y, w: Math.max(...rs.map((r) => r.x + r.w)) - x, h: Math.max(...rs.map((r) => r.y + r.h)) - y }
}

/** The dragged selection snaps by its corners and center (pixel-rounded, so offsets stay whole). */
export const dragPoints = (r: Rect): Pt[] =>
  ([[r.x, r.y], [r.x + r.w, r.y], [r.x, r.y + r.h], [r.x + r.w, r.y + r.h], [r.x + r.w / 2, r.y + r.h / 2]] as Pt[]).map(([x, y]) => [Math.round(x), Math.round(y)])

/** Targets offer corners and edge midpoints (rounded to whole pixels). */
export const anchorPoints = (r: Rect): Pt[] => {
  const [x0, y0, x1, y1] = [r.x, r.y, r.x + r.w, r.y + r.h].map(Math.round)
  const [cx, cy] = [Math.round(r.x + r.w / 2), Math.round(r.y + r.h / 2)]
  return [[x0, y0], [x1, y0], [x0, y1], [x1, y1], [cx, y0], [cx, y1], [x0, cy], [x1, cy]]
}

/**
 * Per axis, the smallest offset (< threshold) that puts one of `pts` on one of `anchors`.
 * Every point pair at that offset contributes to the guide drawn on the snapped line.
 */
export function snap(pts: Pt[], anchors: Pt[], threshold: number, axes: [boolean, boolean] = [true, true]): { offset: Pt; guides: Guide[] } {
  const best = [Infinity, Infinity]
  let hits: [Pt, Pt][][] = [[], []]
  for (const s of pts) for (const o of anchors) for (const a of [0, 1] as const) {
    if (!axes[a]) continue
    const d = o[a] - s[a]
    if (Math.abs(d) < threshold && Math.abs(d) <= Math.abs(best[a])) {
      if (d !== best[a]) hits[a] = []
      hits[a].push([s, o])
      best[a] = d
    }
  }
  const offset: Pt = [Number.isFinite(best[0]) ? best[0] : 0, Number.isFinite(best[1]) ? best[1] : 0]
  const guides: Guide[] = []
  for (const a of [0, 1] as const) {
    if (!hits[a].length) continue
    const b = 1 - a
    const snapped = hits[a].map(([s]) => s[b] + offset[b])
    const anchored = hits[a].map(([, o]) => o[b])
    const all = [...snapped, ...anchored]
    const marks = [...new Set([Math.min(...snapped), Math.max(...snapped), Math.min(...anchored), Math.max(...anchored)])]
    guides.push({ axis: a, pos: hits[a][0][1][a], from: Math.min(...all), to: Math.max(...all), marks })
  }
  return { offset, guides }
}

/** Distances from `s` (selection) to `t` (hovered) on both axes, for the Alt overlay. */
export function measure(s: Rect, t: Rect): Measure[] {
  const out: Measure[] = []
  const lo = (r: Rect, a: number) => (a === 0 ? r.x : r.y)
  const hi = (r: Rect, a: number) => (a === 0 ? r.x + r.w : r.y + r.h)
  for (const a of [0, 1]) {
    const c = 1 - a
    const overlapCross = hi(s, c) > lo(t, c) && hi(t, c) > lo(s, c)
    const spans: [number, number][] =
      lo(t, a) >= hi(s, a) ? [[hi(s, a), lo(t, a)]]
      : hi(t, a) <= lo(s, a) ? [[hi(t, a), lo(s, a)]]
      : overlapCross ? [[lo(s, a), lo(t, a)], [hi(s, a), hi(t, a)]]
      : []
    const at = (lo(s, c) + hi(s, c)) / 2
    for (const [p, q] of spans) {
      const len = Math.abs(q - p)
      if (len < 0.005) continue
      const m: Measure = a === 0 ? { x1: p, y1: at, x2: q, y2: at, len } : { x1: at, y1: p, x2: at, y2: q, len }
      // The line runs at the selection's center; reach the target with a dashed extension.
      if (at < lo(t, c) || at > hi(t, c)) {
        const edge = at < lo(t, c) ? lo(t, c) : hi(t, c)
        const end = Math.abs(q - lo(t, a)) < 1e-9 || Math.abs(q - hi(t, a)) < 1e-9 ? q : p
        m.ext = a === 0 ? [end, at, end, edge] : [at, end, edge, end]
      }
      out.push(m)
    }
  }
  return out
}

/**
 * Resize: move the handle's edges by the delta (×2 with Alt, centered), Shift keeps the aspect ratio.
 * Dragged past the opposite edge the size goes negative (`x`/`y` stay the node's origin side): the node flips.
 */
export function resizeBox(b: Rect, handle: string, dx: number, dy: number, shift: boolean, alt: boolean): Rect {
  const k = alt ? 2 : 1
  let w = b.w + (handle.includes('e') ? dx * k : handle.includes('w') ? -dx * k : 0)
  let h = b.h + (handle.includes('s') ? dy * k : handle.includes('n') ? -dy * k : 0)
  const horiz = handle === 'e' || handle === 'w'
  const vert = handle === 'n' || handle === 's'
  if (shift && b.w > 0 && b.h > 0) {
    // The derived axis keeps its own direction; an edge drag never flips it.
    const aspect = b.w / b.h
    if (vert || (!horiz && Math.abs(h / b.h) > Math.abs(w / b.w))) w = (vert || w >= 0 ? 1 : -1) * Math.abs(h * aspect)
    else h = (horiz || h >= 0 ? 1 : -1) * Math.abs(w / aspect)
  }
  w = (w < 0 ? -1 : 1) * Math.max(1, Math.abs(w))
  h = (h < 0 ? -1 : 1) * Math.max(1, Math.abs(h))
  const cx = b.x + b.w / 2, cy = b.y + b.h / 2
  // Alt grows around the center; a Shift edge drag centers the derived axis.
  const x = alt || (shift && vert) ? cx - w / 2 : handle.includes('w') ? b.x + b.w - w : b.x
  const y = alt || (shift && horiz) ? cy - h / 2 : handle.includes('n') ? b.y + b.h - h : b.y
  return { x, y, w, h }
}

/** Resize cursor for a handle whose outward direction (box coordinates) is drawn through `m`. */
export function resizeCursor(handle: string, m: M | null | undefined): 'ns-resize' | 'ew-resize' | 'nwse-resize' | 'nesw-resize' {
  const [vx, vy] = lin(m, handle.includes('e') ? 1 : handle.includes('w') ? -1 : 0, handle.includes('s') ? 1 : handle.includes('n') ? -1 : 0)
  const a = (((Math.atan2(vy, vx) * 180) / Math.PI) % 180 + 180) % 180
  return a < 22.5 || a >= 157.5 ? 'ew-resize' : a < 67.5 ? 'nwse-resize' : a < 112.5 ? 'ns-resize' : 'nesw-resize'
}

// Self-check: `bun src/geometry.ts`
if (import.meta.main) {
  const eq = (a: unknown, b: unknown, m: string) => {
    if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${m}: ${JSON.stringify(a)} != ${JSON.stringify(b)}`)
  }
  const r = snap(dragPoints({ x: 103, y: 50, w: 20, h: 20 }), anchorPoints({ x: 0, y: 0, w: 100, h: 100 }), 5)
  eq(r.offset, [-3, 0], 'left edge snaps to the right edge of the target; y is already on the midpoint line')
  eq(r.guides.map((g) => [g.axis, g.pos]), [[0, 100], [1, 50]], 'one guide per axis')
  eq(snap([[10, 10]], [[20, 20]], 5).offset, [0, 0], 'beyond the threshold nothing snaps')
  eq(measure({ x: 0, y: 0, w: 10, h: 10 }, { x: 30, y: 0, w: 10, h: 10 }).map((m) => m.len), [20], 'gap to the right')
  eq(measure({ x: 10, y: 10, w: 10, h: 10 }, { x: 0, y: 0, w: 100, h: 100 }).map((m) => m.len), [10, 80, 10, 80], 'inside a parent: four distances')
  eq(resizeBox({ x: 0, y: 0, w: 100, h: 50 }, 'se', 20, 0, true, false), { x: 0, y: 0, w: 120, h: 60 }, 'shift keeps the aspect')
  eq(resizeBox({ x: 0, y: 0, w: 100, h: 50 }, 'e', 10, 0, false, true), { x: -10, y: 0, w: 120, h: 50 }, 'alt resizes from the center')
  eq(resizeBox({ x: 0, y: 0, w: 100, h: 50 }, 'e', -150, 0, false, false), { x: 0, y: 0, w: -50, h: 50 }, 'past the left edge: flipped, origin stays')
  eq(resizeBox({ x: 0, y: 0, w: 100, h: 50 }, 'nw', 130, 80, false, false), { x: 130, y: 80, w: -30, h: -30 }, 'past both edges: flipped on both axes')
  eq(resizeBox({ x: 0, y: 0, w: 100, h: 50 }, 'w', 150, 0, true, false), { x: 150, y: 12.5, w: -50, h: 25 }, 'shift edge flip keeps the derived axis positive')
  const m = own(90, false, false)
  eq(apply(m, 10, 0).map(Math.round), [0, -10], '90° turns +x to screen up (CCW)')
  eq(unapply([0, -1, 1, 0, 5, 7], ...apply([0, -1, 1, 0, 5, 7], 3, 4)).map((v) => Math.round(v * 1e9) / 1e9), [3, 4], 'unapply inverts apply')
  eq(painted({ x: 0, y: 0, w: 20, h: 10, m: [-1, 0, 0, 1, 0, 0] }), { x: -20, y: 0, w: 20, h: 10 }, 'flipX paints left of the origin')
  eq([resizeCursor('e', null), resizeCursor('e', m), resizeCursor('ne', null), resizeCursor('ne', m)], ['ew-resize', 'ns-resize', 'nesw-resize', 'nwse-resize'], 'cursors turn with the box')
  eq(turnAroundCenter(0, 0, 100, 50, 0, 90, false, false), [25, 75], 'turning keeps the center put')
  console.log('geometry ok')
}
