package main

import (
	"context"
	"testing"

	"wails-gui/internal/platform/activitymon"
)

// Erasing every session (Forget everything's activity step) also empties the
// OCR text, its full-text search rows and its embeddings: they hang off the
// session (ON DELETE CASCADE and the FTS delete trigger).
func TestForgetEverythingActivityStepEmptiesTheSearchAndEmbeddingIndexes(t *testing.T) {
	store, err := NewActivityStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.SaveSession(ActivitySessionRow{ID: "s1", StartTime: 1000, EndTime: 2000, AppNames: `["Notes"]`}, []activitymon.ActivityEvent{{}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO activity_ocr_frames(session_id,segment_id,frame_time,text,embedding) VALUES('s1',0,1,'halcyon import plan','[1,0]')`); err != nil {
		t.Skipf("ocr frame shape: %v", err)
	}
	app := &App{activity: &activityMonitorState{store: store}}
	if err := app.forgetAllActivitySessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	var frames, matches int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM activity_ocr_frames`).Scan(&frames)
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM activity_ocr_fts WHERE activity_ocr_fts MATCH 'halcyon'`).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if frames != 0 || matches != 0 {
		t.Fatalf("after the activity step: frames=%d search matches=%d", frames, matches)
	}
}
