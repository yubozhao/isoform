package main

import (
	"context"
	"testing"
	"time"

	"wails-gui/internal/activitybuf"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/ocr"
)

type fakeOCREngine struct {
	supported bool
}

func (f fakeOCREngine) RecognizeJPEG(_ []byte, _ []string, _ ocr.Priority) (*ocr.Result, error) {
	return nil, nil
}

func (f fakeOCREngine) RecognizeJPEGWithBoxes(_ []byte, _ []string, _ ocr.Priority) ([]ocr.TextObservation, error) {
	return nil, nil
}

func (f fakeOCREngine) IsSupported() bool {
	return f.supported
}

func (f fakeOCREngine) SupportedLanguages() []string {
	return nil
}

func TestCollectSpeechEventStats(t *testing.T) {
	events := []activitymon.ActivityEvent{
		{Kind: activitymon.EventKeyboard, KeyChars: []string{"a"}},
		{Kind: activitymon.EventSpeech, SpeechText: "  first speech  "},
		{Kind: activitymon.EventSpeech, SpeechText: ""},
		{Kind: activitymon.EventSpeech, SpeechText: "second speech"},
		{Kind: activitymon.EventSpeech, SpeechText: "third speech"},
		{Kind: activitymon.EventSpeech, SpeechText: "fourth speech"},
	}

	count, chars := collectSpeechEventStats(events)
	if count != 4 {
		t.Fatalf("collectSpeechEventStats() count = %d, want %d", count, 4)
	}

	wantChars := len([]rune("first speech")) + len([]rune("second speech")) + len([]rune("third speech")) + len([]rune("fourth speech"))
	if chars != wantChars {
		t.Fatalf("collectSpeechEventStats() chars = %d, want %d", chars, wantChars)
	}
}

func TestPIIOCRInterval(t *testing.T) {
	if got := piiOCRInterval("keyboard"); got != piiOCRMinInterval {
		t.Fatalf("piiOCRInterval(keyboard) = %v, want %v", got, piiOCRMinInterval)
	}
	if got := piiOCRInterval("heartbeat"); got != 0 {
		t.Fatalf("piiOCRInterval(heartbeat) = %v, want 0", got)
	}
	if got := piiOCRInterval("session_end"); got != 0 {
		t.Fatalf("piiOCRInterval(session_end) = %v, want 0", got)
	}
	if got := piiOCRInterval("calendar_keyboard"); got != 0 {
		t.Fatalf("piiOCRInterval(calendar_keyboard) = %v, want 0", got)
	}
}

// A blurred sensitive window leaves the rest of the frame legible, so such a
// frame still needs the OCR-based mask; before, a throttled or busy one was
// written with only the window blurred.
func TestUnmaskedWriteAllowed(t *testing.T) {
	cases := []struct {
		blur         sensitiveBlur
		ocrAvailable bool
		want         bool
	}{
		{sensitiveBlurNone, true, false},
		{sensitiveBlurRegions, true, false},
		{sensitiveBlurFull, true, true},
		{sensitiveBlurFailed, true, false},
		{sensitiveBlurNone, false, true},
		{sensitiveBlurRegions, false, true},
		{sensitiveBlurFull, false, true},
		{sensitiveBlurFailed, false, false},
	}
	for _, c := range cases {
		if got := unmaskedWriteAllowed(c.blur, c.ocrAvailable); got != c.want {
			t.Errorf("unmaskedWriteAllowed(%d, ocr=%v) = %v, want %v", c.blur, c.ocrAvailable, got, c.want)
		}
	}
}

func TestIsHeavyAXWalkApp(t *testing.T) {
	cases := map[string]bool{
		"com.alibaba.DingTalk":      true, // exact bundle, case-insensitive
		"com.alibaba.dingtalk.foo":  true, // prefix match on a sub-bundle
		"COM.ALIBABA.DINGTALK":      true,
		"com.tinyspeck.slackmacgap": false, // primed + parsed, must NOT throttle
		"com.google.Chrome":         false,
		"":                          false,
	}
	for bundle, want := range cases {
		if got := isHeavyAXWalkApp(bundle); got != want {
			t.Fatalf("isHeavyAXWalkApp(%q) = %v, want %v", bundle, got, want)
		}
	}
}

func TestReserveAXWalk(t *testing.T) {
	am := &activityMonitorState{}
	base := time.Unix(1_700_000_000, 0)

	// First walk for a bundle is always allowed.
	if !am.reserveAXWalk("com.alibaba.DingTalk", base) {
		t.Fatal("first reserveAXWalk should be allowed")
	}
	// Within the throttle window it is denied.
	if am.reserveAXWalk("com.alibaba.DingTalk", base.Add(heavyAXWalkMinInterval-time.Second)) {
		t.Fatal("reserveAXWalk within the throttle window should be denied")
	}
	// After the window it is allowed again.
	if !am.reserveAXWalk("com.alibaba.DingTalk", base.Add(heavyAXWalkMinInterval)) {
		t.Fatal("reserveAXWalk after the throttle window should be allowed")
	}
	// The throttle is keyed per bundle: a different app is independent.
	if !am.reserveAXWalk("com.example.other", base.Add(time.Second)) {
		t.Fatal("reserveAXWalk for a different bundle should be independent")
	}
}

func TestGoogleCalendarForcedCaptureHelpers(t *testing.T) {
	saveClick := activitymon.ActivityEvent{
		Kind:        activitymon.EventClick,
		WindowTitle: "Google 日历 - 2027年9月5日这周 - Google Chrome - Alex",
		ClickLabel:  "保存",
	}
	if !isGoogleCalendarSaveClick(saveClick) {
		t.Fatal("expected localized Google Calendar Save click to force capture")
	}

	englishSaveClick := activitymon.ActivityEvent{
		Kind:        activitymon.EventClick,
		WindowTitle: "Google Calendar - Week of September 5, 2027 - Google Chrome",
		ClickLabel:  "Save",
	}
	if !isGoogleCalendarSaveClick(englishSaveClick) {
		t.Fatal("expected English Google Calendar Save click to force capture")
	}

	nonCalendarSaveClick := activitymon.ActivityEvent{
		Kind:        activitymon.EventClick,
		WindowTitle: "GitHub - Google Chrome",
		ClickLabel:  "Save",
	}
	if isGoogleCalendarSaveClick(nonCalendarSaveClick) {
		t.Fatal("non-calendar Save click should not force capture")
	}

	if !isForcedActivityCaptureTrigger("calendar_save_followup") {
		t.Fatal("calendar save follow-up should bypass visual/PII throttles")
	}
	if isForcedActivityCaptureTrigger("keyboard") {
		t.Fatal("regular keyboard capture should not bypass visual/PII throttles")
	}
}

func TestIsSlideNavKeyEvent(t *testing.T) {
	const (
		pageDown   uint16 = 0x79
		pageUp     uint16 = 0x74
		home       uint16 = 0x73
		end        uint16 = 0x77
		leftArrow  uint16 = 0x7B
		rightArrow uint16 = 0x7C
		downArrow  uint16 = 0x7D
		upArrow    uint16 = 0x7E
		space      uint16 = 0x31
		returnKey  uint16 = 0x24
		letterA    uint16 = 0x00
	)
	const (
		modCtrl uint64 = cgFlagMaskControl
		modOpt  uint64 = cgFlagMaskOption
		modCmd  uint64 = cgFlagMaskCommand
		modNone uint64 = 0
	)

	cases := []struct {
		name     string
		evt      activitymon.ActivityEvent
		fallback string
		want     bool
	}{
		{
			name: "PageDown in Keynote fires regardless of modifier",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: pageDown, Modifiers: modNone,
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: true,
		},
		{
			name: "PageDown in modern Keynote fires",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: pageDown, Modifiers: modNone,
				AppBundleID: "com.apple.Keynote",
			},
			want: true,
		},
		{
			name: "PageUp in PowerPoint fires",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: pageUp, Modifiers: modNone,
				AppBundleID: "com.microsoft.Powerpoint",
			},
			want: true,
		},
		{
			name: "Home in Keynote fires",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: home, Modifiers: modNone,
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: true,
		},
		{
			name: "End in Keynote fires",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: end, Modifiers: modNone,
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: true,
		},
		{
			name: "Bare arrow key in Keynote does NOT fire (edit cursor)",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: leftArrow, Modifiers: modNone,
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: false,
		},
		{
			name: "Ctrl+Option+Up in Keynote fires (the real-user case)",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: upArrow,
				Modifiers:   modCtrl | modOpt,
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: true,
		},
		{
			name: "Cmd+Down in Keynote fires",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: downArrow, Modifiers: modCmd,
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: true,
		},
		{
			name: "Shift+Right alone in Keynote fires (shift counts as modifier)",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: rightArrow,
				Modifiers:   cgFlagMaskShift,
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: true,
		},
		{
			name: "Modifier+Arrow in Word does NOT fire (not a presentation app)",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: upArrow,
				Modifiers:   modCtrl | modOpt,
				AppBundleID: "com.microsoft.Word",
			},
			want: false,
		},
		{
			name: "Space in Keynote does NOT fire (insert-space false positive)",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: space, Modifiers: modNone,
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: false,
		},
		{
			name: "Return in Keynote does NOT fire",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: returnKey, Modifiers: modNone,
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: false,
		},
		{
			name: "Letter key in Keynote does NOT fire",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: letterA, Modifiers: modNone,
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: false,
		},
		{
			name: "PageDown in Word does NOT fire (not a presentation app)",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: pageDown, Modifiers: modNone,
				AppBundleID: "com.microsoft.Word",
			},
			want: false,
		},
		{
			name: "PageDown with empty AppBundleID falls back to last-app",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, KeyCode: pageDown, Modifiers: modNone,
				AppBundleID: "",
			},
			fallback: "com.apple.iWork.Keynote",
			want:     true,
		},
		{
			name: "Click event with matching KeyCode does NOT fire (wrong event kind)",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventClick, KeyCode: pageDown,
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isSlideNavKeyEvent(tc.evt, tc.fallback)
			if got != tc.want {
				t.Errorf("isSlideNavKeyEvent: got %v want %v", got, tc.want)
			}
		})
	}

	if !isForcedActivityCaptureTrigger("slide_change") {
		t.Fatal("slide_change must be a forced trigger so consecutive slides survive visual-distance dedup")
	}
}

func TestIsSlideNavigatorClick(t *testing.T) {
	cases := []struct {
		name     string
		evt      activitymon.ActivityEvent
		fallback string
		want     bool
	}{
		{
			name: "AXRow click in Keynote fires (navigator thumbnail — the real-user case)",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventClick, ClickRole: "AXRow",
				ClickLabel:  "效率与系统健壮性提升",
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: true,
		},
		{
			name: "AXRow click in modern Keynote fires",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventClick, ClickRole: "AXRow",
				AppBundleID: "com.apple.Keynote",
			},
			want: true,
		},
		{
			name: "AXRow click in PowerPoint fires",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventClick, ClickRole: "AXRow",
				AppBundleID: "com.microsoft.Powerpoint",
			},
			want: true,
		},
		{
			name: "AXRow click in Word does NOT fire (not a presentation app)",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventClick, ClickRole: "AXRow",
				AppBundleID: "com.microsoft.Word",
			},
			want: false,
		},
		{
			name: "Non-AXRow click in Keynote does NOT fire",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventClick, ClickRole: "AXButton",
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: false,
		},
		{
			name: "Keyboard event with ClickRole=AXRow does NOT fire (wrong kind)",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventKeyboard, ClickRole: "AXRow",
				AppBundleID: "com.apple.iWork.Keynote",
			},
			want: false,
		},
		{
			name: "AXRow click with empty AppBundleID falls back to last-app",
			evt: activitymon.ActivityEvent{
				Kind: activitymon.EventClick, ClickRole: "AXRow",
				AppBundleID: "",
			},
			fallback: "com.apple.iWork.Keynote",
			want:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isSlideNavigatorClick(tc.evt, tc.fallback)
			if got != tc.want {
				t.Errorf("isSlideNavigatorClick: got %v want %v", got, tc.want)
			}
		})
	}
}

func TestReservePIIOCRThrottle(t *testing.T) {
	am := &activityMonitorState{}
	now := time.Unix(100, 0)

	if !am.reservePIIOCR(now, piiOCRMinInterval) {
		t.Fatal("fresh monitor should reserve the first PII OCR pass")
	}
	if am.reservePIIOCR(now.Add(piiOCRMinInterval-time.Millisecond), piiOCRMinInterval) {
		t.Fatal("expected PII OCR to throttle inside the minimum interval")
	}
	if !am.reservePIIOCR(now.Add(piiOCRMinInterval), piiOCRMinInterval) {
		t.Fatal("expected PII OCR to reserve once the minimum interval has elapsed")
	}
}

func TestReservePIIOCRBypass(t *testing.T) {
	am := &activityMonitorState{}
	now := time.Unix(100, 0)

	if !am.reservePIIOCR(now, 0) {
		t.Fatal("zero interval should reserve the first PII OCR pass")
	}
	if !am.reservePIIOCR(now.Add(time.Millisecond), 0) {
		t.Fatal("zero interval should bypass throttling for forced captures")
	}
}

func TestQueuePendingPIISnapshotBounds(t *testing.T) {
	am := &activityMonitorState{enabled: true}

	for i := 0; i < pendingPIIQueueMaxFrames+1; i++ {
		ok := am.queuePendingPIISnapshot(pendingPIISnapshot{
			path:     "unused.jpg",
			jpegData: []byte{byte(i)},
			snapshot: snapshotEntry{
				relPath:   "snap.jpg",
				timestamp: int64(i + 1),
			},
			bundleID: "com.example.App",
		})
		if ok != (i < pendingPIIQueueMaxFrames) {
			t.Fatalf("queuePendingPIISnapshot(%d) returned false", i)
		}
	}

	if got := len(am.pendingPIISnapshots); got != pendingPIIQueueMaxFrames {
		t.Fatalf("pendingPIISnapshots len = %d, want %d", got, pendingPIIQueueMaxFrames)
	}
	if got := am.pendingPIIBytes; got != int64(pendingPIIQueueMaxFrames) {
		t.Fatalf("pendingPIIBytes = %d, want %d", got, pendingPIIQueueMaxFrames)
	}
	if got := am.pendingPIISnapshots[0].jpegData[0]; got != 0 {
		t.Fatalf("oldest pending frame was evicted, first byte = %d", got)
	}
	if got := am.lastCaptureTime; got != int64(pendingPIIQueueMaxFrames) {
		t.Fatalf("lastCaptureTime = %d, want latest queued timestamp", got)
	}
	if got := am.lastSavedTime; got != int64(pendingPIIQueueMaxFrames) {
		t.Fatalf("lastSavedTime = %d, want latest queued timestamp for pacing", got)
	}
}

func TestAppendCapturedSnapshotAdvancesParsedHash(t *testing.T) {
	am := &activityMonitorState{enabled: true}
	entry := snapshotEntry{relPath: "snap.jpg", timestamp: 1234}

	if !am.appendCapturedSnapshot(entry, "com.example.App", "hash-1") {
		t.Fatal("appendCapturedSnapshot returned false")
	}

	if got := len(am.allSnapshots); got != 1 {
		t.Fatalf("allSnapshots len = %d, want 1", got)
	}
	if got := am.lastParsedHashByBundle["com.example.App"]; got != "hash-1" {
		t.Fatalf("lastParsedHashByBundle = %q, want hash-1", got)
	}
}

func TestDeferExtractSessionUntilPIIReadyWhileInitInFlight(t *testing.T) {
	app := &App{}
	am := &activityMonitorState{
		enabled:         true,
		ocrEngine:       fakeOCREngine{supported: true},
		piiInitInFlight: true,
	}

	if !app.deferLocalFlushWhilePIIStarting(am, "idle_transition") {
		t.Fatal("expected extraction to defer while GLiNER init is in flight")
	}
	if got := am.pendingPIIExtractTrigger; got != "idle_transition" {
		t.Fatalf("pendingPIIExtractTrigger = %q, want idle_transition", got)
	}
}

func TestDeferExtractSessionUntilPIIReadySkipsShutdown(t *testing.T) {
	app := &App{}
	am := &activityMonitorState{
		enabled:         false,
		ocrEngine:       fakeOCREngine{supported: true},
		piiInitInFlight: true,
	}

	if app.deferLocalFlushWhilePIIStarting(am, "shutdown_in_meeting") {
		t.Fatal("shutdown flush should not defer behind PII readiness")
	}
	if got := am.pendingPIIExtractTrigger; got != "" {
		t.Fatalf("pendingPIIExtractTrigger = %q, want empty", got)
	}
}

func TestMergeDeferredPIIExtractTriggerPrioritizesTerminalMeeting(t *testing.T) {
	if got := mergeDeferredPIIExtractTrigger("periodic_flush", "meeting_ended"); got != "meeting_ended" {
		t.Fatalf("mergeDeferredPIIExtractTrigger = %q, want meeting_ended", got)
	}
	if got := mergeDeferredPIIExtractTrigger("idle_transition", "periodic_flush"); got != "idle_transition" {
		t.Fatalf("mergeDeferredPIIExtractTrigger = %q, want idle_transition", got)
	}
}

func TestRecordStateEventChurnActivatesTemporaryCaptureFloor(t *testing.T) {
	am := &activityMonitorState{}
	start := int64(1_000)

	for i := 0; i < windowChurnThreshold-1; i++ {
		am.recordStateEventChurnLocked(start + int64(i*100))
	}
	if am.stateEventChurnUntilMs != 0 {
		t.Fatalf("stateEventChurnUntilMs = %d, want inactive before threshold", am.stateEventChurnUntilMs)
	}

	triggerAt := start + int64((windowChurnThreshold-1)*100)
	am.recordStateEventChurnLocked(triggerAt)
	if am.stateEventChurnUntilMs != triggerAt+int64(windowChurnCooldownMs) {
		t.Fatalf("stateEventChurnUntilMs = %d, want %d", am.stateEventChurnUntilMs, triggerAt+int64(windowChurnCooldownMs))
	}
}

func TestRecordStateEventChurnResetsAfterWindow(t *testing.T) {
	am := &activityMonitorState{}
	start := int64(1_000)

	am.recordStateEventChurnLocked(start)
	am.recordStateEventChurnLocked(start + int64(windowChurnWindowMs) + 1)

	if am.stateEventCount != 1 {
		t.Fatalf("stateEventCount = %d, want reset to 1", am.stateEventCount)
	}
	if am.stateEventWindowMs != start+int64(windowChurnWindowMs)+1 {
		t.Fatalf("stateEventWindowMs = %d, want reset window start", am.stateEventWindowMs)
	}
}

func TestHasRecentUserActivityForVisualChange(t *testing.T) {
	am := &activityMonitorState{}
	now := int64(60_000)

	if am.hasRecentUserActivityForVisualChangeLocked(now) {
		t.Fatal("expected visual change to skip before any user activity")
	}

	am.lastUserActivityTime = now - int64(visualActivityWindow/time.Millisecond) + 1
	if !am.hasRecentUserActivityForVisualChangeLocked(now) {
		t.Fatal("expected visual change after recent user activity")
	}

	am.lastUserActivityTime = now - int64(visualActivityWindow/time.Millisecond) - 1
	if am.hasRecentUserActivityForVisualChangeLocked(now) {
		t.Fatal("expected visual change to skip after user activity ages out")
	}
}

func TestInjectSpeechEventDoesNotMarkVisualUserActivity(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}

	app.injectSpeechEvent("  hello world  ", backgroundListeningSourceMicrophone, speechCaptureContext{})
	if app.activity.idleTimer != nil {
		defer app.activity.idleTimer.Stop()
	}

	if app.activity.lastUserActivityTime != 0 {
		t.Fatalf("lastUserActivityTime = %d, want speech-only events to leave it unchanged", app.activity.lastUserActivityTime)
	}
}

func TestShouldTrimActivitySessionMemory(t *testing.T) {
	tests := []struct {
		name           string
		snapshotCount  int
		totalSizeBytes int64
		want           bool
	}{
		{name: "small session", snapshotCount: memoryTrimSnapshotThreshold - 1, totalSizeBytes: memoryTrimSizeBytes - 1, want: false},
		{name: "many snapshots", snapshotCount: memoryTrimSnapshotThreshold, totalSizeBytes: 1, want: true},
		{name: "large bytes", snapshotCount: 1, totalSizeBytes: memoryTrimSizeBytes, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldTrimActivitySessionMemory(tt.snapshotCount, tt.totalSizeBytes); got != tt.want {
				t.Fatalf("shouldTrimActivitySessionMemory() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestInjectSpeechEvent_ArmsIdleTimer(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}

	app.injectSpeechEvent("  hello world  ", backgroundListeningSourceMicrophone, speechCaptureContext{})

	if app.activity.idleTimer == nil {
		t.Fatal("expected speech event to arm idle timer")
	}
	defer app.activity.idleTimer.Stop()

	events := app.activity.buf.Snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 buffered event, got %d", len(events))
	}
	if events[0].Kind != activitymon.EventSpeech {
		t.Fatalf("event kind = %q, want %q", events[0].Kind, activitymon.EventSpeech)
	}
	if events[0].SpeechText != "hello world" {
		t.Fatalf("speech text = %q, want %q", events[0].SpeechText, "hello world")
	}
	// Speech events now capture the actual frontmost app instead of
	// hardcoded "Background audio". In tests the foreground app varies
	// so just verify the field is non-empty.
	if events[0].AppName == "" {
		t.Fatal("app name should be non-empty (frontmost app or fallback)")
	}
}

func TestInjectSpeechEvent_KeepsVoiceInputAppAsDictation(t *testing.T) {
	// Pick a known voice-input bundle ID from voiceInputAppBundleIDs so the
	// test stays in sync if entries are renamed (the lookup, not the literal
	// string, is what we're asserting).
	var voiceInputBundleID string
	for id := range voiceInputAppBundleIDs {
		voiceInputBundleID = id
		break
	}
	if voiceInputBundleID == "" {
		t.Fatal("voiceInputAppBundleIDs is empty — test cannot run")
	}

	cases := []struct {
		name         string
		source       backgroundListeningSource
		bundleID     string
		wantBuffered bool
	}{
		{
			name:         "mic + voice-input app frontmost is kept as dictation",
			source:       backgroundListeningSourceMicrophone,
			bundleID:     voiceInputBundleID,
			wantBuffered: true,
		},
		{
			name:         "mic + unknown app frontmost is buffered",
			source:       backgroundListeningSourceMicrophone,
			bundleID:     "com.example.someregularapp",
			wantBuffered: true,
		},
		{
			name:         "system_audio source ignores voice-input filter",
			source:       backgroundListeningSourceSystem,
			bundleID:     voiceInputBundleID,
			wantBuffered: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{
				activity: &activityMonitorState{
					buf: activitybuf.NewRingBuffer(16, time.Minute),
				},
			}

			hearComputerOutsideCalls(t)
			ctx := speechCaptureContext{
				App: activitymon.AppInfo{BundleID: tc.bundleID, AppName: "Test App"},
			}
			app.injectSpeechEvent("  hello world  ", tc.source, ctx)
			if app.activity.idleTimer != nil {
				defer app.activity.idleTimer.Stop()
			}

			events := app.activity.buf.Snapshot()
			gotBuffered := len(events) > 0
			if gotBuffered != tc.wantBuffered {
				t.Fatalf("buffered = %v (%d events), want buffered = %v",
					gotBuffered, len(events), tc.wantBuffered)
			}
			wantDictation := tc.source == backgroundListeningSourceMicrophone && tc.bundleID == voiceInputBundleID
			if gotBuffered && (events[0].SpeechDictationApp != "") != wantDictation {
				t.Fatalf("dictation label = %q, want labeled = %v", events[0].SpeechDictationApp, wantDictation)
			}
			if gotBuffered && events[0].SpeechInCall {
				t.Fatal("speech outside a call is never call speech")
			}
		})
	}
}

func TestInjectSpeechEvent_OtherMicUserSoftAmbient(t *testing.T) {
	cases := []struct {
		name           string
		source         backgroundListeningSource
		otherMicUser   bool
		inMeeting      bool
		alreadyEcho    bool
		frontBundle    string
		wantLikelyEcho bool
		wantDictation  string
	}{
		{
			name:          "mic + other mic user + not in meeting -> dictation to the app in front",
			source:        backgroundListeningSourceMicrophone,
			otherMicUser:  true,
			inMeeting:     false,
			alreadyEcho:   false,
			wantDictation: "Test App",
		},
		{
			name:           "mic + other mic user + a meeting app in front -> ambient",
			source:         backgroundListeningSourceMicrophone,
			otherMicUser:   true,
			frontBundle:    "us.zoom.xos",
			wantLikelyEcho: true,
		},
		{
			name:           "mic + other mic user + in meeting -> trust meeting (no change)",
			source:         backgroundListeningSourceMicrophone,
			otherMicUser:   true,
			inMeeting:      true,
			alreadyEcho:    false,
			wantLikelyEcho: false,
		},
		{
			name:           "mic + no other mic user -> unchanged",
			source:         backgroundListeningSourceMicrophone,
			otherMicUser:   false,
			inMeeting:      false,
			alreadyEcho:    false,
			wantLikelyEcho: false,
		},
		{
			name:           "system_audio + other mic user -> filter doesn't apply",
			source:         backgroundListeningSourceSystem,
			otherMicUser:   true,
			inMeeting:      false,
			alreadyEcho:    false,
			wantLikelyEcho: false,
		},
		{
			name:           "mic already echo + other mic user -> stays echo (no double-set)",
			source:         backgroundListeningSourceMicrophone,
			otherMicUser:   true,
			inMeeting:      false,
			alreadyEcho:    true,
			wantLikelyEcho: true,
			wantDictation:  "Test App",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{
				activity: &activityMonitorState{
					buf:       activitybuf.NewRingBuffer(16, time.Minute),
					inMeeting: tc.inMeeting,
				},
			}

			hearComputerOutsideCalls(t)
			front := tc.frontBundle
			if front == "" {
				front = "com.example.regularapp"
			}
			ctx := speechCaptureContext{
				App:          activitymon.AppInfo{BundleID: front, AppName: "Test App"},
				OtherMicUser: tc.otherMicUser,
				LikelyEcho:   tc.alreadyEcho,
			}
			app.injectSpeechEvent("  hello world  ", tc.source, ctx)
			if app.activity.idleTimer != nil {
				defer app.activity.idleTimer.Stop()
			}

			events := app.activity.buf.Snapshot()
			if len(events) != 1 {
				t.Fatalf("expected 1 buffered event, got %d", len(events))
			}
			if got := events[0].SpeechLikelyEcho; got != tc.wantLikelyEcho {
				t.Fatalf("SpeechLikelyEcho = %v, want %v", got, tc.wantLikelyEcho)
			}
			if !tc.inMeeting && tc.source == backgroundListeningSourceMicrophone {
				if got := events[0].SpeechDictationApp; got != tc.wantDictation {
					t.Fatalf("SpeechDictationApp = %q, want %q", got, tc.wantDictation)
				}
			}
		})
	}
}

func TestInjectSpeechEvent_UsesCapturedMeetingContextAfterWatcherDrops(t *testing.T) {
	capturedAt := time.Now()
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
			// Simulate delayed ASR returning after the live watcher briefly
			// decided the meeting was no longer active.
			inMeeting: false,
		},
	}

	app.injectSpeechEvent("  still in the Teams call  ", backgroundListeningSourceSystem, speechCaptureContext{
		CapturedAt:      capturedAt,
		App:             activitymon.AppInfo{BundleID: "com.googlecode.iterm2", AppName: "iTerm2", WindowTitle: "repo notes"},
		InMeeting:       true,
		MeetingAppName:  "Microsoft Teams",
		MeetingBundleID: "com.microsoft.teams2",
	})
	if app.activity.idleTimer != nil {
		defer app.activity.idleTimer.Stop()
	}

	events := app.activity.buf.Snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 buffered speech event, got %d", len(events))
	}
	evt := events[0]
	if evt.AppName != "Microsoft Teams" {
		t.Fatalf("AppName = %q, want Microsoft Teams", evt.AppName)
	}
	if evt.AppBundleID != "com.microsoft.teams2" {
		t.Fatalf("AppBundleID = %q, want com.microsoft.teams2", evt.AppBundleID)
	}
	if evt.Timestamp != capturedAt.UnixMilli() {
		t.Fatalf("Timestamp = %d, want captured timestamp %d", evt.Timestamp, capturedAt.UnixMilli())
	}
}

func TestInjectSpeechEvent_PersistsCandidateMeetingContext(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}

	app.injectSpeechEvent("  candidate meeting speech  ", backgroundListeningSourceSystem, speechCaptureContext{
		App:                     activitymon.AppInfo{BundleID: "com.googlecode.iterm2", AppName: "iTerm2", WindowTitle: "notes"},
		MeetingCandidate:        true,
		CandidateMeetingAppName: "Zoom",
		CandidateMeetingReason:  "candidate_continuation_audio",
	})
	if app.activity.idleTimer != nil {
		defer app.activity.idleTimer.Stop()
	}

	events := app.activity.buf.Snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 buffered speech event, got %d", len(events))
	}
	evt := events[0]
	if !evt.MeetingCandidate {
		t.Fatal("expected meeting candidate flag to be persisted on speech event")
	}
	if evt.MeetingCandidateApp != "Zoom" {
		t.Fatalf("MeetingCandidateApp = %q, want Zoom", evt.MeetingCandidateApp)
	}
	if evt.MeetingCandidateReason != "candidate_continuation_audio" {
		t.Fatalf("MeetingCandidateReason = %q, want candidate_continuation_audio", evt.MeetingCandidateReason)
	}
}

func TestExtractSession_PeriodicFlushSkipsDuringMeeting(t *testing.T) {
	// Periodic flush must not run while a meeting is active — the meeting
	// watcher owns those boundaries and extractSession would tear down the
	// meeting state. Verify the early-return preserves buffered events so
	// the next non-periodic trigger (meeting_flush / meeting_ended) can
	// pick them up.
	app := &App{
		activity: &activityMonitorState{
			buf:       activitybuf.NewRingBuffer(16, time.Minute),
			inMeeting: true,
		},
	}
	app.activity.buf.Push(activitymon.ActivityEvent{
		Timestamp:  time.Now().UnixMilli(),
		Kind:       activitymon.EventSpeech,
		AppName:    "Zoom",
		SpeechText: "discussion in progress",
	})

	app.extractSession("periodic_flush")

	if got := app.activity.buf.Len(); got != 1 {
		t.Fatalf("buffer length after periodic_flush during meeting = %d, want 1 (events preserved)", got)
	}
}

func TestExtractSession_PeriodicFlushSkipsDuringMeetingCandidate(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf:                    activitybuf.NewRingBuffer(16, time.Minute),
			meetingCandidateActive: true,
		},
	}
	app.activity.buf.Push(activitymon.ActivityEvent{
		Timestamp:           time.Now().UnixMilli(),
		Kind:                activitymon.EventSpeech,
		AppName:             "iTerm2",
		SpeechText:          "candidate call discussion",
		MeetingCandidate:    true,
		MeetingCandidateApp: "Zoom",
	})

	app.extractSession("periodic_flush")

	if got := app.activity.buf.Len(); got != 1 {
		t.Fatalf("buffer length after periodic_flush during candidate = %d, want 1", got)
	}
}

func TestExtractSession_EmptyCandidateResetsState(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf:                         activitybuf.NewRingBuffer(16, time.Minute),
			meetingCandidateActive:      true,
			meetingCandidateAppName:     "Zoom",
			meetingCandidateStartMs:     12345,
			lastMeetingCandidateAudioMs: 23456,
		},
	}

	app.extractSession("meeting_candidate_idle_transition")

	if app.activity.meetingCandidateActive {
		t.Fatal("empty candidate extraction should clear meetingCandidateActive")
	}
	if app.activity.meetingCandidateAppName != "" {
		t.Fatalf("meetingCandidateAppName = %q, want cleared", app.activity.meetingCandidateAppName)
	}
	if app.activity.meetingCandidateStartMs != 0 || app.activity.lastMeetingCandidateAudioMs != 0 {
		t.Fatalf("candidate timestamps not cleared: start=%d last=%d",
			app.activity.meetingCandidateStartMs, app.activity.lastMeetingCandidateAudioMs)
	}
}

func TestExtractSession_PeriodicFlushPreservesMeetingStateAcrossRace(t *testing.T) {
	// The fix for the race-condition review: periodic_flush must NOT clear
	// meeting fields, even when it proceeds. Reason: the inMeeting check and
	// the meeting-state reset can't be atomic across the whole
	// extractSession (we do I/O between them). If the meeting watcher sets
	// inMeeting=true between check and reset, the reset would trample the
	// watcher's freshly-installed state. Skipping the reset for
	// periodic_flush makes the race benign.
	//
	// We can't deterministically reproduce the goroutine interleave in a
	// unit test, so assert the invariant directly: pre-seed meeting fields
	// to non-zero values (representing watcher state we shouldn't touch),
	// run periodic_flush against a non-meeting buffer, and verify the
	// meeting fields are unchanged after the critical section runs.
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
			// inMeeting=false so periodic_flush proceeds; the other meeting
			// fields stand in for state the watcher might have set.
			meetingAppName:              "Zoom",
			meetingBundleID:             "us.zoom.xos",
			meetingStartMs:              12345,
			lastMeetingEventMs:          67890,
			lastMeetingAudioMs:          98765,
			meetingCandidateActive:      true,
			meetingCandidateAppName:     "Zoom",
			meetingCandidateStartMs:     22222,
			lastMeetingCandidateAudioMs: 33333,
		},
	}
	app.activity.buf.Push(activitymon.ActivityEvent{
		Timestamp: time.Now().UnixMilli(),
		Kind:      activitymon.EventKeyboard,
		AppName:   "Cursor",
		KeyChars:  []string{"hello"},
	})

	// extractSession will eventually try am.store.BeginTx() which panics on
	// nil store. The critical section we're testing runs and unlocks before
	// that, so recover and assert post-conditions.
	func() {
		defer func() { _ = recover() }()
		app.extractSession("periodic_flush")
	}()

	am := app.activity
	if am.meetingAppName != "Zoom" {
		t.Errorf("periodic_flush cleared meetingAppName = %q, want %q (must not touch meeting state)",
			am.meetingAppName, "Zoom")
	}
	if am.meetingBundleID != "us.zoom.xos" {
		t.Errorf("periodic_flush cleared meetingBundleID = %q, want preserved", am.meetingBundleID)
	}
	if am.meetingStartMs != 12345 {
		t.Errorf("periodic_flush cleared meetingStartMs = %d, want 12345", am.meetingStartMs)
	}
	if am.lastMeetingEventMs != 67890 {
		t.Errorf("periodic_flush cleared lastMeetingEventMs = %d, want 67890", am.lastMeetingEventMs)
	}
	if am.lastMeetingAudioMs != 98765 {
		t.Errorf("periodic_flush cleared lastMeetingAudioMs = %d, want 98765", am.lastMeetingAudioMs)
	}
}

func TestExtractSession_OnlyTheCallsEndResetsMeetingState(t *testing.T) {
	// During a call only its end resets the meeting state: an idle timer
	// armed before the call is ignored (it used to clear the state while
	// the call's recording stayed attached), and meeting_ended resets it.
	app := &App{
		activity: &activityMonitorState{
			buf:                activitybuf.NewRingBuffer(16, time.Minute),
			inMeeting:          true,
			meetingAppName:     "Zoom",
			meetingBundleID:    "us.zoom.xos",
			meetingStartMs:     12345,
			lastMeetingEventMs: 67890,
			lastMeetingAudioMs: 98765,
		},
	}
	app.activity.buf.Push(activitymon.ActivityEvent{
		Timestamp: time.Now().UnixMilli(),
		Kind:      activitymon.EventKeyboard,
		AppName:   "Cursor",
		KeyChars:  []string{"hello"},
	})

	app.extractSession("idle_transition")
	am := app.activity
	if !am.inMeeting || am.meetingAppName != "Zoom" || am.meetingStartMs != 12345 || am.lastMeetingAudioMs != 98765 {
		t.Fatalf("idle_transition must not touch a call: inMeeting=%v app=%q start=%d audio=%d", am.inMeeting, am.meetingAppName, am.meetingStartMs, am.lastMeetingAudioMs)
	}
	if am.buf.Len() != 1 {
		t.Fatal("the call's events stay for the call's session")
	}

	func() {
		defer func() { _ = recover() }()
		app.extractSession("meeting_ended")
	}()
	if am.inMeeting {
		t.Error("meeting_ended did not clear inMeeting")
	}
	if am.meetingAppName != "" {
		t.Errorf("meeting_ended did not clear meetingAppName = %q", am.meetingAppName)
	}
	if am.meetingStartMs != 0 {
		t.Errorf("meeting_ended did not clear meetingStartMs = %d", am.meetingStartMs)
	}
	if am.lastMeetingAudioMs != 0 {
		t.Errorf("meeting_ended did not clear lastMeetingAudioMs = %d", am.lastMeetingAudioMs)
	}
	if am.meetingCandidateActive || am.meetingCandidateAppName != "" || am.meetingCandidateStartMs != 0 || am.lastMeetingCandidateAudioMs != 0 {
		t.Error("meeting_ended did not clear the candidate state")
	}
}

func TestOnMeetingEndedIgnoresStaleEndWhenNotInMeeting(t *testing.T) {
	app := &App{
		activity: &activityMonitorState{
			buf: activitybuf.NewRingBuffer(16, time.Minute),
		},
	}
	app.activity.buf.Push(activitymon.ActivityEvent{
		Timestamp:  time.Now().UnixMilli(),
		Kind:       activitymon.EventSpeech,
		AppName:    "iTerm2",
		SpeechText: "this should remain buffered",
	})

	app.onMeetingEnded(app.activity)

	if got := app.activity.buf.Len(); got != 1 {
		t.Fatalf("buffer length after stale meeting end = %d, want 1", got)
	}
}

func TestEnsureActivityTriagerStarted_StartsOnce(t *testing.T) {
	store, err := NewActivityStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewActivityStore() error = %v", err)
	}
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app := &App{
		ctx:             ctx,
		memoryGenerator: &MemoryGenerator{},
		activity: &activityMonitorState{
			store: store,
		},
	}

	app.ensureActivityTriagerStarted()
	if app.activity.triager == nil {
		t.Fatal("expected triager to start for initialized activity store")
	}

	first := app.activity.triager
	app.ensureActivityTriagerStarted()
	if app.activity.triager != first {
		t.Fatal("expected triager start to be idempotent")
	}
}

// The session_end final capture must wait out a transient in-flight capture
// instead of dropping the session's only guaranteed frame.
func TestBeginCaptureWait_WaitsForInFlightCapture(t *testing.T) {
	am := &activityMonitorState{captureSem: make(chan struct{}, 1)}

	// Free slot: acquires immediately.
	if !am.beginCaptureWait(100 * time.Millisecond) {
		t.Fatal("beginCaptureWait must acquire a free slot")
	}
	am.endCapture()

	// Slot held and never released: gives up after the timeout.
	if !am.tryBeginCapture() {
		t.Fatal("tryBeginCapture on empty sem")
	}
	if am.beginCaptureWait(50 * time.Millisecond) {
		t.Fatal("beginCaptureWait must time out while the slot is held")
	}

	// Slot released mid-wait: acquires.
	done := make(chan bool, 1)
	go func() { done <- am.beginCaptureWait(2 * time.Second) }()
	time.Sleep(20 * time.Millisecond)
	am.endCapture()
	if !<-done {
		t.Fatal("beginCaptureWait must acquire once the in-flight capture ends")
	}
	am.endCapture()
}

// A session_end frame passes busyWait > 0 so a still-running PII pass delays
// it instead of dropping it; every other trigger keeps the non-blocking drop.
func TestRunAsyncPIIMasking_BusyWait(t *testing.T) {
	am := &activityMonitorState{piiSem: make(chan struct{}, 1)}
	now := time.Now()
	discard := func() bool { return true } // frames discard immediately; no OCR engine needed

	// Occupy the single PII slot.
	am.piiSem <- struct{}{}

	// Non-blocking callers (every non-final trigger) drop on busy.
	if got := am.runAsyncPIIMasking("p", []byte{1}, 80, now, 0, 0, discard, nil); got != piiMaskBusy {
		t.Fatalf("non-blocking launch on busy sem = %v, want piiMaskBusy", got)
	}

	// A waiting caller acquires once the in-flight pass finishes.
	launched := make(chan piiMaskLaunchResult, 1)
	go func() {
		launched <- am.runAsyncPIIMasking("p", []byte{1}, 80, now, 0, 2*time.Second, discard, nil)
	}()
	time.Sleep(20 * time.Millisecond)
	<-am.piiSem // in-flight pass completes
	if got := <-launched; got != piiMaskLaunched {
		t.Fatalf("waiting launch = %v, want piiMaskLaunched", got)
	}

	// And a waiting caller still gives up after the timeout.
	am.piiSem <- struct{}{}
	if got := am.runAsyncPIIMasking("p", []byte{1}, 80, now, 0, 30*time.Millisecond, discard, nil); got != piiMaskBusy {
		t.Fatalf("waiting launch on stuck sem = %v, want piiMaskBusy", got)
	}
}

// A frame is dropped when focus moved between sampling the frontmost app and
// capturing its window or walking its accessibility tree: every privacy
// check of the frame keys on the first sample.
func TestCaptureFocusStable(t *testing.T) {
	vscode := frontmostSample{bundleID: "com.microsoft.VSCode", pid: 100}
	cases := []struct {
		name           string
		capture, later frontmostSample
		checkable      bool
		want           bool
	}{
		{"same app", vscode, vscode, true, true},
		{"password manager took focus", vscode, frontmostSample{bundleID: "com.1password.1password", pid: 200}, true, false},
		{"two apps without a bundle id", frontmostSample{pid: 300}, frontmostSample{pid: 301}, true, false},
		{"same app without a bundle id", frontmostSample{pid: 300}, frontmostSample{pid: 300}, true, true},
		{"pid unknown on one side", vscode, frontmostSample{bundleID: "com.microsoft.VSCode"}, true, false},
		{"pid unknown on both sides", frontmostSample{bundleID: "com.microsoft.VSCode"}, frontmostSample{bundleID: "com.microsoft.VSCode"}, true, true},
		{"bundle-less app replaced by a bundled one", frontmostSample{pid: 300}, frontmostSample{bundleID: "com.1password.1password", pid: 200}, true, false},
		{"platform cannot check", vscode, frontmostSample{}, false, true},
	}
	for _, c := range cases {
		if got := captureFocusStable(c.capture, c.later, c.checkable); got != c.want {
			t.Errorf("%s: captureFocusStable = %v, want %v", c.name, got, c.want)
		}
	}
}

func hearComputerOutsideCalls(t *testing.T) {
	t.Helper()
	t.Setenv(agentHomeEnv, t.TempDir())
	if err := updateConfig(func(cfg *yansuConfig) {
		cfg.BackgroundListeningConfigured = true
		cfg.BackgroundSystemAudioOutsideCalls = true
	}); err != nil {
		t.Fatal(err)
	}
}
