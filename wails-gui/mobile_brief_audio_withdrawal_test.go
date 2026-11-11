package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The edit case of TestBriefAudioWithholdsRecordingAfterCitedSourceEdit no longer
// asserts that text is kept from the voice, because an edit is not a withdrawal.
// This is the withdrawal case: once the cited note is forgotten, its recording is
// not served and the withheld text is not sent to the voice.
func TestBriefAudioWithdrawnSourceIsNotServedOrSpoken(t *testing.T) {
	_, svc, brief := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	var ref string
	for _, evidence := range brief.Evidence {
		if evidence.SourceID == "safe-recall" {
			ref = evidence.Ref
		}
	}
	if ref == "" {
		t.Fatal("fixture lacks memory source")
	}
	brief.NarrativeStatus = "ok"
	brief.Narrative = []MobileBriefPara{{Text: "The original note is ready.", Refs: []string{ref}}}
	data := testWAV(1)
	brief.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Mime: mobileBriefAudioMimeWAV, Bytes: len(data), DurationSeconds: 1, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
	brief.Audio.binding = mobileBriefAudioBinding(svc.app.mobileBriefForDelivery(brief), "en")
	if err := svc.store.save(brief, svc.now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc.store.dir, brief.BriefID+mobileBriefAudioExtWAV), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := svc.seal(t.Context(), brief); !ok {
		t.Fatal("fixture did not seal")
	}
	if _, ok := svc.Audio(brief.BriefID); !ok {
		t.Fatal("original recording was not available")
	}
	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "memory", ID: "safe-recall"})
	svc.wg.Wait()
	if _, ok := svc.Audio(brief.BriefID); ok {
		t.Fatal("the recording of a withdrawn source was served")
	}
	calls := 0
	svc.speaker = fakeSpeaker("en", data, nil)
	svc.speaker.synthesize = func(context.Context, string, string) ([]byte, error) { calls++; return data, nil }
	svc.addAudio(t.Context(), brief, svc.now())
	if calls != 0 {
		t.Fatal("withheld text was sent to the voice after its source was withdrawn")
	}
}
