# Computer settings v1.0.0: Settings opens on What Isoform sees, Messages becomes a source, Your agents replaces Thinks with, and the menu bar ranks like the app

Version 1.0.0 · written November 15, 2026, 5:04 PM Pacific, by Claude Design.

**Part of the computer pass of November 15.** Read computer-map v1.0.0 first. redesign-shell v1.0.0 holds except where this file says so (Your data, Closed Eyes, notifications).

**Bo, November 15:** "Make sure we have message access on iPhone app." The phone can't read Messages (iOS gives no app that access); on the phone, access means handing work to agents you text (phone-agent v1.1.0). Reading your conversations happens on the computer, as a source here.

**Board:** Isoform Computer, artboard 8 (Settings and the menu bar).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Closed Eyes** is one keystroke, everywhere, and always wins.
2. **Every source on one page,** each with what it gives and one switch.
3. **The menu bar** gives the brief and what's to answer without opening the app.

## Settings

1. **Sections:** What Isoform sees, Closed Eyes, Your data, Your agents, Your phone, Your watch, Briefs and notifications, About. Settings opens on What Isoform sees.
2. **What Isoform sees:** "Every source, on one page. Off means nothing from it is kept." One row each, with a line saying what it is and a switch:
   - Screen: "What's on screen, with the apps you exclude left out".
   - Calls and meetings: "Recorded calls on this computer, transcribed here".
   - Mail and calendar: "Your accounts on this computer".
   - Messages (new): "Your iMessage and text conversations with people you know, on this computer".
   - Your watch: "Talk it hears while on your wrist, by the hour".
   - Your agents' history: "What connected agents did, per agent".
3. **Each source opens to its details:** excluded apps for Screen, the accounts for Mail, each agent's switch for Your agents' history (computer-agents rule 14), and Forget for everything from it.
4. **Messages, off until turned on.** Turning it on says once: "Isoform reads your conversations with people you know, to know who's waiting on you and what you promised. It stays on this computer. It needs Full Disk Access, which you give in System Settings." with Open System Settings and Not now. While it's off, the row's hint says "Needs Full Disk Access · asked when you turn it on"; turned on without access, it says "Needs Full Disk Access", in amber, until access is given. It reads only; Isoform never sends a message (a reply is a draft for your Yes). observation-sources-plan item 11 as written (C5).
5. **Your agents** (was Thinks with): each agent with its job, connected or not, Disconnect, Change its job, and Learn from it; then Connect an agent (computer-agents rules 11 to 14). "Isoform passes them the task and What it gets, nothing else." (thinks.note stands.)
6. **Briefs and notifications** (was Speaks up): the brief times, and the three kinds of notification (redesign v1.0.0 rule 22).
7. **Your phone and Your watch:** paired or not, Pair a phone, and the watch's switches (phone-settings v1.0.0's watch rows, shown here too).

## The menu bar

8. **Its popover, ranked like the app:** at the top, the mark, "Isoform", its state ("seeing · watch listening"), and the **Closed Eyes** button, first (redesign v1.0.0 rule 27); then the latest brief with Hear it; then "4 to answer · 1 back from Codex" with Open Today; then Quick chat with its shortcut.
9. **The mark in the menu bar** shows the same pose as the sidebar's watch line; Closed Eyes shows it closed.

## What goes

10. Settings opening on an overview; Thinks with and Speaks up as names.

## What the build needs

C4 (agents' history) and C5 (Messages) from computer-map; the rest is redesign-shell's.

## Every word

| Key | English | Where |
| --- | --- | --- |
| sees.title | What Isoform sees | Section |
| sees.sub | Every source, on one page. Off means nothing from it is kept. | Section |
| sees.messages | Messages | Row |
| sees.messagesSub | Your iMessage and text conversations with people you know, on this computer | Row |
| sees.messagesAsk | Isoform reads your conversations with people you know, to know who's waiting on you and what you promised. It stays on this computer. It needs Full Disk Access, which you give in System Settings. | When turned on |
| sees.openSystem | Open System Settings | When turned on |
| sees.needsFda | Needs Full Disk Access | Row, on without access |
| sees.needsFdaOff | Needs Full Disk Access · asked when you turn it on | Row, off |
| sees.agentsHistory | Your agents' history | Row |
| sees.agentsHistorySub | What connected agents did, per agent | Row |
| sees.watchSub | Talk it hears while on your wrist, by the hour | Row |
| settings.yourAgents | Your agents | Section |
| settings.briefs | Briefs and notifications | Section |
| menu.state | seeing · watch listening | Popover |
| menu.toAnswer | {n} to answer · {m} back from {agent} | Popover |

## Proof

On Bo's computer, a named desktop build. **Before:** a screenshot of main's Settings.

1. **One page of sources:** Settings opens on What Isoform sees with six rows.
2. **Messages:** turned on, it asks once and opens System Settings; with access given, a conversation with someone Bo knows shows as a row in Activity; turned off, its rows are gone.
3. **Closed Eyes first:** the menu bar popover's first control is Closed Eyes; pressing it closes the mark on the computer, the phone and the watch.

## On the map (shell)

- ST1 · Settings opens on What Isoform sees: six sources, one switch each.
- ST2 · Messages as a source on the computer: people you know only, Full Disk Access, off until turned on.
- ST3 · Your agents replaces Thinks with; Briefs and notifications replaces Speaks up.
- ST4 · The menu bar popover: Closed Eyes first, then the brief, what's to answer, Quick chat.
