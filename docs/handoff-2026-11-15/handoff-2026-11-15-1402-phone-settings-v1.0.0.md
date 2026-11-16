# Phone settings v1.0.0: Settings ranks what everything depends on first; Your data shows what left

Version 1.0.0 · written November 15, 2026, 2:02 PM Pacific, by Claude Design.

**Part of the phone pass of November 15.** Read phone-map v1.0.0 first. This file replaces redesign-phone v1.0.0's Settings › Your data (rule 18) and the Settings sheet on `feat/iphone-companion`.

**Board:** Isoform iPhone, artboard 8 (Settings).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **The connection is true and fixable:** which computer, whether it's reachable, and Pair again only when needed.
2. **Trust:** Closed Eyes, one switch for every device; Your data, what's kept and what left.
3. **What the Yes path needs:** Your agent; and Your voice, which makes what Isoform hears better.
4. **Notifications:** the three kinds.
5. **Switches for what's still settling:** Proposals from your topics; Agent of You.
6. **This phone:** Face ID, clear, unpair.

**Before:** walk-1114-ios 10 to 12 (Settings: You, Agent of You · Soon, Your computer, Phone alerts, Quiet hours, Notifications, This iPhone, Recordings, Work on it, Your voice, What Isoform sees) and 13 (Agent of You).

## Settings

1. **A sheet from the person button on every tab,** with Done.
2. **Your computer:**
   - "Paired with {computer}" and its state: "Online · heard from it just now", "Asleep since 4:12 PM · answers wait on this phone", "Can't reach it · last heard 2:41 PM";
   - **Keep it awake while it's plugged in** · "So this phone can reach it" (the switch the build has under This iPhone moves here);
   - **Pair again** only when the pairing can't be read; Pair another computer under Details.
3. **Closed Eyes:** one switch, "Stops seeing on every device: your computer, this phone and your watch". Turning it on asks for how long: 15 minutes, an hour, until I end it. On, it reads "Closed Eyes until {time}". The build's phone-to-computer pause (959ea8e65) is this switch.
4. **Your data** › (below).
5. **Your agent** › with its name at the right: Claude, ChatGPT, Grok or Codex (phone-agent v1.0.0); "Who does the work you say yes to".
6. **Your voice** › "Added · from your computer" or "Not added yet" (watch-voice v1.0.0; the page the build has stands).
7. **Apple Watch** ›, only when a watch is paired: "Opens the Watch tab". It isn't a page of its own.
8. **Notifications** ›: "The brief, meeting prep, finished work". The page has a switch for each kind, and the brief's times as the computer sets them ("Set on your computer"). Quiet hours and the count on the app icon stand; "Phone alerts" as a separate switch goes (each kind is its own switch).
9. **Isoform:**
   - **Proposals from your topics** · "Topics offer their next piece of work in Your work" (the build's switch for topics' proposed work);
   - **Agent of You** with "Soon" until the website's flag says otherwise (redesign-phone v1.0.0, rule 20).
10. **This phone:** Face ID to open; **Clear this phone** · "Removes saved briefs and answers here. Your computer keeps everything."; **Unpair this phone** in red, last.
11. **At the bottom:** "Version {version} ({build}). Briefs are end-to-end encrypted."

## Your data

12. **Three places, by where the data is:**
    - **Kept on your computer:** "Screen, calls and your watch" · "In Activity on your computer"; "Your answers" · "Every Yes, No, Not mine and edit"; "Your voice" · "One sample". Each opens a read-only view here, with Delete where the computer allows it.
    - **On this phone:** "Today's brief and your work" · "Until tomorrow's build"; "Past briefs" · "30 days"; "Watch audio on its way" · "Only until your computer has it", with how much ("2 hours").
    - **Left your devices:** "Sent to your agent" · "Exactly what went, each time you said yes": each hand-off, its time, the agent, and the exact text and attachments.
13. **Under them:** "Isoform never sends anything else anywhere. Closed Eyes stops seeing on every device at once."

## What goes

14. The "You · This iPhone · iPhone 15" card (the phone's name says nothing the person needs).
15. The Work on it switch (phone-agent v1.0.0).
16. "Recordings" and the watch rows (the Watch tab).
17. "What Isoform sees" as a separate row: it's Your data › Kept on your computer.
18. "This iPhone" as a heading: "This phone".

## What the build needs

B4 (Your agent; each hand-off kept for Sent to your agent), B7 (the computer's state words), B12 (pairing sends the latest brief), and the existing Closed Eyes and keep-awake routes.

## Every word

| Key | English | Where |
| --- | --- | --- |
| set.computer | Your computer | Group |
| set.paired | Paired with {computer} | Row |
| set.online | Online · heard from it just now | Row |
| set.asleep | Asleep since {time} · answers wait on this phone | Row |
| set.cantReach | Can't reach it · last heard {time} | Row |
| set.awake | Keep it awake while it's plugged in | Row |
| set.awakeSub | So this phone can reach it | Row |
| set.pairAgain | Pair again | Button, when needed |
| set.closed | Closed Eyes | Row |
| set.closedSub | Stops seeing on every device: your computer, this phone and your watch | Row |
| set.closedUntil | Closed Eyes until {time} | Row, on |
| set.data | Your data | Row |
| set.dataSub | What's kept, where, and how to delete it | Row |
| set.agent | Your agent | Row |
| set.agentSub | Who does the work you say yes to | Row |
| set.voice | Your voice | Row |
| set.watch | Apple Watch | Row |
| set.watchSub | Opens the Watch tab | Row |
| set.notif | Notifications | Row |
| set.notifSub | The brief, meeting prep, finished work | Row |
| set.topics | Proposals from your topics | Row |
| set.topicsSub | Topics offer their next piece of work in Your work | Row |
| set.phone | This phone | Group |
| set.clear | Clear this phone | Row |
| set.clearSub | Removes saved briefs and answers here. Your computer keeps everything. | Row |
| set.unpair | Unpair this phone | Row |
| data.computer | Kept on your computer | Group |
| data.seen | Screen, calls and your watch | Row |
| data.seenSub | In Activity on your computer | Row |
| data.answers | Your answers | Row |
| data.answersSub | Every Yes, No, Not mine and edit | Row |
| data.phone | On this phone | Group |
| data.today | Today's brief and your work | Row |
| data.todaySub | Until tomorrow's build | Row |
| data.watchAudio | Watch audio on its way | Row |
| data.watchAudioSub | Only until your computer has it | Row |
| data.left | Left your devices | Group |
| data.sent | Sent to your agent | Row |
| data.sentSub | Exactly what went, each time you said yes | Row |
| data.foot | Isoform never sends anything else anywhere. Closed Eyes stops seeing on every device at once. | Under the groups |

## Proof

On Bo's iPhone, a named TestFlight build, against the befores above:

1. **True state:** with the computer asleep, Settings says "Asleep since {time}"; awake, "Online"; stills of both.
2. **One Closed Eyes:** turned on here for an hour, the computer's menu bar and the watch show it within seconds; turned off here, both resume.
3. **What left:** after a hand-off, Your data › Sent to your agent shows it word for word.
4. **No Work on it switch, no Recordings row** in Settings.

## On the map (phone)

- P238 · Settings ranks the connection first, then Closed Eyes and Your data, then Your agent and Your voice.
- P239 · Your data by place: on your computer, on this phone, and what left (Sent to your agent).
- P240 · Settings loses the phone card, the Work on it switch, Recordings and What Isoform sees; "This phone" for "This iPhone".
