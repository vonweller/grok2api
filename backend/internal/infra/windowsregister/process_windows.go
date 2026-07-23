//go:build windows

package windowsregister

import (
	"os/exec"
	"syscall"
)

func hideWindowsConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}
