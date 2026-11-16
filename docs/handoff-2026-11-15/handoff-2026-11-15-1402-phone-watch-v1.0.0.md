# Phone watch v1.0.0: Watch is a tab; it says whether the watch listens, and shows what it heard as conversations

Version 1.0.0 · written November 15, 2026, 2:02 PM Pacific, by Claude Design.

**Part of the phone pass of November 15.** Read phone-map v1.0.0 first. This file is the Watch tab and a conversation's page. It replaces watch v1.2.0 §5 (the iPhone's Your watch page) and moves Today's watch rules (§6) into phone-today v1.0.0. Watch v1.2.0's other sections stand; its words for the phone now say "phone".

**From the hub:** Bo couldn't find Settings › Watch › Recordings and sync on today's TestFlight. Watch v1.1.0 is built: automatic hour-long parts, hold to stop, tap or double tap to mark, pause from the watch, conversations named per hour, and notes with follow-ups for conversations over 5 minutes. "The phone needs a first-class place for all of it."

**Board:** Isoform iPhone, artboards 5 (Watch) and 6 (A conversation).

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Is it listening, right now:** one line and the mark's pose.
2. **What it heard:** today's conversations, named, newest first, with their notes.
3. **What's yours to do from them:** follow-ups, judged here or in Your work (the same items).
4. **Where the talk is, only when it matters:** hours waiting or failed.
5. **The watch's settings.**

Nothing lower blocks anything higher: the Now card draws from the last state the phone has, at once; conversations load on their own; the where sheet never shows unless asked.

**Before:** Settings › Recordings (`feat/iphone-companion` at c1f0e8bcf) and Settings › Watch › Recordings and sync (1.0.6): a count card, Sync now, "Checked 5:12 PM", recordings with eight states.

## The tab

1. **Watch is the middle tab,** with the watch glyph, only when this phone has an Apple Watch paired (the system says so). With none, there is no tab and nothing about a watch anywhere; Settings has no watch row.
2. **Title "Watch",** the person button at the right.

### 1. Now

3. **The Now card:** the mark at 42 points in the watch's pose (watch v1.2.0 §2), a word, and one line:
   - **Listening** · "On your wrist · heard talk a minute ago" (the brow lifted, rippling only while the phone hears the watch hearing talk; still otherwise);
   - **Closed Eyes until 12:04 PM** in amber · "Nothing is heard or kept, on every device."; with "Closed Eyes on your computer" when it began there;
   - **Off for today** · "You held the mark to stop. Tap the mark on your watch to listen again." (the built stop, 79676acff);
   - **Not listening** · "After your call. Tap the mark on your watch to listen." (or "Your watch was off your wrist.");
   - **Not near this phone** · "Last heard 2:41 PM. It keeps listening and sends when it's near."
4. **What to do is always on the watch.** The phone never offers to start or stop listening: the watch's own tap is the one way, because watchOS can't be relied on to start listening from the phone.
5. **One amber line inside the card, only when it matters,** opening Where today's talk is: hours have waited for the computer 30 minutes or more ("2 hours are waiting for your computer."), or one couldn't be sent ("1 hour couldn't be sent.").

### 2. Today's hours

6. **A card of columns,** one per hour from the first hour heard to now: height by minutes of talk kept, Closed Eyes striped amber, quiet hours flat, an hour still on its way lighter (amber when it's waiting for the computer), this hour dashed. Over it, "Today" and "2 h 17 min of talk kept · 2 marks". Tapping a column scrolls to its conversations.

### 3. Conversations

7. **Today's conversations, newest first,** as the computer names them (bd24cb538, ec3941f5b): one row each with
   - the name in bold ("Pricing with Sam");
   - "2:05–2:41 PM · 36 min · You, Sam · 1 mark";
   - a dot at the left when it holds a mark;
   - tags under it: **Note** (it has one) and **1 to answer** (filled, in the accent) or **Who is Speaker 2?** (grey).
8. **Not named yet:** an hour on its way shows as "1 PM" / "18 min of talk · on its way to your computer", in grey. Under two minutes there's no name: "Talk · 1 min".
9. **Closed Eyes** is a striped row: "11:04–12:04" / "Closed Eyes · nothing kept".
10. **Earlier days** below, seven of them, each folded to its day and "4 conversations · 1 h 50 min".

### 4. Where today's talk is

11. **A sheet, opened only from the amber line** (or the Today line): the three places in a row, Watch › This phone › Computer, each with its glyph, what it holds and its state; the place holding the waiting hours ringed in the accent.
12. **Under them, one sentence:** what happens next and whether anything is the person's to do. Watch v1.2.0 rule 31's sentences stand, with "phone" for "iPhone":
    - "2 hours are on this phone, waiting for your computer. It's asleep; they go when it wakes. Nothing for you to do."
    - "1 hour couldn't reach your computer. Update Isoform on your computer, then try again." with **Try again**.
    - and the rest of rule 31.
13. "This phone keeps the audio only until your computer has it." at the bottom. No Sync now, no "Checked" time; pulling down checks at once.

### 5. Your watch

14. **At the bottom of the tab, the watch's settings:**
    - **Listen on your watch** · "The only off. Closed Eyes is for a while."
    - **Notes for long talks** · "Over 5 minutes: a note, and what you said you'd do", on by default once the build's switch becomes the person's (B8);
    - **Heart rate** · "heart rate raised" beside what was said; off until turned on;
    - **Place** · "The area, like "Mission District", never the address"; off until turned on;
    - **Your voice** › (watch-voice v1.0.0);
    - **Ways to mark and close eyes** ›: tap the mark, double tap and the Action button mark a moment; holding the mark stops for today in the current build, and becomes Closed Eyes when watch v1.2.0 ships (the page shows what the watch on the wrist does).

### 6. Before Isoform is on the watch

15. **One card:** the mark at 64 points, "Put Isoform on your watch", "Your Apple Watch is paired with this phone. Isoform isn't on it yet.", **Install on your watch** (opens the system's Watch app at Isoform), and three lines on what it does. Nothing here asks for the microphone; the watch asks on its own first open.

## A conversation

Artboard 6.

16. **Title: its name;** under it "Today 2:05–2:41 PM · 36 min · You, Sam · 1 mark", and "at Mission District · heart rate calm" when those are on. The back button says "Watch".
17. **Note** (over 5 minutes): four lines at most, what was decided and said ("Sam wants the annual discount at 15%, not 10%."); each line opens the words it came from (26a08f65d ties the note to its words).
18. **To answer:** what you said you'd do, as items (phone-map §4): "You said, at 2:31 PM" / "You'll send the pricing page tonight." with the drafted reply, Yes, No, Not mine. The same items as in Your work.
19. **Play,** from the start or from a mark (an accent tick on the bar; "Play from your mark, 2:31 PM"). When the audio is only on an asleep computer: "The audio is on your computer, which is asleep."
20. **What it heard:** the words with You, names and Speaker N, and "marked" beside the line where a mark fell.
21. **Who is Speaker 2?** A guess is a claim (watch-voice v1.0.0): "Speaker 2 might be Priya Shah: she was on the 2:00 invite." with Yes, No, Edit. A stranger heard once reads "Speaker 2 might be the server at Tartine." with "Only heard once, for 4 seconds."
22. **Where it is,** only while it isn't on the computer yet.
23. **Delete this conversation everywhere,** in red: "From your watch, this phone and your computer, with what was made from it." It asks once and can't be undone.
24. **Under 5 minutes:** no note; "Notes come for conversations over 5 minutes." in its place.
25. **Asking about it** is Reply in the ••• menu: Chat with the conversation attached.

## What goes

26. Settings › Recordings and Settings › Watch › Recordings and sync.
27. Sync now, "Checked {time}", the count card, the eight states, "Part 1 · 10:00 · 15:00", and the Watch or Phone source label.
28. Heart rate and Location switches on the watch's home (they're here, watch v1.2.0 rule 23).

## What the build needs

B6 (conversations for the phone, with notes, follow-ups, marks and where each is; delete everywhere), B7 (the watch's state and the computer's), B8 (notes for long talks as a person's setting), B13 (a speaker guess answerable from the phone), B2 (follow-ups as items in the one list).

## Every word

| Key | English | Where |
| --- | --- | --- |
| watch.tab | Watch | Tab |
| watch.now.listening | Listening | Now |
| watch.now.listeningSub | On your wrist · heard talk {when} | Now |
| watch.now.closed | Closed Eyes until {time} | Now |
| watch.now.closedSub | Nothing is heard or kept, on every device. | Now |
| watch.now.closedComputer | Closed Eyes on your computer | Now |
| watch.now.off | Off for today | Now |
| watch.now.offSub | You held the mark to stop. Tap the mark on your watch to listen again. | Now |
| watch.now.not | Not listening | Now |
| watch.now.notSub | {reason} Tap the mark on your watch to listen. | Now |
| watch.now.far | Not near this phone | Now |
| watch.now.farSub | Last heard {time}. It keeps listening and sends when it's near. | Now |
| watch.line.waiting | {n} hours are waiting for your computer. | Now, amber |
| watch.line.failed | {n} hour couldn't be sent. | Now, amber |
| watch.today | Today | Hours card |
| watch.today.sum | {duration} of talk kept · {n} marks | Hours card |
| watch.convs | Conversations | Section |
| watch.conv.meta | {start}–{end} · {mins} min · {speakers} · {n} mark | Row |
| watch.conv.note | Note | Tag |
| watch.conv.toAnswer | {n} to answer | Tag |
| watch.conv.who | Who is {speaker}? | Tag, section |
| watch.conv.coming | {mins} min of talk · on its way to your computer | Row, not named yet |
| watch.conv.short | Talk · {mins} min | Row, under 2 minutes |
| watch.conv.closed | Closed Eyes · nothing kept | Striped row |
| watch.earlier | Earlier days | Section |
| watch.earlier.row | {n} conversations · {duration} | Day row |
| watch.where.title | Where today's talk is | Sheet |
| watch.where.watch | Watch | Place |
| watch.where.phone | This phone | Place |
| watch.where.computer | Computer | Place |
| watch.where.keep | This phone keeps the audio only until your computer has it. | Sheet |
| watch.set.section | Your watch | Settings group |
| watch.set.notes | Notes for long talks | Setting |
| watch.set.notesSub | Over 5 minutes: a note, and what you said you'd do | Setting |
| watch.setup.title | Put Isoform on your watch | Setup card |
| watch.setup.body | Your Apple Watch is paired with this phone. Isoform isn't on it yet. | Setup card |
| watch.setup.install | Install on your watch | Button |
| conv.note | Note | Section |
| conv.noteLine | Each line opens where it was said. | Under the note |
| conv.youSaid | You said, at {time} | Item's first line |
| conv.playMark | Play from your mark, {time} | Player |
| conv.heard | What it heard | Section |
| conv.marked | marked | Beside a line |
| conv.onceHeard | Only heard once, for {secs} seconds. | Speaker guess |
| conv.noNote | Notes come for conversations over 5 minutes. | Short conversation |
| conv.where | Where it is | Row |
| conv.delete | Delete this conversation everywhere | Button |
| conv.deleteSub | From your watch, this phone and your computer, with what was made from it | Button |

Watch v1.2.0's p.* keys are replaced by these; its sentences (p.s.*) stand with "phone" for "iPhone". Plural forms follow the language.

## Proof

On Bo's iPhone and watch, named builds, against the befores above:

1. **Found without being told:** Bo opens the Watch tab from the bar and finds his lunch by its name. Bo's word.
2. **Now is true:** stills of the Now card while listening, during Closed Eyes held on the watch, after holding to stop (Off for today), and after a call.
3. **Notes and follow-ups:** a conversation over 5 minutes shows its note, and its follow-up appears both in it and in Your work; answering in one shows it answered in the other.
4. **Plumbing only when it matters:** with the computer awake all day, no amber line appears; with it asleep for an hour, the line appears after 30 minutes and the sheet rings This phone.
5. **Delete everywhere:** a deleted conversation is gone from the watch, the phone and the computer the same minute, with its note and follow-ups.

## On the map (phone)

- P228 · Watch is a tab, only when an Apple Watch is paired; Settings › Recordings and Recordings and sync go (replaces P211).
- P229 · Now: the watch's state in one word and line (Listening, Closed Eyes, Off for today, Not listening, Not near); what to do is on the watch.
- P230 · Today's hours as columns: talk, Closed Eyes striped, on its way, this hour.
- P231 · Conversations, named, with marks, Note and "{n} to answer" (replaces P212).
- P232 · Where today's talk is: a sheet, only when hours wait 30 minutes or fail (replaces P211's three places on the page).
- P233 · A conversation: note first, its follow-ups as items, play from a mark, the words, who spoke.
- P234 · Notes for long talks as the person's setting, with heart rate and place, on the Watch tab (replaces P213).
