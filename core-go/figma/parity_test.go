// Golden tests: each Figma clipboard in testdata/figma is decoded and compared with the committed goldens
// (<fixture>.json).
package figma_test

import (
	"fmt"
	"os"
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
			t.Logf("both fail in a library: got %q, want %q", g, w)
			return
		}
	}
	t.Fatalf("output differs from golden:\n%s", firstDiff(got, want))
}

func firstDiff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(i-200, 0)
	cut := func(s string) string { return s[from:min(i+200, len(s))] }
	return fmt.Sprintf("at byte %d\n  got:  …%s…\n  want: …%s…", i, cut(a), cut(b))
}

func TestParity(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join(fixtureDir, "*.html"))
	if err != nil || len(fixtures) == 0 {
		t.Fatal("no fixtures", err)
	}
	for _, fx := range fixtures {
		name := strings.TrimSuffix(filepath.Base(fx), ".html")
		t.Run(name, func(t *testing.T) {
			golden := filepath.Join(fixtureDir, name+".json")
			b, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.TrimRight(string(b), "\r\n")
			same(t, goJSON(t, fx), want)
		})
	}
}
