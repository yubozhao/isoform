package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeWhyBelief(t *testing.T, content string) {
	t.Helper()
	dir, _ := memoryDir()
	if err := os.WriteFile(filepath.Join(dir, "belief.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// A claim's Why is its moments by day and source, titles only, through the
// same gates as the detail pane.
func TestGetWhyClaimMomentsByDay(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	day := time.Date(2026, 11, 20, 10, 0, 0, 0, time.Local).UnixMilli()
	readableEvidenceMoment(t, app, "a", "Editor", "com.example.editor", day)
	readableEvidenceMoment(t, app, "b", "Editor", "com.example.editor", day-24*time.Hour.Milliseconds())
	if _, err := app.activity.store.db.Exec(`UPDATE activities SET title='Wrote the spec' WHERE id='moment-b'`); err != nil {
		t.Fatal(err)
	}
	writeWhyBelief(t, renderOKFDocument(map[string]any{"type": "Isoform Memory", "title": "Plans first", "evidence_sessions": []string{"a", "b"}}, "Plans first."))
	view, err := app.GetWhy(whyKindClaim, "belief")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Days) != 2 || view.Days[0].Day != "2026-11-20" || view.Days[1].Day != "2026-11-19" {
		t.Fatalf("days = %+v", view.Days)
	}
	first := view.Days[0].Moments[0]
	if first.ID != "a" || first.Source != whySourceScreen || first.Title != "Reviewed the plan" || first.App != "Editor" || first.ActivitySessionID != "a" || !first.CanNotThis {
		t.Fatalf("moment = %+v", first)
	}
	if view.Summary.Total != 2 || view.Summary.BySource[whySourceScreen] != 2 {
		t.Fatalf("summary = %+v", view.Summary)
	}
	raw, _ := json.Marshal(view)
	for _, secret := range []string{"OCR_ONLY_SECRET", "AX_ONLY_SECRET", "PARSED_ONLY_SECRET"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("captured text leaked: %s", raw)
		}
	}
}

// Closed Eyes and protected apps hide every moment, the history ones too.
func TestGetWhyClaimHidesClosedEyesAndProtected(t *testing.T) {
	for _, gate := range []string{"closed_eyes", "protected"} {
		t.Run(gate, func(t *testing.T) {
			app := newPrivacyReadTestApp(t)
			readableEvidenceMoment(t, app, "a", "Editor", "com.example.editor", 100000)
			writeWhyBelief(t, renderOKFDocument(map[string]any{"type": "Isoform Memory", "title": "Plans first", "source": assistantHistorySource,
				"history_assistant": "claude", "history_session_at": "2026-11-19T10:00:00Z", "evidence_sessions": []string{"a"}}, "Plans first."))
			switch gate {
			case "closed_eyes":
				app.closedEyes.enabled.Store(true)
			case "protected":
				if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='a'`); err != nil {
					t.Fatal(err)
				}
			}
			view, err := app.GetWhy(whyKindClaim, "belief")
			if err != nil {
				t.Fatal(err)
			}
			for _, day := range view.Days {
				for _, moment := range day.Moments {
					if moment.Source == whySourceScreen || gate == "closed_eyes" {
						t.Fatalf("%s leaked %+v", gate, moment)
					}
				}
			}
		})
	}
}

// Not this takes one moment off the claim, keeps the claim, and is kept in
// the ledger under the claim's moment, not as the claim's answer.
func TestNotThisTakesOneMomentOff(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	t.Cleanup(func() { answersLedgers.Delete(app) })
	readableEvidenceMoment(t, app, "a", "Editor", "com.example.editor", 200000)
	readableEvidenceMoment(t, app, "b", "Editor", "com.example.editor", 100000)
	if _, err := app.activity.store.db.Exec(`UPDATE activities SET title='Wrote the spec' WHERE id='moment-b'`); err != nil {
		t.Fatal(err)
	}
	writeWhyBelief(t, renderOKFDocument(map[string]any{"type": "Isoform Memory", "title": "Plans first", "activity_sessions": []string{"a", "b"}, "evidence_sessions": []string{"a", "b"}}, "Plans first."))
	if err := app.NotThis(whyKindClaim, "belief", "a"); err != nil {
		t.Fatal(err)
	}
	view, err := app.GetWhy(whyKindClaim, "belief")
	if err != nil || view.Summary.Total != 1 || view.Days[0].Moments[0].ID != "b" {
		t.Fatalf("after Not this = %+v %v", view, err)
	}
	entry, err := app.GetMemoryEntry("belief")
	if err != nil || !strings.Contains(entry.Content, "activity_sessions") {
		t.Fatalf("privacy provenance lost: %v\n%s", err, entry.Content)
	}
	if err := app.NotThis(whyKindClaim, "belief", "b"); err != nil {
		t.Fatal(err)
	}
	if view, _ := app.GetWhy(whyKindClaim, "belief"); view.Summary.Total != 0 {
		t.Fatalf("moments left = %+v", view)
	}
	if _, err := app.GetMemoryEntry("belief"); err != nil {
		t.Fatalf("the claim went with its moments: %v", err)
	}
	var notThis int
	for _, record := range app.answersLedger().entries() {
		if record.Action == answerActionNotThis && record.Kind == answerKindBelief && record.ItemID == "belief:belief#moment" {
			notThis++
		}
	}
	if notThis != 2 {
		t.Fatalf("Not this kept %d times", notThis)
	}
	if _, standing := app.beliefAnswers()["belief"]; standing {
		t.Fatal("Not this stood as the claim's answer")
	}
	if err := app.NotThis(whyKindClaim, "belief", "missing"); !errors.Is(err, errWhyMomentNotFound) {
		t.Fatalf("unknown moment = %v", err)
	}
	if err := app.NotThis(whyKindWork, "w1", "a"); !errors.Is(err, errWhyWorkNotYet) {
		t.Fatalf("work Not this = %v", err)
	}
	if _, err := app.GetWhy("nope", "belief"); !errors.Is(err, errWhyKind) {
		t.Fatalf("unknown kind = %v", err)
	}
}

func TestMemoryWithoutEvidenceSessionLegacy(t *testing.T) {
	single := "# Title\n- **Source**: activity\n- **Activity Session**: a\n\nBody\n"
	got, changed := memoryWithoutEvidenceSession(single, "a")
	if !changed || strings.Contains(got, "Activity Session") || !strings.Contains(got, "Body") {
		t.Fatalf("single = %q", got)
	}
	nested := "# Title\n- **Activity Session**:\n  - **Update 2026-04-20 11:30** (initial)\n    - a — 10:28, Editor\n    - b — 10:40, Editor\n\nBody\n"
	got, changed = memoryWithoutEvidenceSession(nested, "a")
	if !changed || strings.Contains(got, "- a —") || !strings.Contains(got, "- b —") {
		t.Fatalf("nested = %q", got)
	}
	if ids := parseMemoryFrontmatter(got).ActivitySessionIDs; len(ids) != 1 || ids[0] != "b" {
		t.Fatalf("ids = %v", ids)
	}
	confirmed := "---\nconfirmed_at: 2026-11-01T10:00:00Z\n---\n" + renderOKFDocument(map[string]any{"type": "Isoform Memory", "title": "T", "evidence_sessions": []string{"a", "b"}}, "Body")
	got, changed = memoryWithoutEvidenceSession(confirmed, "b")
	if !changed || memoryConfirmedAt(got) == 0 {
		t.Fatalf("confirmation lost: %q", got)
	}
	if ids := memoryEvidenceSessionIDs(legacyMemoryWithoutConfirmation(got)); len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestWhyViewGroupsByDayAndCountsThisWeek(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local)
	view := whyView(whyKindWork, "w", []WhyMoment{
		{ID: "old", At: now.AddDate(0, 0, -10).UnixMilli(), Source: whySourceMail},
		{ID: "new", At: now.UnixMilli(), Source: whySourceCall},
		{ID: "new2", At: now.Add(-time.Hour).UnixMilli(), Source: whySourceMail},
	}, now)
	if len(view.Days) != 2 || len(view.Days[0].Moments) != 2 || view.Days[0].Moments[0].ID != "new" {
		t.Fatalf("days = %+v", view.Days)
	}
	if view.Summary.ThisWeek != 2 || view.Summary.BySource[whySourceMail] != 2 || view.Summary.BySource[whySourceCall] != 1 {
		t.Fatalf("summary = %+v", view.Summary)
	}
}

// Not this is a correction on a moment, never an answer on an item made
// (redesign v1.0.0 rule 30): it leaves Answered at 0.
func TestJudgmentStatsCountNotThisApart(t *testing.T) {
	now := time.Now()
	entries := []AnswerRecord{{EntryID: "e1", ItemID: "belief:b#moment", Kind: answerKindBelief, Source: answerSourceAboutYou, Action: answerActionNotThis, At: now.UTC().Format(time.RFC3339)}}
	stats := judgmentStatsAt(entries, 1, now)
	if got := stats.Days[0].Total; got.Answered != 0 || got.MomentCorrections != 1 {
		t.Fatalf("total = %+v", got)
	}
	if got := stats.Days[0].BySource[answerSourceAboutYou]; got.Answered != 0 || got.MomentCorrections != 1 {
		t.Fatalf("about you = %+v", got)
	}
}
