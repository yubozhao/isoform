package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// The owner portrait is Agent of You step 1 (integrations/claude-code-plugin):
// portrait_draft builds a short, sourced draft from what Isoform already
// learned; portrait_keep saves the person's corrected version on this
// computer. Nothing here uploads; the agent page is a later, separate approval.

const (
	portraitSectionWorksOn  = "works_on"
	portraitSectionHow      = "how_they_work"
	portraitSectionWith     = "works_with"
	portraitSectionStrength = "strengths"
	portraitSectionWontDo   = "wont_do"

	portraitSourceAboutYou  = "about_you"
	portraitSourceKnowHow   = "how_you_do_things"
	portraitSourcePeople    = "who_you_know"
	portraitSourceActivity  = "activity"
	portraitSourceMemory    = "claude_memory"
	portraitSourceNotes     = "project_notes"
	portraitSourcePerson    = "person"
	portraitDraftDays       = 30
	portraitSectionMaxLines = 8
	portraitLineMaxRunes    = 200
	portraitActivityScan    = mcpReadSessionCheckLimit
	portraitPeopleScan      = 300
	ownerPortraitFileName   = "owner-portrait.json"
	ownerPortraitFormat     = 1
)

// portraitSections is the fixed order the draft and the kept portrait use.
var portraitSections = []string{portraitSectionWorksOn, portraitSectionHow, portraitSectionWith, portraitSectionStrength, portraitSectionWontDo}

// The draft can only name Isoform's own sources; a kept line may also come
// from the person's Claude memory, project notes, or their own words.
var portraitDraftSources = map[string]bool{portraitSourceAboutYou: true, portraitSourceKnowHow: true, portraitSourcePeople: true, portraitSourceActivity: true}
var portraitKeepSources = map[string]bool{portraitSourceAboutYou: true, portraitSourceKnowHow: true, portraitSourcePeople: true, portraitSourceActivity: true, portraitSourceMemory: true, portraitSourceNotes: true, portraitSourcePerson: true}

// A belief that opens with one of these is a boundary: it goes under wont_do.
var portraitWontDoPrefixes = []string{"never ", "won't ", "will not ", "doesn't ", "does not ", "avoids ", "refuses ", "declines ", "stays away from "}

type mcpPortraitLine struct {
	Text   mcpUntrustedText `json:"text"`
	Source string           `json:"source"`
	// Basis is a code-made phrase: why Isoform believes the line.
	Basis string `json:"basis"`
}

type mcpPortraitSection struct {
	ID    string            `json:"id"`
	Lines []mcpPortraitLine `json:"lines"`
}

type mcpPortraitDraft struct {
	Sections []mcpPortraitSection `json:"sections"`
	// Withheld counts candidate lines dropped by privacy checks.
	Withheld  int    `json:"withheld"`
	Truncated bool   `json:"truncated"`
	Note      string `json:"note"`
}

const portraitDraftNote = "Draft only. Lines are short facts; no screen text, transcripts, or other people's names or details. Show each line with its source, take corrections, and call portrait_keep only after the person explicitly says keep."

// portraitDraftBuilder collects lines per section and drops duplicates and
// anything the work-profile cleaner would have to change.
type portraitDraftBuilder struct {
	fullName string
	private  []string
	sections map[string][]mcpPortraitLine
	seen     map[string]bool
	withheld int
}

func newPortraitDraftBuilder(fullName string, private []string) *portraitDraftBuilder {
	return &portraitDraftBuilder{fullName: fullName, private: private, sections: map[string][]mcpPortraitLine{}, seen: map[string]bool{}}
}

func (b *portraitDraftBuilder) add(section, text, source, basis string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	// "[...]" is what an earlier redaction left behind.
	if portraitSensitiveTopic(text) || strings.Contains(text, "[...]") {
		b.withheld++
		return
	}
	clean, changed := workProfileClean(text, b.fullName, b.private)
	if changed || clean == "" || utf8.RuneCountInString(clean) > portraitLineMaxRunes {
		// A line with a name, number or secret is dropped whole: a cleaned
		// half sentence can still point at the detail it lost.
		b.withheld++
		return
	}
	key := strings.ToLower(clean)
	if b.seen[key] || len(b.sections[section]) >= portraitSectionMaxLines {
		return
	}
	b.seen[key] = true
	b.sections[section] = append(b.sections[section], mcpPortraitLine{Text: mcpReadText(clean), Source: source, Basis: basis})
}

func (b *portraitDraftBuilder) draft(truncated bool) mcpPortraitDraft {
	out := mcpPortraitDraft{Sections: make([]mcpPortraitSection, 0, len(portraitSections)), Withheld: b.withheld, Truncated: truncated, Note: portraitDraftNote}
	for _, id := range portraitSections {
		lines := b.sections[id]
		if lines == nil {
			lines = []mcpPortraitLine{}
		}
		out.Sections = append(out.Sections, mcpPortraitSection{ID: id, Lines: lines})
	}
	return out
}

// portraitPrivateValues are the strings no portrait line may contain: the
// person's full name and, when an Agent of You reservation exists, its hub.
// It only reads; it never creates the reservation file.
func portraitPrivateValues() (string, []string) {
	name := yourAgentFullName()
	dir, err := flavorDataDir()
	if err != nil {
		return name, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, yourAgentStateFileName))
	if err != nil {
		return name, nil
	}
	var state struct {
		Hub string `json:"hub"`
	}
	if json.Unmarshal(data, &state) != nil || strings.TrimSpace(state.Hub) == "" {
		return name, nil
	}
	return name, []string{state.Hub}
}

func (a *App) mcpPortraitDraft(ctx context.Context) (mcpPortraitDraft, error) {
	p, err := a.mcpReadPrivacy(ctx)
	if err != nil {
		return mcpPortraitDraft{}, err
	}
	name, private := portraitPrivateValues()
	b := newPortraitDraftBuilder(name, private)
	truncated := false
	if dir, err := knowledgeDir(); err == nil {
		if err := portraitAddKnowHow(b, workProfileDiskSources{dir: dir}); err != nil {
			return mcpPortraitDraft{}, errors.New("Isoform read data is unavailable")
		}
	}
	uses, err := portraitAddBeliefs(b, p)
	if err != nil {
		return mcpPortraitDraft{}, errors.New("Isoform read data is unavailable")
	}
	limited, err := portraitAddPeople(b, p)
	if err != nil {
		return mcpPortraitDraft{}, errors.New("Isoform read data is unavailable")
	}
	truncated = truncated || limited
	limited, err = portraitAddActivity(b, p, time.Now())
	if err != nil {
		return mcpPortraitDraft{}, errors.New("Isoform read data is unavailable")
	}
	truncated = truncated || limited || p.memoryScanTruncated || p.privacyBudgetExhausted
	// A pause during the reads must suppress the in-flight draft too.
	if _, err := a.mcpReadPrivacy(ctx); err != nil {
		return mcpPortraitDraft{}, err
	}
	a.recordContextEntries(uses, "assistant_tool", "")
	return b.draft(truncated), nil
}

// portraitAddKnowHow reuses the work profile's candidates, which already
// keep only eligible notes and clean names out: specialties are strengths,
// the taste lines are how the person works.
func portraitAddKnowHow(b *portraitDraftBuilder, sources WorkProfileSources) error {
	candidates, _, err := workProfileCandidates(sources, b.fullName, b.private)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, candidate := range candidates {
		if candidate.Filtered {
			b.withheld++
			continue
		}
		switch candidate.Kind {
		case "specialty":
			basis := "from How you do things"
			if candidate.Sessions > 0 {
				basis = fmt.Sprintf("from How you do things, seen in %d sessions over %s", candidate.Sessions, candidate.Span)
			}
			b.add(portraitSectionStrength, candidate.Text, portraitSourceKnowHow, basis)
		case "taste":
			basis := "from a How you do things note"
			if candidate.LastSeen != "" {
				basis += ", last seen " + candidate.LastSeen
			}
			b.add(portraitSectionHow, candidate.Text, portraitSourceKnowHow, basis)
		}
	}
	return nil
}

// portraitBeliefSection places an About you belief, or returns "" for one
// that does not belong in a public-facing portrait.
func portraitBeliefSection(area, title string) string {
	lower := strings.ToLower(strings.TrimSpace(title))
	switch area {
	case "people", "interests", "other":
		return ""
	case "work", "tools", "routines", "communication":
	}
	for _, prefix := range portraitWontDoPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return portraitSectionWontDo
		}
	}
	switch area {
	case "work":
		return portraitSectionWorksOn
	case "tools", "routines", "communication":
		return portraitSectionHow
	}
	return ""
}

func portraitAddBeliefs(b *portraitDraftBuilder, p *mcpReadPrivacy) ([]contextUseEntry, error) {
	entries, err := p.memoryEntries()
	if err != nil {
		return nil, err
	}
	useEpoch := p.app.contextUseEpoch()
	// Confirmed beliefs first, so they win the per-section cap.
	sort.SliceStable(entries, func(i, j int) bool {
		return memoryConfirmedAt(entries[i].Content) > 0 && memoryConfirmedAt(entries[j].Content) == 0
	})
	var uses []contextUseEntry
	for _, entry := range entries {
		if p.ctx.Err() != nil {
			return nil, p.ctx.Err()
		}
		if !memoryInUse(entry.Content) {
			continue
		}
		// The raw title, so the cleaner sees (and drops) a stored secret
		// instead of a display placeholder.
		title := extractTitle(entry.Content)
		if pii.DisplayText(title) != title {
			b.withheld++
			continue
		}
		if memoryEntryIsPromptText(title, entry.Content) {
			continue
		}
		area := memoryAreaOrOther(memoryAreaOnRead(entry.Content, parseMemoryFrontmatter(entry.Content)))
		section := portraitBeliefSection(area, title)
		if section == "" {
			if area == "people" {
				b.withheld++
			}
			continue
		}
		if !p.memoryAllowed(entry) {
			b.withheld++
			continue
		}
		basis := "Isoform's guess from your activity; not confirmed by you"
		if memoryConfirmedAt(entry.Content) > 0 {
			basis = "you confirmed this in About you"
		}
		before := len(b.sections[section])
		b.add(section, title, portraitSourceAboutYou, basis)
		if len(b.sections[section]) > before {
			uses = append(uses, contextUseEntry{"memory", entry.ID, useEpoch})
		}
	}
	return uses, nil
}

// portraitAddPeople says how many colleagues Isoform knows of: a count only,
// never a name or anything about them.
func portraitAddPeople(b *portraitDraftBuilder, p *mcpReadPrivacy) (bool, error) {
	people, err := p.app.store.ListPrincipalsByKind(p.ctx, storage.PrincipalKindContact)
	if err != nil {
		return false, err
	}
	limited := false
	colleagues := 0
	checked := 0
	for _, person := range people {
		if person.Relationship != "colleague" {
			continue
		}
		if checked == portraitPeopleScan {
			limited = true
			break
		}
		checked++
		if p.personAllowed(person) {
			colleagues++
		}
	}
	portraitAddColleagues(b, colleagues, limited)
	return limited, nil
}

func portraitAddColleagues(b *portraitDraftBuilder, colleagues int, limited bool) {
	if colleagues == 0 {
		return
	}
	count := fmt.Sprintf("%d", colleagues)
	if limited {
		count = "more than " + count
	}
	noun := "colleagues"
	if colleagues == 1 {
		noun = "colleague"
	}
	// Code-made text: the count is ours, so it skips the cleaner's digit rule.
	b.sections[portraitSectionWith] = append(b.sections[portraitSectionWith], mcpPortraitLine{
		Text:   mcpReadText(fmt.Sprintf("Works with %s %s Isoform knows of", count, noun)),
		Source: portraitSourcePeople,
		Basis:  "a count from Who you know; no names or details are shared",
	})
}

// portraitActivitySession is one activity session's start and apps.
type portraitActivitySession struct {
	ID    string
	Start int64
	Apps  []string
}

// portraitAddActivity names the apps the person used on the most days of
// the last 30: app names and day counts only, never window or screen text.
func portraitAddActivity(b *portraitDraftBuilder, p *mcpReadPrivacy, now time.Time) (bool, error) {
	if p.app.activity == nil || p.app.activity.store == nil {
		return false, nil
	}
	since := now.AddDate(0, 0, -portraitDraftDays)
	rows, err := p.app.activity.store.db.QueryContext(p.ctx, `SELECT id,start_time,app_names FROM activity_sessions
		WHERE start_time>=? AND COALESCE(triage_status,'')!='protected' ORDER BY start_time DESC,id LIMIT ?`, since.UnixMilli(), portraitActivityScan+1)
	if err != nil {
		return false, err
	}
	var sessions []portraitActivitySession
	for rows.Next() {
		var item portraitActivitySession
		var apps sql.NullString
		if err := rows.Scan(&item.ID, &item.Start, &apps); err != nil {
			rows.Close()
			return false, err
		}
		_ = json.Unmarshal([]byte(apps.String), &item.Apps)
		sessions = append(sessions, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	limited := len(sessions) > portraitActivityScan
	if limited {
		sessions = sessions[:portraitActivityScan]
	}
	allowed := sessions[:0]
	for _, session := range sessions {
		if p.sessionAllowed(session.ID) {
			allowed = append(allowed, session)
		}
	}
	portraitAddApps(b, allowed, func(app string) bool {
		return p.appAllowed(app) && !activitySourceAppIsSensitive("", app)
	})
	return limited, nil
}

func portraitAddApps(b *portraitDraftBuilder, sessions []portraitActivitySession, allowed func(string) bool) {
	appDays := map[string]map[string]bool{}
	activeDays := map[string]bool{}
	for _, session := range sessions {
		day := time.UnixMilli(session.Start).In(time.Local).Format("2006-01-02")
		activeDays[day] = true
		for _, app := range session.Apps {
			app = strings.TrimSpace(app)
			if app == "" || !allowed(app) {
				continue
			}
			if clean, changed := workProfileClean(app, b.fullName, b.private); changed || clean == "" {
				continue
			}
			if appDays[app] == nil {
				appDays[app] = map[string]bool{}
			}
			appDays[app][day] = true
		}
	}
	type appCount struct {
		name string
		days int
	}
	var apps []appCount
	for name, days := range appDays {
		if len(days) >= 3 {
			apps = append(apps, appCount{name, len(days)})
		}
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].days == apps[j].days {
			return apps[i].name < apps[j].name
		}
		return apps[i].days > apps[j].days
	})
	if len(apps) == 0 {
		return
	}
	if len(apps) > 3 {
		apps = apps[:3]
	}
	names := make([]string, len(apps))
	for i, app := range apps {
		names[i] = app.name
	}
	list := names[0]
	switch len(names) {
	case 2:
		list = names[0] + " and " + names[1]
	case 3:
		list = names[0] + ", " + names[1] + " and " + names[2]
	}
	b.sections[portraitSectionHow] = append(b.sections[portraitSectionHow], mcpPortraitLine{
		Text:   mcpReadText("Works mostly in " + list),
		Source: portraitSourceActivity,
		Basis:  fmt.Sprintf("app names only, from activity on %d of the last %d days", len(activeDays), portraitDraftDays),
	})
}

// --- portrait_keep -------------------------------------------------------

type ownerPortraitLine struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

type ownerPortraitSection struct {
	ID    string              `json:"id"`
	Lines []ownerPortraitLine `json:"lines"`
}

// ownerPortraitPhase is the kept portrait's lifecycle on this computer.
type ownerPortraitPhase string

const (
	ownerPortraitNone ownerPortraitPhase = "none"
	ownerPortraitKept ownerPortraitPhase = "kept"
)

type ownerPortraitState struct {
	Format   int                    `json:"format"`
	Phase    ownerPortraitPhase     `json:"phase"`
	Version  int64                  `json:"version"`
	KeptAt   string                 `json:"keptAt,omitempty"`
	KeptFrom string                 `json:"keptFrom,omitempty"`
	Sections []ownerPortraitSection `json:"sections,omitempty"`
}

type ownerPortraitEventKind string

const (
	ownerPortraitEventKeep   ownerPortraitEventKind = "keep"
	ownerPortraitEventForget ownerPortraitEventKind = "forget"
)

type ownerPortraitEvent struct {
	Kind     ownerPortraitEventKind
	Sections []ownerPortraitSection
	Client   string
	At       time.Time
}

type ownerPortraitEffect string

const (
	ownerPortraitEffectWrite  ownerPortraitEffect = "write"
	ownerPortraitEffectRemove ownerPortraitEffect = "remove"
)

// ownerPortraitTransition is the whole lifecycle: Keep saves a new version
// from either phase; Forget returns to none. Pure; the caller runs effects.
func ownerPortraitTransition(state ownerPortraitState, event ownerPortraitEvent) (ownerPortraitState, []ownerPortraitEffect, error) {
	keep := func() ownerPortraitState {
		return ownerPortraitState{Format: ownerPortraitFormat, Phase: ownerPortraitKept, Version: state.Version + 1, KeptAt: event.At.UTC().Format(time.RFC3339), KeptFrom: event.Client, Sections: event.Sections}
	}
	switch state.Phase {
	case ownerPortraitNone:
		switch event.Kind {
		case ownerPortraitEventKeep:
			return keep(), []ownerPortraitEffect{ownerPortraitEffectWrite}, nil
		case ownerPortraitEventForget:
			return ownerPortraitState{Format: ownerPortraitFormat, Phase: ownerPortraitNone}, nil, nil
		}
	case ownerPortraitKept:
		switch event.Kind {
		case ownerPortraitEventKeep:
			return keep(), []ownerPortraitEffect{ownerPortraitEffectWrite}, nil
		case ownerPortraitEventForget:
			return ownerPortraitState{Format: ownerPortraitFormat, Phase: ownerPortraitNone}, []ownerPortraitEffect{ownerPortraitEffectRemove}, nil
		}
	}
	return state, nil, fmt.Errorf("owner portrait: no transition from %q on %q", state.Phase, event.Kind)
}

// ownerPortraitValidate checks the corrected portrait and refuses, rather
// than edits, any line with a name, contact detail, number or secret.
func ownerPortraitValidate(sections []ownerPortraitSection, fullName string, private []string) error {
	if len(sections) == 0 {
		return mcpReadInvalid("sections are required")
	}
	known := map[string]bool{}
	for _, id := range portraitSections {
		known[id] = true
	}
	seen := map[string]bool{}
	total := 0
	for _, section := range sections {
		if !known[section.ID] {
			return mcpReadInvalid("unknown portrait section " + section.ID)
		}
		if seen[section.ID] {
			return mcpReadInvalid("portrait section " + section.ID + " appears twice")
		}
		seen[section.ID] = true
		if len(section.Lines) > portraitSectionMaxLines {
			return mcpReadInvalid(fmt.Sprintf("portrait section %s has more than %d lines", section.ID, portraitSectionMaxLines))
		}
		for i, line := range section.Lines {
			where := fmt.Sprintf("line %d in %s", i+1, section.ID)
			if !portraitKeepSources[line.Source] {
				return mcpReadInvalid(where + " has an unknown source")
			}
			text := strings.TrimSpace(line.Text)
			if text == "" || utf8.RuneCountInString(text) > portraitLineMaxRunes || strings.IndexFunc(text, unicode.IsControl) >= 0 {
				return mcpReadInvalid(fmt.Sprintf("%s must be one line of 1 to %d characters", where, portraitLineMaxRunes))
			}
			if line.Source != portraitSourcePeople {
				if _, changed := workProfileClean(text, fullName, private); changed {
					return mcpReadInvalid(where + " has a name, contact detail, number or other private detail; reword it without that and keep again")
				}
			} else if redacted, _ := pii.RedactText(text); redacted != text {
				return mcpReadInvalid(where + " has a contact detail or other private detail; reword it without that and keep again")
			}
			if strings.Contains(text, "[...]") {
				return mcpReadInvalid(where + " still has a redaction mark; reword it and keep again")
			}
			if portraitSensitiveTopic(text) {
				return mcpReadInvalid(where + " touches health, money, a relationship or another private subject; leave it out of the portrait")
			}
			total++
		}
	}
	if total == 0 {
		return mcpReadInvalid("the portrait has no lines")
	}
	return nil
}

func ownerPortraitPath() (string, error) {
	dir, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ownerPortraitFileName), nil
}

func ownerPortraitRead(path string) (ownerPortraitState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ownerPortraitState{Format: ownerPortraitFormat, Phase: ownerPortraitNone}, nil
	}
	if err != nil {
		return ownerPortraitState{}, err
	}
	var state ownerPortraitState
	if json.Unmarshal(data, &state) != nil || state.Format != ownerPortraitFormat || (state.Phase != ownerPortraitNone && state.Phase != ownerPortraitKept) {
		return ownerPortraitState{}, errors.New("invalid owner portrait")
	}
	return state, nil
}

// ownerPortraitApply runs one event against the file at path. It shares the
// work profile's lock: both are the owner's Agent of You data.
func ownerPortraitApply(path string, event ownerPortraitEvent) (ownerPortraitState, error) {
	workProfileStateMu.Lock()
	defer workProfileStateMu.Unlock()
	state, err := ownerPortraitRead(path)
	if err != nil && event.Kind != ownerPortraitEventForget {
		return ownerPortraitState{}, err
	}
	if err != nil {
		// A damaged file still goes on Forget.
		state = ownerPortraitState{Format: ownerPortraitFormat, Phase: ownerPortraitKept}
	}
	next, effects, err := ownerPortraitTransition(state, event)
	if err != nil {
		return ownerPortraitState{}, err
	}
	for _, effect := range effects {
		switch effect {
		case ownerPortraitEffectWrite:
			if err := ownerPortraitWrite(path, next); err != nil {
				return ownerPortraitState{}, err
			}
		case ownerPortraitEffectRemove:
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return ownerPortraitState{}, err
			}
		}
	}
	return next, nil
}

func ownerPortraitWrite(path string, state ownerPortraitState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".owner-portrait-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

type mcpPortraitKeepResult struct {
	Kept    bool   `json:"kept"`
	Version int64  `json:"version"`
	KeptAt  string `json:"keptAt"`
	Lines   int    `json:"lines"`
	Note    string `json:"note"`
}

const portraitKeepNote = "Kept on this computer only, as Agent of You step 1. Nothing was uploaded. An agent page on isoform.ai needs its own approval later."

func (a *App) mcpPortraitKeep(ctx context.Context, client string, raw json.RawMessage) (mcpPortraitKeepResult, error) {
	// Keep honors the same pause and closed-eyes rules as every read.
	if _, err := a.mcpReadPrivacy(ctx); err != nil {
		return mcpPortraitKeepResult{}, err
	}
	var args struct {
		Sections []ownerPortraitSection `json:"sections"`
	}
	if err := decodeStrictJSON(raw, &args); err != nil {
		return mcpPortraitKeepResult{}, mcpReadInvalid("invalid portrait_keep arguments")
	}
	name, private := portraitPrivateValues()
	if err := ownerPortraitValidate(args.Sections, name, private); err != nil {
		return mcpPortraitKeepResult{}, err
	}
	sections := ownerPortraitOrdered(args.Sections)
	path, err := ownerPortraitPath()
	if err != nil {
		return mcpPortraitKeepResult{}, errors.New("Isoform could not keep the portrait")
	}
	state, err := ownerPortraitApply(path, ownerPortraitEvent{Kind: ownerPortraitEventKeep, Sections: sections, Client: client, At: time.Now()})
	if err != nil {
		return mcpPortraitKeepResult{}, errors.New("Isoform could not keep the portrait")
	}
	lines := 0
	for _, section := range state.Sections {
		lines += len(section.Lines)
	}
	return mcpPortraitKeepResult{Kept: true, Version: state.Version, KeptAt: state.KeptAt, Lines: lines, Note: portraitKeepNote}, nil
}

// ownerPortraitOrdered stores sections in the fixed order with trimmed text.
func ownerPortraitOrdered(sections []ownerPortraitSection) []ownerPortraitSection {
	byID := map[string]ownerPortraitSection{}
	for _, section := range sections {
		byID[section.ID] = section
	}
	out := []ownerPortraitSection{}
	for _, id := range portraitSections {
		section, ok := byID[id]
		if !ok || len(section.Lines) == 0 {
			continue
		}
		lines := make([]ownerPortraitLine, len(section.Lines))
		for i, line := range section.Lines {
			lines[i] = ownerPortraitLine{Text: strings.TrimSpace(line.Text), Source: line.Source}
		}
		out = append(out, ownerPortraitSection{ID: id, Lines: lines})
	}
	return out
}

func decodeStrictJSON(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// forgetOwnerPortrait removes the kept portrait (part of the work profile
// step of Forget everything).
func forgetOwnerPortrait() error {
	path, err := ownerPortraitPath()
	if err != nil {
		return err
	}
	_, err = ownerPortraitApply(path, ownerPortraitEvent{Kind: ownerPortraitEventForget, At: time.Now()})
	return err
}
