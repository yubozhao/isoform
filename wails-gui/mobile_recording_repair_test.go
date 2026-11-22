package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The case of 2026-11-18: the phone paired again and got a new device id. The
// computer held the first parts of its recordings under the old id and
// answered "the recording is not valid" to every part that followed, so hours
// of audio stayed on the phone. A recording whose device is no longer paired
// passes to the paired phone that names it.
func TestARecordingPassesToThePhoneThatPairedAgain(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	now := time.Now()
	store.now = func() time.Time { return now }
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	var uploadID string
	send := func(device string, part int, last bool) (MobileRecordingView, bool, error) {
		t.Helper()
		req := sealedRecordingPart(t, uploads, device, recordingID, part, last, []byte{byte(part), 1, 2}, now.Add(-time.Hour))
		uploadID = req.Upload.ID
		return receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, device, req)
	}
	store.setPaired([]string{"dev-old"})
	for part := 0; part < 2; part++ {
		if _, _, err := send("dev-old", part, false); err != nil {
			t.Fatal(err)
		}
	}
	// Closed and transcribed while the phone could not send, as on 2026-11-14.
	now = now.Add(mobileRecordingAbandonedAfter + time.Minute)
	if pending := store.pending(); len(pending) != 1 {
		t.Fatalf("not closed after the wait: %+v", pending)
	}
	store.setState(recordingID, mobileRecordingTranscribed, "rec-"+recordingID, 10, "")
	if _, _, err := send("dev-old", 2, false); err != nil {
		t.Fatalf("late part before the re-pair: %v", err)
	}
	continuation := mobileRecordingContinuationID(recordingID, 2)

	// While the old pairing stands, another phone cannot add to the recording
	// or see it.
	store.setPaired([]string{"dev-old", "dev-other"})
	if _, _, err := send("dev-other", 3, false); !errors.Is(err, errMobileRecordingInvalid) {
		t.Fatalf("another paired phone added a part: %v", err)
	}
	otherUpload := uploadID
	if views, err := store.list("dev-other", []string{recordingID}, 10, false); err != nil || len(views) != 0 {
		t.Fatalf("another paired phone sees the recording: %+v %v", views, err)
	}

	// The phone pairs again: the old device is gone, a new one is paired.
	store.setPaired([]string{"dev-new"})
	// A phone that is not paired gets nothing.
	if _, _, err := send("dev-stranger", 3, false); !errors.Is(err, errMobileRecordingInvalid) {
		t.Fatalf("an unpaired device added a part: %v", err)
	}
	unpairedUpload := uploadID
	// Without naming it, the new pairing does not see the old pairing's recordings.
	if views, err := store.list("dev-new", nil, 10, false); err != nil || len(views) != 0 {
		t.Fatalf("recordings passed without being named: %+v %v", views, err)
	}
	view, complete, err := send("dev-new", 3, true)
	if err != nil || !complete {
		t.Fatalf("part after pairing again refused: %+v complete=%v err=%v", view, complete, err)
	}
	if view.ID != continuation || view.Parts != 2 {
		t.Fatalf("part stored in %q with %d parts, want the continuation %q with 2", view.ID, view.Parts, continuation)
	}
	if len(uploads.bodies) != 2 || uploads.bodies[otherUpload] == nil || uploads.bodies[unpairedUpload] == nil {
		t.Fatalf("expected only the two refused uploads on the relay, got %d", len(uploads.bodies))
	}
	// The recording and its continuation are the new pairing's now, on disk too.
	reopened := newMobileRecordingStore(store.dir[:len(store.dir)-len("/recordings")])
	reopened.setPaired([]string{"dev-new"})
	views, err := reopened.list("dev-new", nil, 10, false)
	if err != nil || len(views) != 2 {
		t.Fatalf("after pairing again the phone lists %d recordings, want 2: %v", len(views), err)
	}
	// A repeat of a part the old pairing sent is answered as stored, not fetched again.
	if _, _, err := send("dev-new", 0, false); err != nil {
		t.Fatalf("repeat of an old part: %v", err)
	}
}

// Asking for a recording by id passes it too, so the phone's list shows where
// its earlier recordings are after pairing again.
func TestListingByIDPassesARecordingOfAnUnpairedDevice(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	store.setPaired([]string{"dev-old"})
	req := sealedRecordingPart(t, uploads, "dev-old", recordingID, 0, true, []byte{1, 2, 3}, time.Now().Add(-time.Hour))
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-old", req); err != nil {
		t.Fatal(err)
	}
	// Before the store is told who is paired, nothing passes.
	fresh := newMobileRecordingStore(store.dir[:len(store.dir)-len("/recordings")])
	if views, _ := fresh.list("dev-new", []string{recordingID}, 10, false); len(views) != 0 {
		t.Fatal("a recording passed before the paired devices were known")
	}
	store.setPaired([]string{"dev-new"})
	views, err := store.list("dev-new", []string{recordingID}, 10, false)
	if err != nil || len(views) != 1 || views[0].ID != recordingID {
		t.Fatalf("named recording not listed after pairing again: %+v %v", views, err)
	}
	if views, _ := store.list("dev-new", nil, 10, false); len(views) != 1 {
		t.Fatal("the passed recording must stay with the new pairing")
	}
}
