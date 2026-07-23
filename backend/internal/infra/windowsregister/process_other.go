//go:build !windows

package windowsregister

import "os/exec"

func hideWindowsConsole(cmd *exec.Cmd) {}
