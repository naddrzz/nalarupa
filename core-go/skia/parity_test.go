// Path geometry parity with tiny-skia-path 0.12: each case of testdata/skia/path-cases.json is built,
// transformed, dashed and stroked here and by `nalarupa-cli skia-path` (see its doc in core/src/bin/cli.rs),
// with every f32 compared by its bit pattern. Without NALARUPA_CLI the comparison is with the committed
// golden (path-cases.golden.json); with it, with the CLI's live output. NALARUPA_UPDATE_GOLDEN=1 (with
// NALARUPA_CLI) rewrites the golden. A case's "ulps" allows that many ulps per value, only for cases that
// go through sin, cos or acos, where Go's functions may round differently from the C library's.
package skia

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var (
	casesFile  = filepath.Join("..", "testdata", "skia", "path-cases.json")
	goldenFile = filepath.Join("..", "testdata", "skia", "path-cases.golden.json")
)

func decode(data []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	return v, d.Decode(&v)
}

// num reads a case number as the CLI does: a JSON number (as f64, then f32), "nan", "inf", "-inf" or
// "0x..." f32 bits.
func num(v any) (float32, error) {
	switch v := v.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		return float32(f), err
	case string:
		switch v {
		case "nan":
			return float32(math.NaN()), nil
		case "inf":
			return float32(math.Inf(1)), nil
		case "-inf":
			return float32(math.Inf(-1)), nil
		}
		if h, ok := strings.CutPrefix(v, "0x"); ok {
			b, err := strconv.ParseUint(h, 16, 32)
			return math.Float32frombits(uint32(b)), err
		}
	}
	return 0, fmt.Errorf("bad number %v", v)
}

func nums(v []any) ([]float32, error) {
	out := make([]float32, len(v))
	for i, x := range v {
		var err error
		if out[i], err = num(x); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func list(v any) ([]any, error) {
	l, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected a list: %v", v)
	}
	return l, nil
}

// op splits `[name, args...]`; args are numbers unless the op is "path".
func op(v any) (string, []float32, error) {
	l, err := list(v)
	if err != nil || len(l) == 0 {
		return "", nil, fmt.Errorf("bad op %v", v)
	}
	name, ok := l[0].(string)
	if !ok {
		return "", nil, fmt.Errorf("bad op %v", v)
	}
	if name == "path" {
		return name, nil, nil
	}
	args, err := nums(l[1:])
	return name, args, err
}

func buildOps(ops any) (*Path, error) {
	l, err := list(ops)
	if err != nil {
		return nil, err
	}
	pb := NewPathBuilder()
	for _, o := range l {
		name, v, err := op(o)
		if err != nil {
			return nil, err
		}
		switch {
		case name == "path":
			sub, err := buildOps(o.([]any)[1])
			if err != nil {
				return nil, err
			}
			if sub != nil {
				pb.PushPath(sub)
			}
		case name == "M" && len(v) == 2:
			pb.MoveTo(v[0], v[1])
		case name == "L" && len(v) == 2:
			pb.LineTo(v[0], v[1])
		case name == "Q" && len(v) == 4:
			pb.QuadTo(v[0], v[1], v[2], v[3])
		case name == "C" && len(v) == 6:
			pb.CubicTo(v[0], v[1], v[2], v[3], v[4], v[5])
		case name == "Z" && len(v) == 0:
			pb.Close()
		case name == "rect" && len(v) == 4:
			if r, ok := RectFromLTRB(v[0], v[1], v[2], v[3]); ok {
				pb.PushRect(r)
			}
		case name == "oval" && len(v) == 4:
			if r, ok := RectFromLTRB(v[0], v[1], v[2], v[3]); ok {
				pb.PushOval(r)
			}
		case name == "circle" && len(v) == 3:
			pb.PushCircle(v[0], v[1], v[2])
		default:
			return nil, fmt.Errorf("bad path op %v", o)
		}
	}
	return pb.Finish(), nil
}

func buildShape(s any) (*Path, error) {
	name, v, err := op(s)
	if err != nil {
		return nil, err
	}
	switch {
	case name == "rect" && len(v) == 4:
		if r, ok := RectFromLTRB(v[0], v[1], v[2], v[3]); ok {
			return PathFromRect(r), nil
		}
		return nil, nil
	case name == "oval" && len(v) == 4:
		if r, ok := RectFromLTRB(v[0], v[1], v[2], v[3]); ok {
			return PathFromOval(r), nil
		}
		return nil, nil
	case name == "circle" && len(v) == 3:
		return PathFromCircle(v[0], v[1], v[2]), nil
	}
	return nil, fmt.Errorf("bad shape %v", s)
}

func buildTransform(ops any) (Transform, bool, error) {
	l, err := list(ops)
	if err != nil {
		return Transform{}, false, err
	}
	ts := Identity()
	for _, o := range l {
		name, v, err := op(o)
		if err != nil {
			return ts, false, err
		}
		n := len(v)
		switch {
		case name == "row" && n == 6:
			ts = TransformFromRow(v[0], v[1], v[2], v[3], v[4], v[5])
		case name == "translate" && n == 2:
			ts = TransformFromTranslate(v[0], v[1])
		case name == "scale" && n == 2:
			ts = TransformFromScale(v[0], v[1])
		case name == "skew" && n == 2:
			ts = TransformFromSkew(v[0], v[1])
		case name == "rotate" && n == 1:
			ts = TransformFromRotate(v[0])
		case name == "rotateAt" && n == 3:
			ts = TransformFromRotateAt(v[0], v[1], v[2])
		case name == "preScale" && n == 2:
			ts = ts.PreScale(v[0], v[1])
		case name == "postScale" && n == 2:
			ts = ts.PostScale(v[0], v[1])
		case name == "preTranslate" && n == 2:
			ts = ts.PreTranslate(v[0], v[1])
		case name == "postTranslate" && n == 2:
			ts = ts.PostTranslate(v[0], v[1])
		case name == "preRotate" && n == 1:
			ts = ts.PreRotate(v[0])
		case name == "postRotate" && n == 1:
			ts = ts.PostRotate(v[0])
		case name == "preRotateAt" && n == 3:
			ts = ts.PreRotateAt(v[0], v[1], v[2])
		case name == "postRotateAt" && n == 3:
			ts = ts.PostRotateAt(v[0], v[1], v[2])
		case name == "preConcat" && n == 6:
			ts = ts.PreConcat(TransformFromRow(v[0], v[1], v[2], v[3], v[4], v[5]))
		case name == "postConcat" && n == 6:
			ts = ts.PostConcat(TransformFromRow(v[0], v[1], v[2], v[3], v[4], v[5]))
		case name == "invert" && n == 0:
			var ok bool
			if ts, ok = ts.Invert(); !ok {
				return ts, false, nil
			}
		default:
			return ts, false, fmt.Errorf("bad transform op %v", o)
		}
	}
	return ts, true, nil
}

func bits(x float32) uint32 { return math.Float32bits(x) }

func rectJSON(r Rect) []uint32 {
	return []uint32{bits(r.left), bits(r.top), bits(r.right), bits(r.bottom)}
}

func pathJSON(p *Path) any {
	if p == nil {
		return nil
	}
	segments := []any{}
	it := p.Segments()
	for {
		s, ok := it.Next()
		if !ok {
			break
		}
		n := map[PathVerb]int{VerbMove: 1, VerbLine: 1, VerbQuad: 2, VerbCubic: 3, VerbClose: 0}[s.Verb]
		seg := []any{"MLQCZ"[s.Verb : s.Verb+1]}
		for _, pt := range s.Pts[:n] {
			seg = append(seg, bits(pt.X), bits(pt.Y))
		}
		segments = append(segments, seg)
	}
	var tight any
	if r, ok := p.ComputeTightBounds(); ok {
		tight = rectJSON(r)
	}
	return map[string]any{"segments": segments, "bounds": rectJSON(p.bounds), "tight": tight}
}

func optNum(m map[string]any, key string, def float32) (float32, error) {
	if v, ok := m[key]; ok {
		return num(v)
	}
	return def, nil
}

// runCase does what the CLI's skia_path::case does.
func runCase(c map[string]any) (map[string]any, error) {
	out := map[string]any{}
	shape, hasShape := c["shape"]
	ops, hasPath := c["path"]
	var path *Path
	var err error
	switch {
	case hasShape && !hasPath:
		path, err = buildShape(shape)
	case hasPath && !hasShape:
		path, err = buildOps(ops)
	default:
		err = fmt.Errorf("a case needs one of path and shape")
	}
	if err != nil {
		return nil, err
	}
	out["built"] = pathJSON(path)
	if ops, ok := c["transform"]; ok {
		ts, ok, err := buildTransform(ops)
		if err != nil {
			return nil, err
		}
		out["transform"] = nil
		if ok {
			out["transform"] = []uint32{bits(ts.SX), bits(ts.KY), bits(ts.KX), bits(ts.SY), bits(ts.TX), bits(ts.TY)}
			out["resScale"] = bits(ComputeResolutionScale(ts))
			if mr, has := c["mapRect"]; has {
				l, err := list(mr)
				if err != nil {
					return nil, err
				}
				v, err := nums(l)
				if err != nil || len(v) != 4 {
					return nil, fmt.Errorf("mapRect needs 4 values")
				}
				out["mappedRect"] = nil
				if r, ok := RectFromLTRB(v[0], v[1], v[2], v[3]); ok {
					if r, ok := r.Transform(ts); ok {
						out["mappedRect"] = rectJSON(r)
					}
				}
			}
		}
		if path != nil && ok {
			path = path.Transform(ts)
		} else {
			path = nil
		}
		out["transformed"] = pathJSON(path)
	}
	if d, ok := c["dash"].(map[string]any); ok {
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
		res, err := optNum(d, "resScale", 1)
		if err != nil {
			return nil, err
		}
		dash := NewStrokeDash(arr, offset)
		out["dashOk"] = dash != nil
		if path != nil && dash != nil {
			path = path.Dash(dash, res)
		} else {
			path = nil
		}
		out["dashed"] = pathJSON(path)
	}
	if s, ok := c["stroke"].(map[string]any); ok {
		stroke := DefaultStroke()
		if stroke.Width, err = optNum(s, "width", 1); err != nil {
			return nil, err
		}
		if stroke.MiterLimit, err = optNum(s, "miterLimit", 4); err != nil {
			return nil, err
		}
		caps := map[string]LineCap{"butt": LineCapButt, "round": LineCapRound, "square": LineCapSquare}
		joins := map[string]LineJoin{"miter": LineJoinMiter, "miter-clip": LineJoinMiterClip, "round": LineJoinRound, "bevel": LineJoinBevel}
		if v, ok := s["cap"].(string); ok {
			if stroke.LineCap, ok = caps[v]; !ok {
				return nil, fmt.Errorf("bad cap %s", v)
			}
		}
		if v, ok := s["join"].(string); ok {
			if stroke.LineJoin, ok = joins[v]; !ok {
				return nil, fmt.Errorf("bad join %s", v)
			}
		}
		res, err := optNum(s, "resScale", 1)
		if err != nil {
			return nil, err
		}
		if path != nil {
			path = path.Stroke(&stroke, res)
		}
		out["stroked"] = pathJSON(path)
	}
	return out, nil
}

// ulpDiff is the distance in ulps between two f32 bit patterns.
func ulpDiff(a, b uint32) int64 {
	return int64(f32As2sCompliment(math.Float32frombits(a))) - int64(f32As2sCompliment(math.Float32frombits(b)))
}

// sameJSON compares decoded outputs; numbers are f32 bit patterns, allowed to differ by ulps.
func sameJSON(at string, got, want any, ulps int64) error {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return fmt.Errorf("%s: got %v, want %v", at, got, want)
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok {
				return fmt.Errorf("%s: missing %s", at, k)
			}
			if err := sameJSON(at+"."+k, gv, wv, ulps); err != nil {
				return err
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return fmt.Errorf("%s: got %v, want %v", at, got, want)
		}
		for i := range w {
			if err := sameJSON(fmt.Sprintf("%s[%d]", at, i), g[i], w[i], ulps); err != nil {
				return err
			}
		}
	case json.Number:
		g, ok := got.(json.Number)
		gb, err1 := strconv.ParseUint(string(g), 10, 32)
		wb, err2 := strconv.ParseUint(string(w), 10, 32)
		if !ok || err1 != nil || err2 != nil {
			return fmt.Errorf("%s: got %v, want %v", at, got, want)
		}
		// Without an allowance the bits must be equal: the ulp distance does not tell -0 from +0.
		if d := ulpDiff(uint32(gb), uint32(wb)); gb != wb && (ulps == 0 || d > ulps || d < -ulps) {
			return fmt.Errorf("%s: got %v (0x%08x), want %v (0x%08x)", at,
				math.Float32frombits(uint32(gb)), gb, math.Float32frombits(uint32(wb)), wb)
		}
	default:
		if got != want {
			return fmt.Errorf("%s: got %v, want %v", at, got, want)
		}
	}
	return nil
}

func TestParityPaths(t *testing.T) {
	data, err := os.ReadFile(casesFile)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := decode(data)
	if err != nil {
		t.Fatal(err)
	}
	cases, err := list(raw)
	if err != nil {
		t.Fatal(err)
	}
	cli := os.Getenv("NALARUPA_CLI")
	update := os.Getenv("NALARUPA_UPDATE_GOLDEN") != ""
	var want []byte
	switch {
	case cli != "":
		// The test cache never rechecks the CLI: a file in the module written during the run keeps it uncached.
		if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
			t.Fatal(err)
		}
		if !filepath.IsAbs(cli) {
			cli = filepath.Join("..", cli) // relative to the core-go module root
		}
		var stderr bytes.Buffer
		cmd := exec.Command(cli, "skia-path", casesFile)
		cmd.Stderr = &stderr
		if want, err = cmd.Output(); err != nil {
			t.Fatalf("%s skia-path: %v\n%s", cli, err, stderr.String())
		}
		if update {
			if err := os.WriteFile(goldenFile, want, 0o666); err != nil {
				t.Fatal(err)
			}
		}
	case update:
		t.Fatal("NALARUPA_UPDATE_GOLDEN needs NALARUPA_CLI")
	default:
		if want, err = os.ReadFile(goldenFile); err != nil {
			t.Fatal(err)
		}
	}
	wantAll, err := decode(want)
	if err != nil {
		t.Fatal(err)
	}
	wants, err := list(wantAll)
	if err != nil || len(wants) != len(cases) {
		t.Fatalf("%d results for %d cases (%v)", len(wants), len(cases), err)
	}
	for i, c := range cases {
		cm, ok := c.(map[string]any)
		if !ok {
			t.Fatalf("case %d is not an object", i)
		}
		name, _ := cm["name"].(string)
		t.Run(name, func(t *testing.T) {
			out, err := runCase(cm)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decode(data)
			if err != nil {
				t.Fatal(err)
			}
			var ulps int64
			if u, ok := cm["ulps"].(json.Number); ok {
				ulps, _ = u.Int64()
			}
			if err := sameJSON(name, got, wants[i], ulps); err != nil {
				t.Error(err)
			}
		})
	}
}
