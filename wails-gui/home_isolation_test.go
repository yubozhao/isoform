package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/apphome"
	"wails-gui/internal/singleinstance"
)

// TestHomeIsolationGuard proves the package TestMain's isolation end to end:
// a representative App action that writes a memory file, and a
// single-instance lock, both land under the temporary home, and neither
// appears under the real home directory the run started with. The real-home
// assertions stat only the exact fixture paths this test would have created;
// they never enumerate the user's folders.
func TestHomeIsolationGuard(t *testing.T) {
	if testRealHomeDir == "" {
		t.Skip("the original home directory was not captured")
	}
	tempHome := os.Getenv("HOME")
	if tempHome == "" || tempHome == testRealHomeDir {
		t.Fatalf("TestMain did not isolate HOME: %q", tempHome)
	}

	// Dismissing a Today suggestion syncs the initiative's learning memory
	// file through the flavor data folder. Clear the per-test override the
	// rig installs so the write resolves through the isolated HOME.
	app, rig, activity := suggestionTestApp(t)
	t.Setenv(agentHomeEnv, "")
	ctx := context.Background()
	day := func(offset int) time.Time { return rig.clock.AddDate(0, 0, -offset) }
	saveProposedAutomation(t, activity, "init-home-guard", "home guard cleanup", 0.8, "manual", []time.Time{day(5), day(3), day(1)})
	app.checkSuggestionEngagement(ctx, rig.clock)
	if _, err := app.ResolveTodaySuggestion(TodaySuggestionDecision{ID: "init-home-guard", Decision: "dismiss", Reason: "guard"}); err != nil {
		t.Fatal(err)
	}
	memoryRoot, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	memoryFile := filepath.Join(memoryRoot, "shadow-learning-init-home-guard.md")
	assertUnderTempHome(t, tempHome, memoryFile)

	inst, err := singleinstance.Acquire("yansu-desktop-home-guard")
	if err != nil {
		t.Fatal(err)
	}
	lockRoot, err := apphome.Dir(singleinstance.BaseDirName)
	if err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(lockRoot, ".yansu-desktop-home-guard.lock")
	assertUnderTempHome(t, tempHome, lockFile)
	if err := inst.Release(); err != nil {
		t.Fatal(err)
	}

	for _, folder := range []string{".yansu-agent", ".isoform-agent"} {
		for _, rel := range []string{
			filepath.Join("memory", "shadow-learning-init-home-guard.md"),
			".yansu-desktop-home-guard.lock",
			".yansu-desktop-home-guard.sock",
		} {
			path := filepath.Join(testRealHomeDir, folder, rel)
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("test artifact escaped into the real home folder: %q (stat: %v)", path, err)
			}
		}
	}
}

// assertUnderTempHome requires path to exist and to live inside the isolated
// temporary home.
func assertUnderTempHome(t *testing.T, tempHome, path string) {
	t.Helper()
	if !strings.HasPrefix(path, tempHome+string(os.PathSeparator)) {
		t.Fatalf("%q resolved outside the isolated home %q", path, tempHome)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %q to exist under the isolated home: %v", path, err)
	}
}

// TestOnlySanctionedTestsSeeTheRealHome keeps the testReal* variables — the
// only way back to the user's real folders after TestMain moved HOME aside —
// out of every test file but the sanctioned ones: main_test.go defines them,
// the guard verifies with them, and the env-gated integration tests use the
// explicitly requested real-home resources. A new reader must fail here
// first, so every exception is a deliberate, reviewed choice.
func TestOnlySanctionedTestsSeeTheRealHome(t *testing.T) {
	sanctioned := map[string]bool{
		"main_test.go":                           true,
		"about_you_portrait_integration_test.go": true,
		"assistant_history_integration_test.go":  true,
		"home_isolation_test.go":                 true,
		"interests_integration_test.go":          true,
		"magpie_integration_test.go":             true,
		"telemetry_posthog_integration_test.go":  true,
	}
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if sanctioned[file] {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("testReal")) {
			t.Fatalf("%s reaches the user's real home; only the guard and the env-gated magpie, interests, portrait and PostHog integration tests may", file)
		}
	}
}

// TestGoCommandsKeepTheRealCaches guards the temporary home against the go
// module cache: a go command a test runs must resolve its module and build
// caches outside the temporary home, and a home holding a read-only module
// cache must still be removed.
func TestGoCommandsKeepTheRealCaches(t *testing.T) {
	tempHome := os.Getenv("HOME")
	if _, err := exec.LookPath("go"); err == nil {
		out, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE").Output()
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range strings.Fields(string(out)) {
			if strings.HasPrefix(dir, tempHome+string(os.PathSeparator)) {
				t.Fatalf("a go command resolves %q inside the temporary home %q", dir, tempHome)
			}
		}
	}

	home := shortTestHome("wgt-rm-")
	mod := filepath.Join(home, "go", "pkg", "mod", "example.com", "m@v1.0.0")
	if err := os.MkdirAll(mod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module example.com/m\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	for dir := mod; dir != home; dir = filepath.Dir(dir) {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	if got := goCachesUnder(home); len(got) != 1 {
		t.Fatalf("goCachesUnder(%q) = %v, want the module cache", home, got)
	}
	removeTestHome(home)
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("a home with a read-only module cache was left behind: %v", err)
	}
}
