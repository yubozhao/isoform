package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"wails-gui/internal/platform/cmdutil"

	worktreecore "wails-gui/internal/worktree"
)

// WorkspaceInfo represents a workspace backed by a git worktree.
type WorkspaceInfo struct {
	WorkspaceID string `json:"workspaceId"`
	Label       string `json:"label"`
	Branch      string `json:"branch"`
	BaseBranch  string `json:"baseBranch"`
	Path        string `json:"path"`
	CreatedAt   int64  `json:"createdAt"`
}

// ChangedFile represents a single file change in a workspace diff.
type ChangedFile = worktreecore.ChangedFile

// RenameResult is returned by RenameWorkspaceBranch with the new branch and path.
type RenameResult struct {
	Branch string `json:"branch"`
	Path   string `json:"path"`
}

// WorktreeSettings holds user-configurable worktree preferences.
type WorktreeSettings struct {
	WorktreeRoot           string `json:"worktreeRoot"`
	RepoRoot               string `json:"repoRoot"`
	BranchPrefix           string `json:"branchPrefix"`
	GitCloneTimeoutMinutes string `json:"gitCloneTimeoutMinutes"`
	WorktreeSetupCommand   string `json:"worktreeSetupCommand"`
	SetupTimeoutMinutes    string `json:"setupTimeoutMinutes"`
}

// GetWorktreeSettings returns the current worktree settings with resolved defaults.
func (a *App) GetWorktreeSettings() WorktreeSettings {
	cfg := loadConfig()
	s := WorktreeSettings{
		WorktreeRoot: cfg.WorktreeRoot,
		RepoRoot:     cfg.RepoRoot,
		BranchPrefix: cfg.BranchPrefix,
	}
	// Resolve defaults for display.
	if s.WorktreeRoot == "" {
		if base, err := defaultWorktreeBaseDir(); err == nil {
			s.WorktreeRoot = base
		}
	}
	if s.RepoRoot == "" {
		if base, err := defaultCloneBaseDir(); err == nil {
			s.RepoRoot = base
		}
	}
	if s.BranchPrefix == "" {
		s.BranchPrefix = resolveDefaultBranchPrefix()
	}

	if cfg.GitCloneTimeoutMS > 0 {
		s.GitCloneTimeoutMinutes = cloneTimeoutMinutesFromMS(cfg.GitCloneTimeoutMS)
	} else {
		s.GitCloneTimeoutMinutes = ""
	}

	s.WorktreeSetupCommand = cfg.WorktreeSetupCommand

	if cfg.WorktreeSetupTimeoutMS > 0 {
		s.SetupTimeoutMinutes = setupTimeoutMinutesFromMS(cfg.WorktreeSetupTimeoutMS)
	} else {
		s.SetupTimeoutMinutes = ""
	}

	return s
}

func cloneTimeoutMinutesFromMS(ms int) string {
	const minuteMS = int(time.Minute / time.Millisecond)
	if ms <= 0 {
		return ""
	}
	minutes := (ms + minuteMS - 1) / minuteMS
	return strconv.Itoa(minutes)
}

func setupTimeoutMinutesFromMS(ms int) string {
	const minuteMS = int(time.Minute / time.Millisecond)
	if ms <= 0 {
		return ""
	}
	minutes := (ms + minuteMS - 1) / minuteMS
	return strconv.Itoa(minutes)
}

// defaultWorktreeBaseDir returns the fallback root: ~/.yansu-agent/worktrees.
func defaultWorktreeBaseDir() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "worktrees"), nil
}

// worktreeBaseDir returns the configured (or default) root directory for worktrees.
func worktreeBaseDir() (string, error) {
	if root := loadConfig().WorktreeRoot; root != "" {
		return root, nil
	}
	return defaultWorktreeBaseDir()
}

// resolveDefaultBranchPrefix returns git user.name, falling back to "workspace".
func resolveDefaultBranchPrefix() string {
	cmd := cmdutil.GitCommand("config", "user.name")
	if out, err := cmd.Output(); err == nil {
		name := strings.TrimSpace(string(out))
		if name != "" {
			return sanitizeBranchName(name)
		}
	}
	return "workspace"
}

// branchPrefix returns the configured prefix or the resolved default.
func branchPrefix() string {
	if p := loadConfig().BranchPrefix; p != "" {
		return p
	}
	return resolveDefaultBranchPrefix()
}

const (
	gitCommandTimeoutDefault = worktreecore.GitCommandTimeoutDefault
	gitCommandTimeoutLong    = worktreecore.GitCommandTimeoutLong
)

// sanitizeBranchName converts a human-readable label into a valid git branch suffix.
// Keeps letters (including Unicode), digits, and hyphens. Truncates to 50 chars.
func sanitizeBranchName(label string) string {
	return worktreecore.SanitizeBranchName(label)
}

func gitCommandTimeout(args []string) time.Duration {
	return worktreecore.GitCommandTimeout(args)
}

func gitCommandTimeoutFromEnv(fallback time.Duration) time.Duration {
	return worktreecore.GitCommandTimeoutFromEnv(fallback)
}

func managedWorktreeRoots() []string {
	seen := make(map[string]struct{}, 2)
	roots := make([]string, 0, 2)

	appendRoot := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return
		}
		absRoot = filepath.Clean(absRoot)
		if _, ok := seen[absRoot]; ok {
			return
		}
		seen[absRoot] = struct{}{}
		roots = append(roots, absRoot)
	}

	if configured, err := worktreeBaseDir(); err == nil {
		appendRoot(configured)
	}
	if fallback, err := defaultWorktreeBaseDir(); err == nil {
		appendRoot(fallback)
	}

	return roots
}

func canSafelyRemoveWorktreePath(pathValue string) bool {
	return worktreecore.CanSafelyRemovePath(pathValue, managedWorktreeRoots())
}

func removeWorktreePathSafe(pathValue string) error {
	return worktreecore.RemovePathSafe(pathValue, managedWorktreeRoots())
}

func (a *App) worktreeService() *worktreecore.Service {
	return worktreecore.NewService(a.store, worktreecore.Dependencies{
		IsGitRepo:              a.IsGitRepo,
		WorktreeBaseDir:        worktreeBaseDir,
		DefaultWorktreeBaseDir: defaultWorktreeBaseDir,
		BranchPrefix:           branchPrefix,
		Translator: func(prompt string) (string, error) {
			return a.runEphemeralHaiku(prompt, 15*time.Second)
		},
	})
}

// CreateWorkspace creates a new git worktree-backed workspace.
// If a setup command is configured (global or per-project), it runs asynchronously
// after creation and emits a "worktree:setup-complete" event with the result.
func (a *App) CreateWorkspace(projectPath, projectID, workspaceID, label, baseBranch string) (*WorkspaceInfo, error) {
	info, err := a.worktreeService().CreateWorkspace(projectPath, projectID, workspaceID, label, baseBranch)
	if err != nil {
		return nil, err
	}

	// Resolve per-project setup command from SQLite before launching goroutine.
	var dbSetupCmd string
	if a.store != nil {
		dbSetupCmd, _ = a.store.GetProjectSetupCommand(a.ctx, projectID)
	}

	// Fire-and-forget: run setup command in background, emit event on completion.
	go func() {
		result := runWorktreeSetupCommand(dbSetupCmd, projectPath, info.Path, workspaceID)
		if result != nil {
			a.emitBrowserEvent("worktree:setup-complete", result)
		}
	}()

	return &WorkspaceInfo{
		WorkspaceID: info.WorkspaceID,
		Label:       info.Label,
		Branch:      info.Branch,
		BaseBranch:  info.BaseBranch,
		Path:        info.Path,
		CreatedAt:   info.CreatedAt,
	}, nil
}

// RemoveWorkspace removes a git worktree and cleans up its branch and DB record.
func (a *App) RemoveWorkspace(projectPath, workspaceID string) error {
	return a.worktreeService().RemoveWorkspace(projectPath, workspaceID)
}

// ListWorkspaces returns all workspaces for a project.
func (a *App) ListWorkspaces(projectID string) ([]WorkspaceInfo, error) {
	rows, err := a.worktreeService().ListWorkspaces(projectID)
	if err != nil {
		return nil, err
	}

	var workspaces []WorkspaceInfo
	for _, w := range rows {
		workspaces = append(workspaces, WorkspaceInfo{
			WorkspaceID: w.WorkspaceID,
			Label:       w.Label,
			Branch:      w.Branch,
			BaseBranch:  w.BaseBranch,
			Path:        w.Path,
			CreatedAt:   w.CreatedAt,
		})
	}
	return workspaces, nil
}

// GetWorkspaceChangedFiles returns the list of files changed in a workspace compared to base branch.
func (a *App) GetWorkspaceChangedFiles(worktreePath, baseBranch string) ([]ChangedFile, error) {
	return worktreecore.GetChangedFiles(worktreePath, baseBranch)
}

// GetWorkspaceFileDiff returns the unified diff for a single file in a workspace.
func (a *App) GetWorkspaceFileDiff(worktreePath, baseBranch, filePath string) (string, error) {
	return worktreecore.GetFileDiff(worktreePath, baseBranch, filePath)
}

// GitStageFiles stages the given file paths in a repository.
func (a *App) GitStageFiles(repoPath string, paths []string) error {
	return worktreecore.StageFiles(repoPath, paths)
}

// GitUnstageFiles unstages the given file paths in a repository.
func (a *App) GitUnstageFiles(repoPath string, paths []string) error {
	return worktreecore.UnstageFiles(repoPath, paths)
}

// GitDiscardFiles discards working directory changes. Tracked files are restored
// via git checkout, untracked files are removed via git clean.
func (a *App) GitDiscardFiles(repoPath string, tracked, untracked []string) error {
	return worktreecore.DiscardFiles(repoPath, tracked, untracked)
}

// GitCommit creates a commit with the given message from currently staged changes.
func (a *App) GitCommit(repoPath, message string) error {
	return worktreecore.Commit(repoPath, message)
}

// RenameWorkspaceBranch renames the workspace's label, git branch, and worktree directory.
func (a *App) RenameWorkspaceBranch(projectPath, workspaceID, newLabel string) (*RenameResult, error) {
	result, err := a.worktreeService().RenameWorkspaceBranch(projectPath, workspaceID, newLabel)
	if err != nil {
		return nil, err
	}
	return &RenameResult{Branch: result.Branch, Path: result.Path}, nil
}

// MergeWorkspace merges a workspace branch into main worktree and cleans up.
func (a *App) MergeWorkspace(projectPath, workspaceID, worktreePath, branch string) error {
	return a.worktreeService().MergeWorkspace(projectPath, workspaceID, worktreePath, branch)
}
