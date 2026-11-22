//go:build darwin

package pii

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"wails-gui/internal/platform/ocr"
)

const (
	piiHelperName       = "yansu-pii-helper"
	piiHelperChildEnv   = "YANSU_PII_HELPER_CHILD"
	piiHelperDisableEnv = "YANSU_PII_HELPER_DISABLE"
	piiHelperPathEnv    = "YANSU_PII_HELPER_PATH"
)

type helperDetector struct {
	path      string
	configDir string

	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	scanner   *bufio.Scanner
	unhealthy bool
}

type helperDetectorRequest struct {
	Observations []ocr.TextObservation `json:"observations"`
}

type helperDetectorResponse struct {
	Ready      bool              `json:"ready,omitempty"`
	Detections []DetectionResult `json:"detections,omitempty"`
	Error      string            `json:"error,omitempty"`
}

func newExternalDetector(configDir string) (Detector, bool, error) {
	if os.Getenv(piiHelperChildEnv) != "" || os.Getenv(piiHelperDisableEnv) != "" {
		return nil, false, nil
	}

	helperPath := piiHelperPath()
	if helperPath == "" {
		return nil, false, nil
	}

	d := &helperDetector{path: helperPath, configDir: configDir}
	if err := d.startLocked(); err != nil {
		return nil, true, err
	}
	log.Printf("[pii] GLiNER helper ready: %s", helperPath)
	return d, true, nil
}

func piiHelperPath() string {
	if override := strings.TrimSpace(os.Getenv(piiHelperPathEnv)); override != "" {
		if piiHelperFileExists(override) {
			return override
		}
	}

	candidates := make([]string, 0, 6)
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "..", "Resources", piiHelperName),
			filepath.Join(dir, piiHelperName),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "build", "bin", piiHelperName),
			filepath.Join(wd, "wails-gui", "build", "bin", piiHelperName),
		)
	}

	for _, candidate := range candidates {
		if piiHelperFileExists(candidate) {
			return candidate
		}
	}
	return ""
}

func piiHelperFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (d *helperDetector) startLocked() error {
	cmd := exec.Command(d.path, "--config-dir", d.configDir)
	cmd.Env = append(os.Environ(), piiHelperChildEnv+"=1")
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("pii helper stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("pii helper stdout: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start pii helper: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	if !scanner.Scan() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("pii helper ready: %w", err)
		}
		return fmt.Errorf("pii helper exited before ready")
	}

	var ready helperDetectorResponse
	if err := json.Unmarshal(scanner.Bytes(), &ready); err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("pii helper ready response: %w", err)
	}
	if ready.Error != "" {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("pii helper init: %s", ready.Error)
	}
	if !ready.Ready {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("pii helper init: not ready")
	}

	d.cmd = cmd
	d.stdin = stdin
	d.scanner = scanner
	return nil
}

func (d *helperDetector) Detect(observations []ocr.TextObservation) []DetectionResult {
	if len(observations) == 0 {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.unhealthy || d.cmd == nil || d.stdin == nil || d.scanner == nil {
		return nil
	}

	req := helperDetectorRequest{Observations: observations}
	if err := json.NewEncoder(d.stdin).Encode(req); err != nil {
		log.Printf("[pii] helper write failed: %v", err)
		d.markUnhealthyLocked()
		return nil
	}

	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			log.Printf("[pii] helper read failed: %v", err)
		} else {
			log.Printf("[pii] helper exited")
		}
		d.markUnhealthyLocked()
		return nil
	}

	var resp helperDetectorResponse
	if err := json.Unmarshal(d.scanner.Bytes(), &resp); err != nil {
		log.Printf("[pii] helper response decode failed: %v", err)
		d.markUnhealthyLocked()
		return nil
	}
	if resp.Error != "" {
		log.Printf("[pii] helper detect failed: %s", resp.Error)
		d.markUnhealthyLocked()
		return nil
	}
	return resp.Detections
}

func (d *helperDetector) IsReady() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.unhealthy && d.cmd != nil
}

func (d *helperDetector) Destroy() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.markUnhealthyLocked()
}

func (d *helperDetector) markUnhealthyLocked() {
	d.unhealthy = true
	if d.stdin != nil {
		_ = d.stdin.Close()
		d.stdin = nil
	}
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
		_ = d.cmd.Wait()
	}
	d.cmd = nil
	d.scanner = nil
}
