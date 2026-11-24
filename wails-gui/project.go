package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"wails-gui/internal/platform/appdetect"

	"github.com/skratchdot/open-golang/open"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ProjectContext represents fetched project context data
type ProjectContext struct {
	IsYansuProject  bool   `json:"is_yansu_project"`
	ProjectName     string `json:"project_name,omitempty"`
	Organization    string `json:"organization,omitempty"`
	Product         string `json:"product,omitempty"`
	PRD             string `json:"prd,omitempty"`
	PRDStatus       string `json:"prd_status,omitempty"`
	Scenarios       string `json:"scenarios,omitempty"`
	ScenariosStatus string `json:"scenarios_status,omitempty"`
	Error           string `json:"error,omitempty"`
}

// projectConfig mirrors the .something/project.json structure
type projectConfig struct {
	UUID         string `json:"uuid"`
	Name         string `json:"name"`
	Organization string `json:"organization"`
	Product      string `json:"product"`
	ProductUUID  string `json:"product_uuid"`
	YansuURL     string `json:"yansu_url"`
}

// loadProjectSetupCommand reads worktree_setup_command from .something/project.json.
// Returns empty string if absent or on any error. Uses a raw map to avoid coupling
// with the projectConfig struct (which is used by clone and detect paths).
func loadProjectSetupCommand(projectPath string) string {
	projectFile := filepath.Join(projectPath, ".something", "project.json")
	data, err := os.ReadFile(projectFile)
	if err != nil {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	if cmd, ok := raw["worktree_setup_command"].(string); ok {
		return cmd
	}
	return ""
}

// SelectWorkDir opens a directory picker dialog and returns the selected path
func (a *App) SelectWorkDir() (string, error) {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select Work Directory",
	})
	if err != nil {
		return "", err
	}
	return dir, nil
}

// DetectProject reads .something/project.json and returns basic project info
// without fetching stages from the API. This is a lightweight check.
func (a *App) DetectProject(dirPath string) (*ProjectContext, error) {
	result := &ProjectContext{IsYansuProject: false}

	if dirPath == "" {
		return result, nil
	}

	projectFile := filepath.Join(dirPath, ".something", "project.json")
	data, err := os.ReadFile(projectFile)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		result.Error = fmt.Sprintf("failed to read project config: %v", err)
		return result, nil
	}

	var cfg projectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		result.Error = fmt.Sprintf("failed to parse project config: %v", err)
		return result, nil
	}

	result.IsYansuProject = true
	result.ProjectName = cfg.Name
	result.Organization = cfg.Organization
	result.Product = cfg.Product

	return result, nil
}

// OpenProjectURL opens the Yansu web platform URL for the given project directory.
func (a *App) OpenProjectURL(projectPath string) error {
	return errNoYansuAccount
}

// OpenProjectFolder opens the project directory in the system file manager.
func (a *App) OpenProjectFolder(projectPath string) error {
	if projectPath == "" {
		return fmt.Errorf("project path is required")
	}
	info, err := os.Stat(projectPath)
	if err != nil {
		return fmt.Errorf("cannot access path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %s", projectPath)
	}
	return open.Start(projectPath)
}

// InstalledApp is an alias for appdetect.InstalledApp to maintain Wails binding compatibility.
type InstalledApp = appdetect.InstalledApp

// DetectInstalledApps returns the list of known editors/terminals installed on this machine.
func (a *App) DetectInstalledApps() []InstalledApp {
	return appdetect.DetectInstalledApps()
}

// OpenInApp opens the given project directory using the specified application.
func (a *App) OpenInApp(appBundle, projectPath string) error {
	return appdetect.OpenInApp(appBundle, projectPath)
}

// FetchProjectContext fetches PRD and scenarios from Yansu API for the given workDir
func (a *App) FetchProjectContext(workDir string) (*ProjectContext, error) {
	return nil, errNoYansuAccount
}
