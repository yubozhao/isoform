package main

import (
	"net/http"
	"path/filepath"
)

type workProfilePhoneSummary struct {
	Version        int64    `json:"version"`
	LearnedThrough string   `json:"learnedThrough"`
	Specialties    []string `json:"specialties"`
	HasDraft       bool     `json:"hasDraft"`
}

func workProfileSummary(state workProfileState) workProfilePhoneSummary {
	summary := workProfilePhoneSummary{Version: state.ApprovedVersion, Specialties: []string{}, HasDraft: state.Pending != nil || state.Ready != nil}
	if state.Approved != nil {
		summary.LearnedThrough = state.Approved.LearnedThrough
		for _, specialty := range state.Approved.Specialties {
			if specialty.Selected && len(summary.Specialties) < 3 {
				summary.Specialties = append(summary.Specialties, specialty.Name)
			}
		}
	}
	return summary
}

func (bridge *mobileBridge) serveWorkProfileSummary(w http.ResponseWriter) {
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	dir, err := flavorDataDir()
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	workProfileStateMu.Lock()
	state, err := workProfileReadState(filepath.Join(dir, "work-profile.json"))
	workProfileStateMu.Unlock()
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, workProfileSummary(state), "")
}
