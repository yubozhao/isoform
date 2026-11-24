package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"time"

	wmcp "wails-gui/internal/mcp"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"wails-gui/internal/privacy"
)

// fallbackComposioApps keeps local/dev Composio testing available when the
// backend does not yet return apps metadata.
var fallbackComposioApps = []ComposioAppMetadata{
	{App: "googlesuper", DisplayName: "Google Super", Configured: true},
}

const (
	composioGoogleSuperApp      = "googlesuper"
	composioGoogleSuperServerID = "composio-googlesuper"
	composioSlackBotApp         = "slackbot"
	composioSlackBotServerID    = "composio-slackbot"
	composioNotionApp           = "notion"
	composioNotionServerID      = "composio-notion"
	composioGitHubApp           = "github"
	composioGitHubServerID      = "composio-github"
	composioLinearApp           = "linear"
	composioLinearServerID      = "composio-linear"
)

var composioManagedApps = []string{
	composioGoogleSuperApp,
	composioSlackBotApp,
	composioNotionApp,
	composioGitHubApp,
	composioLinearApp,
}

type ComposioAppMetadata struct {
	App         string `json:"app"`
	DisplayName string `json:"display_name"`
	Configured  bool   `json:"configured"`
}

// ComposioConnectionInfo is the per-app status returned to the frontend.
type ComposioConnectionInfo struct {
	App         string `json:"app"`
	DisplayName string `json:"display_name"`
	Configured  bool   `json:"configured"`
	Connected   bool   `json:"connected"`
	Status      string `json:"status"`
	// MCPActive is true when this desktop process has an initialized MCP
	// client for this app. The backend may report Connected=true while
	// MCPActive is false until ConnectComposioApp completes locally.
	MCPActive bool `json:"mcp_active"`
	// LastActivationError carries the most recent error string from
	// activateComposioApp. Cleared on the next successful activation.
	// Exposed so the UI can surface why activation has not completed
	// instead of leaving the user staring at a silent "Activating" state.
	LastActivationError string `json:"last_activation_error,omitempty"`
}

// ComposioStatus is the aggregate response for GetComposioStatus.
type ComposioStatus struct {
	Configured    bool                     `json:"configured"`
	Authenticated bool                     `json:"authenticated"`
	Connections   []ComposioConnectionInfo `json:"connections"`
	SupportedApps []ComposioAppMetadata    `json:"supported_apps"`
}

// composioStatusResponse mirrors the backend GET /api/composio/status JSON.
type composioStatusResponse struct {
	Configured  bool                        `json:"configured"`
	Apps        []ComposioAppMetadata       `json:"apps"`
	Connections []composioBackendConnection `json:"connections"`
}

type composioBackendConnection struct {
	App         string `json:"app"`
	Connected   bool   `json:"connected"`
	Status      string `json:"status"`
	ConnectedAt string `json:"connected_at"`
}

// composioConnectResponse mirrors the backend POST /api/composio/connect JSON.
type composioConnectResponse struct {
	RedirectURL string `json:"redirect_url"`
}

// composioMCPURLResponse mirrors the backend GET /api/composio/mcp-url JSON.
type composioMCPURLResponse struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type ComposioTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema,omitempty"`
}

type ComposioToolInfo struct {
	Server string       `json:"server"`
	Tool   ComposioTool `json:"tool"`
}

// composioBackendFetcher calls the yansu backend for Composio MCP metadata.
// The Composio API key never leaves the server; we receive a per-user backend
// proxy URL and attach the normal yansu Bearer token at runtime.
type composioBackendFetcher struct{ app *App }

func (f *composioBackendFetcher) GetMCPConfig(ctx context.Context, app string) (string, map[string]string, error) {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return "", nil, fmt.Errorf("not authenticated")
	}
	var resp composioMCPURLResponse
	path := "/api/composio/mcp-url?app=" + url.QueryEscape(app)
	if err := apiGet(loadAPIBaseURL(), token, path, &resp); err != nil {
		return "", nil, err
	}
	if resp.URL == "" {
		return "", nil, fmt.Errorf("backend returned empty MCP URL for %s", app)
	}
	headers := resp.Headers
	if len(headers) == 0 {
		headers, err = f.AuthHeaders(ctx)
		if err != nil {
			return "", nil, err
		}
	}
	return resp.URL, headers, nil
}

// AuthHeaders returns headers the SSE client should attach on every request.
// Required by the backend MCP proxy at /api/composio/mcp/{app}/sse, which
// authenticates the desktop via the standard Bearer token (same as apiGet).
func (f *composioBackendFetcher) AuthHeaders(ctx context.Context) (map[string]string, error) {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return nil, fmt.Errorf("not authenticated")
	}
	return map[string]string{
		"Authorization": "Bearer " + token,
		"User-Agent":    "yansu-gui/1.0",
	}, nil
}

// GetComposioStatus reports which Composio apps are connected on the backend
// and whether this desktop process has an active MCP client for each.
func (a *App) GetComposioStatus() ComposioStatus {
	if !managedComposioAppAccessEnabled {
		return ComposioStatus{
			Configured:    false,
			Authenticated: false,
			Connections:   []ComposioConnectionInfo{},
			SupportedApps: []ComposioAppMetadata{},
		}
	}
	out := ComposioStatus{
		SupportedApps: append([]ComposioAppMetadata{}, fallbackComposioApps...),
		Connections:   buildComposioStatus(composioStatusResponse{}, nil, nil).Connections,
	}

	// Authenticated: direct (BYO key) mode is gated by the local key; backend
	// mode by the Yansu token.
	if useDirectComposio() {
		out.Authenticated = composioByokConfigured()
	} else if token, err := loadYansuToken(); err == nil && token != "" {
		out.Authenticated = true
	}
	if !out.Authenticated {
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := a.composioClient().Status(ctx)
	if err != nil {
		// Backend down / not configured / direct API unreachable — treat as unconfigured.
		return out
	}
	out.Configured = resp.Configured

	active := map[string]bool{}
	for _, app := range composioAppsFromStatus(resp) {
		active[app.App] = a.composioMCPActive(app.App)
	}
	errs := a.snapshotComposioActivationErrors()
	status := buildComposioStatus(resp, active, errs)
	status.Authenticated = out.Authenticated
	return status
}

func buildComposioStatus(resp composioStatusResponse, active map[string]bool, errs map[string]string) ComposioStatus {
	apps := composioAppsFromStatus(resp)
	backend := map[string]composioBackendConnection{}
	for _, c := range resp.Connections {
		backend[c.App] = c
	}

	connections := make([]ComposioConnectionInfo, 0, len(apps))
	for _, app := range apps {
		conn := backend[app.App]
		info := ComposioConnectionInfo{
			App:         app.App,
			DisplayName: app.DisplayName,
			Configured:  app.Configured,
			Connected:   conn.Connected,
			Status:      conn.Status,
			MCPActive:   active[app.App],
		}
		// Only surface the last error while the app is still inactive —
		// once the MCP client is healthy the prior error is stale and
		// confusing.
		if !info.MCPActive {
			info.LastActivationError = errs[app.App]
		}
		connections = append(connections, info)
	}

	return ComposioStatus{
		Configured:    resp.Configured,
		Connections:   connections,
		SupportedApps: apps,
	}
}

// recordComposioActivationError stores err's string under app so the next
// GetComposioStatus call can surface it to the UI. Passing nil clears the slot.
func (a *App) recordComposioActivationError(app string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.composioActivationErrs == nil {
		a.composioActivationErrs = make(map[string]string)
	}
	if err == nil {
		delete(a.composioActivationErrs, app)
		return
	}
	a.composioActivationErrs[app] = err.Error()
}

// snapshotComposioActivationErrors returns a defensive copy of the per-app
// error map. Callers may iterate the result without holding a.mu.
func (a *App) snapshotComposioActivationErrors() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.composioActivationErrs) == 0 {
		return nil
	}
	out := make(map[string]string, len(a.composioActivationErrs))
	for k, v := range a.composioActivationErrs {
		out[k] = v
	}
	return out
}

func composioAppsFromStatus(resp composioStatusResponse) []ComposioAppMetadata {
	if len(resp.Apps) == 0 {
		return append([]ComposioAppMetadata{}, fallbackComposioApps...)
	}
	apps := make([]ComposioAppMetadata, 0, len(resp.Apps))
	for _, app := range resp.Apps {
		if app.App == "" {
			continue
		}
		if app.DisplayName == "" {
			app.DisplayName = app.App
		}
		apps = append(apps, app)
	}
	if len(apps) == 0 {
		return append([]ComposioAppMetadata{}, fallbackComposioApps...)
	}
	return apps
}

// ConnectComposioApp asks the backend to start a managed-OAuth flow for app
// and opens the returned redirect URL in the user's browser. The desktop
// does not wait for the OAuth to complete here — callers should poll
// GetComposioStatus and call ActivateComposioApp once the backend reports the
// connection as ready.
func (a *App) ConnectComposioApp(app string) error {
	if !managedComposioAppAccessEnabled {
		return fmt.Errorf("provider OAuth is retired; use Isoform Computer Use with the existing signed-in app session")
	}
	if app == "" {
		return fmt.Errorf("app is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	redirect, err := a.composioClient().Connect(ctx, app)
	if err != nil {
		return err
	}
	if redirect == "" {
		return fmt.Errorf("did not return a redirect URL")
	}
	runtime.BrowserOpenURL(a.ctx, redirect)
	log.Printf("[composio] opened OAuth redirect for %s", app)
	return nil
}

// ActivateComposioApp fetches the signed MCP URL for app and brings up an
// MCP client locally. Idempotent — calling twice replaces the existing client.
func (a *App) ActivateComposioApp(app string) error {
	if !managedComposioAppAccessEnabled {
		return fmt.Errorf("managed app MCP is retired; app access is computer_use_only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.activateComposioApp(ctx, app, true)
}

func (a *App) ensureConnectedComposioMCPActiveInBackground(reason string) {
	if !managedComposioAppAccessEnabled {
		return
	}
	go func() {
		status := a.GetComposioStatus()
		a.ensureConnectedComposioMCPActive(status, reason)
	}()
}

// composioStartupRetryDelays controls the backoff between background
// activation attempts. Composio's hosted MCP servers occasionally return
// 5xx during initialize right after a fresh OAuth or when the upstream
// service is warming up; without retry the user is stuck looking at
// "Activating" until they reopen Settings, which feels like a hang.
var composioStartupRetryDelays = []time.Duration{2 * time.Second, 8 * time.Second, 20 * time.Second}

func (a *App) ensureConnectedComposioMCPActive(status ComposioStatus, reason string) {
	if !status.Authenticated || !status.Configured {
		return
	}
	// One goroutine per app so a slow/failing app (worst case ~150s from
	// the 4×30s timeouts plus backoff sleeps) does not delay activation of
	// the others. Each app already has its own singleflight slot
	// (composioActivationSF keyed by app), so concurrent activation is
	// safe.
	for _, app := range composioAppsNeedingActivation(status) {
		go a.activateComposioAppWithRetry(app, reason)
	}
}

// activateComposioAppWithRetry attempts activation with exponential backoff.
// Each attempt is bounded by its own 30s timeout so a slow upstream cannot
// stall the whole sequence. We stop at the first success; on terminal failure
// the recorded error remains in composioActivationErrs for the UI to surface.
//
// The loop is parented to a.ctx so shutdown aborts pending sleeps and
// in-flight attempts immediately, instead of relying on the 5s os.Exit
// watchdog in App.shutdown to kill the goroutine — that watchdog is meant
// for genuinely stuck cgo threads, not for clean Go-only work like this.
func (a *App) activateComposioAppWithRetry(app, reason string) {
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	for i := 0; i <= len(composioStartupRetryDelays); i++ {
		if parent.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		err := a.activateComposioApp(ctx, app, false)
		cancel()
		if err == nil {
			return
		}
		if i == len(composioStartupRetryDelays) {
			log.Printf("[composio] ensure active %q (%s) gave up after %d attempts: %v", app, reason, i+1, err)
			return
		}
		log.Printf("[composio] ensure active %q (%s) attempt %d failed, retrying in %s: %v", app, reason, i+1, composioStartupRetryDelays[i], err)
		select {
		case <-parent.Done():
			return
		case <-time.After(composioStartupRetryDelays[i]):
		}
	}
}

func composioAppsNeedingActivation(status ComposioStatus) []string {
	if !status.Authenticated || !status.Configured {
		return nil
	}
	var apps []string
	for _, conn := range status.Connections {
		if !conn.Connected || conn.MCPActive || !conn.Configured {
			continue
		}
		apps = append(apps, conn.App)
	}
	return apps
}

func (a *App) activateComposioApp(ctx context.Context, app string, force bool) error {
	if app == "" {
		return fmt.Errorf("app is required")
	}
	serverID, err := composioManagedServerID(app)
	if err != nil {
		return err
	}
	rt := a.runtimeRef()
	if rt == nil {
		return fmt.Errorf("mcp runtime not initialised")
	}
	if !force && a.composioMCPActive(app) {
		a.recordComposioActivationError(app, nil)
		return nil
	}

	_, err, _ = a.composioActivationSF.Do(app, func() (any, error) {
		mcpURL, headers, err := a.composioClient().MCPConfig(ctx, app)
		if err != nil {
			return nil, err
		}
		spec, err := composioManagedSpec(app, mcpURL, headers)
		if err != nil {
			return nil, err
		}
		if err := a.restartMcpSpec(ctx, serverID, spec); err != nil {
			return nil, err
		}
		log.Printf("[composio] managed MCP server %q activated for %s", serverID, app)
		return nil, nil
	})
	a.recordComposioActivationError(app, err)
	return err
}

// DisconnectComposioApp removes legacy local managed-connector state. Once
// managed app access is retired, cleanup must not contact a provider.
func (a *App) DisconnectComposioApp(app string) error {
	if app == "" {
		return fmt.Errorf("app is required")
	}
	serverID, err := composioManagedServerID(app)
	if err != nil {
		return err
	}
	a.unregisterAndStopMcp(serverID)
	defer publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Account, ID: "composio:" + app})
	if !managedComposioAppAccessEnabled {
		err = mutateComposioByok(func(state *composioByokState) {
			delete(state.Connections, app)
		})
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = a.composioClient().Disconnect(ctx, app)
	}
	return err
}

// ListComposioTools returns the tools advertised by every active Composio MCP
// client. Useful for debugging from the settings page.
func (a *App) ListComposioTools() []ComposioToolInfo {
	if !managedComposioAppAccessEnabled {
		return []ComposioToolInfo{}
	}
	rt := a.runtimeRef()
	if rt == nil {
		return []ComposioToolInfo{}
	}
	out := []ComposioToolInfo{}
	for _, app := range composioManagedApps {
		serverID, err := composioManagedServerID(app)
		if err != nil {
			continue
		}
		tools, err := rt.manager.Tools(serverID)
		if err != nil {
			continue
		}
		for _, tool := range tools {
			out = append(out, ComposioToolInfo{
				Server: serverID,
				Tool: ComposioTool{
					Name:        tool.Name,
					Description: tool.Description,
					InputSchema: tool.InputSchema,
				},
			})
		}
	}
	return out
}

// CallComposioTool dispatches a tool call against a registered MCP server.
// Returns the raw JSON-encoded CallToolResult so the frontend can render
// content blocks without a tight Go type binding.
func (a *App) CallComposioTool(server, tool string, args map[string]any) (string, error) {
	if !managedComposioAppAccessEnabled {
		return "", fmt.Errorf("managed app tools are retired; app access is computer_use_only")
	}
	if server == "" || tool == "" {
		return "", fmt.Errorf("server and tool are required")
	}
	serverID, err := normalizeComposioToolServer(server)
	if err != nil {
		return "", err
	}
	rt := a.runtimeRef()
	if rt == nil {
		return "", fmt.Errorf("mcp runtime not initialised")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := rt.manager.CallTool(ctx, serverID, tool, args)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func composioManagedServerID(app string) (string, error) {
	switch app {
	case composioGoogleSuperApp:
		return composioGoogleSuperServerID, nil
	case composioSlackBotApp:
		return composioSlackBotServerID, nil
	case composioNotionApp:
		return composioNotionServerID, nil
	case composioGitHubApp:
		return composioGitHubServerID, nil
	case composioLinearApp:
		return composioLinearServerID, nil
	default:
		return "", fmt.Errorf("unsupported Composio app %q", app)
	}
}

func normalizeComposioToolServer(server string) (string, error) {
	switch server {
	case composioGoogleSuperServerID, "composio:" + composioGoogleSuperApp:
		return composioGoogleSuperServerID, nil
	case composioSlackBotServerID, "composio:" + composioSlackBotApp:
		return composioSlackBotServerID, nil
	case composioNotionServerID, "composio:" + composioNotionApp:
		return composioNotionServerID, nil
	case composioGitHubServerID, "composio:" + composioGitHubApp:
		return composioGitHubServerID, nil
	case composioLinearServerID, "composio:" + composioLinearApp:
		return composioLinearServerID, nil
	default:
		return "", fmt.Errorf("unsupported Composio MCP server %q", server)
	}
}

func composioManagedAppForServerID(id string) (string, bool) {
	switch id {
	case composioGoogleSuperServerID:
		return composioGoogleSuperApp, true
	case composioSlackBotServerID:
		return composioSlackBotApp, true
	case composioNotionServerID:
		return composioNotionApp, true
	case composioGitHubServerID:
		return composioGitHubApp, true
	case composioLinearServerID:
		return composioLinearApp, true
	default:
		return "", false
	}
}

func (a *App) composioMCPActive(app string) bool {
	serverID, err := composioManagedServerID(app)
	if err != nil {
		return false
	}
	rt := a.runtimeRef()
	if rt == nil {
		return false
	}
	health, err := rt.manager.Health(serverID)
	return err == nil && health == wmcp.HealthHealthy
}

func composioActiveFromInfo(app string, infos []wmcp.ServerInfo) bool {
	serverID, err := composioManagedServerID(app)
	if err != nil {
		return false
	}
	for _, info := range infos {
		if info.ID == serverID {
			return info.Health == wmcp.HealthHealthy
		}
	}
	return false
}

func composioManagedSpec(app, mcpURL string, headers map[string]string) (wmcp.Spec, error) {
	serverID, err := composioManagedServerID(app)
	if err != nil {
		return wmcp.Spec{}, err
	}
	copied := make(map[string]string, len(headers))
	for k, v := range headers {
		copied[k] = v
	}
	return wmcp.Spec{
		ID:              serverID,
		ManagedComposio: true,
		Transport:       mcpTransportHTTP,
		URL:             mcpURL,
		Headers:         copied,
	}, nil
}

func configureManagedMcpExposure(bridge *wmcp.Bridge) {
	if !managedComposioAppAccessEnabled {
		return
	}
	if bridge == nil {
		return
	}
	for _, app := range composioManagedApps {
		serverID, err := composioManagedServerID(app)
		if err != nil {
			continue
		}
		if err := bridge.SetExposureMode(serverID, wmcp.ExposureModeGateway); err != nil {
			log.Printf("[mcp] configure managed exposure %q: %v", serverID, err)
		}
	}
}

func isGatewayMcpServer(id string) bool {
	_, ok := composioManagedAppForServerID(id)
	return ok
}
