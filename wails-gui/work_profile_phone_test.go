package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkProfilePhoneSummaryScopeAndShape(t *testing.T) {
	bridge := &mobileBridge{}
	path := mobileAPIPrefix + "/work-profile/summary"
	denied := httptest.NewRecorder()
	bridge.handleAuthorized(denied, httptest.NewRequest(http.MethodGet, path, nil), mobileTestDevice())
	if denied.Code != http.StatusForbidden {
		t.Fatalf("scope check: %d", denied.Code)
	}
	allowed := httptest.NewRecorder()
	bridge.handleAuthorized(allowed, httptest.NewRequest(http.MethodGet, path, nil), mobileTestDevice(mobileScopeBriefRead))
	if allowed.Code == http.StatusForbidden || allowed.Code == http.StatusNotFound {
		t.Fatalf("route not registered: %d", allowed.Code)
	}
	state := workProfileState{ApprovedVersion: 2, Approved: &WorkProfileDraft{LearnedThrough: "2026-11-08", Specialties: []WorkProfileSpecialty{{Name: "Client SOWs", Selected: true}}, Lines: []WorkProfileLine{{Text: "PRIVATE LINE"}}}, Pending: &WorkProfileDraft{Lines: []WorkProfileLine{{Text: "PRIVATE DRAFT"}}}}
	data, err := json.Marshal(workProfileSummary(state))
	if err != nil || string(data) != `{"version":2,"learnedThrough":"2026-11-08","specialties":["Client SOWs"],"hasDraft":true}` || strings.Contains(string(data), "PRIVATE") {
		t.Fatalf("summary: %s, %v", data, err)
	}
}
