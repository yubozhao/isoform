package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Design S12 (computer conformance answers v1.5.0): when the screen state
// can't be read, the popover says so with Try again. It is never shown as
// seeing or as off, and Closed Eyes still wins and stays pressable.
func TestMenuBarStatusSaysWhenTheStateCantBeRead(t *testing.T) {
	for _, lang := range []string{"en", "zh-CN"} {
		w := menuBarWords(lang)
		if w["stateUnknown"] == "" || w["tryAgain"] == "" {
			t.Fatalf("%s: missing the S12 words", lang)
		}
		for _, observing := range []bool{false, true} {
			status, state := menuBarStatus(w, lang, DoingNow{}, false, false, false, false, observing)
			if status != "unknown" || state != w["stateUnknown"] {
				t.Errorf("%s observing=%v: %q %q, want unknown with the S12 line", lang, observing, status, state)
			}
		}
		if status, _ := menuBarStatus(w, lang, DoingNow{}, true, false, false, false, true); status != "paused" {
			t.Errorf("%s: Closed Eyes must win over an unreadable state, got %q", lang, status)
		}
	}
	if got := menuBarWords("en")["stateUnknown"]; got != "Can't tell what Isoform is seeing right now." {
		t.Fatalf("S12 line %q", got)
	}
	page, err := os.ReadFile(filepath.Join("menubar_window_assets", "popover.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"el('unk').hidden = !unknown;", "send('refreshState')", "hb.disabled = !!model.loading;"} {
		if !strings.Contains(string(page), want) {
			t.Fatalf("popover lost %q", want)
		}
	}
}
