package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"wails-gui/internal/platform/activitymon"
)

func TestActivityEvidencePathMatching(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"~/sample-agent", "~/sample-agent/records", false},
		{"~/sample-agent/records/", "~/sample-agent", false},
		{"~/.sample-agent/records/", "~/.sample-agent/records", true},
		{"~/sample-agent", "~/sample-agent-backup", false},
		{"/tmp/project", "/tmp/other", false},
		{"", "~/sample-agent", false},
	} {
		if got := activityPathMatches(tc.a, tc.b); got != tc.want {
			t.Errorf("match(%q, %q)=%v", tc.a, tc.b, got)
		}
	}
}

func TestActivityEvidenceFigureMatching(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"60.3 GB", "60gb", true},
		{"60 GB", "60000 MB", true},
		{"1 GiB", "1024 MiB", true},
		{"60 GB", "6 GB", false},
		{"60 GB", "60 ms", false},
		{"60 GB", "60", false},
		{"0 GB", "1 GB", false},
	} {
		if got := activityFigureMatches(tc.a, tc.b); got != tc.want {
			t.Errorf("match(%q, %q)=%v", tc.a, tc.b, got)
		}
	}
}

func TestActivityEvidenceDampsCommonTokensAndExpiresMatchedEvidence(t *testing.T) {
	const now int64 = 2 * activityStrongLinkMs
	token := ActivityEvidenceToken{Kind: "repo", Value: "sample/tools", LastSeenMs: now - 1000}
	buckets := make([]ActivityRow, 5)
	for i := range buckets {
		buckets[i] = ActivityRow{ID: string(rune('a' + i)), EvidenceTokens: []ActivityEvidenceToken{token, token}}
	}
	common := commonActivityEvidence(buckets, now)
	current := []ActivityEvidenceToken{{Kind: token.Kind, Value: token.Value, LastSeenMs: now}}
	previous := []ActivityEvidenceToken{token}
	strongScore, strong := activityEvidenceScore(current, previous, nil, now)
	weakScore, weakStrong := activityEvidenceScore(current, previous, common, now)
	if !strong || weakStrong || weakScore >= strongScore || weakScore == 0 {
		t.Fatalf("strong=%d/%v common=%d/%v", strongScore, strong, weakScore, weakStrong)
	}
	previous[0].LastSeenMs = now - activityWeakLinkMs - 1
	if score, _ := activityEvidenceScore(current, previous, common, now); score != 0 {
		t.Fatalf("common token survived weak lease: %d", score)
	}
	if score, strong := activityEvidenceScore(current, previous, nil, now); score == 0 || !strong {
		t.Fatalf("specific token lost strong lease: %d/%v", score, strong)
	}
	previous[0].LastSeenMs = now - activityStrongLinkMs - 1
	if score, _ := activityEvidenceScore(current, previous, nil, now); score != 0 {
		t.Fatalf("stale token survived: %d", score)
	}
	// A later unrelated run in a bucket must not renew this token's lease.
	id := activityRunIdentity{Tokens: previous}
	if score := activityAssignmentScore(activityRunIdentity{Tokens: current}, id, ActivityTimelineRunRow{StartMs: now}, ActivityRow{EndMs: now}); score != 0 {
		t.Fatalf("bucket freshness renewed stale evidence: %d", score)
	}
	if common := commonActivityEvidence(buckets[:1], now); len(common) != 0 {
		t.Fatalf("repeated mentions in one run are not common: %v", common)
	}
}

func TestMovementSyntheticTerminalChatComposesOneActivity(t *testing.T) {
	isolateActivityPrivacyTest(t)
	raw, err := os.ReadFile("testdata/movement/terminal-chat.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Runs []struct {
			App, Bundle, Text, Title string
			StartMs, EndMs           int64
			Sent                     bool
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	store := newTimelineTestStore(t)
	tr := &ActivityTriager{app: &App{store: newIdentityTestStore(t), activity: &activityMonitorState{store: store}}}
	var firstID string
	for i, input := range fixture.Runs {
		sessionID := input.App + "-synthetic"
		events := []activitymon.ActivityEvent{
			{Kind: activitymon.EventKeyboard, Timestamp: input.StartMs, AppBundleID: input.Bundle, AppName: input.App, TypedText: input.Text},
			{Kind: activitymon.EventKeyboard, Timestamp: input.EndMs, AppBundleID: input.Bundle, AppName: input.App, TypedText: input.Text},
		}
		if input.Sent {
			events[1] = activitymon.ActivityEvent{Kind: activitymon.EventClick, Timestamp: input.EndMs, AppBundleID: input.Bundle, AppName: input.App, ClickLabel: "Send"}
		}
		saveTimelineTestSession(t, store, ActivitySessionRow{ID: sessionID, StartTime: input.StartMs, EndTime: input.EndMs, AppNames: composeJSON([]string{input.App})}, events)
		runs, err := store.GetTimelineRuns(1, 0, input.StartMs, input.EndMs)
		if err != nil || len(runs) != 1 {
			t.Fatalf("derived run: %v / %v", runs, err)
		}
		run := runs[0]
		if ok, err := store.RefreshTimelineRunEvidence(&run); err != nil || !ok {
			t.Fatalf("refresh evidence: %v %v", ok, err)
		}
		if len(run.EvidenceTokens) < 2 {
			t.Fatalf("lost path/figure before summary: %+v", run.EvidenceTokens)
		}
		summary := ActivityTimelineGroupSummary{Title: input.Title}
		resp := composerResponse{Route: "new", Bucket: composerBucketUpdate{Title: "Investigate sample storage usage", Status: activityStatusActive, Category: "sustain"}}
		if i == 0 {
			firstID = "act_" + run.ID
		} else {
			candidates, err := tr.activityAssignmentCandidates(store, run, summary)
			if err != nil || len(candidates) != 1 || candidates[0].ID != firstID {
				t.Fatalf("restatement candidates=%+v err=%v", candidates, err)
			}
			if score := activityAssignmentScore(identityForTimelineRun(run, summary), identityForActivity(candidates[0]), run, candidates[0]); score < 150 {
				t.Fatalf("shared path/figure score=%d", score)
			}
			transition := tr.composeTransitionContext(store, run)
			prompt := buildComposerPrompt(candidates, run, summary, transition, "")
			if len(transition.Restatements) != 1 || !strings.Contains(prompt, "restatement from run") || !strings.Contains(prompt, "sample-agent/records") {
				t.Fatalf("missing restatement prompt: %s", prompt)
			}
			// Exercise the existing model-output seam directly. No model call or
			// integration mock: selection and admission were checked above.
			resp.Route = "join"
			resp.JoinBucketID = candidates[0].ID
			resp.Confidence = "high"
			resp.Admission = []composerAdmission{{BucketID: firstID, Decision: "join", SubjectMatch: "strong", ObjectiveMatch: "medium", StateContinuity: "strong"}}
			resp = tr.guardComposerAssignment(resp, candidates)
		}
		if changed, err := tr.composeApply(store, run, summary, resp, input.EndMs); err != nil || !changed {
			t.Fatalf("compose apply: %v %v", changed, err)
		}
	}
	activities, err := store.GetActivitiesInRange(0, 0)
	if err != nil || len(activities) != 1 || len(composeParseStrings(activities[0].RunIDs)) != 2 {
		t.Fatalf("want one activity with both apps: %+v err=%v", activities, err)
	}
	outcome, err := store.GetLatestActivityOutcome(firstID)
	if err != nil || outcome == nil || outcome.Status != activityOutcomeReported || outcome.Target != "Claude" {
		t.Fatalf("synthetic trace final outcome=%+v err=%v", outcome, err)
	}
	if activities[0].Status == activityStatusSettled {
		t.Fatalf("reported handoff closed task: %+v", activities[0])
	}
}

func TestActivityCandidatesCapIncludesNewestZeroScore(t *testing.T) {
	store := newTimelineTestStore(t)
	tr := &ActivityTriager{}
	const now int64 = 10_000_000
	for i := 0; i < 8; i++ {
		end := now - int64(10-i)*60_000
		run := testRun(string(rune('a'+i)), "Terminal", end-10_000, end, 10_000, 2)
		summary := ActivityTimelineGroupSummary{Title: "Review sample widget", Entities: []string{"sample widget"}}
		if i == 7 {
			summary = ActivityTimelineGroupSummary{Title: "Eat lunch"}
		}
		insertComposableTimelineRun(t, store, run, summary)
		if _, err := tr.composeApplySegment(store, nil, run, summary, end); err != nil {
			t.Fatal(err)
		}
	}
	current := testRun("new", "Editor", now, now+10_000, 10_000, 2)
	candidates, err := tr.activityAssignmentCandidates(store, current, ActivityTimelineGroupSummary{Title: "Review sample widget", Entities: []string{"sample widget"}})
	if err != nil || len(candidates) != 6 || !hasActivityCandidate(candidates, "act_h") {
		t.Fatalf("candidates cap/newest=%+v err=%v", candidates, err)
	}
}

func TestActivityEvidenceHomeRootsAndRestatementLease(t *testing.T) {
	const now int64 = 2 * activityStrongLinkMs
	for _, value := range []string{"~", "/Users/sample", "/home/sample"} {
		if _, strong := activityEvidenceWeight(ActivityEvidenceToken{Kind: "path", Value: value}, nil); strong {
			t.Errorf("home root %q is strong", value)
		}
	}
	current := []ActivityEvidenceToken{{Kind: "path", Value: "~/sample/records", LastSeenMs: now}}
	stale := []ActivityEvidenceToken{{Kind: "path", Value: "~/sample/records", LastSeenMs: now - activityWeakLinkMs - 1}}
	if hints := matchedActivityEvidence(current, stale, now); len(hints) != 0 {
		t.Fatalf("old evidence claimed recent restatement: %+v", hints)
	}
	stale[0].LastSeenMs = now - activityStrongLinkMs - 1
	if got := eligibleActivityPromptEvidence(stale, current, nil, now); len(got) != 0 {
		t.Fatalf("expired evidence entered prompt: %+v", got)
	}
	common := map[string]bool{activityEvidenceKey(current[0]): true}
	stale[0].LastSeenMs = now - activityWeakLinkMs - 1
	if got := eligibleActivityPromptEvidence(stale, current, common, now); len(got) != 0 {
		t.Fatalf("expired common evidence entered prompt: %+v", got)
	}
	other := make([]ActivityEvidenceToken, 20)
	for i := range other {
		other[i] = ActivityEvidenceToken{Kind: "path", Value: "/tmp/" + string(rune('a'+i)), LastSeenMs: now}
	}
	ordered := orderActivityPromptEvidence(append(other, current...), current)
	if !strings.Contains(composerEvidenceTokens(ordered), "~/sample/records") {
		t.Fatal("prompt cap lost matching token")
	}
}

func TestActivityCandidatesExcludePrivateAndMissingProvenance(t *testing.T) {
	store := newActivityEvidenceTestStore(t)
	tr := &ActivityTriager{}
	for i, tc := range []struct {
		id, app  string
		excluded bool
	}{
		{"protected", "1Password", false}, {"private", "Chrome", true}, {"missing", "Editor", false},
	} {
		member := testRun(tc.id, tc.app, 1000, 2000, 1000, 2)
		member.FirstEventRowID = int64(i + 1)
		member.EvidenceExcluded = tc.excluded
		insertComposableTimelineRun(t, store, member, ActivityTimelineGroupSummary{Title: "Inspect sample path"})
		if _, err := tr.composeApply(store, member, ActivityTimelineGroupSummary{}, composerResponse{Route: "new", Bucket: composerBucketUpdate{Title: "Inspect sample path"}}, 2000); err != nil {
			t.Fatal(err)
		}
		if tc.id == "missing" {
			if _, err := store.db.Exec(`DELETE FROM activity_timeline_groups WHERE id = ?`, tc.id); err != nil {
				t.Fatal(err)
			}
		}
	}
	incoming := testRun("current", "Editor", 3000, 4000, 1000, 2)
	candidates, err := tr.activityAssignmentCandidates(store, incoming, ActivityTimelineGroupSummary{Title: "Inspect sample path"})
	if err != nil || len(candidates) != 0 {
		t.Fatalf("excluded candidate fallback=%+v err=%v", candidates, err)
	}
}

func TestActivityCandidatesStopDuringPrivacyPause(t *testing.T) {
	app := &App{}
	app.closedEyes.enabled.Store(true)
	tr := &ActivityTriager{app: app}
	if candidates, err := tr.activityAssignmentCandidates(nil, ActivityTimelineRunRow{}, ActivityTimelineGroupSummary{}); err != nil || len(candidates) != 0 {
		t.Fatalf("paused candidates=%+v err=%v", candidates, err)
	}
}

func TestActivityEvidenceSpecificPathSurvivesAlongsideHomeRoot(t *testing.T) {
	const now int64 = 2 * activityStrongLinkMs
	current := []ActivityEvidenceToken{{Kind: "path", Value: "/Users/sample", LastSeenMs: now}, {Kind: "path", Value: "/Users/sample/project/src/file.go", LastSeenMs: now}}
	prior := []ActivityEvidenceToken{{Kind: "path", Value: "/Users/sample/project/src/file.go", LastSeenMs: now - 2*activityWeakLinkMs}}
	eligible := eligibleActivityPromptEvidence(prior, current, nil, now)
	if len(eligible) != 1 {
		t.Fatalf("specific match lost to generic prefix: %+v", eligible)
	}
	if score, strong := activityEvidenceScore(current, eligible, nil, now); score == 0 || !strong {
		t.Fatalf("strong resumption=%d/%v", score, strong)
	}
}

func TestActivityCandidatesResumePausedWithStrongEvidenceOnly(t *testing.T) {
	store := newActivityEvidenceTestStore(t)
	tr := &ActivityTriager{}
	const now int64 = 2 * activityStrongLinkMs
	end := now - 7*60*60*1000
	member := testRun("paused-source", "iTerm2", end-60_000, end, 60_000, 2)
	member.EvidenceTokens = []ActivityEvidenceToken{{Kind: "path", Value: "~/.sample/records", LastSeenMs: end}}
	insertComposableTimelineRun(t, store, member, ActivityTimelineGroupSummary{Title: "Inspect sample records"})
	if _, err := tr.composeApply(store, member, ActivityTimelineGroupSummary{}, composerResponse{Route: "new", Bucket: composerBucketUpdate{Title: "Inspect sample records"}}, end); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleActivities(now); err != nil {
		t.Fatal(err)
	}
	current := testRun("resume", "Claude", now, now+60_000, 60_000, 2)
	current.EvidenceTokens = []ActivityEvidenceToken{{Kind: "path", Value: "~/.sample/records", LastSeenMs: now}}
	candidates, err := tr.activityAssignmentCandidates(store, current, ActivityTimelineGroupSummary{})
	if err != nil || len(candidates) != 1 || candidates[0].Status != activityStatusPaused {
		t.Fatalf("strong paused resumption=%+v err=%v", candidates, err)
	}
	current.EvidenceTokens = nil
	candidates, err = tr.activityAssignmentCandidates(store, current, ActivityTimelineGroupSummary{Title: "Inspect sample records"})
	if err != nil || len(candidates) != 0 {
		t.Fatalf("weak summary resumed paused task=%+v err=%v", candidates, err)
	}
}
