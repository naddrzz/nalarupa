// Agent presence on the canvas:
// per-agent cursors with name pills, flashes on touched nodes, the "generating"
// glow on placeholder frames, shimmer on pending images, and agent message pills.
// Positions are kept in document units and projected with the camera every frame.
import { Fragment, useEffect, useMemo, useRef, useState } from 'react'
import { tryCall, type Box } from './core'
import { FADE_DELAY, FADE_DUR, FADE_IN, GONE, PILL_MS, PRESENCE, cursorFaded, needsFrame, profileOf, store, type CursorState } from './agents'
import { ICONS } from './icons'
import { painted, turned, type M, type Rect } from './geometry'
import { SvgOverlay } from './ui'
import { C, FONT, REDUCED_MOTION } from './theme'
import type { Cam } from './Canvas'

// ── constants ───────────────────────────────────────────────────────────────
const DECAY = 2.5            // cursor approach rate, 1/s
const WOBBLE_AMP = 4
const REST_DELAY = 250
const WOBBLE_DECAY = 3
const ATTACK = 200
const RELEASE = 300
const SCAN_DELAY = 40
const PEN_DECAY = 40         // the AI Pen follows its points much faster than agent cursors

const easeOutCubic = (t: number) => 1 - Math.pow(1 - t, 3)
const easeOutBack = (t: number, s = 2) => 1 + (s + 1) * Math.pow(t - 1, 3) + s * Math.pow(t - 1, 2)
// Under reduced motion cursors and the pen jump to their target, glows and pills hold still, and only
// opacity changes (fades) remain.
const expDecay = (a: number, b: number, k: number, dt: number) => (REDUCED_MOTION ? b : b + (a - b) * Math.exp(-k * dt))
const wave = (v: number) => (REDUCED_MOTION ? 0 : Math.sin(v))
const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v))

function hexAlpha(hex: string, a: number) {
  const h = hex.replace('#', '').slice(0, 6)
  return `#${h}${Math.round(clamp(a, 0, 1) * 255).toString(16).padStart(2, '0')}`
}

/** Black or white label text, whichever contrasts more (WCAG luminance). */
function contrastText(hex: string) {
  const h = hex.replace('#', '')
  const lin = (i: number) => {
    const c = parseInt(h.slice(i, i + 2), 16) / 255
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4)
  }
  const L = 0.2126 * lin(0) + 0.7152 * lin(2) + 0.0722 * lin(4)
  return (L + 0.05) / 0.05 > 1.05 / (L + 0.05) ? '#000000' : '#ffffff'
}

// lucide "mouse-pointer-2" (ISC) as a filled glyph, plus a thicker copy for the white outline.
const ARROW = 'M4.037 4.688a.495.495 0 0 1 .651-.651l16 6.5a.5.5 0 0 1-.063.947l-6.124 1.58a2 2 0 0 0-1.438 1.435l-1.579 6.126a.5.5 0 0 1-.947.063z'
const ARROW_FILL = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="${ARROW}" fill="#000"/></svg>`
const ARROW_OUTLINE = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="${ARROW}" fill="#000" stroke="#000" stroke-width="3" stroke-linejoin="round"/></svg>`

/** Point inside a box, biased toward the middle (average of two uniforms). */
function pickTarget(b: Box) {
  return { x: b.x + (b.w * (Math.random() + Math.random())) / 2, y: b.y + (b.h * (Math.random() + Math.random())) / 2 }
}

/** Move an agent's cursor toward a node; "kept" when it was already visible (flash waits for it). */
function moveToBounds(agent: string, b: Box, now: number): 'spawned' | 'kept' {
  const t = pickTarget(b)
  let c = store.cursors.get(agent)
  const faded = cursorFaded(agent, c, now)
  if (!c) {
    c = { profile: profileOf(agent), x: t.x, y: t.y, tx: t.x, ty: t.y, appearAt: now, lastActivity: now, wobble: 0, phase: Math.random() * Math.PI * 2 }
    store.cursors.set(agent, c)
  }
  c.profile = profileOf(agent)
  c.tx = t.x
  c.ty = t.y
  c.lastActivity = now
  if (faded) {
    c.x = t.x
    c.y = t.y
    c.appearAt = now
  }
  return faded ? 'spawned' : 'kept'
}

function cursorOpacity(c: CursorState, now: number, running: boolean) {
  const sa = now - c.appearAt
  const si = running ? 0 : now - c.lastActivity
  if (sa < FADE_IN) return easeOutCubic(sa / FADE_IN)
  if (si > FADE_DELAY) return Math.max(0, 1 - easeOutCubic((si - FADE_DELAY) / FADE_DUR))
  return 1
}

export function AgentLayer({ cam, w, h, version, chatOpen }: { cam: Cam; w: number; h: number; version: number; chatOpen: boolean }) {
  const [, setFrame] = useState(0)
  const lastFrame = useRef(performance.now())
  const boxes = useRef(new Map<string, Box>())
  const glowPhase = useRef(new Map<string, number>())
  const live = useMemo(() => tryCall<(Box & { placeholder?: boolean; pending?: boolean; clip?: Rect | null })[]>('live') ?? [], [version])

  store.viewCenter = { x: cam.x + w / 2 / cam.zoom, y: cam.y + h / 2 / cam.zoom }

  // Flashes follow their node: refresh boxes whenever the document (or preview) changes.
  useEffect(() => {
    const ids = [...new Set(store.flashes.map((f) => f.nodeId))]
    if (!ids.length) return
    for (const b of tryCall<Box[]>('boxes', { ids }) ?? []) boxes.current.set(b.id, b)
  }, [version])

  // 60 fps while anything animates; idle otherwise (under reduced motion only while a fade is due).
  useEffect(() => {
    const t = setInterval(() => {
      if (needsFrame(performance.now(), REDUCED_MOTION, live.length, lastFrame.current)) setFrame((f) => (f + 1) % 1_000_000)
    }, 16)
    return () => clearInterval(t)
  }, [live.length])

  const now = performance.now()
  const dt = clamp((now - lastFrame.current) / 1000, 0, 0.1)
  lastFrame.current = now

  // New agent activity → flashes + cursor travel to the touched nodes.
  if (store.activity.length) {
    const batch = store.activity.splice(0)
    const ids = [...new Set(batch.flatMap((a) => a.ids.slice(-40)))]
    const found = new Map((tryCall<Box[]>('boxes', { ids }) ?? []).map((b) => [b.id, b]))
    for (const a of batch) {
      const color = a.agent === 'external' ? PRESENCE : profileOf(a.agent).color
      for (const id of a.ids.slice(-40)) {
        const b = found.get(id)
        if (!b || b.w <= 1 || b.h <= 1) continue
        boxes.current.set(id, b)
        const kept = moveToBounds(a.agent, b, now) === 'kept'
        store.flashes.push({ nodeId: id, start: now + Math.random() * 50 + (kept ? 300 : 0), color, longHold: a.op === 'read' })
      }
    }
    if (store.flashes.length > 120) store.flashes.splice(0, store.flashes.length - 120)
  }

  const sx = (x: number) => (x - cam.x) * cam.zoom
  const sy = (y: number) => (y - cam.y) * cam.zoom
  const rgb = (hex: string) => `#${hex.replace('#', '').slice(0, 6)}`
  /** Overlay of a rotated node: GPUI cannot rotate divs, so `body` is SVG in the node's own document units,
   *  placed by the painted transform `m` and the camera, on an image over where the node is painted. */
  const spun = (r: Rect, m: M, body: string, testId: string, key: string) => {
    const p = painted({ ...r, m })
    const x = Math.floor(sx(p.x)) - 2
    const y = Math.floor(sy(p.y)) - 2
    const box = { x, y, w: Math.ceil(sx(p.x + p.w)) + 2 - x, h: Math.ceil(sy(p.y + p.h)) + 2 - y }
    const t = [m[0] * cam.zoom, m[1] * cam.zoom, m[2] * cam.zoom, m[3] * cam.zoom, (m[4] - cam.x) * cam.zoom, (m[5] - cam.y) * cam.zoom]
    return <SvgOverlay key={key} testId={testId} box={box} body={`<g transform="matrix(${t.join(' ')})">${body}</g>`} />
  }

  // ── flashes ───────────────────────────────────────────────────────────────
  const flashEls = []
  store.flashes = store.flashes.filter((f) => {
    const hold = f.longHold ? 1500 : 400
    return now - f.start < ATTACK + hold + RELEASE
  })
  for (let i = store.flashes.length - 1; i >= 0; i--) {
    const f = store.flashes[i]
    const b = boxes.current.get(f.nodeId)
    const g = now - f.start
    if (!b || g < 0) continue
    const hold = f.longHold ? 1500 : 400
    let alpha = 1
    let expand = 0
    // Reduced motion: the outline only fades in and out, with no grow-in and no scan band.
    if (g < ATTACK) {
      const o = REDUCED_MOTION ? easeOutCubic(g / ATTACK) : easeOutBack(g / ATTACK, 2)
      expand = REDUCED_MOTION ? 0 : 1 - o
      alpha = clamp(o, 0, 1)
    } else if (g > ATTACK + hold) {
      const e = easeOutCubic((g - ATTACK - hold) / RELEASE)
      expand = REDUCED_MOTION ? 0 : 0.1 * e
      alpha = 1 - e
    }
    const pad = 5 * cam.zoom + 30 * Math.max(0, expand)
    const pb = painted(b)
    const W = pb.w * cam.zoom + pad
    const H = pb.h * cam.zoom + pad
    const X = sx(pb.x) - pad / 2
    const Y = sy(pb.y) - pad / 2
    if (X > w || Y > h || X + W < 0 || Y + H < 0) continue
    const radius = Math.min(b.w, b.h) < 20 ? 0 : 3 * cam.zoom
    const scanMs = f.longHold ? clamp((b.h / 350) * 1000, 300, 2000) : 300
    const repeat = f.longHold ? 1.5 : 3
    const P = g > SCAN_DELAY && !REDUCED_MOTION ? clamp(((g - SCAN_DELAY) / scanMs) % repeat, 0, 1) : -1
    const ctr = -H / 2 + P * 2 * H
    if (turned(b.m)) {
      // The same outline and scan band in document units (a screen length / zoom), on the turned box.
      const d = pad / cam.zoom
      const [bx, by, bw, bh] = [b.x - d / 2, b.y - d / 2, b.w + d, b.h + d]
      const [lw, rx, c] = [2 / cam.zoom, radius / cam.zoom, rgb(f.color)]
      const top = by + bh * (2 * P - 1)
      const [y0, y1] = [Math.max(by, top), Math.min(by + bh, top + bh)]
      const band = P >= 0 && P < 1 && y1 > y0
        ? `<linearGradient id="g" gradientUnits="userSpaceOnUse" x1="0" y1="${top}" x2="0" y2="${top + bh}"><stop offset="0" stop-color="${c}" stop-opacity="0"/><stop offset="1" stop-color="${c}" stop-opacity="${0.2 * alpha}"/></linearGradient>`
          + `<g clip-path="url(#k)"><rect x="${bx}" y="${y0}" width="${bw}" height="${y1 - y0}" fill="url(#g)"/></g>`
        : ''
      flashEls.push(spun(
        { x: bx, y: by, w: bw, h: bh }, b.m!,
        `<clipPath id="k"><rect x="${bx}" y="${by}" width="${bw}" height="${bh}" rx="${rx}"/></clipPath>${band}`
          + `<rect x="${bx + lw / 2}" y="${by + lw / 2}" width="${bw - lw}" height="${bh - lw}" rx="${rx}" fill="none" stroke="${c}" stroke-opacity="${alpha}" stroke-width="${lw}"/>`,
        'agent-flash-turned', `f${i}-${f.nodeId}`,
      ))
      continue
    }
    flashEls.push(
      <div key={`f${i}-${f.nodeId}`} style={{ position: 'absolute', left: X, top: Y, width: W, height: H, borderRadius: radius, borderWidth: 2, borderColor: hexAlpha(f.color, alpha), overflow: 'hidden', pointerEvents: 'none' }}>
        {P >= 0 && P < 1 ? (
          <div style={{
            position: 'absolute', left: 0, top: ctr - H / 2, width: W, height: H,
            background: { type: 'linear-gradient', angle: 180, stops: [{ color: hexAlpha(f.color, 0), position: 0 }, { color: hexAlpha(f.color, 0.2 * alpha), position: 1 }] },
          }} />
        ) : null}
      </div>,
    )
  }

  // ── placeholder "generating" glow + pending-image shimmer ─────────────────
  const liveEl = (n: (typeof live)[number]) => {
    const pb = painted(n)
    const X = sx(pb.x)
    const Y = sy(pb.y)
    const W = pb.w * cam.zoom
    const H = pb.h * cam.zoom
    if (X > w || Y > h || X + W < 0 || Y + H < 0) return null
    const m = turned(n.m) ? n.m! : null
    // The node's own screen size: a turned node's painted bounds (W×H) are larger than its box.
    const [nw, nh] = [n.w * cam.zoom, n.h * cam.zoom]
    if (n.pending) {
      const band = 0.5 * Math.max(nw, nh)
      const p = REDUCED_MOTION ? 0.5 : (now % 1600) / 1600
      const pos = -band + p * (nw + 2 * band)
      if (m) {
        // Document units: the turned box with the band sliding along its own x axis.
        const [bd, o] = [band / cam.zoom, pos / cam.zoom]
        const half = (id: string, a: number, z: number) => `<linearGradient id="${id}"><stop offset="0" stop-color="#fff" stop-opacity="${a}"/><stop offset="1" stop-color="#fff" stop-opacity="${z}"/></linearGradient>`
        return spun(n, m, `<defs><clipPath id="k"><rect x="${n.x}" y="${n.y}" width="${n.w}" height="${n.h}"/></clipPath>${half('a', 0, 0.45)}${half('b', 0.45, 0)}</defs>`
          + `<g clip-path="url(#k)"><rect x="${n.x}" y="${n.y}" width="${n.w}" height="${n.h}" fill="#000" fill-opacity="0.25"/>`
          + `<rect x="${n.x + o}" y="${n.y}" width="${bd / 2}" height="${n.h}" fill="url(#a)"/><rect x="${n.x + o + bd / 2}" y="${n.y}" width="${bd / 2}" height="${n.h}" fill="url(#b)"/></g>`, 'agent-shimmer-turned', `p${n.id}`)
      }
      return (
        <div key={`p${n.id}`} style={{ position: 'absolute', left: X, top: Y, width: W, height: H, overflow: 'hidden', backgroundColor: '#00000040', pointerEvents: 'none' }}>
          <div style={{ position: 'absolute', left: pos, top: 0, width: band / 2, height: H, background: { type: 'linear-gradient', angle: 90, stops: [{ color: '#FFFFFF00', position: 0 }, { color: '#FFFFFF73', position: 1 }] } }} />
          <div style={{ position: 'absolute', left: pos + band / 2, top: 0, width: band / 2, height: H, background: { type: 'linear-gradient', angle: 90, stops: [{ color: '#FFFFFF73', position: 0 }, { color: '#FFFFFF00', position: 1 }] } }} />
        </div>
      )
    }
    let phase = glowPhase.current.get(n.id)
    if (phase === undefined) glowPhase.current.set(n.id, (phase = Math.random() * Math.PI * 2))
    const c = Math.max(nw, nh)
    const theta = phase + (REDUCED_MOTION ? 0 : now / 2000)
    const r = 0.4 * c
    const blobs = [1, -1].map((s) => ({ cx: nw / 2 + s * Math.cos(theta) * 0.5 * c, cy: nh / 2 + s * Math.sin(theta) * 0.5 * c }))
    // The turned glow: the same two blobs and outline in document units (a screen length / zoom), as SVG.
    const spin = m && (() => {
      const [k, c0] = [1 / cam.zoom, PRESENCE.slice(0, 7)]
      const rx = 5 * k
      const lw = 2 * k
      const glow = blobs.map((bl) => `<circle cx="${n.x + bl.cx * k}" cy="${n.y + bl.cy * k}" r="${2 * r * k}" fill="url(#b)"/>`).join('')
      return spun(n, m, `<defs><clipPath id="k"><rect x="${n.x}" y="${n.y}" width="${n.w}" height="${n.h}" rx="${rx}"/></clipPath>`
        + `<radialGradient id="b"><stop offset="0" stop-color="${c0}" stop-opacity="0.3"/><stop offset="1" stop-color="${c0}" stop-opacity="0"/></radialGradient></defs>`
        + `<g clip-path="url(#k)">${glow}</g>`
        + `<rect x="${n.x + lw / 2}" y="${n.y + lw / 2}" width="${n.w - lw}" height="${n.h - lw}" rx="${rx}" fill="none" stroke="${c0}" stroke-width="${lw}"/>`, 'agent-glow-turned', `g${n.id}`)
    })()
    return (
      <Fragment key={`g${n.id}`}>
        {spin}
        <div style={{ position: 'absolute', left: X, top: Y, width: W, height: H, pointerEvents: 'none' }}>
          {m ? null : (
            <>
              <div style={{ position: 'absolute', left: 0, top: 0, width: W, height: H, borderRadius: 5, overflow: 'hidden' }}>
                {blobs.map((bl, k) => (
                  <div key={k} style={{
                    position: 'absolute', left: bl.cx - r, top: bl.cy - r, width: 2 * r, height: 2 * r, borderRadius: r,
                    // The glow is 0.5-alpha blobs blurred with sigma 0.2·c; GPUI has no blur filter, so a faint core
                    // with a wide soft shadow gives the same glow.
                    backgroundColor: '#607dff10', boxShadow: { offsetX: 0, offsetY: 0, blurRadius: 0.5 * c, spreadRadius: 0, color: '#607dff38' },
                  }} />
                ))}
              </div>
              <div style={{ position: 'absolute', left: 0, top: 0, width: W, height: H, borderRadius: 5, borderWidth: 2, borderColor: PRESENCE }} />
            </>
          )}
          {n.parent ? null : (
            // A turned node keeps its pill upright, above where it is painted.
            <div style={{ position: 'absolute', left: 0, top: -18 + wave(now / 200) * 0.6, height: 16, maxWidth: Math.max(60, W), display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 5, overflow: 'hidden' }}>
              <svg source={ICONS.sparkles} style={{ width: 13, height: 13, color: PRESENCE }} />
              <text style={{ fontSize: 11, fontWeight: '500', color: PRESENCE, fontFamily: FONT, whiteSpace: 'nowrap' }}>{`Generating: ${n.name}`}</text>
            </div>
          )}
        </div>
      </Fragment>
    )
  }
  // Inside a clipping frame the overlay is cut to what the frame leaves visible: an outer box at that rect
  // holds a layer-sized box shifted back, so the overlay keeps its own coordinates.
  const liveEls = live.map((n) => {
    const el = liveEl(n)
    const c = n.clip
    if (!el || !c) return el
    const [cx, cy] = [sx(c.x), sy(c.y)]
    return (
      <div key={el.key} style={{ position: 'absolute', left: cx, top: cy, width: c.w * cam.zoom, height: c.h * cam.zoom, overflow: 'hidden', pointerEvents: 'none' }}>
        <div style={{ position: 'absolute', left: -cx, top: -cy, width: w, height: h, pointerEvents: 'none' }}>{el}</div>
      </div>
    )
  })

  // ── cursors ───────────────────────────────────────────────────────────────
  const cursorEls = []
  for (const [id, c] of store.cursors) {
    const running = store.running.has(id)
    if (!running && now - c.lastActivity > GONE) {
      store.cursors.delete(id)
      continue
    }
    c.x = expDecay(c.x, c.tx, DECAY, dt)
    c.y = expDecay(c.y, c.ty, DECAY, dt)
    c.wobble = REDUCED_MOTION ? 0 : expDecay(c.wobble, now - c.lastActivity > REST_DELAY ? 1 : 0, WOBBLE_DECAY, dt)
    const wx = Math.cos(now * 0.003 + c.phase) * WOBBLE_AMP * c.wobble
    const wy = Math.sin(now * 0.0023 + c.phase) * WOBBLE_AMP * c.wobble
    const px = sx(c.x) + wx
    const py = sy(c.y) + wy
    if (px < -200 || py < -60 || px > w + 20 || py > h + 20) continue
    const opacity = cursorOpacity(c, now, running)
    const color = c.profile.color
    cursorEls.push(
      <div key={`c${id}`} testId={`agent-cursor-${id}`} style={{ position: 'absolute', left: px - 4, top: py - 5, width: 200, height: 40, opacity, pointerEvents: 'none' }}>
        <svg source={ARROW_OUTLINE} style={{ position: 'absolute', left: 0, top: 1, width: 24, height: 24, color: '#00000059' }} />
        <svg source={ARROW_OUTLINE} style={{ position: 'absolute', left: 0, top: 0, width: 24, height: 24, color: '#FFFFFF' }} />
        <svg source={ARROW_FILL} style={{ position: 'absolute', left: 0, top: 0, width: 24, height: 24, color }} />
        <div style={{
          position: 'absolute', left: 18, top: 18, paddingTop: 2, paddingBottom: 2, paddingLeft: 5, paddingRight: 5, borderRadius: 2,
          backgroundColor: color, boxShadow: { offsetX: 0, offsetY: 1, blurRadius: 3.5, spreadRadius: 0, color: '#00000040' },
        }}>
          <text style={{ fontSize: 11, fontWeight: '500', color: contrastText(color), fontFamily: FONT, whiteSpace: 'nowrap' }}>{c.profile.name}</text>
        </div>
      </div>,
    )
  }

  // ── AI Pen: one pen per frame being drawn, tip on the newest point ────────
  for (const [id, pen] of store.pens) {
    pen.x = expDecay(pen.x, pen.tx, PEN_DECAY, dt)
    pen.y = expDecay(pen.y, pen.ty, PEN_DECAY, dt)
    const px = sx(pen.x)
    const py = sy(pen.y)
    if (px < -200 || py < -60 || px > w + 20 || py > h + 20) continue
    cursorEls.push(
      // lucide "pen-tool" has its nib tip at (2, 2) of 24.
      <div key={`pen${id}`} testId="ai-pen" style={{ position: 'absolute', left: px - 2, top: py - 2, width: 120, height: 44, opacity: easeOutCubic(clamp((now - pen.appearAt) / FADE_IN, 0, 1)), pointerEvents: 'none' }}>
        <svg source={ICONS.pen} style={{ position: 'absolute', left: 0, top: 1, width: 24, height: 24, color: '#00000080' }} />
        <svg source={ICONS.pen} style={{ position: 'absolute', left: 0, top: 0, width: 24, height: 24, color: PRESENCE }} />
        <div style={{
          position: 'absolute', left: 22, top: 22, paddingTop: 2, paddingBottom: 2, paddingLeft: 5, paddingRight: 5, borderRadius: 2,
          backgroundColor: PRESENCE, boxShadow: { offsetX: 0, offsetY: 1, blurRadius: 3.5, spreadRadius: 0, color: '#00000040' },
        }}>
          <text style={{ fontSize: 11, fontWeight: '500', color: contrastText(PRESENCE), fontFamily: FONT, whiteSpace: 'nowrap' }}>AI Pen</text>
        </div>
      </div>,
    )
  }

  // ── agent message pills (bottom center) ───────────────────────────────────
  const pills = [...store.pills.entries()].filter(([id, p]) => now - p.at < PILL_MS && !(chatOpen && !profileOf(id).parent))
  const pulse = REDUCED_MOTION ? 1 : 0.55 + 0.45 * Math.sin(now / 250)

  return (
    <div style={{ position: 'absolute', left: 0, top: 0, width: w, height: h, overflow: 'hidden', pointerEvents: 'none' }}>
      {liveEls}
      {flashEls}
      {cursorEls}
      {pills.length ? (
        <div style={{ position: 'absolute', left: 0, bottom: 16, width: w, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6 }}>
          {pills.map(([id, p]) => {
            const prof = profileOf(id)
            const msg = p.text.replace(/\s+/g, ' ').trim()
            return (
              <div key={id} style={{
                display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 8, maxWidth: Math.min(512, w - 32),
                paddingLeft: 12, paddingRight: 12, paddingTop: 6, paddingBottom: 6, borderRadius: 999,
                backgroundColor: C.panel, borderWidth: 1, borderColor: C.border,
                boxShadow: { offsetX: 0, offsetY: 4, blurRadius: 12, spreadRadius: 0, color: '#00000055' },
              }}>
                <div testId={`agent-pill-dot-${id}`} style={{ width: 8, height: 8, flexShrink: 0, borderRadius: 4, backgroundColor: prof.color, opacity: pulse }} />
                <text style={{ fontSize: 12, fontWeight: '600', color: prof.color, fontFamily: FONT, whiteSpace: 'nowrap' }}>{prof.name}</text>
                <text style={{ fontSize: 12, color: C.text, fontFamily: FONT, whiteSpace: 'nowrap', textOverflow: 'ellipsis', overflow: 'hidden', flexShrink: 1, minWidth: 0 }}>
                  {msg.length > 140 ? `${msg.slice(0, 140)}…` : msg}
                </text>
              </div>
            )
          })}
        </div>
      ) : null}
    </div>
  )
}
