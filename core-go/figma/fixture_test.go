package figma

// The parity fixtures in testdata/figma, written by `NALARUPA_FIGMA_FIXTURES=1 go test -run TestWriteFixtures
// ./figma/`: Figma clipboards built with a kiwi encoder here (like app/scripts/figma-fixture.ts), the card
// fixture re-encoded with a raw-deflate message, and malformed payloads.

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/binary"
	hexenc "encoding/hex"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

var fixtureDir = filepath.Join("..", "testdata", "figma")

// ── kiwi encoder ─────────────────────────────────────────────────────────────

type tdef struct {
	name   string
	kind   byte
	fields []tfield
}

type tfield struct {
	name, typ string
	array     bool
}

var builtin = []string{"bool", "byte", "int", "uint", "float", "string", "int64", "uint64"}

type enc struct{ b []byte }

func (o *enc) byte(v byte) { o.b = append(o.b, v) }
func (o *enc) uint(v uint32) {
	for {
		x := byte(v & 127)
		v >>= 7
		if v != 0 {
			o.byte(x | 128)
			continue
		}
		o.byte(x)
		return
	}
}
func (o *enc) int(v int32) { o.uint(uint32(v<<1) ^ uint32(v>>31)) }
func (o *enc) float(v float32) {
	r := bits.RotateLeft32(math.Float32bits(v), -23)
	if r&255 == 0 {
		o.byte(0)
		return
	}
	o.b = binary.LittleEndian.AppendUint32(o.b, r)
}
func (o *enc) string(s string) { o.b = append(append(o.b, s...), 0) }

func enumDef(name string, values ...string) tdef {
	d := tdef{name: name, kind: 0}
	for _, v := range values {
		d.fields = append(d.fields, tfield{name: v})
	}
	return d
}

func fields(spec string) []tfield {
	var out []tfield
	for _, f := range strings.Fields(spec) {
		name, typ, _ := strings.Cut(f, ":")
		arr := strings.HasSuffix(typ, "[]")
		out = append(out, tfield{name, strings.TrimSuffix(typ, "[]"), arr})
	}
	return out
}

type tschema []tdef

func (s tschema) index(name string) int {
	return slices.IndexFunc(s, func(d tdef) bool { return d.name == name })
}

func (s tschema) encode() []byte {
	o := &enc{}
	o.uint(uint32(len(s)))
	for _, d := range s {
		o.string(d.name)
		o.byte(d.kind)
		o.uint(uint32(len(d.fields)))
		for i, f := range d.fields {
			o.string(f.name)
			t := 0
			if d.kind != 0 {
				if b := slices.Index(builtin, f.typ); b >= 0 {
					t = ^b
				} else {
					t = s.index(f.typ)
				}
			}
			o.int(int32(t))
			if f.array {
				o.byte(1)
			} else {
				o.byte(0)
			}
			if d.kind == 0 {
				o.uint(uint32(i))
			} else {
				o.uint(uint32(i + 1))
			}
		}
	}
	return o.b
}

func (s tschema) value(o *enc, typ string, v jsonv.Value) {
	f, _ := jsonv.F64(v)
	switch typ {
	case "bool":
		if jsonv.IsTrue(v) {
			o.byte(1)
		} else {
			o.byte(0)
		}
		return
	case "byte":
		o.byte(byte(f))
		return
	case "int":
		o.int(int32(f))
		return
	case "uint":
		o.uint(uint32(f))
		return
	case "float":
		o.float(float32(f))
		return
	case "string":
		o.string(jsonv.StrOr(v))
		return
	}
	d := s[s.index(typ)]
	if d.kind == 0 {
		o.uint(uint32(slices.IndexFunc(d.fields, func(f tfield) bool { return f.name == jsonv.StrOr(v) })))
		return
	}
	for i, fl := range d.fields {
		x, ok := jsonv.Obj(v).Get(fl.name)
		if d.kind == 2 {
			if !ok {
				continue
			}
			o.uint(uint32(i + 1))
		}
		if !fl.array {
			s.value(o, fl.typ, x)
			continue
		}
		if h, ok := x.(string); ok && fl.typ == "byte" { // bytes given as hex
			b, _ := hexenc.DecodeString(h)
			o.uint(uint32(len(b)))
			o.b = append(o.b, b...)
			continue
		}
		items := jsonv.Items(x)
		o.uint(uint32(len(items)))
		for _, e := range items {
			s.value(o, fl.typ, e)
		}
	}
	if d.kind == 2 {
		o.uint(0)
	}
}

// figmaSchema is a subset of Figma's schema with its type and field names (more than the TS generator's:
// styles, component properties, variable bindings, image fills, independent borders, masks).
var figmaSchema = tschema{
	enumDef("MessageType", "JOIN_START", "NODE_CHANGES"),
	enumDef("NodeType", "NONE", "DOCUMENT", "CANVAS", "GROUP", "FRAME", "BOOLEAN_OPERATION", "VECTOR", "STAR", "LINE", "ELLIPSE", "RECTANGLE",
		"REGULAR_POLYGON", "ROUNDED_RECTANGLE", "TEXT", "SLICE", "SYMBOL", "INSTANCE", "SECTION", "VARIABLE", "VARIABLE_SET"),
	enumDef("PaintType", "SOLID", "GRADIENT_LINEAR", "GRADIENT_RADIAL", "GRADIENT_ANGULAR", "GRADIENT_DIAMOND", "IMAGE", "EMOJI", "VIDEO"),
	enumDef("BlendMode", "PASS_THROUGH", "NORMAL", "DARKEN", "MULTIPLY", "LINEAR_BURN", "COLOR_BURN", "LIGHTEN", "SCREEN", "LINEAR_DODGE",
		"COLOR_DODGE", "OVERLAY", "SOFT_LIGHT", "HARD_LIGHT", "DIFFERENCE", "EXCLUSION", "HUE", "SATURATION", "COLOR", "LUMINOSITY"),
	enumDef("ImageScaleMode", "STRETCH", "FIT", "FILL", "TILE"),
	enumDef("EffectType", "INNER_SHADOW", "DROP_SHADOW", "FOREGROUND_BLUR", "BACKGROUND_BLUR"),
	enumDef("StrokeAlign", "CENTER", "INSIDE", "OUTSIDE"),
	enumDef("StrokeJoin", "MITER", "BEVEL", "ROUND"),
	enumDef("StrokeCap", "NONE", "ROUND", "SQUARE"),
	enumDef("StackMode", "NONE", "HORIZONTAL", "VERTICAL"),
	enumDef("StackSize", "FIXED", "RESIZE_TO_FIT", "RESIZE_TO_FIT_WITH_IMPLICIT_SIZE"),
	enumDef("StackJustify", "MIN", "CENTER", "MAX", "SPACE_EVENLY", "SPACE_BETWEEN"),
	enumDef("StackAlign", "MIN", "CENTER", "MAX", "BASELINE"),
	enumDef("StackCounterAlign", "MIN", "CENTER", "MAX", "STRETCH", "AUTO", "BASELINE"),
	enumDef("StackPositioning", "AUTO", "ABSOLUTE"),
	enumDef("TextAutoResize", "NONE", "WIDTH_AND_HEIGHT", "HEIGHT"),
	enumDef("TextAlignHorizontal", "LEFT", "CENTER", "RIGHT", "JUSTIFIED"),
	enumDef("TextAlignVertical", "TOP", "CENTER", "BOTTOM"),
	enumDef("NumberUnits", "RAW", "PIXELS", "PERCENT"),
	enumDef("VariableDataType", "BOOLEAN", "FLOAT", "STRING", "ALIAS", "COLOR", "NODE_FIELD_ALIAS", "SYMBOL_ID", "TEXT_DATA", "PROP_REF"),
	enumDef("VariableResolvedDataType", "BOOLEAN", "FLOAT", "STRING", "COLOR", "SYMBOL_ID", "TEXT_DATA"),
	enumDef("VariableField", "MISSING", "CORNER_RADIUS", "PARAGRAPH_SPACING", "WIDTH", "HEIGHT", "OPACITY", "VISIBLE", "TEXT_DATA",
		"OVERRIDDEN_SYMBOL_ID", "STROKE_WEIGHT", "STACK_SPACING", "STACK_PADDING_LEFT", "STACK_PADDING_TOP", "STACK_PADDING_RIGHT",
		"STACK_PADDING_BOTTOM", "FONT_SIZE", "RECTANGLE_TOP_LEFT_CORNER_RADIUS", "BORDER_TOP_WEIGHT", "STACK_COUNTER_SPACING"),
	{"GUID", 1, fields("sessionID:uint localID:uint")},
	{"Color", 1, fields("r:float g:float b:float a:float")},
	{"Vector", 1, fields("x:float y:float")},
	{"Matrix", 1, fields("m00:float m01:float m02:float m10:float m11:float m12:float")},
	{"ParentIndex", 1, fields("guid:GUID position:string")},
	{"FontName", 1, fields("family:string style:string postscript:string")},
	{"ColorStop", 1, fields("color:Color position:float")},
	{"Number", 2, fields("value:float units:NumberUnits")},
	{"AssetRef", 2, fields("key:string version:string")},
	{"StyleId", 2, fields("guid:GUID assetRef:AssetRef")},
	{"VariableID", 2, fields("guid:GUID assetRef:AssetRef")},
	{"VariableSetID", 2, fields("guid:GUID assetRef:AssetRef")},
	{"PropRef", 2, fields("defId:GUID")},
	{"SymbolId", 2, fields("guid:GUID")},
	{"TextData", 2, fields("characters:string")},
	{"VariableAnyValue", 2, fields("boolValue:bool textValue:string floatValue:float alias:VariableID colorValue:Color propRefValue:PropRef symbolIdValue:SymbolId textDataValue:TextData")},
	{"VariableData", 2, fields("value:VariableAnyValue dataType:VariableDataType resolvedDataType:VariableResolvedDataType")},
	{"VariableDataValuesEntry", 2, fields("modeID:GUID variableData:VariableData")},
	{"VariableDataValues", 2, fields("entries:VariableDataValuesEntry[]")},
	{"VariableSetMode", 2, fields("id:GUID name:string")},
	{"VariableModeBySetMapEntry", 2, fields("variableSetID:VariableSetID variableModeID:GUID")},
	{"VariableModeBySetMap", 2, fields("entries:VariableModeBySetMapEntry[]")},
	{"VariableConsumptionMapEntry", 2, fields("variableField:VariableField variableData:VariableData")},
	{"VariableConsumptionMap", 2, fields("entries:VariableConsumptionMapEntry[]")},
	{"ComponentPropAssignment", 2, fields("defID:GUID varValue:VariableData")},
	{"ComponentPropRef", 2, fields("defID:GUID componentPropNodeField:VariableField")},
	{"Paint", 2, fields("type:PaintType color:Color opacity:float visible:bool blendMode:BlendMode stops:ColorStop[] transform:Matrix colorVar:VariableData imageScaleMode:ImageScaleMode")},
	{"Effect", 2, fields("type:EffectType color:Color offset:Vector radius:float visible:bool blendMode:BlendMode spread:float")},
	{"GUIDPath", 2, fields("guids:GUID[]")},
	{"VectorData", 2, fields("vectorNetworkBlob:uint normalizedSize:Vector")},
	{"ArcData", 2, fields("startingAngle:float endingAngle:float innerRadius:float")},
	{"SymbolData", 2, fields("symbolID:GUID symbolOverrides:NodeChange[] uniformScaleFactor:float")},
	{"NodeChange", 2, fields(`guid:GUID parentIndex:ParentIndex type:NodeType name:string visible:bool opacity:float size:Vector transform:Matrix
		fillPaints:Paint[] strokePaints:Paint[] strokeWeight:float strokeAlign:StrokeAlign strokeJoin:StrokeJoin strokeCap:StrokeCap
		cornerRadius:float rectangleTopLeftCornerRadius:float rectangleTopRightCornerRadius:float rectangleBottomLeftCornerRadius:float
		rectangleBottomRightCornerRadius:float effects:Effect[] frameMaskDisabled:bool resizeToFit:bool stackMode:StackMode
		stackSpacing:float stackHorizontalPadding:float stackVerticalPadding:float stackPaddingRight:float stackPaddingBottom:float
		stackPrimarySizing:StackSize stackCounterSizing:StackSize stackPrimaryAlignItems:StackJustify stackCounterAlignItems:StackAlign
		stackChildAlignSelf:StackCounterAlign stackChildPrimaryGrow:float stackPositioning:StackPositioning textData:TextData
		fontSize:float fontName:FontName lineHeight:Number letterSpacing:Number textAlignHorizontal:TextAlignHorizontal
		textAlignVertical:TextAlignVertical textAutoResize:TextAutoResize symbolData:SymbolData overrideKey:GUID guidPath:GUIDPath
		vectorData:VectorData arcData:ArcData count:uint key:string variableSetModes:VariableSetMode[] variableSetID:VariableSetID
		variableDataValues:VariableDataValues variableModeBySetMap:VariableModeBySetMap mask:bool borderStrokeWeightsIndependent:bool
		borderTopWeight:float borderRightWeight:float borderBottomWeight:float borderLeftWeight:float bordersTakeSpace:bool
		styleIdForFill:StyleId styleIdForStrokeFill:StyleId styleIdForEffect:StyleId styleIdForText:StyleId overriddenSymbolID:GUID
		derivedSymbolData:NodeChange[] componentPropAssignments:ComponentPropAssignment[] componentPropRefs:ComponentPropRef[]
		parameterConsumptionMap:VariableConsumptionMap variableConsumptionMap:VariableConsumptionMap`)},
	{"Blob", 2, fields("bytes:byte[]")},
	{"Message", 2, fields("type:MessageType sessionID:uint ackID:uint nodeChanges:NodeChange[] blobs:Blob[]")},
}

func deflate(b []byte) []byte {
	var out bytes.Buffer
	w, _ := flate.NewWriter(&out, flate.BestCompression)
	w.Write(b)
	w.Close()
	return out.Bytes()
}

func zstdEnc(b []byte) []byte {
	e, _ := zstd.NewWriter(nil)
	defer e.Close()
	return e.EncodeAll(b, nil)
}

func archive(magic string, chunks ...[]byte) []byte {
	b := append([]byte(magic), 15, 0, 0, 0)
	for _, c := range chunks {
		b = binary.LittleEndian.AppendUint32(b, uint32(len(c)))
		b = append(b, c...)
	}
	return b
}

func clipboard(b64 string) string {
	meta := base64.StdEncoding.EncodeToString([]byte(`{"fileKey":"nalarupaFixture","pasteID":1,"dataType":"scene"}`))
	return `<meta charset="utf-8"><div><span data-metadata="<!--(figmeta)` + meta + `(/figmeta)-->"></span><span data-buffer="<!--(figma)` +
		b64 + `(/figma)-->"></span></div>`
}

// clipboardOf encodes a message (JSON) with figmaSchema: deflate schema, zstd or deflate message.
func clipboardOf(t *testing.T, msg string, zstdMsg bool) string {
	t.Helper()
	o := &enc{}
	figmaSchema.value(o, "Message", jsonv.MustParse(msg))
	m := deflate(o.b)
	if zstdMsg {
		m = zstdEnc(o.b)
	}
	return clipboard(base64.StdEncoding.EncodeToString(archive("fig-kiwi", deflate(figmaSchema.encode()), m)))
}

// blob builds a vector network: vertices, segments (a, ad, z, zd) and regions (winding rule, loops of
// segment indices).
func blob(verts [][2]float32, segs [][6]float32, regions [][]any) string {
	var b []byte
	u := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	f := func(v float32) { u(math.Float32bits(v)) }
	u(uint32(len(verts)))
	u(uint32(len(segs)))
	u(uint32(len(regions)))
	for _, v := range verts {
		u(0)
		f(v[0])
		f(v[1])
	}
	for _, s := range segs {
		u(0)
		u(uint32(s[0]))
		f(s[1])
		f(s[2])
		u(uint32(s[3]))
		f(s[4])
		f(s[5])
	}
	for _, r := range regions {
		u(r[0].(uint32))
		loops := r[1].([][]uint32)
		u(uint32(len(loops)))
		for _, l := range loops {
			u(uint32(len(l)))
			for _, s := range l {
				u(s)
			}
		}
	}
	return hexenc.EncodeToString(b)
}

// shapesMsg covers transforms, gradients, images, effects, blend modes, polygons, arcs, lines, text,
// strokes, auto layout, masks, styles and vectors with regions and curves.
func shapesMsg() string {
	vec := blob(
		[][2]float32{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {3, 3}, {7, 3}, {5, 7}},
		[][6]float32{{0, 0, 0, 1, 0, 0}, {1, 0, 0, 2, 0, 0}, {2, 0, 0, 3, -2, 1.5}, {3, 0, 0, 0, 0, 0}, {4, 1, 0, 5, 0, 0}, {6, 0, 0, 5, 0, 0}, {6, 0, 0, 4, 0.333333, 0}},
		[][]any{{uint32(1), [][]uint32{{0, 1, 2, 3}}}, {uint32(0), [][]uint32{{4, 5, 6}, {}}}},
	)
	return `{"type": "NODE_CHANGES", "sessionID": 0, "ackID": 0, "blobs": [{"bytes": "` + vec + `"}], "nodeChanges": [
 {"guid": {"sessionID": 0, "localID": 0}, "type": "DOCUMENT", "name": "Document", "visible": true},
 {"guid": {"sessionID": 0, "localID": 1}, "parentIndex": {"guid": {"sessionID": 0, "localID": 0}, "position": "!"}, "type": "CANVAS", "name": "Hidden", "visible": false},
 {"guid": {"sessionID": 0, "localID": 2}, "parentIndex": {"guid": {"sessionID": 0, "localID": 0}, "position": "#"}, "type": "CANVAS", "name": "Page", "visible": true},
 {"guid": {"sessionID": 0, "localID": 3}, "parentIndex": {"guid": {"sessionID": 0, "localID": 0}, "position": "~"}, "type": "CANVAS", "name": "Internal", "visible": false},
 {"guid": {"sessionID": 1, "localID": 1}, "parentIndex": {"guid": {"sessionID": 0, "localID": 3}, "position": "a"}, "type": "RECTANGLE", "name": "Brand", "key": "brandstyle",
  "fillPaints": [{"type": "SOLID", "color": {"r": 1, "g": 0.2, "b": 0.1, "a": 1}, "opacity": 0.75, "visible": true, "blendMode": "NORMAL"}]},
 {"guid": {"sessionID": 1, "localID": 2}, "parentIndex": {"guid": {"sessionID": 0, "localID": 3}, "position": "b"}, "type": "TEXT", "name": "Heading",
  "fontName": {"family": "Georgia", "style": "Extra Light Italic", "postscript": ""}, "fontSize": 30, "lineHeight": {"value": 1.25, "units": "RAW"}},
 {"guid": {"sessionID": 1, "localID": 3}, "parentIndex": {"guid": {"sessionID": 0, "localID": 3}, "position": "c"}, "type": "RECTANGLE", "name": "Glow",
  "effects": [{"type": "DROP_SHADOW", "color": {"r": 0.2, "g": 0.4, "b": 1, "a": 1}, "offset": {"x": 3, "y": -2}, "radius": 0, "visible": true, "blendMode": "SCREEN"}]},

 {"guid": {"sessionID": 2, "localID": 1}, "parentIndex": {"guid": {"sessionID": 0, "localID": 2}, "position": "a"}, "type": "FRAME", "name": "Rotated", "visible": true,
  "transform": {"m00": 0.8660254, "m01": -0.5, "m02": 100, "m10": 0.5, "m11": 0.8660254, "m12": 20}, "size": {"x": 300, "y": 200},
  "styleIdForFill": {"assetRef": {"key": "brandstyle"}}, "styleIdForEffect": {"guid": {"sessionID": 1, "localID": 3}}, "frameMaskDisabled": true,
  "rectangleTopLeftCornerRadius": 4, "rectangleBottomRightCornerRadius": 2.5, "opacity": 0.5},
 {"guid": {"sessionID": 2, "localID": 2}, "parentIndex": {"guid": {"sessionID": 2, "localID": 1}, "position": "a"}, "type": "ELLIPSE", "name": "Arc", "visible": true,
  "transform": {"m00": -1, "m01": 0, "m02": 60, "m10": 0, "m11": 1, "m12": 10}, "size": {"x": 50, "y": 40},
  "arcData": {"startingAngle": 0.5, "endingAngle": 3.0, "innerRadius": 0.25},
  "fillPaints": [{"type": "GRADIENT_RADIAL", "visible": true, "opacity": 0.9, "blendMode": "NORMAL",
    "transform": {"m00": 0.6, "m01": 0.2, "m02": 0.1, "m10": -0.3, "m11": 1.1, "m12": 0.05},
    "stops": [{"color": {"r": 1, "g": 1, "b": 1, "a": 1}, "position": 0}, {"color": {"r": 0, "g": 0, "b": 0, "a": 0.5}, "position": 1}]}]},
 {"guid": {"sessionID": 2, "localID": 3}, "parentIndex": {"guid": {"sessionID": 2, "localID": 1}, "position": "b"}, "type": "REGULAR_POLYGON", "name": "Hex", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 120, "m10": 0, "m11": 1, "m12": 30}, "size": {"x": 40, "y": 40}, "count": 6, "cornerRadius": 3,
  "fillPaints": [{"type": "IMAGE", "imageScaleMode": "FIT", "opacity": 0.5, "visible": true, "blendMode": "NORMAL"},
    {"type": "SOLID", "color": {"r": 0, "g": 0.5, "b": 0, "a": 1}, "opacity": 1, "visible": false, "blendMode": "SCREEN"},
    {"type": "IMAGE", "imageScaleMode": "STRETCH", "opacity": 1, "visible": true, "blendMode": "MULTIPLY"},
    {"type": "GRADIENT_DIAMOND", "opacity": 1, "visible": true}],
  "strokePaints": [{"type": "SOLID", "color": {"r": 0, "g": 0, "b": 0, "a": 1}, "opacity": 1, "visible": true, "blendMode": "NORMAL"}],
  "borderStrokeWeightsIndependent": true, "borderTopWeight": 1, "borderRightWeight": 2, "borderBottomWeight": 0, "borderLeftWeight": 2,
  "strokeAlign": "OUTSIDE", "strokeJoin": "BEVEL", "strokeCap": "SQUARE"},
 {"guid": {"sessionID": 2, "localID": 4}, "parentIndex": {"guid": {"sessionID": 2, "localID": 1}, "position": "c"}, "type": "LINE", "name": "Rule", "visible": false,
  "transform": {"m00": 0, "m01": -1, "m02": 10, "m10": 1, "m11": 0, "m12": 150}, "size": {"x": 80, "y": 0},
  "strokePaints": [{"type": "SOLID", "color": {"r": 0.5, "g": 0.5, "b": 0.5, "a": 1}, "opacity": 1, "visible": true, "blendMode": "NORMAL"}], "strokeWeight": 2},
 {"guid": {"sessionID": 2, "localID": 5}, "parentIndex": {"guid": {"sessionID": 2, "localID": 1}, "position": "d"}, "type": "VECTOR", "name": "Shape", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 200, "m10": 0, "m11": 1, "m12": 100}, "size": {"x": 10, "y": 10}, "vectorData": {"vectorNetworkBlob": 0},
  "fillPaints": [{"type": "SOLID", "color": {"r": 0.1, "g": 0.2, "b": 0.3, "a": 1}, "opacity": 1, "visible": true, "blendMode": "NORMAL"}]},
 {"guid": {"sessionID": 2, "localID": 6}, "parentIndex": {"guid": {"sessionID": 2, "localID": 1}, "position": "e"}, "type": "TEXT", "name": "Caption", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 20, "m10": 0, "m11": 1, "m12": 160}, "size": {"x": 140, "y": 30}, "opacity": 0.8,
  "textData": {"characters": "Ünïcödé caption"}, "fontName": {"family": "Roboto", "style": "Bold Italic", "postscript": ""}, "fontSize": 16,
  "lineHeight": {"value": 24, "units": "PIXELS"}, "letterSpacing": {"value": 5, "units": "PERCENT"}, "textAutoResize": "NONE",
  "textAlignHorizontal": "CENTER", "textAlignVertical": "BOTTOM",
  "fillPaints": [{"type": "SOLID", "color": {"r": 0, "g": 0, "b": 0, "a": 1}, "opacity": 1, "visible": true, "blendMode": "NORMAL"}],
  "strokePaints": [{"type": "SOLID", "color": {"r": 1, "g": 0, "b": 0, "a": 1}, "opacity": 1, "visible": true, "blendMode": "NORMAL"}], "strokeWeight": 1},
 {"guid": {"sessionID": 2, "localID": 7}, "parentIndex": {"guid": {"sessionID": 2, "localID": 1}, "position": "f"}, "type": "TEXT", "name": "Styled", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 20, "m10": 0, "m11": 1, "m12": 100}, "size": {"x": 100, "y": 40}, "textData": {"characters": "Styled"},
  "styleIdForText": {"guid": {"sessionID": 1, "localID": 2}}, "textAutoResize": "HEIGHT", "textAlignHorizontal": "JUSTIFIED"},

 {"guid": {"sessionID": 3, "localID": 1}, "parentIndex": {"guid": {"sessionID": 0, "localID": 2}, "position": "b"}, "type": "FRAME", "name": "Row", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 0, "m10": 0, "m11": 1, "m12": 400}, "size": {"x": 400, "y": 60},
  "stackMode": "HORIZONTAL", "stackPrimaryAlignItems": "SPACE_BETWEEN", "stackCounterAlignItems": "MAX", "stackPrimarySizing": "FIXED",
  "stackCounterSizing": "RESIZE_TO_FIT_WITH_IMPLICIT_SIZE", "stackVerticalPadding": 1, "stackPaddingRight": 2, "stackPaddingBottom": 3,
  "stackHorizontalPadding": 4, "bordersTakeSpace": true, "cornerRadius": 12,
  "fillPaints": [{"type": "GRADIENT_ANGULAR", "visible": true, "opacity": 1, "blendMode": "NORMAL",
    "transform": {"m00": 1, "m01": 0, "m02": 0, "m10": 0, "m11": 1, "m12": 0},
    "stops": [{"color": {"r": 1, "g": 0, "b": 0, "a": 1}, "position": 0.7}, {"color": {"r": 0, "g": 0, "b": 1, "a": 1}, "position": 0.2}]},
   {"type": "GRADIENT_LINEAR", "visible": true, "opacity": 0.3, "blendMode": "OVERLAY",
    "transform": {"m00": 0, "m01": 1, "m02": 0, "m10": -1, "m11": 0, "m12": 1},
    "stops": [{"color": {"r": 0, "g": 1, "b": 0, "a": 1}, "position": 0}, {"color": {"r": 1, "g": 1, "b": 0, "a": 1}, "position": 0.5}]}],
  "effects": [{"type": "INNER_SHADOW", "color": {"r": 0, "g": 0, "b": 0, "a": 0.25}, "offset": {"x": 0, "y": 0}, "radius": 4, "visible": false, "blendMode": "MULTIPLY"},
   {"type": "FOREGROUND_BLUR", "radius": 8, "visible": true}, {"type": "BACKGROUND_BLUR", "radius": 16, "visible": false}]},
 {"guid": {"sessionID": 3, "localID": 2}, "parentIndex": {"guid": {"sessionID": 3, "localID": 1}, "position": "a"}, "type": "RECTANGLE", "name": "Pinned", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 5, "m10": 0, "m11": 1, "m12": 5}, "size": {"x": 10, "y": 10}, "stackPositioning": "ABSOLUTE"},
 {"guid": {"sessionID": 3, "localID": 3}, "parentIndex": {"guid": {"sessionID": 3, "localID": 1}, "position": "b"}, "type": "RECTANGLE", "name": "Grow", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 4, "m10": 0, "m11": 1, "m12": 1}, "size": {"x": 100, "y": 20}, "stackChildPrimaryGrow": 1, "stackChildAlignSelf": "STRETCH"},
 {"guid": {"sessionID": 3, "localID": 4}, "parentIndex": {"guid": {"sessionID": 3, "localID": 1}, "position": "c"}, "type": "TEXT", "name": "", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 120, "m10": 0, "m11": 1, "m12": 1}, "size": {"x": 50, "y": 20}, "textData": {"characters": ""},
  "letterSpacing": {"value": 1, "units": "PIXELS"}, "lineHeight": {"value": 1.2, "units": "RAW"}, "textAutoResize": "WIDTH_AND_HEIGHT"},
 {"guid": {"sessionID": 3, "localID": 5}, "parentIndex": {"guid": {"sessionID": 3, "localID": 1}, "position": "d"}, "type": "FRAME", "name": "Group", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 200, "m10": 0, "m11": 1, "m12": 1}, "size": {"x": 30, "y": 30}, "resizeToFit": true, "stackChildAlignSelf": "STRETCH",
  "fillPaints": [{"type": "SOLID", "color": {"r": 1, "g": 1, "b": 1, "a": 1}, "opacity": 1, "visible": true, "blendMode": "NORMAL"}]},
 {"guid": {"sessionID": 3, "localID": 6}, "parentIndex": {"guid": {"sessionID": 3, "localID": 5}, "position": "a"}, "type": "RECTANGLE", "name": "Mask", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 0, "m10": 0, "m11": 1, "m12": 0}, "size": {"x": 30, "y": 30}, "mask": true,
  "fillPaints": [{"type": "SOLID", "color": {"r": 1, "g": 1, "b": 1, "a": 1}, "opacity": 1, "visible": true, "blendMode": "NORMAL"}]},
 {"guid": {"sessionID": 3, "localID": 7}, "parentIndex": {"guid": {"sessionID": 3, "localID": 1}, "position": "e"}, "type": "FRAME", "name": "Hug", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 300, "m10": 0, "m11": 1, "m12": 1}, "size": {"x": 40, "y": 40}, "stackMode": "VERTICAL",
  "stackPrimarySizing": "RESIZE_TO_FIT", "stackCounterSizing": "RESIZE_TO_FIT_WITH_IMPLICIT_SIZE", "stackSpacing": 2.5},
 {"guid": {"sessionID": 3, "localID": 8}, "parentIndex": {"guid": {"sessionID": 3, "localID": 7}, "position": "a"}, "type": "ELLIPSE", "name": "In hug", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 0, "m10": 0, "m11": 1, "m12": 0}, "size": {"x": 40, "y": 40}, "stackChildAlignSelf": "STRETCH"},

 {"guid": {"sessionID": 4, "localID": 1}, "parentIndex": {"guid": {"sessionID": 0, "localID": 2}, "position": "c"}, "type": "ROUNDED_RECTANGLE", "name": "Mirrored", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 500, "m10": 0, "m11": -1, "m12": 80}, "size": {"x": 30, "y": 20}, "cornerRadius": 5, "strokeAlign": "CENTER",
  "strokePaints": [{"type": "SOLID", "color": {"r": 0, "g": 0, "b": 1, "a": 1}, "opacity": 1, "visible": true, "blendMode": "NORMAL"}], "strokeWeight": 1.5,
  "strokeJoin": "ROUND", "strokeCap": "ROUND"},
 {"guid": {"sessionID": 5, "localID": 1}, "parentIndex": {"guid": {"sessionID": 0, "localID": 2}, "position": "d"}, "type": "STAR", "name": "Unsupported", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 0, "m10": 0, "m11": 1, "m12": 0}, "size": {"x": 10, "y": 10}},
 {"guid": {"sessionID": 6, "localID": 1}, "parentIndex": {"guid": {"sessionID": 0, "localID": 2}, "position": "e"}, "type": "SECTION", "name": "Section", "visible": true,
  "transform": {"m00": 1, "m01": 0, "m02": 0, "m10": 0, "m11": 1, "m12": 600}, "size": {"x": 200, "y": 100}}
]}`
}

// instancesMsg covers component properties, swaps (by override and by property), nested overrides,
// derived data and bound fields.
func instancesMsg() string {
	g := func(s, l int) string { return `{"sessionID": ` + itoa(s) + `, "localID": ` + itoa(l) + `}` }
	at := func(x, y int) string {
		return `{"m00": 1, "m01": 0, "m02": ` + itoa(x) + `, "m10": 0, "m11": 1, "m12": ` + itoa(y) + `}`
	}
	solid := func(r, gg, b string) string {
		return `{"type": "SOLID", "color": {"r": ` + r + `, "g": ` + gg + `, "b": ` + b + `, "a": 1}, "opacity": 1, "visible": true, "blendMode": "NORMAL"}`
	}
	child := func(s, l int, pos string) string { return `{"guid": ` + g(s, l) + `, "position": "` + pos + `"}` }
	return `{"type": "NODE_CHANGES", "sessionID": 0, "ackID": 0, "nodeChanges": [
 {"guid": ` + g(0, 0) + `, "type": "DOCUMENT", "name": "Document", "visible": true},
 {"guid": ` + g(0, 1) + `, "parentIndex": ` + child(0, 0, "!") + `, "type": "CANVAS", "name": "Page", "visible": true},
 {"guid": ` + g(0, 2) + `, "parentIndex": ` + child(0, 0, "~") + `, "type": "CANVAS", "name": "Internal", "visible": false},
 {"guid": ` + g(7, 1) + `, "type": "VARIABLE_SET", "name": "Sizes", "key": "sizeset", "variableSetModes": [{"id": ` + g(7, 10) + `, "name": "Compact"}, {"id": ` + g(7, 11) + `, "name": "Roomy"}]},
 {"guid": ` + g(7, 2) + `, "type": "VARIABLE", "name": "radius", "key": "radiusvar", "variableSetID": {"assetRef": {"key": "sizeset"}},
  "variableDataValues": {"entries": [{"modeID": ` + g(7, 10) + `, "variableData": {"value": {"floatValue": 4}, "dataType": "FLOAT", "resolvedDataType": "FLOAT"}},
   {"modeID": ` + g(7, 11) + `, "variableData": {"value": {"floatValue": 10}, "dataType": "FLOAT", "resolvedDataType": "FLOAT"}}]}},
 {"guid": ` + g(7, 3) + `, "type": "VARIABLE", "name": "accent", "variableSetID": {"guid": ` + g(7, 1) + `},
  "variableDataValues": {"entries": [{"modeID": ` + g(7, 10) + `, "variableData": {"value": {"colorValue": {"r": 1, "g": 0, "b": 1, "a": 1}}, "dataType": "COLOR", "resolvedDataType": "COLOR"}}]}},

 {"guid": ` + g(10, 1) + `, "parentIndex": ` + child(0, 2, "a") + `, "type": "SYMBOL", "name": "Badge", "visible": true, "transform": ` + at(0, 0) + `, "size": {"x": 80, "y": 24},
  "stackMode": "HORIZONTAL", "stackSpacing": 4, "stackHorizontalPadding": 6, "stackPaddingRight": 6, "stackVerticalPadding": 6, "stackPaddingBottom": 6,
  "fillPaints": [` + solid("0.9", "0.9", "0.9") + `]},
 {"guid": ` + g(10, 2) + `, "parentIndex": ` + child(10, 1, "a") + `, "type": "TEXT", "name": "Text", "visible": true, "transform": ` + at(6, 6) + `, "size": {"x": 40, "y": 12},
  "textData": {"characters": "Default"}, "fontName": {"family": "Inter", "style": "Medium", "postscript": ""}, "fontSize": 10, "textAutoResize": "WIDTH_AND_HEIGHT",
  "componentPropRefs": [{"defID": ` + g(20, 1) + `, "componentPropNodeField": "TEXT_DATA"}]},
 {"guid": ` + g(10, 3) + `, "parentIndex": ` + child(10, 1, "b") + `, "type": "ELLIPSE", "name": "Dot", "visible": true, "transform": ` + at(50, 8) + `, "size": {"x": 8, "y": 8},
  "fillPaints": [` + solid("0", "1", "0") + `], "componentPropRefs": [{"defID": ` + g(20, 2) + `, "componentPropNodeField": "VISIBLE"}]},
 {"guid": ` + g(11, 1) + `, "parentIndex": ` + child(0, 2, "b") + `, "type": "SYMBOL", "name": "Alt", "visible": true, "transform": ` + at(0, 0) + `, "size": {"x": 30, "y": 30},
  "fillPaints": [` + solid("0", "0", "0") + `]},
 {"guid": ` + g(11, 2) + `, "parentIndex": ` + child(11, 1, "a") + `, "type": "RECTANGLE", "name": "AltRect", "visible": true, "transform": ` + at(5, 5) + `, "size": {"x": 20, "y": 20},
  "fillPaints": [{"type": "SOLID", "color": {"r": 0, "g": 0, "b": 0, "a": 1}, "opacity": 1, "visible": true, "blendMode": "NORMAL",
    "colorVar": {"value": {"alias": {"guid": ` + g(7, 3) + `}}, "dataType": "ALIAS", "resolvedDataType": "COLOR"}}],
  "parameterConsumptionMap": {"entries": [{"variableField": "CORNER_RADIUS", "variableData": {"value": {"alias": {"assetRef": {"key": "radiusvar"}}}, "dataType": "ALIAS", "resolvedDataType": "FLOAT"}}]}},
 {"guid": ` + g(12, 1) + `, "parentIndex": ` + child(0, 2, "c") + `, "type": "SYMBOL", "name": "Card", "visible": true, "transform": ` + at(0, 0) + `, "size": {"x": 100, "y": 60},
  "stackMode": "VERTICAL", "stackSpacing": 8, "fillPaints": [` + solid("1", "1", "1") + `]},
 {"guid": ` + g(12, 2) + `, "parentIndex": ` + child(12, 1, "a") + `, "type": "INSTANCE", "name": "Inner badge", "visible": true, "transform": ` + at(0, 0) + `, "size": {"x": 80, "y": 24},
  "symbolData": {"symbolID": ` + g(10, 1) + `}},
 {"guid": ` + g(12, 3) + `, "parentIndex": ` + child(12, 1, "b") + `, "type": "INSTANCE", "name": "Swap slot", "visible": true, "transform": ` + at(0, 30) + `, "size": {"x": 30, "y": 30},
  "symbolData": {"symbolID": ` + g(10, 1) + `}},

 {"guid": ` + g(30, 1) + `, "parentIndex": ` + child(0, 1, "a") + `, "type": "INSTANCE", "name": "Badge 1", "visible": true, "transform": ` + at(0, 0) + `, "size": {"x": 80, "y": 24},
  "symbolData": {"symbolID": ` + g(10, 1) + `, "symbolOverrides": [{"guidPath": {"guids": [` + g(10, 3) + `]}, "fillPaints": [` + solid("0", "0", "1") + `]}]},
  "componentPropAssignments": [{"defID": ` + g(20, 1) + `, "varValue": {"value": {"textValue": "Hello"}, "dataType": "STRING"}},
   {"defID": ` + g(20, 2) + `, "varValue": {"value": {"boolValue": false}, "dataType": "BOOLEAN"}}]},
 {"guid": ` + g(30, 2) + `, "parentIndex": ` + child(0, 1, "b") + `, "type": "INSTANCE", "name": "Swapped", "visible": true, "transform": ` + at(100, 0) + `, "size": {"x": 30, "y": 30},
  "symbolData": {"symbolID": ` + g(10, 1) + `}, "overriddenSymbolID": ` + g(11, 1) + `,
  "variableModeBySetMap": {"entries": [{"variableSetID": {"assetRef": {"key": "sizeset"}}, "variableModeID": ` + g(7, 11) + `}]}},
 {"guid": ` + g(30, 3) + `, "parentIndex": ` + child(0, 1, "c") + `, "type": "INSTANCE", "name": "Card 1", "visible": true, "transform": ` + at(0, 100) + `, "size": {"x": 100, "y": 60},
  "symbolData": {"symbolID": ` + g(12, 1) + `, "symbolOverrides": [
    {"guidPath": {"guids": [` + g(12, 2) + `, ` + g(10, 2) + `]}, "textData": {"characters": "Nested"}},
    {"guidPath": {"guids": [` + g(12, 2) + `]}, "opacity": 0.5, "componentPropAssignments": [{"defID": ` + g(20, 2) + `, "varValue": {"value": {"boolValue": false}, "dataType": "BOOLEAN"}}]},
    {"guidPath": {"guids": [` + g(12, 3) + `]}, "overriddenSymbolID": ` + g(11, 1) + `}]},
  "derivedSymbolData": [{"guidPath": {"guids": [` + g(12, 2) + `]}, "size": {"x": 90, "y": 26}, "styleIdForFill": {"guid": ` + g(1, 1) + `}}]},
 {"guid": ` + g(30, 4) + `, "parentIndex": ` + child(0, 1, "d") + `, "type": "INSTANCE", "name": "Bound", "visible": true, "transform": ` + at(200, 0) + `, "size": {"x": 80, "y": 24},
  "symbolData": {"symbolID": ` + g(10, 1) + `},
  "parameterConsumptionMap": {"entries": [{"variableField": "CORNER_RADIUS", "variableData": {"value": {"floatValue": 6}, "dataType": "FLOAT"}},
   {"variableField": "WIDTH", "variableData": {"value": {"floatValue": 120}, "dataType": "FLOAT"}},
   {"variableField": "OPACITY", "variableData": {"value": {"floatValue": 0.25}, "dataType": "FLOAT"}},
   {"variableField": "STACK_SPACING", "variableData": {"value": {"floatValue": 9}, "dataType": "FLOAT"}}]}},
 {"guid": ` + g(30, 5) + `, "parentIndex": ` + child(0, 1, "e") + `, "type": "INSTANCE", "name": "Missing", "visible": true, "transform": ` + at(300, 0) + `, "size": {"x": 10, "y": 10},
  "symbolData": {"symbolID": ` + g(99, 1) + `}}
]}`
}

// budgetMsg nests instances 50 wide and three deep: more than the 100k node budget once expanded.
func budgetMsg() string {
	g := func(s, l int) string { return `{"sessionID": ` + itoa(s) + `, "localID": ` + itoa(l) + `}` }
	var b strings.Builder
	b.WriteString(`{"type": "NODE_CHANGES", "nodeChanges": [{"guid": ` + g(0, 0) + `, "type": "DOCUMENT", "visible": true},
 {"guid": ` + g(0, 1) + `, "parentIndex": {"guid": ` + g(0, 0) + `, "position": "a"}, "type": "CANVAS", "visible": true},
 {"guid": ` + g(0, 2) + `, "parentIndex": {"guid": ` + g(0, 0) + `, "position": "b"}, "type": "CANVAS", "visible": false}`)
	for level := 1; level <= 3; level++ {
		b.WriteString(`, {"guid": ` + g(level, 0) + `, "parentIndex": {"guid": ` + g(0, 2) + `, "position": "a"}, "type": "SYMBOL", "visible": true, "size": {"x": 10, "y": 10}}`)
		for i := 1; i <= 50; i++ {
			kid := `"type": "RECTANGLE", "size": {"x": 1, "y": 1}`
			if level > 1 {
				kid = `"type": "INSTANCE", "symbolData": {"symbolID": ` + g(level-1, 0) + `}`
			}
			b.WriteString(`, {"guid": ` + g(level, i) + `, "parentIndex": {"guid": ` + g(level, 0) + `, "position": "` + string(rune('a'+i/26)) + string(rune('a'+i%26)) + `"}, "visible": true, ` + kid + `}`)
		}
	}
	b.WriteString(`, {"guid": ` + g(9, 1) + `, "parentIndex": {"guid": ` + g(0, 1) + `, "position": "a"}, "type": "INSTANCE", "visible": true, "symbolData": {"symbolID": ` + g(3, 0) + `}}]}`)
	return b.String()
}

func itoa(i int) string { return string(jsonv.Int(int64(i))) }

// deepSchema is a struct that holds itself: decoding it recurses past the depth cap without reading.
func deepClipboard() string {
	s := tschema{{"Deep", 1, fields("d:Deep")}, {"Message", 2, fields("deep:Deep")}}
	return clipboard(base64.StdEncoding.EncodeToString(archive("fig-kiwi", deflate(s.encode()), deflate([]byte{1}))))
}

func TestWriteFixtures(t *testing.T) {
	if os.Getenv("NALARUPA_FIGMA_FIXTURES") == "" {
		t.Skip("set NALARUPA_FIGMA_FIXTURES=1 to rewrite testdata/figma")
	}
	card, err := os.ReadFile(filepath.Join("..", "..", "core", "tests", "figma-clipboard.html"))
	if err != nil {
		t.Fatal(err)
	}
	// The card with a raw-deflate message chunk, as a "fig-jam." archive.
	b64, _ := buffer(string(card))
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	var chunks [][]byte
	for i := 12; i+4 < len(raw); {
		n := int(binary.LittleEndian.Uint32(raw[i:]))
		chunks = append(chunks, raw[i+4:i+4+n])
		i += 4 + n
	}
	msg, err := inflate(chunks[1])
	if err != nil {
		t.Fatal(err)
	}
	wrap := func(s string) string { // base64 with line breaks and spaces, which are stripped
		var b strings.Builder
		for i := 0; i < len(s); i += 76 {
			b.WriteString(s[i:min(i+76, len(s))])
			b.WriteString("\r\n ")
		}
		return b.String()
	}
	deflated := archive("fig-jam.", chunks[0], deflate(msg))
	files := map[string]string{
		"card.html":         string(card),
		"card-deflate.html": clipboard(wrap(base64.StdEncoding.EncodeToString(deflated))),
		"shapes.html":       clipboardOf(t, shapesMsg(), true),
		"instances.html":    clipboardOf(t, instancesMsg(), false),
		"plain.html":        `<meta charset="utf-8"><span>plain text, not Figma</span>`,
		"no-metadata.html":  `<span data-buffer="<!--(figma)` + base64.StdEncoding.EncodeToString(deflated) + `(/figma)-->"></span>`,
		"no-end.html":       `<span data-metadata=""></span><span data-buffer="<!--(figma)ZmlnLWtpd2k=-->"></span>`,
		"bad-base64.html":   clipboard(base64.StdEncoding.EncodeToString(deflated)[:101]),
		"bad-magic.html":    clipboard(base64.StdEncoding.EncodeToString(append([]byte("fig-kiwX"), deflated[8:]...))),
		"one-chunk.html":    clipboard(base64.StdEncoding.EncodeToString(archive("fig-kiwi", chunks[0]))),
		"truncated.html":    clipboard(base64.StdEncoding.EncodeToString(deflated[:len(deflated)-10])),
		"bad-deflate.html":  clipboard(base64.StdEncoding.EncodeToString(archive("fig-kiwi", chunks[0], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}))),
		"cut-message.html":  clipboard(base64.StdEncoding.EncodeToString(archive("fig-kiwi", chunks[0], deflate(msg[:len(msg)/2])))),
		"deep.html":         deepClipboard(),
		"no-message.html": clipboard(base64.StdEncoding.EncodeToString(archive("fig-kiwi",
			deflate(tschema{{"Other", 2, fields("x:uint")}}.encode()), deflate([]byte{0})))),
		"bad-kind.html": clipboard(base64.StdEncoding.EncodeToString(archive("fig-kiwi",
			deflate([]byte{1, 'M', 0, 3, 0}), deflate([]byte{0})))),
		"over-cap.html": clipboard(base64.StdEncoding.EncodeToString(archive("fig-kiwi", chunks[0], zstdEnc(make([]byte, maxBytes+1))))),
	}
	files[filepath.Join("live", "budget.html")] = clipboardOf(t, budgetMsg(), true)
	if err := os.MkdirAll(filepath.Join(fixtureDir, "live"), 0o777); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(fixtureDir, name), []byte(content), 0o666); err != nil {
			t.Fatal(err)
		}
	}
}
