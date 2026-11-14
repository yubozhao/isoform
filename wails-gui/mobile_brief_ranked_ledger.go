package main

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// A historical proof cannot be recovered by waiting for a store to open if
// its referenced brief has already left an accessible store. Treat only a
// definite missing file as a withdrawal; other I/O failures remain transient.
func morningBriefMissingLedgerProof(store *mobileBriefStore, proofs []morningBriefLedgerProof) bool {
	if store == nil || store.dir == "" {
		return false
	}
	if info, err := os.Stat(store.dir); err != nil || !info.IsDir() {
		return false
	}
	for _, proof := range proofs {
		if !mobileBriefIDPattern.MatchString(proof.BriefID) {
			continue
		}
		if _, err := os.Stat(filepath.Join(store.dir, proof.BriefID+".json")); os.IsNotExist(err) {
			return true
		}
	}
	return false
}

// A historical fact stays inside code. The model receives only the resulting
// change flag, never the previous title or other previous source text.
type morningBriefLedgerFact struct {
	at        time.Time
	kind      string
	id        string
	title     string
	status    string
	due       string
	lastTouch string
	proof     morningBriefLedgerProof
}

// A proof is local-only and can recheck a comparison without copying old text
// into the new brief's binding.
type morningBriefLedgerProof struct {
	BriefID        string `json:"briefId"`
	CandidateID    string `json:"candidateId"`
	SourceKind     string `json:"sourceKind"`
	SourceID       string `json:"sourceId"`
	OriginDigest   string `json:"originDigest"`
	ManifestDigest string `json:"manifestDigest"`
	Synthesis      string `json:"synthesis"`
}

// morningBriefLedgerInputReadable checks the original source identity and
// today's policy, while allowing the source's title or status to have changed.
// It does not authorize sending the old candidate text to a model.
func (a *App) morningBriefLedgerInputReadable(ctx context.Context, input morningBriefBoundInput, subjects []string) bool {
	if a == nil || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) || ctx.Err() != nil || input.OriginDigest == "" || input.ID == "" ||
		input.Candidate.ID != mobileBriefItemKey("candidate", input.Kind, input.ID) || !morningBriefCandidateSafe(input.Candidate) || mobileBriefValueEchoesMail(input.Candidate, subjects) {
		return false
	}
	switch input.Kind {
	case "history":
		return a.morningBriefHandoffOriginReadable(ctx, input.ID, input.OriginDigest)
	case mobileBriefCalendarSource:
		return a.morningBriefCalendarOriginReadable(ctx, input.ID, input.OriginDigest)
	default:
		return false
	}
}

// morningBriefApplyLedger compares only yesterday's latest frozen fact for the
// same source. Call it on candidates gathered without unbound prior briefs;
// the returned proofs must be bound and rechecked before any model call.
func morningBriefApplyLedger(candidates []morningBriefCandidate, facts []morningBriefLedgerFact, now time.Time, loc *time.Location) ([]morningBriefCandidate, []morningBriefLedgerProof) {
	if loc == nil {
		loc = time.Local
	}
	yesterday := now.In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	latest := map[string]morningBriefLedgerFact{}
	for _, fact := range facts {
		if fact.at.In(loc).Format("2006-01-02") != yesterday {
			continue
		}
		key := mobileBriefSourceKey(fact.kind, fact.id)
		if prior, ok := latest[key]; !ok || fact.at.After(prior.at) {
			latest[key] = fact
		}
	}
	out := append([]morningBriefCandidate(nil), candidates...)
	var proofs []morningBriefLedgerProof
	for i := range out {
		item := &out[i]
		if item.ChangeKnown || len(item.PriorBriefs) != 0 {
			continue
		}
		prior, ok := latest[mobileBriefSourceKey(item.SourceKind, item.SourceID)]
		if !ok {
			continue
		}
		item.ChangeKnown = true
		item.Changed = item.Title != prior.title || item.Status != prior.status || item.Due != prior.due || item.LastTouch != prior.lastTouch
		if item.Changed {
			item.Change = "source changed"
		}
		proofs = append(proofs, prior.proof)
	}
	return out, proofs
}

func (a *App) morningBriefLedgerProofFacts(ctx context.Context, store *mobileBriefStore, cutoff time.Time, proofs []morningBriefLedgerProof) ([]morningBriefLedgerFact, bool) {
	if a == nil || store == nil || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) || ctx.Err() != nil || cutoff.IsZero() {
		return nil, false
	}
	subjects, err := mobileBriefMailSubjects(a)
	if err != nil {
		return nil, false
	}
	allowed := morningBriefLastThreeIDs(store, cutoff)
	seen := map[string]bool{}
	var facts []morningBriefLedgerFact
	for _, proof := range proofs {
		key := proof.BriefID + "\x00" + proof.CandidateID
		if proof.BriefID == "" || !allowed[proof.BriefID] || proof.CandidateID == "" || seen[key] || proof.ManifestDigest == "" || proof.Synthesis == "" {
			return nil, false
		}
		seen[key] = true
		brief, found := store.load(proof.BriefID)
		binding := brief.modelBinding
		at := mobileBriefAsOfTime(brief)
		if !found || !brief.rankedEdition || brief.NarrativeStatus != "ok" || at.IsZero() || !at.Before(cutoff) || binding == nil || binding.Version != 2 ||
			binding.RankedInputsDigest != proof.ManifestDigest || !mobileBriefRankedManifestValid(brief, binding) ||
			binding.Synthesis != proof.Synthesis || mobileBriefSynthesisDigest(brief) != proof.Synthesis {
			return nil, false
		}
		matched := false
		for _, input := range binding.RankedInputs {
			if input.Candidate.ID == proof.CandidateID && input.Kind == proof.SourceKind && input.ID == proof.SourceID && input.OriginDigest == proof.OriginDigest &&
				a.morningBriefLedgerInputReadable(ctx, input, subjects) {
				facts = append(facts, morningBriefLedgerFact{at: at, kind: input.Kind, id: input.ID, title: input.Candidate.Title,
					status: input.Candidate.Status, due: input.Candidate.Due, lastTouch: input.Candidate.LastTouch, proof: proof})
				matched = true
				break
			}
		}
		if !matched {
			return nil, false
		}
	}
	return facts, ctx.Err() == nil
}

// morningBriefRankedLedgerMatches recomputes every change from the exact
// historical proofs. It rejects both fabricated changes and unused proofs.
func (a *App) morningBriefRankedLedgerMatches(ctx context.Context, brief MobileBriefV1, inputs []morningBriefBoundInput, store *mobileBriefStore, proofs []morningBriefLedgerProof) bool {
	cutoff := mobileBriefAsOfTime(brief)
	if cutoff.IsZero() || ctx.Err() != nil {
		return false
	}
	var facts []morningBriefLedgerFact
	if len(proofs) > 0 {
		var ok bool
		facts, ok = a.morningBriefLedgerProofFacts(ctx, store, cutoff, proofs)
		if !ok {
			return false
		}
	}
	candidates := make([]morningBriefCandidate, len(inputs))
	for i, input := range inputs {
		candidate := input.Candidate
		if len(candidate.PriorBriefs) != 0 {
			return false
		}
		candidate.SourceKind, candidate.SourceID = input.Kind, input.ID
		candidate.ChangeKnown, candidate.Changed, candidate.Change = false, false, ""
		candidates[i] = candidate
	}
	expected, used := morningBriefApplyLedger(candidates, facts, cutoff, mobileBriefNarrativeLocation(brief))
	if len(used) != len(proofs) {
		return false
	}
	proofSet := make(map[morningBriefLedgerProof]bool, len(proofs))
	for _, proof := range proofs {
		if proofSet[proof] {
			return false
		}
		proofSet[proof] = true
	}
	for _, proof := range used {
		if !proofSet[proof] {
			return false
		}
	}
	for i, item := range inputs {
		if item.Candidate.ChangeKnown != expected[i].ChangeKnown || item.Candidate.Changed != expected[i].Changed || item.Candidate.Change != expected[i].Change {
			return false
		}
	}
	return ctx.Err() == nil
}

// The service owns the store used for prior proof reads. If it is unavailable,
// the proof is temporarily unreadable rather than recreated from another path.
func (a *App) morningBriefLedgerStore() *mobileBriefStore {
	if a == nil {
		return nil
	}
	service, ok := a.mobileBriefSource().(*mobileBriefService)
	if !ok || service == nil {
		return nil
	}
	return service.store
}

// A missing or unprovable recent brief is not replaced by an older one.
func morningBriefLastThreeIDs(store *mobileBriefStore, cutoff time.Time) map[string]bool {
	allowed := map[string]bool{}
	if store == nil || cutoff.IsZero() {
		return allowed
	}
	for _, brief := range store.briefs() {
		at := mobileBriefAsOfTime(brief)
		if at.IsZero() || !at.Before(cutoff) {
			continue
		}
		allowed[brief.BriefID] = true
		if len(allowed) == 3 {
			break
		}
	}
	return allowed
}
