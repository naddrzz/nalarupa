import { useMemo } from 'react'
import { tryCall } from './core'
import { nodeIcon, type IconName } from './icons'
import { C } from './theme'
import { Button, Field, Icon, IconButton, NumField, Row, Section, Segmented, Swatch, T, Toggle, type Toast } from './ui'

type Props = { version: number; selection: string[]; select: (ids: string[]) => void; toast: Toast }
type Info = { id: string; node: any; box: { w: number; h: number } | null; parentLayout: string; variables: Record<string, any> }

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

export function Inspector({ version, selection, select, toast }: Props) {
  const infos = useMemo(() => selection.map((id) => ({ id, ...tryCall('node', { id }) }) as Info).filter((i) => i.node), [selection, version])
  const update = (props: Record<string, unknown>, coalesce?: string) => {
    if (tryCall('update', { ids: selection, props, coalesce }) === null) toast('Unable to apply that value. Check its format, for example #FF6A3D or 24, and try again.', true)
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
        <T size={11} color={C.ghost}>Tools: V move · F/A frame · R rectangle · O ellipse · T text · H hand · Space+drag pan · Ctrl+wheel zoom · Shift+1 fit</T>
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

  // W/H: typing a number makes that axis fixed; auto text becomes fixed width (height: fixed size).
  const setSize = (axis: 'width' | 'height', v: number, key: string) => perNode((i) => {
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
  const canFill = all(inFlow)
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
          : <Field name="Layer name" value={str(n.name)} placeholder={n.type} onCommit={(v) => update({ name: v })} testId="inspector-name" />}
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
          <NumField label="↻" name="Rotation" {...numOf((i) => i.node.rotation)} onCommit={(v, k) => update({ rotation: v || null }, k)} testId="inspector-rotation" />
          <NumField label="α" name="Opacity" {...numOf((i) => (i.node.opacity ?? 1) * 100)} onCommit={(v, k) => update({ opacity: v >= 100 ? null : Math.max(0, v) / 100 }, k)} testId="inspector-opacity" />
        </Row>
      </Section>

      <Section title={!multi && inFlow(infos[0]) ? 'Size (auto layout child)' : 'Size'}>
        <Row>
          <NumField label="W" name="Width" {...numOf((i) => dimOf(i, 'width'))} onCommit={(v, k) => setSize('width', v, k)} testId="inspector-w" />
          <NumField label="H" name="Height" {...numOf((i) => dimOf(i, 'height'))} onCommit={(v, k) => setSize('height', v, k)} testId="inspector-h" />
        </Row>
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
            onChange={(v) => (v === 'none' ? tryCall('remove_layout', { ids: selection }) : update({ layout: v }))}
            options={[{ value: 'none', label: 'Free' }, { value: 'horizontal', label: 'Row' }, { value: 'vertical', label: 'Column' }]}
          />
          {n.layout !== 'none' ? (
            <>
              <Row>
                <NumField label="G" name="Gap" value={Number(n.gap ?? 0) || 0} onCommit={(v, k) => update({ gap: v }, k)} testId="inspector-gap" />
                <Field label="P" name="Padding" value={str(n.padding ?? 0)} onCommit={(v) => update({ padding: parse(v) })} />
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
          <div style={{ borderRadius: 6, backgroundColor: C.panel2, borderWidth: 1, borderColor: C.border, padding: 6 }}>
            <textarea
              aria-label="Text content"
              value={str(n.content)}
              minRows={2}
              maxRows={8}
              onChange={() => {}}
              onBlur={() => {}}
              onSubmit={(e) => update({ content: e.value ?? '' })}
              style={{ fontSize: 12, color: C.text, width: '100%' }}
            />
          </div>
          <T size={10} color={C.ghost}>Enter applies · Shift+Enter new line</T>
          <Field name="Font family" value={str(n.fontFamily ?? 'Inter')} onCommit={(v) => update({ fontFamily: v })} />
          <Row>
            <NumField label="S" name="Font size" value={Number(n.fontSize ?? 14) || 14} onCommit={(v, k) => update({ fontSize: Math.max(1, v) }, k)} testId="inspector-font-size" />
            <Field label="W" name="Font weight" value={str(n.fontWeight ?? '400')} onCommit={(v) => update({ fontWeight: v })} />
          </Row>
          <Row>
            <Field label="L" name="Line height" value={str(n.lineHeight ?? '')} onCommit={(v) => update({ lineHeight: parse(v) })} />
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
            <Field value={fillMixed ? '' : fillValues[0]} mono onCommit={(v) => update({ fill: parse(v) })} name="Fill" placeholder={fillMixed ? 'Mixed' : '#FFFFFF'} testId="inspector-fill" />
          </Row>
        </Section>
      ) : null}

      {!multi && ['frame', 'rectangle', 'ellipse', 'polygon', 'path'].includes(n.type) ? (
        <Section title="Stroke & corners">
          <Row>
            {stroke ? <Swatch color={stroke} /> : null}
            <Field name="Stroke color" value={str(n.stroke)} mono onCommit={(v) => update({ stroke: parse(v) })} placeholder="#000000" />
            <Field name="Stroke width" value={str(n.strokeWidth ?? '')} width={58} onCommit={(v) => update({ strokeWidth: parse(v) })} placeholder="1" />
          </Row>
          {n.type !== 'ellipse' && n.type !== 'path' ? (
            typeof (n.cornerRadius ?? 0) === 'number'
              ? <NumField label="R" name="Corner radius" value={n.cornerRadius ?? 0} onCommit={(v, k) => update({ cornerRadius: Math.max(0, v) || null }, k)} testId="inspector-radius" />
              : <Field label="R" name="Corner radius" value={str(n.cornerRadius)} onCommit={(v) => update({ cornerRadius: parse(v) })} testId="inspector-radii" />
          ) : null}
        </Section>
      ) : null}

      {!multi ? (
        <Section title="Effect">
          <Field name="Effect" value={str(n.effect)} mono onCommit={(v) => update({ effect: parse(v) })} placeholder='{"type":"shadow","blur":12,"offset":{"x":0,"y":4},"color":"#00000033"}' />
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
