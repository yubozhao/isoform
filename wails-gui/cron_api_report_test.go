//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"encoding/json"
	"testing"
)

func TestAPIUpdateStatus(t *testing.T) {
	enabled := true
	disabled := false
	tests := []struct {
		name  string
		patch CronJobPatch
		want  string
	}{
		{"enable", CronJobPatch{Enabled: &enabled}, "resumed"},
		{"disable", CronJobPatch{Enabled: &disabled}, "paused"},
		{"content change", CronJobPatch{Name: strPtr("x")}, "changed"},
		{"empty", CronJobPatch{}, "changed"},
		// A prompt rewrite sent with enabled=true is a change, not a resume.
		{"enable with new prompt", CronJobPatch{Enabled: &enabled, TargetConfig: rawPtr(`{"prompt":"x"}`)}, "changed"},
		{"disable with new schedule", CronJobPatch{Enabled: &disabled, ScheduleConfig: rawPtr(`{"every":"5m"}`)}, "changed"},
	}
	for _, tt := range tests {
		if got := apiUpdateStatus(tt.patch); got != tt.want {
			t.Errorf("%s: apiUpdateStatus = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func strPtr(s string) *string { return &s }

func rawPtr(s string) *json.RawMessage {
	raw := json.RawMessage(s)
	return &raw
}
