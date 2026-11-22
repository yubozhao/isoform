package remoteaccess

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func signalListener(r listenerRecord, sig syscall.Signal) (bool, error) {
	// Pin this process before reading /proc so PID reuse cannot redirect a signal.
	fd, err := unix.PidfdOpen(r.PID, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	dir := fmt.Sprintf("/proc/%d/", r.PID)
	path, err := os.Readlink(dir + "exe")
	if err != nil {
		return false, err
	}
	argv, err := os.ReadFile(dir + "cmdline")
	if err != nil {
		return false, err
	}
	env, err := os.ReadFile(dir + "environ")
	if err != nil {
		return false, err
	}
	args := strings.Split(strings.TrimSuffix(string(argv), "\x00"), "\x00")
	if !r.matches(strings.TrimSuffix(path, " (deleted)"), args, strings.Split(string(env), "\x00")) {
		return false, nil
	}
	if sig != 0 {
		err = unix.PidfdSendSignal(fd, sig, nil, 0)
	}
	return true, err
}
