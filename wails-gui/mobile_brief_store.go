package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	mobileBriefRetentionDays    = 14
	mobileBriefDirName          = "briefs"
	mobileBriefLatestFileName   = "latest.json"
	mobileBriefStoreListDefault = 30
	// Fourteen days at both on-demand sources' ten-minute limit, plus slots.
	mobileBriefStoreListMax = 8192
)

// mobileBriefIDPattern is the only shape a brief ID may take. IDs come from
// phones as well as from the clock, so they are checked before they become
// file names.
var mobileBriefIDPattern = regexp.MustCompile(
	`^[0-9]{4}-[0-9]{2}-[0-9]{2}-(morning|evening|on_request-[0-9]{6})$`,
)

var errMobileBriefStateUnavailable = errors.New("brief storage is unavailable")

// Readers instantiate stores independently, so the publication barrier must
// cover every store instance. It protects atomic replacements and retention.
var mobileBriefPublicationMu sync.RWMutex

// mobileBriefStateDir returns YANSU_MOBILE_STATE_DIR when it is set, otherwise
// ~/.yansu-agent/mobile. It matches the mobile connection state directory.
func mobileBriefStateDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("YANSU_MOBILE_STATE_DIR")); dir != "" {
		return dir, nil
	}
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "mobile"), nil
}

// mobileBriefLatestPointer names the most recently stored brief.
type mobileBriefLatestPointer struct {
	Version int    `json:"version"`
	BriefID string `json:"briefId"`
}

// mobileBriefStore keeps one JSON file per brief under <stateDir>/briefs, plus
// a latest.json pointer. Files are written atomically, so a reader never sees a
// partial brief.
type mobileBriefStore struct {
	dir string
	mu  sync.Mutex
	// The current brief (mobile_brief_current.go) is kept here until a brief
	// is stored or removed; currentFresh is false until it is next computed.
	currentBrief MobileBriefV1
	currentOK    bool
	currentFresh bool
}

func newMobileBriefStore(stateDir string) *mobileBriefStore {
	store := &mobileBriefStore{}
	if stateDir != "" {
		store.dir = filepath.Join(stateDir, mobileBriefDirName)
		adoptMobileBriefPrivacyEpoch(store.dir)
	}
	return store
}

// Local dependency identities stay beside the wire document on disk. Embedding
// the public document preserves existing readers; ordinary JSON encoding of
// MobileBriefV1 can never expose these session IDs to a phone or model.
type storedMobileBrief struct {
	ModelBinding    *mobileBriefModelBinding    `json:"modelReadBinding,omitempty"`
	RankedEdition   bool                        `json:"rankedEdition,omitempty"`
	RankedEmpty     bool                        `json:"rankedEmpty,omitempty"`
	OwnVoiceFacts   bool                        `json:"ownVoiceFacts,omitempty"`
	GenerationDebug *mobileBriefGenerationDebug `json:"generationDebug,omitempty"`
	MobileBriefV1
	AttendanceSourceSessions map[string][]string `json:"attendanceSourceSessions,omitempty"`
	AudioBinding             string              `json:"audioBinding,omitempty"`
	WeekLead                 string              `json:"weekLead,omitempty"`
	WeekLeadFacts            string              `json:"weekLeadFacts,omitempty"`
}

func marshalStoredMobileBrief(brief MobileBriefV1) ([]byte, error) {
	stored := storedMobileBrief{ModelBinding: brief.modelBinding, RankedEdition: brief.rankedEdition, RankedEmpty: brief.rankedEmpty, OwnVoiceFacts: brief.ownVoiceFacts, GenerationDebug: brief.generationDebug, MobileBriefV1: brief,
		AttendanceSourceSessions: map[string][]string{}, WeekLead: brief.WeekLead, WeekLeadFacts: brief.WeekLeadFacts}
	if brief.Audio != nil {
		stored.AudioBinding = brief.Audio.binding
	}
	for _, row := range brief.MeetingAttendance {
		if row.sourceSessionIDs != nil {
			stored.AttendanceSourceSessions[row.EventID] = row.sourceSessionIDs
		}
	}
	return json.MarshalIndent(stored, "", "  ")
}

func unmarshalStoredMobileBrief(data []byte, brief *MobileBriefV1) error {
	var stored storedMobileBrief
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	*brief = stored.MobileBriefV1
	brief.modelBinding = stored.ModelBinding
	brief.rankedEdition = stored.RankedEdition
	brief.rankedEmpty = stored.RankedEmpty
	brief.ownVoiceFacts = stored.OwnVoiceFacts
	brief.generationDebug = stored.GenerationDebug
	brief.WeekLead, brief.WeekLeadFacts = stored.WeekLead, stored.WeekLeadFacts
	if brief.Audio != nil {
		brief.Audio.binding = stored.AudioBinding
	}
	for i := range brief.MeetingAttendance {
		brief.MeetingAttendance[i].sourceSessionIDs = stored.AttendanceSourceSessions[brief.MeetingAttendance[i].EventID]
	}
	return nil
}

// save writes the brief, points latest.json at it, and removes briefs past the
// retention window measured from now. The source hook runs after the store's
// lock is released, so a re-seal may read the store.
func (s *mobileBriefStore) save(brief MobileBriefV1, now time.Time) error {
	if err := s.saveDocument(brief, now); err != nil {
		return err
	}
	briefSaved(brief.BriefID)
	return nil
}

func (s *mobileBriefStore) saveDocument(brief MobileBriefV1, now time.Time) error {
	if s.dir == "" {
		return errMobileBriefStateUnavailable
	}
	if !mobileBriefIDPattern.MatchString(brief.BriefID) {
		return errors.New("brief ID has an unsupported shape")
	}
	data, err := marshalStoredMobileBrief(brief)
	if err != nil {
		return err
	}
	pointer, err := json.MarshalIndent(mobileBriefLatestPointer{
		Version: 1,
		BriefID: brief.BriefID,
	}, "", "  ")
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// A stored brief may change which one is current, so the choice is read
	// again on the next use.
	s.currentFresh = false
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	if err := writeMobileBriefFileAtomic(s.dir, brief.BriefID+".json", data); err != nil {
		return err
	}
	// The pointer only moves forward in time: saving an older brief again
	// (its audio, a rebuild of an earlier slot) never makes it current.
	if s.pointerAdvancesLocked(brief) {
		if err := writeMobileBriefFileAtomic(s.dir, mobileBriefLatestFileName, pointer); err != nil {
			return err
		}
	}
	s.sweepLocked(now)
	return nil
}

// pointerAdvancesLocked reports whether brief is at least as new as the brief
// latest.json names now. The caller holds s.mu.
func (s *mobileBriefStore) pointerAdvancesLocked(brief MobileBriefV1) bool {
	data, err := os.ReadFile(filepath.Join(s.dir, mobileBriefLatestFileName))
	if err != nil {
		return true
	}
	var pointer mobileBriefLatestPointer
	if json.Unmarshal(data, &pointer) != nil || pointer.BriefID == brief.BriefID {
		return true
	}
	current, ok := s.load(pointer.BriefID)
	if !ok {
		return true
	}
	return !mobileBriefAsOfTime(brief).Before(mobileBriefAsOfTime(current))
}

// load returns the stored brief for an ID. The ID must match the strict
// pattern, and the file must describe the same ID.
func (s *mobileBriefStore) load(briefID string) (MobileBriefV1, bool) {
	if s.dir == "" || !mobileBriefIDPattern.MatchString(briefID) {
		return MobileBriefV1{}, false
	}
	data, err := os.ReadFile(filepath.Join(s.dir, briefID+".json"))
	if err != nil {
		return MobileBriefV1{}, false
	}
	var brief MobileBriefV1
	if err := unmarshalStoredMobileBrief(data, &brief); err != nil {
		return MobileBriefV1{}, false
	}
	if brief.BriefID != briefID {
		return MobileBriefV1{}, false
	}
	return brief, true
}

func (s *mobileBriefStore) exists(briefID string) bool {
	if s.dir == "" || !mobileBriefIDPattern.MatchString(briefID) {
		return false
	}
	_, err := os.Stat(filepath.Join(s.dir, briefID+".json"))
	return err == nil
}

// latest returns the brief named by latest.json, or the newest stored brief
// when the pointer is missing or stale.
func (s *mobileBriefStore) latest() (MobileBriefV1, bool) {
	if s.dir == "" {
		return MobileBriefV1{}, false
	}
	if data, err := os.ReadFile(filepath.Join(s.dir, mobileBriefLatestFileName)); err == nil {
		var pointer mobileBriefLatestPointer
		if json.Unmarshal(data, &pointer) == nil {
			if brief, ok := s.load(pointer.BriefID); ok && !mobileBriefHeld(brief, mobileBriefClock()) {
				return brief, true
			}
		}
	}
	briefs := s.briefs()
	if len(briefs) == 0 {
		return MobileBriefV1{}, false
	}
	return briefs[0], true
}

// list returns summaries of the newest briefs first.
func (s *mobileBriefStore) list(limit int) []MobileBriefSummary {
	if limit <= 0 {
		limit = mobileBriefStoreListDefault
	}
	if limit > mobileBriefStoreListMax {
		limit = mobileBriefStoreListMax
	}
	out := []MobileBriefSummary{}
	for _, brief := range s.briefs() {
		if brief.NarrativeStatus == "quiet" {
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, MobileBriefSummary{
			BriefID:         brief.BriefID,
			Slot:            mobileBriefDisplaySlot(brief, defaultMobileBriefSettings()),
			LocalDate:       brief.LocalDate,
			AsOf:            brief.AsOf,
			NarrativeStatus: brief.NarrativeStatus,
		})
	}
	return out
}

// briefs loads every valid stored brief, newest first.
func (s *mobileBriefStore) briefs() []MobileBriefV1 {
	if s.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []MobileBriefV1
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if id == entry.Name() {
			continue
		}
		// An early brief is held until its slot: no reader sees it before then.
		if brief, ok := s.load(id); ok && !mobileBriefHeld(brief, mobileBriefClock()) {
			out = append(out, brief)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai := mobileBriefAsOfTime(out[i])
		aj := mobileBriefAsOfTime(out[j])
		if !ai.Equal(aj) {
			return ai.After(aj)
		}
		return out[i].BriefID > out[j].BriefID
	})
	return out
}

func mobileBriefAsOfTime(brief MobileBriefV1) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, brief.AsOf)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// sweepLocked removes stored briefs whose local date is older than the
// retention window. The caller holds s.mu.
func (s *mobileBriefStore) sweepLocked(now time.Time) {
	// Removing a brief can change the current one.
	s.currentFresh = false
	mobileBriefPublicationMu.Lock()
	defer mobileBriefPublicationMu.Unlock()
	local := now.In(time.Local)
	cutoff := time.Date(
		local.Year(), local.Month(), local.Day()-mobileBriefRetentionDays,
		0, 0, 0, 0, time.Local,
	)
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// Briefs and their spoken versions share the brief id as the stem.
		id := entry.Name()
		if id == mobileBriefTodayFileName {
			brief, ok := s.currentToday()
			if !ok {
				continue
			}
			id = brief.BriefID + ".json"
		}
		switch filepath.Ext(id) {
		case ".json", mobileBriefAudioExtM4A, mobileBriefAudioExtWAV,
			mobileBriefPackageExt, mobileBriefPackageMetaExt, mobileBriefPackageSrcExt:
			id = strings.TrimSuffix(id, filepath.Ext(id))
		default:
			continue
		}
		if !mobileBriefIDPattern.MatchString(id) {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", id[:10], time.Local)
		if err != nil || !day.Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, entry.Name())); err == nil {
			removed++
		}
	}
	if removed > 0 {
		log.Printf("[mobile-brief] retention removed %d stored briefs", removed)
	}
}

// writeMobileBriefFileAtomic writes data to a temporary file in dir and renames
// it over name, so readers see either the old file or the new one.
func writeMobileBriefFileAtomic(dir, name string, data []byte) (err error) {
	mobileBriefPublicationMu.Lock()
	defer mobileBriefPublicationMu.Unlock()
	tmp, err := os.CreateTemp(dir, "."+name+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close brief file: %w", err)
	}
	if err = os.Chmod(tmpName, 0600); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, name))
}
