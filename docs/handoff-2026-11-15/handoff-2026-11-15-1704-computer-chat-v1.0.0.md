# Computer chat v1.0.0: Chat narrowed the same way as the phone, one thread, and Quick chat over any app

Version 1.0.0 · written November 15, 2026, 5:04 PM Pacific, by Claude Design.

**Part of the computer pass of November 15.** Read computer-map v1.0.0 first. phone-chat v1.0.0 holds on the computer, rule for rule, except where this file says so. This file replaces redesign-work v1.0.0's Chat (rules 8 to 13) and Quick chat (rules 14 to 16).

**Board:** Isoform Computer, artboard 6 (Chat and Quick chat).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Asking about your own day** gets an answer that says where it came from.
2. **A correction** takes effect at once and says what changed, with Undo.
3. **Quick chat** is one keystroke from any app.
4. **Work asked for in Chat** becomes a hand-off, never chat work.

## The page

1. **Title "Chat"; under it:** "About your own day, and setting Isoform straight." (phone-chat rule 1).
2. **One thread,** newest at the bottom, the field at the foot: "Ask about your day, or correct Isoform", with the microphone. No list of chats, no New chat, no Pinned, no groups (computer-map rule 7). ⌘K finds an old exchange.
3. **Answers, corrections and replies attached** as phone-chat rules 4 to 11: the source chip under an answer ("From your watch · Pricing with Sam, 2:05 PM ›"); claims underlined, opening their Why; "Changed: Prep for Lena · Undo" under a correction; "About: Prep for Lena" over a message sent from an item, a brief sentence, a topic or a conversation.
4. **Work asked for:** "That's work for your agent. Here's what would go:" and a card with the title, who it's for and what it gets, and **Hand it to Claude** and No (phone-chat rule 12). Hand it to opens the hand-off sheet (computer-today rules 6 and 7); nothing runs from the card itself.
5. **No general-assistant work** (phone-chat rule 13). On the computer that includes code: "Fix this bug" is work for the code agent, with the repo it's in as What it gets.
6. **Chat never speaks first** (phone-chat rule 3).

## Quick chat

7. **Its shortcut opens it over any app** (the person's global shortcut, which the build already has; the board shows ⌥Space), and the menu bar opens it too.
8. **What's on screen is offered as a chip:** "About: what's on screen · Mail, Priya Shah". It goes with the question only while the chip is there; × or ⌫ removes it. With Screen off in What Isoform sees, or with Closed Eyes on, there's no chip.
9. **Ready work first:** when what you typed matches work that's made, the item comes first, with Open and Open in Chat (redesign-work rule 15 stands).
10. **Otherwise it answers in place,** with the same rules as Chat, and **Open in Chat ›**: the exchange is already in the thread. Esc closes; ↩ sends.
11. **Asking for work from Quick chat** shows the same card, and Hand it to opens the sheet over the app you're in.

## What goes

12. The chat session list, New chat, Pinned and Custom Groups (main's sidebar), and the composer's "Ask, or give Isoform a task" (redesign-work rule 13).
13. Chat's "Isoform can do this" offer with Yes runs it in Tasks (redesign-work rule 9): the offer is now the hand-off card.

## What the build needs

B10 (chat narrowing) and B3 (answers from any device) from phone-map; the hand-off from B4; the screen chip from what the screen source already reads.

## Every word

phone-chat's keys stand (chat.*). New here:

| Key | English | Where |
| --- | --- | --- |
| qchat.aboutScreen | About: what's on screen · {app}, {title} | Quick chat chip |
| qchat.openInChat | Open in Chat › | Under an answer |
| qchat.placeholder | Ask about your day, or correct Isoform | Quick chat field |

## Proof

On Bo's computer, a named desktop build. **Before:** a screenshot of main's Chat with its session list and New chat.

1. **One thread:** the sidebar has Chat and nothing under it; yesterday's exchange is found with ⌘K.
2. **Asked and sourced:** "What did Sam and I agree on at lunch?" answers with the watch conversation's chip.
3. **Quick chat over Mail:** the shortcut opens it with the Mail chip; removing the chip sends the question without it (the request, logged, has no screen context).
4. **Work, not chat:** "Draft the Q4 plan outline for Monday." gets the hand-off card, not an outline.

## On the map (shell)

- CH1 · Chat is one thread: no session list, New chat, Pinned or groups; ⌘K finds old exchanges.
- CH2 · Quick chat over any app: the screen chip goes only while it's there; ready work first; Open in Chat.
- CH3 · Work asked for in Chat or Quick chat becomes the hand-off card, then the sheet.
