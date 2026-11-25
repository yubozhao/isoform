package main

import (
	"context"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/browserhistory"
	"wails-gui/internal/topics"
	"wails-gui/storage"
)

// What the user has been looking at, from the last fourteen days the Mac
// observed. collectTopicInput reads the sources; topicSignals turns them into
// ranker signals and applies every exclusion (paused stretches, private
// windows, protected apps and browsers, people's names), so the privacy rules
// are tested from fixtures. Nothing here leaves the Mac: only the ranked
// labels do, and only when the user has the feature on.

const (
	topicWindow          = 14 * 24 * time.Hour
	topicWindowTitlesMax = 2000
	topicUpcomingAhead   = 48 * time.Hour
)

// topicWindowRow is one window title and how often it came to the front.
type topicWindowRow struct {
	Title    string
	AppName  string
	BundleID string
	Count    int
	Last     int64
}

// topicGoal is one open Purpose Map goal or commitment.
type topicGoal struct {
	Title string
	At    time.Time
}

// topicInput is the plain data behind one ranking.
type topicInput struct {
	Now time.Time
	// Pauses is the "Pause observing" log. PausesUnknown means it could not
	// be read: then nothing observed counts, as if every moment was paused.
	Pauses        []observationPause
	PausesUnknown bool

	Visits     []BrowserVisitRow
	Journal    []topicJournalDay
	Windows    []topicWindowRow
	Activities []ActivityRow
	Semantic   []SemanticEventRow
	Calendar   []storage.CalendarEventRow
	Sessions   []storage.SessionRow
	Memories   []MemoryEntry
	Goals      []topicGoal
	// Files is empty unless the recent files switch is on.
	Files []FileActivityRow
	// People are names the user knows; they never become topics.
	People []string
}

// collectTopicInput reads every source for the window ending at now. Each
// source is best effort.
func collectTopicInput(a *App, now time.Time) topicInput {
	in := topicInput{Now: now}
	if a == nil {
		return in
	}
	from := now.Add(-topicWindow)
	pauses, err := a.observationPauses()
	if err != nil {
		in.PausesUnknown = true
	}
	in.Pauses = pauses

	if a.activity != nil && a.activity.store != nil {
		store := a.activity.store
		if rows, err := store.ListBrowserVisits(from.UnixMilli(), now.UnixMilli()); err == nil {
			in.Visits = rows
		}
		in.Windows = collectTopicWindowTitles(store, from.UnixMilli(), now.UnixMilli())
		if rows, err := store.GetActivitiesInRange(from.UnixMilli(), now.UnixMilli()); err == nil {
			in.Activities = a.filterComposerModelCandidates(store, rows)
		}
		if rows, err := store.GetSemanticEventsInRange(from.UnixMilli(), now.UnixMilli()); err == nil {
			for _, row := range rows {
				if a.interestSemanticSourceAllowsRead(row) {
					in.Semantic = append(in.Semantic, row)
				}
			}
		}
		if a.captureAllowed(context.Background(), "topics_collect", filesPermissionKey) {
			if rows, err := store.ListFileActivity(from.UnixMilli(), now.UnixMilli()); err == nil {
				in.Files = rows
			}
		}
		in.Goals = topicGoalsFrom(a.collectTodayChatSources(context.Background(), now).Purposes)
	}
	if a.store != nil {
		ctx := context.Background()
		if rows, err := a.store.ListCalendarEvents(ctx, from.UnixMilli(), now.Add(topicUpcomingAhead).UnixMilli(), false); err == nil {
			in.Calendar = rows
		}
		if all, err := a.store.LoadAllSessions(ctx); err == nil {
			for _, rows := range all {
				in.Sessions = append(in.Sessions, rows...)
			}
		}
	}
	if entries, err := a.GetMemoryEntries(); err == nil {
		if allowed, err := a.memoryReadFilter(); err == nil && allowed != nil {
			for _, entry := range entries {
				if allowed(entry.ID, entry.Content) {
					in.Memories = append(in.Memories, entry)
				}
			}
		}
	}
	if people, err := a.ListPrincipalsWithStats(); err == nil {
		for _, p := range people {
			in.People = append(in.People, p.Principal.DisplayName)
			in.People = append(in.People, p.Principal.Aliases...)
		}
	}
	return in
}

// collectTopicWindowTitles counts the window titles that came to the front,
// newest first. Protected sessions are left out in the query; protected apps
// and private windows are left out by topicSignals.
func collectTopicWindowTitles(store *ActivityStore, fromMs, toMs int64) []topicWindowRow {
	if store == nil || store.db == nil {
		return nil
	}
	rows, err := store.db.Query(`
		SELECT e.data FROM activity_events e
		JOIN activity_sessions s ON e.session_id = s.id
		WHERE e.kind IN ('app_switch', 'focus')
		AND e.timestamp >= ? AND e.timestamp < ?`+sqlExcludeProtected+`
		ORDER BY e.timestamp DESC
		LIMIT ?`, fromMs, toMs, topicWindowTitlesMax)
	if err != nil {
		log.Printf("[topics] window titles unavailable: %v", err)
		return nil
	}
	defer rows.Close()
	byKey := map[string]*topicWindowRow{}
	var order []string
	for rows.Next() {
		var data string
		if rows.Scan(&data) != nil {
			continue
		}
		var evt activitymon.ActivityEvent
		if json.Unmarshal([]byte(data), &evt) != nil || strings.TrimSpace(evt.WindowTitle) == "" {
			continue
		}
		key := evt.AppBundleID + "\x00" + evt.WindowTitle
		row := byKey[key]
		if row == nil {
			row = &topicWindowRow{Title: evt.WindowTitle, AppName: evt.AppName, BundleID: evt.AppBundleID, Last: evt.Timestamp}
			byKey[key] = row
			order = append(order, key)
		}
		row.Count++
	}
	out := make([]topicWindowRow, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	return out
}

// topicGoalsFrom keeps the open goals and commitments.
func topicGoalsFrom(details []PurposeOpenLoopDetail) []topicGoal {
	var out []topicGoal
	for _, detail := range details {
		switch kind, _ := normalizePurposeKind(detail.OpenLoop.Kind); kind {
		case "goal", "commitment":
		default:
			continue
		}
		if detail.Current.Title == "" || !purposeStateOpen(detail.Current.LifecycleState) {
			continue
		}
		at := time.UnixMilli(detail.Current.OccurredAt)
		out = append(out, topicGoal{Title: detail.Current.Title, At: at})
	}
	return out
}

// Titles in messaging and mail apps and on people's pages name people and
// conversations, not topics: they never count, and neither do the summaries,
// About you entries and files that came from those apps. Searches typed on
// those sites are not affected.
var topicPeopleApps = map[string]bool{
	"slack": true, "messages": true, "wechat": true, "微信": true, "whatsapp": true, "telegram": true,
	"discord": true, "microsoft teams": true, "teams": true, "mail": true, "outlook": true,
	"microsoft outlook": true, "signal": true, "zoom": true, "zoom.us": true, "line": true, "lark": true,
	"feishu": true, "飞书": true, "dingtalk": true, "钉钉": true, "skype": true, "messenger": true,
	"facetime": true, "contacts": true, "spark": true, "superhuman": true, "mimestream": true,
	"qq": true, "企业微信": true, "wecom": true, "wechat work": true, "thunderbird": true,
	"google chat": true, "facebook messenger": true, "airmail": true, "canary mail": true,
	"ringcentral": true,
}

var topicPeopleHosts = []string{
	"linkedin.com", "facebook.com", "instagram.com", "x.com", "twitter.com", "threads.net",
	"mail.google.com", "outlook.live.com", "outlook.office.com", "web.whatsapp.com", "messenger.com",
	"discord.com", "slack.com", "web.wechat.com", "wx.qq.com", "web.telegram.org", "teams.microsoft.com",
	"calendar.google.com", "contacts.google.com", "tiktok.com", "weibo.com",
	"mail.qq.com", "work.weixin.qq.com", "feishu.cn", "larksuite.com", "chat.google.com",
	"dingtalk.com", "mail.yahoo.com", "mail.163.com", "mail.126.com", "mail.proton.me",
	"outlook.office365.com", "teams.live.com", "messages.google.com", "web.skype.com", "web.viber.com",
}

func topicPeopleApp(name, bundleID string) bool {
	if topicPeopleApps[strings.ToLower(strings.TrimSpace(name))] {
		return true
	}
	lower := strings.ToLower(bundleID)
	if strings.HasPrefix(lower, "com.ringcentral") {
		return true
	}
	for _, marker := range []string{"slack", "messages", "mobilesms", "wechat", "whatsapp", "telegram", "discord", "teams", "mail", "outlook", "signal", "zoom", "lark", "feishu", "dingtalk", "skype", "tencent.qq", "wework", "thunderbird", "facebook.archon"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// topicAnyPeopleApp reports a summary row that involved a messaging or mail
// app: its title and entities may name the conversation or the person.
func topicAnyPeopleApp(apps []string) bool {
	for _, app := range apps {
		if topicPeopleApp(app, "") {
			return true
		}
	}
	return false
}

func topicPeopleHost(rawURL string) bool {
	host := browserhistory.Host(rawURL)
	for _, h := range topicPeopleHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// topicText scrubs one piece of text before it is ranked: personal details
// and codes are removed, so they can never surface as part of a label.
func topicText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	redacted, _ := pii.RedactText(text)
	return pii.RedactCodes(redacted)
}

// topicSignals turns the input into ranker signals and the names to avoid.
func topicSignals(in topicInput) ([]topics.Signal, []string) {
	now := in.Now
	observed := func(at time.Time) bool {
		if in.PausesUnknown {
			return false
		}
		return !pausedAt(in.Pauses, at)
	}
	// Anything older than the window but still current (an open goal) counts
	// as seen at the window's start.
	clamp := func(at time.Time) time.Time {
		floor := now.Add(-topicWindow + time.Hour)
		if at.IsZero() || at.Before(floor) {
			return floor
		}
		if at.After(now) {
			return now
		}
		return at
	}
	var out []topics.Signal
	add := func(source topics.Source, text string, at time.Time, dwell time.Duration, count int) {
		if clean := topicText(text); clean != "" {
			out = append(out, topics.Signal{Source: source, Text: clean, At: at, Dwell: dwell, Count: count})
		}
	}

	// Browser: searches and page titles from the stored visits, then the
	// journal for the days the visits no longer cover.
	byBrowser := map[string][]browserhistory.Visit{}
	liveDays := map[string]bool{}
	for _, row := range in.Visits {
		browser, _ := browserhistory.BrowserNamed(row.Browser)
		if browserVisitSensitive(browser.BundleID, row.Browser) {
			continue
		}
		at := time.UnixMilli(row.VisitedAt)
		if !observed(at) || activitymon.IsPrivateBrowsing(row.Title) {
			continue
		}
		liveDays[at.In(time.Local).Format("2006-01-02")] = true
		byBrowser[row.Browser] = append(byBrowser[row.Browser], browserhistory.Visit{URL: row.URL, Title: row.Title, At: at})
	}
	for _, visits := range byBrowser {
		for _, signal := range topicBrowserSignals(visits) {
			add(signal.Source, signal.Text, signal.At, signal.Dwell, signal.Count)
		}
	}
	for _, day := range in.Journal {
		if liveDays[day.Date] {
			continue
		}
		browser, _ := browserhistory.BrowserNamed(day.Browser)
		if browserVisitSensitive(browser.BundleID, day.Browser) {
			continue
		}
		at, err := time.ParseInLocation("2006-01-02", day.Date, time.Local)
		if err != nil {
			continue
		}
		at = at.Add(12 * time.Hour)
		if !observed(at) {
			continue
		}
		for _, phrase := range day.Phrases {
			add(topics.Source(phrase.Source), phrase.Text, at, 0, phrase.Count)
		}
	}

	for _, row := range in.Windows {
		if _, sensitive := pii.IsSensitiveApp(row.BundleID); sensitive || pii.IsSensitiveAppName(row.AppName) {
			continue
		}
		if topicPeopleApp(row.AppName, row.BundleID) {
			continue
		}
		at := time.UnixMilli(row.Last)
		if !observed(at) || activitymon.IsPrivateBrowsing(row.Title) {
			continue
		}
		add(topics.SourceWindow, row.Title, at, 0, row.Count)
	}

	for _, row := range in.Activities {
		apps, dwell := topicAppTime(row.ActiveMsByApp)
		at := time.UnixMilli(row.StartMs)
		if topicAnySensitive(apps) || topicAnyPeopleApp(apps) || !observed(at) {
			continue
		}
		add(topics.SourceTimeline, row.Title, at, dwell, 0)
		for _, name := range topicEntityNames(row.Entities) {
			add(topics.SourceEntity, name, at, dwell, 0)
		}
	}
	for _, row := range in.Semantic {
		var apps []string
		_ = json.Unmarshal([]byte(row.Apps), &apps)
		at := time.UnixMilli(row.StartMs)
		if topicAnySensitive(apps) || topicAnyPeopleApp(apps) || !observed(at) {
			continue
		}
		dwell := time.Duration(row.EndMs-row.StartMs) * time.Millisecond
		if dwell < 0 {
			dwell = 0
		}
		add(topics.SourceTimeline, row.Title, at, dwell, 0)
		for _, name := range topicEntityNames(row.Entities) {
			add(topics.SourceEntity, name, at, dwell, 0)
		}
	}

	names := append([]string(nil), in.People...)
	for _, ev := range in.Calendar {
		if ev.DeletedAt != 0 || strings.EqualFold(ev.Status, "canceled") || strings.EqualFold(ev.MyStatus, "declined") {
			continue
		}
		names = append(names, ev.OrganizerName, ev.OrganizerEmail)
		for _, attendee := range ev.Attendees {
			names = append(names, attendee.Name, attendee.Email)
		}
		// A meeting during a pause (the user paused before it) stays out.
		if !observed(time.UnixMilli(ev.StartAt)) {
			continue
		}
		add(topics.SourceMeeting, ev.Title, clamp(time.UnixMilli(ev.StartAt)), 0, 0)
	}

	for _, s := range in.Sessions {
		if s.Hidden || s.AutomationResult || s.MeetingID != "" || s.Kind == storage.SessionKindMain {
			continue
		}
		label := strings.TrimSpace(s.Label)
		if label == "" || strings.HasPrefix(label, "Transcript:") {
			continue
		}
		add(topics.SourceChat, label, time.UnixMilli(s.UpdatedAt), 0, 0)
	}

	for _, m := range in.Memories {
		switch m.Dimension {
		case "user_insight", "topic_highlight", "custom":
		default:
			continue
		}
		if topicAnySensitive(m.Apps) || topicAnyPeopleApp(m.Apps) {
			continue
		}
		at := m.UpdatedAt
		if at == 0 {
			at = m.ModTime
		}
		add(topics.SourceMemory, m.Title, time.UnixMilli(at), 0, 0)
	}

	for _, goal := range in.Goals {
		add(topics.SourceGoal, goal.Title, clamp(goal.At), 0, 0)
	}

	for _, file := range in.Files {
		if _, sensitive := pii.IsSensitiveApp(file.AppBundleID); sensitive || pii.IsSensitiveAppName(file.AppName) || topicPeopleApp(file.AppName, file.AppBundleID) {
			continue
		}
		at := time.UnixMilli(file.ActiveAt)
		if !observed(at) {
			continue
		}
		name := strings.TrimSuffix(file.Name, filepath.Ext(file.Name))
		add(topics.SourceFile, strings.NewReplacer("_", " ", "-", " ").Replace(name), at, 0, 0)
	}
	return out, names
}

// topicBrowserSignals reads one browser's visits: each search with the pages
// read after it, and the titles of the other pages, folded by title.
func topicBrowserSignals(visits []browserhistory.Visit) []topics.Signal {
	var out []topics.Signal
	for _, thread := range browserhistory.ResearchThreads(visits, 0) {
		out = append(out, topics.Signal{
			Source: topics.SourceSearch,
			Text:   thread.Query,
			At:     thread.Last,
			Dwell:  time.Duration(thread.Pages) * 2 * time.Minute,
		})
	}
	type page struct {
		title string
		count int
		last  time.Time
	}
	pages := map[string]*page{}
	var order []string
	for _, v := range visits {
		if _, search := browserhistory.SearchQuery(v.URL, v.Title); search {
			continue
		}
		if topicPeopleHost(v.URL) {
			continue
		}
		title := strings.TrimSpace(v.Title)
		if title == "" {
			continue
		}
		key := strings.ToLower(title)
		p := pages[key]
		if p == nil {
			p = &page{title: title}
			pages[key] = p
			order = append(order, key)
		}
		p.count++
		if v.At.After(p.last) {
			p.last = v.At
		}
	}
	for _, key := range order {
		p := pages[key]
		out = append(out, topics.Signal{Source: topics.SourcePage, Text: p.title, At: p.last, Count: p.count})
	}
	return out
}

// topicAppTime reads an {app: ms} map into the app names and the total.
func topicAppTime(raw string) ([]string, time.Duration) {
	var byApp map[string]int64
	if json.Unmarshal([]byte(raw), &byApp) != nil {
		return nil, 0
	}
	var apps []string
	var total int64
	for app, ms := range byApp {
		apps = append(apps, app)
		if ms > 0 {
			total += ms
		}
	}
	return apps, time.Duration(total) * time.Millisecond
}

// topicAnySensitive reports a record touched by a protected app: its title
// may carry that app's content.
func topicAnySensitive(apps []string) bool {
	for _, app := range apps {
		if pii.IsSensitiveAppName(app) {
			return true
		}
		if _, sensitive := pii.IsSensitiveApp(app); sensitive {
			return true
		}
	}
	return false
}

// topicEntityNames keeps the topic entities of a summary row.
func topicEntityNames(raw string) []string {
	var entities []Entity
	if json.Unmarshal([]byte(raw), &entities) != nil {
		return nil
	}
	var out []string
	for _, e := range entities {
		if e.Type == entityTopic && strings.TrimSpace(e.Name) != "" {
			out = append(out, e.Name)
		}
	}
	return out
}
