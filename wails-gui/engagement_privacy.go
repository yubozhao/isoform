package main

import (
	"context"
	"strings"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/internal/engagement"
	"wails-gui/internal/pii"
	"wails-gui/internal/proactivevoice"
)

// Pending rows are observations, not permission to disclose their old Params.
// Use the same current source readers as model voice and the brief.
func (a *App) engagementMomentAllowsRead(ctx context.Context, m engagement.Moment) bool {
	if a == nil || a.store == nil || ctx.Err() != nil || a.IsClosedEyesModeEnabled() || !a.threadOutcomeMomentAllowsRead(ctx, m) {
		return false
	}
	if m.Params["_activity_thread"] != "" {
		return true
	}
	now := time.Now()
	if a.engagement != nil {
		now = a.engagement.now()
	}
	switch m.Kind {
	case engagement.KindReminder:
		if m.Params["_list_task"] != "" {
			_, _, err := a.brainDumpReminderSource(ctx, m)
			return err == nil
		}
		return true
	case engagement.KindWorkFinished, engagement.KindWorkBlocked:
		return a.mobileBriefHandoffSourceReadable(ctx, m.SubjectID)
	case engagement.KindMeetingPrep:
		event, ok := a.proactiveVoiceCalendarEvent(ctx, m.SubjectID)
		if !ok {
			return false
		}
		current := meetingPrepMoment(event, m.CreatedAt, time.Local)
		if !engagementParamsMatch(m, current.Params, "title", "place", "people", "time") {
			return false
		}
		_, reason := a.loadProactiveVoiceSource(ctx, m, now, a.currentUILanguage())
		return reason == ""
	case engagement.KindCommitmentSlipping:
		detail, ok := a.proactiveVoicePurpose(ctx, m.SubjectID, now)
		if !ok {
			return false
		}
		current, eligible := engagementCommitmentSlippingMoment(detail, now)
		return eligible && engagementParamsMatch(m, current.Params, "title")
	case engagement.KindWaitingOnReply:
		_, reason := a.loadProactiveVoiceSource(ctx, m, now, a.currentUILanguage())
		return reason == ""
	case engagement.KindCalendarConflict:
		events, err := a.listCalendarEvents(ctx, now.Add(-24*time.Hour), now.Add(engagementConflictHorizon))
		if err != nil {
			return false
		}
		var readable []calendar.Event
		for _, event := range events {
			if _, ok := a.proactiveVoiceCalendarEvent(ctx, event.ID); ok {
				readable = append(readable, event)
			}
		}
		for day := 0; day < 2; day++ {
			shape := calendar.ShapeDay(readable, now.Add(time.Duration(day)*24*time.Hour), time.Local, now)
			for _, current := range conflictMoments(shape, now, time.Local) {
				clean, ok := sanitizeEngagementMoment(current)
				if ok && current.DedupeKey == m.DedupeKey && clean.Params["summary"] == m.Params["summary"] {
					return true
				}
			}
		}
		return false
	case engagement.KindPlaybookSuggested, engagement.KindPlaybookResult:
		if a.sopStore == nil {
			return false
		}
		entry, err := a.sopStore.Get(m.SubjectID)
		return err == nil && a.sopEntryAllowsModelRead(entry)
	case engagement.KindGoalCheckIn:
		detail, ok := a.proactiveVoicePurpose(ctx, m.SubjectID, now)
		if !ok {
			return false
		}
		current, eligible := engagementGoalCheckInMoment(detail, now)
		return eligible && engagementParamsMatch(m, current.Params, "title")
	case engagement.KindPersonReappeared:
		person, err := a.store.GetPrincipal(ctx, m.SubjectID)
		return err == nil && a.proactiveVoicePersonAllowed(ctx, person) &&
			engagementParamsMatch(m, map[string]string{"name": strings.TrimSpace(person.DisplayName)}, "name") && !personResearchAppProtected(m.Params["where"])
	case engagement.KindScheduleMissed:
		job, err := a.store.LoadCronJob(ctx, m.SubjectID)
		return err == nil && job != nil && engagementParamsMatch(m, map[string]string{"title": scheduleMissedTitle(job.Name)}, "title")
	case engagement.KindFirstWeek:
		return m.Params["stage"] == "welcome" || m.Params["stage"] == "brief" || a.firstWeekSourceReadable(ctx, m, now)
	case engagement.KindLearnedToConfirm, engagement.KindSuggestion:
		// Old proposals have no complete delivery envelope for their text.
		// Keep their local review UI; never disclose unbound observed prose.
		return false
	default:
		// Detailed source kinds also receive canonical fingerprints before
		// submission. Generated prose has its own presentation receipt.
		return true
	}
}

func engagementParamsMatch(m engagement.Moment, current map[string]string, keys ...string) bool {
	clean, allowed := sanitizeEngagementMoment(engagement.Moment{Kind: m.Kind, Params: current})
	if !allowed {
		return false
	}
	for _, key := range keys {
		if m.Params[key] != clean.Params[key] {
			return false
		}
	}
	return true
}

func (a *App) storedEngagementAllowsRead(ctx context.Context, id string) bool {
	if a == nil || a.store == nil || id == "" {
		return false
	}
	row, err := a.store.GetEngagementMoment(ctx, id)
	if err != nil || row == nil {
		return false
	}
	m, allowed := sanitizeEngagementMoment(momentFromRow(*row))
	return allowed && a.engagementDeliveryAllowsRead(ctx, m)
}

// sanitizeEngagementMoment runs before persistence and again before delivery
// for rows written by an older version. It reads only the submitted evidence;
// it never probes the foreground application or reads another data source.
func sanitizeEngagementMoment(m engagement.Moment) (engagement.Moment, bool) {
	allowed := true
	if m.Kind == engagement.KindWaitingOnReply {
		_, sensitive := pii.IsSensitiveApp("com.apple.mail")
		allowed = !sensitive && !pii.IsSensitiveAppName("Mail")
	}
	params := make(map[string]string, len(m.Params))
	for key, value := range m.Params {
		// The approved proactive voice policy drops financial and medical
		// moments before storage and every delivery surface. A flagged mail
		// subject is withheld instead, leaving its headers-only caution.
		if proactivevoice.Supported(string(m.Kind)) && proactiveVoiceRestrictedText(value) && !(m.Kind == engagement.KindWaitingOnReply && key == "subject" && proactiveVoiceMailFlag(value, "") == "money" && !proactiveVoiceHealth.MatchString(value)) {
			allowed = false
		}
		if _, sensitive := pii.IsSensitiveApp(strings.TrimSpace(value)); sensitive || pii.IsSensitiveAppName(value) {
			allowed = false
		}
		for _, app := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == '|' || r == '\n' || r == '[' || r == ']'
		}) {
			if pii.IsSensitiveAppName(strings.Trim(strings.TrimSpace(app), `"'`)) {
				allowed = false
			}
		}
		// Some observations carry app lists or a bundle id inside a source
		// reference. Check these without trusting the producer's key name.
		for _, prefix := range pii.AllBundlePrefixes() {
			if strings.Contains(strings.ToLower(value), strings.ToLower(prefix)) {
				allowed = false
			}
		}
		params[key], _ = pii.RedactText(value)
	}
	if !allowed {
		params = nil
	}
	m.Params = params
	return m, allowed
}

func sanitizeEngagementText(text engagementText) engagementText {
	text.PushBody, _ = pii.RedactText(text.PushBody)
	text.Title, _ = pii.RedactText(text.Title)
	text.PhoneTitle, _ = pii.RedactText(text.PhoneTitle)
	text.PhoneBody, _ = pii.RedactText(text.PhoneBody)
	text.EmailSubject, _ = pii.RedactText(text.EmailSubject)
	text.EmailBody, _ = pii.RedactText(text.EmailBody)
	if !engagement.ValidBody(text.Title) {
		text.Title = text.PushBody
	}
	return text
}
