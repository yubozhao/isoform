package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type WorkProfileLine struct {
	ID       string `json:"id"`
	Group    string `json:"group,omitempty"`
	Text     string `json:"text"`
	Previous string `json:"previous,omitempty"`
	Status   string `json:"status"`
	Filtered bool   `json:"filtered"`
	Removed  bool   `json:"removed"`
	LastSeen string `json:"lastSeen,omitempty"`
	Kept     bool   `json:"kept,omitempty"`
}

type WorkProfileSpecialty struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Selected bool   `json:"selected"`
}

type WorkProfileDraft struct {
	ID             string                 `json:"id"`
	MadeAt         string                 `json:"madeAt"`
	LearnedThrough string                 `json:"learnedThrough"`
	BasedOnVersion int64                  `json:"basedOnVersion"`
	Fingerprint    string                 `json:"fingerprint"`
	Specialties    []WorkProfileSpecialty `json:"specialties"`
	Lines          []WorkProfileLine      `json:"lines"`
	Edited         bool                   `json:"edited"`
}

type workProfileState struct {
	Format              int               `json:"format"`
	ApprovedVersion     int64             `json:"approvedVersion"`
	ApprovedAt          string            `json:"approvedAt,omitempty"`
	Approved            *WorkProfileDraft `json:"approved,omitempty"`
	Pending             *WorkProfileDraft `json:"pending,omitempty"`
	Ready               *WorkProfileDraft `json:"ready,omitempty"`
	RemovedFingerprints []string          `json:"removedFingerprints,omitempty"`
	LastWeeklySlot      string            `json:"lastWeeklySlot,omitempty"`
	LastDraftAt         string            `json:"lastDraftAt,omitempty"`
	LastError           string            `json:"lastError,omitempty"`
	LastFingerprint     string            `json:"lastFingerprint,omitempty"`
}

var workProfileStateMu sync.Mutex

func workProfileReadState(path string) (workProfileState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return workProfileState{Format: 1}, nil
	}
	if err != nil {
		return workProfileState{}, err
	}
	var state workProfileState
	if err := json.Unmarshal(data, &state); err != nil || state.Format != 1 {
		return workProfileState{}, errors.New("invalid work profile state")
	}
	return state, nil
}

func workProfileWriteState(path string, state workProfileState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".work-profile-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func workProfileUpdateState(path string, update func(*workProfileState) error) (workProfileState, error) {
	workProfileStateMu.Lock()
	defer workProfileStateMu.Unlock()
	state, err := workProfileReadState(path)
	if err != nil {
		return workProfileState{}, err
	}
	if err := update(&state); err != nil {
		return workProfileState{}, err
	}
	if err := workProfileWriteState(path, state); err != nil {
		return workProfileState{}, err
	}
	return state, nil
}
