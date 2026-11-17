// Parity of the user commands with the Rust CLI, live only (NALARUPA_CLI): `render` must write the same files
// decoding to the same pixels, `svg` and `html` the same bytes, for the offline fixtures in testdata/svg. Embedded
// PNGs are masked: Go encodes them with other bytes, and the svg package's parity test compares their pixels. The
// packages behind the commands have their own golden parity tests; this checks the commands' wiring.
package main

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

var fixtures = []string{"shapes", "effects"}

// dataPNG is an embedded PNG.
var dataPNG = regexp.MustCompile(`data:image/png;base64,[A-Za-z0-9+/=]*`)

func rust(t *testing.T, cli string, args ...string) {
	t.Helper()
	cmd := exec.Command(cli, args...)
	cmd.Env = append(os.Environ(), "NALARUPA_DATA="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", cli, args, err, out)
	}
}

func rgba(t *testing.T, path string) *image.RGBA {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(path, err)
	}
	out := image.NewRGBA(img.Bounds())
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestCLIParity(t *testing.T) {
	cli := os.Getenv("NALARUPA_CLI")
	if cli == "" {
		t.Skip("CLI parity is live only: set NALARUPA_CLI to the release nalarupa-cli")
	}
	// A file in the module written during the run keeps the test cache from replaying a live pass.
	if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NALARUPA_DATA", t.TempDir())
	for _, name := range fixtures {
		path := filepath.Join("..", "..", "testdata", "svg", name+".pen")
		t.Run(name+"/render", func(t *testing.T) {
			want, got := t.TempDir(), t.TempDir()
			rust(t, cli, "render", path, want, "2", "--offline")
			if err := run([]string{"nalarupa-cli", "render", path, got, "2", "--offline"}); err != nil {
				t.Fatal(err)
			}
			files := names(t, want)
			if g := names(t, got); len(files) == 0 || !slices.Equal(g, files) {
				t.Fatalf("files %v, want %v", g, files)
			}
			for _, f := range files {
				w, g := rgba(t, filepath.Join(want, f)), rgba(t, filepath.Join(got, f))
				if w.Bounds() != g.Bounds() || !bytes.Equal(w.Pix, g.Pix) {
					t.Errorf("%s: pixels differ (%v, want %v)", f, g.Bounds(), w.Bounds())
				}
			}
		})
		for _, cmd := range []string{"svg", "html"} {
			t.Run(name+"/"+cmd, func(t *testing.T) {
				dir := t.TempDir()
				id := map[string]string{"shapes": "fills", "effects": "shadows"}[name]
				want, got := filepath.Join(dir, "rust."+cmd), filepath.Join(dir, "go."+cmd)
				rust(t, cli, cmd, path, id, want, "--offline")
				if err := run([]string{"nalarupa-cli", cmd, path, id, got, "--offline"}); err != nil {
					t.Fatal(err)
				}
				w, err := os.ReadFile(want)
				if err != nil {
					t.Fatal(err)
				}
				g, err := os.ReadFile(got)
				if err != nil {
					t.Fatal(err)
				}
				if g, w := dataPNG.ReplaceAll(g, nil), dataPNG.ReplaceAll(w, nil); !bytes.Equal(g, w) {
					t.Errorf("%s differs from the Rust CLI (%d bytes, want %d)", cmd, len(g), len(w))
				}
			})
		}
	}
}
