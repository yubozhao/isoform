package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/pii"
	"wails-gui/internal/privacy"
	"wails-gui/storage"
)

func yourWeekOpenFixture(section, kind, title, key string) yourWeekOpenCandidate {
	return yourWeekOpenCandidate{Section: section, Item: MobileBriefItem{ID: key, Key: key, Kind: kind, Title: title,
		Refs: []string{"e-" + key}, Since: "2026-10-21T09:00:00Z", AttentionReason: "Needs you."}}
}

func yourWeekInputFixture(lang string) yourWeekInput {
	start := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	return yourWeekInput{
		Lang: lang, Start: start, End: start.AddDate(0, 0, 4), SeenDays: 5, WorkDays: 5,
		Topics: []yourWeekTopic{
			{ID: "t-hiring", Name: "Hiring", Minutes: 4 * 60},
			{ID: "t-halcyon", Name: "Halcyon", Minutes: 9*60 + 10},
			{ID: "t-q4", Name: "Q4 plan", Minutes: 6 * 60},
			{ID: "t-small", Name: "Expenses", Minutes: 20},
			{ID: "t-fourth", Name: "Travel", Minutes: 2 * 60},
		},
		TotalMinutes: 25 * 60,
		Done: []MobileBriefWeekDone{
			{ID: "deliverable:a", Kind: yourWeekDoneDeliverable, Title: "Pricing page copy", At: "2026-10-22T10:00:00Z", Source: "chat", SourceID: "a", Path: "/tmp/a.md"},
			{ID: "outcome:1", Kind: yourWeekDoneOutcome, Title: "Reply to Priya", At: "2026-10-20T10:00:00Z"},
			{ID: "deliverable:b", Kind: yourWeekDoneDeliverable, Title: "Board deck", At: "2026-10-23T10:00:00Z", Source: "handoff", SourceID: "b", Path: "/tmp/b.pdf"},
			{ID: "outcome:2", Kind: yourWeekDoneOutcome, Title: "Reply to Dan", At: "2026-10-21T10:00:00Z"},
		},
		Meetings:   4,
		MeetingsAt: time.Date(2026, 10, 22, 16, 0, 0, 0, time.UTC),
		Open: []yourWeekOpenCandidate{
			yourWeekOpenFixture(mobileBriefSectionOpenLoops, "waiting_on_reply", "Dan", "dan"),
			yourWeekOpenFixture(mobileBriefSectionCommitments, "commitment", "the Q4 plan", "q4"),
			yourWeekOpenFixture(mobileBriefSectionWorkingToward, "goal", "the pricing page", "pricing"),
			yourWeekOpenFixture(mobileBriefSectionDeliverables, "chat", "the weekly update", "update"),
		},
		Memories: []MobileBriefWeekLearned{
			{MemoryID: "m1", Text: "You answer Priya within the hour, so her mail now comes first."},
			{MemoryID: "m2", Text: "You prefer mornings for deep work."},
		},
		Muted: []string{string(engagement.KindTopicFinds)},
	}
}

// yourWeekFixture is a full week for the speech tests.
func yourWeekFixture(lang string) *MobileBriefWeek {
	return selectYourWeek(yourWeekInputFixture(lang))
}

func TestYourWeekSelectsEachPartInOrderWithItsCap(t *testing.T) {
	week := selectYourWeek(yourWeekInputFixture("en"))
	if week == nil || week.Start != "2026-10-19" || week.End != "2026-10-23" {
		t.Fatalf("week: %+v", week)
	}
	if week.Headline != "A Halcyon week: most of your time went there, then to Q4 plan." {
		t.Fatalf("lead: %q", week.Headline)
	}
	// Top three topics by hours, then the rest of the time on this computer.
	want := []MobileBriefWeekTime{{TopicID: "t-halcyon", Name: "Halcyon", Hours: 9}, {TopicID: "t-q4", Name: "Q4 plan", Hours: 6}, {TopicID: "t-hiring", Name: "Hiring", Hours: 4}, {Other: true, Hours: 6}}
	if fmt.Sprint(week.Time) != fmt.Sprint(want) {
		t.Fatalf("time: %+v", week.Time)
	}
	// Replies first (newest first), then the calls, then files; the rest is
	// counted, the calls by call.
	var done []string
	for _, item := range week.Done {
		done = append(done, item.ID)
	}
	if strings.Join(done, ",") != "outcome:2,outcome:1,meetings:2026-10-19" || week.DoneMore != 2 {
		t.Fatalf("done: %v more %d", done, week.DoneMore)
	}
	if week.Done[2].Count != 4 {
		t.Fatalf("calls: %+v", week.Done[2])
	}
	if len(week.Open) != 3 {
		t.Fatalf("open: %+v", week.Open)
	}
	for i, wantOpen := range []MobileBriefWeekOpen{
		{ItemKey: "dan", CardID: "item:dan", Title: "Dan", Since: "2026-10-21T09:00:00Z", Label: "Draft the reply to Dan"},
		{ItemKey: "q4", CardID: "item:q4", Title: "the Q4 plan", Since: "2026-10-21T09:00:00Z", Label: "Find time for the Q4 plan"},
		{ItemKey: "pricing", CardID: "item:pricing", Title: "the pricing page", Since: "2026-10-21T09:00:00Z", Label: "What's left on the pricing page?"},
	} {
		if week.Open[i] != wantOpen {
			t.Fatalf("open %d: %+v", i, week.Open[i])
		}
	}
	// At most two learned lines; a mute is a learned line, never a count.
	if len(week.Learned) != 2 || week.Learned[0].Kind != yourWeekLearnedMute || week.Learned[0].EngagementKind != "topic_finds" ||
		week.Learned[0].Text != "You muted “New on your topics”, so I'll stay quiet about them." ||
		week.Learned[1].Kind != yourWeekLearnedMemory || week.Learned[1].MemoryID != "m1" {
		t.Fatalf("learned: %+v", week.Learned)
	}
	// One next-workday recommendation, on the first open commitment, with its card.
	if week.Ask == nil || week.Ask.Subject != "the Q4 plan" || week.Ask.CardID != "week:ask:q4" ||
		week.Ask.Text != "Start next week on the Q4 plan?" ||
		!strings.Contains(week.Ask.Message, "start Monday on the Q4 plan") ||
		!strings.Contains(week.Ask.Message, "Don't write to my calendar.") {
		t.Fatalf("ask: %+v", week.Ask)
	}
}

func TestYourWeekChineseCopy(t *testing.T) {
	week := selectYourWeek(yourWeekInputFixture("zh"))
	if week.Headline != "这周的重心是 Halcyon，其次是 Q4 plan。" {
		t.Fatalf("lead: %q", week.Headline)
	}
	if week.Open[0].Label != "起草给Dan的回复" || week.Open[1].Label != "为“the Q4 plan”留出时间" || week.Open[2].Label != "“the pricing page”还差什么？" {
		t.Fatalf("labels: %+v", week.Open)
	}
	if week.Ask.Text != "下周从the Q4 plan开始？" ||
		!strings.Contains(week.Ask.Message, "规划周一如何从the Q4 plan开始") ||
		!strings.Contains(week.Ask.Message, "不要写入日历") {
		t.Fatalf("ask: %q", week.Ask.Text)
	}
}

func TestYourWeekModelLeadValidationAndWithdrawalFallback(t *testing.T) {
	week := yourWeekFixture("en")
	valid, digest := validateYourWeekModelLead("A Halcyon week shipped the pricing page.", week, "en")
	if valid == "" || digest != yourWeekNamesDigest(week) {
		t.Fatalf("valid lead: %q %q", valid, digest)
	}
	for _, invalid := range []string{
		"The week moved forward steadily.", "Halcyon had 3 outcomes.", "Halcyon shipped the pricing page. Then Q4 moved.",
	} {
		if lead, facts := validateYourWeekModelLead(invalid, week, "en"); lead != "" || facts != "" {
			t.Fatalf("invalid lead accepted: %q %q", lead, facts)
		}
	}
	brief := MobileBriefV1{WeekLead: valid, WeekLeadFacts: digest}
	if got := yourWeekHeadline(brief, week, false); got != valid {
		t.Fatalf("model lead not selected: %q", got)
	}
	brief.WeekLeadFacts = "withdrawn-name-digest"
	if got := yourWeekHeadline(brief, week, false); got != week.Headline {
		t.Fatalf("withdrawn-name lead did not fall back: %q", got)
	}
	brief.WeekLeadFacts = digest
	if got := yourWeekHeadline(brief, week, true); got != week.Headline {
		t.Fatalf("quiet week used model lead: %q", got)
	}
	raw, err := json.Marshal(brief)
	if err != nil || strings.Contains(string(raw), "WeekLead") || strings.Contains(string(raw), valid) {
		t.Fatalf("stored-only lead reached wire: %s (%v)", raw, err)
	}
}

func TestMobileBriefPhoneProjectionPreservesModelWeekLead(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 23, 18, 5, 0, 0, time.UTC)
	app.engagement = newEngagementService(app.store, nil, engagementDelivery{Report: app.postEngagementReport})
	app.engagement.now = func() time.Time { return now }
	for key, value := range map[string]string{
		onboardingStateKey:  `{"completed":true}`,
		firstWeekInstallKey: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	} {
		if err := app.store.SetUIState(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.topicsSvc().store.update(func(state *topicsStateFile) {
		state.Projects = []topicProject{{ID: "t-halcyon", Name: "Halcyon", ActivityIDs: []string{"week-phone"}}}
	}); err != nil {
		t.Fatal(err)
	}
	seedThreadMoment(t, app, threadFixtureMoment{
		id: "week-phone", title: "Review Halcyon project", app: "Finder",
		start: at(time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), 9, 0),
		end:   at(time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), 15, 0),
	})
	brief := MobileBriefV1{BriefID: "2026-10-23-evening", Slot: mobileBriefSlotEvening,
		LocalDate: "2026-10-23", Timezone: "UTC", AsOf: now.Format(time.RFC3339),
		NarrativeStatus: "unavailable"}
	snapshot, err := json.Marshal(yourWeekSnapshot{Version: 1, StartDate: "2026-10-19", HasActivityProvenance: true,
		SeenDays: 3, TotalMinutes: 6 * 60,
		Activities: []yourWeekSnapshotActivity{{ID: "week-phone", DayMs: []int64{6 * time.Hour.Milliseconds(), 0, 0, 0, 0}}},
		Topics:     []yourWeekSnapshotRef{{ID: "t-halcyon", Minutes: 6 * 60, ActivityIDs: []string{"week-phone"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetUIState(ctx, yourWeekSnapshotKey+"2026-10-23", string(snapshot)); err != nil {
		t.Fatal(err)
	}
	deterministic, recap := app.yourWeekForBinding(brief)
	if deterministic == nil || len(deterministic.Time) == 0 {
		t.Fatalf("deterministic week: %+v", deterministic)
	}
	lead := "A Halcyon week shipped the pricing page."
	brief.WeekLead, brief.WeekLeadFacts = lead, yourWeekNamesDigest(deterministic)
	withLead := *deterministic
	withLead.Headline = lead
	brief.YourWeek = &withLead
	brief.WeeklyRecap = make([]MobileBriefPara, 0, len(recap))
	for _, paragraph := range yourWeekParagraphs(&withLead, "en") {
		brief.WeeklyRecap = append(brief.WeeklyRecap, MobileBriefPara{Text: paragraph.Text, Refs: []string{}, Role: paragraph.Role})
	}
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, binding: mobileBriefAudioBinding(brief, "en")}

	phone := app.mobileBriefForPhone(brief)
	if phone.YourWeek == nil || phone.YourWeek.Headline != lead {
		t.Fatalf("phone week lost model lead: %+v", phone.YourWeek)
	}
	if len(phone.WeeklyRecap) == 0 || phone.WeeklyRecap[0].Text != lead {
		t.Fatalf("phone recap lost model lead: %+v", phone.WeeklyRecap)
	}
	if phone.Audio == nil {
		t.Fatal("phone lost audio bound to the model lead")
	}
	raw, err := json.Marshal(phone)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"weekLead", "weekLeadFacts"} {
		if _, ok := wire[key]; ok {
			t.Fatalf("phone wire included stored-only %q field: %s", key, raw)
		}
	}

	bridge := newMobileBriefPrivacyBridge(t, app)
	mobilePhone := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, mobilePhone, []string{mobileScopeBriefRead})
	if err := bridge.publishBrief(brief, false); err != nil {
		t.Fatal(err)
	}
	queued, ok := bridge.pendingBriefs[mobilePhone.deviceID]
	if !ok {
		t.Fatal("phone brief was not queued")
	}
	bridge.briefDeliveryMu.Lock()
	refreshed, err := bridge.refreshPendingBrief(queued)
	bridge.briefDeliveryMu.Unlock()
	if err != nil {
		t.Fatalf("unchanged model-led phone revision: %v", err)
	}
	if refreshed.Env.Seq != queued.Env.Seq {
		t.Fatalf("unchanged model-led phone revision changed: %d -> %d", queued.Env.Seq, refreshed.Env.Seq)
	}
}

func TestYourWeekMuteLabelsMatchSettings(t *testing.T) {
	for i, lang := range []string{"en", "zh-CN"} {
		raw, err := os.ReadFile(filepath.Join("frontend", "src", "i18n", "locales", lang, "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		var settings struct {
			ProactiveNotifications struct {
				Kinds map[string]struct{ Label string } `json:"kinds"`
			} `json:"proactiveNotifications"`
		}
		if err := json.Unmarshal(raw, &settings); err != nil {
			t.Fatal(err)
		}
		for _, kind := range engagement.AllKinds() {
			if kind == engagement.KindWeeklyRecap {
				continue
			}
			if got, want := yourWeekMuteLabels[string(kind)][i], settings.ProactiveNotifications.Kinds[string(kind)].Label; got == "" || got != want {
				t.Fatalf("%s %s: %q, Settings says %q", lang, kind, got, want)
			}
		}
	}
	if got := yourWeekMuteText("topic_finds", "zh"); got != "你静音了“你关注话题的新内容”，我之后不再提。" {
		t.Fatal(got)
	}
}

func TestYourWeekAskFallsBackToTheTopTopic(t *testing.T) {
	in := yourWeekInputFixture("en")
	in.Open = in.Open[:1]
	week := selectYourWeek(in)
	if week.Ask == nil || week.Ask.TopicID != "t-halcyon" || week.Ask.CardID != "" || week.Ask.Subject != "Halcyon" {
		t.Fatalf("ask: %+v", week.Ask)
	}
	in.Topics = nil
	if week := selectYourWeek(in); week.Ask != nil {
		t.Fatalf("an ask with no subject: %+v", week.Ask)
	}
}

func TestYourWeekChipFollowsTheBriefChipsRule(t *testing.T) {
	for _, tc := range []struct {
		candidate yourWeekOpenCandidate
		want      string
	}{
		{yourWeekOpenFixture(mobileBriefSectionWaitingOnYou, "commitment", "Priya", "p"), "Draft the reply to Priya"},
		{yourWeekOpenFixture(mobileBriefSectionDeliverables, "chat", "the weekly update", "w"), "Open the weekly update"},
		{yourWeekOpenFixture(mobileBriefSectionOpenLoops, "one_time_work", "the audit", "a"), ""},
		{yourWeekOpenFixture(mobileBriefSectionMeetings, "calendar", "Standup", "s"), ""},
		{yourWeekOpenFixture(mobileBriefSectionPeople, "person", "Dan", "d"), ""},
		{yourWeekOpenFixture(mobileBriefSectionMoved, "history", "Atlas", "m"), ""},
		// No plain name within 40 characters: no chip, never a generic one.
		{yourWeekOpenFixture(mobileBriefSectionCommitments, "commitment", "the very long quarterly planning document for the board", "l"), ""},
	} {
		if got := yourWeekChipLabel(tc.candidate, tc.candidate.Item.Title, "en"); got != tc.want {
			t.Fatalf("%s %s: %q, want %q", tc.candidate.Section, tc.candidate.Item.Title, got, tc.want)
		}
	}
	review := yourWeekOpenFixture(mobileBriefSectionOpenLoops, "one_time_work", "the audit", "a")
	review.Item.Status = "needs_review"
	if got := yourWeekChipLabel(review, "the audit", "en"); got != "Open the audit" {
		t.Fatalf("review: %q", got)
	}
	week := selectYourWeek(yourWeekInput{Lang: "en", SeenDays: 5, WorkDays: 5, Open: []yourWeekOpenCandidate{
		yourWeekOpenFixture(mobileBriefSectionMeetings, "calendar", "Standup", "s")}})
	if len(week.Open) != 1 || week.Open[0].Label != "" || week.Open[0].CardID != "" {
		t.Fatalf("a row without a next step keeps no chip: %+v", week.Open)
	}
}

func TestYourWeekQuietWeek(t *testing.T) {
	for _, tc := range []struct {
		lang string
		seen int
		want string
	}{
		{"en", 2, "A quiet week. I saw you on this computer 2 of 5 days."},
		{"zh", 2, "这周比较安静，我只在 5 天里的 2 天看到你用这台电脑。"},
		{"en", 1, "A quiet week. I saw you on this computer 1 of 5 days."},
		// Never "0 of 5 days".
		{"en", 0, "A quiet week."},
		{"zh", 0, "这周比较安静。"},
	} {
		week := selectYourWeek(yourWeekInput{Lang: tc.lang, SeenDays: tc.seen, WorkDays: 5, TotalMinutes: 50})
		if week == nil || week.Headline != tc.want {
			t.Fatalf("%s %d: %+v", tc.lang, tc.seen, week)
		}
		// Empty parts hide: less than an hour of time draws no bar.
		if week.Time != nil || week.Done != nil || week.Open != nil || week.Learned != nil || week.Ask != nil || week.DoneMore != 0 {
			t.Fatalf("empty parts shown: %+v", week)
		}
	}
	if week := selectYourWeek(yourWeekInput{Lang: "en", SeenDays: 5, WorkDays: 5}); week != nil {
		t.Fatalf("a full week with nothing to say: %+v", week)
	}
}

func TestYourWeekNeverShowsCountsInternalNamesOrZeros(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		in := yourWeekInputFixture(lang)
		in.Topics = append(in.Topics, yourWeekTopic{ID: "t-zero", Name: "0 receipts", Minutes: 20 * 60}, yourWeekTopic{ID: "t-map", Name: "Purpose Map", Minutes: 30 * 60})
		in.Done = append(in.Done, MobileBriefWeekDone{ID: "outcome:x", Kind: yourWeekDoneOutcome, Title: "Delivered 0 messages", At: "2026-10-23T11:00:00Z"})
		in.Memories = append([]MobileBriefWeekLearned{{MemoryID: "bad", Text: "Opens recorded for 3 of 4 messages."}}, in.Memories...)
		in.Muted = nil
		week := selectYourWeek(in)
		raw, _ := json.Marshal(week)
		var weeklyText []string
		for _, paragraph := range yourWeekParagraphs(week, lang) {
			weeklyText = append(weeklyText, paragraph.Text)
		}
		text := string(raw) + "\n" + strings.Join(weeklyText, "\n")
		for _, word := range append([]string{"Purpose Map", "receipt", "Delivered", "Opens recorded", "possible reason", "回执", "送达"}, yourWeekNeverShown...) {
			if strings.Contains(strings.ToLower(text), strings.ToLower(word)) {
				t.Fatalf("%s: %q shown: %s", lang, word, text)
			}
		}
		for _, row := range week.Time {
			if row.Hours < 1 {
				t.Fatalf("%s: a zero bar: %+v", lang, week.Time)
			}
		}
		for _, paragraph := range yourWeekParagraphs(week, lang) {
			if yourWeekZero.MatchString(paragraph.Text) {
				t.Fatalf("%s: zero in %q", lang, paragraph.Text)
			}
		}
		if strings.Contains(string(raw), `"doneMore":0`) {
			t.Fatal("a zero count on the wire")
		}
	}
	for _, text := range []string{"I sent 0 messages", "0 h", "已发送 0 条", "Your Purpose Map holds 2 goals", "delivery confirmed for 3"} {
		if yourWeekCopyAllowed(text) {
			t.Fatalf("guard let %q through", text)
		}
	}
	for _, text := range []string{"Ship v2.0", "Room 101", "Prep me for 10:00 with Dan", "Q4 plan", "Oct 20 review"} {
		if !yourWeekCopyAllowed(text) {
			t.Fatalf("guard dropped %q", text)
		}
	}
}

func TestYourWeekFitsOneScreen(t *testing.T) {
	for _, lang := range []string{"en", "zh"} {
		words := 0
		for _, paragraph := range yourWeekParagraphs(yourWeekFixture(lang), lang) {
			if lang == "zh" {
				words += len([]rune(paragraph.Text)) / 2
			} else {
				words += len(strings.Fields(paragraph.Text))
			}
		}
		if words > 160 {
			t.Fatalf("%s: %d words before chips", lang, words)
		}
	}
}

func TestYourWeekWindowIsTheFridayEveningBrief(t *testing.T) {
	friday := MobileBriefV1{Slot: mobileBriefSlotEvening, LocalDate: "2026-10-23", Timezone: "America/Los_Angeles", AsOf: "2026-10-23T18:05:00-07:00"}
	start, end, cutoff, ok := yourWeekWindow(friday, yourWeekDefaultWorkDays())
	if !ok || start.Format("2006-01-02 15:04 Mon") != "2026-10-19 00:00 Mon" || end.Format("2006-01-02") != "2026-10-23" || cutoff.Format("15:04") != "18:05" {
		t.Fatalf("window: %v %v %v %v", start, end, cutoff, ok)
	}
	for _, brief := range []MobileBriefV1{
		{Slot: mobileBriefSlotMorning, LocalDate: "2026-10-23", Timezone: "UTC"},
		{Slot: mobileBriefSlotEvening, LocalDate: "2026-10-22", Timezone: "UTC"},
		{Slot: mobileBriefSlotEvening, LocalDate: "2026-10-25", Timezone: "UTC"},
		{Slot: "on_request", LocalDate: "2026-10-23", Timezone: "UTC"},
	} {
		if _, _, _, ok := yourWeekWindow(brief, yourWeekDefaultWorkDays()); ok {
			t.Fatalf("%s %s has a week", brief.Slot, brief.LocalDate)
		}
	}
}

func TestYourWeekLearnsMonThroughThuAndQualifiesThursday(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	installed := time.Date(2026, 9, 1, 9, 0, 0, 0, time.Local)
	if err := app.store.SetUIState(ctx, firstWeekInstallKey, installed.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	for _, weekStart := range []time.Time{
		time.Date(2026, 9, 21, 0, 0, 0, 0, time.Local),
		time.Date(2026, 10, 5, 0, 0, 0, 0, time.Local),
	} {
		for offset := 0; offset < 4; offset++ {
			day := weekStart.AddDate(0, 0, offset)
			start, end := day.Add(9*time.Hour), day.Add(9*time.Hour+10*time.Minute)
			if err := app.activity.store.SaveSession(ActivitySessionRow{
				ID: fmt.Sprintf("learned-%s", day.Format("20060102")), StartTime: start.UnixMilli(), EndTime: end.UnixMilli(),
				AppNames: `["Finder"]`, TriageStatus: "done",
			}, nil, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	workDays := app.yourWeekLearnWorkDays(ctx, now)
	if !sameYourWeekWorkDays(workDays, []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday}) {
		t.Fatalf("learned workdays: %v", workDays)
	}
	for _, tc := range []struct {
		date string
		want bool
	}{{"2026-10-22", true}, {"2026-10-23", false}} {
		brief := MobileBriefV1{Slot: mobileBriefSlotEvening, LocalDate: tc.date, Timezone: "UTC", AsOf: tc.date + "T18:00:00Z"}
		if _, _, _, got := yourWeekWindow(brief, workDays); got != tc.want {
			t.Fatalf("%s has week=%v, want %v", tc.date, got, tc.want)
		}
	}
	quiet := selectYourWeek(yourWeekInput{Lang: "en", SeenDays: 2, WorkDays: len(workDays), TotalMinutes: 30})
	if quiet == nil || quiet.Headline != "A quiet week. I saw you on this computer 2 of 4 days." {
		t.Fatalf("quiet line for learned workdays: %+v", quiet)
	}
}

func TestYourWeekDefaultsToMondayFridayWithTooLittleHistory(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	installed := time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local)
	if err := app.store.SetUIState(ctx, firstWeekInstallKey, installed.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if got := app.yourWeekLearnWorkDays(ctx, now); !sameYourWeekWorkDays(got, yourWeekDefaultWorkDays()) {
		t.Fatalf("short history workdays: %v", got)
	}
}

func yourWeekFrozenWindowFixture(t *testing.T, currentDays []time.Weekday, snapshotStart string, snapshotWorkDays *int, seenDays int, topics []yourWeekSnapshotRef) (*App, MobileBriefV1, time.Time) {
	t.Helper()
	app := newMobileBriefPurposeTestApp(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 23, 18, 5, 0, 0, time.UTC)
	t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
	app.engagement = newEngagementService(app.store, nil, engagementDelivery{Report: app.postEngagementReport})
	app.engagement.now = func() time.Time { return now }
	for key, value := range map[string]string{
		onboardingStateKey:  `{"completed":true}`,
		firstWeekInstallKey: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	} {
		if err := app.store.SetUIState(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	workDays, err := json.Marshal(yourWeekWorkDaysState{WorkDays: currentDays, ComputedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetUIState(ctx, yourWeekWorkDaysStateKey, string(workDays)); err != nil {
		t.Fatal(err)
	}
	if got := app.yourWeekStoredWorkDays(ctx); !sameYourWeekWorkDays(got, currentDays) {
		t.Fatalf("fixture workdays %v, want %v", got, currentDays)
	}
	if _, err := app.topicsSvc().store.update(func(state *topicsStateFile) {
		activityIDs := map[string][]string{
			"t-halcyon":  {"week-halcyon"},
			"t-borealis": {"week-borealis"},
		}
		state.Projects = []topicProject{
			{ID: "t-halcyon", Name: "Halcyon", ActivityIDs: activityIDs["t-halcyon"]},
			{ID: "t-borealis", Name: "Borealis", ActivityIDs: activityIDs["t-borealis"]},
		}
	}); err != nil {
		t.Fatal(err)
	}
	monday := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	seedThreadMoment(t, app, threadFixtureMoment{
		id: "week-halcyon", title: "Review Halcyon project", app: "Finder",
		start: at(monday, 9, 0), end: at(monday, 15, 0),
	})
	seedThreadMoment(t, app, threadFixtureMoment{
		id: "week-borealis", title: "Review Borealis project", app: "Workspace",
		start: at(monday.AddDate(0, 0, 1), 9, 0), end: at(monday.AddDate(0, 0, 1), 13, 0),
	})
	for i := range topics {
		switch topics[i].ID {
		case "t-halcyon":
			topics[i].ActivityIDs = []string{"week-halcyon"}
		case "t-borealis":
			topics[i].ActivityIDs = []string{"week-borealis"}
		}
	}
	activities := []yourWeekSnapshotActivity{
		{ID: "week-halcyon", DayMs: []int64{6 * time.Hour.Milliseconds(), 0, 0, 0, 0}},
		{ID: "week-borealis", DayMs: []int64{0, 4 * time.Hour.Milliseconds(), 0, 0, 0}},
	}
	if snapshotStart == "" {
		activities = nil
	}
	snapshot, err := json.Marshal(yourWeekSnapshot{
		Version: 1, StartDate: snapshotStart, CutoffMs: now.UnixMilli(),
		SeenDays: seenDays, TotalMinutes: 10 * 60, HasActivityProvenance: len(activities) > 0, Topics: topics,
		Activities: activities,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshotWorkDays != nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(snapshot, &fields); err != nil {
			t.Fatal(err)
		}
		fields["workDays"], err = json.Marshal(*snapshotWorkDays)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := app.store.SetUIState(ctx, yourWeekSnapshotKey+"2026-10-23", string(snapshot)); err != nil {
		t.Fatal(err)
	}
	brief := MobileBriefV1{
		BriefID: "2026-10-23-evening", Slot: mobileBriefSlotEvening,
		LocalDate: "2026-10-23", Timezone: "UTC", AsOf: now.Format(time.RFC3339),
		NarrativeStatus: "unavailable",
	}
	return app, brief, now
}

func TestYourWeekFridaySnapshotSurvivesMonThuWorkdayChange(t *testing.T) {
	app, brief, _ := yourWeekFrozenWindowFixture(t,
		[]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday},
		"2026-10-19", intPointer(5), 3,
		[]yourWeekSnapshotRef{{ID: "t-halcyon", Minutes: 6 * 60}, {ID: "t-borealis", Minutes: 4 * 60}})
	week, _ := app.yourWeekForDelivery(brief)
	if week == nil || week.Start != "2026-10-19" || week.End != "2026-10-23" ||
		len(week.Time) != 2 || week.Time[0].Name != "Halcyon" || week.Time[1].Name != "Borealis" {
		t.Fatalf("frozen Friday week changed: %+v", week)
	}
}

func TestYourWeekFridaySnapshotKeepsStoredMondayStart(t *testing.T) {
	app, brief, _ := yourWeekFrozenWindowFixture(t,
		[]time.Weekday{time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		"2026-10-19", intPointer(5), 3,
		[]yourWeekSnapshotRef{{ID: "t-halcyon", Minutes: 6 * 60}})
	week, _ := app.yourWeekForDelivery(brief)
	if week == nil || week.Start != "2026-10-19" {
		t.Fatalf("stored Monday start was not kept: %+v", week)
	}
}

func TestYourWeekLegacySnapshotFailsClosed(t *testing.T) {
	app, brief, _ := yourWeekFrozenWindowFixture(t,
		[]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday},
		"", intPointer(4), 2, nil)
	start, end, _, workDays, ok := app.yourWeekResolvedWindow(context.Background(), brief)
	if !ok || start.Format("2006-01-02") != "2026-10-19" ||
		end.Format("2006-01-02") != "2026-10-23" || workDays != 5 {
		t.Fatalf("legacy Friday window: %s - %s, workdays=%d, ok=%v", start, end, workDays, ok)
	}
	raw, err := app.store.GetUIState(context.Background(), yourWeekSnapshotKey+"2026-10-23")
	if err != nil {
		t.Fatal(err)
	}
	var snap yourWeekSnapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		t.Fatal(err)
	}
	in := app.yourWeekReadable(context.Background(), snap, start, time.Date(2026, 10, 23, 18, 5, 0, 0, time.UTC))
	if in.SeenDays != -1 || in.TotalMinutes != 0 || len(in.Topics) != 0 {
		t.Fatalf("legacy aggregates were trusted: %+v", in)
	}
	in.WorkDays = 5
	if week := selectYourWeek(in); week != nil && strings.Contains(week.Headline, "days") {
		t.Fatalf("unknown legacy days produced quiet-week copy: %+v", week)
	}
}

func TestYourWeekDropsWithdrawnActivityMinutes(t *testing.T) {
	previous := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
	pii.SetUserSensitiveApps(nil)
	app, brief, now := yourWeekFrozenWindowFixture(t,
		[]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		"2026-10-19", intPointer(5), 2,
		[]yourWeekSnapshotRef{{ID: "t-halcyon", Minutes: 6 * 60}, {ID: "t-borealis", Minutes: 4 * 60}})
	ctx := context.Background()
	raw, err := app.store.GetUIState(ctx, yourWeekSnapshotKey+"2026-10-23")
	if err != nil {
		t.Fatal(err)
	}
	var snap yourWeekSnapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		t.Fatal(err)
	}
	pii.SetUserSensitiveApps(map[string]string{"com.example.finder": "Finder"})
	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.App, ID: "com.example.finder"})
	in := app.yourWeekReadable(ctx, snap, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), now)
	if in.TotalMinutes != 4*60 || in.SeenDays != 1 || len(in.Topics) != 1 ||
		in.Topics[0].ID != "t-borealis" || in.Topics[0].Minutes != 4*60 {
		t.Fatalf("protected source remained in the week: %+v", in)
	}
	week, _ := app.yourWeekForDelivery(brief)
	if week == nil {
		t.Fatal("readable weekly projection was missing")
	}
	var displayedHours int
	for _, row := range week.Time {
		displayedHours += row.Hours
		if row.TopicID == "t-halcyon" || row.Other && row.Hours > 0 {
			t.Fatalf("withdrawn time migrated into a visible row: %+v", week.Time)
		}
	}
	if displayedHours != 4 || !strings.Contains(week.Headline, "1 of 5 days") {
		t.Fatalf("delivery did not reflect readable hours and days: %+v", week)
	}
	assertReduced := func(surface string, got *MobileBriefWeek) {
		t.Helper()
		if got == nil || len(got.Time) != 1 || got.Time[0].TopicID != "t-borealis" || got.Time[0].Hours != 4 {
			t.Fatalf("%s did not use the readable week: %+v", surface, got)
		}
	}
	bindingWeek, _ := app.yourWeekForBinding(brief)
	assertReduced("binding", bindingWeek)
	delivered := app.mobileBriefForDelivery(brief)
	assertReduced("desktop delivery", delivered.YourWeek)
	phone, _ := app.mobileBriefPhoneProjection(brief)
	assertReduced("phone delivery", phone.YourWeek)
	service, ok := app.ensureMobileBriefService().(*mobileBriefService)
	if !ok || service.store == nil {
		t.Fatal("mobile brief store unavailable")
	}
	if err := service.store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	entry, err := app.GetYourWeekForDate("2026-10-20")
	if err != nil {
		t.Fatal(err)
	}
	assertReduced("Activity date lookup", entry.Week)

	if err := app.SetClosedEyesModeEnabled(true); err != nil {
		t.Fatal(err)
	}
	paused := app.yourWeekReadable(ctx, snap, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), now)
	if paused.TotalMinutes != 0 || paused.SeenDays != 0 || len(paused.Topics) != 0 {
		t.Fatalf("paused week retained activity: %+v", paused)
	}
	if err := app.SetClosedEyesModeEnabled(false); err != nil {
		t.Fatal(err)
	}
	resumed := app.yourWeekReadable(ctx, snap, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), now)
	if resumed.TotalMinutes != 4*60 || len(resumed.Topics) != 1 || resumed.Topics[0].ID != "t-borealis" {
		t.Fatalf("resuming restored withheld activity: %+v", resumed)
	}
	if err := app.DeleteActivity("week-borealis"); err != nil {
		t.Fatal(err)
	}
	forgotten := app.yourWeekReadable(ctx, snap, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), now)
	if forgotten.TotalMinutes != 0 || forgotten.SeenDays != 0 || len(forgotten.Topics) != 0 {
		t.Fatalf("forgotten activity remained in the week: %+v", forgotten)
	}
}

func TestYourWeekMixedTopicKeepsOnlyReadableMinutes(t *testing.T) {
	previous := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
	pii.SetUserSensitiveApps(nil)
	app, _, now := yourWeekFrozenWindowFixture(t,
		[]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		"2026-10-19", intPointer(5), 1, nil)
	monday := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	seedThreadMoment(t, app, threadFixtureMoment{
		id: "week-protected", title: "Protected Halcyon work", app: "Finder",
		start: at(monday, 9, 0), end: at(monday, 12, 0),
	})
	seedThreadMoment(t, app, threadFixtureMoment{
		id: "week-readable", title: "Readable Halcyon work", app: "Workspace",
		start: at(monday, 13, 0), end: at(monday, 16, 0),
	})
	if _, err := app.topicsSvc().store.update(func(state *topicsStateFile) {
		state.Projects = []topicProject{{ID: "t-halcyon", Name: "Halcyon",
			ActivityIDs: []string{"week-protected", "week-readable"}}}
	}); err != nil {
		t.Fatal(err)
	}
	pii.SetUserSensitiveApps(map[string]string{"com.example.finder": "Finder"})
	snap := yourWeekSnapshot{
		Version: 1, StartDate: "2026-10-19", CutoffMs: now.UnixMilli(), HasActivityProvenance: true,
		Activities: []yourWeekSnapshotActivity{
			{ID: "week-protected", DayMs: []int64{3 * time.Hour.Milliseconds(), 0, 0, 0, 0}},
			{ID: "week-readable", DayMs: []int64{3 * time.Hour.Milliseconds(), 0, 0, 0, 0}},
		},
		Topics: []yourWeekSnapshotRef{{ID: "t-halcyon",
			ActivityIDs: []string{"week-protected", "week-readable"}}},
	}
	in := app.yourWeekReadable(context.Background(), snap, monday, now)
	if in.TotalMinutes != 3*60 || len(in.Topics) != 1 || in.Topics[0].Minutes != 3*60 {
		t.Fatalf("mixed protected contribution was retained: %+v", in)
	}
}

func TestYourWeekCapsFrozenActivityAtCurrentReadableDay(t *testing.T) {
	app, brief, now := yourWeekFrozenWindowFixture(t,
		[]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		"2026-10-19", intPointer(5), 1, nil)
	monday := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	readable := seedThreadMoment(t, app, threadFixtureMoment{
		id: "week-partial", title: "Readable partial work", app: "Workspace",
		start: at(monday, 9, 0), end: at(monday, 11, 0),
	})
	protected := seedThreadMoment(t, app, threadFixtureMoment{
		id: "week-partial-protected", title: "Protected partial work", app: "Workspace",
		start: at(monday, 11, 0), end: at(monday, 13, 0),
	})
	if err := app.activity.store.RepointActivityRunLinks(protected.ID, readable.ID); err != nil {
		t.Fatal(err)
	}
	before, _, err := app.activityMomentTimeMs(context.Background(), monday.UnixMilli(), monday.AddDate(0, 0, 1).UnixMilli())
	if err != nil || before[readable.ID] != 4*time.Hour.Milliseconds() {
		t.Fatalf("fixture did not produce four readable hours: %+v, %v", before, err)
	}
	snap := yourWeekSnapshot{
		Version: 1, StartDate: "2026-10-19", CutoffMs: now.UnixMilli(), HasActivityProvenance: true,
		Activities: []yourWeekSnapshotActivity{{
			ID: readable.ID, DayMs: []int64{4 * time.Hour.Milliseconds(), 0, 0, 0, 0},
		}},
		Topics: []yourWeekSnapshotRef{{
			ID: "t-halcyon", ActivityIDs: []string{readable.ID},
		}},
	}
	snapshot, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetUIState(context.Background(), yourWeekSnapshotKey+"2026-10-23", string(snapshot)); err != nil {
		t.Fatal(err)
	}
	if _, err := app.activity.store.db.Exec(
		`UPDATE activity_sessions SET triage_status='protected' WHERE id=?`,
		"session-"+protected.ID,
	); err != nil {
		t.Fatal(err)
	}
	in := app.yourWeekReadable(context.Background(), snap, monday, now)
	if in.TotalMinutes != 2*60 || in.SeenDays != 1 || len(in.Topics) != 1 || in.Topics[0].Minutes != 2*60 {
		t.Fatalf("frozen activity exceeded current readable time: %+v", in)
	}
	week, _ := app.yourWeekForDelivery(brief)
	if week == nil || len(week.Time) != 1 || week.Time[0].TopicID != "t-halcyon" || week.Time[0].Hours != 2 {
		t.Fatalf("delivered week did not use current readable time: %+v", week)
	}
}

func TestYourWeekRegistryMembershipEditsKeepFrozenActivityAssignment(t *testing.T) {
	app, brief, _ := yourWeekFrozenWindowFixture(t,
		[]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		"2026-10-19", intPointer(5), 2,
		[]yourWeekSnapshotRef{{ID: "t-halcyon", Minutes: 6 * 60}, {ID: "t-borealis", Minutes: 4 * 60}})
	if _, err := app.topicsSvc().store.update(func(state *topicsStateFile) {
		state.Projects = []topicProject{
			{ID: "t-halcyon", Name: "Halcyon", ActivityIDs: []string{"week-borealis"}},
			{ID: "t-borealis", Name: "Borealis", ActivityIDs: []string{"week-halcyon"}},
		}
	}); err != nil {
		t.Fatal(err)
	}
	week, _ := app.yourWeekForDelivery(brief)
	if week == nil || len(week.Time) != 2 || week.Time[0].Name != "Halcyon" ||
		week.Time[0].Hours != 6 || week.Time[1].Name != "Borealis" || week.Time[1].Hours != 4 {
		t.Fatalf("registry edit changed frozen topic membership: %+v", week)
	}
}

func TestYourWeekLiveCacheInvalidatedByWithdrawal(t *testing.T) {
	previous := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
	pii.SetUserSensitiveApps(nil)
	app, brief, now := yourWeekFrozenWindowFixture(t,
		[]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		"2026-10-19", intPointer(5), 2,
		[]yourWeekSnapshotRef{{ID: "t-halcyon", Minutes: 6 * 60}, {ID: "t-borealis", Minutes: 4 * 60}})
	ctx := context.Background()
	start, end, cutoff, workDays, ok := app.yourWeekResolvedWindow(ctx, brief)
	if !ok {
		t.Fatal("no resolved window")
	}
	if err := app.store.SetUIState(ctx, yourWeekSnapshotKey+end.Format("2006-01-02"), ""); err != nil {
		t.Fatal(err)
	}
	key := yourWeekLiveKey{app: app, end: end.Format("2006-01-02"), start: start.Format("2006-01-02"),
		cutoff: cutoff.UnixMilli(), workDays: workDays}
	snap := app.gatherYourWeek(ctx, start, cutoff, now, workDays)
	yourWeekLive.mu.Lock()
	yourWeekLive.weeks = map[yourWeekLiveKey]yourWeekLiveWeek{key: {snap: snap, at: now}}
	generation := yourWeekLive.generation
	yourWeekLive.mu.Unlock()
	pii.SetUserSensitiveApps(map[string]string{"com.example.finder": "Finder"})
	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.App, ID: "com.example.finder"})
	refreshed, ok := app.yourWeekSnapshotFor(ctx, start, end, cutoff, now, workDays)
	if !ok {
		t.Fatal("weekly snapshot did not refresh")
	}
	in := app.yourWeekReadable(ctx, refreshed, start, now)
	if in.TotalMinutes != 4*60 {
		t.Fatalf("cache returned withdrawn contribution: %+v", in)
	}
	yourWeekLive.mu.Lock()
	cached, exists := yourWeekLive.weeks[key]
	currentGeneration, entryCount := yourWeekLive.generation, len(yourWeekLive.weeks)
	yourWeekLive.mu.Unlock()
	if !exists || currentGeneration <= generation || cached.snap.TotalMinutes != 4*60 {
		t.Fatalf("privacy withdrawal did not fence cache: generation %d -> %d, entries=%d",
			generation, currentGeneration, entryCount)
	}
	for _, activity := range cached.snap.Activities {
		if activity.ID == "week-halcyon" {
			t.Fatal("pre-withdrawal activity repopulated the live cache")
		}
	}
	yourWeekLive.mu.Lock()
	yourWeekLive.weeks = map[yourWeekLiveKey]yourWeekLiveWeek{key: {snap: refreshed, at: now}}
	generation = yourWeekLive.generation
	yourWeekLive.mu.Unlock()
	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Account, ID: "feishu:lark"})
	yourWeekLive.mu.Lock()
	accountGeneration, accountEntries := yourWeekLive.generation, len(yourWeekLive.weeks)
	yourWeekLive.mu.Unlock()
	if accountEntries != 0 || accountGeneration <= generation {
		t.Fatalf("account sign-out did not clear cache: generation %d -> %d, entries=%d",
			generation, accountGeneration, accountEntries)
	}
	yourWeekLive.mu.Lock()
	yourWeekLive.weeks = map[yourWeekLiveKey]yourWeekLiveWeek{key: {snap: refreshed, at: now}}
	generation = yourWeekLive.generation
	yourWeekLive.mu.Unlock()
	app.emitBrowserEvent(magpieModelsUpdatedEvent, ModelAccountsStatus{})
	yourWeekLive.mu.Lock()
	defer yourWeekLive.mu.Unlock()
	if len(yourWeekLive.weeks) != 0 || yourWeekLive.generation <= generation {
		t.Fatalf("account identity update did not clear cache: generation %d -> %d, entries=%d",
			generation, yourWeekLive.generation, len(yourWeekLive.weeks))
	}
}

func TestYourWeekFrozenWindowReappliesLiveTopicPrivacy(t *testing.T) {
	app, brief, _ := yourWeekFrozenWindowFixture(t,
		[]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday},
		"2026-10-19", intPointer(5), 3,
		[]yourWeekSnapshotRef{{ID: "t-halcyon", Minutes: 6 * 60}, {ID: "t-borealis", Minutes: 4 * 60}})
	if _, err := app.topicsSvc().store.update(func(state *topicsStateFile) {
		state.Projects = []topicProject{{ID: "t-borealis", Name: "Borealis"}}
	}); err != nil {
		t.Fatal(err)
	}
	week, _ := app.yourWeekForDelivery(brief)
	var topics []string
	if week != nil {
		for _, row := range week.Time {
			if !row.Other {
				topics = append(topics, row.Name)
			}
		}
	}
	if week == nil || len(topics) != 1 || topics[0] != "Borealis" {
		t.Fatalf("withdrawn topic remained in the frozen week: %+v", week)
	}
}

func TestGetYourWeekForDateFindsFrozenWeekAfterWorkdayChange(t *testing.T) {
	app, brief, now := yourWeekFrozenWindowFixture(t,
		[]time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday},
		"2026-10-19", intPointer(5), 3,
		[]yourWeekSnapshotRef{{ID: "t-halcyon", Minutes: 6 * 60}})
	service, ok := app.ensureMobileBriefService().(*mobileBriefService)
	if !ok || service.store == nil {
		t.Fatal("mobile brief store unavailable")
	}
	if err := service.store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	entry, err := app.GetYourWeekForDate("2026-10-20")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Week == nil || entry.Week.Start != "2026-10-19" || entry.Week.End != "2026-10-23" {
		t.Fatalf("Activity did not resolve the frozen week: %+v", entry)
	}
}

func intPointer(value int) *int {
	return &value
}

func TestYourWeekAskCardUsesTheRowsSources(t *testing.T) {
	item := MobileBriefItem{ID: "q4", Key: "commitments:q4", Kind: "commitment", Title: "the Q4 plan", Refs: []string{"e1"}}
	brief := MobileBriefV1{Sections: MobileBriefSections{Commitments: []MobileBriefItem{item}},
		YourWeek: &MobileBriefWeek{Ask: &MobileBriefWeekAsk{CardID: "week:ask:commitments:q4", Message: "Help me plan how to start Monday on “the Q4 plan”. Don't write to my calendar."}}}
	card, ok := yourWeekAskCard(brief)
	if !ok || card.ID != "week:ask:commitments:q4" || card.Kind != mobileBriefCardItem || card.Mode != mobileBriefCardSend ||
		card.Message != brief.YourWeek.Ask.Message || fmt.Sprint(card.Refs) != "[e1]" || card.ItemKey != "" {
		t.Fatalf("card: %+v %v", card, ok)
	}
	brief.Sections.Commitments = nil
	if _, ok := yourWeekAskCard(brief); ok {
		t.Fatal("a card for a row that left the brief")
	}
}

func TestYourWeekHygieneDropsMaskedRows(t *testing.T) {
	week := yourWeekFixture("en")
	week.Open[0].Title = "Reply to [name]"
	week.Done[0].Title = "Notes about TBD"
	got := yourWeekHygiene(week)
	if len(got.Open) != 2 || len(got.Done) != 2 {
		t.Fatalf("masked rows kept: %+v", got)
	}
	week.Headline = "A {{topic}} week."
	if yourWeekHygiene(week) != nil {
		t.Fatal("a masked lead kept the card")
	}
}

// seedYourWeekMute mutes "New on your topics" a day before now.
func seedYourWeekMute(t *testing.T, app *App, now time.Time) {
	t.Helper()
	ctx := context.Background()
	at := now.Add(-26 * time.Hour)
	if _, err := app.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{ID: "muted-finds", Kind: string(engagement.KindTopicFinds), SubjectID: "finds", DedupeKey: "muted-finds", State: storage.EngagementStateSent, CreatedAt: at.UnixMilli(), SentAt: at.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.RecordEngagementReceipt(ctx, storage.EngagementReceiptRow{MomentID: "muted-finds", State: storage.EngagementReceiptMuted, Channel: "desktop", At: at.Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := app.engagement.SetKindEnabled(ctx, engagement.KindTopicFinds, false); err != nil {
		t.Fatal(err)
	}
}

// The last-workday evening brief carries the week (card and paragraphs) and rings
// "Your week is ready" once; other briefs, old Fridays and the switch do not.
func TestYourWeekFridayEveningBriefAndDoorbell(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	ctx := context.Background()
	loc := time.UTC
	now := time.Date(2026, 10, 23, 18, 5, 0, 0, loc)
	app.engagement = newEngagementService(app.store, nil, engagementDelivery{Report: app.postEngagementReport})
	app.engagement.now = func() time.Time { return now }
	if err := app.store.SetUIState(ctx, onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
	// Muted this week, still muted now: a learned line with Undo.
	seedYourWeekMute(t, app, now)
	item := MobileBriefItem{ID: "q4", Key: "commitments:q4", Kind: "commitment", Title: "the Q4 plan", Refs: []string{"e1"}}
	brief := MobileBriefV1{BriefID: "2026-10-23-evening", Slot: mobileBriefSlotEvening, LocalDate: "2026-10-23", Timezone: "UTC", AsOf: now.Format(time.RFC3339),
		Sections: MobileBriefSections{Commitments: []MobileBriefItem{item}}}
	week, paragraphs := app.yourWeekForDelivery(brief)
	if week == nil || len(week.Learned) != 1 || week.Learned[0].EngagementKind != string(engagement.KindTopicFinds) {
		t.Fatalf("week: %+v", week)
	}
	if week.Headline != "A quiet week." || len(paragraphs) == 0 || paragraphs[0].Text != week.Headline {
		t.Fatalf("no time on this computer reads as a quiet week: %+v %+v", week, paragraphs)
	}
	if raw, _ := app.store.GetUIState(ctx, yourWeekSnapshotKey+"2026-10-23"); raw != "" {
		t.Fatal("a brief read wrote the week inside the brief's source fence")
	}
	if !app.produceYourWeekReady(brief) {
		t.Fatal("no doorbell")
	}
	raw, _ := app.store.GetUIState(ctx, yourWeekSnapshotKey+"2026-10-23")
	if raw == "" {
		t.Fatal("the doorbell did not freeze the week")
	}
	var frozen struct {
		StartDate string `json:"startDate"`
		WorkDays  int    `json:"workDays"`
	}
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.StartDate != "2026-10-19" || frozen.WorkDays != 5 {
		t.Fatalf("frozen window = %+v", frozen)
	}
	app.engagement.Pass(ctx)
	items := mainChatProactiveItems(t, app)
	if len(items) != 1 || items[0].Kind != string(engagement.KindWeeklyRecap) {
		t.Fatalf("doorbell: %+v", items)
	}
	if items[0].Target.Kind != mainChatTargetToday || items[0].Target.ID != brief.BriefID {
		t.Fatalf("weekly recap target = %+v, want source brief %q", items[0].Target, brief.BriefID)
	}
	if items[0].BriefID != brief.BriefID {
		t.Fatalf("weekly recap Play brief = %q, want %q (not the week's end date)", items[0].BriefID, brief.BriefID)
	}
	// The read the app shows keeps the brief id, so "Read your week" opens
	// the brief that holds the week instead of today's.
	encoded, err := json.Marshal(items[0])
	if err != nil {
		t.Fatal(err)
	}
	_, shown := app.projectProactiveVoiceDelivery(ctx, mainChatProactiveTool, "", string(encoded))
	var projected mainChatProactive
	if err := json.Unmarshal([]byte(shown), &projected); err != nil {
		t.Fatal(err)
	}
	if projected.Target.Kind != mainChatTargetToday || projected.Target.ID != brief.BriefID {
		t.Fatalf("shown weekly recap target = %+v, want source brief %q", projected.Target, brief.BriefID)
	}
	if got := engagementClickData(engagement.Moment{Kind: engagement.KindWeeklyRecap,
		Params: map[string]string{"source_brief_id": brief.BriefID}}); got != "today:brief:"+brief.BriefID {
		t.Fatalf("weekly recap notification target = %q", got)
	}
	// The chat line is the catalog's doorbell, never the week's contents.
	for _, lang := range engagement.Languages {
		catalog, _ := engagement.Copy(engagement.KindWeeklyRecap, lang)
		if body := items[0].Localized[lang].Body; body != catalog.Sealed && body != catalog.PushBody {
			t.Fatalf("%s doorbell: %q", lang, body)
		}
	}
	if !app.produceYourWeekReady(brief) {
		t.Fatal("a second Friday brief rang again instead of standing in for brief-ready")
	}
	app.engagement.Pass(ctx)
	if len(mainChatProactiveItems(t, app)) != 1 {
		t.Fatal("doorbell repeated")
	}
	// Undoing the mute removes its learned line.
	if err := app.engagement.SetKindEnabled(ctx, engagement.KindTopicFinds, true); err != nil {
		t.Fatal(err)
	}
	if week, _ := app.yourWeekForDelivery(brief); week == nil || len(week.Learned) != 0 {
		t.Fatalf("undone mute still shown: %+v", week)
	}
	// Other briefs carry no week; an old Friday is never built afresh.
	for _, other := range []MobileBriefV1{
		{Slot: mobileBriefSlotMorning, LocalDate: "2026-10-23", Timezone: "UTC"},
		{Slot: mobileBriefSlotEvening, LocalDate: "2026-10-22", Timezone: "UTC"},
		{Slot: mobileBriefSlotEvening, LocalDate: "2026-10-16", Timezone: "UTC"},
	} {
		if week, _ := app.yourWeekForDelivery(other); week != nil {
			t.Fatalf("%s %s: %+v", other.Slot, other.LocalDate, week)
		}
	}
	// "Turn off Your week".
	if err := app.engagement.SetKindEnabled(ctx, engagement.KindWeeklyRecap, false); err != nil {
		t.Fatal(err)
	}
	if week, _ := app.yourWeekForDelivery(brief); week != nil {
		t.Fatal("brief ignored the switch")
	}
	// Closed Eyes withholds the week.
	if err := app.engagement.SetKindEnabled(ctx, engagement.KindWeeklyRecap, true); err != nil {
		t.Fatal(err)
	}
	app.closedEyes.enabled.Store(true)
	if week, _ := app.yourWeekForDelivery(brief); week != nil {
		t.Fatal("week shown while observation is paused")
	}
}

// The display pass (displayModelCopy) reads fields by name; every word of
// the week must be one it reads.
func TestYourWeekWordsPassTheDisplayPass(t *testing.T) {
	mask := "[REDACTED:account_number]"
	week := yourWeekFixture("en")
	week.Headline, week.Open[0].Title, week.Open[0].Label, week.Done[0].Title, week.Learned[1].Text, week.Ask.Text, week.Ask.Message, week.Time[0].Name =
		"A "+mask+" week.", mask, mask, mask, mask, mask, mask, mask
	for _, language := range []string{"en", "zh-CN"} {
		got := displayModelCopy(*week, language)
		raw, _ := json.Marshal(got)
		if strings.Contains(string(raw), "[REDACTED") {
			t.Fatalf("%s: a week field skipped the display pass: %s", language, raw)
		}
		if got.Open[0].ItemKey != week.Open[0].ItemKey || got.Open[0].CardID != week.Open[0].CardID || got.Learned[1].MemoryID != week.Learned[1].MemoryID {
			t.Fatal("display changed identities")
		}
	}
	if raw, _ := json.Marshal(week); strings.Contains(string(raw), `"subject"`) {
		t.Fatal("the ask's subject left the computer")
	}
}

func TestPausedBriefRechecksOnlyFrozenWeek(t *testing.T) {
	app, brief, _ := yourWeekFrozenWindowFixture(t, []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}, "2026-10-19", intPointer(5), 3, []yourWeekSnapshotRef{{ID: "t-halcyon", Minutes: 360}})
	_, expected := app.yourWeekForBinding(brief)
	if len(expected) == 0 {
		t.Fatal("fixture has no frozen weekly recap")
	}
	app.closedEyes.enabled.Store(true)
	t.Cleanup(func() { app.closedEyes.enabled.Store(false) })
	ctx := context.WithValue(t.Context(), pausedBriefReadKey{}, true)
	_, paused := app.yourWeekForDeliveryWithLeadContext(ctx, brief, false)
	if !mobileBriefWeeklyRecapMatches(expected, paused) {
		t.Fatal("paused brief could not recheck the frozen weekly recap")
	}
	if week, _ := app.yourWeekForBinding(brief); week != nil {
		t.Fatal("ordinary paused read exposed the week")
	}
	if err := app.store.SetUIState(t.Context(), yourWeekSnapshotKey+"2026-10-23", ""); err != nil {
		t.Fatal(err)
	}
	if week, _ := app.yourWeekForDeliveryWithLeadContext(ctx, brief, false); week != nil {
		t.Fatal("paused proof gathered a new week")
	}
}

func TestYourWeekCarriesOnlyExplicitTaskIdentity(t *testing.T) {
	now := time.Now()
	selected := selectYourWeek(yourWeekInput{Start: now, End: now, Done: []MobileBriefWeekDone{{ID: "outcome-1", Kind: yourWeekDoneOutcome, Title: "Finished", TaskID: "task-1"}, {ID: "outcome-2", Kind: yourWeekDoneOutcome, Title: "Other"}}})
	if selected == nil || len(selected.Done) != 2 || selected.Done[0].TaskID != "task-1" || selected.Done[1].TaskID != "" {
		t.Fatalf("task identities: %+v", selected)
	}
	raw, err := json.Marshal(selected.Done)
	if err != nil || !strings.Contains(string(raw), `"taskId":"task-1"`) || strings.Count(string(raw), `"taskId"`) != 1 {
		t.Fatalf("wire identities: %s %v", raw, err)
	}
}
