package main

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

// WorkProfileView is local review state; it never contains credentials.
type WorkProfileView struct {
	ApprovedVersion int64             `json:"approvedVersion"`
	ApprovedAt      string            `json:"approvedAt,omitempty"`
	Approved        *WorkProfileDraft `json:"approved,omitempty"`
	Pending         *WorkProfileDraft `json:"pending,omitempty"`
	NewerReady      bool              `json:"newerReady"`
	Building        bool              `json:"building"`
	LastError       string            `json:"lastError,omitempty"`
	NextDraftAt     string            `json:"nextDraftAt"`
	LastDraftAt     string            `json:"lastDraftAt,omitempty"`
}

type WorkProfileApprovalResult struct {
	View            WorkProfileView `json:"view"`
	ConflictVersion *int64          `json:"conflictVersion,omitempty"`
}

func workProfileView(s workProfileState) WorkProfileView {
	return WorkProfileView{ApprovedVersion: s.ApprovedVersion, ApprovedAt: s.ApprovedAt, Approved: s.Approved, Pending: s.Pending, NewerReady: s.Ready != nil, LastDraftAt: s.LastDraftAt, LastError: s.LastError, NextDraftAt: workProfileWeeklySlot(time.Now()).AddDate(0, 0, 7).Format(time.RFC3339)}
}

func workProfileViewAtPath(state workProfileState, path string) WorkProfileView {
	view := workProfileView(state)
	_, view.Building = workProfileBuilds.Load(path)
	return view
}

func workProfilePath() (string, error) {
	dir, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "work-profile.json"), nil
}

func (a *App) workProfilePrivacy() (string, []string, error) {
	env, err := a.yourAgentEnvFor()
	if err != nil {
		return "", nil, err
	}
	state, err := loadYourAgentState(env)
	if err != nil {
		return "", nil, err
	}
	return env.fullName, []string{state.Hub}, nil
}

func (a *App) workProfileChanged() {
	a.emitBrowserEvent("work-profile:changed")
	a.emitBrowserEvent("agent-owner:changed")
}

func (a *App) GetWorkProfile() (WorkProfileView, error) {
	path, err := workProfilePath()
	if err != nil {
		return WorkProfileView{}, err
	}
	workProfileStateMu.Lock()
	state, err := workProfileReadState(path)
	workProfileStateMu.Unlock()
	return workProfileViewAtPath(state, path), err
}

func (a *App) updateWorkProfile(draftID string, change func(*workProfileState) error) (WorkProfileView, error) {
	path, err := workProfilePath()
	if err != nil {
		return WorkProfileView{}, err
	}
	name, private, err := a.workProfilePrivacy()
	if err != nil {
		return WorkProfileView{}, err
	}
	state, err := workProfileUpdateState(path, func(state *workProfileState) error {
		if (state.Pending != nil && state.Pending.ID != draftID) || (state.Pending == nil && draftID != "") {
			return errors.New("stale work profile draft")
		}
		if err := change(state); err != nil {
			return err
		}
		return workProfileCheckDraftPrivacy(state.Pending, name, private)
	})
	if err == nil {
		a.workProfileChanged()
	}
	return workProfileViewAtPath(state, path), err
}

func (a *App) EditWorkProfileLine(draftID, id, text string) (WorkProfileView, error) {
	name, private, err := a.workProfilePrivacy()
	if err != nil {
		return WorkProfileView{}, err
	}
	return a.updateWorkProfile(draftID, func(s *workProfileState) error { return workProfileEditLine(s, id, text, name, private) })
}
func (a *App) SetWorkProfileLineRemoved(draftID, id string, removed bool) (WorkProfileView, error) {
	return a.updateWorkProfile(draftID, func(s *workProfileState) error { return workProfileSetRemoved(s, id, removed) })
}
func (a *App) AddWorkProfileLine(draftID, group, text string) (WorkProfileView, error) {
	name, private, err := a.workProfilePrivacy()
	if err != nil {
		return WorkProfileView{}, err
	}
	return a.updateWorkProfile(draftID, func(s *workProfileState) error { return workProfileAddLine(s, group, text, name, private, time.Now()) })
}
func (a *App) SetWorkProfileSpecialties(draftID string, ids []string) (WorkProfileView, error) {
	return a.updateWorkProfile(draftID, func(s *workProfileState) error { return workProfileSelect(s, ids) })
}
func (a *App) RenameWorkProfileSpecialty(draftID, id, name string) (WorkProfileView, error) {
	fullName, private, err := a.workProfilePrivacy()
	if err != nil {
		return WorkProfileView{}, err
	}
	return a.updateWorkProfile(draftID, func(s *workProfileState) error {
		_, err := workProfileRename(s, id, name, fullName, private)
		return err
	})
}
func (a *App) KeepWorkProfileLine(draftID, id string) (WorkProfileView, error) {
	return a.updateWorkProfile(draftID, func(s *workProfileState) error { return workProfileKeepLine(s, id) })
}
func (a *App) DiscardWorkProfileDraft(draftID string) (WorkProfileView, error) {
	view, err := a.updateWorkProfile(draftID, func(s *workProfileState) error {
		if s.Pending == nil {
			return errors.New("no draft")
		}
		s.Pending = nil
		return nil
	})
	if err == nil {
		workProfileKick()
	}
	return view, err
}
func (a *App) PreviewWorkProfileUpload(draftID string) (WorkProfileUpload, error) {
	path, err := workProfilePath()
	if err != nil {
		return WorkProfileUpload{}, err
	}
	name, private, err := a.workProfilePrivacy()
	if err != nil {
		return WorkProfileUpload{}, err
	}
	workProfileStateMu.Lock()
	defer workProfileStateMu.Unlock()
	state, err := workProfileReadState(path)
	if err != nil {
		return WorkProfileUpload{}, err
	}
	if state.Pending == nil || draftID == "" || state.Pending.ID != draftID {
		return WorkProfileUpload{}, errors.New("stale work profile draft")
	}
	return workProfilePreview(state, name, private, time.Now())
}
func (a *App) ApproveWorkProfile(draftID string, preview WorkProfileUpload) (WorkProfileApprovalResult, error) {
	if draftID == "" {
		return WorkProfileApprovalResult{}, errors.New("stale work profile draft")
	}
	path, err := workProfilePath()
	if err != nil {
		return WorkProfileApprovalResult{}, err
	}
	name, private, err := a.workProfilePrivacy()
	if err != nil {
		return WorkProfileApprovalResult{}, err
	}
	env, err := a.agentOwnerEnv()
	if err != nil {
		return WorkProfileApprovalResult{}, err
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := workProfileApproveDraft(ctx, path, agentOwnerProfileUploader{env}, draftID, &preview, name, private, time.Now())
	var refusal *agentServerError
	if errors.As(err, &refusal) && refusal.Code == "profile_version_conflict" && refusal.CurrentVersion != nil {
		return WorkProfileApprovalResult{View: workProfileViewAtPath(state, path), ConflictVersion: refusal.CurrentVersion}, nil
	}
	if err == nil {
		a.workProfileChanged()
		workProfileKick()
	}
	return WorkProfileApprovalResult{View: workProfileViewAtPath(state, path)}, err
}

// BuildWorkProfileDraft starts one background draft and enforces the manual limit.
func (a *App) BuildWorkProfileDraft() (WorkProfileView, error) {
	return a.startWorkProfileDraft(true, "")
}

func (a *App) startWorkProfileDraft(manual bool, weeklySlot string) (WorkProfileView, error) {
	path, err := workProfilePath()
	if err != nil {
		return WorkProfileView{}, err
	}
	name, private, err := a.workProfilePrivacy()
	if err != nil {
		return WorkProfileView{}, err
	}
	dir, err := knowledgeDir()
	if err != nil {
		return WorkProfileView{}, err
	}
	a.mu.Lock()
	runner := a.runner
	ctx := a.ctx
	a.mu.Unlock()
	if runner == nil {
		return WorkProfileView{}, errors.New("background model unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, loaded := workProfileBuilds.LoadOrStore(path, true); loaded {
		return WorkProfileView{}, errors.New("work profile build already running")
	}
	workProfileStateMu.Lock()
	state, err := workProfileReadState(path)
	now := time.Now()
	if err == nil && manual && state.LastDraftAt != "" {
		last, parseErr := time.Parse(time.RFC3339, state.LastDraftAt)
		if parseErr != nil || now.Before(last.Add(24*time.Hour)) {
			err = errors.New("work profile draft limit")
		}
	}
	workProfileStateMu.Unlock()
	if err != nil {
		workProfileBuilds.Delete(path)
		return WorkProfileView{}, err
	}
	go func() {
		_, buildErr := workProfileRunDraftLocked(ctx, path, workProfileDiskSources{dir: dir}, workProfileBackgroundModel{runner: runner}, func() (string, error) { return yourAgentDirFingerprint(dir) }, name, private, now, manual)
		_, _ = workProfileUpdateState(path, func(s *workProfileState) error {
			s.LastError = ""
			if weeklySlot != "" && ctx.Err() == nil {
				s.LastWeeklySlot = weeklySlot
			}
			if buildErr != nil {
				s.LastError = "draft_failed"
			}
			return nil
		})
		workProfileBuilds.Delete(path)
		a.workProfileChanged()
		if manual {
			workProfileKick()
		}
	}()
	view := workProfileView(state)
	view.Building = true
	a.workProfileChanged()
	return view, nil
}
