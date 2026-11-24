// Raster golden tests with tiny-skia 0.12: each scene of testdata/skia/raster-scenes.json is painted here,
// and the premultiplied RGBA (or mask) bytes are compared against the committed goldens (testdata/skia/raster/).
// A scene's "tolerance" allows that much difference per channel, only where a libm call or the CPU's
// approximate reciprocal is involved (its "why" says which).
//
// A scene that uses an earlier one (a pattern, a mask, draw_pixmap) gets the golden's bytes of that scene, so a
// mismatch does not cascade into the scenes built on it.
package skia

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

var (
	rasterScenesFile = filepath.Join("..", "testdata", "skia", "raster-scenes.json")
	rasterGoldenDir  = filepath.Join("..", "testdata", "skia", "raster")
)

var rasterBlendModes = map[string]BlendMode{
	"clear": BlendModeClear, "source": BlendModeSource, "destination": BlendModeDestination,
	"source-over": BlendModeSourceOver, "destination-over": BlendModeDestinationOver,
	"source-in": BlendModeSourceIn, "destination-in": BlendModeDestinationIn,
	"source-out": BlendModeSourceOut, "destination-out": BlendModeDestinationOut,
	"source-atop": BlendModeSourceAtop, "destination-atop": BlendModeDestinationAtop,
	"xor": BlendModeXor, "plus": BlendModePlus, "modulate": BlendModeModulate, "screen": BlendModeScreen,
	"overlay": BlendModeOverlay, "darken": BlendModeDarken, "lighten": BlendModeLighten,
	"color-dodge": BlendModeColorDodge, "color-burn": BlendModeColorBurn, "hard-light": BlendModeHardLight,
	"soft-light": BlendModeSoftLight, "difference": BlendModeDifference, "exclusion": BlendModeExclusion,
	"multiply": BlendModeMultiply, "hue": BlendModeHue, "saturation": BlendModeSaturation,
	"color": BlendModeColor, "luminosity": BlendModeLuminosity,
}

// rasterDone holds the finished scenes by name: a *Pixmap, a *Mask, or nil where Rust returns None.
type rasterDone map[string]any

func rasterGet(m map[string]any, key string) (any, error) {
	v, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("missing %s in %v", key, m)
	}
	return v, nil
}

func rasterFlag(m map[string]any, key string, def bool) (bool, error) {
	v, ok := m[key]
	if !ok {
		return def, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("bad %s in %v", key, m)
	}
	return b, nil
}

func rasterPick[T any](m map[string]any, key, def string, names map[string]T) (T, error) {
	name := def
	if v, ok := m[key].(string); ok {
		name = v
	}
	t, ok := names[name]
	if !ok {
		return t, fmt.Errorf("bad %s %s", key, name)
	}
	return t, nil
}

func rasterNums(v any, n int, what string) ([]float32, error) {
	l, err := list(v)
	if err != nil {
		return nil, err
	}
	f, err := nums(l)
	if err != nil {
		return nil, err
	}
	if len(f) != n {
		return nil, fmt.Errorf("%s needs %d values", what, n)
	}
	return f, nil
}

func rasterPoint(m map[string]any, key string) (Point, error) {
	v, err := rasterGet(m, key)
	if err != nil {
		return Point{}, err
	}
	p, err := rasterNums(v, 2, key)
	if err != nil {
		return Point{}, err
	}
	return Point{p[0], p[1]}, nil
}

func rasterTransform(m map[string]any) (Transform, error) {
	v, ok := m["transform"]
	if !ok {
		return Identity(), nil
	}
	t, err := rasterNums(v, 6, "a transform")
	if err != nil {
		return Transform{}, err
	}
	return TransformFromRow(t[0], t[1], t[2], t[3], t[4], t[5]), nil
}

func rasterColor(v any) (Color, error) {
	if m, ok := v.(map[string]any); ok {
		c, err := rasterNums(m["f"], 4, "a color")
		if err != nil {
			return Color{}, err
		}
		col, ok := ColorFromRGBA(c[0], c[1], c[2], c[3])
		if !ok {
			return Color{}, fmt.Errorf("bad color %v", v)
		}
		return col, nil
	}
	l, err := list(v)
	if err != nil || len(l) != 4 {
		return Color{}, fmt.Errorf("bad color %v", v)
	}
	var c [4]uint8
	for i, x := range l {
		n, ok := x.(json.Number)
		k, err := n.Int64()
		if !ok || err != nil || k < 0 || k > 255 {
			return Color{}, fmt.Errorf("bad color %v", v)
		}
		c[i] = uint8(k)
	}
	return ColorFromRGBA8(c[0], c[1], c[2], c[3]), nil
}

var (
	rasterSpreads   = map[string]SpreadMode{"pad": SpreadModePad, "reflect": SpreadModeReflect, "repeat": SpreadModeRepeat}
	rasterQualities = map[string]FilterQuality{"nearest": FilterQualityNearest, "bilinear": FilterQualityBilinear, "bicubic": FilterQualityBicubic}
	rasterRules     = map[string]FillRule{"winding": FillRuleWinding, "evenodd": FillRuleEvenOdd}
	rasterSpaces    = map[string]ColorSpace{"linear": ColorSpaceLinear, "gamma2": ColorSpaceGamma2, "simple-srgb": ColorSpaceSimpleSRGB, "full-srgb": ColorSpaceFullSRGBGamma}
	rasterCaps      = map[string]LineCap{"butt": LineCapButt, "round": LineCapRound, "square": LineCapSquare}
	rasterJoins     = map[string]LineJoin{"miter": LineJoinMiter, "miter-clip": LineJoinMiterClip, "round": LineJoinRound, "bevel": LineJoinBevel}
	rasterMaskTypes = map[string]MaskType{"alpha": MaskTypeAlpha, "luminance": MaskTypeLuminance}
)

func (d rasterDone) pixmap(m map[string]any) (*Pixmap, error) {
	name, _ := m["pixmap"].(string)
	if p, ok := d[name].(*Pixmap); ok {
		return p, nil
	}
	return nil, fmt.Errorf("%q is not an earlier, non-null pixmap scene", name)
}

func (d rasterDone) mask(m map[string]any) (*Mask, error) {
	name, ok := m["mask"].(string)
	if !ok {
		return nil, nil
	}
	if mk, ok := d[name].(*Mask); ok {
		return mk, nil
	}
	return nil, fmt.Errorf("%q is not an earlier, non-null mask scene", name)
}

func rasterStops(m map[string]any) ([]GradientStop, error) {
	v, err := rasterGet(m, "stops")
	if err != nil {
		return nil, err
	}
	l, err := list(v)
	if err != nil {
		return nil, err
	}
	stops := []GradientStop{}
	for _, st := range l {
		pair, err := list(st)
		if err != nil || len(pair) != 2 {
			return nil, fmt.Errorf("a stop is [pos, color]: %v", st)
		}
		pos, err := num(pair[0])
		if err != nil {
			return nil, err
		}
		c, err := rasterColor(pair[1])
		if err != nil {
			return nil, err
		}
		stops = append(stops, NewGradientStop(pos, c))
	}
	return stops, nil
}

// rasterShader builds a shader; nil (with no error) where the Rust constructor returns None.
func rasterShader(m map[string]any, done rasterDone) (Shader, error) {
	ts, err := rasterTransform(m)
	if err != nil {
		return nil, err
	}
	kind, _ := m["type"].(string)
	if kind == "solid" {
		v, err := rasterGet(m, "color")
		if err != nil {
			return nil, err
		}
		c, err := rasterColor(v)
		return NewSolidColor(c), err
	}
	if kind == "pattern" {
		p, err := done.pixmap(m)
		if err != nil {
			return nil, err
		}
		mode, err := rasterPick(m, "mode", "pad", rasterSpreads)
		if err != nil {
			return nil, err
		}
		q, err := rasterPick(m, "quality", "nearest", rasterQualities)
		if err != nil {
			return nil, err
		}
		opacity, err := optNum(m, "opacity", 1)
		if err != nil {
			return nil, err
		}
		return NewPattern(p.AsRef(), mode, q, opacity, ts), nil
	}
	stops, err := rasterStops(m)
	if err != nil {
		return nil, err
	}
	mode, err := rasterPick(m, "mode", "pad", rasterSpreads)
	if err != nil {
		return nil, err
	}
	var sh Shader
	var ok bool
	switch kind {
	case "linear":
		start, err1 := rasterPoint(m, "start")
		end, err2 := rasterPoint(m, "end")
		if err := rasterFirstErr(err1, err2); err != nil {
			return nil, err
		}
		sh, ok = NewLinearGradient(start, end, stops, mode, ts)
	case "radial":
		start, err1 := rasterPoint(m, "start")
		sr, err2 := optNum(m, "startRadius", 0)
		end, err3 := rasterPoint(m, "end")
		er, err4 := optNum(m, "endRadius", 0)
		if err := rasterFirstErr(err1, err2, err3, err4); err != nil {
			return nil, err
		}
		sh, ok = NewRadialGradient(start, sr, end, er, stops, mode, ts)
	case "sweep":
		c, err1 := rasterPoint(m, "center")
		a0, err2 := optNum(m, "startAngle", 0)
		a1, err3 := optNum(m, "endAngle", 360)
		if err := rasterFirstErr(err1, err2, err3); err != nil {
			return nil, err
		}
		sh, ok = NewSweepGradient(c, a0, a1, stops, mode, ts)
	default:
		return nil, fmt.Errorf("bad shader %v", m)
	}
	if !ok {
		return nil, nil
	}
	return sh, nil
}

func rasterFirstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// rasterPaint builds an op's paint; nil (with no error) where its shader constructor returns None.
func rasterPaint(o map[string]any, done rasterDone) (*Paint, error) {
	p, _ := o["paint"].(map[string]any)
	paint := DefaultPaint()
	if sm, ok := p["shader"].(map[string]any); ok {
		sh, err := rasterShader(sm, done)
		if err != nil || sh == nil {
			return nil, err
		}
		paint.Shader = sh
	}
	var err1, err2, err3, err4 error
	paint.BlendMode, err1 = rasterPick(p, "blend", "source-over", rasterBlendModes)
	paint.AntiAlias, err2 = rasterFlag(p, "aa", true)
	paint.ForceHQPipeline, err3 = rasterFlag(p, "hq", false)
	paint.Colorspace, err4 = rasterPick(p, "colorspace", "linear", rasterSpaces)
	return &paint, rasterFirstErr(err1, err2, err3, err4)
}

func rasterStroke(o map[string]any) (*Stroke, error) {
	st, ok := o["stroke"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing stroke in %v", o)
	}
	s := DefaultStroke()
	var err1, err2, err3, err4 error
	s.Width, err1 = optNum(st, "width", 1)
	s.MiterLimit, err2 = optNum(st, "miterLimit", 4)
	s.LineCap, err3 = rasterPick(st, "cap", "butt", rasterCaps)
	s.LineJoin, err4 = rasterPick(st, "join", "miter", rasterJoins)
	if err := rasterFirstErr(err1, err2, err3, err4); err != nil {
		return nil, err
	}
	if d, ok := st["dash"].(map[string]any); ok {
		l, err := list(d["array"])
		if err != nil {
			return nil, err
		}
		arr, err := nums(l)
		if err != nil {
			return nil, err
		}
		offset, err := optNum(d, "offset", 0)
		if err != nil {
			return nil, err
		}
		s.Dash = NewStrokeDash(arr, offset) // nil when invalid: strokes undashed, as in the CLI
	}
	return &s, nil
}

func rasterInt(o map[string]any, key string) (int32, error) {
	v, ok := o[key]
	if !ok {
		return 0, nil
	}
	n, ok := v.(json.Number)
	k, err := n.Int64()
	if !ok || err != nil || k != int64(int32(k)) {
		return 0, fmt.Errorf("bad %s in %v", key, o)
	}
	return int32(k), nil
}

func rasterPixmapOp(pm *Pixmap, o map[string]any, done rasterDone) error {
	ts, err := rasterTransform(o)
	if err != nil {
		return err
	}
	mask, err := done.mask(o)
	if err != nil {
		return err
	}
	name, _ := o["op"].(string)
	switch name {
	case "fill":
		v, err := rasterGet(o, "color")
		if err != nil {
			return err
		}
		c, err := rasterColor(v)
		if err != nil {
			return err
		}
		pm.Fill(c)
	case "fill_rect":
		r, err := rasterNums(o["rect"], 4, "a rect")
		if err != nil {
			return err
		}
		paint, err := rasterPaint(o, done)
		if err != nil {
			return err
		}
		if rect, ok := RectFromLTRB(r[0], r[1], r[2], r[3]); ok && paint != nil {
			pm.FillRect(rect, paint, ts, mask)
		}
	case "fill_path", "stroke_path":
		path, err := buildOps(o["path"])
		if err != nil {
			return err
		}
		paint, err := rasterPaint(o, done)
		if err != nil || path == nil || paint == nil {
			return err
		}
		if name == "fill_path" {
			rule, err := rasterPick(o, "rule", "winding", rasterRules)
			if err != nil {
				return err
			}
			pm.FillPath(path, paint, rule, ts, mask)
		} else {
			s, err := rasterStroke(o)
			if err != nil {
				return err
			}
			pm.StrokePath(path, paint, s, ts, mask)
		}
	case "draw_pixmap":
		p, _ := o["paint"].(map[string]any)
		paint := DefaultPixmapPaint()
		var err1, err2, err3, err4, err5 error
		paint.Opacity, err1 = optNum(p, "opacity", 1)
		paint.BlendMode, err2 = rasterPick(p, "blend", "source-over", rasterBlendModes)
		paint.Quality, err3 = rasterPick(p, "quality", "nearest", rasterQualities)
		x, err4 := rasterInt(o, "x")
		y, err5 := rasterInt(o, "y")
		if err := rasterFirstErr(err1, err2, err3, err4, err5); err != nil {
			return err
		}
		src, err := done.pixmap(o)
		if err != nil {
			return err
		}
		pm.DrawPixmap(x, y, src.AsRef(), &paint, ts, mask)
	case "apply_mask":
		if mask == nil {
			return fmt.Errorf("apply_mask needs a mask")
		}
		pm.ApplyMask(mask)
	default:
		return fmt.Errorf("bad pixmap op %v", o)
	}
	return nil
}

func rasterMaskOp(mk *Mask, o map[string]any) error {
	name, _ := o["op"].(string)
	switch name {
	case "invert":
		mk.Invert()
	case "fill_path", "intersect_path":
		path, err := buildOps(o["path"])
		if err != nil || path == nil {
			return err
		}
		rule, err1 := rasterPick(o, "rule", "winding", rasterRules)
		aa, err2 := rasterFlag(o, "aa", true)
		ts, err3 := rasterTransform(o)
		if err := rasterFirstErr(err1, err2, err3); err != nil {
			return err
		}
		if name == "fill_path" {
			mk.FillPath(path, rule, aa, ts)
		} else {
			mk.IntersectPath(path, rule, aa, ts)
		}
	default:
		return fmt.Errorf("bad mask op %v", o)
	}
	return nil
}

func rasterSize(sc map[string]any, key string) (uint32, error) {
	n, ok := sc[key].(json.Number)
	k, err := n.Int64()
	if !ok || err != nil || k < 0 || k != int64(uint32(k)) {
		return 0, fmt.Errorf("bad %s", key)
	}
	return uint32(k), nil
}

// rasterRunScene does what the CLI's skia_raster::run does for one scene: a *Pixmap, a *Mask or nil.
func rasterRunScene(sc map[string]any, done rasterDone) (out any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	var ops []any
	if v, ok := sc["ops"]; ok {
		if ops, err = list(v); err != nil {
			return nil, err
		}
	}
	ops2 := make([]map[string]any, len(ops))
	for i, o := range ops {
		if ops2[i], _ = o.(map[string]any); ops2[i] == nil {
			return nil, fmt.Errorf("op %d is not an object", i)
		}
	}
	switch sc["kind"] {
	case "pixmap":
		w, err1 := rasterSize(sc, "width")
		h, err2 := rasterSize(sc, "height")
		if err := rasterFirstErr(err1, err2); err != nil {
			return nil, err
		}
		pm, ok := NewPixmap(w, h)
		if !ok {
			return nil, nil
		}
		for _, o := range ops2 {
			if err := rasterPixmapOp(pm, o, done); err != nil {
				return nil, err
			}
		}
		return pm, nil
	case "mask":
		var mk *Mask
		if f, ok := sc["from"].(map[string]any); ok {
			ty, err := rasterPick(f, "type", "alpha", rasterMaskTypes)
			if err != nil {
				return nil, err
			}
			src, err := done.pixmap(f)
			if err != nil {
				return nil, err
			}
			mk = MaskFromPixmap(src.AsRef(), ty)
		} else {
			w, err1 := rasterSize(sc, "width")
			h, err2 := rasterSize(sc, "height")
			if err := rasterFirstErr(err1, err2); err != nil {
				return nil, err
			}
			if mk, ok = NewMask(w, h); !ok {
				return nil, nil
			}
		}
		for _, o := range ops2 {
			if err := rasterMaskOp(mk, o); err != nil {
				return nil, err
			}
		}
		return mk, nil
	}
	return nil, fmt.Errorf("bad kind %v", sc["kind"])
}

// rasterCompare reports the first pixel whose channels differ by more than tol, and the number of such
// pixels.
func rasterCompare(got, want []byte, width uint32, bpp int, tol int) error {
	if len(got) != len(want) {
		return fmt.Errorf("got %d bytes, want %d", len(got), len(want))
	}
	count, first := 0, -1
	for i := 0; i < len(want); i += bpp {
		for c := range bpp {
			if d := int(got[i+c]) - int(want[i+c]); d > tol || d < -tol {
				if first < 0 {
					first = i
				}
				count++
				break
			}
		}
	}
	if count == 0 {
		return nil
	}
	px := first / bpp
	return fmt.Errorf("%d of %d pixels differ (tolerance %d); first at (%d, %d): Go %v, Rust %v", count, len(want)/bpp, tol,
		px%int(width), px/int(width), got[first:first+bpp], want[first:first+bpp])
}

func TestRasterParity(t *testing.T) {
	data, err := os.ReadFile(rasterScenesFile)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := decode(data)
	if err != nil {
		t.Fatal(err)
	}
	scenes, err := list(raw)
	if err != nil {
		t.Fatal(err)
	}
	dir := rasterGoldenDir
	data, err = os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]*struct {
		Kind          string
		Width, Height uint32
	}
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	if len(index) != len(scenes) {
		t.Fatalf("%d results for %d scenes", len(index), len(scenes))
	}

	done := rasterDone{}
	for i, s := range scenes {
		sc, ok := s.(map[string]any)
		if !ok {
			t.Fatalf("scene %d is not an object", i)
		}
		name, _ := sc["name"].(string)
		want, ok := index[name]
		if !ok {
			t.Fatalf("no result for scene %q", name)
		}
		var wantBytes []byte
		if want != nil {
			ext := map[string]string{"pixmap": ".rgba", "mask": ".a8"}[want.Kind]
			if wantBytes, err = os.ReadFile(filepath.Join(dir, name+ext)); err != nil {
				t.Fatal(err)
			}
		}
		t.Run(name, func(t *testing.T) {
			out, err := rasterRunScene(sc, done)
			if err != nil {
				t.Fatal(err)
			}
			var tol int
			if n, ok := sc["tolerance"].(json.Number); ok {
				k, _ := n.Int64()
				tol = int(k)
			}
			switch got := out.(type) {
			case nil:
				if want != nil {
					t.Fatalf("got None, want a %dx%d %s", want.Width, want.Height, want.Kind)
				}
			case *Pixmap:
				if want == nil || want.Kind != "pixmap" || got.Width() != want.Width || got.Height() != want.Height {
					t.Fatalf("got a %dx%d pixmap, want %+v", got.Width(), got.Height(), want)
				}
				if err := rasterCompare(got.Data(), wantBytes, want.Width, 4, tol); err != nil {
					t.Error(err)
				}
			case *Mask:
				if want == nil || want.Kind != "mask" || got.Width() != want.Width || got.Height() != want.Height {
					t.Fatalf("got a %dx%d mask, want %+v", got.Width(), got.Height(), want)
				}
				if err := rasterCompare(got.Data(), wantBytes, want.Width, 1, tol); err != nil {
					t.Error(err)
				}
			}
		})
		// Later scenes get Rust's result as their source.
		if want != nil {
			size, _ := IntSizeFromWH(want.Width, want.Height)
			switch want.Kind {
			case "pixmap":
				done[name], ok = PixmapFromVec(wantBytes, size)
			case "mask":
				done[name], ok = MaskFromVec(wantBytes, size)
			}
			if !ok {
				t.Fatalf("bad result for scene %q", name)
			}
		}
	}
}
