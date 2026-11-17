//go:build !windows

package doc

import (
	"runtime"
	"strings"
	"syscall"
)

func systemErrno(e syscall.Errno) syscall.Errno { return e }

func removeFile(path string) error { return syscall.Unlink(path) }

// systemMessage is strerror's text as Rust reports it: Go's table writes it in lower case, and a code outside
// it is libc's "Unknown error N" ("Unknown error: N" on macOS) where Go says "errno N".
func systemMessage(e syscall.Errno) string {
	msg := e.Error()
	if n, ok := strings.CutPrefix(msg, "errno "); ok {
		sep := " "
		if runtime.GOOS == "darwin" {
			sep = ": "
		}
		msg = "Unknown error" + sep + n
	}
	if msg != "" && 'a' <= msg[0] && msg[0] <= 'z' {
		msg = string(msg[0]-'a'+'A') + msg[1:]
	}
	return msg
}
