package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
	"wails-gui/internal/identity"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

func TestEngagementReappearanceReceiptFollowsProductionSightingWrite(t *testing.T) {
	for _, state := range []string{"readable", "protected", "replacement"} {
		t.Run(state, func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			hrig := newHandoffTestRig(t)
			rig := newEngagementTestRig(t)
			a := hrig.app
			a.store, a.engagement = rig.store, rig.svc
			rig.svc.prepareSource, rig.svc.sourceAllowsRead = a.prepareEngagementRead, a.engagementDeliveryAllowsRead
			if err := rig.svc.SetKindEnabled(t.Context(), engagement.KindPersonReappeared, true); err != nil {
				t.Fatal(err)
			}
			rig.presence = engagement.Presence{MacForeground: true}
			sent := 0
			rig.svc.deliver.Desktop = func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
				sent++
				return nil
			}

			previous := engagementPersonSightingHook
			ctx, cancel := context.WithCancel(t.Context())
			a.startEngagementSignals(ctx)
			t.Cleanup(func() { cancel(); engagementPersonSightingHook = previous })
			person, err := rig.store.CreatePrincipal(t.Context(), storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Priya", CreatedFrom: "auto_discord"})
			if err != nil {
				t.Fatal(err)
			}
			prior := rig.clock.Add(-31 * 24 * time.Hour)
			oldSession := seedMobileBriefPurposeRun(t, a, "prior-sighting", 1, prior, 1000, []string{"Discord"})
			currentSession := seedMobileBriefPurposeRun(t, a, "triggering-sighting", 2, rig.clock, 1000, []string{"Discord"})
			labeler := newSenderLabeler(rig.store)
			entry := senderLabelCacheEntry{principalID: person.ID, kind: identity.SubjectContact}
			labeler.recordSighting(t.Context(), entry, "discord", "Earlier hello", oldSession, prior.UnixMilli())
			if rows := engagementPendingOfKind(t, rig, engagement.KindPersonReappeared); len(rows) != 0 {
				t.Fatal("first sighting became a reappearance")
			}
			labeler.recordSighting(t.Context(), entry, "discord", "Hello again", currentSession, rig.clock.UnixMilli())
			rows := engagementPendingOfKind(t, rig, engagement.KindPersonReappeared)
			if len(rows) != 1 {
				t.Fatalf("reappearance rows: %+v", rows)
			}
			row := rows[0]
			lineage, err := rig.store.ListPrincipalSessionSightings(t.Context(), person.ID, 10)
			if err != nil || len(lineage) != 2 {
				t.Fatalf("triggering sighting was not stored: %+v %v", lineage, err)
			}
			if !a.engagementDeliveryAllowsRead(t.Context(), momentFromRow(row)) {
				t.Fatal("the triggering sighting invalidated its own receipt")
			}
			switch state {
			case "protected":
				if _, err := hrig.activity.db.Exec("UPDATE activity_sessions SET triage_status='protected' WHERE id=?", currentSession); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				// Reusing the principal ID and display name cannot lend a new,
				// readable lineage to the original queued observation.
				if err := rig.store.DeletePrincipal(t.Context(), person.ID); err != nil {
					t.Fatal(err)
				}
				replacement, err := rig.store.CreatePrincipal(t.Context(), person)
				if err != nil {
					t.Fatal(err)
				}
				replacementSession := seedMobileBriefPurposeRun(t, a, "replacement-sighting", 3, rig.clock, 1000, []string{"Discord"})
				if err := rig.store.BumpPrincipalSighting(t.Context(), replacement.ID, "discord", replacementSession, rig.clock.UnixMilli(), "A new observation"); err != nil {
					t.Fatal(err)
				}
				if !a.proactiveVoicePersonAllowed(t.Context(), replacement) {
					t.Fatal("replacement source must itself be readable")
				}
			}
			rig.svc.Pass(t.Context())
			stored, err := rig.store.GetEngagementMoment(t.Context(), row.ID)
			if err != nil || stored == nil {
				t.Fatalf("stored moment: %+v %v", stored, err)
			}
			if state == "readable" && stored.State == storage.EngagementStateScheduled {
				rig.clock = time.UnixMilli(stored.HoldUntil).Add(time.Second)
				rig.svc.Pass(t.Context())
				stored, err = rig.store.GetEngagementMoment(t.Context(), row.ID)
				if err != nil || stored == nil {
					t.Fatal(err)
				}
			}
			if state == "readable" {
				// Delivered as a quiet line on Today; it never rings (B3).
				if sent != 0 || stored.State != storage.EngagementStateSent || stored.Params == "{}" {
					t.Fatalf("valid reappearance suppressed: sent=%d row=%+v", sent, stored)
				}
			} else if sent != 0 || stored.State != storage.EngagementStateSuppressed || stored.Params != "{}" {
				t.Fatalf("withdrawn reappearance delivered: sent=%d row=%+v", sent, stored)
			}
		})
	}
}

func newReadReceiptDeliveryFixture(t *testing.T, id string) (*App, *engagementTestRig, handoff.Entry, *int) {
	t.Helper()
	rig := newEngagementTestRig(t)
	work, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item, err := work.Save(handoff.Entry{ID: "receipt-work", Source: "manual", Title: "Release draft", Status: handoff.StatusBlocked})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: rig.store, handoffStore: work, engagement: rig.svc}
	rig.svc.prepareSource = func(ctx context.Context, m engagement.Moment) (engagement.Moment, bool) {
		return a.prepareEngagementReadWithID(ctx, m, id)
	}
	rig.svc.sourceAllowsRead = a.engagementDeliveryAllowsRead
	rig.presence = engagement.Presence{MacForeground: true}
	sent := new(int)
	rig.svc.deliver.Desktop = func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
		*sent++
		return nil
	}
	return a, rig, item, sent
}

func receiptTestMoment(item handoff.Entry) engagement.Moment {
	return engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: item.ID, Params: map[string]string{"title": item.Title, "question": "your attention."}, Channels: []engagement.Channel{engagement.ChannelDesktop}}
}

func TestEngagementReceiptIdentitySurvivesPersistenceAndPass(t *testing.T) {
	for _, id := range []string{"abcdefab-1234-4567-8901-abcdefabcdef", "abcdefab-abcd-4abc-8abc-abcdefabcdef"} {
		t.Run(id, func(t *testing.T) {
			a, rig, item, sent := newReadReceiptDeliveryFixture(t, id)
			if strings.Contains(id, "1234") {
				if redacted, _ := pii.RedactText(id); redacted == id {
					t.Fatal("numeric UUID must exercise the prose-redaction collision")
				}
			}
			m := receiptTestMoment(item)
			m.Params["question"] += " " + activityPrivacySecret
			row, err := rig.svc.Submit(t.Context(), m)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(row.Params, id) || strings.Contains(row.Params, activityPrivacySecret) || !strings.Contains(row.Params, "[REDACTED:api_key]") {
				t.Fatalf("metadata or prose was mishandled: %s", row.Params)
			}
			decoded := momentFromRow(row)
			if _, prose := decoded.Params[engagementReadReceiptParam]; prose || decoded.ReadReceipt != id {
				t.Fatal("receipt metadata remained in template prose")
			}
			if !a.engagementDeliveryAllowsRead(t.Context(), decoded) {
				t.Fatal("stored owned receipt rejected")
			}
			rig.svc.Pass(t.Context())
			stored, err := rig.store.GetEngagementMoment(t.Context(), row.ID)
			if err != nil || stored == nil || stored.State != storage.EngagementStateSent || *sent != 1 || !strings.Contains(stored.Params, id) {
				t.Fatalf("valid receipt suppressed: sent=%d row=%+v err=%v", *sent, stored, err)
			}
		})
	}
}

func TestEngagementReceiptRefusesMalformedForgedAndChangedBindings(t *testing.T) {
	const ownedID = "abcdefab-1234-4567-8901-abcdefabcdef"
	const otherID = "fedcbafe-abcd-4abc-8abc-fedcbafedcba"
	for _, state := range []string{"malformed", "unknown-owner", "other-moment", "changed-prose", "changed-source"} {
		t.Run(state, func(t *testing.T) {
			a, rig, item, sent := newReadReceiptDeliveryFixture(t, ownedID)
			row, err := rig.svc.Submit(t.Context(), receiptTestMoment(item))
			if err != nil {
				t.Fatal(err)
			}
			var params map[string]string
			if err := json.Unmarshal([]byte(row.Params), &params); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "malformed":
				params[engagementReadReceiptParam] = "abcdefab-[REDACTED:credit_card]-abcdefabcdef"
			case "unknown-owner":
				params[engagementReadReceiptParam] = otherID
			case "other-moment":
				other := receiptTestMoment(item)
				other.Params["question"] = "a different question."
				if _, allowed := a.prepareEngagementReadWithID(t.Context(), other, otherID); !allowed {
					t.Fatal("second owned receipt not minted")
				}
				params[engagementReadReceiptParam] = otherID
			case "changed-prose":
				params["question"] = "a changed question."
			case "changed-source":
				item.Body = "A replacement work description"
				if _, err := a.handoffStore.Save(item); err != nil {
					t.Fatal(err)
				}
				if !a.mobileBriefHandoffSourceReadable(t.Context(), item.ID) {
					t.Fatal("replacement source must still be readable")
				}
			}
			raw, _ := json.Marshal(params)
			if err := rig.store.SetEngagementMomentParams(t.Context(), row.ID, string(raw)); err != nil {
				t.Fatal(err)
			}
			rig.svc.Pass(t.Context())
			stored, err := rig.store.GetEngagementMoment(t.Context(), row.ID)
			if err != nil || stored == nil || stored.State != storage.EngagementStateSuppressed || stored.Params != "{}" || *sent != 0 {
				t.Fatalf("forged receipt escaped: sent=%d row=%+v err=%v", *sent, stored, err)
			}
		})
	}
}

func TestPeopleReadTraversesAllLineagePagesAndCancellation(t *testing.T) {
	isolateActivityPrivacyTest(t)
	rig := newHandoffTestRig(t)
	p, err := rig.state.CreatePrincipal(t.Context(), storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Frequent Contact", CreatedFrom: "auto_discord", Notes: "Readable relationship"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= todayChatCandidateLimit; i++ {
		id := fmt.Sprintf("person-session-%03d", i)
		at := time.Now().Add(time.Duration(i-todayChatCandidateLimit-1) * time.Minute)
		if err := rig.activity.SaveSession(ActivitySessionRow{ID: id, StartTime: at.Add(-time.Minute).UnixMilli(), EndTime: at.UnixMilli(), AppNames: `["Discord"]`, TriageStatus: "done", CreatedAt: at.UnixMilli()}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := rig.state.BumpPrincipalSighting(t.Context(), p.ID, "discord", id, at.UnixMilli(), "Hello"); err != nil {
			t.Fatal(err)
		}
	}
	if !rig.app.peopleProfileAllowsModelRead(t.Context(), p, nil) || !rig.app.proactiveVoicePersonAllowed(t.Context(), p) || !strings.Contains(rig.app.injectPeopleContext(p.DisplayName), "Readable relationship") {
		t.Fatal("a readable profile was withheld at the old lifetime cap")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := rig.app.principalSessionSightingsForRead(ctx, p.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("lineage pagination ignored cancellation: %v", err)
	}
	if rig.app.peopleProfileAllowsModelRead(ctx, p, nil) {
		t.Fatal("canceled profile read allowed")
	}
	// The oldest row lies on the second page and must still withdraw the profile.
	if _, err := rig.activity.db.Exec("UPDATE activity_sessions SET triage_status='protected' WHERE id='person-session-000'"); err != nil {
		t.Fatal(err)
	}
	if rig.app.peopleProfileAllowsModelRead(t.Context(), p, nil) || rig.app.proactiveVoicePersonAllowed(t.Context(), p) || rig.app.injectPeopleContext(p.DisplayName) != "" {
		t.Fatal("the last lineage page escaped its privacy gate")
	}
	t.Run("sighting moves protected source between pages", func(t *testing.T) {
		reader := &sightingPageInterleaver{Store: rig.state, betweenPages: func() {
			if err := rig.state.BumpPrincipalSighting(t.Context(), p.ID, "discord", "person-session-000", time.Now().Add(time.Minute).UnixMilli(), "Hello again"); err != nil {
				t.Fatal(err)
			}
		}}
		rows, err := readPrincipalSessionSightings(t.Context(), reader, p.ID)
		if !reader.interleaved || !errors.Is(err, storage.ErrReadRevisionChanged) || rows != nil {
			t.Fatalf("reordered lineage was accepted: interleaved=%v rows=%d err=%v", reader.interleaved, len(rows), err)
		}
		// A fresh traversal now includes the moved protected source, and the
		// production readers must still withhold it.
		if rig.app.peopleProfileAllowsModelRead(t.Context(), p, nil) || rig.app.proactiveVoicePersonAllowed(t.Context(), p) || rig.app.injectPeopleContext(p.DisplayName) != "" {
			t.Fatal("the moved protected source escaped its privacy gate")
		}
	})
	t.Run("cancellation between pages", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		reader := &sightingPageInterleaver{Store: rig.state, betweenPages: cancel}
		rows, err := readPrincipalSessionSightings(ctx, reader, p.ID)
		if !reader.interleaved || !errors.Is(err, context.Canceled) || rows != nil {
			t.Fatalf("canceled lineage was accepted: interleaved=%v rows=%d err=%v", reader.interleaved, len(rows), err)
		}
	})
}

type sightingPageInterleaver struct {
	*storage.Store
	betweenPages func()
	interleaved  bool
}

func (r *sightingPageInterleaver) ListPrincipalSessionSightingsPage(ctx context.Context, id string, limit, offset int) ([]storage.PrincipalSessionSightingRow, error) {
	page, err := r.Store.ListPrincipalSessionSightingsPage(ctx, id, limit, offset)
	if err == nil && offset == 0 {
		r.interleaved = true
		r.betweenPages()
	}
	return page, err
}

func TestPhoneBriefExistingMomentCopyHasBothCatalogLocales(t *testing.T) {
	const key = "Already in your main chat on your computer"
	raw, err := os.ReadFile("../ios/Yansu/Resources/Localizable.xcstrings")
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Strings map[string]struct {
			Localizations map[string]struct{ StringUnit struct{ State, Value string } }
		}
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	for locale, want := range map[string]string{"en": "Already in your chat on your computer", "zh-Hans": "已在电脑上的聊天中"} {
		unit := catalog.Strings[key].Localizations[locale].StringUnit
		if unit.State != "translated" || unit.Value != want {
			t.Fatalf("%s missing computer-only brief copy: %+v", locale, unit)
		}
	}
	view, err := os.ReadFile("../ios/Yansu/BriefView.swift")
	if err != nil || !strings.Contains(string(view), `Text("`+key+`")`) {
		t.Fatal("the brief no longer uses its localized catalog key")
	}
}
