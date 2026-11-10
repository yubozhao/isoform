package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/storage"
)

// A template can quote an edited memory even when its source remains readable.
// The seal must not show the old text after the watcher detects the change.
func TestMobileBriefTemplateWithholdsChangedMemory(t *testing.T) {
	for _, change := range []string{"edit", "remove"} {
		t.Run(change, func(t *testing.T) {
			app, svc, brief := newStoredMobileBriefPrivacyFixture(t)
			svc.wg.Wait()
			dir, err := memoryDir()
			if err != nil {
				t.Fatal(err)
			}
			watcher, err := NewMemoryStore(filepath.Dir(dir), MemoryStoreConfig{Provider: "none"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = watcher.Close() })
			watcher.detectFileChanges()
			var ref string
			for _, evidence := range brief.Evidence {
				if evidence.SourceID == "safe-recall" {
					ref = evidence.Ref
				}
			}
			if ref == "" {
				t.Fatal("fixture lacks the memory source")
			}
			brief.NarrativeStatus = "unavailable"
			brief.Narrative = []MobileBriefPara{{Text: "Old safe recall sentence.", Refs: []string{ref}}}
			brief.Audio = nil
			if err := svc.store.save(brief, svc.now()); err != nil {
				t.Fatal(err)
			}
			if _, _, wrote := svc.seal(t.Context(), brief); !wrote {
				t.Fatal("could not seal the initial brief")
			}
			switch change {
			case "edit":
				writePrivacyMemory(t, "safe-recall", "# Corrected recall\n\nA corrected note.\n")
			case "remove":
				if err := os.Remove(filepath.Join(dir, "safe-recall.md")); err != nil {
					t.Fatal(err)
				}
			}
			checkMemoryFolderNow(app)
			watcher.pollFileChanges(false)
			stored, ok := svc.store.load(brief.BriefID)
			if !ok {
				t.Fatal("stored brief missing")
			}
			sealed, _, err := svc.finishedReadWithholding(stored)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(sealed)
			if err != nil {
				t.Fatal(err)
			}
			for _, old := range []string{"Old safe recall sentence", "Safe recall title"} {
				if strings.Contains(string(data), old) {
					t.Errorf("sealed read still shows text from the %s source: %q", change, old)
				}
			}
		})
	}
}

func TestMobileBriefSealedSourceProvenance(t *testing.T) {
	app, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	saved, found, err := svc.store.loadFinishedRead(source.BriefID)
	if err != nil || !found || saved.SealedAt.IsZero() {
		t.Fatalf("missing seal time: found=%v err=%v", found, err)
	}
	key := mobileBriefSourceKey("memory", "safe-recall")
	if len(saved.Fingerprints[key]) != 67 {
		t.Fatalf("missing memory fingerprint: %q", saved.Fingerprints[key])
	}
	originalTime := saved.SealedAt
	originalPrint := saved.Fingerprints[key]

	// The check does not depend on the watcher or the privacy epoch.
	writePrivacyMemory(t, "safe-recall", "# Corrected offline\n")
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	later := originalTime.Add(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "safe-recall.md"), later, later); err != nil {
		t.Fatal(err)
	}
	if kind, changed := app.mobileBriefEditedSource(source, originalTime); !changed || kind != "memory" {
		t.Fatalf("offline edit was not detected: %q %v", kind, changed)
	}
	if got, withheld, wrote := svc.seal(t.Context(), source); !wrote || withheld.Reason != mobileBriefWithheldSource || len(got.Narrative) != 0 {
		t.Fatalf("reseal exposed an edited source: wrote=%v reason=%q", wrote, withheld.Reason)
	}
	saved, _, err = svc.store.loadFinishedRead(source.BriefID)
	if err != nil || !saved.SealedAt.Equal(originalTime) || saved.Fingerprints[key] != originalPrint {
		t.Fatal("reseal replaced the original provenance")
	}
}

func TestMobileBriefLegacySealUsesFileTime(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	saved, _, err := svc.store.loadFinishedRead(source.BriefID)
	if err != nil {
		t.Fatal(err)
	}
	saved.SealedAt = time.Time{}
	saved.Fingerprints = nil
	if err := svc.store.saveFinishedRead(source.BriefID, saved); err != nil {
		t.Fatal(err)
	}
	sealPath, _ := svc.store.finishedReadPath(source.BriefID)
	sealTime := time.Now().Add(-time.Minute)
	if err := os.Chtimes(sealPath, sealTime, sealTime); err != nil {
		t.Fatal(err)
	}
	writePrivacyMemory(t, "safe-recall", "# Corrected after legacy seal\n")
	got, withheld, err := svc.finishedReadWithholding(source)
	if err != nil || withheld.Reason != mobileBriefWithheldSource || len(got.Narrative) != 0 {
		t.Fatalf("legacy seal exposed edited memory: reason=%q err=%v", withheld.Reason, err)
	}
}

func TestMobileBriefEditableSourceTimes(t *testing.T) {
	app, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	dir, err := knowledgeDir()
	if err != nil {
		t.Fatal(err)
	}
	baseline := time.Now().Add(time.Second)
	ref := MobileBriefRef{SourceKind: "knowledge", SourceID: "protected-knowledge"}
	one := MobileBriefV1{Evidence: []MobileBriefRef{ref}}
	if kind, changed := app.mobileBriefEditedSource(one, baseline); changed {
		t.Fatalf("unchanged knowledge was withheld: %s", kind)
	}
	later := baseline.Add(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "protected-knowledge.md"), later, later); err != nil {
		t.Fatal(err)
	}
	if kind, changed := app.mobileBriefEditedSource(one, baseline); !changed || kind != "knowledge" {
		t.Fatal("knowledge modification was not detected")
	}
	if err := os.Remove(filepath.Join(dir, "protected-knowledge.md")); err != nil {
		t.Fatal(err)
	}
	if _, changed := app.mobileBriefEditedSource(one, baseline); !changed {
		t.Fatal("missing knowledge was not detected")
	}
	_ = source
}

func TestMobileBriefCitedPersonEditAndRemoval(t *testing.T) {
	app, svc, _ := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	person, err := app.store.CreatePrincipal(t.Context(), storage.PrincipalRow{ID: "fictional-person", Kind: storage.PrincipalKindContact, DisplayName: "Original name"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := app.store.CreatePrincipal(t.Context(), storage.PrincipalRow{ID: "other-person", Kind: storage.PrincipalKindContact, DisplayName: "Other person"})
	if err != nil {
		t.Fatal(err)
	}
	source := MobileBriefV1{Evidence: []MobileBriefRef{{Ref: "person", SourceKind: "principal", SourceID: person.ID}}}
	baseline := time.Now()
	if _, changed := app.mobileBriefEditedSource(source, baseline); changed {
		t.Fatal("unchanged person was withheld")
	}
	time.Sleep(3 * time.Millisecond) // principal timestamps have millisecond precision
	other.DisplayName = "Other renamed"
	if _, err := app.store.UpdatePrincipal(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, changed := app.mobileBriefEditedSource(source, baseline); changed {
		t.Fatal("unrelated person withdrew the brief")
	}
	person.DisplayName = "Corrected name"
	if _, err := app.store.UpdatePrincipal(t.Context(), person); err != nil {
		t.Fatal(err)
	}
	if kind, changed := app.mobileBriefEditedSource(source, baseline); !changed || kind != "principal" {
		t.Fatal("person edit was not detected")
	}
	if err := app.store.DeletePrincipal(t.Context(), person.ID); err != nil {
		t.Fatal(err)
	}
	if _, changed := app.mobileBriefEditedSource(source, baseline); !changed {
		t.Fatal("removed person was not detected")
	}
}

func TestMobileBriefUnrelatedMemoryEditLeavesWrittenText(t *testing.T) {
	app, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	before, _, err := svc.finishedReadWithholding(source)
	if err != nil || len(before.Narrative) == 0 {
		t.Fatalf("missing original text: %v", err)
	}
	writePrivacyMemory(t, "unrelated-note", "# Other note\n")
	writePrivacyMemory(t, "unrelated-note", "# Corrected other note\n")
	checkMemoryFolderNow(app)
	briefPrivacyChanged()
	after, withheld, err := svc.finishedReadWithholding(source)
	if err != nil || withheld.Reason != "" || len(after.Narrative) == 0 || after.Narrative[0].Text != before.Narrative[0].Text {
		t.Fatalf("unrelated edit hid the written brief: reason=%q err=%v", withheld.Reason, err)
	}
}

func TestMobileBriefOtherEditableSourceTimes(t *testing.T) {
	for _, kind := range []string{"activity", "summary", "thread summary", "schedule", "deliverable"} {
		t.Run(kind, func(t *testing.T) {
			app := newPrivacyReadTestApp(t)
			ctx := t.Context()
			activity := ActivityRow{ID: "fictional-activity", Status: activityStatusIdle, Title: "Original activity", UpdatedAt: time.Now().Add(-time.Second).UnixMilli()}
			job := storage.CronJobRow{ID: "fictional-schedule", Name: "Original schedule"}
			deliverable := storage.ChatDeliverableRow{ID: "fictional-output", SessionID: "fictional-session", Title: "Original output", Files: `["draft.txt"]`}
			var id string
			sourceKind := kind
			switch kind {
			case "activity", "summary", "thread summary":
				id = activity.ID
				if kind == "thread summary" {
					sourceKind = "summary"
					id = threadOutcomeMomentPrefix + activity.ID + ":1"
				}
				if err := app.activity.store.InsertActivity(activity); err != nil {
					t.Fatal(err)
				}
			case "schedule":
				id = job.ID
				if err := app.store.SaveCronJob(ctx, job); err != nil {
					t.Fatal(err)
				}
			case "deliverable":
				id = "chat:" + deliverable.ID
				if err := app.store.SaveChatDeliverable(ctx, deliverable); err != nil {
					t.Fatal(err)
				}
			}
			brief := MobileBriefV1{Evidence: []MobileBriefRef{{Ref: "source", SourceKind: sourceKind, SourceID: id}}}
			baseline := time.Now()
			if _, changed := app.mobileBriefEditedSource(brief, baseline); changed {
				t.Fatal("unchanged source was withheld")
			}
			time.Sleep(3 * time.Millisecond)
			switch kind {
			case "activity", "summary", "thread summary":
				activity.Title = "Corrected activity"
				activity.UpdatedAt = time.Now().UnixMilli()
				if err := app.activity.store.UpdateActivity(activity); err != nil {
					t.Fatal(err)
				}
			case "schedule":
				job.Name = "Corrected schedule"
				if err := app.store.SaveCronJob(ctx, job); err != nil {
					t.Fatal(err)
				}
			case "deliverable":
				deliverable.Title = "Corrected output"
				if err := app.store.SaveChatDeliverable(ctx, deliverable); err != nil {
					t.Fatal(err)
				}
			}
			if got, changed := app.mobileBriefEditedSource(brief, baseline); !changed || got != sourceKind {
				t.Fatal("edited source was not detected")
			}
		})
	}
}

func TestMobileBriefCalendarHandoffAndPurposeSourceTimes(t *testing.T) {
	for _, kind := range []string{"calendar", "history", "purpose"} {
		t.Run(kind, func(t *testing.T) {
			rig := newHandoffTestRig(t)
			app := rig.app
			ctx := t.Context()
			var id string
			event := storage.CalendarEventRow{ID: "fictional-event", Title: "Original meeting"}
			entry := handoff.Entry{ID: "fictional-handoff", Title: "Original handoff", Status: handoff.StatusBlocked}
			var purpose PurposeOpenLoopDetail
			var err error
			switch kind {
			case "calendar":
				id = event.ID
				err = app.store.UpsertCalendarEvents(ctx, []storage.CalendarEventRow{event}, time.Now().Add(-time.Second).UnixMilli())
			case "history":
				id = entry.ID
				_, err = rig.handoffs.Save(entry)
			case "purpose":
				purpose, err = app.createUserPurpose(userPurposeCreate{Key: "fictional-purpose", Origin: "computer", Kind: PurposeKindGoal, Text: "Original goal"}, time.Now())
				id = purpose.OpenLoop.ID
			}
			if err != nil {
				t.Fatal(err)
			}
			brief := MobileBriefV1{Evidence: []MobileBriefRef{{Ref: "source", SourceKind: kind, SourceID: id}}}
			baseline := time.Now()
			if _, changed := app.mobileBriefEditedSource(brief, baseline); changed {
				t.Fatal("unchanged source was withheld")
			}
			time.Sleep(3 * time.Millisecond)
			switch kind {
			case "calendar":
				event.Title = "Corrected meeting"
				err = app.store.UpsertCalendarEvents(ctx, []storage.CalendarEventRow{event}, time.Now().UnixMilli())
			case "history":
				entry.Title = "Corrected handoff"
				_, err = rig.handoffs.Save(entry)
			case "purpose":
				_, err = app.moveUserPurpose(userPurposeTransition{Key: "fictional-update", Origin: "test", OpenLoopID: id, Action: userPurposeWaiting, WaitingOn: "A person"}, time.Now())
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, changed := app.mobileBriefEditedSource(brief, baseline); !changed || got != kind {
				t.Fatal("edited source was not detected")
			}
		})
	}
}

func TestMobileBriefEditedSourceDoesNotBypassAnotherWithdrawal(t *testing.T) {
	app, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal the brief")
	}
	saved, _, err := svc.store.loadFinishedRead(source.BriefID)
	if err != nil {
		t.Fatal(err)
	}
	writePrivacyMemory(t, "safe-recall", "# Corrected recall\n")
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	later := saved.SealedAt.Add(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "safe-recall.md"), later, later); err != nil {
		t.Fatal(err)
	}
	withdrawWithoutAmendment(t, app)
	read, _, err := svc.finishedReadWithholding(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Safe recall title", "Protected recall title", "Protected knowledge title", "Protected meeting title"} {
		if strings.Contains(string(mustJSON(t, read)), forbidden) {
			t.Errorf("edited-source path bypassed a withdrawal: %s", forbidden)
		}
	}
}

func TestMobileBriefLaterEvidenceDoesNotChangeOriginalCitations(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	before, withheld, err := svc.finishedReadWithholding(source)
	if err != nil || withheld.Reason != "" || len(before.Narrative) == 0 {
		t.Fatalf("missing initial text: %q %v", withheld.Reason, err)
	}
	writePrivacyMemory(t, "later-note", "# Later note\n")
	source.Evidence = append(source.Evidence, MobileBriefRef{Ref: "later", SourceKind: "memory", SourceID: "later-note", Label: "Later note"})
	if err := svc.store.saveDocument(source, svc.now()); err != nil {
		t.Fatal(err)
	}
	if _, withheld, wrote := svc.seal(t.Context(), source); !wrote || withheld.Reason != "" {
		t.Fatalf("later evidence withdrew the original text: %q", withheld.Reason)
	}
	after, withheld, err := svc.finishedReadWithholding(source)
	if err != nil || withheld.Reason != "" || len(after.Narrative) == 0 || after.Narrative[0].Text != before.Narrative[0].Text {
		t.Fatalf("later evidence hid the original text: %q %v", withheld.Reason, err)
	}
}

func TestMobileBriefFingerprintMemoryRewriteAndEdit(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "safe-recall.md")
	original := "---\ntype: memory\ntitle: Safe recall title\ntimestamp: 2026-01-01T00:00:00Z\n---\n\nA manually written note.\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, wrote := svc.seal(t.Context(), source); !wrote {
		t.Fatal("could not seal brief")
	}
	saved, _, err := svc.store.loadFinishedRead(source.BriefID)
	if err != nil {
		t.Fatal(err)
	}
	rewrite := strings.Replace(original, "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z", 1)
	if err := os.WriteFile(path, []byte(rewrite), 0600); err != nil {
		t.Fatal(err)
	}
	later := saved.SealedAt.Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	got, withheld, err := svc.finishedReadWithholding(source)
	if err != nil || withheld.Reason != "" || len(got.Narrative) == 0 {
		t.Fatalf("metadata rewrite withheld the brief: reason=%q kind=%q err=%v", withheld.Reason, withheld.SourceKind, err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(rewrite, "A manually written note.", "An edited note.", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previousOutput)
	got, withheld, err = svc.finishedReadWithholding(source)
	if err != nil || withheld.Reason != mobileBriefWithheldSource || withheld.SourceKind != "memory" || len(got.Narrative) != 0 {
		t.Fatalf("edited memory was shown: reason=%q kind=%q err=%v", withheld.Reason, withheld.SourceKind, err)
	}
	if !strings.Contains(logs.String(), "written=withheld reason=source_withdrawn kind=memory") || strings.Contains(logs.String(), "An edited note") {
		t.Fatal("read log did not identify the source kind without its content")
	}
}

func TestMobileBriefFingerprintCalendarSyncAndEdits(t *testing.T) {
	for _, edit := range []string{"rename", "move", "attendee", "remove"} {
		t.Run(edit, func(t *testing.T) {
			app, svc, source := newStoredMobileBriefPrivacyFixture(t)
			svc.wg.Wait()
			event := storage.CalendarEventRow{ID: "fictional-event", Title: "Planning", StartAt: time.Now().Add(time.Hour).UnixMilli(), EndAt: time.Now().Add(2 * time.Hour).UnixMilli(), Attendees: []storage.CalendarAttendeeRow{{Name: "Alex"}}}
			if err := app.store.UpsertCalendarEvents(t.Context(), []storage.CalendarEventRow{event}, time.Now().Add(-time.Second).UnixMilli()); err != nil {
				t.Fatal(err)
			}
			ref := MobileBriefRef{Ref: "calendar-ref", SourceKind: "calendar", SourceID: event.ID}
			source.Evidence = []MobileBriefRef{ref}
			source.Narrative = []MobileBriefPara{{Text: "Planning is coming up.", Refs: []string{ref.Ref}}}
			saved, found, err := svc.store.loadFinishedRead(source.BriefID)
			if err != nil || !found {
				t.Fatalf("missing seal: %v", err)
			}
			saved.SourceRefs = mobileBriefSealSources(source)
			saved.Fingerprints = app.mobileBriefSourceFingerprints(t.Context(), source)
			saved.Brief, err = marshalStoredMobileBrief(source)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.store.saveFinishedRead(source.BriefID, saved); err != nil {
				t.Fatal(err)
			}
			event.Provider = "new sync metadata"
			event.AttendeeCount = 42
			event.Attendees[0].PrincipalID = "new-index-link"
			if err := app.store.UpsertCalendarEvents(t.Context(), []storage.CalendarEventRow{event}, saved.SealedAt.Add(time.Second).UnixMilli()); err != nil {
				t.Fatal(err)
			}
			got, withheld, err := svc.finishedReadWithholding(source)
			if err != nil || withheld.Reason != "" || len(got.Narrative) == 0 {
				t.Fatalf("calendar sync withheld the brief: reason=%q kind=%q err=%v", withheld.Reason, withheld.SourceKind, err)
			}
			switch edit {
			case "rename":
				event.Title = "Renamed"
			case "move":
				event.StartAt += int64(time.Hour / time.Millisecond)
			case "attendee":
				event.Attendees[0].Name = "Taylor"
			case "remove":
				if _, err := app.store.MarkCalendarEventsVanished(t.Context(), event.Source, event.Provider, event.StartAt-1, event.EndAt+1, map[string]bool{}, saved.SealedAt.Add(2*time.Second).UnixMilli()); err != nil {
					t.Fatal(err)
				}
			}
			if edit != "remove" {
				if err := app.store.UpsertCalendarEvents(t.Context(), []storage.CalendarEventRow{event}, saved.SealedAt.Add(2*time.Second).UnixMilli()); err != nil {
					t.Fatal(err)
				}
			}
			got, withheld, err = svc.finishedReadWithholding(source)
			if err != nil || withheld.Reason != mobileBriefWithheldSource || withheld.SourceKind != "calendar" || len(got.Narrative) != 0 {
				t.Fatalf("calendar %s was shown: reason=%q kind=%q err=%v", edit, withheld.Reason, withheld.SourceKind, err)
			}
		})
	}
}

func TestMobileBriefLegacyFingerprintAndTimestamp(t *testing.T) {
	app, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	ref := MobileBriefRef{Ref: "memory", SourceKind: "memory", SourceID: "safe-recall"}
	one := MobileBriefV1{Evidence: []MobileBriefRef{ref}}
	sealedAt := time.Now()
	_, value, supported, found := app.mobileBriefEditableSource(t.Context(), ref)
	if !supported || !found {
		t.Fatal("missing memory fixture")
	}
	oldPrint := map[string]string{mobileBriefSourceKey(ref.SourceKind, ref.SourceID): mobileBriefModelDigest(value)}
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	later := sealedAt.Add(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "safe-recall.md"), later, later); err != nil {
		t.Fatal(err)
	}
	if kind, _ := app.mobileBriefEditedSources(one, sealedAt, oldPrint); kind != "" {
		t.Fatalf("matching legacy fingerprint withheld source: %s", kind)
	}
	if kind, _ := app.mobileBriefEditedSources(one, sealedAt, nil); kind != "memory" {
		t.Fatalf("legacy seal without fingerprints ignored mtime: %s", kind)
	}
	writePrivacyMemory(t, "safe-recall", "# Different title\n\nDifferent text.\n")
	if kind, _ := app.mobileBriefEditedSources(one, sealedAt, oldPrint); kind != "memory" {
		t.Fatalf("changed legacy fingerprint was shown: %s", kind)
	}
	_ = source
}

func TestMobileBriefFingerprintPersonFields(t *testing.T) {
	app, svc, _ := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	person, err := app.store.CreatePrincipal(t.Context(), storage.PrincipalRow{ID: "fictional-person-fingerprint", Kind: storage.PrincipalKindContact, DisplayName: "Alex", Notes: "Working together"})
	if err != nil {
		t.Fatal(err)
	}
	ref := MobileBriefRef{Ref: "person", SourceKind: "principal", SourceID: person.ID}
	source := MobileBriefV1{Evidence: []MobileBriefRef{ref}}
	prints := app.mobileBriefSourceFingerprints(t.Context(), source)
	person.Confidence = 0.5
	if _, err := app.store.UpdatePrincipal(t.Context(), person); err != nil {
		t.Fatal(err)
	}
	if kind, _ := app.mobileBriefEditedSources(source, time.Now(), prints); kind != "" {
		t.Fatalf("person metadata withheld the brief: %s", kind)
	}
	person.Notes = "Changed notes"
	if _, err := app.store.UpdatePrincipal(t.Context(), person); err != nil {
		t.Fatal(err)
	}
	if kind, _ := app.mobileBriefEditedSources(source, time.Now(), prints); kind != "principal" {
		t.Fatalf("person edit was shown: %s", kind)
	}
}
