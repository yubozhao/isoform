package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"wails-gui/storage"
)

// Summary fingerprints retain only content and hashed run identities. The
// latter let a replacement bucket be distinguished from unrelated work with
// the same wording, including when the original runs are folded into a day.
type mobileBriefSummaryPrint struct {
	Content string   `json:"content"`
	Start   int64    `json:"start"`
	End     int64    `json:"end"`
	Runs    []string `json:"runs"`
}

func mobileBriefSummaryFingerprint(row ActivityRow) string {
	p := mobileBriefSummaryPrint{
		Content: mobileBriefModelDigest(struct{ Title, Description, LatestChange string }{row.Title, row.Description, row.LatestChange}),
		Start:   row.StartMs, End: row.EndMs,
	}
	for _, id := range composeParseStrings(row.RunIDs) {
		p.Runs = append(p.Runs, mobileBriefModelDigest(id))
	}
	sort.Strings(p.Runs)
	raw, _ := json.Marshal(p)
	return "summary-v1:" + base64.RawURLEncoding.EncodeToString(raw)
}

func mobileBriefParseSummaryFingerprint(value string) (mobileBriefSummaryPrint, bool) {
	var p mobileBriefSummaryPrint
	if !strings.HasPrefix(value, "summary-v1:") {
		return p, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "summary-v1:"))
	if err != nil || json.Unmarshal(raw, &p) != nil || p.Content == "" {
		return p, false
	}
	return p, true
}

func mobileBriefSummaryContainsRuns(current, original mobileBriefSummaryPrint) bool {
	runs := make(map[string]bool, len(current.Runs))
	for _, run := range current.Runs {
		runs[run] = true
	}
	for _, run := range original.Runs {
		if !runs[run] {
			return false
		}
	}
	return true
}

func (a *App) mobileBriefSummaryReadable(ctx context.Context, row ActivityRow) bool {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return false
	}
	store := a.activity.store
	pauses, err := a.observationPauses()
	reader := mobileBriefPurposeReader{app: a, store: store, asOf: time.Now(), pauses: pauses, pausesReadable: err == nil, cache: map[string]mobileBriefPurposeObservation{}}
	observation := reader.source("activity", row.ID, 0, false)
	return !observation.Sensitive && !observation.Unavailable && !observation.MissingEvidence && len(a.filterComposerModelCandidatesContext(ctx, store, []ActivityRow{row})) == 1
}

// Resolve a regenerated summary only when its text and every original run
// survive in an overlapping bucket. A present but edited original never falls
// back to a different bucket.
func (a *App) mobileBriefSummaryReplacement(ctx context.Context, id string, p mobileBriefSummaryPrint) (string, bool) {
	if a == nil || a.activity == nil || a.activity.store == nil || len(p.Runs) == 0 || p.Start <= 0 || p.End < p.Start {
		return "", false
	}
	store := a.activity.store
	original, err := store.GetActivityByIDContext(ctx, id)
	if err != nil || original != nil {
		return "", false
	}
	rows, err := store.GetActivitiesInRangeContext(ctx, p.Start, p.End)
	if err != nil {
		return "", false
	}
	for _, row := range rows {
		current, ok := mobileBriefParseSummaryFingerprint(mobileBriefSummaryFingerprint(row))
		if !ok || current.Content != p.Content {
			continue
		}
		if mobileBriefSummaryContainsRuns(current, p) && a.mobileBriefSummaryReadable(ctx, row) {
			return row.ID, true
		}
	}
	return "", false
}

type mobileBriefSummaryReplacementKey struct{}

type mobileBriefSummaryProjectionSources struct {
	refs map[string]string
	ids  map[string]bool
}

// Substitute only inside the privacy gate, leaving the model binding and wire
// citations attached to their original identities.
func (a *App) mobileBriefSummaryProjection(ctx context.Context, brief MobileBriefV1, prints map[string]string) context.Context {
	replaced := mobileBriefSummaryProjectionSources{refs: map[string]string{}, ids: map[string]bool{}}
	if a == nil || a.activity == nil || a.activity.store == nil {
		return context.WithValue(ctx, mobileBriefSummaryReplacementKey{}, replaced)
	}
	for _, ref := range brief.Evidence {
		if ref.SourceKind != mobileBriefRecapSource || strings.HasPrefix(ref.SourceID, threadOutcomeMomentPrefix) {
			continue
		}
		p, ok := mobileBriefParseSummaryFingerprint(prints[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)])
		if !ok {
			continue
		}
		id := ref.SourceID
		row, err := a.activity.store.GetActivityByIDContext(ctx, id)
		if err != nil {
			continue
		}
		if row == nil {
			var found bool
			id, found = a.mobileBriefSummaryReplacement(ctx, ref.SourceID, p)
			if !found {
				continue
			}
			replaced.refs[ref.Ref] = id
			replaced.ids[ref.SourceID] = true
			continue
		}
		current, valid := mobileBriefParseSummaryFingerprint(mobileBriefSummaryFingerprint(*row))
		if valid && current.Content == p.Content && mobileBriefSummaryContainsRuns(current, p) && a.mobileBriefSummaryReadable(ctx, *row) {
			replaced.ids[id] = true
		}
	}
	return context.WithValue(ctx, mobileBriefSummaryReplacementKey{}, replaced)
}

// Source identities are local to the seal. Later Today refreshes may add
// display evidence without adding it to the already written prose.
type mobileBriefSealSource struct {
	Ref  string `json:"ref"`
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func mobileBriefSealSources(brief MobileBriefV1) []mobileBriefSealSource {
	refs := make([]mobileBriefSealSource, 0, len(brief.Evidence))
	for _, ref := range brief.Evidence {
		refs = append(refs, mobileBriefSealSource{Ref: ref.Ref, Kind: ref.SourceKind, ID: ref.SourceID})
	}
	return refs
}

func mobileBriefSealedSources(saved mobileBriefFinishedRead, source MobileBriefV1) MobileBriefV1 {
	if saved.SourceRefs != nil {
		cited := source
		cited.Evidence = nil
		for _, ref := range saved.SourceRefs {
			cited.Evidence = append(cited.Evidence, MobileBriefRef{Ref: ref.Ref, SourceKind: ref.Kind, SourceID: ref.ID})
		}
		return cited
	}
	// Older seals retain the evidence in their saved display copy. When the
	// stored document still matches, it also supplies sources filtered from it.
	if saved.Source == mobileBriefSourceDigest(source) {
		return source
	}
	var old MobileBriefV1
	if unmarshalStoredMobileBrief(saved.Brief, &old) == nil {
		return old
	}
	return source
}

// mobileBriefDropCited removes what cites a withdrawn source (changed holds the
// evidence refs). Items, cards, paragraphs and evidence that cite only other
// sources stay. The written synthesis that depends on the removed material (the
// recap, the week, the audio) is withheld as a whole, because its words cannot be
// tied to one source.
func mobileBriefDropCited(brief MobileBriefV1, changed map[string]bool) MobileBriefV1 {
	usesChanged := func(refs []string) bool {
		for _, ref := range refs {
			if changed[ref] {
				return true
			}
		}
		return false
	}
	removed := 0
	filter := func(items []MobileBriefItem) []MobileBriefItem {
		if items == nil {
			return nil
		}
		kept := make([]MobileBriefItem, 0, len(items))
		for _, item := range items {
			if usesChanged(item.Refs) {
				removed++
				continue
			}
			kept = append(kept, item)
		}
		return kept
	}
	sections := &brief.Sections
	for _, items := range []*[]MobileBriefItem{&sections.Noticed, &sections.Decide, &sections.Review, &sections.Plan, &sections.WaitingOnYou, &sections.Commitments, &sections.WorkingToward, &sections.DayAhead, &sections.Highlights, &sections.Moved, &sections.Doing, &sections.Watching, &sections.Meetings, &sections.People, &sections.OpenLoops, &sections.Learned, &sections.Deliverables, &sections.Schedules, &brief.Upcoming} {
		*items = filter(*items)
	}
	// Written paragraphs can name a withdrawn item in their words without citing
	// it in the same paragraph, so the written synthesis is withheld as a whole
	// when any paragraph cites a withdrawn source.
	paragraphTouched := func(list []MobileBriefPara) bool {
		for _, para := range list {
			if usesChanged(para.Refs) {
				return true
			}
		}
		return false
	}
	if paragraphTouched(brief.Narrative) || paragraphTouched(brief.WeeklyRecap) {
		removed++
		brief.Narrative, brief.WeeklyRecap = nil, nil
	}
	var cards []MobileBriefCard
	for _, card := range brief.Cards {
		if usesChanged(card.Refs) {
			removed++
			continue
		}
		cards = append(cards, card)
	}
	brief.Cards = cards
	var evidence []MobileBriefRef
	for _, ref := range brief.Evidence {
		if !changed[ref.Ref] {
			evidence = append(evidence, ref)
		}
	}
	brief.Evidence = evidence
	var attendance []MobileBriefAttendance
	for _, row := range brief.MeetingAttendance {
		if !usesChanged(row.Refs) {
			attendance = append(attendance, row)
		}
	}
	brief.MeetingAttendance = attendance
	if removed == 0 {
		return brief
	}
	brief.Audio = nil
	brief.Offers, brief.YourWeek, brief.WeekQuestion, brief.DayShape = nil, nil, nil, nil
	brief.WeekLead, brief.WeekLeadFacts = "", ""
	if len(brief.Narrative) == 0 {
		brief.NarrativeStatus = "unavailable"
	}
	brief.Limits.ExcludedSensitiveItems += removed
	brief.Counts = MobileBriefCounts{OpenLoopsByKind: map[string]int{}}
	for _, item := range brief.Sections.OpenLoops {
		brief.Counts.OpenLoopsByKind[mobileBriefFirstText(item.Kind, "other")]++
	}
	return brief
}

// finishedReadTime uses the saved file's mtime for seals written before the
// explicit timestamp existed. Subsequent amendments preserve that boundary.
func (s *mobileBriefStore) finishedReadTime(id string, saved mobileBriefFinishedRead) time.Time {
	if !saved.SealedAt.IsZero() {
		return saved.SealedAt
	}
	if path, ok := s.finishedReadPath(id); ok {
		if info, err := os.Stat(path); err == nil {
			return info.ModTime()
		}
	}
	return time.Time{}
}

// mobileBriefSourceLookups counts reads of live sources made for briefs (store,
// file and chat-source lookups), for tests: a read of a sealed brief adds none.
var mobileBriefSourceLookups atomic.Uint64

// mobileBriefEditableSource reads the current source and its modification
// time. A missing or unreadable supported source fails closed. The boolean
// says whether this kind is supported, separately from whether it was found.
func (a *App) mobileBriefEditableSource(ctx context.Context, ref MobileBriefRef) (time.Time, any, bool, bool) {
	mobileBriefSourceLookups.Add(1)
	id, _, _ := strings.Cut(ref.SourceID, ":")
	switch ref.SourceKind {
	case "memory", taskContextSourceMemoryKind, "knowledge", taskContextSourceKnowledgeKind:
		if id == "" || strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
			return time.Time{}, nil, true, false
		}
		var dir string
		var err error
		switch ref.SourceKind {
		case "memory", taskContextSourceMemoryKind:
			dir, err = memoryDir()
		case "knowledge", taskContextSourceKnowledgeKind:
			dir, err = knowledgeDir()
		}
		if err != nil {
			return time.Time{}, nil, true, false
		}
		path := filepath.Join(dir, id+".md")
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return time.Time{}, nil, true, false
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return time.Time{}, nil, true, false
		}
		modified := info.ModTime()
		if ref.SourceKind == "memory" || ref.SourceKind == taskContextSourceMemoryKind {
			if updated := parseMemoryFrontmatter(string(data)).UpdatedAt; updated > 0 && time.UnixMilli(updated).After(modified) {
				modified = time.UnixMilli(updated)
			}
		}
		return modified, data, true, true
	case "principal":
		if a == nil || a.store == nil {
			return time.Time{}, nil, true, false
		}
		row, err := a.store.GetPrincipal(ctx, ref.SourceID)
		if err != nil {
			return time.Time{}, nil, true, false
		}
		return time.UnixMilli(row.UpdatedAt), row, true, true
	case "schedule":
		if a == nil || a.store == nil {
			return time.Time{}, nil, true, false
		}
		row, err := a.store.LoadCronJob(ctx, ref.SourceID)
		if err != nil || row == nil {
			return time.Time{}, nil, true, false
		}
		return time.UnixMilli(row.UpdatedAt), row, true, true
	case "deliverable":
		if a == nil {
			return time.Time{}, nil, true, false
		}
		rows, err := a.listDeliverablesContext(ctx)
		if err == nil {
			for _, row := range rows {
				if row.ID == ref.SourceID {
					return time.UnixMilli(row.UpdatedAt), row, true, true
				}
			}
		}
		return time.Time{}, nil, true, false
	case "activity", mobileBriefRecapSource:
		activityID := ref.SourceID
		if strings.HasPrefix(ref.SourceID, threadOutcomeMomentPrefix) {
			end := strings.LastIndex(ref.SourceID, ":")
			if end <= len(threadOutcomeMomentPrefix) {
				return time.Time{}, nil, true, false
			}
			activityID = strings.TrimPrefix(ref.SourceID[:end], threadOutcomeMomentPrefix)
		}
		if a == nil || a.activity == nil || a.activity.store == nil {
			return time.Time{}, nil, true, false
		}
		row, err := a.activity.store.GetActivityByIDContext(ctx, activityID)
		if err != nil || row == nil {
			return time.Time{}, nil, true, false
		}
		return time.UnixMilli(row.UpdatedAt), row, true, true
	case "purpose":
		if a == nil {
			return time.Time{}, nil, true, false
		}
		store, err := a.purposeProductStore()
		if err != nil {
			return time.Time{}, nil, true, false
		}
		row, err := store.GetDetail(ref.SourceID)
		if err != nil {
			return time.Time{}, nil, true, false
		}
		return time.UnixMilli(row.Current.CreatedAt), row, true, true
	case mobileBriefCalendarSource:
		if a == nil || a.store == nil {
			return time.Time{}, nil, true, false
		}
		row, found, err := a.store.GetCalendarEvent(ctx, ref.SourceID)
		if err != nil || !found || row.DeletedAt != 0 {
			return time.Time{}, nil, true, false
		}
		return time.UnixMilli(row.UpdatedAt), row, true, true
	case "history", "handoff":
		if a == nil || a.handoffStore == nil {
			return time.Time{}, nil, true, false
		}
		row, err := a.handoffStore.Get(ref.SourceID)
		if err != nil {
			return time.Time{}, nil, true, false
		}
		modified := time.UnixMilli(row.UpdatedAt)
		for _, ext := range []string{".json", ".md"} {
			if info, err := os.Stat(filepath.Join(a.handoffStore.Dir(), ref.SourceID+ext)); err == nil && info.ModTime().After(modified) {
				modified = info.ModTime()
				break
			}
		}
		return modified, row, true, true
	default:
		return time.Time{}, nil, false, false
	}
}

// v2 fingerprints omit metadata that does not contribute to the brief. Old
// unprefixed fingerprints hash the whole record and remain comparable as-is.
func mobileBriefContentFingerprint(ref MobileBriefRef, value any) string {
	switch ref.SourceKind {
	case mobileBriefRecapSource:
		if row, ok := value.(*ActivityRow); ok && !strings.HasPrefix(ref.SourceID, threadOutcomeMomentPrefix) {
			return mobileBriefSummaryFingerprint(*row)
		}
		return "v2:" + mobileBriefModelDigest(value)
	case "memory", taskContextSourceMemoryKind:
		data, ok := value.([]byte)
		if !ok {
			return ""
		}
		content := string(data)
		return "v2:" + mobileBriefModelDigest(struct{ Title, Body string }{extractTitle(content), memoryConfirmationBody(content)})
	case "principal":
		row, ok := value.(storage.PrincipalRow)
		if !ok {
			return ""
		}
		return "v2:" + mobileBriefModelDigest(struct {
			DisplayName, Relationship, Notes string
			Aliases                          []string
		}{row.DisplayName, row.Relationship, row.Notes, row.Aliases})
	case mobileBriefCalendarSource:
		row, ok := value.(storage.CalendarEventRow)
		if !ok {
			return ""
		}
		type attendee struct {
			Name, Email string
			IsMe        bool
		}
		var attendees []attendee
		for _, person := range row.Attendees {
			attendees = append(attendees, attendee{person.Name, person.Email, person.IsMe})
		}
		return "v2:" + mobileBriefModelDigest(struct {
			Title                                                                                                                 string
			StartAt, EndAt                                                                                                        int64
			AllDay                                                                                                                bool
			TimeZone, Location, ConferenceURL, ConferenceHost, URL, Availability, Status, MyStatus, OrganizerEmail, OrganizerName string
			Attendees                                                                                                             []attendee
		}{row.Title, row.StartAt, row.EndAt, row.AllDay, row.TimeZone, row.Location, row.ConferenceURL, row.ConferenceHost, row.URL, row.Availability, row.Status, row.MyStatus, row.OrganizerEmail, row.OrganizerName, attendees})
	default:
		return "v2:" + mobileBriefModelDigest(value)
	}
}

// Store the source content at the original seal boundary.
func (a *App) mobileBriefSourceFingerprints(ctx context.Context, source MobileBriefV1) map[string]string {
	prints := make(map[string]string, len(source.Evidence))
	var sources todayChatSources
	var collected bool
	for _, ref := range source.Evidence {
		key := mobileBriefSourceKey(ref.SourceKind, ref.SourceID)
		_, value, supported, found := a.mobileBriefEditableSource(ctx, ref)
		if supported && found {
			prints[key] = mobileBriefContentFingerprint(ref, value)
			continue
		}
		if !supported {
			if !collected {
				sources = a.collectTodayChatSources(ctx, mobileBriefAsOfTime(source))
				collected = true
			}
			if digest, ok := a.mobileBriefModelSourceDigest(ctx, ref, source, sources); ok {
				prints[key] = digest
			}
		}
	}
	return prints
}
