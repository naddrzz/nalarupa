// Shader fills through the core (no window): `bun scripts/shader-check.ts`
// A `.glsl` file next to the document paints a node: @color / @default / @range uniforms and the node's own
// `uniforms`, @resolution, @sdf (the node's outline), opacity, a file edited on disk, an error hatch for a missing
// or broken file, and the same fill after save → open and undo. Pixels come from PNG exports.
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { call } from '../src/core'
import { decodePng, type Px } from './png'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`shader-check failed: ${msg}`)
}
// The core reads NALARUPA_DATA through libc, which `process.env` writes don't reach: rerun in a child
// with a fresh data folder so the documents saved here never touch the real ones.
if (!process.env.NALARUPA_CHECK_CHILD) {
  const data = mkdtempSync(join(tmpdir(), 'nalarupa-shader-'))
  const run = Bun.spawnSync([process.execPath, import.meta.path], { env: { ...process.env, NALARUPA_DATA: data, NALARUPA_CHECK_CHILD: '1' }, stdio: ['inherit', 'inherit', 'inherit'] })
  rmSync(data, { recursive: true, force: true })
  process.exit(run.exitCode ?? 1)
}
const data = process.env.NALARUPA_DATA!

const dir = mkdtempSync(join(data, 'doc-'))
const glsl = (name: string, src: string) => writeFileSync(join(dir, name), src)
const shot = (id: string) => decodePng(call('export', { ids: [id], format: 'png', path: dir, opts: { scale: 1 } })[0])
const near = (p: Px, want: Px, tol = 3) => p.every((v, i) => Math.abs(v - want[i]) <= tol)
const same = (p: Px, want: Px, msg: string) => assert(near(p, want), `${msg}: got ${p}, want ${want}`)
const insert = (node: object) => call('insert', { parent: 'document', node: { type: 'rectangle', x: 0, y: 0, width: 100, height: 40, ...node } }).id as string
const shader = (url: string, extra: object = {}) => ({ type: 'shader', url, ...extra })
/** Count the strongly red, mostly opaque pixels: the error hatch is 1-unit red stripes. */
const reds = (s: ReturnType<typeof shot>) => {
  let n = 0
  for (let y = 0; y < s.h; y++) for (let x = 0; x < s.w; x++) {
    const p = s.at(x, y)
    if (p[0] > 200 && p[1] < 60 && p[2] < 60 && p[3] > 100) n++
  }
  return n
}

call('init')
call('new', { template: 'empty' })
// Shader urls are relative to the document file, so it has to be saved first.
const docPath = join(dir, 'shaders.pen')
call('save', { path: docPath })

// A @color uniform takes its @default, then the value the fill's `uniforms` give it; a float uniform with @range.
glsl('tint.glsl', 'precision mediump float;\n/** @color @default #0000ff */\nuniform vec3 tint;\n/** @default 0.25 @range 0, 1 */\nuniform float level;\nvoid main() { gl_FragColor = vec4(tint * (level * 4.0), 1.0); }\n')
const tinted = insert({ name: 'Tinted', fill: shader('tint.glsl') })
same(shot(tinted).at(50, 20), [0, 0, 255, 255], 'a shader uses its @default values')
call('update', { ids: [tinted], props: { fill: shader('tint.glsl', { uniforms: { tint: '#00ff00' } }) } })
same(shot(tinted).at(50, 20), [0, 255, 0, 255], 'the fill\'s uniforms override the @color default')
call('update', { ids: [tinted], props: { fill: shader('tint.glsl', { uniforms: { tint: '#00ff00', level: 0.125 } }) } })
same(shot(tinted).at(50, 20), [0, 128, 0, 255], 'a number uniform scales the colour (0.125 * 4 = half)')
call('undo')
same(shot(tinted).at(50, 20), [0, 255, 0, 255], 'undo restores the previous uniforms')
call('undo')
same(shot(tinted).at(50, 20), [0, 0, 255, 255], 'undo again is back at the defaults')

// @resolution is the node size and gl_FragCoord is in node units with the origin bottom-left.
glsl('coord.glsl', 'precision mediump float;\n/** @resolution */\nuniform vec2 res;\nvoid main() { vec2 p = gl_FragCoord.xy / res; gl_FragColor = vec4(p.x, p.y, 0.0, 1.0); }\n')
const coord = shot(insert({ name: 'Coord', fill: shader('coord.glsl') }))
assert(coord.w === 100 && coord.h === 40, `the export is the node size: ${coord.w}x${coord.h}`)
const [topLeft, topRight, bottomLeft] = [coord.at(5, 2), coord.at(95, 2), coord.at(5, 37)]
assert(topLeft[0] < 40 && topRight[0] > 215, `red grows with x across the width: ${topLeft[0]} -> ${topRight[0]}`)
assert(topLeft[1] > 215 && bottomLeft[1] < 40, `green is high at the top, because the origin is bottom-left: ${topLeft[1]} vs ${bottomLeft[1]}`)

// @sdf is positive inside the node's own outline: an ellipse keeps its corners clear, a rectangle does not.
glsl('inside.glsl', 'precision mediump float;\n/** @resolution */\nuniform vec2 res;\n/** @sdf */\nuniform sampler2D sdf;\nvoid main() { float d = texture2D(sdf, gl_FragCoord.xy / res).r; gl_FragColor = d > 0.0 ? vec4(0.0, 0.0, 1.0, 1.0) : vec4(0.0); }\n')
const disc = shot(insert({ type: 'ellipse', name: 'Disc', fill: shader('inside.glsl') }))
same(disc.at(50, 20), [0, 0, 255, 255], 'inside the ellipse the shader paints')
assert(disc.at(2, 2)[3] === 0 && disc.at(97, 37)[3] === 0, 'the ellipse corners stay transparent')
const slab = shot(insert({ name: 'Slab', fill: shader('inside.glsl') }))
same(slab.at(2, 2), [0, 0, 255, 255], 'a rectangle is painted into its corners')

// The fill's opacity applies to the shader's colour.
glsl('red.glsl', 'precision mediump float;\nvoid main() { gl_FragColor = vec4(1.0, 0.0, 0.0, 1.0); }\n')
const faded = shot(insert({ name: 'Faded', fill: shader('red.glsl', { opacity: 0.5 }) })).at(50, 20)
assert(faded[0] > 200 && faded[3] > 100 && faded[3] < 155, `opacity 0.5 halves the alpha: ${faded}`)

// A shader file edited on disk shows its new code on the next render; a missing or broken file draws red stripes, not a crash.
const live = insert({ name: 'Live', fill: shader('red.glsl') })
same(shot(live).at(50, 20), [255, 0, 0, 255], 'red shader')
glsl('red.glsl', 'precision mediump float;\nvoid main() { gl_FragColor = vec4(1.0, 1.0, 0.0, 1.0); }\n')
same(shot(live).at(50, 20), [255, 255, 0, 255], 'the edited file is picked up')
assert(reds(shot(live)) === 0, 'a working shader has no hatch')
glsl('broken.glsl', 'void main() { nonsense }')
const broken = shot(insert({ name: 'Broken', fill: shader('broken.glsl') }))
const lost = shot(insert({ name: 'Lost', fill: shader('missing.glsl') }))
assert(reds(broken) > 300 && reds(broken) < 1800, `a file that does not compile draws the red hatch: ${reds(broken)} red pixels`)
assert(reds(lost) > 300 && reds(lost) < 1800, `a missing file draws the red hatch: ${reds(lost)} red pixels`)

// SVG export embeds the rendered shader; the fill survives save → open.
const svgPath = call('export', { ids: [live], format: 'svg', path: dir })[0]
assert(existsSync(svgPath) && readFileSync(svgPath, 'utf8').includes('data:image/png;base64,'), 'svg export embeds the shader as an image')
call('save', { path: docPath })
call('new', { template: 'empty' })
call('open', { path: docPath })
assert(call('node', { id: tinted }).node.fill.url === 'tint.glsl', 'the shader url is kept after reopening')
same(shot(tinted).at(50, 20), [0, 0, 255, 255], 'the reopened shader renders like before')
console.log('shader-check ok')
