//go:build darwin

package main

/*
#include <stdlib.h>
#include "menubar_popover_darwin.h"
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

var menuBarPresentation struct {
	sync.Mutex
	refreshMu    sync.Mutex
	attention    menuBarAttention
	accounts     menuBarAccountPrivacy
	presentedIDs []string
	presented    bool
	waiting      menuBarWaitingMemory
	open         bool
	stopped      bool
	revision     uint64
	privacyEpoch uint64
}

// Privacy/source changes discard the transient native page immediately and
// fence any read already in flight. Opening always performs a fresh read.
func (a *App) noteMenuBarEvent(event string) {
	if a == nil || menuBarApp != a {
		return
	}
	menuBarPresentation.Lock()
	switch event {
	case magpieModelsUpdatedEvent:
		if !menuBarPresentation.accounts.update(a.magpieModels.snapshot().Accounts) {
			menuBarPresentation.Unlock()
			return
		}
		menuBarPresentation.presentedIDs = nil
		menuBarPresentation.attention = menuBarAttention{}
	case "memory:updated":
		// A belief update withdraws the held list only when it was a privacy
		// action (a forget or correction advances the privacy epoch); the
		// index emits memory:updated for every learned belief.
		epoch := mobileBriefPrivacyEpoch.Load()
		if a.derivedViews().active() && epoch == menuBarPresentation.privacyEpoch {
			menuBarPresentation.Unlock()
			return
		}
		menuBarPresentation.privacyEpoch = epoch
	case "sensitive-apps:updated", closedEyesModeChangedEvent, "background-listening:changed", "permissions:changed":
		menuBarPresentation.privacyEpoch = mobileBriefPrivacyEpoch.Load()
	case mainChatWaitingChangedEvent, mainChatWaitingItemsUpdated:
		// The waiting set changed: the next status tick reads the waiting
		// list again (from the waiting snapshot). Nothing is withdrawn. A
		// new chat row alone no longer re-reads it.
		menuBarPresentation.waiting.changed()
		menuBarPresentation.Unlock()
		return
	case mainChatItemsAppendedEvent:
		if !a.derivedViews().active() {
			menuBarPresentation.waiting.changed()
		}
		menuBarPresentation.Unlock()
		return
	default:
		menuBarPresentation.Unlock()
		return
	}
	menuBarPresentation.waiting.withdraw()
	menuBarPresentation.revision++
	menuBarPresentation.Unlock()
	// The native side empties the page now, open or not; a fresh read then
	// fills it in place (it no longer closes an open popover).
	C.isoformMenuBarInvalidate()
	a.nudgeDoingNow()
	go a.refreshMenuBarPresentation()
}
func menuBarPresentationStopped() bool {
	menuBarPresentation.Lock()
	defer menuBarPresentation.Unlock()
	return menuBarPresentation.stopped
}
func menuBarPresentationRevision() uint64 {
	menuBarPresentation.Lock()
	defer menuBarPresentation.Unlock()
	return menuBarPresentation.revision
}

func menuBarPopoverStep(step string) {
	log.Printf("[menubar] popover: %s", step)
}

// The existing five-second status loop owns refreshes; there is no new worker.
func (a *App) refreshMenuBarPresentation() { a.presentMenuBarView() }

// presentMenuBarView builds the popover's view and hands it to the native
// side; it reports whether a view was handed over.
func (a *App) presentMenuBarView() bool {
	if menuBarApp != a || !menuBarReady.Load() {
		return false
	}
	menuBarPresentation.refreshMu.Lock()
	defer menuBarPresentation.refreshMu.Unlock()
	lang := a.doingNowLanguage()
	menuBarPresentation.Lock()
	revision := menuBarPresentation.revision
	read := menuBarPresentation.waiting.begin(time.Now(), lang)
	menuBarPresentation.Unlock()
	view, err := a.menuBarView(lang, read)
	if err != nil {
		log.Printf("[menubar] read view: %v", err)
	}
	menuBarPresentation.Lock()
	defer menuBarPresentation.Unlock()
	if menuBarPresentation.stopped || menuBarPresentation.revision != revision {
		// The fence also returns the change this refresh took.
		if read {
			menuBarPresentation.waiting.changed()
		}
		return false
	}
	// The waiting list is read only after a change, and a slow or failed
	// read keeps the last good list; see menuBarWaitingMemory.
	menuBarPresentation.waiting.settle(&view, read, err, time.Now())
	// What the native side shows on the next click.
	menuBarPresentation.presentedIDs = append([]string(nil), view.WaitingIDs...)
	view.Icon = menuBarPresentation.attention.icon(view.Paused, menuBarPresentation.open, view.WaitingIDs)
	data, _ := json.Marshal(view)
	c := C.CString(string(data))
	defer C.free(unsafe.Pointer(c))
	C.isoformMenuBarUpdate(c)
	menuBarPresentation.presented = true
	return true
}

// menuBarPreloadFallback starts the launch preload when no first screen is
// painted, as in a launch at login with the window hidden.
const menuBarPreloadFallback = 10 * time.Second

var menuBarLaunchPreload sync.Once

// preloadMenuBarView builds the popover's whole view once at launch: the
// observing state, the brief row, the waiting count and list and Make my
// brief now. It waits for the main window's first screen so it never
// competes with it, then runs in the background; the first click shows its
// content instead of "Starting…". Afterwards the view stays warm through
// events only (menuBarWaitingMemory). The page itself is loaded earlier,
// when the status item is created (preloadMenuBarPopover).
func (a *App) preloadMenuBarView(ctx context.Context) {
	menuBarLaunchPreload.Do(func() {
		go func() {
			if !a.launch.wait(ctx, menuBarPreloadFallback) {
				return
			}
			started := time.Now()
			shown := a.presentMenuBarView()
			menuBarPresentation.Lock()
			held := menuBarPresentation.waiting.held
			menuBarPresentation.Unlock()
			log.Printf("[menubar] preload ms=%d since_launch_ms=%d shown=%t waiting_read=%t",
				time.Since(started).Milliseconds(), time.Since(launchStartedAt).Milliseconds(), shown, held)
		}()
	})
}

// menuBarPreloadView is the page a click shows before Go's first read: our
// popover in its "Starting…" state with the saved theme, the short menu and
// placeholders, and no personal content.
func menuBarPreloadView(lang string) menuBarView {
	w := menuBarWords(lang)
	return menuBarView{Language: lang, Theme: currentThemePreference(), Words: w, Status: "starting", State: w["starting"], Items: []menuBarWaitingItem{},
		Menu: menuBarMenu(lang, false), Icon: menuBarIconState{Opacity: 1}, Label: menuBarLabel(w, w["starting"], 0), Loading: true}
}

// preloadMenuBarPopover loads the popover page once the status item exists,
// so the first click after launch opens it at once.
func preloadMenuBarPopover(a *App) {
	// The theme file is normally read later in startup; it is a small file.
	loadThemePreferenceFromDisk()
	lang := "en"
	if a != nil && a.store != nil {
		lang = a.currentUILanguage()
	}
	view := menuBarPreloadView(lang)
	data, _ := json.Marshal(view)
	html := C.CString(renderMenuBarHTML(view))
	model := C.CString(string(data))
	C.isoformMenuBarPreload(html, model)
	C.free(unsafe.Pointer(html))
	C.free(unsafe.Pointer(model))
	menuBarPopoverStep(fmt.Sprintf("preload dispatched theme=%s lang=%s", view.Theme, lang))
}

func menuBarPlaybackChanged(data string) {
	if menuBarPresentationStopped() {
		return
	}
	c := C.CString(data)
	defer C.free(unsafe.Pointer(c))
	C.isoformMenuBarPlayback(c)
}

//export goMenuBarPopoverStep
func goMenuBarPopoverStep(step *C.char) {
	if step == nil {
		return
	}
	menuBarPopoverStep(C.GoString(step))
}

//export goMenuBarPopoverVisibility
func goMenuBarPopoverVisibility(open C._Bool) {
	// This lock protects only presentation state, never a database read.
	// Record visibility on the callback thread so open/close cannot reorder.
	menuBarPresentation.Lock()
	changed := menuBarPresentation.open != bool(open)
	menuBarPresentation.open = bool(open)
	if bool(open) {
		// Opening reads the waiting list again; the page fills in place.
		menuBarPresentation.waiting.changed()
		menuBarPresentation.attention.icon(false, true, menuBarPresentation.presentedIDs)
	}
	menuBarPresentation.Unlock()
	if changed && menuBarApp != nil {
		go menuBarApp.refreshMenuBarPresentation()
	}
}

//export goMenuBarCommand
func goMenuBarCommand(command *C.char) {
	value := C.GoString(command)
	if menuBarApp == nil {
		return
	}
	go func() {
		if !menuBarReady.Load() && value != "open" && value != "quit" {
			return
		}
		if err := menuBarApp.runMenuBarCommand(value); err != nil {
			log.Printf("[menubar] action %q: %v", value, err)
			c := C.CString(menuBarWords(menuBarApp.doingNowLanguage())["actionFailed"])
			C.isoformMenuBarError(c)
			C.free(unsafe.Pointer(c))
		}
	}()
}
func (a *App) showFromMenuBar() {
	if a.ctx == nil {
		return
	}
	switchToRegularApp()
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
}
func (a *App) runMenuBarCommand(command string) error {
	if menuBarPresentationStopped() {
		return nil
	}
	switch command {
	case "open":
		a.showFromMenuBar()
	case "activity":
		a.showFromMenuBar()
		a.emitBrowserEvent(activityOpenEvent)
	case "settings":
		a.showFromMenuBar()
		a.emitBrowserEvent("settings:open")
	case "sees":
		a.showFromMenuBar()
		a.emitBrowserEvent("menubar:sees")
	case "turnOn":
		// Observing is off: Settings at the section that holds Observing.
		a.showFromMenuBar()
		a.emitBrowserEvent("settings:open", "activity")
	case "restart":
		return a.RestartToUpdate()
	case "signIn:claude-code", "signIn:codex":
		return a.OpenAssistantSignIn(strings.TrimPrefix(command, "signIn:"))
	case "makeHere":
		// From the popover: while a brief is being made the brief row shows
		// it and the popover stays open; a wait or a failure is answered
		// in the app, which closes the popover.
		result, err := a.GenerateMobileBriefNow()
		if err == nil && (result.Status == "started" || result.Status == "running") {
			go a.refreshMenuBarPresentation()
			return nil
		}
		status := result.Status
		if err != nil {
			log.Printf("[menubar] make brief: %v", err)
			status = "failed"
		}
		a.showFromMenuBar()
		a.emitBrowserEvent("menubar:make-brief", status, result.NextAllowedAt)
	case "quit":
		performRealQuit()
	case "make":
		// The popover has closed; the app says what happened (it was
		// silent during cooldown or while a brief was being made).
		result, err := a.GenerateMobileBriefNow()
		status := result.Status
		if err != nil {
			log.Printf("[menubar] make brief: %v", err)
			status = "failed"
		}
		a.showFromMenuBar()
		a.emitBrowserEvent("menubar:make-brief", status, result.NextAllowedAt)
	case "pause60":
		menuBarToggleActivityMu.Lock()
		defer menuBarToggleActivityMu.Unlock()
		if a.IsClosedEyesModeEnabled() {
			return a.ResumeObservation()
		}
		return a.PauseObservationFor(60)
	case "pause15":
		return a.PauseObservationFor(15)
	case "resume":
		menuBarToggleActivityMu.Lock()
		defer menuBarToggleActivityMu.Unlock()
		if a.IsClosedEyesModeEnabled() {
			return a.ResumeObservation()
		}
	case "waiting":
		a.showFromMenuBar()
		a.emitBrowserEvent("menubar:waiting", "", "")
	default:
		if strings.HasPrefix(command, "play:") {
			// Nothing here waits on the brief service: LatestMobileBrief
			// recomputes today's signals, so the event goes out at once and
			// Today's player does the source-gated audio read (GetMobileBriefAudio).
			// The player reports loading, then playing or failed, through
			// SetMenuBarBriefPlayback, which logs the outcome line.
			start := time.Now()
			id := strings.TrimPrefix(command, "play:")
			if !mobileValidBriefID(id) {
				log.Printf("[menubar] action=play brief=%q result=failed:invalid-id", id)
				return nil
			}
			noteMenuBarPlay(id, start)
			a.emitBrowserEvent("menubar:brief-play", id)
			log.Printf("[menubar] action=play brief=%s result=loading dispatch_ms=%d", id, time.Since(start).Milliseconds())
		}
		if strings.HasPrefix(command, "read:") {
			// A brief with no recording opens Today at that brief.
			if id := strings.TrimPrefix(command, "read:"); mobileValidBriefID(id) {
				a.showFromMenuBar()
				a.emitBrowserEvent("notification:clicked", "today:brief:"+id)
			}
		}
		if strings.HasPrefix(command, "waiting:"+menuBarNeedNoChat) {
			a.showFromMenuBar()
			a.emitBrowserEvent("menubar:needs")
			return nil
		}
		if strings.HasPrefix(command, "waiting:") {
			// Queue order may change between display and click. Validate the
			// row's current visibility and main-chat membership, not its rank.
			revision := menuBarPresentationRevision()
			overview, err := a.GetMainChatOverview()
			if err != nil {
				return err
			}
			id := strings.TrimPrefix(command, "waiting:")
			if _, err := a.FindStreamItemCursor(overview.SessionID, id); err != nil {
				return err
			}
			if revision != menuBarPresentationRevision() {
				return nil
			}
			a.showFromMenuBar()
			a.emitBrowserEvent("menubar:waiting", overview.SessionID, id)
		}
	}
	return nil
}
