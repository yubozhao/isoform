package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"wails-gui/internal/platform/activitymon"
	"yansu-agent/agent/authority"
	"yansu-agent/agent/tools"
)

// The fixture index uses bag-of-words embeddings so the real search API can
// rank text locally without a model download or a remote service.
type nativeReadWordIndex struct{}

func (nativeReadWordIndex) Provider() string    { return "word-index" }
func (nativeReadWordIndex) Model() string       { return "word-index" }
func (nativeReadWordIndex) ProviderKey() string { return "word-index" }
func (nativeReadWordIndex) EmbedQuery(ctx context.Context, text string) ([]float64, error) {
	vector := make([]float64, 32)
	for _, word := range strings.Fields(strings.ToLower(text)) {
		hash := fnv.New32a()
		hash.Write([]byte(word))
		vector[hash.Sum32()%uint32(len(vector))]++
	}
	return vector, ctx.Err()
}
func (index nativeReadWordIndex) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	vectors := make([][]float64, len(texts))
	for i, text := range texts {
		vector, err := index.EmbedQuery(ctx, text)
		if err != nil {
			return nil, err
		}
		vectors[i] = vector
	}
	return vectors, nil
}

func indexNativeReadFrame(t *testing.T, store *ActivityStore, id, body string) {
	t.Helper()
	index := nativeReadWordIndex{}
	store.SetEmbedder(index)
	vector, err := index.EmbedQuery(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(vector)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO activity_ocr_frames(session_id,segment_id,frame_time,text,embedding) VALUES(?,1,1,?,?)`, id, body, string(raw)); err != nil {
		t.Fatal(err)
	}
}

func nativeRead(t *testing.T, a *App, name string, input map[string]any) tools.ToolResult {
	t.Helper()
	reader := &chatContextTool{app: a, name: name}
	wantCapability := "activity.read"
	if strings.HasPrefix(name, "Memory") {
		wantCapability = "memory.read"
	}
	action := reader.DescribeAuthority(input)
	if action.Capability != wantCapability || action.Operation != "read" || action.Effect != authority.EffectRead {
		t.Fatal("unexpected authority for", name)
	}
	call := draftCall("read", input)
	result, err := reader.Execute(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		release, err := reader.LockResultPublication(t.Context(), call)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	return result
}

func TestChatNativeReadsCLIParity(t *testing.T) {
	a := newMCPReadTestApp(t)
	store := a.activity.store
	at := time.Date(2026, 5, 12, 10, 30, 0, 0, time.Local).UnixMilli()
	date := "2026-05-12"
	row := ActivitySessionRow{ID: "meeting-fixture", StartTime: at, EndTime: at + 60000, CreatedAt: at, AppNames: `["zoom.us"]`, IsMeeting: true, MeetingApp: "Zoom", TriageStatus: "pending"}
	events := []activitymon.ActivityEvent{
		{Timestamp: at, AppName: "zoom.us", AppBundleID: "us.zoom.xos", SpeechText: "Review the proposal.", SpeechSource: "microphone"},
		{Timestamp: at + 1000, AppName: "zoom.us", AppBundleID: "us.zoom.xos", SpeechText: "Thursday works.", SpeechSource: "system_audio"},
	}
	if err := store.SaveSession(row, events, nil); err != nil {
		t.Fatal(err)
	}
	indexNativeReadFrame(t, store, row.ID, strings.Repeat("proposal ", 40))
	if _, err := store.SaveSummary(date, "daily", "You reviewed the proposal.", "fixture", false); err != nil {
		t.Fatal(err)
	}
	// A timestamp ID can pass the card-number checksum and must still resolve.
	note, err := a.SaveMemoryEntry("memory-1795370000006", "# Working note\n- **Source**: user_edit\n\nReview on Thursday.\n")
	if err != nil {
		t.Fatal(err)
	}
	api := &cronAPIServer{app: a}
	endpoint := func(handler http.HandlerFunc, path string, into any) {
		t.Helper()
		resp := httptest.NewRecorder()
		handler(resp, httptest.NewRequest("GET", path, nil))
		if resp.Code != 200 {
			t.Fatalf("endpoint %s status %d", path, resp.Code)
		}
		if err := json.Unmarshal(resp.Body.Bytes(), into); err != nil {
			t.Fatal(err)
		}
	}
	var list []ActivitySessionSummary
	var journal journalResponse
	var detail ActivitySessionDetail
	var hits []ActivitySemanticResult
	var notes []memoryEntrySummary
	var shown json.RawMessage
	endpoint(api.handleActivitySessions, "/activity/sessions?date="+date, &list)
	endpoint(api.handleActivitySummary, "/activity/summary?date="+date, &journal)
	endpoint(api.handleActivitySessionByID, "/activity/sessions/"+row.ID, &detail)
	endpoint(api.handleActivitySearch, "/activity/search?q=proposal", &hits)
	endpoint(api.handleMemoryList, "/memory/list?limit=1", &notes)
	endpoint(api.handleMemoryShow, "/memory/show/"+note.ID, &shown)
	if len(list) != 1 || len(hits) != 1 || len(notes) != 1 || journal.Journal == "" || len(detail.Events) != 2 || detail.Events[0].SpeechSource != "microphone" || detail.Events[1].SpeechSource != "system_audio" {
		t.Fatal("incomplete endpoint fixtures")
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		ref  string
		data any
	}{
		{"ActivityDay", map[string]any{"date": date}, "activity:" + row.ID, list[0]},
		{"ActivityList", map[string]any{"limit": 1}, "activity:" + row.ID, list[0]},
		{"ActivitySearch", map[string]any{"query": "proposal", "limit": 1}, "activity:" + row.ID, hits[0]},
		{"ActivitySession", map[string]any{"id": row.ID}, "activity:" + row.ID, detail},
		{"MemoryList", map[string]any{"dimension": notes[0].Dimension, "limit": 1}, "memory:" + note.ID, notes[0]},
		{"MemoryShow", map[string]any{"id": note.ID}, "memory:" + note.ID, shown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nativeRead(t, a, tc.name, tc.args)
			raw, err := json.Marshal(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			if got.IsError || !strings.Contains(got.Content, "ref="+tc.ref+" "+string(raw)) {
				t.Fatalf("%s parity: failed=%v ref=%v payload=%v redactedPayload=%v", tc.name, got.IsError, strings.Contains(got.Content, "ref="+tc.ref), strings.Contains(got.Content, string(raw)), strings.Contains(got.Content, redactActivityModelText(string(raw))))
			}
			if tc.name == "ActivityDay" && !strings.Contains(got.Content, journal.Journal) {
				t.Fatal("day lost readable narrative")
			}
			payload, dropped := a.chatSourcesFromInput(t.Context(), []any{map[string]any{"ref": tc.ref}})
			if dropped != 0 || len(payload.Sources) != 1 {
				t.Fatal("CiteSources could not resolve tool ref")
			}
			a.closedEyes.enabled.Store(true)
			paused := nativeRead(t, a, tc.name, tc.args)
			a.closedEyes.enabled.Store(false)
			if !paused.IsError || !strings.Contains(paused.Content, "paused") {
				t.Fatal("paused read did not refuse")
			}
		})
	}
}

func TestChatNativeReadsWithdrawals(t *testing.T) {
	a := newMCPReadTestApp(t)
	store := a.activity.store
	for _, fixture := range []struct{ id, app, bundle, status string }{
		{"mail-only", "Mail", "com.apple.mail", "pending"},
		{"protected-only", "Editor", "com.example.editor", "protected"},
		{"sensitive-only", "1Password", "com.1password.desktop", "pending"},
	} {
		at := seedMCPReadActivity(t, a, fixture.id, fixture.app, fixture.bundle, "fixture", "private match", fixture.status)
		indexNativeReadFrame(t, store, fixture.id, "fixture fixture fixture fixture private match")
		date := time.UnixMilli(at).Format("2006-01-02")
		if _, err := store.SaveSummary(date, "daily", "withheld journal", "fixture", false); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			args map[string]any
		}{
			{"ActivityDay", map[string]any{"date": date}}, {"ActivityList", nil}, {"ActivitySearch", map[string]any{"query": "fixture"}}, {"ActivitySession", map[string]any{"id": fixture.id}},
		} {
			got := nativeRead(t, a, tc.name, tc.args)
			if strings.Contains(got.Content, fixture.id) || strings.Contains(got.Content, "withheld journal") || strings.Contains(got.Content, "private match") || got.IsError != (tc.name == "ActivitySession") {
				t.Fatalf("unexpected privacy result for %s/%s", fixture.id, tc.name)
			}
		}
	}
	note, err := a.SaveMemoryEntry("", "# Withdrawn note\n- **Source**: user_edit\n\nA unique discarded plan.\n")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := a.GetMemoryForgetPreview(note.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ForgetMemoryEntry(note.ID, preview.ConfirmationToken); err != nil {
		t.Fatal(err)
	}
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, note.ID+".md"), []byte(note.Content), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"MemoryList", "MemoryShow"} {
		got := nativeRead(t, a, name, map[string]any{"id": note.ID})
		if strings.Contains(got.Content, note.ID) || got.IsError != (name == "MemoryShow") {
			t.Fatalf("restored forgotten note escaped %s", name)
		}
	}
}

func TestChatNativeReadsFailuresAndBounds(t *testing.T) {
	a := newMCPReadTestApp(t)
	if _, err := a.activity.store.db.Exec("DROP TABLE activity_ocr_frames"); err != nil {
		t.Fatal(err)
	}
	a.activity.store.SetEmbedder(nativeReadWordIndex{})
	if got := nativeRead(t, a, "ActivitySearch", map[string]any{"query": "fixture"}); !got.IsError {
		t.Fatal("store error became an empty search")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := (&chatContextTool{app: a, name: "ActivityList"}).Execute(ctx, draftCall("read", nil))
	if err != nil || !got.IsError {
		t.Fatal("canceled read returned success")
	}
	for _, s := range []string{strings.Repeat("界", 30000), strings.Repeat("line\n", 4000)} {
		got := boundChatRead(s)
		if len(got) > 32*1024 || strings.Count(got, "\n") >= 3000 || !utf8.ValidString(got) || !strings.Contains(got, "truncated") {
			t.Fatal("read outside output budget")
		}
	}
}

func TestChatNativeReadsOrderingAndLimits(t *testing.T) {
	a := newMCPReadTestApp(t)
	store := a.activity.store
	at := time.Date(2026, 5, 12, 9, 0, 0, 0, time.Local)
	for i := 0; i < 22; i++ {
		id := fmt.Sprintf("session-%02d", i)
		ms := at.Add(time.Duration(i) * time.Minute).UnixMilli()
		row := ActivitySessionRow{ID: id, StartTime: ms, EndTime: ms + 1000, CreatedAt: ms, AppNames: `["Editor"]`, TriageStatus: "pending"}
		if err := store.SaveSession(row, []activitymon.ActivityEvent{{Timestamp: ms, AppName: "Editor", AppBundleID: "com.example.editor"}}, nil); err != nil {
			t.Fatal(err)
		}
		indexNativeReadFrame(t, store, id, "example")
	}
	day := nativeRead(t, a, "ActivityDay", map[string]any{"date": "2026-05-12"})
	if day.IsError || strings.Count(day.Content, "ref=activity:") != 20 || strings.Contains(day.Content, "ref=activity:session-01") {
		t.Fatal("day list lost CLI default limit")
	}
	search := nativeRead(t, a, "ActivitySearch", map[string]any{"query": "example", "limit": 22})
	refs := regexp.MustCompile(`ref=activity:(session-\d+)`).FindAllStringSubmatch(search.Content, -1)
	if search.IsError || len(refs) != 22 {
		t.Fatal("search lost matching rows")
	}
	for i, ref := range refs {
		if ref[1] != fmt.Sprintf("session-%02d", 21-i) {
			t.Fatal("search results out of order")
		}
	}
	note, err := a.SaveMemoryEntry("", "# Long note\n- **Source**: user_edit\n\n"+strings.Repeat("bounded text ", 5000))
	if err != nil {
		t.Fatal(err)
	}
	show := nativeRead(t, a, "MemoryShow", map[string]any{"id": note.ID})
	if show.IsError || len(show.Content) > 32*1024 || !strings.Contains(show.Content, "truncated") || !strings.Contains(show.Content, "ref=memory:"+note.ID) {
		t.Fatal("memory show outside CLI bounds")
	}
}
