package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
)

func TestBriefMissingLedgerProofDistinguishesUnavailableStore(t *testing.T) {
	dir := t.TempDir()
	store := newMobileBriefStore(dir)
	if err := os.MkdirAll(store.dir, 0700); err != nil {
		t.Fatal(err)
	}
	proofs := []morningBriefLedgerProof{{BriefID: "2026-10-29-morning"}}
	if !morningBriefMissingLedgerProof(store, proofs) {
		t.Fatal("missing historical brief was not recognized")
	}
	path := filepath.Join(store.dir, proofs[0].BriefID+".json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if morningBriefMissingLedgerProof(store, proofs) {
		t.Fatal("existing historical brief was reported missing")
	}
	if morningBriefMissingLedgerProof(newMobileBriefStore(filepath.Join(dir, "unavailable")), proofs) {
		t.Fatal("unavailable store was reported as a missing proof")
	}
}

func TestMorningBriefLedgerComparesAuthorizedEditsWithoutOldText(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	store, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.handoffStore = store
	now := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	yesterday := now.AddDate(0, 0, -1)
	entry := handoff.Entry{ID: "work", Title: "Old private wording", Source: handoff.SourceMobile, Status: handoff.StatusBlocked, CreatedAt: now.AddDate(0, 0, -7).UnixMilli(), UpdatedAt: yesterday.Add(-time.Hour).UnixMilli()}
	if _, err := store.Save(entry); err != nil {
		t.Fatal(err)
	}
	oldInput := mobileBriefInput{AsOf: yesterday, Location: time.UTC}
	collectMobileBriefWork(app, &oldInput)
	oldRows := morningBriefGatherCandidates(oldInput)
	bound := app.morningBriefBindRankedInputs(t.Context(), yesterday, oldRows)
	if len(bound) != 1 || bound[0].OriginDigest == "" {
		t.Fatalf("missing old source proof: %+v", bound)
	}
	binding := &mobileBriefModelBinding{Version: 2, RankedInputs: bound, RankedInputsDigest: mobileBriefModelDigest(bound)}
	old := MobileBriefV1{BriefID: "2026-10-29-morning", AsOf: formatMobileBriefTime(yesterday), Slot: mobileBriefSlotMorning, Timezone: "UTC", NarrativeStatus: "ok", rankedEdition: true, modelBinding: binding,
		Evidence:  []MobileBriefRef{{Ref: "e1", SourceKind: "history", SourceID: entry.ID, Label: entry.Title}},
		Narrative: []MobileBriefPara{{Text: "The work is blocked.", Refs: []string{"e1"}}}}
	binding.Synthesis = mobileBriefSynthesisDigest(old)
	briefStore := newMobileBriefStore(t.TempDir())
	if err := briefStore.save(old, now); err != nil {
		t.Fatal(err)
	}
	entry.Title, entry.Status, entry.UpdatedAt = "Current plan", handoff.StatusInProgress, now.Add(-time.Hour).UnixMilli()
	if _, err := store.Save(entry); err != nil {
		t.Fatal(err)
	}
	facts := app.morningBriefHistoricalLedger(t.Context(), []MobileBriefV1{old}, now)
	currentInput := mobileBriefInput{AsOf: now, Location: time.UTC}
	collectMobileBriefWork(app, &currentInput)
	current := morningBriefGatherCandidates(currentInput)
	rows, proofs := morningBriefApplyLedger(current, facts, now, time.UTC)
	if len(rows) != 1 || !rows[0].ChangeKnown || !rows[0].Changed || len(proofs) != 1 {
		t.Fatalf("authorized change was not detected: rows=%+v proofs=%+v", rows, proofs)
	}
	admitted := app.morningBriefBindRankedInputs(t.Context(), now, current)
	currentBrief := MobileBriefV1{AsOf: formatMobileBriefTime(now), Timezone: "UTC"}
	withLedger, boundProofs, ok := app.morningBriefBindRankedLedger(t.Context(), currentBrief, admitted, facts, briefStore)
	if !ok || len(withLedger) != 1 || !withLedger[0].Candidate.ChangeKnown || !withLedger[0].Candidate.Changed || len(boundProofs) != 1 {
		t.Fatalf("authorized diff was not bound: inputs=%+v proofs=%+v", withLedger, boundProofs)
	}
	if !app.morningBriefRankedInputsReadableWithLedger(t.Context(), currentBrief, withLedger, briefStore, boundProofs) ||
		app.morningBriefRankedInputsReadableWithLedger(t.Context(), currentBrief, withLedger, nil, nil) {
		t.Fatal("diff input did not require its historical proof")
	}
	incorrect := append([]morningBriefBoundInput(nil), withLedger...)
	incorrect[0].Candidate.Changed = false
	if app.morningBriefRankedInputsReadableWithLedger(t.Context(), currentBrief, incorrect, briefStore, boundProofs) {
		t.Fatal("fabricated diff retained a historical proof")
	}
	raw, err := json.Marshal(struct {
		Rows   []morningBriefCandidate   `json:"rows"`
		Proofs []morningBriefLedgerProof `json:"proofs"`
	}{rows, proofs})
	if err != nil || strings.Contains(string(raw), "Old private wording") {
		t.Fatalf("old text escaped the code-side comparison: %s, %v", raw, err)
	}
	if !app.morningBriefLedgerProofsReadable(t.Context(), briefStore, now, proofs) {
		t.Fatal("authorized historical proof was rejected")
	}
	app.mobileBriefSvc = &mobileBriefService{store: briefStore}
	currentBrief.BriefID, currentBrief.Slot, currentBrief.rankedEdition, currentBrief.NarrativeStatus = "2026-10-30-morning", mobileBriefSlotMorning, true, "ok"
	currentBrief.Evidence = []MobileBriefRef{{Ref: "e1", SourceKind: "history", SourceID: entry.ID, Label: entry.Title}}
	currentBrief.Narrative = []MobileBriefPara{{Text: "Review the current plan.", Refs: []string{"e1"}}}
	gathered, err := app.readMobileBriefGatherRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer gathered.close()
	currentBinding := app.captureRankedBriefModelSourcesWithLedger(t.Context(), currentBrief, gathered, withLedger, briefStore, boundProofs)
	if currentBinding == nil {
		t.Fatal("historical diff proof did not bind")
	}
	app.finishMobileBriefModelBinding(t.Context(), &currentBrief, currentBinding)
	if currentBrief.modelBinding == nil || !app.validateMobileBriefModelSources(t.Context(), currentBrief, currentBinding) {
		t.Fatal("historical diff binding did not validate")
	}
	if delivered, withheld := app.mobileBriefDelivery(currentBrief); len(delivered.Narrative) != 1 || withheld.Reason != "" {
		t.Fatalf("authorized diff synthesis was withheld: %+v, %+v", delivered.Narrative, withheld)
	}
	app.closedEyes.enabled.Store(true)
	if app.morningBriefLedgerProofsReadable(t.Context(), briefStore, now, proofs) {
		t.Fatal("historical proof bypassed Closed Eyes")
	}
	pausedCtx := context.WithValue(t.Context(), pausedBriefReadKey{}, true)
	if !app.morningBriefLedgerProofsReadable(pausedCtx, briefStore, now, proofs) {
		t.Fatal("selected brief could not recheck its historical proof while paused")
	}
	sources := app.collectTodayChatSources(pausedCtx, time.Now())
	if !app.validateMobileBriefModelSourcesFrom(pausedCtx, currentBrief, currentBinding, sources) {
		t.Fatal("selected brief could not recheck its ranked ledger while paused")
	}
	app.closedEyes.enabled.Store(false)
	tampered := old
	copied := *binding
	copied.RankedInputs = append([]morningBriefBoundInput(nil), binding.RankedInputs...)
	copied.RankedInputs[0].Candidate.Title = "Tampered"
	tampered.modelBinding = &copied
	if err := briefStore.save(tampered, now); err != nil {
		t.Fatal(err)
	}
	if app.morningBriefLedgerProofsReadable(t.Context(), briefStore, now, proofs) || app.validateMobileBriefModelSources(t.Context(), currentBrief, currentBinding) {
		t.Fatal("changed historical manifest retained its proof")
	}
	app.closedEyes.enabled.Store(true)
	if app.morningBriefLedgerProofsReadable(pausedCtx, briefStore, now, proofs) {
		t.Fatal("changed historical manifest survived paused proof")
	}
	app.closedEyes.enabled.Store(false)
	if delivered, _ := app.mobileBriefDelivery(currentBrief); len(delivered.Narrative) != 0 {
		t.Fatal("synthesis survived a changed historical proof")
	}
	if err := briefStore.save(old, now); err != nil {
		t.Fatal(err)
	}
	for i, clock := range []string{"050000", "060000", "070000"} {
		moreRecent := MobileBriefV1{BriefID: "2026-10-30-on_request-" + clock, AsOf: formatMobileBriefTime(now.Add(-time.Duration(3-i) * time.Hour)), Slot: mobileBriefSlotOnRequest, Timezone: "UTC"}
		if err := briefStore.save(moreRecent, now); err != nil {
			t.Fatal(err)
		}
	}
	if app.morningBriefLedgerProofsReadable(t.Context(), briefStore, now, proofs) {
		t.Fatal("an older brief replaced one of the last three")
	}
	app.closedEyes.enabled.Store(true)
	if len(app.morningBriefHistoricalLedger(t.Context(), []MobileBriefV1{old}, now)) != 0 {
		t.Fatal("historical comparison bypassed Closed Eyes")
	}
}
