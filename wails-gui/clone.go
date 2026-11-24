package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wails-gui/internal/platform/cmdutil"
)

// ── Clone helpers ──

func isLocalGitHost(repoURL string) bool {
	parsed, err := url.Parse(repoURL)
	if err != nil {
		return strings.Contains(repoURL, "localhost") || strings.Contains(repoURL, "127.0.0.1")
	}

	switch parsed.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// repoNameFromURL extracts the repository name from a git URL.
// Handles both HTTPS (https://github.com/org/repo.git) and
// SSH (git@github.com:org/repo.git) formats.
func repoNameFromURL(gitURL string) string {
	u := strings.TrimSuffix(gitURL, ".git")
	if idx := strings.LastIndex(u, "/"); idx >= 0 {
		return u[idx+1:]
	}
	if idx := strings.LastIndex(u, ":"); idx >= 0 {
		return u[idx+1:]
	}
	return u
}

const (
	gitHostReachabilityProbe = 10 * time.Second
)

// gitCloneTimeoutFromEnv resolves clone timeout from YANSU_GIT_CLONE_TIMEOUT_MS if present.
func gitCloneTimeoutFromEnv(fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv("YANSU_GIT_CLONE_TIMEOUT_MS"))
	if raw == "" {
		return fallback
	}

	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

// gitCloneTimeoutFromConfig resolves clone timeout from config.json if present.
func gitCloneTimeoutFromConfig(fallback time.Duration) time.Duration {
	ms := loadConfig().GitCloneTimeoutMS
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func gitCloneTimeout() time.Duration {
	timeout := gitCloneTimeoutFromConfig(0)
	return gitCloneTimeoutFromEnv(timeout)
}

// defaultGitCloneTimeout is the fallback when no user config is set.
// Large repositories can take a long time to clone, so the default is
// generous. Users can still override via YANSU_GIT_CLONE_TIMEOUT_MS or
// config.json → gitCloneTimeoutMs.
const defaultGitCloneTimeout = 2 * time.Hour

// cloneProgress is called with phase name and percentage (0-100).
type cloneProgress func(phase string, percent int)

// gitClone runs git clone with a timeout. Disables SSL verification for localhost.
// GIT_TERMINAL_PROMPT=0 prevents git from hanging on hidden credential prompts (Windows).
// If onProgress is non-nil, it receives real-time progress updates parsed from git output.
func gitClone(repoURL, targetDir string, onProgress cloneProgress) (string, error) {
	timeout := gitCloneTimeout()
	if timeout <= 0 {
		timeout = defaultGitCloneTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Git config flags applied to every clone:
	// -c credential.helper=  — disables Git Credential Manager (GCM) on Windows
	// -c http.version=1.1    — forces HTTP/1.1; HTTP/2 multiplexing causes
	//                          "stream was not closed cleanly" errors on large repos
	// -c http.postBuffer=...  — 512MB buffer for large pack uploads/downloads
	// GIT_TERMINAL_PROMPT=0  — disables terminal-based prompts
	// GCM_INTERACTIVE=never  — disables GCM GUI dialogs

	gitCfg := []string{
		"-c", "credential.helper=",
		"-c", "http.version=1.1",
		"-c", "http.postBuffer=524288000",
	}
	var cmd *exec.Cmd
	if isLocalGitHost(repoURL) {
		args := append(gitCfg, "-c", "http.sslVerify=false", "clone", "--progress", repoURL, targetDir)
		cmd = cmdutil.GitCommandContext(ctx, args...)
	} else {
		args := append(gitCfg, "clone", "--progress", repoURL, targetDir)
		cmd = cmdutil.GitCommandContext(ctx, args...)
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never",
	)

	// Pipe stdin from empty reader to prevent console allocation on Windows.
	cmd.Stdin = bytes.NewReader(nil)

	// Stream git output to the log in real-time so progress is visible in
	// ~/.yansu-agent/yansu-gui.log, while also capturing it for error reporting.
	var buf bytes.Buffer
	logWriter := &logLineWriter{prefix: "[git-clone]", onProgress: onProgress}
	cmd.Stdout = io.MultiWriter(&buf, logWriter)
	cmd.Stderr = io.MultiWriter(&buf, logWriter)

	log.Printf("[git-clone] exec: git clone %s -> %s", redactToken(repoURL), targetDir)

	// Use StartWithJobObject so that all child processes spawned by git
	// (e.g. git-index-pack during "Resolving deltas") are contained in the
	// same Windows Job Object, preventing them from creating visible console
	// windows.
	cleanup, err := cmdutil.StartWithJobObject(cmd)
	if err != nil {
		log.Printf("[git-clone] failed to start: %v", err)
		return buf.String(), friendlyCloneError(err, buf.String(), ctx.Err(), timeout)
	}
	defer cleanup()

	err = cmd.Wait()
	if err != nil {
		// Git may exit with status 1 due to non-fatal issues (e.g. HTTP/2
		// stream errors) even though the clone completed successfully. If the
		// output shows the checkout finished, treat it as success.
		output := buf.String()
		if cloneOutputLooksComplete(output) {
			log.Printf("[git-clone] git exited with %v but output shows clone completed — treating as success", err)
			return output, nil
		}
		log.Printf("[git-clone] failed: %v", err)
		return output, friendlyCloneError(err, output, ctx.Err(), timeout)
	}
	log.Printf("[git-clone] success: %s", targetDir)
	return buf.String(), nil
}

// cloneReachabilityTarget resolves host/port for a lightweight reachability probe.
// Returns ok=false when preflight should be skipped (e.g. local path or malformed input).
func cloneReachabilityTarget(repoURL string) (host string, port string, ok bool) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return "", "", false
	}

	// Support SCP-style SSH remotes, e.g. git@github.com:org/repo.git.
	if !strings.Contains(repoURL, "://") {
		// Git also accepts host:path without a user name.
		authority, _, found := strings.Cut(repoURL, ":")
		if found && !strings.ContainsAny(authority, "/\\") {
			_, host, hasUser := strings.Cut(authority, "@")
			if !hasUser {
				host = authority
			}
			if host != "" && (hasUser || len(host) > 1) {
				return host, "22", true
			}
		}
	}

	parsed, err := url.Parse(repoURL)
	if err != nil {
		return "", "", false
	}

	host = parsed.Hostname()
	if host == "" {
		return "", "", false
	}

	port = parsed.Port()
	if port == "" {
		switch parsed.Scheme {
		case "http":
			port = "80"
		case "ssh":
			port = "22"
		default:
			port = "443"
		}
	}

	return host, port, true
}

// checkGitHostReachable performs a quick TCP dial to verify the git host is reachable
// before starting a potentially long git clone operation.
//
//isoform:destination metadata
func checkGitHostReachable(repoURL string) error {
	host, port, ok := cloneReachabilityTarget(repoURL)
	if !ok {
		return nil
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), gitHostReachabilityProbe)
	if err != nil {
		return fmt.Errorf("server %s is not reachable: %w", host, err)
	}
	conn.Close()
	return nil
}

// friendlyCloneError converts raw git clone errors into user-friendly messages.
func friendlyCloneError(err error, output string, ctxErr error, timeout time.Duration) error {
	out := strings.ToLower(output)

	errorKind := "default"
	// Only treat as timeout if the context deadline was actually exceeded AND
	// git was killed by the context (signal: killed). If git exited on its own
	// (exit status N) after the deadline, it finished — don't mask the real error.
	if ctxErr == context.DeadlineExceeded && !strings.Contains(err.Error(), "exit status") {
		errorKind = "timeout"
	} else if strings.Contains(out, "could not resolve host") {
		errorKind = "dns"
	} else if strings.Contains(out, "couldn't connect to server") || strings.Contains(out, "failed to connect") {
		errorKind = "connect"
	} else if strings.Contains(out, "authentication failed") || strings.Contains(out, "403") || strings.Contains(out, "401") {
		errorKind = "auth"
	} else if strings.Contains(out, "repository not found") || strings.Contains(out, "404") {
		errorKind = "not_found"
	}

	switch errorKind {
	case "timeout":
		return fmt.Errorf(
			"clone timed out after %s — the server may be unreachable or the repository is very large (set YANSU_GIT_CLONE_TIMEOUT_MS to increase timeout)",
			timeout.Round(time.Second),
		)
	case "dns":
		return fmt.Errorf("DNS lookup failed — check your network connection and server URL")
	case "connect":
		return fmt.Errorf("cannot connect to the git server — check your network and ensure the server is running")
	case "auth":
		return fmt.Errorf("authentication failed — your token may be expired, try logging out and back in")
	case "not_found":
		return fmt.Errorf("repository not found — verify the organization, product, and project names are correct")
	default:
		return fmt.Errorf("git clone failed: %w", err)
	}
}

// CloneProject is retained for callers of the retired project proxy.
func (a *App) CloneProject(org, product, projectName, projectUUID, productUUID, codingBranch, parentDir string) (string, error) {
	return "", errNoYansuAccount
}

const retiredGitDomain = "yansu.ai"

func validateCloneURL(gitURL string) error {
	host, _, _ := cloneReachabilityTarget(gitURL)
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == retiredGitDomain || strings.HasSuffix(host, "."+retiredGitDomain) {
		return errNoYansuAccount
	}
	return nil
}

// CloneFromURL clones a git repository from any URL into ~/.yansu-agent/repos/{repo-name}.
// Returns the full path to the cloned directory.
func (a *App) CloneFromURL(gitURL string) (string, error) {
	if err := validateCloneURL(gitURL); err != nil {
		return "", err
	}
	repoName := repoNameFromURL(gitURL)
	if repoName == "" {
		return "", fmt.Errorf("could not determine repository name from URL: %s", gitURL)
	}

	cloneBase, err := cloneBaseDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve clone base directory: %w", err)
	}

	targetDir := filepath.Join(cloneBase, repoName)

	// Check if target already exists
	if _, err := os.Stat(targetDir); err == nil {
		return "", fmt.Errorf("directory already exists: %s", targetDir)
	}

	if err := os.MkdirAll(cloneBase, 0755); err != nil {
		return "", fmt.Errorf("failed to create clone base directory: %w", err)
	}

	log.Printf("Cloning %s into %s", gitURL, targetDir)

	if err := checkGitHostReachable(gitURL); err != nil {
		log.Printf("Clone failed for %s: preflight check failed: %v", gitURL, err)
		return "", err
	}

	if _, err := gitClone(gitURL, targetDir, a.cloneProgressEmitter()); err != nil {
		cleanupFailedCloneDir(targetDir)
		log.Printf("Clone failed for %s into %s: %v", gitURL, targetDir, err)
		return "", err
	}

	log.Printf("Repository cloned to %s", targetDir)
	return targetDir, nil
}

// ── Knowledge sync ──

// PullKnowledge pulls knowledge and context from cloud for a project.
// Returns a summary map with counts of created/updated/deleted items.
func (a *App) PullKnowledge(projectPath string) (map[string]any, error) {
	return nil, errNoYansuAccount
}

// PushKnowledge pushes new-knowledge files to cloud for a project.
// Returns a summary map with counts of created/updated/deleted items.
func (a *App) PushKnowledge(projectPath string) (map[string]any, error) {
	return nil, errNoYansuAccount
}

// cloneBaseDir returns the configured (or default) directory for cloned repositories.
func cloneBaseDir() (string, error) {
	if root := loadConfig().RepoRoot; root != "" {
		return root, nil
	}
	return defaultCloneBaseDir()
}

// defaultCloneBaseDir returns the fallback clone directory: ~/.yansu-agent/repos
func defaultCloneBaseDir() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "repos"), nil
}

func cleanupFailedCloneDir(targetDir string) {
	targetDir = strings.TrimSpace(targetDir)
	if targetDir == "" || targetDir == string(filepath.Separator) {
		return
	}
	if err := os.RemoveAll(targetDir); err != nil {
		log.Printf("Warning: failed to clean up partial clone directory %s: %v", targetDir, err)
	}
}

// cloneOutputLooksComplete returns true if the git clone output indicates that
// the checkout phase finished successfully. Git may still exit with status 1
// due to non-fatal HTTP/2 stream errors even when the repo is fully cloned.
func cloneOutputLooksComplete(output string) bool {
	return strings.Contains(output, "Updating files: 100%") ||
		strings.Contains(output, "Checking out files: 100%")
}

// logLineWriter writes each line of output to log.Printf with a prefix.
// It also parses git progress lines (e.g. "Receiving objects: 45% (45/100)")
// and forwards them to onProgress if set.
type logLineWriter struct {
	prefix     string
	buf        []byte
	onProgress cloneProgress
}

// gitProgressRe matches lines like "Receiving objects:  45% (45/100), 1.23 MiB"
var gitProgressRe = regexp.MustCompile(`^((?:remote: )?\w[\w ]+):\s+(\d+)%`)

func (w *logLineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			// Git progress uses \r without \n — flush those too.
			if cr := bytes.LastIndexByte(w.buf, '\r'); cr >= 0 {
				line := strings.TrimSpace(string(w.buf[:cr+1]))
				if line != "" {
					log.Printf("%s %s", w.prefix, line)
					w.parseProgress(line)
				}
				w.buf = w.buf[cr+1:]
			}
			break
		}
		line := strings.TrimSpace(string(w.buf[:idx]))
		if line != "" {
			log.Printf("%s %s", w.prefix, line)
			w.parseProgress(line)
		}
		w.buf = w.buf[idx+1:]
	}
	return len(p), nil
}

func (w *logLineWriter) parseProgress(line string) {
	if w.onProgress == nil {
		return
	}
	m := gitProgressRe.FindStringSubmatch(line)
	if m == nil {
		return
	}
	phase := strings.TrimPrefix(m[1], "remote: ")
	pct, _ := strconv.Atoi(m[2])
	w.onProgress(phase, pct)
}

// redactToken replaces the token in a git proxy URL for safe logging.
// e.g. https://tok_abc123@host/... → https://***@host/...
func redactToken(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User == nil {
		return rawURL
	}
	parsed.User = url.User("***")
	return parsed.String()
}

// cloneProgressEmitter returns a cloneProgress callback that emits Wails
// "clone:progress" events so the frontend can display real-time progress.
func (a *App) cloneProgressEmitter() cloneProgress {
	return func(phase string, percent int) {
		if a.ctx != nil {
			a.emitBrowserEvent("clone:progress", map[string]any{
				"phase":   phase,
				"percent": percent,
			})
		}
	}
}
