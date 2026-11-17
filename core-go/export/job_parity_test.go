// Export job parity with the Rust core. Each request of testdata/exportjob/fixtures.json is planned and run
// here (export.Plan, Settle without waiting, Replan when stale, Run; with CheckScriptExport and
// ClaimScriptFiles for "script" requests) and by `nalarupa-cli export <doc> <format> <out> <id>... --opts <json>
// [--script] --offline`, each on its own copy of the fixture folder. Both must write the same files (paths
// relative to the copy) or fail with the same error (the copy's path written as <W>). Contents are compared by
// format: SVG and HTML byte for byte, PNG and WebP by decoded pixels exactly, JPEG by decoded pixels within the
// tolerance in fixtures.json (Go's image/jpeg is not jpeg-encoder, see 4d), and PDF by page count, page sizes
// and image count (the files themselves are checked by the 4f PDF parity). Without NALARUPA_CLI the
// comparison is with the committed Rust results (testdata/exportjob/golden); with it, with the CLI's live
// output. NALARUPA_UPDATE_GOLDEN=1 (with NALARUPA_CLI) rewrites them. The "live" requests draw text and are
// compared live only.
package export_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/export"
	// The html formats are written through package html.
	_ "github.com/naddrzz/nalarupa/core-go/html"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/text"
)

var jobDir = filepath.Join(testdata, "exportjob")

type jobCase struct {
	Name   string          `json:"name"`
	Doc    string          `json:"doc"`
	Format string          `json:"format"`
	Out    string          `json:"out"`
	IDs    []string        `json:"ids"`
	Opts   json.RawMessage `json:"opts"`
	Script bool            `json:"script"`
	Pre    []string        `json:"pre"`
	// DocPath is how the document path is written: "" absolute (native separators), "slash" absolute with
	// forward slashes, "relative" as doc/<name> from W (the working folder of both runs).
	DocPath string `json:"docPath"`
}

// jobResult is what a request wrote, as written (the folder copy's path as <W>), or its error.
type jobResult struct {
	Files []string `json:"files,omitempty"`
	Error string   `json:"error,omitempty"`
}

type jobFixtures struct {
	Cases     []jobCase            `json:"cases"`
	Live      []jobCase            `json:"live"`
	Tolerance map[string]Tolerance `json:"tolerance"`
}

func (c jobCase) opts() string {
	if len(c.Opts) == 0 {
		return "{}"
	}
	return string(c.Opts)
}

// workCopy copies the folder holding the request's document to a fresh folder W (as W/doc) and creates the
// request's pre-existing files there, and a data folder W/data with the fixture icons. It returns W, the
// document path as the request writes it, and the data folder.
func workCopy(t *testing.T, c jobCase) (string, string, string) {
	t.Helper()
	w := t.TempDir()
	data := filepath.Join(w, "data")
	fillDataDir(t, data)
	src := filepath.Join(jobDir, filepath.Dir(filepath.FromSlash(c.Doc)))
	dst := filepath.Join(w, "doc")
	if err := os.MkdirAll(dst, 0o777); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "fixtures.json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil || os.WriteFile(filepath.Join(dst, e.Name()), b, 0o666) != nil {
			t.Fatal("cannot copy", e.Name(), err)
		}
	}
	for _, p := range c.Pre {
		f := filepath.Join(dst, filepath.FromSlash(p))
		if os.MkdirAll(filepath.Dir(f), 0o777) != nil || os.WriteFile(f, []byte("not exported"), 0o666) != nil {
			t.Fatal("cannot create", f)
		}
	}
	docPath := filepath.Join(dst, filepath.Base(c.Doc))
	switch c.DocPath {
	case "slash":
		docPath = filepath.ToSlash(docPath)
	case "relative":
		docPath = "doc/" + filepath.Base(c.Doc)
	}
	return w, docPath, data
}

func (c jobCase) outIn(w string) string {
	return strings.ReplaceAll(c.Out, "<W>", w)
}

// withW writes <W> for the folder copy w (in native or forward-slash form) in s.
func withW(s, w string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, w, "<W>"), filepath.ToSlash(w), "<W>")
}

// result is the written paths and the error, with <W> for w.
func result(w string, files []string, err string) jobResult {
	r := jobResult{Error: withW(err, w)}
	for _, f := range files {
		r.Files = append(r.Files, withW(f, w))
	}
	return r
}

// onDisk is where a written path (from result) is under w: <W> stands for w, and a relative path is relative
// to w (the working folder).
func onDisk(name, w string) string {
	if rest, ok := strings.CutPrefix(name, "<W>"); ok {
		return w + rest
	}
	return filepath.Join(w, name)
}

// relName is a written path relative to the folder copy, for the goldens.
func relName(name string) string {
	return filepath.FromSlash(strings.TrimLeft(strings.TrimPrefix(name, "<W>"), `/\`))
}

// rustJob runs the request through the CLI in a fresh copy and returns its result and folder.
func rustJob(t *testing.T, cli string, c jobCase) (jobResult, string) {
	t.Helper()
	w, docPath, data := workCopy(t, c)
	args := []string{"export", docPath, c.Format, c.outIn(w)}
	args = append(args, c.IDs...)
	args = append(args, "--opts", c.opts(), "--offline")
	if c.Script {
		args = append(args, "--script")
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(cli, args...)
	cmd.Env = append(os.Environ(), "NALARUPA_DATA="+data)
	cmd.Dir = w
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if !strings.HasPrefix(msg, "error: ") {
			t.Fatalf("%s export %s: %v\n%s", cli, c.Name, err, msg)
		}
		return result(w, nil, strings.TrimPrefix(msg, "error: ")), w
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	return result(w, files, ""), w
}

// agentPolicy is agent.rs `path_allowed` for "Write": inside base and never the open document.
func agentPolicy(file, base, docPath string) bool {
	f := resolved(file)
	if docPath != "" && resolved(docPath) == f {
		return false
	}
	rel, err := filepath.Rel(resolved(base), f)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolved is p absolute with `..` and links resolved in its deepest existing ancestor.
func resolved(p string) string {
	abs, _ := filepath.Abs(p)
	for a := abs; ; a = filepath.Dir(a) {
		if real, err := filepath.EvalSymlinks(a); err == nil {
			rest, _ := filepath.Rel(a, abs)
			return filepath.Join(real, rest)
		}
		if filepath.Dir(a) == a {
			return abs
		}
	}
}

// goJob runs the request through export.Plan in a fresh copy, as the CLI does.
func goJob(t *testing.T, c jobCase, fonts *text.Fonts) (jobResult, string) {
	t.Helper()
	w, docPath, data := workCopy(t, c)
	if c.DocPath == "relative" {
		t.Chdir(w) // the CLI runs there
	}
	ctx := openCtx(t, docPath, data, fonts)
	files, err := func() ([]string, error) {
		out := c.outIn(w)
		if c.Script {
			if err := export.CheckScriptExport(ctx.Doc, data, c.Format, out, agentPolicy); err != nil {
				return nil, err
			}
		}
		job, err := export.Plan(ctx, data, c.IDs, c.Format, out, jsonv.MustParse(c.opts()))
		if err != nil {
			return nil, err
		}
		if c.Script {
			if err := export.ClaimScriptFiles(job); err != nil {
				return nil, err
			}
		}
		if job.Stale() {
			job.Settle(nil, time.Now())
			if job, err = job.Replan(ctx, data); err != nil {
				return nil, err
			}
		}
		return job.Run()
	}()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return result(w, files, msg), w
}

var (
	pdfPage     = regexp.MustCompile(`/Type\s*/Page\b`)
	pdfPages    = regexp.MustCompile(`/Type\s*/Pages\b`)
	pdfMediaBox = regexp.MustCompile(`/MediaBox\s*\[([^\]]*)\]`)
	pdfImage    = regexp.MustCompile(`/Subtype\s*/Image\b`)
)

// pdfSummary is a PDF's page count, page sizes (in file order) and image count.
func pdfSummary(t *testing.T, b []byte) string {
	t.Helper()
	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
	var boxes []string
	for _, m := range pdfMediaBox.FindAllSubmatch(b, -1) {
		var nums []string
		for _, f := range strings.Fields(string(m[1])) {
			v, err := strconv.ParseFloat(f, 64)
			if err != nil {
				t.Fatalf("MediaBox %q", m[1])
			}
			nums = append(nums, strconv.FormatFloat(v, 'f', 3, 64))
		}
		boxes = append(boxes, strings.Join(nums, " "))
	}
	pages := len(pdfPage.FindAll(b, -1)) // not /Pages
	return strconv.Itoa(pages) + " pages " + strings.Join(boxes, ", ") + "; " + strconv.Itoa(len(pdfImage.FindAll(b, -1))) + " images"
}

// compareJobFile compares one written file by its format.
func compareJobFile(t *testing.T, name string, got, want []byte, tol Tolerance) {
	t.Helper()
	switch ext := strings.TrimPrefix(filepath.Ext(name), "."); ext {
	case "png", "webp", "jpeg":
		compareFiles(t, got, want, ext, tol)
	case "pdf":
		if g, w := pdfSummary(t, got), pdfSummary(t, want); g != w {
			t.Errorf("%s: PDF %s, Rust's %s", name, g, w)
		}
	default:
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from Rust's:\n%s", name, firstDifference(got, want))
		}
	}
}

func firstDifference(a, b []byte) string {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	lo := max(i-80, 0)
	return "at byte " + strconv.Itoa(i) + "\n  go:   " + string(a[lo:min(i+80, len(a))]) + "\n  rust: " + string(b[lo:min(i+80, len(b))])
}

func loadJobFixtures(t *testing.T) *jobFixtures {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(jobDir, "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f jobFixtures
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return &f
}

func TestExportJobParity(t *testing.T) {
	cli, live := setup(t)
	fx := loadJobFixtures(t)
	update := live && os.Getenv("NALARUPA_UPDATE_GOLDEN") != ""
	// Absolute: a relative-path request runs in its folder copy.
	goldenRoot, err := filepath.Abs(filepath.Join(jobDir, "golden"))
	if err != nil {
		t.Fatal(err)
	}
	resultsFile := filepath.Join(goldenRoot, "results.json")
	wants := map[string]jobResult{}
	if !live {
		b, err := os.ReadFile(resultsFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &wants); err != nil {
			t.Fatal(err)
		}
	}
	if update {
		if err := os.RemoveAll(goldenRoot); err != nil {
			t.Fatal(err)
		}
	}
	f := fonts(t)
	cases := fx.Cases
	if live {
		cases = append(append([]jobCase{}, cases...), fx.Live...)
	}
	for i, c := range cases {
		golden := i < len(fx.Cases)
		t.Run(c.Name, func(t *testing.T) {
			var want jobResult
			var rustDir string
			if live {
				want, rustDir = rustJob(t, cli, c)
				if golden {
					wants[c.Name] = want
				}
			} else {
				var ok bool
				if want, ok = wants[c.Name]; !ok {
					t.Fatal("no golden result: run with NALARUPA_CLI and NALARUPA_UPDATE_GOLDEN=1")
				}
				if runtime.GOOS != "windows" {
					// The goldens are written on Windows; Rust writes `/` here.
					want.Error = filepath.ToSlash(want.Error)
					for i, f := range want.Files {
						want.Files[i] = filepath.ToSlash(f)
					}
				}
				rustDir = filepath.Join(goldenRoot, c.Name)
			}
			got, goDir := goJob(t, c, f)
			if got.Error != want.Error || !slices.Equal(got.Files, want.Files) {
				t.Fatalf("got %+v\nwant %+v", got, want)
			}
			seen := map[string]bool{}
			for _, name := range want.Files {
				if seen[name] {
					continue
				}
				seen[name] = true
				rel := relName(name)
				wantFile := onDisk(name, rustDir)
				if !live {
					wantFile = filepath.Join(rustDir, rel)
				}
				w, err := os.ReadFile(wantFile)
				if err != nil {
					t.Fatal(err)
				}
				if update && golden {
					dst := filepath.Join(goldenRoot, c.Name, rel)
					if os.MkdirAll(filepath.Dir(dst), 0o777) != nil || os.WriteFile(dst, w, 0o666) != nil {
						t.Fatal("cannot write golden", dst)
					}
				}
				g, err := os.ReadFile(onDisk(name, goDir))
				if err != nil {
					t.Fatal(err)
				}
				compareJobFile(t, name, g, w, fx.Tolerance["exportjob/"+c.Name+"/"+filepath.ToSlash(rel)])
			}
		})
	}
	if update {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", " ")
		if err := enc.Encode(wants); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(goldenRoot, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(resultsFile, b.Bytes(), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	// Every golden request is still in the fixtures (a renamed one leaves no stale goldens behind).
	if !live {
		_ = filepath.WalkDir(goldenRoot, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() && p != goldenRoot && filepath.Dir(p) == goldenRoot && !slices.ContainsFunc(fx.Cases, func(c jobCase) bool { return c.Name == d.Name() }) {
				t.Errorf("golden folder %s has no request", d.Name())
			}
			return nil
		})
	}
}
