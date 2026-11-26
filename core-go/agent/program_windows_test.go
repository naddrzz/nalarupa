//go:build windows

package agent

import "testing"

func TestCommandHidesWindowsConsole(t *testing.T) {
	const createNoWindow = 0x08000000
	cmd := command("cmd.exe", "/C", "echo ok")
	if cmd.SysProcAttr == nil {
		t.Fatal("expected SysProcAttr to be configured on Windows so subprocess does not open a visible terminal window")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("expected SysProcAttr.HideWindow == true")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Errorf("expected CREATE_NO_WINDOW (0x08000000) in CreationFlags, got 0x%x", cmd.SysProcAttr.CreationFlags)
	}
}
