package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

	// Bo, 2026-11-24: the portrait comes from Claude's own memory first, so
	// the plugin starts no MCP server and needs no Isoform install.
	for _, gone := range []string{".mcp.json", "bin"} {
		if _, err := os.Stat(filepath.Join(claudeCodePluginDir, gone)); !os.IsNotExist(err) {
			t.Errorf("plugin must not ship %s: %v", gone, err)
		}
	}
	for _, path := range []string{"README.md", filepath.Join("skills", "portrait", "SKILL.md")} {
		if _, err := os.Stat(filepath.Join(claudeCodePluginDir, path)); err != nil {
			t.Fatal(err)
		}
	}
}

func readPortraitSkill(t *testing.T) (string, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(claudeCodePluginDir, "skills", "portrait", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	skill := string(data)
	front, body, ok := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---\n")
	if !strings.HasPrefix(skill, "---\n") || !ok || !strings.Contains(front, "name: portrait\n") || !strings.Contains(front, "description: ") {
		t.Fatalf("SKILL.md frontmatter: %q", front)
	}
	return front, body
}

func TestClaudeCodePluginSkillShape(t *testing.T) {
	front, body := readPortraitSkill(t)
	// Reads go through read tools only; nothing pre-allows a write or a shell.
	if !strings.Contains(front+"\n", "\nallowed-tools: Read, Glob, Grep\n") {
		t.Errorf("skill must pre-allow only the read tools: %q", front)
	}
	ordered := []string{
		"goes to Anthropic",
		"Without asking",
		"~/.claude/CLAUDE.md",
		"~/.claude/projects/*/memory/*.md",
		"### Part A: Listing",
		"**Who I am**: exactly three sentences",
		"5 to 8 concrete offers",
		"**Strengths**",
		"**Limits**",
		"**When to hire the human instead**",
		"### Part B: Agent instructions",
		"**How I work**",
		"Leave out how the person runs their own development",
		"**Taste and voice**",
		"**Hard lines**",
		"## 4. Ask about the gaps",
		"## 6. Save only on an explicit keep",
		"~/.claude/agent-of-you/portrait.md",
	}
	at := 0
	for _, want := range ordered {
		i := strings.Index(body[at:], want)
		if i < 0 {
			t.Fatalf("skill lacks %q after position %d", want, at)
		}
		at += i + len(want)
	}
	for _, want := range []string{
		"Every line ends with its source in brackets", "[memory: <memory name>]", "[CLAUDE.md]", "(guess)", "[you said]",
		"**at most 6**", "rates and availability", "which client or company names may be public", "writing samples", "bring the person in", "claims in the draft to confirm", "work they would turn down",
		"secrets, keys, tokens", "email addresses, phone numbers, home folder paths", "private details about other people", "may be public",
		"chmod 600 ~/.claude/agent-of-you/portrait.md", "Kept: <YYYY-MM-DD>", "nothing goes to isoform.ai",
		`"Looks good", "fine" or silence is not a keep`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("skill lacks %q", want)
		}
	}
	// No MCP dependency, and no consent question before reading local notes.
	for _, gone := range []string{"portrait_draft", "portrait_keep", "mcp__", "MCP tool", "Ask exactly once", "Yes or no", "May I also read", "Settings > Other AI apps"} {
		if strings.Contains(body, gone) {
			t.Errorf("skill still contains %q", gone)
		}
	}
	if !strings.Contains(body, "Nothing here needs Isoform or any MCP server") {
		t.Error("skill must say it needs no Isoform or MCP server")
	}
	for _, path := range []string{"skills/portrait/SKILL.md", "README.md"} {
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

// The bridge's "not connected" error quotes the app's own labels for the page,
// the card and the button that issue an assistant's access. Renaming any of
// them must update yansu-agent/mcpread/server.go too.
func TestClaudeCodePluginConnectHintMatchesSettingsLabels(t *testing.T) {
	var locale struct {
		Pages map[string]struct {
			Label string `json:"label"`
		} `json:"pages"`
		AssistantMcp struct {
			Title      string `json:"title"`
			ClaudeCode string `json:"claudeCode"`
			Connect    string `json:"connect"`
		} `json:"assistantMcp"`
	}
	data, err := os.ReadFile("frontend/src/i18n/locales/en/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &locale); err != nil {
		t.Fatal(err)
	}
	bridge, err := os.ReadFile("../yansu-agent/mcpread/server.go")
	if err != nil {
		t.Fatal(err)
	}
	page := locale.Pages["use-yansu-anywhere"].Label
	for _, label := range []string{"Settings > " + page, "under " + locale.AssistantMcp.Title, "press " + locale.AssistantMcp.Connect, `name := "` + locale.AssistantMcp.ClaudeCode + `"`} {
		if page == "" || !strings.Contains(string(bridge), label) {
			t.Errorf("connect hint in the bridge lacks the app's label %q", label)
		}
	}
}
