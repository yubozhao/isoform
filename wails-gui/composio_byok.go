package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// composio_byok.go — "bring your own Composio key" local state (Path B).
//
// In standalone (and any build where the user opts in by saving a key), the
// desktop talks DIRECTLY to Composio with the user's own API key instead of
// proxying through the Yansu backend. This file owns the local key + connection
// cache; the direct client (composio_direct.go) and the client selector
// (composio_client.go) build on it.

const composioByokFileName = "composio.json"

// composioByokConnection caches one app's Composio connection so we don't
// recreate auth configs / MCP servers on every call. Mirrors the columns the
// backend keeps server-side (auth_config id, connected_account id, mcp server
// id, generated mcp url, last known status).
type composioByokConnection struct {
	App                string `json:"app"`
	AuthConfigID       string `json:"auth_config_id,omitempty"`
	ConnectedAccountID string `json:"connected_account_id,omitempty"`
	ServerID           string `json:"server_id,omitempty"`
	MCPURL             string `json:"mcp_url,omitempty"`
	// Status is the projected status (see composioProjectStatus): connected /
	// connecting / disconnected.
	Status string `json:"status,omitempty"`
}

// composioByokState is the on-disk shape of ~/.yansu-agent/composio.json (0600).
type composioByokState struct {
	APIKey   string `json:"api_key"`
	EntityID string `json:"entity_id"`
	// Connections keyed by normalized app slug.
	Connections map[string]composioByokConnection `json:"connections,omitempty"`
}

var composioByokMu sync.Mutex

// managedComposioAppAccessEnabled keeps Isoform-managed Composio app access
// off; only a user's own Composio key can turn connectors on.
const managedComposioAppAccessEnabled = false

func composioByokPath() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, composioByokFileName), nil
}

func loadComposioByok() composioByokState {
	composioByokMu.Lock()
	defer composioByokMu.Unlock()
	return loadComposioByokLocked()
}

func loadComposioByokLocked() composioByokState {
	var st composioByokState
	path, err := composioByokPath()
	if err != nil {
		return st
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	if st.Connections == nil {
		st.Connections = map[string]composioByokConnection{}
	}
	return st
}

func saveComposioByokLocked(st composioByokState) error {
	if strings.TrimSpace(st.EntityID) == "" {
		st.EntityID = newComposioEntityID()
	}
	if st.Connections == nil {
		st.Connections = map[string]composioByokConnection{}
	}
	path, err := composioByokPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// mutateComposioByok loads, applies fn, and saves under one lock. Used to update
// a single connection without clobbering concurrent writers.
func mutateComposioByok(fn func(*composioByokState)) error {
	composioByokMu.Lock()
	defer composioByokMu.Unlock()
	st := loadComposioByokLocked()
	fn(&st)
	return saveComposioByokLocked(st)
}

func newComposioEntityID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "yansu-desktop"
	}
	return "yansu-" + hex.EncodeToString(b)
}

// composioByokConfigured reports whether a Composio API key is saved locally.
// This is the single trigger for the direct (BYO) path.
func composioByokConfigured() bool {
	return strings.TrimSpace(loadComposioByok().APIKey) != ""
}

// useDirectComposio reports whether the desktop should talk to Composio directly
// with the user's own key (Path B) instead of proxying through the Yansu
// backend. True whenever a local key is configured — which is the only path in
// standalone (the backend proxy is denied there) and an explicit opt-in in
// normal builds.
func useDirectComposio() bool {
	return managedComposioAppAccessEnabled && composioByokConfigured()
}

// ── Wails bindings (frontend key entry, P1) ──

// ComposioApiKeyStatus is returned to the frontend so the settings UI can show
// whether a key is configured without ever sending the key itself back.
type ComposioApiKeyStatus struct {
	Configured bool   `json:"configured"`
	EntityID   string `json:"entity_id,omitempty"`
}

// GetComposioApiKeyStatus reports whether a Composio API key is configured.
func (a *App) GetComposioApiKeyStatus() ComposioApiKeyStatus {
	if !managedComposioAppAccessEnabled {
		return ComposioApiKeyStatus{}
	}
	st := loadComposioByok()
	return ComposioApiKeyStatus{
		Configured: strings.TrimSpace(st.APIKey) != "",
		EntityID:   st.EntityID,
	}
}

// SaveComposioApiKey validates and persists the user's Composio API key. A
// trivial reachability probe (list auth_configs) confirms the key before saving
// so a typo surfaces immediately rather than on first connect.
func (a *App) SaveComposioApiKey(apiKey string) error {
	if !managedComposioAppAccessEnabled {
		return fmt.Errorf("managed provider access is retired; use Computer Use with the existing signed-in app session")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return fmt.Errorf("API key is required")
	}
	if err := probeComposioAPIKey(apiKey); err != nil {
		return fmt.Errorf("Composio key check failed — not saved: %w", err)
	}
	if err := mutateComposioByok(func(st *composioByokState) {
		st.APIKey = apiKey
	}); err != nil {
		return err
	}
	// Bring up MCP clients for any already-connected apps now that direct mode
	// is live.
	a.ensureConnectedComposioMCPActiveInBackground("composio-key-saved")
	return nil
}

// ClearComposioApiKey removes the saved key and tears down any active Composio
// MCP clients.
func (a *App) ClearComposioApiKey() error {
	for _, app := range composioManagedApps {
		if serverID, err := composioManagedServerID(app); err == nil {
			a.unregisterAndStopMcp(serverID)
		}
	}
	return mutateComposioByok(func(st *composioByokState) {
		st.APIKey = ""
		st.Connections = map[string]composioByokConnection{}
	})
}
