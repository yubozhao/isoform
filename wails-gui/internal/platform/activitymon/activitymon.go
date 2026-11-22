package activitymon

import "context"

// EventKind identifies the type of user activity event.
type EventKind string

const (
	EventClick     EventKind = "click"
	EventAppSwitch EventKind = "app_switch"
	EventFocus     EventKind = "focus"
	EventKeyboard  EventKind = "keyboard"
	EventSpeech    EventKind = "speech"

	// EventCalendarPeek records one read of a web calendar through the
	// browser (wails-gui/calendar_peek.go), so it shows in the Activity log.
	EventCalendarPeek EventKind = "calendar_peek"
	// EventDeviceInUse records the microphone or camera starting or stopping
	// in any app; EventSessionState records lock, unlock, sleep, wake, idle
	// and power changes. Both come from the context signals, need no
	// permission, and carry their detail in StateName, Device and InUse.
	EventDeviceInUse  EventKind = "device_in_use"
	EventSessionState EventKind = "session_state"
	// EventClipboard records one copy (wails-gui/clipboard_observer.go): its
	// kind, length and a scrubbed preview in the Clipboard fields. Copies
	// marked concealed or transient, from password managers, protected apps
	// or private browser windows are never recorded.
	EventClipboard EventKind = "clipboard"

	// TypedTextCaptureMethodAXFieldDiff means TypedText was recovered by
	// diffing focused-field accessibility values at the edges of one typing
	// segment. It is the only capture method currently eligible to become a
	// source-backed user result.
	TypedTextCaptureMethodAXFieldDiff = "ax_field_diff"
)

// AppInfo holds the frontmost application's identity at a point in time.
type AppInfo struct {
	// FocusUnavailable is an observation failure, never evidence of safe capture.
	FocusUnavailable bool
	BundleID         string
	AppName          string
	WindowTitle      string
}

// FrontmostWindow is the frontmost app's main window as picked for
// per-window capture, with the identity of the app it was picked for, sampled
// together with the window.
type FrontmostWindow struct {
	ID        uint32 // CGWindowID (macOS) or HWND (Windows); 0 when there is none
	OwnerName string // owner app name from the window list
	BundleID  string // bundle id of the app (macOS only; empty elsewhere)
	PID       int    // process id of the app; 0 when unknown
}

// WindowBounds holds normalized (0-1) window coordinates on screen.
type WindowBounds struct {
	X, Y, W, H float64
}

// ActivityEvent represents a single user interaction captured by the monitor.
type ActivityEvent struct {
	Timestamp   int64     `json:"timestamp"` // Unix ms
	Kind        EventKind `json:"kind"`
	AppBundleID string    `json:"appBundleId"`
	AppName     string    `json:"appName"`
	WindowTitle string    `json:"windowTitle"`
	// PrivateWindow preserves the exclusion after private capture removes text.
	PrivateWindow bool `json:"privateWindow,omitempty"`

	// Click fields
	ClickX     float64 `json:"clickX,omitempty"`
	ClickY     float64 `json:"clickY,omitempty"`
	ClickRole  string  `json:"clickRole,omitempty"`  // AX role of clicked element (e.g. AXButton)
	ClickLabel string  `json:"clickLabel,omitempty"` // title/description of clicked element

	// Keyboard fields
	KeyCode   uint16   `json:"keyCode,omitempty"`
	Modifiers uint64   `json:"modifiers,omitempty"`
	KeyChar   string   `json:"keyChar,omitempty"`  // legacy: single typed character (kept for backward-compat with stored data)
	KeyChars  []string `json:"keyChars,omitempty"` // typed characters (each element is one keystroke; empty in sensitive areas)

	// TypedText is the text the user actually committed during this keyboard
	// burst, recovered by diffing focused-field AX snapshots across the
	// burst. Under a composing input method (Chinese pinyin etc.) this holds
	// the composed characters — KeyChars then holds raw pre-composition
	// keystrokes, flagged by IMEComposing, and must not be read as literal
	// text. Empty when the focused element exposed no readable text value
	// (terminals, secure fields, canvas editors).
	TypedText              string `json:"typedText,omitempty"`
	TypedTextCaptureMethod string `json:"typedTextCaptureMethod,omitempty"`
	// IMEComposing is true when a composing input method was the active
	// input source while this burst was typed.
	IMEComposing bool `json:"imeComposing,omitempty"`
	// FieldText / FieldTextValid carry the focused-field snapshot taken at
	// this keydown (burst-start, commit-key, and recent-typing click events
	// only). Transient inputs for the typed-text differ — never persisted.
	FieldText      string `json:"-"`
	FieldTextValid bool   `json:"-"`

	// Focus fields
	FocusRole  string `json:"focusRole,omitempty"` // AXTextField, AXSearchField, etc.
	FocusLabel string `json:"focusLabel,omitempty"`

	// Speech fields (background audio transcription)
	SpeechText string `json:"speechText,omitempty"`
	// SpeechEndAt is the wall-clock ASR segment end in milliseconds, when known.
	SpeechEndAt int64 `json:"speechEndAt,omitempty"`
	// SpeechSource identifies the audio source that produced this
	// transcription — "microphone" = the local user's voice, "system_audio"
	// = remote speakers picked up from the Mac's output (e.g. Zoom / Teams
	// audio). Empty for events persisted before speaker diarization
	// landed; transcript rendering treats that as unknown and omits the
	// speaker prefix.
	SpeechSource string `json:"speechSource,omitempty"`

	// SpeechLikelyEcho is true when this microphone-source transcript was
	// captured while the Mac's speakers were also playing audible content
	// (detected via the system_audio tap). Such transcripts are very likely
	// acoustic echo of remote audio bleeding into the mic rather than the
	// user actually speaking, so downstream summarization should discount
	// them. Always false for system_audio events and for microphone events
	// captured when the speakers were silent.
	SpeechLikelyEcho bool `json:"speechLikelyEcho,omitempty"`

	// SpeechInCall is true when the audio was captured during a call (a
	// confirmed meeting or a candidate call). Only such speech can make a
	// session a meeting; speech outside calls is ordinary activity.
	SpeechInCall bool `json:"speechInCall,omitempty"`

	// SpeechDictationApp names the app the user was dictating into when
	// this microphone speech was captured (Claude, Codex, ChatGPT, a
	// voice-typing helper): the words went to that app, not to a person.
	SpeechDictationApp string `json:"speechDictationApp,omitempty"`

	// SpeechSpeakerVerified records the result of voice-fingerprint
	// matching for microphone-source transcripts:
	//
	//   nil   — verification didn't run (no enrolled voice samples,
	//           speaker model not loaded, system_audio source, error)
	//   true  — chunk embedding matched an enrolled Self voice sample
	//           above the configured threshold
	//   false — chunk embedding did NOT match any enrolled Self sample;
	//           the speaker is almost certainly someone else (co-located
	//           coworker, family member) using the same mic
	//
	// Consumed by subject_attribution.isLikelyNonUserSpeech as a strong
	// signal — a false here promotes the chunk to received content mode,
	// closing the "in-person counterparty" gap that bundle-ID + echo-
	// detection alone couldn't cover.
	SpeechSpeakerVerified *bool `json:"speechSpeakerVerified,omitempty"`

	// SpeechSpeakerLabel names who spoke within one recording that was split
	// by speaker (a recording from the watch): "you" for the enrolled owner,
	// "speaker_1", "speaker_2" and so on for other voices in the order they
	// were first heard. Empty when the speech was not split, the stretch was
	// too short to tell, or no voice model is installed. Without an
	// enrollment nobody is "you": every voice is a numbered speaker.
	SpeechSpeakerLabel string `json:"speechSpeakerLabel,omitempty"`

	// Transient fingerprint from microphone verification (~192-512 floats).
	// Never serialize biometric bytes through general Activity DTOs or event
	// persistence. Named call signatures have their own local-only table and
	// explicit erasure path. Nil when verification did not run.
	SpeechSpeakerEmbedding []float32 `json:"-"`

	// MeetingCandidate is set when speech was captured in the conservative
	// fallback path: no explicit meeting was detected, but supported meeting-app
	// and audio evidence were strong enough to keep the transcript together for
	// holistic classification at session end.
	MeetingCandidate       bool   `json:"meetingCandidate,omitempty"`
	MeetingCandidateApp    string `json:"meetingCandidateApp,omitempty"`
	MeetingCandidateReason string `json:"meetingCandidateReason,omitempty"`
	// MeetingRecordingEpoch is transient process-local provenance for
	// automatic meeting classification. It intentionally is not persisted.
	MeetingRecordingEpoch uint64 `json:"-"`

	// Detail is a short plain sentence for the log; the Activity log renders
	// these events from the structured fields below through i18n, never
	// from Detail.
	Detail string `json:"detail,omitempty"`
	// Provider and Count describe an EventCalendarPeek: the web calendar
	// read ("google", "outlook") and how many events it showed.
	Provider string `json:"provider,omitempty"`
	Count    int    `json:"count,omitempty"`
	// StateName is the session state an EventSessionState reports: locked,
	// unlocked, sleep, wake, idle, active, battery, power.
	StateName string `json:"stateName,omitempty"`
	// Device and InUse describe an EventDeviceInUse: "microphone" or
	// "camera", and whether it just started (true) or stopped (false).
	Device string `json:"device,omitempty"`
	InUse  bool   `json:"inUse,omitempty"`
	// ClipboardKind, ClipboardLength and ClipboardPreview describe an
	// EventClipboard: text, url, file, image or other; the copied text's
	// length in characters (0 when macOS did not let Isoform read it); and at
	// most 160 characters with personal details removed (empty for a
	// password-like text or when the content could not be read).
	ClipboardKind    string `json:"clipboardKind,omitempty"`
	ClipboardLength  int    `json:"clipboardLength,omitempty"`
	ClipboardPreview string `json:"clipboardPreview,omitempty"`
}

// GetKeyChars returns the structured key list. For events persisted before
// the KeyChars field was introduced, it falls back to the legacy KeyChar
// string (which may contain concatenated keys without boundaries).
func (e ActivityEvent) GetKeyChars() []string {
	if len(e.KeyChars) > 0 {
		return e.KeyChars
	}
	if e.KeyChar != "" {
		return []string{e.KeyChar}
	}
	return nil
}

// Monitor captures user activity events from the operating system.
type Monitor interface {
	Start(ctx context.Context, callback func(ActivityEvent)) error
	Stop()
	IsRunning() bool
}

// Stats is a snapshot of internal health counters from the event pipeline.
// All values are cumulative since process start except RingPeakDepth which
// is a high-water mark. Returned by GetStats(); safe to call concurrently
// and cheap enough to poll from a metrics panel. Fields not applicable to
// a given platform (e.g. ring buffer on non-darwin) are zero.
type Stats struct {
	EventsDrained uint64 `json:"events_drained"`
	Wakeups       uint64 `json:"wakeups"`
	RingPeakDepth int    `json:"ring_peak_depth"`
	RingDrops     uint64 `json:"ring_drops"`
}

// ScreenCapture captures the screen as a JPEG snapshot.
type ScreenCapture interface {
	// CaptureRaw takes a screenshot and returns raw RGBA pixels.
	CaptureRaw() (rgbaPixels []byte, width, height int, err error)

	// EncodeJPEG encodes raw RGBA pixels to JPEG.
	EncodeJPEG(rgbaPixels []byte, width, height, quality int) ([]byte, error)
}
