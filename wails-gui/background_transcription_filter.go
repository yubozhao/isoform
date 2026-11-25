package main

import (
	"strings"
	"time"

	"wails-gui/internal/meetingdetect"
)

// shouldTranscribeBackgroundAudioChunk is the product-level privacy gate for
// continuous background audio. During a call (in the recording generation
// that is current) both sources are transcribed and recorded. Outside calls
// the microphone is transcribed all day and the computer's own sound only
// with its own switch (background_listening_outside_calls.go); nothing
// outside a call is recorded, since only call chunks reach a recording.
func (a *App) shouldTranscribeBackgroundAudioChunk(chunk capturedAudioChunk) bool {
	if a == nil || a.IsClosedEyesModeEnabled() || a.isBackgroundListeningScreenLocked() {
		return false
	}
	if chunk.FocusUnavailable || chunk.CapturedApp.FocusUnavailable {
		return false
	}
	cfg := readAudioConfig()
	if !cfg.allowsSource(chunk.Source) {
		return false
	}
	if a.chunkCapturedInCall(chunk) {
		return true
	}
	// Outside calls, nothing said while an app the user listed under "Apps
	// Isoform does not listen to" was in front or held the microphone, or
	// while a private browser window was in front, becomes text: the chunk
	// never reaches speech recognition, so no words are logged and no voice
	// print is taken. Safari does not mark its private windows (their title
	// is the page's), so speech in one does reach speech recognition; the
	// Listen copy says so. A call is recorded whole, whatever app is in front; a
	// listed call app's calls never start (userListedAppMatches in the call
	// checks).
	if chunk.ListedAppInFront || chunk.PrivateWindowInFront || userListedAppMatches(chunk.CapturedApp.BundleID) ||
		chunk.MicHolders.listedAppHolds() || privateBrowserWindow(chunk.CapturedApp) {
		return false
	}
	return cfg.listenOutsideCalls(chunk.Source)
}

// chunkCapturedInCall reports whether a chunk belongs to a call in the
// current recording generation, as annotateBackgroundAudioChunk decided at
// capture time.
func (a *App) chunkCapturedInCall(chunk capturedAudioChunk) bool {
	if !a.automaticMeetingRecordingEpochIsCurrent(chunk.MeetingRecordingEpoch) {
		return false
	}
	if chunk.CapturedInMeeting && meetingdetect.IsTranscriptionSupportedMeetingName(chunk.CapturedMeetingAppName) {
		return true
	}
	return chunk.CapturedMeetingCandidate && meetingdetect.IsTranscriptionSupportedMeetingName(chunk.CandidateMeetingAppName)
}

// annotateBackgroundAudioChunk snapshots meeting context at capture time. ASR
// can lag by 5-30s, so delayed transcripts should use this captured identity
// instead of whatever app/meeting state happens to be live when Whisper returns.
func (a *App) annotateBackgroundAudioChunk(chunk capturedAudioChunk) capturedAudioChunk {
	if a == nil || a.IsClosedEyesModeEnabled() || a.isBackgroundListeningScreenLocked() {
		return chunk
	}
	if !chunk.CapturedAt.IsZero() {
		end := chunk.CapturedAt.UnixMilli()
		chunk.ProtectedAppInFront, chunk.ListedAppInFront, chunk.PrivateWindowInFront = a.captureSpans.during(end-chunk.Duration.Milliseconds(), end)
		chunk.FocusUnavailable = chunk.FocusUnavailable || a.captureSpans.focusUnavailableDuring(end-chunk.Duration.Milliseconds(), end)
	}
	epoch, enabled := a.automaticMeetingRecordingEpoch()
	if !enabled {
		return chunk
	}
	chunk.MeetingRecordingEpoch = epoch
	if a.activity == nil {
		return chunk
	}

	a.activity.mu.Lock()
	defer a.activity.mu.Unlock()
	if !a.automaticMeetingRecordingEpochIsCurrent(epoch) {
		return chunk
	}

	if a.activity.inMeeting && a.activity.meetingRecordingEpoch == epoch {
		// Only while the call's own app holds the microphone: once it lets
		// go, what follows (dictation, a video) is not the call.
		if !meetingdetect.IsTranscriptionSupportedMeetingName(a.activity.meetingAppName) || !a.callChunkOnMicrophoneLocked(chunk) {
			return chunk
		}
		chunk.CapturedInMeeting = true
		chunk.CapturedMeetingAppName = a.activity.meetingAppName
		chunk.CapturedMeetingBundleID = a.activity.meetingBundleID
		return chunk
	}

	if a.activity.inMeeting {
		return chunk
	}
	if appName, reason, ok := a.meetingCandidateForChunkLocked(chunk); ok {
		chunk.CapturedMeetingCandidate = true
		chunk.CandidateMeetingAppName = appName
		chunk.CandidateMeetingReason = reason
		a.noteMeetingCandidateAudioLocked(chunk, appName)
	}
	return chunk
}

func (a *App) meetingCandidateForChunkLocked(chunk capturedAudioChunk) (string, string, bool) {
	if !a.automaticMeetingRecordingEpochIsCurrent(chunk.MeetingRecordingEpoch) {
		return "", "", false
	}
	if a.activity.meetingCandidateActive &&
		a.activity.meetingCandidateRecordingEpoch == chunk.MeetingRecordingEpoch &&
		meetingdetect.IsTranscriptionSupportedMeetingName(a.activity.meetingCandidateAppName) &&
		micHeldByMeeting(chunk, a.activity.meetingCandidateAppName, "") {
		a.activity.meetingCandidateFrontmostStreak = 0
		return a.activity.meetingCandidateAppName, "candidate_continuation_audio", true
	}

	// A call in front (Zoom's "Zoom Meeting" window, a Google Meet tab by
	// its address) with its app on the microphone: the call the watcher
	// confirms within a poll or two. Recording starts now, so the call's
	// first seconds are in it and not split off.
	if name := frontCallAtCapture(chunk); name != "" {
		a.activity.meetingCandidateFrontmostStreak = 0
		return name, "meeting_window_in_front", true
	}

	// The meeting app itself holds the microphone and shows a call window.
	if meetingdetect.IsTranscriptionSupportedMeetingName(chunk.MeetingCallOnMic) {
		a.activity.meetingCandidateFrontmostStreak = 0
		return chunk.MeetingCallOnMic, "meeting_app_in_call", true
	}

	// Where the platform cannot name the processes on the microphone: the
	// meeting app in front, not on its idle launcher window, and another
	// process reading the microphone on two microphone chunks in a row. A
	// meeting app merely running in the background is not enough: that
	// matched dictation into Claude or ChatGPT while Zoom sat idle in the
	// menu bar, and recorded it as a Zoom call. Where the holders are named,
	// the meeting app not among them means someone else (dictation, Siri)
	// has the microphone, so this path never starts a call there.
	if chunk.Source != backgroundListeningSourceMicrophone {
		return "", "", false
	}
	result := meetingdetect.TranscriptionSupportedNativeMeetingForBundle(chunk.CapturedApp.BundleID)
	if !chunk.OtherMicUser || len(chunk.MicHolders.BundleIDs) > 0 || !result.IsMeeting || isMeetingIdleWindow(chunk.CapturedApp.WindowTitle) || userListedAppMatches(chunk.CapturedApp.BundleID) {
		a.activity.meetingCandidateFrontmostStreak = 0
		return "", "", false
	}
	a.activity.meetingCandidateFrontmostStreak++
	if a.activity.meetingCandidateFrontmostStreak < meetingCandidateFrontmostChunks {
		return "", "", false
	}
	return result.MeetingName, "native_supported_app_frontmost_with_external_mic", true
}

// meetingCandidateFrontmostChunks is how many microphone chunks in a row
// must show the frontmost-meeting-app evidence before a call starts.
const meetingCandidateFrontmostChunks = 2

// meetingIdleWindowTitles are the launcher windows a meeting app shows when
// no call is running.
var meetingIdleWindowTitles = []string{"zoom workplace", "zoom 工作区", "zoom", "microsoft teams"}

func isMeetingIdleWindow(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	if lower == "" {
		return true
	}
	for _, idle := range meetingIdleWindowTitles {
		if lower == idle {
			return true
		}
	}
	return isMeetingSettingsWindow(title)
}

func (a *App) noteMeetingCandidateAudioLocked(chunk capturedAudioChunk, appName string) {
	if !a.automaticMeetingRecordingEpochIsCurrent(chunk.MeetingRecordingEpoch) || !meetingdetect.IsTranscriptionSupportedMeetingName(appName) {
		return
	}

	ts := time.Now().UnixMilli()
	if !chunk.CapturedAt.IsZero() {
		ts = chunk.CapturedAt.UnixMilli()
	}
	if !a.activity.meetingCandidateActive {
		a.activity.meetingCandidateActive = true
		a.activity.meetingCandidateAppName = appName
		a.activity.meetingCandidateStartMs = ts
		a.activity.meetingCandidateRecordingEpoch = chunk.MeetingRecordingEpoch
	} else if a.activity.meetingCandidateRecordingEpoch != chunk.MeetingRecordingEpoch {
		return
	}
	a.activity.lastMeetingCandidateAudioMs = ts
	a.armActivityIdleTimerLocked(a.activity, "meeting_candidate_idle_transition")
}

// frontCallAtCapture is the supported call the app in front showed when the
// chunk was captured, with that app on the microphone: a browser by its
// tab's address (looked up in the capture loop), any other app by its
// window title.
func frontCallAtCapture(chunk capturedAudioChunk) string {
	if !chunk.MicHolders.couldHold(chunk.CapturedApp.BundleID) || userListedAppMatches(chunk.CapturedApp.BundleID) {
		return ""
	}
	if meetingdetect.IsBrowserBundle(chunk.CapturedApp.BundleID) {
		if browserWindowOpenToCalls(chunk.CapturedApp) && meetingdetect.IsTranscriptionSupportedMeetingName(chunk.BrowserMeeting) {
			return chunk.BrowserMeeting
		}
		return ""
	}
	return meetingdetect.IsTranscriptionSupportedMeetingEvent(chunk.CapturedApp.BundleID, chunk.CapturedApp.WindowTitle).MeetingName
}
