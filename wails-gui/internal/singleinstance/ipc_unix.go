//go:build !windows

package singleinstance

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"wails-gui/internal/apphome"
)

// socketPath resolves the Unix domain socket path for the given appID.
// Lives next to the flock file under ~/.yansu-agent/ so they share the
// same per-user scoping.
func socketPath(appID string) (string, error) {
	dataDir, err := apphome.Dir(BaseDirName)
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(dataDir, "."+appID+".sock"), nil
}

func listenIPC(appID string) (net.Listener, error) {
	path, err := socketPath(appID)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}

	// Stale socket file from a crashed previous instance. Safe to remove
	// because acquireLock has already taken the flock — any process that
	// could have legitimately been listening is gone.
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path)
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", path, err)
	}
	return l, nil
}

//isoform:destination local
func dialIPC(appID string, timeout time.Duration) (net.Conn, error) {
	path, err := socketPath(appID)
	if err != nil {
		return nil, err
	}
	return net.DialTimeout("unix", path, timeout)
}
