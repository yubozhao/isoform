package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
	"wails-gui/internal/pii"
	"wails-gui/internal/privacy"
)

// The week is read once, when its configured last workday evening brief is
// first read, and kept as identities only (topic, activity, deliverable,
// meeting and memory ids, minutes and days), so each surface reads the same
// week. Every read names those identities again through their own gates: a
// withdrawn source leaves the card at once.
const yourWeekSnapshotKey = "your_week.snapshot.v1:"

type YourWeekEntry struct {
	BriefID  string            `json:"briefId,omitempty"`
	TimeZone string            `json:"timeZone,omitempty"`
	Week     *MobileBriefWeek  `json:"week,omitempty"`
	Cards    []MobileBriefCard `json:"cards"`
}

func (a *App) GetYourWeekForDate(dateISO string) (perfValue YourWeekEntry, perfErr error) {
	defer logPageBinding("GetYourWeekForDate", time.Now(), &perfValue)
	requested, err := time.Parse("2006-01-02", dateISO)
	if err != nil || requested.Format("2006-01-02") != dateISO {
		return YourWeekEntry{}, fmt.Errorf("invalid date")
	}
	if a == nil || a.IsClosedEyesModeEnabled() {
		return YourWeekEntry{Cards: []MobileBriefCard{}}, nil
	}
	service, ok := a.ensureMobileBriefService().(*mobileBriefService)
	if !ok || service == nil || service.store == nil {
		return YourWeekEntry{Cards: []MobileBriefCard{}}, nil
	}
	ctx := context.Background()
	for _, stored := range service.store.briefs() {
		if mobileBriefDisplaySlot(stored, defaultMobileBriefSettings()) != mobileBriefSlotEvening {
			continue
		}
		start, end, _, _, ok := a.yourWeekResolvedWindow(ctx, stored)
		if !ok || dateISO < start.Format("2006-01-02") || dateISO > end.Format("2006-01-02") {
			continue
		}
		delivered := a.mobileBriefWithCards(a.mobileBriefForDelivery(stored))
		if delivered.YourWeek == nil {
			continue
		}
		return YourWeekEntry{
			BriefID:  delivered.BriefID,
			TimeZone: delivered.Timezone,
			Week:     delivered.YourWeek,
			Cards:    delivered.Cards,
		}, nil
	}
	return YourWeekEntry{Cards: []MobileBriefCard{}}, nil
}

type yourWeekSnapshot struct {
	Version               int                        `json:"version"`
	StartDate             string                     `json:"startDate,omitempty"`
	WorkDays              int                        `json:"workDays,omitempty"`
	CutoffMs              int64                      `json:"cutoffMs"`
	SeenDays              int                        `json:"seenDays"`
	TotalMinutes          int64                      `json:"totalMinutes"`
	HasActivityProvenance bool                       `json:"hasActivityProvenance,omitempty"`
	Activities            []yourWeekSnapshotActivity `json:"activities,omitempty"`
	Topics                []yourWeekSnapshotRef      `json:"topics,omitempty"`
	Outcomes              []yourWeekSnapshotRef      `json:"outcomes,omitempty"`
	Tasks                 []yourWeekSnapshotRef      `json:"tasks,omitempty"`
	Deliverables          []yourWeekSnapshotRef      `json:"deliverables,omitempty"`
	Meetings              []yourWeekSnapshotRef      `json:"meetings,omitempty"`
	Memories              []yourWeekSnapshotRef      `json:"memories,omitempty"`
	Muted                 []string                   `json:"muted,omitempty"`
}

type yourWeekSnapshotActivity struct {
	ID    string  `json:"id"`
	DayMs []int64 `json:"dayMs,omitempty"`
}

type yourWeekSnapshotRef struct {
	ID          string   `json:"id"`
	AtMs        int64    `json:"atMs,omitempty"`
	OpenedAtMs  int64    `json:"openedAtMs,omitempty"`
	Digest      string   `json:"digest,omitempty"`
	Minutes     int64    `json:"minutes,omitempty"`
	ActivityIDs []string `json:"activityIds,omitempty"`
}

// yourWeekWindow names an evening brief's configured work week.
func yourWeekWindow(brief MobileBriefV1, workDays []time.Weekday) (start, end, cutoff time.Time, ok bool) {
	if brief.Slot != mobileBriefSlotEvening {
		return
	}
	workDays = normalizeYourWeekWorkDays(workDays)
	if len(workDays) == 0 {
		workDays = yourWeekDefaultWorkDays()
	}
	loc := time.Local
	if zone, err := time.LoadLocation(brief.Timezone); err == nil && brief.Timezone != "" {
		loc = zone
	}
	end, err := time.ParseInLocation("2006-01-02", brief.LocalDate, loc)
	if err != nil || end.Weekday() != workDays[len(workDays)-1] {
		return time.Time{}, time.Time{}, time.Time{}, false
	}
	start = end.AddDate(0, 0, (int(workDays[0])+6)%7-(int(workDays[len(workDays)-1])+6)%7)
	cutoff = mobileBriefParseTime(brief.AsOf).In(loc)
	if cutoff.Before(end) || !cutoff.Before(end.AddDate(0, 0, 1)) {
		cutoff = end.Add(18 * time.Hour)
	}
	return start, end, cutoff, true
}

func (a *App) yourWeekResolvedWindow(ctx context.Context, brief MobileBriefV1) (start, end, cutoff time.Time, workDays int, ok bool) {
	if brief.Slot != mobileBriefSlotEvening {
		return
	}
	loc := time.Local
	if zone, err := time.LoadLocation(brief.Timezone); err == nil && brief.Timezone != "" {
		loc = zone
	}
	end, err := time.ParseInLocation("2006-01-02", brief.LocalDate, loc)
	if err != nil {
		return time.Time{}, time.Time{}, time.Time{}, 0, false
	}
	if a != nil && a.store != nil {
		raw, err := a.store.GetUIState(ctx, yourWeekSnapshotKey+end.Format("2006-01-02"))
		if err == nil && raw != "" {
			var snap yourWeekSnapshot
			if json.Unmarshal([]byte(raw), &snap) == nil && snap.Version == 1 {
				start, workDays = end.AddDate(0, 0, -4), 5
				if snap.StartDate != "" {
					start, err = time.ParseInLocation("2006-01-02", snap.StartDate, loc)
					if err != nil {
						return time.Time{}, time.Time{}, time.Time{}, 0, false
					}
					workDays = snap.WorkDays
				}
				cutoff = time.UnixMilli(snap.CutoffMs).In(loc)
				if snap.CutoffMs == 0 {
					cutoff = mobileBriefParseTime(brief.AsOf).In(loc)
					if cutoff.Before(end) || !cutoff.Before(end.AddDate(0, 0, 1)) {
						cutoff = end.Add(18 * time.Hour)
					}
				}
				return start, end, cutoff, workDays, true
			}
		}
	}
	currentDays := a.yourWeekStoredWorkDays(ctx)
	start, end, cutoff, ok = yourWeekWindow(brief, currentDays)
	return start, end, cutoff, len(normalizeYourWeekWorkDays(currentDays)), ok
}

// yourWeekForDelivery is the week as this brief may show it now, and the same
// week as paragraphs. Nil outside the configured last workday evening, while observation is
// paused, before the second day, or once "Your week" is turned off.
func (a *App) yourWeekForDelivery(brief MobileBriefV1) (*MobileBriefWeek, []MobileBriefPara) {
	return a.yourWeekForDeliveryWithLead(brief, true)
}

func (a *App) yourWeekForBinding(brief MobileBriefV1) (*MobileBriefWeek, []MobileBriefPara) {
	return a.yourWeekForDeliveryWithLead(brief, false)
}

func (a *App) yourWeekForDeliveryWithLead(brief MobileBriefV1, allowModelLead bool) (*MobileBriefWeek, []MobileBriefPara) {
	return a.yourWeekForDeliveryWithLeadContext(context.Background(), brief, allowModelLead)
}

func (a *App) yourWeekForDeliveryWithLeadContext(ctx context.Context, brief MobileBriefV1, allowModelLead bool) (*MobileBriefWeek, []MobileBriefPara) {
	if a == nil || a.engagement == nil || a.store == nil || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) || ctx.Err() != nil {
		return nil, nil
	}
	now := a.engagement.now()
	if !a.onboardingFinished(ctx) || !a.journeyEnabled(engagement.KindWeeklyRecap, now) {
		return nil, nil
	}
	currentWorkDays := a.yourWeekStoredWorkDays(ctx)
	start, end, cutoff, resolvedWorkDays, ok := a.yourWeekResolvedWindow(ctx, brief)
	if !ok || now.Before(start) {
		return nil, nil
	}
	if installed := a.firstWeekInstall(ctx); !installed.IsZero() && engagement.FirstWeekDay(installed, cutoff) < 2 {
		return nil, nil
	}
	snap, ok := a.yourWeekSnapshotFor(ctx, start, end, cutoff, now, resolvedWorkDays)
	if !ok {
		return nil, nil
	}
	lang := mobileBriefVoiceLang(uiLanguageDisplayName(a.currentUILanguage()))
	in := a.yourWeekReadable(ctx, snap, start, now)
	nextDay := yourWeekNextFirstDay(end, currentWorkDays)
	in.Lang, in.Start, in.End, in.NextWorkDay, in.WorkDays = lang, start, end, nextDay, resolvedWorkDays
	in.Open = yourWeekOpenCandidates(brief)
	week := selectYourWeek(in)
	if week == nil {
		return nil, nil
	}
	quiet := in.SeenDays >= 0 && in.SeenDays <= yourWeekQuietSeenDays && in.SeenDays < in.WorkDays
	if week.Ask != nil {
		week.Ask.Day = nextDay.Format("2006-01-02")
	}
	if allowModelLead {
		week.Headline = yourWeekHeadline(brief, week, quiet)
	}
	var paragraphs []MobileBriefPara
	for _, paragraph := range yourWeekParagraphs(week, lang) {
		paragraphs = append(paragraphs, MobileBriefPara{Text: paragraph.Text, Refs: []string{}, Role: paragraph.Role})
	}
	return week, paragraphs
}

func yourWeekNextFirstDay(end time.Time, workDays []time.Weekday) time.Time {
	workDays = normalizeYourWeekWorkDays(workDays)
	if len(workDays) == 0 {
		workDays = yourWeekDefaultWorkDays()
	}
	daysUntil := ((int(workDays[0])+6)%7 - (int(end.Weekday())+6)%7 + 7) % 7
	if daysUntil == 0 {
		daysUntil = 7
	}
	return end.AddDate(0, 0, daysUntil)
}

// yourWeekSnapshotFor reads the frozen week, or, on the last workday before
// it is frozen, reads it now without saving: brief reads run inside the
// brief's source fence, where any write would invalidate it. A later read of
// an old brief never builds a new week.
func (a *App) yourWeekSnapshotFor(ctx context.Context, start, end, cutoff, now time.Time, workDays int) (yourWeekSnapshot, bool) {
	raw, err := a.store.GetUIState(ctx, yourWeekSnapshotKey+end.Format("2006-01-02"))
	if err != nil {
		return yourWeekSnapshot{}, false
	}
	var snap yourWeekSnapshot
	if raw != "" {
		if json.Unmarshal([]byte(raw), &snap) != nil || snap.Version != 1 {
			return yourWeekSnapshot{}, false
		}
		return snap, true
	}
	if a.IsClosedEyesModeEnabled() && pausedBriefReadAllowed(ctx) {
		// A paused proof may reopen the frozen week, but never collect a new one.
		return yourWeekSnapshot{}, false
	}
	if now.In(end.Location()).Format("2006-01-02") != end.Format("2006-01-02") {
		return yourWeekSnapshot{}, false
	}
	// One brief read projects the brief several times; read the week once.
	key := yourWeekLiveKey{app: a, end: end.Format("2006-01-02"), start: start.Format("2006-01-02"),
		cutoff: cutoff.UnixMilli(), workDays: workDays}
	for {
		yourWeekLive.mu.Lock()
		generation := yourWeekLive.generation
		if cached, ok := yourWeekLive.weeks[key]; ok && now.Sub(cached.at) < yourWeekLiveTTL && !now.Before(cached.at) {
			yourWeekLive.mu.Unlock()
			return cached.snap, true
		}
		yourWeekLive.mu.Unlock()

		snap = a.gatherYourWeek(ctx, start, cutoff, now, workDays)

		yourWeekLive.mu.Lock()
		if generation != yourWeekLive.generation {
			yourWeekLive.mu.Unlock()
			continue
		}
		yourWeekLive.weeks = map[yourWeekLiveKey]yourWeekLiveWeek{key: {snap: snap, at: now}}
		yourWeekLive.mu.Unlock()
		return snap, true
	}
}

const yourWeekLiveTTL = 2 * time.Minute

type yourWeekLiveKey struct {
	app      *App
	end      string
	start    string
	cutoff   int64
	workDays int
}

type yourWeekLiveWeek struct {
	snap yourWeekSnapshot
	at   time.Time
}

var yourWeekLive struct {
	mu         sync.Mutex
	generation uint64
	weeks      map[yourWeekLiveKey]yourWeekLiveWeek
}

func invalidateYourWeekLive() {
	yourWeekLive.mu.Lock()
	yourWeekLive.generation++
	yourWeekLive.weeks = nil
	yourWeekLive.mu.Unlock()
}

func init() {
	subscribePrivacyWithdrawals(func(privacy.Withdrawal) {
		invalidateYourWeekLive()
	})
}

// freezeYourWeek saves the week its last-workday evening brief reads, once,
// outside any read path, so later reads on every surface read the same week.
func (a *App) freezeYourWeek(ctx context.Context, brief MobileBriefV1) {
	if a == nil || a.store == nil || a.engagement == nil {
		return
	}
	start, end, cutoff, workDays, ok := a.yourWeekResolvedWindow(ctx, brief)
	if !ok {
		return
	}
	key := yourWeekSnapshotKey + end.Format("2006-01-02")
	if raw, err := a.store.GetUIState(ctx, key); err != nil || raw != "" {
		return
	}
	snap := a.gatherYourWeek(ctx, start, cutoff, a.engagement.now(), workDays)
	snap.StartDate = start.Format("2006-01-02")
	snap.WorkDays = workDays
	if data, err := json.Marshal(snap); err == nil {
		_ = a.store.SetUIState(ctx, key, string(data))
	}
}

// gatherYourWeek reads the week's identities through the sources' own
// filters: present time per activity, topics, things Isoform did that the
// person used, meeting notes, memories and mutes.
func (a *App) gatherYourWeek(ctx context.Context, start, cutoff, now time.Time, workDays int) yourWeekSnapshot {
	snap := yourWeekSnapshot{Version: 1, StartDate: start.Format("2006-01-02"), WorkDays: workDays, CutoffMs: cutoff.UnixMilli(),
		Activities: make([]yourWeekSnapshotActivity, 0)}
	if a.activity != nil && a.activity.store != nil {
		snap.HasActivityProvenance = true
		spent := map[string]int64{}
		activityDays := map[string][]int64{}
		dayCount := 0
		end := cutoff.In(start.Location())
		end = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, start.Location())
		for from := start; !from.After(end); from = from.AddDate(0, 0, 1) {
			to := from.AddDate(0, 0, 1)
			if to.After(cutoff) {
				to = cutoff
			}
			if !to.After(from) {
				break
			}
			byActivity, _, err := a.activityMomentTimeMs(ctx, from.UnixMilli(), to.UnixMilli())
			dayCount++
			if err != nil {
				snap.HasActivityProvenance = false
				continue
			}
			for id, ms := range byActivity {
				if id != "" {
					spent[id] += ms
					if _, ok := activityDays[id]; !ok {
						activityDays[id] = make([]int64, dayCount)
					}
					for len(activityDays[id]) < dayCount {
						activityDays[id] = append(activityDays[id], 0)
					}
					activityDays[id][dayCount-1] = ms
				}
			}
		}
		ids := make([]string, 0, len(activityDays))
		for id := range activityDays {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			dayMs := activityDays[id]
			for len(dayMs) < dayCount {
				dayMs = append(dayMs, 0)
			}
			var totalMs int64
			for _, ms := range dayMs {
				totalMs += ms
			}
			if totalMs > 0 {
				snap.TotalMinutes += totalMs / int64(time.Minute/time.Millisecond)
			}
			snap.Activities = append(snap.Activities, yourWeekSnapshotActivity{ID: id, DayMs: dayMs})
		}
		seen := map[int]bool{}
		for _, activity := range snap.Activities {
			for i, ms := range activity.DayMs {
				if ms > 0 {
					seen[i] = true
				}
			}
		}
		snap.SeenDays = len(seen)
		snap.Topics = a.yourWeekTopicMinutes(spent)
		if rows, err := a.activity.store.GetActivitiesInRangeContext(ctx, start.UnixMilli(), cutoff.UnixMilli()); err == nil {
			for _, row := range rows {
				if yourWeekUsedOutcome(a.readableThreadOutcome(ctx, row)) {
					snap.Outcomes = append(snap.Outcomes, yourWeekSnapshotRef{ID: row.ID, AtMs: row.EndMs})
				}
			}
		}
		if notes, err := a.GetRecentMeetingNotes(yourWeekDaysSince(start, now)); err == nil {
			for _, note := range notes {
				if note.EndTime >= start.UnixMilli() && note.EndTime < cutoff.UnixMilli() {
					snap.Meetings = append(snap.Meetings, yourWeekSnapshotRef{ID: note.MeetingKey, AtMs: note.EndTime})
				}
			}
		}
	}
	if items, err := a.ListDeliverables(); err == nil {
		allowed := a.deliverableFolderReadFilter(ctx)
		for _, item := range items {
			if yourWeekDeliverableUsable(item) && allowed(item) && item.UpdatedAt >= start.UnixMilli() && item.UpdatedAt < cutoff.UnixMilli() {
				snap.Deliverables = append(snap.Deliverables, yourWeekSnapshotRef{ID: item.ID, AtMs: item.UpdatedAt})
			}
		}
	}
	if a.handoffStore != nil {
		entries, err := a.handoffStore.List()
		subjects, subjectErr := mobileGoalSubjects(a)
		if err == nil && subjectErr == nil {
			for _, entry := range entries {
				if row, ok := yourWeekOpenedTask(entry, subjects, start, cutoff); ok {
					snap.Tasks = append(snap.Tasks, yourWeekSnapshotRef{ID: row.TaskID, AtMs: entry.Thread.Outcome.At, OpenedAtMs: entry.Thread.OpenedAt})
				}
			}
		}
	}
	if allowRead, err := a.memoryReadFilter(); err == nil {
		if entries, err := a.GetMemoryEntries(); err == nil {
			for _, entry := range entries {
				updated := entry.UpdatedAt
				if updated == 0 {
					updated = entry.ModTime
				}
				if updated >= start.UnixMilli() && updated < cutoff.UnixMilli() && allowRead(entry.ID, entry.Content) {
					snap.Memories = append(snap.Memories, yourWeekSnapshotRef{ID: entry.ID, AtMs: updated, Digest: todayWeekBeliefDigest(entry.Content)})
				}
			}
			sort.SliceStable(snap.Memories, func(i, j int) bool { return snap.Memories[i].AtMs > snap.Memories[j].AtMs })
		}
	}
	if stats, err := a.store.EngagementStatsBetween(ctx, start, cutoff); err == nil {
		for kind, stat := range stats {
			if stat.Muted > 0 && kind != string(engagement.KindWeeklyRecap) && !engagementKindSilent(engagement.Kind(kind)) {
				snap.Muted = append(snap.Muted, kind)
			}
		}
		sort.Strings(snap.Muted)
	}
	return snap
}

// yourWeekTopicMinutes gives each activity's time to one topic: the topic
// with the most time takes its activities first, so no minute counts twice.
func (a *App) yourWeekTopicMinutes(spent map[string]int64) []yourWeekSnapshotRef {
	projects := a.topicsSvc().store.load().Projects
	taken := map[string]bool{}
	var out []yourWeekSnapshotRef
	for len(out) < yourWeekTimeTopics*2 {
		best, bestMs := -1, int64(0)
		for i, project := range projects {
			if project.Deferred {
				continue
			}
			var ms int64
			for _, id := range project.ActivityIDs {
				if !taken[id] {
					ms += spent[id]
				}
			}
			if ms > bestMs {
				best, bestMs = i, ms
			}
		}
		if best < 0 {
			break
		}
		var activityIDs []string
		for _, id := range projects[best].ActivityIDs {
			activityIDs = append(activityIDs, id)
			taken[id] = true
		}
		out = append(out, yourWeekSnapshotRef{ID: projects[best].ID,
			Minutes: bestMs / int64(time.Minute/time.Millisecond), ActivityIDs: activityIDs})
		projects = append(projects[:best:best], projects[best+1:]...)
	}
	return out
}

// yourWeekReadable names the frozen week's identities as they read now. What
// can no longer be read is left out.
func (a *App) yourWeekReadable(ctx context.Context, snap yourWeekSnapshot, start, now time.Time) yourWeekInput {
	in := yourWeekInput{}
	projectionFailed := false
	if snap.HasActivityProvenance && len(snap.Activities) > 0 &&
		(a.activity == nil || a.activity.store == nil) {
		projectionFailed = true
	}
	if snap.HasActivityProvenance && len(snap.Activities) > 0 && !projectionFailed {
		candidates := map[string]bool{}
		frozenActivityDayMs := map[string][]int64{}
		for _, activity := range snap.Activities {
			if strings.TrimSpace(activity.ID) != "" {
				candidates[activity.ID] = true
				frozenActivityDayMs[activity.ID] = activity.DayMs
			}
		}
		currentActivityDayMs := map[string][]int64{}
		for id := range candidates {
			currentActivityDayMs[id] = make([]int64, 0, len(frozenActivityDayMs[id]))
		}
		end := snap.CutoffMs
		if end == 0 {
			end = now.UnixMilli()
		}
		projectionComplete := true
		for from := start; from.UnixMilli() < end; from = from.AddDate(0, 0, 1) {
			to := from.AddDate(0, 0, 1)
			if to.UnixMilli() > end {
				to = time.UnixMilli(end).In(start.Location())
			}
			if !to.After(from) {
				break
			}
			byActivity, _, err := a.activityMomentTimeMs(ctx, from.UnixMilli(), to.UnixMilli())
			if err != nil {
				projectionComplete = false
				break
			}
			for id := range candidates {
				currentActivityDayMs[id] = append(currentActivityDayMs[id], byActivity[id])
			}
		}
		if !projectionComplete {
			clear(currentActivityDayMs)
			projectionFailed = true
		}
		seenDays := map[int]bool{}
		activityMs := map[string]int64{}
		for id, frozenDays := range frozenActivityDayMs {
			row, err := a.activity.store.GetActivityByIDContext(ctx, id)
			if err != nil || row == nil {
				continue
			}
			for i, frozenMs := range frozenDays {
				if frozenMs <= 0 || i >= len(currentActivityDayMs[id]) {
					continue
				}
				ms := min(frozenMs, currentActivityDayMs[id][i])
				if ms > 0 {
					seenDays[i] = true
					activityMs[id] += ms
				}
			}
		}
		in.SeenDays = len(seenDays)
		var totalMs int64
		for _, ms := range activityMs {
			totalMs += ms
		}
		in.TotalMinutes = totalMs / int64(time.Minute/time.Millisecond)
		names := map[string]string{}
		for _, project := range a.topicsSvc().store.load().Projects {
			if !project.Deferred {
				names[project.ID] = project.Name
			}
		}
		for _, topic := range snap.Topics {
			if name := names[topic.ID]; name != "" {
				var ms int64
				for _, id := range topic.ActivityIDs {
					ms += activityMs[id]
				}
				if ms > 0 {
					in.Topics = append(in.Topics, yourWeekTopic{ID: topic.ID, Name: name,
						Minutes: ms / int64(time.Minute/time.Millisecond)})
				}
			}
		}
	}
	if !snap.HasActivityProvenance || projectionFailed {
		in.SeenDays = -1
	}
	if a.activity != nil && a.activity.store != nil {
		for _, ref := range snap.Outcomes {
			row, err := a.activity.store.GetActivityByIDContext(ctx, ref.ID)
			if err != nil || row == nil {
				continue
			}
			if outcome := a.readableThreadOutcome(ctx, *row); yourWeekUsedOutcome(outcome) {
				in.Done = append(in.Done, MobileBriefWeekDone{ID: "outcome:" + row.ID, Kind: yourWeekDoneOutcome, Title: outcome.Summary, At: yourWeekTime(row.EndMs)})
			}
		}
		if len(snap.Meetings) > 0 {
			if notes, err := a.GetRecentMeetingNotes(yourWeekDaysSince(start, now)); err == nil {
				keys := map[string]bool{}
				for _, ref := range snap.Meetings {
					keys[ref.ID] = true
				}
				for _, note := range notes {
					if keys[note.MeetingKey] {
						in.Meetings++
						if at := time.UnixMilli(note.EndTime); at.After(in.MeetingsAt) {
							in.MeetingsAt = at
						}
					}
				}
			}
		}
	}
	if len(snap.Deliverables) > 0 {
		if items, err := a.ListDeliverables(); err == nil {
			allowed := a.deliverableFolderReadFilter(ctx)
			byID := map[string]DeliverableEntry{}
			for _, item := range items {
				byID[item.ID] = item
			}
			for _, ref := range snap.Deliverables {
				item, ok := byID[ref.ID]
				if !ok || !yourWeekDeliverableUsable(item) || !allowed(item) {
					continue
				}
				in.Done = append(in.Done, MobileBriefWeekDone{ID: "deliverable:" + item.ID, Kind: yourWeekDoneDeliverable, Title: item.Title,
					At: yourWeekTime(item.UpdatedAt), Source: item.Source, SourceID: item.SourceID, Path: item.Files[0].Path})
			}
		}
	}
	if len(snap.Tasks) > 0 && a.handoffStore != nil {
		entries, err := a.handoffStore.List()
		subjects, subjectErr := mobileGoalSubjects(a)
		if err == nil && subjectErr == nil {
			refs := make(map[string]yourWeekSnapshotRef, len(snap.Tasks))
			for _, ref := range snap.Tasks {
				refs[ref.ID] = ref
			}
			for _, entry := range entries {
				ref, exists := refs[entry.ID]
				if !exists || entry.Thread == nil || entry.Thread.Outcome == nil || entry.Thread.Outcome.At != ref.AtMs || ref.OpenedAtMs <= 0 || entry.Thread.OpenedAt < ref.OpenedAtMs {
					continue
				}
				thread := *entry.Thread
				thread.OpenedAt = ref.OpenedAtMs
				entry.Thread = &thread
				if row, ok := yourWeekOpenedTask(entry, subjects, start, time.UnixMilli(snap.CutoffMs)); ok {
					in.Done = append(in.Done, row)
				}
			}
		}
	}
	if len(snap.Memories) > 0 {
		if allowRead, err := a.memoryReadFilter(); err == nil {
			if entries, err := a.GetMemoryEntries(); err == nil {
				byID := map[string]MemoryEntry{}
				for _, entry := range entries {
					byID[entry.ID] = entry
				}
				for _, ref := range snap.Memories {
					entry, ok := byID[ref.ID]
					if !ok || !memoryInUse(entry.Content) || !allowRead(entry.ID, entry.Content) {
						continue
					}
					updated := entry.UpdatedAt
					if updated == 0 {
						updated = entry.ModTime
					}
					if ref.Digest != "" && ref.Digest != todayWeekBeliefDigest(entry.Content) || ref.Digest == "" && ref.AtMs > 0 && ref.AtMs != updated {
						continue
					}
					in.Memories = append(in.Memories, MobileBriefWeekLearned{MemoryID: entry.ID, Text: entry.Title})
				}
			}
		}
	}
	for _, kind := range snap.Muted {
		// A mute already undone is no longer something Isoform does.
		if _, known := engagement.Spec(engagement.Kind(kind)); known && !a.journeyEnabled(engagement.Kind(kind), now) {
			in.Muted = append(in.Muted, kind)
		}
	}
	return in
}

// yourWeekOpenCandidates is "What needs your attention" in attention order,
// each with the section its subject sits in.
func yourWeekOpenCandidates(brief MobileBriefV1) []yourWeekOpenCandidate {
	sections := map[string]string{}
	for _, row := range mobileBriefCardRows(brief) {
		if key := mobileBriefCardItemKey(row.item); sections[key] == "" {
			sections[key] = row.section
		}
	}
	var out []yourWeekOpenCandidate
	for _, item := range mobileBriefAttentionItems(brief) {
		out = append(out, yourWeekOpenCandidate{Item: item, Section: sections[mobileBriefCardItemKey(item)]})
	}
	return out
}

// yourWeekAskCard is the next-workday recommendation's card when its subject is a
// brief row: the row's sources, the recommendation's own words.
func yourWeekAskCard(brief MobileBriefV1) (MobileBriefCard, bool) {
	if brief.YourWeek == nil || brief.YourWeek.Ask == nil || !strings.HasPrefix(brief.YourWeek.Ask.CardID, "week:ask:") {
		return MobileBriefCard{}, false
	}
	ask := brief.YourWeek.Ask
	key := strings.TrimPrefix(ask.CardID, "week:ask:")
	for _, row := range mobileBriefCardRows(brief) {
		if mobileBriefCardItemKey(row.item) != key || len(row.item.Refs) == 0 {
			continue
		}
		return MobileBriefCard{ID: ask.CardID, Kind: mobileBriefCardItem, Mode: mobileBriefCardSend, Message: ask.Message,
			Refs: append([]string{}, row.item.Refs...), Facts: mobileBriefCardFacts(brief, row.item, "week_ask"), Writer: mobileBriefCardWriterPattern}, true
	}
	return MobileBriefCard{}, false
}

// produceYourWeekReady rings "Your week is ready" for the configured last-workday evening brief
// that has a week, in place of the brief-ready notice. The chat line is the
// doorbell; the week itself lives in the brief.
func (a *App) produceYourWeekReady(brief MobileBriefV1) bool {
	if a == nil || a.engagement == nil {
		return false
	}
	week, _ := a.yourWeekForDelivery(brief)
	if week == nil {
		return false
	}
	a.freezeYourWeek(context.Background(), brief)
	now := a.engagement.now()
	key := engagement.DedupeKey(engagement.KindWeeklyRecap, week.End)
	if exists, err := a.store.EngagementMomentKeyExists(context.Background(), key); err != nil || exists {
		return exists
	}
	return a.submitEngagementSignal(context.Background(), engagement.Moment{Kind: engagement.KindWeeklyRecap,
		ID: mainChatEngagementMomentID(key), SubjectID: week.End, DedupeKey: key, CreatedAt: now,
		// Quiet hours can hold the doorbell until Saturday's late morning.
		ExpiresAt: now.Add(18 * time.Hour), Params: map[string]string{"source_brief_id": brief.BriefID}}) == nil
}

func yourWeekUsedOutcome(outcome *ActivityThreadOutcome) bool {
	return outcome != nil && outcome.Kind == "assistant" && outcome.State == "acted_on" && outcome.UserActed == "observed" && strings.TrimSpace(outcome.Summary) != ""
}

// A task's own completion and opened receipt establish both its identity and
// that its result was seen before the week's cutoff. Activity observations do
// not carry this association and must never be matched by title.
func yourWeekOpenedTask(entry handoff.Entry, subjects []string, start, cutoff time.Time) (MobileBriefWeekDone, bool) {
	if entry.ID == "" || entry.Status != handoff.StatusCompleted || entry.Thread == nil || entry.Thread.Outcome == nil {
		return MobileBriefWeekDone{}, false
	}
	outcome := entry.Thread.Outcome
	if outcome.Kind != handoff.OutcomeCompleted || outcome.By != handoff.OutcomeByIsoform || outcome.At < start.UnixMilli() || outcome.At >= cutoff.UnixMilli() || entry.Thread.OpenedAt < outcome.At || entry.Thread.OpenedAt >= cutoff.UnixMilli() {
		return MobileBriefWeekDone{}, false
	}
	if _, visible := mobileTaskDetail(entry, subjects); !visible || pii.IsSensitiveAppName(entry.AppName) {
		return MobileBriefWeekDone{}, false
	}
	title := mobileOfferText(outcome.Params["summary"], mobileTaskSummaryMax)
	if title == "" || mobileTaskEchoesMail(title, subjects) {
		return MobileBriefWeekDone{}, false
	}
	return MobileBriefWeekDone{ID: "task:" + entry.ID, Kind: yourWeekDoneOutcome, TaskID: entry.ID, Title: title, At: yourWeekTime(outcome.At)}, true
}

// A finished file the person asked for, which the card can open.
func yourWeekDeliverableUsable(item DeliverableEntry) bool {
	return item.Status == "completed" && (item.Source == "chat" || item.Source == "handoff") && len(item.Files) > 0 && item.Files[0].Path != ""
}

func yourWeekDaysSince(start, now time.Time) int {
	days := int(now.Sub(start).Hours()/24) + 1
	if days < 1 {
		return 1
	}
	return days
}

func yourWeekTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).Format(time.RFC3339)
}
