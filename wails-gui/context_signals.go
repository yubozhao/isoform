package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/internal/meetingdetect"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/storage"
)

// Context signals need no permission and make the calendar signals reliable:
// whether the microphone or camera is in use (a third meeting-detection
// signal), and whether the Mac is locked, asleep, idle or on battery (so
// engagement holds non-urgent deliveries while locked, the calendar is read
// again on wake, and the brief knows when the day started, paused and ended).
const (
	contextDevicePoll = 5 * time.Second
	contextIdlePoll   = 30 * time.Second
	contextPowerPoll  = time.Minute
	// contextIdleAway is how long without input counts as away for the
	// session_state events; contextIdleBreak is how long counts as a break
	// in the day shape.
	contextIdleAway  = 5 * time.Minute
	contextIdleBreak = 15 * time.Minute
	// contextPresentWithin is how recent the last input must be for the day
	// marks to call the user present.
	contextPresentWithin = time.Minute

	contextDayMarksKeyPrefix = "context_day:"
)

// contextState is what the service knows right now.
type contextState struct {
	Locked      bool
	Asleep      bool
	Away        bool
	MicInUse    bool
	CameraInUse bool
	Idle        time.Duration
	LastWake    time.Time
	LastUnlock  time.Time
	Power       activitymon.PowerState
	ObservedAt  time.Time
}

// contextBreak is one stretch away from the Mac inside a day.
type contextBreak struct {
	StartAt int64 `json:"startAt"`
	EndAt   int64 `json:"endAt"`
}

// contextDayMarks records the day's presence and breaks. FirstActive and
// LastActive include wake/unlock for the existing day shape; the additive
// input marks are exclusively observed keyboard/pointer input timestamps.
type contextDayMarks struct {
	Date        string         `json:"date"`
	FirstActive int64          `json:"firstActive"`
	LastActive  int64          `json:"lastActive"`
	FirstInput  int64          `json:"firstInput,omitempty"`
	LastInput   int64          `json:"lastInput,omitempty"`
	Breaks      []contextBreak `json:"breaks"`
	// openBreak is the start of a break still running; not stored.
	openBreak int64
}

// BreakMinutes sums the closed breaks.

// systemStateEvent carries "locked", "unlocked", "sleep" or "wake" to the
// window, never anything else.
const systemStateEvent = "system:state"

func (m contextDayMarks) BreakMinutes() int {
	total := int64(0)
	for _, b := range m.Breaks {
		if b.EndAt > b.StartAt {
			total += b.EndAt - b.StartAt
		}
	}
	return int(total / 60000)
}

// applyActivityMark records that the user was present (active) or away at
// now. A new local day resets the marks. It reports whether anything changed.
func applyActivityMark(marks *contextDayMarks, now time.Time, active bool, awaySince time.Time, loc *time.Location) bool {
	if loc == nil {
		loc = time.UTC
	}
	date := now.In(loc).Format("2006-01-02")
	changed := false
	if marks.Date != date {
		*marks = contextDayMarks{Date: date}
		changed = true
	}
	ms := now.UnixMilli()
	if active {
		if marks.FirstActive == 0 {
			marks.FirstActive = ms
			changed = true
		}
		if ms > marks.LastActive {
			marks.LastActive = ms
			changed = true
		}
		if marks.openBreak != 0 {
			if ms > marks.openBreak {
				marks.Breaks = append(marks.Breaks, contextBreak{StartAt: marks.openBreak, EndAt: ms})
			}
			marks.openBreak = 0
			changed = true
		}
		return changed
	}
	if marks.openBreak == 0 {
		start := ms
		if !awaySince.IsZero() && awaySince.Before(now) {
			start = awaySince.UnixMilli()
		}
		if marks.LastActive > 0 && start < marks.LastActive {
			start = marks.LastActive
		}
		marks.openBreak = start
		changed = true
	}
	return changed
}

// applyInputMark runs only from a present idle poll, after applyActivityMark
// has selected the current day. Wake/unlock callbacks never call it. Using
// now-idle keeps repeated polls of the same input from extending work.
func applyInputMark(marks *contextDayMarks, now time.Time, idle time.Duration, loc *time.Location) bool {
	if idle < 0 || idle >= contextPresentWithin {
		return false
	}
	if loc == nil {
		loc = time.UTC
	}
	date := now.In(loc).Format("2006-01-02")
	inputAt := now.Add(-idle)
	if marks.Date != date || inputAt.In(loc).Format("2006-01-02") != date {
		return false
	}
	ms := inputAt.UnixMilli()
	changed := false
	if marks.FirstInput == 0 {
		marks.FirstInput = ms
		changed = true
	}
	if ms > marks.LastInput {
		marks.LastInput = ms
		changed = true
	}
	return changed
}

// meetingSignalFromDevices is the third meeting signal: the microphone or the
// camera is in use while a calendar event with a conference link is live,
// and the app in front is that link's platform (its native app, or a browser
// for a web meeting). Isoform's own listening also opens the microphone, so
// the microphone alone counts only while that is off. A FaceTime call or a
// dictation during an unrelated event never qualifies. It returns the
// meeting app to confirm, or false.
func meetingSignalFromDevices(micInUse, cameraInUse, ownListening bool, live *calendar.Event, running []activitymon.RunningApp) (meetingdetect.Result, bool) {
	if live == nil || !live.IsMeetingLike() || live.ConferenceHost == "" {
		return meetingdetect.Result{}, false
	}
	if !meetingdetect.IsTranscriptionSupportedMeetingName(live.ConferenceHost) {
		return meetingdetect.Result{}, false
	}
	if !cameraInUse && (!micInUse || ownListening) {
		return meetingdetect.Result{}, false
	}
	for _, app := range running {
		if !app.IsActive {
			continue
		}
		if result := meetingdetect.TranscriptionSupportedNativeMeetingForBundle(app.BundleID); result.IsMeeting && result.MeetingName == live.ConferenceHost {
			result.AppName = app.Name
			return result, true
		}
		if meetingdetect.IsBrowserBundle(app.BundleID) {
			return meetingdetect.Result{IsMeeting: true, MeetingName: live.ConferenceHost, BundleID: app.BundleID, AppName: app.Name}, true
		}
	}
	return meetingdetect.Result{}, false
}

// contextSignalService polls the device, idle and power probes, listens for
// lock, unlock, sleep and wake, and keeps the state and the day marks.
type contextSignalService struct {
	store *storage.Store
	now   func() time.Time
	loc   *time.Location

	idle func() time.Duration
	// Unsupported platforms report zero idle as a presence fallback, which
	// must never be promoted into an observation of actual input.
	inputTimingSupported bool
	mic                  func() bool
	camera               func() bool
	power                func() activitymon.PowerState
	locked               func() bool
	running              func() []activitymon.RunningApp
	changes              <-chan activitymon.SystemStateChange

	// emit records an activity event; onWake runs after a wake or unlock;
	// onMeeting confirms a meeting the devices and the calendar agree on;
	// liveEvent and ownListening are read when a device turns on.
	emit          func(activitymon.ActivityEvent)
	onWake        func()
	onSystemState func(activitymon.SystemStateChange)
	onMeeting     func(meetingdetect.Result)
	liveEvent     func(now time.Time) *calendar.Event
	ownListening  func() bool
	observing     func() bool

	mu                   sync.Mutex
	state                contextState
	marks                contextDayMarks
	deviceStatesObserved bool
	cancel               context.CancelFunc
	wg                   sync.WaitGroup
}

func newContextSignalService(a *App) *contextSignalService {
	svc := &contextSignalService{
		store:                a.store,
		now:                  time.Now,
		loc:                  time.Local,
		idle:                 activitymon.IdleTime,
		inputTimingSupported: runtime.GOOS == "darwin",
		mic:                  activitymon.MicrophoneInUse,
		camera:               activitymon.CameraInUse,
		power:                activitymon.PowerSource,
		locked:               activitymon.IsScreenLocked,
		running:              activitymon.ListRunningApps,
		changes:              activitymon.SystemStateChanges(),
		emit:                 a.recordSystemActivityEvent,
		onWake: func() {
			a.markMainChatWaitingDirty("wake")
			if a.calendarSync != nil {
				a.calendarSync.RequestSync("wake")
			}
			if a.remindersSync != nil {
				a.remindersSync.RequestSync("wake")
			}
			if a.filesActivity != nil {
				a.filesActivity.RequestSync("wake")
			}
			if a.mailSync != nil {
				a.mailSync.RequestSync("wake")
			}
			// A recording cut short by sleep is made again; a bound one is
			// left as it is (backfillTodayAudio).
			if service, ok := a.mobileBriefSource().(*mobileBriefService); ok && service != nil {
				service.remakeAudioSoon()
			}
		},
		onSystemState: func(change activitymon.SystemStateChange) {
			// The window does no work while the Mac is locked or asleep
			// (frontend lib/app-activity.ts); it catches up on unlock.
			a.emitBrowserEvent(systemStateEvent, string(change.Kind))
			if change.Kind == activitymon.SystemStateWake {
				a.checkObservationDeadline()
				a.mobileRelayRecovered()
			}
			a.mobileBriefSystemStateChanged(change)
		},
		onMeeting: func(result meetingdetect.Result) {
			if a.activity != nil && a.meetingRecordingOn() {
				a.onMeetingStarted(a.activity, result)
			}
		},
		liveEvent: func(now time.Time) *calendar.Event {
			events, err := a.listCalendarEvents(context.Background(), now.Add(-2*time.Hour), now.Add(time.Hour))
			if err != nil {
				return nil
			}
			return calendar.LiveEvent(events, now)
		},
		ownListening: a.isBackgroundListeningRunning,
		observing:    func() bool { return !a.IsClosedEyesModeEnabled() },
	}
	return svc
}

// Start installs the observers and runs the polls until ctx ends.
func (s *contextSignalService) Start(parent context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.mu.Unlock()
	activitymon.StartSystemStateObservers()
	s.wg.Add(1)
	go s.loop(ctx)
}

func (s *contextSignalService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

func (s *contextSignalService) loop(ctx context.Context) {
	defer s.wg.Done()
	s.mu.Lock()
	s.state.Locked = s.locked()
	s.state.Power = s.power()
	s.state.MicInUse, s.state.CameraInUse = s.mic(), s.camera()
	s.state.ObservedAt = s.now()
	s.deviceStatesObserved = false
	s.mu.Unlock()
	s.pollDevices()
	s.loadMarks(ctx)
	s.pollIdle(ctx)
	devices := time.NewTicker(contextDevicePoll)
	defer devices.Stop()
	idle := time.NewTicker(contextIdlePoll)
	defer idle.Stop()
	power := time.NewTicker(contextPowerPoll)
	defer power.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case change := <-s.changes:
			s.handleChange(ctx, change)
		case <-devices.C:
			s.pollDevices()
		case <-idle.C:
			s.pollIdle(ctx)
		case <-power.C:
			s.pollPower()
		}
	}
}

// handleChange applies a lock, unlock, sleep or wake.
func (s *contextSignalService) handleChange(ctx context.Context, change activitymon.SystemStateChange) {
	now := change.At
	if now.IsZero() {
		now = s.now()
	}
	detail := ""
	s.mu.Lock()
	switch change.Kind {
	case activitymon.SystemStateLocked:
		s.state.Locked = true
		detail = "Screen locked"
	case activitymon.SystemStateUnlocked:
		s.state.Locked = false
		s.state.LastUnlock = now
		detail = "Screen unlocked"
	case activitymon.SystemStateSleep:
		s.state.Asleep = true
		detail = "Your computer went to sleep"
	case activitymon.SystemStateWake:
		s.state.Asleep = false
		s.state.LastWake = now
		detail = "Your computer woke up"
	default:
		s.mu.Unlock()
		return
	}
	active := change.Kind == activitymon.SystemStateUnlocked || change.Kind == activitymon.SystemStateWake
	changed := applyActivityMark(&s.marks, now, active, time.Time{}, s.loc)
	marks := s.marks
	s.mu.Unlock()
	if s.onSystemState != nil {
		change.At = now
		s.onSystemState(change)
	}
	if s.emit != nil {
		s.emit(activitymon.ActivityEvent{Timestamp: now.UnixMilli(), Kind: activitymon.EventSessionState, StateName: string(change.Kind), Detail: detail})
	}
	if changed {
		s.saveMarks(ctx, marks)
	}
	if active && s.onWake != nil {
		s.onWake()
	}
}

// pollDevices notices the microphone or camera starting or stopping.
func (s *contextSignalService) pollDevices() {
	now := s.now()
	mic, camera := s.mic(), s.camera()
	observing := s.observing == nil || s.observing()
	s.mu.Lock()
	prevMic, prevCamera := s.state.MicInUse, s.state.CameraInUse
	s.state.MicInUse, s.state.CameraInUse = mic, camera
	s.state.ObservedAt = now
	snapshot := observing && !s.deviceStatesObserved
	s.deviceStatesObserved = observing
	s.mu.Unlock()
	if snapshot {
		// Transitions alone cannot tell a later reader whether a device was
		// already in use at startup or changed while observing was paused.
		s.emitDeviceSnapshot(now, "microphone", mic)
		s.emitDeviceSnapshot(now, "camera", camera)
	} else {
		if mic != prevMic {
			s.emitDevice(now, "microphone", mic)
		}
		if camera != prevCamera {
			s.emitDevice(now, "camera", camera)
		}
	}
	turnedOn := (mic && !prevMic) || (camera && !prevCamera)
	if !turnedOn || s.liveEvent == nil || s.onMeeting == nil {
		return
	}
	own := false
	if s.ownListening != nil {
		own = s.ownListening()
	}
	var running []activitymon.RunningApp
	if s.running != nil {
		running = s.running()
	}
	if result, ok := meetingSignalFromDevices(mic, camera, own, s.liveEvent(now), running); ok {
		log.Printf("[context] %s in use during a calendar meeting; confirming %s", deviceWord(mic, camera), result.MeetingName)
		s.onMeeting(result)
	}
}

func deviceWord(mic, camera bool) string {
	switch {
	case mic && camera:
		return "microphone and camera"
	case camera:
		return "camera"
	default:
		return "microphone"
	}
}

func (s *contextSignalService) emitDevice(now time.Time, device string, inUse bool) {
	if s.emit == nil {
		return
	}
	verb := "stopped"
	if inUse {
		verb = "started"
	}
	s.emit(activitymon.ActivityEvent{
		Timestamp: now.UnixMilli(), Kind: activitymon.EventDeviceInUse,
		Device: device, InUse: inUse, Detail: fmt.Sprintf("The %s %s being used", device, verb),
	})
}

func (s *contextSignalService) emitDeviceSnapshot(now time.Time, device string, inUse bool) {
	if s.emit == nil {
		return
	}
	use := "is not in use"
	if inUse {
		use = "is in use"
	}
	s.emit(activitymon.ActivityEvent{
		Timestamp: now.UnixMilli(), Kind: activitymon.EventDeviceInUse,
		Device: device, InUse: inUse, Detail: fmt.Sprintf("The %s %s", device, use),
	})
}

// pollIdle tracks away and present, and the day marks.
func (s *contextSignalService) pollIdle(ctx context.Context) {
	now := s.now()
	idle := s.idle()
	s.mu.Lock()
	s.state.Idle = idle
	if s.locked != nil {
		// The notifications can be missed (a lock during sleep); the poll
		// keeps the state honest.
		s.state.Locked = s.locked()
	}
	wasAway := s.state.Away
	s.state.Away = idle >= contextIdleAway
	present := idle < contextPresentWithin && !s.state.Locked && !s.state.Asleep
	changed := false
	if present {
		changed = applyActivityMark(&s.marks, now, true, time.Time{}, s.loc)
		if s.inputTimingSupported && applyInputMark(&s.marks, now, idle, s.loc) {
			changed = true
		}
	} else if idle >= contextIdleBreak {
		changed = applyActivityMark(&s.marks, now, false, now.Add(-idle), s.loc)
	}
	marks := s.marks
	away := s.state.Away
	s.mu.Unlock()
	if away != wasAway && s.emit != nil {
		name, detail := "active", "Back at your computer"
		if away {
			name, detail = "idle", fmt.Sprintf("Away from your computer for %d minutes", int(idle.Minutes()))
		}
		s.emit(activitymon.ActivityEvent{Timestamp: now.UnixMilli(), Kind: activitymon.EventSessionState, StateName: name, Detail: detail})
	}
	if changed {
		s.saveMarks(ctx, marks)
	}
}

// pollPower notices the power source changing.
func (s *contextSignalService) pollPower() {
	now := s.now()
	power := s.power()
	s.mu.Lock()
	prev := s.state.Power
	s.state.Power = power
	s.mu.Unlock()
	if !power.Known || (prev.Known && prev.OnBattery == power.OnBattery) || s.emit == nil {
		return
	}
	name, detail := "power", "Plugged in"
	if power.OnBattery {
		name, detail = "battery", fmt.Sprintf("On battery (%d%%)", power.Percent)
	}
	s.emit(activitymon.ActivityEvent{Timestamp: now.UnixMilli(), Kind: activitymon.EventSessionState, StateName: name, Detail: detail})
}

// snapshot is the current state.
func (s *contextSignalService) snapshot() contextState {
	if s == nil {
		return contextState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func contextDayMarksKey(date string) string { return contextDayMarksKeyPrefix + date }

func (s *contextSignalService) loadMarks(ctx context.Context) {
	if s.store == nil {
		return
	}
	date := s.now().In(s.loc).Format("2006-01-02")
	raw, err := s.store.GetUIState(ctx, contextDayMarksKey(date))
	if err != nil || raw == "" {
		return
	}
	var marks contextDayMarks
	if json.Unmarshal([]byte(raw), &marks) == nil && marks.Date == date {
		s.mu.Lock()
		s.marks = marks
		s.mu.Unlock()
	}
}

func (s *contextSignalService) saveMarks(ctx context.Context, marks contextDayMarks) {
	if s.store == nil || marks.Date == "" {
		return
	}
	raw, err := json.Marshal(marks)
	if err != nil {
		return
	}
	if err := s.store.SetUIState(ctx, contextDayMarksKey(marks.Date), string(raw)); err != nil {
		log.Printf("[context] save day marks: %v", err)
	}
}

// loadContextDayMarks reads the stored marks for a local date.
func loadContextDayMarks(ctx context.Context, store *storage.Store, date string) (contextDayMarks, bool) {
	if store == nil {
		return contextDayMarks{}, false
	}
	raw, err := store.GetUIState(ctx, contextDayMarksKey(date))
	if err != nil || raw == "" {
		return contextDayMarks{}, false
	}
	var marks contextDayMarks
	if err := json.Unmarshal([]byte(raw), &marks); err != nil || marks.Date != date {
		return contextDayMarks{}, false
	}
	return marks, true
}

func (a *App) startContextSignals(ctx context.Context) {
	if a == nil || a.store == nil {
		return
	}
	svc := newContextSignalService(a)
	a.contextSignals = svc
	svc.Start(ctx)
}

func (a *App) stopContextSignals() {
	if a == nil || a.contextSignals == nil {
		return
	}
	a.contextSignals.Stop()
}
