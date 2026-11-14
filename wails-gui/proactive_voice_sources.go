package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/internal/engagement"
	"wails-gui/internal/pii"
	"wails-gui/internal/proactivevoice"
	"wails-gui/storage"
)

// proactiveVoiceSource is local-only evidence. Storage identities and raw mail
// fields never serialize directly; the assembler constructs the provider job.
type proactiveVoiceSource struct {
	Provenance           []proactiveVoiceDependency `json:"-"`
	Items                []proactivevoice.Item
	People               []proactivevoice.Person
	UserWords            []proactiveVoiceUserWordSource
	ContactNames         []string
	UserName             string
	SourceApps           []string
	RawMailSubject       string
	RawMailBody          string
	DraftRecipient       string
	DraftSubject         string
	DraftLanguage        string
	WritingSamples       []string
	CommunicationBeliefs []communicationBeliefSource
	MailBodyConsent      bool
}

// The production seam reads capture's existing live status without starting it.
var proactiveVoiceCaptureSnapshots = func(a *App) []captureSnapshot {
	return a.liveEngagementCaptureSnapshots()
}

type proactiveVoiceUserWordSource struct {
	Source      string
	Text        string
	When        time.Time
	PromisedDay *time.Time
}

// loadProactiveVoiceSource rechecks the source at delivery time. Moment params
// are not evidence for mail, calendars, commitments or briefs: they may have
// changed, been deleted, or become protected since the producer ran.
func (a *App) loadProactiveVoiceSource(ctx context.Context, m engagement.Moment, now time.Time, lang string) (proactiveVoiceSource, string) {
	var source proactiveVoiceSource
	if a == nil || a.store == nil || ctx.Err() != nil {
		return source, "no_source"
	}
	if a.IsClosedEyesModeEnabled() {
		return source, "privacy_excluded"
	}
	ctx, dependencies := proactiveVoiceDependencyContext(ctx)
	people, err := a.proactiveVoicePrincipals(ctx)
	if err != nil {
		return source, "source_unavailable"
	}
	for _, person := range people {
		source.ContactNames = append(source.ContactNames, person.DisplayName)
		source.ContactNames = append(source.ContactNames, person.Aliases...)
		if person.Kind == storage.PrincipalKindSelf && a.proactiveVoicePersonAllowed(ctx, person) {
			if words := strings.Fields(person.DisplayName); len(words) > 0 {
				source.UserName = words[0]
			}
		}
	}
	var reason string
	switch m.Kind {
	case engagement.KindWaitingOnReply:
		reason = a.loadProactiveVoiceMail(ctx, m, now, lang, &source)
	case engagement.KindMeetingPrep:
		event, ok := a.proactiveVoiceCalendarEvent(ctx, m.SubjectID)
		if !ok || event.Start.Before(now) || event.Start.After(now.Add(engagementMeetingPrepLead+time.Minute)) {
			return source, "no_source"
		}
		source.SourceApps = []string{"Calendar"}
		minutes := int(event.Start.Sub(now).Round(time.Minute) / time.Minute)
		item := proactivevoice.Item{Ref: "c1", Type: "calendar_event", Title: proactiveVoiceSourceString(event.Title), Start: proactiveVoiceSourceDisplay(proactiveVoiceDate(event.Start, lang) + " " + event.Start.Format("15:04")), StartsIn: proactiveVoiceSourceDisplay(proactiveVoiceMinutes(minutes, lang))}
		place := calendar.Place(event)
		if event.ConferenceURL != "" {
			place = "video link"
			if lang == "zh-CN" {
				place = "视频会议"
			}
		}
		if place != "" {
			item.Place = proactiveVoiceSourceString(place)
		}
		for _, attendee := range event.Attendees {
			if attendee.IsMe || strings.TrimSpace(attendee.Name) == "" || len(source.People) >= engagementPrepPeopleLimit {
				continue
			}
			ref := fmt.Sprintf("p%d", len(source.People)+1)
			source.People = append(source.People, proactivevoice.Person{Ref: ref, Name: proactiveVoiceSourceString(attendee.Name), RoleLabel: proactiveVoiceRole(lang)})
			item.PeopleRefs = append(item.PeopleRefs, ref)
		}
		if event.AttendeeCount() > 0 {
			count := fmt.Sprintf("%d people", event.AttendeeCount())
			if lang == "zh-CN" {
				count = fmt.Sprintf("%d 人", event.AttendeeCount())
			}
			item.AttendeeTotal = proactiveVoiceSourceDisplay(count)
		}
		source.Items = []proactivevoice.Item{item}
	case engagement.KindCommitmentSlipping:
		detail, ok := a.proactiveVoicePurpose(ctx, m.SubjectID, now)
		if !ok {
			return source, "no_source"
		}
		current, eligible := engagementCommitmentSlippingMoment(detail, now)
		if !eligible {
			return source, "no_source"
		}
		source.Items = []proactivevoice.Item{{Ref: "k1", Type: "commitment", Title: proactiveVoiceSourceString(detail.Current.Title), Due: proactiveVoiceSourceDisplay(proactiveVoiceDate(current.DueAt, lang)), Progress: proactiveVoiceSourceString("none recorded")}}
	case engagement.KindBriefReady:
		reason = a.loadProactiveVoiceBrief(ctx, m, now, lang, &source)
	case engagement.KindCapturePaused:
		off := false
		for _, snapshot := range proactiveVoiceCaptureSnapshots(a) {
			if snapshot.Kind == m.SubjectID {
				off = snapshot.off()
				proactiveVoiceRecordDependency(ctx, "capture_status", snapshot.Kind, snapshot)
				break
			}
		}
		if !off {
			return source, "no_source"
		}
		what := ""
		switch m.SubjectID {
		case "screen":
			what = "Screen capture"
			if lang == "zh-CN" {
				what = "屏幕采集"
			}
		case "microphone":
			what = "Microphone"
			if lang == "zh-CN" {
				what = "麦克风"
			}
		case "system_audio":
			what = "System audio"
			if lang == "zh-CN" {
				what = "系统音频"
			}
		}
		if what == "" || m.CreatedAt.IsZero() || m.CreatedAt.After(now) {
			return source, "no_source"
		}
		source.Items = []proactivevoice.Item{{Ref: "s1", Type: "capture_status", What: proactiveVoiceSourceDisplay(what), OffFor: proactiveVoiceSourceDisplay(proactiveVoiceMinutes(int(now.Sub(m.CreatedAt)/time.Minute), lang))}}
	default:
		return source, "kind_not_supported"
	}
	if reason != "" {
		return source, reason
	}
	if m.Kind == engagement.KindWaitingOnReply || m.Kind == engagement.KindMeetingPrep || m.Kind == engagement.KindCommitmentSlipping {
		source.UserWords = a.proactiveVoiceChatWords(ctx, source, now)
	}
	if m.Kind == engagement.KindWaitingOnReply && source.MailBodyConsent && source.DraftRecipient != "" {
		source.CommunicationBeliefs = a.loadCommunicationBeliefs(ctx)
	}
	var complete bool
	source.Provenance, complete = dependencies.dependencies()
	if !complete {
		return source, "no_source"
	}
	return source, ""
}

func (a *App) loadProactiveVoiceMail(ctx context.Context, m engagement.Moment, now time.Time, lang string, source *proactiveVoiceSource) string {
	if !a.proactiveVoicePermission(ctx, mailPermissionKey) || !proactiveVoiceAppAllowed("com.apple.mail", "Mail") {
		return "privacy_excluded"
	}
	rows, err := a.proactiveVoiceMailMessages(ctx, now.Add(-mailWindowDays*24*time.Hour).UnixMilli())
	if err != nil {
		return "source_unavailable"
	}
	for _, thread := range mailWaitingOnReply(rows, now) {
		if engagement.DedupeKey(engagement.KindWaitingOnReply, thread.PrincipalID, thread.ThreadKey) != m.DedupeKey {
			continue
		}
		name := thread.SenderName
		if person, err := a.store.GetPrincipal(ctx, thread.PrincipalID); err == nil && strings.TrimSpace(person.DisplayName) != "" && a.proactiveVoicePersonAllowed(ctx, person) {
			name = person.DisplayName
		}
		if strings.TrimSpace(name) == "" {
			return "no_source"
		}
		proactiveVoiceRecordDependency(ctx, "mail_thread", thread.ThreadKey, thread)
		for _, row := range rows {
			if row.ThreadKey == thread.ThreadKey {
				proactiveVoiceRecordDependency(ctx, "mail_message", row.ID, row)
			}
		}
		source.SourceApps = []string{"Mail"}
		source.RawMailSubject = thread.Subject
		source.DraftRecipient = thread.SenderEmail
		source.DraftSubject = "Re: " + thread.Subject
		source.DraftLanguage = lang
		source.People = []proactivevoice.Person{{Ref: "p1", Name: proactiveVoiceSourceString(name), RoleLabel: proactiveVoiceRole(lang)}}
		source.Items = []proactivevoice.Item{{Ref: "m1", Type: "mail_thread", FromName: proactiveVoiceSourceString(name), WaitingSince: proactiveVoiceSourceDisplay(proactiveVoiceDate(thread.At, lang))}}
		// mailConsentVersion authorizes headers only. No body-consent version,
		// body reader or sent-mail writing sample source exists in v1.
		source.MailBodyConsent = false
		return ""
	}
	return "no_source"
}

func (a *App) proactiveVoicePermission(ctx context.Context, key string) bool {
	value, err := a.store.GetUIState(ctx, permissionUIStateKey(key))
	return err == nil && value != "false" && (value != "" || !permissionsOffByDefault[key])
}

func proactiveVoiceAppAllowed(bundle, name string) bool {
	return !activitySourceAppIsSensitive(bundle, name)
}

func (a *App) proactiveVoicePersonAllowed(ctx context.Context, person storage.PrincipalRow) bool {
	// The local denylist may use every name, but a displayed name requires
	// current provenance. Use direct mail headers if an inferred identity no
	// longer passes its source checks.
	sightings, err := a.principalSessionSightingsForRead(ctx, person.ID)
	if err != nil || (len(sightings) == 0 && strings.HasPrefix(person.CreatedFrom, "auto_")) {
		return false
	}
	for _, sighting := range sightings {
		if !a.proactiveVoiceSessionsAllowedContext(ctx, []string{sighting.SessionID}, nil) {
			return false
		}
	}
	handles, err := a.store.ListAppHandlesByPrincipal(ctx, person.ID)
	if err != nil {
		return false
	}
	for _, handle := range handles {
		if !proactiveVoiceAppAllowed(handle.App, handle.App) {
			return false
		}
	}
	return proactiveVoiceRecordDependency(ctx, "principal", person.ID, person) &&
		proactiveVoiceRecordDependency(ctx, "principal_sightings", person.ID, sightings) &&
		proactiveVoiceRecordDependency(ctx, "principal_handles", person.ID, handles)
}

func (a *App) proactiveVoiceCalendarEvent(ctx context.Context, id string) (calendar.Event, bool) {
	if !a.proactiveVoicePermission(ctx, calendarPermissionKey) || !proactiveVoiceAppAllowed("com.apple.iCal", "Calendar") {
		return calendar.Event{}, false
	}
	row, found, err := a.store.GetCalendarEvent(ctx, id)
	if err != nil || !found || row.DeletedAt != 0 {
		return calendar.Event{}, false
	}
	// Web calendar rows have no originating app provenance. They wait for
	// that provenance instead of bypassing protected-browser policy.
	if row.Source != calendar.SourceEventKit {
		return calendar.Event{}, false
	}
	if row.SessionID != "" && !a.proactiveVoiceSessionsAllowedContext(ctx, []string{row.SessionID}, nil) {
		return calendar.Event{}, false
	}
	event := calendarEventFromRow(row)
	return event, event.Busy() && !event.AllDay && proactiveVoiceRecordDependency(ctx, "calendar", row.ID, row)
}

// proactiveVoicePurpose validates every source contributing to the current
// projection. An old inclusion receipt never overrides a current protection.
func (a *App) proactiveVoicePurpose(ctx context.Context, id string, now time.Time) (PurposeOpenLoopDetail, bool) {
	detail, err := a.userPurposeDetail(id)
	if err != nil || !detail.Eligible || detail.Current.Owner != PurposeOwnerUser {
		return PurposeOpenLoopDetail{}, false
	}
	// A status-only revision can inherit an older title or due date. Use the
	// same field lineage as Today context, requiring complete provenance for
	// every carried field before exposing this projection to the assistant.
	events := map[string]bool{detail.Current.ID: true}
	for index := range todayChatPurposeTextFields(&detail.Current) {
		ids, complete := todayChatPurposeFieldEvents(detail, index)
		if !complete {
			return PurposeOpenLoopDetail{}, false
		}
		for _, eventID := range ids {
			events[eventID] = true
		}
	}
	for eventID := range events {
		evidence := todayChatPurposeEventEvidence(detail, eventID)
		if len(evidence) == 0 {
			return PurposeOpenLoopDetail{}, false
		}
		for _, item := range evidence {
			if ctx.Err() != nil || item.PrivacyDecision == TaskContextPrivacyExclude || !a.proactiveVoicePurposeSourceAllowed(ctx, item) {
				return PurposeOpenLoopDetail{}, false
			}
		}
	}
	// Existing mobile projection guard prevents mail subjects resurfacing
	// through derived Purpose titles without body consent.
	subjects, err := mobileGoalSubjects(a)
	if err != nil || mobileGoalEchoesMail(detail, mobileGoalView(detail, now), subjects) {
		return PurposeOpenLoopDetail{}, false
	}
	return detail, proactiveVoiceRecordDependency(ctx, "purpose", detail.OpenLoop.ID, detail)
}

func (a *App) proactiveVoicePurposeSourceAllowed(ctx context.Context, evidence PurposeEvidenceRecord) bool {
	switch evidence.SourceKind {
	case userPurposeSourceKind:
		return evidence.EvidenceClass == PurposeEvidenceExplicitUser
	case reminderPurposeSourceKind:
		if !a.proactiveVoicePermission(ctx, remindersPermissionKey) || !proactiveVoiceAppAllowed("com.apple.reminders", "Reminders") {
			return false
		}
		rows, err := a.proactiveVoiceOpenReminders(ctx)
		if err != nil {
			return false
		}
		for _, row := range rows {
			reminder := reminderFromRow(row)
			if reminderPurposeRef(row.UID) == evidence.SourceID && reminder.Fingerprint(time.Local) == evidence.SourceHash {
				return proactiveVoiceRecordDependency(ctx, "reminder", row.UID, row)
			}
		}
		return false
	case taskContextSourceMemoryKind:
		id := proactiveVoiceCurrentMarkdownID(evidence.SourceID)
		if id == "" {
			return false
		}
		entry, err := a.GetMemoryEntry(id)
		if err != nil || agencyPayloadHash([]byte(strings.TrimSpace(entry.Content))) != evidence.SourceHash {
			return false
		}
		dir, err := memoryDir()
		if err != nil {
			return false
		}
		forgotten, err := isMemoryForgotten(dir, entry.ID, entry.Content, time.Now())
		if err != nil || forgotten {
			return false
		}
		allowRead, err := a.memoryReadFilter()
		return err == nil && allowRead(entry.ID, entry.Content) && a.proactiveVoiceMarkdownDependencies(ctx, "memory", entry.ID, entry.Content)
	case taskContextSourceKnowledgeKind:
		id := proactiveVoiceCurrentMarkdownID(evidence.SourceID)
		if id == "" {
			return false
		}
		entry, err := a.GetKnowledgeEntry(id)
		if err != nil || agencyPayloadHash([]byte(strings.TrimSpace(entry.Content))) != evidence.SourceHash {
			return false
		}
		allowRead, err := a.memoryReadFilter()
		return err == nil && allowRead(entry.ID, entry.Content) && a.proactiveVoiceMarkdownDependencies(ctx, "knowledge", entry.ID, entry.Content)
	default:
		// Self entries and unversioned legacy source kinds do not retain
		// enough source-app provenance to justify a new provider disclosure.
		return false
	}
}

func proactiveVoiceCurrentMarkdownID(id string) string {
	base, version, _ := strings.Cut(id, ":")
	if version != "" && version != "live" {
		return ""
	}
	return strings.TrimSuffix(base, ".md")
}

func proactiveVoiceAppsAllowed(apps []string) bool {
	for _, app := range apps {
		if activitySourceAppIsSensitive(app, app) {
			return false
		}
		// Older provenance can retain only a display name. The default PII
		// list is bundle-based, so also recognize its product-name suffixes
		// (for example 1Password and Keychain Access) without a second list.
		name := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(app), " ", ""))
		for _, prefix := range pii.AllBundlePrefixes() {
			if name != "" && name == strings.ToLower(prefix[strings.LastIndex(prefix, ".")+1:]) {
				return false
			}
		}
	}
	return true
}

func (a *App) proactiveVoiceSessionsAllowed(ids []string) bool {
	return a.proactiveVoiceSessionsAllowedContext(context.Background(), ids, nil)
}

func (a *App) loadProactiveVoiceBrief(ctx context.Context, m engagement.Moment, now time.Time, lang string, source *proactiveVoiceSource) string {
	svc := a.mobileBriefSource()
	if svc == nil {
		return "no_source"
	}
	brief, ok := svc.GetMobileBrief(m.SubjectID)
	if !ok {
		return "no_source"
	}
	brief = a.mobileBriefForDelivery(brief)
	if !proactiveVoiceRecordDependency(ctx, "brief", brief.BriefID, brief) {
		return "no_source"
	}
	refs := make(map[string]MobileBriefRef, len(brief.Evidence))
	for _, ref := range brief.Evidence {
		refs[ref.Ref] = ref
	}
	var top []proactivevoice.TopItem
	for _, section := range mobileBriefSectionLines(brief.Sections) {
		for _, item := range section.items {
			if len(item.Refs) != 1 || strings.TrimSpace(item.Title) == "" {
				continue
			}
			ref, ok := refs[item.Refs[0]]
			if !ok {
				continue
			}
			title := ""
			switch ref.SourceKind {
			case mobileBriefCalendarSource:
				if event, ok := a.proactiveVoiceCalendarEvent(ctx, ref.SourceID); ok {
					title = event.Title
				}
			case "purpose":
				if detail, ok := a.proactiveVoicePurpose(ctx, ref.SourceID, now); ok {
					title = detail.Current.Title
				}
			}
			// Stale summaries, generated narrative, screen, transcript, mail,
			// browsing and file text are not read as fresh top-item evidence.
			if title == "" || title != item.Title {
				continue
			}
			top = append(top, proactivevoice.TopItem{Ref: fmt.Sprintf("b%d", len(top)+1), Text: title})
			if len(top) == 2 {
				break
			}
		}
		if len(top) == 2 {
			break
		}
	}
	if len(top) == 0 {
		return "no_source"
	}
	slot := engagementBriefSlot(brief.Slot)
	if lang == "zh-CN" {
		switch brief.Slot {
		case mobileBriefSlotMorning:
			slot = "早间"
		case mobileBriefSlotEvening:
			slot = "晚间"
		default:
			slot = "最新"
		}
	}
	source.Items = []proactivevoice.Item{{Ref: "b0", Type: "brief", Slot: proactiveVoiceSourceDisplay(slot), TopItems: top}}
	return ""
}

func (a *App) proactiveVoiceChatWords(ctx context.Context, source proactiveVoiceSource, now time.Time) []proactiveVoiceUserWordSource {
	main, err := a.store.LoadMainSession(ctx)
	if err != nil || main.Hidden {
		return nil
	}
	page, err := a.store.LoadStreamItemsLatest(ctx, main.LocalID, 100)
	if err != nil {
		return nil
	}
	var matches []string
	for _, person := range source.People {
		if person.Name != nil {
			matches = append(matches, *person.Name)
		}
	}
	for _, item := range source.Items {
		if item.Title != nil {
			matches = append(matches, *item.Title)
		}
	}
	var out []proactiveVoiceUserWordSource
	for i := len(page.Items) - 1; i >= 0 && len(out) < 5; i-- {
		item := page.Items[i]
		if item.Kind != "user" || item.AutomationContext != "" || item.CreatedAt <= 0 || item.CreatedAt > now.UnixMilli() || len(item.Content) > 2048 {
			continue
		}
		for _, match := range matches {
			if strings.TrimSpace(match) != "" && proactiveVoiceExactContains(item.Content, match) {
				// Appending the proactive result updates session recency, which
				// is not provenance for the user's earlier message.
				proactiveVoiceRecordDependency(ctx, "chat_session", main.LocalID, struct {
					ID, Kind, ParentID string
					Hidden             bool
				}{main.LocalID, main.Kind, main.ParentID, main.Hidden})
				proactiveVoiceRecordDependency(ctx, "chat_user_message", item.ID, item)
				out = append(out, proactiveVoiceUserWordSource{Source: "chat", Text: item.Content, When: time.UnixMilli(item.CreatedAt), PromisedDay: proactiveVoicePromisedDay(item.Content, time.UnixMilli(item.CreatedAt))})
				break
			}
		}
	}
	return out
}

func proactiveVoiceSourceString(text string) *string { return &text }
func proactiveVoiceSourceDisplay(text string) *proactivevoice.Display {
	return &proactivevoice.Display{Display: text}
}
func proactiveVoiceRole(lang string) string {
	if lang == "zh-CN" {
		return "同事"
	}
	return "a colleague"
}
func proactiveVoiceMinutes(minutes int, lang string) string {
	if lang == "zh-CN" {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	if minutes == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", minutes)
}

var proactiveVoiceExplicitPromise = regexp.MustCompile(`(?i)\b(I will|I'll|I’ll|I am going to|I'm going to|I’m going to|I plan to|I promise|I shall)\b|我(?:会|打算|计划|答应)`)
var proactiveVoiceComputableDay = regexp.MustCompile(`(?i)\b(today|tonight|tomorrow)\b|今天|今晚|明天|\d{4}[-/]\d{2}[-/]\d{2}`)
var proactiveVoiceAmbiguousPromiseDay = regexp.MustCompile(`(?i)\bbefore\b|之前|以前|前给|后天`)

// proactiveVoicePromisedDay recognizes only a date the user explicitly put in
// the same sentence as a first-person promise. Relative days are anchored to
// that user message's timestamp, never to delivery time. Conflicting dates,
// named weekdays and unsupported date expressions stay absent and are dropped
// by the assembler's day-word guard.
func proactiveVoicePromisedDay(text string, when time.Time) *time.Time {
	if when.IsZero() || proactiveVoiceAmbiguousPromiseDay.MatchString(text) {
		return nil
	}
	var promised *time.Time
	sentences := strings.FieldsFunc(text, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '。' || r == '！' || r == '？' || r == '\n'
	})
	for _, sentence := range sentences {
		days := proactiveVoiceComputableDay.FindAllString(sentence, -1)
		remainder := proactiveVoiceComputableDay.ReplaceAllString(sentence, "")
		if proactiveVoiceDay.MatchString(remainder) {
			return nil
		}
		if len(days) == 0 {
			continue
		}
		if !proactiveVoiceExplicitPromise.MatchString(sentence) {
			return nil
		}
		for _, word := range days {
			day := time.Date(when.Year(), when.Month(), when.Day(), 0, 0, 0, 0, when.Location())
			switch strings.ToLower(word) {
			case "today", "tonight", "今天", "今晚":
			case "tomorrow", "明天":
				day = day.AddDate(0, 0, 1)
			default:
				parsed, err := time.ParseInLocation("2006-01-02", strings.ReplaceAll(word, "/", "-"), when.Location())
				if err != nil {
					return nil
				}
				day = parsed
			}
			if promised != nil && !promised.Equal(day) {
				return nil
			}
			copy := day
			promised = &copy
		}
	}
	return promised
}
