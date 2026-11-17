package engagement

import (
	"testing"
	"time"
)

var la = mustLocation("America/Los_Angeles")

func mustLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func at(hour, minute int) time.Time {
	return time.Date(2026, 10, 12, hour, minute, 0, 0, la)
}

func mustSpec(t *testing.T, kind Kind) KindSpec {
	t.Helper()
	spec, ok := Spec(kind)
	if !ok {
		t.Fatalf("no spec for %s", kind)
	}
	return spec
}

func TestEveryKindHasASpecWithActions(t *testing.T) {
	for _, kind := range AllKinds() {
		spec, ok := Spec(kind)
		if !ok {
			t.Fatalf("%s has no spec", kind)
		}
		if len(spec.Actions) == 0 {
			t.Fatalf("%s has no actions", kind)
		}
		if spec.Weight <= 0 || spec.Weight > 1 {
			t.Fatalf("%s weight %v out of range", kind, spec.Weight)
		}
	}
}

func TestDedupeKeyAndHashPart(t *testing.T) {
	if got := DedupeKey(KindWorkBlocked, " hf-1 "); got != "work_blocked:hf-1" {
		t.Fatalf("DedupeKey = %q", got)
	}
	if got := DedupeKey(KindGoalCheckIn, "g1", WeekKey(at(9, 0))); got != "goal_check_in:g1:2026-W42" {
		t.Fatalf("DedupeKey with week = %q", got)
	}
	if a, b := HashPart("needs the project code"), HashPart("needs the project code "); a != b || len(a) != 12 {
		t.Fatalf("HashPart unstable or wrong length: %q %q", a, b)
	}
	if HashPart("a") == HashPart("b") {
		t.Fatal("HashPart collided on different text")
	}
}

func TestQuietWindowWrapsMidnight(t *testing.T) {
	q := DefaultPrefs().Quiet
	cases := []struct {
		at   time.Time
		want bool
	}{
		{at(23, 0), true},
		{at(3, 0), true},
		{at(7, 59), true},
		{at(8, 0), false},
		{at(12, 0), false},
		{at(21, 59), false},
		{at(22, 0), true},
	}
	for _, c := range cases {
		if got := q.Contains(c.at); got != c.want {
			t.Errorf("Contains(%s) = %v, want %v", c.at.Format("15:04"), got, c.want)
		}
	}
	if end := q.EndAfter(at(23, 0)); !end.Equal(time.Date(2026, 10, 13, 8, 0, 0, 0, la)) {
		t.Fatalf("EndAfter(23:00) = %s", end)
	}
	if end := q.EndAfter(at(3, 0)); !end.Equal(at(8, 0)) {
		t.Fatalf("EndAfter(03:00) = %s", end)
	}
	if end := q.EndAfter(at(12, 0)); !end.Equal(at(12, 0)) {
		t.Fatalf("EndAfter outside the window moved the time: %s", end)
	}
}

func TestNextDigest(t *testing.T) {
	p := DefaultPrefs()
	if got := p.NextDigest(at(9, 0)); !got.Equal(at(13, 0)) {
		t.Fatalf("NextDigest(09:00) = %s", got)
	}
	if got := p.NextDigest(at(19, 0)); !got.Equal(time.Date(2026, 10, 13, 8, 30, 0, 0, la)) {
		t.Fatalf("NextDigest(19:00) = %s", got)
	}
}

func TestDailyBoundariesAcrossDST(t *testing.T) {
	prefs := DefaultPrefs()
	for _, day := range []time.Time{
		time.Date(2026, 3, 8, 0, 0, 0, 0, la),
		time.Date(2026, 11, 1, 0, 0, 0, 0, la),
	} {
		t.Run(day.Format("2006-01-02"), func(t *testing.T) {
			next := day.AddDate(0, 0, 1)
			if got := nextLocalMidnight(day.Add(12 * time.Hour)); !got.Equal(next) {
				t.Fatalf("nextLocalMidnight = %s, want %s", got, next)
			}
			atNight := time.Date(day.Year(), day.Month(), day.Day(), 23, 0, 0, 0, la)
			if got := prefs.Quiet.EndAfter(atNight); !got.Equal(next.Add(8 * time.Hour)) {
				t.Fatalf("quiet window end = %s, want %s", got, next.Add(8*time.Hour))
			}
			atEvening := time.Date(day.Year(), day.Month(), day.Day(), 19, 0, 0, 0, la)
			wantDigest := next.Add(8*time.Hour + 30*time.Minute)
			if got := prefs.NextDigest(atEvening); !got.Equal(wantDigest) {
				t.Fatalf("next digest = %s, want %s", got, wantDigest)
			}
		})
	}
}

func TestInterruptionLevels(t *testing.T) {
	now := at(10, 0)
	cases := []struct {
		name string
		m    Moment
		want Interruption
	}{
		{"blocked with a deadline", Moment{Kind: KindWorkBlocked, DueAt: now.Add(48 * time.Hour), HardDeadline: true}, InterruptionTimeSensitive},
		{"blocked without a deadline", Moment{Kind: KindWorkBlocked}, InterruptionActive},
		{"capture paused for twenty minutes", Moment{Kind: KindCapturePaused, CreatedAt: now.Add(-20 * time.Minute)}, InterruptionActive},
		{"capture paused for two hours", Moment{Kind: KindCapturePaused, CreatedAt: now.Add(-2 * time.Hour)}, InterruptionTimeSensitive},
		{"meeting ended", Moment{Kind: KindMeetingEnded}, InterruptionPassive},
		{"work finished", Moment{Kind: KindWorkFinished}, InterruptionActive},
	}
	for _, c := range cases {
		spec := mustSpec(t, c.m.Kind)
		if got := spec.Interruption(c.m, now); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestScoreOrdering(t *testing.T) {
	now := at(10, 0)
	prefs := DefaultPrefs()
	spec := mustSpec(t, KindWorkBlocked)
	soon := Score(Moment{Kind: KindWorkBlocked, DueAt: now.Add(30 * time.Minute)}, spec, prefs, 0, now)
	later := Score(Moment{Kind: KindWorkBlocked, DueAt: now.Add(3 * 24 * time.Hour)}, spec, prefs, 0, now)
	if soon <= later {
		t.Fatalf("a closer deadline must score higher: %v vs %v", soon, later)
	}
	tired := Score(Moment{Kind: KindWorkBlocked, DueAt: now.Add(30 * time.Minute)}, spec, prefs, 1, now)
	if tired >= soon {
		t.Fatalf("fatigue must lower the score: %v vs %v", tired, soon)
	}
	unknown := Score(Moment{Kind: KindWorkBlocked}, spec, prefs, 0, now)
	high := Score(Moment{Kind: KindWorkBlocked, Stakes: 1}, spec, prefs, 0, now)
	if !(unknown > 0 && unknown < high) {
		t.Fatalf("unknown stakes should sit between zero and full: %v vs %v", unknown, high)
	}
	prefs.Weights[KindWorkBlocked] = 0.1
	if weighted := Score(Moment{Kind: KindWorkBlocked, Stakes: 1}, spec, prefs, 0, now); weighted >= high {
		t.Fatalf("a lower preference weight must lower the score: %v vs %v", weighted, high)
	}
}

func TestPaceSuppressions(t *testing.T) {
	now := at(10, 0)
	prefs := DefaultPrefs()
	state := NewPaceState()
	slipping := mustSpec(t, KindCommitmentSlipping)
	m := Moment{Kind: KindCommitmentSlipping, DedupeKey: DedupeKey(KindCommitmentSlipping, "c1")}

	prefs.Enabled[KindCommitmentSlipping] = false
	if v := Pace(m, slipping, prefs, state, now); v.Mode != ModeSuppress || v.Reason != "kind off" {
		t.Fatalf("disabled kind: %+v", v)
	}
	delete(prefs.Enabled, KindCommitmentSlipping)

	prefs.MutedUntil[KindCommitmentSlipping] = now.Add(time.Hour)
	if v := Pace(m, slipping, prefs, state, now); v.Mode != ModeSuppress || v.Reason != "muted" {
		t.Fatalf("muted kind: %+v", v)
	}
	delete(prefs.MutedUntil, KindCommitmentSlipping)

	expired := m
	expired.ExpiresAt = now.Add(-time.Minute)
	if v := Pace(expired, slipping, prefs, state, now); v.Mode != ModeSuppress || v.Reason != "expired" {
		t.Fatalf("expired moment: %+v", v)
	}

	first := Pace(m, slipping, prefs, state, now)
	if first.Mode != ModeInterrupt {
		t.Fatalf("first delivery: %+v", first)
	}
	state.Record(m, first, now)
	if v := Pace(m, slipping, prefs, state, now.Add(time.Hour)); v.Mode != ModeSuppress || v.Reason != "cooldown" {
		t.Fatalf("inside the one day cooldown: %+v", v)
	}
	if v := Pace(m, slipping, prefs, state, now.Add(25*time.Hour)); v.Mode != ModeInterrupt {
		t.Fatalf("after the cooldown: %+v", v)
	}

	meeting := mustSpec(t, KindMeetingEnded)
	once := Moment{Kind: KindMeetingEnded, DedupeKey: DedupeKey(KindMeetingEnded, "s1")}
	v := Pace(once, meeting, prefs, state, now)
	state.Record(once, v, now)
	if again := Pace(once, meeting, prefs, state, now.Add(48*time.Hour)); again.Mode != ModeSuppress || again.Reason != "cooldown" {
		t.Fatalf("a zero cooldown must mean at most once per key: %+v", again)
	}
}

func TestPaceDailyCapAndDigest(t *testing.T) {
	now := at(10, 0)
	prefs := DefaultPrefs()
	prefs.Enabled[KindGoalCheckIn] = true
	state := NewPaceState()
	goal := mustSpec(t, KindGoalCheckIn)
	g1 := Moment{Kind: KindGoalCheckIn, DedupeKey: DedupeKey(KindGoalCheckIn, "g1", WeekKey(now))}
	v := Pace(g1, goal, prefs, state, now)
	if v.Mode != ModeDigest || !v.HoldUntil.Equal(at(13, 0)) {
		t.Fatalf("passive goal check-in should wait for the 13:00 digest: %+v", v)
	}
	state.Record(g1, v, now)
	g2 := Moment{Kind: KindGoalCheckIn, DedupeKey: DedupeKey(KindGoalCheckIn, "g2", WeekKey(now))}
	if v := Pace(g2, goal, prefs, state, now.Add(time.Minute)); v.Mode != ModeSuppress || v.Reason != "daily cap" {
		t.Fatalf("second goal check-in the same day: %+v", v)
	}
	tomorrow := now.Add(24 * time.Hour)
	if v := Pace(g2, goal, prefs, state, tomorrow); v.Mode != ModeDigest {
		t.Fatalf("the cap must reset the next day: %+v", v)
	}
}

func TestPaceQuietHours(t *testing.T) {
	prefs := DefaultPrefs()
	state := NewPaceState()
	blocked := mustSpec(t, KindWorkBlocked)
	night := at(23, 0)

	calm := Moment{Kind: KindWorkBlocked, DedupeKey: "work_blocked:a"}
	if v := Pace(calm, blocked, prefs, state, night); v.Mode != ModeHold || !v.HoldUntil.Equal(time.Date(2026, 10, 13, 8, 0, 0, 0, la)) {
		t.Fatalf("an active moment at night must wait for 08:00: %+v", v)
	}

	urgent := Moment{Kind: KindWorkBlocked, DedupeKey: "work_blocked:b", DueAt: night.Add(time.Hour), HardDeadline: true}
	v := Pace(urgent, blocked, prefs, state, night)
	if v.Mode != ModeInterrupt || v.Reason != "quiet hours break" {
		t.Fatalf("time-sensitive blocked work may break quiet hours once: %+v", v)
	}
	state.Record(urgent, v, night)

	second := Moment{Kind: KindWorkBlocked, DedupeKey: "work_blocked:c", DueAt: night.Add(90 * time.Minute), HardDeadline: true}
	if v := Pace(second, blocked, prefs, state, night.Add(30*time.Minute)); v.Mode != ModeHold {
		t.Fatalf("a second break within two hours must wait: %+v", v)
	}
	if v := Pace(second, blocked, prefs, state, night.Add(3*time.Hour)); v.Mode != ModeInterrupt {
		t.Fatalf("after two hours another break is allowed: %+v", v)
	}

	finished := mustSpec(t, KindWorkFinished)
	done := Moment{Kind: KindWorkFinished, DedupeKey: "work_finished:h1"}
	if v := Pace(done, finished, prefs, state, night); v.Mode != ModeHold {
		t.Fatalf("work finished never breaks quiet hours: %+v", v)
	}
}

func TestPacePassiveMomentsRespectQuietHours(t *testing.T) {
	prefs := DefaultPrefs()
	state := NewPaceState()
	night := at(23, 30)
	brief := mustSpec(t, KindBriefReady)
	if v := Pace(Moment{Kind: KindBriefReady, DedupeKey: "brief_ready:v9"}, brief, prefs, state, night); v.Mode != ModeHold || !v.HoldUntil.Equal(time.Date(2026, 10, 13, 8, 0, 0, 0, la)) {
		t.Fatalf("a passive brief at night must wait for the morning: %+v", v)
	}
	// ST86: a scheduled brief buzzes by day; one the person asked for does not.
	if v := Pace(Moment{Kind: KindBriefReady, DedupeKey: "brief_ready:v9"}, brief, prefs, state, at(9, 0)); v.Mode != ModeInterrupt || v.Interruption != InterruptionActive {
		t.Fatalf("a scheduled brief by day buzzes: %+v", v)
	}
	if v := Pace(Moment{Kind: KindBriefReady, DedupeKey: "brief_ready:v10", Params: map[string]string{BriefRequestParam: BriefRequestManual}}, brief, prefs, state, at(9, 0)); v.Mode != ModeInterrupt || v.Interruption != InterruptionPassive {
		t.Fatalf("a brief made by hand goes out quietly: %+v", v)
	}
	meeting := mustSpec(t, KindMeetingEnded)
	if v := Pace(Moment{Kind: KindMeetingEnded, DedupeKey: "meeting_ended:s9"}, meeting, prefs, state, night); v.Mode != ModeDigest || !v.HoldUntil.Equal(time.Date(2026, 10, 13, 8, 30, 0, 0, la)) {
		t.Fatalf("a digestable moment at night waits for the first digest after quiet hours: %+v", v)
	}
	prefs.DigestTimes = []time.Duration{23*time.Hour + 45*time.Minute}
	if v := Pace(Moment{Kind: KindMeetingEnded, DedupeKey: "meeting_ended:s10"}, meeting, prefs, state, night); v.Mode != ModeDigest || !v.HoldUntil.Equal(time.Date(2026, 10, 13, 8, 0, 0, 0, la)) {
		t.Fatalf("a digest time inside quiet hours moves to the end of quiet hours: %+v", v)
	}
}

func TestPaceBudget(t *testing.T) {
	now := at(10, 0)
	prefs := DefaultPrefs()
	prefs.DailyInterruptBudget = 2
	state := NewPaceState()
	finished := mustSpec(t, KindWorkFinished)
	for i := 0; i < 2; i++ {
		m := Moment{Kind: KindWorkFinished, DedupeKey: DedupeKey(KindWorkFinished, string(rune('a'+i)))}
		v := Pace(m, finished, prefs, state, now)
		if v.Mode != ModeInterrupt {
			t.Fatalf("delivery %d within budget: %+v", i, v)
		}
		state.Record(m, v, now)
	}
	third := Moment{Kind: KindWorkFinished, DedupeKey: "work_finished:c"}
	if v := Pace(third, finished, prefs, state, now); v.Mode != ModeInterrupt {
		t.Fatalf("routing must allow chat; the notification sender enforces the budget: %+v", v)
	}
	blocked := mustSpec(t, KindWorkBlocked)
	deadline := Moment{Kind: KindWorkBlocked, DedupeKey: "work_blocked:d", DueAt: now.Add(time.Hour), HardDeadline: true}
	if v := Pace(deadline, blocked, prefs, state, now); v.Mode != ModeInterrupt {
		t.Fatalf("time-sensitive moments are not budgeted: %+v", v)
	}
	if state.InterruptsToday != 2 {
		t.Fatalf("InterruptsToday = %d", state.InterruptsToday)
	}
	state.Record(deadline, Verdict{Mode: ModeInterrupt, Interruption: InterruptionTimeSensitive}, now)
	if state.InterruptsToday != 2 {
		t.Fatalf("a time-sensitive delivery must not count against the budget: %d", state.InterruptsToday)
	}
	state.Record(third, Verdict{Mode: ModeInterrupt, Interruption: InterruptionActive}, now.Add(24*time.Hour))
	if state.InterruptsToday != 1 {
		t.Fatalf("counters must roll over at the next local day: %d", state.InterruptsToday)
	}
}

func TestChannelFor(t *testing.T) {
	now := at(10, 0)
	active := Verdict{Mode: ModeInterrupt, Interruption: InterruptionActive}
	urgent := Verdict{Mode: ModeInterrupt, Interruption: InterruptionTimeSensitive}
	cases := []struct {
		name string
		v    Verdict
		p    Presence
		want Channel
	}{
		{"digest gets no channel", Verdict{Mode: ModeDigest}, Presence{PhoneRegistered: true}, ChannelNone},
		{"at the Mac", active, Presence{MacForeground: true, MacIdle: 10 * time.Second, PhoneRegistered: true}, ChannelDesktop},
		{"Mac open but idle", active, Presence{MacForeground: true, MacIdle: 5 * time.Minute, PhoneRegistered: true, PhoneLastSeen: now}, ChannelPush},
		{"phone registered, long silent", active, Presence{PhoneRegistered: true, PhoneLastSeen: now.Add(-2 * time.Hour), MacIdle: time.Hour}, ChannelPush},
		{"push failed, Mac nearby", active, Presence{PhoneRegistered: true, PushFailed: true, MacIdle: 3 * time.Minute, TextAvailable: true}, ChannelDesktop},
		{"push failed, urgent, text set up", urgent, Presence{PhoneRegistered: true, PushFailed: true, TextAvailable: true, MacIdle: time.Hour}, ChannelDesktop},
		{"no phone, urgent, text set up", urgent, Presence{TextAvailable: true, MacIdle: time.Hour}, ChannelDesktop},
		{"no phone, Mac idle an hour", active, Presence{MacIdle: time.Hour}, ChannelDesktop},
	}
	for _, c := range cases {
		if got := ChannelFor(c.v, c.p, now); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
