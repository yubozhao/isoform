package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/storage"
)

var errMCPReadPaused = errors.New("paused")

// MCP data never becomes instructions or authority, including titles and IDs.
// Redaction precedes truncation so a boundary cannot leave half a secret visible.
type mcpUntrustedText struct {
	Text      string `json:"text"`
	Untrusted bool   `json:"untrusted"`
}

func mcpReadText(text string) mcpUntrustedText {
	return mcpReadSnippet(text, "")
}

func mcpReadSnippet(text, query string) mcpUntrustedText {
	text, _ = pii.RedactText(text)
	text = pii.RedactCodes(text)
	if query != "" {
		lower := strings.ToLower(text)
		if index := strings.Index(lower, strings.ToLower(query)); index >= 0 {
			// Retain some context before the match, without cutting UTF-8.
			at := utf8.RuneCountInString(lower[:index])
			text = string([]rune(text)[max(0, at-60):])
		}
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > mcpReadSnippetLimit {
		runes = runes[:mcpReadSnippetLimit]
	}
	return mcpUntrustedText{Text: string(runes), Untrusted: true}
}

const (
	mcpReadSessionCheckLimit = 1000
	mcpReadEventCheckLimit   = 100000
)

type mcpReadPrivacy struct {
	app                     *App
	ctx                     context.Context
	pauses                  []observationPause
	hidden                  map[string]string
	ignored                 map[string]string
	sessions                map[string]bool
	activityPolicy          activityReadPrivacyPolicy
	memoryReadAllowed       func(string, string) bool
	memoryReadPolicyLoaded  bool
	memorySessions          map[string]bool
	calendar                bool
	memories                []MemoryEntry
	memoriesLoaded          bool
	memoriesErr             error
	memoryScanTruncated     bool
	memoryScanOmitted       int
	memoryProvenance        map[string]memoryReadProvenance
	memoryHistoryBudget     *memoryProvenanceReadBudget
	memoryProvenanceOmitted map[string]bool
	sessionChecks           int
	eventChecks             int
	privacyBudgetExhausted  bool
	limitedSessions         map[string]bool
	limitedPeople           map[string]bool
}

// Read the same preferences as capture on every request, including persisted
// lists, rather than trusting the privacy state at the time a summary was made.
func (a *App) mcpReadPrivacy(ctx context.Context) (*mcpReadPrivacy, error) {
	if a == nil || a.store == nil {
		return nil, errors.New("Isoform storage is not ready")
	}
	if a.IsClosedEyesModeEnabled() {
		return nil, errMCPReadPaused
	}
	p := &mcpReadPrivacy{app: a, ctx: ctx, sessions: map[string]bool{}, calendar: true}
	for _, key := range []string{"screen_recording", "accessibility", calendarPermissionKey} {
		value, err := a.store.GetUIState(ctx, permissionUIStateKey(key))
		if err != nil {
			return nil, errors.New("Isoform privacy settings are unavailable")
		}
		if value == "false" {
			if key == calendarPermissionKey {
				p.calendar = false
			} else {
				return nil, errMCPReadPaused
			}
		}
	}
	var err error
	p.activityPolicy, err = a.activityReadPrivacyPolicy()
	if err != nil {
		return nil, errors.New("Isoform privacy settings are unavailable")
	}
	p.pauses = p.activityPolicy.pauses
	if pausedAt(p.pauses, time.Now()) {
		return nil, errMCPReadPaused
	}
	for key, target := range map[string]*map[string]string{"sensitive_apps": &p.hidden, voiceInputAppsUIStateKey: &p.ignored} {
		raw, err := a.store.GetUIState(ctx, key)
		if err != nil {
			return nil, errors.New("Isoform privacy settings are unavailable")
		}
		if raw != "" && json.Unmarshal([]byte(raw), target) != nil {
			return nil, errors.New("Isoform privacy settings are unavailable")
		}
	}
	return p, nil
}

func (p *mcpReadPrivacy) appAllowed(values ...string) bool {
	return p.chatAppsAllowed("", values...)
}

func (p *mcpReadPrivacy) chatAppsAllowed(meeting string, values ...string) bool {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		// Reuse the same messaging/mail exclusion as topic and brief sources.
		// Pairing or a generated memory is not an explicit sharing grant.
		if topicPeopleApp(value, value) && !chatMeetingIdentity(meeting, value) {
			return false
		}
		if activitySourceAppIsSensitive(value, value) {
			return false
		}
		// Session app_names can lack bundle IDs. The built-in password
		// managers are protected even in those older name-only records.
		switch strings.ToLower(value) {
		case "1password", "bitwarden", "lastpass", "dashlane", "keepassxc", "keepass", "enpass", "nordpass", "roboform", "keychain access", "passwords":
			return false
		}
		for bundle, name := range p.hidden {
			if (bundle != "" && strings.HasPrefix(strings.ToLower(value), strings.ToLower(bundle))) || (name != "" && strings.EqualFold(value, name)) {
				return false
			}
		}
		for _, entry := range AllVoiceInputAppEntries() {
			if strings.EqualFold(value, entry.BundleID) || strings.EqualFold(value, entry.AppName) {
				return false
			}
		}
		for bundle, name := range p.ignored {
			if (bundle != "" && strings.EqualFold(value, bundle)) || (name != "" && strings.EqualFold(value, name)) {
				return false
			}
		}
	}
	return true
}

func (p *mcpReadPrivacy) timeAllowed(start, end int64) bool {
	if start <= 0 {
		return false
	}
	if end < start {
		end = start
	}
	if !p.activityPolicy.allows(activitySourceProvenance{StartTime: start, EndTime: end}) {
		return false
	}
	for _, pause := range p.pauses {
		if end >= pause.Start && (pause.End == 0 || start < pause.End) {
			return false
		}
	}
	return true
}

// Browser capture can identify a mail or messaging page only in the snapshot.
// Authorize that observation's own identity before exposing AX or OCR text.
func (p *mcpReadPrivacy) snapshotAllowed(app, browserURL, parsed string) bool {
	identity, err := readActivitySnapshotPrivacyIdentity(app, browserURL, parsed)
	return err == nil && p.snapshotIdentityAllowed(identity)
}

func (p *mcpReadPrivacy) snapshotIdentityAllowed(identity activitySnapshotPrivacyIdentity) bool {
	if identity.Private || !p.appAllowed(identity.Apps...) || !p.appAllowed(identity.URLs...) || !p.appAllowed(identity.Hosts...) {
		return false
	}
	for _, host := range identity.Hosts {
		if topicPeopleHost("https://" + host) {
			return false
		}
	}
	return true
}

func (p *mcpReadPrivacy) sessionSnapshotsAllowed(id string) (bool, error) {
	rows, err := p.app.activity.store.db.QueryContext(p.ctx, `SELECT COALESCE(app,''), COALESCE(browser_url,''), COALESCE(parsed_json,''), privacy_held
		FROM activity_snapshots WHERE session_id=? LIMIT 10001`, id)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		if p.eventChecks >= mcpReadEventCheckLimit || count >= 10000 {
			p.markSessionLimited(id)
			return false, nil
		}
		p.eventChecks++
		count++
		var app, browserURL, parsed string
		var held bool
		if err := rows.Scan(&app, &browserURL, &parsed, &held); err != nil {
			return false, err
		}
		if held || !p.snapshotAllowed(app, browserURL, parsed) {
			return false, nil
		}
	}
	return rows.Err() == nil, rows.Err()
}

func (p *mcpReadPrivacy) sessionAllowed(id string) bool {
	allowed, _ := p.sessionAllowedRead(id)
	return allowed
}

// Keep unavailable storage distinct from a completed privacy decision so a
// later brief item can retry a source that failed earlier in this request.
func (p *mcpReadPrivacy) sessionAllowedRead(id string) (allowed bool, err error) {
	if allowed, ok := p.sessions[id]; ok {
		return allowed, nil
	}
	defer func() {
		if err == nil {
			p.sessions[id] = allowed
		}
	}()
	if err := p.ctx.Err(); err != nil {
		return false, err
	}
	if id == "" {
		return false, nil
	}
	if p.app.activity == nil || p.app.activity.store == nil {
		return false, errors.New("Isoform activity storage is not ready")
	}
	if p.sessionChecks >= mcpReadSessionCheckLimit {
		p.markSessionLimited(id)
		return false, nil
	}
	p.sessionChecks++
	s := p.app.activity.store
	rows, err := s.GetSessionsByIDs([]string{id})
	if err != nil {
		return false, err
	}
	if rows[id] == nil {
		return false, nil
	}
	row := rows[id]
	if !p.timeAllowed(row.StartTime, row.EndTime) || !p.appAllowed(row.MeetingApp) {
		return false, nil
	}
	var apps []string
	if json.Unmarshal([]byte(row.AppNames), &apps) != nil || len(apps) == 0 || !p.appAllowed(apps...) {
		return false, nil
	}
	// The UI reader skips malformed event JSON. For external context, even
	// one unreadable event means we cannot establish private-window safety.
	events, err := s.db.QueryContext(p.ctx, `SELECT data FROM activity_events WHERE session_id=? LIMIT 10001`, id)
	if err != nil {
		return false, err
	}
	defer events.Close()
	count := 0
	for events.Next() {
		if p.eventChecks >= mcpReadEventCheckLimit || count >= 10000 {
			p.markSessionLimited(id)
			return false, nil
		}
		p.eventChecks++
		count++
		var raw string
		var event activitymon.ActivityEvent
		if err := events.Scan(&raw); err != nil {
			return false, err
		}
		if json.Unmarshal([]byte(raw), &event) != nil {
			return false, nil
		}
		// A localized or renamed display name cannot establish whether an
		// app is on a bundle-based exclusion list. Legacy name-only captures
		// therefore remain private even when their title looks ordinary.
		if strings.TrimSpace(event.AppBundleID) == "" {
			return false, nil
		}
		if !p.appAllowed(event.AppName, event.AppBundleID) || event.PrivateWindow || activitymon.IsPrivateBrowsing(event.WindowTitle) {
			return false, nil
		}
		// Capture deliberately strips a private browser's title. A browser
		// event without it cannot establish non-private provenance.
		if mcpReadBrowser(event.AppBundleID, event.AppName) && strings.TrimSpace(event.WindowTitle) == "" {
			return false, nil
		}
	}
	if err := events.Err(); err != nil {
		return false, err
	}
	if err := events.Close(); err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	// Bound capture work before calling the shared chat-context gate. This
	// also checks snapshot identities when the query matched a separate event.
	if allowed, err := p.sessionSnapshotsAllowed(id); err != nil || !allowed {
		return false, err
	}
	return p.app.activitySessionAllowsWithPolicy(s, *row, p.activityPolicy)
}

// Raw activity and people still require live observations. A memory can
// outlive raw retention only with an explicit receipt that preserves MCP's
// stricter capture checks, plus today's app and pause policy.
func (p *mcpReadPrivacy) memorySessionAllowed(id string) bool {
	if allowed, known := p.memorySessions[id]; known {
		return allowed
	}
	if p.memorySessions == nil {
		p.memorySessions = map[string]bool{}
	}
	p.memorySessions[id] = false
	if p.sessionAllowed(id) {
		if allowed, err := p.sessionSnapshotsAllowed(id); err != nil || !allowed {
			return false
		}
		// Retention may remove snapshots after the live session check. A now
		// empty snapshot query proves nothing unless the session still exists;
		// otherwise the retained receipt must establish snapshot completeness.
		var live bool
		if err := p.app.activity.store.db.QueryRowContext(p.ctx, `SELECT EXISTS (SELECT 1 FROM activity_sessions WHERE id=?)`, id).Scan(&live); err != nil {
			return false
		}
		if live {
			p.memorySessions[id] = true
			return true
		}
		p.sessions[id] = false
	}
	if id == "" || p.app.activity == nil || p.app.activity.store == nil || p.ctx.Err() != nil || p.sessionLimited(id) {
		return false
	}
	var raw string
	if err := p.app.activity.store.db.QueryRowContext(p.ctx, `SELECT provenance FROM activity_retained_source_privacy
		WHERE session_id=? AND NOT EXISTS (SELECT 1 FROM activity_sessions WHERE id=?)`, id, id).Scan(&raw); err != nil {
		return false
	}
	var source activitySourceProvenance
	if json.Unmarshal([]byte(raw), &source) != nil || source.SessionID != id || !source.Retained || !source.MCPVerified || source.Private || len(source.Apps) == 0 {
		return false
	}
	// The old receipt format did not prove which snapshots existed. An
	// explicit empty list proves there were none; a missing list does not.
	if source.MCPSnapshots == nil {
		return false
	}
	for _, snapshot := range *source.MCPSnapshots {
		if !p.snapshotIdentityAllowed(snapshot) {
			return false
		}
	}
	allowed := p.activityPolicy.allows(source) && p.timeAllowed(source.StartTime, source.EndTime) && p.appAllowed(source.Apps...)
	p.memorySessions[id] = allowed
	return allowed
}

func mcpReadBrowser(bundle, name string) bool {
	if activitymon.IsBrowser(bundle) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "safari", "google chrome", "chrome", "chromium", "firefox", "brave", "brave browser", "arc", "microsoft edge", "edge", "opera", "vivaldi":
		return true
	default:
		return false
	}
}

func (p *mcpReadPrivacy) memoryAllowed(entry MemoryEntry) bool {
	if !p.appAllowed(entry.Apps...) || topicAnyPeopleApp(entry.Apps) || topicPeopleApp(entry.Source, entry.Source) {
		return false
	}
	dir := filepath.Join(configDirOrFallback(), "memory")
	p.app.mu.Lock()
	if p.app.memoryStore != nil {
		dir = p.app.memoryStore.baseDir
	}
	p.app.mu.Unlock()
	// Backup/sync can restore a file without passing a writer or the index.
	// Recheck suppression for every raw snapshot, including cached provenance.
	if forgotten, err := isMemoryForgotten(dir, entry.ID, entry.Content, time.Now()); err != nil || forgotten {
		return false
	}
	if p.memoryProvenance == nil {
		p.memoryProvenance = map[string]memoryReadProvenance{}
		p.memoryProvenanceOmitted = map[string]bool{}
		p.memoryHistoryBudget = &memoryProvenanceReadBudget{bytes: 8 << 20, files: 1000}
	}
	provenance, known := p.memoryProvenance[entry.ID]
	if !known {
		before := p.memoryHistoryBudget.omitted
		provenance = memoryReadCapturedProvenanceBudget(dir, entry.ID, entry.Content, p.memoryHistoryBudget)
		p.memoryProvenanceOmitted[entry.ID] = p.memoryHistoryBudget.omitted > before
		p.memoryProvenance[entry.ID] = provenance
	}
	if !memoryReadProvenanceValid(provenance) || provenance.Withheld || provenance.ReadBlocked || len(provenance.Kinds) == 0 || !p.appAllowed(provenance.Apps...) {
		return false
	}
	for _, kind := range provenance.Kinds {
		switch kind {
		case "manual", "conversation":
		case "activity", "learning":
			if len(provenance.Sessions) == 0 {
				return false
			}
		default:
			return false
		}
	}
	for _, id := range compactStrings(append(provenance.Sessions, entry.ActivitySessionIDs...)) {
		if !p.memorySessionAllowed(id) {
			if p.limitedSessions[id] {
				p.memoryProvenanceOmitted[entry.ID] = true
			}
			return false
		}
	}
	// Apply the same correction suppression and flattened read provenance as
	// chat context after the bounded MCP checks. MCP-specific limits and the
	// stricter connector boundary can only further restrict that decision.
	if !p.memoryReadPolicyLoaded {
		p.memoryReadPolicyLoaded = true
		var err error
		p.memoryReadAllowed, err = p.app.memoryReadFilter()
		if err != nil {
			p.memoryReadAllowed = nil
		}
	}
	return p.memoryReadAllowed != nil && p.memoryReadAllowed(entry.ID, entry.Content)
}

func (p *mcpReadPrivacy) personAllowed(person storage.PrincipalRow) bool {
	allowed, _ := p.personAllowedRead(person)
	return allowed
}

func (p *mcpReadPrivacy) personAllowedRead(person storage.PrincipalRow) (bool, error) {
	handles, err := p.app.store.ListAppHandlesByPrincipal(p.ctx, person.ID)
	if err != nil {
		return false, err
	}
	for _, handle := range handles {
		if !p.appAllowed(handle.App) {
			return false, nil
		}
	}
	// Names imported from Contacts or entered by the user are not captures.
	switch person.CreatedFrom {
	case "manual", "onboarding", "contacts", "contacts_import":
		return true, nil
	}
	return p.personSightingsAllowedRead(person.ID)
}

// Keep observed identity provenance checks independent of the destination's
// creation source: merging into a contact does not make sightings imported.
func (p *mcpReadPrivacy) personSightingsAllowedRead(personID string) (bool, error) {
	seen := false
	for offset := 0; p.ctx.Err() == nil; offset += 200 {
		sightings, err := p.app.store.ListPrincipalSessionSightingsPage(p.ctx, personID, 200, offset)
		if err != nil {
			return false, err
		}
		for _, sighting := range sightings {
			seen = true
			allowed, err := p.sessionAllowedRead(sighting.SessionID)
			if !allowed {
				if p.sessionLimited(sighting.SessionID) {
					if p.limitedPeople == nil {
						p.limitedPeople = map[string]bool{}
					}
					p.limitedPeople[personID] = true
				}
				return false, err
			}
		}
		if len(sightings) < 200 {
			return seen, nil
		}
	}
	return false, p.ctx.Err()
}

func (p *mcpReadPrivacy) calendarAllowed(event storage.CalendarEventRow) bool {
	if !p.calendar || event.DeletedAt != 0 || event.Status == "canceled" || event.MyStatus == "declined" {
		return false
	}
	if !p.appAllowed(event.Source, event.Provider) {
		return false
	}
	if event.SessionID != "" && !p.sessionAllowed(event.SessionID) {
		return false
	}
	// Native calendar imports are not window observations. Web-derived
	// calendar text needs a source session to recheck private-window gates.
	if event.Source != "eventkit" && event.SessionID == "" {
		return false
	}
	if event.Source == "eventkit" && !p.appAllowed("com.apple.iCal", "Calendar") {
		return false
	}
	return true
}

func (p *mcpReadPrivacy) markSessionLimited(id string) {
	p.privacyBudgetExhausted = true
	if p.limitedSessions == nil {
		p.limitedSessions = map[string]bool{}
	}
	p.limitedSessions[id] = true
}

// Used by memory and brief readers after a denied candidate so bounded work is
// reported as incomplete, rather than misreported as a privacy decision.
func (p *mcpReadPrivacy) memoryLimited(id string) bool {
	return p.memoryProvenanceOmitted[id]
}

func (p *mcpReadPrivacy) sessionLimited(id string) bool {
	return p.limitedSessions[id]
}

func (p *mcpReadPrivacy) personLimited(id string) bool {
	return p.limitedPeople[id]
}
