package main

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/engine"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nalarupa-cli-test-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("NALARUPA_DATA", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func runIsolated(t *testing.T, args []string) error {
	t.Helper()
	dir := t.TempDir()
	return runWith(args, func() *engine.Engine { return engine.New(dir) })
}

func TestCLICommands(t *testing.T) {
	for _, name := range []string{"shapes", "effects"} {
		path := filepath.Join("..", "..", "testdata", "svg", name+".pen")
		id := map[string]string{"shapes": "fills", "effects": "shadows"}[name]
		t.Run(name+"/render", func(t *testing.T) {
			out := t.TempDir()
			if err := runIsolated(t, []string{"nalarupa-cli", "render", path, out, "2", "--offline"}); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(out)
			if err != nil || len(entries) == 0 {
				t.Fatalf("no rendered files (%v)", err)
			}
			for _, e := range entries {
				b, err := os.ReadFile(filepath.Join(out, e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := png.Decode(bytes.NewReader(b)); err != nil {
					t.Errorf("%s: %v", e.Name(), err)
				}
			}
		})
		for _, cmd := range []string{"svg", "html"} {
			t.Run(name+"/"+cmd, func(t *testing.T) {
				out := filepath.Join(t.TempDir(), "out."+cmd)
				if err := runIsolated(t, []string{"nalarupa-cli", cmd, path, id, out, "--offline"}); err != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(out)
				if err != nil || len(b) == 0 {
					t.Fatalf("%s empty (%v)", cmd, err)
				}
			})
		}
	}
}

func TestCLIIcon(t *testing.T) {
	brand := filepath.Join("..", "..", "..", "app", "assets", "brand")
	out := t.TempDir()
	if err := runIsolated(t, []string{"nalarupa-cli", "icon", filepath.Join(brand, "nalarupa.svg"), filepath.Join(brand, "nalarupa-small.svg"), out}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Contains(names, "nalarupa.ico") || !slices.Contains(names, "icon-256.png") || !slices.Contains(names, "icon-16.png") {
		t.Fatalf("unexpected icon outputs: %v", names)
	}
}

func TestCLIBench(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "svg", "shapes.pen")
	if err := runIsolated(t, []string{"nalarupa-cli", "bench", path, "400", "300"}); err != nil {
		t.Fatal(err)
	}
	// Oversized dimensions are clamped to 8192 so PNG output never panics.
	prefix := filepath.Join(t.TempDir(), "shot")
	if err := runIsolated(t, []string{"nalarupa-cli", "bench", path, "9000", "16", prefix}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(prefix + "-fit.png")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 8192 || cfg.Height != 16 {
		t.Fatalf("clamped PNG size = %dx%d, want 8192x16", cfg.Width, cfg.Height)
	}
}

func TestCLIErrors(t *testing.T) {
	if err := runIsolated(t, []string{"nalarupa-cli"}); err == nil || !strings.Contains(err.Error(), "missing argument") {
		t.Fatalf("got %v", err)
	}
	if err := runIsolated(t, []string{"nalarupa-cli", "nope"}); err == nil || err.Error() != "unknown command 'nope'" {
		t.Fatalf("got %v", err)
	}
}
