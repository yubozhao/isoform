package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// "Isoform noticed" (backlog item 62, step 4): up to three suggestions from
// the day, written in the same model call as the day's summary. Each points
// at a real moment and offers Add follow-up or Make a routine, plus Not now.
// Go keeps every rule: what the model may suggest, which suggestions are
// shown (checked again on every read), and where a saved routine goes.
const (
	noticedKindFollowUp = "follow_up"
	noticedKindRoutine  = "routine"
	noticedMaxItems     = 3
	// noticedMaxMoments bounds the moments listed to the model.
	noticedMaxMoments   = 40
	noticedTextMaxRunes = 120
	noticedWhyMaxRunes  = 160
	noticedNameMaxRunes = 80
	noticedStepMaxRunes = 160
	noticedMaxSteps     = 8
	noticedMaxLines     = 5
	// noticedRoutineSource marks Know-how saved from a routine draft.
	noticedRoutineSource = "isoform-noticed-routine"
)

var (
	errNoticedNotFound    = errors.New("this suggestion is no longer there")
	errNoticedRoutineName = errors.New("a routine needs a name")
	errNoticedRoutineStep = errors.New("a routine needs at least one step")
	errNoticedRoutineWhen = errors.New("pick when the routine runs, or none")
	errNoticedSchedules   = errors.New("schedules are not available right now")
)

// NoticedRoutineDraft is what Make a routine opens with, and what Save takes
// back after the user's edits. When is "" (no time: Know-how), or "daily
// HH:MM", "weekdays HH:MM" or "weekly <mon..sun> HH:MM" (a Schedule).
type NoticedRoutineDraft struct {
	Name  string   `json:"name"`
	When  string   `json:"when"`
	Steps []string `json:"steps"`
	Will  []string `json:"will"`
	Wont  []string `json:"wont"`
}

// NoticedSuggestion is one row of the "Isoform noticed" box.
type NoticedSuggestion struct {
	ID            string               `json:"id"`
	Kind          string               `json:"kind"`
	MomentID      string               `json:"momentId"`
	MomentTitle   string               `json:"momentTitle"`
	MomentStartMs int64                `json:"momentStartMs"`
	Text          string               `json:"text"`
	Why           string               `json:"why"`
	Routine       *NoticedRoutineDraft `json:"routine,omitempty"`
}

// NoticedRoutineSaved says where a saved routine went: "schedule" (Upcoming)
// or "knowhow" (Knowledge), with the new entry's id.
type NoticedRoutineSaved struct {
	Home string `json:"home"`
	ID   string `json:"id"`
}

// noticedRaw is one item as the model wrote it.
type noticedRaw struct {
	Kind     string               `json:"kind"`
	MomentID string               `json:"moment_id"`
	Text     string               `json:"text"`
	Why      string               `json:"why"`
	Routine  *NoticedRoutineDraft `json:"routine,omitempty"`
}

// ── Migration ──

func migrateActivityNoticed(tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS activity_noticed (
			date       TEXT NOT NULL,
			language   TEXT NOT NULL,
			generation INTEGER NOT NULL DEFAULT 0,
			items      TEXT NOT NULL DEFAULT '[]',
			created_at INTEGER NOT NULL,
			PRIMARY KEY (date, language)
		)`,
		`CREATE TABLE IF NOT EXISTS activity_noticed_dismissals (
			key        TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL
		)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *ActivityStore) activityNoticedPresent() (bool, error) {
	a, e1 := s.hasTable("activity_noticed")
	b, e2 := s.hasTable("activity_noticed_dismissals")
	return a && b, firstErr(e1, e2)
}

// ── The request in the summary call ──

// noticedMoments are the day's moments the model may point at: the composer's
// own model-input filter, minus private or paused moments.
func (a *App) noticedMoments(dateStr string) ([]ActivityRow, map[string][]ActivityAppUsage, error) {
	if a.activity == nil || a.activity.store == nil {
		return nil, nil, errors.New("activity store not initialized")
	}
	day, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
	if err != nil {
		return nil, nil, err
	}
	store := a.activity.store
	rows, err := store.GetActivitiesInRange(day.UnixMilli(), day.AddDate(0, 0, 1).UnixMilli()-1)
	if err != nil {
		return nil, nil, err
	}
	pauses, err := a.observationPauses()
	if err != nil {
		return nil, nil, err
	}
	rows = a.filterComposerModelCandidates(store, rows)
	kept := rows[:0]
	for _, row := range rows {
		if activityActionsWithheld(row, pauses) == "" {
			kept = append(kept, row)
		}
	}
	ids := make([]string, len(kept))
	for i, row := range kept {
		ids[i] = row.ID
	}
	apps, err := store.GetActivityAppUsage(ids)
	if err != nil {
		return nil, nil, err
	}
	return kept, apps, nil
}

// withNoticedRequest adds the day's moments and the "Isoform noticed"
// instruction to the summary prompt, before its closing line. Without
// moments the prompt is unchanged.
func (a *App) withNoticedRequest(prompt, dateStr string) string {
	if prompt == "" {
		return prompt
	}
	moments, apps, err := a.noticedMoments(dateStr)
	if err != nil {
		log.Printf("[activity-noticed] moments for %s: %v", dateStr, err)
		return prompt
	}
	section := noticedPromptSection(moments, apps)
	if section == "" {
		return prompt
	}
	const closing = "\n\nWrite the summary now:"
	if i := strings.LastIndex(prompt, closing); i >= 0 {
		return prompt[:i] + "\n\n" + section + prompt[i:]
	}
	return prompt + "\n\n" + section
}

func noticedPromptSection(moments []ActivityRow, apps map[string][]ActivityAppUsage) string {
	if len(moments) == 0 {
		return ""
	}
	if len(moments) > noticedMaxMoments {
		moments = moments[len(moments)-noticedMaxMoments:]
	}
	var b strings.Builder
	b.WriteString("Moments of the day (for the suggestions block only):\n")
	for _, m := range moments {
		var names []string
		for _, app := range apps[m.ID] {
			names = append(names, app.AppName)
		}
		var people []string
		for _, e := range parseEntityList(m.Entities) {
			if e.Type == "person" {
				people = append(people, e.Name)
			}
		}
		fmt.Fprintf(&b, "- id=%s %s %q apps: %s", m.ID, time.UnixMilli(m.StartMs).Format("15:04"),
			truncateRunes(redactActivityModelText(strings.TrimSpace(m.Title)), 160), strings.Join(names, ", "))
		if len(people) > 0 {
			fmt.Fprintf(&b, " people: %s", strings.Join(people, ", "))
		}
		b.WriteString("\n")
	}
	b.WriteString(`
After the digest, add at most 3 suggestions of something useful the person could do next, in this exact form and nothing after it:
<isoform_noticed>[{"kind":"follow_up","moment_id":"<an id above>","text":"<one short line, under 100 characters, in the digest's language>","why":"<one short reason>"},{"kind":"routine","moment_id":"<an id above>","text":"<the routine's name>","why":"<one short reason>","routine":{"name":"<name>","when":"","steps":["<step>"],"will":["<what it will do>"],"wont":["<what it will not do>"]}}]</isoform_noticed>
Rules for the suggestions:
- Each must point at one moment above by its id. Suggest nothing you cannot tie to a moment.
- A follow_up is a concrete next step with a person, an order or bill, a document, or a project from that moment.
- A routine is work the person did step by step that Isoform could repeat; "when" is "" or "daily HH:MM", "weekdays HH:MM" or "weekly mon HH:MM".
- What the person typed into Claude, Codex, ChatGPT or a terminal is never a suggestion on its own.
- Use an empty array when nothing is worth suggesting.`)
	return b.String()
}

var noticedBlockRe = regexp.MustCompile(`(?s)\s*<isoform_noticed>(.*?)</isoform_noticed>\s*`)

// splitNoticedBlock removes every suggestions block from the model's text and
// returns the digest unchanged otherwise, plus the first block's items. A
// missing or invalid block is no suggestions.
func splitNoticedBlock(text string) (string, []noticedRaw) {
	var raws []noticedRaw
	if m := noticedBlockRe.FindStringSubmatch(text); m != nil {
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &raws); err != nil {
			log.Printf("[activity-noticed] the suggestions block is not valid JSON: %v", err)
			raws = nil
		}
	}
	clean := noticedBlockRe.ReplaceAllString(text, "\n")
	return strings.TrimSpace(clean), raws
}

// ── Rules ──

// noticedAITypingApp is an app where what the person does is type to an AI
// tool or a terminal.
func noticedAITypingApp(key, name string) bool {
	for _, app := range []string{key, name} {
		app = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(app)), "app:")
		switch app {
		case "claude", "chatgpt", "codex", "copilot", "gemini", "grok", "perplexity",
			"com.anthropic.claudefordesktop", "com.anthropic.claude", "com.openai.chat", "com.openai.codex",
			"com.google.gemini", "com.microsoft.copilot":
			return true
		}
	}
	return isTerminalAppFallback(key, name, "")
}

// noticedAITypingOnly reports a moment whose every app is an AI tool or a
// terminal.
func noticedAITypingOnly(apps []ActivityAppUsage) bool {
	if len(apps) == 0 {
		return true
	}
	for _, app := range apps {
		if !noticedAITypingApp(app.AppKey, app.AppName) {
			return false
		}
	}
	return true
}

// noticedAnchored applies the brief's anchor rule to a suggestion's moment.
// A follow-up needs a person, a purchase or document outcome, or a concrete
// work object on a moment that is not only typing to an AI tool; a routine
// needs steps on such a moment.
func noticedAnchored(kind string, row ActivityRow, apps []ActivityAppUsage, steps int) bool {
	typingOnly := noticedAITypingOnly(apps)
	switch kind {
	case noticedKindFollowUp:
		concrete := false
		for _, e := range parseEntityList(row.Entities) {
			switch e.Type {
			case "person":
				return true
			case "repo", "project", "pr", "channel":
				concrete = true
			}
		}
		if mobileBriefOutcomeAnchor(row.ThreadOutcome) != "" {
			return true
		}
		return concrete && !typingOnly
	case noticedKindRoutine:
		return steps >= 2 && !typingOnly
	default:
		return false
	}
}

func noticedNormalize(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func noticedID(kind, momentID, text string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + momentID + "\x00" + noticedNormalize(text)))
	return hex.EncodeToString(sum[:6])
}

// noticedDismissKeys are the keys Not now stores: the moment's suggestion of
// that kind, and the same words on any day.
func noticedDismissKeys(item NoticedSuggestion) []string {
	return []string{"moment:" + item.Kind + ":" + item.MomentID, "text:" + item.Kind + ":" + noticedNormalize(item.Text)}
}

func noticedLines(values []string, maxItems, maxRunes int) []string {
	var out []string
	for _, v := range values {
		v = activityCorrectionText(v, maxRunes)
		if v == "" {
			continue
		}
		out = append(out, v)
		if len(out) == maxItems {
			break
		}
	}
	return out
}

func noticedCleanRoutine(draft *NoticedRoutineDraft, fallbackName string) *NoticedRoutineDraft {
	if draft == nil {
		return nil
	}
	clean := &NoticedRoutineDraft{
		Name:  activityCorrectionText(draft.Name, noticedNameMaxRunes),
		Steps: noticedLines(draft.Steps, noticedMaxSteps, noticedStepMaxRunes),
		Will:  noticedLines(draft.Will, noticedMaxLines, noticedStepMaxRunes),
		Wont:  noticedLines(draft.Wont, noticedMaxLines, noticedStepMaxRunes),
	}
	if clean.Name == "" {
		clean.Name = activityCorrectionText(fallbackName, noticedNameMaxRunes)
	}
	if _, ok := noticedCronExpr(draft.When); ok {
		clean.When = strings.Join(strings.Fields(strings.ToLower(draft.When)), " ")
	}
	return clean
}

// noticedCheck applies every rule to the items for a date and returns at most
// noticedMaxItems, each with its moment's title and time.
func (a *App) noticedCheck(dateStr string, items []NoticedSuggestion) ([]NoticedSuggestion, error) {
	if len(items) == 0 {
		return nil, nil
	}
	moments, apps, err := a.noticedCandidateMoments(dateStr, items)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]ActivityRow, len(moments))
	ids := make([]string, 0, len(moments))
	for _, m := range moments {
		byID[m.ID] = m
		ids = append(ids, m.ID)
	}
	followUps, err := a.activityOpenFollowUps(ids)
	if err != nil {
		return nil, err
	}
	openTitles := a.noticedOpenCommitmentTitles()
	dismissed, err := a.activity.store.noticedDismissed()
	if err != nil {
		return nil, err
	}
	var out []NoticedSuggestion
	seen := map[string]bool{}
	for _, item := range items {
		row, ok := byID[item.MomentID]
		if !ok || seen[item.ID] {
			continue
		}
		steps := 0
		if item.Routine != nil {
			steps = len(item.Routine.Steps)
		}
		if !noticedAnchored(item.Kind, row, apps[row.ID], steps) {
			continue
		}
		if item.Kind == noticedKindFollowUp && (followUps[row.ID] != nil || openTitles[noticedNormalize(item.Text)]) {
			continue
		}
		skip := false
		for _, key := range noticedDismissKeys(item) {
			skip = skip || dismissed[key]
		}
		if skip {
			continue
		}
		seen[item.ID] = true
		item.MomentTitle = row.Title
		item.MomentStartMs = row.StartMs
		out = append(out, item)
		if len(out) == noticedMaxItems {
			break
		}
	}
	return out, nil
}

// noticedCandidateMoments reads only the moments named by the suggestions.
// The summary prompt still uses noticedMoments to consider the whole day.
// Recheck the current rows and privacy policy on every read, so a withdrawal
// or an edit is visible without waiting for a cache to expire.
func (a *App) noticedCandidateMoments(dateStr string, items []NoticedSuggestion) ([]ActivityRow, map[string][]ActivityAppUsage, error) {
	if a.activity == nil || a.activity.store == nil {
		return nil, nil, errors.New("activity store not initialized")
	}
	day, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if item.MomentID != "" && !seen[item.MomentID] {
			ids = append(ids, item.MomentID)
			seen[item.MomentID] = true
		}
	}
	if len(ids) == 0 {
		return nil, nil, nil
	}
	store := a.activity.store
	cursor, err := store.reader().Query(`SELECT `+activityColumns+` FROM activities
		WHERE id IN (SELECT value FROM json_each(?)) AND end_ms >= ? AND start_ms <= ?`,
		composeJSON(ids), day.UnixMilli(), day.AddDate(0, 0, 1).UnixMilli()-1)
	if err != nil {
		return nil, nil, err
	}
	rows, err := scanActivityRows(cursor)
	closeErr := cursor.Close()
	if err != nil {
		return nil, nil, err
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	pauses, err := a.observationPauses()
	if err != nil {
		return nil, nil, err
	}
	rows = a.filterComposerModelCandidates(store, rows)
	kept := rows[:0]
	ids = ids[:0]
	for _, row := range rows {
		if activityActionsWithheld(row, pauses) == "" {
			kept = append(kept, row)
			ids = append(ids, row.ID)
		}
	}
	apps, err := store.GetActivityAppUsage(ids)
	if err != nil {
		return nil, nil, err
	}
	return kept, apps, nil
}

// noticedOpenCommitmentTitles are the open commitments' titles, normalized,
// so a suggestion never repeats one the user already holds.
func (a *App) noticedOpenCommitmentTitles() map[string]bool {
	out := map[string]bool{}
	if a.activity == nil || a.activity.store == nil || a.activity.store.db == nil {
		return out
	}
	store := NewPurposeMapStore(a.activity.store.reader())
	details, err := store.ListDetailsAll()
	if err != nil {
		return out
	}
	for _, detail := range details {
		current := purposeCurrentLedgerEvent(detail)
		if current.LifecycleState == PurposeStateActive || current.LifecycleState == PurposeStateWaiting {
			out[noticedNormalize(current.Title)] = true
		}
	}
	return out
}

// ── Store ──

// saveNoticed keeps the checked suggestions from one summary generation;
// a later generation for the date replaces them.
func (a *App) saveNoticed(dateStr, language string, generation int, raws []noticedRaw) {
	items := make([]NoticedSuggestion, 0, len(raws))
	self := a.activitySelfNames()
	for _, raw := range raws {
		raw.Text = secondPersonLine(raw.Text, self)
		raw.Why = secondPersonLine(raw.Why, self)
		kind := strings.TrimSpace(raw.Kind)
		text := activityCorrectionText(raw.Text, noticedTextMaxRunes)
		if kind == noticedKindFollowUp && normalizeSummaryLanguage(language) == "en" {
			text = plainInvoiceFollowup(text)
		}
		if (kind != noticedKindFollowUp && kind != noticedKindRoutine) || text == "" {
			continue
		}
		item := NoticedSuggestion{
			ID:       noticedID(kind, strings.TrimSpace(raw.MomentID), text),
			Kind:     kind,
			MomentID: strings.TrimSpace(raw.MomentID),
			Text:     text,
			Why:      activityCorrectionText(raw.Why, noticedWhyMaxRunes),
		}
		if kind == noticedKindRoutine {
			item.Routine = noticedCleanRoutine(raw.Routine, text)
			if item.Routine == nil {
				continue
			}
		}
		items = append(items, item)
	}
	checked, err := a.noticedCheck(dateStr, items)
	if err != nil {
		log.Printf("[activity-noticed] checking suggestions for %s: %v", dateStr, err)
		return
	}
	for i := range checked {
		// The moment's words are read again at display time.
		checked[i].MomentTitle, checked[i].MomentStartMs = "", 0
	}
	raw, err := json.Marshal(checked)
	if err != nil {
		return
	}
	if _, err := a.activity.store.db.Exec(`INSERT INTO activity_noticed (date, language, generation, items, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(date, language) DO UPDATE SET generation = excluded.generation, items = excluded.items, created_at = excluded.created_at`,
		dateStr, normalizeSummaryLanguage(language), generation, string(raw), time.Now().UnixMilli()); err != nil {
		log.Printf("[activity-noticed] saving suggestions for %s: %v", dateStr, err)
	}
}

func (s *ActivityStore) noticedStored(dateStr, language string) ([]NoticedSuggestion, int, error) {
	var raw string
	var generation int
	err := s.reader().QueryRow(`SELECT items, generation FROM activity_noticed WHERE date = ? AND language = ?`, dateStr, normalizeSummaryLanguage(language)).Scan(&raw, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var items []NoticedSuggestion
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, generation, nil
	}
	return items, generation, nil
}

// noticedCurrent reports whether stored suggestions still belong to the day's
// summary: the summary they were written with is still there, readable, and
// not replaced. A summary Forget, retention or a privacy change withdrew
// takes its suggestions with it.
func (a *App) noticedCurrent(dateStr, language string, generation int) bool {
	if _, readable := a.readableActivitySummarySnapshot(dateStr, "daily", language); !readable {
		return false
	}
	_, current, ok := a.activity.store.GetMomentsPartialForLanguage(dateStr, language)
	return ok && current == generation
}

func (s *ActivityStore) noticedDismissed() (map[string]bool, error) {
	rows, err := s.reader().Query(`SELECT key FROM activity_noticed_dismissals`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out[key] = true
	}
	return out, rows.Err()
}

// dropNoticedForMoment removes the suggestions that cite a deleted moment.
func (s *ActivityStore) dropNoticedForMoment(tx *sql.Tx, momentID string) error {
	rows, err := tx.Query(`SELECT date, language, items FROM activity_noticed WHERE items LIKE ?`, `%"momentId":"`+momentID+`"%`)
	if err != nil {
		return err
	}
	type stored struct{ date, language, items string }
	var hits []stored
	for rows.Next() {
		var r stored
		if err := rows.Scan(&r.date, &r.language, &r.items); err != nil {
			rows.Close()
			return err
		}
		hits = append(hits, r)
	}
	rows.Close()
	for _, r := range hits {
		var items []NoticedSuggestion
		if json.Unmarshal([]byte(r.items), &items) != nil {
			items = nil
		}
		kept := items[:0]
		for _, item := range items {
			if item.MomentID != momentID {
				kept = append(kept, item)
			}
		}
		raw, _ := json.Marshal(kept)
		if _, err := tx.Exec(`UPDATE activity_noticed SET items = ? WHERE date = ? AND language = ?`, string(raw), r.date, r.language); err != nil {
			return err
		}
	}
	return nil
}

// ── Bindings ──

// GetDayNoticed returns the day's "Isoform noticed" suggestions, checked
// against every rule now.
func (a *App) GetDayNoticed(dateStr string) (perfValue []NoticedSuggestion, perfErr error) {
	defer logPageBinding("GetDayNoticed", time.Now(), &perfValue)
	return derivedRead(a, derivedDayNoticed, dateStr, func() ([]NoticedSuggestion, error) { return a.dayNoticedNow(dateStr) }, nil)
}

func (a *App) dayNoticedNow(dateStr string) ([]NoticedSuggestion, error) {
	if a.activity == nil || a.activity.store == nil {
		return []NoticedSuggestion{}, nil
	}
	language := a.currentUILanguage()
	items, generation, err := a.activity.store.noticedStored(dateStr, language)
	if err != nil {
		return nil, err
	}
	if len(items) > 0 && !a.noticedCurrent(dateStr, language, generation) {
		return []NoticedSuggestion{}, nil
	}
	checked, err := a.noticedCheck(dateStr, items)
	if err != nil {
		return nil, err
	}
	if checked == nil {
		checked = []NoticedSuggestion{}
	}
	a.secondPersonNoticed(checked)
	// The page re-reads on noticed:updated only when the day's set really
	// changed since it was last published. A read that finds the same set emits
	// nothing, so reads cannot feed each other.
	if fp, fpErr := noticedSetFingerprint(checked); fpErr == nil && noticedPublished.changed(dateStr, fp) {
		a.emitBrowserEvent("noticed:updated", map[string]string{"date": dateStr})
	}
	return checked, nil
}

// noticedSetFingerprint hashes a day's suggestions as read.
func noticedSetFingerprint(items []NoticedSuggestion) (string, error) {
	raw, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// noticedPublished remembers the fingerprint of each day's suggestions as last
// read, so a real change (and only a real change) is announced.
var noticedPublished noticedPublications

type noticedPublications struct {
	mu   sync.Mutex
	last map[string]string
}

// changed records fp for date and reports whether it differs from the value
// seen before. The first sighting is a baseline, not a change.
func (p *noticedPublications) changed(date, fp string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		p.last = map[string]string{}
	}
	prior, seen := p.last[date]
	p.last[date] = fp
	return seen && prior != fp
}

func (a *App) noticedByID(dateStr, id string) (NoticedSuggestion, error) {
	items, _, err := a.activity.store.noticedStored(dateStr, a.currentUILanguage())
	if err != nil {
		return NoticedSuggestion{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return NoticedSuggestion{}, errNoticedNotFound
}

// DismissNoticed is Not now: the suggestion, and the same words on any day,
// are not suggested again.
func (a *App) DismissNoticed(dateStr, id string) error {
	// The person's own change shows at once.
	defer a.touchDerivedViews(derivedDayNoticed)
	if a.activity == nil || a.activity.store == nil {
		return errNoticedNotFound
	}
	item, err := a.noticedByID(dateStr, id)
	if err != nil {
		return err
	}
	return a.activity.store.setNoticedDismissed(noticedDismissKeys(item), true)
}

// UndoDismissNoticed takes Not now back.
func (a *App) UndoDismissNoticed(dateStr, id string) error {
	// The person's own change shows at once.
	defer a.touchDerivedViews(derivedDayNoticed)
	if a.activity == nil || a.activity.store == nil {
		return errNoticedNotFound
	}
	item, err := a.noticedByID(dateStr, id)
	if err != nil {
		return err
	}
	return a.activity.store.setNoticedDismissed(noticedDismissKeys(item), false)
}

func (s *ActivityStore) setNoticedDismissed(keys []string, on bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, key := range keys {
		if on {
			_, err = tx.Exec(`INSERT OR IGNORE INTO activity_noticed_dismissals (key, created_at) VALUES (?, ?)`, key, time.Now().UnixMilli())
		} else {
			_, err = tx.Exec(`DELETE FROM activity_noticed_dismissals WHERE key = ?`, key)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

var noticedWeekdays = map[string]string{"sun": "0", "mon": "1", "tue": "2", "wed": "3", "thu": "4", "fri": "5", "sat": "6"}

// noticedCronExpr turns a routine's "when" into a cron expression: "" is no
// schedule; "daily HH:MM", "weekdays HH:MM" and "weekly <day> HH:MM" are.
func noticedCronExpr(when string) (string, bool) {
	fields := strings.Fields(strings.ToLower(when))
	if len(fields) == 0 {
		return "", true
	}
	clock := fields[len(fields)-1]
	t, err := time.Parse("15:04", clock)
	if err != nil {
		return "", false
	}
	minuteHour := fmt.Sprintf("%d %d", t.Minute(), t.Hour())
	switch {
	case len(fields) == 2 && fields[0] == "daily":
		return minuteHour + " * * *", true
	case len(fields) == 2 && fields[0] == "weekdays":
		return minuteHour + " * * 1-5", true
	case len(fields) == 3 && fields[0] == "weekly" && noticedWeekdays[fields[1]] != "":
		return minuteHour + " * * " + noticedWeekdays[fields[1]], true
	default:
		return "", false
	}
}

// noticedRoutinePrompt is what a scheduled routine runs: the user's steps and
// limits, and nothing beyond them.
func noticedRoutinePrompt(draft NoticedRoutineDraft) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run the routine %q for the user. Do only what is listed here.\n\nSteps:\n", draft.Name)
	for i, step := range draft.Steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, step)
	}
	if len(draft.Will) > 0 {
		b.WriteString("\nWhat this routine does:\n")
		for _, line := range draft.Will {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	if len(draft.Wont) > 0 {
		b.WriteString("\nWhat this routine never does:\n")
		for _, line := range draft.Wont {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	b.WriteString("\nIf a step needs anything not listed here, stop and report what is missing instead of doing it.")
	return b.String()
}

// noticedRoutineKnowHow is the Know-how entry a routine without a time
// becomes.
func noticedRoutineKnowHow(draft NoticedRoutineDraft, dateStr string, now time.Time, lang string) string {
	labels := map[string]string{"steps": "Steps", "will": "What it does", "wont": "What it never does"}
	if lang == "zh-CN" {
		labels = map[string]string{"steps": "步骤", "will": "它会做什么", "wont": "它不会做什么"}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", labels["steps"])
	for i, step := range draft.Steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, step)
	}
	for _, part := range []struct {
		label string
		lines []string
	}{{labels["will"], draft.Will}, {labels["wont"], draft.Wont}} {
		if len(part.lines) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n", part.label)
		for _, line := range part.lines {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	return renderOKFDocument(map[string]any{
		"type":      "Isoform Knowledge",
		"kind":      "how_to",
		"title":     draft.Name,
		"timestamp": okfTimestamp(now),
		"source":    noticedRoutineSource,
		"from_day":  dateStr,
	}, b.String())
}

// SaveNoticedRoutine saves the user's routine draft. With a time it becomes
// a Schedule on Upcoming; without one, a Know-how entry. Nothing is saved or
// run before this call. The suggestion is then not shown again.
func (a *App) SaveNoticedRoutine(dateStr, id string, draft NoticedRoutineDraft) (NoticedRoutineSaved, error) {
	// The person's own change shows at once.
	defer a.touchDerivedViews(derivedDayNoticed)
	if a.activity == nil || a.activity.store == nil {
		return NoticedRoutineSaved{}, errNoticedNotFound
	}
	item, err := a.noticedByID(dateStr, id)
	if err != nil {
		return NoticedRoutineSaved{}, err
	}
	clean := noticedCleanRoutine(&draft, "")
	if clean.Name == "" {
		return NoticedRoutineSaved{}, errNoticedRoutineName
	}
	if len(clean.Steps) == 0 {
		return NoticedRoutineSaved{}, errNoticedRoutineStep
	}
	expr, ok := noticedCronExpr(draft.When)
	if !ok {
		return NoticedRoutineSaved{}, errNoticedRoutineWhen
	}
	var saved NoticedRoutineSaved
	if expr != "" {
		saved, err = a.saveNoticedSchedule(*clean, expr, dateStr)
	} else {
		saved, err = a.saveNoticedKnowHow(*clean, dateStr, time.Now())
	}
	if err != nil {
		return NoticedRoutineSaved{}, err
	}
	if err := a.activity.store.setNoticedDismissed(noticedDismissKeys(item), true); err != nil {
		log.Printf("[activity-noticed] marking a saved routine as handled: %v", err)
	}
	return saved, nil
}

func (a *App) saveNoticedSchedule(draft NoticedRoutineDraft, expr, dateStr string) (NoticedRoutineSaved, error) {
	if a.cronScheduler == nil {
		return NoticedRoutineSaved{}, errNoticedSchedules
	}
	schedule, err := json.Marshal(scheduleCron{Expr: expr, TZ: time.Local.String()})
	if err != nil {
		return NoticedRoutineSaved{}, err
	}
	target, err := json.Marshal(cronTargetConfig{Prompt: noticedRoutinePrompt(draft), Model: a.chatTaskModel()})
	if err != nil {
		return NoticedRoutineSaved{}, err
	}
	state, err := a.CreateCronJob(CronJobInput{
		Name:           draft.Name,
		Description:    "Saved from Isoform noticed (" + dateStr + ")",
		ScheduleKind:   "cron",
		ScheduleConfig: schedule,
		TargetKind:     "agent",
		TargetConfig:   target,
	})
	if err != nil {
		return NoticedRoutineSaved{}, err
	}
	return NoticedRoutineSaved{Home: "schedule", ID: state.ID}, nil
}

func (a *App) saveNoticedKnowHow(draft NoticedRoutineDraft, dateStr string, now time.Time) (NoticedRoutineSaved, error) {
	dir, err := knowledgeDir()
	if err != nil {
		return NoticedRoutineSaved{}, err
	}
	sum := sha256.Sum256([]byte(dateStr + "\x00" + draft.Name + "\x00" + now.Format(time.RFC3339Nano)))
	id := "routine-" + hex.EncodeToString(sum[:6])
	unlock := memoryFileLocks.Acquire("knowledge:" + id)
	defer unlock()
	path := filepath.Join(dir, id+".md")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return NoticedRoutineSaved{}, err
	}
	if _, err := file.WriteString(noticedRoutineKnowHow(draft, dateStr, now, a.currentUILanguage())); err != nil {
		file.Close()
		_ = os.Remove(path)
		return NoticedRoutineSaved{}, err
	}
	if err := file.Close(); err != nil {
		return NoticedRoutineSaved{}, err
	}
	a.markMemoryDirty("knowledge-save")
	if a.ctx != nil {
		a.emitBrowserEvent("insight:saved", map[string]string{"kind": "knowledge", "id": id, "title": draft.Name, "source": "edit"})
	}
	return NoticedRoutineSaved{Home: "knowhow", ID: id}, nil
}
