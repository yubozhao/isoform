package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/storage"
)

func TestWorkProfileDiskSourcesConfinedToKnowHow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("YANSU_AGENT_HOME", root)
	dir, err := knowledgeDir()
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	notes := []string{
		workProfileTestNote("one", "Client updates", "You write clear updates.", first, "activity", []string{"Editor"}, []string{"a", "b", "c"}, nil),
		workProfileTestNote("two", "Client updates", "You lead with decisions.", first.Add(15*24*time.Hour), "activity", []string{"Editor"}, []string{"d", "e"}, nil),
	}
	for i, note := range notes {
		if err := os.WriteFile(filepath.Join(dir, string(rune('a'+i))+".md"), []byte(note), 0600); err != nil {
			t.Fatal(err)
		}
	}
	memory, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	canary := "PRIVATE_MEMORY_CANARY_7193"
	banned := filepath.Join(memory, "private.md")
	if err := os.WriteFile(banned, []byte(canary), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(banned, filepath.Join(dir, "linked.md")); err != nil {
		t.Fatal(err)
	}
	portrait, err := portraitPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(portrait), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(portrait, []byte(`{"canary":"PRIVATE_PORTRAIT_CANARY_7194"}`), 0600); err != nil {
		t.Fatal(err)
	}
	activity, err := NewActivityStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer activity.Close()
	activitySQL := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO activity_sessions(id,start_time,end_time,trigger_kind,app_names,event_count,segment_count,created_at) VALUES('s',0,1,'test','[]',1,0,0)`, nil},
		{`INSERT INTO activity_events(session_id,timestamp,kind,data,sort_order) VALUES('s',0,'test',?,0)`, []any{"SCREEN_TEXT_CANARY_1001"}},
		{`INSERT INTO activity_ocr_frames(session_id,segment_id,frame_time,text) VALUES('s',0,0,?)`, []any{"OCR_TEXT_CANARY_1002"}},
		{`INSERT INTO self_model_entries(id,entry_key,revision,kind,scope,statement,status,confidence,reasoning_status,evidence_json,contradictions_json,source_hash,created_at) VALUES('self','self',1,'belief','',?,'active',1,'ready','[]','[]','test',0)`, []any{"SELF_MODEL_CANARY_1003"}},
	}
	for _, row := range activitySQL {
		if _, err := activity.db.Exec(row.query, row.args...); err != nil {
			t.Fatal(err)
		}
	}
	dbPath := filepath.Join(root, "sessions.db")
	sessionStore, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer sessionStore.Close()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// This fixture connection does not enforce foreign keys, so each store's
	// sensitive text can be seeded without creating unrelated user records.
	sessionSQL := []struct {
		query string
		value string
	}{
		{`INSERT INTO mail_messages(id,mailbox,subject,at,first_seen_at,updated_at) VALUES('mail','inbox',?,0,0,0)`, "MAIL_CANARY_1004"},
		{`INSERT INTO imported_contact_cards(external_ref,payload) VALUES('contact',?)`, "CONTACT_CANARY_1005"},
		{`INSERT INTO principal_recent_messages(principal_id,app,seen_at,preview) VALUES('p','Messages',0,?)`, "MESSAGES_CANARY_1006"},
		{`INSERT INTO stream_items(id,session_local_id,kind,content,sort_order,created_at) VALUES('history','session','assistant',?,0,0)`, "ASSISTANT_CHAT_CANARY_1007"},
	}
	for _, row := range sessionSQL {
		if _, err := db.Exec(row.query, row.value); err != nil {
			t.Fatal(err)
		}
	}
	src := workProfileDiskSources{dir: dir}
	entries, err := src.KnowHow()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected source entries: %d", len(entries))
	}
	model := &workProfileFakeModel{output: `{"lines":[{"text":"You lead with decisions.","from":["c2"]}],"publicSpecialties":[]}`}
	draft, err := workProfileMakeDraft(context.Background(), src, model, func() (string, error) { return "same", nil }, nil, 0, nil, "", nil, first.Add(20*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := workProfilePreview(workProfileState{Pending: draft}, "", nil, first.Add(20*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	bodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			bodies <- body
			_, _ = w.Write([]byte(`{"version":1,"approvedAt":"2026-01-21T00:00:00Z"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	reservation := yourAgentEnv{stateFile: filepath.Join(root, "identity-test.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "test-hub", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	owner := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(root, "cache-test.json")}
	waitAgentOwnerRefreshes(t, owner.cachePath)
	path := filepath.Join(root, "profile-test.json")
	if err := workProfileWriteState(path, workProfileState{Format: 1, Pending: draft}); err != nil {
		t.Fatal(err)
	}
	if _, err := workProfileApprove(context.Background(), path, agentOwnerProfileUploader{owner}, "", nil, first.Add(20*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	body := <-bodies
	encoded, err := json.Marshal([]any{model.prompts, draft, payload, string(body)})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{canary, "PRIVATE_PORTRAIT_CANARY_7194", "SCREEN_TEXT_CANARY_1001", "OCR_TEXT_CANARY_1002", "SELF_MODEL_CANARY_1003", "MAIL_CANARY_1004", "CONTACT_CANARY_1005", "MESSAGES_CANARY_1006", "ASSISTANT_CHAT_CANARY_1007"} {
		marker := strings.TrimRight(value, "0123456789_")
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(marker)) {
			t.Fatal("excluded store reached profile")
		}
	}
}
