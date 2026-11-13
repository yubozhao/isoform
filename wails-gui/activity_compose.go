package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode"

	"yansu-agent/agent"
)

// Activity composer (layer 3): online bucketing of per-app timeline runs into
// activities. Per finalized+summarized run, one LLM call routes the run into an
// existing open bucket or a new one (optionally merging two buckets it reveals
// are one piece of work) AND returns the refreshed bucket summary. Pure code
// then applies the routing, recomputes per-app time accounting incrementally,
// links the cross-day continuation, and persists. The composer consumes run summaries and redacted evidence tokens, never raw
// observations. See .something/new-knowledge/activity-composer-design.md.

const (
	// featureActivityComposer gates the layer-3 composer. Built behind a flag so
	// the legacy per-session semantic-event path keeps working until this is
	// proven (retired in Phase 4).
	featureActivityComposer = true

	activityComposerCheckInterval = 2 * time.Minute
	activityComposerQueueBatch    = 4 * activityComposerBatchSize
	activityComposerTimeout       = 2 * time.Minute

	// activityIdleAfterMs: a bucket whose newest run ended longer ago than this
	// is at most 'idle', never 'active'.
	activityIdleAfterMs int64 = 15 * 60 * 1000
	// activityTransitionWindowMs bounds the look-back used to count app switches
	// (run boundaries) before a run — the cohesion-density signal.
	activityTransitionWindowMs int64 = 10 * 60 * 1000
	// activitySegmentLocalGapMs is the local continuity window for fuzzy subject
	// anchors. Activities are contiguous segments; beyond this gap, a repeated
	// loose topic starts a fresh segment.
	activitySegmentLocalGapMs int64 = 8 * 60 * 1000
	// activitySegmentConcreteGapMs is the wider continuity window for concrete
	// objects such as repos, PRs, files, or domains.
	activitySegmentConcreteGapMs int64 = 30 * 60 * 1000
	// activityContinuesLookback bounds the prior-day buckets scanned for a
	// cross-day continuation link.
	activityContinuesLookback = 40
	// composerTrivialActiveMs: a run with less active time than this is a
	// glance (open an app, switch a window, log in) and is skipped before any
	// LLM call — it never becomes or joins an activity. Longer-but-mechanical
	// runs are still filtered by the LLM "skip" route.
	composerTrivialActiveMs int64 = 5000
	// activityComposerBackfillHorizonMs bounds how far back the composer reaches
	// for unrouted runs, so first launch never fans out LLM calls over the
	// entire run history. 2 days aligns with the timeline auto-summarize horizon
	// (older runs are summarized only on demand), and keeps the one-time
	// backfill cost bounded; today is composed first regardless (see
	// GetComposerReadyRuns ordering).
	activityComposerBackfillHorizonMs int64 = 2 * 24 * 60 * 60 * 1000
)

// composerBucketUpdate is the refreshed bucket summary the LLM returns alongside
// its routing decision (the "fold route + refresh into one call" decision).
type composerBucketUpdate struct {
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Status       string   `json:"status"` // active | idle
	LatestChange string   `json:"latestChange"`
	Category     string   `json:"category"` // build | connect | explore | sustain
	Entities     []Entity `json:"entities"`
}

// composerResponse is the per-run composer call's output.
type composerResponse struct {
	Route        string               `json:"route"` // "join" | "new"
	JoinBucketID string               `json:"joinBucketId"`
	Merge        []string             `json:"merge"` // optional [survivorId, foldedId]
	Confidence   string               `json:"confidence"`
	Reason       string               `json:"reason"`
	Admission    []composerAdmission  `json:"admission"`
	Bucket       composerBucketUpdate `json:"bucket"`
}

type composerAdmission struct {
	BucketID         string   `json:"bucketId"`
	Decision         string   `json:"decision"`         // join | reject
	ObjectiveMatch   string   `json:"objectiveMatch"`   // strong | medium | weak | none
	SubjectMatch     string   `json:"subjectMatch"`     // strong | medium | weak | none
	StateContinuity  string   `json:"stateContinuity"`  // strong | medium | weak | none
	NegativeEvidence []string `json:"negativeEvidence"` // strong negatives should be explicit
	Reason           string   `json:"reason"`
}

// runActivityComposerLoop is the composer's independent goroutine/ticker,
// mirroring the semantic + timeline-summary loops (decoupled so a slow pass
// cannot starve other layers).
func (c *ActivityTriager) runActivityComposerLoop(ctx context.Context) {
	c.runActivityComposerPassGuarded(ctx)
	ticker := time.NewTicker(activityComposerCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runActivityComposerPassGuarded(ctx)
		}
	}
}

func (c *ActivityTriager) runActivityComposerPassGuarded(ctx context.Context) {
	if !c.activityComposerMu.TryLock() {
		return
	}
	defer c.activityComposerMu.Unlock()
	c.runActivityComposerPass(ctx)
}

// runActivityComposerPass settles quiet activities, then drains composer-ready
// runs (summarized but not-yet-composed, or grown since), composing each. Never
// errors out — a failure must not disturb the timeline/run layers.
func (c *ActivityTriager) runActivityComposerPass(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[activity-composer] recovered from panic: %v", r)
		}
	}()
	if !featureActivityComposer {
		return
	}
	store := c.activityStore()
	if store == nil || c.app == nil {
		return
	}
	now := time.Now().UnixMilli()
	c.settleActivities(store, now)
	if !c.activityJoinsEnabled() {
		return
	}
	c.app.mu.Lock()
	runner := c.app.runner
	c.app.mu.Unlock()
	if runner == nil {
		return
	}

	// One-time cleanup of trivial single-run activities composed before the
	// glance filter existed (open-app/login/switch noise). Runs once per
	// process; the glance filter prevents new ones.
	if !c.activityComposerCleaned {
		if n := c.purgeTrivialActivities(store, now); n > 0 {
			log.Printf("[activity-composer] purged %d trivial activities", n)
			c.app.emitBrowserEvent("activity:activities-updated")
		}
		c.activityComposerCleaned = true
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		ready, err := store.GetComposerReadyRuns(activityComposerQueueBatch, now-activityComposerBackfillHorizonMs, startOfDayLocal(now))
		if err != nil {
			log.Printf("[activity-composer] load ready runs: %v", err)
			return
		}
		if len(ready) == 0 {
			return
		}
		// Runs go to the model in ordered batches of
		// activityComposerBatchSize (activity_compose_batch.go).
		progressed, stop := c.composeRuns(ctx, store, ready, func() int64 { return time.Now().UnixMilli() })
		if stop {
			return
		}
		if !progressed {
			return
		}
	}
}

// purgeTrivialActivities deletes single-run activities whose sole run is a
// glance (active time below the trivial floor, or a run that no longer exists),
// and skip-marks those runs so they are not recomposed. Returns the count
// removed. Cleans up data composed before the glance filter landed.
func (c *ActivityTriager) purgeTrivialActivities(store *ActivityStore, now int64) int {
	acts, err := store.GetActivitiesInRange(0, 0)
	if err != nil {
		log.Printf("[activity-composer] purge load activities: %v", err)
		return 0
	}
	removed := 0
	for _, a := range acts {
		runIDs := composeParseStrings(a.RunIDs)
		if len(runIDs) != 1 {
			continue
		}
		run, err := store.GetTimelineRunByID(runIDs[0])
		if err != nil {
			continue
		}
		if run != nil && run.ActiveMs >= composerTrivialActiveMs {
			continue // single but substantive — keep
		}
		if err := c.app.deleteActivityWithTopicScrub(store, a.ID); err != nil {
			log.Printf("[activity-composer] purge delete %s: %v", a.ID, err)
			continue
		}
		// Skip-mark the run so it isn't recomposed into a new bucket.
		skipCount := 0
		if run != nil {
			skipCount = run.SummarizedEventCount
		}
		_ = store.UpsertActivityRunLink(runIDs[0], "", skipCount, now)
		removed++
	}
	return removed
}

// composeRun assigns a summarized app run to a resumable user task. App runs are
// evidence; activities are task abstractions, so the router considers every
// identity-related open task rather than only the immediately previous segment.
func (c *ActivityTriager) composeRun(ctx context.Context, run ActivityTimelineRunRow, now int64) (bool, error) {
	item, changed, err := c.prepareComposeRun(run, now)
	if item == nil {
		return changed, err
	}
	prompt := buildComposerPrompt(item.candidates, item.run, item.summary, item.transition, c.composerLanguageInstruction())
	if prompt != "" {
		prompt += c.composerNamingCorrections()
	}
	resp, err := c.runComposerPrompt(ctx, prompt)
	if err != nil {
		return false, err
	}
	return c.applyComposerResponse(*item, resp, nil, now)
}

// composerItem is one run ready for a composer model decision.
type composerItem struct {
	run        ActivityTimelineRunRow
	summary    ActivityTimelineGroupSummary
	candidates []ActivityRow
	transition composerTransition
}

// prepareComposeRun runs every step before the model call. A nil item means
// the run was settled without a model (privacy, a glance, a missing summary)
// or failed; changed and err report that outcome.
func (c *ActivityTriager) prepareComposeRun(run ActivityTimelineRunRow, now int64) (*composerItem, bool, error) {
	if !c.activityJoinsEnabled() {
		return nil, false, nil
	}
	store := c.activityStore()
	if store == nil {
		return nil, false, fmt.Errorf("activity store not ready")
	}

	allowed, err := store.RefreshTimelineRunEvidence(&run)
	if err != nil {
		return nil, false, err
	}
	if c.app == nil {
		return nil, false, fmt.Errorf("activity app not ready")
	}
	if allowed {
		allowed, err = c.app.activityTimelineRunAllowsModelReadResult(store, run)
		if err != nil {
			// A failed read is retryable. Do not persist the same empty link
			// used for a confirmed privacy rejection or trivial glance.
			return nil, false, fmt.Errorf("check composer source privacy: %w", err)
		}
	}
	if !allowed {
		return nil, false, store.UpsertActivityRunLink(run.ID, "", run.SummarizedEventCount, now)
	}

	// Deterministic glance filter: a sub-threshold run (open app / switch
	// window / login / momentary glance) is not a piece of work — skip it
	// before spending an LLM call. Mark it composed so it isn't retried.
	if run.ActiveMs < composerTrivialActiveMs {
		return nil, false, store.UpsertActivityRunLink(run.ID, "", run.SummarizedEventCount, now)
	}

	summaries, err := store.GetTimelineGroupSummaries([]string{run.ID})
	if err != nil {
		return nil, false, fmt.Errorf("load run summary: %w", err)
	}
	runSummary, ok := summaries[run.ID]
	if !ok {
		// summary_status=done but no row (raced/deleted) — skip without churn.
		return nil, false, store.UpsertActivityRunLink(run.ID, "", run.SummarizedEventCount, now)
	}

	common, err := store.commonComposerEvidence(max(run.StartMs, run.EndMs))
	if err != nil {
		return nil, false, err
	}
	candidates, err := c.activityAssignmentCandidates(store, run, runSummary, common)
	if err != nil {
		return nil, false, fmt.Errorf("load assignment candidates: %w", err)
	}
	candidates = c.app.filterComposerModelCandidates(store, candidates)
	return &composerItem{
		run: run, summary: runSummary, candidates: candidates,
		transition: c.composeTransitionContext(store, run, common),
	}, false, nil
}

func (c *ActivityTriager) composerLanguageInstruction() string {
	if c.app == nil {
		return ""
	}
	return responseLanguageInstruction(c.app.currentUILanguageName())
}

func (c *ActivityTriager) composerNamingCorrections() string {
	store := c.activityStore()
	if store == nil {
		return ""
	}
	corrections, err := store.recentActivityNameCorrections(activityNamingCorrectionsInPrompt)
	if err != nil {
		log.Printf("[activity] reading naming corrections: %v", err)
		return ""
	}
	return composerNamingCorrections(corrections)
}

// applyComposerResponse guards the model's route against the run's own
// candidates (plus, in a batch, activities earlier runs just started) and
// applies it.
func (c *ActivityTriager) applyComposerResponse(item composerItem, resp composerResponse, extra []ActivityRow, now int64) (bool, error) {
	if !c.activityJoinsEnabled() {
		return false, nil
	}
	store := c.activityStore()
	if store == nil {
		return false, fmt.Errorf("activity store not ready")
	}
	allowed := append(append([]ActivityRow{}, item.candidates...), extra...)
	resp = c.guardComposerAssignment(resp, allowed)
	return c.composeApply(store, item.run, item.summary, resp, now)
}

type activityRunIdentity struct {
	Tokens    []ActivityEvidenceToken
	Common    map[string]bool
	Concrete  map[string]bool
	Intent    map[string]bool
	Local     map[string]bool
	Objective map[string]bool
	Subject   map[string]bool
}

type activityAssignmentCandidate struct {
	Activity ActivityRow
	Score    int
}

// activityAssignmentCandidates keeps matching recent open tasks plus the newest
// open task even without a match. Specific evidence can also resume a task for
// 24 hours; paused tasks require that strong match. This is candidate retrieval,
// never permission to join: the existing model admission guard still applies.
func (c *ActivityTriager) activityAssignmentCandidates(store *ActivityStore, run ActivityTimelineRunRow, runSummary ActivityTimelineGroupSummary, commonMaps ...map[string]bool) ([]ActivityRow, error) {
	if !c.activityJoinsEnabled() {
		return nil, nil
	}
	allowed, err := store.timelineRunEvidenceAllowed(run)
	if err != nil || !allowed {
		return nil, err
	}
	activities, err := store.GetUnsettledActivities()
	if err != nil {
		return nil, err
	}
	var common map[string]bool
	if len(commonMaps) > 0 {
		common = commonMaps[0]
	} else {
		common, err = store.commonComposerEvidence(max(run.StartMs, run.EndMs))
		if err != nil {
			return nil, err
		}
	}
	runID := identityForTimelineRun(run, runSummary)
	runID.Common = common
	candidates := make([]activityAssignmentCandidate, 0, len(activities))
	newestOpenID := ""
	cached := map[string]*ActivityTimelineRunRow{}
	for _, activity := range activities {
		rejected, err := store.activityRunRejected(activity.ID, run.ID)
		if err != nil {
			return nil, err
		}
		if rejected {
			continue
		}
		if !activityStatusResumable(activity.Status) {
			continue
		}
		gap := max(int64(0), run.StartMs-activity.EndMs)
		if gap > activityStrongLinkMs && (newestOpenID != "" || activity.Status == activityStatusPaused) {
			continue
		}
		ids := composeParseStrings(activity.RunIDs)
		if len(ids) == 0 {
			continue
		}
		eligibleCount := 0
		filteredMembers := false
		activity.EvidenceTokens = nil
		for _, id := range ids {
			member, loaded := cached[id]
			if !loaded {
				member, err = store.GetTimelineRunByID(id)
				if err != nil {
					return nil, err
				}
				if member != nil {
					ok, loadErr := store.timelineRunEvidenceAllowed(*member)
					if loadErr != nil {
						return nil, loadErr
					}
					if !ok {
						member = nil
					}
				}
				cached[id] = member
			}
			if member == nil || member.EvidenceExcluded {
				filteredMembers = true
				continue
			}
			eligibleCount++
			activity.EvidenceTokens = append(activity.EvidenceTokens, member.EvidenceTokens...)
		}
		if eligibleCount == 0 {
			continue
		}
		if filteredMembers {
			// Composed prose may incorporate the excluded member. Keep only
			// surviving token evidence for admission and the model prompt.
			activity.Title, activity.Description, activity.Entities = "", "", "[]"
			activity.ActiveMsByApp = "{}"
		}
		if newestOpenID == "" && activity.Status != activityStatusPaused && activity.StartMs <= run.StartMs {
			newestOpenID = activity.ID
		}
		comparisonMs := max(run.StartMs, run.EndMs)
		if activity.StartMs > run.StartMs {
			// A late summary may be older than the task already composed from
			// its later restatement. Admit future-relative evidence only within
			// the same strong lease, and only with a strong match below.
			comparisonMs = max(comparisonMs, min(activity.EndMs, comparisonMs+activityStrongLinkMs))
		}
		activity.EvidenceTokens = eligibleActivityPromptEvidence(activity.EvidenceTokens, run.EvidenceTokens, common, comparisonMs)
		activity.EvidenceTokens = orderActivityPromptEvidence(activity.EvidenceTokens, run.EvidenceTokens)
		activityID := identityForActivity(activity)
		_, strong := activityEvidenceScore(runID.Tokens, activityID.Tokens, common, comparisonMs)
		if (activity.Status == activityStatusPaused || activity.StartMs > run.StartMs) && !strong {
			continue
		}
		scoringRun := run
		scoringRun.StartMs = comparisonMs
		score := activityAssignmentScore(runID, activityID, scoringRun, activity)
		if activity.ID != newestOpenID && !strong && (gap > activityCandidateWindowMs || score == 0) {
			continue
		}
		candidates = append(candidates, activityAssignmentCandidate{Activity: activity, Score: score})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Activity.EndMs > candidates[j].Activity.EndMs
	})
	// Reserve a slot for the newest open task even when six matches outrank it.
	if len(candidates) > activityCandidateLimit {
		for i := activityCandidateLimit; i < len(candidates); i++ {
			if candidates[i].Activity.ID == newestOpenID {
				candidates[activityCandidateLimit-1] = candidates[i]
				break
			}
		}
		candidates = candidates[:activityCandidateLimit]
	}
	out := make([]ActivityRow, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.Activity)
	}
	return out, nil
}

func activityAssignmentScore(runID, activityID activityRunIdentity, run ActivityTimelineRunRow, activity ActivityRow) int {
	score, _ := activityEvidenceScore(runID.Tokens, activityID.Tokens, runID.Common, run.StartMs)
	gap := max(int64(0), run.StartMs-activity.EndMs)
	// Legacy summary identities are weak hints only. Two topic words must agree;
	// chat sources never supply free-word joins. An inferred anchor cannot extend
	// the evidence lease or defeat damping of frequently observed tokens.
	if len(runID.Tokens) == 0 && len(activityID.Tokens) == 0 && !activityEvidenceChatApp(run.AppKey, run.AppName) && gap <= activityWeakLinkMs {
		shared := sharedActivityKeyCount(runID.Intent, activityID.Intent)
		if shared >= 2 && (sharedActivityKeyCount(runID.Subject, activityID.Subject) > 0 || sharedActivityKeyCount(runID.Objective, activityID.Objective) > 0 || sharedActivityKeyCount(runID.Concrete, activityID.Concrete) > 0) {
			score += min(24, shared*4)
		}
	}
	if score > 0 && gap <= activityWeakLinkMs {
		for app := range composeParseInt64Map(activity.ActiveMsByApp) {
			if activityAppFamily(app) == activityAppFamily(run.AppName) {
				score += 3
				break
			}
		}
	}
	return score
}

func sharedActivityKeyCount(a, b map[string]bool) int {
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return n
}

func (c *ActivityTriager) guardComposerAssignment(resp composerResponse, candidates []ActivityRow) composerResponse {
	candidateIDs := map[string]bool{}
	for _, candidate := range candidates {
		candidateIDs[candidate.ID] = true
	}
	if len(resp.Merge) == 2 {
		survivorID := strings.TrimSpace(resp.Merge[0])
		foldedID := strings.TrimSpace(resp.Merge[1])
		if !candidateIDs[survivorID] || !candidateIDs[foldedID] ||
			(!composerAdmissionAllowsJoin(resp.Admission, survivorID) && !composerAdmissionAllowsJoin(resp.Admission, foldedID)) {
			resp.Merge = nil
		}
	}
	if resp.Route != "join" {
		return resp
	}
	if strings.EqualFold(strings.TrimSpace(resp.Confidence), "low") {
		resp.Route = "new"
		resp.JoinBucketID = ""
		resp.Merge = nil
		return resp
	}
	joinID := strings.TrimSpace(resp.JoinBucketID)
	if joinID == "" {
		resp.Route = "new"
		return resp
	}
	if !candidateIDs[joinID] {
		resp.Route = "new"
		resp.JoinBucketID = ""
		resp.Merge = nil
		return resp
	}
	if !composerAdmissionAllowsJoin(resp.Admission, joinID) {
		resp.Route = "new"
		resp.JoinBucketID = ""
		resp.Merge = nil
	}
	return resp
}

func composerAdmissionAllowsJoin(admissions []composerAdmission, bucketID string) bool {
	var admission *composerAdmission
	for i := range admissions {
		if strings.TrimSpace(admissions[i].BucketID) == bucketID {
			admission = &admissions[i]
			break
		}
	}
	if admission == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(admission.Decision), "join") {
		return false
	}
	if hasStrongNegativeEvidence(admission.NegativeEvidence) {
		return false
	}
	objective := admissionLevel(admission.ObjectiveMatch)
	subject := admissionLevel(admission.SubjectMatch)
	state := admissionLevel(admission.StateContinuity)
	if objective >= admissionMedium && subject >= admissionMedium && state >= admissionWeak {
		return true
	}
	if subject >= admissionStrong && objective >= admissionWeak {
		return true
	}
	return false
}

type admissionLevelValue int

const (
	admissionNone admissionLevelValue = iota
	admissionWeak
	admissionMedium
	admissionStrong
)

func admissionLevel(s string) admissionLevelValue {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "strong":
		return admissionStrong
	case "medium":
		return admissionMedium
	case "weak":
		return admissionWeak
	default:
		return admissionNone
	}
}

func hasStrongNegativeEvidence(items []string) bool {
	for _, item := range items {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if strings.Contains(item, "strong") ||
			strings.Contains(item, "different task") ||
			strings.Contains(item, "different subject") ||
			strings.Contains(item, "history review") ||
			strings.Contains(item, "activity ui") ||
			strings.Contains(item, "only person") ||
			strings.Contains(item, "only app") {
			return true
		}
	}
	return false
}

func (c *ActivityTriager) segmentContinuesPrevious(store *ActivityStore, prev ActivityRow, run ActivityTimelineRunRow, runSummary ActivityTimelineGroupSummary) bool {
	gap := run.StartMs - prev.EndMs
	if gap < 0 {
		gap = 0
	}
	prevRunID := lastString(composeParseStrings(prev.RunIDs))
	if prevRunID == "" {
		return false
	}
	prevRun, err := store.GetTimelineRunByID(prevRunID)
	if err != nil || prevRun == nil {
		return false
	}
	summaries, err := store.GetTimelineGroupSummaries([]string{prevRunID})
	if err != nil {
		return false
	}
	prevSummary, ok := summaries[prevRunID]
	if !ok {
		return false
	}

	currentID := identityForTimelineRun(run, runSummary)
	previousID := identityForTimelineRun(*prevRun, prevSummary)
	if gap <= activitySegmentConcreteGapMs && mapsIntersect(currentID.Concrete, previousID.Concrete) {
		return true
	}
	if gap <= activitySegmentLocalGapMs && mapsIntersect(currentID.Local, previousID.Local) {
		return true
	}
	return false
}

func (c *ActivityTriager) composeApplySegment(store *ActivityStore, target *ActivityRow, run ActivityTimelineRunRow, runSummary ActivityTimelineGroupSummary, now int64) (bool, error) {
	dates, dirty := map[string]bool{}, false
	defer func() {
		if dirty {
			c.app.emitActivityDateUpdate(dates)
		}
	}()

	day := startOfDayLocal(run.StartMs)
	isNew := target == nil
	if isNew {
		activityID := "act_" + run.ID
		existing, err := store.GetActivityByID(activityID)
		if err != nil {
			return false, err
		}
		if existing != nil {
			target = existing
			isNew = false
		} else {
			target = &ActivityRow{
				ID:         activityID,
				DayStartMs: day,
				CreatedAt:  now,
			}
		}
	}

	activityAddRowDates(dates, *target)
	runIDs := composeParseStrings(target.RunIDs)
	if !composeContains(runIDs, run.ID) {
		runIDs = append(runIDs, run.ID)
	}
	if err := rebuildActivitySegment(store, target, runIDs, run.ID, runSummary, now); err != nil {
		return false, err
	}

	if isNew {
		if priorID := composeFindContinuation(store, day, parseEntityList(target.Entities)); priorID != "" {
			target.ContinuesActivityID = priorID
		}
		if err := store.InsertActivity(*target); err != nil {
			return false, err
		}
	} else if err := store.UpdateActivity(*target); err != nil {
		return false, err
	}
	activityAddRowDates(dates, *target)
	dirty = true
	if err := store.UpsertActivityRunLink(run.ID, target.ID, run.SummarizedEventCount, now); err != nil {
		return false, err
	}
	return true, nil
}

func rebuildActivitySegment(store *ActivityStore, target *ActivityRow, runIDs []string, latestRunID string, latestSummary ActivityTimelineGroupSummary, now int64) error {
	orderedRuns := make([]ActivityTimelineRunRow, 0, len(runIDs))
	validRunIDs := make([]string, 0, len(runIDs))
	for _, id := range runIDs {
		run, err := store.GetTimelineRunByID(id)
		if err != nil {
			return err
		}
		if run == nil {
			continue
		}
		orderedRuns = append(orderedRuns, *run)
		validRunIDs = append(validRunIDs, id)
	}
	if len(orderedRuns) == 0 {
		return fmt.Errorf("activity segment %s has no valid runs", target.ID)
	}

	summaries, err := store.GetTimelineGroupSummaries(validRunIDs)
	if err != nil {
		return err
	}
	if latestRunID != "" {
		summaries[latestRunID] = latestSummary
	}

	byApp := map[string]int64{}
	var entities []Entity
	var firstSummary, lastSummary ActivityTimelineGroupSummary
	var firstSummarySet bool
	target.StartMs = 0
	target.EndMs = 0
	for i, run := range orderedRuns {
		allowed, err := store.timelineRunEvidenceAllowed(run)
		if err != nil {
			return err
		}
		if allowed {
			byApp[run.AppName] += run.ActiveMs
		}
		if i == 0 || run.StartMs < target.StartMs || target.StartMs == 0 {
			target.StartMs = run.StartMs
		}
		if run.EndMs > target.EndMs {
			target.EndMs = run.EndMs
		}
		if summary, ok := summaries[run.ID]; ok {
			if !firstSummarySet {
				firstSummary = summary
				firstSummarySet = true
			}
			lastSummary = summary
			entities = composeUnionEntities(entities, typedEntitiesForRunSummary(run, summary))
		}
	}

	target.RunIDs = composeJSON(validRunIDs)
	target.ActiveMsByApp = composeJSON(byApp)
	target.Title = composeFirstNonEmpty(firstSummary.Title, target.Title, latestSummary.Title, orderedRuns[0].AppName)
	if len(validRunIDs) == 1 {
		target.Description = composeFirstNonEmpty(firstSummary.Description, target.Description)
	} else {
		target.Description = composeSegmentDescription(firstSummary, lastSummary)
	}
	target.LatestChange = composeFirstNonEmpty(lastSummary.Title, latestSummary.Title)
	target.Category = composeFirstNonEmpty(activityCategoryForRunSummary(lastSummary), target.Category, "sustain")
	target.CategoryColor = semanticCategoryDotColor(target.Category)
	target.Entities = composeJSON(entities)
	status := activityStatusActive
	if now-target.EndMs > activityIdleAfterMs {
		status = activityStatusIdle
	}
	target.Status = status
	target.UpdatedAt = now
	return nil
}

// composerTransition carries the inter-run signal derived from the run rows.
type composerTransition struct {
	TriageHints       []composerTriageHint
	GapMs             int64
	PrevApp           string
	RecentSwitchCount int
	Restatements      []composerRestatement
}

// composeTransitionContext derives the app-switch cohesion signal for a run from
// the surrounding run rows (consecutive runs = app switches), with no new
// persistence: the prior run's end gap and the count of run boundaries in the
// preceding window.
type composerRestatement struct {
	RunID  string
	App    string
	Tokens []ActivityEvidenceToken
}

func (c *ActivityTriager) composeTransitionContext(store *ActivityStore, run ActivityTimelineRunRow, commonMaps ...map[string]bool) composerTransition {
	// The switch count covers the entire ten-minute window. Only evidence
	// hints use the last-three limit.
	rows, err := store.db.Query(`SELECT `+timelineRunColumns+` FROM activity_timeline_groups
		WHERE end_ms >= ? AND end_ms <= ? ORDER BY end_ms DESC`, max(int64(0), run.StartMs-activityWeakLinkMs), run.StartMs)
	if err != nil {
		return composerTransition{}
	}
	prior, err := scanTimelineRunRows(rows)
	rows.Close()
	if err != nil {
		return composerTransition{}
	}
	transition := composerTransition{}
	var common map[string]bool
	if len(commonMaps) > 0 {
		common = commonMaps[0]
	} else if common, err = store.commonComposerEvidence(max(run.StartMs, run.EndMs)); err != nil {
		return transition
	}
	considered := 0
	var readablePrior []ActivityTimelineRunRow
	for _, prev := range prior {
		if prev.ID == run.ID {
			continue
		}
		considered++
		gap := run.StartMs - prev.EndMs
		if gap < 0 {
			continue
		}
		if considered == 1 {
			// The nearest boundary still defines the gap when its app is
			// private. Do not substitute a different, older predecessor.
			transition.GapMs = gap
		}
		allowed, err := store.timelineRunEvidenceAllowed(prev)
		if err != nil || !allowed || c.app == nil || !c.app.activityTimelineRunAllowsModelRead(store, prev) {
			continue
		}
		readablePrior = append(readablePrior, prev)
		if considered == 1 {
			transition.PrevApp = prev.AppName
		}
		if gap <= activityTransitionWindowMs {
			transition.RecentSwitchCount++
		}
		if considered > 3 || gap > activityWeakLinkMs {
			continue
		}
		matched := matchedActivityEvidence(run.EvidenceTokens, prev.EvidenceTokens, run.StartMs, common)
		if len(matched) > 0 {
			transition.Restatements = append(transition.Restatements, composerRestatement{RunID: prev.ID, App: prev.AppName, Tokens: matched})
		}
	}
	transition.TriageHints = store.composerTriageHints(run, readablePrior)
	return transition
}

// runComposerPrompt keeps persisted task joins and merges on the chat-quality
// model, with background priority so unattended work uses the paced route.
func (c *ActivityTriager) runComposerPrompt(ctx context.Context, prompt string) (composerResponse, error) {
	var resp composerResponse
	if err := c.runComposerJSON(ctx, prompt, activityComposerTimeout, &resp); err != nil {
		return composerResponse{}, err
	}
	return resp, nil
}

// runComposerJSON runs one composer call and decodes its JSON reply into out.
func (c *ActivityTriager) runComposerJSON(ctx context.Context, prompt string, timeout time.Duration, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c.app.mu.Lock()
	runner := c.app.runner
	c.app.mu.Unlock()
	if runner == nil {
		return fmt.Errorf("runner not initialized")
	}

	var result strings.Builder
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		AutoApproveTools:  true,
		Prompt:            redactActivityModelText(prompt),
		Model:             c.app.chatTaskModel(),
		Priority:          agent.PriorityBackground,
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(event *agent.StreamEvent) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if event.Type == "assistant" {
			result.WriteString(event.Content)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("execute activity composer: %w", err)
	}
	raw := stripCodeFences(strings.TrimSpace(result.String()))
	if err := unmarshalJSONBestEffort(raw, out); err != nil {
		return fmt.Errorf("parse composer response: %w", err)
	}
	return nil
}

// composeApply mutates the store per the routing decision: optional bucket merge,
// resolve/create the target bucket, append the run, recompute time accounting +
// span incrementally, refresh the LLM-written fields, set the cross-day link, and
// persist + link the run. Pure store logic (no LLM) so it is unit-testable.
func (c *ActivityTriager) composeApply(store *ActivityStore, run ActivityTimelineRunRow, runSummary ActivityTimelineGroupSummary, resp composerResponse, now int64) (bool, error) {
	dates, dirty := map[string]bool{}, false
	defer func() {
		if dirty {
			c.app.emitActivityDateUpdate(dates)
		}
	}()

	day := startOfDayLocal(run.StartMs)
	// A growing run refreshes its existing task, including a task with a
	// terminal outcome. Summary retries cannot move it or duplicate its time.
	var existingID string
	if err := store.db.QueryRow(`SELECT activity_id FROM activity_run_links WHERE run_id = ?`, run.ID).Scan(&existingID); err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if existingID == "" {
		// Repair the interrupted persist-before-link path as well as normal
		// growth. A missing link must not reinsert an existing bucket id.
		err := store.db.QueryRow(`SELECT id FROM activities WHERE id = ? AND EXISTS (SELECT 1 FROM json_each(run_ids) WHERE value = ?)`, "act_"+run.ID, run.ID).Scan(&existingID)
		if err != nil && err != sql.ErrNoRows {
			return false, err
		}
	}
	if existingID != "" {
		resp.Route, resp.JoinBucketID, resp.Merge = "join", existingID, nil
	}

	// 0. Skip: the LLM judged this run mechanical / not a piece of work (an app
	// open, a glance, idle browsing). Mark it composed without creating or
	// joining any activity.
	if resp.Route == "skip" {
		return false, store.UpsertActivityRunLink(run.ID, "", run.SummarizedEventCount, now)
	}

	// 1. Merge two existing buckets if the new run reveals they are one.
	joinID := strings.TrimSpace(resp.JoinBucketID)
	if len(resp.Merge) == 2 {
		survivor, folded := strings.TrimSpace(resp.Merge[0]), strings.TrimSpace(resp.Merge[1])
		if survivor != "" && folded != "" && survivor != folded {
			if err := c.mergeBuckets(store, folded, survivor, now); err != nil {
				log.Printf("[activity-composer] merge %s<-%s: %v", survivor, folded, err)
			} else if joinID == folded {
				joinID = survivor
			}
		}
	}

	// 2. Resolve the target bucket (join a resumable task, else new).
	var target *ActivityRow
	if resp.Route == "join" && joinID != "" {
		rejected, err := store.activityRunRejected(joinID, run.ID)
		if err != nil {
			return false, err
		}
		if t, _ := store.GetActivityByID(joinID); !rejected && t != nil && (activityStatusResumable(t.Status) || existingID == joinID) {
			target = t
		}
	}
	isNew := target == nil
	if isNew {
		activityID := "act_" + run.ID
		// If the task began with the corrected run, its original id remains
		// occupied by other runs. Give the detached run its own new identity.
		if rejected, err := store.activityRunRejected(activityID, run.ID); err != nil {
			return false, err
		} else if rejected {
			var corrections int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM activity_run_corrections WHERE run_id = ?`, run.ID).Scan(&corrections); err != nil {
				return false, err
			}
			activityID = deterministicTaskEpisodeID("activity-after-correction", fmt.Sprintf("%s:%d", run.ID, corrections))
		}
		stored, err := store.GetActivityByID(activityID)
		if err != nil {
			return false, err
		}
		if stored != nil {
			target, isNew, existingID = stored, false, stored.ID
		} else {
			target = &ActivityRow{
				ID:         activityID,
				DayStartMs: day,
				StartMs:    run.StartMs,
				EndMs:      run.EndMs,
				CreatedAt:  now,
			}
		}
	}

	// 3. Append the run (dedup).
	activityAddRowDates(dates, *target)
	runIDs := composeParseStrings(target.RunIDs)
	alreadyMember := composeContains(runIDs, run.ID)
	if !alreadyMember {
		runIDs = append(runIDs, run.ID)
	}
	target.RunIDs = composeJSON(runIDs)

	// 4. Rebuild time from members, respecting later privacy changes. The
	// incoming run overrides its stored revision while it is growing.
	byApp, err := store.activityRunActiveMsByApp(runIDs, &run)
	if err != nil {
		return false, err
	}
	target.ActiveMsByApp = composeJSON(byApp)
	if run.StartMs < target.StartMs || target.StartMs == 0 {
		target.StartMs = run.StartMs
	}
	if run.EndMs > target.EndMs {
		target.EndMs = run.EndMs
	}

	// 5. Apply the LLM-written fields (fall back to the run summary / existing).
	target.Title = composeFirstNonEmpty(strings.TrimSpace(resp.Bucket.Title), target.Title, strings.TrimSpace(runSummary.Title))
	target.Description = composeFirstNonEmpty(strings.TrimSpace(resp.Bucket.Description), target.Description, strings.TrimSpace(runSummary.Description))
	target.LatestChange = strings.TrimSpace(resp.Bucket.LatestChange)
	target.Category = composeFirstNonEmpty(strings.TrimSpace(resp.Bucket.Category), target.Category, "other")
	target.CategoryColor = semanticCategoryDotColor(target.Category)
	if len(resp.Bucket.Entities) > 0 {
		target.Entities = composeJSON(resp.Bucket.Entities)
	} else if strings.TrimSpace(target.Entities) == "" {
		target.Entities = "[]"
	}
	status := strings.TrimSpace(resp.Bucket.Status)
	if status != activityStatusActive && status != activityStatusIdle {
		status = activityStatusActive
	}
	if now-target.EndMs > activityIdleAfterMs && status == activityStatusActive {
		status = activityStatusIdle
	}
	target.Status = status
	target.UpdatedAt = now

	// 6. Cross-day continuation (new buckets only) — only when a STRONG, specific
	// entity (repo/pr/project/topic) matches a prior-day bucket, so generic app
	// names (WeChat, Slack) never spuriously link everything together.
	if isNew {
		if priorID := composeFindContinuation(store, day, parseEntityList(target.Entities)); priorID != "" {
			target.ContinuesActivityID = priorID
		}
	}

	// 7. Persist + link the run.
	if isNew {
		if err := store.InsertActivity(*target); err != nil {
			return false, err
		}
	} else {
		if err := store.UpdateActivity(*target); err != nil {
			return false, err
		}
	}
	activityAddRowDates(dates, *target)
	dirty = true
	if err := store.UpsertActivityRunLink(run.ID, target.ID, run.SummarizedEventCount, now); err != nil {
		return false, err
	}
	if err := store.recordActivityLinkReason(target.ID, run); err != nil {
		return false, err
	}
	if err := store.observeActivityRunOutcomeWithEvidence(target.ID, run, now); err != nil {
		return false, err
	}
	return true, nil
}

// mergeBuckets folds `from` into `to`: concat run lists, sum time accounting,
// widen span, union entities, repoint run links, delete `from`.
func (c *ActivityTriager) mergeBuckets(store *ActivityStore, fromID, toID string, now int64) error {
	if rejected, err := store.activityMergeCorrected(fromID, toID); err != nil {
		return err
	} else if rejected {
		return fmt.Errorf("merge contradicts an activity link correction")
	}
	from, err := store.GetActivityByID(fromID)
	if err != nil || from == nil {
		return fmt.Errorf("merge: load from %s: %w", fromID, err)
	}
	to, err := store.GetActivityByID(toID)
	if err != nil || to == nil {
		return fmt.Errorf("merge: load to %s: %w", toID, err)
	}

	dates, dirty := map[string]bool{}, false
	activityAddRowDates(dates, *from)
	activityAddRowDates(dates, *to)
	defer func() {
		if dirty {
			activityAddRowDates(dates, *to)
			c.app.emitActivityDateUpdate(dates)
		}
	}()
	runIDs := composeParseStrings(to.RunIDs)
	for _, id := range composeParseStrings(from.RunIDs) {
		if !composeContains(runIDs, id) {
			runIDs = append(runIDs, id)
		}
	}
	to.RunIDs = composeJSON(runIDs)

	byApp, err := store.activityRunActiveMsByApp(runIDs, nil)
	if err != nil {
		return err
	}
	to.ActiveMsByApp = composeJSON(byApp)
	if from.StartMs < to.StartMs || to.StartMs == 0 {
		to.StartMs = from.StartMs
	}
	if from.EndMs > to.EndMs {
		to.EndMs = from.EndMs
	}
	to.Entities = composeJSON(composeUnionEntities(parseEntityList(to.Entities), parseEntityList(from.Entities)))
	to.UpdatedAt = now

	if err := store.RepointActivityRunLinks(fromID, toID); err != nil {
		return err
	}
	dirty = true
	if err := store.UpdateActivity(*to); err != nil {
		return err
	}
	return c.app.deleteActivityWithTopicScrub(store, fromID)
}

// composerStrongEntityTypes are the specific, identifying entity types that
// justify a cross-day continuation link. Generic types (channel, person, and
// bare app names) are excluded — they match far too broadly.
var composerStrongEntityTypes = map[string]bool{
	"repo": true, "pr": true, "file": true, "url": true,
}

// composeFindContinuation links a new segment to the most recent prior-day
// segment sharing the same concrete object. Continuations are links, not merges;
// broad project/topic/person names deliberately do not cross days.
func composeFindContinuation(store *ActivityStore, dayStartMs int64, entities []Entity) string {
	keys := map[string]bool{}
	for _, e := range entities {
		if !composerStrongEntityTypes[strings.ToLower(strings.TrimSpace(e.Type))] {
			continue
		}
		if k := strings.ToLower(strings.TrimSpace(e.Name)); k != "" {
			keys[k] = true
		}
	}
	if len(keys) == 0 {
		return ""
	}
	prior, err := store.GetActivitiesBeforeDay(dayStartMs, activityContinuesLookback)
	if err != nil {
		return ""
	}
	for _, b := range prior {
		for _, e := range parseEntityList(b.Entities) {
			if !composerStrongEntityTypes[strings.ToLower(strings.TrimSpace(e.Type))] {
				continue
			}
			if keys[strings.ToLower(strings.TrimSpace(e.Name))] {
				return b.ID
			}
		}
	}
	return ""
}

// ── compose-local JSON / slice helpers ───────────────────────────────────

func composeParseStrings(raw string) []string {
	var out []string
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func composeParseInt64Map(raw string) map[string]int64 {
	out := map[string]int64{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		out = map[string]int64{}
	}
	return out
}

func composeJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func composeContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func composeFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func composeUnionEntities(a, b []Entity) []Entity {
	seen := map[string]bool{}
	out := make([]Entity, 0, len(a)+len(b))
	for _, e := range append(append([]Entity{}, a...), b...) {
		key := strings.ToLower(e.Type + "|" + e.Name)
		if e.Name == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

func identityForTimelineRun(run ActivityTimelineRunRow, summary ActivityTimelineGroupSummary) activityRunIdentity {
	id := activityRunIdentity{
		Tokens:    run.EvidenceTokens,
		Concrete:  map[string]bool{},
		Intent:    map[string]bool{},
		Local:     map[string]bool{},
		Objective: map[string]bool{},
		Subject:   map[string]bool{},
	}
	addIntent := func(s string) {
		addActivityIntentText(&id, s)
	}
	addSubject := func(s string) {
		if k := subjectActivityAnchor(s); k != "" && k != normalizeActivityAnchor(run.AppName) {
			id.Subject[k] = true
		}
	}
	addLocal := func(s string) {
		if k := normalizeActivityAnchor(s); k != "" && k != normalizeActivityAnchor(run.AppName) {
			id.Local[k] = true
		}
	}
	addConcrete := func(s string) {
		if k := concreteActivityAnchor(s); k != "" {
			id.Concrete[k] = true
			id.Local[k] = true
		}
	}

	for _, entity := range summary.Entities {
		addSubject(entity)
		addLocal(entity)
		addConcrete(entity)
	}
	addIntent(summary.Title)
	addIntent(summary.Description)
	addLocal(summary.Title)
	addConcrete(summary.Title)
	for _, item := range summary.Timeline {
		addIntent(item.Action)
		addIntent(item.Target)
		addConcrete(item.Target)
		for _, detail := range item.Details {
			addIntent(detail)
			addConcrete(detail)
		}
	}
	return id
}

func identityForActivity(activity ActivityRow) activityRunIdentity {
	id := activityRunIdentity{
		Tokens:    activity.EvidenceTokens,
		Concrete:  map[string]bool{},
		Intent:    map[string]bool{},
		Local:     map[string]bool{},
		Objective: map[string]bool{},
		Subject:   map[string]bool{},
	}
	addIntent := func(s string) {
		addActivityIntentText(&id, s)
	}
	addSubject := func(s string) {
		if k := subjectActivityAnchor(s); k != "" {
			id.Subject[k] = true
		}
	}
	addLocal := func(s string) {
		if k := normalizeActivityAnchor(s); k != "" {
			id.Local[k] = true
		}
	}
	addConcrete := func(s string) {
		if k := concreteActivityAnchor(s); k != "" {
			id.Concrete[k] = true
			id.Local[k] = true
		}
	}
	for _, entity := range parseEntityList(activity.Entities) {
		addSubject(entity.Name)
		addLocal(entity.Name)
		addConcrete(entity.Name)
		switch strings.ToLower(strings.TrimSpace(entity.Type)) {
		case entityRepo:
			if name := strings.ToLower(strings.TrimSpace(entity.Name)); name != "" {
				id.Concrete["repo:"+name] = true
				id.Local[name] = true
			}
		case entityPR:
			if name := strings.ToLower(strings.TrimSpace(entity.Name)); name != "" {
				id.Concrete["pr:"+name] = true
				id.Local[name] = true
			}
		case "file":
			if name := strings.ToLower(strings.TrimSpace(entity.Name)); name != "" {
				name = normalizeFileActivityAnchor(name)
				id.Concrete["file:"+name] = true
				id.Local[name] = true
			}
		case "url":
			if name := strings.ToLower(strings.TrimSpace(entity.Name)); name != "" {
				id.Concrete["url:"+name] = true
				id.Local[name] = true
			}
		}
	}
	addLocal(activity.Title)
	addConcrete(activity.Title)
	addIntent(activity.Title)
	addLocal(activity.Description)
	addConcrete(activity.Description)
	addIntent(activity.Description)
	addLocal(activity.LatestChange)
	addConcrete(activity.LatestChange)
	addIntent(activity.LatestChange)
	return id
}

func typedEntitiesForRunSummary(run ActivityTimelineRunRow, summary ActivityTimelineGroupSummary) []Entity {
	out := make([]Entity, 0, len(summary.Entities)+1)
	for _, raw := range summary.Entities {
		name := strings.TrimSpace(raw)
		if name == "" || strings.EqualFold(name, run.AppName) {
			continue
		}
		out = append(out, Entity{Type: activityEntityType(name), Name: name})
	}
	return dedupEntities(out)
}

func displayActivityAnchors(summary ActivityTimelineGroupSummary) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 3)
	add := func(s string) {
		s = strings.TrimSpace(s)
		k := normalizeActivityAnchor(s)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, s)
	}
	for _, entity := range summary.Entities {
		if concreteActivityAnchor(entity) != "" {
			add(entity)
		}
		if len(out) >= 3 {
			return out
		}
	}
	for _, entity := range summary.Entities {
		add(entity)
		if len(out) >= 3 {
			return out
		}
	}
	return out
}

func activityEntityType(name string) string {
	if k := concreteActivityAnchor(name); k != "" {
		switch {
		case strings.HasPrefix(k, "repo:"):
			return entityRepo
		case strings.HasPrefix(k, "pr:"):
			return entityPR
		case strings.HasPrefix(k, "file:"):
			return "file"
		case strings.HasPrefix(k, "url:"):
			return "url"
		}
	}
	return entityTopic
}

func concreteActivityAnchor(text string) string {
	s := strings.TrimSpace(text)
	if s == "" {
		return ""
	}
	if m := githubPRRe.FindStringSubmatch(s); m != nil {
		return "pr:" + strings.ToLower(m[1]+"/"+m[2]+"#"+m[3])
	}
	if m := githubRepoRe.FindStringSubmatch(s); m != nil {
		return "repo:" + strings.ToLower(m[1]+"/"+m[2])
	}
	for _, token := range strings.Fields(strings.Trim(s, " \t\r\n\"'()[]{}<>")) {
		token = strings.Trim(token, " \t\r\n\"'()[]{}<>,.;")
		lower := strings.ToLower(token)
		if looksLikeRepoSlug(lower) {
			return "repo:" + lower
		}
		if looksLikeFilePath(lower) {
			return "file:" + normalizeFileActivityAnchor(lower)
		}
		if looksLikeDomain(lower) {
			return "url:" + lower
		}
	}
	return ""
}

func normalizeActivityAnchor(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(s, " \t\r\n\"'()[]{}<>.,;:!?")
	if len([]rune(s)) < 3 {
		return ""
	}
	switch s {
	case "codex", "synara", "yansu", "wechat", "slack", "google chrome", "chrome", "wezterm", "finder", "todesk":
		return ""
	}
	return s
}

func subjectActivityAnchor(s string) string {
	k := normalizeActivityAnchor(s)
	if k == "" {
		return ""
	}
	if concreteActivityAnchor(k) != "" {
		return k
	}
	if activitySubjectStopwords[k] {
		return ""
	}
	parts := strings.Fields(k)
	if len(parts) >= 3 {
		return k
	}
	if len(parts) == 1 && len([]rune(k)) >= 6 {
		return k
	}
	return ""
}

func addActivityIntentText(id *activityRunIdentity, text string) {
	for _, token := range activityIntentTokens(text) {
		id.Intent[token] = true
		if activityObjectiveTokens[token] {
			id.Objective[token] = true
		}
	}
}

func activityIntentTokens(text string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		token := normalizeActivityIntentToken(raw)
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

func normalizeActivityIntentToken(token string) string {
	token = strings.TrimSpace(token)
	if len([]rune(token)) < 3 {
		return ""
	}
	switch token {
	case "request", "requested", "requesting":
		token = "request"
	case "refund", "refunded", "refunding":
		token = "refund"
	case "downgrade", "downgraded", "downgrading":
		token = "downgrade"
	case "debug", "debugged", "debugging":
		token = "debug"
	case "investigate", "investigated", "investigating":
		token = "investigate"
	case "review", "reviewed", "reviewing":
		token = "review"
	default:
		switch {
		case strings.HasSuffix(token, "ing") && len(token) > 5:
			token = strings.TrimSuffix(token, "ing")
		case strings.HasSuffix(token, "ed") && len(token) > 4:
			token = strings.TrimSuffix(token, "ed")
		case strings.HasSuffix(token, "s") && len(token) > 4:
			token = strings.TrimSuffix(token, "s")
		}
	}
	if activityIntentStopwords[token] {
		return ""
	}
	return token
}

var activityObjectiveTokens = map[string]bool{
	"apply": true, "audit": true, "build": true, "configure": true,
	"debug": true, "downgrade": true, "draft": true, "fix": true,
	"investigate": true, "merge": true, "refund": true, "request": true,
	"review": true, "send": true, "ship": true, "test": true, "update": true,
}

var activityIntentStopwords = map[string]bool{
	"about": true, "accessed": true, "account": true, "activity": true,
	"after": true, "agent": true, "also": true, "and": true, "app": true,
	"because": true, "before": true, "chat": true, "clicked": true,
	"currently": true, "document": true, "event": true, "file": true,
	"from": true, "help": true, "into": true, "issue": true, "latest": true,
	"opened": true, "page": true, "plan": true, "project": true, "report": true,
	"settings": true, "support": true, "task": true, "the": true, "their": true,
	"then": true, "this": true, "through": true, "timeline": true, "user": true,
	"viewed": true, "while": true, "with": true, "work": true, "working": true,
}

var activitySubjectStopwords = map[string]bool{
	"activity": true, "anthropic": true, "chrome": true, "codex": true,
	"finder": true, "google chrome": true, "google meet": true, "slack": true,
	"synara": true, "todesk": true, "wechat": true, "wezterm": true,
	"yansu": true, "zoom": true,
}

func isDurableLocalActivityAnchor(k string) bool {
	k = normalizeActivityAnchor(k)
	if k == "" {
		return false
	}
	if concreteActivityAnchor(k) != "" {
		return true
	}
	if len([]rune(k)) >= 4 && !activityLocalStopwords[k] {
		return true
	}
	return false
}

var activityLocalStopwords = map[string]bool{
	"activity": true, "timeline": true, "summary": true, "review": true,
	"opened": true, "viewed": true, "clicked": true, "switched": true,
	"document": true, "file": true, "chat": true, "browser": true,
	"proxy": true, "ssh": true, "test": true,
}

func looksLikeRepoSlug(s string) bool {
	if strings.Count(s, "/") != 1 || strings.ContainsAny(s, " \t") {
		return false
	}
	parts := strings.Split(s, "/")
	return len(parts[0]) >= 2 && len(parts[1]) >= 2 && !strings.Contains(parts[1], ".")
}

func looksLikeFilePath(s string) bool {
	if strings.ContainsAny(s, " \t") {
		return false
	}
	if strings.Contains(s, "/") && strings.Contains(s, ".") {
		return true
	}
	for _, suffix := range []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".md", ".json", ".yaml", ".yml", ".sql", ".css", ".html", ".doc", ".docx", ".pdf", ".ppt", ".pptx", ".xls", ".xlsx", ".vbs", ".exe", ".sh"} {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

func normalizeFileActivityAnchor(s string) string {
	s = strings.TrimSpace(strings.Trim(s, " \t\r\n\"'()[]{}<>,.;"))
	for _, prefix := range []string{"只读]", "[只读]", "readonly-", "read-only-", "read only "} {
		s = strings.TrimPrefix(s, prefix)
	}
	if idx := strings.LastIndexAny(s, `/\`); idx >= 0 && idx+1 < len(s) {
		s = s[idx+1:]
	}
	return s
}

func looksLikeDomain(s string) bool {
	if strings.ContainsAny(s, " \t/") || !strings.Contains(s, ".") {
		return false
	}
	for _, suffix := range []string{".com", ".ai", ".dev", ".io", ".org", ".net", ".cn"} {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

func mapsIntersect(a, b map[string]bool) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

func lastString(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[len(items)-1]
}

func activityCategoryForRunSummary(summary ActivityTimelineGroupSummary) string {
	switch strings.ToLower(strings.TrimSpace(summary.ActivityType)) {
	case "coding", "writing", "design":
		return "build"
	case "communication":
		return "connect"
	case "research", "browsing":
		return "explore"
	case "ops", "system":
		return "sustain"
	default:
		return ""
	}
}

func composeSegmentDescription(first, last ActivityTimelineGroupSummary) string {
	firstDesc := strings.TrimSpace(first.Description)
	lastTitle := strings.TrimSpace(last.Title)
	if firstDesc == "" {
		return strings.TrimSpace(last.Description)
	}
	if lastTitle == "" || strings.EqualFold(strings.TrimSpace(first.Title), lastTitle) {
		return firstDesc
	}
	return firstDesc + " Latest: " + lastTitle + "."
}

// A paused/off observer must not send queued evidence to the composer. The
// sweep still runs: quiet-time lifecycle changes do not consume new evidence.
func (c *ActivityTriager) activityJoinsEnabled() bool {
	return c.app == nil || (!c.app.IsClosedEyesModeEnabled() && c.app.screenWatchingEnabled())
}
