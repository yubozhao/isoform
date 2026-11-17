package engagement

import "testing"

// Prep is Time Sensitive (notify v1.0.0 rule 11, B4); inside quiet hours it
// still comes, without a sound.
func TestPrepIsTimeSensitiveAndSilentInQuietHours(t *testing.T) {
	spec, _ := Spec(KindMeetingPrep)
	prefs := DefaultPrefs()
	state := NewPaceState()
	m := Moment{Kind: KindMeetingPrep, SubjectID: "ev-1"}
	day := at(10, 0)
	if v := Pace(m, spec, prefs, state, day); v.Mode != ModeInterrupt || v.Interruption != InterruptionTimeSensitive {
		t.Fatalf("prep by day = %+v", v)
	}
	night := at(23, 30)
	if !prefs.Quiet.Contains(night) {
		t.Skip("the default quiet hours changed")
	}
	if v := Pace(m, spec, prefs, state, night); v.Mode != ModeInterrupt || v.Interruption != InterruptionPassive {
		t.Fatalf("prep in quiet hours = %+v, want a silent delivery", v)
	}
}
