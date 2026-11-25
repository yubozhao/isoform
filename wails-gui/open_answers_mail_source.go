package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"wails-gui/internal/platform/mailheaders"
	"wails-gui/storage"
)

var errOpenAnswerMailUnavailable = errors.New("mail source is not currently available")

type openAnswerMailFingerprint struct {
	digest    [sha256.Size]byte
	hasPerson bool
}

// Exclude storage timestamps; all other fields are covered, including the
// person resolved for this read. JSON encoding is deterministic for this
// fixed struct and has no unsupported field types.
func fingerprintOpenAnswerMail(row storage.MailMessageRow) openAnswerMailFingerprint {
	row.FirstSeenAt, row.UpdatedAt = 0, 0
	data, _ := json.Marshal(row)
	return openAnswerMailFingerprint{digest: sha256.Sum256(data), hasPerson: row.PrincipalID != ""}
}

// A successful Mail read can still omit headers: parsing drops messages
// without an ID or date, and mailRows excludes paused or invalid messages.
// Duplicate IDs likewise cannot represent all observed messages in storage.
// Keep ordinary Mail sync behavior, but do not use such a read to establish
// that a reply is absent. A later complete read can restore this evidence.
func openAnswerMailReadFingerprints(snap mailheaders.Snapshot, rows []storage.MailMessageRow) map[string]openAnswerMailFingerprint {
	if !snap.Complete || snap.Dropped != 0 || len(rows) != len(snap.Inbox)+len(snap.Sent) {
		return nil
	}
	fingerprints := make(map[string]openAnswerMailFingerprint, len(rows))
	for _, row := range rows {
		if row.ID == "" || row.At <= 0 {
			return nil
		}
		if _, exists := fingerprints[row.ID]; exists {
			return nil
		}
		fingerprints[row.ID] = fingerprintOpenAnswerMail(row)
	}
	return fingerprints
}

// openAnswerMailSnapshot is a preliminary, header-only read. The revision
// must be checked again at admission; it does not fence Mail's in-memory
// permissions or prove that a subject-derived thread is unambiguous. It is
// not an actionable check and must not by itself create a promise.
type openAnswerMailSnapshot struct {
	rows     []storage.MailMessageRow
	revision storage.ReadRevision
}

func (s *mailSyncService) openAnswerSnapshot(ctx context.Context, now time.Time) (openAnswerMailSnapshot, error) {
	if s == nil || s.store == nil {
		return openAnswerMailSnapshot{}, errOpenAnswerMailUnavailable
	}
	// Order against the observer's write and protection-clear paths. Other
	// store writers are covered by the before/after revision, not this mutex.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if !s.openAnswerReadable(now) {
		return openAnswerMailSnapshot{}, errOpenAnswerMailUnavailable
	}
	if s.lastReadRows == nil {
		return openAnswerMailSnapshot{}, errOpenAnswerMailUnavailable
	}
	revision, err := s.store.ReadRevision(ctx)
	if err != nil {
		return openAnswerMailSnapshot{}, err
	}
	rows, err := s.store.ListMailMessages(ctx, now.Add(-storage.MailRetention).UnixMilli())
	if err != nil {
		return openAnswerMailSnapshot{}, err
	}
	current, err := s.store.ReadRevision(ctx)
	if err != nil {
		return openAnswerMailSnapshot{}, err
	}
	if current != revision {
		return openAnswerMailSnapshot{}, storage.ErrReadRevisionChanged
	}
	if !s.openAnswerReadable(now) {
		return openAnswerMailSnapshot{}, errOpenAnswerMailUnavailable
	}
	// The observer keeps some missing rows intentionally (for example near
	// the retention boundary). Only headers actually seen in its latest complete
	// read are evidence; an absent or changed row does not establish no reply.
	currentRows := make([]storage.MailMessageRow, 0, len(s.lastReadRows))
	for _, row := range rows {
		if observed, ok := s.lastReadRows[row.ID]; ok {
			// Storage retains a previous person when the latest resolver has no
			// match. Neither that value nor storage timestamps are Mail headers.
			// Do not let them replace the latest observation as evidence.
			if !observed.hasPerson {
				row.PrincipalID = ""
			}
			if fingerprintOpenAnswerMail(row) != observed {
				return openAnswerMailSnapshot{}, errOpenAnswerMailUnavailable
			}
			currentRows = append(currentRows, row)
		}
	}
	if len(currentRows) != len(s.lastReadRows) {
		return openAnswerMailSnapshot{}, errOpenAnswerMailUnavailable
	}
	return openAnswerMailSnapshot{rows: currentRows, revision: revision}, nil
}

// The last successful read is not enough: disabling reading, protecting Mail,
// pausing observation, losing Automation access or stopping the loop can all
// leave retained headers behind. Do not prompt for access or launch Mail here.
// Caller holds writeMu while reading emptyReads.
func (s *mailSyncService) openAnswerReadable(now time.Time) bool {
	if s.enabled == nil || s.protected == nil || s.paused == nil || s.process == nil || s.automation == nil ||
		!s.enabled.on() || s.protected() || s.paused() || s.process() <= 0 || s.automation() != mailAutomationAllowed {
		return false
	}
	s.mu.Lock()
	running := s.cancel != nil && s.loopCtx != nil && s.loopCtx.Err() == nil
	last, lastErr, notRunning, slow, syncing := s.lastSync, s.lastErr, s.notRunning, s.slowReads, s.syncing
	s.mu.Unlock()
	// A pending read may already have seen a reply that is not yet stored.
	// Do not treat the preceding snapshot as current during that read.
	if !running || syncing || last.IsZero() || last.After(now) || now.Sub(last) > 2*mailSyncInterval || lastErr != "" || notRunning || slow > 0 {
		return false
	}
	// One empty read is explicitly treated by the observer as inconclusive.
	return s.emptyReads[storage.MailboxInbox] != 1 && s.emptyReads[storage.MailboxSent] != 1 &&
		s.openAnswerEmptyReads[storage.MailboxInbox] != 1 && s.openAnswerEmptyReads[storage.MailboxSent] != 1
}
