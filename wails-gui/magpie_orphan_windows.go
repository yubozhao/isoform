//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// Windows children use cmdutil's hidden-console launch. Shutdown terminates
// the owned child directly; Unix process groups do not apply here.

func setMagpieProcessGroup(*exec.Cmd) {}

func signalMagpieChild(cmd *exec.Cmd, sig syscall.Signal) error {
	return cmd.Process.Signal(sig)
}

func killMagpiePID(pid int) {
	if process, err := os.FindProcess(pid); err == nil {
		_ = process.Kill()
	}
}

func (s *magpieSidecar) reapStaleChild(string) {}
