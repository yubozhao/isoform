//go:build offfeatures && darwin

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import "testing"

func TestHoldEventKitRefusalClassification(t *testing.T) {
	if !eventKitWriteRefused(eventKitErrorNotMutable) || !eventKitWriteRefused(eventKitErrorReadOnly) {
		t.Fatal("EventKit calendar refusal was not classified as unwritable")
	}
	if eventKitWriteRefused(eventKitErrorInternal) {
		t.Fatal("an internal failure was incorrectly classified as a calendar refusal")
	}
}
