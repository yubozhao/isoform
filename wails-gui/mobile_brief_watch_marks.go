package main

import (
	"log"
	"sort"
	"strings"
	"time"
)

// Moments the wearer marked on their watch, for the brief. A mark is the one
// thing in a day of listening the person pointed at on purpose, so the brief
// opens with it.
//
// One candidate for each watch recording that holds a mark in the brief's
// window, newest first, at most mobileBriefWatchMarkLimit. Its source is the
// recording's activity session, so the brief's own privacy checks (a deleted,
// private or withdrawn session) apply to it when the brief is bound and read.
//
// What was being said goes to the writer only when the marked line is the
// owner's own, and the writer is told not to quote it. Another person's words
// never go. Nothing of a transcript is logged.

const (
	mobileBriefWatchMarkSource = "activity_session"
	mobileBriefWatchMarkKind   = "marked_moment"
	mobileBriefWatchMarkLimit  = 3
	// mobileBriefWatchMarkWords bounds the owner's words handed to the writer.
	mobileBriefWatchMarkWords = 240
)

// mobileBriefWatchMarkTitles name the items for the writer and as evidence
// labels. They differ because the material keeps one item per title.
var mobileBriefWatchMarkTitles = [mobileBriefWatchMarkLimit]string{
	"A moment you marked on your watch",
	"Another moment you marked on your watch",
	"A third moment you marked on your watch",
}

// collectMobileBriefWatchMarks adds the window's marked moments to in.Marked.
// It fails closed like the recap: nothing while observing is paused or when
// the pause log cannot be read, nothing from a stretch that was paused, and
// nothing from a recording that was deleted or whose session may not be read.
func collectMobileBriefWatchMarks(a *App, in *mobileBriefInput) {
	if a == nil || in == nil || a.activity == nil || a.activity.store == nil || a.IsClosedEyesModeEnabled() {
		return
	}
	store := a.watchRecordingStore()
	if store == nil {
		return
	}
	pauses, err := a.observationPauses()
	if err != nil {
		log.Printf("[mobile-brief] watch marks skipped: the pause log cannot be read")
		return
	}
	if pausedAt(pauses, in.AsOf) {
		return
	}
	recordings, err := store.all()
	if err != nil {
		log.Printf("[mobile-brief] watch marks unavailable: %v", err)
		return
	}
	from, to := in.recapStart().UnixMilli(), in.AsOf.UnixMilli()
	inWindow := func(at int64) bool {
		return at >= from && at <= to && !pausedAt(pauses, time.UnixMilli(at))
	}
	var found []mobileBriefCandidate
	for _, rec := range recordings {
		if rec.State == mobileRecordingDeleted || rec.SessionID == "" {
			continue
		}
		var latest int64
		for _, at := range rec.marksAt() {
			if inWindow(at) && at > latest {
				latest = at
			}
		}
		if latest == 0 {
			continue
		}
		session, err := a.activity.store.GetSessionByID(rec.SessionID)
		if err != nil || session == nil || !a.activitySessionAllowsModelRead(a.activity.store, *session) {
			continue
		}
		candidate := mobileBriefCandidate{SourceKind: mobileBriefWatchMarkSource, SourceID: rec.SessionID,
			Status: "done", Kind: mobileBriefWatchMarkKind, At: time.UnixMilli(latest)}
		lines := a.watchRecordingLines(rec.SessionID)
		markWatchRecordingLines(lines, rec.marksAt())
		for i := len(lines) - 1; i >= 0; i-- {
			line := lines[i]
			if !line.Marked || line.Speaker != mobileRecordingSpeakerOwner || !inWindow(line.At) {
				continue
			}
			if words := mobileBriefWatchMarkOwnWords(line.Text); words != "" {
				candidate.writerDetail = "what you were saying then, to put in your own words and never quote: " + words
			}
			break
		}
		found = append(found, candidate)
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].At.After(found[j].At) })
	if len(found) > mobileBriefWatchMarkLimit {
		found = found[:mobileBriefWatchMarkLimit]
	}
	for i := range found {
		found[i].Title = mobileBriefWatchMarkTitles[i]
	}
	in.Marked = append(in.Marked, found...)
}

// mobileBriefWatchMarkOwnWords returns the owner's marked words when they are
// safe to hand to the writer, or nothing.
func mobileBriefWatchMarkOwnWords(text string) string {
	words := mobileBriefText(strings.TrimSpace(text), mobileBriefWatchMarkWords)
	if words == "" || !mobileBriefCopySafe(words) || redactThreadOutcomeText(words) != words {
		return ""
	}
	return words
}
