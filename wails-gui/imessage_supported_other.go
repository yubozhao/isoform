//go:build !darwin

package main

// systemMessagesHistorySupported reports whether this platform keeps a
// Messages database Isoform can read. Only macOS does.
func systemMessagesHistorySupported() bool { return false }
