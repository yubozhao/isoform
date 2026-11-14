package main

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/storage"
)

// mobileBriefPurposeOutcomeSource is the adapter seam for movement step 2.
// Its ActivityOutcome carries activity/run IDs, status, reason and identifier
// evidence. An adapter must resolve that evidence's privacy before returning
// this title/summary-only projection. No transcript, OCR, token or message body
// belongs here. Nil deliberately means the outcome pipeline is not installed.
type mobileBriefPurposeOutcomeSource interface {
	BriefPurposeOutcomes(activityIDs []string, asOf time.Time) ([]mobileBriefPurposeOutcome, error)
}

type mobileBriefPurposeOutcome struct {
	ID         string
	ActivityID string
	Status     string
	Summary    string
	At         time.Time
	Apps       []string
	Protected  bool
}

type mobileBriefPurposeObservation struct {
	Evidence         []mobileBriefCandidate
	ActivityIDs      []string
	SessionIDs       []string
	LastTouch        time.Time
	AttentionMs      int64
	Sensitive        bool
	Unavailable      bool
	RetentionExpired bool
	MissingEvidence  bool
}

func mobileBriefPurposeFirst(a, b mobileBriefCandidate) bool {
	if a.Urgency != b.Urgency {
		return a.Urgency > b.Urgency
	}
	if a.AttentionMs != b.AttentionMs {
		return a.AttentionMs > b.AttentionMs
	}
	if !a.DueAt.Equal(b.DueAt) {
		if a.DueAt.IsZero() != b.DueAt.IsZero() {
			return !a.DueAt.IsZero()
		}
		return a.DueAt.Before(b.DueAt)
	}
	return mobileBriefNewestFirst(a, b)
}

func mobileBriefOldestTouchFirst(a, b mobileBriefCandidate) bool {
	if !a.LastTouch.Equal(b.LastTouch) {
		return a.LastTouch.Before(b.LastTouch)
	}
	return mobileBriefPurposeFirst(a, b)
}

func mobileBriefHasSensitiveApp(apps []string) bool {
	for _, app := range apps {
		if pii.IsSensitiveAppName(app) {
			return true
		}
	}
	return false
}

func mobileBriefPurposeCurrent(detail PurposeOpenLoopDetail) bool {
	return detail.Eligible && (detail.Current.LifecycleState == PurposeStateActive || detail.Current.LifecycleState == PurposeStateWaiting)
}

// collectMobileBriefPurposes reads the ledger and linked summaries. It never
// submits a moment, appends chat, resolves a Purpose, or interprets inactivity as
// proof that the user has not done something elsewhere.
func collectMobileBriefPurposes(a *App, in *mobileBriefInput, details []PurposeOpenLoopDetail, outcomes mobileBriefPurposeOutcomeSource) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return
	}
	loc := in.Location
	if loc == nil {
		loc = time.Local
	}
	now := in.AsOf.In(loc)
	pauses, pauseErr := a.observationPauses()
	reader := mobileBriefPurposeReader{app: a, store: a.activity.store, asOf: in.AsOf, pauses: pauses, pausesReadable: pauseErr == nil, cache: map[string]mobileBriefPurposeObservation{}}
	subjects, mailErr := mobileBriefMailSubjects(a)
	observations := map[string]mobileBriefPurposeObservation{}
	allowed := map[string]PurposeOpenLoopDetail{}
	for _, detail := range details {
		if !mobileBriefPurposeCurrent(detail) || detail.Current.OccurredAt > in.AsOf.UnixMilli() {
			continue
		}
		// Words the user wrote for a goal are theirs: they do not hold its card
		// (item 66 C3). Everything else on it is checked, and so is the
		// brief's written text, on its own.
		if mailErr != nil || mobileBriefValueEchoesMail(mobileGoalForeignText(detail), subjects) {
			in.ExcludedSensitiveItems++
			continue
		}
		observation := reader.purpose(detail)
		if observation.Sensitive {
			in.ExcludedSensitiveItems++
			continue
		}
		// A derived statement with unreadable backing evidence cannot be safely
		// projected onto another device. Missing sources are not inactivity.
		if observation.Unavailable {
			continue
		}
		if observation.MissingEvidence {
			// The Goals ledger still owns this identity and state. Missing old
			// activity cannot support any progress, timing or obligation claim.
			fallback := mobileBriefCandidate{SourceKind: "purpose", SourceID: detail.OpenLoop.ID,
				Title: detail.Current.Title, EvidenceLabel: detail.Current.Title,
				Kind: detail.OpenLoop.Kind, Status: detail.Current.LifecycleState,
				Detail:              mobileBriefPurposeText(a, "Evidence no longer on this computer.", "相关依据已不在这台电脑上。"),
				EvidenceUnavailable: true}
			in.WorkingToward = append(in.WorkingToward, fallback)
			in.OpenLoops = append(in.OpenLoops, fallback)
			continue
		}
		observations[detail.OpenLoop.ID], allowed[detail.OpenLoop.ID] = observation, detail
	}
	var commitmentKeys []string
	for id, detail := range allowed {
		if detail.OpenLoop.Kind != PurposeKindCommitment || detail.Current.DueKind != PurposeDueDate {
			continue
		}
		if due, ok := parseEngagementDue(detail.Current.DueValue, loc); ok {
			commitmentKeys = append(commitmentKeys, engagement.DedupeKey(engagement.KindCommitmentSlipping, id, due.In(loc).Format("2006-01-02")))
		}
	}
	moments := mobileBriefPurposeMoments(a, commitmentKeys, now)
	chatRefs := mobileBriefPurposeChatRefs(a, moments, in.AsOf)
	people := map[string]mobileBriefCandidate{}
	personLastTouch := map[string]time.Time{}
	personUnknown := map[string]bool{}
	for _, detail := range details {
		person := strings.ToLower(strings.TrimSpace(detail.Current.Counterparty))
		if person == "" || detail.Current.OccurredAt > in.AsOf.UnixMilli() {
			continue
		}
		observation, ok := observations[detail.OpenLoop.ID]
		if !ok {
			observation = reader.purpose(detail)
		}
		if observation.Sensitive || observation.Unavailable || observation.RetentionExpired || observation.MissingEvidence {
			personUnknown[person] = true
			continue
		}
		if observation.LastTouch.After(personLastTouch[person]) {
			personLastTouch[person] = observation.LastTouch
		}
	}
	for _, detail := range details {
		id := detail.OpenLoop.ID
		if _, ok := allowed[id]; !ok {
			continue
		}
		observation := observations[id]
		current := detail.Current
		due, _ := parseEngagementDue(current.DueValue, loc)
		if current.DueKind != PurposeDueDate {
			due = time.Time{}
		}
		title := current.Title
		if detail.OpenLoop.Kind == PurposeKindCommitment {
			title = commitmentShortName(title)
		}
		item := mobileBriefCandidate{SourceKind: "purpose", SourceID: id, Title: title, EvidenceLabel: current.Title,
			Kind: detail.OpenLoop.Kind, Status: current.LifecycleState, At: observation.LastTouch, Person: current.Counterparty, OpenedAt: mobileBriefMillis(detail.OpenLoop.CreatedAt),
			LastTouch: observation.LastTouch, DueAt: due, AttentionMs: observation.AttentionMs,
			Evidence: observation.Evidence, NextStep: mobileBriefPurposeNextStep(detail, allowed), confidence: current.Confidence, confidenceKnown: true,
			rankedSourceDigest: morningBriefPurposeSourceDigest(detail, observation)}
		item.Urgency = mobileBriefPurposeUrgency(due, now)
		item.FollowUp = activityFollowUpPurpose(detail)
		item.Detail = mobileBriefPurposeProgress(a, observation)
		if nextID := mobileBriefPurposeNextStepID(detail, allowed); nextID != "" {
			next := allowed[nextID]
			item.rankedDependencies = append(item.rankedDependencies, morningBriefSourceDependency{Kind: "purpose", ID: nextID, Digest: morningBriefPurposeSourceDigest(next, observations[nextID])})
			item.Evidence = append(append([]mobileBriefCandidate{}, item.Evidence...), mobileBriefCandidate{SourceKind: "purpose", SourceID: nextID, Title: next.Current.Title, At: mobileBriefMillis(next.Current.OccurredAt)})
		}
		if outcomes != nil && len(observation.ActivityIDs) > 0 {
			if rows, err := outcomes.BriefPurposeOutcomes(observation.ActivityIDs, in.AsOf); err == nil {
				sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.After(rows[j].At) })
				for _, row := range rows {
					if strings.TrimSpace(row.ID) == "" || row.Protected || mobileBriefHasSensitiveApp(row.Apps) || row.At.After(in.AsOf) || !containsString(observation.ActivityIDs, row.ActivityID) {
						continue
					}
					switch row.Status {
					case "reported", "fixed", "done":
						if strings.TrimSpace(row.Summary) != "" {
							// The outcome has a separate source closure, not yet part of
							// the ranked-input manifest.
							item.rankedUnbound = true
							item.Detail = mobileBriefText(strings.Join(strings.Fields(row.Summary), " "), mobileBriefTextLimit)
							item.Evidence = append(item.Evidence, mobileBriefCandidate{SourceKind: "activity_outcome", SourceID: row.ID, Title: item.Detail, At: row.At})
							if row.At.After(item.LastTouch) {
								item.LastTouch, item.At = row.At, row.At
							}
						}
					}
					break
				}
			}
		}
		in.WorkingToward = append(in.WorkingToward, item)
		// Keep the old-phone section even when chat already carries a moment.
		// New clients and the narrative deduplicate by the purpose identity.
		legacy := item
		legacy.Detail = mobileBriefFirstText(current.DesiredOutcome, current.WaitingCondition)
		if detail.OpenLoop.Kind == PurposeKindCommitment && commitmentShortName(legacy.Detail) == title {
			legacy.Detail = title
		}
		legacy.Evidence = nil
		in.OpenLoops = append(in.OpenLoops, legacy)
		if detail.OpenLoop.Kind == PurposeKindCommitment && current.Owner == PurposeOwnerUser && !due.IsZero() {
			key := engagement.DedupeKey(engagement.KindCommitmentSlipping, id, due.In(loc).Format("2006-01-02"))
			moment, hasMoment := moments[key]
			_, producerSlipping := engagementCommitmentSlippingMoment(detail, now)
			dueToday := due.In(loc).Format("2006-01-02") == now.Format("2006-01-02")
			slipping := producerSlipping || hasMoment || due.Before(now)
			if dueToday || slipping {
				commitment := item
				commitment.Status = "due_today"
				if slipping && (!dueToday || due.Before(now)) {
					commitment.Status = "slipping"
				}
				if len(observation.Evidence) == 0 {
					commitment.Detail = mobileBriefPurposeText(a, "This commitment is still open in your plan.", "这项承诺仍在你的计划中。")
					if observation.RetentionExpired {
						commitment.Detail = mobileBriefPurposeText(a, "This commitment is still open; its earlier activity is no longer retained.", "这项承诺仍未关闭；早期活动已不再保留。")
					}
				}
				if hasMoment {
					commitment.rankedUnbound = true
					commitment.MomentID = moment.ID
					commitment.Evidence = append(append([]mobileBriefCandidate{}, commitment.Evidence...), mobileBriefCandidate{
						SourceKind: "engagement_moment", SourceID: moment.ID, Title: current.Title, At: mobileBriefMillis(moment.CreatedAt)})
					if ref, ok := chatRefs[moment.ID]; ok {
						commitment.ChatSessionID, commitment.ChatMessageID = ref.ChatSessionID, ref.ChatMessageID
						commitment.Evidence = append(commitment.Evidence, mobileBriefCandidate{SourceKind: "chat_message", SourceID: ref.ChatMessageID, Title: current.Title, At: ref.At})
						// Only the commitment card links the report. Independent
						// progress remains eligible for the opening narrative.
					}
				}
				in.Commitments = append(in.Commitments, commitment)
			}
		}
		person := strings.TrimSpace(current.Counterparty)
		personKey := strings.ToLower(person)
		last := personLastTouch[personKey]
		if person == "" || personUnknown[personKey] || current.Owner != PurposeOwnerUser || detail.OpenLoop.CreatedAt <= 0 ||
			!last.IsZero() && now.Sub(last) < 21*24*time.Hour || last.IsZero() && now.Sub(mobileBriefMillis(detail.OpenLoop.CreatedAt)) < 21*24*time.Hour {
			continue
		}
		waiting := item
		// This person's last touch aggregates other Purposes too.
		waiting.rankedUnbound = true
		waiting.Title, waiting.Status, waiting.LastTouch = person, "no_recent_touch", last
		waiting.Detail = mobileBriefPurposeText(a, "No linked activity recorded in three weeks for: ", "近三周没有关联活动记录：") + current.Title
		if last.IsZero() {
			waiting.Detail = mobileBriefPurposeText(a, "No recorded touch for: ", "没有联系记录：") + current.Title
		}
		if prior, ok := people[personKey]; !ok || mobileBriefPurposeFirst(waiting, prior) {
			people[personKey] = waiting
		}
	}
	for _, person := range people {
		in.WaitingOnYou = append(in.WaitingOnYou, person)
	}
}

func mobileBriefPurposeUrgency(due, now time.Time) int {
	if due.IsZero() {
		return 0
	}
	if due.Before(now) {
		return 3
	}
	if due.In(now.Location()).Format("2006-01-02") == now.Format("2006-01-02") {
		return 2
	}
	if due.Before(now.AddDate(0, 0, 7)) {
		return 1
	}
	return 0
}

// The ledger has no next-step text. An active user-owned commitment explicitly
// blocking this purpose is a concrete map step; desired outcomes are not steps.
func mobileBriefPurposeNextStep(detail PurposeOpenLoopDetail, allowed map[string]PurposeOpenLoopDetail) string {
	if id := mobileBriefPurposeNextStepID(detail, allowed); id != "" {
		return allowed[id].Current.Title
	}
	return ""
}

func mobileBriefPurposeNextStepID(detail PurposeOpenLoopDetail, allowed map[string]PurposeOpenLoopDetail) string {
	var steps []mobileBriefCandidate
	for _, relation := range detail.Relations {
		if relation.Current.EventType != PurposeRelationEventAsserted {
			continue
		}
		other := ""
		switch relation.Relation.RelationType {
		case PurposeRelationDependsOn:
			if relation.Relation.SubjectID == detail.OpenLoop.ID {
				other = relation.Relation.ObjectID
			}
		case PurposeRelationBlocks:
			if relation.Relation.ObjectID == detail.OpenLoop.ID {
				other = relation.Relation.SubjectID
			}
		}
		related, ok := allowed[other]
		if !ok || related.OpenLoop.Kind != PurposeKindCommitment || related.Current.Owner != PurposeOwnerUser || related.Current.LifecycleState != PurposeStateActive {
			continue
		}
		steps = append(steps, mobileBriefCandidate{SourceID: other, Title: related.Current.Title, At: mobileBriefMillis(related.Current.OccurredAt)})
	}
	sort.SliceStable(steps, func(i, j int) bool { return mobileBriefNewestFirst(steps[i], steps[j]) })
	if len(steps) == 0 {
		return ""
	}
	return steps[0].SourceID
}

func mobileBriefPurposeText(a *App, english, chinese string) string {
	switch a.currentUILanguage() {
	case "zh-CN":
		return chinese
	default:
		return english
	}
}

func mobileBriefPurposeProgress(a *App, observation mobileBriefPurposeObservation) string {
	if len(observation.Evidence) == 0 {
		if observation.RetentionExpired {
			return mobileBriefPurposeText(a, "Linked activity is no longer retained.", "关联活动已不再保留。")
		}
		return mobileBriefPurposeText(a, "No linked activity recorded yet.", "尚无关联活动记录。")
	}
	latest := observation.Evidence[0]
	for _, evidence := range observation.Evidence {
		if latest.SourceKind == "activity_session" && evidence.SourceKind != "activity_session" || evidence.SourceKind == latest.SourceKind && evidence.At.After(latest.At) {
			latest = evidence
		}
	}
	return strings.Join(strings.Fields(mobileBriefJoinParts(latest.Detail, latest.Title)), " ")
}

type mobileBriefPurposeReader struct {
	app            *App
	store          *ActivityStore
	asOf           time.Time
	pauses         []observationPause
	pausesReadable bool
	cache          map[string]mobileBriefPurposeObservation
}

func (r *mobileBriefPurposeReader) purpose(detail PurposeOpenLoopDetail) mobileBriefPurposeObservation {
	var result mobileBriefPurposeObservation
	for _, evidence := range detail.Evidence {
		if evidence.SourceObservedAt > r.asOf.UnixMilli() {
			continue
		}
		if evidence.PrivacyDecision != TaskContextPrivacyInclude && evidence.PrivacyDecision != TaskContextPrivacyRedact {
			result.Unavailable = true
			continue
		}
		mergeMobileBriefPurposeObservation(&result, r.source(evidence.SourceKind, evidence.SourceID, 0, evidence.EvidenceClass == PurposeEvidenceExplicitUser))
	}
	result.Evidence = prepareMobileBriefCandidates(result.Evidence, mobileBriefNewestFirst)
	if len(result.Evidence) > 3 {
		result.Evidence = result.Evidence[:3]
	}
	return result
}

func mergeMobileBriefPurposeObservation(to *mobileBriefPurposeObservation, from mobileBriefPurposeObservation) {
	to.Sensitive = to.Sensitive || from.Sensitive
	to.Unavailable = to.Unavailable || from.Unavailable
	to.RetentionExpired = to.RetentionExpired || from.RetentionExpired
	to.MissingEvidence = to.MissingEvidence || from.MissingEvidence
	to.ActivityIDs = uniqueSortedStrings(append(to.ActivityIDs, from.ActivityIDs...))
	to.SessionIDs = uniqueSortedStrings(append(to.SessionIDs, from.SessionIDs...))
	if from.LastTouch.After(to.LastTouch) {
		to.LastTouch = from.LastTouch
	}
	for _, evidence := range from.Evidence {
		found := false
		for _, existing := range to.Evidence {
			if existing.SourceKind == evidence.SourceKind && existing.SourceID == evidence.SourceID {
				found = true
				break
			}
		}
		if !found {
			to.Evidence = append(to.Evidence, evidence)
			to.AttentionMs += evidence.AttentionMs
		}
	}
}

func (r *mobileBriefPurposeReader) source(kind, id string, depth int, explicit bool) mobileBriefPurposeObservation {
	key := kind + "\x00" + id + "\x00" + strconv.FormatBool(explicit)
	if cached, ok := r.cache[key]; ok {
		return cached
	}
	if depth > 8 {
		return mobileBriefPurposeObservation{Unavailable: true}
	}
	var result mobileBriefPurposeObservation
	switch kind {
	case "activity_session", "session", meetingFollowupSourceKind:
		result = r.sessions([]string{id})
	case "activity", "activity_summary":
		activity, err := r.store.GetActivityByID(id)
		if err != nil || activity == nil {
			result.Unavailable = true
			break
		}
		result.Sensitive = activitymon.IsPrivateBrowsing(activity.Title) || r.paused(activity.StartMs, activity.EndMs)
		result.Unavailable = !r.pausesReadable
		var runs []string
		if json.Unmarshal([]byte(activity.RunIDs), &runs) != nil || len(runs) == 0 {
			result.Unavailable = true
			break
		}
		for _, runID := range runs {
			mergeMobileBriefPurposeObservation(&result, r.source("activity_run", runID, depth+1, false))
		}
		var apps map[string]int64
		if json.Unmarshal([]byte(activity.ActiveMsByApp), &apps) == nil {
			for app := range apps {
				result.Sensitive = result.Sensitive || pii.IsSensitiveAppName(app)
			}
		}
		if !result.Sensitive && !result.Unavailable && activity.EndMs <= r.asOf.UnixMilli() && len(runs) > 0 {
			result.ActivityIDs = append(result.ActivityIDs, id)
			result.Evidence = append(result.Evidence, mobileBriefCandidate{SourceKind: "activity", SourceID: id,
				Title: activity.Title, Detail: mobileBriefFirstText(activity.LatestChange, activity.Description), At: mobileBriefMillis(activity.EndMs)})
		}
	case "activity_run", "activity_timeline_group":
		run, err := r.store.GetTimelineRunByID(id)
		if err != nil {
			result.Unavailable = true
			break
		}
		if run == nil {
			result = r.retainedSource("activity_run", id)
			break
		}
		result = r.sessionPrivacy(run.SessionIDs)
		mergeMobileBriefPurposeObservation(&result, r.retentionAvailability("activity_run", id))
		result.Unavailable = result.Unavailable || len(run.SessionIDs) == 0
		result.Sensitive = result.Sensitive || run.EvidenceExcluded || activityEvidenceAppExcluded(run.AppKey, run.AppName)
		if !result.Sensitive && !result.Unavailable && run.EndMs <= r.asOf.UnixMilli() {
			r.addRun(&result, *run)
		}
	case taskContextSourceMemoryKind, taskContextSourceKnowledgeKind, "memory", "knowledge":
		if kind == "memory" {
			kind = taskContextSourceMemoryKind
		}
		if kind == "knowledge" {
			kind = taskContextSourceKnowledgeKind
		}
		baseID, version, _ := strings.Cut(id, ":")
		var sessions, apps []string
		var content string
		var err error
		if kind == taskContextSourceMemoryKind {
			var entry MemoryEntry
			entry, err = r.app.GetMemoryEntry(baseID)
			content, apps = entry.Content, entry.Apps
		} else {
			var entry KnowledgeEntry
			entry, err = r.app.GetKnowledgeEntry(baseID)
			content, apps = entry.Content, splitMobileBriefNames(entry.Apps)
		}
		if err != nil {
			result.Unavailable = true
			break
		}
		if strings.HasPrefix(version, "history:") {
			stamp, parseErr := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(version, "history:"), ".md"), 10, 64)
			if parseErr != nil {
				result.Unavailable = true
				break
			}
			if kind == taskContextSourceMemoryKind {
				content, err = r.app.GetMemoryVersion(baseID, stamp)
			} else {
				content, err = r.app.GetKnowledgeVersion(baseID, stamp)
			}
			if err != nil {
				result.Unavailable = true
				break
			}
		}
		if kind == taskContextSourceMemoryKind {
			fm := parseMemoryFrontmatter(content)
			sessions = fm.ActivitySessionIDs
			apps = append(apps, fm.Apps...)
		} else {
			entry := parseKnowledgeFile(baseID+".md", content)
			sessions = entry.SessionIDs
			apps = append(apps, splitMobileBriefNames(entry.Apps)...)
		}
		result = r.sessions(sessions)
		result.Sensitive = result.Sensitive || mobileBriefHasSensitiveApp(apps)
	case taskContextSourceSelfKind:
		entry, err := r.store.GetSelfModelEntryByID(id)
		if err != nil {
			result.Unavailable = true
			break
		}
		// Self has its own authority vocabulary, shared with task context.
		evidenceClass, _ := taskContextSelfEvidenceClass(entry.ReasoningStatus)
		for _, evidence := range entry.Evidence {
			mergeMobileBriefPurposeObservation(&result, r.source(evidence.SourceKind, evidence.SourceID, depth+1, evidenceClass == TaskContextEvidenceExplicitUser))
		}
	case activityFollowUpSourceKind:
		// The user wrote the follow-up. The moment it came from adds context
		// while it is readable; a deleted or expired moment leaves the
		// follow-up standing on the user's own words, and a moment that is
		// now private or paused keeps it off the brief.
		linked := r.source("activity", id, depth+1, false)
		switch {
		case linked.Sensitive:
			result.Sensitive = true
		case !linked.Unavailable && !linked.MissingEvidence && !linked.RetentionExpired:
			result = linked
		}
	case reminderPurposeSourceKind:
		result.Sensitive = pii.IsSensitiveAppName("Reminders")
	default:
		// Only explicit user evidence can have no resolvable activity provenance.
		result.Unavailable = !explicit
	}
	r.cache[key] = result
	return result
}

func (r *mobileBriefPurposeReader) sessionPrivacy(ids []string) mobileBriefPurposeObservation {
	result := mobileBriefPurposeObservation{SessionIDs: uniqueSortedStrings(ids)}
	rows, err := r.store.GetSessionsByIDsIncludingProtected(ids)
	if err != nil {
		result.Unavailable = true
		return result
	}
	for _, id := range ids {
		row := rows[id]
		if row == nil {
			mergeMobileBriefPurposeObservation(&result, r.retainedSource("activity_session", id))
			continue
		}
		mergeMobileBriefPurposeObservation(&result, r.retentionAvailability("activity_session", id))
		if !r.pausesReadable {
			result.Unavailable = true
			continue
		}
		var apps []string
		if json.Unmarshal([]byte(row.AppNames), &apps) != nil {
			result.Unavailable = true
			continue
		}
		result.Sensitive = result.Sensitive || row.TriageStatus == "protected" || mobileBriefHasSensitiveApp(apps) || r.paused(row.StartTime, row.EndTime)
	}
	return result
}

// retainedSource accepts only a receipt written atomically by retention.
// Its frozen exclusion bit cannot reconstruct deleted activity, a last touch,
// or privacy settings changed after deletion. Missing activity permits only the
// content-free goal identity fallback; a read error still fails closed.
func (r *mobileBriefPurposeReader) retainedSource(kind, id string) mobileBriefPurposeObservation {
	row, err := r.store.getActivityRetentionSource(kind, id)
	if err != nil {
		return mobileBriefPurposeObservation{Unavailable: true}
	}
	if row == nil {
		return mobileBriefPurposeObservation{MissingEvidence: true}
	}
	result := r.retentionAvailability(kind, id)
	result.RetentionExpired = true
	result.Sensitive = row.Excluded
	return result
}

// sessionRemovedByRetention reports a session that is gone from the store and
// whose retained receipt (if any) does not mark it excluded. A protected
// session is still in the store, so it is not removed; an explicit delete moves
// the brief privacy print, which ends the re-seal leniency that uses this.
func (r *mobileBriefPurposeReader) sessionRemovedByRetention(id string) bool {
	if r.store == nil {
		return false
	}
	rows, err := r.store.GetSessionsByIDsIncludingProtected([]string{id})
	if err != nil || rows[id] != nil {
		return false
	}
	observation := r.retainedSource("activity_session", id)
	return !observation.Sensitive && !observation.Unavailable
}

// Missing historical provenance permits the content-free fallback. A failed
// availability lookup is unreadable evidence and must still fail closed.
func (r *mobileBriefPurposeReader) retentionAvailability(kind, id string) mobileBriefPurposeObservation {
	missing, err := r.store.activityRetentionEvidenceUnavailable(kind, id)
	return mobileBriefPurposeObservation{MissingEvidence: missing, Unavailable: err != nil}
}

func (r *mobileBriefPurposeReader) sessions(ids []string) mobileBriefPurposeObservation {
	result := r.sessionPrivacy(ids)
	if result.Sensitive || result.Unavailable || len(ids) == 0 {
		return result
	}
	runs, err := r.store.GetTimelineRunsForSessions(ids)
	if err != nil {
		result.Unavailable = true
		return result
	}
	for _, run := range runs {
		privacy := r.sessionPrivacy(run.SessionIDs)
		mergeMobileBriefPurposeObservation(&privacy, r.retentionAvailability("activity_run", run.ID))
		mergeMobileBriefPurposeObservation(&result, privacy)
		result.Sensitive = result.Sensitive || run.EvidenceExcluded || activityEvidenceAppExcluded(run.AppKey, run.AppName)
		if !privacy.Sensitive && !privacy.Unavailable && run.EndMs <= r.asOf.UnixMilli() {
			r.addRun(&result, run)
		}
	}
	// Even when summarization is pending, a linked safe session establishes
	// a recorded touch, not completion. Its raw events are never read.
	rows, _ := r.store.GetSessionsByIDs(ids)
	for _, id := range ids {
		row := rows[id]
		if row == nil || row.EndTime > r.asOf.UnixMilli() {
			continue
		}
		at := mobileBriefMillis(row.EndTime)
		if at.After(result.LastTouch) {
			result.LastTouch = at
		}
		{
			var apps []string
			_ = json.Unmarshal([]byte(row.AppNames), &apps)
			result.Evidence = append(result.Evidence, mobileBriefCandidate{SourceKind: "activity_session", SourceID: id, Title: mobileBriefJoinParts(mobileBriefPurposeText(r.app, "Recorded activity", "已记录的活动"), strings.Join(apps, ", ")), At: at})
		}
	}
	return result
}

func (r *mobileBriefPurposeReader) addRun(result *mobileBriefPurposeObservation, run ActivityTimelineRunRow) {
	at := mobileBriefMillis(run.EndMs)
	if at.After(result.LastTouch) {
		result.LastTouch = at
	}
	// Resolve exact composer membership, not a title/keyword similarity.
	rows, err := r.store.db.Query(`SELECT activity_id FROM activity_run_links WHERE run_id = ? ORDER BY activity_id`, run.ID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				result.ActivityIDs = append(result.ActivityIDs, id)
			}
		}
	}
	summaries, err := r.store.GetTimelineGroupSummaries([]string{run.ID})
	if err != nil {
		return
	}
	summary, ok := summaries[run.ID]
	if activitymon.IsPrivateBrowsing(summary.Title) {
		result.Sensitive = true
		return
	}
	if !ok {
		return
	}
	attention := int64(0)
	if !at.Before(r.asOf.AddDate(0, 0, -7)) {
		attention = max(0, run.ActiveMs)
	}
	item := mobileBriefCandidate{SourceKind: "activity_run", SourceID: run.ID, Title: summary.Title,
		Detail: summary.Description, At: at, Apps: []string{run.AppName}, AttentionMs: attention}
	result.Evidence = append(result.Evidence, item)
	result.AttentionMs += attention

}

func splitMobileBriefNames(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' })
}

func mobileBriefPurposeMoments(a *App, keys []string, now time.Time) map[string]storage.EngagementMomentRow {
	result := map[string]storage.EngagementMomentRow{}
	if a.store == nil {
		return result
	}
	ctx := context.Background()
	rows, _ := a.store.BriefEngagementMoments(ctx, string(engagement.KindCommitmentSlipping), keys, now.UnixMilli())
	for _, row := range rows {
		if row.Kind != string(engagement.KindCommitmentSlipping) || row.CreatedAt > now.UnixMilli() {
			continue
		}
		if prior, ok := result[row.DedupeKey]; !ok || row.CreatedAt > prior.CreatedAt {
			result[row.DedupeKey] = row
		}
	}
	return result
}

// Only identifiers from structured report metadata cross this boundary. The
// chat body is neither copied into the brief nor sent to its narrative model.
func mobileBriefPurposeChatRefs(a *App, moments map[string]storage.EngagementMomentRow, asOf time.Time) map[string]mobileBriefCandidate {
	out := map[string]mobileBriefCandidate{}
	if a.store == nil || len(moments) == 0 {
		return out
	}
	ctx := context.Background()
	main, err := a.store.LoadMainSession(ctx)
	if err != nil || main == nil {
		return out
	}
	chat, visible, _ := a.store.MobileChat(ctx, main.LocalID)
	keys := map[string]string{}
	var reportKeys []string
	for _, row := range moments {
		key := engagementReportKey(momentFromRow(row), mobileBriefMillis(row.CreatedAt))
		keys[key] = row.ID
		reportKeys = append(reportKeys, key)
	}
	refs, err := a.store.BriefChatReportReferences(ctx, main.LocalID, mainChatReportTool, mainChatSourceEngagement, string(engagement.KindCommitmentSlipping), reportKeys, asOf.UnixMilli())
	if err != nil {
		return out
	}
	for _, ref := range refs {
		id := keys[ref.Key]
		if _, exists := out[id]; exists {
			continue
		}
		item := mobileBriefCandidate{ChatMessageID: ref.ID, At: mobileBriefMillis(ref.CreatedAt)}
		if visible {
			item.ChatSessionID = chat.ID
		}
		out[id] = item
	}
	// Reminders delivered as proactive cards, alone or inside a digest, carry the
	// moment id instead of a report key.
	pending := make([]string, 0, len(moments))
	for _, row := range moments {
		if _, found := out[row.ID]; !found {
			pending = append(pending, row.ID)
		}
	}
	if proactive, err := a.store.BriefProactiveReferences(ctx, main.LocalID, mainChatProactiveTool, pending, asOf.UnixMilli()); err == nil {
		for _, ref := range proactive {
			if _, exists := out[ref.Key]; exists {
				continue
			}
			item := mobileBriefCandidate{ChatMessageID: ref.ID, At: mobileBriefMillis(ref.CreatedAt)}
			if visible {
				item.ChatSessionID = chat.ID
			}
			out[ref.Key] = item
		}
	}
	return out
}

// Derived purpose text cannot be separated from a protected contributing app.
// Other legacy brief sections retain their existing per-app filtering policy.
func mobileBriefCandidateSensitive(item mobileBriefCandidate) bool {
	if item.Protected || mobileBriefHasSensitiveApp(item.Apps) {
		return true
	}
	for _, source := range item.Evidence {
		if mobileBriefCandidateSensitive(source) {
			return true
		}
	}
	return false
}

func prepareMobileBriefPurposeCandidates(items []mobileBriefCandidate, less func(a, b mobileBriefCandidate) bool) []mobileBriefCandidate {
	allowed := make([]mobileBriefCandidate, 0, len(items))
	for _, item := range items {
		if !mobileBriefCandidateSensitive(item) {
			allowed = append(allowed, item)
		}
	}
	return prepareMobileBriefCandidates(allowed, less)
}

func (r *mobileBriefPurposeReader) paused(start, end int64) bool {
	for _, pause := range r.pauses {
		if start < pause.End || pause.End == 0 {
			if end >= pause.Start {
				return true
			}
		}
	}
	return false
}
