package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMobileStatusAdvertisesRecordingRelay(t *testing.T) {
	bridge, _ := mobileTestChatBridge(t)
	response := watchDeskCall(bridge, http.MethodGet, "/status", "", mobileScopeBriefRead)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /status returned %d", response.Code)
	}
	var flags struct {
		RecordingRelay bool `json:"recordingRelay"`
		RecordingAhead bool `json:"recordingAhead"`
	}
	if err := json.Unmarshal(mobileTestDecodeReply(t, response).Data, &flags); err != nil {
		t.Fatal(err)
	}
	if !flags.RecordingRelay {
		t.Fatal("GET /status must advertise recordingRelay")
	}
	if !flags.RecordingAhead {
		t.Fatal("GET /status must advertise recordingAhead")
	}
}

// Both ways of telling the computer, against two real relays: a phone that
// tells ahead of its upload, and an older phone that does not.
func TestRecordingPartToldAheadOfItsUpload(t *testing.T) {
	if os.Getenv("YANSU_MOBILE_E2E") != "1" {
		t.Skip("set YANSU_MOBILE_E2E=1 to run with both relay binaries")
	}
	app, bridge, phone, pinned, _, invitation, _ := mobileDirectE2EPairedPhone(t)
	if _, err := app.SetWatchHeartRate(false); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SetWatchPlace(false); err != nil {
		t.Fatal(err)
	}
	audio := []byte{2, 7, 1, 8}
	part, sealed := makeSealedRecordingPart(t, invitation.Hub, phone.deviceID, uuid.NewString(), 0, false, audio, time.Now())
	part.Context = &MobileRecordingPartContext{
		HeartRate: &MobileRecordingHeartRate{Avg: 80, Min: 70, Max: 90, Samples: 5},
		Location:  &MobileRecordingLocation{Latitude: 10, Longitude: 20, Place: "Town"},
	}
	part.Marks = []float64{2}
	part.Upload.Relay = "pinned"
	local := filepath.Join(bridge.stateDir, "recordings", part.RecordingID, "part-00.m4a")
	post := func(ahead bool) (int, string) {
		part.Ahead = ahead
		body, err := json.Marshal(part)
		if err != nil {
			t.Fatal(err)
		}
		if !ahead && bytes.Contains(body, []byte(`"ahead"`)) {
			t.Fatal("an older phone's request contains the ahead field")
		}
		return phone.request(t, http.MethodPost, "/recordings", string(body))
	}
	state := func(body string) string {
		var reply struct {
			Data struct {
				State string `json:"state"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &reply)
		if reply.Data.State != "" {
			return reply.Data.State
		}
		var view struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal([]byte(body), &view)
		return view.State
	}

	// An older phone, nothing uploaded yet: 404 as before, nothing kept.
	if code, _ := post(false); code != http.StatusNotFound {
		t.Fatalf("an older phone before its upload: %d", code)
	}
	bridge.mu.Lock()
	store := bridge.recordingStoreLocked()
	bridge.mu.Unlock()
	if len(store.expected()) != 0 {
		t.Fatal("an older phone's request was kept")
	}

	// Told ahead: accepted as expected, and not as received.
	code, body := post(true)
	if code != http.StatusAccepted || state(body) != mobileRecordingStateExpected {
		t.Fatalf("told ahead: %d %s", code, body)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatal("a part was stored before its upload")
	}

	if pending := store.expected(); len(pending) != 1 || pending[0].Request.Context != nil {
		t.Fatal("disabled context reached the pending queue")
	}

	// The upload arrives, and the phone says nothing more.
	upload, err := http.NewRequest(http.MethodPut, pinned+"/v1/uploads/"+part.Upload.ID, bytes.NewReader(sealed))
	if err != nil {
		t.Fatal(err)
	}
	upload.Header.Set("Content-Type", "application/octet-stream")
	upload.Header.Set("Authorization", "Bearer "+phone.bearer)
	resp, err := phone.client.Do(upload)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload returned %d", resp.StatusCode)
	}
	if taken := bridge.takeExpectedRecordingParts(time.Now()); taken != 1 {
		t.Fatalf("took %d expected parts", taken)
	}
	if raw, err := os.ReadFile(local); err != nil || !bytes.Equal(raw, audio) {
		t.Fatalf("stored part: %v %v", raw, err)
	}
	disk := newMobileRecordingStore(bridge.stateDir)
	rec, ok := disk.get(part.RecordingID, phone.deviceID)
	if !ok || len(rec.Received) != 1 {
		t.Fatal("missing persisted part")
	}
	got := rec.Received[0]
	if got.Context != nil || got.SHA256 != part.SHA256 || got.Bytes != part.Bytes || got.StartedAt != part.StartedAt || got.DurationSeconds != part.DurationSeconds || len(got.Marks) != 1 || got.Marks[0] != 2 {
		t.Fatal("stored part must lose only the disabled context")
	}
	bridge.mu.Lock()
	relays := bridge.recordingRelaysLocked()
	bridge.mu.Unlock()
	if _, err := relays.client("pinned").downloadUpload(context.Background(), part.Upload.ID); !errors.Is(err, errMobileRecordingGone) {
		t.Fatalf("the relay's copy after the computer took the part: %v", err)
	}

	// The phone's next wake, old way or new: the computer has the part.
	for _, ahead := range []bool{false, true} {
		code, body := post(ahead)
		if code != http.StatusAccepted || state(body) != mobileRecordingReceived {
			t.Fatalf("after the fetch (ahead %v): %d %s", ahead, code, body)
		}
	}
}

func TestRecordingPartFetchesFromAnnouncedRelay(t *testing.T) {
	if os.Getenv("YANSU_MOBILE_E2E") != "1" {
		t.Skip("set YANSU_MOBILE_E2E=1 to run with both relay binaries")
	}
	_, bridge, phone, pinned, direct, invitation, grant := mobileDirectE2EPairedPhone(t)
	pinnedBearer := phone.bearer
	ctx := context.Background()
	bridge.mu.Lock()
	relays := bridge.recordingRelaysLocked()
	bridge.mu.Unlock()

	for _, tc := range []struct {
		name, announced, holding string
		wantStatus               int
	}{
		{"named pinned", "pinned", "pinned", 202},
		{"named direct", "direct", "direct", 202},
		{"legacy pinned", "", "pinned", 202},
		{"legacy direct after pinned 404", "", "direct", 202},
		{"named pinned missing", "pinned", "direct", 404},
		{"named direct missing", "direct", "pinned", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			audio := []byte{3, 1, 4}
			part, sealed := makeSealedRecordingPart(t, invitation.Hub, phone.deviceID, uuid.NewString(), 0, false, audio, time.Now())
			part.Upload.Relay = tc.announced
			url, bearer := pinned, pinnedBearer
			if tc.holding == "direct" {
				url, bearer = direct, grant.Data.Bearer
			}
			upload, err := http.NewRequest(http.MethodPut, url+"/v1/uploads/"+part.Upload.ID, bytes.NewReader(sealed))
			if err != nil {
				t.Fatal(err)
			}
			upload.Header.Set("Content-Type", "application/octet-stream")
			upload.Header.Set("Authorization", "Bearer "+bearer)
			resp, err := phone.client.Do(upload)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("upload returned %d", resp.StatusCode)
			}
			// Deliver the announcement through the opposite relay.
			phone.relay, phone.bearer = direct, grant.Data.Bearer
			if tc.holding == "direct" {
				phone.relay, phone.bearer = pinned, pinnedBearer
			}
			body, err := json.Marshal(part)
			if err != nil {
				t.Fatal(err)
			}
			if tc.announced == "" && bytes.Contains(body, []byte(`"relay"`)) {
				t.Fatal("legacy request contains a relay field")
			}
			logs := &lockedLog{}
			previous := log.Writer()
			log.SetOutput(logs)
			code, _ := phone.request(t, http.MethodPost, "/recordings", string(body))
			log.SetOutput(previous)
			if code != tc.wantStatus {
				t.Fatalf("announcement returned %d, want %d", code, tc.wantStatus)
			}
			// However the request ends, the computer's log says so in one line.
			want := "[recordings] part 0 of " + part.RecordingID + " taken: "
			if code == http.StatusNotFound {
				want = "[recordings] part 0 of " + part.RecordingID + " is not on the relay"
			}
			logs.mu.Lock()
			got := logs.b.String()
			logs.mu.Unlock()
			if !strings.Contains(got, want) {
				t.Fatalf("log %q lacks %q", got, want)
			}
			local, readErr := os.ReadFile(filepath.Join(bridge.stateDir, "recordings", part.RecordingID, "part-00.m4a"))
			remaining, fetchErr := relays.client(tc.holding).downloadUpload(ctx, part.Upload.ID)
			if code == http.StatusNotFound {
				if !os.IsNotExist(readErr) || fetchErr != nil || !bytes.Equal(remaining, sealed) {
					t.Fatal("missing named upload changed a copy")
				}
				return
			}
			if readErr != nil || !bytes.Equal(local, audio) || !errors.Is(fetchErr, errMobileRecordingGone) {
				t.Fatal("expected the stored part on the computer and the relay upload removed")
			}
		})
	}
}

func TestRecordingRelayWithOnlyTheDirectPath(t *testing.T) {
	t.Setenv("ISOFORM_RELAY_URL", "")
	t.Setenv("YANSU_RELAY_URL", "")
	t.Setenv("ISOFORM_MOBILE_REMOTE_ACCESS", "1")
	bridge := &mobileBridge{client: &mobileRelayClient{}}
	relays := bridge.recordingRelaysLocked()
	if relays.primaryName != "direct" || relays.client("direct") != bridge.client || relays.client("pinned") != nil {
		t.Fatal("ticket-only computer did not use its primary connection for direct uploads")
	}
}

func TestRecordingRetryAfterIndexWriteFailureKeepsUpload(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	req := sealedRecordingPart(t, uploads, "dev-a", uuid.NewString(), 0, false, []byte{1}, time.Now())
	// Load the empty store, then block its atomic index rename.
	if _, _, err := store.storedPart("dev-a", req.RecordingID, 0); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(store.dir, "index.json")
	if err := os.MkdirAll(index, 0700); err != nil {
		t.Fatal(err)
	}
	receive := func() error {
		_, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req)
		return err
	}
	for range 2 {
		if receive() == nil || len(uploads.deleted) != 0 {
			t.Fatal("acknowledged or removed the upload before committing its index")
		}
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := receive(); err != nil || len(uploads.deleted) != 1 {
		t.Fatalf("retry after restoring the index: %v, %d deletions", err, len(uploads.deleted))
	}
}

func TestRecordingRelayCleanupUsesTheStoredSourceOnReplay(t *testing.T) {
	dir := t.TempDir()
	pinned := &mapUploads{bodies: map[string][]byte{}}
	direct := &mapUploads{bodies: map[string][]byte{}}
	relays := mobileRecordingRelays{primaryName: mobileRecordingRelayPinned, primary: pinned, direct: direct}
	req := sealedRecordingPart(t, direct, "dev-a", uuid.NewString(), 0, false, []byte{1}, time.Now())
	sealed := direct.bodies[req.Upload.ID]
	receive := func() error {
		_, _, err := relays.receivePart(context.Background(), newMobileRecordingStore(dir), recordingTestHub, "dev-a", req)
		return err
	}
	if err := receive(); err != nil {
		t.Fatal(err)
	}
	// Simulate a lost cleanup, a restart, and the same id present on both relays.
	direct.bodies[req.Upload.ID] = sealed
	pinned.bodies[req.Upload.ID] = []byte{2}
	if err := receive(); err != nil {
		t.Fatal(err)
	}
	if len(direct.bodies) != 0 || len(pinned.deleted) != 0 {
		t.Fatal("replayed legacy announcement cleaned up the wrong relay")
	}
}

func TestRecordingLegacyPartFallsBackOnlyWhenMissing(t *testing.T) {
	pinned := &mapUploads{bodies: map[string][]byte{}}
	direct := &mapUploads{bodies: map[string][]byte{}}
	relays := mobileRecordingRelays{primaryName: mobileRecordingRelayPinned, primary: pinned, direct: direct}
	req := sealedRecordingPart(t, direct, "dev-a", uuid.NewString(), 0, false, []byte{1}, time.Now())
	pinned.bodies[req.Upload.ID] = []byte{2}
	_, _, err := relays.receivePart(context.Background(), newMobileRecordingStore(t.TempDir()), recordingTestHub, "dev-a", req)
	if !errors.Is(err, errMobileAttachmentRejected) || len(pinned.deleted) != 0 || len(direct.deleted) != 0 {
		t.Fatalf("invalid pinned body: %v, deletions %d/%d", err, len(pinned.deleted), len(direct.deleted))
	}
}
