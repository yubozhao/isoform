package pii

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"wails-gui/internal/onnxdl"
	"wails-gui/models"
)

// ensurePIIModel is called from multiple subsystems and can also be invoked
// across two overlapping app instances during Wails hot-reload. Both code
// paths previously raced on the fixed `model.onnx.tmp` filename: each writer
// truncated the file the others were still streaming into, and on Windows the
// loser of the rename hit ERROR_SHARING_VIOLATION because the winner still
// held the tmp's handle. We saw 11x bandwidth waste and ~6 min of disabled
// PII masking. The mutex serialises within-process callers; per-call unique
// tmp filenames (see downloadFile) keep cross-process races correct.
var (
	ensurePIIModelMu    sync.Mutex
	ensurePIIModelCache = map[string]*piiModelPaths{}
)

const (
	piiModelName          = "gliner-pii-base"
	piiModelHFRepo        = "knowledgator/gliner-pii-base-v1.0"
	piiModelMirrorBaseURL = "https://model-assets.yansu.app/gliner-pii/base-v1.0"
	envPIIModelURL        = "YANSU_PII_MODEL_URL"
	envPIITokenizerURL    = "YANSU_PII_TOKENIZER_URL"
	downloadTimeout       = 30 * time.Minute

	// SHA-256 of the canonical fp32 GLiNER PII assets. These hashes were
	// captured from the validated local model files uploaded to R2 on
	// 2026-05-21.
	sha256PIIModel     = "c6ccec44625d46bfe3191152e41d6564b69bc9d4313b7f3e419e8372679e9fed"
	sha256PIITokenizer = "ee028763434d18611c1c36356ea1d050e90a9fa94ede57fac48b39f85f818ad1" // gitleaks:allow
)

// piiModelFile returns the GLiNER ONNX export to ship.
//
// The fp16 export is smaller (~333 MB) but has failed session creation across
// ORT builds with a Cast output type mismatch. The fp32 export (~665 MB) has no
// Cast-type ambiguity and loads consistently. The quint8 export uses dynamic-
// quantization ops whose type-inference path can crash ORT after failures, so
// we never select it.
func piiModelFile() string {
	return "model.onnx"
}

// staleModelFiles lists earlier model filenames that should be removed from
// disk on startup so users don't carry around hundreds of MB of dead weight
// after a model upgrade or platform-variant switch.
func staleModelFiles() []string {
	return []string{"model_fp16.onnx", "model_quint8.onnx"}
}

// piiModelPaths holds resolved file paths for the PII detection model.
type piiModelPaths struct {
	ModelFile     string
	TokenizerFile string
	RuntimeLib    string
}

// ProgressFn reports download progress.
type ProgressFn func(fileName string, downloaded, total int64, bytesPerSec float64)

// ensurePIIModel resolves the installed GLiNER PII model and ONNX Runtime shared
// library if not present. The runtime download is self-contained so PII masking
// works independently of the memory store. Within a process all callers are
// serialised on a mutex so a concurrent NewDetector call doesn't kick off a
// second 665 MB download.
func ensurePIIModel(configDir string, onProgress ProgressFn) (*piiModelPaths, error) {
	ensurePIIModelMu.Lock()
	defer ensurePIIModelMu.Unlock()

	if err := models.Require(configDir, "gliner-pii"); err != nil {
		delete(ensurePIIModelCache, configDir)
		return nil, err
	}
	if paths, ok := ensurePIIModelCache[configDir]; ok {
		return paths, nil
	}

	paths, err := doEnsurePIIModel(configDir, onProgress)
	if err == nil {
		ensurePIIModelCache[configDir] = paths
	}
	return paths, err
}

func doEnsurePIIModel(configDir string, onProgress ProgressFn) (*piiModelPaths, error) {
	if err := models.Require(configDir, "gliner-pii"); err != nil {
		return nil, err
	}
	modelDir := filepath.Join(configDir, "models", piiModelName)
	if err := os.MkdirAll(modelDir, 0755); err != nil {
		return nil, fmt.Errorf("create model dir: %w", err)
	}

	runtimePaths, err := onnxdl.ResolveRuntimePaths(configDir)
	if err != nil {
		return nil, err
	}

	modelFile := piiModelFile()
	paths := &piiModelPaths{
		ModelFile:     filepath.Join(modelDir, modelFile),
		TokenizerFile: filepath.Join(modelDir, "tokenizer.json"),
		RuntimeLib:    runtimePaths.RuntimeLib,
	}

	if err := requireBundledRuntime(paths.RuntimeLib); err != nil {
		return nil, err
	}

	return paths, nil
}

func piiAssetURLs(file string) []string {
	return []string{
		piiMirrorURL(file),
		hfRawURL(piiModelHFRepo, file),
	}
}

func piiMirrorURL(file string) string {
	return fmt.Sprintf("%s/%s", piiModelMirrorBaseURL, file)
}

func hfRawURL(repo, file string) string {
	return fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", repo, file)
}

// urlsForPIIAsset returns the download URL list for a PII asset and whether
// the user supplied an override. The override is a single URL by design so a
// local test mirror is not silently bypassed by fallback URLs.
func urlsForPIIAsset(envKey string, urls []string) ([]string, bool) {
	if v := os.Getenv(envKey); v != "" {
		return []string{v}, true
	}
	return urls, false
}

func sha256OrEmpty(hashHex string, isOverride bool) string {
	if isOverride {
		return ""
	}
	return hashHex
}

func verifyExistingFile(path, displayName, expectedSHA256 string) error {
	if expectedSHA256 == "" || !fileExists(path) {
		return nil
	}
	got, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf("verify existing %s: %w", displayName, err)
	}
	if got == expectedSHA256 {
		return nil
	}
	log.Printf("[pii] existing %s checksum mismatch at %s (got sha256=%s want sha256=%s); removing and redownloading",
		displayName, path, got, expectedSHA256)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove invalid %s: %w", displayName, err)
	}
	return nil
}

func downloadFile(url, dest, displayName, expectedSHA256 string, onProgress ProgressFn) error {
	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, url)
	}

	// Per-call unique tmp filename. With a fixed `.tmp` suffix, concurrent
	// downloaders (across processes during hot-reload) would clobber each
	// other's bytes via os.Create and then collide on Windows rename with
	// ERROR_SHARING_VIOLATION. CreateTemp gives each writer its own slot;
	// the rename-already-exists path below silently no-ops the losers.
	dir, base := filepath.Split(dest)
	f, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return err
	}
	tmp := f.Name()

	reader := io.Reader(resp.Body)
	if onProgress != nil {
		reader = &progressReader{
			reader:      resp.Body,
			fileName:    displayName,
			total:       resp.ContentLength,
			startTime:   time.Now(),
			onProgress:  onProgress,
			reportIntvl: 200 * time.Millisecond,
		}
	}

	writer := io.Writer(f)
	hasher := sha256.New()
	if expectedSHA256 != "" {
		writer = io.MultiWriter(f, hasher)
	}

	_, err = io.Copy(writer, reader)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}

	if expectedSHA256 != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if got != expectedSHA256 {
			os.Remove(tmp)
			return fmt.Errorf("checksum mismatch: got sha256=%s want sha256=%s", got, expectedSHA256)
		}
	}

	if onProgress != nil && resp.ContentLength > 0 {
		onProgress(displayName, resp.ContentLength, resp.ContentLength, 0)
	}

	if err := os.Rename(tmp, dest); err != nil {
		// Another goroutine may have completed the download first.
		os.Remove(tmp)
		if fileExists(dest) {
			if verifyErr := verifyExistingFile(dest, displayName, expectedSHA256); verifyErr != nil {
				return verifyErr
			}
			if !fileExists(dest) {
				return fmt.Errorf("existing %s failed checksum during download race", displayName)
			}
			return nil
		}
		return err
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type progressReader struct {
	reader      io.Reader
	fileName    string
	total       int64
	downloaded  int64
	lastReport  time.Time
	startTime   time.Time
	onProgress  ProgressFn
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

func fileExists(path string) bool {
	return onnxdl.FileExists(path)
}

func requireBundledRuntime(path string) error {
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return nil
	}
	return fmt.Errorf("bundled ONNX Runtime is not available")
}
