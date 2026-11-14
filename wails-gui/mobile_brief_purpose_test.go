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
	"wails-gui/storage"
)

func newMobileBriefPurposeTestApp(t *testing.T) *App {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "pb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	activity, err := NewActivityStore(filepath.Join(home, "activity"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := storage.New(filepath.Join(home, "state.db"))
	if err != nil {
		activity.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close(); activity.Close() })
	return &App{activity: &activityMonitorState{store: activity}, store: state}
}

func mobileBriefPurposeFixture(id string, now time.Time) PurposeOpenLoopDetail {
	return PurposeOpenLoopDetail{OpenLoop: PurposeOpenLoopRecord{ID: id, Kind: PurposeKindGoal, CreatedAt: now.AddDate(0, 0, -30).UnixMilli()},
		Current: PurposeOpenLoopEventRecord{ID: id + "-event", Title: "Purpose " + id, DesiredOutcome: "An intended result", Owner: PurposeOwnerUser,
			LifecycleState: PurposeStateActive, SupportState: PurposeSupportSupported, OccurredAt: now.Add(-time.Hour).UnixMilli()}, Eligible: true,
		Evidence: []PurposeEvidenceRecord{{SourceKind: "manual", SourceID: id, EvidenceClass: PurposeEvidenceExplicitUser, PrivacyDecision: TaskContextPrivacyInclude, SourceObservedAt: now.AddDate(0, 0, -30).UnixMilli()}}}
}

func seedMobileBriefPurposeRun(t *testing.T, app *App, id string, rowID int64, at time.Time, activeMs int64, apps []string) string {
	t.Helper()
	store := app.activity.store
	raw, _ := json.Marshal(apps)
	sessionID := "s-" + id
	if err := store.SaveSession(ActivitySessionRow{ID: sessionID, StartTime: at.Add(-time.Minute).UnixMilli(), EndTime: at.UnixMilli(), AppNames: string(raw), TriageStatus: "done", CreatedAt: at.UnixMilli()}, nil, nil); err != nil {
		t.Fatal(err)
	}
	insertTestRun(t, store, id, rowID, at.Add(-time.Minute).UnixMilli(), "done", 1)
	sessionJSON, _ := json.Marshal([]string{sessionID})
	if _, err := store.db.Exec(`UPDATE activity_timeline_groups SET session_ids=?,app_name=?,end_ms=?,active_ms=? WHERE id=?`, string(sessionJSON), apps[0], at.UnixMilli(), activeMs, id); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTimelineGroupSummary(ActivityTimelineGroup{ID: id}, ActivityTimelineGroupSummary{GroupID: id, Title: "Reviewed " + id, Description: "Compared the release options"}, "test", "", ""); err != nil {
		t.Fatal(err)
	}
	return sessionID
}

func linkMobileBriefPurposeSession(detail *PurposeOpenLoopDetail, sessionID string, at time.Time) {
	detail.Evidence = append(detail.Evidence, PurposeEvidenceRecord{SourceKind: "activity_session", SourceID: sessionID, SourceObservedAt: at.UnixMilli(), EvidenceClass: PurposeEvidenceObservedUser, PrivacyDecision: TaskContextPrivacyRedact})
}

func TestMobileBriefPurposePrivacyClosureAndSummaryOnly(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	pii.SetUserSensitiveApps(map[string]string{"secret.app": "Secret Notes"})
	t.Cleanup(func() { pii.SetUserSensitiveApps(map[string]string{}) })
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	safe := seedMobileBriefPurposeRun(t, app, "safe", 1, now.Add(-time.Hour), 1000, []string{"Editor"})
	secret := seedMobileBriefPurposeRun(t, app, "secret", 2, now.Add(-2*time.Hour), 9000, []string{"Editor", "Secret Notes"})
	protected := seedMobileBriefPurposeRun(t, app, "protected", 3, now.Add(-3*time.Hour), 9000, []string{"Editor"})
	if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id=?`, protected); err != nil {
		t.Fatal(err)
	}
	memory := "# Release memory\n\n- **Source**: activity\n- **Activity Session**: " + safe + "\n- **Apps**: Editor\n\nRAW_MEMORY_BODY_MUST_NOT_ENTER_BRIEF\n"
	if _, err := app.SaveMemoryEntry("release", memory); err != nil {
		t.Fatal(err)
	}
	clean := mobileBriefPurposeFixture("clean", now)
	clean.Evidence = append(clean.Evidence, PurposeEvidenceRecord{SourceKind: taskContextSourceMemoryKind, SourceID: "release:live", PrivacyDecision: TaskContextPrivacyRedact, EvidenceClass: PurposeEvidenceEvaluatedProjection})
	mixed := mobileBriefPurposeFixture("SECRET_PURPOSE", now)
	linkMobileBriefPurposeSession(&mixed, secret, now.Add(-2*time.Hour))
	blocked := mobileBriefPurposeFixture("PROTECTED_PURPOSE", now)
	linkMobileBriefPurposeSession(&blocked, protected, now.Add(-3*time.Hour))
	unknown := mobileBriefPurposeFixture("UNKNOWN_DERIVED", now)
	unknown.Evidence[0].EvidenceClass = PurposeEvidenceEvaluatedProjection
	missing := mobileBriefPurposeFixture("MISSING_ACTIVITY", now)
	missing.Evidence[0].SourceKind = "activity"
	missing.Evidence[0].SourceID = "missing"
	in := mobileBriefInput{AsOf: now, Location: time.UTC}
	collectMobileBriefPurposes(app, &in, []PurposeOpenLoopDetail{clean, mixed, blocked, unknown, missing}, nil)
	brief := buildMobileBriefV1(in)
	if len(brief.Sections.WorkingToward) != 1 || len(brief.Sections.OpenLoops) != 1 || brief.Limits.ExcludedSensitiveItems != 2 {
		t.Fatalf("privacy result = %+v, excluded=%d", brief.Sections, brief.Limits.ExcludedSensitiveItems)
	}
	raw, _ := json.Marshal(brief)
	for _, forbidden := range []string{"SECRET_PURPOSE", "PROTECTED_PURPOSE", "UNKNOWN_DERIVED", "MISSING_ACTIVITY", "RAW_MEMORY_BODY", "Secret Notes"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("brief leaked %s", forbidden)
		}
	}
	if !strings.Contains(brief.Sections.WorkingToward[0].Detail, "Compared the release options") {
		t.Fatal("memory provenance did not resolve the linked session summary")
	}
}

func TestMobileBriefPurposePeopleUseObservedTouchAndUserOwnership(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	var details []PurposeOpenLoopDetail
	for i := 0; i < 5; i++ {
		d := mobileBriefPurposeFixture(fmt.Sprintf("person-%d", i), now)
		d.Current.Counterparty = fmt.Sprintf("Person %d", i)
		details = append(details, d)
	}
	// A ledger rewrite today is not a touch; a real linked session is.
	details[0].Current.OccurredAt = now.UnixMilli()
	recent := seedMobileBriefPurposeRun(t, app, "recent", 1, now.Add(-time.Hour), 1000, []string{"Editor"})
	shared := mobileBriefPurposeFixture("same-person-other-purpose", now)
	shared.Current.Counterparty = "Person 0"
	shared.Current.LifecycleState = PurposeStateResolved
	shared.Eligible = false
	linkMobileBriefPurposeSession(&shared, recent, now.Add(-time.Hour))
	details = append(details, shared)
	other := mobileBriefPurposeFixture("other-owned", now)
	other.Current.Owner = PurposeOwnerOther
	other.Current.Counterparty = "Not waiting on user"
	details = append(details, other)
	in := mobileBriefInput{AsOf: now, Location: time.UTC}
	collectMobileBriefPurposes(app, &in, details, nil)
	brief := buildMobileBriefV1(in)
	// The four primary open loops also cap their people aliases. Other open
	// goals can occupy those slots without asserting that someone awaits us.
	if len(brief.Sections.WaitingOnYou) != 2 {
		t.Fatalf("waiting = %+v", brief.Sections.WaitingOnYou)
	}
	for _, person := range brief.Sections.WaitingOnYou {
		if person.Title == "Person 0" || person.Title == "Not waiting on user" || person.LastTouch != "" || !strings.Contains(person.Detail, "No recorded touch") {
			t.Fatalf("unsupported wait = %+v", person)
		}
	}
}

func TestMobileBriefPurposeCommitmentReferencesExistingChatWithoutWriting(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	detail := mobileBriefPurposeFixture("owed", now)
	detail.OpenLoop.Kind = PurposeKindCommitment
	detail.Current.DueKind = PurposeDueDate
	detail.Current.DueValue = "2026-10-16"
	moment, ok := engagementCommitmentSlippingMoment(detail, now)
	if !ok {
		t.Fatal("fixture has no slipping moment")
	}
	moment.ID = "existing-moment"
	row, err := app.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{ID: moment.ID, Kind: string(moment.Kind), SubjectID: moment.SubjectID, DedupeKey: moment.DedupeKey, CreatedAt: now.Add(-time.Hour).UnixMilli(), DueAt: moment.DueAt.UnixMilli(), State: storage.EngagementStatePending})
	if err != nil {
		t.Fatal(err)
	}
	main, err := app.ensureMainChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(mainChatReport{Source: mainChatSourceEngagement, Key: engagementReportKey(momentFromRow(row), now), Status: string(engagement.KindCommitmentSlipping), Title: "RAW_CHAT_TITLE"})
	if _, err := app.store.AppendStreamItem(ctx, main.LocalID, storage.StreamItemRow{ID: "existing-message", Kind: "system", Content: "RAW_CHAT_BODY", ToolName: mainChatReportTool, ToolInput: string(payload), CreatedAt: now.Add(-time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	// Reports remain referencable after normal conversation moves them out
	// of the main chat's recent dedupe window; future reports are not visible.
	for i := 0; i < mainChatReportDedupeWindow+1; i++ {
		if _, err := app.store.AppendStreamItem(ctx, main.LocalID, storage.StreamItemRow{ID: fmt.Sprintf("ordinary-%d", i), Kind: "assistant", Content: "RAW_CHAT_BODY", CreatedAt: now.Add(-time.Minute).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.store.AppendStreamItem(ctx, main.LocalID, storage.StreamItemRow{ID: "future-message", Kind: "system", ToolName: mainChatReportTool, ToolInput: string(payload), CreatedAt: now.Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	in := mobileBriefInput{AsOf: now, Location: time.UTC}
	collectMobileBriefPurposes(app, &in, []PurposeOpenLoopDetail{detail}, nil)
	brief := buildMobileBriefV1(in)
	if len(brief.Sections.Commitments) != 1 {
		t.Fatalf("commitments = %+v", brief.Sections.Commitments)
	}
	item := brief.Sections.Commitments[0]
	if item.MomentID != moment.ID || item.ChatMessageID != "existing-message" || item.ChatSessionID != main.LocalID || item.Status != "due_today" {
		t.Fatalf("reference = %+v", item)
	}
	if brief.Sections.WorkingToward[0].ChatMessageID != "" {
		t.Fatal("progress must not carry the commitment's chat report")
	}
	wire, _ := json.Marshal(brief)
	var oldPhone struct {
		Sections struct {
			OpenLoops []struct{ Title, Detail, Status string }
		}
	}
	if err := json.Unmarshal(wire, &oldPhone); err != nil || len(oldPhone.Sections.OpenLoops) != 1 || oldPhone.Sections.OpenLoops[0].Title != detail.Current.Title || oldPhone.Sections.OpenLoops[0].Detail != detail.Current.DesiredOutcome {
		t.Fatalf("chat-reported commitment disappeared on openLoops-only phone: %+v, %v", oldPhone, err)
	}
	prompt := mobileBriefTestPrompt(brief, "English")
	if strings.Contains(prompt, "openLoops:") || strings.Contains(prompt, "commitments:") {
		t.Fatal("keeping the legacy row must not repeat the reported commitment in the narrative")
	}
	page, err := app.store.LoadStreamItemsLatest(ctx, main.LocalID, 1000)
	if err != nil || len(page.Items) != mainChatReportDedupeWindow+3 {
		t.Fatalf("brief wrote chat: %+v %v", page, err)
	}
	rows, _ := app.store.OpenEngagementMomentsOfKind(ctx, string(engagement.KindCommitmentSlipping))
	if len(rows) != 1 {
		t.Fatal("brief created a duplicate moment")
	}
	raw, _ := json.Marshal(brief)
	if strings.Contains(string(raw), "RAW_CHAT") {
		t.Fatal("brief copied chat content")
	}
}

func TestMobileBriefPurposeOldPhoneCompatibilityAndStrictPrivacy(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	in := mobileBriefGoldenInput()
	base := buildMobileBriefV1(in)
	in.WorkingToward = []mobileBriefCandidate{{SourceKind: "purpose", SourceID: "goal", Title: "Release", Detail: "Compared deployment options", At: now, LastTouch: now, NextStep: "Send the draft"}}
	in.Commitments = []mobileBriefCandidate{{SourceKind: "purpose", SourceID: "due", Title: "Send draft", DueAt: now, MomentID: "moment"}}
	in.WaitingOnYou = []mobileBriefCandidate{{SourceKind: "purpose", SourceID: "waiting", Title: "Dana", Status: "no_recent_touch"}}
	raw, _ := json.Marshal(buildMobileBriefV1(in))
	// This decoder deliberately models the pre-extension phone contract.
	type oldSections struct{ Moved, Doing, Watching, Meetings, People, OpenLoops, Learned, Deliverables, Schedules []MobileBriefItem }
	type oldBrief struct {
		Version  int
		Sections oldSections
	}
	var old oldBrief
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	if old.Version != 1 || len(old.Sections.Moved) != len(base.Sections.Moved) || old.Sections.Moved[0].Title != base.Sections.Moved[0].Title || len(old.Sections.OpenLoops) != len(base.Sections.OpenLoops) {
		t.Fatal("additive fields changed old-phone sections")
	}
	pii.SetUserSensitiveApps(map[string]string{"secret.app": "Secret Notes"})
	t.Cleanup(func() { pii.SetUserSensitiveApps(map[string]string{}) })
	in.WorkingToward[0].Evidence = []mobileBriefCandidate{{SourceKind: "activity", SourceID: "sensitive", Title: "PRIVATE_SUMMARY", Apps: []string{"Editor", "Secret Notes"}}}
	in.Commitments[0].Apps = []string{"Editor", "Secret Notes"}
	brief := buildMobileBriefV1(in)
	if len(brief.Sections.WorkingToward) != 0 || len(brief.Sections.Commitments) != 0 {
		t.Fatal("mixed sensitive derived source was not excluded")
	}
}

func TestMobileBriefPurposeOverdueProgressIsNotCompletion(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	detail := mobileBriefPurposeFixture("overdue", now)
	detail.OpenLoop.Kind = PurposeKindCommitment
	detail.Current.DueKind = PurposeDueDate
	detail.Current.DueValue = "2026-10-13"
	session := seedMobileBriefPurposeRun(t, app, "progress", 1, now.Add(-time.Hour), 1000, []string{"Editor"})
	linkMobileBriefPurposeSession(&detail, session, now.Add(-time.Hour))
	detail.Evidence[1].Stance = PurposeStanceUpdate
	in := mobileBriefInput{AsOf: now, Location: time.UTC}
	collectMobileBriefPurposes(app, &in, []PurposeOpenLoopDetail{detail}, nil)
	brief := buildMobileBriefV1(in)
	if len(brief.Sections.Commitments) != 1 || brief.Sections.Commitments[0].Status != "slipping" || !strings.Contains(brief.Sections.Commitments[0].Detail, "Compared the release options") {
		t.Fatalf("overdue progress = %+v", brief.Sections.Commitments)
	}
	if brief.Sections.Commitments[0].MomentID != "" {
		t.Fatal("brief minted an engagement moment")
	}
}

func TestMobileBriefPurposeEvidenceBudgetKeepsStatementsAtomic(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	builder := &mobileBriefBuilder{asOf: now, index: map[string]string{}}
	for i := 0; i < mobileBriefEvidenceLimit-2; i++ {
		_, _ = builder.refFor(mobileBriefCandidate{SourceKind: "seed", SourceID: fmt.Sprint(i), Title: "Seed"})
	}
	item := mobileBriefCandidate{SourceKind: "purpose", SourceID: "goal", Title: "Release", Detail: "Reviewed changes", Evidence: []mobileBriefCandidate{
		{SourceKind: "activity", SourceID: "activity", Title: "Reviewed the release"},
		{SourceKind: "purpose", SourceID: "next", Title: "Send draft"},
	}}
	items := builder.section(mobileBriefSectionWorkingToward, []mobileBriefCandidate{item})
	if len(items) != 0 || len(builder.evidence) != mobileBriefEvidenceLimit-2 || !containsString(builder.truncated, mobileBriefSectionWorkingToward) {
		t.Fatalf("partial evidence allocation: %+v %+v", items, builder)
	}
}

type mobileBriefPurposeOutcomeFixture []mobileBriefPurposeOutcome

func (rows mobileBriefPurposeOutcomeFixture) BriefPurposeOutcomes(_ []string, _ time.Time) ([]mobileBriefPurposeOutcome, error) {
	return rows, nil
}

func TestMobileBriefPurposeOutcomeSeamKeepsSupportedSummaryAndEvidence(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	session := seedMobileBriefPurposeRun(t, app, "outcome-run", 1, now.Add(-time.Hour), 1000, []string{"Editor"})
	if err := app.activity.store.UpsertActivityRunLink("outcome-run", "activity", 1, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	detail := mobileBriefPurposeFixture("goal", now)
	linkMobileBriefPurposeSession(&detail, session, now.Add(-time.Hour))
	in := mobileBriefInput{AsOf: now, Location: time.UTC}
	collectMobileBriefPurposes(app, &in, []PurposeOpenLoopDetail{detail}, mobileBriefPurposeOutcomeFixture{
		{ID: "future", ActivityID: "activity", Status: "done", Summary: "FUTURE_RESULT", At: now.Add(time.Hour)},
		{ID: "unrelated", ActivityID: "other", Status: "done", Summary: "UNRELATED_RESULT", At: now},
		{ID: "protected", ActivityID: "activity", Status: "done", Summary: "PRIVATE_RESULT", At: now, Protected: true},
		{ID: "outcome", ActivityID: "activity", Status: "reported", Summary: "Reported the\nrelease result", At: now.Add(-time.Minute)},
	})
	brief := buildMobileBriefV1(in)
	item := brief.Sections.WorkingToward[0]
	if item.Detail != "Reported the release result" {
		t.Fatalf("outcome projection = %+v", item)
	}
	found := false
	for _, ref := range brief.Evidence {
		if ref.SourceKind == "activity_outcome" && ref.SourceID == "outcome" && containsString(item.Refs, ref.Ref) {
			found = true
		}
	}
	if !found {
		t.Fatal("outcome summary lacks receipt reference")
	}
	raw, _ := json.Marshal(brief)
	for _, bad := range []string{"FUTURE_RESULT", "UNRELATED_RESULT", "PRIVATE_RESULT"} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("leaked %s", bad)
		}
	}
}

func TestMobileBriefPurposePrivatePausedAndUnreadablePauseSourcesExcluded(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	private := seedMobileBriefPurposeRun(t, app, "private", 1, now.Add(-time.Hour), 1000, []string{"Chrome"})
	if _, err := app.activity.store.db.Exec(`UPDATE activity_timeline_group_summaries SET title='Private research (Incognito)' WHERE group_id='private'`); err != nil {
		t.Fatal(err)
	}
	paused := seedMobileBriefPurposeRun(t, app, "paused", 2, now.Add(-2*time.Hour), 1000, []string{"Editor"})
	app.saveObservationPauses([]observationPause{{Start: now.Add(-2*time.Hour - 30*time.Second).UnixMilli(), End: now.Add(-2*time.Hour - 10*time.Second).UnixMilli()}})
	var details []PurposeOpenLoopDetail
	for i, session := range []string{private, paused} {
		d := mobileBriefPurposeFixture(fmt.Sprintf("private-%d", i), now)
		linkMobileBriefPurposeSession(&d, session, now.Add(-time.Hour))
		details = append(details, d)
	}
	in := mobileBriefInput{AsOf: now, Location: time.UTC}
	collectMobileBriefPurposes(app, &in, details, nil)
	if len(in.WorkingToward) != 0 || len(in.OpenLoops) != 0 {
		t.Fatalf("private or paused source leaked: %+v", in.WorkingToward)
	}
	if err := app.store.SetUIState(context.Background(), observationPausesKey, "broken-json"); err != nil {
		t.Fatal(err)
	}
	in = mobileBriefInput{AsOf: now, Location: time.UTC}
	collectMobileBriefPurposes(app, &in, details, nil)
	if len(in.WorkingToward) != 0 {
		t.Fatal("unreadable pause log allowed a source")
	}
}

func TestMobileBriefPurposeSectionsEndColdStart(t *testing.T) {
	for _, section := range []string{"purpose", "commitment", "person"} {
		t.Run(section, func(t *testing.T) {
			in := mobileBriefInput{ColdStart: true}
			item := mobileBriefCandidate{SourceKind: "purpose", SourceID: "p", Title: "Existing purpose", ChatMessageID: "existing-report"}
			switch section {
			case "purpose":
				in.WorkingToward = []mobileBriefCandidate{item}
			case "commitment":
				in.Commitments = []mobileBriefCandidate{item}
			case "person":
				in.WaitingOnYou = []mobileBriefCandidate{item}
			}
			mobileBriefEndColdStart(&in)
			if in.ColdStart {
				t.Fatal("existing purpose section remained a cold start")
			}
		})
	}
}

// A reminder delivered through the proactive-card producer (alone or inside a
// digest) carries its moment id, not a report key. The brief must still link to
// that conversation and keep the reminder out of the narrative.
func TestMobileBriefPurposeCommitmentReferencesProactiveCards(t *testing.T) {
	for _, digest := range []bool{false, true} {
		name := "single card"
		if digest {
			name = "digest child"
		}
		t.Run(name, func(t *testing.T) {
			app := newMobileBriefPurposeTestApp(t)
			ctx := context.Background()
			now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			detail := mobileBriefPurposeFixture("owed-proactive", now)
			detail.OpenLoop.Kind = PurposeKindCommitment
			detail.Current.DueKind = PurposeDueDate
			detail.Current.DueValue = "2026-10-16"
			moment, ok := engagementCommitmentSlippingMoment(detail, now)
			if !ok {
				t.Fatal("fixture has no slipping moment")
			}
			moment.ID = "proactive-moment"
			if _, err := app.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{ID: moment.ID, Kind: string(moment.Kind), SubjectID: moment.SubjectID, DedupeKey: moment.DedupeKey, CreatedAt: now.Add(-time.Hour).UnixMilli(), DueAt: moment.DueAt.UnixMilli(), State: storage.EngagementStatePending}); err != nil {
				t.Fatal(err)
			}
			main, err := app.ensureMainChat(ctx)
			if err != nil {
				t.Fatal(err)
			}
			card := mainChatProactive{MomentID: moment.ID, Kind: string(moment.Kind), Headline: "RAW_CHAT_TITLE", Body: "RAW_CHAT_BODY", State: "pending"}
			if digest {
				card = mainChatProactive{Kind: "digest", Headline: "RAW_CHAT_TITLE", State: "pending",
					Items: []mainChatProactive{{MomentID: "other-moment", Kind: "meeting_prep"}, {MomentID: moment.ID, Kind: string(moment.Kind), Body: "RAW_CHAT_BODY"}}}
			}
			payload, _ := json.Marshal(card)
			if _, err := app.store.AppendStreamItem(ctx, main.LocalID, storage.StreamItemRow{ID: "proactive-message", Kind: "system", Content: "RAW_CHAT_BODY", ToolName: mainChatProactiveTool, ToolInput: string(payload), CreatedAt: now.Add(-time.Hour).UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			// A card delivered after the brief's time is not visible to it.
			if _, err := app.store.AppendStreamItem(ctx, main.LocalID, storage.StreamItemRow{ID: "future-proactive", Kind: "system", ToolName: mainChatProactiveTool, ToolInput: string(payload), CreatedAt: now.Add(time.Minute).UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			in := mobileBriefInput{AsOf: now, Location: time.UTC}
			collectMobileBriefPurposes(app, &in, []PurposeOpenLoopDetail{detail}, nil)
			brief := buildMobileBriefV1(in)
			if len(brief.Sections.Commitments) != 1 {
				t.Fatalf("commitments = %+v", brief.Sections.Commitments)
			}
			item := brief.Sections.Commitments[0]
			if item.MomentID != moment.ID || item.ChatMessageID != "proactive-message" || item.ChatSessionID != main.LocalID {
				t.Fatalf("the reminder is not linked to its delivered card: %+v", item)
			}
			wire, _ := json.Marshal(brief)
			if strings.Contains(string(wire), "RAW_CHAT_BODY") || strings.Contains(string(wire), "RAW_CHAT_TITLE") {
				t.Fatal("the chat card text reached the brief")
			}
			if prompt := mobileBriefTestPrompt(brief, "English"); strings.Contains(prompt, "commitments:") {
				t.Fatal("a delivered reminder must not be repeated in the narrative")
			}
		})
	}
}

func TestCommitmentShortNameSharedByBriefAndChat(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	detail := mobileBriefPurposeFixture("calf", now)
	detail.OpenLoop.Kind = PurposeKindCommitment
	detail.Current.Title = "Make sure I do some sort of calf race or exercise for AFO like every day"
	detail.Current.DesiredOutcome = detail.Current.Title
	detail.Current.DueKind, detail.Current.DueValue = PurposeDueDate, "2026-10-30"
	moment, ok := engagementCommitmentSlippingMoment(detail, now)
	if !ok {
		t.Fatal("fixture did not yield a commitment moment")
	}
	input := mobileBriefInput{AsOf: now, Location: time.UTC}
	collectMobileBriefPurposes(app, &input, []PurposeOpenLoopDetail{detail}, nil)
	if len(input.Commitments) != 1 {
		t.Fatalf("commitments = %+v", input.Commitments)
	}
	if input.Commitments[0].Title != moment.Params["title"] || input.Commitments[0].EvidenceLabel != detail.Current.Title {
		t.Fatalf("name differs or original evidence lost: %+v, %+v", input.Commitments[0], moment.Params)
	}
	if len(input.OpenLoops) != 1 || input.OpenLoops[0].Detail != moment.Params["title"] {
		t.Fatalf("brief input retained dictated boilerplate: %+v", input.OpenLoops)
	}
}
