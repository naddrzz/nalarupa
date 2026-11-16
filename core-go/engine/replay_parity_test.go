// Engine parity with the Rust core. Each sequence in testdata/replay/*.jsonl is replayed here (Replayer) and by
// `nalarupa-cli replay <script> --seed 42 --data data --out <dir>` (see replay in core/src/bin/cli.rs), each in a
// fresh work folder holding a copy of testdata/replay/files. Every response line must be equal as text (the same
// JSON with the same key order and number formatting), and so must the final document line, every render's RGB
// bytes and every file the run leaves in the work folder (saved documents, copied images, settings.json).
//
// Without NALARUPA_CLI the comparison is with the committed goldens (testdata/replay/golden: <name>.json with the
// lines and files, painted with the band count recorded there, plus gzipped frames); with it, with the CLI's live
// output. NALARUPA_UPDATE_GOLDEN=1 (with NALARUPA_CLI) rewrites them. A sequence whose first line is `# live` holds
// platform- or font-dependent output and is compared live only.
package engine

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

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
	e := New("data")
	e.Offline()
	e.Seed(replaySeed)
	e.Renderer.Threads = threads
	run := replayRun{Threads: threads, frames: map[int][]byte{}}
	r := NewReplayer(e)
	r.Frames = func(step int, rgb []byte) { run.frames[step] = rgb }
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

// rustReplay runs the script with the CLI in a fresh work folder.
func rustReplay(t *testing.T, cli, script string) replayRun {
	t.Helper()
	abs, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	dir := workDir(t)
	before := fixtures(t, dir)
	out := t.TempDir()
	cmd := exec.Command(cli, "replay", abs, "--seed", fmt.Sprint(replaySeed), "--data", "data", "--out", out)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s replay: %v\n%s", cli, err, stderr.String())
	}
	run := replayRun{Threads: min(max(runtime.NumCPU(), 1), 16), frames: map[int][]byte{}}
	for _, l := range strings.Split(strings.TrimRight(stdout.String(), "\r\n"), "\n") {
		run.Lines = append(run.Lines, strings.TrimRight(l, "\r"))
	}
	for _, l := range run.Lines {
		var v struct {
			Data struct {
				File string `json:"file"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(l), &v) == nil && v.Data.File != "" && strings.HasSuffix(v.Data.File, ".rgb") {
			b, err := os.ReadFile(filepath.Join(out, v.Data.File))
			if err != nil {
				t.Fatal(err)
			}
			run.frames[stepOf(t, v.Data.File)] = b
		}
	}
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

func gzipFile(t *testing.T, path string, b []byte) {
	t.Helper()
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(b)
	zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o666); err != nil {
		t.Fatal(err)
	}
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
	// The backend settings_get reports depends on it: blank is no key, on both sides.
	t.Setenv("ANTHROPIC_API_KEY", "")
	scripts, _ := filepath.Glob(filepath.Join(replayDir, "*.jsonl"))
	if len(scripts) == 0 {
		t.Fatal("no replay sequences")
	}
	goldenDir := filepath.Join(replayDir, "golden")
	for _, script := range scripts {
		name := strings.TrimSuffix(filepath.Base(script), ".jsonl")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(script)
			if err != nil {
				t.Fatal(err)
			}
			live := bytes.HasPrefix(src, []byte("# live"))
			if live && cli == "" {
				t.Skip("platform- or font-dependent: compared live only")
			}
			var want replayRun
			if cli != "" {
				want = rustReplay(t, cli, script)
				if update && !live {
					if err := os.WriteFile(filepath.Join(goldenDir, name+".json"), jsonIndent(want), 0o666); err != nil {
						t.Fatal(err)
					}
					for i, f := range want.frames {
						gzipFile(t, filepath.Join(goldenDir, fmt.Sprintf("%s.%d.rgb.gz", name, i)), f)
					}
				}
			} else {
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
			}
			threads := 0
			if cli == "" {
				threads = want.Threads
			}
			got := goReplay(t, script, threads)
			for i := range max(len(got.Lines), len(want.Lines)) {
				if i >= len(got.Lines) || i >= len(want.Lines) {
					t.Fatalf("%d lines, want %d", len(got.Lines), len(want.Lines))
				}
				if got.Lines[i] != want.Lines[i] {
					t.Errorf("line %d differs %s", i, firstDiff(got.Lines[i], want.Lines[i]))
				}
			}
			steps := make([]int, 0, len(want.frames))
			for i := range want.frames {
				steps = append(steps, i)
			}
			slices.Sort(steps)
			for _, i := range steps {
				g, w := got.frames[i], want.frames[i]
				if !bytes.Equal(g, w) {
					diff := 0
					for k := range min(len(g), len(w)) {
						if g[k] != w[k] {
							diff++
						}
					}
					t.Errorf("frame of step %d: %d bytes, want %d; %d channels differ", i, len(g), len(w), diff)
				}
			}
			for _, rel := range sortedKeys(want.Files, got.Files) {
				g, w := got.Files[rel], want.Files[rel]
				switch {
				case g == "":
					t.Errorf("%s: written by Rust only", rel)
				case w == "":
					t.Errorf("%s: written by Go only", rel)
				case g != w:
					gb, _ := base64.StdEncoding.DecodeString(g)
					wb, _ := base64.StdEncoding.DecodeString(w)
					t.Errorf("%s differs %s", rel, firstDiff(string(gb), string(wb)))
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

func jsonIndent(v any) []byte {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		panic(err)
	}
	return b
}
