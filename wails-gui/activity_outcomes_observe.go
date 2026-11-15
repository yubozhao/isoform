package main

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
)

const activityOutcomeCommitWindowMs = int64(2 * 60 * 1000)

type activityOutcomeObservation struct {
	evidence ActivityOutcomeEvidence
}

func activityOutcomeChat(event activitymon.ActivityEvent) bool {
	if taskLifecycleBrowserDestination(strings.ToLower(event.AppBundleID), normalizeTaskWorkflowText(event.AppName)) {
		return false
	}
	return activityEvidenceChatApp(event.AppBundleID, event.AppName) || strings.EqualFold(event.AppBundleID, "com.apple.mail") || strings.EqualFold(event.AppName, "Mail")
}

type activityOutcomeInferenceContext struct {
	Common          map[string]bool
	WebDestinations map[string]string
}

func activityOutcomeWebDestination(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if u.Scheme != "https" {
		return ""
	}
	host, path := strings.ToLower(u.Hostname()), u.Path
	switch host {
	case "claude.ai":
		if path == "/" || path == "/new" || strings.HasPrefix(path, "/chat/") {
			return "Claude"
		}
	case "chatgpt.com":
		if path == "/" || strings.HasPrefix(path, "/c/") || strings.HasPrefix(path, "/g/") {
			return "ChatGPT"
		}
	case "chat.openai.com":
		if path == "/" || path == "/chat" || strings.HasPrefix(path, "/c/") {
			return "ChatGPT"
		}
	case "app.slack.com":
		if strings.HasPrefix(path, "/client/") {
			return "Slack"
		}
	case "discord.com":
		if strings.HasPrefix(path, "/channels/") {
			return "Discord"
		}
	case "teams.microsoft.com":
		if strings.HasPrefix(path, "/v2/") {
			return "Teams"
		}
	case "web.whatsapp.com":
		return "WhatsApp"
	case "web.telegram.org":
		return "Telegram"
	case "mail.google.com":
		if strings.HasPrefix(path, "/mail/") {
			return "Gmail"
		}
	}
	return ""
}

// Only captured URLs establish a web chat destination. Brand words in an
// ordinary page title carry no such meaning.
func (s *ActivityStore) activityOutcomeWebDestinations(run ActivityTimelineRunRow, events []ActivityTimelineEvent) (map[string]string, error) {
	result := map[string]string{}
	if !taskLifecycleBrowserDestination(strings.ToLower(run.AppKey), normalizeTaskWorkflowText(run.AppName)) {
		return result, nil
	}
	type snapshot struct {
		at                    int64
		target, window, title string
	}
	var snapshots []snapshot
	for _, sessionID := range run.SessionIDs {
		rows, err := s.db.Query(`SELECT timestamp,app,browser_url,
			CASE WHEN json_valid(parsed_json) THEN json_object('title',json_extract(parsed_json,'$.title'),'metadata',json_object('bundleId',json_extract(parsed_json,'$.metadata.bundleId'))) ELSE '' END
			FROM activity_snapshots WHERE session_id=? AND timestamp>=? AND timestamp<=? ORDER BY timestamp`, sessionID, run.StartMs, run.EndMs)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var snap ActivitySnapshotRow
			if err := rows.Scan(&snap.Timestamp, &snap.App, &snap.BrowserURL, &snap.ParsedJSON); err != nil {
				rows.Close()
				return nil, err
			}
			if !timelineGroupSnapshotMatches(timelineRunToGroup(run, nil), snap) || (snap.App == "" && parsedSnapshotBundleID(snap.ParsedJSON) == "") {
				continue
			}
			window := ""
			for _, event := range events {
				if event.Timestamp > snap.Timestamp {
					break
				}
				window = event.WindowTitle
			}
			title := ""
			if parsed := parseSnapshotContent(snap); parsed != nil {
				title = parsed.Title
			}
			snapshots = append(snapshots, snapshot{at: snap.Timestamp, target: activityOutcomeWebDestination(snap.BrowserURL), window: window, title: title})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].at < snapshots[j].at })
	for _, event := range events {
		for i := len(snapshots) - 1; i >= 0; i-- {
			snap := snapshots[i]
			if snap.at > event.Timestamp {
				continue
			}
			windowMatches := snap.window == event.WindowTitle
			titleMatches := snap.title == "" || strings.Contains(normalizeTaskWorkflowText(event.WindowTitle), normalizeTaskWorkflowText(snap.title))
			if event.Timestamp-snap.at <= activityOutcomeCommitWindowMs && snap.target != "" && windowMatches && titleMatches {
				result[event.EventID] = snap.target
			}
			break
		}
	}
	return result, nil
}

// Return is only a send on known messaging surfaces with exact field-diff
// capture. Email and generic browser editors require a real Send click.
func activityOutcomeReturnSends(event activitymon.ActivityEvent, source *ActivityTimelineEvent) bool {
	if source == nil || event.IMEComposing || event.Modifiers&(1<<17) != 0 || source.TypedTextCaptureMethod != activitymon.TypedTextCaptureMethodAXFieldDiff {
		return false
	}
	plainReturn := false
	for _, key := range event.GetKeyChars() {
		if key == "Return" {
			plainReturn = true
		}
	}
	if !plainReturn {
		return false
	}
	destination := taskLifecycleResultDestinationForEvent(source.ActivityEvent)
	return taskLifecycleTextCommitCanTerminate(destination) || taskLifecycleNativeDelegatedAI(strings.ToLower(event.AppBundleID), normalizeTaskWorkflowText(event.AppName))
}

func activityOutcomeDestination(event activitymon.ActivityEvent) string {
	app := strings.TrimSpace(event.AppName)
	if strings.EqualFold(app, "yansu") || strings.EqualFold(app, "isoform") {
		return "Isoform"
	}
	return app
}

func activityOutcomeUserCompleted(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if strings.ContainsAny(text, "?？") {
		return false
	}
	words := " " + strings.Join(strings.Fields(text), " ") + " "
	for _, uncertain := range []string{" when ", " if ", " once ", " after ", " later ", " tomorrow ", " not ", " almost "} {
		if strings.Contains(words, uncertain) {
			return false
		}
	}
	for _, cue := range []string{"i am done", "i'm done", "i finished", "i have finished", "we are done", "we're done", "done:", "completed:", "已完成", "我完成了", "完成了"} {
		if strings.HasPrefix(text, cue) {
			return true
		}
	}
	return false
}

func activityOutcomeEventAllowed(event activitymon.ActivityEvent) bool {
	return !event.PrivateWindow && !activitymon.IsPrivateBrowsing(event.WindowTitle) &&
		!activityEvidenceAppExcluded(event.AppBundleID, event.AppName) &&
		!activityEvidenceSecureField(event.FocusRole) && !activityEvidenceSecureField(event.ClickRole)
}

func activityOutcomeMatchedTokens(tokens, task []ActivityEvidenceToken, common map[string]bool) []ActivityEvidenceToken {
	out := []ActivityEvidenceToken{}
	for _, token := range tokens {
		if _, strong := activityEvidenceWeight(token, common); !strong {
			continue
		}
		for _, known := range task {
			_, strong := activityEvidenceWeight(known, common)
			if strong && activityEvidenceMatches(token, known) {
				out = append(out, token)
				break
			}
		}
	}
	return out
}

func activityOutcomeEventEvidence(event ActivityTimelineEvent, text string) ActivityOutcomeEvidence {
	return ActivityOutcomeEvidence{SourceKind: "activity_event", SourceID: event.EventID, ObservedAt: event.Timestamp, Tokens: extractActivityIdentifierTokens(text, event.Timestamp)}
}

// A send click must follow the token-bearing user's text in the same window.
// Visible messages, summaries and typing without a commit cannot report work.
func inferActivityMessageOutcomes(activityID string, run ActivityTimelineRunRow, task []ActivityEvidenceToken, events []ActivityTimelineEvent, contexts ...activityOutcomeInferenceContext) []ActivityOutcome {
	context := activityOutcomeInferenceContext{}
	if len(contexts) > 0 {
		context = contexts[0]
	}
	out := []ActivityOutcome{}
	var typed *ActivityTimelineEvent
	for i := range events {
		event := events[i]
		if !activityOutcomeEventAllowed(event.ActivityEvent) {
			typed = nil
			continue
		}
		if typed != nil && (event.AppBundleID != typed.AppBundleID || event.AppName != typed.AppName || event.WindowTitle != typed.WindowTitle || event.Kind == activitymon.EventFocus) {
			typed = nil
		}
		if event.Kind == activitymon.EventKeyboard && strings.TrimSpace(event.TypedText) == "" && !taskLifecycleTextCommitCandidate(event.ActivityEvent) {
			typed = nil
		}
		if event.Kind == activitymon.EventKeyboard && strings.TrimSpace(event.TypedText) != "" {
			copy := event
			copy.TypedText, _ = pii.RedactText(copy.TypedText)
			typed = &copy
		}
		chat := activityOutcomeChat(event.ActivityEvent) || context.WebDestinations[event.EventID] != ""
		kind := classifyTaskLifecycleEvent(event.ActivityEvent)
		if !chat && kind == "sent" && taskLifecycleBrowserDestination(strings.ToLower(event.AppBundleID), normalizeTaskWorkflowText(event.AppName)) {
			continue
		}
		committed := chat && activityOutcomeReturnSends(event.ActivityEvent, typed)
		if event.Kind == activitymon.EventClick {
			switch kind {
			case "sent", "submission", "merged", "published":
				committed = true
			}
		}
		if !committed {
			continue
		}
		var source *ActivityTimelineEvent
		if typed != nil && event.Timestamp >= typed.Timestamp && event.Timestamp-typed.Timestamp <= activityOutcomeCommitWindowMs && event.WindowTitle == typed.WindowTitle {
			source = typed
		}
		var evidence []ActivityOutcomeEvidence
		if source != nil {
			item := activityOutcomeEventEvidence(*source, source.TypedText)
			item.Tokens = activityOutcomeMatchedTokens(item.Tokens, task, context.Common)
			if len(item.Tokens) > 0 {
				evidence = append(evidence, item)
			}
		}
		// Non-chat completion clicks may identify the task in their own
		// title/label; chat sends always require token-bearing user text.
		if !chat && len(evidence) == 0 {
			item := activityOutcomeEventEvidence(event, event.WindowTitle+"\n"+event.ClickLabel)
			item.Tokens = activityOutcomeMatchedTokens(item.Tokens, task, context.Common)
			if len(item.Tokens) > 0 {
				evidence = append(evidence, item)
			}
		}
		if len(evidence) == 0 {
			typed = nil
			continue
		}
		if evidence[0].SourceID != event.EventID {
			evidence = append(evidence, activityOutcomeEventEvidence(event, ""))
		}
		for i := range evidence {
			evidence[i].RunID = run.ID
		}
		status, reason, target := activityOutcomeDone, "completion_click", evidence[0].Tokens[0].Value
		if chat {
			status, reason, target = activityOutcomeReported, "message_sent", activityOutcomeDestination(event.ActivityEvent)
			if destination := context.WebDestinations[event.EventID]; destination != "" {
				target = destination
			}
			if source != nil && activityOutcomeUserCompleted(source.TypedText) {
				status, reason = activityOutcomeDone, "user_completed"
			}
		}
		out = append(out, ActivityOutcome{ActivityID: activityID, RunID: run.ID, Status: status, Reason: reason, OccurredAt: event.Timestamp, Target: target, Evidence: evidence})
		typed = nil
	}
	return out
}

func inferActivityChangedTarget(activityID string, run ActivityTimelineRunRow, prior, current []activityOutcomeObservation, commonMaps ...map[string]bool) []ActivityOutcome {
	var common map[string]bool
	if len(commonMaps) > 0 {
		common = commonMaps[0]
	}
	out := []ActivityOutcome{}
	for _, after := range current {
		if after.evidence.SourceKind != "activity_snapshot" {
			continue
		}
		for _, before := range prior {
			if before.evidence.SourceKind != "activity_snapshot" {
				continue
			}
			if after.evidence.ObservedAt <= before.evidence.ObservedAt {
				continue
			}
			var bp, bf, ap, af ActivityEvidenceToken
			for _, token := range before.evidence.Tokens {
				switch token.Kind {
				case "path":
					bp = token
				case "figure":
					bf = token
				}
			}
			for _, token := range after.evidence.Tokens {
				switch token.Kind {
				case "path":
					ap = token
				case "figure":
					af = token
				}
			}
			if bp.Value == "" || bp.Value != ap.Value {
				continue
			}
			if _, strong := activityEvidenceWeight(ap, common); !strong {
				continue
			}
			bv, bu, bok := activityFigureValue(bf.Value)
			av, au, aok := activityFigureValue(af.Value)
			// A lower observed storage measurement proves this narrow target
			// changed. A different number, dimension or quoted chat does not.
			if !bok || !aok || bu != "bytes" || au != bu || av >= bv || activityFigureMatches(bf.Value, af.Value) {
				continue
			}
			out = append(out, ActivityOutcome{ActivityID: activityID, RunID: run.ID, Status: activityOutcomeFixed, Reason: "target_changed", OccurredAt: after.evidence.ObservedAt, Target: ap.Value, Evidence: []ActivityOutcomeEvidence{before.evidence, after.evidence}})
			break
		}
	}
	return out
}

// ObserveActivityRunOutcome is shared by composition and the lifecycle pass.
// It uses actual redacted capture; model summaries cannot manufacture outcomes.
func (s *ActivityStore) ObserveActivityRunOutcome(activityID string, run ActivityTimelineRunRow, nowMs int64) error {
	allowed, err := s.RefreshTimelineRunEvidence(&run)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	return s.observeActivityRunOutcomeWithEvidence(activityID, run, nowMs)
}

// Exact AX pairs are cached independently of the run token budget.
func activityOutcomeHasMeasurementTokens(tokens []ActivityEvidenceToken, common map[string]bool) bool {
	path, figure := false, false
	for _, token := range tokens {
		switch token.Kind {
		case "path":
			_, strong := activityEvidenceWeight(token, common)
			path = path || strong
		case "figure":
			_, unit, ok := activityFigureValue(token.Value)
			figure = figure || (ok && unit == "bytes")
		}
	}
	return path && figure
}

// The composer has already refreshed the current run. Source fingerprints
// prevent repeated lifecycle passes from re-reading identical capture; member
// evidence and privacy are checked without loading message or screen bodies.
func (s *ActivityStore) observeActivityRunOutcomeWithEvidence(activityID string, run ActivityTimelineRunRow, nowMs int64) error {
	allowed, err := s.timelineRunEvidenceAllowed(run)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	activity, err := s.GetActivityByID(activityID)
	if err != nil || activity == nil {
		return err
	}
	cutoff := min(nowMs, run.EndMs)
	currentFingerprint, err := s.timelineRunEvidenceFingerprint(run)
	if err != nil {
		return err
	}
	type source struct {
		ID, Fingerprint string
		Tokens          []ActivityEvidenceToken
		Allowed         bool
	}
	sources := []source{{ID: run.ID, Fingerprint: currentFingerprint, Tokens: run.EvidenceTokens, Allowed: true}}
	var members []ActivityTimelineRunRow
	var task []ActivityEvidenceToken
	for _, id := range composeParseStrings(activity.RunIDs) {
		if id == run.ID {
			continue
		}
		member, err := s.GetTimelineRunByID(id)
		if err != nil {
			return err
		}
		if member == nil {
			continue
		}
		// A prior run can receive late AX capture outside the lifecycle
		// window. Refresh uses its persisted tokens unless its source changed.
		ok, err := s.RefreshTimelineRunEvidence(member)
		if err != nil {
			return err
		}
		fingerprint, err := s.timelineRunEvidenceFingerprint(*member)
		if err != nil {
			return err
		}
		sources = append(sources, source{ID: id, Fingerprint: fingerprint, Tokens: member.EvidenceTokens, Allowed: ok})
		if !ok || member.StartMs >= cutoff {
			continue
		}
		members = append(members, *member)
		for _, token := range member.EvidenceTokens {
			if token.LastSeenMs <= cutoff {
				task = mergeActivityEvidenceTokens(task, []ActivityEvidenceToken{token})
			}
		}
	}
	if len(task) == 0 {
		for _, token := range run.EvidenceTokens {
			if token.LastSeenMs <= cutoff {
				task = append(task, token)
			}
		}
	}
	common, err := s.commonComposerEvidence(cutoff)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(struct {
		Policy  string
		Sources []source
		RunIDs  string
		Common  map[string]bool
	}{"activity-outcomes-v4", sources, activity.RunIDs, common})
	fingerprint := deterministicTaskEpisodeID("activity-outcome-observation", string(payload))
	messageFingerprint := activityOutcomeMessageContextFingerprint(task, activity.RunIDs, common)
	var observedFingerprint, observedRewriteFingerprint, observedMessageFingerprint string
	var observedUntil, observedRowID int64
	err = s.db.QueryRow(`SELECT source_fingerprint,observed_until_ms,event_rewrite_fingerprint,event_row_id,message_context_fingerprint FROM activity_outcome_observations WHERE activity_id=? AND run_id=?`, activityID, run.ID).Scan(&observedFingerprint, &observedUntil, &observedRewriteFingerprint, &observedRowID, &observedMessageFingerprint)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && observedFingerprint == fingerprint && observedUntil >= cutoff {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := refreshActivityOutcomeStatusTx(tx, activityID, nowMs); err != nil {
			return err
		}
		return tx.Commit()
	}
	var current, measurements []activityOutcomeObservation
	if activityOutcomeHasStrongPath(run.EvidenceTokens, common) {
		observations, err := s.activityOutcomeMeasurements(run)
		if err != nil {
			return err
		}
		for _, observation := range observations {
			if observation.evidence.ObservedAt <= cutoff {
				current = append(current, observation)
			}
		}
	}
	if len(current) > 0 {
		for _, member := range members {
			if !activityOutcomeHasStrongPath(member.EvidenceTokens, common) {
				continue
			}
			observations, err := s.activityOutcomeMeasurements(member)
			if err != nil {
				return err
			}
			measurements = append(measurements, observations...)
		}
	}
	rewriteFingerprint, err := s.activityOutcomeEventRewriteFingerprint(run)
	if err != nil {
		return err
	}
	var eventStart int64
	if observedUntil > 0 && observedRewriteFingerprint == rewriteFingerprint && observedMessageFingerprint == messageFingerprint {
		eventStart, err = s.activityOutcomeEventReadStart(run, observedUntil, observedRowID)
		if err != nil {
			return err
		}
	}
	boundedEvents, err := s.loadTimelineRunEventsBetween(run, eventStart, cutoff)
	if err != nil {
		return err
	}
	webDestinations, err := s.activityOutcomeWebDestinations(run, boundedEvents)
	if err != nil {
		return err
	}
	outcomes := inferActivityMessageOutcomes(activityID, run, task, boundedEvents, activityOutcomeInferenceContext{Common: common, WebDestinations: webDestinations})
	outcomes = append(outcomes, inferActivityChangedTarget(activityID, run, append(measurements, current...), current, common)...)
	sort.SliceStable(outcomes, func(i, j int) bool { return outcomes[i].OccurredAt < outcomes[j].OccurredAt })
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range outcomes {
		atCommon, err := commonComposerEvidence(tx, item.OccurredAt)
		if err != nil {
			return err
		}
		if !sanitizeActivityOutcomeEvidence(&item, atCommon) {
			continue
		}
		if err := appendActivityOutcomeTx(tx, item, nowMs); err != nil {
			return err
		}
	}
	if err := refreshActivityOutcomeStatusTx(tx, activityID, nowMs); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO activity_outcome_observations(activity_id,run_id,source_fingerprint,observed_until_ms,event_rewrite_fingerprint,event_row_id,message_context_fingerprint) VALUES(?,?,?,?,?,?,?) ON CONFLICT(activity_id,run_id) DO UPDATE SET source_fingerprint=excluded.source_fingerprint, observed_until_ms=excluded.observed_until_ms, event_rewrite_fingerprint=excluded.event_rewrite_fingerprint,event_row_id=excluded.event_row_id,message_context_fingerprint=excluded.message_context_fingerprint`, activityID, run.ID, fingerprint, cutoff, rewriteFingerprint, run.LastEventRowID, messageFingerprint); err != nil {
		return err
	}
	return tx.Commit()
}
