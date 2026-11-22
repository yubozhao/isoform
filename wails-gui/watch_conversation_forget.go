package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/workonit"
)

var errWatchConversationGone = errors.New("this conversation is no longer kept")

type WatchConversationAudioCut struct {
	RecordingID string `json:"recordingId"`
	StartMs     int64  `json:"startMs"`
	EndMs       int64  `json:"endMs"`
}
type WatchConversationForgetItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type WatchConversationForgetPreview struct {
	ID                string                        `json:"id"`
	Name              string                        `json:"name"`
	LineCount         int                           `json:"lineCount"`
	Audio             []WatchConversationAudioCut   `json:"audio"`
	HasNote           bool                          `json:"hasNote"`
	Followups         []WatchConversationForgetItem `json:"followups"`
	Claims            []WatchConversationForgetItem `json:"claims"`
	StayingWithAgents []string                      `json:"stayingWithAgents"`
	Count             int                           `json:"count"`
}

type watchForgetHour struct {
	recording mobileRecording
	whole     bool
	events    []int64
	words     int
}

type watchForgetPlan struct {
	speech    []watchForgetSpeech
	signature [32]byte
	preview   WatchConversationForgetPreview
	hours     []watchForgetHour
	loops     []string
}

type watchForgetSpeech struct {
	watchConversationSpeech
	rowID, endAt int64
}

// Display reads report skipped rows without logging recording IDs or speech.
type watchSpeechSkips struct {
	missingSessions   int
	invalidRecordings int
	events            int
}

func (s *watchSpeechSkips) report() {
	if s.missingSessions+s.invalidRecordings+s.events > 0 {
		log.Printf("[recordings] speech read skipped %d recordings and %d events", s.missingSessions+s.invalidRecordings, s.events)
	}
}

// A nil skips requests the strict snapshot used to plan and commit a cut.
func readWatchSpeech(ctx context.Context, tx *sql.Tx, recordings []mobileRecording, since time.Time, skips *watchSpeechSkips) ([]watchForgetSpeech, error) {
	var speech []watchForgetSpeech
	for _, rec := range recordings {
		if rec.State != mobileRecordingTranscribed || rec.SessionID == "" {
			continue
		}
		started, err := time.Parse(time.RFC3339, rec.view().StartedAt)
		if err != nil {
			if skips != nil {
				skips.invalidRecordings++
				continue
			}
			return nil, err
		}
		if started.Before(since) {
			continue
		}
		var status string
		if err := tx.QueryRowContext(ctx, "SELECT triage_status FROM activity_sessions WHERE id=?", rec.SessionID).Scan(&status); err != nil {
			if skips != nil && errors.Is(err, sql.ErrNoRows) {
				skips.missingSessions++
				continue
			}
			return nil, fmt.Errorf("read recording session: %w", err)
		}
		if status == "protected" {
			continue
		}
		rows, err := tx.QueryContext(ctx, "SELECT id, data FROM activity_events WHERE session_id=? AND kind=? ORDER BY sort_order", rec.SessionID, activitymon.EventSpeech)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return nil, err
			}
			var event activitymon.ActivityEvent
			if err := json.Unmarshal(raw, &event); err != nil || event.Kind != activitymon.EventSpeech || event.Timestamp <= 0 {
				if skips != nil {
					skips.events++
					continue
				}
				rows.Close()
				return nil, errors.New("invalid stored speech event")
			}
			if strings.TrimSpace(event.SpeechText) != "" {
				speech = append(speech, watchForgetSpeech{watchConversationSpeech{At: event.Timestamp, RecordingID: rec.ID, DeviceID: rec.DeviceID, Speaker: event.SpeechSpeakerLabel, Text: event.SpeechText}, id, event.SpeechEndAt})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return speech, nil
}

// The note, speaker-name and conversation-name stores delete the index when
// its last entry goes. A missing file is their on-disk empty representation.
func readWatchForgetIndex[T any](dir, name string) (map[string]T, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]T{}, nil
	}
	if err != nil {
		return nil, err
	}
	var entries map[string]T
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if entries == nil {
		return nil, fmt.Errorf("invalid %s", name)
	}
	return entries, nil
}

func (a *App) watchForgetPlan(id string) (*watchForgetPlan, error) {
	store := a.watchRecordingStore()
	if store == nil || a.activity == nil || a.activity.store == nil {
		return nil, errMobileStoreUnavailable
	}
	recs, err := store.all()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := a.activity.store.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	lines, err := readWatchSpeech(ctx, tx, recs, time.Now().AddDate(0, 0, -watchConversationDays), nil)
	if err != nil {
		return nil, err
	}
	speech := make([]watchConversationSpeech, len(lines))
	for i, line := range lines {
		speech[i] = line.watchConversationSpeech
	}
	convs := buildWatchConversations(speech, watchConversationSilence)
	var target *WatchConversation
	for i := range convs {
		if convs[i].ID == id {
			target = &convs[i]
			break
		}
	}
	if target == nil {
		return nil, errWatchConversationGone
	}
	names, err := readWatchForgetIndex[watchConversationNameEntry](store.dir, watchConversationNamesFile)
	if err != nil {
		return nil, err
	}
	notes, err := readWatchForgetIndex[watchConversationNotesEntry](store.dir, watchConversationNotesFile)
	if err != nil {
		return nil, err
	}
	if _, err := readWatchForgetIndex[watchSpeakerNamesEntry](store.dir, watchSpeakerNamesFile); err != nil {
		return nil, err
	}
	applyWatchConversationNames(convs, names)
	note := notes[id]
	// Two notes may mention the same stable ledger ID. Purging it would
	// remove work still attached to the neighboring conversation.
	for key, other := range notes {
		if key == id {
			continue
		}
		for _, loop := range note.LoopIDs {
			if containsString(other.LoopIDs, loop) {
				return nil, errors.New("a follow-up belongs to another conversation too")
			}
		}
	}
	p := &watchForgetPlan{
		loops: note.LoopIDs,
		preview: WatchConversationForgetPreview{
			ID: id, Name: target.Name, LineCount: len(target.Lines),
			HasNote: note.Summary != "" || len(note.Decisions)+len(note.Followups) > 0,
			Audio:   []WatchConversationAudioCut{}, Claims: []WatchConversationForgetItem{},
			Followups: []WatchConversationForgetItem{}, StayingWithAgents: []string{},
		},
	}
	for _, line := range lines {
		if containsString(target.RecordingIDs, line.RecordingID) {
			p.speech = append(p.speech, line)
		}
	}
	for _, recID := range target.RecordingIDs {
		var hour watchForgetHour
		for _, rec := range recs {
			if rec.ID == recID {
				hour.recording = rec
				break
			}
		}
		hour.whole = true
		for _, c := range convs {
			if c.ID != id && containsString(c.RecordingIDs, recID) {
				hour.whole = false
				break
			}
		}
		for _, line := range lines {
			if line.RecordingID != recID || line.At < target.StartAt || line.At > target.EndAt {
				continue
			}
			hour.events = append(hour.events, line.rowID)
			hour.words += len(strings.Fields(line.Text))
		}
		p.hours = append(p.hours, hour)
	}
	// Exact ASR ends are retained on new captures. The display's words per
	// second estimate cannot establish which samples to erase on legacy ones.
	end := target.StartAt
	for _, line := range lines {
		if !containsString(target.RecordingIDs, line.RecordingID) || line.At < target.StartAt || line.At > target.EndAt {
			continue
		}
		for _, h := range p.hours {
			if h.recording.ID == line.RecordingID && !h.whole && !h.recording.AudioRemoved && line.endAt <= line.At {
				return nil, errors.New("recording does not keep exact speech boundaries")
			}
		}
		end = max(end, line.At, line.endAt)
	}
	// A conversation is grouped using estimated duration. If ASR shows a
	// neighbor actually overlaps it, there is no disjoint audio edit.
	for _, line := range lines {
		if !containsString(target.RecordingIDs, line.RecordingID) {
			continue
		}
		if line.At > target.EndAt && line.At < end || line.At < target.StartAt && line.endAt > target.StartAt {
			return nil, errors.New("conversation overlaps another voice in the recording")
		}
	}
	for _, h := range p.hours {
		rec := h.recording
		if !h.whole {
			if _, err := os.Stat(filepath.Join(store.dir, rec.ID, mobileRecordingVoicesFile)); err == nil {
				return nil, errors.New("the shared recording has a combined speaker fingerprint")
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
		if h.whole {
			for _, other := range recs {
				if other.ContinuationOf != rec.ID {
					continue
				}
				deletedWithParent := false
				for _, hour := range p.hours {
					if hour.recording.ID == other.ID && hour.whole {
						deletedWithParent = true
						break
					}
				}
				if !deletedWithParent {
					return nil, errors.New("this recording has a separately kept continuation")
				}
			}
		}
		if rec.AudioRemoved {
			continue
		}
		start, err := time.Parse(time.RFC3339, rec.view().StartedAt)
		if err != nil {
			return nil, err
		}
		for _, part := range rec.Received {
			if part.Paused {
				continue
			}
			if part.PausedCut && !h.whole {
				return nil, errors.New("recording has an earlier cut with unknown offsets")
			}
			from, err := time.Parse(time.RFC3339, part.StartedAt)
			if err != nil {
				return nil, err
			}
			lo := from.UnixMilli()
			hi := lo + part.watchOriginalDurationMs()
			if !h.whole {
				lo = max(lo, target.StartAt)
				hi = min(hi, end)
			}
			if hi <= lo {
				continue
			}
			if _, err := os.Stat(filepath.Join(store.dir, rec.ID, filepath.Base(part.File))); err != nil {
				return nil, err
			}
			p.preview.Audio = append(p.preview.Audio, WatchConversationAudioCut{rec.ID, lo - start.UnixMilli(), hi - start.UnixMilli()})
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, h := range p.hours {
		if !h.whole {
			continue
		}
		rec := h.recording
		derived, err := purposeOpenLoopsDiscoveredFromKinds(a.activity.store.reader(), purposeSessionSourceKinds, []string{rec.SessionID})
		if err != nil {
			return nil, err
		}
		for _, loop := range derived {
			if !containsString(p.loops, loop) {
				return nil, errors.New("a recording commitment has no conversation link")
			}
		}

	}
	itemIDs := map[string]bool{}
	for _, loop := range p.loops {
		detail, err := NewPurposeMapStore(a.activity.store.db).GetDetail(loop)
		if err != nil {
			return nil, err
		}
		item := WatchConversationForgetItem{watchFollowupItemPrefix + loop, purposeCurrentLedgerEvent(detail).Title}
		p.preview.Followups = append(p.preview.Followups, item)
		itemIDs[item.ID] = true
	}
	handoffs := a.workOnIt()
	handoffs.recordsMu.Lock()
	records, err := readWorkOnItHandoffsLocked()
	handoffs.recordsMu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.SourceKind == workonit.SourceWorkItem && itemIDs[record.SourceID] {
			p.preview.StayingWithAgents = append(p.preview.StayingWithAgents, a.handoffAgentName(record.AgentID, record.AgentName))
		}
	}
	sort.Strings(p.preview.StayingWithAgents)
	if err := p.readClaims(); err != nil {
		return nil, err
	}
	p.preview.Count = 1 + len(p.preview.Followups) + len(p.preview.Claims)
	if p.preview.HasNote {
		p.preview.Count++
	}
	payload, err := json.Marshal([]any{p.preview, watchConversationHash(*target), note})
	if err != nil {
		return nil, err
	}
	p.signature = sha256.Sum256(payload)
	return p, nil
}

func (p *watchForgetPlan) readClaims() error {
	dir, err := memoryDir()
	if err != nil {
		return err
	}
	records, err := loadMemoryForgetRecords(dir)
	if err != nil {
		return err
	}
	for _, record := range records {
		for _, content := range record.contents {
			doc := parseMemoryEntryDocument(content)
			if strings.HasPrefix(strings.TrimSpace(content), "---\n") && !doc.ok {
				return fmt.Errorf("cannot read memory metadata for %s", record.id)
			}
			support := memoryEvidenceSessionIDsFromDocument(doc)
			if len(support) == 0 {
				for _, h := range p.hours {
					if containsString(doc.frontmatter.ActivitySessionIDs, h.recording.SessionID) {
						return errors.New("the memory has a session dependency without attributable evidence")
					}
				}
			}
			owned := 0
			for _, h := range p.hours {
				if containsString(support, h.recording.SessionID) {
					if !h.whole {
						return errors.New("a claim identifies the shared recording but not its conversation")
					}
					owned++
				}
			}
			if owned == 0 {
				continue
			}
			if owned != len(support) {
				return errors.New("the claim has additional supporting sessions")
			}
			p.preview.Claims = append(p.preview.Claims, WatchConversationForgetItem{record.id, record.title})
			break
		}
	}
	sort.Slice(p.preview.Claims, func(i, j int) bool { return p.preview.Claims[i].ID < p.preview.Claims[j].ID })
	return nil
}

func (a *App) GetWatchConversationForgetPreview(conversationID string) (WatchConversationForgetPreview, error) {
	watchCutMu.Lock()
	defer watchCutMu.Unlock()
	p, err := a.watchForgetPlan(conversationID)
	if err != nil {
		return WatchConversationForgetPreview{}, err
	}
	store := a.watchRecordingStore()
	if store.forgetPreviews == nil {
		store.forgetPreviews = map[string][32]byte{}
	}
	store.forgetPreviews[conversationID] = p.signature
	return p.preview, nil
}
