package main

import (
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
			// An edited, regenerated, rolled-up or retention-deleted summary is not a
			// withdrawal: the sealed words keep showing. Only a protected (hidden)
			// source is a privacy action; its amend drops the words through the gates.
			wantShown := scenario != "protected original" && scenario != "protected replacement"
			if !wantShown {
				briefPrivacyChanged()
			}
			got, withheld, err := svc.finishedReadWithholding(brief)
			if err != nil || (len(got.Narrative) == 1) != wantShown {
				t.Fatalf("%s: narrative=%d reason=%q kind=%q err=%v", scenario, len(got.Narrative), withheld.Reason, withheld.SourceKind, err)
			}
			if !wantShown && withheld.Reason == mobileBriefWithheldSource {
				t.Fatalf("a hidden source was withheld as an edit: %+v", withheld)
			}
		})
	}
}
