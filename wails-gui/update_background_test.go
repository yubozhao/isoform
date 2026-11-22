package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpdateScheduleLaunchRepeatAndCancel(t *testing.T) {
	if updateLaunchDelay != 3*time.Second || updateCheckInterval != 2*time.Hour {
		t.Fatal("unexpected update schedule")
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	runUpdateSchedule(ctx, 0, 0, func() {
		calls++
		if calls == 3 {
			cancel()
		}
	})
	if calls != 3 {
		t.Fatalf("checks = %d", calls)
	}
	runUpdateSchedule(ctx, 0, 0, func() { t.Fatal("checked after cancellation") })
}

func TestUpdateBackgroundDownloadPolicyAndNextLaunch(t *testing.T) {
	setReleaseTestBuild(t, appFlavorIsoform, buildChannelRelease)
	root := t.TempDir()
	t.Setenv(agentHomeEnv, root)
	payload := []byte("verified update bytes")
	checksum := fmt.Sprintf("%x", sha256.Sum256(payload))
	downloads := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			downloads++
			_, _ = w.Write(payload)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.1.303", "updater_assets": map[string]string{runtime.GOOS + "-" + displayArch(): server.URL + "/asset"}, "updater_checksums": map[string]string{runtime.GOOS + "-" + displayArch(): checksum}})
	}))
	defer server.Close()
	R2ManifestURL = server.URL
	app := &App{updater: &updater{}}
	if !backgroundUpdatesEnabled() {
		t.Fatal("background updates must default on")
	}
	if err := app.SetBackgroundUpdates(false); err != nil {
		t.Fatal(err)
	}
	app.autoCheckForUpdates()
	waitBackgroundDownload(t, app)
	if downloads != 0 || app.GetUpdateStatus().Status != "update-available" {
		t.Fatal("off switch did not hold")
	}
	if err := app.SetBackgroundUpdates(true); err != nil {
		t.Fatal(err)
	}
	waitBackgroundDownload(t, app)
	if downloads != 1 || app.GetUpdateStatus().Status != "update-downloaded" {
		t.Fatal("did not download in background")
	}
	app.autoCheckForUpdates()
	waitBackgroundDownload(t, app)
	if downloads != 1 {
		t.Fatal("staged update downloaded twice")
	}
	app.DismissUpdateUntilNextLaunch()
	if !app.updater.later {
		t.Fatal("Later not recorded")
	}
	next := &App{updater: &updater{}}
	if !next.restorePendingUpdate() || next.updater.later {
		t.Fatal("restart lost install flag or kept Later")
	}
	if next.updater.downloadedVersion != "0.1.303" {
		t.Fatal("wrong downloaded version")
	}
	if err := os.WriteFile(next.updater.stagingPath, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if (&App{updater: &updater{}}).restorePendingUpdate() {
		t.Fatal("restored tampered archive")
	}
}

func TestUpdateDevelopmentCannotDownloadOrRestore(t *testing.T) {
	setReleaseTestBuild(t, appFlavorIsoform, "")
	t.Setenv(agentHomeEnv, t.TempDir())
	app := &App{updater: &updater{downloadURL: "http://127.0.0.1:1"}}
	app.downloadBackgroundUpdate()
	if app.restorePendingUpdate() {
		t.Fatal("development build restored release update")
	}
	if info := app.CheckForUpdates(); info.Available || info.Error != "" {
		t.Fatalf("development check: %+v", info)
	}
	if app.GetUpdateStatus().Status != "disabled" || app.updater.lastChecked != 0 {
		t.Fatal("development build claimed a release check")
	}
}

func TestAboutSupportArchiveDropsAllFreeTextAndOldLogs(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.Local)
	raw := "2026/10/22 10:00:00.123 [update] screen secret speech /Users/person token\n2026/10/10 10:00:00 [main] old\n2026/10/22 10:00:01 [secret] content\n2026/10/22 10:00:02 [audio] private transcript\ncontinuation private\n"
	if err := os.WriteFile(filepath.Join(root, "yansu-gui.log"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "yansu-gui.log.old")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeSupportArchive(&buf, root, now); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 1 {
		t.Fatal("followed symlink")
	}
	reader, _ := archive.File[0].Open()
	defer reader.Close()
	clean, _ := io.ReadAll(reader)
	if string(clean) != "2026/10/22 10:00:00 [update]\n2026/10/22 10:00:02 [audio]\n" {
		t.Fatalf("unsafe support log: %s", clean)
	}
}

func TestAboutSupportArchiveSummarizesCrashesWithoutTheirContents(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	logs := filepath.Join(root, "logs")
	if err := os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "crash-20261027T100000.000000000Z-42.txt"), []byte("secret crash text"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(logs, "crash-20261027T100000.000000000Z-42.txt"), filepath.Join(logs, "crash-20261027T110000.000000000Z-43.txt")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeSupportArchive(&buf, root, now); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil || len(archive.File) != 1 || archive.File[0].Name != "crash-summary.txt" {
		t.Fatalf("crash summary: %+v, %v", archive, err)
	}
	reader, err := archive.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	contents, err := io.ReadAll(reader)
	if err != nil || string(contents) != "2026-10-27T10:00:00Z 17 bytes\n" {
		t.Fatalf("unsafe crash summary: %q, %v", contents, err)
	}
}

func TestAboutAppMenuCopyMatchesBothLocales(t *testing.T) {
	for _, language := range []string{"en", "zh-CN"} {
		raw, err := os.ReadFile(filepath.Join("frontend", "src", "i18n", "locales", language, "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, title := range appMenuTitles(language) {
			if !strings.Contains(string(raw), title) {
				t.Fatalf("menu label %q missing in %s", title, language)
			}
		}
	}
}

func TestAboutBuildNeverReportsStagingForRelease(t *testing.T) {
	setReleaseTestBuild(t, appFlavorIsoform, buildChannelRelease)
	t.Setenv(agentHomeEnv, t.TempDir())
	app := &App{updater: &updater{lastChecked: 123}}
	info, err := app.GetAboutInfo()
	if err != nil || info.Build != "release" || info.LastChecked != 123 || !info.BackgroundUpdates {
		t.Fatalf("about: %+v %v", info, err)
	}
	if !app.GetEnvironment().IsRelease {
		t.Fatal("release badge gate missing")
	}
}

func TestAboutAppMenuNavigation(t *testing.T) {
	cases := map[int]string{appMenuAbout: "about", appMenuCheck: "about", appMenuSettings: "", appMenuShortcuts: "keybindings", appMenuIsoformHelp: "about"}
	for action := appMenuAbout; action <= appMenuFeedback; action++ {
		opened, checked, restarted, mailed := "unopened", false, false, false
		dispatchAppMenuAction(action, appMenuActions{
			open:     func(topic string) { opened = topic },
			check:    func() { checked = true },
			restart:  func() { restarted = true },
			feedback: func() { mailed = true },
		})
		want, opens := cases[action]
		if !opens {
			want = "unopened"
		}
		if opened != want || checked != (action == appMenuCheck) || restarted != (action == appMenuRestart) || mailed != (action == appMenuFeedback) {
			t.Fatalf("menu action %d opened %q checked %v restarted %v mailed %v", action, opened, checked, restarted, mailed)
		}
	}
}

// Design S3: a downloaded update shows Restart to Update in the app menu.
// A5: the Help menu's Send Feedback… opens the same draft as Settings › About.
func TestAppMenuRestartAndFeedback(t *testing.T) {
	app := &App{updater: &updater{}}
	if app.appMenuUpdateReady() {
		t.Fatal("no update downloaded, but the menu would show Restart to Update")
	}
	app.updater.status = UpdateStatus{Status: "update-downloaded", Version: "1.2.3"}
	if !app.appMenuUpdateReady() {
		t.Fatal("a downloaded update must show Restart to Update")
	}
	got := appMenuFeedbackURL("en", "1.2.3")
	want := "mailto:bo@isoform.ai?subject=Isoform%20feedback&body=Isoform%20version%3A%201.2.3%0A%0AWhat%20happened%2C%20or%20what%20would%20you%20like%20to%20improve%3F%0A%0A"
	if got != want {
		t.Fatalf("feedback url = %q, want %q", got, want)
	}
	for _, language := range []string{"en", "zh-CN"} {
		raw, err := os.ReadFile(filepath.Join("frontend", "src", "i18n", "locales", language, "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		w := appMenuWords(language)
		for _, text := range []string{w.FeedbackSubject, strings.ReplaceAll(w.FeedbackBody, "\n", `\n`)} {
			if !strings.Contains(string(raw), text) {
				t.Fatalf("feedback words %q missing in %s settings.json", text, language)
			}
		}
	}
}

func waitBackgroundDownload(t *testing.T, app *App) {
	t.Helper()
	app.updater.mu.Lock()
	done := app.updater.backgroundDone
	app.updater.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background download did not settle")
	}
}

func TestUpdateBackgroundQueueCoalescesAndCancels(t *testing.T) {
	state := &updater{}
	started, release := make(chan struct{}), make(chan struct{})
	var calls, active atomic.Int32
	download := func() {
		if active.Add(1) != 1 {
			t.Error("background downloads overlapped")
		}
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		active.Add(-1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := queueUpdateDownload(ctx, state, download)
	<-started
	if second := queueUpdateDownload(ctx, state, download); second != done {
		t.Fatal("spawned another worker")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("queued update never finished")
	}
	if calls.Load() != 2 {
		t.Fatal("lost the policy change queued during download")
	}
	cancel()
	if queueUpdateDownload(ctx, state, func() { t.Error("downloaded after shutdown") }) != nil {
		t.Fatal("queued after shutdown")
	}
}
