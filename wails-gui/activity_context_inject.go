package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/pii"
)

// scrubSensitiveAppNames removes user-protected app display names from a
// session's app_names JSON array. Fails CLOSED: malformed input yields an
// empty list rather than passing the unparseable (and unfilterable) raw
// value through to a prompt.
func scrubSensitiveAppNames(appNamesJSON string) string {
	var names []string
	if err := json.Unmarshal([]byte(appNamesJSON), &names); err != nil {
		return "[]"
	}
	out, err := json.Marshal(pii.FilterSensitiveAppNames(names))
	if err != nil {
		return "[]"
	}
	return string(out)
}

// Recent-activity context injection for chat sessions.
//
// When the user opens a new chat, we prepend a compact 2–3 sentence summary
// of what they've been doing on the computer recently so the assistant can
// ground its answers without the user having to re-explain context. A few
// constraints shape the design:
//
//   1. Keep the injected text stable across turns and across sessions that
//      open close together — each change blows out the prompt cache.
//      We cache the summary globally with a 30-minute TTL, and only inject
//      it on the *first* turn of each session (`injectedTags` tracks this).
//   2. Inject a distilled summary, not a verbatim log of sessions/OCR/etc.
//      A long raw dump would balloon context and starve real work of
//      tokens. We hand raw sessions to Haiku and ask for ≤80 words.
//   3. Fail quiet. On any error (no activity monitor, model failure, empty
//      activity) we emit no context at all — better to skip injection than
//      to prepend a broken or empty block.

const (
	// How long a generated summary stays fresh before the next request
	// regenerates. Tuned high because the prompt cache TTL is only a few
	// minutes; keeping the injected text stable for far longer lets
	// back-to-back sessions reuse the same cached prefix.
	activityContextCacheTTL = 30 * time.Minute

	// How far back "recent" extends. Two days covers the common "I worked
	// on this yesterday, picking it back up today" pattern without going
	// so wide that older work drowns out what's actually top-of-mind.
	activityContextLookbackHours = 48

	// Cap on sessions passed to the summarizer so the Haiku prompt stays
	// bounded even on very active days. Scales with the lookback window.
	activityContextMaxSessions = 100

	// Upper bound on the cache of "tag -> already injected" entries. Well
	// above any realistic active-sessions count; prevents unbounded growth
	// across a long-running app process.
	activityContextInjectedTagCap = 1000

	// Returned by generateRecentActivitySummary when there are no sessions
	// in the lookback window. Lets warmup distinguish "no data to
	// summarize" (stop retrying) from "transient failure" (retry).
	noRecentSessionsSentinel = "__no_recent_sessions__"
)

type activityContextCache struct {
	mu           sync.Mutex
	summary      string
	generatedAt  time.Time
	privacy      activityContextPrivacyFingerprint
	generating   bool
	injectedTags map[string]struct{}
}

// Only in-memory state belongs on chat's critical path. Generations already
// validate their source sessions; this fingerprint invalidates their result
// when protection settings, the source store, or retained sources change.
type activityContextPrivacyFingerprint struct {
	store         *ActivityStore
	revision      uint64
	sensitiveApps string
	closedEyes    bool
}

func (a *App) activityContextPrivacy() (activityContextPrivacyFingerprint, bool) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return activityContextPrivacyFingerprint{}, false
	}
	fingerprint := activityContextPrivacyFingerprint{
		store:      a.activity.store,
		revision:   a.activity.store.readPrivacyRevision.Load(),
		closedEyes: a.IsClosedEyesModeEnabled(),
	}
	apps, err := json.Marshal(pii.GetUserSensitiveApps())
	if err != nil || fingerprint.closedEyes || fingerprint.revision%2 != 0 {
		return fingerprint, false
	}
	fingerprint.sensitiveApps = string(apps)
	return fingerprint, true
}

func newActivityContextCache() *activityContextCache {
	return &activityContextCache{
		injectedTags: make(map[string]struct{}),
	}
}

// getSummary returns whatever summary is currently cached without ever
// blocking the caller. If the cache is stale (or empty), it kicks off a
// background regeneration so the next request benefits — the current
// request gets what's on hand (which may be empty on first call before
// the startup warmup completes). Blocking here is a hard NO because the
// caller is on the user's chat send path and a slow Haiku call would
// stall the first turn by tens of seconds.
func (c *activityContextCache) getSummary(a *App) string {
	privacy, allowed := a.activityContextPrivacy()
	if !allowed {
		return ""
	}
	c.mu.Lock()
	if c.privacy != privacy {
		c.summary = ""
		c.generatedAt = time.Time{}
	}
	fresh := c.summary != "" && time.Since(c.generatedAt) < activityContextCacheTTL
	if fresh || c.generating {
		out := c.summary
		c.mu.Unlock()
		return redactActivityModelText(out)
	}
	c.generating = true
	current := c.summary
	c.mu.Unlock()

	go c.regenerate(a)

	return redactActivityModelText(current)
}

// regenerate runs the (slow) Haiku summarization in the background and
// updates the cache when done. Concurrent calls are serialized via the
// `generating` flag set by the caller. Returns the raw result string so
// callers like warmup can inspect it (e.g. to detect the no-sessions
// sentinel and stop retrying).
func (c *activityContextCache) regenerate(a *App) string {
	startedAt := time.Now()
	privacy, allowed := a.activityContextPrivacy()
	if !allowed {
		c.mu.Lock()
		c.generating = false
		c.mu.Unlock()
		return ""
	}
	summary := a.generateRecentActivitySummary()
	currentPrivacy, stillAllowed := a.activityContextPrivacy()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.generating = false
	if !stillAllowed || privacy != currentPrivacy {
		c.summary = ""
		c.generatedAt = time.Time{}
		return ""
	}
	if summary != "" && summary != noRecentSessionsSentinel {
		c.summary = summary
		c.generatedAt = startedAt
		c.privacy = privacy
	}
	return summary
}

// warmupActivityContext kicks off a background summary generation shortly
// after the agent probe succeeds so the first user chat session of the run
// already has a cached summary to inject. Without this, the very first
// chat's turn-1 either gets no context (empty cache) or waits for Haiku
// to finish (too slow) — neither is acceptable.
func (a *App) warmupActivityContext() {
	if a.activityContextCache == nil {
		return
	}
	go func() {
		// Do NOT gate on the credential probe: probeAndGreet returns early
		// on the Yansu gateway path, which loadConfig forces for every
		// install, even though Haiku calls work fine.
		//
		// Instead: wait a short grace period for the agent to spin up,
		// then just try to generate. If the call fails (runner still
		// cold, auth hiccup, transient network issue), retry with a
		// moderate backoff.
		const startupGrace = 10 * time.Second
		const maxAttempts = 10
		const retryBackoff = 30 * time.Second

		time.Sleep(startupGrace)

		for attempt := 1; attempt <= maxAttempts; attempt++ {
			// If another code path (e.g. a user chat send) already warmed
			// the cache, we're done.
			c := a.activityContextCache
			c.mu.Lock()
			if c.summary != "" {
				c.mu.Unlock()
				log.Printf("[activity-context] warmup: cache already populated, attempt=%d", attempt)
				return
			}
			if c.generating {
				c.mu.Unlock()
				time.Sleep(retryBackoff)
				continue
			}
			c.generating = true
			c.mu.Unlock()

			log.Printf("[activity-context] warmup: generating initial summary (attempt %d/%d)", attempt, maxAttempts)
			result := c.regenerate(a)

			// If there are simply no sessions in the lookback window,
			// retrying won't help — new sessions won't appear out of
			// thin air. Stop immediately instead of burning 5 minutes
			// of retries that spam the log.
			if result == noRecentSessionsSentinel {
				log.Printf("[activity-context] warmup: no recent sessions, skipping remaining attempts")
				return
			}

			c.mu.Lock()
			ok := c.summary != ""
			c.mu.Unlock()
			if ok {
				log.Printf("[activity-context] warmup: success on attempt %d", attempt)
				return
			}
			log.Printf("[activity-context] warmup: generation failed on attempt %d/%d, retrying in %v", attempt, maxAttempts, retryBackoff)
			time.Sleep(retryBackoff)
		}
		log.Printf("[activity-context] warmup: exhausted %d attempts, giving up", maxAttempts)
	}()
}

// markInjected records that the given chat tag has already received the
// activity context. Returns true the first time a tag is seen so the
// caller knows to prepend on this turn only.
func (c *activityContextCache) markInjected(tag string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, seen := c.injectedTags[tag]; seen {
		return false
	}
	if len(c.injectedTags) >= activityContextInjectedTagCap {
		// Drop an arbitrary entry to bound the map. Being imprecise here
		// is fine: the worst case is a tag gets re-injected once.
		for k := range c.injectedTags {
			delete(c.injectedTags, k)
			break
		}
	}
	c.injectedTags[tag] = struct{}{}
	return true
}

// generateRecentActivitySummary pulls recent activity (sessions + window
// titles + OCR keywords) and asks Haiku to distill it into a specific
// context note. Returns the empty string if there's nothing to summarize
// or the model fails.
//
// We feed Haiku richer data than just session metadata — window titles
// reveal concrete file/project/doc names, and OCR keywords surface the
// actual topics on screen. Without these, Haiku can only output vague
// themes like "coding and debugging" which the chat model then can't
// use for real personalization.
func (a *App) generateRecentActivitySummary() string {
	if a == nil || a.activity == nil || a.activity.store == nil {
		log.Printf("[activity-context] generate: activity monitor not initialized (a=%v activity=%v)", a != nil, a != nil && a.activity != nil)
		return ""
	}
	nowMs := time.Now().UnixMilli()
	sinceMs := nowMs - int64(activityContextLookbackHours)*3600*1000

	sessions, err := a.activity.store.modelSessionsOverlapping(sinceMs, nowMs+60_000)
	if err != nil {
		log.Printf("[activity-context] load sessions: %v", err)
		return ""
	}
	var recent []ActivitySessionRow
	for _, s := range sessions {
		if s.EndTime < sinceMs || !a.activitySessionAllowsModelRead(a.activity.store, s) {
			continue
		}
		// Recheck current protection before using historical session metadata.
		s.AppNames = scrubSensitiveAppNames(s.AppNames)
		recent = append(recent, s)
		if len(recent) == activityContextMaxSessions {
			break
		}
	}
	if len(recent) == 0 {
		log.Printf("[activity-context] generate: no sessions in last %dh", activityContextLookbackHours)
		return noRecentSessionsSentinel
	}

	// Pull specifics: window titles and OCR keywords over the same window.
	// Both helpers filter on session start time internally.
	windowTitles := a.extractTopWindowTitles(a.activity.store, sinceMs, nowMs+60_000)
	ocrKeywords := a.extractTopOCRKeywords(a.activity.store, sinceMs, nowMs+60_000)

	// User display name so the model can greet by name.
	userName := a.getUserDisplayName()
	if userName == "the user" {
		userName = ""
	}

	log.Printf("[activity-context] generate: sessions=%d titles=%d ocr=%d has_user_name=%t lookbackH=%d",
		len(recent), len(windowTitles), len(ocrKeywords), userName != "", activityContextLookbackHours)

	prompt := buildRecentActivityPrompt(recent, windowTitles, ocrKeywords, userName)
	out, err := a.callHaikuForSummary(prompt)
	if err != nil {
		log.Printf("[activity-context] haiku: %v", err)
		return ""
	}
	trimmed := strings.TrimSpace(out)
	log.Printf("[activity-context] generate: haiku ok, summary_len=%d", len(trimmed))
	return trimmed
}

func buildRecentActivityPrompt(sessions []ActivitySessionRow, windowTitles, ocrKeywords []string, userName string) string {
	var b strings.Builder
	b.WriteString("You are writing a compact context note that another AI assistant will use to greet the user warmly, like a thoughtful colleague — not to recite a status report. Write a 3rd-person factual note; do NOT greet the user yourself.\n\n")

	b.WriteString("STRUCTURE (exactly this, no lists):\n")
	b.WriteString("1. ONE sentence naming the single most notable thing the user is currently head-down on (most recent + most specific). Name a concrete project, repo, document, course, or topic — not an app category.\n")
	b.WriteString("2. ONE short sentence adding just enough context to anchor the greeting — e.g. time-of-day vibe (\"just came out of back-to-back Zoom calls this morning\"), workplace/company, or one other concrete signal. Only include a second sentence if it genuinely helps; otherwise stop after sentence 1.\n\n")

	b.WriteString("RULES:\n")
	b.WriteString("- Pick ONE salient thing, don't enumerate. Do not list apps, channels, or tool stacks.\n")
	b.WriteString("- Prefer what's happening NOW over what happened yesterday.\n")
	b.WriteString("- If the data is ambiguous, say so briefly rather than guessing.\n")
	b.WriteString("- Under 60 words total, plain prose, no markdown.\n\n")

	// Time context so the model can do "Monday evening" style references.
	now := time.Now()
	fmt.Fprintf(&b, "Current time: %s\n", now.Format("Monday 2006-01-02 15:04 MST"))
	if userName != "" {
		fmt.Fprintf(&b, "User display name: %s\n", userName)
	}
	fmt.Fprintf(&b, "Lookback window: last ~%d hours\n\n", activityContextLookbackHours)

	if len(windowTitles) > 0 {
		b.WriteString("Most frequent window titles (what was on screen):\n")
		for _, t := range windowTitles {
			fmt.Fprintf(&b, "- %s\n", t)
		}
		b.WriteString("\n")
	}

	if len(ocrKeywords) > 0 {
		fmt.Fprintf(&b, "Top terms from on-screen text (OCR): %s\n\n", strings.Join(ocrKeywords, ", "))
	}

	b.WriteString("Session timeline (most recent first, apps/meeting hint only):\n")
	for _, s := range sessions {
		startStr := time.UnixMilli(s.StartTime).Format("Mon 15:04")
		durMin := (s.EndTime - s.StartTime) / 60000
		fmt.Fprintf(&b, "- %s (%dm) apps=%s", startStr, durMin, s.AppNames)
		if s.IsMeeting {
			fmt.Fprintf(&b, " [meeting: %s]", s.MeetingApp)
		}
		b.WriteString("\n")
	}
	return redactActivityModelText(b.String())
}

// injectRecentActivityContext prepends a once-per-session recent-activity
// summary so the assistant can ground its responses in what the user has
// been doing. Returns the prompt unchanged on all subsequent turns of the
// same session, and on any failure path, to keep the prompt cache warm.
func (a *App) injectRecentActivityContext(prompt string, tag string) string {
	if tag == "" {
		log.Printf("[activity-context] skip: empty tag")
		return prompt
	}
	if a.activityContextCache == nil {
		log.Printf("[activity-context] skip: cache not initialized tag=%s", tag)
		return prompt
	}
	if !a.activityContextCache.markInjected(tag) {
		// Expected on every turn after the first of a session; intentionally silent.
		return prompt
	}
	summary := a.activityContextCache.getSummary(a)
	if summary == "" {
		log.Printf("[activity-context] skip: empty summary tag=%s", tag)
		return prompt
	}
	log.Printf("[activity-context] INJECTED tag=%s summary_len=%d", tag, len(summary))
	return "<recent-activity-context>\n" + recentActivityOpenerRules +
		summary + "\n" +
		"</recent-activity-context>\n\n" + prompt
}

// Shared by native injection and the on-demand external activity reader.
const recentActivityOpenerRules = "Below is a short factual note on what the user has been doing recently. " +
	"Use it to open warmly, like a thoughtful colleague who's been paying attention — NOT like a status report.\n\n" +
	"When the user sends a greeting or open-ended opener (\"hi\", \"hello\", \"what's up\", \"good morning\", \"help me\"):\n" +
	"- Greet them by name (if available) with a time-appropriate feel (morning / afternoon / evening / late night).\n" +
	"- Anchor your reply to ONE salient thing from the note — the most current, specific, or emotionally charged detail. Do NOT list or enumerate recent work.\n" +
	"- Where plausible, acknowledge how they might be feeling (\"that's a lot of meetings\", \"still wrestling with X?\", \"finally shipped Y?\"). Short, not saccharine.\n" +
	"- End with ONE inviting, specific question — never a bare \"How can I help?\".\n\n" +
	"For targeted questions, treat this as background context only.\n\n" +
	"Do not quote this block or say things like \"according to the context\". Do not invent details beyond what the note states.\n\n"
