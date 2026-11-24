package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"yansu-agent/agent"

	"wails-gui/internal/destinations"
	"wails-gui/storage"
)

// How the app's research runs (Daily News, "New on your topics") reach the
// web, in order of preference:
//
//  1. The user's own Claude Code, when it is signed in: it runs with only its
//     WebSearch tool (no fetch, no shell, no files, no MCP servers, no
//     settings files, hooks or plugins, nothing approved beyond search), in
//     an empty folder, and nothing it does is kept as a session. WebFetch is
//     left out: a fetched link carries data out in its path and query, so a
//     poisoned search result could send the topics to any host.
//  2. The native loop with opencli and the user's signed-in Grok, Gemini or
//     Doubao in Chrome (cron_app_research.go grants exactly that).
//
// Whichever way it ran, the app writes down what the searches returned, from
// the results of the search calls themselves (paired with their call by
// tool-use id), and appends it to the run's result as a research_meta block.
// Whether a search could run, and which links the answer may use, are
// decided from that block, never from what the model says about itself: a
// block the model writes is always removed, and a run without the app's
// block counts as one that could not search.

const (
	researchViaClaudeCode = "claude_code"
	researchViaOpencli    = "opencli"

	researchMetaOpen  = "<research_meta>"
	researchMetaClose = "</research_meta>"

	researchCLITimeout = 8 * time.Minute
	researchProbeFor   = 10 * time.Minute
	// researchProbeNegativeFor is how long a signed-out or failed probe is
	// trusted before it is asked again.
	researchProbeNegativeFor = 45 * time.Second
	researchClaudeModel      = "sonnet"
	researchURLsMax          = 200
)

// researchClaudeBinary is the Claude Code command (a variable for tests).
var researchClaudeBinary = "claude"

// researchMeta is what a research run's tools returned.
type researchMeta struct {
	Via string `json:"via"`
	// Results counts the search calls (WebSearch, or an opencli "ask") that
	// returned something; Failures the ones that errored or were refused.
	// Help, list and any other call count for neither.
	Results  int `json:"results"`
	Failures int `json:"failures"`
	// FailureReason and Denied preserve the tool's failure for the run history
	// and Today. They never come from the assistant's final answer.
	FailureReason string `json:"failure_reason,omitempty"`
	Denied        bool   `json:"denied,omitempty"`
	// URLs are the links that appeared in the search results.
	URLs []string `json:"urls"`
	// Personal says whether the run searched for the user's own topics:
	// "yes" or "no". Missing on runs from before it was recorded, which
	// count as personal.
	Personal string `json:"personal,omitempty"`
}

const (
	researchPersonalYes = "yes"
	researchPersonalNo  = "no"
)

// researchPersonal says whether a Claude Code research run searched for the
// user's own topics: the topic search always does, Daily News when its
// prompt carries the user's interests. Both the dedicated research route
// and the task-scoped scheduled composer use this same classification.
func researchPersonal(jobName, prompt string) string {
	if jobName == topicFindsJobName || strings.Contains(prompt, todayNewsInterestsHeader) {
		return researchPersonalYes
	}
	return researchPersonalNo
}

// searched reports a run in which at least one search returned something.
func (m researchMeta) searched() bool {
	return m.Results > 0
}

// hasURL reports a link that appeared in the run's search results.
func (m researchMeta) hasURL(raw string) bool {
	key := topicLinkKey(raw)
	if key == "" {
		return false
	}
	for _, u := range m.URLs {
		if topicLinkKey(u) == key {
			return true
		}
	}
	return false
}

var researchURLPattern = regexp.MustCompile(`https?://[^\s"'<>\[\]{}|\\^` + "`" + `]+`)

// addResultText records the links in one search result.
func (m *researchMeta) addResultText(text string) {
	for _, u := range researchURLPattern.FindAllString(text, -1) {
		u = trimResearchURL(u)
		if len(m.URLs) >= researchURLsMax {
			return
		}
		m.URLs = append(m.URLs, u)
	}
}

// trimResearchURL drops the punctuation that ends a sentence or a Markdown
// link after a URL, and a closing parenthesis only when the URL opened none
// (so a Wikipedia-style "/Go_(language)" link stays whole).
func trimResearchURL(u string) string {
	for {
		trimmed := strings.TrimRight(u, ".,;:!?*_")
		if strings.HasSuffix(trimmed, ")") && strings.Count(trimmed, ")") > strings.Count(trimmed, "(") {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if trimmed == u {
			return u
		}
		u = trimmed
	}
}

// researchSearchCall reports a tool call that searches: Claude Code's
// WebSearch, or an opencli "ask" to one of the AI search adapters. Help,
// list and every other call are not searches, so their results neither
// show that a search ran nor add links.
func researchSearchCall(toolName string, input json.RawMessage) bool {
	switch toolName {
	case "WebSearch":
		return true
	case "Bash":
		var args struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &args) != nil {
			return false
		}
		words := strings.Fields(args.Command)
		if len(words) < 4 || words[0] != "opencli" || words[2] != "ask" {
			return false
		}
		// "ask -h" prints help; it does not search.
		if words[3] == "-h" || words[3] == "--help" {
			return false
		}
		for _, word := range words[3:] {
			if word == "-h" || word == "--help" {
				// Help for ask is not a search.
				return false
			}
		}
		for _, adapter := range appResearchAdapters {
			if words[1] == adapter {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// addSearchResult records the result of one search call.
func (m *researchMeta) addSearchResult(text string, isError bool) {
	// A result's text can contain anything the web returned, including quoted
	// guard messages. Only the tool's own failed status makes that text a
	// diagnostic whose denial reason we can trust.
	reason := ""
	if isError {
		reason = researchDenialReason(text)
	}
	denied := reason != ""
	if isError || researchResultFailed(text) {
		m.Failures++
		if m.FailureReason == "" || denied && !m.Denied {
			m.FailureReason = agencyBoundedText(firstNonEmpty(reason, text, "the search tool failed"), 512)
		}
		m.Denied = m.Denied || denied
		return
	}
	m.Results++
	m.addResultText(text)
}

// researchFailureMarkers start the first line of a tool result that did not
// search: opencli's error envelope ("ok: false", whatever the code, such as
// BROWSER_CONNECT on a later line), a timeout, or a failed start. Denials are
// deliberately absent: a successful page can quote one, and the tool's own
// failed status supplies their provenance in addSearchResult. Non-zero exits
// and timeouts also come from that status (researchMetaFromItems).
var researchFailureMarkers = []string{
	"ok: false", "BROWSER_CONNECT", "[command timed out]",
	"[execution error:", "Browser Bridge extension not connected",
}

func researchResultFailed(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	first, _, _ := strings.Cut(trimmed, "\n")
	first = strings.TrimSpace(first)
	for _, marker := range researchFailureMarkers {
		if strings.HasPrefix(first, marker) {
			return true
		}
	}
	return strings.HasSuffix(first, "command not found")
}

// researchDenialReason reads a failed tool's or CLI's diagnostic, never a
// successful search result. Guard messages may follow progress output.
func researchDenialReason(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "blocked:") || strings.HasPrefix(lower, "blocked by authority policy") ||
			strings.Contains(lower, "denied permission to execute") || strings.HasPrefix(lower, "permission denied") ||
			strings.HasSuffix(lower, ": permission denied") {
			return line
		}
	}
	return ""
}

// runError turns a denied search into a failed scheduled run. Other tool
// failures retain the existing ok + unavailable state so Today offers its
// connection guidance and stale-run retry. A recovered search stays ok.
func (m researchMeta) runError() error {
	if m.searched() || !m.Denied {
		return nil
	}
	return fmt.Errorf("research denied: %s", firstNonEmpty(m.FailureReason, "permission to search was refused"))
}

func researchRunError(summary string) error {
	_, meta, ok := splitResearchMeta(summary)
	if !ok {
		return nil
	}
	return meta.runError()
}

// withResearchMeta appends the app's record to a run's final answer. Any
// research_meta block in the model's own text is removed first, so the
// model can never write the record.
func withResearchMeta(final string, meta researchMeta) string {
	raw, err := json.Marshal(meta)
	if err != nil {
		return stripResearchMeta(final)
	}
	return strings.TrimSpace(stripResearchMeta(final)) + "\n" + researchMetaOpen + string(raw) + researchMetaClose
}

// stripResearchMeta removes every research_meta block from text.
func stripResearchMeta(text string) string {
	for {
		i := strings.Index(text, researchMetaOpen)
		if i < 0 {
			return text
		}
		j := strings.Index(text[i:], researchMetaClose)
		if j < 0 {
			return text[:i]
		}
		text = text[:i] + text[i+j+len(researchMetaClose):]
	}
}

// splitResearchMeta returns the answer without any record, and the app's
// record when the run carries one (the last block, which the app appended).
// A run without it counts as one that could not search.
func splitResearchMeta(summary string) (string, researchMeta, bool) {
	i := strings.LastIndex(summary, researchMetaOpen)
	if i < 0 {
		return strings.TrimSpace(summary), researchMeta{}, false
	}
	rest := summary[i+len(researchMetaOpen):]
	j := strings.Index(rest, researchMetaClose)
	if j < 0 {
		return stripResearchMeta(summary), researchMeta{}, false
	}
	var meta researchMeta
	if json.Unmarshal([]byte(rest[:j]), &meta) != nil {
		return stripResearchMeta(summary), researchMeta{}, false
	}
	return strings.TrimSpace(stripResearchMeta(summary)), meta, true
}

// researchMetaFromItems records what a native run's searches returned, from
// the stream items the run stored, and returns its final assistant message.
// A result counts only when its tool-use id names a search call the run
// made, and fails when failed names it (the tool's own status: an error, a
// refusal, a timeout or a non-zero exit) or its result reports a failure.
func researchMetaFromItems(items []storage.StreamItemRow, failed map[string]bool) (researchMeta, string) {
	meta := researchMeta{Via: researchViaOpencli, URLs: []string{}}
	final := ""
	searches := map[string]bool{}
	for _, item := range items {
		switch item.Kind {
		case "tool_use":
			if item.ToolUseID != "" && researchSearchCall(item.ToolName, json.RawMessage(item.ToolInput)) {
				searches[item.ToolUseID] = true
			}
		case "tool_result":
			if item.ToolUseID == "" || !searches[item.ToolUseID] {
				continue
			}
			delete(searches, item.ToolUseID)
			meta.addSearchResult(item.Content, failed[item.ToolUseID])
		case "assistant":
			if strings.TrimSpace(item.Content) != "" {
				final = item.Content
			}
		default:
		}
	}
	return meta, final
}

// claudeResearch caches whether Claude Code is signed in.
var claudeResearch struct {
	mu      sync.Mutex
	checked time.Time
	ready   bool
	probing bool
}

// claudeResearchProbe asks the CLI whether it is signed in. It can take up
// to the CLI's sign-in timeout, so it never runs under the cache's lock.
var claudeResearchProbe = func() bool {
	ok, err := agent.ExternalAgentSignedIn(agent.ExternalAgentConfig{Format: "claude-code"})
	return err == nil && ok
}

// claudeResearchReady reports a signed-in Claude Code, probed at most every
// ten minutes. Starting a search uses it; read paths use claudeResearchKnown.
var claudeResearchReady = func() bool {
	claudeResearch.mu.Lock()
	if claudeResearchFresh() {
		ready := claudeResearch.ready
		claudeResearch.mu.Unlock()
		return ready
	}
	claudeResearch.mu.Unlock()
	ok := claudeResearchProbe()
	claudeResearch.mu.Lock()
	claudeResearch.ready = ok
	claudeResearch.checked = time.Now()
	claudeResearch.mu.Unlock()
	return ok
}

// claudeResearchFresh reports a probe recent enough to trust, with the
// lock held: a signed-in result for ten minutes, a signed-out or failed one
// for 45 seconds, so a user who signs in after launch is seen soon.
func claudeResearchFresh() bool {
	if claudeResearch.checked.IsZero() {
		return false
	}
	ttl := researchProbeFor
	if !claudeResearch.ready {
		ttl = researchProbeNegativeFor
	}
	return time.Since(claudeResearch.checked) < ttl
}

// claudeResearchKnown returns the last probe without probing: ready, and
// whether there has been a probe at all. A stale or missing probe is
// refreshed in the background, one at a time, so Settings and Today never
// wait on the CLI.
var claudeResearchKnown = func() (ready, known bool) {
	claudeResearch.mu.Lock()
	ready, known = claudeResearch.ready, !claudeResearch.checked.IsZero()
	start := !claudeResearchFresh() && !claudeResearch.probing
	if start {
		claudeResearch.probing = true
	}
	claudeResearch.mu.Unlock()
	if start {
		go func() {
			claudeResearchReady()
			claudeResearch.mu.Lock()
			claudeResearch.probing = false
			claudeResearch.mu.Unlock()
		}()
	}
	return ready, known
}

// researchScratchDir is the project folder of research runs (Daily News and
// topic finds): a private folder inside the system temp folder rather than
// the temp folder itself, so a run cannot reach other programs' temp files.
func researchScratchDir() string {
	dir := filepath.Join(os.TempDir(), "isoform-research")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[research] scratch folder: %v", err)
		return os.TempDir()
	}
	return dir
}

// researchConfigChangedAt is when the assistant settings last changed: the
// moment research may be starting again.
var researchConfigChangedAt atomic.Int64

// noteResearchConfigChanged forgets the sign-in probe after the assistant
// settings changed, so the next read probes again, and restarts the grace
// for a starting assistant.
func noteResearchConfigChanged() {
	claudeResearch.mu.Lock()
	claudeResearch.checked = time.Time{}
	claudeResearch.ready = false
	claudeResearch.mu.Unlock()
	researchConfigChangedAt.Store(time.Now().UnixMilli())
}

// researchStartGrace is how long an assistant that is set may still be
// starting before Today says the search cannot run.
const researchStartGrace = 2 * time.Minute

// claudeSignedIn is the sign-in state: probed when a search starts, cached
// on read paths.
func claudeSignedIn(probe bool) bool {
	if probe {
		return claudeResearchReady()
	}
	ready, _ := claudeResearchKnown()
	return ready
}

// researchProviderReady reports the native loop can search: the user's own
// key, or Isoform's models served by the sidecar (the default route, and
// the loop Isoform runs itself when the assistant is Claude Code or
// Codex). An external assistant alone is not ready: its sign-in is checked
// on its own, and the sidecar must serve the loop.
func (a *App) researchProviderReady() bool {
	cfg := a.GetAIProviderConfig()
	if cfg.isBYOA() {
		return true
	}
	if cfg.isExternalAgent() || a.managedModelsActive() {
		return a.managedModelsReady()
	}
	return false
}

// researchReady reports a way for a research run to reach the web now: the
// user's signed-in Claude Code, or the native loop with opencli. Starting a
// search uses it, and it may probe the CLI.
func (a *App) researchReady() bool {
	return claudeSignedIn(true) || a.researchProviderReady()
}

// researchReadyCached is researchReady from the cached sign-in, for reads.
func (a *App) researchReadyCached() bool {
	return claudeSignedIn(false) || a.researchProviderReady()
}

// researchConfigured reports that research has a way to run, even while
// it is still starting: an assistant is set (Isoform's own models, the
// user's key or an external CLI), or Claude Code is signed in. Only
// without either does Today ask the user to choose an assistant. It reads
// the cached sign-in only.
func (a *App) researchConfigured() bool {
	cfg := a.GetAIProviderConfig()
	if cfg.isBYOA() || cfg.isExternalAgent() || a.managedModelsActive() {
		return true
	}
	return claudeSignedIn(false)
}

// researchStarting reports an assistant that is set but not ready yet,
// within the grace after launch or a settings change. After it, Today says
// the search cannot run instead of "try again in a moment" forever.
func (a *App) researchStarting() bool {
	if !a.researchConfigured() || a.researchReadyCached() {
		return false
	}
	since := appLaunchedAt.UnixMilli()
	if changed := researchConfigChangedAt.Load(); changed > since {
		since = changed
	}
	return time.Since(time.UnixMilli(since)) < researchStartGrace
}

// researchRouteShown names the route research takes, for Settings, without
// waiting on the CLI: claude_code, opencli (the assistant with opencli in
// Chrome) or none.
func (a *App) researchRouteShown() string {
	if claudeSignedIn(false) {
		return researchViaClaudeCode
	}
	cfg := a.GetAIProviderConfig()
	if cfg.isBYOA() || cfg.isExternalAgent() || a.managedModelsActive() {
		return researchViaOpencli
	}
	return "none"
}

// researchVia names the way the next research run reaches the web.
func researchVia() string {
	if claudeResearchReady() {
		return researchViaClaudeCode
	}
	return researchViaOpencli
}

// researchViaCached is researchVia from the cached sign-in, for reads.
func researchViaCached() string {
	if claudeSignedIn(false) {
		return researchViaClaudeCode
	}
	return researchViaOpencli
}

// researchToolsText says how a research run searches, for the way it runs.
func researchToolsText(via string) string {
	if via == researchViaClaudeCode {
		return "Search the web with the WebSearch tool. There are no other tools: pages cannot be opened, so work from what the searches return and use only links they returned.\n"
	}
	return "Research with opencli via Bash (WebSearch is not available in this build):\n" +
		"- Ask exactly ONE AI source (grok, gemini, or doubao) with its `ask` command — check the adapter's subcommands with `opencli <site> -h` first, and never fan the same question out to multiple AI sites.\n" +
		"- EVERY opencli adapter command MUST include `--site-session ephemeral` so the automated browser window closes itself once the query finishes. Never omit this flag.\n" +
		"- Run each opencli command on its own: no `&&`, `;`, variables, redirects to files or command substitution, and put the whole question in one pair of quotes. Adding ` 2>&1 | head -40` to trim output is fine. Besides `--site-session ephemeral`, only `--timeout`, `--new`, `--model` and `-f` are allowed. Anything else is refused.\n" +
		"- Fetching pages is not available: work from the search answers and the links they give.\n"
}

// claudeResearchArgs are the flags that keep Claude Code to web search
// only: --safe-mode turns off CLAUDE.md, skills, plugins, hooks and MCP
// servers, --restricted ignores the user, project and local settings files
// and drops the code-running tools and WebFetch, and --tools and
// --allowedTools name WebSearch alone.
func claudeResearchArgs(prompt string) []string {
	return []string{
		"-p",
		"--safe-mode",
		"--restricted",
		"--tools", "WebSearch",
		"--allowedTools", "WebSearch",
		"--permission-mode", "dontAsk",
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
		"--no-session-persistence",
		"--output-format", "stream-json", "--verbose",
		"--model", researchClaudeModel,
		prompt,
	}
}

// runClaudeResearch runs one research prompt on Claude Code and returns its
// final answer with the record of what its searches returned.
//
//isoform:destination assistant_search
var runClaudeResearch = func(ctx context.Context, prompt string) (string, researchMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, researchCLITimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "isoform-research-")
	if err != nil {
		return "", researchMeta{}, err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, researchClaudeBinary, claudeResearchArgs(prompt)...)
	cmd.Dir = dir
	cmd.Stdin = nil
	cmd.Env = researchCLIEnv(dir)
	cmd.WaitDelay = 2 * time.Second
	// Its own process group, so a timeout or quitting the app ends anything
	// it started too, not only the claude process.
	setResearchProcessGroup(cmd)
	cmd.Cancel = func() error { return killResearchProcess(cmd) }
	// Stdout goes through a pipe the command copies into, so WaitDelay
	// also bounds it: once the CLI exits (or the context ends), anything it
	// started that still holds stdout gets two seconds, then the pipe
	// closes and the read below ends.
	stdout, stdoutW := io.Pipe()
	cmd.Stdout = stdoutW
	var stderr strings.Builder
	cmd.Stderr = &stderr
	launch, _ := ctx.Value(chatWebLaunchKey{}).(*chatWebLaunch)
	start := startResearchProcess
	if launch != nil {
		start = launch.start
	}
	if err := start(cmd); err != nil {
		stdoutW.Close()
		return "", researchMeta{}, err
	}
	detail := researchViaClaudeCode
	if launch != nil {
		detail = destinations.ChatSearchDetail
	}
	destinations.RecordSearch(detail)
	if launch != nil {
		launch.started()
	}
	defer forgetResearchProcess(cmd)
	waited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		stdoutW.Close()
		waited <- err
	}()
	final, meta, streamErr := parseClaudeResearchStream(stdout)
	// Read what is left (after a line too long to parse, say), so the CLI
	// never blocks on a full pipe.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := <-waited
	// A child can hold stdout open after a successful CLI exit. WaitDelay
	// closes that pipe; it is not a non-zero exit and must not discard the
	// completed answer.
	if errors.Is(waitErr, exec.ErrWaitDelay) && final != "" && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		waitErr = nil
	}
	// Inspect the full diagnostic before bounding what is saved. A denial may
	// follow lengthy progress output, and stderr can explain a generic stream
	// error even when the stream also reports a final answer.
	msg := strings.TrimSpace(stderr.String())
	var denialErr error
	if reason := researchDenialReason(msg); reason != "" {
		meta.Denied = true
		meta.FailureReason = agencyBoundedText(reason, 512)
		if !meta.searched() || waitErr != nil || streamErr != nil {
			denialErr = fmt.Errorf("research denied: %s", meta.FailureReason)
		}
		msg = reason
	}
	if ctx.Err() != nil {
		return "", meta, fmt.Errorf("the search took too long: %w", ctx.Err())
	}
	// Claude can exit non-zero during cleanup after returning a complete
	// answer. Keep that answer only when a search actually succeeded and no
	// denial or terminal stream failure contradicts it.
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) && strings.TrimSpace(final) != "" && meta.searched() && !meta.Denied && streamErr == nil {
		waitErr = nil
	}
	if waitErr != nil {
		waitErr = fmt.Errorf("claude: %w\n%s", waitErr, agencyBoundedText(msg, 300))
	}
	if err := errors.Join(streamErr, waitErr, denialErr); err != nil {
		return "", meta, err
	}
	return final, meta, nil
}

// researchProcs are the research CLIs running now, so quitting the app can
// end them before it exits.
var researchProcs struct {
	mu       sync.Mutex
	stopping bool
	cmds     map[*exec.Cmd]struct{}
}

var errResearchStopping = errors.New("the app is quitting")

// startResearchProcess starts cmd unless the app is quitting, and keeps it
// on the list shutdown ends.
func startResearchProcess(cmd *exec.Cmd) error {
	researchProcs.mu.Lock()
	defer researchProcs.mu.Unlock()
	if researchProcs.stopping {
		return errResearchStopping
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if researchProcs.cmds == nil {
		researchProcs.cmds = map[*exec.Cmd]struct{}{}
	}
	researchProcs.cmds[cmd] = struct{}{}
	trackIsoformClaudeWorker(cmd.Process.Pid, time.Now())
	return nil
}

func forgetResearchProcess(cmd *exec.Cmd) {
	researchProcs.mu.Lock()
	defer researchProcs.mu.Unlock()
	delete(researchProcs.cmds, cmd)
	if cmd.Process != nil {
		forgetIsoformClaudeWorker(cmd.Process.Pid)
	}
}

// stopResearchProcesses ends every running research CLI with what it
// started, and refuses new ones. Shutdown calls it first, before anything
// that could make the watchdog exit the app with them still running.
func stopResearchProcesses() {
	researchProcs.mu.Lock()
	defer researchProcs.mu.Unlock()
	researchProcs.stopping = true
	for cmd := range researchProcs.cmds {
		_ = killResearchProcess(cmd)
	}
}

// researchCLIEnv is the CLI's environment: the user's, as their own Claude
// Code sees it (the assistant runs it the same way, external_runner.go),
// minus the app's YANSU_ variables, and run from the empty folder.
func researchCLIEnv(dir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "YANSU_") || name == "PWD" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "PWD="+filepath.Clean(dir))
}

// parseClaudeResearchStream reads Claude Code's stream-json events: the
// results of its search calls (paired by tool-use id) become the record,
// the result event is the final answer. A result event that reports an
// error, or a stream that cannot be read, is an error, so a run that broke
// off never reads as "nothing new".
func parseClaudeResearchStream(r io.Reader) (string, researchMeta, error) {
	meta := researchMeta{Via: researchViaClaudeCode, URLs: []string{}}
	final := ""
	var resultErr error
	searches := map[string]bool{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		var parts []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		}
		switch event.Type {
		case "assistant":
			if json.Unmarshal(event.Message.Content, &parts) != nil {
				continue
			}
			for _, part := range parts {
				if part.Type == "tool_use" && part.ID != "" && researchSearchCall(part.Name, part.Input) {
					searches[part.ID] = true
				}
			}
		case "user":
			if json.Unmarshal(event.Message.Content, &parts) != nil {
				continue
			}
			for _, part := range parts {
				if part.Type != "tool_result" || part.ToolUseID == "" || !searches[part.ToolUseID] {
					continue
				}
				delete(searches, part.ToolUseID)
				meta.addSearchResult(toolResultText(part.Content), part.IsError)
			}
		case "result":
			final = event.Result
			resultErr = nil
			if event.IsError || (event.Subtype != "" && event.Subtype != "success") {
				final = ""
				diagnostic := event.Result
				if reason := researchDenialReason(diagnostic); reason != "" {
					meta.Denied = true
					meta.FailureReason = agencyBoundedText(reason, 512)
					diagnostic = reason
				}
				resultErr = fmt.Errorf("claude: the run ended with %s\n%s", firstNonEmpty(event.Subtype, "an error"), agencyBoundedText(diagnostic, 512))
			}
		default:
		}
	}
	if err := scanner.Err(); err != nil {
		return "", meta, fmt.Errorf("claude: the output could not be read: %w", err)
	}
	return final, meta, resultErr
}

// toolResultText flattens a tool result's content (a string, or a list of
// text blocks).
func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, block := range blocks {
			b.WriteString(block.Text)
			b.WriteString("\n")
		}
		return b.String()
	}
	return string(raw)
}

// runResearchJob runs a research schedule on the user's Claude Code when it
// is signed in. handled is false when the native loop should run it.
func (a *App) runResearchJob(ctx context.Context, job *storage.CronJobRow, via string) (summary string, handled bool, err error) {
	if !isAppResearchJob(job) || via != researchViaClaudeCode {
		return "", false, nil
	}
	if isTopicSearchJob(job) && !a.topicSearchMayLeave() {
		// Asked again just before the words leave.
		return "", true, errTopicSearchPaused
	}
	state := jobRowToState(job)
	prompt, ok := a.appResearchPrompt(&state, via)
	if !ok {
		return "", true, errors.New("the search has no topics to look for")
	}
	final, meta, err := runClaudeResearch(ctx, prompt)
	meta.Personal = researchPersonal(job.Name, prompt)
	if err != nil {
		if reason := researchDenialReason(err.Error()); reason != "" {
			meta.Denied = true
			meta.FailureReason = agencyBoundedText(reason, 512)
			if meta.Failures == 0 {
				meta.Failures++
			}
		}
	}
	if err == nil {
		err = meta.runError()
	}
	if err != nil {
		log.Printf("[research] Claude Code search did not finish: %v", err)
		return withResearchMeta("", meta), true, err
	}
	log.Printf("[research] %s ran on Claude Code: %d results, %d failures", job.Name, meta.Results, meta.Failures)
	return withResearchMeta(final, meta), true, nil
}

// cronToolFailures holds, per cron session tag, the tool-use ids whose tool
// failed in the run going on now, from the native loop's own status. The
// stored stream items keep only the text.
var cronToolFailures struct {
	mu    sync.Mutex
	byTag map[string]map[string]bool
}

func noteCronToolFailure(tag, toolUseID string) {
	if tag == "" || toolUseID == "" {
		return
	}
	cronToolFailures.mu.Lock()
	defer cronToolFailures.mu.Unlock()
	if cronToolFailures.byTag == nil {
		cronToolFailures.byTag = map[string]map[string]bool{}
	}
	if cronToolFailures.byTag[tag] == nil {
		cronToolFailures.byTag[tag] = map[string]bool{}
	}
	cronToolFailures.byTag[tag][toolUseID] = true
}

// takeCronToolFailures returns and forgets the failures noted for tag.
func takeCronToolFailures(tag string) map[string]bool {
	cronToolFailures.mu.Lock()
	defer cronToolFailures.mu.Unlock()
	failed := cronToolFailures.byTag[tag]
	delete(cronToolFailures.byTag, tag)
	return failed
}
