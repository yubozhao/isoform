package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"wails-gui/internal/onnxdl"
	"wails-gui/models"
)

const (
	// all-MiniLM-L6-v2: small, fast, high quality sentence embeddings (384 dims).
	localModelName       = "all-MiniLM-L6-v2"
	localModelMaxSeqLen  = 256
	localModelDims       = 384
	localModelHFRepo     = "sentence-transformers/all-MiniLM-L6-v2"
	localModelONNXFile   = "onnx/model.onnx"
	localModelVocabFile  = "vocab.txt"
	modelDownloadTimeout = 10 * time.Minute
)

// modelPaths holds resolved file paths for a local embedding model.
type modelPaths struct {
	ModelFile  string // path to .onnx model
	VocabFile  string // path to vocab.txt
	RuntimeLib string // path to ONNX Runtime shared library
}

// downloadProgressFn is called periodically during downloads with progress info.
// fileName: human-readable name of the file being downloaded.
// downloaded: bytes downloaded so far.
// total: total bytes (-1 if unknown).
// bytesPerSec: current download speed in bytes/sec.
type downloadProgressFn func(fileName string, downloaded, total int64, bytesPerSec float64)

// ensureLocalModel resolves installed assets. Setup owns every model download.
// Returns the paths to all required files.
func ensureLocalModel(configDir string, onProgress downloadProgressFn) (*modelPaths, error) {
	if err := models.Require(configDir, "minilm"); err != nil {
		return nil, err
	}
	modelDir := filepath.Join(configDir, "models", localModelName)
	if err := os.MkdirAll(modelDir, 0755); err != nil {
		return nil, fmt.Errorf("create model dir: %w", err)
	}

	runtimePaths, err := onnxdl.ResolveRuntimePaths(configDir)
	if err != nil {
		return nil, err
	}

	paths := &modelPaths{
		ModelFile:  filepath.Join(modelDir, "model.onnx"),
		VocabFile:  filepath.Join(modelDir, "vocab.txt"),
		RuntimeLib: runtimePaths.RuntimeLib,
	}

	if err := requireBundledRuntime(paths.RuntimeLib); err != nil {
		return nil, err
	}

	return paths, nil
}

// progressReader wraps an io.Reader and reports download progress.
type progressReader struct {
	reader      io.Reader
	fileName    string
	total       int64
	downloaded  int64
	lastReport  time.Time
	startTime   time.Time
	onProgress  downloadProgressFn
	reportIntvl time.Duration
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.downloaded += int64(n)

	now := time.Now()
	if pr.onProgress != nil && now.Sub(pr.lastReport) >= pr.reportIntvl {
		elapsed := now.Sub(pr.startTime).Seconds()
		var speed float64
		if elapsed > 0 {
			speed = float64(pr.downloaded) / elapsed
		}
		pr.onProgress(pr.fileName, pr.downloaded, pr.total, speed)
		pr.lastReport = now
	}

	return n, err
}

// downloadFile downloads a URL to a local file with a temp file for atomicity.
func downloadFile(url, dest, displayName string, onProgress downloadProgressFn) error {
	client := &http.Client{Timeout: modelDownloadTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, url)
	}

	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	reader := io.Reader(resp.Body)
	if onProgress != nil {
		reader = &progressReader{
			reader:      resp.Body,
			fileName:    displayName,
			total:       resp.ContentLength,
			startTime:   time.Now(),
			lastReport:  time.Time{},
			onProgress:  onProgress,
			reportIntvl: 200 * time.Millisecond,
		}
	}

	_, err = io.Copy(f, reader)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}

	// Final progress report (100%)
	if onProgress != nil && resp.ContentLength > 0 {
		onProgress(displayName, resp.ContentLength, resp.ContentLength, 0)
	}

	return os.Rename(tmp, dest)
}

func requireBundledRuntime(path string) error {
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return nil
	}
	return fmt.Errorf("bundled ONNX Runtime is not available")
}
