package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// urlHost returns a clean host (sans "www.") for display in the trajectory;
// empty on parse failure so the frontend falls back to a generic label.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(u.Host, "www.")
}

// Tavily is a fast HTTP web-search API (~2-3s/call) that returns clean results
// with URLs + content snippets, plus a synthesized answer. Used to ground the
// People research dossier in real, citable sources — the thing that makes
// Town's People profiles strong — without the ~100s/call browser-automation
// cost of opencli's AI-search adapters.

const tavilyEndpoint = "https://api.tavily.com/search"

// tavilyResult is one web result.
type tavilyResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

type tavilyResponse struct {
	Answer  string         `json:"answer"`
	Results []tavilyResult `json:"results"`
}

// tavilyAPIKey returns the configured key, or "" when web grounding is off.
func tavilyAPIKey() string {
	return strings.TrimSpace(os.Getenv("TAVILY_API_KEY"))
}

// tavilySearch runs a single web search. The caller is responsible for the
// context deadline; a short one keeps a flaky network from stalling research.
//
//isoform:destination tavily
func tavilySearch(ctx context.Context, query string, maxResults int) (*tavilyResponse, error) {
	key := tavilyAPIKey()
	if key == "" {
		return nil, errors.New("TAVILY_API_KEY not set")
	}
	if maxResults <= 0 {
		maxResults = 6
	}
	payload, err := json.Marshal(map[string]any{
		"api_key":        key,
		"query":          query,
		"max_results":    maxResults,
		"include_answer": true,
		"search_depth":   "basic",
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilyEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tavily status %d", resp.StatusCode)
	}
	var out tavilyResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode tavily response: %w", err)
	}
	return &out, nil
}
