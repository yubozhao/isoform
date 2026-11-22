package main

// "Notes for long talks" as the person's own setting (PROTOCOL.md 24.12).
//
// Notes and follow-ups from watch conversations over five minutes began
// behind a build switch (YANSU_FEATURES=watch-notes). The phone's Watch tab
// has a switch for it, so the choice is now stored: once the person has
// chosen, their choice decides. Until they have, the build switch decides, as
// before.
//
// Turning it off stops new notes. Notes already written stay until their
// words go (a deleted hour, a withdrawn transcript), as before.

const watchNotesSettingKey = "watch_notes_setting"

// MobileWatchNotesSetting is the setting as the phone reads it.
type MobileWatchNotesSetting struct {
	On bool `json:"on"`
	// Chosen is true once the person set it themself.
	Chosen bool `json:"chosen"`
}

// Only start background note generation when its setting can be read.
func (a *App) watchNotesOn() bool {
	setting, err := a.readWatchSetting(watchNotesSettingKey)
	if err != nil {
		return false
	}
	return setting.On
}
