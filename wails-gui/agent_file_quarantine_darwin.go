package main

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// quarantineAgentFile marks a file an agent posted as downloaded and not yet
// approved, so macOS asks before anything runs it (a .terminal, .command,
// .app or .workflow), as it does for a file from the web.
func quarantineAgentFile(path string) error {
	value := fmt.Sprintf("0081;%08x;Isoform;", time.Now().Unix())
	return unix.Setxattr(path, quarantineXattr, []byte(value), 0)
}
