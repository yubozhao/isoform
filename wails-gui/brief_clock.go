package main

import "time"

// The brief's clock: one answer to "which day are this brief's words about".
//
// A brief's day words ("today", "yesterday", "tomorrow", a weekday) are written
// for one day: the brief's LocalDate, in the brief's own zone. The builder sets
// LocalDate from the writer's own day reference (briefDayRef: the slot's day
// for a brief made ahead, otherwise the day it was made), so it is the day the
// words were written for. Every step that turns those words into the reader's
// day anchors on briefWordsDay and nothing else: not the run record (another
// build's time under the same ID shifted every word on 2026-11-15), not AsOf
// (an early brief is made the evening before its day).
//
// Day words are compared as civil days in the brief's zone
// (mobileBriefCivilDays) and worded by mobileBriefDayWords.

// briefWordsDay is local midnight of the day brief's words are written for.
// A brief without a LocalDate (older documents, tests) falls back to its AsOf
// day; one with neither returns the zero time.
func briefWordsDay(brief MobileBriefV1) time.Time {
	loc := mobileBriefNarrativeLocation(brief)
	if day, err := time.ParseInLocation("2006-01-02", brief.LocalDate, loc); err == nil {
		return day
	}
	at := mobileBriefAsOfTime(brief)
	if at.IsZero() {
		return time.Time{}
	}
	local := at.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}
