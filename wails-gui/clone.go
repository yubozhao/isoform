package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wails-gui/internal/platform/cmdutil"
	"wails-gui/internal/platform/shellenv"
	"yansu-agent/ksync"
)

// ── Clone helpers ──

// buildGitProxyURL builds the Git proxy URL for cloning.
// Format: https://<token>@<host>/git/<org>/<product>/<project>.git
// The token is placed in the URL userinfo so the Yansu git proxy can
// authenticate the request during the routing phase.
func buildGitProxyURL(baseURL, token, org, product, project string) string {
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Host != "" {
		scheme := parsed.Scheme
		if scheme == "" {
			scheme = "https"
		}
		return fmt.Sprintf("%s://%s@%s/git/%s/%s/%s.git", scheme, token, parsed.Host, org, product, project)
	}

	host := baseURL
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(host, prefix) {
			host = host[len(prefix):]
			break
		}
	}
	host = strings.TrimRight(host, "/")
	return fmt.Sprintf("https://%s@%s/git/%s/%s/%s.git", token, host, org, product, project)
}

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
	// The proxy URL already contains the auth token in userinfo.
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
		at := strings.LastIndex(repoURL, "@")
		if at >= 0 && at < len(repoURL)-1 {
			hostPath := repoURL[at+1:]
			colon := strings.Index(hostPath, ":")
			if colon > 0 {
				host = strings.TrimSpace(hostPath[:colon])
				if host != "" {
					return host, "22", true
				}
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
//isoform:destination yansu_backend
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

// gitCheckout checks out a branch, fetching from remote if needed.
func gitCheckout(repoDir, branch string) error {
	cmd := cmdutil.GitCommand("checkout", branch)
	cmd.Dir = repoDir
	if err := cmd.Run(); err == nil {
		return nil
	}

	// Fetch from remote and retry
	fetch := cmdutil.GitCommand("fetch", "origin", branch)
	fetch.Dir = repoDir
	if err := fetch.Run(); err != nil {
		return fmt.Errorf("branch %s not found locally or remotely", branch)
	}

	retry := cmdutil.GitCommand("checkout", branch)
	retry.Dir = repoDir
	if err := retry.Run(); err != nil {
		// Create tracking branch
		track := cmdutil.GitCommand("checkout", "-b", branch, "origin/"+branch)
		track.Dir = repoDir
		return track.Run()
	}
	return nil
}

// CloneProject clones a remote project into parentDir/{projectName},
// checks out the coding branch, and creates .something/project.json.
// Returns the full path to the cloned directory.
func (a *App) CloneProject(org, product, projectName, projectUUID, productUUID, codingBranch, parentDir string) (string, error) {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return "", fmt.Errorf("not authenticated")
	}

	// Use default clone directory when parentDir is not specified.
	if parentDir == "" {
		base, err := cloneBaseDir()
		if err != nil {
			return "", fmt.Errorf("failed to resolve default clone directory: %w", err)
		}
		if err := os.MkdirAll(base, 0755); err != nil {
			return "", fmt.Errorf("failed to create clone directory: %w", err)
		}
		parentDir = base
	}

	targetDir := filepath.Join(parentDir, projectName)

	// Check if target already exists
	if _, err := os.Stat(targetDir); err == nil {
		return "", fmt.Errorf("directory already exists: %s", targetDir)
	}

	apiBaseURL := loadAPIBaseURL()
	webBaseURL := loadWebBaseURL()
	proxyURL := buildGitProxyURL(apiBaseURL, token, org, product, projectName)

	log.Printf("Cloning %s/%s/%s into %s", org, product, projectName, targetDir)

	if err := checkGitHostReachable(proxyURL); err != nil {
		log.Printf("Clone failed for %s/%s/%s: preflight check failed: %v", org, product, projectName, err)
		return "", err
	}

	if _, err := gitClone(proxyURL, targetDir, a.cloneProgressEmitter()); err != nil {
		cleanupFailedCloneDir(targetDir)
		log.Printf("Clone failed for %s/%s/%s into %s: %v", org, product, projectName, targetDir, err)
		return "", err
	}

	// Checkout coding branch if specified
	if codingBranch != "" {
		if err := gitCheckout(targetDir, codingBranch); err != nil {
			log.Printf("Warning: could not checkout branch %s: %v", codingBranch, err)
		}
	}

	// Create .something/project.json
	somethingDir := filepath.Join(targetDir, ".something")
	if err := os.MkdirAll(somethingDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create .something directory: %w", err)
	}

	yansuURL := fmt.Sprintf("%s/orgs/%s/products/%s/projects/%s", webBaseURL, org, product, projectName)
	cfg := projectConfig{
		UUID:         projectUUID,
		Name:         projectName,
		Organization: org,
		Product:      product,
		ProductUUID:  productUUID,
		YansuURL:     yansuURL,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal project config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(somethingDir, "project.json"), data, 0644); err != nil {
		return "", fmt.Errorf("failed to write project config: %w", err)
	}

	// Post-clone setup: knowledge capture/sync hook
	postCloneSetup(targetDir)

	// Sync knowledge and context from cloud (fault-tolerant)
	syncAfterClone(apiBaseURL, token, org, product, projectName, targetDir)

	log.Printf("Project cloned to %s", targetDir)
	return targetDir, nil
}

// CloneFromURL clones a git repository from any URL into ~/.yansu-agent/repos/{repo-name}.
// Returns the full path to the cloned directory.
func (a *App) CloneFromURL(gitURL string) (string, error) {
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

	postCloneSetup(targetDir)

	log.Printf("Repository cloned to %s", targetDir)
	return targetDir, nil
}

// ── Knowledge sync ──

// PullKnowledge pulls knowledge and context from cloud for a project.
// Returns a summary map with counts of created/updated/deleted items.
func (a *App) PullKnowledge(projectPath string) (map[string]any, error) {
	k, err := ksync.NewKSyncerFromProject(projectPath)
	if err != nil {
		return nil, fmt.Errorf("init syncer: %w", err)
	}

	ctx := context.Background()
	result := map[string]any{}

	pullResult, err := k.Pull(ctx)
	if err != nil {
		return nil, fmt.Errorf("pull knowledge: %w", err)
	}
	result["knowledge_created"] = len(pullResult.Created)
	result["knowledge_updated"] = len(pullResult.Updated)
	result["knowledge_deleted"] = len(pullResult.Deleted)

	if syncErr := k.SyncContext(ctx); syncErr != nil {
		log.Printf("Warning: context sync failed: %v", syncErr)
	}
	result["context_synced"] = true

	return result, nil
}

// PushKnowledge pushes new-knowledge files to cloud for a project.
// Returns a summary map with counts of created/updated/deleted items.
func (a *App) PushKnowledge(projectPath string) (map[string]any, error) {
	k, err := ksync.NewKSyncerFromProject(projectPath)
	if err != nil {
		return nil, fmt.Errorf("init syncer: %w", err)
	}

	ctx := context.Background()

	pushResult, err := k.Push(ctx)
	if err != nil {
		return nil, fmt.Errorf("push knowledge: %w", err)
	}

	return map[string]any{
		"created": len(pushResult.Created),
		"updated": len(pushResult.Updated),
		"deleted": len(pushResult.Deleted),
		"errors":  pushResult.Errors,
	}, nil
}

// postCloneSetup installs the knowledge capture/sync hook after any git clone.
func postCloneSetup(targetDir string) {
	installGitHook(targetDir)
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

// syncAfterClone performs knowledge/context sync and daemon registration.
// All operations are fault-tolerant — failures are logged but don't fail the clone.
func syncAfterClone(baseURL, token, org, product, projectName, targetDir string) {
	ctx := context.Background()

	k := ksync.NewKSyncer(baseURL, token, org, product, projectName, targetDir)

	if err := k.EnsureDirectories(); err != nil {
		log.Printf("Warning: failed to ensure directories: %v", err)
	}

	if _, err := k.Pull(ctx); err != nil {
		log.Printf("Warning: knowledge pull failed: %v", err)
	}

	if err := k.SyncContext(ctx); err != nil {
		log.Printf("Warning: context sync failed: %v", err)
	}

	if err := k.UpdateIndex(); err != nil {
		log.Printf("Warning: index update failed: %v", err)
	}

	registerWithDaemon(targetDir)
}

//isoform:destination yansu_backend
func registerWithDaemon(projectPath string) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return
	}
	socketPath := filepath.Join(dataRoot, "daemon.sock")

	// Check if socket exists (daemon running)
	if _, err := os.Stat(socketPath); err != nil {
		return
	}

	daemonHTTP := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
		Timeout: 10 * time.Second,
	}

	body, _ := json.Marshal(map[string]string{"project": projectPath})
	resp, err := daemonHTTP.Post("http://unix/register", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("Warning: daemon registration failed: %v", err)
		return
	}
	resp.Body.Close()
	log.Printf("Registered with background sync daemon")
}

// installGitHook installs a post-commit hook for automatic knowledge capture.
func installGitHook(targetDir string) {
	gitDir := filepath.Join(targetDir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return
	}

	hooksDir := filepath.Join(gitDir, "hooks")
	_ = os.MkdirAll(hooksDir, 0755)

	yansuCmd := "yansu"
	if installed := installedYansuCLIPath(); installed != "" {
		yansuCmd = shellenv.ShellQuote(installed)
	}

	hookContent := fmt.Sprintf(`#!/bin/sh
# Isoform Agent - Automatic Knowledge Capture & Sync
# This hook analyzes commits and syncs knowledge automatically

# Run yansu hook and push in background to avoid slowing down commits
(
  # Extract knowledge from commit
  %s hook run 2>/dev/null
  # Push knowledge to cloud
  %s push --knowledge-only 2>/dev/null
) &
`, yansuCmd, yansuCmd)
	_ = os.WriteFile(filepath.Join(hooksDir, "post-commit"), []byte(hookContent), 0755)
}
