//go:build darwin

package main

import (
	"os"

	"wails-gui/internal/platform/imessage"
)

// systemMessagesHistorySupported reports whether this platform keeps a
// Messages database Isoform can read. Only macOS does.
func systemMessagesHistorySupported() bool { return true }

// messagesHistoryAccess reports whether Isoform can open the Messages
// database: Full Disk Access granted and Messages used on this computer. It
// never prompts.
func messagesHistoryAccess() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return imessage.Access(imessage.DefaultPath(home)) == imessage.StateAllowed
}
