package main

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCLICommands(t *testing.T) {
	t.Setenv("NALARUPA_DATA", t.TempDir())
	for _, name := range []string{"shapes", "effects"} {
		path := filepath.Join("..", "..", "testdata", "svg", name+".pen")
		id := map[string]string{"shapes": "fills", "effects": "shadows"}[name]
		t.Run(name+"/render", func(t *testing.T) {
			out := t.TempDir()
			if err := run([]string{"nalarupa-cli", "render", path, out, "2", "--offline"}); err != nil {
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
				if err := run([]string{"nalarupa-cli", cmd, path, id, out, "--offline"}); err != nil {
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
	if err := run([]string{"nalarupa-cli", "icon", filepath.Join(brand, "nalarupa.svg"), filepath.Join(brand, "nalarupa-small.svg"), out}); err != nil {
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
	t.Setenv("NALARUPA_DATA", t.TempDir())
	path := filepath.Join("..", "..", "testdata", "svg", "shapes.pen")
	if err := run([]string{"nalarupa-cli", "bench", path, "400", "300"}); err != nil {
		t.Fatal(err)
	}
}

func TestCLIErrors(t *testing.T) {
	if err := run([]string{"nalarupa-cli"}); err == nil || !strings.Contains(err.Error(), "missing argument") {
		t.Fatalf("got %v", err)
	}
	if err := run([]string{"nalarupa-cli", "nope"}); err == nil || err.Error() != "unknown command 'nope'" {
		t.Fatalf("got %v", err)
	}
}
