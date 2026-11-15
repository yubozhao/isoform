package main

import (
	"os"
	"strings"
)

const (
	defaultClaudeChatModel  = "sonnet"
	defaultClaudeCodeModel  = "opus"
	defaultClaudeLightModel = "haiku"

	apiClaudeSonnetModel = "claude-sonnet-4-6"
	apiClaudeOpusModel   = "claude-opus-4-6"
	apiClaudeHaikuModel  = "claude-haiku-4-5-20251001" // gitleaks:allow
	apiClaudeFableModel  = "claude-fable-5"
)

// ClaudeCodeModelConfig holds the three model roles used internally.
type ClaudeCodeModelConfig struct {
	ChatModel  string `json:"chatModel"`
	CodeModel  string `json:"codeModel"`
	LightModel string `json:"lightModel"`
}

// defaultClaudeCodeModelConfig returns the env-var-aware defaults.
// Priority: ANTHROPIC_DEFAULT_*_MODEL env var > hardcoded constant.
func defaultClaudeCodeModelConfig() ClaudeCodeModelConfig {
	cfg := ClaudeCodeModelConfig{
		ChatModel:  defaultClaudeChatModel,
		CodeModel:  defaultClaudeCodeModel,
		LightModel: defaultClaudeLightModel,
	}

	// ANTHROPIC_MODEL overrides all three roles as a universal fallback.
	if v := strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL")); v != "" {
		cfg.ChatModel = v
		cfg.CodeModel = v
		cfg.LightModel = v
	}

	// Role-specific env vars take precedence over ANTHROPIC_MODEL.
	if v := strings.TrimSpace(os.Getenv("ANTHROPIC_DEFAULT_SONNET_MODEL")); v != "" {
		cfg.ChatModel = v
	}
	if v := strings.TrimSpace(os.Getenv("ANTHROPIC_DEFAULT_OPUS_MODEL")); v != "" {
		cfg.CodeModel = v
	}
	if v := strings.TrimSpace(os.Getenv("ANTHROPIC_DEFAULT_HAIKU_MODEL")); v != "" {
		cfg.LightModel = v
	}

	return cfg
}

// claudeCodeModelConfig returns the effective model config.
// Gateway on: keep the short model names ("sonnet"/"opus"/"haiku") so the
// runner's ModelAliases table resolves them the same way it does elsewhere.
// Do not switch the gateway path to pinned API model IDs here.
// Gateway off: env-var-aware defaults.
func claudeCodeModelConfig() ClaudeCodeModelConfig {
	if loadConfig().UseYansuGateway {
		// Use short names so the runner's ModelAliases table resolves them
		// and the existing sub2api calling logic stays unchanged.
		return ClaudeCodeModelConfig{
			ChatModel:  defaultClaudeChatModel,
			CodeModel:  defaultClaudeCodeModel,
			LightModel: defaultClaudeLightModel,
		}
	}
	return defaultClaudeCodeModelConfig()
}

func claudeModelDisplayName(id string) string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "sonnet", apiClaudeSonnetModel:
		return "Sonnet"
	case "opus", apiClaudeOpusModel:
		return "Opus"
	case "haiku", apiClaudeHaikuModel:
		return "Haiku"
	case "fable", apiClaudeFableModel:
		return "Fable"
	default:
		s := strings.TrimSpace(id)
		if s == "" {
			return s
		}
		// Capitalize by rune, not byte: allowCustom lets users type arbitrary
		// ids, and slicing a multi-byte first character would emit invalid UTF-8.
		r := []rune(s)
		return strings.ToUpper(string(r[0])) + string(r[1:])
	}
}

// buildClaudeModelOptions returns deduplicated model entries for the chat dropdown.
// No env vars set  → [sonnet, opus, haiku] (the three defaults).
// Env vars set     → unique models from the resolved config (ANTHROPIC_MODEL +
//
//	ANTHROPIC_DEFAULT_*_MODEL), preserving insertion order.
func buildClaudeModelOptions(cfg ClaudeCodeModelConfig) []ModelInfo {
	seen := make(map[string]bool)
	var models []ModelInfo
	add := func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		models = append(models, ModelInfo{
			ID:       id,
			Name:     claudeModelDisplayName(id),
			Executor: "claude",
		})
	}
	// ChatModel first (it is the default selection).
	add(cfg.ChatModel)
	add(cfg.CodeModel)
	add(cfg.LightModel)
	return models
}

func claudeModelForAnthropicAPI(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "sonnet":
		return apiClaudeSonnetModel
	case "opus":
		return apiClaudeOpusModel
	case "haiku":
		return apiClaudeHaikuModel
	case "fable":
		return apiClaudeFableModel
	default:
		return strings.TrimSpace(model)
	}
}

func claudeAPIModelAliases() map[string]string {
	return map[string]string{
		"sonnet": apiClaudeSonnetModel,
		"opus":   apiClaudeOpusModel,
		"haiku":  apiClaudeHaikuModel,
		"fable":  apiClaudeFableModel,
	}
}

func (a *App) chatTaskModel() string {
	return claudeCodeModelConfig().ChatModel
}

func (a *App) lightTaskModel() string {
	return claudeCodeModelConfig().LightModel
}

// Magpie routing groups (see magpie_bootstrap.go). In managed mode every model
// string the native runner sends must resolve to a group: a raw
// claude/... or codex/... id would reach a subscription directly and skip the
// consent gate. So the pinned ids Yansu call sites emit are mapped too.
const (
	magpieChatGroupModel       = "group/yansu-chat"
	magpieBackgroundGroupModel = "group/yansu-background"
	magpieLightGroupModel      = "group/yansu-light"
)

// managedModelAliasesFor maps every model id Isoform's call sites emit to the
// two targets of one routing mode: sonnet, opus and fable (and their pinned
// ids) go to the heavy target, haiku to the light one. With the bundled
// gateway the targets are Isoform's own groups; with the user's magpie they
// are the group the user already routes through.
func managedModelAliasesFor(heavy, light string) map[string]string {
	return map[string]string{
		"sonnet":             heavy,
		"opus":               heavy,
		"fable":              heavy,
		"haiku":              light,
		apiClaudeSonnetModel: heavy,
		apiClaudeOpusModel:   heavy,
		apiClaudeFableModel:  heavy,
		apiClaudeHaikuModel:  light,
	}
}

// managedModelAliases routes interactive chat and light work through the
// bundled gateway's groups: yansu-chat and yansu-light.
func managedModelAliases() map[string]string {
	return managedModelAliasesFor(magpieChatGroupModel, magpieLightGroupModel)
}

// managedBackgroundModelAliases routes background-priority work (Dream,
// understanding, summaries, extraction) to yansu-background, whose pace routing
// keeps it from draining the plan the user needs during the day. Light work
// still goes to yansu-light.
func managedBackgroundModelAliases() map[string]string {
	return managedModelAliasesFor(magpieBackgroundGroupModel, magpieLightGroupModel)
}
