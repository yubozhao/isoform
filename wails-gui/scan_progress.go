package main

import (
	"fmt"
	"strings"
	"time"
)

// StageDetailItem is a single label/value pair shown in the stage detail accordion.
type StageDetailItem struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// StageStats tracks one pipeline stage's progress and final statistics.
type StageStats struct {
	Name      string            `json:"name"`
	Status    string            `json:"status"` // "pending" | "running" | "done" | "skipped"
	StartedAt *time.Time        `json:"started_at,omitempty"`
	EndedAt   *time.Time        `json:"ended_at,omitempty"`
	Current   int               `json:"current,omitempty"`
	Total     int               `json:"total,omitempty"`
	Summary   string            `json:"summary,omitempty"`
	Details   []StageDetailItem `json:"details,omitempty"`
}

// initStages creates a Stages slice with all stages set to "pending".
func initStages(names []string) []StageStats {
	stages := make([]StageStats, len(names))
	for i, n := range names {
		stages[i] = StageStats{Name: n, Status: "pending"}
	}
	return stages
}

// transitionStage marks the named stage as "running" and any previously
// "running" stage as "done". Returns the mutated slice.
func transitionStage(stages []StageStats, name string) []StageStats {
	now := time.Now()
	for i := range stages {
		if stages[i].Status == "running" {
			stages[i].Status = "done"
			stages[i].EndedAt = &now
		}
	}
	for i := range stages {
		if stages[i].Name == name {
			stages[i].Status = "running"
			stages[i].StartedAt = &now
			break
		}
	}
	return stages
}

// finishStage marks a single named stage as "done".
func finishStage(stages []StageStats, name string) []StageStats {
	now := time.Now()
	for i := range stages {
		if stages[i].Name == name && stages[i].Status == "running" {
			stages[i].Status = "done"
			stages[i].EndedAt = &now
			break
		}
	}
	return stages
}

// finishStages marks any still-"running" stage as "done".
func finishStages(stages []StageStats) []StageStats {
	now := time.Now()
	for i := range stages {
		if stages[i].Status == "running" {
			stages[i].Status = "done"
			stages[i].EndedAt = &now
		}
	}
	return stages
}

// updateStageProgress sets Current/Total on the currently running stage.
func updateStageProgress(stages []StageStats, current, total int) []StageStats {
	for i := range stages {
		if stages[i].Status == "running" {
			stages[i].Current = current
			stages[i].Total = total
			break
		}
	}
	return stages
}

// setStageSummary sets the Summary field on the named stage.
func setStageSummary(stages []StageStats, name, summary string) []StageStats {
	for i := range stages {
		if stages[i].Name == name {
			stages[i].Summary = summary
			break
		}
	}
	return stages
}

// setStageDetails sets the Details field on the named stage.
func setStageDetails(stages []StageStats, name string, details []StageDetailItem) []StageStats {
	for i := range stages {
		if stages[i].Name == name {
			stages[i].Details = details
			break
		}
	}
	return stages
}

// formatAutomationSummary builds the legacy scan-stage summary. The stage
// name remains "automations" for persisted scan-history compatibility, but
// inferred work now stops at shadow initiatives.
func formatAutomationSummary(count int, names []string) string {
	if count == 0 {
		return "0 shadow initiatives"
	}
	return fmt.Sprintf("%d shadow initiatives: %s", count, strings.Join(names, ", "))
}
