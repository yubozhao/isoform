package main

import (
	"strings"
	"time"

	"wails-gui/internal/pii"
)

const (
	taskOnsetObservationHorizon = 3 * time.Minute
	taskOnsetEvidenceTextLimit  = 320
)

func truncateTaskOnsetText(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return ""
	}
	redacted, _ := pii.RedactText(value)
	if len(redacted) <= taskOnsetEvidenceTextLimit {
		return redacted
	}
	return truncateAtRuneBoundary(redacted, taskOnsetEvidenceTextLimit) + "..."
}
