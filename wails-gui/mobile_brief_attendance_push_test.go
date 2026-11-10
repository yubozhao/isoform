package main

import (
	"encoding/json"
	"strings"
	"testing"

	"wails-gui/internal/pii"
)

func TestMobileBriefAttendancePushRechecksSourcesAtReconnect(t *testing.T) {
	for _, privacy := range []string{"readable", "closed-eyes", "sensitive", "purged"} {
		t.Run(privacy, func(t *testing.T) {
			app, in := newAttendancePrivacyFixture(t, "skipped")
			collectMobileBriefAttendance(app, &in)
			brief := buildMobileBriefV1(in)
			if len(brief.MeetingAttendance) != 1 || brief.MeetingAttendance[0].State != "skipped" {
				t.Fatalf("fixture attendance = %+v", brief.MeetingAttendance)
			}
			brief.Narrative = []MobileBriefPara{{Text: "Review with Tim needs review.", Refs: brief.MeetingAttendance[0].Refs}}
			brief.NarrativeStatus = "ok"
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
			if err := bridge.publishBrief(brief, true); err != nil {
				t.Fatal(err)
			}
			queued := bridge.pendingBriefs[phone.deviceID]
			assertLocalOnly := func(data []byte) {
				t.Helper()
				for _, forbidden := range []string{"fixture-session", "sourceSessionIds", "attendanceSourceSessions"} {
					if strings.Contains(string(data), forbidden) {
						t.Fatalf("wire contains local session provenance: %s", data)
					}
				}
			}
			frameJSON, err := json.Marshal(queued)
			if err != nil {
				t.Fatal(err)
			}
			assertLocalOnly(frameJSON)
			queuedWire := phone.openFromHub(t, identity.Public, identity.HubID, queued.Env)
			assertLocalOnly(queuedWire)
			var queuedBrief MobileBriefV1
			if err := json.Unmarshal(queuedWire, &queuedBrief); err != nil {
				t.Fatal(err)
			}
			if len(queuedBrief.MeetingAttendance) != 1 || len(queuedBrief.Sections.Review) != 0 || len(queuedBrief.Narrative) != 1 {
				t.Fatalf("offline queue lost readable attendance: %+v", queuedBrief)
			}
			switch privacy {
			case "closed-eyes":
				app.closedEyes.enabled.Store(true)
			case "sensitive":
				pii.SetUserSensitiveApps(map[string]string{"com.example.code": "Code"})
			case "purged":
				if err := app.activity.store.DeleteSession("fixture-session"); err != nil {
					t.Fatal(err)
				}
			}
			// The actual relay registration flushes the offline envelope after
			// rechecking its local dependencies under the current read policy.
			relay := newMobileTestRelay(t, identity.Credential, identity.HubID)
			if err := bridge.enableRelay(relay.url(), false); err != nil {
				t.Fatal(err)
			}
			relay.acceptRegistration(t, mobileTestListed(phone))
			frameWire := relay.nextFrame(t, "brief").raw
			assertLocalOnly(frameWire)
			var delivered mobileBriefFrame
			if err := json.Unmarshal(frameWire, &delivered); err != nil {
				t.Fatal(err)
			}
			wire := phone.openFromHub(t, identity.Public, identity.HubID, delivered.Env)
			assertLocalOnly(wire)
			var got MobileBriefV1
			if err := json.Unmarshal(wire, &got); err != nil {
				t.Fatal(err)
			}
			if privacy == "readable" {
				if len(got.MeetingAttendance) != 1 || got.MeetingAttendance[0].State != "skipped" || len(got.Sections.Review) != 0 || len(got.Narrative) != 1 {
					t.Fatalf("reconnect dropped readable attendance: %+v", got)
				}
				if delivered.Env.Seq != queued.Env.Seq {
					t.Fatal("unchanged readable attendance replaced its encrypted envelope")
				}
				return
			}
			if len(got.MeetingAttendance) != 0 || len(got.Sections.Review) != 0 || len(got.Narrative) != 0 {
				t.Fatalf("reconnect retained withdrawn attendance: %+v", got)
			}
			for _, forbidden := range []string{"Review with Tim", "active_elsewhere_on_computer", "calendar-review"} {
				if strings.Contains(string(wire), forbidden) {
					t.Fatalf("reconnect leaked withdrawn attendance: %s", wire)
				}
			}
			if delivered.Env.Seq <= queued.Env.Seq {
				t.Fatal("withdrawal reused an encryption sequence for changed content")
			}
		})
	}
}
