package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/activitymon/appparser"
)

// focusedAppSnapshot holds the multi-tier context the quick-chat path mines
// from the focused app: a fresh on-screen capture (tier 1), recent prior
// activity in the same app (tier 2), and the timestamps that let the consumer
// decide how stale each piece is.
type focusedAppSnapshot struct {
	BundleID     string
	AppName      string                 // display name resolved via bundleIDPrettyName
	WindowTitle  string                 // tier 1 — current window
	Current      *ActivitySnapshotRow   // tier 1 — most recent capture (ideally just-fired)
	CurrentFresh bool                   // true if Current came back from a freshly-fired capture
	Recent       []*ActivitySnapshotRow // tier 2 — prior captures from same app, newest first
	General      string                 // tier 3 — broader recent-work context
	CapturedAt   time.Time
}

// captureFocusedAppContext gathers structured context about the user's
// focused app for the quick-chat panel. Current-window context is collected
// directly from the requested bundle ID so opening Quick Chat cannot race the
// frontmost-app capture path and substitute Yansu or another latest snapshot.
// Recent prior snapshots are filtered to the same parsed app before broader
// recent-work context is appended as the lowest-priority tier.
//
// Returns nil if no usable current, same-app, or general signal could be
// gathered. Never returns an error; callers can render an empty suggestion set.
func (a *App) captureFocusedAppContext(bundleID string, _ time.Duration) *focusedAppSnapshot {
	// With "Watch my screen" off nothing on screen is read (computer-chat
	// v1.0.0 rule 8).
	if a == nil || a.IsClosedEyesModeEnabled() || !a.screenWatchingEnabled() {
		return nil
	}
	bundleID = strings.TrimSpace(bundleID)
	if bundleID == "" {
		return nil
	}
	// Never capture context from a protected/sensitive app: no AX read, no
	// recent-snapshot lookup, no window title. Quick Chat opens with no
	// app context rather than leaking vault items / diary text into the
	// suggestion and conversation prompts.
	appName := bundleIDPrettyName(bundleID)
	if activitySourceAppIsSensitive(bundleID, appName) {
		return nil
	}

	current := captureCurrentAppSnapshotByBundle(bundleID)
	fresh := current != nil

	// Tier 2 — recent prior activity in this app.
	var recent []*ActivitySnapshotRow
	if a.activity != nil && a.activity.store != nil && appName != "" {
		since := time.Now().Add(-24 * time.Hour).UnixMilli()
		rows, err := a.activity.store.GetRecentSnapshotsForApp(appName, since, 15)
		if err == nil {
			recent = filterRecentSnapshotsForFocusedApp(rows, current, appName, 5)
		}
	}

	general := a.quickChatGeneralContext()
	if current == nil && len(recent) == 0 && strings.TrimSpace(general) == "" {
		return nil
	}

	windowTitle := ""
	if current != nil {
		windowTitle = extractWindowTitle(current)
	}

	return a.focusedAppContextForModelRead(&focusedAppSnapshot{
		BundleID:     bundleID,
		AppName:      appName,
		WindowTitle:  windowTitle,
		Current:      current,
		CurrentFresh: fresh,
		Recent:       recent,
		General:      general,
		CapturedAt:   time.Now(),
	})
}

// Recheck captured and stashed context against today's privacy settings at
// every model boundary. A rejected stored source is not usable as a dedup
// sibling, OCR lookup source, title, or local suggestion fallback either.
func (a *App) focusedAppContextForModelRead(snap *focusedAppSnapshot) *focusedAppSnapshot {
	if a == nil || snap == nil || a.IsClosedEyesModeEnabled() || !a.screenWatchingEnabled() ||
		activitySourceAppIsSensitive(snap.BundleID, snap.AppName) || activitymon.IsPrivateBrowsing(snap.WindowTitle) {
		return nil
	}
	clean := *snap
	clean.Recent = nil
	if clean.Current != nil && !a.focusedSnapshotAllowsModelRead(*clean.Current) {
		clean.Current = nil
		clean.WindowTitle = ""
		clean.CurrentFresh = false
	}
	for _, row := range snap.Recent {
		if row != nil && a.focusedSnapshotAllowsModelRead(*row) {
			clean.Recent = append(clean.Recent, row)
		}
	}
	clean.General = a.quickChatGeneralContext()
	if clean.Current == nil && len(clean.Recent) == 0 && clean.General == "" {
		return nil
	}
	return &clean
}

func (a *App) focusedSnapshotAllowsModelRead(row ActivitySnapshotRow) bool {
	if a == nil || a.IsClosedEyesModeEnabled() || activitySourceAppIsSensitive(row.App, row.App) || activitymon.IsPrivateBrowsing(extractWindowTitle(&row)) {
		return false
	}
	if strings.TrimSpace(row.ParsedJSON) != "" {
		var parsed appparser.ParsedContent
		if json.Unmarshal([]byte(row.ParsedJSON), &parsed) != nil ||
			activitySourceAppIsSensitive(parsed.Metadata["bundleId"], parsed.Metadata["app"]) ||
			activitymon.IsPrivateBrowsing(parsed.Metadata["windowTitle"]) {
			return false
		}
	}
	if row.SessionID != "" {
		if a.activity == nil || a.activity.store == nil {
			return false
		}
		session, err := a.activity.store.GetSessionByID(row.SessionID)
		return err == nil && session != nil && a.activitySessionAllowsModelRead(a.activity.store, *session)
	}
	if row.ID != 0 {
		return false
	}
	// Fresh on-demand captures have no persisted session. Their timestamp
	// still belongs to the pause history, including a pause followed by resume
	// while the Quick Chat panel or its main-chat stash was waiting.
	allowed, err := a.activitySourceProvenanceAllowsRead(activitySourceProvenance{StartTime: row.Timestamp, EndTime: row.Timestamp + 1})
	return err == nil && allowed
}

// captureCurrentAppSnapshotByBundle gathers the current window from the app the
// quick-chat caller identified before the panel appeared. The activity monitor's
// normal snapshot path is intentionally not used here because it asks the OS for
// the current frontmost app after Quick Chat may already be focused.
func captureCurrentAppSnapshotByBundle(bundleID string) *ActivitySnapshotRow {
	tree := activitymon.CollectWindowTreeByBundle(bundleID)
	if tree == nil {
		return nil
	}

	windowTitle := windowTitleFromAXTree(tree)
	if activitymon.IsPrivateBrowsing(windowTitle) {
		return nil
	}
	axText := flattenAXTreeText(tree)
	browserURL := activitymon.GetBrowserURL(bundleID)

	row := &ActivitySnapshotRow{
		Timestamp:  time.Now().UnixMilli(),
		Trigger:    "quick-chat-on-demand",
		AXText:     axText,
		BrowserURL: browserURL,
	}

	parsed := appparser.ParseTree(tree, axText, bundleID, windowTitle, browserURL)
	if parsed != nil {
		row.ContentType = parsed.ContentType
		if parsed.Metadata != nil {
			row.App = parsed.Metadata["app"]
		}
		if parsed.Body != "" {
			row.AXText = parsed.Body
		}
		if data, err := json.Marshal(parsed); err == nil {
			row.ParsedJSON = string(data)
		}
	}
	if strings.TrimSpace(row.AXText) == "" && windowTitle != "" {
		row.AXText = windowTitle
	}

	if strings.TrimSpace(row.AXText) == "" && strings.TrimSpace(row.ParsedJSON) == "" && strings.TrimSpace(windowTitle) == "" {
		return nil
	}
	return row
}

func windowTitleFromAXTree(root *activitymon.AXNode) string {
	if root == nil {
		return ""
	}
	if strings.TrimSpace(root.Title) != "" {
		return strings.TrimSpace(root.Title)
	}
	var title string
	appparser.Traverse(root, func(n *activitymon.AXNode) bool {
		if strings.TrimSpace(n.Title) == "" {
			return true
		}
		title = strings.TrimSpace(n.Title)
		return false
	})
	return title
}

func flattenAXTreeText(root *activitymon.AXNode) string {
	if root == nil {
		return ""
	}
	var lines []string
	last := ""
	appparser.Traverse(root, func(n *activitymon.AXNode) bool {
		if n.Role == "AXScrollBar" {
			return false
		}
		text := strings.TrimSpace(appparser.Text(n))
		if text == "" {
			return true
		}
		for _, line := range strings.Split(text, "\n") {
			line = strings.Join(strings.Fields(line), " ")
			if line == "" || line == last {
				continue
			}
			lines = append(lines, line)
			last = line
		}
		return true
	})
	return strings.Join(lines, "\n")
}

func filterRecentSnapshotsForFocusedApp(rows []ActivitySnapshotRow, current *ActivitySnapshotRow, appName string, limit int) []*ActivitySnapshotRow {
	if limit <= 0 {
		return nil
	}
	targets := snapshotAppTokens(current)
	if len(targets) == 0 {
		if token := normalizeFocusedAppToken(appName); token != "" {
			targets[token] = struct{}{}
		}
	}
	if len(targets) == 0 {
		return nil
	}

	recent := make([]*ActivitySnapshotRow, 0, min(limit, len(rows)))
	for i := range rows {
		if current != nil && current.ID != 0 && rows[i].ID == current.ID {
			continue
		}
		if !snapshotMatchesFocusedApp(rows[i], targets) {
			continue
		}
		snap := rows[i]
		recent = append(recent, &snap)
		if len(recent) >= limit {
			break
		}
	}
	return recent
}

func snapshotMatchesFocusedApp(row ActivitySnapshotRow, targets map[string]struct{}) bool {
	for token := range snapshotAppTokens(&row) {
		if _, ok := targets[token]; ok {
			return true
		}
	}
	return false
}

func snapshotAppTokens(row *ActivitySnapshotRow) map[string]struct{} {
	tokens := make(map[string]struct{})
	if row == nil {
		return tokens
	}
	add := func(v string) {
		if token := normalizeFocusedAppToken(v); token != "" {
			tokens[token] = struct{}{}
		}
	}
	add(row.App)
	if row.ParsedJSON == "" {
		return tokens
	}
	var pc appparser.ParsedContent
	if err := json.Unmarshal([]byte(row.ParsedJSON), &pc); err == nil {
		if pc.Metadata != nil {
			add(pc.Metadata["app"])
		}
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(row.ParsedJSON), &raw); err == nil {
		if app, ok := raw["app"].(string); ok {
			add(app)
		}
	}
	return tokens
}

func normalizeFocusedAppToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// extractWindowTitle pulls a usable window title out of a snapshot. Prefers
// ParsedContent.Title (parser-curated), then the parser's window-title
// metadata, then the AX text's first non-empty line as a last resort.
func extractWindowTitle(snap *ActivitySnapshotRow) string {
	if snap == nil {
		return ""
	}
	if snap.ParsedJSON != "" {
		var pc appparser.ParsedContent
		if err := json.Unmarshal([]byte(snap.ParsedJSON), &pc); err == nil {
			if pc.Title != "" {
				return pc.Title
			}
			if t := pc.Metadata["windowTitle"]; t != "" {
				return t
			}
		}
	}
	if snap.AXText != "" {
		for _, line := range strings.Split(snap.AXText, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				return line
			}
		}
	}
	return ""
}

// parsedContentForPrompt decodes the parsed_json on `current` and, if the
// payload is a dedup stub (`dedupOfHash` set), resolves it against `siblings`
// — same-app prior snapshots, newest first — by matching
// `metadata.contentHash`. Returns the resolved ParsedContent and true when
// it carries any usable signal (Title / Body / Channel / URL / Messages),
// false otherwise so callers can fall back to AXText.
//
// Why this matters: the activity monitor elides redundant payloads to tiny
// stubs ({"dedupOfHash":"...","app":"...","contentType":"..."}). Decoding a
// stub directly yields a ParsedContent with no body and no messages —
// without this resolver, the quick-chat prompt would describe the focused
// window with only a content type and skip both messages AND the AX text
// (the prior code path branched on `ParsedJSON != ""` and never reached the
// AX fallback).
func parsedContentForPrompt(current *ActivitySnapshotRow, siblings []*ActivitySnapshotRow) (appparser.ParsedContent, bool) {
	var empty appparser.ParsedContent
	if current == nil || current.ParsedJSON == "" {
		return empty, false
	}
	var pc appparser.ParsedContent
	if err := json.Unmarshal([]byte(current.ParsedJSON), &pc); err != nil {
		return empty, false
	}
	if pc.DedupOfHash != "" {
		for _, sib := range siblings {
			if sib == nil || sib.ParsedJSON == "" {
				continue
			}
			var cand appparser.ParsedContent
			if err := json.Unmarshal([]byte(sib.ParsedJSON), &cand); err != nil {
				continue
			}
			if cand.DedupOfHash != "" {
				continue
			}
			if cand.Metadata != nil && cand.Metadata["contentHash"] == pc.DedupOfHash {
				pc = cand
				break
			}
		}
	}
	hasSignal := pc.Title != "" || pc.Body != "" || pc.Channel != "" || pc.URL != "" || len(pc.Messages) > 0
	return pc, hasSignal
}

// snapshotOCRLookup is the narrow read used by the partial-AX fusion
// path: given (sessionID, snapshotID) return the OCR text the worker
// extracted for that frame. *ActivityStore satisfies this naturally;
// callers without a store (tests, very early startup) pass nil to
// disable the splice — the rest of the prompt still renders.
type snapshotOCRLookup interface {
	GetOCRTextBySnapshot(sessionID string, snapshotID int64) (string, error)
}

// focusedAppOCRLookup returns the activity store typed as the narrow
// OCR-lookup interface, or nil when the activity subsystem hasn't
// been initialised yet (early-startup callers must tolerate nil).
func (a *App) focusedAppOCRLookup() snapshotOCRLookup {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return nil
	}
	return a.activity.store
}

// formatFocusedAppContextForPrompt renders a focusedAppSnapshot as a markdown
// block suitable for either the Haiku suggestion prompt or the agent context
// producer. Caps each section so a runaway AX dump can't blow the budget.
//
// ocrLookup is consulted only when the parser flags partial AX coverage
// (PowerPoint outline-only, Numbers first-column-only, …) so the prompt
// can splice in the visible pixels the AX path can't reach. Pass nil to
// skip the OCR splice entirely.
func formatFocusedAppContextForPrompt(s *focusedAppSnapshot, ocrLookup snapshotOCRLookup) string {
	if s == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Current application\n")
	if s.AppName != "" {
		fmt.Fprintf(&sb, "**App:** %s (%s)\n", s.AppName, s.BundleID)
	}
	if s.WindowTitle != "" {
		fmt.Fprintf(&sb, "**Window:** %s\n", truncateForFocusedAppPrompt(s.WindowTitle, 200))
	}

	if s.Current != nil {
		fmt.Fprintf(&sb, "**Captured:** %s",
			time.UnixMilli(s.Current.Timestamp).Format("2006-01-02 15:04:05"))
		if !s.CurrentFresh {
			sb.WriteString(" (stored snapshot — fresh capture timed out)")
		}
		sb.WriteString("\n\n")

		// Prefer parser-extracted body when available; fall back to AX text.
		// When the current snapshot is a dedup stub (the activity monitor
		// elided the payload because the previous frame from the same bundle
		// had identical content), resolve it against s.Recent before deciding
		// what to render — otherwise the prompt would describe the focused
		// window with only a content type, no actual messages or body.
		pc, hasParsedSignal := parsedContentForPrompt(s.Current, s.Recent)
		switch {
		case hasParsedSignal:
			if pc.ContentType != "" {
				fmt.Fprintf(&sb, "**Type:** %s\n", pc.ContentType)
			}
			if pc.Channel != "" {
				fmt.Fprintf(&sb, "**Channel:** %s\n", pc.Channel)
			}
			if pc.URL != "" {
				fmt.Fprintf(&sb, "**URL:** %s\n", pc.URL)
			}
			body := pc.Body
			// When the parser flagged its coverage as partial (PowerPoint
			// outline, Numbers first-column-only, …) splice in OCR text
			// for the same snapshot so the prompt carries the visible
			// pixels the AX path can't reach. Safe no-op when no OCR
			// has landed yet (background worker, ~2 min lag) or when
			// the snapshot is a fresh in-memory capture (ID == 0).
			if appparser.IsPartialAX(&pc) && ocrLookup != nil && s.Current.ID > 0 {
				if ocr, err := ocrLookup.GetOCRTextBySnapshot(s.Current.SessionID, s.Current.ID); err == nil {
					body = appparser.MergeWithOCR(&pc, ocr)
				}
			}
			if body != "" {
				sb.WriteString("\n")
				sb.WriteString(truncateForFocusedAppPrompt(body, 1500))
				sb.WriteString("\n")
			}
			if len(pc.Messages) > 0 {
				sb.WriteString("\n### Recent messages\n")
				n := len(pc.Messages)
				if n > 8 {
					pc.Messages = pc.Messages[n-8:]
				}
				for _, m := range pc.Messages {
					who := strings.TrimSpace(m.Sender)
					if who == "" {
						who = "—"
					}
					body := strings.TrimSpace(m.Content)
					if body == "" {
						continue
					}
					fmt.Fprintf(&sb, "- **%s:** %s\n", who, truncateForFocusedAppPrompt(body, 300))
				}
			}
		case s.Current.AXText != "":
			sb.WriteString("\n### On screen (AX text)\n")
			sb.WriteString(truncateForFocusedAppPrompt(s.Current.AXText, 1500))
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString("**Current window content:** unavailable from the requested app capture.\n")
	}

	if len(s.Recent) > 0 {
		sb.WriteString("\n## Past context from the same application\n")
		for i, r := range s.Recent {
			if i >= 5 {
				break
			}
			when := time.UnixMilli(r.Timestamp).Format("15:04")
			title := extractWindowTitle(r)
			if title == "" {
				title = "(no title)"
			}
			fmt.Fprintf(&sb, "- %s — %s\n", when, truncateForFocusedAppPrompt(title, 120))
		}
	}

	if strings.TrimSpace(s.General) != "" {
		sb.WriteString("\n## General context\n")
		sb.WriteString(truncateForFocusedAppPrompt(s.General, 1800))
		sb.WriteString("\n")
	}

	return redactActivityModelText(sb.String())
}

// truncateForFocusedAppPrompt cuts s to at most n runes, appending an ellipsis when cut.
func truncateForFocusedAppPrompt(s string, n int) string {
	s = strings.TrimSpace(redactActivityModelText(s))
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---- Stash for context handoff to main chat ----

type focusedAppStashEntry struct {
	snap     *focusedAppSnapshot
	storedAt time.Time
}

const focusedAppStashTTL = 60 * time.Second

var focusedAppStash sync.Map // map[sessionLocalID string]focusedAppStashEntry

// stashFocusedAppContext stores a focused-app snapshot keyed by the upcoming
// chat session's localID so the streaming.go producer can pick it up on the
// first prompt of that session. Caller is responsible for choosing a fresh
// localID per quick-chat submission.
func stashFocusedAppContext(sessionLocalID string, snap *focusedAppSnapshot) {
	if sessionLocalID == "" || snap == nil {
		return
	}
	focusedAppStash.Store(sessionLocalID, focusedAppStashEntry{
		snap:     snap,
		storedAt: time.Now(),
	})
}

// AttachQuickChatContext is the Wails-exposed method the frontend calls
// after creating a chat session in response to quickChat:submit. It captures
// fresh focused-app context for the given bundle ID and stashes it under the
// session's local ID; the focused-app producer in composePrompt consumes the
// stash on the first prompt of that session.
//
// Runs synchronously but bounded by quickChatSubmitCaptureBudget so the
// frontend can safely await it before sending the first prompt — the chat
// transition (handleSwitchProject + handleNewSession) typically takes
// hundreds of ms anyway, masking this overhead.
func (a *App) AttachQuickChatContext(sessionLocalID, bundleID string) error {
	sessionLocalID = strings.TrimSpace(sessionLocalID)
	if sessionLocalID == "" {
		return nil
	}
	bundleID = strings.TrimSpace(bundleID)
	if bundleID == "" {
		return nil
	}
	snap := a.captureFocusedAppContext(bundleID, quickChatSubmitCaptureBudget)
	if snap == nil {
		return nil
	}
	stashFocusedAppContext(sessionLocalID, snap)
	return nil
}

// quickChatSubmitCaptureBudget bounds the wait at submit time. Slightly
// larger than the panel-open budget because submit-path latency is masked by
// the chat-opening transition and the user values context completeness over
// shaving a few hundred ms off an already-multi-step navigation.
const quickChatSubmitCaptureBudget = 800 * time.Millisecond

// popFocusedAppContext returns and clears the stashed snapshot for a session,
// returning nil if none is present or the entry has expired (TTL guards
// against leaks if the user cancels the chat before the prompt fires).
func popFocusedAppContext(sessionLocalID string) *focusedAppSnapshot {
	if sessionLocalID == "" {
		return nil
	}
	v, ok := focusedAppStash.LoadAndDelete(sessionLocalID)
	if !ok {
		return nil
	}
	entry, ok := v.(focusedAppStashEntry)
	if !ok {
		return nil
	}
	if time.Since(entry.storedAt) > focusedAppStashTTL {
		return nil
	}
	return entry.snap
}

// injectFocusedAppContext is a composePrompt producer that surfaces the
// focused-window context captured at quick-chat submit time. One-shot per
// session: the first prompt of a quick-chat session gets the block, later
// prompts in the same session do not (intentional — by then the user is
// having a normal conversation, not asking about what was on screen).
//
// Returns "" when no stash exists, so non-quickchat sessions are unaffected.
func (a *App) injectFocusedAppContext(sessionLocalID string) string {
	snap := a.focusedAppContextForModelRead(popFocusedAppContext(sessionLocalID))
	if snap == nil {
		return ""
	}
	body := formatFocusedAppContextForPrompt(snap, a.focusedAppOCRLookup())
	if strings.TrimSpace(body) == "" {
		return ""
	}
	return body + "\n"
}

// Peek without consuming quick-chat's one-shot context while choosing a tier.
func hasFocusedAppContext(tag string) bool {
	value, ok := focusedAppStash.Load(tag)
	entry, valid := value.(focusedAppStashEntry)
	return ok && valid && time.Since(entry.storedAt) <= focusedAppStashTTL
}

// spotlightScreenChip is what Quick chat may offer as its screen chip
// (computer-chat v1.0.0 rule 8): the app, and its window title when it is the
// frontmost window's. With "Watch my screen" off, Closed Eyes on, or for a
// sensitive app or a private browser window, there is no chip: everything comes back empty, so
// no screen context can go with the question either.
func (a *App) spotlightScreenChip(bundleID, appName string) (string, string, string) {
	return a.spotlightScreenChipFrom(bundleID, appName, activitymon.FrontmostAppInfo())
}

func (a *App) spotlightScreenChipFrom(bundleID, appName string, front activitymon.AppInfo) (string, string, string) {
	// Opened over Isoform itself, Quick chat offers no chip
	// (computer-chat v1.1.0 rule 8).
	if a == nil || strings.TrimSpace(bundleID) == "" || isOwnAppName(appName) || isOwnBundleID(bundleID) || a.IsClosedEyesModeEnabled() || !a.screenWatchingEnabled() || activitySourceAppIsSensitive(bundleID, appName) {
		return "", "", ""
	}
	title := ""
	if !front.FocusUnavailable && (front.BundleID == bundleID || (appName != "" && strings.EqualFold(front.AppName, appName))) &&
		!activitySourceAppIsSensitive(front.BundleID, front.AppName) {
		if activitymon.IsPrivateBrowsing(front.WindowTitle) {
			return "", "", ""
		}
		title = truncateAtRuneBoundary(strings.Join(strings.Fields(front.WindowTitle), " "), 160)
	}
	return bundleID, appName, title
}

// isOwnBundleID is Isoform's own app, by bundle id.
func isOwnBundleID(bundleID string) bool {
	switch strings.ToLower(strings.TrimSpace(bundleID)) {
	case "com.wails.isoform", "com.wails.yansu":
		return true
	}
	return false
}
