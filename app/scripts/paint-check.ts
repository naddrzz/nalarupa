// Fills and strokes through the core (no window): `bun scripts/paint-check.ts`
// Image fills in each mode, mesh / linear / radial / angular gradients, stacked fills with a blend mode and a
// disabled layer, and stroke alignment, read back as pixels from PNG exports.
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { call } from '../src/core'
import { decodePng, encodePng, type Px } from './png'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`paint-check failed: ${msg}`)
}
// The core reads NALARUPA_DATA through libc, which `process.env` writes don't reach: rerun in a child
// with a fresh data folder so the documents saved here never touch the real ones.
if (!process.env.NALARUPA_CHECK_CHILD) {
  const data = mkdtempSync(join(tmpdir(), 'nalarupa-paint-'))
  let code = 1
  try {
    code = Bun.spawnSync([process.execPath, import.meta.path], { env: { ...process.env, NALARUPA_DATA: data, NALARUPA_CHECK_CHILD: '1' }, stdio: ['inherit', 'inherit', 'inherit'] }).exitCode ?? 1
  } finally {
    rmSync(data, { recursive: true, force: true })
  }
  process.exit(code)
}
const data = process.env.NALARUPA_DATA!
const dir = mkdtempSync(join(data, 'doc-'))

const RED: Px = [255, 0, 0, 255]
const GREEN: Px = [0, 255, 0, 255]
const BLUE: Px = [0, 0, 255, 255]
const WHITE: Px = [255, 255, 255, 255]
const CLEAR: Px = [0, 0, 0, 0]
const shot = (id: string) => decodePng(call('export', { ids: [id], format: 'png', path: dir, opts: { scale: 1 } })[0])
const same = (p: Px, want: Px, msg: string, tol = 4) => assert(p.every((v, i) => Math.abs(v - want[i]) <= tol), `${msg}: got ${p}, want ${want}`)
const square = (node: object) => call('insert', { parent: 'document', node: { type: 'rectangle', x: 0, y: 0, width: 100, height: 100, ...node } }).id as string
const stops = (a: string, b: string) => [{ color: a, position: 0 }, { color: b, position: 1 }]

call('init')
call('new', { template: 'empty' })
call('save', { path: join(dir, 'paint.pen') })

// A 40x20 picture, one colour per quadrant, in a 100x100 node: stretch and fill cover the box, fit letterboxes it.
writeFileSync(join(dir, 'quad.png'), encodePng(40, 20, (x, y) => (y < 10 ? (x < 20 ? RED : GREEN) : x < 20 ? BLUE : WHITE)))
const picture = (mode?: string) => shot(square({ name: `Picture ${mode ?? 'default'}`, fill: { type: 'image', url: 'quad.png', mode } }))
for (const mode of ['stretch', 'fill', undefined]) {
  const s = picture(mode)
  same(s.at(5, 5), RED, `${mode} top-left`)
  same(s.at(95, 5), GREEN, `${mode} top-right`)
  same(s.at(5, 95), BLUE, `${mode} bottom-left`)
  same(s.at(95, 95), WHITE, `${mode} bottom-right`)
}
const fit = picture('fit')
same(fit.at(5, 30), RED, 'fit keeps the whole picture: top-left quadrant')
same(fit.at(95, 70), WHITE, 'fit keeps the whole picture: bottom-right quadrant')
same(fit.at(50, 5), CLEAR, 'fit leaves the strip above the picture empty')
same(fit.at(50, 95), CLEAR, 'fit leaves the strip below the picture empty')
// In a wide node, fill crops the picture's top and bottom instead (zoomed on the middle rows).
const wide = shot(square({ name: 'Wide', width: 200, height: 40, fill: { type: 'image', url: 'quad.png', mode: 'fill' } }))
same(wide.at(5, 5), RED, 'fill in a wide node: top-left')
same(wide.at(195, 35), WHITE, 'fill in a wide node: bottom-right')

// A mesh gradient hits its corner colours and blends them in the middle.
const mesh = shot(square({ name: 'Mesh', fill: { type: 'mesh_gradient', columns: 2, rows: 2, colors: ['#ff0000', '#00ff00', '#0000ff', '#ffffff'], points: [[0, 0], [1, 0], [0, 1], [1, 1]] } }))
same(mesh.at(1, 1), RED, 'mesh top-left corner', 6)
same(mesh.at(98, 1), GREEN, 'mesh top-right corner', 6)
same(mesh.at(1, 98), BLUE, 'mesh bottom-left corner', 6)
same(mesh.at(98, 98), WHITE, 'mesh bottom-right corner', 6)
const mid = mesh.at(50, 50)
assert(mid.slice(0, 3).every((v) => v > 90 && v < 170), `the mesh centre is a blend of the four colours: ${mid}`)

// Linear gradients: rotation 0 points up (position 0 at the bottom), 90 turns counter-clockwise (position 0 on the right).
const linear = (rotation: number) => shot(square({ name: `Linear ${rotation}`, fill: { type: 'gradient', gradientType: 'linear', rotation, colors: stops('#ff0000', '#0000ff') } }))
const up = linear(0)
same(up.at(50, 97), RED, 'rotation 0 starts at the bottom', 12)
same(up.at(50, 2), BLUE, 'rotation 0 ends at the top', 12)
same(up.at(50, 50), [128, 0, 127, 255], 'rotation 0 is half way in the middle', 6)
same(up.at(2, 50), up.at(97, 50), 'a vertical gradient is constant along a row')
const left = linear(90)
same(left.at(97, 50), RED, 'rotation 90 starts on the right', 12)
same(left.at(2, 50), BLUE, 'rotation 90 ends on the left', 12)
const down = linear(180)
same(down.at(50, 2), RED, 'rotation 180 starts at the top', 12)

// Radial: the first stop at the centre, the last at the edge and beyond; angular: the first stop at the top, the seam of a full turn.
const radial = shot(square({ name: 'Radial', fill: { type: 'gradient', gradientType: 'radial', colors: stops('#ff0000', '#0000ff') } }))
same(radial.at(50, 50), RED, 'radial centre', 12)
same(radial.at(98, 98), BLUE, 'radial corner is past the last stop')
const angular = shot(square({ name: 'Angular', fill: { type: 'gradient', gradientType: 'angular', colors: stops('#ff0000', '#0000ff') } }))
same(angular.at(50, 2), RED, 'angular starts at the top', 12)
same(angular.at(50, 98), [128, 0, 127, 255], 'angular is half way at the bottom', 8)
assert(angular.at(98, 50)[0] !== angular.at(2, 50)[0], 'angular sweeps around: the right and left sides differ')

// A gradient fill's opacity makes it translucent over what is below.
const ghost = shot(square({ name: 'Ghost', fill: { type: 'gradient', gradientType: 'linear', opacity: 0.5, colors: stops('#ff0000', '#ff0000') } })).at(50, 50)
assert(ghost[0] > 200 && ghost[3] > 100 && ghost[3] < 155, `fill opacity 0.5 halves the alpha: ${ghost}`)

// Fills stack bottom to top; a disabled layer is skipped; blendMode multiplies with what is below.
same(shot(square({ name: 'Stack', fill: ['#ff0000', '#0000ff'] })).at(50, 50), BLUE, 'the last fill is on top')
same(shot(square({ name: 'Skip', fill: [{ type: 'color', color: '#00ff00' }, { type: 'color', color: '#ff0000', enabled: false }] })).at(50, 50), GREEN, 'a disabled fill is not painted')
same(shot(square({ name: 'Multiply', fill: ['#ff0000', { type: 'color', color: '#0000ff', blendMode: 'multiply' }] })).at(50, 50), [0, 0, 0, 255], 'red multiplied by blue is black')
same(shot(square({ name: 'Screen', fill: ['#ff0000', { type: 'color', color: '#0000ff', blendMode: 'screen' }] })).at(50, 50), [255, 0, 255, 255], 'red screened with blue is magenta')

// Stroke alignment: inner stays in the box, outer grows the export by the stroke width on every side.
const inner = shot(square({ name: 'Inner', fill: '#ffffff', stroke: '#ff0000', strokeWidth: 10, strokeAlignment: 'inner' }))
assert(inner.w === 100 && inner.h === 100, `an inner stroke keeps the export at the box size: ${inner.w}x${inner.h}`)
same(inner.at(4, 50), RED, 'inner stroke band')
same(inner.at(15, 50), WHITE, 'inner stroke leaves the middle to the fill')
const outer = shot(square({ name: 'Outer', fill: '#ffffff', stroke: '#ff0000', strokeWidth: 10, strokeAlignment: 'outer' }))
assert(outer.w === 120 && outer.h === 120, `an outer stroke grows the export to 120x120: ${outer.w}x${outer.h}`)
same(outer.at(4, 60), RED, 'outer stroke band outside the box')
same(outer.at(15, 60), WHITE, 'outer stroke leaves the box to the fill')
// Per-side widths: only the sides given get a stroke.
const sides = shot(square({ name: 'Sides', fill: '#ffffff', stroke: '#ff0000', strokeWidth: { bottom: 10 }, strokeAlignment: 'inner' }))
same(sides.at(50, 95), RED, 'the bottom side is stroked')
same(sides.at(50, 4), WHITE, 'the top side is not')
console.log('paint-check ok')
