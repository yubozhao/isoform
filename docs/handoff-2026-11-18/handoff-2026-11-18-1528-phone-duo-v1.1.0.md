# Phone Duo v1.1.0: What Isoform sees opens from Settings › Your data, in both layouts, and nowhere else

Version 1.1.0 · written November 18, 2026, 3:28 PM Pacific, by Claude Design.

**This file answers the hub's follow-up on phone-duo v1.0.0 rule 13** (the mark lives only in the Watch item; the hub called it rule 12). On the phone, the mark in Chat's title is a button that opens "What Isoform sees". **It changes iphone-issues v1.0.0 rule 5 and phone-settings v1.0.0's row words where it says so; everything else in phone-duo v1.0.0 and phone-settings v1.0.0 stands.**

A change goes in a new file with a new version; this one isn't edited after it's written.

**Read** (read-only), on `claude/watch-tab-in-the-wide-layout` at 804421684: `IsoformSeesView.swift`, `ChatTabView.swift`, `SettingsView.swift`, `YourDataView.swift` and the English strings.

## The rule

1. **What Isoform sees opens from one place in both layouts: Settings › Your data, as the first row under "Kept on your computer".**
   - This is where phone-settings v1.0.0 put it (rule 14, and rule 17: "What Isoform sees as a separate row: it's Your data › Kept on your computer"). The build already has the row; it calls the same page.
   - It's the phone's copy of the computer's first Settings section (computer-settings v1.0.0 rule 1). It's read rarely and it's about the computer, so it belongs with what's kept on the computer, not on a tab.
2. **The row says what it opens:** "What Isoform sees" · "Each source on your computer, on or off". This replaces "Screen, calls and your watch" · "In Activity on your computer". That page lists every source, not three, and it isn't in Activity. A source is one thing everywhere (computer-data v1.0.0 rule 2). These are small wording choices, mine.
3. **It opens as a page inside Settings,** with "Your data" as its back button, not as a sheet over the Settings sheet. On the open Duo, Settings is the person button's sheet like every other row, so this one path works the same in both layouts.
4. **Chat's mark button goes, in both layouts** (phone-duo v1.0.0 rule 13), and with it the narrow layout's hold-out. Chat's title is "Chat". This changes iphone-issues v1.0.0 rule 5.
5. **The mark's blink while Isoform works moves into words where the reply will appear.** "Writing the reply to Priya…" sits at the end of the conversation, only while it's true, in both layouts. It isn't under the title, because the title isn't over the conversation on the open Duo.

## The same page's words

While checking where it opens, I found that four of the page's lines break data-leaves v1.1.0 rule 6. "Stays on" and "never leaves" aren't said about anything a model reads, and where the assistant reads a source, the sentence says so. They change before this page ships in either layout:

| Key | Now | English |
| --- | --- | --- |
| sees.screen.on | Sees the app and window you're in. Screenshots stay on your computer. | Sees the app and window you're in. It keeps what it sees on your computer, and your AI reads it to write your briefs. |
| sees.leaves.never → sees.leaves.kept | Screenshots and audio never leave your computer. | Screenshots and audio are kept on your computer; your AI reads what's in them to write your briefs and answers. |
| sees.leaves.ai.claude | The text a task needs, to your AI, Claude Code. | What each brief or answer is written from, to your AI, Claude Code. |
| sees.leaves.ai.codex | The text a task needs, to your AI, Codex. | What each brief or answer is written from, to your AI, Codex. |
| sees.tap.line | Isoform doesn't send, delete or accept anything until you tap. | Isoform doesn't send, delete or accept anything until you tap. The one exception is Agent of You with its review off. (The second sentence shows only while that's so.) |
| sees.foot | Change these in Isoform on your computer › Settings › Privacy. | Change these on your computer, in Settings › What Isoform sees. |
| data.seen | Screen, calls and your watch | What Isoform sees |
| data.seenSub | In Activity on your computer | Each source on your computer, on or off |

The assistant rows follow computer-data v1.0.0's words ("What each brief or answer is written from"). The foot names the computer's section by its name (computer-settings v1.0.0).

## Proof

On Bo's open Duo and a narrow iPhone, on a named TestFlight build:

1. **One path:** person button › Your data › What Isoform sees opens the page with "Your data" as its back button. Stills in both layouts.
2. **No button in Chat:** Chat's title is the word alone in both layouts.
3. **Working words:** a still of "Writing the reply…" at the end of the conversation while a reply is being written.
4. **The words:** a still of the page against the table above.

## On the map (phone)

- P278 · What Isoform sees opens only from Settings › Your data, first row ("What Isoform sees" · "Each source on your computer, on or off"), as a page inside Settings in both layouts; Chat's mark button goes; the working line sits where the reply will appear.
- P279 · What Isoform sees says what the AI reads: no "stay on" or "never leave" for what it reads, the assistant's row in computer-data's words, the tap line's one exception, and the foot naming the computer's section.
