package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
)

func TestDailySummaryStaysOnMacAcrossPhoneReadsAndPush(t *testing.T) {
	rig := newHandoffTestRig(t)
	now := mobileBriefTestDay(8, 0)
	svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: now})
	svc.app, rig.app.mobileBriefSvc = rig.app, svc
	insertTestSession(t, rig.activity, "protected-summary-source", "protected", now.Add(-time.Hour).UnixMilli())
	secret := "Protected session: acquisition price is 42 million."
	if _, err := rig.activity.SaveSummaryWithLanguage(now.Format("2006-01-02"), "daily", secret, "test", false, rig.app.currentUILanguage()); err != nil {
		t.Fatal(err)
	}
	if rig.app.activityDependencyAllowsModelRead("protected-summary-source", nil) {
		t.Fatal("source is not protected")
	}
	// Use the real collector, builder, persistence and phone serialization paths.
	in := mobileBriefInput{Slot: mobileBriefSlotMorning, AsOf: now, Location: time.UTC}
	collectMobileBriefToday(rig.app, &in)
	brief := buildMobileBriefV1(in)
	if err := svc.store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	checkSafe := func(where string, data []byte) {
		t.Helper()
		if strings.Contains(string(data), secret) || strings.Contains(string(data), `"dailySummary"`) {
			t.Errorf("%s leaked an ungated activity summary", where)
		}
	}
	stored, err := os.ReadFile(filepath.Join(svc.store.dir, brief.BriefID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	checkSafe("stored brief", stored)
	local, err := rig.app.GetLatestBrief()
	if err != nil {
		t.Fatal(err)
	}
	localJSON, _ := json.Marshal(local)
	var view map[string]json.RawMessage
	if err := json.Unmarshal(localJSON, &view); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(view["dailySummary"]), secret) {
		t.Error("local Today lost its Mac-only summary")
	}

	dir := t.TempDir()
	identity, err := createMobileHubIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := newMobileBridge(rig.app, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	bridge.identity = &identity
	phone := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead})
	for _, legacy := range []bool{false, true} {
		// Old files may already carry this field; decoding must not relay it again.
		if legacy {
			var old map[string]any
			if err := json.Unmarshal(stored, &old); err != nil {
				t.Fatal(err)
			}
			old["dailySummary"] = map[string]string{"text": secret, "at": now.Format(time.RFC3339)}
			data, _ := json.Marshal(old)
			if err := os.WriteFile(filepath.Join(svc.store.dir, brief.BriefID+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		for _, route := range []string{"/mobile/v1/brief/latest", "/mobile/v1/briefs/" + brief.BriefID} {
			response := httptest.NewRecorder()
			bridge.handleAuthorized(response, httptest.NewRequest(http.MethodGet, route, nil), *bridge.devices[phone.deviceID])
			if response.Code != http.StatusOK {
				t.Fatalf("%s: %s", route, response.Body.String())
			}
			checkSafe(route, response.Body.Bytes())
		}
		latest, ok := svc.GetMobileBrief(brief.BriefID)
		if !ok {
			t.Fatal("stored brief missing")
		}
		if err := bridge.publishBrief(latest, true); err != nil {
			t.Fatal(err)
		}
		// This is the actual encrypted push envelope queued for the paired phone.
		frame, ok := bridge.pendingBriefs[phone.deviceID]
		if !ok {
			t.Fatal("push payload missing")
		}
		checkSafe("encrypted push", phone.openFromHub(t, identity.Public, identity.HubID, frame.Env))
	}
}

// Intended behavior change (brief-rebuild PR 1, signed off): a sealed brief keeps
// showing its handoffs when a source read fails. Reads touch no store.
func TestTodaySealedBriefKeepsHandoffsWhenSourceReadFails(t *testing.T) {
	for _, generated := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "generated"}[generated], func(t *testing.T) {
			rig := newHandoffTestRig(t)
			now := mobileBriefTestDay(7, 15)
			svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: now})
			svc.app = rig.app
			if generated {
				svc.checkDue(context.Background())
				svc.wg.Wait()
			}
			if _, err := rig.handoffs.Save(handoff.Entry{ID: "open-work", Title: "Review release", Body: "Check the draft", Status: handoff.StatusBlocked, BlockedReason: "interrupted", UpdatedAt: now.UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			// The save is a source change: the brief is sealed again in the background.
			svc.wg.Wait()
			original, ok := svc.LatestMobileBrief()
			if !ok || len(original.Sections.OpenLoops) != 1 {
				t.Fatal("open handoff missing before failure")
			}
			dir := rig.handoffs.Dir()
			if err := os.Rename(dir, dir+".saved"); err != nil {
				t.Fatal(err)
			}
			// A regular file in place of the directory fails even as Linux root.
			if err := os.WriteFile(dir, []byte("unavailable"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := rig.handoffs.List(); err == nil {
				t.Fatal("fixture did not fail the source read")
			}
			for range 2 {
				current, ok := svc.LatestMobileBrief()
				if !ok || len(current.Sections.OpenLoops) != 1 {
					t.Errorf("a sealed brief lost its handoff when the source read failed: %+v", current.Sections.OpenLoops)
				}
				persisted, ok := svc.GetMobileBrief(original.BriefID)
				if !ok || len(persisted.Sections.OpenLoops) != 1 {
					t.Error("the sealed brief stopped showing its handoff")
				}
			}
			if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(dir+".saved", dir); err != nil {
				t.Fatal(err)
			}
			if _, err := rig.handoffs.UpdateStatus("open-work", handoff.StatusInProgress); err != nil {
				t.Fatal(err)
			}
			if _, err := rig.handoffs.UpdateStatus("open-work", handoff.StatusCompleted); err != nil {
				t.Fatal(err)
			}
			svc.LatestMobileBrief()
			var persisted MobileBriefV1
			if generated {
				persisted, _ = svc.store.load(original.BriefID)
			} else {
				persisted, _ = svc.store.currentToday()
			}
			if len(persisted.Sections.OpenLoops) != 0 {
				t.Fatal("successful source read retained completed work")
			}
		})
	}
}

func TestTodayRetainsOnlyUnavailableSources(t *testing.T) {
	now := mobileBriefTestDay(8, 0)
	for _, source := range []string{"history", "schedule", "schedule_run", "memory", "knowledge", "deliverable"} {
		t.Run(source, func(t *testing.T) {
			makeInput := func() mobileBriefInput {
				return mobileBriefInput{Slot: mobileBriefSlotMorning, AsOf: now, Location: time.UTC, Today: &mobileBriefTodayInput{Context: map[string]MobileBriefItemContext{}, Unavailable: map[string]bool{}}}
			}
			in := makeInput()
			row := mobileBriefCandidate{SourceKind: source, SourceID: "kept", Title: "Previously read", At: now}
			var section string
			switch source {
			case "history":
				in.OpenLoops = []mobileBriefCandidate{row}
				section = "openLoops"
			case "schedule":
				in.Today.Upcoming = []mobileBriefCandidate{row}
				section = "upcoming"
			case "schedule_run":
				in.Schedules = []mobileBriefCandidate{row}
				section = "schedules"
			case "memory", "knowledge":
				in.Learned = []mobileBriefCandidate{row}
				section = "learned"
			case "deliverable":
				in.Deliverables = []mobileBriefCandidate{row}
				section = "deliverables"
			}
			in.Today.Context[mobileBriefSourceKey(source, "kept")] = MobileBriefItemContext{Preview: "Read earlier"}
			base := buildMobileBriefV1(in)
			for _, unavailable := range []bool{true, false} {
				next := makeInput()
				next.Today.Unavailable[source] = unavailable
				current := projectMobileBriefToday(base, next)
				data, _ := json.Marshal(current)
				// Memory and knowledge are never restored when their read fails: their
				// privacy gate cannot be run again.
				want := unavailable && source != "memory" && source != "knowledge"
				if strings.Contains(string(data), "Previously read") != want || strings.Contains(string(data), "Read earlier") != want {
					t.Errorf("%s unavailable=%v: %s", section, unavailable, data)
				}
			}
		})
	}
}

func TestTodayRetainsInsightsWhenSourceFileReadFails(t *testing.T) {
	for _, source := range []string{"memory", "knowledge"} {
		t.Run(source, func(t *testing.T) {
			rig := newHandoffTestRig(t)
			now := mobileBriefTestDay(8, 0)
			svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: now})
			svc.app = rig.app
			dirFn := memoryDir
			if source == "knowledge" {
				dirFn = knowledgeDir
			}
			dir, err := dirFn()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "insight.md")
			content := "# Remember the release\n- **Date**: " + now.Format(time.RFC3339) + "\nPreviously read content."
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, now, now); err != nil {
				t.Fatal(err)
			}
			// The memory watcher's scan takes the snapshot the removal is found against.
			memDir, err := memoryDir()
			if err != nil {
				t.Fatal(err)
			}
			knDir, err := knowledgeDir()
			if err != nil {
				t.Fatal(err)
			}
			watcher := &MemoryStore{baseDir: memDir, knowledgeDir: knDir}
			watcher.detectFileChanges()
			original, ok := svc.LatestMobileBrief()
			if !ok || len(original.Sections.Learned) != 1 {
				t.Fatal("insight missing before read failure")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			// Directory enumeration succeeds, but reading this entry fails on
			// every OS user, including root. The source must report the failure.
			if err := os.Symlink(t.TempDir(), path); err != nil {
				t.Fatal(err)
			}
			if _, err := os.ReadFile(path); err == nil {
				t.Fatal("fixture did not fail the file read")
			}
			checkMemoryFolderNow(rig.app)
			// The memory watcher reports the changed folder (the same call its
			// poll makes); the sealed briefs are amended before a read shows them.
			// The watcher's scan finds the entry gone from its snapshot and reports the
			// removal, which the poll (pollFileChanges) passes on as a withdrawal; the
			// sealed briefs are amended before a read shows them.
			if _, removed := watcher.detectFileChanges(); len(removed) > 0 {
				memorySourcesRemovedOnDisk(removed)
			}
			// The source session's privacy gate cannot be run for an unreadable file, so
			// the insight is not shown to the phone until the file reads again.
			current, ok := svc.LatestMobileBrief()
			if ok && len(current.Sections.Learned) != 0 {
				t.Errorf("an insight whose source cannot be read was still served: %s", mustJSON(t, current))
			}
			// The rest of the brief shows, and the count says one item was left out.
			if ok && current.Limits.ExcludedSensitiveItems == 0 {
				t.Errorf("the left-out insight is not counted in the excluded items: %+v", current.Limits)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			checkMemoryFolderNow(rig.app)
			svc.LatestMobileBrief()
			stored, _ := svc.store.currentToday()
			if len(stored.Sections.Learned) != 0 {
				t.Fatal("successful source read kept a deleted insight")
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
