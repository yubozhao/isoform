package main

import (
	"errors"
	"strings"

	"wails-gui/internal/platform/activitymon"
)

const (
	taskLifecycleResultDestinationVersion = "task-result-destination-v1"

	TaskLifecycleDestinationUserResultCandidate = "user_result_candidate"
	TaskLifecycleDestinationDelegatedAIPrompt   = "delegated_ai_prompt"
	TaskLifecycleDestinationUnknown             = "unknown"
)

// TaskLifecycleResultDestination binds captured committed text to the exact
// production surface where it appeared. SemanticRole is deterministic derived
// data; admission recomputes it from the raw destination fields.
type TaskLifecycleResultDestination struct {
	Version       string `json:"version"`
	AppBundleID   string `json:"appBundleId"`
	AppName       string `json:"appName"`
	WindowTitle   string `json:"windowTitle"`
	SemanticRole  string `json:"semanticRole"`
	CaptureMethod string `json:"captureMethod"`
}

func taskLifecycleResultDestinationForEvent(
	event activitymon.ActivityEvent,
) *TaskLifecycleResultDestination {
	if event.Kind != activitymon.EventKeyboard ||
		strings.TrimSpace(event.TypedText) == "" ||
		strings.TrimSpace(event.TypedTextCaptureMethod) == "" {
		return nil
	}
	destination := &TaskLifecycleResultDestination{
		Version:       taskLifecycleResultDestinationVersion,
		AppBundleID:   strings.TrimSpace(event.AppBundleID),
		AppName:       truncateTaskOnsetText(event.AppName),
		WindowTitle:   truncateTaskOnsetText(event.WindowTitle),
		CaptureMethod: strings.ToLower(strings.TrimSpace(event.TypedTextCaptureMethod)),
	}
	destination.SemanticRole = taskLifecycleResultDestinationRole(
		destination.AppBundleID,
		destination.AppName,
		destination.WindowTitle,
	)
	return destination
}

func normalizeTaskLifecycleResultDestination(
	value *TaskLifecycleResultDestination,
) (*TaskLifecycleResultDestination, error) {
	if value == nil {
		return nil, nil
	}
	destination := *value
	destination.Version = strings.ToLower(strings.TrimSpace(destination.Version))
	destination.AppBundleID = truncateTaskOnsetText(destination.AppBundleID)
	destination.AppName = truncateTaskOnsetText(destination.AppName)
	destination.WindowTitle = truncateTaskOnsetText(destination.WindowTitle)
	destination.SemanticRole = strings.ToLower(strings.TrimSpace(destination.SemanticRole))
	destination.CaptureMethod = strings.ToLower(strings.TrimSpace(destination.CaptureMethod))
	if destination.Version == "" || destination.SemanticRole == "" ||
		destination.CaptureMethod == "" {
		return nil, errors.New("task lifecycle result destination is incomplete")
	}
	switch destination.SemanticRole {
	case TaskLifecycleDestinationUserResultCandidate,
		TaskLifecycleDestinationDelegatedAIPrompt,
		TaskLifecycleDestinationUnknown:
	default:
		return nil, errors.New("task lifecycle result destination has unsupported semantic role")
	}
	if destination.Version == taskLifecycleResultDestinationVersion {
		expectedRole := taskLifecycleResultDestinationRole(
			destination.AppBundleID,
			destination.AppName,
			destination.WindowTitle,
		)
		if destination.SemanticRole != expectedRole {
			return nil, errors.New("task lifecycle result destination semantic role does not match source fields")
		}
	}
	return &destination, nil
}

func taskLifecycleCurrentUserResultDestination(
	value *TaskLifecycleResultDestination,
) (*TaskLifecycleResultDestination, bool) {
	destination, err := normalizeTaskLifecycleResultDestination(value)
	if err != nil || destination == nil ||
		destination.Version != taskLifecycleResultDestinationVersion ||
		destination.CaptureMethod !=
			activitymon.TypedTextCaptureMethodAXFieldDiff ||
		destination.SemanticRole !=
			TaskLifecycleDestinationUserResultCandidate {
		return nil, false
	}
	return destination, true
}

// taskLifecycleTextCommitCanTerminate is intentionally narrower than result
// destination admission. Return commonly sends in messaging apps, but it only
// inserts a newline in email, document, and note editors. Those editors need a
// separate source-backed terminal event before their retained text can close a
// task.
func taskLifecycleTextCommitCanTerminate(
	value *TaskLifecycleResultDestination,
) bool {
	destination, ok := taskLifecycleCurrentUserResultDestination(value)
	if !ok {
		return false
	}
	bundleID := strings.ToLower(strings.TrimSpace(destination.AppBundleID))
	app := normalizeTaskWorkflowText(destination.AppName)
	switch bundleID {
	case "com.apple.mobilesms",
		"com.tinyspeck.slackmacgap",
		"com.microsoft.teams2",
		"com.hnc.discord":
		return true
	}
	if bundleID != "" {
		return false
	}
	switch app {
	case "messages", "slack", "teams", "microsoft teams", "discord":
		return true
	default:
		return false
	}
}

func taskLifecycleResultDestinationRole(
	bundleID, appName, windowTitle string,
) string {
	bundleID = strings.ToLower(strings.TrimSpace(bundleID))
	app := normalizeTaskWorkflowText(appName)
	windowWords := taskWorkflowWordSet(normalizeTaskWorkflowText(windowTitle))

	if taskLifecycleNativeDelegatedAI(bundleID, app) {
		return TaskLifecycleDestinationDelegatedAIPrompt
	}
	if taskLifecycleBrowserDestination(bundleID, app) {
		if taskWorkflowHasAny(
			windowWords,
			"anthropic", "chatgpt", "claude", "codex", "copilot",
			"gemini", "grok", "perplexity", "yansu",
		) {
			return TaskLifecycleDestinationDelegatedAIPrompt
		}
		if taskWorkflowHasAny(windowWords, "gmail") ||
			(taskWorkflowHasAny(windowWords, "google") &&
				taskWorkflowHasAny(windowWords, "docs", "document")) {
			return TaskLifecycleDestinationUserResultCandidate
		}
		return TaskLifecycleDestinationUnknown
	}
	if taskLifecycleNativeUserResultDestination(bundleID, app) {
		return TaskLifecycleDestinationUserResultCandidate
	}
	return TaskLifecycleDestinationUnknown
}

func taskLifecycleNativeDelegatedAI(bundleID, app string) bool {
	switch bundleID {
	case "com.openai.chat",
		"com.openai.codex",
		"com.anthropic.claudefordesktop",
		"com.anthropic.claude",
		"com.wails.yansu",
		"com.yansu":
		return true
	}
	switch app {
	case "chatgpt", "claude", "codex", "copilot", "gemini", "grok",
		"perplexity", "yansu":
		return true
	default:
		return false
	}
}

func taskLifecycleBrowserDestination(bundleID, app string) bool {
	switch bundleID {
	case "com.apple.safari",
		"com.google.chrome",
		"com.brave.browser",
		"com.microsoft.edgemac",
		"company.thebrowser.browser",
		"org.mozilla.firefox",
		"chrome.exe",
		"msedge.exe",
		"firefox.exe",
		"brave.exe",
		"arc.exe":
		return true
	}
	switch app {
	case "safari", "chrome", "google chrome", "brave", "edge",
		"microsoft edge", "arc", "firefox":
		return true
	default:
		return false
	}
}

func taskLifecycleNativeUserResultDestination(bundleID, app string) bool {
	switch bundleID {
	case "com.apple.mail",
		"com.apple.mobilesms",
		"com.apple.notes",
		"com.apple.textedit",
		"com.apple.iwork.pages",
		"com.tinyspeck.slackmacgap",
		"com.microsoft.teams2",
		"com.microsoft.outlook",
		"com.microsoft.word",
		"com.hnc.discord",
		"com.notion.id",
		"mail.exe",
		"outlook.exe",
		"slack.exe",
		"teams.exe",
		"discord.exe",
		"notion.exe",
		"winword.exe":
		return true
	}
	switch app {
	case "mail", "messages", "notes", "textedit", "pages", "slack",
		"teams", "microsoft teams", "outlook", "microsoft outlook",
		"word", "microsoft word", "discord", "notion", "superhuman":
		return true
	default:
		return false
	}
}
