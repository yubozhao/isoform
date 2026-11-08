//go:build windows

package main

import (
	"os/exec"
	"strings"
	"syscall"
)

// Only explicit sign-in/install clicks open a visible terminal. The command
// consists entirely of the allowlisted assistant's command and static arguments.
func openWindowsSetupTerminal(command []string) error {
	cmd := exec.Command("cmd.exe", "/D", "/K", strings.Join(command, " "))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010} // CREATE_NEW_CONSOLE
	return cmd.Start()
}
