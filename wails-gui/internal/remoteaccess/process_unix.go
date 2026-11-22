//go:build unix

package remoteaccess

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessGroup(cmd *exec.Cmd, done <-chan struct{}, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-done
}

// listen-tcp does not read stdin. Give its process group a controlling
// terminal: the kernel sends SIGHUP when the app's last master FD closes,
// including on SIGKILL. Keep stdout and stderr on their existing log pipes.
func startListener(cmd *exec.Cmd) (*os.File, error) {
	return pty.Start(cmd)
}

func lockListenerState(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, "dumbpipe.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock dumbpipe state: %w", err)
	}
	return f, nil
}
