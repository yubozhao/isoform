package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is the Yansu API client
type Client struct {
	baseURL      string
	token        string
	organization string
	product      string
	project      string
	productUUID  string
	httpClient   *http.Client
}

// New creates a new API client
//
//isoform:destination yansu_backend
func New(baseURL, token string) *Client {
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}
	return &Client{
		baseURL:    baseURL,
		token:      token,
		httpClient: httpClient,
	}
}

// SetProject sets the project context for subsequent requests
func (c *Client) SetProject(org, product, project string) {
	c.organization = org
	c.product = product
	c.project = project
}

// SetProductUUID sets the product UUID for knowledge queries
func (c *Client) SetProductUUID(uuid string) {
	c.productUUID = uuid
}

// UserProfile represents the authenticated user's profile.
type UserProfile struct {
	UUID        string    `json:"uuid"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
	CreatedAt   time.Time `json:"created_at"`
}

// Organization represents a Yansu organization.
type Organization struct {
	UUID        string    `json:"uuid"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// Product represents a Yansu product.
type Product struct {
	UUID        string    `json:"uuid"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// Project represents a Yansu project
type Project struct {
	UUID         string    `json:"uuid"`
	Name         string    `json:"name"`
	Organization string    `json:"organization_name"`
	Product      string    `json:"product_name"`
	ProductUUID  string    `json:"product_uuid"`
	CodingBranch string    `json:"coding_branch"`
	CreatedAt    time.Time `json:"created_at"`
}

// CodeRepository represents a code repository
type CodeRepository struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	GitHubURL string `json:"github_url"`
}

// KnowledgeResponse represents a knowledge item from the API
type KnowledgeResponse struct {
	UUID      string    `json:"uuid"`
	Trigger   string    `json:"trigger"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags"`
	Path      *string   `json:"path,omitempty"`
	Scope     string    `json:"scope"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateKnowledgeRequest is the request body for creating knowledge
type CreateKnowledgeRequest struct {
	Trigger  string         `json:"trigger"`
	Content  string         `json:"content"`
	Scope    string         `json:"scope,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// UpdateKnowledgeRequest is the request body for updating knowledge
type UpdateKnowledgeRequest struct {
	Trigger  string         `json:"trigger,omitempty"`
	Content  string         `json:"content,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Activity represents a development activity
type Activity struct {
	Type         string   `json:"type"`
	Summary      string   `json:"summary"`
	Details      string   `json:"details,omitempty"`
	FilesChanged []string `json:"files_changed,omitempty"`
}

// ContextResponse represents the project context from API
type ContextResponse struct {
	PRD          string `json:"prd"`
	Summary      string `json:"summary"`
	Requirements string `json:"requirements"`
	Scenarios    string `json:"scenarios"`
	Decisions    string `json:"decisions"`
}

// GetProject retrieves a project by org/product/project names
//
//isoform:destination yansu_backend
func (c *Client) GetProject(ctx context.Context, org, product, project string) (*Project, error) {
	url := fmt.Sprintf("%s/api/orgs/%s/products/%s/projects/%s", c.baseURL, org, product, project)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var result struct {
		UUID         string `json:"uuid"`
		Name         string `json:"name"`
		ProductUUID  string `json:"product_uuid"`
		CodingBranch string `json:"coding_branch"`
		Product      struct {
			Name         string `json:"name"`
			Organization struct {
				Name string `json:"name"`
			} `json:"organization"`
		} `json:"product"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &Project{
		UUID:         result.UUID,
		Name:         result.Name,
		Organization: result.Product.Organization.Name,
		Product:      result.Product.Name,
		ProductUUID:  result.ProductUUID,
		CodingBranch: result.CodingBranch,
		CreatedAt:    result.CreatedAt,
	}, nil
}

// GetProjectCodeRepositories retrieves code repositories for a project
//
//isoform:destination yansu_backend
func (c *Client) GetProjectCodeRepositories(ctx context.Context, org, product, project string) ([]*CodeRepository, error) {
	url := fmt.Sprintf("%s/api/public/orgs/%s/products/%s/projects/%s/code-repositories",
		c.baseURL, org, product, project)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var repos []*CodeRepository
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return repos, nil
}

// StageResponse represents stage data from the API
type StageResponse struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// GetProjectContext retrieves the project context by fetching stages
//
//isoform:destination yansu_backend
func (c *Client) GetProjectContext(ctx context.Context) (*ContextResponse, error) {
	// Fetch all stages for the project
	url := fmt.Sprintf("%s/api/orgs/%s/products/%s/projects/%s/stages",
		c.baseURL, c.organization, c.product, c.project)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var stages []StageResponse
	if err := json.NewDecoder(resp.Body).Decode(&stages); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Build context from stages
	context := &ContextResponse{}
	for _, stage := range stages {
		switch stage.Type {
		case "prd":
			context.PRD = stage.Content
		case "simulations":
			context.Scenarios = stage.Content
		}
	}

	return context, nil
}

// ListKnowledge retrieves all knowledge for the current product
//
//isoform:destination yansu_backend
func (c *Client) ListKnowledge(ctx context.Context) ([]*KnowledgeResponse, error) {
	// Use the hierarchical knowledge endpoint
	url := fmt.Sprintf("%s/api/orgs/%s/products/%s/knowledge",
		c.baseURL, c.organization, c.product)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	// The response may be an array or an object with data field
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Try array first
	var knowledge []*KnowledgeResponse
	if err := json.Unmarshal(body, &knowledge); err == nil {
		return knowledge, nil
	}

	// Try object with data field
	var result struct {
		Data []*KnowledgeResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return result.Data, nil
}

// CreateKnowledge creates a new knowledge item
//
//isoform:destination yansu_backend
func (c *Client) CreateKnowledge(ctx context.Context, req *CreateKnowledgeRequest) (*KnowledgeResponse, error) {
	// Use hierarchical endpoint
	url := fmt.Sprintf("%s/api/orgs/%s/products/%s/knowledge",
		c.baseURL, c.organization, c.product)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, c.handleError(resp)
	}

	var knowledge KnowledgeResponse
	if err := json.NewDecoder(resp.Body).Decode(&knowledge); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &knowledge, nil
}

// UpdateKnowledge updates an existing knowledge item
//
//isoform:destination yansu_backend
func (c *Client) UpdateKnowledge(ctx context.Context, uuid string, req *UpdateKnowledgeRequest) (*KnowledgeResponse, error) {
	// Use flat knowledge endpoint
	url := fmt.Sprintf("%s/api/knowledge/%s", c.baseURL, uuid)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var knowledge KnowledgeResponse
	if err := json.NewDecoder(resp.Body).Decode(&knowledge); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &knowledge, nil
}

// DeleteKnowledge deletes a knowledge item
//
//isoform:destination yansu_backend
func (c *Client) DeleteKnowledge(ctx context.Context, uuid string) error {
	// Use flat knowledge endpoint
	url := fmt.Sprintf("%s/api/knowledge/%s", c.baseURL, uuid)

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return c.handleError(resp)
	}

	return nil
}

// LogActivity logs a development activity
// Note: Activity logging endpoint doesn't exist yet in the backend.
// This is a placeholder that will silently succeed for now.
func (c *Client) LogActivity(ctx context.Context, activity *Activity) error {
	// TODO: Implement when backend activity logging endpoint is added
	// For now, we just log locally and don't send to server
	return nil
}

// ExtractKnowledgeRequest is the request body for knowledge extraction
type ExtractKnowledgeRequest struct {
	Diff          string `json:"diff"`
	CommitMessage string `json:"commit_message"`
	ChangedFiles  string `json:"changed_files"`
}

// ExtractedKnowledge represents AI-extracted knowledge from code changes
type ExtractedKnowledge struct {
	Trigger string `json:"trigger"`
	Content string `json:"content"`
	Type    string `json:"type"` // pattern, decision, learning, etc.
}

// ExtractKnowledgeResponse is the response from knowledge extraction
type ExtractKnowledgeResponse struct {
	Knowledge []ExtractedKnowledge `json:"knowledge"`
	Skipped   bool                 `json:"skipped"` // true if no significant knowledge found
	Reason    string               `json:"reason"`  // reason for skipping (if skipped)
}

// ExtractKnowledge uses AI to extract potential knowledge from code changes
//
//isoform:destination yansu_backend
func (c *Client) ExtractKnowledge(ctx context.Context, req *ExtractKnowledgeRequest) (*ExtractKnowledgeResponse, error) {
	url := fmt.Sprintf("%s/api/orgs/%s/products/%s/knowledge/extract",
		c.baseURL, c.organization, c.product)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var result ExtractKnowledgeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// Verify verifies the token is valid by fetching the user profile
//
//isoform:destination yansu_backend
func (c *Client) Verify(ctx context.Context) error {
	url := fmt.Sprintf("%s/api/auth/profile", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.handleError(resp)
	}

	return nil
}

// GetProfile returns the current user's profile.
//
//isoform:destination yansu_backend
func (c *Client) GetProfile(ctx context.Context) (*UserProfile, error) {
	url := fmt.Sprintf("%s/api/auth/profile", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}
	var profile UserProfile
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &profile, nil
}

// SlackBinding represents a personal Slack identity binding returned by GET /api/daemon/me/binding.
type SlackBinding struct {
	BindingID   int    `json:"binding_id"`
	TeamID      string `json:"team_id"`
	SlackUserID string `json:"slack_user_id"`
}

// GetMySlackBinding returns the most recent Slack binding for the authenticated user.
// Used by the daemon at startup to auto-discover team_id and slack_user_id.
//
//isoform:destination yansu_backend
func (c *Client) GetMySlackBinding(ctx context.Context) (*SlackBinding, error) {
	url := fmt.Sprintf("%s/api/daemon/me/binding", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no Slack binding found — visit %s/settings/slack to connect your workspace", c.baseURL)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}
	var binding SlackBinding
	if err := json.NewDecoder(resp.Body).Decode(&binding); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &binding, nil
}

// ListOrganizations returns all organizations the current user belongs to.
//
//isoform:destination yansu_backend
func (c *Client) ListOrganizations(ctx context.Context) ([]Organization, error) {
	url := fmt.Sprintf("%s/api/orgs?page_size=100", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	return decodeListOrPagedItems[Organization](body)
}

// ListProducts returns all products in the given organization.
//
//isoform:destination yansu_backend
func (c *Client) ListProducts(ctx context.Context, org string) ([]Product, error) {
	url := fmt.Sprintf("%s/api/orgs/%s/products?page_size=100", c.baseURL, org)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	return decodeListOrPagedItems[Product](body)
}

// ListProjectsInProduct returns all projects in the given org/product.
//
//isoform:destination yansu_backend
func (c *Client) ListProjectsInProduct(ctx context.Context, org, product string) ([]*Project, error) {
	url := fmt.Sprintf("%s/api/orgs/%s/products/%s/projects", c.baseURL, org, product)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	type projectItem struct {
		UUID         string    `json:"uuid"`
		Name         string    `json:"name"`
		DisplayName  string    `json:"display_name"`
		ProductUUID  string    `json:"product_uuid"`
		CodingBranch string    `json:"coding_branch"`
		CreatedAt    time.Time `json:"created_at"`
	}
	toProjects := func(items []projectItem) []*Project {
		result := make([]*Project, len(items))
		for i, item := range items {
			result[i] = &Project{
				UUID:         item.UUID,
				Name:         item.Name,
				Organization: org,
				Product:      product,
				ProductUUID:  item.ProductUUID,
				CodingBranch: item.CodingBranch,
				CreatedAt:    item.CreatedAt,
			}
		}
		return result
	}

	items, decodeErr := decodeListOrPagedItems[projectItem](body)
	if decodeErr != nil {
		return nil, decodeErr
	}
	return toProjects(items), nil
}

// decodeListOrPagedItems decodes a JSON body that is either a direct array of T
// or a paginated envelope with an "items" field containing the array.
func decodeListOrPagedItems[T any](body []byte) ([]T, error) {
	var direct []T
	if err := json.Unmarshal(body, &direct); err == nil {
		return direct, nil
	}
	var paged struct {
		Items []T `json:"items"`
	}
	if err := json.Unmarshal(body, &paged); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return paged.Items, nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "yansu-agent/1.0")
}

func (c *Client) handleError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)

	var errResp struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error)
	}
	if errResp.Message != "" {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Message)
	}

	return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
}

// DaemonContextMessage represents a single message in conversation context.
type DaemonContextMessage struct {
	UserID    string    `json:"user_id"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

// DaemonFetchContextResponse is the response from POST /api/daemon/fetch-context.
type DaemonFetchContextResponse struct {
	Messages []DaemonContextMessage `json:"messages"`
}

// DaemonFetchContext retrieves conversation context for a relay event.
//
//isoform:destination yansu_backend
func (c *Client) DaemonFetchContext(ctx context.Context, relayID string) (*DaemonFetchContextResponse, error) {
	url := fmt.Sprintf("%s/api/daemon/fetch-context", c.baseURL)
	payload, _ := json.Marshal(map[string]string{"relay_id": relayID})
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}
	var result DaemonFetchContextResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// DaemonVerifyResponse is the response from POST /api/daemon/verify-slack.
type DaemonVerifyResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// DaemonConfirmResponse is the response from POST /api/daemon/confirm-slack.
type DaemonConfirmResponse struct {
	Status string `json:"status"`
	TeamID string `json:"team_id,omitempty"`
	Error  string `json:"error,omitempty"`
}

// DaemonVerifySlack initiates Slack user verification by sending a DM with a code.
//
//isoform:destination yansu_backend
func (c *Client) DaemonVerifySlack(ctx context.Context, slackUserID string) (*DaemonVerifyResponse, error) {
	url := fmt.Sprintf("%s/api/daemon/verify-slack", c.baseURL)
	payload, _ := json.Marshal(map[string]string{"slack_user_id": slackUserID})
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	var result DaemonVerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if result.Error != "" {
			return &result, nil
		}
		return nil, fmt.Errorf("API error (%d)", resp.StatusCode)
	}
	return &result, nil
}

// DaemonConfirmSlack confirms a Slack verification code and creates the binding.
//
//isoform:destination yansu_backend
func (c *Client) DaemonConfirmSlack(ctx context.Context, slackUserID, code string) (*DaemonConfirmResponse, error) {
	url := fmt.Sprintf("%s/api/daemon/confirm-slack", c.baseURL)
	payload, _ := json.Marshal(map[string]string{
		"slack_user_id": slackUserID,
		"code":          code,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	var result DaemonConfirmResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if result.Error != "" {
			return &result, nil
		}
		return nil, fmt.Errorf("API error (%d)", resp.StatusCode)
	}
	return &result, nil
}

// ============================================================================
// SOP Types
// ============================================================================

// StageSummaryResponse matches backend viewmodels.StageSummaryResponse.
type StageSummaryResponse struct {
	UUID            string    `json:"uuid"`
	Name            string    `json:"name"`
	DisplayName     string    `json:"display_name"`
	Type            string    `json:"type"`
	ProjectUUID     string    `json:"project_uuid"`
	ParentStageUUID *string   `json:"parent_stage_uuid"`
	Status          string    `json:"status"`
	HasContent      bool      `json:"has_content"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// StageDetailResponse matches backend viewmodels.StageResponse (full).
type StageDetailResponse struct {
	UUID            string         `json:"uuid"`
	Name            string         `json:"name"`
	DisplayName     string         `json:"display_name"`
	Type            string         `json:"type"`
	ProjectUUID     string         `json:"project_uuid"`
	ParentStageUUID *string        `json:"parent_stage_uuid"`
	Status          string         `json:"status"`
	Content         string         `json:"content"`
	Metadata        map[string]any `json:"metadata"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// FeedbackItem matches backend viewmodels.FeedbackResponse.
type FeedbackItem struct {
	UUID                 string         `json:"uuid"`
	StageVersionUUID     string         `json:"stage_version_uuid"`
	UserUUID             string         `json:"user_uuid"`
	Type                 string         `json:"type"`
	Status               string         `json:"status"`
	Content              string         `json:"content"`
	Metadata             map[string]any `json:"metadata"`
	RequiresAIProcessing bool           `json:"requires_ai_processing"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

// PendingReplyFeedback matches backend viewmodels.PendingReplyFeedbackResponse.
type PendingReplyFeedback struct {
	UUID              string     `json:"uuid"`
	StageName         string     `json:"stage_name"`
	StageDisplayName  string     `json:"stage_display_name"`
	Content           string     `json:"content"`
	Status            string     `json:"status"`
	LatestCommentAt   *time.Time `json:"latest_comment_at,omitempty"`
	LatestCommentUser string     `json:"latest_comment_user,omitempty"`
	CommentCount      int        `json:"comment_count"`
	CreatedAt         time.Time  `json:"created_at"`
}

// FeedbackCommentItem represents a feedback comment.
type FeedbackCommentItem struct {
	UUID         string    `json:"uuid"`
	FeedbackUUID string    `json:"feedback_uuid"`
	UserUUID     string    `json:"user_uuid"`
	Content      string    `json:"content"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

// FeedbackCommentUser is a lightweight user embedded in comment list responses.
type FeedbackCommentUser struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// FeedbackCommentListItem represents a feedback comment with user info (from list endpoint).
type FeedbackCommentListItem struct {
	UUID         string              `json:"uuid"`
	FeedbackUUID string              `json:"feedback_uuid"`
	UserUUID     string              `json:"user_uuid"`
	Content      string              `json:"content"`
	Status       string              `json:"status"`
	CreatedAt    time.Time           `json:"created_at"`
	User         FeedbackCommentUser `json:"user"`
}

// CreateFeedbackCommentRequest for POST /api/feedback/{uuid}/comments.
type CreateFeedbackCommentRequest struct {
	Content string `json:"content"`
}

// StageVersionItem matches backend viewmodels.StageVersionResponse (summary fields).
type StageVersionItem struct {
	UUID          string    `json:"uuid"`
	VersionNumber int       `json:"version_number"`
	Source        string    `json:"source"`
	CreatedAt     time.Time `json:"created_at"`
}

// RestoreStageVersionRequest for POST .../versions/restore.
type RestoreStageVersionRequest struct {
	VersionNumber int `json:"version_number"`
}

// ExecutorTaskSummary matches backend viewmodels.StageTasksSummaryResponse.
type ExecutorTaskSummary struct {
	Total     int `json:"total"`
	Running   int `json:"running"`
	Pending   int `json:"pending"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// UnmarshalJSON supports both summary response formats:
// 1) Flat counts: {"total":1,"running":1,...}
// 2) Nested counts: {"total":1,"by_status":{"running":1,...}}
func (s *ExecutorTaskSummary) UnmarshalJSON(data []byte) error {
	type alias ExecutorTaskSummary
	var flat alias
	if err := json.Unmarshal(data, &flat); err != nil {
		return err
	}

	var nested struct {
		Total    int            `json:"total"`
		ByStatus map[string]int `json:"by_status"`
	}
	if err := json.Unmarshal(data, &nested); err != nil {
		return err
	}

	*s = ExecutorTaskSummary(flat)
	if nested.ByStatus == nil {
		return nil
	}
	s.Total = nested.Total

	if v, ok := nested.ByStatus["running"]; ok {
		s.Running = v
	}
	if v, ok := nested.ByStatus["pending"]; ok {
		s.Pending = v
	}
	if v, ok := nested.ByStatus["completed"]; ok {
		s.Completed = v
	}
	if v, ok := nested.ByStatus["failed"]; ok {
		s.Failed = v
	}
	if v, ok := nested.ByStatus["cancelled"]; ok {
		s.Cancelled = v
	}
	return nil
}

// ============================================================================
// decodeListResponse — handles multiple backend list response formats
// ============================================================================

// decodeListResponse tries: raw array → {data:[]} → {items:[]} wrappers.
func decodeListResponse[T any](body []byte) ([]T, int, error) {
	var direct []T
	if err := json.Unmarshal(body, &direct); err == nil {
		return direct, len(direct), nil
	}
	var dataWrap struct {
		Data  []T `json:"data"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(body, &dataWrap); err == nil && dataWrap.Data != nil {
		return dataWrap.Data, dataWrap.Total, nil
	}
	var itemsWrap struct {
		Items []T `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(body, &itemsWrap); err != nil {
		return nil, 0, fmt.Errorf("failed to decode list response: %w", err)
	}
	return itemsWrap.Items, itemsWrap.Total, nil
}

// ============================================================================
// SOP API Methods
// ============================================================================

// projectURL returns the base hierarchical URL for the configured project.
func (c *Client) projectURL() string {
	return fmt.Sprintf("%s/api/orgs/%s/products/%s/projects/%s",
		c.baseURL, c.organization, c.product, c.project)
}

// ListStages returns all stages for the configured project (summary mode).
//
//isoform:destination yansu_backend
func (c *Client) ListStages(ctx context.Context) ([]StageSummaryResponse, error) {
	url := c.projectURL() + "/stages?summary=true"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	items, _, err := decodeListResponse[StageSummaryResponse](body)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// GetStage returns full details for a single stage.
//
//isoform:destination yansu_backend
func (c *Client) GetStage(ctx context.Context, stageName string) (*StageDetailResponse, error) {
	url := fmt.Sprintf("%s/stages/%s", c.projectURL(), stageName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var result StageDetailResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// RunStage triggers execution of a stage.
//
//isoform:destination yansu_backend
func (c *Client) RunStage(ctx context.Context, stageName string) (*StageDetailResponse, error) {
	url := fmt.Sprintf("%s/stages/%s/run", c.projectURL(), stageName)

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var result StageDetailResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// ListFeedbacksByStage returns feedbacks for all versions of a stage.
//
//isoform:destination yansu_backend
func (c *Client) ListFeedbacksByStage(ctx context.Context, stageName string) ([]FeedbackItem, int, error) {
	url := fmt.Sprintf("%s/stages/%s/feedback", c.projectURL(), stageName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, c.handleError(resp)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read response: %w", err)
	}

	return decodeListResponse[FeedbackItem](body)
}

// ListPendingReplyFeedbacks returns feedbacks awaiting the current user's reply.
//
//isoform:destination yansu_backend
func (c *Client) ListPendingReplyFeedbacks(ctx context.Context) ([]PendingReplyFeedback, int, error) {
	url := c.projectURL() + "/feedback/pending-reply"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, c.handleError(resp)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read response: %w", err)
	}

	return decodeListResponse[PendingReplyFeedback](body)
}

// ListFeedbackComments returns all comments for a given feedback UUID (flat API).
//
//isoform:destination yansu_backend
func (c *Client) ListFeedbackComments(ctx context.Context, feedbackUUID string) ([]FeedbackCommentListItem, int, error) {
	const pageSize = 200

	var (
		allComments []FeedbackCommentListItem
		total       int
		offset      int
	)

	for {
		url := fmt.Sprintf("%s/api/feedback/%s/comments?limit=%d&offset=%d", c.baseURL, feedbackUUID, pageSize, offset)

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create request: %w", err)
		}
		c.setHeaders(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to send request: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			return nil, 0, c.handleError(resp)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read response: %w", err)
		}

		// Backend returns {comments: [], total: N}
		var wrap struct {
			Comments []FeedbackCommentListItem `json:"comments"`
			Total    int                       `json:"total"`
		}
		if err := json.Unmarshal(body, &wrap); err != nil {
			return nil, 0, fmt.Errorf("failed to decode response: %w", err)
		}

		if total == 0 {
			total = wrap.Total
		}

		pageCount := len(wrap.Comments)
		allComments = append(allComments, wrap.Comments...)

		switch {
		case total > 0 && len(allComments) >= total:
			return allComments, total, nil
		case pageCount == 0:
			return allComments, len(allComments), nil
		case total == 0 && pageCount < pageSize:
			return allComments, len(allComments), nil
		default:
			offset += pageCount
		}
	}
}

// CreateFeedbackComment creates a comment on a feedback (flat API).
//
//isoform:destination yansu_backend
func (c *Client) CreateFeedbackComment(ctx context.Context, feedbackUUID string, commentReq CreateFeedbackCommentRequest) (*FeedbackCommentItem, error) {
	url := fmt.Sprintf("%s/api/feedback/%s/comments", c.baseURL, feedbackUUID)

	body, err := json.Marshal(commentReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, c.handleError(resp)
	}

	var result FeedbackCommentItem
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// RetryFeedback retries AI processing for a feedback (flat API).
//
//isoform:destination yansu_backend
func (c *Client) RetryFeedback(ctx context.Context, feedbackUUID string) error {
	url := fmt.Sprintf("%s/api/feedback/%s/retry", c.baseURL, feedbackUUID)

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.handleError(resp)
	}
	return nil
}

// GetFeedback returns a single feedback by UUID (flat API).
//
//isoform:destination yansu_backend
func (c *Client) GetFeedback(ctx context.Context, feedbackUUID string) (*FeedbackItem, error) {
	url := fmt.Sprintf("%s/api/feedback/%s", c.baseURL, feedbackUUID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var result FeedbackItem
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// UpdateFeedback updates a feedback (flat API). Used for resolve/status changes.
//
//isoform:destination yansu_backend
func (c *Client) UpdateFeedback(ctx context.Context, feedbackUUID string, status string) error {
	url := fmt.Sprintf("%s/api/feedback/%s", c.baseURL, feedbackUUID)

	payload := map[string]string{"status": status}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return c.handleError(resp)
	}
	return nil
}

// UpdateStageRequest is the request body for updating stage content.
type UpdateStageRequest struct {
	Content           string `json:"content"`
	ChangeDescription string `json:"change_description,omitempty"`
}

// UpdateStage updates the content of a stage, creating a new minor version.
//
//isoform:destination yansu_backend
func (c *Client) UpdateStage(ctx context.Context, stageName string, req UpdateStageRequest) (*StageDetailResponse, error) {
	url := fmt.Sprintf("%s/stages/%s", c.projectURL(), stageName)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var result StageDetailResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// GetStageTasksSummary returns task execution summary for a stage.
//
//isoform:destination yansu_backend
func (c *Client) GetStageTasksSummary(ctx context.Context, stageName string) (*ExecutorTaskSummary, error) {
	url := fmt.Sprintf("%s/stages/%s/tasks/summary", c.projectURL(), stageName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var result ExecutorTaskSummary
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// ListStageVersions returns version history for a stage.
//
//isoform:destination yansu_backend
func (c *Client) ListStageVersions(ctx context.Context, stageName string) ([]StageVersionItem, int, error) {
	url := fmt.Sprintf("%s/stages/%s/versions", c.projectURL(), stageName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, c.handleError(resp)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read response: %w", err)
	}

	return decodeListResponse[StageVersionItem](body)
}

// ============================================================================
// Scenario Types and Methods
// ============================================================================

// ScenarioItem represents a scenario in a list response.
type ScenarioItem struct {
	UUID             string     `json:"uuid"`
	ScenarioSequence int        `json:"scenario_sequence"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	Status           string     `json:"status"`
	QueuedAt         time.Time  `json:"queued_at"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// ChecklistItem represents a single item in a scenario checklist.
type ChecklistItem struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Steps       []string `json:"steps,omitempty"`
	Checked     bool     `json:"checked"`
}

// ChecklistData holds the full checklist for a scenario.
type ChecklistData struct {
	Title       string          `json:"title,omitempty"`
	Notes       string          `json:"notes,omitempty"`
	GeneratedAt string          `json:"generated_at,omitempty"`
	Items       []ChecklistItem `json:"items"`
}

// ScenarioMetadataResponse holds optional metadata returned with a scenario.
type ScenarioMetadataResponse struct {
	Checklist *ChecklistData `json:"checklist,omitempty"`
}

// ScenarioDetail is the full scenario response from the API.
type ScenarioDetail struct {
	UUID             string                    `json:"uuid"`
	ScenarioSequence int                       `json:"scenario_sequence"`
	Name             string                    `json:"name"`
	Description      string                    `json:"description"`
	Status           string                    `json:"status"`
	Error            *string                   `json:"error,omitempty"`
	Metadata         *ScenarioMetadataResponse `json:"metadata,omitempty"`
	QueuedAt         time.Time                 `json:"queued_at"`
	CompletedAt      *time.Time                `json:"completed_at,omitempty"`
	CreatedAt        time.Time                 `json:"created_at"`
}

// ListScenarios returns all scenarios for the configured project.
//
//isoform:destination yansu_backend
func (c *Client) ListScenarios(ctx context.Context) ([]ScenarioItem, error) {
	url := fmt.Sprintf("%s/scenarios", c.projectURL())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var result struct {
		Scenarios []ScenarioItem `json:"scenarios"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return result.Scenarios, nil
}

// GetScenario returns full details for a single scenario by UUID.
//
//isoform:destination yansu_backend
func (c *Client) GetScenario(ctx context.Context, uuid string) (*ScenarioDetail, error) {
	url := fmt.Sprintf("%s/scenarios/%s", c.projectURL(), uuid)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var result ScenarioDetail
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// RequeueScenario re-queues a failed or passed scenario.
//
//isoform:destination yansu_backend
func (c *Client) RequeueScenario(ctx context.Context, uuid string) (*ScenarioDetail, error) {
	url := fmt.Sprintf("%s/scenarios/%s/requeue", c.projectURL(), uuid)

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var result ScenarioDetail
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// CancelScenario cancels a queued or building scenario.
//
//isoform:destination yansu_backend
func (c *Client) CancelScenario(ctx context.Context, uuid string) (*ScenarioDetail, error) {
	url := fmt.Sprintf("%s/scenarios/%s/cancel", c.projectURL(), uuid)

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var result ScenarioDetail
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// GenerateChecklistResponse is returned when checklist generation is accepted.
type GenerateChecklistResponse struct {
	ExecutorTaskUUID string `json:"executor_task_uuid"`
	Message          string `json:"message"`
}

// GenerateScenarioChecklist triggers async checklist generation for a passed scenario.
//
//isoform:destination yansu_backend
func (c *Client) GenerateScenarioChecklist(ctx context.Context, uuid string) (*GenerateChecklistResponse, error) {
	url := fmt.Sprintf("%s/scenarios/%s/checklist/generate", c.projectURL(), uuid)

	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return nil, c.handleError(resp)
	}

	var result GenerateChecklistResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// UpdateScenarioChecklistItem sets the checked state of a checklist item.
//
//isoform:destination yansu_backend
func (c *Client) UpdateScenarioChecklistItem(ctx context.Context, scenarioUUID, itemID string, checked bool) (*ScenarioDetail, error) {
	url := fmt.Sprintf("%s/scenarios/%s/checklist/items/%s", c.projectURL(), scenarioUUID, itemID)

	body, err := json.Marshal(map[string]bool{"checked": checked})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PATCH", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}

	var result ScenarioDetail
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// RestoreStageVersion restores a stage to a previous version.
//
//isoform:destination yansu_backend
func (c *Client) RestoreStageVersion(ctx context.Context, stageName string, versionNumber int) error {
	url := fmt.Sprintf("%s/stages/%s/versions/restore", c.projectURL(), stageName)

	payload := RestoreStageVersionRequest{VersionNumber: versionNumber}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return c.handleError(resp)
	}
	return nil
}

// SlackFileDownloadRequest represents a request to download a Slack file.
type SlackFileDownloadRequest struct {
	TeamID string `json:"team_id"`
	FileID string `json:"file_id"`
	URL    string `json:"url"`
}

// SlackFileDownloadResponse represents the response with file content.
type SlackFileDownloadResponse struct {
	FileID   string `json:"file_id"`
	Content  []byte `json:"content"`
	MimeType string `json:"mime_type"`
	Size     int    `json:"size"`
}

// DownloadSlackFile downloads a Slack file via the backend API.
//
//isoform:destination yansu_backend
func (c *Client) DownloadSlackFile(ctx context.Context, teamID, fileID, url string) (*SlackFileDownloadResponse, error) {
	apiURL := fmt.Sprintf("%s/api/slack/files/download", c.baseURL)

	reqBody := SlackFileDownloadRequest{
		TeamID: teamID,
		FileID: fileID,
		URL:    url,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result SlackFileDownloadResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// APITokenResponse represents the response from creating an API token
type APITokenResponse struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
}

// CreateAPIToken creates a long-lived API token
//
//isoform:destination yansu_backend
func (c *Client) CreateAPIToken(ctx context.Context, name string, expiresInDays int) (*APITokenResponse, error) {
	url := fmt.Sprintf("%s/api/auth/api-tokens", c.baseURL)
	body := map[string]interface{}{
		"name":            name,
		"expires_in_days": expiresInDays,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, c.handleError(resp)
	}
	var tokenResp APITokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &tokenResp, nil
}
