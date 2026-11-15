package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/platform/cmdutil"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// aiProviderHTTPClient is shared across model-listing and probe requests so the
// underlying connection pool is reused instead of allocating a fresh client
// (and transport) per call. The 30s timeout is a hard cap on top of each
// request's context (unifying the former per-call 20s/30s clients).
var aiProviderHTTPClient = &http.Client{Timeout: 30 * time.Second}

// byoaEntitled reports whether the user may use a bring-your-own provider.
// The Yansu AI Gateway has been retired (paid users refunded), so BYOA/BYOK
// is the only AI path for everyone — this is now unconditionally true. The
// function is kept (rather than inlined) because it is the single named
// chokepoint referenced by buildAgentRunner, SaveAIProviderConfig, and
// TestAIProvider; collapsing it later is a separate cleanup.
func (a *App) byoaEntitled() bool {
	return true
}

// IsBYOAEntitled exposes the entitlement to the frontend. With the Yansu
// gateway retired, BYOA/BYOK is unconditionally available, so this is now
// always true. Kept for the settings surface and browser RPC allowlist that
// still reference it; the frontend stops gating on it in the same change.
func (a *App) IsBYOAEntitled() bool {
	return a.byoaEntitled()
}

// DetectedAgent describes an external-agent CLI found on the user's PATH.
// Used by the retirement banner and the AI Provider settings to recommend
// Claude Code / Codex when they are already installed.
type DetectedAgent struct {
	Provider string `json:"provider"` // "claude-code" | "codex"
	Command  string `json:"command"`  // resolved binary name (claude | codex) or full path
	Found    bool   `json:"found"`    // the binary resolves on PATH
}

// AIReady reports whether the user has a genuinely usable AI path configured
// — the single source of truth for the retirement banner's visibility. True
// when EITHER:
//   - an external-agent CLI (Claude Code / Codex) is selected AND its binary
//     resolves on PATH, OR
//   - an API-key BYOK provider (anthropic/openai) is selected with BOTH a
//     non-empty API key AND a non-empty model.
//
// The banner disappears as
// soon as this returns true; it reappears if the user clears/invalidates
// their config.
//
// In managed mode readiness comes from the magpie routing groups instead:
// the chat group must have at least one member the sidecar can serve.
func (a *App) AIReady() bool {
	cfg := loadAIProviderConfig()
	if a.managedModelsActive() {
		return a.managedModelsReady()
	}
	return cfg.aiReady()
}

// aiReady is the pure (no App receiver) readiness check shared by AIReady
// and any future caller. Kept on the config type because it reads only the
// persisted config.
func (c AIProviderConfig) aiReady() bool {
	// External agent: selected + binary on PATH. We resolve via LookPath
	// (cheap, no subprocess) so a "selected but not installed" config does
	// not falsely read as ready.
	if c.isExternalAgent() {
		bin := strings.TrimSpace(c.Command)
		if bin == "" {
			switch strings.ToLower(strings.TrimSpace(c.Provider)) {
			case "codex":
				bin = "codex"
			default:
				bin = "claude"
			}
		}
		_, err := exec.LookPath(bin)
		return err == nil
	}
	// API-key BYOK: anthropic/openai + non-empty key + non-empty model.
	p := strings.ToLower(strings.TrimSpace(c.Provider))
	if p != "anthropic" && p != "openai" {
		return false
	}
	return strings.TrimSpace(c.APIKey) != "" && strings.TrimSpace(c.Model) != ""
}

// GetAIProviderConfig returns the persisted BYOA provider configuration
// for the settings UI.
func (a *App) GetAIProviderConfig() AIProviderConfig {
	return loadAIProviderConfig()
}

// SetCachedAIProviderModels persists the model list fetched via "Fetch models"
// so the dropdowns repopulate on reopen without a re-fetch. It updates ONLY the
// CachedModels field on the existing config — no validation, no agent restart —
// so it is safe to call the moment a fetch succeeds, independent of Save. The
// cache is a pure UI convenience the runner never reads; the rest of the
// persisted config is left untouched.
func (a *App) SetCachedAIProviderModels(models []string) error {
	_, err := updateAIProviderConfig(func(cfg *AIProviderConfig) error {
		cfg.CachedModels = models
		return nil
	})
	return err
}

// SaveAIProviderConfig validates, persists, and applies the BYOA provider
// configuration. A non-gateway provider is validated (the same probe the
// "Test connection" button runs) BEFORE anything is written — if validation
// fails the config is NOT saved, so a broken endpoint/key/CLI can't be
// applied. After persisting, the agent is restarted synchronously so an
// agent-init failure surfaces to the caller too.
func (a *App) SaveAIProviderConfig(cfg AIProviderConfig) error {
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	cfg.PresetID = strings.ToLower(strings.TrimSpace(cfg.PresetID))
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.LightModel = strings.TrimSpace(cfg.LightModel)
	cfg.Command = strings.TrimSpace(cfg.Command)

	cfg.Provider = cfg.normalizedProvider()
	if cfg.AdvancedProvider == "" {
		cfg.AdvancedProvider = loadAIProviderConfig().AdvancedProvider
	}
	// Older settings clients omit the file-only assistant budget. Saving an
	// unrelated provider setting must preserve a configured positive ceiling.
	if cfg.AssistantMaxBudgetUSD == 0 {
		cfg.AssistantMaxBudgetUSD = loadAIProviderConfig().AssistantMaxBudgetUSD
	}

	// Managed mode is always allowed without validation: magpie checks its own
	// accounts and keys. An advanced provider is probed before anything is saved.
	if cfg.normalizedProvider() != aiProviderManaged {
		// BYOA / BYOK is a paid feature.
		if !a.byoaEntitled() {
			return fmt.Errorf("bring-your-own provider requires a paid plan — upgrade in Settings > Subscription")
		}
		// Validate the provider actually works before persisting. Block the
		// save on failure so an invalid config never gets applied.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := a.probeAIProvider(ctx, cfg); err != nil {
			return fmt.Errorf("connection test failed — not saved: %v", err)
		}
	}

	if err := saveAIProviderConfig(cfg); err != nil {
		return err
	}

	// Notify the retirement banner (and any other config watcher) to re-check
	// readiness now that a new config is on disk. Emitted before the restart
	// so the banner disappears even if the agent fails to (re)initialize — the
	// config is what determines readiness, not the runner's live state.
	noteResearchConfigChanged()
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "ai:config-changed")
	}

	// Restart synchronously so an agent-init failure (e.g. the CLI binary
	// can't be launched) is reported back instead of being silently logged.
	if _, err := a.RestartAgent(); err != nil {
		return fmt.Errorf("provider saved but the agent failed to start: %v", err)
	}

	return nil
}

// probeAIProvider performs one minimal validation of a non-gateway provider
// config: a 1-token completion for API providers, or a `--version` check for
// external-agent CLIs. Returns nil on success. Shared by SaveAIProviderConfig
// (pre-save gate) and TestAIProvider (the "Test connection" button).
//
// NOTE: for external agents this only confirms the binary is installed and
// runnable — it does not verify the CLI is logged in (that would require a
// real, billable run).
func (a *App) probeAIProvider(ctx context.Context, cfg AIProviderConfig) error {
	provider := cfg.normalizedProvider()
	switch provider {
	case "openai", "anthropic":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return fmt.Errorf("API key is required")
		}
		if strings.TrimSpace(cfg.Model) == "" {
			return fmt.Errorf("model is required")
		}
		if provider == "openai" {
			return probeOpenAIProvider(ctx, cfg)
		}
		return probeAnthropicProvider(ctx, cfg)
	case "claude-code", "codex":
		return probeExternalAgent(ctx, provider, cfg.Command)
	default:
		return fmt.Errorf("provider %q cannot be tested", provider)
	}
}

// TestAIProviderResult is returned by TestAIProvider so the settings UI can
// show whether a transient probe call to the user's endpoint succeeded.
type TestAIProviderResult struct {
	Success   bool   `json:"success"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

// TestAIProvider validates a BYOA configuration by issuing one minimal,
// non-streaming completion against the user's endpoint. The config is NOT
// persisted — this is for validating settings input before saving.
func (a *App) TestAIProvider(cfg AIProviderConfig) TestAIProviderResult {
	provider := cfg.normalizedProvider()
	model := strings.TrimSpace(cfg.Model)
	res := TestAIProviderResult{Provider: provider, Model: model}

	// BYOA / BYOK validation is part of the paid configuration flow. Gate it
	// the same way as SaveAIProviderConfig so the entitlement check can't be
	// sidestepped via the test path.
	if provider != aiProviderManaged && !a.byoaEntitled() {
		res.Error = "bring-your-own provider requires a paid plan — upgrade in Settings > Subscription"
		return res
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	err := a.probeAIProvider(ctx, cfg)
	res.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Success = true
	return res
}

// BuildProviderTestCurl returns a copy-paste curl command that reproduces the
// EXACT request TestAIProvider issues, so a user can run it in a terminal and
// see the full response when the in-app probe surfaces only an opaque status
// (e.g. an upstream 500 with an empty body, which carries no detail to show).
// -i prints the response status + headers + body. The key is never written
// into the command: it reads $API_KEY, which the UI tells the user to set
// first, so copying, pasting or screenshotting the text leaks nothing. Returns
// "" for providers without an HTTP probe (yansu / external agents).
func (a *App) BuildProviderTestCurl(cfg AIProviderConfig) string {
	provider := cfg.normalizedProvider()
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	model := strings.TrimSpace(cfg.Model)
	msgs := []map[string]any{{"role": "user", "content": "hi"}}

	switch provider {
	case "anthropic":
		if base == "" {
			base = "https://api.anthropic.com"
		}
		// Anthropic requires max_tokens, so the probe (and this curl) send it.
		body, _ := json.Marshal(map[string]any{
			"model": model, "max_tokens": 1, "messages": msgs,
		})
		return strings.Join([]string{
			"curl -i -X POST " + shellSingleQuote(base+"/v1/messages"),
			"  -H " + shellSingleQuote("content-type: application/json"),
			"  -H " + providerTestCurlKeyHeader("x-api-key: "),
			"  -H " + shellSingleQuote("anthropic-version: 2023-06-01"),
			"  -d " + shellSingleQuote(string(body)),
		}, " \\\n")
	case "openai":
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		// No max_tokens: matches the probe and the runtime client, which never
		// send an output cap on the OpenAI-compatible path (some gateways reject
		// it with an opaque 500).
		body, _ := json.Marshal(map[string]any{"model": model, "messages": msgs})
		return strings.Join([]string{
			"curl -i -X POST " + shellSingleQuote(base+"/chat/completions"),
			"  -H " + shellSingleQuote("content-type: application/json"),
			"  -H " + providerTestCurlKeyHeader("authorization: Bearer "),
			"  -d " + shellSingleQuote(string(body)),
		}, " \\\n")
	default:
		return ""
	}
}

// providerTestCurlKeyHeader is a header whose value ends in the API key,
// double-quoted so the shell expands $API_KEY when the user runs it.
func providerTestCurlKeyHeader(prefix string) string {
	return `"` + prefix + `$API_KEY"`
}

// shellSingleQuote wraps s in single quotes for safe POSIX-shell pasting,
// escaping any embedded single quote as:
//
//	'\''
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ListAIProviderModels fetches the available model ids from a BYOA API
// provider's model-listing endpoint (OpenAI-compatible: GET {base}/models;
// Anthropic-compatible: GET {base}/v1/models). The desktop UI uses this to
// offer a model dropdown. Best-effort: the caller falls back to manual model
// entry when this errors (not every gateway exposes a listing endpoint).
func (a *App) ListAIProviderModels(cfg AIProviderConfig) ([]string, error) {
	provider := cfg.normalizedProvider()
	if provider != "openai" && provider != "anthropic" {
		return nil, fmt.Errorf("model listing is only available for API providers")
	}

	// Seed/fallback resolved from the chosen preset so the dropdown is never
	// left empty when the live listing fails. The currently-selected model is
	// always kept selectable. A free-form "custom" preset carries no seed, so
	// a failure there surfaces the error (the user must fix their endpoint).
	preset, hasPreset := aiProviderPresetByID(cfg.PresetID)
	fallback := mergeModelList(preset.SeedModels, cfg.Model)
	requiresKey := !hasPreset || preset.RequiresKey

	if strings.TrimSpace(cfg.APIKey) == "" && requiresKey {
		if len(fallback) > 0 {
			return fallback, nil
		}
		return nil, fmt.Errorf("API key is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	models, err := fetchProviderModels(ctx, provider, cfg)
	if err != nil || len(models) == 0 {
		if len(fallback) > 0 {
			return fallback, nil
		}
		if err != nil {
			return nil, err
		}
		return nil, nil
	}
	return mergeModelList(models, cfg.Model), nil
}

// fetchProviderModels lists model ids from an API provider's listing endpoint.
// OpenAI-compatible endpoints (OpenAI, DeepSeek, Moonshot, Qwen, Zhipu,
// OpenRouter, Ollama, ...) return a single {"data":[{"id"}]} page; Anthropic
// paginates with has_more / last_id, so we follow up to a bounded number of
// pages to avoid truncating the list.
func fetchProviderModels(ctx context.Context, provider string, cfg AIProviderConfig) ([]string, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	key := strings.TrimSpace(cfg.APIKey)
	client := aiProviderHTTPClient

	if provider == "openai" {
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
		if err != nil {
			return nil, err
		}
		if key != "" {
			req.Header.Set("authorization", "Bearer "+key)
		}
		ids, _, _, err := fetchModelsPage(client, req)
		return ids, err
	}

	// Anthropic — follow pagination.
	if base == "" {
		base = "https://api.anthropic.com"
	}
	var all []string
	after := ""
	for range 20 {
		url := base + "/v1/models?limit=1000"
		if after != "" {
			url += "&after_id=" + after
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
		ids, hasMore, lastID, err := fetchModelsPage(client, req)
		if err != nil {
			return nil, err
		}
		all = append(all, ids...)
		if !hasMore || lastID == "" {
			break
		}
		after = lastID
	}
	return all, nil
}

// fetchModelsPage performs one model-listing request and decodes the shared
// {"data":[{"id"}], "has_more", "last_id"} envelope used by both OpenAI- and
// Anthropic-compatible endpoints (the pagination fields are simply absent on
// single-page OpenAI responses).
func fetchModelsPage(client *http.Client, req *http.Request) (ids []string, hasMore bool, lastID string, err error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, false, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateProbe(strings.TrimSpace(string(raw)), 200))
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		HasMore bool   `json:"has_more"`
		LastID  string `json:"last_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, false, "", err
	}
	for _, m := range parsed.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, parsed.HasMore, parsed.LastID, nil
}

// mergeModelList returns the unique ids in `primary` (sorted for stable
// display), then appends `extra` (the currently-selected model) when it is
// non-empty and not already present — so a custom or unlisted model the user
// already chose stays selectable in the dropdown.
func mergeModelList(primary []string, extra string) []string {
	seen := make(map[string]bool, len(primary)+1)
	cleaned := make([]string, 0, len(primary))
	for _, m := range primary {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		cleaned = append(cleaned, m)
	}
	sort.Strings(cleaned)
	if extra = strings.TrimSpace(extra); extra != "" && !seen[extra] {
		cleaned = append(cleaned, extra)
	}
	return cleaned
}

// probeAnthropicProvider issues a 1-token /v1/messages call.
func probeAnthropicProvider(ctx context.Context, cfg AIProviderConfig) error {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	body, _ := json.Marshal(map[string]any{
		"model":      cfg.Model,
		"max_tokens": 1,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", strings.TrimSpace(cfg.APIKey))
	req.Header.Set("anthropic-version", "2023-06-01")
	return doProbe(req)
}

// probeOpenAIProvider issues a minimal /chat/completions call.
//
// We deliberately omit "max_tokens": some OpenAI-compatible gateways reject it
// outright (returning an opaque HTTP 500 with no body), and the runtime client
// never sends it on this path either — so the connection test must not gate on
// a parameter the real agent won't send. The probe only needs to confirm the
// endpoint + key + model produce a valid completion.
func probeOpenAIProvider(ctx context.Context, cfg AIProviderConfig) error {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	body, _ := json.Marshal(map[string]any{
		"model":    cfg.Model,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+strings.TrimSpace(cfg.APIKey))
	return doProbe(req)
}

// probeExternalAgent verifies the external agent CLI is installed and
// runnable by resolving it on PATH and invoking `<bin> --version`.
func probeExternalAgent(ctx context.Context, provider, command string) error {
	bin := strings.TrimSpace(command)
	if bin == "" {
		switch provider {
		case "codex":
			bin = "codex"
		default:
			bin = "claude"
		}
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("%q not found on PATH — install the %s CLI or set a full path", bin, provider)
	}
	out, err := cmdutil.CommandContext(ctx, resolved, "--version").CombinedOutput()
	if err != nil {
		snippet := strings.TrimSpace(string(out))
		if snippet == "" {
			snippet = err.Error()
		}
		return fmt.Errorf("%s --version failed: %s", bin, truncateProbe(snippet, 200))
	}
	return nil
}

func truncateProbe(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// doProbe sends the request and turns a non-2xx response into a concise
// error containing a snippet of the upstream body.
func doProbe(req *http.Request) error {
	resp, err := aiProviderHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	snippet := strings.TrimSpace(string(raw))
	if len(snippet) > 400 {
		snippet = snippet[:400] + "..."
	}
	if snippet == "" {
		// An empty body carries no detail — point the user at the curl command
		// (rendered in the settings UI) so they can inspect headers manually.
		snippet = "(empty response body — run the test as a curl command to debug)"
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet)
}
