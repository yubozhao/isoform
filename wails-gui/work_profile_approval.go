package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"
)

func workProfilePreview(state workProfileState, fullName string, privateValues []string, now time.Time) (WorkProfileUpload, error) {
	if state.Pending == nil {
		return WorkProfileUpload{}, errors.New("no draft")
	}
	if err := workProfileCheckDraftPrivacy(state.Pending, fullName, privateValues); err != nil {
		return WorkProfileUpload{}, err
	}
	groups := make([]workProfileUploadGroup, 0, len(state.Pending.Specialties))
	groupIndex := make(map[string]int)
	for _, specialty := range state.Pending.Specialties {
		if specialty.ID == "" {
			return WorkProfileUpload{}, errors.New("invalid specialty")
		}
		if _, exists := groupIndex[specialty.ID]; exists {
			return WorkProfileUpload{}, errors.New("duplicate specialty")
		}
		groupIndex[specialty.ID] = len(groups)
		groups = append(groups, workProfileUploadGroup{Name: specialty.Name, Selected: specialty.Selected})
	}
	var general []string
	for _, line := range state.Pending.Lines {
		if line.Removed || line.Status == "suggested_removal" {
			continue
		}
		if line.Group == "" {
			general = append(general, line.Text)
			continue
		}
		index, exists := groupIndex[line.Group]
		if !exists {
			return WorkProfileUpload{}, errors.New("unknown specialty")
		}
		groups[index].Lines = append(groups[index].Lines, line.Text)
	}
	return workProfilePrepareUpload(groups, general, state.Pending.LearnedThrough, state.Pending.BasedOnVersion, fullName, privateValues, now)
}

// Approval serializes the preview, upload, and local version transition so an
// edit cannot change what is sent between review and the request.
func workProfileApprove(ctx context.Context, path string, uploader workProfileUploader, fullName string, privateValues []string, now time.Time) (workProfileState, error) {
	return workProfileApproveDraft(ctx, path, uploader, "", nil, fullName, privateValues, now)
}

func workProfileApproveDraft(ctx context.Context, path string, uploader workProfileUploader, draftID string, expected *WorkProfileUpload, fullName string, privateValues []string, now time.Time) (workProfileState, error) {
	workProfileStateMu.Lock()
	defer workProfileStateMu.Unlock()
	state, err := workProfileReadState(path)
	if err != nil {
		return workProfileState{}, err
	}
	if uploader == nil {
		return state, errors.New("profile uploader unavailable")
	}
	if state.Pending == nil || (draftID != "" && state.Pending.ID != draftID) || state.Pending.BasedOnVersion != state.ApprovedVersion {
		return state, errors.New("stale work profile draft")
	}
	payload, err := workProfilePreview(state, fullName, privateValues, now)
	if err != nil {
		return state, err
	}
	if expected != nil {
		want, _ := json.Marshal(expected)
		got, _ := json.Marshal(payload)
		if !bytes.Equal(want, got) {
			return state, errors.New("work profile preview changed")
		}
	}
	result, err := uploader.UploadApprovedProfile(ctx, payload)
	if err != nil {
		return state, err
	}
	if result.Version != state.ApprovedVersion+1 || result.ApprovedAt == "" {
		return state, errors.New("profile uploaded but response was invalid; reload server state")
	}
	approved := *state.Pending
	approved.Lines = make([]WorkProfileLine, 0, len(state.Pending.Lines))
	for _, line := range state.Pending.Lines {
		if line.Status == "suggested_removal" {
			state.RemovedFingerprints = knowledgeProvenanceUnion(state.RemovedFingerprints, []string{workProfileLineHash(line.Text)})
		}
		if !line.Removed && line.Status != "suggested_removal" {
			line.Previous = ""
			line.Status = "unchanged"
			approved.Lines = append(approved.Lines, line)
		}
	}
	approved.Edited = false
	state.Approved, state.Pending = &approved, nil
	state.ApprovedVersion, state.ApprovedAt = result.Version, result.ApprovedAt
	if err := workProfileWriteState(path, state); err != nil {
		return state, errors.New("profile uploaded but local state could not be saved; reload server state")
	}
	return state, nil
}
