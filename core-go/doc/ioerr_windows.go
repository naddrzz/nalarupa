package doc

import (
	"strconv"
	"strings"
	"syscall"
)

// systemErrno is the code Windows gives (and so Rust reports) where Go reports one of its own.
func systemErrno(e syscall.Errno) syscall.Errno {
	if e == syscall.EISDIR { // a file opened for writing is a folder
		return syscall.ERROR_ACCESS_DENIED
	}
	return e
}

func removeFile(path string) error {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return syscall.DeleteFile(p)
}

// asciiSpace is what Rust's `trim_ascii_end` trims.
const asciiSpace = " \t\n\f\r"

// systemMessage is Rust's `error_string`: FormatMessageW in the user's default language (langid 0; Go's own
// Errno.Error asks for US English), trailing ASCII whitespace trimmed. Go's invented codes keep Go's text.
func systemMessage(e syscall.Errno) string {
	if e >= syscall.APPLICATION_ERROR {
		return e.Error()
	}
	buf := make([]uint16, 2048)
	n, err := syscall.FormatMessage(syscall.FORMAT_MESSAGE_FROM_SYSTEM|syscall.FORMAT_MESSAGE_IGNORE_INSERTS, 0, uint32(e), 0, buf, nil)
	if err != nil || n == 0 {
		code := 0
		if errno, ok := err.(syscall.Errno); ok {
			code = int(errno)
		}
		return "OS Error " + strconv.Itoa(int(e)) + " (FormatMessageW() returned error " + strconv.Itoa(code) + ")"
	}
	return strings.TrimRight(syscall.UTF16ToString(buf[:n]), asciiSpace)
}
