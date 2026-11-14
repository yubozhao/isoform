package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ActivityAppUsage is one app's contribution to an activity: its bundle id
// (for native-icon resolution), display name, and summed active time.
type ActivityAppUsage struct {
	AppKey   string `json:"appKey"` // bundle id (or "app:<name>"/"unknown")
	AppName  string `json:"appName"`
	ActiveMs int64  `json:"activeMs"`
}

// ActivityView is the front-end-facing form of one activity bucket: JSON
// columns parsed, apps ranked by dwell time, totals derived.
type ActivityView struct {
	LatestOutcome       *ActivityOutcome    `json:"latestOutcome,omitempty"`
	LatestLink          *ActivityLinkReason `json:"latestLink,omitempty"`
	InProgress          bool                `json:"inProgress"`
	ID                  string              `json:"id"`
	Status              string              `json:"status"`
	Title               string              `json:"title"`
	Description         string              `json:"description"`
	LatestChange        string              `json:"latestChange"`
	Category            string              `json:"category"`
	CategoryColor       string              `json:"categoryColor"`
	Entities            []Entity            `json:"entities"`
	Apps                []ActivityAppUsage  `json:"apps"`     // bundle id + name + time, ranked by dwell desc
	ActiveMs            int64               `json:"activeMs"` // total
	StartMs             int64               `json:"startMs"`
	EndMs               int64               `json:"endMs"`
	RunCount            int                 `json:"runCount"`
	ContinuesActivityID string              `json:"continuesActivityId,omitempty"`
	Thumbnails          []ActivityRunFrame  `json:"thumbnails"`
	// Row actions (activity_row_actions.go): the prepared follow-up line, the
	// open follow-up, the pending reminder's time, whether the user corrected
	// the title or summary, and why actions are withheld ("private",
	// "paused").
	FollowUpText    string            `json:"followUpText,omitempty"`
	FollowUp        *ActivityFollowUp `json:"followUp,omitempty"`
	ReminderAt      int64             `json:"reminderAt,omitempty"`
	Corrected       bool              `json:"corrected,omitempty"`
	ActionsWithheld string            `json:"actionsWithheld,omitempty"` // a flat, representative strip across the activity's runs
}

// ActivityRunFrame is one representative screenshot of a run, with its focus
// rect (normalized 0..1) so the front-end can crop to the focused app region.
type ActivityRunFrame struct {
	Timestamp int64   `json:"timestamp"`
	Path      string  `json:"path"`
	FocusX    float64 `json:"focusX"`
	FocusY    float64 `json:"focusY"`
	FocusW    float64 `json:"focusW"`
	FocusH    float64 `json:"focusH"`
}

// ActivityKeywordFrame is a run frame whose stored text matched an active
// activity entity filter. Visual=true means capture-time OCR saw the term, so
// GetOCRHighlightBoxes can usually draw boxes on the frame.
type ActivityKeywordFrame struct {
	ActivityRunFrame
	Visual bool `json:"visual"`
}

// ActivityRunView is one member run in an activity's drill-down: the per-app run
// with its cached summary (title / description / steps) and its screenshots.
// Screenshots are grouped here, under the run they belong to.
type ActivityRunView struct {
	Link     *ActivityLinkReason           `json:"link,omitempty"`
	ID       string                        `json:"id"`
	AppKey   string                        `json:"appKey"`
	AppName  string                        `json:"appName"`
	StartMs  int64                         `json:"startMs"`
	EndMs    int64                         `json:"endMs"`
	ActiveMs int64                         `json:"activeMs"`
	Summary  *ActivityTimelineGroupSummary `json:"summary,omitempty"`
	Frames   []ActivityRunFrame            `json:"frames"`
}

// maxActivityRunFrames caps the screenshots returned per run in the drill-down.
const maxActivityRunFrames = 8

// maxActivityKeywordFrames caps filter-driven frame promotion so a broad
// entity tag does not flood the Activities list or lightbox.
const maxActivityKeywordFrames = 12

// activityRowToView fills the non-app fields; Apps (with bundle ids) and the
// recomputed total are set by the caller from the run-table breakdown.
func activityRowToView(r ActivityRow) ActivityView {
	var total int64
	for _, ms := range composeParseInt64Map(r.ActiveMsByApp) {
		total += ms
	}
	ents := parseEntityList(r.Entities)
	if ents == nil {
		ents = []Entity{}
	}
	return ActivityView{
		ID:                  r.ID,
		Status:              r.Status,
		Title:               r.Title,
		Description:         r.Description,
		LatestChange:        r.LatestChange,
		Category:            r.Category,
		CategoryColor:       r.CategoryColor,
		Entities:            ents,
		Apps:                []ActivityAppUsage{},
		ActiveMs:            total,
		StartMs:             r.StartMs,
		EndMs:               r.EndMs,
		RunCount:            len(composeParseStrings(r.RunIDs)),
		ContinuesActivityID: r.ContinuesActivityID,
	}
}

// GetActivitiesInRange is the Wails binding backing the Activities tab.
func (a *App) buildActivitiesInRange(startMs, endMs int64) ([]ActivityView, error) {
	if a.activity == nil || a.activity.store == nil {
		return nil, nil
	}
	store := a.activity.store
	rows, err := store.GetActivitiesInRange(startMs, endMs)
	if err != nil {
		return nil, err
	}
	// Moments made only of agent runs are shown on the day's agent line.
	if agentRuns, err := store.agentRunIDsBetween(context.Background(), startMs, endMs); err == nil {
		rows = withoutAgentMoments(rows, agentRuns)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	usage, err := store.GetActivityAppUsage(ids)
	if err != nil {
		return nil, err
	}
	var daySpent map[string]int64
	crossingDay := false
	if startMs > 0 && startMs == startOfDayLocal(startMs) && endMs == activityThreadDayEnd(startMs) {
		for _, row := range rows {
			crossingDay = crossingDay || row.StartMs < startMs || row.EndMs > endMs
		}
	}
	if crossingDay {
		dayApps := activityMomentAppTimes{}
		daySpent, _, err = a.activityMomentTimeMs(context.Background(), startMs, endMs, dayApps)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.StartMs >= startMs && row.EndMs <= endMs {
				continue
			}
			id, apps := row.ID, dayApps[row.ID]
			usage[id] = nil
			for _, app := range apps {
				usage[id] = append(usage[id], app)
			}
			sort.Slice(usage[id], func(i, j int) bool {
				if usage[id][i].ActiveMs != usage[id][j].ActiveMs {
					return usage[id][i].ActiveMs > usage[id][j].ActiveMs
				}
				return usage[id][i].AppKey < usage[id][j].AppKey
			})
		}
	}
	// Every card in this response uses one evidence snapshot. Anchor it to
	// the displayed history so reading an old day cannot revive common tokens.
	var evidenceAt int64
	for _, row := range rows {
		evidenceAt = max(evidenceAt, row.EndMs)
	}
	var common map[string]bool
	if len(rows) > 0 {
		common, err = store.commonComposerEvidence(evidenceAt)
		if err != nil {
			return nil, err
		}
	}
	views := make([]ActivityView, 0, len(rows))
	now := time.Now().UnixMilli()
	for _, r := range rows {
		v := activityRowToView(r)
		if v.RunCount > 0 {
			v.LatestOutcome, err = store.GetLatestActivityOutcome(r.ID, common)
			if err != nil {
				return nil, err
			}
		}
		lastEnd := int64(0)
		var latestLinkEnd int64
		for _, id := range composeParseStrings(r.RunIDs) {
			run, err := store.GetTimelineRunByID(id)
			if err != nil {
				return nil, err
			}
			if run == nil || run.EvidenceExcluded || activityEvidenceAppExcluded(run.AppKey, run.AppName) {
				continue
			}
			allowed, err := store.timelineRunEvidenceAllowed(*run)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			lastEnd = max(lastEnd, run.EndMs)
			if v.LatestLink == nil || run.EndMs >= latestLinkEnd {
				v.LatestLink, err = store.activityRunLinkReason(r.ID, id, common)
				if err != nil {
					return nil, err
				}
				latestLinkEnd = run.EndMs
			}
		}
		v.InProgress = (endMs <= 0 || endMs > startOfDayLocal(now)) && activityInProgress(r.Status, lastEnd, now)
		v.ActiveMs = 0
		if apps := usage[r.ID]; apps != nil {
			v.Apps = apps
			for _, app := range apps {
				v.ActiveMs += app.ActiveMs
			}
		}
		if daySpent != nil && (r.StartMs < startMs || r.EndMs > endMs) {
			v.ActiveMs = daySpent[r.ID]
		}
		v.Thumbnails = a.activityThumbnails(r)
		views = append(views, v)
	}
	a.fillActivityRowActions(views, rows)
	return views, nil
}

const (
	// maxActivityThumbnails caps the flat representative strip shown on a card.
	maxActivityThumbnails = 4
	// maxActivityThumbnailRuns bounds how many of an activity's runs are scanned
	// to fill that strip, keeping the list query cheap.
	maxActivityThumbnailRuns = 2
)

// activityThumbnails gathers a small flat strip of representative screenshots
// across an activity's first runs (for the collapsed card). Bounded so the
// Activities list stays fast.
func (a *App) activityThumbnails(act ActivityRow) []ActivityRunFrame {
	if a.activity == nil || a.activity.store == nil {
		return []ActivityRunFrame{}
	}
	out := make([]ActivityRunFrame, 0, maxActivityThumbnails)
	runsSeen := 0
	for _, id := range composeParseStrings(act.RunIDs) {
		if len(out) >= maxActivityThumbnails || runsSeen >= maxActivityThumbnailRuns {
			break
		}
		run, err := a.activity.store.GetTimelineRunByID(id)
		if err != nil || run == nil {
			continue
		}
		runsSeen++
		for _, f := range a.activityRunFrames(*run) {
			out = append(out, f)
			if len(out) >= maxActivityThumbnails {
				break
			}
		}
	}
	return out
}

// GetActivityRuns is the drill-down binding: the activity's member runs, each
// with its cached run summary, in chronological order. Run ids that no longer
// exist (folded by reconcile) are skipped.
func (a *App) GetActivityRuns(activityID string) ([]ActivityRunView, error) {
	if a.activity == nil || a.activity.store == nil {
		return nil, nil
	}
	store := a.activity.store
	act, err := store.GetActivityByID(activityID)
	if err != nil {
		return nil, err
	}
	if act == nil {
		return nil, fmt.Errorf("activity %s not found", activityID)
	}
	runIDs := composeParseStrings(act.RunIDs)
	if len(runIDs) == 0 {
		return []ActivityRunView{}, nil
	}
	// Links are decided at each member's end. Reuse maps only for members
	// with that same evidence window, never the activity's later end.
	commonAt := make(map[int64]map[string]bool)
	summaries, err := store.GetTimelineGroupSummaries(runIDs)
	if err != nil {
		return nil, err
	}
	out := make([]ActivityRunView, 0, len(runIDs))
	for _, id := range runIDs {
		run, err := store.GetTimelineRunByID(id)
		if err != nil || run == nil {
			continue // folded by reconcile — skip
		}
		view := ActivityRunView{
			ID: run.ID, AppKey: run.AppKey, AppName: run.AppName,
			StartMs: run.StartMs, EndMs: run.EndMs, ActiveMs: run.ActiveMs,
			Frames: a.activityRunFrames(*run),
		}
		allowed, err := store.timelineRunEvidenceAllowed(*run)
		if err != nil {
			return nil, err
		}
		if allowed {
			common, ok := commonAt[run.EndMs]
			if !ok {
				common, err = store.commonComposerEvidence(run.EndMs)
				if err != nil {
					return nil, err
				}
				commonAt[run.EndMs] = common
			}
			view.Link, err = store.activityRunLinkReason(activityID, id, common)
			if err != nil {
				return nil, err
			}
		}
		if s, ok := summaries[id]; ok {
			view.Summary = &s
		}
		out = append(out, view)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartMs < out[j].StartMs })
	return out, nil
}

// Handoffs currently identify initiatives, not observed tasks. Until a real
// task/run provenance link exists, only recent permitted evidence is live.
func activityInProgress(status string, endMs, nowMs int64) bool {
	return activityStatusResumable(status) && endMs > 0 && endMs <= nowMs && nowMs-endMs <= activityIdleAfterMs
}

// UnlinkActivityRun is an explicit user correction, never an inference.
func (a *App) UnlinkActivityRun(activityID, runID string) error {
	if a.activity == nil || a.activity.store == nil {
		return fmt.Errorf("activity store not ready")
	}
	a.activity.mu.Lock()
	triager := a.activity.triager
	a.activity.mu.Unlock()
	if triager != nil {
		triager.activityComposerMu.Lock()
		defer triager.activityComposerMu.Unlock()
	}
	if err := a.activity.store.UnlinkActivityRun(activityID, runID, time.Now().UnixMilli()); err != nil {
		return err
	}
	if a.ctx != nil {
		a.emitBrowserEvent("activity:activities-updated")
	}
	return nil
}

// activityRunFrames returns up to maxActivityRunFrames representative
// screenshots of a run, evenly sampled across its span and cropped to the
// focused window. Reuses the same run->snapshot matching the timeline summary
// uses, so the frames are exactly the run's own captures.
func (a *App) activityRunFrames(run ActivityTimelineRunRow) []ActivityRunFrame {
	cacheKey := activityRunFramesCacheKey(run)
	if cached, ok := a.getCachedActivityRunFrames(cacheKey); ok {
		return cached
	}
	group := ActivityTimelineGroup{
		ID:         run.ID,
		AppKey:     run.AppKey,
		AppName:    run.AppName,
		StartTime:  run.StartMs,
		EndTime:    run.EndMs,
		SessionIDs: run.SessionIDs,
	}
	snaps := snapshotsWithImages(a.timelineGroupSummarySnapshots(group)) // chronological, run-matched
	if len(snaps) == 0 {
		a.setCachedActivityRunFrames(cacheKey, []ActivityRunFrame{})
		return []ActivityRunFrame{}
	}
	frames := make([]ActivityRunFrame, 0, maxActivityRunFrames)
	denom := maxActivityRunFrames - 1
	if denom <= 0 {
		denom = 1
	}
	lastIdx := -1
	for pick := 0; pick < maxActivityRunFrames && pick < len(snaps); pick++ {
		idx := 0
		if len(snaps) > 1 {
			idx = pick * (len(snaps) - 1) / denom
		}
		if idx == lastIdx {
			continue
		}
		lastIdx = idx
		s := snaps[idx]
		frames = append(frames, ActivityRunFrame{
			Timestamp: s.Timestamp,
			Path:      s.FilePath,
			FocusX:    s.FocusRectX,
			FocusY:    s.FocusRectY,
			FocusW:    s.FocusRectW,
			FocusH:    s.FocusRectH,
		})
	}
	a.setCachedActivityRunFrames(cacheKey, frames)
	return frames
}

func activityRunFramesCacheKey(run ActivityTimelineRunRow) string {
	return fmt.Sprintf("%s:%d:%d:%d:%d", run.ID, run.StartMs, run.EndMs, run.EventCount, run.UpdatedAt)
}

func (a *App) getCachedActivityRunFrames(key string) ([]ActivityRunFrame, bool) {
	if a == nil || a.activity == nil {
		return nil, false
	}
	a.activity.activityRunFramesMu.Lock()
	defer a.activity.activityRunFramesMu.Unlock()
	if a.activity.activityRunFramesCache == nil {
		return nil, false
	}
	frames, ok := a.activity.activityRunFramesCache[key]
	if !ok {
		return nil, false
	}
	return append([]ActivityRunFrame(nil), frames...), true
}

func (a *App) setCachedActivityRunFrames(key string, frames []ActivityRunFrame) {
	if a == nil || a.activity == nil {
		return
	}
	a.activity.activityRunFramesMu.Lock()
	defer a.activity.activityRunFramesMu.Unlock()
	if a.activity.activityRunFramesCache == nil {
		a.activity.activityRunFramesCache = make(map[string][]ActivityRunFrame)
	}
	a.activity.activityRunFramesCache[key] = append([]ActivityRunFrame(nil), frames...)
}

// GetActivityKeywordFrames returns frames in an activity whose stored screen
// text matches the active entity filters. This restores the pre-composer Events
// behavior where clicking a tag promoted matching screenshots and let the UI
// draw OCR boxes on them.
func (a *App) GetActivityKeywordFrames(activityID string, keywords []string) ([]ActivityKeywordFrame, error) {
	if a.activity == nil || a.activity.store == nil {
		return nil, nil
	}
	norm := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		k := strings.ToLower(removeCJKSpaces(strings.TrimSpace(kw)))
		if k != "" {
			norm = append(norm, k)
		}
	}
	if len(norm) == 0 {
		return []ActivityKeywordFrame{}, nil
	}
	containsAny := func(hay string) bool {
		hay = strings.ToLower(removeCJKSpaces(hay))
		for _, kw := range norm {
			if strings.Contains(hay, kw) {
				return true
			}
		}
		return false
	}
	flattenParsed := func(parsedJSON string) string {
		if parsedJSON == "" {
			return ""
		}
		pc := parseSnapshotContent(ActivitySnapshotRow{ParsedJSON: parsedJSON})
		if pc == nil {
			return ""
		}
		parts := []string{pc.Title, pc.Body, pc.URL, pc.Channel}
		for _, m := range pc.Messages {
			parts = append(parts, m.Sender, m.Content)
		}
		return strings.Join(parts, "\n")
	}

	store := a.activity.store
	act, err := store.GetActivityByID(activityID)
	if err != nil {
		return nil, err
	}
	if act == nil {
		return nil, fmt.Errorf("activity %s not found", activityID)
	}
	runIDs := composeParseStrings(act.RunIDs)
	out := make([]ActivityKeywordFrame, 0, 4)
	seen := make(map[string]bool)
	for _, id := range runIDs {
		if len(out) >= maxActivityKeywordFrames {
			break
		}
		run, err := store.GetTimelineRunByID(id)
		if err != nil || run == nil {
			continue
		}
		group := ActivityTimelineGroup{
			ID:         run.ID,
			AppKey:     run.AppKey,
			AppName:    run.AppName,
			StartTime:  run.StartMs,
			EndTime:    run.EndMs,
			SessionIDs: run.SessionIDs,
		}
		for _, snap := range a.timelineGroupSummarySnapshots(group) {
			if len(out) >= maxActivityKeywordFrames {
				break
			}
			if !snap.HasImage() || seen[snap.FilePath] {
				continue
			}
			ocrText, _ := store.GetOCRTextBySnapshot(snap.SessionID, snap.ID)
			visual := containsAny(ocrText)
			if !visual && !containsAny(snap.AXText) && !containsAny(flattenParsed(snap.ParsedJSON)) {
				continue
			}
			seen[snap.FilePath] = true
			out = append(out, ActivityKeywordFrame{
				ActivityRunFrame: ActivityRunFrame{
					Timestamp: snap.Timestamp,
					Path:      snap.FilePath,
					FocusX:    snap.FocusRectX,
					FocusY:    snap.FocusRectY,
					FocusW:    snap.FocusRectW,
					FocusH:    snap.FocusRectH,
				},
				Visual: visual,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Visual != out[j].Visual {
			return out[i].Visual
		}
		return out[i].Timestamp < out[j].Timestamp
	})
	return out, nil
}

// DeleteActivity removes one bucket (member runs + summaries survive) and
// refreshes the UI.
func (a *App) DeleteActivity(activityID string) error {
	if a.activity == nil || a.activity.store == nil {
		return fmt.Errorf("activity store not ready")
	}
	if err := a.deleteActivityWithPrivacyWithdrawal(a.activity.store, activityID); err != nil {
		return err
	}
	if a.ctx != nil {
		a.emitBrowserEvent("activity:activities-updated")
	}
	return nil
}
