package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wails-gui/internal/platform/cmdutil"
)

// Audio brief (docs/notifications-design.md section 7). When a brief is built,
// its narrative is read by the app's own voice and stored beside the brief as
// <briefId>.m4a (AAC through the bundled ffmpeg) or <briefId>.wav. The brief
// document carries the file's shape in Audio, so Today and the phone know
// there is something to play. A missing voice starts a background download;
// this brief reports that status and the next brief uses the installed voice.

const (
	mobileBriefAudioMimeM4A = "audio/mp4"
	mobileBriefAudioMimeWAV = "audio/wav"
	mobileBriefAudioExtM4A  = ".m4a"
	mobileBriefAudioExtWAV  = ".wav"
	// mobileBriefAudioBitrate is the AAC bitrate for one speaking voice.
	mobileBriefAudioBitrate = "48k"
	// mobileBriefAudioMaxBytes bounds a stored audio file so a runaway
	// synthesis never fills the disk or the relay.
	mobileBriefAudioMaxBytes          = 16 << 20
	mobileBriefAudioEncode            = 90 * time.Second
	mobileBriefAudioStatusOK          = "ok"
	mobileBriefAudioStatusDownloading = "downloading"
	// mobileBriefAudioStatusRendering marks a brief whose recording is being made right now: the computer
	// is speaking it. It carries no file, and the phone shows "Recording on its way" for it (P138).
	mobileBriefAudioStatusRendering = "rendering"
)

// MobileBriefAudio describes the spoken brief stored beside the document.
// Attachment is set once the file was sealed for the relay (section 17); a
// phone without it plays nothing and shows the text as before.
type MobileBriefAudio struct {
	Status          string                      `json:"status,omitempty"`
	StatusMessage   string                      `json:"statusMessage,omitempty"`
	Mime            string                      `json:"mime"`
	Bytes           int                         `json:"bytes"`
	DurationSeconds float64                     `json:"durationSeconds"`
	Attachment      *MobileBriefAudioAttachment `json:"attachment,omitempty"`
	// SHA256 binds a local file to this generation before a later upload.
	SHA256 string `json:"sha256,omitempty"`
	// Paragraphs says where each written paragraph starts in the recording,
	// when the reading follows them (PROTOCOL.md section 8.1).
	Paragraphs []MobileBriefAudioParagraph `json:"paragraphs,omitempty"`
	// binding is the text the recording speaks (mobileBriefAudioBinding).
	// It stays on this computer, beside the stored brief.
	binding string
}

// MobileBriefAudioParagraph is where one written paragraph starts in the
// recording: Source is the array (narrative or weeklyRecap), Index is
// zero-based into it.
type MobileBriefAudioParagraph struct {
	Source       string  `json:"source"`
	Index        int     `json:"index"`
	StartSeconds float64 `json:"startSeconds"`
}

const (
	mobileBriefAudioSourceNarrative = "narrative"
	mobileBriefAudioSourceWeekly    = "weeklyRecap"
)

// MobileBriefAudioAttachment names the sealed copy of the audio on the relay
// and carries what the phone needs to open it. The brief document that holds
// it is itself sealed end to end, so the relay never sees these fields.
type MobileBriefAudioAttachment struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Nonce string `json:"nonce"`
}

// mobileBriefSpeaker is what the audio step needs from the app: a voice for a
// language, its background download, and the ffmpeg binary when installed.
type mobileBriefSpeaker struct {
	// voiceReady reports whether the voice for a language is installed; it
	// never downloads.
	voiceReady func(lang string) bool
	// downloadVoice starts or joins the language's background asset download.
	// It returns false if the request is already cancelled.
	downloadVoice func(ctx context.Context, lang string) bool
	// synthesize returns a WAV for the text in the language.
	synthesize func(ctx context.Context, lang, text string) ([]byte, error)
	// ffmpeg returns the ffmpeg binary path, or "" when none is installed.
	ffmpeg func() string
	// language is the UI language the brief is read in ("en" or "zh-CN").
	language func() string
}

// voiceLang is the voice engine the brief is read with.
func (speaker mobileBriefSpeaker) voiceLang() string {
	if speaker.language == nil {
		return "en"
	}
	return mobileBriefVoiceLang(speaker.language())
}

// appMobileBriefSpeaker wires the speaker to the app's TTS runtime.
func appMobileBriefSpeaker(a *App) mobileBriefSpeaker {
	return mobileBriefSpeaker{
		voiceReady:    a.ttsModelPresentForLang,
		downloadVoice: a.startTtsVoiceDownload,
		synthesize: func(ctx context.Context, lang, text string) ([]byte, error) {
			engine, err := a.ensureTtsReadyForLang(ctx, lang)
			if err != nil {
				return nil, err
			}
			return engine.Synthesize(text)
		},
		ffmpeg:   installedFFmpegPath,
		language: a.currentUILanguage,
	}
}

// mobileBriefVoiceLang maps the UI language to a voice engine.
func mobileBriefVoiceLang(uiLanguage string) string {
	if briefChinese(uiLanguage) {
		return "zh"
	}
	return "en"
}

// mobileBriefSpokenText is the written narrative or weekly recap read by the
// voice, with display text normalized for speech.
func mobileBriefSpokenText(brief MobileBriefV1, lang string) string {
	text, _ := mobileBriefSpeech(brief, lang)
	return text
}

// mobileBriefSpeech reads the paragraphs delivered to the user in order. The
// display has already applied its publication and privacy rules; applying a
// second speech budget here would make a complete recording sound cut off.
func mobileBriefSpeech(brief MobileBriefV1, lang string) (string, bool) {
	if brief.YourWeek != nil {
		return yourWeekSpeech(brief, lang), false
	}
	if brief.NarrativeStatus != "ok" {
		return "", false
	}
	var parts []string
	question := morningBriefWeekQuestionSpeech(brief.WeekQuestion, lang)
	for i, paragraph := range brief.Narrative {
		text := mobileBriefSpokenClocks(mobileBriefParagraphSpeech(brief, paragraph, lang), lang)
		if question != "" && i == len(brief.Narrative)-1 && strings.ContainsAny(text, "?？") {
			parts = append(parts, question)
			question = ""
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	if question != "" {
		parts = append(parts, question)
	}
	return speechDisplayText(strings.Join(parts, "\n\n"), lang), false
}

func yourWeekSpeech(brief MobileBriefV1, lang string) string {
	var parts []string
	for _, paragraph := range brief.WeeklyRecap {
		if text := mobileBriefParagraphSpeech(brief, paragraph, lang); text != "" {
			parts = append(parts, text)
		}
	}
	return speechDisplayText(strings.Join(parts, "\n\n"), lang)
}

func mobileBriefParagraphSpeech(brief MobileBriefV1, paragraph MobileBriefPara, language string) string {
	// Only a paragraph citing exclusively observed recap Highlights opts in to
	// approximate activity totals. Mixed refs, plans and unknown refs stay exact.
	activityRefs := map[string]bool{}
	for _, item := range mobileBriefChiefSections(brief).Highlights {
		if item.Kind == "recap" {
			for _, ref := range item.Refs {
				activityRefs[ref] = true
			}
		}
	}
	activity := len(paragraph.Refs) > 0
	for _, ref := range paragraph.Refs {
		activity = activity && activityRefs[ref]
	}
	text := strings.TrimSpace(paragraph.Text)
	if activity {
		return normalizeBriefActivityText(text, language, mobileBriefTextReference(brief))
	}
	return normalizeBriefText(text, language, mobileBriefTextReference(brief), true)
}

func pick(lang, en, zh string) string {
	if lang == "zh" {
		return zh
	}
	return en
}

// mobileBriefAudioFile is the stored audio and its shape.
type mobileBriefAudioFile struct {
	Path  string
	Audio MobileBriefAudio
}

// renderMobileBriefAudio reads the brief aloud and stores the file beside the
// brief. It returns ok=false with a reason when there is nothing to say, no
// voice is installed, or synthesis fails. While a voice downloads, Audio
// carries status metadata without a file. The ffmpeg encode is optional:
// without it the WAV is kept.
func renderMobileBriefAudio(ctx context.Context, speaker mobileBriefSpeaker, dir string, brief MobileBriefV1) (mobileBriefAudioFile, bool, string) {
	if dir == "" || speaker.synthesize == nil || speaker.voiceReady == nil {
		return mobileBriefAudioFile{}, false, "no audio store"
	}
	lang := speaker.voiceLang()
	text, template := mobileBriefSpeech(brief, lang)
	if text == "" {
		return mobileBriefAudioFile{}, false, "nothing to say"
	}
	if !speaker.voiceReady(lang) {
		if speaker.downloadVoice != nil && speaker.downloadVoice(ctx, lang) {
			return mobileBriefAudioFile{Audio: MobileBriefAudio{
				Status:        mobileBriefAudioStatusDownloading,
				StatusMessage: pick(lang, "Voice downloading", "正在下载语音"),
			}}, false, mobileBriefAudioStatusDownloading
		}
		return mobileBriefAudioFile{}, false, "no voice installed"
	}
	wav, starts, err := synthesizeMobileBriefParagraphs(ctx, speaker, lang, text)
	if err != nil || len(wav) == 0 {
		return mobileBriefAudioFile{}, false, "synthesis failed"
	}
	var paragraphs []MobileBriefAudioParagraph
	if !template && starts != nil {
		paragraphs = mobileBriefParagraphStarts(brief, lang, mobileBriefSpokenSegments(text), starts)
	}
	duration, err := wavDurationSeconds(wav)
	if err != nil {
		return mobileBriefAudioFile{}, false, "synthesis produced no audio"
	}
	data, ext, mime := wav, mobileBriefAudioExtWAV, mobileBriefAudioMimeWAV
	if speaker.ffmpeg != nil {
		if ffmpegPath := speaker.ffmpeg(); ffmpegPath != "" {
			if encoded, encodeErr := encodeAACWithFFmpeg(ctx, ffmpegPath, wav); encodeErr == nil {
				data, ext, mime = encoded, mobileBriefAudioExtM4A, mobileBriefAudioMimeM4A
			} else {
				log.Printf("[mobile-brief] audio kept as WAV; the AAC encode did not complete")
			}
		}
	}
	if len(data) > mobileBriefAudioMaxBytes {
		return mobileBriefAudioFile{}, false, "audio too large"
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return mobileBriefAudioFile{}, false, "audio store unavailable"
	}
	name := brief.BriefID + ext
	if err := writeMobileBriefFileAtomic(dir, name, data); err != nil {
		return mobileBriefAudioFile{}, false, "audio could not be stored"
	}
	// Any earlier file of the other format for the same brief goes away.
	for _, other := range []string{mobileBriefAudioExtWAV, mobileBriefAudioExtM4A} {
		if other != ext {
			_ = os.Remove(filepath.Join(dir, brief.BriefID+other))
		}
	}
	return mobileBriefAudioFile{
		Path:  filepath.Join(dir, name),
		Audio: MobileBriefAudio{Status: mobileBriefAudioStatusOK, Mime: mime, Bytes: len(data), DurationSeconds: duration, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Paragraphs: paragraphs},
	}, true, mobileBriefAudioStatusOK
}

// encodeAACWithFFmpeg turns a WAV into an AAC file in a plain MP4 container,
// mono at the brief bitrate, with the index up front (+faststart) so
// AVAudioPlayer and podcast apps read the duration and seek. It goes through
// temporary files because a seekable container cannot be written to a pipe.
func encodeAACWithFFmpeg(ctx context.Context, ffmpegPath string, wav []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, mobileBriefAudioEncode)
	defer cancel()
	dir, err := os.MkdirTemp("", "isoform-brief-audio-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "brief.wav")
	output := filepath.Join(dir, "brief.m4a")
	if err := os.WriteFile(input, wav, 0600); err != nil {
		return nil, err
	}
	cmd := cmdutil.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", input,
		"-vn", "-ac", "1", "-c:a", "aac", "-b:a", mobileBriefAudioBitrate,
		"-movflags", "+faststart",
		"-f", "mp4", output,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	out, err := os.ReadFile(output)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("ffmpeg produced no audio")
	}
	return out, nil
}

// wavDurationSeconds reads the duration of a 16-bit PCM WAV as encodeWAV
// writes it: a 44-byte header followed by the samples.
func wavDurationSeconds(wav []byte) (float64, error) {
	if len(wav) < 44 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return 0, errors.New("not a WAV file")
	}
	channels := binary.LittleEndian.Uint16(wav[22:24])
	sampleRate := binary.LittleEndian.Uint32(wav[24:28])
	bitsPerSample := binary.LittleEndian.Uint16(wav[34:36])
	dataLen := binary.LittleEndian.Uint32(wav[40:44])
	if channels == 0 || sampleRate == 0 || bitsPerSample == 0 {
		return 0, errors.New("WAV header is incomplete")
	}
	if int(dataLen) > len(wav)-44 {
		dataLen = uint32(len(wav) - 44)
	}
	bytesPerSecond := float64(sampleRate) * float64(channels) * float64(bitsPerSample) / 8
	if bytesPerSecond == 0 || dataLen == 0 {
		return 0, errors.New("WAV has no samples")
	}
	return float64(dataLen) / bytesPerSecond, nil
}

// mobileBriefAudioPath returns the stored audio file for a brief, if any.
// The brief id is checked before it becomes a file name.
func mobileBriefAudioPath(dir, briefID string) (string, string, bool) {
	if dir == "" || !mobileBriefIDPattern.MatchString(briefID) {
		return "", "", false
	}
	for _, candidate := range []struct{ ext, mime string }{
		{mobileBriefAudioExtM4A, mobileBriefAudioMimeM4A},
		{mobileBriefAudioExtWAV, mobileBriefAudioMimeWAV},
	} {
		path := filepath.Join(dir, briefID+candidate.ext)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Size() > 0 {
			return path, candidate.mime, true
		}
	}
	return "", "", false
}
