// Minimal PNG reader and writer for the checks that look at pixels (no window, no dependency).
import { readFileSync } from 'node:fs'
import { deflateSync, inflateSync } from 'node:zlib'

export type Px = number[]

/** Decode an 8-bit RGB / RGBA PNG (what the core exports) into a pixel reader: `at(x, y)` is [r, g, b, a]. */
export const decodePng = (file: string) => {
  const buf = readFileSync(file)
  const w = buf.readUInt32BE(16)
  const h = buf.readUInt32BE(20)
  const bpp = buf[25] === 6 ? 4 : 3
  const idat: Buffer[] = []
  for (let o = 8; o < buf.length; ) {
    const n = buf.readUInt32BE(o)
    if (buf.toString('latin1', o + 4, o + 8) === 'IDAT') idat.push(buf.subarray(o + 8, o + 8 + n))
    o += 12 + n
  }
  const raw = inflateSync(Buffer.concat(idat))
  const stride = w * bpp
  const px = Buffer.alloc(h * stride)
  for (let y = 0; y < h; y++) {
    const filter = raw[y * (stride + 1)]
    for (let i = 0; i < stride; i++) {
      const a = i >= bpp ? px[y * stride + i - bpp] : 0
      const b = y ? px[(y - 1) * stride + i] : 0
      const c = i >= bpp && y ? px[(y - 1) * stride + i - bpp] : 0
      const p = a + b - c
      const [pa, pb, pc] = [Math.abs(p - a), Math.abs(p - b), Math.abs(p - c)]
      const pred = filter === 0 ? 0 : filter === 1 ? a : filter === 2 ? b : filter === 3 ? (a + b) >> 1 : pa <= pb && pa <= pc ? a : pb <= pc ? b : c
      px[y * stride + i] = (raw[y * (stride + 1) + 1 + i] + pred) & 255
    }
  }
  const at = (x: number, y: number): Px => {
    const v = [...px.subarray(y * stride + x * bpp, y * stride + (x + 1) * bpp)]
    return bpp === 3 ? [...v, 255] : v
  }
  return { w, h, at }
}

/** Encode a w x h RGBA PNG, `pixel` giving each pixel's [r, g, b, a]. */
export const encodePng = (w: number, h: number, pixel: (x: number, y: number) => Px) => {
  const be = (n: number) => Buffer.from([n >>> 24, (n >>> 16) & 255, (n >>> 8) & 255, n & 255])
  const chunk = (type: string, data: Uint8Array) => {
    const body = Buffer.concat([Buffer.from(type), data])
    return Buffer.concat([be(data.length), body, be(Bun.hash.crc32(body))])
  }
  const raw = Buffer.alloc(h * (w * 4 + 1))
  for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) raw.set(pixel(x, y), y * (w * 4 + 1) + 1 + x * 4)
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', Buffer.concat([be(w), be(h), Buffer.from([8, 6, 0, 0, 0])])), chunk('IDAT', deflateSync(raw)), chunk('IEND', Buffer.alloc(0))])
}
