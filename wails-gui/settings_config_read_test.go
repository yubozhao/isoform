package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafetySettingReadsAndWritesRejectMalformedConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := yansuConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	const broken = `{"assistantHistoryOff":`
	if err := os.WriteFile(path, []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	if _, err := app.GetReadAssistantHistory(); err == nil {
		t.Fatal("history read accepted malformed config")
	}
	if err := updateConfig(func(c *yansuConfig) { c.AssistantHistoryOff = true }); err == nil {
		t.Fatal("write accepted malformed config")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != broken {
		t.Fatalf("config overwritten: %q, %v", got, err)
	}
}

func TestLoginStatusFromCodeRejectsFailedRead(t *testing.T) {
	if _, err := loginStatusFromCode(-1); err == nil {
		t.Fatal("failed login status read accepted")
	}
	for _, status := range []int{0, 1, 2, 3} {
		got, err := loginStatusFromCode(status)
		if err != nil || got != (status == 1) {
			t.Fatalf("status %d: %v, %v", status, got, err)
		}
	}
}

func TestUnavailableUIStateDoesNotPretendToReadOrSave(t *testing.T) {
	app := &App{}
	if _, err := app.LoadUIState(triageEnableMemoryKey); err == nil {
		t.Fatal("unavailable store read succeeded")
	}
	if err := app.SaveUIState(triageEnableMemoryKey, "false"); err == nil {
		t.Fatal("unavailable store save succeeded")
	}
}
