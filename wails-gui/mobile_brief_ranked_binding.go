package main

import (
	"context"
	"time"
)

type morningBriefSourceDependency struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// A ranked input is local-only. Unlike a citation, it records even candidates
// omitted by the scorer, since their contents can still influence the prose.
type morningBriefBoundInput struct {
	Kind         string                         `json:"kind"`
	ID           string                         `json:"id"`
	Digest       string                         `json:"digest"`
	OriginDigest string                         `json:"originDigest,omitempty"`
	Dependencies []morningBriefSourceDependency `json:"dependencies,omitempty"`
	Candidate    morningBriefCandidate          `json:"candidate"`
}

// morningBriefBindRankedInputs admits only sources with an exact source reader.
// Prior-brief facts and other unbound evidence are withheld until they can be
// proven independently. Related Purpose next steps carry their own sources.
func (a *App) morningBriefBindRankedInputs(ctx context.Context, cutoff time.Time, candidates []morningBriefCandidate) []morningBriefBoundInput {
	if a == nil || a.IsClosedEyesModeEnabled() || ctx.Err() != nil || cutoff.IsZero() {
		return nil
	}
	subjects, err := mobileBriefMailSubjects(a)
	if err != nil {
		return nil
	}
	var sources todayChatSources
	for _, c := range candidates {
		if c.SourceKind == "purpose" || morningBriefObservedSourceKind(c.SourceKind) || len(c.Dependencies) > 0 {
			sources = a.collectTodayChatSources(ctx, time.Now())
			break
		}
	}
	var result []morningBriefBoundInput
	for _, c := range candidates {
		if len(c.PriorBriefs) != 0 || c.ChangeKnown || c.Changed || c.Change != "" || c.Unbound || c.SourceDigest == "" || c.NextStep != "" && (c.SourceKind != "purpose" || len(c.Dependencies) == 0) {
			continue
		}
		item := morningBriefBoundInput{Kind: c.SourceKind, ID: c.SourceID, Digest: c.SourceDigest, OriginDigest: c.SourceOrigin, Dependencies: append([]morningBriefSourceDependency(nil), c.Dependencies...), Candidate: c}
		if a.morningBriefRankedInputsReadableFrom(ctx, cutoff, []morningBriefBoundInput{item}, subjects, sources) {
			result = append(result, item)
		}
	}
	return result
}

// Recheck the original source and mail holdback, including for uncited inputs.
// A changed source withdraws all prose that was influenced by the batch.
func (a *App) morningBriefRankedInputsReadable(ctx context.Context, cutoff time.Time, inputs []morningBriefBoundInput) bool {
	if a == nil || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) || ctx.Err() != nil || cutoff.IsZero() {
		return false
	}
	subjects, err := mobileBriefMailSubjects(a)
	if err != nil {
		return false
	}
	var sources todayChatSources
	for _, item := range inputs {
		if item.Kind == "purpose" || morningBriefObservedSourceKind(item.Kind) || len(item.Dependencies) > 0 {
			sources = a.collectTodayChatSources(ctx, time.Now())
			break
		}
	}
	return a.morningBriefRankedInputsReadableFrom(ctx, cutoff, inputs, subjects, sources)
}

func (a *App) morningBriefRankedInputsReadableFrom(ctx context.Context, cutoff time.Time, inputs []morningBriefBoundInput, subjects []string, sources todayChatSources) bool {
	return a.morningBriefRankedInputsReadableFromWithLedger(ctx, cutoff, inputs, subjects, sources, false)
}

func (a *App) morningBriefRankedInputsReadableFromWithLedger(ctx context.Context, cutoff time.Time, inputs []morningBriefBoundInput, subjects []string, sources todayChatSources, allowLedger bool) bool {
	seen := map[string]bool{}
	for _, item := range inputs {
		key := mobileBriefSourceKey(item.Kind, item.ID)
		if item.ID == "" || item.Digest == "" || seen[key] || item.Candidate.ID != mobileBriefItemKey("candidate", item.Kind, item.ID) ||
			len(item.Candidate.PriorBriefs) != 0 || item.Candidate.Unbound ||
			!allowLedger && (item.Candidate.ChangeKnown || item.Candidate.Changed || item.Candidate.Change != "") ||
			item.Candidate.NextStep != "" && (item.Kind != "purpose" || len(item.Dependencies) == 0) ||
			!morningBriefCandidateSafe(item.Candidate) || mobileBriefValueEchoesMail(item.Candidate, subjects) {
			return false
		}
		seen[key] = true
		var readable bool
		switch item.Kind {
		case "purpose":
			readable = a.morningBriefPurposeSourceReadable(ctx, sources, item.ID, cutoff, item.Digest)
		case mobileBriefCalendarSource:
			readable = a.morningBriefCalendarSourceReadable(ctx, item.ID, item.Digest)
		case mobileBriefWaitingReplySource:
			readable = a.morningBriefWaitingSourceReadable(ctx, item.ID, item.Digest, cutoff)
		case "history":
			readable = a.morningBriefHandoffSourceReadable(ctx, item.ID, item.Digest)
		case mobileBriefRecapSource, mobileBriefFileSource, "memory", "knowledge", "principal":
			if item.Kind == mobileBriefRecapSource {
				if checked, ok := ctx.Value(mobileBriefSummaryReplacementKey{}).(mobileBriefSummaryProjectionSources); ok && checked.ids[item.ID] {
					readable = true
					break
				}
			}
			ref := MobileBriefRef{SourceKind: item.Kind, SourceID: item.ID}
			brief := MobileBriefV1{Slot: mobileBriefSlotMorning, AsOf: formatMobileBriefTime(cutoff)}
			digest, ok := a.mobileBriefModelSourceDigest(ctx, ref, brief, sources)
			readable = ok && digest == item.Digest
		default:
			return false
		}
		if !readable {
			return false
		}
		for _, dependency := range item.Dependencies {
			if dependency.Kind != "purpose" || !a.morningBriefPurposeSourceReadable(ctx, sources, dependency.ID, cutoff, dependency.Digest) {
				return false
			}
		}
	}
	return ctx.Err() == nil
}

// The batch carries only sanitized text. A sensitive value is omitted rather
// than silently changing the fact attached to its source fingerprint.
func morningBriefCandidateSafe(candidate morningBriefCandidate) bool {
	switch candidate.DecisionKind {
	case "", "invitation_response", "work_review":
	default:
		return false
	}
	// IDs and timestamps are generated by the app and validated separately;
	// the secret redactor would otherwise mistake their digits for codes.
	for _, timestamp := range []string{candidate.LastTouch, candidate.Due} {
		if timestamp != "" {
			if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
				return false
			}
		}
	}
	fields := []string{candidate.Title, candidate.Status, candidate.NextStep,
		candidate.Counterparty, candidate.Direction, candidate.Serves, candidate.Change}
	fields = append(fields, candidate.Attendees...)
	fields = append(fields, candidate.PriorBriefs...)
	for _, field := range fields {
		if !mobileBriefCopySafe(field) || redactThreadOutcomeText(field) != field {
			return false
		}
	}
	return true
}

// morningBriefBindRankedLedger adds only changes derived from rechecked local
// ledger facts to an already admitted batch. The old wording remains local.
func (a *App) morningBriefBindRankedLedger(ctx context.Context, brief MobileBriefV1, admitted []morningBriefBoundInput, facts []morningBriefLedgerFact, store *mobileBriefStore) ([]morningBriefBoundInput, []morningBriefLedgerProof, bool) {
	cutoff := mobileBriefAsOfTime(brief)
	if !a.morningBriefRankedInputsReadable(ctx, cutoff, admitted) {
		return nil, nil, false
	}
	candidates := make([]morningBriefCandidate, len(admitted))
	for i, input := range admitted {
		candidates[i] = input.Candidate
		candidates[i].SourceKind, candidates[i].SourceID = input.Kind, input.ID
		candidates[i].SourceDigest, candidates[i].SourceOrigin = input.Digest, input.OriginDigest
		candidates[i].Dependencies = append([]morningBriefSourceDependency(nil), input.Dependencies...)
	}
	rows, proofs := morningBriefApplyLedger(candidates, facts, cutoff, mobileBriefNarrativeLocation(brief))
	out := append([]morningBriefBoundInput(nil), admitted...)
	for i := range out {
		out[i].Candidate = rows[i]
	}
	if !a.morningBriefRankedInputsReadableWithLedger(ctx, brief, out, store, proofs) {
		return nil, nil, false
	}
	return out, proofs, true
}

func (a *App) morningBriefRankedInputsReadableWithLedger(ctx context.Context, brief MobileBriefV1, inputs []morningBriefBoundInput, store *mobileBriefStore, proofs []morningBriefLedgerProof) bool {
	cutoff := mobileBriefAsOfTime(brief)
	if a == nil || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) || ctx.Err() != nil || cutoff.IsZero() || !a.morningBriefRankedLedgerMatches(ctx, brief, inputs, store, proofs) {
		return false
	}
	subjects, err := mobileBriefMailSubjects(a)
	if err != nil {
		return false
	}
	var sources todayChatSources
	for _, input := range inputs {
		if input.Kind == "purpose" || morningBriefObservedSourceKind(input.Kind) || len(input.Dependencies) > 0 {
			sources = a.collectTodayChatSources(ctx, time.Now())
			break
		}
	}
	return a.morningBriefRankedInputsReadableFromWithLedger(ctx, cutoff, inputs, subjects, sources, len(proofs) > 0)
}

// morningBriefRankedSourceKind lists the sources
// morningBriefRankedInputsReadableFromWithLedger can read again.
func morningBriefRankedSourceKind(kind string) bool {
	switch kind {
	case "purpose", mobileBriefCalendarSource, mobileBriefWaitingReplySource, "history", mobileBriefRecapSource, mobileBriefFileSource, "memory", "knowledge", "principal":
		return true
	}
	return false
}

func morningBriefObservedSourceKind(kind string) bool {
	switch kind {
	case mobileBriefRecapSource, mobileBriefFileSource, "memory", "knowledge", "principal":
		return true
	default:
		return false
	}
}
