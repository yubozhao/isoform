//go:build windows

package main

import (
	"context"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Global hotkey on Windows: Alt+Space, the de-facto industry standard
// (ChatGPT Desktop companion, Raycast for Windows, PowerToys Run, Flow
// Launcher all default to it). The system-menu Alt+Space is swallowed
// in the same step the way every other launcher does — nobody actually
// uses that menu and the conflict is universally accepted.
//
// Implementation mirrors internal/platform/activitymon/monitor_windows.go:
// pure-syscall WH_KEYBOARD_LL low-level keyboard hook, dedicated
// LockOSThread'd message-pump goroutine, hook proc kept tiny.

var (
	hotkeyUser32   = syscall.NewLazyDLL("user32.dll")
	hotkeyKernel32 = syscall.NewLazyDLL("kernel32.dll")

	procHotkeySetWindowsHookExW          = hotkeyUser32.NewProc("SetWindowsHookExW")
	procHotkeyCallNextHookEx             = hotkeyUser32.NewProc("CallNextHookEx")
	procHotkeyUnhookWindowsHookEx        = hotkeyUser32.NewProc("UnhookWindowsHookEx")
	procHotkeyGetMessageW                = hotkeyUser32.NewProc("GetMessageW")
	procHotkeyTranslateMessage           = hotkeyUser32.NewProc("TranslateMessage")
	procHotkeyDispatchMessageW           = hotkeyUser32.NewProc("DispatchMessageW")
	procHotkeyPostThreadMessageW         = hotkeyUser32.NewProc("PostThreadMessageW")
	procHotkeyPeekMessageW               = hotkeyUser32.NewProc("PeekMessageW")
	procHotkeyGetAsyncKeyState           = hotkeyUser32.NewProc("GetAsyncKeyState")
	procHotkeyVkKeyScanW                 = hotkeyUser32.NewProc("VkKeyScanW")
	procHotkeyGetForegroundWindow        = hotkeyUser32.NewProc("GetForegroundWindow")
	procHotkeyGetWindowThreadProcessId   = hotkeyUser32.NewProc("GetWindowThreadProcessId")
	procHotkeyGetCurrentThreadId         = hotkeyKernel32.NewProc("GetCurrentThreadId")
	procHotkeyGetCurrentProcessId        = hotkeyKernel32.NewProc("GetCurrentProcessId")
	procHotkeyGetModuleHandleW           = hotkeyKernel32.NewProc("GetModuleHandleW")
	procHotkeyOpenProcess                = hotkeyKernel32.NewProc("OpenProcess")
	procHotkeyCloseHandle                = hotkeyKernel32.NewProc("CloseHandle")
	procHotkeyQueryFullProcessImageNameW = hotkeyKernel32.NewProc("QueryFullProcessImageNameW")
)

const (
	hotkeyWHKeyboardLL = 13
	hotkeyWMKeydown    = 0x0100
	hotkeyWMSyskeydown = 0x0104
	hotkeyWMQuit       = 0x0012

	hotkeyVKSpace    = 0x20
	hotkeyVKShift    = 0x10
	hotkeyVKControl  = 0x11
	hotkeyVKMenu     = 0x12 // Alt
	hotkeyVKLWin     = 0x5B
	hotkeyVKRWin     = 0x5C
	hotkeyVKLShift   = 0xA0
	hotkeyVKRShift   = 0xA1
	hotkeyVKLControl = 0xA2
	hotkeyVKRControl = 0xA3
	hotkeyVKLMenu    = 0xA4
	hotkeyVKRMenu    = 0xA5

	hotkeyProcessQueryLimited = 0x1000
)

// hotkeyKBDLLHookStruct mirrors KBDLLHOOKSTRUCT exactly. The lParam in
// the WH_KEYBOARD_LL callback is documented to point at one of these.
type hotkeyKBDLLHookStruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

type hotkeyWinMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	PtX     int32
	PtY     int32
}

// quickChatHotkey is the singleton owning the keyboard hook and pump
// goroutine. The hook proc is a free function (Windows callback contract
// requires a stable cdecl-style entrypoint), so it talks back to this
// state via package-scope pointer.
type quickChatHotkey struct {
	mu           sync.Mutex
	running      bool
	cancel       context.CancelFunc
	threadID     uint32
	hook         uintptr
	triggerCh    chan struct{}
	closedEyesCh chan struct{}
	recordingCh  chan uint32 // VK codes captured while recording
	ownPID       uint32
}

var globalQuickChatHotkey = &quickChatHotkey{}

// applyHotkeyToPlatform is a no-op on Windows: the hook callback reads
// the current config on every keystroke, so SetQuickChatHotkey changes
// take effect immediately without re-installing the hook.
func applyHotkeyToPlatform(_ QuickChatHotkeyConfig) {}

// StartQuickChatHotkey installs the Alt+Space global hotkey. Returns
// nil even if the system thread couldn't be initialized yet — actual
// errors are logged from the pump goroutine, since hook installation
// happens asynchronously after we LockOSThread.
func (a *App) StartQuickChatHotkey() error {
	if err := chatFeatureErr(); err != nil {
		return err
	}
	quickChatHotkeyMu.Lock()
	quickChatApp = a
	quickChatHotkeyMu.Unlock()

	globalQuickChatHotkey.mu.Lock()
	if globalQuickChatHotkey.running {
		globalQuickChatHotkey.mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	globalQuickChatHotkey.cancel = cancel
	globalQuickChatHotkey.triggerCh = make(chan struct{}, 1)
	globalQuickChatHotkey.closedEyesCh = make(chan struct{}, 1)
	// Recording channel is buffered enough to absorb one chord; the
	// drain goroutine empties it as the events go out via Wails.
	globalQuickChatHotkey.recordingCh = make(chan uint32, 4)
	globalQuickChatHotkey.running = true

	pid, _, _ := procHotkeyGetCurrentProcessId.Call()
	globalQuickChatHotkey.ownPID = uint32(pid)
	globalQuickChatHotkey.mu.Unlock()

	go globalQuickChatHotkey.runHookLoop(ctx)
	go globalQuickChatHotkey.runTriggerDrain(ctx)
	go globalQuickChatHotkey.runClosedEyesDrain(ctx)
	go globalQuickChatHotkey.runRecordingDrain(ctx)

	log.Printf("[quickChat] global hotkey enabled (current: %s)", describeCombo(currentQuickChatHotkey()))
	return nil
}

// StopQuickChatHotkey uninstalls the hook and tears down the pump
// goroutine. Safe to call when not started.
func (a *App) StopQuickChatHotkey() {
	globalQuickChatHotkey.mu.Lock()
	if !globalQuickChatHotkey.running {
		globalQuickChatHotkey.mu.Unlock()
		return
	}
	globalQuickChatHotkey.running = false
	if globalQuickChatHotkey.cancel != nil {
		globalQuickChatHotkey.cancel()
		globalQuickChatHotkey.cancel = nil
	}
	tid := globalQuickChatHotkey.threadID
	globalQuickChatHotkey.mu.Unlock()

	if tid != 0 {
		// Wake the GetMessage pump so it can observe ctx cancellation
		// and exit cleanly. Without WM_QUIT, the message loop blocks
		// indefinitely.
		procHotkeyPostThreadMessageW.Call(uintptr(tid), hotkeyWMQuit, 0, 0)
	}

	quickChatHotkeyMu.Lock()
	quickChatApp = nil
	quickChatHotkeyMu.Unlock()
}

// runHookLoop is the message pump. Windows requires hook installation
// and the dispatch loop to run on the same OS thread, hence LockOSThread.
func (s *quickChatHotkey) runHookLoop(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	tid, _, _ := procHotkeyGetCurrentThreadId.Call()
	s.mu.Lock()
	s.threadID = uint32(tid)
	s.mu.Unlock()

	// Force the system to allocate the thread message queue before we
	// install the hook (Windows is lazy about this and a hook installed
	// before the queue exists can fail silently).
	var probe hotkeyWinMsg
	procHotkeyPeekMessageW.Call(uintptr(unsafe.Pointer(&probe)), 0, 0, 0, 0)

	moduleHandle, _, _ := procHotkeyGetModuleHandleW.Call(0)

	hook, _, err := procHotkeySetWindowsHookExW.Call(
		hotkeyWHKeyboardLL,
		syscall.NewCallback(quickChatKeyboardHookProc),
		moduleHandle,
		0,
	)
	if hook == 0 {
		log.Printf("[quickChat] failed to install keyboard hook: %v", err)
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.hook = hook
	s.mu.Unlock()

	// Cancel handler: post WM_QUIT so the pump exits.
	go func() {
		<-ctx.Done()
		procHotkeyPostThreadMessageW.Call(uintptr(tid), hotkeyWMQuit, 0, 0)
	}()

	var msg hotkeyWinMsg
	for {
		ret, _, _ := procHotkeyGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procHotkeyTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procHotkeyDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	s.mu.Lock()
	hookToRelease := s.hook
	s.hook = 0
	s.mu.Unlock()
	if hookToRelease != 0 {
		procHotkeyUnhookWindowsHookEx.Call(hookToRelease)
	}
}

// quickChatKeyboardHookProc is the WH_KEYBOARD_LL callback. Must return
// quickly: the OS input thread blocks on it. The combo is read fresh on
// every keystroke so changes via SetQuickChatHotkey take effect without
// reinstalling the hook. Returning 1 swallows the event so the combo's
// underlying chord (e.g. Alt+Space → system menu) doesn't fall through
// to its default handler.
func quickChatKeyboardHookProc(nCode, wParam, lParam uintptr) uintptr {
	if nCode == 0 && (wParam == hotkeyWMKeydown || wParam == hotkeyWMSyskeydown) {
		kb := (*hotkeyKBDLLHookStruct)(unsafe.Pointer(lParam)) //nolint:govet // Windows callback contract

		// Recording mode short-circuits the matcher: capture the key,
		// hand it to the renderer, swallow it. Modifier-only events
		// (just Ctrl/Alt/Shift/Win pressed alone) are ignored so the
		// user can still hold the modifier before pressing the actual
		// key — the recorder commits on the first non-modifier press.
		if quickChatHotkeyRecording.Load() {
			if !isModifierVK(kb.VkCode) {
				globalQuickChatHotkey.captureForRecording(kb.VkCode)
				return 1
			}
			// Let the modifier press pass through so the OS still
			// updates its async-key state (we rely on it to know which
			// modifiers are held when the next non-modifier comes in).
			ret, _, _ := procHotkeyCallNextHookEx.Call(0, nCode, wParam, lParam)
			return ret
		}

		if matchesClosedEyesHotkey(kb.VkCode) {
			globalQuickChatHotkey.fireClosedEyesFromHook()
			return 1
		}
		if matchesCurrentCombo(kb.VkCode) {
			globalQuickChatHotkey.fireFromHook()
			return 1
		}
	}
	ret, _, _ := procHotkeyCallNextHookEx.Call(0, nCode, wParam, lParam)
	return ret
}

// isModifierVK reports whether the VK is one of the modifier keys
// themselves (Ctrl/Alt/Shift/Win, both sides). The recorder ignores
// these so the user can build up a chord modifier-first.
func isModifierVK(vk uint32) bool {
	switch vk {
	case hotkeyVKShift, hotkeyVKControl, hotkeyVKMenu,
		hotkeyVKLWin, hotkeyVKRWin,
		hotkeyVKLShift, hotkeyVKRShift,
		hotkeyVKLControl, hotkeyVKRControl,
		hotkeyVKLMenu, hotkeyVKRMenu:
		return true
	}
	return false
}

// matchesCurrentCombo checks the current config against the just-pressed
// VK plus the live modifier state. The modifier set must match exactly
// (no superset / subset hits) so a user-bound Ctrl+Shift+Y doesn't fire
// on, say, Ctrl+Shift+Alt+Y.
func matchesCurrentCombo(vk uint32) bool {
	cfg := currentQuickChatHotkey()
	if cfg.Key == quickChatHotkeyKeyOff {
		return false
	}
	wantVK, ok := vkCodeForKey(cfg.Key)
	if !ok || uint32(wantVK) != vk {
		return false
	}
	if isKeyHeld(hotkeyVKControl) != cfg.Ctrl {
		return false
	}
	if isKeyHeld(hotkeyVKMenu) != cfg.Alt {
		return false
	}
	if isKeyHeld(hotkeyVKShift) != cfg.Shift {
		return false
	}
	winHeld := isKeyHeld(hotkeyVKLWin) || isKeyHeld(hotkeyVKRWin)
	return winHeld == cfg.Meta
}

func matchesClosedEyesHotkey(vk uint32) bool {
	if vk != 0x45 { // E
		return false
	}
	return isKeyHeld(hotkeyVKControl) &&
		isKeyHeld(hotkeyVKMenu) &&
		isKeyHeld(hotkeyVKShift) &&
		!isKeyHeld(hotkeyVKLWin) &&
		!isKeyHeld(hotkeyVKRWin)
}

func isKeyHeld(vk int) bool {
	state, _, _ := procHotkeyGetAsyncKeyState.Call(uintptr(vk))
	return (uint16(state) & 0x8000) != 0
}

// vkCodeForKey maps the JS-side `key` string (lowercase) to a Windows
// virtual-key code. Named keys (Space/Enter/F-keys/etc.) come from a
// fixed table because VkKeyScanW returns junk for them; printable
// characters fall through to VkKeyScanW so the user's current keyboard
// layout is honoured.
func vkCodeForKey(key string) (uint16, bool) {
	switch key {
	case " ", "space":
		return 0x20, true
	case "enter", "return":
		return 0x0D, true
	case "tab":
		return 0x09, true
	case "escape", "esc":
		return 0x1B, true
	case "backspace":
		return 0x08, true
	case "delete":
		return 0x2E, true
	case "arrowup":
		return 0x26, true
	case "arrowdown":
		return 0x28, true
	case "arrowleft":
		return 0x25, true
	case "arrowright":
		return 0x27, true
	case "home":
		return 0x24, true
	case "end":
		return 0x23, true
	case "pageup":
		return 0x21, true
	case "pagedown":
		return 0x22, true
	}

	// Function keys F1..F24
	if len(key) >= 2 && (key[0] == 'f' || key[0] == 'F') {
		var n int
		for i := 1; i < len(key); i++ {
			c := key[i]
			if c < '0' || c > '9' {
				n = 0
				break
			}
			n = n*10 + int(c-'0')
		}
		if n >= 1 && n <= 24 {
			return uint16(0x70 + n - 1), true
		}
	}

	// Printable single character (a-z, 0-9, punctuation): defer to
	// VkKeyScanW which honours the current keyboard layout. The low byte
	// is the VK; the high byte is the shift state we ignore here (the
	// hook proc already validates modifiers independently).
	runes := []rune(key)
	if len(runes) == 1 {
		r, _, _ := procHotkeyVkKeyScanW.Call(uintptr(uint16(runes[0])))
		if int16(r) != -1 {
			return uint16(r) & 0xFF, true
		}
	}
	return 0, false
}

// jsKeyForVK is the inverse of vkCodeForKey: maps a Windows VK code to
// the JS-style `key` string the renderer uses. We bias toward
// well-known named keys; printable characters fall back to lowercased
// ASCII via ToUnicode/MapVirtualKey-ish heuristics (here just A-Z and
// 0-9 main row, which is what 99% of users will record).
func jsKeyForVK(vk uint32) (string, bool) {
	switch vk {
	case 0x20:
		return " ", true
	case 0x0D:
		return "enter", true
	case 0x09:
		return "tab", true
	case 0x1B:
		return "escape", true
	case 0x08:
		return "backspace", true
	case 0x2E:
		return "delete", true
	case 0x26:
		return "arrowup", true
	case 0x28:
		return "arrowdown", true
	case 0x25:
		return "arrowleft", true
	case 0x27:
		return "arrowright", true
	case 0x24:
		return "home", true
	case 0x23:
		return "end", true
	case 0x21:
		return "pageup", true
	case 0x22:
		return "pagedown", true
	}
	// A..Z → "a".."z"
	if vk >= 0x41 && vk <= 0x5A {
		return string(rune('a' + int(vk-0x41))), true
	}
	// 0..9 (top row) → "0".."9"
	if vk >= 0x30 && vk <= 0x39 {
		return string(rune('0' + int(vk-0x30))), true
	}
	// F1..F24 → "f1".."f24"
	if vk >= 0x70 && vk <= 0x87 {
		return "f" + itoa(int(vk-0x70+1)), true
	}
	return "", false
}

// itoa avoids pulling in strconv just for this one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [3]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// describeCombo renders a human-readable form for log output.
func describeCombo(c QuickChatHotkeyConfig) string {
	parts := []string{}
	if c.Ctrl {
		parts = append(parts, "Ctrl")
	}
	if c.Alt {
		parts = append(parts, "Alt")
	}
	if c.Shift {
		parts = append(parts, "Shift")
	}
	if c.Meta {
		parts = append(parts, "Win")
	}
	parts = append(parts, strings.ToUpper(c.Key))
	return strings.Join(parts, "+")
}

// fireFromHook is the only entry from the hook proc. It must not block
// or do disk/IPC work — those happen in runTriggerDrain.
func (s *quickChatHotkey) fireFromHook() {
	select {
	case s.triggerCh <- struct{}{}:
	default:
		// Already a pending trigger; coalesce.
	}
}

func (s *quickChatHotkey) fireClosedEyesFromHook() {
	select {
	case s.closedEyesCh <- struct{}{}:
	default:
		// Already pending; coalesce.
	}
}

// captureForRecording funnels a recorded VK from the hook proc to the
// drain goroutine without blocking the OS input thread.
func (s *quickChatHotkey) captureForRecording(vk uint32) {
	select {
	case s.recordingCh <- vk:
	default:
		// Drain hasn't caught up; drop. The recorder UI is interactive
		// and another keypress will arrive immediately.
	}
}

// runTriggerDrain handles each fired trigger off the hook thread:
// resolves the foreground app, opens the floating panel, and emits the
// renderer event.
func (s *quickChatHotkey) runTriggerDrain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.triggerCh:
			s.handleTrigger()
		}
	}
}

func (s *quickChatHotkey) runClosedEyesDrain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closedEyesCh:
			s.handleClosedEyesTrigger()
		}
	}
}

// runRecordingDrain consumes captured VK codes from the recorder
// channel, snapshots the live modifier state (the hook fires before
// the OS updates focus or hands events to the renderer, so a single
// GetAsyncKeyState read is the source of truth), and emits the combo
// to the renderer via Wails events.
func (s *quickChatHotkey) runRecordingDrain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case vk := <-s.recordingCh:
			s.emitRecordedCombo(vk)
		}
	}
}

func (s *quickChatHotkey) emitRecordedCombo(vk uint32) {
	quickChatHotkeyMu.Lock()
	app := quickChatApp
	quickChatHotkeyMu.Unlock()
	if app == nil || app.ctx == nil {
		return
	}
	key, ok := jsKeyForVK(vk)
	if !ok {
		// Unknown VK (media keys, IME, etc.) — silently ignore. The
		// recorder will keep waiting for the next press.
		return
	}
	combo := QuickChatHotkeyConfig{
		Ctrl:  isKeyHeld(hotkeyVKControl),
		Alt:   isKeyHeld(hotkeyVKMenu),
		Shift: isKeyHeld(hotkeyVKShift),
		Meta:  isKeyHeld(hotkeyVKLWin) || isKeyHeld(hotkeyVKRWin),
		Key:   key,
	}
	wailsruntime.EventsEmit(app.ctx, "quickChat:hotkeyRecorded", combo)
}

func (s *quickChatHotkey) handleTrigger() {
	quickChatHotkeyMu.Lock()
	app := quickChatApp
	quickChatHotkeyMu.Unlock()
	if app == nil {
		return
	}

	bundleID := frontmostExeBasename(s.ownPID)
	appName := bundleIDPrettyName(bundleID)

	// Preferred path: signal the Spotlight helper. It owns the
	// pill + conversation floating windows (see DESIGN.md in
	// quick-chat-spotlight/). Falls back to the in-app overlay when
	// the helper isn't installed (typical in dev) or hasn't reached
	// /health yet.
	if app.spotlight != nil {
		// The screen chip (computer-chat v1.0.0 rule 8): none with Closed
		// Eyes on or for a sensitive app.
		chipBundle, chipName, chipTitle := app.spotlightScreenChip(bundleID, appName)
		if err := app.spotlight.Show(chipBundle, chipName, chipTitle); err == nil {
			return
		} else if err != ErrSpotlightUnavailable {
			log.Printf("[quickchat] spotlight.Show failed: %v — falling back to in-app overlay", err)
		}
	}
	app.ShowQuickChatWindow(bundleID)
}

func (s *quickChatHotkey) handleClosedEyesTrigger() {
	quickChatHotkeyMu.Lock()
	app := quickChatApp
	quickChatHotkeyMu.Unlock()
	if app == nil {
		return
	}
	app.toggleClosedEyesModeFromHotkey()
}

// frontmostExeBasename returns the foreground process's .exe basename
// (e.g. "Code.exe", "Discord.exe") to use as the bundleID equivalent
// the macOS pipeline expects. Returns empty string if the foreground
// process is Yansu itself or cannot be queried.
func frontmostExeBasename(ownPID uint32) string {
	hwnd, _, _ := procHotkeyGetForegroundWindow.Call()
	if hwnd == 0 {
		return ""
	}

	var pid uint32
	procHotkeyGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 || pid == ownPID {
		return ""
	}

	hProc, _, _ := procHotkeyOpenProcess.Call(hotkeyProcessQueryLimited, 0, uintptr(pid))
	if hProc == 0 {
		return ""
	}
	defer procHotkeyCloseHandle.Call(hProc)

	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	r, _, _ := procHotkeyQueryFullProcessImageNameW.Call(
		hProc, 0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if r == 0 {
		return ""
	}
	full := windows.UTF16ToString(buf[:size])
	if full == "" {
		return ""
	}
	return strings.ToLower(filepath.Base(full))
}
