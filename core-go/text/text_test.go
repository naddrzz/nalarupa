package text

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// system fonts are loaded once; tests that change the database build their own.
var shared *Fonts

func fonts(t *testing.T) *Fonts {
	t.Helper()
	if shared == nil {
		shared = offline(filepath.Join(os.TempDir(), "nalarupa-test-fonts"))
	}
	return shared
}

// offline is New with Online off, like the Rust CLI and tests: nothing is downloaded.
func offline(cache string) *Fonts {
	f := New(cache)
	f.Online = false
	return f
}

func arial(size float64) layout.TextStyle {
	return layout.TextStyle{Family: "Arial", Size: size, Weight: 400}
}

func texts(lines []resolve.Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

// systemFace is a regular, upright face loaded from a single-face file (not a collection), as (family,
// weight, bytes).
func systemFace(t *testing.T, f *Fonts) (string, uint16, []byte) {
	t.Helper()
	for _, x := range f.db.faces() {
		if x.weight == 400 && x.style == styleNormal && x.src.path != "" && x.index == 0 {
			if b, err := os.ReadFile(x.src.path); err == nil && !bytes.HasPrefix(b, []byte("ttcf")) {
				return x.families[0].name, x.weight, b
			}
		}
	}
	t.Skip("no system font installed")
	return "", 0, nil
}

// A font that arrives in the cache is loaded when Downloaded reports it, never while text is laid out.
func TestDownloadedFontLoadsFromCache(t *testing.T) {
	cache := t.TempDir()
	f := offline(cache)
	family, weight, data := systemFace(t, f)
	for _, x := range f.db.faces() {
		if x.hasName(family) {
			f.db.remove(x.id)
		}
	}
	if f.HasFamily(family) {
		t.Fatal("family still loaded")
	}
	st := layout.TextStyle{Family: family, Size: 16, Weight: weight}
	f.Ensure(st)
	if f.HasFamily(family) || f.Generation != 0 {
		t.Fatal("nothing on disk: text falls back, no block")
	}
	if err := os.WriteFile(staticFile(cache, family, weight, false), data, 0o666); err != nil {
		t.Fatal(err)
	}
	if !f.Downloaded(family, weight, false) {
		t.Fatal("the cached font was not added")
	}
	if !f.HasFamily(family) || f.Generation != 1 {
		t.Fatalf("has %v, generation %d", f.HasFamily(family), f.Generation)
	}
	if id, ok := f.face(st); !ok || !f.db.face(id).hasName(family) {
		t.Fatal("the family does not resolve to the new face")
	}
	if f.Downloaded(family, weight, false) || f.Generation != 1 {
		t.Fatal("a second report adds nothing")
	}
}

func TestLineHeightsRound(t *testing.T) {
	f := fonts(t)
	st := arial(15)
	st.LineHeight = 1.5
	if m := f.Metrics(st); m.LineH != 23 { // round(22.5)
		t.Fatalf("line height %v", m.LineH)
	}
	st.LineHeight = 0
	if h := f.Metrics(st).LineH; h != math.Trunc(h) {
		t.Fatalf("natural line height %v is not whole", h)
	}
}

func TestLetterSpacingAndKerning(t *testing.T) {
	f := fonts(t)
	st := arial(20)
	plain := f.Width("AVA", st)
	st.LetterSpacing = 2
	if d := f.Width("AVA", st) - plain - 6; math.Abs(d) > 1e-9 {
		t.Fatalf("trailing spacing counts: off by %v", d)
	}
	st.LetterSpacing = 0
	separate := f.Width("A", st)*2 + f.Width("V", st)
	if _, ok := f.face(st); ok && !(plain < separate-0.1) {
		t.Fatalf("AV is kerned (%v vs %v)", plain, separate)
	}
}

func TestWrapsAtUAX14Opportunities(t *testing.T) {
	f := fonts(t)
	st := arial(16)
	w := f.Width("hello world", st)
	lines := f.Wrap("hello world again", w+1, st)
	if got := texts(lines); !slices.Equal(got, []string{"hello world", "again"}) {
		t.Fatal(got)
	}
	if !lines[0].Soft || lines[1].Soft {
		t.Fatal("only the wrapped line is soft")
	}
	if math.Abs(lines[0].Width-w) > 1e-6 {
		t.Fatal("trailing space hangs")
	}
	// CJK breaks between ideographs; zero width puts one word (here one ideograph) per line.
	if n := len(f.Wrap("漢字かな", 0, st)); n != 4 {
		t.Fatalf("%d CJK lines", n)
	}
	hard := f.Wrap("a\nb", 1000, st)
	if got := texts(hard); !slices.Equal(got, []string{"a", "b"}) || hard[0].Soft || hard[1].Soft {
		t.Fatal(hard)
	}
	// Overlong words break between clusters, never producing empty lines.
	abc := f.Width("abc", st)
	if got := texts(f.Wrap("abcdefgh", abc+0.5, st)); !slices.Equal(got, []string{"abc", "def", "gh"}) {
		t.Fatal(got)
	}
	// Zero width breaks only at word boundaries, never inside a word.
	if got := texts(f.Wrap("one two three", 0, st)); !slices.Equal(got, []string{"one", "two", "three"}) {
		t.Fatal(got)
	}
	if got := texts(f.Wrap(" one two", 0, st)); !slices.Equal(got, []string{" one", "two"}) {
		t.Fatal(got)
	}
	// An infinite width breaks only at `\n`.
	if got := texts(f.Wrap("one two\r\nthree ", math.Inf(1), st)); !slices.Equal(got, []string{"one two", "three"}) {
		t.Fatal(got)
	}
}

func TestLineBreaks(t *testing.T) {
	cases := map[string][]lineBreak{
		"a b \nc":      {{2, false}, {5, true}, {6, true}},
		"Hello world!": {{6, false}, {12, true}},
		"":             nil, // no break at all, like unicode-linebreak
		"a-b 50%":      {{2, false}, {4, false}, {7, true}},
	}
	for s, want := range cases {
		if got := lineBreaks(s); !slices.Equal(got, want) {
			t.Errorf("%q: %v, want %v", s, got, want)
		}
	}
	for cp, want := range map[rune]uint8{0xA: 2, 0xe01ef: 3, 0x10ffff: lbXX, 0x2CF3: 29, 0x4E00: 36} {
		if got := breakProperty(cp); got != want {
			t.Errorf("U+%04X: class %d, want %d", cp, got, want)
		}
	}
}

// Without any face, ASCII is 0.55 em and the rest 1 em, with the fallback vertical metrics.
func TestNoFaces(t *testing.T) {
	f := &Fonts{db: newDB(), cache: t.TempDir(), tried: map[string]bool{}}
	f.fontsChanged()
	st := arial(10)
	if w := f.Width("ab漢", st); math.Abs(w-(0.55+0.55+1)*10) > 1e-9 {
		t.Fatalf("width %v", w)
	}
	if m := f.Metrics(st); m.Ascent != 9 || math.Abs(m.Descent-2.2) > 1e-12 || m.LineH != 11 {
		t.Fatalf("metrics %+v", m)
	}
	if got := texts(f.Wrap("ab cd", 12, st)); !slices.Equal(got, []string{"ab", "cd"}) {
		t.Fatal(got)
	}
}

// Broken font files are skipped; the other faces of the folder still load.
func TestCorruptFontSkipped(t *testing.T) {
	probe := fonts(t)
	family, _, data := systemFace(t, probe)
	cache := t.TempDir()
	for name, b := range map[string][]byte{
		"garbage.ttf":   []byte("not a font at all"),
		"empty.ttf":     nil,
		"truncated.ttf": data[:min(len(data), 200)],
		"bad.ttc":       {'t', 't', 'c', 'f', 0, 1, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF},
		"good.ttf":      data,
	} {
		if err := os.WriteFile(filepath.Join(cache, name), b, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	f := offline(cache)
	n := 0
	for _, x := range f.db.faces() {
		if filepath.Dir(x.src.path) == cache {
			n++
			if filepath.Base(x.src.path) != "good.ttf" || !x.hasName(family) {
				t.Errorf("loaded %s from %s", x.families[0].name, x.src.path)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d faces from the cache, want 1", n)
	}
}

// openDigests reaches into go-text's unexported accelerators; a go-text upgrade that renames them must fail
// here instead of silently bringing back skipped lookups.
func TestOpenDigestsFindsGoTextAccelerators(t *testing.T) {
	f := fonts(t)
	for _, x := range f.db.faces() {
		hb := f.shaper(x.id, 400)
		if hb == nil || len(x.font().GPOS.Lookups) == 0 {
			continue
		}
		accels := reflect.ValueOf(hb).Elem().FieldByName("gposAccels")
		if !accels.IsValid() || accels.Len() == 0 || !accels.Index(0).FieldByName("digest").IsValid() || !accels.Index(0).FieldByName("subtables").IsValid() {
			t.Fatal("go-text's harfbuzz.Font no longer has gposAccels[].digest and .subtables")
		}
		if d := accels.Index(0).FieldByName("digest"); d.Index(0).Uint() != math.MaxUint64 {
			t.Fatal("digest not opened")
		}
		return
	}
	t.Skip("no installed font with GPOS")
}
