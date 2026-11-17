package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/workonit"
)

// The Activity page's one timeline (computer-activity v1.0.0): everything
// Isoform saw in a stretch of time, by time and source, newest first. Each
// row carries what Forget needs; GetActivityForgetPreview says what goes with
// a row before ForgetActivityFeedRow removes it.

// ActivityFeedRow is one thing Isoform saw or did. Kind is "screen" (a
// moment), "watch" (a conversation the watch or phone heard), "agent" (a
// coding agent working on this computer), "handoff" (work your Yes sent to
// one of your agents) or "closed" (Closed Eyes: nothing was kept, so the row
// has no id and no Forget).
type ActivityFeedRow struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
	// App is the app the row happened in: the moment's main app, or the
	// agent's app ("Codex", "Claude"). Empty for watch and closed rows.
	App         string `json:"app,omitempty"`
	Title       string `json:"title,omitempty"`
	Detail      string `json:"detail,omitempty"`
	ActiveMs    int64  `json:"activeMs,omitempty"`
	Words       int    `json:"words,omitempty"`
	Device      string `json:"device,omitempty"` // watch rows: "watch" or "phone"
	InProgress  bool   `json:"inProgress,omitempty"`
	Withheld    string `json:"withheld,omitempty"`
	RecordingID string `json:"recordingId,omitempty"`
	// Hand-off rows (kind "handoff"): where the Yes was given, and what
	// Isoform knows the agent did: "sent", "taken" (it read it), "done" (it
	// came back, at DoneMs), "not_done" (it couldn't do it) or "no_word".
	Surface string `json:"surface,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	DoneMs  int64  `json:"doneMs,omitempty"`
	// Cause is why Isoform didn't see a "trouble" row: "screen",
	// "signed_out", "offline" or "restarted". Ongoing is true while it lasts.
	Cause   string `json:"cause,omitempty"`
	Ongoing bool   `json:"ongoing,omitempty"`
}

// ActivityForgetItem is one thing that goes when a row is forgotten. Kind is
// "topic" (the row leaves that topic), "goal" (a goal made from it),
// "screen" (raw screen capture of a stretch), "moment" (another moment
// whose capture shares that stretch), "words" (a transcript), "audio",
// "copies" (the phone's and the watch's copies) or "notes".
type ActivityForgetItem struct {
	Kind    string `json:"kind"`
	Title   string `json:"title,omitempty"`
	StartMs int64  `json:"startMs,omitempty"`
	EndMs   int64  `json:"endMs,omitempty"`
}

// ActivityForgetPreview is what Forget names before the person confirms.
type ActivityForgetPreview struct {
	Kind  string               `json:"kind"`
	ID    string               `json:"id"`
	Title string               `json:"title"`
	Goes  []ActivityForgetItem `json:"goes"`
	// Agent names the agent a hand-off went to: what it made stays with it.
	Agent string `json:"agent,omitempty"`
}

var errActivityFeedNotReady = errors.New("activity store not ready")

// GetActivityFeed returns the rows that start in [startMs, endMs), newest
// first. A source that fails to read is left out rather than failing the
// page: the other sources still show.
func (a *App) GetActivityFeed(startMs, endMs int64) (perfValue []ActivityFeedRow, perfErr error) {
	defer logPageBinding("GetActivityFeed", time.Now(), &perfValue)
	rows := []ActivityFeedRow{}
	if a == nil || endMs <= startMs {
		return rows, nil
	}
	if a.activity != nil && a.activity.store != nil {
		if moments, err := a.GetActivitiesInRange(startMs, endMs); err == nil {
			rows = append(rows, activityFeedMomentRows(moments)...)
		}
		if agents, err := a.activityFeedAgentRows(context.Background(), startMs, endMs); err == nil {
			rows = append(rows, agents...)
		}
	}
	if handoffs, err := a.GetWorkOnItHandoffs(); err == nil {
		rows = append(rows, activityFeedHandoffRows(handoffs, a.workOnItOutcomes(), startMs, endMs, time.Now().UnixMilli())...)
	}
	if recordings, err := a.GetWatchRecordings(); err == nil {
		rows = append(rows, activityFeedWatchRows(recordings, startMs, endMs)...)
	}
	if pauses, err := a.observationPauses(); err == nil {
		rows = append(rows, activityFeedClosedRows(pauses, startMs, endMs, time.Now().UnixMilli())...)
	}
	if stretches, err := a.troubleStretches(startMs, endMs); err == nil {
		rows = append(rows, activityFeedTroubleRows(stretches, startMs, endMs, time.Now().UnixMilli())...)
	}
	sortActivityFeed(rows)
	return rows, nil
}

func sortActivityFeed(rows []ActivityFeedRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].StartMs != rows[j].StartMs {
			return rows[i].StartMs > rows[j].StartMs
		}
		return rows[i].ID < rows[j].ID
	})
}

func activityFeedMomentRows(moments []ActivityView) []ActivityFeedRow {
	out := make([]ActivityFeedRow, 0, len(moments))
	for _, m := range moments {
		row := ActivityFeedRow{ID: m.ID, Kind: "screen", StartMs: m.StartMs, EndMs: m.EndMs, Title: strings.TrimSpace(m.Title),
			Detail: strings.TrimSpace(m.Description), ActiveMs: m.ActiveMs, InProgress: m.InProgress, Withheld: m.ActionsWithheld}
		if len(m.Apps) > 0 {
			row.App = m.Apps[0].AppName
		}
		out = append(out, row)
	}
	return out
}

func activityFeedWatchRows(recordings []WatchRecording, startMs, endMs int64) []ActivityFeedRow {
	out := []ActivityFeedRow{}
	for _, rec := range recordings {
		if rec.State == mobileRecordingDeleted {
			continue
		}
		started, err := time.Parse(time.RFC3339, rec.StartedAt)
		if err != nil {
			continue
		}
		at := started.UnixMilli()
		if at < startMs || at >= endMs {
			continue
		}
		device := "watch"
		if rec.Source == "phone" {
			device = "phone"
		}
		out = append(out, ActivityFeedRow{ID: rec.ID, Kind: "watch", StartMs: at, EndMs: at + int64(rec.DurationSeconds*1000),
			Words: rec.Words, Device: device, RecordingID: rec.ID})
	}
	return out
}

// workOnItOutcomes reads each sent task's record by id: what Isoform knows
// the agent did with it. The record keeps counts and times, never text.
func (a *App) workOnItOutcomes() map[string]workonit.Record {
	s := a.workOnIt()
	s.recordsMu.Lock()
	records, err := workOnItReadLocked()
	s.recordsMu.Unlock()
	out := map[string]workonit.Record{}
	if err != nil {
		return out
	}
	for _, r := range records {
		out[r.ID] = r
	}
	return out
}

func activityFeedHandoffRows(handoffs []workonit.Handoff, records map[string]workonit.Record, startMs, endMs, nowMs int64) []ActivityFeedRow {
	out := []ActivityFeedRow{}
	for _, h := range handoffs {
		if h.SentAt < startMs || h.SentAt >= endMs {
			continue
		}
		name := h.AgentID
		if agent, ok := workonit.AgentByID(h.AgentID); ok {
			name = agent.Name
		}
		title := strings.TrimSpace(strings.SplitN(strings.TrimSpace(h.Text), "\n", 2)[0])
		row := ActivityFeedRow{ID: h.ID, Kind: "handoff", StartMs: h.SentAt, EndMs: h.SentAt, App: name,
			Title: truncateRunes(title, 140), Surface: h.Surface, Outcome: "sent"}
		if r, ok := records[h.ID]; ok {
			switch r.Status {
			case workonit.StatusDone:
				row.Outcome, row.DoneMs = "done", r.DoneAt
			case workonit.StatusNotDone:
				row.Outcome = "not_done"
			case workonit.StatusTaken:
				row.Outcome = "taken"
			default:
				if nowMs-r.SentAt >= workonit.AskAfter.Milliseconds() {
					row.Outcome = "no_word"
				}
			}
		}
		out = append(out, row)
	}
	return out
}

// activityFeedTroubleRows are the hours trouble stopped Isoform
// (computer-trouble v1.0.0 rule 7): a gap with its cause, clipped to the
// window, with no id and no Forget, since nothing was kept.
func activityFeedTroubleRows(stretches []TroubleStretch, startMs, endMs, nowMs int64) []ActivityFeedRow {
	out := []ActivityFeedRow{}
	for _, t := range stretches {
		end, ongoing := t.EndMs, t.EndMs == 0
		if ongoing {
			end = nowMs
		}
		s, e := max(t.StartMs, startMs), min(end, endMs)
		if e <= s {
			continue
		}
		out = append(out, ActivityFeedRow{Kind: "trouble", StartMs: s, EndMs: e, Cause: t.Cause, Ongoing: ongoing && end <= endMs})
	}
	return out
}

// activityFeedClosedRows clips each Closed Eyes stretch to the window. One
// still on runs to now.
func activityFeedClosedRows(pauses []observationPause, startMs, endMs, nowMs int64) []ActivityFeedRow {
	out := []ActivityFeedRow{}
	for _, p := range pauses {
		end := p.End
		if end == 0 {
			end = nowMs
		}
		s, e := max(p.Start, startMs), min(end, endMs)
		if e <= s {
			continue
		}
		out = append(out, ActivityFeedRow{Kind: "closed", StartMs: s, EndMs: e})
	}
	return out
}

func (a *App) activityFeedAgentRows(ctx context.Context, startMs, endMs int64) ([]ActivityFeedRow, error) {
	rows, err := a.activity.store.reader().QueryContext(ctx, `SELECT r.run_id, r.start_ms, r.end_ms, r.active_ms, COALESCE(g.app_name, '')
		FROM activity_agent_runs r LEFT JOIN activity_timeline_groups g ON g.id = r.run_id
		WHERE r.start_ms >= ? AND r.start_ms < ? ORDER BY r.start_ms DESC`, startMs, endMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ActivityFeedRow{}
	for rows.Next() {
		row := ActivityFeedRow{Kind: "agent"}
		if err := rows.Scan(&row.ID, &row.StartMs, &row.EndMs, &row.ActiveMs, &row.App); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetActivityForgetPreview names what goes with a row. It changes nothing.
func (a *App) GetActivityForgetPreview(kind, id string) (ActivityForgetPreview, error) {
	preview := ActivityForgetPreview{Kind: kind, ID: id, Goes: []ActivityForgetItem{}}
	if strings.TrimSpace(id) == "" {
		return preview, fmt.Errorf("nothing to forget")
	}
	switch kind {
	case "screen":
		if a.activity == nil || a.activity.store == nil {
			return preview, errActivityFeedNotReady
		}
		row, err := a.activity.store.GetActivityByID(id)
		if err != nil {
			return preview, err
		}
		if row == nil {
			return preview, fmt.Errorf("activity %s not found", id)
		}
		preview.Title = strings.TrimSpace(row.Title)
		capture, err := a.activityForgetCapture(id)
		if err != nil {
			return preview, err
		}
		preview.Goes = append(preview.Goes, capture...)
		preview.Goes = append(preview.Goes, a.activityForgetTopics(id)...)
		goals, err := a.activityForgetGoals(id)
		if err != nil {
			return preview, err
		}
		preview.Goes = append(preview.Goes, goals...)
	case "watch":
		store := a.watchRecordingStore()
		if store == nil {
			return preview, errWatchRecordingNotFound
		}
		rec, ok := store.get(id, "")
		if !ok || rec.State == mobileRecordingDeleted {
			return preview, errWatchRecordingNotFound
		}
		if rec.Words > 0 {
			preview.Goes = append(preview.Goes, ActivityForgetItem{Kind: "words"})
		}
		if !rec.AudioRemoved {
			preview.Goes = append(preview.Goes, ActivityForgetItem{Kind: "audio"})
		}
		if rec.SessionID != "" {
			preview.Goes = append(preview.Goes, ActivityForgetItem{Kind: "notes"})
		}
		preview.Goes = append(preview.Goes, ActivityForgetItem{Kind: "copies"})
	case "handoff":
		handoffs, err := a.GetWorkOnItHandoffs()
		if err != nil {
			return preview, err
		}
		for _, row := range activityFeedHandoffRows(handoffs, nil, 0, 1<<62, 0) {
			if row.ID == id {
				preview.Title, preview.Agent = row.Title, row.App
				return preview, nil
			}
		}
		return preview, errWorkOnItNoHandoff
	case "agent":
		if a.activity == nil || a.activity.store == nil {
			return preview, errActivityFeedNotReady
		}
		spans, err := a.activityAgentRunSessions(id)
		if err != nil {
			return preview, err
		}
		for _, s := range spans {
			preview.Goes = append(preview.Goes, ActivityForgetItem{Kind: "screen", StartMs: s.StartTime, EndMs: s.EndTime})
		}
	default:
		return preview, fmt.Errorf("cannot forget a %q row", kind)
	}
	return preview, nil
}

// ForgetActivityFeedRow removes a row and what was made from it, through
// the same deletion each source already uses, so privacy withdrawal and
// the phone's copies follow as they do there.
func (a *App) ForgetActivityFeedRow(kind, id string) error {
	switch kind {
	case "screen":
		// The raw capture goes first: a failure leaves the moment in place to
		// Forget again, never a moment gone with its screens still kept.
		if a.activity == nil || a.activity.store == nil {
			return errActivityFeedNotReady
		}
		sessions, err := a.activity.store.momentCaptureSessions(id)
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if err := a.activity.store.eraseSessionCapture(session); err != nil {
				return err
			}
		}
		return a.DeleteActivity(id)
	case "watch":
		return a.DeleteWatchRecording(id)
	case "handoff":
		// Isoform's record of the hand-off goes; what the agent made stays
		// with the agent (computer-activity v1.2.0 rule 3).
		return a.ForgetWorkOnItHandoff(id)
	case "agent":
		if a.activity == nil || a.activity.store == nil {
			return errActivityFeedNotReady
		}
		spans, err := a.activityAgentRunSessions(id)
		if err != nil {
			return err
		}
		for _, s := range spans {
			if err := a.activity.store.eraseSessionCapture(s.ID); err != nil {
				return err
			}
		}
		if a.ctx != nil {
			a.emitBrowserEvent("activity:activities-updated")
		}
		return nil
	default:
		return fmt.Errorf("cannot forget a %q row", kind)
	}
}

// activityAgentRunSessions reads the capture sessions behind one agent run.
// Only runs marked as agent work qualify, so this never deletes a run of
// the person's own.
func (a *App) activityAgentRunSessions(runID string) ([]ActivitySessionRow, error) {
	var raw string
	err := a.activity.store.reader().QueryRow(`SELECT g.session_ids FROM activity_agent_runs r
		JOIN activity_timeline_groups g ON g.id = r.run_id WHERE r.run_id = ?`, runID).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("agent run %s: %w", runID, err)
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("agent run %s sessions: %w", runID, err)
	}
	out := []ActivitySessionRow{}
	for _, id := range ids {
		session, err := a.activity.store.GetSessionByID(id)
		if err != nil || session == nil {
			continue
		}
		out = append(out, *session)
	}
	return out, nil
}

// activityForgetCapture names the raw capture a moment's Forget erases: each
// stretch of screen, and any other moment that shares it.
func (a *App) activityForgetCapture(id string) ([]ActivityForgetItem, error) {
	store := a.activity.store
	sessions, err := store.momentCaptureSessions(id)
	if err != nil {
		return nil, err
	}
	out := []ActivityForgetItem{}
	for _, sid := range sessions {
		session, err := store.GetSessionByID(sid)
		if err != nil || session == nil {
			continue
		}
		out = append(out, ActivityForgetItem{Kind: "screen", StartMs: session.StartTime, EndMs: session.EndTime})
	}
	others, err := store.momentsSharingCapture(id, sessions)
	if err != nil {
		return nil, err
	}
	for _, title := range others {
		out = append(out, ActivityForgetItem{Kind: "moment", Title: title})
	}
	return out, nil
}

func (a *App) activityForgetTopics(id string) []ActivityForgetItem {
	out := []ActivityForgetItem{}
	for _, p := range a.topicsSvc().store.load().Projects {
		for _, member := range p.ActivityIDs {
			if member == id && strings.TrimSpace(p.Name) != "" {
				out = append(out, ActivityForgetItem{Kind: "topic", Title: strings.TrimSpace(p.Name)})
				break
			}
		}
	}
	return out
}

// activityForgetGoals names the goals the existing withdrawal would erase
// with this moment.
func (a *App) activityForgetGoals(id string) ([]ActivityForgetItem, error) {
	w, err := a.topicWithdrawalForSource("activity", []string{id}, nil)
	if err != nil {
		return nil, err
	}
	out := []ActivityForgetItem{}
	if len(w.goals) == 0 || a.activity == nil || a.activity.store == nil {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	details, err := NewPurposeMapStore(a.activity.store.db).ListDetailsAllContext(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range details {
		if w.goals[d.OpenLoop.ID] {
			out = append(out, ActivityForgetItem{Kind: "goal", Title: purposePayloadTitle(d.OpenLoop.PayloadJSON)})
		}
	}
	return out, nil
}

func purposePayloadTitle(payload string) string {
	var fields map[string]any
	if json.Unmarshal([]byte(payload), &fields) != nil {
		return ""
	}
	for _, key := range []string{"title", "summary", "text"} {
		if s, ok := fields[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
