package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/platform/activitymon"
)

// Bo talks with Sam from 9:50 to 10:20, is quiet, then has a standup from
// 10:40 to 10:55. The watch kept two hours: 9 AM and 10 AM.
func samAndStandup(nine, ten string, day time.Time) []watchConversationSpeech {
	at := func(hour, minute, second int) int64 {
		return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, second, 0, day.Location()).UnixMilli()
	}
	say := func(rec string, ms int64, speaker, text string) watchConversationSpeech {
		return watchConversationSpeech{At: ms, RecordingID: rec, DeviceID: "dev-a", Speaker: speaker, Text: text}
	}
	return []watchConversationSpeech{
		say(nine, at(9, 50, 0), "you", "Sam, do you have a minute for the pricing page"),
		say(nine, at(9, 51, 10), "speaker_1", "Sure, what changed"),
		say(nine, at(9, 52, 30), "you", "Three tiers instead of two"),
		say(nine, at(9, 59, 40), "speaker_1", "I like the middle one, keep going"),
		// The hour's edge: the next words are in the next recording.
		say(ten, at(10, 0, 20), "you", "So the middle tier gets the phone app"),
		say(ten, at(10, 1, 50), "speaker_1", "And the top tier the team seats"),
		say(ten, at(10, 20, 0), "you", "Great, I will send it tonight"),
		// Twenty minutes of quiet, then the standup.
		say(ten, at(10, 40, 0), "speaker_2", "Standup, who goes first"),
		say(ten, at(10, 41, 0), "you", "Pricing page is ready for review"),
		say(ten, at(10, 55, 0), "speaker_2", "Thanks all"),
	}
}

func TestATalkAcrossTheHourIsOneConversationAndTheStandupAnother(t *testing.T) {
	day := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	// The middle of the Sam talk has gaps under two minutes only when the
	// lines above are that close; fill the long stretches the way real speech does.
	speech := samAndStandup("nine", "ten", day)
	fill := func(rec string, from, to time.Time) {
		for at := from; at.Before(to); at = at.Add(90 * time.Second) {
			speech = append(speech, watchConversationSpeech{At: at.UnixMilli(), RecordingID: rec, DeviceID: "dev-a", Speaker: "speaker_1", Text: "and another thing"})
		}
	}
	fill("nine", day.Add(9*time.Hour+53*time.Minute), day.Add(9*time.Hour+59*time.Minute+30*time.Second))
	fill("ten", day.Add(10*time.Hour+3*time.Minute), day.Add(10*time.Hour+20*time.Minute))
	fill("ten", day.Add(10*time.Hour+42*time.Minute), day.Add(10*time.Hour+55*time.Minute))

	convs := buildWatchConversations(speech, watchConversationSilence)
	if len(convs) != 2 {
		t.Fatalf("got %d conversations, want the Sam talk and the standup: %+v", len(convs), convs)
	}
	standup, sam := convs[0], convs[1] // newest first
	if got := time.UnixMilli(sam.StartAt).UTC().Format("15:04"); got != "09:50" {
		t.Fatalf("the Sam talk begins %s", got)
	}
	if got := time.UnixMilli(sam.EndAt).UTC().Format("15:04"); got != "10:20" {
		t.Fatalf("the Sam talk ends %s", got)
	}
	if strings.Join(sam.RecordingIDs, ",") != "nine,ten" {
		t.Fatalf("the Sam talk draws from %v, want both hours", sam.RecordingIDs)
	}
	if strings.Join(standup.RecordingIDs, ",") != "ten" || time.UnixMilli(standup.StartAt).UTC().Format("15:04") != "10:40" {
		t.Fatalf("standup %+v", standup)
	}
	if sam.Speakers[0] != "you" || len(sam.Speakers) != 2 {
		t.Fatalf("speakers %v", sam.Speakers)
	}
	for _, line := range sam.Lines {
		if strings.Contains(line.Text, "Standup") {
			t.Fatal("the standup was glued to the Sam talk")
		}
	}
	if sam.Lines[0].Text != "Sam, do you have a minute for the pricing page" || sam.Words == 0 {
		t.Fatalf("first line %+v", sam.Lines[0])
	}
}

func TestSilenceLongerThanTheGapEndsAConversation(t *testing.T) {
	base := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	say := func(after time.Duration, text string) watchConversationSpeech {
		return watchConversationSpeech{At: base.Add(after).UnixMilli(), RecordingID: "r", DeviceID: "dev-a", Text: text}
	}
	// "one two three four five" lasts two seconds, so the second line begins
	// 1:59 after the first ended and the third 2:01 after the second ended.
	speech := []watchConversationSpeech{
		say(0, "one two three four five"),
		say(2*time.Second+119*time.Second, "one two three four five"),
		say(2*time.Second+119*time.Second+2*time.Second+121*time.Second, "later"),
		say(-time.Hour, "   "),
	}
	convs := buildWatchConversations(speech, 2*time.Minute)
	if len(convs) != 2 || len(convs[1].Lines) != 2 || len(convs[0].Lines) != 1 {
		t.Fatalf("conversations %+v", convs)
	}
	if len(buildWatchConversations(nil, 2*time.Minute)) != 0 {
		t.Fatal("no speech made a conversation")
	}
}

func TestTwoDevicesNeverShareAConversation(t *testing.T) {
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC).UnixMilli()
	convs := buildWatchConversations([]watchConversationSpeech{
		{At: at, RecordingID: "a", DeviceID: "dev-a", Text: "hello"},
		{At: at + 1000, RecordingID: "b", DeviceID: "dev-b", Text: "hello"},
	}, 2*time.Minute)
	if len(convs) != 2 {
		t.Fatalf("conversations %+v", convs)
	}
}

// The real path: two stored hours, read through the activity store. Deleting
// the 9 AM hour leaves the Sam talk from 10:00 on, and marks nothing else.
func TestConversationsFollowTheStoredHoursAndADeletedHour(t *testing.T) {
	app, _, store, first := watchDeskFixture(t)
	// The fixture's own recording is an hour ago; delete it so only the two hours below count.
	if _, err := app.deleteWatchRecording(store, first, ""); err != nil {
		t.Fatal(err)
	}
	uploads := &mapUploads{bodies: map[string][]byte{}}
	day := time.Now().UTC().Add(-24 * time.Hour).Truncate(24 * time.Hour)
	nine, ten := uuid.NewString(), uuid.NewString()
	starts := map[string]time.Time{nine: day.Add(9*time.Hour + 40*time.Minute), ten: day.Add(10 * time.Hour)}
	for _, id := range []string{nine, ten} {
		req := sealedRecordingPart(t, uploads, mobileTestDeviceID, id, 0, true, []byte("audio"), starts[id])
		if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, mobileTestDeviceID, req); err != nil {
			t.Fatal(err)
		}
	}
	speech := samAndStandup(nine, ten, day)
	byRecording := map[string][]activitymon.ActivityEvent{}
	for _, s := range speech {
		byRecording[s.RecordingID] = append(byRecording[s.RecordingID], activitymon.ActivityEvent{Timestamp: s.At, Kind: activitymon.EventSpeech,
			AppName: mobileRecordingAppName, SpeechText: s.Text, SpeechSpeakerLabel: s.Speaker})
	}
	// Keep the Sam talk unbroken: a line every 90 seconds between the scripted ones.
	for at := day.Add(9*time.Hour + 53*time.Minute); at.Before(day.Add(9*time.Hour + 59*time.Minute + 30*time.Second)); at = at.Add(90 * time.Second) {
		byRecording[nine] = append(byRecording[nine], activitymon.ActivityEvent{Timestamp: at.UnixMilli(), Kind: activitymon.EventSpeech, AppName: mobileRecordingAppName, SpeechText: "go on", SpeechSpeakerLabel: "speaker_1"})
	}
	for at := day.Add(10*time.Hour + 3*time.Minute); at.Before(day.Add(10*time.Hour + 20*time.Minute)); at = at.Add(90 * time.Second) {
		byRecording[ten] = append(byRecording[ten], activitymon.ActivityEvent{Timestamp: at.UnixMilli(), Kind: activitymon.EventSpeech, AppName: mobileRecordingAppName, SpeechText: "go on", SpeechSpeakerLabel: "speaker_1"})
	}
	for at := day.Add(10*time.Hour + 42*time.Minute); at.Before(day.Add(10*time.Hour + 55*time.Minute)); at = at.Add(90 * time.Second) {
		byRecording[ten] = append(byRecording[ten], activitymon.ActivityEvent{Timestamp: at.UnixMilli(), Kind: activitymon.EventSpeech, AppName: mobileRecordingAppName, SpeechText: "next", SpeechSpeakerLabel: "speaker_2"})
	}
	for _, id := range []string{nine, ten} {
		events := byRecording[id]
		session := ActivitySessionRow{ID: "rec-" + id, StartTime: starts[id].UnixMilli(), EndTime: starts[id].Add(time.Hour).UnixMilli(),
			TriggerKind: mobileRecordingTrigger, AppNames: `["` + mobileRecordingAppName + `"]`, EventCount: len(events),
			TriageStatus: "pending", CreatedAt: time.Now().UnixMilli()}
		if err := app.activity.store.SaveSession(session, events, nil); err != nil {
			t.Fatal(err)
		}
		store.setState(id, mobileRecordingTranscribed, session.ID, 10, "")
	}

	convs, err := app.GetWatchConversations()
	if err != nil {
		t.Fatal(err)
	}
	if len(convs) != 2 || strings.Join(convs[1].RecordingIDs, ",") != nine+","+ten {
		t.Fatalf("conversations %+v", convs)
	}

	if _, err := app.deleteWatchRecording(store, nine, ""); err != nil {
		t.Fatal(err)
	}
	convs, err = app.GetWatchConversations()
	if err != nil {
		t.Fatal(err)
	}
	if len(convs) != 2 {
		t.Fatalf("after deleting 9 AM: %+v", convs)
	}
	sam := convs[1]
	if strings.Join(sam.RecordingIDs, ",") != ten || time.UnixMilli(sam.StartAt).UTC().Format("15:04") != "10:00" {
		t.Fatalf("after deleting 9 AM the Sam talk is %+v", sam)
	}
	for _, line := range sam.Lines {
		if strings.Contains(line.Text, "do you have a minute") {
			t.Fatal("words of the deleted hour are still shown")
		}
	}
}

func TestANameIsAcceptedOnlyWhenItIsShortAndPlain(t *testing.T) {
	for raw, want := range map[string]string{
		"Pricing with Sam":           "Pricing with Sam",
		"  \"Pricing with Sam.\"\n":  "Pricing with Sam",
		"**Standup**":                "Standup",
		"Standup\nIt was about work": "Standup",
		"定价讨论":                       "定价讨论",
	} {
		if got, ok := cleanWatchConversationName(raw); !ok || got != want {
			t.Errorf("%q: got %q %t, want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "-", "...", "12:30", strings.Repeat("word ", 9), strings.Repeat("x", 60)} {
		if got, ok := cleanWatchConversationName(raw); ok {
			t.Errorf("%q was accepted as %q", raw, got)
		}
	}
}

func TestTheNamePromptMarksTheOwnersLinesAndIsBounded(t *testing.T) {
	c := WatchConversation{Lines: []WatchRecordingLine{
		{Speaker: "you", Text: "Sam, do you have a minute"},
		{Speaker: "speaker_1", Text: "Sure"},
	}}
	prompt := buildWatchConversationNamePrompt(c)
	if !strings.Contains(prompt, "Me: Sam, do you have a minute\nSure\n") {
		t.Fatalf("prompt %q", prompt)
	}
	long := WatchConversation{}
	for i := 0; i < 2000; i++ {
		long.Lines = append(long.Lines, WatchRecordingLine{Text: "and another thing was said here"})
	}
	if n := len([]rune(buildWatchConversationNamePrompt(long))); n > watchConversationNameTranscriptRunes+1500 {
		t.Fatalf("prompt is %d runes", n)
	}
}

// The stored path: the Sam talk (30 minutes, yesterday) gets a name from one
// call; a few words in passing get none and no call; nothing is asked twice;
// nothing is asked while observing is paused; and deleting the 9 AM hour
// drops the name at once, because its words changed.
func TestConversationsAreNamedOnceAndANameGoesWithItsWords(t *testing.T) {
	app, bridge, store, first := watchDeskFixture(t)
	if _, err := app.deleteWatchRecording(store, first, ""); err != nil {
		t.Fatal(err)
	}
	uploads := &mapUploads{bodies: map[string][]byte{}}
	day := time.Now().UTC().Add(-24 * time.Hour).Truncate(24 * time.Hour)
	nine, ten, noon := uuid.NewString(), uuid.NewString(), uuid.NewString()
	starts := map[string]time.Time{nine: day.Add(9*time.Hour + 40*time.Minute), ten: day.Add(10 * time.Hour), noon: day.Add(12 * time.Hour)}
	events := map[string][]activitymon.ActivityEvent{}
	say := func(id string, at time.Time, speaker, text string) {
		events[id] = append(events[id], activitymon.ActivityEvent{Timestamp: at.UnixMilli(), Kind: activitymon.EventSpeech,
			AppName: mobileRecordingAppName, SpeechText: text, SpeechSpeakerLabel: speaker})
	}
	// The Sam talk: 9:50 to 10:20, a line every 90 seconds, across the hour.
	for at := day.Add(9*time.Hour + 50*time.Minute); !at.After(day.Add(10*time.Hour + 20*time.Minute)); at = at.Add(90 * time.Second) {
		id := nine
		if !at.Before(starts[ten]) {
			id = ten
		}
		say(id, at, "you", "about the pricing page")
	}
	// A few words in passing at noon.
	say(noon, day.Add(12*time.Hour+5*time.Minute), "speaker_1", "thanks, see you")
	for _, id := range []string{nine, ten, noon} {
		req := sealedRecordingPart(t, uploads, mobileTestDeviceID, id, 0, true, []byte("audio"), starts[id])
		if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, mobileTestDeviceID, req); err != nil {
			t.Fatal(err)
		}
		session := ActivitySessionRow{ID: "rec-" + id, StartTime: starts[id].UnixMilli(), EndTime: starts[id].Add(time.Hour).UnixMilli(),
			TriggerKind: mobileRecordingTrigger, AppNames: `["` + mobileRecordingAppName + `"]`, EventCount: len(events[id]),
			TriageStatus: "pending", CreatedAt: time.Now().UnixMilli()}
		if err := app.activity.store.SaveSession(session, events[id], nil); err != nil {
			t.Fatal(err)
		}
		store.setState(id, mobileRecordingTranscribed, session.ID, 10, "")
	}
	old := watchConversationNamer
	t.Cleanup(func() { watchConversationNamer = old })
	var prompts []string
	watchConversationNamer = func(_ *App, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		return "Pricing with Sam\n", nil
	}

	app.nameWatchConversations(context.Background(), store)
	if len(prompts) != 1 || !strings.Contains(prompts[0], "Me: about the pricing page") || strings.Contains(prompts[0], "see you") {
		t.Fatalf("%d calls; only the long conversation is named, from its own words", len(prompts))
	}
	convs, err := app.watchConversationsOf(store)
	if err != nil || len(convs) != 2 {
		t.Fatalf("conversations %+v %v", convs, err)
	}
	if convs[1].Name != "Pricing with Sam" || convs[0].Name != "" {
		t.Fatalf("names %q and %q", convs[1].Name, convs[0].Name)
	}
	// Nothing is asked twice.
	app.nameWatchConversations(context.Background(), store)
	if len(prompts) != 1 {
		t.Fatalf("%d calls after a second pass", len(prompts))
	}
	raw, err := os.ReadFile(filepath.Join(store.dir, watchConversationNamesFile))
	if err != nil || !strings.Contains(string(raw), "Pricing with Sam") {
		t.Fatalf("names file %q %v", raw, err)
	}
	if info, _ := os.Stat(filepath.Join(store.dir, watchConversationNamesFile)); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}

	// The phone's view of each hour carries the names of what it heard: the
	// talk ran across 9 AM and 10 AM, so both carry it; noon carries none.
	list := watchDeskCall(bridge, http.MethodGet, "/recordings?ids="+nine+","+ten+","+noon, "", mobileScopeChatWrite).Body.String()
	if strings.Count(list, `"conversations":["Pricing with Sam"]`) != 2 {
		t.Fatalf("the phone's views: %s", list)
	}

	// Delete the 9 AM hour: the name was made from words that are gone now.
	if _, err := app.deleteWatchRecording(store, nine, ""); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(store.dir, watchConversationNamesFile)); err == nil && strings.Contains(string(raw), "Pricing with Sam") {
		t.Fatalf("the name outlived the deleted hour: %s", raw)
	}
	convs, _ = app.watchConversationsOf(store)
	for _, c := range convs {
		if c.Name != "" {
			t.Fatalf("a conversation still shows the old name: %+v", c.Name)
		}
	}
	if list := watchDeskCall(bridge, http.MethodGet, "/recordings?ids="+ten, "", mobileScopeChatWrite).Body.String(); strings.Contains(list, "conversations") {
		t.Fatalf("the phone still gets the old name: %s", list)
	}
}

// A transcript that is withdrawn (deleted in Activity, or removed at the end
// of its keep time) takes the names made from it, without waiting for a read.
func TestAWithdrawnTranscriptTakesItsNames(t *testing.T) {
	dir := t.TempDir()
	store := newMobileRecordingStore(dir)
	gone, stays := uuid.NewString(), uuid.NewString()
	// The recordings folder exists once anything was received.
	if err := os.MkdirAll(store.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeWatchConversationNames(store.dir, map[string]watchConversationNameEntry{
		"dev:1": {Hash: "a", Name: "Pricing with Sam", StartAt: 1, RecordingIDs: []string{gone, stays}},
		"dev:2": {Hash: "b", Name: "Standup", StartAt: 2, RecordingIDs: []string{stays}},
		"dev:3": {Hash: "c", Name: "", StartAt: 3, RecordingIDs: []string{stays}},
	}); err != nil {
		t.Fatal(err)
	}
	byRecording := watchConversationNamesByRecording(store.dir)
	if strings.Join(byRecording[stays], ",") != "Pricing with Sam,Standup" || strings.Join(byRecording[gone], ",") != "Pricing with Sam" {
		t.Fatalf("names by recording %+v", byRecording)
	}
	store.onPrivacyWithdrawal(privacyWithdrawal{Scope: privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: "rec-" + gone}})
	byRecording = watchConversationNamesByRecording(store.dir)
	if len(byRecording[gone]) != 0 || strings.Join(byRecording[stays], ",") != "Standup" {
		t.Fatalf("after the withdrawal %+v", byRecording)
	}
}
