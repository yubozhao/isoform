package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/platform/cmdutil"
)

type SetupAgent struct {
	ID          string `json:"id"`
	Installed   bool   `json:"installed"`
	Version     string `json:"version"`
	SignedIn    bool   `json:"signedIn"`
	Plan        string `json:"plan"`
	NotFoundYet bool   `json:"notFoundYet"`
	ProbedAt    string `json:"probedAt"`
}
type SetupMagpieProbe struct {
	MagpieInstallView
	Downloadable  bool   `json:"downloadable"`
	DownloadBytes int64  `json:"downloadBytes"`
	Required      bool   `json:"required"`
	Installed     bool   `json:"installed"`
	SignedIn      bool   `json:"signedIn"`
	Plan          string `json:"plan"`
	NotFoundYet   bool   `json:"notFoundYet"`
	ProbedAt      string `json:"probedAt"`
}
type AgentProbe struct {
	Agents   []SetupAgent     `json:"agents"`
	Magpie   SetupMagpieProbe `json:"magpie"`
	NPM      bool             `json:"npm"`
	Git      GitStatus        `json:"git"`
	ProbedAt string           `json:"probedAt"`
}
type agentProbeCache struct {
	mu            sync.Mutex
	publishMu     sync.Mutex
	once          sync.Once
	result        AgentProbe
	refreshing    bool
	refreshDone   chan struct{}
	refreshed     time.Time
	welcomeCancel context.CancelFunc
	path          string
	probe         func(context.Context, string) SetupAgent
	emit          func(AgentProbe)
	metadata      func() (SetupMagpieProbe, bool)
}

func (c *agentProbeCache) init(path string, probe func(context.Context, string) SetupAgent, emit func(AgentProbe), metadata func() (SetupMagpieProbe, bool)) {
	c.once.Do(func() {
		c.path = path
		c.probe = probe
		c.emit = emit
		c.metadata = metadata
		c.result = AgentProbe{Agents: []SetupAgent{}, Magpie: SetupMagpieProbe{MagpieInstallView: MagpieInstallView{Groups: []ModelGroup{}, AgentModels: map[string]string{}}, NotFoundYet: true}}
		raw, err := os.ReadFile(path)
		if err == nil {
			_ = json.Unmarshal(raw, &c.result)
		}
		if len(c.result.Agents) != 3 {
			c.result.Agents = []SetupAgent{{ID: "claude", NotFoundYet: true}, {ID: "codex", NotFoundYet: true}, {ID: "ollama", NotFoundYet: true}}
		}
	})
}
func (c *agentProbeCache) snapshot() AgentProbe {
	c.mu.Lock()
	r := c.result
	r.Agents = append([]SetupAgent{}, r.Agents...)
	c.mu.Unlock()
	// Git readiness is local-only and can change after a confirmed tool
	// download, so it is resolved fresh on every read rather than cached.
	r.Git = gitReadiness()
	return r
}
func (c *agentProbeCache) refresh(force bool) <-chan struct{} {
	done := make(chan struct{})
	c.mu.Lock()
	if c.refreshing || (!force && time.Since(c.refreshed) < 2*time.Second) {
		c.mu.Unlock()
		close(done)
		return done
	}
	c.refreshing = true
	c.refreshDone = done
	c.mu.Unlock()
	go func() {
		defer func() {
			close(done)
			c.mu.Lock()
			if c.refreshDone == done {
				c.refreshDone = nil
			}
			c.mu.Unlock()
		}()
		var wg sync.WaitGroup
		if c.metadata != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				magpie, npm := c.metadata()
				c.mu.Lock()
				c.result.Magpie = magpie
				c.result.NPM = npm
				c.mu.Unlock()
			}()
		}
		for _, id := range []string{"claude", "codex", "ollama"} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), setupAgentProbeTimeout)
				defer cancel()
				row := c.probe(ctx, id)
				row.ID = id
				row.ProbedAt = time.Now().UTC().Format(time.RFC3339Nano)
				c.mu.Lock()
				for i := range c.result.Agents {
					if c.result.Agents[i].ID == id {
						c.result.Agents[i] = row
					}
				}
				c.result.ProbedAt = row.ProbedAt
				c.mu.Unlock()
				c.publish()
			}(id)
		}
		wg.Wait()
		if c.metadata != nil {
			c.publish()
		}
		c.mu.Lock()
		c.refreshing = false
		c.refreshed = time.Now()
		c.mu.Unlock()
	}()
	return done
}

func (c *agentProbeCache) waitIdle() {
	c.mu.Lock()
	done := c.refreshDone
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}

// setupAgentProbeTimeout bounds one background probe of an assistant
// (`--version`, then its sign-in status). Two seconds read a slow first run of
// a node CLI as "not installed", which kept a fresh install on Install.
const setupAgentProbeTimeout = 8 * time.Second

func (a *App) initAgentProbe() {
	a.setupAgents.init(filepath.Join(configDirOrFallback(), "setup-agent-probe.json"), probeSetupAgent, func(p AgentProbe) { a.emitBrowserEvent("setup:agents-updated", p) }, func() (SetupMagpieProbe, bool) { return a.probeSetupMagpie(), execExists("npm") })
}

// prepareAgentProbe runs on the optional launch worker. A cache costs no probe
// wait; a fresh install spends at most 900 ms while probes finish in background.
func (a *App) prepareAgentProbe() {
	a.initAgentProbe()
	cached := a.setupAgents.snapshot().ProbedAt != ""
	done := a.setupAgents.refresh(true)
	if !cached {
		timer := time.NewTimer(900 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
	}
}
func (a *App) GetAgentProbe() AgentProbe {
	a.initAgentProbe()
	p := a.setupAgents.snapshot()
	p.Magpie.Required = a.modelSidecarActive()
	a.setupAgents.refresh(false)
	return p
}

// SetSetupWelcomeVisible scopes automatic sign-in polling to Welcome. The
// frontend calls false on unmount; Check again may call RefreshAgentProbe.
func (a *App) SetSetupWelcomeVisible(visible bool) {
	a.initAgentProbe()
	c := &a.setupAgents
	c.mu.Lock()
	if c.welcomeCancel != nil {
		c.welcomeCancel()
		c.welcomeCancel = nil
	}
	if !visible {
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.welcomeCancel = cancel
	c.mu.Unlock()
	go func() {
		c.refresh(true)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.refresh(false)
			}
		}
	}()
}
func (a *App) RefreshAgentProbe() { a.initAgentProbe(); a.setupAgents.refresh(true) }
func probeSetupAgent(ctx context.Context, id string) (row SetupAgent) {
	row = SetupAgent{ID: id}
	defer func() {
		if ctx.Err() != nil {
			row.NotFoundYet = true
		}
	}()
	if id == "ollama" {
		row.Installed = execExists("ollama")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:11434/api/version", nil)
		if err != nil {
			return row
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return row
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return row
		}
		var v struct {
			Version string `json:"version"`
		}
		if json.NewDecoder(resp.Body).Decode(&v) == nil {
			row.Installed = true
			row.Version = v.Version
			row.SignedIn = true
			row.Plan = "local"
		}
		return row
	}
	out, err := cliProbeCommand(ctx, id, "--version").CombinedOutput()
	if err != nil {
		return row
	}
	row.Installed = true
	row.Version = strings.TrimSpace(string(out))
	args := []string{"auth", "status"}
	if id == "codex" {
		args = []string{"login", "status"}
	}
	out, err = cliProbeCommand(ctx, id, args...).CombinedOutput()
	row.SignedIn = cliLoggedIn(string(out), err)
	if id == "claude" {
		var v struct {
			SubscriptionType string `json:"subscriptionType"`
		}
		if json.Unmarshal(out, &v) == nil {
			row.Plan = v.SubscriptionType
		}
	} else if row.SignedIn && strings.Contains(strings.ToLower(string(out)), "chatgpt") {
		row.Plan = "ChatGPT"
	}
	return row
}
func execExists(name string) bool { _, err := exec.LookPath(name); return err == nil }

// Probe only the gateway's read commands. runCLI is intentionally not used:
// it reconciles BYOK configuration, whereas Welcome is observation only.
func (a *App) gatewaySetupAvailability() SetupMagpieProbe {
	result := SetupMagpieProbe{MagpieInstallView: a.magpieInstallView(), ProbedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	result.Required = a.modelSidecarActive()
	if entry, err := magpieDownloadEntry(runtime.GOOS + "/" + runtime.GOARCH); err == nil {
		result.Downloadable, result.DownloadBytes = true, entry.SizeBytes
	}
	_, err := LocateBundledMagpie()
	result.Installed = err == nil
	return result
}

func (a *App) probeSetupMagpie() (result SetupMagpieProbe) {
	result = a.gatewaySetupAvailability()
	bin, err := LocateBundledMagpie()
	if in, ok := detectUserMagpieInstall(); ok {
		bin = in.BinaryPath
		err = nil
	}
	if err != nil {
		return result
	}
	result.Installed = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			result.NotFoundYet = true
		}
	}()
	run := func(args ...string) ([]byte, error) {
		cmd := cmdutil.CommandContext(ctx, bin, args...)
		if result.Kind == "bundled" && a.magpie != nil {
			cmd.Env = a.magpie.commandEnv(a.magpie.Status().Port)
		}
		return cmd.Output()
	}
	if out, err := run("version"); err == nil {
		result.Version = strings.TrimSpace(string(out))
	}
	if out, err := run("groups"); err == nil {
		groups := parseMagpieGroups(string(out))
		result.Groups = []ModelGroup{}
		for _, g := range groups {
			result.Groups = append(result.Groups, ModelGroup{ID: g.ID, Name: g.Name, Routing: g.Routing, Members: g.Members})
		}
	}
	if out, err := run("accounts", "--json"); err == nil {
		var accounts []struct {
			On   bool   `json:"on"`
			Plan string `json:"plan"`
		}
		if json.Unmarshal(out, &accounts) == nil {
			for _, account := range accounts {
				if account.On {
					result.SignedIn = true
					if account.Plan != "" {
						result.Plan = account.Plan
						break
					}
				}
			}
		}
	}
	return result
}

// Disk sync and event delivery never hold the read lock used by Welcome.
func (c *agentProbeCache) publish() {
	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	snapshot := c.snapshot()
	_ = saveSetupJSON(c.path, snapshot)
	if c.emit != nil {
		c.emit(snapshot)
	}
}
