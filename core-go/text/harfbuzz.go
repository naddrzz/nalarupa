package text

// The go-text side of shaping: parsing a face for HarfBuzz and working around the places where go-text
// v0.3.5 shapes differently from rustybuzz 0.20, the Rust core's shaper.

import (
	"bytes"
	"math"
	"reflect"
	"unsafe"

	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
	"github.com/go-text/typesetting/harfbuzz"
)

// openDigests works around a go-text v0.3.5 bug: a lookup's glyph digest stops taking coverage ranges once
// one of its three bit masks is full, so lookups and subtables with large range coverages skip glyphs
// they cover (Calibri loses its class kerning, for example). Opening every digest makes HarfBuzz rely on
// the coverage checks alone, as rustybuzz does.
func openDigests(hb *harfbuzz.Font) {
	open := func(d reflect.Value) {
		if d.IsValid() && d.Kind() == reflect.Array {
			d = reflect.NewAt(d.Type(), unsafe.Pointer(d.UnsafeAddr())).Elem()
			for i := 0; i < d.Len(); i++ {
				d.Index(i).SetUint(math.MaxUint64)
			}
		}
	}
	v := reflect.ValueOf(hb).Elem()
	for _, name := range []string{"gsubAccels", "gposAccels"} {
		accels := v.FieldByName(name)
		for i := 0; accels.IsValid() && i < accels.Len(); i++ {
			a := accels.Index(i)
			open(a.FieldByName("digest"))
			subs := a.FieldByName("subtables")
			for j := 0; subs.IsValid() && j < subs.Len(); j++ {
				open(subs.Index(j).FieldByName("digest"))
			}
		}
	}
}

// font parses the face for shaping, once.
func (fi *faceInfo) font() *font.Font {
	if fi.shaper != nil || fi.noShaper {
		return fi.shaper
	}
	fi.noShaper = true
	data, ok := fi.src.bytes()
	if !ok {
		return nil
	}
	lds, err := ot.NewLoaders(bytes.NewReader(data))
	if err != nil || int(fi.index) >= len(lds) {
		return nil
	}
	ft, err := font.NewFont(lds[fi.index])
	if err != nil {
		return nil
	}
	lenientLayouts(ft, lds[fi.index])
	keepUnvariedFeatures(&ft.GSUB.Layout)
	keepUnvariedFeatures(&ft.GPOS.Layout)
	for _, l := range ft.GPOS.Lookups {
		for i, s := range l.Subtables {
			p, ok := s.(tables.PairPos)
			if !ok {
				continue
			}
			switch d := p.Data.(type) {
			case tables.PairPosData1:
				// ttf-parser 0.25 resolves the device offsets of these records inside the record array instead of
				// from the pair set, so rustybuzz never finds them and variable fonts get no kerning deltas from
				// them.
				d.ValueFormat1 &^= tables.Devices
				d.ValueFormat2 &^= tables.Devices
				l.Subtables[i] = tables.PairPos{Data: d}
			case tables.PairPosData2:
				// A second glyph the class definition does not list is class 0, as in HarfBuzz and rustybuzz,
				// and its (usually empty) record ends the lookup; go-text tries the next subtable instead.
				d.ClassDef2 = classZero{d.ClassDef2}
				l.Subtables[i] = tables.PairPos{Data: d}
			}
		}
	}
	fi.shaper, fi.noShaper = ft, false
	return ft
}

// lenientLayouts rebuilds GSUB and GPOS when go-text dropped the whole table over one subtable it found
// inconsistent (Linux Libertine's class kerning declares more classes than it uses). rustybuzz keeps such
// tables, so they are rebuilt here without the subtables that would be unsafe to apply: pair class counts
// may disagree (go-text reads class records with bounds checks), other inconsistent subtables are left out.
func lenientLayouts(ft *font.Font, ld *ot.Loader) {
	if ft.GSUB.Lookups == nil {
		if lay, ok := rawLayout(ld, "GSUB"); ok {
			out := font.GSUB{Layout: layoutOf(lay), Lookups: make([]font.GSUBLookup, len(lay.LookupList.Lookups))}
			for i, lk := range lay.LookupList.Lookups {
				subs, _ := lk.AsGSUBLookups()
				var keep []tables.GSUBLookup
				for _, s := range subs {
					var err error
					if ext, ok := s.(tables.ExtensionSubs); ok {
						if s, err = ext.Resolve(); err != nil {
							continue
						}
					}
					switch st := s.(type) {
					case tables.MultipleSubs:
						err = st.Sanitize()
					case tables.LigatureSubs:
						err = st.Sanitize()
					case tables.ContextualSubs:
						err = st.Sanitize(uint16(len(out.Lookups)))
					case tables.ReverseChainSingleSubs:
						err = st.Sanitize()
					}
					if err == nil {
						keep = append(keep, s)
					}
				}
				out.Lookups[i] = font.GSUBLookup{LookupOptions: font.LookupOptions{Flag: lk.LookupFlag, MarkFilteringSet: lk.MarkFilteringSet}, Subtables: keep}
			}
			ft.GSUB = out
		}
	}
	if ft.GPOS.Lookups == nil {
		if lay, ok := rawLayout(ld, "GPOS"); ok {
			out := font.GPOS{Layout: layoutOf(lay), Lookups: make([]font.GPOSLookup, len(lay.LookupList.Lookups))}
			for i, lk := range lay.LookupList.Lookups {
				subs, _ := lk.AsGPOSLookups()
				var keep []tables.GPOSLookup
				for _, s := range subs {
					var err error
					if ext, ok := s.(tables.ExtensionPos); ok {
						if s, err = ext.Resolve(); err != nil {
							continue
						}
					}
					switch st := s.(type) {
					case tables.SinglePos:
						err = st.Sanitize()
					case tables.PairPos:
						if _, ok := st.Data.(tables.PairPosData1); ok {
							err = st.Sanitize()
						}
					case tables.MarkBasePos:
						err = st.Sanitize()
					case tables.MarkLigPos:
						err = st.Sanitize()
					case tables.ContextualPos:
						err = st.Sanitize(uint16(len(out.Lookups)))
					}
					if err == nil {
						keep = append(keep, s)
					}
				}
				out.Lookups[i] = font.GPOSLookup{LookupOptions: font.LookupOptions{Flag: lk.LookupFlag, MarkFilteringSet: lk.MarkFilteringSet}, Subtables: keep}
			}
			ft.GPOS = out
		}
	}
}

// rawLayout parses a layout table that is present and has lookups.
func rawLayout(ld *ot.Loader, name string) (tables.Layout, bool) {
	raw, err := ld.RawTable(ot.MustNewTag(name))
	if err != nil || len(raw) == 0 {
		return tables.Layout{}, false
	}
	lay, _, err := tables.ParseLayout(raw)
	return lay, err == nil && len(lay.LookupList.Lookups) > 0
}

// layoutOf is go-text's newLayout: scripts and features with their tags, invalid required features cleared.
func layoutOf(t tables.Layout) font.Layout {
	n := len(t.FeatureList.Features)
	out := font.Layout{Scripts: make([]font.Script, len(t.ScriptList.Scripts)), Features: make([]font.Feature, n)}
	for i, s := range t.ScriptList.Scripts {
		if l := s.DefaultLangSys; l != nil && int(l.RequiredFeatureIndex) >= n {
			l.RequiredFeatureIndex = 0xFFFF
		}
		for j := range s.LangSys {
			if int(s.LangSys[j].RequiredFeatureIndex) >= n {
				s.LangSys[j].RequiredFeatureIndex = 0xFFFF
			}
		}
		out.Scripts[i] = font.Script{Script: s, Tag: t.ScriptList.Records[i].Tag}
	}
	for i, f := range t.FeatureList.Features {
		out.Features[i] = font.Feature{Feature: f, Tag: t.FeatureList.Records[i].Tag}
	}
	if t.FeatureVariations != nil {
		out.FeatureVariations = t.FeatureVariations.FeatureVariationRecords
	}
	return out
}

// classZero puts glyphs a class definition does not list in class 0.
type classZero struct{ tables.ClassDef }

func (c classZero) Class(g tables.GlyphID) (uint16, bool) {
	k, _ := c.ClassDef.Class(g)
	return k, true
}

// keepUnvariedFeatures works around go-text giving a feature no lookups at all when a feature variation
// (selected by the weight) applies but does not replace that feature: rustybuzz, like HarfBuzz, keeps the
// feature's own lookups. Each variation record gets the unreplaced features as identity substitutions.
func keepUnvariedFeatures(l *font.Layout) {
	for i := range l.FeatureVariations {
		subs := &l.FeatureVariations[i].Substitutions.Substitutions
		replaced := map[uint16]bool{}
		for _, s := range *subs {
			replaced[s.FeatureIndex] = true
		}
		for k, f := range l.Features {
			if !replaced[uint16(k)] {
				*subs = append(*subs, tables.FeatureTableSubstitutionRecord{FeatureIndex: uint16(k), AlternateFeature: f.Feature})
			}
		}
	}
}
