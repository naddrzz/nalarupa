import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { ImgInstance } from '@gpuix/react'
import { renderPixels, tryCall, type Box } from './core'
import { fitCam } from './Canvas'
import { C, DPR } from './theme'
import { T } from './ui'

/** Presentation mode: each top-level frame is a slide, shown alone and fitted to the window. */
export function Present({ w, h, start, version, exit }: { w: number; h: number; start?: string; version: number; exit: () => void }) {
  const slides = useMemo(() => (tryCall<Box[]>('frames') ?? []).filter((f) => f.type === 'frame'), [version])
  const [i, setI] = useState(() => Math.max(0, slides.findIndex((s) => s.id === start)))
  const img = useRef<ImgInstance>(null)
  const slide = slides[Math.min(i, slides.length - 1)]

  useLayoutEffect(() => {
    if (!slide || w < 2 || h < 2) return
    const cam = fitCam(slide, w, h, 40, 8)
    const pw = Math.round(w * DPR)
    const ph = Math.round(h * DPR)
    const px = renderPixels(pw, ph, cam.x, cam.y, cam.zoom * DPR, 0x000000, slide.id)
    if (px) img.current?.setImagePixels(pw, ph, px)
  }, [slide?.id, w, h, version])

  const step = (d: number) => setI((v) => Math.min(slides.length - 1, Math.max(0, v + d)))
  return (
    <div
      testId="presentation"
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
        else if (k === 'end') setI(slides.length - 1)
      }}
      style={{ position: 'relative', width: w, height: h, backgroundColor: '#000000' }}
    >
      <img ref={img} objectFit="fill" style={{ position: 'absolute', left: 0, top: 0, width: w, height: h }} />
      <div style={{ position: 'absolute', right: 16, bottom: 12, paddingLeft: 8, paddingRight: 8, height: 22, borderRadius: 6, backgroundColor: '#00000099', display: 'flex', alignItems: 'center' }}>
        <T size={11} color={C.muted}>{slides.length ? `${i + 1} / ${slides.length} · Esc to exit` : 'No slides: add top-level frames'}</T>
      </div>
    </div>
  )
}
