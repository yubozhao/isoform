package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDirHonorsAgentHome(t *testing.T) {
	t.Setenv(AgentHomeEnv, "")
	t.Setenv("YANSU_APP_FLAVOR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	dir, err := ConfigDir()
	if err != nil || dir != filepath.Join(home, ConfigDirName) {
		t.Fatalf("default ConfigDir = %q, %v", dir, err)
	}
	t.Setenv(AgentHomeEnv, " /tmp/isoform-home ")
	dir, err = ConfigDir()
	if err != nil || dir != "/tmp/isoform-home" {
		t.Fatalf("overridden ConfigDir = %q, %v", dir, err)
	}
}

func TestConfigDirRejectsRelativeHomes(t *testing.T) {
	for _, value := range []string{"data", "~/.isoform-agent", "../private"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(AgentHomeEnv, value)
			if root, err := ConfigDir(); err == nil || root != "" {
				t.Fatalf("ConfigDir = %q, %v; want rejected home", root, err)
			}
		})
	}
}

func TestConfigDirStandaloneFlavor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(AgentHomeEnv, "")
	for _, flavor := range []string{"", "yansu", "isoform"} {
		t.Run(flavor, func(t *testing.T) {
			t.Setenv("YANSU_APP_FLAVOR", flavor)
			folder := ConfigDirName
			if flavor == "isoform" {
				folder = ".isoform-agent"
			}
			root, err := ConfigDir()
			if err != nil || root != filepath.Join(home, folder) {
				t.Fatalf("ConfigDir = %q, %v", root, err)
			}
		})
	}
}

func TestConfigDirBundledExecutable(t *testing.T) {
	if expected := os.Getenv("ISOFORM_TEST_BUNDLED_HOME"); expected != "" {
		root, err := ConfigDir()
		if os.Getenv("ISOFORM_TEST_REJECT_LEGACY") == "1" {
			if root != "" || err == nil {
				t.Fatalf("bundled CLI accepted published home %q, %v", root, err)
			}
			return
		}
		if err != nil || !samePath(root, expected) {
			t.Fatalf("bundled ConfigDir = %q, %v; want %q", root, err, expected)
		}
		if err := SaveToken("isolated-token"); err != nil {
			t.Fatal(err)
		}
		return
	}
	home := t.TempDir()
	root := filepath.Join(home, ".isoform-agent")
	bin := filepath.Join(root, "bin", "yansu")
	if err := os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, contents, 0700); err != nil {
		t.Fatal(err)
	}
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "HOME", "USERPROFILE", AgentHomeEnv, "YANSU_APP_FLAVOR", "ISOFORM_TEST_BUNDLED_HOME":
			continue
		default:
			env = append(env, entry)
		}
	}
	command := exec.Command(bin, "-test.run=^TestConfigDirBundledExecutable$")
	command.Env = append(env, "HOME="+home, "USERPROFILE="+home, "YANSU_APP_FLAVOR=yansu", "ISOFORM_TEST_BUNDLED_HOME="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bundled CLI: %v\n%s", err, output)
	}
	command = exec.Command(bin, "-test.run=^TestConfigDirBundledExecutable$")
	command.Env = append(env, "HOME="+home, "USERPROFILE="+home, "ISOFORM_TEST_BUNDLED_HOME="+root, "ISOFORM_TEST_REJECT_LEGACY=1", AgentHomeEnv+"="+filepath.Join(home, ConfigDirName))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bundled CLI legacy override: %v\n%s", err, output)
	}
	if token, err := os.ReadFile(filepath.Join(root, TokenFileName)); err != nil || string(token) != "isolated-token" {
		t.Fatalf("bundled token = %q, %v", token, err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".isoform-agent" {
		t.Fatalf("fake home contains unexpected data: %v, %v", entries, err)
	}
}

func TestConfigDirRejectsIsoformPublishedAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("YANSU_APP_FLAVOR", "isoform")
	published := filepath.Join(home, ConfigDirName)
	if err := os.MkdirAll(published, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(published, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, override := range []string{published, filepath.Join(published, "new", "branch"), alias, filepath.Join(alias, "new", "branch")} {
		t.Setenv(AgentHomeEnv, override)
		if root, err := ConfigDir(); err == nil || root != "" {
			t.Fatalf("Isoform accepted published path %q: %q, %v", override, root, err)
		}
	}
	t.Setenv(AgentHomeEnv, alias)
	t.Setenv("YANSU_APP_FLAVOR", "yansu")
	if root, err := ConfigDir(); err != nil || root != alias {
		t.Fatalf("published CLI override changed: %q, %v", root, err)
	}
}

func TestBundledAgentHomeFollowsSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".isoform-agent")
	bin := filepath.Join(root, "bin", "yansu")
	if err := os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, nil, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "yansu")
	if err := os.Symlink(bin, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := bundledAgentHome(link); !samePath(got, root) {
		t.Fatalf("bundledAgentHome = %q, want %q", got, root)
	}
	if got := bundledAgentHome(filepath.Join(t.TempDir(), "bin", "yansu")); got != "" {
		t.Fatalf("standalone binary inferred root %q", got)
	}
}

// samePath compares two paths after resolving symlinks, so a temp dir under
// /var on macOS matches its /private/var form returned by bundledAgentHome.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func TestRetiredBackendHasNoDefaultURL(t *testing.T) {
	t.Setenv(AgentHomeEnv, t.TempDir())
	t.Setenv(EnvBaseURL, "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebBaseURL != "" || cfg.APIBaseURL != "" || cfg.BaseURL != "" {
		t.Fatal("empty configuration resolved a backend endpoint")
	}
}
