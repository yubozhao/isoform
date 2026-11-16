# Phone agent v1.2.1: With your agents in every state, a hand-off opened, and Take it back; and connecting an app agent on the phone

Version 1.2.1 · written November 15, 2026, 9:23 PM Pacific, by Claude Design.

**This file adds drawings and words to phone-agent v1.2.0; everything in it stands.** v1.2.0 drew the hand-off by kind; agents-join v1.0.0 §7 set the status words; agents-join v1.1.0 set how each app connects. This draws what the phone shows for each, so the Today builder has every state.

**Board:** Isoform iPhone, new artboards 15 (Connecting an app agent, on the phone) and 16 (With your agents, every state).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Every row says exactly what Isoform knows,** with the time.
2. **Taking work back is one tap and says what it can't undo.**

## With your agents (artboard 16, frame 1)

1. **One row per hand-off:** the agent's badge, the title, and its state in words with the time:
   - "{agent} · working, on your computer", with the green dot, only for an agent Isoform starts on the computer while it runs;
   - "Waiting for your computer · it's asleep", in amber;
   - "{agent} has it · since {time}";
   - "Opened in {app} · {time}";
   - "Written in Messages · {time}";
   - "No word from {agent} since {time}", in amber, after two hours.
2. **Rows are ordered by what needs you:** no word first, then waiting for your computer, then the rest, newest first.

## A hand-off, opened (frame 2)

3. **Tapping a row opens it as a sheet:** the title and its state; one sentence of what Isoform knows, in order and with times ("You said yes at 4:10 PM. It opened in Claude with the task written in; Claude read it at 4:12 PM. Nothing has come back since."); What went (opens the exact list), Sent, and has it, with times; **Ask again** and **Take it back**; and "Ask again opens {agent} with the same task. Take it back stops {agent} reading it; anything it already made stays with {agent}."

## Taken back (frame 3)

4. **One line at the top** for five seconds: "Taken back. {agent} can't open it any more; anything it already made stays with {agent}." with Undo.
5. **The work returns to Your work as offered,** at the top, its grey box saying "Taken back from {agent} at {time}. Hand it to someone else, or keep it for later."; Yes opens the hand-off sheet again (Change agent there).

## Connecting an app agent, on the phone (artboard 15)

6. **The same three steps as the computer** (agents-join v1.1.0 §2): Copy this address; Add it in {app}, with that app's own path; Check the code; then "Waiting for {app} · works once, for 10 minutes".
7. **An app that can't connect says so first** (Muse today): "{app} can't connect to Isoform yet. It gets each task written in, and sends results back through a link." / "You'll see exactly what goes before it's written in, and you press send in {app}." / **Add {app}**.

## Every word

| Key | English | Where |
| --- | --- | --- |
| with.working | {agent} · working, on your computer | Row |
| with.waitingComputer | Waiting for your computer · it's asleep | Row |
| with.hasIt | {agent} has it · since {time} | Row |
| with.openedIn | Opened in {app} · {time} | Row |
| with.written | Written in Messages · {time} | Row |
| with.noWord | No word from {agent} since {time} | Row |
| with.story | You said yes at {time}. {how it went}. {what came back}. | Opened sheet |
| with.whatWent | What went | Opened sheet |
| with.askAgain | Ask again | Opened sheet |
| with.takeBack | Take it back | Opened sheet |
| with.takeBackNote | Ask again opens {agent} with the same task. Take it back stops {agent} reading it; anything it already made stays with {agent}. | Opened sheet |
| with.takenBack | Taken back. {agent} can't open it any more; anything it already made stays with {agent}. | Top line |
| with.takenBackItem | Taken back from {agent} at {time}. Hand it to someone else, or keep it for later. | Item's grey box |
| connect.mayNot | You'll see exactly what goes before it's written in, and you press send in {app}. | Can't-connect sheet |

## Proof

On Bo's iPhone, a named TestFlight build, with his computer:

1. **Every state:** one screenshot of With your agents showing at least four of the six states, against frame 1.
2. **Take it back:** a hand-off to Claude taken back from the phone; the agent's next read is refused (a log); the item is offered again at the top of Your work.

## On the map (phone)

- P261 · With your agents on the phone: six states in words with times, ordered by what needs you; a hand-off opened with its story, Ask again and Take it back; taken back returns the work as offered.
- P262 · Connecting an app agent on the phone: the app's three steps, or "can't connect yet" first.
