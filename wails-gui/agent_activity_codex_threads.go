package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Codex archives a thread's session file (compressed, under
// archived_sessions) once it is done, so a record learned from an archived
// thread has no file to match. Codex keeps every thread in its own index,
// state_<n>.sqlite: the working folder, when the thread ran, and how it was
// started. The cleanup reads that index, read-only, to place such records by
// project folder and time window. Only those columns are read, never a
// message.

// codexThreadSpan is one Codex thread from its index.
type codexThreadSpan struct {
	project string // folder name, as the history reader names projects
	root    string // the folder's repository name, when it folds into one
	start   time.Time
	end     time.Time
	agent   bool
}

// codexThreadIndexPath returns the newest state_<n>.sqlite next to the
// sessions folder.
func codexThreadIndexPath(sessionsDir string) string {
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(sessionsDir), "state_*.sqlite"))
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}

// readCodexThreadSpans reads the index. Any error (no index, a changed
// schema) returns nothing, and those records stay as they are.
func readCodexThreadSpans(roots assistantHistoryRoots) []codexThreadSpan {
	path := codexThreadIndexPath(roots.Codex)
	if path == "" {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT cwd, COALESCE(source, ''), COALESCE(originator, ''), created_at_ms, updated_at_ms FROM threads`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	resolver := &assistantHistoryRootResolver{home: roots.Home, cache: map[string]string{}}
	var out []codexThreadSpan
	for rows.Next() {
		var cwd, source, originator string
		var created, updated sql.NullInt64
		if rows.Scan(&cwd, &source, &originator, &created, &updated) != nil || !created.Valid {
			continue
		}
		cwd = strings.TrimSpace(cwd)
		if cwd == "" {
			continue
		}
		end := created.Int64
		if updated.Valid && updated.Int64 > end {
			end = updated.Int64
		}
		span := codexThreadSpan{
			project: filepath.Base(cwd),
			start:   time.UnixMilli(created.Int64),
			end:     time.UnixMilli(end),
			agent:   codexThreadSourceIsAgent(source, originator),
		}
		if root := resolver.rootFor(cwd); root != "" {
			span.root = filepath.Base(root)
		}
		out = append(out, span)
	}
	return out
}

// codexThreadSourceIsAgent applies the session_meta rule to the index's
// columns: exec runs and spawned subagents are agents.
func codexThreadSourceIsAgent(source, originator string) bool {
	line, err := json.Marshal(map[string]any{
		"type":    "session_meta",
		"payload": map[string]any{"originator": originator, "source": json.RawMessage(codexSourceJSON(source))},
	})
	if err != nil {
		return false
	}
	agent, _ := codexHistoryMetaIsAgent(line)
	return agent
}

// codexSourceJSON turns the stored source (a bare name or a JSON object)
// into JSON.
func codexSourceJSON(source string) []byte {
	source = strings.TrimSpace(source)
	if source == "" {
		return []byte(`""`)
	}
	if json.Valid([]byte(source)) && (strings.HasPrefix(source, "{") || strings.HasPrefix(source, `"`)) {
		return []byte(source)
	}
	quoted, _ := json.Marshal(source)
	return quoted
}

// resolveCodexRecordsByThreads places Codex records no session file
// matched: every thread in the record's project whose time window holds the
// record's time is a candidate. A record is resolved only when its
// candidates agree; mixed candidates stay unresolved.
func resolveCodexRecordsByThreads(spans []codexThreadSpan, records []*agentCleanupRecord) map[*agentCleanupRecord]bool {
	out := map[*agentCleanupRecord]bool{}
	for _, r := range records {
		if r.assistant != "codex" {
			continue
		}
		agentAll, resolved := true, true
		for _, at := range r.sessionsAt {
			found, agent, person := false, false, false
			for _, span := range spans {
				if span.project != r.project && span.root != r.project {
					continue
				}
				if at.Before(span.start.Add(-agentActivityMatchSlack)) || at.After(span.end.Add(agentActivityMatchSlack)) {
					continue
				}
				found = true
				if span.agent {
					agent = true
				} else {
					person = true
				}
			}
			if !found || agent == person {
				resolved = false
				break
			}
			agentAll = agentAll && agent
		}
		if resolved {
			out[r] = agentAll
		}
	}
	return out
}
