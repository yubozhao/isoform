package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var allScopes = []string{"brief:read", "brief:refresh", "presence:read", "push"}

// A reconnecting hub receives its unacknowledged requests again, and an
// acknowledged request is never replayed.
func TestRequestQueueReplayAndAck(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, allScopes)
	hc.conn.Close()
	r.waitHubOnline(dev, false)

	request := requestEnvelope(t, dev, 1)
	r.expectStatus(r.postWith("/v1/requests", dev.bearer, request), http.StatusAccepted, "offline request")

	hc = r.connectHub(hubID, credential)
	frame := hc.recvType("in")
	var got struct {
		Seq uint64 `json:"seq"`
		CT  string `json:"ct"`
	}
	if err := json.Unmarshal(mustMarshal(t, frame["env"]), &got); err != nil || got.Seq != 1 || got.CT != request.CT {
		t.Fatalf("replayed request %v (%v)", frame, err)
	}
	hc.conn.Close()
	r.waitHubOnline(dev, false)

	// Unacknowledged requests are replayed again after another reconnect.
	hc = r.connectHub(hubID, credential)
	hc.recvType("in")
	hc.send(map[string]any{"t": "ack", "dev": dev.id, "upTo": 1})
	hc.barrier()
	hc.conn.Close()
	r.waitHubOnline(dev, false)
	hc = r.connectHub(hubID, credential)
	hc.barrier()
	for _, pending := range hc.pending {
		if pending["t"] == "in" {
			t.Fatal("acknowledged request was replayed")
		}
	}
}

// A phone numbers a request, then seals and sends it. Requests sent together reach
// the relay in any order. On 2026-11-20 the relay handed over only numbers above
// the highest it had sent, and an acknowledgement deleted every number up to the
// acknowledged one, so a part request that arrived after a later-numbered read was
// answered 202, never reached the computer, and was deleted. Wake after wake the
// relay accepted a few more requests than the computer served, and no recording
// crossed.
//
// The shape of a wake: the app opens and sends eight reads, numbered 2 to 9, and a
// part request numbered 1 that reaches the relay last. The computer acknowledges
// reads while the part request is on its way and after it was handed over.
func TestARequestThatArrivesAfterLaterNumberedOnesStillReachesTheComputer(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, allScopes)
	seqOf := func(frame map[string]any) uint64 {
		var got struct {
			Seq uint64 `json:"seq"`
		}
		if err := json.Unmarshal(mustMarshal(t, frame["env"]), &got); err != nil {
			t.Fatal(err)
		}
		return got.Seq
	}
	replayed := func(hc *hubClient) []uint64 {
		hc.barrier()
		seqs := []uint64{}
		for _, pending := range hc.pending {
			if pending["t"] == "in" {
				seqs = append(seqs, seqOf(pending))
			}
		}
		return seqs
	}

	// The reads arrive, are handed over in order, and all but the last are acknowledged.
	for seq := uint64(2); seq <= 9; seq++ {
		r.expectStatus(r.postWith("/v1/requests", dev.bearer, requestEnvelope(t, dev, seq)), http.StatusAccepted, "a read")
		if got := seqOf(hc.recvType("in")); got != seq {
			t.Fatalf("delivery carried %d, want %d", got, seq)
		}
		if seq < 9 {
			hc.send(map[string]any{"t": "ack", "dev": dev.id, "upTo": seq})
		}
	}
	hc.barrier()

	// The part request, numbered 1, arrives last. It is handed over all the same.
	part := requestEnvelope(t, dev, 1)
	r.expectStatus(r.postWith("/v1/requests", dev.bearer, part), http.StatusAccepted, "the part request, late")
	frame := hc.recvType("in")
	if seq := seqOf(frame); seq != 1 || !strings.Contains(string(mustMarshal(t, frame["env"])), part.CT) {
		t.Fatalf("the part request was not delivered: %v", frame)
	}

	// The computer acknowledges read 9, which it was handed before the part request.
	// The part request is not served yet: it stays. The computer goes away and comes
	// back, and the part request, still unacknowledged, is handed over again; the
	// acknowledged reads are not.
	hc.send(map[string]any{"t": "ack", "dev": dev.id, "upTo": 9})
	hc.barrier()
	hc.conn.Close()
	r.waitHubOnline(dev, false)
	hc = r.connectHub(hubID, credential)
	if got := replayed(hc); len(got) != 1 || got[0] != 1 {
		t.Fatalf("after a reconnect the computer was handed %v, want only the part request (1)", got)
	}

	// An acknowledgement of a number this connection never handed over deletes nothing.
	hc.send(map[string]any{"t": "ack", "dev": dev.id, "upTo": 9})
	hc.barrier()
	hc.conn.Close()
	r.waitHubOnline(dev, false)
	hc = r.connectHub(hubID, credential)
	if got := replayed(hc); len(got) != 1 || got[0] != 1 {
		t.Fatalf("an acknowledgement of another number deleted the part request: handed %v", got)
	}

	// Its own acknowledgement deletes it.
	hc.send(map[string]any{"t": "ack", "dev": dev.id, "upTo": 1})
	hc.barrier()
	hc.conn.Close()
	r.waitHubOnline(dev, false)
	hc = r.connectHub(hubID, credential)
	if got := replayed(hc); len(got) != 0 {
		t.Fatalf("the acknowledged part request was handed over again: %v", got)
	}

	// A request nobody acknowledges does not stay for ever: the store drops it after
	// RequestTTL (24 hours) and keeps at most MaxQueued (200) per phone:
	// TestRequestQueueCapAndExpiry in the store's tests.
}

func TestRequestDeliveredWhileHubIsConnected(t *testing.T) {
	r := newRelay(t, "")
	hc := r.connectHub(uuid.NewString(), randomToken(t))
	dev := r.pairDevice(hc, allScopes)
	request := requestEnvelope(t, dev, 1)
	r.expectStatus(r.postWith("/v1/requests", dev.bearer, request), http.StatusAccepted, "online request")
	frame := hc.recvType("in")
	if !strings.Contains(string(mustMarshal(t, frame["env"])), request.CT) {
		t.Fatalf("live delivery carried the wrong envelope: %v", frame)
	}
}

func TestRequestConflictsAndIdempotentRetry(t *testing.T) {
	r := newRelay(t, "")
	dev := r.pairDevice(r.connectHub(uuid.NewString(), randomToken(t)), allScopes)
	first := requestEnvelope(t, dev, 5)
	r.expectStatus(r.postWith("/v1/requests", dev.bearer, first), http.StatusAccepted, "first request")
	r.expectStatus(r.postWith("/v1/requests", dev.bearer, first), http.StatusAccepted, "identical retry")
	changed := first
	changed.CT = randomCT(t, 80)
	r.expectStatus(r.postWith("/v1/requests", dev.bearer, changed), http.StatusConflict, "different content under the same sequence")
	foreign := first
	foreign.Seq = 6
	foreign.Dev = uuid.NewString()
	r.expectStatus(r.postWith("/v1/requests", dev.bearer, foreign), http.StatusForbidden, "envelope for another device")
	wrongKind := first
	wrongKind.Seq = 7
	wrongKind.Kind = "res"
	wrongKind.Dir = "h2d"
	r.expectStatus(r.postWith("/v1/requests", dev.bearer, wrongKind), http.StatusBadRequest, "response envelope posted as a request")
}

func TestResponsesLongPollWakesOnOut(t *testing.T) {
	r := newRelay(t, "")
	hubID := uuid.NewString()
	hc := r.connectHub(hubID, randomToken(t))
	dev := r.pairDevice(hc, allScopes)
	type outcome struct {
		status int
		body   []byte
		err    error
	}
	done := make(chan outcome, 1)
	started := time.Now()
	go func() {
		status, body, err := rawGet(r.srv.URL+"/v1/responses?after=0&wait=25", dev.bearer, nil)
		done <- outcome{status, body, err}
	}()
	time.Sleep(300 * time.Millisecond)
	hc.send(frameWithEnvelope(t, "out", makeEnvelope(t, hubID, dev.id, "h2d", 1, "res", 120)))
	select {
	case got := <-done:
		if got.err != nil || got.status != http.StatusOK || time.Since(started) > 10*time.Second {
			t.Fatalf("response poll: %d %v after %s", got.status, got.err, time.Since(started))
		}
		var reply struct {
			Data struct {
				Envelopes []struct {
					Seq uint64 `json:"seq"`
				} `json:"envelopes"`
			} `json:"data"`
		}
		if err := json.Unmarshal(got.body, &reply); err != nil || len(reply.Data.Envelopes) != 1 || reply.Data.Envelopes[0].Seq != 1 {
			t.Fatalf("woken poll body %s (%v)", got.body, err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("response poll did not wake on out")
	}
	r.expectStatus(r.postWith("/v1/responses/ack", dev.bearer, map[string]uint64{"upTo": 1}), http.StatusOK, "ack")
	empty := r.getWith("/v1/responses?after=0&wait=0", dev.bearer)
	r.expectStatus(empty, http.StatusOK, "poll after ack")
	if string(mustMarshal(t, rawField(t, empty.Data, "envelopes"))) != "[]" {
		t.Fatalf("acknowledged envelope still delivered: %s", empty.Data)
	}
}

func TestResponsesWaitIsCappedAndValidated(t *testing.T) {
	r := newRelay(t, "")
	dev := r.pairDevice(r.connectHub(uuid.NewString(), randomToken(t)), allScopes)
	r.expectStatus(r.getWith("/v1/responses?wait=-1", dev.bearer), http.StatusBadRequest, "negative wait")
	r.expectStatus(r.getWith("/v1/responses?after=abc", dev.bearer), http.StatusBadRequest, "non-numeric cursor")
	started := time.Now()
	r.expectStatus(r.getWith("/v1/responses?after=0&wait=0", dev.bearer), http.StatusOK, "zero wait")
	if time.Since(started) > 2*time.Second {
		t.Fatal("zero wait blocked")
	}
}

func TestBriefStoreLatestAndRetention(t *testing.T) {
	r := newRelay(t, "")
	hubID := uuid.NewString()
	hc := r.connectHub(hubID, randomToken(t))
	dev := r.pairDevice(hc, allScopes)
	r.expectStatus(r.getWith("/v1/brief/latest", dev.bearer), http.StatusNotFound, "no brief yet")
	for seq := uint64(1); seq <= 9; seq++ {
		hc.send(briefFrame(t, hubID, dev.id, seq, false))
	}
	hc.barrier()
	latest := r.getWith("/v1/brief/latest", dev.bearer)
	r.expectStatus(latest, http.StatusOK, "latest brief")
	if version := rawField(t, latest.Data, "version"); string(version) != "9" {
		t.Fatalf("latest version %s, want 9", version)
	}
	list := r.getWith("/v1/briefs?limit=7", dev.bearer)
	r.expectStatus(list, http.StatusOK, "brief list")
	versions := briefVersions(t, list.Data)
	if strings.Join(versions, ",") != "9,8,7,6,5,4,3" {
		t.Fatalf("retained versions %v, want the newest seven", versions)
	}
	r.expectStatus(r.getWith("/v1/briefs?limit=8", dev.bearer), http.StatusBadRequest, "limit above seven")
	r.expectStatus(r.getWith("/v1/briefs?limit=0", dev.bearer), http.StatusBadRequest, "limit zero")
	if all := briefVersions(t, r.getWith("/v1/briefs", dev.bearer).Data); len(all) != 7 {
		t.Fatalf("default list has %d briefs", len(all))
	}
}

func TestBriefNotifyQueuesOneNotificationPerBrief(t *testing.T) {
	r := newRelay(t, "development")
	hubID := uuid.NewString()
	hc := r.connectHub(hubID, randomToken(t))
	dev := r.pairDevice(hc, allScopes)
	r.expectStatus(r.putPush(dev, strings.Repeat("ab", 32), "development"), http.StatusOK, "push registration")
	hc.send(briefFrame(t, hubID, dev.id, 1, true))
	hc.send(briefFrame(t, hubID, dev.id, 2, false))
	hc.barrier()
	claim, err := r.store.ClaimPush(t.Context(), time.Now().Add(time.Second), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("notify brief queued no push: %v", err)
	}
	if claim.EventID != "1" || claim.HubID != hubID || claim.DeviceID != dev.id {
		t.Fatalf("push routing %+v", claim)
	}
	if next, err := r.store.ClaimPush(t.Context(), time.Now().Add(time.Second), time.Minute); err != nil || next != nil {
		t.Fatalf("silent brief produced a notification: %+v %v", next, err)
	}
}

func TestPresenceReportsHubState(t *testing.T) {
	r := newRelay(t, "")
	hc := r.connectHub(uuid.NewString(), randomToken(t))
	dev := r.pairDevice(hc, allScopes)
	online, seen := r.presenceOf(dev)
	if !online || !seen {
		t.Fatalf("connected hub presence online=%t seen=%t", online, seen)
	}
	hc.conn.Close()
	r.waitHubOnline(dev, false)
	if _, seen = r.presenceOf(dev); !seen {
		t.Fatal("last-seen time was lost when the hub disconnected")
	}
}

func TestPresenceGoodbyeAndReconnect(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, allScopes)
	for _, cause := range []string{"sleeping", "closing", "shutting_down"} {
		hc.send(map[string]any{"t": "goodbye", "cause": cause})
		hc.barrier()
		resp := r.getWith("/v1/presence", dev.bearer)
		var got struct {
			HubOnline bool   `json:"hubOnline"`
			Cause     string `json:"goodbyeCause"`
			At        string `json:"goodbyeAt"`
		}
		if err := json.Unmarshal(resp.Data, &got); err != nil || got.HubOnline || got.Cause != cause || got.At == "" {
			t.Fatalf("goodbye %s: %+v, %v", cause, got, err)
		}
	}
	hc.send(map[string]any{"t": "goodbye", "cause": "closing"})
	hc.barrier()
	retained := r.getWith("/v1/presence", dev.bearer)
	var poweroff map[string]any
	if err := json.Unmarshal(retained.Data, &poweroff); err != nil || poweroff["goodbyeCause"] != "shutting_down" {
		t.Fatalf("quit overwrote shutdown: %v, %v", poweroff, err)
	}
	hc.send(map[string]any{"t": "goodbye", "cause": "guessed"})
	if got := hc.recvType("error"); got["ref"] != "goodbye" {
		t.Fatalf("invalid cause accepted: %v", got)
	}
	hc.conn.Close()
	newer := r.connectHub(hubID, credential)
	newer.barrier()
	resp := r.getWith("/v1/presence", dev.bearer)
	var got map[string]any
	if err := json.Unmarshal(resp.Data, &got); err != nil || got["hubOnline"] != true || got["goodbyeCause"] != nil {
		t.Fatalf("reconnected presence = %v, %v", got, err)
	}
}

func TestPresenceGoodbyeReason(t *testing.T) {
	r := newRelay(t, "")
	hc := r.connectHub(uuid.NewString(), randomToken(t))
	dev := r.pairDevice(hc, allScopes)
	presence := func() map[string]any {
		var got map[string]any
		if err := json.Unmarshal(r.getWith("/v1/presence", dev.bearer).Data, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, reason := range []string{"battery_floor", "low_power", "lid_closed", "user_sleep", "unknown"} {
		hc.send(map[string]any{"t": "goodbye", "cause": "sleeping", "reason": reason})
		hc.barrier()
		if got := presence(); got["goodbyeCause"] != "sleeping" || got["goodbyeReason"] != reason {
			t.Fatalf("goodbye reason %s: %v", reason, got)
		}
	}
	hc.send(map[string]any{"t": "goodbye", "cause": "sleeping"})
	hc.barrier()
	if got := presence(); got["goodbyeCause"] != "sleeping" {
		t.Fatalf("goodbye without reason: %v", got)
	} else if _, ok := got["goodbyeReason"]; ok {
		t.Fatalf("a goodbye without a reason kept the old one: %v", got)
	}
	hc.send(map[string]any{"t": "goodbye", "cause": "sleeping", "reason": "guessed"})
	if got := hc.recvType("error"); got["ref"] != "goodbye" {
		t.Fatalf("invalid reason accepted: %v", got)
	}
}

func TestScopeBearerAndExpiry(t *testing.T) {
	r := newRelay(t, "")
	dev := r.pairDevice(r.connectHub(uuid.NewString(), randomToken(t)), []string{"presence:read"})
	r.expectStatus(r.getWith("/v1/presence", dev.bearer), http.StatusOK, "granted scope")
	r.expectStatus(r.getWith("/v1/brief/latest", dev.bearer), http.StatusForbidden, "missing brief:read")
	r.expectStatus(r.getWith("/v1/briefs", dev.bearer), http.StatusForbidden, "missing brief:read on list")
	r.expectStatus(r.getWith("/v1/responses?wait=0", dev.bearer), http.StatusForbidden, "missing brief:read on responses")
	r.expectStatus(r.postWith("/v1/responses/ack", dev.bearer, map[string]uint64{"upTo": 1}), http.StatusForbidden, "missing brief:read on ack")
	r.expectStatus(r.putPush(dev, strings.Repeat("cd", 32), "development"), http.StatusForbidden, "missing push scope")
	r.expectStatus(r.call(http.MethodGet, "/v1/presence", nil, nil), http.StatusUnauthorized, "no bearer")
	r.expectStatus(r.getWith("/v1/presence", "not-a-bearer"), http.StatusUnauthorized, "malformed bearer")
	r.advance(91 * 24 * time.Hour)
	r.expectStatus(r.getWith("/v1/presence", dev.bearer), http.StatusUnauthorized, "expired device")
}

// On 2026-11-20 a phone opened the app for thirty seconds: 48 requests for the
// computer, each with a long-poll and an acknowledgement beside it. At 60 a
// minute the relay refused everything after the eleventh second, the request
// that hands a recorded part to the computer among them.
func TestOpeningTheAppLeavesRoomForTheRequestsThatFollow(t *testing.T) {
	r := newRelay(t, "")
	dev := r.pairDevice(r.connectHub(uuid.NewString(), randomToken(t)), allScopes)
	for range 50 {
		r.expectStatus(r.getWith("/v1/presence", dev.bearer), http.StatusOK, "a read while the app opens")
		r.expectStatus(r.getWith("/v1/responses?wait=0", dev.bearer), http.StatusOK, "its long-poll")
		r.expectStatus(r.postWith("/v1/responses/ack", dev.bearer, map[string]uint64{"upTo": 0}), http.StatusOK, "its acknowledgement")
	}
	r.expectStatus(r.getWith("/v1/presence", dev.bearer), http.StatusOK, "the request after the app has opened")
}

func TestDeviceRequestRateLimit(t *testing.T) {
	r := newRelay(t, "")
	dev := r.pairDevice(r.connectHub(uuid.NewString(), randomToken(t)), allScopes)
	for range deviceRequestsPerMinute {
		r.expectStatus(r.getWith("/v1/presence", dev.bearer), http.StatusOK, "within the minute budget")
	}
	limited := r.getWith("/v1/presence", dev.bearer)
	r.expectStatus(limited, http.StatusTooManyRequests, "one request past the minute's budget")
	if limited.Header.Get("Retry-After") == "" {
		t.Fatal("device rate limit omitted Retry-After")
	}
	r.advance(time.Minute)
	r.expectStatus(r.getWith("/v1/presence", dev.bearer), http.StatusOK, "after the window resets")
}

func TestDeviceRemovalRefusesStaleRequests(t *testing.T) {
	r := newRelay(t, "")
	dev := r.pairDevice(r.connectHub(uuid.NewString(), randomToken(t)), allScopes)
	r.expectStatus(r.call(http.MethodDelete, "/v1/devices/me", bearer(dev.bearer), nil), http.StatusOK, "unpair")
	r.expectStatus(r.postWith("/v1/requests", dev.bearer, requestEnvelope(t, dev, 1)), http.StatusUnauthorized, "request after unpair")
}

// mustMarshal re-encodes a decoded JSON value so typed fields can be read.
func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// rawField returns one top-level field of a JSON object.
func rawField(t *testing.T, data json.RawMessage, name string) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("not an object: %s (%v)", data, err)
	}
	return fields[name]
}

// briefVersions lists the versions in a brief list reply, in reply order.
func briefVersions(t *testing.T, data json.RawMessage) []string {
	t.Helper()
	var list struct {
		Briefs []struct {
			Version uint64 `json:"version"`
		} `json:"briefs"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	versions := make([]string, 0, len(list.Briefs))
	for _, brief := range list.Briefs {
		versions = append(versions, strconv.FormatUint(brief.Version, 10))
	}
	return versions
}
