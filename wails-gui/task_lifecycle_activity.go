package main

import (
	"strings"

	"wails-gui/internal/platform/activitymon"
)

func classifyTaskLifecycleEvent(event activitymon.ActivityEvent) string {
	if event.Kind == activitymon.EventSpeech &&
		event.SpeechSource == "microphone" &&
		(event.SpeechSpeakerVerified == nil || *event.SpeechSpeakerVerified) {
		speech := strings.ToLower(strings.Join(strings.Fields(event.SpeechText), " "))
		switch {
		case strings.Contains(speech, "i am done"),
			strings.Contains(speech, "i'm done"),
			strings.Contains(speech, "i finished"),
			strings.Contains(speech, "we are done"),
			strings.Contains(speech, "we're done"):
			return "explicit_completion"
		case strings.Contains(speech, "i give up"),
			strings.Contains(speech, "i'm abandoning"),
			strings.Contains(speech, "i am abandoning"):
			return "explicit_abandonment"
		}
	}
	if event.Kind == activitymon.EventKeyboard {
		if taskLifecycleTextCommitCandidate(event) {
			return TaskLifecycleEvidenceTextCommit
		}
		return string(event.Kind)
	}
	if event.Kind != activitymon.EventClick {
		return string(event.Kind)
	}
	label := strings.ToLower(strings.Join(strings.Fields(event.ClickLabel), " "))
	switch {
	case label == "send" || strings.HasPrefix(label, "send "):
		return "sent"
	case label == "submit" || strings.HasPrefix(label, "submit "):
		return "submission"
	case label == "publish" || strings.HasPrefix(label, "publish "):
		return "published"
	case label == "merge" || strings.HasPrefix(label, "merge "):
		return "merged"
	case label == "discard" || strings.HasPrefix(label, "discard "):
		return "discarded"
	case label == "cancel task" || label == "cancel job":
		return "cancelled"
	default:
		return string(event.Kind)
	}
}

func taskLifecycleTextCommitCandidate(event activitymon.ActivityEvent) bool {
	if event.Kind != activitymon.EventKeyboard {
		return false
	}
	for _, key := range event.GetKeyChars() {
		switch strings.TrimSpace(key) {
		case "Return", "⌘Return":
			return true
		}
	}
	return false
}
