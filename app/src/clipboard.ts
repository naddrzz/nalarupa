// System clipboard with text/html + text/plain. GPUIX has no clipboard API, so on Windows this
// talks to user32 directly (CF_UNICODETEXT + the registered "HTML Format").
// ponytail: other platforms keep an in-process clipboard; add pbcopy/xclip when they ship.
import { dlopen, FFIType, ptr, toArrayBuffer } from 'bun:ffi'

export type Clip = { html?: string; text?: string }

const CF_UNICODETEXT = 13
const GMEM_MOVEABLE = 0x2

const win = process.platform === 'win32'
  ? {
      user: dlopen('user32.dll', {
        OpenClipboard: { args: [FFIType.ptr], returns: FFIType.i32 },
        CloseClipboard: { args: [], returns: FFIType.i32 },
        EmptyClipboard: { args: [], returns: FFIType.i32 },
        GetClipboardData: { args: [FFIType.u32], returns: FFIType.ptr },
        SetClipboardData: { args: [FFIType.u32, FFIType.ptr], returns: FFIType.ptr },
        RegisterClipboardFormatW: { args: [FFIType.ptr], returns: FFIType.u32 },
      }).symbols,
      kernel: dlopen('kernel32.dll', {
        GlobalAlloc: { args: [FFIType.u32, FFIType.u64], returns: FFIType.ptr },
        GlobalLock: { args: [FFIType.ptr], returns: FFIType.ptr },
        GlobalUnlock: { args: [FFIType.ptr], returns: FFIType.i32 },
        GlobalSize: { args: [FFIType.ptr], returns: FFIType.u64 },
        RtlMoveMemory: { args: [FFIType.ptr, FFIType.ptr, FFIType.u64], returns: FFIType.void },
      }).symbols,
    }
  : null

let local: Clip = {}
const htmlFormat = win ? win.user.RegisterClipboardFormatW(ptr(Buffer.from('HTML Format\0', 'utf16le'))) : 0

/** Another app may hold the clipboard for a moment. */
function open(): boolean {
  for (let i = 0; i < 10; i++) {
    if (win!.user.OpenClipboard(null)) return true
    Bun.sleepSync(10)
  }
  return false
}

function put(format: number, bytes: Buffer) {
  const { kernel, user } = win!
  const h = kernel.GlobalAlloc(GMEM_MOVEABLE, bytes.length)
  if (!h) return
  const p = kernel.GlobalLock(h)
  if (!p) return
  kernel.RtlMoveMemory(p, bytes, bytes.length)
  kernel.GlobalUnlock(h)
  user.SetClipboardData(format, h)
}

function get(format: number): Buffer | null {
  const { kernel, user } = win!
  const h = user.GetClipboardData(format)
  if (!h) return null
  const p = kernel.GlobalLock(h)
  if (!p) return null
  const copy = Buffer.from(new Uint8Array(toArrayBuffer(p, 0, Number(kernel.GlobalSize(h)))))
  kernel.GlobalUnlock(h)
  return copy
}

/** CF_HTML: a header of byte offsets in front of the UTF-8 document. */
function cfHtml(fragment: string): Buffer {
  const pre = '<html><body><!--StartFragment-->'
  const post = '<!--EndFragment--></body></html>'
  const header = 'Version:0.9\r\nStartHTML:0000000000\r\nEndHTML:0000000000\r\nStartFragment:0000000000\r\nEndFragment:0000000000\r\n'
  const n = (v: number) => String(v).padStart(10, '0')
  const len = (s: string) => Buffer.byteLength(s, 'utf8')
  const start = len(header)
  const fragStart = start + len(pre)
  const fragEnd = fragStart + len(fragment)
  const end = fragEnd + len(post)
  const head = header.replace('StartHTML:0000000000', `StartHTML:${n(start)}`).replace('EndHTML:0000000000', `EndHTML:${n(end)}`)
    .replace('StartFragment:0000000000', `StartFragment:${n(fragStart)}`).replace('EndFragment:0000000000', `EndFragment:${n(fragEnd)}`)
  return Buffer.from(head + pre + fragment + post + '\0', 'utf8')
}

export function writeClipboard(c: Clip): boolean {
  local = c
  if (!win || !open()) return !win
  try {
    win.user.EmptyClipboard()
    if (c.text !== undefined) put(CF_UNICODETEXT, Buffer.from(c.text + '\0', 'utf16le'))
    if (c.html !== undefined && htmlFormat) put(htmlFormat, cfHtml(c.html))
    return true
  } finally {
    win.user.CloseClipboard()
  }
}

export function readClipboard(): Clip {
  if (!win || !open()) return local
  try {
    const t = get(CF_UNICODETEXT)
    const h = htmlFormat ? get(htmlFormat) : null
    const text = t ? t.toString('utf16le').split('\0')[0] : undefined
    const html = h ? h.toString('utf8').split('\0')[0] : undefined
    return { html, text }
  } finally {
    win.user.CloseClipboard()
  }
}

// Round trip through the real clipboard: `bun src/clipboard.ts`
if (import.meta.main) {
  writeClipboard({ html: '<span data-x="1">héllo</span>', text: 'héllo' })
  const back = readClipboard()
  if (back.text !== 'héllo' || !back.html?.includes('<span data-x="1">héllo</span>')) throw new Error(`clipboard round trip failed: ${JSON.stringify(back)}`)
  console.log('clipboard ok')
}
