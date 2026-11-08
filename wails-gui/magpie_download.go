package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Runtime installs use reviewed, platform-specific CLI assets. They are separate
// from speech/privacy model downloads and never start from a status read.
func magpieDownloadEntry(platform string) (ToolDownloadEntry, error) {
	e := ToolDownloadEntry{ID: "model-gateway", DisplayName: "Magpie", Version: "0.1.3315",
		Licence: "MIT", LicenceURL: "https://github.com/yetone/magpie/blob/main/LICENSE"}
	var asset string
	switch platform {
	case "windows/amd64":
		asset, e.SizeBytes, e.SHA256 = "magpie-cli-windows-amd64.exe", 74073600, "fc644325f5b012ae742fd865d56e3ecfc1659393445579c15e97109ae9172cfc"
	case "windows/arm64":
		asset, e.SizeBytes, e.SHA256 = "magpie-cli-windows-arm64.exe", 67719680, "99a41bb1eef3b7f7d1c2246f826fc6cf5a9bde2d7cad2e085843e6cdebafc66f"
	default:
		return ToolDownloadEntry{}, fmt.Errorf("gateway download unavailable for %s", platform)
	}
	e.Source = "https://github.com/yetone/magpie-releases/releases/download/v" + e.Version + "/" + asset
	return e, nil
}

type ToolDownloadProgress struct {
	ID         string `json:"id"`
	BytesDone  int64  `json:"bytesDone"`
	BytesTotal int64  `json:"bytesTotal"`
}

func downloadMagpieTool(plan ToolDownloadPlan, progress func(int64)) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	url := plan.Entry.Source
	// GitHub's release-page download route can fail at regional edges. The
	// asset API serves the same pinned artifact and avoids that redirect cache.
	for _, asset := range []struct{ name, id string }{
		{"magpie-cli-windows-amd64.exe", "723348815"},
		{"magpie-cli-windows-arm64.exe", "723348821"},
	} {
		if url == "https://github.com/yetone/magpie-releases/releases/download/v0.1.3315/"+asset.name {
			url = "https://api.github.com/repos/yetone/magpie-releases/releases/assets/" + asset.id
		}
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway download returned HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(plan.Folder, 0o755); err != nil {
		return err
	}
	stage, err := os.CreateTemp(plan.Folder, ".magpie-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(stage.Name())
	defer stage.Close()
	hash := sha256.New()
	reader := &gatewayProgressReader{reader: io.LimitReader(resp.Body, plan.Entry.SizeBytes+1), progress: progress}
	n, err := io.Copy(io.MultiWriter(stage, hash), reader)
	if err != nil {
		return err
	}
	if n != plan.Entry.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != plan.Entry.SHA256 {
		return fmt.Errorf("gateway download size or SHA-256 does not match confirmed manifest")
	}
	if err := stage.Chmod(0o755); err != nil {
		return err
	}
	if err := stage.Sync(); err != nil {
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	return os.Rename(stage.Name(), filepath.Join(plan.Folder, "magpie-cli.exe"))
}

type gatewayProgressReader struct {
	reader   io.Reader
	progress func(int64)
	done     int64
	last     time.Time
}

func (r *gatewayProgressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.done += int64(n)
	if r.progress != nil && (time.Since(r.last) >= 150*time.Millisecond || err != nil) {
		r.progress(r.done)
		r.last = time.Now()
	}
	return n, err
}

// Reconnect is explicit; a failed download can be retried without changing the
// assistant's sign-in or requiring the person to restart Isoform.
func (a *App) ReconnectModelGateway() error {
	a.syncMagpieSidecar()
	a.RefreshAgentProbe()
	if a.magpie == nil || !a.magpie.Healthy() {
		return fmt.Errorf("model gateway is not running")
	}
	// A runner created while the gateway was missing has no model endpoint.
	// Rebuild it so this connection is usable without restarting the app.
	_, err := a.RestartAgent()
	return err
}
