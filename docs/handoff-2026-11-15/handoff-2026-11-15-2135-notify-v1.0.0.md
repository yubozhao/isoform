# Notify v1.0.0: where each notification rings, and what the build changes

Version 1.0.0 · written November 15, 2026, 9:35 PM Pacific, by Claude Design.

**This file adds to redesign v1.0.0 §8 (three kinds and nothing else), redesign-shell v1.0.0, phone-map v1.0.0 §5, phone-agent v1.2.0 rules 10 to 12 and agents-join v1.0.0 rule 7; everything in them stands.** They say *what* notifies. None says *where*: which device rings when you have a computer, a phone and a watch. The build answers that today in ways the design doesn't want. I read the router on c1f0e8bcf (read-only); §5 lists what it does now, with files.

**Board:** Isoform Computer, new artboard 24 (Notifications, and where each one rings).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **One notification, on the device you're at, once.**
2. **Never one you've already read somewhere else.**
3. **Nothing about your day leaves Isoform's sealed paths** to tell you about it.

## 1. Where it rings

1. **At the computer** means its screen is unlocked and the last key press or pointer movement was under 2 minutes ago, **in any app**. Locked, asleep, or idle for 2 minutes is away. Isoform reads only those two facts for this, at the moment it decides, and keeps neither. With Closed Eyes on, only locked or asleep counts (an unlocked computer counts as at it).
2. **At the computer, the computer rings; away, the phone rings,** and the watch shows it by the phone's own rule. **Never both.**
3. **No phone paired, or it can't be reached** (its pushes fail): the computer shows it at its time, so it's in Notification Center when you're back. Prep whose meeting has started is dropped instead.
4. **Finished work at the computer isn't a notification:** it's on Today, in Your day and in the menu bar's next. Away, the phone rings with "Back from {agent}", Read it and Useful (phone-agent v1.2.0 rule 10). If the phone can't be reached, nothing rings; it waits on Today.
5. **Prep follows you once.** Prep that rang on the computer, still unopened when you leave it before the meeting starts, goes to the phone once, without a sound. Never the other way, and never twice.
6. **A join you started** (agents-join v1.0.0 rule 7) asks on the device you started it from, only while Isoform isn't in front there.
7. **A reminder you set yourself** (Remind me in Activity, which the build still offers) is your own alarm: it rings at your time, by rules 1 and 2. It isn't a fourth kind.

## 2. Read anywhere, gone everywhere

8. **Reading it anywhere removes its notification everywhere,** within a minute: opening the brief, the prep or the result in the app, on the phone or on the watch, or pressing Read it, Not needed or Useful. The case that matters most: the brief rang on your phone while you were away, and you read it at the computer; the phone's lock screen no longer shows it.
9. **Swiping a notification away** removes it only on that device, and isn't an answer.

## 3. Levels and quiet

10. **The brief at its time** uses the normal sound. A brief you asked for (Make my brief now) arrives without one, since it's already in front of you. Both are as the build does now.
11. **Prep is Time Sensitive** on both devices. It passes a Focus only if you allow Time Sensitive notifications for Isoform in the system's own settings; Isoform never asks to break through. Inside the phone's quiet hours it arrives without a sound.
12. **The brief and finished work inside quiet hours** wait for their end, and ring then only if still unread.
13. **Closed Eyes stops seeing, not these three.** A brief made during Closed Eyes says what it couldn't see. For quiet, use a Focus or quiet hours; Settings says so under Closed Eyes.
14. **No text message or email stands in for a notification.** They aren't sealed between your devices, and a notification carries your day.

## 4. Every word

The notification words stand (redesign v1.0.0 and phone-map v1.0.0 tables). New:

| Key | English | Where |
| --- | --- | --- |
| settings.notifWhere | Isoform rings where you are: this computer while you're using it, your phone when you're away. | Computer: Settings › Briefs and notifications |
| set.notifWhere | Isoform rings where you are: your computer while you're using it, this phone when you're away. | Phone: Settings › Notifications |
| settings.prepFocus | Prep is Time Sensitive. To let it through a Focus, allow Time Sensitive notifications for Isoform in System Settings. | Computer: Briefs and notifications |
| set.prepFocus | Prep is Time Sensitive. To let it through a Focus, allow Time Sensitive notifications for Isoform in Settings. | Phone: Notifications |
| settings.closedNotif | Closed Eyes stops seeing. Your brief and prep still arrive; for quiet, use a Focus. | Computer: Settings › Closed Eyes |

## 5. What the build does now, and what changes

As I read c1f0e8bcf. Each is one change for the engagement session.

| # | Now | Change |
| --- | --- | --- |
| B1 | `internal/engagement/channel.go`, `ChannelForKind`: the computer only when `MacForeground && MacIdle < 2m`, so working in another app at the computer sends the brief to the phone. | `!ScreenLocked && MacIdle < 2m`, any app (rule 1). |
| B2 | `pace.go` `HoldWhileLocked`, applied in `engagement_service.go` before the channel is chosen: with the screen locked, every delivery that isn't Time Sensitive waits for the unlock. The 6:00 PM brief doesn't reach the phone while the computer is locked. | Locked is away: the phone rings. Hold on the computer only when no phone can be reached (rule 3). |
| B3 | `moment.go` has 24 kinds, many of which interrupt (commitment slipping, work blocked, decision due, capture paused, first week, weekly recap and others). | Interrupting: brief_ready, meeting_prep, and finished work (work_finished, answer_ready, playbook_result) only when away. capture_paused shows on the menu bar's mark and on Today, and the next brief opens with it. Every other kind is a quiet Today line or nothing. reminder by rule 7. |
| B4 | meeting_prep is `active`; the computer's toast sets no interruption level and the desktop app has no Time Sensitive entitlement (the iPhone app has it in `Yansu.entitlements`). | Prep `time-sensitive` on both, with the entitlement on the computer; `passive` inside quiet hours (rule 11). |
| B5 | `ChannelText` (relay SMS or Messages) and `ChannelEmail` stand in when the phone can't be reached. | Not for notifications (rule 14). Rule 3 instead. |
| B6 | `engagementDesktop` (`engagement_app.go`) shows only the first action. | Every action of the kind: prep shows Read it and Not needed. |
| B7 | PROTOCOL.md §14 has receipts but no way to remove a delivered notice. | A withdraw notice: the hub sends it through the relay as a background push and on the poll; the phone removes the delivered notification by its id, and clears any already read when Isoform next runs (a background push can be delayed). The computer removes its own copy directly. |
| B8 | Nothing moves a delivered moment between devices. | Prep follows you once (rule 5): when presence turns away and an unopened prep's meeting hasn't started, one `passive` push. |

## Proof

On Bo's computer and phone, a named build, with screenshots and the router's log line (kind, channel, reason):

1. **At the computer in another app** at brief time: the brief banner on the computer; nothing on the phone.
2. **Computer locked** at brief time: the phone rings at the brief's time, not at the unlock.
3. **Read on the computer:** a brief that rang on the phone, then opened on the computer, is gone from the phone's lock screen within a minute.
4. **Prep:** a Time Sensitive banner on the computer with Read it and Not needed; Not needed records a No; leaving the computer unopened puts it on the phone once, silently.

## On the map

- shell NT1 · Where it rings: the computer while you're using it (unlocked, input in the last 2 minutes, any app), the phone when away, never both; no phone, the computer holds it; finished work at the computer isn't a notification; prep follows you once.
- shell NT2 · Read anywhere, gone everywhere: a withdraw notice through the relay; swiping away isn't an answer.
- shell NT3 · Levels: prep Time Sensitive (entitlement on the computer), quiet hours make it silent; Closed Eyes doesn't silence the three; no text or email stand-ins; the router's 24 kinds cut to three.
- phone P263 · The phone rings only when you're away from the computer, and loses a notification you read anywhere.
