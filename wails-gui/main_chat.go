package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"wails-gui/storage"
)

// mainChatTitle is the name the main chat carries in the sidebar. The main
// chat is the one conversation the user keeps talking in; background work
// reports into it and side chats split off from it.
const mainChatTitle = "Isoform"

// MainChatInfo tells the renderer where the main chat lives.
type MainChatInfo struct {
	ProjectID   string      `json:"projectId"`
	WorkspaceID string      `json:"workspaceId"`
	Session     SessionData `json:"session"`
}

// mainChatEnsureMu serializes ensureMainChat: startup and the first producer
// to post can race, and two creators would leave two main chats.
var mainChatEnsureMu sync.Mutex

// ensureMainChat returns the one main chat session, creating it in the
// standalone project when the install has none yet. A main chat that an
// older build hid is restored rather than replaced, so there is never a
// second one. Called on startup and again by every caller that posts into
// the main chat, so a database reset heals on first use.
func (a *App) ensureMainChat(ctx context.Context) (*storage.SessionRow, error) {
	if a == nil || a.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	mainChatEnsureMu.Lock()
	defer mainChatEnsureMu.Unlock()

	existing, err := a.store.LoadMainSession(ctx)
	if err == nil {
		if existing.Hidden {
			if restoreErr := a.store.RestoreSession(ctx, existing.LocalID); restoreErr != nil {
				return nil, fmt.Errorf("restore main chat: %w", restoreErr)
			}
			existing.Hidden = false
			a.markMainChatWaitingDirty("main-chat-created")
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load main chat: %w", err)
	}

	projectID, workspaceID, err := a.standaloneChatProjectAndWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	row := storage.SessionRow{
		LocalID:     uuid.NewString(),
		ProjectID:   projectID,
		WorkspaceID: workspaceID,
		Label:       mainChatTitle,
		Model:       a.chatTaskModel(),
		Kind:        storage.SessionKindMain,
	}
	if err := a.store.SaveSession(ctx, row); err != nil {
		return nil, fmt.Errorf("create main chat: %w", err)
	}
	saved, err := a.store.LoadSession(ctx, row.LocalID)
	if err != nil {
		return nil, fmt.Errorf("reload main chat: %w", err)
	}
	log.Printf("[main-chat] created %s", saved.LocalID)
	a.markMainChatWaitingDirty("main-chat-created")
	return saved, nil
}

// ensureMainChatOnStartup creates the main chat before the renderer loads
// its first state, so the sidebar's Isoform row always has a session behind it.
func (a *App) ensureMainChatOnStartup() {
	if a == nil || a.store == nil {
		return
	}
	if _, err := a.ensureMainChat(context.Background()); err != nil {
		log.Printf("[main-chat] startup ensure failed: %v", err)
		return
	}
	a.ensureMainChatIntroOnStartup()
}

// GetMainChat returns the main chat, creating it when missing. The renderer
// calls it when its loaded state has no session of kind "main".
func (a *App) GetMainChat() (perfValue MainChatInfo, perfErr error) {
	if err := chatFeatureErr(); err != nil {
		return perfValue, err
	}
	defer logPageBinding("GetMainChat", time.Now(), &perfValue)
	row, err := a.ensureMainChat(context.Background())
	if err != nil {
		return MainChatInfo{}, err
	}
	return MainChatInfo{
		ProjectID:   row.ProjectID,
		WorkspaceID: row.WorkspaceID,
		Session:     sessionRowToData(*row),
	}, nil
}

// sessionRowToData maps a stored session to the renderer's session shape
// without its items, the way LoadState does.
func sessionRowToData(row storage.SessionRow) SessionData {
	return SessionData{
		LocalID:          row.LocalID,
		WorkspaceID:      row.WorkspaceID,
		SessionID:        row.SessionID,
		Label:            row.Label,
		Model:            row.Model,
		CreatedAt:        row.CreatedAt,
		PrdEnabled:       row.PrdEnabled,
		ThinkingLevel:    row.ThinkingLevel,
		PlanMode:         row.PlanMode,
		ThinkingEnabled:  row.ThinkingEnabled,
		Pinned:           row.Pinned,
		PinnedAt:         row.PinnedAt,
		AutomationResult: row.AutomationResult,
		Kind:             row.Kind,
		ParentID:         row.ParentID,
		UpdatedAt:        row.UpdatedAt,
	}
}

// isMainChatSession reports whether localID is the main chat. The renderer
// never offers to delete it; DeleteSessionData, DeleteWorkspaceData and
// DeleteProjectData refuse to remove it or cascade over it.
func (a *App) isMainChatSession(ctx context.Context, localID string) bool {
	if a == nil || a.store == nil || localID == "" {
		return false
	}
	row, err := a.store.LoadSession(ctx, localID)
	return err == nil && row != nil && row.Kind == storage.SessionKindMain
}

// errMainChatProtected is returned when a caller tries to delete the main
// chat; the store refuses too, so it is the store's error.
var errMainChatProtected = storage.ErrMainChatProtected

// mainChatLivesIn reports whether the main chat sits in the project or, when
// workspaceID is set, in that workspace, so cascading deletes can refuse.
func (a *App) mainChatLivesIn(ctx context.Context, projectID, workspaceID string) bool {
	if a == nil || a.store == nil {
		return false
	}
	main, err := a.store.LoadMainSession(ctx)
	if err != nil || main == nil {
		return false
	}
	if workspaceID != "" {
		return main.WorkspaceID == workspaceID
	}
	return main.ProjectID == projectID
}
