package main

import (
	"strings"
	"testing"
)

// The privacy change (a withdrawn source) is durable before its action returns.
// These tests cover the states around the amendment that follows it: a restart
// before the amendment runs, a store that cannot finish it, and a queued copy
// that must not be replayed with the words the change removed.

const amendSentinel = "Protected meeting title"

// withdrawWithoutAmendment performs the action and leaves the amendment unstarted,
// as if the app stopped right after the action. The live service is detached
// for the rest of the test and reattached in cleanup.
func withdrawWithoutAmendment(t *testing.T, app *App) {
	t.Helper()
	live := mobileBriefLive.Swap(nil)
	t.Cleanup(func() { mobileBriefLive.Store(live) })
	if err := app.activity.store.DeleteSession("brief-delivery-source"); err != nil {
		t.Fatal(err)
	}
}

// A restart between the action and the amendment: the in-memory epoch is lost,
// the durable epoch is read back, and the next read amends before it shows.
func TestAmendmentSurvivesRestartBeforeItRuns(t *testing.T) {
	app, svc, _ := keptTextFixture(t, "brief-delivery-source")
	withdrawWithoutAmendment(t, app)

	// The restart is simulated on the epoch globals, which other tests share.
	// They are restored afterwards, so the epoch never moves backwards for them.
	savedEpoch := mobileBriefPrivacyEpoch.Load()
	savedDir := mobileBriefPrivacyEpoch.SwapSavedDir("")
	mobileBriefPrivacyEpoch.Store(0)
	t.Cleanup(func() {
		mobileBriefPrivacyEpoch.SwapSavedDir(savedDir)
		mobileBriefPrivacyEpoch.Store(max(savedEpoch, mobileBriefPrivacyEpoch.Load()))
	})
	adoptMobileBriefPrivacyEpoch(svc.store.dir)

	latest, _ := svc.LatestMobileBrief()
	if len(latest.Narrative) != 0 {
		t.Fatalf("the words survived a restart before the amendment: %+v", latest.Narrative)
	}
	data := mustJSON(t, latest)
	if strings.Contains(string(data), amendSentinel) || strings.Contains(string(data), "brief-delivery-source") {
		t.Fatalf("withdrawn source still shown after restart: %s", data)
	}
}

// A store that cannot finish the amendment: the read leaves the affected words
// out (the placeholder), never the old text, and the stored copy is kept.
func TestAmendmentWithClosedStoreShowsNoOldText(t *testing.T) {
	app, svc, brief := keptTextFixture(t, "brief-delivery-source")
	withdrawWithoutAmendment(t, app)

	activity := app.activity
	app.activity = nil
	latest, _ := svc.LatestMobileBrief()
	app.activity = activity
	if len(latest.Narrative) != 0 || strings.Contains(string(mustJSON(t, latest)), amendSentinel) {
		t.Fatalf("old text shown while the amendment could not finish: %+v", latest.Narrative)
	}
	if stored := keptTextStored(t, svc, brief.BriefID); len(stored.Narrative) == 0 && stored.NarrativeStatus != "ok" {
		t.Fatal("the stored text was erased by a read that could not amend")
	}

	// Once the store is open again, the next read amends and the result stands.
	after, _ := svc.LatestMobileBrief()
	if len(after.Narrative) != 0 || strings.Contains(string(mustJSON(t, after)), amendSentinel) {
		t.Fatalf("amended read shows withdrawn text: %+v", after.Narrative)
	}
}

// A copy queued before the change is not replayed with the words the change
// removed: the replay re-checks the queued document against the current state.
func TestQueuedCopyIsNotReplayedAfterAmendment(t *testing.T) {
	app, svc, brief := keptTextFixture(t, "brief-delivery-source")
	stored := keptTextStored(t, svc, brief.BriefID)
	bridge := newMobileBriefPrivacyBridge(t, app)
	phone := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead})
	if err := bridge.publishBrief(stored, false); err != nil {
		t.Fatal(err)
	}
	queued := bridge.pendingBriefs[phone.deviceID]
	before := phone.openFromHub(t, bridge.identity.Public, bridge.identity.HubID, queued.Env)
	if !strings.Contains(string(before), amendSentinel) {
		t.Fatal("fixture did not queue the pre-change document")
	}

	withdrawWithoutAmendment(t, app)
	bridge.briefDeliveryMu.Lock()
	refreshed, err := bridge.refreshPendingBrief(queued)
	bridge.briefDeliveryMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	after := phone.openFromHub(t, bridge.identity.Public, bridge.identity.HubID, refreshed.Env)
	if strings.Contains(string(after), amendSentinel) {
		t.Fatal("a queued copy was replayed with withdrawn words")
	}
}
