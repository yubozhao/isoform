package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/workonit"
)

// activityFeedRowKinds is every kind of row the feed can return, each with a
// row built the way GetActivityFeed builds it. A new kind must be added here,
// and so must name its consent (hub class rule, 2026-11-21).
func activityFeedRowKinds(t *testing.T) map[string][]ActivityFeedRow {
	day := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	start, end := day.UnixMilli(), day.Add(24*time.Hour).UnixMilli()
	at := func(h int) int64 { return day.Add(time.Duration(h) * time.Hour).UnixMilli() }
	hour := mobileRecording{ID: "hour", Source: "watch", State: mobileRecordingTranscribed,
		Received: []mobileRecordingPart{{StartedAt: day.Add(9 * time.Hour).Format(time.RFC3339), DurationSeconds: 60}}}
	talk := WatchConversation{ID: "dev:1", StartAt: at(10), EndAt: at(10) + 60000, RecordingIDs: []string{"talk"}, Speakers: []string{"you"}}
	var trouble []ActivityFeedRow
	for _, cause := range []string{troubleScreen, troubleSignedOut, troubleOffline, troubleRestarted, troublePhone} {
		trouble = append(trouble, activityFeedTroubleRows([]TroubleStretch{{StartMs: at(1), EndMs: at(2), Cause: cause}}, start, end, end)...)
	}
	return map[string][]ActivityFeedRow{
		"screen":  activityFeedMomentRows([]ActivityView{{ID: "m", StartMs: at(8), EndMs: at(9), Title: "Pricing page"}}),
		"watch":   activityFeedWatchRows([]mobileRecording{hour}, []WatchConversation{talk}, start, end),
		"agent":   {{Kind: "agent", Basis: ActivityFeedBasis{Setting: basisScreen, Agent: "Codex"}}}, // built by SQL; checked against the real query in TestActivityFeedShowsAgentsAndForgetsOnlyTheirCapture
		"handoff": activityFeedHandoffRows([]workonit.Handoff{{ID: "h", AgentID: "codex", Text: "Write the plan", SentAt: at(3)}}, nil, start, end, end),
		"closed":  activityFeedClosedRows([]observationPause{{Start: at(4), End: at(5)}}, start, end, end),
		"trouble": trouble,
	}
}

// TestActivityFeedEveryRowKindNamesItsConsent: every kind of row the feed
// can return carries a basis, and the table above lists every kind the feed's
// code builds, so a new kind without a basis fails here.
func TestActivityFeedEveryRowKindNamesItsConsent(t *testing.T) {
	kinds := activityFeedRowKinds(t)
	want := map[string]string{"screen": basisScreen, "watch": basisWatch, "agent": basisScreen, "handoff": basisYes, "closed": basisClosedEyes}
	for kind, rows := range kinds {
		if len(rows) == 0 {
			t.Fatalf("no %s row was built", kind)
		}
		for _, row := range rows {
			if row.Kind != kind {
				t.Fatalf("a %s builder made a %s row", kind, row.Kind)
			}
			if strings.TrimSpace(row.Basis.Setting) == "" {
				t.Fatalf("a %s row names no consent: %+v", kind, row)
			}
			if w, ok := want[kind]; ok && row.Basis.Setting != w {
				t.Fatalf("a %s row stands on %q, want %q", kind, row.Basis.Setting, w)
			}
		}
	}
	if len(kinds["watch"]) != 2 {
		t.Fatalf("watch rows = %+v, want the conversation and the quiet hour", kinds["watch"])
	}
	for _, row := range kinds["trouble"] {
		wantTrouble := map[string]string{troubleScreen: basisScreen, troubleRestarted: basisScreen,
			troubleSignedOut: basisAssistant, troubleOffline: basisAssistant, troublePhone: basisWatch}[row.Cause]
		if row.Basis.Setting != wantTrouble {
			t.Fatalf("a %s gap stands on %q, want %q", row.Cause, row.Basis.Setting, wantTrouble)
		}
	}
	// Every kind the feed's code builds is in the table.
	literal := regexp.MustCompile(`ActivityFeedRow\{[^}]*Kind: "([a-z_]+)"`)
	for _, file := range []string{"activity_feed.go", "activity_feed_watch.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range literal.FindAllStringSubmatch(string(src), -1) {
			if _, ok := kinds[m[1]]; !ok {
				t.Fatalf("%s builds %q rows, which the consent table does not list", file, m[1])
			}
		}
	}
	// The keys for rows still to come exist, so their basis is decided now.
	for _, key := range []string{basisLearn, basisMessages} {
		if key == "" {
			t.Fatal("a future basis key is empty")
		}
	}
}

// TestActivityFeedLeavesOutARowWithNoConsent: a row built without a basis is
// never shown, and an unknown trouble cause has none.
func TestActivityFeedLeavesOutARowWithNoConsent(t *testing.T) {
	rows := activityFeedWithBasis([]ActivityFeedRow{
		{ID: "a", Kind: "screen", Basis: ActivityFeedBasis{Setting: basisScreen}},
		{ID: "b", Kind: "screen"},
		{ID: "c", Kind: "watch", Basis: ActivityFeedBasis{Setting: "  "}},
	})
	if len(rows) != 1 || rows[0].ID != "a" {
		t.Fatalf("rows = %+v", rows)
	}
	if got := activityFeedTroubleRows([]TroubleStretch{{StartMs: 1, EndMs: 5, Cause: "weather"}}, 0, 10, 10); len(got) != 1 || got[0].Basis.Setting != "" {
		t.Fatalf("an unknown cause = %+v", got)
	}
}

type watchForgetWorld struct {
	app                     *App
	store                   *mobileRecordingStore
	day                     time.Time
	nine, ten, eleven, noon string
}

// watchRowForgetFixture: yesterday the watch kept 9 AM and 10 AM, where Bo
// talked pricing with Sam from 9:50 to 10:20, then a standup at 10:40 in the
// same 10 AM hour; 11 AM heard nothing; at noon someone said goodbye.
func watchRowForgetFixture(t *testing.T) watchForgetWorld {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	app, _, store, first := watchDeskFixture(t)
	if _, err := app.deleteWatchRecording(store, first, ""); err != nil {
		t.Fatal(err)
	}
	day := time.Now().Add(-24 * time.Hour)
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local)
	w := watchForgetWorld{app: app, store: store, day: day, nine: uuid.NewString(), ten: uuid.NewString(), eleven: uuid.NewString(), noon: uuid.NewString()}
	starts := map[string]time.Time{w.nine: day.Add(9*time.Hour + 40*time.Minute), w.ten: day.Add(10 * time.Hour), w.eleven: day.Add(11 * time.Hour), w.noon: day.Add(12 * time.Hour)}
	events := map[string][]activitymon.ActivityEvent{}
	say := func(id string, at time.Time, speaker, text string) {
		events[id] = append(events[id], activitymon.ActivityEvent{Timestamp: at.UnixMilli(), Kind: activitymon.EventSpeech,
			AppName: mobileRecordingAppName, SpeechText: text, SpeechSpeakerLabel: speaker})
	}
	for i, at := 0, day.Add(9*time.Hour+50*time.Minute); !at.After(day.Add(10*time.Hour + 20*time.Minute)); i, at = i+1, at.Add(90*time.Second) {
		id := w.nine
		if !at.Before(starts[w.ten]) {
			id = w.ten
		}
		if i%2 == 0 {
			say(id, at, "you", "I will send the pricing page tonight")
		} else {
			say(id, at, "speaker_1", "sounds right to me")
		}
	}
	for at := day.Add(10*time.Hour + 40*time.Minute); !at.After(day.Add(10*time.Hour + 50*time.Minute)); at = at.Add(90 * time.Second) {
		say(w.ten, at, "speaker_2", "standup notes go here")
	}
	say(w.noon, day.Add(12*time.Hour+5*time.Minute), "speaker_3", "thanks, see you")
	uploads := &mapUploads{bodies: map[string][]byte{}}
	for _, id := range []string{w.nine, w.ten, w.eleven, w.noon} {
		req := sealedRecordingPart(t, uploads, mobileTestDeviceID, id, 0, true, []byte("audio"), starts[id])
		if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, mobileTestDeviceID, req); err != nil {
			t.Fatal(err)
		}
		if len(events[id]) == 0 {
			store.setState(id, mobileRecordingTranscribed, "", 0, "")
			continue
		}
		session := ActivitySessionRow{ID: "rec-" + id, StartTime: starts[id].UnixMilli(), EndTime: starts[id].Add(time.Hour).UnixMilli(),
			TriggerKind: mobileRecordingTrigger, AppNames: `["` + mobileRecordingAppName + `"]`, EventCount: len(events[id]),
			TriageStatus: "pending", CreatedAt: time.Now().UnixMilli()}
		if err := app.activity.store.SaveSession(session, events[id], nil); err != nil {
			t.Fatal(err)
		}
		store.setState(id, mobileRecordingTranscribed, session.ID, len(events[id])*6, "")
	}
	return w
}

func (w watchForgetWorld) conversations(t *testing.T) (sam, standup WatchConversation) {
	t.Helper()
	convs, err := w.app.GetWatchConversations()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range convs {
		switch {
		case containsString(c.RecordingIDs, w.nine):
			sam = c
		case containsString(c.RecordingIDs, w.ten) && !containsString(c.RecordingIDs, w.nine):
			standup = c
		}
	}
	if sam.ID == "" || standup.ID == "" {
		t.Fatalf("conversations %d, want the Sam talk and the standup", len(convs))
	}
	return sam, standup
}

func (w watchForgetWorld) watchRows(t *testing.T) []ActivityFeedRow {
	t.Helper()
	rows, err := w.app.GetActivityFeed(w.day.UnixMilli(), w.day.Add(24*time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	var out []ActivityFeedRow
	for _, row := range rows {
		if row.Kind == "watch" {
			out = append(out, row)
		}
	}
	return out
}

// computer-activity v1.0.0 rule 4: a watch row is a conversation, by name and
// length, with who spoke and its marks and note; an hour with no speech stays
// an hour row; no words of the transcript travel in a row.
func TestActivityFeedWatchRowIsAConversation(t *testing.T) {
	w := watchRowForgetFixture(t)
	sam, standup := w.conversations(t)
	if err := writeWatchConversationNames(w.store.dir, map[string]watchConversationNameEntry{
		sam.ID: {Hash: watchConversationHash(sam), Name: "Pricing with Sam", StartAt: sam.StartAt, RecordingIDs: sam.RecordingIDs}}); err != nil {
		t.Fatal(err)
	}
	if err := writeWatchSpeakerNames(w.store.dir, map[string]watchSpeakerNamesEntry{
		sam.ID: {Hash: watchConversationHash(sam), StartAt: sam.StartAt, RecordingIDs: sam.RecordingIDs, Names: map[string]string{"speaker_1": "Sam"}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeWatchConversationNotes(w.store.dir, map[string]watchConversationNotesEntry{
		sam.ID: {Hash: watchConversationHash(sam), StartAt: sam.StartAt, RecordingIDs: sam.RecordingIDs, Summary: "Settled the tiers."}}); err != nil {
		t.Fatal(err)
	}
	// One mark, ten minutes into the 9:40 part: 9:50, inside the Sam talk.
	w.store.mu.Lock()
	w.store.recordings[w.nine].Received[0].Marks = []float64{600}
	if err := w.store.saveLocked(); err != nil {
		t.Fatal(err)
	}
	w.store.mu.Unlock()

	rows := w.watchRows(t)
	byID := map[string]ActivityFeedRow{}
	for _, row := range rows {
		byID[row.ID] = row
		if row.Basis.Setting != basisWatch {
			t.Fatalf("a watch row stands on %+v", row.Basis)
		}
	}
	if len(rows) != 4 {
		t.Fatalf("watch rows = %+v, want Sam, the standup, 11 AM and noon", rows)
	}
	row := byID[sam.ID]
	if !row.Conversation || row.Title != "Pricing with Sam" || strings.Join(row.Speakers, ",") != "you,Sam" || row.OtherVoices != 0 ||
		row.Marks != 1 || !row.HasNote || row.StartMs != sam.StartAt || row.EndMs != sam.EndAt || row.Device != "watch" {
		t.Fatalf("Sam row = %+v", row)
	}
	if _, ok := byID[w.nine]; ok {
		t.Fatal("an hour a conversation draws on is also listed as an hour")
	}
	// Unnamed voices are counted, never listed as "Speaker 2".
	if row := byID[standup.ID]; !row.Conversation || row.Title != "" || len(row.Speakers) != 0 || row.OtherVoices != 1 || row.HasNote || row.Marks != 0 {
		t.Fatalf("standup row = %+v", row)
	}
	if hour := byID[w.eleven]; hour.Conversation || hour.RecordingID != w.eleven {
		t.Fatalf("the quiet hour = %+v", hour)
	}
	raw, _ := json.Marshal(rows)
	for _, words := range []string{"pricing page tonight", "sounds right", "standup notes", "see you", "Settled the tiers"} {
		if strings.Contains(string(raw), words) {
			t.Fatalf("a row carries the transcript or the note: %q", words)
		}
	}
	// The Watch tab reads the same rows.
	tab, err := w.app.GetActivityWatchRows(w.day.UnixMilli(), w.day.Add(24*time.Hour).UnixMilli())
	if err != nil || len(tab) != len(rows) || tab[0].ID != rows[0].ID {
		t.Fatalf("Watch tab rows %d %v, want the feed's %d", len(tab), err, len(rows))
	}
}

// computer-activity v1.1.0, 15e: Forget on a conversation names everything
// that goes, where it lives, and work an agent has (not counted); then all of
// it really goes, and the agent's hand-off stays.
func TestForgetAWatchConversationNamesAndRemovesEverything(t *testing.T) {
	w := watchRowForgetFixture(t)
	t.Setenv(featureSwitchesEnv, string(featureWatchNotes))
	old := meetingFollowupModel
	t.Cleanup(func() { meetingFollowupModel = old })
	meetingFollowupModel = func(_ *App, prompt string) (string, error) {
		if !strings.Contains(prompt, "pricing page") {
			return `{"summary":"","decisions":[],"followups":[]}`, nil
		}
		return `{"summary":"Bo and Sam settled pricing.","decisions":["Three tiers"],"followups":[{"text":"Send the pricing page tonight","owner":"me","person":"","due":"tonight"}]}`, nil
	}
	w.app.noteWatchConversations(context.Background(), w.store)
	sam, standup := w.conversations(t)
	if len(sam.ItemIDs) != 1 {
		t.Fatalf("the Sam talk's follow-ups %v, want one item", sam.ItemIDs)
	}
	// The follow-up was handed to Claude.
	w.app.keepWorkOnItHandoff(workonit.Handoff{ID: "h-sam", SourceKind: workonit.SourceWorkItem, SourceID: sam.ItemIDs[0],
		AgentID: "claude", AgentName: "Claude", Text: "Send the pricing page", SentAt: time.Now().UnixMilli()})
	// Isoform read a claim from the 10 AM words.
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	claim := "# Sam owns pricing\n\n- **Source**: activity\n- **Activity Session**: rec-" + w.ten + "\n\n- Sam owns pricing.\n"
	if err := os.WriteFile(filepath.Join(dir, "sam-owns-pricing.md"), []byte(claim), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := "# Likes short reviews\n\n- **Source**: activity\n- **Activity Session**: rec-" + w.noon + "\n\n- Short reviews.\n"
	if err := os.WriteFile(filepath.Join(dir, "short-reviews.md"), []byte(unrelated), 0o600); err != nil {
		t.Fatal(err)
	}

	preview, err := w.app.GetActivityForgetPreview("watch", sam.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, item := range preview.Goes {
		got[item.Kind] = append(got[item.Kind], item.Title)
	}
	for _, kind := range []string{"words", "audio", "notes", "copies"} {
		if len(got[kind]) != 1 {
			t.Fatalf("preview %v lacks its %s", got, kind)
		}
	}
	if strings.Join(got["followup"], "|") != "Send the pricing page tonight" {
		t.Fatalf("follow-ups %v", got["followup"])
	}
	if strings.Join(got["claim"], "|") != "Sam owns pricing" {
		t.Fatalf("claims %v, want the one read from these hours", got["claim"])
	}
	if strings.Join(got["agent_work"], "|") != "Claude" {
		t.Fatalf("agent work %v", got["agent_work"])
	}
	if len(got["conversation"]) != 1 {
		t.Fatalf("the standup in the same hour is not named: %v", got)
	}
	for _, item := range preview.Goes {
		if item.Kind == "conversation" && item.StartMs != standup.StartAt {
			t.Fatalf("sibling %+v, want the standup", item)
		}
	}

	if err := w.app.ForgetActivityFeedRow("watch", sam.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{w.nine, w.ten} {
		if rec, ok := w.store.get(id, ""); !ok || rec.State != mobileRecordingDeleted {
			t.Fatalf("hour %s outlived Forget", id)
		}
	}
	if rec, ok := w.store.get(w.noon, ""); !ok || rec.State == mobileRecordingDeleted {
		t.Fatal("Forget took an hour it did not name")
	}
	purpose, err := w.app.purposeProductStore()
	if err != nil {
		t.Fatal(err)
	}
	if loops, err := purpose.ListDetailsAll(); err != nil || len(loops) != 0 {
		t.Fatalf("follow-ups after Forget: %d %v", len(loops), err)
	}
	entries, err := w.app.GetMemoryEntries()
	if err != nil {
		t.Fatal(err)
	}
	titles := []string{}
	for _, e := range entries {
		titles = append(titles, e.Title)
	}
	if strings.Contains(strings.Join(titles, "|"), "Sam owns pricing") || !strings.Contains(strings.Join(titles, "|"), "Likes short reviews") {
		t.Fatalf("memories after Forget: %v", titles)
	}
	if raw, err := os.ReadFile(filepath.Join(w.store.dir, watchConversationNotesFile)); err == nil && strings.Contains(string(raw), "pricing") {
		t.Fatal("the notes outlived Forget")
	}
	handoffs, err := w.app.GetWorkOnItHandoffs()
	if err != nil || len(handoffs) != 1 {
		t.Fatalf("the hand-off to Claude should stay with Claude: %d %v", len(handoffs), err)
	}
	for _, row := range w.watchRows(t) {
		if row.ID == sam.ID || row.ID == standup.ID {
			t.Fatalf("a forgotten conversation is still a row: %+v", row)
		}
	}
}

// The conversation id names its first words. When they changed after the
// preview (an hour went), Forget refuses rather than guess, and takes nothing.
func TestForgetRefusesAConversationWhoseWordsChanged(t *testing.T) {
	w := watchRowForgetFixture(t)
	sam, _ := w.conversations(t)
	if _, err := w.app.GetActivityForgetPreview("watch", sam.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.app.DeleteWatchRecording(w.nine); err != nil {
		t.Fatal(err)
	}
	if err := w.app.ForgetActivityFeedRow("watch", sam.ID); !errors.Is(err, errWatchConversationGone) {
		t.Fatalf("Forget of a changed conversation = %v", err)
	}
	if rec, ok := w.store.get(w.ten, ""); !ok || rec.State == mobileRecordingDeleted {
		t.Fatal("a refused Forget took the 10 AM hour")
	}
	if _, err := w.app.GetActivityForgetPreview("watch", "not-a-conversation"); !errors.Is(err, errWatchConversationGone) {
		t.Fatalf("an unreadable id = %v", err)
	}
}

// An hour with no conversation forgets as itself: its audio and copies.
func TestForgetAQuietHour(t *testing.T) {
	w := watchRowForgetFixture(t)
	preview, err := w.app.GetActivityForgetPreview("watch", w.eleven)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, item := range preview.Goes {
		kinds = append(kinds, item.Kind)
	}
	if strings.Join(kinds, ",") != "audio,copies" {
		t.Fatalf("quiet hour preview %v", kinds)
	}
	if err := w.app.ForgetActivityFeedRow("watch", w.eleven); err != nil {
		t.Fatal(err)
	}
	if rec, ok := w.store.get(w.eleven, ""); !ok || rec.State != mobileRecordingDeleted {
		t.Fatal("the quiet hour outlived Forget")
	}
	if rec, ok := w.store.get(w.ten, ""); !ok || rec.State == mobileRecordingDeleted {
		t.Fatal("Forget of a quiet hour took another")
	}
}

// A claim that fails to go stops Forget before the hours go, so the
// conversation is still there to Forget again; the retry finishes, and a
// claim already forgotten counts as done.
func TestForgetAWatchConversationThatFailsPartWayCanBeForgottenAgain(t *testing.T) {
	w := watchRowForgetFixture(t)
	sam, _ := w.conversations(t)
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sam-owns-pricing", "sam-reviews-tiers"} {
		claim := "# " + name + "\n\n- **Source**: activity\n- **Activity Session**: rec-" + w.ten + "\n\n- " + name + ".\n"
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(claim), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	original := forgetWatchClaim
	t.Cleanup(func() { forgetWatchClaim = original })
	calls := 0
	forgetWatchClaim = func(a *App, id string) error {
		calls++
		if calls == 2 {
			return errors.New("memory index is locked")
		}
		return original(a, id)
	}
	if err := w.app.ForgetActivityFeedRow("watch", sam.ID); err == nil {
		t.Fatal("Forget reported success with a claim left")
	}
	for _, id := range []string{w.nine, w.ten} {
		if rec, ok := w.store.get(id, ""); !ok || rec.State == mobileRecordingDeleted || rec.Words == 0 {
			t.Fatalf("hour %s went before its claims did", id)
		}
	}
	if _, err := w.app.GetActivityForgetPreview("watch", sam.ID); err != nil {
		t.Fatalf("the conversation can't be reached to Forget again: %v", err)
	}
	forgetWatchClaim = original
	if err := w.app.ForgetActivityFeedRow("watch", sam.ID); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	for _, id := range []string{w.nine, w.ten} {
		if rec, ok := w.store.get(id, ""); !ok || rec.State != mobileRecordingDeleted {
			t.Fatalf("hour %s outlived the retry", id)
		}
	}
	entries, err := w.app.GetMemoryEntries()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Title, "sam-") {
			t.Fatalf("claim %q outlived the retry", e.Title)
		}
	}
	// A claim already gone is done.
	if err := original(w.app, "sam-owns-pricing"); err != nil {
		t.Fatalf("forgetting a claim already gone: %v", err)
	}
}
