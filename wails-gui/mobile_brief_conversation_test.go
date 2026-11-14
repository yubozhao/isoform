package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
	"wails-gui/internal/pii"
	"wails-gui/storage"
	"yansu-agent/agent"
)

type conversationFixture struct{ Date, Summary, ZhSummary, State, Kind, Action, Next, ZhNext string }

func conversationFixtures(t *testing.T) []conversationFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/brief-conversation/days.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []conversationFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func conversationFixtureInput(t *testing.T, f conversationFixture, lang string) mobileBriefInput {
	t.Helper()
	now, err := time.Parse(time.RFC3339, f.Date+"T09:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	summary, next := f.Summary, f.Next
	if lang == "zh-CN" {
		summary, next = f.ZhSummary, f.ZhNext
	}
	outcome := &ActivityThreadOutcome{Kind: f.Kind, State: f.State, Summary: summary, NextStep: next, Action: f.Action, Confidence: .9}
	return mobileBriefInput{Slot: "morning", AsOf: now, Location: time.UTC, WindowStart: now.Add(-24 * time.Hour),
		OpenLoops: []mobileBriefCandidate{{SourceKind: "summary", SourceID: f.Kind, Title: summary, Status: "needs_review", Kind: mobileBriefThreadOutcomeSource, At: now.AddDate(0, 0, -7), Outcome: outcome, NextStep: next},
			{SourceKind: "history", SourceID: "plan", Title: pick(map[bool]string{true: "zh", false: "en"}[lang == "zh-CN"], "Send the plan to Dana", "把计划发给Dana"), Kind: "one_time_work", Status: "blocked", At: now.AddDate(0, 0, -4)}},
	}
}

func conversationFixtureBrief(t *testing.T, f conversationFixture, lang string) MobileBriefV1 {
	return buildMobileBriefV1(conversationFixtureInput(t, f, lang))
}

func TestBriefConversationFixtureVoice(t *testing.T) {
	for _, f := range conversationFixtures(t) {
		for _, lang := range []string{"en", "zh-CN"} {
			t.Run(f.Date+"/"+lang, func(t *testing.T) {
				brief := conversationFixtureBrief(t, f, lang)
				paras := mobileBriefNarrativeTemplate(brief, lang)
				if len(paras) == 0 {
					t.Fatal("missing brief")
				}
				if err := validateMobileBriefClosing(brief, paras, lang); err != nil {
					t.Fatal(err)
				}
				if err := validateMobileBriefNarrativeVoice(brief, paras, lang); err != nil {
					t.Fatal(err)
				}
				closing, offers := mobileBriefClosing(brief, lang)
				if len(offers) < 1 || len(offers) > 2 || paras[len(paras)-1].Text != closing.Text {
					t.Fatal("missing closing offers")
				}
				for _, offer := range offers {
					if offer.Action == "" || offer.Prompt == "" || len(offer.Refs) == 0 {
						t.Fatal("non-actionable offer")
					}
				}
				for _, p := range paras {
					if mobileBriefBannedOutcomePhrase(p.Text) || len(p.Text) > mobileBriefNarrativeMaxBytes {
						t.Fatalf("invalid copy: %s", p.Text)
					}
				}
				if !mobileBriefNarrativeWithinBudget(paras) {
					t.Fatal("over budget")
				}
				raw, _ := json.Marshal(mobileBriefNarrativeReply{Paragraphs: func() []mobileBriefNarrativeReplyParagraph {
					var out []mobileBriefNarrativeReplyParagraph
					for _, p := range paras {
						out = append(out, mobileBriefNarrativeReplyParagraph{Text: p.Text, Refs: p.Refs})
					}
					return out
				}()})
				parsed, err := parseMobileBriefNarrative(string(raw), mobileBriefNarrativeInputEvidence(brief))
				if err != nil {
					t.Fatal(err)
				}
				if err := validateMobileBriefClosing(brief, parsed, lang); err != nil {
					t.Fatal(err)
				}
				if err := validateMobileBriefClosing(brief, paras[:len(paras)-1], lang); err == nil {
					t.Fatal("missing closing accepted")
				}
				brief.Narrative = paras
				// A template is never spoken (design R74): its closing is shown, not read aloud.
				if spoken := mobileBriefSpokenText(brief, lang); spoken != "" {
					t.Fatalf("a template was spoken: %q", spoken)
				}
				t.Logf("Example %s (%s):\n%s", f.Date, lang, strings.Join(func() []string {
					var texts []string
					for _, p := range paras {
						texts = append(texts, p.Text)
					}
					return texts
				}(), "\n\n"))
			})
		}
	}
}

func TestBriefConversationBannedOutput(t *testing.T) {
	for _, phrase := range []string{"I saw", "I noticed", "I observed", "I did not see", "I can’t tell", "Completion is unconfirmed", "unclear whether", "我看到了", "我注意到", "完成情况尚未确认", "无法判断"} {
		t.Run(phrase, func(t *testing.T) {
			brief := buildMobileBriefV1(mobileBriefInput{AsOf: time.Now(), OpenLoops: []mobileBriefCandidate{{SourceKind: "history", SourceID: "bad", Title: phrase, Status: "blocked"}}})
			// The model's sentence goes; with nothing else, so does its text.
			var refs []string
			for _, ref := range mobileBriefNarrativeInputEvidence(brief) {
				refs = append(refs, ref.Ref)
			}
			raw, _ := json.Marshal(mobileBriefNarrativeReply{Paragraphs: []mobileBriefNarrativeReplyParagraph{{Text: phrase + " the plan.", Refs: refs}}})
			if _, _, err := mobileBriefNarrativeFromReply(brief, mobileBriefPicks(brief), string(raw), "English"); err == nil {
				t.Fatal("banned output accepted")
			}
			for _, lang := range []string{"en", "zh-CN"} {
				for _, p := range mobileBriefNarrativeTemplate(brief, lang) {
					if mobileBriefBannedOutcomePhrase(p.Text) {
						t.Fatal("template leaked phrase")
					}
				}
			}
		})
	}
	prompt := mobileBriefTestPrompt(conversationFixtureBrief(t, conversationFixtures(t)[1], "en"), "en")
	for _, rule := range []string{"question", "I saw", "I noticed", "I can't tell"} {
		if !strings.Contains(prompt, rule) {
			t.Fatalf("prompt missing %s", rule)
		}
	}
}

func TestBriefConversationAcrossDaysRankingCapAndDedupe(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	candidates := []mobileBriefCandidate{}
	for i := 0; i < 7; i++ {
		candidates = append(candidates, mobileBriefCandidate{SourceKind: "history", SourceID: fmt.Sprint(i), Title: fmt.Sprintf("Open task %d", i), Status: "active", Kind: "one_time_work", At: now.AddDate(0, 0, -i-3)})
	}
	candidates[0].DueAt = now.Add(2 * time.Hour)
	candidates[1].DueAt = now.AddDate(0, 0, -1)
	candidates[6].Person = "Dana"
	in := mobileBriefInput{Slot: "evening", AsOf: now, Location: time.UTC, OpenLoops: candidates}
	first := buildMobileBriefV1(in)
	ids := func(brief MobileBriefV1) []string {
		var out []string
		for _, item := range brief.Sections.OpenLoops {
			for _, ref := range brief.Evidence {
				if ref.Ref == item.Refs[0] {
					out = append(out, ref.SourceID)
				}
			}
		}
		return out
	}
	if got := ids(first); !reflect.DeepEqual(got, []string{"1", "0", "6", "5"}) {
		t.Fatalf("rank/cap: %v", got)
	}
	// Rendered loops are remembered; due-today is allowed to recur even within
	// the same local day. An evening brief receives old work just like morning.
	first.Narrative = mobileBriefNarrativeTemplate(first, "en")
	for _, item := range first.Sections.OpenLoops {
		first.Narrative = append(first.Narrative, MobileBriefPara{Text: "Raised", Refs: item.Refs})
	}
	in.AsOf = now.Add(time.Hour)
	in.PreviousBriefs = []MobileBriefV1{first}
	second := buildMobileBriefV1(in)
	if got := ids(second); !reflect.DeepEqual(got, []string{"0", "4", "3", "2"}) {
		t.Fatalf("dedupe/today exception: %v", got)
	}
	in.AsOf = now.AddDate(0, 0, 1)
	in.PreviousBriefs = []MobileBriefV1{second, first}
	third := buildMobileBriefV1(in)
	if got := ids(third); len(got) != 0 {
		t.Fatalf("last two briefs not deduped: %v", got)
	}
	in.PreviousBriefs = []MobileBriefV1{third, second}
	if got := ids(buildMobileBriefV1(in)); len(got) == 0 || got[0] != "1" {
		t.Fatalf("older brief suppressed forever: %v", got)
	}
	// A rich alias cannot revive an already-raised source or exceed the cap.
	in.PreviousBriefs = []MobileBriefV1{first}
	in.Commitments = []mobileBriefCandidate{candidates[1]}
	in.WorkingToward = []mobileBriefCandidate{candidates[1]}
	selected := selectMobileBriefOpenLoops(in)
	if len(selected.Commitments) != 0 || len(selected.WorkingToward) != 0 {
		t.Fatal("alias bypassed dedupe")
	}
	a, b := candidates[3], candidates[3]
	a.SourceID = "person"
	a.Person = "Dana"
	b.SourceID = "anonymous"
	if !mobileBriefOpenLoopFirst(a, b) {
		t.Fatal("person did not break equal-age tie")
	}
	in.PreviousBriefs = nil
	in.WaitingOnYou = []mobileBriefCandidate{candidates[2], candidates[3], candidates[4]}
	if capped := selectMobileBriefOpenLoops(in); len(capped.WaitingOnYou) != 0 {
		t.Fatal("person card bypassed the primary loop cap")
	}
}

func TestBriefConversationEmptyCalendarFields(t *testing.T) {
	for _, lang := range []string{"en", "zh-CN"} {
		brief := MobileBriefV1{}
		text := mobileBriefTemplateItem(brief, MobileBriefItem{}, "Day ahead", pick(map[bool]string{true: "zh", false: "en"}[lang == "zh-CN"], "dinner", "晚餐"), mobileBriefVoiceLang(lang), lang)
		if strings.Contains(text, "at :") || strings.Contains(text, "，时间是；") || strings.Contains(text, "Free ,") || text == "" {
			t.Fatalf("empty fields: %s", text)
		}
	}
}

func TestTodayChatWholeBriefRevisionContextAndPrivacy(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	writeTodayContextMemory(t, "brief-note", "# Current plan\n\n- **Source**: user_edit\n\nCurrent plan.")
	now := time.Now()
	brief := buildMobileBriefV1(mobileBriefInput{Slot: "morning", AsOf: now.Add(-time.Minute), OpenLoops: []mobileBriefCandidate{{SourceKind: "memory", SourceID: "brief-note", Title: "Current plan", Status: "active", Kind: "goal", At: now.Add(-time.Hour)}}})
	brief.Narrative = mobileBriefNarrativeTemplate(brief, "en")
	_, brief.Offers = mobileBriefClosing(brief, "en")
	dir, _ := mobileBriefStateDir()
	bindConversationBriefFixture(t, app, &brief)
	if err := newMobileBriefStore(dir).save(brief, now); err != nil {
		t.Fatal(err)
	}
	safe := app.mobileBriefForDelivery(brief)
	ref := TodayChatContextRef{SourceKind: "brief", SourceID: mobileBriefConversationKey(safe), BriefID: brief.BriefID}
	item, err := app.resolveTodayChatWholeBrief(context.Background(), ref, now)
	if err != nil {
		t.Fatal(err)
	}
	got := buildTodayChatContextSections(item, todayChatSources{})
	if len(got.Text) > todayChatBriefTokens+12000 || !strings.Contains(got.Text, "Current plan") || !strings.Contains(got.Text, "brief-note") || !strings.Contains(got.Text, "narrative") {
		t.Fatalf("invalid whole context: %s", got.Text)
	}
	// An offer is a brief card now (TestTodayBriefCardsClickThrough).
	ref.SourceID = "invented"
	if _, err := app.resolveTodayChatWholeBrief(context.Background(), ref, now); err == nil {
		t.Fatal("invented revision accepted")
	}
	memory, _ := memoryDir()
	if err := os.Remove(filepath.Join(memory, "brief-note.md")); err != nil {
		t.Fatal(err)
	}
	ref.SourceID = mobileBriefConversationKey(safe) + ":0"
	if _, err := app.resolveTodayChatWholeBrief(context.Background(), ref, now); err == nil {
		t.Fatal("withdrawn evidence retained whole brief")
	}
	withdrawn := app.mobileBriefForDelivery(brief)
	if len(withdrawn.Narrative) != 0 || len(withdrawn.Offers) != 0 || withdrawn.Audio != nil {
		t.Fatal("privacy failed to withdraw offers/text/audio")
	}
}

func TestBriefConversationRealSourcesAcrossDaysAndWithdrawal(t *testing.T) {
	isolateActivityPrivacyTest(t)
	app := newPrivacyReadTestApp(t)
	now := time.Now()
	fixtures := threadOutcomeFixtures(t)
	for index, f := range fixtures {
		row := seedThreadFixture(t, app, fmt.Sprintf("older-%d", index), f, now.AddDate(0, 0, -14-index))
		saveFixtureOutcome(t, app, row, f)
	}
	in := mobileBriefInput{AsOf: now, Slot: "evening", Location: time.UTC}
	collectMobileBriefOlderThreads(t.Context(), app, &in)
	if len(in.OpenLoops) == 0 {
		t.Fatal("no earlier-day unfinished outcomes")
	}
	for _, item := range in.OpenLoops {
		if !item.At.Before(now.AddDate(0, 0, -7)) || !threadOutcomeUnfinished(item.Outcome) || item.Outcome.UserActed == "observed" {
			t.Fatalf("invalid older outcome: %+v", item)
		}
	}
	for _, item := range in.OpenLoops {
		if !app.threadOutcomeSourceReadable(t.Context(), item.SourceID) {
			t.Fatal("missing provenance")
		}
	}
	// The item path must resolve an observed handoff older than chat's ordinary
	// seven-day retrieval window, using its complete current source closure.
	handoffs, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.handoffStore = handoffs
	if _, err := handoffs.Save(handoff.Entry{ID: "older-handoff", Title: "Follow up on the draft", Status: handoff.StatusBlocked, Source: handoff.SourceTriage, SourceID: "older-0", AppName: "Notes", CreatedAt: now.AddDate(0, 0, -14).UnixMilli(), UpdatedAt: now.AddDate(0, 0, -14).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.resolveTodayChatItem(t.Context(), TodayChatContextRef{SourceKind: "history", SourceID: "older-handoff"}, todayChatSources{}, now); err != nil {
		t.Fatalf("old handoff could not open in chat: %v", err)
	}

	app.closedEyes.enabled.Store(true)
	hidden := mobileBriefInput{AsOf: now}
	collectMobileBriefOlderThreads(t.Context(), app, &hidden)
	if len(hidden.OpenLoops) != 0 {
		t.Fatal("closed eyes admitted old outcome")
	}
	app.closedEyes.enabled.Store(false)
	pii.AddUserSensitiveApp("com.apple.Notes", "Notes")
	hidden = mobileBriefInput{AsOf: now}
	collectMobileBriefOlderThreads(t.Context(), app, &hidden)
	if len(hidden.OpenLoops) != 0 {
		t.Fatal("sensitive old source revived")
	}
}

func TestBriefConversationWaitingRepliesRecheckHeadersAndConsent(t *testing.T) {
	isolateActivityPrivacyTest(t)
	app := newTodayContextTestApp(t, true)
	now := time.Now()
	if err := app.store.SetUIState(t.Context(), permissionUIStateKey(mailPermissionKey), "true"); err != nil {
		t.Fatal(err)
	}
	moment := proactiveVoiceTestMail(t, app, now)
	row, err := app.store.CreateEngagementMoment(t.Context(), storage.EngagementMomentRow{ID: "reply-moment", Kind: string(engagement.KindWaitingOnReply), SubjectID: moment.SubjectID, DedupeKey: moment.DedupeKey, State: storage.EngagementStatePending, Params: momentParamsJSON(moment), CreatedAt: now.AddDate(0, 0, -2).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	in := mobileBriefInput{AsOf: now, Slot: "evening"}
	collectMobileBriefWaitingReplies(t.Context(), app, &in)
	if len(in.OpenLoops) != 1 || in.OpenLoops[0].SourceID != row.ID || in.OpenLoops[0].Title != "Dana Ruiz" {
		t.Fatalf("waiting source: %+v", in.OpenLoops)
	}
	digest := in.OpenLoops[0].rankedSourceDigest
	if !app.morningBriefWaitingSourceReadable(t.Context(), row.ID, digest, now) {
		t.Fatal("fresh waiting source rejected")
	}
	app.closedEyes.enabled.Store(true)
	pausedCtx := context.WithValue(t.Context(), pausedBriefReadKey{}, true)
	if !app.morningBriefWaitingSourceReadable(pausedCtx, row.ID, digest, now) {
		t.Fatal("selected brief could not verify waiting headers while paused")
	}
	if app.morningBriefWaitingSourceReadable(t.Context(), row.ID, digest, now) {
		t.Fatal("ordinary paused read exposed waiting headers")
	}
	app.closedEyes.enabled.Store(false)
	selected, err := app.resolveTodayChatItem(t.Context(), TodayChatContextRef{SourceKind: mobileBriefWaitingReplySource, SourceID: row.ID}, todayChatSources{}, now)
	if err != nil || !strings.Contains(selected.Record.Text, "Dana Ruiz") || strings.Contains(selected.Record.Text, "Project review") {
		t.Fatalf("waiting item chat context: %+v %v", selected, err)
	}
	raw, _ := json.Marshal(in.OpenLoops)
	if strings.Contains(string(raw), "Project review") || strings.Contains(string(raw), "example.com") {
		t.Fatal("mail payload escaped holdback")
	}
	if _, err := app.store.UpsertMailMessages(t.Context(), []storage.MailMessageRow{{ID: "mail-local-only", Mailbox: storage.MailboxInbox, SenderName: "Dana Ruiz", SenderEmail: "dana@example.com", ToMe: true, Replied: true, ThreadKey: "thread-local-only", At: now.Add(-48 * time.Hour).UnixMilli(), PrincipalID: moment.SubjectID}}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if app.morningBriefWaitingSourceReadable(t.Context(), row.ID, digest, now) {
		t.Fatal("replied thread retained its scoring source")
	}
	app.closedEyes.enabled.Store(true)
	if app.morningBriefWaitingSourceReadable(pausedCtx, row.ID, digest, now) {
		t.Fatal("replied thread survived the paused brief proof")
	}
	app.closedEyes.enabled.Store(false)
	if err := app.store.SetUIState(t.Context(), permissionUIStateKey(mailPermissionKey), "false"); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.mobileBriefWaitingReply(t.Context(), row.ID, now); ok {
		t.Fatal("revoked consent admitted mail")
	}
	app.closedEyes.enabled.Store(true)
	if _, ok := app.mobileBriefWaitingReply(pausedCtx, row.ID, now); ok {
		t.Fatal("revoked consent admitted mail into paused brief proof")
	}
	app.closedEyes.enabled.Store(false)
}

func TestTodayChatWholeBriefSendAndFinalWithdrawal(t *testing.T) {
	for _, withdraw := range []bool{false, true} {
		t.Run(fmt.Sprint(withdraw), func(t *testing.T) {
			app, tag, _ := todaySendTestApp(t)
			now := time.Now()
			brief := buildMobileBriefV1(mobileBriefInput{Slot: "morning", AsOf: now.Add(-time.Minute), OpenLoops: []mobileBriefCandidate{{SourceKind: "memory", SourceID: "launch", Title: "Launch plan", Kind: "goal", Status: "active"}}})
			brief.Narrative = mobileBriefNarrativeTemplate(brief, "en")
			_, brief.Offers = mobileBriefClosing(brief, "en")
			dir, _ := mobileBriefStateDir()
			bindConversationBriefFixture(t, app, &brief)
			if err := newMobileBriefStore(dir).save(brief, now); err != nil {
				t.Fatal(err)
			}
			safe := app.mobileBriefForDelivery(brief)
			ref := TodayChatContextRef{SourceKind: "brief", SourceID: mobileBriefConversationKey(safe), BriefID: brief.BriefID}
			draft, err := app.PrepareTodayChatDraft(ref)
			if err != nil {
				t.Fatal(err)
			}
			if draft.Prefill.Kind != "brief" {
				t.Fatal("brief source lost its opener kind")
			}
			ref.GeneratedTitle = draft.Prefill.Title
			ref.GeneratedFingerprint = draft.Fingerprint
			const visible = "Let's go through today's brief. Start with the launch."
			if got, err := app.PrepareTodayChatTurn(tag, visible, ref); err != nil || got != visible {
				t.Fatalf("editable opener changed: %q %v", got, err)
			}
			if withdraw {
				if err := app.DeleteMemoryEntry("launch"); err != nil {
					t.Fatal(err)
				}
			}
			runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 1)}
			app.runner = runner
			app.tagCancels = map[string]context.CancelFunc{}
			app.sessionTempDirs = map[string]string{}
			app.streams = map[string]context.CancelFunc{}
			app.runIDByTag = map[string]string{}
			app.pendingTraceIDs = map[string]string{}
			_, err = app.SendPromptWithTodayContext(visible, "", "m", "", tag, nil, nil, false, false, "en", "", StreamItemData{ID: "brief-user", Kind: "user"}, ref)
			if withdraw {
				if err == nil {
					t.Fatal("withdrawn whole brief admitted")
				}
				select {
				case <-runner.inputs:
					t.Fatal("withdrawn context reached runner")
				default:
				}
				page, err := app.store.LoadStreamItemsLatest(t.Context(), tag, 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range page.Items {
					if row.ID == "brief-user" {
						t.Fatal("rejected user draft was persisted")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case input := <-runner.inputs:
				prompt, _ := input.PromptFunc()
				if strings.Count(prompt, "<today-item-context>") != 1 || !strings.Contains(prompt, pii.RedactCodes(brief.BriefID)) || !strings.Contains(prompt, "release review is on Friday") || !strings.Contains(prompt, "narrative") || !strings.Contains(prompt, "evidence") || !strings.HasSuffix(prompt, visible) {
					t.Fatalf("whole brief did not reach existing send path: %s", prompt)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("main chat did not receive brief context")
			}
			waitForTagIdle(t, app, tag)
		})
	}
}

func TestBriefConversationOffersRedactOutcomeBeforeTruncating(t *testing.T) {
	brief := conversationFixtureBrief(t, conversationFixtures(t)[0], "en")
	for i := range brief.Sections.OpenLoops {
		if brief.Sections.OpenLoops[i].Outcome != nil {
			brief.Sections.OpenLoops[i].Title = "Verification code 837462. " + strings.Repeat("public ", 40)
		}
	}
	brief.Sections = mobileBriefChiefSections(brief)
	raw, _ := json.Marshal(mobileBriefConversationOffers(brief, "en"))
	if strings.Contains(string(raw), "837462") {
		t.Fatal("outcome secret escaped through closing offers")
	}
}

func TestTodayChatWholeBriefBoundsBeforeInjection(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	writeTodayContextMemory(t, "oversize", "# Public plan\n\n- **Source**: user_edit\n\nPublic plan.")
	now := time.Now()
	brief := buildMobileBriefV1(mobileBriefInput{Slot: "morning", AsOf: now.Add(-time.Minute), OpenLoops: []mobileBriefCandidate{{SourceKind: "memory", SourceID: "oversize", Title: "Public plan", Kind: "goal", Status: "active"}}})
	if len(brief.Evidence) != 1 {
		t.Fatal("missing source fixture")
	}
	brief.Narrative = []MobileBriefPara{{Text: strings.Repeat("Public plan. ", todayChatBriefTokens), Refs: []string{brief.Evidence[0].Ref}}}
	dir, _ := mobileBriefStateDir()
	bindConversationBriefFixture(t, app, &brief)
	if err := newMobileBriefStore(dir).save(brief, now); err != nil {
		t.Fatal(err)
	}
	safe := app.mobileBriefForDelivery(brief)
	ref := TodayChatContextRef{SourceKind: "brief", SourceID: mobileBriefConversationKey(safe), BriefID: brief.BriefID}
	if _, err := app.resolveTodayChatWholeBrief(t.Context(), ref, now); err == nil {
		t.Fatal("oversized stored snapshot admitted instead of failing closed")
	}
}

func TestBriefConversationThreeDailyExamples(t *testing.T) {
	for _, lang := range []string{"en", "zh-CN"} {
		t.Run(lang, func(t *testing.T) {
			var history []MobileBriefV1
			for index, fixture := range conversationFixtures(t) {
				in := conversationFixtureInput(t, fixture, lang)
				in.PreviousBriefs = history
				brief := buildMobileBriefV1(in)
				brief.Narrative = mobileBriefNarrativeTemplate(brief, lang)
				_, brief.Offers = mobileBriefClosing(brief, lang)
				if err := validateMobileBriefClosing(brief, brief.Narrative, lang); err != nil {
					t.Fatal(err)
				}
				if err := validateMobileBriefNarrativeVoice(brief, brief.Narrative, lang); err != nil {
					t.Fatal(err)
				}
				var text []string
				for _, paragraph := range brief.Narrative {
					text = append(text, paragraph.Text)
				}
				rendered := strings.Join(text, "\n\n")
				if index > 0 && strings.Contains(rendered, "Dana") {
					t.Fatal("previously raised plan returned within two briefs")
				}
				t.Logf("Daily example %s (%s):\n%s", fixture.Date, lang, rendered)
				history = append(history, brief)
			}
		})
	}
}

func TestBriefConversationSettledDecisionOffer(t *testing.T) {
	for _, lang := range []string{"en", "zh-CN"} {
		brief := conversationFixtureBrief(t, conversationFixtures(t)[1], lang)
		// The observed thread; a thread in an AI tool is listed under What
		// you worked on (mobile_brief_anchor.go). Here it settled in a document.
		var item MobileBriefItem
		for _, candidate := range append(append([]MobileBriefItem{}, brief.Sections.OpenLoops...), brief.Sections.Moved...) {
			if candidate.Kind == mobileBriefThreadOutcomeSource {
				item = candidate
				break
			}
		}
		item.Outcome = &ActivityThreadOutcome{Kind: "document", State: "decision", Summary: item.Title}
		item.Status = "done"
		brief.Sections = MobileBriefSections{Moved: []MobileBriefItem{item}}
		offers := mobileBriefConversationOffers(brief, lang)
		want := "Add the decision to my plan"
		if lang == "zh-CN" {
			want = "把决定记入计划"
		}
		// The offer carries the settled decision forward; it never asks the
		// user to decide again.
		if len(offers) != 1 || offers[0].Label != want || offers[0].Kind != "note_decision" || offers[0].Action != mobileBriefOfferSend ||
			strings.Contains(strings.ToLower(offers[0].Text), "decide") || strings.Contains(offers[0].Text, "决定吗") {
			t.Fatalf("settled decision was re-opened (%s): %+v", lang, offers)
		}
	}
}
