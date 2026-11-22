package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"wails-gui/internal/asr"
	"wails-gui/internal/meetingdetect"
	"wails-gui/internal/platform/activitymon"
)

// A meeting transcript is built from the call's own recording when there is
// one: the stereo WAV the meeting audio writer keeps (left the microphone,
// right the system audio) is transcribed again from start to end, so speech
// is cut at its own pauses instead of at the live path's 5-second chunk
// boundaries, nothing the live path skipped under load is missing, and each
// side keeps its own channel. Without a recording (or an engine that can
// read one) the live speech events are used. Either way the pieces are
// merged into paragraphs per speaker.

const (
	// meetingParagraphGap: the same speaker after a pause longer than this
	// starts a new paragraph.
	meetingParagraphGap = 8 * time.Second
	// meetingParagraphSpan breaks a long monologue into paragraphs of about
	// this length, each with its own timestamp.
	meetingParagraphSpan = 90 * time.Second
	// meetingLiveEventSpan is how much audio one live speech event covers
	// (one recorder chunk).
	meetingLiveEventSpan = backgroundAudioChunkDuration
	// meetingEchoContainment: a microphone segment whose words nearly all
	// repeat what the system audio played at the same moment is the speakers
	// leaking into the microphone, not the user. Short segments are never
	// judged echo: a "Yes." over the far side's "Yes, we can ship Friday."
	// is the user's reply.
	meetingEchoContainment = 0.8
	meetingEchoSlack       = time.Second
	meetingEchoMinWords    = 4
	meetingEchoMinCJK      = 6
	// meetingWAVReadSeconds is how much audio one ReadSamples call returns.
	meetingWAVReadSeconds = 2
)

// Raw-track startup recovery ignores files created by this process.
var meetingTranscriptRecoveryCutoff = time.Now()

var (
	meetingTranscriptRecoveryMu   sync.Mutex
	meetingTranscriptRecoveryDone bool
)

// recoverUntranscribedMeetings writes the transcripts of recent calls whose
// recording was kept but whose transcript never was: transcribing a whole
// call takes a while, and the app may quit first. Retried while capture is
// running, only while automatic recording is on and in its current generation.
// Failed decodes stay pending, regardless of their age; concurrent attempts
// for the same session are suppressed by meetingTranscriptActive.
func (a *App) recoverUntranscribedMeetings() {
	a.recoverUntranscribedMeetingsWithContext(context.Background())
}

func (a *App) recoverUntranscribedMeetingsWithContext(ctx context.Context) {
	if !meetingTranscriptRecoveryMu.TryLock() {
		return
	}
	defer meetingTranscriptRecoveryMu.Unlock()
	epoch, enabled := a.automaticMeetingRecordingEpoch()
	if a.activity == nil || a.activity.store == nil || !enabled || a.speechTranscriber() == nil {
		return
	}
	meetingTranscriptRecoveryDone = true

	sessions, err := a.activity.store.GetUntranscribedMeetings()
	if err != nil {
		log.Printf("[transcript] recovery: load sessions failed: %v", err)
		return
	}
	for _, s := range sessions {
		if ctx.Err() != nil || !a.automaticMeetingRecordingEpochIsCurrent(epoch) || a.IsClosedEyesModeEnabled() {
			return
		}
		if !s.IsMeeting || s.AudioPath == "" || s.TranscriptPath != "" || s.TriageStatus == "protected" ||
			!meetingdetect.IsTranscriptionSupportedMeetingName(s.MeetingApp) || transcriptFileExists(s.ID) || transcriptAttempted(s.ID) {
			continue
		}
		if _, err := os.Stat(filepath.Join(a.activity.store.AudioDir(), s.AudioPath)); err != nil {
			continue
		}
		events, err := a.activity.store.GetSessionEvents(s.ID)
		if err != nil {
			continue
		}
		log.Printf("[transcript] recovery: writing the transcript of session %s", s.ID)
		// The recording's sidecar names its meeting, so its notes join that
		// meeting's entry instead of starting a second one.
		meetingID := readMeetingWAVMeta(a.activity.store.AudioDir(), s.AudioPath).MeetingID
		a.saveAutomaticMeetingTranscript(s, events, s.MeetingApp, epoch, meetingID)
	}
}

// transcriptAttemptDir records the sessions recovery already tried, so a
// call without speech is not transcribed again on every start.
const transcriptAttemptDir = ".attempted"

func transcriptAttempted(sessionID string) bool {
	dir, err := transcriptDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, transcriptAttemptDir, sessionID))
	return err == nil
}

func markTranscriptAttempted(sessionID string) {
	dir, err := transcriptDir()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, transcriptAttemptDir), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, transcriptAttemptDir, sessionID), nil, 0o600)
}

// transcriptFileExists reports whether a transcript was written for the
// session ("<date>-<app>-<session8>.md"), even if its row never learned.
func transcriptFileExists(sessionID string) bool {
	if len(sessionID) < 8 {
		return false
	}
	dir, err := transcriptDir()
	if err != nil {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*-"+sessionID[:8]+".md"))
	return len(matches) > 0
}

// meetingUtterance is one piece of speech in a call.
type meetingUtterance struct {
	At            time.Time
	End           time.Time
	Source        string // "microphone", "system_audio", or "" when unknown
	Text          string
	SpeakerID     string
	CallSpeakerID string
	LegacyText    bool      // parsed from the old transcript, not from audio
	Embedding     []float32 `json:"-"` // transient; never serialized or passed to a model
}

// meetingTranscriptUtterances returns the call's speech, from its recording
// when present; otherwise it uses live speech events. Failed full-audio passes
// remain pending instead of treating partial live events as complete.
func (a *App) meetingTranscriptUtterances(session ActivitySessionRow, events []activitymon.ActivityEvent) ([]meetingUtterance, bool) {
	if session.AudioPath != "" {
		// A temporarily unavailable engine must not turn partial live text into
		// the final transcript and prevent recovery of the complete recording.
		return a.meetingAudioUtterances(session)
	}
	// A reply may repeat the other track's words while both people speak.
	// Deduplicate within each track, never across their independent voices.
	bySource := map[string][]activitymon.ActivityEvent{}
	for _, event := range callSpeechEvents(events) {
		bySource[event.SpeechSource] = append(bySource[event.SpeechSource], event)
	}
	var utterances []meetingUtterance
	sources := make([]string, 0, len(bySource))
	for source := range bySource {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	for _, source := range sources {
		utterances = append(utterances, speechEventUtterances(bySource[source])...)
	}
	sort.SliceStable(utterances, func(i, j int) bool { return utterances[i].At.Before(utterances[j].At) })
	return utterances, true
}

// callSpeechEvents keeps the speech captured in the call: dictation and
// other speech in the same session is never part of its transcript. Rows
// saved before calls were told apart carry neither a call mark nor a
// dictation label on any speech; all their speech counts as the call's.
func callSpeechEvents(events []activitymon.ActivityEvent) []activitymon.ActivityEvent {
	marked := false
	for _, evt := range events {
		if evt.Kind == activitymon.EventSpeech && (evt.SpeechInCall || evt.SpeechDictationApp != "") {
			marked = true
			break
		}
	}
	if !marked {
		return events
	}
	out := make([]activitymon.ActivityEvent, 0, len(events))
	for _, evt := range events {
		if evt.Kind != activitymon.EventSpeech || evt.SpeechInCall {
			out = append(out, evt)
		}
	}
	return out
}

// meetingAudioUtterances transcribes the complete recording. A read/engine
// failure remains pending; a successful empty decode still archives the audio.
func (a *App) meetingAudioUtterances(session ActivitySessionRow) ([]meetingUtterance, bool) {
	if session.AudioPath == "" || a.activity == nil || a.activity.store == nil {
		return nil, false
	}
	path := filepath.Join(a.activity.store.AudioDir(), session.AudioPath)
	if _, err := os.Stat(path); err != nil {
		return nil, false
	}
	// Storage may have compressed the recording (Opus); the engine reads
	// PCM, so a compressed one is decoded to a temporary WAV first.
	wavPath, cleanup, err := decodeMeetingAudioToWAV(installedFFmpegPath(), path)
	if err != nil {
		log.Printf("[transcript] recording of session %s cannot be read, leaving transcript pending: %v", session.ID, err)
		return nil, false
	}
	defer cleanup()
	live, _ := a.speechTranscriber().(asr.LongFormTranscriber)
	transcriber, release := a.afterCallTranscriber()
	defer release()
	if transcriber == nil {
		return nil, false
	}
	engine := meetingTranscribedByAfterCall
	if transcriber == live {
		engine = meetingTranscribedByLive
	}
	started := time.Now()
	monoSource := readMeetingWAVMeta(a.activity.store.AudioDir(), session.AudioPath).MonoSource
	var utterances []meetingUtterance
	diarizer, diarizerErr := newCallDiarizer(callSpeakerModelPaths())
	if diarizerErr == nil {
		utterances, err = transcribeDiarizedMeetingRecording(transcriber, wavPath, diarizer, monoSource)
		diarizer.Close()
	} else {
		err = errCallModelsUnavailable
	}
	if errors.Is(err, errCallModelsUnavailable) {
		utterances, err = transcribeMeetingRecordingSources(transcriber, wavPath, false, monoSource)
	}
	if err != nil {
		log.Printf("[transcript] recording of session %s not transcribed, leaving transcript pending: %v", session.ID, err)
		return nil, false
	}
	for i := range utterances {
		if utterances[i].Source == "microphone" {
			utterances[i].SpeakerID = "you"
		}
	}
	// The recording owns the whole call, even when this activity row is
	// only its final rotation. Do not clip the opening to the row's start.

	log.Printf("[transcript] transcribed recording of session %s: %d segments in %s", session.ID, len(utterances), time.Since(started).Round(time.Millisecond))
	recordMeetingTranscribedBy(a.activity.store.AudioDir(), session.AudioPath, engine)
	return utterances, true
}

// meetingRecordingSourceSessions is the complete capture provenance of a
// call, or nil when the read gate cannot establish it: a recording without a
// readable, complete sidecar and no transcript that already declares its
// sources, or one that is not readable now. A transcript
// then declares no sources, so the gate keeps deciding from the recording at
// each read; naming the session alone would open it through the session row.
// Dev Tools' synthetic calls have no activity store and keep their session.
func (a *App) meetingRecordingSourceSessions(session ActivitySessionRow) []string {
	if a.activity == nil || a.activity.store == nil {
		return []string{session.ID}
	}
	ids, err := a.activity.store.transcriptSourceIDs(session, "")
	if err != nil {
		return nil
	}
	return ids
}

// speechTranscriber is the running speech engine, if any.
func (a *App) speechTranscriber() asr.Transcriber {
	if a == nil {
		return nil
	}
	whisper := a.getWhisperState()
	if whisper == nil {
		return nil
	}
	whisper.mu.Lock()
	defer whisper.mu.Unlock()
	if whisper.server == nil || !whisper.server.IsRunning() {
		return nil
	}
	return whisper.server
}

// transcribeMeetingRecording transcribes a meeting WAV channel by channel.
// The wall-clock start comes from the file name (meetingAudioStartFromPath).
func transcribeMeetingRecording(transcriber asr.LongFormTranscriber, path string) ([]meetingUtterance, error) {
	return transcribeMeetingRecordingSources(transcriber, path, true)
}

func transcribeMeetingRecordingSources(transcriber asr.LongFormTranscriber, path string, filterEcho bool, monoSource ...string) ([]meetingUtterance, error) {
	start, ok := meetingAudioStartFromPath(path)
	if !ok {
		return nil, fmt.Errorf("no start time in %s", filepath.Base(path))
	}
	info, err := readMeetingWAVInfo(path)
	if err != nil {
		return nil, err
	}
	sources := []string{""}
	if len(monoSource) > 0 && (monoSource[0] == "microphone" || monoSource[0] == "system_audio") {
		sources[0] = monoSource[0]
	}
	if info.channels == 2 {
		sources = []string{string(backgroundListeningSourceMicrophone), string(backgroundListeningSourceSystem)}
	}

	bySource := make([][]meetingUtterance, len(sources))
	for channel, source := range sources {
		reader, err := openMeetingWAVChannel(path, info, channel)
		if err != nil {
			return nil, err
		}
		engineSource := source
		if engineSource == "" {
			engineSource = string(backgroundListeningSourceMicrophone)
		}
		segments, err := transcriber.TranscribeLongForm(reader, engineSource)
		reader.Close()
		if err != nil {
			return nil, fmt.Errorf("channel %d: %w", channel, err)
		}
		for _, seg := range segments {
			bySource[channel] = append(bySource[channel], meetingUtterance{
				At:     start.Add(seg.Start),
				End:    start.Add(seg.End),
				Source: source,
				Text:   strings.TrimSpace(seg.Text),
			})
		}
	}

	var utterances []meetingUtterance
	if len(sources) == 2 {
		mic := bySource[0]
		if filterEcho {
			mic = dropMicrophoneEcho(mic, bySource[1])
		}
		utterances = append(mic, bySource[1]...)
	} else {
		utterances = bySource[0]
	}
	sort.SliceStable(utterances, func(i, j int) bool { return utterances[i].At.Before(utterances[j].At) })
	return utterances, nil
}

// dropMicrophoneEcho removes microphone segments that repeat what the system
// audio played at the same time: on speakers instead of headphones the
// microphone hears the other side too.
func dropMicrophoneEcho(mic, system []meetingUtterance) []meetingUtterance {
	out := make([]meetingUtterance, 0, len(mic))
	for _, m := range mic {
		if !longEnoughForEcho(m.Text) {
			out = append(out, m)
			continue
		}
		from, to := m.At.Add(-meetingEchoSlack), m.End.Add(meetingEchoSlack)
		var heard []string
		for _, s := range system {
			if s.At.Before(to) && s.End.After(from) {
				heard = append(heard, wordsSpokenBetween(s, from, to)...)
			}
		}
		if len(heard) > 0 && tokenContainment(transcriptTokens(m.Text), heard) >= meetingEchoContainment {
			continue
		}
		out = append(out, m)
	}
	return out
}

// longEnoughForEcho reports whether a segment has enough words to be judged
// echo at all.
func longEnoughForEcho(text string) bool {
	words, cjk := 0, 0
	for _, tok := range transcriptTokens(text) {
		if r, _ := utf8.DecodeRuneInString(tok); isTranscriptCJK(r) {
			cjk++
		} else {
			words++
		}
	}
	return words >= meetingEchoMinWords || cjk >= meetingEchoMinCJK
}

// meetingEchoWordPad widens the words taken from a system segment around a
// microphone reply.
const meetingEchoWordPad = 2

// wordsSpokenBetween returns the words of a segment that fall between from
// and to, assuming they are spread evenly over the segment: a long system
// segment that only touches a microphone reply lends it only the words
// said around it.
func wordsSpokenBetween(u meetingUtterance, from, to time.Time) []string {
	tokens := transcriptTokens(u.Text)
	span := u.End.Sub(u.At)
	if span <= 0 || len(tokens) == 0 {
		return tokens
	}
	startOffset, endOffset := from.Sub(u.At), to.Sub(u.At)
	if startOffset < 0 {
		startOffset = 0
	}
	if endOffset > span {
		endOffset = span
	}
	if endOffset <= startOffset {
		return nil
	}
	// Speech is not evenly paced: a couple of words either side absorb it.
	first := int(int64(len(tokens))*int64(startOffset)/int64(span)) - meetingEchoWordPad
	last := int((int64(len(tokens))*int64(endOffset)+int64(span)-1)/int64(span)) + meetingEchoWordPad
	if first < 0 {
		first = 0
	}
	if last > len(tokens) {
		last = len(tokens)
	}
	return tokens[first:last]
}

// transcriptTokenContainment is the share of a's tokens that also occur in
// b. Han, kana and hangul count per character, everything else per word.
func transcriptTokenContainment(a, b string) float64 {
	return tokenContainment(transcriptTokens(a), transcriptTokens(b))
}

func tokenContainment(ta, tb []string) float64 {
	if len(ta) == 0 {
		return 0
	}
	inB := map[string]bool{}
	for _, tok := range tb {
		inB[tok] = true
	}
	shared := 0
	for _, tok := range ta {
		if inB[tok] {
			shared++
		}
	}
	return float64(shared) / float64(len(ta))
}

func transcriptTokens(s string) []string {
	var tokens []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			tokens = append(tokens, word.String())
			word.Reset()
		}
	}
	for _, r := range strings.ToLower(s) {
		if isTranscriptCJK(r) {
			flush()
			tokens = append(tokens, string(r))
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			word.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

func isTranscriptCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// speechEventUtterances turns the live speech events into utterances,
// oldest first, with near-duplicates from the two sources collapsed.
func speechEventUtterances(events []activitymon.ActivityEvent) []meetingUtterance {
	sorted := append([]activitymon.ActivityEvent(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Timestamp < sorted[j].Timestamp })
	deduped := dedupSpeechEvents(sorted)
	out := make([]meetingUtterance, 0, len(deduped))
	for _, evt := range deduped {
		at := time.UnixMilli(evt.Timestamp)
		out = append(out, meetingUtterance{
			At:     at,
			End:    at.Add(meetingLiveEventSpan),
			Source: evt.SpeechSource,
			Text:   evt.SpeechText,
		})
	}
	return out
}

// transcriptParagraph is one speaker's stretch of speech.
type transcriptParagraph struct {
	At        time.Time
	End       time.Time
	Source    string
	Text      string
	SpeakerID string
}

// buildTranscriptParagraphs merges consecutive utterances of the same
// speaker into paragraphs. A new paragraph starts when the speaker changes,
// after a long pause, or once a monologue has run for meetingParagraphSpan.
func buildTranscriptParagraphs(utterances []meetingUtterance) []transcriptParagraph {
	sorted := append([]meetingUtterance(nil), utterances...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	var out []transcriptParagraph
	for _, u := range sorted {
		text := strings.TrimSpace(u.Text)
		if text == "" {
			continue
		}
		end := u.End
		if end.Before(u.At) {
			end = u.At
		}
		if n := len(out); n > 0 {
			cur := &out[n-1]
			if cur.Source == u.Source && cur.SpeakerID == u.SpeakerID && !u.At.After(cur.End.Add(meetingParagraphGap)) && u.At.Sub(cur.At) < meetingParagraphSpan {
				cur.Text = joinTranscriptText(cur.Text, text)
				if end.After(cur.End) {
					cur.End = end
				}
				continue
			}
		}
		out = append(out, transcriptParagraph{At: u.At, End: end, Source: u.Source, SpeakerID: u.SpeakerID, Text: text})
	}
	return out
}

// joinTranscriptText joins two pieces of speech, without a space between
// two CJK characters.
func joinTranscriptText(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	last, _ := utf8.DecodeLastRuneInString(a)
	first, _ := utf8.DecodeRuneInString(b)
	if (isTranscriptCJK(last) || isCJKPunctuation(last)) && isTranscriptCJK(first) {
		return a + b
	}
	return a + " " + b
}

func isCJKPunctuation(r rune) bool {
	return (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF)
}

// transcriptHasBothSides reports whether the user and the other side both
// spoke; only then do the speaker labels say anything.
func transcriptHasBothSides(paragraphs []transcriptParagraph) bool {
	var mic, system bool
	for _, p := range paragraphs {
		switch p.Source {
		case string(backgroundListeningSourceMicrophone):
			mic = true
		case string(backgroundListeningSourceSystem):
			system = true
		default:
		}
	}
	return mic && system
}

// writeTranscriptParagraphs renders paragraphs as "[HH:MM:SS] Me: text"
// lines, one per paragraph, and returns the word count. Labels appear only
// when both sides spoke.
func writeTranscriptParagraphs(b *strings.Builder, paragraphs []transcriptParagraph) int {
	labeled := transcriptHasBothSides(paragraphs)
	words := 0
	for _, p := range paragraphs {
		ts := p.At.Format("15:04:05")
		speaker := ""
		if labeled {
			speaker = speakerLabel(p.Source)
		}
		if p.SpeakerID == "you" {
			speaker = "You"
		} else if strings.HasPrefix(p.SpeakerID, "speaker-") {
			speaker = "Speaker " + strings.TrimPrefix(p.SpeakerID, "speaker-")
		}
		if speaker != "" {
			fmt.Fprintf(b, "[%s] %s: %s\n\n", ts, speaker, p.Text)
		} else {
			fmt.Fprintf(b, "[%s] %s\n\n", ts, p.Text)
		}
		words += len(strings.Fields(p.Text))
	}
	return words
}

// meetingWAVInfo is the layout of a meeting WAV's PCM data.
type meetingWAVInfo struct {
	channels   int
	dataOffset int64
	dataSize   int64
}

// readMeetingWAVInfo reads the header of a 16 kHz, 16-bit PCM WAV.
func readMeetingWAVInfo(path string) (meetingWAVInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return meetingWAVInfo{}, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return meetingWAVInfo{}, err
	}
	r := bufio.NewReader(f)
	var riff [12]byte
	if _, err := io.ReadFull(r, riff[:]); err != nil {
		return meetingWAVInfo{}, fmt.Errorf("read WAV header: %w", err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return meetingWAVInfo{}, errors.New("not a RIFF/WAVE file")
	}
	var info meetingWAVInfo
	var fmtFound bool
	offset := int64(12)
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return meetingWAVInfo{}, errors.New("missing data chunk")
		}
		offset += 8
		id := string(hdr[0:4])
		size := int64(binary.LittleEndian.Uint32(hdr[4:8]))
		switch id {
		case "fmt ":
			if size < 16 {
				return meetingWAVInfo{}, fmt.Errorf("fmt chunk too small: %d", size)
			}
			body := make([]byte, size)
			if _, err := io.ReadFull(r, body); err != nil {
				return meetingWAVInfo{}, err
			}
			if format := binary.LittleEndian.Uint16(body[0:2]); format != 1 {
				return meetingWAVInfo{}, fmt.Errorf("unsupported audio format %d", format)
			}
			info.channels = int(binary.LittleEndian.Uint16(body[2:4]))
			if rate := binary.LittleEndian.Uint32(body[4:8]); rate != meetingAudioSampleRate {
				return meetingWAVInfo{}, fmt.Errorf("unsupported sample rate %d", rate)
			}
			if bits := binary.LittleEndian.Uint16(body[14:16]); bits != 16 {
				return meetingWAVInfo{}, fmt.Errorf("unsupported bit depth %d", bits)
			}
			if info.channels != 1 && info.channels != 2 {
				return meetingWAVInfo{}, fmt.Errorf("unsupported channel count %d", info.channels)
			}
			fmtFound = true
		case "data":
			if !fmtFound {
				return meetingWAVInfo{}, errors.New("data chunk before fmt chunk")
			}
			info.dataOffset = offset
			info.dataSize = size
			// A header written before the data was complete under-reports
			// its size; the file length is the truth.
			if remaining := stat.Size() - offset; remaining < info.dataSize || info.dataSize == 0 {
				info.dataSize = remaining
			}
			return info, nil
		default:
			if _, err := r.Discard(int(size + size%2)); err != nil {
				return meetingWAVInfo{}, err
			}
		}
		offset += size + size%2
	}
}

// meetingAudioSampleRate is the rate every recorder writes and the speech
// engine reads.
const meetingAudioSampleRate = 16000

// meetingWAVChannelReader reads one channel of a meeting WAV as float32
// samples in [-1, 1].
type meetingWAVChannelReader struct {
	f         *os.File
	r         *bufio.Reader
	channels  int
	channel   int
	remaining int64
	buf       []byte
}

func openMeetingWAVChannel(path string, info meetingWAVInfo, channel int) (*meetingWAVChannelReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(info.dataOffset, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	frame := 2 * info.channels
	return &meetingWAVChannelReader{
		f:         f,
		r:         bufio.NewReaderSize(f, 1<<16),
		channels:  info.channels,
		channel:   channel,
		remaining: info.dataSize / int64(frame) * int64(frame),
		buf:       make([]byte, meetingWAVReadSeconds*meetingAudioSampleRate*frame),
	}, nil
}

func (c *meetingWAVChannelReader) ReadSamples() ([]float32, error) {
	if c.remaining <= 0 {
		return nil, io.EOF
	}
	frame := 2 * c.channels
	want := int64(len(c.buf))
	if c.remaining < want {
		want = c.remaining
	}
	n, err := io.ReadFull(c.r, c.buf[:want])
	n = n / frame * frame
	c.remaining -= int64(n)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n == 0 {
		return nil, io.EOF
	}
	samples := make([]float32, n/frame)
	const scale = float32(1.0 / 32768.0)
	for i := range samples {
		at := i*frame + 2*c.channel
		samples[i] = float32(int16(binary.LittleEndian.Uint16(c.buf[at:at+2]))) * scale
	}
	if err != nil {
		c.remaining = 0
	}
	return samples, nil
}

func (c *meetingWAVChannelReader) Close() {
	if c != nil && c.f != nil {
		c.f.Close()
	}
}
