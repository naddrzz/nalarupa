package export

import (
	"strings"
	"syscall"
	"unsafe"
)

var getFinalPathNameByHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")

// canonical is Rust's `fs::canonicalize` on Windows: the file opened (every link, junction and mount point on
// the way followed) and its final path read back. filepath.EvalSymlinks no longer follows junctions and mount
// points (Go 1.23), so a junction inside the workspace would pass as inside it. The `\\?\` prefix Rust keeps is
// dropped (`\\?\UNC\` becomes `\\`): both sides of every comparison are resolved here, so they still agree.
func canonical(p string) (string, error) {
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	h, err := syscall.CreateFile(name, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(h)
	buf := make([]uint16, 260)
	for {
		// FILE_NAME_NORMALIZED | VOLUME_NAME_DOS.
		n, _, err := getFinalPathNameByHandle.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
		if n == 0 {
			return "", err
		}
		if int(n) < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]uint16, n)
	}
	s := syscall.UTF16ToString(buf)
	if rest, ok := strings.CutPrefix(s, `\\?\UNC\`); ok {
		return `\\` + rest, nil
	}
	return strings.TrimPrefix(s, `\\?\`), nil
}
