package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMobileBriefScheduleDefaultsWhenMissing(t *testing.T) {
	store := newMobileBriefConfigStore(t.TempDir())
	got := store.load()
	want := mobileBriefSettings{Enabled: true, Morning: "07:00", Evening: "18:00", NewBrief: true, MessagingOutcomes: true}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
}

func TestMobileBriefScheduleRoundTripWritesVersionedFile(t *testing.T) {
	dir := t.TempDir()
	store := newMobileBriefConfigStore(dir)
	saved := mobileBriefSettings{Enabled: false, Morning: "06:45", Evening: "23:15"}
	if err := store.save(saved); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := store.load(); got != saved {
		t.Fatalf("loaded %+v, want %+v", got, saved)
	}

	data, err := os.ReadFile(filepath.Join(dir, mobileBriefScheduleFileName))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file["version"] != float64(1) || file["enabled"] != false ||
		file["morning"] != "06:45" || file["evening"] != "23:15" {
		t.Fatalf("schedule file = %v", file)
	}
	if runtime.GOOS != "windows" {
		mobileBriefAssertMode(t, filepath.Join(dir, mobileBriefScheduleFileName), 0600)
	}
}

func TestMobileBriefScheduleValidation(t *testing.T) {
	valid := []string{"00:00", "07:00", "12:59", "18:00", "23:59"}
	for _, value := range valid {
		settings := mobileBriefSettings{Enabled: true, Morning: value, Evening: "18:00"}
		if err := validateMobileBriefSettings(settings); err != nil {
			t.Errorf("morning %q rejected: %v", value, err)
		}
	}
	invalid := []string{"7:00", "24:00", "12:60", "ab", "", "07:00 ", "07:0", "07-00", "1200", "-1:00"}
	for _, value := range invalid {
		morning := mobileBriefSettings{Enabled: true, Morning: value, Evening: "18:00"}
		if err := validateMobileBriefSettings(morning); err == nil {
			t.Errorf("morning %q was accepted", value)
		}
		evening := mobileBriefSettings{Enabled: true, Morning: "07:00", Evening: value}
		if err := validateMobileBriefSettings(evening); err == nil {
			t.Errorf("evening %q was accepted", value)
		}
	}
}

func TestMobileBriefScheduleSaveRejectsInvalidTimes(t *testing.T) {
	dir := t.TempDir()
	store := newMobileBriefConfigStore(dir)
	if err := store.save(mobileBriefSettings{Enabled: true, Morning: "25:00", Evening: "18:00"}); err == nil {
		t.Fatal("save accepted an invalid morning time")
	}
	if _, err := os.Stat(filepath.Join(dir, mobileBriefScheduleFileName)); !os.IsNotExist(err) {
		t.Fatalf("an invalid schedule was written to disk: %v", err)
	}
}

func TestMobileBriefScheduleFallsBackPerField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, mobileBriefScheduleFileName)

	if err := os.WriteFile(path, []byte(`{"version":1,"enabled":false,"morning":"9:99","evening":"19:30"}`), 0600); err != nil {
		t.Fatal(err)
	}
	got := newMobileBriefConfigStore(dir).load()
	want := mobileBriefSettings{Enabled: false, Morning: "07:00", Evening: "19:30", NewBrief: true, MessagingOutcomes: true}
	if got != want {
		t.Fatalf("per-field fallback = %+v, want %+v", got, want)
	}

	if err := os.WriteFile(path, []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := newMobileBriefConfigStore(dir).load(); got != defaultMobileBriefSettings() {
		t.Fatalf("a file with only the version should keep defaults, got %+v", got)
	}
}

func TestMobileBriefScheduleIgnoresUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, mobileBriefScheduleFileName)
	cases := []string{
		`not json`,
		`{"version":2,"enabled":false,"morning":"06:00","evening":"20:00"}`,
	}
	for _, body := range cases {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if got := newMobileBriefConfigStore(dir).load(); got != defaultMobileBriefSettings() {
			t.Errorf("unreadable file %q changed the schedule to %+v", body, got)
		}
	}
}

func TestMobileBriefScheduleEnabledDefaultsToTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, mobileBriefScheduleFileName)
	if err := os.WriteFile(path, []byte(`{"version":1,"morning":"08:00"}`), 0600); err != nil {
		t.Fatal(err)
	}
	got := newMobileBriefConfigStore(dir).load()
	if !got.Enabled || got.Morning != "08:00" || got.Evening != "18:00" {
		t.Fatalf("missing enabled should default to true, got %+v", got)
	}
}

func TestMobileBriefParseClock(t *testing.T) {
	hour, minute, ok := parseMobileBriefClock("07:45")
	if !ok || hour != 7 || minute != 45 {
		t.Fatalf("parseMobileBriefClock(07:45) = %d:%d ok=%v", hour, minute, ok)
	}
	if _, _, ok := parseMobileBriefClock("7:45"); ok {
		t.Fatal("a single-digit hour should not parse")
	}
}
