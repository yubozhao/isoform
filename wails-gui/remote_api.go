package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ── Remote project browsing (exposed to frontend via Wails) ──

// RemoteOrg represents a Yansu organization for the frontend.
type RemoteOrg struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// RemoteProduct represents a Yansu product for the frontend.
type RemoteProduct struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// RemoteProject represents a Yansu project for the frontend.
type RemoteProject struct {
	UUID         string `json:"uuid"`
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	CodingBranch string `json:"coding_branch"`
	ProductUUID  string `json:"product_uuid"`
}

// stageResponse represents a stage from the API
type stageResponse struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Content string `json:"content"`
}

// apiHTTPError is returned by apiGet/apiDelete when the server responds with a
// non-success status code. Callers can use errors.As to inspect StatusCode.
type apiHTTPError struct {
	StatusCode int
	Body       string
}

func (e *apiHTTPError) Error() string {
	return fmt.Sprintf("API error (%d): %s", e.StatusCode, e.Body)
}

// isAPINotFound returns true if err is an HTTP 404 response.
func isAPINotFound(err error) bool {
	var he *apiHTTPError
	if errors.As(err, &he) {
		return he.StatusCode == http.StatusNotFound
	}
	return false
}

// isAuthRejected reports whether err is the server actively rejecting the
// credentials (HTTP 401/403). Transient failures -- network errors, timeouts,
// 5xx -- are deliberately excluded so callers never delete a still-valid token
// over a momentary blip. See CheckAuth.
func isAuthRejected(err error) bool {
	var he *apiHTTPError
	if errors.As(err, &he) {
		return he.StatusCode == http.StatusUnauthorized || he.StatusCode == http.StatusForbidden
	}
	return false
}

// yansuHTTPClient is the shared HTTP client for all Yansu API calls.
//
//isoform:destination yansu_backend
var yansuHTTPClient = &http.Client{Timeout: 30 * time.Second}

// apiGet performs an authenticated GET request and decodes the JSON response into target.
//
//isoform:destination yansu_backend
func apiGet(baseURL, token, path string, target any) error {
	if noYansuAccount() {
		return errNoYansuAccount
	}
	req, err := http.NewRequest("GET", baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "yansu-gui/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := yansuHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		bodyStr := string(body)
		return &apiHTTPError{StatusCode: resp.StatusCode, Body: bodyStr}
	}
	return json.Unmarshal(body, target)
}

// apiGetList fetches a JSON endpoint that may return either a raw array [...]
// or a wrapped object {"items":[...]} / {"data":[...]}, and returns the slice.
func apiGetList[T any](baseURL, token, path string) ([]T, error) {
	var raw json.RawMessage
	if err := apiGet(baseURL, token, path, &raw); err != nil {
		return nil, err
	}

	var items []T
	if err := json.Unmarshal(raw, &items); err == nil {
		return items, nil
	}

	var wrapper struct {
		Items []T `json:"items"`
		Data  []T `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("unexpected response format for %s", path)
	}
	if wrapper.Items != nil {
		return wrapper.Items, nil
	}
	return wrapper.Data, nil
}

// apiPost sends an authenticated JSON POST and optionally decodes the response into target (may be nil).
//
//isoform:destination yansu_backend
func apiPost(baseURL, token, path string, body, target any) error {
	if noYansuAccount() {
		return errNoYansuAccount
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", baseURL+path, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "yansu-gui/1.0")

	resp, err := yansuHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(respBody))
	}
	if target != nil && len(respBody) > 0 {
		return json.Unmarshal(respBody, target)
	}
	return nil
}

// apiDelete sends an authenticated DELETE request.
//
//isoform:destination yansu_backend
func apiDelete(baseURL, token, path string) error {
	if noYansuAccount() {
		return errNoYansuAccount
	}
	req, err := http.NewRequest("DELETE", baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "yansu-gui/1.0")

	resp, err := yansuHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return &apiHTTPError{StatusCode: resp.StatusCode, Body: string(b)}
	}
	return nil
}

// ListOrganizations returns all organizations the current user belongs to.
func (a *App) ListOrganizations() ([]RemoteOrg, error) {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return nil, fmt.Errorf("not authenticated")
	}
	return apiGetList[RemoteOrg](loadAPIBaseURL(), token, "/api/orgs")
}

// ListProducts returns all products under the given organization.
func (a *App) ListProducts(org string) ([]RemoteProduct, error) {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return nil, fmt.Errorf("not authenticated")
	}
	return apiGetList[RemoteProduct](loadAPIBaseURL(), token, fmt.Sprintf("/api/orgs/%s/products", org))
}

// ListProjects returns all projects under the given organization and product.
func (a *App) ListProjects(org, product string) ([]RemoteProject, error) {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return nil, fmt.Errorf("not authenticated")
	}
	return apiGetList[RemoteProject](loadAPIBaseURL(), token, fmt.Sprintf("/api/orgs/%s/products/%s/projects", org, product))
}

// fetchStages fetches stages from Yansu API.
func fetchStages(baseURL, token, org, product, project string) ([]stageResponse, error) {
	path := fmt.Sprintf("/api/orgs/%s/products/%s/projects/%s/stages", org, product, project)
	var stages []stageResponse
	if err := apiGet(baseURL, token, path, &stages); err != nil {
		return nil, err
	}
	return stages, nil
}
