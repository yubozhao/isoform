package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// composio_direct.go — direct Composio client (Path B). Talks to Composio's API
// with the user's own key (x-api-key), bypassing the Yansu backend. Implements
// composioClient (see composio_client.go).
//
// API version: the request/response shapes here mirror backend/internal/viewmodels
// (which target Composio v3 and are proven against the live API). composioAPIVersion
// is a single constant so a future migration to v3.1 is a one-line flip + a field
// re-check via the first-day smoke (see docs plan §2). Endpoints are written as
// /<composioAPIVersion>/... — never hardcode the version inline.
const composioAPIVersion = "v3" // docs current = v3.1; v3 matches the proven backend shapes — smoke to migrate.

// composioAPIBase is a var (not const) only so tests can point it at a local
// httptest server.
var composioAPIBase = "https://backend.composio.dev"

// composioToolkitSlug maps a yansu app slug to the Composio toolkit slug used in
// auth_config + mcp server creation. For the managed apps these are 1:1.
var composioToolkitSlug = map[string]string{
	composioGoogleSuperApp: "googlesuper",
	composioSlackBotApp:    "slack",
	composioNotionApp:      "notion",
	composioGitHubApp:      "github",
	composioLinearApp:      "linear",
}

func toolkitSlugForApp(app string) string {
	if s, ok := composioToolkitSlug[app]; ok {
		return s
	}
	return app
}

type directComposioClient struct{ app *App }

// ── HTTP plumbing ──

//isoform:destination composio
var composioDirectHTTP = &http.Client{Timeout: 30 * time.Second}

// probeComposioAPIKey does a minimal reachability check with a candidate key
// (used by SaveComposioApiKey before persisting). Rejects only on a definitive
// auth rejection (401/403) or a server outage (5xx); any other reachable
// response means the key was accepted by Composio.
//
//isoform:destination composio
func probeComposioAPIKey(apiKey string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, composioPath("auth_configs?limit=1"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", strings.TrimSpace(apiKey))
	req.Header.Set("Accept", "application/json")
	resp, err := composioDirectHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("invalid API key")
	}
	if resp.StatusCode >= 500 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("composio unavailable (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func composioPath(p string) string {
	return composioAPIBase + "/api/" + composioAPIVersion + "/" + strings.TrimPrefix(p, "/")
}

// do issues an x-api-key authenticated JSON request to Composio. body may be nil;
// out may be nil.
//
//isoform:destination composio
func (c *directComposioClient) do(ctx context.Context, method, p string, body, out any) error {
	apiKey := strings.TrimSpace(loadComposioByok().APIKey)
	if apiKey == "" {
		return fmt.Errorf("composio api key not configured")
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, composioPath(p), reader)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := composioDirectHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("composio API %s %s: %d: %s", method, p, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out != nil && len(respBody) > 0 {
		return json.Unmarshal(respBody, out)
	}
	return nil
}

// ── P3: auth_config (managed) ──

// composioAuthConfigCreateRequest is the minimal managed-auth create body
// (defaults to use_composio_managed_auth — no user OAuth client needed).
type composioAuthConfigCreateRequest struct {
	Toolkit struct {
		Slug string `json:"slug"`
	} `json:"toolkit"`
}

// composioAuthConfigCreateResponse: the id is nested under auth_config (review #2);
// a few API shapes also return it at the top level, so accept both.
type composioAuthConfigCreateResponse struct {
	AuthConfig struct {
		ID string `json:"id"`
	} `json:"auth_config"`
	ID string `json:"id"`
}

func (r composioAuthConfigCreateResponse) id() string {
	if strings.TrimSpace(r.AuthConfig.ID) != "" {
		return r.AuthConfig.ID
	}
	return r.ID
}

// ensureAuthConfig returns a cached auth_config id for the app, creating a
// managed one on first use.
func (c *directComposioClient) ensureAuthConfig(ctx context.Context, app string) (string, error) {
	if conn, ok := loadComposioByok().Connections[app]; ok && strings.TrimSpace(conn.AuthConfigID) != "" {
		return conn.AuthConfigID, nil
	}
	var req composioAuthConfigCreateRequest
	req.Toolkit.Slug = toolkitSlugForApp(app)
	var resp composioAuthConfigCreateResponse
	if err := c.do(ctx, http.MethodPost, "auth_configs", req, &resp); err != nil {
		return "", fmt.Errorf("create auth_config: %w", err)
	}
	acID := resp.id()
	if acID == "" {
		return "", fmt.Errorf("composio did not return an auth_config id")
	}
	if err := mutateComposioByok(func(st *composioByokState) {
		conn := st.Connections[app]
		conn.App = app
		conn.AuthConfigID = acID
		st.Connections[app] = conn
	}); err != nil {
		return "", err
	}
	return acID, nil
}

// ── P4: connect (link) + poll ──

type composioLinkRequest struct {
	AuthConfigID string `json:"auth_config_id"`
	UserID       string `json:"user_id"`
}

type composioLinkResponse struct {
	ID                 string `json:"id"`
	ConnectedAccountID string `json:"connected_account_id"`
	RedirectURL        string `json:"redirect_url"`
	URL                string `json:"url"`
	Status             string `json:"status"`
}

func (r composioLinkResponse) redirect() string {
	if strings.TrimSpace(r.RedirectURL) != "" {
		return r.RedirectURL
	}
	return r.URL
}

func (r composioLinkResponse) accountID() string {
	if strings.TrimSpace(r.ConnectedAccountID) != "" {
		return r.ConnectedAccountID
	}
	return r.ID
}

func (c *directComposioClient) Connect(ctx context.Context, app string) (string, error) {
	acID, err := c.ensureAuthConfig(ctx, app)
	if err != nil {
		return "", err
	}
	entity := loadComposioByok().EntityID
	req := composioLinkRequest{AuthConfigID: acID, UserID: entity}
	var resp composioLinkResponse
	if err := c.do(ctx, http.MethodPost, "connected_accounts/link", req, &resp); err != nil {
		return "", fmt.Errorf("composio link: %w", err)
	}
	redirect := resp.redirect()
	if redirect == "" {
		return "", fmt.Errorf("composio did not return a redirect URL")
	}
	caID := resp.accountID()
	_ = mutateComposioByok(func(st *composioByokState) {
		conn := st.Connections[app]
		conn.App = app
		conn.AuthConfigID = acID
		conn.ConnectedAccountID = caID
		conn.Status = composioStatusConnecting
		st.Connections[app] = conn
	})
	// Background poll: flip to connected once Composio reports ACTIVE, then bring
	// up the MCP client. Readiness lag (~30-60s) is handled by the activation
	// retry path (composioStartupRetryDelays).
	if caID != "" {
		go c.pollConnectionActive(app, caID)
	}
	return redirect, nil
}

// composioConnectedAccount is the subset of a connected_account we read for
// status. Composio statuses: ACTIVE / INITIATED / INITIALIZING / EXPIRED /
// FAILED / INACTIVE.
type composioConnectedAccount struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (c *directComposioClient) fetchAccountStatus(ctx context.Context, caID string) (string, error) {
	var acct composioConnectedAccount
	if err := c.do(ctx, http.MethodGet, "connected_accounts/"+url.PathEscape(caID), nil, &acct); err != nil {
		return "", err
	}
	return acct.Status, nil
}

func (c *directComposioClient) pollConnectionActive(app, caID string) {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		raw, err := c.fetchAccountStatus(ctx, caID)
		cancel()
		if err != nil {
			continue
		}
		projected := composioProjectStatus(raw)
		// Only update if this account is still the current one for the app. The
		// user may have disconnected (clearing Connections[app]) or reconnected
		// with a different account during the up-to-3-minute poll; writing back
		// unconditionally would resurrect a deleted connection or overwrite the
		// new account's status with a stale one.
		stillCurrent := false
		_ = mutateComposioByok(func(st *composioByokState) {
			conn := st.Connections[app]
			if conn.ConnectedAccountID != caID {
				return
			}
			conn.Status = projected
			st.Connections[app] = conn
			stillCurrent = true
		})
		if !stillCurrent {
			log.Printf("[composio-direct] %s account %s no longer current; stopping poll", app, caID)
			return
		}
		if projected == composioStatusConnected {
			log.Printf("[composio-direct] %s connected (account %s); activating MCP", app, caID)
			c.app.activateComposioAppWithRetry(app, "composio-direct-connected")
			return
		}
		if projected == composioStatusDisconnected {
			return
		}
	}
	log.Printf("[composio-direct] %s connection poll timed out (account %s)", app, caID)
}

// ── Disconnect ──

func (c *directComposioClient) Disconnect(ctx context.Context, app string) error {
	conn, ok := loadComposioByok().Connections[app]
	if ok && strings.TrimSpace(conn.ConnectedAccountID) != "" {
		if err := c.do(ctx, http.MethodDelete, "connected_accounts/"+url.PathEscape(conn.ConnectedAccountID), nil, nil); err != nil {
			log.Printf("[composio-direct] delete connected_account %s: %v", conn.ConnectedAccountID, err)
		}
	}
	return mutateComposioByok(func(st *composioByokState) {
		c := st.Connections[app]
		c.ConnectedAccountID = ""
		c.MCPURL = ""
		c.Status = composioStatusDisconnected
		st.Connections[app] = c
	})
}

// ── Status ──

func (c *directComposioClient) Status(ctx context.Context) (composioStatusResponse, error) {
	st := loadComposioByok()
	resp := composioStatusResponse{Configured: strings.TrimSpace(st.APIKey) != ""}
	// App catalog comes from the local managed-apps registry (no backend).
	resp.Apps = composioDirectAppCatalog()
	// Refresh any "connecting" connections so the active transition is reflected
	// even without an in-flight poll goroutine (e.g. after a restart).
	for app, conn := range st.Connections {
		if conn.Status == composioStatusConnecting && strings.TrimSpace(conn.ConnectedAccountID) != "" {
			if raw, err := c.fetchAccountStatus(ctx, conn.ConnectedAccountID); err == nil {
				projected := composioProjectStatus(raw)
				if projected != conn.Status {
					// Only write back if the account we just queried is still the
					// current one for this app. The user may have disconnected or
					// reconnected with a different account during fetchAccountStatus;
					// writing unconditionally would stamp the queried account's
					// status onto a disconnected/replaced connection (mirrors the
					// guard in pollConnectionActive).
					applied := false
					_ = mutateComposioByok(func(s *composioByokState) {
						cc := s.Connections[app]
						if cc.ConnectedAccountID != conn.ConnectedAccountID {
							return
						}
						cc.Status = projected
						s.Connections[app] = cc
						applied = true
					})
					if applied {
						conn.Status = projected
					}
				}
			}
		}
		resp.Connections = append(resp.Connections, composioBackendConnection{
			App:       app,
			Connected: conn.Status == composioStatusConnected,
			Status:    conn.Status,
		})
	}
	return resp, nil
}

func composioDirectAppCatalog() []ComposioAppMetadata {
	out := make([]ComposioAppMetadata, 0, len(composioManagedApps))
	for _, app := range composioManagedApps {
		out = append(out, ComposioAppMetadata{
			App:         app,
			DisplayName: composioDisplayName(app),
			Configured:  true,
		})
	}
	return out
}

// ── P5: MCP config (ensure server → generate → identity params) ──

type composioCreateServerRequest struct {
	Name          string   `json:"name"`
	AuthConfigIDs []string `json:"auth_config_ids"`
	Toolkits      []string `json:"toolkits"`
}

type composioServerResponse struct {
	ID     string `json:"id"`
	MCPURL string `json:"mcp_url"`
	Name   string `json:"name"`
}

type composioGenerateRequest struct {
	MCPServerID           string   `json:"mcp_server_id"`
	ManagedAuthByComposio bool     `json:"managed_auth_by_composio"`
	UserIDs               []string `json:"user_ids,omitempty"`
	ConnectedAccountIDs   []string `json:"connected_account_ids,omitempty"`
}

type composioGenerateResponse struct {
	MCPURL               string   `json:"mcp_url"`
	ConnectedAccountURLs []string `json:"connected_account_urls"`
	UserIDsURLs          []string `json:"user_ids_url"`
}

func (r composioGenerateResponse) pick() string {
	if len(r.ConnectedAccountURLs) > 0 && strings.TrimSpace(r.ConnectedAccountURLs[0]) != "" {
		return r.ConnectedAccountURLs[0]
	}
	if len(r.UserIDsURLs) > 0 && strings.TrimSpace(r.UserIDsURLs[0]) != "" {
		return r.UserIDsURLs[0]
	}
	return r.MCPURL
}

func (c *directComposioClient) ensureServer(ctx context.Context, app, acID string) (string, error) {
	if conn, ok := loadComposioByok().Connections[app]; ok && strings.TrimSpace(conn.ServerID) != "" {
		return conn.ServerID, nil
	}
	name := "yansu-" + app
	req := composioCreateServerRequest{
		Name:          name,
		AuthConfigIDs: []string{acID},
		Toolkits:      []string{toolkitSlugForApp(app)},
	}
	var resp composioServerResponse
	if err := c.do(ctx, http.MethodPost, "mcp/servers", req, &resp); err != nil {
		// The server name is unique per Composio account. If it already exists
		// remotely (e.g. the user cleared the local cache after a prior session),
		// the create hits a unique-name conflict — recover by looking it up by
		// name and reusing it, mirroring the backend findMCPServerByName path.
		found, ferr := c.findServerByName(ctx, name)
		if ferr != nil || found == nil || strings.TrimSpace(found.ID) == "" {
			return "", fmt.Errorf("create mcp server: %w", err)
		}
		resp = *found
	}
	if resp.ID == "" {
		return "", fmt.Errorf("composio did not return an mcp server id")
	}
	_ = mutateComposioByok(func(st *composioByokState) {
		conn := st.Connections[app]
		conn.ServerID = resp.ID
		st.Connections[app] = conn
	})
	return resp.ID, nil
}

// findServerByName recovers an existing MCP server by exact name (Composio
// server names are unique per account). Returns nil (no error) when no exact
// match is found. Mirrors backend findMCPServerByName.
func (c *directComposioClient) findServerByName(ctx context.Context, name string) (*composioServerResponse, error) {
	var page struct {
		Items []composioServerResponse `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "mcp/servers?limit=20&name="+url.QueryEscape(name), nil, &page); err != nil {
		return nil, err
	}
	for i := range page.Items {
		if page.Items[i].Name == name && strings.TrimSpace(page.Items[i].ID) != "" {
			return &page.Items[i], nil
		}
	}
	return nil, nil
}

func (c *directComposioClient) MCPConfig(ctx context.Context, app string) (string, map[string]string, error) {
	st := loadComposioByok()
	conn := st.Connections[app]
	if strings.TrimSpace(conn.AuthConfigID) == "" || strings.TrimSpace(conn.ConnectedAccountID) == "" {
		return "", nil, fmt.Errorf("composio app %q is not connected", app)
	}
	serverID, err := c.ensureServer(ctx, app, conn.AuthConfigID)
	if err != nil {
		return "", nil, err
	}
	gen := composioGenerateRequest{
		MCPServerID:           serverID,
		ManagedAuthByComposio: true,
		UserIDs:               []string{st.EntityID},
		ConnectedAccountIDs:   []string{conn.ConnectedAccountID},
	}
	var genResp composioGenerateResponse
	if err := c.do(ctx, http.MethodPost, "mcp/servers/generate", gen, &genResp); err != nil {
		return "", nil, fmt.Errorf("generate mcp url: %w", err)
	}
	mcpURL := genResp.pick()
	if mcpURL == "" {
		return "", nil, fmt.Errorf("composio did not return a generated mcp url")
	}
	mcpURL = addComposioMCPIdentity(mcpURL, st.EntityID, conn.ConnectedAccountID)
	_ = mutateComposioByok(func(s *composioByokState) {
		cc := s.Connections[app]
		cc.MCPURL = mcpURL
		s.Connections[app] = cc
	})
	headers := map[string]string{"x-api-key": strings.TrimSpace(st.APIKey)}
	return mcpURL, headers, nil
}

// addComposioMCPIdentity appends the identity query params Composio's MCP gateway
// expects (singular names), mirroring backend addComposioMCPIdentityParams.
func addComposioMCPIdentity(rawURL, entityID, connectedAccountID string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return rawURL
	}
	q := u.Query()
	if q.Get("user_id") == "" {
		q.Set("user_id", entityID)
	}
	if q.Get("connected_account_id") == "" {
		q.Set("connected_account_id", connectedAccountID)
	}
	if q.Get("include_composio_helper_actions") == "" {
		q.Set("include_composio_helper_actions", "true")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
