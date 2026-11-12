package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
)

// generateOnRequestRanked makes an on-request brief at the given local time
// (UTC in tests) through the ranked writer. The model reply is built from the
// real candidate IDs, so the writer's citations resolve.
func generateOnRequestRanked(t *testing.T, at time.Time, paragraphs func(invoiceID string) []morningBriefDraftParagraph) MobileBriefV1 {
	t.Helper()
	svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: at})
	svc.app.mobileBriefSvc = svc
	svc.compose = nil
	svc.composeWithLead = nil
	work, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.app.handoffStore = work
	entry := handoff.Entry{ID: "review-plan", Title: "Review the plan", Source: handoff.SourceMobile, Status: handoff.StatusNeedsReview, CreatedAt: at.Add(-time.Hour).UnixMilli(), UpdatedAt: at.Add(-time.Minute).UnixMilli()}
	if _, err := work.Save(entry); err != nil {
		t.Fatal(err)
	}
	invoice := handoff.Entry{ID: "send-invoice", Title: "Send the invoice", Source: handoff.SourceMobile, Status: handoff.StatusNeedsReview, CreatedAt: at.Add(-2 * time.Hour).UnixMilli(), UpdatedAt: at.Add(-2 * time.Minute).UnixMilli()}
	if _, err := work.Save(invoice); err != nil {
		t.Fatal(err)
	}
	svc.gather = func(_ context.Context, slot string, start, asOf time.Time) (mobileBriefInput, error) {
		input := mobileBriefInput{Slot: slot, WindowStart: start, AsOf: asOf, Location: time.UTC}
		collectMobileBriefWork(svc.app, &input)
		return input, nil
	}
	id := mobileBriefItemKey("candidate", "history", entry.ID)
	invoiceID := mobileBriefItemKey("candidate", "history", invoice.ID)
	scores := morningBriefScores{Items: []morningBriefScore{{ID: id, Critique: "The review awaits your decision.", Score: .8, Pick: "need"}, {ID: invoiceID, Critique: "The invoice awaits your review.", Score: 1, Pick: "need"}}, LeadID: invoiceID,
		Openings: []morningBriefOpening{{Text: "Send the invoice because it awaits your review.", Probability: .9}, {Text: "Review the invoice because it awaits sending.", Probability: .8}, {Text: "Send the invoice because your review is pending.", Probability: .7}}}
	rawScores, err := json.Marshal(scores)
	if err != nil {
		t.Fatal(err)
	}
	svc.modelCall = func(_ context.Context, prompt string) (string, error) {
		switch true {
		case strings.Contains(prompt, "chief of staff's first pass"):
			return string(rawScores), nil
		case strings.Contains(prompt, "You write the user's morning brief"):
			reply, err := json.Marshal(morningBriefDraftReply{Paragraphs: paragraphs(invoiceID), Answers: []mobileBriefCardWords{
				{Label: "Send the invoice", Message: "Yes, send the invoice with me."}, {Label: "Not now", Message: "No, leave it."}}})
			return string(reply), err
		case strings.Contains(prompt, "Judge the morning brief"):
			return `{"critique":"Pass","pass":true}`, nil
		default:
			t.Fatalf("unexpected model call: %s", prompt)
			return "", nil
		}
	}
	svc.publish = func(MobileBriefV1, bool) error { return nil }
	brief, err := svc.generate(context.Background(), mobileBriefSlotOnRequest, mobileBriefReasonSettings)
	if err != nil {
		t.Fatalf("the on-request brief failed (%s): %s", mobileBriefFailureKind(err), safeLogError(err))
	}
	svc.wg.Wait()
	stored, found := svc.store.load(brief.BriefID)
	if !found || !stored.rankedEdition {
		t.Fatal("the on-request brief was not stored as a ranked edition")
	}
	return brief
}

// A brief made on request before noon takes the morning balance: next is the
// larger part, and the brief keeps its on_request id and slot.
func TestOnRequestRankedBriefBeforeNoonIsTwoPartWithNextLarger(t *testing.T) {
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	brief := generateOnRequestRanked(t, at, func(invoiceID string) []morningBriefDraftParagraph {
		return []morningBriefDraftParagraph{
			{Text: "Shipped the draft yesterday.", Items: []string{invoiceID}, Part: briefPartDone},
			{Text: "Send the invoice because your review is pending.", Items: []string{invoiceID}, Part: briefPartNext},
			{Text: "Review the plan because your decision is pending.", Items: []string{invoiceID}, Part: briefPartNext},
			{Text: "Want to send the invoice?", Items: []string{invoiceID}, Part: briefPartNext},
		}
	})
	if brief.Slot != mobileBriefSlotOnRequest || !strings.Contains(brief.BriefID, "-on_request-") {
		t.Fatalf("brief id/slot = %s/%s, want on_request", brief.BriefID, brief.Slot)
	}
	parts := mobileBriefNarrativePartsOf(brief.Narrative)
	if len(parts) != 2 || parts[0].Kind != briefPartDone || parts[1].Kind != briefPartNext || parts[1].Count <= parts[0].Count {
		t.Fatalf("morning on-request parts = %+v, want done then a larger next", parts)
	}
	if brief.modelBinding == nil {
		t.Fatal("the ranked on-request brief has no model binding")
	}
}

// A brief made on request from noon takes the evening balance: done is the
// larger part and the closing question stays last.
func TestOnRequestRankedBriefFromNoonIsTwoPartWithDoneLarger(t *testing.T) {
	at := time.Date(2026, 11, 11, 17, 0, 0, 0, time.UTC)
	brief := generateOnRequestRanked(t, at, func(invoiceID string) []morningBriefDraftParagraph {
		return []morningBriefDraftParagraph{
			{Text: "You shipped the draft and closed two loops, but the review slipped again.", Items: []string{invoiceID}, Part: briefPartDone},
			{Text: "The review has slipped twice this week, which is becoming a pattern.", Items: []string{invoiceID}, Part: briefPartDone},
			{Text: "Want to send the invoice tomorrow?", Items: []string{invoiceID}, Part: briefPartNext},
		}
	})
	if brief.Slot != mobileBriefSlotOnRequest || !strings.Contains(brief.BriefID, "-on_request-") {
		t.Fatalf("brief id/slot = %s/%s, want on_request", brief.BriefID, brief.Slot)
	}
	parts := mobileBriefNarrativePartsOf(brief.Narrative)
	if len(parts) != 2 || parts[0].Kind != briefPartDone || parts[0].Count != 2 || parts[1].Count != 1 {
		t.Fatalf("afternoon on-request parts = %+v, want done 2 then next 1", parts)
	}
}

// The balance comes from the local time of the request, and scheduled slots
// keep their own balance.
func TestRankedBalanceSlotFollowsLocalTimeForOnRequest(t *testing.T) {
	nine := time.Date(2026, 11, 11, 9, 59, 0, 0, time.UTC)
	noon := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		slot string
		now  time.Time
		want string
	}{
		{mobileBriefSlotOnRequest, nine, mobileBriefSlotMorning},
		{mobileBriefSlotOnRequest, noon, mobileBriefSlotEvening},
		{mobileBriefSlotOnRequest, time.Date(2026, 11, 11, 17, 0, 0, 0, time.UTC), mobileBriefSlotEvening},
		{mobileBriefSlotMorning, noon, mobileBriefSlotMorning},
		{mobileBriefSlotEvening, nine, mobileBriefSlotEvening},
	}
	for _, c := range cases {
		if got := rankedBalanceSlot(c.slot, c.now); got != c.want {
			t.Fatalf("rankedBalanceSlot(%s, %s) = %s, want %s", c.slot, c.now.Format(time.RFC3339), got, c.want)
		}
	}
}
