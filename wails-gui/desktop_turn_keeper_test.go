package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/storage"

	"github.com/google/uuid"
)

const desktopTurnTestChat = "desktop-chat-1"

func desktopTurnTestApp(t *testing.T) *App {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "yansu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project := storage.ProjectRow{ID: "project-1", Name: "Repo", Path: filepath.Join(t.TempDir(), "repo")}
	workspaces := []storage.WorkspaceRow{{ID: "workspace-1", ProjectID: project.ID, Label: "Main", IsDefault: true}}
	if err := store.EnsureProjectStructure(t.Context(), project, workspaces, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(t.Context(), storage.SessionRow{LocalID: desktopTurnTestChat, ProjectID: project.ID, WorkspaceID: "workspace-1", Label: "Notes"}); err != nil {
		t.Fatal(err)
	}
	previous := mobileChatSettleDelay
	mobileChatSettleDelay = 10 * time.Millisecond
	t.Cleanup(func() { mobileChatSettleDelay = previous })
	return &App{store: store, activeTags: map[string]bool{}}
}

func appendRow(t *testing.T, app *App, row storage.StreamItemRow) {
	t.Helper()
	if row.ID == "" {
		row.ID = uuid.NewString()
	}
	if _, err := app.store.AppendStreamItem(t.Context(), desktopTurnTestChat, row); err != nil {
		t.Fatal(err)
	}
}

func chatKinds(t *testing.T, app *App) []storage.StreamItemRow {
	t.Helper()
	page, err := app.store.LoadStreamItemsLatest(t.Context(), desktopTurnTestChat, 50)
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

// waitRows waits for the settle timer to store want rows in all.
func waitRows(t *testing.T, app *App, want int) []storage.StreamItemRow {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows := chatKinds(t, app)
		if len(rows) >= want || time.Now().After(deadline) {
			return rows
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runDesktopTurn(app *App, events []AgentEvent, stopped bool) {
	app.beginDesktopTurn(desktopTurnTestChat, promptOriginMac)
	for _, evt := range events {
		evt.Tag = desktopTurnTestChat
		app.desktopTurns.observe(evt)
	}
	app.finishDesktopTurn(desktopTurnTestChat, stopped)
}

func TestDesktopTurnWithNoWindowKeepsTheAnswer(t *testing.T) {
	app := desktopTurnTestApp(t)
	appendRow(t, app, storage.StreamItemRow{Kind: "user", Content: "What is on today?"})
	runDesktopTurn(app, []AgentEvent{{Type: "run_created", RunID: "r1"}, {Type: "assistant", RunID: "r1", Content: "Two meetings "}, {Type: "assistant", RunID: "r1", Content: "and a review."}}, false)
	rows := waitRows(t, app, 2)
	if len(rows) != 2 || rows[1].Kind != "assistant" || rows[1].Content != "Two meetings and a review." {
		t.Fatalf("the hub did not keep the answer: %+v", rows)
	}
}

func TestDesktopTurnEndingEmptyStoresNoReply(t *testing.T) {
	app := desktopTurnTestApp(t)
	appendRow(t, app, storage.StreamItemRow{Kind: "user", Content: "Hello?"})
	runDesktopTurn(app, []AgentEvent{{Type: "run_created", RunID: "r1"}, {Type: "done", RunID: "r1"}}, false)
	rows := waitRows(t, app, 2)
	if len(rows) != 2 || rows[1].Kind != "error" || chatErrorCauseOf(rows[1]) != chatCodeNoReply {
		t.Fatalf("an empty turn left the message unanswered: %+v", rows)
	}
}

func TestDesktopTurnStoredByTheWindowIsLeftAlone(t *testing.T) {
	app := desktopTurnTestApp(t)
	appendRow(t, app, storage.StreamItemRow{Kind: "user", Content: "Hello?"})
	app.beginDesktopTurn(desktopTurnTestChat, promptOriginMac)
	app.desktopTurns.observe(AgentEvent{Type: "assistant", Tag: desktopTurnTestChat, RunID: "r1", Content: "Hi."})
	// The window stores the reply itself.
	appendRow(t, app, storage.StreamItemRow{Kind: "assistant", Content: "Hi."})
	app.finishDesktopTurn(desktopTurnTestChat, false)
	time.Sleep(100 * time.Millisecond)
	if rows := chatKinds(t, app); len(rows) != 2 {
		t.Fatalf("the hub stored a second copy beside the window's: %+v", rows)
	}
}

func TestDesktopTurnStoppedWithNothingStoresNothing(t *testing.T) {
	app := desktopTurnTestApp(t)
	appendRow(t, app, storage.StreamItemRow{Kind: "user", Content: "Hello?"})
	runDesktopTurn(app, nil, true)
	time.Sleep(100 * time.Millisecond)
	if rows := chatKinds(t, app); len(rows) != 1 {
		t.Fatalf("a stopped turn got a row: %+v", rows)
	}
}

func TestDesktopTurnKeptOnlyForComputerMessages(t *testing.T) {
	for _, origin := range []promptOrigin{promptOriginPhone, promptOriginPhoneAsk, promptOriginPanelAsk, promptOriginResume} {
		if desktopTurnKept(origin) {
			t.Fatalf("origin %q got the desktop copy", origin)
		}
	}
	if !desktopTurnKept(promptOriginMac) {
		t.Fatal("a message typed on the computer got no copy")
	}
}

func TestStartupSweepMarksAMessageTheQuitCutOff(t *testing.T) {
	t.Run("typed on the computer", func(t *testing.T) {
		app := desktopTurnTestApp(t)
		appendRow(t, app, storage.StreamItemRow{Kind: "assistant", Content: "Earlier answer."})
		appendRow(t, app, storage.StreamItemRow{Kind: "user", Content: "And then?"})
		app.markUnansweredTurns(t.Context())
		rows := chatKinds(t, app)
		if len(rows) != 3 || chatErrorCauseOf(rows[2]) != chatCodeNoReply {
			t.Fatalf("the cut-off message was not marked: %+v", rows)
		}
		app.markUnansweredTurns(t.Context())
		if again := chatKinds(t, app); len(again) != 3 {
			t.Fatalf("a second sweep marked it twice: %+v", again)
		}
	})
	t.Run("a phone message still waiting is left alone", func(t *testing.T) {
		app := desktopTurnTestApp(t)
		appendRow(t, app, storage.StreamItemRow{Kind: "user", Content: "From the phone", ToolInput: mobileChatPhoneOriginToolInput})
		app.markUnansweredTurns(t.Context())
		if rows := chatKinds(t, app); len(rows) != 1 {
			t.Fatalf("a waiting phone message was marked: %+v", rows)
		}
	})
	t.Run("a turn running since launch is left alone", func(t *testing.T) {
		app := desktopTurnTestApp(t)
		appendRow(t, app, storage.StreamItemRow{Kind: "user", Content: "Running"})
		app.activeTags[desktopTurnTestChat] = true
		app.markUnansweredTurns(t.Context())
		if rows := chatKinds(t, app); len(rows) != 1 {
			t.Fatalf("a running turn was marked: %+v", rows)
		}
	})
}

// chatErrorCauseOf reads the code an error row carries.
func chatErrorCauseOf(row storage.StreamItemRow) string {
	var cause chatErrorCause
	_ = json.Unmarshal([]byte(row.ToolInput), &cause)
	return cause.Code
}
