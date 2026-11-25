package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// recoveredRecordingTrigger marks the session row of a call recovered from
// the raw tracks a crash left behind.
const recoveredRecordingTrigger = "recovered_recording"

// Maximum delay before storage treats an existing live-engine transcript as final.
const meetingTrackMaxAge = 7 * 24 * time.Hour

// recoverMeetingAudio runs once at start. With call recording on, the raw
// tracks a crash left behind become recordings; with it off they are
// removed, as turning recording off removes a recording in progress. The
// recording fence is held throughout, so a change of the setting meanwhile
// waits.
func (a *App) recoverMeetingAudio(store *ActivityStore, before time.Time) {
	a.meetingRecordingMu.Lock()
	defer a.meetingRecordingMu.Unlock()
	if enabled, known := a.meetingRecordingPreference(); known {
		store.recoverMeetingAudioTracks(before, !enabled)
	}
}

// recoverMeetingAudioTracks finalizes the raw tracks of calls that a crash or
// force quit left behind into a WAV with a meeting session row, so the
// transcript can be written after the next start (recoverUntranscribedMeetings).
// Tracks newer than before are a recording of this run and are left alone.
// Tracks whose WAV cannot be written stay for the next start.
// discard removes them instead (call recording is off).
func (s *ActivityStore) recoverMeetingAudioTracks(before time.Time, discard bool) {
	if s == nil || s.audioDir == "" {
		return
	}
	days, err := os.ReadDir(s.audioDir)
	if err != nil {
		return
	}
	for _, day := range days {
		if !day.IsDir() {
			continue
		}
		dayDir := filepath.Join(s.audioDir, day.Name())
		files, err := os.ReadDir(dayDir)
		if err != nil {
			continue
		}
		prefixes := map[string]bool{}
		for _, f := range files {
			name := f.Name()
			for _, ext := range []string{".mic.pcm", ".sys.pcm", ".json"} {
				if strings.HasSuffix(name, ext) {
					prefixes[strings.TrimSuffix(name, ext)] = true
				}
			}
		}
		for prefix := range prefixes {
			s.recoverMeetingAudioPrefix(day.Name(), prefix, before, discard)
		}
	}
}

func (s *ActivityStore) recoverMeetingAudioPrefix(relDir, prefix string, before time.Time, discard bool) {
	if err := s.recoverMeetingAudioJournal(relDir, prefix, before, discard); err != nil {
		log.Printf("[meeting-audio] recovery of %s/%s deferred: %v", relDir, prefix, err)
	}
}

// The sidecar is a journal across both rename and database commit. Pending
// identities may be reconstructed; a missing committed identity never may.
func (s *ActivityStore) recoverMeetingAudioJournal(relDir, prefix string, before time.Time, discard bool) error {
	dayDir := filepath.Join(s.audioDir, relDir)
	micPath, sysPath := filepath.Join(dayDir, prefix+".mic.pcm"), filepath.Join(dayDir, prefix+".sys.pcm")
	metaPath := filepath.Join(dayDir, prefix+".json")
	var meta meetingAudioMeta
	if raw, err := os.ReadFile(metaPath); err == nil {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var micBytes, sysBytes int64
	var newest time.Time
	hasTracks := false
	for _, track := range []struct {
		path  string
		bytes *int64
	}{{micPath, &micBytes}, {sysPath, &sysBytes}} {
		if info, err := os.Stat(track.path); err == nil {
			hasTracks = true
			*track.bytes = info.Size() / 2 * 2
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	createdMs, id := prefix, prefix
	if dash := strings.IndexByte(prefix, '-'); dash > 0 {
		createdMs, id = prefix[:dash], prefix[dash+1:]
	}
	startMs := meta.StartMs
	if startMs <= 0 {
		startMs, _ = strconv.ParseInt(createdMs, 10, 64)
	}
	finalPrefix := strconv.FormatInt(startMs, 10) + "-" + id
	relPath := ""
	var size int64
	if !hasTracks {
		if !meta.PendingAudio && len(meta.PendingSources) == 0 {
			return nil
		}
		for _, name := range []string{finalPrefix, prefix} {
			for _, ext := range []string{".wav", ".opus"} {
				candidate := filepath.Join(relDir, name+ext)
				if info, err := os.Stat(filepath.Join(s.audioDir, candidate)); err == nil && !info.IsDir() {
					relPath, size, newest = candidate, info.Size(), info.ModTime()
					break
				}
			}
			if relPath != "" {
				break
			}
		}
	}
	if newest.IsZero() || !newest.Before(before) {
		return nil
	}
	var owner *ActivitySessionRow
	if meta.SessionID != "" {
		var err error
		owner, err = s.GetSessionByID(meta.SessionID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if discard {
		// Turning recording off discards pending capture, never historical calls.
		if hasTracks || (owner == nil && (meta.PendingAudio || len(meta.PendingSources) > 0)) {
			os.Remove(micPath)
			os.Remove(sysPath)
			os.Remove(metaPath)
			if relPath != "" {
				os.Remove(filepath.Join(s.audioDir, relPath))
			}
		}
		return nil
	}
	if startMs <= 0 {
		startMs = newest.UnixMilli()
		finalPrefix = strconv.FormatInt(startMs, 10) + "-" + id
	}
	if meta.SessionID == "" {
		if !hasTracks {
			return nil
		}
		// Legacy raw capture has no provisional owner. Reserve it durably once.
		meta.SessionID = uuid.New().String()
		longest := max(micBytes, sysBytes)
		source := meta.CaptureSource
		source.SessionID, source.StartTime, source.EndTime = meta.SessionID, startMs, startMs+longest*1000/meetingAudioBytesPerSecond
		source.Apps = compactStrings(append(source.Apps, meta.App, meta.BundleID))
		source.Private = source.Private || meta.Protected
		meta.ActivitySessions = compactStrings(append(meta.ActivitySessions, source.SessionID))
		meta.PendingSources = append(meta.PendingSources, pendingRecordingSource{activitySourceProvenance: source, Complete: true})
		if err := writeMeetingAudioMeta(metaPath, meta); err != nil {
			return err
		}
	}
	if hasTracks {
		meta.PendingAudio = true
		meta.MonoSource = meetingMonoSource(micBytes, sysBytes)
		if err := writeMeetingAudioMeta(metaPath, meta); err != nil {
			return err
		}
		var err error
		relPath, size, err = finalizeMeetingTracks(s.audioDir, relDir, finalPrefix, micPath, sysPath, micBytes, sysBytes)
		if err != nil {
			return err
		}
		if relPath == "" {
			os.Remove(metaPath)
			return nil
		}
	} else if strings.HasSuffix(relPath, ".wav") {
		if _, err := readMeetingWAVInfo(filepath.Join(s.audioDir, relPath)); err != nil {
			return err
		}
	}
	if relPath == "" {
		return fmt.Errorf("recording audio unavailable")
	}
	finalMeta := meetingWAVMetaPath(s.audioDir, relPath)
	if err := writeMeetingAudioMeta(finalMeta, meta); err != nil {
		return err
	}
	if finalMeta != metaPath {
		os.Remove(metaPath)
	}
	s.readPrivacyMu.Lock()
	defer s.readPrivacyMu.Unlock()
	if err := s.recoverPendingRecordingSources(&meta); err != nil {
		return err
	}
	// No pending proof means a missing owner was committed and later removed,
	// or its provenance is unknown. Do not replace it with a different row.
	owner, err := s.GetSessionByID(meta.SessionID)
	if err != nil {
		return err
	}
	if owner == nil {
		return fmt.Errorf("recording owner unavailable")
	}
	if err := s.AttachMeetingRecording(owner.ID, relPath, size); err != nil {
		return err
	}
	meta.PendingAudio = false
	if err := writeMeetingAudioMeta(finalMeta, meta); err != nil {
		return err
	}
	log.Printf("[meeting-audio] recovered recording for session %s: %s", owner.ID, relPath)
	return nil
}

// meetingRecordingTriageStatus is the triage status of a session made from a
// recording alone: protected when a protected app was in front during the
// call (no transcript, no notes, as for any protected session), otherwise
// done.
func meetingRecordingTriageStatus(meta meetingAudioMeta) string {
	if meta.Protected || meta.CaptureSource.Private {
		return "protected"
	}
	return "dismissed"
}
