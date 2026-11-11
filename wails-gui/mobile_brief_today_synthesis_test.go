package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A displayed citation is not the complete input dependency set. P contributed
// to the old synthesis even though its paragraph cites only the readable Q.
func TestMobileBriefTodayRefreshWithdrawsSynthesisBeforeDiscardingSources(t *testing.T) {
	for _, source := range []string{"memory", "purpose"} {
		for _, state := range []string{"protected", "missing", "readable", "completed-then-protected"} {
			for _, citations := range []string{"q-only", "p-and-q"} {
				t.Run(source+"/"+state+"/"+citations, func(t *testing.T) {
					if source == "memory" && state == "completed-then-protected" {
						t.Skip("completion applies to Purpose lifecycle")
					}
					app := newMobileBriefPurposeTestApp(t)
					t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
					now := time.Now()
					at := now.Add(-time.Hour)
					session := seedMobileBriefPurposeRun(t, app, "today-synthesis", 1, at, 1000, []string{"Editor"})
					writePrivacyMemory(t, "safe-today-q", "# Readable source Q\n\nA manually written note.\n")
					var pSourceID string
					switch source {
					case "memory":
						pSourceID = "private-today-p"
						writePrivacyMemory(t, "private-today-p", fmt.Sprintf("---\ntitle: Source P\nsource: dream\nread_provenance: '{\"version\":2,\"kinds\":[\"activity\"],\"sessions\":[%q]}'\n---\n\nUNRETAINED_SOURCE_DETAIL\n", session))
					case "purpose":
						detail := mobileTestPurpose(t, NewPurposeMapStore(app.activity.store.db), "today-synthesis-p", PurposeKindGoal, "Review the project plan", false, func(input *CreatePurposeOpenLoopInput) {
							evidence := input.Evidence[0]
							evidence.SourceKind, evidence.SourceID, evidence.SourceObservedAt = "activity_session", session, at.UnixMilli()
							evidence.EvidenceClass, evidence.IndependenceKey = PurposeEvidenceObservedUser, session
							input.Evidence = append(input.Evidence, evidence)
							input.DesiredOutcome = "UNRETAINED_SOURCE_DETAIL"
						})
						pSourceID = detail.OpenLoop.ID
					}
					now = time.Now()
					in := mobileBriefInput{Slot: mobileBriefSlotMorning, AsOf: now, WindowStart: now.Add(-time.Hour), Location: time.UTC}
					collectMobileBriefToday(app, &in)
					collectMobileBriefTodayPurposes(app, &in)
					base := buildMobileBriefV1(in)
					var p, q string
					for _, ref := range base.Evidence {
						if ref.SourceKind == source && ref.SourceID == pSourceID {
							p = ref.Ref
						}
						if ref.SourceKind == "memory" && ref.SourceID == "safe-today-q" {
							q = ref.Ref
						}
					}
					if p == "" || q == "" {
						t.Fatal("fixture needs readable P and Q citations")
					}
					refs := []string{q}
					if citations == "p-and-q" {
						refs = []string{p, q}
					}
					base.Narrative = []MobileBriefPara{{Text: "UNRETAINED_SYNTHESIS_FROM_P", Refs: refs}}
					base.NarrativeStatus = "ok"
					base.Offers = []MobileBriefOffer{{ID: "offer-1", Action: "draft_plan", Label: "Draft a plan", Text: "UNRETAINED_OFFER_FROM_P", Prompt: "UNRETAINED_OFFER_FROM_P", Refs: []string{q}}}
					base.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Bytes: 4, Mime: mobileBriefAudioMimeWAV}
					base.Audio.binding = mobileBriefAudioBinding(base, "en")
					base.DayShape = &MobileBriefDay{ActiveMinutes: 73, TopApps: []MobileBriefAppTime{{Name: "Editor", ActiveMinutes: 73}}}
					base.Counts = MobileBriefCounts{OpenLoopsByKind: map[string]int{"old_synthesis": 37}, LearningItems: 29}
					svc := newMobileBriefService(app)
					svc.location = time.UTC
					svc.now = func() time.Time { return now }
					app.mobileBriefSvc = svc
					if err := svc.store.save(base, now); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(svc.store.dir, base.BriefID+".wav"), []byte("wave"), 0600); err != nil {
						t.Fatal(err)
					}
					if safe := app.mobileBriefForDelivery(base); len(safe.Narrative) != 1 || safe.Audio == nil || safe.DayShape == nil {
						t.Fatal("fixture is not readable before the privacy change")
					}
					if state == "completed-then-protected" {
						ledger := NewPurposeMapStore(app.activity.store.db)
						detail, err := ledger.GetDetail(pSourceID)
						if err != nil {
							t.Fatal(err)
						}
						resolution := purposeTestAppendInput(detail, "synthesis-resolution", PurposeEventResolved, PurposeStanceResolve, PurposeEvidenceExplicitUser, "explicit-done", time.Now().UnixMilli())
						if _, _, err := ledger.AppendEvent(resolution); err != nil {
							t.Fatal(err)
						}
						now = time.Now().Add(time.Second)
						first, ok := svc.LatestMobileBrief()
						if !ok {
							t.Fatal("first refresh missing")
						}
						if citations == "q-only" && (len(first.Narrative) != 0 || first.Audio != nil) {
							t.Error("first refresh kept synthesis after uncited dependency disappeared")
						}
						reopened := newMobileBriefStore(filepath.Dir(svc.store.dir))
						if _, ok := reopened.load(base.BriefID); !ok {
							t.Fatal("real-store first refresh could not reopen")
						}
					}
					switch state {
					case "protected", "completed-then-protected":
						if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id=?`, session); err != nil {
							t.Fatal(err)
						}
					case "missing":
						if err := app.activity.store.DeleteSession(session); err != nil {
							t.Fatal(err)
						}
					}
					now = time.Now()
					// Today keeps the day's active time but drops a stored top-app list.
					day := *base.DayShape
					day.TopApps = []MobileBriefAppTime{}
					check := func(stage string, brief MobileBriefV1) {
						t.Helper()
						if state == "readable" {
							expectedAudio := *base.Audio
							if stage == "push" {
								expectedAudio.binding = ""
							}
							if len(brief.Offers) != 1 || len(brief.Narrative) != 1 || brief.Narrative[0].Text != base.Narrative[0].Text || brief.NarrativeStatus != "ok" || !reflect.DeepEqual(brief.Audio, &expectedAudio) || !reflect.DeepEqual(brief.DayShape, &day) || !reflect.DeepEqual(brief.Counts, base.Counts) {
								t.Fatalf("%s withdrew synthesis with readable P: %+v", stage, brief)
							}
							return
						}
						if len(brief.Offers) != 0 || len(brief.Narrative) != 0 || brief.NarrativeStatus != "unavailable" || brief.Audio != nil || brief.DayShape != nil || brief.Counts.OpenLoopsByKind["old_synthesis"] != 0 || brief.Counts.LearningItems == 29 {
							t.Fatalf("%s retained synthesis after %s P: %+v", stage, state, brief)
						}
						wire, err := json.Marshal(brief)
						if err != nil {
							t.Fatal(err)
						}
						if strings.Contains(string(wire), "UNRETAINED_") {
							t.Fatalf("%s revived removed source text: %s", stage, wire)
						}
						if !strings.Contains(string(wire), "Readable source Q") {
							t.Fatalf("%s lost readable Q", stage)
						}
					}
					latest, ok := svc.LatestMobileBrief()
					if !ok {
						t.Fatal("latest brief disappeared")
					}
					check("latest", latest)
					stored, ok := svc.store.load(base.BriefID)
					if !ok {
						t.Fatal("refreshed brief was not persisted")
					}
					check("persisted", stored)
					byID, ok := svc.GetMobileBrief(base.BriefID)
					if !ok {
						t.Fatal("refreshed brief cannot be read by ID")
					}
					check("by ID", byID)
					bridge := newMobileBriefPrivacyBridge(t, app)
					phone := newMobileTestPhone(t)
					mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead})
					if err := bridge.publishBrief(stored, false); err != nil {
						t.Fatal(err)
					}
					queued := bridge.pendingBriefs[phone.deviceID]
					wire := phone.openFromHub(t, bridge.identity.Public, bridge.identity.HubID, queued.Env)
					var pushed MobileBriefV1
					if err := json.Unmarshal(wire, &pushed); err != nil {
						t.Fatal(err)
					}
					check("push", pushed)
					check("refresh helper", refreshMobileBriefToday(app, base, now, time.UTC))
				})
			}
		}
	}
}
