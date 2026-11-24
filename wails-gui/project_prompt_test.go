package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var projectInstructionFiles = []string{"CLAUDE.md", "AGENTS.md", ".cursorrules", ".windsurfrules", ".github/copilot-instructions.md"}

func TestClonePreservesProjectAgentFilesAndRunContext(t *testing.T) {
	for _, name := range []string{"absent", "user", "legacy"} {
		original := ""
		if name != "absent" {
			original = "User instructions.\n"
		}
		if name == "legacy" {
			original += "# Isoform Project Context\nLegacy text must stay.\n"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			source := filepath.Join(t.TempDir(), "source")
			writeProjectTestFile(t, source, ".gitignore", ".something/\n")
			if original != "" {
				for _, path := range projectInstructionFiles {
					writeProjectTestFile(t, source, path, original)
				}
			}
			for _, args := range [][]string{{"init", source}, {"-C", source, "add", "."}, {"-C", source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "commit", "-m", "fixture"}} {
				if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
			}
			if err := saveConfig(yansuConfig{RepoRoot: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			app := &App{}
			target, err := app.CloneFromURL(source)
			if err != nil {
				t.Fatal(err)
			}
			target, err = filepath.EvalSymlinks(target)
			if err != nil {
				t.Fatal(err)
			}
			assertProjectAgentFiles(t, target, original)
			if _, err := os.Stat(filepath.Join(target, ".git/hooks/post-commit")); !os.IsNotExist(err) {
				t.Fatalf("unexpected post-commit hook: %v", err)
			}

			writeProjectTestFile(t, target, ".something/context.md", "Project requirements")
			runner := wireHandoffPromptCapture(t, app)
			prompt := sendHandoffBoundaryPrompt(t, app, runner, "Fix the project", target, "project-prompt")
			assertProjectRunContext(t, prompt)
			assertProjectAgentFiles(t, target, original)

			worktree := filepath.Join(t.TempDir(), "workspace")
			if out, err := exec.Command("git", "-C", target, "worktree", "add", "--detach", worktree).CombinedOutput(); err != nil {
				t.Fatalf("worktree add: %v: %s", err, out)
			}
			prompt = sendHandoffBoundaryPrompt(t, app, runner, "Fix the project", worktree, "project-worktree-prompt")
			assertProjectRunContext(t, prompt)
			if !strings.Contains(prompt, filepath.ToSlash(filepath.Join(target, ".something/context.md"))) {
				t.Fatal("worktree prompt points at the wrong project context")
			}
			assertProjectAgentFiles(t, worktree, original)
			if _, err := os.Stat(filepath.Join(worktree, ".something")); !os.IsNotExist(err) {
				t.Fatalf("context was copied into the execution worktree: %v", err)
			}
		})
	}
}

func TestProjectScheduledRunReceivesContext(t *testing.T) {
	root := t.TempDir()
	writeProjectTestFile(t, root, ".something/context.md", "Project requirements")
	if err := os.Mkdir(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	runner := wireHandoffPromptCapture(t, app)
	if _, _, _, err := app.executePromptSync(context.Background(), "project-cron", "", filepath.Join(root, "nested"), "", "Fix the project", "m", "", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	input := <-runner.inputs
	assertProjectRunContext(t, input.Prompt)
	if !strings.Contains(input.Prompt, filepath.ToSlash(filepath.Join(root, ".something/context.md"))) {
		t.Fatal("scheduled subdirectory run points at the wrong project context")
	}
	assertProjectAgentFiles(t, root, "")
}

func TestProjectRunWithoutContextReceivesNoProjectInstructions(t *testing.T) {
	app := &App{}
	runner := wireHandoffPromptCapture(t, app)
	prompt := sendHandoffBoundaryPrompt(t, app, runner, "Fix the project", t.TempDir(), "ordinary-project")
	if strings.Contains(prompt, "# Isoform Project Context") {
		t.Fatal("ordinary repository acquired Isoform project instructions")
	}
}

func writeProjectTestFile(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func assertProjectAgentFiles(t *testing.T, root, original string) {
	t.Helper()
	for _, path := range projectInstructionFiles {
		data, err := os.ReadFile(filepath.Join(root, path))
		if original != "" {
			if err != nil || string(data) != original {
				t.Errorf("project instruction file %s changed: %q, %v", path, data, err)
			}
		} else if !os.IsNotExist(err) {
			t.Errorf("project instruction file %s created: %q, %v", path, data, err)
		}
	}
}

func assertProjectRunContext(t *testing.T, prompt string) {
	t.Helper()
	for _, want := range []string{"# Isoform Project Context", ".something/context.md", ".something/knowledge/_index.md", ".something/new-knowledge/", "Fix the project"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("run prompt lost %q", want)
		}
	}
}
