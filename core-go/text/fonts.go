// Package text finds fonts, shapes text and wraps lines as the Rust core's text.rs does, offline: system
// fonts plus the font cache folder, Noto-first fallback, shaping with the `wght` axis, SkParagraph line
// heights and UAX #14 wrapping. Fonts implements layout.TextMeasurer. Downloading missing families from
// Google Fonts is not ported yet: a family is used once its file is in the cache (see Downloaded).
package text

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-text/typesetting/harfbuzz"

	"github.com/naddrzz/nalarupa/core-go/layout"
)

type faceKey struct {
	family string
	weight uint16
	italic bool
}

type weightKey struct {
	id     faceID
	weight uint16
}

type shapeKey struct {
	id     faceID
	weight uint16
	text   string
}

// cluster is a shaped cluster: its byte offset in the text and its advance in em.
type cluster struct {
	at  int
	adv float64
}

type maybeFace struct {
	id faceID
	ok bool
}

// Fonts is the font database with its measuring memos. It is not safe for concurrent use.
type Fonts struct {
	db    *fontDB
	cache string
	tried map[string]bool
	// Memos, cleared whenever a font is added: primary face per style, vertical metrics per face and
	// weight (em), shaped clusters per face, weight and text, fallback face per character and faces tried.
	faces    map[faceKey]maybeFace
	vmetrics map[weightKey][3]float64
	shaped   map[shapeKey][]cluster
	fallback map[string]maybeFace
	shapers  map[weightKey]*harfbuzz.Font
	// Generation goes up when Downloaded adds a font; the renderer rebuilds every layer when it changes.
	Generation uint64
}

var _ layout.TextMeasurer = (*Fonts)(nil)

// New loads the system fonts and the font files in cache (created if missing).
func New(cache string) *Fonts {
	db := newDB()
	db.loadSystemFonts()
	os.MkdirAll(cache, 0o777)
	db.loadDir(cache, map[string]bool{})
	db.sansSerif = "Arial"
	for _, name := range []string{"Segoe UI", "Helvetica", "Arial", "DejaVu Sans"} {
		if anyFace(db, func(f *faceInfo) bool { return f.hasName(name) }) {
			db.sansSerif = name
			break
		}
	}
	preferVariable(db, cache)
	f := &Fonts{db: db, cache: cache, tried: map[string]bool{}, shapers: map[weightKey]*harfbuzz.Font{}}
	f.fontsChanged()
	return f
}

func anyFace(db *fontDB, ok func(*faceInfo) bool) bool {
	for _, f := range db.faces() {
		if ok(f) {
			return true
		}
	}
	return false
}

// HasFamily reports whether a face of this family (any case) is loaded.
func (f *Fonts) HasFamily(family string) bool {
	return anyFace(f.db, func(x *faceInfo) bool { return x.hasNameFold(family) })
}

// Downloaded loads a font file of this family that has arrived in the cache since the start; true when a
// font was added, which bumps Generation. A family still missing falls back to a cached Noto Sans.
func (f *Fonts) Downloaded(family string, weight uint16, italic bool) bool {
	before := f.db.len()
	f.loadCached(family, weight, italic)
	if !f.HasFamily(family) && !asciiFold(family, "Noto Sans") {
		f.fetch("Noto Sans", weight, italic)
	}
	added := f.db.len() != before
	if added {
		f.Generation++
	}
	return added
}

// ensure makes the family available from the cache, falling back to a cached Noto Sans.
func (f *Fonts) ensure(st layout.TextStyle) {
	if f.fetch(st.Family, st.Weight, st.Italic) && !asciiFold(st.Family, "Noto Sans") && !f.HasFamily(st.Family) {
		f.fetch("Noto Sans", st.Weight, st.Italic)
	}
}

// fetch loads a style from the cache once; false when it was already handled. (Rust also starts a
// download here when the cache has nothing.)
func (f *Fonts) fetch(family string, weight uint16, italic bool) bool {
	key := family + "|" + strconv.Itoa(int(weight)) + "|" + strconv.FormatBool(italic)
	if f.tried[key] {
		return false
	}
	f.tried[key] = true
	f.loadCached(family, weight, italic)
	return true
}

// loadCached makes the face available from what is on disk; true when it is.
func (f *Fonts) loadCached(family string, weight uint16, italic bool) bool {
	if anyFace(f.db, func(x *faceInfo) bool {
		return x.hasNameFold(family) && (x.style != styleNormal) == italic && x.isVariable()
	}) {
		return true
	}
	// Before the exact-face check: a cached static instance must not shadow the variable font.
	if b, err := os.ReadFile(varFile(f.cache, family, italic)); err == nil {
		f.db.loadData(b)
		preferVariable(f.db, f.cache)
		f.fontsChanged()
		return true
	}
	if anyFace(f.db, func(x *faceInfo) bool {
		d := int(x.weight) - int(weight)
		return x.hasNameFold(family) && d >= -50 && d <= 50 && (x.style != styleNormal) == italic
	}) {
		return true
	}
	b, err := os.ReadFile(staticFile(f.cache, family, weight, italic))
	if err != nil {
		return false
	}
	f.db.loadData(b)
	f.fontsChanged()
	return true
}

func (f *Fonts) fontsChanged() {
	f.faces = map[faceKey]maybeFace{}
	f.vmetrics = map[weightKey][3]float64{}
	f.shaped = map[shapeKey][]cluster{}
	f.fallback = map[string]maybeFace{}
}

// varFile is the family's variable font in the cache (every axis, used at its default except `wght`).
func varFile(cache, family string, italic bool) string {
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, family)
	if italic {
		compact += "-Italic"
	}
	return filepath.Join(cache, "var", compact+".ttf")
}

func staticFile(cache, family string, weight uint16, italic bool) string {
	name := strings.ReplaceAll(family, " ", "_") + "-" + strconv.Itoa(int(weight))
	if italic {
		name += "i"
	}
	return filepath.Join(cache, name+".ttf")
}

// face is the face usvg picks for `font-family="'<family>', 'Noto Sans', sans-serif"` (see svg.rs), after
// making sure the family is loaded. Memoized.
func (f *Fonts) face(st layout.TextStyle) (faceID, bool) {
	key := faceKey{st.Family, st.Weight, st.Italic}
	if hit, ok := f.faces[key]; ok {
		return hit.id, hit.ok
	}
	f.ensure(st)
	id, ok := f.db.query([]string{st.Family, "Noto Sans", f.db.sansSerif, f.db.serif}, st.Weight, st.Italic)
	f.faces[key] = maybeFace{id, ok}
	return id, ok
}

// Metrics are the style's vertical metrics: SkParagraph rounds a set line height (round(lh·size), half
// leading) and a natural one (ascent + descent + gap).
func (f *Fonts) Metrics(st layout.TextStyle) layout.Metrics {
	asc, desc, gap := 0.9, -0.22, 0.0
	if id, ok := f.face(st); ok {
		if m, ok := f.vMetrics(id, st.Weight); ok {
			asc, desc, gap = m[0], m[1], m[2]
		}
	}
	ascent, descent, gap := asc*st.Size, -desc*st.Size, gap*st.Size
	if st.LineHeight != 0 {
		if h := math.Round(st.LineHeight * st.Size); h > 0 {
			return layout.Metrics{Ascent: ascent, Descent: descent, LineH: h, Baseline: (h-ascent-descent)/2 + ascent}
		}
	}
	return layout.Metrics{Ascent: ascent, Descent: descent, LineH: math.Max(math.Round(ascent+descent+gap), 1), Baseline: gap/2 + ascent}
}

// vMetrics are a face's ascender, descender and line gap in em at a weight (variable fonts move them
// with MVAR, as Skia applies).
func (f *Fonts) vMetrics(id faceID, weight uint16) ([3]float64, bool) {
	key := weightKey{id, weight}
	if m, ok := f.vmetrics[key]; ok {
		return m, true
	}
	face := f.db.face(id)
	if face == nil {
		return [3]float64{}, false
	}
	var m [3]float64
	ok := false
	face.withRaw(func(raw *rawFace) {
		if !raw.faceParses() {
			return
		}
		head, _ := raw.table(tag("head"))
		upem, _ := u16(head, 18)
		a, d, g := vMetrics(raw, wghtCoords(raw, weight))
		u := float64(upem)
		m, ok = [3]float64{float64(a) / u, float64(d) / u, float64(g) / u}, true
	})
	if ok {
		f.vmetrics[key] = m
	}
	return m, ok
}

// Width is the advance of text (trailing spaces included) plus letter spacing after every cluster.
func (f *Fonts) Width(text string, st layout.TextStyle) float64 {
	cl := f.clusters(text, st)
	sum := 0.0
	for _, c := range cl {
		sum += c.adv
	}
	return sum*st.Size + st.LetterSpacing*float64(len(cl))
}
