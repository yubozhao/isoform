package main

import (
	"log"
	"time"
)

// mobileBriefNotifyHoldMax caps how long a brief's phone push waits for its
// recording (PROTOCOL.md 8.2). The text package reaches the phone at once and
// silently; the push goes with the package that plays, so a tap on the
// notification opens a brief that has its audio.
var mobileBriefNotifyHoldMax = 2 * time.Minute

// owePhoneNotify holds a brief's phone push until its recording is published,
// the recording step ends without one, or mobileBriefNotifyHoldMax passes.
// One push is owed at a time: a newer brief's push replaces an older one's.
func (s *mobileBriefService) owePhoneNotify(brief MobileBriefV1) {
	s.mu.Lock()
	s.notifyOwed = map[string]string{brief.BriefID: brief.AsOf}
	s.mu.Unlock()
	time.AfterFunc(mobileBriefNotifyHoldMax, func() {
		if !s.beginWork() {
			return
		}
		defer s.wg.Done()
		s.payPhoneNotify(brief)
	})
}

// textPhoneNotify is the push flag for a brief's first, text-only package:
// a written brief without its recording goes to the phone silently and owes
// its push to the recording step.
func (s *mobileBriefService) textPhoneNotify(brief MobileBriefV1, notify bool) bool {
	if !notify || brief.NarrativeStatus != "ok" || mobileBriefHasRecording(brief) {
		return notify
	}
	s.owePhoneNotify(brief)
	return false
}

// takePhoneNotify reports whether a push is owed for this version of the
// brief, and clears it so it is sent once.
func (s *mobileBriefService) takePhoneNotify(brief MobileBriefV1) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	asOf, owed := s.notifyOwed[brief.BriefID]
	if !owed || asOf != brief.AsOf {
		return false
	}
	delete(s.notifyOwed, brief.BriefID)
	return true
}

// payPhoneNotify sends a still-owed push with the brief as stored now.
func (s *mobileBriefService) payPhoneNotify(brief MobileBriefV1) {
	if s.app == nil || !s.takePhoneNotify(brief) {
		return
	}
	s.deliverStoredBrief(brief, func(current MobileBriefV1) {
		if err := s.publish(current, true); err != nil {
			log.Printf("[mobile-brief] paired-device delivery of the brief's push did not complete")
		}
	})
}

// mobileBriefHasRecording says whether a brief already carries the recording
// the phone downloads.
func mobileBriefHasRecording(brief MobileBriefV1) bool {
	return brief.Audio != nil && brief.Audio.Attachment != nil
}
