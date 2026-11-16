# Computer trouble v1.0.0: when something stops that nobody chose

Version 1.0.0 · written November 15, 2026, 9:43 PM Pacific, by Claude Design.

**This file adds to assistant-signed-out v1.0.0, computer-today v1.1.0, computer-settings v1.2.0 (the popover) and notify v1.0.0, and changes two lines in them where it says so; everything else in them stands.** The computer pass drew Today, the menu bar and Activity on days when everything works. R75 showed what happens when something stops and nothing says so: Claude Code was signed out for 24 hours, and every brief fell back to the template in silence. This draws the one place each stop is said, in the new layout.

**Board:** Isoform Computer, new artboard 26 (When something stops that nobody chose).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Nothing that stops by itself stays silent.**
2. **One line, never a pile, and never a notification of its own.**
3. **A choice is never trouble.**

## What counts

1. **Trouble is something still wrong, that nobody chose, that keeps Isoform from doing what you expect.** Four kinds, in this order, by what each costs you:
   1. Isoform hasn't seen your screen (Screen Recording is off for Isoform, often after a system update);
   2. the assistant is signed out (assistant-signed-out v1.0.0);
   3. no internet;
   4. your phone hasn't checked in (its pushes fail, or it's been silent for 30 minutes; notify v1.0.0 rule 3).
2. **Never trouble:** sleep, lock, Closed Eyes, quitting Isoform by hand, a watch off the wrist (the sidebar's foot and the popover's state line already say "watch not listening"). **A stop that fixed itself** (a restart after a crash) is only a gap in Activity: "Isoform restarted · not seen, 5:03 to 5:13 PM".

## Where it's said

3. **One line under Today's date,** on an amber wash with no edge bar: what's wrong and since when, in bold; why, in plain words; and the one button that fixes it: **Open System Settings** opens the exact pane (on macOS 15 and later it's called Screen & System Audio Recording); **Sign in** opens the assistant's own sign-in (assistant-signed-out v1.0.0). No button when there's nothing to press. With more than one, the first by rule 1, and "{n} more ›" opens the rest under it.
4. **The menu bar:** the same line is the popover's first row, with its button, and the mark carries a small amber dot while a line is up. The popover's state line says "not seeing" while the screen can't be seen.
5. **The brief still goes out at its time, and ends with what it's missing:** "Nothing after 2:10 PM is in this brief: Screen Recording is off for Isoform." Its notification adds the line after the count: "4 to answer · Isoform hasn't seen your screen since 2:10 PM."
6. **No notification of its own.** This changes assistant-signed-out v1.0.0's "one notification, once per sign-out", which is now carried by rule 5, and notify v1.0.0 B3's "the next brief opens with it", which is now "ends with it". Three kinds stay three; in 30 days "Observing paused" was sent 126 times and was useful none of them (ST66).
7. **It goes by itself when fixed,** checked every minute and at once when Isoform comes to the front. **Activity › Your day** keeps the hours as a gap, with a dashed dot: "Not seen, until 6:40 PM" / "Screen Recording was off for Isoform. Nothing from these hours is in a brief."
8. **The phone** shows kinds 1 and 2 as one line under its date, without a button: "Isoform hasn't seen your computer's screen since 2:10 PM. It's fixed on your computer." Kind 2's phone words stand (assistant-signed-out v1.0.0).

## Every word

| Key | English | Where |
| --- | --- | --- |
| trouble.screen | Isoform hasn't seen your screen since {time}. | Today, popover |
| trouble.screenWhy | Screen Recording is off for Isoform. | Today, popover |
| trouble.openSettings | Open System Settings | Button |
| trouble.signedOut | {assistant} has been signed out since {time}, so briefs are shorter and Chat can't answer. | Today, popover (adds the time to assistant-signed-out v1.0.0's line) |
| trouble.offline | No internet since {time}. Briefs are shorter until it's back. | Today, popover |
| trouble.phone | Your phone hasn't checked in since {time}. Notifications show here until it does. | Today, popover |
| trouble.more | {n} more › | Today |
| menu.notSeeing | not seeing | Popover state line |
| brief.missing | Nothing after {time} is in this brief: {cause}. | The brief's last line |
| notif.briefTrouble | {n} to answer · {trouble} | The brief's notification, last line |
| act.gap | Not seen, until {time} | Activity › Your day |
| act.gapWhy | {cause}. Nothing from these hours is in a brief. | Activity › Your day |
| act.restarted | Isoform restarted · not seen, {from} to {to} | Activity › Your day |
| phone.troubleScreen | Isoform hasn't seen your computer's screen since {time}. It's fixed on your computer. | Phone, Today |

## Proof

On Bo's computer, a named build, with a recording: turn Screen Recording off for Isoform in System Settings; within a minute the line is on Today and in the popover, and the mark has its dot; the next brief ends with the missing line and its notification carries it; turn it back on, and the line goes by itself and Activity shows the gap with its cause.

## On the map (shell)

- SH13 · Trouble: four kinds by what each costs (screen not seen, assistant signed out, no internet, phone not checking in); one line under Today's date with when and the one fix, "{n} more ›" for the rest; the popover's first row and an amber dot on the mark; the brief ends with what it's missing and its notification carries it; no notification of its own; gone by itself when fixed, a gap in Activity with the cause.
