package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// ToolDownloadEntry is the shipped manifest metadata for the confirmation
// sheet. Bundled Bun, FFmpeg and OpenCLI never need runtime download entries.
type ToolDownloadEntry struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Version     string `json:"version"`
	SizeBytes   int64  `json:"sizeBytes"`
	Source      string `json:"source"`
	Licence     string `json:"licence"`
	LicenceURL  string `json:"licenceUrl"`
	SHA256      string `json:"sha256"`
}

type ToolDownloadPlan struct {
	Entry     ToolDownloadEntry `json:"entry"`
	Folder    string            `json:"folder"`
	Token     string            `json:"token"`
	ExpiresAt int64             `json:"expiresAt"`
}

type toolDownloadPlans struct {
	mu      sync.Mutex
	pending map[string]ToolDownloadPlan
}

func gitDownloadEntry(platform string) (ToolDownloadEntry, error) {
	e := ToolDownloadEntry{ID: "windows-git", DisplayName: "Git for Windows (MinGit)", Version: minGitVersion,
		Licence: "GPL-2.0", LicenceURL: "https://github.com/git-for-windows/git/blob/v" + minGitRelease + "/COPYING"}
	suffix := ""
	switch platform {
	case "windows/amd64":
		suffix, e.SizeBytes, e.SHA256 = "64-bit", 47241394, "50b04b55425b5c465d076cdb184f63a0cd0f86f6ec8bb4d5860114a713d2c29a"
	case "windows/arm64":
		suffix, e.SizeBytes, e.SHA256 = "arm64", 46105833, "fc5747e187a70147404a94da104dc9f6005a3d45a78a56dbfa132075ad4a45e4"
	default:
		return ToolDownloadEntry{}, fmt.Errorf("runtime Git download unavailable for %s", platform)
	}
	e.Source = fmt.Sprintf("https://github.com/git-for-windows/git/releases/download/v%s/MinGit-%s-%s.zip", minGitRelease, minGitVersion, suffix)
	return e, nil
}

// PlanToolDownload performs no I/O beyond local path resolution. Only the
// shipped manifest can be planned; caller-supplied URLs are never accepted.
func (a *App) PlanToolDownload(id string) (ToolDownloadPlan, error) {
	if id != "windows-git" {
		return ToolDownloadPlan{}, fmt.Errorf("unknown tool %q", id)
	}
	entry, err := gitDownloadEntry(runtime.GOOS + "/" + runtime.GOARCH)
	if err != nil {
		return ToolDownloadPlan{}, err
	}
	dir, err := flavorDataDir()
	if err != nil {
		return ToolDownloadPlan{}, err
	}
	return a.toolDownloads.plan(entry, filepath.Join(dir, gitInstallRoot))
}

func (p *toolDownloadPlans) plan(entry ToolDownloadEntry, folder string) (ToolDownloadPlan, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ToolDownloadPlan{}, err
	}
	plan := ToolDownloadPlan{Entry: entry, Folder: folder, Token: hex.EncodeToString(nonce[:]), ExpiresAt: time.Now().Add(15 * time.Minute).UnixMilli()}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil {
		p.pending = make(map[string]ToolDownloadPlan)
	}
	for token, pending := range p.pending {
		if pending.ExpiresAt < time.Now().UnixMilli() {
			delete(p.pending, token)
		}
	}
	p.pending[plan.Token] = plan
	return plan, nil
}

// StartToolDownload is the explicit confirmation action. Plans are bound to
// every displayed field, this App instance, a short lifetime and one use.
// Startup must never call it. Until the shared model sheet supports tools,
// missing Windows Git stays not ready rather than bypassing that sheet.
func (a *App) StartToolDownload(plan ToolDownloadPlan) error {
	return a.toolDownloads.startGit(plan)
}

func (p *toolDownloadPlans) startGit(plan ToolDownloadPlan) error {
	p.mu.Lock()
	expected, ok := p.pending[plan.Token]
	if !ok || expected != plan || plan.ExpiresAt < time.Now().UnixMilli() {
		p.mu.Unlock()
		return fmt.Errorf("tool download requires confirmation of a current, unchanged plan")
	}
	delete(p.pending, plan.Token)
	p.mu.Unlock()

	// No network request or directory creation occurs above this boundary.
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(plan.Entry.Source)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Git download returned HTTP %d", resp.StatusCode)
	}
	archive, err := os.CreateTemp("", "confirmed-git-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(resp.Body, plan.Entry.SizeBytes+1))
	if err != nil {
		return err
	}
	if size != plan.Entry.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != plan.Entry.SHA256 {
		return fmt.Errorf("Git download size or SHA-256 does not match confirmed manifest")
	}
	parent := filepath.Dir(plan.Folder)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, "git-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := extractZipFromReader(archive, size, staging); err != nil {
		return err
	}
	bin := filepath.Join(staging, "bin", "git.exe")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	if err := copyFileExecutable(filepath.Join(staging, "cmd", "git.exe"), bin); err != nil {
		return err
	}
	if err := os.RemoveAll(plan.Folder); err != nil {
		return err
	}
	if err := os.Rename(staging, plan.Folder); err != nil {
		return err
	}
	installed := filepath.Join(plan.Folder, "bin", "git.exe")
	setResolvedGitPath(installed)
	ensurePathDirInEnv(installed)
	return nil
}
