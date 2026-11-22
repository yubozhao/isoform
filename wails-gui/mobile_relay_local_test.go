package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testUploadID = "0b0f4d0e-58a5-4c53-9d1e-0f5a37f1c001"

func quickUploadRetries(t *testing.T) {
	t.Helper()
	first, limit := mobileUploadRetryFirst, mobileUploadRetryMax
	mobileUploadRetryFirst, mobileUploadRetryMax = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { mobileUploadRetryFirst, mobileUploadRetryMax = first, limit })
}

func testRelayClient(t *testing.T, origin string) *mobileRelayClient {
	t.Helper()
	client, err := newMobileRelayClient(origin, mobileHubIdentity{HubID: "hub", Credential: "secret"}, "computer", nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// The tunnel's 504 on 2026-11-16 cost a part its turn. A slow answer is asked again.
func TestUploadDownloadIsTriedAgainAfterAGatewayTimeout(t *testing.T) {
	quickUploadRetries(t)
	body := bytes.Repeat([]byte("sealed"), 1000)
	var mu sync.Mutex
	calls := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n <= 2 {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		http.ServeContent(w, r, "", time.Unix(1_700_000_000, 0), bytes.NewReader(body))
	}))
	defer relay.Close()

	got, err := testRelayClient(t, relay.URL).downloadUpload(context.Background(), testUploadID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got %d bytes, want %d", len(got), len(body))
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

// A connection cut in the middle goes on from where it stopped.
func TestUploadDownloadResumesWhereItStopped(t *testing.T) {
	quickUploadRetries(t)
	body := bytes.Repeat([]byte("0123456789"), 5000)
	modified := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	var ranges []string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		first := len(ranges) == 1
		mu.Unlock()
		if first {
			// Promise everything, deliver a part, then drop the connection.
			w.Header().Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
			w.Header().Set("Content-Length", "50000")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body[:20000])
			w.(http.Flusher).Flush()
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		http.ServeContent(w, r, "", modified, bytes.NewReader(body))
	}))
	defer relay.Close()

	got, err := testRelayClient(t, relay.URL).downloadUpload(context.Background(), testUploadID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got %d bytes, want %d", len(got), len(body))
	}
	if len(ranges) != 2 || ranges[0] != "" || !strings.HasPrefix(ranges[1], "bytes=") || ranges[1] == "bytes=0-" {
		t.Fatalf("ranges = %q, want one whole request and one for the rest", ranges)
	}
}

// When the time is over the error stays a "not yet" (503 to the phone), never a refusal.
func TestUploadDownloadGivesUpOnlyWhenTheTimeIsOver(t *testing.T) {
	quickUploadRetries(t)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer relay.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err := testRelayClient(t, relay.URL).downloadUpload(ctx, testUploadID)
	if !errors.Is(err, errMobileUploadRetry) {
		t.Fatalf("err = %v, want a retry error", err)
	}
	for _, refusal := range []error{errMobileRecordingGone, errMobileRecordingSHA, errMobileAttachmentRejected, errMobileRecordingInvalid} {
		if errors.Is(err, refusal) {
			t.Fatalf("err %v reads as a refusal", err)
		}
	}
}

func TestUploadDownloadStopsAtOnceWhenTheUploadIsGone(t *testing.T) {
	quickUploadRetries(t)
	calls := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer relay.Close()
	_, err := testRelayClient(t, relay.URL).downloadUpload(context.Background(), testUploadID)
	if !errors.Is(err, errMobileRecordingGone) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want gone after 1", err, calls)
	}
}

// With the relay on this computer, the computer asks it here; the phone's address is unchanged.
func TestComputerReachesALocalRelayOnLoopback(t *testing.T) {
	body := []byte("sealed upload")
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "", time.Unix(1_700_000_000, 0), bytes.NewReader(body))
	}))
	defer local.Close()
	t.Setenv(mobileRelayLocalURLEnv, local.URL)

	client := testRelayClient(t, "https://relay.example.invalid")
	if client.origin != "https://relay.example.invalid" {
		t.Fatalf("origin = %q, the address phones are given must stay public", client.origin)
	}
	if client.hubOrigin() != local.URL || !strings.HasPrefix(client.wsURL, "ws://127.0.0.1:") {
		t.Fatalf("hubOrigin = %q, wsURL = %q, want the loopback relay", client.hubOrigin(), client.wsURL)
	}
	got, err := client.downloadUpload(context.Background(), testUploadID)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("download through loopback: %q, %v", got, err)
	}
}

func TestLocalRelayAddressMustBeThisComputer(t *testing.T) {
	for _, value := range []string{"https://relay.example.com", "http://192.168.1.5:8791", "http://example.com:8791", "nonsense", "https://127.0.0.1:8791"} {
		t.Setenv(mobileRelayLocalURLEnv, value)
		if got := mobileRelayLocalOrigin(); got != "" {
			t.Fatalf("%q accepted as %q", value, got)
		}
	}
	t.Setenv(mobileRelayLocalURLEnv, "")
	client := testRelayClient(t, "https://relay.example.invalid")
	if client.local != "" || client.hubOrigin() != client.origin || client.wsURL != "wss://relay.example.invalid/v1/hub/ws" {
		t.Fatalf("without the setting the client changed: local %q ws %q", client.local, client.wsURL)
	}
}

// Isoform's own remote access runs a second relay on its own loopback port. The setting names
// the relay behind the public address and must not move that other connection.
func TestLocalRelaySettingLeavesAnotherLoopbackRelayAlone(t *testing.T) {
	t.Setenv(mobileRelayLocalURLEnv, "http://127.0.0.1:8791")
	client := testRelayClient(t, "http://127.0.0.1:49512")
	if client.local != "" || client.hubOrigin() != "http://127.0.0.1:49512" || client.wsURL != "ws://127.0.0.1:49512/v1/hub/ws" {
		t.Fatalf("a loopback relay was moved: local %q ws %q", client.local, client.wsURL)
	}
}

// Monday 2026-11-16, 7:22 to 8:23 PM: the direct link reached the pinned relay and re-sent its
// grants there. A direct link that would connect where the pinned relay connection does is refused.
func TestADirectLinkNeverReachesThePinnedRelay(t *testing.T) {
	t.Setenv(mobileRelayLocalURLEnv, "http://127.0.0.1:8791")
	pinned := testRelayClient(t, "https://relay.example.test")
	if !mobileDirectReachesPinned(testRelayClient(t, "http://127.0.0.1:8791"), pinned) {
		t.Fatal("a direct link to the pinned relay's own address was allowed")
	}
	if mobileDirectReachesPinned(testRelayClient(t, "http://127.0.0.1:49512"), pinned) {
		t.Fatal("the sidecar on its own port was refused")
	}
	if mobileDirectReachesPinned(testRelayClient(t, "http://127.0.0.1:49512"), nil) {
		t.Fatal("a direct link without a pinned relay was refused")
	}
}
