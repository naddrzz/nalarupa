import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { ImgInstance } from '@gpuix/react'
import { renderPixels, tryCall, type Box } from './core'
import { fitCam } from './Canvas'
import { C, DPR } from './theme'
import { T } from './ui'

/** Presentation mode: each top-level frame is a slide, shown alone and fitted to the window. */
export function Present({ w, h, start, version, animating, exit }: { w: number; h: number; start?: string; version: number; animating?: boolean; exit: () => void }) {
  const slides = useMemo(() => (tryCall<Box[]>('frames') ?? []).filter((f) => f.type === 'frame'), [version])
  // A selected layer inside a slide starts that slide: climb to its top-level ancestor.
  const [i, setI] = useState(() => {
    let id = start
    for (let p = id; p && p !== 'document'; p = tryCall<Box[]>('boxes', { ids: [p] })?.[0]?.parent ?? 'document') id = p
    return Math.max(0, slides.findIndex((s) => s.id === id))
  })
  const img = useRef<ImgInstance>(null)
  // Clamped both ways: a slide removed while presenting must not leave `i` past the end, and an empty
  // deck must not leave it below 0 once a slide is added.
  const at = Math.max(0, Math.min(i, slides.length - 1))
  const slide = slides[at]

  const draw = () => {
    if (!slide || w < 2 || h < 2) return
    const cam = fitCam(slide, w, h, 40, 8)
    const pw = Math.round(w * DPR)
    const ph = Math.round(h * DPR)
    const px = renderPixels(pw, ph, cam.x, cam.y, cam.zoom * DPR, 0x000000, slide.id)
    if (px) img.current?.setImagePixels(pw, ph, px)
  }
  // `animating` also redraws once it ends: the settled nodes move back into the cached layer without a version bump.
  useLayoutEffect(draw, [slide?.id, w, h, version, animating])
  // Agent inserts glide in frame by frame, as on the canvas.
  useEffect(() => {
    if (!animating) return
    const t = setInterval(draw, 16)
    return () => clearInterval(t)
  }, [animating, slide?.id, w, h, version])

  const last = Math.max(0, slides.length - 1)
  const step = (d: number) => setI((v) => Math.min(last, Math.max(0, Math.min(v, last) + d)))
  return (
    <div
      testId="presentation"
      // The focused surface carries the slide, so a screen reader has something to announce.
      role="group"
      aria-label={slide ? `Slide ${at + 1} of ${slides.length}: ${slide.name}` : 'No slides'}
      aria-description="Right arrow or Space for the next slide, Left arrow for the previous one, Escape to exit"
      tabIndex={0}
      autoFocus
      onClick={() => step(1)}
      onAuxClick={() => step(-1)}
      onKeyDown={(e) => {
        const k = (e.key ?? '').toLowerCase()
        if (k === 'escape') exit()
        else if (['right', 'down', 'space', 'enter', 'pagedown'].includes(k)) step(1)
        else if (['left', 'up', 'backspace', 'pageup'].includes(k)) step(-1)
        else if (k === 'home') setI(0)
        else if (k === 'end') setI(last)
      }}
      style={{ position: 'relative', width: w, height: h, backgroundColor: '#000000' }}
    >
      {/* Not mounted without a slide, so deleting the last one does not leave its bitmap on screen. */}
      {slide ? <img ref={img} objectFit="fill" style={{ position: 'absolute', left: 0, top: 0, width: w, height: h }} /> : null}
      <div style={{ position: 'absolute', right: 16, bottom: 12, paddingLeft: 8, paddingRight: 8, height: 22, borderRadius: 6, backgroundColor: '#00000099', display: 'flex', alignItems: 'center' }}>
        <T size={11} color={C.muted}>{slides.length ? `${at + 1} / ${slides.length} · Esc to exit` : 'No slides: add top-level frames · Esc to exit'}</T>
      </div>
    </div>
  )
}
