package main

import (
	"context"
	"strings"
	"testing"
)

// Nov 14: the phone held the sealed read of the morning brief, and a Today
// refresh had renumbered the stored copy's evidence, so the live read's
// conversation key no longer matched the phone's. Every card Ask (and the
// answered-card check) came back 409 "the brief changed" although the phone
// showed the brief as it was sealed. The sealed read's key is accepted too.
func TestMobileBriefCardAskAcceptsTheSealedReadThePhoneWasGiven(t *testing.T) {
	bridge, _, delivered, _ := mobileBriefChatFixture(t)
	app := bridge.app
	svc := newMobileBriefService(app)
	t.Cleanup(svc.Stop)
	app.mobileBriefSvc = svc
	stored, ok := svc.store.load(delivered.BriefID)
	if !ok {
		t.Fatal("the fixture brief is not stored")
	}
	if _, _, wrote := svc.seal(context.Background(), stored); !wrote {
		t.Fatal("the fixture brief could not be sealed")
	}
	sealed, found, err := svc.finishedBrief(stored.BriefID)
	if err != nil || !found {
		t.Fatalf("sealed read: %v %v", found, err)
	}
	var card MobileBriefCard
	for _, candidate := range sealed.Cards {
		if candidate.Mode == mobileBriefCardSend {
			card = candidate
			break
		}
	}
	if card.ID == "" {
		t.Fatal("the sealed read has no send card")
	}
	// A Today refresh renumbers the stored copy's evidence; the sealed copy the
	// phone holds keeps its own.
	renumber := func(ref string) string {
		if ref == "" {
			return ref
		}
		return ref + "9"
	}
	stored.Evidence = append([]MobileBriefRef{}, stored.Evidence...)
	for i := range stored.Evidence {
		stored.Evidence[i].Ref = renumber(stored.Evidence[i].Ref)
	}
	stored.Narrative = append([]MobileBriefPara{}, stored.Narrative...)
	for i := range stored.Narrative {
		refs := make([]string, 0, len(stored.Narrative[i].Refs))
		for _, ref := range stored.Narrative[i].Refs {
			refs = append(refs, renumber(ref))
		}
		stored.Narrative[i].Refs = refs
	}
	if err := svc.store.saveDocument(stored, svc.now()); err != nil {
		t.Fatal(err)
	}
	if live, ok := app.todayChatDeliveredBrief(stored.BriefID); !ok || live.ConversationKey == sealed.ConversationKey {
		t.Fatal("the fixture must make the live key differ from the sealed key")
	}
	input := mobileBriefChatInput{BriefID: sealed.BriefID, BriefVersion: sealed.ConversationKey,
		CardID: card.ID, CardMessage: card.Message, Ask: true}
	if _, _, err := app.mobileBriefChatReference(input, card.Message); err != nil {
		t.Fatalf("a card from the sealed read the phone holds was refused: %v", err)
	}
	input.BriefVersion = strings.Repeat("0", 64)
	if _, _, err := app.mobileBriefChatReference(input, card.Message); err == nil {
		t.Fatal("a card from an unknown revision was accepted")
	}
}
