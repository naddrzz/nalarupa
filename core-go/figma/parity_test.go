// Parity with the Rust core: each Figma clipboard in testdata/figma is decoded and compared with
// `nalarupa-cli figma <clipboard.html>`. Without NALARUPA_CLI the comparison is with the committed goldens
// (<fixture>.json); with it, with the CLI's live output. NALARUPA_UPDATE_GOLDEN=1 (with NALARUPA_CLI)
// rewrites the goldens. Fixtures in testdata/figma/live (the 100k node budget, whose output is megabytes)
// are compared live only. Errors from the base64 and decompression libraries differ in wording: they are
// equal when both sides report one; the size cap's message is compared exactly.
package figma_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/figma"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

var fixtureDir = filepath.Join("..", "testdata", "figma")

const (
	libError = "bad Figma clipboard data: "
	tooBig   = "bad Figma clipboard data: decompressed size exceeds 256 MB"
)

// goJSON is what `nalarupa-cli figma` prints: [[node, [x, y, w, h]], ...], null, or {"error"}.
func goJSON(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	nodes, ok, err := figma.Decode(string(b))
	switch {
	case err != nil:
		o := jsonv.NewObject()
		o.Set("error", err.Error())
		return string(jsonv.Marshal(o))
	case !ok:
		return "null"
	}
	items := make([]jsonv.Value, len(nodes))
	for i, n := range nodes {
		items[i] = jsonv.NewArray(n.Value, jsonv.NewArray(jsonv.Float(n.X), jsonv.Float(n.Y), jsonv.Float(n.W), jsonv.Float(n.H)))
	}
	return string(jsonv.Marshal(jsonv.NewArray(items...)))
}

func rustJSON(t *testing.T, cli, path string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(cli, "figma", path)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s figma %s: %v\n%s", cli, path, err, stderr.String())
	}
	return strings.TrimRight(stdout.String(), "\r\n")
}

// libErr returns the message of a {"error"} that a base64 or decompression library worded.
func libErr(s string) (string, bool) {
	v, err := jsonv.Parse([]byte(s))
	if err != nil {
		return "", false
	}
	msg, ok := jsonv.Str(jsonv.Get(v, "error"))
	return msg, ok && strings.HasPrefix(msg, libError) && msg != tooBig
}

func same(t *testing.T, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	if g, ok := libErr(got); ok {
		if w, ok := libErr(want); ok {
			t.Logf("both fail in a library: go %q, rust %q", g, w)
			return
		}
	}
	t.Fatalf("output differs from Rust:\n%s", firstDiff(got, want))
}

func firstDiff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(i-200, 0)
	cut := func(s string) string { return s[from:min(i+200, len(s))] }
	return fmt.Sprintf("at byte %d\n  go:   …%s…\n  rust: …%s…", i, cut(a), cut(b))
}

func liveRun(t *testing.T) {
	t.Helper()
	// A file in the module written during the run keeps the test cache from replaying a live pass.
	if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
		t.Fatal(err)
	}
}

func TestParity(t *testing.T) {
	cli := os.Getenv("NALARUPA_CLI")
	update := os.Getenv("NALARUPA_UPDATE_GOLDEN") != ""
	if update && cli == "" {
		t.Fatal("NALARUPA_UPDATE_GOLDEN needs NALARUPA_CLI")
	}
	if cli != "" {
		liveRun(t)
	}
	fixtures, err := filepath.Glob(filepath.Join(fixtureDir, "*.html"))
	if err != nil || len(fixtures) == 0 {
		t.Fatal("no fixtures", err)
	}
	for _, fx := range fixtures {
		name := strings.TrimSuffix(filepath.Base(fx), ".html")
		t.Run(name, func(t *testing.T) {
			golden := filepath.Join(fixtureDir, name+".json")
			var want string
			if cli != "" {
				want = rustJSON(t, cli, fx)
				if update && os.WriteFile(golden, []byte(want+"\n"), 0o666) != nil {
					t.Fatal("cannot write golden")
				}
			} else {
				b, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				want = strings.TrimRight(string(b), "\r\n")
			}
			same(t, goJSON(t, fx), want)
		})
	}
}

func TestParityLive(t *testing.T) {
	cli := os.Getenv("NALARUPA_CLI")
	if cli == "" {
		t.Skip("the node budget fixture is compared live only: set NALARUPA_CLI to the release nalarupa-cli")
	}
	liveRun(t)
	fixtures, err := filepath.Glob(filepath.Join(fixtureDir, "live", "*.html"))
	if err != nil || len(fixtures) == 0 {
		t.Fatal("no live fixtures", err)
	}
	for _, fx := range fixtures {
		t.Run(strings.TrimSuffix(filepath.Base(fx), ".html"), func(t *testing.T) {
			same(t, goJSON(t, fx), rustJSON(t, cli, fx))
		})
	}
}
