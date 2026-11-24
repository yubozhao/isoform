package main

import (
	"errors"
	"fmt"
	"net/http"
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

func apiGet(baseURL, token, path string, target any) error {
	return errNoYansuAccount
}

func apiGetList[T any](baseURL, token, path string) ([]T, error) {
	return nil, errNoYansuAccount
}

// apiPost sends an authenticated JSON POST and optionally decodes the response into target (may be nil).
func apiPost(baseURL, token, path string, body, target any) error {
	return errNoYansuAccount
}

// apiDelete sends an authenticated DELETE request.
func apiDelete(baseURL, token, path string) error {
	return errNoYansuAccount
}

// ListOrganizations returns all organizations the current user belongs to.
func (a *App) ListOrganizations() ([]RemoteOrg, error) {
	return nil, errNoYansuAccount
}

// ListProducts returns all products under the given organization.
func (a *App) ListProducts(org string) ([]RemoteProduct, error) {
	return nil, errNoYansuAccount
}

// ListProjects returns all projects under the given organization and product.
func (a *App) ListProjects(org, product string) ([]RemoteProject, error) {
	return nil, errNoYansuAccount
}
