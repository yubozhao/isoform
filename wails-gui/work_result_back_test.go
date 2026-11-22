package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// workTestResultBack leaves a connected agent's result with two files on the
// plan that went to it, and returns the list after it came back.
func workTestResultBack(t *testing.T, app *App, recordID string) WorkList {
	t.Helper()
	workTestConnectedYes(t, app, recordID)
	dir, err := app.agentFilesDir(recordID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"test-plan.md", "cases.csv"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.rewriteTaskLinkResults(func(results []TaskLinkResult) []TaskLinkResult {
		return append(results, TaskLinkResult{LinkID: recordID, AgentID: "claude-code", Status: "done",
			Summary: "The test plan is ready: 14 cases. Two fail today, the ones Lena will ask about.",
			Files:   []string{"test-plan.md", "cases.csv"}, ResultAt: time.Now().UnixMilli(), Via: "inbox"})
	}); err != nil {
		t.Fatal(err)
	}
	list, err := app.buildWorkList(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A result back from a hand-off is an item with the one sentence of what it
// made, its summary for the grey box, both files with where they are and an
// id Open takes, and the reasons a Not useful may give (computer-today
// v1.1.0 rules 4 and 5).
func TestWorkResultBackCarriesSentenceSummaryAndFiles(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	const recordID = "wo_result_back"
	back := workTestItem(t, workTestResultBack(t, app, recordID), workTestPlanID)
	if back.State != workStateBack || back.Kind != workKindResult || back.Row != "result" {
		t.Fatalf("the result = %+v", back)
	}
	if back.Noticed != "The test plan is ready: 14 cases." {
		t.Fatalf("noticed = %q, want the summary's first sentence", back.Noticed)
	}
	if back.Work.Text != "The test plan is ready: 14 cases. Two fail today, the ones Lena will ask about." {
		t.Fatalf("summary = %q", back.Work.Text)
	}
	if len(back.Files) != 2 {
		t.Fatalf("files = %+v", back.Files)
	}
	for i, name := range []string{"test-plan.md", "cases.csv"} {
		f := back.Files[i]
		if f.Name != name || f.Where != workFileWhere || f.ID != workFileID(recordID, name) || !f.Readable {
			t.Fatalf("file %d = %+v", i, f)
		}
		if got, ok := workItemFile(back, f.ID); !ok || got.Name != name {
			t.Fatalf("Open can't find %s", name)
		}
	}
	if !reflect.DeepEqual(back.Reasons, workReasonsResult) {
		t.Fatalf("reasons = %v", back.Reasons)
	}
}

// The result's JSON names stay what the phone and the computer read
// (PROTOCOL.md 26.6): noticed, work.text, files[].id/name/where/readable,
// reasons.
func TestWorkResultBackJSONShape(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	back := workTestItem(t, workTestResultBack(t, app, "wo_result_shape"), workTestPlanID)
	raw, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		Noticed string `json:"noticed"`
		Work    struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"work"`
		Files   []map[string]any `json:"files"`
		Reasons []string         `json:"reasons"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	if shape.Noticed == "" || shape.Work.Text == "" || len(shape.Reasons) != 3 || len(shape.Files) != 2 {
		t.Fatalf("shape = %s", raw)
	}
	for _, f := range shape.Files {
		for _, key := range []string{"id", "name", "where", "readable"} {
			if _, ok := f[key]; !ok {
				t.Fatalf("file has no %q: %s", key, raw)
			}
		}
	}
}

// A local task's result with no summary still says it is ready, and carries
// the result reasons.
func TestWorkMarkBackWithoutSummary(t *testing.T) {
	item := WorkItem{Work: WorkBody{Title: "Q4 outline"}, Reasons: workReasonsOffered}
	workMarkBack(&item, "  ", nil)
	if item.Noticed != "Q4 outline is ready." || item.Work.Text != "" || item.State != workStateBack || !reflect.DeepEqual(item.Reasons, workReasonsResult) {
		t.Fatalf("item = %+v", item)
	}
}

// Not useful takes one of the result's reasons, which is kept and words the
// answer line; a reason not on the item, or one on Useful, is refused.
func TestWorkNotUsefulTakesAResultReason(t *testing.T) {
	for _, c := range []struct {
		name, action, reason string
		ok                   bool
	}{
		{"missed the point", workActionNotUseful, "missed_point", true},
		{"no reason", workActionNotUseful, "", true},
		{"not a result reason", workActionNotUseful, "not_now", false},
		{"useful with a reason", workActionUseful, "wrong_facts", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			app, _ := workAnswersTestApp(t)
			workTestResultBack(t, app, "wo_result_reason")
			item, err := app.AnswerWork(workTestPlanID, WorkAnswerInput{Action: c.action, Reason: c.reason})
			if !c.ok {
				if !errors.Is(err, errWorkAnswerInvalid) {
					t.Fatalf("err = %v, want invalid", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if item.State != workStateAnswered || item.Answer == nil || item.Answer.Reason != c.reason {
				t.Fatalf("answered = %+v", item)
			}
			if c.reason == "missed_point" && item.Answer.Line != "Plan the Q4 outline: not useful, missed the point" {
				t.Fatalf("line = %q", item.Answer.Line)
			}
			standing := answersStanding(app.answersLedger().entries())[workTestPlanID]
			if standing.Action != workActionNotUseful || standing.Reason != c.reason {
				t.Fatalf("ledger = %+v", standing)
			}
		})
	}
}
