package main

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

// Conversations for the phone (PROTOCOL.md 24.11): what the watch heard, as
// the phone's Watch tab shows it. One read gives the last days' conversations
// (name, when, who spoke, marks, notes and follow-ups, place and heart-rate
// words) and today's hours for the strip; a second read gives one
// conversation's words.
//
// Nothing is stored for it: it is the computer's own conversations
// (watch_conversations.go) and lane (watch_today_lane.go), limited to the
// recordings this phone sent. While observing is paused nothing that was heard
// is read out, here as elsewhere.

const (
	// mobileConversationsMax bounds one answer: it travels in one sealed envelope.
	mobileConversationsMax = 80
	// mobileConversationMarkAfter: a mark this long after the last word still
	// belongs to the conversation (the wearer marks what was just said).
	mobileConversationMarkAfter = 30 * time.Second
)

// MobileConversationsView is the answer of GET /recordings/conversations.
type MobileConversationsView struct {
	// DayStart is the computer's local midnight today.
	DayStart string `json:"dayStart"`
	// Hours are today's clock hours since the watch first recorded.
	Hours []MobileConversationHour `json:"hours"`
	// Closed are today's stretches when observing was paused.
	Closed []MobileConversationSpan `json:"closed"`
	// Conversations are the last days' conversations, newest first.
	Conversations []MobileConversationView `json:"conversations"`
	Truncated     bool                     `json:"truncated,omitempty"`
}

// MobileConversationHour is one clock hour of today's strip.
type MobileConversationHour struct {
	Start           string `json:"start"`
	RecordedSeconds int    `json:"recordedSeconds"`
	TalkSeconds     int    `json:"talkSeconds"`
	PausedSeconds   int    `json:"pausedSeconds"`
	Current         bool   `json:"current,omitempty"`
}

// MobileConversationSpan is a stretch of time.
type MobileConversationSpan struct {
	StartAt string `json:"startAt"`
	// EndAt is empty while the pause has not ended.
	EndAt string `json:"endAt,omitempty"`
}

// MobileConversationView is one conversation without its words.
type MobileConversationView struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	StartAt string `json:"startAt"`
	EndAt   string `json:"endAt"`
	// TalkSeconds estimates how long people spoke, from the words heard.
	TalkSeconds int `json:"talkSeconds"`
	// Speakers are the voice labels heard, "you" first.
	Speakers []string `json:"speakers"`
	// Marks are the moments the wearer marked in it.
	Marks        []string `json:"marks"`
	RecordingIDs []string `json:"recordingIds"`
	// Summary, Decisions and Followups are its notes (over five minutes, when
	// notes are on); empty otherwise.
	Summary   string                      `json:"summary,omitempty"`
	Decisions []string                    `json:"decisions"`
	Followups []WatchConversationFollowup `json:"followups"`
	// ItemIDs are the owner's own follow-ups as items of the one list of work
	// ("followup:<commitment id>"); the phone shows the same items in both places.
	ItemIDs []string `json:"itemIds"`
	// Place is the area it was heard in, and HeartRate "calm", "raised" or
	// "high", when the wearer shares them.
	Place     string `json:"place,omitempty"`
	HeartRate string `json:"heartRate,omitempty"`
	// SpeakerNames are the names the person gave its speakers, by label, and
	// SpeakerGuess who one of them might be, still to be judged (24.13).
	SpeakerNames map[string]string  `json:"speakerNames,omitempty"`
	SpeakerGuess *WatchSpeakerGuess `json:"speakerGuess,omitempty"`
}

// MobileConversationDetail is the answer of GET /recordings/conversation.
type MobileConversationDetail struct {
	Conversation MobileConversationView          `json:"conversation"`
	Lines        []MobileRecordingTranscriptLine `json:"lines"`
	Truncated    bool                            `json:"truncated,omitempty"`
}

// mobileConversationOwned reports a conversation heard through this device.
// A conversation's id begins with the device that sent its recordings.
func mobileConversationOwned(c WatchConversation, deviceID string) bool {
	return deviceID != "" && strings.HasPrefix(c.ID, deviceID+":")
}

// mobileConversationView builds the phone's view of one conversation from the
// recordings it draws from.
func mobileConversationView(c WatchConversation, byID map[string]mobileRecording) MobileConversationView {
	view := MobileConversationView{ID: c.ID, Name: c.Name,
		StartAt: mobileTimestamp(time.UnixMilli(c.StartAt)), EndAt: mobileTimestamp(time.UnixMilli(c.EndAt)),
		TalkSeconds: int(float64(c.Words) / watchConversationWordsPerSecond),
		Speakers:    append([]string{}, c.Speakers...), Marks: []string{}, RecordingIDs: append([]string{}, c.RecordingIDs...),
		Summary: c.Summary, Decisions: append([]string{}, c.Decisions...), Followups: append([]WatchConversationFollowup{}, c.Followups...),
		ItemIDs: append([]string{}, c.ItemIDs...), SpeakerNames: c.SpeakerNames, SpeakerGuess: c.SpeakerGuess}
	var marks []int64
	var heartSum float64
	heartParts := 0
	for _, id := range c.RecordingIDs {
		rec, ok := byID[id]
		if !ok {
			continue
		}
		for _, at := range rec.marksAt() {
			if at >= c.StartAt && at <= c.EndAt+mobileConversationMarkAfter.Milliseconds() {
				marks = append(marks, at)
			}
		}
		for _, part := range rec.Received {
			if part.Context == nil {
				continue
			}
			start, err := time.Parse(time.RFC3339, part.StartedAt)
			if err != nil {
				continue
			}
			from := start.UnixMilli()
			to := start.Add(time.Duration(part.DurationSeconds * float64(time.Second))).UnixMilli()
			if to < c.StartAt || from > c.EndAt {
				continue
			}
			if view.Place == "" && part.Context.Location != nil {
				view.Place = part.Context.Location.Place
			}
			if hr := part.Context.HeartRate; hr != nil {
				heartSum += hr.Avg
				heartParts++
			}
		}
	}
	sort.Slice(marks, func(i, j int) bool { return marks[i] < marks[j] })
	for _, at := range marks {
		view.Marks = append(view.Marks, mobileTimestamp(time.UnixMilli(at)))
	}
	if heartParts > 0 {
		view.HeartRate = mobileRecordingIntensity(heartSum / float64(heartParts))
	}
	return view
}

// mobileConversationsFor builds the phone's whole view from this device's
// recordings.
func (a *App) mobileConversationsFor(store *mobileRecordingStore, deviceID string, now time.Time) (MobileConversationsView, []WatchConversation, map[string]mobileRecording, error) {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	out := MobileConversationsView{DayStart: mobileTimestamp(dayStart), Hours: []MobileConversationHour{},
		Closed: []MobileConversationSpan{}, Conversations: []MobileConversationView{}}
	all, err := store.all()
	if err != nil {
		return out, nil, nil, err
	}
	var own []mobileRecording
	byID := map[string]mobileRecording{}
	for _, rec := range all {
		if rec.DeviceID == deviceID && rec.State != mobileRecordingDeleted {
			own = append(own, rec)
			byID[rec.ID] = rec
		}
	}
	convs, err := a.watchConversationsOf(store)
	if err != nil {
		return out, nil, nil, err
	}
	var mine []WatchConversation
	for _, c := range convs {
		if !mobileConversationOwned(c, deviceID) {
			continue
		}
		mine = append(mine, c)
		if len(out.Conversations) >= mobileConversationsMax {
			out.Truncated = true
			continue
		}
		out.Conversations = append(out.Conversations, mobileConversationView(c, byID))
	}
	pauses := watchLanePauses(a)
	lane := buildWatchLane(own, a.watchConversationSpeechOf(own, dayStart.Add(-2*time.Hour)), pauses, now)
	for _, hour := range lane.Hours {
		out.Hours = append(out.Hours, MobileConversationHour{Start: mobileTimestamp(time.UnixMilli(hour.Start)),
			RecordedSeconds: hour.RecordedSeconds, TalkSeconds: hour.TalkSeconds, PausedSeconds: hour.PausedSeconds, Current: hour.Current})
	}
	for _, p := range pauses {
		if p.End != 0 && p.End <= dayStart.UnixMilli() || p.Start > now.UnixMilli() {
			continue
		}
		from := p.Start
		if from < dayStart.UnixMilli() {
			from = dayStart.UnixMilli()
		}
		span := MobileConversationSpan{StartAt: mobileTimestamp(time.UnixMilli(from))}
		if p.End != 0 {
			span.EndAt = mobileTimestamp(time.UnixMilli(p.End))
		}
		out.Closed = append(out.Closed, span)
	}
	return out, mine, byID, nil
}

func (bridge *mobileBridge) conversationStore(w http.ResponseWriter) *mobileRecordingStore {
	bridge.mu.Lock()
	store := bridge.recordingStoreLocked()
	bridge.mu.Unlock()
	if store == nil || bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return nil
	}
	return store
}

// serveRecordingConversations handles GET /recordings/conversations (24.11).
func (bridge *mobileBridge) serveRecordingConversations(w http.ResponseWriter, _ *http.Request, device mobileDeviceRecord) {
	store := bridge.conversationStore(w)
	if store == nil {
		return
	}
	now := time.Now()
	if bridge.app.IsClosedEyesModeEnabled() {
		// Paused: nothing that was heard is read out, here as elsewhere.
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		mobileJSON(w, http.StatusOK, MobileConversationsView{DayStart: mobileTimestamp(dayStart), Hours: []MobileConversationHour{},
			Closed: []MobileConversationSpan{}, Conversations: []MobileConversationView{}}, "")
		return
	}
	view, _, _, err := bridge.app.mobileConversationsFor(store, device.DeviceID, now)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, view, "")
}

// serveRecordingConversation handles GET /recordings/conversation?id= (24.11).
func (bridge *mobileBridge) serveRecordingConversation(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	store := bridge.conversationStore(w)
	if store == nil {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" || len(id) > 120 {
		mobileJSON(w, http.StatusBadRequest, nil, "the conversation id is not valid")
		return
	}
	if bridge.app.IsClosedEyesModeEnabled() {
		mobileJSON(w, http.StatusNotFound, nil, errMobileNotFound.Error())
		return
	}
	_, mine, byID, err := bridge.app.mobileConversationsFor(store, device.DeviceID, time.Now())
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	for _, c := range mine {
		if c.ID != id {
			continue
		}
		out := MobileConversationDetail{Conversation: mobileConversationView(c, byID), Lines: []MobileRecordingTranscriptLine{}}
		var marks []int64
		for _, recID := range c.RecordingIDs {
			if rec, ok := byID[recID]; ok {
				marks = append(marks, rec.marksAt()...)
			}
		}
		sort.Slice(marks, func(i, j int) bool { return marks[i] < marks[j] })
		lines := append([]WatchRecordingLine{}, c.Lines...)
		markWatchRecordingLines(lines, marks)
		size := 0
		for _, line := range lines {
			size += len(line.Text)
			if len(out.Lines) >= mobileRecordingTranscriptMaxLines || size > mobileRecordingTranscriptMaxBytes {
				out.Truncated = true
				break
			}
			out.Lines = append(out.Lines, MobileRecordingTranscriptLine{
				At: mobileTimestamp(time.UnixMilli(line.At)), Speaker: line.Speaker, Text: line.Text, Marked: line.Marked})
		}
		mobileJSON(w, http.StatusOK, out, "")
		return
	}
	mobileJSON(w, http.StatusNotFound, nil, errMobileNotFound.Error())
}

// Conversations for the phone (24.11): the two reads, and the flag in the status.
func init() {
	mobileRegisterRoute("GET /recordings/conversations", []string{mobileScopeChatWrite, mobileScopeBriefRead}, func(bridge *mobileBridge, w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
		bridge.serveRecordingConversations(w, r, device)
	})
	mobileRegisterRoute("GET /recordings/conversation", []string{mobileScopeChatWrite, mobileScopeBriefRead}, func(bridge *mobileBridge, w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
		bridge.serveRecordingConversation(w, r, device)
	})
	mobileRegisterStatus(func(_ *mobileBridge, status map[string]any) { status["recordingConversations"] = true })
}
