//go:build windows

package activitymon

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	version  = syscall.NewLazyDLL("version.dll")

	procSetWindowsHookExW          = user32.NewProc("SetWindowsHookExW")
	procCallNextHookEx             = user32.NewProc("CallNextHookEx")
	procUnhookWindowsHookEx        = user32.NewProc("UnhookWindowsHookEx")
	procPeekMessageW               = user32.NewProc("PeekMessageW")
	procGetMessageW                = user32.NewProc("GetMessageW")
	procPostThreadMessageW         = user32.NewProc("PostThreadMessageW")
	procTranslateMessage           = user32.NewProc("TranslateMessage")
	procDispatchMessageW           = user32.NewProc("DispatchMessageW")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW             = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	procSetWinEventHook            = user32.NewProc("SetWinEventHook")
	procUnhookWinEvent             = user32.NewProc("UnhookWinEvent")
	procGetModuleHandleW           = kernel32.NewProc("GetModuleHandleW")
	procGetCurrentThreadId         = kernel32.NewProc("GetCurrentThreadId")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procGetFileVersionInfoSizeW    = version.NewProc("GetFileVersionInfoSizeW")
	procGetFileVersionInfoW        = version.NewProc("GetFileVersionInfoW")
	procVerQueryValueW             = version.NewProc("VerQueryValueW")
)

const (
	whKeyboardLL = 13
	whMouseLL    = 14

	wmKeydown     = 0x0100
	wmSyskeydown  = 0x0104
	wmLbuttondown = 0x0201
	wmRbuttondown = 0x0204

	eventSystemForeground = 0x0003
	winEventOutOfContext  = 0x0000

	wmQuit = 0x0012

	processQueryLimitedInformation = 0x1000

	// rawEventChanSize is the buffer for hook→drain handoff. The hook proc
	// is on the OS-level input thread; if the drain falls behind, dropping
	// is safer than blocking (a stuck hook proc freezes mouse input).
	rawEventChanSize = 256

	vkShift   = 0x10
	vkControl = 0x11
	vkMenu    = 0x12 // Alt
	vkLWin    = 0x5B
	vkRWin    = 0x5C
)

// KBDLLHOOKSTRUCT — low-level keyboard hook data.
type kbdllHookStruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// MSLLHOOKSTRUCT — low-level mouse hook data.
type msllHookStruct struct {
	X           int32
	Y           int32
	MouseData   uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

// MSG structure for PeekMessage/GetMessage.
type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	PtX     int32
	PtY     int32
}

// globalMonitor is the singleton used by hook callbacks.
// Windows hook callbacks must be package-level or closures;
// using a global avoids the need to pass context through uintptr tricks.
var globalMonitor *windowsMonitor

// appNameCache caches exe path -> display name lookups to avoid
// repeated disk I/O in performance-sensitive hook callbacks.
var (
	appNameCacheMu  sync.RWMutex
	appNameCacheMap = make(map[string]string, 32)
)

func cachedAppName(exePath string) string {
	appNameCacheMu.RLock()
	name, ok := appNameCacheMap[exePath]
	appNameCacheMu.RUnlock()
	if ok {
		return name
	}

	name = fileDescriptionFromExe(exePath)
	if name == "" {
		name = baseNameWithoutExt(exePath)
	}

	appNameCacheMu.Lock()
	appNameCacheMap[exePath] = name
	appNameCacheMu.Unlock()
	return name
}

// rawEvent is the minimal payload pushed from a hook callback to the drain
// goroutine. We deliberately avoid touching the foreground window (disk I/O,
// process queries) inside the hook proc — that work is done in the drain.
type rawEvent struct {
	kind   EventKind
	timeMs int64
	vk     uint32
	clickX float64
	clickY float64
}

// windowsMonitor implements Monitor using Windows low-level hooks.
type windowsMonitor struct {
	allowed  func() bool
	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	callback func(ActivityEvent)

	events   chan rawEvent
	drops    atomic.Uint64
	threadID uint32

	kbHook       uintptr
	mouseHook    uintptr
	winEventHook uintptr
}

// FrontmostAppBundleID returns the lowercase executable basename of the
// foreground process (e.g. "wisprflow.exe"). Windows has no native bundle ID
// concept, so we use the exe basename as the closest stable identifier — it
// survives install-path differences and is what users naturally recognize.
// Used by background-listening filtering (voice_input_apps.go) and triager
// heuristics (activity_triage.isLikelyNonUserSpeech) the same way macOS
// uses real bundle IDs.
func FrontmostAppBundleID() string {
	_, _, _, bundleID := frontmostWindowExe()
	return bundleID
}

// FrontmostAppPID returns the process id of the foreground window's owner,
// or 0 when there is none.
func FrontmostAppPID() int {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return int(pid)
}

// FrontmostAppName returns the display name of the foreground application.
func FrontmostAppName() string {
	_, _, appName, _ := frontmostWindowExe()
	return appName
}

// FrontmostAppInfo returns the foreground window's app identity and title.
// Reuses getForegroundWindowInfo's GetForegroundWindow + QueryFullProcessImageNameW
// dance so we don't open the process twice.
func FrontmostAppInfo() AppInfo {
	_, title, appName, bundleID := frontmostWindowExe()
	return AppInfo{
		BundleID:    bundleID,
		AppName:     appName,
		WindowTitle: title,
	}
}

// frontmostWindowExe is the shared core for both FrontmostAppBundleID and
// FrontmostAppInfo: a single GetForegroundWindow + OpenProcess sequence that
// returns the window handle, title, friendly app name, and the lowercase
// exe basename used as the Windows-side "bundle ID".
func frontmostWindowExe() (hwnd uintptr, title, appName, bundleID string) {
	hwnd, _, _ = procGetForegroundWindow.Call()
	if hwnd == 0 {
		return
	}

	buf := make([]uint16, 512)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	title = syscall.UTF16ToString(buf)

	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return
	}

	hProc, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if hProc == 0 {
		return
	}
	defer procCloseHandle.Call(hProc)

	exePath := queryProcessImageName(hProc)
	if exePath == "" {
		return
	}

	appName = cachedAppName(exePath)
	bundleID = strings.ToLower(baseNameWithExt(exePath))
	return
}

// baseNameWithExt extracts just the filename (with extension) from a path.
// Used to derive the Windows-side bundle-ID stand-in. Kept separate from
// baseNameWithoutExt — that one is used for friendly-name fallback when
// the version-info FileDescription is unavailable.
func baseNameWithExt(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '\\' || path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// FrontmostWindowBounds returns zero bounds on Windows.
func FrontmostWindowBounds() WindowBounds { return WindowBounds{} }

// FindVisibleSensitiveWindows returns nil on Windows.
func FindVisibleSensitiveWindows(_ []string) []WindowBounds { return nil }

// New creates a new Windows Monitor.
func New(allowed func() bool) Monitor {
	m := &windowsMonitor{allowed: allowed}
	globalMonitor = m
	return m
}

func (m *windowsMonitor) Start(ctx context.Context, callback func(ActivityEvent)) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	m.callback = callback
	m.events = make(chan rawEvent, rawEventChanSize)
	m.running = true
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	go m.runHookLoop(ctx)
	go m.runEventDrain(ctx)
	return nil
}

func (m *windowsMonitor) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.mu.Unlock()
}

func (m *windowsMonitor) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// runHookLoop installs low-level hooks and runs a Windows message loop.
// Windows hooks require the message pump to run on the same OS thread
// that installed them, so we lock the goroutine.
//
// The pump uses blocking GetMessage rather than PeekMessage+Sleep. WH_MOUSE_LL
// and WH_KEYBOARD_LL deliver via SendMessage from the OS input thread; while
// our thread isn't pumping, that SendMessage blocks the input thread, which
// shows up as occasional cursor lag. GetMessage stays in an alertable wait so
// hook callbacks fire as soon as the OS posts them, with no 10 ms polling
// floor.
func (m *windowsMonitor) runHookLoop(ctx context.Context) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	tid, _, _ := procGetCurrentThreadId.Call()
	m.mu.Lock()
	m.threadID = uint32(tid)
	m.mu.Unlock()

	// Force the system to allocate the thread message queue before any hook
	// is installed. PeekMessage with PM_NOREMOVE is the documented way.
	var probe winMsg
	procPeekMessageW.Call(uintptr(unsafe.Pointer(&probe)), 0, 0, 0, 0)

	moduleHandle, _, _ := procGetModuleHandleW.Call(0)

	// Install low-level keyboard hook
	kbHook, _, _ := procSetWindowsHookExW.Call(
		whKeyboardLL,
		syscall.NewCallback(keyboardHookProc),
		moduleHandle,
		0,
	)
	m.mu.Lock()
	m.kbHook = kbHook
	m.mu.Unlock()

	// Install low-level mouse hook
	mouseHook, _, _ := procSetWindowsHookExW.Call(
		whMouseLL,
		syscall.NewCallback(mouseHookProc),
		moduleHandle,
		0,
	)
	m.mu.Lock()
	m.mouseHook = mouseHook
	m.mu.Unlock()

	// Install foreground window change event hook
	winEvtHook, _, _ := procSetWinEventHook.Call(
		eventSystemForeground,
		eventSystemForeground,
		0,
		syscall.NewCallback(winEventProc),
		0, 0,
		winEventOutOfContext,
	)
	m.mu.Lock()
	m.winEventHook = winEvtHook
	m.mu.Unlock()

	// Wake the pump on cancel by posting WM_QUIT to this thread.
	go func() {
		<-ctx.Done()
		procPostThreadMessageW.Call(tid, wmQuit, 0, 0)
	}()

	// Blocking message pump. GetMessage returns:
	//   > 0: dispatch normally
	//   = 0: WM_QUIT received → exit
	//   < 0: error → exit
	var message winMsg
	for {
		ret, _, _ := procGetMessageW.Call(
			uintptr(unsafe.Pointer(&message)),
			0, 0, 0,
		)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
	m.unhookAll()
}

// runEventDrain consumes raw hook events on a regular goroutine, performs the
// expensive foreground-window lookups (which can hit disk via version-info
// reads) off the hook thread, and forwards the enriched ActivityEvent to the
// user callback. Keeping this work off the hook thread is what eliminates
// the 300 ms LowLevelHooksTimeout risk that surfaces as system-wide input lag.
func (m *windowsMonitor) runEventDrain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-m.events:
			if !m.allowed() {
				continue
			}
			// frontmostWindowExe (not the 3-arg getForegroundWindowInfo
			// wrapper) so we keep the lowercase exe basename as the
			// Windows-side bundleID. Without this every event hits the
			// store with AppBundleID="" and the activity popup's
			// same-app snapshot matcher silently falls back to
			// nearest-by-time-only — routing TIM event clicks to a
			// Cursor snapshot that happens to sit a few seconds away
			// and frustrating "why doesn't my event show its app"
			// diagnoses.
			_, title, appName, bundleID := frontmostWindowExe()
			evt := ActivityEvent{
				Timestamp:   ev.timeMs,
				Kind:        ev.kind,
				AppName:     appName,
				AppBundleID: bundleID,
				WindowTitle: title,
			}
			switch ev.kind {
			case EventKeyboard:
				evt.KeyCode = uint16(ev.vk)
				if char := vkToString(ev.vk); char != "" {
					evt.KeyChars = []string{char}
				}
			case EventClick:
				evt.ClickX = ev.clickX
				evt.ClickY = ev.clickY
			}
			if cb := m.getCallback(); cb != nil {
				cb(evt)
			}
		}
	}
}

// enqueueRaw pushes an event from a hook callback. Non-blocking; on a full
// channel we drop and bump the drop counter, since blocking the hook proc
// is what causes the lag we're trying to fix.
func (m *windowsMonitor) enqueueRaw(ev rawEvent) {
	if m == nil || m.events == nil {
		return
	}
	select {
	case m.events <- ev:
	default:
		m.drops.Add(1)
	}
}

func (m *windowsMonitor) unhookAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.kbHook != 0 {
		procUnhookWindowsHookEx.Call(m.kbHook)
		m.kbHook = 0
	}
	if m.mouseHook != 0 {
		procUnhookWindowsHookEx.Call(m.mouseHook)
		m.mouseHook = 0
	}
	if m.winEventHook != 0 {
		procUnhookWinEvent.Call(m.winEventHook)
		m.winEventHook = 0
	}
}

func (m *windowsMonitor) getCallback() func(ActivityEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callback
}

// keyboardHookProc is the global low-level keyboard hook callback.
// Must return as fast as possible: it runs on the OS input thread and any
// stall here freezes keyboard/mouse input system-wide. Heavy work
// (foreground-window lookup, user callback) is deferred to runEventDrain.
func keyboardHookProc(nCode, wParam, lParam uintptr) uintptr {
	if nCode == 0 && (wParam == wmKeydown || wParam == wmSyskeydown) {
		// lParam is guaranteed by Windows to point to a KBDLLHOOKSTRUCT.
		kb := (*kbdllHookStruct)(unsafe.Pointer(lParam)) //nolint:govet // Windows callback contract
		vk := kb.VkCode

		if vk != vkShift && vk != vkControl && vk != vkMenu && vk != vkLWin && vk != vkRWin {
			globalMonitor.enqueueRaw(rawEvent{
				kind:   EventKeyboard,
				timeMs: time.Now().UnixMilli(),
				vk:     vk,
			})
		}
	}

	ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
	return ret
}

// mouseHookProc is the global low-level mouse hook callback.
// Must return as fast as possible — see keyboardHookProc.
func mouseHookProc(nCode, wParam, lParam uintptr) uintptr {
	if nCode == 0 && (wParam == wmLbuttondown || wParam == wmRbuttondown) {
		// lParam is guaranteed by Windows to point to a MSLLHOOKSTRUCT.
		ms := (*msllHookStruct)(unsafe.Pointer(lParam)) //nolint:govet // Windows callback contract
		globalMonitor.enqueueRaw(rawEvent{
			kind:   EventClick,
			timeMs: time.Now().UnixMilli(),
			clickX: float64(ms.X),
			clickY: float64(ms.Y),
		})
	}

	ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
	return ret
}

// winEventProc is the global callback for foreground window changes.
// Out-of-context delivery still runs on the hook thread, so we keep it
// trivial and let the drain enrich with title/app name.
func winEventProc(hWinEventHook, event, hwnd, idObject, idChild, dwEventThread, dwmsEventTime uintptr) uintptr {
	globalMonitor.enqueueRaw(rawEvent{
		kind:   EventAppSwitch,
		timeMs: time.Now().UnixMilli(),
	})
	return 0
}

// getForegroundWindowInfo returns the foreground window handle, title, and display name.
// Delegates to frontmostWindowExe (the shared core) and discards the bundle-ID
// stand-in that internal callers don't need.
func getForegroundWindowInfo() (hwnd uintptr, title, appName string) {
	hwnd, title, appName, _ = frontmostWindowExe()
	return
}

// queryProcessImageName returns the full executable path via QueryFullProcessImageNameW.
func queryProcessImageName(hProcess uintptr) string {
	pathBuf := make([]uint16, 1024)
	pathLen := uint32(len(pathBuf))
	ret, _, _ := procQueryFullProcessImageNameW.Call(
		hProcess, 0,
		uintptr(unsafe.Pointer(&pathBuf[0])),
		uintptr(unsafe.Pointer(&pathLen)),
	)
	if ret == 0 || pathLen == 0 {
		return ""
	}
	return syscall.UTF16ToString(pathBuf[:pathLen])
}

// baseNameWithoutExt extracts the file name from a path and strips the .exe extension.
func baseNameWithoutExt(path string) string {
	name := path
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '\\' || path[i] == '/' {
			name = path[i+1:]
			break
		}
	}
	if len(name) > 4 {
		ext := name[len(name)-4:]
		if ext == ".exe" || ext == ".EXE" || ext == ".Exe" {
			name = name[:len(name)-4]
		}
	}
	return name
}

// fileDescriptionFromExe reads the FileDescription from the executable's version info.
// Returns empty string if unavailable.
func fileDescriptionFromExe(exePath string) string {
	pathPtr, _ := syscall.UTF16PtrFromString(exePath)

	var dummy uint32
	size, _, _ := procGetFileVersionInfoSizeW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&dummy)),
	)
	if size == 0 {
		return ""
	}

	data := make([]byte, size)
	ret, _, _ := procGetFileVersionInfoW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		0,
		size,
		uintptr(unsafe.Pointer(&data[0])),
	)
	if ret == 0 {
		return ""
	}

	// Query the translation table to build the correct sub-block path
	type langAndCodePage struct {
		Language uint16
		CodePage uint16
	}

	var transPtr *langAndCodePage
	var transLen uint32
	subBlock, _ := syscall.UTF16PtrFromString(`\VarFileInfo\Translation`)
	ret, _, _ = procVerQueryValueW.Call(
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(unsafe.Pointer(subBlock)),
		uintptr(unsafe.Pointer(&transPtr)),
		uintptr(unsafe.Pointer(&transLen)),
	)
	if ret == 0 || transLen < uint32(unsafe.Sizeof(langAndCodePage{})) {
		return ""
	}

	lang := transPtr.Language
	cp := transPtr.CodePage

	descPath := fmt.Sprintf(`\StringFileInfo\%04x%04x\FileDescription`, lang, cp)
	descPathPtr, _ := syscall.UTF16PtrFromString(descPath)

	var descPtr *uint16
	var descLen uint32
	ret, _, _ = procVerQueryValueW.Call(
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(unsafe.Pointer(descPathPtr)),
		uintptr(unsafe.Pointer(&descPtr)),
		uintptr(unsafe.Pointer(&descLen)),
	)
	if ret == 0 || descLen == 0 || descPtr == nil {
		return ""
	}

	desc := syscall.UTF16ToString(unsafe.Slice(descPtr, descLen))
	return desc
}

// vkToString converts a virtual key code to a readable string.
func vkToString(vk uint32) string {
	switch {
	case vk >= 0x30 && vk <= 0x39: // 0-9
		return string(rune(vk))
	case vk >= 0x41 && vk <= 0x5A: // A-Z
		return string(rune(vk))
	case vk >= 0x60 && vk <= 0x69: // Numpad 0-9
		return string(rune('0' + vk - 0x60))
	case vk == 0x0D:
		return "Return"
	case vk == 0x09:
		return "Tab"
	case vk == 0x20:
		return "Space"
	case vk == 0x08:
		return "Backspace"
	case vk == 0x2E:
		return "Delete"
	case vk == 0x1B:
		return "Escape"
	case vk == 0x25:
		return "Left"
	case vk == 0x26:
		return "Up"
	case vk == 0x27:
		return "Right"
	case vk == 0x28:
		return "Down"
	case vk == 0x24:
		return "Home"
	case vk == 0x23:
		return "End"
	case vk == 0x21:
		return "Page Up"
	case vk == 0x22:
		return "Page Down"
	case vk >= 0x70 && vk <= 0x87: // F1-F24
		n := int(vk - 0x70 + 1)
		return "F" + fKeyNum(n)
	default:
		return ""
	}
}

func fKeyNum(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// GetStats reports drop counters from the hook→drain channel. RingPeakDepth
// and Wakeups stay zero — those are darwin ring-buffer concepts.
func GetStats() Stats {
	m := globalMonitor
	if m == nil {
		return Stats{}
	}
	return Stats{RingDrops: m.drops.Load()}
}
