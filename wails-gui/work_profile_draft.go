package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// workProfileMakeDraft takes only the approved source interface and a model.
// The fingerprint recheck discards a result if its inputs changed in flight.
func workProfileMakeDraft(ctx context.Context, sources WorkProfileSources, model workProfileModel, fingerprint func() (string, error), approved *WorkProfileDraft, version int64, removed []string, fullName string, privateValues []string, now time.Time) (*WorkProfileDraft, error) {
	for attempt := 0; attempt < 2; attempt++ {
		before, err := fingerprint()
		if err != nil {
			return nil, err
		}
		candidates, learned, err := workProfileCandidates(sources, fullName, privateValues)
		if err != nil {
			return nil, err
		}
		result, err := workProfileGenerate(ctx, model, candidates, fullName, privateValues)
		if err != nil {
			return nil, err
		}
		after, err := fingerprint()
		if err != nil {
			return nil, err
		}
		if before != after {
			continue
		}
		draft, err := workProfileAssembleDraft(candidates, result, learned, before, approved, version, removed, fullName, privateValues, now)
		if err != nil {
			return nil, err
		}
		return draft, nil
	}
	return nil, errors.New("knowledge changed during work profile generation")
}

func workProfileAssembleDraft(candidates []workProfileCandidate, result workProfileModelResult, learned, fingerprint string, approved *WorkProfileDraft, version int64, removed []string, fullName string, privateValues []string, now time.Time) (*WorkProfileDraft, error) {
	draft := &WorkProfileDraft{ID: uuid.NewString(), MadeAt: now.UTC().Format(time.RFC3339), LearnedThrough: learned, BasedOnVersion: version, Fingerprint: fingerprint, Specialties: []WorkProfileSpecialty{}, Lines: []WorkProfileLine{}}
	byGroup := make(map[string]int)
	for _, candidate := range candidates {
		if candidate.Kind != "specialty" || candidate.Group == "" {
			continue
		}
		id := workProfileLineHash(candidate.Group)
		name := candidate.Text
		if approved != nil {
			for _, old := range approved.Specialties {
				if old.ID == id {
					name = old.Name
				}
			}
		}
		clean, _ := workProfileClean(name, fullName, privateValues)
		if clean != name || name == "" {
			return nil, errors.New("work profile specialty privacy check failed")
		}
		byGroup[candidate.Group] = len(draft.Specialties)
		draft.Specialties = append(draft.Specialties, WorkProfileSpecialty{ID: id, Name: name})
	}
	if approved != nil {
		for _, old := range approved.Specialties {
			found := false
			for _, specialty := range draft.Specialties {
				found = found || specialty.ID == old.ID
			}
			if !found {
				clean, _ := workProfileClean(old.Name, fullName, privateValues)
				if clean != old.Name || old.Name == "" {
					return nil, errors.New("work profile specialty privacy check failed")
				}
				draft.Specialties = append(draft.Specialties, old)
			}
		}
	}
	oldUsed := map[int]bool{}
	for _, line := range result.Lines {
		if workProfileContains(removed, workProfileLineHash(line.Text)) {
			continue
		}
		group := ""
		if index, ok := byGroup[line.Group]; ok {
			group = draft.Specialties[index].ID
		}
		item := WorkProfileLine{ID: uuid.NewString(), Group: group, Text: line.Text, Status: "new", Filtered: line.Filtered, LastSeen: line.LastSeen}
		if approved != nil {
			for i, old := range approved.Lines {
				if oldUsed[i] || old.Group != group {
					continue
				}
				a, b := selfDreamCanonicalStatement(old.Text), selfDreamCanonicalStatement(line.Text)
				shorter, longer := a, b
				if len(strings.Fields(shorter)) > len(strings.Fields(longer)) {
					shorter, longer = longer, shorter
				}
				if shorter == "" || selfDreamWordCoverage(shorter, longer) < 0.8 {
					continue
				}
				oldUsed[i] = true
				item.ID = old.ID
				item.Kept = old.Kept
				if old.Text == line.Text {
					item.Status = "unchanged"
				} else {
					item.Status, item.Previous = "changed", old.Text
				}
				break
			}
		}
		draft.Lines = append(draft.Lines, item)
	}
	if approved != nil {
		for i, old := range approved.Lines {
			if oldUsed[i] || workProfileContains(removed, workProfileLineHash(old.Text)) {
				continue
			}
			old.Status, old.Previous = "unchanged", ""
			if at, err := time.Parse("2006-01-02", old.LastSeen); !old.Kept && err == nil && !at.After(now.AddDate(0, 0, -60)) {
				old.Status = "suggested_removal"
			}
			draft.Lines = append(draft.Lines, old)
		}
	}
	for i := range draft.Specialties {
		hasLine := false
		for _, line := range draft.Lines {
			hasLine = hasLine || (line.Group == draft.Specialties[i].ID && !line.Removed && line.Status != "suggested_removal")
		}
		draft.Specialties[i].Selected = hasLine && approved == nil
		if approved != nil {
			for _, old := range approved.Specialties {
				if old.ID == draft.Specialties[i].ID {
					draft.Specialties[i].Selected = hasLine && old.Selected
				}
			}
		}
	}
	if err := workProfileCheckDraftPrivacy(draft, fullName, privateValues); err != nil {
		return nil, err
	}

	return draft, nil
}

func workProfileContains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

// Check every piece of text shown in a local draft, including old lines.
func workProfileCheckDraftPrivacy(draft *WorkProfileDraft, fullName string, privateValues []string) error {
	if draft == nil {
		return nil
	}
	var texts []string
	for _, specialty := range draft.Specialties {
		texts = append(texts, specialty.Name)
	}
	for _, line := range draft.Lines {
		texts = append(texts, line.Text, line.Previous)
	}
	for _, text := range texts {
		if text == "" {
			continue
		}
		clean, _ := workProfileClean(text, fullName, privateValues)
		if clean != text {
			return errors.New("work profile draft privacy check failed")
		}
	}
	return nil
}
