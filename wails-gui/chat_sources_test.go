package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	if got := rig.app.injectChatSourcesContext(main.LocalID, promptOriginMac); !strings.Contains(got, "call CiteSources once") {
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
