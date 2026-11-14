//go:build !bindings && ((darwin && (amd64 || arm64) && !ios) || (windows && (amd64 || 386)))

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeQwen3ModelFiles(t *testing.T, p qwen3ASRPaths) {
	t.Helper()
	for _, f := range []string{p.convFrontend, p.encoder, p.decoder, filepath.Join(p.tokenizer, "vocab.json"), filepath.Join(p.tokenizer, "merges.txt")} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestQwen3ASRModelPresentNeedsAVerifiedInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths := defaultQwen3ASRPaths()
	if qwen3ASRModelPresent(paths) {
		t.Fatal("an empty folder has no model")
	}
	// Every file there, as a full disk can leave them: not a model until
	// the install marked it verified.
	writeQwen3ModelFiles(t, paths)
	if qwen3ASRModelPresent(paths) {
		t.Fatal("files without the verified marker are not a model")
	}
	if err := os.WriteFile(filepath.Join(paths.modelDir, qwen3ASRVerifiedMarker), []byte(sha256Qwen3ASRArchive+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if qwen3ASRModelPresent(paths) {
		t.Fatal("a legacy marker cannot hide corrupt required weights")
	}
	if _, err := newQwen3ASRTranscriber(paths, filepath.Join(t.TempDir(), "missing-silero.onnx")); err == nil {
		t.Fatal("no silero model must fail before loading anything")
	}
}

// qwen3Archive builds a tar.bz2 shaped like the release with the system tar.
func qwen3Archive(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("needs tar")
	}
	src := t.TempDir()
	writeQwen3ModelFiles(t, qwen3ASRPathsUnder(src))
	out := filepath.Join(t.TempDir(), "model.tar.bz2")
	if raw, err := exec.Command("tar", "-cjf", out, "-C", src, qwen3ASRDirName).CombinedOutput(); err != nil {
		t.Fatalf("tar: %v %s", err, raw)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func serveBytes(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "model.tar.bz2", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// assumeQwen3DiskRoom: these tests install a few-kilobyte fake archive, so the
// test machine's real free space (checked against the 2.8 GB production model)
// must not decide them. The low-disk branch is
// TestEnsureQwen3ASRAssetsSkipsWhenDiskIsShort's.
func assumeQwen3DiskRoom(t *testing.T) {
	t.Helper()
	old := freeDiskSpace
	freeDiskSpace = func(string) (uint64, error) { return 1 << 40, nil }
	t.Cleanup(func() { freeDiskSpace = old })
}

func TestEnsureQwen3ASRAssetsInstallsAVerifiedModel(t *testing.T) {
	assumeQwen3DiskRoom(t)
	archive := qwen3Archive(t)
	srv := serveBytes(t, archive)
	paths := qwen3ASRPathsUnder(filepath.Join(t.TempDir(), "qwen3-asr"))
	if err := ensureQwen3ASRAssetsFrom(context.Background(), paths, []string{srv.URL}, sha256Hex(archive), nil); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(paths.modelDir, qwen3ASRVerifiedMarker))
	if err != nil || strings.TrimSpace(string(marker)) != sha256Hex(archive) || !qwen3ASRFilesPresent(paths) {
		t.Fatalf("installed model: marker=%q err=%v", marker, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(paths.root, ".extract-*"))
	archives, _ := filepath.Glob(filepath.Join(paths.root, "*.tar.bz2*"))
	if len(leftovers)+len(archives) != 0 {
		t.Fatalf("install left %v %v", leftovers, archives)
	}
}

func TestEnsureQwen3ASRAssetsRejectsAWrongChecksum(t *testing.T) {
	assumeQwen3DiskRoom(t)
	archive := qwen3Archive(t)
	srv := serveBytes(t, archive)
	paths := qwen3ASRPathsUnder(filepath.Join(t.TempDir(), "qwen3-asr"))
	err := ensureQwen3ASRAssetsFrom(context.Background(), paths, []string{srv.URL}, strings.Repeat("0", 64), nil)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("a wrong checksum must fail: %v", err)
	}
	if _, err := os.Stat(paths.modelDir); !os.IsNotExist(err) {
		t.Fatal("nothing may be installed")
	}
	if files, _ := filepath.Glob(filepath.Join(paths.root, "*")); len(files) != 0 {
		t.Fatalf("a rejected download leaves nothing: %v", files)
	}
}

func TestEnsureQwen3ASRAssetsFailedExtractionInstallsNothing(t *testing.T) {
	assumeQwen3DiskRoom(t)
	// Verified bytes that are not a bzip2 archive: the extraction fails, as
	// a full disk would make it.
	broken := []byte("this is not a bzip2 archive, but its checksum matches")
	srv := serveBytes(t, broken)
	paths := qwen3ASRPathsUnder(filepath.Join(t.TempDir(), "qwen3-asr"))
	writeQwen3ModelFiles(t, paths) // a half-written earlier attempt
	err := ensureQwen3ASRAssetsFrom(context.Background(), paths, []string{srv.URL}, sha256Hex(broken), nil)
	if err == nil || !strings.Contains(err.Error(), "extract") {
		t.Fatalf("the extraction must fail: %v", err)
	}
	if qwen3ASRModelPresent(paths) {
		t.Fatal("a failed extraction is never a model")
	}
	leftovers, _ := filepath.Glob(filepath.Join(paths.root, ".extract-*"))
	archives, _ := filepath.Glob(filepath.Join(paths.root, "*.tar.bz2"))
	if len(leftovers)+len(archives) != 0 {
		t.Fatalf("a failed extraction left %v %v", leftovers, archives)
	}
}

func TestEnsureQwen3ASRAssetsSkipsWhenDiskIsShort(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer srv.Close()
	old := freeDiskSpace
	freeDiskSpace = func(string) (uint64, error) { return 500_000_000, nil }
	defer func() { freeDiskSpace = old }()
	paths := qwen3ASRPathsUnder(filepath.Join(t.TempDir(), "qwen3-asr"))
	err := ensureQwen3ASRAssetsFrom(context.Background(), paths, []string{srv.URL}, strings.Repeat("0", 64), nil)
	if !errors.Is(err, errQwen3ASRLowDisk) || requests.Load() != 0 {
		t.Fatalf("low disk must skip the download: %v (%d requests)", err, requests.Load())
	}
}

func TestDownloadSherpaAssetResumesAPartialFile(t *testing.T) {
	data := bytes.Repeat([]byte("speech-model-bytes "), 4096)
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "model.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(dest+".partial", data[:len(data)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+".partial"+sherpaResumeValidatorSuffix, []byte(`"v1"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := downloadSherpaAsset(context.Background(), "model", srv.URL, dest, sha256Hex(data), nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("resumed file differs: %v", err)
	}
	if len(ranges) != 1 || ranges[0] != "bytes="+strconv.Itoa(len(data)/2)+"-" {
		t.Fatalf("expected one ranged request, got %q", ranges)
	}
}

// partialDownload leaves half of old as a partial download of dest, with
// the validator it was fetched under.
func partialDownload(t *testing.T, dest string, old []byte, validator string) {
	t.Helper()
	if err := os.WriteFile(dest+".partial", old[:len(old)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+".partial"+sherpaResumeValidatorSuffix, []byte(validator), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadSherpaAssetStartsOverWhenTheFileChanged(t *testing.T) {
	old := bytes.Repeat([]byte("old-model-bytes "), 4096)
	data := bytes.Repeat([]byte("new-model-bytes "), 4096)
	var ranges, ifRanges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		ifRanges = append(ifRanges, r.Header.Get("If-Range"))
		w.Header().Set("ETag", `"v2"`)
		http.ServeContent(w, r, "model.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.bin")
	partialDownload(t, dest, old, `"v1"`)
	if err := downloadSherpaAsset(context.Background(), "model", srv.URL, dest, sha256Hex(data), nil); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(dest); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the replaced file must come whole, never spliced onto the old part: %v", err)
	}
	if len(ifRanges) != 1 || ifRanges[0] != `"v1"` {
		t.Fatalf("the resume must say which file it continues: %q", ifRanges)
	}
	if _, err := os.Stat(dest + ".partial" + sherpaResumeValidatorSuffix); !os.IsNotExist(err) {
		t.Fatal("the validator goes with the finished download")
	}
}

func TestDownloadSherpaAssetWithoutAChecksumStartsOver(t *testing.T) {
	data := bytes.Repeat([]byte("model-bytes "), 4096)
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "model.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "model.bin")
	partialDownload(t, dest, data, `"v1"`)
	if err := downloadSherpaAsset(context.Background(), "model", srv.URL, dest, "", nil); err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 1 || ranges[0] != "" {
		t.Fatalf("with nothing to check the result against, no resume: %q", ranges)
	}
	if got, err := os.ReadFile(dest); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("downloaded file differs: %v", err)
	}
}

// The former automatic download lifecycle is now covered by models.Manager.
// A feature attempt must remain not ready and leave the configured URL alone.
func TestQwen3ASRFeatureDoesNotDownloadWithoutConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	t.Setenv(envQwen3ASRURL, server.URL)
	if err := ensureQwen3ASRAssets(context.Background(), qwen3ASRPathsUnder(t.TempDir()), nil); err == nil {
		t.Fatal("unconfirmed model reported ready")
	}
	if requests.Load() != 0 {
		t.Fatal("feature downloaded without confirmation")
	}
}

func TestDownloadSherpaAssetStopsWhenTheServerGoesQuiet(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write(bytes.Repeat([]byte("a"), 100))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)
	old := sherpaDownloadIdleTimeout
	sherpaDownloadIdleTimeout = 200 * time.Millisecond
	defer func() { sherpaDownloadIdleTimeout = old }()

	dest := filepath.Join(t.TempDir(), "model.bin")
	started := time.Now()
	if err := downloadSherpaAsset(context.Background(), "model", srv.URL, dest, "", nil); err == nil {
		t.Fatal("a stalled download must fail")
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("the idle timeout must end a stalled download")
	}
	if st, err := os.Stat(dest + ".partial"); err != nil || st.Size() != 100 {
		t.Fatalf("the bytes received stay to resume: %v %v", st, err)
	}
}

func TestPackSpeechSpansCutsInPauses(t *testing.T) {
	rate := sherpaSampleRate
	samples := make([]float32, 40*rate)
	for i := range samples {
		samples[i] = 0.3
	}
	// Speech with pauses at 9-10 s, 17-17.5 s and 31-32 s; pieces of at
	// most 18 s.
	spans := [][2]int{{0, 9 * rate}, {10 * rate, 17 * rate}, {17*rate + rate/2, 31 * rate}, {32 * rate, 40 * rate}}
	pieces := packSpeechSpans(samples, spans, 18*rate, rate, 19*rate)
	want := [][2]int{{0, 17*rate + rate/4}, {17*rate + rate/4, 31*rate + rate/2}, {31*rate + rate/2, 40 * rate}}
	if len(pieces) != len(want) {
		t.Fatalf("pieces = %v, want %v", pieces, want)
	}
	for i := range want {
		if pieces[i] != want[i] {
			t.Fatalf("piece %d = %v, want %v (cut in the middle of a pause)", i, pieces[i], want[i])
		}
	}

	// A pause just before the end: the short tail joins the piece before it.
	short := samples[:int(18.5*float64(rate))]
	tail := packSpeechSpans(short, [][2]int{{0, 17*rate + 8*rate/10}, {17*rate + 9*rate/10, len(short)}}, 18*rate, rate, 19*rate)
	if len(tail) != 1 || tail[0] != [2]int{0, len(short)} {
		t.Fatalf("a 0.65 s tail must join the piece before it: %v", tail)
	}

	// No pause at all: cut at the quietest point, every piece in the limit.
	for _, p := range packSpeechSpans(samples[:30*rate], nil, 18*rate, rate, 19*rate) {
		if p[1]-p[0] > 18*rate || p[1] <= p[0] {
			t.Fatalf("piece %v outside the limit", p)
		}
	}
}

func TestFittingPadKeepsSegmentsInsideTheModelWindow(t *testing.T) {
	tr := &sherpaTranscriber{maxDecodeSamples: 18 * sherpaSampleRate}
	if got := tr.fittingPad(750*time.Millisecond, 17*sherpaSampleRate); got != 500*time.Millisecond {
		t.Fatalf("pad for 17 s = %s, want 500ms", got)
	}
	if got := tr.fittingPad(750*time.Millisecond, 18*sherpaSampleRate); got != 0 {
		t.Fatalf("a full window takes no pad, got %s", got)
	}
	if got := (&sherpaTranscriber{}).fittingPad(750*time.Millisecond, 30*sherpaSampleRate); got != 750*time.Millisecond {
		t.Fatalf("no window, no cap: %s", got)
	}
	opts := sherpaTranscriptionOptions{speechPad: time.Second}
	if !shouldReDecodeSherpaSegmentWithPadding("Chinese", opts) || !shouldReDecodeSherpaSegmentWithPadding("<|zh|>", opts) || shouldReDecodeSherpaSegmentWithPadding("English", opts) {
		t.Fatal("the Chinese re-decode must match SenseVoice tags and Qwen3 names")
	}
}

func TestAfterCallSpeechModelSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}
	view := app.GetAfterCallSpeechModel()
	if !view.Supported || !view.Enabled || view.Status != "absent" || view.DownloadBytes <= 0 || view.DiskBytes <= 0 {
		t.Fatalf("fresh view = %+v", view)
	}
	paths := defaultQwen3ASRPaths()
	writeQwen3ModelFiles(t, paths)
	if err := os.WriteFile(filepath.Join(paths.modelDir, qwen3ASRVerifiedMarker), []byte(sha256Qwen3ASRArchive), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := app.GetAfterCallSpeechModel().Status; got != "absent" {
		t.Fatalf("corrupt required weights must remain unavailable: %q", got)
	}
	if err := app.RemoveAfterCallSpeechModel(); err != nil {
		t.Fatal(err)
	}
	view = app.GetAfterCallSpeechModel()
	if view.Status != "absent" || view.Enabled {
		t.Fatalf("remove deletes the model and turns it off: %+v", view)
	}
	if _, err := os.Stat(paths.modelDir); !os.IsNotExist(err) {
		t.Fatal("the model folder must be gone")
	}
	if err := app.SetAfterCallSpeechModelEnabled(true); err != nil || !app.GetAfterCallSpeechModel().Enabled {
		t.Fatalf("turn on again: %v", err)
	}
}
