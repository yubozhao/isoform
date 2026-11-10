package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"yansu-agent/agent"
)

// cardsFixture is the Oct 19 attention brief cut to four anchored rows (an
// overdue card payment, Dana waiting, a backpack left in a cart, a blocked memo)
// plus a thread the user only had with an AI tool.
func cardsFixture(t *testing.T, language string) MobileBriefV1 {
	t.Helper()
	in, asOf := attentionFixture(t, language)
	keep := map[string]bool{"card": true, "dana": true, "thread-outcome:backpack:1": true, "memo": true}
	var loops []mobileBriefCandidate
	for _, item := range in.OpenLoops {
		if keep[item.SourceID] {
			loops = append(loops, item)
		}
	}
	in.OpenLoops = append(loops, mobileBriefAIChatThread(asOf))
	in.PreviousBriefs = nil
	brief := buildMobileBriefV1(in)
	_, brief.Offers = mobileBriefClosing(brief, language)
	return brief
}

// mobileBriefAIChatThread is what the user typed into an AI tool, as the
// thread reader records it.
func mobileBriefAIChatThread(asOf time.Time) mobileBriefCandidate {
	return mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: "thread-outcome:codex:1", Title: "The Codex run stopped at its usage limit.",
		Kind: mobileBriefThreadOutcomeSource, Status: "needs_review", OpenedAt: asOf.Add(-20 * time.Hour), At: asOf.Add(-19 * time.Hour),
		Outcome: &ActivityThreadOutcome{Kind: "assistant", State: "incomplete", Summary: "The Codex run stopped at its usage limit.", Question: "Should the run resume now?", Confidence: .9}}
}

func cardsByKind(cards []MobileBriefCard, kind string) []MobileBriefCard {
	var out []MobileBriefCard
	for _, card := range cards {
		if card.Kind == kind {
			out = append(out, card)
		}
	}
	return out
}

func mobileBriefParagraphChipCards(cards []MobileBriefCard) []MobileBriefCard {
	var out []MobileBriefCard
	for _, card := range cards {
		if card.Kind == mobileBriefCardQuestion || card.Kind == mobileBriefCardItem && card.Paragraph != nil && card.Label != "" {
			out = append(out, card)
		}
	}
	return out
}

func TestMobileBriefAnchorRule(t *testing.T) {
	due := time.Date(2026, 10, 20, 17, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		item mobileBriefCandidate
		want string
	}{
		{"meeting", mobileBriefCandidate{SourceKind: mobileBriefCalendarSource, Title: "1:1 with Priya"}, mobileBriefAnchorMeeting},
		{"due date", mobileBriefCandidate{SourceKind: "purpose", Title: "Pay the Citi card", DueAt: due}, mobileBriefAnchorDue},
		{"person waiting", mobileBriefCandidate{SourceKind: mobileBriefWaitingReplySource, Kind: "waiting_on_reply", Title: "Dana"}, mobileBriefAnchorPerson},
		{"order", mobileBriefCandidate{SourceKind: mobileBriefRecapSource, Kind: mobileBriefThreadOutcomeSource, Outcome: &ActivityThreadOutcome{Kind: "purchase", State: "cart_left"}}, mobileBriefAnchorOrder},
		{"document", mobileBriefCandidate{SourceKind: mobileBriefRecapSource, Kind: mobileBriefThreadOutcomeSource, Outcome: &ActivityThreadOutcome{Kind: "document", State: "draft"}}, mobileBriefAnchorDocument},
		{"Isoform's own work", mobileBriefCandidate{SourceKind: "history", Kind: "one_time_work", Title: "Release memo"}, mobileBriefAnchorOwnWork},
		{"a goal", mobileBriefCandidate{SourceKind: "purpose", Kind: "goal", Title: "Halcyon Arc lamp follow-up"}, mobileBriefAnchorGoal},
		{"an AI tool's answer", mobileBriefCandidate{SourceKind: mobileBriefRecapSource, Kind: mobileBriefThreadOutcomeSource, Outcome: &ActivityThreadOutcome{Kind: "assistant", State: "incomplete"}}, ""},
		{"research", mobileBriefCandidate{SourceKind: mobileBriefRecapSource, Kind: mobileBriefThreadOutcomeSource, Outcome: &ActivityThreadOutcome{Kind: "research", State: "incomplete"}}, ""},
		{"an activity summary", mobileBriefCandidate{SourceKind: mobileBriefRecapSource, Kind: "recap", Title: "Worked in Codex"}, ""},
		{"a note", mobileBriefCandidate{SourceKind: "memory", Title: "Prefers mornings"}, ""},
		{"typed words as a title", mobileBriefCandidate{SourceKind: "purpose", Kind: "goal", Title: "You typed, “i used reset on codex”"}, ""},
		{"withdrawn evidence", mobileBriefCandidate{SourceKind: "purpose", Kind: "goal", Title: "Old plan", EvidenceUnavailable: true}, ""},
	} {
		if got := mobileBriefCandidateAnchor(tc.item); got != tc.want {
			t.Errorf("%s: anchor %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Today's failure shape: no model text ("unavailable") and a thread the user
// only had with an AI tool. The thread counts toward "What you worked on" and
// becomes no task, loop, attention row, text or card.
func TestMobileBriefAIChatThreadIsWorkNeverATask(t *testing.T) {
	brief := cardsFixture(t, "en")
	brief.Narrative, brief.NarrativeStatus = mobileBriefNarrativeTemplate(brief, "en"), "unavailable"
	if len(brief.Narrative) == 0 {
		t.Fatal("the template wrote nothing")
	}
	found := false
	for _, item := range brief.Sections.Moved {
		if strings.Contains(item.Title, "Codex") {
			found = true
			if item.AttentionReason != "" {
				t.Fatalf("an AI chat thread needs attention: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("the AI chat thread is not under What you worked on")
	}
	for _, item := range brief.Sections.OpenLoops {
		if strings.Contains(item.Title, "Codex") {
			t.Fatalf("an AI chat thread became a loop: %+v", item)
		}
	}
	for _, para := range brief.Narrative {
		if strings.Contains(para.Text, "Codex") {
			t.Fatalf("the brief text speaks of an AI chat thread: %q", para.Text)
		}
	}
	for _, card := range mobileBriefPatternCards(brief, "en") {
		if strings.Contains(card.Message+card.Label, "Codex") {
			t.Fatalf("an AI chat thread became a card: %+v", card)
		}
	}
}

func TestMobileBriefCardsForEveryClick(t *testing.T) {
	for _, tc := range []struct {
		language string
		labels   []string
		messages map[string]string
		opener   string
	}{
		{"en", []string{"Pay the Citi and Amazon card", "Reply to Dana"}, map[string]string{
			"card":                      "Where do I stand on “Pay the Citi and Amazon card”, which was due yesterday, and what's next?",
			"dana":                      "Dana has been waiting on my reply since last Tuesday. What do they need, and how should I reply?",
			"thread-outcome:backpack:1": "The Halcyon Tide backpack is still in the cart. What's left before I order?",
			"memo":                      "“Release memo” is blocked and waiting for me. What do you need from me to continue?",
		}, "I'd start with “Pay the Citi and Amazon card”: it was due yesterday. Want to begin there?"},
		{"zh", []string{"完成Pay the Citi and Amazon card", "回复Dana"}, map[string]string{
			"card":                      "“Pay the Citi and Amazon card”（昨天已到期）目前进展如何，下一步是什么？",
			"dana":                      "Dana从上周二起一直在等我回复。对方需要什么，我该怎么回？",
			"thread-outcome:backpack:1": "The Halcyon Tide backpack is still in the cart。下单前还差什么？",
			"memo":                      "“Release memo”已暂停，在等我处理。要继续下去，你需要我做什么？",
		}, "我建议先从“Pay the Citi and Amazon card”开始：已于昨天到期。要从这里开始吗？"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			brief := cardsFixture(t, tc.language)
			cards := mobileBriefPatternCards(brief, tc.language)
			var labels []string
			for _, card := range cardsByKind(cards, mobileBriefCardAction) {
				labels = append(labels, card.Label)
				if card.Mode != mobileBriefCardSend || card.Message == "" || len(card.Refs) == 0 {
					t.Fatalf("incomplete button: %+v", card)
				}
			}
			if strings.Join(labels, "|") != strings.Join(tc.labels, "|") {
				t.Fatalf("buttons %q, want %q", labels, tc.labels)
			}
			evidence := mobileBriefEvidenceByRef(brief)
			got := map[string]string{}
			for _, card := range cardsByKind(cards, mobileBriefCardItem) {
				got[evidence[card.Refs[0]].SourceID] = card.Message
				if card.Mode != mobileBriefCardSend {
					t.Fatalf("a row's question waits for the user: %+v", card)
				}
			}
			for source, want := range tc.messages {
				if got[source] != want {
					t.Errorf("%s: %q, want %q", source, got[source], want)
				}
			}
			openers := cardsByKind(cards, mobileBriefCardOpener)
			if len(openers) != 1 || openers[0].Message != tc.opener || openers[0].Mode != mobileBriefCardSpeakFirst {
				t.Fatalf("opener = %+v, want %q", openers, tc.opener)
			}
		})
	}
}

func TestMobileBriefCardWordsFromTheBriefModelAreCheckedFirst(t *testing.T) {
	brief := cardsFixture(t, "en")
	cards := mobileBriefPatternCards(brief, "en")
	action := cardsByKind(cards, mobileBriefCardAction)[0]
	var question MobileBriefCard
	for _, card := range cardsByKind(cards, mobileBriefCardItem) {
		if strings.Contains(card.Message, "Citi") {
			question = card
		}
	}
	for _, tc := range []struct {
		name  string
		words mobileBriefCardWords
		model bool
	}{
		{"a verb and the thing", mobileBriefCardWords{ID: action.ID, Label: "Set up Citi payment", Message: "Set up the Citi and Amazon card payment that was due yesterday: list what it needs, and pay nothing without me."}, true},
		{"a label with no verb", mobileBriefCardWords{ID: action.ID, Label: "The Citi payment", Message: "Set up the Citi and Amazon card payment."}, false},
		{"a label too long", mobileBriefCardWords{ID: action.ID, Label: "Set up the Citi and Amazon card payment today", Message: "Set up the Citi and Amazon card payment."}, false},
		{"another subject", mobileBriefCardWords{ID: action.ID, Label: "Draft reply", Message: "Draft a reply to Priya about Tuesday."}, false},
		{"typed words", mobileBriefCardWords{ID: action.ID, Label: "Pay the card", Message: "You typed that the Citi card is due; pay it."}, false},
		{"a question", mobileBriefCardWords{ID: question.ID, Message: "Where do I stand on the Citi card payment, due yesterday, and what's next?"}, true},
		{"a question that asks nothing", mobileBriefCardWords{ID: question.ID, Message: "Tell me about the Citi card payment."}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := mobileBriefApplyCardWords(brief, cards, []mobileBriefCardWords{tc.words}, "en")
			for _, card := range out {
				if card.ID != tc.words.ID {
					continue
				}
				if got := card.Writer == mobileBriefCardWriterModel; got != tc.model {
					t.Fatalf("model words used = %v, want %v: %+v", got, tc.model, card)
				}
				if tc.model && card.Message != tc.words.Message {
					t.Fatalf("message %q", card.Message)
				}
				if !tc.model && card.Writer != mobileBriefCardWriterPattern {
					t.Fatalf("no fallback: %+v", card)
				}
			}
		})
	}
}

// cardRig is a stored brief Today shows: two of Isoform's handoffs waiting on
// the user, made by the real brief service.
func cardRig(t *testing.T) (*handoffTestRig, *mobileBriefService, MobileBriefV1) {
	return cardRigWithStore(t, nil)
}

func cardRigWithStore(t *testing.T, setup func(*handoffTestRig)) (*handoffTestRig, *mobileBriefService, MobileBriefV1) {
	t.Helper()
	rig := newHandoffTestRig(t)
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.app, rig.app.mobileBriefSvc = rig.app, svc
	if setup != nil {
		setup(rig)
	}
	var candidates []mobileBriefCandidate
	for i, entry := range []handoff.Entry{
		{ID: "memo", Title: "Release memo", Status: handoff.StatusBlocked},
		{ID: "deck", Title: "Board deck draft", Status: handoff.StatusNeedsReview},
	} {
		at := clock.Now().Add(-time.Duration(i+1) * time.Hour)
		entry.UpdatedAt = at.UnixMilli()
		if _, err := rig.app.handoffStore.Save(entry); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, mobileBriefCandidate{SourceKind: "history", SourceID: entry.ID, Title: entry.Title, Kind: "one_time_work", Status: string(entry.Status), At: at})
	}
	svc.gather = func(_ context.Context, slot string, windowStart, asOf time.Time) (mobileBriefInput, error) {
		return mobileBriefInput{Slot: slot, WindowStart: windowStart, AsOf: asOf, Location: time.UTC, OpenLoops: candidates}, nil
	}
	brief, err := svc.generate(context.Background(), mobileBriefSlotMorning, mobileBriefReasonSchedule)
	if err != nil {
		t.Fatal(err)
	}
	if len(brief.Cards) == 0 {
		t.Fatal("the brief was saved without its cards")
	}
	clock.Set(mobileBriefTestDay(9, 0))
	return rig, svc, brief
}

func cardRigWithParagraphChip(t *testing.T) (*handoffTestRig, *mobileBriefService, MobileBriefV1) {
	t.Helper()
	rig, svc, brief := cardRig(t)
	if len(brief.Sections.OpenLoops) == 0 || len(brief.Sections.OpenLoops[0].Refs) == 0 {
		t.Fatal("fixture has no source for its paragraph")
	}
	subject := brief.Sections.OpenLoops[0]
	ref := subject.Refs[0]
	brief.Sections.OpenLoops = brief.Sections.OpenLoops[1:]
	withoutSubjectRef := func(items []MobileBriefItem) []MobileBriefItem {
		kept := make([]MobileBriefItem, 0, len(items))
		for _, item := range items {
			if len(item.Refs) == 0 || item.Refs[0] != ref {
				kept = append(kept, item)
			}
		}
		return kept
	}
	brief.Sections.Decide = withoutSubjectRef(brief.Sections.Decide)
	brief.Sections.Review = withoutSubjectRef(brief.Sections.Review)
	brief.Sections.Plan = withoutSubjectRef(brief.Sections.Plan)
	subject.ID, subject.Key, subject.Kind = "waiting-on-you-test", "waiting-on-you.test", "waiting_on_reply"
	subject.Title = "Dana"
	subject.Status, subject.AttentionReason = "waiting", "Waiting for Dana's reply"
	brief.Sections.WaitingOnYou = append(brief.Sections.WaitingOnYou, subject)
	brief.Narrative, brief.NarrativeStatus = []MobileBriefPara{{Text: "Dana is waiting for a reply.", Refs: []string{ref}}}, "ok"
	brief = rig.app.mobileBriefWithCards(brief)
	if err := svc.store.save(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	return rig, svc, brief
}

func cardFor(t *testing.T, brief MobileBriefV1, subject string) MobileBriefCard {
	t.Helper()
	for _, card := range cardsByKind(brief.Cards, mobileBriefCardItem) {
		if strings.Contains(card.Message, subject) {
			return card
		}
	}
	t.Fatalf("no card about %s in %+v", subject, brief.Cards)
	return MobileBriefCard{}
}

// The model's words stay while the card's facts hold; a change since the brief
// rewrites the card from the current facts, and a withdrawn source removes it.
func TestTodayBriefCardsRewriteAfterAChangeAndLeaveWithTheirSource(t *testing.T) {
	rig, svc, brief := cardRig(t)
	view, err := rig.app.GetLatestBrief()
	if err != nil || view.Brief == nil {
		t.Fatalf("view = %+v, %v", view, err)
	}
	memo := cardFor(t, *view.Brief, "Release memo")
	written := memo
	written.Message, written.Writer = "What does the Release memo still need from me?", mobileBriefCardWriterModel
	// A card written when the memo's facts were different.
	older := memo
	older.ID, older.Facts, older.Message, older.Writer = cardFor(t, *view.Brief, "Board deck draft").ID, "facts before the change", "Is the Board deck draft started yet?", mobileBriefCardWriterModel
	stored, ok := svc.store.load(brief.BriefID)
	if !ok {
		t.Fatal("the brief is not stored")
	}
	stored.Cards = append(stored.Cards, written, older)
	// The brief offered the memo in words its facts no longer support.
	for i := range stored.Offers {
		if strings.Contains(stored.Offers[i].Label, "Release memo") {
			stored.Offers[i].Label, stored.Offers[i].Prompt = "Follow up on Release memo", "Follow up on the Release memo with legal."
		}
	}
	if err := svc.store.save(stored, svc.now()); err != nil {
		t.Fatal(err)
	}
	svc.wg.Wait()
	view, _ = rig.app.GetLatestBrief()
	if got := cardFor(t, *view.Brief, "Release memo"); got.Message != written.Message || got.Writer != mobileBriefCardWriterModel {
		t.Fatalf("the model's words were not kept while the facts held: %+v", got)
	}
	if got := cardFor(t, *view.Brief, "Board deck draft"); got.Writer != mobileBriefCardWriterPattern || got.Message == older.Message {
		t.Fatalf("words written for other facts were kept: %+v", got)
	}
	for _, card := range cardsByKind(view.Brief.Cards, mobileBriefCardAction) {
		if strings.Contains(card.Label, "Release memo") && (card.Label != "Unblock Release memo" || strings.Contains(card.Message, "legal")) {
			t.Fatalf("a button kept words its facts no longer support: %+v", card)
		}
	}

	// Withdrawn: the card leaves, and a click on it sends nothing.
	if err := rig.app.handoffStore.Delete("memo"); err != nil {
		t.Fatal(err)
	}
	view, _ = rig.app.GetLatestBrief()
	for _, card := range view.Brief.Cards {
		if strings.Contains(card.Message+card.Label, "Release memo") {
			t.Fatalf("a withdrawn source kept its card: %+v", card)
		}
	}
	if _, err := rig.app.PrepareTodayChatDraft(TodayChatContextRef{SourceKind: todayChatBriefCardKind, SourceID: memo.ID, BriefID: brief.BriefID}); !errors.Is(err, errTodayChatItemUnavailable) {
		t.Fatalf("a withdrawn card was prepared: %v", err)
	}
}

// Press every card the brief shows: each sends exactly its own words with
// its own sources, and "Talk about this brief" posts Isoform's opener first.
func TestTodayBriefCardsClickThrough(t *testing.T) {
	rig, _, brief := cardRig(t)
	app := rig.app
	app.activeTags, app.eventBroadcaster = map[string]bool{}, NewEventBroadcaster()
	runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 4)}
	app.runner, app.tagCancels, app.sessionTempDirs = runner, map[string]context.CancelFunc{}, map[string]string{}
	app.streams, app.runIDByTag, app.pendingTraceIDs = map[string]context.CancelFunc{}, map[string]string{}, map[string]string{}
	main, err := app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	view, err := app.GetLatestBrief()
	if err != nil || view.Brief == nil {
		t.Fatalf("view = %+v, %v", view, err)
	}
	evidence := mobileBriefEvidenceByRef(*view.Brief)
	pressed := 0
	for _, card := range view.Brief.Cards {
		if card.Mode != mobileBriefCardSend {
			continue
		}
		pressed++
		ref := TodayChatContextRef{SourceKind: todayChatBriefCardKind, SourceID: card.ID, BriefID: view.Brief.BriefID}
		draft, err := app.PrepareTodayChatDraft(ref)
		if err != nil || draft.Message != card.Message {
			t.Fatalf("%s: prepared %q, %v; want %q", card.ID, draft.Message, err, card.Message)
		}
		ref.GeneratedTitle, ref.GeneratedFingerprint, ref.GeneratedMessage = draft.Prefill.Title, draft.Fingerprint, card.Message
		// The click put the card's words in the box (backlog item 64); the
		// user may edit them before Send, which reads the card again.
		edited := card.Message + " Keep it short."
		if checked, err := app.PrepareTodayChatTurn(main.LocalID, edited, ref); err != nil || checked != edited {
			t.Fatalf("%s: edited words = %q, %v", card.ID, checked, err)
		}
		if _, err := app.SendPromptWithTodayContext(edited, "", "m", "", main.LocalID, nil, nil, false, false, "en", "", StreamItemData{ID: "user-" + card.ID, Kind: "user"}, ref); err != nil {
			t.Fatalf("%s: send: %v", card.ID, err)
		}
		var input *agent.TaskInput
		select {
		case input = <-runner.inputs:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the answer did not start", card.ID)
		}
		prompt, _ := input.PromptFunc()
		if !strings.HasSuffix(prompt, edited) || strings.Count(prompt, "<today-item-context>") != 1 {
			t.Fatalf("%s: the turn does not carry the user's words with the card's context: %q", card.ID, prompt)
		}
		for _, id := range card.Refs {
			if source := evidence[id].SourceID; !strings.Contains(prompt, `"id":"`+source+`"`) {
				t.Fatalf("%s: source %s missing from the turn: %q", card.ID, source, prompt)
			}
		}
		waitForTagIdle(t, app, main.LocalID)
	}
	if pressed < 2 {
		t.Fatalf("pressed %d cards; the fixture has two handoff rows", pressed)
	}

	opened, err := app.OpenTodayBriefConversation(brief.BriefID)
	if err != nil || opened.Ref.SourceKind != "brief" || opened.Ref.SourceID != view.Brief.ConversationKey {
		t.Fatalf("opened = %+v, %v", opened, err)
	}
	opener, _ := mobileBriefFindCard(*view.Brief, mobileBriefOpenerCardID)
	if _, err := app.OpenTodayBriefConversation(brief.BriefID); err != nil {
		t.Fatal(err)
	}
	page, err := app.store.LoadStreamItemsLatest(context.Background(), main.LocalID, 200)
	if err != nil {
		t.Fatal(err)
	}
	said := 0
	for _, row := range page.Items {
		if row.Kind == "assistant" && row.Content == opener.Message {
			said++
		}
	}
	if opener.Message == "" || said != 1 {
		t.Fatalf("Isoform's opener %q was said %d times", opener.Message, said)
	}
}

// While observation is paused nothing is checked, so no card is shown or sent.
func TestTodayBriefCardsWhileObservationIsPaused(t *testing.T) {
	rig, _, brief := cardRig(t)
	view, _ := rig.app.GetLatestBrief()
	card := cardFor(t, *view.Brief, "Release memo")
	rig.app.closedEyes.enabled.Store(true)
	view, _ = rig.app.GetLatestBrief()
	if view.Brief != nil && len(view.Brief.Cards) != 0 {
		t.Fatalf("cards shown while paused: %+v", view.Brief.Cards)
	}
	if _, err := rig.app.PrepareTodayChatDraft(TodayChatContextRef{SourceKind: todayChatBriefCardKind, SourceID: card.ID, BriefID: brief.BriefID}); err == nil {
		t.Fatal("a card was prepared while paused")
	}
	if _, err := rig.app.OpenTodayBriefConversation(brief.BriefID); err == nil {
		t.Fatal("Isoform spoke first while paused")
	}
}

// Words the brief's model wrote for a card go out only while every input that
// model read is unchanged, as for the brief's own text; after a change the
// card says Isoform's own sentence from the current facts.
func TestTodayBriefCardModelWordsNeedTheBriefsProof(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	brief := boundMemoryConversationFixture(t, app)
	bindConversationBriefFixture(t, app, &brief)
	pattern := cardsByKind(mobileBriefPatternCards(brief, app.currentUILanguageName()), mobileBriefCardItem)
	if len(pattern) == 0 {
		t.Fatal("the launch plan has no card")
	}
	written := pattern[0]
	written.Message, written.Writer = "What should the Launch plan settle before the release?", mobileBriefCardWriterModel
	brief.Cards = []MobileBriefCard{written}
	sources := app.collectTodayChatSources(t.Context(), time.Now())
	if card, ok := app.todayChatCardNow(t.Context(), brief, written.ID, sources); !ok || card.Message != written.Message {
		t.Fatalf("proven words were not used: %+v", card)
	}
	if _, err := app.SaveMemoryEntry("launch", "# Launch plan\nPrepare the release and the budget review."); err != nil {
		t.Fatal(err)
	}
	sources = app.collectTodayChatSources(t.Context(), time.Now())
	card, ok := app.todayChatCardNow(t.Context(), brief, written.ID, sources)
	if !ok || card.Writer != mobileBriefCardWriterPattern || card.Message != pattern[0].Message {
		t.Fatalf("words the model wrote from changed inputs went out: %+v", card)
	}
}

// Isoform's opener is read through its subject's own gates before it is said:
// an opener about a withdrawn source is never posted.
func TestTodayBriefOpenerNeedsItsSubject(t *testing.T) {
	rig, _, brief := cardRig(t)
	app := rig.app
	view, err := app.GetLatestBrief()
	if err != nil || view.Brief == nil {
		t.Fatalf("view = %+v, %v", view, err)
	}
	opener, ok := mobileBriefFindCard(*view.Brief, mobileBriefOpenerCardID)
	subject, found := mobileBriefCardSubject(*view.Brief, opener.Refs)
	if !ok || !found {
		t.Fatalf("opener = %+v", opener)
	}
	title := mobileBriefCardTitle(subject)
	source := mobileBriefEvidenceByRef(*view.Brief)[opener.Refs[0]].SourceID
	if err := app.handoffStore.Delete(source); err != nil {
		t.Fatal(err)
	}
	sources := app.collectTodayChatSources(t.Context(), time.Now())
	if _, err := app.todayChatCardSubject(t.Context(), *view.Brief, opener, sources, time.Now()); err == nil {
		t.Fatal("an opener about a withdrawn source was read")
	}
	main, err := app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = app.OpenTodayBriefConversation(brief.BriefID)
	page, err := app.store.LoadStreamItemsLatest(t.Context(), main.LocalID, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range page.Items {
		if strings.Contains(row.Content, title) {
			t.Fatalf("Isoform spoke about a withdrawn source: %q", row.Content)
		}
	}
}

// Both service read paths preserve a paragraph chip's identity and words.
func TestStoredBriefParagraphChipKeepsIdentityAndWords(t *testing.T) {
	_, svc, brief := cardRigWithParagraphChip(t)
	chips := mobileBriefParagraphChipCards(brief.Cards)
	if len(chips) == 0 || chips[0].Paragraph == nil {
		t.Fatalf("fixture has no paragraph chip: %+v", brief.Cards)
	}
	want := chips[0]
	for _, read := range []struct {
		name string
		get  func() (MobileBriefV1, bool)
	}{
		{"latest", svc.store.latest},
		{"by id", func() (MobileBriefV1, bool) { return svc.store.load(brief.BriefID) }},
	} {
		t.Run(read.name, func(t *testing.T) {
			delivered, ok := read.get()
			if !ok {
				t.Fatal("stored brief is missing")
			}
			got, found := mobileBriefFindCard(delivered, want.ID)
			if !found || got.Kind != want.Kind || got.ItemKey != want.ItemKey || got.Label != want.Label ||
				got.Message != want.Message || got.Writer != want.Writer || got.Facts != want.Facts ||
				strings.Join(got.Refs, "\x00") != strings.Join(want.Refs, "\x00") ||
				got.Paragraph == nil || *got.Paragraph != *want.Paragraph {
				t.Fatalf("paragraph chip changed: got %+v, want %+v", got, want)
			}
		})
	}
}
