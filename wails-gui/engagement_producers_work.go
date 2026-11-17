package main

import (
	"context"
	"log"
	"strings"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
	"wails-gui/storage"
)

// Phase 1 moment producers. Each producer turns one app event into an
// engagement.Moment. Producers pass titles, counts and ids only. When the
// Engagement service is not running a producer does nothing. When Submit fails
// the producer logs and reports nothing submitted, so the caller keeps its old
// behavior.

const (
	engagementWorkFinishedStakes = 0.6
	engagementWorkBlockedStakes  = 0.7
	engagementBriefStakes        = 0.4
	engagementBriefMissedStakes  = 0.5
	engagementBriefExpiry        = 12 * time.Hour
	engagementTitleMax           = 160
)

func (a *App) engagementActive() bool {
	return a != nil && a.engagement != nil
}

// submitEngagementMoment hands one moment to the Engagement service. It reports
// whether the service accepted it. A repeat of an open moment counts as accepted.
func (a *App) submitEngagementMoment(what string, m engagement.Moment) bool {
	if !a.engagementActive() {
		return false
	}
	if _, err := a.engagement.Submit(context.Background(), m); err != nil {
		log.Printf("[engagement] %s moment not submitted: %v", what, err)
		return false
	}
	return true
}

// produceWorkFinished submits work_finished for a Handoff whose run ended without
// error, whether its evidence verified (completed) or needs review. The copy
// says the result is ready to review, so both outcomes read the same.
func (a *App) produceWorkFinished(entry handoff.Entry) {
	a.submitEngagementMoment("work finished", engagement.Moment{
		ID:        mainChatEngagementMomentID(mainChatHandoffMomentID(entry)),
		Kind:      engagement.KindWorkFinished,
		SubjectID: entry.ID,
		DedupeKey: engagement.DedupeKey(engagement.KindWorkFinished, entry.ID),
		Stakes:    engagementWorkFinishedStakes,
		Params:    a.workFinishedParams(entry, time.Now()),
	})
}

// The agent and the one sentence finished work carries to a phone that
// reads Your work (phone-agent v1.2.0 rule 10). Both pass the moment's
// privacy filter (sanitizeEngagementMoment) like every param.
const (
	engagementAgentParam    = "agent"
	engagementSentenceParam = "sentence"
	engagementSentenceMax   = 200
)

func (a *App) workFinishedParams(entry handoff.Entry, now time.Time) map[string]string {
	params := map[string]string{"title": engagementTitle(entry.Title)}
	if a == nil {
		return params
	}
	agentID := ""
	if handoffs, err := a.GetWorkOnItHandoffs(); err == nil {
		for _, h := range handoffs {
			// A local task's hand-off shares the task's id.
			if h.ID == entry.ID {
				agentID = h.AgentID
				break
			}
		}
	}
	params[engagementAgentParam] = a.workAgentName(agentID)
	if sentence := a.workFinishedSentence(entry, now); sentence != "" {
		params[engagementSentenceParam] = sentence
	}
	return params
}

// workFinishedSentence is the first sentence of the work's result, or ""
// when any of the run fell in a Closed Eyes period, Closed Eyes is on now,
// or the pause log can't be read (it fails closed).
func (a *App) workFinishedSentence(entry handoff.Entry, now time.Time) string {
	if a.IsClosedEyesModeEnabled() {
		return ""
	}
	pauses, err := a.observationPauses()
	if err != nil {
		return ""
	}
	start := entry.AttemptStartedAt
	if start == 0 {
		start = entry.CreatedAt
	}
	for _, p := range pauses {
		end := p.End
		if end == 0 {
			end = now.UnixMilli()
		}
		if end > start && p.Start <= now.UnixMilli() {
			return ""
		}
	}
	_, summary := dailyTopicTaskState(entry)
	return engagementFirstSentence(summary)
}

// engagementFirstSentence is a text's first sentence, at most
// engagementSentenceMax runes.
func engagementFirstSentence(text string) string {
	if line, _, found := strings.Cut(strings.TrimSpace(text), "\n"); found {
		text = line
	}
	text = strings.Join(strings.Fields(text), " ")
	for i, r := range text {
		if r == '。' || ((r == '.' || r == '!' || r == '?') && (i+1 == len(text) || text[i+1] == ' ')) {
			text = text[:i+len(string(r))]
			break
		}
	}
	if runes := []rune(text); len(runes) > engagementSentenceMax {
		return strings.TrimSpace(string(runes[:engagementSentenceMax])) + "..."
	}
	return text
}

// produceWorkBlocked submits work_blocked for a Handoff that became blocked. The
// dedupe key includes the reason, so each reason is one moment.
func (a *App) produceWorkBlocked(entry handoff.Entry) {
	a.submitEngagementMoment("work blocked", engagement.Moment{
		ID:        mainChatEngagementMomentID(mainChatHandoffMomentID(entry)),
		Kind:      engagement.KindWorkBlocked,
		SubjectID: entry.ID,
		DedupeKey: engagement.DedupeKey(engagement.KindWorkBlocked, entry.ID, engagement.HashPart(entry.BlockedReason)),
		Stakes:    engagementWorkBlockedStakes,
		Params: map[string]string{
			"title":    engagementTitle(entry.Title),
			"question": engagementBlockedQuestion(entry.BlockedReason),
		},
	})
}

// engagementBlockedQuestion turns a blocked reason code into the phrase the
// sealed notice reads after the title.
func engagementBlockedQuestion(reason string) string {
	switch reason {
	case handoff.BlockedInterrupted:
		return "to be picked up again."
	case handoff.BlockedTimeout:
		return "a check-in to keep going."
	default:
		return "your attention."
	}
}

// announceBriefStoredBeforeEngagement posts the brief-ready moment for a brief that
// was stored while the Engagement service was not running (a launch while locked,
// or a brief built before the service started). Without it that brief is never
// announced: its moment was refused and nothing retries it (R36, 2026-11-05). The
// moment's dedupe key is the brief, so announcing again is harmless.
func (a *App) announceBriefStoredBeforeEngagement(brief MobileBriefV1) bool {
	if brief.BriefID == "" || (brief.Slot != mobileBriefSlotMorning && brief.Slot != mobileBriefSlotEvening) {
		return false
	}
	return a.produceBriefReady(brief, true, false)
}

// produceBriefReady submits brief_ready for a brief that would have pushed. It
// returns the notify flag the brief publish should use. The flag is false once
// the moment carries the push, so the phone is not pushed twice. manual is set
// for a brief the person asked for; it arrives without a buzz (ST86).
func (a *App) produceBriefReady(brief MobileBriefV1, notify, manual bool) bool {
	if a != nil && a.engagement != nil {
		a.refreshYourWeekWorkDays(context.Background(), a.engagement.now())
	}
	if brief.NarrativeStatus == "quiet" {
		return false
	}
	// A day-one first-week push is one per day; a manual brief must not use it,
	// or the scheduled brief that day would never buzz.
	firstWeek := !manual && a.produceFirstWeekBrief(brief)
	if firstWeek || a.produceYourWeekReady(brief) {
		return false
	}
	if !notify || brief.BriefID == "" || !a.engagementActive() {
		return false
	}
	now := a.engagement.now()
	// A scheduled brief built ahead of its slot is held until the slot's time,
	// so the 6 PM brief arrives at 6 PM. One built after its time goes at once.
	var notBefore time.Time
	if svc, isService := a.mobileBriefSource().(*mobileBriefService); isService && svc != nil {
		if at, ok := svc.scheduledTimeFor(brief.Slot, now); ok && at.After(now) {
			notBefore = at
		}
	}
	// A written brief without its recording yet waits for it, at most
	// mobileBriefNotifyHoldMax, so a tap opens a brief that plays
	// (PROTOCOL.md 8.2). releaseBriefReady sends it once the recording step
	// ends. The moment is stored at once, so the first read still waits on it.
	if brief.NarrativeStatus == "ok" && !mobileBriefHasRecording(brief) {
		if hold := now.Add(mobileBriefNotifyHoldMax); hold.After(notBefore) {
			notBefore = hold
		}
	}
	if !a.submitEngagementMoment("brief ready", engagement.Moment{
		Kind:      engagement.KindBriefReady,
		SubjectID: brief.BriefID,
		DedupeKey: engagement.DedupeKey(engagement.KindBriefReady, brief.BriefID),
		Stakes:    engagementBriefStakes,
		NotBefore: notBefore,
		ExpiresAt: now.Add(engagementBriefExpiry),
		Params:    briefReadyParams(brief, manual),
	}) {
		return false
	}
	return false
}

// briefReadyParams are the copy inputs of a brief_ready moment. A brief the
// person asked for carries the manual marker, which keeps it silent.
func briefReadyParams(brief MobileBriefV1, manual bool) map[string]string {
	params := map[string]string{"slot": mobileBriefDisplaySlot(brief, defaultMobileBriefSettings())}
	if manual {
		params[engagement.BriefRequestParam] = engagement.BriefRequestManual
	}
	if line := briefTroubleLine(brief.Missing); line != "" {
		params[briefTroubleParam] = line
	}
	return params
}

// produceBriefMissed submits brief_missed for a scheduled slot that did not
// produce a brief. The key names the slot, so each slot is said at most once.
// It reports whether the moment was accepted.
// details may carry the run's "reason" and its due "time".
func (a *App) produceBriefMissed(briefID, slot string, details ...map[string]string) bool {
	if !a.engagementActive() || a.engagement.store == nil || strings.TrimSpace(briefID) == "" {
		return false
	}
	key := engagement.DedupeKey(engagement.KindBriefMissed, briefID)
	seen, err := a.engagement.store.EngagementMomentKeyExists(context.Background(), key)
	if err != nil {
		return false
	}
	if seen {
		return true
	}
	now := a.engagement.now()
	id := mainChatEngagementMomentID("brief-missed:" + key)
	accepted := a.submitEngagementMoment("brief missed", engagement.Moment{
		ID:        id,
		Kind:      engagement.KindBriefMissed,
		SubjectID: briefID,
		DedupeKey: key,
		Stakes:    engagementBriefMissedStakes,
		ExpiresAt: now.Add(engagementBriefExpiry),
		Params:    briefMissedParams(slot, details...),
	})
	if accepted {
		return true
	}
	// A concurrent producer may have created the deterministic ID after the
	// first lookup. Treat that as accepted so its run can mark itself notified.
	seen, err = a.engagement.store.EngagementMomentKeyExists(context.Background(), key)
	return err == nil && seen
}

func briefMissedParams(slot string, details ...map[string]string) map[string]string {
	params := map[string]string{"slot": engagementBriefSlot(slot)}
	for _, detail := range details {
		for _, key := range []string{"reason", "time"} {
			if value := strings.TrimSpace(detail[key]); value != "" {
				params[key] = value
			}
		}
	}
	return params
}

// engagementBriefSlot names the brief slot the sealed notice reads.
func engagementBriefSlot(slot string) string {
	switch slot {
	case mobileBriefSlotMorning, mobileBriefSlotEvening:
		return slot
	default:
		return "on-request"
	}
}

// engagementTitle trims a title to one line of bounded length for the phone.
func engagementTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > engagementTitleMax {
		return strings.TrimSpace(string(runes[:engagementTitleMax])) + "..."
	}
	return title
}

// releaseBriefReady sends a brief_ready moment that waits for its recording
// now that the recording step ended, but never before the brief's slot. A
// moment held for another reason (the daily budget, the chat report) keeps
// its hold.
func (a *App) releaseBriefReady(brief MobileBriefV1) {
	if a == nil || a.store == nil || !a.engagementActive() || brief.BriefID == "" {
		return
	}
	ctx := context.Background()
	row, err := a.store.GetOpenEngagementMomentByKey(ctx, engagement.DedupeKey(engagement.KindBriefReady, brief.BriefID))
	if err != nil || row == nil || row.State != storage.EngagementStateScheduled || row.Reason != "" {
		return
	}
	now := a.engagement.now()
	release := now
	if svc, isService := a.mobileBriefSource().(*mobileBriefService); isService && svc != nil {
		if at, ok := svc.scheduledTimeFor(brief.Slot, now); ok && at.After(now) {
			release = at
		}
	}
	if row.HoldUntil <= release.UnixMilli() {
		return
	}
	if err := a.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, "",
		storage.EngagementMomentRow{HoldUntil: release.UnixMilli(), UpdatedAt: now.UnixMilli()}); err != nil {
		log.Printf("[engagement] the brief notice could not be sent early: %v", err)
		return
	}
	a.engagement.nudge()
}
