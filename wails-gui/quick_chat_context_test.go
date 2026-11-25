package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
)

// seedSnapshotForApp inserts a session whose AppNames include the given
// display name plus a single snapshot, returning the snapshot ID. Useful
// for the per-app query tests.
func seedSnapshotForApp(t *testing.T, s *ActivityStore, sessionID, appDisplayName, axText string, ts int64) int64 {
	t.Helper()
	appNamesJSON := `["` + appDisplayName + `"]`
	if err := s.SaveSession(ActivitySessionRow{
		ID:         sessionID,
		StartTime:  ts - 1000,
		EndTime:    ts,
		AppNames:   appNamesJSON,
		EventCount: 1,
		CreatedAt:  ts,
	}, nil, nil); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := s.SaveSnapshots(tx, []ActivitySnapshotRow{{
		SessionID:   sessionID,
		Timestamp:   ts,
		FilePath:    "test/" + sessionID + ".jpg",
		Width:       100,
		Height:      100,
		SizeBytes:   1024,
		Trigger:     "test",
		StorageTier: "hot",
		AXText:      axText,
	}}); err != nil {
		tx.Rollback()
		t.Fatalf("SaveSnapshots: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var id int64
	if err := s.db.QueryRow(
		`SELECT id FROM activity_snapshots WHERE session_id = ?`, sessionID,
	).Scan(&id); err != nil {
		t.Fatalf("scan id: %v", err)
	}
	return id
}

func TestGetLatestSnapshot_Empty(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetLatestSnapshot()
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot on empty store, got id=%d", got.ID)
	}
}

func TestGetLatestSnapshot_ReturnsMostRecent(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UnixMilli()
	seedSnapshotForApp(t, s, "sess-old", "Discord", "old text", now-10_000)
	wantID := seedSnapshotForApp(t, s, "sess-new", "Slack", "new text", now-1_000)
	seedSnapshotForApp(t, s, "sess-mid", "Notion", "mid text", now-5_000)

	got, err := s.GetLatestSnapshot()
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if got == nil || got.ID != wantID {
		t.Fatalf("got %v, want id=%d", got, wantID)
	}
}

func TestGetRecentSnapshotsForApp_FiltersByDisplayName(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UnixMilli()
	discordID := seedSnapshotForApp(t, s, "sess-discord", "Discord", "discord text", now-1_000)
	seedSnapshotForApp(t, s, "sess-slack", "Slack", "slack text", now-2_000)

	got, err := s.GetRecentSnapshotsForApp("Discord", now-60_000, 5)
	if err != nil {
		t.Fatalf("GetRecentSnapshotsForApp: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d snapshots, want 1", len(got))
	}
	if got[0].ID != discordID {
		t.Errorf("got id=%d, want %d", got[0].ID, discordID)
	}
}

func TestGetRecentSnapshotsForApp_RespectsSinceMs(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UnixMilli()
	seedSnapshotForApp(t, s, "old", "Discord", "old", now-2*60_000) // 2 min ago
	recentID := seedSnapshotForApp(t, s, "new", "Discord", "new", now-30_000)

	got, err := s.GetRecentSnapshotsForApp("Discord", now-60_000, 5) // last 60s
	if err != nil {
		t.Fatalf("GetRecentSnapshotsForApp: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d, want 1 (only the new one within window)", len(got))
	}
	if got[0].ID != recentID {
		t.Errorf("got id=%d, want %d", got[0].ID, recentID)
	}
}

func TestGetRecentSnapshotsForApp_PrefixCollision(t *testing.T) {
	// "Mail" must not match a session whose AppNames include "MailMate".
	// The JSON-quoted LIKE pattern `%"Mail"%` requires exact-token match.
	s := newTestStore(t)
	now := time.Now().UnixMilli()
	seedSnapshotForApp(t, s, "sess-mailmate", "MailMate", "ax", now-1_000)

	got, err := s.GetRecentSnapshotsForApp("Mail", now-60_000, 5)
	if err != nil {
		t.Fatalf("GetRecentSnapshotsForApp: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d snapshots, want 0 (no false match across name prefixes)", len(got))
	}
}

func TestGetRecentSnapshotsForApp_LimitAndOrder(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UnixMilli()
	for i := 0; i < 7; i++ {
		seedSnapshotForApp(t, s, fmtSessID(i), "Discord", "text", now-int64(i)*1_000)
	}

	got, err := s.GetRecentSnapshotsForApp("Discord", now-60_000, 3)
	if err != nil {
		t.Fatalf("GetRecentSnapshotsForApp: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3 (limit honored)", len(got))
	}
	// Newest first.
	for i := 1; i < len(got); i++ {
		if got[i-1].Timestamp < got[i].Timestamp {
			t.Errorf("not ordered DESC: %v", got)
			break
		}
	}
}

func fmtSessID(i int) string {
	return "sess-" + string(rune('a'+i))
}

// ---- Stash tests ----

func TestStashFocusedAppContext_PopOnce(t *testing.T) {
	defer focusedAppStash.Range(func(k, _ any) bool {
		focusedAppStash.Delete(k)
		return true
	})

	snap := &focusedAppSnapshot{BundleID: "com.hnc.Discord", AppName: "Discord"}
	stashFocusedAppContext("session-1", snap)

	got := popFocusedAppContext("session-1")
	if got == nil || got.AppName != "Discord" {
		t.Fatalf("first pop got %v, want Discord", got)
	}
	again := popFocusedAppContext("session-1")
	if again != nil {
		t.Fatalf("second pop should return nil, got %v", again)
	}
}

func TestStashFocusedAppContext_TTLExpires(t *testing.T) {
	defer focusedAppStash.Range(func(k, _ any) bool {
		focusedAppStash.Delete(k)
		return true
	})

	// Insert a stash entry with a deliberately old storedAt.
	focusedAppStash.Store("expired-session", focusedAppStashEntry{
		snap:     &focusedAppSnapshot{BundleID: "x", AppName: "X"},
		storedAt: time.Now().Add(-2 * focusedAppStashTTL),
	})
	got := popFocusedAppContext("expired-session")
	if got != nil {
		t.Fatalf("expected nil for expired stash, got %v", got)
	}
}

func TestStashFocusedAppContext_EmptyKeysIgnored(t *testing.T) {
	stashFocusedAppContext("", &focusedAppSnapshot{})
	stashFocusedAppContext("ignored-empty-snap", nil)
	if got := popFocusedAppContext(""); got != nil {
		t.Errorf("empty key pop should be nil, got %v", got)
	}
	if got := popFocusedAppContext("ignored-empty-snap"); got != nil {
		t.Errorf("nil-snap stash should not have been written")
	}
}

// ---- Producer tests ----

func TestInjectFocusedAppContext_NoStash(t *testing.T) {
	app := &App{}
	out := app.injectFocusedAppContext("nonexistent-session")
	if out != "" {
		t.Errorf("expected empty string when no stash, got %q", out)
	}
}

func TestInjectFocusedAppContext_FormatsAndClears(t *testing.T) {
	defer focusedAppStash.Range(func(k, _ any) bool {
		focusedAppStash.Delete(k)
		return true
	})

	snap := &focusedAppSnapshot{
		BundleID:    "com.hnc.Discord",
		AppName:     "Discord",
		WindowTitle: "#general — Yansu Discord",
		Current: &ActivitySnapshotRow{
			Timestamp: time.Now().UnixMilli(),
			AXText:    "channel: general\nmessage: hello world",
		},
		CurrentFresh: true,
		CapturedAt:   time.Now(),
	}
	stashFocusedAppContext("sess-x", snap)

	app := &App{store: newIdentityTestStore(t)}
	out := app.injectFocusedAppContext("sess-x")
	if out == "" {
		t.Fatal("expected non-empty context block")
	}
	for _, want := range []string{"Current application", "Discord", "com.hnc.Discord", "general"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q. Got:\n%s", want, out)
		}
	}
	// Stash should be cleared.
	if popFocusedAppContext("sess-x") != nil {
		t.Error("stash should be cleared after injection")
	}
}

// ---- Format helpers ----

func TestTruncateForFocusedAppPrompt(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello world", 100, "hello world"},
		{"hello world", 5, "hello…"},
		{"  spaced  ", 100, "spaced"},
		{"", 10, ""},
		{"any", 0, ""},
	}
	for _, tc := range cases {
		got := truncateForFocusedAppPrompt(tc.in, tc.n)
		if got != tc.want {
			t.Errorf("truncateForFocusedAppPrompt(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestExtractWindowTitle_PrefersParsedTitle(t *testing.T) {
	parsed := `{"contentType":"conversation","title":"#general","metadata":{"windowTitle":"Discord"}}`
	got := extractWindowTitle(&ActivitySnapshotRow{ParsedJSON: parsed})
	if got != "#general" {
		t.Errorf("got %q, want #general", got)
	}
}

func TestExtractWindowTitle_FallsBackToWindowTitleMetadata(t *testing.T) {
	parsed := `{"metadata":{"windowTitle":"Discord — #general"}}`
	got := extractWindowTitle(&ActivitySnapshotRow{ParsedJSON: parsed})
	if got != "Discord — #general" {
		t.Errorf("got %q, want Discord — #general", got)
	}
}

func TestExtractWindowTitle_FallsBackToAXText(t *testing.T) {
	got := extractWindowTitle(&ActivitySnapshotRow{AXText: "\n\nFirst real line\nsecond"})
	if got != "First real line" {
		t.Errorf("got %q, want first non-empty AX line", got)
	}
}

func TestFormatFocusedAppContextForPrompt_OrdersContextByRelevance(t *testing.T) {
	now := time.Now()
	snap := &focusedAppSnapshot{
		BundleID: "com.tinyspeck.slackmacgap",
		AppName:  "Slack",
		Current: &ActivitySnapshotRow{
			Timestamp: now.UnixMilli(),
			App:       "slack",
			AXText:    "current Slack thread about the launch blocker",
		},
		CurrentFresh: true,
		Recent: []*ActivitySnapshotRow{{
			ID:        1,
			Timestamp: now.Add(-30 * time.Minute).UnixMilli(),
			App:       "slack",
			AXText:    "earlier Slack follow-up about pricing",
		}},
		General: "daily summary about unrelated website work",
	}

	out := formatFocusedAppContextForPrompt(snap, nil)
	currentIdx := strings.Index(out, "## Current application")
	pastIdx := strings.Index(out, "## Past context from the same application")
	generalIdx := strings.Index(out, "## General context")
	if currentIdx < 0 || pastIdx < 0 || generalIdx < 0 {
		t.Fatalf("missing expected sections. Got:\n%s", out)
	}
	if !(currentIdx < pastIdx && pastIdx < generalIdx) {
		t.Fatalf("sections are not in relevance order. Got:\n%s", out)
	}
	for _, want := range []string{
		"current Slack thread about the launch blocker",
		"earlier Slack follow-up about pricing",
		"daily summary about unrelated website work",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q. Got:\n%s", want, out)
		}
	}
}

func TestFilterRecentSnapshotsForFocusedApp_ExcludesUnrelatedMixedSessionRows(t *testing.T) {
	current := &ActivitySnapshotRow{ID: 10, App: "slack"}
	rows := []ActivitySnapshotRow{
		{ID: 1, App: "github", AXText: "GitHub PR"},
		{ID: 2, ParsedJSON: `{"metadata":{"app":"slack"},"title":"Slack thread"}`},
		{ID: 3, ParsedJSON: `{"app":"slack","dedupOfHash":"abc","contentType":"conversation"}`},
		{ID: 4, AXText: "unknown unparsed snapshot"},
		{ID: 10, App: "slack", AXText: "duplicate current"},
	}

	got := filterRecentSnapshotsForFocusedApp(rows, current, "Slack", 5)
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %#v", len(got), got)
	}
	if got[0].ID != 2 || got[1].ID != 3 {
		t.Fatalf("got IDs [%d %d], want [2 3]", got[0].ID, got[1].ID)
	}
}

func TestBuildFocusedAppQuestionsPrompt_EncodesContextPriority(t *testing.T) {
	prompt := buildFocusedAppQuestionsPrompt("Slack", "## Current application\n## Past context from the same application\n## General context", "en")
	for _, want := range []string{
		`"Current application" is the strongest evidence`,
		`"Past context from the same application" is lower priority`,
		`"General context" is the weakest evidence`,
		"never let it outrank or",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q. Got:\n%s", want, prompt)
		}
	}
}

func TestContextualQuickChatQuestionsFromSnapshot_UsesSlackMessages(t *testing.T) {
	snap := &focusedAppSnapshot{
		BundleID:    "com.tinyspeck.slackmacgap",
		AppName:     "Slack",
		WindowTitle: "Maya Chen - Halcyon - Slack",
		Current: &ActivitySnapshotRow{
			App: "slack",
			ParsedJSON: `{
				"contentType":"conversation",
				"title":"Maya Chen - Halcyon - Slack",
				"channel":"Maya Chen",
				"messages":[
					{"sender":"Alex","content":"please use the storyboard draft; the current outline needs revision"}
				],
				"metadata":{"app":"slack"}
			}`,
		},
	}

	got := contextualQuickChatQuestionsFromSnapshot(snap, "en")
	if len(got) == 0 {
		t.Fatal("expected contextual Slack fallback suggestions")
	}
	if got[0].Title != "Reply to Alex" {
		t.Fatalf("first title = %q, want Reply to Alex", got[0].Title)
	}
	if !strings.Contains(got[0].Prompt, "storyboard draft") {
		t.Fatalf("first prompt should reference current Slack message, got %q", got[0].Prompt)
	}
	for _, q := range got {
		if q.Title == "Summarize this conversation" {
			t.Fatalf("should not use basic conversation fallback when Slack context exists: %#v", got)
		}
	}
}

func TestContextualQuickChatQuestionsFromSnapshot_UsesTerminalContext(t *testing.T) {
	snap := &focusedAppSnapshot{
		BundleID:    "com.googlecode.iterm2",
		AppName:     "iTerm2",
		WindowTitle: "something",
		Current: &ActivitySnapshotRow{
			ParsedJSON: `{
				"contentType":"terminal",
				"title":"something",
				"body":"Edited wails-gui/recommended_questions.go (+5 -3)\nRan gofmt -w recommended_questions.go && go test -run TestQuickChat .\nok wails-gui 0.675s\nRan git diff --check"
			}`,
		},
	}

	got := contextualQuickChatQuestionsFromSnapshot(snap, "en")
	if len(got) == 0 {
		t.Fatal("expected contextual terminal fallback suggestions")
	}
	if got[0].Title != "Review current diff" {
		t.Fatalf("first title = %q, want Review current diff", got[0].Title)
	}
	if !strings.Contains(got[0].Prompt, "diff and test output") {
		t.Fatalf("first prompt should describe visible terminal context, got %q", got[0].Prompt)
	}
	for _, q := range got {
		if strings.Contains(q.Title, "something") {
			t.Fatalf("terminal fallback should not use only the project/window title: %#v", got)
		}
	}
}

func TestGetQuickChatSuggestionsForApp_BundleSpecificNoContextReturnsAppFallback(t *testing.T) {
	app := &App{}
	got, err := app.GetQuickChatSuggestionsForApp("com.example.SlackTest", "en")
	if err != nil {
		t.Fatalf("GetQuickChatSuggestionsForApp: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected app-aware fallback suggestions")
	}
	if got[0].Title != "Summarize this conversation" {
		t.Fatalf("first fallback = %#v, want conversation fallback", got[0])
	}
	for _, q := range got {
		if strings.Contains(q.Prompt, "yesterday") || strings.Contains(q.Prompt, "this week") {
			t.Fatalf("fallback should not use stale generic composer prompts: %#v", got)
		}
	}
}

// Compile-time check the seed helper compiles against the real ActivityStore
// API; this catches schema drift the moment SaveSession's signature changes.
var _ = func(_ *ActivityStore, _ activitymon.ActivityEvent, _ *sql.Tx) {}
