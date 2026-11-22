package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"wails-gui/models"
)

type FindingProviderView struct {
	Choice               string `json:"choice"`
	HasOpenAIKey         bool   `json:"hasOpenAIKey"`
	Address              string `json:"address,omitempty"`
	OnThisComputer       *bool  `json:"onThisComputer,omitempty"`
	LocalModelDownloaded bool   `json:"localModelDownloaded"`
	LocalModelBytes      int64  `json:"localModelBytes"`
}

type FindingLeaves struct {
	Service  string `json:"service"`
	Address  string `json:"address,omitempty"`
	ThisWeek *int   `json:"thisWeek,omitempty"`
}

func findingURL(address string) (*url.URL, error) {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid embeddings service address")
	}
	return u, nil
}

func stopFindingRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func findingChoice(choice string) string {
	switch choice {
	case "", "automatic":
		return "local"
	default:
		return choice
	}
}

func (cfg MemoryStoreConfig) effectiveFindingProvider() string {
	if cfg.Provider == "openai" && strings.TrimSpace(cfg.APIKey) == "" {
		return "local"
	}
	return findingChoice(cfg.Provider)
}

func (cfg MemoryStoreConfig) findingAddress() (string, bool, error) {
	address := strings.TrimRight(cfg.BaseURL, "/")
	if address == "" {
		if cfg.Provider == "ollama" {
			address = ollamaDefaultBaseURL
		} else {
			address = embeddingDefaultBaseURL
		}
	}
	u, err := findingURL(address)
	if err != nil {
		return "", false, err
	}
	host := u.Hostname()
	local := strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
	return address, local, nil
}

func (cfg MemoryStoreConfig) findingLeaves() (*FindingLeaves, error) {
	switch cfg.effectiveFindingProvider() {
	case "local", "none":
		return nil, nil
	case "openai":
		return &FindingLeaves{Service: "openai"}, nil
	case "ollama":
		address, local, err := cfg.findingAddress()
		if err != nil || local {
			return nil, err
		}
		return &FindingLeaves{Service: "ollama", Address: address}, nil
	default:
		return nil, fmt.Errorf("unknown memory embedding provider")
	}
}

func findingView(dir string, cfg MemoryStoreConfig) (FindingProviderView, error) {
	view := FindingProviderView{Choice: findingChoice(cfg.Provider), HasOpenAIKey: strings.TrimSpace(cfg.APIKey) != ""}
	switch view.Choice {
	case "local", "openai":
	case "ollama":
		address, local, err := cfg.findingAddress()
		if err != nil {
			return FindingProviderView{}, err
		}
		view.Address, view.OnThisComputer = address, &local
	default:
		return FindingProviderView{}, fmt.Errorf("saved embedding provider is unavailable in Your AI")
	}
	ready, size, err := models.MemoryModelStatus(dir)
	view.LocalModelDownloaded, view.LocalModelBytes = ready, size
	return view, err
}

func (a *App) GetFindingProvider() (FindingProviderView, error) {
	dir, err := flavorDataDir()
	if err != nil {
		return FindingProviderView{}, err
	}
	cfg, err := loadMemoryStoreConfig(dir)
	if err != nil {
		return FindingProviderView{}, err
	}
	return findingView(dir, cfg)
}

func (a *App) SetFindingProvider(choice, apiKey, baseURL string) (FindingProviderView, error) {
	a.memoryConfigMu.Lock()
	defer a.memoryConfigMu.Unlock()
	dir, err := flavorDataDir()
	if err != nil {
		return FindingProviderView{}, err
	}
	cfg, err := loadMemoryStoreConfig(dir)
	if err != nil {
		return FindingProviderView{}, err
	}
	if cfg.Provider != choice {
		cfg.Model = ""
	}
	cfg.Provider = choice
	cfg.BaseURL = ""
	switch choice {
	case "local":
	case "ollama":
		cfg.BaseURL = strings.TrimSpace(baseURL)
	case "openai":
		if key := strings.TrimSpace(apiKey); key != "" {
			cfg.APIKey = key
		}
		if strings.TrimSpace(cfg.APIKey) == "" {
			return FindingProviderView{}, fmt.Errorf("OpenAI requires a saved or supplied API key")
		}
	default:
		return FindingProviderView{}, fmt.Errorf("unknown finding provider")
	}
	view, err := findingView(dir, cfg)
	if err != nil {
		return FindingProviderView{}, err
	}
	if err := a.applyMemoryConfig(dir, cfg); err != nil {
		return FindingProviderView{}, err
	}
	return view, nil
}

// The Memory choice covers memory text and searches. OCR frame backfill
// and idea-title grouping use only an on-device provider.
func (s *MemoryStore) onDeviceEmbedder() EmbeddingProvider {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	leaves, err := s.cfg.findingLeaves()
	if err != nil || leaves != nil {
		return nil
	}
	return s.embedder
}

// Readers hold this across authorization and transmission. Publishing a new
// config waits for in-flight calls, including calls through retained providers.
var findingSendMu sync.RWMutex

func (p *openaiProvider) authorizeFindingCall(ctx context.Context) error {
	cfg, err := loadMemoryStoreConfig(p.store.cfgDir)
	if err != nil {
		return err
	}
	address, _, err := cfg.findingAddress()
	if err != nil {
		return err
	}
	if cfg.effectiveFindingProvider() != p.provName || address != p.baseURL || (p.provName == "openai" && strings.TrimSpace(cfg.APIKey) != p.apiKey) {
		return fmt.Errorf("embedding destination is no longer selected")
	}
	leaves, err := cfg.findingLeaves()
	if err != nil || leaves == nil {
		return err
	}
	now := time.Now()
	tx, err := p.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM finding_embedding_calls WHERE second < ?", now.Add(-yourDataWeek).Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO finding_embedding_calls(service,address,second,calls) VALUES(?,?,?,1)
        ON CONFLICT(service,address,second) DO UPDATE SET calls=calls+1`, leaves.Service, leaves.Address, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (f *FindingLeaves) countCalls(db *sql.DB) {
	var n int
	if db != nil && db.QueryRow(`SELECT COALESCE(SUM(calls),0) FROM finding_embedding_calls
        WHERE service=? AND address=? AND second>=?`, f.Service, f.Address, time.Now().Add(-yourDataWeek).Unix()).Scan(&n) == nil {
		f.ThisWeek = &n
	}
}
