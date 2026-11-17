package text

// What PDF export (core-go/pdf) reads from a face to embed it: the font file, the variation axes, the
// normalized coordinates of a set of variations, and glyph advances at those coordinates.

import (
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/font/opentype/tables"
)

// FaceData is the face's font file (the whole collection for a .ttc) and its index in it.
func (d *Database) FaceData(id FaceID) ([]byte, uint32, bool) {
	f := d.db.face(id)
	if f == nil {
		return nil, 0, false
	}
	data, ok := f.src.bytes()
	return data, f.index, ok
}

// Axes are the tags of the face's variation axes, in fvar order; none for a static face.
func (d *Database) Axes(id FaceID) [][4]byte {
	o := d.outlineFace(id)
	if o == nil {
		return nil
	}
	var tags [][4]byte
	for i := 0; i+20 <= len(o.fvar); i += 20 {
		var t [4]byte
		copy(t[:], o.fvar[i:i+4])
		tags = append(tags, t)
	}
	return tags
}

// Coords are the normalized coordinates (F2Dot14) of vars set in turn, as Outline uses them; nil for a
// static face.
func (d *Database) Coords(id FaceID, vars []Variation) []int16 {
	o := d.outlineFace(id)
	if o == nil {
		return nil
	}
	return o.coords(vars)
}

// Advance is the horizontal advance of a glyph in font units at the coordinates of vars: hmtx, plus HVAR
// or the gvar phantom points for a variable face.
func (d *Database) Advance(id FaceID, gid uint16, vars []Variation) (float32, bool) {
	f := d.db.face(id)
	if f == nil {
		return 0, false
	}
	ft := f.font()
	if ft == nil {
		return 0, false
	}
	fc := font.NewFace(ft)
	nonDefault := false
	coords := d.Coords(id, vars)
	c := make([]tables.Coord, len(coords))
	for i, v := range coords {
		c[i] = tables.Coord(v)
		nonDefault = nonDefault || v != 0
	}
	if nonDefault {
		fc.SetCoords(c)
	}
	return fc.HorizontalAdvance(font.GID(gid)), true
}
