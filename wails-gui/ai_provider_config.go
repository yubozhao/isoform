package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"

	"yansu-agent/agent"
)

// aiProviderManaged is the default provider: model calls go through the
// local magpie sidecar, which routes to the user's subscriptions, keys and
// local models (see magpie_sidecar.go).
const aiProviderManaged = "managed"

// AIProviderConfig is the model-provider configuration. The default
// ("managed") routes through the magpie sidecar. The advanced path is the
// "bring your own agent / AI-API" (BYOA) provider: it points the in-process
// agent straight at the user's own endpoint and key.
//
// Persisted to ~/.yansu-agent/ai-provider.json, mirroring the
// MemoryStoreConfig (memory.json) pattern so storage stays consistent.
type AIProviderConfig struct {
	// Provider selects the agent backend:
	//   "managed" (or "", "yansu") -> magpie sidecar routing groups (default)
	//   "anthropic"      -> Anthropic Messages API (api.anthropic.com or
	//                       any Anthropic-compatible endpoint via BaseURL)
	//   "openai"         -> OpenAI Chat Completions (api.openai.com/v1 or
	//                       any OpenAI-compatible endpoint via BaseURL)
	//   "claude-code"    -> spawn the user's Claude Code CLI (B-class BYOA)
	//   "codex"          -> spawn the user's Codex CLI (B-class BYOA)
	Provider string `json:"provider"`
	// PresetID is the catalog entry the user picked (e.g. "deepseek",
	// "qwen", "custom") — see ai_provider_presets.go. It is a UI/listing
	// convenience only: the runner dispatches on Provider (the wire format),
	// never on PresetID. Empty for legacy configs and for external agents,
	// in which case it falls back to Provider. Persisting it lets the picker
	// restore the selection and the model listing resolve the seed fallback.
	PresetID string `json:"presetId"`
	// APIKey is the user's credential, sent as x-api-key (anthropic) or
	// Authorization: Bearer (openai).
	APIKey string `json:"apiKey"`
	// BaseURL overrides the default endpoint. Empty uses the provider
	// default. For "openai" it conventionally ends in the version segment
	// (e.g. https://api.openai.com/v1).
	BaseURL string `json:"baseUrl"`
	// Model is the primary ("big-brain") model id used for chat/code roles
	// under BYOA (e.g. "claude-sonnet-4-6", "gpt-4o", "openai/gpt-4o" on
	// OpenRouter). For external agents it is passed to the CLI's --model/-m
	// flag.
	Model string `json:"model"`
	// LightModel is the optional "small-brain" model id used for cheap,
	// high-frequency background tasks (activity summaries, titles, memory /
	// event extraction, vision triage — everything routed through
	// lightTaskModel/runEphemeralHaiku). Empty falls back to Model, so
	// existing single-model configs keep behaving exactly as before. For
	// external agents it maps onto the CLI's light role: the runner spawns the
	// CLI with this model (--model / -m) for light tasks (see
	// externalAgentRunnerConfig's ModelAliases).
	LightModel string `json:"lightModel"`
	// Command is the external-agent CLI binary path/name (claude-code /
	// codex providers only). Empty falls back to the CLI's default name on
	// PATH ("claude" / "codex").
	Command string `json:"command"`
	// DisableSandbox (claude-code / codex) lifts the CLI's OS-level Bash
	// sandbox so shell commands can access localhost services and the network.
	DisableSandbox bool `json:"disableSandbox,omitempty"`
	// AssistantMaxBudgetUSD caps each controlled Claude attempt. Zero or an
	// invalid value uses the default; this advanced file setting needs no UI.
	AssistantMaxBudgetUSD float64 `json:"assistantMaxBudgetUsd,omitempty"`
	// CachedModels is the last model list fetched from the provider's listing
	// endpoint ("Fetch models"). It is a pure UI convenience — the runner never
	// reads it — persisted so the model dropdowns repopulate on reopen instead
	// of forcing a re-fetch every time. Cleared when the provider changes.
	CachedModels []string `json:"cachedModels,omitempty"`
	// AdvancedProvider remembers the advanced provider (anthropic, openai,
	// claude-code or codex) while Provider is managed, so switching back to
	// advanced restores it. Mode switches never clear APIKey or the models.
	AdvancedProvider string `json:"advancedProvider,omitempty"`
	// ManagedOrder lists the signed-in assistants the routing groups admit,
	// in the user's order: the first is the primary, the rest are backups.
	// Onboarding writes it when the user picks an assistant. Empty means the
	// default (see managedAccountProviders). Entries are provider ids as
	// magpie names them ("claude", "codex").
	ManagedOrder []string `json:"managedOrder,omitempty"`
	// AnswerWith is the user's pick in Settings > Assistant, "Who answers in
	// Isoform": "claude-code", "codex" or "isoform". Only the pick is tried,
	// with Isoform as the backup; ManagedOrder still orders the subscriptions
	// Isoform thinks with. Empty means the first pickable assistant in ManagedOrder.
	AnswerWith string `json:"answerWith,omitempty"`
	// BackgroundUsesChoice runs background work (the brief, learning,
	// summaries) on the pick's model and effort too. Off by default: they stay
	// on a lighter default.
	BackgroundUsesChoice bool `json:"backgroundUsesChoice,omitempty"`
	// AgentChoices holds each agent's model and effort pick from Settings >
	// Assistant ("claude-code", "codex" and "isoform" keys). They live only
	// here, in Isoform's own config: every Claude Code or Codex run gets them
	// as flags, and Isoform uses the pair in its own gateway routing. Models keep
	// the catalog id (provider prefix included), as Settings shows it.
	AgentChoices map[string]AgentModelChoice `json:"agentChoices,omitempty"`
	// AgentChoicesMigrated marks the one-time move of the picks out of
	// magpie's per-agent settings and isoform-choice.json
	// (assistant_choice_migration.go). It stays true even with no picks, so
	// the migration never runs twice.
	AgentChoicesMigrated bool `json:"agentChoicesMigrated,omitempty"`
	// MagpieProfile is Isoform's ordered routing group. Nil retains the
	// existing defaults until the person edits the profile.
	MagpieProfile []AgentModelChoice `json:"magpieProfile,omitempty"`
}

// AgentModelChoice is one agent's model and effort pick. Empty fields mean the
// default: the agent's own model, High effort.
type AgentModelChoice struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// agentChoice returns the saved pick for agentID, zero when none.
func (c AIProviderConfig) agentChoice(agentID string) AgentModelChoice {
	return c.AgentChoices[agentID]
}

// setAgentChoice saves the pick for agentID, dropping the entry when both
// fields are empty so the map stays nil when nothing is chosen.
func (c *AIProviderConfig) setAgentChoice(agentID string, choice AgentModelChoice) {
	if choice == (AgentModelChoice{}) {
		delete(c.AgentChoices, agentID)
		if len(c.AgentChoices) == 0 {
			c.AgentChoices = nil
		}
		return
	}
	if c.AgentChoices == nil {
		c.AgentChoices = make(map[string]AgentModelChoice)
	}
	c.AgentChoices[agentID] = choice
}

// managedAccountProviders are the subscription providers the routing groups
// know how to use, in the default order: Claude Code first, Codex as backup.
var managedAccountProviders = []string{"claude", "codex"}

// managedAccountProviderKnown reports whether id is a provider the groups admit.
func managedAccountProviderKnown(id string) bool {
	for _, known := range managedAccountProviders {
		if id == known {
			return true
		}
	}
	return false
}

// managedOrder returns the subscription providers the groups admit, in the
// user's order. Unknown and repeated entries are dropped; an empty result
// falls back to the default order.
func (c AIProviderConfig) managedOrder() []string {
	return normalizeManagedOrder(c.ManagedOrder)
}

// normalizeManagedOrder lower-cases, trims and de-duplicates order, keeping
// only known providers. An empty result is the default order.
func normalizeManagedOrder(order []string) []string {
	out := make([]string, 0, len(managedAccountProviders))
	seen := make(map[string]bool, len(managedAccountProviders))
	for _, raw := range order {
		id := strings.ToLower(strings.TrimSpace(raw))
		if seen[id] || !managedAccountProviderKnown(id) {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return append([]string{}, managedAccountProviders...)
	}
	return out
}

// lightModelOrChat returns the configured light ("small-brain") model.
// The frontend always persists a concrete value for external agents
// (claude-code / codex), so the fallback here is a safety net only.
func (c AIProviderConfig) lightModelOrChat() string {
	if light := strings.TrimSpace(c.LightModel); light != "" {
		return light
	}
	return strings.TrimSpace(c.Model)
}

const aiProviderConfigFileName = "ai-provider.json"

const defaultAssistantMaxBudgetUSD = 10

func (c AIProviderConfig) assistantBudgetUSD() float64 {
	if c.AssistantMaxBudgetUSD > 0 && !math.IsInf(c.AssistantMaxBudgetUSD, 0) {
		return c.AssistantMaxBudgetUSD
	}
	return defaultAssistantMaxBudgetUSD
}

// assistantRunnerConfig applies the same budget to managed and explicitly
// selected assistants, retaining custom command/model settings for the latter.
func (c AIProviderConfig) assistantRunnerConfig(format string) agent.ExternalAgentConfig {
	cfg, selected := c.externalAgentRunnerConfig()
	if !selected || cfg.Format != format {
		cfg = agent.ExternalAgentConfig{Format: format, MaxBudgetUSD: c.assistantBudgetUSD()}
	}
	return cfg
}

// isBYOA reports whether this config selects a real bring-your-own API
// provider (an Anthropic/OpenAI-format endpoint with a key). A
// "yansu"/empty provider, an external-agent provider, or any API provider
// missing a key, is not API-BYOA.
func (c AIProviderConfig) isBYOA() bool {
	p := strings.ToLower(strings.TrimSpace(c.Provider))
	if p != "anthropic" && p != "openai" {
		return false
	}
	return strings.TrimSpace(c.APIKey) != ""
}

// isExternalAgent reports whether this config selects a bring-your-own
// external agent CLI (Claude Code / Codex). No API key is required — the
// CLI carries its own auth.
func (c AIProviderConfig) isExternalAgent() bool {
	switch strings.ToLower(strings.TrimSpace(c.Provider)) {
	case "claude-code", "codex":
		return true
	default:
		return false
	}
}

// externalAgentRunnerConfig converts the persisted config into the
// agent.ExternalAgentConfig the runner expects, and reports whether an
// external agent is configured.
func (c AIProviderConfig) externalAgentRunnerConfig() (agent.ExternalAgentConfig, bool) {
	if !c.isExternalAgent() {
		return agent.ExternalAgentConfig{}, false
	}
	cfg := agent.ExternalAgentConfig{
		Format:         strings.ToLower(strings.TrimSpace(c.Provider)),
		Command:        strings.TrimSpace(c.Command),
		Model:          strings.TrimSpace(c.Model),
		DisableSandbox: c.DisableSandbox,
		MaxBudgetUSD:   c.assistantBudgetUSD(),
	}
	// Map the role aliases the runner emits per task onto the configured
	// big/light models, mirroring the native runner. With a big model set,
	// chat/code tasks ("sonnet"/"opus") use it and light/background tasks
	// ("haiku") use LightModel (falling back to the big model when unset) — so
	// external agents get the same big/small-brain split as BYOK. Without a
	// configured model the aliases are left empty and the task model passes
	// through (e.g. Claude resolves "sonnet"/"haiku" to its own tiers).
	if cfg.Model != "" {
		cfg.ModelAliases = map[string]string{
			"sonnet": cfg.Model,
			"opus":   cfg.Model,
			"haiku":  c.lightModelOrChat(),
		}
	}
	return cfg, true
}

// normalizedProvider returns the lower-cased provider. A missing provider and
// the retired "yansu" gateway value both mean managed: configs written before
// managed mode existed migrate on read without a rewrite.
func (c AIProviderConfig) normalizedProvider() string {
	p := strings.ToLower(strings.TrimSpace(c.Provider))
	if p == "" || p == "yansu" {
		return aiProviderManaged
	}
	return p
}

// managedActive reports whether the config selects managed mode.
func (c AIProviderConfig) managedActive() bool {
	return c.normalizedProvider() == aiProviderManaged
}

// sidecarActive reports whether the magpie sidecar serves Yansu's own loop:
// in managed mode, and in the external CLI modes, where the CLI takes chat and
// unscoped work while the native loop still needs a model for scoped,
// ephemeral and background work (Dream, titles, the verifier). Only an
// explicit key or endpoint opts out of the sidecar.
func (c AIProviderConfig) sidecarActive() bool {
	return c.managedActive() || c.isExternalAgent()
}

func aiProviderConfigPath() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, aiProviderConfigFileName), nil
}

// readAIProviderConfig reads ~/.yansu-agent/ai-provider.json. A missing or
// unreadable file yields the zero value, which is managed mode with no key.
// The provider is normalized on read, so migrated configs report managed
// without being rewritten.
func readAIProviderConfig() AIProviderConfig {
	var cfg AIProviderConfig
	path, err := aiProviderConfigPath()
	if err != nil {
		cfg.Provider = aiProviderManaged
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		cfg.Provider = aiProviderManaged
		return cfg
	}
	if json.Unmarshal(data, &cfg) != nil {
		cfg = AIProviderConfig{}
	}
	cfg.Provider = cfg.normalizedProvider()
	return cfg
}

func loadAIProviderConfig() AIProviderConfig {
	cfg := readAIProviderConfig()
	// Ordinary reads stay lock-free against the atomic source file. A retired
	// pick's repair reloads under the mutation lock before writing anything.
	if _, blocked := unpickableAgents[cfg.AnswerWith]; blocked {
		cfg.AnswerWith = ""
		if current, err := updateAIProviderConfig(func(*AIProviderConfig) error { return nil }); err == nil {
			return current
		}
	}
	return cfg
}

// updateAIProviderConfig applies a partial update to the current source while
// holding the credential lock through publication. Callers must not load a
// full snapshot before this lock and accidentally restore a withdrawn key.
func updateAIProviderConfig(update func(*AIProviderConfig) error) (AIProviderConfig, error) {
	magpieByokMu.Lock()
	defer magpieByokMu.Unlock()
	cfg := readAIProviderConfig()
	if _, blocked := unpickableAgents[cfg.AnswerWith]; blocked {
		cfg.AnswerWith = ""
	}
	if err := update(&cfg); err != nil {
		return cfg, err
	}
	return cfg, saveAIProviderConfigLocked(cfg)
}

// saveAIProviderConfig writes the config with 0600 perms (it holds a
// secret). It creates the config dir if missing.
func saveAIProviderConfig(cfg AIProviderConfig) error {
	magpieByokMu.Lock()
	defer magpieByokMu.Unlock()
	return saveAIProviderConfigLocked(cfg)
}

func saveAIProviderConfigLocked(cfg AIProviderConfig) error {
	path, err := aiProviderConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	stateDir := filepath.Join(filepath.Dir(path), magpieStateDirName)
	// Withdraw the old copy first. If saving fails, startup can restore it
	// from the still-authoritative config; it must never outlive a removal.
	if err := reconcileMagpieByok(stateDir, AIProviderConfig{}); err != nil {
		return err
	}
	if err := writeMagpieConfigFile(path, data); err != nil {
		return err
	}
	return reconcileMagpieByok(stateDir, cfg)
}

// aiProviderActive returns the BYOA config and true when a real
// bring-your-own provider is configured. Callers use this at agent
// construction to feed credentials/endpoint/model to the runner.
func aiProviderActive() (AIProviderConfig, bool) {
	cfg := loadAIProviderConfig()
	return cfg, cfg.isBYOA()
}

// externalAgentActive returns the external-agent runner config and true
// when the user has selected Claude Code / Codex as their agent backend.
func externalAgentActive() (agent.ExternalAgentConfig, bool) {
	return loadAIProviderConfig().externalAgentRunnerConfig()
}

// byoaPaidPlans is the set of subscription plans that unlock BYOA / BYOK
// configuration. The dedicated "byoa" plan ($10) unlocks it, and the higher
// tiers (studio, max) include it. Pro ($20) is the gateway-only entry tier
// and deliberately does NOT unlock BYOA. Keeping the set here makes "which
// plans unlock BYOA" a one-line policy change.
var byoaPaidPlans = map[string]bool{
	"byoa":   true,
	"studio": true,
	"max":    true,
}

// isByoaEntitledPlan reports whether an (active, plan) pair entitles the
// user to BYOA. NOTE: not every PAID plan qualifies — pro ($20) is a
// gateway-only tier and is deliberately excluded (see byoaPaidPlans). Pure
// function so the policy is unit-testable without a live subscription. A
// nil/empty/"free"/"pro" plan, or an inactive subscription, is not entitled.
func isByoaEntitledPlan(plan string, isActive bool) bool {
	if !isActive {
		return false
	}
	return byoaPaidPlans[strings.ToLower(strings.TrimSpace(plan))]
}
