package main

import (
	"math"
	"sort"
	"time"
)

// Marked moments (PROTOCOL.md 24.9). While the watch records, the wearer can
// mark a moment: "this matters". A mark is only a time, seconds from the
// start of the part it was made in. It travels with the part, is stored with
// it, and is deleted with the recording. The computer shows which words were
// being said when the wearer marked.

const (
	// mobileRecordingMaxMarks bounds the marks of one part.
	mobileRecordingMaxMarks = 60
	// A mark points at the stretch of speech that began at most this long
	// before it; with none, at the first one that begins this soon after.
	mobileRecordingMarkLookBack  = 60 * time.Second
	mobileRecordingMarkLookAhead = 30 * time.Second
)

// sanitizeMobileRecordingMarks keeps the marks that fall inside the part, in
// order, without repeats. Bad values are dropped; they never refuse the audio.
func sanitizeMobileRecordingMarks(marks []float64, durationSeconds float64) []float64 {
	var kept []float64
	for _, mark := range marks {
		if math.IsNaN(mark) || math.IsInf(mark, 0) || mark < 0 || mark > durationSeconds {
			continue
		}
		kept = append(kept, math.Round(mark*10)/10)
	}
	sort.Float64s(kept)
	out := kept[:0]
	for i, mark := range kept {
		if i > 0 && mark == kept[i-1] {
			continue
		}
		out = append(out, mark)
	}
	if len(out) > mobileRecordingMaxMarks {
		out = out[:mobileRecordingMaxMarks]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// marksAt returns a recording's marks as clock times, in Unix milliseconds.
func (r *mobileRecording) marksAt() []int64 {
	var out []int64
	for _, part := range r.Received {
		start, err := time.Parse(time.RFC3339, part.StartedAt)
		if err != nil {
			continue
		}
		for _, mark := range part.Marks {
			out = append(out, start.Add(time.Duration(mark*float64(time.Second))).UnixMilli())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// markWatchRecordingLines sets Marked on the line each mark points at: the
// last line that began at or before the mark, within the look-back; else the
// first line that begins after it, within the look-ahead. Lines are in time
// order.
func markWatchRecordingLines(lines []WatchRecordingLine, marks []int64) {
	for _, mark := range marks {
		before, after := -1, -1
		for i, line := range lines {
			if line.At <= mark {
				before = i
				continue
			}
			after = i
			break
		}
		switch {
		case before >= 0 && mark-lines[before].At <= mobileRecordingMarkLookBack.Milliseconds():
			lines[before].Marked = true
		case after >= 0 && lines[after].At-mark <= mobileRecordingMarkLookAhead.Milliseconds():
			lines[after].Marked = true
		}
	}
}

// dropMarks removes the marks of every part. They go whenever the audio goes.
func (r *mobileRecording) dropMarks() {
	for i := range r.Received {
		r.Received[i].Marks = nil
	}
}
