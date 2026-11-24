package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// The Claude Code plugin (integrations/claude-code-plugin) ships no Go code;
// these checks keep its manifests, launcher and skill in step with the tools
// in mcp_portrait.go.

const claudeCodePluginDir = "../integrations/claude-code-plugin"

func readPluginJSON(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

type pluginAuthor struct {
	Name string `json:"name"`
}

func TestClaudeCodePluginManifests(t *testing.T) {
	var marketplace struct {
		Name     string       `json:"name"`
		Owner    pluginAuthor `json:"owner"`
		Metadata struct {
			Description string `json:"description"`
			Version     string `json:"version"`
		} `json:"metadata"`
		Plugins []struct {
			Name        string       `json:"name"`
			Description string       `json:"description"`
			Version     string       `json:"version"`
			Author      pluginAuthor `json:"author"`
			Source      string       `json:"source"`
		} `json:"plugins"`
	}
	readPluginJSON(t, "../.claude-plugin/marketplace.json", &marketplace)
	if marketplace.Name != "isoform" || marketplace.Owner.Name == "" || len(marketplace.Plugins) != 1 {
		t.Fatalf("marketplace must be isoform with one plugin: %+v", marketplace)
	}
	entry := marketplace.Plugins[0]
	// "/plugin install isoform@isoform" needs these two names.
	if entry.Name != "isoform" || entry.Source != "./integrations/claude-code-plugin" {
		t.Fatalf("marketplace entry: %+v", entry)
	}
	var plugin struct {
		Name        string       `json:"name"`
		Version     string       `json:"version"`
		Description string       `json:"description"`
		Author      pluginAuthor `json:"author"`
		Homepage    string       `json:"homepage"`
		Keywords    []string     `json:"keywords"`
	}
	readPluginJSON(t, filepath.Join(claudeCodePluginDir, ".claude-plugin", "plugin.json"), &plugin)
	if plugin.Name != entry.Name || plugin.Version != entry.Version || plugin.Description != entry.Description || plugin.Version == "" {
		t.Fatalf("plugin.json and the marketplace entry disagree: %+v vs %+v", plugin, entry)
	}

	var servers struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	readPluginJSON(t, filepath.Join(claudeCodePluginDir, ".mcp.json"), &servers)
	server, ok := servers.MCPServers["isoform"]
	if len(servers.MCPServers) != 1 || !ok || server.Command != "${CLAUDE_PLUGIN_ROOT}/bin/isoform-mcp" || len(server.Args) != 0 {
		t.Fatalf(".mcp.json must start only the bundled launcher: %+v", servers)
	}
	info, err := os.Stat(filepath.Join(claudeCodePluginDir, "bin", "isoform-mcp"))
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("launcher must exist and be executable: %v %v", info, err)
	}
	for _, path := range []string{"README.md", filepath.Join("skills", "portrait", "SKILL.md")} {
		if _, err := os.Stat(filepath.Join(claudeCodePluginDir, path)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeCodePluginSkillMatchesTools(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(claudeCodePluginDir, "skills", "portrait", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	skill := string(data)
	front, body, ok := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---\n")
	if !strings.HasPrefix(skill, "---\n") || !ok || !strings.Contains(front, "name: portrait\n") || !strings.Contains(front, "description: ") {
		t.Fatalf("SKILL.md frontmatter: %q", front)
	}
	for _, want := range []string{"portrait_draft", "portrait_keep", "goes to Anthropic", "Ask exactly once", "explicit keep"} {
		if !strings.Contains(body, want) {
			t.Errorf("skill lacks %q", want)
		}
	}
	for _, section := range portraitSections {
		if !strings.Contains(body, "`"+section+"`") {
			t.Errorf("skill does not name section %s", section)
		}
	}
	var sources []string
	for source := range portraitKeepSources {
		sources = append(sources, source)
		if !strings.Contains(body, "`"+source+"`") {
			t.Errorf("skill does not name source %s", source)
		}
	}
	sort.Strings(sources)
	// The bridge's keep schema lists the same sources (yansu-agent/mcpread).
	bridge, err := os.ReadFile("../yansu-agent/mcpread/server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		if !strings.Contains(string(bridge), `"`+source+`"`) {
			t.Errorf("bridge schema lacks source %s", source)
		}
	}
	for _, path := range []string{"skills/portrait/SKILL.md", "README.md", "bin/isoform-mcp"} {
		text, err := os.ReadFile(filepath.Join(claudeCodePluginDir, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range string(text) {
			if r > unicode.MaxLatin1 && (unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r)) {
				t.Errorf("%s contains a decorative symbol %U", path, r)
			}
		}
		if strings.Contains(string(text), "/Users/") {
			t.Errorf("%s contains a home path", path)
		}
	}
}

func runPluginLauncher(t *testing.T, env ...string) (string, string, error) {
	t.Helper()
	launcher, err := filepath.Abs(filepath.Join(claudeCodePluginDir, "bin", "isoform-mcp"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(launcher)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestClaudeCodePluginLauncher(t *testing.T) {
	home := t.TempDir()
	notInstalled := "Isoform is not installed on this computer. Get it at https://isoform.ai, open it once, then try again.\n"
	// No data folder, then a data folder without the CLI: one plain line on
	// stderr, nothing on stdout (which belongs to MCP), exit 1.
	for _, env := range [][]string{{"HOME=" + home}, {"HOME=" + home, "ISOFORM_DATA_DIR=" + home}} {
		stdout, stderr, err := runPluginLauncher(t, env...)
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 1 || stdout != "" || stderr != notInstalled {
			t.Fatalf("env %v: err=%v stdout=%q stderr=%q", env, err, stdout, stderr)
		}
	}
	// With Isoform's data folder, it runs the CLI the app installed there
	// against that folder, so it reaches the running app's socket.
	dataDir := filepath.Join(home, ".isoform-agent")
	if err := os.MkdirAll(filepath.Join(dataDir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\necho \"$0 $*\"\n"
	if err := os.WriteFile(filepath.Join(dataDir, "bin", "yansu"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runPluginLauncher(t, "HOME="+home)
	want := filepath.Join(dataDir, "bin", "yansu") + " mcp serve --client claude-code --data-dir " + dataDir + "\n"
	if err != nil || stdout != want || stderr != "" {
		t.Fatalf("launcher: err=%v stdout=%q stderr=%q want %q", err, stdout, stderr, want)
	}
	// ISOFORM_YANSU wins, for tests and unusual installs.
	override := filepath.Join(home, "other-yansu")
	if err := os.WriteFile(override, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runPluginLauncher(t, "HOME="+home, "ISOFORM_YANSU="+override)
	if err != nil || !strings.HasPrefix(stdout, override+" mcp serve") {
		t.Fatalf("override: err=%v stdout=%q", err, stdout)
	}
}

// Opt-in: the real Claude Code validator, like the other real-CLI checks.
func TestClaudeCodePluginValidatesWithRealCLI(t *testing.T) {
	if os.Getenv("YANSU_CLAUDE_INTEGRATION") != "1" {
		t.Skip("set YANSU_CLAUDE_INTEGRATION=1 to run the real claude plugin validate")
	}
	cli, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal("claude CLI not found")
	}
	for _, path := range []string{claudeCodePluginDir, ".."} {
		output, err := exec.Command(cli, "plugin", "validate", "--strict", path).CombinedOutput()
		if err != nil {
			t.Fatalf("claude plugin validate %s: %v\n%s", path, err, output)
		}
	}
}
