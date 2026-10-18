// Canvas interactions against the real window: `bun scripts/editor-e2e.ts`
// Rotated selection frame, flip by negative resize, snapping guides, Alt measurement, marquee, Ctrl+G /
// Ctrl+Shift+G, Shift+A, drag into an auto layout, layer rename, inspector scrubbing, copy/paste, corner
// radius handles, frame title rename. Screenshots: app/screenshots/editor-*.png
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
  assert((await size()).join() === [rw0, rh0].join(), "undo restores the size")

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
  assert(!(await count('rotated-overlay')) && (await size()).join() === [rw0, rh0].join(), 'two undo steps revert the resize and the rotation')

  // Dragging the right edge past the left one flips the rectangle: it now paints left of its old left edge.
  await app.mouse.drag(at(250, 300), at(100, 300), { steps: 6 })
  await sleep(300)
  await shot('00d-flipped')
  const [wf, hf] = await size()
  assert(Math.abs(wf - rw0 / 2) <= rw0 / 10 && hf === rh0, `negative resize keeps a positive size (${wf}×${hf}, ~${rw0 / 2}×${rh0})`)
  await canvas.press('escape')
  await sleep(200)
  assert(!(await count('size-label')), 'escape clears the selection')
  await app.mouse.click(at(125, 300))
  await sleep(300)
  assert((await size()).join() === [wf, hf].join(), 'clicking where the flipped rectangle paints selects it')
  await canvas.press('ctrl-z')
  await sleep(300)
  assert((await size()).join() === [rw0, rh0].join(), 'one undo step reverts the flip')

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
  assert((await rectRows()) === 2, 'undo removes the Alt-drag copy in one step')

  // Alt-hover: distances from the selection to the other rectangle.
  await app.mouse.click(at(400, 300))
  await sleep(200)
  await app.mouse.move(at(200, 300), { modifiers: 'alt' })
  await sleep(250)
  const measures = await count('measure-line')
  await shot('02-alt-measure')
  assert(measures > 0, 'Alt+hover measures to the hovered layer')

  // Marquee from the artboard's empty area selects both rectangles (touch semantics).
  await app.mouse.drag(at(120, 420), at(430, 300), { steps: 6 })
  await sleep(300)
  await shot('03-marquee')
  assert(await app.getByText('2 layers selected').count(), 'marquee selects both rectangles')

  // Ctrl+G groups, Ctrl+Shift+G ungroups.
  await canvas.press('ctrl-g')
  await sleep(400)
  assert((await app.getByText('Group').count()) > 0, 'Ctrl+G creates a group')
  await shot('04-grouped')
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
  await app.getByTestId('layer-rename').fill('Cards')
  await app.getByTestId('layer-rename').press('enter')
  await sleep(400)
  assert(await app.getByText('Cards').count(), 'layer renamed from the layers panel')
  await shot('08-renamed')

  // Inspector: scrub the gap label (2 px per step): 20 px = +10 per gap, the hugging row grows by 20; one undo step.
  const width = async () => Number((await app.getByTestId('size-label').textContent()).split(' ')[0])
  const w0 = await width()
  const gap = await app.getByTestId('inspector-gap-scrub').center()
  await app.mouse.down(gap)
  for (let i = 1; i <= 5; i++) await app.mouse.move({ x: gap.x + i * 4, y: gap.y }, { pressedButton: 0 })
  await app.mouse.up({ x: gap.x + 20, y: gap.y })
  await sleep(400)
  await shot('09-scrubbed-gap')
  const w1 = await width()
  assert(w1 === w0 + 20, `scrubbing the gap 20 px widens the row by 2 × 10 (${w0} → ${w1})`)
  await canvas.press('ctrl-z')
  await sleep(400)
  assert((await width()) === w0, 'one undo step reverts the whole scrub')
  // Up arrow ×3 in the gap field: +3 per gap; leaving the field and one undo revert all three.
  for (let i = 0; i < 3; i++) {
    await app.getByTestId('inspector-gap').press('up')
    await sleep(120)
  }
  await sleep(400)
  const wa = await width()
  await shot('09b-arrow-gap')
  assert(wa === w0 + 6, `arrow keys step the gap (${w0} → ${wa})`)
  await canvas.press('ctrl-z')
  await sleep(400)
  assert((await width()) === w0, 'arrow steps in one focus session undo together')

  // Copy / paste: the pasted frame gets selected; undo removes it in one step.
  await canvas.press('ctrl-c')
  await canvas.press('ctrl-v')
  await sleep(400)
  assert((await app.getByText('Cards').count()) >= 2, 'Ctrl+V pastes a copy')
  await canvas.press('ctrl-z')
  await sleep(400)

  // Corner-radius handles on a large rectangle: 12 px in from each corner at radius 0.
  await app.getByTestId('tool-rectangle').click()
  await app.mouse.drag(at(600, 420), at(780, 580), { steps: 5 })
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
  await canvas.press('escape')
  await sleep(200)

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
  await shot('10-final')
  console.log('editor-e2e ok — screenshots in', out)
} finally {
  await app.close()
}
