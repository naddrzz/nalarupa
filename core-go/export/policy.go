package export

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/naddrzz/nalarupa/core-go/doc"
)

// PathPolicy says whether a script may write file, given the export base folder and the open document's
// path ("" when unsaved). The agent supplies its file tools' rule (agent.rs `path_allowed` with "Write");
// nil allows every path.
type PathPolicy func(file, base, docPath string) bool

// CheckScriptExport is html.rs `check_script_export`. A script (the agent's `execute`) exports without a
// permission card, so it gets the file tools' rule: inside the document's folder, never the open document
// itself, and a PDF / HTML file must be named like one so it cannot replace some other file (another
// document, a dotfile) in that folder.
func CheckScriptExport(d *doc.Doc, dataDir, format, out string, allowed PathPolicy) error {
	base := exportBase(d.Path, dataDir)
	// The default destination is checked too: a link at its place could point anywhere.
	def := "exports"
	switch format {
	case "pdf":
		def = d.Title() + ".pdf"
	case "html-css", "html-tailwind", "html":
		def = "export.html"
	}
	file := outPath(base, out, def)
	hidden := strings.HasPrefix(FileName(file), ".")
	ext := func(want ...string) bool {
		x, ok := extension(file)
		return !hidden && ok && slices.ContainsFunc(want, func(w string) bool { return strings.EqualFold(x, w) })
	}
	var named bool
	switch format {
	case "pdf":
		named = ext("pdf")
	case "html-css", "html-tailwind", "html":
		named = ext("html", "htm")
	default:
		named = !hidden
	}
	if !named {
		want := "html"
		if format == "pdf" {
			want = "pdf"
		}
		return fmt.Errorf("'%s' is not a %s file name; use a .%s path that does not start with a dot", file, format, want)
	}
	if allowed != nil && !allowed(file, base, d.Path) {
		return fmt.Errorf("Export writes only inside %s and never over the open document; ask the user to export elsewhere", base)
	}
	return nil
}

// scriptWritten holds the files a script's export wrote since the app started (normalized paths): the only
// existing files it may write again, so the agent can export the same file after a fix.
var scriptWritten struct {
	sync.Mutex
	files []string
}

func scriptWrote(file string) bool {
	n := normalize(file)
	scriptWritten.Lock()
	defer scriptWritten.Unlock()
	return slices.Contains(scriptWritten.files, n)
}

func noteScriptWrote(file string) {
	n := normalize(file)
	scriptWritten.Lock()
	defer scriptWritten.Unlock()
	scriptWritten.files = append(scriptWritten.files, n)
}

func taken(file string) error {
	return fmt.Errorf("%s already exists and Export did not write it; export under a new name, or ask the user before replacing it", file)
}

// ClaimScriptFiles is html.rs `claim_script_files`: a script's export never replaces a file it did not write,
// so each file of the job must be new or one an earlier script export wrote. Checked here so a refused export
// renders nothing, and again as Run creates each file.
func ClaimScriptFiles(j *Job) error {
	for _, f := range j.files {
		// Lstat of the path itself: a link counts as an existing file even when it dangles.
		if _, err := os.Lstat(f.path); err == nil && !scriptWrote(f.path) {
			return taken(f.path)
		}
	}
	j.script = true
	return nil
}

// normalize is agent.rs `normalize`: the absolute path with `.` and `..` resolved and symlinks followed. The
// deepest existing ancestor is resolved by the OS (which follows links before `..`), the not-yet-existing rest
// is appended lexically; a dangling link is followed by hand, and a link loop resolves to "".
func normalize(p string) string { return resolvePath(p, 0) }

func resolvePath(p string, depth int) string {
	if depth > 40 {
		return ""
	}
	abs := absolute(p)
	for a, ok := abs, true; ok; a, ok = parent(a) {
		rest := strings.TrimLeft(abs[len(a):], `/`+string(os.PathSeparator))
		if real, err := filepath.EvalSymlinks(a); err == nil {
			return filepath.Clean(join(real, rest))
		}
		if target, err := os.Readlink(a); err == nil {
			up, ok := parent(a)
			if !ok {
				up = a
			}
			next := join(up, target)
			if rest != "" {
				next = join(next, rest)
			}
			return resolvePath(next, depth+1)
		}
	}
	return filepath.Clean(abs)
}

// absolute is Rust's `std::path::absolute`: on Windows the OS's full path (`..` resolved lexically); elsewhere
// the path against the working folder with `.` and repeated separators dropped but `..` kept, since it can
// only be resolved after the links before it.
func absolute(p string) string {
	if runtime.GOOS == "windows" {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	if !filepath.IsAbs(p) {
		wd, err := os.Getwd()
		if err != nil {
			return p
		}
		p = wd + "/" + p
	}
	var parts []string
	for _, c := range strings.Split(p, "/") {
		if c != "" && c != "." {
			parts = append(parts, c)
		}
	}
	return "/" + strings.Join(parts, "/")
}
