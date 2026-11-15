package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wails-gui/internal/pii"
)

func TestSearchMemoryAppliesReadTimePrivacy(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	store, err := NewMemoryStore(filepath.Join(os.Getenv("HOME"), "memory-fixture"), MemoryStoreConfig{Provider: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app.memoryStore = store
	priorApps := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(priorApps) })
	pii.SetUserSensitiveApps(nil)
	insertTestSession(t, app.activity.store, "private-session", "dismissed", 1000)
	if _, err := app.activity.store.db.Exec(`UPDATE activity_events SET data = '{"windowTitle":"Bank - Incognito"}' WHERE session_id = 'private-session'`); err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]string{
		"safe.md":      "# Public note\n\nprivacyneedle contact customer@example.com\n",
		"sensitive.md": "# Sensitive note\n\n- **Apps**: Diary\n\n" + strings.Repeat("Unrelated history.\n", 100) + "\nprivacyneedle historical secret\n",
		"private.md":   "# Private note\n\n- **Activity Session**: private-session\n\nprivacyneedle private browsing\n",
		"corrected.md": "# Corrected note\n\nprivacyneedle superseded observation\n",
	}
	// BM25 gives a term present in nearly every indexed chunk negligible
	// relevance. An ordinary mixed corpus keeps the privacy hits above the
	// production minimum score without overriding ranking in this test.
	for i := range 20 {
		fixtures[fmt.Sprintf("unrelated-%02d.md", i)] = "# Cooking notes\n\nRoast vegetables and boil pasta for dinner.\n"
	}
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(store.baseDir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Sync("privacy-fixture"); err != nil {
		t.Fatal(err)
	}
	baseline, err := store.Search(context.Background(), "privacyneedle", 20)
	if err != nil || len(baseline) < 4 {
		t.Fatalf("fixture did not index all sources: results=%v err=%v", paths(baseline), err)
	}
	// Privacy changed after indexing; neither the stale index nor a chunk
	// missing its header is authority to release the source now.
	pii.SetUserSensitiveApps(map[string]string{"com.example.diary": "Diary"})
	seedLegacyLearningRun(t, app.activity.store, "run:one", []legacyLearningCandidate{{RevisionID: "revision:one", ProjectionKey: "projection:one", TargetKind: correctedLearningTargetMemory, Statement: "superseded inference"}}, 1000)
	seedLegacyLearningMaterialization(t, app.activity.store, "revision:one", correctedLearningTargetMemory, "corrected", 1100)
	addLearningCorrection(t, app.activity.store, AgencyLearningCorrectionRecord{
		IdempotencyKey: "memory-read-correction", CapabilityKey: "coding.review",
		LearningRevisionID: "revision:one", OriginalStatement: "superseded observation",
		Correction: "Do not use this inference", CreatedAt: 1200,
	})
	results, err := app.SearchMemory("privacyneedle", 20)
	if err != nil || len(results) != 1 || results[0].Path != "safe.md" {
		t.Fatalf("search leaked protected or corrected sources: results=%v err=%v", paths(results), err)
	}
	if strings.Contains(results[0].Snippet, "customer@example.com") || !strings.Contains(results[0].Snippet, "[...]") || strings.Contains(results[0].Snippet, "[REDACTED:") {
		t.Fatalf("search returned unredacted snippet: %q", results[0].Snippet)
	}
	app.closedEyes.enabled.Store(true)
	results, err = app.SearchMemory("privacyneedle", 20)
	if err != nil || len(results) != 0 {
		t.Fatalf("closed-eyes search leaked data: results=%v err=%v", paths(results), err)
	}
	app.closedEyes.enabled.Store(false)
	if err := app.activity.store.Close(); err != nil {
		t.Fatal(err)
	}
	results, err = app.SearchMemory("privacyneedle", 20)
	if err == nil || len(results) != 0 {
		t.Fatalf("privacy lookup failure must fail closed: results=%v err=%v", paths(results), err)
	}
}

func TestMemorySearchRedactsBeforeSnippetLimit(t *testing.T) {
	store := setupFixtureStore(t)
	heading := "# snippetboundary\n\n"
	content := heading + strings.Repeat("x", memorySnippetMaxChars-len(heading)-25) + " " + activityPrivacySecret + " tail\n"
	if err := os.WriteFile(filepath.Join(store.baseDir, "snippet-boundary.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	results, err := store.Search(context.Background(), "snippetboundary", 20)
	if err != nil || len(results) != 1 {
		t.Fatalf("snippet fixture missing: paths=%v err=%v", paths(results), err)
	}
	if strings.Contains(results[0].Snippet, "sk-proj-") || !strings.Contains(results[0].Snippet, "[REDACTED:") {
		t.Fatalf("snippet cap split a secret before redaction: %q", results[0].Snippet)
	}
}
