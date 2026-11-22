package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"wails-gui/internal/topicday"
	"wails-gui/storage"

	"yansu-agent/agent"
)

func newAssistantCallTestApp(t *testing.T) *App {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &App{store: store}
}

func assistantCallsKept(t *testing.T, a *App) []storage.AssistantCall {
	t.Helper()
	assistantCallWrites.Wait()
	calls, err := a.store.ListAssistantCalls(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

// A stretch of the day is one entry with the number of calls it made; a
// stretch whose reads made no call keeps nothing; work of another purpose
// inside it is its own entry.
func TestDayScopeCountsCallsAndKeepsOnlyWhenCalled(t *testing.T) {
	a := newAssistantCallTestApp(t)
	group := sessionGroup{Sessions: []ActivitySessionRow{
		{ID: "sess-1", StartTime: 1000, EndTime: 2000},
		{ID: "sess-2", StartTime: 2000, EndTime: 3000, IsMeeting: true},
	}}

	ctx, scope, opened := withDayScope(context.Background(), group)
	if !opened {
		t.Fatal("no scope opened")
	}
	// A second pass over the same stretch joins the scope.
	inner, same, opened := withDayScope(ctx, group)
	if opened || same != scope {
		t.Fatal("a nested pass opened its own entry")
	}
	noteAssistantCall(ctx)
	noteAssistantCall(inner)
	assistantCallScopeFrom(ctx).add("wechat", "", 1)
	// A brief written inside it counts apart.
	briefCtx, briefScope := withBriefCallScope(ctx)
	noteAssistantCall(briefCtx)
	if briefScope == scope || briefScope.calls() != 1 || scope.calls() != 2 {
		t.Fatalf("brief=%d day=%d", briefScope.calls(), scope.calls())
	}
	a.finishAssistantCall(scope)

	_, empty, _ := withDayScope(context.Background(), group)
	a.finishAssistantCall(empty)
	// No scope: a call counts nowhere and nothing panics.
	noteAssistantCall(context.Background())

	calls := assistantCallsKept(t, a)
	if len(calls) != 1 {
		t.Fatalf("entries = %+v", calls)
	}
	day := calls[0]
	if day.Purpose != storage.AssistantCallDay || day.Times != 2 || day.SpanStart != 1000 || day.SpanEnd != 3000 {
		t.Fatalf("day = %+v", day)
	}
	want := []storage.AssistantCallSource{{Kind: "call", Ref: "sess-2", Count: 1}, {Kind: "screen", Ref: "sess-1", Count: 1}, {Kind: "wechat", Count: 1}}
	if len(day.Sources) != len(want) {
		t.Fatalf("sources = %+v", day.Sources)
	}
	for i := range want {
		if day.Sources[i] != want[i] {
			t.Fatalf("sources = %+v", day.Sources)
		}
	}

	// Forgetting the session in Activity takes the entry with it.
	a.forgetAssistantCallsForSession("sess-2")
	if calls := assistantCallsKept(t, a); len(calls) != 0 {
		t.Fatalf("after Forget: %+v", calls)
	}
}

// An answer's entry is keyed by its question and holds what the context
// tools read for that turn, found by the turn's tag.
func TestAnswerScopeCollectsContextReadsByTag(t *testing.T) {
	a := newAssistantCallTestApp(t)
	ctx, scope := a.beginAnswerCallScope(context.Background(), "tag-1", "item-1")
	noteAssistantCall(ctx)
	noteChatContextRead("tag-1", "MemorySearch", []contextUseEntry{{Kind: "memory", ID: "mem-1"}, {Kind: "knowledge", ID: "kn-1"}})
	noteChatContextRead("tag-1", "PeopleLookup", nil)
	noteChatContextRead("tag-other", "RecentActivity", nil)
	a.endAnswerCallScope("tag-1", scope)
	if answerScopeFor("tag-1") != nil {
		t.Fatal("the turn's scope outlived it")
	}
	calls := assistantCallsKept(t, a)
	if len(calls) != 1 || calls[0].Purpose != storage.AssistantCallAnswer || calls[0].Ref != "item-1" || len(calls[0].Sources) != 3 {
		t.Fatalf("answer = %+v", calls)
	}
	// A question id that isn't an opaque id is not kept as a ref.
	ctx, scope = a.beginAnswerCallScope(context.Background(), "tag-2", "what did Sam ask")
	noteAssistantCall(ctx)
	a.endAnswerCallScope("tag-2", scope)
	for _, c := range assistantCallsKept(t, a) {
		if c.Ref == "what did Sam ask" {
			t.Fatal("prose was kept as a ref")
		}
	}
}

// A brief keeps its evidence by kind and id, never its labels.
func TestBriefEntryKeepsEvidenceIDsNotLabels(t *testing.T) {
	a := newAssistantCallTestApp(t)
	a.recordBriefCall(MobileBriefV1{BriefID: "brief-1", Evidence: []MobileBriefRef{
		{SourceKind: "mail", SourceID: "m1", Label: "Contract from Priya"},
		{SourceKind: "mail", SourceID: "m2", Label: "Invoice"},
		{SourceKind: "calendar_day", SourceID: "cal-1", Label: "Board meeting"},
		{SourceKind: "principal", SourceID: "Priya Shah"},
	}}, time.UnixMilli(5000))
	calls := assistantCallsKept(t, a)
	if len(calls) != 1 || calls[0].Ref != "brief-1" {
		t.Fatalf("brief = %+v", calls)
	}
	kinds := map[string]int{}
	for _, src := range calls[0].Sources {
		kinds[src.Kind] += src.Count
		if src.Ref == "Priya Shah" || src.Ref == "Contract from Priya" {
			t.Fatalf("a label was kept: %+v", src)
		}
	}
	if kinds["mail"] != 2 || kinds["calendar"] != 1 || kinds["people"] != 1 {
		t.Fatalf("kinds = %v", kinds)
	}
}

// A proposal counts when it is written: once, for a new id, never again
// when the same proposal is still offered.
func TestProposalsCountWhenWritten(t *testing.T) {
	a := newAssistantCallTestApp(t)
	first := topicday.Copy{BuiltAt: 1000, Topics: []topicday.Topic{
		{ID: "topic-1", Proposal: &topicday.Proposal{ID: "prop-1"}},
		{ID: "topic-2"},
	}}
	a.recordWrittenProposals(nil, first)
	second := topicday.Copy{BuiltAt: 2000, Topics: []topicday.Topic{
		{ID: "topic-1", Proposal: &topicday.Proposal{ID: "prop-1"}},
		{ID: "topic-2", Proposal: &topicday.Proposal{ID: "prop-2"}},
	}}
	a.recordWrittenProposals(&first, second)
	calls := assistantCallsKept(t, a)
	if len(calls) != 2 {
		t.Fatalf("proposals = %+v", calls)
	}
	for _, c := range calls {
		if c.Purpose != storage.AssistantCallProposal || c.Times != 1 || len(c.Sources) != 1 || c.Sources[0].Kind != "topic_names" {
			t.Fatalf("proposal = %+v", c)
		}
	}
}

// Forget everything empties the record, as its last step.
func TestForgetEverythingEndsWithTheRecord(t *testing.T) {
	last := forgetEverythingSteps[len(forgetEverythingSteps)-1]
	if last.id != "assistantCalls" {
		t.Fatalf("last step = %s", last.id)
	}
	a := newAssistantCallTestApp(t)
	a.recordMessagesCall("abc123", time.UnixMilli(1), time.UnixMilli(2))
	assistantCallWrites.Wait()
	if err := last.run(a, context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := assistantCallsKept(t, a); len(calls) != 0 {
		t.Fatalf("left %+v", calls)
	}
}

// Your data's assistant row counts this week's briefs and answers from the
// record: last week's don't count.
func TestYourDataCountsThisWeeksAssistantCalls(t *testing.T) {
	a := newAssistantCallTestApp(t)
	now := time.Now()
	old := now.Add(-8 * 24 * time.Hour).UnixMilli()
	for _, c := range []storage.AssistantCall{
		{Purpose: storage.AssistantCallBrief, Ref: "brief-1", At: now.UnixMilli()},
		{Purpose: storage.AssistantCallBrief, Ref: "brief-old", At: old},
		{Purpose: storage.AssistantCallAnswer, Ref: "item-1", At: now.UnixMilli()},
		{Purpose: storage.AssistantCallAnswer, Ref: "item-2", At: now.UnixMilli()},
		{Purpose: storage.AssistantCallProposal, Ref: "prop-1", At: now.UnixMilli()},
		{Purpose: storage.AssistantCallDay, At: now.UnixMilli()},
	} {
		a.recordAssistantCall(c)
	}
	assistantCallWrites.Wait()
	got, err := a.GetYourDataSummary()
	if err != nil {
		t.Fatal(err)
	}
	n := func(p *int) int {
		if p == nil {
			t.Fatal("a count read as unknown")
		}
		return *p
	}
	if n(got.AssistantBriefs) != 1 || n(got.AssistantAnswers) != 2 || n(got.AssistantProposals) != 1 || n(got.AssistantDay) != 1 {
		t.Fatalf("summary = briefs %d answers %d proposals %d day %d", *got.AssistantBriefs, *got.AssistantAnswers, *got.AssistantProposals, *got.AssistantDay)
	}
	// An empty week is zero, not unknown.
	empty, err := newAssistantCallTestApp(t).GetYourDataSummary()
	if err != nil || empty.AssistantBriefs == nil || *empty.AssistantBriefs != 0 {
		t.Fatalf("empty week = %v, %v", empty.AssistantBriefs, err)
	}
}

// A failed count is unknown (nil), never a zero the row would show as true.
func TestYourDataAssistantCountsAreNilWhenTheRecordFails(t *testing.T) {
	a := newAssistantCallTestApp(t)
	a.store.Close()
	got, _ := a.GetYourDataSummary()
	if got.AssistantBriefs != nil || got.AssistantAnswers != nil || got.AssistantProposals != nil || got.AssistantDay != nil {
		t.Fatalf("a failed count read as a number: %v %v %v %v", got.AssistantBriefs, got.AssistantAnswers, got.AssistantProposals, got.AssistantDay)
	}
	// Without a store at all, too.
	if got, _ := (&App{}).GetYourDataSummary(); got.AssistantBriefs != nil {
		t.Fatal("no store read as a count")
	}
}

// A routed call outside any piece of work is still recorded: a background
// pass as learning, one entry per kind per hour; a chat call as an answer.
// Hand-offs and scheduled runs aren't the assistant's reading.
func TestUnscopedCallsAreRecordedToo(t *testing.T) {
	a := newAssistantCallTestApp(t)
	for _, input := range []*agent.TaskInput{
		{Caller: "(*App).generateTimelineSummary", BackendHint: agent.BackendHintEphemeral},
		{Caller: "(*App).generateTimelineSummary", BackendHint: agent.BackendHintEphemeral},
		{Caller: "(*App).runDreamMerge", BackendHint: agent.BackendHintLearning},
		{Caller: "(*App).sendTodayChat", BackendHint: agent.BackendHintChat},
		{Caller: "(*App).runHandoff", BackendHint: agent.BackendHintHandoff},
		{Caller: "(*App).executeAgentJob", BackendHint: agent.BackendHintCron},
	} {
		a.noteRoutedAssistantCall(context.Background(), input)
	}
	calls := assistantCallsKept(t, a)
	byPurpose := map[string][]storage.AssistantCall{}
	for _, c := range calls {
		byPurpose[c.Purpose] = append(byPurpose[c.Purpose], c)
	}
	learning := byPurpose[storage.AssistantCallLearning]
	if len(learning) != 2 || len(byPurpose[storage.AssistantCallAnswer]) != 1 || len(calls) != 3 {
		t.Fatalf("entries = %+v", calls)
	}
	kinds := map[string]int{}
	for _, c := range learning {
		kinds[c.Sources[0].Kind] = c.Times
	}
	if kinds["activity"] != 2 || kinds["memory"] != 1 {
		t.Fatalf("learning kinds = %v", kinds)
	}
	// Under a scope the call counts for that work only.
	ctx, scope, _ := withDayScope(context.Background(), sessionGroup{})
	a.noteRoutedAssistantCall(ctx, &agent.TaskInput{Caller: "(*App).runActivityTriage"})
	if scope.calls() != 1 || len(assistantCallsKept(t, a)) != 3 {
		t.Fatal("a scoped call was also recorded on its own")
	}
}

// See them groups the record: readings of the day with short gaps are one
// stretch, a watch hour is its own row, briefs and answers keep their kinds,
// and learning is counted, not listed.
func TestAssistantGivenGroupsTheWeek(t *testing.T) {
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, time.Local).UnixMilli()
	min := int64(time.Minute / time.Millisecond)
	view := assistantGivenFrom([]storage.AssistantCall{
		{Purpose: storage.AssistantCallDay, At: base + 10*min, SpanStart: base, SpanEnd: base + 10*min, Times: 2,
			Sources: []storage.AssistantCallSource{{Kind: "screen", Ref: "s1", Count: 1}, {Kind: "wechat", Count: 2}}},
		{Purpose: storage.AssistantCallDay, At: base + 30*min, SpanStart: base + 20*min, SpanEnd: base + 30*min, Times: 1,
			Sources: []storage.AssistantCallSource{{Kind: "screen", Ref: "s2", Count: 1}}},
		// Two hours later: a new stretch.
		{Purpose: storage.AssistantCallDay, At: base + 180*min, SpanStart: base + 170*min, SpanEnd: base + 180*min, Times: 1,
			Sources: []storage.AssistantCallSource{{Kind: "call", Ref: "s3", Count: 1}}},
		{Purpose: storage.AssistantCallDay, At: base + 300*min, SpanStart: base + 240*min, SpanEnd: base + 300*min, Times: 1,
			Sources: []storage.AssistantCallSource{{Kind: "watch", Ref: "rec-1", Count: 1}}},
		{Purpose: storage.AssistantCallBrief, Ref: "brief-1", At: base, Sources: []storage.AssistantCallSource{{Kind: "mail", Ref: "m1", Count: 1}, {Kind: "mail", Ref: "m2", Count: 1}}},
		{Purpose: storage.AssistantCallAnswer, At: base, Sources: []storage.AssistantCallSource{{Kind: "chat", Count: 1}}},
		// A kind-only hour of learning about you, which made two claims, and
		// a sense hour that names its sessions.
		{Purpose: storage.AssistantCallLearning, Ref: "learning:about:2026112009", At: base, SpanStart: base, SpanEnd: base + 60*min, Times: 7, Made: 2,
			Sources: []storage.AssistantCallSource{{Kind: "memory", Count: 7}}},
		{Purpose: storage.AssistantCallLearning, Ref: "learning:sense:2026112009", At: base, SpanStart: base, SpanEnd: base + 60*min, Times: 2,
			Sources: []storage.AssistantCallSource{{Kind: "screen", Ref: "s1", Count: 1}}},
		{Purpose: storage.AssistantCallLearning, Ref: "learning:other:2026112009", At: base, Times: 4},
	})
	if len(view.Stretches) != 2 || len(view.Watch) != 1 {
		t.Fatalf("stretches=%+v watch=%+v", view.Stretches, view.Watch)
	}
	first := view.Stretches[1] // newest first
	if first.Times != 3 || !first.Screen || first.WeChat != 2 || first.From != base || first.To != base+30*min || first.EveryMinutes != 10 {
		t.Fatalf("first stretch = %+v", first)
	}
	if view.Stretches[0].Calls != 1 || view.Stretches[0].Screen {
		t.Fatalf("call stretch = %+v", view.Stretches[0])
	}
	if len(view.Briefs) != 1 || view.Briefs[0].Sources[0] != (AssistantGivenSource{Kind: "mail", Count: 2}) {
		t.Fatalf("briefs = %+v", view.Briefs)
	}
	if len(view.Answers) != 1 || view.Answers[0].Removable {
		t.Fatalf("an answer naming nothing read as removable: %+v", view.Answers)
	}
	if len(view.About) != 1 || view.About[0].Made != 2 || view.About[0].Times != 7 || view.About[0].Removable || len(view.About[0].Sources) != 0 {
		t.Fatalf("about = %+v", view.About)
	}
	if len(view.Sense) != 1 || !view.Sense[0].Removable || view.Sense[0].Sources[0].Kind != "screen" {
		t.Fatalf("sense = %+v", view.Sense)
	}
	if len(view.Work) != 0 || view.Held != 4 {
		t.Fatalf("work=%+v held=%d", view.Work, view.Held)
	}
}

// A store that can't be read is an error, never an empty sheet.
func TestAssistantGivenFailsLoudly(t *testing.T) {
	if _, err := (&App{}).GetAssistantGiven(); err == nil {
		t.Fatal("no store read as an empty week")
	}
	a := newAssistantCallTestApp(t)
	a.store.Close()
	if _, err := a.GetAssistantGiven(); err == nil {
		t.Fatal("a closed store read as an empty week")
	}
}

// A learning pass names what it read, so forgetting a session in Activity,
// or any withdrawn source, removes the learning entries that read it.
func TestForgettingASourceRemovesTheLearningEntriesThatReadIt(t *testing.T) {
	a := newAssistantCallTestApp(t)
	ctx, scope, opened := withLearningScope(context.Background(), learningGroupSense)
	if !opened {
		t.Fatal("no scope")
	}
	scope.addSessions([]string{"sess-1", "sess-2"})
	a.noteRoutedAssistantCall(ctx, &agent.TaskInput{Caller: "(*App).generateTimelineRunSummary"})
	a.finishAssistantCall(scope)
	a.recordLearningRead(learningGroupAbout, storage.AssistantCallSource{Kind: "memory", Ref: "mem-1", Count: 1})
	a.recordLearningRead(learningGroupWork, storage.AssistantCallSource{Kind: "people", Ref: "person-1", Count: 1})
	calls := assistantCallsKept(t, a)
	if len(calls) != 3 {
		t.Fatalf("learning entries = %+v", calls)
	}
	for _, c := range calls {
		if c.Purpose != storage.AssistantCallLearning || c.Times != 1 {
			t.Fatalf("entry = %+v", c)
		}
	}

	// Forget in Activity, by session.
	a.forgetAssistantCallsForSession("sess-2")
	if calls := assistantCallsKept(t, a); len(calls) != 2 {
		t.Fatalf("after forgetting a session: %+v", calls)
	}
	// A memory withdrawn elsewhere, through the privacy bus.
	a.assistantCallsOnWithdrawal(privacyWithdrawal{Scope: privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "memory", ID: "mem-1"}})
	a.assistantCallsOnWithdrawal(privacyWithdrawal{Scope: privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "principal", ID: "person-1"}})
	if calls := assistantCallsKept(t, a); len(calls) != 0 {
		t.Fatalf("after withdrawals: %+v", calls)
	}
}

// A stretch of time withdrawn as a whole removes the day and learning
// entries over it, not the briefs or answers.
func TestWithdrawnTimeRangeRemovesItsEntries(t *testing.T) {
	a := newAssistantCallTestApp(t)
	a.recordAssistantCall(storage.AssistantCall{Purpose: storage.AssistantCallDay, At: 2000, SpanStart: 1000, SpanEnd: 2000})
	a.recordAssistantCall(storage.AssistantCall{Purpose: storage.AssistantCallDay, At: 9000, SpanStart: 8000, SpanEnd: 9000})
	a.recordAssistantCall(storage.AssistantCall{Purpose: storage.AssistantCallBrief, Ref: "brief-1", At: 1500})
	assistantCallWrites.Wait()
	a.assistantCallsOnWithdrawal(privacyWithdrawal{Scope: privacyWithdrawalScope{Kind: privacyWithdrawalTimeRange, StartMs: 1500, EndMs: 3000}})
	calls := assistantCallsKept(t, a)
	if len(calls) != 2 {
		t.Fatalf("entries = %+v", calls)
	}
	for _, c := range calls {
		if c.Purpose == storage.AssistantCallDay && c.SpanStart == 1000 {
			t.Fatal("the withdrawn stretch kept its entry")
		}
	}
}

// A learning pass's group comes from its ref; the claims it made are kept.
func TestLearningGroupsAndMadeClaims(t *testing.T) {
	a := newAssistantCallTestApp(t)
	ctx, scope, _ := withLearningScope(context.Background(), learningGroupAbout)
	scope.add("memory", "mem-1", 1)
	scope.madeClaims(2)
	a.noteRoutedAssistantCall(ctx, &agent.TaskInput{Caller: "(*ActivityTriager).runMemoryGeneration"})
	a.finishAssistantCall(scope)
	// An unscoped compose call is work made for you, by the hour.
	a.noteRoutedAssistantCall(context.Background(), &agent.TaskInput{Caller: "(*ActivityTriager).runComposerPrompt", BackendHint: agent.BackendHintEphemeral})
	assistantCallsKept(t, a)
	view, err := a.GetAssistantGiven()
	if err != nil {
		t.Fatal(err)
	}
	if len(view.About) != 1 || view.About[0].Made != 2 || !view.About[0].Removable {
		t.Fatalf("about = %+v", view.About)
	}
	if len(view.Work) != 1 || view.Work[0].Removable {
		t.Fatalf("work = %+v", view.Work)
	}
	if learningGroupOf("learning:bogus:2026112009") != learningGroupOther || learningGroupOf("day") != learningGroupOther {
		t.Fatal("an unknown group was shown")
	}
}

// Every evidence kind a brief can carry has a See them name (design T15:
// "Other" goes). The known kinds are the ones mobileBriefEvidenceCounts
// doesn't call "other".
func TestEveryBriefEvidenceKindIsNamed(t *testing.T) {
	src, err := os.ReadFile("mobile_brief_model_binding.go")
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`(?m)^\s*case ("activity".*):\s*$`).FindSubmatch(src)
	if line == nil {
		t.Fatal("mobileBriefEvidenceCounts' kind list moved; update this test")
	}
	kinds := regexp.MustCompile(`"([a-z_]+)"`).FindAllSubmatch(line[1], -1)
	if len(kinds) < 30 {
		t.Fatalf("read only %d kinds", len(kinds))
	}
	for _, k := range kinds {
		kind := string(k[1])
		mapped, ok := briefEvidenceKindMapped(kind)
		if !ok {
			t.Errorf("brief evidence kind %q has no See them name", kind)
			continue
		}
		if !storage.AssistantCallSourceKind(mapped) {
			t.Errorf("%q maps to %q, which the record doesn't keep", kind, mapped)
		}
	}
}

// The summary-reading passes are one named row each for the week, in
// Learning about you's order, and never held back (design T14).
func TestSummaryPassesAreNamedRows(t *testing.T) {
	a := newAssistantCallTestApp(t)
	for _, pass := range []string{passWeekly, passPurpose, passWeekly, passSelfDream} {
		ctx, scope := withPassScope(context.Background(), pass)
		a.noteRoutedAssistantCall(ctx, &agent.TaskInput{Caller: "(*App).GenerateWeeklySummary"})
		a.finishAssistantCall(scope)
	}
	// A pass's call is counted once, for the pass.
	assistantCallsKept(t, a)
	view, err := a.GetAssistantGiven()
	if err != nil {
		t.Fatal(err)
	}
	want := []AssistantGivenPass{{Pass: passPurpose, Times: 1}, {Pass: passSelfDream, Times: 1}, {Pass: passWeekly, Times: 2}}
	if len(view.Passes) != len(want) {
		t.Fatalf("passes = %+v", view.Passes)
	}
	for i := range want {
		if view.Passes[i] != want[i] {
			t.Fatalf("passes = %+v", view.Passes)
		}
	}
	if view.Held != 0 || len(view.About) != 0 {
		t.Fatalf("a pass was held or shown by the hour: held=%d about=%+v", view.Held, view.About)
	}
}
