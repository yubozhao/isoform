//go:build !unix

package remoteaccess

import (
	"os"
	"os/exec"
	"time"
)

func configureProcessGroup(*exec.Cmd) {}

func terminateProcessGroup(cmd *exec.Cmd, done <-chan struct{}, _ time.Duration) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
		<-done
	}
}

func startListener(cmd *exec.Cmd) (*os.File, error) { return nil, cmd.Start() }
func lockListenerState(string) (*os.File, error)    { return nil, nil }
