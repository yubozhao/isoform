package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"yansu-agent/agent"
)

// Settings > Assistant, "Who answers in Isoform": one pick (Claude Code, Codex
// or Isoform itself; DeepSeek Harness is listed but cannot be picked yet) with Isoform as the automatic backup, and the picked
// agent's model and effort (High by default). A choice applies only to
// Isoform's runs, never to the user's own Claude Code or Codex:
//
//   - The pick lives in Isoform's routing config (AIProviderConfig.AnswerWith)
//     and changes no magpie group and no subscription order.
//   - Every agent's model and effort lives in Isoform's own config
//     (AIProviderConfig.AgentChoices in ai-provider.json). Each Claude Code or
//     Codex run gets them as run flags (--model and --effort; Codex -m and
//     model_reasoning_effort), so the agent keeps its own sign-in, and
//     The model profile in Isoform's config controls its own magpie group.
//     Nothing writes the user's other groups, ~/.claude or ~/.codex. Picks made
//     before this move are migrated once on start (assistant_choice_migration.go).
//   - Background work (the brief, learning, summaries) stays on a lighter
//     default: Sonnet 5.5 at Medium on Claude Code, Medium on Codex, and
//     Isoform's paced background group, unless the user turns on "Use my
//     choice for background work too".

const (
	magpieAgentHomeDirName = "agent-home"
	answeringIsoform       = "isoform"
	// answeringDeepSeek is DeepSeek Harness (the dsh CLI). It has no runner:
	// Settings lists it, and nothing routes to it.
	answeringDeepSeek      = "deepseek"
	deepSeekCLI            = "dsh"
	assistantChoiceTimeout = 20 * time.Second
	// defaultEffort is the effort a pick runs at until the user changes it.
	defaultEffort = "high"
)

// backgroundChoices is the lighter default for background work on each
// assistant, unless the user uses their pick for background work too.
var backgroundChoices = map[string]agent.AgentChoice{
	string(agent.BackendClaudeCode): {Model: "claude-sonnet-5-5", Effort: "medium"},
	string(agent.BackendCodex):      {Effort: "medium"},
}

// unpickableAgents cannot answer in Isoform yet, with the reason. Codex's
// tool isolation is unverified, so every Codex run fails its control check
// (agent.ExternalControlProfileVersion) and Isoform would answer anyway.
// DeepSeek Harness can call Isoform's tools, but its start event attests no
// profile Isoform can verify, and its sandbox confines neither the harness
// nor its MCP child, so a run cannot be locked down to Isoform's tools.
var unpickableAgents = map[string]string{
	string(agent.BackendCodex): "codex can't use Isoform's tools yet",
	answeringDeepSeek:          "deepseek harness can't be locked down to Isoform's tools yet",
}

// signInCommands are the agents' own sign-in commands, run in Terminal.
var signInCommands = map[string][]string{
	string(agent.BackendClaudeCode): {"claude", "auth", "login"},
	string(agent.BackendCodex):      {"codex", "login"},
}

// choiceAgent is one agent Settings offers.
type choiceAgent struct {
	id      string // claude-code, codex, deepseek, isoform
	magpie  string // the name the magpie CLI uses; "" for Isoform itself and DeepSeek Harness
	efforts []string
	runs    func(model string) bool // models the agent can run with its own sign-in
	cli     string                  // the CLI whose absence the row shows as not installed; "" when health says it
}

var choiceAgents = []choiceAgent{
	{id: string(agent.BackendClaudeCode), magpie: "claude", efforts: []string{"low", "medium", "high", "xhigh", "max"},
		runs: func(model string) bool { return strings.HasPrefix(modelPart(model), "claude-") }},
	{id: string(agent.BackendCodex), magpie: "codex", efforts: []string{"low", "medium", "high", "xhigh"},
		runs: func(model string) bool {
			return providerPart(model) == "codex" || strings.HasPrefix(modelPart(model), "gpt-")
		}},
	// No models or efforts: it cannot run yet, so there is nothing to choose.
	{id: answeringDeepSeek, cli: deepSeekCLI, runs: func(string) bool { return false }},
	{id: answeringIsoform, efforts: []string{"low", "medium", "high", "xhigh", "max"},
		runs: func(string) bool { return true }},
}

func findChoiceAgent(id string) (choiceAgent, bool) {
	for _, a := range choiceAgents {
		if a.id == id {
			return a, true
		}
	}
	return choiceAgent{}, false
}

func providerPart(model string) string {
	provider, _, _ := strings.Cut(model, "/")
	return strings.ToLower(provider)
}

// modelPart is a catalog id without its provider and context variant:
// "claude/claude-opus-5-5[1m]" is "claude-opus-5-5", the id the agent's own
// CLI takes.
func modelPart(model string) string {
	if _, rest, ok := strings.Cut(model, "/"); ok {
		model = rest
	}
	if i := strings.IndexByte(model, '['); i > 0 {
		model = model[:i]
	}
	return model
}

// runModel is the id a run passes to the agent's own CLI: the catalog id
// without its provider, keeping a context variant ("claude-opus-5-5[1m]"),
// which Claude Code takes as its 1M-context model.
func runModel(model string) string {
	if _, rest, ok := strings.Cut(model, "/"); ok {
		return rest
	}
	return model
}

// nearestEffort is the step a run uses: the wanted one when the model offers
// it, else the closest step it does offer (the lower on a tie), so switching
// to a model without the saved step never drops the effort. "" when the model
// offers none.
func nearestEffort(steps []string, want string) string {
	if len(steps) == 0 {
		return ""
	}
	if slices.Contains(steps, want) {
		return want
	}
	at := slices.Index(effortLevels, want)
	best, bestDistance := "", len(effortLevels)+1
	for _, step := range steps {
		distance := slices.Index(effortLevels, step) - at
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance {
			best, bestDistance = step, distance
		}
	}
	return best
}

// AgentChoiceView is one agent's row in Settings. Model is empty when the
// agent uses its own default. Effort is the effort its runs use (High until
// the user changes it, else the nearest step the model offers; empty when the
// model offers none or Isoform does not choose it). ModelEfforts is the effort
// steps magpie lists for each model; Efforts is the steps for the current
// model. CanAnswer is whether the row can be picked: never Codex or DeepSeek
// Harness yet, and Isoform remains selectable while its models start.
// NotInstalled is set for an agent without a health line (DeepSeek Harness)
// whose CLI is not on PATH.
type AgentChoiceView struct {
	Agent        string              `json:"agent"`
	CanAnswer    bool                `json:"canAnswer"`
	NotInstalled bool                `json:"notInstalled,omitempty"`
	Model        string              `json:"model"`
	Effort       string              `json:"effort"`
	Models       []string            `json:"models"`
	ModelEfforts map[string][]string `json:"modelEfforts"`
	Efforts      []string            `json:"efforts"`
}

// AssistantChoiceView describes saved choices and the current model source.
type AssistantChoiceView struct {
	Answering            string            `json:"answering"`
	Agents               []AgentChoiceView `json:"agents"`
	BackgroundUsesChoice bool              `json:"backgroundUsesChoice"`
	Editable             bool              `json:"editable"`
	Reason               string            `json:"reason,omitempty"`
	IsoformReady         bool              `json:"isoformReady"`
}

// magpieEfforts are the suffixes magpie accepts on a group member
// ("provider/model:high"). Other colons belong to the model id
// ("ollama/llama3.2:latest").
var magpieEfforts = []string{"none", "low", "medium", "high", "xhigh", "max"}

// memberBase drops a trailing effort from a group member.
func memberBase(member string) string {
	if i := strings.LastIndexByte(member, ':'); i > 0 && slices.Contains(magpieEfforts, member[i+1:]) {
		return member[:i]
	}
	return member
}

// applyIsoformChoice puts the chosen model first and gives every member the
// chosen effort, High unless the user chose another ("provider/model:high").
// magpie sends a model without that step its nearest one.
func applyIsoformChoice(members []string, choice AgentModelChoice) []string {
	effort := choice.Effort
	if effort == "" {
		effort = defaultEffort
	}
	out := make([]string, 0, len(members)+1)
	seen := make(map[string]bool, len(members)+1)
	add := func(member string) {
		base := memberBase(member)
		if base == "" || seen[base] {
			return
		}
		seen[base] = true
		out = append(out, base+":"+effort)
	}
	if choice.Model != "" {
		add(choice.Model)
	}
	for _, member := range members {
		add(member)
	}
	return out
}

// agentChoiceCache holds the Claude Code and Codex picks runs read, so a run
// never waits on the magpie CLI, and the last view Settings read, so saving
// the pick answers without reading magpie again.
type agentChoiceCache struct {
	mu      sync.Mutex
	choices map[string]agent.AgentChoice
	view    *AssistantChoiceView
	catalog []string
	listed  map[string][]string
}

func (c *agentChoiceCache) get(agentID string) agent.AgentChoice {
	choice, _ := c.lookup(agentID)
	return choice
}

// lookup also reports whether the agent's choice has been read.
func (c *agentChoiceCache) lookup(agentID string) (agent.AgentChoice, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	choice, ok := c.choices[agentID]
	return choice, ok
}

func (c *agentChoiceCache) set(choices map[string]agent.AgentChoice) {
	c.mu.Lock()
	c.choices = choices
	c.view = nil
	c.mu.Unlock()
}

func (c *agentChoiceCache) setView(view AssistantChoiceView) {
	c.mu.Lock()
	c.view = &view
	c.mu.Unlock()
}

func (c *agentChoiceCache) lastView() (AssistantChoiceView, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.view == nil {
		return AssistantChoiceView{}, false
	}
	view := *c.view
	view.Agents = slices.Clone(view.Agents)
	return view, true
}

func (c *agentChoiceCache) catalogSnapshot() ([]string, map[string][]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.catalog), c.listed
}

func (c *agentChoiceCache) setCatalog(models []string, listed map[string][]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.catalog, c.listed = slices.Clone(models), listed
}

func (c *agentChoiceCache) put(agentID string, choice agent.AgentChoice) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.choices == nil {
		c.choices = make(map[string]agent.AgentChoice)
	}
	c.choices[agentID] = choice
	c.view = nil
}

// agentSettingsEnv points every agent's settings folder at Isoform's agent
// home, including the variables that override HOME for one agent, so an
// inherited CLAUDE_CONFIG_DIR or CODEX_HOME never leads magpie to the user's
// files.
func agentSettingsEnv(home string) []string {
	return []string{
		"HOME=" + home,
		"CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"),
		"CODEX_HOME=" + filepath.Join(home, ".codex"),
	}
}

// runCatalogCLI lists the gateway's models: the gateway's providers, read
// with HOME at Isoform's agent home so no agent file of the user's is read.
func (s *magpieSidecar) runCatalogCLI(ctx context.Context) ([]byte, error) {
	home, err := s.agentHome()
	if err != nil {
		return nil, err
	}
	return s.runCLIEnv(ctx, agentSettingsEnv(home), "models")
}

// agentHome is Isoform's own magpie home: the sandboxed HOME the catalog CLI
// (and the one-time choice migration) runs with, so no agent file of the
// user's is ever touched. Only the bundled copy has one.
func (s *magpieSidecar) agentHome() (string, error) {
	if s.External() {
		return "", errors.New("your own magpie is read with your own home")
	}
	if s.stateDir == "" {
		return "", errMagpieStateDir
	}
	home := filepath.Join(s.stateDir, magpieAgentHomeDirName)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	return home, nil
}

var magpieEffortPattern = regexp.MustCompile(`·\s+effort\s+(\S+)`)

// parseMagpieAgentShow reads `magpie <agent>`:
//
//	Claude Code  claude/claude-opus-5-5[1m]  ·  effort high  ·  haiku …  ~/.claude/settings.json
//
// A dash is unset.
func parseMagpieAgentShow(text, agentName string) (model, effort string) {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, agentName+"  ") {
			continue
		}
		if fields := strings.Fields(strings.TrimPrefix(trimmed, agentName)); len(fields) > 0 && fields[0] != "—" && fields[0] != "-" {
			model = fields[0]
		}
		if m := magpieEffortPattern.FindStringSubmatch(trimmed); m != nil && m[1] != "—" && m[1] != "-" {
			effort = m[1]
		}
		return model, effort
	}
	return "", ""
}

var magpieEffortListPattern = regexp.MustCompile(`^[a-z]+(/[a-z]+)*$`)

// effortLevels are the five steps Settings offers, in order.
var effortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// parseMagpieModelList reads `magpie models`: provider headings, then one
// provider/model per line, with the effort steps it offers last when it has
// any; routing groups (group/...) are left out. Every model gets an entry in
// the steps, empty when it offers none.
//
//	local/claude-opus-5-5   Claude Opus 5.5  low/medium/high/xhigh/max
func parseMagpieModelList(text string) ([]string, map[string][]string) {
	var models []string
	efforts := map[string][]string{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !magpieModelIDPattern.MatchString(fields[0]) || strings.HasPrefix(fields[0], "group/") {
			continue
		}
		if !slices.Contains(models, fields[0]) {
			models = append(models, fields[0])
		}
		if _, ok := efforts[fields[0]]; !ok {
			efforts[fields[0]] = []string{}
		}
		if last := fields[len(fields)-1]; len(fields) > 1 && strings.Contains(last, "/") && magpieEffortListPattern.MatchString(last) {
			listed := strings.Split(last, "/")
			var steps []string
			for _, step := range effortLevels {
				if slices.Contains(listed, step) {
					steps = append(steps, step)
				}
			}
			efforts[fields[0]] = steps
		}
	}
	return models, efforts
}

// effortSteps is the effort steps for an agent's model: what magpie lists for
// it (none when it lists the model without steps), or the agent's own steps
// for its default model or one the catalog does not list. Isoform's effort
// applies to every model of its group, so it offers all five; magpie sends a
// model without the chosen step its nearest.
func effortSteps(ca choiceAgent, model string, listed map[string][]string) []string {
	if model != "" && ca.id != answeringIsoform {
		base := memberBase(model)
		if i := strings.IndexByte(base, '['); i > 0 {
			base = base[:i]
		}
		if steps, ok := listed[base]; ok {
			return steps
		}
	}
	return ca.efforts
}

func magpieAgentDisplayName(magpieName string) string {
	for display, name := range magpieAgentNames {
		if name == magpieName {
			return display
		}
	}
	return magpieName
}

// assistantChoiceSource describes the model source without restricting agent choices.
func (a *App) assistantChoiceSource() string {
	if !a.modelSidecarActive() {
		return "own_key"
	}
	if a.magpie != nil && a.magpie.External() {
		return "own_magpie"
	}
	return ""
}

// isoformCanAnswer reports whether Isoform has a model to answer with: the
// running gateway, or the user's own key.
func (a *App) isoformCanAnswer() bool {
	if a.modelSidecarActive() {
		return a.magpie != nil && a.magpie.Healthy()
	}
	_, ok := aiProviderActive()
	return ok
}

// answeringAgent is the pick: the user's choice, or, before they made one,
// the first pickable assistant of the order onboarding chose. A pick saved before its
// agent became unpickable (Codex cannot answer in Isoform yet) is no pick.
func answeringAgent(cfg AIProviderConfig) string {
	if _, blocked := unpickableAgents[cfg.AnswerWith]; !blocked {
		if _, ok := findChoiceAgent(cfg.AnswerWith); ok {
			return cfg.AnswerWith
		}
	}
	for _, candidate := range assistantFormatOrder(cfg) {
		if _, blocked := unpickableAgents[candidate]; !blocked {
			return candidate
		}
	}
	return string(agent.BackendClaudeCode)
}

// routeOrder is the assistants a run may try before Isoform: only the pick.
func routeOrder(cfg AIProviderConfig) []string {
	if pick := answeringAgent(cfg); pick != answeringIsoform {
		if len(cfg.MagpieProfile) > 0 {
			provider := pick
			if pick == string(agent.BackendClaudeCode) {
				provider = "claude"
			}
			// An executor may run first only when it corresponds to the first
			// profile member. Otherwise the native runner lets magpie honor
			// the profile's order and fallback chain.
			if providerPart(cfg.MagpieProfile[0].Model) == provider {
				return []string{pick}
			}
			return nil
		}
		return []string{pick}
	}
	return nil
}

// GetAssistantChoice reads who answers and each agent's model and effort.
func (a *App) GetAssistantChoice() (AssistantChoiceView, error) {
	ctx, cancel := context.WithTimeout(a.contextOrBackground(), assistantChoiceTimeout)
	defer cancel()
	return a.assistantChoice(ctx)
}

func (a *App) assistantChoice(ctx context.Context) (AssistantChoiceView, error) {
	cfg := loadAIProviderConfig()
	view := AssistantChoiceView{Answering: answeringAgent(cfg), BackgroundUsesChoice: cfg.BackgroundUsesChoice}
	view.Editable, view.Reason = true, a.assistantChoiceSource()
	view.IsoformReady = a.isoformCanAnswer()
	catalog, listed := a.agentChoices.catalogSnapshot()
	if listed == nil {
		listed = map[string][]string{}
	}
	if a.modelSidecarActive() && a.magpie != nil && a.magpie.Healthy() {
		var models []string
		var steps map[string][]string
		var err error
		if a.magpie.External() {
			models, err = a.magpie.listModels(ctx)
			steps = map[string][]string{}
			models = slices.DeleteFunc(models, func(id string) bool { return strings.HasPrefix(id, "group/") || !magpieModelIDPattern.MatchString(id) })
		} else {
			var out []byte
			out, err = a.magpie.runCatalogCLI(ctx)
			if err == nil {
				models, steps = parseMagpieModelList(string(out))
			}
		}
		if err != nil {
			log.Printf("[assistant-choice] model list unavailable: %v", err)
		} else {
			catalog, listed = models, steps
			a.agentChoices.setCatalog(catalog, listed)
		}
	}
	for _, ca := range choiceAgents {
		row := AgentChoiceView{Agent: ca.id, Models: []string{}, ModelEfforts: map[string][]string{}}
		if ca.cli != "" {
			_, err := lookPathCLI(ca.cli)
			row.NotInstalled = err != nil
		}
		for _, model := range catalog {
			if ca.runs(model) {
				row.Models = append(row.Models, model)
				if steps, ok := listed[model]; ok {
					row.ModelEfforts[model] = steps
				}
			}
		}
		saved := cfg.agentChoice(ca.id)
		row.Model, row.Effort = saved.Model, saved.Effort
		row.Efforts = effortSteps(ca, row.Model, listed)
		if row.Effort == "" {
			row.Effort = nearestEffort(row.Efforts, defaultEffort)
		} else {
			row.Effort = nearestEffort(row.Efforts, row.Effort)
		}
		view.Agents = append(view.Agents, row)
	}
	markCanAnswer(&view)
	a.cacheAgentChoices(view)
	return view, nil
}

// markCanAnswer sets which rows can be picked, the rule SetAnsweringAgent
// enforces.
func markCanAnswer(view *AssistantChoiceView) {
	for i := range view.Agents {
		_, blocked := unpickableAgents[view.Agents[i].Agent]
		view.Agents[i].CanAnswer = !blocked
	}
}

// lookPathCLI finds an agent's CLI; tests replace it.
var lookPathCLI = exec.LookPath

// cacheAgentChoices keeps the agent picks for runs.
func (a *App) cacheAgentChoices(view AssistantChoiceView) {
	choices := make(map[string]agent.AgentChoice, len(view.Agents))
	for _, row := range view.Agents {
		if row.Agent == answeringIsoform || row.Agent == answeringDeepSeek {
			continue
		}
		choice := agent.AgentChoice{Effort: row.Effort}
		if row.Model != "" {
			choice.Model = runModel(row.Model)
		}
		choices[row.Agent] = choice
	}
	a.agentChoices.set(choices)
	a.agentChoices.setView(view)
}

// runChoice is the model and effort one Claude Code or Codex run uses. Light
// tasks (titles, extraction: the "haiku" tier) always keep their own cheap
// model; other background work stays on the lighter default unless the user
// chose to use their pick for it too; everything else uses the pick, at High
// until its choice has been read.
func (a *App) runChoice(agentID string, input *agent.TaskInput) agent.AgentChoice {
	if input != nil && strings.Contains(strings.ToLower(input.Model), "haiku") && (input.Priority != agent.PriorityBackground || !a.backgroundUsesChoice.Load()) {
		return agent.AgentChoice{}
	}
	if input != nil && input.Priority == agent.PriorityBackground && !a.backgroundUsesChoice.Load() {
		return backgroundChoices[agentID]
	}
	provider := agentID
	if agentID == string(agent.BackendClaudeCode) {
		provider = "claude"
	}
	for _, member := range loadAIProviderConfig().MagpieProfile {
		if providerPart(member.Model) == provider {
			return agent.AgentChoice{Model: runModel(member.Model), Effort: member.Effort}
		}
	}
	choice, ok := a.agentChoices.lookup(agentID)
	if !ok {
		saved := loadAIProviderConfig().agentChoice(agentID)
		choice = agent.AgentChoice{Model: runModel(saved.Model), Effort: saved.Effort}
		if choice.Effort == "" {
			choice.Effort = defaultEffort
		}
	}
	return choice
}

// refreshAgentChoices reloads the cache after the gateway starts or Bootstrap.
func (a *App) refreshAgentChoices(ctx context.Context) {
	a.backgroundUsesChoice.Store(loadAIProviderConfig().BackgroundUsesChoice)
	if _, err := a.assistantChoice(ctx); err != nil {
		log.Printf("[assistant-choice] read failed: %v", err)
	}
}

// quickAssistantChoice is the view after a change magpie does not hold (the
// pick, the background switch): the last view read with the saved config,
// so saving never waits on the magpie CLI. It reads everything when there is
// no last view or the gateway's state changed.
func (a *App) quickAssistantChoice() (AssistantChoiceView, error) {
	view, ok := a.agentChoices.lastView()
	reason := a.assistantChoiceSource()
	if !ok || view.Reason != reason {
		return a.GetAssistantChoice()
	}
	cfg := loadAIProviderConfig()
	view.Answering, view.BackgroundUsesChoice = answeringAgent(cfg), cfg.BackgroundUsesChoice
	view.Reason, view.IsoformReady = reason, a.isoformCanAnswer()
	markCanAnswer(&view)
	a.agentChoices.setView(view)
	return view, nil
}

// SetAnsweringAgent picks who answers in Isoform: claude-code, codex or
// isoform. Isoform answers whenever the pick cannot. The pick changes no
// magpie group and no subscription order. An agent that cannot answer in
// Isoform yet (Codex, DeepSeek Harness) is refused.
func (a *App) SetAnsweringAgent(agentID string) (AssistantChoiceView, error) {
	if _, ok := findChoiceAgent(agentID); !ok {
		view, _ := a.quickAssistantChoice()
		return view, fmt.Errorf("unknown agent %q", agentID)
	}
	if reason, blocked := unpickableAgents[agentID]; blocked {
		view, _ := a.quickAssistantChoice()
		return view, errors.New(reason)
	}
	_, err := updateAIProviderConfig(func(cfg *AIProviderConfig) error {
		cfg.AnswerWith = agentID
		return nil
	})
	if err != nil {
		return AssistantChoiceView{}, err
	}
	a.assistantChoiceChanged()
	return a.quickAssistantChoice()
}

// SetBackgroundUsesChoice turns "Use my choice for background work too" on or
// off. It applies to the next background run.
func (a *App) SetBackgroundUsesChoice(on bool) (AssistantChoiceView, error) {
	_, err := updateAIProviderConfig(func(cfg *AIProviderConfig) error {
		cfg.BackgroundUsesChoice = on
		return nil
	})
	if err != nil {
		return AssistantChoiceView{}, err
	}
	a.backgroundUsesChoice.Store(on)
	a.assistantChoiceChanged()
	return a.quickAssistantChoice()
}

// SetAgentModel saves an agent's model; "" puts it back on its own default.
func (a *App) SetAgentModel(agentID, model string) (AssistantChoiceView, error) {
	return a.setAgentChoice(agentID, "model", model)
}

// SetAgentEffort saves an agent's reasoning effort; "" is the default (High).
func (a *App) SetAgentEffort(agentID, effort string) (AssistantChoiceView, error) {
	return a.setAgentChoice(agentID, "effort", effort)
}

func (a *App) setAgentChoice(agentID, field, value string) (AssistantChoiceView, error) {
	ctx, cancel := context.WithTimeout(a.contextOrBackground(), assistantChoiceTimeout)
	defer cancel()
	ca, ok := findChoiceAgent(agentID)
	if !ok {
		return AssistantChoiceView{}, fmt.Errorf("unknown agent %q", agentID)
	}
	if ca.id == answeringDeepSeek {
		view, _ := a.quickAssistantChoice()
		return view, fmt.Errorf("%s has no model or effort to choose yet", agentID)
	}
	value = strings.TrimSpace(value)
	current, err := a.assistantChoice(ctx)
	if err != nil {
		return current, err
	}
	if value != "" {
		var allowed []string
		for _, row := range current.Agents {
			if row.Agent != agentID {
				continue
			}
			allowed = row.Efforts
			if field == "model" {
				allowed = row.Models
			}
		}
		if !slices.Contains(allowed, value) {
			return current, fmt.Errorf("%s cannot use %s %q", agentID, field, value)
		}
	}
	if field == "effort" && value == defaultEffort && !(agentID == answeringIsoform && !a.modelSidecarActive()) {
		// High is the managed default; an API key may need an explicit effort.
		value = ""
	}
	// The pick lives in Isoform's own config; nothing is written into
	// magpie's per-agent settings anymore.
	_, err = updateAIProviderConfig(func(cfg *AIProviderConfig) error {
		choice := cfg.agentChoice(agentID)
		if field == "model" {
			choice.Model = value
		} else {
			choice.Effort = value
		}
		cfg.setAgentChoice(agentID, choice)
		return nil
	})
	if err != nil {
		return current, err
	}
	if ca.id == answeringIsoform {
		if err := a.applyIsoformChoiceNow(ctx); err != nil {
			log.Printf("[assistant-choice] group update deferred: %v", err)
		}
	} else {
		// Runs see the new choice even if reading it back fails.
		chosen := a.agentChoices.get(agentID)
		if field == "model" {
			chosen.Model = runModel(value)
		} else {
			chosen.Effort = value
			if value == "" {
				chosen.Effort = defaultEffort
			}
		}
		a.agentChoices.put(agentID, chosen)
	}
	a.assistantChoiceChanged()
	return a.assistantChoice(ctx)
}

// externalIsoformModel is sent as the model on each external gateway request.
func (a *App) externalIsoformModel() string {
	choice := loadAIProviderConfig().agentChoice(answeringIsoform)
	if choice.Model == "" {
		return ""
	}
	return choice.Model
}

// assistantChoiceChanged applies a new choice to routing and tells Settings.
// Runs read the model and effort when they start.
func (a *App) assistantChoiceChanged() {
	a.refreshAssistantRoutingPolicy()
	noteResearchConfigChanged()
	a.emitBrowserEvent("ai:config-changed")
}

// applyIsoformChoiceNow reorders Isoform's group for a new Isoform choice
// without a full Bootstrap; the subscription order and the planned chat
// members stay as the last Bootstrap left them.
func (a *App) applyIsoformChoiceNow(ctx context.Context) error {
	if a.magpie == nil || !a.magpie.Healthy() || a.magpie.External() || len(loadAIProviderConfig().MagpieProfile) > 0 {
		return nil
	}
	// Bootstrap rewrites the same group; one at a time, so neither writes
	// back the other's older pick.
	a.magpie.bootstrapMu.Lock()
	defer a.magpie.bootstrapMu.Unlock()
	if len(loadAIProviderConfig().MagpieProfile) > 0 {
		return nil
	}
	var planned []string
	for _, group := range a.magpieModels.snapshot().Groups {
		if group.ID == "yansu-chat" {
			planned = group.Members
		}
	}
	groups := a.magpie.readGroups(ctx)
	choice := loadAIProviderConfig().agentChoice(answeringIsoform)
	if !hasGroup(groups, magpieIsoformGroupID) {
		groups = a.magpie.ensureIsoformGroup(ctx, groups, applyIsoformChoice(planned, choice))
	}
	for _, group := range a.magpie.applyIsoformOrder(ctx, groups, planned, loadAIProviderConfig().managedOrder(), choice) {
		if group.ID == magpieIsoformGroupID && choice.Model != "" && (len(group.Members) == 0 || memberBase(group.Members[0]) != choice.Model) {
			return fmt.Errorf("magpie did not put %s first in Isoform's group", choice.Model)
		}
	}
	return nil
}

// openSignInTerminal opens Terminal on a script; tests replace it.
var openSignInTerminal = func(script string) error {
	return exec.Command("open", "-a", "Terminal", script).Run()
}

// OpenAssistantSignIn opens Terminal on the agent's own sign-in command
// (`claude auth login`, `codex login`). The user signs in there; setup keeps
// re-checking until the sign-in shows.
func (a *App) OpenAssistantSignIn(agentID string) error {
	command, ok := signInCommand(agentID)
	if !ok {
		return fmt.Errorf("unknown assistant %q", agentID)
	}
	if runtime.GOOS == "windows" {
		return openWindowsSetupTerminal(command)
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("open a terminal and run %s", strings.Join(command, " "))
	}
	if path, err := exec.LookPath(command[0]); err == nil {
		command = append([]string{path}, command[1:]...)
	}
	log.Printf("[assistant-setup] %s: opening Terminal on %s", agentID, strings.Join(command, " "))
	return openTerminalCommand(agentID, command)
}

// signInCommand returns the agent's sign-in command. Settings names Claude
// Code "claude-code" and setup names it "claude"; both open the same sign-in.
// (Setup's "claude" once matched nothing, so Sign in always failed there.)
func signInCommand(agentID string) ([]string, bool) {
	if agentID == "claude" {
		agentID = string(agent.BackendClaudeCode)
	}
	command, ok := signInCommands[agentID]
	return append([]string(nil), command...), ok
}

// openTerminalCommand writes command to a script in the user's own temporary
// folder, replaced on each click, and opens Terminal on it. Terminal starts
// it from the user's login shell, so its PATH is the user's own.
func openTerminalCommand(name string, command []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("open a terminal and run %s", strings.Join(command, " "))
	}
	dir := filepath.Join(os.TempDir(), "isoform-sign-in")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	script := filepath.Join(dir, name+".command")
	quoted := make([]string, 0, len(command))
	for _, part := range command {
		quoted = append(quoted, "'"+strings.ReplaceAll(part, "'", `'\''`)+"'")
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+strings.Join(quoted, " ")+"\n"), 0o700); err != nil {
		return err
	}
	return openSignInTerminal(script)
}
