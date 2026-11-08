package main

import (
	"os"
	"testing"
)

// testRealHomeDir is the home directory the test run started with, captured
// before TestMain moved HOME aside. Only the home isolation guard and the
// explicitly sanctioned env-gated integration tests may read it;
// TestOnlySanctionedTestsSeeTheRealHome enforces that list.
var testRealHomeDir string

// testRealClaudeConfigDir and testRealCodexHome are the values of
// CLAUDE_CONFIG_DIR and CODEX_HOME before TestMain cleared them, restored by
// TestMagpieSidecarIntegration so magpie finds the user's sign-ins.
var testRealClaudeConfigDir, testRealCodexHome string

// testHelperChildEnvVars mark a process as this test binary re-executed as a
// helper child. A helper must keep the exact environment its parent test
// chose, so TestMain leaves it untouched.
var testHelperChildEnvVars = []string{
	stderrHelperEnv,           // TestStderrLogHelperProcess
	"ISOFORM_MCP_STDIO_TEST",  // TestMCPReadStdioProcess
	"ISOFORM_SKILL_LOCK_TEST", // TestIsoformSkillSharedLockHelper
	"UPDATE_TEST_CHILD",       // TestUpdateChildProcess
	"YANSU_GOAL_TEST_FIXTURE", // TestGoalExternalCLIFixture and its tool shim
}

func TestMain(m *testing.M) {
	// No test, helper children included, starts the user's real Claude Code
	// or Codex (testmain_cli_test.go).
	installCLIProbeFakes()
	// Headless permission snapshots must not enter native Automation probes.
	// Permission-specific tests override this hook with their own fixtures.
	todayBriefPermissionStatus = func(*App) PlatformPermissions { return PlatformPermissions{} }
	if testHelperChildProcess() {
		installSharedTextPermissionFake()
		os.Exit(m.Run())
	}
	cleanup := setupIsolatedTestHome()
	installSharedTextPermissionFake()
	code := m.Run()
	cleanup()
	removeSharedFakeMagpie()
	os.Exit(code)
}

// testHelperChildProcess reports whether this process is the test binary
// re-executed as a helper child.
func testHelperChildProcess() bool {
	for _, key := range testHelperChildEnvVars {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

// setupIsolatedTestHome runs every test in this package with an isolated home
// folder: HOME and USERPROFILE point at a fresh temporary directory and the
// data-folder environment variables the code reads are cleared, so any code
// path that resolves ~/.yansu-agent, ~/.isoform-agent or another
// home-relative folder lands inside the temporary directory instead of the
// user's real data. Tests that need a specific folder set it themselves, and
// integration tests that need a real fixture on purpose read their own
// explicit variable (YANSU_MAGPIE_BIN, YANSU_INTERESTS_DATA, ...). The
// returned cleanup removes the temporary home.
func setupIsolatedTestHome() func() {
	testRealHomeDir, _ = os.UserHomeDir()
	testRealClaudeConfigDir = os.Getenv("CLAUDE_CONFIG_DIR")
	testRealCodexHome = os.Getenv("CODEX_HOME")
	home := shortTestHome("wgt-home-")
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("USERPROFILE", home)
	// Clear ambient data-folder overrides so every resolution falls back
	// under the temporary home. Unset rather than repoint: flavor tests
	// assert the flavor's own folder name under HOME, which a fixed
	// YANSU_AGENT_HOME would shadow.
	for _, key := range []string{
		agentHomeEnv,
		"YANSU_MOBILE_STATE_DIR",
		topicsStateDirEnvVar,
		"XDG_CONFIG_HOME",
		"XDG_CACHE_HOME",
		"XDG_DATA_HOME",
		"XDG_STATE_HOME",
		"CLAUDE_CONFIG_DIR",
		"CODEX_HOME",
	} {
		_ = os.Unsetenv(key)
	}
	return func() { _ = os.RemoveAll(home) }
}

// shortTestHome creates the isolated home under /tmp so Unix socket paths
// built inside it (the MCP read socket, the single-instance socket) stay
// under the macOS length limit. Falls back to the default temp root where
// /tmp does not exist (Windows, which uses named pipes instead).
func shortTestHome(prefix string) string {
	home, err := os.MkdirTemp("/tmp", prefix)
	if err != nil {
		home, err = os.MkdirTemp("", prefix)
	}
	if err != nil {
		panic(err)
	}
	return home
}

// Initialize only after HOME is isolated (helpers inherit their parent's home).
// Unit callers of the shared permission inventory must never enter native
// Messages Automation or gain permission to send a real message. Text-channel
// tests construct their own channel and override its hooks explicitly.
func installSharedTextPermissionFake() {
	sharedTextChannel().automation = func(bool) textAutomationState { return textAutomationNotAllowed }
}
