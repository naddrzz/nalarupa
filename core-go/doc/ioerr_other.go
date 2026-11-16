//go:build !windows

package doc

import "syscall"

func systemErrno(e syscall.Errno) syscall.Errno { return e }

// systemMessage is strerror's text as Rust reports it: Go's table writes it in lower case.
func systemMessage(e syscall.Errno) string {
	msg := e.Error()
	if msg != "" && 'a' <= msg[0] && msg[0] <= 'z' {
		msg = string(msg[0]-'a'+'A') + msg[1:]
	}
	return msg
}
