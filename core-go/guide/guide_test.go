package guide

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The topics are copies of core/guide/*.md: the Rust core embeds those, so both must stay byte for byte equal.
func TestCopiesMatchTheRustGuide(t *testing.T) {
	rust, err := filepath.Glob(filepath.Join("..", "..", "core", "guide", "*.md"))
	if err != nil || len(rust) == 0 {
		t.Skip("no core/guide next to core-go")
	}
	own, _ := files.ReadDir("md")
	if len(own) != len(rust) {
		t.Fatalf("%d topics, core/guide has %d", len(own), len(rust))
	}
	for _, f := range rust {
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := files.ReadFile("md/" + filepath.Base(f))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("md/%s differs from %s: copy it again", filepath.Base(f), f)
		}
	}
}

func fontsIn(text string) []string {
	var out []string
	for _, r := range strings.Split(text, `fontFamily: "`)[1:] {
		out = append(out, strings.SplitN(r, `"`, 2)[0])
	}
	return out
}

func TestWorkedExamplesDoNotAllTeachOneFont(t *testing.T) {
	var fonts []string
	for _, tp := range topics {
		fonts = append(fonts, fontsIn(tp.body)...)
	}
	if len(fonts) == 0 {
		t.Fatal("the guide has examples with fonts")
	}
	for _, f := range fonts {
		if f != fonts[0] {
			return
		}
	}
	t.Errorf("every example uses %s", fonts[0])
}

func TestTheDesignTopicOffersTypePairingsAndSlidesVaryLayouts(t *testing.T) {
	design, _ := Read("design")
	pairings := 0
	for _, l := range strings.Split(design, "\n") {
		if strings.HasPrefix(l, "- ") && strings.Contains(l, " + ") {
			pairings++
		}
	}
	if pairings < 3 || !strings.Contains(design, "Inter is one option among them") {
		t.Errorf("at least three display + text pairings: %s", design)
	}
	slides, _ := Read("slides")
	if strings.Contains(slides, "reuse its structure") || !strings.Contains(slides, "one design direction") {
		t.Errorf("slides vary their layouts: %s", slides)
	}
}

func TestStylesAreWorkedExamplesOfDesignDirections(t *testing.T) {
	list, bad := Directions("")
	if bad || !strings.Contains(list, "worked example") || !strings.Contains(list, "not a paste target") {
		t.Fatal(list)
	}
	if n := strings.Count(list, "\n- **"); n != len(directions) {
		t.Errorf("%d listed, want every example (%d)", n, len(directions))
	}
	for _, s := range directions {
		one, bad := Directions(s.name)
		if bad {
			t.Fatal(s.name)
		}
		for _, part := range []string{"Brief", "Framing", "Concept", "Palette", "Type", "Layout and spacing", "Signature move", "Why"} {
			if !strings.Contains(one, "**"+part) {
				t.Errorf("%s shows its %s", s.name, part)
			}
		}
		if strings.Contains(one, "SetVariables") {
			t.Errorf("%s offers no tokens to paste", s.name)
		}
	}
	if _, bad := Directions("no-such-style"); !bad {
		t.Error("an unknown name is still rejected")
	}
	if got, _ := Directions("  tide TABLE "); !strings.HasPrefix(got, "# Tide Table\n") {
		t.Error("names match ignoring ASCII case and surrounding space")
	}
}

func TestReadTopics(t *testing.T) {
	for _, name := range []string{"", " overview ", "OVERVIEW.md"} {
		if got, bad := Read(name); bad || got != Overview {
			t.Errorf("%q: the overview", name)
		}
	}
	if got, _ := Read("Schema.md.md"); got != topics[0].body {
		t.Error("every .md suffix is dropped and the name lowercased")
	}
	if got, bad := Read("nope"); !bad || got != "unknown topic 'nope'; topics: schema, execute, components, slides, design, code, designing" {
		t.Error(got)
	}
}
