package main

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// A saved first page's cards can rest on activity sessions: a meeting, its
// notes file, the session a calendar event or a sighting came from. Two changes
// to those reach neither the privacy stamp nor the voice source counter: a
// session protected or removed outside Isoform's withdrawal path, and a notes
// file deleted on disk. So the copy names the sessions its cards' gates read
// (recorded during the projection that made it, from a cached verdict too) and
// what each one looked like then: its privacy fields and its notes file. A read
// compares them in one batched query plus a stat per notes file before the copy
// is served, so such a card never paints from the copy, and a page whose
// sources hold still paints at once.
const streamPageSourceCheckBudget = 40 * time.Millisecond

type streamPageSourceRecorderKey struct{}

type streamPageSourceRecorder struct {
	mu       sync.Mutex
	sessions map[string]bool
}

func withStreamPageSources(ctx context.Context) (context.Context, *streamPageSourceRecorder) {
	rec := &streamPageSourceRecorder{sessions: map[string]bool{}}
	return context.WithValue(ctx, streamPageSourceRecorderKey{}, rec), rec
}

func streamPageSourcesFrom(ctx context.Context) *streamPageSourceRecorder {
	rec, _ := ctx.Value(streamPageSourceRecorderKey{}).(*streamPageSourceRecorder)
	return rec
}

// recordStreamPageSessions notes the activity sessions a card gate reads. It
// does nothing outside a saved-page projection.
func recordStreamPageSessions(ctx context.Context, ids []string) {
	rec := streamPageSourcesFrom(ctx)
	if rec == nil {
		return
	}
	rec.mu.Lock()
	for _, id := range ids {
		if id != "" {
			rec.sessions[id] = true
		}
	}
	rec.mu.Unlock()
}

func (rec *streamPageSourceRecorder) ids() []string {
	if rec == nil {
		return nil
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]string, 0, len(rec.sessions))
	for id := range rec.sessions {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// streamPageSessionState is what a card gate read of one session: its privacy
// fields and its notes file. Missing means the row was gone.
type streamPageSessionState struct {
	Missing    bool   `json:"missing,omitempty"`
	Status     string `json:"status,omitempty"`
	Apps       string `json:"apps,omitempty"`
	Meeting    bool   `json:"meeting,omitempty"`
	MeetingApp string `json:"meetingApp,omitempty"`
	Start      int64  `json:"start,omitempty"`
	End        int64  `json:"end,omitempty"`
	Revision   int64  `json:"revision,omitempty"`
	Notes      string `json:"notes,omitempty"`
	NotesSize  int64  `json:"notesSize,omitempty"`
	NotesMod   int64  `json:"notesMod,omitempty"`
	NotesGone  bool   `json:"notesGone,omitempty"`
}

// streamPageSessionStates reads the state of ids now: one query, one stat per
// notes file.
func (a *App) streamPageSessionStates(ctx context.Context, ids []string) (map[string]streamPageSessionState, error) {
	out := make(map[string]streamPageSessionState, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	if a == nil || a.activity == nil || a.activity.store == nil {
		return nil, os.ErrNotExist
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := a.activity.store.db.QueryContext(ctx, `SELECT s.id,COALESCE(s.triage_status,''),COALESCE(s.app_names,''),COALESCE(s.is_meeting,0),COALESCE(s.meeting_app,''),
		s.start_time,s.end_time,COALESCE(r.revision,0),COALESCE(s.transcript_path,'')
		FROM activity_sessions s LEFT JOIN activity_evidence_source_revisions r ON r.session_id=s.id
		WHERE s.id IN (SELECT value FROM json_each(?))`, string(encoded))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var st streamPageSessionState
		if err := rows.Scan(&id, &st.Status, &st.Apps, &st.Meeting, &st.MeetingApp, &st.Start, &st.End, &st.Revision, &st.Notes); err != nil {
			return nil, err
		}
		out[id] = st
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		st, found := out[id]
		if !found {
			out[id] = streamPageSessionState{Missing: true}
			continue
		}
		if st.Notes != "" {
			if info, err := os.Stat(st.Notes); err == nil {
				st.NotesSize, st.NotesMod = info.Size(), info.ModTime().UnixNano()
			} else {
				st.NotesGone = true
			}
		}
		out[id] = st
	}
	return out, nil
}

// streamPageSourcesHold reports whether every session the copy's cards rest on
// still reads as it did when the copy was made. A check that cannot finish in
// its budget does not hold.
func (a *App) streamPageSourcesHold(saved map[string]streamPageSessionState) bool {
	if len(saved) == 0 {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), streamPageSourceCheckBudget)
	defer cancel()
	ids := make([]string, 0, len(saved))
	for id := range saved {
		ids = append(ids, id)
	}
	now, err := a.streamPageSessionStates(ctx, ids)
	if err != nil {
		return false
	}
	for id, was := range saved {
		if now[id] != was {
			return false
		}
	}
	return true
}
