package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"yansu-agent/agent"
)

// Activity threads: storage, the background pass, and the Wails bindings.
// Membership lives beside the activities table, never in it, so the composer's
// moments stay exactly as they are. A user move or rename is stored as a
// correction and as a member row with source "user"; a regroup never touches
// user rows, so a move survives.

const (
	// activityThreadModelGap spaces today's model calls; sure joins still apply
	// on every pass. Past days get at most one call, the first time they open.
	activityThreadModelGap  = 10 * time.Minute
	activityThreadCallLimit = 3 * time.Minute
	activityThreadReplyMax  = 64 << 10
	activityThreadCacheTTL  = 60 * time.Second
	activityThreadDayMs     = int64(24 * time.Hour / time.Millisecond)

	activityThreadsUpdatedEvent = "activity:threads-updated"
)

// ActivityThreadView is one thread of a day as the screen shows it.
type ActivityThreadView struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Kind        string   `json:"kind"`
	UserTitled  bool     `json:"userTitled"`
	StartMs     int64    `json:"startMs"`
	EndMs       int64    `json:"endMs"`
	TimeSpentMs int64    `json:"timeSpentMs"`
	ActivityIDs []string `json:"activityIds"`

	Stretches    []ActivityThreadStretch  `json:"stretches"`
	Gaps         []ActivityThreadGap      `json:"gaps"`
	ActiveMs     int64                    `json:"activeMs"`
	LinkReason   ActivityThreadLinkReason `json:"linkReason"`
	Interruption bool                     `json:"interruption"`
	Reviewed     bool                     `json:"reviewed"`
	Unfinished   bool                     `json:"unfinished"`
}

// ActivityDayThreads is a day's threads. Grouped is false while a past day
// waits for its first grouping; the screen then shows moments ungrouped.
type ActivityDayThreads struct {
	Grouped bool                 `json:"grouped"`
	Threads []ActivityThreadView `json:"threads"`
	// Agents is the day's agent work, shown only as one labelled line; it is
	// never part of the person's threads.
	Agents    ActivityAgentDay `json:"agents"`
	carryDays []int64
}

var (
	activityThreadsMu        sync.Mutex
	activityThreadCacheMu    sync.Mutex
	activityThreadCache      = map[activityThreadCacheKey]activityThreadCacheEntry{}
	activityThreadGroupingMu sync.Mutex
	activityThreadGrouping   = map[int64]bool{}
)

type activityThreadCacheKey struct {
	store *ActivityStore
	day   int64
}

type activityThreadCacheEntry struct {
	signature activityThreadProjectionStamp
	at        time.Time
	value     ActivityDayThreads
}

func migrateActivityThreads(tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS activity_threads (
			id                  TEXT PRIMARY KEY,
			day_start_ms        INTEGER NOT NULL,
			title               TEXT NOT NULL DEFAULT '',
			title_basis         TEXT NOT NULL DEFAULT '[]',
			user_title          TEXT NOT NULL DEFAULT '',
			kind                TEXT NOT NULL DEFAULT 'work',
			origin              TEXT NOT NULL DEFAULT '',
			purpose_key         TEXT NOT NULL DEFAULT '',
			created_at          INTEGER NOT NULL,
			updated_at          INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_threads_day ON activity_threads(day_start_ms)`,
		`CREATE TABLE IF NOT EXISTS activity_thread_members (
			activity_id  TEXT PRIMARY KEY,
			thread_id    TEXT NOT NULL,
			day_start_ms INTEGER NOT NULL,
			source       TEXT NOT NULL,
			reason       TEXT NOT NULL DEFAULT '',
			reviewed     INTEGER NOT NULL DEFAULT 0,
			updated_at   INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_thread_members_thread ON activity_thread_members(thread_id)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_thread_members_day ON activity_thread_members(day_start_ms)`,
		`CREATE TABLE IF NOT EXISTS activity_thread_corrections (
			id             TEXT PRIMARY KEY,
			activity_id    TEXT NOT NULL,
			kind           TEXT NOT NULL,
			from_thread_id TEXT NOT NULL DEFAULT '',
			from_members   TEXT NOT NULL DEFAULT '[]',
			to_thread_id   TEXT NOT NULL DEFAULT '',
			created_at     INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_thread_corrections_created ON activity_thread_corrections(created_at)`,
		`CREATE TABLE IF NOT EXISTS activity_thread_days (
			day_start_ms INTEGER PRIMARY KEY,
			grouped_at   INTEGER NOT NULL DEFAULT 0,
			model_at     INTEGER NOT NULL DEFAULT 0,
			updated_at   INTEGER NOT NULL
		)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *ActivityStore) activityThreadsPresent() (bool, error) {
	a, e1 := s.hasTable("activity_threads")
	b, e2 := s.hasTable("activity_thread_members")
	c, e3 := s.hasTable("activity_thread_corrections")
	d, e4 := s.hasTable("activity_thread_days")
	e, e5 := s.hasIndex("idx_activity_thread_members_day")
	return a && b && c && d && e, firstErr(e1, e2, e3, e4, e5)
}

// ── Store reads and writes ──

type activityThreadRow struct {
	threadState
	DayStartMs    int64
	TitleBasis    []string
	PurposeKey    string
	UserConfirmed bool
	ReasonBasis   activityThreadStoredReason
}

type activityThreadMember struct {
	ActivityID    string
	ThreadID      string
	Source        string
	Reason        string
	ReasonContext string
	Reviewed      bool
}

func (s *ActivityStore) loadActivityThreads(ctx context.Context, day int64) ([]activityThreadRow, []activityThreadMember, error) {
	rows, err := s.reader().QueryContext(ctx, `SELECT id, title, title_basis, user_title, kind, origin, purpose_key, user_confirmed, reason_basis FROM activity_threads WHERE day_start_ms = ? ORDER BY created_at, id`, day)
	if err != nil {
		return nil, nil, err
	}
	var threads []activityThreadRow
	for rows.Next() {
		var t activityThreadRow
		var basis, origin, reasonBasis string
		if err := rows.Scan(&t.ID, &t.Title, &basis, &t.UserTitle, &t.Kind, &origin, &t.PurposeKey, &t.UserConfirmed, &reasonBasis); err != nil {
			rows.Close()
			return nil, nil, err
		}
		t.DayStartMs = day
		t.UserMade = origin == threadSourceUser
		_ = json.Unmarshal([]byte(basis), &t.TitleBasis)
		_ = json.Unmarshal([]byte(reasonBasis), &t.ReasonBasis)
		threads = append(threads, t)
	}
	if err := firstErr(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	rows, err = s.reader().QueryContext(ctx, `SELECT activity_id, thread_id, source, reason, reviewed FROM activity_thread_members WHERE day_start_ms = ?`, day)
	if err != nil {
		return nil, nil, err
	}
	var members []activityThreadMember
	for rows.Next() {
		var m activityThreadMember
		var reviewed int
		if err := rows.Scan(&m.ActivityID, &m.ThreadID, &m.Source, &m.Reason, &reviewed); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if m.Source == threadSourceAI {
			var record activityThreadStoredReason
			if json.Unmarshal([]byte(m.Reason), &record) == nil && record.Version == 2 {
				m.Reason, m.ReasonContext = record.Text, record.Context
			}
		}
		m.Reviewed = reviewed != 0
		members = append(members, m)
	}
	if err := firstErr(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	return threads, members, nil
}

func (s *ActivityStore) activityThreadDay(ctx context.Context, day int64) (groupedAt, modelAt int64, err error) {
	err = s.reader().QueryRowContext(ctx, `SELECT grouped_at, model_at FROM activity_thread_days WHERE day_start_ms = ?`, day).Scan(&groupedAt, &modelAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	return groupedAt, modelAt, err
}

// activityThreadDayEnd advances by a local date, never a fixed 24 hours.
func activityThreadDayEnd(day int64) int64 {
	return time.UnixMilli(day).In(time.Local).AddDate(0, 0, 1).UnixMilli()
}

// dayActivityRows returns moments owned by their start day, oldest first.
func (s *ActivityStore) dayActivityRows(ctx context.Context, day int64) ([]ActivityRow, error) {
	rows, err := s.GetActivitiesInRangeContext(ctx, day, activityThreadDayEnd(day)-1)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, row := range rows {
		if row.DayStartMs == day {
			out = append(out, row)
		}
	}
	// Moments made only of agent runs are not the person's work.
	if agentRuns, err := s.agentRunIDs(ctx, day); err == nil {
		out = withoutAgentMoments(out, agentRuns)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartMs < out[j].StartMs })
	return out, nil
}

func threadMomentFromRow(row ActivityRow) threadMoment {
	var apps []string
	for app := range composeParseInt64Map(row.ActiveMsByApp) {
		apps = append(apps, app)
	}
	sort.Strings(apps)
	return threadMoment{
		ID: row.ID, StartMs: row.StartMs, EndMs: row.EndMs, Title: row.Title,
		Description: row.Description, Category: row.Category,
		Entities: threadEntitiesFromJSON(row.Entities), Apps: apps, ContinuesID: row.ContinuesActivityID,
	}
}

// ── The pass ──

// runActivityThreadPass groups one day's moments. Sure joins always run; the
// model runs only when allowModel is set. Reads and the model call happen
// outside any write transaction; the writes land in one short transaction.
// It reports whether anything changed.
func (a *App) runActivityThreadPass(ctx context.Context, day int64, allowModel bool, now time.Time) (bool, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return false, errors.New("activity store unavailable")
	}
	store := a.activity.store
	dayLock := activityThreadDayLock(day)
	dayLock.Lock()
	defer dayLock.Unlock()
	nowMs := now.UnixMilli()

	// ── Read ──
	rows, err := store.dayActivityRows(ctx, day)
	if err != nil {
		return false, err
	}
	allowedRows := a.filterComposerModelCandidates(store, rows)
	allowed := map[string]ActivityRow{}
	for _, row := range allowedRows {
		allowed[row.ID] = row
	}
	threads, members, err := store.loadActivityThreads(ctx, day)
	if err != nil {
		return false, err
	}
	groupedAt, modelAt, err := store.activityThreadDay(ctx, day)
	if err != nil {
		return false, err
	}
	assigned := map[string]string{}
	threadByID := map[string]*activityThreadRow{}
	for i := range threads {
		threadByID[threads[i].ID] = &threads[i]
	}
	// A deleted or newly private moment leaves its thread.
	var leaving, unreviewed []string
	for _, member := range members {
		if _, ok := allowed[member.ActivityID]; !ok || threadByID[member.ThreadID] == nil {
			leaving = append(leaving, member.ActivityID)
			continue
		}
		assigned[member.ActivityID] = member.ThreadID
		threadByID[member.ThreadID].Members = append(threadByID[member.ThreadID].Members, member.ActivityID)
		if !member.Reviewed && member.Source == threadSourceRule {
			unreviewed = append(unreviewed, member.ActivityID)
		}
	}
	selfNames := threadSelfNames(a.getUserDisplayName())
	states := make([]threadState, 0, len(threads))
	threadKeys := map[string]map[string]bool{}
	for _, thread := range threads {
		if len(thread.Members) == 0 {
			continue
		}
		states = append(states, thread.threadState)
		keys := map[string]bool{}
		for _, id := range thread.Members {
			for k := range threadSureKeys(threadMomentFromRow(allowed[id]), selfNames) {
				keys[k] = true
			}
		}
		threadKeys[thread.ID] = keys
	}
	var unassigned []threadMoment
	for _, row := range allowedRows {
		if _, ok := assigned[row.ID]; !ok {
			unassigned = append(unassigned, threadMomentFromRow(row))
		}
	}
	ruleJoins, started, pending := threadRulesPass(states, threadKeys, assigned, unassigned, selfNames)
	states = append(states, started...)
	stateIndex := map[string]int{}
	for i := range states {
		stateIndex[states[i].ID] = i
	}
	for _, join := range ruleJoins {
		unreviewed = append(unreviewed, join.MomentID)
		if i, ok := stateIndex[join.ThreadID]; ok && !containsString(states[i].Members, join.MomentID) {
			states[i].Members = append(states[i].Members, join.MomentID)
		}
	}

	// ── Model ──
	var in threadPromptInput
	var aliases threadPromptAliases
	var resp threadModelResponse
	modelRan, modelOK := false, false
	if allowModel && (len(pending) > 0 || len(unreviewed) > 0) {
		auxBefore, auxErr := a.activityThreadReasonAux(ctx)
		if auxErr != nil {
			return false, auxErr
		}
		in = threadPromptInput{
			Threads:     states,
			ThreadTitle: map[string]string{},
			RuleJoined:  map[string]string{},
			Language:    responseLanguageInstruction(a.currentUILanguageName()),
			Goals:       a.activityThreadGoals(ctx, now),
		}
		in.Corrections, err = store.activityThreadCorrectionExamples(ctx, allowed)
		if err != nil {
			return false, err
		}
		in.ReasonAux, err = a.activityThreadReasonAux(ctx)
		if err != nil {
			return false, err
		}
		in.ReasonTitles = activityThreadReasonTitleHashes(threads)
		// Purpose can depend on file-backed Memory/Knowledge with no database revision.
		// Keep it in the provider prompt, but withhold derived explanation text.
		if len(in.Goals) == 0 && auxBefore == in.ReasonAux && activityThreadReasonTitlesReadable(threads, allowed) {
			in.ReasonSources, err = a.activityThreadReasonContext(ctx, allowedRows)
			if err != nil {
				return false, err
			}
			if in.ReasonSources != "" {
				contextJSON, _ := json.Marshal([]any{in.ReasonSources, in.ReasonAux, in.ReasonTitles})
				in.ReasonContext = agencyPayloadHash(contextJSON)
			}
		}
		deciding := map[string]bool{}
		for _, moment := range pending {
			in.Moments = append(in.Moments, moment)
			deciding[moment.ID] = true
		}
		for _, id := range unreviewed {
			if row, ok := allowed[id]; ok && !deciding[id] {
				in.Moments = append(in.Moments, threadMomentFromRow(row))
				in.RuleJoined[id] = assigned[id]
				deciding[id] = true
			}
		}
		sort.SliceStable(in.Moments, func(i, j int) bool { return in.Moments[i].StartMs < in.Moments[j].StartMs })
		for id, row := range allowed {
			if !deciding[id] {
				in.ThreadTitle[id] = row.Title
			}
		}
		var prompt string
		prompt, aliases = buildThreadPrompt(in)
		callCtx, cancel := context.WithTimeout(ctx, activityThreadCallLimit)
		raw, callErr := a.runActivityThreadPrompt(callCtx, prompt)
		cancel()
		modelRan = true
		if callErr != nil {
			log.Printf("[activity-threads] model call failed (moments stay ungrouped): %v", callErr)
		} else if resp, err = parseThreadModelResponse(raw); err != nil {
			log.Printf("[activity-threads] %v", err)
		} else {
			modelOK = true
		}
		// Privacy may change while the provider runs: a moment that is no
		// longer readable is not written into any thread.
		still := map[string]bool{}
		for _, row := range a.filterComposerModelCandidates(store, rows) {
			still[row.ID] = true
		}
		for id := range allowed {
			if !still[id] {
				delete(allowed, id)
				leaving = append(leaving, id)
			}
		}
	}

	// ── Write ──
	activityThreadsMu.Lock()
	defer activityThreadsMu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	changed := false
	for _, id := range leaving {
		if _, err := tx.ExecContext(ctx, `DELETE FROM activity_thread_members WHERE activity_id = ?`, id); err != nil {
			return false, err
		}
		// A title written from a deleted or now-private moment is erased, not
		// only hidden: no text derived from it stays behind.
		if _, err := tx.ExecContext(ctx, `UPDATE activity_threads SET title = '', title_basis = '[]', updated_at = ?
			WHERE day_start_ms = ? AND EXISTS (SELECT 1 FROM json_each(activity_threads.title_basis) WHERE value = ?)`, nowMs, day, id); err != nil {
			return false, err
		}
		changed = true
	}
	for _, thread := range started {
		if err := insertActivityThread(ctx, tx, thread.ID, day, thread.Title, nil, thread.Kind, nowMs); err != nil {
			return false, err
		}
	}
	for _, join := range ruleJoins {
		if _, ok := allowed[join.MomentID]; !ok {
			continue
		}
		if err := upsertActivityThreadMember(ctx, tx, join.MomentID, join.ThreadID, day, threadSourceRule, join.Reason, false, nowMs); err != nil {
			return false, err
		}
		changed = true
	}
	if modelOK {
		if err := applyThreadModel(ctx, tx, day, in, aliases, resp, threadByID, allowed, nowMs); err != nil {
			return false, err
		}
		changed = true
	}
	if modelRan {
		modelAt = nowMs
	}
	// A day counts as grouped once the model has answered, or when nothing is
	// left for it. A failed call leaves a past day to retry on a later open.
	if groupedAt == 0 && (modelOK || len(pending) == 0 && len(unreviewed) == 0) {
		groupedAt = nowMs
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO activity_thread_days(day_start_ms, grouped_at, model_at, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(day_start_ms) DO UPDATE SET grouped_at=excluded.grouped_at, model_at=excluded.model_at, updated_at=excluded.updated_at`,
		day, groupedAt, modelAt, nowMs); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	activityThreadCacheForget(day)
	return changed, nil
}

var (
	activityThreadDayLocksMu sync.Mutex
	activityThreadDayLocks   = map[int64]*sync.Mutex{}
)

// activityThreadDayLock serializes passes over one day, so the loop, a past
// day's first grouping and a regroup never interleave on the same day.
func activityThreadDayLock(day int64) *sync.Mutex {
	activityThreadDayLocksMu.Lock()
	defer activityThreadDayLocksMu.Unlock()
	lock := activityThreadDayLocks[day]
	if lock == nil {
		lock = &sync.Mutex{}
		activityThreadDayLocks[day] = lock
	}
	return lock
}

func applyThreadModel(ctx context.Context, tx *sql.Tx, day int64, in threadPromptInput, aliases threadPromptAliases, resp threadModelResponse, existing map[string]*activityThreadRow, allowed map[string]ActivityRow, nowMs int64) error {
	assignments, created, retitled := threadApplyModel(in, aliases, resp)
	kept := assignments[:0]
	for _, as := range assignments {
		if _, ok := allowed[as.MomentID]; ok {
			kept = append(kept, as)
		}
	}
	assignments = kept
	newIDs := map[string]string{}
	members := map[string][]string{}
	for _, as := range assignments {
		members[as.ThreadID] = append(members[as.ThreadID], as.MomentID)
	}
	for _, thread := range created {
		ids := members[thread.Key]
		if len(ids) == 0 {
			continue
		}
		id := threadIDForMoment(ids[0])
		newIDs[thread.Key] = id
		if err := insertActivityThread(ctx, tx, id, day, thread.Title, ids, thread.Kind, nowMs); err != nil {
			return err
		}
	}
	for _, as := range assignments {
		target := as.ThreadID
		if id, ok := newIDs[target]; ok {
			target = id
		}
		if err := upsertActivityThreadMember(ctx, tx, as.MomentID, target, day, as.Source, as.Reason, true, nowMs, activityThreadStoredReason{Version: 2, Context: in.ReasonContext}); err != nil {
			return err
		}
	}
	reasonBasis, err := json.Marshal(activityThreadStoredReason{Version: 2, Context: in.ReasonContext, Titles: in.ReasonTitles, Aux: in.ReasonAux, Sources: in.ReasonSources, UnsafeInputs: in.ReasonContext == ""})
	if err != nil {
		return err
	}
	targets := map[string]bool{}
	for _, as := range assignments {
		target := as.ThreadID
		if id, ok := newIDs[target]; ok {
			target = id
		}
		if !targets[target] {
			if _, err := tx.ExecContext(ctx, `UPDATE activity_threads SET reason_basis=? WHERE id=?`, string(reasonBasis), target); err != nil {
				return err
			}
			targets[target] = true
		}
	}
	// Every moment the model saw is reviewed, including rule joins it kept.
	for _, moment := range in.Moments {
		if _, err := tx.ExecContext(ctx, `UPDATE activity_thread_members SET reviewed = 1 WHERE activity_id = ? AND source = ?`, moment.ID, threadSourceRule); err != nil {
			return err
		}
	}
	for id, title := range retitled {
		thread := existing[id]
		var basis []string
		if thread != nil {
			basis = append(basis, thread.Members...)
		}
		for _, as := range assignments {
			if as.ThreadID == id {
				basis = append(basis, as.MomentID)
			}
		}
		for mid, tid := range in.RuleJoined {
			if tid == id && !containsString(basis, mid) {
				basis = append(basis, mid)
			}
		}
		basisJSON, _ := json.Marshal(basis)
		if _, err := tx.ExecContext(ctx, `UPDATE activity_threads SET title = ?, title_basis = ?, reason_basis = ?, updated_at = ? WHERE id = ?`, title, string(basisJSON), string(reasonBasis), nowMs, id); err != nil {
			return err
		}
	}
	// Threads the rules started in this pass take the kind the model gives.
	for _, thread := range resp.Threads {
		if id, ok := aliases.threadByAlias[strings.TrimSpace(thread.ID)]; ok && existing[id] == nil {
			kind := threadNormKind(thread.Kind)
			if _, err := tx.ExecContext(ctx, `UPDATE activity_threads SET kind = ? WHERE id = ? AND kind != ?`, kind, id, kind); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertActivityThread(ctx context.Context, tx *sql.Tx, id string, day int64, title string, basis []string, kind string, nowMs int64) error {
	if kind == "" {
		kind = threadKindWork
	}
	if basis == nil {
		basis = []string{}
	}
	basisJSON, _ := json.Marshal(basis)
	_, err := tx.ExecContext(ctx, `INSERT INTO activity_threads(id, day_start_ms, title, title_basis, kind, created_at, updated_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET title = CASE WHEN excluded.title != '' THEN excluded.title ELSE activity_threads.title END,
		title_basis = CASE WHEN excluded.title != '' THEN excluded.title_basis ELSE activity_threads.title_basis END,
		kind = excluded.kind, updated_at = excluded.updated_at`,
		id, day, threadCleanTitle(title), string(basisJSON), kind, nowMs, nowMs)
	return err
}

func upsertActivityThreadMember(ctx context.Context, tx *sql.Tx, activityID, threadID string, day int64, source, reason string, reviewed bool, nowMs int64, reasonContext ...activityThreadStoredReason) error {
	r := 0
	if reviewed {
		r = 1
	}
	if source == threadSourceAI {
		reason = redactThreadOutcomeText(reason)
	}
	reason = threadPromptText(reason, 120)
	if source == threadSourceAI && len(reasonContext) > 0 && reasonContext[0].Context != "" {
		record := activityThreadStoredReason{Version: 2, Text: reason, Context: reasonContext[0].Context}
		encoded, err := json.Marshal(record)
		if err != nil {
			return err
		}
		reason = string(encoded)
	}
	// A user row is never replaced by grouping.
	_, err := tx.ExecContext(ctx, `INSERT INTO activity_thread_members(activity_id, thread_id, day_start_ms, source, reason, reviewed, updated_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(activity_id) DO UPDATE SET thread_id = excluded.thread_id, day_start_ms = excluded.day_start_ms, source = excluded.source,
		reason = excluded.reason, reviewed = excluded.reviewed, updated_at = excluded.updated_at
		WHERE activity_thread_members.source != 'user' OR excluded.source = 'user'`,
		activityID, threadID, day, source, reason, r, nowMs)
	return err
}

// activityThreadCorrectionExamples resolves the newest user corrections into
// titles at prompt time. Nothing is copied: a deleted or private moment drops
// out, and a thread without a title is named by its earliest readable moment.
func (s *ActivityStore) activityThreadCorrectionExamples(ctx context.Context, allowed map[string]ActivityRow) ([]threadCorrectionExample, error) {
	rows, err := s.reader().QueryContext(ctx, `SELECT activity_id, kind, from_thread_id, from_members, to_thread_id FROM activity_thread_corrections
		WHERE kind IN ('move', 'own', 'rename') ORDER BY created_at DESC LIMIT 40`)
	if err != nil {
		return nil, err
	}
	type fix struct {
		activityID, kind, from, to string
		fromMembers                []string
	}
	var fixes []fix
	for rows.Next() {
		var f fix
		var members string
		if err := rows.Scan(&f.activityID, &f.kind, &f.from, &members, &f.to); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(members), &f.fromMembers)
		fixes = append(fixes, f)
	}
	if err := firstErr(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var out []threadCorrectionExample
	renamedThreads := map[string]bool{}
	for _, f := range fixes {
		if f.kind == "rename" {
			if renamedThreads[f.to] {
				continue
			}
			title := s.activityThreadRenameForPrompt(ctx, f.to, allowed)
			if title == "" {
				continue
			}
			renamedThreads[f.to] = true
			out = append(out, threadCorrectionExample{RenamedTitle: title})
			if len(out) >= threadPromptFixLimit {
				break
			}
			continue
		}
		moment, ok := allowed[f.activityID]
		if !ok || moment.Title == "" {
			continue
		}
		example := threadCorrectionExample{MomentTitle: moment.Title, FromTitle: s.activityThreadNameForPrompt(ctx, f.from, f.activityID, f.fromMembers, allowed)}
		if f.kind == "move" {
			example.ToTitle = s.activityThreadNameForPrompt(ctx, f.to, f.activityID, nil, allowed)
			if example.ToTitle == "" {
				continue
			}
		}
		if example.FromTitle == "" {
			continue
		}
		out = append(out, example)
		if len(out) >= threadPromptFixLimit {
			break
		}
	}
	return out, nil
}

func (s *ActivityStore) activityThreadRenameForPrompt(ctx context.Context, threadID string, allowed map[string]ActivityRow) string {
	if threadID == "" {
		return ""
	}
	var title string
	if err := s.reader().QueryRowContext(ctx, `SELECT user_title FROM activity_threads WHERE id = ?`, threadID).Scan(&title); err != nil || strings.TrimSpace(title) == "" {
		return ""
	}
	rows, err := s.reader().QueryContext(ctx, `SELECT activity_id FROM activity_thread_members WHERE thread_id = ?`, threadID)
	if err != nil {
		return ""
	}
	readable := false
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			if _, ok := allowed[id]; ok {
				readable = true
			}
		}
	}
	if firstErr(rows.Err(), rows.Close()) != nil || !readable {
		return ""
	}
	return title
}

// activityThreadNameForPrompt names a thread for a correction example: the
// user's title, else the model's title, else the earliest readable moment the
// thread held (now, or at the time of the move).
func (s *ActivityStore) activityThreadNameForPrompt(ctx context.Context, threadID, exceptActivity string, formerMembers []string, allowed map[string]ActivityRow) string {
	if threadID == "" {
		return ""
	}
	var userTitle, title string
	if err := s.reader().QueryRowContext(ctx, `SELECT user_title, title FROM activity_threads WHERE id = ?`, threadID).Scan(&userTitle, &title); err == nil {
		if name := firstNonEmpty(strings.TrimSpace(userTitle), strings.TrimSpace(title)); name != "" {
			return name
		}
	}
	ids := append([]string(nil), formerMembers...)
	rows, err := s.reader().QueryContext(ctx, `SELECT activity_id FROM activity_thread_members WHERE thread_id = ?`, threadID)
	if err != nil {
		return ""
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if firstErr(rows.Err(), rows.Close()) != nil {
		return ""
	}
	var earliest ActivityRow
	for _, id := range ids {
		if id == exceptActivity {
			continue
		}
		if row, ok := allowed[id]; ok && (earliest.ID == "" || row.StartMs < earliest.StartMs) {
			earliest = row
		}
	}
	return earliest.Title
}

// activityThreadGoals lists current Purpose Map goals the person owns, read
// through the same checks the brief uses.
func (a *App) activityThreadGoals(ctx context.Context, now time.Time) []threadGoal {
	if a == nil || a.IsClosedEyesModeEnabled() {
		return nil
	}
	store, err := a.purposeProductStore()
	if err != nil {
		return nil
	}
	details, err := store.ListDetailsAllContext(ctx)
	if err != nil {
		return nil
	}
	var out []threadGoal
	for _, detail := range details {
		switch detail.OpenLoop.Kind {
		case PurposeKindGoal, PurposeKindOpportunity, PurposeKindResponsibility:
		default:
			continue
		}
		if !mobileBriefPurposeCurrent(detail) {
			continue
		}
		current, ok := a.proactiveVoicePurpose(ctx, detail.OpenLoop.ID, now)
		if !ok || strings.TrimSpace(current.Current.Title) == "" {
			continue
		}
		out = append(out, threadGoal{Key: detail.OpenLoop.CanonicalKey, Title: current.Current.Title})
		if len(out) >= threadPromptGoalLimit {
			break
		}
	}
	return out
}

func (a *App) runActivityThreadPrompt(ctx context.Context, prompt string) (string, error) {
	a.mu.Lock()
	runner := a.runner
	a.mu.Unlock()
	if runner == nil {
		return "", errors.New("runner unavailable")
	}
	var result strings.Builder
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		AutoApproveTools:  true,
		Prompt:            redactActivityModelText(prompt),
		Model:             a.chatTaskModel(),
		Priority:          agent.PriorityBackground,
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(event *agent.StreamEvent) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if event.Type == "assistant" {
			if result.Len()+len(event.Content) > activityThreadReplyMax {
				return errors.New("thread reply exceeds budget")
			}
			result.WriteString(event.Content)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("execute thread grouping: %w", err)
	}
	return result.String(), nil
}

// activityThreadsCanUseModel mirrors the composer's gate: no model reads while
// Closed Eyes is on or screen watching is off.
func (a *App) activityThreadsCanUseModel() bool {
	if a == nil || a.IsClosedEyesModeEnabled() || !a.screenWatchingEnabled() {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runner != nil
}

func (a *App) runTodayThreadPass(ctx context.Context, now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[activity-threads] recovered from panic: %v", r)
		}
	}()
	if a == nil || a.activity == nil || a.activity.store == nil {
		return
	}
	day := startOfDayLocal(now.UnixMilli())
	_, modelAt, err := a.activity.store.activityThreadDay(ctx, day)
	if err != nil {
		return
	}
	allowModel := a.activityThreadsCanUseModel() && now.UnixMilli()-modelAt >= activityThreadModelGap.Milliseconds()
	changed, err := a.runActivityThreadPass(ctx, day, allowModel, now)
	if err != nil {
		log.Printf("[activity-threads] pass: %v", err)
		return
	}
	if changed {
		a.emitBrowserEvent(activityThreadsUpdatedEvent, map[string]bool{"live": true})
	}
}

// groupPastDayOnce runs a past day's first grouping in the background, one
// model call at most. Concurrent opens of the same day share one run.
func (a *App) groupPastDayOnce(day int64) {
	activityThreadGroupingMu.Lock()
	if activityThreadGrouping[day] {
		activityThreadGroupingMu.Unlock()
		return
	}
	activityThreadGrouping[day] = true
	activityThreadGroupingMu.Unlock()
	go func() {
		defer func() {
			activityThreadGroupingMu.Lock()
			delete(activityThreadGrouping, day)
			activityThreadGroupingMu.Unlock()
			if r := recover(); r != nil {
				log.Printf("[activity-threads] recovered from panic: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), activityThreadCallLimit+time.Minute)
		defer cancel()
		allowModel := a.activityThreadsCanUseModel()
		if _, err := a.runActivityThreadPass(ctx, day, allowModel, time.Now()); err != nil {
			log.Printf("[activity-threads] group past day: %v", err)
			return
		}
		a.emitBrowserEvent(activityThreadsUpdatedEvent)
	}()
}

// ── Bindings ──

// GetDayThreads returns the threads of the day that starts at or contains
// startMs. It never waits for the model: a past day that was never grouped
// returns Grouped=false and starts its grouping in the background.
func (a *App) GetDayThreads(startMs, endMs int64) (perfValue ActivityDayThreads, perfErr error) {
	defer logPageBinding("GetDayThreads", time.Now(), &perfValue)
	defer func() { a.secondPersonThreads(&perfValue) }()
	empty := ActivityDayThreads{Grouped: true, Threads: []ActivityThreadView{}}
	if a == nil || a.activity == nil || a.activity.store == nil {
		return empty, nil
	}
	_ = endMs // Bounds are always the local calendar day, including DST.
	ctx := context.Background()
	store := a.activity.store
	day := startOfDayLocal(startMs)
	today := startOfDayLocal(time.Now().UnixMilli())
	if day > today {
		return empty, nil
	}
	groupedAt, modelAt, err := store.activityThreadDay(ctx, day)
	if err != nil {
		return empty, err
	}
	if day < today && groupedAt == 0 && time.Now().UnixMilli()-modelAt >= activityThreadModelGap.Milliseconds() {
		a.groupPastDayOnce(day)
	}
	value, err := a.dayThreadsCached(ctx, day)
	if err != nil {
		return empty, err
	}
	// Membership belongs to the start day. Carry overlapping threads into the
	// next day's read without moving their members or inventing a new thread.
	var carrySpent map[string]int64
	if len(value.carryDays) > 0 {
		carrySpent, _, err = a.activityMomentTimeMs(ctx, day, activityThreadDayEnd(day))
		if err != nil {
			return empty, err
		}
		value.Threads = append([]ActivityThreadView(nil), value.Threads...)
	}
	for _, origin := range value.carryDays {
		prior, err := a.dayThreadsCached(ctx, origin)
		if err != nil {
			return empty, err
		}
		var carried []ActivityThreadView
		for _, thread := range prior.Threads {
			if thread.EndMs > day && thread.StartMs < activityThreadDayEnd(day) {
				thread.ActiveMs = 0
				for _, id := range thread.ActivityIDs {
					thread.ActiveMs += carrySpent[id]
				}
				thread.TimeSpentMs = thread.ActiveMs
				carried = append(carried, thread)
			}
		}
		if len(carried) == 0 {
			continue
		}
		// The prior day's stretches end at its midnight; redraw this day's.
		if err := a.carryActivityThreadStretches(ctx, day, origin, carried, carrySpent, value.Threads); err != nil {
			return empty, err
		}
		value.Threads = append(value.Threads, carried...)
	}
	value.Grouped = day == today || groupedAt > 0
	return value, nil
}

func activityThreadCacheForget(day int64) {
	activityThreadCacheMu.Lock()
	for key := range activityThreadCache {
		if key.day == day {
			delete(activityThreadCache, key)
		}
	}
	activityThreadCacheMu.Unlock()
}

func (a *App) dayThreadsCached(ctx context.Context, day int64) (ActivityDayThreads, error) {
	store := a.activity.store
	signature, cacheable, signatureErr := a.activityThreadProjectionSignature(ctx, day)
	key := activityThreadCacheKey{store: store, day: day}
	activityThreadCacheMu.Lock()
	entry, ok := activityThreadCache[key]
	activityThreadCacheMu.Unlock()
	if signatureErr == nil && cacheable && ok && entry.signature == signature && (day < startOfDayLocal(time.Now().UnixMilli()) || time.Since(entry.at) < activityThreadCacheTTL) {
		return entry.value, nil
	}
	value, err := a.buildDayThreads(ctx, day)
	if err != nil {
		return ActivityDayThreads{}, err
	}
	if signatureErr == nil {
		if err := json.Unmarshal([]byte(signature.carryDays), &value.carryDays); err != nil {
			return ActivityDayThreads{}, err
		}
	} else {
		// An unavailable cache proof must not hide a carry-in. Preserve the
		// uncached read path, while healthy warm reads scan no activity rows.
		rows, err := store.GetActivitiesInRangeContext(ctx, day, activityThreadDayEnd(day)-1)
		if err != nil {
			return ActivityDayThreads{}, err
		}
		seen := map[int64]bool{}
		for _, row := range rows {
			if row.DayStartMs < day && row.EndMs > day && !seen[row.DayStartMs] {
				value.carryDays = append(value.carryDays, row.DayStartMs)
				seen[row.DayStartMs] = true
			}
		}
	}
	after, cacheableAfter, err := a.activityThreadProjectionSignature(ctx, day)
	if signatureErr == nil && err == nil && cacheable && cacheableAfter && after == signature {
		activityThreadCacheMu.Lock()
		if len(activityThreadCache) >= 64 {
			for old := range activityThreadCache {
				delete(activityThreadCache, old)
				break
			}
		}
		activityThreadCache[key] = activityThreadCacheEntry{signature: signature, at: time.Now(), value: value}
		activityThreadCacheMu.Unlock()
	}
	return value, nil
}

type activityThreadDayRead struct {
	ActivityDayThreads
	threads []activityThreadRow
	members []activityThreadMember
	rows    []ActivityRow
	spent   map[string]int64
}

// buildActivityThreadDay retains the base read and time allocation. Additive
// presentation details use this same already-filtered snapshot.
func (a *App) buildActivityThreadDay(ctx context.Context, day int64) (activityThreadDayRead, error) {
	store := a.activity.store
	out := activityThreadDayRead{ActivityDayThreads: ActivityDayThreads{Threads: []ActivityThreadView{}}}
	if agents, err := store.agentRunDay(ctx, day); err == nil {
		out.Agents = agents
	}
	threads, members, err := store.loadActivityThreads(ctx, day)
	out.threads, out.members = threads, members
	if err != nil || len(members) == 0 {
		return out, err
	}
	rows, err := store.dayActivityRows(ctx, day)
	if err != nil {
		return out, err
	}
	memberSet := map[string]bool{}
	for _, m := range members {
		memberSet[m.ActivityID] = true
	}
	// Privacy can change after grouping: only moments that still pass the
	// model-read filter appear in a thread, and only they can lend a title.
	readableRows := a.filterComposerModelCandidates(store, rows)
	out.rows = readableRows
	live := map[string]ActivityRow{}
	for _, row := range readableRows {
		if memberSet[row.ID] {
			live[row.ID] = row
		}
	}
	spent, _, err := a.activityMomentTimeMs(ctx, day, activityThreadDayEnd(day))
	out.spent = spent
	if err != nil {
		return out, err
	}
	byThread := map[string][]ActivityRow{}
	for _, m := range members {
		if row, ok := live[m.ActivityID]; ok {
			byThread[m.ThreadID] = append(byThread[m.ThreadID], row)
		}
	}
	for _, thread := range threads {
		moments := byThread[thread.ID]
		if len(moments) == 0 {
			continue
		}
		sort.SliceStable(moments, func(i, j int) bool { return moments[i].StartMs < moments[j].StartMs })
		view := ActivityThreadView{ID: thread.ID, Kind: thread.Kind, StartMs: moments[0].StartMs, ActivityIDs: make([]string, 0, len(moments))}
		for _, moment := range moments {
			view.ActivityIDs = append(view.ActivityIDs, moment.ID)
			view.EndMs = max(view.EndMs, moment.EndMs)
			view.TimeSpentMs += spent[moment.ID]
		}
		view.ActiveMs, view.Reviewed = view.TimeSpentMs, thread.UserConfirmed
		view.Title, view.UserTitled = activityThreadDisplayTitle(thread, live, moments[0].Title)
		out.Threads = append(out.Threads, view)
	}
	sort.SliceStable(out.Threads, func(i, j int) bool { return out.Threads[i].StartMs < out.Threads[j].StartMs })
	return out, nil
}

func (a *App) buildDayThreads(ctx context.Context, day int64) (ActivityDayThreads, error) {
	base, err := a.buildActivityThreadDay(ctx, day)
	out := base.ActivityDayThreads
	if err != nil {
		return out, err
	}
	if err := a.addActivityThreadStretchDetails(ctx, out.Threads, base.members, base.rows, base.spent, base.threads, day, activityThreadDayEnd(day)); err != nil {
		return ActivityDayThreads{}, err
	}
	return out, nil
}

// activityThreadDisplayTitle uses the user's title, then the model's title
// while every moment it was written from is still present and readable, then
// the first moment's title.
func activityThreadDisplayTitle(thread activityThreadRow, live map[string]ActivityRow, fallback string) (string, bool) {
	// A redaction tag never reaches a screen, in a title saved before the
	// producer stopped writing them as well as in new ones (AR62).
	if title := activityPrivacyMarkerStrip(thread.UserTitle); strings.TrimSpace(title) != "" {
		return title, true
	}
	if title := activityPrivacyMarkerStrip(thread.Title); strings.TrimSpace(title) != "" {
		ok := true
		for _, id := range thread.TitleBasis {
			if _, present := live[id]; !present {
				ok = false
				break
			}
		}
		if ok {
			return title, false
		}
	}
	return activityPrivacyMarkerStrip(fallback), false
}

// MoveActivityToThread moves a moment into another thread of its day, or into
// a thread of its own when threadID is empty. The move is a correction: no
// later grouping changes it, and the model sees it as an example.
func (a *App) MoveActivityToThread(activityID, threadID string) (string, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return "", errors.New("activity store unavailable")
	}
	store := a.activity.store
	ctx := context.Background()
	activityThreadsMu.Lock()
	defer activityThreadsMu.Unlock()
	row, err := store.GetActivityByID(activityID)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", errors.New("moment not found")
	}
	day := row.DayStartMs
	nowMs := time.Now().UnixMilli()
	var from string
	if err := store.reader().QueryRowContext(ctx, `SELECT thread_id FROM activity_thread_members WHERE activity_id = ?`, activityID).Scan(&from); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	fromMembers := []string{}
	if from != "" {
		memberRows, err := store.reader().QueryContext(ctx, `SELECT activity_id FROM activity_thread_members WHERE thread_id = ? AND activity_id != ?`, from, activityID)
		if err != nil {
			return "", err
		}
		for memberRows.Next() {
			var id string
			if memberRows.Scan(&id) == nil {
				fromMembers = append(fromMembers, id)
			}
		}
		if err := firstErr(memberRows.Err(), memberRows.Close()); err != nil {
			return "", err
		}
	}
	fromMembersJSON, _ := json.Marshal(fromMembers)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	kind := "move"
	target := strings.TrimSpace(threadID)
	if target == "" {
		kind = "own"
		target = threadIDForMoment(activityID)
		if target == from {
			target = fmt.Sprintf("th_%s_%d", activityID, nowMs)
		}
		threadKind := threadKindWork
		_ = tx.QueryRowContext(ctx, `SELECT kind FROM activity_threads WHERE id = ?`, from).Scan(&threadKind)
		if err := insertActivityThread(ctx, tx, target, day, "", nil, threadKind, nowMs); err != nil {
			return "", err
		}
		// A thread the user split off takes no sure joins: the moments it
		// left share its objects, and the user said they belong apart.
		if _, err := tx.ExecContext(ctx, `UPDATE activity_threads SET origin = ? WHERE id = ?`, threadSourceUser, target); err != nil {
			return "", err
		}
	} else {
		var targetDay int64
		if err := tx.QueryRowContext(ctx, `SELECT day_start_ms FROM activity_threads WHERE id = ?`, target).Scan(&targetDay); err != nil {
			return "", fmt.Errorf("thread not found: %w", err)
		}
		if targetDay != day {
			return "", errors.New("a moment can only move to a thread of its own day")
		}
	}
	if err := upsertActivityThreadMember(ctx, tx, activityID, target, day, threadSourceUser, kind, true, nowMs); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO activity_thread_corrections(id, activity_id, kind, from_thread_id, from_members, to_thread_id, created_at) VALUES(?,?,?,?,?,?,?)`,
		fmt.Sprintf("thc_%s_%d", activityID, nowMs), activityID, kind, from, string(fromMembersJSON), target, nowMs); err != nil {
		return "", err
	}
	// The source thread's model title no longer matches its moments.
	if from != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE activity_threads SET title = '', title_basis = '[]', updated_at = ? WHERE id = ? AND user_title = ''`, nowMs, from); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	activityThreadCacheForget(day)
	a.emitBrowserEvent(activityThreadsUpdatedEvent)
	return target, nil
}

// RenameActivityThread sets the user's own title for a thread. An empty title
// returns the thread to its generated title.
func (a *App) RenameActivityThread(threadID, title string) error {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return errors.New("activity store unavailable")
	}
	store := a.activity.store
	activityThreadsMu.Lock()
	defer activityThreadsMu.Unlock()
	title = threadPromptText(strings.Join(strings.Fields(title), " "), threadTitleMaxRunes)
	nowMs := time.Now().UnixMilli()
	var day int64
	if err := store.reader().QueryRow(`SELECT day_start_ms FROM activity_threads WHERE id = ?`, threadID).Scan(&day); err != nil {
		return fmt.Errorf("thread not found: %w", err)
	}
	if _, err := store.db.Exec(`UPDATE activity_threads SET user_title = ?, updated_at = ? WHERE id = ?`, title, nowMs, threadID); err != nil {
		return err
	}
	if _, err := store.db.Exec(`INSERT INTO activity_thread_corrections(id, activity_id, kind, from_thread_id, to_thread_id, created_at) VALUES(?,?,?,?,?,?)`,
		fmt.Sprintf("thc_%s_%d", threadID, time.Now().UnixNano()), "", "rename", threadID, threadID, nowMs); err != nil {
		return err
	}
	activityThreadCacheForget(day)
	a.emitBrowserEvent(activityThreadsUpdatedEvent)
	return nil
}

// RegroupActivityDay drops the day's generated grouping and groups it again.
// User moves and titles stay.
func (a *App) RegroupActivityDay(dayMs int64) error {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return errors.New("activity store unavailable")
	}
	store := a.activity.store
	day := startOfDayLocal(dayMs)
	activityThreadsMu.Lock()
	_, err := store.db.Exec(`DELETE FROM activity_thread_members WHERE day_start_ms = ? AND source != 'user'`, day)
	if err == nil {
		_, err = store.db.Exec(`DELETE FROM activity_threads WHERE day_start_ms = ? AND user_title = ''
			AND id NOT IN (SELECT thread_id FROM activity_thread_members WHERE day_start_ms = ?)`, day, day)
	}
	if err == nil {
		_, err = store.db.Exec(`UPDATE activity_thread_days SET grouped_at = 0, model_at = 0 WHERE day_start_ms = ?`, day)
	}
	activityThreadsMu.Unlock()
	if err != nil {
		return err
	}
	activityThreadCacheForget(day)
	_, err = a.runActivityThreadPass(context.Background(), day, a.activityThreadsCanUseModel(), time.Now())
	if err == nil {
		a.emitBrowserEvent(activityThreadsUpdatedEvent)
	}
	return err
}
