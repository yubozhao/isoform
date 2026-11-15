package main

import (
	"testing"

	"wails-gui/internal/platform/activitymon"
)

func taskLifecycleTestResultDestination() *TaskLifecycleResultDestination {
	return taskLifecycleTestDestination(
		"com.apple.mail",
		"Mail",
		"Customer update",
	)
}

func taskLifecycleTestSendOnReturnDestination() *TaskLifecycleResultDestination {
	return taskLifecycleTestDestination(
		"com.apple.MobileSMS",
		"Messages",
		"Customer conversation",
	)
}

func taskLifecycleTestDestination(
	bundleID, appName, windowTitle string,
) *TaskLifecycleResultDestination {
	return taskLifecycleResultDestinationForEvent(activitymon.ActivityEvent{
		Kind:                   activitymon.EventKeyboard,
		AppBundleID:            bundleID,
		AppName:                appName,
		WindowTitle:            windowTitle,
		TypedText:              "source-backed text",
		TypedTextCaptureMethod: activitymon.TypedTextCaptureMethodAXFieldDiff,
	})
}

func TestTaskLifecycleResultDestinationRoleMatrix(t *testing.T) {
	tests := []struct {
		name        string
		bundleID    string
		appName     string
		windowTitle string
		want        string
	}{
		{
			name:     "native ChatGPT",
			bundleID: "com.openai.chat",
			appName:  "ChatGPT",
			want:     TaskLifecycleDestinationDelegatedAIPrompt,
		},
		{
			name:        "ChatGPT browser tab",
			bundleID:    "com.apple.Safari",
			appName:     "Safari",
			windowTitle: "Proposal review - ChatGPT",
			want:        TaskLifecycleDestinationDelegatedAIPrompt,
		},
		{
			name:        "generic browser",
			bundleID:    "com.apple.Safari",
			appName:     "Safari",
			windowTitle: "Customer proposal",
			want:        TaskLifecycleDestinationUnknown,
		},
		{
			name:     "native Mail",
			bundleID: "com.apple.mail",
			appName:  "Mail",
			want:     TaskLifecycleDestinationUserResultCandidate,
		},
		{
			name:        "Gmail browser tab",
			bundleID:    "com.google.Chrome",
			appName:     "Google Chrome",
			windowTitle: "Draft - Gmail",
			want:        TaskLifecycleDestinationUserResultCandidate,
		},
		{
			name:        "Google Docs browser tab",
			bundleID:    "com.apple.Safari",
			appName:     "Safari",
			windowTitle: "Customer update - Google Docs",
			want:        TaskLifecycleDestinationUserResultCandidate,
		},
		{
			name:     "terminal",
			bundleID: "com.apple.Terminal",
			appName:  "Terminal",
			want:     TaskLifecycleDestinationUnknown,
		},
		{
			name: "missing identity",
			want: TaskLifecycleDestinationUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := taskLifecycleResultDestinationRole(
				test.bundleID,
				test.appName,
				test.windowTitle,
			); got != test.want {
				t.Fatalf("role=%q, want %q", got, test.want)
			}
		})
	}
}

func TestTaskLifecycleResultDestinationRejectsForgedRole(t *testing.T) {
	destination := taskLifecycleTestDestination(
		"com.openai.chat",
		"ChatGPT",
		"Proposal review",
	)
	destination.SemanticRole = TaskLifecycleDestinationUserResultCandidate
	if _, err := normalizeTaskLifecycleResultDestination(destination); err == nil {
		t.Fatal("forged semantic role was accepted")
	}
}

func TestTaskLifecycleTextCommitTerminalPolicy(t *testing.T) {
	tests := []struct {
		name        string
		bundleID    string
		appName     string
		windowTitle string
		want        bool
	}{
		{name: "Messages", bundleID: "com.apple.MobileSMS", appName: "Messages", want: true},
		{name: "Slack", bundleID: "com.tinyspeck.slackmacgap", appName: "Slack", want: true},
		{name: "Teams", bundleID: "com.microsoft.teams2", appName: "Microsoft Teams", want: true},
		{name: "Discord", bundleID: "com.hnc.discord", appName: "Discord", want: true},
		{name: "conflicting Slack identity", bundleID: "com.example.editor", appName: "Slack"},
		{name: "Slack without bundle identity", appName: "Slack", want: true},
		{name: "Mail", bundleID: "com.apple.mail", appName: "Mail"},
		{name: "Notes", bundleID: "com.apple.Notes", appName: "Notes"},
		{name: "Gmail", bundleID: "com.apple.Safari", appName: "Safari", windowTitle: "Draft - Gmail"},
		{name: "Google Docs", bundleID: "com.apple.Safari", appName: "Safari", windowTitle: "Plan - Google Docs"},
		{name: "delegated AI", bundleID: "com.openai.codex", appName: "ChatGPT"},
		{name: "unknown browser", bundleID: "com.apple.Safari", appName: "Safari", windowTitle: "Customer portal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destination := taskLifecycleTestDestination(
				test.bundleID,
				test.appName,
				test.windowTitle,
			)
			if got := taskLifecycleTextCommitCanTerminate(destination); got != test.want {
				t.Fatalf("terminal=%t, want %t for %+v", got, test.want, destination)
			}
		})
	}
}
