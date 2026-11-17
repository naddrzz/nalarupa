package doc

import (
	"os"
	"runtime"
	"testing"
)

func TestPathsFollowRust(t *testing.T) {
	sep := string(os.PathSeparator)
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"a/b.pen", "a/b.pen", true}, {"a/b.pen", "a//b.pen", true}, {"a/./b.pen", "a/b.pen", true}, {"a/b/", "a/b", true},
		{"a/b/.", "a/b", true}, {"./a.pen", "a.pen", false}, {"a/../b.pen", "b.pen", false}, {"/a", "a", false},
	} {
		if SamePath(c.a, c.b) != c.same {
			t.Errorf("SamePath(%q, %q) != %v", c.a, c.b, c.same)
		}
	}
	for p, want := range map[string]string{"a/b/c.png": "a/b", "c.png": "", "/c": "/", "a/b/": "a", "a//b": "a", "a/b/.": "a", "a/./b": "a", "./x": "."} {
		if got, ok := ParentPath(p); !ok || got != want {
			t.Errorf("ParentPath(%q) = %q, want %q", p, got, want)
		}
	}
	for _, p := range []string{"", "/", "."} {
		if got, ok := ParentPath(p); ok && p != "." {
			t.Errorf("ParentPath(%q) = %q: none", p, got)
		}
	}
	for _, c := range [][3]string{{"a", "b.pen", "a" + sep + "b.pen"}, {"a/", "b", "a/b"}, {"", "b", "b"}} {
		if got := JoinPath(c[0], c[1]); got != c[2] {
			t.Errorf("JoinPath(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	if runtime.GOOS == "windows" {
		// A rooted path without a drive keeps the base's drive; drive letters compare in either case.
		if got := JoinPath(`C:\docs\x`, `/lib.pen`); got != `C:/lib.pen` {
			t.Errorf("rooted join: %q", got)
		}
		if got := JoinPath(`C:\docs`, `D:\lib.pen`); got != `D:\lib.pen` {
			t.Errorf("absolute join: %q", got)
		}
		if !SamePath(`c:\docs\a.pen`, `C:/docs/a.pen`) {
			t.Error("drive letters compare upper-cased")
		}
		// UNC and device prefixes compare by their parts, whichever separators they are written with (as Rust's Path does).
		for _, c := range []struct {
			a, b string
			same bool
		}{
			{`\\srv\share\a`, `//srv/share/a`, true}, {`\\srv/share\a`, `\\srv\share\a`, true}, {`/\srv\share\a`, `\\srv\share\a`, true},
			{`\\.\pipe\x`, `\\./pipe\x`, true}, {`//srv/share/a`, `/srv/share/a`, false}, {`\\SRV\share\a`, `\\srv\share\a`, false},
		} {
			if SamePath(c.a, c.b) != c.same {
				t.Errorf("SamePath(%q, %q) != %v", c.a, c.b, c.same)
			}
		}
	} else if got := JoinPath("/docs/x", "/lib.pen"); got != "/lib.pen" {
		t.Errorf("rooted join: %q", got)
	}
}
