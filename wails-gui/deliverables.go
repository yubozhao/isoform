package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	agenttools "yansu-agent/agent/tools"
)

// File size limits for deliverable attachments.
// All data is held in-memory across wails-gui -> HTTP upload -> backend -> platform API,
// so conservative limits prevent OOM.
const (
	maxDeliverableFileSize  = 10 << 20 // 10 MB per file
	maxDeliverableTotalSize = 50 << 20 // 50 MB total across all files
)

// deliverableFile represents a validated, read file ready for upload.
type deliverableFile struct {
	Filename    string
	ContentType string
	Data        []byte
}

// validateDeliverablePath checks that a file path is contained within one of
// the allowed directories: the job's workDir, sessionTempDir, or the user's
// home directory. Uses filepath.EvalSymlinks to resolve symlinks and
// filepath.Rel to verify containment, preventing both prefix-bypass and
// symlink-escape attacks.
func validateDeliverablePath(path, workDir, sessionTempDir string) bool {
	resolved, err := resolvedExistingPath(path)
	if err != nil {
		return false
	}

	allowedDirs := []string{workDir, sessionTempDir}
	if home, err := os.UserHomeDir(); err == nil {
		allowedDirs = append(allowedDirs, home)
	}

	for _, dir := range allowedDirs {
		if dir == "" {
			continue
		}
		resolvedDir, err := resolvedExistingPath(dir)
		if err != nil || resolvedDir == "" {
			continue
		}
		if pathWithinFolded(resolved, resolvedDir) {
			return true
		}
	}
	return false
}

// validateUploadDeliverablePath is the strict check for files that will be
// uploaded to the backend and posted to a channel after an unattended run.
// Unlike validateDeliverablePath (used when the user themselves clicks a file
// open in the UI), it does NOT allow the whole home directory or the work
// folder. A file may be uploaded only when the run itself read or wrote it
// through its Read/Write/Edit/NotebookEdit tools (writtenFiles, canonical
// paths), even when the file sits inside the work folder. This stops a run
// from naming a sensitive file such as ~/.ssh/id_ed25519, or a stale file it
// never touched, and shipping it off the machine.
func validateUploadDeliverablePath(path string, writtenFiles map[string]struct{}) bool {
	resolved, err := resolvedExistingPath(path)
	if err != nil {
		return false
	}
	for written := range writtenFiles {
		if sameResolvedPath(resolved, written) {
			return true
		}
	}
	return false
}

// readDeliverableFiles reads validated files from disk and returns them
// along with skip reasons for invalid, missing, or oversized files. Only
// files the run read or wrote (writtenFiles) are eligible: see
// validateUploadDeliverablePath.
func readDeliverableFiles(paths []string, workDir, sessionTempDir string, writtenFiles map[string]struct{}) ([]deliverableFile, []string) {
	var files []deliverableFile
	var skipped []string
	var totalSize int64

	for _, p := range paths {
		// Resolve relative paths against workDir.
		absPath := p
		if !filepath.IsAbs(p) && workDir != "" {
			absPath = filepath.Join(workDir, p)
		}

		if !validateUploadDeliverablePath(absPath, writtenFiles) {
			skipped = append(skipped, fmt.Sprintf("%s -- not read or written by this run", filepath.Base(p)))
			continue
		}
		resolved, err := resolvedExistingPath(absPath)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s -- file not found", filepath.Base(p)))
			continue
		}
		if reason := privateUploadReason(resolved, workDir, sessionTempDir); reason != "" {
			skipped = append(skipped, fmt.Sprintf("%s -- %s", filepath.Base(p), reason))
			continue
		}

		// Open the checked path itself, never following a link put there
		// since the check.
		f, info, reason := openUploadDeliverable(resolved)
		if reason != "" {
			skipped = append(skipped, fmt.Sprintf("%s -- %s", filepath.Base(p), reason))
			continue
		}

		if info.Size() > maxDeliverableFileSize {
			_ = f.Close()
			skipped = append(skipped, fmt.Sprintf("%s (%.1f MB) -- exceeds %d MB limit",
				filepath.Base(p), float64(info.Size())/(1<<20), maxDeliverableFileSize/(1<<20)))
			continue
		}

		if totalSize+info.Size() > maxDeliverableTotalSize {
			_ = f.Close()
			skipped = append(skipped, fmt.Sprintf("%s -- total size limit reached", filepath.Base(p)))
			continue
		}

		data, err := io.ReadAll(io.LimitReader(f, maxDeliverableFileSize+1))
		_ = f.Close()
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s -- read error: %v", filepath.Base(p), err))
			continue
		}
		if int64(len(data)) > maxDeliverableFileSize {
			skipped = append(skipped, fmt.Sprintf("%s -- exceeds %d MB limit", filepath.Base(p), maxDeliverableFileSize/(1<<20)))
			continue
		}

		totalSize += int64(len(data))
		files = append(files, deliverableFile{
			Filename:    filepath.Base(p),
			ContentType: detectContentType(filepath.Base(p)),
			Data:        data,
		})
	}

	return files, skipped
}

// openUploadDeliverable opens a deliverable for upload and checks the open
// file itself, so the checks cannot race a rename. It opens without blocking
// (a FIFO would otherwise hang the notify goroutine), requires a regular file,
// and refuses a file with more than one hard link, which could be another name
// for a file outside the work folder. reason is non-empty when it is skipped.
func openUploadDeliverable(path string) (*os.File, os.FileInfo, string) {
	checkedInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Sprintf("read error: %v", err)
	}
	f, err := openUploadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, "file not found"
		}
		return nil, nil, fmt.Sprintf("read error: %v", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, fmt.Sprintf("read error: %v", err)
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, "not a regular file"
	}
	if fileLinkCount(info) > 1 {
		_ = f.Close()
		return nil, nil, "has other hard links"
	}
	// Recheck the resolved path and descriptor identity before reading bytes.
	// On Unix openUploadFile also refuses links in every directory component;
	// this check covers file replacements and the Windows fallback.
	resolved, err := resolvedExistingPath(path)
	if err != nil || !sameResolvedPath(resolved, path) || !os.SameFile(checkedInfo, info) {
		_ = f.Close()
		return nil, nil, "file changed while preparing upload"
	}
	currentInfo, err := os.Stat(resolved)
	if err != nil || !os.SameFile(currentInfo, info) {
		_ = f.Close()
		return nil, nil, "file changed while preparing upload"
	}
	return f, info, ""
}

// privateUploadReason returns why a file may not leave the machine: it is
// private app state or a private folder such as ~/.ssh or ~/Library.
// Ordinary work and scratch reports may be uploaded; their location never
// exempts secret files from the shared tool policy. It returns "" otherwise.
func privateUploadReason(resolved, workDir, sessionTempDir string) string {
	if home, err := os.UserHomeDir(); err != nil || home == "" {
		return "the home folder is unknown"
	}
	resolved = resolvedFolder(resolved)
	input := map[string]any{"path": resolved}
	if agenttools.CheckProtectedPaths("Read", input, sessionTempDir) != nil &&
		agenttools.CheckProtectedPaths("Read", input, workDir) != nil {
		return "inside Isoform's own data folder"
	}
	for _, dir := range privateHomeFolders() {
		if pathWithinFolded(resolved, dir) {
			return "inside a private folder (" + filepath.Base(dir) + ")"
		}
	}
	return ""
}

// detectContentType returns a MIME type based on file extension.
func detectContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".zip":
		return "application/zip"
	case ".md", ".txt":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

// apiPostMultipart sends a multipart/form-data POST request with text fields and file attachments.
func apiPostMultipart(baseURL, token, path string, fields map[string]string, files []deliverableFile, result any) error {
	return errNoYansuAccount
}

// buildSkipNote creates a text note for skipped files to append to the DM.
func buildSkipNote(skipped []string) string {
	if len(skipped) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n---\nNote: Some files could not be attached:\n")
	for _, s := range skipped {
		sb.WriteString("- ")
		sb.WriteString(s)
		sb.WriteString("\n")
	}
	return sb.String()
}
