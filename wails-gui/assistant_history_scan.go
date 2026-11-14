package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// assistant_history_scan.go is pass 1 of the assistant history: local files
// only, no model. It finds Claude Code and Codex session files touched in the
// last 30 days, reads each one's working folder from its first lines, folds
// worktrees into their repository, and counts sessions per project. The file's
// modification time is the session's last activity, which is cheaper than
// reading to the end of a long session.

const (
	assistantHistoryWindow     = 30 * 24 * time.Hour
	assistantHistoryWeek       = 7 * 24 * time.Hour
	assistantHistoryTwoWeeks   = 14 * 24 * time.Hour
	assistantHistoryMaxProject = 10
	// The working folder is in the first lines of each file; a bounded read
	// keeps pass 1 quick on very large session files.
	assistantHistoryHeadLines = 200
)

// assistantHistoryRoots are the folders pass 1 reads. Tests point them at a
// temporary home.
type assistantHistoryRoots struct {
	Home   string
	Claude string
	Codex  string
}

// assistantHistoryRootsFromEnv honors CLAUDE_CONFIG_DIR and CODEX_HOME, the
// same overrides the rest of the app reads.
func assistantHistoryRootsFromEnv() (assistantHistoryRoots, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return assistantHistoryRoots{}, err
	}
	claude := filepath.Join(home, ".claude")
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		claude = dir
	}
	codex := filepath.Join(home, ".codex")
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		codex = dir
	}
	return assistantHistoryRoots{
		Home:   home,
		Claude: filepath.Join(claude, "projects"),
		Codex:  filepath.Join(codex, "sessions"),
	}, nil
}

// assistantHistorySession is one session file in the window.
type assistantHistorySession struct {
	Assistant string
	Path      string
	CWD       string
	Root      string
	At        time.Time
	// Agent is a session an agent started: its first prompt is not the
	// person's (see assistant_history_origin.go).
	Agent bool
}

// assistantHistoryScan is pass 1's output. Sessions are kept in memory for
// pass 2 and are never written to the cache.
type assistantHistoryScan struct {
	Assistants []string
	Projects   []HistoryProject
	Sessions   map[string][]assistantHistorySession
	ScannedAt  time.Time
}

// scanAssistantHistory runs pass 1 for the given folders at the given time.
func scanAssistantHistory(roots assistantHistoryRoots, now time.Time) assistantHistoryScan {
	cutoff := now.Add(-assistantHistoryWindow)
	var found []assistantHistorySession
	found = append(found, scanClaudeHistorySessions(roots.Claude, cutoff)...)
	found = append(found, scanCodexHistorySessions(roots.Codex, cutoff)...)

	resolver := &assistantHistoryRootResolver{home: roots.Home, cache: map[string]string{}}
	projects := map[string]*HistoryProject{}
	sessions := map[string][]assistantHistorySession{}
	seenAssistant := map[string]bool{}
	for _, session := range found {
		root := resolver.rootFor(session.CWD)
		if root == "" || (roots.Home != "" && root == filepath.Clean(roots.Home)) {
			// A session started in the home folder itself is not a project.
			continue
		}
		session.Root = root
		id := assistantHistoryProjectID(root)
		project := projects[id]
		if project == nil {
			project = &HistoryProject{ID: id, Name: filepath.Base(root), Path: root, Assistants: []string{}}
			projects[id] = project
		}
		if !session.Agent && !containsString(project.Assistants, session.Assistant) {
			project.Assistants = append(project.Assistants, session.Assistant)
		}
		sessions[id] = append(sessions[id], session)
		age := now.Sub(session.At)
		if session.Agent {
			// Agent sessions are context, never the person's own work.
			if age <= assistantHistoryTwoWeeks {
				project.AgentSessions14d++
			}
			continue
		}
		project.human = true
		seenAssistant[session.Assistant] = true
		if age <= assistantHistoryWeek {
			project.SessionsThisWeek++
		}
		if age <= assistantHistoryTwoWeeks {
			project.Sessions14d++
		}
		if ms := session.At.UnixMilli(); ms > project.LastAt {
			project.LastAt = ms
		}
	}

	out := assistantHistoryScan{
		Assistants: []string{},
		Projects:   []HistoryProject{},
		Sessions:   sessions,
		ScannedAt:  now,
	}
	for _, name := range []string{"claude", "codex"} {
		if seenAssistant[name] {
			out.Assistants = append(out.Assistants, name)
		}
	}
	for id, project := range projects {
		if !project.human {
			// Only agents worked here: not one of the person's projects.
			delete(sessions, id)
			continue
		}
		sort.SliceStable(project.Assistants, func(i, j int) bool {
			return assistantHistoryOrder(project.Assistants[i]) < assistantHistoryOrder(project.Assistants[j])
		})
		out.Projects = append(out.Projects, *project)
	}
	sort.SliceStable(out.Projects, func(i, j int) bool {
		if out.Projects[i].Sessions14d != out.Projects[j].Sessions14d {
			return out.Projects[i].Sessions14d > out.Projects[j].Sessions14d
		}
		if out.Projects[i].LastAt != out.Projects[j].LastAt {
			return out.Projects[i].LastAt > out.Projects[j].LastAt
		}
		return out.Projects[i].ID < out.Projects[j].ID
	})
	for id := range sessions {
		list := sessions[id]
		sort.SliceStable(list, func(i, j int) bool { return list[i].At.After(list[j].At) })
	}
	if len(out.Projects) > assistantHistoryMaxProject {
		out.Projects = out.Projects[:assistantHistoryMaxProject]
	}
	return out
}

// assistantHistoryOrder keeps the assistants in the order the contract lists.
func assistantHistoryOrder(name string) int {
	switch name {
	case "claude":
		return 0
	case "codex":
		return 1
	default:
		return 2
	}
}

// assistantHistoryProjectID is a stable hash of the project's path.
func assistantHistoryProjectID(path string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return hex.EncodeToString(sum[:])[:16]
}

// scanClaudeHistorySessions reads ~/.claude/projects/<encoded>/<uuid>.jsonl.
// Subagent transcripts sit one level deeper and are not separate sessions.
func scanClaudeHistorySessions(dir string, cutoff time.Time) []assistantHistorySession {
	var out []assistantHistorySession
	projectDirs, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, projectDir := range projectDirs {
		if !projectDir.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(dir, projectDir.Name()))
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(dir, projectDir.Name(), file.Name())
			info, err := file.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				continue
			}
			cwd := readClaudeHistoryCWD(path)
			if cwd == "" {
				continue
			}
			out = append(out, assistantHistorySession{Assistant: "claude", Path: path, CWD: cwd, At: info.ModTime(), Agent: readAssistantHistorySessionAgent("claude", path)})
		}
	}
	return out
}

// scanCodexHistorySessions reads ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl.
func scanCodexHistorySessions(dir string, cutoff time.Time) []assistantHistorySession {
	var out []assistantHistorySession
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			return nil
		}
		cwd := readCodexHistoryCWD(path)
		if cwd != "" {
			out = append(out, assistantHistorySession{Assistant: "codex", Path: path, CWD: cwd, At: info.ModTime(), Agent: readAssistantHistorySessionAgent("codex", path)})
		}
		return nil
	})
	return out
}

type claudeHistoryHeadLine struct {
	CWD string `json:"cwd"`
}

type codexHistoryHeadLine struct {
	Type    string `json:"type"`
	Payload struct {
		CWD string `json:"cwd"`
	} `json:"payload"`
}

// readClaudeHistoryCWD returns the first working folder in the file's head.
func readClaudeHistoryCWD(path string) string {
	return readHistoryHead(path, func(line []byte) string {
		var row claudeHistoryHeadLine
		if json.Unmarshal(line, &row) != nil {
			return ""
		}
		return strings.TrimSpace(row.CWD)
	})
}

// readCodexHistoryCWD returns the working folder from the session_meta line.
func readCodexHistoryCWD(path string) string {
	return readHistoryHead(path, func(line []byte) string {
		var row codexHistoryHeadLine
		if json.Unmarshal(line, &row) != nil || row.Type != "session_meta" {
			return ""
		}
		return strings.TrimSpace(row.Payload.CWD)
	})
}

// readHistoryHead scans at most assistantHistoryHeadLines lines and returns
// the first non-empty value the parser finds.
func readHistoryHead(path string, parse func([]byte) string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64<<10)
	for i := 0; i < assistantHistoryHeadLines; i++ {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if value := parse(line); value != "" {
				return value
			}
		}
		if err != nil {
			return ""
		}
	}
	return ""
}

// assistantHistoryRootResolver folds a working folder into its repository:
// Claude Code worktrees under <repo>/.claude/worktrees/<name>, and git
// worktrees whose .git file points into <repo>/.git/worktrees/<name>.
type assistantHistoryRootResolver struct {
	home  string
	cache map[string]string
}

func (r *assistantHistoryRootResolver) rootFor(cwd string) string {
	cwd = filepath.Clean(strings.TrimSpace(cwd))
	if cwd == "" || cwd == "." {
		return ""
	}
	if root, ok := r.cache[cwd]; ok {
		return root
	}
	root := r.resolve(cwd)
	r.cache[cwd] = root
	return root
}

func (r *assistantHistoryRootResolver) resolve(cwd string) string {
	if i := strings.Index(cwd, string(filepath.Separator)+".claude"+string(filepath.Separator)+"worktrees"+string(filepath.Separator)); i > 0 {
		return cwd[:i]
	}
	home := ""
	if r.home != "" {
		home = filepath.Clean(r.home)
	}
	// Walk up to the repository root, but never past the home folder: a dotfile
	// repository at home would otherwise claim every project under it.
	dir := cwd
	for {
		if dir == home || dir == string(filepath.Separator) || dir == "." || dir == "" {
			break
		}
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if info.IsDir() {
				return dir
			}
			if root := gitWorktreeMainRoot(gitPath); root != "" {
				return root
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return cwd
}

// gitWorktreeMainRoot reads a worktree's .git file ("gitdir: <repo>/.git/worktrees/<name>")
// and returns the repository's main folder.
func gitWorktreeMainRoot(gitFile string) string {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	marker := string(filepath.Separator) + ".git" + string(filepath.Separator) + "worktrees" + string(filepath.Separator)
	if i := strings.Index(gitdir, marker); i > 0 {
		return gitdir[:i]
	}
	return ""
}

// assistantHistoryTurn is one user or assistant message from a session.
type assistantHistoryTurn struct {
	Role string
	Text string
}

// assistantHistoryOriginTurn is a turn with the origin of the prompt it
// answers, for the filter in readAssistantHistoryTurns.
type assistantHistoryOriginTurn struct {
	turn   assistantHistoryTurn
	origin historyTurnOrigin
}

type claudeHistoryTurnLine struct {
	Type    string `json:"type"`
	IsMeta  bool   `json:"isMeta"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type codexHistoryTurnLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"payload"`
}

// assistantHistoryTailBytes bounds how much of a session pass 2 reads: the
// end of the file is where the latest work is.
const assistantHistoryTailBytes = 256 << 10

// readAssistantHistoryTurns returns the user and assistant text at the end of
// a session file, in order, with secret-looking lines already dropped.
func readAssistantHistoryTurns(session assistantHistorySession) []assistantHistoryTurn {
	file, err := os.Open(session.Path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if info.Size() > assistantHistoryTailBytes {
		start = info.Size() - assistantHistoryTailBytes
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	if start > 0 {
		// The first line of a tail read is usually cut in half; drop it.
		_, _ = reader.ReadBytes('\n')
	}
	if session.Agent && session.Assistant != "claude" {
		// A Codex exec or subagent session: no prompt in it is the person's.
		return nil
	}
	var tagged []assistantHistoryOriginTurn
	current := historyOriginUnknown
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if session.Assistant == "claude" {
				if origin, ok := claudeHistoryLineOrigin(line); ok {
					current = origin
				}
			}
			if turn, ok := parseAssistantHistoryTurn(session.Assistant, line); ok {
				tagged = append(tagged, assistantHistoryOriginTurn{turn: turn, origin: current})
			}
		}
		if err != nil {
			break
		}
	}
	// Turns before the first prompt in the tail answer a prompt further up
	// the file; the session's own start stands in for it.
	before := historyOriginHuman
	if session.Agent {
		before = historyOriginAgent
	}
	var turns []assistantHistoryTurn
	for _, t := range tagged {
		origin := t.origin
		if origin == historyOriginUnknown {
			origin = before
		}
		if origin == historyOriginHuman {
			turns = append(turns, t.turn)
		}
	}
	return turns
}

// claudeHistoryLineOrigin classifies a Claude Code line that opens a prompt.
func claudeHistoryLineOrigin(line []byte) (historyTurnOrigin, bool) {
	if !bytes.Contains(line, []byte(`"user"`)) {
		return historyOriginUnknown, false
	}
	var row claudeHistoryTurnLine
	if json.Unmarshal(line, &row) != nil || row.Type != "user" {
		return historyOriginUnknown, false
	}
	return claudeHistoryPromptOrigin(line, row.IsMeta, row.Message.Content, claudeHistoryText(row.Message.Content))
}

func parseAssistantHistoryTurn(assistant string, line []byte) (assistantHistoryTurn, bool) {
	switch assistant {
	case "claude":
		var row claudeHistoryTurnLine
		if json.Unmarshal(line, &row) != nil || row.IsMeta {
			return assistantHistoryTurn{}, false
		}
		if row.Type != "user" && row.Type != "assistant" {
			return assistantHistoryTurn{}, false
		}
		text := claudeHistoryText(row.Message.Content)
		return cleanAssistantHistoryTurn(row.Type, text)
	case "codex":
		var row codexHistoryTurnLine
		if json.Unmarshal(line, &row) != nil || row.Type != "response_item" || row.Payload.Type != "message" {
			return assistantHistoryTurn{}, false
		}
		if row.Payload.Role != "user" && row.Payload.Role != "assistant" {
			return assistantHistoryTurn{}, false
		}
		var parts []string
		for _, part := range row.Payload.Content {
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, part.Text)
			}
		}
		return cleanAssistantHistoryTurn(row.Payload.Role, strings.Join(parts, "\n"))
	default:
		return assistantHistoryTurn{}, false
	}
}

// claudeHistoryText reads message.content, which is a string or a list of
// blocks; only text blocks count (tool results and images do not).
func claudeHistoryText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// assistantHistoryTurnRunes caps one message in the excerpt.
const assistantHistoryTurnRunes = 400

func cleanAssistantHistoryTurn(role, text string) (assistantHistoryTurn, bool) {
	clean, dropped := redactAssistantHistoryText(text)
	if dropped > 0 {
		// The line was a credential; keep what is left of the message.
		clean = strings.TrimSpace(clean)
	}
	clean = strings.Join(strings.Fields(clean), " ")
	if clean == "" {
		return assistantHistoryTurn{}, false
	}
	runes := []rune(clean)
	if len(runes) > assistantHistoryTurnRunes {
		clean = string(runes[:assistantHistoryTurnRunes]) + "..."
	}
	return assistantHistoryTurn{Role: role, Text: clean}, true
}

// readAssistantHistoryInstructions returns the project's CLAUDE.md or AGENTS.md
// (trimmed and redacted) with its modification time. Empty when neither exists.
func readAssistantHistoryInstructions(root string) (name, text string, at time.Time) {
	for _, candidate := range []string{"CLAUDE.md", "AGENTS.md"} {
		path := filepath.Join(root, candidate)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := readFileHead(path, 6<<10)
		if err != nil {
			continue
		}
		clean, _ := redactAssistantHistoryText(string(data))
		return candidate, strings.TrimSpace(clean), info.ModTime()
	}
	return "", "", time.Time{}
}

func readFileHead(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit))
}
