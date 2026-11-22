package main

// "In this brief" (computer-brief v1.0.0 rule 6 as changed by the
// 2026-11-21 conformance answers B2, computer-today v1.0.0 rule 2): every
// source the brief used, by its name in What Isoform sees. Each evidence ref gets its group when the
// brief is made, so the computer and the phone list the same kinds and the
// band can count the watch conversations. A group is never guessed: a
// session is the watch's only when a kept watch recording names it, and a
// kind of source outside the eight has no group and is not listed.

const (
	briefGroupCalendar  = "calendar"
	briefGroupMail      = "mail"
	briefGroupMessages  = "messages"
	briefGroupReminders = "reminders"
	briefGroupCalls     = "calls"
	briefGroupWatch     = "watch"
	briefGroupAgents    = "agents"
	briefGroupScreen    = "screen"
)

// briefSourceGroup is a source kind's group. watchSessions are the activity
// sessions a watch recording made.
func briefSourceGroup(ref MobileBriefRef, watchSessions map[string]bool) string {
	switch ref.SourceKind {
	case mobileBriefCalendarSource, mobileBriefDayShapeSource:
		return briefGroupCalendar
	case mobileBriefMeetingAppSource, mobileBriefAttendanceSource, "meeting_transcript":
		// Calls and recorded meetings: a call app's session, a meeting
		// Isoform saw you attend, a meeting's transcript.
		return briefGroupCalls
	case reminderPurposeSourceKind:
		return briefGroupReminders
	case "mail", "mail_thread", mobileBriefWaitingReplySource:
		return briefGroupMail
	case mobileBriefMessageAskSource:
		return briefGroupMessages
	case "handoff", "history", "deliverable", mobileBriefThreadOutcomeSource, "schedule", "schedule_run", briefAgentSource:
		return briefGroupAgents
	case mobileBriefWatchMarkSource:
		if watchSessions[ref.SourceID] {
			return briefGroupWatch
		}
		return briefGroupScreen
	case "activity", "activity_run", "activity_outcome", "activity_summary", "activity_timeline_group", mobileBriefNoticedSource,
		"session", mobileBriefRecapSource, mobileBriefFileSource, mobileBriefWebSource:
		return briefGroupScreen
	default:
		return ""
	}
}

// mobileBriefWithSourceGroups gives each of the brief's sources its group.
func (a *App) mobileBriefWithSourceGroups(brief *MobileBriefV1) {
	if brief == nil || len(brief.Evidence) == 0 {
		return
	}
	watch := a.briefWatchSessions(brief.Evidence)
	for i := range brief.Evidence {
		brief.Evidence[i].Group = briefSourceGroup(brief.Evidence[i], watch)
	}
}

// briefWatchSessions are the activity sessions among refs that a kept
// watch recording made. Nothing is read when no ref is a session.
func (a *App) briefWatchSessions(refs []MobileBriefRef) map[string]bool {
	out := map[string]bool{}
	needed := false
	for _, ref := range refs {
		needed = needed || ref.SourceKind == mobileBriefWatchMarkSource
	}
	if !needed || a == nil {
		return out
	}
	store := a.watchRecordingStore()
	if store == nil {
		return out
	}
	recordings, err := store.all()
	if err != nil {
		return out
	}
	for _, rec := range recordings {
		if rec.State != mobileRecordingDeleted && rec.SessionID != "" {
			out[rec.SessionID] = true
		}
	}
	return out
}
