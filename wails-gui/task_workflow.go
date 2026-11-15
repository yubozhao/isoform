package main

import (
	"errors"
	"fmt"
	"strings"
)

const (
	TaskWorkflowComposeText = "compose.text"
	TaskWorkflowAnalyzeText = "analyze.text"
)

var taskWorkflowProductionShadowRegistry = [...]string{
	TaskWorkflowComposeText,
	TaskWorkflowAnalyzeText,
}

var ErrTaskWorkflowOutsideProductionRollout = errors.New(
	"task workflow is outside the production shadow rollout",
)

var ErrTaskWorkflowInsufficientFrozenContext = errors.New(
	"task workflow has insufficient frozen onset context",
)

func normalizeTaskWorkflowText(values ...string) string {
	var joined strings.Builder
	for _, value := range values {
		if joined.Len() > 0 {
			joined.WriteByte(' ')
		}
		joined.WriteString(strings.ToLower(strings.TrimSpace(value)))
	}
	words := strings.FieldsFunc(joined.String(), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	return strings.Join(words, " ")
}

func taskWorkflowWordSet(value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, word := range strings.Fields(value) {
		result[word] = struct{}{}
	}
	return result
}

func taskWorkflowHasAny(words map[string]struct{}, values ...string) bool {
	for _, value := range values {
		if _, ok := words[value]; ok {
			return true
		}
	}
	return false
}

// taskWorkflowComposeTextQualified is deliberately high precision. The first
// parity workflow needs a directly inspectable text result, not every task
// that happens to contain words.
func taskWorkflowComposeTextQualified(task, desiredResult string) bool {
	taskText := normalizeTaskWorkflowText(task)
	allText := normalizeTaskWorkflowText(task, desiredResult)
	if taskText == "" || allText == "" {
		return false
	}
	taskWords := taskWorkflowWordSet(taskText)
	allWords := taskWorkflowWordSet(allText)
	if taskWorkflowHasAny(
		taskWords,
		"code", "coding", "implement", "implementing", "debug", "debugging",
		"test", "testing", "patch", "terminal", "repository", "repo",
	) {
		return false
	}

	artifact := taskWorkflowHasAny(
		allWords,
		"message", "email", "proposal", "brief", "memo", "post", "summary",
		"response", "reply", "document", "copy", "announcement", "update",
		"letter", "note", "text", "onepager",
	) || strings.Contains(allText, "one pager")

	strongAction := taskWorkflowHasAny(
		taskWords,
		"drafting", "compose", "composing", "rewrite", "rewriting", "revise",
		"revising", "refine", "refining", "reply", "replying", "respond",
		"responding",
	)
	if strongAction {
		return true
	}
	transformAction := taskWorkflowHasAny(
		taskWords,
		"adapt", "adapting", "condense", "condensing", "convert", "converting",
		"summarize", "summarizing", "translate", "translating", "transform",
		"transforming",
	)
	if transformAction && artifact {
		return true
	}
	if strings.HasPrefix(taskText, "draft ") ||
		strings.Contains(taskText, " to draft ") ||
		strings.Contains(taskText, " begin draft ") {
		return true
	}
	return artifact && taskWorkflowHasAny(
		taskWords,
		"write", "writing", "edit", "editing", "prepare", "preparing",
		"create", "creating", "produce", "producing",
	)
}

func taskWorkflowComposeTextNeedsSourceContext(task string) bool {
	words := taskWorkflowWordSet(normalizeTaskWorkflowText(task))
	return taskWorkflowHasAny(
		words,
		"adapt", "adapting", "condense", "condensing", "convert", "converting",
		"edit", "editing", "refine", "refining", "reply", "replying",
		"respond", "responding", "revise", "revising", "rewrite", "rewriting",
		"summarize", "summarizing", "translate", "translating", "transform",
		"transforming",
	)
}

// taskWorkflowAnalyzeTextQualified admits only bounded analysis whose result is
// inspectable user-authored text. Research, delegation, implementation, and
// production effects remain outside this workflow even when they contain an
// analysis verb.
func taskWorkflowAnalyzeTextQualified(task, desiredResult string) bool {
	if taskWorkflowComposeTextQualified(task, desiredResult) {
		return false
	}
	taskText := normalizeTaskWorkflowText(task)
	desiredText := normalizeTaskWorkflowText(desiredResult)
	if taskText == "" || desiredText == "" {
		return false
	}
	taskWords := taskWorkflowWordSet(taskText)
	desiredWords := taskWorkflowWordSet(desiredText)

	if taskWorkflowHasAny(
		taskWords,
		"ask", "asking", "browse", "browsing", "build", "building", "buy",
		"buying", "click", "clicking", "code", "coding", "configure",
		"configuring", "create", "creating", "debug", "debugging", "deploy",
		"deploying", "download", "downloading", "edit", "editing", "explore",
		"exploring", "implement", "implementing", "login", "merge", "merging",
		"message", "messaging", "modify", "modifying", "navigate", "navigating",
		"open", "opening", "patch", "prompt", "prompting", "publish",
		"publishing", "purchase", "purchasing", "query", "querying", "request",
		"requesting", "research", "researching", "revise", "revising", "send",
		"sending", "share", "sharing", "submit", "submitting", "test", "testing",
		"update", "updating", "upload", "uploading", "visit", "visiting", "write",
		"writing",
	) {
		return false
	}
	if taskWorkflowHasAny(
		taskWords,
		"branch", "branches", "codebase", "codebases", "commit", "commits",
		"database", "diff", "diffs", "implementation", "oauth", "pr",
		"repository", "repo", "settings", "terminal",
		"chatgpt", "claude", "codex", "copilot", "gemini", "grok",
		"perplexity", "yansu", "assistant", "carousel", "image", "images",
		"photo", "photos", "screenshot", "screenshots", "video", "videos",
		"visual", "visuals",
	) {
		return false
	}

	action := taskWorkflowHasAny(
		taskWords,
		"analyze", "analyzing", "analyse", "analysing", "assess", "assessing",
		"audit", "auditing", "compare", "comparing", "evaluate", "evaluating",
		"explain", "explaining", "identify", "identifying", "inspect", "inspecting",
		"review", "reviewing", "synthesize", "synthesizing", "synthesise",
		"synthesising",
	)
	textResult := taskWorkflowHasAny(
		desiredWords,
		"analysis", "analyses", "answer", "answers", "assessment", "assessments",
		"audit", "audits", "comparison", "comparisons", "concern", "concerns",
		"evaluation", "evaluations", "explanation", "explanations", "finding",
		"findings", "gap", "gaps", "insight", "insights", "issue", "issues",
		"option", "options", "recommendation", "recommendations", "report",
		"reports", "risk", "risks", "summary", "summaries", "synthesis",
		"syntheses", "takeaway", "takeaways", "tradeoff", "tradeoffs",
	) || strings.Contains(desiredText, "trade off") ||
		strings.Contains(desiredText, "trade offs")
	return action && textResult
}

func taskWorkflowProductionShadowWorkflows() []string {
	result := make([]string, len(taskWorkflowProductionShadowRegistry))
	copy(result, taskWorkflowProductionShadowRegistry[:])
	return result
}

func taskWorkflowProductionShadowScope() string {
	return strings.Join(taskWorkflowProductionShadowWorkflows(), ", ")
}

func taskWorkflowCapabilityKey(
	task, desiredResult, fallbackSeed string,
) string {
	if taskWorkflowComposeTextQualified(task, desiredResult) {
		return TaskWorkflowComposeText
	}
	if taskWorkflowAnalyzeTextQualified(task, desiredResult) {
		return TaskWorkflowAnalyzeText
	}
	return normalizeCapabilityKey(task, fallbackSeed)
}

// taskWorkflowProductionShadowEligible defines the deliberately narrow
// production rollout. Generic task-mirror construction remains available for
// fixtures, replay, and future versioned expansions, but production execution
// cannot infer rollout eligibility from confidence or model output.
func taskWorkflowProductionShadowEligible(capabilityKey string) bool {
	capabilityKey = strings.TrimSpace(capabilityKey)
	for _, enabled := range taskWorkflowProductionShadowRegistry {
		if capabilityKey == enabled {
			return true
		}
	}
	return false
}

func taskEpisodeProductionShadowCapability(episode TaskEpisode) string {
	return taskWorkflowCapabilityKey(
		episode.LatestHypothesis.TaskHypothesis,
		episode.LatestHypothesis.DesiredResultHypothesis,
		episode.ID,
	)
}

func taskEpisodeHasFrozenSourceContext(episode TaskEpisode) bool {
	for _, entry := range episode.FrozenContext.Manifest {
		if entry.Included &&
			entry.SourceKind == taskContextSourceActivitySnapshotKind &&
			entry.ObservedAt > 0 &&
			entry.ObservedAt <= episode.InferredStartAt {
			return true
		}
	}
	return false
}

func taskEpisodeProductionShadowReady(
	episode TaskEpisode,
) (bool, error) {
	return taskWorkflowProductionShadowReadiness(
		taskEpisodeProductionShadowCapability(episode),
		episode.LatestHypothesis.TaskHypothesis,
		taskEpisodeHasFrozenSourceContext(episode),
	)
}

func taskWorkflowProductionShadowReadiness(
	workflow, task string,
	hasFrozenSourceContext bool,
) (bool, error) {
	if !taskWorkflowProductionShadowEligible(workflow) {
		return false, fmt.Errorf(
			"%w: workflow %q; enabled workflows are %s",
			ErrTaskWorkflowOutsideProductionRollout,
			workflow,
			taskWorkflowProductionShadowScope(),
		)
	}
	if err := taskWorkflowFrozenContextSafetyError(
		workflow,
		task,
		hasFrozenSourceContext,
	); err != nil {
		return false, err
	}
	return true, nil
}

func taskWorkflowFrozenContextSafetyError(
	workflow, task string,
	hasFrozenSourceContext bool,
) error {
	if workflow == TaskWorkflowComposeText &&
		taskWorkflowComposeTextNeedsSourceContext(task) &&
		!hasFrozenSourceContext {
		return fmt.Errorf(
			"%w: source-dependent composition has no exact pre-onset activity snapshot",
			ErrTaskWorkflowInsufficientFrozenContext,
		)
	}
	if workflow == TaskWorkflowAnalyzeText &&
		!hasFrozenSourceContext {
		return fmt.Errorf(
			"%w: text analysis has no exact pre-onset activity snapshot",
			ErrTaskWorkflowInsufficientFrozenContext,
		)
	}
	return nil
}

func taskShadowBackgroundFallbackReason(err error) string {
	switch {
	case errors.Is(err, ErrTaskWorkflowInsufficientFrozenContext):
		return "Shadow could not safely mirror the observed task because its frozen onset context was incomplete."
	case errors.Is(err, ErrTaskWorkflowOutsideProductionRollout):
		return "The observed task is outside the current Shadow rollout."
	default:
		return "Shadow cannot safely advance the observed task right now."
	}
}
