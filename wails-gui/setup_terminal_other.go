//go:build !windows

package main

import "fmt"

func openWindowsSetupTerminal([]string) error {
	return fmt.Errorf("Windows terminal is unavailable on this platform")
}
