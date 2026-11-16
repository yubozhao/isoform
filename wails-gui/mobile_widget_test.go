package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The widget shows the latest brief, its time and first line, and the count
// to answer (phone-map rule 18, B11), from Today's snapshot as this phone may
// see it, and never builds on the request.
func TestMobileWidgetShowsTheCurrentBriefFromTodaysSnapshot(t *testing.T) {
	bridge := &mobileBridge{}
	bridge.today.value = &MobileTodayV1{
		GeneratedAt: "2026-11-15T18:02:00Z",
		Briefs: []mobileTodayBrief{
			{ID: "2026-11-15-evening", Lead: "Lena's fix is in review."},
			{ID: "2026-11-15-morning", Lead: "A quiet morning."},
		},
		BriefStatus: &mobileBriefStatus{Current: &mobileBriefStatusCurrent{
			ID: "2026-11-15-evening", Version: "v2", Kind: "evening", MadeAt: "2026-11-15T18:00:40Z", Audio: true,
		}},
		NeedsYouCount: 3,
	}
	device := mobileDeviceRecord{}
	device.Scopes = []string{mobileScopeBriefRead, mobileScopeChatRead}
	recorder := httptest.NewRecorder()
	bridge.serveWidget(recorder, device)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /widget status = %d", recorder.Code)
	}
	var response struct {
		Data mobileWidgetV1 `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	got := response.Data
	if got.V != 1 || got.AsOf != "2026-11-15T18:02:00Z" || got.ToAnswer != 3 || got.Brief == nil {
		t.Fatalf("widget = %+v", got)
	}
	if got.Brief.ID != "2026-11-15-evening" || got.Brief.Kind != "evening" || got.Brief.MadeAt != "2026-11-15T18:00:40Z" ||
		got.Brief.Version != "v2" || got.Brief.Lead != "Lena's fix is in review." {
		t.Fatalf("widget brief = %+v", got.Brief)
	}
}

// Before any brief the widget has no brief, and before the first snapshot the
// route answers 503 at once.
func TestMobileWidgetWithoutABriefOrASnapshot(t *testing.T) {
	if got := mobileWidgetFromToday(MobileTodayV1{GeneratedAt: "t", BriefStatus: &mobileBriefStatus{}}); got.Brief != nil || got.V != 1 {
		t.Fatalf("a widget without a brief = %+v", got)
	}
	bridge := &mobileBridge{}
	release := make(chan struct{})
	defer close(release)
	bridge.today.build = func() (MobileTodayV1, error) {
		<-release
		return MobileTodayV1{}, nil
	}
	recorder := httptest.NewRecorder()
	bridge.serveWidget(recorder, mobileDeviceRecord{})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /widget without a snapshot = %d, want 503", recorder.Code)
	}
}
