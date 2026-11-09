package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

var workProfileBuilds sync.Map

// workProfileRunDraft is called by the manual action or a weekly scheduler.
// A separate in-process guard prevents overlapping model calls for one file.
func workProfileRunDraft(ctx context.Context, path string, sources WorkProfileSources, model workProfileModel, fingerprint func() (string, error), fullName string, privateValues []string, now time.Time, manual bool) (workProfileState, error) {
	if _, loaded := workProfileBuilds.LoadOrStore(path, true); loaded {
		return workProfileState{}, errors.New("work profile build already running")
	}
	defer workProfileBuilds.Delete(path)
	return workProfileRunDraftLocked(ctx, path, sources, model, fingerprint, fullName, privateValues, now, manual)
}

func workProfileRunDraftLocked(ctx context.Context, path string, sources WorkProfileSources, model workProfileModel, fingerprint func() (string, error), fullName string, privateValues []string, now time.Time, manual bool) (workProfileState, error) {
	currentFingerprint, err := fingerprint()
	if err != nil {
		return workProfileState{}, err
	}
	workProfileStateMu.Lock()
	state, err := workProfileReadState(path)
	if err == nil && manual && state.LastDraftAt != "" {
		last, parseErr := time.Parse(time.RFC3339, state.LastDraftAt)
		if parseErr != nil || now.Before(last.Add(24*time.Hour)) {
			err = errors.New("work profile draft limit")
		}
	}
	if err == nil && !manual && state.LastFingerprint == currentFingerprint && !(state.Pending == nil && state.Ready != nil) {
		workProfileStateMu.Unlock()
		return state, nil
	}
	if err == nil && manual {
		state.LastDraftAt = now.UTC().Format(time.RFC3339)
		err = workProfileWriteState(path, state)
	}
	workProfileStateMu.Unlock()
	if err != nil {
		return state, err
	}
	draft, err := workProfileMakeDraft(ctx, sources, model, fingerprint, state.Approved, state.ApprovedVersion, state.RemovedFingerprints, fullName, privateValues, now)
	if err != nil {
		return state, err
	}
	return workProfileUpdateState(path, func(current *workProfileState) error {
		if current.ApprovedVersion != state.ApprovedVersion || !workProfileEqualStrings(current.RemovedFingerprints, state.RemovedFingerprints) {
			return errors.New("work profile changed during build")
		}
		current.LastFingerprint = draft.Fingerprint
		if current.Pending != nil && current.Pending.Edited {
			current.Ready = draft
		} else {
			current.Pending = draft
			current.Ready = nil
		}
		return nil
	})
}

func workProfileEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
