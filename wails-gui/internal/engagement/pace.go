package engagement

import "time"

// DeliveryMode is what the pacing rules decided for a moment.
type DeliveryMode string

const (
	// ModeQuiet records a Today line without notification pacing.
	ModeQuiet DeliveryMode = "quiet"
	// ModeInterrupt delivers now on the chosen channel.
	ModeInterrupt DeliveryMode = "interrupt"
	// ModeDigest waits for the next digest.
	ModeDigest DeliveryMode = "digest"
	// ModeHold waits until HoldUntil, then is paced again.
	ModeHold DeliveryMode = "hold"
	// ModeSuppress drops the moment with a reason.
	ModeSuppress DeliveryMode = "suppress"
)

// Verdict is the pacing outcome for one moment.
type Verdict struct {
	Mode         DeliveryMode
	Interruption Interruption
	Reason       string
	HoldUntil    time.Time
}

// PaceState is what the rules remember between moments. The caller persists
// it; Record keeps it current after a delivery.
type PaceState struct {
	// Day is the local date the counters belong to (2006-01-02).
	Day string
	// InterruptsToday tracks active deliveries for legacy phone pacing.
	InterruptsToday int
	// SentByKindToday counts deliveries per kind for daily caps.
	SentByKindToday map[Kind]int
	// LastSentByKey is the last delivery time per dedupe key.
	LastSentByKey map[string]time.Time
	// LastQuietBreak is the last interruption made inside quiet hours.
	LastQuietBreak time.Time
}

// NewPaceState returns an empty state.
func NewPaceState() *PaceState {
	return &PaceState{
		SentByKindToday: map[Kind]int{},
		LastSentByKey:   map[string]time.Time{},
	}
}

func (s *PaceState) rollover(now time.Time) {
	day := now.Format("2006-01-02")
	if s.Day == day {
		return
	}
	s.Day = day
	s.InterruptsToday = 0
	s.SentByKindToday = map[Kind]int{}
}

// Pace applies the enabled, mute, expiry, cooldown, kind cap and quiet-hours
// rules. The delivery service enforces the notification budget after routing.
// It does not mutate the
// state; call Record after a delivery.
func Pace(m Moment, spec KindSpec, prefs Prefs, state *PaceState, now time.Time) Verdict {
	if state == nil {
		state = NewPaceState()
	}
	state.rollover(now)

	if !prefs.KindEnabled(spec) {
		return Verdict{Mode: ModeSuppress, Reason: "kind off"}
	}
	if until, ok := prefs.MutedUntil[spec.Kind]; ok && until.After(now) && !spec.Direct {
		return Verdict{Mode: ModeSuppress, Reason: "muted"}
	}
	if !m.ExpiresAt.IsZero() && !m.ExpiresAt.After(now) {
		return Verdict{Mode: ModeSuppress, Reason: "expired"}
	}
	quiet := prefs.KindQuietOnToday(spec.Kind)
	if prefs.FirstWeek {
		switch spec.Kind {
		case KindFirstWeek, KindWeeklyRecap:
			if !quiet && state.SentByKindToday[KindFirstWeek]+state.SentByKindToday[KindWeeklyRecap] >= 1 {
				return Verdict{Mode: ModeSuppress, Reason: "first week daily cap"}
			}
		case KindWorkBlocked, KindWorkFinished, KindDecisionDue, KindPlaybookResult, KindReminder, KindBriefReady:
			// Explicit work keeps its existing delivery contract. A brief push is
			// one per brief (its dedupe key) and follows quiet hours, the switch
			// and the cap; brief_missed stays under the first-week rule (ST86).
		default:
			return Verdict{Mode: ModeSuppress, Reason: "first week discovery"}
		}
	}
	// Today lines are not interruptions: notification caps, cooldowns, quiet
	// hours and digest slots must not delay them or consume their allowance.
	if quiet {
		return Verdict{Mode: ModeQuiet, Interruption: InterruptionPassive, Reason: "today quiet"}
	}
	if last, ok := state.LastSentByKey[m.DedupeKey]; ok && m.DedupeKey != "" {
		if spec.Cooldown <= 0 || now.Sub(last) < spec.Cooldown {
			return Verdict{Mode: ModeSuppress, Reason: "cooldown"}
		}
	}
	if spec.DailyCap > 0 && state.SentByKindToday[spec.Kind] >= spec.DailyCap && !(spec.Kind == KindFirstWeek && m.Params["stage"] == "brief") {
		return Verdict{Mode: ModeSuppress, Reason: "daily cap"}
	}

	level := spec.Interruption(m, now)
	if level == InterruptionPassive {
		if spec.Digestable {
			hold := prefs.NextDigest(now)
			if prefs.Quiet.Contains(hold) {
				hold = prefs.Quiet.EndAfter(hold)
			}
			return Verdict{Mode: ModeDigest, Interruption: level, Reason: "passive", HoldUntil: hold}
		}
		if prefs.Quiet.Contains(now) {
			return Verdict{Mode: ModeHold, Interruption: level, Reason: "quiet hours", HoldUntil: prefs.Quiet.EndAfter(now)}
		}
		return Verdict{Mode: ModeInterrupt, Interruption: level, Reason: "passive delivery"}
	}

	if prefs.Quiet.Contains(now) && spec.Kind == KindReminder {
		// The user picked this time themselves.
		return Verdict{Mode: ModeInterrupt, Interruption: level, Reason: "reminder time"}
	}
	if prefs.Quiet.Contains(now) {
		breakable := level == InterruptionTimeSensitive && spec.Kind == KindWorkBlocked
		spaced := state.LastQuietBreak.IsZero() || now.Sub(state.LastQuietBreak) >= prefs.QuietBreakSpacing
		if breakable && spaced {
			return Verdict{Mode: ModeInterrupt, Interruption: level, Reason: "quiet hours break"}
		}
		return Verdict{Mode: ModeHold, Interruption: level, Reason: "quiet hours", HoldUntil: prefs.Quiet.EndAfter(now)}
	}

	return Verdict{Mode: ModeInterrupt, Interruption: level, Reason: "ok"}
}

// Record updates the state after a moment was delivered with the verdict.
func (s *PaceState) Record(m Moment, v Verdict, now time.Time) {
	if v.Mode == ModeQuiet {
		return
	}
	if s.SentByKindToday == nil {
		s.SentByKindToday = map[Kind]int{}
	}
	if s.LastSentByKey == nil {
		s.LastSentByKey = map[string]time.Time{}
	}
	s.rollover(now)
	if m.DedupeKey != "" {
		s.LastSentByKey[m.DedupeKey] = now
	}
	s.SentByKindToday[m.Kind]++
	if v.Mode != ModeInterrupt {
		return
	}
	if v.Interruption == InterruptionActive && !requestedKind(m.Kind) {
		s.InterruptsToday++
	}
	if v.Reason == "quiet hours break" {
		s.LastQuietBreak = now
	}
}

func nextLocalMidnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
}

// requestedKind reports whether a kind's deliveries are ones the user asked
// for, which the automatic notification budget does not count.
func requestedKind(kind Kind) bool {
	spec, ok := Spec(kind)
	return ok && spec.Requested
}
