// Text sizing through the core (no window): `bun scripts/text-check.ts`
// textGrowth (auto, fixed-width, fixed-width-height), line height, letter spacing, font size, newlines, wrapping
// and font fallback decide the box a text node gets; editing the text or its frame re-measures it. Assertions
// compare boxes with each other (or with the line height), never with absolute widths, so they hold with any
// installed fonts. Fonts that are not installed download in the background and the text lays out again when they
// land, so the check first waits for Inter to settle.
import { call } from '../src/core'

const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`text-check failed: ${msg}`)
}
type Box = { id: string; w: number; h: number }
const box = (id: string): Box => call<Box[]>('boxes', { ids: [id] })[0]
const make = (node: object, parent = 'document') => call('insert', { parent, node: { type: 'text', fontFamily: 'Inter', fontSize: 20, fill: '#000000', ...node } }).id as string
const update = (id: string, props: object) => call('update', { ids: [id], props })

call('init')
call('new', { template: 'empty' })

// Warm up: measure Inter text (regular and bold) until the width has held still for 1.5 s (at most 20 s).
const probes = [make({ content: 'Hello world' }), make({ content: 'Hello world', fontWeight: '700' })]
const widths = () => probes.map((p) => box(p).w).join()
for (let seen = widths(), since = Date.now(), end = since + 20000; Date.now() < end && Date.now() - since < 1500; await Bun.sleep(100)) {
  const now = widths()
  if (now !== seen) [seen, since] = [now, Date.now()]
}

// Auto growth: size from the content. A newline adds a line of the same height and the box is as wide as the longest line.
const one = box(make({ content: 'Hello world' }))
assert(one.w > 0 && one.h > 20 && one.h === box(make({ content: 'Hello world', textGrowth: 'fixed-width', width: 1000 })).h, `auto text is one line, as tall as a wide fixed-width box: ${one.w}x${one.h}`)
const two = box(make({ content: 'Hello world\nHi' }))
assert(two.w === one.w && two.h === one.h * 2, `a newline adds a line and keeps the longest line's width: ${two.w}x${two.h}`)
const wide = box(make({ content: 'Hello world', width: 10, height: 10 }))
assert(wide.w === one.w && wide.h === one.h, `auto text ignores a given width and height: ${wide.w}x${wide.h}`)
assert(box(make({ content: '' })).w === 0, 'empty text has no width')

// Line height is a multiple of the font size rounded to whole pixels; letter spacing is added after every character.
assert(box(make({ content: 'Hello', lineHeight: 1.5 })).h === 30 && box(make({ content: 'Hello', lineHeight: 2 })).h === 40, 'lineHeight 1.5 and 2 at 20px give 30 and 40')
const spaced = box(make({ content: 'Hello world', letterSpacing: 5 }))
assert(spaced.w === one.w + 5 * 'Hello world'.length && spaced.h === one.h, `letterSpacing 5 adds 5px per character: ${spaced.w} vs ${one.w}`)
const big = box(make({ content: 'Hello world', fontSize: 40 }))
assert(Math.abs(big.w - one.w * 2) <= 2 && big.h > one.h, `doubling the font size doubles the box: ${big.w}x${big.h}`)
assert(box(make({ content: 'Hello world', fontWeight: '700' })).w >= one.w, 'bold text is not narrower than regular text')

// Fixed width: wraps inside the width, the height grows with the lines, the width stays.
const words = 'one two three four five six'
const narrow = box(make({ content: words, textGrowth: 'fixed-width', width: 80 }))
assert(narrow.w === 80 && narrow.h > one.h * 2 && narrow.h % one.h === 0, `fixed-width wraps and keeps its width: ${narrow.w}x${narrow.h}`)
const roomy = box(make({ content: words, textGrowth: 'fixed-width', width: 1000 }))
assert(roomy.w === 1000 && roomy.h === one.h, `a wide enough fixed-width box is one line: ${roomy.w}x${roomy.h}`)
const none = box(make({ content: 'one two three', textGrowth: 'fixed-width' }))
assert(none.w === 0 && none.h === one.h * 3, `fixed-width without a width wraps one word per line: ${none.w}x${none.h}`)
const unbroken = box(make({ content: 'Supercalifragilistic', textGrowth: 'fixed-width', width: 40 }))
assert(unbroken.w === 40, `a word wider than the box leaves the width alone: ${unbroken.w}x${unbroken.h}`)
const spacedWrap = box(make({ content: words, textGrowth: 'fixed-width', width: 80, letterSpacing: 6 }))
assert(spacedWrap.h > narrow.h, `letter spacing makes the same text wrap onto more lines: ${spacedWrap.h} vs ${narrow.h}`)

// Fixed width and height: wraps, but the box keeps the height it was given even when the text overflows it.
const capped = box(make({ content: words, textGrowth: 'fixed-width-height', width: 80, height: 30 }))
assert(capped.w === 80 && capped.h === 30, `fixed-width-height keeps both sizes: ${capped.w}x${capped.h}`)

// Editing re-measures: new content, a new width and a new growth mode update the same node.
const t = make({ content: words, textGrowth: 'fixed-width', width: 80 })
update(t, { width: 1000 })
assert(box(t).h === one.h, `widening the box unwraps the text: ${box(t).h}`)
update(t, { width: 80, content: 'one two' })
assert(box(t).h < narrow.h, `shorter content makes a shorter box: ${box(t).h}`)
update(t, { content: words, lineHeight: 2 })
assert(box(t).h === narrow.h / one.h * 40, `lineHeight rescales the wrapped box: ${box(t).h}`)
update(t, { textGrowth: 'auto' })
assert(box(t).h === 40 && box(t).w > 80, `back to auto: one line, as wide as the text: ${box(t).w}x${box(t).h}`)
call('undo')
assert(box(t).w === 80, `undo restores the growth mode: ${box(t).w}`)

// Text in a frame: fill_container takes the frame's width and wraps there; resizing the frame re-wraps.
const col = call('insert', { parent: 'document', node: { type: 'frame', name: 'Col', layout: 'vertical', width: 100, padding: 10, fill: '#ffffff' } }).id as string
const para = make({ content: words, textGrowth: 'fixed-width', width: 'fill_container' }, col)
const label = make({ content: 'A label that never wraps even when it is longer than the frame' }, col)
assert(box(para).w === 80 && box(para).h > one.h, `fill_container text wraps at the padded frame width: ${box(para).w}x${box(para).h}`)
assert(box(label).w > 100 && box(label).h === one.h, `auto text in the same frame keeps one line: ${box(label).w}x${box(label).h}`)
assert(box(col).h === 20 + box(para).h + box(label).h, `the fit_content frame hugs its text: ${box(col).h}`)
const hBefore = box(para).h
update(col, { width: 400 })
assert(box(para).w === 380 && box(para).h < hBefore, `widening the frame re-wraps the paragraph: ${box(para).w}x${box(para).h}`)

// Characters the font lacks fall back to another font instead of vanishing.
assert(box(make({ content: '日本語テキスト' })).w > 40, 'CJK text has a width')
assert(box(make({ content: '😀' })).w > 0, 'an emoji has a width')
console.log('text-check ok')
