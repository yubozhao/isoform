package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMobileBriefSummarySealTracksContentAndReplacement(t *testing.T) {
	for _, scenario := range []string{"regenerated", "rolled up", "metadata", "deleted", "edited", "runs replaced", "replacement edited", "unrelated duplicate", "protected replacement", "protected original", "legacy", "legacy fingerprint"} {
		t.Run(scenario, func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			rig := newHandoffTestRig(t)
			now := time.Now().Truncate(time.Second)
			row := ActivityRow{ID: "summary-original", Title: "Reviewed the launch plan", Description: "Compared options", LatestChange: "Prepared the review", Status: activityStatusSettled, ActiveMsByApp: `{"Code":1800000}`, StartMs: now.Add(-time.Hour).UnixMilli(), EndMs: now.Add(-30 * time.Minute).UnixMilli(), CreatedAt: now.Add(-time.Hour).UnixMilli(), UpdatedAt: now.Add(-30 * time.Minute).UnixMilli()}
			row = addMobileBriefRecapProvenance(t, rig, row)
			if err := rig.activity.InsertActivity(row); err != nil {
				t.Fatal(err)
			}
			brief := buildMobileBriefV1(mobileBriefInput{AsOf: now, WindowStart: now.Add(-2 * time.Hour), Slot: mobileBriefSlotMorning, Location: time.UTC,
				Moved: []mobileBriefCandidate{{SourceKind: mobileBriefRecapSource, SourceID: row.ID, Title: row.Title, Kind: "recap", Status: "done", At: time.UnixMilli(row.StartMs)}}})
			if len(brief.Evidence) != 1 {
				t.Fatalf("missing summary evidence: %+v", brief.Evidence)
			}
			brief.NarrativeStatus = "unavailable"
			brief.Narrative = []MobileBriefPara{{Text: "Reviewed the launch plan.", Refs: []string{brief.Evidence[0].Ref}}}
			t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
			svc := newMobileBriefService(rig.app)
			t.Cleanup(svc.Stop)
			svc.location = time.UTC
			svc.now = func() time.Time { return now }
			rig.app.mobileBriefSvc = svc
			if err := svc.store.save(brief, now); err != nil {
				t.Fatal(err)
			}
			if got, withheld, wrote := svc.seal(t.Context(), brief); !wrote || withheld.Reason != "" || len(got.Narrative) != 1 {
				t.Fatalf("initial seal: wrote=%v reason=%q narrative=%d", wrote, withheld.Reason, len(got.Narrative))
			}
			saved, _, err := svc.store.loadFinishedRead(brief.BriefID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(saved.Fingerprints[mobileBriefSourceKey("summary", row.ID)], "summary-v1:") {
				t.Fatal("missing summary content fingerprint")
			}
			if scenario == "legacy" {
				saved.Fingerprints = nil
				if err := svc.store.saveFinishedRead(brief.BriefID, saved); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "legacy fingerprint" {
				original, err := rig.activity.GetActivityByID(row.ID)
				if err != nil || original == nil {
					t.Fatal("missing original activity")
				}
				saved.Fingerprints[mobileBriefSourceKey("summary", row.ID)] = "v2:" + mobileBriefModelDigest(original)
				if err := svc.store.saveFinishedRead(brief.BriefID, saved); err != nil {
					t.Fatal(err)
				}
			}
			// Prime the read decision before changing summary content or its
			// underlying privacy evidence, without sending a source event.
			if got, withheld, err := svc.finishedReadWithholding(brief); err != nil || len(got.Narrative) != 1 || withheld.Reason != "" {
				t.Fatalf("initial read: narrative=%d reason=%q err=%v", len(got.Narrative), withheld.Reason, err)
			}
			switch scenario {
			case "metadata", "legacy", "legacy fingerprint":
				row.UpdatedAt = now.Add(time.Minute).UnixMilli()
				row.Status = activityStatusIdle
				if err := rig.activity.UpdateActivity(row); err != nil {
					t.Fatal(err)
				}
			case "edited":
				row.Description = "Changed options"
				if err := rig.activity.UpdateActivity(row); err != nil {
					t.Fatal(err)
				}
			case "runs replaced":
				other := row
				other.ID = "other-source"
				other = addMobileBriefRecapProvenance(t, rig, other)
				row.RunIDs = other.RunIDs
				if err := rig.activity.UpdateActivity(row); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := rig.activity.DeleteActivity(row.ID); err != nil {
					t.Fatal(err)
				}
			case "protected original":
				if err := rig.activity.UpdateTriageStatus("recap-source-summary-original", "protected"); err != nil {
					t.Fatal(err)
				}
			default:
				if err := rig.activity.DeleteActivity(row.ID); err != nil {
					t.Fatal(err)
				}
				row.ID = "summary-replacement"
				row.UpdatedAt = now.Add(time.Minute).UnixMilli()
				switch scenario {
				case "rolled up":
					row.StartMs -= int64(time.Hour / time.Millisecond)
					row.EndMs += int64(time.Hour / time.Millisecond)
					extra := row
					extra.ID = "another-summary"
					extra = addMobileBriefRecapProvenance(t, rig, extra)
					row.RunIDs = composeJSON(append(composeParseStrings(row.RunIDs), composeParseStrings(extra.RunIDs)...))
				case "replacement edited":
					row.Title = "A different activity"
				case "unrelated duplicate":
					row = addMobileBriefRecapProvenance(t, rig, row)
				case "protected replacement":
					if err := rig.activity.UpdateTriageStatus("recap-source-summary-original", "protected"); err != nil {
						t.Fatal(err)
					}
				}
				if err := rig.activity.InsertActivity(row); err != nil {
					t.Fatal(err)
				}
			}
			wantShown := scenario == "regenerated" || scenario == "rolled up" || scenario == "metadata"
			for _, amend := range []bool{false, true} {
				if amend {
					briefPrivacyChanged()
				}
				got, withheld, err := svc.finishedReadWithholding(brief)
				if err != nil || (len(got.Narrative) == 1) != wantShown {
					t.Fatalf("%s amend=%v: narrative=%d reason=%q kind=%q err=%v", scenario, amend, len(got.Narrative), withheld.Reason, withheld.SourceKind, err)
				}
				if !wantShown && withheld.Reason != mobileBriefWithheldSource {
					t.Fatalf("unexpected withholding: %+v", withheld)
				}
			}
		})
	}
}

func TestMobileBriefRankedSummaryResealChecksContent(t *testing.T) {
	for _, scenario := range []string{"regenerated", "metadata", "edited"} {
		t.Run(scenario, func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			rig := newHandoffTestRig(t)
			now := time.Now().Truncate(time.Second)
			row := ActivityRow{ID: "ranked-summary", Title: "Reviewed the launch plan", Description: "Compared options", Status: activityStatusSettled, ActiveMsByApp: `{"Code":1800000}`, StartMs: now.Add(-time.Hour).UnixMilli(), EndMs: now.Add(-30 * time.Minute).UnixMilli(), CreatedAt: now.Add(-time.Hour).UnixMilli(), UpdatedAt: now.Add(-30 * time.Minute).UnixMilli()}
			row = addMobileBriefRecapProvenance(t, rig, row)
			if err := rig.activity.InsertActivity(row); err != nil {
				t.Fatal(err)
			}
			brief := buildMobileBriefV1(mobileBriefInput{AsOf: now, WindowStart: now.Add(-2 * time.Hour), Slot: mobileBriefSlotMorning, Location: time.UTC,
				Moved: []mobileBriefCandidate{{SourceKind: mobileBriefRecapSource, SourceID: row.ID, Title: row.Title, Kind: "recap", Status: "done", At: time.UnixMilli(row.StartMs)}}})
			ref := brief.Evidence[0]
			sources := rig.app.collectTodayChatSources(t.Context(), now)
			digest, ok := rig.app.mobileBriefModelSourceDigest(t.Context(), ref, brief, sources)
			if !ok {
				t.Fatal("summary could not be bound")
			}
			candidate := morningBriefCandidate{ID: mobileBriefItemKey("candidate", ref.SourceKind, ref.SourceID), SourceKind: ref.SourceKind, SourceID: ref.SourceID, SourceDigest: digest, Title: row.Title, Confidence: 1}
			bound := rig.app.morningBriefBindRankedInputs(t.Context(), now, []morningBriefCandidate{candidate})
			if len(bound) != 1 {
				t.Fatal("summary was not admitted")
			}
			brief.rankedEdition, brief.NarrativeStatus = true, "ok"
			brief.Narrative = []MobileBriefPara{{Text: "Reviewed the launch plan.", Refs: []string{ref.Ref}}}
			revision, err := rig.app.readMobileBriefGatherRevision(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			binding := rig.app.captureRankedBriefModelSources(t.Context(), brief, revision, bound)
			revision.close()
			if binding == nil {
				t.Fatal("ranked binding was not captured")
			}
			rig.app.finishMobileBriefModelBinding(context.Background(), &brief, binding)
			t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
			svc := newMobileBriefService(rig.app)
			t.Cleanup(svc.Stop)
			svc.location, svc.now = time.UTC, func() time.Time { return now }
			rig.app.mobileBriefSvc = svc
			if err := svc.store.save(brief, now); err != nil {
				t.Fatal(err)
			}
			if got, withheld, wrote := svc.seal(t.Context(), brief); !wrote || withheld.Reason != "" || len(got.Narrative) != 1 {
				t.Fatalf("initial seal: wrote=%v reason=%q narrative=%d", wrote, withheld.Reason, len(got.Narrative))
			}
			switch scenario {
			case "regenerated":
				if err := rig.activity.DeleteActivity(row.ID); err != nil {
					t.Fatal(err)
				}
				row.ID = "ranked-replacement"
				if err := rig.activity.InsertActivity(row); err != nil {
					t.Fatal(err)
				}
			case "metadata", "edited":
				row.UpdatedAt = now.Add(time.Minute).UnixMilli()
				if scenario == "edited" {
					row.Description = "Changed options"
				}
				if err := rig.activity.UpdateActivity(row); err != nil {
					t.Fatal(err)
				}
			}
			briefPrivacyChanged()
			got, withheld, err := svc.finishedReadWithholding(brief)
			if err != nil || (len(got.Narrative) == 1) != (scenario != "edited") {
				t.Fatalf("%s: narrative=%d reason=%q kind=%q err=%v", scenario, len(got.Narrative), withheld.Reason, withheld.SourceKind, err)
			}
		})
	}
}
