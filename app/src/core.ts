// Bridge to the Rust core (nalarupa_core.dll / .so / .dylib) through bun:ffi.
import { CString, dlopen, FFIType, suffix, toArrayBuffer } from 'bun:ffi'
import { existsSync } from 'node:fs'
import type { M } from './geometry'
import { dirname, join } from 'node:path'

const name = `${process.platform === 'win32' ? '' : 'lib'}nalarupa_core.${suffix}`
const candidates = [
  process.env.NALARUPA_CORE,
  join(dirname(process.execPath), name),
  join(import.meta.dir, '..', '..', 'core', 'target', 'release', name),
  join(import.meta.dir, '..', '..', 'core', 'target', 'debug', name),
].filter((p): p is string => !!p)
const libPath = candidates.find((p) => existsSync(p))
if (!libPath) {
  throw new Error(`Nalarupa core library not found. Build it with \`cargo build --release\` in core/. Looked in:\n${candidates.join('\n')}`)
}

const lib = dlopen(libPath, {
  nr_call: { args: [FFIType.ptr, FFIType.u32], returns: FFIType.ptr },
  nr_free: { args: [FFIType.ptr], returns: FFIType.void },
  nr_render: {
    args: [FFIType.u32, FFIType.u32, FFIType.f64, FFIType.f64, FFIType.f64, FFIType.u32, FFIType.ptr, FFIType.u32, FFIType.u32],
    returns: FFIType.ptr,
  },
})

/** Run a core command; throws with the core's message on failure. */
export function call<T = any>(cmd: string, args: Record<string, unknown> = {}): T {
  const buf = Buffer.from(JSON.stringify({ cmd, ...args }))
  const p = lib.symbols.nr_call(buf, buf.length)
  if (!p) throw new Error(`core returned nothing for ${cmd}`)
  const text = new CString(p).toString()
  lib.symbols.nr_free(p)
  const res = JSON.parse(text)
  if (!res.ok) throw new Error(res.error)
  return res.data as T
}

/** Like `call`, but logs and returns null instead of throwing. */
export function tryCall<T = any>(cmd: string, args: Record<string, unknown> = {}): T | null {
  try {
    return call<T>(cmd, args)
  } catch (e) {
    console.error(`[core] ${cmd}:`, (e as Error).message)
    return null
  }
}

/**
 * Rasterize the viewport. Camera is in document units; returns the RGBA pixels as a view of
 * core memory, valid only until the next render call: hand it straight to `setImagePixels`
 * (which copies synchronously) or copy it. With `sharp = false` the core may answer a zoom
 * change with a cheaper frame (viewport only, or its cached view scaled); ask for a sharp
 * frame once the camera settles.
 */
export function renderPixels(w: number, h: number, camX: number, camY: number, zoom: number, bg: number, solo?: string, sharp = true): Uint8Array | null {
  const soloBuf = solo ? Buffer.from(solo) : null
  const p = lib.symbols.nr_render(w, h, camX, camY, zoom, bg, soloBuf, soloBuf ? soloBuf.length : 0, sharp ? 1 : 0)
  if (!p) return null
  return new Uint8Array(toArrayBuffer(p, 0, w * h * 4))
}

/** A laid-out node box; `m` = its painted rotation/flip transform (see geometry.ts `M`), absent when none. */
export type Box = { id: string; name: string; type: string; x: number; y: number; w: number; h: number; parent?: string | null; depth?: number; m?: M | null }
export type State = { version: number; title: string; path: string | null; dirty: boolean; selection: string[]; agentRunning: boolean; mcpPort: number; animating?: boolean }
export type Layer = { id: string; name: string; type: string; depth: number; parent: string; reusable: boolean; hidden: boolean; children: number }
export type AgentEvent = { type: 'agent'; kind: 'user' | 'text' | 'note' | 'tool' | 'error' | 'done' | 'external'; text: string }
