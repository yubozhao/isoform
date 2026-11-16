package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// splicedBriefFixture is a built brief of three paragraphs whose recording
// speaks them for 1 s, 2 s and 1 s, in the given format, made by ffmpeg.
func splicedBriefFixture(t *testing.T, dir, mime string) (MobileBriefV1, string) {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		if ffmpegPath = findBundledFFmpeg(); ffmpegPath == "" {
			t.Skip("no ffmpeg on this computer")
		}
	}
	ext, codec := mobileBriefAudioExtWAV, []string{"-c:a", "pcm_s16le", "-f", "wav"}
	if mime == mobileBriefAudioMimeM4A {
		ext, codec = mobileBriefAudioExtM4A, []string{"-c:a", "aac", "-b:a", mobileBriefAudioBitrate, "-movflags", "+faststart", "-f", "mp4"}
	}
	path := filepath.Join(dir, "2026-11-15-morning"+ext)
	args := append([]string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=4:sample_rate=24000", "-ac", "1", "-map_metadata", "-1", "-bitexact"}, codec...)
	if out, err := exec.Command(ffmpegPath, append(args, path)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	built := MobileBriefV1{BriefID: "2026-11-15-morning", AsOf: "2026-11-15T06:51:21Z", NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{{Text: "The first paragraph."}, {Text: "The withdrawn paragraph."}, {Text: "The last paragraph."}},
		Audio: &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Mime: mime, Bytes: len(data), DurationSeconds: 4, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)),
			Paragraphs: []MobileBriefAudioParagraph{{Source: mobileBriefAudioSourceNarrative, Index: 0, StartSeconds: 0, EndSeconds: 0.55}, {Source: mobileBriefAudioSourceNarrative, Index: 1, StartSeconds: 1, EndSeconds: 2.55}, {Source: mobileBriefAudioSourceNarrative, Index: 2, StartSeconds: 3, EndSeconds: 4}}},
	}
	return built, ffmpegPath
}

// Hub ruling, 2026-11-15: after a removal, a recording that marks where each
// paragraph starts is cut, in seconds, so it never says the removed words.
func TestSpliceCutsTheRemovedParagraphOutOfTheBuildRecording(t *testing.T) {
	for _, mime := range []string{mobileBriefAudioMimeWAV, mobileBriefAudioMimeM4A} {
		t.Run(mime, func(t *testing.T) {
			dir := t.TempDir()
			built, ffmpegPath := splicedBriefFixture(t, dir, mime)
			amended := built
			amended.Narrative = []MobileBriefPara{built.Narrative[0], built.Narrative[2]}
			amended.Audio = nil
			cut, ok := spliceMobileBriefAudio(context.Background(), ffmpegPath, dir, "en", amended, built)
			if !ok {
				t.Fatal("the removed paragraph was not cut")
			}
			if math.Abs(cut.Audio.DurationSeconds-2) > 0.01 || cut.Audio.SHA256 == built.Audio.SHA256 || cut.Audio.Bytes == 0 {
				t.Fatalf("cut recording: %+v", cut.Audio)
			}
			want := []MobileBriefAudioParagraph{{Source: mobileBriefAudioSourceNarrative, Index: 0, StartSeconds: 0, EndSeconds: 0.55}, {Source: mobileBriefAudioSourceNarrative, Index: 1, StartSeconds: 1, EndSeconds: 2}}
			if fmt.Sprint(cut.Audio.Paragraphs) != fmt.Sprint(want) {
				t.Fatalf("paragraph starts = %v, want %v", cut.Audio.Paragraphs, want)
			}
			if mime == mobileBriefAudioMimeWAV {
				data, _ := os.ReadFile(cut.Path)
				if got, err := wavDurationSeconds(data); err != nil || math.Abs(got-2) > 0.01 {
					t.Fatalf("the stored file lasts %.3f s, want 2", got)
				}
			}
		})
	}
}

// What the cut cannot prove is left to a new voice pass: words that changed
// rather than left, a recording without paragraph starts, or a recording
// that is no longer the build's.
func TestSpliceRefusesWhatItCannotProve(t *testing.T) {
	dir := t.TempDir()
	built, ffmpegPath := splicedBriefFixture(t, dir, mobileBriefAudioMimeWAV)
	amended := built
	amended.Narrative = []MobileBriefPara{built.Narrative[0], built.Narrative[2]}
	reworded := built
	reworded.Narrative = []MobileBriefPara{{Text: "The first paragraph, reworded."}, built.Narrative[2]}
	untimed := built
	audio := *built.Audio
	audio.Paragraphs = nil
	untimed.Audio = &audio
	other := built
	otherAudio := *built.Audio
	otherAudio.SHA256 = "another-recording"
	other.Audio = &otherAudio
	for name, c := range map[string]struct{ brief, built MobileBriefV1 }{
		"reworded": {reworded, built}, "no paragraph starts": {amended, untimed}, "not the build's file": {amended, other}, "nothing removed": {built, built},
	} {
		if _, ok := spliceMobileBriefAudio(context.Background(), ffmpegPath, dir, "en", c.brief, c.built); ok {
			t.Errorf("%s: cut anyway", name)
		}
	}
}

// While a removal is spoken again, Today says the recording is updating.
func TestRemakeAfterARemovalIsMarkedUpdating(t *testing.T) {
	_, svc, built := boundBuiltBriefFixture(t)
	svc.speaker.voiceReady = func(string) bool { return true }
	briefPrivacyChanged(briefWithdrawn{Kind: "memory", ID: "safe-recall"})
	svc.wg.Wait()
	stored, _ := svc.store.load(built.BriefID)
	if _, _, err := svc.finishedReadWithholding(stored); err != nil {
		t.Fatal(err)
	}
	stored, _ = svc.store.load(built.BriefID)
	sealed, ok := svc.savedSealedCopy(stored)
	if !ok {
		t.Fatal("no sealed copy")
	}
	if !svc.markAudioRendering(built.BriefID, mobileBriefAudioBinding(sealed, "en"), "en", svc.now()) {
		t.Fatal("the remake was not marked")
	}
	stored, _ = svc.store.load(built.BriefID)
	if stored.Audio == nil || stored.Audio.Status != mobileBriefAudioStatusRendering || !stored.Audio.Updating {
		t.Fatalf("the remake mark does not say the recording is updating: %+v", stored.Audio)
	}
}
