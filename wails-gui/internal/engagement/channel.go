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
	// ScreenLocked is whether the Mac's screen is locked. Non-urgent
	// deliveries wait for the unlock (HoldWhileLocked).
	ScreenLocked bool
}

const (
	// macAttentionIdle is how recent an input event must be for a desktop
	// toast to be the right channel.
	macAttentionIdle = 2 * time.Minute
	// macNearbyIdle is how recent an input event must be for a desktop toast
	// to be worth anything at all.
	macNearbyIdle = 10 * time.Minute
	// phoneOfflineAfter is how long without contact counts as offline for the
	// text fallback.
	phoneOfflineAfter = 30 * time.Minute
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

// ChannelForKind picks the channel for an interrupting verdict of a kind.
// Non-interrupting verdicts get no channel. The order: the Mac when the user
// is at it, the phone when it can be pushed, a text through the relay when
// the user subscribed a number and the moment is time-sensitive or needs
// them, a text through Messages on this Mac only for time-sensitive moments,
// both only when the phone cannot be reached, and the Mac again if it is at
// least awake nearby. ChannelText leaves the choice between the relay and
// Messages to the sender, which prefers the relay.
func ChannelForKind(kind Kind, v Verdict, p Presence, now time.Time) Channel {
	if v.Mode != ModeInterrupt {
		return ChannelNone
	}
	if p.MacForeground && p.MacIdle < macAttentionIdle {
		return ChannelDesktop
	}
	if p.PhoneRegistered && !p.PushFailed && (!p.PhoneReadsWork || PhonePushKind(kind)) {
		return ChannelPush
	}
	phoneOffline := p.PhoneLastSeen.IsZero() || now.Sub(p.PhoneLastSeen) >= phoneOfflineAfter
	unreachable := !p.PhoneRegistered || p.PushFailed || phoneOffline
	timeSensitive := v.Interruption == InterruptionTimeSensitive
	if p.SMSActive && unreachable && (timeSensitive || NeedsYou(kind)) {
		return ChannelText
	}
	if timeSensitive && p.TextAvailable && unreachable {
		return ChannelText
	}
	if p.MacIdle < macNearbyIdle {
		return ChannelDesktop
	}
	return ChannelNone
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
