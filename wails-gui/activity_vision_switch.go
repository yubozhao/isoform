package main

import (
	"context"
	"errors"
	"strconv"

	"wails-gui/models"
)

// WeChat pictures (computer-conformance answers v1.4.0 S18 and S13).
//
// The vision pass sends a picture of a WeChat window to the assistant when
// its text can't be read. It has its own switch in What Isoform sees ›
// Screen, apart from the Activity timeline's: on for an install that never
// set it, so no one's behaviour changes silently, and a saved choice wins.
// A failed read sends nothing.
//
// No picture leaves before the privacy model (GLiNER, which masks
// screenshots) is on this computer. Text goes on as usual.
//
// Both are checked in visionSessionAllowsModelRead, which every vision path
// calls before it builds an image and again right before it sends one.

const activityVisionEnabledKey = "activity_vision_enabled"

// visionPrivacyModelID is the screenshot masker in the model catalog.
const visionPrivacyModelID = "gliner-pii"

// visionPrivacyModelReady reports whether the privacy model is on this
// computer. A variable so tests can stand in for the models folder.
var visionPrivacyModelReady = func() bool {
	dir := configDirOrFallback()
	return dir != "" && models.Present(dir, visionPrivacyModelID)
}

// ActivityVisionView is the WeChat pictures row.
type ActivityVisionView struct {
	Enabled bool `json:"enabled"`
	// PrivacyModelReady is false while pictures wait for the privacy model.
	PrivacyModelReady bool `json:"privacyModelReady"`
}

func (a *App) activityVisionEnabled() (bool, error) {
	if a == nil || a.store == nil {
		return false, errors.New("settings store unavailable")
	}
	raw, err := a.store.GetUIState(context.Background(), activityVisionEnabledKey)
	if err != nil {
		return false, err
	}
	if raw == "" {
		return true, nil
	}
	on, err := strconv.ParseBool(raw)
	if err != nil {
		return false, err
	}
	return on, nil
}

// visionPicturesAllowed is the switch and the privacy model together; a
// failed read is no.
func (a *App) visionPicturesAllowed() bool {
	on, err := a.activityVisionEnabled()
	return err == nil && on && visionPrivacyModelReady()
}

// GetActivityVision reads the row. A failed read is an error, never off.
func (a *App) GetActivityVision() (ActivityVisionView, error) {
	on, err := a.activityVisionEnabled()
	if err != nil {
		return ActivityVisionView{}, err
	}
	return ActivityVisionView{Enabled: on, PrivacyModelReady: visionPrivacyModelReady()}, nil
}

// SetActivityVisionEnabled saves the choice.
func (a *App) SetActivityVisionEnabled(enabled bool) (ActivityVisionView, error) {
	if a == nil || a.store == nil {
		return ActivityVisionView{}, errors.New("settings store unavailable")
	}
	if err := a.store.SetUIState(context.Background(), activityVisionEnabledKey, strconv.FormatBool(enabled)); err != nil {
		return ActivityVisionView{}, err
	}
	return a.GetActivityVision()
}
