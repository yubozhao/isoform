package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// The mutation happens after a real collector read, before its callback returns.
// Keep this on the production generation/storage path. The brief is built
// from the memory snapshot pinned when it began. Since #823 an edit or removal
// of a cited note during collection is not a privacy action: the build
// finishes. Chat over the brief re-reads its sources, so a removed note is
// unavailable and a replaced title never reaches the model. A privacy action
// still restarts the build (TestMobileBriefGenerationIgnoresBackgroundWrites-
// ButNotPrivacyChanges).
func TestMobileBriefGenerationFinishesAfterEditsAndRemovalDuringCollection(t *testing.T) {
	for _, change := range []string{"unchanged", "title", "body", "restored bytes", "removed"} {
		t.Run(change, func(t *testing.T) {
			app, tag, _ := todaySendTestApp(t)
			const privateTitle = "Confidential acquisition plan"
			initial := "# " + privateTitle + "\nPrepare the private transaction."
			if change == "restored bytes" {
				initial = "# Public launch plan\nPrepare the public release."
			}
			if _, err := app.SaveMemoryEntry("launch", initial); err != nil {
				t.Fatal(err)
			}
			memory, err := memoryDir()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(memory, "launch.md")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			originalInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			svc := newMobileBriefService(app)
			svc.compose = composePublishableMobileBriefForTest
			now := time.Now()
			svc.now = func() time.Time { return now }
			svc.location = time.Local
			svc.sleepState = nil
			svc.speaker = mobileBriefSpeaker{}
			svc.power = func(string) (func(), error) { return func() {}, nil }
			svc.publish = func(MobileBriefV1, bool) error { return nil }
			gather := svc.gather
			var collected mobileBriefInput
			svc.gather = func(ctx context.Context, slot string, start, at time.Time) (mobileBriefInput, error) {
				// Simulate a supported external file edit and exact restoration.
				// The file starts public, is private when collected, and is
				// public again by capture, even with its old mtime restored.
				if change == "restored bytes" {
					tmp := filepath.Join(memory, "collecting.tmp")
					if err := os.WriteFile(tmp, []byte("# "+privateTitle+"\nPrivate temporary content."), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(tmp, path); err != nil {
						t.Fatal(err)
					}
					// Keep the fixture inside the frozen collection window;
					// its new inode still distinguishes the replaced source.
					if err := os.Chtimes(path, originalInfo.ModTime(), originalInfo.ModTime()); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				collected, err = gather(ctx, slot, start, at)
				if err != nil {
					return collected, err
				}
				found := false
				for _, item := range collected.Learned {
					if item.SourceKind == "memory" && item.SourceID == "launch" && (item.Title == privateTitle || change == "restored bytes") {
						found = found || item.Title == privateTitle
						// Select this real collected record as a goal, so both the
						// narrative and its offer consume the captured title.
						item.Kind, item.Status = "goal", "active"
						collected.OpenLoops = append(collected.OpenLoops, item)
					}
				}
				// The collector reads the memory folder pinned when collection
				// began: a note replaced during it is never read.
				if change == "restored bytes" && found {
					t.Fatal("the collector read a note written after collection began")
				}
				if change != "restored bytes" && !found {
					t.Fatal("real collector did not read the original title")
				}
				switch change {
				case "title":
					_, err = app.SaveMemoryEntry("launch", "# Public launch plan\nPrepare the public release.")
				case "body":
					_, err = app.SaveMemoryEntry("launch", "# "+privateTitle+"\nCorrected public body.")
				case "removed":
					err = os.Remove(path)
				case "restored bytes":
					err = os.WriteFile(path, original, 0600)
					if err == nil {
						err = os.Chtimes(path, originalInfo.ModTime(), originalInfo.ModTime())
					}
				}
				return collected, err
			}
			brief, generationErr := svc.generate(t.Context(), "morning", mobileBriefReasonPhone)
			if change == "unchanged" {
				if generationErr != nil {
					t.Fatalf("unchanged generation: %v", generationErr)
				}
				var ok bool
				brief, ok = svc.store.load(brief.BriefID)
				if !ok || brief.modelBinding == nil {
					t.Fatal("unchanged generation lost its stored binding")
				}
				if len(brief.Offers) == 0 {
					t.Fatal("missing offer control")
				}
			} else {
				// An edit or removal during collection is not a privacy
				// action: the build finishes from the snapshot it collected.
				if generationErr != nil {
					t.Fatalf("an edit during gathering failed the build: %v", generationErr)
				}
				if _, ok := svc.store.load(brief.BriefID); !ok {
					t.Fatal("the finished build was not stored")
				}
			}
			cache, err := app.store.GetUIState(t.Context(), mobileBriefAttendanceKeyPrefix+now.In(time.Local).Format("2006-01-02"))
			if err != nil || cache == "" {
				t.Fatalf("a finished build lost its attendance output: change=%s, err=%v", change, err)
			}
			available := change != "removed"
			titleGone := change == "title" || change == "removed"
			for _, kind := range []string{"brief"} {
				t.Run(kind, func(t *testing.T) {
					ref := TodayChatContextRef{SourceKind: kind, SourceID: mobileBriefConversationKey(app.mobileBriefForDelivery(brief)), BriefID: brief.BriefID}
					item, _, resolveErr := app.loadTodayChatContextItem(t.Context(), ref)
					_, draftErr := app.PrepareTodayChatDraft(ref)
					row := storage.StreamItemRow{ID: "gather-" + kind, Kind: "user", Content: "Let's go through this brief."}
					result, _, admitErr := app.admitTodayChatTurn(t.Context(), tag, row, ref, nil)
					for phase, err := range map[string]error{"resolve": resolveErr, "draft": draftErr, "admit": admitErr} {
						// Chat over the brief re-reads its sources at use: a
						// note removed during collection is unavailable.
						if (err == nil) != available {
							t.Errorf("%s %s: %v", change, phase, err)
						}
					}
					// A title that is no longer the note's never enters model
					// context, though the build that collected it finished.
					if titleGone && (strings.Contains(fmt.Sprint(item), privateTitle) || strings.Contains(fmt.Sprint(result), privateTitle)) {
						t.Error("removed collected title entered model context")
					}
					page, err := app.store.LoadStreamItemsLatest(t.Context(), tag, 100)
					if err != nil {
						t.Fatal(err)
					}
					persisted := false
					for _, saved := range page.Items {
						persisted = persisted || saved.ID == row.ID
					}
					if persisted != available {
						t.Fatalf("atomic row persisted=%v, change=%s", persisted, change)
					}
				})
			}
		})
	}
}

// A memory-only content check is insufficient: collection also depends on
// source ancestry, mutable services and the current model-read policies. The
// fence watches privacy, not write counters: ordinary background writes (a
// state heartbeat, a new activity session) keep the capture (2026-10-27).
func TestMobileBriefGatherFenceCoversSourceAndPolicyRevisions(t *testing.T) {
	ordinary := map[string]bool{"unchanged": true, "state heartbeat": true, "new activity": true}
	for _, change := range []string{"unchanged", "state heartbeat", "new activity", "knowledge", "memory controls", "handoff", "permission switch", "protected session", "deleted session", "privacy withdrawal", "sensitive apps", "Closed Eyes"} {
		t.Run(change, func(t *testing.T) {
			app := newTodayContextTestApp(t, true)
			brief := boundMemoryConversationFixture(t, app)
			var err error
			app.handoffStore, err = handoff.NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := app.activity.store.SaveSession(ActivitySessionRow{ID: "gather-source", StartTime: 1, EndTime: 2, AppNames: `["Notes"]`, TriageStatus: "done", CreatedAt: 1}, nil, nil); err != nil {
				t.Fatal(err)
			}
			before, err := app.readMobileBriefGatherRevision(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "knowledge":
				var dir string
				dir, err = knowledgeDir()
				if err == nil {
					err = os.WriteFile(filepath.Join(dir, "changed.md"), []byte("# Corrected knowledge"), 0600)
				}
			case "memory controls":
				var dir string
				dir, err = memoryDir()
				if err == nil {
					err = os.MkdirAll(filepath.Join(dir, ".semantic"), 0700)
				}
				if err == nil {
					err = os.WriteFile(filepath.Join(dir, ".semantic", "user-controls.json"), []byte(`{"revision":"corrected"}`), 0600)
				}
			case "handoff":
				_, err = app.handoffStore.Save(handoff.Entry{ID: "changed", Title: "Corrected one-time work", Status: handoff.StatusActive})
			case "state heartbeat":
				err = app.store.SetUIState(t.Context(), "gather-test-heartbeat", "1")
			case "new activity":
				err = app.activity.store.SaveSession(ActivitySessionRow{ID: "gather-new", StartTime: 3, EndTime: 4, AppNames: `["Notes"]`, TriageStatus: "pending", CreatedAt: time.Now().Add(time.Hour).UnixMilli()}, nil, nil)
			case "permission switch":
				err = app.store.SetUIState(t.Context(), permissionUIStateKey(calendarPermissionKey), "false")
			case "protected session":
				_, err = app.activity.store.db.ExecContext(t.Context(), `UPDATE activity_sessions SET triage_status='protected' WHERE id='gather-source'`)
			case "deleted session":
				_, err = app.activity.store.db.ExecContext(t.Context(), `DELETE FROM activity_sessions WHERE id='gather-source'`)
			case "privacy withdrawal":
				publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "memory", ID: "launch"})
			case "sensitive apps":
				old := pii.GetUserSensitiveApps()
				t.Cleanup(func() { pii.SetUserSensitiveApps(old) })
				pii.SetUserSensitiveApps(map[string]string{"com.apple.Notes": "Notes"})
			case "Closed Eyes":
				app.closedEyes.enabled.Store(true)
			}
			if err != nil {
				t.Fatal(err)
			}
			binding := app.captureMobileBriefModelSources(t.Context(), brief, before)
			before.close()
			if (binding != nil) != ordinary[change] {
				t.Fatalf("capture after %s: bound=%v", change, binding != nil)
			}
		})
	}
}

// 2026-10-27: a state database write every 20 to 30 seconds and activity rows
// every few seconds failed most morning briefs. A steady background write
// (every 5 s of a long collection) keeps the brief; a protect, delete or
// withdrawal during collection (each moves the privacy print) still rejects it.
func TestMobileBriefGenerationIgnoresBackgroundWritesButNotPrivacyChanges(t *testing.T) {
	for _, change := range []string{"heartbeats", "protect", "delete", "withdrawal"} {
		t.Run(change, func(t *testing.T) {
			app, _, _ := todaySendTestApp(t)
			if app.activity == nil {
				activity, err := NewActivityStore(filepath.Join(t.TempDir(), "activity"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = activity.Close() })
				app.activity = &activityMonitorState{store: activity}
			}
			if err := app.activity.store.SaveSession(ActivitySessionRow{ID: "earlier", StartTime: 1, EndTime: 2, AppNames: `["Notes"]`, TriageStatus: "done", CreatedAt: 1}, nil, nil); err != nil {
				t.Fatal(err)
			}
			svc := newMobileBriefService(app)
			svc.compose = composePublishableMobileBriefForTest
			now := time.Now()
			svc.now = func() time.Time { return now }
			svc.location = time.Local
			svc.sleepState = nil
			svc.speaker = mobileBriefSpeaker{}
			svc.power = func(string) (func(), error) { return func() {}, nil }
			svc.publish = func(MobileBriefV1, bool) error { return nil }
			gather := svc.gather
			svc.gather = func(ctx context.Context, slot string, start, at time.Time) (mobileBriefInput, error) {
				// Six 5-second ticks of a 30-second collection: the state
				// heartbeat and a new activity session with its rows.
				for tick := 0; tick < 6; tick++ {
					if err := app.store.SetUIState(ctx, "gather-test-heartbeat", fmt.Sprint(tick)); err != nil {
						return mobileBriefInput{}, err
					}
					row := ActivitySessionRow{ID: fmt.Sprintf("live-%d", tick), StartTime: int64(10 + tick), EndTime: int64(11 + tick), AppNames: `["Notes"]`, TriageStatus: "pending", CreatedAt: time.Now().Add(time.Hour).UnixMilli()}
					if err := app.activity.store.SaveSession(row, nil, nil); err != nil {
						return mobileBriefInput{}, err
					}
				}
				collected, err := gather(ctx, slot, start, at)
				if err != nil {
					return collected, err
				}
				switch change {
				case "protect":
					// The product's protect action, which moves the privacy print.
					err = app.activity.store.UpdateTriageStatus("earlier", "protected")
				case "delete":
					err = app.deleteActivityWithPrivacyWithdrawal(app.activity.store, "earlier")
				case "withdrawal":
					publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: "earlier"})
				}
				return collected, err
			}
			brief, err := svc.generate(t.Context(), "morning", mobileBriefReasonSchedule)
			if change == "heartbeats" {
				if err != nil {
					t.Fatalf("background writes failed the brief: %v", err)
				}
				if _, ok := svc.store.load(brief.BriefID); !ok {
					t.Fatal("the brief was not stored")
				}
				return
			}
			if !errors.Is(err, errMobileBriefSourcesChanged) {
				t.Fatalf("a %s during collection was accepted: %v", change, err)
			}
			if svc.store.exists(time.Now().Format("2006-01-02") + "-morning") {
				t.Fatal("a brief collected before the privacy change was stored")
			}
		})
	}
}
