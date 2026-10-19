import { useMemo } from 'react'
import { tryCall } from './core'
import { nodeIcon, type IconName } from './icons'
import { turnAroundCenter } from './geometry'
import { C } from './theme'
import { Button, Field, FocusRing, Icon, IconButton, NumField, Row, Section, Segmented, Swatch, T, Toggle, useFieldRing, type Toast } from './ui'

type Props = { version: number; selection: string[]; select: (ids: string[]) => void; toast: Toast }
type Info = { id: string; node: any; box: { w: number; h: number } | null; local: { bx: number; by: number } | null; parentLayout: string; variables: Record<string, any> }

const str = (v: unknown) => (v === undefined || v === null ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v))
const isMode = (v: unknown, mode: string) => typeof v === 'string' && v.startsWith(mode)
let clicks = 0

const ALIGNS: { dir: string; icon: IconName; label: string }[] = [
  { dir: 'left', icon: 'alignLeft', label: 'Align left (Alt+A)' },
  { dir: 'hcenter', icon: 'alignHCenter', label: 'Align horizontal centers (Alt+H)' },
  { dir: 'right', icon: 'alignRight', label: 'Align right (Alt+D)' },
  { dir: 'top', icon: 'alignTop', label: 'Align top (Alt+W)' },
  { dir: 'vcenter', icon: 'alignVCenter', label: 'Align vertical centers (Alt+V)' },
  { dir: 'bottom', icon: 'alignBottom', label: 'Align bottom (Alt+S)' },
]

/** Numbers stay numbers; sizing keywords and $variables stay strings; JSON for arrays/objects. */
function parse(v: string): unknown {
  const t = v.trim()
  if (t === '') return null
  if (/^-?\d+(\.\d+)?$/.test(t)) return Number(t)
  if (t.startsWith('[') || t.startsWith('{')) {
    try { return JSON.parse(t) } catch { return t }
  }
  return t
}

function solidColor(fill: unknown, vars: Record<string, any>): string | null {
  const f = Array.isArray(fill) ? fill[0] : fill
  let c = typeof f === 'string' ? f : f && typeof f === 'object' && (f as any).type === 'color' ? (f as any).color : null
  if (typeof c === 'string' && c.startsWith('$')) {
    const v = vars[c.slice(1)]?.value
    c = Array.isArray(v) ? v[0]?.value : v
  }
  return typeof c === 'string' && c.startsWith('#') ? c.slice(0, 7) : null
}

const PAINT_TYPES = ['color', 'gradient', 'image', 'mesh_gradient', 'shader']

/** A paint the renderer can draw: empty (none), #RGB / #RRGGBB / #RRGGBBAA, a $variable that exists, or a JSON paint (or list of them) with a known type. */
function validPaint(v: string, vars: Record<string, any>): boolean {
  const t = v.trim()
  if (t === '' || /^#([0-9a-f]{3}|[0-9a-f]{6}|[0-9a-f]{8})$/i.test(t)) return true
  if (t.startsWith('$')) return Object.prototype.hasOwnProperty.call(vars, t.slice(1))
  const p = parse(t)
  const one = (f: any): boolean => {
    if (typeof f === 'string') return f.startsWith('#') || f.startsWith('$')
    if (!f || typeof f !== 'object' || Array.isArray(f)) return false
    return (f.type ?? 'color') === 'color' ? typeof f.color === 'string' : PAINT_TYPES.includes(f.type)
  }
  return Array.isArray(p) ? p.every(one) : typeof p === 'object' && one(p)
}

const BAD_VALUE = 'Unable to apply that value. Check its format, for example #FF6A3D or 24, and try again.'
const EFFECT_TYPES = ['shadow', 'blur', 'background_blur']
/** A `$variable` that exists and has one of `types`. */
const isVar = (t: unknown, vars: Record<string, any>, types: string[] = ['number']) =>
  typeof t === 'string' && t.startsWith('$') && Object.prototype.hasOwnProperty.call(vars, t.slice(1)) && types.includes(vars[t.slice(1)]?.type)
const length = (v: unknown) => typeof v === 'number' && Number.isFinite(v) && v >= 0
const lengthVar = (vars: Record<string, any>) => (v: unknown) => length(v) || isVar(v, vars)

/** Per-side lengths as a number, [v, h] or [t, r, b, l]: typed CSS-style ("10 20", "1,2,3,4") or as a JSON array. `null` clears it, `undefined` rejects it. */
function sides(v: string, vars: Record<string, any>, counts: number[]): unknown {
  const t = v.trim()
  if (t === '') return null
  if (isVar(t, vars)) return t
  let p: unknown = t.startsWith('[') ? parse(t) : t.split(/[\s,]+/).filter(Boolean).map((x) => (/^\d+(\.\d+)?$/.test(x) ? Number(x) : NaN))
  if (!Array.isArray(p) || !p.every(lengthVar(vars)) || !counts.includes(p.length)) return undefined
  return p.length === 1 ? p[0] : p
}

/** A non-negative number, a $variable that exists, or empty to clear. */
function lengthOf(v: string, vars: Record<string, any>): unknown {
  const t = v.trim()
  return t === '' ? null : isVar(t, vars) ? t : /^\d+(\.\d+)?$/.test(t) ? Number(t) : undefined
}

/** Stroke width: a length, or JSON per-side widths {top, right, bottom, left}. */
function strokeWidthOf(v: string, vars: Record<string, any>): unknown {
  const p = lengthOf(v, vars)
  if (p !== undefined) return p
  const o = parse(v)
  return o && typeof o === 'object' && !Array.isArray(o) && Object.keys(o).length > 0 && Object.keys(o).every((k) => ['top', 'right', 'bottom', 'left'].includes(k)) && Object.values(o).every(lengthVar(vars)) ? o : undefined
}

/** Font weight: normal, bold, a number from 1 to 1000, a $variable that exists, or empty for the default. */
function fontWeightOf(v: string, vars: Record<string, any>): unknown {
  const t = v.trim()
  return t === '' ? null : isVar(t, vars, ['number', 'string']) || t === 'normal' || t === 'bold' || (/^\d+$/.test(t) && Number(t) >= 1 && Number(t) <= 1000) ? t : undefined
}

/** An effect object (or non-empty list of them) of a type the renderer knows, with fields it can read: lengths for blur / radius / offset, a hex or $color for the shadow color. */
function effectOf(v: string, vars: Record<string, any>): unknown {
  const t = v.trim()
  if (t === '') return null
  const p = parse(t)
  const optional = (x: unknown, ok: (x: unknown) => boolean) => x === undefined || ok(x)
  const len = (x: unknown) => lengthVar(vars)(x)
  const coord = (x: unknown) => (typeof x === 'number' && Number.isFinite(x)) || isVar(x, vars)
  const hue = (x: unknown) => typeof x === 'string' && (/^#([0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/i.test(x.trim()) || isVar(x.trim(), vars, ['color']))
  const one = (e: any) => {
    if (!e || typeof e !== 'object' || Array.isArray(e) || !EFFECT_TYPES.includes(e.type)) return false
    if (e.type !== 'shadow') return optional(e.radius, len)
    const o = e.offset
    const offset = o === undefined || (!!o && typeof o === 'object' && !Array.isArray(o) && optional(o.x, coord) && optional(o.y, coord))
    return optional(e.blur, len) && optional(e.color, hue) && offset
  }
  return Array.isArray(p) ? (p.length > 0 && p.every(one) ? p : undefined) : one(p) ? p : undefined
}

function TextContent({ value, onSubmit }: { value: string; onSubmit: (v: string) => void }) {
  const field = useFieldRing()
  return (
    <div onMouseDown={field.onMouseDown} style={{ position: 'relative', borderRadius: 6, backgroundColor: C.panel2, borderWidth: 1, borderColor: C.border, padding: 6 }}>
      <textarea
        ref={field.ref}
        aria-label="Text content"
        value={value}
        minRows={2}
        maxRows={8}
        onChange={() => {}}
        onFocus={field.onFocus}
        onBlur={field.onBlur}
        onSubmit={(e) => onSubmit(e.value ?? '')}
        style={{ fontSize: 12, color: C.text, width: '100%' }}
      />
      {field.ring ? <FocusRing /> : null}
    </div>
  )
}

export function Inspector({ version, selection, select, toast }: Props) {
  const infos = useMemo(() => selection.map((id) => ({ id, ...tryCall('node', { id }) }) as Info).filter((i) => i.node), [selection, version])
  const update = (props: Record<string, unknown>, coalesce?: string) => {
    const ok = tryCall('update', { ids: selection, props, coalesce }) !== null
    if (!ok) toast(BAD_VALUE, true)
    return ok
  }
  /** Free-text commit: a value the renderer cannot use reverts the field instead of being stored. */
  const setChecked = (prop: string, check: (v: string) => unknown) => (v: string) => {
    const value = check(v)
    if (value !== undefined) return update({ [prop]: value })
    toast(BAD_VALUE, true)
    return false
  }
  /** Fill / stroke commit: a color the renderer cannot read would leave the layer unpainted, so reject it. */
  const setPaint = (prop: 'fill' | 'stroke', vars: Record<string, any>) => (v: string) => {
    if (validPaint(v, vars)) return void update({ [prop]: parse(v) })
    toast('Unable to apply that color. Use a hex such as #FF6A3D, a $variable, or leave it empty for none.', true)
    return false
  }
  /** Different props per layer, still one undo step. */
  const perNode = (make: (i: Info) => Record<string, unknown> | null, key = `inspector:${++clicks}`) => {
    for (const i of infos) {
      const props = make(i)
      if (props) tryCall('update', { ids: [i.id], props, coalesce: key })
    }
  }
  const exportAs = (format: string) => {
    const files = tryCall<string[]>('export', { ids: selection, format, path: '', opts: { scale: 2 } })
    if (files) toast(`Exported ${files.join(', ')}`)
    else toast('Unable to export. Check that the export folder is writable and try again.', true)
  }

  if (!selection.length) {
    return (
      <div style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 10 }}>
        <T color={C.muted}>Nothing selected</T>
        <T size={11} color={C.ghost}>Click a layer on the canvas or drag a marquee. Double-click drills into groups or edits text. Ctrl+click selects the deepest layer.</T>
        <T size={11} color={C.ghost}>Tools: V move · F/A frame · R rectangle · O ellipse · T text · P pen · H hand · Space+drag pan · Ctrl+wheel zoom · Shift+1 fit</T>
        <T size={11} color={C.ghost}>Ctrl+G group · Ctrl+Alt+G frame · Ctrl+Shift+G ungroup · Shift+A auto layout · Alt+W/A/S/D/H/V align · [ ] order · Alt-drag duplicates · Ctrl holds off snapping</T>
      </div>
    )
  }
  if (!infos.length) {
    return (
      <div style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 10 }}>
        <T color={C.text}>{`Instance child ${selection[0]}`}</T>
        <Row>
          <Button icon="copy" onClick={() => tryCall('duplicate', { ids: selection })}>Duplicate</Button>
          <Button icon="trash" danger onClick={() => { tryCall('delete', { ids: selection }); select([]) }}>Delete</Button>
        </Row>
      </div>
    )
  }

  const multi = infos.length > 1
  const n = infos[0].node
  /** A hidden layer has no render box; its own numeric size stands in. */
  const dimOf = (i: Info, axis: 'width' | 'height') => i.box?.[axis === 'width' ? 'w' : 'h'] ?? (typeof i.node[axis] === 'number' ? i.node[axis] : 0)
  const box = { w: dimOf(infos[0], 'width'), h: dimOf(infos[0], 'height') }
  const vars = infos[0].variables ?? {}
  /** Shared numeric value, flagged as mixed when the selection disagrees. */
  const numOf = (get: (i: Info) => unknown) => {
    const vals = infos.map((i) => Number(get(i) ?? 0) || 0)
    return { value: vals[0], mixed: vals.some((v) => Math.abs(v - vals[0]) > 1e-9) }
  }
  const all = (pred: (i: Info) => boolean) => infos.every(pred)
  const inFlow = (i: Info) => i.parentLayout !== 'none' && i.node.layoutPosition !== 'absolute'
  const isFrame = !multi && n.type === 'frame'
  const fillValues = infos.map((i) => str(i.node.fill))
  const fillMixed = fillValues.some((v) => v !== fillValues[0])
  const fill = fillMixed ? null : solidColor(n.fill, vars)
  const stroke = solidColor(n.stroke, vars)

  // Rotation turns around the center like the canvas handle: a free layer's origin moves so the center stays put.
  // A group's box starts (bx, by) past its origin.
  const setRotation = (v: number, key: string) => perNode((i) => {
    const props: Record<string, unknown> = { rotation: v || null }
    if (!inFlow(i) && i.box) {
      const [fx, fy] = [!!i.node.flipX, !!i.node.flipY]
      ;[props.x, props.y] = turnAroundCenter(Number(i.node.x ?? 0), Number(i.node.y ?? 0), i.box.w, i.box.h, Number(i.node.rotation ?? 0), v, fx, fy, i.local?.bx, i.local?.by)
    }
    return props
  }, key)
  // W/H: typing a number makes that axis fixed; auto text becomes fixed width (height: fixed size). A group is the union of its children and has no size to set.
  const setSize = (axis: 'width' | 'height', v: number, key: string) => perNode((i) => {
    if (i.node.type === 'group') return null
    const p: Record<string, unknown> = { [axis]: Math.max(0, Math.round(v * 100) / 100) }
    if (i.node.type === 'text') {
      if (axis === 'height') Object.assign(p, { textGrowth: 'fixed-width-height', width: i.node.width ?? Math.round(i.box?.w ?? 0) })
      else if ((i.node.textGrowth ?? 'auto') === 'auto') p.textGrowth = 'fixed-width'
    }
    return p
  }, key)
  // Hug / Fill: on = that mode, off = fixed at the current size. Text leaves auto growth first.
  const sizing = (axis: 'width' | 'height', mode: string, on: boolean) => perNode((i) => {
    const size = Math.round(dimOf(i, axis))
    const p: Record<string, unknown> = { [axis]: on ? mode : size }
    if (i.node.type === 'text' && on) {
      if (axis === 'height') Object.assign(p, { textGrowth: 'fixed-width-height', width: i.node.width ?? Math.round(i.box?.w ?? 0) })
      else if ((i.node.textGrowth ?? 'auto') === 'auto') p.textGrowth = 'fixed-width'
    }
    return p
  })
  const canHug = all((i) => i.node.type === 'frame' && i.node.layout !== 'none')
  // A group's size is its children's union, so it cannot fill either.
  const canFill = all((i) => inFlow(i) && i.node.type !== 'group')
  const hugW = all((i) => isMode(i.node.width, 'fit_content'))
  const hugH = all((i) => isMode(i.node.height, 'fit_content'))
  const fillW = all((i) => isMode(i.node.width, 'fill_container'))
  const fillH = all((i) => isMode(i.node.height, 'fill_container'))

  return (
    <div style={{ display: 'flex', flexDirection: 'column', flexGrow: 1, minHeight: 0, overflowY: 'scroll' }}>
      <div style={{ display: 'flex', flexDirection: 'row', alignItems: 'center', gap: 8, padding: 14, borderBottomWidth: 1, borderColor: C.border }}>
        <Icon name={multi ? 'layers' : nodeIcon(n.type, n.reusable)} color={!multi && (n.reusable || n.type === 'ref') ? C.component : C.muted} />
        {multi
          ? <T color={C.text}>{`${infos.length} layers selected`}</T>
          : <Field name="Layer name" value={str(n.name)} placeholder={n.type} onCommit={(v) => v.trim() ? void update({ name: v }) : false} testId="inspector-name" />}
      </div>

      <Section title="Position">
        <Row gap={2}>
          {ALIGNS.map((a) => <IconButton key={a.dir} icon={a.icon} title={a.label} testId={`align-${a.dir}`} size={26} onClick={() => tryCall('align', { ids: selection, dir: a.dir })} />)}
        </Row>
        {!infos.some(inFlow) ? (
          <Row>
            <NumField label="X" name="X position" {...numOf((i) => i.node.x)} onCommit={(v, k) => update({ x: v }, k)} testId="inspector-x" />
            <NumField label="Y" name="Y position" {...numOf((i) => i.node.y)} onCommit={(v, k) => update({ y: v }, k)} testId="inspector-y" />
          </Row>
        ) : null}
        <Row>
          <NumField label="↻" name="Rotation" {...numOf((i) => i.node.rotation)} onCommit={(v, k) => setRotation(v, k)} testId="inspector-rotation" />
          <NumField label="α" name="Opacity" {...numOf((i) => (i.node.opacity ?? 1) * 100)} onCommit={(v, k) => update({ opacity: v >= 100 ? null : Math.max(0, v) / 100 }, k)} testId="inspector-opacity" />
        </Row>
      </Section>

      <Section title={!multi && inFlow(infos[0]) ? 'Size (auto layout child)' : 'Size'}>
        {all((i) => i.node.type === 'group') ? null : (
          <Row>
            <NumField label="W" name="Width" {...numOf((i) => dimOf(i, 'width'))} onCommit={(v, k) => setSize('width', v, k)} testId="inspector-w" />
            <NumField label="H" name="Height" {...numOf((i) => dimOf(i, 'height'))} onCommit={(v, k) => setSize('height', v, k)} testId="inspector-h" />
          </Row>
        )}
        {canHug || canFill ? (
          <Row gap={6}>
            {canHug ? <Toggle label="Hug W" on={hugW} onChange={(on) => sizing('width', 'fit_content', on)} testId="hug-width" /> : null}
            {canHug ? <Toggle label="Hug H" on={hugH} onChange={(on) => sizing('height', 'fit_content', on)} testId="hug-height" /> : null}
            {canFill ? <Toggle label="Fill W" on={fillW} onChange={(on) => sizing('width', 'fill_container', on)} testId="fill-width" /> : null}
            {canFill ? <Toggle label="Fill H" on={fillH} onChange={(on) => sizing('height', 'fill_container', on)} testId="fill-height" /> : null}
          </Row>
        ) : null}
        {!multi ? <T size={10} color={C.ghost}>{`${infos[0].box ? 'Rendered' : 'Hidden'} ${Math.round(box.w)} × ${Math.round(box.h)} · drag a label to scrub, ↑/↓ steps`}</T> : null}
      </Section>

      {isFrame ? (
        <Section title="Layout">
          <Segmented
            label="Layout"
            value={(n.layout ?? 'horizontal') as string}
            onChange={(v) => (v === 'none' ? tryCall('remove_layout', { ids: selection }) : n.layout === 'none' ? update({ layout: v }) : tryCall('set_layout', { ids: selection, layout: v }))}
            options={[{ value: 'none', label: 'Free' }, { value: 'horizontal', label: 'Row' }, { value: 'vertical', label: 'Column' }]}
          />
          {n.layout !== 'none' ? (
            <>
              <Row>
                <NumField label="G" name="Gap" value={Number(n.gap ?? 0) || 0} onCommit={(v, k) => update({ gap: v }, k)} testId="inspector-gap" />
                <Field label="P" name="Padding" value={str(n.padding ?? 0)} onCommit={setChecked('padding', (v) => sides(v, vars, [1, 2, 4]))} testId="inspector-padding" />
              </Row>
              <Segmented
                label="Distribute"
                value={(n.justifyContent ?? 'start') as string}
                onChange={(v) => update({ justifyContent: v })}
                options={[{ value: 'start', label: 'Start' }, { value: 'center', label: 'Mid' }, { value: 'end', label: 'End' }, { value: 'space_between', label: 'Between' }]}
              />
              <Segmented
                label="Align"
                value={(n.alignItems ?? 'start') as string}
                onChange={(v) => update({ alignItems: v })}
                options={[{ value: 'start', label: 'Top/Left' }, { value: 'center', label: 'Center' }, { value: 'end', label: 'Bottom/Right' }]}
              />
            </>
          ) : (
            <Button onClick={() => tryCall('add_layout', { ids: selection })}>Add auto layout (Shift+A)</Button>
          )}
          <Segmented label="Overflow" value={n.clip ? 'clip' : 'visible'} onChange={(v) => update({ clip: v === 'clip' })} options={[{ value: 'visible', label: 'Overflow visible' }, { value: 'clip', label: 'Clip content' }]} />
        </Section>
      ) : null}

      {!multi && n.type === 'text' ? (
        <Section title="Text">
          <TextContent value={str(n.content)} onSubmit={(v) => update({ content: v })} />
          <T size={10} color={C.ghost}>Enter applies · Shift+Enter new line</T>
          <Field name="Font family" value={str(n.fontFamily ?? 'Inter')} onCommit={(v) => update({ fontFamily: v })} />
          <Row>
            <NumField label="S" name="Font size" value={Number(n.fontSize ?? 14) || 14} onCommit={(v, k) => update({ fontSize: Math.max(1, v) }, k)} testId="inspector-font-size" />
            <Field label="W" name="Font weight" value={str(n.fontWeight ?? '400')} onCommit={setChecked('fontWeight', (v) => fontWeightOf(v, vars))} testId="inspector-font-weight" />
          </Row>
          <Row>
            <Field label="L" name="Line height" value={str(n.lineHeight ?? '')} onCommit={setChecked('lineHeight', (v) => lengthOf(v, vars))} testId="inspector-line-height" />
            <NumField label="K" name="Letter spacing" value={Number(n.letterSpacing ?? 0) || 0} step={0.1} onCommit={(v, k) => update({ letterSpacing: v || null }, k)} />
          </Row>
          <Segmented
            label="Text align"
            value={(n.textAlign ?? 'left') as string}
            onChange={(v) => update({ textAlign: v })}
            options={[{ value: 'left', label: 'Left' }, { value: 'center', label: 'Center' }, { value: 'right', label: 'Right' }]}
          />
          <Segmented
            label="Text resizing"
            value={(n.textGrowth ?? 'auto') as string}
            onChange={(v) => update(v === 'auto' ? { textGrowth: null, width: null, height: null } : { textGrowth: v, width: n.width ?? Math.round(box.w) })}
            options={[{ value: 'auto', label: 'Auto' }, { value: 'fixed-width', label: 'Wrap' }, { value: 'fixed-width-height', label: 'Fixed' }]}
          />
        </Section>
      ) : null}

      {!multi && n.type === 'icon' ? (
        <Section title="Icon">
          <Field name="Icon name" value={str(n.icon)} onCommit={(v) => update({ icon: v })} />
          <Field name="Icon library" value={str(n.library ?? 'lucide')} onCommit={(v) => update({ library: v })} />
        </Section>
      ) : null}

      {all((i) => i.node.type !== 'group') ? (
        <Section title="Fill">
          <Row>
            {fill ? <Swatch color={fill} /> : null}
            <Field value={fillMixed ? '' : fillValues[0]} mono onCommit={setPaint('fill', vars)} name="Fill" placeholder={fillMixed ? 'Mixed' : '#FFFFFF'} testId="inspector-fill" />
          </Row>
        </Section>
      ) : null}

      {!multi && ['frame', 'rectangle', 'ellipse', 'polygon', 'path'].includes(n.type) ? (
        <Section title="Stroke & corners">
          <Row>
            {stroke ? <Swatch color={stroke} /> : null}
            <Field name="Stroke color" value={str(n.stroke)} mono onCommit={setPaint('stroke', vars)} placeholder="#000000" />
            <Field name="Stroke width" value={str(n.strokeWidth ?? '')} width={58} onCommit={setChecked('strokeWidth', (v) => strokeWidthOf(v, vars))} placeholder="1" testId="inspector-stroke-width" />
          </Row>
          {n.type !== 'ellipse' && n.type !== 'path' ? (
            typeof (n.cornerRadius ?? 0) === 'number'
              ? <NumField label="R" name="Corner radius" value={n.cornerRadius ?? 0} onCommit={(v, k) => update({ cornerRadius: Math.max(0, v) || null }, k)} testId="inspector-radius" />
              : <Field label="R" name="Corner radius" value={str(n.cornerRadius)} onCommit={setChecked('cornerRadius', (v) => sides(v, vars, [1, 4]))} testId="inspector-radii" />
          ) : null}
        </Section>
      ) : null}

      {!multi ? (
        <Section title="Effect">
          <Field name="Effect" value={str(n.effect)} mono onCommit={setChecked('effect', (v) => effectOf(v, vars))} testId="inspector-effect" placeholder='{"type":"shadow","blur":12,"offset":{"x":0,"y":4},"color":"#00000033"}' />
        </Section>
      ) : null}

      <Section title="Layer">
        {!multi ? (
          <Row gap={6}>
            <Button onClick={() => update({ reusable: !n.reusable })}>{n.reusable ? 'Detach component' : 'Make component'}</Button>
            <Button testId="inspector-visibility" onClick={() => update({ enabled: n.enabled === false ? null : false })}>{n.enabled === false ? 'Show' : 'Hide'}</Button>
          </Row>
        ) : null}
        <Row gap={6}>
          <Button icon="copy" onClick={() => { const r = tryCall('duplicate', { ids: selection }); if (r?.ids) select(r.ids) }}>Duplicate</Button>
          <Button icon="trash" danger onClick={() => { tryCall('delete', { ids: selection }); select([]) }}>Delete</Button>
        </Row>
        <Row gap={6}>
          <Button onClick={() => tryCall('reorder', { ids: selection, dir: 'up' })}>Bring forward</Button>
          <Button onClick={() => tryCall('reorder', { ids: selection, dir: 'down' })}>Send backward</Button>
        </Row>
      </Section>

      <Section title="Export">
        <Row gap={6}>
          <Button icon="download" onClick={() => exportAs('png')}>PNG</Button>
          <Button onClick={() => exportAs('jpeg')}>JPEG</Button>
          <Button onClick={() => exportAs('webp')}>WebP</Button>
        </Row>
        <Row gap={6}>
          <Button onClick={() => exportAs('svg')}>SVG</Button>
          <Button onClick={() => exportAs('html-css')}>HTML</Button>
        </Row>
      </Section>
    </div>
  )
}
