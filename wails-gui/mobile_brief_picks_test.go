package main

import (
	"strings"
	"testing"
)

// mobileBriefTestPrompt is the brief prompt as composeMobileBriefNarrative
// builds it, without a profile or cards.
func mobileBriefTestPrompt(brief MobileBriefV1, languageName string) string {
	return buildMobileBriefNarrativePrompt(brief, mobileBriefPicks(brief), mobileBriefProfile{}, nil, languageName)
}

// validateMobileBriefClosing checks that a template ends with Isoform's own
// closing, with its refs. Written text closes on the model's own question
// since backlog item 65 (mobileBriefWrittenClosing).
func validateMobileBriefClosing(brief MobileBriefV1, paragraphs []MobileBriefPara, language string) error {
	closing, _ := mobileBriefClosing(brief, language)
	if closing.Text == "" {
		return nil
	}
	if len(paragraphs) == 0 || paragraphs[len(paragraphs)-1].Text != closing.Text || strings.Join(uniqueSortedStrings(paragraphs[len(paragraphs)-1].Refs), ",") != strings.Join(closing.Refs, ",") {
		return mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, "brief must end with its source-backed concrete offers")
	}
	return nil
}

// briefPicksFixture has the shape of the 2026-10-20 morning brief the user
// called "really bad", with invented names: a weekly check-in on now with
// its invite unanswered, a 1:1 tonight, deep work, an appointment and dinner,
// a follow-up owed to a person since last Sunday, a commitment seven months
// late, and what was only activity: a Reddit thread read, a question typed
// into an AI tool, our own dev thread, research in an AI tool, a learning
// note, news and a page in the browser.
func briefPicksFixture() MobileBriefV1 {
	meeting := func(ref, title, status, at, end, detail, url string) MobileBriefItem {
		return MobileBriefItem{Key: "meetings." + ref, ID: "meetings-" + ref, Title: title, Status: status, Kind: "meeting",
			At: at, EndAt: end, Detail: detail, URL: url, Refs: []string{ref}}
	}
	checkIn := meeting("e23", "Northwind Weekly Check In", "now", "2026-10-20T14:00:00Z", "2026-10-20T15:00:00Z",
		"Microsoft Teams; Andy Reed, Aaron Cole, agus@example.com, Diego Park, +5; invite not answered", "https://teams.microsoft.com/l/meetup-join/1")
	deep := meeting("e9", "Deep work", "upcoming", "2026-10-20T19:30:00Z", "2026-10-20T21:30:00Z", "12:30pm to 2:30pm", "")
	doctor := meeting("e10", "Dr Lee", "upcoming", "2026-10-20T22:00:00Z", "2026-10-20T23:15:00Z", "3pm to 4:15pm", "")
	dinner := meeting("e11", "dinner", "upcoming", "2026-10-21T01:00:00Z", "2026-10-21T02:00:00Z", "6pm to 7pm; sam@example.com", "")
	oneOnOne := meeting("e22", "1:1 with Wei", "upcoming", "2026-10-21T02:30:00Z", "2026-10-21T03:00:00Z", "7:30pm to 8pm; Halcyon Meet; wei@example.com", "https://meet.halcyon.example/room-1")
	shape := MobileBriefItem{Key: "meetings.shape", Title: "The shape of today", Status: "day_shape", Kind: "day_shape", At: "2026-10-20T07:00:00Z", Refs: []string{"e24"}}
	discovery := MobileBriefItem{Key: "openLoops.discovery", Title: "Discovery", Status: "active", Kind: "commitment", DueAt: "2026-03-21T06:59:59Z",
		Since: "2026-10-18T06:00:24Z", Refs: []string{"e6"}, AttentionReason: "Was due March 20", AttentionRank: 1}
	shoe := MobileBriefItem{Key: "openLoops.shoe", Title: "follow-up Sam shoe: insert on one shoe", Status: "active", Kind: "commitment",
		Since: "2026-10-18T06:00:24Z", Refs: []string{"e7"}}
	retune := MobileBriefItem{Key: "openLoops.retune", Title: "The briefing needs retuning to sound more proactive.", Status: "needs_review",
		Kind: mobileBriefThreadOutcomeSource, At: "2026-10-18T18:02:18Z", Since: "2026-10-18T18:00:19Z", Refs: []string{"e8"},
		AttentionReason: "Still a draft since last Sunday", AttentionRank: 6,
		Outcome: &ActivityThreadOutcome{Version: 2, Kind: "document", State: "draft", Summary: "The briefing needs retuning.", Confidence: 0.9}}
	thread := MobileBriefItem{Key: "moved.thread", Title: "Reading an r/ycombinator SOC 2 thread; the last reply says a Type I takes two to four weeks.",
		Status: "needs_review", Kind: mobileBriefThreadOutcomeSource, At: "2026-10-19T18:06:47Z", Since: "2026-10-19T17:53:06Z", Refs: []string{"e12"},
		NextStep: "Review the thread's SOC 2 timeline answer and write your own conclusion sentence."}
	typed := MobileBriefItem{Key: "moved.typed", Title: "The user began a hair assessment question: how is my hair.", Status: "needs_review",
		Kind: mobileBriefThreadOutcomeSource, At: "2026-10-18T18:25:23Z", Since: "2026-10-18T18:19:24Z", Refs: []string{"e13"}}
	research := MobileBriefItem{Key: "moved.research", Title: "Research silent speech wearables in ChatGPT", Status: "done", Kind: "recap", At: "2026-10-19T21:53:22Z", Refs: []string{"e14"}}
	page := MobileBriefItem{Key: "doing.page", Title: "Hacker News: agents everywhere", Status: "opened", Kind: "web", At: "2026-10-19T20:00:00Z", Refs: []string{"e30"}}
	news := MobileBriefItem{Key: "watching.news", Title: "Three coding agents ship updates", Status: "news", Kind: "news", At: "2026-10-19T15:00:38Z", Refs: []string{"e19"}}
	learned := MobileBriefItem{Key: "learned.eeg", Title: "Surveying EEG hardware for a wearable prototype", Status: "updated", Kind: "memory", At: "2026-10-20T13:56:00Z", Refs: []string{"e25"}}
	working := []MobileBriefItem{discovery, shoe}
	working[0].Key, working[1].Key = "workingToward.discovery", "workingToward.shoe"
	working[0].Detail, working[1].Detail = "No linked activity recorded yet.", "No linked activity recorded yet."
	commitment := discovery
	commitment.Key, commitment.Status = "commitments.discovery", "slipping"
	return MobileBriefV1{
		Version: 1, BriefID: "2026-10-20-morning", Slot: "morning", LocalDate: "2026-10-20", Timezone: "America/Los_Angeles",
		WindowStart: "2026-10-19T14:00:00Z", AsOf: "2026-10-20T14:11:20Z",
		Sections: MobileBriefSections{
			Review:        []MobileBriefItem{retune},
			Plan:          []MobileBriefItem{deep, doctor, dinner},
			DayAhead:      []MobileBriefItem{deep, doctor, dinner},
			Commitments:   []MobileBriefItem{commitment},
			WorkingToward: working,
			Meetings:      []MobileBriefItem{deep, doctor, dinner, oneOnOne, checkIn, shape},
			OpenLoops:     []MobileBriefItem{discovery, shoe, retune},
			Moved:         []MobileBriefItem{thread, typed, research},
			Doing:         []MobileBriefItem{page},
			Watching:      []MobileBriefItem{news},
			Learned:       []MobileBriefItem{learned},
		},
		Evidence: []MobileBriefRef{
			{Ref: "e6", SourceKind: "purpose", SourceID: "purpose-discovery", Label: "Discovery"},
			{Ref: "e7", SourceKind: "purpose", SourceID: "purpose-shoe", Label: "follow-up Sam shoe: insert on one shoe"},
			{Ref: "e8", SourceKind: "summary", SourceID: "summary-retune", Label: "The briefing needs retuning."},
			{Ref: "e9", SourceKind: "calendar", SourceID: "event-deep", Label: "Deep work"},
			{Ref: "e10", SourceKind: "calendar", SourceID: "event-doctor", Label: "Dr Lee"},
			{Ref: "e11", SourceKind: "calendar", SourceID: "event-dinner", Label: "dinner"},
			{Ref: "e12", SourceKind: "summary", SourceID: "summary-thread", Label: "SOC 2 thread"},
			{Ref: "e13", SourceKind: "summary", SourceID: "summary-typed", Label: "hair assessment question"},
			{Ref: "e14", SourceKind: "summary", SourceID: "summary-research", Label: "Research silent speech wearables"},
			{Ref: "e19", SourceKind: "news", SourceID: "news-agents", Label: "Three coding agents ship updates"},
			{Ref: "e22", SourceKind: "calendar", SourceID: "event-wei", Label: "1:1 with Wei"},
			{Ref: "e23", SourceKind: "calendar", SourceID: "event-checkin", Label: "Northwind Weekly Check In"},
			{Ref: "e24", SourceKind: "calendar_day", SourceID: "day", Label: "The shape of today"},
			{Ref: "e25", SourceKind: "memory", SourceID: "memory-eeg", Label: "Surveying EEG hardware"},
			{Ref: "e30", SourceKind: mobileBriefWebSource, SourceID: "web-hn", Label: "Hacker News: agents everywhere"},
		},
	}
}

// briefPicksActivityWords are the words of what was only activity in the
// fixture; none may reach the picks or the prompt.
var briefPicksActivityWords = []string{"SOC 2", "ycombinator", "hair", "retuning", "silent speech", "ChatGPT", "Hacker News", "coding agents", "EEG", "conclusion sentence"}

func briefPickRefs(picks []mobileBriefPick) []string {
	var out []string
	for _, p := range picks {
		out = append(out, p.Tier+":"+p.Item.Refs[0])
	}
	return out
}

// On 2026-10-20 the brief led with a Reddit thread, our own dev work and a
// question typed into an AI tool, and left out the check-in on now and the
// 1:1 at 7:30pm. The follow-up and a learning lead; calendar-only meetings
// stay in the day instead of becoming picks.
func TestMobileBriefPicksTheDayThatMatters(t *testing.T) {
	brief := briefPicksFixture()
	if got, want := strings.Join(briefPickRefs(mobileBriefPicks(brief)), " "), "follow_up:e7 observed:e25"; got != want {
		t.Fatalf("picks = %s, want %s", got, want)
	}
	var day []string
	for _, item := range mobileBriefDayItems(brief) {
		day = append(day, item.Refs[0])
	}
	if got := strings.Join(day, " "); got != "e23 e9 e10 e11 e22" {
		t.Fatalf("day = %s", got)
	}
	prompt := mobileBriefTestPrompt(brief, "English")
	for _, want := range []string{"Northwind Weekly Check In | on now | Microsoft Teams; Andy Reed, Aaron Cole, Diego Park, +5; invite not answered",
		"e22 | 1:1 with Wei | at 7:30 PM | Halcyon Meet", "open since last Sunday", "a follow-up you owe someone", "draft the follow-up on Sam shoe"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "@") {
		t.Fatal("the prompt carries an address")
	}
}

// Browsing, what the user typed into an AI tool, an AI's answer, our own
// threads and news never become picks, and their words never
// reach the prompt, whatever state or due date they carry.
func TestMobileBriefPicksNeverIncludeActivity(t *testing.T) {
	brief := briefPicksFixture()
	// Even a due date or a person-like title on an observed thread does not
	// make it a pick.
	brief.Sections.Moved[0].DueAt = "2026-10-20T23:00:00Z"
	brief.Sections.Moved[1].Title = "Reply to the hair assessment question"
	activity := map[string]bool{"e8": true, "e12": true, "e13": true, "e14": true, "e19": true, "e30": true}
	for _, p := range mobileBriefPicks(brief) {
		for _, ref := range p.Item.Refs {
			if activity[ref] {
				t.Fatalf("activity became a pick: %s", ref)
			}
		}
	}
	for _, offer := range mobileBriefConversationOffers(brief, "English") {
		if activity[offer.Refs[0]] {
			t.Fatalf("activity became a button: %+v", offer)
		}
	}
	prompt := mobileBriefTestPrompt(brief, "English")
	for _, word := range briefPicksActivityWords {
		if word == "EEG" {
			continue // The filtered learning title is now eligible.
		}
		if strings.Contains(prompt, word) {
			t.Errorf("the prompt carries %q", word)
		}
	}
}

// A deadline seven months gone is not news, nor is a follow-up untouched for
// over 30 days or one whose age is unknown; a deadline this week is.
func TestMobileBriefPicksLeaveStaleItemsOut(t *testing.T) {
	brief := briefPicksFixture()
	stale := brief.Sections.OpenLoops[1]
	stale.Key, stale.Title, stale.Since, stale.Refs = "openLoops.stale", "follow-up with the landlord", "2026-09-10T06:00:00Z", []string{"e40"}
	unknown := brief.Sections.OpenLoops[1]
	unknown.Key, unknown.Title, unknown.Since, unknown.Refs = "openLoops.unknown", "reply to Omar", "", []string{"e41"}
	late := brief.Sections.OpenLoops[0]
	late.Key, late.Title, late.DueAt, late.Refs = "openLoops.late", "Pay the card", "2026-10-17T06:59:59Z", []string{"e42"}
	brief.Sections.OpenLoops = append(brief.Sections.OpenLoops, stale, unknown, late)
	brief.Evidence = append(brief.Evidence,
		MobileBriefRef{Ref: "e40", SourceKind: "purpose", SourceID: "purpose-stale"},
		MobileBriefRef{Ref: "e41", SourceKind: "purpose", SourceID: "purpose-unknown"},
		MobileBriefRef{Ref: "e42", SourceKind: "purpose", SourceID: "purpose-late"})
	got := strings.Join(briefPickRefs(mobileBriefPicks(brief)), " ")
	if want := "due:e42 follow_up:e7 observed:e25"; got != want {
		t.Fatalf("picks = %s, want %s", got, want)
	}
	if prompt := mobileBriefTestPrompt(brief, "English"); strings.Contains(prompt, "Discovery") || strings.Contains(prompt, "landlord") {
		t.Fatal("a stale item reached the prompt")
	}
}

// Six picks at most, in tier order: due now, people waiting,
// follow-ups owed, due soon, Isoform's own work, goals that moved.
func TestMobileBriefPicksKeepSixInTierOrder(t *testing.T) {
	brief := briefPicksFixture()
	purpose := func(ref, title, kind, status, due, since, last string) MobileBriefItem {
		return MobileBriefItem{Key: "x." + ref, Title: title, Kind: kind, Status: status, DueAt: due, Since: since, LastTouch: last, Refs: []string{ref}}
	}
	brief.Sections.WaitingOnYou = []MobileBriefItem{purpose("e50", "Dana's question about the venue", "waiting", "waiting", "", "2026-10-19T10:00:00Z", "")}
	brief.Sections.OpenLoops = append(brief.Sections.OpenLoops,
		purpose("e51", "Send the board update", "commitment", "active", "2026-10-22T06:59:59Z", "2026-10-15T10:00:00Z", ""),
		purpose("e52", "Pay the Citi card", "commitment", "active", "2026-10-20T06:59:59Z", "2026-10-15T10:00:00Z", ""),
		MobileBriefItem{Key: "x.e53", Title: "Draft the hiring plan", Kind: "one_time_work", Status: "blocked", Refs: []string{"e53"}})
	brief.Sections.WorkingToward = append(brief.Sections.WorkingToward, purpose("e54", "Launch the beta", "goal", "active", "", "2026-10-01T10:00:00Z", "2026-10-19T20:00:00Z"))
	for _, ref := range []string{"e50", "e51", "e52", "e54"} {
		brief.Evidence = append(brief.Evidence, MobileBriefRef{Ref: ref, SourceKind: "purpose", SourceID: "purpose-" + ref})
	}
	brief.Evidence = append(brief.Evidence, MobileBriefRef{Ref: "e53", SourceKind: "history", SourceID: "history-e53"})
	picks := mobileBriefPicks(brief)
	if got, want := strings.Join(briefPickRefs(picks), " "), "due:e52 waiting:e50 follow_up:e7 due_soon:e51 own_work:e53 goal:e54"; got != want {
		t.Fatalf("picks = %s, want %s", got, want)
	}
	// Without the meetings, Isoform's own work and the goal come in.
	brief.Sections.Meetings = nil
	if got, want := strings.Join(briefPickRefs(mobileBriefPicks(brief)), " "), "due:e52 waiting:e50 follow_up:e7 due_soon:e51 own_work:e53 goal:e54"; got != want {
		t.Fatalf("picks = %s, want %s", got, want)
	}
}

// The buttons follow the picks, one of a kind first, so the brief can close
// on the follow-up the user liked hearing about.
func TestMobileBriefOffersFollowThePicks(t *testing.T) {
	offers := mobileBriefConversationOffers(briefPicksFixture(), "English")
	if len(offers) != 2 || offers[0].Refs[0] != "e7" || offers[0].Kind != "follow_up" || offers[1].Refs[0] != "e9" {
		t.Fatalf("offers = %+v", offers)
	}
	if offers[0].Label != "Follow up on Sam shoe" || !strings.Contains(offers[0].Prompt, "do not send it") {
		t.Fatalf("follow-up offer = %+v", offers[0])
	}
}

// The prompt says who the user is, shows two example briefs and gives five
// rules, with the 150 to 300 word budget and the buttons by id.
func TestMobileBriefPromptIsShort(t *testing.T) {
	brief := briefPicksFixture()
	_, brief.Offers = mobileBriefClosing(brief, "English")
	profile := mobileBriefProfile{Name: "Alex", Goals: []string{"Launch the beta"}, Notes: []string{"Builds product with coding agents"}}
	prompt := buildMobileBriefNarrativePrompt(brief, mobileBriefPicks(brief), profile, nil, "English")
	for _, want := range []string{"chief of staff to Alex", "Working toward this week: Launch the beta", "How they work: Builds product with coding agents",
		"Morning: Good morning.", "Evening: Tomorrow starts", "1. Lead with what matters most", "2. Say why", "3. Give each item one next step",
		"4. End with one question", "5. Write 150 to 300 words", "offer-1 | Follow up on Sam shoe",
		`"closing":{"text":"...?","card":"offer-1","answers":[`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(prompt, "6.") || strings.Count(prompt, "Never") > 3 {
		t.Fatalf("the prompt grew rules again:\n%s", prompt)
	}
}

// With no pick the brief is the day ahead in two sentences: no buttons in
// the prompt, no closing asked for, and none kept.
func TestMobileBriefShortFormWithNoPicks(t *testing.T) {
	brief := briefPicksFixture()
	brief.Sections.Meetings = brief.Sections.Meetings[:3] // deep work, the appointment, dinner
	brief.Sections.OpenLoops, brief.Sections.WorkingToward = nil, nil
	brief.Sections.Learned = nil
	picks := mobileBriefPicks(brief)
	if len(picks) != 0 {
		t.Fatalf("picks = %v", briefPickRefs(picks))
	}
	prompt := mobileBriefTestPrompt(brief, "English")
	if !strings.Contains(prompt, "two sentences about today") || strings.Contains(prompt, "Buttons") || strings.Contains(prompt, `"closing"`) {
		t.Fatalf("prompt:\n%s", prompt)
	}
	raw := `{"paragraphs":[{"text":"You have deep work at 12:30pm, then Dr Lee at 3pm. Dinner is at 6pm, and the evening is yours after that.","refs":["e9","e10","e11"]}],"closing":{"text":"Want me to plan your evening?","card":"offer-1"}}`
	if _, _, err := mobileBriefNarrativeFromReply(brief, picks, raw, "English"); mobileBriefNarrativeRule(err) != mobileBriefRuleOrder {
		t.Fatalf("calendar-only opening was accepted: %v", err)
	}
}

// About-you notes reach the prompt only when they are about work or a goal
// and pass the sensitive filter.
func TestMobileBriefProfileNotesStayToWork(t *testing.T) {
	goals := []string{"Launch the beta"}
	for title, want := range map[string]bool{
		"Product preferences for the memory page":   true,
		"Prefers short status reports for the team": true,
		"Wants the beta launch to feel calm":        true,
		"Hair loss treatment options":               false,
		"Planning a family trip":                    false,
		"Weekly budget for payroll":                 false,
		"Translation":                               false,
		"Dating app habits":                         false,
	} {
		if got := mobileBriefProfileNoteAllowed(title, goals); got != want {
			t.Errorf("%q allowed = %v, want %v", title, got, want)
		}
	}
}

// "1:1 with Wei" is said once with "with", not "the 1:1 with with Wei".
func TestMobileBriefSpokenOneOnOneSaysWithOnce(t *testing.T) {
	for title, want := range map[string]string{"1:1 with Wei": "1:1 with Wei", "Priya / Alex: 1:1": "1:1 with Priya and Alex", "1:1 With Ana": "1:1 with Ana"} {
		if got := mobileBriefSpokenTitle(title); got != want {
			t.Errorf("%q -> %q, want %q", title, got, want)
		}
	}
}

// Only the cards about the picks go to the model for new words; a card
// about anything else keeps Isoform's sentence, so its words never reach
// the prompt.
func TestMobileBriefPromptCardsAreThePicks(t *testing.T) {
	brief := briefPicksFixture()
	_, brief.Offers = mobileBriefClosing(brief, "English")
	cards := mobileBriefPatternCards(brief, "English")
	prompt := buildMobileBriefNarrativePrompt(brief, mobileBriefPicks(brief), mobileBriefProfile{}, cards, "English")
	if !strings.Contains(prompt, "offer-1 | button | label: Follow up on Sam shoe") {
		t.Fatalf("the pick's button is missing:\n%s", prompt)
	}
	for _, word := range append(briefPicksActivityWords, "Discovery") {
		if word == "EEG" {
			continue
		}
		if strings.Contains(prompt, word) {
			t.Errorf("the prompt carries %q", word)
		}
	}
}

// One pick is said with the day, with its age, and the closing offers its
// button: on 10-19 the shoe follow-up must not drop out (hub, 2026-10-20).
func TestMobileBriefOnePickIsSaidWithTheDay(t *testing.T) {
	brief := briefPicksFixture()
	brief.Sections.Meetings = brief.Sections.Meetings[:3] // deep work, the appointment, dinner
	brief.Sections.Learned = nil
	_, brief.Offers = mobileBriefClosing(brief, "English")
	picks := mobileBriefPicks(brief)
	if got := strings.Join(briefPickRefs(picks), " "); got != "follow_up:e7" {
		t.Fatalf("picks = %s", got)
	}
	buttons := mobileBriefPickOffers(brief, picks, "English")
	if len(buttons) != 1 || buttons[0].Refs[0] != "e7" {
		t.Fatalf("buttons = %+v", buttons)
	}
	prompt := buildMobileBriefNarrativePrompt(brief, picks, mobileBriefProfile{}, nil, "English")
	for _, want := range []string{"e7 | a follow-up you owe someone | follow-up Sam shoe: insert on one shoe | open since last Saturday",
		buttons[0].ID + " | Follow up on Sam shoe", "4. End with one question", "stay shorter rather than pad", `"closing"`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	for _, other := range brief.Offers {
		if other.Refs[0] != "e7" && strings.Contains(prompt, other.ID+" | ") {
			t.Fatalf("a button about an item the brief leaves out is offered: %+v", other)
		}
	}
	day := "You have deep work at 12:30pm, then Dr Lee at 3pm and dinner at 6pm."
	shoe := "The shoe insert follow-up with Sam has been open since last Saturday, and I can draft the note for you."
	raw := `{"paragraphs":[{"text":"` + shoe + `","refs":["e7"]},{"text":"` + day + `","refs":["e9","e10","e11"]}],"closing":{"text":"Want me to draft the note to Sam?","card":"` + buttons[0].ID + `"}}`
	paragraphs, _, err := mobileBriefNarrativeFromReply(brief, picks, raw, "English")
	if err != nil || len(paragraphs) != 3 || !strings.Contains(paragraphs[0].Text, "since last Saturday") {
		t.Fatalf("paragraphs = %+v, rule=%s", paragraphs, mobileBriefNarrativeRule(err))
	}
	noClosing := `{"paragraphs":[{"text":"` + shoe + `","refs":["e7"]}]}`
	if _, _, err := mobileBriefNarrativeFromReply(brief, picks, noClosing, "English"); mobileBriefNarrativeRule(err) != mobileBriefRuleClosing {
		t.Fatalf("a one-pick brief without its question passed: %v", err)
	}
}

// "Talk about this brief" starts from the brief's own top pick, never from an
// item the text left out (Discovery, seven months late, on 10-20); a brief
// with no pick starts from its day; the template keeps its attention order.
func TestMobileBriefOpenerStartsFromTheTopPick(t *testing.T) {
	brief := briefPicksFixture()
	for _, status := range []string{"", "ok"} {
		brief.NarrativeStatus = status
		message, item, ok := mobileBriefOpenerMessage(brief, "en")
		if !ok || item.Refs[0] != "e7" || message != "I'd start with “follow-up Sam shoe: insert on one shoe”: it has been open since last Saturday. Want to begin there?" {
			t.Fatalf("status %q: opener = %q (%v)", status, message, item.Refs)
		}
	}
	opener, found := mobileBriefFindCard(MobileBriefV1{Cards: mobileBriefPatternCards(brief, "English")}, mobileBriefOpenerCardID)
	if !found || strings.Contains(opener.Message, "Discovery") || opener.Refs[0] != "e7" {
		t.Fatalf("opener card = %+v", opener)
	}
	brief.Sections.Meetings = brief.Sections.Meetings[:3]
	brief.Sections.OpenLoops, brief.Sections.WorkingToward = nil, nil
	brief.Sections.Learned = nil
	if message, _, _ := mobileBriefOpenerMessage(brief, "en"); message != "I'd start with “Deep work”: it is at 12:30 PM. Want to begin there?" {
		t.Fatalf("no-pick opener = %q", message)
	}
	brief.NarrativeStatus = "unavailable"
	if message, _, _ := mobileBriefOpenerMessage(brief, "en"); !strings.Contains(message, "Discovery") {
		t.Fatalf("template opener = %q", message)
	}
}
