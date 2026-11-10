package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wails-gui/storage"
)

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

func mobileBriefChangedRefs(cited, source, shown MobileBriefV1, changed map[string]bool) map[string]bool {
	keys := map[string]bool{}
	for _, ref := range cited.Evidence {
		if changed[ref.Ref] {
			keys[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)] = true
		}
	}
	out := map[string]bool{}
	for _, brief := range []MobileBriefV1{source, shown} {
		for _, ref := range brief.Evidence {
			if keys[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)] {
				out[ref.Ref] = true
			}
		}
	}
	return out
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

// mobileBriefEditableSource reads the current source and its modification
// time. A missing or unreadable supported source fails closed. The boolean
// says whether this kind is supported, separately from whether it was found.
func (a *App) mobileBriefEditableSource(ctx context.Context, ref MobileBriefRef) (time.Time, any, bool, bool) {
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

func (a *App) mobileBriefEditedSources(source MobileBriefV1, sealedAt time.Time, fingerprints map[string]string) (string, map[string]bool) {
	var kind string
	changed := map[string]bool{}
	var sources todayChatSources
	var collected bool
	for _, ref := range source.Evidence {
		modified, value, supported, found := a.mobileBriefEditableSource(context.Background(), ref)
		sealedPrint := fingerprints[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)]
		edited := false
		if supported {
			switch true {
			case !found:
				edited = true
			case strings.HasPrefix(sealedPrint, "v2:"):
				edited = sealedPrint != mobileBriefContentFingerprint(ref, value)
			case sealedPrint != "":
				edited = sealedPrint != mobileBriefModelDigest(value)
			default:
				edited = sealedAt.IsZero() || modified.After(sealedAt)
			}
		} else if sealedPrint != "" {
			if !collected {
				sources = a.collectTodayChatSources(context.Background(), mobileBriefAsOfTime(source))
				collected = true
			}
			current, ok := a.mobileBriefModelSourceDigest(context.Background(), ref, source, sources)
			// Browser evidence retains its own privacy-checked provenance when
			// routine retention removes the underlying visit rows.
			edited = (!ok && ref.SourceKind != mobileBriefWebSource) || (ok && current != sealedPrint)
		}
		if edited {
			if kind == "" {
				kind = ref.SourceKind
			}
			// A temporarily inaccessible handoff directory cannot establish
			// that the item itself was removed. Keep its independently shown
			// card, but withhold all prose until the store can be read.
			if (ref.SourceKind == "history" || ref.SourceKind == "handoff") && a != nil && a.handoffStore != nil {
				if info, err := os.Stat(a.handoffStore.Dir()); err != nil || !info.IsDir() {
					continue
				}
			}
			changed[ref.Ref] = true
		}
	}
	return kind, changed
}

func (a *App) mobileBriefEditedSource(source MobileBriefV1, sealedAt time.Time) (string, bool) {
	kind, _ := a.mobileBriefEditedSources(source, sealedAt, nil)
	return kind, kind != ""
}

// An edit may arrive with a separate privacy withdrawal. In that case the old
// copy cannot provide the unaffected sections until the current gates run.
func (s *mobileBriefService) withholdEditedRead(ctx context.Context, saved mobileBriefFinishedRead, brief MobileBriefV1, kind string, changed map[string]bool) MobileBriefV1 {
	print := s.app.briefPrivacyPrint()
	if saved.Print != print || len(changed) > 0 || (kind != "history" && kind != "handoff") {
		projected, withheld := s.app.mobileBriefForProjectionWithholding(ctx, brief, true)
		if withheld.Transient() || print != s.app.briefPrivacyPrint() {
			return mobileBriefPaused(brief)
		}
		brief = projected
	}
	brief = mobileBriefWithholdEdited(brief, changed)
	if print != s.app.briefPrivacyPrint() {
		return mobileBriefPaused(brief)
	}
	return brief
}

// Withhold the written synthesis and items tied to an edited source; retain
// independently readable sections just as the withdrawal filter does.
func mobileBriefWithholdEdited(brief MobileBriefV1, changed map[string]bool) MobileBriefV1 {
	usesChanged := func(refs []string) bool {
		for _, ref := range refs {
			if changed[ref] {
				return true
			}
		}
		return false
	}
	filter := func(items []MobileBriefItem) []MobileBriefItem {
		if items == nil {
			return nil
		}
		kept := make([]MobileBriefItem, 0, len(items))
		for _, item := range items {
			if !usesChanged(item.Refs) {
				kept = append(kept, item)
			}
		}
		return kept
	}
	sections := &brief.Sections
	for _, items := range []*[]MobileBriefItem{&sections.Noticed, &sections.Decide, &sections.Review, &sections.Plan, &sections.WaitingOnYou, &sections.Commitments, &sections.WorkingToward, &sections.DayAhead, &sections.Highlights, &sections.Moved, &sections.Doing, &sections.Watching, &sections.Meetings, &sections.People, &sections.OpenLoops, &sections.Learned, &sections.Deliverables, &sections.Schedules, &brief.Upcoming} {
		*items = filter(*items)
	}
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
	brief.Narrative, brief.WeeklyRecap, brief.Audio, brief.Cards, brief.Offers = nil, nil, nil, nil, nil
	brief.YourWeek, brief.WeekQuestion, brief.DayShape = nil, nil, nil
	brief.WeekLead, brief.WeekLeadFacts = "", ""
	brief.NarrativeStatus = "unavailable"
	brief.Counts = MobileBriefCounts{OpenLoopsByKind: map[string]int{}}
	for _, item := range sections.OpenLoops {
		brief.Counts.OpenLoopsByKind[mobileBriefFirstText(item.Kind, "other")]++
	}
	return brief
}

// v2 fingerprints omit metadata that does not contribute to the brief. Old
// unprefixed fingerprints hash the whole record and remain comparable as-is.
func mobileBriefContentFingerprint(ref MobileBriefRef, value any) string {
	switch ref.SourceKind {
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
