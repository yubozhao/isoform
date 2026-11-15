package main

import (
	"strings"
	"testing"
)

// Nov 14: the morning recording said exactly the words shown, but a Today
// refresh read the brief with a different evidence set (a purpose read again,
// a sensitive item the seal had left out) and the v2 binding, which bound the
// cited sources, no longer matched. The recording was dropped and the phone
// had no player. The v3 binding binds what the voice says, nothing else.
func TestMobileBriefAudioBindingV3FollowsTheWordsNotTheCitedSources(t *testing.T) {
	_, svc, _, brief, release := audioKeepRig(t)
	close(release)
	svc.wg.Wait()
	shown := svc.app.mobileBriefForDelivery(brief)
	if len(shown.Narrative) == 0 || len(shown.Narrative[0].Refs) == 0 {
		t.Fatalf("the fixture has no cited paragraph: %+v", shown.Narrative)
	}
	recheck := shown
	recheck.Evidence = append([]MobileBriefRef{}, shown.Evidence...)
	for i := range recheck.Evidence {
		if recheck.Evidence[i].Ref == shown.Narrative[0].Refs[0] {
			recheck.Evidence[i].SourceID = "read-again-" + recheck.Evidence[i].SourceID
		}
	}
	if mobileBriefAudioBindingV2(shown, "en") == mobileBriefAudioBindingV2(recheck, "en") {
		t.Fatal("the fixture must change what v2 bound")
	}
	if !strings.Contains(mobileBriefAudioBinding(shown, "en"), ":v3:") {
		t.Fatal("a new recording is not bound with v3")
	}
	if mobileBriefAudioBinding(shown, "en") != mobileBriefAudioBinding(recheck, "en") {
		t.Fatal("v3 changed although every spoken word is the same")
	}
	audio := &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Bytes: 4, Mime: mobileBriefAudioMimeWAV, binding: mobileBriefAudioBinding(shown, "en")}
	recheck.Audio = audio
	if mobileBriefAudioForRead(recheck) == nil {
		t.Fatal("a v3 recording was dropped for a cited source that the voice does not say")
	}
	if mobileBriefAudioNeedsRemake(audio, recheck, "en") {
		t.Fatal("a v3 recording that speaks the words is made again")
	}

	// The words change: the recording leaves, as before.
	changed := recheck
	changed.Narrative = append([]MobileBriefPara{}, recheck.Narrative...)
	changed.Narrative[0].Text += " One more line."
	if mobileBriefAudioForRead(changed) != nil || !mobileBriefAudioNeedsRemake(audio, changed, "en") {
		t.Fatal("a recording kept speaking words the brief no longer shows")
	}
}

// Recordings made before v3 are checked by the rule they were made under, and
// one the v2 rule no longer matches is made again (as v3), not kept silent.
func TestMobileBriefAudioV2RecordingsKeepTheirRuleAndAreRemade(t *testing.T) {
	_, svc, _, brief, release := audioKeepRig(t)
	close(release)
	svc.wg.Wait()
	shown := svc.app.mobileBriefForDelivery(brief)
	old := &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Bytes: 4, Mime: mobileBriefAudioMimeWAV, binding: mobileBriefAudioBindingV2(shown, "en")}
	same := shown
	same.Audio = old
	if mobileBriefAudioForRead(same) == nil || mobileBriefAudioNeedsRemake(old, same, "en") {
		t.Fatal("a v2 recording of the same brief stopped playing")
	}
	recheck := same
	recheck.Evidence = append([]MobileBriefRef{}, shown.Evidence...)
	for i := range recheck.Evidence {
		recheck.Evidence[i].SourceID = "read-again-" + recheck.Evidence[i].SourceID
	}
	if mobileBriefAudioForRead(recheck) != nil {
		t.Fatal("a v2 recording was checked by the v3 rule")
	}
	if !mobileBriefAudioNeedsRemake(old, recheck, "en") {
		t.Fatal("a v2 recording that no longer matches is not made again")
	}
}
