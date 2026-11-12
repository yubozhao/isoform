package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	mobileBriefScheduleFileName = "brief-schedule.json"
	mobileBriefScheduleVersion  = 1
	mobileBriefDefaultMorning   = "07:00"
	mobileBriefDefaultEvening   = "18:00"
)

var mobileBriefClockPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

// mobileBriefSettings is the persisted brief schedule.
type mobileBriefSettings struct {
	Enabled  bool
	Morning  string
	Evening  string
	Adaptive bool
	// Feed serves the spoken briefs as a private podcast feed on this Mac
	// (docs/notifications-design.md section 7). Off by default.
	Feed bool
	// EnabledAt is when the schedule was last turned on (RFC 3339), so slots
	// from a period the schedule was off are never reported as missed. Empty
	// for the defaults and for files written before it existed.
	EnabledAt string
	// ShadowCompose runs the new brief pipeline (internal/brief) beside each
	// scheduled brief and stores its result under briefs/shadow only. Nothing
	// user-visible changes. Off by default; the hub turns it on for a build.
	ShadowCompose bool
	// NewBrief writes the narrative with the new one-call writer
	// (mobile_brief_new_writer.go), the old path as its fallback. On by
	// default; "newBrief": false rolls back to the old path.
	NewBrief bool
}

// mobileBriefScheduleFile is the on-disk shape of brief-schedule.json.
type mobileBriefScheduleFile struct {
	Version   int    `json:"version"`
	Enabled   *bool  `json:"enabled,omitempty"`
	Morning   string `json:"morning,omitempty"`
	Evening   string `json:"evening,omitempty"`
	Feed      *bool  `json:"feed,omitempty"`
	EnabledAt string `json:"enabledAt,omitempty"`
	Adaptive  *bool  `json:"adaptiveTiming,omitempty"`
	// A missing key reads as off.
	ShadowCompose *bool `json:"shadowCompose,omitempty"`
	// A missing key reads as on.
	NewBrief *bool `json:"newBrief,omitempty"`
}

func defaultMobileBriefSettings() mobileBriefSettings {
	return mobileBriefSettings{
		Enabled:  true,
		Morning:  mobileBriefDefaultMorning,
		Evening:  mobileBriefDefaultEvening,
		Adaptive: appFlavor() == appFlavorIsoform,
		NewBrief: true,
	}
}

// validateMobileBriefSettings requires both slot times in 24-hour HH:MM form.
func validateMobileBriefSettings(settings mobileBriefSettings) error {
	if !mobileBriefClockPattern.MatchString(settings.Morning) {
		return errors.New("morning must be a 24-hour time such as 07:00")
	}
	if !mobileBriefClockPattern.MatchString(settings.Evening) {
		return errors.New("evening must be a 24-hour time such as 18:00")
	}
	return nil
}

// parseMobileBriefClock returns the hour and minute of a valid HH:MM value.
func parseMobileBriefClock(value string) (hour, minute int, ok bool) {
	match := mobileBriefClockPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, 0, false
	}
	hour, _ = strconv.Atoi(match[1])
	minute, _ = strconv.Atoi(match[2])
	return hour, minute, true
}

// mobileBriefConfigStore reads and writes brief-schedule.json in the mobile
// state directory.
type mobileBriefConfigStore struct {
	path     string
	mu       sync.Mutex
	warnOnce sync.Once
}

func newMobileBriefConfigStore(stateDir string) *mobileBriefConfigStore {
	store := &mobileBriefConfigStore{}
	if stateDir != "" {
		store.path = filepath.Join(stateDir, mobileBriefScheduleFileName)
	}
	return store
}

// load returns the saved schedule. A missing file yields the defaults. A file
// that cannot be parsed, or that has the wrong version, yields the defaults as
// a whole. An invalid time yields the default for that slot only. A service
// built without a store (tests, or the Today warm-up racing start) gets the
// defaults too.
func (c *mobileBriefConfigStore) load() mobileBriefSettings {
	settings := defaultMobileBriefSettings()
	if c == nil || c.path == "" {
		return settings
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return settings
	}
	var file mobileBriefScheduleFile
	if err := json.Unmarshal(data, &file); err != nil || file.Version != mobileBriefScheduleVersion {
		c.warnOnce.Do(func() {
			log.Printf("[mobile-brief] schedule file is not readable; using default times")
		})
		return settings
	}
	if file.Enabled != nil {
		settings.Enabled = *file.Enabled
	}
	if file.Feed != nil {
		settings.Feed = *file.Feed
	}
	if file.Adaptive != nil {
		settings.Adaptive = *file.Adaptive
	}
	if file.ShadowCompose != nil {
		settings.ShadowCompose = *file.ShadowCompose
	}
	if file.NewBrief != nil {
		settings.NewBrief = *file.NewBrief
	}
	if _, err := time.Parse(time.RFC3339Nano, file.EnabledAt); err == nil {
		settings.EnabledAt = file.EnabledAt
	}
	if _, _, ok := parseMobileBriefClock(file.Morning); ok {
		settings.Morning = file.Morning
	}
	if _, _, ok := parseMobileBriefClock(file.Evening); ok {
		settings.Evening = file.Evening
	}
	return settings
}

// save validates the schedule and writes it atomically with 0600 permissions.
func (c *mobileBriefConfigStore) save(settings mobileBriefSettings) error {
	if c.path == "" {
		return errMobileBriefStateUnavailable
	}
	if err := validateMobileBriefSettings(settings); err != nil {
		return err
	}
	enabled, feed, adaptive, shadow, newBrief := settings.Enabled, settings.Feed, settings.Adaptive, settings.ShadowCompose, settings.NewBrief
	data, err := json.MarshalIndent(mobileBriefScheduleFile{
		Version:       mobileBriefScheduleVersion,
		Enabled:       &enabled,
		Morning:       settings.Morning,
		Evening:       settings.Evening,
		Feed:          &feed,
		EnabledAt:     settings.EnabledAt,
		Adaptive:      &adaptive,
		ShadowCompose: &shadow,
		NewBrief:      &newBrief,
	}, "", "  ")
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return writeMobileBriefFileAtomic(dir, filepath.Base(c.path), data)
}
