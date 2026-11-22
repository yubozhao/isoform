//go:build (darwin && cgo) || linux

package remoteaccess

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

const listenerRecoveryAvailable = true

func stopRecordedListener(record listenerRecord) error {
	check := func(sig syscall.Signal) (bool, error) {
		matched, err := signalListener(record, sig)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return matched, err
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		matched, err := check(sig)
		if err != nil || !matched {
			return err
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
			matched, err = check(0)
			if err != nil || !matched {
				return err
			}
		}
	}
	return fmt.Errorf("dumbpipe pid %d survived SIGKILL", record.PID)
}
