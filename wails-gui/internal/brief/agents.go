package brief

import "strings"

// Agent-driven activity. Observation sees the screen, so an agent working in
// Claude Code, Codex or a terminal looks like the user working. The brief must
// not credit that work to the user or turn it into the user's next step. A
// record is agent-driven when the apps it was seen in are only agent hosts and
// its words name agent work, or, with no apps known (a learned habit, a goal),
// when its words name agent work outright.

// agentHostApps are the apps agents run in. Matching is on the lowercased name.
var agentHostApps = map[string]bool{
	"claude": true, "claude code": true, "codex": true, "chatgpt codex": true,
	"terminal": true, "iterm2": true, "iterm": true, "ghostty": true, "warp": true,
	"conductor": true, "devin": true,
}

// agentCues are words that name agent work. They are matched on lowercased
// text with spaces around it, so "pr #" and " hub " stay whole.
var agentCues = []string{
	"claude code", "codex", "isoform hub", " the hub", " hub ", "side session", "sub thread",
	"agent", "the assistant", "pull request", " prs ", " pr #", " pr ", "merge", "worktree",
	"devin", "kimi", "scheduled prompt", "scheduled task", "automation", "app runner", "workflow run",
}

// AgentHost reports whether app is an app agents run in.
func AgentHost(app string) bool {
	return agentHostApps[strings.ToLower(strings.TrimSpace(app))]
}

// AgentCue reports whether text names agent work.
func AgentCue(text string) bool {
	lower := " " + strings.ToLower(strings.Join(strings.Fields(text), " ")) + " "
	for _, cue := range agentCues {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

// AgentDriven classifies one record by the apps it was seen in and its words.
func AgentDriven(apps []string, text string) bool {
	if !AgentCue(text) {
		return false
	}
	for _, app := range apps {
		if strings.TrimSpace(app) != "" && !AgentHost(app) {
			return false
		}
	}
	return true
}
