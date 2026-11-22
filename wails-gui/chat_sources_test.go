package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/storage"
)

// An answer says where it came from, and only from sources Isoform may read
// (phone-chat v1.0.0 rules 4 and 5).
func TestCiteSourcesShowsOnlyReadableSources(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rig := newUpcomingToolsRig(t)
	ctx := context.Background()
	main, err := rig.app.ensureMainChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	note, err := rig.app.SaveMemoryEntry("", "# Pricing with Sam\n- **Source**: user_edit\n\nSam and I agreed on three tiers.\n")
	if err != nil {
		t.Fatal(err)
	}
	// New notes are named by the millisecond: keep the two apart.
	time.Sleep(3 * time.Millisecond)
	gone, err := rig.app.SaveMemoryEntry("", "# Old plan\n- **Source**: user_edit\n\nThe old plan.\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.app.DeleteMemoryEntry(gone.ID); err != nil {
		t.Fatal(err)
	}
	sam, err := rig.app.store.CreatePrincipal(ctx, storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Sam Lee", CreatedFrom: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	tools := rig.app.mainChatSourcesTools(main.LocalID, promptOriginMac)
	if len(tools) != 1 || tools[0].Name() != chatSourcesToolName {
		t.Fatalf("tools = %v", tools)
	}
	input := map[string]any{"sources": []any{
		map[string]any{"ref": "memory:" + note.ID, "claim": "three tiers"},
		map[string]any{"ref": "person:" + sam.ID, "claim": "Sam"},
		map[string]any{"ref": "memory:" + gone.ID, "claim": "the old plan"},
		map[string]any{"ref": "memory:made-up", "claim": "invented"},
		map[string]any{"ref": "memory:" + note.ID, "claim": "agreed"},
	}}
	result, err := tools[0].Execute(t.Context(), draftCall("toolu_s1", input))
	if err != nil || result.IsError || !strings.Contains(result.Content, "left out") {
		t.Fatalf("cite = %v %+v", err, result)
	}
	row, err := rig.app.store.LoadStreamItem(ctx, "sources:"+main.LocalID+":toolu_s1")
	if err != nil || row == nil || row.ToolName != chatSourcesLineTool {
		t.Fatalf("row = %+v %v", row, err)
	}
	var payload chatSourcesPayload
	if err := json.Unmarshal([]byte(row.ToolInput), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Sources) != 2 || payload.Sources[0].Title != "Pricing with Sam" || payload.Sources[0].Target.Kind != "memory" || payload.Sources[1].Title != "Sam Lee" {
		t.Fatalf("sources = %+v", payload.Sources)
	}
	if len(payload.Claims) != 3 || payload.Claims[2].Text != "agreed" || payload.Claims[2].Source != 0 {
		t.Fatalf("claims = %+v", payload.Claims)
	}
	// With Closed Eyes on nothing is shown.
	if err := rig.app.SetClosedEyesModeEnabled(true); err != nil {
		t.Fatal(err)
	}
	if got, _ := rig.app.chatSourcesFromInput(ctx, input["sources"].([]any)); len(got.Sources) != 0 {
		t.Fatalf("Closed Eyes showed %+v", got.Sources)
	}
}

func TestContextPrintsTheRefsCiteSourcesAccepts(t *testing.T) {
	if got := memoryIDFromPath("memory-123.md"); got != "memory-123" {
		t.Fatalf("id = %q", got)
	}
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := rig.app.injectChatSourcesContext(main.LocalID, promptOriginMac); !strings.Contains(got, "call CiteSources once") || !strings.Contains(got, "activity:") || !strings.Contains(got, "say what you did see instead and cite that") {
		t.Fatalf("context = %q", got)
	}
	if rig.app.mainChatSourcesTools(main.LocalID, promptOriginPhone) == nil {
		t.Fatal("a phone turn did not get CiteSources")
	}
	for _, origin := range []promptOrigin{promptOriginPhoneAsk, promptOriginPanelAsk, promptOriginResume} {
		if rig.app.mainChatSourcesTools(main.LocalID, origin) != nil || rig.app.injectChatSourcesContext(main.LocalID, origin) != "" {
			t.Fatalf("origin %q got CiteSources", origin)
		}
	}
}

// An answer about the person's day cites the Activity session, call or watch
// conversation it read, each with when it happened and a link that opens it
// (computer-chat v1.0.0 rule 3, phone-chat v1.0.0 rule 4). Each is read again
// through the gates the model's read went through.
func TestCiteSourcesActivityCallAndTheirGates(t *testing.T) {
	app := newMCPReadTestApp(t)
	ctx := context.Background()
	screenAt := seedMCPReadActivity(t, app, "s-figma", "Figma", "com.figma.Desktop", "Pricing page", "", "pending")
	seedMCPReadActivity(t, app, "s-protected", "Figma", "com.figma.Desktop", "Pricing page", "", "protected")
	seedMCPReadActivity(t, app, "s-mail", "Mail", "com.apple.mail", "Inbox", "", "pending")
	callAt := time.Now().Add(-2 * time.Hour).UnixMilli()
	if err := app.activity.store.SaveSession(ActivitySessionRow{ID: "s-call", StartTime: callAt, EndTime: callAt + 60000, CreatedAt: callAt,
		AppNames: `["zoom.us"]`, TriageStatus: "pending", IsMeeting: true, MeetingApp: "Zoom"},
		[]activitymon.ActivityEvent{{Timestamp: callAt, AppName: "zoom.us", AppBundleID: "us.zoom.xos", WindowTitle: "Zoom Meeting"}}, nil); err != nil {
		t.Fatal(err)
	}
	// RecentActivity prints the ref of each session it shows, and only those.
	recent := app.readChatRecentActivity(ctx)
	if !strings.Contains(recent, "ref=activity:s-figma") || strings.Contains(recent, "s-protected") || strings.Contains(recent, "s-mail") {
		t.Fatalf("RecentActivity refs: %q", recent)
	}
	refs := []any{
		map[string]any{"ref": "activity:s-figma", "claim": "the pricing page"},
		map[string]any{"ref": "activity:s-call", "claim": "on the call"},
		map[string]any{"ref": "activity:s-protected"},
		map[string]any{"ref": "activity:s-mail"},
		map[string]any{"ref": "activity:made-up"},
	}
	payload, dropped := app.chatSourcesFromInput(ctx, refs)
	if dropped != 3 || len(payload.Sources) != 2 {
		t.Fatalf("sources %+v, %d dropped", payload.Sources, dropped)
	}
	screen, call := payload.Sources[0], payload.Sources[1]
	if screen.Kind != chatSourceActivity || screen.Title != "Figma" || screen.At != screenAt || screen.Target != (mainChatTarget{Kind: mainChatTargetActivity, ID: "s-figma"}) {
		t.Fatalf("screen = %+v", screen)
	}
	if call.Kind != chatSourceCall || call.Title != "Zoom" || call.At != callAt || call.Target != (mainChatTarget{Kind: mainChatTargetActivity, ID: "s-call"}) {
		t.Fatalf("call = %+v", call)
	}
	// A sensitive app is never named, nor cited.
	pii.SetUserSensitiveApps(map[string]string{"com.figma.Desktop": "Figma"})
	t.Cleanup(func() { pii.SetUserSensitiveApps(nil) })
	if got, _ := app.chatSourcesFromInput(ctx, refs[:1]); len(got.Sources) != 0 {
		t.Fatalf("a sensitive app was cited: %+v", got.Sources)
	}
	pii.SetUserSensitiveApps(nil)
	// Closed Eyes: nothing at all.
	if err := app.SetClosedEyesModeEnabled(true); err != nil {
		t.Fatal(err)
	}
	if got, _ := app.chatSourcesFromInput(ctx, refs); len(got.Sources) != 0 {
		t.Fatalf("Closed Eyes showed %+v", got.Sources)
	}
}

// A watch recording is cited as the conversation the computer named, opening
// the watch's conversations; a private transcript is not cited.
func TestCiteSourcesNamesTheWatchConversation(t *testing.T) {
	app, _, store, nine, _ := samTalkFixture(t)
	state, err := storage.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	app.store = state
	old := watchConversationNamer
	t.Cleanup(func() { watchConversationNamer = old })
	watchConversationNamer = func(*App, string) (string, error) { return "Pricing with Sam\n", nil }
	app.nameWatchConversations(context.Background(), store)
	convs, err := app.watchConversationsOf(store)
	if err != nil {
		t.Fatal(err)
	}
	var sam WatchConversation
	for _, conv := range convs {
		if conv.Name == "Pricing with Sam" {
			sam = conv
		}
	}
	if sam.ID == "" {
		t.Fatalf("conversations %+v", convs)
	}
	refs := []any{map[string]any{"ref": "activity:rec-" + nine}}
	payload, dropped := app.chatSourcesFromInput(context.Background(), refs)
	if dropped != 0 || len(payload.Sources) != 1 {
		t.Fatalf("sources %+v, %d dropped", payload.Sources, dropped)
	}
	got := payload.Sources[0]
	if got.Kind != chatSourceWatch || got.Title != "Pricing with Sam" || got.At != sam.StartAt || got.Target != (mainChatTarget{Kind: mainChatTargetWatch, ID: sam.ID}) {
		t.Fatalf("watch source = %+v", got)
	}
	if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status = 'protected' WHERE id = ?`, "rec-"+nine); err != nil {
		t.Fatal(err)
	}
	if got, _ := app.chatSourcesFromInput(context.Background(), refs); len(got.Sources) != 0 {
		t.Fatalf("a private transcript was cited: %+v", got.Sources)
	}
}

// The row keeps the shape an older phone reads: a source without a time has
// no "at", and a row written before times still reads.
func TestChatSourcesPayloadKeepsTheOldShape(t *testing.T) {
	blob, err := json.Marshal(chatSourcesPayload{Sources: []chatSource{{Kind: "memory", ID: "m1", Title: "Pricing with Sam", Target: mainChatTarget{Kind: "memory", ID: "m1"}}}})
	if err != nil || strings.Contains(string(blob), `"at"`) {
		t.Fatalf("blob %s %v", blob, err)
	}
	var old chatSourcesPayload
	if err := json.Unmarshal([]byte(`{"sources":[{"kind":"person","id":"p1","title":"Sam Lee","target":{"kind":"person","id":"p1"}}]}`), &old); err != nil || old.Sources[0].At != 0 || old.Sources[0].Title != "Sam Lee" {
		t.Fatalf("old row %+v %v", old, err)
	}
}
