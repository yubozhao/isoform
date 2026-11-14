package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/interests"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// Stored interest names and explanations are derivatives, not fresh user
// input. All their original references must still authorize a model read.
// A missing source never becomes permission to replay its old derivative.
func (a *App) interestsReadFilter() func(interests.Interest) bool {
	var allowMemory func(string, string) bool
	var memoryErr, policyErr error
	var policyOnce sync.Once
	// Read on the first decision this filter has to work out, never for a
	// read whose decisions are all remembered.
	readPolicy := func() {
		policyOnce.Do(func() {
			allowMemory, memoryErr = a.memoryReadFilter()
			_, policyErr = a.activityReadPrivacyPolicy()
		})
	}
	// Remembered decisions and the sources they were derived from are shared
	// by every filter built while the key holds (the request path and the
	// background topic rebuild), so they are guarded by the shared lock, not
	// one local to this filter.
	shared := &interestsReadDecisions{checked: map[interests.Ref]bool{}, allowed: map[interests.Ref]bool{}}
	if a != nil {
		if remembered := a.interestsReads.decisions(a.interestsReadKey(), time.Now()); remembered != nil {
			shared = remembered
		}
	}
	decisionsMu := &shared.mu
	checked, allowed := shared.checked, shared.allowed
	// collectible rebuilds the search/named hashes the evidence pass would
	// derive right now, with the same collection logic and the same window.
	// A stored hash one of them still produces names something the sources
	// may still show; a hash they no longer produce is refused. The result
	// lives as long as the decisions it serves (same key, same expiry), so it
	// never outlives a source change that would change the decisions.
	collectible := func() map[string]bool {
		decisionsMu.Lock()
		if built := shared.hashes; built != nil {
			decisionsMu.Unlock()
			return built
		}
		decisionsMu.Unlock()
		hashes := map[string]bool{}
		now := time.Now()
		in := collectInterestInput(a, now.Add(-interests.RefreshWindow), now)
		if in.PeopleUnknown {
			// Without the people list nothing may be re-authorized, and an
			// empty answer is not remembered.
			return hashes
		}
		items, _ := interestItems(in)
		for _, item := range items {
			switch item.Source {
			case interests.SourceSearch, interests.SourceNamed:
				hashes[item.Ref] = true
			default:
			}
		}
		decisionsMu.Lock()
		if shared.hashes == nil {
			shared.hashes = hashes
		}
		hashes = shared.hashes
		decisionsMu.Unlock()
		return hashes
	}
	// goalPurposes is Today's complete Purpose evidence closure, reused by
	// every goal reference under the same key.
	goalPurposes := func() map[string]PurposeOpenLoopDetail {
		decisionsMu.Lock()
		if built := shared.purposes; built != nil {
			decisionsMu.Unlock()
			return built
		}
		decisionsMu.Unlock()
		// Reuse Today's complete Purpose evidence closure, including
		// corrections and carried-forward fields from earlier events.
		purposes := map[string]PurposeOpenLoopDetail{}
		for _, detail := range a.collectTodayChatSources(context.Background(), time.Now()).Purposes {
			purposes[detail.OpenLoop.ID] = detail
		}
		decisionsMu.Lock()
		if shared.purposes == nil {
			shared.purposes = purposes
		}
		purposes = shared.purposes
		decisionsMu.Unlock()
		return purposes
	}
	check := func(ref interests.Ref) bool {
		readPolicy()
		kind, id, ok := strings.Cut(ref.Key, ":")
		if !ok || id == "" {
			return false
		}
		switch ref.Source {
		case interests.SourceAbout:
			if kind != "about" || memoryErr != nil || allowMemory == nil {
				return false
			}
			memoryID, point, found := strings.Cut(id, "#")
			index, err := strconv.Atoi(point)
			if !found || err != nil || index < 0 {
				return false
			}
			entry, err := a.memoryEntryForInterestRead(memoryID)
			return err == nil && allowMemory(entry.ID, entry.Content)
		case interests.SourceActivity:
			if a.activity == nil || a.activity.store == nil || policyErr != nil {
				return false
			}
			store := a.activity.store
			switch kind {
			case "activity":
				row, err := store.GetActivityByID(id)
				return err == nil && row != nil && len(a.filterComposerModelCandidates(store, []ActivityRow{*row})) == 1
			case "semantic":
				var appsJSON, sourcesJSON string
				var source activitySourceProvenance
				err := store.db.QueryRow(`SELECT apps,source_refs,start_ms,end_ms FROM activity_semantic_events WHERE id=?`, id).
					Scan(&appsJSON, &sourcesJSON, &source.StartTime, &source.EndTime)
				if err != nil {
					return false
				}
				return a.interestSemanticSourceAllowsRead(SemanticEventRow{ID: id, Apps: appsJSON, SourceRefs: sourcesJSON, StartMs: source.StartTime, EndMs: source.EndTime})
			default:
				return false
			}
		case interests.SourceChat:
			if kind != "chat" || a.store == nil {
				return false
			}
			ctx := context.Background()
			row, err := a.store.LoadSession(ctx, id)
			if err != nil || row == nil || row.AutomationResult || row.MeetingID != "" || row.Kind == storage.SessionKindMain || strings.HasPrefix(row.Label, "Transcript:") {
				return false
			}
			// LoadSession omits Hidden; query the stored flag explicitly.
			hidden, err := a.store.SessionHidden(ctx, id)
			return err == nil && !hidden
		case interests.SourceGoal:
			if kind != "goal" {
				return false
			}
			detail, ok := goalPurposes()[id]
			return ok && detail.Current.Title != "" && detail.Current.OccurredAt == ref.At
		case interests.SourceSearch, interests.SourceNamed:
			// The reference stores only a content hash, but it stands for one
			// typed query or one named thing: it is authorized while the same
			// collection still derives that hash from readable sources.
			if kind != string(ref.Source) {
				return false
			}
			return collectible()[ref.Key]
		default:
			return false
		}
	}
	return func(interest interests.Interest) bool {
		if a == nil || a.IsClosedEyesModeEnabled() || len(interest.Evidence) == 0 {
			return false
		}
		for _, ref := range interest.Evidence {
			decisionsMu.Lock()
			seen, ok := checked[ref], allowed[ref]
			decisionsMu.Unlock()
			if !seen {
				ok = check(ref)
				decisionsMu.Lock()
				checked[ref], allowed[ref] = true, ok
				decisionsMu.Unlock()
			}
			if !ok {
				return false
			}
		}
		return true
	}
}

// interestsReadCache remembers interestsReadFilter's decision for each
// evidence reference while nothing those decisions read has changed (see
// interestsReadKey), so Today's topics re-read without collecting every
// source again. The decisions expire after topicRefreshInterval as well:
// search and named references age out of the collection window.
type interestsReadCache struct {
	mu      sync.Mutex
	current *interestsReadDecisions
}

type interestsReadDecisions struct {
	mu               sync.Mutex // guards every field below
	key              string
	at               time.Time
	checked, allowed map[interests.Ref]bool
	// hashes and purposes are the collection results the decisions were
	// derived from; nil until a filter builds them.
	hashes   map[string]bool
	purposes map[string]PurposeOpenLoopDetail
}

// decisions returns the remembered decisions for key, starting new ones when
// key changed or they expired. An empty key (a state read failed) remembers
// nothing.
func (c *interestsReadCache) decisions(key string, now time.Time) *interestsReadDecisions {
	if key == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.current; d != nil && d.key == key && now.Sub(d.at) < topicRefreshInterval {
		return d
	}
	c.current = &interestsReadDecisions{key: key, at: now, checked: map[interests.Ref]bool{}, allowed: map[interests.Ref]bool{}}
	return c.current
}

// interestsReadKey stamps what the evidence checks read: the privacy epoch
// (privacy withdrawals and the app events in mobileBriefReadInputEvents,
// Closed Eyes and sensitive apps among them), memory writes and the current
// memory snapshot (outside edits), Closed Eyes, the user's sensitive apps,
// the pause log and the sessions' protection count.
func (a *App) interestsReadKey() string {
	return a.interestsReadKeyContext(context.Background())
}

// interestsReadKeyContext is interestsReadKey whose database reads stop with
// ctx: a caller that is cancelled while the database is busy returns at once.
func (a *App) interestsReadKeyContext(ctx context.Context) string {
	snapshot, err := a.memorySnapshot()
	if err != nil {
		return ""
	}
	pauses, err := a.observationPausesContext(ctx)
	if err != nil && a.store != nil {
		return ""
	}
	activity, err := a.mobileBriefActivityPrivacyStamp(ctx, time.Now().UnixMilli())
	if err != nil {
		return ""
	}
	return mobileBriefModelDigest(struct {
		Epoch, MemoryRevision uint64
		Snapshot              string
		ClosedEyes            bool
		SensitiveApps         map[string]string
		Pauses                []observationPause
		Activity              [2]int64
	}{mobileBriefReadEpoch.Load(), a.memoryExternalReads.revision.Load(), snapshotIdentity(snapshot), a.IsClosedEyesModeEnabled(),
		pii.GetUserSensitiveApps(), pauses, [2]int64{activity.sessions, activity.protected}})
}

// memoryEntryForInterestRead is GetMemoryEntry from the current memory
// snapshot, without reading the file again.
func (a *App) memoryEntryForInterestRead(id string) (MemoryEntry, error) {
	if snapshot, err := a.memorySnapshot(); err == nil {
		if entry, ok := snapshot.entryByID(id); ok {
			return entry, nil
		}
	}
	return a.GetMemoryEntry(id)
}

// interestsFollowingReadFilter checks each activity identity, reusing verified
// source facts and reading only referenced cache misses. Other evidence kinds
// keep their complete checks in the background pass and wait for its snapshot.
// followingSlowCheckLog is the per-interest check time above which the check
// logs its breakdown.
const followingSlowCheckLog = 500 * time.Millisecond

func (a *App) interestsFollowingReadFilter(ctx context.Context) func(interests.Interest) bool {
	policy, err := a.activityReadPrivacyPolicyContext(ctx)
	return func(interest interests.Interest) bool {
		if a == nil || a.IsClosedEyesModeEnabled() || err != nil ||
			len(interest.Evidence) == 0 {
			return false
		}
		// Which read stalls when one interest's check is slow: counts and
		// durations only.
		var activityTime, semanticTime time.Duration
		activityRefs, semanticRefs := 0, 0
		started := time.Now()
		defer func() {
			if total := time.Since(started); total >= followingSlowCheckLog {
				log.Printf("[topics] following_check_slow total_ms=%d refs=%d activity_refs=%d activity_ms=%d semantic_refs=%d semantic_ms=%d",
					total.Milliseconds(), len(interest.Evidence), activityRefs, activityTime.Milliseconds(), semanticRefs, semanticTime.Milliseconds())
			}
		}()
		for _, ref := range interest.Evidence {
			if ctx.Err() != nil || ref.Source != interests.SourceActivity {
				return false
			}
			kind, id, ok := strings.Cut(ref.Key, ":")
			if !ok || id == "" || a.activity == nil || a.activity.store == nil {
				return false
			}
			switch kind {
			case "activity":
				activityRefs++
				at := time.Now()
				row, err := a.activity.store.GetActivityByIDContext(ctx, id)
				ok := err == nil && row != nil && a.interestFollowingActivity(ctx, *row, policy)
				activityTime += time.Since(at)
				if !ok {
					return false
				}
			case "semantic":
				semanticRefs++
				at := time.Now()
				ok := a.interestFollowingSemantic(ctx, id, policy)
				semanticTime += time.Since(at)
				if !ok {
					return false
				}
			default:
				return false
			}
		}
		return ctx.Err() == nil
	}
}

func (a *App) interestFollowingActivity(ctx context.Context, row ActivityRow, policy activityReadPrivacyPolicy) bool {
	ids := composeParseStrings(row.RunIDs)
	if len(ids) == 0 || len(ids) > 64 {
		return false
	}
	encoded, _ := json.Marshal(ids)
	store := a.activity.store
	cursor, err := store.reader().QueryContext(ctx, `SELECT `+timelineRunColumns+` FROM activity_timeline_groups WHERE id IN (SELECT value FROM json_each(?))`, string(encoded))
	if err != nil {
		return false
	}
	batch, err := scanTimelineRunRowsContext(ctx, cursor)
	cursor.Close()
	if err != nil || len(batch) != len(ids) {
		return false
	}
	runs := make(map[string]ActivityTimelineRunRow, len(batch))
	sessionIDs := []string{}
	seenSessions := map[string]bool{}
	for _, run := range batch {
		runs[run.ID] = run
		for _, id := range run.SessionIDs {
			if !seenSessions[id] {
				seenSessions[id] = true
				sessionIDs = append(sessionIDs, id)
			}
		}
		if len(sessionIDs) > 64 {
			return false
		}
	}
	sessions, err := store.GetSessionsByIDsContext(ctx, sessionIDs)
	if err != nil {
		return false
	}
	allowedSessions := map[string]bool{}
	return composerCandidateAllowsModelRead(row, func(id string) bool {
		run, found := runs[id]
		if !found || activitySourceAppIsSensitive(run.AppKey, run.AppName) || len(run.SessionIDs) == 0 || len(run.SessionIDs) > 64 {
			return false
		}
		for _, sourceID := range run.SessionIDs {
			allowed, checked := allowedSessions[sourceID]
			if !checked {
				session := sessions[sourceID]
				allowed = session != nil && a.interestFollowingSessionAllowed(ctx, *session, policy)
				allowedSessions[sourceID] = allowed
			}
			if !allowed {
				return false
			}
		}
		return ctx.Err() == nil
	})
}

func (a *App) interestFollowingSessionAllowed(ctx context.Context, session ActivitySessionRow, policy activityReadPrivacyPolicy) bool {
	declared := activitySourceProvenance{StartTime: session.StartTime, EndTime: session.EndTime, Private: session.TriageStatus == "protected"}
	if session.AppNames != "" && json.Unmarshal([]byte(session.AppNames), &declared.Apps) != nil {
		return false
	}
	declared.Apps = append(declared.Apps, session.MeetingApp)
	// Recording dependencies need their complete background closure here.
	if session.TranscriptPath != "" || !policy.allows(declared) {
		return false
	}
	store := a.activity.store
	source, found, err := store.cachedActivitySessionProvenanceContext(ctx, session.ID)
	if err != nil {
		return false
	}
	if !found {
		// A referenced session outside the bounded activity pre-pass still
		// needs its own complete provenance check, never a whole-window read.
		source, err = store.activitySessionProvenanceContext(ctx, session)
	}
	return err == nil && policy.allows(source) && ctx.Err() == nil
}

func (a *App) interestFollowingSemantic(ctx context.Context, id string, policy activityReadPrivacyPolicy) bool {
	store := a.activity.store
	var row SemanticEventRow
	err := store.reader().QueryRowContext(ctx, `SELECT apps,source_refs,start_ms,end_ms FROM activity_semantic_events WHERE id=?`, id).
		Scan(&row.Apps, &row.SourceRefs, &row.StartMs, &row.EndMs)
	if err != nil {
		return false
	}
	var refs semanticSourceRefs
	source := activitySourceProvenance{StartTime: row.StartMs, EndTime: row.EndMs}
	if json.Unmarshal([]byte(row.Apps), &source.Apps) != nil || json.Unmarshal([]byte(row.SourceRefs), &refs) != nil ||
		len(refs.SessionIDs) == 0 || len(refs.SessionIDs) > 64 || len(refs.SnapshotIDs) > 64 || !policy.allows(source) {
		return false
	}
	sessions, err := store.GetSessionsByIDsContext(ctx, refs.SessionIDs)
	if err != nil || len(sessions) != len(refs.SessionIDs) {
		return false
	}
	for _, sessionID := range refs.SessionIDs {
		if !a.interestFollowingSessionAllowed(ctx, *sessions[sessionID], policy) {
			return false
		}
	}
	for _, snapshotID := range refs.SnapshotIDs {
		var sessionID string
		if err := store.reader().QueryRowContext(ctx, `SELECT session_id FROM activity_snapshots WHERE id=?`, snapshotID).Scan(&sessionID); err != nil || sessions[sessionID] == nil {
			return false
		}
	}
	return ctx.Err() == nil
}

// Derivative app/time metadata cannot stand in for its original sessions or
// snapshots. Validate the complete recorded closure, including foreign and
// missing snapshot identities, before any semantic text or entities are read.
func (a *App) interestSemanticSourceAllowsRead(row SemanticEventRow) bool {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return false
	}
	policy, err := a.activityReadPrivacyPolicy()
	if err != nil {
		return false
	}
	var refs semanticSourceRefs
	source := activitySourceProvenance{StartTime: row.StartMs, EndTime: row.EndMs}
	if json.Unmarshal([]byte(row.Apps), &source.Apps) != nil || json.Unmarshal([]byte(row.SourceRefs), &refs) != nil || len(refs.SessionIDs) == 0 || !policy.allows(source) {
		return false
	}
	sessions := map[string]bool{}
	for _, id := range refs.SessionIDs {
		if !a.activityDependencyAllowsModelRead(id, nil) {
			return false
		}
		sessions[id] = true
	}
	for _, id := range refs.SnapshotIDs {
		var session string
		if err := a.activity.store.db.QueryRow(`SELECT session_id FROM activity_snapshots WHERE id=?`, id).Scan(&session); err != nil || !sessions[session] {
			return false
		}
	}
	return true
}

// snapshotIdentity names a memory snapshot for the stamp. A settled snapshot is
// named by its folder, write revision and signal, so the stamp holds while the
// folder is unchanged. A snapshot still settling is named by its pointer: its
// folder can change within the same second, which a signal cannot show, so the
// stamp must differ from the last read's.
func snapshotIdentity(snapshot *memoryExternalSnapshot) string {
	if snapshot == nil {
		return ""
	}
	if snapshot.settled {
		return fmt.Sprintf("settled|%s|%d|%x", snapshot.dirKey, snapshot.revision, snapshot.signal)
	}
	return fmt.Sprintf("unsettled|%p", snapshot)
}
