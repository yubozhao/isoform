package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"wails-gui/storage"
)

// That's me keeps the version (answer feedback sections 3 and 11.5): it
// stays answered on every open, an automatic write waits behind Show it,
// the person's own asks apply at once, and Undo lets the newer one through.
func TestPortraitThatsMeKeepsTheVersion(t *testing.T) {
	app, service, runner, path := newPortraitTestRig(t, 20)
	now := time.Now()
	service.now = func() time.Time { return now }
	service.check(context.Background(), now)
	shown := mustPortraitRead(t, app, "ready")
	if shown.SaidAt != 0 || shown.Pending {
		t.Fatalf("answered before the person did: %+v", shown)
	}
	ids, err := app.ConfirmAboutYouPortrait()
	if err != nil || len(ids) == 0 {
		t.Fatalf("That's me = %v, %v", ids, err)
	}
	for _, id := range ids {
		if entry, _ := app.GetMemoryEntry(id); !portraitConfirmed(entry) {
			t.Fatalf("%s not confirmed", id)
		}
	}
	got := mustPortraitRead(t, app, "ready")
	if got.SaidAt != now.UnixMilli() || got.GeneratedAt != shown.GeneratedAt {
		t.Fatalf("the answer did not stay: %+v", got)
	}

	// The weekly write runs, and waits behind Show it on every open.
	later := now.Add(portraitCadence + time.Hour)
	calls := len(runner.inputs)
	service.check(context.Background(), later)
	if len(runner.inputs) != calls+1 {
		t.Fatal("the weekly write did not run")
	}
	for range 2 {
		got = mustPortraitRead(t, app, "ready")
		if got.GeneratedAt != shown.GeneratedAt || !got.Pending || got.SaidAt == 0 {
			t.Fatalf("an automatic version replaced the kept one: %+v", got)
		}
	}
	if err := app.ShowAboutYouPortraitPending(); err != nil {
		t.Fatal(err)
	}
	got = mustPortraitRead(t, app, "ready")
	if got.GeneratedAt != later.UnixMilli() || got.Pending || got.SaidAt != 0 {
		t.Fatalf("Show it = %+v", got)
	}

	// The person's own ask (Try now) applies at once over a kept version.
	service.now = func() time.Time { return later }
	if _, err := app.ConfirmAboutYouPortrait(); err != nil {
		t.Fatal(err)
	}
	asked := later.Add(time.Hour)
	service.requestWrite(true)
	service.check(context.Background(), asked)
	if got = mustPortraitRead(t, app, "ready"); got.GeneratedAt != asked.UnixMilli() || got.Pending || got.SaidAt != 0 {
		t.Fatalf("Try now waited: %+v", got)
	}

	// Undo stops keeping it: the waiting version shows on the next read.
	service.now = func() time.Time { return asked }
	said, err := app.ConfirmAboutYouPortrait()
	if err != nil {
		t.Fatal(err)
	}
	weekly := asked.Add(portraitCadence + time.Hour)
	service.check(context.Background(), weekly)
	if got = mustPortraitRead(t, app, "ready"); !got.Pending {
		t.Fatalf("no waiting version: %+v", got)
	}
	if err := app.UndoConfirmAboutYouPortrait(said); err != nil {
		t.Fatal(err)
	}
	got = mustPortraitRead(t, app, "ready")
	if got.GeneratedAt != weekly.UnixMilli() || got.Pending || got.SaidAt != 0 {
		t.Fatalf("after Undo = %+v", got)
	}
	doc, _ := readPortrait(path)
	if doc.Pending != nil || doc.SaidAt != 0 {
		t.Fatalf("stored after Undo = %+v", doc)
	}
}

// Not quite on a kept version takes That's me back, and a forgotten belief
// takes a waiting version with it.
func TestPortraitNotQuiteAndForgetClearWhatWasKept(t *testing.T) {
	app, service, _, path := newPortraitTestRig(t, 20)
	now := time.Now()
	service.now = func() time.Time { return now }
	service.check(context.Background(), now)
	mustPortraitRead(t, app, "ready")
	if _, err := app.ConfirmAboutYouPortrait(); err != nil {
		t.Fatal(err)
	}
	service.check(context.Background(), now.Add(portraitCadence+time.Hour))
	if got := mustPortraitRead(t, app, "ready"); !got.Pending {
		t.Fatal("no waiting version")
	}
	if err := app.forgetAboutYouPortrait([]string{"belief-0"}); err != nil {
		t.Fatal(err)
	}
	doc, _ := readPortrait(path)
	if doc.Pending != nil {
		t.Fatal("a waiting version kept a forgotten belief")
	}
	if err := app.NotQuiteAboutYouPortrait(""); err != nil {
		t.Fatal(err)
	}
	if got := mustPortraitRead(t, app, "ready"); got.SaidAt != 0 {
		t.Fatalf("Not quite left That's me standing: %+v", got)
	}
}

// A changed sentence says why, once (sections 4, 5 and 11.5): a returned
// sentence names the confirm row, a rewrite carries the note, and seeing
// it clears both the tint and the line.
func TestPortraitChangedSentencesSayWhy(t *testing.T) {
	app, service, runner, _ := newPortraitTestRig(t, 20)
	runner.response = strings.Replace(portraitTestDraft, `"ids":["belief-6","belief-7"]`, `"ids":["belief-6"]`, 1)
	now := time.Now()
	service.now = func() time.Time { return now }
	service.check(context.Background(), now)
	if err := app.AnswerAboutYouPortraitBelief("belief-6", true); err != nil {
		t.Fatal(err)
	}
	service.check(context.Background(), now.Add(31*time.Second))
	got := mustPortraitRead(t, app, "ready")
	life := got.Sentences[2]
	if life.Role != "life" || !life.Changed || life.Cause == nil || !slices.Equal(life.Cause.Confirm, []string{"belief-6"}) || life.Cause.NotQuite {
		t.Fatalf("returned sentence = %+v", life)
	}
	for _, sentence := range got.Sentences[:2] {
		if sentence.Cause != nil {
			t.Fatalf("an unchanged sentence carries a cause: %+v", sentence)
		}
	}
	if err := app.MarkAboutYouPortraitSeen(); err != nil {
		t.Fatal(err)
	}
	if got = mustPortraitRead(t, app, "ready"); got.Sentences[2].Cause != nil || got.Sentences[2].Changed {
		t.Fatal("seeing it did not clear the line")
	}

	// Not quite with a note: the rewritten words carry it.
	runner.response = strings.Replace(portraitTestDraft, "your nos take about two days", "your nos go out together on Friday", 1)
	if err := app.NotQuiteAboutYouPortrait("I save nos for Friday"); err != nil {
		t.Fatal(err)
	}
	service.check(context.Background(), now.Add(time.Minute))
	got = mustPortraitRead(t, app, "ready")
	var cause *AboutYouPortraitCause
	for _, sentence := range got.Sentences {
		if sentence.Changed {
			cause = sentence.Cause
		}
	}
	if cause == nil || !cause.NotQuite || cause.Note != "I save nos for Friday" {
		t.Fatalf("rewrite cause = %+v in %+v", cause, got.Sentences)
	}
}

// Each answer on a belief and its Undo (sections 6, 7 and 11.1 to 11.3).
func TestBeliefAnswersAndUndo(t *testing.T) {
	app, service, runner, _ := newPortraitTestRig(t, 20)
	runner.response = strings.Replace(portraitTestDraft, `"ids":["belief-6","belief-7"]`, `"ids":["belief-6"]`, 1)
	service.check(context.Background(), time.Now())
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	read := func(id string) string {
		raw, err := os.ReadFile(filepath.Join(dir, id+".md"))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	belief := func(id string) AboutYouBelief {
		view, err := app.GetAboutYou()
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range view.Beliefs {
			if row.ID == id {
				return row
			}
		}
		t.Fatalf("no %s", id)
		return AboutYouBelief{}
	}

	// The portrait's confirm row is flagged for its line on That's me.
	if !belief("belief-6").PortraitCandidate || belief("belief-5").PortraitCandidate {
		t.Fatal("portrait candidate flag")
	}

	// That's me, then Undo.
	if _, err := app.ConfirmMemoryEntry("belief-1"); err != nil {
		t.Fatal(err)
	}
	if belief("belief-1").Status != "confirmed" {
		t.Fatal("not confirmed")
	}
	if err := app.UndoConfirmMemoryEntry("belief-1"); err != nil {
		t.Fatal(err)
	}
	if belief("belief-1").Status == "confirmed" {
		t.Fatal("Undo left it confirmed")
	}

	// Not quite left blank on an inferred belief: its chip, out of New this
	// week, and Undo puts the file back as it was.
	original := read("belief-2")
	confirmedAt, err := app.NotQuiteMemoryEntry("belief-2")
	if err != nil || confirmedAt != 0 {
		t.Fatalf("Not quite = %d, %v", confirmedAt, err)
	}
	marked := belief("belief-2")
	if marked.Status != "not_quite" || marked.NotQuiteAt == 0 || marked.ConfirmedAt != 0 {
		t.Fatalf("Not quite belief = %+v", marked)
	}
	view, _ := app.GetAboutYou()
	if slices.Contains(view.NewThisWeek, "belief-2") {
		t.Fatal("a Not quite belief stayed in New this week")
	}
	if err := app.UndoNotQuiteMemoryEntry("belief-2", confirmedAt); err != nil {
		t.Fatal(err)
	}
	if read("belief-2") != original {
		t.Fatalf("Undo changed the file:\n%s\nwant\n%s", read("belief-2"), original)
	}

	// On a confirmed belief, Undo restores the confirmation it had.
	confirmed, err := app.ConfirmMemoryEntry("belief-3")
	if err != nil {
		t.Fatal(err)
	}
	had := memoryConfirmedAt(confirmed.Content)
	if confirmedAt, err = app.NotQuiteMemoryEntry("belief-3"); err != nil || confirmedAt != had {
		t.Fatalf("Not quite returned %d, want %d (%v)", confirmedAt, had, err)
	}
	if err := app.UndoNotQuiteMemoryEntry("belief-3", confirmedAt); err != nil {
		t.Fatal(err)
	}
	if got := belief("belief-3"); got.Status != "confirmed" || got.ConfirmedAt != had || got.NotQuiteAt != 0 {
		t.Fatalf("after Undo = %+v", got)
	}

	// Not quite with words is the Edit wording save: confirmed, Not quite
	// gone, and an Edit in the answers ledger with the words before and
	// after. Undo puts back the version history kept, and the ledger keeps the
	// Undo.
	t.Cleanup(func() { answersLedgers.Delete(app) })
	if _, err := app.NotQuiteMemoryEntry("belief-4"); err != nil {
		t.Fatal(err)
	}
	before := read("belief-4")
	points := parseMemoryPoints(before)
	wording := "Says yes within the hour"
	saved, err := app.RewordMemoryEntry("belief-4", strings.Replace(before, "- "+points[0], "- "+wording, 1), wording)
	if err != nil {
		t.Fatal(err)
	}
	if memoryNotQuiteAt(saved.Content) != 0 || belief("belief-4").Status != "confirmed" {
		t.Fatalf("reworded belief = %s", saved.Content)
	}
	if _, err := app.UndoMemoryWording("belief-4", "Something else"); !errors.Is(err, errMemoryWordingChanged) {
		t.Fatalf("Undo over other words = %v", err)
	}
	if _, err := app.UndoMemoryWording("belief-4", wording); err != nil {
		t.Fatal(err)
	}
	if read("belief-4") != before {
		t.Fatalf("Undo did not restore the snapshot:\n%s", read("belief-4"))
	}
	var edit, undo *AnswerRecord
	entries := app.answersLedger().entries()
	for i := range entries {
		if entries[i].ItemID != "belief:belief-4" {
			continue
		}
		switch entries[i].Action {
		case workActionEdit:
			edit = &entries[i]
		case workActionUndo:
			undo = &entries[i]
		}
	}
	if edit == nil || edit.Edited == nil || edit.Edited.Before != strings.TrimSpace(points[0]) || edit.Edited.After != wording || edit.Kind != answerKindBelief || edit.Source != answerSourceAboutYou {
		t.Fatalf("the reword is not an Edit in the ledger: %+v", entries)
	}
	if undo == nil || undo.Undoes != edit.EntryID {
		t.Fatalf("the reword's Undo is not in the ledger: %+v", entries)
	}
}

// "Isoform stopped using this" (section 11.2): a Not quite belief is left
// out of what the AI reads until it is confirmed again.
func TestNotQuiteBeliefsLeftOutOfContext(t *testing.T) {
	app, _, _, _ := newPortraitTestRig(t, 3)
	has := func(id string) bool {
		entries, err := app.memoryEntriesForExternalRead()
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(entries, func(entry MemoryEntry) bool { return entry.ID == id })
	}
	if !has("belief-1") {
		t.Fatal("control: belief not readable")
	}
	if _, err := app.NotQuiteMemoryEntry("belief-1"); err != nil {
		t.Fatal(err)
	}
	if has("belief-1") {
		t.Fatal("a Not quite belief reaches the AI")
	}
	if _, err := app.memoryEntryForExternalRead("belief-1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read by id = %v", err)
	}
	if _, err := app.ConfirmMemoryEntry("belief-1"); err != nil {
		t.Fatal(err)
	}
	if !has("belief-1") {
		t.Fatal("confirming did not bring it back")
	}
}

// Not the same (section 9): the suggestion never comes back, and Undo
// lets it.
func TestPeopleNotTheSame(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	var ids []string
	for _, email := range []string{"Person@example.test", "person@example.test"} {
		p, err := a.store.CreatePrincipal(t.Context(), storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: email, CreatedFrom: contactsSourceSystem, Source: contactsSourceSystem})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.store.BindAppHandle(t.Context(), p.ID, "email", email, email); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	path, err := contactsConsentPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := recordContactsConsent(path, "people", time.Now()); err != nil {
		t.Fatal(err)
	}
	groups := func() int {
		overview, err := a.ListPeopleOverview()
		if err != nil {
			t.Fatal(err)
		}
		return len(overview.Duplicates)
	}
	if groups() != 1 {
		t.Fatal("control: no suggestion")
	}
	if err := a.NotSamePeople(ids); err != nil {
		t.Fatal(err)
	}
	if groups() != 0 {
		t.Fatal("Not the same was suggested again")
	}
	if err := a.UndoNotSamePeople(ids); err != nil {
		t.Fatal(err)
	}
	if groups() != 1 {
		t.Fatal("Undo did not bring the suggestion back")
	}
	if err := a.NotSamePeople(ids[:1]); err == nil {
		t.Fatal("Not the same took one profile")
	}
}
