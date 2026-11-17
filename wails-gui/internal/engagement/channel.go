package engagement

import "time"

// Channel is where a delivery goes.
type Channel string

const (
	ChannelPush    Channel = "push"
	ChannelDesktop Channel = "desktop"
	ChannelText    Channel = "text"
	ChannelEmail   Channel = "email"
	ChannelNone    Channel = "none"
	// ChannelChat is a durable chat record, not an interruption.
	ChannelChat Channel = "chat"
	// ChannelTodayQuiet records a delivery shown as a quiet line on Today
	// instead of interrupting. It is a receipt channel, not a sender: no
	// sender is registered for it and users cannot choose it.
	ChannelTodayQuiet Channel = "today_quiet"
)

// ChoosableChannels are the channels a user can pick for a playbook's result,
// in display order.
func ChoosableChannels() []Channel {
	return []Channel{ChannelDesktop, ChannelPush, ChannelText, ChannelEmail}
}

// ParseChannels keeps the known choosable channels of a list, once each, in
// display order.
func ParseChannels(values []string) []Channel {
	want := map[Channel]bool{}
	for _, v := range values {
		want[Channel(v)] = true
	}
	out := []Channel{}
	for _, c := range ChoosableChannels() {
		if want[c] {
			out = append(out, c)
		}
	}
	return out
}

// Presence is what the router knows about where the user is.
type Presence struct {
	// MacForeground is whether the desktop app is the active application.
	MacForeground bool
	// MacIdle is the time since the last keyboard or mouse event on the Mac.
	MacIdle time.Duration
	// PhoneRegistered is whether a paired phone holds a push token.
	PhoneRegistered bool
	// PhoneLastSeen is the last time the phone talked to the relay.
	PhoneLastSeen time.Time
	// PushFailed is whether pushes to the phone fail permanently (an invalid
	// token, permission revoked).
	PushFailed bool
	// PhoneReadsWork is whether a paired phone reads Your work (the phone
	// redesign): that phone is pushed only for PhonePushKind kinds, and the
	// rest wait in its Your work. An older phone keeps every push.
	PhoneReadsWork bool
	// TextAvailable is whether the Messages text channel on this Mac is set
	// up and allowed.
	TextAvailable bool
	// SMSActive is whether the user subscribed a verified number to texts
	// through the relay (PROTOCOL.md section 18) and the relay is connected.
	SMSActive bool
	// EmailActive is whether the user verified an address for email through
	// the relay (PROTOCOL.md section 19) and the relay is connected.
	EmailActive bool
	// ScreenLocked is whether the Mac's screen is locked: the person is away
	// from it (notify v1.0.0 rule 1).
	ScreenLocked bool
	// ClosedEyes is whether observation is paused: input is not read, so an
	// unlocked computer counts as at it.
	ClosedEyes bool
}

const (
	// macAttentionIdle is how recent an input event must be for a desktop
	// toast to be the right channel.
	macAttentionIdle = 2 * time.Minute
)

// NeedsYou reports whether a kind waits on the user's answer: blocked work.
func NeedsYou(kind Kind) bool {
	switch kind {
	case KindWorkBlocked:
		return true
	default:
		return false
	}
}

// PhonePushKind reports whether a kind may push a phone that reads Your work
// (phone redesign, phone-map v1.0.0 rules 16 and 17): the brief (and its
// first-week and weekly forms), meeting prep and finished work. Every other
// kind waits in the app; on that phone, in Your work. Blocked agent work, reminders the
// person set one by one and answers to a chat that waited keep their push
// until Your work and Chat carry them. A moment the person routed to the
// phone themselves (RequestedChannels) is not affected.
func PhonePushKind(kind Kind) bool {
	switch kind {
	case "", KindBriefReady, KindFirstWeek, KindWeeklyRecap, KindMeetingPrep, KindWorkFinished,
		KindWorkBlocked, KindReminder, KindAnswerReady:
		return true
	default:
		return false
	}
}

// ChannelFor picks the channel for an interrupting verdict of no particular
// kind; see ChannelForKind.
func ChannelFor(v Verdict, p Presence, now time.Time) Channel {
	return ChannelForKind("", v, p, now)
}

// InterruptingKind reports whether a kind may ring at all (notify v1.0.0 B3,
// redesign v1.0.0 section 8): the brief (with its first-week and weekly
// forms), meeting prep, finished work, and a reminder the person set
// themselves (rule 7). Every other kind is a quiet line on Today.
func InterruptingKind(kind Kind) bool {
	switch kind {
	case "", KindBriefReady, KindFirstWeek, KindWeeklyRecap, KindMeetingPrep, KindReminder:
		return true
	default:
		return FinishedWorkKind(kind)
	}
}

// NotificationKinds are the three kinds a person switches on or off: the
// brief, meeting prep and finished work (phone-rings v1.0.0 17c).
func NotificationKinds() []Kind {
	return []Kind{KindBriefReady, KindMeetingPrep, KindWorkFinished}
}

// FinishedWorkKind reports whether a kind is finished work coming back: an
// agent's work, an answer that waited, a playbook's result (notify v1.0.0
// rule 4).
func FinishedWorkKind(kind Kind) bool {
	switch kind {
	case KindWorkFinished, KindAnswerReady, KindPlaybookResult:
		return true
	default:
		return false
	}
}

// AtComputer reports whether the person is at the computer (notify v1.0.0
// rule 1): its screen is unlocked and the last input was under 2 minutes
// ago, in any app. With Closed Eyes on, only the lock counts.
func AtComputer(p Presence) bool {
	if p.ScreenLocked {
		return false
	}
	return p.ClosedEyes || p.MacIdle < macAttentionIdle
}

// PhoneReachable reports whether a paired phone can be pushed now.
func PhoneReachable(p Presence) bool {
	return p.PhoneRegistered && !p.PushFailed
}

// ChannelForKind picks the one place an interrupting verdict of a kind rings
// (notify v1.0.0 rules 1 to 4, B1, B2, B5). Non-interrupting verdicts get no
// channel. At the computer, the computer; away, the phone; never both. With
// no phone that can be reached, the computer shows it at its time, so it
// waits in Notification Center. Finished work is not a notification at the
// computer, nor when the phone can't be reached: it is a quiet line on Today
// (ChannelTodayQuiet). A phone that reads Your work is pushed only for its
// kinds; the rest wait in the app there. No text message or email stands in
// for a notification (rule 14): those go only where the person chose them
// (RequestedChannels).
func ChannelForKind(kind Kind, v Verdict, p Presence, now time.Time) Channel {
	if v.Mode != ModeInterrupt {
		return ChannelNone
	}
	// Three kinds ring (B3); every other is a quiet line on Today. Its chat
	// record, where it has one, is unchanged.
	if !InterruptingKind(kind) {
		return ChannelTodayQuiet
	}
	finished := FinishedWorkKind(kind)
	if AtComputer(p) {
		if finished {
			return ChannelTodayQuiet
		}
		return ChannelDesktop
	}
	if PhoneReachable(p) {
		if !p.PhoneReadsWork || PhonePushKind(kind) {
			return ChannelPush
		}
		return ChannelNone
	}
	if finished {
		return ChannelTodayQuiet
	}
	return ChannelDesktop
}

// RequestedChannels returns the channels the user chose for a moment that can
// be reached now, in display order: the Mac always (a toast waits in
// Notification Center), the phone when one can be pushed, a text when a
// number is subscribed through the relay or Messages on this Mac is set up,
// and email when an address is verified.
func RequestedChannels(m Moment, p Presence) []Channel {
	out := []Channel{}
	for _, c := range ChoosableChannels() {
		if !m.Wants(c) {
			continue
		}
		switch c {
		case ChannelDesktop:
			out = append(out, c)
		case ChannelPush:
			if p.PhoneRegistered && !p.PushFailed {
				out = append(out, c)
			}
		case ChannelText:
			if p.SMSActive || p.TextAvailable {
				out = append(out, c)
			}
		case ChannelEmail:
			if p.EmailActive {
				out = append(out, c)
			}
		default:
		}
	}
	return out
}
