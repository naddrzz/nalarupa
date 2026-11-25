// Engine golden tests: each sequence in testdata/replay/*.jsonl is replayed here (Replayer) in a fresh
// work folder holding a copy of testdata/replay/files, and every response line, the final document line,
// every render's RGB bytes and every file the run leaves in the work folder are compared against the
// committed goldens (testdata/replay/golden).
//
// The editor package's commands join the dispatcher here, so the editor sequences (editor-*.jsonl) replay too.
package engine_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	_ "github.com/naddrzz/nalarupa/core-go/editor"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

var replayDir = filepath.Join("..", "testdata", "replay")

const replaySeed = 42

// replayRun is one replay's output: its response lines (the final one last), its frames by step and the files it
// left in the work folder by slash path.
type replayRun struct {
	Threads int               `json:"threads"`
	Lines   []string          `json:"lines"`
	Files   map[string]string `json:"files"` // base64
	frames  map[int][]byte
	// shots are the screenshots of `tool` steps with `png: true`, by `<step>.<k>.png`.
	shots map[string][]byte
}

// workDir is a fresh work folder with the replay fixtures and, when the system has one, a font at font.ttf.
func workDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(replayDir, "files")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o777)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), b, 0o666)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{`C:\Windows\Fonts\arial.ttf`, "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", "/System/Library/Fonts/Supplemental/Arial.ttf"} {
		if b, err := os.ReadFile(f); err == nil {
			os.WriteFile(filepath.Join(dir, "font.ttf"), b, 0o666)
			break
		}
	}
	return dir
}

// fixtures is what the work folder held before the run, so only what the run wrote is compared.
func fixtures(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out[filepath.ToSlash(rel)], _ = os.ReadFile(p)
		}
		return nil
	})
	return out
}

// written is every file in dir that is new or changed since before.
func written(t *testing.T, dir string, before map[string][]byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	for rel, b := range fixtures(t, dir) {
		if old, ok := before[rel]; !ok || !bytes.Equal(old, b) {
			out[rel] = base64.StdEncoding.EncodeToString(b)
		}
	}
	return out
}

// goReplay runs the script with this package's Replayer in a fresh work folder (the process's working folder for
// the run, as the CLI's is).
func goReplay(t *testing.T, script string, threads int) replayRun {
	t.Helper()
	src, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	dir := workDir(t)
	before := fixtures(t, dir)
	t.Chdir(dir)
	e := engine.New("data")
	e.Offline()
	e.Seed(replaySeed)
	e.Renderer.Threads = threads
	run := replayRun{Threads: threads, frames: map[int][]byte{}, shots: map[string][]byte{}}
	r := engine.NewReplayer(e)
	r.Frames = func(step int, rgb []byte) { run.frames[step] = rgb }
	r.Shots = func(step, k int, png []byte) { run.shots[fmt.Sprintf("%d.%d.png", step, k)] = png }
	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		line, err := r.Line(sc.Text())
		if err != nil {
			t.Fatal(err)
		}
		if line != nil {
			run.Lines = append(run.Lines, string(jsonv.Marshal(line)))
		}
	}
	run.Lines = append(run.Lines, string(jsonv.Marshal(r.Final())))
	run.Files = written(t, dir, before)
	return run
}

func stepOf(t *testing.T, file string) int {
	var i int
	if _, err := fmt.Sscanf(file, "%d.rgb", &i); err != nil {
		t.Fatal(err)
	}
	return i
}

func gunzipFile(t *testing.T, path string) []byte {
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

// firstDiff is where two texts first differ, with some context.
func firstDiff(got, want string) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	from := max(i-80, 0)
	return fmt.Sprintf("at byte %d\n  got:  …%s\n  want: …%s", i, got[from:min(len(got), i+120)], want[from:min(len(want), i+120)])
}

func TestReplayParity(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	scripts, _ := filepath.Glob(filepath.Join(replayDir, "*.jsonl"))
	if len(scripts) == 0 {
		t.Fatal("no replay sequences")
	}
	goldenDir := filepath.Join(replayDir, "golden")
	for _, script := range scripts {
		name := strings.TrimSuffix(filepath.Base(script), ".jsonl")
		src, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.HasPrefix(src, []byte("# live")) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			var want replayRun
			b, err := os.ReadFile(filepath.Join(goldenDir, name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatal(err)
			}
			want.frames = map[int][]byte{}
			frames, _ := filepath.Glob(filepath.Join(goldenDir, name+".*.rgb.gz"))
			for _, f := range frames {
				want.frames[stepOf(t, strings.TrimPrefix(filepath.Base(f), name+"."))] = gunzipFile(t, f)
			}
			want.shots = map[string][]byte{}
			shots, _ := filepath.Glob(filepath.Join(goldenDir, name+".*.png"))
			for _, f := range shots {
				b, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				want.shots[strings.TrimPrefix(filepath.Base(f), name+".")] = b
			}
			got := goReplay(t, script, want.Threads)
			for i := range max(len(got.Lines), len(want.Lines)) {
				if i >= len(got.Lines) || i >= len(want.Lines) {
					t.Fatalf("%d lines, want %d", len(got.Lines), len(want.Lines))
				}
				wantLine := want.Lines[i]
				if runtime.GOOS != "windows" {
					wantLine = normalizeReplayPaths(wantLine)
				}
				if got.Lines[i] != wantLine {
					t.Errorf("line %d differs %s", i, firstDiff(got.Lines[i], wantLine))
				}
			}
			steps := make([]int, 0, len(want.frames))
			for i := range want.frames {
				steps = append(steps, i)
			}
			slices.Sort(steps)
			within := 0
			if m := framesWithin.FindSubmatch(src); m != nil {
				within, _ = strconv.Atoi(string(m[1]))
				if within > maxFramesWithin {
					t.Fatalf("# frames within %d: at most %d is allowed", within, maxFramesWithin)
				}
			}
			for _, i := range steps {
				g, w := got.frames[i], want.frames[i]
				if !bytes.Equal(g, w) {
					diff, maxDelta, pixels := 0, 0, 0
					for p := 0; p+3 <= min(len(g), len(w)); p += 3 {
						off := false
						for k := p; k < p+3; k++ {
							d := int(g[k]) - int(w[k])
							d = max(d, -d)
							maxDelta = max(maxDelta, d)
							if d > within {
								diff++
								off = true
							}
						}
						if off {
							pixels++
						}
					}
					if diff == 0 && len(g) == len(w) {
						continue
					}
					t.Errorf("frame of step %d: %d bytes, want %d; %d channels of %d pixels differ by over %d, max delta %d",
						i, len(g), len(w), diff, pixels, within, maxDelta)
				}
			}
			for _, f := range sortedKeys(names(want.shots), names(got.shots)) {
				if msg := comparePNG(got.shots[f], want.shots[f]); msg != "" {
					t.Errorf("screenshot %s: %s", f, msg)
				}
			}
			for _, rel := range sortedKeys(want.Files, got.Files) {
				g, w := got.Files[rel], want.Files[rel]
				switch {
				case g == "":
					t.Errorf("%s: missing from output", rel)
				case w == "":
					t.Errorf("%s: unexpected file in output", rel)
				case g != w:
					gb, _ := base64.StdEncoding.DecodeString(g)
					wb, _ := base64.StdEncoding.DecodeString(w)
					if msg := compareWritten(rel, gb, wb); msg != "" {
						t.Errorf("%s differs %s", rel, msg)
					}
				}
			}
		})
	}
}

func sortedKeys(maps ...map[string]string) []string {
	var keys []string
	for _, m := range maps {
		for k := range m {
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	slices.Sort(keys)
	return keys
}

// names is m's keys as a set for sortedKeys.
func names(m map[string][]byte) map[string]string {
	out := map[string]string{}
	for k := range m {
		out[k] = k
	}
	return out
}

// jpegDelta is the largest channel difference allowed between Go's and Rust's JPEG exports: the encoders differ
// (DCT and quantisation rounding, chroma subsampling; see the 4d and 7c parity).
const jpegDelta = 12

// maxFramesWithin caps a `# frames within` allowance: more would hide real rendering differences.
const maxFramesWithin = 2

var (
	// framesWithin is a sequence's `# frames within <n>: <why>` line: its frames may differ by n per channel.
	// n is at most maxFramesWithin.
	framesWithin = regexp.MustCompile(`(?m)^# frames within (\d+):`)
	pdfPage      = regexp.MustCompile(`/Type\s*/Page\b`)
	pdfMediaBox  = regexp.MustCompile(`/MediaBox\s*\[([^\]]*)\]`)
)

// pdfSummary is a PDF's page count and page sizes: the files are written by different PDF writers (the bytes are
// compared by the 4f parity).
func pdfSummary(b []byte) string {
	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		return "not a PDF"
	}
	var boxes []string
	for _, m := range pdfMediaBox.FindAllSubmatch(b, -1) {
		var nums []string
		for _, f := range strings.Fields(string(m[1])) {
			v, _ := strconv.ParseFloat(f, 64)
			nums = append(nums, strconv.FormatFloat(v, 'f', 3, 64))
		}
		boxes = append(boxes, strings.Join(nums, " "))
	}
	return fmt.Sprintf("%d pages %s", len(pdfPage.FindAll(b, -1)), strings.Join(boxes, ", "))
}

// compareWritten compares a file the run wrote by its kind: PNG by pixels, JPEG by pixels within jpegDelta, PDF by
// its pages, anything else byte for byte. "" when they match.
func compareWritten(name string, got, want []byte) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return comparePNG(got, want)
	case ".jpeg", ".jpg":
		g, err := jpeg.Decode(bytes.NewReader(got))
		if err != nil {
			return "Go: " + err.Error()
		}
		w, err := jpeg.Decode(bytes.NewReader(want))
		if err != nil {
			return "Rust: " + err.Error()
		}
		return comparePixels(g, w, jpegDelta)
	case ".pdf":
		if g, w := pdfSummary(got), pdfSummary(want); g != w {
			return fmt.Sprintf("PDF %s, Rust's %s", g, w)
		}
		return ""
	}
	return firstDiff(string(got), string(want))
}

// comparePNG compares two screenshots by their decoded pixels: "" when equal.
func comparePNG(got, want []byte) string {
	if got == nil || want == nil {
		return fmt.Sprintf("made by one side only (Go %d bytes, Rust %d bytes)", len(got), len(want))
	}
	g, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		return "Go: " + err.Error()
	}
	w, err := png.Decode(bytes.NewReader(want))
	if err != nil {
		return "Rust: " + err.Error()
	}
	return comparePixels(g, w, 0)
}

// comparePixels compares two images by their non-premultiplied pixels, each channel within delta: "" when they
// match.
func comparePixels(g, w image.Image, delta int) string {
	if g.Bounds() != w.Bounds() {
		return fmt.Sprintf("size %v, want %v", g.Bounds(), w.Bounds())
	}
	diff, most := 0, 0
	b := w.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			gc := color.NRGBAModel.Convert(g.At(x, y)).(color.NRGBA)
			wc := color.NRGBAModel.Convert(w.At(x, y)).(color.NRGBA)
			d := max(absDiff(gc.R, wc.R), absDiff(gc.G, wc.G), absDiff(gc.B, wc.B), absDiff(gc.A, wc.A))
			if d > delta {
				diff++
			}
			most = max(most, d)
		}
	}
	if diff > 0 {
		return fmt.Sprintf("%d pixels differ by more than %d (at most %d)", diff, delta, most)
	}
	return ""
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func normalizeReplayPaths(line string) string {
	v, err := jsonv.Parse([]byte(line))
	if err != nil {
		return line
	}
	obj := jsonv.Obj(v)
	if obj == nil {
		return line
	}
	dataObj := jsonv.Obj(jsonv.Get(obj, "data"))
	if dataObj == nil {
		return line
	}
	txt, ok := jsonv.Str(jsonv.Get(dataObj, "text"))
	if !ok || (!strings.Contains(txt, "Exported: ") && !strings.Contains(txt, "Export failed: ")) {
		return line
	}
	parts := strings.Split(txt, "\n")
	changed := false
	for i, p := range parts {
		if strings.HasPrefix(p, "Exported: ") || strings.HasPrefix(p, "Export failed: ") {
			n := strings.ReplaceAll(p, `\`, "/")
			if n != p {
				parts[i] = n
				changed = true
			}
		}
	}
	if !changed {
		return line
	}
	dataObj.Set("text", strings.Join(parts, "\n"))
	return string(jsonv.Marshal(obj))
}
