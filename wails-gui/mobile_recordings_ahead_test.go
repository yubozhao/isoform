package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 2026-11-19: four parts were whole on the relay for five hours, because the
// phone had to be awake once more after each upload to say so. A part told
// ahead of its upload is kept and fetched when the relay has it.
func TestAPartToldAheadIsFetchedWhenTheRelayHasIt(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	relays := mobileRecordingRelays{primaryName: mobileRecordingRelayPinned, primary: uploads}
	bridge := &mobileBridge{}
	paired := map[string]bool{"dev-a": true}
	now := time.Now()
	recordingID := uuid.NewString()
	audio := []byte("a part of a recording")
	req, sealed := makeSealedRecordingPart(t, recordingTestHub, "dev-a", recordingID, 0, false, audio, now.Add(-time.Minute))
	req.Ahead = true
	if err := store.expect("dev-a", req, now); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(store.expectedPath()); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("expected.json %v %v", info, err)
	}
	file := filepath.Join(store.dir, recordingID, "part-00.m4a")

	// The upload is not on the relay yet: nothing is stored, and the part stays expected.
	if taken := bridge.takeExpectedParts(relays, store, recordingTestHub, paired, now); taken != 0 {
		t.Fatalf("took %d parts from an empty relay", taken)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("a part was stored before its upload arrived")
	}
	if len(store.expected()) != 1 {
		t.Fatalf("expected parts: %+v", store.expected())
	}

	// Told again, it is kept once.
	if err := store.expect("dev-a", req, now); err != nil || len(store.expected()) != 1 {
		t.Fatalf("told twice: %v %+v", err, store.expected())
	}

	// The upload arrives with the phone asleep. The computer takes it, and only
	// then is the relay's copy removed.
	uploads.bodies[req.Upload.ID] = sealed
	if taken := bridge.takeExpectedParts(relays, store, recordingTestHub, paired, now.Add(time.Minute)); taken != 1 {
		t.Fatalf("took %d parts", taken)
	}
	if raw, err := os.ReadFile(file); err != nil || string(raw) != string(audio) {
		t.Fatalf("stored part: %q %v", raw, err)
	}
	if len(uploads.deleted) != 1 || uploads.deleted[0] != req.Upload.ID {
		t.Fatalf("relay copy: %v", uploads.deleted)
	}
	if len(store.expected()) != 0 {
		t.Fatalf("still expected: %+v", store.expected())
	}
	if _, err := os.Stat(store.expectedPath()); !os.IsNotExist(err) {
		t.Fatal("an empty expected.json was left")
	}
}

func TestAnExpectedPartIsDroppedWithoutTouchingTheRelay(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		announced time.Time
		paired    map[string]bool
		body      func(sealed []byte) []byte
	}{
		// The phone was unpaired: nothing of it is fetched.
		{"unpaired", now, map[string]bool{}, func(sealed []byte) []byte { return sealed }},
		// Older than a relay keeps an upload.
		{"too old", now.Add(-mobileRecordingExpectedKeep), map[string]bool{"dev-a": true}, func(sealed []byte) []byte { return sealed }},
		// The upload is not this part: the phone still has the part and sends it again.
		{"another body", now, map[string]bool{"dev-a": true}, func(sealed []byte) []byte { return append([]byte{1}, sealed...) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMobileRecordingStore(t.TempDir())
			uploads := &mapUploads{bodies: map[string][]byte{}}
			relays := mobileRecordingRelays{primaryName: mobileRecordingRelayPinned, primary: uploads}
			recordingID := uuid.NewString()
			req, sealed := makeSealedRecordingPart(t, recordingTestHub, "dev-a", recordingID, 0, false, []byte("audio"), now.Add(-time.Minute))
			req.Ahead = true
			uploads.bodies[req.Upload.ID] = tc.body(sealed)
			if err := store.expect("dev-a", req, tc.announced); err != nil {
				t.Fatal(err)
			}
			if taken := (&mobileBridge{}).takeExpectedParts(relays, store, recordingTestHub, tc.paired, now); taken != 0 {
				t.Fatalf("took %d parts", taken)
			}
			if len(store.expected()) != 0 {
				t.Fatalf("still expected: %+v", store.expected())
			}
			if _, err := os.Stat(filepath.Join(store.dir, recordingID, "part-00.m4a")); !os.IsNotExist(err) {
				t.Fatal("a part was stored")
			}
			if len(uploads.deleted) != 0 || len(uploads.bodies) != 1 {
				t.Fatalf("the relay's copy was touched: deleted %v", uploads.deleted)
			}
		})
	}
}

func TestExpectedPartsAreBounded(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	now := time.Now()
	recordingID := uuid.NewString()
	for part := 0; part < mobileRecordingExpectedMax+3; part++ {
		req := MobileRecordingRequest{RecordingID: recordingID, Part: part}
		if err := store.expect("dev-a", req, now.Add(time.Duration(part)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	kept := store.expected()
	if len(kept) != mobileRecordingExpectedMax || kept[0].Request.Part != 3 {
		t.Fatalf("kept %d, first part %d", len(kept), kept[0].Request.Part)
	}
}

// The log shows the expected list drain: one line for a pass that took or
// dropped something, none for a pass that only waited.
func TestAPassOverExpectedPartsSaysWhatItDid(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	relays := mobileRecordingRelays{primaryName: mobileRecordingRelayPinned, primary: uploads}
	paired := map[string]bool{"dev-a": true}
	now := time.Now()
	recordingID := uuid.NewString()
	var here []byte
	for part := 0; part < 2; part++ {
		req, sealed := makeSealedRecordingPart(t, recordingTestHub, "dev-a", recordingID, part, false, []byte("audio"), now.Add(-time.Minute))
		if part == 0 {
			here = sealed
			uploads.bodies[req.Upload.ID] = here
		}
		if err := store.expect("dev-a", req, now); err != nil {
			t.Fatal(err)
		}
	}
	logs := &lockedLog{}
	previous := log.Writer()
	log.SetOutput(logs)
	defer log.SetOutput(previous)
	read := func() string {
		logs.mu.Lock()
		defer logs.mu.Unlock()
		text := logs.b.String()
		logs.b.Reset()
		return text
	}
	bridge := &mobileBridge{}
	if taken := bridge.takeExpectedParts(relays, store, recordingTestHub, paired, now); taken != 1 {
		t.Fatalf("took %d", taken)
	}
	if got, want := read(), "[recordings] expected parts: took 1, dropped 0, 1 still expected"; !strings.Contains(got, want) {
		t.Fatalf("log %q lacks %q", got, want)
	}
	// Only waiting: no line.
	bridge.takeExpectedParts(relays, store, recordingTestHub, paired, now)
	if got := read(); strings.Contains(got, "expected parts:") {
		t.Fatalf("a pass that only waited wrote %q", got)
	}
	// The last one grows too old.
	bridge.takeExpectedParts(relays, store, recordingTestHub, paired, now.Add(mobileRecordingExpectedKeep))
	if got, want := read(), "[recordings] expected parts: took 0, dropped 1, 0 still expected"; !strings.Contains(got, want) {
		t.Fatalf("log %q lacks %q", got, want)
	}
}
