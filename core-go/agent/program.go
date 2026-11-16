package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// command runs program as Rust's `Command::new(program)` finds it. On Windows a bare name gets `.exe` when it has no
// extension and is searched in the application's folder, the system folders and PATH, never through PATHEXT: a
// `claude.cmd` shim would mangle the multi-line system prompt argument, and killing it would leave its node child
// running. A path is used as is (Rust's tests point at a `.cmd` stand-in that way). Elsewhere it is exec's PATH
// search, as Rust's.
func command(program string, args ...string) *exec.Cmd {
	if runtime.GOOS != "windows" || strings.ContainsAny(program, `/\`) {
		return exec.Command(program, args...)
	}
	name := program
	if filepath.Ext(name) == "" {
		name += ".exe"
	}
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		dirs = append(dirs, filepath.Join(root, "System32"), root)
	}
	dirs = append(dirs, filepath.SplitList(os.Getenv("PATH"))...)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return exec.Command(p, args...)
		}
	}
	cmd := exec.Command(program, args...)
	cmd.Err = &exec.Error{Name: program, Err: exec.ErrNotFound}
	return cmd
}
