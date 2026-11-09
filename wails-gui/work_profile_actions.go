package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

func workProfileLineHash(text string) string {
	sum := sha256.Sum256([]byte(selfDreamCanonicalStatement(text)))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func workProfileActionText(text, fullName string, privateValues []string, limit int) (string, bool, error) {
	if !utf8.ValidString(text) || strings.IndexFunc(text, unicode.IsControl) >= 0 {
		return "", false, errors.New("invalid text")
	}
	clean, filtered := workProfileClean(text, fullName, privateValues)
	if clean == "" || len(clean) > limit {
		return "", false, errors.New("invalid text")
	}
	return clean, filtered, nil
}

func workProfileDraftForAdd(state *workProfileState, now time.Time) *WorkProfileDraft {
	if state.Pending != nil {
		return state.Pending
	}
	var draft WorkProfileDraft
	if state.Approved != nil {
		draft = *state.Approved
		draft.Lines = append([]WorkProfileLine(nil), state.Approved.Lines...)
		draft.Specialties = append([]WorkProfileSpecialty(nil), state.Approved.Specialties...)
		for i := range draft.Lines {
			draft.Lines[i].Status = "unchanged"
			draft.Lines[i].Previous = ""
		}
	}
	draft.ID = uuid.NewString()
	draft.MadeAt = now.UTC().Format(time.RFC3339)
	draft.BasedOnVersion = state.ApprovedVersion
	draft.Edited = true
	state.Pending = &draft
	return state.Pending
}

func workProfileAddLine(state *workProfileState, group, text, fullName string, privateValues []string, now time.Time) error {
	clean, filtered, err := workProfileActionText(text, fullName, privateValues, 1000)
	if err != nil {
		return err
	}
	if group != "" {
		found := false
		draft := state.Pending
		if draft == nil {
			draft = state.Approved
		}
		if draft != nil {
			for _, specialty := range draft.Specialties {
				found = found || specialty.ID == group
			}
		}
		if !found {
			return errors.New("unknown specialty")
		}
	}
	draft := workProfileDraftForAdd(state, now)
	draft.Lines = append(draft.Lines, WorkProfileLine{ID: uuid.NewString(), Group: group, Text: clean, Status: "new", Filtered: filtered})
	draft.Edited = true
	return nil
}

func workProfileEditLine(state *workProfileState, id, text, fullName string, privateValues []string) error {
	clean, filtered, err := workProfileActionText(text, fullName, privateValues, 1000)
	if err != nil {
		return err
	}
	if state.Pending == nil {
		return errors.New("no draft")
	}
	for i := range state.Pending.Lines {
		line := &state.Pending.Lines[i]
		if line.ID != id || line.Removed {
			continue
		}
		if line.Status == "unchanged" {
			line.Previous = line.Text
			line.Status = "changed"
		}
		line.Text, line.Filtered = clean, filtered
		state.Pending.Edited = true
		return nil
	}
	return errors.New("unknown line")
}

func workProfileSetRemoved(state *workProfileState, id string, removed bool) error {
	if state.Pending == nil {
		return errors.New("no draft")
	}
	for i := range state.Pending.Lines {
		line := &state.Pending.Lines[i]
		if line.ID != id {
			continue
		}
		if line.Removed == removed {
			return nil
		}
		fingerprint := workProfileLineHash(line.Text)
		line.Removed = removed
		if removed {
			state.RemovedFingerprints = knowledgeProvenanceUnion(state.RemovedFingerprints, []string{fingerprint})
			if line.Group != "" {
				hasLine := false
				for _, other := range state.Pending.Lines {
					hasLine = hasLine || (other.Group == line.Group && !other.Removed && other.Status != "suggested_removal")
				}
				if !hasLine {
					for j := range state.Pending.Specialties {
						if state.Pending.Specialties[j].ID == line.Group {
							state.Pending.Specialties[j].Selected = false
						}
					}
				}
			}
		} else {
			stillRemoved := false
			for _, other := range state.Pending.Lines {
				stillRemoved = stillRemoved || (other.Removed && workProfileLineHash(other.Text) == fingerprint)
			}
			kept := state.RemovedFingerprints[:0]
			for _, hash := range state.RemovedFingerprints {
				if hash != fingerprint || stillRemoved {
					kept = append(kept, hash)
				}
			}
			state.RemovedFingerprints = kept
		}
		state.Pending.Edited = true
		return nil
	}
	return errors.New("unknown line")
}

func workProfileRename(state *workProfileState, id, name, fullName string, privateValues []string) (bool, error) {
	clean, filtered, err := workProfileActionText(name, fullName, privateValues, 160)
	if err != nil || utf8.RuneCountInString(clean) > 40 {
		return false, errors.New("invalid specialty")
	}
	if state.Pending == nil {
		return false, errors.New("no draft")
	}
	for _, specialty := range state.Pending.Specialties {
		if specialty.ID != id && strings.EqualFold(specialty.Name, clean) {
			return false, errors.New("duplicate specialty")
		}
	}
	for i := range state.Pending.Specialties {
		if state.Pending.Specialties[i].ID == id {
			state.Pending.Specialties[i].Name = clean
			state.Pending.Edited = true
			return filtered, nil
		}
	}
	return false, errors.New("unknown specialty")
}

func workProfileSelect(state *workProfileState, ids []string) error {
	if state.Pending == nil || len(ids) > 3 {
		return errors.New("invalid specialty selection")
	}
	selected := make(map[string]bool)
	for _, id := range ids {
		if selected[id] {
			return errors.New("duplicate specialty")
		}
		selected[id] = true
	}
	for id := range selected {
		found, hasLine := false, false
		for _, specialty := range state.Pending.Specialties {
			found = found || specialty.ID == id
		}
		for _, line := range state.Pending.Lines {
			hasLine = hasLine || (line.Group == id && !line.Removed && line.Status != "suggested_removal")
		}
		if !found || !hasLine {
			return errors.New("specialty has no usable line")
		}
	}
	for i := range state.Pending.Specialties {
		state.Pending.Specialties[i].Selected = selected[state.Pending.Specialties[i].ID]
	}
	state.Pending.Edited = true
	return nil
}

func workProfileKeepLine(state *workProfileState, id string) error {
	if state.Pending == nil {
		return errors.New("no draft")
	}
	for i := range state.Pending.Lines {
		line := &state.Pending.Lines[i]
		if line.ID == id && !line.Removed && line.Status == "suggested_removal" {
			line.Status = "unchanged"
			line.Kept = true
			state.Pending.Edited = true
			return nil
		}
	}
	return errors.New("unknown suggested removal")
}
