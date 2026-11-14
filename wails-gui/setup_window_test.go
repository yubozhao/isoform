package main

import (
	"testing"
)

func TestChooseSetupWindowLandingPrefersVisibleMenuBarItem(t *testing.T) {
	in := setupWindowLandingInput{
		ItemVisible:    true,
		Item:           setupWindowRect{X: 1300, Y: 877, W: 24, H: 22},
		MenuBarVisible: true,
		MenuBarTop:     900,
		Bounds:         setupWindowRect{X: 0, Y: 0, W: 1440, H: 900},
		WindowScreen:   setupWindowRect{X: 0, Y: 0, W: 1440, H: 900},
	}
	got := chooseSetupWindowLanding(in)
	if got.Landed != setupLandedItem {
		t.Fatalf("Landed = %q, want %q", got.Landed, setupLandedItem)
	}
	if got.Center != (setupWindowPoint{X: 1312, Y: 888}) {
		t.Fatalf("Center = %+v, want the item's centre", got.Center)
	}
	if got.Anchor != (setupWindowPoint{X: 1312, Y: 877}) {
		t.Fatalf("Anchor = %+v, want the item's bottom centre", got.Anchor)
	}
}

func TestChooseSetupWindowLandingFallsBackToTopCentre(t *testing.T) {
	base := setupWindowLandingInput{
		ItemVisible:    true,
		Item:           setupWindowRect{X: 1300, Y: 877, W: 24, H: 22},
		MenuBarVisible: true,
		MenuBarTop:     900,
		Bounds:         setupWindowRect{X: 0, Y: 0, W: 1440, H: 900},
		WindowScreen:   setupWindowRect{X: 0, Y: 0, W: 1440, H: 900},
	}
	cases := map[string]func(*setupWindowLandingInput){
		"item not visible":            func(in *setupWindowLandingInput) { in.ItemVisible = false },
		"menu bar auto-hidden":        func(in *setupWindowLandingInput) { in.MenuBarVisible = false },
		"zero-size item":              func(in *setupWindowLandingInput) { in.Item.W = 0 },
		"item off the displays":       func(in *setupWindowLandingInput) { in.Item.X = 1700 },
		"item below the menu bar":     func(in *setupWindowLandingInput) { in.Item.Y = 600 },
		"item behind the notch":       func(in *setupWindowLandingInput) { in.HasNotch = true; in.NotchX = 1280; in.NotchW = 120 },
		"item above the menu bar top": func(in *setupWindowLandingInput) { in.Item.Y = 950 },
	}
	for name, mutate := range cases {
		in := base
		mutate(&in)
		got := chooseSetupWindowLanding(in)
		if got.Landed != setupLandedTopCenter {
			t.Errorf("%s: Landed = %q, want %q", name, got.Landed, setupLandedTopCenter)
			continue
		}
		if got.Center != (setupWindowPoint{X: 720, Y: 888}) {
			t.Errorf("%s: Center = %+v, want the top centre of the window's display", name, got.Center)
		}
		if got.Anchor != (setupWindowPoint{X: 720, Y: 876}) {
			t.Errorf("%s: Anchor = %+v, want below the menu bar", name, got.Anchor)
		}
	}
}

func TestChooseSetupWindowLandingUsesWindowDisplayForTopCentre(t *testing.T) {
	in := setupWindowLandingInput{
		MenuBarTop:   900,
		Bounds:       setupWindowRect{X: -1920, Y: 0, W: 3360, H: 900},
		WindowScreen: setupWindowRect{X: -1920, Y: 0, W: 1920, H: 1080},
	}
	got := chooseSetupWindowLanding(in)
	if got.Landed != setupLandedTopCenter {
		t.Fatalf("Landed = %q, want %q", got.Landed, setupLandedTopCenter)
	}
	if got.Center.X != -960 || got.Center.Y != 1068 {
		t.Fatalf("Center = %+v, want the top centre of the second display", got.Center)
	}
}

func TestSetupLaunchStateFor(t *testing.T) {
	cases := []struct {
		name                string
		done, watching      bool
		wantSetup, wantMenu bool
	}{
		{"fresh install", false, false, true, false},
		{"watching started, setup unfinished", false, true, true, true},
		{"onboarding finished", true, false, false, true},
		{"finished and watching", true, true, false, true},
	}
	for _, tc := range cases {
		got := setupLaunchStateFor(tc.done, tc.watching)
		if got.SetupNeeded != tc.wantSetup || got.MenuBarVisible != tc.wantMenu {
			t.Errorf("%s: got %+v, want SetupNeeded=%v MenuBarVisible=%v", tc.name, got, tc.wantSetup, tc.wantMenu)
		}
	}
}

func TestOnboardingStateDone(t *testing.T) {
	cases := map[string]bool{
		"":                        false,
		"not json":                false,
		`{}`:                      false,
		`{"completed":false}`:     false,
		`{"completed":true}`:      true,
		`{"neverShowAgain":true}`: true,
		`{"completed":false,"neverShowAgain":false}`: false,
	}
	for raw, want := range cases {
		if got := onboardingStateDone(raw); got != want {
			t.Errorf("onboardingStateDone(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestSetupWindowStateBookkeeping(t *testing.T) {
	var s setupWindowState
	if s.isActive() {
		t.Fatal("a new state must be inactive")
	}
	frame := setupWindowRect{X: 100, Y: 200, W: 1200, H: 800}
	if !s.enter(frame, true) {
		t.Fatal("first enter must report a change")
	}
	if s.enter(setupWindowRect{X: 1, Y: 1, W: 600, H: 560}, true) {
		t.Fatal("a second enter must not change anything")
	}
	got, hasFrame, wasActive := s.leave()
	if !wasActive || !hasFrame || got != frame {
		t.Fatalf("leave = (%+v, %v, %v), want the first frame back", got, hasFrame, wasActive)
	}
	if _, _, wasActive := s.leave(); wasActive {
		t.Fatal("a second leave must report that setup mode was already off")
	}
}

func TestSetupWindowStateLaunchHasNoFrame(t *testing.T) {
	var s setupWindowState
	s.enterAtLaunch()
	if !s.isActive() {
		t.Fatal("enterAtLaunch must turn setup mode on")
	}
	if s.enter(setupWindowRect{W: 600, H: 560}, true) {
		t.Fatal("enter after a launch must not remember the setup-size frame")
	}
	_, hasFrame, wasActive := s.leave()
	if !wasActive || hasFrame {
		t.Fatalf("leave after launch = (hasFrame %v, wasActive %v), want (false, true)", hasFrame, wasActive)
	}
}

// A mode change the window could not apply yet ("not ready") must not be
// recorded: the retry then still resizes the window. Recording it first sent
// the retry down the "already in setup mode" path, so the window kept its
// size, and a failed leave left the window at the setup size for good.
func TestSetupWindowApplyRecordsOnlyAfterTheWindowChanged(t *testing.T) {
	var s setupWindowState
	main := setupWindowRect{X: 100, Y: 200, W: 1200, H: 800}
	frame := func() (setupWindowRect, bool) { return main, true }
	mainSize := func() (float64, float64) { return 1200, 800 }
	var modes []int
	var targets []setupWindowRect
	ready := false
	native := func(mode int, target setupWindowRect) bool {
		modes = append(modes, mode)
		targets = append(targets, target)
		return ready
	}

	if err := s.apply(true, frame, native, mainSize); err == nil {
		t.Fatal("enter before the window is ready must report not ready")
	}
	if s.isActive() {
		t.Fatal("a failed enter was recorded")
	}
	ready = true
	if err := s.apply(true, frame, native, mainSize); err != nil {
		t.Fatalf("retried enter: %v", err)
	}
	if modes[1] != setupApplyEnter || !s.isActive() {
		t.Fatalf("retried enter mode = %d active = %v, want %d and true", modes[1], s.isActive(), setupApplyEnter)
	}

	ready = false
	if err := s.apply(false, frame, native, mainSize); err == nil {
		t.Fatal("leave before the window is ready must report not ready")
	}
	if !s.isActive() {
		t.Fatal("a failed leave was recorded")
	}
	ready = true
	if err := s.apply(false, frame, native, mainSize); err != nil {
		t.Fatalf("retried leave: %v", err)
	}
	last := len(modes) - 1
	if modes[last] != setupApplyLeaveFrame || targets[last] != main || s.isActive() {
		t.Fatalf("retried leave mode = %d target = %+v active = %v, want %d, %+v, false",
			modes[last], targets[last], s.isActive(), setupApplyLeaveFrame, main)
	}
}
