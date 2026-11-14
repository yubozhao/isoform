package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	briefcore "wails-gui/internal/brief"
	"wails-gui/internal/handoff"
	"wails-gui/internal/interests"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/recentfiles"
	"wails-gui/storage"
)

// Each stored brief reference is a membership claim, not a privacy grant.
// Resolve its current source without starting a service or replaying prose.
// Each item gets its own bounded traversal; its cache also stops source cycles.
// Completed person and calendar-day reads, including principal denials, are
// shared only within this request. Traversal failures never become denials.
type mcpBriefReader struct {
	p                *mcpReadPrivacy
	cache            map[string]mcpBriefCachedRead
	completed        map[string]mcpReadItem
	deniedPrincipals map[string]bool
	checked          map[string]bool
	limited          bool
	noticed          *mobileBriefNoticedLookup
}

type mcpBriefCachedRead struct {
	item  mcpReadItem
	depth int
}

func (p *mcpReadPrivacy) briefToday(result *mcpReadResult) error {
	dir, err := mobileBriefStateDir()
	if err != nil {
		return err
	}
	brief, ok := p.mcpBriefLatest(dir)
	if !ok || brief.LocalDate != time.Now().Format("2006-01-02") {
		return nil
	}
	before, err := p.mcpBriefSupplementalPolicy()
	if err != nil {
		return err
	}
	refs := map[string]MobileBriefRef{}
	for _, ref := range brief.Evidence {
		refs[ref.Ref] = ref
	}
	completed := map[string]mcpReadItem{}
	deniedPrincipals := map[string]bool{}
	noticed := newMobileBriefNoticedLookup(p.app)
	// No narrative or old section detail: those can combine sources absent from
	// their references. A meeting label is the explicitly requested exception,
	// after every contributing source has been checked again.
	for _, items := range [][]MobileBriefItem{brief.Sections.Moved, brief.Sections.Doing, brief.Sections.Watching, brief.Sections.Meetings, brief.Sections.People, brief.Sections.OpenLoops, brief.Sections.Learned, brief.Sections.Deliverables, brief.Sections.Schedules, brief.Sections.Noticed} {
		for _, item := range items {
			if err := p.ctx.Err(); err != nil {
				return err
			}
			reader := &mcpBriefReader{p: p, cache: map[string]mcpBriefCachedRead{}, completed: completed, deniedPrincipals: deniedPrincipals, noticed: noticed, checked: map[string]bool{}}
			result.Total++
			if len(item.Refs) > mobileBriefEvidenceLimit {
				result.Truncated = true
				result.Omitted++
				continue
			}
			allowed := len(item.Refs) > 0 && len(item.Refs) <= mobileBriefEvidenceLimit
			var current mcpReadItem
			meeting := false
			for _, id := range item.Refs {
				ref, exists := refs[id]
				source, safe := reader.reference(ref.SourceKind, ref.SourceID, 0)
				if !exists || !safe {
					allowed = false
					break
				}
				if current.Source == "" {
					current = source
				}
				meeting = meeting || ref.SourceKind == "meeting"
			}
			if reader.limited {
				result.Truncated = true
				result.Omitted++
				continue
			}
			if !allowed {
				result.Withheld++
				continue
			}
			if meeting && strings.TrimSpace(item.Title) != "" {
				current.Title = mcpReadTextPtr(item.Title)
			}
			if len(result.Items) >= mcpReadMaxResults {
				result.Truncated = true
				result.Omitted++
				continue
			}
			result.Items = append(result.Items, current)
		}
	}
	if len(brief.Limits.TruncatedSections) > 0 {
		result.Truncated = true
	}
	after, err := p.mcpBriefSupplementalPolicy()
	if err != nil {
		return err
	}
	if before != after {
		return errors.New("Isoform privacy settings changed; retry the read")
	}
	return nil
}

// mcpBriefLatest is the latest brief through the brief reader
// (internal/brief): the sealed copy the computer and the phone show when it
// may be shown as it is, else the stored brief. Either way every item below is
// checked again against its current source, so a copy that needs an amend
// first (its privacy print moved) reads no less safely than before.
func (p *mcpReadPrivacy) mcpBriefLatest(stateDir string) (MobileBriefV1, bool) {
	dir := filepath.Join(stateDir, mobileBriefDirName)
	options := briefcore.ReaderOptions{Dir: dir, Day: func() string { return time.Now().Format("2006-01-02") }}
	if p.app != nil {
		options.Print = p.app.briefPrivacyPrint
	}
	reader := briefcore.NewReader(options)
	latest, ok := reader.LatestBuilt()
	if !ok {
		return MobileBriefV1{}, false
	}
	if sealed, shown := reader.Get(latest.BriefID); shown {
		var brief MobileBriefV1
		if unmarshalStoredMobileBrief(sealed.Raw, &brief) == nil && brief.BriefID == latest.BriefID {
			return brief, true
		}
	}
	return newMobileBriefStore(stateDir).load(latest.BriefID)
}

func (p *mcpReadPrivacy) briefReferenceItem(ref MobileBriefRef) (mcpReadItem, bool) {
	r := &mcpBriefReader{p: p, cache: map[string]mcpBriefCachedRead{}, checked: map[string]bool{}}
	return r.reference(ref.SourceKind, ref.SourceID, 0)
}

func (r *mcpBriefReader) reference(kind, id string, depth int) (mcpReadItem, bool) {
	if id == "" || r.p.ctx.Err() != nil {
		return mcpReadItem{}, false
	}
	if depth > 6 {
		r.limited = true
		return mcpReadItem{}, false
	}
	key := kind + "\x00" + id
	if r.checked[key] {
		cached, ok := r.cache[key]
		if !ok {
			return mcpReadItem{}, false // failed read or active recursion cycle
		}
		if depth <= cached.depth {
			return cached.item, true
		}
		// A deeper path has less room for this source's descendants. Remove
		// the prior success while rechecking so recursion still rejects cycles.
		// Each unique source is resolved at most once at each allowed depth.
		delete(r.cache, key)
	} else if len(r.checked) >= 256 {
		r.limited = true
		return mcpReadItem{}, false
	}
	r.checked[key] = true
	if kind == "principal" && r.deniedPrincipals[id] {
		return mcpReadItem{}, false
	}
	if item, ok := r.completed[key]; ok {
		r.cache[key] = mcpBriefCachedRead{item: item, depth: depth}
		return item, true
	}
	item, ok := r.resolve(kind, id, depth)
	if ok {
		r.cache[key] = mcpBriefCachedRead{item: item, depth: depth}
		// Composite sources must traverse again at their new depth. Reusing a
		// shallow composite's success could bypass a later item's depth limit.
		// A partial calendar aggregate is also not a completed source read.
		switch kind {
		case "principal", mobileBriefDayShapeSource:
			if !r.limited && r.completed != nil {
				r.completed[key] = item
			}
		}
	}
	return item, ok
}

func (r *mcpBriefReader) sessionAllowed(id string) bool {
	allowed := r.p.sessionAllowed(id)
	if !allowed && r.p.sessionLimited(id) {
		r.limited = true
	}
	return allowed
}

func (r *mcpBriefReader) exceedsLimit(count, limit int) bool {
	if count > limit {
		r.limited = true
		return true
	}
	return false
}

func (r *mcpBriefReader) resolve(kind, id string, depth int) (mcpReadItem, bool) {
	p := r.p
	switch kind {
	case mobileBriefNoticedSource:
		if r.noticed == nil {
			r.noticed = newMobileBriefNoticedLookup(p.app)
		}
		if item, ok := r.noticed.suggestion(id); ok {
			return mcpReadItem{Source: kind, ID: mcpReadText(id), At: item.MomentStartMs, Title: mcpReadTextPtr(item.Text)}, true
		}
	case taskContextSourceActivitySnapshotKind:
		split := strings.LastIndex(id, ":")
		if split < 1 || p.app.activity == nil || p.app.activity.store == nil {
			break
		}
		snapshotID, err := strconv.ParseInt(id[split+1:], 10, 64)
		if err != nil {
			break
		}
		var sessionID string
		if p.app.activity.store.db.QueryRowContext(p.ctx, `SELECT session_id FROM activity_snapshots WHERE id=? AND session_id=?`, snapshotID, id[:split]).Scan(&sessionID) == nil {
			return r.reference("activity_session", sessionID, depth+1)
		}
	case "activity_session", "session", "meeting":
		if !r.sessionAllowed(id) {
			break
		}
		rows, err := p.app.activity.store.GetSessionsByIDs([]string{id})
		if err == nil && rows[id] != nil {
			row := rows[id]
			return mcpReadItem{Source: "activity_session", ID: mcpReadText(id), At: row.StartTime, EndAt: row.EndTime, Title: mcpReadTextPtr(row.MeetingApp)}, true
		}
	case "memory", taskContextSourceMemoryKind:
		entries, err := p.memoryEntries()
		if err != nil {
			break
		}
		for _, entry := range entries {
			if entry.ID != id {
				continue
			}
			if !p.memoryAllowed(entry) {
				r.limited = r.limited || p.memoryLimited(id)
				return mcpReadItem{}, false
			}
			return mcpReadItem{Source: "memory", ID: mcpReadText(id), At: max(entry.UpdatedAt, entry.ModTime), Title: mcpReadTextPtr(entry.Title), Snippet: mcpReadTextPtr(stripMemoryFrontmatter(entry.Content))}, true
		}
		r.limited = r.limited || p.memoryScanTruncated
	case "principal":
		person, err := p.app.store.GetPrincipal(p.ctx, id)
		if err == nil {
			allowed, readErr := p.personAllowedRead(person)
			if allowed {
				return mcpReadItem{Source: "person", ID: mcpReadText(id), At: person.UpdatedAt, Title: mcpReadTextPtr(person.DisplayName)}, true
			}
			r.limited = r.limited || p.personLimited(id)
			// Repeated brief items need not page the same denied person's
			// sightings again. Storage failures, budgets and canceled reads
			// cannot establish a stable denial for the shared request memo.
			if readErr == nil && !r.limited && p.ctx.Err() == nil && r.deniedPrincipals != nil {
				r.deniedPrincipals[id] = true
			}
		}
	case mobileBriefCalendarSource:
		event, ok, err := p.app.store.GetCalendarEvent(p.ctx, id)
		if err == nil && ok {
			if p.calendarAllowed(event) {
				return mcpReadItem{Source: "calendar_event", ID: mcpReadText(id), At: event.StartAt, EndAt: event.EndAt, Title: mcpReadTextPtr(event.Title)}, true
			}
			r.limited = r.limited || p.sessionLimited(event.SessionID)
		}
	case mobileBriefDayShapeSource:
		return r.calendarDay(id)
	case "history":
		return r.history(id)
	case "purpose", taskContextSourcePurposeOpenLoopKind:
		return r.purpose(id, depth)
	case "learning":
		return r.learning(id, depth)
	case "deliverable":
		return r.deliverable(id)
	case "schedule_run":
		return r.scheduleRun(id)
	case mobileBriefFileSource:
		return r.file(id)
	case mobileBriefRecapSource, "activity":
		return r.recap(id, depth)
	case mobileBriefNewsSource:
		return r.news(id)
	case mobileBriefFindSource:
		return r.find(id, depth)
	}
	return mcpReadItem{}, false
}

func (r *mcpBriefReader) ownChat(id string) bool {
	if id == "" {
		return false
	}
	s, err := r.p.app.store.LoadSession(r.p.ctx, id)
	if err != nil || s.AutomationResult || s.MeetingID != "" || (s.Kind != storage.SessionKindMain && s.Kind != storage.SessionKindSide) {
		return false
	}
	hidden, err := r.p.app.store.SessionHidden(r.p.ctx, id)
	return err == nil && !hidden
}

func (r *mcpBriefReader) history(id string) (mcpReadItem, bool) {
	p := r.p
	if p.app.handoffStore == nil || id == "." || id == ".." || strings.ContainsAny(id, "/\\") {
		return mcpReadItem{}, false
	}
	e, err := p.app.handoffStore.Get(id)
	if err != nil || !p.appAllowed(e.AppName) || !p.timeAllowed(e.CreatedAt, e.UpdatedAt) {
		return mcpReadItem{}, false
	}
	allowed := false
	switch e.Source {
	case handoff.SourceTriage, handoff.SourceFallback:
		allowed = r.sessionAllowed(e.SourceID)
	case handoff.SourceMobile, handoff.SourceDesktop, handoff.SourceOnboarding:
		allowed = true // explicit requests, not captured connector messages
	}
	if !allowed {
		return mcpReadItem{}, false
	}
	return mcpReadItem{Source: "history", ID: mcpReadText(id), At: e.UpdatedAt, Title: mcpReadTextPtr(e.Title), Status: mcpReadTextPtr(string(e.Status))}, true
}

func (r *mcpBriefReader) purpose(id string, depth int) (mcpReadItem, bool) {
	store, err := r.p.app.purposeProductStore()
	if err != nil {
		return mcpReadItem{}, false
	}
	detail, err := store.GetDetail(id)
	if err != nil || !detail.Eligible || len(detail.Evidence) == 0 || r.exceedsLimit(len(detail.Evidence), 100) {
		return mcpReadItem{}, false
	}
	for _, e := range detail.Evidence {
		if e.PrivacyDecision != TaskContextPrivacyInclude && e.PrivacyDecision != TaskContextPrivacyRedact {
			return mcpReadItem{}, false
		}

		if _, safe := r.reference(e.SourceKind, e.SourceID, depth+1); !safe {
			return mcpReadItem{}, false
		}
	}
	current := detail.Current
	return mcpReadItem{Source: "purpose", ID: mcpReadText(id), At: current.OccurredAt, Title: mcpReadTextPtr(current.Title), Snippet: mcpReadTextPtr(current.DesiredOutcome), Status: mcpReadTextPtr(current.LifecycleState)}, true
}

func (r *mcpBriefReader) learning(id string, depth int) (mcpReadItem, bool) {
	p := r.p
	if p.app.activity == nil || p.app.activity.store == nil {
		return mcpReadItem{}, false
	}
	s := p.app.activity.store
	// Explicit corrections are the user's statement. Never resurrect the older
	// candidate after it has been corrected.
	var correction string
	var correctedAt int64
	err := s.db.QueryRowContext(p.ctx, `SELECT correction,created_at FROM agency_learning_corrections_v1 WHERE learning_revision_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, id).Scan(&correction, &correctedAt)
	if err == nil {
		return mcpReadItem{Source: "learning", ID: mcpReadText(id), At: correctedAt, Title: mcpReadTextPtr(correction), Status: mcpReadTextPtr("user_corrected")}, true
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return mcpReadItem{}, false
	}
	var raw string
	var at int64
	err = s.db.QueryRowContext(p.ctx, `SELECT candidate_json,created_at FROM shadow_task_learning_revisions WHERE revision_id=? AND length(candidate_json)<=262144`, id).Scan(&raw, &at)
	var candidate ShadowTaskLearningCandidate
	if err != nil || json.Unmarshal([]byte(raw), &candidate) != nil || candidate.RevisionID != id || !learnedPatternCandidateUseful(candidate) || len(candidate.Evidence) == 0 || r.exceedsLimit(len(candidate.Evidence), 100) {
		return mcpReadItem{}, false
	}
	for _, ev := range candidate.Evidence {
		detail, err := s.GetTaskEpisodeDetail(ev.TaskEpisodeID)
		if err != nil || ev.ContextHash != detail.Episode.FrozenContext.SHA256 || len(detail.Evidence) == 0 || r.exceedsLimit(len(detail.Evidence), 100) || len(detail.Episode.FrozenContext.Manifest) == 0 || r.exceedsLimit(len(detail.Episode.FrozenContext.Manifest), 100) || detail.Episode.FrozenContext.WorkProfileSnapshotID != "" {
			return mcpReadItem{}, false
		}
		for _, source := range detail.Evidence {
			// Onset activity_event IDs are indices, not database IDs. Resolve the
			// recorded event time to its real capture session instead of guessing.
			if source.SourceKind == "activity_event" {
				if !r.eventTimeAllowed(source.ObservedAt, depth) {
					return mcpReadItem{}, false
				}
			} else if _, ok := r.reference(source.SourceKind, source.SourceID, depth+1); !ok {
				return mcpReadItem{}, false
			}
		}
		for _, source := range detail.Episode.FrozenContext.Manifest {
			if !source.Included {
				continue
			}
			if source.PrivacyDecision != TaskContextPrivacyInclude && source.PrivacyDecision != TaskContextPrivacyRedact {
				return mcpReadItem{}, false
			}
			if _, ok := r.reference(source.SourceKind, source.SourceID, depth+1); !ok {
				return mcpReadItem{}, false
			}
		}
	}
	return mcpReadItem{Source: "learning", ID: mcpReadText(id), At: at, Title: mcpReadTextPtr(candidate.Statement), Status: mcpReadTextPtr("supported")}, true
}

func (r *mcpBriefReader) eventTimeAllowed(at int64, depth int) bool {
	p := r.p
	rows, err := p.app.activity.store.db.QueryContext(p.ctx, `SELECT DISTINCT session_id FROM activity_events WHERE timestamp=? LIMIT 101`, at)
	if err != nil {
		return false
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return false
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) == 0 || r.exceedsLimit(len(ids), 100) {
		return false
	}
	for _, id := range ids {
		if _, ok := r.reference("activity_session", id, depth+1); !ok {
			return false
		}
	}
	return true
}

func (r *mcpBriefReader) scheduleRun(id string) (mcpReadItem, bool) {
	p := r.p
	run, err := p.app.store.ReadMCPRun(p.ctx, id)
	if err != nil || !p.timeAllowed(run.StartedAt, run.FinishedAt) {
		return mcpReadItem{}, false
	}
	title := "Scheduled run"
	if job, err := p.app.store.LoadCronJob(p.ctx, run.JobID); err == nil && job.SourcePlatform == "chat" && r.ownChat(job.SourceSessionID) {
		title = job.Name
	}
	return mcpReadItem{Source: "schedule_run", ID: mcpReadText(id), At: run.StartedAt, EndAt: run.FinishedAt, Title: mcpReadTextPtr(title), Status: mcpReadTextPtr(run.Status)}, true
}

func (r *mcpBriefReader) deliverable(id string) (mcpReadItem, bool) {
	source, sourceID, ok := strings.Cut(id, ":")
	if !ok {
		return mcpReadItem{}, false
	}
	switch source {
	case deliverableSourceChat:
		row, err := r.p.app.store.GetChatDeliverable(r.p.ctx, sourceID)
		if err == nil && row != nil && r.ownChat(row.SessionID) && r.p.timeAllowed(row.CreatedAt, row.UpdatedAt) {
			// Files, summaries and paths can contain captured context. Only the
			// artifact's label, owned chat provenance and status are disclosed.
			return mcpReadItem{Source: "deliverable", ID: mcpReadText(id), At: row.UpdatedAt, Title: mcpReadTextPtr(row.Title), Status: mcpReadTextPtr("completed")}, true
		}
	case deliverableSourceHandoff:
		if item, ok := r.history(sourceID); ok {
			item.Source = "deliverable"
			item.ID = mcpReadText(id)
			return item, true
		}
	case deliverableSourceAutomation:
		jobID, runID, ok := strings.Cut(sourceID, ":")
		if !ok {
			break
		}
		run, err := r.p.app.store.ReadMCPRun(r.p.ctx, runID)
		if err == nil && run.JobID == jobID {
			if item, ok := r.scheduleRun(runID); ok {
				item.Source = "deliverable"
				item.ID = mcpReadText(id)
				item.Title = mcpReadTextPtr("Automation deliverable")
				return item, true
			}
		}
	}
	return mcpReadItem{}, false
}

func (r *mcpBriefReader) file(id string) (mcpReadItem, bool) {
	p := r.p
	if p.app.activity == nil || p.app.activity.store == nil {
		return mcpReadItem{}, false
	}
	enabled, err := p.app.store.GetUIState(p.ctx, permissionUIStateKey(filesPermissionKey))
	if err != nil || enabled != "true" {
		return mcpReadItem{}, false
	}
	raw, err := p.app.store.GetUIState(p.ctx, filesPrivateFoldersKey)
	if err != nil {
		return mcpReadItem{}, false
	}
	var folders []string
	if raw != "" && json.Unmarshal([]byte(raw), &folders) != nil {
		return mcpReadItem{}, false
	}
	var row FileActivityRow
	err = p.app.activity.store.db.QueryRowContext(p.ctx, `SELECT id,name,dir_hashes,active_at,action,app_bundle_id,app_name FROM file_activity WHERE id=?`, strings.TrimPrefix(id, "file:")).Scan(&row.ID, &row.Name, &row.DirHashes, &row.ActiveAt, &row.Action, &row.AppBundleID, &row.AppName)
	if err != nil || row.AppBundleID == "" || !p.appAllowed(row.AppBundleID, row.AppName) || !p.timeAllowed(row.ActiveAt, row.ActiveAt) || activitymon.IsBrowser(row.AppBundleID) {
		return mcpReadItem{}, false
	}
	// Spotlight cannot establish which browser window opened a file.
	protected, err := p.app.activity.store.ProtectedSessionAt(row.ActiveAt)
	if err != nil || protected {
		return mcpReadItem{}, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return mcpReadItem{}, false
	}
	hidden := map[string]bool{}
	for _, folder := range folders {
		hidden[recentfiles.FolderHash(recentfiles.Canonical(folder, home))] = true
	}
	if row.DirHashes == "" || fileUnderHidden(row.DirHashes, hidden) {
		return mcpReadItem{}, false
	}
	return mcpReadItem{Source: "file", ID: mcpReadText(id), At: row.ActiveAt, Title: mcpReadTextPtr(row.Name), Status: mcpReadTextPtr(row.Action)}, true
}

func (r *mcpBriefReader) recap(id string, depth int) (mcpReadItem, bool) {
	p := r.p
	if p.app.activity == nil || p.app.activity.store == nil {
		return mcpReadItem{}, false
	}
	row, err := p.app.activity.store.GetActivityByID(id)
	if err != nil || row == nil || !p.timeAllowed(row.StartMs, row.EndMs) || activitymon.IsPrivateBrowsing(row.Title) {
		return mcpReadItem{}, false
	}
	if row.ContinuesActivityID != "" {
		if _, ok := r.reference(mobileBriefRecapSource, row.ContinuesActivityID, depth+1); !ok {
			return mcpReadItem{}, false
		}
	}
	apps, _ := topicAppTime(row.ActiveMsByApp)
	if len(apps) == 0 || !p.appAllowed(apps...) || topicAnyPeopleApp(apps) {
		return mcpReadItem{}, false
	}
	var runIDs []string
	if json.Unmarshal([]byte(row.RunIDs), &runIDs) != nil || len(runIDs) == 0 || r.exceedsLimit(len(runIDs), 100) {
		return mcpReadItem{}, false
	}
	for _, runID := range runIDs {
		run, err := p.app.activity.store.GetTimelineRunByID(runID)
		if err != nil || run == nil || len(run.SessionIDs) == 0 || r.exceedsLimit(len(run.SessionIDs), 100) || !p.appAllowed(run.AppKey, run.AppName) {
			return mcpReadItem{}, false
		}
		for _, sessionID := range run.SessionIDs {
			if _, ok := r.reference("activity_session", sessionID, depth+1); !ok {
				return mcpReadItem{}, false
			}
		}
	}
	return mcpReadItem{Source: "summary", ID: mcpReadText(id), At: row.StartMs, EndAt: row.EndMs, Title: mcpReadTextPtr(row.Title), Snippet: mcpReadTextPtr(row.LatestChange)}, true
}

func (r *mcpBriefReader) calendarDay(id string) (mcpReadItem, bool) {
	day, err := time.ParseInLocation("2006-01-02", id, time.Local)
	if err != nil || !r.p.calendar {
		return mcpReadItem{}, false
	}
	events, err := r.p.app.store.ListCalendarEvents(r.p.ctx, day.UnixMilli(), day.AddDate(0, 0, 1).UnixMilli(), false)
	if err != nil {
		return mcpReadItem{}, false
	}
	// Count only current readable events, never reuse the old aggregate (which
	// could reveal that private or subsequently deleted meetings existed).
	count := 0
	for _, e := range events {
		if r.p.calendarAllowed(e) {
			count++
		} else if r.p.sessionLimited(e.SessionID) {
			r.limited = true
		}
	}
	if count == 0 {
		return mcpReadItem{}, false
	}
	return mcpReadItem{Source: "calendar_day", ID: mcpReadText(id), At: day.UnixMilli(), Title: mcpReadTextPtr("Calendar day"), Snippet: mcpReadTextPtr(fmt.Sprintf("%d readable calendar events", count))}, true
}

func (r *mcpBriefReader) news(id string) (mcpReadItem, bool) {
	split := strings.LastIndex(id, ":")
	if split < 0 {
		return mcpReadItem{}, false
	}
	index, err := strconv.Atoi(id[split+1:])
	if err != nil || index < 0 {
		return mcpReadItem{}, false
	}
	run, err := r.p.app.store.ReadMCPRun(r.p.ctx, id[:split])
	if err != nil || run.Status != "ok" || !r.p.timeAllowed(run.StartedAt, run.FinishedAt) || time.Since(time.UnixMilli(run.FinishedAt)) > mobileBriefNewsFresh {
		return mcpReadItem{}, false
	}
	job, err := r.p.app.store.LoadCronJob(r.p.ctx, run.JobID)
	if err != nil || !job.Enabled || job.Name != todayNewsJobName || job.SurfaceKind != todayNewsSurfaceKind {
		return mcpReadItem{}, false
	}
	_, meta, hasMeta := splitResearchMeta(run.ResultSummary)
	// Older personalized runs have no exact source provenance. Do not infer
	// permission from the fact that a historical topic label left the Mac.
	if !hasMeta || meta.Personal != researchPersonalNo {
		return mcpReadItem{}, false
	}
	items, err := todayNewsItemsFromRun(run.ResultSummary)
	if err != nil || index >= len(items) {
		return mcpReadItem{}, false
	}
	item := items[index]
	return mcpReadItem{Source: "news", ID: mcpReadText(id), At: run.FinishedAt, Title: mcpReadTextPtr(item.Title), Snippet: mcpReadTextPtr(item.Source + ": " + item.Summary), URL: mcpReadBriefURL(item.URL), Status: mcpReadTextPtr("news")}, true
}

// Read bounded JSON directly; initializing topics services can start routines.
func mcpReadBriefJSON(path string, target any) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	return err == nil && len(data) <= 2<<20 && json.Unmarshal(data, target) == nil
}

func (r *mcpBriefReader) find(id string, depth int) (mcpReadItem, bool) {
	dir, err := topicsStateDir()
	if err != nil {
		return mcpReadItem{}, false
	}
	var state topicsStateFile
	if !mcpReadBriefJSON(filepath.Join(dir, topicsStateFileName), &state) || !state.enabled() {
		return mcpReadItem{}, false
	}
	for _, item := range topicFindsFresh(state, time.Now()) {
		if item.ID != id || state.Overrides[item.TopicID].Removed {
			continue
		}
		// Every sent topic was available to the search, so validating only the
		// topic selected by a returned item would omit contributing context.
		if len(state.Finds.Sent) == 0 || r.exceedsLimit(len(state.Finds.Sent), topicFindsTopics) {
			return mcpReadItem{}, false
		}
		matched := false
		for _, sent := range state.Finds.Sent {
			if state.Overrides[sent.ID].Removed || !r.interestAllowed(dir, sent, depth) {
				return mcpReadItem{}, false
			}
			matched = matched || sent.ID == item.TopicID
		}
		if !matched {
			return mcpReadItem{}, false
		}
		// The code stores only links observed in the search; recheck the retained
		// run record too so a model-authored URL never becomes provenance.
		run, err := r.p.app.store.ReadMCPRun(r.p.ctx, state.Finds.RunID)
		if err != nil || run.Status != "ok" || !r.p.timeAllowed(run.StartedAt, run.FinishedAt) {
			return mcpReadItem{}, false
		}
		_, meta, ok := splitResearchMeta(run.ResultSummary)
		if !ok || !meta.hasURL(item.URL) {
			return mcpReadItem{}, false
		}
		return mcpReadItem{Source: "find", ID: mcpReadText(id), At: run.FinishedAt, Title: mcpReadTextPtr(item.Title), Snippet: mcpReadTextPtr(item.Source + ": " + item.WhatsNew), URL: mcpReadBriefURL(item.URL), Status: mcpReadTextPtr("new")}, true
	}
	return mcpReadItem{}, false
}

func (r *mcpBriefReader) interestAllowed(dir string, sent topicSent, depth int) bool {
	provenance := sent.ReadSources
	if provenance == nil || provenance.Version != 1 {
		return false
	}
	if provenance.UserNamed {
		return len(provenance.Evidence) == 0 && len(sent.Queries) == 0
	}
	if len(provenance.Evidence) == 0 || r.exceedsLimit(len(provenance.Evidence), 100) {
		return false
	}
	var state interestsStateFile
	if !mcpReadBriefJSON(filepath.Join(dir, interestsStateFileName), &state) {
		return false
	}
	for _, item := range state.Interests {
		if item.ID != sent.ID {
			continue
		}
		for _, ev := range provenance.Evidence {
			_, sourceID, ok := strings.Cut(ev.Key, ":")
			if !ok {
				return false
			}
			switch ev.Source {
			case interests.SourceActivity:
				if _, ok := r.reference(mobileBriefRecapSource, sourceID, depth+1); !ok {
					return false
				}
			case interests.SourceGoal:
				if _, ok := r.reference("purpose", sourceID, depth+1); !ok {
					return false
				}
			case interests.SourceAbout:
				sourceID, _, _ = strings.Cut(sourceID, "#")
				if _, ok := r.reference("memory", sourceID, depth+1); !ok {
					return false
				}
			case interests.SourceChat:
				if !r.ownChat(sourceID) {
					return false
				}
			default:
				return false // hashed searches/names do not retain a recheckable capture
			}
		}
		return true
	}
	return false
}

// Shared MCP privacy checks cover capture settings. Briefs additionally read
// files and topic preferences, so a concurrent hide/forget also cancels output.
func (p *mcpReadPrivacy) mcpBriefSupplementalPolicy() ([32]byte, error) {
	hash := sha256.New()
	for _, key := range []string{permissionUIStateKey(filesPermissionKey), filesPrivateFoldersKey} {
		raw, err := p.app.store.GetUIState(p.ctx, key)
		if err != nil {
			return [32]byte{}, err
		}
		_, _ = fmt.Fprintf(hash, "%s:%d:%s\n", key, len(raw), raw)
	}
	dir, err := topicsStateDir()
	if err != nil {
		return [32]byte{}, err
	}
	for _, name := range []string{topicsStateFileName, interestsStateFileName} {
		var raw json.RawMessage
		if mcpReadBriefJSON(filepath.Join(dir, name), &raw) {
			_, _ = hash.Write(raw)
		}
		_, _ = hash.Write([]byte{0})
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func mcpReadBriefURL(raw string) *mcpUntrustedText {
	safe := pii.RedactURL(mobileBriefLink(raw))
	if safe == "" {
		return nil
	}
	return mcpReadTextPtr(safe)
}
