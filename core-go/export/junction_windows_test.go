package export_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/export"
)

// A junction (`mklink /J`, no admin needed) inside the workspace does not make its target count as inside: it is
// followed as Rust's `canonicalize` follows it, also when it dangles. agent's TestPermissionPathsFollowSymlinks
// covers symbolic links on Unix.
func TestJunctionsAreFollowed(t *testing.T) {
	base := t.TempDir()
	ws, out := filepath.Join(base, "project"), filepath.Join(base, "outside")
	for _, d := range []string{ws, out, filepath.Join(out, "gone")} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	junction := func(at, target string) {
		if b, err := exec.Command("cmd", "/c", "mklink", "/J", at, target).CombinedOutput(); err != nil {
			t.Fatalf("mklink /J: %v %s", err, b)
		}
	}
	junction(filepath.Join(ws, "j"), out)
	junction(filepath.Join(ws, "inner"), filepath.Join(ws, "sub"))
	junction(filepath.Join(ws, "dangling"), filepath.Join(out, "gone"))
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(out, "gone")); err != nil {
		t.Fatal(err)
	}
	ok := func(f string) bool { return export.PathAllowed(ws+`\`+f, "Write", ws, "", "") }
	if ok(`j\secret.txt`) {
		t.Error("a new file through a junction to outside")
	}
	if !ok(`inner\new.txt`) {
		t.Error("a junction that stays inside the workspace")
	}
	if ok(`dangling\x.txt`) {
		t.Error("a dangling junction to outside")
	}
	if !ok(`plain\new.txt`) {
		t.Error("a plain new file in the workspace stays allowed")
	}
	if export.WritePolicy(ws+`\j\secret.txt`, ws, "") {
		t.Error("a script export through a junction")
	}
}
