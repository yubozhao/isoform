//go:build offfeatures

// Feature being deleted (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundledOpenCLISkillSpecsCoverEmbeddedDirs(t *testing.T) {
	entries, err := bundledOpenCLISkillsFS.ReadDir(bundledOpenCLIEmbedRoot)
	if err != nil {
		t.Fatalf("read embed root: %v", err)
	}
	embedded := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			embedded[e.Name()] = true
		}
	}
	if len(embedded) == 0 {
		t.Fatal("no skills embedded — check //go:embed directive")
	}
	specs := map[string]bool{}
	for _, s := range bundledOpenCLISkillSpecs {
		specs[s.EmbedSubdir] = true
	}
	for name := range embedded {
		if !specs[name] {
			t.Errorf("embedded skill %q has no spec entry", name)
		}
	}
	for name := range specs {
		if !embedded[name] {
			t.Errorf("spec entry %q has no embedded directory", name)
		}
	}
}

func TestBundledOpenCLISkillsHaveValidFrontmatter(t *testing.T) {
	for _, spec := range bundledOpenCLISkillSpecs {
		t.Run(spec.Slug, func(t *testing.T) {
			data, err := bundledOpenCLISkillsFS.ReadFile(bundledOpenCLIEmbedRoot + "/" + spec.EmbedSubdir + "/SKILL.md")
			if err != nil {
				t.Fatalf("read SKILL.md: %v", err)
			}
			desc := parseSkillDescription(data)
			if desc == "" {
				t.Fatalf("description not extracted from frontmatter; first 200 bytes=%q", data[:min(200, len(data))])
			}
			if len(desc) > maxSkillDescriptionBytes {
				t.Logf("description is %d bytes (max %d) — UI may truncate", len(desc), maxSkillDescriptionBytes)
			}
		})
	}
}

func TestParseSkillDescriptionInline(t *testing.T) {
	data := []byte("---\nname: foo\ndescription: Hello world\n---\n\n# body\n")
	if got := parseSkillDescription(data); got != "Hello world" {
		t.Fatalf("got %q, want %q", got, "Hello world")
	}
}

func TestParseSkillDescriptionMultiline(t *testing.T) {
	data := []byte("---\nname: foo\ndescription:\n  Line one\n  Line two\nallowed-tools: Bash\n---\n")
	got := parseSkillDescription(data)
	if got != "Line one Line two" {
		t.Fatalf("got %q, want %q", got, "Line one Line two")
	}
}

func TestParseSkillDescriptionMissing(t *testing.T) {
	if got := parseSkillDescription([]byte("# no frontmatter\n")); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := parseSkillDescription([]byte("---\nname: foo\n---\n")); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestWithOpenCLIManagedMarkerAppendsOnce(t *testing.T) {
	out := withOpenCLIManagedMarker([]byte("body\n"))
	if !strings.Contains(string(out), bundledOpenCLISkillManaged) {
		t.Fatal("marker not appended")
	}
	if strings.Count(string(out), bundledOpenCLISkillManaged) != 1 {
		t.Fatalf("marker added more than once: %s", out)
	}
}

func TestWriteManagedOpenCLIFilePreservesUserEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	userContent := "user wrote this — no marker\n"
	if err := os.WriteFile(path, []byte(userContent), 0o644); err != nil {
		t.Fatalf("seed user file: %v", err)
	}
	writeManagedOpenCLIFile(path, withOpenCLIManagedMarker([]byte("yansu would overwrite")))
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != userContent {
		t.Fatalf("user file was clobbered: %s", got)
	}
}

func TestWriteManagedOpenCLIFileRewritesManagedCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	stale := withOpenCLIManagedMarker([]byte("stale"))
	if err := os.WriteFile(path, stale, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fresh := withOpenCLIManagedMarker([]byte("fresh"))
	writeManagedOpenCLIFile(path, fresh)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(fresh) {
		t.Fatalf("expected fresh content, got %s", got)
	}
}

func TestPatchOpenCLIUsageInstallSection(t *testing.T) {
	data, err := bundledOpenCLISkillsFS.ReadFile(bundledOpenCLIEmbedRoot + "/opencli-usage/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}
	patched := patchOpenCLIUsageInstallSection(data)

	// The misleading upstream install instructions must be gone — they tell
	// the agent to npm-install OpenCLI globally, which would either fail
	// or duplicate the bundle Yansu already manages via bun.
	for _, banned := range []string{
		"npm install -g @jackwener/opencli",
		"git clone git@github.com:jackwener/OpenCLI.git",
		"npx tsx src/main.ts",
	} {
		if strings.Contains(string(patched), banned) {
			t.Errorf("patched content still contains forbidden upstream snippet %q", banned)
		}
	}

	if !strings.Contains(string(patched), "## Install (Isoform)") {
		t.Error("patched content missing Isoform install section header")
	}
	if !strings.Contains(string(patched), "already installed and on PATH") {
		t.Error("patched content missing the 'already installed' guidance")
	}
	// Chrome extension is the one install step Yansu can't automate, so
	// the patch must surface it — otherwise the agent will miss the only
	// real prereq for COOKIE / INTERCEPT / UI strategies.
	if !strings.Contains(string(patched), "Chrome extension") {
		t.Error("patched content missing Chrome extension heads-up")
	}

	// Downstream sections must survive the patch — `## Prerequisites by command type`
	// is the next H2 in upstream, and `opencli doctor` and the strategy
	// table are critical for browser-bridge users.
	if !strings.Contains(string(patched), "## Prerequisites by command type") {
		t.Error("patched content lost downstream section headers")
	}
	if !strings.Contains(string(patched), "opencli doctor") {
		t.Error("patched content lost opencli doctor reference")
	}
}

func TestPatchOpenCLIUsageInstallSectionNoOpWhenHeaderMissing(t *testing.T) {
	original := []byte("# something else\n\nno install header here\n")
	got := patchOpenCLIUsageInstallSection(original)
	if string(got) != string(original) {
		t.Fatalf("patcher modified content with no Install header: %s", got)
	}
}

func TestInstallBundledOpenCLISkillEndToEnd(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	spec := bundledOpenCLISkillSpecs[0] // opencli-usage
	if err := installBundledOpenCLISkill(spec); err != nil {
		t.Fatalf("install: %v", err)
	}

	skillDir, err := skillHubSkillDir(spec.Slug)
	if err != nil {
		t.Fatalf("resolve dir: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read installed SKILL.md: %v", err)
	}
	if !strings.Contains(string(body), bundledOpenCLISkillManaged) {
		t.Fatal("installed SKILL.md missing managed marker")
	}

	state, err := loadSkillHubState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	installed, ok := state.Skills[spec.Slug]
	if !ok {
		t.Fatalf("skill %q not registered in state", spec.Slug)
	}
	if installed.Source != bundledOpenCLISkillSourceTag {
		t.Errorf("source = %q, want %q", installed.Source, bundledOpenCLISkillSourceTag)
	}
	if installed.Version != bundledOpenCLISkillVersion {
		t.Errorf("version = %q, want %q", installed.Version, bundledOpenCLISkillVersion)
	}
	if installed.Description == "" {
		t.Error("description not stored in state")
	}
	if !installed.Enabled {
		t.Error("expected newly installed skill to be enabled by default")
	}
}

func TestInstallBundledOpenCLISkillCopiesReferences(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// opencli-adapter-author has nested references/site-memory/*.md
	var spec bundledOpenCLISkillSpec
	for _, s := range bundledOpenCLISkillSpecs {
		if s.Slug == "opencli-adapter-author" {
			spec = s
			break
		}
	}
	if spec.Slug == "" {
		t.Fatal("opencli-adapter-author spec not found")
	}

	if err := installBundledOpenCLISkill(spec); err != nil {
		t.Fatalf("install: %v", err)
	}

	skillDir, err := skillHubSkillDir(spec.Slug)
	if err != nil {
		t.Fatalf("resolve dir: %v", err)
	}
	for _, want := range []string{
		"SKILL.md",
		"references/adapter-template.md",
		"references/site-memory/bilibili.md",
	} {
		full := filepath.Join(skillDir, want)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
}
