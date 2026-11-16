package engagement

import (
	"testing"
	"time"
)

// A phone that reads Your work is pushed for three kinds only (phone-map
// v1.0.0 rules 16 and 17): the brief, meeting prep and finished work.
// Everything else waits in the app, on the Mac when the person is near it.
// An older phone keeps every push.
func TestChannelForKindPushesThePhoneOnlyForItsKinds(t *testing.T) {
	now := at(10, 0)
	active := Verdict{Mode: ModeInterrupt, Interruption: InterruptionActive}
	phone := Presence{PhoneRegistered: true, PhoneLastSeen: now, MacIdle: 5 * time.Minute, PhoneReadsWork: true}
	for _, kind := range []Kind{KindBriefReady, KindFirstWeek, KindWeeklyRecap, KindMeetingPrep, KindWorkFinished} {
		if got := ChannelForKind(kind, active, phone, now); got != ChannelPush {
			t.Errorf("%s: %s, want push", kind, got)
		}
	}
	for _, kind := range []Kind{KindCommitmentSlipping, KindMeetingEnded, KindPersonReappeared, KindGoalCheckIn,
		KindLearnedToConfirm, KindSuggestion, KindTopicFinds, KindMeetingFollowup, KindWaitingOnReply, KindBriefMissed} {
		if got := ChannelForKind(kind, active, phone, now); got != ChannelDesktop {
			t.Errorf("%s: %s, want the Mac nearby, never the phone", kind, got)
		}
		away := phone
		away.MacIdle = time.Hour
		if got := ChannelForKind(kind, active, away, now); got != ChannelNone {
			t.Errorf("%s away from the Mac: %s, want none (it waits in the app)", kind, got)
		}
		older := phone
		older.PhoneReadsWork = false
		if got := ChannelForKind(kind, active, older, now); got != ChannelPush {
			t.Errorf("%s on an older phone: %s, want push as before", kind, got)
		}
	}
}
