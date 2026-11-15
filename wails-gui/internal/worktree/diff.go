package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"wails-gui/internal/platform/cmdutil"
)

// ChangedFile represents a single file change in a workspace diff.
// A single file may appear twice (once staged, once unstaged) when it has been
// partially staged (e.g. git add then further edits).
type ChangedFile struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "added" | "modified" | "deleted" | "renamed" | "untracked"
	Staged bool   `json:"staged"`
}

// GetChangedFiles returns files changed in a workspace.
// For default workspaces (baseBranch == "HEAD"), uses git status to show
// uncommitted working directory changes. For worktree workspaces, merges
// uncommitted changes (git status) with committed branch differences
// (git diff merge-base...HEAD) so that files already committed on the branch
// still appear in the list and can be expanded to view their full diff.
func GetChangedFiles(worktreePath, baseBranch string) ([]ChangedFile, error) {
	if _, err := os.Stat(worktreePath); err != nil {
		return nil, fmt.Errorf("worktree not found: %w", err)
	}

	// Always start with working directory state.
	statusFiles, err := getStatusChangedFiles(worktreePath)
	if err != nil {
		return nil, err
	}

	// Default workspace: only uncommitted changes matter.
	if baseBranch == "HEAD" {
		return statusFiles, nil
	}

	// Worktree workspace: also include committed branch differences.
	base := resolveDiffBase(worktreePath, baseBranch)
	if base == "HEAD" {
		// merge-base == HEAD means no branch divergence yet.
		return statusFiles, nil
	}

	committedFiles, err := getCommittedBranchFiles(worktreePath, base)
	if err != nil {
		// Non-fatal: return what we have from git status.
		return statusFiles, nil
	}

	return mergeChangedFiles(statusFiles, committedFiles), nil
}

// getCommittedBranchFiles returns files changed between mergeBase and HEAD
// (committed on this branch). These are shown as staged: true since they are
// already committed and don't need further staging.
func getCommittedBranchFiles(worktreePath, mergeBase string) ([]ChangedFile, error) {
	out, err := RunGit(worktreePath, "diff", "--name-status", mergeBase+"...HEAD")
	if err != nil {
		return nil, err
	}
	return parseDiffNameStatus(out), nil
}

// parseDiffNameStatus parses output from git diff --name-status.
// Files are returned with Staged: true since they represent committed changes.
func parseDiffNameStatus(out string) []ChangedFile {
	if strings.TrimSpace(out) == "" {
		return nil
	}

	var files []ChangedFile
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		statusCode := parts[0]
		filePath := parts[len(parts)-1]

		var status string
		switch {
		case strings.HasPrefix(statusCode, "A"):
			status = "added"
		case strings.HasPrefix(statusCode, "M"):
			status = "modified"
		case strings.HasPrefix(statusCode, "D"):
			status = "deleted"
		case strings.HasPrefix(statusCode, "R"):
			status = "renamed"
		default:
			status = "modified"
		}

		files = append(files, ChangedFile{Path: filePath, Status: status, Staged: true})
	}
	return files
}

// mergeChangedFiles combines uncommitted (git status) and committed (branch diff)
// file lists. Uncommitted entries take priority; committed files only appear if
// they have no corresponding uncommitted entry.
func mergeChangedFiles(statusFiles, committedFiles []ChangedFile) []ChangedFile {
	// Build a set of paths already present from git status.
	seen := make(map[string]struct{}, len(statusFiles))
	for _, f := range statusFiles {
		seen[f.Path] = struct{}{}
	}

	// Append committed files not already covered by git status.
	merged := make([]ChangedFile, len(statusFiles), len(statusFiles)+len(committedFiles))
	copy(merged, statusFiles)
	for _, f := range committedFiles {
		if _, ok := seen[f.Path]; !ok {
			merged = append(merged, f)
		}
	}

	return merged
}

// getStatusChangedFiles uses git status --porcelain to capture all changes
// in the working directory. It separates staged (index) and unstaged (worktree)
// changes — a single file may produce two entries when partially staged.
func getStatusChangedFiles(repoPath string) ([]ChangedFile, error) {
	out, err := RunGit(repoPath, "status", "--porcelain")
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(out) == "" {
		return nil, nil
	}

	var files []ChangedFile
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		// porcelain format: XY <path>  (first two chars are status codes)
		indexStatus := line[0] // X: staged (index) status
		workStatus := line[1]  // Y: unstaged (worktree) status
		filePath := strings.TrimSpace(line[3:])

		// Handle renames: "R  old -> new"
		if idx := strings.Index(filePath, " -> "); idx >= 0 {
			filePath = filePath[idx+4:]
		}
		if filePath == "" {
			continue
		}

		// Untracked files: always unstaged.
		if indexStatus == '?' && workStatus == '?' {
			files = append(files, ChangedFile{Path: filePath, Status: "untracked", Staged: false})
			continue
		}

		// Staged entry (index has a real status).
		if indexStatus != ' ' && indexStatus != '?' {
			files = append(files, ChangedFile{
				Path:   filePath,
				Status: porcelainCharToStatus(indexStatus),
				Staged: true,
			})
		}

		// Unstaged entry (worktree has a real status).
		if workStatus != ' ' && workStatus != '?' {
			files = append(files, ChangedFile{
				Path:   filePath,
				Status: porcelainCharToStatus(workStatus),
				Staged: false,
			})
		}
	}
	return files, nil
}

// porcelainCharToStatus maps a single git porcelain status character to a
// human-readable status string.
func porcelainCharToStatus(c byte) string {
	switch c {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'M', 'T':
		return "modified"
	default:
		return "modified"
	}
}

// resolveDiffBase returns the commit ref to diff against.
// For worktree workspaces (baseBranch != "HEAD"), computes the merge-base so that
// the diff includes both committed branch changes AND uncommitted working directory
// edits. Falls back to HEAD if merge-base cannot be determined.
func resolveDiffBase(worktreePath, baseBranch string) string {
	if baseBranch == "HEAD" {
		return "HEAD"
	}
	mb, err := RunGit(worktreePath, "merge-base", baseBranch, "HEAD")
	if err != nil || strings.TrimSpace(mb) == "" {
		return "HEAD"
	}
	return strings.TrimSpace(mb)
}

// GetFileDiff returns unified diff for a single file.
// For default workspaces, diffs against HEAD. For worktree workspaces, diffs
// against the merge-base with the base branch.
// For untracked files, uses git diff --no-index to show the full content as a
// new-file diff.
func GetFileDiff(worktreePath, baseBranch, filePath string) (string, error) {
	if _, err := os.Stat(worktreePath); err != nil {
		return "", fmt.Errorf("worktree not found: %w", err)
	}

	// Check if the file is untracked by git.
	lsOut, _ := RunGit(worktreePath, "ls-files", "--", filePath)
	if strings.TrimSpace(lsOut) == "" {
		return diffUntracked(worktreePath, filePath)
	}

	base := resolveDiffBase(worktreePath, baseBranch)
	return RunGit(worktreePath, "diff", base, "--", filePath)
}

// diffUntracked generates a unified diff for an untracked file by comparing
// /dev/null against the file. git diff --no-index exits with code 1 when
// differences are found, which is expected and not an error.
func diffUntracked(worktreePath, filePath string) (string, error) {
	absFile := filepath.Join(worktreePath, filePath)
	if _, err := os.Stat(absFile); err != nil {
		return "", fmt.Errorf("file not found: %w", err)
	}

	cmd := cmdutil.GitCommand("diff", "--no-index", "--", os.DevNull, filePath)
	cmd.Dir = worktreePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Exit code 1 means "differences found" — that's the expected case.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return strings.TrimSpace(string(out)), nil
		}
		return "", fmt.Errorf("git diff --no-index: %w\n%s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// StageFiles stages the given file paths (git add -- ...paths).
func StageFiles(repoPath string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"add", "--"}, paths...)
	_, err := RunGit(repoPath, args...)
	return err
}

// UnstageFiles unstages the given file paths (git reset HEAD -- ...paths).
func UnstageFiles(repoPath string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"reset", "HEAD", "--"}, paths...)
	_, err := RunGit(repoPath, args...)
	return err
}

// DiscardFiles discards working directory changes for tracked files
// (git checkout -- ...paths) and removes untracked files (git clean -f -- ...paths).
func DiscardFiles(repoPath string, tracked, untracked []string) error {
	if len(tracked) > 0 {
		args := append([]string{"checkout", "--"}, tracked...)
		if _, err := RunGit(repoPath, args...); err != nil {
			return fmt.Errorf("discard tracked: %w", err)
		}
	}
	if len(untracked) > 0 {
		args := append([]string{"clean", "-f", "--"}, untracked...)
		if _, err := RunGit(repoPath, args...); err != nil {
			return fmt.Errorf("discard untracked: %w", err)
		}
	}
	return nil
}

// Commit creates a commit with the given message from currently staged changes.
func Commit(repoPath, message string) error {
	_, err := RunGit(repoPath, "commit", "-m", message)
	return err
}
