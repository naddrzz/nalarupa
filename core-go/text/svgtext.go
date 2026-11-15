package text

// What usvg's text layout (core-go/usvg) reads from the font database: fontdb's query and face records,
// usvg's default fallback selector, the face metrics and glyph outlines it reads through ttf-parser, and
// shaping as rustybuzz does it. Database shares the faces of a Fonts, so SVG text and measuring resolve
// the same faces.

import (
	"encoding/binary"
	"runtime"
	"strconv"
	"strings"

	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/harfbuzz"
)

// FaceID identifies a face of a Database (fontdb's ID); a removed face's id is never reused.
type FaceID = faceID

// Style is a face style (fontdb's Style).
type Style = style

const (
	StyleNormal  = styleNormal
	StyleItalic  = styleItalic
	StyleOblique = styleOblique
)

// Generic is a generic font family; GenericNone means a named family.
type Generic uint8

const (
	GenericNone Generic = iota
	GenericSerif
	GenericSansSerif
	GenericCursive
	GenericFantasy
	GenericMonospace
)

// Family is a family in a query (fontdb's Family): a generic family or a name.
type Family struct {
	Generic Generic
	Name    string
}

// Variation sets a variation axis (rustybuzz's Variation, usvg's FontVariation).
type Variation struct {
	Tag   [4]byte
	Value float32
}

// Database is the font database SVG text reads (usvg's Arc<fontdb::Database>). Like Fonts, it is not
// safe for concurrent use.
type Database struct {
	db      *fontDB
	shapers map[string]*harfbuzz.Font
}

// NewDatabase is an empty database (fontdb::Database::new()).
func NewDatabase() *Database { return &Database{db: newDB()} }

// Database is the font database of f: the same faces, with Fonts' sans-serif family.
func (f *Fonts) Database() *Database {
	if f.svgDB == nil {
		f.svgDB = &Database{db: f.db}
	}
	return f.svgDB
}

// Fallback is the fallback face for c that Fonts' usvg resolver picks (text.rs `cached_fallback`): Noto
// faces first, then usvg's default selector. used holds the faces already tried, the primary one first.
func (f *Fonts) Fallback(c rune, used []FaceID) (FaceID, bool) {
	return f.cachedFallback(c, used)
}

func (d *Database) familyName(f Family) string {
	switch f.Generic {
	case GenericSerif:
		return d.db.serif
	case GenericSansSerif:
		return d.db.sansSerif
	case GenericCursive:
		return "Comic Sans MS"
	case GenericFantasy:
		if runtime.GOOS == "darwin" {
			return "Papyrus"
		}
		return "Impact"
	case GenericMonospace:
		return "Courier New"
	}
	return f.Name
}

// Query is fontdb's Database::query. stretch is 1 (ultra-condensed) to 9 (ultra-expanded).
func (d *Database) Query(families []Family, weight, stretch uint16, st Style) (FaceID, bool) {
	names := make([]string, len(families))
	for i, f := range families {
		names[i] = d.familyName(f)
	}
	return d.db.queryStyle(names, weight, stretch, st)
}

// DefaultFallback is usvg's FontResolver::default_fallback_selector: the first face, in database order,
// that is not excluded, shares the style, weight or stretch of exclude[0] and has c.
func (d *Database) DefaultFallback(c rune, exclude []FaceID) (FaceID, bool) {
	return d.db.defaultFallback(c, exclude)
}

func (db *fontDB) defaultFallback(c rune, exclude []FaceID) (FaceID, bool) {
	if len(exclude) == 0 {
		return 0, false
	}
	// usvg looks the base face up per candidate; it does not change, so once is the same.
	base := db.face(exclude[0])
	if base == nil {
		return 0, false
	}
	for _, x := range db.faces() {
		excluded := false
		for _, u := range exclude {
			excluded = excluded || u == x.id
		}
		if excluded {
			continue
		}
		if base.style != x.style && base.weight != x.weight && base.stretch != x.stretch {
			continue
		}
		if x.hasChar(c) {
			return x.id, true
		}
	}
	return 0, false
}

// FaceSource describes a face for diagnostics and test output: its first family, file (empty for font
// data loaded from memory) and index in the file.
func (d *Database) FaceSource(id FaceID) (family, path string, index uint32, ok bool) {
	f := d.db.face(id)
	if f == nil {
		return "", "", 0, false
	}
	return f.families[0].name, f.src.path, f.index, true
}

// LineMetrics is a position and thickness in font units (ttf_parser::LineMetrics).
type LineMetrics struct{ Position, Thickness int16 }

// FaceMetrics are what usvg's load_font reads through ttf-parser, in font units, at the face's default
// variation. A Has field is false where ttf-parser gives None.
type FaceMetrics struct {
	UnitsPerEm          uint16
	Ascender, Descender int16
	XHeight             int16
	HasXHeight          bool
	Strikeout           LineMetrics
	HasStrikeout        bool
	Underline           LineMetrics
	HasUnderline        bool
	SubscriptYOffset    int16
	SuperscriptYOffset  int16
	HasScriptMetrics    bool // OS/2 present: subscript and superscript metrics
}

// Metrics reads the face metrics; false when ttf-parser cannot parse the face.
func (d *Database) Metrics(id FaceID) (FaceMetrics, bool) {
	f := d.db.face(id)
	if f == nil {
		return FaceMetrics{}, false
	}
	var m FaceMetrics
	ok := false
	f.withRaw(func(raw *rawFace) {
		if !raw.faceParses() {
			return
		}
		ok = true
		head, _ := raw.table(tag("head"))
		m.UnitsPerEm, _ = u16(head, 18)
		m.Ascender, m.Descender, _ = vMetrics(raw, nil)
		if o := parseOS2(raw.table(tag("OS/2"))); o != nil {
			if o.version() >= 2 {
				m.XHeight, m.HasXHeight = i16(o, 86)
			}
			m.Strikeout.Thickness, _ = i16(o, 26)
			m.Strikeout.Position, _ = i16(o, 28)
			m.HasStrikeout = true
			m.SubscriptYOffset, _ = i16(o, 16)
			m.SuperscriptYOffset, _ = i16(o, 24)
			m.HasScriptMetrics = true
		}
		if post, ok := raw.table(tag("post")); ok && len(post) >= 32 {
			switch v, _ := u32(post, 0); v {
			case 0x00010000, 0x00020000, 0x00025000, 0x00030000, 0x00040000:
				m.Underline.Position, _ = i16(post, 8)
				m.Underline.Thickness, _ = i16(post, 10)
				m.HasUnderline = true
			}
		}
	})
	return m, ok
}

// HasChar is usvg's `has_char`: the face parses and maps c.
func (d *Database) HasChar(id FaceID, c rune) bool {
	f := d.db.face(id)
	return f != nil && f.hasChar(c)
}

// HasAxis reports whether the face parses and has a variation axis with this tag.
func (d *Database) HasAxis(id FaceID, axis [4]byte) bool {
	f := d.db.face(id)
	if f == nil || !f.isVariable() {
		return false
	}
	o := f.outlineFace()
	if o == nil {
		return false
	}
	t := binary.BigEndian.Uint32(axis[:])
	for i := 0; i+20 <= len(o.fvar); i += 20 {
		if binary.BigEndian.Uint32(o.fvar[i:]) == t {
			return true
		}
	}
	return false
}

// ShapedGlyph is one glyph rustybuzz outputs: its cluster (a byte offset in the shaped text), glyph id,
// and offsets and advance in font units.
type ShapedGlyph struct {
	Cluster                    int
	ID                         uint32
	XOffset, YOffset, XAdvance int32
}

// Shape shapes one directional run as usvg's shape_text_with_font does with rustybuzz: no language,
// `smcp` on for small caps, `kern` off without kerning, and the variations set on the face. False when
// the face cannot be shaped.
func (d *Database) Shape(id FaceID, text string, rtl, smallCaps, kerning bool, vars []Variation) ([]ShapedGlyph, bool) {
	hb := d.shaper(id, vars)
	if hb == nil {
		return nil, false
	}
	runes := make([]rune, 0, len(text))
	offsets := make([]int, 0, len(text))
	for i, r := range text {
		runes = append(runes, r)
		offsets = append(offsets, i)
	}
	buf := harfbuzz.NewBuffer()
	buf.AddRunes(runes, 0, -1)
	buf.Props.Direction = harfbuzz.LeftToRight
	if rtl {
		buf.Props.Direction = harfbuzz.RightToLeft
	}
	buf.GuessSegmentProperties()
	buf.Props.Language = ""
	var features []harfbuzz.Feature
	if smallCaps {
		features = append(features, harfbuzz.Feature{Tag: ot.MustNewTag("smcp"), Value: 1, Start: harfbuzz.FeatureGlobalStart, End: harfbuzz.FeatureGlobalEnd})
	}
	if !kerning {
		features = append(features, harfbuzz.Feature{Tag: ot.MustNewTag("kern"), Value: 0, Start: harfbuzz.FeatureGlobalStart, End: harfbuzz.FeatureGlobalEnd})
	}
	buf.Shape(hb, features)
	out := make([]ShapedGlyph, len(buf.Info))
	for i, g := range buf.Info {
		p := buf.Pos[i]
		out[i] = ShapedGlyph{Cluster: offsets[g.Cluster], ID: uint32(g.Glyph), XOffset: int32(p.XOffset), YOffset: int32(p.YOffset), XAdvance: int32(p.XAdvance)}
	}
	return out, true
}

// shaper is the HarfBuzz font of a face with the variations set, as rustybuzz's set_variations computes
// the coordinates.
func (d *Database) shaper(id FaceID, vars []Variation) *harfbuzz.Font {
	f := d.db.face(id)
	if f == nil {
		return nil
	}
	o := f.outlineFace()
	var coords []int16
	if o != nil {
		coords = o.coords(vars)
	}
	nonDefault := false
	var key strings.Builder
	key.WriteString(strconv.FormatUint(uint64(id), 10))
	for _, c := range coords {
		nonDefault = nonDefault || c != 0
		key.WriteByte(',')
		key.WriteString(strconv.Itoa(int(c)))
	}
	if !nonDefault {
		coords = nil
	}
	if d.shapers == nil {
		d.shapers = map[string]*harfbuzz.Font{}
	}
	k := key.String()
	if hb, ok := d.shapers[k]; ok {
		return hb
	}
	hb := newShaper(f, coords, o != nil)
	d.shapers[k] = hb
	return hb
}

// Outline outlines a glyph at the face's default variation (usvg's DatabaseExt::outline: every axis set
// to its default). False when ttf-parser gives no outline.
func (d *Database) Outline(id FaceID, gid uint16, b OutlineBuilder) bool {
	o := d.outlineFace(id)
	if o == nil {
		return false
	}
	var vars []Variation
	fixed := func(b []byte) float32 { return float32(int32(binary.BigEndian.Uint32(b))) / 65536 }
	for i := 0; i+20 <= len(o.fvar) && i/20 < o.axes; i += 20 {
		var v Variation
		copy(v.Tag[:], o.fvar[i:i+4])
		v.Value = fixed(o.fvar[i+8:])
		vars = append(vars, v)
	}
	return o.outlineGlyph(gid, o.coords(vars), b)
}

// OutlineVaried outlines a glyph with the variations set in turn (usvg's outline_with_variations).
func (d *Database) OutlineVaried(id FaceID, gid uint16, vars []Variation, b OutlineBuilder) bool {
	o := d.outlineFace(id)
	if o == nil {
		return false
	}
	return o.outlineGlyph(gid, o.coords(vars), b)
}

func (d *Database) outlineFace(id FaceID) *outlineFace {
	f := d.db.face(id)
	if f == nil {
		return nil
	}
	return f.outlineFace()
}

// GlyphImage reports whether usvg would draw a glyph from a color (COLR), SVG or bitmap table instead of
// its outline. The port draws none of them; usvg's layout uses this to leave such glyphs out.
func (d *Database) GlyphImage(id FaceID, gid uint16) (colr, image bool) {
	f := d.db.face(id)
	if f == nil {
		return false, false
	}
	ft := f.font()
	if ft == nil {
		return false, false
	}
	fc := font.NewFace(ft)
	if _, ok := fc.GlyphDataColor(font.GID(gid)); ok {
		return true, false
	}
	if _, ok := fc.GlyphDataSVG(font.GID(gid)); ok {
		return false, true
	}
	if b, ok := fc.GlyphDataBitmap(font.GID(gid)); ok && b.Format == font.PNG {
		return false, true
	}
	return false, false
}
