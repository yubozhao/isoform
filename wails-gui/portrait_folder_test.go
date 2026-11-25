package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
)

// The portrait folder (integrations/portrait-spec/PORTRAIT-FORMAT.md) and its
// scripts/portrait tool. Every test works in a temporary HOME.

const portraitFixtureMD = `---
name: bo
description: "Bo's agentic self: how to decide, work and communicate the way he would. Load it when acting for Bo or deciding as he would."
metadata:
  format: portrait/1
  version: "2"
  made: "2026-11-20"
  updated: "2026-11-24"
  agents: "claude, codex"
---

# Bo's agentic self

## Who you are
You are Bo's agentic self: you act for him, the way he would.
You are an AI: never tell anyone you are Bo, and never invent facts about his life.

## How you decide
- In this project, merge a reviewed change without asking; everywhere else, wait for his yes. For example: a fix is ready; Bo would merge it here, because waiting costs more. <!-- claude: memory: merge-fast; codex: history -->
- For a new kind of decision, read examples.md.

## When to act and when to ask
| Action | What to do |
|---|---|
| Fix a failing test on a ready pull request | Act alone <!-- claude: history --> |
| Send a message in his name | Ask first <!-- claude: memory: messages --> |

## Never
- Never handle keys or money. <!-- claude: CLAUDE.md -->

## What they care about
- Ship fast behind a rollback switch. (guess) <!-- codex: AGENTS.md -->

## How they do things
- Fix a bug: replay it; find the root cause; add a guard test. <!-- claude: memory: root-cause -->
- For the full steps, read references/methods.md.

## What they know
- Go desktop apps, in depth. <!-- claude: CLAUDE.md -->

## How they communicate
- Plain words, short lines. <!-- claude: memory: explain-plainly -->
- For samples of his writing, read examples.md.

## People and places they work in
- Reviewers: give them the facts first. <!-- claude: memory: reviews -->

## What you don't know
- His rates. <!-- claude: CLAUDE.md -->
- Outside this portrait, say so and ask.
`

const portraitFixtureScenarios = `# Scenarios

## 1. Ready pull request, one failing test
- Situation: a pull request is ready but one test fails.
- Agent would: fix the test and merge without asking.
- Verdict: not yet
- Reason:
- Date: 2026-11-24

## 2. A message to a reviewer
- Situation: a reviewer asks for a status update.
- Agent would: draft it and ask before sending.
- Verdict: not yet
- Reason:
- Date: 2026-11-24
`

// writePortraitFolder writes a valid portrait folder named name under parent.
func writePortraitFolder(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	files := map[string]string{
		"PORTRAIT.md":           strings.Replace(portraitFixtureMD, "name: bo", "name: "+name, 1),
		"scenarios.md":          portraitFixtureScenarios,
		"examples.md":           "# Examples\n\n- Merged a fix behind a switch at night, because waiting cost more. <!-- claude: history -->\n",
		"corrections.md":        "# Corrections\n\n- Situation: a test failed. Did: asked first. Wanted: fix and merge. <!-- claude: history -->\n",
		"references/methods.md": "# Methods\n\n- Fix a bug: replay; root cause; guard test.\n",
	}
	for path, text := range files {
		writePortraitFile(t, filepath.Join(dir, path), text)
	}
	return dir
}

// runPortrait runs the package's portrait script on dir with a stub HOME.
func runPortrait(t *testing.T, home, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(claudeCodePluginDir, "scripts", "portrait"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, args...)
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + filepath.Join(home, "..", "bin") + ":/usr/bin:/bin", "TMPDIR=" + home, "PORTRAIT_DIR=" + dir}, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestPortraitFolderPassesCheck(t *testing.T) {
	home := t.TempDir()
	dir := writePortraitFolder(t, home, "bo")
	if out, code := runPortrait(t, home, dir, nil, "check"); code != 0 || out != "ok: bo (portrait/1, version 2)\n" {
		t.Fatalf("check: %d %q", code, out)
	}
	broken := map[string]func(string) string{
		"problem: name bo must match the folder":            func(s string) string { return s },
		"problem: missing or out-of-order section ## Never": func(s string) string { return strings.Replace(s, "## Never\n", "## Hard lines\n", 1) },
		"problem: metadata.format must be portrait/1":       func(s string) string { return strings.Replace(s, "portrait/1", "portrait/0", 1) },
		"is marked (conflict): state which rule applies where": func(s string) string {
			return strings.Replace(s, "- Never handle keys", "- (conflict) Never handle keys", 1)
		},
		"problem: PORTRAIT.md line 49 has an email address": func(s string) string {
			return strings.Replace(s, "- His rates.", "- His rates; ask bo@example.com.", 1)
		},
		"problem: quote a description that contains \": \"": func(s string) string {
			return strings.Replace(s, `description: "Bo's agentic self: how`, `description: Bo's agentic self: how`, 1)
		},
		"problem: metadata.updated must be YYYY-MM-DD":                    func(s string) string { return strings.Replace(s, `updated: "2026-11-24"`, `updated: "yesterday"`, 1) },
		"problem: unexpected section ## Notes":                            func(s string) string { return s + "\n## Notes\n- extra\n" },
		"problem: missing or out-of-order section ## What you don't know": func(s string) string { return strings.Replace(s, "## What you don't know", "## Gaps", 1) },
	}
	for want, edit := range broken {
		name := "bo"
		if strings.Contains(want, "must match the folder") {
			name = "other"
		}
		d := writePortraitFolder(t, t.TempDir(), name)
		text, _ := os.ReadFile(filepath.Join(d, "PORTRAIT.md"))
		fixed := strings.Replace(string(text), "name: "+name, "name: bo", 1)
		writePortraitFile(t, filepath.Join(d, "PORTRAIT.md"), edit(fixed))
		if out, code := runPortrait(t, home, d, nil, "check"); code == 0 || !strings.Contains(out, want) {
			t.Errorf("check should report %q, got %d %q", want, code, out)
		}
	}
	// Scenarios need every field and a known verdict.
	d := writePortraitFolder(t, t.TempDir(), "bo")
	writePortraitFile(t, filepath.Join(d, "scenarios.md"), strings.Replace(portraitFixtureScenarios, "- Verdict: not yet\n- Reason:\n- Date: 2026-11-24\n\n## 2.", "- Verdict: maybe\n- Reason:\n\n## 2.", 1))
	out, code := runPortrait(t, home, d, nil, "check")
	if code == 0 || !strings.Contains(out, "scenario 1 verdict must be right, wrong or not yet") || !strings.Contains(out, "scenario 1 has no Date") {
		t.Errorf("scenario check: %d %q", code, out)
	}
}

// PORTRAIT.md's frontmatter loads with a SKILL.md-style loader: YAML with a
// name that matches the folder and a description, and the portrait fields
// under metadata as strings.
func TestPortraitFrontmatterLoadsLikeASkill(t *testing.T) {
	dir := writePortraitFolder(t, t.TempDir(), "bo")
	text, err := os.ReadFile(filepath.Join(dir, "PORTRAIT.md"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(text), "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		t.Fatal("no frontmatter")
	}
	var front struct {
		Name        string            `yaml:"name"`
		Description string            `yaml:"description"`
		Metadata    map[string]string `yaml:"metadata"`
	}
	if err := yaml.UnmarshalStrict([]byte(parts[1]), &front); err != nil {
		t.Fatalf("frontmatter does not load as a skill's: %v", err)
	}
	if front.Name != filepath.Base(dir) || !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(front.Name) {
		t.Errorf("name %q", front.Name)
	}
	if front.Description == "" || len(front.Description) > 1024 {
		t.Errorf("description %q", front.Description)
	}
	for _, key := range []string{"format", "version", "made", "updated", "agents"} {
		if front.Metadata[key] == "" {
			t.Errorf("metadata lacks %s", key)
		}
	}
	if front.Metadata["format"] != "portrait/1" {
		t.Errorf("format %q", front.Metadata["format"])
	}
	// The lead's template uses the same shape.
	_, body := readPortraitSkill(t)
	for _, want := range []string{"\nname: <folder name>\ndescription: \"", "\nmetadata:\n  format: portrait/1\n  version: \"", "\n  made: \"", "\n  updated: \"", "\n  agents: \""} {
		if !strings.Contains(body, want) {
			t.Errorf("the lead's PORTRAIT.md template lacks %q", want)
		}
	}
}

func TestPortraitVerdictUpdatesScenario(t *testing.T) {
	home := t.TempDir()
	dir := writePortraitFolder(t, home, "bo")
	if out, code := runPortrait(t, home, dir, nil, "verdict", "1", "wrong", "ask before merging on Fridays"); code != 0 || out != "scenario 1: wrong\n" {
		t.Fatalf("verdict: %d %q", code, out)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "scenarios.md"))
	text := string(data)
	if !regexp.MustCompile(`## 1\. Ready pull request, one failing test\n- Situation: .*\n- Agent would: .*\n- Verdict: wrong\n- Reason: ask before merging on Fridays\n- Date: \d{4}-\d{2}-\d{2}\n`).MatchString(text) {
		t.Fatalf("scenario 1 not updated:\n%s", text)
	}
	if !strings.Contains(text, "## 2. A message to a reviewer\n- Situation: a reviewer asks for a status update.\n- Agent would: draft it and ask before sending.\n- Verdict: not yet\n") {
		t.Fatalf("scenario 2 must stay not yet:\n%s", text)
	}
	// The verdict copied the earlier version first.
	earlier, _ := filepath.Glob(filepath.Join(dir, "versions", "*", "scenarios.md"))
	if len(earlier) != 1 {
		t.Fatalf("versions: %v", earlier)
	}
	if old, _ := os.ReadFile(earlier[0]); !strings.Contains(string(old), "- Verdict: not yet\n- Reason:\n- Date: 2026-11-24\n\n## 2.") {
		t.Fatalf("snapshot is not the earlier version:\n%s", old)
	}
	if out, code := runPortrait(t, home, dir, nil, "verdict", "2", "right"); code != 0 || out != "scenario 2: right\n" {
		t.Fatalf("right verdict: %d %q", code, out)
	}
	if out, code := runPortrait(t, home, dir, nil, "check"); code != 0 {
		t.Fatalf("check after verdicts: %q", out)
	}
	for _, args := range [][]string{{"verdict", "1", "wrong"}, {"verdict", "9", "right"}, {"verdict", "1", "maybe"}} {
		if _, code := runPortrait(t, home, dir, nil, args...); code == 0 {
			t.Errorf("%v should fail", args)
		}
	}
}

// Save installs a built folder at ~/.agents/portraits/<name>/, keeps every
// earlier version under versions/, and migrates a 0.4 single-file portrait.
func TestPortraitSaveVersionsAndMigrates(t *testing.T) {
	s := newPortraitStubs(t, claudeCodePluginDir)
	old := filepath.Join(s.home, ".agents", "agent-of-you")
	writePortraitFile(t, filepath.Join(old, "portrait.md"), "Made: 2026-11-24\n\n# Bo's agentic self\n")
	writePortraitFile(t, filepath.Join(old, "portrait-2026-11-23-1000.md"), "Made: 2026-11-23\n")
	built := writePortraitFolder(t, filepath.Join(s.home, "work"), "portrait")
	text, _ := os.ReadFile(filepath.Join(built, "PORTRAIT.md"))
	writePortraitFile(t, filepath.Join(built, "PORTRAIT.md"), strings.Replace(string(text), "name: portrait", "name: bo", 1))

	out, stderr, code := s.run(t, nil, "save", built)
	if code != 0 {
		t.Fatalf("save: %d %q %q", code, out, stderr)
	}
	if !regexp.MustCompile(`^moved the 0\.4 portrait to ~/\.agents/portraits/bo/versions/\d{4}-\d{2}-\d{2}-\d{4}-0\.4\nsaved ~/\.agents/portraits/bo\nok: bo \(portrait/1, version 2\)\n$`).MatchString(out) {
		t.Fatalf("save output %q", out)
	}
	dest := filepath.Join(s.home, ".agents", "portraits", "bo")
	migrated, _ := filepath.Glob(filepath.Join(dest, "versions", "*-0.4", "*"))
	if len(migrated) != 2 {
		t.Fatalf("0.4 files not migrated: %v", migrated)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the empty 0.4 folder should be gone")
	}
	for _, path := range []string{"PORTRAIT.md", "scenarios.md", "examples.md", "corrections.md", "references/methods.md", "scripts/portrait"} {
		info, err := os.Stat(filepath.Join(dest, path))
		if err != nil {
			t.Fatalf("saved folder lacks %s", path)
		}
		want := os.FileMode(0o600)
		if path == "scripts/portrait" {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode %v", path, info.Mode().Perm())
		}
	}
	if info, _ := os.Stat(dest); info.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v", info.Mode().Perm())
	}

	// A second save keeps the first under versions/, and drops a file the
	// new build no longer has.
	writePortraitFile(t, filepath.Join(built, "PORTRAIT.md"), strings.Replace(string(text), "name: portrait", "name: bo", 1)+"")
	if err := os.Remove(filepath.Join(built, "references", "methods.md")); err != nil {
		t.Fatal(err)
	}
	out, _, code = s.run(t, nil, "save", built)
	if code != 0 || !regexp.MustCompile(`^kept the earlier version in ~/\.agents/portraits/bo/versions/\d{4}-\d{2}-\d{2}-\d{4}(-\d+)?\nsaved`).MatchString(out) {
		t.Fatalf("second save: %d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(dest, "references", "methods.md")); !os.IsNotExist(err) {
		t.Error("a file the new build dropped is still there")
	}
	firsts, _ := filepath.Glob(filepath.Join(dest, "versions", "*", "references", "methods.md"))
	if len(firsts) != 1 {
		t.Errorf("the first version is not under versions/: %v", firsts)
	}
	// A helper can never save.
	if _, _, code := s.run(t, []string{"ISOFORM_PORTRAIT_HELPER=1"}, "save", built); code != 65 {
		t.Errorf("save inside a helper exit %d", code)
	}
}

// install puts a pointer to the portrait where each agent looks, keeps the
// person's own instructions, and uninstall removes exactly that.
func TestPortraitInstallEveryAgent(t *testing.T) {
	s := newPortraitStubs(t, claudeCodePluginDir, "alma")
	dir := writePortraitFolder(t, filepath.Join(s.home, ".agents", "portraits"), "bo")
	agentsMD := filepath.Join(s.home, ".codex", "AGENTS.md")
	geminiMD := filepath.Join(s.home, ".gemini", "GEMINI.md")
	writePortraitFile(t, agentsMD, "# My rules\n- Be brief.\n")
	want := map[string]string{
		"claude": "wrote " + filepath.Join(s.home, ".claude", "skills", "portrait-bo", "SKILL.md") + "\n",
		"cursor": "wrote " + filepath.Join(s.home, ".cursor", "skills", "portrait-bo", "SKILL.md") + "\n",
		"kimi":   "wrote " + filepath.Join(s.home, ".agents", "skills", "portrait-bo", "SKILL.md") + "\n",
		"codex":  "wrote a portrait block in " + agentsMD + "\n",
		"gemini": "wrote a portrait block in " + geminiMD + "\n",
		"alma":   "wrote alma:skill:portrait-bo\n",
	}
	for agent, line := range want {
		for i := 0; i < 2; i++ { // installing twice changes nothing
			if out, code := runPortrait(t, s.home, dir, nil, "install", agent); code != 0 || out != line {
				t.Fatalf("install %s: %d %q", agent, code, out)
			}
		}
	}
	for _, agent := range []string{"claude", "cursor", "kimi"} {
		path := strings.TrimSuffix(strings.TrimPrefix(want[agent], "wrote "), "\n")
		data, _ := os.ReadFile(path)
		parts := strings.SplitN(string(data), "---\n", 3)
		var front struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		}
		if len(parts) != 3 || yaml.UnmarshalStrict([]byte(parts[1]), &front) != nil || front.Name != "portrait-bo" || filepath.Base(filepath.Dir(path)) != front.Name || !strings.HasPrefix(front.Description, "Bo's agentic self") {
			t.Fatalf("%s pointer skill:\n%s", agent, data)
		}
		if !strings.Contains(parts[2], "Read "+dir+"/PORTRAIT.md now and follow it.") {
			t.Errorf("%s pointer does not load the portrait:\n%s", agent, data)
		}
	}
	codex, _ := os.ReadFile(agentsMD)
	if string(codex) != "# My rules\n- Be brief.\n\n<!-- isoform-portrait:bo:begin joined=nl -->\nRead "+dir+"/PORTRAIT.md at the start of each task and follow it: it is the person's Agent of You portrait. Read its other files only when it points you to them.\n<!-- isoform-portrait:bo:end -->\n" {
		t.Fatalf("AGENTS.md:\n%s", codex)
	}
	if gemini, _ := os.ReadFile(geminiMD); !strings.Contains(string(gemini), "\n@"+dir+"/PORTRAIT.md\n") || strings.Count(string(gemini), "isoform-portrait:bo:begin") != 1 {
		t.Fatalf("GEMINI.md:\n%s", gemini)
	}
	if !strings.Contains(s.calls(t), "alma skill create portrait-bo --file ") {
		t.Fatalf("alma: %s", s.calls(t))
	}
	for agent := range want {
		if _, code := runPortrait(t, s.home, dir, nil, "uninstall", agent); code != 0 {
			t.Fatalf("uninstall %s", agent)
		}
	}
	if codex, _ := os.ReadFile(agentsMD); string(codex) != "# My rules\n- Be brief.\n" {
		t.Fatalf("uninstall changed the person's AGENTS.md:\n%q", codex)
	}
	for _, path := range []string{geminiMD, filepath.Join(s.home, ".claude", "skills", "portrait-bo"), filepath.Join(s.home, ".cursor", "skills", "portrait-bo"), filepath.Join(s.home, ".agents", "skills", "portrait-bo")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("uninstall left %s", path)
		}
	}
	if !strings.Contains(s.calls(t), "alma skill uninstall portrait-bo") {
		t.Error("alma uninstall not called")
	}
}

// uninstall takes out exactly what install added to the person's global
// AGENTS.md or GEMINI.md, byte for byte, whatever the file looked like.
func TestPortraitUninstallRestoresInstructionsByteForByte(t *testing.T) {
	s := newPortraitStubs(t, claudeCodePluginDir)
	dir := writePortraitFolder(t, filepath.Join(s.home, ".agents", "portraits"), "bo")
	other := writePortraitFolder(t, filepath.Join(s.home, ".agents", "portraits"), "ana")
	text, _ := os.ReadFile(filepath.Join(other, "PORTRAIT.md"))
	writePortraitFile(t, filepath.Join(other, "PORTRAIT.md"), strings.Replace(string(text), "name: bo", "name: ana", 1))
	agentsMD := filepath.Join(s.home, ".codex", "AGENTS.md")
	for name, original := range map[string]*string{
		"no file":            nil,
		"empty":              ptr(""),
		"final newline":      ptr("# Rules\n- Be brief.\n"),
		"no final newline":   ptr("# Rules\n- Be brief."),
		"trailing blanks":    ptr("# Rules\n\n\n"),
		"only blank lines":   ptr("\n\n"),
		"carriage returns":   ptr("# Rules\r\n- Be brief.\r\n"),
		"marker-like prefix": ptr("x <!-- isoform-portrait:bo:begin joined=nl -->\n"),
	} {
		_ = os.Remove(agentsMD)
		if original != nil {
			writePortraitFile(t, agentsMD, *original)
		}
		// Install twice, add another portrait's block, and let the person
		// write below ours; then remove ours.
		for i := 0; i < 2; i++ {
			if out, code := runPortrait(t, s.home, dir, nil, "install", "codex"); code != 0 {
				t.Fatalf("%s: install: %q", name, out)
			}
		}
		if out, code := runPortrait(t, s.home, other, nil, "install", "codex"); code != 0 {
			t.Fatalf("%s: install ana: %q", name, out)
		}
		if out, code := runPortrait(t, s.home, other, nil, "uninstall", "codex"); code != 0 {
			t.Fatalf("%s: uninstall ana: %q", name, out)
		}
		if out, code := runPortrait(t, s.home, dir, nil, "uninstall", "codex"); code != 0 || !strings.HasPrefix(out, "removed the portrait block from ") {
			t.Fatalf("%s: uninstall: %d %q", name, code, out)
		}
		data, err := os.ReadFile(agentsMD)
		switch {
		case original == nil:
			if !os.IsNotExist(err) {
				t.Errorf("%s: uninstall left a file install created: %q", name, data)
			}
		case err != nil:
			t.Errorf("%s: uninstall removed the person's file", name)
		case string(data) != *original:
			t.Errorf("%s: AGENTS.md changed:\nwant %q\ngot  %q", name, *original, data)
		}
	}
	// Uninstall with no block of ours leaves the file alone.
	writePortraitFile(t, agentsMD, "# Rules")
	if out, code := runPortrait(t, s.home, dir, nil, "uninstall", "codex"); code != 0 || out != "" {
		t.Errorf("uninstall without a block: %d %q", code, out)
	}
	if data, _ := os.ReadFile(agentsMD); string(data) != "# Rules" {
		t.Errorf("uninstall without a block changed the file: %q", data)
	}
}

func ptr(s string) *string { return &s }

// The blind test: as-me answers each situation from the portrait alone, and a
// separate judge, with no tools and no portrait, scores it against the
// person's real answer. Neither sees what the other must not.
func TestPortraitEvalIsBlindBothWays(t *testing.T) {
	s := newPortraitStubs(t, claudeCodePluginDir)
	portrait := writePortraitFolder(t, filepath.Join(s.home, ".agents", "portraits"), "bo")
	text, _ := os.ReadFile(filepath.Join(portrait, "PORTRAIT.md"))
	writePortraitFile(t, filepath.Join(portrait, "PORTRAIT.md"), string(text)+"\nPORTRAIT-ONLY-MARKER\n")
	// A claude stub that records each run's arguments and working folder.
	runs := filepath.Join(s.root, "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		t.Fatal(err)
	}
	stub := `#!/bin/sh
n=$(ls "` + runs + `" | grep -c args)
printf '%s\n' "$@" > "` + runs + `/$n.args"
{ pwd; ls -A; } > "` + runs + `/$n.cwd"
case "$*" in
*"judge in a blind test"*)
	case "$*" in
	*CASE-WRONG*) printf 'Verdict: wrong\nWhy: the person waited for a yes.\n' ;;
	*CASE-CLOSE*) printf '**Verdict:** Close\nWhy: same call, different order.\n' ;;
	*CASE-BROKEN*) echo "I cannot judge this." ;;
	*) printf 'Verdict: match\nWhy: the same decision.\n' ;;
	esac ;;
*) printf 'I would merge it now and tell Bo after.\nLeaned on: merge reviewed fixes without asking (this project)\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(s.bin, "claude"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := filepath.Join(s.home, "cases")
	for name, c := range map[string][2]string{
		"a-match":  {"A reviewed fix is ready on a Friday evening. CASE-MATCH", "Merged it at once. Reason: it was reviewed. ANSWER-A-SECRET"},
		"b-close":  {"A pull request has one flaky test. CASE-CLOSE", "Re-ran the test, then merged. Reason: flaky. ANSWER-B-SECRET"},
		"c-wrong":  {"A customer asks for a refund by email. CASE-WRONG", "Waited for a yes before replying. Reason: money. ANSWER-C-SECRET"},
		"d-broken": {"A meeting moved. CASE-BROKEN", "Accepted the new time. Reason: free then. ANSWER-D-SECRET"},
	} {
		writePortraitFile(t, filepath.Join(cases, name, "situation.md"), c[0]+"\n")
		writePortraitFile(t, filepath.Join(cases, name, "answer.md"), c[1]+"\n")
	}
	writePortraitFile(t, filepath.Join(cases, "e-incomplete", "situation.md"), "No answer here.\n")

	out, stderr, code := s.run(t, nil, "eval", cases)
	if code != 0 {
		t.Fatalf("eval: %d %q %q", code, out, stderr)
	}
	if out != "a-match: match\nb-close: close\nc-wrong: wrong\nd-broken: error\nscore: 37% of 4; wrote "+cases+"/results.md\n" {
		t.Fatalf("eval output %q", out)
	}
	// Eight runs: as-me then the judge, for each of the four full cases.
	files, _ := filepath.Glob(filepath.Join(runs, "*.args"))
	if len(files) != 8 {
		t.Fatalf("runs: %d", len(files))
	}
	for i := 0; i < 8; i++ {
		args, _ := os.ReadFile(filepath.Join(runs, strconv.Itoa(i)+".args"))
		cwd, _ := os.ReadFile(filepath.Join(runs, strconv.Itoa(i)+".cwd"))
		a, c := string(args), string(cwd)
		if !strings.Contains(a, "\n--effort\nmax\n") || !strings.Contains(a, "\n--strict-mcp-config\n") || !strings.Contains(a, "\n--no-session-persistence\n") {
			t.Errorf("run %d: not a max-effort, MCP-free, unsaved run:\n%s", i, a)
		}
		if strings.Contains(c, cases) || strings.Contains(c, "answer.md") || strings.Contains(c, "PORTRAIT.md") {
			t.Errorf("run %d started where it can see the cases or the portrait:\n%s", i, c)
		}
		if i%2 == 0 {
			// as-me: the situation and the portrait path, never an answer.
			if strings.Contains(a, "ANSWER-") || strings.Contains(a, cases) || strings.Contains(a, "judge") {
				t.Errorf("as-me run %d saw the answer or the cases:\n%s", i, a)
			}
			if !strings.Contains(a, "\n--tools\nRead\n--allowed-tools\nRead(/"+portrait+"/**)\n") || !strings.Contains(a, portrait+"/PORTRAIT.md") || !strings.Contains(a, "Leaned on:") {
				t.Errorf("as-me run %d must read only the portrait:\n%s", i, a)
			}
			if !strings.HasSuffix(strings.TrimSpace(c), "situation.md") || strings.Count(c, "\n") != 2 {
				t.Errorf("as-me run %d folder must hold only situation.md:\n%s", i, c)
			}
		} else {
			// The judge: the answer and as-me's reply, never the portrait,
			// and no tools at all.
			if strings.Contains(a, "PORTRAIT-ONLY-MARKER") || strings.Contains(a, portrait) || strings.Contains(a, "agentic self") {
				t.Errorf("judge run %d saw the portrait:\n%s", i, a)
			}
			if !strings.Contains(a, "\n--tools\n\n") || strings.Contains(a, "--allowed-tools") {
				t.Errorf("judge run %d must have no tools:\n%s", i, a)
			}
			if !strings.Contains(a, "ANSWER-") || !strings.Contains(a, "I would merge it now") {
				t.Errorf("judge run %d lacks the answer or the reply:\n%s", i, a)
			}
			if strings.TrimSpace(c) != strings.Split(c, "\n")[0] {
				t.Errorf("judge run %d folder must be empty:\n%s", i, c)
			}
		}
	}
	results, err := os.ReadFile(filepath.Join(cases, "results.md"))
	if err != nil {
		t.Fatal(err)
	}
	r := string(results)
	for _, want := range []string{
		"# Blind test: bo, ",
		"- Portrait: bo, version 2, updated 2026-11-24, made 2026-11-20\n- As-me: claude, effort max\n- Judge: claude, effort max\n",
		"Score: 37% (1 match, 1 close, 1 wrong, of 4; match counts 1, close 0.5).",
		"| a-match | match | the same decision. |",
		"| b-close | close | same call, different order. |",
		"| c-wrong | wrong | the person waited for a yes. |",
		"| d-broken | error | the judge gave no verdict (status 0) |",
		"## Candidate scenarios",
		"## <n>. c-wrong\n- Situation: A customer asks for a refund by email. CASE-WRONG\n- Agent would: I would merge it now and tell Bo after.\n- Verdict: not yet\n- Reason:\n- Date: ",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("results.md lacks %q:\n%s", want, r)
		}
	}
	if strings.Contains(r, "e-incomplete") || strings.Contains(r, "## <n>. a-match") {
		t.Errorf("results.md lists the wrong cases:\n%s", r)
	}
	if info, _ := os.Stat(filepath.Join(cases, "results.md")); info.Mode().Perm() != 0o600 {
		t.Errorf("results.md mode %v", info.Mode().Perm())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(s.home, "isoform-portrait-eval.*")); len(leftovers) != 0 {
		t.Errorf("eval left %v", leftovers)
	}
	// No portrait: one clear line.
	empty := newPortraitStubs(t, claudeCodePluginDir)
	if _, stderr, code := empty.run(t, nil, "eval", cases); code != 1 || !strings.Contains(stderr, "no portrait") {
		t.Errorf("eval without a portrait: %d %q", code, stderr)
	}
}
