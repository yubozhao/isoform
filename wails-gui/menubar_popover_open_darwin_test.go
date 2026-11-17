//go:build darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The popover opens on click without waiting on Go (Bo, 2026-10-27: a 7 s
// read meant nothing appeared, or the native menu did), keeps our page on a
// privacy invalidation, and has dark chrome from the first open when the
// saved theme is dark.
func TestMenuBarPopoverOpensAtOnce(t *testing.T) {
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang is unavailable")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	source := filepath.Join(wd, "menubar_popover_darwin.m")
	if override := os.Getenv("ISOFORM_MENU_BAR_POPOVER_SOURCE"); override != "" {
		source = override
	}
	harness := filepath.Join(wd, "testdata", "menubar_popover_harness", "harness.m")
	binary := filepath.Join(dir, "menubar-popover-harness")
	compileCtx, cancelCompile := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCompile()
	compile := exec.CommandContext(compileCtx, clang, "-fno-objc-arc", "-fblocks", "-w", "-I", wd,
		harness, source, "-o", binary, "-framework", "Cocoa", "-framework", "WebKit", "-framework", "QuartzCore")
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile popover harness: %v\n%s", err, output)
	}
	page := func(theme string) (string, string) {
		view := menuBarPreloadView("en")
		view.Theme = theme
		htmlPath, modelPath := filepath.Join(dir, theme+".html"), filepath.Join(dir, theme+".json")
		data, _ := json.Marshal(view)
		if err := os.WriteFile(htmlPath, []byte(renderMenuBarHTML(view)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(modelPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		return htmlPath, modelPath
	}
	lightHTML, lightModel := page("paper")
	darkHTML, darkModel := page("onedark")

	t.Run("click_shows_without_go", func(t *testing.T) {
		// The bar is 100 ms; the best of three runs keeps a loaded machine
		// from failing it while a real regression still does.
		best := -1
		for attempt := 0; attempt < 3; attempt++ {
			out := runMenuBarPopoverHarness(t, binary, lightHTML, lightModel, "instant", "SHOWN", 12*time.Second)
			var ms, goBefore, gap, width, opaque int
			if _, err := fmt.Sscanf(out[strings.Index(out, "SHOWN"):], "SHOWN ms=%d go_before_show=%d gap=%d width=%d opaque=%d", &ms, &goBefore, &gap, &width, &opaque); err != nil {
				t.Fatalf("parse %q: %v", out, err)
			}
			if goBefore != 0 {
				t.Fatal("the click called into Go before the popover showed")
			}
			// Our own panel (1.0): 5 pt below the menu bar, 360 pt plus the
			// shadow's room, transparent around the page's border and corners.
			if gap != 5 || width != 396 || opaque != 0 {
				t.Fatalf("panel placement gap=%d width=%d opaque=%d, want 5, 396, 0", gap, width, opaque)
			}
			if best < 0 || ms < best {
				best = ms
			}
			if best <= 100 {
				break
			}
		}
		if best > 100 {
			t.Fatalf("click to shown took %d ms at best, want at most 100", best)
		}
	})
	t.Run("click_before_page_opens_when_ready", func(t *testing.T) {
		runMenuBarPopoverHarness(t, binary, lightHTML, lightModel, "early", "SHOWN", 12*time.Second)
	})
	t.Run("invalidation_while_opening_still_opens_our_popover", func(t *testing.T) {
		runMenuBarPopoverHarness(t, binary, lightHTML, lightModel, "invalidate_opening", "SHOWN withdrawn=1 native_menu=0", 12*time.Second)
	})
	t.Run("invalidation_keeps_popover_and_empties_it", func(t *testing.T) {
		runMenuBarPopoverHarness(t, binary, lightHTML, lightModel, "invalidate", "STILL_SHOWN withdrawn=1", 12*time.Second)
	})
	t.Run("dark_theme_has_dark_chrome_on_first_open", func(t *testing.T) {
		runMenuBarPopoverHarness(t, binary, darkHTML, darkModel, "dark", "DARK", 12*time.Second)
	})
}

func runMenuBarPopoverHarness(t *testing.T, binary, htmlPath, modelPath, mode, expected string, timeout time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, htmlPath, modelPath, mode).CombinedOutput()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 77 {
			t.Skip("no active WindowServer status item")
		}
		t.Fatalf("popover harness %s: %v\n%s", mode, err, output)
	}
	t.Logf("popover harness %s output:\n%s", mode, output)
	if !strings.Contains(string(output), expected) {
		t.Fatalf("popover harness %s did not report %q:\n%s", mode, expected, output)
	}
	return string(output)
}

// The first second after launch: "Starting…" with no brief row and no
// Waiting on you section or See all line (no placeholders, shell-review B5;
// no count, evening walk B7); never "Loading…" or a claim such as "Nothing is
// waiting on you".
func TestMenuBarLoadingPlaceholderMakesNoClaims(t *testing.T) {
	view := menuBarPreloadView("en")
	if view.Status != "starting" || view.State != "Starting…" || !view.Loading || len(view.Menu) == 0 {
		t.Fatalf("preload %+v", view)
	}
	html := renderMenuBarHTML(view)
	if !strings.Contains(html, `"loading":true`) {
		t.Fatal("the loading flag does not reach the page")
	}
	for _, guard := range []string{
		"const pending = !!model.loading || !!model.withdrawn || !!model.waitingPending;",
		"const none = !pending && !failed && !model.next;",
		"row.hidden = !!model.loading;",
		"el('wsec').hidden = pending || failed;",
		"el('done').hidden = !none;",
		"hb.disabled = status === 'starting';",
	} {
		if !strings.Contains(html, guard) {
			t.Fatalf("popover no longer guards the first second: %s", guard)
		}
	}
	if strings.Contains(html, "Loading") {
		t.Fatal(`the page says "Loading"`)
	}
}

// The popover's Quick chat row opens the same Quick chat as its shortcut,
// never the old panel session (redesign review on #1132).
func TestMenuBarQuickChatUsesTheShortcutsOpener(t *testing.T) {
	source, err := os.ReadFile("menubar_controller_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, `case "quickChat":`)
	if start < 0 {
		t.Fatal("no quickChat command")
	}
	block := text[start:]
	if end := strings.Index(block[1:], "\n\tcase "); end > 0 {
		block = block[:end+1]
	}
	if !strings.Contains(block, "openQuickChat(") || strings.Contains(block, "ShowQuickChatWindow(") {
		t.Fatalf("quickChat command = %q", block)
	}
}
