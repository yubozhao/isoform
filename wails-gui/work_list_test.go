package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/topicday"
	"wails-gui/internal/workonit"
)

// workTestBriefSource is a brief source with a current brief and its package.
type workTestBriefSource struct {
	mobileTestBriefSource
}

func (s *workTestBriefSource) BriefPackageContext(_ context.Context, briefID string) (mobileBriefPackage, bool) {
	if s.brief == nil || s.brief.BriefID != briefID {
		return mobileBriefPackage{}, false
	}
	raw, err := json.Marshal(s.brief)
	if err != nil {
		return mobileBriefPackage{}, false
	}
	return mobileBriefPackage{BriefID: briefID, Version: "v", Bytes: raw}, true
}

func workTestBrief() *MobileBriefV1 {
	return &MobileBriefV1{Version: 1, BriefID: "2026-11-14-morning", Slot: "morning", LocalDate: "2026-11-14",
		AsOf: "2026-11-14T07:00:12Z", NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "The plan shipped."}},
		Offers: []MobileBriefOffer{
			{ID: "o1", Action: "send", Kind: "prepare", Label: "Prep for Lena", Text: "Lena asked two questions.", Prompt: "Prepare."},
			{ID: "o2", Action: "send", Kind: "plan", Label: "Plan the Q4 outline", Prompt: "Plan."},
		}}
}

// One list for both devices (phone-map B2, computer-map C6): the brief's items
// first in the brief's order, then the topics' proposals, each with an id that
// names its source and stays the same across rebuilds.
func TestWorkListHoldsTheBriefsItemsThenTopicProposals(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	handoffs, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.handoffStore = handoffs
	ctx := context.Background()
	if err := app.store.SetUIState(ctx, topicsDailyEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.buildDailyTopics(ctx); err != nil {
		t.Fatal(err)
	}
	app.mobileBriefSvc = &workTestBriefSource{mobileTestBriefSource{brief: workTestBrief()}}

	list, err := app.GetWorkList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 4 {
		t.Fatalf("items = %d, want 2 brief offers and 2 topic proposals: %+v", len(list.Items), list.Items)
	}
	prep, plan := list.Items[0], list.Items[1]
	if prep.ID != "brief:2026-11-14-morning:offer:o1" || prep.Source != workSourceBrief || prep.Row != "prep" ||
		prep.Kind != workKindOffered || prep.State != workStateOpen || prep.Noticed != "Lena asked two questions." ||
		prep.Work.Title != "Prep for Lena" || prep.Yes.Does != "agent" || prep.Yes.Line == "" || len(prep.Reasons) == 0 {
		t.Fatalf("the brief's first item = %+v", prep)
	}
	if plan.ID != "brief:2026-11-14-morning:offer:o2" || plan.Noticed != "Plan the Q4 outline" || plan.Order != 1 {
		t.Fatalf("the brief's second item = %+v", plan)
	}
	for _, item := range list.Items[2:] {
		if item.Source != workSourceTopic || item.Ref.TopicID == "" || item.ID != workTopicProposalID(item.Ref.TopicID, item.Ref.ProposalID) || len(item.First) != 3 {
			t.Fatalf("a topic item = %+v", item)
		}
	}
	if list.ToAnswer != 4 || list.BackFromAgents != 0 || list.Version == "" || list.UndoUntil == "" {
		t.Fatalf("list counts = %+v", list)
	}
	again, err := app.buildWorkList(ctx, mustParseWorkTime(t, list.GeneratedAt))
	if err != nil || again.Version != list.Version {
		t.Fatalf("a rebuild with nothing changed has another version: %s vs %s (%v)", again.Version, list.Version, err)
	}
}

// A topic's Yes starts a task: the item stays, with your agent, and comes back
// as a result to be judged when the task finishes.
func TestWorkListFollowsATopicProposalToItsAgentAndBack(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	handoffs, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.handoffStore = handoffs
	previous := dailyTopicTaskStart
	dailyTopicTaskStart = func(*App, string) error { return nil }
	t.Cleanup(func() { dailyTopicTaskStart = previous })
	ctx := context.Background()
	if err := app.store.SetUIState(ctx, topicsDailyEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.buildDailyTopics(ctx); err != nil {
		t.Fatal(err)
	}
	view, _ := app.GetDailyTopics()
	harbor := view.Topics[0]
	if _, err := app.AnswerDailyTopicProposal(harbor.ID, harbor.Proposal.ID, topicday.VerdictYes); err != nil {
		t.Fatal(err)
	}
	list, err := app.buildWorkList(ctx, mustParseWorkTime(t, "2026-11-14T06:30:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	item := workTestItem(t, list, workTopicProposalID(harbor.ID, harbor.Proposal.ID))
	if item.State != workStateWithAgent || item.Agent == nil || item.Answer == nil || item.Answer.Action != "yes" || item.Ref.TaskID == "" {
		t.Fatalf("a started proposal = %+v", item)
	}
	if list.ToAnswer != 1 {
		t.Fatalf("a proposal with its agent still counts to answer: %d", list.ToAnswer)
	}
}

// A phone without chat:read gets no topic proposals, as on /today, and its
// counts and version are its own.
func TestWorkListOnAPhoneWithoutChatReadLeavesOutTopics(t *testing.T) {
	list := WorkList{V: 1, Version: "full", Items: []WorkItem{
		{ID: "brief:b:offer:o1", Source: workSourceBrief, State: workStateOpen},
		{ID: "topic:t:p", Source: workSourceTopic, State: workStateOpen},
	}, ToAnswer: 2}
	device := mobileDeviceRecord{}
	device.Scopes = []string{mobileScopeBriefRead}
	got := mobileWorkListForDevice(list, device)
	if len(got.Items) != 1 || got.Items[0].Source != workSourceBrief || got.ToAnswer != 1 || got.Version == "full" {
		t.Fatalf("scoped list = %+v", got)
	}
	device.Scopes = append(device.Scopes, mobileScopeChatRead)
	if got := mobileWorkListForDevice(list, device); len(got.Items) != 2 || got.Version != "full" {
		t.Fatalf("a phone with chat:read lost items: %+v", got)
	}
}

// GET /work serves the same list the computer shows.
func TestMobileWorkRouteServesTheComputersList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YANSU_AGENT_HOME", t.TempDir())
	app := &App{}
	app.mobileBriefSvc = &workTestBriefSource{mobileTestBriefSource{brief: workTestBrief()}}
	bridge := &mobileBridge{app: app}
	device := mobileDeviceRecord{}
	device.Scopes = []string{mobileScopeBriefRead, mobileScopeChatRead}
	recorder := httptest.NewRecorder()
	bridge.serveWorkList(recorder, device)
	var response struct {
		Data WorkList `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	want, err := app.GetWorkList()
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Code != 200 || response.Data.Version != want.Version || len(response.Data.Items) != 2 {
		t.Fatalf("GET /work = %d %+v, want version %s", recorder.Code, response.Data, want.Version)
	}
}

func workTestItem(t *testing.T, list WorkList, id string) WorkItem {
	t.Helper()
	for _, item := range list.Items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("no item %s in %+v", id, list.Items)
	return WorkItem{}
}

func mustParseWorkTime(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// A proposal with its agent says since when (the phone decodes agent.since;
// #1136): the time its Yes was given.
func TestWorkListTopicWithItsAgentSaysSince(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	handoffs, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.handoffStore = handoffs
	previous := dailyTopicTaskStart
	dailyTopicTaskStart = func(*App, string) error { return nil }
	t.Cleanup(func() { dailyTopicTaskStart = previous })
	ctx := context.Background()
	if err := app.store.SetUIState(ctx, topicsDailyEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.buildDailyTopics(ctx); err != nil {
		t.Fatal(err)
	}
	view, _ := app.GetDailyTopics()
	harbor := view.Topics[0]
	if _, err := app.AnswerDailyTopicProposal(harbor.ID, harbor.Proposal.ID, topicday.VerdictYes); err != nil {
		t.Fatal(err)
	}
	list, err := app.buildWorkList(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	item := workTestItem(t, list, workTopicProposalID(harbor.ID, harbor.Proposal.ID))
	if item.Agent == nil || item.Agent.Since == "" || item.Answer == nil || item.Answer.At == "" {
		t.Fatalf("a proposal with its agent has no since: %+v %+v", item.Agent, item.Answer)
	}
	if _, err := time.Parse(time.RFC3339, item.Agent.Since); err != nil {
		t.Fatalf("since = %q", item.Agent.Since)
	}
}

// Code work goes to the code agent (phone-agent v1.1.0 §4): a topic seen in an
// editor or a terminal offers its Yes as code, the rest as else, and the
// brief's offers are else.
func TestWorkListOfferedItemsSayTheirKind(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	previous := topicsDailyCandidatesHook
	topicsDailyCandidatesHook = func(*App) []topicday.Candidate {
		return []topicday.Candidate{
			{ID: "t1", Kind: topicday.KindProject, Name: "Harbor launch", LastSeenAt: "2026-11-14T05:00:00Z", Next: "Ship the checklist", Apps: []string{"Visual Studio Code", "Safari"}},
			{ID: "t2", Kind: topicday.KindSubject, Name: "Tide tables", LastSeenAt: "2026-11-13T05:00:00Z", Apps: []string{"Safari"}},
		}
	}
	t.Cleanup(func() { topicsDailyCandidatesHook = previous })
	ctx := context.Background()
	if err := app.store.SetUIState(ctx, topicsDailyEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.buildDailyTopics(ctx); err != nil {
		t.Fatal(err)
	}
	app.mobileBriefSvc = &workTestBriefSource{mobileTestBriefSource{brief: workTestBrief()}}
	list, err := app.GetWorkList()
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, item := range list.Items {
		if item.Yes.Does != "agent" {
			continue
		}
		switch item.Source {
		case workSourceTopic:
			kinds[item.Ref.TopicID] = item.Yes.Kind
		case workSourceBrief:
			if item.Yes.Kind != workonit.KindElse {
				t.Fatalf("a brief offer's kind = %q, want else", item.Yes.Kind)
			}
		}
	}
	if kinds["t1"] != workonit.KindCode || kinds["t2"] != workonit.KindElse {
		t.Fatalf("topic kinds = %v, want t1 code and t2 else", kinds)
	}
}

// A prep offer for a meeting that has begun is not offered at 3 PM for a 2 PM
// meeting (Bo's Duo, 2026-11-18): the brief stays sealed, but Your work leaves
// it out once the meeting starts, at build and at read time.
func TestWorkListDropsAPrepOfferOnceItsMeetingBegins(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	brief := workTestBrief()
	brief.Offers[0].Refs = []string{"cal-1"}
	brief.Sections.Meetings = []MobileBriefItem{{ID: "m1", Title: "Therapist", Status: "upcoming", Kind: "meeting",
		At: "2026-11-14T14:00:00Z", Refs: []string{"cal-1"}}}
	app.mobileBriefSvc = &workTestBriefSource{mobileTestBriefSource{brief: brief}}
	ctx := context.Background()

	before, err := app.buildWorkList(ctx, mustParseWorkTime(t, "2026-11-14T13:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Items) < 2 || before.Items[0].ID != "brief:2026-11-14-morning:offer:o1" || before.Items[0].ExpiresAt != "2026-11-14T14:00:00Z" {
		t.Fatalf("before the meeting the prep offer leads and ends at its start: %+v", before.Items)
	}
	// The snapshot built at 1 PM, read at 3 PM.
	read := workListDropEnded(before, mustParseWorkTime(t, "2026-11-14T15:00:00Z"))
	if len(read.Items) != len(before.Items)-1 || read.Items[0].ID != "brief:2026-11-14-morning:offer:o2" || read.Items[0].Order != 0 ||
		read.ToAnswer != before.ToAnswer-1 || read.Version == before.Version {
		t.Fatalf("read after the meeting began: %+v", read)
	}
	after, err := app.buildWorkList(ctx, mustParseWorkTime(t, "2026-11-14T15:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range after.Items {
		if item.ID == "brief:2026-11-14-morning:offer:o1" {
			t.Fatalf("a rebuild after the meeting began still offers prep: %+v", item)
		}
	}
	// A plan offer has no end, and a meeting already on ends at its end.
	if !workBriefPrepEnds(brief.Offers[1], brief.Sections).IsZero() {
		t.Fatal("a plan offer got an end")
	}
	brief.Sections.Meetings[0].Status, brief.Sections.Meetings[0].EndAt = "now", "2026-11-14T15:00:00Z"
	if got := workBriefPrepEnds(brief.Offers[0], brief.Sections); !got.Equal(mustParseWorkTime(t, "2026-11-14T15:00:00Z")) {
		t.Fatalf("a meeting on now ends at %v", got)
	}
}
