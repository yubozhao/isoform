package main

import (
	"strings"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/clipboard"
)

func TestClipboardSourceAllowed(t *testing.T) {
	cases := []struct {
		name  string
		front activitymon.AppInfo
		want  bool
	}{
		{"notes", activitymon.AppInfo{BundleID: "com.apple.Notes", AppName: "Notes", WindowTitle: "Ideas"}, true},
		{"password manager", activitymon.AppInfo{BundleID: "com.1password.1password", AppName: "1Password"}, false},
		{"keychain", activitymon.AppInfo{BundleID: "com.apple.keychainaccess", AppName: "Keychain Access"}, false},
		{"incognito", activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: "Bank - Google Chrome (Incognito)"}, false},
		{"private safari", activitymon.AppInfo{BundleID: "com.apple.Safari", AppName: "Safari", WindowTitle: "Private Browsing"}, false},
		{"normal chrome", activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: "Docs"}, true},
	}
	for _, tc := range cases {
		if got := clipboardSourceAllowed(tc.front, "com.wails.Isoform"); got != tc.want {
			t.Fatalf("%s: clipboardSourceAllowed = %v, want %v", tc.name, got, tc.want)
		}
	}
	// Copies made in Isoform itself are its own content, never noted.
	if clipboardSourceAllowed(activitymon.AppInfo{BundleID: "com.wails.Isoform", AppName: "Isoform"}, "com.wails.Isoform") {
		t.Fatal("a copy in Isoform must not be noted")
	}
	if clipboardAppAllowed(clipboard.App{BundleID: "com.bitwarden.desktop", Name: "Bitwarden"}, "") {
		t.Fatal("a password manager must not be noted")
	}
	// A private window sampled at the tick before the copy was seen refuses
	// it, even after a switch to an ordinary app.
	if clipboardAppAllowed(clipboard.App{BundleID: "com.google.Chrome", Name: "Google Chrome", Private: true}, "") {
		t.Fatal("a copy from a private window must not be noted")
	}
}

func TestClipboardActivityEventKeepsOnlyKindLengthAndPreview(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	front := activitymon.AppInfo{BundleID: "com.apple.Notes", AppName: "Notes", WindowTitle: "Secret plans"}

	evt, ok := clipboardActivityEvent(clipboard.Copy{
		At: at, Kind: clipboard.KindText, ContentRead: true,
		Text: "Call ann@example.com about the pricing deck", Length: 43,
	}, front, false)
	if !ok || evt.Kind != activitymon.EventClipboard || evt.ClipboardKind != "text" || evt.ClipboardLength != 43 {
		t.Fatalf("event %+v", evt)
	}
	if strings.Contains(evt.ClipboardPreview, "ann@example.com") || !strings.Contains(evt.ClipboardPreview, "pricing deck") {
		t.Fatalf("preview %q", evt.ClipboardPreview)
	}
	if evt.WindowTitle != "" || evt.Timestamp != at.UnixMilli() || evt.AppName != "Notes" {
		t.Fatalf("event carries more than it should: %+v", evt)
	}

	// Not readable: only the kind.
	evt, ok = clipboardActivityEvent(clipboard.Copy{At: at, Kind: clipboard.KindText}, front, false)
	if !ok || evt.ClipboardPreview != "" || evt.ClipboardLength != 0 {
		t.Fatalf("unreadable copy %+v", evt)
	}

	// A password-like text keeps neither a preview nor its length.
	evt, _ = clipboardActivityEvent(clipboard.Copy{At: at, Kind: clipboard.KindText, ContentRead: true, Text: "hX7#kq9!Lm", Length: 10}, front, false)
	if evt.ClipboardPreview != "" || evt.ClipboardLength != 0 {
		t.Fatalf("password kept %q / %d", evt.ClipboardPreview, evt.ClipboardLength)
	}

	// An image or a file never gets a text preview.
	evt, _ = clipboardActivityEvent(clipboard.Copy{At: at, Kind: clipboard.KindFile, ContentRead: true, Text: "/Users/me/Taxes.pdf", Length: 19}, front, false)
	if evt.ClipboardPreview != "" {
		t.Fatalf("file preview %q", evt.ClipboardPreview)
	}

	// The copying app named a password manager as the source: dropped.
	if _, ok := clipboardActivityEvent(clipboard.Copy{At: at, Kind: clipboard.KindText, ContentRead: true, Text: "x", Source: "com.bitwarden.desktop"}, front, false); ok {
		t.Fatal("a copy whose source is a password manager must be dropped")
	}
}

func TestClipboardSafariCopiesKeepOnlyTheKind(t *testing.T) {
	safari := activitymon.AppInfo{BundleID: "com.apple.Safari", AppName: "Safari", WindowTitle: "Bank - Statement"}
	notes := activitymon.AppInfo{BundleID: "com.apple.Notes", AppName: "Notes"}
	chrome := clipboard.App{BundleID: "com.google.Chrome", Name: "Google Chrome"}

	// Safari in front, or in front at the tick before (copied, then switched).
	if !clipboardKindOnly([]clipboard.App{{BundleID: "com.apple.Safari"}}, safari) {
		t.Fatal("a copy in Safari keeps only its kind")
	}
	if !clipboardKindOnly([]clipboard.App{{BundleID: "com.apple.Notes"}, {BundleID: "com.apple.SafariTechnologyPreview"}}, notes) {
		t.Fatal("a copy made in Safari just before switching keeps only its kind")
	}
	if clipboardKindOnly([]clipboard.App{chrome}, activitymon.AppInfo{BundleID: chrome.BundleID}) {
		t.Fatal("Chrome's private windows are recognised by title; its copies keep a preview")
	}

	evt, ok := clipboardActivityEvent(clipboard.Copy{
		At: time.Now(), Kind: clipboard.KindText, ContentRead: true,
		Text: "Statement balance due Friday", Length: 28,
	}, safari, true)
	if !ok || evt.ClipboardKind != clipboard.KindText || evt.ClipboardPreview != "" || evt.ClipboardLength != 0 || evt.AppName != "Safari" {
		t.Fatalf("a Safari copy kept more than its kind: %+v", evt)
	}
}

func TestClipboardTimelineLine(t *testing.T) {
	line := clipboardTimelineLine(activitymon.ActivityEvent{
		Kind: activitymon.EventClipboard, AppName: "Safari", ClipboardKind: "url",
		ClipboardPreview: "https://example.com/pricing",
	})
	if line != "- Copied a link in Safari: \"https://example.com/pricing\" (a copy is a sign of what the user means to use next)\n" {
		t.Fatalf("line %q", line)
	}
	line = clipboardTimelineLine(activitymon.ActivityEvent{Kind: activitymon.EventClipboard, AppName: "Notes", ClipboardKind: "text", ClipboardLength: 120})
	if !strings.HasPrefix(line, "- Copied text (about 120 characters) in Notes (") || strings.Count(line, "\n") != 1 {
		t.Fatalf("line %q", line)
	}
}

func TestTriageTimelineHasOneLinePerCopy(t *testing.T) {
	c := &ActivityTriager{app: &App{}}
	group := sessionGroup{Sessions: []ActivitySessionRow{{ID: "s1", StartTime: 1, EndTime: 2}}}
	events := []activitymon.ActivityEvent{
		{Kind: activitymon.EventClipboard, AppName: "Notes", ClipboardKind: "text", ClipboardLength: 12, ClipboardPreview: "Ship it today"},
		{Kind: activitymon.EventClipboard, AppName: "Safari", ClipboardKind: "url"},
	}
	body, _, _ := c.buildGroupSummary(group, events, ProfileGroup(group, events))
	if strings.Count(body, "- Copied ") != 2 || !strings.Contains(body, `"Ship it today"`) {
		t.Fatalf("timeline:\n%s", body)
	}
}

func TestClipboardObserverFollowsSwitchAndPause(t *testing.T) {
	a := &App{store: newIdentityTestStore(t)}
	obs := &clipboardObserver{app: a, watcher: &clipboard.Watcher{}}
	a.clipboard.Store(obs)

	if !obs.allowed() {
		t.Fatal("on by default")
	}
	if err := a.store.SetUIState(t.Context(), permissionUIStateKey(clipboardPermissionKey), "false"); err != nil {
		t.Fatal(err)
	}
	a.clipboardSwitchChanged(false)
	if obs.allowed() {
		t.Fatal("the switch turns it off")
	}
	if err := a.store.SetUIState(t.Context(), permissionUIStateKey(clipboardPermissionKey), "true"); err != nil {
		t.Fatal(err)
	}
	a.clipboardSwitchChanged(true)
	a.closedEyes.enabled.Store(true)
	if obs.allowed() {
		t.Fatal("pausing observation pauses it")
	}
}

// resumeRaceBoard is a pasteboard whose change counter can run a hook, so a
// test can put a watcher tick at an exact point of a resume.
type resumeRaceBoard struct {
	count   int
	onCount func()
}

func (b *resumeRaceBoard) ChangeCount() int {
	if hook := b.onCount; hook != nil {
		hook()
	}
	return b.count
}
func (b *resumeRaceBoard) Types() []string       { return []string{"public.utf8-plain-text"} }
func (b *resumeRaceBoard) ContentReadable() bool { return false }
func (b *resumeRaceBoard) ReadText(int) (string, int, string, bool) {
	return "", 0, "", false
}

func TestClipboardCopyInThePausesLastSecondIsNeverRecorded(t *testing.T) {
	a := &App{store: newIdentityTestStore(t), audioScreenLocked: func() bool { return false }}
	board := &resumeRaceBoard{count: 1}
	obs := &clipboardObserver{app: a}

	var copies []clipboard.Copy
	obs.watcher = &clipboard.Watcher{
		Board:   board,
		Allowed: obs.allowed,
		OnCopy:  func(c clipboard.Copy) { copies = append(copies, c) },
	}
	a.clipboard.Store(obs)
	obs.watcher.Tick() // baseline

	if err := a.SetClosedEyesModeEnabled(true); err != nil {
		t.Fatal(err)
	}
	obs.watcher.Tick()
	board.count++ // copied in the pause's last second

	// A watcher tick runs the first time the counter is read once the pause
	// flag has cleared: in the middle of the resume.
	ticked := false
	board.onCount = func() {
		if ticked || a.IsClosedEyesModeEnabled() {
			return
		}
		ticked = true
		board.onCount = nil
		obs.watcher.Tick()
	}
	if err := a.SetClosedEyesModeEnabled(false); err != nil {
		t.Fatal(err)
	}
	board.onCount = nil
	obs.watcher.Tick()
	if len(copies) != 0 {
		t.Fatalf("a copy made while paused was recorded on resume: %+v", copies)
	}

	board.count++ // a real copy after resuming
	obs.watcher.Tick()
	if len(copies) != 1 {
		t.Fatalf("a copy after resuming was not recorded: %d", len(copies))
	}
}
