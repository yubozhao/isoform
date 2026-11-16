package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// meetingNotesSavedEvent tells an open Activity screen to reload a
// meeting's notes.
const meetingNotesSavedEvent = "meeting-notes:saved"

// recordMeetingNotes stores one activity session's notes with its meeting
// on Activity and starts the learning pass over the meeting's notes. The
// outline is the after-call pass's result; when the pass did not run in
// this call (a crash recovery), the outline it stored earlier for the
// session is kept. The other activity sessions a recording spans join the
// meeting too, so its notes show on each of them and every one of them
// gates its reads. It tells nobody: recording a call and writing it up
// never notifies. The live call path calls it inside the recording fence;
// startup recovery checks meetingTranscriptAllowed first.
func (a *App) recordMeetingNotes(session ActivitySessionRow, meetingApp string, wordCount int, meetingID, title string, outline *meetingFollowupExtraction) (string, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return "", fmt.Errorf("activity store unavailable")
	}
	store := a.activity.store
	key := meetingNotesKey(session, meetingID)
	if title == "" {
		title = a.storedMeetingTitle(session.ID)
	}
	header := meetingNotesHeader{
		Key:        key,
		MeetingID:  strings.TrimSpace(meetingID),
		MeetingApp: meetingApp,
		Title:      title,
		StartTime:  session.StartTime,
		EndTime:    session.EndTime,
	}
	if err := store.saveMeetingNotesSlice(header, session.ID, session.StartTime, session.EndTime, wordCount); err != nil {
		return "", err
	}
	for _, id := range a.meetingRecordingSourceSessions(session) {
		if id == session.ID {
			continue
		}
		if _, known, err := store.meetingNotesKeyForSession(id); err != nil || known {
			continue
		}
		if source, err := store.GetSessionByID(id); err == nil && source != nil {
			if err := store.saveMeetingNotesSlice(header, id, source.StartTime, source.EndTime, 0); err != nil {
				return key, err
			}
		}
	}
	if outline == nil && !store.meetingNoteSliceHasOutline(session.ID) {
		// Notes recorded before the summary existed kept their outline in
		// the after-call pass's own table.
		outline = a.storedMeetingOutline(session.ID)
	}
	if outline != nil {
		if err := store.saveMeetingNotesOutline(session.ID, title, *outline); err != nil {
			return key, err
		}
	}
	if a.ctx != nil {
		a.emitBrowserEvent(meetingNotesSavedEvent, map[string]string{"sessionId": session.ID})
	}
	log.Printf("[meeting-notes] saved notes of %s with its activity (%s, %d words)", session.ID, meetingApp, wordCount)
	a.learnFromMeetingNotesAsync(key)
	return key, nil
}

// storedMeetingOutline reads what the after-call pass stored for a session
// in its own table (decisions and follow-ups; no summary).
func (a *App) storedMeetingOutline(sessionID string) *meetingFollowupExtraction {
	if a.store == nil {
		return nil
	}
	row, found, err := a.store.GetMeetingFollowup(context.Background(), sessionID)
	if err != nil || !found {
		return nil
	}
	out := meetingFollowupExtraction{Decisions: []string{}, Followups: []meetingFollowup{}}
	_ = json.Unmarshal([]byte(row.DecisionsJSON), &out.Decisions)
	_ = json.Unmarshal([]byte(row.FollowupsJSON), &out.Followups)
	return &out
}

// storedMeetingTitle is the calendar title the after-call pass stored.
func (a *App) storedMeetingTitle(sessionID string) string {
	if a.store == nil {
		return ""
	}
	row, found, err := a.store.GetMeetingFollowup(context.Background(), sessionID)
	if err != nil || !found {
		return ""
	}
	return strings.TrimSpace(row.Title)
}

// DeleteMeetingNotes deletes a meeting's notes and the call with them: the
// recording and the transcript of every part, the call's leftover audio,
// the knowledge and memory entries learned only from these notes, the
// commitments made from its transcript, and the revision history of a moved
// Knowledge entry. A publish marker keeps
// startup recovery from writing the notes again.
// What the learning pass merged into other entries stays there.
func (a *App) DeleteMeetingNotes(meetingKey string) error {
	// Removed notes are a source a sealed brief may cite: seal again before it is shown.
	defer briefPrivacyChanged()
	if a.activity == nil || a.activity.store == nil {
		return fmt.Errorf("activity store not initialized")
	}
	store := a.activity.store
	r, ok, err := store.meetingNotesByKey(meetingKey)
	if err != nil || !ok {
		return err
	}
	// The marker comes first: without it, startup recovery could write the
	// notes again from a transcript a failed removal left behind.
	dir, err := knowledgeDir()
	if err != nil {
		return err
	}
	if err := markMeetingNotesPublished(dir, meetingKnowledgeEntryID(r.Key)); err != nil {
		return fmt.Errorf("deleted-notes marker: %w", err)
	}
	a.forgetMeetingCall(r.Key, r.SessionIDs())
	if err := store.deleteMeetingNotes(r.Key); err != nil {
		return err
	}
	// The commitments that were made from this meeting's transcript go with
	// it: deleted means deleted (purpose_purge.go).
	if n, err := a.purgePurposeLearnedFrom(meetingFollowupSourceKind, r.SessionIDs()); err != nil {
		log.Printf("[meeting-notes] commitments of %s were not purged: %s", r.Key, activityPrivateLogField("error", err.Error()))
	} else if n > 0 {
		log.Printf("[meeting-notes] purged %d commitments of %s", n, r.Key)
	}
	// After the row: a learning pass rechecks the notes after it writes, so
	// whatever it wrote is gone either way.
	a.forgetMeetingNotesLearning(r.Key)
	if r.LegacyKnowledgeID != "" && !strings.ContainsAny(r.LegacyKnowledgeID, "/\\") && !strings.Contains(r.LegacyKnowledgeID, "..") {
		// The moved entry's revision history goes with the notes.
		_ = os.RemoveAll(filepath.Join(dir, knowledgeHistoryDirName, r.LegacyKnowledgeID))
	}
	if a.ctx != nil {
		a.emitBrowserEvent(meetingNotesSavedEvent, map[string]string{"meetingKey": r.Key})
	}
	log.Printf("[meeting-notes] deleted the notes and recording of %s", r.Key)
	return nil
}

// forgetMeetingNotesLearning deletes the knowledge and memory entries the
// learning pass wrote from one meeting's notes.
func (a *App) forgetMeetingNotesLearning(key string) {
	seed := meetingNotesEntrySeed(key)
	if err := a.DeleteKnowledgeEntry(activityKnowledgeEntryID(seed)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[meeting-notes] delete learned knowledge of %s: %v", key, err)
	}
	if err := a.DeleteMemoryEntry(activityMemoryEntryID(seed)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[meeting-notes] delete learned memory of %s: %v", key, err)
	}
}

// forgetMeetingCall removes the recording and the transcript file of each
// activity session of a meeting, clears them from the session rows, and
// removes the meeting's leftover audio. The recording in progress stays.
func (a *App) forgetMeetingCall(key string, sessionIDs []string) {
	a.forgetMeetingCallFiles(sessionIDs, meetingKnowledgeEntryID(key))
}

// forgetMeetingCallFiles is forgetMeetingCall for a meeting known by its
// old entry id.
func (a *App) forgetMeetingCallFiles(sessionIDs []string, entryID string) {
	if a.activity == nil || a.activity.store == nil {
		return
	}
	store := a.activity.store
	for _, sessionID := range sessionIDs {
		session, err := store.GetSessionByID(sessionID)
		if err != nil || session == nil {
			continue
		}
		if session.AudioPath != "" {
			if err := removeMeetingRecording(store.AudioDir(), session.AudioPath); err != nil {
				log.Printf("[meeting-notes] remove recording of %s: %v", sessionID, err)
			}
		}
		if session.TranscriptPath != "" {
			if err := os.Remove(session.TranscriptPath); err != nil && !os.IsNotExist(err) {
				log.Printf("[meeting-notes] remove transcript of %s: %v", sessionID, err)
			}
		}
		if err := store.ClearMeetingRecording(sessionID); err != nil {
			log.Printf("[meeting-notes] clear recording of %s: %v", sessionID, err)
		}
		markTranscriptAttempted(sessionID)
	}
	a.forgetMeetingAudioFiles(store.AudioDir(), entryID)
}
