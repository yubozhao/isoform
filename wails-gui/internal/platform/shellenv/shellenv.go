package shellenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ResolveCommandPath finds a command in PATH using multiple strategies.
func ResolveCommandPath(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}

	if resolved, err := exec.LookPath(name); err == nil && IsExecutableFile(resolved) {
		return resolved
	}

	if resolved := resolveCommandPathViaLoginShell(name); resolved != "" {
		return resolved
	}

	return resolveCommandPathInCommonDirs(name)
}

// ExpandUserPath expands a leading ~ to the user's home directory.
func ExpandUserPath(pathValue string) string {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		return ""
	}
	if strings.HasPrefix(pathValue, "~/") || pathValue == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		if pathValue == "~" {
			return home
		}
		return filepath.Join(home, strings.TrimPrefix(pathValue, "~/"))
	}
	return pathValue
}

// ParseMarkedValue extracts a value between start/end markers in output.
func ParseMarkedValue(output, startMarker, endMarker string) string {
	start := strings.LastIndex(output, startMarker)
	if start < 0 {
		return ""
	}
	start += len(startMarker)
	end := strings.Index(output[start:], endMarker)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(output[start : start+end])
}

func ApplyEnvVars(names ...string) []string {
	// Batch-resolve all variables in a single login shell invocation to avoid
	// spawning N×2 shell processes (critical for heavy .zshrc like oh-my-zsh).
	resolved := batchResolveEnvVarsViaLoginShell(names)
	applied := make([]string, 0, len(resolved))
	for _, name := range names {
		value := resolved[name]
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			continue
		}
		applied = append(applied, name)
	}
	return applied
}

func isSafeEnvVarName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
			continue
		case r >= 'A' && r <= 'Z':
			continue
		case i > 0 && r >= '0' && r <= '9':
			continue
		default:
			return false
		}
	}
	return true
}

// RefreshPATH re-reads PATH from the login shell and appends the entries the
// process lacks, then the directories holding names. It never drops an entry,
// so the bundled tools prepended at startup stay first. Setup calls it after
// an install, because the tool may land in a folder that was not on PATH (or
// did not exist) when the app started. It returns the directories it added.
func RefreshPATH(names ...string) []string {
	current := os.Getenv("PATH")
	sep := string(os.PathListSeparator)
	added := []string{}
	for _, dir := range filepath.SplitList(resolveUserPATH()) {
		if strings.TrimSpace(dir) == "" || pathContainsDir(current, dir) {
			continue
		}
		if current == "" {
			current = dir
		} else {
			current += sep + dir
		}
		added = append(added, dir)
	}
	if len(added) > 0 {
		_ = os.Setenv("PATH", current)
	}
	return append(added, ensureCommandDirsInPATH(names...)...)
}

// AppendPATH appends dir to the process PATH when it is an existing folder
// the PATH lacks, and reports whether it did.
func AppendPATH(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false
	}
	current := os.Getenv("PATH")
	if pathContainsDir(current, dir) {
		return false
	}
	if current != "" {
		current += string(os.PathListSeparator)
	}
	_ = os.Setenv("PATH", current+dir)
	return true
}
