package main

import (
	"crypto/sha256"
	"fmt"

	"github.com/google/uuid"
)

// Late parts of a recording the computer already closed.
//
// A watch that records for hours sends its parts through the phone as it
// goes. When the phone cannot send for a while (out of reach, the app not
// running), the computer stops waiting after mobileRecordingAbandonedAfter,
// closes the recording with the parts it has and transcribes it. The rest of
// the recording still arrives later, under the same recording id and with the
// part numbers that follow. Those parts used to be refused as not valid, for
// good: the audio never left the phone, and each refused copy stayed on the
// relay until it filled the phone's quota there and nothing else could be
// sent either (2026-11-16).
//
// Now a part past the end of a closed recording opens a continuation: a
// recording row of its own that holds the parts from that number on, numbered
// from 0, so it is completed, transcribed, listed and deleted like any other.
// The phone keeps using the id and the part numbers it knows.

// mobileRecordingContinuationID names the continuation of a recording that
// starts at firstPart. It is derived, so the same late part always lands in
// the same row.
func mobileRecordingContinuationID(recordingID string, firstPart int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("isoform-recording-continuation|%s|%d", recordingID, firstPart)))
	var id uuid.UUID
	copy(id[:], sum[:16])
	id[6] = (id[6] & 0x0f) | 0x40 // version 4, the only form the store's ids take
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

// rowForPartLocked finds the row that holds, or should hold, a part of a
// recording, and the part's number in that row. Without create it returns nil
// when that row does not exist yet. root is the row the phone names.
func (s *mobileRecordingStore) rowForPartLocked(root *mobileRecording, part int, create bool, now string) (*mobileRecording, int) {
	current, first := root, 0
	for {
		if current.Parts == 0 || part < first+current.Parts {
			return current, part - first
		}
		first += current.Parts
		id := mobileRecordingContinuationID(root.ID, first)
		next := s.recordings[id]
		if next == nil {
			if !create {
				return nil, 0
			}
			next = &mobileRecording{ID: id, DeviceID: root.DeviceID, Source: root.Source, Mime: root.Mime,
				State: mobileRecordingReceived, ReceivedAt: now, ContinuationOf: root.ID, FirstPart: first}
			return next, part - first
		}
		current = next
	}
}

// continuationIDsLocked lists the continuations of a recording.
func (s *mobileRecordingStore) continuationIDsLocked(id string) []string {
	var out []string
	for _, rec := range s.recordings {
		if rec.ContinuationOf == id {
			out = append(out, rec.ID)
		}
	}
	return out
}

// continuationIDs lists the continuations of a recording, so a delete of the
// recording takes them too.
func (s *mobileRecordingStore) continuationIDs(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil
	}
	return s.continuationIDsLocked(id)
}
