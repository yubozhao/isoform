package main

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"wails-gui/internal/platform/activitymon"
)

// What the watch knew while a part of a recording was made (PROTOCOL.md 24.4):
// the wearer's heart rate and roughly where they were. Both are optional and
// each needs its own permission on the watch. They travel inside the sealed
// POST /recordings request, are stored with the part, and are deleted with the
// recording's audio. The computer uses them only as context for the brief:
// a place name and how intense the moment was. Coordinates never reach text.

const (
	mobileRecordingMinBPM         = 20
	mobileRecordingMaxBPM         = 260
	mobileRecordingMaxPlaceRunes  = 80
	mobileRecordingMaxAccuracyM   = 100000
	mobileRecordingCoarseDecimals = 2 // about 1.1 km of latitude
)

// MobileRecordingPartContext is the optional `context` of POST /recordings.
type MobileRecordingPartContext struct {
	HeartRate *MobileRecordingHeartRate `json:"heartRate,omitempty"`
	Location  *MobileRecordingLocation  `json:"location,omitempty"`
}

// MobileRecordingHeartRate summarizes the heart rate samples of one part, in
// beats per minute.
type MobileRecordingHeartRate struct {
	Avg     float64 `json:"avg"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Samples int     `json:"samples"`
}

// MobileRecordingLocation is where the part was recorded, coarse. Place is a
// name the watch or phone resolved (a neighborhood or town), when it had one.
type MobileRecordingLocation struct {
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	AccuracyMeters float64 `json:"accuracyMeters,omitempty"`
	Place          string  `json:"place,omitempty"`
}

// sanitizeMobileRecordingContext keeps what is usable and drops the rest. A
// bad context never refuses the audio it came with: the words matter more.
// Coordinates are rounded here too, so the computer never stores a precise
// position whatever the device sent.
func sanitizeMobileRecordingContext(in *MobileRecordingPartContext) *MobileRecordingPartContext {
	if in == nil {
		return nil
	}
	out := &MobileRecordingPartContext{}
	if hr := in.HeartRate; hr != nil && hr.Samples > 0 && hr.Samples <= 100000 &&
		validBPM(hr.Avg) && validBPM(hr.Min) && validBPM(hr.Max) && hr.Min <= hr.Avg && hr.Avg <= hr.Max {
		out.HeartRate = &MobileRecordingHeartRate{Avg: math.Round(hr.Avg), Min: math.Round(hr.Min), Max: math.Round(hr.Max), Samples: hr.Samples}
	}
	if loc := in.Location; loc != nil && validCoordinate(loc.Latitude, 90) && validCoordinate(loc.Longitude, 180) &&
		!(loc.Latitude == 0 && loc.Longitude == 0) {
		kept := &MobileRecordingLocation{
			Latitude:  roundCoarse(loc.Latitude),
			Longitude: roundCoarse(loc.Longitude),
			Place:     cleanMobileRecordingPlace(loc.Place),
		}
		if loc.AccuracyMeters > 0 && loc.AccuracyMeters <= mobileRecordingMaxAccuracyM && !math.IsNaN(loc.AccuracyMeters) {
			kept.AccuracyMeters = math.Round(loc.AccuracyMeters)
		}
		out.Location = kept
	}
	if out.HeartRate == nil && out.Location == nil {
		return nil
	}
	return out
}

func validBPM(v float64) bool {
	return !math.IsNaN(v) && v >= mobileRecordingMinBPM && v <= mobileRecordingMaxBPM
}

func validCoordinate(v, limit float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= -limit && v <= limit
}

func roundCoarse(v float64) float64 {
	scale := math.Pow(10, mobileRecordingCoarseDecimals)
	return math.Round(v*scale) / scale
}

// cleanMobileRecordingPlace keeps a short, printable place name. A name that
// looks like coordinates is dropped: only names may reach text.
func cleanMobileRecordingPlace(place string) string {
	place = strings.Join(strings.FieldsFunc(place, func(r rune) bool {
		return unicode.IsControl(r) || r == ' ' || r == ' '
	}), " ")
	place = strings.Join(strings.Fields(place), " ")
	if runes := []rune(place); len(runes) > mobileRecordingMaxPlaceRunes {
		place = strings.TrimSpace(string(runes[:mobileRecordingMaxPlaceRunes]))
	}
	letters, digits := 0, 0
	for _, r := range place {
		switch {
		case unicode.IsLetter(r):
			letters++
		case unicode.IsDigit(r):
			digits++
		}
	}
	if letters == 0 || digits > letters {
		return ""
	}
	return place
}

// mobileRecordingIntensity names how hard the heart was working. It is a rough
// reading of the average alone; the computer does not know the wearer's
// resting rate.
func mobileRecordingIntensity(avg float64) string {
	switch {
	case avg < 90:
		return "calm"
	case avg < 120:
		return "raised"
	default:
		return "high"
	}
}

// mobileRecordingContextLine is the text the brief may read for one part:
// "at Mission District · heart rate raised (avg 104, peak 131)". It is empty
// when there is nothing to say, and never holds coordinates.
func mobileRecordingContextLine(ctx *MobileRecordingPartContext) string {
	if ctx == nil {
		return ""
	}
	var parts []string
	if ctx.Location != nil && ctx.Location.Place != "" {
		parts = append(parts, "at "+ctx.Location.Place)
	}
	if hr := ctx.HeartRate; hr != nil {
		parts = append(parts, fmt.Sprintf("heart rate %s (avg %.0f, peak %.0f)", mobileRecordingIntensity(hr.Avg), hr.Avg, hr.Max))
	}
	return strings.Join(parts, " · ")
}

// applyMobileRecordingContext writes each part's context line into the window
// title of the speech events that fall inside that part, which is where the
// triage and the brief read what surrounded the words.
func applyMobileRecordingContext(events []activitymon.ActivityEvent, parts []mobileRecordingPart, appName string) {
	for _, part := range parts {
		line := mobileRecordingContextLine(part.Context)
		if line == "" {
			continue
		}
		start, err := time.Parse(time.RFC3339, part.StartedAt)
		if err != nil {
			continue
		}
		from := start.UnixMilli()
		to := start.Add(time.Duration(part.DurationSeconds * float64(time.Second))).UnixMilli()
		for i := range events {
			if events[i].Timestamp >= from && events[i].Timestamp <= to {
				events[i].WindowTitle = appName + " · " + line
			}
		}
	}
}

// dropContext removes the heart rate and location of every part of a
// recording. It goes whenever the audio goes.
func (r *mobileRecording) dropContext() {
	for i := range r.Received {
		r.Received[i].Context = nil
	}
}
