package doc

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Paths as Rust's `Path` handles them: written paths stay as written (nothing is cleaned), so paths that reach
// files and error texts read as Rust's.

func isSep(c byte) bool { return c == '/' || c == os.PathSeparator }

// JoinPath is Rust's `PathBuf::join`: an absolute p replaces base, a rooted p without a volume keeps base's
// volume, anything else is appended after one separator.
func JoinPath(base, p string) string {
	switch {
	case filepath.IsAbs(p) || filepath.VolumeName(p) != "" || base == "":
		return p
	case strings.HasPrefix(p, `/`) || strings.HasPrefix(p, string(os.PathSeparator)):
		return filepath.VolumeName(base) + p
	case strings.HasSuffix(base, `/`) || strings.HasSuffix(base, string(os.PathSeparator)):
		return base + p
	}
	return base + string(os.PathSeparator) + p
}

// ParentPath is Rust's `Path::parent` of p, as written: p without its last component and the separators (and
// `.` components) before it ("" for a bare name); false when p has no component to drop ("", a root, a volume).
func ParentPath(p string) (string, bool) {
	vol := len(filepath.VolumeName(p))
	root := vol < len(p) && isSep(p[vol])
	start := vol
	if root {
		start++
	}
	// trim drops trailing separators and `.` components (a leading `.` of a relative path is a component).
	trim := func(s string) string {
		for len(s) > start {
			if isSep(s[len(s)-1]) {
				s = s[:len(s)-1]
				continue
			}
			at := start
			for i := len(s) - 1; i >= start; i-- {
				if isSep(s[i]) {
					at = i + 1
					break
				}
			}
			if s[at:] != "." || at == vol && !root {
				break
			}
			s = s[:at]
		}
		return s
	}
	s := trim(p)
	if len(s) <= start {
		return "", false
	}
	at := start
	for i := len(s) - 1; i >= start; i-- {
		if isSep(s[i]) {
			at = i
			break
		}
	}
	return trim(s[:at]), true
}

// PathComponents is Rust's `Path::components`: the volume (a drive letter upper-cased, as Rust compares it), the
// root, then the names; empty names and `.` are dropped, except a leading `.` of a relative path.
func PathComponents(p string) []string {
	vol := filepath.VolumeName(p)
	rest := p[len(vol):]
	var out []string
	if vol != "" {
		if len(vol) == 2 && vol[1] == ':' {
			vol = strings.ToUpper(vol)
		}
		out = append(out, vol)
	}
	rooted := rest != "" && isSep(rest[0])
	if rooted {
		out = append(out, string(os.PathSeparator))
	}
	names := strings.FieldsFunc(rest, func(r rune) bool { return r < 0x80 && isSep(byte(r)) })
	for i, n := range names {
		if n == "." && (i > 0 || rooted || vol != "") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// SamePath is Rust's `Path` equality: by components, whichever separators they are written with.
func SamePath(a, b string) bool { return slices.Equal(PathComponents(a), PathComponents(b)) }
