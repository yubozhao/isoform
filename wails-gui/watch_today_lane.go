package main

import (
	"strings"
	"time"
)

// "Your watch today": the day's hours as columns of talk (design's watch
// v1.1.0, rule 42). One column for each clock hour from the first hour the
// watch recorded today up to now: as tall as the talk heard in it, marked
// when observing was paused for part of it, empty when it was quiet. It
// answers what a person needs to know about the day (was it listening, what
// did it keep) without a running clock anywhere.
//
// It is computed from what is already stored: the recordings' parts, the
// speech in their transcripts, and the pause log. Nothing is saved.

// WatchLane is today's hours.
type WatchLane struct {
	// DayStart is local midnight, Unix milliseconds.
	DayStart int64           `json:"dayStart"`
	Hours    []WatchLaneHour `json:"hours"`
}

// WatchLaneHour is one clock hour of the lane.
type WatchLaneHour struct {
	// Start is the hour's beginning, Unix milliseconds.
	Start int64 `json:"start"`
	// RecordedSeconds is how much of the hour the watch recorded.
	RecordedSeconds int `json:"recordedSeconds"`
	// TalkSeconds estimates how much of it was speech, from the words heard.
	TalkSeconds int `json:"talkSeconds"`
	// PausedSeconds is how much of the hour observing was paused.
	PausedSeconds int `json:"pausedSeconds"`
	// Current is true for the hour that is not over yet.
	Current bool `json:"current"`
}

// overlapSeconds is how long [aFrom, aTo) and [bFrom, bTo) overlap, in
// milliseconds in, seconds out.
func overlapSeconds(aFrom, aTo, bFrom, bTo int64) int {
	from, to := max(aFrom, bFrom), min(aTo, bTo)
	if to <= from {
		return 0
	}
	return int((to - from) / 1000)
}

// buildWatchLane lays today's recordings, speech and pauses over the clock
// hours. With nothing recorded today it has no hours.
func buildWatchLane(recordings []mobileRecording, speech []watchConversationSpeech, pauses []observationPause, now time.Time) WatchLane {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	lane := WatchLane{DayStart: dayStart.UnixMilli(), Hours: []WatchLaneHour{}}
	type span struct{ from, to int64 }
	var recorded []span
	first := int64(0)
	for _, rec := range recordings {
		if rec.State == mobileRecordingDeleted {
			continue
		}
		for _, part := range rec.Received {
			start, err := time.Parse(time.RFC3339, part.StartedAt)
			if err != nil {
				continue
			}
			from := start.UnixMilli()
			to := start.Add(time.Duration(part.DurationSeconds * float64(time.Second))).UnixMilli()
			if to <= lane.DayStart || from > now.UnixMilli() {
				continue
			}
			from = max(from, lane.DayStart)
			recorded = append(recorded, span{from, to})
			if first == 0 || from < first {
				first = from
			}
		}
	}
	if first == 0 {
		return lane
	}
	// By the wall clock, so a zone on the half hour still gets whole clock hours.
	begun := time.UnixMilli(first).In(now.Location())
	hour := time.Date(begun.Year(), begun.Month(), begun.Day(), begun.Hour(), 0, 0, 0, now.Location())
	for !hour.After(now) {
		from, to := hour.UnixMilli(), hour.Add(time.Hour).UnixMilli()
		entry := WatchLaneHour{Start: from, Current: now.UnixMilli() < to}
		for _, r := range recorded {
			entry.RecordedSeconds += overlapSeconds(r.from, r.to, from, to)
		}
		entry.RecordedSeconds = min(entry.RecordedSeconds, 3600)
		for _, s := range speech {
			if s.At < from || s.At >= to {
				continue
			}
			words := len(strings.Fields(s.Text))
			entry.TalkSeconds += int(float64(words) / watchConversationWordsPerSecond)
		}
		// Talk cannot be longer than what was recorded.
		entry.TalkSeconds = min(entry.TalkSeconds, entry.RecordedSeconds)
		for _, p := range pauses {
			end := p.End
			if end == 0 {
				end = now.UnixMilli()
			}
			entry.PausedSeconds += overlapSeconds(p.Start, end, from, min(to, now.UnixMilli()))
		}
		entry.PausedSeconds = min(entry.PausedSeconds, 3600)
		lane.Hours = append(lane.Hours, entry)
		hour = hour.Add(time.Hour)
	}
	return lane
}

// watchLanePauses reads the pause log for the lane. Without a store there is
// no log; a log that cannot be read shows no pauses rather than wrong ones.
var watchLanePauses = func(a *App) []observationPause {
	if a == nil || a.store == nil {
		return nil
	}
	pauses, err := a.observationPauses()
	if err != nil {
		return nil
	}
	return pauses
}

// GetWatchLane returns today's hours from the watch and the phone.
func (a *App) GetWatchLane() (WatchLane, error) {
	now := time.Now()
	store := a.watchRecordingStore()
	if store == nil {
		return buildWatchLane(nil, nil, nil, now), nil
	}
	recordings, err := store.all()
	if err != nil {
		return WatchLane{}, err
	}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	// A recording that began before midnight may run into today.
	speech, err := a.watchConversationSpeechOf(recordings, dayStart.Add(-2*time.Hour))
	if err != nil {
		return WatchLane{}, err
	}
	return buildWatchLane(recordings, speech, watchLanePauses(a), now), nil
}
