package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// allFlagsOn retains the complete historical prompt-builder contract. The
// production activity-summary path applies activitySummaryTriageFlags first.
func allFlagsOn() triageFeatureFlags {
	return triageFeatureFlags{
		Memory: true, Knowledge: true, Shadow: true,
		Automation: true, Handoff: true, SOP: true,
	}
}

func TestActivitySummaryTriageFlags_RetiresWorkOriginationOnly(t *testing.T) {
	settings := allFlagsOn()
	got := activitySummaryTriageFlags(settings)

	if !got.Memory || !got.Knowledge || !got.SOP {
		t.Fatalf("learning outputs were disabled: %+v", got)
	}
	if got.Shadow || got.Automation || got.Handoff {
		t.Fatalf("legacy work outputs remained enabled: %+v", got)
	}
	if !settings.Shadow || !settings.Automation || !settings.Handoff {
		t.Fatalf("input settings were mutated: %+v", settings)
	}
}

func TestActivitySummaryTriageFlags_OmitsRetiredPromptAndResponseFields(t *testing.T) {
	flags := activitySummaryTriageFlags(allFlagsOn())
	prompt := buildTriagePrompt(
		flags,
		"summary",
		"LANG",
		"MEMORY",
		"KNOWLEDGE",
		"LEGACY_AUTOMATION_DEDUP_SENTINEL",
		"LEGACY_HANDOFF_DEDUP_SENTINEL",
		"PLAYBOOK",
		"",
	)
	for _, retired := range []string{
		"## Shadow initiative prediction (worthInitiative)",
		"## Automation (worthAutomation)",
		"## Handoff (worthHandoff)",
		`"worthInitiative"`,
		`"worthAutomation"`,
		`"worthHandoff"`,
		"LEGACY_AUTOMATION_DEDUP_SENTINEL",
		"LEGACY_HANDOFF_DEDUP_SENTINEL",
	} {
		if strings.Contains(prompt, retired) {
			t.Errorf("activity-summary prompt retained %q", retired)
		}
	}
	for _, kept := range []string{
		"## Memory (worthMemory)",
		"## Knowledge (worthKnowledge)",
		"## SOP (worthSOP)",
		"MEMORY",
		"KNOWLEDGE",
		"PLAYBOOK",
	} {
		if !strings.Contains(prompt, kept) {
			t.Errorf("activity-summary prompt omitted %q", kept)
		}
	}

	response := &activityTriageResponse{
		WorthInitiative:     true,
		PredictedUserAction: "legacy prediction",
		WorthAutomation:     true,
		AutomationPrompt:    "legacy schedule",
		WorthHandoff:        true,
		HandoffPrompt:       "legacy handoff",
		WorthMemory:         true,
		WorthKnowledge:      true,
		WorthSOP:            true,
	}
	applyTriageFeatureFlags(response, flags)
	if response.WorthInitiative || response.PredictedUserAction != "" ||
		response.WorthAutomation || response.AutomationPrompt != "" ||
		response.WorthHandoff || response.HandoffPrompt != "" {
		t.Fatalf("retired model output survived sanitization: %+v", response)
	}
	if !response.WorthMemory || !response.WorthKnowledge || !response.WorthSOP {
		t.Fatalf("learning model output was cleared: %+v", response)
	}
}

func TestTriageFeatureFlags_AnyEnabled(t *testing.T) {
	tests := []struct {
		name  string
		flags triageFeatureFlags
		want  bool
	}{
		{"all off", triageFeatureFlags{}, false},
		{"only memory", triageFeatureFlags{Memory: true}, true},
		{"only knowledge", triageFeatureFlags{Knowledge: true}, true},
		{"only shadow", triageFeatureFlags{Shadow: true}, true},
		{"only automation", triageFeatureFlags{Automation: true}, true},
		{"only handoff", triageFeatureFlags{Handoff: true}, true},
		{"only sop", triageFeatureFlags{SOP: true}, true},
		{"all on", allFlagsOn(), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.flags.anyEnabled(); got != tc.want {
				t.Errorf("anyEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildTriagePrompt_AllOnContainsEveryFeatureSection(t *testing.T) {
	got := buildTriagePrompt(allFlagsOn(), "summary body", "LANG", "MEM", "KN", "AUTO_DEDUP", "HO_DEDUP", "SOP_DEDUP", "")
	// All per-feature rule sections must be present.
	for _, want := range []string{
		"## Memory (worthMemory)",
		"## Knowledge (worthKnowledge)",
		"## Shadow initiative prediction (worthInitiative)",
		"## Automation (worthAutomation)",
		"## Handoff (worthHandoff)",
		"## SOP (worthSOP)",
		"## Self-validation",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing section %q", want)
		}
	}
	// JSON template must list every worth flag and every companion field.
	for _, want := range []string{
		`"worthMemory": true/false`,
		`"worthKnowledge": true/false`,
		`"worthInitiative": true/false`,
		`"predictedUserAction"`,
		`"shadowCapabilityKey"`,
		`"shadowExpectedResult"`,
		`"shadowReasoningHypotheses"`,
		`"shadowResultConfidence"`,
		`"worthAutomation": true/false`,
		`"worthHandoff": true/false`,
		`"worthSOP": true/false`,
		`"automationName"`,
		`"automationScheduleKind"`,
		`"handoffName"`,
		`"handoffPrompt"`,
		`"sopName"`,
		`"sopOutline"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing JSON template field %q", want)
		}
	}
	if !strings.Contains(got, "LANG") {
		t.Errorf("prompt missing language instruction")
	}
	if !strings.Contains(got, "summary body") {
		t.Errorf("prompt missing activity summary")
	}
	if !strings.Contains(got, "MEM") || !strings.Contains(got, "KN") || !strings.Contains(got, "AUTO_DEDUP") || !strings.Contains(got, "HO_DEDUP") || !strings.Contains(got, "SOP_DEDUP") {
		t.Errorf("prompt missing one or more dedup blocks")
	}
	for _, banned := range []string{
		`"matchedInitiativeId"`,
		`"observedUserAction"`,
		`"predictionAssessment"`,
	} {
		if strings.Contains(got, banned) {
			t.Errorf("prediction prompt leaked legacy comparison field %q", banned)
		}
	}
}

// When the user only keeps Memory + Knowledge on, the prompt must drop
// every Automation / Handoff section + JSON field. This is the
// scenario that motivated the toggle: a pro-plan user out of quota who
// only wants memory + knowledge from triage.
func TestBuildTriagePrompt_MemoryKnowledgeOnly(t *testing.T) {
	flags := triageFeatureFlags{Memory: true, Knowledge: true}
	got := buildTriagePrompt(flags, "s", "L", "MEM", "KN", "", "", "", "")

	for _, want := range []string{
		"## Memory (worthMemory)",
		"## Knowledge (worthKnowledge)",
		`"worthMemory": true/false`,
		`"worthKnowledge": true/false`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing required content %q", want)
		}
	}
	for _, banned := range []string{
		"## Shadow initiative prediction (worthInitiative)",
		"## Automation (worthAutomation)",
		"## Handoff (worthHandoff)",
		"## SOP (worthSOP)",
		"## Self-validation",
		`"worthInitiative":`,
		`"predictedUserAction"`,
		`"shadowExpectedResult"`,
		`"shadowReasoningHypotheses"`,
		`"matchedInitiativeId"`,
		`"observedUserAction"`,
		`"predictionAssessment"`,
		`"worthAutomation":`,
		`"worthHandoff":`,
		`"worthSOP":`,
		`"automationName"`,
		`"handoffName"`,
		`"sopName"`,
	} {
		if strings.Contains(got, banned) {
			t.Errorf("prompt should not contain disabled-feature content %q", banned)
		}
	}
}

func TestBuildTriagePrompt_ShadowOnly(t *testing.T) {
	got := buildTriagePrompt(triageFeatureFlags{Shadow: true}, "s", "L", "", "", "INITIATIVES", "", "", "")

	for _, want := range []string{
		"## Shadow initiative prediction (worthInitiative)",
		`"worthInitiative": true/false`,
		`"predictedUserAction"`,
		`"shadowExpectedResult"`,
		`"shadowReasoningHypotheses"`,
		"missing reasoning",
		"INITIATIVES",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("shadow-only prompt missing %q", want)
		}
	}
	for _, banned := range []string{
		"## Automation (worthAutomation)",
		"## Aligning shadow predictions with automations",
		"## Self-validation",
		`"worthAutomation":`,
		`"automationPrompt"`,
	} {
		if strings.Contains(got, banned) {
			t.Errorf("shadow-only prompt should not contain automation content %q", banned)
		}
	}
}

func TestBuildTriagePrompt_AutomationOnly(t *testing.T) {
	got := buildTriagePrompt(triageFeatureFlags{Automation: true}, "s", "L", "", "", "AUTOMATIONS", "", "", "")

	for _, want := range []string{
		"## Automation (worthAutomation)",
		"## Self-validation",
		`"worthAutomation": true/false`,
		`"automationPrompt"`,
		"AUTOMATIONS",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("automation-only prompt missing %q", want)
		}
	}
	for _, banned := range []string{
		"## Shadow initiative prediction (worthInitiative)",
		"## Aligning shadow predictions with automations",
		`"worthInitiative":`,
		`"predictedUserAction"`,
		`"matchedInitiativeId"`,
	} {
		if strings.Contains(got, banned) {
			t.Errorf("automation-only prompt should not contain shadow content %q", banned)
		}
	}
}

func TestBuildTriagePrompt_SectionsAreActuallySmallerWhenTrimmed(t *testing.T) {
	full := buildTriagePrompt(allFlagsOn(), "s", "L", "M", "K", "A", "H", "S", "")
	trimmed := buildTriagePrompt(triageFeatureFlags{Memory: true, Knowledge: true}, "s", "L", "M", "K", "", "", "", "")
	if len(trimmed) >= len(full) {
		t.Errorf("trimmed prompt (%d B) should be smaller than full (%d B)", len(trimmed), len(full))
	}
	// The Automation rule block alone is several KB — without it the
	// trim must save a meaningful chunk, not just a handful of bytes.
	if delta := len(full) - len(trimmed); delta < 5_000 {
		t.Errorf("trimmed prompt only saved %d B; expected >= 5 KB", delta)
	}
}

func TestBuildTriagePrompt_NumberingIsContiguous(t *testing.T) {
	// When only Memory + Knowledge are on, the numbered intro list must
	// renumber from 1 (no gaps referencing absent Automation/Handoff items).
	got := buildTriagePrompt(triageFeatureFlags{Memory: true, Knowledge: true}, "s", "L", "", "", "", "", "", "")
	if !strings.Contains(got, "1. Whether a MEMORY") {
		t.Errorf("expected MEMORY to be renumbered to item 1; got:\n%s", got)
	}
	if !strings.Contains(got, "2. Whether any genuine KNOWLEDGE") {
		t.Errorf("expected KNOWLEDGE to be renumbered to item 2; got:\n%s", got)
	}
}

func TestBuildDedupSection_EmptyWhenAllInputsEmpty(t *testing.T) {
	if got := buildDedupSection(allFlagsOn(), "", "", "", "", ""); got != "" {
		t.Errorf("expected empty dedup section; got %q", got)
	}
}

func TestBuildDedupSection_OnlyIncludesEnabledFeatureBullets(t *testing.T) {
	// Memory off, Knowledge on — the "Set worthMemory=false" bullet must
	// disappear and only the worthKnowledge bullet should remain.
	flags := triageFeatureFlags{Knowledge: true}
	got := buildDedupSection(flags, "", "some knowledge", "", "", "")
	if strings.Contains(got, "Set worthMemory=false") {
		t.Errorf("Memory bullet leaked through with Memory disabled:\n%s", got)
	}
	if !strings.Contains(got, "Set worthKnowledge=false") {
		t.Errorf("Knowledge bullet missing:\n%s", got)
	}
}

func TestApplyTriageFeatureFlags_ForcesDisabledToFalse(t *testing.T) {
	resp := &activityTriageResponse{
		WorthMemory:                true,
		WorthKnowledge:             true,
		WorthInitiative:            true,
		InitiativeTitle:            "next",
		InitiativeGoal:             "goal",
		PredictedUserAction:        "take action",
		InitiativeRationale:        "evidence",
		InitiativeConfidence:       0.8,
		MatchedInitiativeID:        "initiative-1",
		ObservedUserAction:         "took action",
		ObservedResult:             "finished",
		PredictionAssessment:       "matched",
		PredictionAssessmentReason: "same observable action",
		PredictionMatchConfidence:  0.9,
		WorthAutomation:            true,
		AutomationName:             "auto",
		AutomationDescription:      "d",
		AutomationPrompt:           "p",
		AutomationScheduleKind:     "cron",
		AutomationScheduleConfig:   json.RawMessage(`{"expr":"* * * * *"}`),
		AutomationScheduleEvidence: "every minute",
		WorthHandoff:               true,
		HandoffName:                "h",
		HandoffDescription:         "hd",
		HandoffPrompt:              "hp",
		WorthSOP:                   true,
		SOPAction:                  "create",
		SOPUpdateID:                "u",
		SOPName:                    "s",
		SOPDescription:             "sd",
		SOPTrigger:                 "st",
		SOPOutline:                 "so",
	}
	// Only Memory + Knowledge stay on — Automation/Handoff must
	// be wiped even though the (hypothetical) model returned them.
	applyTriageFeatureFlags(resp, triageFeatureFlags{Memory: true, Knowledge: true})

	if !resp.WorthMemory || !resp.WorthKnowledge {
		t.Errorf("enabled flags must remain true: %+v", resp)
	}
	if resp.WorthInitiative || resp.InitiativeTitle != "" || resp.PredictedUserAction != "" ||
		resp.MatchedInitiativeID != "" || resp.ObservedUserAction != "" || resp.PredictionAssessment != "" {
		t.Errorf("Initiative fields not cleared: %+v", resp)
	}
	if resp.WorthAutomation || resp.AutomationName != "" || resp.AutomationPrompt != "" || resp.AutomationScheduleKind != "" || resp.AutomationScheduleConfig != nil {
		t.Errorf("Automation fields not cleared: %+v", resp)
	}
	if resp.WorthHandoff || resp.HandoffName != "" || resp.HandoffPrompt != "" {
		t.Errorf("Handoff fields not cleared: %+v", resp)
	}
	if resp.WorthSOP || resp.SOPAction != "" || resp.SOPUpdateID != "" || resp.SOPName != "" || resp.SOPOutline != "" {
		t.Errorf("SOP fields not cleared: %+v", resp)
	}
}

func TestApplyTriageFeatureFlags_ShadowAndAutomationAreIndependent(t *testing.T) {
	shadowOnly := &activityTriageResponse{
		WorthInitiative:     true,
		PredictedUserAction: "draft the follow-up",
		WorthAutomation:     true,
		AutomationPrompt:    "draft it automatically",
	}
	applyTriageFeatureFlags(shadowOnly, triageFeatureFlags{Shadow: true})
	if !shadowOnly.WorthInitiative || shadowOnly.PredictedUserAction == "" {
		t.Fatalf("shadow fields should remain enabled: %+v", shadowOnly)
	}
	if shadowOnly.WorthAutomation || shadowOnly.AutomationPrompt != "" {
		t.Fatalf("automation fields should be cleared: %+v", shadowOnly)
	}

	automationOnly := &activityTriageResponse{
		WorthInitiative:     true,
		PredictedUserAction: "draft the follow-up",
		WorthAutomation:     true,
		AutomationPrompt:    "draft it automatically",
	}
	applyTriageFeatureFlags(automationOnly, triageFeatureFlags{Automation: true})
	if automationOnly.WorthInitiative || automationOnly.PredictedUserAction != "" {
		t.Fatalf("shadow fields should be cleared: %+v", automationOnly)
	}
	if !automationOnly.WorthAutomation || automationOnly.AutomationPrompt == "" {
		t.Fatalf("automation fields should remain enabled: %+v", automationOnly)
	}
}

func TestApplyTriageFeatureFlags_NilSafe(t *testing.T) {
	applyTriageFeatureFlags(nil, allFlagsOn())
}

// Regression: settings-skip must win over runner-not-ready, so users
// who disabled every triage output never trigger the rotator's
// cooldown+retry loop during agent startup. See the comment block in
// triagePreflight for full motivation.
func TestTriagePreflight_AllOffWinsOverRunnerNotReady(t *testing.T) {
	skip, err := triagePreflight(triageFeatureFlags{}, false)
	if !skip {
		t.Fatalf("expected skip=true when all flags off + runner not ready; got skip=%v err=%v", skip, err)
	}
	if err != nil {
		t.Fatalf("settings-skip path must not return an error; got %v", err)
	}
}

func TestTriagePreflight_RunnerNotReadyOnlyWhenWorkPending(t *testing.T) {
	skip, err := triagePreflight(triageFeatureFlags{Memory: true}, false)
	if skip {
		t.Fatalf("expected skip=false when at least one feature is on, got skip=true")
	}
	if err == nil || !strings.Contains(err.Error(), "runner not initialized") {
		t.Fatalf("expected runner-not-initialized error, got %v", err)
	}
}

func TestTriagePreflight_HappyPath(t *testing.T) {
	skip, err := triagePreflight(triageFeatureFlags{Knowledge: true}, true)
	if skip || err != nil {
		t.Fatalf("expected proceed (skip=false err=nil), got skip=%v err=%v", skip, err)
	}
}

// Regression for the "AI did not run for this axis" semantics. nil and
// boolPtr(false) carry different meanings in ActivityPipelineRunRow —
// nil = not evaluated (user disabled the feature), boolPtr(false) =
// evaluated and rejected by the model. Mixing these two corrupts the
// triage log diagnostics.
func TestBoolPtrIf_NilWhenCondFalse(t *testing.T) {
	if got := boolPtrIf(false, true); got != nil {
		t.Errorf("boolPtrIf(false, true) = %v, want nil (AI did not run for this axis)", *got)
	}
	if got := boolPtrIf(false, false); got != nil {
		t.Errorf("boolPtrIf(false, false) = %v, want nil", *got)
	}
}

func TestBoolPtrIf_PointerWhenCondTrue(t *testing.T) {
	if got := boolPtrIf(true, true); got == nil || *got != true {
		t.Errorf("boolPtrIf(true, true) = %v, want pointer to true", got)
	}
	if got := boolPtrIf(true, false); got == nil || *got != false {
		t.Errorf("boolPtrIf(true, false) = %v, want pointer to false", got)
	}
}

// TestEncodeTriageFlags_WireFormat locks the JSON contract consumed by the
// Activity Pipeline DevTools panel (frontend settings-page.tsx parses these
// exact key names to drive the per-feature flag chip row). Renaming a key,
// uppercasing it, or adding ",omitempty" would silently break the chip
// render on rows persisted by older builds — without this test, the
// only safeguard is a comment.
func TestEncodeTriageFlags_WireFormat(t *testing.T) {
	// Use a mix of true/false to verify both the key names AND that
	// every key is always emitted (no omitempty).
	encoded := encodeTriageFlags(triageFeatureFlags{
		Memory:     true,
		Knowledge:  false,
		Shadow:     true,
		Automation: false,
		Handoff:    true,
		SOP:        true,
	})

	var got map[string]bool
	if err := json.Unmarshal([]byte(encoded), &got); err != nil {
		t.Fatalf("encoded must parse as JSON object: %v\nencoded=%s", err, encoded)
	}

	want := map[string]bool{
		"memory":     true,
		"knowledge":  false,
		"shadow":     true,
		"automation": false,
		"handoff":    true,
		"sop":        true,
	}

	if len(got) != len(want) {
		t.Fatalf("expected all wire keys present regardless of values (no omitempty); got %d: %v", len(got), got)
	}
	for k, v := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("missing wire-format key %q (frontend depends on this exact name)", k)
			continue
		}
		if gv != v {
			t.Errorf("wire key %q: got %v, want %v", k, gv, v)
		}
	}
}

// Regression: errors that the API permanently rejects FOR CONTENT REASONS
// (safety filter, oversized prompt) must NOT feed the retry-with-cooldown
// loop, because the same prompt will be rejected again on every retry and
// burn quota for no gain. The motivating incident: a single group hit
// Anthropic's "considered high risk" gate 12 times across 10 hours
// before this fix went in.
//
// At the same time, invalid_request_errors caused by CODE / CONFIG bugs
// (schema violations, unsupported model features, parameter range errors)
// MUST stay on the transient path — fixing the bug recovers retry value,
// and the alternative (batch-dismiss every in-flight session for the
// duration of the bug) silently loses user data with no recovery path.
func TestIsPermanentTriageError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},

		// Content-bound — permanent (whitelisted).
		{
			name: "anthropic high risk safety rejection is permanent",
			err:  fmt.Errorf(`execute triage: anthropic: HTTP 400: {"error":{"type":"invalid_request_error","message":"The request was rejected because it was considered high risk"},"type":"error"}`),
			want: true,
		},
		{
			name: "anthropic oversized prompt (standard phrasing) is permanent",
			err:  fmt.Errorf(`execute triage: anthropic: HTTP 400: {"error":{"type":"invalid_request_error","message":"prompt is too long"}}`),
			want: true,
		},
		{
			name: "anthropic oversized prompt (alt phrasing) is permanent",
			err:  fmt.Errorf(`execute triage: anthropic: HTTP 400: {"error":{"type":"invalid_request_error","message":"input exceeds the maximum context length"}}`),
			want: true,
		},

		// Code / config bug — must stay transient so the fix can recover
		// these sessions instead of losing them to a terminal dismiss.
		{
			name: "anthropic schema error is transient (code bug, recoverable)",
			err:  fmt.Errorf(`execute triage: anthropic: HTTP 400: {"error":{"type":"invalid_request_error","message":"messages.0: required"}}`),
			want: false,
		},
		{
			name: "anthropic unsupported model feature is transient (config bug, recoverable)",
			err:  fmt.Errorf(`execute triage: anthropic: HTTP 400: {"error":{"type":"invalid_request_error","message":"this model does not support prefilled assistant messages"}}`),
			want: false,
		},
		{
			name: "bare invalid_request_error without content signal is transient",
			err:  fmt.Errorf("outer: %w", fmt.Errorf(`anthropic: invalid_request_error`)),
			want: false,
		},

		// Non-invalid_request_error families — always transient (covered
		// before the whitelist check).
		{
			name: "rate limit (429) is transient",
			err:  fmt.Errorf(`execute triage: anthropic: HTTP 429: {"error":{"type":"rate_limit_error","message":"slow down"}}`),
			want: false,
		},
		{
			name: "overloaded (529) is transient",
			err:  fmt.Errorf(`execute triage: anthropic: HTTP 529: {"error":{"type":"overloaded_error"}}`),
			want: false,
		},
		{
			name: "network timeout is transient",
			err:  fmt.Errorf("execute triage: context deadline exceeded"),
			want: false,
		},
		{
			name: "runner not initialized is transient",
			err:  errors.New("runner not initialized"),
			want: false,
		},
		{
			name: "generic api_error is transient",
			err:  fmt.Errorf(`execute triage: anthropic: HTTP 500: {"error":{"type":"api_error"}}`),
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPermanentTriageError(tc.err); got != tc.want {
				t.Errorf("isPermanentTriageError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestJoinWithAnd(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a and b"},
		{[]string{"a", "b", "c"}, "a, b, and c"},
		{[]string{"a", "b", "c", "d"}, "a, b, c, and d"},
	}
	for _, tc := range tests {
		if got := joinWithAnd(tc.in); got != tc.want {
			t.Errorf("joinWithAnd(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
