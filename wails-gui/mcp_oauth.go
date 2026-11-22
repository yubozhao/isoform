package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/skratchdot/open-golang/open"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	mcpOAuthTokenFileName = "oauth-token.json"
	mcpOAuthCallbackPath  = "/mcp/oauth/callback"
)

type mcpOAuthToken struct {
	AccessToken     string `json:"access_token"`
	RefreshToken    string `json:"refresh_token,omitempty"`
	TokenType       string `json:"token_type,omitempty"`
	Expiry          int64  `json:"expiry,omitempty"`
	Scope           string `json:"scope,omitempty"`
	ClientID        string `json:"client_id,omitempty"`
	RefreshFailedAt int64  `json:"refresh_failed_at,omitempty"`
}

type mcpOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

type mcpOAuthProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

type mcpOAuthServerMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	ScopesSupported               []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
}

type mcpOAuthClientRegistrationResponse struct {
	ClientID string `json:"client_id"`
}

type mcpOAuthCallbackResult struct {
	Code  string
	State string
	Error string
}

func normalizeMcpAuthMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "", mcpAuthModeNone:
		return mcpAuthModeNone
	case mcpAuthModeOAuth:
		return mcpAuthModeOAuth
	default:
		return strings.TrimSpace(strings.ToLower(mode))
	}
}

func normalizeMcpOAuthConfig(cfg McpOAuthConfig) McpOAuthConfig {
	cfg.AuthURL = strings.TrimSpace(cfg.AuthURL)
	cfg.TokenURL = strings.TrimSpace(cfg.TokenURL)
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.Resource = strings.TrimSpace(cfg.Resource)
	cfg.Issuer = strings.TrimSpace(cfg.Issuer)
	scopes := make([]string, 0, len(cfg.Scopes))
	seen := map[string]bool{}
	for _, scope := range cfg.Scopes {
		for _, piece := range strings.Fields(scope) {
			if piece == "" || seen[piece] {
				continue
			}
			seen[piece] = true
			scopes = append(scopes, piece)
		}
	}
	cfg.Scopes = scopes
	return cfg
}

func validateMcpOAuthEndpointURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("missing scheme or host")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("must use https, except loopback http for local development")
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func mcpOAuthTokenPath(id string) (string, error) {
	dir, err := mcpHubServerDir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, mcpOAuthTokenFileName), nil
}

func loadMcpOAuthToken(id string) (mcpOAuthToken, error) {
	path, err := mcpOAuthTokenPath(id)
	if err != nil {
		return mcpOAuthToken{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return mcpOAuthToken{}, err
	}
	var token mcpOAuthToken
	if err := json.Unmarshal(data, &token); err != nil {
		return mcpOAuthToken{}, fmt.Errorf("parse MCP OAuth token: %w", err)
	}
	return token, nil
}

func saveMcpOAuthToken(id string, token mcpOAuthToken) error {
	path, err := mcpOAuthTokenPath(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create MCP OAuth token directory: %w", err)
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal MCP OAuth token: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write MCP OAuth token: %w", err)
	}
	return nil
}

func mcpOAuthReadyForBackgroundStart(id string) bool {
	token, err := loadMcpOAuthToken(id)
	if err != nil || token.AccessToken == "" {
		return false
	}
	if token.RefreshFailedAt > 0 {
		return false
	}
	if !tokenExpired(token) {
		return true
	}
	return token.RefreshToken != ""
}

func mcpOAuthStatus(id string) string {
	token, err := loadMcpOAuthToken(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "authorization_required"
		}
		return "token_error"
	}
	if token.AccessToken == "" {
		return "authorization_required"
	}
	if token.RefreshFailedAt > 0 {
		return "token_error"
	}
	if tokenExpired(token) {
		if token.RefreshToken != "" {
			return "refreshable"
		}
		return "expired"
	}
	return "authorized"
}

func tokenExpired(token mcpOAuthToken) bool {
	return token.Expiry > 0 && time.Now().Unix() >= token.Expiry
}

func tokenFromResponse(resp mcpOAuthTokenResponse, clientID string) mcpOAuthToken {
	token := mcpOAuthToken{
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		TokenType:    resp.TokenType,
		Scope:        resp.Scope,
		ClientID:     clientID,
	}
	if resp.ExpiresIn > 0 {
		token.Expiry = time.Now().Add(time.Duration(resp.ExpiresIn-30) * time.Second).Unix()
	}
	return token
}

func (a *App) mcpOAuthAccessToken(ctx context.Context, cfg McpServerConfig) (string, error) {
	token, err := loadMcpOAuthToken(cfg.ID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("mcp server %q requires OAuth authorization", cfg.ID)
		}
		return "", err
	}
	if token.AccessToken == "" {
		return "", fmt.Errorf("mcp server %q has no OAuth access token; authorize it first", cfg.ID)
	}
	if token.RefreshFailedAt > 0 {
		return "", fmt.Errorf("mcp server %q OAuth refresh previously failed; authorize it again", cfg.ID)
	}
	if !tokenExpired(token) {
		return token.AccessToken, nil
	}
	if token.RefreshToken == "" {
		return "", fmt.Errorf("mcp server %q OAuth token expired; authorize it again", cfg.ID)
	}
	oauth, _, err := resolveMcpOAuthConfig(ctx, cfg, "")
	if err != nil {
		return "", err
	}
	if oauth.ClientID == "" {
		oauth.ClientID = token.ClientID
	}
	refreshed, err := refreshMcpOAuthToken(ctx, oauth, token)
	if err != nil {
		token.RefreshFailedAt = time.Now().Unix()
		_ = saveMcpOAuthToken(cfg.ID, token)
		return "", fmt.Errorf("refresh OAuth token for MCP server %q: %w", cfg.ID, err)
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = token.RefreshToken
	}
	if err := saveMcpOAuthToken(cfg.ID, refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

// ConnectMcpServerOAuth runs an OAuth authorization-code + PKCE flow for
// an existing remote HTTP MCP server, persists the resulting token outside
// state.json, and restarts the server when it is enabled.
func (a *App) ConnectMcpServerOAuth(id string) (McpServerView, error) {
	state, err := loadMcpHubState()
	if err != nil {
		return McpServerView{}, err
	}
	cfg, ok := state.Servers[id]
	if !ok {
		return McpServerView{}, fmt.Errorf("mcp server %q not found", id)
	}
	cfg = normalizeMcpServerConfig(cfg)
	if cfg.Transport != mcpTransportHTTP || cfg.AuthMode != mcpAuthModeOAuth {
		return McpServerView{}, fmt.Errorf("mcp server %q is not configured for HTTP OAuth", id)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	updated, token, err := a.runMcpOAuthAuthorization(ctx, cfg)
	if err != nil {
		return a.viewForServer(cfg), err
	}
	if err := saveMcpOAuthToken(updated.ID, token); err != nil {
		return a.viewForServer(cfg), err
	}

	var saved McpServerConfig
	if err := withMcpHubState(func(s *mcpHubState) error {
		current, ok := s.Servers[id]
		if !ok {
			return fmt.Errorf("mcp server %q not found", id)
		}
		current.AuthMode = mcpAuthModeOAuth
		current.OAuth = updated.OAuth
		current.UpdatedAt = time.Now().UnixMilli()
		s.Servers[id] = current
		saved = current
		return nil
	}); err != nil {
		return a.viewForServer(updated), err
	}

	if saved.Enabled {
		if err := a.restartMcp(saved); err != nil {
			return a.viewForServer(saved), fmt.Errorf("OAuth connected but server failed to start: %w", err)
		}
	}
	return a.viewForServer(saved), nil
}

func (a *App) runMcpOAuthAuthorization(ctx context.Context, cfg McpServerConfig) (McpServerConfig, mcpOAuthToken, error) {
	redirectURL, codeCh, stopServer, err := startMcpOAuthCallbackServer(ctx)
	if err != nil {
		return cfg, mcpOAuthToken{}, err
	}
	defer stopServer()

	oauth, metadata, err := resolveMcpOAuthConfig(ctx, cfg, redirectURL)
	if err != nil {
		return cfg, mcpOAuthToken{}, err
	}
	if metadata == nil {
		return cfg, mcpOAuthToken{}, fmt.Errorf("OAuth server metadata required for MCP server %q to verify PKCE support", cfg.ID)
	}
	if !containsString(metadata.CodeChallengeMethodsSupported, "S256") {
		return cfg, mcpOAuthToken{}, fmt.Errorf("OAuth server for %q does not advertise PKCE S256 support", cfg.ID)
	}
	if oauth.ClientID == "" {
		if metadata != nil && metadata.RegistrationEndpoint != "" {
			clientID, err := registerMcpOAuthClient(ctx, metadata.RegistrationEndpoint, cfg.Name, redirectURL, oauth.Scopes)
			if err != nil {
				return cfg, mcpOAuthToken{}, err
			}
			oauth.ClientID = clientID
		}
	}
	if oauth.ClientID == "" {
		return cfg, mcpOAuthToken{}, fmt.Errorf("OAuth client ID required for MCP server %q; enter a client ID or use a server with dynamic client registration", cfg.ID)
	}

	state, err := randomURLSafeString(32)
	if err != nil {
		return cfg, mcpOAuthToken{}, err
	}
	verifier, err := randomURLSafeString(64)
	if err != nil {
		return cfg, mcpOAuthToken{}, err
	}
	challenge := pkceS256(verifier)
	authURL, err := buildMcpOAuthAuthorizeURL(oauth, redirectURL, state, challenge)
	if err != nil {
		return cfg, mcpOAuthToken{}, err
	}

	if a.ctx != nil {
		wailsRuntime.BrowserOpenURL(a.ctx, authURL)
	} else if err := open.Start(authURL); err != nil {
		return cfg, mcpOAuthToken{}, fmt.Errorf("open OAuth authorization URL: %w", err)
	}

	result, err := waitMcpOAuthCallback(ctx, codeCh)
	if err != nil {
		return cfg, mcpOAuthToken{}, err
	}
	if result.Error != "" {
		return cfg, mcpOAuthToken{}, fmt.Errorf("OAuth authorization failed: %s", result.Error)
	}
	if result.State != state {
		return cfg, mcpOAuthToken{}, fmt.Errorf("OAuth authorization state mismatch")
	}
	resp, err := exchangeMcpOAuthCode(ctx, oauth, result.Code, redirectURL, verifier)
	if err != nil {
		return cfg, mcpOAuthToken{}, err
	}
	token := tokenFromResponse(resp, oauth.ClientID)
	cfg.OAuth = oauth
	return cfg, token, nil
}

func startMcpOAuthCallbackServer(ctx context.Context) (string, <-chan mcpOAuthCallbackResult, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, fmt.Errorf("start local OAuth callback listener: %w", err)
	}
	resultCh := make(chan mcpOAuthCallbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(mcpOAuthCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		result := mcpOAuthCallbackResult{
			Code:  q.Get("code"),
			State: q.Get("state"),
			Error: q.Get("error"),
		}
		select {
		case resultCh <- result:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body><h1>OAuth completed</h1><p>You can return to Isoform Desktop.</p></body></html>")
	})
	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	redirectURL := "http://" + listener.Addr().String() + mcpOAuthCallbackPath
	stop := func() {
		_ = server.Shutdown(context.Background())
	}
	return redirectURL, resultCh, stop, nil
}

func waitMcpOAuthCallback(ctx context.Context, ch <-chan mcpOAuthCallbackResult) (mcpOAuthCallbackResult, error) {
	select {
	case result := <-ch:
		if result.Code == "" && result.Error == "" {
			return result, fmt.Errorf("OAuth callback did not include a code")
		}
		return result, nil
	case <-ctx.Done():
		return mcpOAuthCallbackResult{}, fmt.Errorf("OAuth authorization timed out or was cancelled: %w", ctx.Err())
	}
}

func buildMcpOAuthAuthorizeURL(cfg McpOAuthConfig, redirectURL, state, challenge string) (string, error) {
	u, err := url.Parse(cfg.AuthURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURL)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if len(cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	if cfg.Resource != "" {
		q.Set("resource", cfg.Resource)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func exchangeMcpOAuthCode(ctx context.Context, cfg McpOAuthConfig, code, redirectURL, verifier string) (mcpOAuthTokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURL)
	form.Set("code_verifier", verifier)
	form.Set("client_id", cfg.ClientID)
	if cfg.Resource != "" {
		form.Set("resource", cfg.Resource)
	}
	return postMcpOAuthTokenRequest(ctx, cfg.TokenURL, form)
}

func refreshMcpOAuthToken(ctx context.Context, cfg McpOAuthConfig, token mcpOAuthToken) (mcpOAuthToken, error) {
	clientID := cfg.ClientID
	if clientID == "" {
		clientID = token.ClientID
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", token.RefreshToken)
	form.Set("client_id", clientID)
	if cfg.Resource != "" {
		form.Set("resource", cfg.Resource)
	}
	resp, err := postMcpOAuthTokenRequest(ctx, cfg.TokenURL, form)
	if err != nil {
		return mcpOAuthToken{}, err
	}
	return tokenFromResponse(resp, clientID), nil
}

//isoform:destination mcp_servers
func postMcpOAuthTokenRequest(ctx context.Context, tokenURL string, form url.Values) (mcpOAuthTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return mcpOAuthTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return mcpOAuthTokenResponse{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return mcpOAuthTokenResponse{}, fmt.Errorf("OAuth token endpoint returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed mcpOAuthTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return mcpOAuthTokenResponse{}, fmt.Errorf("parse OAuth token response: %w", err)
	}
	if parsed.AccessToken == "" {
		return mcpOAuthTokenResponse{}, fmt.Errorf("OAuth token response missing access_token")
	}
	return parsed, nil
}

func resolveMcpOAuthConfig(ctx context.Context, cfg McpServerConfig, redirectURL string) (McpOAuthConfig, *mcpOAuthServerMetadata, error) {
	oauth := normalizeMcpOAuthConfig(cfg.OAuth)
	if oauth.AuthURL != "" && oauth.TokenURL != "" {
		metadata, err := discoverMcpOAuthServerMetadataFromEndpoint(ctx, oauth.AuthURL, oauth.TokenURL)
		if err != nil {
			return oauth, nil, err
		}
		if oauth.Issuer == "" {
			oauth.Issuer = metadata.Issuer
		}
		if len(oauth.Scopes) == 0 {
			oauth.Scopes = metadata.ScopesSupported
		}
		if oauth.Resource == "" {
			oauth.Resource = canonicalMcpResourceURI(cfg.URL)
		}
		return oauth, metadata, nil
	}
	discovered, metadata, err := discoverMcpOAuthConfig(ctx, cfg.URL)
	if err != nil {
		return oauth, nil, fmt.Errorf("discover OAuth metadata for MCP server %q: %w", cfg.ID, err)
	}
	if oauth.AuthURL == "" {
		oauth.AuthURL = discovered.AuthURL
	}
	if oauth.TokenURL == "" {
		oauth.TokenURL = discovered.TokenURL
	}
	if oauth.Resource == "" {
		oauth.Resource = discovered.Resource
	}
	if oauth.Resource == "" {
		oauth.Resource = canonicalMcpResourceURI(cfg.URL)
	}
	if oauth.Issuer == "" {
		oauth.Issuer = discovered.Issuer
	}
	if len(oauth.Scopes) == 0 {
		oauth.Scopes = discovered.Scopes
	}
	if oauth.ClientID == "" {
		oauth.ClientID = discovered.ClientID
	}
	if oauth.AuthURL == "" || oauth.TokenURL == "" {
		return oauth, metadata, fmt.Errorf("OAuth authorization and token endpoints are required")
	}
	_ = redirectURL
	return oauth, metadata, nil
}

func discoverMcpOAuthConfig(ctx context.Context, mcpURL string) (McpOAuthConfig, *mcpOAuthServerMetadata, error) {
	resourceMeta, err := discoverMcpProtectedResourceMetadata(ctx, mcpURL)
	if err != nil {
		return McpOAuthConfig{}, nil, err
	}
	if len(resourceMeta.AuthorizationServers) == 0 {
		return McpOAuthConfig{}, nil, fmt.Errorf("protected resource metadata does not list authorization_servers")
	}
	serverMeta, err := discoverMcpOAuthServerMetadata(ctx, resourceMeta.AuthorizationServers[0])
	if err != nil {
		return McpOAuthConfig{}, nil, err
	}
	cfg := McpOAuthConfig{
		AuthURL:  serverMeta.AuthorizationEndpoint,
		TokenURL: serverMeta.TokenEndpoint,
		Resource: resourceMeta.Resource,
		Issuer:   serverMeta.Issuer,
		Scopes:   resourceMeta.ScopesSupported,
	}
	if cfg.Resource == "" {
		cfg.Resource = canonicalMcpResourceURI(mcpURL)
	}
	return cfg, serverMeta, nil
}

//isoform:destination mcp_servers
func discoverMcpProtectedResourceMetadata(ctx context.Context, mcpURL string) (mcpOAuthProtectedResourceMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mcpURL, nil)
	if err == nil {
		req.Header.Set("Accept", "application/json")
		if resp, doErr := http.DefaultClient.Do(req); doErr == nil {
			metadataURL := parseMcpResourceMetadataURL(resp.Header.Values("WWW-Authenticate"))
			_ = resp.Body.Close()
			if metadataURL != "" {
				return fetchMcpProtectedResourceMetadata(ctx, metadataURL)
			}
		}
	}
	var lastErr error
	for _, candidate := range protectedResourceMetadataCandidates(mcpURL) {
		meta, err := fetchMcpProtectedResourceMetadata(ctx, candidate)
		if err == nil {
			return meta, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return mcpOAuthProtectedResourceMetadata{}, lastErr
	}
	return mcpOAuthProtectedResourceMetadata{}, fmt.Errorf("OAuth protected resource metadata not found")
}

func parseMcpResourceMetadataURL(values []string) string {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			lower := strings.ToLower(part)
			for _, key := range []string{"resource_metadata=", "resource_metadata_uri="} {
				idx := strings.Index(lower, key)
				if idx == -1 {
					continue
				}
				raw := strings.TrimSpace(part[idx+len(key):])
				raw = strings.Trim(raw, `"`)
				return raw
			}
		}
	}
	return ""
}

func protectedResourceMetadataCandidates(rawURL string) []string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	root := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: "/.well-known/oauth-protected-resource"}
	pathSpecific := *root
	pathSpecific.Path = strings.TrimRight(root.Path, "/") + parsed.EscapedPath()
	if pathSpecific.Path == root.Path {
		return []string{root.String()}
	}
	return []string{pathSpecific.String(), root.String()}
}

func fetchMcpProtectedResourceMetadata(ctx context.Context, rawURL string) (mcpOAuthProtectedResourceMetadata, error) {
	if err := validateMcpOAuthEndpointURL(rawURL); err != nil {
		return mcpOAuthProtectedResourceMetadata{}, err
	}
	var meta mcpOAuthProtectedResourceMetadata
	if err := getOAuthJSON(ctx, rawURL, &meta); err != nil {
		return mcpOAuthProtectedResourceMetadata{}, err
	}
	return meta, nil
}

func discoverMcpOAuthServerMetadata(ctx context.Context, issuerURL string) (*mcpOAuthServerMetadata, error) {
	var lastErr error
	for _, candidate := range oauthServerMetadataCandidates(issuerURL) {
		meta, err := fetchMcpOAuthServerMetadata(ctx, candidate)
		if err != nil {
			lastErr = err
			continue
		}
		return meta, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("OAuth server metadata not found")
}

func discoverMcpOAuthServerMetadataFromEndpoint(ctx context.Context, authURL, tokenURL string) (*mcpOAuthServerMetadata, error) {
	parsed, err := url.Parse(authURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid OAuth authorization endpoint")
	}
	candidates := oauthServerMetadataCandidates((&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String())
	if parsed.Path != "" {
		trimmed := strings.TrimSuffix(parsed.EscapedPath(), "/authorize")
		trimmed = strings.Trim(trimmed, "/")
		if trimmed != "" {
			issuer := (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: "/" + trimmed}).String()
			candidates = append(oauthServerMetadataCandidates(issuer), candidates...)
		}
	}
	seen := map[string]bool{}
	var lastErr error
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		meta, err := fetchMcpOAuthServerMetadata(ctx, candidate)
		if err != nil {
			lastErr = err
			continue
		}
		if meta.AuthorizationEndpoint == authURL && meta.TokenEndpoint == tokenURL {
			return meta, nil
		}
		lastErr = fmt.Errorf("OAuth metadata at %s does not match configured endpoints", candidate)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("OAuth server metadata not found for configured endpoints")
}

func fetchMcpOAuthServerMetadata(ctx context.Context, candidate string) (*mcpOAuthServerMetadata, error) {
	if err := validateMcpOAuthEndpointURL(candidate); err != nil {
		return nil, err
	}
	var meta mcpOAuthServerMetadata
	if err := getOAuthJSON(ctx, candidate, &meta); err != nil {
		return nil, err
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return nil, fmt.Errorf("OAuth server metadata at %s missing authorization_endpoint or token_endpoint", candidate)
	}
	if err := validateMcpOAuthEndpointURL(meta.AuthorizationEndpoint); err != nil {
		return nil, fmt.Errorf("invalid authorization endpoint: %w", err)
	}
	if err := validateMcpOAuthEndpointURL(meta.TokenEndpoint); err != nil {
		return nil, fmt.Errorf("invalid token endpoint: %w", err)
	}
	return &meta, nil
}

func oauthServerMetadataCandidates(issuerURL string) []string {
	parsed, err := url.Parse(issuerURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	if strings.Contains(parsed.Path, "/.well-known/") {
		return []string{parsed.String()}
	}
	path := strings.Trim(parsed.EscapedPath(), "/")
	base := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	oauthPath := "/.well-known/oauth-authorization-server"
	oidcPath := "/.well-known/openid-configuration"
	if path != "" {
		return []string{
			base.ResolveReference(&url.URL{Path: oauthPath + "/" + path}).String(),
			base.ResolveReference(&url.URL{Path: "/" + path + oidcPath}).String(),
			base.ResolveReference(&url.URL{Path: oidcPath + "/" + path}).String(),
			base.ResolveReference(&url.URL{Path: oauthPath}).String(),
			base.ResolveReference(&url.URL{Path: oidcPath}).String(),
		}
	}
	return []string{
		base.ResolveReference(&url.URL{Path: oauthPath}).String(),
		base.ResolveReference(&url.URL{Path: oidcPath}).String(),
	}
}

//isoform:destination mcp_servers
func getOAuthJSON(ctx context.Context, rawURL string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s returned HTTP %d: %s", rawURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("parse JSON from %s: %w", rawURL, err)
	}
	return nil
}

//isoform:destination mcp_servers
func registerMcpOAuthClient(ctx context.Context, registrationURL, name, redirectURL string, scopes []string) (string, error) {
	if err := validateMcpOAuthEndpointURL(registrationURL); err != nil {
		return "", fmt.Errorf("invalid OAuth dynamic registration endpoint: %w", err)
	}
	body := map[string]any{
		"client_name":                coalesce(name, "Isoform Desktop"),
		"redirect_uris":              []string{redirectURL},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	if len(scopes) > 0 {
		body["scope"] = strings.Join(scopes, " ")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationURL, strings.NewReader(string(data)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("dynamic OAuth client registration returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed mcpOAuthClientRegistrationResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parse OAuth client registration response: %w", err)
	}
	if parsed.ClientID == "" {
		return "", fmt.Errorf("OAuth client registration response missing client_id")
	}
	return parsed.ClientID, nil
}

func canonicalMcpResourceURI(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return strings.TrimSpace(rawURL)
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func randomURLSafeString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
