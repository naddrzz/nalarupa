// Build a self-contained dist/: `bun scripts/package.ts`
//   nalarupa(.exe)            Bun-compiled GPUIX front-end
//   nalarupa_core.(dll|so|dylib)   Rust core (cargo build --release in ../core first)
//   gpuix-native.<target>.node     GPUI renderer addon
import { copyFileSync, existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { suffix } from 'bun:ffi'

const root = join(import.meta.dir, '..')
const dist = join(root, 'dist')
mkdirSync(dist, { recursive: true })

const exe = join(dist, process.platform === 'win32' ? 'nalarupa.exe' : 'nalarupa')
// Explorer/taskbar icon and the name Task Manager shows (assets/brand: regenerate with core's `icon` example).
const ico = join(root, 'assets', 'brand', 'nalarupa.ico')
const win = process.platform === 'win32' ? [`--windows-icon=${ico}`, '--windows-title=Nalarupa', '--windows-description=Nalarupa'] : []
const build = Bun.spawnSync(['bun', 'build', '--compile', ...win, 'main.ts', '--outfile', exe], { cwd: root, stdout: 'inherit', stderr: 'inherit' })
if (build.exitCode !== 0) throw new Error('bun build --compile failed')
if (win.length) useOwnIconGroup(exe)

/** Bun 1.3.14's --windows-icon adds our icon group as id 0 but keeps Bun's own group (named IDI_MYICON),
 *  which now points at our 16 px image; Explorer shows the first group (named ones sort first), so the
 *  file icon came out as a blurry upscaled 16 px. Point the named group at ours: a 4-byte in-place patch
 *  of the PE resource directory. */
function useOwnIconGroup(path: string) {
  const buf = readFileSync(path)
  const dv = new DataView(buf.buffer, buf.byteOffset, buf.byteLength)
  const pe = dv.getUint32(0x3c, true)
  const opt = pe + 24
  const sections = opt + dv.getUint16(pe + 20, true)
  const rva = dv.getUint32(opt + (dv.getUint16(opt, true) === 0x20b ? 112 : 96) + 16, true)
  let base = -1
  for (let i = 0, s = sections; i < dv.getUint16(pe + 6, true); i++, s += 40) {
    const va = dv.getUint32(s + 12, true)
    if (rva >= va && rva < va + dv.getUint32(s + 8, true)) base = rva - va + dv.getUint32(s + 20, true)
  }
  // An IMAGE_RESOURCE_DIRECTORY at `dir`: named entries, then id entries, 8 bytes each (name, offset).
  const entries = (dir: number) => Array.from({ length: dv.getUint16(dir + 12, true) + dv.getUint16(dir + 14, true) }, (_, i) => dir + 16 + i * 8)
  const groups = base < 0 ? undefined : entries(base).find((e) => dv.getUint32(e, true) === 14) // RT_GROUP_ICON
  if (groups === undefined) throw new Error('no icon group in the compiled exe')
  const list = entries(base + (dv.getUint32(groups + 4, true) & 0x7fffffff))
  const ours = list.find((e) => dv.getUint32(e, true) === 0)
  if (ours === undefined) throw new Error('--windows-icon left no icon group 0')
  for (const e of list) if (dv.getUint32(e, true) & 0x80000000) dv.setUint32(e + 4, dv.getUint32(ours + 4, true), true)
  writeFileSync(path, buf)
}

const core = `${process.platform === 'win32' ? '' : 'lib'}nalarupa_core.${suffix}`
const coreSrc = join(root, '..', 'core', 'target', 'release', core)
if (!existsSync(coreSrc)) throw new Error(`missing ${coreSrc}: run cargo build --release in core/`)
copyFileSync(coreSrc, join(dist, core))

const gpuix = join(root, 'node_modules', '@gpuix')
const pkg = readdirSync(gpuix).find((d) => d.startsWith('native-') && d !== 'native')
const addon = pkg && readdirSync(join(gpuix, pkg)).find((f) => f.endsWith('.node'))
if (!pkg || !addon) throw new Error('GPUIX native addon not installed (bun install)')
copyFileSync(join(gpuix, pkg, addon), join(dist, addon))
// The window's title-bar/taskbar icon is loaded from this file at runtime (theme.ts decorateWindow).
if (win.length) copyFileSync(ico, join(dist, 'nalarupa.ico'))

console.log(`dist ready: ${readdirSync(dist).join(', ')}`)
