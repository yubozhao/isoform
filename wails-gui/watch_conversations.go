package main

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"wails-gui/internal/platform/activitymon"
)

// Conversations from the watch's hours.
//
// The watch keeps recordings by the clock hour, so a talk from 9:50 to 10:20
// arrives as the end of one recording and the start of the next, and the 10
// o'clock recording may also hold a standup that has nothing to do with it.
// The hour stays the unit that is stored, played and deleted. A conversation
// is computed on top of it, from the speech already stored: a run of speech
// with no silence longer than watchConversationSilence, across recordings.
// Nothing is copied or saved; delete an hour and the conversations are simply
// computed again from what is left.

const (
	// watchConversationSilence is how long nobody speaks before a
	// conversation is over. Design's v1.1.0 does not say; change it here.
	watchConversationSilence = 2 * time.Minute
	// watchConversationDays is how far back conversations are computed: the
	// days the watch and the phone list.
	watchConversationDays = 7
	// watchConversationWordsPerSecond estimates how long a stretch of speech
	// lasted from its words; the store keeps only when it began.
	watchConversationWordsPerSecond = 2.5
)

// WatchConversation is one conversation the watch (or the phone) heard.
type WatchConversation struct {
	// ID is stable while the conversation's first words stay: the device, then
	// when it began.
	ID      string `json:"id"`
	StartAt int64  `json:"startAt"`
	EndAt   int64  `json:"endAt"`
	// RecordingIDs are the hours it draws from, in order.
	RecordingIDs []string `json:"recordingIds"`
	Words        int      `json:"words"`
	// Speakers are the distinct voice labels heard, "you" first.
	Speakers []string             `json:"speakers"`
	Lines    []WatchRecordingLine `json:"lines"`
}

// watchConversationSpeech is one stretch of speech and where it came from.
type watchConversationSpeech struct {
	At          int64
	RecordingID string
	DeviceID    string
	Speaker     string
	Text        string
}

// watchConversationSpeechEnd estimates when a stretch of speech ended.
func watchConversationSpeechEnd(s watchConversationSpeech) int64 {
	words := len(strings.Fields(s.Text))
	return s.At + int64(float64(words)/watchConversationWordsPerSecond*1000)
}

// buildWatchConversations groups speech into conversations: per device, in
// time order, a new conversation begins when nobody has spoken for longer
// than silence. Newest first.
func buildWatchConversations(speech []watchConversationSpeech, silence time.Duration) []WatchConversation {
	byDevice := map[string][]watchConversationSpeech{}
	for _, s := range speech {
		if strings.TrimSpace(s.Text) == "" {
			continue
		}
		byDevice[s.DeviceID] = append(byDevice[s.DeviceID], s)
	}
	out := []WatchConversation{}
	for device, items := range byDevice {
		sort.SliceStable(items, func(i, j int) bool { return items[i].At < items[j].At })
		var current *WatchConversation
		flush := func() {
			if current != nil {
				out = append(out, *current)
				current = nil
			}
		}
		for _, s := range items {
			end := watchConversationSpeechEnd(s)
			if current != nil && s.At-current.EndAt > silence.Milliseconds() {
				flush()
			}
			if current == nil {
				current = &WatchConversation{ID: device + ":" + strconv.FormatInt(s.At, 10), StartAt: s.At, EndAt: end,
					RecordingIDs: []string{}, Speakers: []string{}, Lines: []WatchRecordingLine{}}
			}
			if end > current.EndAt {
				current.EndAt = end
			}
			if !containsString(current.RecordingIDs, s.RecordingID) {
				current.RecordingIDs = append(current.RecordingIDs, s.RecordingID)
			}
			if s.Speaker != "" && !containsString(current.Speakers, s.Speaker) {
				if s.Speaker == mobileRecordingSpeakerOwner {
					current.Speakers = append([]string{s.Speaker}, current.Speakers...)
				} else {
					current.Speakers = append(current.Speakers, s.Speaker)
				}
			}
			text := strings.TrimSpace(s.Text)
			current.Words += len(strings.Fields(text))
			current.Lines = append(current.Lines, WatchRecordingLine{At: s.At, Speaker: s.Speaker, Text: text})
		}
		flush()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartAt > out[j].StartAt })
	return out
}

// watchConversationSpeechOf reads the stored speech of the transcribed
// recordings that began after since. A transcript marked private reads as
// nothing, as it does everywhere else.
func (a *App) watchConversationSpeechOf(recordings []mobileRecording, since time.Time) []watchConversationSpeech {
	var speech []watchConversationSpeech
	if a.activity == nil || a.activity.store == nil {
		return speech
	}
	for _, rec := range recordings {
		if rec.State != mobileRecordingTranscribed || rec.SessionID == "" {
			continue
		}
		if started, err := time.Parse(time.RFC3339, rec.view().StartedAt); err != nil || started.Before(since) {
			continue
		}
		events, err := a.activity.store.GetSessionEvents(rec.SessionID)
		if err != nil {
			continue
		}
		for _, evt := range events {
			if evt.Kind != activitymon.EventSpeech {
				continue
			}
			speech = append(speech, watchConversationSpeech{At: evt.Timestamp, RecordingID: rec.ID, DeviceID: rec.DeviceID,
				Speaker: evt.SpeechSpeakerLabel, Text: evt.SpeechText})
		}
	}
	return speech
}

// GetWatchConversations lists the conversations heard on the watch and the
// phone in the last days, newest first, each with what was said.
func (a *App) GetWatchConversations() ([]WatchConversation, error) {
	store := a.watchRecordingStore()
	if store == nil {
		return []WatchConversation{}, nil
	}
	recordings, err := store.all()
	if err != nil {
		return nil, err
	}
	since := time.Now().AddDate(0, 0, -watchConversationDays)
	return buildWatchConversations(a.watchConversationSpeechOf(recordings, since), watchConversationSilence), nil
}
