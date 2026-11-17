package doc

import (
	"path/filepath"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// Relocate rewrites the relative references of root (image and shader `url`, `scriptUri`, `imports`)
// for a document moving from folder `from` to folder `to` (save as), so they reach the same files.
// Absolute paths, urls and `pencil:` ids stay.
func Relocate(root jsonv.Value, from, to string) {
	from, to = lexical(from), lexical(to)
	if imports := jsonv.Obj(jsonv.Get(root, "imports")); imports != nil {
		for _, k := range imports.Keys() {
			v, _ := imports.Get(k)
			if s, ok := jsonv.Str(v); ok {
				if n, ok := relocated(strings.ReplaceAll(s, "%20", " "), from, to); ok {
					imports.Set(k, strings.ReplaceAll(n, " ", "%20"))
				}
			}
		}
	}
	if children := jsonv.Get(root, "children"); children != nil {
		relocateIn(children, from, to)
	}
}

// SameDir reports whether two folders are the same one (by their paths, without touching the disk).
func SameDir(a, b string) bool {
	return lexical(a) == lexical(b)
}

// lexical is p made absolute with `.` and `..` folded away, without touching the disk.
func lexical(p string) string {
	if p == "" {
		p = "."
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

// relocated is the relative reference url (resolved against from) as written for a document in to;
// false when it is not a relative path (empty, absolute, a `scheme:` url or a `pencil:` id). Another
// drive gets an absolute path.
func relocated(url, from, to string) (string, bool) {
	prefix := ""
	if rest, ok := strings.CutPrefix(url, "file://"); ok { // loaders read `file://x` as relative too
		prefix, url = "file://", rest
	}
	scheme := false
	if s, _, ok := strings.Cut(url, ":"); ok && s != "" {
		scheme = strings.IndexFunc(s, func(c rune) bool {
			return !(c < 128 && (c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.ContainsRune("+-.", c)))
		}) < 0
	}
	if url == "" || scheme || url[0] == '/' || url[0] == '\\' || filepath.IsAbs(url) {
		return "", false
	}
	full := lexical(filepath.Join(from, url))
	target, base := PathComponents(full), PathComponents(to)
	common := 0
	for common < len(target) && common < len(base) && target[common] == base[common] {
		common++
	}
	if common == 0 {
		return prefix + strings.ReplaceAll(full, `\`, "/"), true
	}
	parts := make([]string, 0, len(base)-common+len(target)-common)
	for range len(base) - common {
		parts = append(parts, "..")
	}
	parts = append(parts, target[common:]...)
	rel := strings.Join(parts, "/")
	if strings.HasPrefix(rel, "..") {
		return prefix + rel, true
	}
	return prefix + "./" + rel, true
}

func relocateIn(v jsonv.Value, from, to string) {
	switch x := v.(type) {
	case *jsonv.Array:
		for _, it := range x.Items {
			relocateIn(it, from, to)
		}
	case *jsonv.Object:
		ty := jsonv.StrOr(jsonv.Get(x, "type"))
		fill := ty == "image" || ty == "shader"
		for _, k := range x.Keys() {
			it, _ := x.Get(k)
			if s, ok := it.(string); ok {
				if (k == "url" && fill) || k == "scriptUri" {
					if n, ok := relocated(s, from, to); ok {
						x.Set(k, n)
					}
				}
				continue
			}
			relocateIn(it, from, to)
		}
	}
}
