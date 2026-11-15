package main

import (
	"sort"
	"strings"
	"unicode"
)

// eventCandidate is the gating-time view of an event (extracted or persisted),
// decoupled from storage so the coarse filter stays a pure, testable function.
type eventCandidate struct {
	Apps     []string
	Entities []Entity
	StartMs  int64
	EndMs    int64
}

// subjectStopwords are generic words stripped before fuzzy topic/project
// matching, so a shared SPECIFIC name ("DJI Pocket 2") links events while a
// shared GENERIC word ("camera") does not over-connect unrelated activity.
var subjectStopwords = map[string]bool{
	"camera": true, "review": true, "video": true, "gear": true, "app": true,
	"page": true, "tool": true, "tab": true, "tabs": true, "new": true,
	"chat": true, "settings": true, "activity": true, "content": true,
	"research": true, "browse": true, "view": true, "watch": true, "read": true,
	"search": true, "the": true, "and": true, "for": true, "with": true,
}

// nearMergeWindowMs is the time-adjacency window for the relaxed gate: two events
// whose spans sit within this gap may become candidates on a SINGLE shared
// subject token (instead of the usual >=2). A single specific token is too weak to
// link on its own — it would chain every "travel"/"coding" event of the day — but
// combined with temporal adjacency it is strong enough to spend one LLM confirm on.
const nearMergeWindowMs = 10 * 60 * 1000

// gateMerge is the pure-function COARSE filter for stitch candidacy: do these two
// events share a strong entity (repo/pr/project/person) or a fuzzy subject
// (topic/project names with >=2 specific tokens)? It does NOT decide sameness —
// the day boundary and the final "same specific piece of work" judgement live in
// the stitch layer (day cutoff + LLM confirmation). An empty result means "not
// even a candidate", so no LLM call is spent on it.
func gateMerge(a, b eventCandidate) (candidate bool, shared []Entity) {
	if strong := strongEntityIntersection(a.Entities, b.Entities); len(strong) > 0 {
		return true, strong
	}
	if sub := fuzzySubjectMatch(a.Entities, b.Entities); len(sub) > 0 {
		return true, sub
	}
	// Time-adjacent relaxation: a single shared specific token plus temporal
	// adjacency is enough to ASK the LLM (it still decides sameness). This catches
	// the same subject named at different abstraction levels across apps minutes
	// apart — e.g. "Exampleton travel" in a browser vs "Halcyon travel" in chat, which
	// share only "travel" and so miss the >=2-token rule above. Requiring a shared
	// token keeps unrelated same-category neighbours (coffee vs travel) out.
	if eventsWithinMs(a, b, nearMergeWindowMs) {
		if shared, nonNumeric := sharedSubjectTokens(a.Entities, b.Entities); shared >= 1 && nonNumeric {
			return true, subjectEntities(a.Entities)
		}
	}
	return false, nil
}

// eventsWithinMs reports whether two events' time spans are within gapMs of each
// other (overlapping spans count as gap 0). Missing timestamps (<=0) disable the
// relaxation: without valid times we fall back to the stricter entity gates.
func eventsWithinMs(a, b eventCandidate, gapMs int64) bool {
	if a.StartMs <= 0 || a.EndMs <= 0 || b.StartMs <= 0 || b.EndMs <= 0 {
		return false
	}
	gap := max(max(a.StartMs, b.StartMs)-min(a.EndMs, b.EndMs), 0)
	return gap <= gapMs
}

// strongEntityIntersection returns entities present (by strong key) in both sets.
func strongEntityIntersection(a, b []Entity) []Entity {
	set := map[string]Entity{}
	for _, e := range a {
		if k := strongEntityKey(e); k != "" {
			set[k] = e
		}
	}
	var out []Entity
	seen := map[string]bool{}
	for _, e := range b {
		k := strongEntityKey(e)
		if k == "" || seen[k] {
			continue
		}
		if _, ok := set[k]; ok {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

// fuzzySubjectMatch flags two events as candidates when their topic/project names
// share >=2 specific tokens (with >=1 non-numeric). Handles the LLM naming the
// same subject inconsistently across sessions/apps (the DJI Pocket 2 case), while
// requiring >=2 specific tokens so a single generic word never over-connects.
func fuzzySubjectMatch(a, b []Entity) []Entity {
	if shared, nonNumeric := sharedSubjectTokens(a, b); shared >= 2 && nonNumeric {
		return subjectEntities(a)
	}
	return nil
}

// sharedSubjectTokens counts the specific (non-stopword) topic/project tokens two
// entity sets have in common, reporting whether any shared token is non-numeric.
// Callers apply their own threshold: the default fuzzy match needs >=2, the
// time-adjacent relaxation accepts >=1 (see gateMerge).
func sharedSubjectTokens(a, b []Entity) (shared int, nonNumeric bool) {
	ta := subjectTokens(a)
	tb := subjectTokens(b)
	for tok := range ta {
		if tb[tok] {
			shared++
			if !isAllDigits(tok) {
				nonNumeric = true
			}
		}
	}
	return shared, nonNumeric
}

// subjectTokens extracts specific (non-stopword) tokens from an event's topic
// and project entity names, for fuzzy subject matching.
func subjectTokens(ents []Entity) map[string]bool {
	toks := map[string]bool{}
	for _, e := range ents {
		if e.Type != entityTopic && e.Type != entityProject {
			continue
		}
		for _, tok := range strings.FieldsFunc(strings.ToLower(e.Name), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			if len(tok) < 2 || subjectStopwords[tok] {
				continue
			}
			toks[tok] = true
		}
	}
	return toks
}

func subjectEntities(ents []Entity) []Entity {
	var out []Entity
	for _, e := range ents {
		if e.Type == entityTopic || e.Type == entityProject {
			out = append(out, e)
		}
	}
	return out
}

// prKey returns a comparison key for a PR entity, else "". A shared PR is the ONLY
// entity that PROVES two observations are the same concrete piece of work: a PR is
// a single work unit. repo is NOT sufficient — the same repo hosts many unrelated
// bugs/features (the stitch layer enforces "same repo, different bug = NO"), so
// repo is used only to CLUSTER (reduce verifier fragmentation), never to prove
// coherence. project/person are weaker still and left entirely to the verifier.
func prKey(e Entity) string {
	if e.Type != entityPR {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(e.Name))
	if name == "" {
		return ""
	}
	return entityPR + "|" + name
}

// highSpecificityKey returns a repo/pr comparison key (the CLUSTERING edge), else "".
func highSpecificityKey(e Entity) string {
	switch e.Type {
	case entityRepo, entityPR:
		name := strings.ToLower(strings.TrimSpace(e.Name))
		if name == "" {
			return ""
		}
		return e.Type + "|" + name
	default:
		return ""
	}
}

// sharesEntityKey reports whether two entity sets share any key under keyFn.
func sharesEntityKey(a, b []Entity, keyFn func(Entity) string) bool {
	set := map[string]bool{}
	for _, e := range a {
		if k := keyFn(e); k != "" {
			set[k] = true
		}
	}
	for _, e := range b {
		if k := keyFn(e); k != "" && set[k] {
			return true
		}
	}
	return false
}

// sharesHighSpecificityEntity reports whether two observations share a repo/pr
// identity — the deterministic CLUSTERING edge for coherence.
func sharesHighSpecificityEntity(a, b []Entity) bool {
	return sharesEntityKey(a, b, highSpecificityKey)
}

// observationComponents returns the connected components (sorted source indices,
// in stable root order) of the given observation indices under the edge predicate
// over their entity sets.
func observationComponents(idxs []int, seq ObservationSequence, edge func(a, b []Entity) bool) [][]int {
	n := len(idxs)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for i := range idxs {
		for j := i + 1; j < n; j++ {
			if edge(seq.Observations[idxs[i]].Entities, seq.Observations[idxs[j]].Entities) {
				if ri, rj := find(i), find(j); ri != rj {
					parent[ri] = rj
				}
			}
		}
	}
	groups := map[int][]int{}
	for i := range idxs {
		root := find(i)
		groups[root] = append(groups[root], idxs[i])
	}
	var out [][]int
	for i := range idxs {
		if find(i) == i {
			c := groups[i]
			sort.Ints(c)
			out = append(out, c)
		}
	}
	return out
}

// coherenceClusters verifies a single extracted event's INTERNAL coherence over
// OBSERVATION nodes (NOT app-level, so a multi-topic app can't bridge unrelated
// work). Unlike the stitch-time gateMerge (which decides whether two SEPARATE
// events may join), this asks "is this ONE event actually one piece of work?".
//
// Returns (nil, false) — coherent, no-op — when the event spans a single real app
// (cross-app over-merge is impossible) OR when a shared PR links every observation
// into one component (same PR == same concrete work). Otherwise it returns the
// repo/pr CLUSTERS (sorted source indices) and needsVerify=true: repo proximity is
// not proof (same repo can hold different bugs), so a content verifier must judge
// whether the clusters are truly the same work before any split.
func coherenceClusters(ev extractedEvent, seq ObservationSequence) (clusters [][]int, needsVerify bool) {
	var idxs []int
	apps := map[string]bool{}
	for _, idx := range ev.SourceIndices {
		if idx < 0 || idx >= len(seq.Observations) {
			continue
		}
		idxs = append(idxs, idx)
		app := strings.ToLower(strings.TrimSpace(seq.Observations[idx].RealApp))
		if app != "" && app != unknownAppName {
			apps[app] = true
		}
	}
	if len(apps) <= 1 {
		return nil, false // single real app -> cross-app over-merge impossible
	}

	// PR is the only proof of "same concrete work": if a shared PR connects every
	// observation into one component, the event is coherent without an LLM call.
	if prComps := observationComponents(idxs, seq, func(a, b []Entity) bool {
		return sharesEntityKey(a, b, prKey)
	}); len(prComps) <= 1 {
		return nil, false
	}

	// Otherwise cluster by repo/pr (to reduce verifier fragmentation) and let the
	// content verifier decide — repo alone never proves coherence.
	return observationComponents(idxs, seq, sharesHighSpecificityEntity), true
}
