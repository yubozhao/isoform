package persistence

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"

	"wails-gui/storage"
)

// Service encapsulates persistence-related business logic and mapping.
type Service struct {
	store *storage.Store
}

// NewService builds a persistence service over the provided store.
func NewService(store *storage.Store) *Service {
	return &Service{store: store}
}

// LoadAllProjects loads persisted projects with workspaces and session
// metadata. Stream items are deliberately excluded so startup and sidebar
// refreshes do not pull every chat's recent history into the frontend; callers
// use LoadStreamItemsPage to hydrate the active session on demand.
func (s *Service) LoadAllProjects(ctx context.Context) ([]ProjectData, error) {
	if s.store == nil {
		return nil, nil
	}

	projectRows, err := s.store.LoadProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("load projects: %w", err)
	}

	workspacesByProject, err := s.store.LoadAllWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("load workspaces: %w", err)
	}

	sessionsByWorkspace, err := s.store.LoadAllSessionsByWorkspace(ctx)
	if err != nil {
		return nil, fmt.Errorf("load sessions: %w", err)
	}

	itemCountsBySession, err := s.store.LoadStreamItemCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("load stream item counts: %w", err)
	}

	var result []ProjectData
	for _, pr := range projectRows {
		pd := ProjectData{
			ID:                pr.ID,
			Name:              pr.Name,
			Path:              pr.Path,
			Organization:      pr.Organization,
			Product:           pr.Product,
			IsYansuProject:    pr.IsYansuProject,
			ActiveSessionID:   pr.ActiveSessionID,
			ActiveWorkspaceID: pr.ActiveWorkspaceID,
			SortOrder:         pr.SortOrder,
		}

		for _, wr := range workspacesByProject[pr.ID] {
			wd := WorkspaceData{
				ID:              wr.ID,
				Label:           wr.Label,
				IsDefault:       wr.IsDefault,
				WorktreePath:    wr.WorktreePath,
				WorktreeBranch:  wr.WorktreeBranch,
				BaseBranch:      wr.BaseBranch,
				ActiveSessionID: wr.ActiveSessionID,
				SortOrder:       wr.SortOrder,
				CreatedAt:       wr.CreatedAt,
				UpdatedAt:       wr.UpdatedAt,
			}

			for _, sr := range sessionsByWorkspace[wr.ID] {
				sd := SessionData{
					LocalID:          sr.LocalID,
					WorkspaceID:      sr.WorkspaceID,
					SessionID:        sr.SessionID,
					Label:            sr.Label,
					Model:            sr.Model,
					CreatedAt:        sr.CreatedAt,
					PrdEnabled:       sr.PrdEnabled,
					ThinkingLevel:    sr.ThinkingLevel,
					PlanMode:         sr.PlanMode,
					ThinkingEnabled:  sr.ThinkingEnabled,
					Pinned:           sr.Pinned,
					PinnedAt:         sr.PinnedAt,
					AutomationResult: sr.AutomationResult,
					Kind:             sr.Kind,
					ParentID:         sr.ParentID,
					UpdatedAt:        sr.UpdatedAt,
					StreamItemCount:  itemCountsBySession[sr.LocalID],
				}

				wd.Sessions = append(wd.Sessions, sd)
			}

			pd.Workspaces = append(pd.Workspaces, wd)
		}

		result = append(result, pd)
	}

	return result, nil
}

// EnsureProjectStructure inserts a project, its workspaces, and their sessions
// only if they do not already exist (ON CONFLICT DO NOTHING). Used to satisfy
// foreign-key constraints before inserting stream items, without overwriting
// mutable fields that may have been updated concurrently (e.g. session labels).
func (s *Service) EnsureProjectStructure(ctx context.Context, data ProjectData) error {
	if s.store == nil {
		return nil
	}

	projectRow := storage.ProjectRow{
		ID:                data.ID,
		Name:              data.Name,
		Path:              data.Path,
		Organization:      data.Organization,
		Product:           data.Product,
		IsYansuProject:    data.IsYansuProject,
		ActiveSessionID:   data.ActiveSessionID,
		ActiveWorkspaceID: data.ActiveWorkspaceID,
		SortOrder:         data.SortOrder,
	}

	var workspaceRows []storage.WorkspaceRow
	var sessionRows []storage.SessionRow

	for _, wd := range data.Workspaces {
		workspaceRows = append(workspaceRows, storage.WorkspaceRow{
			ID:              wd.ID,
			ProjectID:       data.ID,
			Label:           wd.Label,
			IsDefault:       wd.IsDefault,
			WorktreePath:    wd.WorktreePath,
			WorktreeBranch:  wd.WorktreeBranch,
			BaseBranch:      wd.BaseBranch,
			ActiveSessionID: wd.ActiveSessionID,
			CreatedAt:       wd.CreatedAt,
			UpdatedAt:       wd.UpdatedAt,
		})

		for _, sd := range wd.Sessions {
			sessionRows = append(sessionRows, storage.SessionRow{
				LocalID:         sd.LocalID,
				ProjectID:       data.ID,
				WorkspaceID:     wd.ID,
				SessionID:       sd.SessionID,
				Label:           sd.Label,
				Model:           sd.Model,
				PrdEnabled:      sd.PrdEnabled,
				ThinkingLevel:   sd.ThinkingLevel,
				PlanMode:        sd.PlanMode,
				ThinkingEnabled: sd.ThinkingEnabled,
				Pinned:          sd.Pinned,
				PinnedAt:        sd.PinnedAt,
				Kind:            sd.Kind,
				ParentID:        sd.ParentID,
				CreatedAt:       sd.CreatedAt,
				UpdatedAt:       sd.UpdatedAt,
			})
		}
	}

	return s.store.EnsureProjectStructure(ctx, projectRow, workspaceRows, sessionRows)
}

// SaveProjectState persists a full project with workspaces and sessions
// (without stream items — those are saved via PersistStreamItem).
func (s *Service) SaveProjectState(ctx context.Context, data ProjectData) error {
	if s.store == nil {
		return nil
	}

	projectRow := storage.ProjectRow{
		ID:                data.ID,
		Name:              data.Name,
		Path:              data.Path,
		Organization:      data.Organization,
		Product:           data.Product,
		IsYansuProject:    data.IsYansuProject,
		ActiveSessionID:   data.ActiveSessionID,
		ActiveWorkspaceID: data.ActiveWorkspaceID,
		SortOrder:         data.SortOrder,
	}

	var workspaceRows []storage.WorkspaceRow
	var sessionRows []storage.SessionRow

	for _, wd := range data.Workspaces {
		workspaceRows = append(workspaceRows, storage.WorkspaceRow{
			ID:              wd.ID,
			ProjectID:       data.ID,
			Label:           wd.Label,
			IsDefault:       wd.IsDefault,
			WorktreePath:    wd.WorktreePath,
			WorktreeBranch:  wd.WorktreeBranch,
			BaseBranch:      wd.BaseBranch,
			ActiveSessionID: wd.ActiveSessionID,
			CreatedAt:       wd.CreatedAt,
			UpdatedAt:       wd.UpdatedAt,
		})

		for _, sd := range wd.Sessions {
			sessionRows = append(sessionRows, storage.SessionRow{
				LocalID:          sd.LocalID,
				ProjectID:        data.ID,
				WorkspaceID:      wd.ID,
				SessionID:        sd.SessionID,
				Label:            sd.Label,
				Model:            sd.Model,
				PrdEnabled:       sd.PrdEnabled,
				ThinkingLevel:    sd.ThinkingLevel,
				PlanMode:         sd.PlanMode,
				ThinkingEnabled:  sd.ThinkingEnabled,
				Pinned:           sd.Pinned,
				PinnedAt:         sd.PinnedAt,
				AutomationResult: sd.AutomationResult,
				Kind:             sd.Kind,
				ParentID:         sd.ParentID,
				CreatedAt:        sd.CreatedAt,
				UpdatedAt:        sd.UpdatedAt,
			})
		}
	}

	return s.store.SaveProjectFull(ctx, projectRow, workspaceRows, sessionRows)
}

// DeleteProjectData removes a project and all its data.
func (s *Service) DeleteProjectData(ctx context.Context, id string) error {
	if s.store == nil {
		return nil
	}
	return s.store.DeleteProject(ctx, id)
}

// SaveSessionState persists a single session under a project.
func (s *Service) SaveSessionState(ctx context.Context, projectID string, data SessionData) error {
	if s.store == nil {
		return nil
	}
	return s.store.SaveSession(ctx, storage.SessionRow{
		LocalID:          data.LocalID,
		ProjectID:        projectID,
		WorkspaceID:      data.WorkspaceID,
		SessionID:        data.SessionID,
		Label:            data.Label,
		Model:            data.Model,
		PrdEnabled:       data.PrdEnabled,
		ThinkingLevel:    data.ThinkingLevel,
		PlanMode:         data.PlanMode,
		ThinkingEnabled:  data.ThinkingEnabled,
		Pinned:           data.Pinned,
		PinnedAt:         data.PinnedAt,
		AutomationResult: data.AutomationResult,
		Kind:             data.Kind,
		ParentID:         data.ParentID,
		CreatedAt:        data.CreatedAt,
		UpdatedAt:        data.UpdatedAt,
	})
}

// ForkSession snapshots a completed chat into a new independent session.
func (s *Service) ForkSession(ctx context.Context, sourceLocalID, newLabel string) (ForkSessionResult, error) {
	if s.store == nil {
		return ForkSessionResult{}, nil
	}

	row, itemCount, err := s.store.ForkSession(ctx, sourceLocalID, uuid.NewString(), newLabel)
	if err != nil {
		return ForkSessionResult{}, errors.Wrap(err, "fork session")
	}

	return ForkSessionResult{
		ProjectID: row.ProjectID,
		Session: SessionData{
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
			StreamItemCount:  itemCount,
		},
	}, nil
}

// SaveWorkspaceState persists a single workspace.
func (s *Service) SaveWorkspaceState(ctx context.Context, projectID string, data WorkspaceData) error {
	if s.store == nil {
		return nil
	}
	return s.store.SaveWorkspace(ctx, storage.WorkspaceRow{
		ID:              data.ID,
		ProjectID:       projectID,
		Label:           data.Label,
		IsDefault:       data.IsDefault,
		WorktreePath:    data.WorktreePath,
		WorktreeBranch:  data.WorktreeBranch,
		BaseBranch:      data.BaseBranch,
		ActiveSessionID: data.ActiveSessionID,
		SortOrder:       data.SortOrder,
		CreatedAt:       data.CreatedAt,
		UpdatedAt:       data.UpdatedAt,
	})
}

// DeleteSessionData removes a session and all its items.
func (s *Service) DeleteSessionData(ctx context.Context, localID string) error {
	if s.store == nil {
		return nil
	}
	return s.store.DeleteSession(ctx, localID)
}

// DeleteWorkspaceData removes a workspace and all its sessions/items.
func (s *Service) DeleteWorkspaceData(ctx context.Context, workspaceID string) error {
	if s.store == nil {
		return nil
	}
	return s.store.DeleteWorkspace(ctx, workspaceID)
}

// PersistStreamItem appends a single stream item to the database.
func (s *Service) PersistStreamItem(ctx context.Context, sessionLocalID string, item StreamItemData) (int, error) {
	if s.store == nil {
		return 0, nil
	}
	return s.store.AppendStreamItem(ctx, sessionLocalID, storage.StreamItemRow{
		ID:                item.ID,
		Kind:              item.Kind,
		Content:           item.Content,
		ToolName:          item.ToolName,
		ToolInput:         item.ToolInput,
		ToolUseID:         item.ToolUseID,
		RunID:             item.RunID,
		SessionID:         item.SessionID,
		Images:            item.Images,
		AutomationContext: item.AutomationContext,
		Files:             item.Files,
	})
}

// UpdateStreamItemContent updates the content of an existing stream item.
func (s *Service) UpdateStreamItemContent(ctx context.Context, id, content string) error {
	if s.store == nil {
		return nil
	}
	return s.store.UpdateStreamItemContent(ctx, id, content)
}

// GrowStreamItemContent writes a chat's streamed row; an assistant or thinking
// row never gets shorter (storage.GrowStreamItemContent). It reports whether
// the row was written.
func (s *Service) GrowStreamItemContent(ctx context.Context, sessionLocalID, id, content string) (bool, error) {
	if s.store == nil {
		return false, nil
	}
	return s.store.GrowStreamItemContent(ctx, sessionLocalID, id, content)
}

// UpdateSessionClaudeID updates the Claude session_id for a session.
func (s *Service) UpdateSessionClaudeID(ctx context.Context, localID, sessionID string) error {
	if s.store == nil {
		return nil
	}
	return s.store.UpdateSessionSessionID(ctx, localID, sessionID)
}

// SaveActiveProjectID persists the currently active project ID.
func (s *Service) SaveActiveProjectID(ctx context.Context, id string) error {
	if s.store == nil {
		return nil
	}
	return s.store.SetUIState(ctx, "active_project_id", id)
}

// LoadActiveProjectID loads the persisted active project ID.
func (s *Service) LoadActiveProjectID(ctx context.Context) (string, error) {
	if s.store == nil {
		return "", nil
	}
	return s.store.GetUIState(ctx, "active_project_id")
}

// UpdateProjectActiveSession updates the active session for a project.
func (s *Service) UpdateProjectActiveSession(ctx context.Context, projectID, sessionID string) error {
	if s.store == nil {
		return nil
	}
	return s.store.UpdateProjectActiveSession(ctx, projectID, sessionID)
}

// UpdateProjectActiveWorkspace updates the active workspace for a project.
func (s *Service) UpdateProjectActiveWorkspace(ctx context.Context, projectID, workspaceID string) error {
	if s.store == nil {
		return nil
	}
	return s.store.UpdateProjectActiveWorkspace(ctx, projectID, workspaceID)
}

// UpdateWorkspaceActiveSession updates the active session for a workspace.
func (s *Service) UpdateWorkspaceActiveSession(ctx context.Context, workspaceID, sessionID string) error {
	if s.store == nil {
		return nil
	}
	return s.store.UpdateWorkspaceActiveSession(ctx, workspaceID, sessionID)
}

// StreamItemVersion returns the session's item version (see storage.StreamItemVersion).
func (s *Service) StreamItemVersion(ctx context.Context, sessionLocalID string) (int64, error) {
	if s.store == nil {
		return 0, nil
	}
	return s.store.StreamItemVersion(ctx, sessionLocalID)
}

// LoadStreamItemsPage loads a page of stream items using cursor-based pagination.
// If cursor < 0 the most recent `limit` items are returned; otherwise items
// with sort_order < cursor are returned.
func (s *Service) LoadStreamItemsPage(ctx context.Context, sessionLocalID string, cursor, limit int) (StreamItemPageData, error) {
	if s.store == nil {
		return StreamItemPageData{}, nil
	}
	if limit <= 0 {
		limit = 50
	}

	queryStart := time.Now()
	var page storage.StreamItemPage
	var err error
	switch {
	case cursor < 0:
		page, err = s.store.LoadStreamItemsLatest(ctx, sessionLocalID, limit)
	default:
		page, err = s.store.LoadStreamItemsBefore(ctx, sessionLocalID, cursor, limit)
	}
	if err != nil {
		log.Printf("[perf] binding=LoadStreamItemsPage stage=query ms=%d", time.Since(queryStart).Milliseconds())
		return StreamItemPageData{}, fmt.Errorf("load stream items page: %w", err)
	}
	log.Printf("[perf] binding=LoadStreamItemsPage stage=query ms=%d", time.Since(queryStart).Milliseconds())

	decodeStart := time.Now()
	items := make([]StreamItemData, len(page.Items))
	for i, ir := range page.Items {
		items[i] = ToStreamItemData(ir)
	}
	log.Printf("[perf] binding=LoadStreamItemsPage stage=decode ms=%d", time.Since(decodeStart).Milliseconds())

	nextCursor := -1
	if len(items) > 0 {
		nextCursor = items[0].SortOrder
	}

	return StreamItemPageData{Items: items, HasMore: page.HasMore, NextCursor: nextCursor}, nil
}

// ReorderProjects updates the sort_order of all projects based on the given ID order.
func (s *Service) ReorderProjects(ctx context.Context, ids []string) error {
	if s.store == nil {
		return nil
	}
	return s.store.UpdateProjectSortOrders(ctx, ids)
}

// ReorderWorkspaces updates the sort_order of workspaces within a project based on the given ID order.
func (s *Service) ReorderWorkspaces(ctx context.Context, projectID string, ids []string) error {
	if s.store == nil {
		return nil
	}
	return s.store.UpdateWorkspaceSortOrders(ctx, projectID, ids)
}

// NextProjectSortOrder returns the next available sort_order for a new project.
func (s *Service) NextProjectSortOrder(ctx context.Context) (int, error) {
	if s.store == nil {
		return 0, nil
	}
	return s.store.NextProjectSortOrder(ctx)
}

// --- Sidebar Groups ---

// LoadAllSidebarGroups loads all sidebar groups with their items.
func (s *Service) LoadAllSidebarGroups(ctx context.Context) ([]SidebarGroupData, error) {
	if s.store == nil {
		return nil, nil
	}

	groups, itemsByGroup, err := s.store.LoadAllSidebarGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("load sidebar groups: %w", err)
	}

	result := make([]SidebarGroupData, len(groups))
	for i, g := range groups {
		gd := SidebarGroupData{
			ID:        g.ID,
			Name:      g.Name,
			SortOrder: g.SortOrder,
			Collapsed: g.Collapsed,
			CreatedAt: g.CreatedAt,
			UpdatedAt: g.UpdatedAt,
		}
		for _, item := range itemsByGroup[g.ID] {
			gd.Items = append(gd.Items, toSidebarGroupItemData(item))
		}
		if gd.Items == nil {
			gd.Items = []SidebarGroupItemData{}
		}
		result[i] = gd
	}

	return result, nil
}

// CreateSidebarGroup creates a new sidebar group.
func (s *Service) CreateSidebarGroup(ctx context.Context, name string) (SidebarGroupData, error) {
	if s.store == nil {
		return SidebarGroupData{}, nil
	}
	row, err := s.store.CreateSidebarGroup(ctx, name)
	if err != nil {
		return SidebarGroupData{}, err
	}
	return SidebarGroupData{
		ID:        row.ID,
		Name:      row.Name,
		SortOrder: row.SortOrder,
		Collapsed: row.Collapsed,
		Items:     []SidebarGroupItemData{},
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

// RenameSidebarGroup updates the name of a sidebar group.
func (s *Service) RenameSidebarGroup(ctx context.Context, id, name string) error {
	if s.store == nil {
		return nil
	}
	return s.store.RenameSidebarGroup(ctx, id, name)
}

// DeleteSidebarGroup removes a sidebar group and all its items.
func (s *Service) DeleteSidebarGroup(ctx context.Context, id string) error {
	if s.store == nil {
		return nil
	}
	return s.store.DeleteSidebarGroup(ctx, id)
}

// ReorderSidebarGroups updates group sort orders based on the given ID order.
func (s *Service) ReorderSidebarGroups(ctx context.Context, ids []string) error {
	if s.store == nil {
		return nil
	}
	return s.store.ReorderSidebarGroups(ctx, ids)
}

// SetSidebarGroupCollapsed updates the collapsed state of a sidebar group.
func (s *Service) SetSidebarGroupCollapsed(ctx context.Context, id string, collapsed bool) error {
	if s.store == nil {
		return nil
	}
	return s.store.SetSidebarGroupCollapsed(ctx, id, collapsed)
}

// AddItemToSidebarGroup adds an item reference to a sidebar group.
func (s *Service) AddItemToSidebarGroup(ctx context.Context, groupID, itemType, refID string) (SidebarGroupItemData, error) {
	if s.store == nil {
		return SidebarGroupItemData{}, nil
	}
	row, err := s.store.AddItemToSidebarGroup(ctx, groupID, itemType, refID)
	if err != nil {
		return SidebarGroupItemData{}, err
	}
	return toSidebarGroupItemData(row), nil
}

// RemoveItemFromSidebarGroup removes an item from a sidebar group.
func (s *Service) RemoveItemFromSidebarGroup(ctx context.Context, groupID, itemID string) error {
	if s.store == nil {
		return nil
	}
	return s.store.RemoveItemFromSidebarGroup(ctx, groupID, itemID)
}

// RemoveGroupItemsByRef removes all group references for a given item type and ref ID.
func (s *Service) RemoveGroupItemsByRef(ctx context.Context, itemType, refID string) error {
	if s.store == nil {
		return nil
	}
	return s.store.RemoveGroupItemsByRef(ctx, itemType, refID)
}

// ReorderSidebarGroupItems updates item sort orders within a group.
func (s *Service) ReorderSidebarGroupItems(ctx context.Context, groupID string, itemIDs []string) error {
	if s.store == nil {
		return nil
	}
	return s.store.ReorderSidebarGroupItems(ctx, groupID, itemIDs)
}

// CleanupOrphanGroupItems removes sidebar group items with invalid references.
func (s *Service) CleanupOrphanGroupItems(ctx context.Context, validChatIDs, validAutomationIDs []string) error {
	if s.store == nil {
		return nil
	}
	return s.store.CleanupOrphanGroupItems(ctx, validChatIDs, validAutomationIDs)
}

// DeleteProjectWithGroupCleanup removes a project and cleans up group references.
func (s *Service) DeleteProjectWithGroupCleanup(ctx context.Context, projectID string) error {
	if s.store == nil {
		return nil
	}
	return s.store.DeleteProjectWithGroupCleanup(ctx, projectID)
}

// DeleteWorkspaceWithGroupCleanup removes a workspace and cleans up group references.
func (s *Service) DeleteWorkspaceWithGroupCleanup(ctx context.Context, workspaceID string) error {
	if s.store == nil {
		return nil
	}
	return s.store.DeleteWorkspaceWithGroupCleanup(ctx, workspaceID)
}

func toSidebarGroupItemData(ir storage.SidebarGroupItemRow) SidebarGroupItemData {
	return SidebarGroupItemData{
		ID:        ir.ID,
		GroupID:   ir.GroupID,
		ItemType:  ir.ItemType,
		RefID:     ir.RefID,
		SortOrder: ir.SortOrder,
		AddedAt:   ir.AddedAt,
	}
}

// ToStreamItemData is the shared renderer projection for ordinary and topic pages.
func ToStreamItemData(ir storage.StreamItemRow) StreamItemData {
	return StreamItemData{
		ID:                ir.ID,
		CreatedAt:         ir.CreatedAt,
		Kind:              ir.Kind,
		Content:           ir.Content,
		ToolName:          ir.ToolName,
		ToolInput:         ir.ToolInput,
		ToolUseID:         ir.ToolUseID,
		RunID:             ir.RunID,
		SessionID:         ir.SessionID,
		SortOrder:         ir.SortOrder,
		Images:            ir.Images,
		AutomationContext: ir.AutomationContext,
		Files:             ir.Files,
	}
}
