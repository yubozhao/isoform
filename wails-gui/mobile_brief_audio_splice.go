package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"wails-gui/internal/platform/cmdutil"
)

// After the person removes a part of a built brief, the recording must stop
// saying it (TestMobileBriefAudioNeverSpeaksTextTheBriefNoLongerShows), and
// the brief keeps a recording (hub ruling, 2026-11-15). When the build's
// recording knows where each paragraph starts, the removed paragraphs are cut
// out of it in seconds, with no new voice pass; otherwise the brief is spoken
// again (renderMobileBriefAudio) and Today says the recording is updating.

// mobileBriefAudioCut is a span of the recording to leave out, in seconds.
type mobileBriefAudioCut struct{ start, end float64 }

// spliceMobileBriefAudio makes brief's recording from built's by cutting out
// the narrative paragraphs brief no longer has. It reports false when the cut
// cannot be proven to leave exactly brief's spoken words: no paragraph
// timings, a recording that is not the build's, words that changed rather
// than left, or a removed paragraph whose end the timings do not mark.
func spliceMobileBriefAudio(ctx context.Context, ffmpegPath, dir, lang string, brief, built MobileBriefV1) (mobileBriefAudioFile, bool) {
	cuts, kept, ok := mobileBriefAudioCuts(brief, built, lang)
	if !ok || ffmpegPath == "" {
		return mobileBriefAudioFile{}, false
	}
	path, mime, found := mobileBriefAudioPath(dir, built.BriefID)
	if !found || mime != built.Audio.Mime {
		return mobileBriefAudioFile{}, false
	}
	source, err := os.ReadFile(path)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(source)) != built.Audio.SHA256 {
		return mobileBriefAudioFile{}, false
	}
	data, err := cutAudioWithFFmpeg(ctx, ffmpegPath, path, mime, mobileBriefAudioKeptSpans(cuts, built.Audio.DurationSeconds))
	if err != nil || len(data) == 0 || len(data) > mobileBriefAudioMaxBytes {
		return mobileBriefAudioFile{}, false
	}
	ext := mobileBriefAudioExtM4A
	if mime == mobileBriefAudioMimeWAV {
		ext = mobileBriefAudioExtWAV
	}
	if err := writeMobileBriefFileAtomic(dir, brief.BriefID+ext, data); err != nil {
		return mobileBriefAudioFile{}, false
	}
	removed := 0.0
	for _, cut := range cuts {
		removed += cut.end - cut.start
	}
	return mobileBriefAudioFile{
		Path: filepath.Join(dir, brief.BriefID+ext),
		Audio: MobileBriefAudio{
			Status: mobileBriefAudioStatusOK, Mime: mime, Bytes: len(data),
			DurationSeconds: built.Audio.DurationSeconds - removed,
			SHA256:          fmt.Sprintf("%x", sha256.Sum256(data)), Paragraphs: kept,
		},
	}, true
}

// mobileBriefAudioCuts finds the spans of built's recording that speak the
// narrative paragraphs brief left out, and the paragraph starts of what is
// left. Every other word built's recording speaks must still be in brief.
func mobileBriefAudioCuts(brief, built MobileBriefV1, lang string) ([]mobileBriefAudioCut, []MobileBriefAudioParagraph, bool) {
	audio := built.Audio
	if audio == nil || audio.Status != mobileBriefAudioStatusOK || len(audio.Paragraphs) == 0 || audio.DurationSeconds <= 0 || audio.SHA256 == "" {
		return nil, nil, false
	}
	if l, _, _ := strings.Cut(audio.binding, ":"); audio.binding != "" && l != lang {
		return nil, nil, false
	}
	if brief.NarrativeStatus != "ok" || built.NarrativeStatus != "ok" || len(brief.Narrative) >= len(built.Narrative) || brief.YourWeek != nil || built.YourWeek != nil {
		return nil, nil, false
	}
	// The narrative paragraphs left, in order, are built's less the removed.
	removed := map[int]bool{}
	position := map[int]int{}
	next := 0
	for i, para := range built.Narrative {
		if next < len(brief.Narrative) && brief.Narrative[next].Text == para.Text {
			position[i] = next
			next++
			continue
		}
		removed[i] = true
	}
	if next != len(brief.Narrative) || len(removed) == 0 {
		return nil, nil, false
	}
	less := built
	less.Narrative = nil
	for i, para := range built.Narrative {
		if !removed[i] {
			less.Narrative = append(less.Narrative, para)
		}
	}
	speech, _ := mobileBriefSpeech(brief, lang)
	if left, _ := mobileBriefSpeech(less, lang); speech == "" || left != speech {
		return nil, nil, false
	}
	// Each removed paragraph runs from its own start to the start of the
	// next spoken segment, which must be a timed paragraph (or the end).
	full, _ := mobileBriefSpeech(built, lang)
	segments := mobileBriefSpokenSegments(full)
	timed := map[string]float64{}
	starts := map[int]float64{}
	for _, p := range audio.Paragraphs {
		var para MobileBriefPara
		switch p.Source {
		case mobileBriefAudioSourceNarrative:
			if p.Index < 0 || p.Index >= len(built.Narrative) {
				return nil, nil, false
			}
			para = built.Narrative[p.Index]
			starts[p.Index] = p.StartSeconds
		case mobileBriefAudioSourceWeekly:
			if p.Index < 0 || p.Index >= len(built.WeeklyRecap) {
				return nil, nil, false
			}
			para = built.WeeklyRecap[p.Index]
		default:
			return nil, nil, false
		}
		timed[mobileBriefParagraphSpoken(built, para, lang)] = p.StartSeconds
	}
	var cuts []mobileBriefAudioCut
	for i := range built.Narrative {
		if !removed[i] {
			continue
		}
		start, ok := starts[i]
		if !ok {
			return nil, nil, false
		}
		spoken := mobileBriefParagraphSpoken(built, built.Narrative[i], lang)
		at := -1
		for j, segment := range segments {
			if segment == spoken {
				if at >= 0 {
					return nil, nil, false // the same words twice: which to cut is unclear
				}
				at = j
			}
		}
		if at < 0 {
			return nil, nil, false
		}
		end := audio.DurationSeconds
		if at+1 < len(segments) {
			following, ok := timed[segments[at+1]]
			if !ok || following <= start {
				return nil, nil, false
			}
			end = following
		}
		cuts = append(cuts, mobileBriefAudioCut{start, end})
	}
	sort.Slice(cuts, func(a, b int) bool { return cuts[a].start < cuts[b].start })
	shift := func(at float64) float64 {
		gone := 0.0
		for _, cut := range cuts {
			if cut.end <= at {
				gone += cut.end - cut.start
			}
		}
		return at - gone
	}
	var kept []MobileBriefAudioParagraph
	for _, p := range audio.Paragraphs {
		if p.Source == mobileBriefAudioSourceNarrative {
			if removed[p.Index] {
				continue
			}
			p.Index = position[p.Index]
		}
		p.StartSeconds = shift(p.StartSeconds)
		if p.EndSeconds > 0 {
			p.EndSeconds = shift(p.EndSeconds)
		}
		kept = append(kept, p)
	}
	return cuts, kept, true
}

// mobileBriefParagraphSpoken is one paragraph as mobileBriefSpeech says it.
func mobileBriefParagraphSpoken(brief MobileBriefV1, para MobileBriefPara, lang string) string {
	return strings.TrimSpace(speechDisplayText(mobileBriefSpokenClocks(mobileBriefParagraphSpeech(brief, para, lang), lang), lang))
}

// mobileBriefAudioKeptSpans is the complement of cuts within [0, duration].
func mobileBriefAudioKeptSpans(cuts []mobileBriefAudioCut, duration float64) []mobileBriefAudioCut {
	var spans []mobileBriefAudioCut
	at := 0.0
	for _, cut := range cuts {
		if cut.start > at {
			spans = append(spans, mobileBriefAudioCut{at, cut.start})
		}
		at = cut.end
	}
	if at < duration {
		spans = append(spans, mobileBriefAudioCut{at, duration})
	}
	return spans
}

// cutAudioWithFFmpeg keeps spans of the recording at path and joins them, in
// the recording's own format (AAC in MP4 with the index up front, or WAV).
func cutAudioWithFFmpeg(ctx context.Context, ffmpegPath, path, mime string, spans []mobileBriefAudioCut) ([]byte, error) {
	if len(spans) == 0 {
		return nil, fmt.Errorf("nothing left to keep")
	}
	ctx, cancel := context.WithTimeout(ctx, mobileBriefAudioEncode)
	defer cancel()
	dir, err := os.MkdirTemp("", "isoform-brief-audio-cut-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	var filter strings.Builder
	for i, span := range spans {
		fmt.Fprintf(&filter, "[0:a]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS[a%d];", mobileBriefAudioSeconds(span.start), mobileBriefAudioSeconds(span.end), i)
	}
	for i := range spans {
		fmt.Fprintf(&filter, "[a%d]", i)
	}
	fmt.Fprintf(&filter, "concat=n=%d:v=0:a=1[out]", len(spans))
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", path, "-filter_complex", filter.String(), "-map", "[out]", "-vn", "-ac", "1"}
	output := filepath.Join(dir, "brief.wav")
	if mime == mobileBriefAudioMimeM4A {
		output = filepath.Join(dir, "brief.m4a")
		args = append(args, "-c:a", "aac", "-b:a", mobileBriefAudioBitrate, "-movflags", "+faststart", "-f", "mp4", output)
	} else {
		// A plain header (no LIST chunk), as the voice writes it.
		args = append(args, "-c:a", "pcm_s16le", "-map_metadata", "-1", "-bitexact", "-f", "wav", output)
	}
	cmd := cmdutil.CommandContext(ctx, ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg cut: %w", err)
	}
	return os.ReadFile(output)
}

func mobileBriefAudioSeconds(value float64) string { return strconv.FormatFloat(value, 'f', 3, 64) }
