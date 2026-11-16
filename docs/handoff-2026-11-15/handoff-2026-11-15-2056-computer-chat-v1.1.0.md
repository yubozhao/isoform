# Computer chat v1.1.0: honest while answering, couldn't answer, undo, and Quick chat's ready work

Version 1.1.0 · written November 15, 2026, 8:56 PM Pacific, by Claude Design.

**This file adds to computer-chat v1.0.0; read that first.** Everything in v1.0.0 and phone-chat v1.0.0 stands. It draws four states: while Chat answers, when it can't, Undo of a correction, and Quick chat with ready work.

**Board:** Isoform Computer, artboard 15 (Chat, Activity and You: more states), frames 15a to 15d.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Nothing is shown that Isoform isn't doing:** while it answers, it shows what it's reading.
2. **A failure keeps the question** and says what it cost.

## 15a · While it answers

1. **Chat shows "Reading" and the sources it's reading, as the chips it will cite** ("Mail · Priya, Nov 12", "Calendar · Thursday"), then the words replace them in place, with the chips under the answer. No dots, no "Thinking…".
2. **"I don't know" names what it did see:** "I didn't see anything about hiring from Priya this week. Her two emails were about moving Thursday's review." with those emails as its chip (phone-chat rule 7).

## 15b · Couldn't answer

3. **After 30 seconds without an answer, or on an error,** the reply is one bordered line: "Couldn't answer: {the service's reason, in plain words}." Under it: "Your question is kept. Nothing about your day left this computer except the question and what it needed." and **Ask again**.
4. **The question stays in the thread;** Ask again sends it again with the same sources. Isoform never retries by itself.

## 15c · Undo a correction

5. **Undo answers in the thread:** "Undone. {what} {is as it was}." ("Undone. Prep for Lena mentions the tests again.") with "Changed back: {what}". The first answer's line turns from "Changed: {what} · Undo" to "Changed: {what} · Undone".
6. **Both are kept** in Your answers: the correction as an Edit, the undo as its undo (redesign v1.0.0 rule 28).

## 15d · Quick chat, ready work first

7. **When what you typed matches work that's made,** the item comes first: "Ready · made at {time}", its title and when it's for, one line, **Open** and **Open in Chat** (redesign-work rule 15 stands).
8. **Opened over Isoform itself, Quick chat has no screen chip;** over another app it offers one (computer-chat rule 8).

## Every word

| Key | English | Where |
| --- | --- | --- |
| chat.reading | Reading | While answering |
| chat.couldnt | Couldn't answer: {reason}. | Failed reply |
| chat.kept | Your question is kept. Nothing about your day left this computer except the question and what it needed. | Failed reply |
| chat.askAgain | Ask again | Failed reply |
| chat.undone | Undone. {sentence} | Undo reply |
| chat.changedBack | Changed back: {what} | Undo reply |
| chat.changedUndone | Changed: {what} · Undone | First reply, after Undo |
| qchat.ready | Ready · made at {time} | Quick chat |
| qchat.open | Open | Quick chat |

## Proof

On Bo's computer, a named desktop build:

1. **Reading:** a recording of one answer shows the chips before the words, and the same chips under the answer.
2. **Couldn't answer:** with the network off, a question shows the failed line after 30 seconds; Ask again with the network on answers it.
3. **Undo:** a correction and its undo both appear in Activity › Your answers.

## On the map (shell)

- CH4 · While Chat answers it shows only what it's reading, as the chips it will cite; "I don't know" names what it did see.
- CH5 · Couldn't answer after 30 seconds: the reason, the question kept, what left the computer, Ask again; no retries by itself.
- CH6 · Undo of a correction answers in the thread and is kept as the Edit's undo.
