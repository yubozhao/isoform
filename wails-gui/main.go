package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"wails-gui/internal/platform/ocr"
	"wails-gui/internal/singleinstance"
	"wails-gui/internal/worktree"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

// frontendAssets exposes the embedded frontend/dist tree to other files in
// this package (e.g. browser_server.go) so the browser bridge can serve the
// same production UI without relying on on-disk files.
func frontendAssets() embed.FS { return assets }

// Keep three 10 MiB files, rotating during a session as well as on startup.
const maxLogSize = 10 * 1024 * 1024

// singleInstanceID names the OS-level lock and IPC channel that gate the
// second-launch behaviour. Both the lock acquisition and the IPC send/receive
// must use the same identifier.
//
// The id carries the flavor, so a development Isoform and an installed Yansu
// never treat each other as the running instance.
var singleInstanceID = appFlavor() + "-desktop"

// msgFocus is the IPC message a second launch sends to ask the running
// instance to bring its window to the front.
const msgFocus = "focus"

// initLogFile sets up file-based logging to ~/.yansu-agent/yansu-gui.log.
// The log file is the primary output. When process stderr is captured in its
// own file, keep ordinary logs here so they remain bounded during a session.
// Otherwise stderr is best-effort (Windows GUI apps have no console, so stderr
// writes fail — io.MultiWriter would discard ALL output if stderr came first).
//
// The active file plus .old and .old.2 retain recent diagnostic history.
func initLogFile() *rotatingAppLog {
	logDir, err := flavorDataDir()
	if err != nil {
		return nil
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil
	}

	logPath := filepath.Join(logDir, "yansu-gui.log")
	f, err := newRotatingAppLog(logPath, maxLogSize)
	if err != nil {
		return nil
	}
	log.SetOutput(f)
	if processStderrLog == nil {
		// A broken stderr must not prevent ordinary file logging.
		log.SetOutput(io.MultiWriter(f, &bestEffortWriter{os.Stderr}))
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	return f
}

// bestEffortWriter wraps a writer and silently ignores errors.
// Used for stderr on Windows GUI apps where the handle may be invalid.
type bestEffortWriter struct{ w io.Writer }

func (b *bestEffortWriter) Write(p []byte) (int, error) {
	n, _ := b.w.Write(p)
	if n == 0 {
		n = len(p)
	}
	return n, nil
}

// initStartupLogs is also used when startup continues without a singleton
// lock. Binding introspection must never open or rotate application logs.
func initStartupLogs(bindingsBuild, ownsSingleton bool) func() {
	if bindingsBuild {
		return func() {}
	}
	var logFile *rotatingAppLog
	if ownsSingleton {
		stderrErr := initStderrLog()
		crashErr := initCrashLog()
		logFile = initLogFile()
		if crashErr != nil {
			log.Printf("[main] could not capture runtime crashes: %v", crashErr)
		}
		if stderrErr != nil {
			log.Printf("[main] could not capture process stderr: %v", stderrErr)
		}
		if logFile != nil {
			fmt.Fprintf(os.Stderr, "[yansu] Logging to %s\n", logFile.Name())
		}
	}
	// A lock-file failure does not stop startup. Keep the Learning decisions
	// history available on that path as well as for the singleton owner.
	initTriageLog()
	return func() {
		closeTriageLog()
		if logFile != nil {
			_ = logFile.Close()
		}
	}
}

func main() {
	recordLaunchStage("process_start")
	if code, handled := runCrashMonitorCommand(os.Args[1:]); handled {
		os.Exit(code)
	}
	if code, handled := runUpdateHelperCommand(os.Args[1:]); handled {
		os.Exit(code)
	}
	if code, handled := runUpdateProbeCommand(os.Args[1:]); handled {
		os.Exit(code)
	}
	// Self-rollback for a quit install that never started healthy. It runs
	// before the singleton lock and before Wails, so it works even when this
	// version crashes during Wails startup.
	if home, err := flavorDataDir(); err == nil {
		if runQuitInstallTrial(home, Version, findAppBundle(), launchRestoredApp) {
			os.Exit(0)
		}
	}
	if exitCode, handled := runPurposeMailBackfillCommand(os.Args[1:], os.Stderr); handled {
		os.Exit(exitCode)
	}
	if exitCode, handled := runLoginItemCommand(os.Args[1:]); handled {
		os.Exit(exitCode)
	}

	// The build flavor decides the data folder for this process, the agent
	// packages, the bundled CLI and the single-instance lock.
	if err := exportAgentHome(); err != nil {
		log.Printf("[flavor] invalid data folder: %v", err)
		return
	}
	singleinstance.BaseDirName = configDirName
	worktree.BaseDirName = configDirName

	// Allocate and immediately hide a console for this GUI process.
	// Must happen before any child processes are spawned so they all
	// inherit this hidden console instead of creating visible ones.
	initHiddenConsole()

	// Single-instance guard. Two concurrent yansu-desktop processes were
	// fighting over the activity DB and GDI screen-capture resources, which
	// surfaced in user-visible ways (input lag, GetDIBits failures, partial
	// WAL checkpoints).
	//
	// On the second launch we don't just exit silently — we forward a
	// "focus" message to the running instance over local IPC so the user
	// sees its window come to the front, matching what Slack / Discord /
	// VS Code do.
	//
	// Skipped in `bindings` build mode: the wails binding generator compiles
	// this main package and runs the binary purely for type introspection
	// (see bindings_mode_off.go for the build-tag wiring). In that mode an
	// already-running Yansu Desktop instance is normal — we'd otherwise fail
	// every binding regeneration during dev/CI builds, leaving the frontend
	// pointing at stale generated files (which is how this bug was found).
	var instance *singleinstance.Instance
	if !isWailsBindingsBuild {
		var lockErr error
		instance, lockErr = singleinstance.Acquire(singleInstanceID)
		if lockErr != nil {
			if errors.Is(lockErr, singleinstance.ErrAlreadyRunning) {
				if err := singleinstance.Send(singleInstanceID, msgFocus); err != nil {
					log.Printf("[main] could not signal running instance: %v", err)
				} else {
					log.Println("[main] another instance is running; sent focus and exiting")
				}
				return
			}
			// Lock acquisition itself failed (couldn't open lock file, etc.).
			// Don't block startup — log and continue without the guarantee.
			log.Printf("[main] single-instance lock unavailable, continuing anyway: %v", lockErr)
		}
		if instance != nil {
			defer instance.Release()
		}
	}

	// Only the singleton owner redirects standard logs and process stderr.
	// Triage history also remains available if the lock is unavailable.
	closeLogs := initStartupLogs(isWailsBindingsBuild, instance != nil)
	recordLaunchStage("logging_ready")
	defer closeLogs()

	if !isWailsBindingsBuild {
		// OCR starts with the optional services after the first screen.
		defer ocr.CloseHelper()
	}

	app := NewApp()
	recordLaunchStage("app_constructed")
	if !isWailsBindingsBuild && instance != nil {
		// Restore the verified download for the update card. Only the explicit
		// restart action may replace the app.
		app.restorePendingUpdate()
	}

	// Hand the IPC channel to the App so it can drain "focus" messages
	// from second-launch attempts in startup (where the Wails ctx is
	// available). nil when the lock failed and we're running unguarded.
	if instance != nil {
		app.singletonMessages = instance.Messages()
	}

	// Open the progress window immediately; saved geometry is restored after storage opens.

	appOptions := &options.App{
		Title:            productName(),
		Width:            1200,
		Height:           800,
		WindowStartState: options.Normal,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: brandWindowBackground(),
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnDomReady:       func(context.Context) { recordLaunchStage("window_dom_ready") },
		OnBeforeClose: func(ctx context.Context) bool {
			return app.beforeWindowClose(ctx, runtime.GOOS)
		},
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop: true,
		},
		Bind: []any{
			app,
		},
	}

	// First-run setup (setup_window.go) opens at its own size. The launch state is
	// read here, before Wails creates the window, so the setup window never shows at
	// the main size first; the menu bar mark's launch visibility is set here too.
	if !isWailsBindingsBuild {
		if launch := prepareSetupWindowAtLaunch(); launch.SetupNeeded {
			w, h := setupWindowSize()
			appOptions.Width = int(w)
			appOptions.Height = int(h)
		}
	}

	switch runtime.GOOS {
	case "darwin":
		// Show snapshot progress even for login launches. Successful startup
		// installs the native close-to-menu-bar hook and applies background mode.
		appOptions.Mac = &mac.Options{
			TitleBar:             mac.TitleBarHidden(),
			WindowIsTranslucent:  true,
			WebviewIsTransparent: true,
			OnUrlOpen: func(url string) {
				app.handleURLOpen(url)
			},
		}
	case "windows":
		// OnBeforeClose hides a ready app; closing during storage startup quits.
		winOpts := &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		}
		if isWindows11OrGreater() {
			winOpts.WebviewIsTransparent = true
			winOpts.WindowIsTranslucent = true
			winOpts.BackdropType = windows.Mica
		}
		appOptions.Windows = winOpts
	}

	if !isWailsBindingsBuild {
		enableWindowDiagnostics()
	}
	recordLaunchStage("wails_run")
	err := wails.Run(appOptions)
	if err != nil {
		log.Fatal(err)
	}
}
