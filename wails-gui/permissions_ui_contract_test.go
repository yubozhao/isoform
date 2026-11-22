package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// settingsSources returns the Settings shell (settings-page.tsx) and every
// page and helper under components/settings/, keyed by path. Tests are left
// out: they mention the forbidden names on purpose.
func settingsSources(t *testing.T) map[string]string {
	t.Helper()
	sources := map[string]string{}
	shell := filepath.Join("frontend", "src", "components", "settings-page.tsx")
	data, err := os.ReadFile(shell)
	if err != nil {
		t.Fatalf("failed to read settings-page source: %v", err)
	}
	sources[shell] = string(data)

	root := filepath.Join("frontend", "src", "components", "settings")
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || strings.Contains(entry.Name(), ".test.") {
			return nil
		}
		if !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sources[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to read settings sources: %v", err)
	}
	return sources
}

func TestSettingsPage_PermissionHintText(t *testing.T) {
	// Each source on What Isoform sees shows its own permission (Privacy's
	// full list went in settings batch D2). Its grants must still be wired to
	// the per-item request handler from usePermissions; the tip text wording
	// is left to UX, so only the wiring is asserted.
	path := filepath.Join("frontend", "src", "components", "settings", "pages", "observation-page.tsx")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read What Isoform sees source: %v", err)
	}
	text := string(source)
	for _, token := range []string{"usePermissions", "requestPermission("} {
		if !strings.Contains(text, token) {
			t.Fatalf("What Isoform sees should use %q for permission actions", token)
		}
	}
}

func TestSettingsPage_NoPermissionDialogPath(t *testing.T) {
	// The original contract forbade ALL dialog references in settings-page, but
	// Settings legitimately owns other dialogs now. Narrow the ban to the
	// specific permission-dialog wiring that must never come back.
	testcases := []string{
		"PermissionDialogTarget",
	}

	for path, text := range settingsSources(t) {
		for _, tc := range testcases {
			if strings.Contains(text, tc) {
				t.Fatalf("%s should not reference %q", path, tc)
			}
		}
	}
}

func TestSettingsPage_NoPlatformHardcoding(t *testing.T) {
	forbidden := []string{
		"GetMacOSPermissions",
		"MacOSPermissions",
		"RequestScreenCapturePermission",
		"RequestAccessibilityPermission",
		"OpenScreenRecordingSettings",
		"OpenAccessibilitySettings",
	}

	for path, text := range settingsSources(t) {
		for _, token := range forbidden {
			if strings.Contains(text, token) {
				t.Fatalf("%s should not reference legacy token %q — use platform-generic APIs", path, token)
			}
		}
	}
}

func TestOnboardingScreen_NoPlatformHardcoding(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("frontend", "src", "components", "onboarding-screen.tsx"))
	if err != nil {
		t.Fatalf("failed to read onboarding-screen source: %v", err)
	}

	text := string(source)
	forbidden := []string{
		"GetMacOSPermissions",
		"MacOSPermissions",
		"RequestScreenCapturePermission",
		"RequestAccessibilityPermission",
		"OpenScreenRecordingSettings",
		"OpenAccessibilitySettings",
		"macOS Permissions",
		"demo mode",
	}

	for _, token := range forbidden {
		if strings.Contains(text, token) {
			t.Fatalf("onboarding screen should not reference legacy token %q — use platform-generic APIs", token)
		}
	}
}

func TestOnboardingScreen_UsesDataDrivenPermissions(t *testing.T) {
	// The permissions flow was extracted from onboarding-screen.tsx into a
	// dedicated step component + hook. Assert the data-driven contract at its
	// new home: usePermissions drives rendering off the platform-generic
	// GetPlatformPermissions binding, and PermissionsStep consumes the hook.
	hookSrc, err := os.ReadFile(filepath.Join("frontend", "src", "hooks", "usePermissions.ts"))
	if err != nil {
		t.Fatalf("failed to read usePermissions hook: %v", err)
	}
	hookText := string(hookSrc)
	hookRequired := []string{
		"GetPlatformPermissions",
		"PlatformPermissions",
		"RequestPermission",
	}
	for _, token := range hookRequired {
		if !strings.Contains(hookText, token) {
			t.Fatalf("usePermissions hook should contain %q for data-driven permission rendering", token)
		}
	}

	// Setup v2 asks through the backend sequencer (setup_permissions.go) and
	// draws its rows from the answers it reports.
	stepSrc, err := os.ReadFile(filepath.Join("frontend", "src", "components", "onboarding", "v2", "scenes", "SeesScene.tsx"))
	if err != nil {
		t.Fatalf("failed to read SeesScene source: %v", err)
	}
	stepText := string(stepSrc)
	for _, token := range []string{"GetSetupPermissions", "RequestAllPermissions", "setup:permissions"} {
		if !strings.Contains(stepText, token) {
			t.Fatalf("SeesScene should contain %q to follow the permission sequencer", token)
		}
	}
}
