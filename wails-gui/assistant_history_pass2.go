package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"wails-gui/internal/pii"
)

// assistant_history_pass2.go is pass 2: one model call per project with new
// sessions, through the person's own AI first (runEphemeralClaudeModelContext
// routes it). The model reads the last few sessions and the project's
// CLAUDE.md or AGENTS.md, and returns strict JSON. Everything it returns is
// checked again before it is stored: secret-looking text is dropped, lengths
// are capped, and beliefs become Inferred memories.

const (
	assistantHistoryBeliefCap   = 8
	assistantHistoryKnowHowCap  = 3
	assistantHistoryExcerptSess = 3
	assistantHistoryExcerptRune = 6000
	assistantHistoryModelWait   = 90 * time.Second
	assistantHistorySource      = "assistant-history"
	assistantHistoryClaudeMd    = "claude-md"
)

// assistantHistoryModelCall is the one model entry point for pass 2. Tests
// replace it so no real model runs and the prompt can be inspected.
var assistantHistoryModelCall = func(ctx context.Context, a *App, prompt string) (string, error) {
	return a.runEphemeralClaudeModelContext(ctx, prompt, a.lightTaskModel(), assistantHistoryModelWait)
}

// The light model is the default: pass 2 summarizes a few kilobytes into a
// short JSON object, which the light tier does reliably, and it runs once per
// project per new session rather than per message.

type assistantHistoryReply struct {
	Status       string                         `json:"status"`
	OpenProblem  string                         `json:"openProblem"`
	OpenQuestion string                         `json:"openQuestion"`
	OpenChip     string                         `json:"openChip"`
	OpenSession  int                            `json:"openSession"`
	Beliefs      []assistantHistoryReplyBelief  `json:"beliefs"`
	KnowHow      []assistantHistoryReplyKnowHow `json:"knowHow"`
}

type assistantHistoryReplyBelief struct {
	Text     string `json:"text"`
	Session  int    `json:"session"`
	Sessions []int  `json:"sessions"`
}

type assistantHistoryReplyKnowHow struct {
	Title        string `json:"title"`
	Takeaway     string `json:"takeaway"`
	Kind         string `json:"kind"`
	FromClaudeMd bool   `json:"fromClaudeMd"`
	Session      int    `json:"session"`
}

// assistantHistoryBelief and assistantHistoryKnowHow are the checked drafts
// the store writes. The session index points into the excerpt.
type assistantHistoryBelief struct {
	Text      string
	Assistant string
	At        time.Time
	// SessionsAt are the times of every session that shows the belief, so
	// the portrait counts each one on its own day.
	SessionsAt []time.Time
}

type assistantHistoryKnowHow struct {
	Title     string
	Takeaway  string
	Kind      string
	Source    string
	Assistant string
	At        time.Time
}

// assistantHistoryExcerpt builds the session text for the prompt, newest
// session first numbered S1, within a size budget. It returns the sessions it
// included, in the same order as the numbers.
func assistantHistoryExcerpt(sessions []assistantHistorySession, budget int) (string, []assistantHistorySession) {
	var blocks []string
	var included []assistantHistorySession
	used := 0
	for _, session := range sessions {
		if len(included) == assistantHistoryExcerptSess {
			break
		}
		turns := readAssistantHistoryTurns(session)
		if len(turns) == 0 {
			continue
		}
		// Keep the most recent turns of each session that fit.
		var lines []string
		for i := len(turns) - 1; i >= 0 && len(lines) < 12; i-- {
			lines = append([]string{turns[i].Role + ": " + turns[i].Text}, lines...)
		}
		header := fmt.Sprintf("[S%d] %s session, %s", len(included)+1, assistantHistoryLabel(session.Assistant), session.At.In(time.Local).Format("2006-01-02 15:04"))
		block := header + "\n" + strings.Join(lines, "\n")
		size := utf8.RuneCountInString(block)
		if used+size > budget {
			if len(included) == 0 {
				// The newest session alone is over budget: cut it to fit.
				runes := []rune(block)
				block = string(runes[:budget])
				size = budget
			} else {
				break
			}
		}
		used += size
		blocks = append(blocks, block)
		included = append(included, session)
	}
	return strings.Join(blocks, "\n\n"), included
}

// assistantHistoryLabel is the assistant's name as the person knows it.
func assistantHistoryLabel(assistant string) string {
	switch assistant {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	default:
		return "Assistant"
	}
}

// buildAssistantHistoryPrompt is the whole request. It names what must never
// be in the answer, and it asks for JSON only.
func buildAssistantHistoryPrompt(projectName, excerpt, instructionsName, instructions, languageName string) string {
	var b strings.Builder
	b.WriteString("You read a person's recent work sessions with their AI coding assistants for one project, ")
	b.WriteString("and say what Isoform should remember about how they work. Use only what the sessions and the project instructions say.\n\n")
	b.WriteString("Write every text value in " + languageName + ".\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- status: one plain sentence on where the project stands, only when the sessions say it plainly. Otherwise \"\".\n")
	b.WriteString("- openProblem: a short noun phrase for a problem the last session left open, for example \"the onboarding tests failing\". Otherwise \"\".\n")
	b.WriteString("- openQuestion: the question the person would ask Isoform about that problem, for example \"Why are the onboarding tests failing?\". \"\" when openProblem is \"\".\n")
	b.WriteString("- openChip: a shorter \"what's left\" question about the same problem, for example \"What's left on the onboarding tests?\". \"\" when openProblem is \"\".\n")
	b.WriteString("- openSession: the session number (S1, S2...) the open problem came from.\n")
	b.WriteString("- beliefs: up to 8 short statements about how this person works, each clearly supported by the sessions, for example \"Runs the tests before every push\". Each names the session number it came from, and in sessions every session number that shows it.\n")
	b.WriteString("- knowHow: up to 3 reusable items: a fix or decision found in the sessions (fromClaudeMd false), or a how-to written in the project instructions (fromClaudeMd true). Each has a title, a one-sentence takeaway, a kind (fix, decision, how_to, gotcha or reference) and the session number it came from (0 for the instructions).\n")
	b.WriteString("- Never make a belief from a prompt, a template or a tool's instructions in the sessions (for example a note telling the assistant what output to return). Describe only what the person did or chose.\n")
	b.WriteString("- Never include secrets, keys, tokens, passwords, or account identifiers.\n")
	b.WriteString("- Never include personal details about other people (names with private context, contact details, health, family, money). Describe the work, not the people.\n")
	b.WriteString("- Leave a list empty rather than guess.\n\n")
	b.WriteString("Reply with one JSON object only, in this shape:\n")
	b.WriteString(`{"status":"","openProblem":"","openQuestion":"","openChip":"","openSession":0,"beliefs":[{"text":"","session":1,"sessions":[1]}],"knowHow":[{"title":"","takeaway":"","kind":"reference","fromClaudeMd":false,"session":1}]}`)
	b.WriteString("\n\n")
	b.WriteString("Project: " + projectName + "\n\n")
	b.WriteString("Recent sessions, newest first:\n")
	b.WriteString(excerpt)
	b.WriteString("\n")
	if strings.TrimSpace(instructions) != "" {
		b.WriteString("\nProject instructions (" + instructionsName + "):\n")
		b.WriteString(instructions)
		b.WriteString("\n")
	}
	return b.String()
}

// parseAssistantHistoryReply reads the first JSON object in the reply.
func parseAssistantHistoryReply(raw string) (assistantHistoryReply, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return assistantHistoryReply{}, errors.New("assistant history reply has no JSON object")
	}
	var reply assistantHistoryReply
	if err := json.Unmarshal([]byte(raw[start:end+1]), &reply); err != nil {
		return assistantHistoryReply{}, fmt.Errorf("assistant history reply is not JSON: %w", err)
	}
	return reply, nil
}

// checkedAssistantHistoryText trims, caps and drops text that looks like a
// secret. A dropped value is "", never a partly masked string.
func checkedAssistantHistoryText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return ""
	}
	if _, dropped := redactAssistantHistoryText(value); dropped > 0 || assistantHistoryLooksLikeSecret(value) {
		return ""
	}
	if pii.HasRedaction(value) {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes])
	}
	return value
}

// assistantHistoryProjectUpdate is what pass 2 learned about one project.
type assistantHistoryProjectUpdate struct {
	Status        string
	OpenProblem   string
	OpenQuestion  string
	OpenChip      string
	OpenProblemAt int64
	Beliefs       []assistantHistoryBelief
	KnowHow       []assistantHistoryKnowHow
}

// buildAssistantHistoryUpdate checks a parsed reply against the excerpt it was
// built from. The session numbers map back to the sessions' own times, so the
// model never supplies a date.
func buildAssistantHistoryUpdate(reply assistantHistoryReply, included []assistantHistorySession, instructionsName string, instructionsAt time.Time) assistantHistoryProjectUpdate {
	sessionAt := func(n int) (assistantHistorySession, bool) {
		if n < 1 || n > len(included) {
			return assistantHistorySession{}, false
		}
		return included[n-1], true
	}
	var out assistantHistoryProjectUpdate
	out.Status = checkedAssistantHistoryText(reply.Status, 200)
	out.OpenProblem = checkedAssistantHistoryText(reply.OpenProblem, 120)
	if out.OpenProblem != "" {
		out.OpenQuestion = checkedAssistantHistoryText(reply.OpenQuestion, 160)
		out.OpenChip = checkedAssistantHistoryText(reply.OpenChip, 120)
		if session, ok := sessionAt(reply.OpenSession); ok {
			out.OpenProblemAt = session.At.UnixMilli()
		} else if len(included) > 0 {
			out.OpenProblemAt = included[0].At.UnixMilli()
		}
	}
	for _, item := range reply.Beliefs {
		if len(out.Beliefs) == assistantHistoryBeliefCap {
			break
		}
		text := checkedAssistantHistoryText(item.Text, 240)
		session, ok := sessionAt(item.Session)
		// Y42: prompt or template text is never a belief.
		if text == "" || !ok || memoryTextIsPromptOrTemplate(text) {
			continue
		}
		belief := assistantHistoryBelief{Text: text, Assistant: session.Assistant, At: session.At}
		seen := map[int]bool{}
		for _, n := range append([]int{item.Session}, item.Sessions...) {
			other, ok := sessionAt(n)
			if !ok || seen[n] || other.Assistant != session.Assistant {
				continue
			}
			seen[n] = true
			belief.SessionsAt = append(belief.SessionsAt, other.At)
		}
		out.Beliefs = append(out.Beliefs, belief)
	}
	for _, item := range reply.KnowHow {
		if len(out.KnowHow) == assistantHistoryKnowHowCap {
			break
		}
		title := checkedAssistantHistoryText(item.Title, 120)
		takeaway := checkedAssistantHistoryText(item.Takeaway, 300)
		if title == "" || takeaway == "" {
			continue
		}
		kind := strings.TrimSpace(item.Kind)
		if !validKnowHowKind(kind) {
			kind = "reference"
		}
		if item.FromClaudeMd {
			if instructionsName == "" {
				continue
			}
			out.KnowHow = append(out.KnowHow, assistantHistoryKnowHow{Title: title, Takeaway: takeaway, Kind: kind, Source: assistantHistoryClaudeMd, Assistant: "", At: instructionsAt})
			continue
		}
		session, ok := sessionAt(item.Session)
		if !ok {
			continue
		}
		out.KnowHow = append(out.KnowHow, assistantHistoryKnowHow{Title: title, Takeaway: takeaway, Kind: kind, Source: assistantHistorySource, Assistant: session.Assistant, At: session.At})
	}
	return out
}

// runAssistantHistoryProject makes the one model call for a project and
// returns the checked update. An empty excerpt makes no call.
func (a *App) runAssistantHistoryProject(ctx context.Context, project HistoryProject, sessions []assistantHistorySession, languageName string) (assistantHistoryProjectUpdate, bool, error) {
	excerpt, included := assistantHistoryExcerpt(sessions, assistantHistoryExcerptRune)
	if excerpt == "" {
		return assistantHistoryProjectUpdate{}, false, nil
	}
	name, instructions, instructionsAt := readAssistantHistoryInstructions(project.Path)
	prompt := buildAssistantHistoryPrompt(project.Name, excerpt, name, instructions, languageName)
	raw, err := assistantHistoryModelCall(ctx, a, prompt)
	if err != nil {
		return assistantHistoryProjectUpdate{}, true, err
	}
	reply, err := parseAssistantHistoryReply(raw)
	if err != nil {
		return assistantHistoryProjectUpdate{}, true, err
	}
	return buildAssistantHistoryUpdate(reply, included, name, instructionsAt), true, nil
}

// assistantHistoryRecordID is a stable ID per project and text, so the same
// belief or know-how item is written once.
func assistantHistoryRecordID(prefix, projectID, text string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + strings.ToLower(strings.TrimSpace(text))))
	return prefix + hex.EncodeToString(sum[:])[:16]
}

// storeAssistantHistoryBeliefs writes each belief as an Inferred memory with
// the history source. Titles already in About you are skipped.
func (a *App) storeAssistantHistoryBeliefs(project HistoryProject, beliefs []assistantHistoryBelief, now time.Time) (int, error) {
	if len(beliefs) == 0 {
		return 0, nil
	}
	existing := map[string]bool{}
	entries, err := a.GetMemoryEntries()
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		existing[strings.ToLower(strings.TrimSpace(beliefInSecondPerson(entry.Title)))] = true
	}
	written := 0
	for _, belief := range beliefs {
		if memoryTextIsPromptOrTemplate(belief.Text) {
			continue
		}
		belief.Text = beliefInSecondPerson(belief.Text)
		key := strings.ToLower(strings.TrimSpace(belief.Text))
		if existing[key] {
			continue
		}
		id := assistantHistoryRecordID("memory-history-", project.ID, belief.Text)
		fields := map[string]any{
			"type":               "Isoform Memory",
			"title":              belief.Text,
			"timestamp":          okfTimestamp(now),
			"source":             assistantHistorySource,
			"dimension":          normalizeMemoryDimension(""),
			"area":               classifyMemoryArea(belief.Text, ""),
			"history_assistant":  belief.Assistant,
			"history_session_at": okfTimestamp(belief.At),
			"history_project":    project.Name,
		}
		if len(belief.SessionsAt) > 1 {
			stamps := make([]string, 0, len(belief.SessionsAt))
			for _, at := range belief.SessionsAt {
				stamps = append(stamps, okfTimestamp(at))
			}
			fields["history_sessions_at"] = stamps
		}
		content := renderOKFDocument(fields, "## Memory Points\n\n- "+belief.Text)
		if _, err := a.saveMemoryEntry(id, content, false); err != nil {
			if errors.Is(err, errMemoryForgotten) {
				continue
			}
			log.Printf("[assistant-history] belief not saved: %v", err)
			continue
		}
		existing[key] = true
		written++
	}
	return written, nil
}

// storeAssistantHistoryKnowHow writes each item as a knowledge entry with the
// history or CLAUDE.md source. An existing entry is left as it is.
func (a *App) storeAssistantHistoryKnowHow(project HistoryProject, items []assistantHistoryKnowHow, now time.Time) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	dir, err := knowledgeDir()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return 0, err
	}
	written := 0
	for _, item := range items {
		prefix := "knowledge-history-"
		if item.Source == assistantHistoryClaudeMd {
			prefix = "knowledge-claude-md-"
		}
		id := assistantHistoryRecordID(prefix, project.ID, item.Title)
		path := filepath.Join(dir, id+".md")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if _, ok := relearnKnowHowWithProvenance(dir, item.Title, id, KnowHowMoment{At: item.At.UnixMilli(), Assistant: item.Assistant}, []string{item.Takeaway}, item.Source, nil); ok {
			a.markMemoryDirty("assistant-history-relearned")
			continue
		}
		content := buildOKFKnowledgeMarkdown(id, item.Title, item.Takeaway, now, item.Source, []string{project.Name}, nil, []string{item.Takeaway}, item.Kind)
		if doc, ok := parseOKFDocument(content); ok {
			doc.Frontmatter["scope"] = "mine"
			doc.Frontmatter["history_session_at"] = okfTimestamp(item.At)
			if item.Assistant != "" {
				doc.Frontmatter["history_assistant"] = item.Assistant
			}
			content = renderOKFDocument(doc.Frontmatter, doc.Body)
		}
		content = knowledgeProvenance(content, "", []string{item.Source}, []string{project.Name}, true)
		unlock := memoryFileLocks.Acquire("knowledge:" + id)
		err := os.WriteFile(path, []byte(content), 0600)
		unlock()
		if err != nil {
			log.Printf("[assistant-history] know-how not saved: %v", err)
			continue
		}
		written++
	}
	if written > 0 {
		a.markMemoryDirty("assistant-history")
	}
	return written, nil
}

// assistantHistoryCounts counts what history has stored, for the view. It
// reads the stored files, so it runs after a pass 2 write, never on the read path.
func (a *App) assistantHistoryCounts() (beliefs, knowHow int) {
	if entries, err := a.GetMemoryEntries(); err == nil {
		for _, entry := range entries {
			if entry.Source == assistantHistorySource {
				beliefs++
			}
		}
	}
	if cards, err := a.GetKnowledgeEntries(); err == nil {
		for _, entry := range cards {
			doc, ok := parseOKFDocument(entry.Content)
			if !ok {
				continue
			}
			if source := okfString(doc.Frontmatter, "source"); source == assistantHistorySource || source == assistantHistoryClaudeMd {
				knowHow++
			}
		}
	}
	return beliefs, knowHow
}
