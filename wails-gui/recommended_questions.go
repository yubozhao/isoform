package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// RecommendedQuestion is a single suggestion rendered in the composer empty state.
type RecommendedQuestion struct {
	// Title is a short 4-8 word label shown on the pill.
	Title string `json:"title"`
	// Prompt is the full question that gets seeded into the composer when
	// the user clicks the pill.
	Prompt string `json:"prompt"`
}

// recommendedQuestionsCacheEntry stores a per-language cache slot. Each
// supported UI language gets its own slot so switching languages doesn't
// have to wait for the TTL on the previously-cached language.
type recommendedQuestionsCacheEntry struct {
	questions   []RecommendedQuestion
	generated   time.Time
	contextHash [32]byte
}

// recommendedQuestionsCache keeps a per-process, per-language cache of the
// last generated suggestions. Regeneration is gated by recommendedQuestionsTTL
// so that repeated composer mounts don't hammer the Haiku tier.
type recommendedQuestionsCache struct {
	mu     sync.Mutex
	byLang map[string]*recommendedQuestionsCacheEntry
}

const recommendedQuestionsTTL = 30 * time.Minute

var recommendedQuestionsStore = recommendedQuestionsCache{
	byLang: make(map[string]*recommendedQuestionsCacheEntry),
}

// quickChatCaptureBudget bounds how long captureFocusedAppContext blocks
// while collecting the current app context. The implementation uses
// bundle-targeted capture, then same-app history, then general context.
const quickChatCaptureBudget = 600 * time.Millisecond

// quickChatSuggestionModelBudget keeps the bundle-specific suggestion call under
// the native panel's 4s total budget. If Haiku is slow, we return local
// context-derived chips instead of showing generic placeholders.
const quickChatSuggestionModelBudget = 2500 * time.Millisecond

// normalizeSuggestionLang collapses incoming locale tags from the frontend
// into one of the canonical values the rest of this file understands.
// Anything we don't explicitly recognise becomes "en" so the LLM and the
// fallbacks always have a definite language to render in.
func normalizeSuggestionLang(lang string) string {
	tag := strings.ToLower(strings.TrimSpace(lang))
	switch tag {
	case "zh-cn", "zh_cn", "zh-hans", "zh":
		return "zh-CN"
	case "en", "en-us", "en-gb", "":
		return "en"
	default:
		return "en"
	}
}

// languageDisplayName returns the human-readable language name we inject
// into the prompt so Haiku knows which language to write the chips in.
func languageDisplayName(lang string) string {
	switch lang {
	case "zh-CN":
		return "Simplified Chinese"
	default:
		return "English"
	}
}

// suggestionExamples bundles the in-prompt example surface forms for a
// given language. We keep the examples in the user's language so Haiku
// doesn't anchor on English phrasing when generating Chinese chips.
type suggestionExamples struct {
	good string
	bad  string
}

func examplesForLang(lang string) suggestionExamples {
	switch lang {
	case "zh-CN":
		return suggestionExamples{
			good: `- {"title": "回复 Alice 的 Slack", "prompt": "帮我回复 Alice 关于 API 上线的 Slack 消息。"}
- {"title": "总结 Stripe 合作进展", "prompt": "Stripe 合作目前进展如何？请总结决策与下一步。"}
- {"title": "跟进 Bob 的 PR #421", "prompt": "我应该就 PR #421 跟 Bob 跟进什么？"}`,
			bad: `- {"title": "总结我的一天", "prompt": "..."}
- {"title": "我今天做了什么？", "prompt": "..."}`,
		}
	default:
		return suggestionExamples{
			good: `- {"title": "Draft reply to Alice's Slack", "prompt": "Draft a reply to Alice's Slack thread about the API rollout."}
- {"title": "Summarize Stripe deal progress", "prompt": "What's the latest on the Stripe deal? Summarize decisions and next steps."}
- {"title": "Follow-up with Bob about PR #421", "prompt": "What should I follow up with Bob about PR #421?"}`,
			bad: `- {"title": "Summarize my day", "prompt": "..."}
- {"title": "What did I work on?", "prompt": "..."}`,
		}
	}
}

// GetQuickChatSuggestionsForApp returns suggestion chips biased toward the
// frontmost app the user was looking at when they invoked the quick-chat
// panel. Called from the panel's request-suggestions bridge.
//
// `lang` is the user's UI language tag (e.g. "en" or "zh-CN"); the chips
// are generated in that language so the panel matches the rest of the UI.
//
// Pulls a tiered context (current app → prior activity in the same app →
// general recent-work context) and asks Haiku to generate chips in relevance
// order. Bundle-specific requests fall back to deterministic app-aware chips
// instead of cached composer chips so the panel always suggests something
// without reviving stale unrelated topics.
func (a *App) GetQuickChatSuggestionsForApp(bundleID string, lang string) ([]RecommendedQuestion, error) {
	normLang := normalizeSuggestionLang(lang)
	bundleID = strings.TrimSpace(bundleID)
	if bundleID == "" {
		return a.GetRecommendedQuestions(false, normLang)
	}

	snap := a.focusedAppContextForModelRead(a.captureFocusedAppContext(bundleID, quickChatCaptureBudget))
	if snap == nil {
		return defaultQuickChatQuestionsForApp(bundleID, normLang), nil
	}
	contextFallback := contextualQuickChatQuestionsFromSnapshot(snap, normLang)

	context := formatFocusedAppContextForPrompt(snap, a.focusedAppOCRLookup())
	if strings.TrimSpace(context) == "" {
		return quickChatFallbackQuestions(bundleID, normLang, contextFallback), nil
	}

	prompt := redactActivityModelText(buildFocusedAppQuestionsPrompt(snap.AppName, context, normLang))
	contextStillReadable := func() bool {
		current := a.focusedAppContextForModelRead(snap)
		return current != nil && formatFocusedAppContextForPrompt(current, a.focusedAppOCRLookup()) == context
	}
	if !contextStillReadable() {
		return defaultQuickChatQuestionsForApp(bundleID, normLang), nil
	}
	givenCtx, given := passScopeFresh(passQuestions)
	resp, err := a.runEphemeralClaudeModelContext(givenCtx, prompt, a.lightTaskModel(), quickChatSuggestionModelBudget)
	a.finishAssistantCall(given)
	if !contextStillReadable() {
		return defaultQuickChatQuestionsForApp(bundleID, normLang), nil
	}
	if err != nil {
		log.Printf("[quickChat] Haiku call failed for %s: %v", bundleID, err)
		return quickChatFallbackQuestions(bundleID, normLang, contextFallback), nil
	}

	questions := parseRecommendedQuestionsResponse(resp)
	if len(questions) == 0 {
		return quickChatFallbackQuestions(bundleID, normLang, contextFallback), nil
	}
	if len(questions) > 5 {
		questions = questions[:5]
	}
	return questions, nil
}

// GetRecommendedQuestions returns 3-5 personalized question suggestions
// based on the user's recent activity and persistent memory. Falls back
// to a static set of generic prompts when no activity is recorded yet
// or when the LLM call fails.
//
// `lang` is the user's UI language tag (e.g. "en" or "zh-CN"); both the
// LLM-generated chips and the static fallbacks are returned in that
// language. The cache is keyed by language so switching languages is
// not blocked by the TTL of the previously-cached locale.
//
// Caches results for recommendedQuestionsTTL to avoid per-mount LLM
// churn. Pass forceRefresh=true to bypass the cache.
func (a *App) GetRecommendedQuestions(forceRefresh bool, lang string) ([]RecommendedQuestion, error) {
	normLang := normalizeSuggestionLang(lang)
	// Re-read provenance even on a cache hit: suggested questions can reveal
	// the same source text as the narrative that generated them.
	context := a.quickChatGeneralContext()
	contextHash := sha256.Sum256([]byte(context))

	recommendedQuestionsStore.mu.Lock()
	entry, ok := recommendedQuestionsStore.byLang[normLang]
	if !forceRefresh && ok && entry.contextHash == contextHash && len(entry.questions) > 0 &&
		time.Since(entry.generated) < recommendedQuestionsTTL {
		out := append([]RecommendedQuestion(nil), entry.questions...)
		recommendedQuestionsStore.mu.Unlock()
		return out, nil
	}
	recommendedQuestionsStore.mu.Unlock()

	questions := a.generateRecommendedQuestionsFromContext(normLang, context)
	if a.quickChatGeneralContext() != context {
		return defaultRecommendedQuestions(normLang), nil
	}
	if len(questions) == 0 {
		questions = defaultRecommendedQuestions(normLang)
	}

	recommendedQuestionsStore.mu.Lock()
	recommendedQuestionsStore.byLang[normLang] = &recommendedQuestionsCacheEntry{
		questions:   questions,
		generated:   time.Now(),
		contextHash: contextHash,
	}
	recommendedQuestionsStore.mu.Unlock()

	return questions, nil
}

func (a *App) generateRecommendedQuestionsFromContext(lang, context string) []RecommendedQuestion {
	if strings.TrimSpace(context) == "" {
		return nil
	}

	prompt := redactActivityModelText(buildRecommendedQuestionsPrompt(context, lang))
	givenCtx, given := passScopeFresh(passQuestions)
	resp, err := a.runEphemeralClaudeModelContext(givenCtx, prompt, a.lightTaskModel(), 30*time.Second)
	a.finishAssistantCall(given)
	if err != nil {
		log.Printf("[recommended-questions] Haiku call failed: %v", err)
		return nil
	}

	questions := parseRecommendedQuestionsResponse(resp)
	// Cap to 5; anything more crowds the composer.
	if len(questions) > 5 {
		questions = questions[:5]
	}
	return questions
}

// quickChatGeneralContext returns the broadest, lowest-priority context tier:
// cached daily activity summaries. Quick Chat includes this only after current
// app content and same-app history, while the normal composer uses the same
// material as its primary suggestion source.
func (a *App) quickChatGeneralContext() string {
	if a == nil || a.IsClosedEyesModeEnabled() {
		return ""
	}

	// Collect a lightweight context: today's summary plus yesterday's if present.
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02")

	context := strings.Builder{}
	addedAny := false

	if a.activity != nil && a.activity.store != nil {
		if s, _, _, _, ok := a.cachedActivitySummaryForRead(today, "daily", ""); ok && strings.TrimSpace(s) != "" {
			context.WriteString("## Today (" + today + ")\n")
			context.WriteString(s)
			context.WriteString("\n\n")
			addedAny = true
		}
		if s, _, _, _, ok := a.cachedActivitySummaryForRead(yesterday, "daily", ""); ok && strings.TrimSpace(s) != "" {
			context.WriteString("## Yesterday (" + yesterday + ")\n")
			context.WriteString(s)
			context.WriteString("\n\n")
			addedAny = true
		}
	}

	if !addedAny {
		return ""
	}
	return redactActivityModelText(strings.TrimSpace(context.String()))
}

// defaultRecommendedQuestions returns a generic fallback set used when the
// user has no activity recorded yet. Keeps the empty composer welcoming.
// The returned chips are written in the user's UI language so the empty
// state matches the rest of the app.
func defaultRecommendedQuestions(lang string) []RecommendedQuestion {
	switch lang {
	case "zh-CN":
		return []RecommendedQuestion{
			{Title: "我昨天做了什么？", Prompt: "我昨天做了什么？请按主题分组给我一段简要回顾。"},
			{Title: "本周工作回顾", Prompt: "请按项目或主题总结我本周的工作内容。"},
			{Title: "起草一份状态更新", Prompt: "根据我最近的活动起草一份简短的状态更新。"},
		}
	default:
		return []RecommendedQuestion{
			{Title: "What did I do yesterday?", Prompt: "What did I do yesterday? Give me a narrative recap grouped by topic."},
			{Title: "Summarize this week", Prompt: "Summarize what I worked on this week, organized by project or theme."},
			{Title: "Draft a status update", Prompt: "Draft a brief status update based on my recent activity."},
		}
	}
}

// defaultQuickChatQuestionsForApp is the last-resort bundle-specific fallback.
// It stays app-aware but content-agnostic: no stale people/projects, and no
// claims about current-window details that may not have been captured.
func defaultQuickChatQuestionsForApp(bundleID string, lang string) []RecommendedQuestion {
	appName := bundleIDPrettyName(strings.TrimSpace(bundleID))
	if strings.TrimSpace(appName) == "" {
		return defaultRecommendedQuestions(lang)
	}
	if isConversationAppFallback(bundleID, appName) {
		return defaultConversationQuickChatQuestions(appName, lang)
	}
	return defaultApplicationQuickChatQuestions(appName, lang)
}

func quickChatFallbackQuestions(bundleID string, lang string, contextual []RecommendedQuestion) []RecommendedQuestion {
	if len(contextual) > 0 {
		if len(contextual) > 5 {
			return contextual[:5]
		}
		return contextual
	}
	return defaultQuickChatQuestionsForApp(bundleID, lang)
}

func contextualQuickChatQuestionsFromSnapshot(snap *focusedAppSnapshot, lang string) []RecommendedQuestion {
	if snap == nil {
		return nil
	}
	facts := quickChatFallbackFactsFromSnapshot(snap)
	if isTerminalAppFallback(facts.bundleID, facts.appName, facts.contentType) {
		return contextualTerminalQuickChatQuestions(facts, lang)
	}
	if facts.conversation {
		return contextualConversationQuickChatQuestions(facts, lang)
	}
	if facts.subject != "" || facts.topic != "" {
		return contextualApplicationQuickChatQuestions(facts, lang)
	}
	return nil
}

type quickChatFallbackFacts struct {
	appName      string
	bundleID     string
	contentType  string
	subject      string
	topic        string
	latestSender string
	conversation bool
}

func quickChatFallbackFactsFromSnapshot(snap *focusedAppSnapshot) quickChatFallbackFacts {
	facts := quickChatFallbackFacts{
		appName:  snap.AppName,
		bundleID: snap.BundleID,
		subject:  cleanQuickChatSubject(snap.WindowTitle),
	}
	if facts.appName == "" {
		facts.appName = bundleIDPrettyName(snap.BundleID)
	}
	if facts.appName == "" {
		facts.appName = "app"
	}
	if snap.Current != nil {
		pc, hasParsedSignal := parsedContentForPrompt(snap.Current, snap.Recent)
		if hasParsedSignal {
			facts.contentType = pc.ContentType
			if pc.Channel != "" {
				facts.subject = cleanQuickChatSubject(pc.Channel)
			} else if pc.Title != "" && facts.subject == "" {
				facts.subject = cleanQuickChatSubject(pc.Title)
			}
			facts.conversation = pc.ContentType == "conversation" || len(pc.Messages) > 0
			for i := len(pc.Messages) - 1; i >= 0; i-- {
				body := strings.TrimSpace(pc.Messages[i].Content)
				if body == "" {
					continue
				}
				facts.latestSender = cleanQuickChatSubject(pc.Messages[i].Sender)
				facts.topic = quickChatFallbackTopic(body)
				break
			}
			if facts.topic == "" {
				facts.topic = quickChatFallbackTopic(pc.Body)
			}
		}
		if facts.topic == "" {
			facts.topic = quickChatFallbackTopic(snap.Current.AXText)
		}
	}
	if facts.subject == "" {
		facts.subject = facts.appName
	}
	if !facts.conversation {
		facts.conversation = isConversationAppFallback(snap.BundleID, facts.appName)
	}
	return facts
}

func contextualConversationQuickChatQuestions(facts quickChatFallbackFacts, lang string) []RecommendedQuestion {
	subject := facts.subject
	if subject == "" {
		subject = facts.appName
	}
	replyTarget := subject
	if facts.latestSender != "" && normalizeSuggestionAppToken(facts.latestSender) != normalizeSuggestionAppToken(subject) {
		replyTarget = facts.latestSender
	}
	topic := facts.topic
	hasSpecificTopic := topic != "" && normalizeSuggestionAppToken(topic) != normalizeSuggestionAppToken(subject)
	replyPrompt := "Draft a reply to " + replyTarget + " based on this conversation."
	if hasSpecificTopic {
		replyPrompt = "Draft a reply to " + replyTarget + " about " + topic + "."
	}
	switch lang {
	case "zh-CN":
		replyPrompt = "帮我回复 " + replyTarget + "。"
		if hasSpecificTopic {
			replyPrompt = "帮我回复 " + replyTarget + "，重点回应：" + topic
		}
		return []RecommendedQuestion{
			{Title: "回复 " + replyTarget, Prompt: replyPrompt},
			{Title: "总结这个对话", Prompt: "总结和 " + subject + " 的对话，并指出下一步。"},
			{Title: "找出跟进事项", Prompt: "从这个对话里找出我应该跟进的事项。"},
			{Title: "解释关键上下文", Prompt: "解释这个对话里最重要的上下文。"},
			{Title: "改写我的回复", Prompt: "把我给 " + replyTarget + " 的回复改写得更自然清楚。"},
		}
	default:
		return []RecommendedQuestion{
			{Title: "Reply to " + replyTarget, Prompt: replyPrompt},
			{Title: "Summarize " + subject + " thread", Prompt: "Summarize this conversation with " + subject + " and the next step."},
			{Title: "Find follow-ups", Prompt: "List the follow-ups from this conversation with " + subject + "."},
			{Title: "Explain the context", Prompt: "Explain the important context in this " + facts.appName + " conversation."},
			{Title: "Rewrite my response", Prompt: "Rewrite my response to " + replyTarget + " to be clear and natural."},
		}
	}
}

func contextualTerminalQuickChatQuestions(facts quickChatFallbackFacts, lang string) []RecommendedQuestion {
	appName := facts.appName
	if appName == "" {
		appName = "terminal"
	}
	switch terminalContextKind(facts.topic) {
	case "diff":
		if lang == "zh-CN" {
			return []RecommendedQuestion{
				{Title: "检查当前 diff", Prompt: "检查 " + appName + " 里可见的 diff 和测试输出，指出提交前的风险。"},
				{Title: "总结终端状态", Prompt: "总结 " + appName + " 里刚刚修改、格式化和测试的内容。"},
				{Title: "找出下一条命令", Prompt: "根据当前终端输出，我下一步应该运行什么命令？"},
				{Title: "解释检查结果", Prompt: "解释当前测试或 diff 检查结果，以及还有什么风险。"},
				{Title: "起草变更总结", Prompt: "根据这个终端上下文起草一段简洁的变更总结。"},
			}
		}
		return []RecommendedQuestion{
			{Title: "Review current diff", Prompt: "Review the visible diff and test output in " + appName + "; flag risks before commit."},
			{Title: "Summarize terminal state", Prompt: "Summarize the edits, formatting, and tests visible in " + appName + "."},
			{Title: "Find the next command", Prompt: "Based on this terminal output, what command should I run next?"},
			{Title: "Explain check results", Prompt: "Explain the visible test or diff-check result and any remaining risk."},
			{Title: "Draft change summary", Prompt: "Draft a concise implementation summary from this terminal context."},
		}
	case "test":
		if lang == "zh-CN" {
			return []RecommendedQuestion{
				{Title: "解释测试结果", Prompt: "解释 " + appName + " 里可见的测试结果和需要注意的风险。"},
				{Title: "找出下一步", Prompt: "根据当前测试输出，我下一步应该做什么？"},
				{Title: "总结终端状态", Prompt: "总结最近运行的命令和结果。"},
				{Title: "检查失败原因", Prompt: "如果这里有失败，帮我定位最可能的原因。"},
				{Title: "准备提交说明", Prompt: "根据当前终端输出起草一段提交或 PR 测试说明。"},
			}
		}
		return []RecommendedQuestion{
			{Title: "Explain test results", Prompt: "Explain the visible test output in " + appName + " and any remaining risk."},
			{Title: "Find the next step", Prompt: "Based on this test output, what should I do next?"},
			{Title: "Summarize recent commands", Prompt: "Summarize the recent commands and their results."},
			{Title: "Debug any failure", Prompt: "If anything failed here, identify the most likely cause."},
			{Title: "Prepare test notes", Prompt: "Draft a concise test note from the current terminal output."},
		}
	default:
		if lang == "zh-CN" {
			return []RecommendedQuestion{
				{Title: "总结终端输出", Prompt: "总结 " + appName + " 当前可见输出里最重要的内容。"},
				{Title: "找出下一条命令", Prompt: "根据当前终端状态，我下一步应该运行什么命令？"},
				{Title: "解释当前状态", Prompt: "解释这个终端窗口当前在做什么，以及有什么需要注意。"},
				{Title: "检查潜在问题", Prompt: "检查当前终端输出里是否有错误、风险或遗漏步骤。"},
				{Title: "起草工作总结", Prompt: "根据这个终端上下文起草一段简洁的工作总结。"},
			}
		}
		return []RecommendedQuestion{
			{Title: "Summarize terminal output", Prompt: "Summarize the important visible output in " + appName + "."},
			{Title: "Find the next command", Prompt: "Based on this terminal state, what command should I run next?"},
			{Title: "Explain current state", Prompt: "Explain what this terminal window is doing and what matters."},
			{Title: "Check for problems", Prompt: "Check this terminal output for errors, risks, or missed steps."},
			{Title: "Draft work summary", Prompt: "Draft a concise work summary from this terminal context."},
		}
	}
}

func contextualApplicationQuickChatQuestions(facts quickChatFallbackFacts, lang string) []RecommendedQuestion {
	subject := facts.subject
	if subject == "" {
		subject = facts.appName
	}
	topic := facts.topic
	if topic == "" {
		topic = subject
	}
	switch lang {
	case "zh-CN":
		return []RecommendedQuestion{
			{Title: "总结 " + subject, Prompt: "总结 " + subject + " 里最重要的内容。"},
			{Title: "找出下一步", Prompt: "根据 " + subject + "，我下一步应该做什么？"},
			{Title: "解释当前上下文", Prompt: "解释 " + subject + " 中关于 " + topic + " 的上下文。"},
			{Title: "列出开放问题", Prompt: "列出 " + subject + " 里需要我处理的问题。"},
			{Title: "起草下一步", Prompt: "根据 " + subject + " 起草一个清晰的下一步。"},
		}
	default:
		return []RecommendedQuestion{
			{Title: "Summarize " + subject, Prompt: "Summarize what matters in " + subject + "."},
			{Title: "Find the next action", Prompt: "What should I do next based on " + subject + "?"},
			{Title: "Explain this context", Prompt: "Explain the context around " + topic + "."},
			{Title: "Spot open questions", Prompt: "What questions or blockers should I notice in " + subject + "?"},
			{Title: "Draft a next step", Prompt: "Draft a clear next step based on " + subject + "."},
		}
	}
}

func isConversationAppFallback(bundleID, appName string) bool {
	token := normalizeSuggestionAppToken(bundleID + " " + appName)
	for _, needle := range []string{
		"messages", "mobilesms", "slack", "discord", "teams", "chatgpt",
		"mail", "outlook", "gmail", "superhuman",
	} {
		if strings.Contains(token, needle) {
			return true
		}
	}
	return false
}

func isTerminalAppFallback(bundleID, appName, contentType string) bool {
	if contentType == "terminal" {
		return true
	}
	token := normalizeSuggestionAppToken(bundleID + " " + appName)
	for _, needle := range []string{
		"terminal", "iterm", "ghostty", "warp", "wezterm", "kitty", "cmux",
	} {
		if strings.Contains(token, needle) {
			return true
		}
	}
	return false
}

func terminalContextKind(topic string) string {
	lower := strings.ToLower(topic)
	switch {
	case strings.Contains(lower, "git diff") ||
		strings.Contains(lower, "edited ") ||
		strings.Contains(lower, "diff --check") ||
		strings.Contains(lower, "+++ ") ||
		strings.Contains(lower, "--- "):
		return "diff"
	case strings.Contains(lower, "go test") ||
		strings.Contains(lower, "ok ") ||
		strings.Contains(lower, "fail") ||
		strings.Contains(lower, "error"):
		return "test"
	default:
		return "terminal"
	}
}

func cleanQuickChatSubject(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, " - "); i > 0 {
		s = s[:i]
	}
	for {
		start := strings.LastIndex(s, " (")
		if start < 0 || !strings.HasSuffix(s, ")") {
			break
		}
		s = strings.TrimSpace(s[:start])
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return truncateForFocusedAppPrompt(s, 48)
}

func quickChatFallbackTopic(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if s == "" {
		return ""
	}
	return truncateForFocusedAppPrompt(s, 90)
}

func normalizeSuggestionAppToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func defaultConversationQuickChatQuestions(appName string, lang string) []RecommendedQuestion {
	switch lang {
	case "zh-CN":
		return []RecommendedQuestion{
			{Title: "总结当前对话", Prompt: "总结这个对话，并指出下一步。"},
			{Title: "起草回复", Prompt: "帮我为这个对话起草一条简洁回复。"},
			{Title: "找出跟进事项", Prompt: "这个对话里我应该跟进什么？"},
			{Title: "解释关键上下文", Prompt: "解释这个对话里最重要的上下文。"},
			{Title: "改写我的回复", Prompt: "把我的回复改写得更自然清楚。"},
		}
	default:
		return []RecommendedQuestion{
			{Title: "Summarize this conversation", Prompt: "Summarize this conversation and highlight the next step."},
			{Title: "Draft a reply", Prompt: "Draft a concise reply for this conversation."},
			{Title: "Find follow-ups", Prompt: "What should I follow up on in this conversation?"},
			{Title: "Explain the context", Prompt: "Explain the important context in this conversation."},
			{Title: "Rewrite my message", Prompt: "Rewrite my response to be clear and natural."},
		}
	}
}

func defaultApplicationQuickChatQuestions(appName string, lang string) []RecommendedQuestion {
	switch lang {
	case "zh-CN":
		return []RecommendedQuestion{
			{Title: "总结当前窗口", Prompt: "总结 " + appName + " 当前窗口里最重要的内容。"},
			{Title: "找出下一步", Prompt: "根据 " + appName + "，我下一步应该做什么？"},
			{Title: "解释当前上下文", Prompt: "解释 " + appName + " 里值得注意的上下文。"},
			{Title: "列出开放问题", Prompt: "列出 " + appName + " 里可能需要我处理的问题。"},
			{Title: "起草下一步", Prompt: "根据 " + appName + " 起草一个清晰的下一步。"},
		}
	default:
		return []RecommendedQuestion{
			{Title: "Summarize current window", Prompt: "Summarize what is visible in " + appName + " and what matters."},
			{Title: "Find the next action", Prompt: "What should I do next in " + appName + "?"},
			{Title: "Explain this context", Prompt: "Explain the important context in " + appName + "."},
			{Title: "Spot open questions", Prompt: "What questions or blockers should I notice in " + appName + "?"},
			{Title: "Draft a next step", Prompt: "Draft a clear next step based on " + appName + "."},
		}
	}
}

const recommendedQuestionsPromptTemplate = `You are helping a user decide what to ask their AI assistant next.

Respond in %s (the user's UI language). Every "title" and every "prompt" field MUST be written in %s.

Based on the user's recent activity below, generate 3-5 short, personalized
question suggestions they might want to ask right now. Each should:

- Reference SPECIFIC people, projects, or decisions you see in the activity
  (not generic "summarize my work" prompts).
- Be phrased as if the user is asking the assistant (first person).
- Be actionable: recap, draft, follow-up, analyze, etc.
- Stay under ~12 words for the question body.

Return a JSON array (no prose, no code fence):

[
  {"title": "<4-8 word label for pill>", "prompt": "<full question>"},
  ...
]

Examples of GOOD suggestions (reference specifics):
%s

Examples of BAD suggestions (too generic):
%s

User's recent activity:

%s

Output ONLY the JSON array. No other text.`

func buildRecommendedQuestionsPrompt(activityContext string, lang string) string {
	const maxContextBytes = 8 * 1024
	if len(activityContext) > maxContextBytes {
		activityContext = activityContext[:maxContextBytes] + "\n... (truncated)"
	}
	langName := languageDisplayName(lang)
	ex := examplesForLang(lang)
	return fmt.Sprintf(recommendedQuestionsPromptTemplate, langName, langName, ex.good, ex.bad, activityContext)
}

const focusedAppQuestionsPromptTemplate = `You are helping a user decide what to ask their AI assistant from a quick-chat panel.

Respond in %s (the user's UI language). Every "title" and every "prompt" field MUST be written in %s.

The user just invoked a quick-chat panel over %s. Generate 5 short, specific
question suggestions they might want to ask RIGHT NOW.

Priority order for grounding chips:
1. "Current application" is the strongest evidence. If it includes current
   window content, at least 3 of your 5 chips MUST reference concrete details
   from it (a person, channel, message, row, document section, URL, error text,
   etc.).
2. "Past context from the same application" is lower priority. Use it for at
   most 1-2 continuity chips (follow up on a thread, revisit an open task, etc.).
3. "General context" is the weakest evidence. Use it only when the first two
   tiers do not provide enough grounded chips, and never let it outrank or
   replace the current-application evidence.

Do NOT produce generic prompts like "Summarize my day" or "What did I work on?"
If you cannot ground a chip in the available context tiers, drop it rather than
padding with generic content.

Each chip:
- Phrased as the user asking the assistant (first person).
- Actionable: draft, summarize, follow-up, analyze, decide, etc.
- Title: 4-8 words. Prompt: under ~16 words.

Return ONLY a JSON array (no prose, no code fence):

[
  {"title": "<4-8 word label>", "prompt": "<full question>"},
  ...
]

Context:

%s

Output ONLY the JSON array.`

// buildFocusedAppQuestionsPrompt builds the Haiku prompt for the quick-chat
// per-app suggestion path. Encodes the tier-priority rules so the model
// can't fall back to generic "summarize my day" chips when on-screen content
// is available.
func buildFocusedAppQuestionsPrompt(appName, focusedContext string, lang string) string {
	const maxContextBytes = 8 * 1024
	if len(focusedContext) > maxContextBytes {
		focusedContext = focusedContext[:maxContextBytes] + "\n... (truncated)"
	}
	if appName == "" {
		appName = "the focused app"
	}
	langName := languageDisplayName(lang)
	return fmt.Sprintf(focusedAppQuestionsPromptTemplate, langName, langName, appName, focusedContext)
}

// parseRecommendedQuestionsResponse extracts the JSON array from the model
// response. Tolerates surrounding prose, code fences, and trailing markers.
func parseRecommendedQuestionsResponse(resp string) []RecommendedQuestion {
	resp = strings.TrimSpace(resp)
	if resp == "" {
		return nil
	}
	// Strip common code-fence wrappers.
	resp = strings.TrimPrefix(resp, "```json")
	resp = strings.TrimPrefix(resp, "```")
	resp = strings.TrimSuffix(resp, "```")
	resp = strings.TrimSpace(resp)

	// Find the first '[' and last ']' to extract the JSON array.
	start := strings.Index(resp, "[")
	end := strings.LastIndex(resp, "]")
	if start < 0 || end <= start {
		return nil
	}

	var out []RecommendedQuestion
	if err := json.Unmarshal([]byte(resp[start:end+1]), &out); err != nil {
		log.Printf("[recommended-questions] parse failed: %v", err)
		return nil
	}

	// Drop entries missing required fields.
	cleaned := out[:0]
	for _, q := range out {
		title := strings.TrimSpace(q.Title)
		prompt := strings.TrimSpace(q.Prompt)
		if title == "" || prompt == "" {
			continue
		}
		cleaned = append(cleaned, RecommendedQuestion{Title: title, Prompt: prompt})
	}
	return cleaned
}
