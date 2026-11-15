package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// selfDreamUpdatesLimit caps how many proposals Settings lists at once.
const selfDreamUpdatesLimit = 50

// SelfDreamUpdate is one Self Dream proposal waiting for the user's decision.
// Settings > Personal context lists them and ReviewSelfDreamOperation records
// the decision. Before holds the current statements the proposal changes;
// After holds the statements it proposes.
type SelfDreamUpdate struct {
	ID            string         `json:"id"`
	RunID         string         `json:"runId"`
	Operation     string         `json:"operation"`
	Before        []string       `json:"before"`
	After         []string       `json:"after"`
	Scope         SelfDreamScope `json:"scope"`
	EvidenceCount int            `json:"evidenceCount"`
	Uncertainty   string         `json:"uncertainty,omitempty"`
	CreatedAt     int64          `json:"createdAt"`
}

// selfDreamUpdateSummary is the one-line product summary of a Self Dream
// operation. It reads only the number of targets from before, so callers may
// pass placeholders.
func selfDreamUpdateSummary(
	operation SelfDreamOperationRecord,
	before, after []string,
) string {
	switch operation.Operation {
	case SelfDreamOperationMerge:
		return fmt.Sprintf(
			"Isoform found %d entries that may describe the same supported understanding.",
			len(before),
		)
	case SelfDreamOperationSplit:
		return "One entry may contain distinct ideas that are clearer separately."
	case SelfDreamOperationContextualize:
		return "This understanding appears supported only in a specific context."
	case SelfDreamOperationContradict:
		return "Newer explicit evidence conflicts with this understanding."
	case SelfDreamOperationSupersede:
		return "The evidence supports a newer, more complete understanding."
	default:
		if len(after) > 0 {
			return after[0]
		}
		return "Isoform found a possible Self revision."
	}
}

// selfDreamAwaitsReview reports whether a persisted operation waits for the
// user. No-change and rejected proposals never do. Auto-eligible merges are
// the ones the rollout gate may promote without review.
func selfDreamAwaitsReview(op SelfDreamOperationRecord) bool {
	if op.ID == "" || op.Operation == SelfDreamOperationNoChange || op.AutoEligible {
		return false
	}
	switch op.ValidationStatus {
	case SelfDreamValidationQualified, SelfDreamValidationReviewRequired:
		return true
	default:
		return false
	}
}

// selfDreamProposedStatements is what an operation proposes in place of its
// targets.
func selfDreamProposedStatements(op SelfDreamOperationRecord) []string {
	if op.Operation == SelfDreamOperationSplit {
		return append([]string(nil), op.SplitStatements...)
	}
	return []string{op.Statement}
}

// selfDreamProposalKey identifies the same proposal across runs: the same
// change to the same canonical Self revisions. Evidence refs, the model's
// rationale, uncertainty and confidence are left out: refs name per-run
// snapshots (a work profile ref carries its snapshot id) and the model cites a
// different subset each run. New evidence that reaches Self appends a target
// revision, which changes the targets and so makes a new proposal.
func selfDreamProposalKey(op SelfDreamOperationRecord) string {
	statements := make([]string, 0, len(op.SplitStatements)+1)
	for _, statement := range selfDreamProposedStatements(op) {
		statements = append(statements, selfDreamCanonicalStatement(statement))
	}
	sort.Strings(statements)
	payload, _ := json.Marshal(struct {
		Operation  string   `json:"operation"`
		SelfKind   string   `json:"selfKind"`
		Scope      string   `json:"scope"`
		Targets    []string `json:"targets"`
		Statements []string `json:"statements"`
	}{
		Operation:  op.Operation,
		SelfKind:   op.SelfKind,
		Scope:      selfDreamStoredScope(op.Scope),
		Targets:    uniqueSortedStrings(op.TargetEntryIDs),
		Statements: statements,
	})
	return sha256Hex(payload)
}

// selfDreamReviewQueue decides which proposals still wait for the user. The
// Settings card and the learned_to_confirm notification both read it, so they
// apply the same rules.
type selfDreamReviewQueue struct {
	// waiting holds the operations that await review and have no answer of
	// their own, oldest first.
	waiting []SelfDreamOperationRecord
	// answered holds the proposal keys that have an answer.
	answered    map[string]struct{}
	currentByID map[string]SelfModelEntry
}

func (s *ActivityStore) loadSelfDreamReviewQueue() (selfDreamReviewQueue, error) {
	if s == nil || s.db == nil {
		return selfDreamReviewQueue{}, errors.New("activity store is unavailable")
	}
	answeredIDs, err := s.selfDreamAnsweredOperationIDs()
	if err != nil {
		return selfDreamReviewQueue{}, err
	}
	current, err := s.ListCurrentSelfModelEntries()
	if err != nil {
		return selfDreamReviewQueue{}, err
	}
	queue := selfDreamReviewQueue{
		waiting:     make([]SelfDreamOperationRecord, 0),
		answered:    make(map[string]struct{}),
		currentByID: selfDreamCurrentEntryByID(current),
	}
	rows, err := s.db.Query(`
		SELECT ` + selfDreamOperationColumns + `
		FROM self_dream_operations_v1
		WHERE validation_status IN ('qualified', 'review_required')
		ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return selfDreamReviewQueue{}, err
	}
	defer rows.Close()
	for rows.Next() {
		op, err := scanSelfDreamOperation(rows)
		if err != nil {
			return selfDreamReviewQueue{}, err
		}
		if !selfDreamAwaitsReview(op) {
			continue
		}
		if _, ok := answeredIDs[op.ID]; ok {
			queue.answered[selfDreamProposalKey(op)] = struct{}{}
			continue
		}
		queue.waiting = append(queue.waiting, op)
	}
	return queue, rows.Err()
}

// open reports whether a proposal still waits for the user: no copy of it has
// an answer and every target is still the current, active revision
// (compilation would refuse a stale one; a later run reconsiders). It also
// returns the targets' current statements.
func (q selfDreamReviewQueue) open(op SelfDreamOperationRecord) ([]string, bool) {
	if _, ok := q.answered[selfDreamProposalKey(op)]; ok {
		return nil, false
	}
	return selfDreamCurrentTargetStatements(op, q.currentByID)
}

// ListPendingSelfDreamUpdates returns the proposals that still wait for the
// user, newest first. A proposal repeated by several runs is listed once, as
// the first run's operation, so its id stays the same while it waits.
func (s *ActivityStore) ListPendingSelfDreamUpdates(
	limit int,
) ([]SelfDreamUpdate, error) {
	if limit <= 0 || limit > selfDreamUpdatesLimit {
		limit = selfDreamUpdatesLimit
	}
	queue, err := s.loadSelfDreamReviewQueue()
	if err != nil {
		return nil, err
	}
	out := make([]SelfDreamUpdate, 0)
	listed := make(map[string]struct{})
	for _, op := range queue.waiting {
		key := selfDreamProposalKey(op)
		if _, ok := listed[key]; ok {
			continue
		}
		before, open := queue.open(op)
		if !open {
			continue
		}
		listed[key] = struct{}{}
		out = append(out, SelfDreamUpdate{
			ID:            op.ID,
			RunID:         op.RunID,
			Operation:     op.Operation,
			Before:        before,
			After:         selfDreamProposedStatements(op),
			Scope:         op.Scope,
			EvidenceCount: len(op.EvidenceRefs),
			Uncertainty:   op.Uncertainty,
			CreatedAt:     op.CreatedAt,
		})
	}
	// Oldest first so each repeat keeps its first operation; return newest
	// first, up to the limit.
	slices.Reverse(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// selfDreamCurrentTargetStatements returns the statements of an operation's
// targets and whether every target is still the current, active revision.
func selfDreamCurrentTargetStatements(
	op SelfDreamOperationRecord,
	currentByID map[string]SelfModelEntry,
) ([]string, bool) {
	before := make([]string, 0, len(op.TargetEntryIDs))
	for _, targetID := range op.TargetEntryIDs {
		target, ok := currentByID[targetID]
		if !ok || target.Status != selfModelStatusActive {
			return nil, false
		}
		before = append(before, target.Statement)
	}
	return before, true
}

// selfDreamAnsweredOperationIDs returns the operations the user answered: kept
// as is, waiting for more evidence, or applied. The decision row is written
// before compilation, so an accept or correction whose compilation failed has
// no compilation row and its proposal stays open for another try.
func (s *ActivityStore) selfDreamAnsweredOperationIDs() (
	map[string]struct{},
	error,
) {
	rows, err := s.db.Query(`
		SELECT operation_id
		FROM self_dream_decisions_v1
		WHERE decision_kind IN (?, ?)
		UNION
		SELECT operation_id
		FROM self_dream_compilations_v1`,
		SelfDreamDecisionKeepSeparate,
		SelfDreamDecisionNotEnoughEvidence,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[strings.TrimSpace(id)] = struct{}{}
	}
	return out, rows.Err()
}

// ListSelfDreamUpdates lists the Self Dream proposals waiting for the user's
// decision, newest first.
func (a *App) ListSelfDreamUpdates() ([]SelfDreamUpdate, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return nil, errors.New("activity store is unavailable")
	}
	return a.activity.store.ListPendingSelfDreamUpdates(selfDreamUpdatesLimit)
}
