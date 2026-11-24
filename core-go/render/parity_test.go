// Canvas golden tests: each scenario in testdata/canvas/*.json names a document and a list of
// steps, replayed here and compared against the committed goldens (testdata/canvas/golden: log.json
// per scenario plus gzipped frames), painted with the band count recorded there. Set
// NALARUPA_UPDATE_GOLDEN=1 to regenerate the committed goldens from Go's renderer.
package render

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
)

var (
	testdata  = filepath.Join("..", "testdata")
	canvasDir = filepath.Join(testdata, "canvas")
	goldenDir = filepath.Join(canvasDir, "golden")
)

type scenario struct {
	Doc  string `json:"doc"` // under testdata
	Live bool   `json:"live"`
	// Tolerance applies to the frames served from a cached view that edits have repainted; others are exact.
	Tolerance *tolerance `json:"tolerance"`
	// CRT applies, live on Windows only, to every frame: Rust takes f64 cos and acos from the platform libm,
	// and the Windows CRT rounds some off by over half an ulp where Go's are correctly rounded
	// (skia/path64.go). A cubic the clipper chops near a tangent at a band's edge then lands a supersampled
	// row apart (a quarter of a pixel's coverage, at most 64 levels). Band edges follow the core count, so
	// which frames it hits depends on the machine.
	CRT   *tolerance       `json:"crt"`
	Steps []map[string]any `json:"steps"`
}

type tolerance struct {
	MaxDelta int     `json:"maxDelta"`
	MaxShare float64 `json:"maxShare"`
	Why      string  `json:"why"`
}

// tolFor is the tolerance of a frame: the scenario's for a repainted one, none otherwise.
func tolFor(sc scenario, repainted bool) *tolerance {
	if repainted {
		return sc.Tolerance
	}
	return nil
}

type golden struct {
	Threads int   `json:"threads"`
	Log     []any `json:"log"`
}

func num(m map[string]any, k string, d float64) float64 {
	if v, ok := m[k].(float64); ok {
		return v
	}
	return d
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func rectJSON(r Rect) []float64 { return []float64{r.X, r.Y, r.W, r.H} }

// runScenario replays the steps as `nalarupa-cli canvas` does: the log entries (as decoded JSON) and the
// RGB bytes of each render step.
// runScenario also returns which frames were served from a cached view that edits have repainted since its full
// paint (Rust repaints those differently, see the scenarios' tolerance). Each repaint is checked against a
// full paint of the same view: they may differ by 1 per channel at most.
func runScenario(t *testing.T, sc scenario, data string, threads int) ([]any, map[int][]byte, map[int]bool) {
	t.Helper()
	d, err := doc.Load(filepath.Join(testdata, filepath.FromSlash(sc.Doc)))
	if err != nil {
		t.Fatal(err)
	}
	resolve.Migrate(d.Root)
	d.EnsureIDs()
	fonts := text.New(filepath.Join(t.TempDir(), "fonts"))
	fonts.Online = false
	assets := svg.NewAssets(filepath.Join(data, "cache"))
	assets.Online = false
	cx := &Ctx{Doc: d, Fonts: fonts, Assets: assets}
	now := time.Now()
	r := &Renderer{Now: func() time.Time { return now }, Threads: threads}
	slides := func() []any {
		out := []any{}
		for _, s := range r.Slides {
			dx, dy, a := s.State(now)
			out = append(out, map[string]any{"id": s.ID, "state": []float64{dx, dy, a}})
		}
		return out
	}
	done := func(v any, err error) map[string]any {
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"ok": v}
	}
	slid := map[string]bool{}
	var logs []any
	frames := map[int][]byte{}
	repainted := map[int]bool{}
	// patched: the cached view holds repaints. rustPatched: Rust serves its frames from a repainted view that Go
	// replaced because its bands differed (see rebanded below).
	patched, rustPatched := false, false
	for i, s := range sc.Steps {
		op := str(s, "op")
		id := str(s, "id")
		index := -1
		if v, ok := s["index"].(float64); ok {
			index = int(v)
		}
		var e map[string]any
		switch op {
		case "render":
			w, h := uint32(num(s, "w", 800)), uint32(num(s, "h", 600))
			bg := [3]uint8{255, 255, 255}
			if c, ok := s["bg"].([]any); ok {
				for k := range 3 {
					bg[k] = 0
					if k < len(c) {
						bg[k] = uint8(c[k].(float64))
					}
				}
			}
			r.MsPerMP = 0
			if s["slow"] == true {
				r.MsPerMP = 1e9
			}
			cam, _ := s["cam"].([]any)
			camAt := func(k int) float64 {
				if k < len(cam) {
					if v, ok := cam[k].(float64); ok {
						return v
					}
				}
				return 0
			}
			zoom, sharp := num(s, "zoom", 1), s["sharp"] != false
			had, edited := r.view, len(r.dirty) > 0
			// A live zoom frame painted for the viewport alone (see renderView) is a full paint.
			live := had != nil && !sharp && r.MsPerMP == 0 && math.Abs(had.zoom-fmin(fmax(zoom, 0.01), 256)) >= 1e-9
			// Rust crops a frame of another height from its cached view too; Go paints it afresh when the bands
			// differ (see renderView), so where that view holds repaints Rust's frames are repainted ones until
			// its view is replaced.
			rebanded := false
			if had != nil && (patched || edited) && str(s, "solo") == had.solo && math.Abs(had.zoom-fmin(fmax(zoom, 0.01), 256)) < 1e-9 {
				fw, fh := min(max(w, 1), 8192), min(max(h, 1), 8192)
				ox := int64(math.Round(camAt(0)*had.zoom)) - int64(had.x0)
				oy := int64(math.Round(camAt(1)*had.zoom)) - int64(had.y0)
				vy := int32(oy) + had.y0
				rebanded = had.ph != fh+2*margin && !sameBandsOver(had.y0, had.ph, had.band, vy-margin, fh+2*margin, r.painter().band(fh+2*margin), vy, vy+int32(fh)) &&
					ox >= 0 && oy >= 0 && ox+int64(fw) <= int64(had.pw) && oy+int64(fh) <= int64(had.ph)
			}
			px := r.Render(w, h, camAt(0), camAt(1), zoom, bg, str(s, "solo"), sharp)
			switch v := r.view; {
			case v != had:
				patched = false
			case v != nil && edited:
				patched = true
				full := make([]byte, len(v.pix))
				if !r.painter().paintBands(v.solo, v.zoom, v.bg, v.x0, v.y0, v.pw, v.ph, v.band, full, nil) {
					t.Fatalf("step %d: full paint failed", i)
				}
				worst := 0
				for k := range full {
					worst = max(worst, absInt(int(full[k])-int(v.pix[k])))
				}
				if worst > 1 {
					t.Errorf("step %d: the repainted view differs from a full paint of it by up to %d", i, worst)
				}
			}
			switch {
			case rebanded:
				rustPatched = true
			case r.view != had:
				rustPatched = false
			}
			repainted[i] = patched && r.view == had && !live || rustPatched && !live
			rgb := make([]byte, 0, len(px)/4*3)
			for p := 0; p+3 < len(px); p += 4 {
				rgb = append(rgb, px[p], px[p+1], px[p+2])
			}
			frames[i] = rgb
			w, h = min(max(w, 1), 8192), min(max(h, 1), 8192)
			e = map[string]any{"width": w, "height": h, "file": fmt.Sprintf("%d.rgb", i), "slides": slides()}
		case "update":
			e = done(nil, d.Update(id, jsonv.MustParse(mustJSON(s["props"]))))
		case "insert":
			parent := str(s, "parent")
			if parent == "" {
				parent = "document"
			}
			e = done(d.Insert(parent, jsonv.MustParse(mustJSON(s["node"])), index))
		case "delete":
			e = done(nil, d.Delete(id))
		case "move":
			e = done(nil, d.MoveNode(id, str(s, "parent"), index))
		case "sync":
			passes := []any{}
			settled, dropped := false, false
			for {
				roots := r.ShiftSiblings(cx)
				rebuilt := r.Sync(cx)
				passes = append(passes, append([]string{}, rebuilt...))
				if len(r.Slides) > 0 {
					if r.SettleCovered() {
						settled = true
						continue
					}
					dropped = r.RefreshSlides(cx, rebuilt, roots)
				}
				break
			}
			dirty := []any{}
			for _, d := range r.Dirty() {
				dirty = append(dirty, rectJSON(d))
			}
			var bounds any
			if b, ok := r.Bounds(); ok {
				bounds = rectJSON(b)
			}
			e = map[string]any{"rebuilt": passes, "settled": settled, "dropped": dropped, "dirty": dirty, "order": append([]string{}, r.Order...), "bounds": bounds, "slides": slides()}
		case "slide":
			room := false
			if p, _, ok := d.ParentOf(id); ok {
				room = len(r.Slides) < MaxSlides && r.CanSlideInto(d, p)
			}
			pushed := !slid[id] && room
			slid[id] = true
			if pushed {
				r.Slides = append(r.Slides, NewSlide(id, now))
			}
			e = map[string]any{"pushed": pushed, "slides": slides()}
		case "tick":
			now = now.Add(time.Duration(num(s, "ms", 0)) * time.Millisecond)
			e = map[string]any{"expired": r.ExpireSlides(), "slides": slides()}
		case "hit":
			chain := HitChain(r, num(s, "x", 0), num(s, "y", 0))
			if chain == nil {
				chain = []string{}
			}
			e = map[string]any{"chain": chain}
		case "empty":
			var anchor *Flat
			if a := str(s, "anchor"); a != "" {
				anchor = r.Find(a)
			}
			dir := str(s, "dir")
			if dir == "" {
				dir = "right"
			}
			x, y := FindEmptySpace(r, num(s, "w", 0), num(s, "h", 0), dir, num(s, "pad", 0), anchor)
			e = map[string]any{"result": map[string]any{"x": x, "y": y}}
		case "flat":
			flat := []any{}
			for _, f := range r.Flats() {
				var parent, problem, m any
				if f.HasParent {
					parent = f.Parent
				}
				if f.Problem != "" {
					problem = f.Problem
				}
				if f.M != nil {
					m = f.M[:]
				}
				flat = append(flat, map[string]any{"id": f.ID, "name": f.Name, "ty": f.Type, "x": f.X, "y": f.Y, "w": f.W, "h": f.H, "depth": f.Depth,
					"parent": parent, "problem": problem, "placeholder": f.Placeholder, "pending": f.Pending, "faded": f.Faded, "m": m,
					"local": []any{[]float64{f.LocalX, f.LocalY}, f.Rotation, f.FlipX, f.FlipY}, "radius": f.Radius[:], "clip": f.Clip})
			}
			e = map[string]any{"flat": flat}
		case "live":
			live := []any{}
			for _, l := range r.Live() {
				var clip any
				if l.HasClip {
					clip = rectJSON(l.Clip)
				}
				cuts := []any{}
				for _, c := range l.Cuts {
					cuts = append(cuts, []any{rectJSON(c.Box), c.Radius[:]})
				}
				live = append(live, map[string]any{"id": l.Flat.ID, "clip": clip, "cuts": cuts})
			}
			e = map[string]any{"live": live}
		default:
			t.Fatalf("step %d: unknown op %q", i, op)
		}
		e["op"] = op
		logs = append(logs, decodeJSON(t, mustJSON(e)))
	}
	return logs, frames, repainted
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func decodeJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// sameJSON compares decoded JSON, numbers to 1e-9 (relative above 1); dirty lists compare as sets.
func sameJSON(got, want any, at string) string {
	switch w := want.(type) {
	case float64:
		g, ok := got.(float64)
		if !ok || math.Abs(g-w) > 1e-9*math.Max(1, math.Abs(w)) {
			return fmt.Sprintf("%s: got %v, want %v", at, got, want)
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return fmt.Sprintf("%s: got %v, want %v", at, mustJSON(got), mustJSON(want))
		}
		if strings.HasSuffix(at, ".dirty") {
			g, w = sortedRects(g), sortedRects(w)
		}
		for i := range w {
			if msg := sameJSON(g[i], w[i], fmt.Sprintf("%s[%d]", at, i)); msg != "" {
				return msg
			}
		}
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return fmt.Sprintf("%s: got %v, want %v", at, mustJSON(got), mustJSON(want))
		}
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if msg := sameJSON(g[k], w[k], at+"."+k); msg != "" {
				return msg
			}
		}
	default:
		if !reflect.DeepEqual(got, want) {
			return fmt.Sprintf("%s: got %v, want %v", at, got, want)
		}
	}
	return ""
}

func renameIDs(v any, ids map[string]string) any {
	switch x := v.(type) {
	case string:
		if n, ok := ids[x]; ok {
			return n
		}
	case []any:
		for i := range x {
			x[i] = renameIDs(x[i], ids)
		}
	case map[string]any:
		for k := range x {
			x[k] = renameIDs(x[k], ids)
		}
	}
	return v
}

func sortedRects(v []any) []any {
	out := slices.Clone(v)
	sort.SliceStable(out, func(i, j int) bool { return mustJSON(out[i]) < mustJSON(out[j]) })
	return out
}

// compareRGB reports the max channel delta, the differing pixels and the first one when they exceed tol.
func compareRGB(got, want []byte, w int, tol *tolerance) string {
	if len(got) != len(want) {
		return fmt.Sprintf("%d bytes, want %d", len(got), len(want))
	}
	maxDelta, differing, first := 0, 0, -1
	for i := 0; i < len(want); i += 3 {
		d := 0
		for c := range 3 {
			d = max(d, absInt(int(got[i+c])-int(want[i+c])))
		}
		if d > 0 {
			differing++
			if first < 0 {
				first = i
			}
		}
		maxDelta = max(maxDelta, d)
	}
	if differing == 0 {
		return ""
	}
	pixels := len(want) / 3
	share := float64(differing) / float64(pixels)
	if tol != nil && maxDelta <= tol.MaxDelta && share <= tol.MaxShare {
		return ""
	}
	return fmt.Sprintf("max channel delta %d, %d of %d pixels differ (%.4f%%); first at (%d,%d): got %v, want %v",
		maxDelta, differing, pixels, share*100, first/3%w, first/3/w, got[first:first+3], want[first:first+3])
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// dataDir is a fresh NALARUPA_DATA with the fixture icons in its asset cache.
func dataDir(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
	icons := filepath.Join(data, "cache", "icons")
	if err := os.MkdirAll(icons, 0o777); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(testdata, "svg", "icons", "*.svg"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || os.WriteFile(filepath.Join(icons, filepath.Base(f)), b, 0o666) != nil {
			t.Fatal("cannot copy icon", f, err)
		}
	}
	return data
}

func gz(t *testing.T, path string, b []byte) {
	t.Helper()
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(b)
	zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o666); err != nil {
		t.Fatal(err)
	}
}

func ungz(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCanvasParity(t *testing.T) {
	update := os.Getenv("NALARUPA_UPDATE_GOLDEN") != ""
	files, _ := filepath.Glob(filepath.Join(canvasDir, "*.json"))
	if len(files) == 0 {
		t.Fatal("no scenarios")
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var sc scenario
		if err := json.Unmarshal(b, &sc); err != nil {
			t.Fatal(err)
		}
		if sc.Live {
			continue
		}
		t.Run(name, func(t *testing.T) {
			data := dataDir(t)
			if update {
				threads := min(max(runtime.NumCPU(), 1), 16)
				got, frames, _ := runScenario(t, sc, data, threads)
				if err := os.MkdirAll(goldenDir, 0o777); err != nil {
					t.Fatal(err)
				}
				if old, _ := filepath.Glob(filepath.Join(goldenDir, name+".*.rgb.gz")); len(old) > 0 {
					for _, p := range old {
						_ = os.Remove(p)
					}
				}
				if err := os.WriteFile(filepath.Join(goldenDir, name+".json"), []byte(mustJSON(golden{Threads: threads, Log: got})), 0o666); err != nil {
					t.Fatal(err)
				}
				for i, f := range frames {
					gz(t, filepath.Join(goldenDir, fmt.Sprintf("%s.%d.rgb.gz", name, i)), f)
				}
				return
			}
			var want golden
			wantFrames := map[int][]byte{}
			gb, err := os.ReadFile(filepath.Join(goldenDir, name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(gb, &want); err != nil {
				t.Fatal(err)
			}
			for i, e := range want.Log {
				if _, ok := e.(map[string]any)["file"].(string); ok {
					wantFrames[i] = ungz(t, filepath.Join(goldenDir, fmt.Sprintf("%s.%d.rgb.gz", name, i)))
				}
			}
			got, frames, repainted := runScenario(t, sc, data, want.Threads)
			if len(got) != len(want.Log) {
				t.Fatalf("%d log entries, want %d", len(got), len(want.Log))
			}
			// Inserted nodes take fresh random ids: read Go's as golden's from then on.
			ids := map[string]string{}
			for i := range want.Log {
				op := str(want.Log[i].(map[string]any), "op")
				got[i] = renameIDs(got[i], ids)
				if op == "insert" {
					g, _ := got[i].(map[string]any)["ok"].(string)
					if w, ok := want.Log[i].(map[string]any)["ok"].(string); ok && g != "" {
						ids[g] = w
						got[i].(map[string]any)["ok"] = w
					}
				}
				if msg := sameJSON(got[i], want.Log[i], fmt.Sprintf("step %d (%s)", i, op)); msg != "" {
					t.Error(msg)
				}
				if wf, ok := wantFrames[i]; ok {
					w := int(want.Log[i].(map[string]any)["width"].(float64))
					if msg := compareRGB(frames[i], wf, w, tolFor(sc, repainted[i])); msg != "" {
						t.Errorf("step %d render: %s", i, msg)
					}
				}
			}
		})
	}
}
