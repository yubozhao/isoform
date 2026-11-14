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

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
)

func newAttendancePrivacyFixture(t *testing.T, state string) (*App, mobileBriefInput) {
	t.Helper()
	app := newPrivacyReadTestApp(t)
	prior := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(prior) })
	pii.SetUserSensitiveApps(nil)
	event, activity, _ := attendanceFixture(t, state)
	storeEvent(t, app.store, event)
	var events []activitymon.ActivityEvent
	for _, item := range activity {
		events = append(events, item.Event)
	}
	if err := app.activity.store.SaveSession(ActivitySessionRow{
		ID: "fixture-session", StartTime: event.Start.Add(-time.Hour).UnixMilli(), EndTime: event.End.Add(time.Hour).UnixMilli(),
		AppNames: `["Zoom","Code"]`, TriageStatus: "done",
	}, events, nil); err != nil {
		t.Fatal(err)
	}
	return app, mobileBriefInput{AsOf: event.Start.Add(2 * time.Hour), WindowStart: event.Start.Add(-time.Hour), Slot: mobileBriefSlotOnRequest, Location: time.UTC}
}

func TestMobileBriefAttendanceCollectionRechecksCurrentPrivacy(t *testing.T) {
	for _, state := range []string{"attended", "skipped"} {
		for _, privacy := range []string{"readable", "closed-eyes-after-meeting", "paused-after-meeting", "sensitive-session", "private-window-outside-slot"} {
			t.Run(state+"/"+privacy, func(t *testing.T) {
				app, in := newAttendancePrivacyFixture(t, state)
				switch privacy {
				case "closed-eyes-after-meeting":
					app.closedEyes.enabled.Store(true)
					app.saveObservationPauses([]observationPause{{Start: in.AsOf.Add(-time.Hour).UnixMilli()}})
				case "paused-after-meeting":
					// Meeting ended 10:30; pause at 11:00; brief at noon.
					app.saveObservationPauses([]observationPause{{Start: in.AsOf.Add(-time.Hour).UnixMilli()}})
				case "sensitive-session":
					// Sensitive provenance is outside the calendar slot and absent
					// from the call samples themselves.
					pii.SetUserSensitiveApps(map[string]string{"com.example.diary": "Diary"})
					if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET app_names = '["Zoom","Code","Diary"]' WHERE id = 'fixture-session'`); err != nil {
						t.Fatal(err)
					}
				case "private-window-outside-slot":
					if _, err := app.activity.store.db.Exec(`INSERT INTO activity_events (session_id,timestamp,kind,data,sort_order) VALUES ('fixture-session',?,'focus','{"appName":"Browser","privateWindow":true}',100)`, in.AsOf.Add(-3*time.Hour).UnixMilli()); err != nil {
						t.Fatal(err)
					}
				}
				collectMobileBriefAttendance(app, &in)
				brief := buildMobileBriefV1(in)
				store := newMobileBriefStore(t.TempDir())
				if err := store.save(brief, in.AsOf); err != nil {
					t.Fatal(err)
				}
				stored, ok := store.load(brief.BriefID)
				if !ok {
					t.Fatal("brief not stored")
				}
				prompt := mobileBriefTestPrompt(brief, "English")
				switch privacy {
				case "closed-eyes-after-meeting", "paused-after-meeting":
					if len(in.Attendance) != 0 || len(stored.MeetingAttendance) != 0 || strings.Contains(prompt, "Review with Tim") {
						t.Fatalf("paused collection retained attendance: input=%+v stored=%+v prompt=%s", in.Attendance, stored.MeetingAttendance, prompt)
					}
				case "readable":
					if len(stored.MeetingAttendance) != 1 || stored.MeetingAttendance[0].State != state {
						t.Fatalf("readable fixture = %+v", stored.MeetingAttendance)
					}
				default:
					if len(stored.MeetingAttendance) != 1 || stored.MeetingAttendance[0].State != "unknown" {
						t.Fatalf("withheld session supported attendance: %+v", stored.MeetingAttendance)
					}
				}
			})
		}
	}
}

func TestMobileBriefAttendanceDeliveryRechecksStoredSessions(t *testing.T) {
	for _, privacy := range []string{"readable", "closed-eyes", "sensitive", "protected", "purged", "expired"} {
		t.Run(privacy, func(t *testing.T) {
			app, in := newAttendancePrivacyFixture(t, "skipped")
			collectMobileBriefAttendance(app, &in)
			brief := buildMobileBriefV1(in)
			brief.Narrative = []MobileBriefPara{{Text: "Review with Tim needs review", Refs: brief.MeetingAttendance[0].Refs}}
			brief.NarrativeStatus = "ok"
			t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
			svc := newMobileBriefService(app)
			svc.location, svc.now = time.UTC, func() time.Time { return in.AsOf }
			app.mobileBriefSvc = svc
			if err := svc.store.save(brief, in.AsOf); err != nil {
				t.Fatal(err)
			}
			// Force a disk round trip: in-memory observations cannot authorize
			// later reads or a publication after the app has restarted.
			stored, ok := svc.store.load(brief.BriefID)
			if !ok {
				t.Fatal("stored brief missing")
			}
			switch privacy {
			case "closed-eyes":
				app.closedEyes.enabled.Store(true)
			case "sensitive":
				pii.SetUserSensitiveApps(map[string]string{"com.example.code": "Code"})
			case "protected":
				if err := app.activity.store.UpdateTriageStatus("fixture-session", "protected"); err != nil {
					t.Fatal(err)
				}
			case "purged":
				if err := app.activity.store.DeleteSession("fixture-session"); err != nil {
					t.Fatal(err)
				}
			case "expired":
				// No retained privacy receipt remains to authorize this source.
				if _, err := app.activity.store.db.Exec(`DELETE FROM activity_sessions WHERE id = 'fixture-session'`); err != nil {
					t.Fatal(err)
				}
			}
			check := func(wire []byte, delivered MobileBriefV1) {
				t.Helper()
				for _, forbidden := range []string{"fixture-session", "sourceSessionIds", "attendanceSourceSessions"} {
					if strings.Contains(string(wire), forbidden) {
						t.Fatalf("wire leaked local dependencies: %s", wire)
					}
				}
				// Seal v2: retention removing a source is not a privacy action. The
				// phone reads the sealed copy, so it shows what the computer shows.
				// Every explicit privacy action still withdraws.
				if privacy == "expired" {
					computer, ok := svc.GetMobileBrief(brief.BriefID)
					if !ok || len(computer.MeetingAttendance) != len(delivered.MeetingAttendance) {
						t.Fatalf("the phone and the computer read different attendance: %d, %d", len(delivered.MeetingAttendance), len(computer.MeetingAttendance))
					}
					return
				}
				if privacy == "readable" {
					if len(delivered.MeetingAttendance) != 1 || delivered.MeetingAttendance[0].State != "skipped" || len(delivered.Sections.Review) != 0 {
						t.Fatalf("readable attendance lost: %+v", delivered)
					}
				} else if len(delivered.MeetingAttendance) != 0 || strings.Contains(string(wire), "Review with Tim") || strings.Contains(string(wire), "1:1 with Ann") || strings.Contains(string(wire), "active_elsewhere_on_computer") || strings.Contains(string(wire), "calendar-review") {
					t.Fatalf("withdrawn attendance delivered: %s", wire)
				}
			}
			dir := t.TempDir()
			identity, err := createMobileHubIdentity(dir)
			if err != nil {
				t.Fatal(err)
			}
			bridge, err := newMobileBridge(app, dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(bridge.Stop)
			bridge.identity = &identity
			phone := newMobileTestPhone(t)
			mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead})
			for _, path := range []string{"/mobile/v1/brief/latest", "/mobile/v1/briefs/" + brief.BriefID} {
				recorder := httptest.NewRecorder()
				bridge.handleAuthorized(recorder, httptest.NewRequest(http.MethodGet, path, nil), mobileTestDevice(mobileScopeBriefRead))
				if recorder.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", path, recorder.Code, recorder.Body.String())
				}
				var response struct {
					Data MobileBriefV1 `json:"data"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				check(recorder.Body.Bytes(), response.Data)
			}
			if err := bridge.publishBrief(stored, true); err != nil {
				t.Fatal(err)
			}
			queued := bridge.pendingBriefs[phone.deviceID]
			wire := phone.openFromHub(t, identity.Public, identity.HubID, queued.Env)
			var pushed MobileBriefV1
			if err := json.Unmarshal(wire, &pushed); err != nil {
				t.Fatal(err)
			}
			check(wire, pushed)
			local, err := os.ReadFile(filepath.Join(svc.store.dir, brief.BriefID+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(local), "fixture-session") || !strings.Contains(string(local), "1:1 with Ann") {
				t.Fatal("delivery lost or mutated the local dependency ledger")
			}
		})
	}
}

func TestMobileBriefAttendanceDeliveryRechecksDeviceSeed(t *testing.T) {
	app, in := newAttendancePrivacyFixture(t, "skipped")
	event, _, _ := attendanceFixture(t, "skipped")
	seedAt := event.Start.Add(-time.Second).UnixMilli()
	if err := app.activity.store.SaveSession(ActivitySessionRow{
		ID: "device-seed-session", StartTime: seedAt, EndTime: event.Start.UnixMilli(),
		AppNames: `["Code"]`, TriageStatus: "done",
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.activity.store.db.Exec(`UPDATE activity_events SET session_id = 'device-seed-session' WHERE session_id = 'fixture-session' AND kind = 'device_in_use'`); err != nil {
		t.Fatal(err)
	}
	collectMobileBriefAttendance(app, &in)
	brief := buildMobileBriefV1(in)
	if len(brief.MeetingAttendance) != 1 || brief.MeetingAttendance[0].State != "skipped" {
		t.Fatalf("seeded skipped fixture = %+v", brief.MeetingAttendance)
	}
	if got := app.mobileBriefForDelivery(app.mobileBriefForDelivery(brief)); len(got.MeetingAttendance) != 1 {
		t.Fatal("readable delivery projection is not repeatable")
	}
	if err := app.activity.store.UpdateTriageStatus("device-seed-session", "protected"); err != nil {
		t.Fatal(err)
	}
	if got := app.mobileBriefForDelivery(brief); len(got.MeetingAttendance) != 0 || len(got.Sections.Review) != 0 {
		t.Fatalf("withdrawn device seed still supports skipped: %+v", got)
	}
}

func TestMobileBriefAttendanceStoredUnknownAndLegacyPrivacy(t *testing.T) {
	app, in := newAttendancePrivacyFixture(t, "unknown")
	collectMobileBriefAttendance(app, &in)
	brief := buildMobileBriefV1(in)
	store := newMobileBriefStore(t.TempDir())
	if err := store.saveCurrentToday(brief); err != nil {
		t.Fatal(err)
	}
	stored, ok := store.currentToday()
	if !ok {
		t.Fatal("Today document missing")
	}
	if got := app.mobileBriefForDelivery(stored); len(got.MeetingAttendance) != 1 || got.MeetingAttendance[0].State != "unknown" {
		t.Fatalf("readable unknown lost on store round trip: %+v", got.MeetingAttendance)
	}
	if err := app.activity.store.DeleteSession("fixture-session"); err != nil {
		t.Fatal(err)
	}
	if got := app.mobileBriefForDelivery(stored); len(got.MeetingAttendance) != 0 {
		t.Fatal("unknown state retained the reason from a purged session")
	}
	// Old stored documents had no local dependencies. Do not authorize their
	// attendance through a bare calendar occurrence ID.
	wire, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	var legacy MobileBriefV1
	if err := json.Unmarshal(wire, &legacy); err != nil {
		t.Fatal(err)
	}
	if got := app.mobileBriefForDelivery(legacy); len(got.MeetingAttendance) != 0 {
		t.Fatal("legacy attendance without checked provenance was delivered")
	}
}

// Seal v2: re-sealing is idempotent. Attendance the first seal kept stays when
// retention removes its session later, with its receipt or (legacy data) with
// none; an explicit delete of that session still withdraws it.
func TestMobileBriefResealKeepsAttendanceAfterRetention(t *testing.T) {
	for _, removal := range []string{"retention", "no-receipt", "explicit"} {
		t.Run(removal, func(t *testing.T) {
			app, in := newAttendancePrivacyFixture(t, "skipped")
			collectMobileBriefAttendance(app, &in)
			brief := buildMobileBriefV1(in)
			brief.Narrative = []MobileBriefPara{{Text: "Review with Tim needs review", Refs: brief.MeetingAttendance[0].Refs}}
			brief.NarrativeStatus = "ok"
			t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
			svc := newMobileBriefService(app)
			svc.location, svc.now = time.UTC, func() time.Time { return in.AsOf }
			app.mobileBriefSvc = svc
			if err := svc.store.save(brief, in.AsOf); err != nil {
				t.Fatal(err)
			}
			stored, ok := svc.store.load(brief.BriefID)
			if !ok {
				t.Fatal("stored brief missing")
			}
			first, _, wrote := svc.seal(context.Background(), stored)
			if !wrote || len(first.MeetingAttendance) != 1 || len(first.Narrative) != 1 {
				t.Fatalf("first seal = attendance %d, narrative %d, wrote %v", len(first.MeetingAttendance), len(first.Narrative), wrote)
			}
			switch removal {
			case "retention":
				if err := app.activity.store.deleteSessionForRetention("fixture-session"); err != nil {
					t.Fatal(err)
				}
			case "no-receipt":
				if _, err := app.activity.store.db.Exec(`DELETE FROM activity_sessions WHERE id = 'fixture-session'`); err != nil {
					t.Fatal(err)
				}
			case "explicit":
				if err := app.activity.store.DeleteSession("fixture-session"); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				again, _, wrote := svc.seal(context.Background(), stored)
				if !wrote {
					t.Fatal("re-seal not saved")
				}
				read, err := svc.finishedRead(stored)
				if err != nil {
					t.Fatal(err)
				}
				switch removal {
				case "retention", "no-receipt":
					if len(again.MeetingAttendance) != 1 || len(read.MeetingAttendance) != 1 || len(read.Narrative) != 1 {
						t.Fatalf("re-seal %d dropped what the first seal kept: attendance %d/%d, narrative %d", i, len(again.MeetingAttendance), len(read.MeetingAttendance), len(read.Narrative))
					}
				case "explicit":
					if len(again.MeetingAttendance) != 0 || len(read.MeetingAttendance) != 0 {
						t.Fatalf("an explicit delete did not withdraw the attendance: %d/%d", len(again.MeetingAttendance), len(read.MeetingAttendance))
					}
				}
			}
		})
	}
}
