package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"wails-gui/internal/platform/shellenv"
)

// Setup's Your AI screen beyond the happy path. After an install or a sign-in
// it re-scans with a fresh PATH and no cached answer, checks the sign-in, and
// makes one tiny real call through the gateway, so a card turns on only when
// the assistant answers. Every failure carries a code the screen maps to its
// real next step, and the real error goes to the log with [assistant-setup].

// AssistantSetupResult is what the setup install and check return. They never
// return an error: Code says what is wrong, "" when nothing is.
type AssistantSetupResult struct {
	ID        string `json:"id"`
	Installed bool   `json:"installed"`
	SignedIn  bool   `json:"signedIn"`
	// Tested is true when the check made the test call; Working when it
	// answered (an assistant at its limit still counts as working).
	Tested  bool   `json:"tested"`
	Working bool   `json:"working"`
	Version string `json:"version"`
	Plan    string `json:"plan"`
	// Code is one of the assistantSetup* codes below.
	Code string `json:"code"`
	// Command is the exact command for Terminal, when one is the next step.
	Command string `json:"command,omitempty"`
	// Detail is the real error, trimmed.
	Detail string `json:"detail,omitempty"`
}

const (
	assistantSetupNeedsNode      = "needsNode"
	assistantSetupInstallDenied  = "installDenied"
	assistantSetupInstallTimeout = "installTimeout"
	assistantSetupInstallFailed  = "installFailed"
	assistantSetupNotFound       = "notFound"
	assistantSetupSignedOut      = "signedOut"
	assistantSetupTestSignedOut  = "testSignedOut"
	assistantSetupTestLimit      = "testLimit"
	assistantSetupTestFailed     = "testFailed"
	assistantSetupGatewayDown    = "gatewayDown"
)

// assistantCheckTimeout bounds one re-scan probe. A CLI's first run after an
// install can be slow, so it is longer than the launch probe's.
const assistantCheckTimeout = 15 * time.Second

// assistantTestTimeout bounds the one-token test call.
const assistantTestTimeout = 45 * time.Second

// assistantGatewayWaitTimeout covers the magpie start deadline and the launch
// work that can precede the first attach.
const assistantGatewayWaitTimeout = 20 * time.Second

// Seams; tests replace them.
var (
	assistantInstallRun = func(ctx context.Context, command string) (string, error) {
		return loginShellOutput(ctx, command)
	}
	assistantNPMAvailable     = func() bool { return loginShellHas("npm") }
	assistantCurlAvailable    = func() bool { return loginShellHas("curl") }
	assistantRefreshPATH      = refreshAssistantPATH
	assistantProbe            = probeSetupAgent
	assistantInstallTimeout   = cliInstallTimeout
	assistantGatewayWait      = assistantGatewayWaitTimeout
	assistantGatewayRetry     = 200 * time.Millisecond
	assistantLoginShellLocate = func(ctx context.Context, id string) string {
		if runtime.GOOS == "windows" {
			out, _ := loginShellOutput(ctx, "where "+id+" & npm prefix -g")
			return strings.TrimSpace(out)
		}
		out, _ := loginShellOutput(ctx, "command -v "+id+"; npm prefix -g")
		return strings.TrimSpace(out)
	}
	openAssistantURL = func(a *App, url string) {
		if a.ctx != nil {
			wailsRuntime.BrowserOpenURL(a.ctx, url)
		}
	}
)

func assistantInstallCommand(id string) string { return "npm install -g " + installPkg(id) }

// assistantNativeInstallCommand is each tool's own installer, from its docs:
// code.claude.com/docs/en/setup and github.com/openai/codex. Both install to
// ~/.local/bin without Node.js or sudo; Codex's would ask questions on a
// terminal, which CODEX_NON_INTERACTIVE turns off.
func assistantNativeInstallCommand(id string) string {
	if runtime.GOOS == "windows" {
		return assistantInstallCommand(id)
	}
	if id == "codex" {
		return "curl -fsSL https://chatgpt.com/codex/install.sh | CODEX_NON_INTERACTIVE=1 sh"
	}
	return "curl -fsSL https://claude.ai/install.sh | bash"
}

// refreshAssistantPATH re-reads the login shell's PATH and adds the folders a
// fresh install may use: npm's global bin, the folders holding the assistant
// and node, and the native installers' ~/.local/bin and ~/.claude/local.
func refreshAssistantPATH(id string) {
	added := shellenv.RefreshPATH(id, "node", "npm")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := loginShellOutput(ctx, "npm prefix -g"); err == nil {
		if runtime.GOOS == "windows" {
			shellenv.AppendPATH(lastLine(out))
		}
		if prefix := lastLine(out); prefix != "" && shellenv.AppendPATH(filepath.Join(prefix, "bin")) {
			added = append(added, filepath.Join(prefix, "bin"))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, dir := range []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, ".claude", "local")} {
			if shellenv.AppendPATH(dir) {
				added = append(added, dir)
			}
		}
	}
	if len(added) > 0 {
		log.Printf("[assistant-setup] PATH gained %s", strings.Join(added, ", "))
	}
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func signInLine(id string) string {
	command, _ := signInCommand(id)
	return strings.Join(command, " ")
}

func validAssistantID(id string) bool { return id == "claude" || id == "codex" }

// SetupInstallAssistant installs Claude Code or Codex, then re-scans with a
// fresh PATH and checks the sign-in. It runs the tool's own native installer
// first, which needs no Node.js and lands in ~/.local/bin; when that fails and
// npm is there, npm is the fallback. "Needs Node.js" is reported only when the
// only route left needs Node.
func (a *App) SetupInstallAssistant(id string) AssistantSetupResult {
	id = strings.ToLower(strings.TrimSpace(id))
	result := AssistantSetupResult{ID: id}
	if !validAssistantID(id) {
		result.Code, result.Detail = assistantSetupInstallFailed, fmt.Sprintf("unknown assistant %q", id)
		return result
	}
	// The Terminal fallback is always the native installer: it needs no Node.
	result.Command = assistantNativeInstallCommand(id)
	native := runtime.GOOS != "windows" && assistantCurlAvailable()
	npm := assistantNPMAvailable()
	if !native && !npm {
		result.Code = assistantSetupNeedsNode
		log.Printf("[assistant-setup] %s install: neither curl nor npm in the login shell", id)
		return result
	}
	var failed AssistantSetupResult
	if native {
		if code, detail := runAssistantInstall(id, assistantNativeInstallCommand(id)); code == "" {
			return a.afterAssistantInstall(id)
		} else {
			failed = AssistantSetupResult{ID: id, Code: code, Detail: detail, Command: result.Command}
		}
	}
	if npm {
		code, detail := runAssistantInstall(id, assistantInstallCommand(id))
		if code == "" {
			return a.afterAssistantInstall(id)
		}
		if failed.Code == "" {
			failed = AssistantSetupResult{ID: id, Code: code, Detail: detail, Command: assistantInstallCommand(id)}
		} else {
			failed.Detail += "\n" + detail
		}
	}
	return failed
}

// runAssistantInstall runs one install route and sorts a failure into a code.
func runAssistantInstall(id, command string) (code, detail string) {
	ctx, cancel := context.WithTimeout(context.Background(), assistantInstallTimeout)
	defer cancel()
	log.Printf("[assistant-setup] %s install: %s", id, command)
	out, err := assistantInstallRun(ctx, command)
	if err == nil {
		return "", ""
	}
	detail = tailText(out, 600)
	if detail == "" {
		detail = err.Error()
	}
	lower := strings.ToLower(out)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		code = assistantSetupInstallTimeout
	case strings.Contains(lower, "eacces") || strings.Contains(lower, "eperm") || strings.Contains(lower, "permission denied"):
		code = assistantSetupInstallDenied
	default:
		code = assistantSetupInstallFailed
	}
	log.Printf("[assistant-setup] %s install failed (%s): %s: %v: %s", id, code, command, err, tailText(out, 1200))
	return code, detail
}

// afterAssistantInstall re-scans after a route that reported success.
func (a *App) afterAssistantInstall(id string) AssistantSetupResult {
	checked := a.checkAssistantSetup(id, false)
	if !checked.Installed {
		checked.Command = assistantNativeInstallCommand(id)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		checked.Detail = assistantLoginShellLocate(ctx, id)
		cancel()
		log.Printf("[assistant-setup] %s installed but not found; login shell says %q; PATH=%s", id, checked.Detail, os.Getenv("PATH"))
	}
	return checked
}

// SetupCheckAssistant re-scans one assistant with a fresh PATH and no cached
// answer. With test it also makes the one-token call through the gateway.
func (a *App) SetupCheckAssistant(id string, test bool) AssistantSetupResult {
	id = strings.ToLower(strings.TrimSpace(id))
	if !validAssistantID(id) {
		return AssistantSetupResult{ID: id, Code: assistantSetupNotFound, Detail: fmt.Sprintf("unknown assistant %q", id)}
	}
	return a.checkAssistantSetup(id, test)
}

func (a *App) checkAssistantSetup(id string, test bool) AssistantSetupResult {
	assistantRefreshPATH(id)
	ctx, cancel := context.WithTimeout(context.Background(), assistantCheckTimeout)
	row := assistantProbe(ctx, id)
	cancel()
	row.ID = id
	row.ProbedAt = time.Now().UTC().Format(time.RFC3339Nano)
	a.initAgentProbe()
	a.setupAgents.setRow(row)
	result := AssistantSetupResult{ID: id, Installed: row.Installed, SignedIn: row.SignedIn, Version: row.Version, Plan: row.Plan}
	switch {
	case !row.Installed:
		result.Code = assistantSetupNotFound
		result.Command = assistantNativeInstallCommand(id)
	case !row.SignedIn:
		result.Code = assistantSetupSignedOut
		result.Command = signInLine(id)
	case test:
		result.Tested = true
		result.Code, result.Detail = a.testAssistantCall(id)
		result.Working = result.Code == "" || result.Code == assistantSetupTestLimit
		if result.Code == assistantSetupTestSignedOut {
			result.Command = signInLine(id)
		}
	}
	if result.Code != "" {
		log.Printf("[assistant-setup] %s check: %s (installed=%t signedIn=%t) %s", id, result.Code, result.Installed, result.SignedIn, result.Detail)
	} else {
		log.Printf("[assistant-setup] %s check: ok (version %q, tested=%t)", id, result.Version, result.Tested)
	}
	return result
}

// assistantGateway is where the test call goes, how its catalog is read, and
// how a new sign-in is brought into it; tests replace it.
var assistantGateway = func(a *App) (baseURL string, models func(context.Context) ([]string, error), reload func(context.Context)) {
	if a.magpie == nil || !a.magpie.Healthy() {
		return "", nil, nil
	}
	return a.magpie.BaseURL(), a.magpie.listModels, func(ctx context.Context) {
		if err := a.bootstrapManagedModels(ctx); err != nil {
			log.Printf("[assistant-setup] gateway reload: %v", err)
		}
	}
}

// awaitAssistantGateway gives the launch-side magpie attach time to finish.
// The final empty result means the gateway stayed down for the whole bound.
func (a *App) awaitAssistantGateway() (baseURL string, models func(context.Context) ([]string, error), reload func(context.Context)) {
	timer := time.NewTimer(assistantGatewayWait)
	ticker := time.NewTicker(assistantGatewayRetry)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		baseURL, models, reload = assistantGateway(a)
		if baseURL != "" && models != nil {
			return baseURL, models, reload
		}
		select {
		case <-timer.C:
			return "", nil, nil
		case <-ticker.C:
		}
	}
}

// testAssistantCall sends one token through the gateway to the assistant's
// own subscription, the way chat reaches it. It prefers the light model so
// the test barely touches the plan.
func (a *App) testAssistantCall(id string) (code, detail string) {
	base, models, reload := a.awaitAssistantGateway()
	if base == "" || models == nil {
		return assistantSetupGatewayDown, "the model gateway is not running"
	}
	ctx, cancel := context.WithTimeout(context.Background(), assistantTestTimeout)
	defer cancel()
	model, err := assistantTestModel(ctx, id, models)
	if model == "" && err == nil && reload != nil {
		// A sign-in made a moment ago may not be in the catalog yet.
		reload(ctx)
		model, err = assistantTestModel(ctx, id, models)
	}
	if err != nil {
		return assistantSetupGatewayDown, err.Error()
	}
	if model == "" {
		return assistantSetupTestSignedOut, "the gateway lists no " + id + " models"
	}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 1,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return assistantSetupTestFailed, err.Error()
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", magpieLocalAPIKeyPlaceholder)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return assistantSetupTestFailed, model + ": no answer in " + assistantTestTimeout.String()
		}
		return assistantSetupGatewayDown, err.Error()
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 600))
	detail = fmt.Sprintf("%s: HTTP %d %s", model, resp.StatusCode, strings.TrimSpace(string(snippet)))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return "", ""
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return assistantSetupTestSignedOut, detail
	case resp.StatusCode == http.StatusTooManyRequests:
		return assistantSetupTestLimit, detail
	default:
		return assistantSetupTestFailed, detail
	}
}

// assistantTestModel picks the catalog model the test uses: the light one
// when the gateway serves it, else the provider's first.
func assistantTestModel(ctx context.Context, id string, models func(context.Context) ([]string, error)) (string, error) {
	ids, err := models(ctx)
	if err != nil {
		return "", err
	}
	have := make(map[string]bool, len(ids))
	for _, m := range ids {
		have[m] = true
	}
	if id == "claude" {
		if m := firstPresent(have, "claude/claude-haiku-5-5", "claude/claude-haiku-4-5"); m != "" {
			return m, nil
		}
	} else if m := firstWithPrefix(ids, "codex/", "-luna"); m != "" {
		return m, nil
	}
	return firstWithPrefix(ids, id+"/", ""), nil
}

// OpenAssistantInstallTerminal opens Terminal on the tool's native installer,
// for when it cannot finish from here (permissions, a slow network, a PATH
// Isoform does not see). It needs no Node.js. Setup keeps re-checking while it
// runs.
func (a *App) OpenAssistantInstallTerminal(id string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if !validAssistantID(id) {
		return fmt.Errorf("unknown assistant %q", id)
	}
	if runtime.GOOS == "windows" {
		return openWindowsSetupTerminal([]string{"npm", "install", "-g", installPkg(id)})
	}
	command := assistantNativeInstallCommand(id)
	log.Printf("[assistant-setup] %s: opening Terminal on %s", id, command)
	return openTerminalCommand(id+"-install", []string{"/bin/sh", "-c", command})
}

// OpenNodeDownload opens the Node.js download page; setup checks for npm
// again when its window comes back.
func (a *App) OpenNodeDownload() {
	log.Printf("[assistant-setup] opening the Node.js download page")
	openAssistantURL(a, "https://nodejs.org/en/download")
}

// setRow replaces one agent's row with a fresh probe and publishes it.
func (c *agentProbeCache) setRow(row SetupAgent) {
	c.mu.Lock()
	for i := range c.result.Agents {
		if c.result.Agents[i].ID == row.ID {
			c.result.Agents[i] = row
		}
	}
	c.result.ProbedAt = row.ProbedAt
	c.mu.Unlock()
	c.publish()
}
