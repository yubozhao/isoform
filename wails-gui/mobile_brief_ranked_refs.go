package main

import (
	"strconv"
	"strings"
)

// morningBriefSelectionRefs reserves citations for the capped selection,
// including candidates the legacy section caps did not display. Its caller
// must have admitted and bound the candidate and each dependency first.
func morningBriefSelectionRefs(brief MobileBriefV1, selection morningBriefSelection) (MobileBriefV1, morningBriefSelection, error) {
	brief.Evidence = append([]MobileBriefRef(nil), brief.Evidence...)
	refs, used := map[string]string{}, map[string]bool{}
	for _, ref := range brief.Evidence {
		key := mobileBriefSourceKey(ref.SourceKind, ref.SourceID)
		if ref.Ref == "" || used[ref.Ref] || ref.SourceKind == "" || ref.SourceID == "" || refs[key] != "" {
			return brief, morningBriefSelection{}, codeError("invalid existing brief evidence")
		}
		refs[key], used[ref.Ref] = ref.Ref, true
	}
	reserve := func(kind, id, label string) (string, error) {
		if kind == "" || id == "" || strings.ContainsRune(kind, '\x00') || strings.ContainsRune(id, '\x00') ||
			strings.TrimSpace(kind) != kind || strings.TrimSpace(id) != id ||
			mobileBriefText(kind, mobileBriefKindLimit) != kind || mobileBriefText(id, mobileBriefSourceIDLimit) != id {
			return "", codeError("invalid ranked source identity")
		}
		key := mobileBriefSourceKey(kind, id)
		if ref := refs[key]; ref != "" {
			return ref, nil
		}
		if kind == mobileBriefCalendarSource && mobileBriefEvidenceCounts(brief.Evidence)[mobileBriefCalendarSource] >= mobileBriefCalendarLimit {
			return "", codeError("calendar evidence limit exceeded")
		}
		label = mobileBriefText(label, mobileBriefLabelLimit)
		if label == "" || !mobileBriefCopySafe(label) || redactThreadOutcomeText(label) != label || len(brief.Evidence) >= mobileBriefEvidenceLimit {
			return "", codeError("ranked evidence unavailable")
		}
		n := len(brief.Evidence) + 1
		ref := "e" + strconv.Itoa(n)
		for used[ref] {
			n++
			ref = "e" + strconv.Itoa(n)
		}
		brief.Evidence = append(brief.Evidence, MobileBriefRef{Ref: ref, SourceKind: kind, SourceID: id, Label: label, ObservedAt: brief.AsOf})
		refs[key], used[ref] = ref, true
		return ref, nil
	}
	seen := map[string]bool{}
	attach := func(item morningBriefCandidate) (morningBriefCandidate, error) {
		if item.ID == "" || seen[item.ID] || item.ID != mobileBriefItemKey("candidate", item.SourceKind, item.SourceID) {
			return morningBriefCandidate{}, codeError("invalid ranked item identity")
		}
		seen[item.ID] = true
		ref, err := reserve(item.SourceKind, item.SourceID, item.Title)
		if err != nil {
			return morningBriefCandidate{}, err
		}
		item.Refs = []string{ref}
		for _, dependency := range item.Dependencies {
			if dependency.Kind != "purpose" || dependency.Digest == "" || item.NextStep == "" {
				return morningBriefCandidate{}, codeError("invalid ranked item dependency")
			}
			ref, err := reserve(dependency.Kind, dependency.ID, item.NextStep)
			if err != nil {
				return morningBriefCandidate{}, err
			}
			if !containsString(item.Refs, ref) {
				item.Refs = append(item.Refs, ref)
			}
		}
		return item, nil
	}
	var out morningBriefSelection
	if selection.Lead != nil {
		item, err := attach(*selection.Lead)
		if err != nil {
			return brief, morningBriefSelection{}, err
		}
		out.Lead = &item
	}
	if len(selection.Needs) > 3 {
		return brief, morningBriefSelection{}, codeError("too many ranked needs")
	}
	for _, need := range selection.Needs {
		item, err := attach(need)
		if err != nil {
			return brief, morningBriefSelection{}, err
		}
		out.Needs = append(out.Needs, item)
	}
	if selection.Slip != nil {
		item, err := attach(*selection.Slip)
		if err != nil {
			return brief, morningBriefSelection{}, err
		}
		out.Slip = &item
	}
	if selection.Handled != nil {
		item, err := attach(*selection.Handled)
		if err != nil {
			return brief, morningBriefSelection{}, err
		}
		out.Handled = &item
	}
	return brief, out, nil
}

// morningBriefBuildSelected reserves the chosen identities before rebuilding
// the normal sections. The returned ref map is suitable for the pipeline's
// post-score resolver; source admission and the model binding remain the
// caller's responsibility.
func morningBriefBuildSelected(in mobileBriefInput, selection morningBriefSelection) (MobileBriefV1, map[string][]string, error) {
	if in.AsOf.IsZero() {
		return MobileBriefV1{}, nil, codeError("ranked brief has no cutoff")
	}
	seed, _, err := morningBriefSelectionRefs(MobileBriefV1{AsOf: formatMobileBriefTime(in.AsOf)}, selection)
	if err != nil {
		return MobileBriefV1{}, nil, err
	}
	in.rankedEvidence = seed.Evidence
	brief := buildMobileBriefV1(in)
	brief.rankedEdition, brief.NarrativeStatus, brief.Narrative = true, "unavailable", nil
	brief, bound, err := morningBriefSelectionRefs(brief, selection)
	if err != nil {
		return MobileBriefV1{}, nil, err
	}
	refs := map[string][]string{}
	add := func(item morningBriefCandidate) { refs[item.ID] = append([]string(nil), item.Refs...) }
	if bound.Lead != nil {
		add(*bound.Lead)
	}
	for _, item := range bound.Needs {
		add(item)
	}
	if bound.Slip != nil {
		add(*bound.Slip)
	}
	if bound.Handled != nil {
		add(*bound.Handled)
	}
	return brief, refs, nil
}
