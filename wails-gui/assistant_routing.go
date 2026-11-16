package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"wails-gui/storage"
	"yansu-agent/agent"
)

const assistantProbeTTL = time.Minute

// assistantProbeCache shares one bounded sign-in/control probe across all
// concurrent runs. Order is read separately, so changing it takes effect now.
type assistantProbeCache struct {
	mu      sync.Mutex
	states  map[string]agent.AssistantState
	expires time.Time
	flight  chan struct{}
	probe   func(context.Context) map[string]agent.AssistantState
}

// invalidate makes the next snapshot read sign-in and version again.
func (c *assistantProbeCache) invalidate() {
	c.mu.Lock()
	c.expires = time.Time{}
	c.mu.Unlock()
}

func (c *assistantProbeCache) snapshot(ctx context.Context) map[string]agent.AssistantState {
	for {
		if ctx.Err() != nil {
			return nil
		}
		c.mu.Lock()
		if time.Now().Before(c.expires) {
			states := cloneAssistantStates(c.states)
			c.mu.Unlock()
			return states
		}
		if flight := c.flight; flight != nil {
			c.mu.Unlock()
			select {
			case <-flight:
				continue
			case <-ctx.Done():
				return nil
			}
		}
		c.flight = make(chan struct{})
		flight := c.flight
		c.mu.Unlock()
		states := c.probe(ctx)
		c.mu.Lock()
		// Cancelling this caller is not evidence that the assistant is
		// unhealthy. Wake live waiters without caching its aborted probe.
		if ctx.Err() == nil {
			c.states = cloneAssistantStates(states)
			c.expires = time.Now().Add(assistantProbeTTL)
		}
		c.flight = nil
		close(flight)
		c.mu.Unlock()
		if ctx.Err() != nil {
			return nil
		}
		return states
	}
}

func cloneAssistantStates(states map[string]agent.AssistantState) map[string]agent.AssistantState {
	copy := make(map[string]agent.AssistantState, len(states))
	for name, state := range states {
		copy[name] = state
	}
	return copy
}

func assistantFormatOrder(cfg AIProviderConfig) []string {
	order := make([]string, 0, len(cfg.managedOrder()))
	for _, provider := range cfg.managedOrder() {
		if provider == "claude" {
			provider = "claude-code"
		}
		order = append(order, provider)
	}
	return order
}

func (a *App) buildAssistantRoutingRunner(native *agent.NativeAgentRunner, allowed bool) *agent.RoutingAgentRunner {
	// Old versions exported user MCP credentials directly to the CLI. The
	// per-run server now mediates every tool, including user MCP tools.
	removeExternalMcpConfig()
	configs := make(map[string]agent.ExternalAgentConfig)
	runners := make(map[string]agent.AgentRunner)
	if allowed {
		provider := loadAIProviderConfig()
		for _, format := range []string{"claude-code", "codex"} {
			cfg := provider.assistantRunnerConfig(format)
			// The advanced CLI sandbox setting does not apply to this route.
			// Controlled runners independently reject any bypass configuration.
			cfg.DisableSandbox = false
			cfg.ToolServerFactory = native.NewToolServer
			cfg.ToolServerSupports = native.ToolServerSupports
			cfg.RespondToPermission = native.RespondToPermission
			cfg.CancelPermission = native.CancelPermission
			// The CLI is installed by a launch task that can finish after
			// this runner is built (a new data folder, the first launch). The
			// router keeps the path it will be at, so Claude Code and Codex
			// are not reported unavailable until the next restart.
			cfg.ToolShimCommand = assistantToolShimPath()
			agentID := format
			cfg.Choice = func(input *agent.TaskInput) agent.AgentChoice { return a.runChoice(agentID, input) }
			configs[format] = cfg
			runners[format] = agent.NewExternalAgentRunner(cfg)
		}
	}
	cache := &assistantProbeCache{probe: func(ctx context.Context) map[string]agent.AssistantState {
		states := make(map[string]agent.AssistantState, len(configs))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for format, cfg := range configs {
			wg.Add(1)
			go func(format string, cfg agent.ExternalAgentConfig) {
				defer wg.Done()
				state := assistantRouteProbe(ctx, cfg)
				mu.Lock()
				states[format] = state
				mu.Unlock()
			}(format, cfg)
		}
		wg.Wait()
		// R75: every probe reading also feeds the signed-out debounce.
		a.observeAssistantSignIn(states)
		return states
	}}
	policy := func(ctx context.Context) agent.RoutingPolicy {
		// Only the pick is tried, then Isoform: one pick with Isoform as
		// the backup.
		cfg := loadAIProviderConfig()
		return agent.RoutingPolicy{
			ManagedOrder: routeOrder(cfg),
			Assistants:   cache.snapshot(ctx),
			NativeOnly:   answeringAgent(cfg) == answeringIsoform,
		}
	}
	router := agent.NewOrderedRoutingAgentRunner(native, runners, agent.RoutingPolicy{})
	router.SetPolicyProvider(policy)
	router.SetObserver(a.recordAgentRunRoute)
	router.SetRunScope(a.issueAgentRunScope)
	router.SetHealthObserver(a.assistantHealthDidChange)
	a.mu.Lock()
	a.assistantProbes = cache
	a.mu.Unlock()
	return router
}

// Record provenance independently of the caller's UI: chat, schedules and
// internal jobs all use this observer, including failed assistant attempts.
func (a *App) recordAgentRunRoute(event *agent.StreamEvent) error {
	if a.store == nil || event == nil || event.RunID == "" {
		return nil
	}
	status := ""
	switch event.Type {
	case "run_created":
		status = "running"
	case "done":
		status = "done"
	case "error":
		status = "error"
	case "model_route", "action_started":
	default:
		return nil
	}
	if event.Blocked {
		status = "blocked"
	}
	reason := event.BackendReason
	if event.Error != "" {
		if reason != "" {
			reason += "; "
		}
		reason += event.Error
	}
	candidates, _ := json.Marshal(event.CandidateBackends)
	fallback, _ := json.Marshal(event.FallbackFrom)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return a.store.RecordAgentRunRoute(ctx, storage.AgentRunRouteRow{
		RunID: event.RunID, SessionID: event.SessionID, Backend: event.Backend,
		CandidateBackends: string(candidates), FallbackFrom: string(fallback),
		BackendReason: reason, Model: event.ServedModel,
		Status: status, ActionsStarted: event.ActionsStarted,
	})
}

// assistantRouteProbe is the probe the router uses; tests replace it so a
// plain go test never starts the user's installed claude or codex
// (testmain_cli_test.go).
var assistantRouteProbe = probeAssistantRoute

func probeAssistantRoute(ctx context.Context, cfg agent.ExternalAgentConfig) agent.AssistantState {
	if !filepath.IsAbs(cfg.ToolShimCommand) {
		return agent.AssistantState{Reason: "Isoform tool shim is unavailable"}
	}
	command := cfg.Command
	if command == "" {
		command = "claude"
		if cfg.Format == "codex" {
			command = "codex"
		}
	}
	if _, err := exec.LookPath(command); err != nil {
		return agent.AssistantState{Reason: "binary missing"}
	}
	state := agent.AssistantState{Available: true}
	signedIn, err := agent.ExternalAgentSignedIn(cfg)
	if err != nil {
		state.Reason = "sign-in probe failed"
		return state
	}
	if !signedIn {
		state.Reason = "signed out"
		return state
	}
	state.SignedIn = true
	version, err := agent.ExternalControlProfileVersion(ctx, cfg)
	state.Version = version
	if err != nil {
		state.Reason = err.Error()
		return state
	}
	state.ControlProfile = true
	return state
}

// assistantHealthChangedEvent tells Settings to read the health lines again.
const assistantHealthChangedEvent = "assistant-health:changed"

// A health change wakes the brief scheduler; its normal tick also catches a
// sign-in made outside Isoform, where there may be no health event yet.
func (a *App) assistantHealthDidChange() {
	a.emitBrowserEvent(assistantHealthChangedEvent)
	if service, ok := a.mobileBriefSource().(*mobileBriefService); ok {
		select {
		case service.wake <- struct{}{}:
		default:
		}
	}
}

// AssistantHealthView is one assistant's health line in Settings. Code names
// the state; the page words it. Diagnostics stay in the log.
type AssistantHealthView struct {
	Assistant string `json:"assistant"`
	Code      string `json:"code"`
	Checking  bool   `json:"checking"`
	RetryAt   string `json:"retryAt,omitempty"`
}

func (a *App) assistantRouter() *agent.RoutingAgentRunner {
	a.mu.Lock()
	defer a.mu.Unlock()
	router, _ := a.runner.(*agent.RoutingAgentRunner)
	return router
}

// GetAssistantHealth lists the signed-in assistants Isoform can route to, in
// the user's order, with whether each answers now and why not.
func (a *App) GetAssistantHealth() []AssistantHealthView {
	router := a.assistantRouter()
	result := make([]AssistantHealthView, 0, 2)
	if router == nil {
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, health := range router.AssistantHealth(ctx) {
		view := AssistantHealthView{Assistant: health.Format, Code: health.Code, Checking: health.Checking}
		if !health.RetryAt.IsZero() {
			view.RetryAt = health.RetryAt.UTC().Format(time.RFC3339)
		}
		result = append(result, view)
	}
	return result
}

// RetryAssistantHealth reads sign-in and version again (a few seconds, in this
// call) and checks a paused assistant now. That live check runs in the
// background; Settings hears assistant-health:changed when it ends.
func (a *App) RetryAssistantHealth(assistant string) []AssistantHealthView {
	router := a.assistantRouter()
	if router == nil {
		return []AssistantHealthView{}
	}
	a.mu.Lock()
	cache := a.assistantProbes
	a.mu.Unlock()
	if cache != nil {
		cache.invalidate()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	router.RefreshPolicy(ctx)
	cancel()
	router.RetryAssistant(assistant)
	return a.GetAssistantHealth()
}

func removeExternalMcpConfig() {
	home, err := flavorDataDir()
	if err != nil {
		return
	}
	if err := os.Remove(filepath.Join(home, "external-mcp.json")); err != nil && !os.IsNotExist(err) {
		log.Printf("[assistant-routing] remove obsolete MCP passthrough: %v", err)
	}
}

func (a *App) refreshAssistantRoutingPolicy() {
	a.mu.Lock()
	router, ok := a.runner.(*agent.RoutingAgentRunner)
	a.mu.Unlock()
	if ok {
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		router.RefreshPolicy(ctx)
	}
}

// mobileBriefAssistantState uses the router's bounded, shared CLI sign-in probe.
func (a *App) mobileBriefAssistantState(ctx context.Context, assistant string) string {
	a.mu.Lock()
	cache := a.assistantProbes
	a.mu.Unlock()
	if cache == nil {
		return ""
	}
	state, ok := cache.snapshot(ctx)[assistant]
	if !ok {
		return ""
	}
	if state.SignedIn {
		return "signed_in"
	}
	if state.Available && state.Reason == "signed out" {
		return "signed_out"
	}
	return ""
}
