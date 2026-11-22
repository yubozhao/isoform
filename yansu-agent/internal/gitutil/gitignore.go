package gitutil

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IsTrackedByGit checks if a path is already tracked by Git
// Returns true if the path is tracked, false otherwise
//
//isoform:destination local
func IsTrackedByGit(projectRoot, relativePath string) bool {
	cmd := exec.Command("git", "ls-files", relativePath)
	hideConsoleWindow(cmd)
	cmd.Dir = projectRoot
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	// If output is non-empty, the file is tracked
	return strings.TrimSpace(string(output)) != ""
}

// IsInGitIgnore checks if a pattern is already in .gitignore
func IsInGitIgnore(projectRoot, pattern string) bool {
	gitignorePath := filepath.Join(projectRoot, ".gitignore")
	file, err := os.Open(gitignorePath)
	if err != nil {
		return false
	}
	defer file.Close()

	// Normalize pattern for comparison
	normalizedPattern := normalizeGitIgnorePattern(pattern)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if normalizeGitIgnorePattern(line) == normalizedPattern {
			return true
		}
	}

	return false
}

// normalizeGitIgnorePattern normalizes a gitignore pattern for comparison
func normalizeGitIgnorePattern(pattern string) string {
	// Remove leading/trailing slashes and whitespace
	pattern = strings.TrimSpace(pattern)
	pattern = strings.TrimPrefix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	return pattern
}

// EnsureGitIgnore ensures a pattern is in .gitignore if the path is not tracked by Git
// It checks if the path is tracked, and if not, adds it to .gitignore
// Returns true if the pattern was added to .gitignore
func EnsureGitIgnore(projectRoot, relativePath string) bool {
	// First check if it's already tracked by Git
	if IsTrackedByGit(projectRoot, relativePath) {
		return false
	}

	// Check if it's already in .gitignore
	if IsInGitIgnore(projectRoot, relativePath) {
		return false
	}

	// Add to .gitignore
	return AddToGitIgnore(projectRoot, relativePath)
}

// AddToGitIgnore appends a pattern to .gitignore
// Returns true on success, false on failure
func AddToGitIgnore(projectRoot, pattern string) bool {
	gitignorePath := filepath.Join(projectRoot, ".gitignore")

	// Check if file exists and if it ends with newline
	needsNewline := false
	if data, err := os.ReadFile(gitignorePath); err == nil {
		if len(data) > 0 && data[len(data)-1] != '\n' {
			needsNewline = true
		}
	}

	file, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: unable to open .gitignore: %v\n", err)
		return false
	}
	defer file.Close()

	// Prepare content to write
	content := pattern
	if needsNewline {
		content = "\n" + content
	}
	content += "\n"

	if _, err := file.WriteString(content); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: unable to write to .gitignore: %v\n", err)
		return false
	}

	return true
}

// GetAgentInstructionPatterns returns patterns for agent instruction files that should be gitignored.
// CLAUDE.md is excluded because it's often intentionally tracked by users.
// .github/copilot-instructions.md is excluded because .github may have other tracked files.
func GetAgentInstructionPatterns() []string {
	return []string{
		".cursorrules",
		".windsurfrules",
		"AGENTS.md",
	}
}
