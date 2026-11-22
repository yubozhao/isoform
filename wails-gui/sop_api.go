package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"wails-gui/internal/sop"
	"wails-gui/storage"

	"github.com/google/uuid"
)

const (
	sopAuthorEventLimit = 100
	// sopManualDefaultWindow is the evidence window when GenerateSOPFromRange
	// is called without an explicit start.
	sopManualDefaultWindow = 3 * 24 * time.Hour
	// sopManualMaxWindow caps how much evidence a manual run may span.
	sopManualMaxWindow = 14 * 24 * time.Hour
)

type SOPAuthorEvent struct {
	TimeMs  int64  `json:"timeMs"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Tool    string `json:"tool"`
	Error   string `json:"error"`
}

type SOPAuthorStatus struct {
	Running   bool             `json:"running"`
	Title     string           `json:"title"`
	Source    string           `json:"source"`
	SourceID  string           `json:"sourceId"`
	StartedAt int64            `json:"startedAt"`
	UpdatedAt int64            `json:"updatedAt"`
	LastEvent string           `json:"lastEvent"`
	LastError string           `json:"lastError"`
	Events    []SOPAuthorEvent `json:"events"`
}

// SOPRunResult mirrors HandoffAcceptResult: everything the frontend needs to
// start the execution chat session for an SOP run.
type SOPRunResult struct {
	SOPID     string `json:"sopId"`
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Prompt    string `json:"prompt"`
	Workdir   string `json:"workdir"`
	Model     string `json:"model"`
}

func (a *App) appendSOPAuthorEvent(kind, message, toolName, errorText string) {
	now := time.Now().UnixMilli()
	a.sopAuthorStatusMu.Lock()
	st := a.sopAuthorStatus
	st.UpdatedAt = now
	st.LastEvent = kind
	if errorText != "" {
		st.LastError = errorText
	}
	st.Events = append([]SOPAuthorEvent{{
		TimeMs:  now,
		Kind:    kind,
		Message: message,
		Tool:    toolName,
		Error:   errorText,
	}}, st.Events...)
	if len(st.Events) > sopAuthorEventLimit {
		st.Events = st.Events[:sopAuthorEventLimit]
	}
	a.sopAuthorStatus = st
	out := st
	a.sopAuthorStatusMu.Unlock()

	a.emitBrowserEvent("sop:author-status", out)
}

func (a *App) startSOPAuthorStatus(spec sopAuthorSpec) {
	now := time.Now().UnixMilli()
	a.sopAuthorStatusMu.Lock()
	st := SOPAuthorStatus{
		Running:   true,
		Title:     strings.TrimSpace(spec.Name),
		Source:    string(spec.Source),
		SourceID:  strings.TrimSpace(spec.SourceID),
		StartedAt: now,
		UpdatedAt: now,
		LastEvent: "started",
		Events: append([]SOPAuthorEvent{{
			TimeMs:  now,
			Kind:    "started",
			Message: "SOP author loop started",
		}}, a.sopAuthorStatus.Events...),
	}
	if len(st.Events) > sopAuthorEventLimit {
		st.Events = st.Events[:sopAuthorEventLimit]
	}
	a.sopAuthorStatus = st
	out := st
	a.sopAuthorStatusMu.Unlock()

	a.emitBrowserEvent("sop:author-status", out)
}

func (a *App) finishSOPAuthorStatus(kind, message, errorText string) {
	now := time.Now().UnixMilli()
	a.sopAuthorStatusMu.Lock()
	st := a.sopAuthorStatus
	st.Running = false
	st.UpdatedAt = now
	st.LastEvent = kind
	if errorText != "" {
		st.LastError = errorText
	}
	st.Events = append([]SOPAuthorEvent{{
		TimeMs:  now,
		Kind:    kind,
		Message: message,
		Error:   errorText,
	}}, st.Events...)
	if len(st.Events) > sopAuthorEventLimit {
		st.Events = st.Events[:sopAuthorEventLimit]
	}
	a.sopAuthorStatus = st
	out := st
	a.sopAuthorStatusMu.Unlock()

	a.emitBrowserEvent("sop:author-status", out)
}

// GetSOPAuthorStatus returns the live SOP author progress. Wails binding.
func (a *App) GetSOPAuthorStatus() SOPAuthorStatus {
	a.sopAuthorStatusMu.Lock()
	defer a.sopAuthorStatusMu.Unlock()
	return a.sopAuthorStatus
}

// ListSOPs returns every SOP, newest first. Wails binding.
func (a *App) ListSOPs() (perfValue []sop.Entry, perfErr error) {
	// Playbooks, SOPs and goals are off unless their switch is on
	// (feature_switches.go).
	if !featureOn(featurePlaybooks) {
		return []sop.Entry{}, nil
	}
	defer logPageBinding("ListSOPs", time.Now(), &perfValue)
	if a.sopStore == nil {
		return nil, nil
	}
	return a.sopStore.List()
}

// SaveSOP persists a user edit to an SOP. The first edit promotes a draft to
// confirmed. Wails binding.
func (a *App) SaveSOP(id, title, description, trigger, body string) (sop.Entry, error) {
	// Playbooks, SOPs and goals are off unless their switch is on
	// (feature_switches.go).
	if !featureOn(featurePlaybooks) {
		return sop.Entry{}, errFeatureOff(featurePlaybooks)
	}
	if a.sopStore == nil {
		return sop.Entry{}, fmt.Errorf("sop store not ready")
	}
	saved, err := a.sopStore.Mutate(id, func(e *sop.Entry) {
		if t := strings.TrimSpace(title); t != "" {
			e.Title = t
		}
		e.Description = strings.TrimSpace(description)
		e.Trigger = strings.TrimSpace(trigger)
		e.Body = body
		e.Status = sop.StatusConfirmed
	})
	if err != nil {
		return sop.Entry{}, fmt.Errorf("save sop %s: %w", id, err)
	}
	// The user wrote these steps: an approved schedule keeps running them.
	a.repinPlaybookBody(saved)
	a.emitBrowserEvent("sop:updated", map[string]any{"id": saved.ID, "status": string(saved.Status)})
	return saved, nil
}

// DeleteSOP removes an SOP bundle. Wails binding.
func (a *App) DeleteSOP(id string) error {
	if a.sopStore == nil {
		return fmt.Errorf("sop store not ready")
	}
	if err := a.sopStore.Delete(id); err != nil {
		return err
	}
	a.forgetPlaybook(id)
	a.emitBrowserEvent("sop:updated", map[string]any{"id": id, "deleted": true})
	if a.telemetry != nil {
		a.telemetry.Capture("sop.deleted", map[string]any{})
	}
	return nil
}

// RunSOP creates the chat session that will execute an SOP once, mirroring
// AcceptHandoff: the frontend sends the returned prompt through the normal
// chat send path so the run has streaming, cancel, and follow-up behavior.
// Running does not change the SOP's status — SOPs are reusable. Wails binding.
func (a *App) RunSOP(id string) (SOPRunResult, error) {
	// Playbooks, SOPs and goals are off unless their switch is on
	// (feature_switches.go).
	if !featureOn(featurePlaybooks) {
		return SOPRunResult{}, errFeatureOff(featurePlaybooks)
	}
	if a.sopStore == nil {
		return SOPRunResult{}, fmt.Errorf("sop store not ready")
	}
	e, err := a.sopStore.Get(id)
	if err != nil {
		return SOPRunResult{}, fmt.Errorf("load sop %s: %w", id, err)
	}

	sessionID, err := a.createSOPExecutionSession(e)
	if err != nil {
		return SOPRunResult{}, fmt.Errorf("create sop session: %w", err)
	}

	if a.telemetry != nil {
		a.telemetry.Capture("sop.run", map[string]any{
			"version":       e.Version,
			"scripts_count": len(e.Scripts),
		})
	}

	return SOPRunResult{
		SOPID:     e.ID,
		SessionID: sessionID,
		Title:     e.Title,
		Prompt:    a.buildSOPRunPrompt(e),
		Workdir:   "",
		Model:     a.chatTaskModel(),
	}, nil
}

// buildSOPRunPrompt prefixes the SOP body with execution instructions: where
// the bundle lives, how to use its scripts, and the checkpoint discipline.
func (a *App) buildSOPRunPrompt(e sop.Entry) string {
	var b strings.Builder
	b.WriteString("Execute this SOP (Standard Operating Procedure) end-to-end.\n\n")
	fmt.Fprintf(&b, "The SOP bundle lives at: %s\n", a.sopStore.PathFor(e.ID))
	if len(e.Scripts) > 0 {
		fmt.Fprintf(&b, "Helper scripts in the bundle (run with Bash when a step references them): %s\n", strings.Join(e.Scripts, ", "))
	}
	b.WriteString("\nWork through the numbered steps in order. After each step, verify its Checkpoint before moving on and report the checkpoint result. If a step cannot be completed, stop and explain what is blocking instead of skipping ahead.\n\n---\n\n")
	b.WriteString(e.Body)
	return b.String()
}

// createSOPExecutionSession creates a plain standalone chat session for an SOP
// run. Unlike handoff execution it joins no sidebar group and posts no chip —
// the run is an ordinary chat the user just watched start.
func (a *App) createSOPExecutionSession(e sop.Entry) (string, error) {
	if a.store == nil {
		return "", fmt.Errorf("store not initialized")
	}
	ctx := context.Background()
	projectID, workspaceID, err := a.standaloneChatProjectAndWorkspace(ctx)
	if err != nil {
		return "", err
	}
	sessionLocalID := uuid.New().String()
	label := fmt.Sprintf("SOP: %s", e.Title)
	if len(label) > 60 {
		label = label[:57] + "..."
	}
	sess := storage.SessionRow{
		LocalID:     sessionLocalID,
		ProjectID:   projectID,
		WorkspaceID: workspaceID,
		Label:       label,
		Model:       a.chatTaskModel(),
	}
	if err := a.store.SaveSession(ctx, sess); err != nil {
		return "", fmt.Errorf("save sop session: %w", err)
	}
	return sessionLocalID, nil
}

// GetSOPScript returns the content of one helper script inside an SOP bundle
// (rel as reported by Entry.Scripts, e.g. "scripts/fetch.sh"). Wails binding.
func (a *App) GetSOPScript(id, rel string) (string, error) {
	// Playbooks, SOPs and goals are off unless their switch is on
	// (feature_switches.go).
	if !featureOn(featurePlaybooks) {
		return "", errFeatureOff(featurePlaybooks)
	}
	if a.sopStore == nil {
		return "", fmt.Errorf("sop store not ready")
	}
	return a.sopStore.ReadScript(id, rel)
}

// GenerateSOPFromRange kicks a manual SOP authoring pass over [startMs, endMs]
// (unix ms). endMs <= 0 means now; startMs <= 0 means endMs minus the default
// window. Async: progress streams via sop:author-status, the result lands via
// sop:updated. Returns an error immediately when the author is busy so the UI
// can toast instead of silently dropping the request. Wails binding.
func (a *App) GenerateSOPFromRange(startMs, endMs int64) error {
	// Playbooks, SOPs and goals are off unless their switch is on
	// (feature_switches.go).
	if !featureOn(featurePlaybooks) {
		return errFeatureOff(featurePlaybooks)
	}
	if a.sopStore == nil {
		return fmt.Errorf("sop store not ready")
	}
	if endMs <= 0 {
		endMs = time.Now().UnixMilli()
	}
	if startMs <= 0 {
		startMs = endMs - sopManualDefaultWindow.Milliseconds()
	}
	if startMs >= endMs {
		return fmt.Errorf("invalid range: start must precede end")
	}
	if endMs-startMs > sopManualMaxWindow.Milliseconds() {
		startMs = endMs - sopManualMaxWindow.Milliseconds()
	}

	spec := sopAuthorSpec{
		Action:        "create",
		Source:        sop.SourceManual,
		SourceID:      "manual-" + uuid.New().String(),
		WindowStartMs: startMs,
		WindowEndMs:   endMs,
	}

	// Probe the single-flight gate synchronously so an explicit user request
	// gets immediate busy feedback (the triage path just drops and retries).
	if !a.sopAuthorMu.TryLock() {
		return fmt.Errorf("sop author is busy; try again when the current run finishes")
	}
	go func() {
		defer a.sopAuthorMu.Unlock()
		if _, err := a.authorSOP(context.Background(), spec); err != nil {
			log.Printf("[sop] manual authoring failed: %v", err)
		}
	}()
	return nil
}
