package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Who "Speaker 1" is (design's watch-voice v1.0.0, rules 11 and 12; PROTOCOL.md
// 24.13). A watch transcript labels the owner "you" and everyone else
// speaker_N. The person can name a speaker, and when the calendar makes one
// name likely the computer offers it as a guess to be judged: Yes, No, or Edit
// (type the name).
//
// A guess is made only when it is nearly certain: the conversation has one
// unnamed speaker, exactly one calendar event overlaps it, and that event has
// exactly one other person with a name. Anything less and nobody is guessed.
//
// Kept in the recordings folder (speaker-names.json, 0600), keyed by the
// conversation and tied to its words by hash. A name goes when the words go:
// reading prunes entries that fit no conversation, and deleting or
// withdrawing a recording drops the entries made from it. A name is shown
// with the conversation only; it is not used in the brief, claims or work.

const (
	watchSpeakerNamesFile = "speaker-names.json"
	// watchSpeakerNameLimit bounds a name the person types.
	watchSpeakerNameLimit = 60
)

// WatchSpeakerGuess is who a speaker might be, to be judged by the person.
type WatchSpeakerGuess struct {
	Speaker string `json:"speaker"`
	Name    string `json:"name"`
	// Reason is "calendar": an event at that time was with this person.
	Reason string `json:"reason"`
	// EventStartAt is when that event began, Unix milliseconds.
	EventStartAt int64 `json:"eventStartAt,omitempty"`
}

// watchSpeakerNamesEntry is what the person said about one conversation's
// speakers. Names are the names given; Declined the guesses they said No to.
type watchSpeakerNamesEntry struct {
	Hash         string              `json:"hash"`
	StartAt      int64               `json:"startAt,omitempty"`
	RecordingIDs []string            `json:"recordingIds,omitempty"`
	Names        map[string]string   `json:"names,omitempty"`
	Declined     map[string][]string `json:"declined,omitempty"`
}

var (
	watchSpeakerNamesMu    sync.Mutex
	errWatchSpeakerUnknown = errors.New("that speaker is not in the conversation")
	errWatchSpeakerName    = errors.New("the name is not usable")
)

func readWatchSpeakerNames(dir string) map[string]watchSpeakerNamesEntry {
	names := map[string]watchSpeakerNamesEntry{}
	raw, err := os.ReadFile(filepath.Join(dir, watchSpeakerNamesFile))
	if err != nil {
		return names
	}
	_ = json.Unmarshal(raw, &names)
	return names
}

func writeWatchSpeakerNames(dir string, names map[string]watchSpeakerNamesEntry) error {
	path := filepath.Join(dir, watchSpeakerNamesFile)
	if len(names) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	raw, err := json.MarshalIndent(names, "", "  ")
	if err != nil {
		return err
	}
	return mobileRecordingWriteFile(path, raw, 0o600)
}

// dropWatchSpeakerNamesOf removes what was said about the speakers of
// conversations made from a recording: it was withdrawn or deleted.
func dropWatchSpeakerNamesOf(dir, recordingID string) {
	watchSpeakerNamesMu.Lock()
	defer watchSpeakerNamesMu.Unlock()
	names := readWatchSpeakerNames(dir)
	changed := false
	for key, entry := range names {
		if containsString(entry.RecordingIDs, recordingID) {
			delete(names, key)
			changed = true
		}
	}
	if changed {
		if err := writeWatchSpeakerNames(dir, names); err != nil {
			log.Printf("[recordings] speaker names not dropped: %v", err)
		}
	}
}

// pruneWatchSpeakerNames drops the entries that fit no conversation any more
// and returns the ones that do.
func pruneWatchSpeakerNames(dir string, convs []WatchConversation) map[string]watchSpeakerNamesEntry {
	watchSpeakerNamesMu.Lock()
	defer watchSpeakerNamesMu.Unlock()
	names := readWatchSpeakerNames(dir)
	if len(names) == 0 {
		return names
	}
	hashes := map[string]string{}
	for _, c := range convs {
		hashes[c.ID] = watchConversationHash(c)
	}
	changed := false
	for key, entry := range names {
		if hashes[key] != entry.Hash {
			delete(names, key)
			changed = true
		}
	}
	if changed {
		if err := writeWatchSpeakerNames(dir, names); err != nil {
			log.Printf("[recordings] speaker names not pruned: %v", err)
		}
	}
	return names
}

// cleanWatchSpeakerName keeps a short, printable, one-line name.
func cleanWatchSpeakerName(raw string) (string, bool) {
	name := strings.Join(strings.Fields(raw), " ")
	if name == "" || len(name) > watchSpeakerNameLimit || !utf8.ValidString(name) {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '<' || r == '>' {
			return "", false
		}
	}
	if name == mobileRecordingSpeakerOwner || strings.HasPrefix(name, mobileRecordingSpeakerPrefix) {
		return "", false
	}
	return name, true
}

// watchSpeakerCalendarGuess guesses the one unnamed speaker of a conversation
// from the calendar, or returns nil. events are the events around it, with
// their attendees.
func watchSpeakerCalendarGuess(c WatchConversation, entry watchSpeakerNamesEntry, events []watchSpeakerEvent) *WatchSpeakerGuess {
	var unnamed []string
	for _, speaker := range c.Speakers {
		if strings.HasPrefix(speaker, mobileRecordingSpeakerPrefix) && entry.Names[speaker] == "" {
			unnamed = append(unnamed, speaker)
		}
	}
	if len(unnamed) != 1 {
		return nil
	}
	var overlapping []watchSpeakerEvent
	for _, event := range events {
		if event.StartAt < c.EndAt && event.EndAt > c.StartAt {
			overlapping = append(overlapping, event)
		}
	}
	if len(overlapping) != 1 || len(overlapping[0].Others) != 1 {
		return nil
	}
	name, ok := cleanWatchSpeakerName(overlapping[0].Others[0])
	if !ok || containsString(entry.Declined[unnamed[0]], name) {
		return nil
	}
	for _, given := range entry.Names {
		if given == name {
			return nil
		}
	}
	return &WatchSpeakerGuess{Speaker: unnamed[0], Name: name, Reason: "calendar", EventStartAt: overlapping[0].StartAt}
}

// watchSpeakerEvent is a calendar event reduced to what a guess needs.
type watchSpeakerEvent struct {
	StartAt, EndAt int64
	// Others are the names of the people on it who are not the owner.
	Others []string
}

// watchSpeakerEvents reads the calendar events between from and to. It is a
// variable so tests need no calendar. Without a store there are none.
var watchSpeakerEvents = func(a *App, from, to int64) []watchSpeakerEvent {
	if a == nil || a.store == nil {
		return nil
	}
	rows, err := a.store.ListCalendarEvents(context.Background(), from, to, false)
	if err != nil {
		return nil
	}
	var out []watchSpeakerEvent
	for _, row := range rows {
		if row.AllDay || row.Status == "cancelled" {
			continue
		}
		event := watchSpeakerEvent{StartAt: row.StartAt, EndAt: row.EndAt}
		for _, attendee := range row.Attendees {
			if attendee.IsMe {
				continue
			}
			// A person with no name on the invite is still a person: the event then has more than a
			// named one, and no guess is made from it.
			event.Others = append(event.Others, strings.TrimSpace(attendee.Name))
		}
		out = append(out, event)
	}
	return out
}

// applyWatchSpeakerNames sets, on each conversation, the names the person gave
// and the guess still open.
func (a *App) applyWatchSpeakerNames(convs []WatchConversation, names map[string]watchSpeakerNamesEntry) {
	if len(convs) == 0 {
		return
	}
	from, to := convs[0].StartAt, convs[0].EndAt
	for _, c := range convs {
		if c.StartAt < from {
			from = c.StartAt
		}
		if c.EndAt > to {
			to = c.EndAt
		}
	}
	events := watchSpeakerEvents(a, from-int64(time.Hour/time.Millisecond), to+int64(time.Hour/time.Millisecond))
	for i := range convs {
		entry := names[convs[i].ID]
		convs[i].SpeakerNames = map[string]string{}
		for speaker, name := range entry.Names {
			if containsString(convs[i].Speakers, speaker) {
				convs[i].SpeakerNames[speaker] = name
			}
		}
		convs[i].SpeakerGuess = watchSpeakerCalendarGuess(convs[i], entry, events)
	}
}

// answerWatchSpeaker records what the person said about a speaker: "yes" to
// the open guess, "no" to it, or "edit" with a name they typed. An empty name
// with "edit" takes a given name away.
func (a *App) answerWatchSpeaker(store *mobileRecordingStore, deviceID, conversationID, speaker, answer, name string) error {
	watchCutMu.RLock()
	defer watchCutMu.RUnlock()
	if store == nil {
		return errMobileStoreUnavailable
	}
	convs, err := a.watchConversationsOf(store)
	if err != nil {
		return err
	}
	var conv *WatchConversation
	for i := range convs {
		if convs[i].ID == conversationID && (deviceID == "" || mobileConversationOwned(convs[i], deviceID)) {
			conv = &convs[i]
			break
		}
	}
	if conv == nil {
		return errMobileNotFound
	}
	if !strings.HasPrefix(speaker, mobileRecordingSpeakerPrefix) || !containsString(conv.Speakers, speaker) {
		return errWatchSpeakerUnknown
	}
	watchSpeakerNamesMu.Lock()
	defer watchSpeakerNamesMu.Unlock()
	names := readWatchSpeakerNames(store.dir)
	hash := watchConversationHash(*conv)
	entry := names[conv.ID]
	if entry.Hash != hash {
		entry = watchSpeakerNamesEntry{}
	}
	entry.Hash, entry.StartAt, entry.RecordingIDs = hash, conv.StartAt, append([]string{}, conv.RecordingIDs...)
	if entry.Names == nil {
		entry.Names = map[string]string{}
	}
	if entry.Declined == nil {
		entry.Declined = map[string][]string{}
	}
	guess := conv.SpeakerGuess
	switch answer {
	case "yes":
		if guess == nil || guess.Speaker != speaker {
			return errWatchSpeakerUnknown
		}
		entry.Names[speaker] = guess.Name
	case "no":
		if guess == nil || guess.Speaker != speaker {
			return errWatchSpeakerUnknown
		}
		if !containsString(entry.Declined[speaker], guess.Name) {
			entry.Declined[speaker] = append(entry.Declined[speaker], guess.Name)
		}
	case "edit":
		if strings.TrimSpace(name) == "" {
			delete(entry.Names, speaker)
			break
		}
		clean, ok := cleanWatchSpeakerName(name)
		if !ok {
			return errWatchSpeakerName
		}
		entry.Names[speaker] = clean
	default:
		return errWatchSpeakerName
	}
	names[conv.ID] = entry
	return writeWatchSpeakerNames(store.dir, names)
}

// MobileSpeakerAnswerRequest is the body of POST /recordings/conversation/speaker.
type MobileSpeakerAnswerRequest struct {
	ConversationID string `json:"conversationId"`
	Speaker        string `json:"speaker"`
	Answer         string `json:"answer"`
	Name           string `json:"name,omitempty"`
}

// serveRecordingSpeakerAnswer handles POST /recordings/conversation/speaker (24.13).
func (bridge *mobileBridge) serveRecordingSpeakerAnswer(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var input MobileSpeakerAnswerRequest
	if !mobileDecode(w, r, &input) {
		return
	}
	store := bridge.conversationStore(w)
	if store == nil {
		return
	}
	if bridge.app.IsClosedEyesModeEnabled() {
		mobileJSON(w, http.StatusNotFound, nil, errMobileNotFound.Error())
		return
	}
	switch err := bridge.app.answerWatchSpeaker(store, device.DeviceID, input.ConversationID, input.Speaker, input.Answer, input.Name); {
	case err == nil:
	case errors.Is(err, errMobileNotFound):
		mobileJSON(w, http.StatusNotFound, nil, errMobileNotFound.Error())
		return
	case errors.Is(err, errWatchSpeakerUnknown), errors.Is(err, errWatchSpeakerName):
		mobileJSON(w, http.StatusBadRequest, nil, err.Error())
		return
	default:
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	_, mine, byID, err := bridge.app.mobileConversationsFor(store, device.DeviceID, time.Now())
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	for _, c := range mine {
		if c.ID == input.ConversationID {
			mobileJSON(w, http.StatusOK, mobileConversationView(c, byID), "")
			return
		}
	}
	mobileJSON(w, http.StatusNotFound, nil, errMobileNotFound.Error())
}

// Who a speaker is (24.13): the answer route, and the flag in the status.
func init() {
	mobileRegisterRoute("POST /recordings/conversation/speaker", []string{mobileScopeChatWrite, mobileScopeBriefRead}, func(bridge *mobileBridge, w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
		bridge.serveRecordingSpeakerAnswer(w, r, device)
	})
	mobileRegisterStatus(func(_ *mobileBridge, status map[string]any) { status["recordingSpeakers"] = true })
}
