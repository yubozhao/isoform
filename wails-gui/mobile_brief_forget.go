package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
)

// forgetAllBriefs is the "briefs" step of Settings › Your data › Forget
// everything (docs/forget-everything-2026-11-16.md). It removes every stored
// brief: the working, built and sealed copies, the phone packages, the
// recordings, today's and the latest pointers, and the answers given to brief
// items. The privacy epoch moves first, so a reader holding an old copy
// re-checks before it shows anything; the epoch and its withdrawal log stay,
// because a later reader still needs them to know older seals are void.
// It can run again after a stop: a second run finds nothing and succeeds.
func (a *App) forgetAllBriefs(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stateDir, err := mobileBriefStateDir()
	if err != nil {
		return err
	}
	briefPrivacyChanged()
	store := newMobileBriefStore(stateDir)
	if live := mobileBriefLive.Load(); live != nil && live.store != nil {
		store = live.store
	}
	removed, err := store.forgetAll()
	if feedbackErr := os.Remove(filepath.Join(stateDir, mobileBriefFeedbackFileName)); feedbackErr != nil && !errors.Is(feedbackErr, os.ErrNotExist) && err == nil {
		err = feedbackErr
	}
	log.Printf("[mobile-brief] forget everything removed=%d err=%v", removed, err)
	return err
}

// forgetAll removes every file in the brief folder except the privacy epoch
// and the withdrawal log, and drops the remembered current brief.
func (s *mobileBriefStore) forgetAll() (int, error) {
	if s == nil || s.dir == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentBrief, s.currentOK, s.currentFresh = MobileBriefV1{}, false, false
	mobileBriefPublicationMu.Lock()
	defer mobileBriefPublicationMu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	var first error
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == mobileBriefPrivacyEpochFile || name == mobileBriefWithdrawalLogFile {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			if first == nil {
				first = err
			}
			continue
		}
		removed++
	}
	return removed, first
}
