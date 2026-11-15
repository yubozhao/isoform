package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"wails-gui/internal/handoff"
)

// One-time work the Agency wrote.
//
// The Agency, removed on 2026-10-13, wrote one-time work (handoff source
// "agency") together with a permission request that holds the plan it
// proposed. One-time work shows that plan instead of the internal brief, and
// only starts work whose stored request still matches the entry. These
// requests are read-only now; nothing writes new ones.

// HandoffPlanView is the plan the Agency proposed for a piece of one-time
// work, in the user's terms.
type HandoffPlanView struct {
	Purpose        string   `json:"purpose"`
	DesiredResult  string   `json:"desiredResult"`
	ScopeSummary   string   `json:"scopeSummary"`
	Includes       []string `json:"includes"`
	Excludes       []string `json:"excludes"`
	Access         []string `json:"access"`
	Risk           string   `json:"risk"`
	RiskSummary    string   `json:"riskSummary"`
	Reversibility  string   `json:"reversibility"`
	ExpectedEffort string   `json:"expectedEffort"`
	WhyNow         string   `json:"whyNow"`
}

// agencyHandoffPlanRequest is the part of a stored Agency permission request
// the plan is built from.
type agencyHandoffPlanRequest struct {
	ContextHash string `json:"contextHash"`
	Candidate   struct {
		Kind           string `json:"kind"`
		Name           string `json:"name"`
		Purpose        string `json:"purpose"`
		DesiredOutcome string `json:"desiredOutcome"`
		Scope          struct {
			Summary  string   `json:"summary"`
			Includes []string `json:"includes"`
			Excludes []string `json:"excludes"`
		} `json:"scope"`
		Rationale     string `json:"rationale"`
		Reversibility string `json:"reversibility"`
		Risk          struct {
			Level   string `json:"level"`
			Summary string `json:"summary"`
		} `json:"risk"`
		ExpectedCost struct {
			Summary       string `json:"summary"`
			EffortMinutes int    `json:"effortMinutes"`
		} `json:"expectedCost"`
		AuthorityCapabilities []string `json:"authorityCapabilities"`
	} `json:"candidate"`
}

// agencyHandoffPlan loads the plan stored for an Agency handoff. It returns
// nil when the request is missing, damaged, or does not belong to entry.
func agencyHandoffPlan(db *sql.DB, entry handoff.Entry) *HandoffPlanView {
	if db == nil || entry.Source != handoff.SourceAgency {
		return nil
	}
	var kind, runID, requestJSON, payloadJSON, payloadHash string
	err := db.QueryRow(`
		SELECT kind, run_id, request_json, payload_json, payload_hash
		FROM agency_origination_permission_requests_v1
		WHERE id = ?`,
		strings.TrimSpace(entry.ID),
	).Scan(&kind, &runID, &requestJSON, &payloadJSON, &payloadHash)
	if err != nil {
		return nil
	}
	if kind != "handoff" || runID != entry.SourceID ||
		agencyPayloadHash([]byte(payloadJSON)) != payloadHash {
		return nil
	}
	var request agencyHandoffPlanRequest
	if err := json.Unmarshal([]byte(requestJSON), &request); err != nil {
		return nil
	}
	candidate := request.Candidate
	if strings.TrimSpace(request.ContextHash) == "" ||
		candidate.Kind != "handoff" || candidate.Name != entry.Title {
		return nil
	}
	return &HandoffPlanView{
		Purpose:        agencyBoundedText(candidate.Purpose, 1_200),
		DesiredResult:  agencyBoundedText(candidate.DesiredOutcome, 1_200),
		ScopeSummary:   agencyBoundedText(candidate.Scope.Summary, 1_200),
		Includes:       handoffPlanItems(candidate.Scope.Includes),
		Excludes:       handoffPlanItems(candidate.Scope.Excludes),
		Access:         handoffPlanAccessLabels(candidate.AuthorityCapabilities),
		Risk:           handoffPlanRiskLabel(candidate.Risk.Level),
		RiskSummary:    agencyBoundedText(candidate.Risk.Summary, 1_200),
		Reversibility:  handoffPlanReversibilityLabel(candidate.Reversibility),
		ExpectedEffort: handoffPlanExpectedEffort(candidate.ExpectedCost.Summary, candidate.ExpectedCost.EffortMinutes),
		WhyNow:         agencyBoundedText(candidate.Rationale, 1_200),
	}
}

func handoffPlanItems(values []string) []string {
	items := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = agencyBoundedText(strings.TrimSpace(value), 700)
		if value == "" {
			continue
		}
		key := normalizeAgencyComparisonText(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, value)
	}
	return items
}

func handoffPlanAccessLabels(capabilities []string) []string {
	unique := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if capability = strings.TrimSpace(capability); capability != "" {
			unique[capability] = struct{}{}
		}
	}
	sorted := make([]string, 0, len(unique))
	for capability := range unique {
		sorted = append(sorted, capability)
	}
	sort.Strings(sorted)
	read := false
	create := false
	change := false
	additional := make([]string, 0)
	for _, capability := range sorted {
		switch capability {
		case "tool.read", "tool.glob", "tool.grep":
			read = true
		case "tool.write":
			create = true
		case "tool.edit":
			change = true
		default:
			label := strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(capability)
			label = strings.Join(strings.Fields(label), " ")
			if label != "" {
				additional = append(additional, "Additional access: "+label)
			}
		}
	}
	labels := make([]string, 0, 3+len(additional))
	if read {
		labels = append(labels, "Read local files and information")
	}
	if create {
		labels = append(labels, "Create local files")
	}
	if change {
		labels = append(labels, "Change local files")
	}
	labels = append(labels, additional...)
	if len(labels) == 0 {
		labels = append(labels, "No tool access requested")
	}
	return labels
}

func handoffPlanRiskLabel(level string) string {
	switch strings.TrimSpace(level) {
	case "low":
		return "Low risk"
	case "medium":
		return "Medium risk"
	case "high":
		return "High risk"
	case "critical":
		return "Critical risk"
	default:
		return "Risk not established"
	}
}

func handoffPlanReversibilityLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "reversible":
		return "Reversible plan"
	case "partially_reversible":
		return "Partly reversible plan"
	case "irreversible":
		return "Irreversible plan"
	default:
		return "Reversibility not established"
	}
}

func handoffPlanExpectedEffort(summary string, effortMinutes int) string {
	if summary := agencyBoundedText(strings.TrimSpace(summary), 700); summary != "" {
		return summary
	}
	if effortMinutes == 1 {
		return "About 1 minute"
	}
	if effortMinutes > 1 {
		return fmt.Sprintf("About %d minutes", effortMinutes)
	}
	return "Effort not estimated"
}
