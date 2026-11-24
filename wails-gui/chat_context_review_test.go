package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/storage"
	"yansu-agent/agent"
)

func TestContextReviewRound1StandingRules(t *testing.T) {
	app := newMCPReadTestApp(t)
	app.slackDaemon = &daemonClient{connected: true}
	prompt := app.composePrompt("Post this to Slack", "chat", "", skillHubDomainChat, "English", string(promptOriginMac), "external-chat")
	for _, want := range []string{"You are Isoform", "RULES:", "NEVER show users code, config files, or dev setup steps", "NEVER present multiple options or give time estimates", "2-3 sentences max, then one question", "warm, personable, and conversational", "CONNECTED INTEGRATIONS (", "- Slack", "do NOT ask for webhook URLs", "runs no commands", "HandOffWork"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("standing prompt lost %q", want)
		}
	}
	// Chat runs no commands (computer-conformance-answers v1.4.0 S25, S19).
	for _, absent := range []string{"<memory-cli>", "<activity-cli>", "<cua-tool>", "cron add --name", "opencli list", "RUNTIME ENVIRONMENT", "yansu CLI"} {
		if strings.Contains(prompt, absent) {
			t.Errorf("standing prompt contains manual %q", absent)
		}
	}
}

func TestContextReviewRound1OpenerRules(t *testing.T) {
	_, c := newChatContextSourceFixture(t)
	result := chatContextMCPCall(t, c, "RecentActivity", map[string]any{})
	for _, want := range []string{"Greet them by name", "ONE salient thing", "Do NOT list or enumerate recent work", "ONE inviting, specific question", "For targeted questions", "project activity marker"} {
		if !strings.Contains(result, want) {
			t.Errorf("RecentActivity lost opener contract %q: %s", want, result)
		}
	}
}

func TestContextReviewRound1MemoryGuidance(t *testing.T) {
	_, c := newChatContextSourceFixture(t)
	result := chatContextMCPCall(t, c, "IsoformHelp", map[string]any{"query": "memory"})
	// The manual names Chat's own memory tools, never a CLI (S25).
	for _, want := range []string{"MemorySearch", "MemoryList", "MemoryShow"} {
		if !strings.Contains(result, want) {
			t.Errorf("memory Help lost %q", want)
		}
	}
	if strings.Contains(result, "yansu memory") || strings.Contains(result, "Bash") {
		t.Error("memory Help still documents the CLI")
	}
	if strings.Contains(result, "AUTO-INJECT (already happening") {
		t.Error("Help claims external auto-injection")
	}
}

func TestContextReviewRound1RedactedMemoryFreshness(t *testing.T) {
	app, c := newChatContextSourceFixture(t)
	writeMCPReadMemory(t, "contact-detail", "context-source", "contactneedle useful personal detail email alice@example.com, 电话: +86 000 0013 8000")
	raw, err := os.ReadFile(filepath.Join(app.memoryStore.baseDir, "contact-detail.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, count := pii.RedactText(string(raw))
	if count < 2 || isUserEditedMemory(string(raw)) {
		t.Fatalf("invalid raw observed-memory fixture: redactions=%d edited=%v", count, isUserEditedMemory(string(raw)))
	}
	app.memoryStore.MarkDirty("new-contact-detail")
	result := chatContextMCPCall(t, c, "MemorySearch", map[string]any{"query": "contactneedle"})
	if strings.Contains(result, "isError: true") || !strings.Contains(result, "contactneedle useful personal detail") {
		t.Errorf("redacted current memory refused: %s", result)
	}
	for _, secret := range []string{"alice@example.com", "000 0013 8000"} {
		if strings.Contains(result, secret) {
			t.Errorf("memory leaked %s", secret)
		}
	}
	// A same-length source edit must still be rejected/refreshed after redaction.
	writeMCPReadMemory(t, "contact-detail", "context-source", "contactneedle newer personal detail email alice@example.com, 电话: +86 000 0013 8000")
	result = chatContextMCPCall(t, c, "MemorySearch", map[string]any{"query": "contactneedle"})
	if strings.Contains(result, "useful personal detail") || !strings.Contains(result, "newer personal detail") {
		t.Errorf("redaction bypassed freshness: %s", result)
	}
}

func TestContextReviewRound1CanceledSyncRepairsNativeFTS(t *testing.T) {
	app := newChatContextSourceApp(t)
	store := app.memoryStore
	if err := os.MkdirAll(store.knowledgeDir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"deleted.md": "withdrawnneedle old deleted source", "edited.md": "editedneedle old edited source", "kept.md": "keptneedle unchanged readable source"} {
		if err := os.WriteFile(filepath.Join(store.knowledgeDir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store.embedder = &activityEmbeddingTestProvider{}
	if err := store.Sync("warm-native-fts"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.knowledgeDir, "deleted.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.knowledgeDir, "edited.md"), []byte("editedneedle current edited source"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &cancelContextRepairEmbedder{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(provider.release)
	store.mu.Lock()
	store.embedder = provider
	store.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- store.syncContext(ctx, "cancel-after-delete") }()
	select {
	case <-provider.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("sync did not pause after delete")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("sync ignored cancellation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled sync did not return")
	}
	for _, phase := range []string{"immediately after cancel", "after next native sync"} {
		rows, err := store.searchKeyword(t.Context(), store.db, "withdrawnneedle", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Errorf("%s: native FTS recalled deleted text: %+v", phase, rows)
		}
		kept, err := store.searchKeyword(t.Context(), store.db, "keptneedle", 10)
		if err != nil || len(kept) != 1 {
			t.Fatalf("lost readable native control: %+v %v", kept, err)
		}
		store.mu.Lock()
		store.embedder = &activityEmbeddingTestProvider{}
		store.mu.Unlock()
		if err := store.Sync("native-recovery"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestContextReviewRound1ResultsAreTransient(t *testing.T) {
	app := newChatContextSourceApp(t)
	main, err := app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var observed []AgentEvent
	app.onAgentEvent(func(evt AgentEvent) {
		if evt.Type != "tool_result" {
			return
		}
		observed = append(observed, evt)
		if _, err := app.PersistStreamItem(main.LocalID, StreamItemData{ID: evt.EventID, Kind: "result", Content: evt.Content, ToolName: evt.ToolName, ToolUseID: evt.ToolUseID}); err != nil {
			t.Error(err)
		}
	})
	c := chatContextMCPClient(t, &agent.TaskInput{WorkDir: os.Getenv("HOME"), ExtraTools: app.chatContextTools(main.LocalID)}, func(evt *agent.StreamEvent) error {
		if evt.Type == "tool_result" {
			app.emitAgentEvent(AgentEvent{Type: evt.Type, Tag: main.LocalID, Content: evt.Content, ToolName: evt.ToolName, ToolUseID: evt.ToolUseID})
		}
		return nil
	})
	for _, tc := range []struct{ name, query, marker string }{
		{"MemorySearch", "project", "project memory marker"}, {"PeopleLookup", "Project Person", "project relationship marker"}, {"RecentActivity", "", "project activity marker"}, {"IsoformHelp", "memory", "MemoryList"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := chatContextMCPCall(t, c, tc.name, map[string]any{"query": tc.query})
			if !strings.Contains(result, tc.marker) {
				t.Fatalf("executor lost source control %q: %s", tc.marker, result)
			}
			// Also cover the Wails persistence API independently of safe events.
			directID := "direct-" + tc.name
			if _, err := app.PersistStreamItem(main.LocalID, StreamItemData{ID: directID, Kind: "result", Content: tc.marker, ToolName: tc.name}); err != nil {
				t.Fatal(err)
			}
			direct, err := app.store.LoadStreamItem(t.Context(), directID)
			if err != nil || direct == nil || direct.Content == "" || strings.Contains(direct.Content, tc.marker) {
				t.Errorf("persistence API retained raw result: %+v %v", direct, err)
			}
			event := observed[len(observed)-1]
			if strings.Contains(event.Content, tc.marker) {
				t.Error("raw source reached renderer event")
			}
			row, err := app.store.LoadStreamItem(t.Context(), event.EventID)
			if err != nil || row == nil {
				t.Fatalf("missing stored result: %+v %v", row, err)
			}
			if row.Content == "" || strings.Contains(row.Content, tc.marker) {
				t.Error("stored result retained raw source")
			}
			for _, name := range []string{tc.name, "mcp__isoform__" + tc.name} {
				// Legacy/raw rows and direct phone reply copies must also be safe.
				raw := storage.StreamItemRow{Kind: "result", ToolName: name, Content: tc.marker}
				phone, ok := mobileChatItemFromRow(raw)
				if !ok || phone.Text == "" || strings.Contains(phone.Text, tc.marker) {
					t.Errorf("phone projected raw %s result: %+v", name, phone)
				}
				var reply mobileChatReply
				reply.note(AgentEvent{Type: "tool_result", ToolName: name, Content: tc.marker})
				if len(reply.rows) != 1 || strings.Contains(reply.rows[0].Content, tc.marker) {
					t.Errorf("phone reply retained raw %s result", name)
				}
			}
		})
	}
	var reply mobileChatReply
	reply.note(AgentEvent{Type: "tool_result", ToolName: "Read", Content: "ordinary native read"})
	if len(reply.rows) != 1 || reply.rows[0].Content != "ordinary native read" {
		t.Fatal("ordinary native result changed")
	}
}

func TestContextReviewRound1ActivityExcludesMailAndMessaging(t *testing.T) {
	app, c := newChatContextSourceFixture(t)
	for i, tc := range []struct{ name, bundle string }{{"Mail", "com.apple.mail"}, {"Slack", "com.tinyspeck.slackmacgap"}, {"Messages", "com.apple.MobileSMS"}, {"Renamed", "com.apple.mail"}} {
		id := fmt.Sprintf("private-message-%d", i)
		seedMCPReadActivity(t, app, id, tc.name, tc.bundle, "privatetitlemarker"+fmt.Sprint(i), "privateocrmarker"+fmt.Sprint(i), "done")
		if _, err := app.activity.store.db.Exec(`UPDATE activity_events SET kind='focus' WHERE session_id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	// A mixed session's OCR has no event-level identity; it must be excluded too.
	seedMCPReadActivity(t, app, "mixed", "Editor", "com.example.editor", "mixedtitlemarker", "mixedocrmarker", "done")
	if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET app_names='["Editor","Slack"]' WHERE id='mixed'`); err != nil {
		t.Fatal(err)
	}
	result := chatContextMCPCall(t, c, "RecentActivity", map[string]any{})
	if !strings.Contains(result, "project activity marker") || !strings.Contains(result, "project") {
		t.Fatalf("ordinary activity lost: %s", result)
	}
	for _, secret := range []string{"privatetitlemarker", "privateocrmarker", "mixedtitlemarker", "mixedocrmarker"} {
		if strings.Contains(result, secret) {
			t.Errorf("external activity leaked %s: %s", secret, result)
		}
	}
}
