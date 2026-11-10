package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/internal/handoff"
)

// Isoform writes memory notes all the time (today-review v1.1.15 D12).
// During a collection an unrelated note never rejects the brief;
// editing, removing or forgetting a note it cites does, and every other file the brief
// reads still counts whatever changed.
func TestMobileBriefGatherFenceKeepsIsoformMemoryWrites(t *testing.T) {
	cases := map[string]string{
		"new note": "", "edited uncited note": "", "removed uncited note": "", "forgot uncited note": "",
		"edited cited note": "memory_files", "edited note before the brief is built": "memory_files", "removed cited note": "memory_files", "forgot cited note": "privacy_withdrawal",
		"knowledge": "source_files", "removed note before the brief is built": "memory_files",
	}
	for change, want := range cases {
		t.Run(change, func(t *testing.T) {
			app := newTodayContextTestApp(t, true)
			brief := boundMemoryConversationFixture(t, app)
			if _, err := app.SaveMemoryEntry("garden", "# Garden rota\nWren waters on Tuesdays."); err != nil {
				t.Fatal(err)
			}
			revision, err := app.readMobileBriefGatherRevision(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer revision.close()
			if change != "removed note before the brief is built" && change != "edited note before the brief is built" {
				revision.cite(brief)
			}
			dir, _ := memoryDir()
			switch change {
			case "new note":
				_, err = app.SaveMemoryEntry("lantern", "# Lantern walk\nOrla leads the route.")
			case "edited uncited note":
				_, err = app.SaveMemoryEntry("garden", "# Garden rota\nWren waters on Thursdays.")
			case "removed uncited note", "removed note before the brief is built":
				err = os.Remove(filepath.Join(dir, "garden.md"))
			case "forgot uncited note":
				publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "memory", ID: "garden"})
			case "edited cited note", "edited note before the brief is built":
				_, err = app.SaveMemoryEntry("launch", "# Launch plan\nPrepare the corrected release.")
			case "removed cited note":
				err = os.Remove(filepath.Join(dir, "launch.md"))
			case "forgot cited note":
				publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "memory", ID: "launch"})
			case "knowledge":
				var knowledge string
				if knowledge, err = knowledgeDir(); err == nil {
					err = os.WriteFile(filepath.Join(knowledge, "changed.md"), []byte("# Corrected knowledge"), 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := app.mobileBriefGatherChange(t.Context(), revision); got != want {
				t.Fatalf("change = %q, want %q", got, want)
			}
		})
	}
}

// The collection is built from the memory folder as it began.
func TestMobileBriefCollectionReadsPinnedMemory(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	if _, err := app.SaveMemoryEntry("launch", "# Launch plan\nPrepare the release."); err != nil {
		t.Fatal(err)
	}
	revision, err := app.readMobileBriefGatherRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer revision.close()
	if _, err := app.SaveMemoryEntry("launch", "# Renamed plan\nPrepare the release."); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	in := mobileBriefInput{WindowStart: now.Add(-time.Hour), AsOf: now.Add(time.Hour), Location: time.Local, memory: revision.memory}
	collectMobileBriefRecall(app, &in)
	titles := []string{}
	for _, item := range in.Learned {
		if item.SourceKind == "memory" && item.SourceID == "launch" {
			titles = append(titles, item.Title)
		}
	}
	if len(titles) != 1 || titles[0] != "Launch plan" {
		t.Fatalf("collected %v, want the note as collection began", titles)
	}
}

// Today shows the stored brief at once, with one batched privacy check of
// its refs, while a live refresh (minutes on a busy computer) is running.
func TestTodayLatestBriefReadsTheStoredBriefWithoutWaiting(t *testing.T) {
	rig, svc, brief := cardRig(t)
	app := rig.app
	for i := 0; i < 400; i++ {
		if _, err := app.handoffStore.Save(handoff.Entry{ID: fmt.Sprintf("errand-%d", i), Title: fmt.Sprintf("Errand %d for Juniper Vale", i), Source: handoff.SourceMobile, Status: handoff.StatusActive, CreatedAt: svc.now().UnixMilli(), UpdatedAt: svc.now().UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 60; i++ {
		brief.Evidence = append(brief.Evidence, MobileBriefRef{Ref: fmt.Sprintf("x%d", i), SourceKind: "history", SourceID: fmt.Sprintf("errand-%d", i), Label: "Errand"})
	}
	if err := svc.store.save(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := app.GetLatestBrief(); err != nil {
		t.Fatal(err)
	}
	svc.todayRefreshWG.Wait()
	loads := 0
	previous := mainChatReadMemoLoad
	mainChatReadMemoLoad = func(kind string) {
		if kind == "handoffs" {
			loads++
		}
	}
	t.Cleanup(func() { mainChatReadMemoLoad = previous })
	// Something the read depends on changed, so it is worked out once more,
	// while a live refresh holds todayMu for its whole source read.
	briefPrivacyChanged()
	svc.todayMu.Lock()
	started := time.Now()
	view, err := app.GetLatestBrief()
	elapsed := time.Since(started)
	mainChatReadMemoLoad = previous
	svc.todayMu.Unlock()
	svc.todayRefreshWG.Wait()
	if err != nil || view.Brief == nil || view.Brief.BriefID != brief.BriefID {
		t.Fatalf("view = %+v, %v", view.Brief, err)
	}
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("GetLatestBrief took %s", elapsed)
	}
	if loads != 1 {
		t.Fatalf("the work records were listed %d times for one read", loads)
	}
}

// today-review v1.1.15 D13: the brief is saved as finished text the moment
// it is made; opening it reads that and works nothing out again, a change it
// depends on makes the next read work it out once, and a saved read that
// cannot be read is an error (Today's "Couldn't load your brief. Try again").
func TestTodayOpensTheFinishedReadSavedWhenTheBriefWasMade(t *testing.T) {
	rig, svc, brief := cardRig(t)
	app := rig.app
	if _, found, err := svc.store.loadFinishedRead(brief.BriefID); !found || err != nil {
		t.Fatalf("no finished read saved with the brief: %v", err)
	}
	if _, err := app.GetLatestBrief(); err != nil {
		t.Fatal(err)
	}
	svc.todayRefreshWG.Wait()
	loads := 0
	previous := mainChatReadMemoLoad
	mainChatReadMemoLoad = func(kind string) {
		if kind == "handoffs" {
			loads++
		}
	}
	t.Cleanup(func() { mainChatReadMemoLoad = previous })
	read, ok, err := svc.finishedLatest()
	if err != nil || !ok || read.BriefID != brief.BriefID || loads != 0 {
		t.Fatalf("finished read = %v %v, %d delivery reads", ok, err, loads)
	}
	// Removing a cited work record changes what the read depends on.
	if err := app.handoffStore.Delete("memo"); err != nil {
		t.Fatal(err)
	}
	read, ok, err = svc.finishedLatest()
	if err != nil || !ok || loads == 0 {
		t.Fatalf("a changed source kept the saved read: %v %v, %d delivery reads", ok, err, loads)
	}
	for _, item := range read.Sections.OpenLoops {
		if item.Title == "Release memo" {
			t.Fatal("the removed work record is still shown")
		}
	}
	mainChatReadMemoLoad = previous
	path, _ := svc.store.finishedReadPath(read.BriefID)
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.GetLatestBrief(); err == nil {
		t.Fatal("an unreadable saved brief was not reported")
	}
	svc.todayRefreshWG.Wait()
	if view, err := app.GetLatestBrief(); err != nil || view.Brief == nil {
		t.Fatalf("Try again did not open the brief: %v", err)
	}
	svc.todayRefreshWG.Wait()
}
