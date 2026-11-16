package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/platform/cmdutil"
)

// Nothing the watch or the phone recorded while observing was paused on this
// computer is kept (PROTOCOL.md 24.6). Pausing is the person saying "not
// this": the computer already discards its own audio during a pause, and a
// recording that arrived afterwards and showed up in the brief would be the
// moment they paused for. So a part recorded wholly inside a pause is never
// transcribed and its audio is deleted; a part that a pause only touches is
// transcribed for the words outside the pause, the words inside are dropped,
// and the paused minutes are cut out of its audio (with a second to spare on
// each side); if they cannot be cut, the whole file is deleted, because it
// still holds them. The pause log (observation_pauses.go) says when observing
// was paused.

type mobileRecordingPauseOverlap int

const (
	mobileRecordingPauseNone mobileRecordingPauseOverlap = iota
	mobileRecordingPausePartial
	mobileRecordingPauseFull
)

// mobileRecordingPauses reads the pause log for the recordings worker. With
// no store there is no pause log at all (tests, and a bridge that is not up).
var mobileRecordingPauses = func(a *App) ([]observationPause, error) {
	if a == nil || a.store == nil {
		return nil, nil
	}
	return a.observationPauses()
}

// mobileRecordingPauseCover says how much of [startMs, endMs] observing was
// paused for. A pause still open has no end.
func mobileRecordingPauseCover(startMs, endMs int64, pauses []observationPause) mobileRecordingPauseOverlap {
	if endMs < startMs {
		endMs = startMs
	}
	type span struct{ from, to int64 }
	var clipped []span
	for _, p := range pauses {
		from, to := p.Start, p.End
		if to == 0 || to > endMs {
			to = endMs
		}
		if from < startMs {
			from = startMs
		}
		if p.End != 0 && p.End <= startMs {
			continue
		}
		// A pause that begins the instant the stretch ends does not touch it.
		if from > endMs || from > to || (from == endMs && endMs > startMs) {
			continue
		}
		clipped = append(clipped, span{from, to})
	}
	if len(clipped) == 0 {
		return mobileRecordingPauseNone
	}
	sort.Slice(clipped, func(i, j int) bool { return clipped[i].from < clipped[j].from })
	// The whole stretch is paused when the pauses, joined, leave no gap in it.
	reach := startMs
	for _, c := range clipped {
		if c.from > reach {
			return mobileRecordingPausePartial
		}
		if c.to > reach {
			reach = c.to
		}
	}
	if reach >= endMs {
		return mobileRecordingPauseFull
	}
	return mobileRecordingPausePartial
}

// mobileRecordingPartPause is the overlap of one stored part with the pauses.
func mobileRecordingPartPause(part mobileRecordingPart, pauses []observationPause) mobileRecordingPauseOverlap {
	start, err := time.Parse(time.RFC3339, part.StartedAt)
	if err != nil {
		return mobileRecordingPauseNone
	}
	end := start.Add(time.Duration(part.DurationSeconds * float64(time.Second)))
	return mobileRecordingPauseCover(start.UnixMilli(), end.UnixMilli(), pauses)
}

// dropPausedMobileRecordingSegments removes the stretches of speech that
// touch a pause. A stretch that only begins or ends inside one goes whole:
// half a sentence from a paused minute is still from a paused minute.
func dropPausedMobileRecordingSegments(segments []mobileRecordingSegment, partStart time.Time, pauses []observationPause) []mobileRecordingSegment {
	if len(pauses) == 0 {
		return segments
	}
	kept := segments[:0:0]
	for _, seg := range segments {
		from := partStart.Add(seg.Start).UnixMilli()
		to := partStart.Add(seg.End).UnixMilli()
		if mobileRecordingPauseCover(from, to, pauses) == mobileRecordingPauseNone {
			kept = append(kept, seg)
		}
	}
	return kept
}

// mobileRecordingPauseMargin is cut away on each side of a pause as well, so
// nothing the codec smeared across the edge of a pause survives.
const mobileRecordingPauseMargin = time.Second

// mobileRecordingShortestKept: a sliver of audio between two pauses is not
// worth keeping.
const mobileRecordingShortestKept = time.Second

// mobileRecordingAudioSpan is a stretch of a part's audio, in seconds from
// its start.
type mobileRecordingAudioSpan struct{ From, To float64 }

// mobileRecordingKeptSpans are the stretches of [startMs, endMs] that no
// pause comes within margin of, as offsets from startMs.
func mobileRecordingKeptSpans(startMs, endMs int64, pauses []observationPause, margin time.Duration) []mobileRecordingAudioSpan {
	type span struct{ from, to int64 }
	pad := margin.Milliseconds()
	var cut []span
	for _, p := range pauses {
		from, to := p.Start-pad, p.End+pad
		if p.End == 0 || to > endMs {
			to = endMs
		}
		if from < startMs {
			from = startMs
		}
		if from >= to {
			continue
		}
		cut = append(cut, span{from, to})
	}
	sort.Slice(cut, func(i, j int) bool { return cut[i].from < cut[j].from })
	var kept []mobileRecordingAudioSpan
	keep := func(from, to int64) {
		if to-from >= mobileRecordingShortestKept.Milliseconds() {
			kept = append(kept, mobileRecordingAudioSpan{From: float64(from-startMs) / 1000, To: float64(to-startMs) / 1000})
		}
	}
	at := startMs
	for _, c := range cut {
		if c.from > at {
			keep(at, c.from)
		}
		if c.to > at {
			at = c.to
		}
	}
	if at < endMs {
		keep(at, endMs)
	}
	return kept
}

// mobileRecordingFFmpegPath finds the bundled ffmpeg; tests point it at theirs.
var mobileRecordingFFmpegPath = installedFFmpegPath

// mobileRecordingTrim writes to out the audio of in inside spans, joined, and
// nothing else. The cut is made on decoded samples (atrim), so it is exact;
// the result is encoded again as AAC.
var mobileRecordingTrim = func(ffmpegPath, in, out string, spans []mobileRecordingAudioSpan) ([]byte, error) {
	var filter strings.Builder
	for i, span := range spans {
		fmt.Fprintf(&filter, "[0:a]atrim=start=%.3f:end=%.3f,asetpts=PTS-STARTPTS[k%d];", span.From, span.To, i)
	}
	for i := range spans {
		fmt.Fprintf(&filter, "[k%d]", i)
	}
	fmt.Fprintf(&filter, "concat=n=%d:v=0:a=1[out]", len(spans))
	return cmdutil.Command(ffmpegPath, "-nostdin", "-v", "error", "-y", "-i", in,
		"-filter_complex", filter.String(), "-map", "[out]",
		"-ac", "1", "-ar", "16000", "-c:a", "aac", "-b:a", "24k", "-map_metadata", "-1", out).CombinedOutput()
}

// cutPausedAudio replaces a part's file with the same audio minus the paused
// minutes. It reports false when that could not be done; the caller then
// deletes the file, so a paused minute is never left on disk either way.
func cutPausedAudio(path string, part mobileRecordingPart, pauses []observationPause) bool {
	start, err := time.Parse(time.RFC3339, part.StartedAt)
	if err != nil {
		return false
	}
	end := start.Add(time.Duration(part.DurationSeconds * float64(time.Second)))
	spans := mobileRecordingKeptSpans(start.UnixMilli(), end.UnixMilli(), pauses, mobileRecordingPauseMargin)
	ffmpegPath := mobileRecordingFFmpegPath()
	if len(spans) == 0 || ffmpegPath == "" {
		return false
	}
	ext := filepath.Ext(path)
	tmp := strings.TrimSuffix(path, ext) + ".cut" + ext
	defer os.Remove(tmp)
	if out, err := mobileRecordingTrim(ffmpegPath, path, tmp, spans); err != nil {
		log.Printf("[recordings] paused minutes not cut from %s: %v: %s", filepath.Base(path), err, strings.TrimSpace(string(out)))
		return false
	}
	if info, err := os.Stat(tmp); err != nil || info.Size() == 0 {
		return false
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return false
	}
	// The rename replaces the file that still holds the paused minutes.
	return os.Rename(tmp, path) == nil
}

// marksOutsidePauses returns the part's marks that were not made during a
// pause. A mark is only a time, but a tap made while observing was paused is
// still something from the pause, so it goes with the audio.
func marksOutsidePauses(part mobileRecordingPart, pauses []observationPause) []float64 {
	start, err := time.Parse(time.RFC3339, part.StartedAt)
	if err != nil {
		return nil
	}
	var kept []float64
	for _, mark := range part.Marks {
		at := start.Add(time.Duration(mark * float64(time.Second))).UnixMilli()
		if mobileRecordingPauseCover(at, at+1, pauses) == mobileRecordingPauseNone {
			kept = append(kept, mark)
		}
	}
	return kept
}

// dropPausedAudio removes what a pause covers from the audio of a recording's
// parts, and marks them. A part recorded wholly inside a pause loses its
// file. With onlyFull the parts a pause only touches are left alone: they are
// still needed to transcribe the words outside the pause. Without it, those
// parts have the paused minutes cut out of their audio, or lose the file when
// that cannot be done. It returns how many parts it changed now.
func (s *mobileRecordingStore) dropPausedAudio(id string, pauses []observationPause, onlyFull bool) int {
	if len(pauses) == 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return 0
	}
	rec := s.recordings[id]
	if rec == nil {
		return 0
	}
	dropped := 0
	for i := range rec.Received {
		part := &rec.Received[i]
		if part.Paused || part.PausedCut {
			continue
		}
		overlap := mobileRecordingPartPause(*part, pauses)
		if overlap == mobileRecordingPauseNone || (onlyFull && overlap != mobileRecordingPauseFull) {
			continue
		}
		path := filepath.Join(s.dir, rec.ID, part.File)
		if overlap == mobileRecordingPausePartial && cutPausedAudio(path, *part, pauses) {
			part.PausedCut = true
			part.Context = nil
			part.Marks = marksOutsidePauses(*part, pauses)
			dropped++
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("[recordings] paused audio of %s part %d not removed: %v", rec.ID, part.Part, err)
			continue
		}
		part.Paused = true
		part.Context = nil
		part.Marks = marksOutsidePauses(*part, pauses)
		dropped++
	}
	if dropped > 0 {
		rec.UpdatedAt = mobileTimestamp(s.now())
		if err := s.saveLocked(); err != nil {
			log.Printf("[recordings] index save failed: %v", err)
		}
	}
	return dropped
}

// dropPausedMobileRecordingOnReceipt deletes, as soon as a part is stored,
// the audio of any part recorded wholly inside a pause. A pause log that
// cannot be read leaves the audio where it is: the worker reads the log again
// before it transcribes anything, and does not transcribe without it.
func (a *App) dropPausedMobileRecordingOnReceipt(store *mobileRecordingStore, id string) {
	source := ""
	if rec, ok := store.get(id, ""); ok {
		source = rec.Source
	}
	pauses, err := a.mobileRecordingDropSpans(source)
	if err != nil {
		log.Printf("[recordings] pause log not read for %s: %v", id, err)
		return
	}
	if n := store.dropPausedAudio(id, pauses, true); n > 0 {
		log.Printf("[recordings] %s: %d part(s) recorded while observing was paused were dropped", id, n)
	}
}
