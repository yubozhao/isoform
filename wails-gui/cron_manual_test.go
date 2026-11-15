//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wails-gui/storage"
)

// Regression coverage for ScheduleKind="manual". Manual automations are
// activity-extracted jobs that must never auto-fire but can still be triggered
// via RunNow or promoted to a cron schedule. If any of these assertions break,
// a PR is likely removing the "manual" case from a switch statement.

func TestComputeFirstRun_Manual(t *testing.T) {
	got, err := computeFirstRun("manual", json.RawMessage(`{}`), time.Now())
	if err != nil {
		t.Fatalf("computeFirstRun(manual) returned error: %v", err)
	}
	if got != 0 {
		t.Errorf("computeFirstRun(manual) = %d, want 0 (manual jobs never auto-fire)", got)
	}
}

func TestComputeNextRun_Manual(t *testing.T) {
	row := &storage.CronJobRow{
		ID:             "job-manual",
		ScheduleKind:   "manual",
		ScheduleConfig: `{}`,
		LastRunAt:      time.Now().UnixMilli(),
	}
	if got := computeNextRun(row); got != 0 {
		t.Errorf("computeNextRun(manual) = %d, want 0 (even after a run, manual jobs stay dormant)", got)
	}
}

func TestFormatScheduleDescription_Manual(t *testing.T) {
	got := formatScheduleDescription("manual", `{}`)
	if got != "manual only" {
		t.Errorf("formatScheduleDescription(manual) = %q, want %q", got, "manual only")
	}
}

func TestValidateAutomationFrequency_Manual(t *testing.T) {
	// Manual jobs have no cadence and must bypass the 15-minute floor that
	// "every"/"cron" jobs are held to.
	if err := validateAutomationFrequency("manual", json.RawMessage(`{}`)); err != nil {
		t.Errorf("validateAutomationFrequency(manual) returned error: %v, want nil", err)
	}
	if err := validateAutomationFrequency("manual", nil); err != nil {
		t.Errorf("validateAutomationFrequency(manual, nil config) returned error: %v, want nil", err)
	}
}

// Exercises the activity-source schedule policy: the LLM is trusted only when
// it commits to "cron" AND supplies a verbatim evidence quote. Any other shape
// — missing kind, empty config, cron without evidence, malformed values — must
// degrade to manual so the scheduler never auto-fires a hallucinated job.
func TestResolveActivitySchedule(t *testing.T) {
	longEvidence := strings.Repeat("a", maxScheduleEvidenceLen+100)

	tests := []struct {
		name         string
		kind         string
		config       json.RawMessage
		evidence     string
		wantKind     string
		wantConfig   string
		wantEvidence string
	}{
		{
			name:         "cron with evidence trusted",
			kind:         "cron",
			config:       json.RawMessage(`{"expr":"0 9 * * 1","tz":"Asia/Shanghai"}`),
			evidence:     "每周一早上 9 点生成上周 PR 报告",
			wantKind:     "cron",
			wantConfig:   `{"expr":"0 9 * * 1","tz":"Asia/Shanghai"}`,
			wantEvidence: "每周一早上 9 点生成上周 PR 报告",
		},
		{
			name:         "cron without evidence falls back to manual",
			kind:         "cron",
			config:       json.RawMessage(`{"expr":"0 9 * * 1"}`),
			evidence:     "",
			wantKind:     "manual",
			wantConfig:   `{}`,
			wantEvidence: "",
		},
		{
			name:         "cron with whitespace-only evidence falls back to manual",
			kind:         "cron",
			config:       json.RawMessage(`{"expr":"0 9 * * 1"}`),
			evidence:     "   \n  ",
			wantKind:     "manual",
			wantConfig:   `{}`,
			wantEvidence: "",
		},
		{
			name:         "cron with empty config falls back to manual",
			kind:         "cron",
			config:       nil,
			evidence:     "every Monday at 9am",
			wantKind:     "manual",
			wantConfig:   `{}`,
			wantEvidence: "",
		},
		{
			name:         "explicit manual stays manual and drops any stray evidence",
			kind:         "manual",
			config:       json.RawMessage(`{}`),
			evidence:     "every Monday",
			wantKind:     "manual",
			wantConfig:   `{}`,
			wantEvidence: "",
		},
		{
			name:         "missing kind falls back to manual",
			kind:         "",
			config:       nil,
			evidence:     "",
			wantKind:     "manual",
			wantConfig:   `{}`,
			wantEvidence: "",
		},
		{
			name:         "every is forbidden and degrades to manual",
			kind:         "every",
			config:       json.RawMessage(`{"every_ms":3600000}`),
			evidence:     "hourly",
			wantKind:     "manual",
			wantConfig:   `{}`,
			wantEvidence: "",
		},
		{
			name:         "long ascii evidence is truncated",
			kind:         "cron",
			config:       json.RawMessage(`{"expr":"0 9 * * *"}`),
			evidence:     longEvidence,
			wantKind:     "cron",
			wantConfig:   `{"expr":"0 9 * * *"}`,
			wantEvidence: longEvidence[:maxScheduleEvidenceLen] + "…",
		},
		{
			// Regression: bytes-vs-runes. Each Chinese char is 3 UTF-8 bytes,
			// so a byte-based truncation would split mid-rune and produce
			// invalid UTF-8 or corrupted glyphs. Rune-based truncation must
			// keep the first maxScheduleEvidenceLen chars intact.
			name:         "long chinese evidence is truncated by runes not bytes",
			kind:         "cron",
			config:       json.RawMessage(`{"expr":"0 9 * * *"}`),
			evidence:     strings.Repeat("周", maxScheduleEvidenceLen+50),
			wantKind:     "cron",
			wantConfig:   `{"expr":"0 9 * * *"}`,
			wantEvidence: strings.Repeat("周", maxScheduleEvidenceLen) + "…",
		},
		{
			// Regression: invalid cron configs (empty object, missing expr,
			// sub-15min interval) must fall back to manual. Before the fix
			// these were accepted by resolveActivitySchedule and then killed
			// downstream in validateAutomationFrequency, skipping the whole
			// automation instead of degrading it.
			name:         "cron with empty object config falls back to manual",
			kind:         "cron",
			config:       json.RawMessage(`{}`),
			evidence:     "每周一早上 9 点",
			wantKind:     "manual",
			wantConfig:   `{}`,
			wantEvidence: "",
		},
		{
			name:         "cron with tz-only config falls back to manual",
			kind:         "cron",
			config:       json.RawMessage(`{"tz":"Asia/Shanghai"}`),
			evidence:     "每周一早上 9 点",
			wantKind:     "manual",
			wantConfig:   `{}`,
			wantEvidence: "",
		},
		{
			name:         "cron with malformed expr falls back to manual",
			kind:         "cron",
			config:       json.RawMessage(`{"expr":"not a cron"}`),
			evidence:     "每周一早上 9 点",
			wantKind:     "manual",
			wantConfig:   `{}`,
			wantEvidence: "",
		},
		{
			name:         "cron below 15-minute floor falls back to manual",
			kind:         "cron",
			config:       json.RawMessage(`{"expr":"*/5 * * * *"}`),
			evidence:     "每 5 分钟",
			wantKind:     "manual",
			wantConfig:   `{}`,
			wantEvidence: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind, config, evidence := resolveActivitySchedule(tc.kind, tc.config, tc.evidence)
			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
			if string(config) != tc.wantConfig {
				t.Errorf("config = %q, want %q", string(config), tc.wantConfig)
			}
			if evidence != tc.wantEvidence {
				t.Errorf("evidence = %q, want %q", evidence, tc.wantEvidence)
			}
		})
	}
}
