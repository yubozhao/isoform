package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"runtime"
	"sync"
	"time"
)

// First-run setup v2 (docs/handoff-2026-10-28/handoff-2026-10-28-1215-setup-v2.0.0.md,
// sections 3, 4 "The finish", 5 and 7). This file holds the decisions and the
// bookkeeping. The native side (setup_window_darwin.m) draws and animates with
// Core Animation; setup_window_darwin.go bridges the two.

const (
	// setupWindowWidth and setupWindowHeight are the setup window's content size in points.
	setupWindowWidth  = 600
	setupWindowHeight = 560

	// setupMainWindowWidth and setupMainWindowHeight are the main window's default size (main.go).
	setupMainWindowWidth  = 1200
	setupMainWindowHeight = 800

	// setupAppTileSize is the app tile on screen 4, in points.
	setupAppTileSize = 72

	// setupWatchingStateKey is set in ui_state when watching starts (screen 3), so the
	// menu bar mark is shown from launch on after a restart.
	setupWatchingStateKey = "isoform.setup.watching"

	// setupRevealDuration is the main window's fade in after the finish (section 4.5).
	setupRevealDuration = 0.35

	// setupMenuBarHeight is the menu bar's thickness; setupMenuBarTolerance absorbs rounding.
	setupMenuBarHeight    = 24
	setupMenuBarTolerance = 1

	// setupTopCenterInset places the top-centre fallback inside the menu bar band.
	setupTopCenterInset = 12

	// setupFinishTimeout bounds how long PlaySetupFinish waits for the landing. The
	// flight takes 0.72 s, so this only guards against a stalled animation.
	setupFinishTimeout = 4 * time.Second

	setupLandedItem      = "item"
	setupLandedTopCenter = "top-center"
)

// Window modes for nativeSetupWindowApply. The values match setup_window_darwin.m.
const (
	setupApplyEnterStay     = 0 // setup styling, no move
	setupApplyEnter         = 1 // setup styling, 600 x 560 centred on the menu bar display
	setupApplyLeaveFrame    = 2 // main styling, the remembered frame
	setupApplyLeaveCentered = 3 // main styling, the given size centred
	setupApplyLeaveStay     = 4 // main styling, no move
)

// setupWindowNativeAvailable is false off macOS, where there is no setup window.
var setupWindowNativeAvailable = runtime.GOOS == "darwin"

var (
	errSetupWindowNotReady = errors.New("setup window is not ready")
	errSetupMarkNotReady   = errors.New("menu bar mark is not ready")
)

// SetupFinishRequest is sent by screen 4 "The finish" after the icon has popped and held.
type SetupFinishRequest struct {
	IconX         float64 `json:"iconX"`         // tile centre in CSS points, top-left origin of the window content
	IconY         float64 `json:"iconY"`         // tile centre in CSS points, top-left origin of the window content
	IconSize      float64 `json:"iconSize"`      // tile size in points; 72 when zero
	Callout       string  `json:"callout"`       // shown under the mark when the icon lands on it
	CalloutHidden string  `json:"calloutHidden"` // shown when the icon flies to the top centre instead
	ReduceMotion  bool    `json:"reduceMotion"`  // no flight: fade, ring once, show the callout
}

// SetupFinishResult reports where the icon landed: "item" or "top-center".
type SetupFinishResult struct {
	Landed string `json:"landed"`
}

// setupWindowRect is an AppKit frame: origin at the bottom left of the primary display.
type setupWindowRect struct{ X, Y, W, H float64 }

type setupWindowPoint struct{ X, Y float64 }

// setupWindowLandingInput is what the native side reports about the menu bar.
type setupWindowLandingInput struct {
	ItemVisible    bool            // the status item is shown and has a window
	Item           setupWindowRect // the status item's button, in screen coordinates
	MenuBarVisible bool            // the menu bar is shown (not auto-hidden)
	MenuBarTop     float64         // top edge of the menu bar's display
	Bounds         setupWindowRect // all displays together: where the icon may fly
	WindowScreen   setupWindowRect // the display the setup window is on
	HasNotch       bool
	NotchX, NotchW float64 // the part of the menu bar the notch hides
}

// setupWindowLanding is the chosen target. Center is where the icon's centre lands;
// Anchor is the bottom centre of the mark, which the callout's tip points at.
type setupWindowLanding struct {
	Landed string
	Center setupWindowPoint
	Anchor setupWindowPoint
}

// chooseSetupWindowLanding picks the menu bar item when it can be seen, and the top
// centre of the setup window's display otherwise (hidden, behind the notch, auto-hidden
// menu bar, or off the displays).
func chooseSetupWindowLanding(in setupWindowLandingInput) setupWindowLanding {
	topCenter := setupWindowPoint{
		X: in.WindowScreen.X + in.WindowScreen.W/2,
		Y: in.WindowScreen.Y + in.WindowScreen.H - setupTopCenterInset,
	}
	top := setupWindowLanding{
		Landed: setupLandedTopCenter,
		Center: topCenter,
		Anchor: setupWindowPoint{X: topCenter.X, Y: in.MenuBarTop - setupMenuBarHeight},
	}
	if !in.ItemVisible || !in.MenuBarVisible || in.Item.W <= 0 || in.Item.H <= 0 {
		return top
	}
	item := in.Item
	if item.X < in.Bounds.X || item.X+item.W > in.Bounds.X+in.Bounds.W ||
		item.Y < in.Bounds.Y || item.Y+item.H > in.Bounds.Y+in.Bounds.H {
		return top
	}
	// The item must sit in the menu bar band of the menu bar's display.
	if item.Y+item.H > in.MenuBarTop+setupMenuBarTolerance ||
		item.Y < in.MenuBarTop-setupMenuBarHeight-setupMenuBarTolerance {
		return top
	}
	if in.HasNotch && item.X < in.NotchX+in.NotchW && item.X+item.W > in.NotchX {
		return top
	}
	center := setupWindowPoint{X: item.X + item.W/2, Y: item.Y + item.H/2}
	return setupWindowLanding{
		Landed: setupLandedItem,
		Center: center,
		Anchor: setupWindowPoint{X: center.X, Y: item.Y},
	}
}

// setupLaunchState is decided in main() before the window exists.
type setupLaunchState struct {
	SetupNeeded    bool // onboarding is not finished: open at the setup size
	MenuBarVisible bool // the menu bar mark is shown at launch
}

// setupLaunchStateFor applies the launch rule: setup runs until onboarding is finished;
// the mark shows once onboarding is finished or watching has started.
func setupLaunchStateFor(onboardingDone, watching bool) setupLaunchState {
	return setupLaunchState{
		SetupNeeded:    !onboardingDone,
		MenuBarVisible: onboardingDone || watching,
	}
}

// onboardingStateDone reads the renderer's onboarding state (onboardingStateKey).
func onboardingStateDone(raw string) bool {
	if raw == "" {
		return false
	}
	var state struct {
		Completed      bool `json:"completed"`
		NeverShowAgain bool `json:"neverShowAgain"`
	}
	if json.Unmarshal([]byte(raw), &state) != nil {
		return false
	}
	return state.Completed || state.NeverShowAgain
}

// setupWindowState remembers the main window's frame while setup mode is on, so that
// leaving setup mode puts the window back where the person left it.
type setupWindowState struct {
	applyMu  sync.Mutex // serializes apply: read state, change the window, record
	mu       sync.Mutex
	active   bool
	hasFrame bool
	frame    setupWindowRect
}

// setupWindowMode is the process-wide setup window state.
var setupWindowMode setupWindowState

// enter turns setup mode on and remembers the current frame. It reports false when
// setup mode was already on, so the window is not moved twice.
func (s *setupWindowState) enter(current setupWindowRect, haveCurrent bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return false
	}
	s.active = true
	s.hasFrame = haveCurrent
	s.frame = current
	return true
}

// enterAtLaunch turns setup mode on for a window created at the setup size. There is
// no main-size frame to remember, so leaving falls back to the saved size.
func (s *setupWindowState) enterAtLaunch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = true
	s.hasFrame = false
	s.frame = setupWindowRect{}
}

// leave turns setup mode off. It returns the remembered frame (when there is one) and
// whether setup mode was on.
func (s *setupWindowState) leave() (setupWindowRect, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return setupWindowRect{}, false, false
	}
	frame, hasFrame := s.frame, s.hasFrame
	s.active, s.hasFrame, s.frame = false, false, setupWindowRect{}
	return frame, hasFrame, true
}

// peek returns what leave would return without changing the state.
func (s *setupWindowState) peek() (setupWindowRect, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frame, s.hasFrame, s.active
}

func (s *setupWindowState) isActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// prepareSetupWindowAtLaunch runs in main() before Wails creates the window. It reads
// the onboarding state synchronously, so the window is created at the setup size and
// never shows at the main size first.
func prepareSetupWindowAtLaunch() setupLaunchState {
	state := readNativeSetupLaunchState()
	if state.SetupNeeded {
		setupWindowMode.enterAtLaunch()
	}
	nativeSetupMarkStartsVisible(state.MenuBarVisible)
	return state
}

// applySetupWindowAtLaunch restyles the window once the app runs (OnStartup). The
// window was already created at the setup size by main().
func (a *App) applySetupWindowAtLaunch() {
	if setupWindowMode.isActive() {
		if err := a.SetSetupWindowMode(true); err != nil {
			log.Printf("[setup-window] launch restyle: %v", err)
		}
	}
}

// setupMainWindowSize is the size to return to when no frame was remembered: the
// saved main window size, or the default.
func (a *App) setupMainWindowSize() (float64, float64) {
	width, height := float64(setupMainWindowWidth), float64(setupMainWindowHeight)
	state, err := a.LoadWindowState()
	if err == nil && state != nil && state.Width >= 400 && state.Height >= 300 {
		return float64(state.Width), float64(state.Height)
	}
	return width, height
}

// SetSetupWindowMode turns the setup window's size and styling on or off. On: the
// frame is remembered, the window is 600 x 560 and centred on the main display, not
// resizable, and its zoom button is off. Off: resizable again, and the remembered
// frame (or the saved size, centred) comes back. Idempotent.
func (a *App) SetSetupWindowMode(on bool) error {
	if !setupWindowNativeAvailable {
		return nil
	}
	return setupWindowMode.apply(on, nativeSetupWindowFrame, nativeSetupWindowApply, a.setupMainWindowSize)
}

// apply records a mode change only after the native side applied it. When
// the window is not ready yet the state is left as it was, so a retry still
// resizes the window instead of taking the "already in this mode" path.
func (s *setupWindowState) apply(
	on bool,
	frame func() (setupWindowRect, bool),
	native func(int, setupWindowRect) bool,
	mainSize func() (float64, float64),
) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	if on {
		mode := setupApplyEnterStay
		entering := !s.isActive()
		var current setupWindowRect
		var haveCurrent bool
		if entering {
			current, haveCurrent = frame()
			mode = setupApplyEnter
		}
		if !native(mode, setupWindowRect{}) {
			return errSetupWindowNotReady
		}
		if entering {
			s.enter(current, haveCurrent)
		}
		return nil
	}
	remembered, hasFrame, wasActive := s.peek()
	mode := setupApplyLeaveStay
	var target setupWindowRect
	if wasActive {
		if hasFrame {
			mode = setupApplyLeaveFrame
			target = remembered
		} else {
			w, h := mainSize()
			mode = setupApplyLeaveCentered
			target = setupWindowRect{W: w, H: h}
		}
	}
	if !native(mode, target) {
		return errSetupWindowNotReady
	}
	s.leave()
	return nil
}

// SetMenuBarMarkVisible shows or hides the menu bar mark. Showing it with pulse plays
// the spring appearance and two rings, once, when the mark was hidden. Showing it
// records that watching has started, so the mark stays on after a restart.
func (a *App) SetMenuBarMarkVisible(visible bool, pulse bool) error {
	if !setupWindowNativeAvailable {
		return nil
	}
	if visible {
		if a.store == nil {
			return errors.New("storage is not ready")
		}
		if err := a.store.SetUIState(context.Background(), setupWatchingStateKey, "1"); err != nil {
			return err
		}
	}
	if !nativeSetupMarkSetVisible(visible, pulse) {
		return errSetupMarkNotReady
	}
	return nil
}

// PlaySetupFinish runs the flight after the icon pop and hold: the setup window fades,
// the icon flies to the menu bar mark (or the top centre when the mark cannot be seen),
// the mark rings, and the callout shows. It returns once the icon has landed; the
// callout keeps its own 6 s.
func (a *App) PlaySetupFinish(req SetupFinishRequest) (SetupFinishResult, error) {
	if !setupWindowNativeAvailable {
		return SetupFinishResult{}, nil
	}
	landing := chooseSetupWindowLanding(nativeSetupWindowLandingInput())
	callout := req.Callout
	if landing.Landed == setupLandedTopCenter {
		callout = req.CalloutHidden
	}
	iconSize := req.IconSize
	if iconSize <= 0 {
		iconSize = setupAppTileSize
	}
	landed := nativeSetupFinishPlay(setupFinishPlay{
		IconX:        req.IconX,
		IconY:        req.IconY,
		IconSize:     iconSize,
		Target:       landing.Center,
		Anchor:       landing.Anchor,
		Ring:         landing.Landed == setupLandedItem,
		ReduceMotion: req.ReduceMotion,
		Callout:      callout,
	})
	select {
	case <-landed:
	case <-time.After(setupFinishTimeout):
		log.Printf("[setup-window] the icon did not land within %s", setupFinishTimeout)
	}
	return SetupFinishResult{Landed: landing.Landed}, nil
}

// RevealMainWindow leaves setup mode while the window is invisible, then fades the
// window in over the main window's 0.35 s.
func (a *App) RevealMainWindow() error {
	if !setupWindowNativeAvailable {
		return nil
	}
	if err := a.SetSetupWindowMode(false); err != nil {
		return err
	}
	nativeSetupReveal(setupRevealDuration)
	return nil
}

// setupFinishPlay is the finish request as the native side needs it.
type setupFinishPlay struct {
	IconX, IconY, IconSize float64
	Target, Anchor         setupWindowPoint
	Ring                   bool
	ReduceMotion           bool
	Callout                string
}
