//go:build windows

package agent

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// hideConsole configures Windows subprocesses launched from the GUI app so they never flash or open a console window.
func hideConsole(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	return cmd
}
