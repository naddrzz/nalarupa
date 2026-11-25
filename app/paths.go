package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// DefaultSavePath returns a non-destructive <stem>.nala (<stem>-1.nala, ...) path inside dir
// (defaulting to ~/Documents/Nalarupa, or ./Documents/Nalarupa if home is unavailable).
func DefaultSavePath(title string, dir ...string) string {
	baseDir := ""
	if len(dir) > 0 && strings.TrimSpace(dir[0]) != "" {
		baseDir = dir[0]
	} else {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			if wd, werr := os.Getwd(); werr == nil && wd != "" {
				home = wd
			} else {
				home = "."
			}
		}
		baseDir = filepath.Join(home, "Documents", "Nalarupa")
	}

	stem := sanitizeStem(title)
	if stem == "" {
		stem = "Untitled"
	}
	_ = os.MkdirAll(baseDir, 0o755)
	candidate := filepath.Join(baseDir, stem+".nala")
	if _, err := os.Lstat(candidate); err != nil {
		// Available when not-exist; on any other Lstat error (permission/invalid path), return immediately
		// rather than scanning 9,999 suffixes so save surfaces the real OS error.
		return candidate
	}
	for i := 1; i < 10000; i++ {
		next := filepath.Join(baseDir, fmt.Sprintf("%s-%d.nala", stem, i))
		if _, err := os.Lstat(next); err != nil {
			return next
		}
	}
	copyCandidate := filepath.Join(baseDir, stem+"-copy.nala")
	if _, err := os.Lstat(copyCandidate); err != nil {
		return copyCandidate
	}
	return filepath.Join(baseDir, fmt.Sprintf("%s-%d.nala", stem, os.Getpid()))
}

func sanitizeStem(title string) string {
	s := strings.TrimSpace(title)
	s = strings.TrimSuffix(s, ".nala")
	s = strings.TrimSuffix(s, ".pen")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteByte('-')
		} else {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .")
	return out
}
