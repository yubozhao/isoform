package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

// Read anywhere, gone everywhere (notify v1.0.0 rule 8, B7).

func noticesTestApp(t *testing.T) (*App, *engagementTestRig, func() []string) {
	t.Helper()
	rig := newEngagementTestRig(t)
	app := &App{store: rig.store, engagement: rig.svc}
	var mu sync.Mutex
	var withdrawn []string
	previous := noticesWithdrawHook
	noticesWithdrawHook = func(_ *App, ids []string) {
		mu.Lock()
		defer mu.Unlock()
		withdrawn = append(withdrawn, ids...)
	}
	t.Cleanup(func() { noticesWithdrawHook = previous })
	return app, rig, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), withdrawn...)
	}
}

func TestNoticesReadAreKeptOnceForTwoDays(t *testing.T) {
	app, _, _ := noticesTestApp(t)
	now := time.Now()
	app.noteNoticesRead([]string{"n1", "brief:2026-11-15-evening", " ", "n1"}, now.Add(-time.Hour))
	app.noteNoticesRead([]string{"n1", "n2"}, now)
	got := app.noticesReadSince(now.Add(-noticesReadKeep), now)
	if len(got) != 3 || got[0].ID != "n1" || got[2].ID != "n2" {
		t.Fatalf("read = %+v", got)
	}
	if later := app.noticesReadSince(now.Add(-30*time.Minute), now); len(later) != 1 || later[0].ID != "n2" {
		t.Fatalf("since half an hour = %+v", later)
	}
	if gone := app.noticesReadSince(time.Time{}, now.Add(noticesReadKeep+2*time.Hour)); len(gone) != 0 {
		t.Fatalf("past two days = %+v", gone)
	}
}

// Read on the computer (a toast opened, or the brief opened in the app) or on
// the phone (a receipt): the notice goes from the other device.
func TestAReadNoticeIsWithdrawnEverywhere(t *testing.T) {
	app, rig, withdrawn := noticesTestApp(t)
	ctx := context.Background()
	rig.svc.deliver.Report = func(engagement.Moment, engagementText) bool { return true }
	brief, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindBriefReady, SubjectID: "2026-10-12-evening",
		DedupeKey: engagement.DedupeKey(engagement.KindBriefReady, "2026-10-12-evening"), Params: map[string]string{"slot": "evening"}})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	app.engagementReceiptOn(engagement.ChannelPush, brief.ID, storage.EngagementReceiptOpened, "", time.Now())
	noticesWaitFor(t, func() bool { return len(withdrawn()) == 2 })
	if got := withdrawn(); got[0] != brief.ID || got[1] != "brief:2026-10-12-evening" {
		t.Fatalf("withdrawn = %v", got)
	}
	// A delivered receipt is not a read.
	other, _ := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindMeetingPrep, SubjectID: "ev-2"})
	rig.svc.Pass(ctx)
	app.engagementReceiptOn(engagement.ChannelPush, other.ID, storage.EngagementReceiptDelivered, "", time.Now())
	if read := app.noticesReadSince(time.Time{}, time.Now()); len(read) != 2 {
		t.Fatalf("a delivery counted as a read: %+v", read)
	}
	// The brief opened on the computer.
	if err := app.MarkBriefRead("2026-10-13-morning"); err != nil {
		t.Fatal(err)
	}
	noticesWaitFor(t, func() bool { got := withdrawn(); return len(got) == 3 && got[2] == "brief:2026-10-13-morning" })
}

func TestThePhoneReadsAndReportsReadNotices(t *testing.T) {
	app, _, _ := noticesTestApp(t)
	bridge := &mobileBridge{app: app, stateDir: t.TempDir()}
	call := func(method, path, body string, scopes ...string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, mobileAPIPrefix+path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		bridge.handleAuthorized(recorder, req, mobileTestDevice(scopes...))
		return recorder
	}
	if got := call(http.MethodGet, "/notices/read", "", mobileScopeBriefRead); got.Code != http.StatusForbidden {
		t.Fatalf("without push: %d", got.Code)
	}
	if got := call(http.MethodPost, "/notices/read", `{"ids":[]}`, mobileScopePush); got.Code != http.StatusBadRequest {
		t.Fatalf("no ids: %d", got.Code)
	}
	if got := call(http.MethodPost, "/notices/read", `{"ids":["brief:2026-10-12-evening"]}`, mobileScopePush); got.Code != http.StatusOK {
		t.Fatalf("POST: %d %s", got.Code, got.Body.String())
	}
	if got := call(http.MethodGet, "/notices/read?since=yesterday", "", mobileScopePush); got.Code != http.StatusBadRequest {
		t.Fatalf("a bad since: %d", got.Code)
	}
	got := call(http.MethodGet, "/notices/read?since="+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "", mobileScopePush)
	var response struct {
		Data struct {
			Read []noticeRead `json:"read"`
		} `json:"data"`
	}
	if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &response) != nil || len(response.Data.Read) != 1 ||
		response.Data.Read[0].ID != "brief:2026-10-12-evening" {
		t.Fatalf("GET: %d %s", got.Code, got.Body.String())
	}
}

func noticesWaitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
