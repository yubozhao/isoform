package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/storage"
	"yansu-agent/agent"
)

func todaySendTestApp(t *testing.T) (*App, string, TodayChatContextRef) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "tcs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	app := mainChatTestApp(t)
	main, err := app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveMemoryEntry("launch", "# Launch plan\nThe release review is on Friday."); err != nil {
		t.Fatal(err)
	}
	return app, main.LocalID, TodayChatContextRef{SourceKind: "memory", SourceID: "launch"}
}

func TestPrepareTodayChatTurnRejectsMissingSourcesAndOtherChats(t *testing.T) {
	app, tag, ref := todaySendTestApp(t)
	for _, tc := range []struct {
		tag string
		ref TodayChatContextRef
	}{
		{"other-chat", ref},
		{tag, TodayChatContextRef{SourceKind: "memory", SourceID: "missing"}},
	} {
		if _, err := app.PrepareTodayChatTurn(tc.tag, "A private card title", tc.ref); err == nil {
			t.Fatal("unsafe draft accepted")
		}
	}
	page, err := app.store.LoadStreamItemsLatest(context.Background(), tag, 100)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("preflight wrote chat history: %+v, %v", page, err)
	}
}

func TestPrepareTodayChatTurnRedactsBeforeUserHistory(t *testing.T) {
	app, tag, ref := todaySendTestApp(t)
	prompt := "Help with card 4111 1111 1111 1111 in the launch plan."
	got, err := app.PrepareTodayChatTurn(tag, prompt, ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "4111") || !strings.Contains(got, "REDACTED") {
		t.Fatalf("unredacted generated draft: %q", got)
	}
}

func TestPrepareTodayChatTurnRefreshesCachedTitleBeforeUserHistory(t *testing.T) {
	for _, kind := range []string{"memory", "knowledge"} {
		t.Run(kind, func(t *testing.T) {
			app, tag, _ := todaySendTestApp(t)
			const current = "Launch plan"
			const stale = "Atlas stale private title"
			if kind == "knowledge" {
				dir, err := knowledgeDir()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "launch.md"), []byte("# "+current+"\nCurrent source."), 0600); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now()
			brief := buildMobileBriefV1(mobileBriefInput{
				Slot: mobileBriefSlotMorning, AsOf: now, WindowStart: now.Add(-24 * time.Hour),
				Learned: []mobileBriefCandidate{{SourceKind: kind, SourceID: "launch", Title: stale, At: now.Add(-time.Hour)}},
			})
			dir, err := mobileBriefStateDir()
			if err != nil {
				t.Fatal(err)
			}
			if err := newMobileBriefStore(dir).save(brief, now); err != nil {
				t.Fatal(err)
			}
			ref := TodayChatContextRef{SourceKind: "brief_item", BriefID: brief.BriefID, SourceID: brief.Sections.Learned[0].ID}
			prompt := "Please help me with \"" + stale + "\", and focus on Friday."
			got, err := app.PrepareTodayChatTurn(tag, prompt, ref)
			if err != nil {
				t.Fatal(err)
			}
			if want := "Please help me with \"" + current + "\", and focus on Friday."; got != want {
				t.Fatalf("prepared = %q, want %q", got, want)
			}
			// The refreshed draft is safe user-visible text on its own; the
			// private evidence bundle is never part of this history row.
			if _, err := app.store.AppendStreamItem(context.Background(), tag, storage.StreamItemRow{ID: "prepared", Kind: "user", Content: got}); err != nil {
				t.Fatal(err)
			}
			page, err := app.store.LoadStreamItemsLatest(context.Background(), tag, 10)
			if err != nil || len(page.Items) != 1 || strings.Contains(page.Items[0].Content, stale) {
				t.Fatalf("stale title persisted: %+v, %v", page, err)
			}
			const rewritten = "I rewrote this request entirely; focus on Friday."
			if got, err := app.PrepareTodayChatTurn(tag, rewritten, ref); err != nil || got != rewritten {
				t.Fatalf("unrelated wording changed: %q, %v", got, err)
			}
		})
	}
}

func TestTodayChatSendInjectsOnceWithoutPersistingSourceText(t *testing.T) {
	app, tag, ref := todaySendTestApp(t)
	runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 1)}
	app.runner = runner
	app.tagCancels = map[string]context.CancelFunc{}
	app.sessionTempDirs = map[string]string{}
	app.streams = map[string]context.CancelFunc{}
	app.runIDByTag = map[string]string{}
	app.pendingTraceIDs = map[string]string{}
	const visible = "Help with the launch plan."
	if _, err := app.SendPromptWithTodayContext(visible, "", "m", "", tag, nil, nil, false, false, "en", "", StreamItemData{ID: "today-user", Kind: "user"}, ref); err != nil {
		t.Fatal(err)
	}
	var input *agent.TaskInput
	select {
	case input = <-runner.inputs:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not receive the Today turn")
	}
	for i := 0; i < 2; i++ {
		prompt, _ := input.PromptFunc()
		if strings.Count(prompt, "<today-item-context>") != 1 || !strings.Contains(prompt, "release review is on Friday") || !strings.HasSuffix(prompt, visible) {
			t.Fatalf("missing contextual prompt: %q", prompt)
		}
	}
	waitForTagIdle(t, app, tag)
	page, err := app.store.LoadStreamItemsLatest(context.Background(), tag, 100)
	if err != nil {
		t.Fatal(err)
	}
	counts := 0
	for _, row := range page.Items {
		if strings.Contains(row.Content, "release review") {
			t.Fatal("source text leaked into history")
		}
		if row.ToolName == "today_context" {
			counts++
			if row.Kind != "context_status" {
				t.Fatalf("context receipt kind = %q", row.Kind)
			}
			if _, visibleOnPhone := mobileChatItemFromRow(row); visibleOnPhone {
				t.Fatal("desktop context metadata leaked into the phone's chat")
			}
			var got TodayChatContextCounts
			if json.Unmarshal([]byte(row.Content), &got) != nil || got.Memories != 1 {
				t.Fatalf("counts = %s", row.Content)
			}
			if !strings.Contains(row.ToolInput, "today-user") {
				t.Fatal("receipt is not tied to its user turn")
			}
			// It names what the message was attached to, the draft's title.
			var receipt map[string]string
			if json.Unmarshal([]byte(row.ToolInput), &receipt) != nil || receipt["about"] != "Launch plan" || receipt["aboutKind"] != ref.SourceKind {
				t.Fatalf("receipt = %s", row.ToolInput)
			}
		}
	}
	if counts != 1 {
		t.Fatalf("count receipts = %d, want one", counts)
	}
	history := formatConversationHistory(page.Items)
	if strings.Contains(history, "memories") || strings.Contains(history, "release review") {
		t.Fatalf("display metadata entered prompt history: %s", history)
	}
}

// Limit the selected SQLite pool to one connection and occupy it to stop a
// real source lookup. WaitCount proves it is queued before Stop is pressed.
func blockTodayLookup(t *testing.T, db *sql.DB) (func(), func()) {
	t.Helper()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	before := db.Stats().WaitCount
	wait := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if db.Stats().WaitCount > before {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("Today source lookup did not reach the blocked database")
	}
	return wait, func() { _ = conn.Close() }
}

func TestPrepareTodayChatTurnStopCancelsSourceLookup(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	main, err := app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeTodayContextMemory(t, "launch", "# Launch plan\nRelease Friday.")
	wait, release := blockTodayLookup(t, app.activity.store.db)
	done := make(chan error, 1)
	go func() {
		_, err := app.PrepareTodayChatTurn(main.LocalID, "Help with launch", TodayChatContextRef{SourceKind: "memory", SourceID: "launch"})
		done <- err
	}()
	wait()
	app.CancelByTag(main.LocalID)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stopped preparation returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopped preparation did not finish")
	}
	waitForTagIdle(t, app, main.LocalID)
	release()
}

func TestTodayChatSendStopCancelsActivitySourceLookupBeforeConnectionRelease(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	main, err := app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeTodayContextMemory(t, "launch", "# Launch plan\nRelease Friday.")
	ref := TodayChatContextRef{SourceKind: "memory", SourceID: "launch"}
	prompt, err := app.PrepareTodayChatTurn(main.LocalID, "Help with launch", ref)
	if err != nil {
		t.Fatal(err)
	}
	runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 1)}
	app.runner = runner
	wait, release := blockTodayLookup(t, app.activity.store.db)
	done := make(chan error, 1)
	go func() {
		_, err := app.SendPromptWithTodayContext(prompt, "", "m", "", main.LocalID, nil, nil, false, false, "en", "", StreamItemData{ID: "today-user", Kind: "user"}, ref)
		done <- err
	}()
	wait()
	app.CancelByTag(main.LocalID)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stopped activity lookup returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel the activity lookup while its connection remained occupied")
	}
	waitForTagIdle(t, app, main.LocalID)
	select {
	case <-runner.inputs:
		t.Fatal("model started after Stop during Today activity lookup")
	default:
	}
	release()
}

func TestTodayChatSourceStoreReadsCancelWhileConnectionIsOccupied(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	store := app.activity.store
	purposeStore, err := app.purposeProductStore()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		db   *sql.DB
		read func(context.Context) error
	}{
		{"corrections", store.db, func(ctx context.Context) error {
			_, err := store.ListCorrectedAgencyLearningTargetIDsContext(ctx)
			return err
		}},
		{"purpose", store.db, func(ctx context.Context) error { _, err := purposeStore.ListDetailsAllContext(ctx); return err }},
		{"self", store.db, func(ctx context.Context) error { _, err := store.listUsableSelfModelEntriesContext(ctx); return err }},
		{"task-onsets", store.reader(), func(ctx context.Context) error { _, err := store.taskEpisodeRepeatedOnsetSetContext(ctx); return err }},
		{"activities", store.reader(), func(ctx context.Context) error { _, err := store.GetActivitiesInRangeContext(ctx, 0, 1); return err }},
		{"sessions", store.reader(), func(ctx context.Context) error {
			_, err := store.GetSessionsByIDsContext(ctx, []string{"source"})
			return err
		}},
		{"timeline-run", store.reader(), func(ctx context.Context) error { _, err := store.GetTimelineRunByIDContext(ctx, "source"); return err }},
		{"files", store.db, func(ctx context.Context) error { _, err := store.ListFileActivityContext(ctx, 0, 1); return err }},
		{"browser", store.db, func(ctx context.Context) error { _, err := store.ListBrowserVisitsContext(ctx, 0, 1); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wait, release := blockTodayLookup(t, tc.db)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() { done <- tc.read(ctx) }()
			wait()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled source read returned %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("source read waited for the occupied connection after cancellation")
			}
			release()
		})
	}
}

func TestTodayBriefFallbacksCancelWhileSourceConnectionIsOccupied(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	if err := app.store.SetUIState(context.Background(), permissionUIStateKey(filesPermissionKey), "true"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{mobileBriefFileSource, mobileBriefWebSource} {
		t.Run(kind, func(t *testing.T) {
			wait, release := blockTodayLookup(t, app.activity.store.db)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			done := make(chan bool, 1)
			go func() {
				_, found := app.currentTodayBriefCandidate(ctx, MobileBriefRef{SourceKind: kind, SourceID: "source"}, MobileBriefV1{}, time.Now())
				done <- found
			}()
			wait()
			cancel()
			select {
			case found := <-done:
				if found {
					t.Fatal("canceled fallback returned a context candidate")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("fallback waited for the occupied connection after cancellation")
			}
			release()
		})
	}
}

func TestTodayChatSendStopDuringSecondLookupNeverStartsModel(t *testing.T) {
	app, tag, ref := todaySendTestApp(t)
	ref.GeneratedTitle = "Launch plan"
	prompt, err := app.PrepareTodayChatTurn(tag, "Help with \"Launch plan\"", ref)
	if err != nil {
		t.Fatal(err)
	}
	index, err := NewMemoryStore(t.TempDir(), MemoryStoreConfig{Provider: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	index.dirty = false
	app.memoryStore = index
	runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 1)}
	app.runner = runner
	wait, release := blockTodayLookup(t, index.db)
	done := make(chan error, 1)
	go func() {
		_, err := app.SendPromptWithTodayContext(prompt, "", "m", "", tag, nil, nil, false, false, "en", "", StreamItemData{ID: "today-user", Kind: "user"}, ref)
		done <- err
	}()
	wait()
	app.CancelByTag(tag)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || err.Error() != errTodayChatItemUnavailable.Error() {
			t.Fatalf("stopped send returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel the second context lookup")
	}
	release()
	waitForTagIdle(t, app, tag)
	select {
	case <-runner.inputs:
		t.Fatal("model started after Stop during Today context lookup")
	default:
	}
	page, err := app.store.LoadStreamItemsLatest(context.Background(), tag, 100)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("stopped lookup wrote a receipt: %+v, %v", page, err)
	}
	if _, err := app.PrepareTodayChatTurn(tag, prompt, ref); err != nil {
		t.Fatalf("Stop left the chat reserved: %v", err)
	}
}
