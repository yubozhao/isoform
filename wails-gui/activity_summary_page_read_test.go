package main

import (
	"testing"
	"time"
)

// The Activity page read leaves out the model-only context: scanning every
// OCR frame and window event of the day for keywords no screen shows made
// the first Activity paint take 4 to 7 s. Model and API callers still get it.
func TestDailySummaryPageReadSkipsModelOnlyContext(t *testing.T) {
	now := time.Now()
	date := now.Format("2006-01-02")
	store := newTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	seedActivitySummaryI18nSession(t, store, "page-read-1", date, 1, []string{"Code"})
	for _, text := range []string{"quarterly roadmap planning", "quarterly roadmap review", "roadmap quarterly notes"} {
		if _, err := store.db.Exec(`INSERT INTO activity_ocr_frames(session_id,segment_id,frame_time,text) VALUES('page-read-1',0,1,?)`, text); err != nil {
			t.Fatal(err)
		}
	}

	page, err := app.GetDailySummary(date)
	if err != nil {
		t.Fatalf("GetDailySummary: %v", err)
	}
	if len(page.TopOCRKeywords) != 0 || len(page.TopWindowTitles) != 0 {
		t.Fatalf("page read computed model-only context: keywords=%v titles=%v", page.TopOCRKeywords, page.TopWindowTitles)
	}
	if page.OCRPageCount != 3 || page.SessionCount != 1 {
		t.Fatalf("page read lost what the page shows: ocr pages=%d sessions=%d", page.OCRPageCount, page.SessionCount)
	}

	model, err := app.dailySummaryWithModelContext(date)
	if err != nil {
		t.Fatalf("dailySummaryWithModelContext: %v", err)
	}
	if len(model.TopOCRKeywords) == 0 {
		t.Fatal("model read lost the OCR keywords")
	}
}
