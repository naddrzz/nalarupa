import { dlopen, FFIType, ptr } from 'bun:ffi'
import { existsSync } from 'node:fs'
import { dirname, join } from 'node:path'

// Text tokens are measured against every surface they sit on (WCAG 1.4.3 / 1.4.11):
// ghost ≥ 4.5:1 on raised, field ≥ 3:1 on panel and panel2, white on selectSolid 5.2:1.
export const C = {
  bg: '#111113',
  panel: '#17171A',
  panel2: '#1E1E22',
  raised: '#26262B',
  border: '#2B2B31',
  text: '#ECECEF',
  muted: '#A1A1AA',
  ghost: '#8C8C96',
  accent: '#FF6A3D',
  accentSoft: '#FF6A3D22',
  select: '#3B82F6',
  /** Filled selection chips that carry white text (the size label). */
  selectSolid: '#2563EB',
  /** Keyboard focus ring. */
  focus: '#60A5FA',
  /** Text-field boundary. */
  field: '#686870',
  /** The window's outer edge (the native frame's border on Windows 11). */
  edge: '#45454E',
  component: '#A78BFA',
  danger: '#F87171',
  ok: '#4ADE80',
}

export const CANVAS_BG = 0x1c1c1f

/** Device pixel ratio for canvas bitmaps (GPUIX takes bitmap pixels, layout takes logical px). */
function detectDpr(): number {
  const env = Number(process.env.NALARUPA_DPR)
  if (env > 0) return env
  if (process.platform === 'darwin') return 2
  if (process.platform === 'win32') {
    try {
      const out = Bun.spawnSync(['reg', 'query', String.raw`HKCU\Control Panel\Desktop\WindowMetrics`, '/v', 'AppliedDPI']).stdout.toString()
      const m = out.match(/0x([0-9a-f]+)/i)
      if (m) return parseInt(m[1], 16) / 96
    } catch {}
  }
  return 1
}
export const DPR = detectDpr()

/** The OS asks for less motion: Windows "Animation effects" off (SPI_GETCLIENTAREAANIMATION), macOS
 *  "Reduce motion". GPUIX has no `prefers-reduced-motion`, so read it once at start. */
function detectReducedMotion(): boolean {
  const env = process.env.NALARUPA_REDUCED_MOTION
  if (env) return env === '1'
  try {
    if (process.platform === 'win32') {
      const { symbols } = dlopen('user32.dll', { SystemParametersInfoW: { args: [FFIType.u32, FFIType.u32, FFIType.ptr, FFIType.u32], returns: FFIType.i32 } })
      const on = new Int32Array(1)
      if (symbols.SystemParametersInfoW(0x1042, 0, ptr(on), 0)) return on[0] === 0
    }
    if (process.platform === 'darwin') return Bun.spawnSync(['defaults', 'read', 'com.apple.universalaccess', 'reduceMotion']).stdout.toString().trim() === '1'
  } catch {}
  return false
}
export const REDUCED_MOTION = detectReducedMotion()

/** Chat indicators (animated ellipsis, running-tool dot) are pure functions of the clock; the panel
 *  re-renders every `INDICATOR_TICK_MS` while one is live, so an ellipsis step lasts 400 ± one tick
 *  and the pulse is sampled ~25 times per period instead of strobing. */
export const INDICATOR_TICK_MS = 50
export const ellipsisAt = (now: number) => '.'.repeat(1 + (Math.floor(now / 400) % 3))
/** The dots `ellipsisAt` leaves out, painted transparent so a label after it keeps its x. */
export const ellipsisPadAt = (now: number) => '.'.repeat(3 - ellipsisAt(now).length)
export const pulseAt =(now: number) => 0.5 + 0.5 * Math.sin(now / 200)
/** The name above a canvas frame: one line that ends in `…` where its label box is too narrow. */
export const FRAME_LABEL_TEXT = { minWidth: 0, whiteSpace: 'nowrap', textOverflow: 'ellipsis', overflow: 'hidden' } as const

/** The primary monitor's work area (screen minus taskbar) in logical px, or null where unknown. */
function workArea(): { w: number; h: number } | null {
  if (process.platform !== 'win32') return null
  try {
    const { symbols: u } = dlopen('user32.dll', {
      SystemParametersInfoW: { args: [FFIType.u32, FFIType.u32, FFIType.ptr, FFIType.u32], returns: FFIType.i32 },
      GetThreadDpiAwarenessContext: { args: [], returns: FFIType.ptr },
      GetAwarenessFromDpiAwarenessContext: { args: [FFIType.ptr], returns: FFIType.i32 },
    })
    const r = new Int32Array(4)
    if (!u.SystemParametersInfoW(0x30, 0, ptr(r), 0)) return null // SPI_GETWORKAREA
    // A DPI-aware thread gets physical pixels; an unaware one already gets logical ones.
    const s = u.GetAwarenessFromDpiAwarenessContext(u.GetThreadDpiAwarenessContext()) !== 0 ? DPR : 1
    return { w: (r[2] - r[0]) / s, h: (r[3] - r[1]) / s }
  } catch {
    return null
  }
}

/** Window size: 1440×900 where it fits, else the work area less a margin and the native frame (title
 *  bar ≈ 32 px, borders ≈ 8 px), so the title bar never lands above the screen. GPUI centres it on the
 *  display; decorateWindow moves it back into the work area when the taskbar would cover part of it. */
export function windowSize() {
  const want = { width: 1440, height: 900, minWidth: 1100, minHeight: 680 }
  const area = workArea()
  if (!area) return want
  const width = Math.floor(Math.min(want.width, area.w * 0.94 - 16))
  const height = Math.floor(Math.min(want.height, area.h * 0.94 - 40))
  return { width, height, minWidth: Math.min(want.minWidth, width), minHeight: Math.min(want.minHeight, height) }
}

/** Windows: give the window GPUI creates the Nalarupa icon (title bar, taskbar, Alt+Tab: GPUI looks for icon
 *  resource 1, which a Bun exe does not have) and a native frame in the app's colours: caption = top bar,
 *  border = C.edge (Windows 11; older builds ignore the attributes). The icon is dist/nalarupa.ico next to
 *  the exe, or assets/brand in a dev run. Polls until the window exists. */
export function decorateWindow(tries = 50) {
  if (process.platform !== 'win32') return
  try {
    const { symbols: u } = dlopen('user32.dll', {
      FindWindowExW: { args: [FFIType.ptr, FFIType.ptr, FFIType.ptr, FFIType.ptr], returns: FFIType.ptr },
      GetWindowThreadProcessId: { args: [FFIType.ptr, FFIType.ptr], returns: FFIType.u32 },
      IsWindowVisible: { args: [FFIType.ptr], returns: FFIType.i32 },
      GetSystemMetrics: { args: [FFIType.i32], returns: FFIType.i32 },
      LoadImageW: { args: [FFIType.ptr, FFIType.ptr, FFIType.u32, FFIType.i32, FFIType.i32, FFIType.u32], returns: FFIType.ptr },
      SendMessageW: { args: [FFIType.ptr, FFIType.u32, FFIType.u64, FFIType.ptr], returns: FFIType.ptr },
      GetWindowRect: { args: [FFIType.ptr, FFIType.ptr], returns: FFIType.i32 },
      MonitorFromWindow: { args: [FFIType.ptr, FFIType.u32], returns: FFIType.ptr },
      GetMonitorInfoW: { args: [FFIType.ptr, FFIType.ptr], returns: FFIType.i32 },
      SetWindowPos: { args: [FFIType.ptr, FFIType.ptr, FFIType.i32, FFIType.i32, FFIType.i32, FFIType.i32, FFIType.u32], returns: FFIType.i32 },
    })
    const { symbols: dwm } = dlopen('dwmapi.dll', { DwmSetWindowAttribute: { args: [FFIType.ptr, FFIType.u32, FFIType.ptr, FFIType.u32], returns: FFIType.i32 } })
    const owner = new Uint32Array(1)
    let hwnd = u.FindWindowExW(null, null, null, null)
    for (; hwnd; hwnd = u.FindWindowExW(null, hwnd, null, null)) {
      u.GetWindowThreadProcessId(hwnd, ptr(owner))
      if (owner[0] === process.pid && u.IsWindowVisible(hwnd)) break
    }
    if (!hwnd) {
      if (tries > 0) setTimeout(() => decorateWindow(tries - 1), 100)
      return
    }
    const ico = [join(dirname(process.execPath), 'nalarupa.ico'), join(import.meta.dir, '..', 'assets', 'brand', 'nalarupa.ico')].find((p) => existsSync(p))
    if (ico) {
      const path = Buffer.from(`${ico}\0`, 'utf16le')
      // ICON_SMALL (title bar) at SM_CXSMICON, ICON_BIG (taskbar, Alt+Tab) at SM_CXICON; LR_LOADFROMFILE.
      for (const [which, metric] of [[0, 49], [1, 11]]) {
        const size = u.GetSystemMetrics(metric)
        const icon = u.LoadImageW(null, ptr(path), 1, size, size, 0x10)
        if (icon) u.SendMessageW(hwnd, 0x80, which, icon) // WM_SETICON
      }
    }
    // GPUI centres the window on the whole display, so on a short screen its bottom slides under the taskbar
    // (1024×768 with a 48 px taskbar: 2 px off). Re-centre it in the work area of the monitor it is on when it
    // does not fit there. GetWindowRect and GetMonitorInfoW answer in the same DPI space (this thread's).
    const win = new Int32Array(4)
    const info = new Int32Array(10) // MONITORINFO: cbSize [0], rcMonitor [1-4], rcWork [5-8], dwFlags [9]
    info[0] = 40
    const mon = u.GetWindowRect(hwnd, ptr(win)) ? u.MonitorFromWindow(hwnd, 2) : null // MONITOR_DEFAULTTONEAREST
    if (mon && u.GetMonitorInfoW(mon, ptr(info))) {
      const area = info.subarray(5, 9) // rcWork: left, top, right, bottom
      const [w, h] = [win[2] - win[0], win[3] - win[1]]
      if (win[0] < area[0] || win[1] < area[1] || win[2] > area[2] || win[3] > area[3]) {
        const x = area[0] + Math.max(0, Math.floor((area[2] - area[0] - w) / 2))
        const y = area[1] + Math.max(0, Math.floor((area[3] - area[1] - h) / 2))
        u.SetWindowPos(hwnd, null, x, y, 0, 0, 0x0001 | 0x0004 | 0x0010) // SWP_NOSIZE | SWP_NOZORDER | SWP_NOACTIVATE
      }
    }
    const colorref = (hex: string) => new Uint32Array([parseInt(hex.slice(5, 7) + hex.slice(3, 5) + hex.slice(1, 3), 16)])
    dwm.DwmSetWindowAttribute(hwnd, 34, ptr(colorref(C.edge)), 4) // DWMWA_BORDER_COLOR
    dwm.DwmSetWindowAttribute(hwnd, 35, ptr(colorref(C.panel)), 4) // DWMWA_CAPTION_COLOR
  } catch {}
}
export const FONT = process.platform === 'darwin' ? 'Helvetica' : 'Segoe UI'
export const MONO = process.platform === 'darwin' ? 'Menlo' : 'Consolas'

// Fixed chrome sizes: the canvas origin in window coordinates is (LEFT_W, TOP_H).
export const TOP_H = 44
export const LEFT_W = 248
export const RIGHT_W = 300
