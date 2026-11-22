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
	// assistantHistoryKnownCap is how many stored beliefs of the project the
	// prompt lists, newest first, so the model does not restate them.
	assistantHistoryKnownCap  = 40
	assistantHistoryModelWait = 90 * time.Second
	assistantHistorySource    = "assistant-history"
	assistantHistoryClaudeMd  = "claude-md"
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
	Agents     []string
	SessionsAt []time.Time
}

type assistantHistoryKnowHow struct {
	Agents    []string
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
func (a *App) assistantHistoryExcerpt(ctx context.Context, sessions []assistantHistorySession, budget int) (string, []assistantHistorySession, error) {
	var blocks []string
	var included []assistantHistorySession
	used := 0
	for _, session := range sessions {
		if len(included) == assistantHistoryExcerptSess {
			break
		}
		var turns []assistantHistoryTurn
		if err := a.withHistorySource(ctx, session.Assistant, func() { turns = readAssistantHistoryTurns(session) }); err != nil {
			return "", nil, err
		}
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
	return strings.Join(blocks, "\n\n"), included, nil
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
func buildAssistantHistoryPrompt(projectName, excerpt, instructionsName, instructions, languageName string, known []string) string {
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
	b.WriteString("- beliefs never repeat one listed under Already known, in the same or other words. Name a belief only for something none of them says.\n")
	b.WriteString("- Leave a list empty rather than guess.\n\n")
	b.WriteString("Reply with one JSON object only, in this shape:\n")
	b.WriteString(`{"status":"","openProblem":"","openQuestion":"","openChip":"","openSession":0,"beliefs":[{"text":"","session":1,"sessions":[1]}],"knowHow":[{"title":"","takeaway":"","kind":"reference","fromClaudeMd":false,"session":1}]}`)
	b.WriteString("\n\n")
	b.WriteString("Project: " + projectName + "\n\n")
	if len(known) > 0 {
		b.WriteString("Already known about how they work on this project:\n")
		for _, belief := range known {
			b.WriteString("- " + belief + "\n")
		}
		b.WriteString("\n")
	}
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
			if !ok || seen[n] {
				continue
			}
			belief.Agents = knowledgeProvenanceUnion(belief.Agents, []string{other.Assistant})
			if other.Assistant != session.Assistant {
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
		if historyKnowHowIsAgentWork(kind, title+" "+takeaway) {
			continue
		}
		if item.FromClaudeMd {
			if instructionsName == "" {
				continue
			}
			var agents []string
			for _, s := range included {
				agents = knowledgeProvenanceUnion(agents, []string{s.Assistant})
			}
			out.KnowHow = append(out.KnowHow, assistantHistoryKnowHow{Agents: agents, Title: title, Takeaway: takeaway, Kind: kind, Source: assistantHistoryClaudeMd, Assistant: "", At: instructionsAt})
			continue
		}
		session, ok := sessionAt(item.Session)
		if !ok {
			continue
		}
		out.KnowHow = append(out.KnowHow, assistantHistoryKnowHow{Title: title, Takeaway: takeaway, Kind: kind, Source: assistantHistorySource, Agents: []string{session.Assistant}, Assistant: session.Assistant, At: session.At})
	}
	return out
}

// runAssistantHistoryProject makes the one model call for a project and
// returns the checked update. An empty excerpt makes no call.
func (a *App) runAssistantHistoryProject(ctx context.Context, project HistoryProject, sessions []assistantHistorySession, languageName string) (assistantHistoryProjectUpdate, bool, error) {
	excerpt, included, err := a.assistantHistoryExcerpt(ctx, sessions, assistantHistoryExcerptRune)
	if err != nil {
		return assistantHistoryProjectUpdate{}, false, err
	}
	if excerpt == "" {
		return assistantHistoryProjectUpdate{}, false, nil
	}
	// Keep dispatch in the same read interval as its final consent check.
	// A withdrawal cancels the worker before waiting for an in-flight call.
	historySourceMu.RLock()
	defer historySourceMu.RUnlock()
	settings, err := readLearnFromIt()
	if err != nil {
		return assistantHistoryProjectUpdate{}, false, err
	}
	allowed := settings.allowedSources()
	for _, s := range included {
		if !allowed[s.Assistant] {
			return assistantHistoryProjectUpdate{}, false, nil
		}
	}
	if ctx.Err() != nil || a.IsClosedEyesModeEnabled() {
		return assistantHistoryProjectUpdate{}, false, ctx.Err()
	}
	name, instructions, instructionsAt := readAssistantHistoryInstructions(project.Path)
	prompt := buildAssistantHistoryPrompt(project.Name, excerpt, name, instructions, languageName, a.assistantHistoryKnownBeliefs(project, allowed))
	if ctx.Err() != nil || a.IsClosedEyesModeEnabled() {
		return assistantHistoryProjectUpdate{}, false, ctx.Err()
	}
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

// assistantHistoryKnownBeliefs are the project's stored history beliefs the
// prompt lists, newest first. Only beliefs this pass learned from the same
// project are listed, each read through the same source check and the same
// text check as a reply.
func (a *App) assistantHistoryKnownBeliefs(project HistoryProject, allowed map[string]bool) []string {
	entries, err := a.GetMemoryEntries()
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if len(out) == assistantHistoryKnownCap {
			break
		}
		if entry.Source != assistantHistorySource || isUserEditedMemory(entry.Content) {
			continue
		}
		if assistantHistoryStoredBeliefOf(entry).project != project.Name || !a.memorySourceAllowsRead(entry.Content) {
			continue
		}
		doc, ok := parseOKFDocument(entry.Content)
		if !ok {
			continue
		}
		names := historyAgentsForExistingRecord(doc.Frontmatter)
		permitted := true
		for _, name := range names {
			permitted = permitted && allowed[name]
		}
		if !permitted {
			continue
		}
		text := checkedAssistantHistoryText(beliefInSecondPerson(entry.Title), 240)
		if text == "" || memoryTextIsPromptOrTemplate(text) {
			continue
		}
		out = append(out, text)
	}
	return out
}

// assistantHistoryRecordID is a stable ID per project and text, so the same
// belief or know-how item is written once.
func assistantHistoryRecordID(prefix, projectID, text string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + strings.ToLower(strings.TrimSpace(text))))
	return prefix + hex.EncodeToString(sum[:])[:16]
}

// storeAssistantHistoryBeliefs writes each belief as an Inferred memory with
// the history source. A belief that repeats one already in About you, in the
// same or nearly the same words, makes no new file: when the stored one came
// from the same project and assistant, this belief's sessions are added to it,
// so a belief seen in more sessions ranks higher instead of showing twice.
func (a *App) storeAssistantHistoryBeliefs(project HistoryProject, beliefs []assistantHistoryBelief, now time.Time) (int, error) {
	if len(beliefs) == 0 {
		return 0, nil
	}
	entries, err := a.GetMemoryEntries()
	if err != nil {
		return 0, err
	}
	stored := make([]assistantHistoryStoredBelief, 0, len(entries))
	for _, entry := range entries {
		stored = append(stored, assistantHistoryStoredBeliefOf(entry))
	}
	written := 0
	for _, belief := range beliefs {
		if memoryTextIsPromptOrTemplate(belief.Text) {
			continue
		}
		belief.Text = beliefInSecondPerson(belief.Text)
		agents := knowledgeProvenanceUnion(belief.Agents, []string{belief.Assistant})
		stamps := assistantHistoryBeliefStamps(belief)
		if match, ok := matchAssistantHistoryBelief(stored, belief.Text); ok {
			if match.id != "" && match.project == project.Name && match.assistant == belief.Assistant {
				a.strengthenAssistantHistoryBelief(match.id, stamps, agents)
			}
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
			"historyAgents":      agents,
			"history_session_at": okfTimestamp(belief.At),
			"history_project":    project.Name,
		}
		if len(stamps) > 1 {
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
		stored = append(stored, assistantHistoryStoredBelief{id: id, title: belief.Text, project: project.Name, assistant: belief.Assistant, shape: newBeliefShape(belief.Text)})
		written++
	}
	return written, nil
}

// assistantHistoryStoredBelief is a belief already in About you, as pass 2
// compares it. project and assistant are set for a history belief only.
type assistantHistoryStoredBelief struct {
	id, title, project, assistant string
	// edited is the person's own words: never strengthened.
	edited bool
	shape  beliefShape
}

func assistantHistoryStoredBeliefOf(entry MemoryEntry) assistantHistoryStoredBelief {
	out := assistantHistoryStoredBelief{id: entry.ID, title: entry.Title, edited: isUserEditedMemory(entry.Content), shape: newBeliefShape(entry.Title)}
	if entry.Source != assistantHistorySource || out.edited {
		return out
	}
	if doc, ok := parseOKFDocument(legacyMemoryWithoutConfirmation(entry.Content)); ok {
		out.project = okfString(doc.Frontmatter, "history_project")
		out.assistant = okfString(doc.Frontmatter, "history_assistant")
	}
	return out
}

// matchAssistantHistoryBelief finds the stored belief a new one repeats:
// the same words first, then nearly the same words that do not contradict it.
func matchAssistantHistoryBelief(stored []assistantHistoryStoredBelief, text string) (assistantHistoryStoredBelief, bool) {
	key := strings.ToLower(strings.TrimSpace(text))
	for _, belief := range stored {
		if strings.ToLower(strings.TrimSpace(beliefInSecondPerson(belief.title))) == key {
			return belief, true
		}
	}
	shape := newBeliefShape(text)
	for _, belief := range stored {
		if nearDuplicateBeliefShapes(shape, belief.shape) {
			return belief, true
		}
	}
	return assistantHistoryStoredBelief{}, false
}

// assistantHistoryBeliefStamps are the session times one belief names, the
// first session included.
func assistantHistoryBeliefStamps(belief assistantHistoryBelief) []string {
	seen := map[int64]bool{}
	var out []string
	for _, at := range append([]time.Time{belief.At}, belief.SessionsAt...) {
		if at.IsZero() || seen[at.UnixMilli()] {
			continue
		}
		seen[at.UnixMilli()] = true
		out = append(out, okfTimestamp(at))
	}
	return out
}

// strengthenAssistantHistoryBelief adds session times to a stored history
// belief through the memory writer, so Forget, the person's edits and version
// history apply as for any other write.
func (a *App) strengthenAssistantHistoryBelief(id string, stamps, agents []string) {
	if len(stamps) == 0 {
		return
	}
	dir, err := memoryDir()
	if err != nil {
		return
	}
	current, err := os.ReadFile(filepath.Join(dir, id+".md"))
	if err != nil {
		return
	}
	merge := func(content string) (string, bool) {
		next, changed := mergeHistorySessionStamps(content, stamps)
		if !changed {
			next = content
		}
		doc, ok := parseOKFDocument(next)
		if !ok {
			return content, false
		}
		prior := historyAgentsForExistingRecord(doc.Frontmatter)
		merged := knowledgeProvenanceUnion(prior, agents)
		if len(merged) == len(prior) && len(okfStringSlice(doc.Frontmatter, "historyAgents")) > 0 {
			return next, changed
		}
		doc.Frontmatter["historyAgents"] = merged
		return renderOKFDocument(doc.Frontmatter, doc.Body), true
	}
	merged, ok := merge(string(current))
	if !ok || learnedHidden(string(current)) || isUserEditedMemory(string(current)) {
		return
	}
	if _, err := a.saveMemoryEntryWithRewrite(id, merged, false, func(latest string) string {
		if learnedHidden(latest) || isUserEditedMemory(latest) {
			return latest
		}
		next, _ := merge(latest)
		return next
	}); err != nil && !errors.Is(err, errMemoryForgotten) {
		log.Printf("[assistant-history] belief not strengthened: %v", err)
	}
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
		evidence := newKnowledgeGenerationProvenance()
		evidence.historyAgents = knowledgeProvenanceUnion(item.Agents, []string{item.Assistant})
		if _, ok := relearnKnowHowWithEvidence(dir, item.Title, id, KnowHowMoment{At: item.At.UnixMilli(), Assistant: item.Assistant}, []string{item.Takeaway}, item.Source, nil, evidence); ok {
			a.markMemoryDirty("assistant-history-relearned")
			continue
		}
		content := buildOKFKnowledgeMarkdown(id, item.Title, item.Takeaway, now, item.Source, []string{project.Name}, nil, []string{item.Takeaway}, item.Kind)
		if doc, ok := parseOKFDocument(content); ok {
			doc.Frontmatter["scope"] = "mine"
			doc.Frontmatter["historyAgents"] = evidence.historyAgents
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
