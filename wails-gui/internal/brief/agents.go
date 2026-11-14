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

// agentBuildCues are words that say software was made or changed. Seen only in
// agent hosts, that work was the agent's, directed by the user: the user asks
// Claude Code for a change and reviews the result, which observation sees as
// the user building it ("Build Scrollback app ..." seen only in Claude). They
// are matched like agentCues, with a space before each, so "ship" does not
// match "sponsorship". Planning, asking and designing in an agent host stay
// the user's: those words are not here.
var agentBuildCues = []string{
	" build", " built", " implement", " refactor", " debug", " fix", " ship",
	" code", " coding", " prototype", " scaffold", " deploy", " migrate",
	" commit", " write tests", " wrote tests",
}

// AgentBuildCue reports whether text says software was made or changed.
func AgentBuildCue(text string) bool {
	lower := " " + strings.ToLower(strings.Join(strings.Fields(text), " ")) + " "
	for _, cue := range agentBuildCues {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

// AgentDriven classifies one record by the apps it was seen in and its words:
// words that name agent work, in agent hosts only or with no apps known; or
// words that say software was built, seen in agent hosts only.
func AgentDriven(apps []string, text string) bool {
	known := false
	for _, app := range apps {
		if strings.TrimSpace(app) == "" {
			continue
		}
		if !AgentHost(app) {
			return false
		}
		known = true
	}
	return AgentCue(text) || known && AgentBuildCue(text)
}

// agentDoers are words that name who did agent work: the agents, not the
// user. Narrower than agentCues, which also hold the work itself ("merge").
var agentDoers = []string{
	"agent", "claude", "codex", "devin", "kimi", "assistant", "automation", "automated",
	"scheduled", "the hub", "isoform", "智能体", "代理", "助手", "自动",
}

// NamesAgents reports whether text says agents did the work.
func NamesAgents(text string) bool {
	lower := strings.ToLower(text)
	for _, doer := range agentDoers {
		if strings.Contains(lower, doer) {
			return true
		}
	}
	return false
}
