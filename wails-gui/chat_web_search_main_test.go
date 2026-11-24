package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/destinations"
	"wails-gui/storage"
	"yansu-agent/agent"
)

const meetingQuestion = "Who is Acme Robotics, before tomorrow's 10:00?"

func TestMainChatWebSearchAdmission(t *testing.T) {
	for _, origin := range []promptOrigin{promptOriginMac, promptOriginPhone, promptOriginPanelAsk, promptOriginPhoneAsk} {
		t.Run("origin-"+string(origin), func(t *testing.T) {
			a, tag := topicChatTestApp(t)
			r := &panelAskCaptureRunner{topicCaptureRunner: &topicCaptureRunner{a: a, tag: tag, prompt: make(chan string, 1)}, inputs: make(chan *agent.TaskInput, 1)}
			a.runner = r
			private := "Use the private budget for Acme Robotics"
			files := []FileData{{FileName: "note.txt", MediaType: "text/plain", FileSize: int64(len(private)), DataBase64: base64.StdEncoding.EncodeToString([]byte(private))}}
			if _, err := a.sendMainChatAs("b9-user", meetingQuestion, nil, files, origin, nil); err != nil {
				t.Fatal(err)
			}
			var input *agent.TaskInput
			select {
			case input = <-r.inputs:
			case <-time.After(10 * time.Second):
				t.Fatal("turn did not reach the runner")
			}
			<-r.prompt
			waitForTagIdle(t, a, tag)
			var search *chatWebSearchTool
			for _, tool := range input.ExtraTools {
				if tool.Name() == chatWebSearchToolName {
					search = tool.(*chatWebSearchTool)
				}
			}
			if (search != nil) != (origin == promptOriginMac) {
				t.Fatal("wrong search availability")
			}
			if search != nil {
				stale, err := search.Execute(t.Context(), searchCall("Acme Robotics"))
				if err != nil || !stale.IsError || !strings.Contains(stale.Content, "turn has ended") {
					t.Fatal("an expired tool kept search authority")
				}
				if search.turn.typed != meetingQuestion || !strings.Contains(search.turn.refusal("use the PRIVATE budget", nil), "copies words") {
					t.Fatal("attachment was not separated from the admitted question")
				}
				if reason := search.turn.refusal("Acme Robotics", nil); reason != "" {
					t.Fatal(reason)
				}
			}
		})
	}
}

func TestMainChatWebSearchPrivacy(t *testing.T) {
	a := webSearchTestApp(t, "Dana Whitfield")
	calls := stubWebSearch(t, true, "A result.", researchMeta{Results: 1}, nil)
	for _, tc := range []struct {
		typed, query string
		allowed      bool
	}{
		{meetingQuestion, "Dana Whitfield", false},
		{"Dana met with Whitfield", "Dana Whitfield", false},
		{"Dana Whitfield mentioned Acme", "Dana Whitfield", true},
		{meetingQuestion, "Acme Robotics", true},
		{meetingQuestion, "Slack", true},
		{meetingQuestion, "Japan", true},
		{meetingQuestion, "secret budget", false},
		{"release and secret and budget and schedule", "release secret budget schedule", false},
		{"release secret budget schedule", "release secret budget schedule", true},
		{"my email is bo@example.com", "bo@example.com", false},
	} {
		turn := &chatWebTurn{typed: tc.typed}
		turn.addContext("Release: secret budget schedule at Acme Robotics.")
		before := len(*calls)
		result, err := (&chatWebSearchTool{app: a, turn: turn}).Execute(t.Context(), searchCall(tc.query))
		if err != nil || result.IsError == tc.allowed || (len(*calls) > before) != tc.allowed {
			t.Fatalf("allowed=%v got %+v, %v", tc.allowed, result, err)
		}
	}
	a.closedEyes.enabled.Store(true)
	before := len(*calls)
	result, err := (&chatWebSearchTool{app: a, turn: &chatWebTurn{typed: meetingQuestion}}).Execute(t.Context(), searchCall("Acme Robotics"))
	if err != nil || !result.IsError || !strings.Contains(result.Content, "Closed Eyes") || len(*calls) != before {
		t.Fatal("Closed Eyes did not stop the search")
	}
}

func TestMainChatWebSearchCiteSources(t *testing.T) {
	a, tag := topicChatTestApp(t)
	turn := &chatWebTurn{tag: tag, typed: meetingQuestion}
	turn.addContext("Meeting tomorrow with Acme Robotics")
	a.eventBroadcaster = NewEventBroadcaster()
	events := a.eventBroadcaster.AddClient("reading-test")
	var reading struct {
		Event string
		Args  []struct {
			SessionID string
			Ref       string
			Source    chatSource
		}
	}
	prevRun, prevReady := chatWebSearchRun, chatWebSearchReady
	t.Cleanup(func() { chatWebSearchRun, chatWebSearchReady = prevRun, prevReady })
	chatWebSearchReady = func() bool { return true }
	chatWebSearchRun = func(ctx context.Context, prompt string) (string, researchMeta, error) {
		if !strings.Contains(prompt, `"Acme Robotics"`) || strings.Contains(prompt, "tomorrow") {
			t.Fatal("outbound search includes more than the query")
		}
		ctx.Value(chatWebLaunchKey{}).(*chatWebLaunch).started()
		select {
		case raw := <-events:
			if err := json.Unmarshal(raw, &reading); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("missing Reading event while search is running")
		}
		return "It makes robots.", researchMeta{Results: 1}, nil
	}
	result, err := (&chatWebSearchTool{app: a, turn: turn}).Execute(t.Context(), searchCall("Acme Robotics"))
	_, receipt, ok := strings.Cut(result.Content, "\nref=")
	ref, query, _ := strings.Cut(receipt, " ")
	if err != nil || result.IsError || !ok || !strings.HasPrefix(ref, "web:") || query != "Acme Robotics" {
		t.Fatalf("search result: %+v %v", result, err)
	}
	input := map[string]any{"sources": []any{map[string]any{"ref": ref, "claim": "makes robots"}}}
	for i, sourceTurn := range []*chatWebTurn{turn, {tag: tag, typed: meetingQuestion}} {
		id := []string{"same", "later"}[i]
		cite := a.mainChatSourcesTools(tag, promptOriginMac, sourceTurn)[0]
		r, err := cite.Execute(t.Context(), draftCall(id, input))
		if err != nil || r.IsError {
			t.Fatalf("cite: %+v %v", r, err)
		}
		row, err := a.store.LoadStreamItem(t.Context(), "sources:"+tag+":"+id)
		if i == 1 {
			if row != nil || !strings.Contains(r.Content, "None of those refs") {
				t.Fatal("another turn cited the search")
			}
			continue
		}
		var p chatSourcesPayload
		if err != nil || row == nil || json.Unmarshal([]byte(row.ToolInput), &p) != nil || len(p.Sources) != 1 {
			t.Fatal("missing sources line")
		}
		s := p.Sources[0]
		if reading.Event != "main-chat:reading" || len(reading.Args) != 1 ||
			reading.Args[0].Ref != ref || reading.Args[0].SessionID != tag || reading.Args[0].Source != s {
			t.Fatal("Reading and CiteSources disagree")
		}
		if s.Kind != "web" || "web:"+s.ID != ref || s.Title != query || s.At <= 0 || s.Target.Kind != mainChatTargetNone {
			t.Fatalf("wrong source: %+v", s)
		}
	}
}

// CCA14 C1 counts at process start, including when the started process fails.
func TestMainChatWebSearchWeeklyCount(t *testing.T) {
	a := webSearchTestApp(t, "Dana Whitfield")
	if err := a.store.StartDestinationRecorder(); err != nil {
		t.Fatal(err)
	}
	oldBinary, oldReady := researchClaudeBinary, chatWebSearchReady
	t.Cleanup(func() { researchClaudeBinary, chatWebSearchReady = oldBinary, oldReady })
	binary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	researchClaudeBinary = binary // Git rejects the research CLI flags and exits without contacting a service.
	chatWebSearchReady = func() bool { return true }
	turn := &chatWebTurn{typed: meetingQuestion}
	turn.addContext("Acme Robotics meeting")
	tool := &chatWebSearchTool{app: a, turn: turn}
	for _, tc := range []struct {
		query string
		want  int
	}{{"Dana Whitfield", 0}, {"Acme Robotics", 1}, {"Acme Robotics", 2}, {"secret mail words here", 2}} {
		r, err := tool.Execute(t.Context(), searchCall(tc.query))
		if err != nil || !r.IsError {
			t.Fatalf("expected model-visible failure: %+v %v", r, err)
		}
		rows, err := a.store.WeeklyDestinationCounts(t.Context(), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		actual := 0
		for _, row := range rows {
			if row.ID == destinations.AssistantSearch && !row.Incomplete {
				if row.Detail != destinations.ChatSearchDetail {
					t.Fatal("missing Chat attribution")
				}
				actual += row.Calls
			}
		}
		if actual != tc.want {
			t.Fatalf("got %d calls, want %d", actual, tc.want)
		}
		if tc.want > 0 {
			w := (leavesPaths{}).summary(rows, nil).WebSearches
			if w == nil || w.Service != researchViaClaudeCode || w.ThisWeek == nil || *w.ThisWeek != tc.want {
				t.Fatalf("wrong Settings row: %+v", w)
			}
		}
	}
}

func TestMainChatWebSearchUpgradeKeepsWeeklyCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	store, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDestination(t.Context(), destinations.Call{ID: destinations.AssistantSearch, Detail: "grok", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := db.QueryRow("SELECT sql FROM sqlite_schema WHERE name='destination_calls'").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	oldSchema := strings.Replace(schema, ",'chat:claude_code'", "", 1)
	if _, err := db.Exec("ALTER TABLE destination_calls RENAME TO seed_calls;" + oldSchema +
		"; INSERT INTO destination_calls SELECT * FROM seed_calls; DROP TABLE seed_calls; PRAGMA user_version=79"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO destination_calls VALUES('assistant_search',0,'chat:claude_code',1,0)"); err == nil {
		t.Fatal("fixture must reject the new detail before upgrade")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordDestination(t.Context(), destinations.Call{ID: destinations.AssistantSearch, Detail: destinations.ChatSearchDetail, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.WeeklyDestinationCounts(t.Context(), time.Now())
	w := (leavesPaths{}).summary(rows, err).WebSearches
	if err != nil || w == nil || w.ThisWeek == nil || *w.ThisWeek != 2 || w.Service != researchViaClaudeCode {
		t.Fatalf("weekly total after upgrade: %+v, %v", w, err)
	}
}

func TestMainChatWebSearchThreadEvents(t *testing.T) {
	turn := &chatWebTurn{typed: meetingQuestion}
	for _, name := range []string{chatWebSearchToolName, "mcp__isoform__SearchWeb", chatSourcesToolName} {
		for _, e := range []*agent.StreamEvent{
			{Type: "tool_use_start", ToolName: name, ToolUseID: name},
			{Type: "tool_use", ToolName: name, ToolUseID: name},
			{Type: "tool_result", ToolUseID: name},
		} {
			if !turn.observeToolEvent(e) {
				t.Fatal("search event would reach thread persistence")
			}
		}
	}
	if turn.observeToolEvent(&agent.StreamEvent{Type: "tool_result", ToolName: "RecentActivity", Content: "bring the private board memo"}) || !strings.Contains(turn.refusal("private board memo", nil), "public origin") || !strings.Contains(turn.refusal("the private board memo", nil), "copies words") {
		t.Fatal("read tool output missing from the privacy check")
	}
}
