package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"

	"wails-gui/storage"
)

func TestSafeLogErrorKeepsCodeTextAndDropsUserContent(t *testing.T) {
	_, pathErr := os.Open("/Users/someone/Ada Lovelace/notes.md")
	userText := errors.New("model said: email Ada Lovelace <ada@example.com>")
	cases := []struct {
		err  error
		want string
	}{
		{withLogStage("read main chat view", context.DeadlineExceeded), "read main chat view: context deadline exceeded"},
		{withLogStage("brief", withLogStage("seal", errMobileBriefSourcesChanged)), "brief: seal: " + errMobileBriefSourcesChanged.Error()},
		{withLogStage("project topic membership", storage.ErrReadRevisionChanged), "project topic membership: source database changed before publication"},
		{withLogStage("ranked morning", withLogStage("write and judge", codeError("draft exceeds word ceiling"))), "ranked morning: write and judge: draft exceeds word ceiling"},
		{withLogStage("gather", pathErr), "gather: file open: errno no such file or directory"},
		{withLogStage("pipeline", userText), "pipeline: unlisted *errors.errorString"},
		{withLogStage("gather", fmt.Errorf("read Ada Lovelace's thread: %w", context.Canceled)), "gather: *fmt.wrapError: context canceled"},
		{errors.Join(fs.ErrNotExist, userText), "[file does not exist; unlisted *errors.errorString]"},
	}
	for _, tc := range cases {
		got := safeLogError(tc.err)
		if got != tc.want {
			t.Errorf("safeLogError(%q) = %q, want %q", tc.err, got, tc.want)
		}
		for _, secret := range []string{"Ada", "Lovelace", "example.com", "/Users/", "notes.md", "model said"} {
			if strings.Contains(got, secret) {
				t.Errorf("log text %q leaked %q", got, secret)
			}
		}
	}
	if !errors.Is(withLogStage("ranked morning", errMobileBriefSourcesChanged), errMobileBriefSourcesChanged) || mobileBriefFailureKind(withLogStage("x", context.DeadlineExceeded)) != "timeout" {
		t.Fatal("log stages must keep the error chain for callers")
	}
}
