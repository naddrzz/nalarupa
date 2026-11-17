package export

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
)

// FileStem is a node id as a file name (html.rs `file_stem`): anything but ASCII letters, digits, `-` and `_`
// (a separator, `..`) becomes `_`, one per character.
func FileStem(id string) string {
	var b strings.Builder
	for _, c := range id {
		if c < 0x80 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// FileName is the last component of p, or "" when there is none or it is `..` (Rust `Path::file_name`).
func FileName(p string) string {
	p = strings.TrimRight(p, `/`+string(os.PathSeparator))
	p = p[len(filepath.VolumeName(p)):]
	at := strings.LastIndexAny(p, `/`+string(os.PathSeparator))
	switch name := p[at+1:]; name {
	case "..":
		return ""
	case ".":
		// A trailing `.` is no component of its own.
		if at < 0 {
			return ""
		}
		return FileName(p[:at])
	default:
		return name
	}
}

// extension is the text after the last dot of the file name, when the name does not start with that dot
// (Rust `Path::extension`).
func extension(p string) (string, bool) {
	name := FileName(p)
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return "", false
	}
	return name[i+1:], true
}

// exportBase is where exports go when no path is given, and the only place a script's `Export` may write: the
// document's folder (Rust's `Path::parent` of its path, as written: "" for a bare file name), or the data
// folder `exports` for an unsaved document.
func exportBase(docPath, dataDir string) string {
	if dir, ok := doc.ParentPath(docPath); ok {
		return dir
	}
	return doc.JoinPath(dataDir, "exports")
}

// outPath is html.rs `out_path`: out, or base/defaultName when out is empty, joined onto base when it is
// relative (so a relative base is joined twice onto an empty out, as in Rust).
func outPath(base, out, defaultName string) string {
	p := out
	if out == "" {
		p = doc.JoinPath(base, defaultName)
	}
	if filepath.IsAbs(p) {
		return p
	}
	return doc.JoinPath(base, p)
}
