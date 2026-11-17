//go:build !windows

package export

import "path/filepath"

// canonical is Rust's `fs::canonicalize`: the path with every link followed.
func canonical(p string) (string, error) { return filepath.EvalSymlinks(p) }
