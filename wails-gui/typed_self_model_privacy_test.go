package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"wails-gui/internal/pii"
)

func addSelfModelPrivacyEvaluation(t *testing.T, app *App, eventEvidence bool) string {
	t.Helper()
	store := app.activity.store
	const sourceID = "self-privacy-source"
	insertTestSession(t, store, sourceID, "done", 100_000)
	input := taskEpisodeFixture("self-privacy-episode", 110_000, 120_000, TaskEpisodeStatusActive)
	input.InitialEvidence[0].SourceID = sourceID
	input.FrozenContext.Manifest = []TaskContextManifestEntry{{
		Ref: "session:" + sourceID, SourceKind: "activity_session", SourceID: sourceID,
		Section: TaskContextSectionActivity, ObservedAt: 100_000,
		Included: true, EvidenceClass: TaskContextEvidenceObservedUser,
		PrivacyDecision: TaskContextPrivacyRedact,
	}}
	if eventEvidence {
		onset := TaskOnsetEvidence{Ref: "event:110000:keyboard:0", Kind: "keyboard",
			SourceKind: "activity_event", SourceID: "0", OccurredAt: 110_000,
			AppKey: "com.example.test", AppName: "TestApp", Summary: "Drafted a progress update."}
		payload, err := json.Marshal(onset)
		if err != nil {
			t.Fatal(err)
		}
		input.InitialEvidence[0].SourceKind = onset.SourceKind
		input.InitialEvidence[0].SourceID = onset.SourceID
		input.InitialEvidence[0].PayloadJSON = string(payload)
	}
	episode, _, err := store.CreateTaskEpisode(input)
	if err != nil {
		t.Fatal(err)
	}
	// The shadow run and its evaluation the removed shadow agent wrote.
	insertLegacyRow(t, store, "shadow_runs", map[string]any{
		"id": "self-privacy-run", "task_episode_id": episode.ID, "created_at": 130_000,
	})
	insertLegacyRow(t, store, "shadow_run_observations", map[string]any{
		"id": "self-privacy-observation", "shadow_run_id": "self-privacy-run", "created_at": 140_001,
	})
	insertLegacyRow(t, store, "shadow_run_evaluations", map[string]any{
		"id": "self-privacy-evaluation", "shadow_run_id": "self-privacy-run",
		"observation_id": "self-privacy-observation", "created_at": 150_000,
	})
	return "self-privacy-evaluation"
}

func TestTypedSelfInjectionRechecksEvaluationSourcePrivacy(t *testing.T) {
	for _, fixture := range []struct {
		name          string
		eventEvidence bool
		retained      bool
		protect       func(*App)
	}{
		{name: "app sensitive after materialization", protect: func(*App) {
			pii.SetUserSensitiveApps(map[string]string{"com.example.test": "TestApp"})
		}},
		{name: "retained source", retained: true, protect: func(*App) {
			pii.SetUserSensitiveApps(map[string]string{"com.example.test": "TestApp"})
		}},
		{name: "retained onset event", eventEvidence: true, retained: true, protect: func(*App) {
			pii.SetUserSensitiveApps(map[string]string{"com.example.test": "TestApp"})
		}},
		{name: "observation paused", protect: func(app *App) {
			app.saveObservationPauses([]observationPause{{Start: 100_000, End: 160_000}})
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			app := newMemoryInjectionPrivacyFixture(t)
			evaluationID := addSelfModelPrivacyEvaluation(t, app, fixture.eventEvidence)
			const statement = "The user prefers concise progress updates from private-source-work."
			entry, _, err := app.activity.store.AddSelfModelEntry(SelfModelEntry{
				Kind: selfModelKindPreference, Scope: "progress updates", Statement: statement,
				Status: selfModelStatusActive, Confidence: 0.9,
				ReasoningStatus: "source_backed_shadow_task_evaluation",
				Evidence: []SelfModelEvidence{{SourceKind: "shadow_task_evaluation", SourceID: evaluationID,
					Confidence: 0.9, OccurredAt: 150_000}}, CreatedAt: 150_001,
			})
			if err != nil {
				t.Fatal(err)
			}
			if fixture.retained {
				if err := app.activity.store.deleteRetainedSession("self-privacy-source"); err != nil {
					t.Fatal(err)
				}
			}
			const prompt = "Help with this task."
			if got := app.injectMemoryContext(context.Background(), prompt, "progress updates"); !strings.Contains(got, statement) {
				t.Fatalf("readable Self evidence did not enter context: %q", got)
			}
			fixture.protect(app)
			if got := app.injectMemoryContext(context.Background(), prompt, "progress updates"); got != prompt {
				t.Fatalf("protected Self evidence entered model context: %q", got)
			}
			if raw, err := app.activity.store.GetSelfModelEntryByID(entry.ID); err != nil || raw.Statement != statement {
				t.Fatalf("privacy filtering rewrote immutable Self history: %+v %v", raw, err)
			}
		})
	}
}

func TestTypedSelfInjectionWithholdsUnresolvedEvaluationEvidence(t *testing.T) {
	app := newMemoryInjectionPrivacyFixture(t)
	_, _, err := app.activity.store.AddSelfModelEntry(SelfModelEntry{
		Kind: selfModelKindPreference, Scope: "progress updates", Statement: "Unresolved private-source-work",
		Status: selfModelStatusActive, Confidence: 0.9, ReasoningStatus: "source_backed_shadow_task_evaluation",
		Evidence: []SelfModelEvidence{{SourceKind: "shadow_task_evaluation", SourceID: "missing-evaluation", Confidence: 0.9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const prompt = "Help with this task."
	if got := app.injectMemoryContext(context.Background(), prompt, "progress updates"); got != prompt {
		t.Fatalf("unresolved Self evidence entered model context: %q", got)
	}
}
