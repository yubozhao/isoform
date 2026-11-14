package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMobileReplayWindowAcceptsOutOfOrderOnceAndRejectsReplays(t *testing.T) {
	w := newMobileReplayWindowAt(0)
	for _, step := range []struct {
		seq  uint64
		want bool
	}{
		{0, false}, // never a valid number
		{5, true},  // the direct path got ahead
		{3, true},  // the relay held this one: inside the window, not seen
		{3, false}, // the same request again
		{5, false}, // and the newer one again
		{4, true},  // the other held request
		{6, true},  // the stream goes on
		{1, true},  // still inside the window
		{1, false},
	} {
		if got := w.accept(step.seq); got != step.want {
			t.Fatalf("accept(%d) = %v, want %v", step.seq, got, step.want)
		}
	}
	if w.top != 6 {
		t.Fatalf("top = %d, want 6", w.top)
	}
	// A number older than the window is a replay: the window cannot tell it apart.
	far := uint64(10_000)
	if !w.accept(far) {
		t.Fatal("a far jump was refused")
	}
	if w.accept(far - mobileReplayWindowSize) {
		t.Fatal("a number older than the window was accepted")
	}
	if !w.accept(far - mobileReplayWindowSize + 1) {
		t.Fatal("the oldest number inside the window was refused")
	}
	// A jump past the whole window forgets the old bits, and the old numbers are too old anyway.
	if !w.accept(far+mobileReplayWindowSize+5) || w.accept(far) {
		t.Fatal("a jump past the window kept accepting old numbers")
	}
}

// A window started at a high-water mark rejects what the high-water mark
// rejected: every number up to it.
func TestMobileReplayWindowAtTheOldHighWaterMark(t *testing.T) {
	w := newMobileReplayWindowAt(40)
	for seq := uint64(1); seq <= 40; seq++ {
		if w.fresh(seq) {
			t.Fatalf("%d is accepted after an upgrade at 40", seq)
		}
	}
	if !w.accept(42) || !w.accept(41) || w.accept(40) {
		t.Fatal("numbers above the upgrade mark are not handled as a window")
	}
}

// The fix itself: a request the relay held arrives after a newer one, and is
// served. A redelivery of either is acknowledged and not served again.
func TestMobileHubServesARequestThatArrivesAfterANewerOne(t *testing.T) {
	bridge, relay := mobileTestHub(t)
	phone := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead})
	conn := relay.acceptRegistration(t, mobileTestListed(phone))
	seal := func(seq uint64) mobileEnvelope {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
		request := []byte(`{"id":"` + id + `","method":"GET","path":"/devices","query":"","body":""}`)
		return phone.sealRequest(t, bridge.identity.Public, bridge.identity.HubID, seq, request)
	}
	newer, held := seal(2), seal(1)
	conn.send(t, map[string]any{"t": "in", "env": newer})
	relay.nextFrame(t, "out")
	relay.nextFrame(t, "ack")
	conn.send(t, map[string]any{"t": "in", "env": held})
	relay.nextFrame(t, "out")
	relay.nextFrame(t, "ack")
	for _, again := range []mobileEnvelope{held, newer} {
		conn.send(t, map[string]any{"t": "in", "env": again})
		relay.nextFrame(t, "ack")
		relay.expectNoFrame(t, "out", 200*time.Millisecond)
	}
	if got := bridge.devices[phone.deviceID].LastAcceptedSeq; got != 2 {
		t.Fatalf("lastAcceptedSeq = %d, want the high-water mark 2", got)
	}
}

// The window survives a restart: a request served before it is still a replay
// after it, and a held one that was never seen is still served.
func TestMobileReplayWindowSurvivesARestart(t *testing.T) {
	bridge, _ := mobileTestHub(t)
	phone := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead})
	for _, seq := range []uint64{5, 3} {
		if fresh, err := bridge.acceptRequest(mobileEnvelope{Dev: phone.deviceID, Seq: seq}); err != nil || !fresh {
			t.Fatalf("accept %d: %v %v", seq, fresh, err)
		}
	}
	restarted, err := newMobileBridge(&App{}, bridge.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Stop)
	for _, step := range []struct {
		seq  uint64
		want bool
	}{{5, false}, {3, false}, {4, true}, {4, false}, {6, true}} {
		fresh, err := restarted.acceptRequest(mobileEnvelope{Dev: phone.deviceID, Seq: step.seq})
		if err != nil || fresh != step.want {
			t.Fatalf("after restart, accept %d = %v %v, want %v", step.seq, fresh, err, step.want)
		}
	}
}

// Each request is acknowledged on the relay that delivered it, and only there.
// An ack deletes every queued request up to its number, so the direct path's
// ack for a newer request must never reach the pinned relay, which may still
// hold an older one.
func TestMobileRequestsAreAcknowledgedOnTheRelayThatDeliveredThem(t *testing.T) {
	bridge, pinned := mobileTestHub(t)
	phone := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead})
	pinnedConn := pinned.acceptRegistration(t, mobileTestListed(phone))

	direct := newMobileTestRelay(t, bridge.identity.Credential, bridge.identity.HubID)
	bridge.directMu.Lock()
	bridge.directGrants[phone.deviceID] = mobileDirectGrant{DeviceID: phone.deviceID, BearerHash: strings.Repeat("a", 64),
		ExpiresAt: mobileTimestamp(time.Now().Add(time.Hour))}
	bridge.directMu.Unlock()
	client, err := newMobileRelayClient(direct.url(), *bridge.identity, mobileHubName(), &mobileDirectHandler{bridge: bridge})
	if err != nil {
		t.Fatal(err)
	}
	bridge.direct.Store(client)
	client.start(bridge.ctx)
	directConn := direct.acceptRegistration(t, mobileTestListed(phone))
	direct.nextFrame(t, "grant")

	seal := func(seq uint64) mobileEnvelope {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
		request := []byte(`{"id":"` + id + `","method":"GET","path":"/devices","query":"","body":""}`)
		return phone.sealRequest(t, bridge.identity.Public, bridge.identity.HubID, seq, request)
	}
	// The newer request comes over the direct path: its ack stays there.
	directConn.send(t, map[string]any{"t": "in", "env": seal(2)})
	pinned.nextFrame(t, "out")
	direct.nextFrame(t, "out")
	direct.nextFrame(t, "ack")
	pinned.expectNoFrame(t, "ack", 300*time.Millisecond)

	// The held one comes over the pinned relay later: served, and acked there only.
	pinnedConn.send(t, map[string]any{"t": "in", "env": seal(1)})
	pinned.nextFrame(t, "out")
	pinned.nextFrame(t, "ack")
	direct.expectNoFrame(t, "ack", 300*time.Millisecond)
}
