package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
	"wails-gui/storage"
)

type mainChatReadMemoFixture struct {
	app         *App
	mainID      string
	mailSubject string
}

func newMainChatReadMemoFixture(t *testing.T) *mainChatReadMemoFixture {
	t.Helper()
	isolateActivityPrivacyTest(t)
	fixture := &mainChatReadMemoFixture{}
	previousMailRows := mobileGoalMailRows
	mobileGoalMailRows = func(*App) ([]storage.MailMessageRow, error) {
		if fixture.mailSubject == "" {
			return nil, nil
		}
		return []storage.MailMessageRow{{
			ID: "main-chat-read-memo-mail", Mailbox: storage.MailboxInbox,
			Subject: fixture.mailSubject, ToMe: true, At: time.Now().UnixMilli(),
		}}, nil
	}
	t.Cleanup(func() { mobileGoalMailRows = previousMailRows })

	app := mainChatTestApp(t)
	activity, err := NewActivityStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = activity.Close() })
	app.activity = &activityMonitorState{store: activity}
	app.engagement = newEngagementService(app.store, nil, engagementDelivery{})
	app.handoffStore, err = handoff.NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if err != nil {
		t.Fatal(err)
	}
	main, err := app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.app, fixture.mainID = app, main.LocalID
	return fixture
}

func (f *mainChatReadMemoFixture) addWorkCard(t *testing.T, id string, source handoff.Source, sourceID string) (string, handoff.Entry, engagementCardReadReceipt, mainChatProactive) {
	t.Helper()
	entry, err := f.app.handoffStore.Save(handoff.Entry{
		ID: "main-chat-read-memo-" + id, Title: "Review project changes",
		Status: handoff.StatusBlocked, Source: source, SourceID: sourceID,
		AppName: "Notes", Body: "Review the project changes and verify the result.",
		CreatedAt: time.Now().Add(-time.Minute).UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	card := mainChatProactive{
		MomentID: "main-chat-read-memo-moment-" + id, Kind: string(engagement.KindWorkBlocked),
		Headline: "Review project changes", Body: "A work update is ready.",
		State: "pending", Target: mainChatTarget{Kind: mainChatTargetHandoff, ID: entry.ID},
		ActionIDs: []string{}, ActionLabels: map[string]string{},
		Evidence: []mainChatEvidenceRef{{Kind: "handoff", ID: entry.ID}},
		Work:     &mainChatHandoffWork{HandoffID: entry.ID, Status: entry.Status, Summary: entry.Title, Deliverables: []mainChatHandoffDeliverable{}},
	}
	card.sourceHash = proactiveVoiceHash(entry)
	receipt := engagementCardReadReceipt{
		Version: 1, Presentation: proactiveVoicePresentationHash(card), MomentID: card.MomentID,
		HandoffID: entry.ID, HandoffHash: card.sourceHash,
	}
	card.ReadReceipt = f.app.saveEngagementCardRead(t.Context(), receipt.Presentation, card.MomentID, entry.ID, card.sourceHash)
	if card.ReadReceipt == "" {
		t.Fatalf("save read receipt for handoff %s", entry.ID)
	}
	return f.appendCard(t, id, card), entry, receipt, card
}

func (f *mainChatReadMemoFixture) addThreadOutcomeCard(t *testing.T, id string, row ActivityRow) string {
	t.Helper()
	card := mainChatProactive{
		MomentID: "main-chat-read-memo-outcome-" + id, Kind: string(engagement.KindCommitmentSlipping),
		Headline: "Project update", Body: "A thread update is ready.",
		State: "pending", Target: mainChatTarget{Kind: mainChatTargetToday},
		ActionIDs: []string{}, ActionLabels: map[string]string{}, Evidence: []mainChatEvidenceRef{},
		ActivityOutcomeSource: threadOutcomeSourceID(row.ID, row.ThreadOutcome),
	}
	return f.appendCard(t, id, card)
}

func (f *mainChatReadMemoFixture) appendCard(t *testing.T, id string, card mainChatProactive) string {
	t.Helper()
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	rowID := "main-chat-read-memo-row-" + id
	_, err = f.app.store.AppendStreamItem(t.Context(), f.mainID, storage.StreamItemRow{
		ID: rowID, Kind: "system", ToolName: mainChatProactiveTool,
		ToolInput: string(raw), CreatedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rowID
}

func (f *mainChatReadMemoFixture) addThread(t *testing.T, id string) ActivityRow {
	t.Helper()
	now := time.Now()
	fixtures := threadOutcomeFixtures(t)
	row := seedThreadFixture(t, f.app, id, fixtures[0], now)
	return saveFixtureOutcome(t, f.app, row, fixtures[0])
}

func (f *mainChatReadMemoFixture) readBoth(t *testing.T) (MainChatOverview, MainChatWaitingPage) {
	t.Helper()
	overview, err := f.app.GetMainChatOverview()
	if err != nil {
		t.Fatalf("GetMainChatOverview: %v", err)
	}
	page, err := f.app.ListMainChatWaitingItems("", 50)
	if err != nil {
		t.Fatalf("ListMainChatWaitingItems: %v", err)
	}
	return overview, page
}

func mainChatItemByID(items []StreamItemData, id string) (StreamItemData, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return StreamItemData{}, false
}

func assertActiveMainChatMemoItem(t *testing.T, item StreamItemData, original *mainChatProactive) {
	t.Helper()
	var card mainChatProactive
	if err := json.Unmarshal([]byte(item.ToolInput), &card); err != nil {
		t.Fatal(err)
	}
	if original != nil {
		if card.Work == nil || card.Headline != original.Headline || card.Body != original.Body {
			t.Fatalf("allowed handoff card was not projected with its source: %+v", card)
		}
		return
	}
	if card.ActivityOutcomeSource == "" || card.Kind == "thread_update_unavailable" {
		t.Fatalf("allowed thread outcome was withdrawn: %+v", card)
	}
}

func assertWithdrawnMainChatMemoItem(t *testing.T, item StreamItemData, original mainChatProactive, lang string, entry handoff.Entry) {
	t.Helper()
	var got mainChatProactive
	if err := json.Unmarshal([]byte(item.ToolInput), &got); err != nil {
		t.Fatal(err)
	}
	want := original
	want.sourceHash = ""
	withdrawProactiveVoice(&want, lang)
	want.Target = sourceFreeProactiveTarget(want.Kind)
	want.Evidence = nil
	want.ReadReceipt = ""
	normalizeMainChatProactiveActions(&want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("withdrawn handoff projection differs:\ngot:  %+v\nwant: %+v", got, want)
	}
	if got.Work != nil || len(got.Evidence) != 0 || got.ReadReceipt != "" ||
		strings.Contains(got.Headline, entry.Title) || strings.Contains(got.Body, entry.Body) {
		t.Fatalf("withdrawn card retained source data: %+v", got)
	}
}

func TestMainChatReadsHideSourceThatTurnsProtectedOrDeletedOnNextRead(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(*testing.T, *mainChatReadMemoFixture) (string, func(), *mainChatProactive, *handoff.Entry)
		withdraw bool
	}{
		{
			name: "deleted handoff", withdraw: true,
			setup: func(t *testing.T, f *mainChatReadMemoFixture) (string, func(), *mainChatProactive, *handoff.Entry) {
				rowID, entry, _, card := f.addWorkCard(t, "deleted-handoff", handoff.SourceDesktop, "")
				return rowID, func() {
					if err := f.app.handoffStore.Delete(entry.ID); err != nil {
						t.Fatal(err)
					}
				}, &card, &entry
			},
		},
		{
			name: "handoff source covered by observation pause", withdraw: true,
			setup: func(t *testing.T, f *mainChatReadMemoFixture) (string, func(), *mainChatProactive, *handoff.Entry) {
				row := seedThreadFixture(t, f.app, "memo-paused-source", threadOutcomeFixtures(t)[0], time.Now())
				rowID, entry, _, card := f.addWorkCard(t, "paused-handoff", handoff.SourceTriage, row.ID)
				return rowID, func() {
					f.app.saveObservationPauses([]observationPause{{Start: row.StartMs - 1, End: row.EndMs + 1}})
				}, &card, &entry
			},
		},
		{
			name: "activity behind thread outcome invalidated",
			setup: func(t *testing.T, f *mainChatReadMemoFixture) (string, func(), *mainChatProactive, *handoff.Entry) {
				row := f.addThread(t, "memo-invalidated-outcome")
				rowID := f.addThreadOutcomeCard(t, "invalidated-outcome", row)
				return rowID, func() {
					if _, err := f.app.activity.store.db.Exec(`UPDATE activities SET thread_outcome='' WHERE id=?`, row.ID); err != nil {
						t.Fatal(err)
					}
				}, nil, nil
			},
		},
		{
			name: "mail subject echoes outcome",
			setup: func(t *testing.T, f *mainChatReadMemoFixture) (string, func(), *mainChatProactive, *handoff.Entry) {
				row := f.addThread(t, "memo-mail-outcome")
				rowID := f.addThreadOutcomeCard(t, "mail-outcome", row)
				return rowID, func() { f.mailSubject = row.ThreadOutcome.Summary }, nil, nil
			},
		},
		{
			name: "closed eyes enabled", withdraw: true,
			setup: func(t *testing.T, f *mainChatReadMemoFixture) (string, func(), *mainChatProactive, *handoff.Entry) {
				rowID, entry, receipt, card := f.addWorkCard(t, "closed-eyes", handoff.SourceDesktop, "")
				ctx := withMainChatReadMemo(context.Background())
				return rowID, func() {
					if !f.app.engagementCardSourceReadable(ctx, receipt) {
						t.Fatal("receipt was unreadable before closed-eyes mode")
					}
					if err := f.app.SetClosedEyesModeEnabled(true); err != nil {
						t.Fatal(err)
					}
					if f.app.engagementCardSourceReadable(ctx, receipt) {
						t.Fatal("closed-eyes mode was bypassed by the cached source result")
					}
				}, &card, &entry
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newMainChatReadMemoFixture(t)
			rowID, mutate, original, entry := tc.setup(t, fixture)
			before, beforePage := fixture.readBoth(t)
			beforeItem, ok := mainChatItemByID(beforePage.Items, rowID)
			if before.Waiting == 0 || !ok {
				t.Fatalf("first read did not show card %s: overview=%+v items=%+v", rowID, before, beforePage.Items)
			}
			assertActiveMainChatMemoItem(t, beforeItem, original)
			mutate()
			after, afterPage := fixture.readBoth(t)
			afterItem, remains := mainChatItemByID(afterPage.Items, rowID)
			if tc.withdraw {
				if !remains || original == nil || entry == nil {
					t.Fatalf("withdrawn pending card %s was not retained: %+v", rowID, afterPage.Items)
				}
				assertWithdrawnMainChatMemoItem(t, afterItem, *original, fixture.app.currentUILanguage(), *entry)
				return
			}
			if remains || after.Waiting != before.Waiting-1 {
				t.Fatalf("thread outcome remained visible after source change: overview=%+v items=%+v", after, afterPage.Items)
			}
		})
	}
}

func TestMainChatReadMemoMatchesUnmemoizedProjection(t *testing.T) {
	fixture := newMainChatReadMemoFixture(t)
	fixture.addWorkCard(t, "equivalent-allowed", handoff.SourceDesktop, "")
	_, denied, _, _ := fixture.addWorkCard(t, "equivalent-denied", handoff.SourceDesktop, "")
	if err := fixture.app.handoffStore.Delete(denied.ID); err != nil {
		t.Fatal(err)
	}

	memoOverview, err := fixture.app.GetMainChatOverview()
	if err != nil {
		t.Fatal(err)
	}
	unmemoizedOverview, err := fixture.app.mainChatOverviewRead(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(memoOverview, unmemoizedOverview) {
		t.Fatalf("memoized overview differs from unmemoized projection:\nmemoized:   %+v\nunmemoized: %+v", memoOverview, unmemoizedOverview)
	}

	memoPage, err := fixture.app.ListMainChatWaitingItems("", 50)
	if err != nil {
		t.Fatal(err)
	}
	unmemoizedPage, err := fixture.app.listMainChatWaitingItems(context.Background(), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(memoPage, unmemoizedPage) {
		t.Fatalf("memoized waiting page differs from unmemoized projection:\nmemoized:   %+v\nunmemoized: %+v", memoPage, unmemoizedPage)
	}
}

func TestMainChatReadLoadsSourcesOncePerRead(t *testing.T) {
	fixture := newMainChatReadMemoFixture(t)
	row := fixture.addThread(t, "memo-load-thread")
	fixture.addWorkCard(t, "memo-load-handoff", handoff.SourceTriage, row.ID)
	fixture.addThreadOutcomeCard(t, "memo-load-outcome", row)
	previousHook := mainChatReadMemoLoad
	t.Cleanup(func() { mainChatReadMemoLoad = previousHook })

	for _, binding := range []struct {
		name string
		read func() error
	}{
		{
			name: "overview",
			read: func() error {
				_, err := fixture.app.GetMainChatOverview()
				return err
			},
		},
		{
			name: "waiting items",
			read: func() error {
				_, err := fixture.app.ListMainChatWaitingItems("", 50)
				return err
			},
		},
	} {
		t.Run(binding.name, func(t *testing.T) {
			loads := map[string]int{}
			mainChatReadMemoLoad = func(kind string) { loads[kind]++ }
			for read := 1; read <= 2; read++ {
				if err := binding.read(); err != nil {
					t.Fatal(err)
				}
				for _, kind := range []string{"handoffs", "pauses", "mail"} {
					if loads[kind] != read {
						t.Fatalf("read %d loaded %s %d times, want %d", read, kind, loads[kind], read)
					}
				}
			}
		})
	}
}

// One projection pass reads the voice gate's list sources once, however many
// cards it checks; a narrower mail window is served from the wider read.
func TestProactiveVoiceSourcesAreReadOncePerPass(t *testing.T) {
	a, _, _ := registryChatTestApp(t)
	loads := map[string]int{}
	previous := mainChatReadMemoLoad
	mainChatReadMemoLoad = func(kind string) { loads[kind]++ }
	t.Cleanup(func() { mainChatReadMemoLoad = previous })
	now := time.Now().UnixMilli()
	if _, err := a.store.UpsertMailMessages(t.Context(), []storage.MailMessageRow{{ID: "old", Mailbox: "INBOX", Subject: "a", At: now - 10_000}, {ID: "new", Mailbox: "INBOX", Subject: "b", At: now}}, now); err != nil {
		t.Skipf("mail fixture: %v", err)
	}
	ctx := withMainChatReadMemo(t.Context())
	for i := 0; i < 48; i++ {
		if _, err := a.proactiveVoicePrincipals(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := a.proactiveVoiceOpenReminders(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := a.proactiveVoiceMailMessages(ctx, now-20_000); err != nil {
			t.Fatal(err)
		}
	}
	narrow, err := a.proactiveVoiceMailMessages(ctx, now-5_000)
	if err != nil || len(narrow) != 1 || narrow[0].ID != "new" {
		t.Fatalf("narrow mail window = %+v %v", narrow, err)
	}
	if loads["principals"] != 1 || loads["reminders"] != 1 || loads["mailMessages"] != 1 {
		t.Fatalf("48 cards read the sources %v times; want once each", loads)
	}
}
