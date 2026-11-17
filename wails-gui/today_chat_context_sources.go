package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/internal/openanswer"
)

func (a *App) resolveTodayChatItem(ctx context.Context, ref TodayChatContextRef, sources todayChatSources, now time.Time) (todayChatItem, error) {
	if err := ctx.Err(); err != nil {
		return todayChatItem{}, err
	}
	var record todayChatRecord
	var label string
	var prefill TodayChatPrefill
	var meetingRecipientProof string
	var refs []MobileBriefRef
	var ok bool
	switch ref.SourceKind {
	case todayChatPausedKind:
		return todayChatPausedItem(), nil
	case "topic":
		return a.resolveTodayChatTopic(ctx, ref.SourceID, sources)
	case "brief":
		return a.resolveTodayChatWholeBriefFrom(ctx, ref, sources, now)
	case "brief_item":
		return a.resolveTodayChatBriefItem(ctx, ref, sources, now)
	case todayChatBriefCardKind:
		return a.resolveTodayChatBriefCard(ctx, ref, sources, now)
	case mobileBriefWaitingReplySource:
		candidate, readable := a.mobileBriefWaitingReply(ctx, ref.SourceID, now)
		if readable {
			label = candidate.Title
			record = todayChatRecord{Kind: "open_loop", ID: ref.SourceID, At: candidate.At.UnixMilli(), Text: fmt.Sprintf("%s is waiting on your reply. Mail content and subject are withheld; open the thread before drafting specifics.", candidate.Title)}
			prefill = TodayChatPrefill{Kind: "open_loop"}
			ok = true
		}
	case "memory":
		record, ok = todayChatFindRecord(sources.Memories, ref.SourceID)
		label = extractTitle(record.Text)
	case "knowledge":
		record, ok = todayChatFindRecord(sources.Knowledge, ref.SourceID)
		label = extractTitle(record.Text)
	case "principal":
		for _, person := range sources.People {
			if person.ID == ref.SourceID {
				label = person.DisplayName
				record = todayChatRecord{Kind: "principal", ID: person.ID, At: person.UpdatedAt, Text: mobileBriefJoinParts(person.DisplayName, person.Relationship, person.Notes)}
				ok = true
				break
			}
		}
	case "chat_message":
		if a.store == nil {
			break
		}
		answer, isPost, err := a.store.OpenAnswerForMessage(ctx, ref.SourceID)
		if err == nil && isPost && (answer.State == "open" || answer.State == "settled" || answer.State == "saw_it") {
			message, err := a.store.LoadStreamItem(ctx, ref.SourceID)
			candidate := openanswer.Candidate{Question: answer.Question, Topic: answer.Topic, Source: answer.Source}
			if err == nil && message != nil && message.Kind == "assistant" && openAnswerCandidateTextAllowed(candidate) &&
				openAnswerBriefTextAllowed(message.Content) {
				muted, muteErr := a.store.OpenAnswerKindMuted(ctx, answer.Topic)
				if muteErr == nil && !muted && !a.IsClosedEyesModeEnabled() {
					label = answer.Question
					record = todayChatRecord{Kind: "chat_message", ID: message.ID, At: message.CreatedAt, Text: message.Content}
					prefill = todayChatSourcePrefill("chat_message", record, now)
					ok = true
				}
			}
		}
	case "purpose":
		for _, purpose := range sources.Purposes {
			if purpose.OpenLoop.ID == ref.SourceID {
				label = purpose.Current.Title
				record = todayChatRecord{Kind: "purpose", ID: purpose.OpenLoop.ID, At: purpose.Current.OccurredAt, Text: mobileBriefJoinParts(purpose.Current.Title, purpose.Current.DesiredOutcome, purpose.Current.Counterparty, purpose.Current.WaitingCondition),
					Owned: true, Facts: todayChatPurposeFacts(purpose, now)}
				prefill = todayChatPurposePrefill(purpose)
				ok = true
				break
			}
		}
	case "interest":
		return a.resolveTopicInterest(ctx, ref.SourceID, sources)
	case "calendar":
		if event, allowed := a.proactiveVoiceCalendarEvent(ctx, ref.SourceID); allowed {
			label = event.Title
			record = todayChatRecord{Kind: "calendar", ID: event.ID, At: event.Start.UnixMilli(), Text: event.Title, Facts: []todayChatFact{{Name: "start", At: event.Start.UnixMilli()}, {Name: "end", At: event.End.UnixMilli()}}}
			if ref.MeetingPrep || ref.MeetingNote {
				var meeting *TodayBeforeYouTalk
				var err error
				if ref.MeetingNote {
					meeting, err = a.todayBeforeYouTalkMeetingAt(ctx, ref.SourceID, now)
				} else {
					var target TodayDayMeetingTarget
					target, err = a.todayDayMeetingTargetAt(ctx, ref.SourceID, now)
					if target.Kind == "prep" {
						meeting = target.Meeting
					}
				}
				if err != nil || meeting == nil || meeting.MeetingID != ref.SourceID || ref.MeetingStartMs != 0 && meeting.StartAt != ref.MeetingStartMs {
					return todayChatItem{}, errTodayChatItemUnavailable
				}
				names := []string{meeting.PersonName}
				for _, attendee := range event.Attendees {
					name := strings.TrimSpace(attendee.Name)
					if !attendee.IsMe && attendee.Status != calendar.StatusDeclined && name != "" && mobileBriefCopySafe(name) && redactThreadOutcomeText(name) == name && !slices.Contains(names, name) {
						names = append(names, name)
					}
				}
				record.Text = mobileBriefJoinParts(event.Title, "Attendees: "+strings.Join(names, ", "), "Last time: "+meeting.LastTime, "Still open: "+meeting.YouOwe, "They asked: "+meeting.TheyAsked)
				kind := "meeting_prep"
				if ref.MeetingNote {
					principal, name, address, subject, err := a.todayMeetingNoteRecipient(ctx, ref.SourceID)
					if err != nil {
						return todayChatItem{}, err
					}
					meetingRecipientProof = proactiveVoiceHash([]string{principal, name, address, subject})
					kind = "meeting_note"
				}
				prefill = TodayChatPrefill{Kind: kind, StartsAt: event.Start.UnixMilli(), People: []string{meeting.PersonName}}
			}
			ok = true
		}
	case "find":
		// Resolving a selected find needs only current stored items, not the
		// topic ranking, journal refresh, or schedule/provider display status.
		state := a.topicsSvc().store.load()
		for _, find := range topicFindsFresh(state, now) {
			if find.ID == ref.SourceID {
				label = find.Title
				record = todayChatRecord{Kind: "find", ID: find.ID, At: mobileBriefParseTime(find.PublishedAt).UnixMilli(), Text: mobileBriefJoinParts(find.Title, find.Topic, find.WhatsNew, find.Source, find.URL)}
				ok = true
				break
			}
		}
	case "news":
		if job := a.findTodayNewsJobContext(ctx); job != nil {
			runs, _ := a.getCronRunsContext(ctx, job.ID)
			for _, run := range runs {
				if run.Status != "ok" {
					continue
				}
				at := max(run.FinishedAt, run.StartedAt)
				if ref.BriefID != "" && time.UnixMilli(at).In(time.Local).Format("2006-01-02") != ref.BriefID {
					continue
				}
				items, _ := todayNewsItemsFromRun(run.ResultSummary)
				for i, item := range items {
					if item.URL == ref.SourceID || item.Title == ref.SourceID || fmt.Sprintf("%s:%d", run.ID, i) == ref.SourceID {
						label = item.Title
						record = todayChatRecord{Kind: "news", ID: item.URL, At: at, Text: mobileBriefJoinParts(item.Title, item.Summary, item.Source, item.URL)}
						ok = true
						break
					}
				}
				if ok {
					break
				}
			}
		}
	case "schedule":
		for _, job := range a.listCronJobsContext(ctx) {
			if job.ID == ref.SourceID {
				label = job.Name
				// TargetConfig and execution output can contain mail/draft
				// payloads. Only the schedule's display metadata belongs here.
				record = todayChatScheduleRecord(job)
				ok = true
				break
			}
		}
	case "schedule_run":
		runs, _ := a.getCronRunsByDateRangeContext(ctx, now.Add(-14*24*time.Hour).UnixMilli(), now.UnixMilli())
		for _, run := range runs {
			if run.ID == ref.SourceID {
				label = run.JobName
				record = todayChatRecord{Kind: "schedule_run", ID: run.ID, At: max(run.FinishedAt, run.StartedAt), Text: mobileBriefJoinParts(run.JobName, "status: "+run.Status, "trigger: "+run.TriggerKind),
					Owned: true, OwnedText: true, Facts: todayChatStatusFacts(run.Status, 0)}
				ok = true
				break
			}
		}
	case "handoff", "history":
		if a.handoffStore != nil {
			items, _ := a.handoffStore.List()
			for _, item := range items {
				if item.ID == ref.SourceID && a.mobileBriefHandoffReadable(ctx, item) {
					// Body is a plan and may contain transcripts or mail drafts.
					label = item.Title
					record = todayChatRecord{Kind: "handoff", ID: item.ID, At: item.UpdatedAt, Text: mobileBriefJoinParts(item.Title, "status: "+string(item.Status)),
						Owned: true, OwnedText: true, Facts: todayChatStatusFacts(string(item.Status), item.UpdatedAt)}
					ok = true
					break
				}
			}
		}
	case todayChatWorkKind:
		if list, err := a.workListNow(); err == nil {
			if item, found := workFind(list, ref.SourceID); found {
				label = item.Work.Title
				record = todayChatRecord{Kind: todayChatWorkKind, ID: item.ID, At: mobileBriefParseTime(item.CreatedAt).UnixMilli(), Text: mobileBriefJoinParts(item.Work.Title, item.Work.Text, "state: "+item.State),
					Owned: true, OwnedText: true}
				ok = true
			}
		}
	case "deliverable":
		items, _ := a.listDeliverablesContext(ctx)
		for _, item := range items {
			if item.ID == ref.SourceID && a.todayChatDeliverableAllowed(ctx, item, sources, now) {
				label = item.Title
				record = todayChatRecord{Kind: "deliverable", ID: item.ID, At: item.UpdatedAt, Text: mobileBriefJoinParts(item.Title, "status: "+item.Status),
					Owned: true, OwnedText: true, Facts: todayChatStatusFacts(item.Status, item.UpdatedAt)}
				ok = true
				break
			}
		}
	case "summary", "summary_week", "activity":
		if strings.HasPrefix(ref.SourceID, threadOutcomeMomentPrefix) {
			if a.threadOutcomeSourceReadable(ctx, ref.SourceID) {
				id := strings.TrimPrefix(ref.SourceID[:strings.LastIndex(ref.SourceID, ":")], threadOutcomeMomentPrefix)
				row, err := a.activity.store.GetActivityByID(id)
				if err == nil && row != nil {
					if outcome := a.readableThreadOutcome(ctx, *row); outcome != nil {
						label = outcome.Summary
						record = todayChatRecord{Kind: mobileBriefThreadOutcomeSource, ID: ref.SourceID, At: row.EndMs, Text: mobileBriefJoinParts(outcome.Summary, "Observed state: "+outcome.State, "Proposed next step (requires user instruction): "+outcome.NextStep, outcome.Question)}
						ok = true
					}
				}
			}

			break
		}
		record, ok = todayChatFindRecord(sources.Activities, ref.SourceID)
		if !ok && ref.SourceKind == "activity" {
			// A moment from a day older than the lookback (Chat about this
			// on Activity), read by id with the same gates.
			record, ok = a.todayChatActivityByID(ctx, ref.SourceID)
		}
		if ok && ref.DayStartMs != 0 {
			if a.activity == nil || a.activity.store == nil {
				return todayChatItem{}, errTodayChatItemUnavailable
			}
			row, err := a.activity.store.GetActivityByID(ref.SourceID)
			end := activityThreadDayEnd(ref.DayStartMs)
			if err != nil || row == nil || row.StartMs >= end || row.EndMs <= ref.DayStartMs {
				return todayChatItem{}, errTodayChatItemUnavailable
			}
			if row.StartMs < ref.DayStartMs || row.EndMs > end {
				spent, _, err := a.activityMomentTimeMs(ctx, ref.DayStartMs, end)
				if err != nil {
					return todayChatItem{}, err
				}
				record.DurationMinutes = int((time.Duration(spent[ref.SourceID]) * time.Millisecond).Round(time.Minute) / time.Minute)
			}
			record.At = min(row.EndMs, end-1)
		}
		label = record.Label
		if !ok && (ref.SourceKind == "summary" || ref.SourceKind == "summary_week") {
			if date, err := time.ParseInLocation("2006-01-02", ref.SourceID, time.Local); err == nil {
				start, days := date, 1
				if ref.SourceKind == "summary_week" {
					start, days = date.AddDate(0, 0, -(int(date.Weekday())+6)%7), 7
				}
				end := start.AddDate(0, 0, days)
				var rows []todayChatRecord
				for _, activity := range sources.Activities {
					at := time.UnixMilli(activity.At)
					if !at.Before(start) && at.Before(end) {
						rows = append(rows, activity)
						refs = append(refs, MobileBriefRef{SourceKind: activity.Kind, SourceID: activity.ID, Label: activity.Text})
					}
				}
				// The cached daily narrative has no per-field provenance. Build
				// from currently allowed summaries instead of replaying it.
				text, _ := packTodayChatSection(rows, todayChatItemTokens/2)
				record = todayChatRecord{Kind: ref.SourceKind, ID: ref.SourceID, At: date.UnixMilli(), Text: "Activity summary for " + start.Format("2006-01-02") + " to " + end.AddDate(0, 0, -1).Format("2006-01-02") + "\n" + text}
				ok = true
			}
		}
	case "meeting":
		if a.activity != nil && a.activity.store != nil {
			rows, err := a.activity.store.GetSessionsByIDsContext(ctx, []string{ref.SourceID})
			row := rows[ref.SourceID]
			if err == nil && row != nil && a.todayChatDerivedAllowedContext(ctx, []string{row.MeetingApp}, []string{ref.SourceID}, "") {
				label = row.MeetingApp
				// TranscriptPath is deliberately not read or included.
				record = todayChatRecord{Kind: "meeting", ID: row.ID, At: row.EndTime, Text: fmt.Sprintf("Meeting in %s; %s; %d minutes", row.MeetingApp, scrubSensitiveAppNames(row.AppNames), (row.EndTime-row.StartTime)/60000)}
				ok = true
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return todayChatItem{}, err
	}
	if !ok || strings.TrimSpace(record.Text) == "" {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	if strings.TrimSpace(label) == "" {
		// A source without a title still has a safe, stable identity. Never
		// substitute its free-form body into the visible generated draft.
		label = record.ID
	}
	if prefill.Kind == "" {
		prefill = todayChatSourcePrefill(ref.SourceKind, record, now)
	}
	return todayChatItem{Record: record, Label: label, Prefill: prefill, Refs: refs, OwnedLabel: record.Owned, MeetingRecipientProof: meetingRecipientProof}, nil
}

// The text is unchanged so stored brief proofs keep matching it; it is built
// only from the schedule's own fields, so its run times stay readable. The
// facts add the runs' ages.
func todayChatScheduleRecord(job CronJobState) todayChatRecord {
	return todayChatRecord{Kind: "schedule", ID: job.ID, At: job.UpdatedAt, Text: fmt.Sprintf("%s; %s; enabled: %t; next run: %s; last status: %s", job.Name, job.ScheduleDescription, job.Enabled, mobileBriefMillis(job.NextRunAt).Format(time.RFC3339), job.LastRunStatus),
		Owned: true, OwnedText: true, Facts: todayChatScheduleFacts(job)}
}

// A deliverable is a projection, not independent evidence. Revalidate its
// current backing records before its title can enter model context. In
// particular, the chat and automation aliases of a handoff retain its gates.
func (a *App) todayChatDeliverableAllowed(ctx context.Context, item DeliverableEntry, sources todayChatSources, now time.Time) bool {
	if !a.deliverableFolderReadFilter(ctx)(item) || item.SourceID == "" || item.ID != item.Source+":"+item.SourceID {
		return false
	}
	checkHandoff := func(id string) bool {
		_, err := a.resolveTodayChatItem(ctx, TodayChatContextRef{SourceKind: "handoff", SourceID: id}, sources, now)
		return err == nil
	}
	if item.Source == deliverableSourceHandoff {
		return checkHandoff(item.SourceID)
	}
	if a.store == nil {
		return false
	}
	visiting := map[string]bool{}
	var checkSession func(string) bool
	checkJob := func(id string) bool {
		key := "job:" + id
		if visiting[key] {
			return false
		}
		visiting[key] = true
		defer delete(visiting, key)
		job, err := a.store.LoadCronJob(ctx, id)
		if err != nil || job == nil || !a.todayChatDerivedAllowedContext(ctx, nil, nil, job.SourcePlatform) {
			return false
		}
		var config cronTargetConfig
		if json.Unmarshal([]byte(job.TargetConfig), &config) != nil || (config.HandoffID != "" && !checkHandoff(config.HandoffID)) {
			return false
		}
		if a.handoffStore != nil {
			entries, err := a.handoffStore.List()
			if err != nil {
				return false
			}
			for _, entry := range entries {
				if entry.CronJobID == id && !checkHandoff(entry.ID) {
					return false
				}
			}
		}
		if job.SourceSessionID != "" {
			_, activityOK := todayChatFindRecord(sources.Activities, job.SourceSessionID)
			if !activityOK {
				// Activity and chat IDs share the UUID namespace. Read only
				// existence here: a protected Activity must not fall through
				// to an unrelated allowed chat with the same ID.
				activityExists := false
				if a.activity == nil || a.activity.store == nil || a.activity.store.db == nil {
					return false
				}
				err := a.activity.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM activity_sessions WHERE id = ?)`, job.SourceSessionID).Scan(&activityExists)
				if err != nil {
					return false
				}
				if activityExists {
					if !a.todayChatDerivedAllowedContext(ctx, nil, []string{job.SourceSessionID}, "") {
						return false
					}
				} else if !checkSession(job.SourceSessionID) {
					return false
				}
			}
		}
		return ctx.Err() == nil
	}
	checkSession = func(id string) bool {
		key := "session:" + id
		if id == "" || visiting[key] {
			return false
		}
		visiting[key] = true
		defer delete(visiting, key)
		session, err := a.store.LoadSession(ctx, id)
		if err != nil || session == nil {
			return false
		}
		if session.MeetingID != "" && !a.todayChatDerivedAllowedContext(ctx, nil, []string{session.MeetingID}, "") {
			return false
		}
		handoffFound := false
		if a.handoffStore != nil {
			entries, err := a.handoffStore.List()
			if err != nil {
				return false
			}
			for _, entry := range entries {
				if entry.ChatSessionID == id {
					if !checkHandoff(entry.ID) {
						return false
					}
					handoffFound = true
				}
			}
		}
		if !handoffFound {
			if strings.HasPrefix(session.Label, handoffSessionLabelPrefix) {
				return false
			}
			// Legacy execution sessions have no immutable origin field.
			// Their Handoff group membership survives a session rename.
			groups, items, err := a.store.LoadAllSidebarGroups(ctx)
			if err != nil {
				return false
			}
			for _, group := range groups {
				if group.Name != handoffsGroupName {
					continue
				}
				for _, entry := range items[group.ID] {
					if entry.ItemType == "chat" && entry.RefID == id {
						return false
					}
				}
			}
		}
		if session.AutomationResult || strings.HasPrefix(session.Label, "Cron session ") {
			// Continue in Chat has its own visible session ID. Its first
			// user turn retains the originating job ID beside display-only
			// output; only that ID participates in context resolution.
			first, err := a.store.LoadStreamItemsAfter(ctx, id, -1, 1)
			if err != nil {
				return false
			}
			if len(first.Items) != 0 && strings.TrimSpace(first.Items[0].AutomationContext) != "" {
				var origin struct {
					JobID string `json:"jobId"`
				}
				if first.Items[0].Kind != "user" || json.Unmarshal([]byte(first.Items[0].AutomationContext), &origin) != nil || strings.TrimSpace(origin.JobID) == "" {
					return false
				}
				// An explicit origin must not fall back to an unrelated job
				// that happens to use this session as its execution target.
				return checkJob(origin.JobID)
			}
			jobs, err := a.store.ListCronJobs(ctx)
			if err != nil {
				return false
			}
			for _, job := range jobs {
				var config cronTargetConfig
				if json.Unmarshal([]byte(job.TargetConfig), &config) == nil && config.SessionTag == id && checkJob(job.ID) {
					return true
				}
			}
			return false
		}
		return ctx.Err() == nil
	}
	switch item.Source {
	case deliverableSourceChat:
		row, err := a.store.GetChatDeliverable(ctx, item.SourceID)
		return err == nil && row != nil && checkSession(row.SessionID)
	case deliverableSourceAutomation:
		jobID, runID, found := strings.Cut(item.SourceID, ":")
		if !found || runID == "" || !checkJob(jobID) {
			return false
		}
		runs, err := a.store.ListCronRuns(ctx, jobID, todayChatCandidateLimit)
		if err != nil {
			return false
		}
		for _, run := range runs {
			if run.ID == runID {
				return checkSession(run.SessionTag)
			}
		}
	}
	return false
}

func (a *App) resolveTodayChatBriefItem(ctx context.Context, ref TodayChatContextRef, sources todayChatSources, now time.Time) (todayChatItem, error) {
	dir, err := mobileBriefStateDir()
	if err != nil {
		return todayChatItem{}, err
	}
	brief, found := newMobileBriefStore(dir).load(ref.BriefID)
	if !found {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	selected, ok := mobileBriefItemByID(brief, ref.SourceID)
	if !ok || len(selected.Refs) == 0 {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	out, err := a.resolveTodayChatBriefRefs(ctx, brief, selected.Refs, sources, now)
	if err != nil {
		return todayChatItem{}, err
	}
	out.PriorLabels = []string{selected.Title}
	if out.Record.Kind == "purpose" {
		for _, section := range [][]MobileBriefItem{brief.Sections.OpenLoops, brief.Sections.WaitingOnYou} {
			for _, item := range section {
				if item.ID == selected.ID {
					out.Prefill.Kind = "open_loop"
				}
			}
		}
	}
	return out, nil
}

func mobileBriefItemByID(brief MobileBriefV1, id string) (MobileBriefItem, bool) {
	for _, section := range [][]MobileBriefItem{brief.Sections.Moved, brief.Sections.Doing, brief.Sections.Watching, brief.Sections.Meetings, brief.Sections.People, brief.Sections.OpenLoops, brief.Sections.Learned, brief.Sections.Deliverables, brief.Sections.Schedules, brief.Sections.WorkingToward, brief.Sections.Commitments, brief.Sections.WaitingOnYou, brief.Sections.Review, brief.Sections.Decide, brief.Sections.Plan, brief.Sections.Highlights, brief.Sections.DayAhead, brief.Upcoming} {
		for _, item := range section {
			if item.ID == id {
				return item, true
			}
		}
	}
	return MobileBriefItem{}, false
}

// resolveTodayChatBriefRefs reads a brief card's evidence refs again at this
// read. The first ref is the card's canonical source; every ref must still be
// readable through its own current gates, or the card is unavailable.
func (a *App) resolveTodayChatBriefRefs(ctx context.Context, brief MobileBriefV1, refIDs []string, sources todayChatSources, now time.Time) (todayChatItem, error) {
	if len(refIDs) == 0 {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	var out todayChatItem
	var current []string
	var labels []string
	// The joined text keeps the primary's owned redaction only when every part
	// is Isoform's own text; a captured secondary source keeps full redaction.
	ownedText := true
	for index, key := range refIDs {
		if err := ctx.Err(); err != nil {
			return todayChatItem{}, err
		}
		var evidence *MobileBriefRef
		for _, candidate := range brief.Evidence {
			if candidate.Ref == key {
				copy := candidate
				evidence = &copy
				break
			}
		}
		if evidence == nil {
			return todayChatItem{}, errTodayChatItemUnavailable
		}
		// Purpose cards carry display-only execution and observation links in
		// addition to their canonical source. The primary Purpose has already
		// revalidated its full evidence graph; these links add no safe text.
		if index > 0 && out.Record.Kind == "purpose" && todayChatPurposeDecoration(evidence.SourceKind) {
			continue
		}
		resolved, err := a.resolveTodayChatBriefEvidence(ctx, *evidence, brief, sources, now)
		if err != nil {
			return todayChatItem{}, err
		}
		if index == 0 {
			// Keep the primary source identity for Purpose decoration handling
			// and context matching; the brief ref remains the UI identity.
			out.Prefill = resolved.Prefill
			out.Record = resolved.Record
			out.Record.Text = ""
			out.Label = resolved.Label
			out.OwnedLabel = resolved.OwnedLabel
		}
		ownedText = ownedText && resolved.Record.OwnedText
		// Always use canonical current source text. Cached card copy and Context
		// previews are display-only: they may contain forgotten memory, schedule
		// instructions or free-form drafts without field-level provenance.
		// Keep evidence identity, but refresh its label at the same boundary.
		current = append(current, resolved.Record.Text)
		labels = append(labels, resolved.Label)
		evidence.Label = resolved.Record.Text
		out.Refs = append(out.Refs, *evidence)
	}
	out.Record.Text = strings.Join(current, "\n")
	out.Record.OwnedText = ownedText
	// The first ref is the item's canonical source. Extra evidence labels are
	// context, not part of its title or the editable user request.
	if out.Label == "" {
		out.Label = strings.Join(labels, "; ")
	}
	return out, nil
}

// One evidence ref, read again: Isoform's stores first, then the browser,
// calendar and private folder collectors, which enforce their read-time policy.
func (a *App) resolveTodayChatBriefEvidence(ctx context.Context, evidence MobileBriefRef, brief MobileBriefV1, sources todayChatSources, now time.Time) (todayChatItem, error) {
	if sources.deferCollectors && todayChatCollectorSource(evidence.SourceKind) {
		return todayChatItem{}, errTodayChatDeferred
	}
	resolved, err := a.resolveTodayChatItem(ctx, TodayChatContextRef{SourceKind: evidence.SourceKind, SourceID: evidence.SourceID}, sources, now)
	if ctx.Err() != nil {
		return todayChatItem{}, ctx.Err()
	}
	if err == nil {
		return resolved, nil
	}
	candidate, prefill, found := a.currentTodayBriefCandidateWithPrefill(ctx, evidence, brief, now)
	if err := ctx.Err(); err != nil {
		return todayChatItem{}, err
	}
	if !found {
		return todayChatItem{}, errTodayChatItemUnavailable
	}
	return todayChatItem{
		Record:  todayChatRecord{Kind: evidence.SourceKind, ID: evidence.SourceID, At: candidate.At.UnixMilli(), Text: mobileBriefJoinParts(candidate.Title, candidate.Detail)},
		Label:   candidate.Title,
		Prefill: prefill,
	}, nil
}

// Sources whose current read is a full collection (every page visited, every
// file opened, the whole calendar day) rather than one record.
func todayChatCollectorSource(kind string) bool {
	switch kind {
	case mobileBriefWebSource, mobileBriefFileSource, mobileBriefDayShapeSource:
		return true
	default:
		return false
	}
}

func todayChatPurposeDecoration(kind string) bool {
	switch kind {
	case "activity_session", "activity_run", "activity_outcome", "engagement_moment", "chat_message":
		return true
	default:
		return false
	}
}

func (a *App) currentTodayBriefCandidate(ctx context.Context, ref MobileBriefRef, brief MobileBriefV1, now time.Time) (mobileBriefCandidate, bool) {
	candidate, _, found := a.currentTodayBriefCandidateWithPrefill(ctx, ref, brief, now)
	return candidate, found
}

func (a *App) currentTodayBriefCandidateWithPrefill(ctx context.Context, ref MobileBriefRef, brief MobileBriefV1, now time.Time) (mobileBriefCandidate, TodayChatPrefill, bool) {
	in := mobileBriefInput{Slot: brief.Slot, WindowStart: mobileBriefParseTime(brief.WindowStart), AsOf: now, Location: time.Local}
	switch ref.SourceKind {
	case mobileBriefWebSource:
		collectMobileBriefWatchingContext(ctx, a, &in)
	case mobileBriefFileSource:
		collectMobileBriefDoingContext(ctx, a, &in, false)
	case mobileBriefCalendarSource, mobileBriefDayShapeSource:
		if !a.isPermissionEnabledContext(ctx, calendarPermissionKey) || !calendarAccessGranted() {
			return mobileBriefCandidate{}, TodayChatPrefill{}, false
		}
		if ref.SourceKind == mobileBriefCalendarSource && a.store != nil {
			return a.currentTodayCalendarCandidateWithPrefill(ctx, ref.SourceID, in)
		}
		collectMobileBriefCalendarContext(ctx, a, &in)
	default:
		// Legacy learning cards have no turn-scoped, field-level provenance
		// read model. They are unavailable to Today chat.
		return mobileBriefCandidate{}, TodayChatPrefill{}, false
	}
	for _, section := range [][]mobileBriefCandidate{in.Watching, in.Doing, in.Meetings, in.Learned} {
		if ctx.Err() != nil {
			return mobileBriefCandidate{}, TodayChatPrefill{}, false
		}
		for _, candidate := range prepareMobileBriefCandidates(section, mobileBriefNewestFirst) {
			if candidate.SourceKind == ref.SourceKind && candidate.SourceID == ref.SourceID {
				return candidate, todayChatSourcePrefill(candidate.SourceKind, todayChatRecord{At: candidate.At.UnixMilli()}, now), true
			}
		}
	}
	return mobileBriefCandidate{}, TodayChatPrefill{}, false
}

// currentTodayCalendarCandidateWithPrefill reads the exact current occurrence
// after the caller has checked both calendar permission gates.
func (a *App) currentTodayCalendarCandidateWithPrefill(ctx context.Context, sourceID string, in mobileBriefInput) (mobileBriefCandidate, TodayChatPrefill, bool) {
	row, found, err := a.store.GetCalendarEvent(ctx, sourceID)
	if err != nil || !found || row.DeletedAt != 0 {
		return mobileBriefCandidate{}, TodayChatPrefill{}, false
	}
	event := calendarEventFromRow(row)
	loc := in.Location
	if loc == nil {
		loc = time.Local
	}
	from, to := mobileBriefCalendarWindow(in.Slot, in.AsOf, loc)
	if !event.Start.Before(to) || !event.End.After(from) {
		return mobileBriefCandidate{}, TodayChatPrefill{}, false
	}
	rows := mobileBriefCalendarCandidatesContext(ctx, a, []calendar.Event{event}, in.Slot, in.AsOf, loc, in.ClockLayout)
	if len(rows) == 1 && ctx.Err() == nil {
		return rows[0], todayChatCalendarPrefill(event, in.AsOf), true
	}
	return mobileBriefCandidate{}, TodayChatPrefill{}, false
}
