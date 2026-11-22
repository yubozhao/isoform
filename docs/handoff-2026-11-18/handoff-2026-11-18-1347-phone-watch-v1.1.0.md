# Phone watch v1.1.0: Watch is a place, not a sheet, and where your hours are is always in sight

Version 1.1.0 · written November 18, 2026, 1:47 PM Pacific, by Claude Design.

**This file answers the hub's urgent question for Bo and changes phone-watch v1.0.0, phone-today v1.0.0 rule 18 and watch v1.2.0 rules 31 and 32 where it says so; everything else in them, and watch-off-phone v1.0.0, stands.** Bo, November 16: record everything first; anything that stops recording is visible; the wearer should always know where their hours are. Bo, today: "we need to make the watch page more permanent on the main screen. it shouldn't be hidden in the settings page."

A change goes in a new file with a new version; this one isn't edited after it's written.

## What's wrong now (build 1.0.6, Bo's iPhone Duo, 12:41)

- **The wide layout has no Watch.** Its rail is Today, Chat and "…"; Watch opens only as a sheet over Today, so Bo reaches it through Settings. The narrow layout has the tab (phone-map v1.0.0).
- **"Not listening" says no why and no since.** "Tap the mark on your watch to listen." alone doesn't tell the wearer when recording stopped, or why.
- **One fact is said three ways and never explained:** the pill "25 hours are waiting for your computer.", the error "Couldn't reach your computer. Pull down to try again." under Conversations, and the row "Hours and where they are / 25 on this phone". None says what happens next or whether anything is the wearer's to do.
- **Inside, the page is plumbing:** "25 on this phone" (25 what?), "Recordings go to your computer when it is awake and observing." ("observing" is retired; Closed Eyes is the word), "Sync now", "Checked 12:41 PM", and 25 rows of "7 PM / On this phone / 49 min".

## Ranked goals (changes phone-watch v1.0.0's)

1. **Is it listening, and if not, since when and why.**
2. **Where every hour is,** always in sight, in words, with what happens next.
3. **What it heard,** named, newest first.
4. **The watch's settings.**

## A. Watch is a tab in both layouts

1. **Narrow:** the tab bar stays Today · Watch · Chat (phone-map v1.0.0).
2. **Wide (Duo):** the rail becomes **Today · Watch · Chat · …**, Watch between Today and Chat, the same order as the tab bar. Watch opens in the main area like the other tabs, never as a sheet. Settings › Apple Watch, the mark on Today and the watch line all open the tab.
3. **The Watch item in the rail and the tab bar is the mark in the watch's pose:** the lifted brow in the accent while it listens, the cap in amber during Closed Eyes, the shut eye in grey when it isn't listening, with a small amber dot whenever something needs looking at (Not listening, or hours waiting 30 minutes or more). This makes the watch visible on every screen, the way Bo asked.
4. **With no watch paired** there's no tab and no rail item (phone-watch v1.0.0 rule 1 stands).

## The open Duo

5. **Left pane (360 wide, the same as Today's), always in view:** the Now card, then Where your hours are, then the watch's settings.
6. **Right pane:** today's hours (the columns card), today's conversations, then Earlier days. A conversation opens in the right pane with "Watch" as its back button; the left pane stays.
7. **Narrow, one column, in rank order:** Now, Where your hours are, today's hours, conversations, Earlier days, the watch's settings.

## B. The page

### Now

8. **Every state says why and since when, in one line** (changes phone-watch v1.0.0 rule 3):
   - **Listening** · "On your wrist · heard talk a minute ago";
   - **Closed Eyes until 12:04 PM**, in amber · "Nothing is heard or kept, on every device.";
   - **Off for today** · "You held the mark to stop, at 3:00 PM. It listens again tomorrow." (the watch's own hold, watch-off-phone v1.0.0 rule 4);
   - **Not listening**, in amber · its reason and since when: "Turned off on your phone at 9:40 AM." (watch-off-phone v1.0.0), "After your call, since 10:02 AM.", "Off your wrist since 7:15 PM yesterday." When Isoform doesn't know why: "Since 7:49 PM yesterday." Then "Tap the mark on your watch to listen.";
   - **Not near this phone** · "Last heard 2:41 PM. It keeps listening and sends when it's near."
9. **The words stay the watch's own,** the same on the watch, the phone and the computer (P181): Listening, Not listening, Closed Eyes, Off for today. What changes is that each says when and why.
10. **Amber is for a stop:** Not listening is amber; Closed Eyes is amber because it's on; Listening, Off for today and Not near are plain.

### Where your hours are (replaces phone-watch v1.0.0 rules 5, 11 to 13)

11. **Always shown, under Now, not only when something's wrong:** three places in a row, **On your watch**, **On this phone**, **On your computer**, each with its count: on the watch and this phone, every hour not yet on your computer, from any day; on your computer, today's ("25 hours", "6 hours"). The place holding hours that are waiting is ringed in amber.
12. **Under them, one sentence, the one thing the wearer needs** (watch v1.2.0 rule 31, with "phone" for "iPhone" and the count in hours):
    - all on the computer: "Every hour is on your computer, the last at 12:40 PM.";
    - on the phone, computer asleep: "25 hours are on this phone, waiting for your computer. It's asleep; they go when it wakes. Nothing for you to do.";
    - on the phone, computer can't be reached: "25 hours are on this phone. Your computer can't be reached; they go as soon as it can. Nothing for you to do." After 30 minutes, **Try again** under it;
    - couldn't send: "1 hour couldn't reach your computer. Update Isoform on your computer, then try again." with **Try again**;
    - on the watch: "3 hours are on your watch. They come to this phone when it's near.";
    - Closed Eyes on the computer: "Your computer has Closed Eyes on. Hours wait here until it ends."
13. **Try again** restarts any stuck send (the merged fix behind today's Sync now). It shows only when something is stuck. "Sync now" and "Checked {time}" go; pulling down checks at once (watch v1.2.0 rule 32 stands).
14. **"Hours by place" ›** under the sentence opens the list: grouped by day, then by place, each group one line ("Yesterday · 9 hours on this phone · 7 h 12 min of talk"), opening to its hours ("7 PM · 49 min of talk · on this phone"). Rows always say "hours" and "of talk"; no bare numbers.
15. **"This phone keeps the audio only until your computer has it."** at the foot of that list.

### Conversations

16. **Never an error alone.** When the computer can't be reached, Conversations shows what this phone holds, each hour as a row: "7 PM" / "49 min of talk · waiting for your computer", in grey, with one line over them: "Names and notes come from your computer." The line "Couldn't reach your computer. Pull down to try again." goes; Where your hours are already says it, once.
17. Named conversations, Closed Eyes rows and Earlier days stand (phone-watch v1.0.0 rules 7 to 10).

## C. Today

18. **The watch line is always there when a watch is paired** (changes phone-today v1.0.0 rule 18, which showed it only when something was wrong). One line between the header and the brief; tapping it opens the Watch tab:
    - plain: "Your watch is listening · every hour is on your computer";
    - amber: "Your watch isn't listening · since 9:40 AM";
    - amber: "25 hours from your watch are waiting on this phone";
    - amber: "1 hour from your watch couldn't be sent".
    When two are true, the one that stops listening comes first.
19. **On the open Duo** the line sits in Today's left pane under the date, above the brief.
20. **The mark in Today's header** keeps the watch's pose (phone-today v1.0.0 rule 1).

## Every word

| Key | English | Where |
| --- | --- | --- |
| watch.now.held | You held the mark to stop, at {time}. It listens again tomorrow. | Now, Off for today |
| watch.now.since | Since {time}. | Now, reason unknown |
| watch.now.afterCall | After your call, since {time}. | Now |
| watch.now.offWrist | Off your wrist since {time}. | Now |
| watch.now.tapToListen | Tap the mark on your watch to listen. | Now |
| watch.where.title | Where your hours are | Card |
| watch.where.watch | On your watch | Card |
| watch.where.phone | On this phone | Card |
| watch.where.computer | On your computer | Card |
| watch.where.hours | {n} hours | Card, each place |
| watch.where.allDone | Every hour is on your computer, the last at {time}. | Card |
| watch.where.asleep | {n} hours are on this phone, waiting for your computer. It's asleep; they go when it wakes. Nothing for you to do. | Card |
| watch.where.unreachable | {n} hours are on this phone. Your computer can't be reached; they go as soon as it can. Nothing for you to do. | Card |
| watch.where.onWatch | {n} hours are on your watch. They come to this phone when it's near. | Card |
| watch.where.byPlace | Hours by place | Card, link |
| watch.where.group | {day} · {n} hours on {place} · {talk} of talk | Hours by place |
| watch.where.hour | {hour} · {talk} of talk · {place} | Hours by place |
| watch.conv.fromComputer | Names and notes come from your computer. | Conversations |
| watch.conv.waiting | {talk} of talk · waiting for your computer | Conversations, unnamed hour |
| today.watch.ok | Your watch is listening · every hour is on your computer | Today |
| today.watch.stopped | Your watch isn't listening · since {time} | Today, amber |
| today.watch.waiting | {n} hours from your watch are waiting on this phone | Today, amber |
| today.watch.failed | {n} hour from your watch couldn't be sent | Today, amber |

Retired: "Hours and where they are", "{n} on this phone", "Your watch sends recordings when it is near this phone.", "Recordings go to your computer when it is awake and observing.", "Sync now", "Checked {time}", "Couldn't reach your computer. Pull down to try again." (under Conversations).

## Proof

On Bo's iPhone Duo and a narrow iPhone, a named TestFlight build, with his watch and computer:

1. **Found without Settings:** Watch is in the rail between Today and Chat, and the tab bar on narrow; Bo opens it in one tap. Stills of both.
2. **Today shows it:** the watch line on Today, plain while listening, amber while the computer is unreachable with hours on the phone.
3. **Every stop says why and since when:** turn listening off from the phone, take the watch off, finish a call; a still of the Now card for each.
4. **Where the hours are, true:** with the computer asleep, the card says the hours are on this phone and why; wake it, and the card reads "Every hour is on your computer" within a minute, with no tap.

## On the map (phone)

- P268 · Watch in both layouts: a rail item on the Duo between Today and Chat, opening in the main area (left pane: Now, Where your hours are, settings; right pane: hours, conversations, a conversation); the item is the mark's pose, with an amber dot when something needs looking at.
- P269 · Now on the phone: every state with why and since when, in the watch's own words; amber for Not listening.
- P270 · Where your hours are, always shown: three places, one sentence, Try again only when stuck, Hours by place; no Sync now, no Checked.
- P271 · Today's watch line, always there when a watch is paired: plain when listening and every hour is on the computer, amber when it isn't.
