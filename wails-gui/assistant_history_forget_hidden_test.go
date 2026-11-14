package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Know-how learned from assistant history and then hidden as agent-only
// (activity_origin: agent) is still on disk. Turning history off must forget
// it like any other history know-how; the page readers skip hidden files, so
// the forget cannot list through them.
func TestAssistantHistoryOffForgetsHiddenAgentKnowHow(t *testing.T) {
	app, _ := newForgetTestApp(t)
	knowledgeDir, err := knowledgeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(knowledgeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hidden, ok := markLearnedFromAgents(renderOKFDocument(map[string]any{"title": "Hidden history how-to", "source": assistantHistorySource}, "A how-to learned from an agent's history."))
	if !ok {
		t.Fatal("fixture not marked")
	}
	observed := renderOKFDocument(map[string]any{"title": "Observed how-to", "source": "observed"}, "An independent how-to.")
	for id, content := range map[string]string{"hidden-history-knowhow": hidden, "observed-knowhow": observed} {
		if err := os.WriteFile(filepath.Join(knowledgeDir, id+".md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.SetReadAssistantHistory(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(knowledgeDir, "hidden-history-knowhow.md")); !os.IsNotExist(err) {
		t.Fatalf("hidden history know-how survived history off: %v", err)
	}
	if _, err := os.Stat(filepath.Join(knowledgeDir, "observed-knowhow.md")); err != nil {
		t.Fatalf("independent know-how removed: %v", err)
	}
}
