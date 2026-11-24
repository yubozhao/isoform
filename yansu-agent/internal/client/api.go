package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Client is the Yansu API client
type Client struct{}

var ErrRetired = errors.New("The Yansu service is retired.")

// Legacy commands still use these signatures. Never retain credentials or construct a transport.
func New(baseURL, token string) *Client                 { return &Client{} }
func (*Client) SetProject(org, product, project string) {}
func (*Client) SetProductUUID(uuid string)              {}

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
func (c *Client) GetProject(ctx context.Context, org, product, project string) (*Project, error) {
	return nil, ErrRetired
}

// GetProjectCodeRepositories retrieves code repositories for a project
func (c *Client) GetProjectCodeRepositories(ctx context.Context, org, product, project string) ([]*CodeRepository, error) {
	return nil, ErrRetired
}

// StageResponse represents stage data from the API
type StageResponse struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// GetProjectContext retrieves the project context by fetching stages
func (c *Client) GetProjectContext(ctx context.Context) (*ContextResponse, error) {
	return nil, ErrRetired
}

// ListKnowledge retrieves all knowledge for the current product
func (c *Client) ListKnowledge(ctx context.Context) ([]*KnowledgeResponse, error) {
	return nil, ErrRetired
}

// CreateKnowledge creates a new knowledge item
func (c *Client) CreateKnowledge(ctx context.Context, req *CreateKnowledgeRequest) (*KnowledgeResponse, error) {
	return nil, ErrRetired
}

// UpdateKnowledge updates an existing knowledge item
func (c *Client) UpdateKnowledge(ctx context.Context, uuid string, req *UpdateKnowledgeRequest) (*KnowledgeResponse, error) {
	return nil, ErrRetired
}

// DeleteKnowledge deletes a knowledge item
func (c *Client) DeleteKnowledge(ctx context.Context, uuid string) error {
	return ErrRetired
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
func (c *Client) ExtractKnowledge(ctx context.Context, req *ExtractKnowledgeRequest) (*ExtractKnowledgeResponse, error) {
	return nil, ErrRetired
}

// Verify verifies the token is valid by fetching the user profile
func (c *Client) Verify(ctx context.Context) error {
	return ErrRetired
}

// GetProfile returns the current user's profile.
func (c *Client) GetProfile(ctx context.Context) (*UserProfile, error) {
	return nil, ErrRetired
}

// SlackBinding represents a personal Slack identity binding returned by GET /api/daemon/me/binding.
type SlackBinding struct {
	BindingID   int    `json:"binding_id"`
	TeamID      string `json:"team_id"`
	SlackUserID string `json:"slack_user_id"`
}

// GetMySlackBinding returns the most recent Slack binding for the authenticated user.
// Used by the daemon at startup to auto-discover team_id and slack_user_id.
func (c *Client) GetMySlackBinding(ctx context.Context) (*SlackBinding, error) {
	return nil, ErrRetired
}

// ListOrganizations returns all organizations the current user belongs to.
func (c *Client) ListOrganizations(ctx context.Context) ([]Organization, error) {
	return nil, ErrRetired
}

// ListProducts returns all products in the given organization.
func (c *Client) ListProducts(ctx context.Context, org string) ([]Product, error) {
	return nil, ErrRetired
}

// ListProjectsInProduct returns all projects in the given org/product.
func (c *Client) ListProjectsInProduct(ctx context.Context, org, product string) ([]*Project, error) {
	return nil, ErrRetired
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
func (c *Client) DaemonFetchContext(ctx context.Context, relayID string) (*DaemonFetchContextResponse, error) {
	return nil, ErrRetired
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
func (c *Client) DaemonVerifySlack(ctx context.Context, slackUserID string) (*DaemonVerifyResponse, error) {
	return nil, ErrRetired
}

// DaemonConfirmSlack confirms a Slack verification code and creates the binding.
func (c *Client) DaemonConfirmSlack(ctx context.Context, slackUserID, code string) (*DaemonConfirmResponse, error) {
	return nil, ErrRetired
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

// ListStages returns all stages for the configured project (summary mode).
func (c *Client) ListStages(ctx context.Context) ([]StageSummaryResponse, error) {
	return nil, ErrRetired
}

// GetStage returns full details for a single stage.
func (c *Client) GetStage(ctx context.Context, stageName string) (*StageDetailResponse, error) {
	return nil, ErrRetired
}

// RunStage triggers execution of a stage.
func (c *Client) RunStage(ctx context.Context, stageName string) (*StageDetailResponse, error) {
	return nil, ErrRetired
}

// ListFeedbacksByStage returns feedbacks for all versions of a stage.
func (c *Client) ListFeedbacksByStage(ctx context.Context, stageName string) ([]FeedbackItem, int, error) {
	return nil, 0, ErrRetired
}

// ListPendingReplyFeedbacks returns feedbacks awaiting the current user's reply.
func (c *Client) ListPendingReplyFeedbacks(ctx context.Context) ([]PendingReplyFeedback, int, error) {
	return nil, 0, ErrRetired
}

// ListFeedbackComments returns all comments for a given feedback UUID (flat API).
func (c *Client) ListFeedbackComments(ctx context.Context, feedbackUUID string) ([]FeedbackCommentListItem, int, error) {
	return nil, 0, ErrRetired
}

// CreateFeedbackComment creates a comment on a feedback (flat API).
func (c *Client) CreateFeedbackComment(ctx context.Context, feedbackUUID string, commentReq CreateFeedbackCommentRequest) (*FeedbackCommentItem, error) {
	return nil, ErrRetired
}

// RetryFeedback retries AI processing for a feedback (flat API).
func (c *Client) RetryFeedback(ctx context.Context, feedbackUUID string) error {
	return ErrRetired
}

// GetFeedback returns a single feedback by UUID (flat API).
func (c *Client) GetFeedback(ctx context.Context, feedbackUUID string) (*FeedbackItem, error) {
	return nil, ErrRetired
}

// UpdateFeedback updates a feedback (flat API). Used for resolve/status changes.
func (c *Client) UpdateFeedback(ctx context.Context, feedbackUUID string, status string) error {
	return ErrRetired
}

// UpdateStageRequest is the request body for updating stage content.
type UpdateStageRequest struct {
	Content           string `json:"content"`
	ChangeDescription string `json:"change_description,omitempty"`
}

// UpdateStage updates the content of a stage, creating a new minor version.
func (c *Client) UpdateStage(ctx context.Context, stageName string, req UpdateStageRequest) (*StageDetailResponse, error) {
	return nil, ErrRetired
}

// GetStageTasksSummary returns task execution summary for a stage.
func (c *Client) GetStageTasksSummary(ctx context.Context, stageName string) (*ExecutorTaskSummary, error) {
	return nil, ErrRetired
}

// ListStageVersions returns version history for a stage.
func (c *Client) ListStageVersions(ctx context.Context, stageName string) ([]StageVersionItem, int, error) {
	return nil, 0, ErrRetired
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
func (c *Client) ListScenarios(ctx context.Context) ([]ScenarioItem, error) {
	return nil, ErrRetired
}

// GetScenario returns full details for a single scenario by UUID.
func (c *Client) GetScenario(ctx context.Context, uuid string) (*ScenarioDetail, error) {
	return nil, ErrRetired
}

// RequeueScenario re-queues a failed or passed scenario.
func (c *Client) RequeueScenario(ctx context.Context, uuid string) (*ScenarioDetail, error) {
	return nil, ErrRetired
}

// CancelScenario cancels a queued or building scenario.
func (c *Client) CancelScenario(ctx context.Context, uuid string) (*ScenarioDetail, error) {
	return nil, ErrRetired
}

// GenerateChecklistResponse is returned when checklist generation is accepted.
type GenerateChecklistResponse struct {
	ExecutorTaskUUID string `json:"executor_task_uuid"`
	Message          string `json:"message"`
}

// GenerateScenarioChecklist triggers async checklist generation for a passed scenario.
func (c *Client) GenerateScenarioChecklist(ctx context.Context, uuid string) (*GenerateChecklistResponse, error) {
	return nil, ErrRetired
}

// UpdateScenarioChecklistItem sets the checked state of a checklist item.
func (c *Client) UpdateScenarioChecklistItem(ctx context.Context, scenarioUUID, itemID string, checked bool) (*ScenarioDetail, error) {
	return nil, ErrRetired
}

// RestoreStageVersion restores a stage to a previous version.
func (c *Client) RestoreStageVersion(ctx context.Context, stageName string, versionNumber int) error {
	return ErrRetired
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
func (c *Client) DownloadSlackFile(ctx context.Context, teamID, fileID, url string) (*SlackFileDownloadResponse, error) {
	return nil, ErrRetired
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
func (c *Client) CreateAPIToken(ctx context.Context, name string, expiresInDays int) (*APITokenResponse, error) {
	return nil, ErrRetired
}
