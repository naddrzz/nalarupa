// Vector editing (path-editing state and pen tool): contours of
// anchors with absolute in/out handles, in canvas coordinates (the core maps them to a path's geometry).

import { snap, type Guide, type Pt } from './geometry'

export type Anchor = { x: number; y: number; in?: Pt; out?: Pt }
export type Contour = { anchors: Anchor[]; closed: boolean }
export type Side = 'in' | 'out'
/** A segment from anchor `i` of contour `c` to the next one. */
export type Seg = { c: Contour; i: number; from: Anchor; to: Anchor }

// Sizes (screen px): anchors 8 (hover 11), handles 9 (hover 12), anchor/handle hit radius 9,
// segment hit 10, point snap 8, segment snap 7, drag threshold 3; positions round to 0.5 px.
export const ANCHOR = 8
export const HANDLE = 9
export const HIT = ANCHOR / 2 + 5
export const SEG_HIT = 10
export const POINT_SNAP = 8
export const SEG_SNAP = 7
export const AXIS_SNAP = 5
export const DRAG = 3
export const GRID = 0.5
const BLUE = '#3D99FF'
const GRAY = '#AAAAAA'

const d2 = (a: Pt, b: Pt) => (a[0] - b[0]) ** 2 + (a[1] - b[1]) ** 2
export const pt = (a: Anchor): Pt => [a.x, a.y]
export const other = (s: Side): Side => (s === 'in' ? 'out' : 'in')
export const mirror = (a: Anchor, p: Pt): Pt => [a.x * 2 - p[0], a.y * 2 - p[1]]
export const round = (v: number, step = GRID) => Math.round(v / step) * step
export const clone = (a: Anchor): Anchor => ({ x: a.x, y: a.y, in: a.in && [...a.in], out: a.out && [...a.out] })
/** Two anchors, or one closed onto itself with a handle. */
export const drawable = (c: Contour) => c.anchors.length >= 2 || (c.closed && c.anchors.length === 1 && !!(c.anchors[0].in || c.anchors[0].out))
/** First (`in`) or last (`out`) anchor of a contour. */
export const endOf = (c: Contour, end: Side) => c.anchors[end === 'in' ? 0 : c.anchors.length - 1]

/** The open contour `a` ends, and which end. */
export function endpoint(cs: Contour[], a: Anchor): { c: Contour; end: Side } | undefined {
  for (const c of cs) {
    if (c.closed) continue
    if (c.anchors[c.anchors.length - 1] === a) return { c, end: 'out' }
    if (c.anchors[0] === a) return { c, end: 'in' }
  }
}

/** Handles opposite each other within 2% of the longer one: they move together. */
export function mirrored(a: Anchor): boolean {
  if (!a.in || !a.out) return false
  const [ix, iy, ox, oy] = [a.in[0] - a.x, a.in[1] - a.y, a.out[0] - a.x, a.out[1] - a.y]
  const [li, lo] = [Math.hypot(ix, iy), Math.hypot(ox, oy)]
  if (li < 1e-6 || lo < 1e-6) return false
  const tol = 0.02 * Math.max(li, lo)
  return Math.abs(ix + ox) < tol && Math.abs(iy + oy) < tol
}

export function segments(cs: Contour[]): Seg[] {
  const out: Seg[] = []
  for (const c of cs) {
    const n = c.anchors.length - (c.closed ? 0 : 1)
    for (let i = 0; i < n; i++) out.push({ c, i, from: c.anchors[i], to: c.anchors[(i + 1) % c.anchors.length] })
  }
  return out
}

const curved = (a: Anchor, b: Anchor) => !!(a.out || b.in)
const bez = (a: Anchor, b: Anchor): [Pt, Pt, Pt, Pt] => [pt(a), a.out ?? pt(a), b.in ?? pt(b), pt(b)]
const cubic = (p: [Pt, Pt, Pt, Pt], t: number): Pt => {
  const s = 1 - t
  const k = [s * s * s, 3 * s * s * t, 3 * s * t * t, t * t * t]
  return [k[0] * p[0][0] + k[1] * p[1][0] + k[2] * p[2][0] + k[3] * p[3][0], k[0] * p[0][1] + k[1] * p[1][1] + k[2] * p[2][1] + k[3] * p[3][1]]
}
export const pointAt = (a: Anchor, b: Anchor, t: number): Pt => cubic(bez(a, b), t)

/** Closest point of a segment to `p`: exact for lines, sampled then refined for curves. */
export function nearest(a: Anchor, b: Anchor, p: Pt): { t: number; at: Pt; dist2: number } {
  if (!curved(a, b)) {
    const [dx, dy] = [b.x - a.x, b.y - a.y]
    const len = dx * dx + dy * dy
    const t = len > 0 ? Math.min(1, Math.max(0, ((p[0] - a.x) * dx + (p[1] - a.y) * dy) / len)) : 0
    const at: Pt = [a.x + dx * t, a.y + dy * t]
    return { t, at, dist2: d2(at, p) }
  }
  const c = bez(a, b)
  let best = 0
  let bd = Infinity
  for (let i = 0; i <= 64; i++) {
    const v = d2(cubic(c, i / 64), p)
    if (v < bd) [bd, best] = [v, i / 64]
  }
  for (let step = 1 / 128; step > 1e-6; step /= 2) {
    for (const t of [best - step, best + step]) {
      if (t < 0 || t > 1) continue
      const v = d2(cubic(c, t), p)
      if (v < bd) [bd, best] = [v, t]
    }
  }
  const at = cubic(c, best)
  return { t: best, at, dist2: bd }
}

/** Splits a segment at `t` (de Casteljau), inserting and returning the new anchor. */
export function split(s: Seg, t: number): Anchor {
  t = Math.min(1 - 1e-4, Math.max(1e-4, t))
  const { from: a, to: b } = s
  let n: Anchor
  if (!curved(a, b)) n = { x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t }
  else {
    const [p0, p1, p2, p3] = bez(a, b)
    const lerp = (p: Pt, q: Pt): Pt => [p[0] + (q[0] - p[0]) * t, p[1] + (q[1] - p[1]) * t]
    const [q0, q1, q2] = [lerp(p0, p1), lerp(p1, p2), lerp(p2, p3)]
    const [r0, r1] = [lerp(q0, q1), lerp(q1, q2)]
    const m = lerp(r0, r1)
    a.out = a.out ? q0 : undefined
    b.in = b.in ? q2 : undefined
    n = { x: m[0], y: m[1], in: r0, out: r1 }
  }
  s.c.anchors.splice(s.i + 1, 0, n)
  return n
}

export function hitAnchor(cs: Contour[], p: Pt, zoom: number): Anchor | undefined {
  let best: Anchor | undefined
  let bd = (HIT / zoom) ** 2
  for (const c of cs) for (const a of c.anchors) {
    const v = d2(pt(a), p)
    if (v <= bd) [bd, best] = [v, a]
  }
  return best
}

/** Handles shown: selected anchors' own and their neighbours' facing ones. */
export function shownHandles(cs: Contour[], sel: Set<Anchor>): Set<Anchor> {
  const out = new Set<Anchor>()
  for (const c of cs) c.anchors.forEach((a, i) => {
    if (!sel.has(a)) return
    out.add(a)
    const n = c.anchors.length
    if (i > 0 || (c.closed && n > 1)) out.add(c.anchors[(i - 1 + n) % n])
    if (i < n - 1 || (c.closed && n > 1)) out.add(c.anchors[(i + 1) % n])
  })
  return out
}

export function hitHandle(shown: Set<Anchor>, p: Pt, zoom: number): { anchor: Anchor; side: Side } | undefined {
  let best: { anchor: Anchor; side: Side } | undefined
  let bd = (HIT / zoom) ** 2
  for (const a of shown) for (const side of ['in', 'out'] as const) {
    const h = a[side]
    // A handle sitting on its anchor is grabbed as the anchor.
    if (!h || d2(h, pt(a)) <= (ANCHOR / 2 / zoom) ** 2) continue
    const v = d2(h, p)
    if (v <= bd) [bd, best] = [v, { anchor: a, side }]
  }
  return best
}

/** Nearest segment within `px` screen px of any of `pts`, skipping segments touching `skip`. */
export function hitSegment(cs: Contour[], pts: Pt[], zoom: number, skip?: Set<Anchor>, px = SEG_HIT) {
  let best: (Seg & { t: number; at: Pt; index: number }) | undefined
  let bd = (px / zoom) ** 2
  for (const s of segments(cs)) {
    if (skip?.has(s.from) || skip?.has(s.to)) continue
    pts.forEach((p, index) => {
      const n = nearest(s.from, s.to, p)
      if (n.dist2 < bd) [bd, best] = [n.dist2, { ...s, t: n.t, at: n.at, index }]
    })
  }
  return best
}

/** The direction from `from` to `to` snapped to 45° steps, same length. */
export function constrain(from: Pt, to: Pt): Pt {
  const [dx, dy] = [to[0] - from[0], to[1] - from[1]]
  const len = Math.hypot(dx, dy)
  if (len < 1e-9) return to
  const a = Math.round(Math.atan2(dy, dx) / (Math.PI / 4)) * (Math.PI / 4)
  return [from[0] + Math.cos(a) * len, from[1] + Math.sin(a) * len]
}

/** Snap along a ray: slide along from→to to the nearest candidate x or y within `max` canvas units. */
export function snapRay(from: Pt, to: Pt, candidates: Pt[], max: number): Pt | undefined {
  const [dx, dy] = [to[0] - from[0], to[1] - from[1]]
  const len = Math.hypot(dx, dy)
  let best: number | undefined
  let bd = max
  for (const c of candidates) for (const a of [0, 1] as const) {
    const d = a === 0 ? dx : dy
    if (Math.abs(d) <= 1e-6) continue
    const k = (c[a] - from[a]) / d
    const dist = Math.abs(k - 1) * len
    if (k > 0 && dist <= bd) [bd, best] = [dist, k]
  }
  return best === undefined ? undefined : [from[0] + dx * best, from[1] + dy * best]
}

/** Round to the grid along a ray: the grid line crossing the ray nearest `to`. */
export function roundRay(from: Pt, to: Pt): Pt {
  const [dx, dy] = [to[0] - from[0], to[1] - from[1]]
  let k: number | undefined
  let axis = 0
  if (Math.abs(dx) > 1e-6) {
    const t = (round(to[0]) - from[0]) / dx
    if (t >= 0) [k, axis] = [t, 0]
  }
  if (Math.abs(dy) > 1e-6) {
    const t = (round(to[1]) - from[1]) / dy
    if (t >= 0 && (k === undefined || Math.abs(t - 1) < Math.abs(k - 1))) [k, axis] = [t, 1]
  }
  if (k === undefined) return to
  return axis === 0 ? [round(to[0]), from[1] + dy * k] : [from[0] + dx * k, round(to[1])]
}

/**
 * Snaps dragged points `pts` against the path's other anchors: onto an anchor within 8 px,
 * else onto a segment within 7 px (its midpoint within 8 px), else to the 0.5 px grid plus axis alignment
 * with an anchor within 5 px (with guides). `moved` anchors are no candidates; segments touching `skip` are
 * ignored. `on` = snapping enabled (Ctrl disables it): grid only.
 * ponytail: no sliding along segments to anchor-aligned and whole-pixel spots; add if missed.
 */
export function snapPoints(pts: Pt[], cs: Contour[], moved: Set<Anchor>, zoom: number, on: boolean, skip = moved): { offset: Pt; guides: Guide[] } {
  const pull = (): Pt => {
    let [bx, by] = [Infinity, Infinity]
    for (const p of pts) {
      const [ox, oy] = [round(p[0]) - p[0], round(p[1]) - p[1]]
      if (Math.abs(ox) < Math.abs(bx)) bx = ox
      if (Math.abs(oy) < Math.abs(by)) by = oy
    }
    return pts.length ? [bx, by] : [0, 0]
  }
  if (!on) return { offset: pull(), guides: [] }
  const candidates = cs.flatMap((c) => c.anchors.filter((a) => !moved.has(a)).map(pt))
  let near: Pt | undefined
  let bd = (POINT_SNAP / zoom) ** 2
  for (const p of pts) for (const c of candidates) {
    const v = d2(p, c)
    if (v <= bd) [bd, near] = [v, [c[0] - p[0], c[1] - p[1]]]
  }
  if (near) return { offset: near, guides: [] }
  const s = hitSegment(cs, pts, zoom, skip, SEG_SNAP)
  if (s) {
    let at = s.at
    if (!curved(s.from, s.to)) {
      const mid = pointAt(s.from, s.to, 0.5)
      if (d2(mid, pts[s.index]) <= (POINT_SNAP / zoom) ** 2) at = mid
    }
    return { offset: [at[0] - pts[s.index][0], at[1] - pts[s.index][1]], guides: [] }
  }
  const g = pull()
  const r = snap(pts.map((p) => [p[0] + g[0], p[1] + g[1]] as Pt), candidates, AXIS_SNAP / zoom)
  return { offset: [g[0] + r.offset[0], g[1] + r.offset[1]], guides: r.guides }
}

/** Double-click on an anchor: both handles → corner, one → mirror it, none → smooth along the neighbours. */
export function toggleSmooth(c: Contour, a: Anchor) {
  if (a.in && a.out) [a.in, a.out] = [undefined, undefined]
  else if (a.in) a.out = mirror(a, a.in)
  else if (a.out) a.in = mirror(a, a.out)
  else {
    const i = c.anchors.indexOf(a)
    const n = c.anchors.length
    const prev = i > 0 || (c.closed && n > 1) ? c.anchors[(i - 1 + n) % n] : undefined
    const next = i < n - 1 || (c.closed && n > 1) ? c.anchors[(i + 1) % n] : undefined
    const p = prev && (prev.out ?? pt(prev))
    const q = next && (next.in ?? pt(next))
    const [hx, hy] = p && q ? [(q[0] - p[0]) / 6, (q[1] - p[1]) / 6] : q ? [(q[0] - a.x) / 3, (q[1] - a.y) / 3] : p ? [(a.x - p[0]) / 3, (a.y - p[1]) / 3] : [0, 0]
    if (hx === 0 && hy === 0) return
    if (prev) a.in = [a.x - hx, a.y - hy]
    if (next) a.out = [a.x + hx, a.y + hy]
  }
}

/** Removes selected anchors (closed contours stay closed); returns the anchors to select next. */
export function deletePoints(cs: Contour[], sel: Set<Anchor>): { contours: Contour[]; next: Anchor[] } {
  const contours: Contour[] = []
  const next: Anchor[] = []
  for (const c of cs) {
    const kept: Anchor[] = []
    const near: Anchor[] = []
    let pending = false
    for (const a of c.anchors) {
      if (sel.has(a)) {
        if (kept.length) near.push(kept[kept.length - 1])
        else pending = true
        continue
      }
      kept.push(a)
      if (pending) {
        near.push(a)
        pending = false
      }
    }
    const d = { anchors: kept, closed: c.closed }
    if (drawable(d)) {
      contours.push(d)
      next.push(...near)
    }
  }
  return { contours, next }
}

/** Overlay geometry in screen px: outline, hovered segment, handles, pen preview (anchors are drawn as divs). */
export function overlay(o: {
  cs: Contour[]; sel: Set<Anchor>; shown: Set<Anchor>; toScreen: (p: Pt) => Pt
  hoverSeg?: Seg; hoverHandle?: { anchor: Anchor; side: Side }; activeHandle?: { anchor: Anchor; side: Side }
  preview?: { from: Anchor; handle?: Pt; to: Pt }; dots?: Pt[]
}): string {
  const r = (v: number) => Math.round(v * 100) / 100
  const xy = (p: Pt) => { const [x, y] = o.toScreen(p); return `${r(x)} ${r(y)}` }
  const segD = (a: Anchor, b: Anchor) => (curved(a, b) ? `C${xy(a.out ?? pt(a))} ${xy(b.in ?? pt(b))} ${xy(pt(b))}` : `L${xy(pt(b))}`)
  let d = ''
  for (const c of o.cs) {
    if (!c.anchors.length) continue
    d += `M${xy(pt(c.anchors[0]))}`
    for (const s of segments([c])) d += segD(s.from, s.to)
  }
  const out = [`<path d="${d}" fill="none" stroke="${GRAY}" stroke-width="1"/>`]
  if (o.hoverSeg) out.push(`<path d="M${xy(pt(o.hoverSeg.from))}${segD(o.hoverSeg.from, o.hoverSeg.to)}" fill="none" stroke="${BLUE}" stroke-opacity="0.4" stroke-width="2.5"/>`)
  if (o.preview) {
    const { from, handle, to } = o.preview
    const tail = handle ? `C${xy(handle)} ${xy(to)} ${xy(to)}` : `L${xy(to)}`
    out.push(`<path d="M${xy(pt(from))}${tail}" fill="none" stroke="${BLUE}" stroke-width="1"/>`)
  }
  for (const a of o.shown) for (const side of ['in', 'out'] as const) {
    const h = a[side]
    if (!h) continue
    const [ax, ay] = o.toScreen(pt(a))
    const [hx, hy] = o.toScreen(h)
    const on = o.activeHandle?.anchor === a && o.activeHandle.side === side
    const hover = o.hoverHandle?.anchor === a && o.hoverHandle.side === side
    const k = (hover ? HANDLE + 3 : HANDLE) / 2
    out.push(`<line x1="${r(ax)}" y1="${r(ay)}" x2="${r(hx)}" y2="${r(hy)}" stroke="${BLUE}" stroke-width="1"/>`)
    out.push(`<path d="M${r(hx)} ${r(hy - k)}L${r(hx + k)} ${r(hy)}L${r(hx)} ${r(hy + k)}L${r(hx - k)} ${r(hy)}Z" fill="${on ? BLUE : '#FFFFFF'}" stroke="${on ? '#FFFFFF' : BLUE}" stroke-width="1"/>`)
  }
  for (const p of o.dots ?? []) {
    const [x, y] = o.toScreen(p)
    out.push(`<circle cx="${r(x)}" cy="${r(y)}" r="4" fill="#FFFFFF" stroke="${BLUE}" stroke-width="1"/>`)
  }
  return out.join('')
}

// Self-check: `bun src/pen.ts`
if (import.meta.main) {
  const eq = (a: unknown, b: unknown, m: string) => {
    if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${m}: ${JSON.stringify(a)} != ${JSON.stringify(b)}`)
  }
  const a: Anchor = { x: 0, y: 0 }, b: Anchor = { x: 100, y: 0 }, c: Anchor = { x: 100, y: 100 }
  const cs: Contour[] = [{ anchors: [a, b, c], closed: false }]
  const r2 = (p: Pt | undefined) => p?.map((v) => Math.round(v * 100) / 100)
  eq([constrain([0, 0], [10, 9]), constrain([0, 0], [10, 3])].map((p) => p.map(Math.round)), [[10, 10], [10, 0]], '45° steps keep the length')
  eq(snapRay([0, 0], [10, 10.4], [[50, 0]], 8), undefined, 'far candidates do not pull along the ray')
  eq(snapRay([0, 0], [48, 48], [[50, 0]], 8), [50, 50], 'slides to the candidate x on the ray')
  eq(r2(roundRay([0, 0], [10.2, 20.4])), [10.25, 20.5], 'grid line nearest the ray end')
  eq(snapPoints([[97, 3]], cs, new Set(), 1, true).offset, [3, -3], 'onto an anchor within 8 px')
  eq(snapPoints([[52, 4]], cs, new Set(), 1, true).offset, [-2, -4], 'onto the midpoint of a straight segment')
  const g = snapPoints([[30.2, 97]], [{ anchors: [{ x: 0, y: 100 }], closed: false }], new Set(), 1, true)
  eq([r2(g.offset), g.guides.map((x) => [x.axis, x.pos])], [[-0.2, 3], [[1, 100]]], 'grid pull plus axis alignment, with a guide')
  eq(r2(snapPoints([[30.2, 97]], cs, new Set(), 1, false).offset), [-0.2, 0], 'Ctrl: grid only')
  const s = segments(cs)[0]
  const n = split(s, 0.25)
  eq([cs[0].anchors.length, n.x, n.y], [4, 25, 0], 'splitting a line inserts a point on it')
  toggleSmooth(cs[0], b)
  eq([r2(b.in), r2(b.out)], [[87.5, -16.67], [112.5, 16.67]], 'corner → smooth along the neighbours')
  eq(mirrored(b), true, 'smooth handles mirror')
  toggleSmooth(cs[0], b)
  eq([b.in, b.out], [undefined, undefined], 'smooth → corner')
  const del = deletePoints(cs, new Set([b]))
  eq([del.contours[0].anchors.length, del.next.map(pt)], [3, [[25, 0]]], 'deleting selects the previous point')
  const curve: Anchor = { x: 0, y: 0, out: [0, 50] }
  const end: Anchor = { x: 100, y: 0, in: [100, 50] }
  eq(nearest(curve, end, [50, 80]).at.map(Math.round), [50, 38], 'closest point on a curve')
  console.log('pen ok')
}
