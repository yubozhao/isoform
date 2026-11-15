package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	selfModelKindPrinciple      = "principle"
	selfModelKindPreference     = "preference"
	selfModelKindDecisionPolicy = "decision_policy"
	selfModelKindBoundary       = "boundary"

	selfModelStatusProvisional = "provisional"
	selfModelStatusActive      = "active"
	selfModelStatusRetracted   = "retracted"

	selfModelContextBudgetChars = 16_000
)

type SelfModelEvidence struct {
	SourceKind string   `json:"sourceKind"`
	SourceID   string   `json:"sourceId"`
	Statement  string   `json:"statement,omitempty"`
	Confidence float64  `json:"confidence"`
	SourceRefs []string `json:"sourceRefs,omitempty"`
	OccurredAt int64    `json:"occurredAt"`
}

type SelfModelEntry struct {
	ID              string              `json:"id"`
	EntryKey        string              `json:"entryKey"`
	Revision        int                 `json:"revision"`
	Kind            string              `json:"kind"`
	Scope           string              `json:"scope,omitempty"`
	Statement       string              `json:"statement"`
	Status          string              `json:"status"`
	Confidence      float64             `json:"confidence"`
	ReasoningStatus string              `json:"reasoningStatus"`
	Evidence        []SelfModelEvidence `json:"evidence"`
	Contradictions  []SelfModelEvidence `json:"contradictions"`
	SourceHash      string              `json:"sourceHash"`
	CreatedAt       int64               `json:"createdAt"`
}

func normalizeSelfModelKind(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case selfModelKindPrinciple,
		selfModelKindPreference,
		selfModelKindDecisionPolicy,
		selfModelKindBoundary:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported self model kind %q", value)
	}
}

func normalizeSelfModelStatus(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case selfModelStatusProvisional, selfModelStatusActive, selfModelStatusRetracted:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported self model status %q", value)
	}
}

func selfModelEntryKey(kind, scope, statement string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
	return sha256Hex([]byte(strings.Join([]string{
		kind,
		strings.ToLower(strings.TrimSpace(scope)),
		normalized,
	}, "\x00")))

}

func selfModelEvidenceKey(item SelfModelEvidence) string {
	return strings.Join([]string{
		strings.TrimSpace(item.SourceKind),
		strings.TrimSpace(item.SourceID),
		strings.TrimSpace(item.Statement),
	}, "\x00")
}

func mergeSelfModelEvidence(left, right []SelfModelEvidence) []SelfModelEvidence {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	byKey := make(map[string]SelfModelEvidence, len(left)+len(right))
	for _, item := range append(append([]SelfModelEvidence(nil), left...), right...) {
		item.SourceKind = strings.TrimSpace(item.SourceKind)
		item.SourceID = strings.TrimSpace(item.SourceID)
		item.Statement = strings.TrimSpace(item.Statement)
		item.Confidence = clampConfidence(item.Confidence)
		item.SourceRefs = uniqueSortedStrings(item.SourceRefs)
		if item.SourceKind == "" || item.SourceID == "" {
			continue
		}
		key := selfModelEvidenceKey(item)
		if current, ok := byKey[key]; ok {
			if item.Confidence > current.Confidence {
				current.Confidence = item.Confidence
			}
			current.SourceRefs = uniqueSortedStrings(append(current.SourceRefs, item.SourceRefs...))
			if item.OccurredAt > current.OccurredAt {
				current.OccurredAt = item.OccurredAt
			}
			byKey[key] = current
			continue
		}
		byKey[key] = item
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]SelfModelEvidence, 0, len(keys))
	for _, key := range keys {
		out = append(out, byKey[key])
	}
	return out
}

func selfModelStatusPriority(value string) int {
	switch value {
	case selfModelStatusActive:
		return 2
	case selfModelStatusProvisional:
		return 1
	case selfModelStatusRetracted:
		return 3
	default:
		return 0
	}
}

func buildSelfModelSourceHash(item SelfModelEntry) (string, error) {
	payload, err := json.Marshal(struct {
		EntryKey        string              `json:"entryKey"`
		Kind            string              `json:"kind"`
		Scope           string              `json:"scope"`
		Statement       string              `json:"statement"`
		Status          string              `json:"status"`
		Confidence      float64             `json:"confidence"`
		ReasoningStatus string              `json:"reasoningStatus"`
		Evidence        []SelfModelEvidence `json:"evidence"`
		Contradictions  []SelfModelEvidence `json:"contradictions"`
	}{
		EntryKey:        item.EntryKey,
		Kind:            item.Kind,
		Scope:           item.Scope,
		Statement:       item.Statement,
		Status:          item.Status,
		Confidence:      item.Confidence,
		ReasoningStatus: item.ReasoningStatus,
		Evidence:        item.Evidence,
		Contradictions:  item.Contradictions,
	})
	if err != nil {
		return "", err
	}
	return sha256Hex(payload), nil
}

func validateSelfModelEntryIntegrity(item SelfModelEntry) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.EntryKey) == "" {
		return errors.New("canonical Self identity is incomplete")
	}
	if item.Revision < 1 || item.CreatedAt <= 0 {
		return fmt.Errorf("canonical Self entry %s has invalid revision metadata", item.ID)
	}
	kind, err := normalizeSelfModelKind(item.Kind)
	if err != nil || kind != item.Kind {
		return fmt.Errorf("canonical Self entry %s has invalid kind", item.ID)
	}
	status, err := normalizeSelfModelStatus(item.Status)
	if err != nil || status != item.Status {
		return fmt.Errorf("canonical Self entry %s has invalid status", item.ID)
	}
	if strings.TrimSpace(item.Scope) != item.Scope ||
		strings.TrimSpace(item.Statement) != item.Statement ||
		strings.TrimSpace(item.ReasoningStatus) != item.ReasoningStatus ||
		item.Statement == "" || item.ReasoningStatus == "" ||
		item.Confidence < 0 || item.Confidence > 1 {
		return fmt.Errorf("canonical Self entry %s is not normalized", item.ID)
	}
	expectedHash, err := buildSelfModelSourceHash(item)
	if err != nil {
		return fmt.Errorf("rebuild canonical Self source hash %s: %w", item.ID, err)
	}
	if item.SourceHash != expectedHash {
		return fmt.Errorf("canonical Self entry %s source hash mismatch", item.ID)
	}
	expectedID := deterministicAuthorityID(
		"self-model",
		item.EntryKey+"\x00"+item.SourceHash,
	)
	if item.ID != expectedID {
		return fmt.Errorf("canonical Self entry %s identity mismatch", item.ID)
	}
	return nil
}

func scanSelfModelEntry(scanner interface{ Scan(dest ...any) error }) (SelfModelEntry, error) {
	var item SelfModelEntry
	var evidenceJSON, contradictionsJSON string
	if err := scanner.Scan(
		&item.ID,
		&item.EntryKey,
		&item.Revision,
		&item.Kind,
		&item.Scope,
		&item.Statement,
		&item.Status,
		&item.Confidence,
		&item.ReasoningStatus,
		&evidenceJSON,
		&contradictionsJSON,
		&item.SourceHash,
		&item.CreatedAt,
	); err != nil {
		return SelfModelEntry{}, err
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &item.Evidence); err != nil {
		return SelfModelEntry{}, fmt.Errorf("decode self model evidence %s: %w", item.ID, err)
	}
	if err := json.Unmarshal([]byte(contradictionsJSON), &item.Contradictions); err != nil {
		return SelfModelEntry{}, fmt.Errorf("decode self model contradictions %s: %w", item.ID, err)
	}
	if err := validateSelfModelEntryIntegrity(item); err != nil {
		return SelfModelEntry{}, err
	}
	return item, nil
}

const selfModelSelectColumns = `
	id, entry_key, revision, kind, scope, statement, status, confidence,
	reasoning_status, evidence_json, contradictions_json, source_hash, created_at`

func (s *ActivityStore) GetLatestSelfModelEntry(entryKey string) (SelfModelEntry, error) {
	return scanSelfModelEntry(s.db.QueryRow(`
		SELECT `+selfModelSelectColumns+`
		FROM self_model_entries
		WHERE entry_key = ?
		ORDER BY revision DESC LIMIT 1`,
		strings.TrimSpace(entryKey),
	))
}

func (s *ActivityStore) GetSelfModelEntryByID(
	entryID string,
) (SelfModelEntry, error) {
	return scanSelfModelEntry(s.db.QueryRow(`
		SELECT `+selfModelSelectColumns+`
		FROM self_model_entries
		WHERE id = ?`,
		strings.TrimSpace(entryID),
	))
}

func (s *ActivityStore) AddSelfModelEntry(item SelfModelEntry) (SelfModelEntry, bool, error) {
	if s == nil || s.db == nil {
		return SelfModelEntry{}, false, errors.New("activity store is unavailable")
	}
	var err error
	if item.Kind, err = normalizeSelfModelKind(item.Kind); err != nil {
		return SelfModelEntry{}, false, err
	}
	if item.Status, err = normalizeSelfModelStatus(item.Status); err != nil {
		return SelfModelEntry{}, false, err
	}
	item.Scope = strings.TrimSpace(item.Scope)
	item.Statement = strings.TrimSpace(item.Statement)
	item.ReasoningStatus = strings.TrimSpace(item.ReasoningStatus)
	item.Confidence = clampConfidence(item.Confidence)
	if item.Statement == "" || item.ReasoningStatus == "" {
		return SelfModelEntry{}, false, errors.New("self model statement and reasoning status are required")
	}
	if item.EntryKey == "" {
		item.EntryKey = selfModelEntryKey(item.Kind, item.Scope, item.Statement)
	}
	current, currentErr := s.GetLatestSelfModelEntry(item.EntryKey)
	if currentErr == nil && selfModelHasUserEdits(current) {
		// Background evidence ingestion cannot replace an explicit edit or
		// append outside a directive's lifecycle ledger. Reset and reviewed
		// replacements use their own validated transactions.
		return current, false, nil
	}
	if currentErr == nil {
		item.Evidence = mergeSelfModelEvidence(current.Evidence, item.Evidence)
		item.Contradictions = mergeSelfModelEvidence(current.Contradictions, item.Contradictions)
		item.Revision = current.Revision + 1
		if selfModelStatusPriority(current.Status) > selfModelStatusPriority(item.Status) {
			item.Status = current.Status
		}
		if current.Confidence > item.Confidence {
			item.Confidence = current.Confidence
		}
	} else if errors.Is(currentErr, sql.ErrNoRows) {
		item.Revision = 1
	} else {
		return SelfModelEntry{}, false, currentErr
	}
	if item.CreatedAt == 0 {
		item.CreatedAt = time.Now().UnixMilli()
	}
	item.SourceHash, err = buildSelfModelSourceHash(item)
	if err != nil {
		return SelfModelEntry{}, false, err
	}
	if currentErr == nil && current.SourceHash == item.SourceHash {
		return current, false, nil
	}
	item.ID = deterministicAuthorityID("self-model", item.EntryKey+"\x00"+item.SourceHash)
	payloadEvidence, err := json.Marshal(item.Evidence)
	if err != nil {
		return SelfModelEntry{}, false, err
	}
	payloadContradictions, err := json.Marshal(item.Contradictions)
	if err != nil {
		return SelfModelEntry{}, false, err
	}
	_, err = s.db.Exec(`
		INSERT OR IGNORE INTO self_model_entries (
			id, entry_key, revision, kind, scope, statement, status, confidence,
			reasoning_status, evidence_json, contradictions_json, source_hash,
			created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID,
		item.EntryKey,
		item.Revision,
		item.Kind,
		item.Scope,
		item.Statement,
		item.Status,
		item.Confidence,
		item.ReasoningStatus,
		string(payloadEvidence),
		string(payloadContradictions),
		item.SourceHash,
		item.CreatedAt,
	)
	if err != nil {
		return SelfModelEntry{}, false, fmt.Errorf("save self model entry: %w", err)
	}
	persisted, err := s.GetLatestSelfModelEntry(item.EntryKey)
	return persisted, persisted.SourceHash == item.SourceHash, err
}

func (s *ActivityStore) ListCurrentSelfModelEntries() ([]SelfModelEntry, error) {
	return s.listCurrentSelfModelEntriesContext(context.Background())
}

func (s *ActivityStore) listCurrentSelfModelEntriesContext(ctx context.Context) ([]SelfModelEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+selfModelSelectColumns+`
		FROM self_model_entries current
		WHERE current.revision = (
			SELECT MAX(candidate.revision)
			FROM self_model_entries candidate
			WHERE candidate.entry_key = current.entry_key
		)
		ORDER BY
			CASE current.kind
				WHEN 'boundary' THEN 0
				WHEN 'principle' THEN 1
				WHEN 'decision_policy' THEN 2
				ELSE 3
			END,
			current.confidence DESC,
			current.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list current self model entries: %w", err)
	}
	defer rows.Close()
	out := make([]SelfModelEntry, 0)
	for rows.Next() {
		entry, err := scanSelfModelEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan self model entry: %w", err)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate self model entries: %w", err)
	}
	return out, nil
}

// listUsableSelfModelEntries keeps immutable Self history inspectable while
// excluding projections whose learned evidence came from a repeated task
// onset. The repeated rows remain available through the raw/debug read model.
func (s *ActivityStore) listUsableSelfModelEntries() ([]SelfModelEntry, error) {
	return s.listUsableSelfModelEntriesContext(context.Background())
}

func (s *ActivityStore) listUsableSelfModelEntriesContext(ctx context.Context) ([]SelfModelEntry, error) {
	entries, err := s.listCurrentSelfModelEntriesContext(ctx)
	if err != nil {
		return nil, err
	}
	repeated, err := s.taskEpisodeRepeatedOnsetSetContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("load repeated task starts for Self: %w", err)
	}
	if len(repeated) == 0 {
		return entries, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT evaluation.id, run.task_episode_id
		FROM shadow_run_evaluations evaluation
		JOIN shadow_runs run ON run.id = evaluation.shadow_run_id
		WHERE run.task_episode_id <> ''`)
	if err != nil {
		return nil, fmt.Errorf("load shadow evaluation provenance for Self: %w", err)
	}
	repeatedEvaluationIDs := make(map[string]struct{})
	for rows.Next() {
		var evaluationID, episodeID string
		if err := rows.Scan(&evaluationID, &episodeID); err != nil {
			rows.Close()
			return nil, err
		}
		if _, excluded := repeated[episodeID]; excluded {
			repeatedEvaluationIDs[evaluationID] = struct{}{}
		}
	}
	rowsErr := rows.Err()
	if err := rows.Close(); err != nil && rowsErr == nil {
		rowsErr = err
	}
	if rowsErr != nil {
		return nil, rowsErr
	}
	if len(repeatedEvaluationIDs) == 0 {
		return entries, nil
	}

	usable := make([]SelfModelEntry, 0, len(entries))
	for _, entry := range entries {
		excluded := false
		allEvidence := append(
			append([]SelfModelEvidence(nil), entry.Evidence...),
			entry.Contradictions...,
		)
		for _, evidence := range allEvidence {
			if evidence.SourceKind != "shadow_task_evaluation" {
				continue
			}
			if _, repeated := repeatedEvaluationIDs[evidence.SourceID]; repeated {
				excluded = true
				break
			}
		}
		if !excluded {
			usable = append(usable, entry)
		}
	}
	return usable, nil
}

func (s *ActivityStore) ListSelfModelEntriesAt(
	cutoffAt int64,
) ([]SelfModelEntry, error) {
	if cutoffAt <= 0 {
		cutoffAt = time.Now().UnixMilli()
	}
	rows, err := s.db.Query(`
		SELECT `+selfModelSelectColumns+`
		FROM self_model_entries current
		WHERE current.created_at <= ?
			AND current.revision = (
			SELECT MAX(candidate.revision)
			FROM self_model_entries candidate
			WHERE candidate.entry_key = current.entry_key
				AND candidate.created_at <= ?
		)
		ORDER BY
			CASE current.kind
				WHEN 'boundary' THEN 0
				WHEN 'principle' THEN 1
				WHEN 'decision_policy' THEN 2
				ELSE 3
			END,
			current.confidence DESC,
			current.created_at DESC`,
		cutoffAt,
		cutoffAt,
	)
	if err != nil {
		return nil, fmt.Errorf("list current self model entries: %w", err)
	}
	defer rows.Close()
	out := make([]SelfModelEntry, 0)
	for rows.Next() {
		item, err := scanSelfModelEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (a *App) addSelfModelEntry(item SelfModelEntry) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return
	}
	persisted, added, err := a.activity.store.AddSelfModelEntry(item)
	if err != nil {
		log.Printf("[self-model] promotion failed: %v", err)
		return
	}
	if added {
		a.emitBrowserEvent("self_model:updated", persisted)
	}
}

func (a *App) promoteSelfModelFromQualification(
	qualification ShadowCapabilityQualification,
	userApproved bool,
) {
	if a == nil || a.activity == nil || a.activity.store == nil ||
		strings.TrimSpace(qualification.RepresentativeShadowRunID) == "" {
		return
	}
	run, err := a.activity.store.GetShadowRun(qualification.RepresentativeShadowRunID)
	if err != nil {
		return
	}
	action := strings.TrimSpace(run.Prediction.PredictedAction)
	if action != "" {
		status := selfModelStatusProvisional
		reasoningStatus := "behavior_derived"
		sourceKind := "shadow_qualification"
		if userApproved {
			status = selfModelStatusActive
			reasoningStatus = "behavior_and_explicit_approval"
			sourceKind = "initiative_approval"
		}
		a.addSelfModelEntry(SelfModelEntry{
			Kind:            selfModelKindPreference,
			Scope:           qualification.CapabilityKey,
			Statement:       action,
			Status:          status,
			Confidence:      qualification.Actual.ActionReliability,
			ReasoningStatus: reasoningStatus,
			Evidence: []SelfModelEvidence{{
				SourceKind: sourceKind,
				SourceID:   qualification.ID,
				Statement:  action,
				Confidence: qualification.Actual.ActionReliability,
				SourceRefs: append([]string(nil), qualification.RuntimeAttemptIDs...),
				OccurredAt: qualification.CreatedAt,
			}},
			CreatedAt: qualification.CreatedAt,
		})
	}
	a.promoteSourceBackedReasoning(qualification)
}

func (a *App) promoteSourceBackedReasoning(qualification ShadowCapabilityQualification) {
	profile, err := a.activity.store.GetShadowCapabilityProfileByEvidence(
		qualification.CapabilityKey,
		shadowCapabilityProfilePolicyVersion,
		qualification.ProfileEvidenceHash,
	)
	if err != nil {
		return
	}
	type group struct {
		kind       string
		statement  string
		evidence   []SelfModelEvidence
		sourceRuns map[string]struct{}
		confidence float64
	}
	groups := make(map[string]*group)
	seenRuns := make(map[string]struct{})
	for _, profileEvidence := range profile.Evidence {
		if !profileEvidence.Qualified {
			continue
		}
		if _, ok := seenRuns[profileEvidence.ShadowRunID]; ok {
			continue
		}
		seenRuns[profileEvidence.ShadowRunID] = struct{}{}
		observations, err := a.activity.store.ListShadowRunObservations(profileEvidence.ShadowRunID)
		if err != nil {
			continue
		}
		for _, observation := range observations {
			for _, evidence := range observation.ReasoningEvidence {
				key := strings.ToLower(strings.Join(strings.Fields(evidence.Statement), " "))
				if key == "" {
					continue
				}
				key = evidence.Kind + "\x00" + key
				current := groups[key]
				if current == nil {
					current = &group{
						kind:       evidence.Kind,
						statement:  evidence.Statement,
						sourceRuns: make(map[string]struct{}),
					}
					groups[key] = current
				}
				current.sourceRuns[profileEvidence.ShadowRunID] = struct{}{}
				current.confidence += evidence.Confidence
				current.evidence = append(current.evidence, SelfModelEvidence{
					SourceKind: "shadow_reasoning",
					SourceID:   observation.ID,
					Statement:  evidence.Statement,
					Confidence: evidence.Confidence,
					SourceRefs: append([]string(nil), evidence.SourceRefs...),
					OccurredAt: observation.ObservedAt,
				})
			}
		}
	}
	for _, current := range groups {
		count := len(current.sourceRuns)
		if count == 0 {
			continue
		}
		confidence := clampConfidence(current.confidence / float64(len(current.evidence)))
		kind := ""
		minSources := 3
		switch current.kind {
		case "repeated_choice":
			kind = selfModelKindPreference
		case "stated_rationale", "correction":
			kind = selfModelKindDecisionPolicy
		case "contextual_constraint":
			kind = selfModelKindBoundary
			minSources = 2
		}
		if kind == "" || count < minSources || confidence < 0.70 {
			continue
		}
		a.addSelfModelEntry(SelfModelEntry{
			Kind:            kind,
			Scope:           qualification.CapabilityKey,
			Statement:       strings.TrimSpace(current.statement),
			Status:          selfModelStatusActive,
			Confidence:      confidence,
			ReasoningStatus: "source_backed",
			Evidence:        current.evidence,
			CreatedAt:       qualification.CreatedAt,
		})
	}
}

func selfModelWords(value string) map[string]struct{} {
	words := make(map[string]struct{})
	for _, field := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if len(field) >= 3 {
			words[field] = struct{}{}
		}
	}
	return words
}

func selfModelRelevance(entry SelfModelEntry, queryWords map[string]struct{}) int {
	entryWords := selfModelWords(entry.Scope + " " + entry.Statement)
	score := 0
	for word := range queryWords {
		if _, ok := entryWords[word]; ok {
			score++
		}
	}
	return score
}

func selectSelfModelContextEntries(
	entries []SelfModelEntry,
	query string,
	maxChars int,
) []SelfModelEntry {
	queryWords := selfModelWords(query)
	type scored struct {
		entry SelfModelEntry
		score int
	}
	candidates := make([]scored, 0, len(entries))
	for _, entry := range entries {
		if entry.Status == selfModelStatusRetracted {
			continue
		}
		score := selfModelRelevance(entry, queryWords)
		alwaysInclude := entry.Status == selfModelStatusActive &&
			(entry.Kind == selfModelKindBoundary || entry.Kind == selfModelKindPrinciple)
		if !alwaysInclude && len(queryWords) > 0 && score == 0 {
			continue
		}
		candidates = append(candidates, scored{entry: entry, score: score})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftBoundary := candidates[i].entry.Kind == selfModelKindBoundary
		rightBoundary := candidates[j].entry.Kind == selfModelKindBoundary
		if leftBoundary != rightBoundary {
			return leftBoundary
		}
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].entry.Confidence > candidates[j].entry.Confidence
	})
	selected := make([]SelfModelEntry, 0, len(candidates))
	used := 0
	for _, candidate := range candidates {
		cost := len(candidate.entry.Kind) + len(candidate.entry.Scope) +
			len(candidate.entry.Statement) + 64
		if used+cost > maxChars {
			break
		}
		selected = append(selected, candidate.entry)
		used += cost
	}
	return selected
}

func (a *App) selfModelContext(query string) string {
	if a == nil || a.IsClosedEyesModeEnabled() || a.activity == nil || a.activity.store == nil {
		return ""
	}
	entries, err := a.activity.store.listUsableSelfModelEntries()
	if err != nil {
		return ""
	}
	readable := entries[:0]
	for _, entry := range entries {
		if a.selfModelEntryAllowsModelRead(entry) {
			readable = append(readable, entry)
		}
	}
	selected := selectSelfModelContextEntries(readable, query, selfModelContextBudgetChars)
	if len(selected) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<self-model-context>\n")
	b.WriteString("Typed, evidence-backed user model. Provisional entries are hypotheses. These entries guide context but never grant execution authority.\n\n")
	for _, entry := range selected {
		fmt.Fprintf(
			&b,
			"- [%s/%s, confidence=%.2f, reasoning=%s] %s",
			entry.Kind,
			entry.Status,
			entry.Confidence,
			entry.ReasoningStatus,
			entry.Statement,
		)
		if entry.Scope != "" {
			fmt.Fprintf(&b, "\n  Scope: %s", entry.Scope)
		}
		fmt.Fprintf(&b, "\n  Evidence count: %d\n", len(entry.Evidence))
	}
	b.WriteString("</self-model-context>\n\n")
	return b.String()
}

func (a *App) ListSelfModelEntries() ([]SelfModelEntry, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return []SelfModelEntry{}, nil
	}
	return a.activity.store.listUsableSelfModelEntries()
}

func (a *App) GetSelfModelContextPreview(query string) string {
	return a.selfModelContext(query)
}
