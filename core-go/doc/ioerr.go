package doc

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// IOError is an error as Rust's `std::io::Error` displays it: an operating-system error is the system's
// message followed by ` (os error N)`, without the operation and path Go puts in front. Any other error is
// its own text.
func IOError(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		errno = systemErrno(errno)
		return systemMessage(errno) + " (os error " + strconv.FormatUint(uint64(errno), 10) + ")"
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err.Error()
	}
	return err.Error()
}

// RemoveFile is Rust's `fs::remove_file`: it removes a file (or link) but never a folder, with the one system
// call Rust makes (unlink, DeleteFileW), which fails on a folder.
func RemoveFile(path string) error {
	if err := removeFile(path); err != nil {
		return &fs.PathError{Op: "remove", Path: path, Err: err}
	}
	return nil
}

// Rename is Rust's `fs::rename`: one rename(2) on Unix (MoveFileExW replacing an existing file on Windows).
// Go's os.Rename checks for a folder at the destination first and reports "file exists" where the system
// call, and so Rust, reports "is a directory" or "directory not empty".
func Rename(from, to string) error {
	if err := rename(from, to); err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}

// CreateDirAll is Rust's `fs::create_dir_all`: it walks up from path until a folder is created or exists
// (a missing parent is created first), then creates the rest downwards. Unlike os.MkdirAll, a file in the way
// reports what creating that folder reports (on Windows "already exists", where Go says "not found").
func CreateDirAll(path string) error {
	if _, ok := ParentPath(path); path == "" || !ok {
		return nil
	}
	isDir := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && st.IsDir()
	}
	var uncreated []string
	for a := path; ; {
		parent, ok := ParentPath(a)
		if a == "" || !ok {
			break
		}
		err := os.Mkdir(a, 0o777)
		if err == nil {
			break
		}
		if errors.Is(err, fs.ErrNotExist) {
			uncreated = append(uncreated, a)
			a = parent
			continue
		}
		if errors.Is(err, fs.ErrExist) && isDir(a) {
			break
		}
		return err
	}
	for i := len(uncreated) - 1; i >= 0; i-- {
		if err := os.Mkdir(uncreated[i], 0o777); err != nil && (!errors.Is(err, fs.ErrExist) || !isDir(uncreated[i])) {
			return err
		}
	}
	return nil
}
