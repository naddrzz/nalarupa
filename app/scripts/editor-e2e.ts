// Canvas interactions against the real window: `bun scripts/editor-e2e.ts`
// Rotated selection frame, flip by negative resize, snapping guides, Alt measurement, marquee, Ctrl+G /
// Ctrl+Shift+G, Shift+A, drag into an auto layout, layer rename, inspector scrubbing, copy/paste, corner
// radius handles, frame title rename, dragging a child of a rotated or flipped frame. Screenshots: app/screenshots/editor-*.png
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { launch } from '@gpuix/react/automation'
import { dlopen, FFIType, ptr } from 'bun:ffi'

const out = join(import.meta.dir, '..', 'screenshots')
mkdirSync(out, { recursive: true })
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))
const assert = (ok: unknown, msg: string) => {
  if (!ok) throw new Error(`editor-e2e failed: ${msg}`)
}

const app = await launch({ command: 'bun', args: ['app.tsx'], cwd: join(import.meta.dir, '..'), env: { ...process.env, GPUIX_BACKGROUND: '1' } })
// GPUIX can't capture on Linux; there the window runs under a headless Wayland compositor, so grab it with grim.
const shot = (name: string) => process.platform === 'linux' ? Bun.$`grim ${join(out, `editor-${name}.png`)}`.quiet() : app.screenshot({ path: join(out, `editor-${name}.png`) })
const count = (testId: string) => app.getByTestId(testId).count()
// CPU rendering (lavapipe on the CI VPS) can lag a fixed sleep: poll a check for up to `ms`.
const until = async (ok: () => Promise<boolean>, ms = 3000) => {
  const end = Date.now() + ms
  // Return the result that ended the loop: asking again could catch the UI mid-update.
  let r = await ok()
  while (!r && Date.now() < end) {
    await sleep(100)
    r = await ok()
  }
  return r
}
const val = async (testId: string) => {
  const e = await app.getByTestId(testId).element()
  return String(e.customProps?.value ?? e.text ?? '')
}
try {
  await app.getByTestId('template-blank').waitFor({ timeoutMs: 30000 })
  await app.getByTestId('template-blank').click()
  await app.getByTestId('canvas').waitFor()
  await sleep(1200)
  const c = await app.getByTestId('canvas').bounds()
  const at = (x: number, y: number) => ({ x: c.x + x, y: c.y + y })
  const canvas = app.getByTestId('canvas')

  // A real mouse wheel pans a little. GPUIX reports a notch as 60 px with precise=false; reading that
  // as lines (×40) threw the view ~2400 px per notch and the canvas looked empty. The automation wheel
  // only sends pixel deltas, so post a real WM_MOUSEWHEEL to this window (no cursor move, no focus).
  if (process.platform === 'win32') {
    const u32 = dlopen('user32.dll', {
      FindWindowExW: { args: [FFIType.ptr, FFIType.ptr, FFIType.ptr, FFIType.ptr], returns: FFIType.ptr },
      GetWindowThreadProcessId: { args: [FFIType.ptr, FFIType.ptr], returns: FFIType.u32 },
      IsWindowVisible: { args: [FFIType.ptr], returns: FFIType.i32 },
      ClientToScreen: { args: [FFIType.ptr, FFIType.ptr], returns: FFIType.i32 },
      PostMessageW: { args: [FFIType.ptr, FFIType.u32, FFIType.u64, FFIType.i64], returns: FFIType.i32 },
    }).symbols
    const { pid } = (await app.call('initialize' as any, { protocolVersion: 1, client: 'editor-e2e' } as any)) as { pid: number }
    const owner = new Uint32Array(1)
    let hwnd = u32.FindWindowExW(null, null, null, null)
    for (; hwnd; hwnd = u32.FindWindowExW(null, hwnd, null, null)) {
      u32.GetWindowThreadProcessId(hwnd, ptr(owner))
      if (owner[0] === pid && u32.IsWindowVisible(hwnd)) break
    }
    assert(hwnd, 'found the test window')
    await shot('00-start')
    const DPR = new DataView(await Bun.file(join(out, 'editor-00-start.png')).arrayBuffer()).getUint32(16) / 1440 // PNG width / logical width
    const pt = new Int32Array([Math.round((c.x + c.width / 2) * DPR), Math.round((c.y + c.height / 2) * DPR)])
    u32.ClientToScreen(hwnd, ptr(pt))
    const notch = async (delta: number) => {
      u32.PostMessageW(hwnd, 0x020a, BigInt(((delta & 0xffff) << 16) >>> 0), BigInt((((pt[1] & 0xffff) << 16) | (pt[0] & 0xffff)) >>> 0))
      await sleep(400)
    }
    const title = canvas.getByText('Desktop')
    const y0 = (await title.bounds()).y
    await notch(-120)
    assert(await title.count(), 'one wheel notch down keeps the frame on screen')
    const moved = y0 - (await title.bounds()).y
    assert(moved > 20 && moved < 200, `one wheel notch pans 20–200 px (got ${moved})`)
    await notch(120)
    assert(Math.abs((await title.bounds()).y - y0) < 2, 'a notch back up restores the view')
  }

  const size = async () => (await app.getByTestId('size-label').textContent()).split(' × ').map(Number)
  const rectRows = () => app.getByTestId('layers').getByText('Rectangle').count()

  // Two rectangles inside the 1440×900 "Desktop" artboard (fit to the canvas).
  await app.getByTestId('tool-rectangle').click()
  await app.mouse.drag(at(150, 260), at(250, 340), { steps: 5 })
  await sleep(300)

  // Shift-resize from a corner keeps the aspect ratio; one undo step reverts it.
  const [rw0, rh0] = await size()
  await app.mouse.drag(at(250, 340), at(290, 345), { steps: 5, modifiers: 'shift' })
  await sleep(300)
  const [rw1, rh1] = await size()
  await shot('00a-shift-resize')
  assert(rw1 > rw0 + 50 && Math.abs(rw1 / rh1 - rw0 / rh0) < 0.02, `shift resize keeps the aspect (${rw0}×${rh0} → ${rw1}×${rh1})`)
  await canvas.press('ctrl-z')
  await sleep(300)
  assert(await until(async () => (await size()).join() === [rw0, rh0].join()), "undo restores the size")

  // Rotation zone just outside a corner; Shift snaps to 15°.
  await app.mouse.drag(at(258, 252), at(275, 330), { steps: 6, modifiers: 'shift' })
  await sleep(300)
  await shot('00b-rotated')
  assert(await count('rotated-overlay'), 'the selection box and handles turn with the rotated rectangle')
  // From the NE corner (-40° around the center at screen 200,300) to +22°: Shift snaps the turn to 60° clockwise
  // (rotation -60). The right-edge handle sits on the turned edge and resizes along the rectangle's own x axis:
  // 30 px out = +30 screen px of width, height unchanged.
  const z = 100 / rw0
  const [ux, uy] = [Math.cos(Math.PI / 3), Math.sin(Math.PI / 3)]
  await app.mouse.drag(at(200 + 50 * ux, 300 + 50 * uy), at(200 + 80 * ux, 300 + 80 * uy), { steps: 5 })
  await sleep(300)
  await shot('00c-rotated-resize')
  const [rw2, rh2] = await size()
  assert(Math.abs(rw2 - (rw0 + 30 / z)) <= 2 && rh2 === rh0, `rotated handle resizes along the turned axis (${rw0}×${rh0} → ${rw2}×${rh2})`)
  await canvas.press('ctrl-z')
  await canvas.press('ctrl-z')
  await sleep(300)
  assert(await until(async () => !(await count('rotated-overlay')) && (await size()).join() === [rw0, rh0].join()), 'two undo steps revert the resize and the rotation')

  // Typing a rotation in the Inspector turns the layer around its center like the handle does: the center stays put.
  const frame = async () => {
    const f = (await app.getByTestId('selection-box').all()).concat(await app.getByTestId('rotated-overlay').all())[0]?.bounds
    return f ? { cx: f.x + f.width / 2, cy: f.y + f.height / 2, w: f.width } : { cx: NaN, cy: NaN, w: NaN }
  }
  const f0 = await frame()
  const rot = await app.getByTestId('inspector-rotation').bounds()
  await app.mouse.click({ x: rot.x + rot.width - 10, y: rot.y + rot.height / 2 })
  await sleep(500)
  await app.call('keystrokes', { keys: 'ctrl-a 9 0' })
  await sleep(500)
  await app.call('keystrokes', { keys: 'enter' })
  // A quarter turn swaps the rectangle's frame width for its height (101 → 92 px on screen).
  assert(await until(async () => Math.abs((await frame()).w - f0.w) > 4, 6000), 'the Inspector rotation applies')
  await sleep(500)
  const f1 = await frame()
  await shot('00c2-inspector-rotated')
  assert(Math.abs(f1.cx - f0.cx) <= 2 && Math.abs(f1.cy - f0.cy) <= 2, `Inspector rotation keeps the center (${f0.cx},${f0.cy} → ${f1.cx},${f1.cy})`)
  await canvas.press('ctrl-z')
  assert(await until(async () => { const u = await frame(); return Math.abs(u.w - f0.w) <= 2 && Math.abs(u.cx - f0.cx) <= 2 && Math.abs(u.cy - f0.cy) <= 2 }), 'one undo step reverts the Inspector rotation')

  // Dragging the right edge past the left one flips the rectangle: it now paints left of its old left edge.
  await app.mouse.drag(at(250, 300), at(100, 300), { steps: 6 })
  await sleep(300)
  await shot('00d-flipped')
  const [wf, hf] = await size()
  assert(Math.abs(wf - rw0 / 2) <= rw0 / 10 && hf === rh0, `negative resize keeps a positive size (${wf}×${hf}, ~${rw0 / 2}×${rh0})`)
  await canvas.press('escape')
  await sleep(200)
  assert(!(await count('size-label')), 'escape clears the selection')
  assert(await app.getByText('P pen').count(), 'the nothing-selected help lists the Pen tool')
  await app.mouse.click(at(125, 300))
  await sleep(300)
  assert((await size()).join() === [wf, hf].join(), 'clicking where the flipped rectangle paints selects it')
  await canvas.press('ctrl-z')
  await sleep(300)
  assert((await size()).join() === [rw0, rh0].join(), 'one undo step reverts the flip')

  // A hidden layer has no render box: the Inspector still reports its own size, not 0 × 0. Hiding drops the
  // selection (no box to select), so pick the layer from the Layers panel again.
  const rectRow = app.getByTestId('layers').getByText('Rectangle')
  await app.getByTestId('inspector-visibility').click()
  assert(await until(async () => !(await count('inspector-visibility'))), 'hiding the layer clears the selection')
  await rectRow.click()
  assert(await until(async () => (await app.getByText(`Hidden ${rw0} × ${rh0}`).count()) > 0), `a hidden layer still shows its ${rw0} × ${rh0} size in the Inspector`)
  assert(!(await app.getByText('× 0 ·').count()), 'a hidden layer is not reported as 0 tall')
  await canvas.press('ctrl-z')
  await sleep(600) // past the double-click window, or the second row click would start a rename
  await rectRow.click()
  assert(await until(async () => (await app.getByText(`Rendered ${rw0} × ${rh0}`).count()) > 0), 'one undo step shows the layer again at its rendered size')

  await app.getByTestId('tool-rectangle').click()
  await app.mouse.drag(at(420, 260), at(520, 340), { steps: 5 })
  await sleep(300)

  // Snapping: drag the second rectangle a little off the first one's top edge → a guide appears.
  await app.mouse.down(at(470, 300))
  for (let i = 1; i <= 6; i++) await app.mouse.move(at(470 - i * 10, 300 + (i === 6 ? 3 : i)), { pressedButton: 0 })
  await sleep(250)
  const guides = await count('snap-guide')
  await shot('01-snap-guide')
  await app.mouse.up(at(410, 303))
  assert(guides > 0, 'dragging near an edge shows a snap guide')
  await sleep(300)
  const y = await app.getByTestId('inspector-y').element()
  console.log('snapped:', guides, 'guide(s), dragged rectangle Y =', JSON.stringify(y.text ?? y.value ?? ''))

  // Alt-drag duplicates: the copy moves, the original stays; one undo step removes the copy.
  await app.mouse.drag(at(450, 300), at(450, 440), { steps: 6, modifiers: 'alt' })
  await sleep(400)
  await shot('01b-alt-duplicate')
  assert((await rectRows()) === 3, 'Alt-drag leaves the original and drags a copy')
  await canvas.press('ctrl-z')
  await sleep(400)
  await shot('01c-alt-undo')
  assert(await until(async () => (await rectRows()) === 2), 'undo removes the Alt-drag copy in one step')

  // Alt-hover: distances from the selection to the other rectangle.
  await app.mouse.click(at(400, 300))
  await sleep(200)
  await app.mouse.move(at(200, 300), { modifiers: 'alt' })
  await sleep(250)
  const measures = await count('measure-line')
  await shot('02-alt-measure')
  assert(measures > 0, 'Alt+hover measures to the hovered layer')

  // Shift+H / Shift+V flip the selected layer about its center: the origin moves across the box (X + width, Y + height)
  // and one undo step puts it back.
  const [fx0, fy0, fw, fh] = await Promise.all(['inspector-x', 'inspector-y', 'inspector-w', 'inspector-h'].map(async (id) => Number(await val(id))))
  for (const [key, field, want] of [['shift-h', 'inspector-x', fx0 + fw], ['shift-v', 'inspector-y', fy0 + fh]] as const) {
    await canvas.press(key)
    assert(await until(async () => Number(await val(field)) === want), `${key} moves the origin across the box (${field} → ${want}, got ${await val(field)})`)
    assert(Number(await val(field === 'inspector-x' ? 'inspector-y' : 'inspector-x')) === (field === 'inspector-x' ? fy0 : fx0), `${key} leaves the other axis alone`)
    await canvas.press('ctrl-z')
    assert(await until(async () => Number(await val(field)) === (field === 'inspector-x' ? fx0 : fy0)), `one undo step restores the origin after ${key}`)
  }

  // Marquee from the artboard's empty area selects both rectangles (touch semantics).
  await app.mouse.drag(at(120, 420), at(430, 300), { steps: 6 })
  await sleep(300)
  await shot('03-marquee')
  assert(await app.getByText('2 layers selected').count(), 'marquee selects both rectangles')

  // Shift+H / Shift+V mirror every selected layer in place; one undo step reverts the whole flip.
  const boxes = async () => JSON.stringify((await app.getByTestId('selection-box').all()).map((n: any) => n.bounds))
  const still = async (a: string) => until(async () => (await boxes()) === a)
  const placed = await boxes()
  const flipKey = async (key: string) => {
    await canvas.press(key)
    await sleep(500)
  }
  for (const key of ['shift-h', 'shift-v']) {
    await flipKey(key)
    assert(await still(placed), `${key} keeps both layers where they were painted (${placed} → ${await boxes()})`)
    await canvas.press('ctrl-z')
    await sleep(500)
    assert(await still(placed), `one undo step reverts the ${key} flip of both layers`)
  }
  // Flipped twice (a second Shift+H) the layers are back to unflipped and still in place.
  await flipKey('shift-h')
  await flipKey('shift-h')
  assert(await still(placed), 'flipping twice returns the layers to where they started')
  await canvas.press('ctrl-z')
  await canvas.press('ctrl-z')
  await sleep(500)
  assert(await still(placed), 'two undo steps revert the two flips')

  // Ctrl+G groups, Ctrl+Shift+G ungroups.
  await canvas.press('ctrl-g')
  await sleep(400)
  assert((await app.getByText('Group').count()) > 0, 'Ctrl+G creates a group')
  await shot('04-grouped')
  // A group is the union of its children: the Inspector offers no W/H to type into, and a typed rotation
  // turns it around its center instead of its origin.
  assert(!(await count('inspector-w')) && !(await count('inspector-h')), 'a group has no W/H fields')
  const g0 = await frame()
  const grot = await app.getByTestId('inspector-rotation').bounds()
  await app.mouse.click({ x: grot.x + grot.width - 10, y: grot.y + grot.height / 2 })
  await sleep(500)
  await app.call('keystrokes', { keys: 'ctrl-a 3 0' })
  await sleep(500)
  await app.call('keystrokes', { keys: 'enter' })
  assert(await until(async () => (await count('rotated-overlay')) > 0, 6000), 'the Inspector rotates a group')
  await sleep(500)
  const g1 = await frame()
  assert(Math.abs(g1.cx - g0.cx) <= 2 && Math.abs(g1.cy - g0.cy) <= 2, `Inspector rotation keeps a group's center (${g0.cx},${g0.cy} → ${g1.cx},${g1.cy})`)
  await canvas.press('ctrl-z')
  assert(await until(async () => !(await count('rotated-overlay'))), 'one undo step reverts the group rotation')
  await canvas.press('ctrl-shift-g')
  await sleep(400)
  assert(await app.getByText('2 layers selected').count(), 'Ctrl+Shift+G ungroups back to the two rectangles')

  // Shift+A wraps them in an auto-layout frame (row, inferred gap).
  await canvas.press('shift-a')
  await sleep(500)
  assert(await count('inspector-gap'), 'Shift+A adds an auto layout (gap field shown)')
  await shot('05-auto-layout')

  // Drag a new rectangle into the auto layout: the drop indicator shows, release inserts it.
  await app.getByTestId('tool-rectangle').click()
  await app.mouse.drag(at(250, 450), at(310, 500), { steps: 4 })
  await sleep(300)
  await shot('06a-drawn')
  await app.mouse.down(at(280, 475))
  for (let i = 1; i <= 8; i++) await app.mouse.move(at(280 + i * 2, 475 - i * 22), { pressedButton: 0 })
  await sleep(250)
  const indicator = await count('drop-target')
  await shot('06-drop-into-layout')
  await app.mouse.up(at(296, 299))
  await sleep(400)
  assert(indicator > 0, 'dragging over an auto-layout frame shows the drop indicator')
  assert(await app.getByText('Size (auto layout child)').count(), 'released rectangle is now an auto-layout child')
  await shot('07-dropped')

  // Layers: drag the selected (middle) rectangle's row onto the Desktop row → it leaves the layout.
  const rowsBefore = await app.getByTestId('layers').getByText('Rectangle').all()
  const from = rowsBefore[1].bounds!
  const desk = await app.getByTestId('layers').getByText('Desktop').center()
  await app.mouse.down({ x: from.x + from.width / 2, y: from.y + from.height / 2 })
  for (let i = 1; i <= 6; i++) await app.mouse.move({ x: desk.x, y: from.y + ((desk.y - from.y) * i) / 6 }, { pressedButton: 0 })
  await sleep(200)
  await shot('07b-layers-drag')
  await app.mouse.up(desk)
  await sleep(400)
  assert(!(await app.getByText('Size (auto layout child)').count()), 'dropping on the Desktop row moved the rectangle out of the layout')
  await canvas.press('ctrl-z')
  await sleep(300)

  // Layers: double-click the frame row, rename it.
  await canvas.press('shift-enter')
  await sleep(300)
  const frameRow = app.getByTestId('layers').getByText('Frame')
  await frameRow.click()
  await frameRow.click()
  await app.getByTestId('layer-rename').waitFor({ timeoutMs: 3000 })
  // Not fill(): GPUIX builds virtual-list rows against the latest tree, so an input whose batch lands
  // mid-frame is built before its focus handle exists and its editor keeps a private one. fill() focuses
  // the element's handle and the keys skip the editor (often under slow CPU rendering). A click focuses
  // the editor's own handle (its right end, after the double-click window, so it is not a third click
  // on the row); then type into whatever has focus.
  const rn = (await app.getByTestId('layer-rename').bounds())!
  await sleep(600)
  await app.mouse.click({ x: rn.x + rn.width - 8, y: rn.y + rn.height / 2 })
  await app.call('keystrokes', { keys: 'ctrl-a C a r d s enter' })
  await sleep(400)
  assert(await app.getByText('Cards').count(), 'layer renamed from the layers panel')
  await shot('08-renamed')

  // An unfocused Inspector field shows the start of a value wider than itself (GPUIX scrolls a set input to
  // its end): rename to a long name and look for the painted text at the field's left edge.
  const long = `Zebra${'q'.repeat(70)}`
  const row = app.getByTestId('layers').getByText('Cards')
  await row.click()
  await row.click()
  await app.getByTestId('layer-rename').waitFor({ timeoutMs: 3000 })
  const rn2 = (await app.getByTestId('layer-rename').bounds())!
  await sleep(600)
  await app.mouse.click({ x: rn2.x + rn2.width - 8, y: rn2.y + rn2.height / 2 })
  await app.call('keystrokes', { keys: `ctrl-a ${[...long].join(' ')} enter` })
  const field = (await app.getByTestId('inspector-name').bounds())!
  const painted = async () => (await app.getByText(long).all()).filter((n) => n.bounds && n.bounds.x >= field.x && n.bounds.x < field.x + field.width && Math.abs(n.bounds.y - field.y) < field.height)
  assert(await until(async () => (await painted()).length === 1), 'the Inspector name field paints the long name')
  const text = (await painted())[0].bounds!
  assert(text.x - field.x < 12 && text.x + text.width <= field.x + field.width, `the long name starts at the field's left edge and is clipped to it (${JSON.stringify({ field, text })})`)
  await shot('08b-long-name')
  await canvas.press('ctrl-z')
  await sleep(400)
  assert(await until(async () => (await app.getByText('Cards').count()) > 0), 'undo restored the layer name')

  // Inspector: scrub the gap label (2 px per step): 20 px = +10 per gap, the hugging row grows by 20; one undo step.
  const width = async () => Number((await app.getByTestId('size-label').textContent()).split(' ')[0])
  const w0 = await width()
  const gap = await app.getByTestId('inspector-gap-scrub').center()
  await app.mouse.down(gap)
  for (let i = 1; i <= 5; i++) await app.mouse.move({ x: gap.x + i * 4, y: gap.y }, { pressedButton: 0 })
  await app.mouse.up({ x: gap.x + 20, y: gap.y })
  await sleep(400)
  await until(async () => (await width()) === w0 + 20)
  await shot('09-scrubbed-gap')
  const w1 = await width()
  assert(w1 === w0 + 20, `scrubbing the gap 20 px widens the row by 2 × 10 (${w0} → ${w1})`)
  await canvas.press('ctrl-z')
  await sleep(400)
  assert(await until(async () => (await width()) === w0), 'one undo step reverts the whole scrub')
  // Up arrow ×3 in the gap field: +3 per gap; leaving the field and one undo revert all three.
  for (let i = 0; i < 3; i++) {
    await app.getByTestId('inspector-gap').press('up')
    await sleep(120)
  }
  await until(async () => (await width()) === w0 + 6)
  const wa = await width()
  await shot('09b-arrow-gap')
  assert(wa === w0 + 6, `arrow keys step the gap (${w0} → ${wa})`)
  await canvas.press('ctrl-z')
  await sleep(400)
  assert(await until(async () => (await width()) === w0), 'arrow steps in one focus session undo together')

  // Free-text fields: Padding takes CSS-style "v h" and is stored as an array; text it cannot use reverts with an error toast.
  // Click the field's right end, then send each key batch in its own call.
  const typeInto = async (testId: string, keys: string) => {
    const b = (await app.getByTestId(testId).bounds())!
    await app.mouse.click({ x: b.x + b.width - 10, y: b.y + b.height / 2 })
    await sleep(700)
    await app.call('keystrokes', { keys })
    await sleep(1000)
    await app.call('keystrokes', { keys: 'enter' })
    await sleep(1500)
  }
  // A focused field paints its draft and an <input> value cannot be read, so move focus to the gap field before reading the padding back.
  const unfocus = async () => {
    const b = (await app.getByTestId('inspector-gap').bounds())!
    await app.mouse.click({ x: b.x + b.width - 10, y: b.y + b.height / 2 })
    await sleep(700)
  }
  await typeInto('inspector-padding', 'ctrl-a 4 0 space 8 0')
  await unfocus()
  assert(await until(async () => (await app.getByText('[40,80]').count()) > 0), 'CSS-style padding "40 80" is stored as [40, 80]')
  assert(!(await count('toast')), 'a valid padding raises no error')
  await canvas.press('ctrl-z')
  await unfocus()
  assert(await until(async () => !(await app.getByText('[40,80]').count())), 'one undo step reverts the padding')
  await typeInto('inspector-padding', 'ctrl-a a b c')
  assert(await until(async () => (await count('toast')) > 0), 'padding text that is not lengths raises an error toast')
  assert(!(await app.getByText('abc').count()), 'the padding field reverts instead of showing the rejected text')
  await app.getByTestId('toast-dismiss').click()

  // Copy / paste: the pasted frame gets selected; undo removes it in one step.
  await canvas.press('ctrl-c')
  await canvas.press('ctrl-v')
  await sleep(400)
  assert((await app.getByTestId('layers').getByText('Cards').count()) >= 2, 'Ctrl+V pastes a copy')
  await canvas.press('ctrl-z')
  await sleep(400)

  // Corner-radius handles on a large rectangle: 12 px in from each corner at radius 0.
  await app.getByTestId('tool-rectangle').click()
  await app.mouse.drag(at(600, 420), at(780, 580), { steps: 5 })
  await sleep(300)

  // Inspector: an empty or unparsable value is rejected (the field reverts) instead of committing 0, an empty
  // name or an unpaintable color. Click the field's right end, then send each key batch in its own call.
  if (await count('toast-dismiss')) await app.getByTestId('toast-dismiss').click()
  const sized0 = (await app.getByTestId('size-label').textContent()).trim()
  const rows0 = await rectRows()
  await typeInto('inspector-w', 'ctrl-a backspace')
  assert((await app.getByTestId('size-label').textContent()).trim() === sized0, 'an empty width reverts instead of committing 0')
  await typeInto('inspector-w', 'ctrl-a a b c')
  assert((await app.getByTestId('size-label').textContent()).trim() === sized0, 'a non-numeric width is ignored')
  await typeInto('inspector-name', 'ctrl-a backspace')
  assert((await rectRows()) === rows0, 'an empty layer name is rejected')
  assert(!(await count('toast')), 'no error toast before bad values')
  await typeInto('inspector-stroke-width', 'ctrl-a x')
  assert(await until(async () => (await count('toast')) > 0), 'a stroke width that is not a number raises an error toast')
  await app.getByTestId('toast-dismiss').click()
  await typeInto('inspector-effect', 'ctrl-a a b c')
  assert(await until(async () => (await count('toast')) > 0), 'an effect that is not JSON raises an error toast')
  await app.getByTestId('toast-dismiss').click()
  await typeInto('inspector-fill', 'ctrl-a r e d')
  assert(await until(async () => (await count('toast')) > 0), 'a color the renderer cannot read raises an error toast')
  await app.getByTestId('toast-dismiss').click()
  await typeInto('inspector-fill', 'ctrl-a # 1 1 2 2 3 3')
  await sleep(500)
  assert(!(await count('toast')), 'a valid hex color is applied without an error')
  await canvas.press('ctrl-z')
  await sleep(300)

  // Handle centers (canvas px), tl, tr, br, bl. Each sits 12 px in at radius 0 and slides toward the middle
  // (80 px for a 180×160 box) as the radius grows: inset = 12 + radius·68/80.
  const insets = async () => (await app.getByTestId('radius-handle').all()).map((n, i) => {
    const b = n.bounds!
    const [x, y] = [b.x + b.width / 2 - c.x, b.y + b.height / 2 - c.y]
    // Distance in from the corner along each axis.
    return [[x - 600, y - 420], [780 - x, y - 420], [780 - x, 580 - y], [x - 600, 580 - y]][i].map((v) => Math.round(v * 10) / 10)
  })
  const near = (a: number[][], b: number[]) => a.length === b.length && a.every(([dx, dy], i) => Math.abs(dx - b[i]) <= 1.5 && Math.abs(dy - b[i]) <= 1.5)
  assert(near(await insets(), [12, 12, 12, 12]), `a rectangle over 100 px shows four radius handles 12 px in (${JSON.stringify(await insets())})`)
  // Equal corners move together: 20 px toward the center on both axes = radius 20 screen px.
  await app.mouse.drag(at(612, 432), at(632, 452), { steps: 5 })
  await sleep(300)
  await shot('11a-radius')
  assert(near(await insets(), [29, 29, 29, 29]), `dragging a radius handle rounds all corners (${JSON.stringify(await insets())})`)
  // Alt drags one corner: 10 px more on the bottom-right only (radius 30 → 12 + 30·68/80 = 37.5 px in).
  await app.mouse.drag(at(751, 551), at(741, 541), { steps: 5, modifiers: 'alt' })
  await sleep(300)
  await shot('11b-radius-one')
  assert(near(await insets(), [29, 29, 37.5, 29]), `Alt rounds one corner (${JSON.stringify(await insets())})`)
  await canvas.press('ctrl-z')
  await sleep(300)
  assert(near(await insets(), [29, 29, 29, 29]), 'one undo step per radius drag')
  await canvas.press('ctrl-z')
  await canvas.press('ctrl-z')
  await sleep(300)

  // Double-click the artboard's canvas title to rename it in place; one undo step.
  const title = await app.getByTestId('frame-label').center()
  await app.mouse.click(title)
  await app.mouse.click(title)
  await app.getByTestId('frame-rename').waitFor({ timeoutMs: 3000 })
  await app.getByTestId('frame-rename').fill('Home')
  await app.getByTestId('frame-rename').press('enter')
  await sleep(400)
  await shot('12-frame-renamed')
  assert(await app.getByTestId('layers').getByText('Home').count(), 'frame renamed from its canvas title')
  await canvas.press('ctrl-z')
  await sleep(400)
  assert(await app.getByTestId('layers').getByText('Desktop').count(), 'one undo step reverts the title rename')

  // Pen tool (vector editing): click = corner, drag = curve handles, clicking the first point closes.
  await canvas.press('escape')
  await canvas.press('p')
  await sleep(200)
  const anchors = async () => (await count('pen-anchor')) + (await count('pen-anchor-selected'))
  const anchorAt = async (testId: string) => {
    const b = (await app.getByTestId(testId).all())[0]?.bounds
    return b ? [Math.round(b.x + b.width / 2 - c.x), Math.round(b.y + b.height / 2 - c.y)] : []
  }
  await app.mouse.click(at(120, 420))
  await app.mouse.click(at(300, 420))
  await app.mouse.drag(at(300, 560), at(340, 600), { steps: 5 })
  await sleep(200)
  await app.mouse.click(at(120, 420))
  await sleep(400)
  await shot('13a-pen-drawn')
  assert((await anchors()) === 3, `three points placed (${await anchors()})`)
  assert(await app.getByTestId('layers').getByText('Path').count(), 'the pen made a Path layer')
  assert(await count('pen-overlay'), 'outline and handles are drawn')
  // V = point tool: drag the second point; a double-click turns it smooth, one undo step reverts that.
  await canvas.press('v')
  await sleep(200)
  await app.mouse.drag(at(300, 420), at(330, 390), { steps: 5 })
  await sleep(400)
  const moved = await anchorAt('pen-anchor-selected')
  assert(Math.abs(moved[0] - 330) <= 1.5 && Math.abs(moved[1] - 390) <= 1.5, `the dragged point follows the pointer (${moved})`)
  const sized = (await app.getByTestId('size-label').textContent()).trim()
  await sleep(500)
  await app.mouse.click(at(330, 390))
  await app.mouse.click(at(330, 390))
  await sleep(400)
  await shot('13b-pen-smooth')
  const smooth = (await app.getByTestId('size-label').textContent()).trim()
  assert(smooth !== sized, `a double-click makes the point smooth and the outline bulges (${sized} → ${smooth})`)
  await canvas.press('ctrl-z')
  await sleep(400)
  assert((await app.getByTestId('size-label').textContent()).trim() === sized, 'one undo step reverts the smooth point')
  // Escape clears the selected points (the undo already did), then leaves; Enter on the path edits it again.
  await canvas.press('escape')
  await sleep(200)
  if (await count('pen-overlay')) await canvas.press('escape')
  await sleep(300)
  assert(!(await count('pen-overlay')) && !(await anchors()), 'escape leaves point editing')
  await app.getByTestId('layers').getByText('Path').click()
  await sleep(200)
  await canvas.press('enter')
  await sleep(300)
  assert((await anchors()) === 3, 'Enter on a path edits its points')
  // With a Layers row focused the point-editing keys still apply: Delete with no point selected keeps the path, Escape leaves editing.
  const pathsEditing = await app.getByTestId('layers').getByText('Path').count()
  await app.getByTestId('layers').getByText('Path').click()
  await sleep(300)
  await app.call('keystrokes', { keys: 'delete' })
  await sleep(300)
  assert((await app.getByTestId('layers').getByText('Path').count()) === pathsEditing && (await anchors()) === 3, 'Delete from a Layers row does not remove the path being point-edited')
  await app.call('keystrokes', { keys: 'escape' })
  assert(await until(async () => !(await count('pen-overlay'))), 'Escape from a Layers row leaves point editing')
  await canvas.press('escape')
  await sleep(200)

  // A clicked Layers row owns the keyboard: the editor shortcuts still work (keys go to the focused element).
  const paths = () => app.getByTestId('layers').getByText('Path').count()
  const paths0 = await paths()
  await app.getByTestId('layers').getByText('Path').click()
  await sleep(300)
  await app.call('keystrokes', { keys: 'ctrl-d' })
  assert(await until(async () => (await paths()) === paths0 + 1), 'Ctrl+D duplicates the layer selected in the Layers panel')
  await app.call('keystrokes', { keys: 'delete' })
  assert(await until(async () => (await paths()) === paths0), 'Delete removes the layer selected in the Layers panel')
  await app.getByTestId('layers').getByText('Path').click()
  await sleep(300)
  assert(await count('size-label'), 'clicking a Layers row selects it')
  await app.call('keystrokes', { keys: 'escape' })
  assert(await until(async () => !(await count('size-label'))), 'Escape clears the selection from a Layers row')

  // A child of a rotated or flipped frame follows the pointer: the drag delta is turned into the frame's space first.
  await app.getByTestId('tool-frame').click()
  await app.mouse.drag(at(560, 80), at(800, 250), { steps: 5 })
  await sleep(300)
  await app.getByTestId('tool-rectangle').click()
  await app.mouse.drag(at(580, 100), at(640, 150), { steps: 5 })
  await sleep(300)
  await canvas.press('shift-enter')
  await typeInto('inspector-rotation', 'ctrl-a 3 0')
  assert(await until(async () => (await count('rotated-overlay')) > 0, 6000), 'the frame is rotated 30°')
  const center = async () => {
    const n = (await app.getByTestId('selection-box').all()).concat(await app.getByTestId('rotated-overlay').all())[0]?.bounds
    return n ? [n.x + n.width / 2, n.y + n.height / 2] : [NaN, NaN]
  }
  const dragRight = async (what: string) => {
    await canvas.press('enter')
    await sleep(400)
    const [x0, y0] = await center()
    await app.mouse.drag({ x: x0, y: y0 }, { x: x0 + 60, y: y0 }, { steps: 8 })
    await sleep(400)
    const [x1, y1] = await center()
    assert(Math.abs(x1 - x0 - 60) <= 2 && Math.abs(y1 - y0) <= 2, `a child of a ${what} frame follows the pointer (${x0},${y0} → ${x1},${y1})`)
    await canvas.press('shift-enter')
  }
  await dragRight('rotated')
  await canvas.press('shift-h')
  await sleep(400)
  await dragRight('flipped')
  await shot('16-rotated-parent-drag')
  await canvas.press('delete')
  await sleep(300)

  // Clipboard pastes: only Windows has a system clipboard (src/clipboard.ts); elsewhere it is per process,
  // so this script can't put data on the app's clipboard.
  if (process.platform === 'win32') {
    // Figma copy → editable layers (the fig-kiwi clipboard: auto layout, text, an instance with an override).
    const { writeClipboard } = await import('../src/clipboard')
    writeClipboard({ html: await Bun.file(join(import.meta.dir, '..', '..', 'core', 'tests', 'figma-clipboard.html')).text(), text: '' })
    await canvas.press('escape')
    await canvas.press('ctrl-v')
    await sleep(700)
    await shot('14-figma-paste')
    assert(await app.getByTestId('layers').getByText('Card').count(), 'the Figma frame pasted as a layer')

    // Mesh gradient fill (a 3×3 mesh with a moved, curved center point), pasted as .pen clipboard data.
    const mesh = {
      type: 'rectangle', name: 'Mesh', x: 1000, y: 600, width: 380, height: 240, cornerRadius: 28,
      fill: {
        type: 'mesh_gradient', columns: 3, rows: 3,
        colors: ['#FF5F6D', '#FFC371', '#47CF73', '#7F7FD5', '#FFFFFF', '#91EAE4', '#F7797D', '#C471ED', '#12C2E9'],
        points: [[0, 0], [0.5, 0], [1, 0], [0, 0.5], { position: [0.62, 0.4], leftHandle: [-0.2, 0.1], rightHandle: [0.2, -0.1] }, [1, 0.5], [0, 1], [0.5, 1], [1, 1]],
      },
    }
    const b64 = Buffer.from(JSON.stringify({ source: 'editor-e2e', localData: [], remoteData: { nodes: [mesh] } })).toString('base64')
    writeClipboard({ html: `<span data-pen-node-clipboard="${b64}">Mesh</span>`, text: 'Mesh' })
    await canvas.press('escape')
    await canvas.press('ctrl-v')
    await sleep(700)
    await shot('15-mesh-gradient')
    assert(await app.getByTestId('layers').getByText('Mesh').count(), 'the mesh-filled rectangle pasted')
  }
  await shot('10-final')
  console.log('editor-e2e ok — screenshots in', out)
} finally {
  await app.close()
}
