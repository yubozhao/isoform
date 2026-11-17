package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
)

func mobileHoldTestBridge(t *testing.T) (*mobileBridge, *fakeHoldEventStore, *time.Time) {
	t.Helper()
	bridge, tasks, _ := mobileTestThreadBridge(t)
	identity, err := createMobileHubIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bridge.identity = &identity
	now := time.Now().Truncate(time.Second)
	if err := bridge.app.store.SetUIState(context.Background(), calendarLastSyncKey, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Save(handoff.Entry{ID: "plan", Title: "Q4 plan", Status: handoff.StatusActive, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	calendar := &fakeHoldEventStore{calendars: []holdCalendar{{ID: "work", Title: "Work", IsDefault: true}}}
	bridge.app.holds = &holdCoordinator{db: bridge.app.activity.store.db, calendar: calendar, now: func() time.Time { return now }, verifyCoverage: func(event holdEvent) error {
		return bridge.app.holdCoverageWithAccess(event, now, true)
	}}
	return bridge, calendar, &now
}

func mobileHoldProposalCall(t *testing.T, bridge *mobileBridge, device mobileDeviceRecord, granted bool) (int, mobileHoldProposal) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/mobile/v1/holds/propose?source_kind=task&source_id=plan", nil)
	bridge.serveHoldProposal(w, r, device, granted)
	var result struct {
		Data mobileHoldProposal `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return w.Code, result.Data
}

func mobileHoldActionCall(t *testing.T, bridge *mobileBridge, device mobileDeviceRecord, input mobileHoldAction, granted bool) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/mobile/v1/holds/actions", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	bridge.serveHoldAction(w, r, device, granted)
	return w
}

// Calendar holds are switched off in the rework: with the switch off, the phone is told holds are unavailable
// even with calendar access and the preference on.
func TestMobileHoldsOffWhileTheSwitchIsOff(t *testing.T) {
	bridge, _, _ := mobileHoldTestBridge(t)
	t.Setenv(featureSwitchesEnv, "")
	if bridge.mobileHoldsAvailable(true) {
		t.Fatal("holds reached the phone with calendar-holds switched off")
	}
	t.Setenv(featureSwitchesEnv, string(featureCalendarHolds))
	if !bridge.mobileHoldsAvailable(true) {
		t.Fatal("holds must reach the phone once calendar-holds is switched on")
	}
}

func TestMobileHoldProposalCapabilityAndScopes(t *testing.T) {
	bridge, _, _ := mobileHoldTestBridge(t)
	device := mobileTestDevice(mobileScopeHandoff, mobileScopeBriefRead)
	if bridge.mobileHoldsAvailable(false) || !bridge.mobileHoldsAvailable(true) {
		t.Fatal("capability must require calendar permission")
	}
	if err := bridge.app.SetOfferHolds(false); err != nil {
		t.Fatal(err)
	}
	if bridge.mobileHoldsAvailable(true) {
		t.Fatal("capability ignored preference")
	}
	if err := bridge.app.SetOfferHolds(true); err != nil {
		t.Fatal(err)
	}
	code, proposal := mobileHoldProposalCall(t, bridge, device, true)
	if code != http.StatusOK || proposal.Proposal.Thing.Title != "Q4 plan" || len(proposal.Proposal.Slots) == 0 || len(proposal.Proposal.Slots) > 4 || len(proposal.Calendars) != 1 || proposal.Calendars[0].Title != "Work" || proposal.Token == "" {
		t.Fatalf("proposal %d %+v", code, proposal)
	}
	if code, _ := mobileHoldProposalCall(t, bridge, device, false); code != http.StatusServiceUnavailable {
		t.Fatalf("permission response %d", code)
	}
	if code, _ := mobileHoldProposalCall(t, bridge, mobileTestDevice(mobileScopeHandoff), true); code != http.StatusForbidden {
		t.Fatalf("source scope response %d", code)
	}
	for _, route := range []struct{ method, path string }{{"GET", "/holds/propose"}, {"GET", "/holds/status"}, {"POST", "/holds/actions"}} {
		w := mobileTestChatCall(bridge, mobileTestDevice(mobileScopeBriefRead), route.method, route.path, "")
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s: %d", route.method, route.path, w.Code)
		}
	}
	other := device
	other.DeviceID = "different-device"
	if _, err := bridge.readHoldToken(other, proposal.Token); err == nil {
		t.Fatal("proposal accepted for another phone")
	}
}

func TestMobileHoldActionsUseCoordinatorAndReceipts(t *testing.T) {
	bridge, calendar, now := mobileHoldTestBridge(t)
	device := mobileTestDevice(mobileScopeHandoff, mobileScopeBriefRead)
	code, proposal := mobileHoldProposalCall(t, bridge, device, true)
	if code != http.StatusOK {
		t.Fatalf("proposal %d", code)
	}
	input := mobileHoldAction{IdempotencyKey: "hold1", Action: "hold", Token: proposal.Token, Slot: proposal.Proposal.Slots[len(proposal.Proposal.Slots)-1], CalendarID: "work"}
	first := mobileHoldActionCall(t, bridge, device, input, true)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"state":"pending_write"`) || len(calendar.saved) != 0 {
		t.Fatalf("hold %d %s", first.Code, first.Body.String())
	}
	second := mobileHoldActionCall(t, bridge, device, input, true)
	if second.Code != first.Code || second.Body.String() != first.Body.String() {
		t.Fatal("receipt did not replay")
	}
	changed := input
	changed.CalendarID = "other"
	if got := mobileHoldActionCall(t, bridge, device, changed, true); got.Code != http.StatusConflict {
		t.Fatalf("key reuse %d", got.Code)
	}
	status := httptest.NewRecorder()
	bridge.serveHoldStatus(status, httptest.NewRequest(http.MethodGet, "/mobile/v1/holds/status?id="+proposal.Proposal.ID, nil), device)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"state":"pending_write"`) {
		t.Fatalf("status %d %s", status.Code, status.Body.String())
	}
	undo := mobileHoldAction{IdempotencyKey: "undo1", Action: "undo_hold", HoldID: proposal.Proposal.ID}
	if got := mobileHoldActionCall(t, bridge, device, undo, false); got.Code != http.StatusOK {
		t.Fatalf("undo %d %s", got.Code, got.Body.String())
	}
	*now = now.Add(6 * time.Second)
	if err := bridge.app.holds.runDue(); err != nil || len(calendar.saved) != 0 {
		t.Fatalf("undo wrote event: %v", err)
	}
	code, proposal = mobileHoldProposalCall(t, bridge, device, true)
	if code != http.StatusOK {
		t.Fatalf("second proposal %d", code)
	}
	input = mobileHoldAction{IdempotencyKey: "hold2", Action: "hold", Token: proposal.Token, Slot: proposal.Proposal.Slots[len(proposal.Proposal.Slots)-1], CalendarID: "work"}
	if got := mobileHoldActionCall(t, bridge, device, input, true); got.Code != http.StatusOK {
		t.Fatalf("hold2 %d %s", got.Code, got.Body.String())
	}
	*now = now.Add(6 * time.Second)
	if err := bridge.app.holds.runDue(); err != nil || len(calendar.saved) != 1 {
		t.Fatalf("write: %v %d", err, len(calendar.saved))
	}
	remove := mobileHoldAction{IdempotencyKey: "remove", Action: "remove_hold", HoldID: proposal.Proposal.ID}
	if got := mobileHoldActionCall(t, bridge, device, remove, true); got.Code != http.StatusOK || len(calendar.removed) != 1 {
		t.Fatalf("remove %d %s", got.Code, got.Body.String())
	}
	if got := mobileHoldActionCall(t, bridge, device, remove, true); got.Code != http.StatusOK || len(calendar.removed) != 1 {
		t.Fatal("remove repeated")
	}
	restore := mobileHoldAction{IdempotencyKey: "restore", Action: "undo_remove_hold", HoldID: proposal.Proposal.ID}
	if got := mobileHoldActionCall(t, bridge, device, restore, true); got.Code != http.StatusOK || len(calendar.saved) != 2 {
		t.Fatalf("restore %d %s", got.Code, got.Body.String())
	}
}

func TestMobileHoldExpiredAndTamperedProposalNeverWrites(t *testing.T) {
	bridge, calendar, now := mobileHoldTestBridge(t)
	device := mobileTestDevice(mobileScopeHandoff, mobileScopeBriefRead)
	code, proposal := mobileHoldProposalCall(t, bridge, device, true)
	if code != http.StatusOK {
		t.Fatalf("proposal %d", code)
	}
	input := mobileHoldAction{IdempotencyKey: "tampered", Action: "hold", Token: proposal.Token + "x", Slot: proposal.Proposal.Slots[0], CalendarID: "work"}
	if got := mobileHoldActionCall(t, bridge, device, input, true); got.Code != http.StatusConflict {
		t.Fatalf("tampered token %d %s", got.Code, got.Body.String())
	}
	start, err := time.Parse(time.RFC3339, input.Slot.Start)
	if err != nil {
		t.Fatal(err)
	}
	*now = start.Add(time.Second)
	input.IdempotencyKey, input.Token = "expired", proposal.Token
	if got := mobileHoldActionCall(t, bridge, device, input, true); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "time_passed") {
		t.Fatalf("expired %d %s", got.Code, got.Body.String())
	}
	if len(calendar.saved) != 0 {
		t.Fatal("invalid proposal wrote an event")
	}
}

func TestMobileHoldRejectsChangedSource(t *testing.T) {
	bridge, calendar, _ := mobileHoldTestBridge(t)
	device := mobileTestDevice(mobileScopeHandoff, mobileScopeBriefRead)
	code, proposal := mobileHoldProposalCall(t, bridge, device, true)
	if code != http.StatusOK {
		t.Fatalf("proposal %d", code)
	}
	entry, err := bridge.app.handoffStore.Get("plan")
	if err != nil {
		t.Fatal(err)
	}
	entry.Title = "Changed plan"
	if _, err := bridge.app.handoffStore.Save(entry); err != nil {
		t.Fatal(err)
	}
	input := mobileHoldAction{IdempotencyKey: "changed", Action: "hold", Token: proposal.Token, Slot: proposal.Proposal.Slots[0], CalendarID: "work"}
	if got := mobileHoldActionCall(t, bridge, device, input, true); got.Code != http.StatusConflict || len(calendar.saved) != 0 {
		t.Fatalf("changed source %d %s", got.Code, got.Body.String())
	}
}

func TestMobileHoldOfferNeedsAnUnambiguousThing(t *testing.T) {
	for _, tc := range []struct{ title, thing string }{
		{"Hold today 2 to 4 PM for the Q4 plan", "Q4 plan"},
		{"把今天下午 2 点到 4 点留给 Q4 计划", "Q4 计划"},
		{"Hold a block", ""},
	} {
		if got := mobileHoldOfferThing(tc.title); got != tc.thing {
			t.Errorf("%q: got %q, want %q", tc.title, got, tc.thing)
		}
	}
	offer := mobileOffer{ID: "suggestion.test", Title: "把今天下午 2 点到 4 点留给 Q4 计划", Prompt: "Plan"}
	if _, ok := mobileOfferForPhone(offer, nil); ok {
		t.Fatal("hold escaped the legacy offer filter")
	}
	if _, ok := mobileOfferForPhoneWithHolds(offer, nil, true); !ok {
		t.Fatal("typed hold was withheld from hold source lookup")
	}
}

func TestMobileHoldOfferOptInAndTaskThread(t *testing.T) {
	bridge := mobileTestOfferBridge(t)
	identity, err := createMobileHubIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bridge.identity = &identity
	tasks, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bridge.app.handoffStore = tasks
	now := time.Now().Truncate(time.Second)
	if err := bridge.app.store.SetUIState(context.Background(), calendarLastSyncKey, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	calendar := &fakeHoldEventStore{calendars: []holdCalendar{{ID: "work", Title: "Work", IsDefault: true}}}
	bridge.app.holds = &holdCoordinator{db: bridge.app.activity.store.db, calendar: calendar, now: func() time.Time { return now }, verifyCoverage: func(event holdEvent) error {
		return bridge.app.holdCoverageWithAccess(event, now, true)
	}}
	item, err := bridge.app.activity.store.GetInitiative("idea")
	if err != nil {
		t.Fatal(err)
	}
	item.Title = "Hold today 2 to 4 PM for the Q4 plan"
	item.ExecutionPlan.Automation.Name = item.Title
	if _, err := bridge.app.activity.store.db.Exec(`UPDATE initiatives SET title = ?, execution_plan_json = ? WHERE id = ?`, item.Title, encodeInitiativeJSON(item.ExecutionPlan, "{}"), item.ID); err != nil {
		t.Fatal(err)
	}
	device := mobileTestDevice(mobileScopeOffersRead, mobileScopeHandoff, mobileScopeBriefRead)
	legacy, err := bridge.mobileOfferListForDevice(t.Context(), device, false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, offer := range legacy.Offers {
		if offer.ID == "suggestion.idea" {
			t.Fatal("legacy phone received hold")
		}
	}
	page, err := bridge.mobileOfferListForDevice(t.Context(), device, true, true)
	if err != nil {
		t.Fatal(err)
	}
	var offer mobileOffer
	for _, candidate := range page.Offers {
		if candidate.ID == "suggestion.idea" {
			offer = candidate
		}
	}
	if offer.Hold == nil || offer.Hold.Proposal.Thing.Title != "Q4 plan" || len(offer.Hold.Proposal.Slots) == 0 {
		t.Fatalf("hold offer %+v", offer)
	}
	hold := offer.Hold
	input := mobileHoldAction{IdempotencyKey: "offer-hold", Action: "hold", Token: hold.Token, Slot: hold.Proposal.Slots[len(hold.Proposal.Slots)-1], CalendarID: "work"}
	response := mobileHoldActionCall(t, bridge, device, input, true)
	if response.Code != http.StatusOK {
		t.Fatalf("hold %d %s", response.Code, response.Body.String())
	}
	var reply struct {
		Data HoldStatus `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil || reply.Data.TaskID == "" {
		t.Fatalf("result %s %v", response.Body.String(), err)
	}
	entry, err := tasks.Get(reply.Data.TaskID)
	if err != nil || entry.Thread == nil || len(entry.Thread.Leftovers) != 1 || entry.Thread.Leftovers[0].Ref != hold.Proposal.ID {
		t.Fatalf("task %+v: %v", entry, err)
	}
	if len(calendar.saved) != 0 {
		t.Fatal("wrote before Undo elapsed")
	}
	now = now.Add(6 * time.Second)
	if err := bridge.app.holds.runDue(); err != nil {
		t.Fatal(err)
	}
	status := httptest.NewRecorder()
	bridge.serveHoldStatus(status, httptest.NewRequest(http.MethodGet, "/mobile/v1/holds/status?id="+hold.Proposal.ID, nil), device)
	entry, err = tasks.Get(reply.Data.TaskID)
	if status.Code != http.StatusOK || entry.Status != handoff.StatusActive || len(calendar.saved) != 1 {
		t.Fatalf("status %d task %+v saves %d err %v", status.Code, entry, len(calendar.saved), err)
	}
	detail, visible := bridge.app.mobileTaskThreadDetail(t.Context(), entry, nil)
	if !visible || detail.Hold == nil || detail.Hold.State != "held" || detail.Group != mobileTaskGroupDone || detail.Outcome == nil || detail.Outcome.Kind != handoff.OutcomeHoldPlaced || !containsString(detail.Actions, mobileTaskActionMarkDone) {
		t.Fatalf("detail %+v", detail)
	}
}
