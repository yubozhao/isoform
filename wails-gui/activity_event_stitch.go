package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"yansu-agent/agent"
)

// logSemanticEventError treats model echoes, recovered panic values and storage
// error paths as private content. Stage must be a fixed operation kind.
func logSemanticEventError(stage, id string, err error) {
	log.Printf("[semantic-events] stage=%s id=%s failed: %s", stage, id, activityPrivateLogField("error", err.Error()))
}

// featureSemanticEvents gates the legacy per-session semantic-event pipeline
// (extraction + coherence + stitch). RETIRED: the Activities tab is now
// produced by the layer-3 activity composer (featureActivityComposer,
// activity_compose.go), which groups per-app timeline runs instead of
// re-deriving from raw observations. Left here (off) for quick rollback; the
// extraction/stitch/coherence code can be deleted in a follow-up. Flipping it
// back on has zero impact on the four triage outputs (memory / knowledge /
// automation / handoff) — it only writes activity_semantic_events, which the
// frontend no longer reads.
const featureSemanticEvents = false

// featurePeopleVision keeps the WeChat VLM sender-attribution pass alive
// independent of featureSemanticEvents. WeChat exposes no AX/OCR sender text,
// so describeWeChatScreenshot + recordWeChatSenderSightings is its SOLE path
// onto the Who you know page (every other chat app feeds People through the AX/OCR
// labeler in buildGroupSummary, which the main triage loop still runs).
// Retiring semantic events disabled this pass as collateral damage, silently
// dropping every WeChat contact; this flag rides the same semantic-pending
// session track to record sightings without paying for event extraction.
const featurePeopleVision = true

// semanticEventsEnabledKey backs the "Events" toggle in Settings → Activity
// (triage outputs). It shares the triage_enable_* key convention so it groups
// with the other outputs in the UI, even though event generation runs on its
// own loop rather than the triage step. Kept in sync with TRIAGE_FEATURE_KEYS
// in components/settings-page.tsx.
const semanticEventsEnabledKey = "triage_enable_events"

// semanticEventsEnabled reports whether the user has the Events output on.
// Missing/empty defaults to true (parity with loadTriageFeatureFlags and the
// frontend); a store error is treated as enabled — a transient hiccup must not
// silently stop event generation.
func (c *ActivityTriager) semanticEventsEnabled(ctx context.Context) bool {
	if c.app == nil || c.app.store == nil {
		return true
	}
	v, err := c.app.store.GetUIState(ctx, semanticEventsEnabledKey)
	if err != nil || v == "" {
		return true
	}
	return v != "false"
}

const (
	// maxThumbnails: representative snapshots kept per event for the P2 card.
	maxThumbnails = 4
	// maxTranscriptRunes caps how much ASR text feeds a meeting observation.
	maxTranscriptRunes = 8000
)

// semanticSourceRefs is the many-to-many link persisted in source_refs (§2).
// SnapshotApps is index-aligned with SnapshotIDs (resolved RealApp per frame) so
// cover-frame selection can prefer the event's dominant app; it stays empty for
// legacy rows, in which case thumbnail selection falls back to chronological order.
type semanticSourceRefs struct {
	SnapshotIDs  []int64  `json:"snapshot_ids"`
	SnapshotApps []string `json:"snapshot_apps,omitempty"`
	SessionIDs   []string `json:"session_ids"`
}

// activityStore returns the activity store or nil when monitoring is off.
func (c *ActivityTriager) activityStore() *ActivityStore {
	if c.app == nil || c.app.activity == nil {
		return nil
	}
	return c.app.activity.store
}

// semanticCheckInterval is the independent cadence for the semantic-event pass,
// separate from the hourly triage batch — each extraction is cheaper than full triage.
const semanticCheckInterval = 2 * time.Minute

// runSemanticLoop drives the semantic-event pass on its own goroutine + ticker,
// fully decoupled from triage evaluate()/runMu so a slow main triage can never
// starve it. Exits when ctx is cancelled.
func (c *ActivityTriager) runSemanticLoop(ctx context.Context) {
	c.runSemanticPassGuarded(ctx)
	ticker := time.NewTicker(semanticCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runSemanticPassGuarded(ctx)
		}
	}
}

// runSemanticPassGuarded runs one semantic pass under semanticMu (non-blocking —
// a still-running pass means this tick is skipped) and settles stale events.
func (c *ActivityTriager) runSemanticPassGuarded(ctx context.Context) {
	if !c.semanticMu.TryLock() {
		return
	}
	defer c.semanticMu.Unlock()
	changed := c.runSemanticPass(ctx)
	c.settleStaleSemanticEvents(time.Now().UnixMilli())
	// Notify the UI so the Events tab live-refreshes while open, instead of only
	// picking up new events on reopen. Emit only when this pass actually produced
	// or merged events.
	if changed && c.app != nil && c.app.ctx != nil {
		c.app.emitBrowserEvent("activity:semantic-events-updated")
	}
}

// runSemanticPass extracts semantic events on an INDEPENDENT status track. It
// pulls sessions whose semantic_status is 'pending' (not coupled to triage_status
// or triage cooldown), groups them, and extracts — so a stuck or timing-out main
// triage can never starve the event layer. Each group is marked done on success
// (including zero events) and left pending on LLM failure to retry next pass.
func (c *ActivityTriager) runSemanticPass(ctx context.Context) (changed bool) {
	if !featureSemanticEvents && !featurePeopleVision {
		return
	}
	store := c.activityStore()
	if store == nil {
		return
	}
	sessions, err := store.GetSemanticPendingSessions()
	if err != nil {
		logSemanticEventError("load_pending", "", err)
		return
	}
	if len(sessions) == 0 {
		return
	}
	// User turned the Events output off: skip generation, but mark these pending
	// sessions done so they are NOT re-processed if the toggle is flipped back on
	// later. Forward-looking, matching the triage toggles — disabling saves quota,
	// it does not defer work for a later catch-up. The toggle only governs event
	// extraction; when semantic events are retired (flag off) we fall through so
	// the per-session loop still records WeChat People sightings (featurePeopleVision).
	if featureSemanticEvents && !c.semanticEventsEnabled(ctx) {
		ids := make([]string, 0, len(sessions))
		for _, sess := range sessions {
			ids = append(ids, sess.ID)
		}
		if err := store.MarkSessionsSemanticDone(ids); err != nil {
			logSemanticEventError("mark_disabled_done", "", err)
		}
		return
	}
	// Per-session extraction: each session is its own small unit, so the prompt
	// never balloons and never times out. Cross-session/app/time grouping is the
	// stitch layer's job (coarse filter + LLM confirm), not the extraction LLM's.
	for _, sess := range sessions {
		select {
		case <-ctx.Done():
			return
		default:
		}
		g := sessionGroup{Sessions: []ActivitySessionRow{sess}}
		// Backoff gate: a session whose previous extraction failed transiently
		// waits out its cooldown instead of retrying every 2-minute tick.
		if rem := c.semanticCooldown.remaining(g, time.Now()); rem > 0 {
			continue
		}
		if c.runSemanticEventExtraction(ctx, g) {
			changed = true
		}
	}
	return changed
}

// markGroupSemanticDone flips every session in the group to semantic_status=done.
func (c *ActivityTriager) markGroupSemanticDone(g sessionGroup) {
	// Done is terminal — the sessions never return as semantic-pending, so
	// drop any cooldown entries to keep the map from accumulating.
	c.semanticCooldown.clear(g)
	store := c.activityStore()
	if store == nil {
		return
	}
	ids := make([]string, 0, len(g.Sessions))
	for _, sess := range g.Sessions {
		ids = append(ids, sess.ID)
	}
	if err := store.MarkSessionsSemanticDone(ids); err != nil {
		logSemanticEventError("mark_group_done", g.primaryID(), err)
	}
}

// runSemanticEventExtraction is the fifth triage bypass: preprocess a group's
// observations, extract semantic events, and stitch them into the store. It
// never returns an error — a failure here must not disturb the four existing
// triage outputs. (§8)
func (c *ActivityTriager) runSemanticEventExtraction(ctx context.Context, g sessionGroup) (changed bool) {
	if !featureSemanticEvents && !featurePeopleVision {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logSemanticEventError("panic", g.primaryID(), fmt.Errorf("%v", r))
		}
	}()

	store := c.activityStore()
	if store == nil || !c.triageGroupAllowsModelRead(g) {
		return
	}

	seq := c.buildGroupObservationSequence(g)
	if len(seq.Observations) == 0 {
		c.markGroupSemanticDone(g) // nothing to extract — close out so it isn't retried forever
		return
	}
	// WeChat exposes almost nothing to AX/OCR, so read its chat area from the
	// screenshot with a VLM before entity-linking and extraction run on the body.
	// This also records WeChat People sightings (its sole path onto the People
	// page), so it runs even when semantic events are retired.
	if featurePeopleVision {
		c.enrichWeChatObservations(ctx, &seq)
	}
	// People-only mode: sightings are recorded above; the legacy event
	// extraction / coherence / stitch below is retired. Close the group out so
	// each session's WeChat frames are VLM-read exactly once.
	if !featureSemanticEvents {
		c.markGroupSemanticDone(g)
		return
	}
	c.enrichWeakObservations(ctx, &seq)
	annotateEntities(&seq)

	if !c.triageGroupAllowsModelRead(g) {
		return
	}
	events, err := c.extractSemanticEvents(ctx, seq)
	if err != nil {
		// Permanent rejections (e.g. HTTP 400 "high risk" content moderation on
		// proxy/VPN screens) fail identically on every retry — close the group out
		// so it doesn't wedge the extraction loop forever. Transient failures
		// (network, 5xx, rate limit) stay pending for the next pass to retry.
		if isPermanentExtractionFailure(err) {
			logSemanticEventError("extraction_rejected", g.primaryID(), err)
			c.markGroupSemanticDone(g)
			return
		}
		c.semanticCooldown.markFailed(g, time.Now())
		logSemanticEventError("extraction_retry", g.primaryID(), err)
		return
	}
	// Coherence gate: split any event whose observations are not one piece of work
	// (an LLM over-merge), before it flows into stitch. No-op for the common coherent
	// case. A transient verify failure returns an error so the group stays pending
	// for retry (never markDone) — mirrors the extractSemanticEvents failure policy.
	// (project_llm_generate_verify_regenerate)
	budget := &coherenceBudget{remaining: maxCoherenceVerifyPerGroup}
	events, err = c.coherentEvents(ctx, events, seq, budget)
	if err != nil {
		if isPermanentExtractionFailure(err) {
			logSemanticEventError("coherence_rejected", g.primaryID(), err)
			c.markGroupSemanticDone(g)
			return
		}
		c.semanticCooldown.markFailed(g, time.Now())
		logSemanticEventError("coherence_retry", g.primaryID(), err)
		return
	}
	if len(events) > 0 {
		c.stitchSemanticEvents(ctx, events, seq, time.Now().UnixMilli())
		changed = true
	}
	// Success (including zero events) — mark done so each session runs once.
	c.markGroupSemanticDone(g)
	return changed
}

// maxCoherenceVerifyPerGroup bounds how many coherence-verify LLM calls a single
// extraction pass (one session group) may spend, so a day full of over-merged
// events cannot fan out unbounded serial 3-minute calls and starve triage. A
// coherent group spends ZERO; only cross-app events without a repo/pr link cost
// one. When the budget is exhausted, remaining incoherent events fall back to a
// deterministic split (no LLM), logged — never a silent cap and never a re-merge.
const maxCoherenceVerifyPerGroup = 8

// coherenceBudget is the per-group verify-call allowance, shared across all events
// in one extraction pass so the total LLM spend is bounded (not just per-event).
type coherenceBudget struct {
	remaining int
}

func (b *coherenceBudget) tryUse() bool {
	if b.remaining <= 0 {
		return false
	}
	b.remaining--
	return true
}

// coherenceVerifyFn judges a sub-sequence's coherence: it returns the events the
// observations should actually form (one event => coherent; many => an over-merge
// to split). Indices are sub-sequence local. Injected so the orchestration in
// applyCoherence is unit-testable without an LLM.
type coherenceVerifyFn func(ctx context.Context, sub ObservationSequence) ([]extractedEvent, error)

// coherentEvents runs the coherence gate over a pass's events using the real LLM
// verifier. See applyCoherence for the orchestration.
func (c *ActivityTriager) coherentEvents(ctx context.Context, events []extractedEvent, seq ObservationSequence, budget *coherenceBudget) ([]extractedEvent, error) {
	return applyCoherence(ctx, events, seq, budget, c.coherenceVerify)
}

// applyCoherence is the pure orchestration of the coherence gate. For each event:
//   - coherenceClusters says coherent (single app, or repo/pr links all) -> keep.
//   - otherwise, if budget remains, ask the verifier to repartition the event's
//     observations by content; its events replace the original (indices remapped).
//   - if budget is exhausted, split deterministically by cluster (no LLM).
//
// A verifier error aborts the whole pass (returned up) so the group stays pending
// for retry rather than letting an over-merge through (#2). The strong-entity
// safety net (#4) restores any repo/pr cluster the verifier dropped entirely.
func applyCoherence(ctx context.Context, events []extractedEvent, seq ObservationSequence, budget *coherenceBudget, verify coherenceVerifyFn) ([]extractedEvent, error) {
	out := make([]extractedEvent, 0, len(events))
	for _, ev := range events {
		clusters, needsVerify := coherenceClusters(ev, seq)
		if !needsVerify {
			out = append(out, ev) // coherent -> keep as-is
			continue
		}
		if !budget.tryUse() {
			log.Printf("[semantic-events] coherence verify budget exhausted; deterministic fallback title_bytes=%d clusters=%d", len(ev.Title), len(clusters))
			out = append(out, deterministicFallback(ev, seq)...)
			continue
		}

		inputIdxs := flattenClusters(clusters)
		sub, mapping := subSequence(seq, inputIdxs)
		verified, err := verify(ctx, sub)
		if err != nil {
			// Deterministic-fallback errors (for THIS event only, so one bad event
			// can't discard the group's other already-extracted events):
			//   - permanent (content moderation etc.) recurs identically on retry;
			//   - incomplete coverage means the verifier's partition is untrustworthy,
			//     so we conservatively split rather than trust a partial answer.
			// Transient errors abort the pass so the group stays pending for retry.
			if isPermanentExtractionFailure(err) || errors.Is(err, errVerifyIncomplete) {
				log.Printf("[semantic-events] coherence verify unusable title_bytes=%d; deterministic fallback: %s", len(ev.Title), activityPrivateLogField("error", err.Error()))
				out = append(out, deterministicFallback(ev, seq)...)
				continue
			}
			return nil, fmt.Errorf("coherence verify title_bytes=%d: %w", len(ev.Title), err)
		}
		for i := range verified {
			verified[i].SourceIndices = remapIndices(verified[i].SourceIndices, mapping)
		}
		verified = dedupEventIndices(verified)
		verified = restoreUncoveredStrongObservations(ev, verified, inputIdxs, seq)
		log.Printf("[semantic-events] coherence verified title_bytes=%d clusters=%d events=%d", len(ev.Title), len(clusters), len(verified))
		out = append(out, verified...)
	}
	return out, nil
}

// deterministicFallback is the no-LLM result when the verifier cannot run (budget
// exhausted or permanently rejected). It must NEVER pass the original (possibly
// over-merged) event through — that would make "repo is not proof of same work"
// hold only when the verifier succeeds. Instead it conservatively splits the
// event's observations into runs of the SAME app contiguous in time: an event here
// always spans >1 app (needsVerify requires it), so this always breaks the cross-
// app merge apart, never re-merges, and loses no observation.
func deterministicFallback(ev extractedEvent, seq ObservationSequence) []extractedEvent {
	idxs := make([]int, 0, len(ev.SourceIndices))
	for _, idx := range ev.SourceIndices {
		if idx >= 0 && idx < len(seq.Observations) {
			idxs = append(idxs, idx)
		}
	}
	sort.SliceStable(idxs, func(i, j int) bool {
		return seq.Observations[idxs[i]].StartMs < seq.Observations[idxs[j]].StartMs
	})

	var runs [][]int
	lastApp := ""
	for _, idx := range idxs {
		app := strings.ToLower(strings.TrimSpace(seq.Observations[idx].RealApp))
		if len(runs) == 0 || app != lastApp {
			runs = append(runs, []int{idx})
		} else {
			runs[len(runs)-1] = append(runs[len(runs)-1], idx)
		}
		lastApp = app
	}
	return deterministicSplit(ev, runs, seq)
}

// dedupEventIndices enforces that each source observation belongs to at most ONE
// post-verify event (assigned to the first event that claims it), dropping any
// event left empty. Prevents the verifier double-counting an observation across
// the split events.
func dedupEventIndices(events []extractedEvent) []extractedEvent {
	seen := map[int]bool{}
	out := events[:0]
	for _, e := range events {
		kept := e.SourceIndices[:0]
		for _, idx := range e.SourceIndices {
			if !seen[idx] {
				seen[idx] = true
				kept = append(kept, idx)
			}
		}
		e.SourceIndices = kept
		if len(e.SourceIndices) > 0 {
			out = append(out, e)
		}
	}
	return out
}

// deterministicSplit is the no-LLM fallback (budget exhausted): emit one event per
// repo/pr cluster with a neutral title derived from the cluster's own entity/app,
// never the over-merged original title. Conservative but loses no data and never
// re-merges unrelated work.
func deterministicSplit(ev extractedEvent, clusters [][]int, seq ObservationSequence) []extractedEvent {
	out := make([]extractedEvent, 0, len(clusters))
	for _, cl := range clusters {
		ents := clusterEntities(cl, seq)
		out = append(out, extractedEvent{
			Title:         degradedClusterTitle(cl, ents, seq),
			Description:   ev.Description,
			Category:      ev.Category,
			Entities:      ents,
			SourceIndices: append([]int(nil), cl...),
			TaskWorthy:    ev.TaskWorthy,
		})
	}
	return out
}

// restoreUncoveredStrongObservations is the safety net against verifier data loss.
// Any INPUT observation that carries a repo/pr identity (deterministic proof of
// real work) but is covered by NO post-verify event is restored — at the single-
// observation granularity, so a PARTIALLY dropped repo cluster is caught too, not
// only a wholly dropped one. Restored observations are grouped by their repo/pr key
// so distinct repos become distinct fallback events.
//
// Observations WITHOUT a repo/pr identity that the verifier dropped are trusted as
// the verifier's noise judgement (a momentary glance / contentless frame) and left
// dropped — the same boundary the extraction stage already draws. (A stricter
// completeness guarantee would require the verifier to return explicit
// dropped_indices; not done here to keep the verifier contract simple.)
func restoreUncoveredStrongObservations(ev extractedEvent, verified []extractedEvent, inputIdxs []int, seq ObservationSequence) []extractedEvent {
	covered := map[int]bool{}
	for _, v := range verified {
		for _, idx := range v.SourceIndices {
			covered[idx] = true
		}
	}
	byKey := map[string][]int{}
	var keyOrder []string
	for _, idx := range inputIdxs {
		if covered[idx] || idx < 0 || idx >= len(seq.Observations) {
			continue
		}
		var key string
		for _, e := range seq.Observations[idx].Entities {
			if k := highSpecificityKey(e); k != "" {
				key = k
				break
			}
		}
		if key == "" {
			continue // no repo/pr provenance -> trust verifier's drop (noise)
		}
		if _, ok := byKey[key]; !ok {
			keyOrder = append(keyOrder, key)
		}
		byKey[key] = append(byKey[key], idx)
	}
	if len(keyOrder) == 0 {
		return verified
	}
	var missing [][]int
	for _, key := range keyOrder {
		missing = append(missing, byKey[key])
	}
	log.Printf("[semantic-events] coherence safety net: restoring %d repo/pr observation group(s) dropped by verifier title_bytes=%d", len(missing), len(ev.Title))
	return append(verified, deterministicSplit(ev, missing, seq)...)
}

// flattenClusters concatenates cluster index slices into one ascending slice.
func flattenClusters(clusters [][]int) []int {
	var out []int
	for _, cl := range clusters {
		out = append(out, cl...)
	}
	sort.Ints(out)
	return out
}

// clusterEntities collects the deduped entities of a cluster's observations.
func clusterEntities(cl []int, seq ObservationSequence) []Entity {
	var ents []Entity
	for _, idx := range cl {
		if idx >= 0 && idx < len(seq.Observations) {
			ents = append(ents, seq.Observations[idx].Entities...)
		}
	}
	return dedupEntities(ents)
}

// degradedClusterTitle builds a neutral fallback title from a cluster's strongest
// entity, else its primary app — used only by deterministicSplit.
func degradedClusterTitle(cl []int, ents []Entity, seq ObservationSequence) string {
	for _, e := range ents {
		if highSpecificityKey(e) != "" {
			return "Activity on " + e.Name
		}
	}
	for _, e := range ents {
		if e.Type == entityProject || e.Type == entityTopic {
			return "Activity on " + e.Name
		}
	}
	for _, idx := range cl {
		if idx >= 0 && idx < len(seq.Observations) {
			if app := strings.TrimSpace(seq.Observations[idx].RealApp); app != "" && app != unknownAppName {
				return "Activity in " + app
			}
		}
	}
	return "Activity"
}

// subSequence builds a standalone ObservationSequence from a subset of source
// indices, contiguously reindexed (0-based) so the verify prompt and
// sanitizeExtractedEvents see a valid index range. It returns mapping[subIdx] =
// originalIdx so verified source indices can be projected back.
func subSequence(seq ObservationSequence, indices []int) (ObservationSequence, []int) {
	sub := ObservationSequence{}
	mapping := make([]int, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(seq.Observations) {
			continue
		}
		o := seq.Observations[idx]
		o.Index = len(mapping)
		mapping = append(mapping, idx)
		sub.Observations = append(sub.Observations, o)
		// Initialize the span unconditionally on the first valid observation rather
		// than using a 0 sentinel: an observation with a legitimate StartMs of 0
		// (or negative EndMs) must not be treated as "unset" and overwritten by a
		// later, larger StartMs. Mirrors buildGroupObservationSequence's i==0 guard.
		if len(mapping) == 1 {
			sub.StartMs = o.StartMs
			sub.EndMs = o.EndMs
		} else {
			if o.StartMs < sub.StartMs {
				sub.StartMs = o.StartMs
			}
			if o.EndMs > sub.EndMs {
				sub.EndMs = o.EndMs
			}
		}
	}
	return sub, mapping
}

// remapIndices projects sub-sequence indices back onto the original sequence via
// the mapping from subSequence, dropping any out-of-range index defensively.
func remapIndices(subIdx []int, mapping []int) []int {
	out := make([]int, 0, len(subIdx))
	for _, i := range subIdx {
		if i >= 0 && i < len(mapping) {
			out = append(out, mapping[i])
		}
	}
	return out
}

// isPermanentExtractionFailure reports whether an extraction error will recur
// identically on retry, so the group should be skipped rather than retried
// forever. HTTP 400 / invalid_request / content-moderation ("high risk")
// rejections are permanent for the same content; 5xx, rate limits, and network
// errors are transient and worth retrying.
func isPermanentExtractionFailure(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "HTTP 400") ||
		strings.Contains(s, "invalid_request") ||
		strings.Contains(s, "high risk")
}

// buildGroupObservationSequence preprocesses every session in a group and
// concatenates the results into one chronologically reindexed sequence. Cross-
// session work inside the group is later joined by the extraction LLM; cross-
// group work is joined by stitching against open events (§8).
func (c *ActivityTriager) buildGroupObservationSequence(g sessionGroup) ObservationSequence {
	store := c.activityStore()
	if store == nil {
		return ObservationSequence{}
	}

	var all []Observation
	for _, sess := range g.Sessions {
		snaps, err := store.GetSessionSnapshots(sess.ID)
		if err != nil {
			logSemanticEventError("load_snapshots", sess.ID, err)
			continue
		}
		ocrMap := c.ocrBySnapshot(sess.ID, snaps)
		var transcript string
		var sourceIDs []string
		if c.app != nil && sess.TranscriptPath != "" {
			if text, dependencies, err := c.app.readMeetingTranscript(sess); err == nil {
				transcript = trimTranscriptText(text)
				for _, source := range dependencies {
					sourceIDs = append(sourceIDs, source.SessionID)
				}
			}
		}
		sub := buildObservationSequence(sess, snaps, ocrMap, transcript)
		for i := range sub.Observations {
			if sub.Observations[i].Source == "meeting_transcript" {
				sub.Observations[i].SourceSessionIDs = sourceIDs
			}
		}
		all = append(all, sub.Observations...)
	}

	sort.SliceStable(all, func(i, j int) bool { return all[i].StartMs < all[j].StartMs })
	seq := ObservationSequence{Observations: all}
	for i := range all {
		all[i].Index = i
		if i == 0 || all[i].StartMs < seq.StartMs {
			seq.StartMs = all[i].StartMs
		}
		if all[i].EndMs > seq.EndMs {
			seq.EndMs = all[i].EndMs
		}
	}
	return seq
}

func (c *ActivityTriager) ocrBySnapshot(sessionID string, snaps []ActivitySnapshotRow) map[int64]string {
	store := c.activityStore()
	if store == nil {
		return nil
	}
	m := make(map[int64]string, len(snaps))
	for _, sn := range snaps {
		if txt, err := store.GetOCRTextBySnapshot(sessionID, sn.ID); err == nil {
			if t := strings.TrimSpace(txt); t != "" {
				m[sn.ID] = t
			}
		}
	}
	return m
}

// stitchSemanticEvents upserts each extracted event using the two-stage merge:
// day boundary -> pure coarse filter -> LLM "same specific piece of work?"
// confirm. Otherwise it inserts a new open event. Events created within the same
// batch become stitch targets for later events.
func (c *ActivityTriager) stitchSemanticEvents(ctx context.Context, events []extractedEvent, seq ObservationSequence, nowMs int64) {
	store := c.activityStore()
	if store == nil {
		return
	}
	open, err := store.GetOpenSemanticEvents()
	if err != nil {
		logSemanticEventError("load_open", "", err)
		return
	}
	// Day boundary: an event never spans days. Only TODAY's open events are
	// stitch candidates; older ones are sealed by the settle pass.
	dayStart := startOfDayLocal(nowMs)
	today := open[:0]
	for _, e := range open {
		if e.StartMs >= dayStart {
			today = append(today, e)
		}
	}
	open = today

	for _, ev := range events {
		cand := candidateFromExtracted(ev, seq)
		// No resolved source app means every source observation's app inference
		// failed — the event has no reliable provenance and is usually an LLM
		// hallucination (e.g. a "Debug Kubernetes" event with apps=[]). Drop it.
		if len(cand.Apps) == 0 {
			continue
		}
		matchIdx := c.findSameEvent(ctx, open, ev, cand)
		if matchIdx >= 0 {
			mergeExtractedInto(&open[matchIdx], ev, cand, seq, nowMs)
			if err := store.UpdateSemanticEvent(open[matchIdx]); err != nil {
				logSemanticEventError("update", open[matchIdx].ID, err)
			}
			continue
		}
		row := buildSemanticEventRow(ev, cand, seq, nowMs)
		if err := store.InsertSemanticEvent(row); err != nil {
			logSemanticEventError("insert", row.ID, err)
			continue
		}
		open = append(open, row)
	}
}

// maxSameEventConfirms caps how many LLM "same event?" checks one new event can
// trigger, so a busy day with many candidates can't fan out unbounded calls.
const maxSameEventConfirms = 3

// findSameEvent applies the two-stage merge decision: a pure-function COARSE
// filter (shared entity/subject — zero LLM, skips most pairs), then an LLM
// CONFIRM on the few candidates ("same specific piece of work?"). Returns the
// index of the matching open event, or -1 to create a new one.
func (c *ActivityTriager) findSameEvent(ctx context.Context, open []SemanticEventRow, ev extractedEvent, cand eventCandidate) int {
	confirms := 0
	for i := range open {
		if shareable, _ := gateMerge(candidateFromRow(open[i]), cand); !shareable {
			continue // coarse filter: no shared entity/subject -> not a candidate (0 LLM)
		}
		if confirms >= maxSameEventConfirms {
			break
		}
		confirms++
		if c.confirmSameEvent(ctx, open[i], ev) {
			return i
		}
	}
	return -1
}

// confirmSameEvent asks the light model whether a new event is the SAME specific
// piece of work as an existing open event (same feature/bug/task/topic), not
// merely related (same repo, different bug = NO). Conservative: any failure or
// non-"yes" answer means "not the same" (create a new event).
func (c *ActivityTriager) confirmSameEvent(ctx context.Context, existing SemanticEventRow, newEv extractedEvent) bool {
	c.app.mu.Lock()
	runner := c.app.runner
	c.app.mu.Unlock()
	if runner == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var result strings.Builder
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		AutoApproveTools:  true,
		Prompt:            buildSameEventPrompt(existing, newEv),
		Model:             c.app.lightTaskModel(),
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(e *agent.StreamEvent) error {
		if e.Type == "assistant" {
			result.WriteString(e.Content)
		}
		return nil
	})
	if err != nil {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(stripCodeFences(result.String())))
	return strings.HasPrefix(ans, "yes")
}

func buildSameEventPrompt(existing SemanticEventRow, newEv extractedEvent) string {
	return fmt.Sprintf(`Decide whether these two desktop-activity events are THE SAME specific piece of work — the same feature, bug, task, conversation, or topic — NOT merely related. Same repo but a DIFFERENT bug/feature is NOT the same. Different topics is NOT the same.

Event A (existing):
  title: %s
  description: %s
  entities: %s

Event B (new):
  title: %s
  description: %s
  entities: %s

Titles can be imprecise — if both events clearly concern the SAME specific entity
(same repo / PR / project / topic, e.g. both about "DJI Pocket 2"), treat them as
the same even when the titles differ.

Answer with ONLY "yes" (the same specific piece of work) or "no". No explanation.`,
		existing.Title, existing.Description, renderEntities(parseEntityList(existing.Entities)),
		newEv.Title, newEv.Description, renderEntities(newEv.Entities))
}

// startOfDayLocal returns local midnight (00:00) for the given epoch-ms instant.
func startOfDayLocal(ms int64) int64 {
	t := time.UnixMilli(ms)
	y, mo, d := t.Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, t.Location()).UnixMilli()
}

// settleStaleSemanticEvents closes out every open event that did not start today
// — events never span days.
func (c *ActivityTriager) settleStaleSemanticEvents(nowMs int64) {
	store := c.activityStore()
	if store == nil {
		return
	}
	if n, err := store.SettleEventsBeforeDay(startOfDayLocal(nowMs), nowMs); err != nil {
		logSemanticEventError("settle", "", err)
	} else if n > 0 {
		log.Printf("[semantic-events] settled %d cross-day events", n)
	}
}

// candidateFromExtracted builds the gating view from an extracted event: apps
// and time come from the referenced observations; entities merge the LLM's
// entities with the rule-extracted ones on those observations.
func candidateFromExtracted(ev extractedEvent, seq ObservationSequence) eventCandidate {
	cand := eventCandidate{Entities: ev.Entities}
	appSeen := map[string]bool{}
	for _, idx := range ev.SourceIndices {
		if idx < 0 || idx >= len(seq.Observations) {
			continue
		}
		o := seq.Observations[idx]
		// Skip the "?" placeholder (app inference failed): it is not a real app
		// and would inflate the cross-app apps list and stats.
		if o.RealApp != "" && o.RealApp != unknownAppName && !appSeen[strings.ToLower(o.RealApp)] {
			appSeen[strings.ToLower(o.RealApp)] = true
			cand.Apps = append(cand.Apps, o.RealApp)
		}
		cand.Entities = append(cand.Entities, o.Entities...)
		if cand.StartMs == 0 || o.StartMs < cand.StartMs {
			cand.StartMs = o.StartMs
		}
		if o.EndMs > cand.EndMs {
			cand.EndMs = o.EndMs
		}
	}
	cand.Entities = dedupEntities(cand.Entities)
	return cand
}

func candidateFromRow(row SemanticEventRow) eventCandidate {
	return eventCandidate{
		Apps:     parseStringList(row.Apps),
		Entities: parseEntityList(row.Entities),
		StartMs:  row.StartMs,
		EndMs:    row.EndMs,
	}
}

func buildSemanticEventRow(ev extractedEvent, cand eventCandidate, seq ObservationSequence, nowMs int64) SemanticEventRow {
	refs := collectSourceRefs(ev.SourceIndices, seq)
	category := normalizeCategory(ev.Category)
	return SemanticEventRow{
		ID:               uuid.New().String(),
		Title:            ev.Title,
		Description:      ev.Description,
		Category:         category,
		Entities:         marshalJSON(cand.Entities),
		Keywords:         marshalJSON(displayKeywordsFor(ev, cand.Entities)),
		Apps:             marshalJSON(cand.Apps),
		StartMs:          cand.StartMs,
		EndMs:            cand.EndMs,
		SourceRefs:       marshalJSON(refs),
		ThumbnailRefs:    marshalJSON(selectThumbnails(refs)),
		TaskWorthy:       ev.TaskWorthy,
		CategoryDotColor: semanticCategoryDotColor(category),
		Status:           "open",
		CreatedAt:        nowMs,
		UpdatedAt:        nowMs,
	}
}

// mergeExtractedInto folds a new extracted event into an existing open row:
// union the observation refs/apps/entities and stretch the time span (which may
// stay non-contiguous). Title/description are kept stable to avoid churn (§7).
func mergeExtractedInto(row *SemanticEventRow, ev extractedEvent, cand eventCandidate, seq ObservationSequence, nowMs int64) {
	newRefs := collectSourceRefs(ev.SourceIndices, seq)
	merged := unionSourceRefs(parseSourceRefs(row.SourceRefs), newRefs)
	row.SourceRefs = marshalJSON(merged)
	row.Apps = marshalJSON(unionStrings(parseStringList(row.Apps), cand.Apps))
	row.Entities = marshalJSON(dedupEntities(append(parseEntityList(row.Entities), cand.Entities...)))
	// Display keywords stay STABLE across merges (like title/description) — this is
	// the deliberate fix for the monotonic tag accumulation that bloated cards.
	// Only backfill when the row has none (legacy rows predating this column).
	if len(parseStringList(row.Keywords)) == 0 {
		row.Keywords = marshalJSON(displayKeywordsFor(ev, parseEntityList(row.Entities)))
	}
	row.ThumbnailRefs = marshalJSON(selectThumbnails(merged))
	if cand.StartMs > 0 && cand.StartMs < row.StartMs {
		row.StartMs = cand.StartMs
	}
	if cand.EndMs > row.EndMs {
		row.EndMs = cand.EndMs
	}
	if ev.TaskWorthy {
		row.TaskWorthy = true
	}
	row.UpdatedAt = nowMs
}

func collectSourceRefs(indices []int, seq ObservationSequence) semanticSourceRefs {
	var refs semanticSourceRefs
	snapSeen := map[int64]bool{}
	sessSeen := map[string]bool{}
	for _, idx := range indices {
		if idx < 0 || idx >= len(seq.Observations) {
			continue
		}
		o := seq.Observations[idx]
		for _, sid := range o.SnapshotIDs {
			if !snapSeen[sid] {
				snapSeen[sid] = true
				refs.SnapshotIDs = append(refs.SnapshotIDs, sid)
				refs.SnapshotApps = append(refs.SnapshotApps, o.RealApp)
			}
		}
		for _, id := range append([]string{o.SessionID}, o.SourceSessionIDs...) {
			if id != "" && !sessSeen[id] {
				sessSeen[id] = true
				refs.SessionIDs = append(refs.SessionIDs, id)
			}
		}
	}
	return refs
}

func topThumbnails(snapshotIDs []int64) []int64 {
	if len(snapshotIDs) <= maxThumbnails {
		return snapshotIDs
	}
	return snapshotIDs[:maxThumbnails]
}

// selectThumbnails picks an event's cover frames. It keeps the chronological
// frame order but moves the event's dominant-app frames to the front, so the
// cover reflects what the event is actually about (e.g. a Slack huddle) instead
// of a background app that merely happened to be frontmost when the first frame
// was captured — per-window capture grabs the frontmost app's window, which at
// the moment a transient notification fires is often the unrelated app behind it.
// Falls back to chronological order when per-frame app info is absent (legacy
// rows) or no real dominant app exists. Reordering runs at any frame count
// because the cover frame is ids[0] even for short (2-4 frame) events.
func selectThumbnails(refs semanticSourceRefs) []int64 {
	ids := refs.SnapshotIDs
	apps := refs.SnapshotApps
	// Reorder regardless of frame count: the cover frame is always ids[0], so
	// dominant-app reordering matters even when every frame fits within
	// maxThumbnails (no truncation needed). Truncation is left to the trailing
	// topThumbnails call. Fall back to chronological order only when per-frame
	// app info is missing or misaligned (legacy rows).
	if len(apps) != len(ids) {
		return topThumbnails(ids)
	}
	primary := dominantApp(apps)
	if primary == "" {
		return topThumbnails(ids)
	}
	// Stable partition: dominant-app frames first, every other frame keeps its
	// chronological position. Within each group the focus-first order set by
	// orderFocusFirst is preserved.
	match := make([]int64, 0, len(ids))
	rest := make([]int64, 0, len(ids))
	for i, id := range ids {
		if strings.EqualFold(apps[i], primary) {
			match = append(match, id)
		} else {
			rest = append(rest, id)
		}
	}
	return topThumbnails(append(match, rest...))
}

// dominantApp returns the most frequent real app in a per-frame app list,
// ignoring empty and unknown ("?") entries. Ties break toward the app seen first
// so the result is deterministic. The returned value is lower-cased.
func dominantApp(apps []string) string {
	counts := map[string]int{}
	var order []string
	for _, a := range apps {
		k := strings.ToLower(strings.TrimSpace(a))
		if k == "" || k == unknownAppName {
			continue
		}
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
	}
	best, bestN := "", 0
	for _, k := range order {
		if counts[k] > bestN {
			best, bestN = k, counts[k]
		}
	}
	return best
}

// --- JSON / set helpers --------------------------------------------------

func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func parseStringList(s string) []string {
	var out []string
	if strings.TrimSpace(s) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func parseEntityList(s string) []Entity {
	var out []Entity
	if strings.TrimSpace(s) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func parseSourceRefs(s string) semanticSourceRefs {
	var out semanticSourceRefs
	if strings.TrimSpace(s) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func unionStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range append(append([]string{}, a...), b...) {
		k := strings.ToLower(strings.TrimSpace(v))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

func dedupEntities(ents []Entity) []Entity {
	seen := map[string]bool{}
	var out []Entity
	for _, e := range ents {
		k := e.Type + "|" + strings.ToLower(strings.TrimSpace(e.Name))
		if strings.TrimSpace(e.Name) == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

func unionSourceRefs(a, b semanticSourceRefs) semanticSourceRefs {
	out := semanticSourceRefs{}
	// Map each snapshot to its app (first non-empty wins) so the unioned
	// SnapshotApps stays index-aligned with SnapshotIDs even when one side is a
	// legacy ref without per-frame apps.
	appByID := map[int64]string{}
	for _, r := range []semanticSourceRefs{a, b} {
		for i, sid := range r.SnapshotIDs {
			if i < len(r.SnapshotApps) && r.SnapshotApps[i] != "" {
				if _, ok := appByID[sid]; !ok {
					appByID[sid] = r.SnapshotApps[i]
				}
			}
		}
	}
	snapSeen := map[int64]bool{}
	for _, sid := range append(append([]int64{}, a.SnapshotIDs...), b.SnapshotIDs...) {
		if !snapSeen[sid] {
			snapSeen[sid] = true
			out.SnapshotIDs = append(out.SnapshotIDs, sid)
			out.SnapshotApps = append(out.SnapshotApps, appByID[sid])
		}
	}
	sessSeen := map[string]bool{}
	for _, sid := range append(append([]string{}, a.SessionIDs...), b.SessionIDs...) {
		if sid != "" && !sessSeen[sid] {
			sessSeen[sid] = true
			out.SessionIDs = append(out.SessionIDs, sid)
		}
	}
	return out
}

// trimTranscriptText applies the semantic prompt budget after authorization.
func trimTranscriptText(text string) string {
	text = strings.TrimSpace(text)
	r := []rune(text)
	if len(r) > maxTranscriptRunes {
		return string(r[:maxTranscriptRunes])
	}
	return text
}
