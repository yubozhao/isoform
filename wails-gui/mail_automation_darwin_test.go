//go:build offfeatures && darwin

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import "testing"

func TestMailQuitAndRefusalAreToldApart(t *testing.T) {
	quit := "execution error: Error: Error: Application isn't running. (-600)"
	refused := "execution error: Error: Error: Not authorized to send Apple events to Mail. (-1743)"
	if !mailQuit(quit) || mailAutomationRefused(quit) {
		t.Fatal("an event to a Mail that quit is Mail closed, not a refusal")
	}
	if mailQuit(refused) || !mailAutomationRefused(refused) {
		t.Fatal("a refusal is not Mail closed")
	}
}
