package text

// The font database, ported from fontdb 0.23 (what usvg and text.rs use): the same font directories in the
// same load order, the same face metadata, the same slot order (removed faces free slots that later faces
// reuse) and the same CSS-like query, so a family resolves to the same face as in Rust.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-text/typesetting/font"
)

type style uint8

const (
	styleNormal style = iota
	styleItalic
	styleOblique
)

// faceID is unique per loaded face; a removed face's id is never reused.
type faceID uint64

// source is a font file, or font bytes loaded from memory (path "").
type source struct {
	path string
	data []byte // the bytes of a memory font, or of the file once it is needed for shaping
}

// open gives random access to the font data.
func (s *source) open() (io.ReaderAt, int64, func(), bool) {
	if s.data != nil || s.path == "" {
		return bytes.NewReader(s.data), int64(len(s.data)), func() {}, true
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, 0, nil, false
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, nil, false
	}
	return f, st.Size(), func() { f.Close() }, true
}

// bytes is the whole font data, read once.
func (s *source) bytes() ([]byte, bool) {
	if s.data == nil && s.path != "" {
		b, err := os.ReadFile(s.path)
		if err != nil {
			return nil, false
		}
		s.data = b
	}
	return s.data, s.data != nil
}

type faceInfo struct {
	id       faceID
	src      *source
	index    uint32
	families []family // the English (US) one first
	style    style
	weight   uint16
	stretch  uint16 // 1 (ultra-condensed) to 9 (ultra-expanded)

	probed   bool
	parses   bool   // ttf-parser accepts the face (head, hhea, maxp)
	variable bool   // it has axes
	cmap     []byte // read for character lookups
	shaper   *font.Font
	noShaper bool
}

func (f *faceInfo) hasName(name string) bool {
	for _, n := range f.families {
		if n.name == name {
			return true
		}
	}
	return false
}

func (f *faceInfo) hasNameFold(name string) bool {
	for _, n := range f.families {
		if asciiFold(n.name, name) {
			return true
		}
	}
	return false
}

// withRaw runs fn on the face's table directory; false when the data cannot be read.
func (f *faceInfo) withRaw(fn func(*rawFace)) bool {
	r, size, done, ok := f.src.open()
	if !ok {
		return false
	}
	defer done()
	raw, ok := parseRawFace(r, size, f.index)
	if !ok {
		return false
	}
	fn(raw)
	return true
}

func (f *faceInfo) probe() {
	if f.probed {
		return
	}
	f.probed = true
	f.withRaw(func(raw *rawFace) {
		if f.parses = raw.faceParses(); f.parses {
			f.variable = isVariable(raw)
			f.cmap, _ = raw.table(tag("cmap"))
		}
	})
}

// hasChar is usvg's `has_char`: the face parses and maps c.
func (f *faceInfo) hasChar(c rune) bool {
	f.probe()
	return f.parses && glyphIndex(f.cmap, c)
}

func (f *faceInfo) isVariable() bool {
	f.probe()
	return f.parses && f.variable
}

// asciiFold is Rust's eq_ignore_ascii_case.
func asciiFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

type fontDB struct {
	slots     []*faceInfo // nil = free
	free      []int       // freed slots, reused last-freed first (slotmap's free list)
	next      faceID
	serif     string
	sansSerif string
}

func newDB() *fontDB { return &fontDB{serif: "Times New Roman", sansSerif: "Arial"} }

func (db *fontDB) faces() []*faceInfo {
	out := make([]*faceInfo, 0, len(db.slots))
	for _, f := range db.slots {
		if f != nil {
			out = append(out, f)
		}
	}
	return out
}

func (db *fontDB) len() int { return len(db.slots) - len(db.free) }

func (db *fontDB) face(id faceID) *faceInfo {
	for _, f := range db.slots {
		if f != nil && f.id == id {
			return f
		}
	}
	return nil
}

func (db *fontDB) push(f *faceInfo) {
	db.next++
	f.id = db.next
	if n := len(db.free); n > 0 {
		db.slots[db.free[n-1]] = f
		db.free = db.free[:n-1]
		return
	}
	db.slots = append(db.slots, f)
}

func (db *fontDB) remove(id faceID) {
	for i, f := range db.slots {
		if f != nil && f.id == id {
			db.slots[i] = nil
			db.free = append(db.free, i)
			return
		}
	}
}

// loadData adds every face of font bytes (fontdb `load_font_data`).
func (db *fontDB) loadData(data []byte) {
	db.loadSource(&source{data: data})
}

func (db *fontDB) loadFile(path string) {
	db.loadSource(&source{path: path})
}

func (db *fontDB) loadSource(src *source) {
	r, size, done, ok := src.open()
	if !ok {
		return
	}
	defer done()
	n, ok := fontsInCollection(r, size)
	if !ok {
		n = 1
	} else if 12+4*int64(n) > size {
		return // no face of it can be read
	}
	for i := uint32(0); i < n; i++ {
		if f, ok := parseFaceInfo(src, r, size, i); ok {
			db.push(f)
		}
	}
}

// parseFaceInfo reads what fontdb keeps of a face; false for malformed or unnamed faces, which are skipped.
func parseFaceInfo(src *source, r io.ReaderAt, size int64, index uint32) (*faceInfo, bool) {
	raw, ok := parseRawFace(r, size, index)
	if !ok {
		return nil, false
	}
	nameData, ok := raw.table(tag("name"))
	if !ok {
		return nil, false
	}
	names, ok := parseNames(nameData)
	if !ok {
		return nil, false
	}
	families := collectFamilies(16, names)
	if len(families) == 0 {
		families = collectFamilies(1, names)
	}
	if len(families) > 1 {
		for i, f := range families {
			if f.enUS {
				families[0], families[i] = families[i], families[0]
				break
			}
		}
	}
	if len(families) == 0 {
		return nil, false
	}
	hasPS := false
	for _, n := range names {
		if n.id == 6 && (n.isUnicode() || n.isMacRoman()) {
			_, hasPS = n.text()
			break
		}
	}
	if !hasPS {
		return nil, false
	}
	f := &faceInfo{src: src, index: index, families: families, style: styleNormal, weight: 400, stretch: 5}
	if o := parseOS2(raw.table(tag("OS/2"))); o != nil {
		f.style, f.weight, f.stretch = o.style(), o.weight(), o.stretch()
	}
	// fontdb: a non-zero (or unreadable) italic angle in `post` makes a normal face italic.
	if post, ok := raw.table(tag("post")); ok && f.style == styleNormal {
		if len(post) < 8 || !bytes.Equal(post[4:8], []byte{0, 0, 0, 0}) {
			f.style = styleItalic
		}
	}
	return f, true
}

// loadDir loads the font files under dir, recursively, in directory order (fontdb `load_fonts_dir_impl`).
// seen holds canonical paths once a symlink was met, so each file loads once.
func (db *fontDB) loadDir(dir string, seen map[string]bool) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	entries, err := d.ReadDir(-1) // unsorted, like Rust's read_dir
	d.Close()
	if err != nil && len(entries) == 0 {
		return
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		isDir, isFile := e.IsDir(), e.Type().IsRegular()
		if e.Type()&os.ModeSymlink == 0 {
			if len(seen) > 0 {
				if seen[path] {
					continue
				}
				seen[path] = true
			}
		} else {
			st, err := os.Stat(path)
			if err != nil {
				continue
			}
			canon, err := filepath.EvalSymlinks(path)
			if err != nil {
				continue
			}
			if canon, err = filepath.Abs(canon); err != nil || seen[canon] {
				continue
			}
			seen[canon] = true
			path, isDir, isFile = canon, st.IsDir(), st.Mode().IsRegular()
		}
		if isFile {
			switch ext(path) {
			case "ttf", "ttc", "TTF", "TTC", "otf", "otc", "OTF", "OTC":
				db.loadFile(path)
			}
		} else if isDir {
			db.loadDir(path, seen)
		}
	}
}

// ext is Rust's Path::extension: none for "name" or ".name".
func ext(path string) string {
	name := filepath.Base(path)
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[i+1:]
	}
	return ""
}

// loadSystemFonts scans the directories fontdb scans, in its order.
func (db *fontDB) loadSystemFonts() {
	seen := map[string]bool{}
	switch runtime.GOOS {
	case "windows":
		if root, ok := os.LookupEnv("SYSTEMROOT"); ok {
			db.loadDir(filepath.Join(root, "Fonts"), seen)
		} else {
			db.loadDir(`C:\Windows\Fonts\`, seen)
		}
		if home, ok := os.LookupEnv("USERPROFILE"); ok {
			db.loadDir(filepath.Join(home, `AppData\Local\Microsoft\Windows\Fonts`), seen)
			db.loadDir(filepath.Join(home, `AppData\Roaming\Microsoft\Windows\Fonts`), seen)
		}
	case "darwin":
		db.loadDir("/Library/Fonts", seen)
		db.loadDir("/System/Library/Fonts", seen)
		if d, err := os.Open("/System/Library/AssetsV2"); err == nil {
			entries, _ := d.ReadDir(-1)
			d.Close()
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "com_apple_MobileAsset_Font") {
					db.loadDir(filepath.Join("/System/Library/AssetsV2", e.Name()), seen)
				}
			}
		}
		db.loadDir("/Network/Library/Fonts", seen)
		if home, ok := os.LookupEnv("HOME"); ok {
			db.loadDir(filepath.Join(home, "Library/Fonts"), seen)
		}
	case "android":
	default:
		if !db.loadFontconfig() {
			db.loadNoFontconfig()
		}
	}
}

func (db *fontDB) loadNoFontconfig() {
	seen := map[string]bool{}
	db.loadDir("/usr/share/fonts/", seen)
	db.loadDir("/usr/local/share/fonts/", seen)
	if home, ok := os.LookupEnv("HOME"); ok {
		db.loadDir(filepath.Join(home, ".fonts"), seen)
		db.loadDir(filepath.Join(home, ".local/share/fonts"), seen)
	}
}

// query is fontdb's CSS matching: the first family with any face wins, then stretch, style and weight
// narrow its faces. Families name faces exactly (case-sensitive); "" never matches.
func (db *fontDB) query(families []string, weight uint16, italic bool) (faceID, bool) {
	want := styleNormal
	if italic {
		want = styleItalic
	}
	for _, name := range families {
		var cands []*faceInfo
		for _, f := range db.faces() {
			if f.hasName(name) {
				cands = append(cands, f)
			}
		}
		if len(cands) > 0 {
			if f := bestMatch(cands, weight, want); f != nil {
				return f.id, true
			}
		}
	}
	return 0, false
}

// bestMatch is fontdb `find_best_match` for a normal-width query
// (https://www.w3.org/TR/2018/REC-css-fonts-3-20180920/#font-style-matching).
func bestMatch(cands []*faceInfo, weight uint16, want style) *faceInfo {
	keep := func(ok func(*faceInfo) bool) {
		out := cands[:0:0]
		for _, f := range cands {
			if ok(f) {
				out = append(out, f)
			}
		}
		cands = out
	}
	// font-stretch: normal (5), else the closest narrower, else the closest wider.
	const normal = 5
	stretch := uint16(0)
	for _, f := range cands {
		if f.stretch == normal {
			stretch = normal
		}
	}
	if stretch == 0 {
		for _, f := range cands {
			if f.stretch < normal && (stretch == 0 || normal-f.stretch < normal-stretch) {
				stretch = f.stretch
			}
		}
	}
	if stretch == 0 {
		for _, f := range cands {
			if stretch == 0 || f.stretch-normal < stretch-normal {
				stretch = f.stretch
			}
		}
	}
	keep(func(f *faceInfo) bool { return f.stretch == stretch })

	prefs := []style{styleNormal, styleOblique, styleItalic}
	if want == styleItalic {
		prefs = []style{styleItalic, styleOblique, styleNormal}
	}
	found := false
	for _, s := range prefs {
		for _, f := range cands {
			if f.style == s {
				found = true
			}
		}
		if found {
			keep(func(f *faceInfo) bool { return f.style == s })
			break
		}
	}
	if !found {
		return nil
	}

	has := func(w uint16) bool {
		for _, f := range cands {
			if f.weight == w {
				return true
			}
		}
		return false
	}
	// closest picks the first candidate with the smallest distance among those ok accepts.
	closest := func(ok func(*faceInfo) bool, dist func(*faceInfo) uint16) (uint16, bool) {
		var best *faceInfo
		for _, f := range cands {
			if ok(f) && (best == nil || dist(f) < dist(best)) {
				best = f
			}
		}
		if best == nil {
			return 0, false
		}
		return best.weight, true
	}
	all := func(*faceInfo) bool { return true }
	var w uint16
	switch {
	case has(weight):
		w = weight
	case weight >= 400 && weight < 450 && has(500):
		w = 500
	case weight >= 450 && weight <= 500 && has(400):
		w = 400
	case weight <= 500:
		var ok bool
		if w, ok = closest(func(f *faceInfo) bool { return f.weight <= weight }, func(f *faceInfo) uint16 { return weight - f.weight }); !ok {
			w, _ = closest(all, func(f *faceInfo) uint16 { return f.weight - weight })
		}
	default:
		var ok bool
		if w, ok = closest(func(f *faceInfo) bool { return f.weight >= weight }, func(f *faceInfo) uint16 { return f.weight - weight }); !ok {
			w, _ = closest(all, func(f *faceInfo) uint16 { return weight - f.weight })
		}
	}
	for _, f := range cands {
		if f.weight == w {
			return f
		}
	}
	return nil
}

// preferVariable drops the other faces (cached statics, installed copies) of families that have a variable
// font in the cache's var/ folder, so the variable face always wins the family query.
func preferVariable(db *fontDB, cache string) {
	varDir := filepath.Join(cache, "var")
	inVar := func(p string) bool {
		rel, err := filepath.Rel(varDir, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	variable := map[string]bool{}
	for _, f := range db.faces() {
		// Faces loaded from bytes at runtime are variable downloads too (their file is in var/).
		if (f.src.path == "" || inVar(f.src.path)) && f.isVariable() {
			for _, n := range f.families {
				variable[strings.ToLower(n.name)] = true
			}
		}
	}
	for _, f := range db.faces() {
		if f.src.path == "" || inVar(f.src.path) {
			continue
		}
		for _, n := range f.families {
			if variable[strings.ToLower(n.name)] {
				db.remove(f.id)
				break
			}
		}
	}
}
