// Canvas parity with the Rust core. Each scenario in testdata/canvas/*.json names a document and a list of
// steps, replayed here and by `nalarupa-cli canvas <doc> <scenario> <out>` (see canvas in
// core/src/bin/cli.rs): renders, document edits, syncs (the engine's shift_siblings, sync, settle_covered
// and refresh_slides), slides, clock ticks, hit tests, empty-space searches and Flat and live dumps. Both
// pin the slide clock and the live-zoom cost, so every step is deterministic. Each step's log entry must
// match (numbers to 1e-9; the dirty list as a set, since Rust lists deleted layers in hash order), and
// each render's RGB bytes must match within the scenario's tolerance (none unless stated, with its why).
//
// Without NALARUPA_CLI the comparison is with the committed goldens (testdata/canvas/golden: log.json per
// scenario plus gzipped frames), painted with the band count recorded there; with it, with the CLI's live
// output. NALARUPA_UPDATE_GOLDEN=1 (with NALARUPA_CLI) rewrites them. Scenarios marked "live" hold text and
// are compared live only.
package render

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
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
	Doc       string           `json:"doc"` // under testdata
	Live      bool             `json:"live"`
	Tolerance *tolerance       `json:"tolerance"`
	Steps     []map[string]any `json:"steps"`
}

type tolerance struct {
	MaxDelta int     `json:"maxDelta"`
	MaxShare float64 `json:"maxShare"`
	Why      string  `json:"why"`
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
func runScenario(t *testing.T, sc scenario, data string, threads int) ([]any, map[int][]byte) {
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
			px := r.Render(w, h, camAt(0), camAt(1), num(s, "zoom", 1), bg, str(s, "solo"), s["sharp"] != false)
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
	return logs, frames
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
	cli := os.Getenv("NALARUPA_CLI")
	update := os.Getenv("NALARUPA_UPDATE_GOLDEN") != ""
	if update && cli == "" {
		t.Fatal("NALARUPA_UPDATE_GOLDEN needs NALARUPA_CLI")
	}
	if cli != "" {
		// A file in the module written during the run keeps the test cache from replaying a live pass.
		if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(canvasDir, "*.json"))
	if len(files) == 0 {
		t.Fatal("no scenarios")
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var sc scenario
			if err := json.Unmarshal(b, &sc); err != nil {
				t.Fatal(err)
			}
			if sc.Live && cli == "" {
				t.Skip("holds text: compared live only")
			}
			data := dataDir(t)
			var want golden
			wantFrames := map[int][]byte{}
			if cli != "" {
				out := t.TempDir()
				cmd := exec.Command(cli, "canvas", filepath.Join(testdata, filepath.FromSlash(sc.Doc)), file, out)
				cmd.Env = append(os.Environ(), "NALARUPA_DATA="+data)
				if msg, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%s canvas: %v\n%s", cli, err, msg)
				}
				lb, err := os.ReadFile(filepath.Join(out, "log.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(lb, &want.Log); err != nil {
					t.Fatal(err)
				}
				want.Threads = min(max(runtime.NumCPU(), 1), 16)
				for i, e := range want.Log {
					if f, ok := e.(map[string]any)["file"].(string); ok {
						if wantFrames[i], err = os.ReadFile(filepath.Join(out, f)); err != nil {
							t.Fatal(err)
						}
					}
				}
				if update && !sc.Live {
					os.MkdirAll(goldenDir, 0o777)
					if err := os.WriteFile(filepath.Join(goldenDir, name+".json"), []byte(mustJSON(want)), 0o666); err != nil {
						t.Fatal(err)
					}
					for i, f := range wantFrames {
						gz(t, filepath.Join(goldenDir, fmt.Sprintf("%s.%d.rgb.gz", name, i)), f)
					}
				}
			} else {
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
			}
			threads := 0
			if cli == "" {
				threads = want.Threads
			}
			got, frames := runScenario(t, sc, data, threads)
			if len(got) != len(want.Log) {
				t.Fatalf("%d log entries, want %d", len(got), len(want.Log))
			}
			// Inserted nodes take fresh random ids: read Go's as Rust's from then on.
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
					if msg := compareRGB(frames[i], wf, w, sc.Tolerance); msg != "" {
						t.Errorf("step %d render: %s", i, msg)
					}
				}
			}
		})
	}
}
