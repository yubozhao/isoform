package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The case of 2026-11-16: a long recording whose first parts arrived, then
// nothing for more than six hours, so the computer closed and transcribed it.
// The parts that follow must be taken, not refused.
func TestPartsAfterAClosedRecordingAreKeptAsItsContinuation(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	now := time.Now()
	store.now = func() time.Time { return now }
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	send := func(part int, last bool) (MobileRecordingView, bool, error) {
		t.Helper()
		req := sealedRecordingPart(t, uploads, "dev-a", recordingID, part, last, []byte{byte(part), 1, 2}, now.Add(-time.Hour))
		return receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req)
	}
	for part := 0; part < 2; part++ {
		if _, _, err := send(part, false); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(mobileRecordingAbandonedAfter + time.Minute)
	if pending := store.pending(); len(pending) != 1 || pending[0].Parts != 2 {
		t.Fatalf("not closed after the wait: %+v", pending)
	}
	store.setState(recordingID, mobileRecordingTranscribed, "rec-"+recordingID, 10, "")

	// Parts 2 and 3 arrive the next day; 3 is the last.
	view, complete, err := send(2, false)
	if err != nil || complete {
		t.Fatalf("late part refused: %+v complete=%v err=%v", view, complete, err)
	}
	continuation := mobileRecordingContinuationID(recordingID, 2)
	if view.ID != continuation || !mobileValidUUID(continuation) {
		t.Fatalf("late part stored in %q, want the continuation %q", view.ID, continuation)
	}
	if view, complete, err = send(3, true); err != nil || !complete || view.Parts != 2 {
		t.Fatalf("last late part: %+v complete=%v err=%v", view, complete, err)
	}
	if len(uploads.bodies) != 0 {
		t.Fatalf("relay copies left: %d", len(uploads.bodies))
	}
	for _, name := range []string{"part-00.m4a", "part-01.m4a"} {
		if _, err := os.Stat(filepath.Join(store.dir, continuation, name)); err != nil {
			t.Fatalf("continuation audio %s: %v", name, err)
		}
	}
	// The continuation is an ordinary recording waiting to be transcribed; the
	// first one is untouched.
	pending := store.pending()
	if len(pending) != 1 || pending[0].ID != continuation || pending[0].ContinuationOf != recordingID || pending[0].FirstPart != 2 {
		t.Fatalf("pending = %+v", pending)
	}
	if first, ok := store.get(recordingID, "dev-a"); !ok || first.State != mobileRecordingTranscribed || len(first.Received) != 2 {
		t.Fatalf("first recording changed: %+v", first)
	}
	// A repeat of a late part is answered from the index, with nothing fetched.
	if view, found, err := store.has("dev-a", recordingID, 3); err != nil || !found || view.ID != continuation {
		t.Fatalf("repeat of a late part: %+v found=%v err=%v", view, found, err)
	}
	if _, found, _ := store.has("dev-a", recordingID, 4); found {
		t.Fatal("a part that never came is reported as stored")
	}
	// The continuation closed too; what comes after it opens the next one.
	if view, _, err := send(4, true); err != nil || view.ID != mobileRecordingContinuationID(recordingID, 4) {
		t.Fatalf("part after the continuation: %+v err=%v", view, err)
	}
}

// Deleted should mean deleted: deleting the recording takes the parts that
// arrived after it was closed, and a late part of a deleted recording is
// never stored.
func TestDeletingARecordingTakesItsContinuation(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	now := time.Now()
	store.now = func() time.Time { return now }
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	send := func(part int, last bool) error {
		t.Helper()
		req := sealedRecordingPart(t, uploads, "dev-a", recordingID, part, last, []byte{byte(part), 9}, now.Add(-time.Hour))
		_, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req)
		return err
	}
	if err := send(0, true); err != nil {
		t.Fatal(err)
	}
	if err := send(1, false); err != nil {
		t.Fatal(err)
	}
	continuation := mobileRecordingContinuationID(recordingID, 1)
	app := &App{}
	if _, err := app.deleteWatchRecording(store, recordingID, "dev-a"); err != nil {
		t.Fatal(err)
	}
	if rec, ok := store.get(continuation, "dev-a"); !ok || rec.State != mobileRecordingDeleted {
		t.Fatalf("continuation not deleted: %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(store.dir, continuation)); !os.IsNotExist(err) {
		t.Fatalf("continuation audio left: %v", err)
	}
	before := len(uploads.deleted)
	if err := send(2, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.dir, recordingID)); !os.IsNotExist(err) {
		t.Fatal("a late part of a deleted recording was stored")
	}
	if len(uploads.deleted) != before+1 {
		t.Fatal("the relay copy of a late part of a deleted recording was left")
	}
}

// A refused part's copy must not stay on the relay: 48 of them filled the
// phone's quota there and stopped every later recording.
func TestARefusedPartIsRemovedFromTheRelay(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	req := sealedRecordingPart(t, uploads, "dev-a", uuid.NewString(), 0, false, []byte("audio"), time.Now().Add(-time.Minute))
	req.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); !errors.Is(err, errMobileRecordingSHA) {
		t.Fatalf("err = %v", err)
	}
	if len(uploads.bodies) != 0 {
		t.Fatal("the refused copy is still on the relay")
	}
}
