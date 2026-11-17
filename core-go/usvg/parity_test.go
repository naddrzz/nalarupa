// Tree parity with usvg 0.47: each document in testdata/usvg (and testdata/usvg/filter, whose documents
// are filter-centric) is parsed here and by `nalarupa-cli usvg-tree`
// (see its doc in core/src/bin/cli.rs) with the options render.rs uses, and the trees are compared as JSON
// with every f32 as its bit pattern. Without NALARUPA_CLI the comparison is with the committed goldens
// (<stem>.golden.json); with it, with the CLI's live output. NALARUPA_UPDATE_GOLDEN=1 (with NALARUPA_CLI)
// rewrites the goldens.
package usvg_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

var corpus = filepath.Join("..", "testdata", "usvg")

func TestParity(t *testing.T) { parity(t, corpus, 60) }

func TestFilterParity(t *testing.T) { parity(t, filepath.Join(corpus, "filter"), 40) }

// parity compares the trees of the documents in dir (at least minDocs), with dir as the resources dir.
func parity(t *testing.T, dir string, minDocs int) {
	cli := os.Getenv("NALARUPA_CLI")
	update := os.Getenv("NALARUPA_UPDATE_GOLDEN") != ""
	if update && cli == "" {
		t.Fatal("NALARUPA_UPDATE_GOLDEN needs NALARUPA_CLI")
	}
	if cli != "" {
		// The test cache never rechecks files outside this module, so a rebuilt CLI would get a cached pass.
		// A file in the module written during the run is too new to cache: live runs always rerun.
		if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	var docs []string
	for _, ext := range []string{"*.svg", "*.svgz"} {
		m, err := filepath.Glob(filepath.Join(dir, ext))
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, m...)
	}
	if len(docs) < minDocs {
		t.Fatalf("%d documents in %s, want at least %d", len(docs), dir, minDocs)
	}
	for _, doc := range docs {
		stem := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(doc), ".svgz"), ".svg")
		t.Run(stem, func(t *testing.T) {
			golden := filepath.Join(dir, stem+".golden.json")
			var want []byte
			if cli != "" {
				out, err := exec.Command(cli, "usvg-tree", doc, "--resources", dir).Output()
				if err != nil {
					t.Fatalf("%s usvg-tree: %v", cli, err)
				}
				want = out
				if update {
					if err := os.WriteFile(golden, out, 0o666); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				var err error
				if want, err = os.ReadFile(golden); err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(doc)
			if err != nil {
				t.Fatal(err)
			}
			opt := usvg.DefaultOptions()
			opt.ResourcesDir = dir
			opt.ImageHrefResolver = svg.ImageHrefResolver()
			var got any
			if tree, err := usvg.FromData(data, &opt); err != nil {
				got = dumpError(err)
			} else {
				got = dumpTree(tree)
			}
			if path, g, w := diff(t, roundTrip(t, got), roundTrip(t, want)); path != "" {
				if why, ok := platformLibm[stem]; ok && cli != "" && matchesGolden(t, got, golden) {
					t.Logf("first difference at %s: got %s, want %s; Go equals the golden (%s)", path, g, w, why)
					return
				}
				t.Errorf("first difference at %s: got %s, want %s", path, g, w)
			}
		})
	}
}

// platformLibm lists, with why, the documents whose tree depends on an f32 libm function that a platform's C
// library rounds off by over half an ulp: Rust calls it, Go's is correctly rounded. Live, such a document may
// differ from the CLI as long as it equals the committed golden.
var platformLibm = map[string]string{
	"markers-orient": "glibc's atan2f(20, 50) is 0x3EC2D1BC, the correctly rounded value (Go's, and the golden's) 0x3EC2D1BB; the marker's rotation follows it",
}

// matchesGolden reports whether got equals the committed golden at path.
func matchesGolden(t *testing.T, got any, path string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	p, _, _ := diff(t, roundTrip(t, got), roundTrip(t, b))
	return p == ""
}

// roundTrip decodes v (or JSON bytes) into generic JSON values with exact numbers.
func roundTrip(t *testing.T, v any) any {
	t.Helper()
	b, ok := v.([]byte)
	if !ok {
		var err error
		if b, err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var out any
	if err := d.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// diff returns the JSON path of the first difference between got and want ("" when equal), with both
// values there.
func diff(t *testing.T, got, want any) (string, string, string) {
	show := func(v any) string {
		b, _ := json.Marshal(v)
		if len(b) > 200 {
			return string(b[:200]) + "..."
		}
		return string(b)
	}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return "$", show(got), show(want)
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok {
				return "$." + k, "missing", show(wv)
			}
			if p, a, b := diff(t, gv, wv); p != "" {
				return "." + k + p, a, b
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return "$", show(got), show(want)
		}
		for i := range w {
			if p, a, b := diff(t, g[i], w[i]); p != "" {
				return fmt.Sprintf("[%d]%s", i, p), a, b
			}
		}
	default:
		if !reflect.DeepEqual(got, want) {
			return "$", show(got), show(want)
		}
	}
	return "", "", ""
}

// dumpTree is the CLI's JSON for a tree: every f32 is its bit pattern, a transform is [sx,ky,kx,sy,tx,ty],
// a rect [left,top,right,bottom] and a size [w,h].
func dumpTree(t *usvg.Tree) map[string]any {
	ids := func(n int, id func(int) string) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = id(i)
		}
		return out
	}
	return map[string]any{
		"size":            dumpSize(t.Size()),
		"root":            dumpGroup(t.Root()),
		"linearGradients": ids(len(t.LinearGradients()), func(i int) string { return t.LinearGradients()[i].ID() }),
		"radialGradients": ids(len(t.RadialGradients()), func(i int) string { return t.RadialGradients()[i].ID() }),
		"patterns":        ids(len(t.Patterns()), func(i int) string { return t.Patterns()[i].ID() }),
		"clipPaths":       ids(len(t.ClipPaths()), func(i int) string { return t.ClipPaths()[i].ID() }),
		"masks":           ids(len(t.Masks()), func(i int) string { return t.Masks()[i].ID() }),
		"filters":         ids(len(t.Filters()), func(i int) string { return t.Filters()[i].ID() }),
	}
}

// dumpError is the CLI's JSON for a parse error.
func dumpError(err error) map[string]any {
	var e *usvg.Error
	if !errors.As(err, &e) {
		return map[string]any{"error": err.Error()}
	}
	out := map[string]any{"error": e.KindName()}
	if e.Kind == usvg.ErrParsingFailed && e.XML != nil {
		out["xml"] = e.XML.KindName()
	}
	return out
}

func dumpF(x float32) any { return math.Float32bits(x) }

func dumpTS(t skia.Transform) any {
	return []any{dumpF(t.SX), dumpF(t.KY), dumpF(t.KX), dumpF(t.SY), dumpF(t.TX), dumpF(t.TY)}
}

func dumpRect(r skia.Rect) any {
	return []any{dumpF(r.Left()), dumpF(r.Top()), dumpF(r.Right()), dumpF(r.Bottom())}
}

func dumpNZ(r skia.NonZeroRect) any {
	return []any{dumpF(r.Left()), dumpF(r.Top()), dumpF(r.Right()), dumpF(r.Bottom())}
}

func dumpSize(s skia.Size) any { return []any{dumpF(s.Width()), dumpF(s.Height())} }

func dumpGroup(g *usvg.Group) map[string]any {
	filters := make([]any, len(g.Filters()))
	for i, f := range g.Filters() {
		filters[i] = dumpFilter(f)
	}
	children := make([]any, len(g.Children()))
	for i, c := range g.Children() {
		children[i] = dumpNode(c)
	}
	var clip, mask any
	if c := g.ClipPath(); c != nil {
		clip = dumpClip(c)
	}
	if m := g.Mask(); m != nil {
		mask = dumpMask(m)
	}
	layer, _ := g.AbsLayerBoundingBox()
	return map[string]any{
		"kind":          "group",
		"id":            g.ID(),
		"transform":     dumpTS(g.Transform()),
		"absTransform":  dumpTS(g.AbsTransform()),
		"opacity":       dumpF(g.Opacity()),
		"blendMode":     g.BlendMode().String(),
		"isolate":       g.Isolate(),
		"clipPath":      clip,
		"mask":          mask,
		"filters":       filters,
		"bbox":          dumpRect(g.BoundingBox()),
		"absBbox":       dumpRect(g.AbsBoundingBox()),
		"strokeBbox":    dumpRect(g.StrokeBoundingBox()),
		"absStrokeBbox": dumpRect(g.AbsStrokeBoundingBox()),
		"layerBbox":     dumpNZ(g.LayerBoundingBox()),
		"absLayerBbox":  dumpNZ(layer),
		"children":      children,
	}
}

var (
	lineCaps      = [...]string{"butt", "round", "square"}
	lineJoins     = [...]string{"miter", "miter-clip", "round", "bevel"}
	fillRules     = [...]string{"nonzero", "evenodd"}
	paintOrders   = [...]string{"fill-and-stroke", "stroke-and-fill"}
	shapeModes    = [...]string{"optimize-speed", "crisp-edges", "geometric-precision"}
	imageModes    = [...]string{"optimize-quality", "optimize-speed", "smooth", "high-quality", "crisp-edges", "pixelated"}
	spreadMethods = [...]string{"pad", "reflect", "repeat"}
	maskKinds     = [...]string{"luminance", "alpha"}
)

func dumpNode(n usvg.Node) map[string]any {
	switch n := n.(type) {
	case *usvg.Group:
		return dumpGroup(n)
	case *usvg.Path:
		return dumpPath(n)
	case *usvg.Image:
		v := map[string]any{
			"kind":          "image",
			"id":            n.ID(),
			"visible":       n.IsVisible(),
			"size":          dumpSize(n.Size()),
			"renderingMode": imageModes[n.RenderingMode()],
			"absTransform":  dumpTS(n.AbsTransform()),
			"bbox":          dumpRect(n.BoundingBox()),
			"absBbox":       dumpRect(n.AbsBoundingBox()),
		}
		switch k := n.Kind().(type) {
		case usvg.ImageKindJPEG:
			v["format"], v["dataLen"] = "jpeg", len(k)
		case usvg.ImageKindPNG:
			v["format"], v["dataLen"] = "png", len(k)
		case usvg.ImageKindGIF:
			v["format"], v["dataLen"] = "gif", len(k)
		case usvg.ImageKindWEBP:
			v["format"], v["dataLen"] = "webp", len(k)
		case usvg.ImageKindSVG:
			v["format"], v["tree"] = "svg", dumpTree(k.Tree)
		}
		return v
	case *usvg.Text:
		return dumpText(n)
	}
	panic(fmt.Sprintf("unknown node %T", n))
}

func dumpPath(n *usvg.Path) map[string]any {
	return map[string]any{
		"kind":          "path",
		"id":            n.ID(),
		"visible":       n.IsVisible(),
		"fill":          dumpFill(n.Fill()),
		"stroke":        dumpStroke(n.Stroke()),
		"paintOrder":    paintOrders[n.PaintOrder()],
		"renderingMode": shapeModes[n.RenderingMode()],
		"absTransform":  dumpTS(n.AbsTransform()),
		"bbox":          dumpRect(n.BoundingBox()),
		"absBbox":       dumpRect(n.AbsBoundingBox()),
		"strokeBbox":    dumpRect(n.StrokeBoundingBox()),
		"absStrokeBbox": dumpRect(n.AbsStrokeBoundingBox()),
		"segments":      pathSegments(n.Data()),
	}
}

func pathSegments(p *skia.Path) []any {
	segments := []any{}
	it := p.Segments()
	for {
		s, ok := it.Next()
		if !ok {
			break
		}
		p := s.Pts
		switch s.Verb {
		case skia.VerbMove:
			segments = append(segments, []any{"M", dumpF(p[0].X), dumpF(p[0].Y)})
		case skia.VerbLine:
			segments = append(segments, []any{"L", dumpF(p[0].X), dumpF(p[0].Y)})
		case skia.VerbQuad:
			segments = append(segments, []any{"Q", dumpF(p[0].X), dumpF(p[0].Y), dumpF(p[1].X), dumpF(p[1].Y)})
		case skia.VerbCubic:
			segments = append(segments, []any{"C", dumpF(p[0].X), dumpF(p[0].Y), dumpF(p[1].X), dumpF(p[1].Y),
				dumpF(p[2].X), dumpF(p[2].Y)})
		case skia.VerbClose:
			segments = append(segments, []any{"Z"})
		}
	}
	return segments
}

// dumpFill is the CLI's JSON for a fill; nil is null.
func dumpFill(f *usvg.Fill) any {
	if f == nil {
		return nil
	}
	return map[string]any{"paint": dumpPaint(f.Paint()), "opacity": dumpF(f.Opacity()), "rule": fillRules[f.Rule()]}
}

// dumpStroke is the CLI's JSON for a stroke; nil is null.
func dumpStroke(s *usvg.Stroke) any {
	if s == nil {
		return nil
	}
	var dash any
	if s.Dasharray() != nil {
		d := make([]any, len(s.Dasharray()))
		for i, v := range s.Dasharray() {
			d[i] = dumpF(v)
		}
		dash = d
	}
	return map[string]any{
		"paint":      dumpPaint(s.Paint()),
		"dasharray":  dash,
		"dashoffset": dumpF(s.Dashoffset()),
		"miterlimit": dumpF(s.Miterlimit().Get()),
		"opacity":    dumpF(s.Opacity()),
		"width":      dumpF(s.Width()),
		"linecap":    lineCaps[s.Linecap()],
		"linejoin":   lineJoins[s.Linejoin()],
	}
}

func dumpPaint(p usvg.Paint) map[string]any {
	stops := func(ss []usvg.Stop) []any {
		out := make([]any, len(ss))
		for i, s := range ss {
			c := s.Color()
			out[i] = map[string]any{"offset": dumpF(s.Offset()), "color": []any{c.Red, c.Green, c.Blue},
				"opacity": dumpF(s.Opacity())}
		}
		return out
	}
	switch p := p.(type) {
	case usvg.Color:
		return map[string]any{"type": "color", "color": []any{p.Red, p.Green, p.Blue}}
	case *usvg.LinearGradient:
		return map[string]any{"type": "linear", "id": p.ID(), "x1": dumpF(p.X1()), "y1": dumpF(p.Y1()),
			"x2": dumpF(p.X2()), "y2": dumpF(p.Y2()), "transform": dumpTS(p.Transform()),
			"spread": spreadMethods[p.SpreadMethod()], "stops": stops(p.Stops())}
	case *usvg.RadialGradient:
		return map[string]any{"type": "radial", "id": p.ID(), "cx": dumpF(p.CX()), "cy": dumpF(p.CY()),
			"r": dumpF(p.R()), "fx": dumpF(p.FX()), "fy": dumpF(p.FY()), "fr": dumpF(p.FR()),
			"transform": dumpTS(p.Transform()), "spread": spreadMethods[p.SpreadMethod()], "stops": stops(p.Stops())}
	case *usvg.Pattern:
		return map[string]any{"type": "pattern", "id": p.ID(), "transform": dumpTS(p.Transform()),
			"rect": dumpNZ(p.Rect()), "root": dumpGroup(p.Root())}
	}
	panic(fmt.Sprintf("unknown paint %T", p))
}

func dumpClip(c *usvg.ClipPath) map[string]any {
	var sub any
	if s := c.ClipPath(); s != nil {
		sub = dumpClip(s)
	}
	return map[string]any{"id": c.ID(), "transform": dumpTS(c.Transform()), "clipPath": sub, "root": dumpGroup(c.Root())}
}

func dumpMask(m *usvg.Mask) map[string]any {
	var sub any
	if s := m.Mask(); s != nil {
		sub = dumpMask(s)
	}
	return map[string]any{"id": m.ID(), "rect": dumpNZ(m.Rect()), "kind": maskKinds[m.Kind()], "mask": sub,
		"root": dumpGroup(m.Root())}
}

var (
	colorInterpolations = [...]string{"sRGB", "linearRGB"}
	compositeOps        = [...]string{"over", "in", "out", "atop", "xor", "arithmetic"}
	edgeModes           = [...]string{"none", "duplicate", "wrap"}
	colorChannels       = [...]string{"R", "G", "B", "A"}
	morphologyOps       = [...]string{"erode", "dilate"}
	turbulenceKinds     = [...]string{"fractalNoise", "turbulence"}
)

func dumpFilter(f *usvg.Filter) map[string]any {
	primitives := make([]any, len(f.Primitives()))
	for i := range f.Primitives() {
		p := &f.Primitives()[i]
		primitives[i] = map[string]any{
			"rect":               dumpNZ(p.Rect()),
			"colorInterpolation": colorInterpolations[p.ColorInterpolation()],
			"result":             p.Result(),
			"kind":               dumpFilterKind(p.Kind()),
		}
	}
	return map[string]any{"id": f.ID(), "rect": dumpNZ(f.Rect()), "primitives": primitives}
}

func dumpInput(in usvg.FilterInput) any {
	switch in.Kind {
	case usvg.FilterInputSourceGraphic:
		return "SourceGraphic"
	case usvg.FilterInputSourceAlpha:
		return "SourceAlpha"
	}
	return map[string]any{"ref": in.Name}
}

func dumpRGB(c usvg.Color) any { return []any{c.Red, c.Green, c.Blue} }

func dumpTransfer(f usvg.TransferFunction) map[string]any {
	switch f := f.(type) {
	case usvg.TransferIdentity:
		return map[string]any{"type": "identity"}
	case usvg.TransferTable:
		return map[string]any{"type": "table", "values": dumpFloats(f)}
	case usvg.TransferDiscrete:
		return map[string]any{"type": "discrete", "values": dumpFloats(f)}
	case usvg.TransferLinear:
		return map[string]any{"type": "linear", "slope": dumpF(f.Slope), "intercept": dumpF(f.Intercept)}
	case usvg.TransferGamma:
		return map[string]any{"type": "gamma", "amplitude": dumpF(f.Amplitude), "exponent": dumpF(f.Exponent),
			"offset": dumpF(f.Offset)}
	}
	panic(fmt.Sprintf("unknown transfer function %T", f))
}

func dumpLight(l usvg.LightSource) map[string]any {
	switch l := l.(type) {
	case usvg.DistantLight:
		return map[string]any{"type": "distant", "azimuth": dumpF(l.Azimuth), "elevation": dumpF(l.Elevation)}
	case usvg.PointLight:
		return map[string]any{"type": "point", "x": dumpF(l.X), "y": dumpF(l.Y), "z": dumpF(l.Z)}
	case usvg.SpotLight:
		var cone any
		if l.HasLimitingConeAngle {
			cone = dumpF(l.LimitingConeAngle)
		}
		return map[string]any{"type": "spot", "x": dumpF(l.X), "y": dumpF(l.Y), "z": dumpF(l.Z),
			"pointsAtX": dumpF(l.PointsAtX), "pointsAtY": dumpF(l.PointsAtY), "pointsAtZ": dumpF(l.PointsAtZ),
			"specularExponent": dumpF(l.SpecularExponent), "limitingConeAngle": cone}
	}
	panic(fmt.Sprintf("unknown light source %T", l))
}

func dumpFilterKind(k usvg.FilterKind) map[string]any {
	switch fe := k.(type) {
	case *usvg.FeBlend:
		return map[string]any{"type": "blend", "in1": dumpInput(fe.Input1()), "in2": dumpInput(fe.Input2()),
			"mode": fe.Mode().String()}
	case *usvg.FeColorMatrix:
		var m map[string]any
		switch v := fe.Kind().(type) {
		case usvg.ColorMatrixMatrix:
			m = map[string]any{"type": "matrix", "values": dumpFloats(v)}
		case usvg.ColorMatrixSaturate:
			m = map[string]any{"type": "saturate", "value": dumpF(float32(v))}
		case usvg.ColorMatrixHueRotate:
			m = map[string]any{"type": "hueRotate", "value": dumpF(float32(v))}
		case usvg.ColorMatrixLuminanceToAlpha:
			m = map[string]any{"type": "luminanceToAlpha"}
		}
		return map[string]any{"type": "colorMatrix", "in": dumpInput(fe.Input()), "matrix": m}
	case *usvg.FeComponentTransfer:
		return map[string]any{"type": "componentTransfer", "in": dumpInput(fe.Input()),
			"r": dumpTransfer(fe.FuncR()), "g": dumpTransfer(fe.FuncG()), "b": dumpTransfer(fe.FuncB()),
			"a": dumpTransfer(fe.FuncA())}
	case *usvg.FeComposite:
		op := fe.Operator()
		var k any
		if op.Op == usvg.CompositeArithmetic {
			k = dumpFloats([]float32{op.K1, op.K2, op.K3, op.K4})
		}
		return map[string]any{"type": "composite", "in1": dumpInput(fe.Input1()), "in2": dumpInput(fe.Input2()),
			"operator": compositeOps[op.Op], "k": k}
	case *usvg.FeConvolveMatrix:
		m := fe.Matrix()
		return map[string]any{"type": "convolveMatrix", "in": dumpInput(fe.Input()),
			"targetX": m.TargetX(), "targetY": m.TargetY(), "columns": m.Columns(), "rows": m.Rows(),
			"data": dumpFloats(m.Data()), "divisor": dumpF(fe.Divisor()), "bias": dumpF(fe.Bias()),
			"edgeMode": edgeModes[fe.EdgeMode()], "preserveAlpha": fe.PreserveAlpha()}
	case *usvg.FeDiffuseLighting:
		return map[string]any{"type": "diffuseLighting", "in": dumpInput(fe.Input()),
			"surfaceScale": dumpF(fe.SurfaceScale()), "diffuseConstant": dumpF(fe.DiffuseConstant()),
			"lightingColor": dumpRGB(fe.LightingColor()), "light": dumpLight(fe.LightSource())}
	case *usvg.FeDisplacementMap:
		return map[string]any{"type": "displacementMap", "in1": dumpInput(fe.Input1()), "in2": dumpInput(fe.Input2()),
			"scale": dumpF(fe.Scale()), "xChannel": colorChannels[fe.XChannelSelector()],
			"yChannel": colorChannels[fe.YChannelSelector()]}
	case *usvg.FeDropShadow:
		return map[string]any{"type": "dropShadow", "in": dumpInput(fe.Input()), "dx": dumpF(fe.Dx()),
			"dy": dumpF(fe.Dy()), "stdDevX": dumpF(fe.StdDevX()), "stdDevY": dumpF(fe.StdDevY()),
			"color": dumpRGB(fe.Color()), "opacity": dumpF(fe.Opacity())}
	case *usvg.FeFlood:
		return map[string]any{"type": "flood", "color": dumpRGB(fe.Color()), "opacity": dumpF(fe.Opacity())}
	case *usvg.FeGaussianBlur:
		return map[string]any{"type": "gaussianBlur", "in": dumpInput(fe.Input()),
			"stdDevX": dumpF(fe.StdDevX()), "stdDevY": dumpF(fe.StdDevY())}
	case *usvg.FeImage:
		return map[string]any{"type": "image", "root": dumpGroup(fe.Root())}
	case *usvg.FeMerge:
		inputs := make([]any, len(fe.Inputs()))
		for i, in := range fe.Inputs() {
			inputs[i] = dumpInput(in)
		}
		return map[string]any{"type": "merge", "inputs": inputs}
	case *usvg.FeMorphology:
		return map[string]any{"type": "morphology", "in": dumpInput(fe.Input()),
			"operator": morphologyOps[fe.Operator()], "radiusX": dumpF(fe.RadiusX()), "radiusY": dumpF(fe.RadiusY())}
	case *usvg.FeOffset:
		return map[string]any{"type": "offset", "in": dumpInput(fe.Input()), "dx": dumpF(fe.Dx()), "dy": dumpF(fe.Dy())}
	case *usvg.FeSpecularLighting:
		return map[string]any{"type": "specularLighting", "in": dumpInput(fe.Input()),
			"surfaceScale": dumpF(fe.SurfaceScale()), "specularConstant": dumpF(fe.SpecularConstant()),
			"specularExponent": dumpF(fe.SpecularExponent()), "lightingColor": dumpRGB(fe.LightingColor()),
			"light": dumpLight(fe.LightSource())}
	case *usvg.FeTile:
		return map[string]any{"type": "tile", "in": dumpInput(fe.Input())}
	case *usvg.FeTurbulence:
		return map[string]any{"type": "turbulence", "baseFrequencyX": dumpF(fe.BaseFrequencyX()),
			"baseFrequencyY": dumpF(fe.BaseFrequencyY()), "numOctaves": fe.NumOctaves(), "seed": fe.Seed(),
			"stitchTiles": fe.StitchTiles(), "kind": turbulenceKinds[fe.Kind()]}
	}
	panic(fmt.Sprintf("unknown filter primitive %T", k))
}
