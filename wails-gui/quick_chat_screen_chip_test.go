package main

import (
	"testing"

	"wails-gui/internal/platform/activitymon"
)

// Quick chat offers what's on screen as a chip, never under Closed Eyes or
// for a sensitive app (computer-chat v1.0.0 rule 8).
func TestSpotlightScreenChipFollowsWhatIsoformMaySee(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	mail := activitymon.AppInfo{BundleID: "com.apple.mail", AppName: "Mail", WindowTitle: "  Priya   Shah "}

	bundle, name, title := app.spotlightScreenChipFrom("com.apple.mail", "Mail", mail)
	if bundle != "com.apple.mail" || name != "Mail" || title != "Priya Shah" {
		t.Fatalf("chip = %q %q %q", bundle, name, title)
	}
	// Over Isoform itself there is no chip.
	if bundle, _, _ := app.spotlightScreenChipFrom("com.wails.Isoform", "Isoform", activitymon.AppInfo{BundleID: "com.wails.Isoform", AppName: "Isoform"}); bundle != "" {
		t.Fatal("Quick chat over Isoform offered a screen chip")
	}
	// Another app in front: the chip names the app without a title.
	if _, _, title := app.spotlightScreenChipFrom("com.apple.mail", "Mail", activitymon.AppInfo{BundleID: "com.apple.Safari", AppName: "Safari", WindowTitle: "News"}); title != "" {
		t.Fatalf("title from another app = %q", title)
	}
	if bundle, _, _ := app.spotlightScreenChipFrom("com.1password.desktop", "1Password", activitymon.AppInfo{BundleID: "com.1password.desktop", AppName: "1Password"}); bundle != "" {
		t.Fatal("a sensitive app got a screen chip")
	}
	if bundle, _, _ := app.spotlightScreenChipFrom("com.apple.Safari", "Safari", activitymon.AppInfo{BundleID: "com.apple.Safari", AppName: "Safari", WindowTitle: "Private Browsing"}); bundle != "" {
		t.Fatal("a private browser window got a screen chip")
	}
	// With "Watch my screen" off there is no chip, no capture and no read.
	if err := app.store.SetUIState(t.Context(), permissionUIStateKey("screen_recording"), "false"); err != nil {
		t.Fatal(err)
	}
	if bundle, name, title := app.spotlightScreenChipFrom("com.apple.mail", "Mail", mail); bundle != "" || name != "" || title != "" {
		t.Fatalf("Watch my screen off left a chip: %q %q %q", bundle, name, title)
	}
	if snap := app.captureFocusedAppContext("com.apple.mail", quickChatSubmitCaptureBudget); snap != nil {
		t.Fatal("Watch my screen off still captured the screen")
	}
	if snap := app.focusedAppContextForModelRead(&focusedAppSnapshot{BundleID: "com.apple.mail", AppName: "Mail", General: "x"}); snap != nil {
		t.Fatal("Watch my screen off still let the model read the screen")
	}
	if err := app.store.SetUIState(t.Context(), permissionUIStateKey("screen_recording"), "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.SetClosedEyesModeEnabled(true); err != nil {
		t.Fatal(err)
	}
	if bundle, name, title := app.spotlightScreenChipFrom("com.apple.mail", "Mail", mail); bundle != "" || name != "" || title != "" {
		t.Fatalf("Closed Eyes left a chip: %q %q %q", bundle, name, title)
	}
}
