package text

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype/tables"
	"github.com/go-text/typesetting/harfbuzz"

	"github.com/naddrzz/nalarupa/core-go/layout"
)

// glyph is one shaped glyph: its cluster's byte offset, glyph id and advance in em.
type glyph struct {
	at  int
	gid uint32
	adv float64
}

// clusters are the clusters of text shaped like usvg does: HarfBuzz with the primary face, then missing
// glyphs replaced from fallback faces. Without any face, ASCII is 0.55 em and the rest 1 em.
func (f *Fonts) clusters(text string, st layout.TextStyle) []cluster {
	id, ok := f.face(st)
	if !ok {
		var out []cluster
		for i, r := range text {
			adv := 1.0
			if r < utf8.RuneSelf {
				adv = 0.55
			}
			out = append(out, cluster{i, adv})
		}
		return out
	}
	key := shapeKey{id, st.Weight, text}
	if hit, ok := f.shaped[key]; ok {
		return hit
	}
	glyphs := f.shape(id, text, st.Weight)
	used := []faceID{id}
	for {
		var c rune = -1
		for _, g := range glyphs {
			if g.gid == 0 {
				if g.at < len(text) {
					c, _ = utf8.DecodeRuneInString(text[g.at:])
				}
				break
			}
		}
		if c < 0 {
			break
		}
		if c >= utf8.RuneSelf {
			f.fetch(notoFamily(c), st.Weight, false)
		}
		fb, ok := f.cachedFallback(c, used)
		if !ok {
			break
		}
		alt := f.shape(fb, text, st.Weight)
		complete := true
		for _, g := range alt {
			complete = complete && g.gid != 0
		}
		if complete {
			glyphs = alt
			break
		}
		if len(alt) != len(glyphs) {
			break
		}
		for i := range glyphs {
			if glyphs[i].gid == 0 && alt[i].gid != 0 {
				glyphs[i] = alt[i]
			}
		}
		used = append(used, fb)
	}
	var out []cluster
	for _, g := range glyphs {
		if n := len(out); n > 0 && out[n-1].at == g.at {
			out[n-1].adv += g.adv
		} else {
			out = append(out, cluster{g.at, g.adv})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at < out[j].at })
	f.shaped[key] = out
	return out
}

// shape shapes text in one face at a weight (only `wght` is set on variable fonts), with no language,
// as rustybuzz does. Nil when the face cannot be shaped.
func (f *Fonts) shape(id faceID, text string, weight uint16) []glyph {
	hb := f.shaper(id, weight)
	if hb == nil {
		return nil
	}
	runes := make([]rune, 0, len(text))
	offsets := make([]int, 0, len(text))
	for i, r := range text {
		runes = append(runes, r)
		offsets = append(offsets, i)
	}
	buf := harfbuzz.NewBuffer()
	buf.AddRunes(runes, 0, -1)
	buf.GuessSegmentProperties()
	buf.Props.Language = ""
	buf.Shape(hb, nil)
	upem := float64(hb.Face().Upem())
	out := make([]glyph, len(buf.Info))
	for i, g := range buf.Info {
		out[i] = glyph{offsets[g.Cluster], uint32(g.Glyph), float64(buf.Pos[i].XAdvance) / upem}
	}
	return out
}

// shaper is the HarfBuzz font of a face at a weight, with the variation coordinates ttf-parser computes.
func (f *Fonts) shaper(id faceID, weight uint16) *harfbuzz.Font {
	key := weightKey{id, weight}
	if hb, ok := f.shapers[key]; ok {
		return hb
	}
	face := f.db.face(id)
	if face == nil {
		return nil
	}
	var coords []int16
	parses := false
	face.withRaw(func(raw *rawFace) {
		if parses = raw.faceParses(); parses {
			coords = wghtCoords(raw, weight)
		}
	})
	if coords == nil { // a static face shapes the same at every weight
		hb, ok := f.shapers[weightKey{id, 0}]
		if !ok {
			hb = newShaper(face, nil, parses)
			f.shapers[weightKey{id, 0}] = hb
		}
		f.shapers[key] = hb
		return hb
	}
	hb := newShaper(face, coords, parses)
	f.shapers[key] = hb
	return hb
}

func newShaper(face *faceInfo, coords []int16, parses bool) *harfbuzz.Font {
	var hb *harfbuzz.Font
	if ft := face.font(); ft != nil && parses {
		fc := font.NewFace(ft)
		if coords != nil {
			c := make([]tables.Coord, len(coords))
			for i, v := range coords {
				c[i] = tables.Coord(v)
			}
			fc.SetCoords(c)
		}
		hb = harfbuzz.NewFont(fc)
		openDigests(hb)
	}
	return hb
}

func (f *Fonts) cachedFallback(c rune, used []faceID) (faceID, bool) {
	var b strings.Builder
	b.WriteString(strconv.Itoa(int(c)))
	for _, id := range used {
		b.WriteByte(',')
		b.WriteString(strconv.FormatUint(uint64(id), 10))
	}
	key := b.String()
	if hit, ok := f.fallback[key]; ok {
		return hit.id, hit.ok
	}
	id, ok := f.pickFallback(c, used)
	f.fallback[key] = maybeFace{id, ok}
	return id, ok
}

// pickFallback prefers Noto faces with c (same style first, then the nearest weight), then usvg's
// default selector: the first face, in database order, that has c.
func (f *Fonts) pickFallback(c rune, used []faceID) (faceID, bool) {
	if len(used) == 0 {
		return 0, false
	}
	base := f.db.face(used[0])
	if base == nil {
		return 0, false
	}
	isUsed := func(id faceID) bool {
		for _, u := range used {
			if u == id {
				return true
			}
		}
		return false
	}
	var best *faceInfo
	var bestStyle, bestDist int
	for _, x := range f.db.faces() {
		if isUsed(x.id) {
			continue
		}
		noto := false
		for _, n := range x.families {
			noto = noto || strings.HasPrefix(n.name, "Noto")
		}
		if !noto || !x.hasChar(c) {
			continue
		}
		s, d := 0, int(x.weight)-int(base.weight)
		if x.style != base.style {
			s = 1
		}
		if d < 0 {
			d = -d
		}
		if best == nil || s < bestStyle || s == bestStyle && d < bestDist {
			best, bestStyle, bestDist = x, s, d
		}
	}
	if best != nil {
		return best.id, true
	}
	return f.db.defaultFallback(c, used)
}

// notoFamily is the fallback library for a character's script. Emoji come from monochrome Noto Emoji.
func notoFamily(c rune) string {
	switch {
	case c >= 0x0590 && c <= 0x05FF, c >= 0xFB1D && c <= 0xFB4F:
		return "Noto Sans Hebrew"
	case c >= 0x0600 && c <= 0x06FF, c >= 0x0750 && c <= 0x077F, c >= 0x08A0 && c <= 0x08FF, c >= 0xFB50 && c <= 0xFDFF, c >= 0xFE70 && c <= 0xFEFF:
		return "Noto Sans Arabic"
	case c >= 0x0E00 && c <= 0x0E7F:
		return "Noto Sans Thai"
	case c >= 0x10A0 && c <= 0x10FF, c >= 0x2D00 && c <= 0x2D2F:
		return "Noto Sans Georgian"
	case c >= 0x1800 && c <= 0x18AF:
		return "Noto Sans Mongolian"
	case c >= 0xA980 && c <= 0xA9DF:
		return "Noto Sans Javanese"
	case c >= 0x1100 && c <= 0x11FF, c >= 0x3130 && c <= 0x318F, c >= 0xAC00 && c <= 0xD7AF:
		return "Noto Sans KR"
	case c >= 0x2E80 && c <= 0x2FFF, c >= 0x3000 && c <= 0x30FF, c >= 0x31F0 && c <= 0x31FF, c >= 0x3400 && c <= 0x4DBF, c >= 0x4E00 && c <= 0x9FFF, c >= 0xF900 && c <= 0xFAFF, c >= 0xFF00 && c <= 0xFFEF:
		return "Noto Sans JP"
	case c >= 0x2190 && c <= 0x21FF, c >= 0x2300 && c <= 0x23FF, c >= 0x25A0 && c <= 0x25FF, c >= 0x2B00 && c <= 0x2BFF:
		return "Noto Sans Symbols 2"
	case c >= 0x2600 && c <= 0x27BF, c == 0xFE0F, c >= 0x1F000 && c <= 0x1FAFF:
		return "Noto Emoji"
	}
	return "Noto Sans"
}
