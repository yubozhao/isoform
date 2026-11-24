package client

import (
	"context"
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// decodeListResponse tests
// ---------------------------------------------------------------------------

func TestDecodeListResponse_RawArray(t *testing.T) {
	type item struct {
		Name string `json:"name"`
	}
	body := []byte(`[{"name":"a"},{"name":"b"}]`)
	items, total, err := decodeListResponse[item](body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
	if items[0].Name != "a" || items[1].Name != "b" {
		t.Errorf("items = %+v", items)
	}
}

func TestDecodeListResponse_RawArrayEmpty(t *testing.T) {
	type item struct {
		Name string `json:"name"`
	}
	body := []byte(`[]`)
	items, total, err := decodeListResponse[item](body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want 0", len(items))
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
}

func TestDecodeListResponse_DataWrapper(t *testing.T) {
	type item struct {
		ID int `json:"id"`
	}
	body := []byte(`{"data":[{"id":1},{"id":2},{"id":3}],"total":10}`)
	items, total, err := decodeListResponse[item](body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	if total != 10 {
		t.Errorf("total = %d, want 10", total)
	}
}

func TestDecodeListResponse_DataWrapperEmpty(t *testing.T) {
	type item struct {
		ID int `json:"id"`
	}
	body := []byte(`{"data":[],"total":0}`)
	items, total, err := decodeListResponse[item](body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want 0", len(items))
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
}

func TestDecodeListResponse_ItemsWrapper(t *testing.T) {
	type item struct {
		UUID string `json:"uuid"`
	}
	body := []byte(`{"items":[{"uuid":"abc"}],"total":5}`)
	items, total, err := decodeListResponse[item](body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if items[0].UUID != "abc" {
		t.Errorf("UUID = %q, want %q", items[0].UUID, "abc")
	}
}

func TestDecodeListResponse_ItemsWrapperEmpty(t *testing.T) {
	type item struct {
		UUID string `json:"uuid"`
	}
	body := []byte(`{"items":[],"total":0}`)
	items, total, err := decodeListResponse[item](body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want 0", len(items))
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
}

func TestDecodeListResponse_InvalidJSON(t *testing.T) {
	type item struct{}
	body := []byte(`not json`)
	_, _, err := decodeListResponse[item](body)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestDecodeListResponse_DataNullFallsToItems(t *testing.T) {
	// When data is null, it should fall through to items wrapper.
	type item struct {
		Name string `json:"name"`
	}
	body := []byte(`{"data":null,"items":[{"name":"x"}],"total":1}`)
	items, total, err := decodeListResponse[item](body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
}

// ---------------------------------------------------------------------------
// SOP type serialization sanity checks
// ---------------------------------------------------------------------------

func TestStageSummaryResponse_JSONRoundTrip(t *testing.T) {
	input := `{"uuid":"u1","name":"prd","display_name":"PRD","type":"prd","project_uuid":"p1","parent_stage_uuid":null,"status":"completed","has_content":true,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	var s StageSummaryResponse
	if err := json.Unmarshal([]byte(input), &s); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if s.Name != "prd" || s.Status != "completed" || !s.HasContent {
		t.Errorf("unexpected fields: %+v", s)
	}
}

func TestPendingReplyFeedback_JSONRoundTrip(t *testing.T) {
	input := `{"uuid":"fb1","stage_name":"prd","stage_display_name":"PRD","content":"test","status":"pending","comment_count":3,"created_at":"2026-01-01T00:00:00Z"}`
	var prf PendingReplyFeedback
	if err := json.Unmarshal([]byte(input), &prf); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if prf.UUID != "fb1" || prf.CommentCount != 3 {
		t.Errorf("unexpected fields: %+v", prf)
	}
}

func TestCreateFeedbackCommentRequest_JSON(t *testing.T) {
	req := CreateFeedbackCommentRequest{Content: "looks good"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if m["content"] != "looks good" {
		t.Errorf("content = %q, want %q", m["content"], "looks good")
	}
}

func TestRestoreStageVersionRequest_JSON(t *testing.T) {
	req := RestoreStageVersionRequest{VersionNumber: 5}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var m map[string]int
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if m["version_number"] != 5 {
		t.Errorf("version_number = %d, want 5", m["version_number"])
	}
}

func TestExecutorTaskSummary_Unmarshal_FlatShape(t *testing.T) {
	input := `{"total":4,"running":1,"pending":2,"completed":1,"failed":0,"cancelled":0}`
	var summary ExecutorTaskSummary
	if err := json.Unmarshal([]byte(input), &summary); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if summary.Total != 4 || summary.Running != 1 || summary.Pending != 2 || summary.Completed != 1 || summary.Failed != 0 || summary.Cancelled != 0 {
		t.Errorf("unexpected summary: %+v", summary)
	}
}

func TestExecutorTaskSummary_Unmarshal_ByStatusShape(t *testing.T) {
	input := `{"total":7,"by_status":{"running":2,"pending":1,"completed":3,"failed":1}}`
	var summary ExecutorTaskSummary
	if err := json.Unmarshal([]byte(input), &summary); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if summary.Total != 7 || summary.Running != 2 || summary.Pending != 1 || summary.Completed != 3 || summary.Failed != 1 || summary.Cancelled != 0 {
		t.Errorf("unexpected summary: %+v", summary)
	}
}

func TestLegacyClientReturnsRetired(t *testing.T) {
	c := New("https://legacy.invalid", "fixture")
	if _, _, err := c.ListFeedbackComments(context.Background(), "example"); err != ErrRetired {
		t.Fatal(err)
	}
}
