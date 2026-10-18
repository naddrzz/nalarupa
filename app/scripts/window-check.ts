// Window placement and frame against the real display: `bun run window-check`
// The window must fit the monitor's work area, its native frame (title bar, border) must be on screen,
// and the frame must carry the Nalarupa icon and colours (theme.ts decorateWindow). Windows only.
// Pass a path to check a built exe instead of the dev run: `bun run window-check dist/nalarupa.exe`.
import { dlopen, FFIType, ptr } from 'bun:ffi'
import { join } from 'node:path'
import { launch } from '@gpuix/react/automation'
import { C } from '../src/theme'

if (process.platform !== 'win32') {
  console.log('window-check: Windows only')
  process.exit(0)
}
const u32 = dlopen('user32.dll', {
  FindWindowExW: { args: [FFIType.ptr, FFIType.ptr, FFIType.ptr, FFIType.ptr], returns: FFIType.ptr },
  GetWindowThreadProcessId: { args: [FFIType.ptr, FFIType.ptr], returns: FFIType.u32 },
  IsWindowVisible: { args: [FFIType.ptr], returns: FFIType.i32 },
  GetWindowRect: { args: [FFIType.ptr, FFIType.ptr], returns: FFIType.i32 },
  GetClientRect: { args: [FFIType.ptr, FFIType.ptr], returns: FFIType.i32 },
  ClientToScreen: { args: [FFIType.ptr, FFIType.ptr], returns: FFIType.i32 },
  MonitorFromWindow: { args: [FFIType.ptr, FFIType.u32], returns: FFIType.ptr },
  GetMonitorInfoW: { args: [FFIType.ptr, FFIType.ptr], returns: FFIType.i32 },
  SendMessageW: { args: [FFIType.ptr, FFIType.u32, FFIType.u64, FFIType.ptr], returns: FFIType.ptr },
}).symbols
const dwm = dlopen('dwmapi.dll', { DwmGetWindowAttribute: { args: [FFIType.ptr, FFIType.u32, FFIType.ptr, FFIType.u32], returns: FFIType.i32 } }).symbols
const rect = (r: Int32Array) => ({ x: r[0], y: r[1], w: r[2] - r[0], h: r[3] - r[1] })

const exe = process.argv[2]
const app = await launch({ command: exe ?? 'bun', args: exe ? [] : ['app.tsx'], cwd: join(import.meta.dir, '..'), env: { ...process.env, GPUIX_BACKGROUND: '1' } })
const failures: string[] = []
try {
  const { pid } = (await app.call('initialize' as any, { protocolVersion: 1, client: 'window-check' } as any)) as { pid: number }
  await app.getByTestId('template-slides').waitFor({ timeoutMs: 30000 })
  const owner = new Uint32Array(1)
  let hwnd = u32.FindWindowExW(null, null, null, null)
  for (; hwnd; hwnd = u32.FindWindowExW(null, hwnd, null, null)) {
    u32.GetWindowThreadProcessId(hwnd, ptr(owner))
    if (owner[0] === pid && u32.IsWindowVisible(hwnd)) break
  }
  if (!hwnd) throw new Error('no window for the app')
  // Measure after decorateWindow has run: its last step sets the caption colour, after the icon and the move into
  // the work area. Poll for that (bounded) rather than guess a delay.
  const captionWanted = parseInt(C.panel.slice(5, 7) + C.panel.slice(3, 5) + C.panel.slice(1, 3), 16)
  const captionNow = new Uint32Array(1)
  const decorated = () => dwm.DwmGetWindowAttribute(hwnd, 35, ptr(captionNow), 4) === 0 && captionNow[0] === captionWanted
  for (let i = 0; i < 50 && !decorated(); i++) await new Promise((r) => setTimeout(r, 100))
  const wr = new Int32Array(4), cr = new Int32Array(4), origin = new Int32Array(2)
  u32.GetWindowRect(hwnd, ptr(wr))
  u32.GetClientRect(hwnd, ptr(cr))
  u32.ClientToScreen(hwnd, ptr(origin))
  // MONITORINFO: cbSize, rcMonitor[4], rcWork[4], dwFlags
  const mi = new Int32Array(10)
  mi[0] = 40
  u32.GetMonitorInfoW(u32.MonitorFromWindow(hwnd, 2), ptr(mi))
  const win = rect(wr), work = rect(mi.subarray(5, 9))
  const client = { x: origin[0], y: origin[1], w: cr[2], h: cr[3] }
  console.log('work area', work, '\nwindow', win, '\nclient', client, `\nnative frame: top ${client.y - win.y}px, left ${client.x - win.x}px`)
  if (win.x < work.x || win.y < work.y || win.x + win.w > work.x + work.w || win.y + win.h > work.y + work.h) failures.push('the window does not fit the work area: part of it (its title bar) is off screen')
  if (client.y - win.y < 20) failures.push('no native title bar above the client area')
  // WM_GETICON: ICON_SMALL (title bar), ICON_BIG (taskbar)
  if (!u32.SendMessageW(hwnd, 0x7f, 0, null) || !u32.SendMessageW(hwnd, 0x7f, 1, null)) failures.push('the window has no Nalarupa icon')
  const caption = new Uint32Array(1)
  if (dwm.DwmGetWindowAttribute(hwnd, 35, ptr(caption), 4) === 0) {
    const want = captionWanted
    console.log(`caption colour 0x${caption[0].toString(16)} (want 0x${want.toString(16)})`)
    if (caption[0] !== want) failures.push('the title bar is not in the app colour')
  }
} finally {
  await app.close()
}
if (failures.length) {
  console.log(failures.join('\n'))
  process.exit(1)
}
console.log('window-check ok')
