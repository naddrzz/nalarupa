// Writes core/tests/figma-clipboard.html, a Figma clipboard (`text/html`) for the paste tests:
// `bun scripts/figma-fixture.ts`. Like Figma's, it is a "fig-kiwi" archive (a kiwi schema, raw
// deflate, and a kiwi message, zstd) base64'd in a data-buffer span. The schema is a subset of
// Figma's, with its type and field names; the message is a card frame with auto layout, text, a
// gradient, a vector, an ellipse, an instance of a button component with a text override, and a
// color variable whose mode the card switches to "Dark".
import { join } from 'node:path'

type Def = { name: string; kind: 0 | 1 | 2; fields: [string, string, boolean?][] }
const ENUM = 0, STRUCT = 1, MESSAGE = 2
const enumDef = (name: string, values: string[]): Def => ({ name, kind: ENUM, fields: values.map((v) => [v, '']) })
const defs: Def[] = [
  enumDef('MessageType', ['JOIN_START', 'NODE_CHANGES']),
  enumDef('NodeType', ['NONE', 'DOCUMENT', 'CANVAS', 'GROUP', 'FRAME', 'BOOLEAN_OPERATION', 'VECTOR', 'STAR', 'LINE', 'ELLIPSE', 'RECTANGLE',
    'REGULAR_POLYGON', 'ROUNDED_RECTANGLE', 'TEXT', 'SLICE', 'SYMBOL', 'INSTANCE', 'STICKY', 'SHAPE_WITH_TEXT', 'CONNECTOR', 'CODE_BLOCK',
    'WIDGET', 'STAMP', 'MEDIA', 'HIGHLIGHT', 'SECTION', 'SECTION_OVERLAY', 'WASHI_TAPE', 'VARIABLE', 'VARIABLE_SET']),
  enumDef('PaintType', ['SOLID', 'GRADIENT_LINEAR', 'GRADIENT_RADIAL', 'GRADIENT_ANGULAR', 'GRADIENT_DIAMOND', 'IMAGE', 'EMOJI', 'VIDEO']),
  enumDef('BlendMode', ['PASS_THROUGH', 'NORMAL', 'DARKEN', 'MULTIPLY', 'LINEAR_BURN', 'COLOR_BURN', 'LIGHTEN', 'SCREEN', 'LINEAR_DODGE',
    'COLOR_DODGE', 'OVERLAY', 'SOFT_LIGHT', 'HARD_LIGHT', 'DIFFERENCE', 'EXCLUSION', 'HUE', 'SATURATION', 'COLOR', 'LUMINOSITY']),
  enumDef('EffectType', ['INNER_SHADOW', 'DROP_SHADOW', 'FOREGROUND_BLUR', 'BACKGROUND_BLUR']),
  enumDef('StrokeAlign', ['CENTER', 'INSIDE', 'OUTSIDE']),
  enumDef('StrokeJoin', ['MITER', 'BEVEL', 'ROUND']),
  enumDef('StrokeCap', ['NONE', 'ROUND', 'SQUARE']),
  enumDef('StackMode', ['NONE', 'HORIZONTAL', 'VERTICAL']),
  enumDef('StackSize', ['FIXED', 'RESIZE_TO_FIT', 'RESIZE_TO_FIT_WITH_IMPLICIT_SIZE']),
  enumDef('StackJustify', ['MIN', 'CENTER', 'MAX', 'SPACE_EVENLY', 'SPACE_BETWEEN']),
  enumDef('StackAlign', ['MIN', 'CENTER', 'MAX', 'BASELINE']),
  enumDef('StackCounterAlign', ['MIN', 'CENTER', 'MAX', 'STRETCH', 'AUTO', 'BASELINE']),
  enumDef('StackPositioning', ['AUTO', 'ABSOLUTE']),
  enumDef('TextAutoResize', ['NONE', 'WIDTH_AND_HEIGHT', 'HEIGHT']),
  enumDef('TextAlignHorizontal', ['LEFT', 'CENTER', 'RIGHT', 'JUSTIFIED']),
  enumDef('TextAlignVertical', ['TOP', 'CENTER', 'BOTTOM']),
  enumDef('NumberUnits', ['RAW', 'PIXELS', 'PERCENT']),
  enumDef('VariableDataType', ['BOOLEAN', 'FLOAT', 'STRING', 'ALIAS', 'COLOR']),
  enumDef('VariableResolvedDataType', ['BOOLEAN', 'FLOAT', 'STRING', 'COLOR']),
  { name: 'GUID', kind: STRUCT, fields: [['sessionID', 'uint'], ['localID', 'uint']] },
  { name: 'Color', kind: STRUCT, fields: [['r', 'float'], ['g', 'float'], ['b', 'float'], ['a', 'float']] },
  { name: 'Vector', kind: STRUCT, fields: [['x', 'float'], ['y', 'float']] },
  { name: 'Matrix', kind: STRUCT, fields: [['m00', 'float'], ['m01', 'float'], ['m02', 'float'], ['m10', 'float'], ['m11', 'float'], ['m12', 'float']] },
  { name: 'ParentIndex', kind: STRUCT, fields: [['guid', 'GUID'], ['position', 'string']] },
  { name: 'FontName', kind: STRUCT, fields: [['family', 'string'], ['style', 'string'], ['postscript', 'string']] },
  { name: 'ColorStop', kind: STRUCT, fields: [['color', 'Color'], ['position', 'float']] },
  { name: 'Number', kind: MESSAGE, fields: [['value', 'float'], ['units', 'NumberUnits']] },
  { name: 'AssetRef', kind: MESSAGE, fields: [['key', 'string'], ['version', 'string']] },
  { name: 'VariableID', kind: MESSAGE, fields: [['guid', 'GUID'], ['assetRef', 'AssetRef']] },
  { name: 'VariableSetID', kind: MESSAGE, fields: [['guid', 'GUID'], ['assetRef', 'AssetRef']] },
  { name: 'VariableAnyValue', kind: MESSAGE, fields: [['boolValue', 'bool'], ['textValue', 'string'], ['floatValue', 'float'], ['alias', 'VariableID'], ['colorValue', 'Color']] },
  { name: 'VariableData', kind: MESSAGE, fields: [['value', 'VariableAnyValue'], ['dataType', 'VariableDataType'], ['resolvedDataType', 'VariableResolvedDataType']] },
  { name: 'VariableDataValuesEntry', kind: MESSAGE, fields: [['modeID', 'GUID'], ['variableData', 'VariableData']] },
  { name: 'VariableDataValues', kind: MESSAGE, fields: [['entries', 'VariableDataValuesEntry', true]] },
  { name: 'VariableSetMode', kind: MESSAGE, fields: [['id', 'GUID'], ['name', 'string']] },
  { name: 'VariableModeBySetMapEntry', kind: MESSAGE, fields: [['variableSetID', 'VariableSetID'], ['variableModeID', 'GUID']] },
  { name: 'VariableModeBySetMap', kind: MESSAGE, fields: [['entries', 'VariableModeBySetMapEntry', true]] },
  { name: 'Paint', kind: MESSAGE, fields: [['type', 'PaintType'], ['color', 'Color'], ['opacity', 'float'], ['visible', 'bool'], ['blendMode', 'BlendMode'],
    ['stops', 'ColorStop', true], ['transform', 'Matrix'], ['colorVar', 'VariableData']] },
  { name: 'Effect', kind: MESSAGE, fields: [['type', 'EffectType'], ['color', 'Color'], ['offset', 'Vector'], ['radius', 'float'], ['visible', 'bool'],
    ['blendMode', 'BlendMode'], ['spread', 'float']] },
  { name: 'TextData', kind: MESSAGE, fields: [['characters', 'string']] },
  { name: 'GUIDPath', kind: MESSAGE, fields: [['guids', 'GUID', true]] },
  { name: 'VectorData', kind: MESSAGE, fields: [['vectorNetworkBlob', 'uint'], ['normalizedSize', 'Vector']] },
  { name: 'ArcData', kind: MESSAGE, fields: [['startingAngle', 'float'], ['endingAngle', 'float'], ['innerRadius', 'float']] },
  { name: 'SymbolData', kind: MESSAGE, fields: [['symbolID', 'GUID'], ['symbolOverrides', 'NodeChange', true], ['uniformScaleFactor', 'float']] },
  { name: 'NodeChange', kind: MESSAGE, fields: [
    ['guid', 'GUID'], ['parentIndex', 'ParentIndex'], ['type', 'NodeType'], ['name', 'string'], ['visible', 'bool'], ['opacity', 'float'],
    ['size', 'Vector'], ['transform', 'Matrix'], ['fillPaints', 'Paint', true], ['strokePaints', 'Paint', true], ['strokeWeight', 'float'],
    ['strokeAlign', 'StrokeAlign'], ['strokeJoin', 'StrokeJoin'], ['strokeCap', 'StrokeCap'], ['cornerRadius', 'float'],
    ['rectangleTopLeftCornerRadius', 'float'], ['rectangleTopRightCornerRadius', 'float'], ['rectangleBottomLeftCornerRadius', 'float'],
    ['rectangleBottomRightCornerRadius', 'float'], ['effects', 'Effect', true], ['frameMaskDisabled', 'bool'], ['resizeToFit', 'bool'],
    ['stackMode', 'StackMode'], ['stackSpacing', 'float'], ['stackHorizontalPadding', 'float'], ['stackVerticalPadding', 'float'],
    ['stackPaddingRight', 'float'], ['stackPaddingBottom', 'float'], ['stackPrimarySizing', 'StackSize'], ['stackCounterSizing', 'StackSize'],
    ['stackPrimaryAlignItems', 'StackJustify'], ['stackCounterAlignItems', 'StackAlign'], ['stackChildAlignSelf', 'StackCounterAlign'],
    ['stackChildPrimaryGrow', 'float'], ['stackPositioning', 'StackPositioning'], ['textData', 'TextData'], ['fontSize', 'float'],
    ['fontName', 'FontName'], ['lineHeight', 'Number'], ['letterSpacing', 'Number'], ['textAlignHorizontal', 'TextAlignHorizontal'],
    ['textAlignVertical', 'TextAlignVertical'], ['textAutoResize', 'TextAutoResize'], ['symbolData', 'SymbolData'], ['overrideKey', 'GUID'],
    ['guidPath', 'GUIDPath'], ['vectorData', 'VectorData'], ['arcData', 'ArcData'], ['count', 'uint'], ['key', 'string'],
    ['variableSetModes', 'VariableSetMode', true], ['variableSetID', 'VariableSetID'], ['variableDataValues', 'VariableDataValues'],
    ['variableModeBySetMap', 'VariableModeBySetMap']] },
  { name: 'Blob', kind: MESSAGE, fields: [['bytes', 'byte', true]] },
  { name: 'Message', kind: MESSAGE, fields: [['type', 'MessageType'], ['sessionID', 'uint'], ['ackID', 'uint'], ['nodeChanges', 'NodeChange', true], ['blobs', 'Blob', true]] },
]

// kiwi wire format (github.com/evanw/kiwi): LEB128 varuints, zigzag varints, floats with the exponent byte first.
const BUILTIN = ['bool', 'byte', 'int', 'uint', 'float', 'string', 'int64', 'uint64']
class Out {
  b: number[] = []
  byte(v: number) { this.b.push(v & 255) }
  uint(v: number) { do { const x = v & 127; v >>>= 7; this.byte(v ? x | 128 : x) } while (v) }
  int(v: number) { this.uint(((v << 1) ^ (v >> 31)) >>> 0) }
  float(v: number) {
    const bits = new Uint32Array(new Float32Array([v]).buffer)[0]
    const r = ((bits >>> 23) | (bits << 9)) >>> 0
    if ((r & 255) === 0) return this.byte(0)
    for (let i = 0; i < 4; i++) this.byte(r >>> (i * 8))
  }
  string(s: string) { for (const x of Buffer.from(s, 'utf8')) this.byte(x); this.byte(0) }
}
const byName = new Map(defs.map((d, i) => [d.name, i]))

function schema(): Uint8Array {
  const o = new Out()
  o.uint(defs.length)
  for (const d of defs) {
    o.string(d.name)
    o.byte(d.kind)
    o.uint(d.fields.length)
    d.fields.forEach(([name, type, array], i) => {
      o.string(name)
      o.int(d.kind === ENUM ? 0 : BUILTIN.includes(type) ? ~BUILTIN.indexOf(type) : byName.get(type)!)
      o.byte(array ? 1 : 0)
      o.uint(d.kind === ENUM ? i : i + 1)
    })
  }
  return new Uint8Array(o.b)
}

function value(o: Out, type: string, v: any) {
  switch (type) {
    case 'bool': return o.byte(v ? 1 : 0)
    case 'byte': return o.byte(v)
    case 'int': return o.int(v)
    case 'uint': return o.uint(v)
    case 'float': return o.float(v)
    case 'string': return o.string(v)
  }
  const d = defs[byName.get(type)!]
  if (d.kind === ENUM) return o.uint(d.fields.findIndex(([n]) => n === v))
  d.fields.forEach(([name, ft, array], i) => {
    const x = v[name]
    if (d.kind === MESSAGE) {
      if (x === undefined) return
      o.uint(i + 1)
    }
    if (!array) return value(o, ft, x)
    o.uint(x.length)
    for (const e of x) value(o, ft, e)
  })
  if (d.kind === MESSAGE) o.uint(0)
}

// ---- the copied content ----
const g = (s: number, l: number) => ({ sessionID: s, localID: l })
const at = (x: number, y: number) => ({ m00: 1, m01: 0, m02: x, m10: 0, m11: 1, m12: y })
const rgb = (hex: string, a = 1) => ({ r: parseInt(hex.slice(1, 3), 16) / 255, g: parseInt(hex.slice(3, 5), 16) / 255, b: parseInt(hex.slice(5, 7), 16) / 255, a })
const solid = (hex: string, extra = {}) => ({ type: 'SOLID', color: rgb(hex), opacity: 1, visible: true, blendMode: 'NORMAL', ...extra })
const child = (parent: [number, number], position: string) => ({ guid: g(...parent), position })
const theme = g(3, 1), light = g(3, 10), dark = g(3, 11)
const colorVar = (v: [number, number]) => ({ value: { alias: { guid: g(...v) } }, dataType: 'ALIAS', resolvedDataType: 'COLOR' })
const modes = (l: string, d: string) => ({ entries: [light, dark].map((modeID, i) => ({ modeID, variableData: { value: { colorValue: rgb([l, d][i]) }, dataType: 'COLOR', resolvedDataType: 'COLOR' } })) })
const buttonLayout = {
  size: { x: 120, y: 40 }, stackMode: 'HORIZONTAL', stackPrimarySizing: 'RESIZE_TO_FIT_WITH_IMPLICIT_SIZE', stackCounterSizing: 'FIXED',
  stackHorizontalPadding: 16, stackPaddingRight: 16, stackVerticalPadding: 10, stackPaddingBottom: 10, stackPrimaryAlignItems: 'CENTER',
  stackCounterAlignItems: 'CENTER', cornerRadius: 8, fillPaints: [solid('#2563EB')],
}
// A check mark: vertices (0,6) (5,11) (16,0), two straight segments, no regions.
const check = new Out()
for (const v of [3, 2, 0]) for (let i = 0; i < 4; i++) check.byte(v >>> (i * 8))
const f32 = (v: number) => [...new Uint8Array(new Float32Array([v]).buffer)]
const u32 = (v: number) => [v & 255, (v >>> 8) & 255, (v >>> 16) & 255, v >>> 24]
for (const [x, y] of [[0, 6], [5, 11], [16, 0]]) check.b.push(...u32(0), ...f32(x), ...f32(y))
for (const [a, z] of [[0, 1], [1, 2]]) check.b.push(...u32(0), ...u32(a), ...f32(0), ...f32(0), ...u32(z), ...f32(0), ...f32(0))

const nodeChanges = [
  { guid: g(0, 0), type: 'DOCUMENT', name: 'Document', visible: true },
  { guid: g(0, 1), parentIndex: child([0, 0], '!'), type: 'CANVAS', name: 'Page 1', visible: true },
  { guid: g(0, 2), parentIndex: child([0, 0], '~'), type: 'CANVAS', name: 'Internal Only Canvas', visible: false },
  { guid: theme, type: 'VARIABLE_SET', name: 'Theme', variableSetModes: [{ id: light, name: 'Light' }, { id: dark, name: 'Dark' }] },
  { guid: g(3, 3), type: 'VARIABLE', name: 'on-primary', variableSetID: { guid: theme }, variableDataValues: modes('#FFFFFF', '#0F172A') },
  { guid: g(2, 1), parentIndex: child([0, 2], 'a'), type: 'SYMBOL', name: 'Button', visible: true, transform: at(0, 0), ...buttonLayout },
  { guid: g(2, 2), parentIndex: child([2, 1], 'a'), type: 'TEXT', name: 'Label', visible: true, transform: at(16, 11), size: { x: 48, y: 17 },
    textData: { characters: 'Button' }, fontName: { family: 'Inter', style: 'Semi Bold', postscript: '' }, fontSize: 14, textAutoResize: 'WIDTH_AND_HEIGHT',
    fillPaints: [solid('#FFFFFF', { colorVar: colorVar([3, 3]) })] },
  { guid: g(1, 1), parentIndex: child([0, 1], 'a'), type: 'FRAME', name: 'Card', visible: true, transform: at(40, 60), size: { x: 320, y: 230 },
    stackMode: 'VERTICAL', stackSpacing: 12, stackHorizontalPadding: 24, stackPaddingRight: 24, stackVerticalPadding: 24, stackPaddingBottom: 24,
    stackPrimarySizing: 'RESIZE_TO_FIT_WITH_IMPLICIT_SIZE', stackCounterSizing: 'FIXED', cornerRadius: 16,
    fillPaints: [solid('#FFFFFF')], strokePaints: [solid('#E5E7EB')], strokeWeight: 1, strokeAlign: 'INSIDE',
    effects: [{ type: 'DROP_SHADOW', color: rgb('#000000', 0.1), offset: { x: 0, y: 4 }, radius: 16, visible: true, blendMode: 'NORMAL' }],
    variableModeBySetMap: { entries: [{ variableSetID: { guid: theme }, variableModeID: dark }] } },
  { guid: g(1, 2), parentIndex: child([1, 1], 'a'), type: 'TEXT', name: 'Title', visible: true, transform: at(24, 24), size: { x: 172, y: 24 },
    textData: { characters: 'Hello from Figma' }, fontName: { family: 'Inter', style: 'Bold', postscript: '' }, fontSize: 20,
    textAutoResize: 'WIDTH_AND_HEIGHT', fillPaints: [solid('#111827')] },
  { guid: g(1, 3), parentIndex: child([1, 1], 'b'), type: 'TEXT', name: 'Body', visible: true, transform: at(24, 60), size: { x: 272, y: 63 },
    textData: { characters: 'Frames, auto layout, text, components and variables arrive as editable layers.' },
    fontName: { family: 'Inter', style: 'Regular', postscript: '' }, fontSize: 14, lineHeight: { value: 21, units: 'PIXELS' },
    textAutoResize: 'HEIGHT', stackChildAlignSelf: 'STRETCH', fillPaints: [solid('#4B5563')] },
  { guid: g(1, 4), parentIndex: child([1, 1], 'c'), type: 'FRAME', name: 'Row', visible: true, transform: at(24, 135), size: { x: 272, y: 40 },
    stackMode: 'HORIZONTAL', stackSpacing: 12, stackCounterAlignItems: 'CENTER', stackPrimarySizing: 'FIXED',
    stackCounterSizing: 'RESIZE_TO_FIT_WITH_IMPLICIT_SIZE', stackChildAlignSelf: 'STRETCH', frameMaskDisabled: true },
  { guid: g(1, 6), parentIndex: child([1, 4], 'a'), type: 'RECTANGLE', name: 'Swatch', visible: true, transform: at(0, 0), size: { x: 40, y: 40 },
    cornerRadius: 8, fillPaints: [{ type: 'GRADIENT_LINEAR', visible: true, opacity: 1, blendMode: 'NORMAL', transform: at(0, 0),
      stops: [{ color: rgb('#F97316'), position: 0 }, { color: rgb('#EC4899'), position: 1 }] }] },
  { guid: g(1, 5), parentIndex: child([1, 4], 'b'), type: 'INSTANCE', name: 'Button', visible: true, transform: at(52, 0), ...buttonLayout,
    symbolData: { symbolID: g(2, 1), symbolOverrides: [{ guidPath: { guids: [g(2, 2)] }, textData: { characters: 'Buy now' } }] } },
  { guid: g(1, 7), parentIndex: child([1, 4], 'c'), type: 'VECTOR', name: 'Check', visible: true, transform: at(184, 14), size: { x: 16, y: 11 },
    strokePaints: [solid('#16A34A')], strokeWeight: 2, strokeCap: 'ROUND', strokeJoin: 'ROUND', vectorData: { vectorNetworkBlob: 0 } },
  { guid: g(1, 8), parentIndex: child([1, 4], 'd'), type: 'ELLIPSE', name: 'Dot', visible: true, transform: at(212, 14), size: { x: 12, y: 12 },
    fillPaints: [solid('#22C55E')] },
]

const msg = new Out()
value(msg, 'Message', { type: 'NODE_CHANGES', sessionID: 0, ackID: 0, nodeChanges, blobs: [{ bytes: check.b }] })
const chunks = [Bun.deflateSync(schema()), Bun.zstdCompressSync(new Uint8Array(msg.b))]
const archive = Buffer.concat([Buffer.from('fig-kiwi'), Buffer.from(u32(15)), ...chunks.flatMap((c) => [Buffer.from(u32(c.length)), Buffer.from(c)])])
const meta = Buffer.from(JSON.stringify({ fileKey: 'nalarupaFixture', pasteID: 1, dataType: 'scene' })).toString('base64')
const html = `<meta charset="utf-8"><div><span data-metadata="<!--(figmeta)${meta}(/figmeta)-->"></span><span data-buffer="<!--(figma)${archive.toString('base64')}(/figma)-->"></span></div><span style="white-space:pre-wrap;">Card</span>`
const file = join(import.meta.dir, '..', '..', 'core', 'tests', 'figma-clipboard.html')
await Bun.write(file, html)
console.log(`wrote ${file} (${html.length} bytes)`)
