package engagement

import (
	"testing"
	"time"
)

// Three kinds ring, on any phone and on the computer (phone-map v1.0.0 rules
// 16 and 17, notify v1.0.0 B3): the brief, meeting prep and finished work.
// Everything else is a quiet line on Today.
func TestChannelForKindPushesThePhoneOnlyForItsKinds(t *testing.T) {
	now := at(10, 0)
	active := Verdict{Mode: ModeInterrupt, Interruption: InterruptionActive}
	away := Presence{PhoneRegistered: true, PhoneLastSeen: now, MacIdle: 5 * time.Minute, PhoneReadsWork: true}
	for _, kind := range []Kind{KindBriefReady, KindFirstWeek, KindWeeklyRecap, KindMeetingPrep, KindWorkFinished} {
		if got := ChannelForKind(kind, active, away, now); got != ChannelPush {
			t.Errorf("%s: %s, want push", kind, got)
		}
	}
	for _, kind := range []Kind{KindCommitmentSlipping, KindMeetingEnded, KindPersonReappeared, KindGoalCheckIn,
		KindLearnedToConfirm, KindSuggestion, KindTopicFinds, KindMeetingFollowup, KindWaitingOnReply, KindBriefMissed} {
		at := away
		at.MacIdle = time.Minute
		older := away
		older.PhoneReadsWork = false
		for _, p := range []Presence{away, at, older} {
			if got := ChannelForKind(kind, active, p, now); got != ChannelTodayQuiet {
				t.Errorf("%s with %+v: %s, want a quiet line on Today", kind, p, got)
			}
		}
	}
}

// Where it rings (notify v1.0.0 rules 1 to 4, B1 and B2): at the computer
// means unlocked with input in the last 2 minutes, in any app; away, the
// phone; never both; no phone, the computer holds it; finished work at the
// computer is a quiet line on Today.
func TestChannelForKindRingsWhereThePersonIs(t *testing.T) {
	now := at(18, 0)
	active := Verdict{Mode: ModeInterrupt, Interruption: InterruptionActive}
	phone := Presence{PhoneRegistered: true, PhoneLastSeen: now, PhoneReadsWork: true}
	cases := []struct {
		name string
		kind Kind
		p    func(Presence) Presence
		want Channel
	}{
		{"in another app at the computer", KindBriefReady, func(p Presence) Presence { p.MacIdle = 30 * time.Second; return p }, ChannelDesktop},
		{"Isoform in front", KindBriefReady, func(p Presence) Presence { p.MacForeground = true; return p }, ChannelDesktop},
		{"idle 2 minutes", KindBriefReady, func(p Presence) Presence { p.MacIdle = 2 * time.Minute; return p }, ChannelPush},
		{"locked, even with fresh input", KindBriefReady, func(p Presence) Presence { p.ScreenLocked, p.MacIdle = true, 0; return p }, ChannelPush},
		{"locked with Isoform in front", KindMeetingPrep, func(p Presence) Presence { p.ScreenLocked, p.MacForeground = true, true; return p }, ChannelPush},
		{"Closed Eyes, unlocked and idle", KindBriefReady, func(p Presence) Presence { p.ClosedEyes, p.MacIdle = true, time.Hour; return p }, ChannelDesktop},
		{"Closed Eyes, locked", KindBriefReady, func(p Presence) Presence { p.ClosedEyes, p.ScreenLocked = true, true; return p }, ChannelPush},
		{"locked, no phone", KindBriefReady, func(Presence) Presence { return Presence{ScreenLocked: true} }, ChannelDesktop},
		{"away, pushes fail", KindMeetingPrep, func(p Presence) Presence { p.MacIdle, p.PushFailed = time.Hour, true; return p }, ChannelDesktop},
		{"finished work at the computer", KindWorkFinished, func(p Presence) Presence { p.MacIdle = 0; return p }, ChannelTodayQuiet},
		{"finished work away", KindWorkFinished, func(p Presence) Presence { p.MacIdle = time.Hour; return p }, ChannelPush},
		{"finished work, no phone", KindAnswerReady, func(Presence) Presence { return Presence{MacIdle: time.Hour} }, ChannelTodayQuiet},
		{"a playbook's result at the computer", KindPlaybookResult, func(p Presence) Presence { p.MacIdle = 0; return p }, ChannelTodayQuiet},
	}
	for _, c := range cases {
		if got := ChannelForKind(c.kind, active, c.p(phone), now); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if got := ChannelForKind(KindBriefReady, Verdict{Mode: ModeQuiet}, phone, now); got != ChannelNone {
		t.Errorf("a quiet verdict: %s", got)
	}
}
