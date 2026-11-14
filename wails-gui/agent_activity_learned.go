package main

import (
	"bufio"
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Learned items that came only from agent activity.
//
// Memories and know-how are files. One that was learned only from work an
// agent did (a side session, a scheduled prompt, a Codex worker, a screen
// session that only showed coding agents) carries `activity_origin: agent`
// in its frontmatter. The readers every page goes through
// (readMemoryEntries, readKnowledgeEntries) skip it, so it is out of About
// you, How you do things, topics, people and the brief. The file stays: it is
// hidden, not deleted, and Forget and Privacy still reach it.
//
// New learning never writes such a file: the history reader drops agent
// turns (assistant_history_origin.go) and the triage batch never sends agent
// sessions to a model (activity_hourly_batch.go). The one-time cleanup below
// marks what was learned before that.

const (
	learnedAgentOriginKey   = "activity_origin"
	learnedAgentOriginValue = "agent"
	learnedAgentOriginLine  = learnedAgentOriginKey + ": " + learnedAgentOriginValue

	// agentActivityCleanupKey is the UI state key that records the cleanup.
	agentActivityCleanupKey = "agent_activity_cleanup_v1"
	// agentActivityMatchSlack is how far a record's session time may sit from
	// a line in the session file it came from. The time is the file's
	// modification time when it was read, which is the time of a line in it.
	agentActivityMatchSlack = 5 * time.Second
	// agentActivityExcerptTurns is how many trailing turns the history reader
	// put in its excerpt (assistantHistoryExcerpt keeps 12 per session).
	agentActivityExcerptTurns = 12
)

// learnedFromAgents reports a memory or know-how file marked as learned only
// from agent activity. It reads the frontmatter lines only, so every list
// pays a string scan, not a YAML parse.
func learnedFromAgents(content string) bool {
	trimmed := strings.TrimPrefix(content, "\ufeff")
	if !strings.HasPrefix(trimmed, "---") {
		return false
	}
	lines := strings.Split(trimmed, "\n")
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "---" {
			return false
		}
		if line == learnedAgentOriginLine || line == learnedAgentOriginKey+": \""+learnedAgentOriginValue+"\"" {
			return true
		}
	}
	return false
}

// markLearnedFromAgents adds the agent origin line under the opening
// frontmatter fence. It changes nothing else in the file, so provenance and
// version history stay as they were. ok is false for a file without
// frontmatter or one already marked.
func markLearnedFromAgents(content string) (string, bool) {
	if learnedFromAgents(content) {
		return content, false
	}
	bom := ""
	body := content
	if strings.HasPrefix(body, "\ufeff") {
		bom, body = "\ufeff", strings.TrimPrefix(body, "\ufeff")
	}
	first, rest, found := strings.Cut(body, "\n")
	if !found || strings.TrimSpace(first) != "---" || !strings.Contains(rest, "\n---") && !strings.HasPrefix(rest, "---") {
		return content, false
	}
	return bom + first + "\n" + learnedAgentOriginLine + "\n" + rest, true
}

// runAgentActivityCleanup runs the one-time cleanup once per data folder.
func (a *App) runAgentActivityCleanup(ctx context.Context) {
	if ctx == nil || a == nil || a.store == nil {
		return
	}
	if done, err := a.store.GetUIState(ctx, agentActivityCleanupKey); err != nil || done == "done" {
		return
	}
	result, err := a.cleanupAgentLearnedRecords(ctx)
	if err != nil {
		log.Printf("[agent-activity] cleanup not finished: %v", err)
		return
	}
	log.Printf("[agent-activity] cleanup hid memories=%d know_how=%d checked=%d unresolved=%d",
		result.Memories, result.KnowHow, result.Checked, result.Unresolved)
	if err := a.store.SetUIState(ctx, agentActivityCleanupKey, "done"); err != nil {
		log.Printf("[agent-activity] cleanup marker not saved: %v", err)
	}
}

type agentCleanupResult struct {
	Memories   int
	KnowHow    int
	Checked    int
	Unresolved int
}

// agentCleanupRecord is one learned file the cleanup checks.
type agentCleanupRecord struct {
	kind       string // "memory" | "knowledge"
	id         string
	path       string
	source     string
	sessionIDs []string // activity sessions
	assistant  string   // history
	project    string   // history
	sessionsAt []time.Time
}

// cleanupAgentLearnedRecords marks every memory and know-how file whose
// sources were all agent activity. A record whose source cannot be found is
// left as it is, and the person's own edits are never touched.
func (a *App) cleanupAgentLearnedRecords(ctx context.Context) (agentCleanupResult, error) {
	var result agentCleanupResult
	records, err := agentCleanupCandidates()
	if err != nil {
		return result, err
	}
	result.Checked = len(records)

	var history []*agentCleanupRecord
	hide := map[*agentCleanupRecord]bool{}
	for i := range records {
		r := &records[i]
		switch r.source {
		case "activity":
			agent, ok := a.activitySessionsAllAgent(r.sessionIDs)
			if !ok {
				result.Unresolved++
				continue
			}
			hide[r] = agent
		case assistantHistorySource:
			history = append(history, r)
		}
	}
	if len(history) > 0 {
		roots, err := assistantHistoryRootsFromEnv()
		if err != nil {
			return result, err
		}
		resolved := resolveHistoryRecordsAgent(ctx, roots, history)
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		for _, r := range history {
			agent, ok := resolved[r]
			if !ok {
				result.Unresolved++
				continue
			}
			hide[r] = agent
		}
	}

	// History writes hold writeMu; take it so a pass 2 never races a mark.
	a.assistantHistory.writeMu.Lock()
	defer a.assistantHistory.writeMu.Unlock()
	for r, agent := range hide {
		if !agent {
			continue
		}
		if a.hideLearnedRecord(r) {
			if r.kind == "memory" {
				result.Memories++
			} else {
				result.KnowHow++
			}
		}
	}
	if result.Memories+result.KnowHow > 0 {
		a.markMemoryDirty("agent-activity-hidden")
	}
	return result, nil
}

// hideLearnedRecord marks one file under its writer lock.
func (a *App) hideLearnedRecord(r *agentCleanupRecord) bool {
	lockID := r.id
	if r.kind == "knowledge" {
		lockID = "knowledge:" + r.id
	}
	unlock := memoryFileLocks.Acquire(lockID)
	defer unlock()
	data, err := os.ReadFile(r.path)
	if err != nil {
		return false
	}
	content := string(data)
	if isUserEditedMemory(content) {
		return false
	}
	marked, ok := markLearnedFromAgents(content)
	if !ok {
		return false
	}
	if err := writeKnowHowAtomic(r.path, []byte(marked)); err != nil {
		log.Printf("[agent-activity] %s not marked: %v", r.kind, err)
		return false
	}
	return true
}

// agentCleanupCandidates lists the learned memory and know-how files that
// came from screen activity or from assistant history.
func agentCleanupCandidates() ([]agentCleanupRecord, error) {
	var out []agentCleanupRecord
	memDir, err := memoryDir()
	if err != nil {
		return nil, err
	}
	knowDir, err := knowledgeDir()
	if err != nil {
		return nil, err
	}
	for _, spec := range []struct{ kind, dir string }{{"memory", memDir}, {"knowledge", knowDir}} {
		entries, err := os.ReadDir(spec.dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || isOKFReservedMarkdownFile(e.Name()) {
				continue
			}
			path := filepath.Join(spec.dir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			content := string(data)
			if learnedFromAgents(content) || isUserEditedMemory(content) {
				continue
			}
			if r, ok := agentCleanupRecordFor(spec.kind, e.Name(), path, content); ok {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func agentCleanupRecordFor(kind, name, path, content string) (agentCleanupRecord, bool) {
	r := agentCleanupRecord{kind: kind, id: strings.TrimSuffix(name, ".md"), path: path}
	doc, ok := parseOKFDocument(content)
	if ok {
		r.source = okfString(doc.Frontmatter, "source")
	}
	switch kind {
	case "memory":
		parsed := parseMemoryEntryDocument(content)
		if r.source == "" {
			r.source = parsed.frontmatter.Source
		}
		r.sessionIDs = memoryEvidenceSessionIDsFromDocument(parsed)
	case "knowledge":
		entry := parseKnowledgeFile(name, content)
		r.sessionIDs = entry.SessionIDs
		if r.source == "" && strings.HasPrefix(r.id, "activity-") {
			r.source = "activity"
		}
	}
	switch r.source {
	case "activity":
		return r, len(r.sessionIDs) > 0
	case assistantHistorySource:
		if !ok {
			return r, false
		}
		r.assistant = okfString(doc.Frontmatter, "history_assistant")
		r.project = okfString(doc.Frontmatter, "history_project")
		if r.project == "" {
			if apps := okfStringSlice(doc.Frontmatter, "apps"); len(apps) > 0 {
				r.project = apps[0]
			}
		}
		stamps := okfStringSlice(doc.Frontmatter, "history_sessions_at")
		if len(stamps) == 0 {
			stamps = []string{okfString(doc.Frontmatter, "history_session_at")}
		}
		for _, stamp := range stamps {
			if at, err := time.Parse(time.RFC3339, stamp); err == nil {
				r.sessionsAt = append(r.sessionsAt, at)
			}
		}
		return r, r.assistant != "" && r.project != "" && len(r.sessionsAt) > 0
	}
	return r, false
}

// activitySessionsAllAgent reports whether every cited screen session only
// showed coding agents at work. ok is false when a session is gone.
func (a *App) activitySessionsAllAgent(ids []string) (agent, ok bool) {
	if a.activity == nil || a.activity.store == nil || len(ids) == 0 {
		return false, false
	}
	for _, id := range ids {
		session, err := a.activity.store.GetSessionByID(id)
		if err != nil || session == nil {
			return false, false
		}
		events, err := a.activity.store.GetSessionEvents(id)
		if err != nil {
			return false, false
		}
		if !activitySessionIsAgent(*session, events) {
			return false, true
		}
	}
	return true, true
}

// historyCleanupTarget is one session time a record cites.
type historyCleanupTarget struct {
	at     time.Time
	record *agentCleanupRecord
}

// historyCleanupMatch is the best line found so far for one target.
type historyCleanupMatch struct {
	delta time.Duration
	agent bool
}

// resolveHistoryRecordsAgent finds the session file and point each record's
// time names, and reports whether every cited excerpt was agent work: none
// of the turns the history reader saw there answered a prompt the person
// typed. A record with any cited time it cannot place is left out.
func resolveHistoryRecordsAgent(ctx context.Context, roots assistantHistoryRoots, records []*agentCleanupRecord) map[*agentCleanupRecord]bool {
	oldest := time.Now()
	for _, r := range records {
		for _, at := range r.sessionsAt {
			if at.Before(oldest) {
				oldest = at
			}
		}
	}
	cutoff := oldest.Add(-time.Hour)
	var sessions []assistantHistorySession
	sessions = append(sessions, scanClaudeHistorySessions(roots.Claude, cutoff)...)
	sessions = append(sessions, scanCodexHistorySessions(roots.Codex, cutoff)...)
	resolver := &assistantHistoryRootResolver{home: roots.Home, cache: map[string]string{}}
	byProject := map[string][]int{}
	for i := range sessions {
		root := resolver.rootFor(sessions[i].CWD)
		if root == "" {
			continue
		}
		sessions[i].Root = root
		key := sessions[i].Assistant + "\x00" + filepath.Base(root)
		byProject[key] = append(byProject[key], i)
	}

	// Each file is read once, for every target that may sit in it.
	perFile := map[int][]historyCleanupTarget{}
	for _, r := range records {
		for _, at := range r.sessionsAt {
			for _, i := range byProject[r.assistant+"\x00"+r.project] {
				if sessions[i].At.Add(agentActivityMatchSlack).Before(at) {
					continue // The file was last written before this time.
				}
				perFile[i] = append(perFile[i], historyCleanupTarget{at: at, record: r})
			}
		}
	}
	type key struct {
		record *agentCleanupRecord
		at     time.Time
	}
	best := map[key]historyCleanupMatch{}
	for i, targets := range perFile {
		if ctx.Err() != nil {
			return nil
		}
		matches := matchHistoryFileTargets(sessions[i], targets)
		for j, m := range matches {
			if m.delta > agentActivityMatchSlack {
				continue
			}
			k := key{targets[j].record, targets[j].at}
			if prev, ok := best[k]; !ok || m.delta < prev.delta {
				best[k] = m
			}
		}
	}
	out := map[*agentCleanupRecord]bool{}
	for _, r := range records {
		agent := true
		resolved := true
		for _, at := range r.sessionsAt {
			m, ok := best[key{r, at}]
			if !ok {
				resolved = false
				break
			}
			agent = agent && m.agent
		}
		if resolved {
			out[r] = agent
		}
	}
	return out
}

// matchHistoryFileTargets reads one session file and, for each target time,
// finds the nearest line and whether the excerpt ending there held only
// agent turns. A Codex session is all one origin.
func matchHistoryFileTargets(session assistantHistorySession, targets []historyCleanupTarget) []historyCleanupMatch {
	out := make([]historyCleanupMatch, len(targets))
	for i := range out {
		out[i].delta = time.Duration(1<<62 - 1)
	}
	if len(targets) == 0 {
		return out
	}
	last := targets[0].at
	for _, t := range targets {
		if t.at.After(last) {
			last = t.at
		}
	}
	file, err := os.Open(session.Path)
	if err != nil {
		return out
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 256<<10)
	current := historyOriginUnknown
	if session.Agent {
		current = historyOriginAgent
	}
	// recent holds the origins of the last excerpt-sized run of turns.
	var recent []historyTurnOrigin
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			at, hasAt := historyLineTimestamp(line)
			if session.Assistant == "claude" {
				if origin, ok := claudeHistoryLineOrigin(line); ok {
					current = origin
				}
			}
			if _, ok := parseAssistantHistoryTurn(session.Assistant, line); ok {
				origin := current
				if session.Assistant != "claude" {
					origin = historyOriginHuman
					if session.Agent {
						origin = historyOriginAgent
					}
				}
				recent = append(recent, origin)
				if len(recent) > agentActivityExcerptTurns {
					recent = recent[1:]
				}
			}
			if hasAt {
				for i, t := range targets {
					delta := at.Sub(t.at)
					if delta < 0 {
						delta = -delta
					}
					if delta < out[i].delta {
						out[i] = historyCleanupMatch{delta: delta, agent: historyOriginsAllAgent(recent)}
					}
				}
				if at.After(last.Add(agentActivityMatchSlack)) {
					break
				}
			}
		}
		if err != nil {
			break
		}
	}
	return out
}

// historyOriginsAllAgent reports an excerpt with turns, none the person's.
func historyOriginsAllAgent(origins []historyTurnOrigin) bool {
	if len(origins) == 0 {
		return false
	}
	for _, origin := range origins {
		if origin != historyOriginAgent {
			return false
		}
	}
	return true
}

var historyTimestampKey = []byte(`"timestamp":"`)

// historyLineTimestamp reads a line's top-level "timestamp" without parsing
// the whole line. Both Claude Code and Codex lines carry one.
func historyLineTimestamp(line []byte) (time.Time, bool) {
	i := bytes.Index(line, historyTimestampKey)
	if i < 0 {
		return time.Time{}, false
	}
	rest := line[i+len(historyTimestampKey):]
	end := bytes.IndexByte(rest, '"')
	if end <= 0 {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, string(rest[:end]))
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}
