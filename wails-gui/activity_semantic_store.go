package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SemanticEventRow is a persisted semantic event — one coherent piece of work
// that may span multiple apps and jump in time. It is the durable form of the
// event-model layer and lives alongside (never replacing) activity_sessions.
//
// JSON-typed columns (Entities/Apps/SourceRefs/ThumbnailRefs) are stored as raw
// JSON strings; callers marshal/unmarshal at the boundary. SourceRefs is the
// many-to-many link to raw observations: {"snapshot_ids":[...],"session_ids":[...]}.
type SemanticEventRow struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	Category         string `json:"category"` // build / connect / explore / sustain
	Entities         string `json:"entities"` // JSON array of {type,name} (internal stitching signal)
	Keywords         string `json:"keywords"` // JSON array of display tag strings (curated, capped)
	Apps             string `json:"apps"`     // JSON array of app names
	StartMs          int64  `json:"startMs"`
	EndMs            int64  `json:"endMs"`
	SourceRefs       string `json:"sourceRefs"`    // JSON object {snapshot_ids,session_ids}
	ThumbnailRefs    string `json:"thumbnailRefs"` // JSON array
	TaskWorthy       bool   `json:"taskWorthy"`
	CategoryDotColor string `json:"categoryDotColor"`
	Status           string `json:"status"` // open / settled
	CreatedAt        int64  `json:"createdAt"`
	UpdatedAt        int64  `json:"updatedAt"`
}

// semanticCategoryDotColor maps a semantic category to a timeline dot color.
// Stored redundantly on the row so the front-end never has to recompute it.
// Mirrors AirJelly's l1_scene -> dot-color mapping; P2 UI may retune these.
func semanticCategoryDotColor(category string) string {
	switch category {
	case "build":
		return "#22c55e" // green — creative output
	case "connect":
		return "#3b82f6" // blue — interpersonal communication
	case "explore":
		return "#eab308" // amber — information acquisition
	case "sustain":
		return "#9ca3af" // gray — maintenance / ops
	default:
		return "#9ca3af"
	}
}

const semanticEventColumns = `id, title, description, category, entities, apps,
	start_ms, end_ms, source_refs, thumbnail_refs, task_worthy,
	category_dot_color, status, created_at, updated_at, display_keywords`

// InsertSemanticEvent persists a new semantic event row.
func (s *ActivityStore) InsertSemanticEvent(ev SemanticEventRow) error {
	taskWorthy := 0
	if ev.TaskWorthy {
		taskWorthy = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO activity_semantic_events (`+semanticEventColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.Title, ev.Description, ev.Category, nonEmptyJSONArray(ev.Entities),
		nonEmptyJSONArray(ev.Apps), ev.StartMs, ev.EndMs, nonEmptyJSONObject(ev.SourceRefs),
		nonEmptyJSONArray(ev.ThumbnailRefs), taskWorthy,
		ev.CategoryDotColor, defaultStatus(ev.Status), ev.CreatedAt, ev.UpdatedAt,
		nonEmptyJSONArray(ev.Keywords),
	)
	if err != nil {
		return fmt.Errorf("insert semantic_event: %w", err)
	}
	return nil
}

// UpdateSemanticEvent overwrites a semantic event row by id (used by stitching).
func (s *ActivityStore) UpdateSemanticEvent(ev SemanticEventRow) error {
	taskWorthy := 0
	if ev.TaskWorthy {
		taskWorthy = 1
	}
	res, err := s.db.Exec(`
		UPDATE activity_semantic_events
		SET title=?, description=?, category=?, entities=?, apps=?,
			start_ms=?, end_ms=?, source_refs=?, thumbnail_refs=?, task_worthy=?,
			category_dot_color=?, status=?, updated_at=?, display_keywords=?
		WHERE id=?`,
		ev.Title, ev.Description, ev.Category, nonEmptyJSONArray(ev.Entities),
		nonEmptyJSONArray(ev.Apps), ev.StartMs, ev.EndMs, nonEmptyJSONObject(ev.SourceRefs),
		nonEmptyJSONArray(ev.ThumbnailRefs), taskWorthy,
		ev.CategoryDotColor, defaultStatus(ev.Status), ev.UpdatedAt, nonEmptyJSONArray(ev.Keywords), ev.ID,
	)
	if err != nil {
		return fmt.Errorf("update semantic_event: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("update semantic_event: id %q not found", ev.ID)
	}
	return nil
}

// DeleteSemanticEvent removes one extracted semantic event. Source snapshots
// and activity sessions are intentionally left intact.
func (s *ActivityStore) DeleteSemanticEvent(id string) error {
	if id == "" {
		return fmt.Errorf("delete semantic_event: id is required")
	}
	res, err := s.db.Exec(`DELETE FROM activity_semantic_events WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete semantic_event %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("delete semantic_event: id %q not found", id)
	}
	return nil
}

// GetOpenSemanticEvents returns events still eligible for stitching
// (status='open'), most recent end_ms first. Hits idx_semantic_events_open_end.
func (s *ActivityStore) GetOpenSemanticEvents() ([]SemanticEventRow, error) {
	rows, err := s.db.Query(`
		SELECT ` + semanticEventColumns + `
		FROM activity_semantic_events
		WHERE status = 'open'
		ORDER BY end_ms DESC`)
	if err != nil {
		return nil, fmt.Errorf("query open semantic_events: %w", err)
	}
	defer rows.Close()
	return scanSemanticEvents(rows)
}

// GetSemanticEventsInRange returns events overlapping [startMs, endMs],
// most recent first. Used by the (P2) event view and by acceptance tooling.
func (s *ActivityStore) GetSemanticEventsInRange(startMs, endMs int64) ([]SemanticEventRow, error) {
	// endMs <= 0 means "no upper bound": the 'today'/'last7'/'all' time presets
	// pass endMs=0 (matching GetFilteredSessions). Without this, the start_ms <= 0
	// predicate matches nothing and the event view renders empty.
	if endMs <= 0 {
		endMs = 1 << 62
	}
	rows, err := s.db.Query(`
		SELECT `+semanticEventColumns+`
		FROM activity_semantic_events
		WHERE start_ms <= ? AND end_ms >= ?
		-- Sort by end_ms (last activity), not start_ms: events stay open and get
		-- merged/extended over time, so an event you're actively working on should
		-- surface at the top even though it first appeared earlier (its start_ms is
		-- old but its end_ms just advanced).
		ORDER BY end_ms DESC`, endMs, startMs)
	if err != nil {
		return nil, fmt.Errorf("query semantic_events in range: %w", err)
	}
	defer rows.Close()
	return scanSemanticEvents(rows)
}

// SettleEventsBeforeDay closes out every open event that did not start today
// (start_ms < dayStartMs). Events never span days, so each new day starts fresh
// and yesterday's open events are sealed.
func (s *ActivityStore) SettleEventsBeforeDay(dayStartMs, nowMs int64) (int, error) {
	res, err := s.db.Exec(`
		UPDATE activity_semantic_events
		SET status = 'settled', updated_at = ?
		WHERE status = 'open' AND start_ms < ?`, nowMs, dayStartMs)
	if err != nil {
		return 0, fmt.Errorf("settle events before day: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetSessionTriageStatus returns the triage_status of a single session, used to
// gate the semantic-event bypass on terminal triage state.
func (s *ActivityStore) GetSessionTriageStatus(id string) (string, error) {
	var status string
	if err := s.db.QueryRow(
		`SELECT triage_status FROM activity_sessions WHERE id = ?`, id,
	).Scan(&status); err != nil {
		return "", fmt.Errorf("query triage status for %s: %w", id, err)
	}
	return status, nil
}

func scanSemanticEvents(rows *sql.Rows) ([]SemanticEventRow, error) {
	var out []SemanticEventRow
	for rows.Next() {
		var ev SemanticEventRow
		var taskWorthy int
		if err := rows.Scan(&ev.ID, &ev.Title, &ev.Description, &ev.Category,
			&ev.Entities, &ev.Apps, &ev.StartMs, &ev.EndMs, &ev.SourceRefs,
			&ev.ThumbnailRefs, &taskWorthy, &ev.CategoryDotColor, &ev.Status,
			&ev.CreatedAt, &ev.UpdatedAt, &ev.Keywords); err != nil {
			return nil, fmt.Errorf("scan semantic_event: %w", err)
		}
		ev.TaskWorthy = taskWorthy != 0
		out = append(out, ev)
	}
	return out, rows.Err()
}

// SemanticTimelineFrame is one source observation of an event for the expanded
// inner timeline: a moment with its app and focus-cropped thumbnail path.
type SemanticTimelineFrame struct {
	Timestamp int64   `json:"timestamp"`
	App       string  `json:"app"`
	Path      string  `json:"path"`
	FocusX    float64 `json:"focusX"`
	FocusY    float64 `json:"focusY"`
	FocusW    float64 `json:"focusW"`
	FocusH    float64 `json:"focusH"`
}

// GetSemanticEventTimeline returns every source observation of an event in time
// order, backing the expandable inner timeline view.
func (s *ActivityStore) GetSemanticEventTimeline(eventID string) ([]SemanticTimelineFrame, error) {
	var refsJSON string
	if err := s.db.QueryRow("SELECT source_refs FROM activity_semantic_events WHERE id = ?", eventID).Scan(&refsJSON); err != nil {
		return nil, fmt.Errorf("query event source_refs: %w", err)
	}
	var refs semanticSourceRefs
	if refsJSON != "" && refsJSON != "null" {
		_ = json.Unmarshal([]byte(refsJSON), &refs)
	}
	if len(refs.SnapshotIDs) == 0 {
		return []SemanticTimelineFrame{}, nil
	}

	// App badge per frame: prefer source_refs.SnapshotApps (the RealApp resolved
	// once at extraction time, index-aligned with SnapshotIDs). This is the fast
	// path — it avoids the old hot loop of reading and JSON-parsing each
	// snapshot's parsed_json (the FULL lossless ParsedContent: OCR/AX/UIA text,
	// often tens of KB) just to recover the app name.
	appByID := make(map[int64]string, len(refs.SnapshotIDs))
	needParse := false
	for i, id := range refs.SnapshotIDs {
		if i < len(refs.SnapshotApps) && refs.SnapshotApps[i] != "" {
			appByID[id] = refs.SnapshotApps[i]
		} else {
			// Legacy/partial rows predating SnapshotApps: we must still parse
			// parsed_json to recover the app from its bundleId — that is how the
			// old code corrected a wrong/empty raw owner (e.g. bundleId resolves
			// to "Slack" where the raw app was "Discord" or blank). Skipping it
			// would regress historical timelines.
			needParse = true
		}
	}

	// Batch-load the timeline columns in ONE query (was an N+1 of per-frame point
	// queries). parsed_json is selected only when some frame lacks SnapshotApps.
	placeholders := make([]string, len(refs.SnapshotIDs))
	args := make([]any, len(refs.SnapshotIDs))
	for i, id := range refs.SnapshotIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	cols := `id, timestamp, COALESCE(app, ''), file_path,
		COALESCE(focus_rect_x, 0), COALESCE(focus_rect_y, 0),
		COALESCE(focus_rect_w, 0), COALESCE(focus_rect_h, 0)`
	if needParse {
		cols += `, COALESCE(parsed_json, '')`
	}
	rows, err := s.db.Query(`SELECT `+cols+
		` FROM activity_snapshots WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("query timeline snapshots: %w", err)
	}
	defer rows.Close()
	frames := make([]SemanticTimelineFrame, 0, len(refs.SnapshotIDs))
	for rows.Next() {
		var f SemanticTimelineFrame
		var id int64
		var rawApp, parsedJSON string
		dest := []any{&id, &f.Timestamp, &rawApp, &f.Path, &f.FocusX, &f.FocusY, &f.FocusW, &f.FocusH}
		if needParse {
			dest = append(dest, &parsedJSON)
		}
		if err := rows.Scan(dest...); err != nil {
			continue
		}
		if app := appByID[id]; app != "" {
			f.App = app
		} else {
			// Same resolution the old code used (bundleId → canonical name, else
			// raw owner). parsedJSON is "" when this frame had no parsed content.
			f.App = resolveRealApp(parseSnapshotContent(ActivitySnapshotRow{ParsedJSON: parsedJSON}), rawApp)
		}
		frames = append(frames, f)
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].Timestamp < frames[j].Timestamp })
	return frames, nil
}

// SemanticKeywordFrame is a keyword-matched source frame plus HOW it matched.
type SemanticKeywordFrame struct {
	SemanticTimelineFrame
	// Visual is true when a keyword was found in the frame's capture-time OCR
	// text — the word was visually on screen, so the on-demand highlight OCR
	// can almost certainly box it. False means the match came only from
	// ax_text / parsed content, which cover off-viewport material (scrolled
	// page body, other tab titles), so the frame's pixels may show nothing.
	Visual bool `json:"visual"`
}

// GetSemanticEventKeywordFrames returns the event's source observations whose
// stored text — capture-time OCR (activity_ocr_frames), accessibility text, or
// parsed app content — contains any of the given keywords. Matching mirrors
// GetOCRHighlightBoxes (case-insensitive substring after removeCJKSpaces) so
// "which frames match" and "where on the frame" never disagree on semantics.
// Visual (OCR-text) matches sort first, then by timestamp, so consumers that
// promote frames surface the ones that can actually draw highlight boxes.
//
// This is a relevance signal, not ground truth: the OCR worker samples frames,
// so a frame without stored text is reported as non-matching even though an
// on-demand OCR might find the keyword. Deliberately NO sibling-OCR fallback
// here (unlike GetOCRTextBySnapshot) — borrowing a neighbor's text would mark
// frames that never showed the keyword.
func (s *ActivityStore) GetSemanticEventKeywordFrames(eventID string, keywords []string) ([]SemanticKeywordFrame, error) {
	norm := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		k := strings.ToLower(removeCJKSpaces(strings.TrimSpace(kw)))
		if k != "" {
			norm = append(norm, k)
		}
	}
	if len(norm) == 0 {
		return []SemanticKeywordFrame{}, nil
	}

	var refsJSON string
	if err := s.db.QueryRow("SELECT source_refs FROM activity_semantic_events WHERE id = ?", eventID).Scan(&refsJSON); err != nil {
		return nil, fmt.Errorf("query event source_refs: %w", err)
	}
	var refs semanticSourceRefs
	if refsJSON != "" && refsJSON != "null" {
		_ = json.Unmarshal([]byte(refsJSON), &refs)
	}
	if len(refs.SnapshotIDs) == 0 {
		return []SemanticKeywordFrame{}, nil
	}

	// App badge fast path only (index-aligned SnapshotApps). The legacy
	// parsed_json bundleId correction GetSemanticEventTimeline performs is
	// skipped here: consumers key matched frames by Path, the App field is
	// informational.
	appByID := make(map[int64]string, len(refs.SnapshotIDs))
	for i, id := range refs.SnapshotIDs {
		if i < len(refs.SnapshotApps) && refs.SnapshotApps[i] != "" {
			appByID[id] = refs.SnapshotApps[i]
		}
	}

	placeholders := make([]string, len(refs.SnapshotIDs))
	args := make([]any, len(refs.SnapshotIDs))
	for i, id := range refs.SnapshotIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	in := strings.Join(placeholders, ",")

	// Capture-time OCR text lives in activity_ocr_frames. Its segment_id is
	// OVERLOADED (see OCRFrameRow in activity_store.go): snapshot-path rows
	// store activity_snapshots.id, legacy video-segment rows store
	// activity_segments.id — a different table's sequence, so a bare
	// `segment_id IN (snapshot ids)` could attach an unrelated session's
	// legacy OCR text to a numerically-colliding snapshot. Join through
	// activity_snapshots on (session_id, segment_id=snapshot id) — the same
	// keying GetOCRTextBySnapshot uses. Legacy segment-based OCR rows are
	// deliberately not resolved: semantic events are snapshot-backed.
	ocrBySnapshot := make(map[int64][]string)
	ocrRows, err := s.db.Query(
		`SELECT s.id, o.text
		 FROM activity_ocr_frames o
		 JOIN activity_snapshots s ON o.session_id = s.session_id AND o.segment_id = s.id
		 WHERE s.id IN (`+in+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("query ocr frames: %w", err)
	}
	defer ocrRows.Close()
	for ocrRows.Next() {
		var id int64
		var text string
		if err := ocrRows.Scan(&id, &text); err != nil {
			continue
		}
		if text != "" {
			ocrBySnapshot[id] = append(ocrBySnapshot[id], text)
		}
	}
	// A mid-iteration failure would silently misclassify frames as
	// non-visual (partial OCR map), so surface it instead.
	if err := ocrRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ocr frames: %w", err)
	}

	// First pass deliberately does NOT select parsed_json: that column is the
	// FULL lossless ParsedContent (often tens of KB per frame), and most
	// matches resolve from capture-time OCR text or ax_text alone. Frames
	// undecided after this pass get parsed_json loaded in a second, narrower
	// query below — so a 76-frame event whose matches are all visual never
	// reads megabytes of JSON it would throw away.
	rows, err := s.db.Query(`SELECT id, timestamp, COALESCE(app, ''), file_path,
		COALESCE(focus_rect_x, 0), COALESCE(focus_rect_y, 0),
		COALESCE(focus_rect_w, 0), COALESCE(focus_rect_h, 0),
		COALESCE(ax_text, '')
		FROM activity_snapshots WHERE id IN (`+in+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("query keyword-frame snapshots: %w", err)
	}
	defer rows.Close()

	containsAny := func(hay string) bool {
		for _, kw := range norm {
			if strings.Contains(hay, kw) {
				return true
			}
		}
		return false
	}
	frames := make([]SemanticKeywordFrame, 0, 4)
	type pendingFrame struct {
		frame SemanticKeywordFrame
		id    int64
	}
	var pending []pendingFrame
	for rows.Next() {
		var f SemanticKeywordFrame
		var id int64
		var rawApp, axText string
		if err := rows.Scan(&id, &f.Timestamp, &rawApp, &f.Path,
			&f.FocusX, &f.FocusY, &f.FocusW, &f.FocusH, &axText); err != nil {
			continue
		}
		if app := appByID[id]; app != "" {
			f.App = app
		} else {
			f.App = rawApp
		}
		// Visual = found in what was visibly on screen (capture-time OCR);
		// ax/parsed match alone means the word may sit outside the frame's
		// visible area.
		f.Visual = containsAny(strings.ToLower(removeCJKSpaces(strings.Join(ocrBySnapshot[id], "\n"))))
		if f.Visual || containsAny(strings.ToLower(removeCJKSpaces(axText))) {
			frames = append(frames, f)
			continue
		}
		pending = append(pending, pendingFrame{frame: f, id: id})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate keyword-frame snapshots: %w", err)
	}

	// Second pass: parsed_json only for the frames still undecided.
	if len(pending) > 0 {
		// User-visible text of the parsed content only. Substring-matching the
		// raw JSON would also hit structural keys ("sender", "title", ...) and
		// metadata values (bundleId, contentHash hex), marking frames for
		// keywords that never appeared in actual content.
		flattenParsed := func(parsedJSON string) string {
			if parsedJSON == "" {
				return ""
			}
			pc := parseSnapshotContent(ActivitySnapshotRow{ParsedJSON: parsedJSON})
			if pc == nil {
				return ""
			}
			parts := []string{pc.Title, pc.Body, pc.URL, pc.Channel}
			for _, m := range pc.Messages {
				parts = append(parts, m.Sender, m.Content)
			}
			return strings.Join(parts, "\n")
		}
		ph := make([]string, len(pending))
		pargs := make([]any, len(pending))
		for i, p := range pending {
			ph[i] = "?"
			pargs[i] = p.id
		}
		prows, err := s.db.Query(`SELECT id, COALESCE(parsed_json, '')
			FROM activity_snapshots WHERE id IN (`+strings.Join(ph, ",")+`)`, pargs...)
		if err != nil {
			return nil, fmt.Errorf("query keyword-frame parsed content: %w", err)
		}
		defer prows.Close()
		parsedMatched := make(map[int64]bool, len(pending))
		for prows.Next() {
			var id int64
			var parsedJSON string
			if err := prows.Scan(&id, &parsedJSON); err != nil {
				continue
			}
			if containsAny(strings.ToLower(removeCJKSpaces(flattenParsed(parsedJSON)))) {
				parsedMatched[id] = true
			}
		}
		if err := prows.Err(); err != nil {
			return nil, fmt.Errorf("iterate keyword-frame parsed content: %w", err)
		}
		for _, p := range pending {
			if parsedMatched[p.id] {
				frames = append(frames, p.frame)
			}
		}
	}
	sort.Slice(frames, func(i, j int) bool {
		if frames[i].Visual != frames[j].Visual {
			return frames[i].Visual
		}
		return frames[i].Timestamp < frames[j].Timestamp
	})
	return frames, nil
}

// GetSemanticPendingSessions returns sessions whose semantic_status is 'pending'
// — the independent track the event layer consumes, decoupled from triage_status
// and triage cooldown (EXCEPT the privacy terminal state: sessions marked
// "protected" touched a protected app and must not be consumed by the semantic
// loop either — its WeChat vision pass writes senders to the Who you know page).
// Includes the meeting columns the preprocessor needs.
func (s *ActivityStore) GetSemanticPendingSessions() ([]ActivitySessionRow, error) {
	rows, err := s.db.Query(`
		SELECT id, start_time, end_time, trigger_kind, app_names,
			event_count, segment_count, snapshot_count, total_size_bytes, triage_status,
			COALESCE(is_meeting, 0), COALESCE(meeting_app, ''), COALESCE(transcript_path, '')
		FROM activity_sessions
		WHERE COALESCE(semantic_status, 'pending') = 'pending' AND ` + sqlSessionHasNoPrivacyHold + `
		AND COALESCE(triage_status, '') != 'protected'
		ORDER BY start_time ASC`)
	if err != nil {
		return nil, fmt.Errorf("query semantic-pending sessions: %w", err)
	}
	defer rows.Close()

	var out []ActivitySessionRow
	for rows.Next() {
		var r ActivitySessionRow
		var isMeeting int
		if err := rows.Scan(&r.ID, &r.StartTime, &r.EndTime, &r.TriggerKind, &r.AppNames,
			&r.EventCount, &r.SegmentCount, &r.SnapshotCount, &r.TotalSizeBytes, &r.TriageStatus,
			&isMeeting, &r.MeetingApp, &r.TranscriptPath); err != nil {
			return nil, fmt.Errorf("scan semantic-pending session: %w", err)
		}
		r.IsMeeting = isMeeting != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkSessionsSemanticDone flips semantic_status to 'done' for the given session
// ids, so the semantic pass processes each session exactly once on success.
func (s *ActivityStore) MarkSessionsSemanticDone(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`UPDATE activity_sessions SET semantic_status = 'done' WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			return fmt.Errorf("mark %s semantic done: %w", id, err)
		}
	}
	return tx.Commit()
}

func defaultStatus(status string) string {
	if status == "" {
		return "open"
	}
	return status
}

// nonEmptyJSONArray normalizes "" and "null" (json.Marshal of a nil slice) to
// an empty array, so an all-"?" event never persists apps as the literal null.
func nonEmptyJSONArray(s string) string {
	if s == "" || s == "null" {
		return "[]"
	}
	return s
}

func nonEmptyJSONObject(s string) string {
	if s == "" || s == "null" {
		return "{}"
	}
	return s
}

// GetSnapshotFilePathByID returns the stored (servable) file path for a snapshot
// id, used to resolve an event's thumbnail refs into displayable images.
func (s *ActivityStore) GetSnapshotFilePathByID(id int64) (string, error) {
	var p string
	if err := s.db.QueryRow("SELECT file_path FROM activity_snapshots WHERE id = ?", id).Scan(&p); err != nil {
		return "", err
	}
	return p, nil
}

// GetSnapshotThumb returns a snapshot's servable path plus its normalized focus
// rect, so the event view can crop the thumbnail to the focused app region.
func (s *ActivityStore) GetSnapshotThumb(id int64) (SemanticThumbnail, error) {
	var t SemanticThumbnail
	if err := s.db.QueryRow(`SELECT file_path,
		COALESCE(focus_rect_x, 0), COALESCE(focus_rect_y, 0),
		COALESCE(focus_rect_w, 0), COALESCE(focus_rect_h, 0)
		FROM activity_snapshots WHERE id = ?`, id).Scan(
		&t.Path, &t.FocusX, &t.FocusY, &t.FocusW, &t.FocusH); err != nil {
		return SemanticThumbnail{}, err
	}
	return t, nil
}

// SemanticThumbnail is a representative snapshot for an event, with its focus
// rect (normalized 0..1) so the front-end can crop to the focused app's region
// instead of showing the whole multi-window screen. An all-zero rect means
// unknown (legacy / multi-display); the renderer then shows the full frame.
type SemanticThumbnail struct {
	Path   string  `json:"path"`
	FocusX float64 `json:"focusX"`
	FocusY float64 `json:"focusY"`
	FocusW float64 `json:"focusW"`
	FocusH float64 `json:"focusH"`
}

// SemanticEventView is the front-end-facing form of a semantic event: JSON
// columns are parsed into arrays and thumbnail snapshot ids are resolved to
// servable file paths, so the renderer needs no further parsing.
type SemanticEventView struct {
	ID            string              `json:"id"`
	Title         string              `json:"title"`
	Description   string              `json:"description"`
	Category      string              `json:"category"`
	CategoryColor string              `json:"categoryColor"`
	Entities      []Entity            `json:"entities"`
	Keywords      []string            `json:"keywords"`
	Apps          []string            `json:"apps"`
	StartMs       int64               `json:"startMs"`
	EndMs         int64               `json:"endMs"`
	Thumbnails    []SemanticThumbnail `json:"thumbnails"`
	SourceCount   int                 `json:"sourceCount"`
	TaskWorthy    bool                `json:"taskWorthy"`
	Status        string              `json:"status"`
	// UpdatedAt is bumped by every stitch/merge (which rewrites source_refs),
	// so the front-end can use it as a version key for caches derived from
	// the event's frame set (keyword-frame scans) without subscribing to
	// update events.
	UpdatedAt int64 `json:"updatedAt"`
}
