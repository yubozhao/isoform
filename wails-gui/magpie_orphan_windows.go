//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// The bundled magpie is macOS-only, so the process-group and stale-child
// handling in magpie_orphan_unix.go has nothing to do here.

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
