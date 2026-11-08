package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"wails-gui/internal/engagement"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/storage"
)

const (
	mainChatProactiveTool = "main_chat_proactive"
	mainChatStatusTool    = "main_chat_status"
)

var mainChatProactiveActionMu sync.Mutex
var activateMainChatPlaceApp = activitymon.ActivateApp

// Display fields are deliberately separate from prompt context. Never replay
// this payload or its plain-text fallback into a model; see proactivePromptLine.
type mainChatHoldSource struct {
	Thing HoldThing `json:"thing"`
	Due   string    `json:"due,omitempty"`
}

type mainChatProactive struct {
	// postedAt is the time the card's row takes, when it is not now. Never stored.
	postedAt                time.Time           `json:"-"`
	FirstReadVersion        int64               `json:"firstReadVersion,omitempty"`
	FirstReadHash           string              `json:"firstReadHash,omitempty"`
	FirstReadConfirmedAt    int64               `json:"firstReadConfirmedAt,omitempty"`
	FirstReadCorrectionHash string              `json:"firstReadCorrectionHash,omitempty"`
	FirstReadCorrectedAt    int64               `json:"firstReadCorrectedAt,omitempty"`
	Sentences               []string            `json:"sentences,omitempty"`
	Helps                   []string            `json:"helps,omitempty"`
	OfferCount              int                 `json:"offerCount,omitempty"`
	HoldSource              *mainChatHoldSource `json:"holdSource,omitempty"`
	sourceHash              string
	ReadReceipt             string `json:"readReceipt,omitempty"`
	ActivityOutcomeSource   string `json:"activityOutcomeSource,omitempty"`
	// UpdatedAt is when the outcome behind this card was refreshed (unix ms); the card keeps its place (chat v1.0.12).
	UpdatedAt    int64                   `json:"updatedAt,omitempty"`
	BriefID      string                  `json:"briefId,omitempty"`
	PersonName   string                  `json:"personName,omitempty"`
	MomentID     string                  `json:"momentId"`
	Kind         string                  `json:"kind"`
	Headline     string                  `json:"headline"`
	Body         string                  `json:"body"`
	Draft        bool                    `json:"draft"`
	DraftContent *mainChatProactiveDraft `json:"draftContent,omitempty"`
	VoiceVersion string                  `json:"voiceVersion,omitempty"`
	VoiceReceipt string                  `json:"voiceReceipt,omitempty"`
	ActionIDs    []string                `json:"actionIds"`
	ActionLabels map[string]string       `json:"actionLabels"`
	Target       mainChatTarget          `json:"target"`
	Evidence     []mainChatEvidenceRef   `json:"evidence"`
	ExpiresAt    int64                   `json:"expiresAt,omitempty"`
	State        string                  `json:"state"`
	// SentAt is when Mail or Slack took a sent draft (unix ms).
	SentAt     int64                            `json:"sentAt,omitempty"`
	KindMuted  bool                             `json:"kindMuted,omitempty"`
	Items      []mainChatProactive              `json:"items,omitempty"`
	Localized  map[string]mainChatProactiveCopy `json:"localized,omitempty"`
	Event      *mainChatProactiveEvent          `json:"event,omitempty"`
	Events     []mainChatProactiveEvent         `json:"events,omitempty"`
	Place      *mainChatProactivePlace          `json:"place,omitempty"`
	Work       *mainChatHandoffWork             `json:"work,omitempty"`
	Permission *mainChatWorkPermission          `json:"permission,omitempty"`
}

type mainChatProactiveEvent struct {
	Title          string `json:"title,omitempty"`
	Start          string `json:"start,omitempty"`
	End            string `json:"end,omitempty"`
	Place          string `json:"place,omitempty"`
	People         string `json:"people,omitempty"`
	ConferenceHost string `json:"conferenceHost,omitempty"`
	Private        bool   `json:"private,omitempty"`
	Minutes        int    `json:"minutes,omitempty"`
}

type mainChatProactivePlace struct {
	App      string `json:"app,omitempty"`
	BundleID string `json:"bundleId,omitempty"`
	Window   string `json:"window,omitempty"`
	At       string `json:"at,omitempty"`
}

// Draft content stays out of model history. The prompt selects catalog text by
// kind and never reads this payload, including its locally resolved address.
type mainChatProactiveDraft struct {
	To            string                      `json:"to"`
	Subject       string                      `json:"subject"`
	Text          string                      `json:"text"`
	Communication *mainChatCommunicationDraft `json:"communication,omitempty"`
	Channel       string                      `json:"channel,omitempty"`
	Recipient     string                      `json:"recipient,omitempty"`
	PrincipalID   string                      `json:"principalId,omitempty"`
	Revision      string                      `json:"revision,omitempty"`
}

type mainChatEvidenceRef struct {
	Kind   string         `json:"kind"`
	ID     string         `json:"id"`
	At     string         `json:"at,omitempty"`
	Target mainChatTarget `json:"target"`
}

type mainChatStatus struct {
	Period  string                `json:"period"`
	Entries []mainChatStatusEntry `json:"entries"`
}

type mainChatStatusEntry struct {
	ReadReceipt  string            `json:"readReceipt,omitempty"`
	MomentID     string            `json:"momentId,omitempty"`
	SentAt       int64             `json:"sentAt,omitempty"`
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Text         string            `json:"text"`
	Localized    map[string]string `json:"localized,omitempty"`
	Target       mainChatTarget    `json:"target"`
	VoiceVersion string            `json:"voiceVersion,omitempty"`
	VoiceReceipt string            `json:"voiceReceipt,omitempty"`
}

func engagementMomentIsStatus(m engagement.Moment, now time.Time) bool {
	switch m.Kind {
	case engagement.KindBriefMissed, engagement.KindPlaybookResult, engagement.KindWorkFinished:
		return true
	case engagement.KindCapturePaused:
		return m.CreatedAt.IsZero() || now.Sub(m.CreatedAt) < time.Hour
	case engagement.KindScheduleMissed:
		return m.Params["failures"] == "1"
	default:
		return false
	}
}

func engagementChatTarget(m engagement.Moment) (mainChatTarget, string) {
	target := mainChatTarget{Kind: mainChatTargetToday}
	source := "moment"
	if m.Params["_activity_thread"] != "" {
		// The thread outcome's next step is about the activity thread it was
		// observed from; Open lands there, not on Today (which is a no-op when
		// the panel already sits beside Today).
		return mainChatTarget{Kind: mainChatTargetThread, ID: m.Params["_activity_thread"]}, "thread_outcome"
	}
	switch m.Kind {
	case engagement.KindFirstWeek:
		switch m.Params["stage"] {
		case "brief":
			source = "brief"
		case "commitment":
			source = "commitment"
		case "person":
			target, source = mainChatTarget{Kind: "person", ID: m.SubjectID}, "person"
		case "meeting":
			target, source = mainChatTarget{Kind: mainChatTargetSession, ID: m.SubjectID}, "meeting"
		}
	case engagement.KindWeeklyRecap:
		target.ID = m.Params["source_brief_id"]
		source = "engagement"
	case engagement.KindBriefReady, engagement.KindBriefMissed:
		source = "brief"
	case engagement.KindSuggestion:
		target = mainChatTarget{Kind: "suggestion", ID: m.SubjectID}
		source = "suggestion"
	case engagement.KindTopicFinds:
		source = "topic"
	case engagement.KindScheduleMissed:
		target = mainChatTarget{Kind: mainChatTargetAutomation, ID: m.SubjectID}
		source = "schedule"
	case engagement.KindCapturePaused:
		target = mainChatTarget{Kind: "settings", ID: "activity"}
		source = "capture"
	case engagement.KindPlaybookSuggested, engagement.KindPlaybookResult:
		target = mainChatTarget{Kind: "playbook", ID: m.SubjectID}
		source = "playbook"
	case engagement.KindMeetingPrep, engagement.KindCalendarConflict:
		target = mainChatTarget{Kind: "calendar", ID: m.SubjectID}
		source = "calendar"
	case engagement.KindWaitingOnReply:
		target = mainChatTarget{Kind: "mail"}
		source = "mail"
	case engagement.KindLearnedToConfirm:
		target = mainChatTarget{Kind: "memory", ID: m.SubjectID}
		source = "memory"
	case engagement.KindCommitmentSlipping, engagement.KindGoalCheckIn:
		source = "commitment"
	case engagement.KindPersonReappeared:
		target = mainChatTarget{Kind: "person", ID: m.SubjectID}
		source = "person"
	case engagement.KindWorkBlocked, engagement.KindWorkFinished:
		target = mainChatTarget{Kind: mainChatTargetHandoff, ID: m.SubjectID}
		source = "handoff"
	}
	return target, source
}

// Repair destinations on rows stored before the review fixes without reading
// another source or treating a person id as a Mail message locator.
func normalizeMainChatProactiveTargets(item *mainChatProactive) {
	var target mainChatTarget
	switch engagement.Kind(item.Kind) {
	case engagement.KindCapturePaused:
		target = mainChatTarget{Kind: "settings", ID: "activity"}
	case engagement.KindWaitingOnReply:
		target = mainChatTarget{Kind: "mail"}
	}
	if target.Kind != "" {
		item.Target = target
		for i := range item.Evidence {
			item.Evidence[i].Target = target
		}
	}
	for i := range item.Items {
		normalizeMainChatProactiveTargets(&item.Items[i])
	}
}

// mainChatHoldSourceForMoment uses the same privacy pass as the card itself.
func mainChatHoldSourceForMoment(m engagement.Moment) *mainChatHoldSource {
	m, allowed := sanitizeEngagementMoment(m)
	if !allowed || m.Kind != engagement.KindCommitmentSlipping || strings.TrimSpace(m.Params["title"]) == "" || m.SubjectID == "" {
		return nil
	}
	source := &mainChatHoldSource{Thing: HoldThing{Title: m.Params["title"], SourceKind: "promise", SourceID: m.SubjectID}}
	if !m.DueAt.IsZero() {
		source.Due = m.DueAt.Format(time.RFC3339)
	}
	return source
}

func (a *App) proactiveItem(m engagement.Moment, text engagementText) (mainChatProactive, bool) {
	if mainChatUnspecificPersonMoment(m) {
		return mainChatProactive{}, false
	}
	if m.Kind == engagement.KindWeeklyRecap && strings.TrimSpace(m.Params["source_brief_id"]) == "" {
		return mainChatProactive{}, false
	}
	now := time.Now()
	if a.engagement != nil {
		now = a.engagement.now()
	}
	if m.Kind == engagement.KindFirstWeek && m.Params["stage"] != "welcome" && m.Params["stage"] != "brief" && !a.firstWeekSourceReadable(context.Background(), m, now) {
		return mainChatProactive{}, false
	}
	m, allowed := sanitizeEngagementMoment(m)
	// This local record can also be an old unbound report. Outward replay
	// requires a presentation/source receipt; never treat this write as one.
	if !allowed || a.IsClosedEyesModeEnabled() || !a.threadOutcomeMomentAllowsRead(context.Background(), m) || engagementKindSilent(m.Kind) {
		return mainChatProactive{}, false
	}
	text = sanitizeEngagementText(text)
	id := m.ID
	if id == "" {
		id = engagementReportKey(m, time.Now())
	}
	sourceID := m.SubjectID
	if sourceID == "" {
		sourceID = m.ID
	}
	if sourceID == "" {
		sourceID = m.DedupeKey
	}
	if sourceID == "" {
		return mainChatProactive{}, false
	}
	target, source := engagementChatTarget(m)
	lang := a.currentUILanguage()
	copyParams := mainChatEngagementParams(m)
	if m.Kind == engagement.KindMeetingPrep {
		event, ok := a.proactiveVoiceCalendarEvent(context.Background(), m.SubjectID)
		if ok && !event.Start.Before(now) && !event.Start.After(now.Add(engagementMeetingPrepLead+time.Minute)) {
			m.Params = meetingPrepMoment(event, now, time.Local).Params
			copyParams = mainChatEngagementParams(m)
		} else {
			m.Params = map[string]string{}
			copyParams = map[string]string{}
			text = engagementText{}
		}
	}
	if m.Params["_activity_thread"] != "" {
		var ok bool
		copyParams, ok = a.threadOutcomeCopyParams(context.Background(), m)
		if !ok {
			return mainChatProactive{}, false
		}
	}
	copy, _ := engagement.Copy(m.Kind, lang, copyParams)
	headline := strings.TrimSpace(text.PushBody)
	if headline == "" {
		headline = copy.PushBody
	}
	body := strings.TrimSpace(text.Title)
	if body == "" || engagementKindQuotesMail(m.Kind) {
		body = headline
	}
	item := mainChatProactive{MomentID: id, Kind: string(m.Kind), Headline: headline, Body: body,
		ActionIDs: []string{}, ActionLabels: map[string]string{}, Target: target, State: "pending",
		Evidence: []mainChatEvidenceRef{{Kind: source, ID: sourceID, Target: target}}}
	item.HoldSource = mainChatHoldSourceForMoment(m)
	item.Event, item.Events = mainChatProactiveEvents(m.Params)
	if m.Kind == engagement.KindPersonReappeared {
		item.PersonName = m.Params["name"]
	}
	switch m.Kind {
	case engagement.KindBriefReady:
		item.BriefID = m.SubjectID
	case engagement.KindWeeklyRecap:
		// The recap's subject is the week's last day; Play needs the brief.
		item.BriefID = m.Params["source_brief_id"]
	case engagement.KindFirstWeek:
		// The first brief's card is titled with its slot and time, not the
		// first-week title the welcome card already shows (today-review D16).
		if m.Params["stage"] == "brief" {
			item.BriefID = m.SubjectID
		}
	}
	if m.Params["_activity_thread"] != "" && copyParams["app"] != "" {
		item.Place = &mainChatProactivePlace{
			App:      copyParams["app"],
			BundleID: copyParams["bundle_id"],
			At:       copyParams["at"],
		}
	}
	if !m.ExpiresAt.IsZero() {
		item.ExpiresAt = m.ExpiresAt.UnixMilli()
	}
	if !m.CreatedAt.IsZero() {
		item.Evidence[0].At = m.CreatedAt.UTC().Format(time.RFC3339)
	}
	for _, action := range engagement.ChatActions(m.Kind) {
		if label := copy.Actions[action.ID]; label != "" {
			item.ActionIDs = append(item.ActionIDs, action.ID)
			item.ActionLabels[action.ID] = label
		}
	}
	if m.Params["_activity_thread"] != "" {
		item.ActivityOutcomeSource = m.Params["_activity_outcome"]
		item.VoiceVersion = "thread-outcome-v1"
		item.ActionIDs = []string{"open"}
		item.ActionLabels = map[string]string{"open": copy.Actions["open"]}
	}
	populateMainChatProactiveCopy(&item, copyParams)
	populateEngagementJourneyCopy(&item, a.engagementJourneyMomentForDelivery(context.Background(), m, now), lang)
	if m.Kind != engagement.KindMeetingPrep && !a.voiceMainChatCopy(m, &item) {
		return mainChatProactive{}, false
	}
	populateMainChatEventCopy(&item, copyParams)
	if m.Kind == engagement.KindMeetingPrep && (item.Event == nil || item.Event.Private || item.Event.Title == "") {
		// A card says what happened: without the meeting's title there is none.
		log.Printf("[engagement] card not posted: kind=%s reason=no_title", item.Kind)
		return mainChatProactive{}, false
	}
	if m.Params["_activity_thread"] != "" {
		applyThreadOutcomeWorkTitle(&item, copyParams["work"])
	}
	if copy := item.Localized[lang]; copy.Headline != "" {
		item.Headline, item.Body = copy.Headline, copy.Body
	}
	if mainChatProactiveNeedsSentence(item) && !mainChatProactiveHasSentence(item) {
		log.Printf("[engagement] card not posted: kind=%s reason=no_sentence", item.Kind)
		return mainChatProactive{}, false
	}
	if m.Kind == engagement.KindFirstWeek && !a.saveEngagementJourneyReceipt(context.Background(), m, &item) {
		return mainChatProactive{}, false
	}
	return item, true
}

func proactiveFallback(item mainChatProactive) string {
	if item.Permission != nil && item.Permission.Details != "" {
		return item.Body + "\n\n" + item.Permission.Details
	}
	if len(item.Items) > 0 {
		lines := []string{item.Headline}
		for _, child := range item.Items {
			lines = append(lines, proactiveFallback(child))
		}
		return strings.Join(lines, "\n")
	}
	if item.VoiceVersion != "" && item.Headline != item.Body {
		return item.Headline + "\n" + item.Body
	}
	return item.Body
}

func (a *App) postProactiveItem(item mainChatProactive, key string) string {
	if a == nil || a.store == nil {
		return ""
	}
	ctx := context.Background()
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return ""
	}
	mainChatPostMu.Lock()
	defer mainChatPostMu.Unlock()
	// A deterministic row id also dedupes after the recent window rolls over.
	id := "proactive:" + engagement.HashPart(key)
	if row, err := a.store.LoadStreamItem(ctx, id); err != nil {
		return ""
	} else if row != nil {
		if err := a.repairMainChatDelivery(*row); err != nil {
			return ""
		}
		return row.ID
	}
	// A crash can occur after appending a digest but before every moment is
	// marked sent. Dedupe its children too: retrying a smaller remaining batch
	// must not repeat them in a differently keyed digest.
	// Both root and digest-child identities use exact indexed lookups. A new
	// moment is the common case and must not scan historical chat payloads.
	seen := map[string]string{}
	repaired := map[string]bool{}
	identities := []string{item.MomentID}
	for _, child := range item.Items {
		identities = append(identities, child.MomentID)
	}
	for _, momentID := range identities {
		if seen[momentID] != "" {
			continue
		}
		stored, err := a.store.FindProactiveStreamItem(ctx, main.LocalID, momentID)
		if err != nil {
			return ""
		}
		if stored != nil {
			if !repaired[stored.ID] {
				if err := a.repairMainChatDelivery(*stored); err != nil {
					return ""
				}
				repaired[stored.ID] = true
			}
			seen[momentID] = stored.ID
		}
	}
	if existing := seen[item.MomentID]; existing != "" {
		return existing
	}
	// One card per brief (R40): a brief re-made or re-sent under a new moment,
	// or posted again by the backfill, keeps the card it already has.
	if item.Kind == string(engagement.KindBriefReady) && item.BriefID != "" {
		stored, err := a.store.FindProactiveBriefCard(ctx, main.LocalID, item.BriefID)
		if err != nil {
			return ""
		}
		if stored != nil {
			if err := a.repairMainChatDelivery(*stored); err != nil {
				return ""
			}
			return stored.ID
		}
	}
	if len(item.Items) > 0 {
		remaining := make([]mainChatProactive, 0, len(item.Items))
		lastExisting := ""
		item.Evidence = []mainChatEvidenceRef{}
		for _, child := range item.Items {
			if existing := seen[child.MomentID]; existing != "" {
				lastExisting = existing
				continue
			}
			remaining = append(remaining, child)
		}
		if len(remaining) == 0 {
			return lastExisting
		}
		item.Items = remaining
		item.Headline = engagement.DigestHeadline(a.currentUILanguage(), len(remaining))
	}
	populateMainChatProactiveCopy(&item, nil)
	a.bindEngagementCardRead(ctx, &item)
	payload, err := json.Marshal(item)
	if err != nil {
		return ""
	}
	createdAt := time.Now()
	if a.engagement != nil {
		createdAt = a.engagement.now()
	}
	if !item.postedAt.IsZero() {
		// A card posted late (the brief-card backfill) keeps the time its brief was ready.
		createdAt = item.postedAt
	}
	storedID := a.appendMainChatItem(ctx, main.LocalID, storage.StreamItemRow{ID: id, Kind: "system", Content: proactiveFallback(item), ToolName: mainChatProactiveTool, ToolInput: string(payload), CreatedAt: createdAt.UnixMilli()}, mainChatSourceEngagement)
	if storedID != "" {
		if err := a.recordMainChatDelivery(item, createdAt.UnixMilli()); err != nil {
			return ""
		}
	}
	return storedID
}

// engagementDigest writes one ranked bundle, keeping the original moment ids
// and actions on its children. The channel is a durable chat delivery, with no
// separate toast for routine digest completion.
func (a *App) engagementDigest(_ context.Context, entries []engagementDigestItem) (engagement.Channel, error) {
	lang := a.currentUILanguage()
	item := mainChatProactive{Kind: "digest", Headline: engagement.DigestHeadline(lang),
		ActionIDs: []string{}, ActionLabels: map[string]string{}, Evidence: []mainChatEvidenceRef{},
		Target: mainChatTarget{Kind: mainChatTargetToday}, State: "pending"}
	var keys []string
	for _, entry := range entries {
		child, ok := a.proactiveItem(entry.Moment, entry.Text)
		if !ok {
			continue
		}
		item.Items = append(item.Items, child)
		keys = append(keys, child.MomentID)
	}
	if len(item.Items) == 0 {
		return "", errors.New("digest has no displayable items")
	}
	key := "digest:" + strings.Join(keys, ":")
	item.MomentID = "digest:" + engagement.HashPart(key)
	item.Headline = engagement.DigestHeadline(lang, len(item.Items))
	item.Body = engagement.DigestStatus(lang)
	storedID := a.postProactiveItem(item, key)
	if storedID == "" {
		return "", errors.New("could not store digest")
	}
	// The canonical row can belong to an earlier, larger successful batch.
	// Use its identity and original period for the status too, including after
	// a restart; a partial retry is not another digest completion.
	stored, err := a.store.LoadStreamItem(context.Background(), storedID)
	if err != nil || stored == nil {
		return "", errors.New("could not read stored digest")
	}
	a.postMainChatStatus(mainChatStatusEntry{ID: storedID, Kind: "digest_done", Text: item.Body, Target: mainChatTarget{Kind: mainChatTargetToday}}, time.UnixMilli(stored.CreatedAt))
	return engagement.ChannelChat, nil
}

// Status periods follow the configured digest slots. Between slots every
// producer updates the same row, including after an app restart.
func (a *App) mainChatStatusPeriod(now time.Time) string {
	prefs := engagement.DefaultPrefs()
	if a != nil && a.engagement != nil {
		prefs = a.engagement.Prefs()
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for _, offset := range prefs.DigestTimes {
		candidate := time.Date(now.Year(), now.Month(), now.Day(), int(offset.Hours()), int(offset.Minutes())%60, 0, 0, now.Location())
		if !candidate.After(now) && candidate.After(start) {
			start = candidate
		}
	}
	return start.Format(time.RFC3339)
}

func (a *App) postMainChatStatus(entry mainChatStatusEntry, at time.Time) string {
	if a == nil || a.store == nil || strings.TrimSpace(entry.Text) == "" {
		return ""
	}
	entry.Text, _ = pii.RedactText(entry.Text)
	if entry.SentAt == 0 && (entry.MomentID != "" || entry.Kind == "work_finished" || entry.Kind == "playbook_result") {
		// The folded row can move later; attribution belongs to this entry's
		// original durable append, including after a receipt-write retry.
		entry.SentAt = at.UnixMilli()
	}
	populateMainChatStatusCopy(&entry)
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	if entry.VoiceVersion == "" {
		entry.ReadReceipt = ""
		entry.ReadReceipt = a.saveEngagementCardRead(context.Background(), proactiveVoiceStatusHash(entry), entry.MomentID, "", "")
	}
	ctx := context.Background()
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return ""
	}
	mainChatPostMu.Lock()
	defer mainChatPostMu.Unlock()
	// A retry can follow a successful append whose engagement receipt failed.
	// Entry identity is durable across periods, not just within the current row.
	existing, err := a.store.FindStatusStreamItem(ctx, main.LocalID, entry.ID)
	if err != nil {
		return ""
	}
	if existing != nil {
		return existing.ID
	}
	period := a.mainChatStatusPeriod(at)
	id := "status:" + engagement.HashPart(main.LocalID+":"+period)
	status := mainChatStatus{Period: period, Entries: []mainChatStatusEntry{}}
	row, err := a.store.LoadStreamItem(ctx, id)
	if err != nil {
		return ""
	}
	if row != nil {
		if json.Unmarshal([]byte(row.ToolInput), &status) != nil {
			return ""
		}
		for _, previous := range status.Entries {
			if previous.ID == entry.ID {
				return id
			}
		}
	}
	status.Entries = append(status.Entries, entry)
	payload, err := json.Marshal(status)
	if err != nil {
		return ""
	}
	lines := make([]string, 0, len(status.Entries))
	for _, e := range status.Entries {
		lines = append(lines, e.Text)
	}
	fallback := strings.Join(lines, "\n")
	if row == nil {
		return a.appendMainChatItem(ctx, main.LocalID, storage.StreamItemRow{ID: id, Kind: "system", Content: fallback, ToolName: mainChatStatusTool, ToolInput: string(payload), CreatedAt: at.UnixMilli()}, "status")
	}
	updated, err := a.store.MoveStreamItemPresentationToEnd(ctx, id, fallback, string(payload), at)
	if err != nil || updated == nil {
		return ""
	}
	a.mainChatRowsChanged(main.LocalID)
	a.emitBrowserEvent(mainChatItemsAppendedEvent, map[string]any{"sessionId": main.LocalID, "itemId": id, "sortOrder": updated.SortOrder, "kind": "system", "source": "status"})
	return id
}

// Only catalog text selected by the typed kind can reach model context. The
// headline, body, draft, evidence, fallback text and any future subject field
// are display data and are intentionally never read here.
func proactivePromptLine(item mainChatProactive) string {
	if len(item.Items) > 0 {
		lines := make([]string, 0, len(item.Items))
		for _, child := range item.Items {
			if line := proactivePromptLine(child); line != "" {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, " ")
	}
	copy, ok := engagement.Copy(engagement.Kind(item.Kind), "en")
	if !ok {
		return ""
	}
	return copy.PushBody
}

func mainChatPromptReportLine(row storage.StreamItemRow) string {
	switch row.ToolName {
	case mainChatProactiveTool:
		var item mainChatProactive
		if json.Unmarshal([]byte(row.ToolInput), &item) != nil {
			return ""
		}
		return proactivePromptLine(item)
	case mainChatStatusTool:
		var status mainChatStatus
		if json.Unmarshal([]byte(row.ToolInput), &status) != nil {
			return ""
		}
		var lines []string
		for _, entry := range status.Entries {
			if copy, ok := engagement.Copy(engagement.Kind(entry.Kind), "en"); ok {
				lines = append(lines, copy.PushBody)
			} else {
				switch entry.Kind {
				case "handoff_completed":
					lines = append(lines, "Work finished; its result is ready to review.")
				case "schedule_ok":
					lines = append(lines, "A schedule produced a result.")
				case "schedule_error":
					lines = append(lines, "A schedule failed on its last run.")
				case "schedule_missed_once":
					lines = append(lines, "A schedule missed its scheduled time.")
				case "digest_done":
					lines = append(lines, "The latest updates are ready.")
				}
			}
		}
		return strings.Join(lines, " ")
	case mainChatReportTool:
		var report mainChatReport
		if json.Unmarshal([]byte(row.ToolInput), &report) != nil {
			return ""
		}
		// Legacy engagement rows may already contain a subject in Content.
		// Interpret their typed kind, never replay their display fields.
		if report.Source == mainChatSourceEngagement {
			return proactivePromptLine(mainChatProactive{Kind: report.Status})
		}
		return strings.TrimSpace(row.Content)
	default:
		return ""
	}
}

// ResolveMainChatProactiveAction accepts a stored action or the kind mute on a
// card. Work review stays inline, stop uses its lifecycle, and permission
// answers use the same live request boundary as the original work card.
// Send uses the durable outbound boundary; other kinds retain their writes.
func (a *App) ResolveMainChatProactiveAction(momentID, actionID string) (perfValue string, perfErr error) {
	defer logPageBinding("ResolveMainChatProactiveAction", time.Now(), &perfValue)
	return a.resolveMainChatProactiveAction(momentID, actionID, engagement.ChannelDesktop, "", nil)
}

// SendMainChatProactiveDraft sends a draft card's reply, or the person's own
// edit of it when text differs from the draft. The card holds Send for 5
// seconds with Undo before it calls this, so Mail only gets it after them.
func (a *App) SendMainChatProactiveDraft(momentID, text string) (perfValue string, perfErr error) {
	defer logPageBinding("SendMainChatProactiveDraft", time.Now(), &perfValue)
	return a.resolveMainChatProactiveActionText(momentID, mainChatSendAction, engagement.ChannelDesktop, "", nil, text)
}

// CorrectMainChatProactiveFact records an explicit correction for a pending
// learned fact without changing the resolver's existing correction action.
func (a *App) CorrectMainChatProactiveFact(momentID, correction string) (string, error) {
	if a == nil || a.store == nil {
		return "", errors.New("chat is unavailable")
	}
	correction = strings.TrimSpace(correction)
	if correction == "" {
		return "", errors.New("correction is required")
	}
	mainChatProactiveActionMu.Lock()
	defer mainChatProactiveActionMu.Unlock()

	ctx := context.Background()
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return "", err
	}
	row, err := a.store.FindProactiveStreamItem(ctx, main.LocalID, momentID)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", fmt.Errorf("proactive message %q was not found", momentID)
	}
	var item mainChatProactive
	if err := json.Unmarshal([]byte(row.ToolInput), &item); err != nil {
		return "", errors.New("proactive message is invalid")
	}
	normalizeMainChatProactiveActions(&item)
	card := &item
	if card.MomentID != momentID {
		card = nil
		for i := range item.Items {
			if item.Items[i].MomentID == momentID {
				card = &item.Items[i]
				break
			}
		}
	}
	if card == nil {
		return "", errors.New("proactive message identity is invalid")
	}
	if card.Kind != string(engagement.KindLearnedToConfirm) {
		return "", errors.New("correction is only available for learned facts")
	}
	if card.State != "pending" {
		return "", errors.New("learned fact is no longer pending")
	}
	available := false
	for _, action := range card.ActionIDs {
		if action == "correct" {
			available = true
			break
		}
	}
	if !available {
		return "", errors.New("correction is not available on this message")
	}
	if card.Target.ID == "" {
		return "", errors.New("learned fact operation is missing")
	}
	if err := a.recordMainChatDelivery(*card, row.CreatedAt); err != nil {
		return "", err
	}
	if _, err := a.ReviewSelfDreamOperation(ReviewSelfDreamOperationInput{
		OperationID:    card.Target.ID,
		IdempotencyKey: "main-chat:" + momentID + ":correct",
		Decision:       SelfDreamDecisionCorrect,
		Note:           correction,
	}); err != nil {
		return "", err
	}
	if err := a.recordMainChatCardReceipt(ctx, card, engagement.ChannelDesktop, storage.EngagementReceiptOpened, "correct"); err != nil {
		return "", err
	}
	card.State = storage.EngagementReceiptActed
	payload, err := json.Marshal(item)
	if err != nil {
		return "", err
	}
	if err := a.store.UpdateStreamItemToolInput(ctx, row.ID, string(payload)); err != nil {
		return "", err
	}
	a.mainChatRowsChanged(main.LocalID)
	a.emitBrowserEvent(mainChatItemsAppendedEvent, map[string]any{
		"sessionId": main.LocalID,
		"itemId":    row.ID,
		"kind":      "system",
		"source":    mainChatSourceEngagement,
	})
	return card.State, nil
}

// A mobile choice supplies the visible main chat it came from. Its transport
// receipt owns replay; a new choice must not claim another surface's terminal
// state as success. Both surfaces still share the stored-card execution path.
func (a *App) resolveMainChatProactiveAction(momentID, actionID string, channel engagement.Channel, mobileChatID string, mobileReceipt *storage.MobileReceipt) (string, error) {
	return a.resolveMainChatProactiveActionText(momentID, actionID, channel, mobileChatID, mobileReceipt, "")
}

// resolveMainChatProactiveActionText is resolveMainChatProactiveAction with the
// person's own edit of a draft for a send; other actions ignore text.
func (a *App) resolveMainChatProactiveActionText(momentID, actionID string, channel engagement.Channel, mobileChatID string, mobileReceipt *storage.MobileReceipt, text string) (string, error) {
	return a.resolveMainChatProactiveActionTextRevision(momentID, actionID, channel, mobileChatID, mobileReceipt, text, "")
}

func (a *App) resolveMainChatProactiveActionTextRevision(momentID, actionID string, channel engagement.Channel, mobileChatID string, mobileReceipt *storage.MobileReceipt, text, expectedRevision string) (string, error) {
	if a == nil || a.store == nil {
		return "", errors.New("chat is unavailable")
	}
	mainChatProactiveActionMu.Lock()
	defer mainChatProactiveActionMu.Unlock()
	ctx := context.Background()
	var main *storage.SessionRow
	var err error
	if mobileChatID == "" {
		main, err = a.ensureMainChat(ctx)
	} else {
		// A phone action cannot restore a hidden chat or create a main chat.
		main, err = a.store.LoadMainSession(ctx)
		if err == nil {
			visible, found, readErr := a.store.MobileChat(ctx, mobileChatID)
			if readErr != nil {
				return "", readErr
			}
			if !found || main.LocalID != mobileChatID || visible.Kind != storage.SessionKindMain {
				return "", errors.New("message is not in the visible chat")
			}
		}
	}
	if err != nil {
		return "", err
	}
	row, err := a.store.FindProactiveStreamItem(ctx, main.LocalID, momentID)
	if err != nil {
		return "", err
	}
	if row != nil {
		var item mainChatProactive
		if json.Unmarshal([]byte(row.ToolInput), &item) != nil {
			return "", errors.New("proactive message is invalid")
		}
		normalizeMainChatProactiveActions(&item)
		card := &item
		if card.MomentID != momentID {
			card = nil
			for i := range item.Items {
				if item.Items[i].MomentID == momentID {
					card = &item.Items[i]
					break
				}
			}
		}
		if card == nil {
			return "", errors.New("proactive message identity is invalid")
		}
		if card.ActivityOutcomeSource != "" && !a.threadOutcomeSourceReadable(ctx, card.ActivityOutcomeSource) {
			return "", errors.New("outcome source held")
		}
		allowed := false
		for _, action := range card.ActionIDs {
			if action == actionID {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", errors.New("action is not available on this message")
		}
		if actionID == "copy_draft" {
			if card.DraftContent == nil || strings.TrimSpace(card.DraftContent.Text) == "" {
				return "", errors.New("draft is not available on this message")
			}
			// Clipboard writes belong to the renderer's explicit user gesture.
			// Copying does not resolve the underlying mail reminder.
			return card.State, nil
		}
		if actionID == mainChatSendAction {
			if expectedRevision != "" && (card.DraftContent == nil || mainChatDraftRevision(*card.DraftContent) != expectedRevision) {
				return "", errors.New("the draft changed; review it again")
			}
			return a.resolveMainChatSendText(ctx, row, &item, card, text)
		}
		if card.Permission != nil && mobileChatID != "" && actionID != engagement.ActionMuteKind {
			if mobileReceipt == nil {
				return "", errors.New("phone permission action has no retry identity")
			}
			// Measurement storage can fail on a retry after the runner already
			// accepted this choice. Keep the transport key retryable as well.
			if err := a.recordMainChatDelivery(*card, row.CreatedAt); err != nil {
				return "", fmt.Errorf("%w: %v", errMainChatPermissionReceiptPending, err)
			}
			if err := a.recordMainChatCardReceipt(ctx, card, channel, storage.EngagementReceiptOpened, actionID); err != nil {
				return "", fmt.Errorf("%w: %v", errMainChatPermissionReceiptPending, err)
			}
			// Permission outcomes belong to the exact accepted retry identity,
			// independently of their current card presentation.
			return a.resolveMainChatWorkPermission(card, actionID, mobileReceipt)
		}
		if actionID != engagement.ActionMuteKind && (card.State == "acted" || card.State == "dismissed" || card.State == "deferred" || card.State == "expired" || card.State == "muted") {
			if mobileChatID != "" {
				return "", errors.New("proactive message was already handled")
			}
			return card.State, nil
		}
		if err := a.recordMainChatDelivery(*card, row.CreatedAt); err != nil {
			return "", err
		}
		state := storage.EngagementReceiptOpened
		if actionID == engagement.ActionMuteKind {
			if a.engagementService() == nil {
				return "", errEngagementUnavailable
			}
			state = card.State
			card.KindMuted = true
		} else if card.Permission != nil {
			if err := a.recordMainChatCardReceipt(ctx, card, channel, storage.EngagementReceiptOpened, actionID); err != nil {
				return "", err
			}
			return a.resolveMainChatWorkPermission(card, actionID, nil)
		} else if card.Work != nil {
			state, err = a.resolveMainChatHandoffAction(card, actionID)
			if err != nil {
				return "", err
			}
		} else if card.Kind == string(engagement.KindSuggestion) {
			if _, err := a.ResolveTodaySuggestion(TodaySuggestionDecision{ID: card.Target.ID, Decision: actionID}); err != nil {
				return "", err
			}
			switch actionID {
			case "dismiss":
				state = storage.EngagementReceiptDismissed
			case "not_now":
				state = "deferred"
			default:
				state = storage.EngagementReceiptActed
			}
		} else if actionID == "resume_capture" && card.Kind == string(engagement.KindCapturePaused) {
			if err := a.ResumeObservation(); err != nil {
				return "", err
			}
			state = storage.EngagementReceiptActed
		} else if actionID == "make_it_now" && card.Kind == string(engagement.KindBriefMissed) {
			if _, err := a.GenerateMobileBriefNow(); err != nil {
				return "", err
			}
			state = storage.EngagementReceiptActed
		} else if actionID == "run_now" && card.Kind == string(engagement.KindScheduleMissed) {
			if card.Target.Kind != mainChatTargetAutomation || card.Target.ID == "" {
				return "", errors.New("schedule is not available on this message")
			}
			if err := a.RunCronJobNow(card.Target.ID); err != nil {
				return "", err
			}
			state = storage.EngagementReceiptActed
		} else if actionID == "done" && card.Kind == string(engagement.KindReminder) {
			state = storage.EngagementReceiptActed
		} else if actionID == "keep_both" && card.Kind == string(engagement.KindCalendarConflict) {
			state = storage.EngagementReceiptActed
		} else if actionID == "keep" && card.Kind == string(engagement.KindLearnedToConfirm) {
			operationID := card.Target.ID
			if _, err := a.ReviewSelfDreamOperation(ReviewSelfDreamOperationInput{
				OperationID:    operationID,
				IdempotencyKey: "main-chat:" + card.MomentID,
				Decision:       SelfDreamDecisionAccept,
			}); err != nil {
				return "", err
			}
			state = storage.EngagementReceiptActed
		} else if actionID == "not_now" {
			state = "deferred"
		} else if actionID == "correct" && card.Kind == string(engagement.KindLearnedToConfirm) {
			state = storage.EngagementReceiptOpened
		} else if actionID == "dismiss" {
			state = storage.EngagementReceiptDismissed
		}
		receiptState := state
		if actionID == engagement.ActionMuteKind {
			receiptState = storage.EngagementReceiptMuted
		}
		if receiptState == storage.EngagementReceiptActed || receiptState == "deferred" {
			// A button press is not evidence that the linked item completed.
			receiptState = storage.EngagementReceiptOpened
		}
		if err := a.recordMainChatCardReceipt(ctx, card, channel, receiptState, actionID); err != nil {
			return "", err
		}
		card.State = state
		if item.Kind == "digest" && actionID != engagement.ActionMuteKind {
			if mainChatProactiveRowClass(item, time.Now()) != MainChatRowWaiting {
				item.State = "acted"
			}
		}
		payload, err := json.Marshal(item)
		if err != nil {
			return "", err
		}
		if err := a.store.UpdateStreamItemToolInput(ctx, row.ID, string(payload)); err != nil {
			return "", err
		}
		a.mainChatRowsChanged(main.LocalID)
		a.emitBrowserEvent(mainChatItemsAppendedEvent, map[string]any{"sessionId": main.LocalID, "itemId": row.ID, "kind": "system", "source": mainChatSourceEngagement})
		return state, nil
	}
	return "", fmt.Errorf("proactive message %q was not found", momentID)
}
