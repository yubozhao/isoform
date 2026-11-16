package main

import (
	"strings"
	"testing"
	"time"
)

// A day on the watch: it recorded 9:40 to 11:20 (three hour recordings), was
// quiet until one, and recorded 1:00 to 1:30. Bo and Sam talked across ten
// o'clock. Observing was paused on the computer from 10:30 to 10:45. It is
// now 1:40 in the afternoon.
func TestTheLaneShowsTheDaysHoursAsTalkPausesAndQuiet(t *testing.T) {
	zone := time.FixedZone("test", -8*3600)
	at := func(hour, minute int) time.Time { return time.Date(2026, 11, 14, hour, minute, 0, 0, zone) }
	part := func(from time.Time, minutes float64) mobileRecordingPart {
		return mobileRecordingPart{StartedAt: from.UTC().Format(time.RFC3339), DurationSeconds: minutes * 60}
	}
	recordings := []mobileRecording{
		{ID: "nine", State: mobileRecordingTranscribed, Received: []mobileRecordingPart{part(at(9, 40), 20)}},
		{ID: "ten", State: mobileRecordingTranscribed, Received: []mobileRecordingPart{part(at(10, 0), 15), part(at(10, 15), 45)}},
		{ID: "eleven", State: mobileRecordingTranscribed, Received: []mobileRecordingPart{part(at(11, 0), 20)}},
		{ID: "one", State: mobileRecordingReceived, Received: []mobileRecordingPart{part(at(13, 0), 30)}},
		{ID: "gone", State: mobileRecordingDeleted, Received: []mobileRecordingPart{part(at(12, 0), 60)}},
		{ID: "yesterday", State: mobileRecordingTranscribed, Received: []mobileRecordingPart{part(at(9, 0).Add(-24*time.Hour), 60)}},
	}
	// 150 words is one minute of talk.
	minute := strings.TrimSpace(strings.Repeat("word ", 150))
	say := func(rec string, when time.Time, minutes int) []watchConversationSpeech {
		var out []watchConversationSpeech
		for i := 0; i < minutes; i++ {
			out = append(out, watchConversationSpeech{At: when.Add(time.Duration(i) * time.Minute).UnixMilli(), RecordingID: rec, DeviceID: "d", Text: minute})
		}
		return out
	}
	var speech []watchConversationSpeech
	speech = append(speech, say("nine", at(9, 50), 10)...)
	speech = append(speech, say("ten", at(10, 0), 20)...)
	pauses := []observationPause{{Start: at(10, 30).UnixMilli(), End: at(10, 45).UnixMilli()}}

	lane := buildWatchLane(recordings, speech, pauses, at(13, 40))
	if lane.DayStart != at(0, 0).UnixMilli() {
		t.Fatalf("day start %v", time.UnixMilli(lane.DayStart))
	}
	if len(lane.Hours) != 5 {
		t.Fatalf("%d hours, want 9 AM through 1 PM: %+v", len(lane.Hours), lane.Hours)
	}
	want := []WatchLaneHour{
		{Start: at(9, 0).UnixMilli(), RecordedSeconds: 20 * 60, TalkSeconds: 10 * 60},
		{Start: at(10, 0).UnixMilli(), RecordedSeconds: 60 * 60, TalkSeconds: 20 * 60, PausedSeconds: 15 * 60},
		{Start: at(11, 0).UnixMilli(), RecordedSeconds: 20 * 60},
		{Start: at(12, 0).UnixMilli()}, // quiet: the deleted recording does not count
		{Start: at(13, 0).UnixMilli(), RecordedSeconds: 30 * 60, Current: true},
	}
	for i, hour := range lane.Hours {
		if hour != want[i] {
			t.Errorf("hour %d: got %+v, want %+v", i, hour, want[i])
		}
	}
}

func TestTheLaneIsEmptyBeforeTheWatchRecordsAnythingToday(t *testing.T) {
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	yesterday := mobileRecording{ID: "y", State: mobileRecordingTranscribed, Received: []mobileRecordingPart{
		{StartedAt: now.Add(-20 * time.Hour).Format(time.RFC3339), DurationSeconds: 600}}}
	if lane := buildWatchLane([]mobileRecording{yesterday}, nil, nil, now); len(lane.Hours) != 0 {
		t.Fatalf("hours %+v", lane.Hours)
	}
	if lane := buildWatchLane(nil, nil, []observationPause{{Start: now.Add(-time.Hour).UnixMilli()}}, now); len(lane.Hours) != 0 {
		t.Fatalf("a pause alone made hours: %+v", lane.Hours)
	}
}

func TestAnOpenPauseAndTalkNeverExceedWhatWasRecorded(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 30, 0, 0, time.UTC)
	rec := mobileRecording{ID: "r", State: mobileRecordingTranscribed, Received: []mobileRecordingPart{
		{StartedAt: now.Add(-30 * time.Minute).Format(time.RFC3339), DurationSeconds: 600}}}
	speech := []watchConversationSpeech{{At: now.Add(-25 * time.Minute).UnixMilli(), Text: strings.Repeat("word ", 9000)}}
	lane := buildWatchLane([]mobileRecording{rec}, speech, []observationPause{{Start: now.Add(-10 * time.Minute).UnixMilli()}}, now)
	if len(lane.Hours) != 1 {
		t.Fatalf("hours %+v", lane.Hours)
	}
	hour := lane.Hours[0]
	if hour.TalkSeconds != 600 || hour.RecordedSeconds != 600 || hour.PausedSeconds != 600 || !hour.Current {
		t.Fatalf("hour %+v", hour)
	}
}
