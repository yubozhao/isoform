package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/calendar"
)

// v1110MixedBrief is Bo's evening brief of v1.1.8 #1: a PT commitment and the
// 1:1 with Jenny tomorrow at 9:00 AM, closing on one question about both.
func v1110MixedBrief(t *testing.T) MobileBriefV1 {
	t.Helper()
	la := attentionTestZone(t)
	asOf := time.Date(2026, 11, 1, 18, 0, 0, 0, la)
	in := mobileBriefInput{Slot: mobileBriefSlotEvening, AsOf: asOf, WindowStart: asOf.Add(-10 * time.Hour), Location: la, Timezone: "America/Los_Angeles", Language: "en",
		OpenLoops: []mobileBriefCandidate{{SourceKind: "purpose", SourceID: "pt", Title: "Do my PT calf raises for AFO", Kind: "commitment", Status: "active", OpenedAt: asOf.Add(-72 * time.Hour), DueAt: asOf.Add(-24 * time.Hour)}},
		Meetings:  []mobileBriefCandidate{{SourceKind: mobileBriefCalendarSource, SourceID: "jenny", Title: "1:1 with Jenny", Kind: "meeting", Status: "upcoming", At: asOf.Add(15 * time.Hour), EndAt: asOf.Add(15*time.Hour + 30*time.Minute)}},
	}
	brief := buildMobileBriefV1(in)
	refs := map[string][]string{}
	for _, ref := range brief.Evidence {
		refs[ref.SourceID] = []string{ref.Ref}
	}
	pt, jenny := refs["pt"], refs["jenny"]
	if len(pt) == 0 || len(jenny) == 0 {
		t.Fatalf("evidence = %+v", brief.Evidence)
	}
	brief.NarrativeStatus = "ok"
	brief.Narrative = []MobileBriefPara{
		{Text: "Your PT calf raises for AFO were due yesterday.", Refs: pt},
		{Text: "The 1:1 with Jenny is tomorrow at 9:00 AM.", Refs: jenny},
		{Text: "Want me to plan your PT calf raises, or help you prepare for the 1:1 with Jenny tomorrow at 9:00 AM?", Refs: append(append([]string{}, pt...), jenny...)},
	}
	brief.Offers = mobileBriefConversationOffers(brief, "en")
	brief.Audio = &MobileBriefAudio{Bytes: 1}
	return brief
}

// today-review v1.1.10 C (v1.1.8 #1 and #3): a closing question that mixes an
// excluded item with an allowed offer is asked again with the allowed offer
// only, with one chip per offer and "Not now".
func TestBriefV1110MixedClosingIsRewrittenWithItsChips(t *testing.T) {
	brief := v1110MixedBrief(t)
	closing := mobileBriefClosingParagraph(brief)
	got := mobileBriefRewrittenClosing(mobileBriefBlankExcluded(brief, []string{"PT"}), closing, "English")
	got.Cards = mobileBriefClosingChips(got, "English")
	got = mobileBriefHygiene(got)
	question := mobileBriefClosingParagraph(got)
	if question.Text != "Want me to help you prepare for the 1:1 with Jenny tomorrow at 9:00 AM?" {
		t.Fatalf("closing = %q", question.Text)
	}
	var labels []string
	for _, card := range got.Cards {
		if !mobileBriefAnswerMatches(got, card) {
			t.Fatalf("chip not bound to the question: %+v", card)
		}
		labels = append(labels, card.Label)
	}
	if strings.Join(labels, " · ") != "Prepare for it · Not now" {
		t.Fatalf("chips = %q", labels)
	}
	if answers := mobileBriefClosingAnswers(got, got.Cards); len(answers) != 2 {
		t.Fatalf("chips cannot be chosen: %+v", answers)
	}
	for _, para := range got.Narrative {
		if mobileBriefExcluded([]string{"PT"}, para.Text) {
			t.Fatalf("PT still speaks: %q", para.Text)
		}
	}
	if got.Audio != nil {
		t.Fatal("the old audio still plays a changed brief")
	}
}

// With no allowed offer left there is no question, and a question whose
// chips cannot be built gets none, so Today shows neither.
func TestBriefV1110NoAllowedOfferNoQuestion(t *testing.T) {
	brief := v1110MixedBrief(t)
	closing := mobileBriefClosingParagraph(brief)
	got := mobileBriefHygiene(mobileBriefRewrittenClosing(mobileBriefBlankExcluded(brief, []string{"PT", "Jenny"}), closing, "English"))
	if question := mobileBriefClosingParagraph(got); question.Text != "" {
		t.Fatalf("question without an allowed offer: %q", question.Text)
	}
	unbound := v1110MixedBrief(t)
	unbound.Offers = nil
	if chips := mobileBriefClosingChips(unbound, "English"); len(chips) != 0 {
		t.Fatalf("chips without an offer: %+v", chips)
	}
}

// today-review v1.1.10 D: day words are anchored at the brief's ready time in
// the person's zone, and an item with its own date is worded from that date.
func TestBriefV1110DayWordsFromReadyTimeAndItemDate(t *testing.T) {
	la := attentionTestZone(t)
	ready := time.Date(2026, 11, 1, 18, 0, 0, 0, la)
	read := time.Date(2026, 11, 2, 0, 30, 0, 0, la)
	meeting := time.Date(2026, 11, 2, 9, 0, 0, 0, la)
	due := time.Date(2026, 10, 31, 17, 0, 0, 0, la)
	brief := MobileBriefV1{AsOf: ready.UTC().Format(time.RFC3339), Timezone: "America/Los_Angeles",
		Narrative: []MobileBriefPara{
			{Text: "The 1:1 with Jenny is tomorrow at 9:00 AM.", Refs: []string{"event"}},
			{Text: "The deck was due yesterday.", Refs: []string{"deck"}},
			{Text: "Tomorrow looks light."},
		},
		Evidence: []MobileBriefRef{{Ref: "event", SourceKind: mobileBriefCalendarSource}, {Ref: "deck", SourceKind: "purpose"}},
		Sections: MobileBriefSections{
			Meetings:    []MobileBriefItem{{At: meeting.Format(time.RFC3339), Refs: []string{"event"}}},
			Commitments: []MobileBriefItem{{DueAt: due.Format(time.RFC3339), Refs: []string{"deck"}}},
		}}
	got := mobileBriefRelativeDaysAtRead(brief, read, ready, la, "English")
	for i, want := range []string{"The 1:1 with Jenny is today at 9:00 AM.", "The deck was due last Saturday.", "Today looks light."} {
		if got.Narrative[i].Text != want {
			t.Errorf("paragraph %d = %q, want %q", i, got.Narrative[i].Text, want)
		}
	}
	// Read the same evening, nothing moves.
	same := mobileBriefRelativeDaysAtRead(brief, ready.Add(time.Hour), ready, la, "English")
	if same.Narrative[0].Text != brief.Narrative[0].Text || same.Narrative[1].Text != brief.Narrative[1].Text {
		t.Fatalf("same-evening read changed day words: %+v", same.Narrative)
	}
}

// today-review v1.1.10 A: a brief shows from when it is ready until the next
// slot's time. Then, while that slot's brief is missing, the card is pending
// with a link to the last brief; with none ever made it is the first brief,
// pending. A quiet slot result stands for its slot.
func TestTodayV1110BriefUntilNextSlotThenPending(t *testing.T) {
	clock := &fakeMobileBriefClock{now: time.Date(2026, 10, 9, 7, 30, 0, 0, time.UTC)}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.location = time.UTC
	svc.app.mobileBriefSvc = svc
	first, err := svc.app.GetLatestBrief()
	if err != nil || !first.FirstBrief || first.PendingSlot != "morning" || first.LastBriefID != "" {
		t.Fatalf("no brief ever made: want the pending first brief, got %+v, %v", first, err)
	}
	evening := mobileBriefStoredBrief(t, "2026-10-08", mobileBriefSlotEvening, 18)
	evening.AsOf = "2026-10-08T18:00:00Z"
	if err := svc.store.save(evening, clock.Now()); err != nil {
		t.Fatal(err)
	}
	pending, err := svc.app.GetLatestBrief()
	if err != nil || pending.Brief != nil || pending.FirstBrief || pending.PendingSlot != "morning" || pending.LastBriefID != evening.BriefID || pending.LastSlot != mobileBriefSlotEvening {
		t.Fatalf("an earlier slot's brief stood in for this slot's: %+v, %v", pending, err)
	}
	quiet := mobileBriefStoredBrief(t, "2026-10-09", mobileBriefSlotMorning, 7)
	quiet.AsOf, quiet.NarrativeStatus = "2026-10-09T07:05:00Z", "quiet"
	if err := svc.store.save(quiet, clock.Now()); err != nil {
		t.Fatal(err)
	}
	view, err := svc.app.GetLatestBrief()
	if err != nil || view.Brief == nil || !view.Quiet || view.PendingSlot != "" {
		t.Fatalf("the quiet morning slot is not shown for its slot: %+v, %v", view, err)
	}
}

// today-review v1.1.10 B: a memory change removes only what it affects, the
// rest of the brief stays, and its audio is made again without the excluded
// parts. Until then there is no player.
func TestBriefV1110ExclusionRemakesAudioWithoutTheExcludedParts(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(8, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	now := clock.Now()
	brief := buildMobileBriefV1(mobileBriefInput{
		Slot: mobileBriefSlotMorning, AsOf: now, WindowStart: now.Add(-time.Hour), Location: time.UTC,
		Moved: []mobileBriefCandidate{{SourceKind: "history", SourceID: "h-morning", Title: "Shipped the plan", At: now.Add(-time.Hour)}},
	})
	refs := map[string]string{}
	for _, ref := range brief.Evidence {
		refs[ref.SourceID] = ref.Ref
	}
	brief.NarrativeStatus = "ok"
	brief.Narrative = []MobileBriefPara{{Text: "You did your calf raises for AFO.", Refs: []string{refs["h-morning"]}}, {Text: "You shipped the plan.", Refs: []string{refs["h-morning"]}}}
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Bytes: 1, binding: mobileBriefAudioBinding(brief, "en")}
	if err := svc.store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	svc.app.mobileBriefSvc = svc
	if delivered := svc.app.mobileBriefForDelivery(brief); delivered.Audio == nil || len(delivered.Narrative) != 2 {
		t.Fatalf("before the exclusion: %+v", delivered)
	}
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brief-preference.md"), []byte("# Brief preference\n\nKeep PT out of my briefs.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	checkMemoryFolderNow(svc.app)
	// The memory sync announces the change, as it does when a note is saved;
	// a new stated exclusion is an explicit privacy action (seal v2).
	svc.app.emitBrowserEvent("memory:updated")
	stored, _ := svc.store.load(brief.BriefID)
	delivered := svc.app.mobileBriefForDelivery(stored)
	if len(delivered.Narrative) != 1 || delivered.Narrative[0].Text != "You shipped the plan." {
		t.Fatalf("the exclusion cleared more or less than its paragraph: %+v", delivered.Narrative)
	}
	if delivered.Audio != nil {
		t.Fatal("the old recording still plays the excluded paragraph")
	}
	spoken := ""
	speaker := fakeSpeaker("en", testWAV(1), nil)
	speaker.synthesize = func(_ context.Context, _, text string) ([]byte, error) {
		spoken += text
		return testWAV(1), nil
	}
	svc.speaker = speaker
	svc.publish = func(MobileBriefV1, bool) error { return nil }
	svc.backfillTodayAudio(t.Context())
	if strings.Contains(spoken, "calf") || !strings.Contains(spoken, "shipped the plan") {
		t.Fatalf("remade recording said %q", spoken)
	}
	stored, _ = svc.store.load(brief.BriefID)
	if again := svc.app.mobileBriefForDelivery(stored); again.Audio == nil || again.Audio.Status != mobileBriefAudioStatusOK {
		t.Fatalf("the remade recording has no player: %+v", again.Audio)
	}
}

// today-review v1.1.9 #1: a failed phone audio send is retried after a
// relaunch too, and stops once no paired phone can receive it.
func TestBriefV1110AudioRetryResumesAfterRelaunch(t *testing.T) {
	svc, brief, _ := newMobileBriefAudioUploadFixture(t)
	if got, ok := svc.briefAudioToResend(); !ok || got.BriefID != brief.BriefID {
		t.Fatalf("today's unsent recording is not resent after a relaunch: %+v, %v", got.Audio, ok)
	}
	sent := brief
	sent.Audio = &MobileBriefAudio{Status: "ok", Bytes: brief.Audio.Bytes, SHA256: brief.Audio.SHA256, Attachment: &MobileBriefAudioAttachment{ID: mobileTestOtherID}}
	if err := svc.store.save(sent, svc.now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.briefAudioToResend(); ok {
		t.Fatal("a recording the relay accepted is sent again")
	}
	if err := svc.store.save(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	svc.retryBriefAudio(ctx, brief, func(int) time.Duration { return time.Millisecond }, func(context.Context, []byte) (*MobileBriefAudioAttachment, error) {
		attempts++
		return nil, errMobileAttachmentOffline
	})
	if attempts != 1 || ctx.Err() != nil {
		t.Fatalf("retry without a paired phone ran %d times", attempts)
	}
}

// today-review v1.1.10 G (v1.1.6 R12): calendar titles read as people say
// them, "The 1:1 with Jenny", never "weekly 1:1 with Jenny".
func TestBriefV1110MeetingReadsAsPeopleSayIt(t *testing.T) {
	app, store := newLinkTestApp(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	event := calendar.Event{ID: "meeting", Title: "Jenny / Bo: weekly fridays 1:1", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), Attendees: []calendar.Attendee{{Name: "Jenny"}, {Name: "Bo", IsMe: true}}}
	storeEvent(t, store, event)
	ref := []string{"meeting-ref"}
	brief := MobileBriefV1{Evidence: []MobileBriefRef{{Ref: "meeting-ref", SourceKind: mobileBriefCalendarSource, SourceID: event.ID, Label: event.Title}},
		Sections: MobileBriefSections{Meetings: []MobileBriefItem{{Title: event.Title, Refs: ref}}},
		Narrative: []MobileBriefPara{
			{Text: event.Title + " is tomorrow at 9:00 AM.", Refs: ref},
			{Text: "Prepare for your " + event.Title + ".", Refs: ref},
			{Text: "Want me to help you prepare for " + event.Title + " tomorrow at 9:00 AM?", Refs: ref},
		}}
	got := app.mobileBriefCalendarDisplay(t.Context(), brief)
	for i, want := range []string{"The 1:1 with Jenny is tomorrow at 9:00 AM.", "Prepare for your 1:1 with Jenny.", "Want me to help you prepare for the 1:1 with Jenny tomorrow at 9:00 AM?"} {
		if got.Narrative[i].Text != want {
			t.Errorf("paragraph %d = %q, want %q", i, got.Narrative[i].Text, want)
		}
	}
	if name := mobileBriefMeetingName(event); name != "1:1 with Jenny" {
		t.Fatalf("meeting name = %q", name)
	}
}

// today-review v1.1.11 #1: on day one the first brief's card never takes the
// day's current work off Today; the snapshot stays below it.
func TestTodayV1111FirstBriefKeepsTheSnapshotBelow(t *testing.T) {
	rig := newHandoffTestRig(t)
	now := mobileBriefTestDay(9, 0)
	clock := &fakeMobileBriefClock{now: now}
	svc, _ := newTestMobileBriefService(t, clock)
	svc.app = rig.app
	rig.app.mobileBriefSvc = svc
	addTodayRefreshSources(t, rig, now)
	view, err := latestBriefAfterRefresh(t, rig.app)
	if err != nil || !view.FirstBrief || view.PendingSlot != mobileBriefSlotMorning || view.LastBriefID != "" {
		t.Fatalf("day one after the slot: want the first brief's card, got %+v, %v", view, err)
	}
	if view.Brief == nil || view.Empty {
		t.Fatalf("the current-work snapshot was taken off Today: %+v", view)
	}
}

// today-review v1.1.11 #2: at the first launch after this update, the brief
// Today shows is made again once, audio included, when its paragraphs carry
// no refs and the person has stated an exclusion.
func TestBriefV1111LegacyBriefOnScreenIsRemadeOnce(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(8, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	now := clock.Now()
	brief := buildMobileBriefV1(mobileBriefInput{
		Slot: mobileBriefSlotMorning, AsOf: now, WindowStart: now.Add(-time.Hour), Location: time.UTC,
		Moved: []mobileBriefCandidate{{SourceKind: "history", SourceID: "h-morning", Title: "Shipped the plan", At: now.Add(-time.Hour)}},
	})
	brief.NarrativeStatus = "ok"
	// A reworded line a text match misses, written before refs.
	brief.Narrative = []MobileBriefPara{{Text: "Your physio exercises for the brace were due yesterday."}, {Text: "You shipped the plan."}}
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Bytes: 1}
	// The preference is written before the brief is stored: storing seals the
	// brief, and that read caches the memory snapshot. A direct file write after
	// it would stay invisible until the cache re-checks the folder.
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brief-preference.md"), []byte("# Brief preference\n\nKeep PT out of my briefs.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	svc.app.mobileBriefSvc = svc
	syntheses := 0
	spoken := ""
	speaker := fakeSpeaker("en", testWAV(1), nil)
	speaker.synthesize = func(_ context.Context, _, text string) ([]byte, error) {
		syntheses++
		spoken += text
		return testWAV(1), nil
	}
	svc.speaker = speaker
	svc.publish = func(MobileBriefV1, bool) error { return nil }
	svc.remakeLegacyBriefOnce(t.Context())
	stored, _ := svc.store.load(brief.BriefID)
	for _, para := range stored.Narrative {
		if strings.Contains(para.Text, "physio") {
			t.Fatalf("the legacy line survived the remake: %+v", stored.Narrative)
		}
	}
	if syntheses != 0 || strings.Contains(spoken, "physio") || stored.Audio != nil {
		t.Fatalf("a template opening was spoken (design R74): status=%s syntheses=%d spoken=%q audio=%+v", stored.NarrativeStatus, syntheses, spoken, stored.Audio)
	}
	shown := svc.app.mobileBriefWithCards(svc.app.mobileBriefForDelivery(stored))
	// The remade brief shows its opening sentence and plays nothing: a template is never spoken (design R74).
	if len(shown.Narrative) == 0 || shown.Audio != nil {
		t.Fatalf("the remade brief should show its opening and not play: %+v", shown)
	}
	// Once: a later launch leaves it alone.
	brief.BriefID = stored.BriefID
	if err := svc.store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	svc.remakeLegacyBriefOnce(t.Context())
	if again, _ := svc.store.load(brief.BriefID); again.NarrativeStatus != "ok" {
		t.Fatal("the remake ran twice")
	}
}
